package metrics

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/BogdanDolia/pod-rightsizer/pkg/kubernetes"
)

// ResourceMetrics represents a point-in-time metrics collection
type ResourceMetrics struct {
	ContainerName string
	Timestamp     time.Time
	Window        time.Duration
	CPUUsage      float64 // in cores
	MemoryUsage   float64 // in Mi
}

type podMetricsClient interface {
	GetPodMetrics(
		ctx context.Context,
		namespace string,
		workload kubernetes.Workload,
	) (kubernetes.ContainerMetrics, error)
}

// Collector is responsible for collecting Kubernetes pod metrics
type Collector struct {
	k8sClient podMetricsClient
	namespace string
	workload  kubernetes.Workload
}

// NewCollector creates a new metrics collector
func NewCollector(
	k8sClient podMetricsClient,
	namespace string,
	workload kubernetes.Workload,
) *Collector {
	return &Collector{
		k8sClient: k8sClient,
		namespace: namespace,
		workload:  workload,
	}
}

// CollectMetrics collects a single metrics point
func (c *Collector) CollectMetrics(ctx context.Context) (ResourceMetrics, error) {
	snapshot, err := c.k8sClient.GetPodMetrics(ctx, c.namespace, c.workload)
	if err != nil {
		return ResourceMetrics{}, err
	}

	return ResourceMetrics{
		ContainerName: snapshot.ContainerName,
		Timestamp:     snapshot.Timestamp,
		Window:        snapshot.Window,
		CPUUsage:      snapshot.CPUUsage,
		MemoryUsage:   snapshot.MemoryUsage,
	}, nil
}

// IndependentSamples validates source metadata, refuses to mix containers, and
// removes repeated polls of the same Metrics API snapshot. Repeated snapshots
// do not increase the amount of evidence used for a recommendation.
func IndependentSamples(samples []ResourceMetrics) ([]ResourceMetrics, error) {
	unique, err := validatedUniqueSamples(samples)
	if err != nil {
		return nil, err
	}
	return nonOverlappingSamples(unique), nil
}

// validatedUniqueSamples validates source metadata, refuses to mix
// containers, removes duplicate polls, and returns samples in source timestamp
// order. It deliberately does not discard overlapping windows so callers can
// apply their own measurement-boundary filter first.
func validatedUniqueSamples(samples []ResourceMetrics) ([]ResourceMetrics, error) {
	if len(samples) == 0 {
		return nil, nil
	}

	containerName := samples[0].ContainerName
	if containerName == "" {
		return nil, fmt.Errorf("metrics sample has no container name")
	}

	independent := make([]ResourceMetrics, 0, len(samples))
	seen := make(map[time.Time]ResourceMetrics, len(samples))
	for _, sample := range samples {
		if sample.ContainerName == "" {
			return nil, fmt.Errorf("metrics sample has no container name")
		}
		if sample.ContainerName != containerName {
			return nil, fmt.Errorf(
				"metrics contain multiple containers %q and %q; calculate each container separately",
				containerName,
				sample.ContainerName,
			)
		}
		if sample.Timestamp.IsZero() {
			return nil, fmt.Errorf("metrics sample for container %q has no source timestamp", containerName)
		}
		if sample.Window <= 0 {
			return nil, fmt.Errorf(
				"metrics sample for container %q has invalid source window %s",
				containerName,
				sample.Window,
			)
		}

		if previous, ok := seen[sample.Timestamp]; ok {
			if previous.Window != sample.Window ||
				previous.CPUUsage != sample.CPUUsage ||
				previous.MemoryUsage != sample.MemoryUsage {
				return nil, fmt.Errorf(
					"conflicting metrics for container %q at source timestamp %s",
					containerName,
					sample.Timestamp.Format(time.RFC3339Nano),
				)
			}
			continue
		}

		seen[sample.Timestamp] = sample
		independent = append(independent, sample)
	}

	sort.Slice(independent, func(i, j int) bool {
		return independent[i].Timestamp.Before(independent[j].Timestamp)
	})

	return independent, nil
}

