package loadtest

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestRunCancellationStopsRequestsBeforeReturning(t *testing.T) {
	var active atomic.Int32
	started := make(chan struct{})
	var startedOnce sync.Once

	tester := NewTester("http://example.test", 0, 2)
	tester.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		active.Add(1)
		defer active.Add(-1)
		startedOnce.Do(func() { close(started) })
		<-request.Context().Done()
		return nil, request.Context().Err()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancelDone := make(chan struct{})
	go func() {
		defer close(cancelDone)
		select {
		case <-started:
			cancel()
		case <-time.After(time.Second):
			cancel()
		}
	}()

	err := tester.Run(ctx, time.Minute)
	<-cancelDone
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if got := active.Load(); got != 0 {
		t.Fatalf("Run() returned with %d active HTTP requests", got)
	}
}

func TestRunDurationTimeoutCancelsHTTPContext(t *testing.T) {
	var active atomic.Int32
	var sawDeadline atomic.Bool

	tester := NewTester("http://example.test", 0, 1)
	tester.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		active.Add(1)
		defer active.Add(-1)
		if _, ok := request.Context().Deadline(); ok {
			sawDeadline.Store(true)
		}
		<-request.Context().Done()
		return nil, request.Context().Err()
	})

	startedAt := time.Now()
	if err := tester.Run(context.Background(), 20*time.Millisecond); err != nil {
		t.Fatalf("Run() error = %v, want duration completion", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("Run() took %s, want bounded completion", elapsed)
	}
	if !sawDeadline.Load() {
		t.Fatal("HTTP request context had no deadline")
	}
	if got := active.Load(); got != 0 {
		t.Fatalf("Run() returned with %d active HTTP requests", got)
	}
}

func TestRunRecoversWorkerPanic(t *testing.T) {
	tester := NewTester("http://example.test", 0, 1)
	tester.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		panic("transport exploded")
	})

	err := tester.Run(context.Background(), time.Second)
	if err == nil || !strings.Contains(err.Error(), "panic in HTTP worker: transport exploded") {
		t.Fatalf("Run() error = %v, want recovered worker panic", err)
	}
}

func TestRunRecoversRPSRequestPanic(t *testing.T) {
	tester := NewTester("http://example.test", 1_000, 0)
	tester.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		panic("RPS transport exploded")
	})

	err := tester.Run(context.Background(), time.Second)
	if err == nil || !strings.Contains(err.Error(), "panic in HTTP request: RPS transport exploded") {
		t.Fatalf("Run() error = %v, want recovered request panic", err)
	}
}

func TestRunRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		tester *Tester
		dur    time.Duration
	}{
		{name: "duration", tester: NewTester("example.test", 1, 0), dur: 0},
		{name: "rps", tester: NewTester("example.test", 0, 0), dur: time.Second},
		{name: "concurrency", tester: NewTester("example.test", 1, -1), dur: time.Second},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.tester.Run(context.Background(), test.dur); err == nil {
				t.Fatal("Run() error = nil, want configuration error")
			}
		})
	}
}
