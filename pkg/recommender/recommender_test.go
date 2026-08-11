package recommender

import (
	"strings"
	"testing"
	"time"

	"github.com/BogdanDolia/pod-rightsizer/pkg/kubernetes"
	"github.com/BogdanDolia/pod-rightsizer/pkg/metrics"
)

func TestGenerateRecommendations(t *testing.T) {
	baseTime := time.Date(2026, time.August, 11, 10, 0, 0, 0, time.UTC)
	// Setup test metrics data
	testMetrics := []metrics.ResourceMetrics{
		{
			ContainerName: "worker",
			Timestamp:     baseTime,
			Window:        15 * time.Second,
			CPUUsage:      0.1, // 100m
			MemoryUsage:   100, // 100Mi
		},
		{
			ContainerName: "worker",
			Timestamp:     baseTime.Add(15 * time.Second),
			Window:        15 * time.Second,
			CPUUsage:      0.15, // 150m
			MemoryUsage:   120,  // 120Mi
		},
		{
			ContainerName: "worker",
			Timestamp:     baseTime.Add(30 * time.Second),
			Window:        15 * time.Second,
			CPUUsage:      0.2, // 200m
			MemoryUsage:   150, // 150Mi
		},
	}

	// Current settings
	currentSettings := kubernetes.ResourceSettings{
		CPURequest:    0.1, // 100m
		CPULimit:      0.3, // 300m
		MemoryRequest: 128, // 128Mi
		MemoryLimit:   256, // 256Mi
	}

	// Test with 20% margin
	margin := 20
	recommendations, err := GenerateRecommendations(testMetrics, currentSettings, margin, 3)
	if err != nil {
		t.Fatalf("GenerateRecommendations() error = %v", err)
	}

	// Expected results (with 20% margin):
	// Avg CPU: (0.1 + 0.15 + 0.2) / 3 = 0.15, with 20% margin = 0.18
	// Peak CPU: 0.2, with 20% margin = 0.24
	// Avg Memory: (100 + 120 + 150) / 3 = 123.33, with 20% margin = 148
	// Peak Memory: 150, with 20% margin = 180

	// Test CPU request (tolerance for floating point comparison)
	if diff := abs(recommendations.CPURequest - 0.18); diff > 0.001 {
		t.Errorf("CPU Request: got %.3f, want %.3f", recommendations.CPURequest, 0.18)
	}

	// Test CPU limit
	if diff := abs(recommendations.CPULimit - 0.24); diff > 0.001 {
		t.Errorf("CPU Limit: got %.3f, want %.3f", recommendations.CPULimit, 0.24)
	}

	// Test Memory request (tolerance for floating point comparison)
	if diff := abs(recommendations.MemoryRequest - 148); diff > 0.5 {
		t.Errorf("Memory Request: got %.1f, want %.1f", recommendations.MemoryRequest, 148.0)
	}

	// Test Memory limit
	if diff := abs(recommendations.MemoryLimit - 180); diff > 0.5 {
		t.Errorf("Memory Limit: got %.1f, want %.1f", recommendations.MemoryLimit, 180.0)
	}

	// Test minimum values
	smallMetrics := []metrics.ResourceMetrics{
		{
			ContainerName: "worker",
			Timestamp:     baseTime,
			Window:        15 * time.Second,
			CPUUsage:      0.001, // 1m (very small)
			MemoryUsage:   10,    // 10Mi (very small)
		},
	}

	minRecommendations, err := GenerateRecommendations(smallMetrics, currentSettings, margin, 1)
	if err != nil {
		t.Fatalf("GenerateRecommendations() minimum values error = %v", err)
	}

	// Should use minimum values, not the actual calculated ones
	if minRecommendations.CPURequest < 0.01 {
		t.Errorf("Min CPU Request not enforced: got %.3f, want at least 0.01", minRecommendations.CPURequest)
	}

	if minRecommendations.CPULimit < 0.05 {
		t.Errorf("Min CPU Limit not enforced: got %.3f, want at least 0.05", minRecommendations.CPULimit)
	}

	if minRecommendations.MemoryRequest < 32 {
		t.Errorf("Min Memory Request not enforced: got %.1f, want at least 32", minRecommendations.MemoryRequest)
	}

	if minRecommendations.MemoryLimit < 64 {
		t.Errorf("Min Memory Limit not enforced: got %.1f, want at least 64", minRecommendations.MemoryLimit)
	}
}

func TestGenerateRecommendationsRequiresIndependentSamples(t *testing.T) {
	timestamp := time.Date(2026, time.August, 11, 10, 0, 0, 0, time.UTC)
	duplicate := metrics.ResourceMetrics{
		ContainerName: "worker",
		Timestamp:     timestamp,
		Window:        15 * time.Second,
		CPUUsage:      0.1,
		MemoryUsage:   100,
	}

	_, err := GenerateRecommendations(
		[]metrics.ResourceMetrics{duplicate, duplicate, duplicate},
		kubernetes.ResourceSettings{},
		20,
		3,
	)
	if err == nil || !strings.Contains(err.Error(), "got 1, need at least 3") {
		t.Fatalf("GenerateRecommendations() error = %v, want independent sample error", err)
	}
}

func TestGenerateRecommendationsDoesNotCountOverlappingSourceWindows(t *testing.T) {
	baseTime := time.Date(2026, time.August, 11, 10, 0, 0, 0, time.UTC)
	samples := []metrics.ResourceMetrics{
		{
			ContainerName: "worker",
			Timestamp:     baseTime,
			Window:        30 * time.Second,
			CPUUsage:      0.1,
			MemoryUsage:   100,
		},
		{
			ContainerName: "worker",
			Timestamp:     baseTime.Add(15 * time.Second),
			Window:        30 * time.Second,
			CPUUsage:      0.2,
			MemoryUsage:   200,
		},
	}

	_, err := GenerateRecommendations(samples, kubernetes.ResourceSettings{}, 20, 2)
	if err == nil || !strings.Contains(err.Error(), "got 1, need at least 2") {
		t.Fatalf("GenerateRecommendations() error = %v, want overlapping-window sample error", err)
	}
}

func TestGenerateRecommendationsRejectsMixedContainers(t *testing.T) {
	timestamp := time.Date(2026, time.August, 11, 10, 0, 0, 0, time.UTC)
	samples := []metrics.ResourceMetrics{
		{
			ContainerName: "worker",
			Timestamp:     timestamp,
			Window:        15 * time.Second,
			CPUUsage:      0.1,
			MemoryUsage:   100,
		},
		{
			ContainerName: "sidecar",
			Timestamp:     timestamp.Add(15 * time.Second),
			Window:        15 * time.Second,
			CPUUsage:      0.9,
			MemoryUsage:   500,
		},
	}

	_, err := GenerateRecommendations(samples, kubernetes.ResourceSettings{}, 20, 2)
	if err == nil || !strings.Contains(err.Error(), "calculate each container separately") {
		t.Fatalf("GenerateRecommendations() error = %v, want mixed-container error", err)
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
