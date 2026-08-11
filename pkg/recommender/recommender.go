package recommender

import (
	"fmt"

	"github.com/BogdanDolia/pod-rightsizer/pkg/kubernetes"
	"github.com/BogdanDolia/pod-rightsizer/pkg/metrics"
)

// DefaultMinimumSamples is the minimum number of non-overlapping Metrics API
// source windows required before a recommendation is considered safe.
const DefaultMinimumSamples = 3

// Recommendations holds the recommended resource settings
type Recommendations struct {
	CPURequest    float64
	CPULimit      float64
	MemoryRequest float64
	MemoryLimit   float64
}

// GenerateRecommendations calculates recommended resource settings based on collected metrics
func GenerateRecommendations(
	allMetrics []metrics.ResourceMetrics,
	currentSettings kubernetes.ResourceSettings,
	margin int,
	minimumSamples int,
) (Recommendations, error) {
	if minimumSamples <= 0 {
		return Recommendations{}, fmt.Errorf("minimum samples must be greater than zero")
	}

	independent, err := metrics.IndependentSamples(allMetrics)
	if err != nil {
		return Recommendations{}, err
	}
	if len(independent) < minimumSamples {
		return Recommendations{}, fmt.Errorf(
			"not enough independent metrics samples: got %d, need at least %d",
			len(independent),
			minimumSamples,
		)
	}

	// Calculate average and peak metrics
	avgCPU, avgMemory := metrics.CalculateAverageMetrics(independent)
	peakCPU, peakMemory := metrics.CalculatePeakMetrics(independent)

	// Apply safety margin
	marginMultiplier := 1.0 + (float64(margin) / 100.0)

	// Generate recommendations
	recommendations := Recommendations{
		// CPU request based on average usage with margin
		CPURequest: avgCPU * marginMultiplier,

		// CPU limit based on peak usage with margin
		CPULimit: peakCPU * marginMultiplier,

		// Memory request based on average usage with margin
		MemoryRequest: avgMemory * marginMultiplier,

		// Memory limit based on peak usage with margin
		MemoryLimit: peakMemory * marginMultiplier,
	}

	// Apply some reasonable minimum values
	recommendations = applyMinimumValues(recommendations)

	return recommendations, nil
}

// applyMinimumValues ensures we don't recommend values that are too small
func applyMinimumValues(r Recommendations) Recommendations {
	// Minimum values
	const (
		minCPURequest    = 0.01 // 10m
		minCPULimit      = 0.05 // 50m
		minMemoryRequest = 32.0 // 32Mi
		minMemoryLimit   = 64.0 // 64Mi
	)

	// Apply minimum values
	if r.CPURequest < minCPURequest {
		r.CPURequest = minCPURequest
	}
	if r.CPULimit < minCPULimit {
		r.CPULimit = minCPULimit
	}
	if r.MemoryRequest < minMemoryRequest {
		r.MemoryRequest = minMemoryRequest
	}
	if r.MemoryLimit < minMemoryLimit {
		r.MemoryLimit = minMemoryLimit
	}

	// Ensure limits are not smaller than requests
	if r.CPULimit < r.CPURequest {
		r.CPULimit = r.CPURequest
	}
	if r.MemoryLimit < r.MemoryRequest {
		r.MemoryLimit = r.MemoryRequest
	}

	return r
}
