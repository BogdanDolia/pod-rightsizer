package loadtest

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEvaluateSLOPreservesNumericGates(t *testing.T) {
	slo := &SLO{
		MinimumRPS:           95,
		MaximumHTTPErrorRate: 0.01,
		MaximumP95Latency:    200 * time.Millisecond,
	}
	passing := sampleOutcome()
	assessment, err := EvaluateSLO(passing, slo)
	if err != nil || !assessment.Passed || assessment.Status != SLOPassed {
		t.Fatalf("EvaluateSLO() = %#v, %v, want pass", assessment, err)
	}

	failing := passing
	failing.ActualThroughput = KnownRate(90)
	failing.HTTPErrorRate = KnownRatio(0.02)
	failing.Latency.P95 = KnownDuration(250 * time.Millisecond)
	failing.Termination = TerminationContextCanceled
	assessment, err = EvaluateSLO(failing, slo)
	if err != nil {
		t.Fatalf("EvaluateSLO() error = %v", err)
	}
	if assessment.Passed || assessment.Status != SLOFailed || len(assessment.Violations) != 4 {
		t.Fatalf("EvaluateSLO() = %#v, want four violations", assessment)
	}
	joined := strings.Join(assessment.Violations, " ")
	for _, expected := range []string{
		"termination reason",
		"actual RPS",
		"HTTP error rate",
		"p95 latency",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("violations %q do not contain %q", assessment.Violations, expected)
		}
	}
}

func TestEvaluateSLONilIsNotAPass(t *testing.T) {
	assessment, err := EvaluateSLO(sampleOutcome(), nil)
	if err != nil {
		t.Fatalf("EvaluateSLO() error = %v", err)
	}
	if assessment.Passed || assessment.Status != SLONotEvaluated {
		t.Fatalf("EvaluateSLO() = %#v, want not_evaluated", assessment)
	}
}

func TestEvaluateSLOZeroRequestsAreNotMeasuredRates(t *testing.T) {
	outcome := sampleOutcome()
	outcome.HTTPRequests = KnownCount(0)
	outcome.HTTPErrors = KnownCount(0)
	outcome.ActualThroughput = MissingRate()
	outcome.HTTPErrorRate = MissingRatio()
	outcome.Latency.P50 = MissingDuration()
	outcome.Latency.P95 = MissingDuration()
	outcome.Latency.P99 = MissingDuration()

	assessment, err := EvaluateSLO(outcome, &SLO{
		MinimumRPS:           95,
		MaximumHTTPErrorRate: 0.01,
		MaximumP95Latency:    time.Second,
	})
	if err != nil {
		t.Fatalf("EvaluateSLO() error = %v", err)
	}
	if assessment.Passed {
		t.Fatal("zero requests passed the SLO")
	}
	joined := strings.Join(assessment.Violations, " ")
	if !strings.Contains(joined, "no requests completed") {
		t.Fatalf("violations = %q, want no requests", assessment.Violations)
	}
	if strings.Contains(joined, "0.00") {
		t.Fatalf("violations = %q, want no measured zero rate", assessment.Violations)
	}
}

