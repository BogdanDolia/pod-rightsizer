# Testing Advisor API with Nginx

Quick guide to test the advisor API with an nginx deployment.

## Prerequisites: Metrics Server

**⚠️ Important:** Advisor requires Kubernetes Metrics API (metrics-server) to collect CPU/Memory metrics.

Check if metrics-server is installed:
```bash
./examples/check-metrics-server.sh
```

If not installed, install it:

**Standard Kubernetes / Docker Desktop:**
```bash
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
```

**Minikube:**
```bash
minikube addons enable metrics-server
```

**For local clusters (Docker Desktop, kind):** You may need to edit the deployment to add `--kubelet-insecure-tls` flag:
```bash
kubectl edit deployment metrics-server -n kube-system
# Add --kubelet-insecure-tls to args
```

Verify it's working:
```bash
kubectl top nodes
kubectl top pods
```

## Option 1: Using the test script (macOS/Linux)

```bash
# From the project root
./examples/test-advisor.sh
```

This will:
1. Deploy nginx to your cluster
2. Set up port-forward (nginx on localhost:8081)
3. Start the advisor API (on localhost:8080)

Then open http://localhost:8080 in your browser and fill in:
- Namespace: `default`
- Deployment: `nginx-test`
- Service: `nginx-test`
- Duration: `60s`
- RPS: `50`
- Target URL: `http://localhost:8081`
- CPU percentile: `95`
- CPU request buffer: `10`
- Memory buffer: `20`

Click "Start analyze" and watch the results!

## Option 2: Manual steps (Windows/macOS/Linux)

### Step 1: Deploy nginx

```bash
kubectl apply -f examples/nginx-test.yaml
kubectl wait --for=condition=available --timeout=60s deployment/nginx-test
```

### Step 2: Port-forward nginx (in a separate terminal)

```bash
kubectl port-forward service/nginx-test 8081:80
```

Keep this running.

### Step 3: Start advisor API (in another terminal)

```bash
# From project root
go run ./cmd/advisor-api
```

Or build and run:
```bash
go build -o advisor-api ./cmd/advisor-api
./advisor-api  # On Windows: advisor-api.exe
```

### Step 4: Use the UI or API

**Via UI:**
- Open http://localhost:8080
- Fill in the form:
  - Namespace: `default`
  - Deployment: `nginx-test`
  - Container: `nginx`
  - Service: `nginx-test` (optional)
  - Duration: `60s`
  - RPS: `50`
  - Target URL: `http://localhost:8081`
  - CPU percentile: `95`
  - CPU request buffer: `10`
  - Memory buffer: `20`
- Click "Start analyze"

**Via API (curl):**

```bash
# Start analysis
RUN_ID=$(curl -s -X POST http://localhost:8080/api/analyze \
  -H "Content-Type: application/json" \
  -d '{
    "namespace": "default",
    "deployment": "nginx-test",
    "container": "nginx",
    "serviceName": "nginx-test",
    "duration": "60s",
    "rps": 50,
    "minimumActualRPS": 47.5,
    "maximumHTTPErrorRate": 1,
    "maximumP95Latency": "1s",
    "policy": {
      "cpuPercentile": 95,
      "cpuRequestBufferPercent": 10,
      "memoryBufferPercent": 20,
      "cpuLimit": {"mode": "none"},
      "memoryLimit": {"mode": "request-multiplier", "multiplier": 1.2},
      "minimumSamples": 3
    },
    "targetURL": "http://localhost:8081"
  }' | jq -r '.runId')

echo "Run ID: $RUN_ID"

# Poll for status (wait for completion)
while true; do
  STATUS=$(curl -s "http://localhost:8080/api/runs/$RUN_ID" | jq -r '.status')
  echo "Status: $STATUS"
  if [ "$STATUS" = "completed" ] || [ "$STATUS" = "failed" ]; then
    break
  fi
  sleep 2
done

# Get results
echo ""
echo "=== Recommendations ==="
curl -s "http://localhost:8080/api/runs/$RUN_ID" | jq '.recommendation'

echo ""
echo "=== Advice ==="
curl -s "http://localhost:8080/api/runs/$RUN_ID" | jq '.advice'

echo ""
echo "=== YAML Patch ==="
curl -s "http://localhost:8080/api/runs/$RUN_ID/yaml-patch"

echo ""
echo "=== Server-side dry-run status ==="
curl -s "http://localhost:8080/api/runs/$RUN_ID" | jq '.patchDryRun'

echo ""
echo "=== HPA Behavior ==="
curl -s "http://localhost:8080/api/runs/$RUN_ID/hpa-behavior"
```

### Step 5: Cleanup

```bash
kubectl delete -f examples/nginx-test.yaml
```

## Troubleshooting

- **Port 8080 already in use**: Set `PORT=8081` env var before starting advisor API
- **Cannot connect to cluster**: Check `kubectl get pods` works, verify kubeconfig
- **No metrics / "error getting pod metrics"**:
  - Run `./examples/check-metrics-server.sh` to diagnose
  - Ensure metrics-server is installed and running:
    ```bash
    kubectl get deployment metrics-server -n kube-system
    kubectl logs -n kube-system deployment/metrics-server
    ```
  - For local clusters, metrics-server may need `--kubelet-insecure-tls` flag
- **Port-forward fails**: Check if port 8081 is free, or change it in the command

## Notes

- The nginx deployment starts with minimal resources (10m CPU, 16Mi memory) to make recommendations more visible
- Load test runs against localhost:8081 (via port-forward)
- Metrics are collected from the cluster using Metrics API
- The Deployment rollout must be complete before analysis starts
- RPS and fixed-concurrency modes are mutually exclusive; set RPS to `0` when selecting concurrency mode
- Every active selected replica must be present in each Metrics API snapshot
- Only Metrics API windows fully contained in the load-test interval count as evidence
- Results include CPU/memory recommendations with the policy and buffers selected above
