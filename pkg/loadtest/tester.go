package loadtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
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

// TerminationReason describes why a load test stopped.
type TerminationReason string

const (
	TerminationDurationElapsed      TerminationReason = "duration_elapsed"
	TerminationContextCanceled      TerminationReason = "context_canceled"
	TerminationContextDeadline      TerminationReason = "context_deadline_exceeded"
	TerminationInvalidConfiguration TerminationReason = "invalid_configuration"
	TerminationInvalidTarget        TerminationReason = "invalid_target"
	TerminationInternalError        TerminationReason = "internal_error"
)

// RequestResult represents the result of one request attempt.
type RequestResult struct {
	Latency    time.Duration
	StatusCode int
	Error      error
}

// RunResult is the typed outcome of a complete load-test run.
// HTTPErrorRate is a ratio in the range [0, 1]. It includes transport errors
// and HTTP status codes outside the 2xx and 3xx ranges.
type RunResult struct {
	Requests          int
	HTTPErrors        int
	ActualRPS         float64
	HTTPErrorRate     float64
	P50Latency        time.Duration
	P95Latency        time.Duration
	P99Latency        time.Duration
	StatusCodes       map[int]int
	Duration          time.Duration
	TerminationReason TerminationReason
}

// SLO defines the service-level objectives that a load test must satisfy
// before its Kubernetes metrics may be used to generate a recommendation.
// MaximumHTTPErrorRate is a ratio in the range [0, 1]. A zero latency limit
// disables the latency objective, and a zero minimum disables the RPS objective.
type SLO struct {
	MinimumRPS           float64
	MaximumHTTPErrorRate float64
	MaximumP95Latency    time.Duration
}

