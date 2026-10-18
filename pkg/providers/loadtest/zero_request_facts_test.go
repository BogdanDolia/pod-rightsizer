package loadtest

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestSessionKeepsInvalidZeroRequestFacts walks Begin, StartPhase,
// SetMeasurements, and Finish. Zero completed requests still normalize
// in-range rates, ratios, latency, and status counts. An out-of-range fact
// stays on the outcome, and suitability names that field.
func TestSessionKeepsInvalidZeroRequestFacts(t *testing.T) {
	adapter := newFake(t, "fixture-engine", "1", successCapabilities(), LatencyUntilResponseHeaders)
	slo := &SLO{
		MinimumRPS:           1,
		MaximumHTTPErrorRate: 1,
		MaximumP95Latency:    time.Second,
	}

	t.Run("valid zeros normalize", func(t *testing.T) {
		measurements := acceptableZeroMeasurements()
		measurements.StatusCodes = map[int]int{200: 0}
		useful := KnownRate(0)
		session, outcome, err := finishZeroRequest(t, adapter, measurements, &useful)
		if err != nil {
			t.Fatalf("Finish() error = %v", err)
		}
		assertNormalizedZeroRequest(t, outcome)
		if outcome.StatusCodes != nil {
			t.Fatalf("status codes = %#v, want nil", outcome.StatusCodes)
		}
		if !outcome.Suitability.Usable || len(outcome.Suitability.Reasons) != 0 {
			t.Fatalf("suitability = %#v, want usable with no reasons", outcome.Suitability)
		}
		assessment, evalErr := EvaluateSLO(outcome, slo)
		if evalErr != nil || assessment.Passed || assessment.Status != SLOFailed {
			t.Fatalf("EvaluateSLO() = %#v, %v, want failed", assessment, evalErr)
		}
		if !strings.Contains(strings.Join(assessment.Violations, " "), "no requests completed") {
			t.Fatalf("violations = %#v, want no requests completed", assessment.Violations)
		}
		none, evalErr := EvaluateSLO(outcome, nil)
		if evalErr != nil || none.Passed || none.Status != SLONotEvaluated {
			t.Fatalf("nil SLO = %#v, %v, want not_evaluated", none, evalErr)
		}
		assertStableZeroResult(t, session, outcome)
	})

	t.Run("absent signals stay missing", func(t *testing.T) {
		measurements := Measurements{
			HTTPRequests: KnownCount(0),
			HTTPErrors:   KnownCount(0),
		}
		_, outcome, err := finishZeroRequest(t, adapter, measurements, nil)
		if err != nil {
			t.Fatalf("Finish() error = %v", err)
		}
		assertNormalizedZeroRequest(t, outcome)
		if outcome.DroppedWork.State != SignalMissing || outcome.GeneratorSaturated.State != SignalMissing || outcome.Checks.State != SignalMissing {
			t.Fatalf("absent signals = dropped %#v saturated %#v checks %#v", outcome.DroppedWork, outcome.GeneratorSaturated, outcome.Checks)
		}
		if outcome.ActualThroughput.State == SignalKnown || outcome.HTTPErrorRate.State == SignalKnown || outcome.Latency.P95.State == SignalKnown {
			t.Fatalf("absent derivative facts became known zeros: throughput %#v error rate %#v p95 %#v", outcome.ActualThroughput, outcome.HTTPErrorRate, outcome.Latency.P95)
		}
	})

	usefulNaN := KnownRate(math.NaN())
	negativeUseful := KnownRate(-1)
	cases := []struct {
		name     string
		mutate   func(*Measurements)
		useful   *Rate
		reason   string
		preserve string
		remains  func(*testing.T, Outcome)
	}{
		{
			name:     "negative error ratio",
			mutate:   func(m *Measurements) { m.HTTPErrorRate = KnownRatio(-1) },
			reason:   "HTTP error rate is not a finite ratio from 0 to 1",
			preserve: "error-rate",
			remains: func(t *testing.T, out Outcome) {
				t.Helper()
				if out.HTTPErrorRate.State != SignalKnown || out.HTTPErrorRate.Value != -1 {
					t.Fatalf("error rate = %#v, want the reported -1", out.HTTPErrorRate)
				}
			},
		},
		{
			name:     "error ratio above one",
			mutate:   func(m *Measurements) { m.HTTPErrorRate = KnownRatio(1.5) },
			reason:   "HTTP error rate is not a finite ratio from 0 to 1",
			preserve: "error-rate",
			remains: func(t *testing.T, out Outcome) {
				t.Helper()
				if out.HTTPErrorRate.State != SignalKnown || out.HTTPErrorRate.Value != 1.5 {
					t.Fatalf("error rate = %#v, want the reported 1.5", out.HTTPErrorRate)
				}
			},
		},
		{
			name:     "NaN throughput",
			mutate:   func(m *Measurements) { m.ActualThroughput = KnownRate(math.NaN()) },
			reason:   "actual throughput is not a finite non-negative rate",
			preserve: "throughput",
			remains: func(t *testing.T, out Outcome) {
				t.Helper()
				if out.ActualThroughput.State != SignalKnown || !math.IsNaN(out.ActualThroughput.Value) {
					t.Fatalf("throughput = %#v, want the reported NaN", out.ActualThroughput)
				}
			},
		},
		{
			name:     "infinite throughput",
			mutate:   func(m *Measurements) { m.ActualThroughput = KnownRate(math.Inf(1)) },
			reason:   "actual throughput is not a finite non-negative rate",
			preserve: "throughput",
			remains: func(t *testing.T, out Outcome) {
				t.Helper()
				if out.ActualThroughput.State != SignalKnown || !math.IsInf(out.ActualThroughput.Value, 1) {
					t.Fatalf("throughput = %#v, want the reported +Inf", out.ActualThroughput)
				}
			},
		},
		{
			name:     "negative throughput",
			mutate:   func(m *Measurements) { m.ActualThroughput = KnownRate(-1) },
			reason:   "actual throughput is not a finite non-negative rate",
			preserve: "throughput",
			remains: func(t *testing.T, out Outcome) {
				t.Helper()
				if out.ActualThroughput.State != SignalKnown || out.ActualThroughput.Value != -1 {
					t.Fatalf("throughput = %#v, want the reported -1", out.ActualThroughput)
				}
			},
		},
		{
			name:     "NaN useful throughput",
			useful:   &usefulNaN,
			reason:   "useful throughput is not a finite non-negative rate",
			preserve: "useful",
			remains: func(t *testing.T, out Outcome) {
				t.Helper()
				if out.UsefulThroughput.State != SignalKnown || !math.IsNaN(out.UsefulThroughput.Value) {
					t.Fatalf("useful throughput = %#v, want the reported NaN", out.UsefulThroughput)
				}
			},
		},
		{
			name:     "negative useful throughput",
			useful:   &negativeUseful,
			reason:   "useful throughput is not a finite non-negative rate",
			preserve: "useful",
			remains: func(t *testing.T, out Outcome) {
				t.Helper()
				if out.UsefulThroughput.State != SignalKnown || out.UsefulThroughput.Value != -1 {
					t.Fatalf("useful throughput = %#v, want the reported -1", out.UsefulThroughput)
				}
			},
		},
		{
			name:     "negative p50",
			mutate:   func(m *Measurements) { m.LatencyP50 = KnownDuration(-time.Millisecond) },
			reason:   "p50 latency is negative",
			preserve: "p50",
			remains: func(t *testing.T, out Outcome) {
				t.Helper()
				if out.Latency.P50.State != SignalKnown || out.Latency.P50.Nanoseconds != int64(-time.Millisecond) {
					t.Fatalf("p50 = %#v, want the reported negative duration", out.Latency.P50)
				}
			},
		},
		{
			name:     "negative p95",
			mutate:   func(m *Measurements) { m.LatencyP95 = KnownDuration(-time.Second) },
			reason:   "p95 latency is negative",
			preserve: "p95",
			remains: func(t *testing.T, out Outcome) {
				t.Helper()
				if out.Latency.P95.State != SignalKnown || out.Latency.P95.Nanoseconds != int64(-time.Second) {
					t.Fatalf("p95 = %#v, want the reported negative duration", out.Latency.P95)
				}
			},
		},
		{
			name:     "negative p99",
			mutate:   func(m *Measurements) { m.LatencyP99 = KnownDuration(-time.Millisecond) },
			reason:   "p99 latency is negative",
			preserve: "p99",
			remains: func(t *testing.T, out Outcome) {
				t.Helper()
				if out.Latency.P99.State != SignalKnown || out.Latency.P99.Nanoseconds != int64(-time.Millisecond) {
					t.Fatalf("p99 = %#v, want the reported negative duration", out.Latency.P99)
				}
			},
		},
		{
			name: "negative status count",
			mutate: func(m *Measurements) {
				m.StatusCodes = map[int]int{200: 0, 404: -2}
			},
			reason:   "HTTP status 404 count is negative",
			preserve: "status",
			remains: func(t *testing.T, out Outcome) {
				t.Helper()
				if out.StatusCodes[200] != 0 || out.StatusCodes[404] != -2 || len(out.StatusCodes) != 2 {
					t.Fatalf("status codes = %#v, want the reported negative count", out.StatusCodes)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			measurements := acceptableZeroMeasurements()
			if tc.mutate != nil {
				tc.mutate(&measurements)
			}
			session, outcome, err := finishZeroRequest(t, adapter, measurements, tc.useful)
			if err != nil {
				t.Fatalf("Finish() error = %v", err)
			}
			again := assertInvalidZeroRequest(t, session, outcome, tc.reason, slo)
			tc.remains(t, outcome)
			tc.remains(t, session.Outcome())
			tc.remains(t, again)
			assertSiblingZerosNormalized(t, outcome, tc.preserve)
		})
	}
}

func finishZeroRequest(t *testing.T, adapter *fakeAdapter, measurements Measurements, useful *Rate) (*Session, Outcome, error) {
	t.Helper()
	session, err := Begin(context.Background(), baseRequest(adapter.spec, nil))
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if err := session.StartPhase(PhaseMeasured); err != nil {
		t.Fatalf("StartPhase() error = %v", err)
	}
	session.SetMeasurements(measurements)
	if useful != nil {
		session.SetUseful(*useful)
	}
	outcome, err := session.Finish(TerminationDurationElapsed, nil)
	return session, outcome, err
}

func acceptableZeroMeasurements() Measurements {
	return Measurements{
		HTTPRequests:     KnownCount(0),
		HTTPErrors:       KnownCount(0),
		HTTPErrorRate:    KnownRatio(0),
		ActualThroughput: KnownRate(0),
		TransportErrors:  MissingCount(),
		LatencyP50:       KnownDuration(0),
		LatencyP95:       KnownDuration(0),
		LatencyP99:       KnownDuration(0),
	}
}

func assertNormalizedZeroRequest(t *testing.T, outcome Outcome) {
	t.Helper()
	if !outcome.Executed || outcome.Partial || outcome.Termination != TerminationDurationElapsed {
		t.Fatalf("run = executed %v partial %v termination %s", outcome.Executed, outcome.Partial, outcome.Termination)
	}
	if outcome.HTTPRequests.State != SignalKnown || outcome.HTTPRequests.Value != 0 || outcome.HTTPErrors.State != SignalKnown || outcome.HTTPErrors.Value != 0 {
		t.Fatalf("requests %#v errors %#v, want known zeros", outcome.HTTPRequests, outcome.HTTPErrors)
	}
	if outcome.HTTPErrorRate.State != SignalMissing || outcome.ActualThroughput.State != SignalMissing || outcome.UsefulThroughput.State != SignalMissing {
		t.Fatalf("rates = error %#v actual %#v useful %#v, want missing", outcome.HTTPErrorRate, outcome.ActualThroughput, outcome.UsefulThroughput)
	}
	if outcome.Latency.P50.State != SignalMissing || outcome.Latency.P95.State != SignalMissing || outcome.Latency.P99.State != SignalMissing {
		t.Fatalf("latency = %#v, want missing", outcome.Latency)
	}
}

func assertStableZeroResult(t *testing.T, session *Session, outcome Outcome) {
	t.Helper()
	observed := session.Outcome()
	if !reflect.DeepEqual(observed.Suitability, outcome.Suitability) || observed.Termination != outcome.Termination {
		t.Fatalf("Outcome() = termination %s suitability %#v, Finish = termination %s suitability %#v", observed.Termination, observed.Suitability, outcome.Termination, outcome.Suitability)
	}
	again, err := session.Finish(TerminationContextCanceled, errors.New("second finish"))
	if err != nil {
		t.Fatalf("second Finish() error = %v, want the original nil error", err)
	}
	if !reflect.DeepEqual(again.Suitability, outcome.Suitability) || again.Termination != outcome.Termination {
		t.Fatalf("second Finish = termination %s suitability %#v, want termination %s suitability %#v", again.Termination, again.Suitability, outcome.Termination, outcome.Suitability)
	}
}

func assertInvalidZeroRequest(t *testing.T, session *Session, outcome Outcome, reason string, slo *SLO) Outcome {
	t.Helper()
	if !outcome.Executed || outcome.Partial || outcome.Termination != TerminationDurationElapsed {
		t.Fatalf("run = executed %v partial %v termination %s, want a completed duration_elapsed run", outcome.Executed, outcome.Partial, outcome.Termination)
	}
	if outcome.Termination == TerminationNotStarted || outcome.Termination == TerminationContextCanceled {
		t.Fatalf("unsuitable run renamed to %s", outcome.Termination)
	}
	if outcome.HTTPRequests.State != SignalKnown || outcome.HTTPRequests.Value != 0 || outcome.HTTPErrors.State != SignalKnown || outcome.HTTPErrors.Value != 0 {
		t.Fatalf("requests %#v errors %#v, want known zeros", outcome.HTTPRequests, outcome.HTTPErrors)
	}
	if outcome.Suitability.Usable || !strings.Contains(strings.Join(outcome.Suitability.Reasons, " "), reason) {
		t.Fatalf("suitability = %#v, want %q", outcome.Suitability, reason)
	}
	observed := session.Outcome()
	if observed.Suitability.Usable || !reflect.DeepEqual(observed.Suitability, outcome.Suitability) {
		t.Fatalf("Outcome() suitability = %#v, want %#v", observed.Suitability, outcome.Suitability)
	}
	again, err := session.Finish(TerminationInternalError, errors.New("second finish"))
	if err != nil {
		t.Fatalf("second Finish() error = %v, want the original nil error", err)
	}
	if again.Suitability.Usable || !reflect.DeepEqual(again.Suitability, outcome.Suitability) || again.Termination != TerminationDurationElapsed {
		t.Fatalf("second Finish = termination %s suitability %#v", again.Termination, again.Suitability)
	}
	assessment, err := EvaluateSLO(again, slo)
	if err != nil || assessment.Passed || assessment.Status != SLOFailed || !strings.Contains(strings.Join(assessment.Violations, " "), reason) {
		t.Fatalf("EvaluateSLO() = %#v, %v, want %q", assessment, err, reason)
	}
	none, err := EvaluateSLO(outcome, nil)
	if err != nil || none.Passed || none.Status != SLONotEvaluated {
		t.Fatalf("nil SLO = %#v, %v, want not_evaluated", none, err)
	}
	return again
}

func assertSiblingZerosNormalized(t *testing.T, outcome Outcome, preserve string) {
	t.Helper()
	if preserve != "error-rate" && outcome.HTTPErrorRate.State != SignalMissing {
		t.Fatalf("error rate = %#v, want a valid zero normalized to missing", outcome.HTTPErrorRate)
	}
	if preserve != "throughput" && outcome.ActualThroughput.State != SignalMissing {
		t.Fatalf("throughput = %#v, want a valid zero normalized to missing", outcome.ActualThroughput)
	}
	if preserve != "useful" && outcome.UsefulThroughput.State != SignalMissing {
		t.Fatalf("useful throughput = %#v, want a valid zero normalized to missing", outcome.UsefulThroughput)
	}
	if preserve != "p50" && outcome.Latency.P50.State != SignalMissing {
		t.Fatalf("p50 = %#v, want a valid zero normalized to missing", outcome.Latency.P50)
	}
	if preserve != "p95" && outcome.Latency.P95.State != SignalMissing {
		t.Fatalf("p95 = %#v, want a valid zero normalized to missing", outcome.Latency.P95)
	}
	if preserve != "p99" && outcome.Latency.P99.State != SignalMissing {
		t.Fatalf("p99 = %#v, want a valid zero normalized to missing", outcome.Latency.P99)
	}
	if preserve != "status" && outcome.StatusCodes != nil {
		t.Fatalf("status codes = %#v, want nil", outcome.StatusCodes)
	}
}
