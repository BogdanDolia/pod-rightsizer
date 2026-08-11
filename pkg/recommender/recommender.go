package recommender

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/BogdanDolia/pod-rightsizer/pkg/kubernetes"
	"github.com/BogdanDolia/pod-rightsizer/pkg/metrics"
)

const (
	DefaultMinimumSamples           = 3
	DefaultCPUPercentile            = 95.0
	DefaultCPURequestBufferPercent  = 10.0
	DefaultMemoryBufferPercent      = 20.0
	DefaultMemoryLimitMultiplier    = 1.20
	minimumCPURequest               = 0.01 // 10m
	minimumCPULimit                 = 0.05 // 50m
	minimumMemoryRequest            = 32.0 // 32Mi
	minimumMemoryLimit              = 64.0 // 64Mi
	targetHighConfidenceSamples     = 30
	targetHighConfidenceObservation = 30 * time.Minute
)

// LimitMode selects how a resource limit is calculated.
type LimitMode string

const (
	LimitNone              LimitMode = "none"
	LimitKeep              LimitMode = "keep"
	LimitRequestMultiplier LimitMode = "request-multiplier"
	LimitPeakMultiplier    LimitMode = "peak-multiplier"
)

// LimitPolicy controls one resource limit. Multiplier is used by the two
// multiplier modes and must be at least 1.
type LimitPolicy struct {
	Mode       LimitMode `json:"mode"`
	Multiplier float64   `json:"multiplier,omitempty"`
}

// Policy contains every input that influences a recommendation.
type Policy struct {
	CPUPercentile           float64     `json:"cpuPercentile"`
	CPURequestBufferPercent float64     `json:"cpuRequestBufferPercent"`
	MemoryBufferPercent     float64     `json:"memoryBufferPercent"`
	CPULimit                LimitPolicy `json:"cpuLimit"`
	MemoryLimit             LimitPolicy `json:"memoryLimit"`
	MinimumSamples          int         `json:"minimumSamples"`
}

// DefaultPolicy returns conservative defaults: CPU request is p95 plus 10%,
// memory request is the observed high-water mark plus 20%, CPU has no limit to
// avoid throttling, and memory limit is 1.2 times its request.
func DefaultPolicy() Policy {
	return Policy{
		CPUPercentile:           DefaultCPUPercentile,
		CPURequestBufferPercent: DefaultCPURequestBufferPercent,
		MemoryBufferPercent:     DefaultMemoryBufferPercent,
		CPULimit: LimitPolicy{
			Mode: LimitNone,
		},
		MemoryLimit: LimitPolicy{
			Mode:       LimitRequestMultiplier,
			Multiplier: DefaultMemoryLimitMultiplier,
		},
		MinimumSamples: DefaultMinimumSamples,
	}
}

// ObservedStatistics records the exact observations used by the calculation.
type ObservedStatistics struct {
	IndependentSamples int     `json:"independentSamples"`
	ObservationSeconds float64 `json:"observationSeconds"`
	CPUPercentile      float64 `json:"cpuPercentile"`
	CPUPercentileValue float64 `json:"cpuPercentileValue"`
	CPUPeak            float64 `json:"cpuPeak"`
	MemoryHighWater    float64 `json:"memoryHighWater"`
}

// Confidence describes evidence quality, not workload safety. A high score
// means the recommendation was supported by enough independent source windows
// over enough time.
type Confidence struct {
	Level              string   `json:"level"`
	Score              float64  `json:"score"`
	IndependentSamples int      `json:"independentSamples"`
	ObservationSeconds float64  `json:"observationSeconds"`
	Reasons            []string `json:"reasons"`
}

// SettingComparison compares one recommended value with the current value.
// DeltaPercent is nil when the current value is zero and a percentage would be
// undefined.
type SettingComparison struct {
	Current      float64  `json:"current"`
	Recommended  float64  `json:"recommended"`
	Delta        float64  `json:"delta"`
	DeltaPercent *float64 `json:"deltaPercent,omitempty"`
	Direction    string   `json:"direction"`
}

// Comparison contains request and limit changes in their native units: cores
// for CPU and Mi for memory.
type Comparison struct {
	CPURequest    SettingComparison `json:"cpuRequest"`
	CPULimit      SettingComparison `json:"cpuLimit"`
	MemoryRequest SettingComparison `json:"memoryRequest"`
	MemoryLimit   SettingComparison `json:"memoryLimit"`
}