func TestEvaluateSLORejectsNonFiniteThreshold(t *testing.T) {
	_, err := EvaluateSLO(Outcome{}, &SLO{MinimumRPS: math.NaN()})
	if err == nil || !strings.Contains(err.Error(), "finite non-negative") {
		t.Fatalf("EvaluateSLO() error = %v, want finite threshold error", err)
	}
	_, err = EvaluateSLO(Outcome{}, &SLO{MaximumHTTPErrorRate: math.Inf(1)})
	if err == nil || !strings.Contains(err.Error(), "between 0 and 1") {
		t.Fatalf("EvaluateSLO() error = %v, want ratio error", err)
	}
	_, err = EvaluateSLO(Outcome{}, &SLO{MaximumP95Latency: -time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "must not be negative") {
		t.Fatalf("EvaluateSLO() error = %v, want latency threshold error", err)
	}
}

func TestConformance(t *testing.T) {
	t.Run("other engine succeeds", func(t *testing.T) {
		adapter := newFake(t, "fixture-engine", "9.9.9", successCapabilities(), LatencyUntilResponseHeaders)
		var events []PhaseEvent
		outcome, err := adapter.Run(context.Background(), baseRequest(adapter.spec, func(event PhaseEvent) {
			events = append(events, event)
		}))
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if outcome.Engine.ID != "fixture-engine" || outcome.Adapter.ID != "fixture-adapter" {
			t.Fatalf("identity = %#v / %#v", outcome.Engine, outcome.Adapter)
		}
		if !outcome.Executed || outcome.Partial || !outcome.Suitability.Usable {
			t.Fatalf("outcome = executed %v partial %v suitability %#v", outcome.Executed, outcome.Partial, outcome.Suitability)
		}
		if outcome.Measured == nil || outcome.Measured.FinishedAt.Before(outcome.Measured.StartedAt) {
			t.Fatalf("measured = %#v", outcome.Measured)
		}
		if outcome.Requested.Duration != time.Second || outcome.Measured.StartedAt.IsZero() {
			t.Fatal("requested duration was copied into the measured interval")
		}
		if outcome.HTTPRequests.Value != 4 || outcome.Latency.Semantics != LatencyUntilResponseHeaders {
			t.Fatalf("measurements = %#v semantics %s", outcome.HTTPRequests, outcome.Latency.Semantics)
		}
		if outcome.Checks.State != SignalMissing || outcome.DroppedWork.State != SignalMissing || outcome.GeneratorSaturated.State != SignalMissing {
			t.Fatalf("unsupported signals = checks %#v dropped %#v saturated %#v", outcome.Checks, outcome.DroppedWork, outcome.GeneratorSaturated)
		}
		assertPhaseOrder(t, events, []PhaseName{PhaseMeasured})
		assessment, err := EvaluateSLO(outcome, &SLO{MinimumRPS: 1, MaximumHTTPErrorRate: 0.01, MaximumP95Latency: time.Second})
		if err != nil || !assessment.Passed {
			t.Fatalf("SLO = %#v, %v", assessment, err)
		}
	})

	t.Run("unknown adapter", func(t *testing.T) {
		adapter := newFake(t, "fixture-engine", "1", successCapabilities(), LatencyUntilResponseHeaders)
		req := baseRequest(adapter.spec, nil)
		req.Adapter.ID = "not-registered"
		assertRejectedBeforeStart(t, adapter, req, ErrUnsupportedAdapter)
	})

	t.Run("missing capability before start", func(t *testing.T) {
		adapter := newFake(t, "fixture-engine", "1", successCapabilities(), LatencyUntilResponseHeaders)
		req := baseRequest(adapter.spec, nil)
		req.Required = append(req.Required, CapabilityApplicationChecks)
		assertRejectedBeforeStart(t, adapter, req, ErrMissingCapability)
	})

	t.Run("claimed capability without signal", func(t *testing.T) {
		capabilities := append(successCapabilities(), CapabilityDroppedWork)
		adapter := newFake(t, "fixture-engine", "1", capabilities, LatencyUntilResponseHeaders)
		adapter.behavior = func(_ context.Context, session *Session) (TerminationReason, error) {
			if err := fillSuccess(session); err != nil {
				return TerminationInternalError, err
			}
			return TerminationDurationElapsed, nil
		}
		req := baseRequest(adapter.spec, nil)
		req.Required = append(req.Required, CapabilityDroppedWork)
		outcome, err := adapter.Run(context.Background(), req)
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if outcome.Suitability.Usable || !strings.Contains(strings.Join(outcome.Suitability.Reasons, " "), "dropped_work") {
			t.Fatalf("suitability = %#v, want missing dropped_work", outcome.Suitability)
		}
		if outcome.DroppedWork.State != SignalMissing || outcome.DroppedWork.Value != 0 {
			t.Fatalf("dropped = %#v, want missing rather than a measured zero", outcome.DroppedWork)
		}
	})

	t.Run("unknown and incompatible latency", func(t *testing.T) {
		adapter := newFake(t, "fixture-engine", "1", successCapabilities(), LatencyUntilResponseHeaders)
		req := baseRequest(adapter.spec, nil)
		req.LatencySemantics = LatencyK6HTTPReqDuration
		assertRejectedBeforeStart(t, adapter, req, ErrIncompatibleLatency)

		req = baseRequest(adapter.spec, nil)
		adapter.behavior = func(_ context.Context, session *Session) (TerminationReason, error) {
			if err := session.StartPhase(PhaseMeasured); err != nil {
				return TerminationInternalError, err
			}
			setMeasured(session)
			session.SetLatencySemantics(LatencyUnknown)
			return TerminationDurationElapsed, nil
		}
		outcome, err := adapter.Run(context.Background(), req)
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if outcome.Suitability.Usable || !strings.Contains(strings.Join(outcome.Suitability.Reasons, " "), "unknown") {
			t.Fatalf("suitability = %#v, want unknown semantics", outcome.Suitability)
		}
	})

	t.Run("measured start arrives before run returns", func(t *testing.T) {
		adapter := newFake(t, "fixture-engine", "1", successCapabilities(), LatencyUntilResponseHeaders)
		release := make(chan struct{})
		started := make(chan struct{})
		var returned atomic.Bool
		adapter.behavior = func(ctx context.Context, session *Session) (TerminationReason, error) {
			if err := session.StartPhase(PhaseMeasured); err != nil {
				return TerminationInternalError, err
			}
			select {
			case <-release:
			case <-ctx.Done():
				return TerminationFromContext(ctx), ctx.Err()
			}
			setMeasured(session)
			return TerminationDurationElapsed, nil
		}
		req := baseRequest(adapter.spec, func(event PhaseEvent) {
			if event.Name == PhaseMeasured && event.Edge == EdgeStarted {
				close(started)
			}
		})
		var outcome Outcome
		var err error
		done := make(chan struct{})
		go func() {
			outcome, err = adapter.Run(context.Background(), req)
			returned.Store(true)
			close(done)
		}()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("measured start was not observed")
		}
		if returned.Load() {
			t.Fatal("Run returned before measured start was observed")
		}
		close(release)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Run did not return after release")
		}
		if err != nil || outcome.Measured == nil {
			t.Fatalf("Run() = %#v, %v", outcome.Measured, err)
		}
	})

	t.Run("warmup and cooldown stay outside measured statistics", func(t *testing.T) {
		capabilities := append(successCapabilities(), CapabilityWarmupCooldown)
		adapter := newFake(t, "fixture-engine", "1", capabilities, LatencyUntilResponseHeaders)
		var events []PhaseEvent
		adapter.behavior = func(_ context.Context, session *Session) (TerminationReason, error) {
			for _, name := range []PhaseName{PhaseWarmup, PhaseMeasured, PhaseCooldown} {
				if err := session.StartPhase(name); err != nil {
					return TerminationInternalError, err
				}
				if name == PhaseMeasured {
					setMeasured(session)
				}
				if err := session.FinishPhase(name); err != nil {
					return TerminationInternalError, err
				}
			}
			return TerminationDurationElapsed, nil
		}
		req := baseRequest(adapter.spec, func(event PhaseEvent) {
			events = append(events, event)
		})
		req.Phases = []PhasePlan{
			{Name: PhaseWarmup, Duration: 10 * time.Millisecond},
			{Name: PhaseMeasured, Duration: 10 * time.Millisecond},
			{Name: PhaseCooldown, Duration: 10 * time.Millisecond},
		}
		outcome, err := adapter.Run(context.Background(), req)
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		assertPhaseOrder(t, events, []PhaseName{PhaseWarmup, PhaseMeasured, PhaseCooldown})
		if outcome.HTTPRequests.Value != 4 {
			t.Fatalf("requests = %d, want measured requests only", outcome.HTTPRequests.Value)
		}
		warmup, _ := findPhase(outcome.Phases, PhaseWarmup)
		cooldown, _ := findPhase(outcome.Phases, PhaseCooldown)
		if outcome.Measured == nil || outcome.Measured.StartedAt.Before(warmup.FinishedAt) || cooldown.StartedAt.Before(outcome.Measured.FinishedAt) {
			t.Fatalf("measured %#v warmup %#v cooldown %#v", outcome.Measured, warmup, cooldown)
		}
	})

	t.Run("refusal before start and technical error after start", func(t *testing.T) {
		adapter := newFake(t, "fixture-engine", "1", successCapabilities(), LatencyUntilResponseHeaders)
		adapter.behavior = func(_ context.Context, _ *Session) (TerminationReason, error) {
			return TerminationInvalidConfiguration, errors.New("generator refused")
		}
		outcome, err := adapter.Run(context.Background(), baseRequest(adapter.spec, nil))
		if err == nil || outcome.Executed || outcome.Measured != nil {
			t.Fatalf("before start = executed %v measured %#v err %v", outcome.Executed, outcome.Measured, err)
		}

		adapter.behavior = func(_ context.Context, session *Session) (TerminationReason, error) {
			if err := session.StartPhase(PhaseMeasured); err != nil {
				return TerminationInternalError, err
			}
			session.SetMeasurements(Measurements{
				HTTPRequests:     KnownCount(2),
				HTTPErrors:       KnownCount(1),
				HTTPErrorRate:    KnownRatio(0.5),
				ActualThroughput: KnownRate(2),
				TransportErrors:  MissingCount(),
				LatencyP50:       KnownDuration(time.Millisecond),
				LatencyP95:       KnownDuration(time.Millisecond),
				LatencyP99:       KnownDuration(time.Millisecond),
			})
			return TerminationInternalError, errors.New("generator failed")
		}
		outcome, err = adapter.Run(context.Background(), baseRequest(adapter.spec, nil))
		if err == nil || !outcome.Executed || !outcome.Partial || outcome.HTTPRequests.Value != 2 {
			t.Fatalf("after start = %#v err %v", outcome, err)
		}
	})

	t.Run("cancellation before start and during measured phase", func(t *testing.T) {
		adapter := newFake(t, "fixture-engine", "1", successCapabilities(), LatencyUntilResponseHeaders)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		outcome, err := adapter.Run(ctx, baseRequest(adapter.spec, nil))
		if !errors.Is(err, context.Canceled) || outcome.Executed || outcome.Measured != nil || outcome.HTTPRequests.Value != 0 {
			t.Fatalf("cancel before start = executed %v measured %#v requests %#v err %v", outcome.Executed, outcome.Measured, outcome.HTTPRequests, err)
		}
		if outcome.ActualThroughput.State != SignalMissing || outcome.Latency.P95.State != SignalMissing {
			t.Fatalf("cancel before start invented measurements: %#v %#v", outcome.ActualThroughput, outcome.Latency)
		}

		var running atomic.Int32
		release := make(chan struct{})
		adapter.behavior = func(ctx context.Context, session *Session) (TerminationReason, error) {
			running.Add(1)
			defer running.Add(-1)
			if err := session.StartPhase(PhaseMeasured); err != nil {
				return TerminationInternalError, err
			}
			session.SetMeasurements(Measurements{
				HTTPRequests:     KnownCount(3),
				HTTPErrors:       KnownCount(0),
				HTTPErrorRate:    KnownRatio(0),
				ActualThroughput: KnownRate(3),
				TransportErrors:  MissingCount(),
				LatencyP50:       KnownDuration(time.Millisecond),
				LatencyP95:       KnownDuration(time.Millisecond),
				LatencyP99:       KnownDuration(time.Millisecond),
			})
			select {
			case <-ctx.Done():
			case <-release:
			}
			return TerminationFromContext(ctx), ctx.Err()
		}
		ctx, cancel = context.WithCancel(context.Background())
		started := make(chan struct{})
		req := baseRequest(adapter.spec, func(event PhaseEvent) {
			if event.Name == PhaseMeasured && event.Edge == EdgeStarted {
				close(started)
			}
		})
		done := make(chan struct{})
		go func() {
			outcome, err = adapter.Run(ctx, req)
			close(done)
		}()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("measured phase did not start")
		}
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("cancelled run did not return")
		}
		if !errors.Is(err, context.Canceled) || !outcome.Executed || !outcome.Partial || outcome.Measured == nil || outcome.HTTPRequests.Value != 3 {
			t.Fatalf("cancel during measured = executed %v partial %v measured %#v requests %#v err %v", outcome.Executed, outcome.Partial, outcome.Measured, outcome.HTTPRequests, err)
		}
		if running.Load() != 0 {
			t.Fatal("load was still running after Run returned")
		}
	})

	t.Run("failed checks with successful HTTP", func(t *testing.T) {
		capabilities := append(successCapabilities(), CapabilityApplicationChecks)
		adapter := newFake(t, "fixture-engine", "1", capabilities, LatencyUntilResponseHeaders)
		adapter.behavior = func(_ context.Context, session *Session) (TerminationReason, error) {
			if err := fillSuccess(session); err != nil {
				return TerminationInternalError, err
			}
			session.SetChecks(Checks{State: SignalKnown, Passed: false, Defined: 1, Failed: 1})
			return TerminationDurationElapsed, nil
		}
		req := baseRequest(adapter.spec, nil)
		req.Required = append(req.Required, CapabilityApplicationChecks)
		outcome, err := adapter.Run(context.Background(), req)
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if outcome.HTTPErrorRate.Value != 0 || outcome.Suitability.Usable || !strings.Contains(strings.Join(outcome.Suitability.Reasons, " "), "application checks failed") {
			t.Fatalf("HTTP-clean failed checks = rate %#v suitability %#v", outcome.HTTPErrorRate, outcome.Suitability)
		}
		assessment, evalErr := EvaluateSLO(outcome, &SLO{MinimumRPS: 1, MaximumHTTPErrorRate: 0.01, MaximumP95Latency: time.Second})
		if evalErr != nil || !assessment.Passed {
			t.Fatalf("numeric SLO = %#v, %v, want pass beside failed checks", assessment, evalErr)
		}
	})

	t.Run("saturation and zero requests", func(t *testing.T) {
		capabilities := append(successCapabilities(), CapabilityGeneratorSaturation)
		adapter := newFake(t, "fixture-engine", "1", capabilities, LatencyUntilResponseHeaders)
		adapter.behavior = func(_ context.Context, session *Session) (TerminationReason, error) {
			if err := fillSuccess(session); err != nil {
				return TerminationInternalError, err
			}
			session.SetSaturated(KnownBool(true))
			return TerminationDurationElapsed, nil
		}
		outcome, err := adapter.Run(context.Background(), baseRequest(adapter.spec, nil))
		if err != nil || outcome.Suitability.Usable || !strings.Contains(strings.Join(outcome.Suitability.Reasons, " "), "generator saturated") {
			t.Fatalf("saturation = err %v suitability %#v", err, outcome.Suitability)
		}

		adapter.behavior = func(_ context.Context, session *Session) (TerminationReason, error) {
			if err := session.StartPhase(PhaseMeasured); err != nil {
				return TerminationInternalError, err
			}
			session.SetMeasurements(Measurements{
				HTTPRequests:     KnownCount(0),
				HTTPErrors:       KnownCount(0),
				ActualThroughput: KnownRate(0),
				HTTPErrorRate:    KnownRatio(0),
				TransportErrors:  MissingCount(),
				LatencyP50:       KnownDuration(0),
				LatencyP95:       KnownDuration(0),
				LatencyP99:       KnownDuration(0),
			})
			return TerminationDurationElapsed, nil
		}
		outcome, err = adapter.Run(context.Background(), baseRequest(adapter.spec, nil))
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if outcome.ActualThroughput.State != SignalMissing || outcome.Latency.P95.State != SignalMissing {
			t.Fatalf("zero requests produced known signals: rate %#v latency %#v", outcome.ActualThroughput, outcome.Latency.P95)
		}
		assessment, evalErr := EvaluateSLO(outcome, &SLO{MinimumRPS: 1, MaximumHTTPErrorRate: 1, MaximumP95Latency: time.Second})
		if evalErr != nil || assessment.Passed || !strings.Contains(strings.Join(assessment.Violations, " "), "no requests completed") {
			t.Fatalf("zero-request SLO = %#v, %v", assessment, evalErr)
		}
	})

	t.Run("missing and unknown stay distinct", func(t *testing.T) {
		adapter := newFake(t, "fixture-engine", "1", successCapabilities(), LatencyUntilResponseHeaders)
		outcome, err := adapter.Run(context.Background(), baseRequest(adapter.spec, nil))
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if outcome.DroppedWork.State != SignalMissing || outcome.GeneratorSaturated.State != SignalMissing || outcome.Checks.State != SignalMissing {
			t.Fatalf("unsupported = dropped %#v saturated %#v checks %#v", outcome.DroppedWork, outcome.GeneratorSaturated, outcome.Checks)
		}
		if outcome.GeneratorSaturated.Value || outcome.Checks.Passed {
			t.Fatal("missing signals were read as false or passed")
		}

		capabilities := append(successCapabilities(), CapabilityDroppedWork)
		adapter = newFake(t, "other-engine", "1", capabilities, LatencyUntilResponseHeaders)
		adapter.behavior = func(_ context.Context, session *Session) (TerminationReason, error) {
			if err := fillSuccess(session); err != nil {
				return TerminationInternalError, err
			}
			session.SetDropped(UnknownCount())
			return TerminationDurationElapsed, nil
		}
		req := baseRequest(adapter.spec, nil)
		req.Required = append(req.Required, CapabilityDroppedWork)
		outcome, err = adapter.Run(context.Background(), req)
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if outcome.DroppedWork.State != SignalUnknown || outcome.Suitability.Usable {
			t.Fatalf("unknown dropped = %#v suitability %#v", outcome.DroppedWork, outcome.Suitability)
		}
		if !strings.Contains(strings.Join(outcome.Suitability.Reasons, " "), "unknown required signal") {
			t.Fatalf("reasons = %#v, want unknown rather than missing", outcome.Suitability.Reasons)
		}
	})

	t.Run("measured phase is required without a callback", func(t *testing.T) {
		testMeasuredPhaseRequired(t)
	})

	t.Run("warmup prefix stays diagnostic", func(t *testing.T) {
		testWarmupPrefix(t)
	})

	t.Run("unknown latency keeps positive requests", func(t *testing.T) {
		testUnknownLatencyWithRequests(t)
	})

	t.Run("invalid reported facts", func(t *testing.T) {
		testReportedFacts(t)
	})
}

