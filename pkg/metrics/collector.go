package metrics

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/HelixThinker/pod-rightsizer/pkg/kubernetes"
)

// ResourceMetrics is one collected source snapshot. CPUUsage and MemoryUsage
// stay in cores and mebibytes, the units recommender-core-1 already consumes.
// PolledAt is the local poll clock. Timestamp and Window stay on the Metrics
// API source clock.
type ResourceMetrics struct {
	ContainerName   string
	Timestamp       time.Time
	Window          time.Duration
	CPUUsage        float64 // in cores
	MemoryUsage     float64 // in Mi
	PolledAt        time.Time
	Pods            []kubernetes.PodResourceUsage
	Coverage        kubernetes.Coverage
	Workload        kubernetes.WorkloadObservation
	CPUSourcePod    string
	MemorySourcePod string
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
	collected := resourceMetricsFromSnapshot(snapshot)
	if err != nil {
		if !resourceMetricsHasFacts(collected) {
			return ResourceMetrics{}, err
		}
		return collected, err
	}
	return collected, nil
}

func resourceMetricsFromSnapshot(snapshot kubernetes.ContainerMetrics) ResourceMetrics {
	return ResourceMetrics{
		ContainerName:   snapshot.ContainerName,
		Timestamp:       snapshot.Timestamp,
		Window:          snapshot.Window,
		CPUUsage:        snapshot.CPUUsage,
		MemoryUsage:     snapshot.MemoryUsage,
		PolledAt:        snapshot.PolledAt,
		Pods:            snapshot.Pods,
		Coverage:        snapshot.Coverage,
		Workload:        snapshot.Workload,
		CPUSourcePod:    snapshot.CPUSourcePod,
		MemorySourcePod: snapshot.MemorySourcePod,
	}
}

func resourceMetricsHasFacts(sample ResourceMetrics) bool {
	return len(sample.Pods) > 0 || !sample.Timestamp.IsZero()
}

const (
	// Discard reasons match the observations contract vocabulary.
	DiscardDuplicatePoll     = "duplicate_poll"
	DiscardOverlappingWindow = "overlapping_window"
	DiscardOutsideMeasured   = "outside_measured_interval"
	DiscardMissingPod        = "missing_pod"
	DispositionAccepted      = "accepted"
	DispositionDiscarded     = "discarded"
)

// ClassifiedSample is one raw observation plus why it was kept or dropped.
// Sample keeps the original resource values. Folded maxima live only on
// WindowEvidence.Accepted.
type ClassifiedSample struct {
	Sample      ResourceMetrics
	Disposition string
	Reason      string
}

// WindowEvidence separates raw observations from the derived series a
// recommendation may consume.
type WindowEvidence struct {
	Raw              []ClassifiedSample
	Accepted         []ResourceMetrics
	Diagnostic       []ClassifiedSample
	IndependentCount int
	DiscardedCount   int
}

// IndependentSamples validates source metadata, refuses to mix containers, and
// removes repeated polls of the same Metrics API snapshot. Repeated snapshots
// do not increase the amount of evidence used for a recommendation.
func IndependentSamples(samples []ResourceMetrics) ([]ResourceMetrics, error) {
	evidence, err := ClassifyIndependent(samples)
	if err != nil {
		return nil, err
	}
	return evidence.Accepted, nil
}

// ClassifyIndependent classifies samples without a measured-interval filter.
func ClassifyIndependent(samples []ResourceMetrics) (WindowEvidence, error) {
	return classifySamples(samples, time.Time{}, time.Time{}, false, nil)
}

// FullyContainedSamples returns independent source windows that fall entirely
// within a measured workload interval. Windows that overlap pre-load or
// post-load time are excluded rather than attributing unmeasured traffic to the
// recommendation. Boundary filtering happens before overlap folding.
func FullyContainedSamples(
	samples []ResourceMetrics,
	intervalStart, intervalEnd time.Time,
) ([]ResourceMetrics, error) {
	evidence, err := ClassifyWindows(samples, intervalStart, intervalEnd)
	if err != nil {
		return nil, err
	}
	return evidence.Accepted, nil
}

// ClassifyWindows records every input sample, then keeps only windows fully
// inside the measured interval and folds overlapping high-water marks.
func ClassifyWindows(
	samples []ResourceMetrics,
	intervalStart, intervalEnd time.Time,
) (WindowEvidence, error) {
	if intervalStart.IsZero() || intervalEnd.IsZero() || !intervalEnd.After(intervalStart) {
		return WindowEvidence{}, fmt.Errorf("measurement interval must have a non-zero end after its start")
	}
	return classifySamples(samples, intervalStart, intervalEnd, true, nil)
}