// SLOAssessment is the typed outcome of evaluating a run against an SLO.
type SLOAssessment struct {
	Passed     bool
	Violations []string
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

func (r *panicReporter) report(component string, value any) {
	r.once.Do(func() {
		r.errCh <- fmt.Errorf("panic in %s: %v", component, value)
		r.cancel()
	})
}

func (r *panicReporter) recover(component string) {
	if value := recover(); value != nil {
		r.report(component, value)
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

// Run executes a load test for the specified duration and returns its typed
// result. Individual HTTP failures are captured in RunResult and do not become
// a Go error; setup failures and caller cancellation do.
func (t *Tester) Run(ctx context.Context, duration time.Duration) (RunResult, error) {
	result := RunResult{
		StatusCodes:       make(map[int]int),
		TerminationReason: TerminationInvalidConfiguration,
	}

	if ctx == nil {
		return result, errors.New("load-test context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		result.TerminationReason = terminationReason(ctx)
		return result, err
	}
	if duration <= 0 {
		return result, errors.New("load-test duration must be greater than zero")
	}
	if t.client == nil {
		return result, errors.New("load-test HTTP client must not be nil")
	}
	if t.concurrency <= 0 && t.rps <= 0 {
		return result, errors.New("load-test RPS must be greater than zero when concurrency is disabled")
	}
	if t.concurrency < 0 {
		return result, errors.New("load-test concurrency must not be negative")
	}

	targetURL, err := t.validateTarget()
	if err != nil {
		result.TerminationReason = TerminationInvalidTarget
		return result, err
	}

	runCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()

	reporter := newPanicReporter(cancel)
	requestResults := make(chan RequestResult, resultsBufferSize)
	termination := make(chan TerminationReason, 1)
	startedAt := time.Now()

	go func() {
		reason := TerminationInternalError
		defer func() {
			if value := recover(); value != nil {
				reporter.report("load-test producer", value)
				reason = TerminationInternalError
			}
			close(requestResults)
			termination <- reason
		}()

		if t.concurrency > 0 {
			reason = t.runConcurrentTest(runCtx, ctx, targetURL, requestResults, reporter)
		} else {
			reason = t.runRPSTest(runCtx, ctx, targetURL, requestResults, reporter)
		}
	}()

	var metrics Metrics
	metrics.StartTime = startedAt
	for requestResult := range requestResults {
		metrics.Add(&requestResult)
		if metrics.Requests > 0 && metrics.Requests%100 == 0 {
			fmt.Printf(
				"Progress: %d requests, %.2f%% success\n",
				metrics.Requests,
				metrics.SuccessRate(),
			)
		}
	}

	metrics.EndTime = time.Now()
	metrics.TestDuration = metrics.EndTime.Sub(metrics.StartTime)
	result = metrics.RunResult(<-termination)
	result.PrintSummary()

	if err := reporter.err(); err != nil {
		result.TerminationReason = TerminationInternalError
		return result, err
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	return result, nil
}

// runRPSTest schedules requests at the configured rate until runCtx is done.
// All in-flight requests use that same context, so cancellation is complete
// before this function returns.
func (t *Tester) runRPSTest(
	runCtx context.Context,
	parentCtx context.Context,
	targetURL *url.URL,
	results chan<- RequestResult,
	reporter *panicReporter,
) TerminationReason {
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
		case <-runCtx.Done():
			requests.Wait()
			return terminationReason(parentCtx)
		case <-ticker.C:
			requests.Add(1)
			go func() {
				defer requests.Done()
				defer reporter.recover("HTTP request")
				sendResult(runCtx, results, t.doRequest(runCtx, targetURL))
			}()
		}
	}
}

// runConcurrentTest keeps the configured number of workers active until the
// requested duration elapses.
func (t *Tester) runConcurrentTest(
	runCtx context.Context,
	parentCtx context.Context,
	targetURL *url.URL,
	results chan<- RequestResult,
	reporter *panicReporter,
) TerminationReason {
	fmt.Printf(
		"Starting concurrent load test with %d workers...\n",
		t.concurrency,
	)

	var workers sync.WaitGroup
	for i := 0; i < t.concurrency; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer reporter.recover("HTTP worker")
			for {
				select {
				case <-runCtx.Done():
					return
				default:
				}

				requestResult := t.doRequest(runCtx, targetURL)
				if !sendResult(runCtx, results, requestResult) {
					return
				}
				if requestResult.Error != nil {
					if !waitFor(runCtx, 100*time.Millisecond) {
						return
					}
				}
			}
		}()
	}

	workers.Wait()
	return terminationReason(parentCtx)
}

func (t *Tester) doRequest(ctx context.Context, targetURL *url.URL) RequestResult {
	startedAt := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL.String(), nil)
	if err != nil {
		return RequestResult{Latency: time.Since(startedAt), Error: err}
	}
	req.Header.Set("User-Agent", "Pod-Rightsizer/1.0")

	resp, err := t.client.Do(req)
	latency := time.Since(startedAt)
	if err != nil {
		logRequestError(err)
		return RequestResult{Latency: latency, Error: err}
	}
	defer resp.Body.Close()

	_, readErr := io.Copy(io.Discard, resp.Body)
	if readErr != nil {
		return RequestResult{
			Latency:    latency,
			StatusCode: resp.StatusCode,
			Error:      fmt.Errorf("read response body: %w", readErr),
		}
	}

	return RequestResult{Latency: latency, StatusCode: resp.StatusCode}
}

