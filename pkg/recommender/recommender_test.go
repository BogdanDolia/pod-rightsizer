package recommender

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/BogdanDolia/pod-rightsizer/pkg/kubernetes"
	"github.com/BogdanDolia/pod-rightsizer/pkg/metrics"
)

func TestGenerateRecommendationsUsesCPUPercentileAndMemoryHighWater(t *testing.T) {
	samples := sampleSeries(20, func(index int) (float64, float64) {
		if index == 19 {
			return 10.0, 500
		}
		return float64(index+1) / 10, float64(100 + index*10)
	})
	current := kubernetes.ResourceSettings{
		CPURequest:    1.5,
		CPULimit:      3,
		MemoryRequest: 400,
		MemoryLimit:   800,
	}
	policy := DefaultPolicy()
	policy.CPUPercentile = 90
	policy.CPURequestBufferPercent = 10
	policy.MemoryBufferPercent = 20

	recommendation, err := GenerateRecommendations(samples, current, policy)
	if err != nil {
		t.Fatalf("GenerateRecommendations() error = %v", err)
	}

	// Nearest-rank p90 is the 18th sorted CPU value (1.8 cores); request adds 10%.
	assertClose(t, recommendation.Observed.CPUPercentileValue, 1.8)
	assertClose(t, recommendation.CPURequest, 1.98)
	assertClose(t, recommendation.Observed.MemoryHighWater, 500)
	assertClose(t, recommendation.MemoryRequest, 600)
	if recommendation.CPULimit != 0 {
		t.Fatalf("CPU limit = %.3f, want omitted", recommendation.CPULimit)
	}
	assertClose(t, recommendation.MemoryLimit, 720)

	if recommendation.Comparison.CPURequest.Direction != "increase" {
		t.Fatalf("CPU comparison = %#v, want increase", recommendation.Comparison.CPURequest)
	}
	if recommendation.Comparison.CPULimit.Direction != "decrease" {
		t.Fatalf("CPU limit comparison = %#v, want decrease", recommendation.Comparison.CPULimit)
	}
	if recommendation.Comparison.MemoryRequest.Direction != "increase" {
		t.Fatalf("memory comparison = %#v, want increase", recommendation.Comparison.MemoryRequest)
	}
	if len(recommendation.Explanation) < 5 ||
		!strings.Contains(strings.Join(recommendation.Explanation, " "), "high-water mark") {
		t.Fatalf("Explanation = %#v, want calculation audit trail", recommendation.Explanation)
	}
}

func TestLimitPolicies(t *testing.T) {
	samples := sampleSeries(3, func(index int) (float64, float64) {
		return float64(index+1) / 10, float64(100 + index*10)
	})
	current := kubernetes.ResourceSettings{CPULimit: 0.8, MemoryLimit: 512}

	tests := []struct {
		name       string
		cpu        LimitPolicy
		memory     LimitPolicy
		wantCPU    float64
		wantMemory float64
	}{
		{
			name:       "none",
			cpu:        LimitPolicy{Mode: LimitNone},
			memory:     LimitPolicy{Mode: LimitNone},
			wantCPU:    0,
			wantMemory: 0,
		},
		{
			name:       "keep",
			cpu:        LimitPolicy{Mode: LimitKeep},
			memory:     LimitPolicy{Mode: LimitKeep},
			wantCPU:    0.8,
			wantMemory: 512,
		},
		{
			name:       "request multiplier",
			cpu:        LimitPolicy{Mode: LimitRequestMultiplier, Multiplier: 2},
			memory:     LimitPolicy{Mode: LimitRequestMultiplier, Multiplier: 1.5},
			wantCPU:    0.66,
			wantMemory: 216,
		},
		{
			name:       "peak multiplier",
			cpu:        LimitPolicy{Mode: LimitPeakMultiplier, Multiplier: 1.5},
			memory:     LimitPolicy{Mode: LimitPeakMultiplier, Multiplier: 2},
			wantCPU:    0.45,
			wantMemory: 240,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := DefaultPolicy()
			policy.CPULimit = test.cpu
			policy.MemoryLimit = test.memory
			recommendation, err := GenerateRecommendations(samples, current, policy)
			if err != nil {
				t.Fatalf("GenerateRecommendations() error = %v", err)
			}
			assertClose(t, recommendation.CPULimit, test.wantCPU)
			assertClose(t, recommendation.MemoryLimit, test.wantMemory)
		})
	}
}