func TestImportsStayEngineNeutral(t *testing.T) {
	// The conformance fake above is the other engine. This test guards the
	// package sources against a generator, k6, or a process runner import.
	assertPackageImports(t)
}

type fakeAdapter struct {
	spec     AdapterSpec
	behavior func(context.Context, *Session) (TerminationReason, error)
}

func newFake(t *testing.T, engineID, engineVersion string, capabilities []Capability, semantics LatencySemantics) *fakeAdapter {
	t.Helper()
	adapter := &fakeAdapter{
		spec: AdapterSpec{
			Adapter:          ComponentIdentity{ID: "fixture-adapter", Version: "1"},
			Engine:           ComponentIdentity{ID: engineID, Version: engineVersion},
			Capabilities:     capabilities,
			LatencySemantics: semantics,
			ContractVersion:  ContractVersion,
		},
		behavior: func(_ context.Context, session *Session) (TerminationReason, error) {
			if err := fillSuccess(session); err != nil {
				return TerminationInternalError, err
			}
			return TerminationDurationElapsed, nil
		},
	}
	if engineID != "fixture-engine" {
		adapter.spec.Adapter.ID = engineID + "-adapter"
	}
	if err := Register(adapter.spec); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	t.Cleanup(func() { unregister(adapter.spec.Adapter.ID) })
	return adapter
}