func classifySamples(
	samples []ResourceMetrics,
	intervalStart, intervalEnd time.Time,
	filterInterval bool,
	containment func(ResourceMetrics) (bool, string),
) (WindowEvidence, error) {
	if len(samples) == 0 {
		return WindowEvidence{}, nil
	}
	containerName := samples[0].ContainerName
	if containerName == "" {
		return WindowEvidence{}, fmt.Errorf("metrics sample has no container name")
	}

	raw := make([]ClassifiedSample, 0, len(samples))
	firstByTimestamp := make(map[time.Time]int, len(samples))
	for _, sample := range samples {
		if err := validateSample(containerName, sample); err != nil {
			return WindowEvidence{}, err
		}
		if previousIndex, ok := firstByTimestamp[sample.Timestamp]; ok {
			if !sameMeasurement(raw[previousIndex].Sample, sample) {
				return WindowEvidence{}, fmt.Errorf(
					"conflicting metrics for container %q at source timestamp %s",
					containerName,
					sample.Timestamp.Format(time.RFC3339Nano),
				)
			}
			raw = append(raw, ClassifiedSample{
				Sample:      sample,
				Disposition: DispositionDiscarded,
				Reason:      DiscardDuplicatePoll,
			})
			continue
		}
		firstByTimestamp[sample.Timestamp] = len(raw)
		raw = append(raw, ClassifiedSample{Sample: sample})
	}

	pending := make([]int, 0, len(raw))
	for i := range raw {
		if raw[i].Reason != "" {
			continue
		}
		pending = append(pending, i)
	}
	sort.SliceStable(pending, func(i, j int) bool {
		return raw[pending[i]].Sample.Timestamp.Before(raw[pending[j]].Sample.Timestamp)
	})

	for _, index := range pending {
		sample := raw[index].Sample
		if filterInterval {
			if containment != nil {
				ok, reason := containment(sample)
				if !ok {
					raw[index].Disposition = DispositionDiscarded
					raw[index].Reason = reason
					continue
				}
			} else if !windowInside(sample, intervalStart, intervalEnd) {
				raw[index].Disposition = DispositionDiscarded
				raw[index].Reason = DiscardOutsideMeasured
				continue
			}
		}
		if sample.Coverage.Assessed && !sample.Coverage.Complete {
			raw[index].Disposition = DispositionDiscarded
			raw[index].Reason = DiscardMissingPod
		}
	}

	accepted := make([]ResourceMetrics, 0, len(pending))
	for _, index := range pending {
		if raw[index].Reason != "" {
			continue
		}
		sample := raw[index].Sample
		if len(accepted) > 0 && !IsIndependentAfter(accepted[len(accepted)-1], sample) {
			raw[index].Disposition = DispositionDiscarded
			raw[index].Reason = DiscardOverlappingWindow
			foldHighWater(&accepted[len(accepted)-1], sample)
			continue
		}
		raw[index].Disposition = DispositionAccepted
		accepted = append(accepted, sample)
	}

	discarded := 0
	for _, sample := range raw {
		if sample.Disposition == DispositionDiscarded {
			discarded++
		}
	}
	return WindowEvidence{
		Raw:              raw,
		Accepted:         accepted,
		IndependentCount: len(accepted),
		DiscardedCount:   discarded,
	}, nil
}

func validateSample(containerName string, sample ResourceMetrics) error {
	if sample.ContainerName == "" {
		return fmt.Errorf("metrics sample has no container name")
	}
	if sample.ContainerName != containerName {
		return fmt.Errorf(
			"metrics contain multiple containers %q and %q; calculate each container separately",
			containerName,
			sample.ContainerName,
		)
	}
	if sample.Timestamp.IsZero() {
		return fmt.Errorf("metrics sample for container %q has no source timestamp", containerName)
	}
	if sample.Window <= 0 {
		return fmt.Errorf(
			"metrics sample for container %q has invalid source window %s",
			containerName,
			sample.Window,
		)
	}
	return nil
}

func sameMeasurement(previous, sample ResourceMetrics) bool {
	return previous.Window == sample.Window &&
		previous.CPUUsage == sample.CPUUsage &&
		previous.MemoryUsage == sample.MemoryUsage &&
		previous.CPUSourcePod == sample.CPUSourcePod &&
		previous.MemorySourcePod == sample.MemorySourcePod &&
		samePodUsages(previous.Pods, sample.Pods) &&
		sameCoverage(previous.Coverage, sample.Coverage) &&
		previous.Workload == sample.Workload
}

func samePodUsages(previous, sample []kubernetes.PodResourceUsage) bool {
	if len(previous) != len(sample) {
		return false
	}
	for i := range previous {
		if previous[i] != sample[i] {
			return false
		}
	}
	return true
}

func sameCoverage(previous, sample kubernetes.Coverage) bool {
	if previous.Assessed != sample.Assessed ||
		previous.Expected != sample.Expected ||
		previous.Observed != sample.Observed ||
		previous.Complete != sample.Complete ||
		len(previous.Missing) != len(sample.Missing) {
		return false
	}
	for i := range previous.Missing {
		if previous.Missing[i] != sample.Missing[i] {
			return false
		}
	}
	return true
}

func windowInside(sample ResourceMetrics, intervalStart, intervalEnd time.Time) bool {
	sourceStart := sample.Timestamp.Add(-sample.Window)
	return !sourceStart.Before(intervalStart) && !sample.Timestamp.After(intervalEnd)
}

func foldHighWater(previous *ResourceMetrics, sample ResourceMetrics) {
	if sample.CPUUsage > previous.CPUUsage {
		previous.CPUUsage = sample.CPUUsage
		if sample.CPUSourcePod != "" {
			previous.CPUSourcePod = sample.CPUSourcePod
		}
	}
	if sample.MemoryUsage > previous.MemoryUsage {
		previous.MemoryUsage = sample.MemoryUsage
		if sample.MemorySourcePod != "" {
			previous.MemorySourcePod = sample.MemorySourcePod
		}
	}
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
