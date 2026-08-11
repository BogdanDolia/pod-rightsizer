package loadtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	resultsBufferSize  = 10_000
	httpRequestTimeout = 30 * time.Second
)

// Tester is responsible for running load tests.
type Tester struct {
	target      string
	rps         int
	concurrency int
	client      *http.Client
}

// Result represents the result of a single request.
type Result struct {
	Latency    time.Duration
	StatusCode int
	Error      error
}

// NewTester creates a new load tester.
func NewTester(target string, rps, concurrency int) *Tester {
	return &Tester{
		target:      target,
		rps:         rps,
		concurrency: concurrency,
		client: &http.Client{
			Timeout: httpRequestTimeout,
		},
	}
}

type panicReporter struct {
	once   sync.Once
	errCh  chan error
	cancel context.CancelFunc
}

func newPanicReporter(cancel context.CancelFunc) *panicReporter {
	return &panicReporter{
		errCh:  make(chan error, 1),
		cancel: cancel,
	}
}

func (r *panicReporter) recover(component string) {
	if value := recover(); value != nil {
		r.once.Do(func() {
			r.errCh <- fmt.Errorf("panic in %s: %v", component, value)
			r.cancel()
		})
	}
}

func (r *panicReporter) err() error {
	select {
	case err := <-r.errCh:
		return err
	default:
		return nil
	}
}

// Run executes a load test for the specified duration. It does not return
// until every producer and request goroutine has stopped.
func (t *Tester) Run(ctx context.Context, duration time.Duration) error {
	if ctx == nil {
		return errors.New("load-test context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if duration <= 0 {
		return errors.New("load-test duration must be greater than zero")
	}
	if t.client == nil {
		return errors.New("load-test HTTP client must not be nil")
	}
	if t.concurrency < 0 {
		return errors.New("load-test concurrency must not be negative")
	}
	if t.concurrency == 0 && t.rps <= 0 {
		return errors.New("load-test RPS must be greater than zero when concurrency is disabled")
	}

	targetURL, err := t.validateTarget()
	if err != nil {
		return err
	}

	runCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()

	reporter := newPanicReporter(cancel)
	results := make(chan *Result, resultsBufferSize)
	producerDone := make(chan struct{})
	startedAt := time.Now()

	go func() {
		defer close(producerDone)
		defer close(results)
		defer reporter.recover("load-test producer")

		if t.concurrency > 0 {
			t.runConcurrentTest(runCtx, targetURL, results, reporter)
			return
		}
		t.runRPSTest(runCtx, targetURL, results, reporter)
	}()

	metrics := Metrics{StartTime: startedAt}
	for result := range results {
		metrics.Add(result)
		if metrics.Requests > 0 && metrics.Requests%100 == 0 {
			fmt.Printf(
				"Progress: %d requests, %.2f%% success\n",
				metrics.Requests,
				metrics.SuccessRate(),
			)
		}
	}
	<-producerDone

	metrics.EndTime = time.Now()
	metrics.TestDuration = metrics.EndTime.Sub(metrics.StartTime)
	fmt.Printf("Test took %s (expected %s)\n", metrics.TestDuration.Round(time.Millisecond), duration)
	metrics.PrintSummary()

	if err := reporter.err(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// runRPSTest schedules requests at the configured rate until ctx is done.
func (t *Tester) runRPSTest(
	ctx context.Context,
	targetURL *url.URL,
	results chan<- *Result,
	reporter *panicReporter,
) {
	fmt.Printf("Starting load test with %d RPS...\n", t.rps)

	interval := time.Second / time.Duration(t.rps)
	if interval <= 0 {
		interval = time.Nanosecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var requests sync.WaitGroup
	for {
		select {
		case <-ctx.Done():
			requests.Wait()
			return
		case <-ticker.C:
			requests.Add(1)
			go func() {
				defer requests.Done()
				defer reporter.recover("HTTP request")
				sendResult(ctx, results, t.doRequest(ctx, targetURL))
			}()
		}
	}
}

// runConcurrentTest keeps a fixed number of workers active until ctx is done.
func (t *Tester) runConcurrentTest(
	ctx context.Context,
	targetURL *url.URL,
	results chan<- *Result,
	reporter *panicReporter,
) {
	fmt.Printf("Starting concurrent load test with %d workers...\n", t.concurrency)

	var workers sync.WaitGroup
	for i := 0; i < t.concurrency; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer reporter.recover("HTTP worker")

			for {
				if ctx.Err() != nil {
					return
				}

				result := t.doRequest(ctx, targetURL)
				if !sendResult(ctx, results, result) {
					return
				}

				delay := 10 * time.Millisecond
				if result.Error != nil {
					delay = 100 * time.Millisecond
				}
				if !waitFor(ctx, delay) {
					return
				}
			}
		}()
	}

	workers.Wait()
}

func (t *Tester) doRequest(ctx context.Context, targetURL *url.URL) *Result {
	startedAt := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL.String(), nil)
	if err != nil {
		return &Result{Latency: time.Since(startedAt), Error: err}
	}
	req.Header.Set("User-Agent", "Pod-Rightsizer/1.0")

	resp, err := t.client.Do(req)
	latency := time.Since(startedAt)
	if err != nil {
		logRequestError(err)
		return &Result{Latency: latency, Error: err}
	}
	defer resp.Body.Close()

	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return &Result{
			Latency:    latency,
			StatusCode: resp.StatusCode,
			Error:      fmt.Errorf("read response body: %w", err),
		}
	}

	return &Result{Latency: latency, StatusCode: resp.StatusCode}
}