func (f *fakeAdapter) Run(ctx context.Context, req Request) (Outcome, error) {
	if req.Adapter.ID == "" {
		req.Adapter = f.spec.Adapter
	}
	session, err := Begin(ctx, req)
	if err != nil {
		return session.Outcome(), err
	}
	reason, runErr := f.behavior(ctx, session)
	return session.Finish(reason, runErr)
}

func fillSuccess(session *Session) error {
	if err := session.StartPhase(PhaseMeasured); err != nil {
		return err
	}
	setMeasured(session)
	return nil
}

func setMeasured(session *Session) {
	session.SetMeasurements(Measurements{
		HTTPRequests:     KnownCount(4),
		HTTPErrors:       KnownCount(0),
		HTTPErrorRate:    KnownRatio(0),
		ActualThroughput: KnownRate(4),
		TransportErrors:  MissingCount(),
		LatencyP50:       KnownDuration(time.Millisecond),
		LatencyP95:       KnownDuration(2 * time.Millisecond),
		LatencyP99:       KnownDuration(3 * time.Millisecond),
	})
}

func successCapabilities() []Capability {
	return []Capability{
		CapabilityHTTPRequests,
		CapabilityHTTPErrors,
		CapabilityActualHTTPThroughput,
		CapabilityLatencyUntilHeaders,
		CapabilityMeasuredPhase,
		CapabilityCancellation,
	}
}

