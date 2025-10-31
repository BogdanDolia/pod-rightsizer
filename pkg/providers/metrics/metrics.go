package metrics

import (
	"context"
	"time"
)

// Sample represents a single resource usage observation.
type Sample struct {
	Timestamp time.Time `json:"timestamp"`
	CPUm      float64   `json:"cpu_m"`     // CPU cores in millicores (m)
	MemoryMi  float64   `json:"memory_mi"` // Memory in MiB
}

// MetricsProvider defines an interface for collecting resource usage samples.
// The since parameter represents the observation window to collect fresh samples for.
type MetricsProvider interface {
	Collect(ctx context.Context, namespace, deployment string, since time.Duration) ([]Sample, error)
}
