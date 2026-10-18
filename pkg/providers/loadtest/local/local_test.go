package local

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contract "github.com/HelixThinker/pod-rightsizer/pkg/providers/loadtest"
)

func TestRunKeepsTypedOutcome(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	var events []contract.PhaseEvent
	outcome, err := New().Run(context.Background(), contract.Request{
		TargetURL:         server.URL,
		Shape:             contract.ShapeRPS,
		RequestedRPS:      200,
		RequestedDuration: 100 * time.Millisecond,
		Scenario:          contract.ScenarioRef{ID: "legacy-get"},
		LatencySemantics:  contract.LatencyUntilResponseHeaders,
		OnPhase: func(event contract.PhaseEvent) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !outcome.Executed || outcome.Partial || outcome.Termination != contract.TerminationDurationElapsed {
		t.Fatalf("execution = executed %v partial %v termination %s", outcome.Executed, outcome.Partial, outcome.Termination)
	}
	if outcome.HTTPRequests.State != contract.SignalKnown || outcome.HTTPRequests.Value == 0 {
		t.Fatalf("requests = %#v, want a measured count", outcome.HTTPRequests)
	}
	if outcome.Adapter.ID != AdapterID || outcome.Engine.ID != EngineID || outcome.ContractVersion != contract.ContractVersion {
		t.Fatalf("identity = adapter %#v engine %#v contract %s", outcome.Adapter, outcome.Engine, outcome.ContractVersion)
	}
	if outcome.Latency.Semantics != contract.LatencyUntilResponseHeaders {
		t.Fatalf("semantics = %s", outcome.Latency.Semantics)
	}
	if outcome.Measured == nil || outcome.Measured.Clock != contract.ClockCollectorLocal || outcome.Measured.FinishedAt.Before(outcome.Measured.StartedAt) {
		t.Fatalf("measured = %#v", outcome.Measured)
	}
	if outcome.Requested.Duration != 100*time.Millisecond {
		t.Fatalf("requested duration = %s", outcome.Requested.Duration)
	}
	if outcome.Checks.State != contract.SignalMissing || outcome.DroppedWork.State != contract.SignalMissing || outcome.GeneratorSaturated.State != contract.SignalMissing || outcome.UsefulThroughput.State != contract.SignalMissing {
		t.Fatalf("unsupported signals = checks %#v dropped %#v saturated %#v useful %#v", outcome.Checks, outcome.DroppedWork, outcome.GeneratorSaturated, outcome.UsefulThroughput)
	}
	if outcome.Checks.Passed || outcome.GeneratorSaturated.Value || outcome.DroppedWork.Value != 0 && outcome.DroppedWork.State == contract.SignalKnown {
		t.Fatal("unsupported signals were reported as measured zeros or passes")
	}
	if len(events) != 2 || events[0].Edge != contract.EdgeStarted || events[1].Edge != contract.EdgeFinished {
		t.Fatalf("events = %#v, want measured start and finish", events)
	}
}

func TestRunRejectsWarmupBeforeStart(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer server.Close()

	outcome, err := New().Run(context.Background(), contract.Request{
		TargetURL:        server.URL,
		Shape:            contract.ShapeRPS,
		RequestedRPS:     1,
		LatencySemantics: contract.LatencyUntilResponseHeaders,
		Phases: []contract.PhasePlan{
			{Name: contract.PhaseWarmup, Duration: 10 * time.Millisecond},
			{Name: contract.PhaseMeasured, Duration: 10 * time.Millisecond},
		},
	})
	if !errors.Is(err, contract.ErrMissingCapability) {
		t.Fatalf("Run() error = %v, want missing warmup capability", err)
	}
	if outcome.Executed || called {
		t.Fatalf("warmup request started: executed %v called %v", outcome.Executed, called)
	}
}

func TestRunCancellationBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	outcome, err := New().Run(ctx, contract.Request{
		TargetURL:         "http://example.test",
		Shape:             contract.ShapeRPS,
		RequestedRPS:      1,
		RequestedDuration: time.Second,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want cancellation", err)
	}
	if outcome.Executed || outcome.Measured != nil || outcome.Termination != contract.TerminationContextCanceled {
		t.Fatalf("outcome = executed %v termination %s measured %#v", outcome.Executed, outcome.Termination, outcome.Measured)
	}
	if outcome.ActualThroughput.State != contract.SignalMissing {
		t.Fatalf("throughput = %#v, want missing", outcome.ActualThroughput)
	}
}

func TestRunDoesNotReadResponseBodyIntoLatency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = io.Copy(w, strings.NewReader("body"))
	}))
	defer server.Close()

	outcome, err := New().Run(context.Background(), contract.Request{
		TargetURL:         server.URL,
		Shape:             contract.ShapeConcurrency,
		Concurrency:       1,
		RequestedDuration: 30 * time.Millisecond,
		LatencySemantics:  contract.LatencyUntilResponseHeaders,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if outcome.Latency.Semantics != contract.LatencyUntilResponseHeaders {
		t.Fatalf("semantics = %s, want until_response_headers", outcome.Latency.Semantics)
	}
}