func sendResult(ctx context.Context, results chan<- *Result, result *Result) bool {
	select {
	case results <- result:
		return true
	case <-ctx.Done():
		return false
	}
}

func waitFor(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func logRequestError(err error) {
	var netErr net.Error
	switch {
	case errors.As(err, &netErr) && netErr.Timeout():
		fmt.Printf("Network timeout error: %v\n", err)
	case strings.Contains(err.Error(), "connection refused"):
		fmt.Printf("Connection refused: %v (is the service running?)\n", err)
	default:
		fmt.Printf("HTTP request error: %v\n", err)
	}
}

// validateTarget ensures the target is a usable HTTP URL and normalizes it.
func (t *Tester) validateTarget() (*url.URL, error) {
	target := t.target
	if !isURL(target) {
		target = "http://" + target
		fmt.Printf("Added http:// prefix, target is now: %s\n", target)
	}

	parsedURL, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("parse target URL: %w", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return nil, errors.New("target URL must use http or https")
	}
	if parsedURL.Host == "" {
		return nil, errors.New("target URL must include a host")
	}

	fmt.Printf("Validated target URL: %s\n", parsedURL.String())
	return parsedURL, nil
}

func isURL(value string) bool {
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")
}

// Metrics holds load test metrics
type Metrics struct {
	Requests     int
	Success      int
	Failures     int
	StatusCodes  map[int]int
	TotalLatency time.Duration
	StartTime    time.Time     // When the test started
	EndTime      time.Time     // When the test ended
	TestDuration time.Duration // Actual duration of the test
	MinLatency   time.Duration
	MaxLatency   time.Duration
	Latencies    []time.Duration
}

// Add adds a result to the metrics
func (m *Metrics) Add(r *Result) {
	if m.StatusCodes == nil {
		m.StatusCodes = make(map[int]int)
		m.MinLatency = 24 * time.Hour // Initialize to a large value
	}

	m.Requests++

	if r.Error != nil {
		m.Failures++
		fmt.Printf("Request error: %v\n", r.Error)
		return
	}

	// Count status codes
	m.StatusCodes[r.StatusCode]++

	// Track latency stats
	m.TotalLatency += r.Latency
	m.Latencies = append(m.Latencies, r.Latency)

	// Update min/max latency
	if r.Latency < m.MinLatency {
		m.MinLatency = r.Latency
	}
	if r.Latency > m.MaxLatency {
		m.MaxLatency = r.Latency
	}

	// Count successes (2xx and 3xx status codes)
	if r.StatusCode >= 200 && r.StatusCode < 400 {
		m.Success++
		// Debug logging to see success codes
		if m.Success%100 == 0 {
			fmt.Printf("Success count: %d for status code %d\n", m.Success, r.StatusCode)
		}
	} else {
		m.Failures++
		fmt.Printf("Non-success status code: %d\n", r.StatusCode)
	}
}

// MeanLatency calculates the mean latency
func (m *Metrics) MeanLatency() time.Duration {
	if m.Requests == 0 || m.TotalLatency == 0 {
		return 0
	}
	return time.Duration(int64(m.TotalLatency) / int64(m.Requests))
}

// SuccessRate calculates the percentage of successful requests
func (m *Metrics) SuccessRate() float64 {
	if m.Requests == 0 {
		return 0
	}
	return float64(m.Success) / float64(m.Requests) * 100.0
}

// P95Latency calculates the 95th percentile latency
func (m *Metrics) P95Latency() time.Duration {
	if len(m.Latencies) == 0 {
		return 0
	}

	// Sort latencies
	sortedLatencies := make([]time.Duration, len(m.Latencies))
	copy(sortedLatencies, m.Latencies)

	// Use sort.Slice to sort the durations
	sort.Slice(sortedLatencies, func(i, j int) bool {
		return sortedLatencies[i] < sortedLatencies[j]
	})

	// Get index for 95th percentile
	idx := int(float64(len(sortedLatencies)) * 0.95)
	if idx >= len(sortedLatencies) {
		idx = len(sortedLatencies) - 1
	}

	return sortedLatencies[idx]
}

// Throughput calculates requests per second
func (m *Metrics) Throughput() float64 {
	if m.Requests == 0 {
		return 0
	}

	// If we have test duration recorded, use it (more accurate)
	if m.TestDuration > 0 {
		return float64(m.Requests) / m.TestDuration.Seconds()
	}

	// If we have start and end time, calculate duration from that
	if !m.StartTime.IsZero() && !m.EndTime.IsZero() {
		duration := m.EndTime.Sub(m.StartTime)
		return float64(m.Requests) / duration.Seconds()
	}

	// Fallback - we can't calculate throughput without duration
	fmt.Fprintf(os.Stderr, "Warning: Cannot calculate throughput without test duration.\n")
	return 0
}

// PrintSummary prints a summary of the metrics to stdout
func (m *Metrics) PrintSummary() {
	fmt.Fprintf(os.Stdout, "\nLoad Test Results\n")
	fmt.Fprintf(os.Stdout, "----------------\n")
	fmt.Fprintf(os.Stdout, "Total Requests: %d\n", m.Requests)
	fmt.Fprintf(os.Stdout, "Successful Requests: %d\n", m.Success)
	fmt.Fprintf(os.Stdout, "Failed Requests: %d\n", m.Failures)
	fmt.Fprintf(os.Stdout, "Success Rate: %.2f%%\n", m.SuccessRate())

	// Add test duration information
	if !m.StartTime.IsZero() && !m.EndTime.IsZero() {
		fmt.Fprintf(os.Stdout, "Test Duration: %s\n", m.EndTime.Sub(m.StartTime).Round(time.Millisecond))
	} else if m.TestDuration > 0 {
		fmt.Fprintf(os.Stdout, "Test Duration: %s\n", m.TestDuration.Round(time.Millisecond))
	}

	if m.Requests > 0 {
		fmt.Fprintf(os.Stdout, "Mean Latency: %.2fms\n", float64(m.MeanLatency().Microseconds())/1000.0)

		if m.MinLatency < 24*time.Hour {
			fmt.Fprintf(os.Stdout, "Min Latency: %.2fms\n", float64(m.MinLatency.Microseconds())/1000.0)
		}
		fmt.Fprintf(os.Stdout, "Max Latency: %.2fms\n", float64(m.MaxLatency.Microseconds())/1000.0)

		// Show both total requests and RPS
		throughput := m.Throughput()
		fmt.Fprintf(os.Stdout, "Throughput: %.2f req/s (based on test duration)\n", throughput)

		// Show expected RPS for comparison if different
		if m.TestDuration > 0 && int(throughput) != int(float64(m.Requests)/m.TestDuration.Seconds()) {
			fmt.Fprintf(os.Stdout, "Expected RPS: %.2f req/s\n", float64(m.Requests)/m.TestDuration.Seconds())
		}
	}

	fmt.Fprintf(os.Stdout, "\nStatus Code Distribution:\n")
	if len(m.StatusCodes) == 0 {
		fmt.Fprintf(os.Stdout, "No status codes recorded (all requests may have failed with errors)\n")
	} else {
		for code, count := range m.StatusCodes {
			fmt.Fprintf(os.Stdout, "[%d]: %d responses\n", code, count)
		}
	}

	if m.Failures > 0 {
		fmt.Fprintf(os.Stdout, "\nWarning: %d failed requests (%.2f%%)\n",
			m.Failures, float64(m.Failures)/float64(m.Requests)*100.0)
	}
}
