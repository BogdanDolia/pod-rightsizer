package loadtest

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMetricsRunResultContainsTypedLoadTestStatistics(t *testing.T) {
	metrics := Metrics{TestDuration: 2 * time.Second}
	for _, result := range []RequestResult{
		{Latency: time.Millisecond, StatusCode: http.StatusOK},
		{Latency: 2 * time.Millisecond, StatusCode: http.StatusNoContent},
		{Latency: 3 * time.Millisecond, StatusCode: http.StatusNotFound},
		{Latency: 4 * time.Millisecond, Error: errors.New("connection reset")},
	} {
		metrics.Add(&result)
	}

	result := metrics.RunResult(TerminationDurationElapsed)
	if result.Requests != 4 || result.HTTPErrors != 2 {
		t.Fatalf("requests/errors = %d/%d, want 4/2", result.Requests, result.HTTPErrors)
	}
	if result.ActualRPS != 2 {
		t.Fatalf("ActualRPS = %.2f, want 2", result.ActualRPS)
	}
	if result.HTTPErrorRate != 0.5 {
		t.Fatalf("HTTPErrorRate = %.2f, want 0.5", result.HTTPErrorRate)
	}
	if result.P50Latency != 2*time.Millisecond {
		t.Fatalf("P50Latency = %s, want 2ms", result.P50Latency)
	}
	if result.P95Latency != 4*time.Millisecond || result.P99Latency != 4*time.Millisecond {
		t.Fatalf(
			"p95/p99 = %s/%s, want 4ms/4ms",
			result.P95Latency,
			result.P99Latency,
		)
	}
	if result.StatusCodes[http.StatusOK] != 1 ||
		result.StatusCodes[http.StatusNoContent] != 1 ||
		result.StatusCodes[http.StatusNotFound] != 1 {
		t.Fatalf("StatusCodes = %#v, want 200/204/404 counts", result.StatusCodes)
	}
	if result.TerminationReason != TerminationDurationElapsed {
		t.Fatalf("TerminationReason = %q, want %q", result.TerminationReason, TerminationDurationElapsed)
	}
}

func TestRunReturnsTypedResult(t *testing.T) {
	tester := NewTester("http://example.test", 100, 0)
	tester.client.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusCreated,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
		}, nil
	})
	result, err := tester.Run(context.Background(), 50*time.Millisecond)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Requests == 0 {
		t.Fatal("Run() completed no requests")
	}
	if result.ActualRPS <= 0 {
		t.Fatalf("ActualRPS = %.2f, want positive", result.ActualRPS)
	}
	if result.HTTPErrorRate != 0 {
		t.Fatalf("HTTPErrorRate = %.2f, want 0", result.HTTPErrorRate)
	}
	if result.StatusCodes[http.StatusCreated] != result.Requests {
		t.Fatalf("StatusCodes = %#v, requests = %d", result.StatusCodes, result.Requests)
	}
	if result.P50Latency <= 0 || result.P95Latency <= 0 || result.P99Latency <= 0 {
		t.Fatalf(
			"latencies = %s/%s/%s, want positive",
			result.P50Latency,
			result.P95Latency,
			result.P99Latency,
		)
	}
	if result.TerminationReason != TerminationDurationElapsed {
		t.Fatalf("TerminationReason = %q, want %q", result.TerminationReason, TerminationDurationElapsed)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestRunReturnsCancellationReason(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := NewTester("http://example.test", 10, 0).Run(ctx, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context cancellation", err)
	}
	if result.TerminationReason != TerminationContextCanceled {
		t.Fatalf("TerminationReason = %q, want %q", result.TerminationReason, TerminationContextCanceled)
	}
}

func TestRunRejectsNilContext(t *testing.T) {
	_, err := NewTester("http://example.test", 10, 0).Run(nil, time.Second)
	if err == nil || !strings.Contains(err.Error(), "must not be nil") {
		t.Fatalf("Run() error = %v, want nil-context error", err)
	}
}

func TestRunConvertsRequestPanicToError(t *testing.T) {
	tester := NewTester("http://example.test", 100, 0)
	tester.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		panic("transport exploded")
	})

	result, err := tester.Run(context.Background(), 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "panic in HTTP request: transport exploded") {
		t.Fatalf("Run() error = %v, want recovered panic", err)
	}
	if result.TerminationReason != TerminationContextCanceled {
		t.Fatalf("termination = %q, want context cancellation from panic recovery", result.TerminationReason)
	}
}

func TestEvaluateSLO(t *testing.T) {
	slo := SLO{
		MinimumRPS:           95,
		MaximumHTTPErrorRate: 0.01,
		MaximumP95Latency:    200 * time.Millisecond,
	}

	passing := RunResult{
		Requests:          100,
		ActualRPS:         100,
		HTTPErrorRate:     0.01,
		P95Latency:        200 * time.Millisecond,
		TerminationReason: TerminationDurationElapsed,
	}
	assessment, err := passing.EvaluateSLO(slo)
	if err != nil || !assessment.Passed {
		t.Fatalf("EvaluateSLO() = %#v, %v, want pass", assessment, err)
	}

	failing := passing
	failing.ActualRPS = 90
	failing.HTTPErrorRate = 0.02
	failing.P95Latency = 250 * time.Millisecond
	failing.TerminationReason = TerminationContextCanceled
	assessment, err = failing.EvaluateSLO(slo)
	if err != nil {
		t.Fatalf("EvaluateSLO() error = %v", err)
	}
	if assessment.Passed || len(assessment.Violations) != 4 {
		t.Fatalf("EvaluateSLO() = %#v, want four violations", assessment)
	}
	for _, expected := range []string{
		"termination reason",
		"actual RPS",
		"HTTP error rate",
		"p95 latency",
	} {
		if !strings.Contains(strings.Join(assessment.Violations, " "), expected) {
			t.Fatalf("violations %q do not contain %q", assessment.Violations, expected)
		}
	}
}

func TestEvaluateSLORejectsNonFiniteThreshold(t *testing.T) {
	_, err := (RunResult{}).EvaluateSLO(SLO{MinimumRPS: math.NaN()})
	if err == nil || !strings.Contains(err.Error(), "finite non-negative") {
		t.Fatalf("EvaluateSLO() error = %v, want finite threshold error", err)
	}
}
