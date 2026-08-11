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
