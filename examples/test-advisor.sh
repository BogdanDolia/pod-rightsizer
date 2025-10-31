#!/bin/bash
# Test script for advisor API with nginx

set -e

echo "=== Step 0: Check metrics-server ==="
if ! kubectl get deployment metrics-server -n kube-system &>/dev/null; then
    echo "⚠️  WARNING: metrics-server not found!"
    echo "   Advisor needs metrics-server to collect metrics."
    echo "   Install it with:"
    echo "   kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml"
    echo ""
    read -p "Continue anyway? (y/N) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
else
    echo "✓ metrics-server found"
fi

echo ""
echo "=== Step 1: Deploy nginx ==="
kubectl apply -f examples/nginx-test.yaml

echo ""
echo "Waiting for nginx to be ready..."
kubectl wait --for=condition=available --timeout=60s deployment/nginx-test -n default || true

echo ""
echo "=== Step 2: Port-forward nginx to localhost:8081 ==="
echo "Running port-forward in background..."
kubectl port-forward service/nginx-test 8081:80 &
PF_PID=$!
sleep 2

echo ""
echo "=== Step 3: Start advisor API ==="
echo "Starting advisor API on port 8080..."
echo "Open http://localhost:8080 in your browser"
echo ""
echo "Press Ctrl+C to stop both port-forward and advisor API"
echo ""

# Trap to cleanup port-forward on exit
trap "kill $PF_PID 2>/dev/null || true" EXIT

# Start advisor API
cd "$(dirname "$0")/.."
go run ./cmd/advisor-api