// Recommendations holds the calculated settings and their audit trail. A zero
// CPU or memory limit means that no limit should be configured.
type Recommendations struct {
	CPURequest    float64            `json:"cpuRequest"`
	CPULimit      float64            `json:"cpuLimit"`
	MemoryRequest float64            `json:"memoryRequest"`
	MemoryLimit   float64            `json:"memoryLimit"`
	Policy        Policy             `json:"policy"`
	Observed      ObservedStatistics `json:"observed"`
	Confidence    Confidence         `json:"confidence"`
	Explanation   []string           `json:"explanation"`
	Comparison    Comparison         `json:"comparison"`
}

// GenerateRecommendations calculates resource settings from validated,
// independent Metrics API samples.
func GenerateRecommendations(
	allMetrics []metrics.ResourceMetrics,
	currentSettings kubernetes.ResourceSettings,
	policy Policy,
) (Recommendations, error) {
	if err := policy.Validate(); err != nil {
		return Recommendations{}, err
	}
	if err := validateCurrentSettings(currentSettings); err != nil {
		return Recommendations{}, err
	}

	independent, err := metrics.IndependentSamples(allMetrics)
	if err != nil {
		return Recommendations{}, err
	}
	if len(independent) < policy.MinimumSamples {
		return Recommendations{}, fmt.Errorf(
			"not enough independent metrics samples: got %d, need at least %d",
			len(independent),
			policy.MinimumSamples,
		)
	}
	if err := validateUsage(independent); err != nil {
		return Recommendations{}, err
	}

	cpuPercentileValue := percentileCPU(independent, policy.CPUPercentile)
	cpuPeak, memoryHighWater := metrics.CalculatePeakMetrics(independent)
	cpuRequest := cpuPercentileValue * percentMultiplier(policy.CPURequestBufferPercent)
	memoryRequest := memoryHighWater * percentMultiplier(policy.MemoryBufferPercent)
	cpuRequest = math.Max(cpuRequest, minimumCPURequest)
	memoryRequest = math.Max(memoryRequest, minimumMemoryRequest)

	cpuLimit := calculateLimit(policy.CPULimit, currentSettings.CPULimit, cpuRequest, cpuPeak)
	memoryLimit := calculateLimit(
		policy.MemoryLimit,
		currentSettings.MemoryLimit,
		memoryRequest,
		memoryHighWater,
	)
	cpuLimit = normalizeLimit(cpuLimit, cpuRequest, minimumCPULimit)
	memoryLimit = normalizeLimit(memoryLimit, memoryRequest, minimumMemoryLimit)
	for _, value := range []struct {
		name   string
		amount float64
	}{
		{"CPU request", cpuRequest},
		{"CPU limit", cpuLimit},
		{"memory request", memoryRequest},
		{"memory limit", memoryLimit},
	} {
		if !finite(value.amount) {
			return Recommendations{}, fmt.Errorf("%s calculation overflowed", value.name)
		}
	}

	observation := observationDuration(independent)
	recommendation := Recommendations{
		CPURequest:    cpuRequest,
		CPULimit:      cpuLimit,
		MemoryRequest: memoryRequest,
		MemoryLimit:   memoryLimit,
		Policy:        policy,
		Observed: ObservedStatistics{
			IndependentSamples: len(independent),
			ObservationSeconds: observation.Seconds(),
			CPUPercentile:      policy.CPUPercentile,
			CPUPercentileValue: cpuPercentileValue,
			CPUPeak:            cpuPeak,
			MemoryHighWater:    memoryHighWater,
		},
		Confidence: confidence(len(independent), observation),
	}
	recommendation.Comparison = compare(currentSettings, recommendation)
	recommendation.Explanation = explanation(recommendation, currentSettings)
	return recommendation, nil
}

// Validate checks policy values without requiring metric samples.
func (policy Policy) Validate() error {
	return validatePolicy(policy)
}

func validatePolicy(policy Policy) error {
	if !finite(policy.CPUPercentile) || policy.CPUPercentile <= 0 || policy.CPUPercentile > 100 {
		return errors.New("CPU percentile must be greater than 0 and at most 100")
	}
	if err := validatePercent("CPU request buffer", policy.CPURequestBufferPercent); err != nil {
		return err
	}
	if err := validatePercent("memory buffer", policy.MemoryBufferPercent); err != nil {
		return err
	}
	if policy.MinimumSamples < 2 {
		return errors.New("minimum samples must be at least 2")
	}
	if err := validateLimitPolicy("CPU", policy.CPULimit); err != nil {
		return err
	}
	return validateLimitPolicy("memory", policy.MemoryLimit)
}

