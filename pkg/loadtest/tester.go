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

// Tester is responsible for running load tests
type Tester struct {
	target      string
	rps         int
	concurrency int
	client      *http.Client
	results     chan *Result
}

// Result represents the result of a single request
type Result struct {
	Latency    time.Duration
	StatusCode int
	Error      error
}

// NewTester creates a new load tester
func NewTester(target string, rps, concurrency int) *Tester {
	return &Tester{
		target:      target,
		rps:         rps,
		concurrency: concurrency,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		results: make(chan *Result, 10000), // Buffer for results
	}
}

// Run executes a load test for the specified duration
func (t *Tester) Run(ctx context.Context, duration time.Duration) error {
	// Check if we should use RPS or Concurrency mode
	if t.concurrency > 0 {
		return t.runConcurrentTest(ctx, duration)
	}
	return t.runRPSTest(ctx, duration)
}

// runRPSTest runs a load test at a specified RPS
func (t *Tester) runRPSTest(ctx context.Context, duration time.Duration) error {
	// Make sure the target URL is valid
	targetURL, err := t.validateTarget()
	if err != nil {
		return err
	}

	fmt.Printf("Starting load test with %d RPS for %s...\n", t.rps, duration)

	// Create contexts for the test
	testCtx, testCancel := context.WithTimeout(ctx, duration)
	defer testCancel()

	// Start collecting results
	resultsChan := make(chan *Result, t.rps*int(duration.Seconds()))
	resultsDone := make(chan struct{})

	// Use a waitgroup to track all goroutines
	var wg sync.WaitGroup
	wg.Add(1)

	// Collect and process results
	go func() {
		defer wg.Done()
		defer close(resultsDone)

		var metrics Metrics

		for {
			select {
			case <-testCtx.Done():
				return
			case result, ok := <-resultsChan:
				if !ok {
					// Print final metrics
					metrics.PrintSummary()
					return
				}

				metrics.Add(result)

				// Log progress periodically
				if metrics.Requests%100 == 0 {
					fmt.Printf("Progress: %d requests, %.2f%% success\n",
						metrics.Requests, metrics.SuccessRate())
				}
			}
		}
	}()

	// Start the load test
	interval := time.Second / time.Duration(t.rps)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Use a mutex to protect access to a "closed" flag
	var resultChanMutex sync.Mutex
	resultChanClosed := false

	// Safe send function to avoid sending on closed channel
	safeSend := func(result *Result) {
		resultChanMutex.Lock()
		defer resultChanMutex.Unlock()

		// Only send if channel is not closed
		if !resultChanClosed {
			select {
			case <-testCtx.Done():
				// Context canceled, don't send
			case resultsChan <- result:
				// Successfully sent
			default:
				// Channel buffer full, log and continue
				fmt.Println("Warning: result channel buffer full")
			}
		}
	}

	// Safely close the channel
	safeClose := func() {
		resultChanMutex.Lock()
		defer resultChanMutex.Unlock()

		if !resultChanClosed {
			resultChanClosed = true
			close(resultsChan)
		}
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer safeClose() // Use safe close instead of direct close

		var requestWg sync.WaitGroup
		sent := 0

		for {
			select {
			case <-testCtx.Done():
				// Wait for all request goroutines to complete before exiting
				requestWg.Wait()
				return
			case <-ticker.C:
				requestWg.Add(1)
				go func() {
					defer requestWg.Done()

					start := time.Now()
					// Create request with special user agent
					req, err := http.NewRequestWithContext(testCtx, "GET", targetURL.String(), nil)
					if err != nil {
						fmt.Printf("Error creating request to %s: %v\n", targetURL.String(), err)
						safeSend(&Result{Error: err})
						return
					}

					// Add custom headers to help identify our requests
					req.Header.Add("User-Agent", "Pod-Rightsizer/1.0")

					// Make the request
					resp, err := t.client.Do(req)
					latency := time.Since(start)

					if err != nil {
						// Extract more details about the error
						var netErr net.Error
						if errors.As(err, &netErr) && netErr.Timeout() {
							fmt.Printf("Network timeout error: %v\n", err)
						} else if strings.Contains(err.Error(), "connection refused") {
							fmt.Printf("Connection refused: %v (is the service running?)\n", err)
						} else {
							fmt.Printf("HTTP request error: %v\n", err)
						}

						safeSend(&Result{Latency: latency, Error: err})
						return
					}
					defer resp.Body.Close()

					// Discard body to properly reuse connections
					io.Copy(io.Discard, resp.Body)

					safeSend(&Result{
						Latency:    latency,
						StatusCode: resp.StatusCode,
					})
				}()

				sent++
				if sent >= t.rps*int(duration.Seconds()) {
					// Wait for all request goroutines to complete before exiting
					go func() {
						requestWg.Wait()
						testCancel() // Signal completion
					}()
					return
				}
			}
		}
	}()

	// Wait for test completion
	select {
	case <-ctx.Done():
		testCancel()
		fmt.Println("Load test was canceled")
	case <-testCtx.Done():
		// Test completed normally
	}

	// Wait for result collection to finish
	<-resultsDone

	// Wait for all goroutines to complete
	wg.Wait()

	return nil
}

