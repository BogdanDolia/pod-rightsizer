package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/HelixThinker/pod-rightsizer/pkg/kubernetes"
)

func TestIndependentSamplesDeduplicatesSourceSnapshots(t *testing.T) {
	timestamp := time.Date(2026, time.August, 11, 10, 0, 0, 0, time.UTC)
	first := ResourceMetrics{
		ContainerName: "worker",
		Timestamp:     timestamp,
		Window:        30 * time.Second,
		CPUUsage:      0.1,
		MemoryUsage:   100,
	}
	second := first
	second.Timestamp = timestamp.Add(30 * time.Second)
	second.CPUUsage = 0.2
	overlapping := first
	overlapping.Timestamp = timestamp.Add(15 * time.Second)
	overlapping.CPUUsage = 0.15

	independent, err := IndependentSamples([]ResourceMetrics{second, overlapping, first, first})
	if err != nil {
		t.Fatalf("IndependentSamples() error = %v", err)
	}
	if len(independent) != 2 {
		t.Fatalf("len(IndependentSamples()) = %d, want 2", len(independent))
	}
	if !independent[0].Timestamp.Equal(first.Timestamp) ||
		!independent[1].Timestamp.Equal(second.Timestamp) {
		t.Fatalf("IndependentSamples() did not sort by source timestamp: %#v", independent)
	}
	if independent[0].CPUUsage != overlapping.CPUUsage {
		t.Fatalf(
			"overlapping CPU high-water = %v, want %v",
			independent[0].CPUUsage,
			overlapping.CPUUsage,
		)
	}
	if resolution := SourceResolution(independent); resolution != 30*time.Second {
		t.Fatalf("SourceResolution() = %s, want 30s", resolution)
	}
}

func TestIndependentSamplesRejectsConflictingDuplicate(t *testing.T) {
	timestamp := time.Date(2026, time.August, 11, 10, 0, 0, 0, time.UTC)
	first := ResourceMetrics{
		ContainerName: "worker",
		Timestamp:     timestamp,
		Window:        15 * time.Second,
		CPUUsage:      0.1,
		MemoryUsage:   100,
	}
	conflict := first
	conflict.CPUUsage = 0.2

	_, err := IndependentSamples([]ResourceMetrics{first, conflict})
	if err == nil || !strings.Contains(err.Error(), "conflicting metrics") {
		t.Fatalf("IndependentSamples() error = %v, want conflicting metrics error", err)
	}
}

func TestIndependentSamplesRequiresSourceMetadata(t *testing.T) {
	_, err := IndependentSamples([]ResourceMetrics{{ContainerName: "worker"}})
	if err == nil || !strings.Contains(err.Error(), "source timestamp") {
		t.Fatalf("IndependentSamples() error = %v, want source timestamp error", err)
	}
}

func TestFullyContainedSamplesExcludesPreAndPostLoadWindows(t *testing.T) {
	loadStart := time.Date(2026, time.August, 11, 10, 0, 0, 0, time.UTC)
	loadEnd := loadStart.Add(time.Minute)
	sample := func(end time.Time, window time.Duration, cpu float64) ResourceMetrics {
		return ResourceMetrics{
			ContainerName: "worker",
			Timestamp:     end,
			Window:        window,
			CPUUsage:      cpu,
			MemoryUsage:   cpu * 100,
		}
	}
	samples := []ResourceMetrics{
		sample(loadStart.Add(5*time.Second), 10*time.Second, 9),  // overlaps pre-load
		sample(loadStart.Add(20*time.Second), 10*time.Second, 1), // fully contained
		sample(loadStart.Add(50*time.Second), 10*time.Second, 2), // fully contained
		sample(loadEnd.Add(5*time.Second), 10*time.Second, 8),    // overlaps post-load
	}

	contained, err := FullyContainedSamples(samples, loadStart, loadEnd)
	if err != nil {
		t.Fatalf("FullyContainedSamples() error = %v", err)
	}
	if len(contained) != 2 || contained[0].CPUUsage != 1 || contained[1].CPUUsage != 2 {
		t.Fatalf("FullyContainedSamples() = %#v, want only in-load windows", contained)
	}
}

func TestFullyContainedSamplesFiltersBoundariesBeforeOverlaps(t *testing.T) {
	loadStart := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	samples := []ResourceMetrics{
		{
			ContainerName: "worker",
			Timestamp:     loadStart.Add(30 * time.Second),
			Window:        time.Minute,
			CPUUsage:      0.1,
			MemoryUsage:   100,
		},
		{
			ContainerName: "worker",
			Timestamp:     loadStart.Add(time.Minute),
			Window:        30 * time.Second,
			CPUUsage:      0.2,
			MemoryUsage:   200,
		},
	}

	contained, err := FullyContainedSamples(
		samples,
		loadStart,
		loadStart.Add(2*time.Minute),
	)
	if err != nil {
		t.Fatalf("FullyContainedSamples() error = %v", err)
	}
	if len(contained) != 1 {
		t.Fatalf("len(FullyContainedSamples()) = %d, want 1", len(contained))
	}
	if contained[0].CPUUsage != 0.2 {
		t.Fatalf(
			"FullyContainedSamples()[0].CPUUsage = %v, want fully-contained sample",
			contained[0].CPUUsage,
		)
	}
}

