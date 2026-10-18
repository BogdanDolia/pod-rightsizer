package k8s

import (
	"context"
	"time"

	corek8s "github.com/HelixThinker/pod-rightsizer/pkg/kubernetes"
	providermetrics "github.com/HelixThinker/pod-rightsizer/pkg/providers/metrics"
)

// Provider implements MetricsProvider using Kubernetes Metrics API.
type Provider struct {
	client kubernetesClient
	// samplingInterval controls how often to query the metrics server within the window.
	samplingInterval time.Duration
}

type kubernetesClient interface {
	ResolveWorkload(
		ctx context.Context,
		namespace, deploymentName, containerName string,
	) (corek8s.Workload, error)
	GetPodMetrics(
		ctx context.Context,
		namespace string,
		workload corek8s.Workload,
	) (corek8s.ContainerMetrics, error)
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
// A poll error or cancellation returns the samples already read, with Partial
// set, together with the error. A first poll that produced no facts returns
// no samples.
func (p *Provider) Collect(ctx context.Context, namespace, deployment, container string, since time.Duration) (providermetrics.Collection, error) {
	if since <= 0 {
		since = 30 * time.Second
	}
	interval := p.samplingInterval

	var samples []providermetrics.Sample
	workload, err := p.client.ResolveWorkload(ctx, namespace, deployment, container)
	if err != nil {
		return providermetrics.Collection{}, err
	}

	deadline := time.Now().Add(since)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		snapshot, err := p.client.GetPodMetrics(ctx, namespace, workload)
		if err != nil {
			if sample, ok := sampleFromSnapshot(snapshot); ok {
				samples = append(samples, sample)
			}
			return providermetrics.Collection{
				Samples: samples,
				Partial: len(samples) > 0,
			}, err
		}
		if sample, ok := sampleFromSnapshot(snapshot); ok {
			samples = append(samples, sample)
		}

		if time.Now().After(deadline) {
			break
		}

		select {
		case <-ctx.Done():
			return providermetrics.Collection{Samples: samples, Partial: len(samples) > 0}, ctx.Err()
		case <-ticker.C:
		}
	}

	return providermetrics.Collection{Samples: samples, Partial: false}, nil
}

func sampleFromSnapshot(snapshot corek8s.ContainerMetrics) (providermetrics.Sample, bool) {
	if len(snapshot.Pods) == 0 && snapshot.Timestamp.IsZero() {
		return providermetrics.Sample{}, false
	}
	return providermetrics.Sample{
		Timestamp:       snapshot.Timestamp,
		SourceTimestamp: snapshot.Timestamp,
		SourceWindow:    snapshot.Window,
		PolledAt:        snapshot.PolledAt,
		ContainerName:   snapshot.ContainerName,
		CPUm:            snapshot.CPUUsage * 1000.0,
		MemoryMi:        snapshot.MemoryUsage,
		Pods:            snapshot.Pods,
		Coverage:        snapshot.Coverage,
		Workload:        snapshot.Workload,
		CPUSourcePod:    snapshot.CPUSourcePod,
		MemorySourcePod: snapshot.MemorySourcePod,
	}, true
}