func TestLimitNeverFallsBelowRequest(t *testing.T) {
	policy := DefaultPolicy()
	policy.CPULimit = LimitPolicy{Mode: LimitKeep}
	policy.MemoryLimit = LimitPolicy{Mode: LimitPeakMultiplier, Multiplier: 1}
	recommendation, err := GenerateRecommendations(
		sampleSeries(3, func(int) (float64, float64) { return 0.2, 100 }),
		kubernetes.ResourceSettings{CPULimit: 0.01},
		policy,
	)
	if err != nil {
		t.Fatalf("GenerateRecommendations() error = %v", err)
	}
	if recommendation.CPULimit < recommendation.CPURequest {
		t.Fatalf("CPU limit %.3f is below request %.3f", recommendation.CPULimit, recommendation.CPURequest)
	}
	if recommendation.MemoryLimit < recommendation.MemoryRequest {
		t.Fatalf("memory limit %.1f is below request %.1f", recommendation.MemoryLimit, recommendation.MemoryRequest)
	}
	if !strings.Contains(strings.Join(recommendation.Explanation, " "), "then is normalized") {
		t.Fatalf("explanation does not disclose limit normalization: %#v", recommendation.Explanation)
	}
}

func TestKeepLimitExplanationDisclosesResourceFloor(t *testing.T) {
	policy := DefaultPolicy()
	policy.CPULimit = LimitPolicy{Mode: LimitKeep}
	recommendation, err := GenerateRecommendations(
		sampleSeries(3, func(int) (float64, float64) { return 0.005, 100 }),
		kubernetes.ResourceSettings{CPULimit: 0.02},
		policy,
	)
	if err != nil {
		t.Fatalf("GenerateRecommendations() error = %v", err)
	}
	joined := strings.Join(recommendation.Explanation, " ")
	if !strings.Contains(joined, "current value 0.02") ||
		!strings.Contains(joined, "resource floor") {
		t.Fatalf("explanation does not disclose keep normalization: %#v", recommendation.Explanation)
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
		DefaultPolicy(),
	)
	if err == nil || !strings.Contains(err.Error(), "got 1, need at least 3") {
		t.Fatalf("GenerateRecommendations() error = %v, want independent sample error", err)
	}
}

func TestGenerateRecommendationsRejectsInvalidPolicyAndUsage(t *testing.T) {
	samples := sampleSeries(3, func(int) (float64, float64) { return 0.1, 100 })

	invalidPercentile := DefaultPolicy()
	invalidPercentile.CPUPercentile = 0
	if _, err := GenerateRecommendations(samples, kubernetes.ResourceSettings{}, invalidPercentile); err == nil {
		t.Fatal("GenerateRecommendations() accepted zero CPU percentile")
	}

	invalidLimit := DefaultPolicy()
	invalidLimit.CPULimit = LimitPolicy{Mode: LimitRequestMultiplier, Multiplier: 0.9}
	if _, err := GenerateRecommendations(samples, kubernetes.ResourceSettings{}, invalidLimit); err == nil {
		t.Fatal("GenerateRecommendations() accepted a limit multiplier below 1")
	}

	samples[1].MemoryUsage = math.NaN()
	if _, err := GenerateRecommendations(samples, kubernetes.ResourceSettings{}, DefaultPolicy()); err == nil {
		t.Fatal("GenerateRecommendations() accepted NaN memory usage")
	}
}

func TestConfidenceReflectsEvidenceQuantityAndDuration(t *testing.T) {
	low, err := GenerateRecommendations(
		sampleSeries(3, func(int) (float64, float64) { return 0.1, 100 }),
		kubernetes.ResourceSettings{},
		DefaultPolicy(),
	)
	if err != nil {
		t.Fatalf("low-confidence recommendation error = %v", err)
	}
	if low.Confidence.Level != "low" {
		t.Fatalf("low confidence = %#v, want low", low.Confidence)
	}

	highSamples := sampleSeries(30, func(int) (float64, float64) { return 0.1, 100 })
	for index := range highSamples {
		highSamples[index].Timestamp = highSamples[0].Timestamp.Add(time.Duration(index) * time.Minute)
		highSamples[index].Window = time.Minute
	}
	high, err := GenerateRecommendations(highSamples, kubernetes.ResourceSettings{}, DefaultPolicy())
	if err != nil {
		t.Fatalf("high-confidence recommendation error = %v", err)
	}
	if high.Confidence.Level != "high" || high.Confidence.Score != 1 {
		t.Fatalf("high confidence = %#v, want high score 1", high.Confidence)
	}
}

func sampleSeries(count int, values func(int) (float64, float64)) []metrics.ResourceMetrics {
	baseTime := time.Date(2026, time.August, 11, 10, 0, 0, 0, time.UTC)
	samples := make([]metrics.ResourceMetrics, count)
	for index := range samples {
		cpu, memory := values(index)
		samples[index] = metrics.ResourceMetrics{
			ContainerName: "worker",
			Timestamp:     baseTime.Add(time.Duration(index) * 15 * time.Second),
			Window:        15 * time.Second,
			CPUUsage:      cpu,
			MemoryUsage:   memory,
		}
	}
	return samples
}

func assertClose(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("got %.10f, want %.10f", got, want)
	}
}
