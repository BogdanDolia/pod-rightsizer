# Pod Rightsizer

A CLI tool to automatically determine optimal CPU and memory requests/limits for Kubernetes pods by performing load tests and analyzing resource metrics.

## Features

- **Typed Load Testing**: Returns actual RPS, HTTP error rate, p50/p95/p99 latency, status-code distribution, and a termination reason
- **SLO-Gated Recommendations**: Refuses to recommend or generate a patch when the target load, error-rate, or latency SLO is missed
- **Kubernetes Integration**: Connects to your cluster in-cluster or via kubeconfig
- **Fail-safe Metrics Collection**: Uses source timestamps/windows, ignores repeated polls, and aborts recommendations after collection errors
- **Container Isolation**: Calculates usage and recommendations only for the explicitly selected container
- **Intelligent Recommendations**: Analyzes usage patterns to suggest optimal resource settings
- **Multiple Output Formats**: Supports text, JSON, and YAML output formats
- **YAML Patch Generation**: Creates ready-to-apply Kubernetes YAML patches
- **Flexible Deployment**: Run locally or in-cluster with separate service targeting
- **Detailed Metrics**: Provides average, peak, and percentile resource utilization

## Installation

```bash
# Clone the repository
git clone https://github.com/BogdanDolia/pod-rightsizer.git
cd pod-rightsizer

# Build the binary
go build -o pod-rightsizer
```

## Usage

### Basic Usage

```bash
# Basic usage with minimal parameters
./pod-rightsizer --target http://localhost:8080 --deployment nginx --container nginx --namespace default --duration 1m --rps 500
```

### Advanced Usage

```bash
# Using all options
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
  --margin 30 \
  --min-samples 3 \
  --output-format yaml \
  --kubeconfig ~/.kube/config
```

### Parameters

- `--target`: Target service URL or identifier for load testing (required)
- `--deployment`: Kubernetes Deployment whose pods should be measured (required)
- `--container`: Container within the Deployment to measure and right-size (required)
- `--namespace`: Kubernetes namespace as a valid DNS label (default: "default")
- `--duration`: Positive duration of the load test, up to 24 hours (default: "5m")
- `--rps`: Requests per second for load testing, from 1 to 10,000 when used (default: 50)
- `--concurrency`: Alternative load mode, from 1 to 1,000 workers when used (default: 0)
- `--min-actual-rps`: Minimum measured RPS SLO; defaults to 95% of `--rps` in RPS mode and is disabled by default in concurrency mode
- `--max-http-error-rate`: Maximum transport/HTTP error rate SLO as a percentage (default: 1); HTTP `2xx` and `3xx` responses are successful
- `--max-p95-latency`: Maximum p95 request latency SLO (default: "1s")
- `--margin`: Safety margin percentage from 0 to 100 (default: 20)
- `--min-samples`: Minimum number of non-overlapping Metrics API source windows required for a recommendation (default: 3, minimum: 2)
- `--output-format`: Output format: text, json, or yaml (default: "text")
- `--kubeconfig`: Path to kubeconfig file for external cluster access

## Deployment Scenarios

### In-Cluster Usage

Run pod-rightsizer directly in your Kubernetes cluster to test internal services:

```bash
# Deploy as a pod or job in the cluster
kubectl apply -f pod-rightsizer-job.yaml

# Check the results
kubectl logs job/pod-rightsizer
```

### Local Testing with Port Forwarding

Test Kubernetes services from your local machine using port forwarding:

```bash
# Step 1: Set up port forwarding to the service
kubectl port-forward service/myservice 8080:80

# Step 2: Run pod-rightsizer with an explicit Deployment and container
./pod-rightsizer \
  --target http://localhost:8080 \
  --deployment myservice-api \
  --container api \
  --namespace default \
  --duration 2m \
  --rps 50
```

### Remote Cluster Testing

Test services in a remote cluster using kubeconfig:

```bash
./pod-rightsizer \
  --target http://service-ingress.example.com \
  --deployment internal-service-api \
  --container api \
  --namespace production \
  --kubeconfig ~/.kube/production-config
```

## Example Output

### Text Output (default)

