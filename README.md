# Pod Rightsizer

A CLI tool to automatically determine optimal CPU and memory requests/limits for Kubernetes pods by performing load tests and analyzing resource metrics.

## Features

- Performs load testing on target services using Vegeta
- Collects resource metrics during load tests
- Analyzes metrics to recommend optimal resource settings
- Kubernetes integration for in-cluster and external usage
- Generates YAML patches for easy application

## Installation

```bash
# Clone the repository
git clone https://github.com/BogdanDolia/pod-rightsizer.git
cd pod-rightsizer

# Build the binary
go build -o pod-rightsizer
```

## Usage

```bash
# Basic usage
./pod-rightsizer --target http://myservice:8080 --namespace default --duration 5m --rps 50

# Using all options
./pod-rightsizer \
  --target http://myservice:8080 \
  --namespace default \
  --duration 5m \
  --rps 50 \
  --margin 30 \
  --output-format yaml \
  --kubeconfig ~/.kube/config
```

### Parameters

- `--target`: Target service URL or identifier (required)
- `--namespace`: Kubernetes namespace (default: "default")
- `--duration`: Duration of the load test (default: "5m")
- `--rps`: Requests per second for load testing (default: 50)
- `--concurrency`: Alternative to RPS, number of concurrent connections (default: 0)
- `--margin`: Safety margin percentage to add to recommendations (default: 20)
- `--output-format`: Output format: text, json, or yaml (default: "text")
- `--kubeconfig`: Path to kubeconfig file for external cluster access

## Example Output

```
===== Pod Rightsizer Results =====

Target: http://myservice:8080
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

## License

MIT