func baseRequest(spec AdapterSpec, observer PhaseObserver) Request {
	return Request{
		TargetURL:         "http://example.test/items",
		Shape:             ShapeRPS,
		RequestedRPS:      4,
		RequestedDuration: time.Second,
		Scenario:          ScenarioRef{ID: "items-get", Version: "1", VersionKnown: true},
		Adapter:           spec.Adapter,
		Required: []Capability{
			CapabilityHTTPRequests,
			CapabilityHTTPErrors,
			CapabilityActualHTTPThroughput,
			CapabilityLatencyUntilHeaders,
		},
		LatencySemantics: spec.LatencySemantics,
		OnPhase:          observer,
	}
}

func assertRejectedBeforeStart(t *testing.T, adapter *fakeAdapter, req Request, sentinel error) {
	t.Helper()
	var events int
	if req.OnPhase == nil {
		req.OnPhase = func(PhaseEvent) { events++ }
	}
	outcome, err := adapter.Run(context.Background(), req)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Run() error = %v, want %v", err, sentinel)
	}
	if outcome.Executed || outcome.Measured != nil || events != 0 {
		t.Fatalf("rejected run started: executed %v measured %#v events %d", outcome.Executed, outcome.Measured, events)
	}
	if outcome.HTTPRequests.Value != 0 || outcome.ActualThroughput.State != SignalMissing {
		t.Fatalf("rejected run invented measurements: %#v %#v", outcome.HTTPRequests, outcome.ActualThroughput)
	}
}

func assertPhaseOrder(t *testing.T, events []PhaseEvent, names []PhaseName) {
	t.Helper()
	if len(events) != len(names)*2 {
		t.Fatalf("events = %#v, want start and finish for %v", events, names)
	}
	for i, name := range names {
		start := events[i*2]
		finish := events[i*2+1]
		if start.Name != name || start.Edge != EdgeStarted || finish.Name != name || finish.Edge != EdgeFinished {
			t.Fatalf("phase %d = %#v %#v, want %s start then finish", i, start, finish, name)
		}
		if !finish.At.After(start.At) && !finish.At.Equal(start.At) {
			t.Fatalf("phase %s finished at %s before start %s", name, finish.At, start.At)
		}
		if start.Clock != ClockCollectorLocal {
			t.Fatalf("clock = %s", start.Clock)
		}
	}
}

func testMeasuredPhaseRequired(t *testing.T) {
	t.Helper()
	capabilities := append(successCapabilities(), CapabilityWarmupCooldown)
	adapter := newFake(t, "fixture-engine", "1", capabilities, LatencyUntilResponseHeaders)
	req := baseRequest(adapter.spec, nil)
	req.OnPhase = nil
	req.RequestedRPS = 100
	req.Phases = []PhasePlan{
		{Name: PhaseWarmup, Duration: 10 * time.Millisecond},
		{Name: PhaseMeasured, Duration: 10 * time.Millisecond},
	}
	adapter.behavior = func(_ context.Context, session *Session) (TerminationReason, error) {
		if err := session.StartPhase(PhaseWarmup); err != nil {
			return TerminationInternalError, err
		}
		session.SetMeasurements(validMeasurements())
		if err := session.FinishPhase(PhaseWarmup); err != nil {
			return TerminationInternalError, err
		}
		return TerminationDurationElapsed, nil
	}
	outcome, err := adapter.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !outcome.Executed || outcome.Partial || outcome.Termination != TerminationDurationElapsed || outcome.Measured != nil || outcome.Suitability.Usable {
		t.Fatalf("warmup-only success = executed %v partial %v termination %s measured %#v suitability %#v", outcome.Executed, outcome.Partial, outcome.Termination, outcome.Measured, outcome.Suitability)
	}
	if !strings.Contains(strings.Join(outcome.Suitability.Reasons, " "), "measured phase is missing") {
		t.Fatalf("reasons = %#v, want missing measured phase", outcome.Suitability.Reasons)
	}
	if outcome.HTTPRequests.Value != 100 || len(outcome.Phases) != 1 || outcome.Phases[0].Name != PhaseWarmup {
		t.Fatalf("facts were rewritten: requests %#v phases %#v", outcome.HTTPRequests, outcome.Phases)
	}
	assessment, evalErr := EvaluateSLO(outcome, &SLO{MinimumRPS: 90, MaximumHTTPErrorRate: 0, MaximumP95Latency: time.Second})
	if evalErr != nil || !assessment.Passed {
		t.Fatalf("numeric SLO = %#v, %v, want pass beside the missing measured phase", assessment, evalErr)
	}
	direct := AssessSuitability(req, adapter.spec, outcome)
	if direct.Usable || !strings.Contains(strings.Join(direct.Reasons, " "), "measured phase is missing") {
		t.Fatalf("direct suitability = %#v", direct)
	}

	adapter.behavior = func(_ context.Context, session *Session) (TerminationReason, error) {
		for _, name := range []PhaseName{PhaseWarmup, PhaseMeasured} {
			if err := session.StartPhase(name); err != nil {
				return TerminationInternalError, err
			}
			if name == PhaseMeasured {
				session.SetMeasurements(validMeasurements())
			}
			if err := session.FinishPhase(name); err != nil {
				return TerminationInternalError, err
			}
		}
		return TerminationDurationElapsed, nil
	}
	outcome, err = adapter.Run(context.Background(), req)
	if err != nil || !outcome.Suitability.Usable || outcome.Measured == nil {
		t.Fatalf("completed measured phase = err %v measured %#v suitability %#v", err, outcome.Measured, outcome.Suitability)
	}
	measured, ok := findPhase(outcome.Phases, PhaseMeasured)
	if !ok || !outcome.Measured.StartedAt.Equal(measured.StartedAt) || !outcome.Measured.FinishedAt.Equal(measured.FinishedAt) {
		t.Fatalf("measured %#v phase %#v", outcome.Measured, measured)
	}

	valid := validMeasuredOutcome()
	if suitability := AssessSuitability(Request{}, AdapterSpec{}, valid); !suitability.Usable {
		t.Fatalf("valid measured outcome = %#v", suitability)
	}
	missing := cloneOutcome(valid)
	missing.Phases = nil
	missing.Measured = nil
	assertUnusable(t, Request{}, missing, "measured phase is missing")

	unfinished := cloneOutcome(valid)
	unfinished.Phases[0].FinishedAt = time.Time{}
	unfinished.Measured = nil
	assertUnusable(t, Request{}, unfinished, "measured phase did not finish")

	repeated := cloneOutcome(valid)
	repeated.Phases = append(repeated.Phases, repeated.Phases[0])
	assertUnusable(t, Request{}, repeated, "measured phase is repeated")

	contradictory := cloneOutcome(valid)
	contradictory.Measured.FinishedAt = contradictory.Measured.StartedAt.Add(10 * time.Millisecond)
	assertUnusable(t, Request{}, contradictory, "measured interval does not match the measured phase")
}

