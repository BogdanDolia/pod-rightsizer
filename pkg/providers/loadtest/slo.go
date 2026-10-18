package loadtest

import (
	"context"
	"errors"
	"fmt"
	"math"
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
func EvaluateSLO(out Outcome, slo *SLO) (SLOAssessment, error) {
	if slo == nil {
		return SLOAssessment{Status: SLONotEvaluated}, nil
	}
	if err := validateSLO(*slo); err != nil {
		return SLOAssessment{}, err
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