func validatePercent(name string, value float64) error {
	if !finite(value) || value < 0 || value > 1000 {
		return fmt.Errorf("%s percent must be a finite number between 0 and 1000", name)
	}
	return nil
}

func validateLimitPolicy(resource string, policy LimitPolicy) error {
	switch policy.Mode {
	case LimitNone, LimitKeep:
		if policy.Multiplier != 0 && (!finite(policy.Multiplier) || policy.Multiplier < 1) {
			return fmt.Errorf("%s limit multiplier must be at least 1", resource)
		}
		return nil
	case LimitRequestMultiplier, LimitPeakMultiplier:
		if !finite(policy.Multiplier) || policy.Multiplier < 1 {
			return fmt.Errorf("%s limit multiplier must be at least 1", resource)
		}
		return nil
	default:
		return fmt.Errorf("unsupported %s limit policy %q", resource, policy.Mode)
	}
}

func validateCurrentSettings(settings kubernetes.ResourceSettings) error {
	values := []struct {
		name  string
		value float64
	}{
		{"current CPU request", settings.CPURequest},
		{"current CPU limit", settings.CPULimit},
		{"current memory request", settings.MemoryRequest},
		{"current memory limit", settings.MemoryLimit},
	}
	for _, item := range values {
		if !finite(item.value) || item.value < 0 {
			return fmt.Errorf("%s must be a finite non-negative number", item.name)
		}
	}
	return nil
}

func validateUsage(samples []metrics.ResourceMetrics) error {
	for _, sample := range samples {
		if !finite(sample.CPUUsage) || sample.CPUUsage < 0 {
			return fmt.Errorf("CPU usage at %s must be a finite non-negative number", sample.Timestamp.Format(time.RFC3339Nano))
		}
		if !finite(sample.MemoryUsage) || sample.MemoryUsage < 0 {
			return fmt.Errorf("memory usage at %s must be a finite non-negative number", sample.Timestamp.Format(time.RFC3339Nano))
		}
	}
	return nil
}