func testWarmupPrefix(t *testing.T) {
	t.Helper()
	capabilities := append(successCapabilities(), CapabilityWarmupCooldown)
	adapter := newFake(t, "fixture-engine", "1", capabilities, LatencyUntilResponseHeaders)
	req := baseRequest(adapter.spec, nil)
	req.OnPhase = nil
	req.Phases = []PhasePlan{
		{Name: PhaseWarmup, Duration: 10 * time.Millisecond},
		{Name: PhaseMeasured, Duration: 10 * time.Millisecond},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	adapter.behavior = func(ctx context.Context, session *Session) (TerminationReason, error) {
		if err := session.StartPhase(PhaseWarmup); err != nil {
			return TerminationInternalError, err
		}
		session.SetMeasurements(zeroRequestMeasurements())
		cancel()
		return TerminationFromContext(ctx), ctx.Err()
	}
	outcome, err := adapter.Run(ctx, req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want cancellation", err)
	}
	assertWarmupPrefix(t, outcome, TerminationContextCanceled)

	adapter.behavior = func(_ context.Context, session *Session) (TerminationReason, error) {
		if err := session.StartPhase(PhaseWarmup); err != nil {
			return TerminationInternalError, err
		}
		session.SetMeasurements(zeroRequestMeasurements())
		if err := session.FinishPhase(PhaseWarmup); err != nil {
			return TerminationInternalError, err
		}
		return TerminationInternalError, errors.New("generator failed during warmup")
	}
	outcome, err = adapter.Run(context.Background(), req)
	if err == nil {
		t.Fatal("Run() error = nil, want warmup failure")
	}
	assertWarmupPrefix(t, outcome, TerminationInternalError)
}

func testUnknownLatencyWithRequests(t *testing.T) {
	t.Helper()
	adapter := newFake(t, "fixture-engine", "1", successCapabilities(), LatencyUntilResponseHeaders)
	adapter.behavior = func(_ context.Context, session *Session) (TerminationReason, error) {
		if err := session.StartPhase(PhaseMeasured); err != nil {
			return TerminationInternalError, err
		}
		session.SetMeasurements(Measurements{
			HTTPRequests:     KnownCount(100),
			HTTPErrors:       KnownCount(0),
			HTTPErrorRate:    KnownRatio(0),
			ActualThroughput: KnownRate(100),
			TransportErrors:  MissingCount(),
			LatencyP50:       MissingDuration(),
			LatencyP95:       MissingDuration(),
			LatencyP99:       MissingDuration(),
		})
		session.SetLatencySemantics(LatencyUnknown)
		return TerminationDurationElapsed, nil
	}
	req := baseRequest(adapter.spec, nil)
	req.OnPhase = nil
	outcome, err := adapter.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if outcome.Latency.Semantics != LatencyUnknown {
		t.Fatalf("semantics = %s, want unknown", outcome.Latency.Semantics)
	}
	if outcome.HTTPRequests.Value != 100 || outcome.ActualThroughput.State != SignalKnown || outcome.ActualThroughput.Value != 100 {
		t.Fatalf("requests %#v throughput %#v, want the reported facts kept", outcome.HTTPRequests, outcome.ActualThroughput)
	}
	for _, percentile := range []DurationSignal{outcome.Latency.P50, outcome.Latency.P95, outcome.Latency.P99} {
		if percentile.State != SignalMissing {
			t.Fatalf("percentile = %#v, want missing_signal rather than a known number", percentile)
		}
	}
	if outcome.Suitability.Usable || !strings.Contains(strings.Join(outcome.Suitability.Reasons, " "), "unknown") {
		t.Fatalf("suitability = %#v, want unknown semantics", outcome.Suitability)
	}
	assessment, evalErr := EvaluateSLO(outcome, &SLO{MinimumRPS: 90, MaximumHTTPErrorRate: 0, MaximumP95Latency: time.Second})
	if evalErr != nil || assessment.Passed || !strings.Contains(strings.Join(assessment.Violations, " "), "p95 latency") {
		t.Fatalf("active latency SLO = %#v, %v", assessment, evalErr)
	}
}

func testReportedFacts(t *testing.T) {
	t.Helper()
	active := SLO{MinimumRPS: 90, MaximumHTTPErrorRate: 0, MaximumP95Latency: time.Second}
	cases := []struct {
		name   string
		mutate func(*Outcome)
		slo    SLO
		reason string
		pass   bool
	}{
		{
			name:   "negative error ratio",
			mutate: func(out *Outcome) { out.HTTPErrorRate = KnownRatio(-1) },
			slo:    SLO{MinimumRPS: 90, MaximumHTTPErrorRate: 1, MaximumP95Latency: time.Second},
			reason: "HTTP error rate is not a finite ratio from 0 to 1",
		},
		{
			name:   "negative p95",
			mutate: func(out *Outcome) { out.Latency.P95 = KnownDuration(-time.Second) },
			slo:    SLO{MinimumRPS: 90, MaximumHTTPErrorRate: 0, MaximumP95Latency: 0},
			reason: "p95 latency is negative",
		},
		{
			name: "errors exceed requests",
			mutate: func(out *Outcome) {
				out.HTTPRequests = KnownCount(100)
				out.HTTPErrors = KnownCount(200)
				out.HTTPErrorRate = KnownRatio(0)
			},
			slo:    active,
			reason: "HTTP errors exceed HTTP requests",
		},
		{
			name:   "negative request count",
			mutate: func(out *Outcome) { out.HTTPRequests = KnownCount(-1) },
			slo:    active,
			reason: "HTTP requests count is negative",
		},
		{
			name:   "negative error count",
			mutate: func(out *Outcome) { out.HTTPErrors = KnownCount(-1) },
			slo:    active,
			reason: "HTTP errors count is negative",
		},
		{
			name:   "negative transport errors",
			mutate: func(out *Outcome) { out.TransportErrors = KnownCount(-1) },
			slo:    active,
			reason: "transport errors count is negative",
		},
		{
			name:   "negative status count",
			mutate: func(out *Outcome) { out.StatusCodes = map[int]int{200: -1} },
			slo:    active,
			reason: "HTTP status 200 count is negative",
		},
		{
			name:   "negative p50",
			mutate: func(out *Outcome) { out.Latency.P50 = KnownDuration(-time.Millisecond) },
			slo:    active,
			reason: "p50 latency is negative",
		},
		{
			name:   "error ratio above one",
			mutate: func(out *Outcome) { out.HTTPErrorRate = KnownRatio(1.1) },
			slo:    SLO{MinimumRPS: 90, MaximumHTTPErrorRate: 1, MaximumP95Latency: time.Second},
			reason: "HTTP error rate is not a finite ratio from 0 to 1",
		},
		{
			name:   "negative dropped work",
			mutate: func(out *Outcome) { out.DroppedWork = KnownCount(-1) },
			slo:    active,
			reason: "dropped work count is negative",
		},
		{
			name:   "negative throughput",
			mutate: func(out *Outcome) { out.ActualThroughput = KnownRate(-1) },
			slo:    SLO{MinimumRPS: 0, MaximumHTTPErrorRate: 0, MaximumP95Latency: time.Second},
			reason: "actual throughput is not a finite non-negative rate",
		},
		{
			name:   "negative useful throughput",
			mutate: func(out *Outcome) { out.UsefulThroughput = KnownRate(-1) },
			slo:    active,
			reason: "useful throughput is not a finite non-negative rate",
		},
		{
			name:   "NaN throughput",
			mutate: func(out *Outcome) { out.ActualThroughput = KnownRate(math.NaN()) },
			slo:    active,
			reason: "actual throughput is not a finite non-negative rate",
		},
		{
			name:   "infinite throughput",
			mutate: func(out *Outcome) { out.ActualThroughput = KnownRate(math.Inf(1)) },
			slo:    active,
			reason: "actual throughput is not a finite non-negative rate",
		},
		{
			name:   "negative infinite throughput",
			mutate: func(out *Outcome) { out.ActualThroughput = KnownRate(math.Inf(-1)) },
			slo:    active,
			reason: "actual throughput is not a finite non-negative rate",
		},
		{
			name:   "NaN error ratio",
			mutate: func(out *Outcome) { out.HTTPErrorRate = KnownRatio(math.NaN()) },
			slo:    active,
			reason: "HTTP error rate is not a finite ratio from 0 to 1",
		},
		{
			name:   "infinite error ratio",
			mutate: func(out *Outcome) { out.HTTPErrorRate = KnownRatio(math.Inf(1)) },
			slo:    active,
			reason: "HTTP error rate is not a finite ratio from 0 to 1",
		},
		{
			name:   "zero error ratio",
			mutate: func(out *Outcome) { out.HTTPErrorRate = KnownRatio(0) },
			slo:    active,
			pass:   true,
		},
		{
			name: "error ratio one",
			mutate: func(out *Outcome) {
				out.HTTPErrors = KnownCount(100)
				out.HTTPErrorRate = KnownRatio(1)
			},
			slo:  SLO{MinimumRPS: 90, MaximumHTTPErrorRate: 1, MaximumP95Latency: time.Second},
			pass: true,
		},
		{
			name: "zero latency",
			mutate: func(out *Outcome) {
				out.Latency.P50 = KnownDuration(0)
				out.Latency.P95 = KnownDuration(0)
				out.Latency.P99 = KnownDuration(0)
			},
			slo:  active,
			pass: true,
		},
		{
			name:   "zero throughput with the rate gate disabled",
			mutate: func(out *Outcome) { out.ActualThroughput = KnownRate(0) },
			slo:    SLO{MinimumRPS: 0, MaximumHTTPErrorRate: 0, MaximumP95Latency: time.Second},
			pass:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := cloneOutcome(validMeasuredOutcome())
			tc.mutate(&out)
			suitability := AssessSuitability(Request{}, AdapterSpec{}, out)
			assessment, err := EvaluateSLO(out, &tc.slo)
			if err != nil {
				t.Fatalf("EvaluateSLO() error = %v, want an assessment", err)
			}
			if tc.pass {
				if !suitability.Usable || !assessment.Passed || assessment.Status != SLOPassed {
					t.Fatalf("suitability %#v SLO %#v, want usable and passed", suitability, assessment)
				}
				return
			}
			if suitability.Usable || !strings.Contains(strings.Join(suitability.Reasons, " "), tc.reason) {
				t.Fatalf("suitability = %#v, want %q", suitability, tc.reason)
			}
			if assessment.Passed || assessment.Status != SLOFailed || !strings.Contains(strings.Join(assessment.Violations, " "), tc.reason) {
				t.Fatalf("SLO = %#v, want %q", assessment, tc.reason)
			}
		})
	}

	bare := sampleOutcome()
	bare.HTTPErrorRate = KnownRatio(-1)
	assessment, err := EvaluateSLO(bare, &SLO{MinimumRPS: 90, MaximumHTTPErrorRate: 1, MaximumP95Latency: 0})
	if err != nil || assessment.Passed || !strings.Contains(strings.Join(assessment.Violations, " "), "HTTP error rate") {
		t.Fatalf("phaseless EvaluateSLO() = %#v, %v", assessment, err)
	}
	if strings.Contains(strings.Join(assessment.Violations, " "), "phase") {
		t.Fatalf("fact check required phases: %q", assessment.Violations)
	}
	assessment, err = EvaluateSLO(bare, nil)
	if err != nil || assessment.Passed || assessment.Status != SLONotEvaluated {
		t.Fatalf("nil SLO = %#v, %v, want not_evaluated", assessment, err)
	}
	_, err = EvaluateSLO(bare, &SLO{MinimumRPS: math.NaN()})
	if err == nil || !strings.Contains(err.Error(), "finite non-negative") {
		t.Fatalf("threshold error = %v, want the documented threshold error", err)
	}
}