// FullyContainedSamples returns independent source windows that fall entirely
// within a measured workload interval. Windows that overlap pre-load or
// post-load time are excluded rather than attributing unmeasured traffic to the
// recommendation.
func FullyContainedSamples(
	samples []ResourceMetrics,
	intervalStart, intervalEnd time.Time,
) ([]ResourceMetrics, error) {
	if intervalStart.IsZero() || intervalEnd.IsZero() || !intervalEnd.After(intervalStart) {
		return nil, fmt.Errorf("measurement interval must have a non-zero end after its start")
	}

	unique, err := validatedUniqueSamples(samples)
	if err != nil {
		return nil, err
	}
	contained := make([]ResourceMetrics, 0, len(unique))
	for _, sample := range unique {
		sourceStart := sample.Timestamp.Add(-sample.Window)
		if sourceStart.Before(intervalStart) || sample.Timestamp.After(intervalEnd) {
			continue
		}
		contained = append(contained, sample)
	}
	return nonOverlappingSamples(contained), nil
}

func nonOverlappingSamples(samples []ResourceMetrics) []ResourceMetrics {
	if len(samples) == 0 {
		return nil
	}
	nonOverlapping := make([]ResourceMetrics, 0, len(samples))
	for _, sample := range samples {
		if len(nonOverlapping) > 0 &&
			!IsIndependentAfter(nonOverlapping[len(nonOverlapping)-1], sample) {
			// The overlapping source window cannot add independent evidence, but
			// silently dropping it could hide a short CPU or memory high-water
			// mark. Conservatively retain its resource maxima in the existing
			// evidence window without increasing the sample count.
			previous := &nonOverlapping[len(nonOverlapping)-1]
			if sample.CPUUsage > previous.CPUUsage {
				previous.CPUUsage = sample.CPUUsage
			}
			if sample.MemoryUsage > previous.MemoryUsage {
				previous.MemoryUsage = sample.MemoryUsage
			}
			continue
		}
		nonOverlapping = append(nonOverlapping, sample)
	}
	return nonOverlapping
}

// IsIndependentAfter reports whether current's source measurement window does
// not overlap the previous sample. A new timestamp alone is insufficient when
// the Metrics API exposes rolling, overlapping windows.
func IsIndependentAfter(previous, current ResourceMetrics) bool {
	return !current.Timestamp.Add(-current.Window).Before(previous.Timestamp)
}

// SourceResolution returns a conservative source-reported resolution for a
// collection. Kubernetes exposes this as the measurement window on each
// PodMetrics snapshot.
func SourceResolution(samples []ResourceMetrics) time.Duration {
	var resolution time.Duration
	for _, sample := range samples {
		if sample.Window > resolution {
			resolution = sample.Window
		}
	}
	return resolution
}

// CalculateAverageMetrics calculates average metrics from a collection
func CalculateAverageMetrics(metrics []ResourceMetrics) (float64, float64) {
	if len(metrics) == 0 {
		return 0, 0
	}

	var totalCPU, totalMemory float64
	for _, m := range metrics {
		totalCPU += m.CPUUsage
		totalMemory += m.MemoryUsage
	}

	return totalCPU / float64(len(metrics)), totalMemory / float64(len(metrics))
}

// CalculatePeakMetrics finds the peak CPU and memory usage
func CalculatePeakMetrics(metrics []ResourceMetrics) (float64, float64) {
	if len(metrics) == 0 {
		return 0, 0
	}

	peakCPU := metrics[0].CPUUsage
	peakMemory := metrics[0].MemoryUsage

	for _, m := range metrics {
		if m.CPUUsage > peakCPU {
			peakCPU = m.CPUUsage
		}
		if m.MemoryUsage > peakMemory {
			peakMemory = m.MemoryUsage
		}
	}

	return peakCPU, peakMemory
}
