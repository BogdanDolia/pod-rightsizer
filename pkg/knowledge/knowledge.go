package knowledge

import (
	coremetrics "github.com/BogdanDolia/pod-rightsizer/pkg/metrics"
	"github.com/BogdanDolia/pod-rightsizer/pkg/recommender"
)

// Evaluate returns a list of best-practices suggestions based on collected samples and recommendations.
// This is a minimal starter; rules can be expanded and loaded from YAML later.
func Evaluate(samples []coremetrics.ResourceMetrics, rec recommender.Recommendations) []string {
	var advice []string

	// CPU: keep HPA target utilization around ~70% for stability
	advice = append(advice, "Set HPA target utilization around ~70% for stable scaling.")

	// Downscale stabilization window default
	advice = append(advice, "Use downscale stabilizationWindowSeconds >= 300 to avoid flapping.")

	// Ensure limits are not too close to requests if p95 is high (simple heuristic)
	if rec.CPULimit > 0 && rec.CPULimit < rec.CPURequest*1.2 {
		advice = append(advice, "Consider setting CPU limit at least 20% above request or remove limit to avoid throttling.")
	}
	if rec.MemoryLimit > 0 && rec.MemoryLimit < rec.MemoryRequest*1.2 {
		advice = append(advice, "Set memory limit comfortably above request to reduce OOM risk (>=20%).")
	}

	// If we have few samples, warn about reliability
	if len(samples) < 3 {
		advice = append(advice, "Few metric samples collected; consider a longer test duration for better accuracy.")
	}

	return advice
}
