package loadtest

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

// TerminationFromContext maps a finished context to a termination reason.
// A nil or still-active context is duration_elapsed: the caller stopped
// because the planned time ended, not because the parent context ended.
func TerminationFromContext(ctx context.Context) TerminationReason {
	if ctx == nil || ctx.Err() == nil {
		return TerminationDurationElapsed
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return TerminationContextCanceled
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return TerminationContextDeadline
	}
	return TerminationDurationElapsed
}

// EvaluateSLO applies the numeric load gates.
// A nil SLO is not_evaluated, not passed. Threshold errors are returned and
// are not SLO failures. HTTP failures inside a finished run are violations,
// not errors. Zero requests do not satisfy a positive rate or a zero rate.
// Known facts outside the contract range are violations too. They are not
// threshold errors, and they are not rewritten to zero or missing. The check
// uses the reported numbers only. It does not require phase metadata.
func EvaluateSLO(out Outcome, slo *SLO) (SLOAssessment, error) {
	if slo == nil {
		return SLOAssessment{Status: SLONotEvaluated}, nil
	}
	if err := validateSLO(*slo); err != nil {
		return SLOAssessment{}, err
	}
	if problems := reportedFactProblems(out); len(problems) > 0 {
		return SLOAssessment{Status: SLOFailed, Violations: problems}, nil
	}

	assessment := SLOAssessment{Status: SLOFailed}
	if out.Termination != TerminationDurationElapsed {
		assessment.Violations = append(
			assessment.Violations,
			fmt.Sprintf("termination reason is %s", out.Termination),
		)
	}
	requestsKnown := out.HTTPRequests.State == SignalKnown && out.HTTPRequests.Value > 0
	if !requestsKnown {
		assessment.Violations = append(assessment.Violations, "no requests completed")
	} else {
		assessment.Violations = append(assessment.Violations, throughputViolations(out, *slo)...)
		assessment.Violations = append(assessment.Violations, errorRateViolations(out, *slo)...)
		assessment.Violations = append(assessment.Violations, latencyViolations(out, *slo)...)
	}
	if len(assessment.Violations) == 0 {
		assessment.Status = SLOPassed
		assessment.Passed = true
	}
	return assessment, nil
}

func validateSLO(slo SLO) error {
	if math.IsNaN(slo.MinimumRPS) || math.IsInf(slo.MinimumRPS, 0) || slo.MinimumRPS < 0 {
		return errors.New("minimum RPS SLO must be a finite non-negative number")
	}
	if math.IsNaN(slo.MaximumHTTPErrorRate) ||
		math.IsInf(slo.MaximumHTTPErrorRate, 0) ||
		slo.MaximumHTTPErrorRate < 0 ||
		slo.MaximumHTTPErrorRate > 1 {
		return errors.New("maximum HTTP error rate SLO must be between 0 and 1")
	}
	if slo.MaximumP95Latency < 0 {
		return errors.New("maximum p95 latency SLO must not be negative")
	}
	return nil
}

func throughputViolations(out Outcome, slo SLO) []string {
	if slo.MinimumRPS <= 0 {
		return nil
	}
	if out.ActualThroughput.State != SignalKnown || !finite(out.ActualThroughput.Value) {
		return []string{"actual RPS is unavailable"}
	}
	if out.ActualThroughput.Value < slo.MinimumRPS {
		return []string{fmt.Sprintf(
			"actual RPS %.2f is below minimum %.2f",
			out.ActualThroughput.Value,
			slo.MinimumRPS,
		)}
	}
	return nil
}

func errorRateViolations(out Outcome, slo SLO) []string {
	if out.HTTPErrorRate.State != SignalKnown || !finite(out.HTTPErrorRate.Value) {
		return []string{"HTTP error rate is unavailable"}
	}
	if out.HTTPErrorRate.Value > slo.MaximumHTTPErrorRate {
		return []string{fmt.Sprintf(
			"HTTP error rate %.2f%% exceeds maximum %.2f%%",
			out.HTTPErrorRate.Value*100,
			slo.MaximumHTTPErrorRate*100,
		)}
	}
	return nil
}