// runConcurrentTest runs a test with a fixed number of concurrent workers
func (t *Tester) runConcurrentTest(ctx context.Context, duration time.Duration) error {
	// Make sure the target URL is valid
	targetURL, err := t.validateTarget()
	if err != nil {
		return err
	}

	fmt.Printf("Starting concurrent load test with %d workers for %s...\n",
		t.concurrency, duration)

	// Create contexts for the test
	testCtx, testCancel := context.WithTimeout(ctx, duration)
	defer testCancel()

	// Start collecting results
	resultsChan := make(chan *Result, 10000)
	resultsDone := make(chan struct{})

	// Use a waitgroup to track all goroutines
	var wg sync.WaitGroup
	wg.Add(1)

	// Collect and process results
	go func() {
		defer wg.Done()
		defer close(resultsDone)

		var metrics Metrics

		for {
			select {
			case <-testCtx.Done():
				return
			case result, ok := <-resultsChan:
				if !ok {
					// Print final metrics
					metrics.PrintSummary()
					return
				}

				metrics.Add(result)

				// Log progress periodically
				if metrics.Requests%100 == 0 {
					fmt.Printf("Progress: %d requests, %.2f%% success\n",
						metrics.Requests, metrics.SuccessRate())
				}
			}
		}
	}()

	// Use a mutex to protect access to a "closed" flag for the results channel
	var resultChanMutex sync.Mutex
	resultChanClosed := false

	// Safe send function to avoid sending on closed channel
	safeSend := func(result *Result) {
		resultChanMutex.Lock()
		defer resultChanMutex.Unlock()

		// Only send if channel is not closed
		if !resultChanClosed {
			select {
			case <-testCtx.Done():
				// Context canceled, don't send
			case resultsChan <- result:
				// Successfully sent
			default:
				// Channel buffer full, log and continue
				fmt.Println("Warning: result channel buffer full")
			}
		}
	}

	// Safely close the channel
	safeClose := func() {
		resultChanMutex.Lock()
		defer resultChanMutex.Unlock()

		if !resultChanClosed {
			resultChanClosed = true
			close(resultsChan)
		}
	}

	// Start worker goroutines
	var workerWg sync.WaitGroup
	for i := 0; i < t.concurrency; i++ {
		workerWg.Add(1)
		go func(id int) {
			defer workerWg.Done()

			for {
				select {
				case <-testCtx.Done():
					return
				default:
					start := time.Now()
					// Create request with special user agent
					req, err := http.NewRequestWithContext(testCtx, "GET", targetURL.String(), nil)
					if err != nil {
						fmt.Printf("Error creating request to %s: %v\n", targetURL.String(), err)
						safeSend(&Result{Error: err})
						time.Sleep(100 * time.Millisecond) // Back off on errors
						continue
					}

					// Add custom headers to help identify our requests
					req.Header.Add("User-Agent", "Pod-Rightsizer/1.0")

					// Make the request
					resp, err := t.client.Do(req)
					latency := time.Since(start)

					if err != nil {
						// Extract more details about the error
						var netErr net.Error
						if errors.As(err, &netErr) && netErr.Timeout() {
							fmt.Printf("Network timeout error: %v\n", err)
						} else if strings.Contains(err.Error(), "connection refused") {
							fmt.Printf("Connection refused: %v (is the service running?)\n", err)
						} else {
							fmt.Printf("HTTP request error: %v\n", err)
						}

						safeSend(&Result{Latency: latency, Error: err})
						time.Sleep(100 * time.Millisecond) // Back off on errors
						continue
					}

					// Discard and close body
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()

					safeSend(&Result{
						Latency:    latency,
						StatusCode: resp.StatusCode,
					})

					// Small delay to prevent excessive CPU usage
					select {
					case <-testCtx.Done():
						return
					case <-time.After(10 * time.Millisecond):
						// Continue after delay
					}
				}
			}
		}(i)
	}

	// Wait for test completion
	select {
	case <-ctx.Done():
		testCancel()
		fmt.Println("Concurrent test was canceled")
	case <-testCtx.Done():
		// Test completed normally
	}

	// Start a goroutine to wait for all workers to finish before closing the channel
	go func() {
		workerWg.Wait()
		safeClose() // Safely close the channel when all workers are done
	}()

	// Wait for metrics collection to finish
	<-resultsDone

	// Wait for all goroutines to complete
	wg.Wait()

	return nil
}

