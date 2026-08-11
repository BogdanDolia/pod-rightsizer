package metrics

import (
	"strings"
	"testing"
	"time"
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

func TestFullyContainedSamplesRejectsInvalidInterval(t *testing.T) {
	instant := time.Now()
	_, err := FullyContainedSamples(nil, instant, instant)
	if err == nil || !strings.Contains(err.Error(), "measurement interval") {
		t.Fatalf("FullyContainedSamples() error = %v, want interval validation", err)
	}
}