func assertUnusable(t *testing.T, req Request, out Outcome, reason string) {
	t.Helper()
	suitability := AssessSuitability(req, AdapterSpec{}, out)
	if suitability.Usable || !strings.Contains(strings.Join(suitability.Reasons, " "), reason) {
		t.Fatalf("suitability = %#v, want %q", suitability, reason)
	}
}

func assertWarmupPrefix(t *testing.T, outcome Outcome, reason TerminationReason) {
	t.Helper()
	if !outcome.Executed || !outcome.Partial || outcome.Termination != reason || outcome.Suitability.Usable || outcome.Measured != nil {
		t.Fatalf("prefix = executed %v partial %v termination %s measured %#v suitability %#v", outcome.Executed, outcome.Partial, outcome.Termination, outcome.Measured, outcome.Suitability)
	}
	if strings.Contains(strings.Join(outcome.Suitability.Reasons, " "), "measured phase is missing") {
		t.Fatalf("reasons = %#v, cancellation should keep the prefix diagnostic", outcome.Suitability.Reasons)
	}
	if len(outcome.Phases) != 1 || outcome.Phases[0].Name != PhaseWarmup {
		t.Fatalf("phases = %#v, want the warmup phase that ran", outcome.Phases)
	}
	phase := outcome.Phases[0]
	if phase.StartedAt.IsZero() || phase.FinishedAt.IsZero() || phase.FinishedAt.Before(phase.StartedAt) || phase.Clock != ClockCollectorLocal {
		t.Fatalf("warmup phase = %#v", phase)
	}
	if outcome.HTTPRequests.State != SignalKnown || outcome.HTTPRequests.Value != 0 || outcome.HTTPErrors.Value != 0 {
		t.Fatalf("requests = %#v errors %#v", outcome.HTTPRequests, outcome.HTTPErrors)
	}
	if outcome.ActualThroughput.State != SignalMissing || outcome.HTTPErrorRate.State != SignalMissing || outcome.Latency.P95.State != SignalMissing {
		t.Fatalf("rates were invented: throughput %#v error rate %#v p95 %#v", outcome.ActualThroughput, outcome.HTTPErrorRate, outcome.Latency.P95)
	}
}