func TestClassifyWindowsKeepsRawValuesAndFoldsHighWater(t *testing.T) {
	timestamp := time.Date(2026, time.August, 11, 10, 0, 0, 0, time.UTC)
	first := ResourceMetrics{
		ContainerName: "worker",
		Timestamp:     timestamp,
		Window:        30 * time.Second,
		CPUUsage:      0.1,
		MemoryUsage:   100,
		CPUSourcePod:  "pod-a",
	}
	second := first
	second.Timestamp = timestamp.Add(30 * time.Second)
	second.CPUUsage = 0.2
	second.CPUSourcePod = "pod-b"
	overlapping := first
	overlapping.Timestamp = timestamp.Add(15 * time.Second)
	overlapping.CPUUsage = 0.15
	overlapping.CPUSourcePod = "pod-c"
	repeated := first
	repeated.PolledAt = timestamp.Add(time.Second)

	evidence, err := ClassifyIndependent([]ResourceMetrics{second, overlapping, first, repeated})
	if err != nil {
		t.Fatalf("ClassifyIndependent() error = %v", err)
	}
	if evidence.IndependentCount != 2 || evidence.DiscardedCount != 2 || len(evidence.Accepted) != 2 {
		t.Fatalf("evidence counts = %+v, want 2 independent and 2 discarded", evidence)
	}
	if evidence.Raw[2].Sample.CPUUsage != 0.1 || evidence.Raw[2].Disposition != DispositionAccepted {
		t.Fatalf("raw first sample = %#v, want the original 0.1 cores", evidence.Raw[2])
	}
	if evidence.Raw[3].Reason != DiscardDuplicatePoll {
		t.Fatalf("repeat reason = %q, want %s", evidence.Raw[3].Reason, DiscardDuplicatePoll)
	}
	if evidence.Accepted[0].CPUUsage != 0.15 || evidence.Accepted[0].CPUSourcePod != "pod-c" {
		t.Fatalf("folded window = %#v, want overlapping high-water from pod-c", evidence.Accepted[0])
	}
	if evidence.Raw[1].Reason != DiscardOverlappingWindow || evidence.Raw[1].Sample.CPUUsage != 0.15 {
		t.Fatalf("raw overlap = %#v, want the original overlapping sample", evidence.Raw[1])
	}
}

func TestClassifyWindowsFiltersBoundariesBeforeFold(t *testing.T) {
	loadStart := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	outside := ResourceMetrics{
		ContainerName: "worker",
		Timestamp:     loadStart.Add(30 * time.Second),
		Window:        time.Minute,
		CPUUsage:      0.9,
		MemoryUsage:   900,
		Coverage:      kubernetes.Coverage{Assessed: true, Expected: 1, Observed: 0, Complete: false, Missing: []string{"pod-b"}},
	}
	inside := ResourceMetrics{
		ContainerName: "worker",
		Timestamp:     loadStart.Add(time.Minute),
		Window:        30 * time.Second,
		CPUUsage:      0.2,
		MemoryUsage:   200,
	}
	evidence, err := ClassifyWindows(
		[]ResourceMetrics{outside, inside},
		loadStart,
		loadStart.Add(2*time.Minute),
	)
	if err != nil {
		t.Fatalf("ClassifyWindows() error = %v", err)
	}
	if evidence.Raw[0].Reason != DiscardOutsideMeasured {
		t.Fatalf("outside reason = %q, want %s", evidence.Raw[0].Reason, DiscardOutsideMeasured)
	}
	if len(evidence.Accepted) != 1 || evidence.Accepted[0].CPUUsage != 0.2 {
		t.Fatalf("accepted = %#v, want the contained window without the outside high-water", evidence.Accepted)
	}
}

func TestClassifyWindowsMarksIncompleteCoverageAfterBoundaryFilter(t *testing.T) {
	loadStart := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	incomplete := kubernetes.Coverage{Assessed: true, Expected: 2, Observed: 1, Complete: false, Missing: []string{"pod-b"}}
	inside := ResourceMetrics{
		ContainerName: "worker",
		Timestamp:     loadStart.Add(20 * time.Second),
		Window:        10 * time.Second,
		CPUUsage:      0.2,
		MemoryUsage:   200,
		Coverage:      incomplete,
	}
	outside := inside
	outside.Timestamp = loadStart.Add(-time.Second)
	evidence, err := ClassifyWindows(
		[]ResourceMetrics{outside, inside},
		loadStart,
		loadStart.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("ClassifyWindows() error = %v", err)
	}
	if evidence.Raw[0].Reason != DiscardOutsideMeasured || evidence.Raw[1].Reason != DiscardMissingPod {
		t.Fatalf("reasons = %q, %q", evidence.Raw[0].Reason, evidence.Raw[1].Reason)
	}
	if evidence.IndependentCount != 0 || len(evidence.Accepted) != 0 {
		t.Fatalf("accepted = %#v, want incomplete coverage excluded", evidence.Accepted)
	}
}

func TestFullyContainedSamplesRejectsInvalidInterval(t *testing.T) {
	instant := time.Now()
	_, err := FullyContainedSamples(nil, instant, instant)
	if err == nil || !strings.Contains(err.Error(), "measurement interval") {
		t.Fatalf("FullyContainedSamples() error = %v, want interval validation", err)
	}
}
