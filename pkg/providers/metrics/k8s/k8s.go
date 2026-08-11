package k8s

import (
	"context"
	"time"

	corek8s "github.com/BogdanDolia/pod-rightsizer/pkg/kubernetes"
	providermetrics "github.com/BogdanDolia/pod-rightsizer/pkg/providers/metrics"
)

// Provider implements MetricsProvider using Kubernetes Metrics API.
type Provider struct {
	client *corek8s.Client
	// samplingInterval controls how often to query the metrics server within the window.
	samplingInterval time.Duration
}

// New returns a new Kubernetes metrics provider.
// kubeconfigPath: path to kubeconfig; if empty, in-cluster or default kubeconfig will be used by the client.
func New(kubeconfigPath string, samplingInterval time.Duration) (*Provider, error) {
	if samplingInterval <= 0 {
		samplingInterval = 5 * time.Second
	}
	c, err := corek8s.NewClient(kubeconfigPath)
	if err != nil {
		return nil, err
	}
	return &Provider{client: c, samplingInterval: samplingInterval}, nil
}

// Collect samples current resource usage over the given observation window.
func (p *Provider) Collect(ctx context.Context, namespace, deployment, container string, since time.Duration) ([]providermetrics.Sample, error) {
	if since <= 0 {
		since = 30 * time.Second
	}
	interval := p.samplingInterval

	var samples []providermetrics.Sample
	workload, err := p.client.ResolveWorkload(ctx, namespace, deployment, container)
	if err != nil {
		return nil, err
	}

	deadline := time.Now().Add(since)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		// Collect one sample immediately on first iteration
		snapshot, err := p.client.GetPodMetrics(ctx, namespace, workload)
		if err == nil {
			samples = append(samples, providermetrics.Sample{
				Timestamp: snapshot.Timestamp,
				CPUm:      snapshot.CPUUsage * 1000.0, // convert cores to millicores
				MemoryMi:  snapshot.MemoryUsage,
			})
		}

		if time.Now().After(deadline) {
			break
		}

		select {
		case <-ctx.Done():
			return samples, ctx.Err()
		case <-ticker.C:
			// loop
		}
	}

	return samples, nil
}