func validMeasurements() Measurements {
	return Measurements{
		HTTPRequests:     KnownCount(100),
		HTTPErrors:       KnownCount(0),
		HTTPErrorRate:    KnownRatio(0),
		ActualThroughput: KnownRate(100),
		TransportErrors:  MissingCount(),
		LatencyP50:       KnownDuration(time.Millisecond),
		LatencyP95:       KnownDuration(time.Millisecond),
		LatencyP99:       KnownDuration(time.Millisecond),
	}
}

func zeroRequestMeasurements() Measurements {
	return Measurements{
		HTTPRequests:     KnownCount(0),
		HTTPErrors:       KnownCount(0),
		HTTPErrorRate:    MissingRatio(),
		ActualThroughput: MissingRate(),
		TransportErrors:  MissingCount(),
		LatencyP50:       MissingDuration(),
		LatencyP95:       MissingDuration(),
		LatencyP99:       MissingDuration(),
	}
}

func validMeasuredOutcome() Outcome {
	started := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	finished := started.Add(time.Second)
	return Outcome{
		Executed:         true,
		Termination:      TerminationDurationElapsed,
		HTTPRequests:     KnownCount(100),
		HTTPErrors:       KnownCount(0),
		HTTPErrorRate:    KnownRatio(0),
		ActualThroughput: KnownRate(100),
		UsefulThroughput: KnownRate(100),
		Latency: LatencyReport{
			Semantics: LatencyUntilResponseHeaders,
			P50:       KnownDuration(time.Millisecond),
			P95:       KnownDuration(time.Millisecond),
			P99:       KnownDuration(time.Millisecond),
		},
		Phases: []PhaseActual{{
			Name:       PhaseMeasured,
			StartedAt:  started,
			FinishedAt: finished,
			Clock:      ClockCollectorLocal,
		}},
		Measured: &Interval{
			StartedAt:  started,
			FinishedAt: finished,
			Clock:      ClockCollectorLocal,
		},
	}
}

func cloneOutcome(out Outcome) Outcome {
	out.Phases = append([]PhaseActual(nil), out.Phases...)
	if out.Measured != nil {
		measured := *out.Measured
		out.Measured = &measured
	}
	return out
}

func sampleOutcome() Outcome {
	return Outcome{
		Executed:         true,
		Termination:      TerminationDurationElapsed,
		HTTPRequests:     KnownCount(100),
		HTTPErrors:       KnownCount(1),
		HTTPErrorRate:    KnownRatio(0.01),
		ActualThroughput: KnownRate(100),
		Latency: LatencyReport{
			Semantics: LatencyUntilResponseHeaders,
			P50:       KnownDuration(20 * time.Millisecond),
			P95:       KnownDuration(200 * time.Millisecond),
			P99:       KnownDuration(300 * time.Millisecond),
		},
	}
}