func sendResult(ctx context.Context, results chan<- RequestResult, result RequestResult) bool {
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

func terminationReason(ctx context.Context) TerminationReason {
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return TerminationContextCanceled
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return TerminationContextDeadline
	default:
		return TerminationDurationElapsed
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
		return nil, fmt.Errorf("target URL must use http or https")
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

// Metrics accumulates request-level load-test metrics.
type Metrics struct {
	Requests     int
	Success      int
	Failures     int
	StatusCodes  map[int]int
	TotalLatency time.Duration
	StartTime    time.Time
	EndTime      time.Time
	TestDuration time.Duration
	MinLatency   time.Duration
	MaxLatency   time.Duration
	Latencies    []time.Duration
}

// Add adds one request result to the metrics.
func (m *Metrics) Add(result *RequestResult) {
	if m.StatusCodes == nil {
		m.StatusCodes = make(map[int]int)
	}

	m.Requests++
	if result.StatusCode > 0 {
		m.StatusCodes[result.StatusCode]++
	}
	if result.Latency > 0 {
		m.TotalLatency += result.Latency
		m.Latencies = append(m.Latencies, result.Latency)
		if m.MinLatency == 0 || result.Latency < m.MinLatency {
			m.MinLatency = result.Latency
		}
		if result.Latency > m.MaxLatency {
			m.MaxLatency = result.Latency
		}
	}

	if result.Error != nil || result.StatusCode < 200 || result.StatusCode >= 400 {
		m.Failures++
		return
	}
	m.Success++
}

// MeanLatency calculates the mean latency of measured request attempts.
func (m *Metrics) MeanLatency() time.Duration {
	if len(m.Latencies) == 0 {
		return 0
	}
	return time.Duration(int64(m.TotalLatency) / int64(len(m.Latencies)))
}

// SuccessRate calculates the percentage of successful requests.
func (m *Metrics) SuccessRate() float64 {
	if m.Requests == 0 {
		return 0
	}
	return float64(m.Success) / float64(m.Requests) * 100
}

// HTTPErrorRate calculates the HTTP/transport error ratio in the range [0, 1].
func (m *Metrics) HTTPErrorRate() float64 {
	if m.Requests == 0 {
		return 0
	}
	return float64(m.Failures) / float64(m.Requests)
}

// P50Latency calculates the nearest-rank 50th percentile latency.
func (m *Metrics) P50Latency() time.Duration {
	return m.PercentileLatency(0.50)
}

// P95Latency calculates the nearest-rank 95th percentile latency.
func (m *Metrics) P95Latency() time.Duration {
	return m.PercentileLatency(0.95)
}

// P99Latency calculates the nearest-rank 99th percentile latency.
func (m *Metrics) P99Latency() time.Duration {
	return m.PercentileLatency(0.99)
}

// PercentileLatency calculates a nearest-rank latency percentile.
func (m *Metrics) PercentileLatency(percentile float64) time.Duration {
	if len(m.Latencies) == 0 {
		return 0
	}
	if percentile <= 0 {
		percentile = 0
	}
	if percentile > 1 {
		percentile = 1
	}

	sortedLatencies := append([]time.Duration(nil), m.Latencies...)
	sort.Slice(sortedLatencies, func(i, j int) bool {
		return sortedLatencies[i] < sortedLatencies[j]
	})
	index := int(math.Ceil(percentile*float64(len(sortedLatencies)))) - 1
	if index < 0 {
		index = 0
	}
	return sortedLatencies[index]
}

// Throughput calculates completed requests per second over the observed run.
func (m *Metrics) Throughput() float64 {
	if m.Requests == 0 || m.TestDuration <= 0 {
		return 0
	}
	return float64(m.Requests) / m.TestDuration.Seconds()
}

// RunResult builds the immutable typed result for the accumulated metrics.
func (m *Metrics) RunResult(reason TerminationReason) RunResult {
	statusCodes := make(map[int]int, len(m.StatusCodes))
	for code, count := range m.StatusCodes {
		statusCodes[code] = count
	}
	return RunResult{
		Requests:          m.Requests,
		HTTPErrors:        m.Failures,
		ActualRPS:         m.Throughput(),
		HTTPErrorRate:     m.HTTPErrorRate(),
		P50Latency:        m.P50Latency(),
		P95Latency:        m.P95Latency(),
		P99Latency:        m.P99Latency(),
		StatusCodes:       statusCodes,
		Duration:          m.TestDuration,
		TerminationReason: reason,
	}
}

// EvaluateSLO checks whether the run is safe to use for a recommendation.
func (r RunResult) EvaluateSLO(slo SLO) (SLOAssessment, error) {
	if math.IsNaN(slo.MinimumRPS) || math.IsInf(slo.MinimumRPS, 0) || slo.MinimumRPS < 0 {
		return SLOAssessment{}, errors.New("minimum RPS SLO must be a finite non-negative number")
	}
	if math.IsNaN(slo.MaximumHTTPErrorRate) ||
		math.IsInf(slo.MaximumHTTPErrorRate, 0) ||
		slo.MaximumHTTPErrorRate < 0 ||
		slo.MaximumHTTPErrorRate > 1 {
		return SLOAssessment{}, errors.New("maximum HTTP error rate SLO must be between 0 and 1")
	}
	if slo.MaximumP95Latency < 0 {
		return SLOAssessment{}, errors.New("maximum p95 latency SLO must not be negative")
	}

	assessment := SLOAssessment{Passed: true}
	if r.TerminationReason != TerminationDurationElapsed {
		assessment.Violations = append(
			assessment.Violations,
			fmt.Sprintf("termination reason is %s", r.TerminationReason),
		)
	}
	if r.Requests == 0 {
		assessment.Violations = append(assessment.Violations, "no requests completed")
	}
	if slo.MinimumRPS > 0 && r.ActualRPS < slo.MinimumRPS {
		assessment.Violations = append(
			assessment.Violations,
			fmt.Sprintf("actual RPS %.2f is below minimum %.2f", r.ActualRPS, slo.MinimumRPS),
		)
	}
	if r.HTTPErrorRate > slo.MaximumHTTPErrorRate {
		assessment.Violations = append(
			assessment.Violations,
			fmt.Sprintf(
				"HTTP error rate %.2f%% exceeds maximum %.2f%%",
				r.HTTPErrorRate*100,
				slo.MaximumHTTPErrorRate*100,
			),
		)
	}
	if slo.MaximumP95Latency > 0 && r.P95Latency > slo.MaximumP95Latency {
		assessment.Violations = append(
			assessment.Violations,
			fmt.Sprintf(
				"p95 latency %s exceeds maximum %s",
				r.P95Latency,
				slo.MaximumP95Latency,
			),
		)
	}
	assessment.Passed = len(assessment.Violations) == 0
	return assessment, nil
}

// PrintSummary prints the typed load-test result to stdout.
func (r RunResult) PrintSummary() {
	fmt.Fprintln(os.Stdout, "\nLoad Test Results")
	fmt.Fprintln(os.Stdout, "-----------------")
	fmt.Fprintf(os.Stdout, "Termination Reason: %s\n", r.TerminationReason)
	fmt.Fprintf(os.Stdout, "Test Duration: %s\n", r.Duration.Round(time.Millisecond))
	fmt.Fprintf(os.Stdout, "Total Requests: %d\n", r.Requests)
	fmt.Fprintf(os.Stdout, "Actual RPS: %.2f req/s\n", r.ActualRPS)
	fmt.Fprintf(os.Stdout, "HTTP Error Rate: %.2f%%\n", r.HTTPErrorRate*100)
	fmt.Fprintf(os.Stdout, "Latency p50: %s\n", r.P50Latency)
	fmt.Fprintf(os.Stdout, "Latency p95: %s\n", r.P95Latency)
	fmt.Fprintf(os.Stdout, "Latency p99: %s\n", r.P99Latency)

	fmt.Fprintln(os.Stdout, "\nStatus Code Distribution:")
	codes := make([]int, 0, len(r.StatusCodes))
	for code := range r.StatusCodes {
		codes = append(codes, code)
	}
	sort.Ints(codes)
	if len(codes) == 0 {
		fmt.Fprintln(os.Stdout, "No HTTP responses recorded")
		return
	}
	for _, code := range codes {
		fmt.Fprintf(os.Stdout, "[%d]: %d responses\n", code, r.StatusCodes[code])
	}
}