// validateTarget ensures the target is a valid URL and normalizes it
func (t *Tester) validateTarget() (*url.URL, error) {
	target := t.target

	// Make sure target has a valid URL scheme
	if !isURL(target) {
		target = "http://" + target
		fmt.Printf("Added http:// prefix, target is now: %s\n", target)
	}

	parsedURL, err := url.Parse(target)
	if err != nil {
		return nil, err
	}

	fmt.Printf("Validated target URL: %s\n", parsedURL.String())
	return parsedURL, nil
}

// isURL checks if a string looks like a URL with a scheme
func isURL(s string) bool {
	// Check for http:// prefix
	hasHTTP := len(s) >= 7 && s[0:7] == "http://"

	// Check for https:// prefix
	hasHTTPS := len(s) >= 8 && s[0:8] == "https://"

	return hasHTTP || hasHTTPS
}

// Metrics holds load test metrics
type Metrics struct {
	Requests     int
	Success      int
	Failures     int
	StatusCodes  map[int]int
	TotalLatency time.Duration
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
	if m.Requests == 0 || m.TotalLatency == 0 {
		return 0
	}

	return float64(m.Requests) / m.TotalLatency.Seconds()
}

// PrintSummary prints a summary of the metrics to stdout
func (m *Metrics) PrintSummary() {
	fmt.Fprintf(os.Stdout, "\nLoad Test Results\n")
	fmt.Fprintf(os.Stdout, "----------------\n")
	fmt.Fprintf(os.Stdout, "Total Requests: %d\n", m.Requests)
	fmt.Fprintf(os.Stdout, "Successful Requests: %d\n", m.Success)
	fmt.Fprintf(os.Stdout, "Failed Requests: %d\n", m.Failures)
	fmt.Fprintf(os.Stdout, "Success Rate: %.2f%%\n", m.SuccessRate())

	if m.Requests > 0 {
		fmt.Fprintf(os.Stdout, "Mean Latency: %.2fms\n", float64(m.MeanLatency().Microseconds())/1000.0)

		if m.MinLatency < 24*time.Hour {
			fmt.Fprintf(os.Stdout, "Min Latency: %.2fms\n", float64(m.MinLatency.Microseconds())/1000.0)
		}
		fmt.Fprintf(os.Stdout, "Max Latency: %.2fms\n", float64(m.MaxLatency.Microseconds())/1000.0)
		fmt.Fprintf(os.Stdout, "Throughput: %.2f req/s\n", m.Throughput())
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