```
===== Pod Rightsizer Results =====

Load Test Target: http://localhost:8080
Deployment: myservice-api
Container: api
Pod Selector: app.kubernetes.io/name=myservice,app.kubernetes.io/component=api
Namespace: default
Load test: 50 RPS for 5m0s

Load Test Result:
Actual RPS: 49.82 req/s
HTTP Error Rate: 0.20%
Latency p50/p95/p99: 42ms / 180ms / 310ms
Termination Reason: duration_elapsed
Status Codes:
  200: 14910
  503: 30
SLO: minimum RPS 47.50, maximum HTTP error rate 1.00%, maximum p95 1s

Current Settings:
CPU Request: 100m
CPU Limit: 200m
Memory Request: 128Mi
Memory Limit: 256Mi

Metrics Collected:
Independent Samples: 18
Source Resolution: 15s
Peak CPU: 156m
Average CPU: 87m
Peak Memory: 145Mi
Average Memory: 98Mi

Recommended Settings:
CPU Request: 105m (avg + 20%)
CPU Limit: 190m (peak + 20%)
Memory Request: 120Mi (avg + 20%)
Memory Limit: 175Mi (peak + 20%)

YAML patch generated in 'resource-patch.yaml'
```

### Resource Patch 

The generated `resource-patch.yaml` file can be directly applied to your cluster:

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
            cpu: "105m"
            memory: "120Mi"
          limits:
            cpu: "190m"
            memory: "175Mi"
```

Apply the patch with:

```bash
kubectl patch deployment myservice --patch-file resource-patch.yaml
```

## Troubleshooting

If you're having issues with connectivity or metrics collection:

- Ensure your service is running and accessible from where pod-rightsizer is running
- For local testing, verify port forwarding is working correctly
- Check that the Deployment selector matches its running pods
- Check that `--container` exactly matches a container name in the Deployment pod template
- Verify the metrics server is running in your cluster
- Ensure the load-test duration is long enough to cover at least `--min-samples` non-overlapping source windows; repeated timestamps and overlapping rolling windows do not count as new evidence
- Any load-test or Metrics API collection error invalidates the run, so no recommendation or resource patch is produced from partial data
- A completed load test must also satisfy every configured SLO. Missing the minimum actual RPS, exceeding the HTTP error-rate limit, or exceeding the p95 latency limit suppresses the recommendation and patch
- Increase verbosity by redirecting stderr to a file for detailed error messages

## Building and Pushing Docker Image

To run pod-rightsizer as a Kubernetes job, you'll need to build and push a Docker image:

```bash
# Build the Docker image
docker build -t yourusername/pod-rightsizer:latest .

# Login to Docker Hub (or your preferred registry)
docker login

# Push the image
docker push yourusername/pod-rightsizer:latest
```

## Running as a Kubernetes Job

A sample Kubernetes job definition is provided in `pod-rightsizer-job.yaml`. This includes:

1. A Job resource to run pod-rightsizer
2. A ServiceAccount with necessary permissions
3. RBAC Role and RoleBinding to allow metrics collection

To use it:

1. Edit `pod-rightsizer-job.yaml` to change the target service, namespace, and other parameters
2. Apply the YAML to your cluster:

```bash
kubectl apply -f pod-rightsizer-job.yaml
```

3. Monitor the job:

```bash
kubectl logs job/pod-rightsizer -f
```

The job will analyze the selected Deployment container and output resource recommendations, which you can then apply to that Deployment.

## Advisor API

The repository also includes an HTTP service that orchestrates an analysis and serves a minimal UI:

```bash
go run ./cmd/advisor-api
# PORT can be set via the environment; the default is 8080
```

The service uses in-cluster credentials or `$HOME/.kube/config`. Open `http://localhost:8080/` for the UI.

### API

- `POST /api/analyze` starts a run. Example body:

  ```json
  {
    "namespace": "default",
    "deployment": "my-deployment",
    "container": "app",
    "duration": "60s",
    "rps": 50,
    "concurrency": 0,
    "margin": 20,
    "targetURL": "http://localhost:8080",
    "minimumSamples": 3,
    "maximumHTTPErrorRate": 1,
    "maximumP95Latency": "1s"
  }
  ```

- `GET /api/runs/{id}` returns status, load-test evidence, recommendation, and advice.
- `GET /api/runs/{id}/yaml-patch` returns a patch for the resolved Deployment and container.
- `GET /api/runs/{id}/hpa-behavior` returns a default HPA behavior block.

When `targetURL` is set, the API applies the same load-test SLO gate as the CLI. If it is omitted, the API only samples metrics. Metrics still must contain the configured number of independent source windows.

## License

MIT
