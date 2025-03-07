package loadtest

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Tester is responsible for running load tests
type Tester struct {
	target      string
	rps         int
	concurrency int
	client      *http.Client
}

// NewTester creates a new load tester
func NewTester(target string, rps, concurrency int) *Tester {
	return &Tester{
		target:      target,
		rps:         rps,
		concurrency: concurrency,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Run executes a load test for the specified duration
func (t *Tester) Run(ctx context.Context, duration time.Duration) error {
	// Check if we should use RPS or Concurrency mode
	if t.concurrency > 0 {
		return t.runConcurrentTest(ctx, duration)
	}
	return t.runSimpleRPSTest(ctx, duration)
}

// runSimpleRPSTest runs a load test at a specified RPS
func (t *Tester) runSimpleRPSTest(ctx context.Context, duration time.Duration) error {
	// Calculate total requests to send
	totalRequests := int(duration.Seconds()) * t.rps

	// Create channel to receive results
	results := make(chan result, totalRequests)

	// Create a ticker for rate limiting
	interval := time.Second / time.Duration(t.rps)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Set up context with timeout
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()

	// Launch workers
	sent := 0

	fmt.Printf("Starting load test with %d RPS for %s...\n", t.rps, duration)
	startTime := time.Now()

	// Send requests at the specified rate
	go func() {
		defer close(results)
		for sent < totalRequests {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				go func(reqNum int) {
					start := time.Now()

					req, err := http.NewRequestWithContext(ctx, "GET", t.target, nil)
					if err != nil {
						results <- result{err: err}
						return
					}

					resp, err := t.client.Do(req)
					latency := time.Since(start)

					if err != nil {
						results <- result{err: err, latency: latency}
						return
					}

					resp.Body.Close()
					code := resp.StatusCode
					success := code >= 200 && code < 400

					results <- result{
						statusCode: code,
						latency:    latency,
						success:    success,
					}
				}(sent)

				sent++
			}
		}
	}()

	// Collect results
	var completed int
	var successful int
	var errors int
	var totalLatency time.Duration

	for r := range results {
		completed++

		if r.err != nil {
			errors++
		} else if r.success {
			successful++
		} else {
			errors++
		}

		totalLatency += r.latency

		// Print progress periodically
		if completed%100 == 0 {
			fmt.Printf("Progress: %d/%d requests completed\n", completed, totalRequests)
		}
	}

	// Calculate stats
	totalTime := time.Since(startTime)
	avgLatency := float64(totalLatency.Milliseconds()) / float64(completed)
	actualRPS := float64(completed) / totalTime.Seconds()
	successRate := float64(successful) / float64(completed) * 100

	fmt.Printf("Load test results: %d requests, %.2f%% success, %.2fms mean latency, %.2f actual RPS\n",
		completed, successRate, avgLatency, actualRPS)

	return nil
}

// result represents the result of a single request
type result struct {
	statusCode int
	latency    time.Duration
	err        error
	success    bool
}

// runConcurrentTest runs a custom concurrent load test
func (t *Tester) runConcurrentTest(ctx context.Context, duration time.Duration) error {
	// Create a cancellable context with the given duration
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()

	// Create a channel for worker results
	resultChan := make(chan error, t.concurrency)

	// Start the workers
	for i := 0; i < t.concurrency; i++ {
		go func(workerID int) {
			resultChan <- t.worker(ctx, workerID)
		}(i)
	}

	// Collect results
	var errors int
	var success int

	for i := 0; i < t.concurrency; i++ {
		err := <-resultChan
		if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
			errors++
		} else {
			success++
		}
	}

	fmt.Printf("Concurrent load test complete: %d workers, %d successful, %d errors\n",
		t.concurrency, success, errors)

	return nil
}

// worker sends requests continuously until the context is cancelled
func (t *Tester) worker(ctx context.Context, id int) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			req, err := http.NewRequestWithContext(ctx, "GET", t.target, nil)
			if err != nil {
				continue
			}

			resp, err := t.client.Do(req)
			if err != nil {
				continue
			}
			resp.Body.Close()

			// Add a small delay to prevent CPU spinning
			time.Sleep(10 * time.Millisecond)
		}
	}
}