func percentileCPU(samples []metrics.ResourceMetrics, percentile float64) float64 {
	values := make([]float64, len(samples))
	for index, sample := range samples {
		values[index] = sample.CPUUsage
	}
	sort.Float64s(values)
	rank := int(math.Ceil(percentile/100*float64(len(values)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(values) {
		rank = len(values) - 1
	}
	return values[rank]
}

func percentMultiplier(percent float64) float64 {
	return 1 + percent/100
}

func calculateLimit(policy LimitPolicy, current, request, peak float64) float64 {
	switch policy.Mode {
	case LimitNone:
		return 0
	case LimitKeep:
		return current
	case LimitRequestMultiplier:
		return request * policy.Multiplier
	case LimitPeakMultiplier:
		return peak * policy.Multiplier
	default:
		return 0
	}
}

func normalizeLimit(limit, request, minimum float64) float64 {
	if limit == 0 {
		return 0
	}
	return math.Max(math.Max(limit, request), minimum)
}

func observationDuration(samples []metrics.ResourceMetrics) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	start := samples[0].Timestamp.Add(-samples[0].Window)
	end := samples[0].Timestamp
	for _, sample := range samples[1:] {
		sampleStart := sample.Timestamp.Add(-sample.Window)
		if sampleStart.Before(start) {
			start = sampleStart
		}
		if sample.Timestamp.After(end) {
			end = sample.Timestamp
		}
	}
	return end.Sub(start)
}

func confidence(sampleCount int, observation time.Duration) Confidence {
	sampleScore := math.Min(float64(sampleCount)/targetHighConfidenceSamples, 1)
	observationScore := math.Min(float64(observation)/float64(targetHighConfidenceObservation), 1)
	score := roundScore(sampleScore*0.8 + observationScore*0.2)
	level := "low"
	if score >= 0.8 {
		level = "high"
	} else if score >= 0.45 {
		level = "medium"
	}

	reasons := []string{
		fmt.Sprintf(
			"%d independent Metrics API windows contribute 80%% of the score (target: %d)",
			sampleCount,
			targetHighConfidenceSamples,
		),
		fmt.Sprintf(
			"%.0f seconds of observation contribute 20%% of the score (target: %.0f seconds)",
			observation.Seconds(),
			targetHighConfidenceObservation.Seconds(),
		),
	}
	if level != "high" {
		reasons = append(reasons, "collect more independent windows and cover representative traffic periods before applying automatically")
	}
	return Confidence{
		Level:              level,
		Score:              score,
		IndependentSamples: sampleCount,
		ObservationSeconds: observation.Seconds(),
		Reasons:            reasons,
	}
}

func roundScore(value float64) float64 {
	return math.Round(value*100) / 100
}

func explanation(
	recommendation Recommendations,
	currentSettings kubernetes.ResourceSettings,
) []string {
	policy := recommendation.Policy
	observed := recommendation.Observed
	return []string{
		fmt.Sprintf(
			"CPU request %.0fm = p%.2g CPU %.0fm plus %.2g%% buffer, with a 10m floor",
			recommendation.CPURequest*1000,
			policy.CPUPercentile,
			observed.CPUPercentileValue*1000,
			policy.CPURequestBufferPercent,
		),
		fmt.Sprintf(
			"Memory request %.0fMi = high-water mark %.0fMi plus %.2g%% buffer, with a 32Mi floor",
			recommendation.MemoryRequest,
			observed.MemoryHighWater,
			policy.MemoryBufferPercent,
		),
		limitExplanation("CPU", recommendation.CPULimit, policy.CPULimit, currentSettings.CPULimit, recommendation.CPURequest, observed.CPUPeak),
		limitExplanation("Memory", recommendation.MemoryLimit, policy.MemoryLimit, currentSettings.MemoryLimit, recommendation.MemoryRequest, observed.MemoryHighWater),
		fmt.Sprintf(
			"Confidence is %s (%.2f) from %d independent windows spanning %.0f seconds",
			recommendation.Confidence.Level,
			recommendation.Confidence.Score,
			recommendation.Confidence.IndependentSamples,
			recommendation.Confidence.ObservationSeconds,
		),
	}
}

func limitExplanation(
	resource string,
	value float64,
	policy LimitPolicy,
	current, request, peak float64,
) string {
	switch policy.Mode {
	case LimitNone:
		return fmt.Sprintf("%s limit is omitted by policy to avoid an enforced ceiling", resource)
	case LimitKeep:
		message := fmt.Sprintf("%s limit %.3g starts from the current value %.3g", resource, value, current)
		if math.Abs(value-current) > 1e-9 {
			message += ", then is normalized to stay at or above the new request and resource floor"
		}
		return message
	case LimitRequestMultiplier:
		raw := request * policy.Multiplier
		message := fmt.Sprintf("%s limit %.3g starts from request %.3g times %.3g", resource, value, request, policy.Multiplier)
		if math.Abs(value-raw) > 1e-9 {
			message += fmt.Sprintf(" = %.3g, then is normalized to stay at or above the request and resource floor", raw)
		}
		return message
	case LimitPeakMultiplier:
		raw := peak * policy.Multiplier
		message := fmt.Sprintf("%s limit %.3g starts from observed peak %.3g times %.3g", resource, value, peak, policy.Multiplier)
		if math.Abs(value-raw) > 1e-9 {
			message += fmt.Sprintf(" = %.3g, then is normalized to stay at or above the request and resource floor", raw)
		}
		return message
	default:
		return fmt.Sprintf("%s limit policy is unsupported", resource)
	}
}

func compare(current kubernetes.ResourceSettings, recommendation Recommendations) Comparison {
	return Comparison{
		CPURequest:    compareSetting(current.CPURequest, recommendation.CPURequest),
		CPULimit:      compareSetting(current.CPULimit, recommendation.CPULimit),
		MemoryRequest: compareSetting(current.MemoryRequest, recommendation.MemoryRequest),
		MemoryLimit:   compareSetting(current.MemoryLimit, recommendation.MemoryLimit),
	}
}

func compareSetting(current, recommended float64) SettingComparison {
	delta := recommended - current
	direction := "unchanged"
	if delta > 1e-9 {
		direction = "increase"
	} else if delta < -1e-9 {
		direction = "decrease"
	}
	comparison := SettingComparison{
		Current:     current,
		Recommended: recommended,
		Delta:       delta,
		Direction:   direction,
	}
	if current != 0 {
		percentage := delta / current * 100
		comparison.DeltaPercent = &percentage
	}
	return comparison
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
