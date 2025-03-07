# Pod Rightsizer

A CLI tool to automatically determine optimal CPU and memory requests/limits for Kubernetes pods by performing load tests and analyzing resource metrics.

## Features

- **Load Testing**: Powerful HTTP load testing engine with RPS and concurrency modes
- **Kubernetes Integration**: Connects to your cluster in-cluster or via kubeconfig
- **Metrics Collection**: Gathers CPU and memory metrics during load tests
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
./pod-rightsizer --target http://localhost:8080 --service-name nginx --namespace default --duration 1m --rps 500
```

### Advanced Usage

```bash
# Using all options
./pod-rightsizer \
  --target http://localhost:8080 \
  --service-name myservice \
  --namespace default \
  --duration 5m \
  --rps 50 \
  --margin 30 \
  --output-format yaml \
  --kubeconfig ~/.kube/config
```

### Parameters

- `--target`: Target service URL or identifier for load testing (required)
- `--service-name`: Kubernetes service name for metrics collection (defaults to target if not specified)
- `--namespace`: Kubernetes namespace (default: "default")
- `--duration`: Duration of the load test (default: "5m")
- `--rps`: Requests per second for load testing (default: 50)
- `--concurrency`: Alternative to RPS, number of concurrent connections (default: 0)
- `--margin`: Safety margin percentage to add to recommendations (default: 20)
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

# Step 2: Run pod-rightsizer with separate target and service-name
./pod-rightsizer \
  --target http://localhost:8080 \
  --service-name myservice \
  --namespace default \
  --duration 2m \
  --rps 50
```

### Remote Cluster Testing

Test services in a remote cluster using kubeconfig:

```bash
./pod-rightsizer \
  --target http://service-ingress.example.com \
  --service-name internal-service \
  --namespace production \
  --kubeconfig ~/.kube/production-config
```

## Example Output

### Text Output (default)

```
===== Pod Rightsizer Results =====

Load Test Target: http://localhost:8080
Service Name: myservice
Namespace: default
Load test: 50 RPS for 5m0s

Current Settings:
CPU Request: 100m
CPU Limit: 200m
Memory Request: 128Mi
Memory Limit: 256Mi

Metrics Collected:
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
  name: myservice
spec:
  template:
    spec:
      containers:
      - name: app
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
- Check that the service has the appropriate Kubernetes labels for selection
- Verify the metrics server is running in your cluster
- Increase verbosity by redirecting stderr to a file for detailed error messages

## License

MIT