func latencyViolations(out Outcome, slo SLO) []string {
	if slo.MaximumP95Latency <= 0 {
		return nil
	}
	if out.Latency.P95.State != SignalKnown {
		return []string{"p95 latency is unavailable"}
	}
	p95 := time.Duration(out.Latency.P95.Nanoseconds)
	if p95 > slo.MaximumP95Latency {
		return []string{fmt.Sprintf(
			"p95 latency %s exceeds maximum %s",
			p95,
			slo.MaximumP95Latency,
		)}
	}
	return nil
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

// reportedFactProblems checks known numeric facts against the contract range.
// Missing and unknown signals are left alone. A known count or duration is
// not negative. A known rate is finite and not negative. A known ratio is
// finite and in [0, 1]. Known HTTP errors cannot exceed known HTTP requests.
// This does not require a measured phase or any other lifecycle metadata.
func reportedFactProblems(out Outcome) []string {
	var reasons []string
	reasons = append(reasons, countProblem("HTTP requests", out.HTTPRequests)...)
	reasons = append(reasons, countProblem("HTTP errors", out.HTTPErrors)...)
	reasons = append(reasons, countProblem("dropped work", out.DroppedWork)...)
	reasons = append(reasons, countProblem("transport errors", out.TransportErrors)...)
	if out.HTTPRequests.State == SignalKnown && out.HTTPErrors.State == SignalKnown &&
		out.HTTPRequests.Value >= 0 && out.HTTPErrors.Value >= 0 &&
		out.HTTPErrors.Value > out.HTTPRequests.Value {
		reasons = append(reasons, "HTTP errors exceed HTTP requests")
	}
	reasons = append(reasons, rateProblem("actual throughput", out.ActualThroughput)...)
	reasons = append(reasons, rateProblem("useful throughput", out.UsefulThroughput)...)
	reasons = append(reasons, ratioProblem("HTTP error rate", out.HTTPErrorRate)...)
	reasons = append(reasons, durationProblem("p50 latency", out.Latency.P50)...)
	reasons = append(reasons, durationProblem("p95 latency", out.Latency.P95)...)
	reasons = append(reasons, durationProblem("p99 latency", out.Latency.P99)...)
	if out.Checks.State == SignalKnown {
		if out.Checks.Defined < 0 {
			reasons = append(reasons, "application checks defined count is negative")
		}
		if out.Checks.Failed < 0 {
			reasons = append(reasons, "application checks failed count is negative")
		}
	}
	if len(out.StatusCodes) > 0 {
		codes := make([]int, 0, len(out.StatusCodes))
		for code := range out.StatusCodes {
			codes = append(codes, code)
		}
		sort.Ints(codes)
		for _, code := range codes {
			if out.StatusCodes[code] < 0 {
				reasons = append(reasons, fmt.Sprintf("HTTP status %d count is negative", code))
			}
		}
	}
	return reasons
}

func countProblem(name string, count Count) []string {
	if count.State != SignalKnown || count.Value >= 0 {
		return nil
	}
	return []string{fmt.Sprintf("%s count is negative", name)}
}

func rateProblem(name string, rate Rate) []string {
	if rate.State != SignalKnown {
		return nil
	}
	if !finite(rate.Value) || rate.Value < 0 {
		return []string{fmt.Sprintf("%s is not a finite non-negative rate", name)}
	}
	return nil
}

func ratioProblem(name string, ratio Ratio) []string {
	if ratio.State != SignalKnown {
		return nil
	}
	if !finite(ratio.Value) || ratio.Value < 0 || ratio.Value > 1 {
		return []string{fmt.Sprintf("%s is not a finite ratio from 0 to 1", name)}
	}
	return nil
}

func durationProblem(name string, duration DurationSignal) []string {
	if duration.State != SignalKnown || duration.Nanoseconds >= 0 {
		return nil
	}
	return []string{fmt.Sprintf("%s is negative", name)}
}
