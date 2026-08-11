# Pod Rightsizer

Pod Rightsizer measures one container in a Kubernetes Deployment during a load test and recommends auditable CPU and memory requests/limits.

## How recommendations are calculated

- The Deployment's real pod selector is resolved from Kubernetes; the Deployment and container must be named explicitly.
- Kubernetes Metrics API source timestamps and windows are used. Repeated polls and overlapping windows do not count as additional evidence, and only windows fully contained in the measured load-test interval are eligible.
- A Deployment rollout must be stable and its generation must remain unchanged throughout each collection. Every active selected pod must have a Metrics API entry; each snapshot uses the highest CPU and memory usage among those replicas so a missing or hot pod cannot be hidden by an average.
- CPU request is the configured CPU percentile plus a configurable request buffer. The default is `p95 + 10%`.
- Memory request is the observed high-water mark plus a configurable buffer. The default is `max + 20%`.
- CPU and memory limits have separate policies: `none`, `keep`, `request-multiplier`, or `peak-multiplier`.
- Every result includes the policy, observed statistics, confidence score with reasons, calculation explanation, and comparison with current settings.
- CLI and Advisor API recommendations are emitted only when the load test meets its configured RPS, HTTP error-rate, and p95 latency SLO.
- The resource patch is built as a structured Kubernetes object for the resolved Deployment/container and must pass Kubernetes server-side dry-run before it is emitted.

The default CPU limit policy is `none` to avoid CPU throttling. The default memory limit is `1.2 × memory request`. A zero limit in the typed recommendation is rendered as `null` in the strategic merge patch so an existing limit is removed.

## Build

Go 1.25 or newer is required.

```bash
git clone https://github.com/BogdanDolia/pod-rightsizer.git
cd pod-rightsizer
go build -o pod-rightsizer ./cmd/pod-rightsizer
```

CI checks module tidiness, formatting, `go vet`, race-enabled tests, both commands, a GoReleaser snapshot, and a kind scenario that proves `dryRun=All` leaves the target and sidecar unchanged. Tags matching `v*` publish linux amd64/arm64 archives for `pod-rightsizer` and `advisor-api`, a checksum file, and GitHub artifact attestations.

## CLI usage

```bash
./pod-rightsizer \
  --target http://localhost:8080 \
  --deployment myservice-api \
  --container api \
  --namespace default \
  --duration 5m \
  --rps 50 \
  --min-actual-rps 47.5 \
  --max-http-error-rate 1 \
  --max-p95-latency 1s \
  --cpu-percentile 95 \
  --cpu-request-buffer 10 \
  --memory-buffer 20 \
  --cpu-limit-policy none \
  --memory-limit-policy request-multiplier \
  --memory-limit-multiplier 1.2 \
  --min-samples 3 \
  --output-format text
```

### Parameters

- `--target`: load-test URL (required).
- `--deployment`: Kubernetes Deployment to measure (required).
- `--container`: container within the Deployment to measure (required).
- `--namespace`: Kubernetes namespace (default `default`).
- `--duration`: positive test duration up to 24 hours (default `5m`).
- `--rps`: requested RPS, up to 10,000 (default `50`).
- `--concurrency`: alternative fixed-worker load mode, up to 1,000 workers. It is mutually exclusive with `--rps`; set `--rps=0` when selecting this mode.
- `--min-actual-rps`: minimum measured RPS; defaults to 95% of `--rps` in RPS mode.
- `--max-http-error-rate`: maximum transport/non-2xx/non-3xx error percentage (default `1`).
- `--max-p95-latency`: maximum p95 request latency (default `1s`).
- `--cpu-percentile`: percentile used for CPU request, in `(0, 100]` (default `95`).
- `--cpu-request-buffer`: percentage added to the CPU percentile (default `10`).
- `--memory-buffer`: percentage added to the memory high-water mark (default `20`).
- `--cpu-limit-policy`: `none`, `keep`, `request-multiplier`, or `peak-multiplier` (default `none`).
- `--cpu-limit-multiplier`: CPU multiplier for a multiplier policy (default `1`).
- `--memory-limit-policy`: same policy choices for memory (default `request-multiplier`).
- `--memory-limit-multiplier`: memory multiplier for a multiplier policy (default `1.2`).
- `--min-samples`: minimum independent Metrics API windows (default `3`, minimum `2`).
- `--output-format`: `text`, `json`, or `yaml`.
- `--kubeconfig`: kubeconfig path; otherwise in-cluster/default kubeconfig resolution is used.

`request-multiplier` multiplies the recommended request. `peak-multiplier` multiplies observed peak usage, but the resulting limit is never allowed below its request. `keep` preserves the current limit unless it would fall below the new request. `none` removes the configured limit.

For `json` and `yaml`, stdout contains only the requested document; progress and status messages go to stderr. All output modes also write `resource-patch.yaml` and return a non-zero exit status if server-side dry-run or that write fails. Pod Rightsizer never applies the patch automatically.

## Result contract

The recommendation uses CPU cores and Mi internally. A shortened JSON example:

