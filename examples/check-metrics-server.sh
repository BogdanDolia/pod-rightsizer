#!/bin/bash
# Script to check if metrics-server is installed and working

set -e

echo "=== Checking for metrics-server ==="

# Check if metrics-server deployment exists
if kubectl get deployment metrics-server -n kube-system &>/dev/null; then
    echo "✓ metrics-server deployment found"
    
    # Check if it's ready
    READY=$(kubectl get deployment metrics-server -n kube-system -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
    DESIRED=$(kubectl get deployment metrics-server -n kube-system -o jsonpath='{.status.replicas}' 2>/dev/null || echo "0")
    
    if [ "$READY" = "$DESIRED" ] && [ "$READY" != "0" ]; then
        echo "✓ metrics-server is running ($READY/$DESIRED pods ready)"
    else
        echo "⚠ metrics-server found but not ready ($READY/$DESIRED)"
        echo "  Check with: kubectl get pods -n kube-system | grep metrics-server"
    fi
else
    echo "✗ metrics-server deployment not found"
    echo ""
    echo "Installation options:"
    echo ""
    echo "For standard Kubernetes (kubeadm, etc):"
    echo "  kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml"
    echo ""
    echo "For Minikube:"
    echo "  minikube addons enable metrics-server"
    echo ""
    echo "For Docker Desktop Kubernetes:"
    echo "  kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml"
    echo "  # May need to edit deployment to add --kubelet-insecure-tls flag"
    echo ""
    echo "For kind:"
    echo "  kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml"
    exit 1
fi

echo ""
echo "=== Testing Metrics API ==="

# Try to get pod metrics
if kubectl top nodes &>/dev/null; then
    echo "✓ Metrics API is working"
    echo ""
    echo "Sample output:"
    kubectl top nodes | head -3
else
    echo "✗ Metrics API is not responding"
    echo ""
    echo "Troubleshooting:"
    echo "  1. Check metrics-server logs:"
    echo "     kubectl logs -n kube-system deployment/metrics-server"
    echo ""
    echo "  2. Check if metrics-server can reach kubelet:"
    echo "     kubectl describe pod -n kube-system -l k8s-app=metrics-server"
    echo ""
    echo "  3. For local clusters, you may need to add --kubelet-insecure-tls flag"
    exit 1
fi

echo ""
echo "=== Testing pod metrics ==="

# Wait a moment for metrics to be available
sleep 2

if kubectl top pods -A 2>/dev/null | head -3; then
    echo ""
    echo "✓ Pod metrics are available"
else
    echo "⚠ Pod metrics not yet available (may take a few seconds after deployment)"
fi

echo ""
echo "=== Advisor will be able to collect metrics ✓ ==="

