// Package metrics is the engine-neutral contract for resource samples.
// A sample keeps the Metrics API source time and the local poll time as
// separate clocks. Missing coverage is not reported as a complete census.
package metrics

import (
	"context"
	"time"

	"github.com/HelixThinker/pod-rightsizer/pkg/kubernetes"
	"github.com/HelixThinker/pod-rightsizer/pkg/metrics"
)

// Sample is one resource observation. Timestamp and SourceTimestamp are the
// source envelope end. PolledAt is when the collector asked for the snapshot.
// CPUm is millicores. MemoryMi is mebibytes. CPU and memory maxima may come
// from different pods.
type Sample struct {
	Timestamp       time.Time                      `json:"timestamp"`
	SourceTimestamp time.Time                      `json:"sourceTimestamp"`
	SourceWindow    time.Duration                  `json:"sourceWindow"`
	PolledAt        time.Time                      `json:"polledAt"`
	ContainerName   string                         `json:"containerName"`
	CPUm            float64                        `json:"cpu_m"`
	MemoryMi        float64                        `json:"memory_mi"`
	Pods            []kubernetes.PodResourceUsage  `json:"pods,omitempty"`
	Coverage        kubernetes.Coverage            `json:"coverage"`
	Workload        kubernetes.WorkloadObservation `json:"workload"`
	CPUSourcePod    string                         `json:"cpuSourcePod,omitempty"`
	MemorySourcePod string                         `json:"memorySourcePod,omitempty"`
	Scope           metrics.Scope                  `json:"scope"`
}

// Collection is the samples obtained so far. Partial is true when the
// collection stopped because of an error or cancellation. Those samples are
// facts, not a complete successful collection.
type Collection struct {
	Samples []Sample
	Partial bool
}

// MetricsProvider collects resource usage samples.
// The since parameter is the observation window to collect fresh samples for.
type MetricsProvider interface {
	Collect(ctx context.Context, namespace, deployment, container string, since time.Duration) (Collection, error)
}