```json
{
  "cpuRequest": 0.198,
  "cpuLimit": 0,
  "memoryRequest": 174,
  "memoryLimit": 208.8,
  "policy": {
    "cpuPercentile": 95,
    "cpuRequestBufferPercent": 10,
    "memoryBufferPercent": 20,
    "cpuLimit": { "mode": "none" },
    "memoryLimit": { "mode": "request-multiplier", "multiplier": 1.2 },
    "minimumSamples": 3
  },
  "observed": {
    "independentSamples": 18,
    "observationSeconds": 270,
    "cpuPercentile": 95,
    "cpuPercentileValue": 0.18,
    "cpuPeak": 0.21,
    "memoryHighWater": 145
  },
  "confidence": {
    "level": "medium",
    "score": 0.51,
    "reasons": ["..."]
  },
  "explanation": ["..."],
  "comparison": {
    "cpuRequest": {
      "current": 0.1,
      "recommended": 0.198,
      "delta": 0.098,
      "deltaPercent": 98,
      "direction": "increase"
    }
  }
}
```

Confidence measures evidence quality, not whether the workload is safe. Independent sample count contributes 80% of the score and observation duration contributes 20%; high confidence targets 30 independent windows spanning 30 minutes. Low/medium confidence results should be reviewed against representative production traffic before applying.

## YAML patch

Text, JSON, and YAML output modes generate `resource-patch.yaml` only after the Kubernetes API accepts the strategic merge patch with `dryRun=All`. The patch is serialized from a structured Kubernetes object, not assembled with YAML string interpolation. With the default CPU limit policy, `cpu: null` removes any existing CPU limit while the memory limit is set explicitly:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  namespace: default
  name: myservice-api
spec:
  template:
    spec:
      containers:
      - name: api
        resources:
          requests:
            cpu: "198m"
            memory: "174Mi"
          limits:
            cpu: null
            memory: "209Mi"
```

Pod Rightsizer stops after writing the file. Review it and repeat server-side dry-run if time has passed or the Deployment may have changed:

```bash
kubectl patch deployment myservice-api --patch-file resource-patch.yaml --dry-run=server -o yaml

# Explicit manual action only; Pod Rightsizer never runs this command.
kubectl patch deployment myservice-api --patch-file resource-patch.yaml
```

## Deployment scenarios

For local testing, expose the target while metrics are read from the cluster:

```bash
kubectl port-forward service/myservice 8080:80

./pod-rightsizer \
  --target http://localhost:8080 \
  --deployment myservice-api \
  --container api \
  --namespace default \
  --duration 2m \
  --rps 50
```

For in-cluster execution, customize and apply [examples/pod-rightsizer-job.yaml](examples/pod-rightsizer-job.yaml).

## Advisor API

Run the HTTP service and open `http://localhost:8080/`:

```bash
go run ./cmd/advisor-api
```

Create an analysis:

```http
POST /api/analyze
Content-Type: application/json
```

```json
{
  "namespace": "default",
  "deployment": "myservice-api",
  "container": "api",
  "duration": "60s",
  "rps": 50,
  "minimumActualRPS": 47.5,
  "maximumHTTPErrorRate": 1,
  "maximumP95Latency": "1s",
  "targetURL": "http://localhost:8080",
  "policy": {
    "cpuPercentile": 95,
    "cpuRequestBufferPercent": 10,
    "memoryBufferPercent": 20,
    "cpuLimit": { "mode": "none" },
    "memoryLimit": { "mode": "request-multiplier", "multiplier": 1.2 },
    "minimumSamples": 3
  }
}
```

The response is `{ "runId": "..." }`.

- `GET /api/runs/{id}` returns status, the typed recommendation, and `patchDryRun: true` only after server-side validation succeeds.
- `GET /api/runs/{id}/yaml-patch` returns the stored, validated resource patch after completion.
- `GET /api/runs/{id}/hpa-behavior` returns the default HPA behavior example.

If `policy` is omitted, the defaults documented above are used. When `targetURL` is present, the API applies the same load-test SLO gate as the CLI; a failed SLO produces a failed run and no patch. If `targetURL` is empty, the API samples ambient workload metrics without generating load.

## Troubleshooting

- Ensure the Deployment rollout is complete, its selector matches ready pods, and `--container` exactly matches a container name.
- Verify Metrics Server access with `kubectl top pods`.
- Use a duration long enough to capture at least `--min-samples` non-overlapping source windows. Repeated timestamps do not increase confidence.
- Any Metrics API collection error invalidates the run; a partial prefix is not used.
- Only Metrics API windows fully contained in the actual load-test interval count toward `--min-samples`; increase the duration when boundary windows leave too little evidence.
- After the measured interval, the CLI and Advisor API keep polling for one source window plus one poll interval so a final, fully contained Metrics API window has time to be published. The API fails closed when this grace would exceed two minutes.
- Missed load-test SLOs suppress both recommendation and patch generation in the CLI and Advisor API.
- The caller needs `patch` permission on Deployments because Kubernetes authorizes server-side dry-run like a real patch. The code always sends `dryRun=All` and has no automatic apply path.

## License

MIT
