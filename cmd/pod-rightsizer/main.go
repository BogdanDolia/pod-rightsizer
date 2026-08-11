package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/BogdanDolia/pod-rightsizer/pkg/kubernetes"
	"github.com/BogdanDolia/pod-rightsizer/pkg/loadtest"
	"github.com/BogdanDolia/pod-rightsizer/pkg/metrics"
	"github.com/BogdanDolia/pod-rightsizer/pkg/output"
	"github.com/BogdanDolia/pod-rightsizer/pkg/recommender"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

// Config holds the CLI configuration
type Config struct {
	Target                  string // Load test target
	DeploymentName          string // Kubernetes Deployment to right-size
	ContainerName           string // Container within the Deployment to right-size
	Namespace               string
	Duration                time.Duration
	RPS                     int
	Concurrency             int
	MinimumActualRPS        float64
	MaximumHTTPErrorRate    float64
	MaximumP95Latency       time.Duration
	CPUPercentile           float64
	CPURequestBufferPercent float64
	MemoryBufferPercent     float64
	CPULimitMode            recommender.LimitMode
	CPULimitMultiplier      float64
	MemoryLimitMode         recommender.LimitMode
	MemoryLimitMultiplier   float64
	MinimumSamples          int
	OutputFormat            string
	KubeconfigPath          string
}

const (
	minimumMetricsPollInterval = time.Second
	maximumLoadTestDuration    = 24 * time.Hour
	maximumRPS                 = 10_000
	maximumConcurrency         = 1_000
	defaultMinimumRPSRatio     = 0.95
	defaultMaximumErrorPercent = 1.0
	defaultMaximumP95Latency   = time.Second
)

type loadTestOutcome struct {
	Result     loadtest.RunResult
	Err        error
	StartedAt  time.Time
	FinishedAt time.Time
}

func main() {
	// Parse command line arguments
	cfg := parseFlags()

	// Set up signal-aware cancellation without leaving a signal goroutine behind.
	ctx, stopSignals := signal.NotifyContext(
		context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer stopSignals()
	measurementCtx, stopMeasurement := context.WithCancel(ctx)
	defer stopMeasurement()

	// Initialize Kubernetes client
	k8sClient, err := kubernetes.NewClient(cfg.KubeconfigPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing Kubernetes client: %v\n", err)
		os.Exit(1)
	}

	// Resolve the Deployment's real pod selector and validate the target container.
	fmt.Fprintf(os.Stderr, "Resolving deployment '%s' and container '%s' in namespace '%s'...\n",
		cfg.DeploymentName, cfg.ContainerName, cfg.Namespace)
	workload, err := k8sClient.ResolveWorkload(
		ctx,
		cfg.Namespace,
		cfg.DeploymentName,
		cfg.ContainerName,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving workload: %v\n", err)
		os.Exit(1)
	}

	// Get initial resource settings to compare against
	fmt.Fprintln(os.Stderr, "Fetching current resource settings...")
	currentSettings, err := k8sClient.GetResourceSettings(ctx, cfg.Namespace, workload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting current resource settings: %v\n", err)
		os.Exit(1)
	}

	// Initialize metrics collector
	fmt.Fprintf(os.Stderr, "Initializing metrics collector for deployment '%s', container '%s'...\n",
		workload.DeploymentName, workload.ContainerName)
	metricsCollector := metrics.NewCollector(k8sClient, cfg.Namespace, workload)

	// Initialize load tester
	fmt.Fprintln(os.Stderr, "Initializing load test...")
	loadTester := loadtest.NewTester(cfg.Target, cfg.RPS, cfg.Concurrency)

	// Run load test and collect metrics
	loadMode := fmt.Sprintf("%d RPS", cfg.RPS)
	if cfg.Concurrency > 0 {
		loadMode = fmt.Sprintf("%d concurrent workers", cfg.Concurrency)
	}
	fmt.Fprintf(os.Stderr, "Starting load test (%s for %s)...\n", loadMode, cfg.Duration)
	metricsChan := make(chan metrics.ResourceMetrics)
	metricsErrChan := make(chan error, 1)

	// Use a WaitGroup to track when all goroutines are done
	var wg sync.WaitGroup

	// Start metrics collection in a goroutine. The collector uses the source
	// timestamp/window and emits each Metrics API snapshot at most once.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(metricsChan)
		collectMetrics(measurementCtx, metricsCollector, metricsChan, metricsErrChan)
	}()

	// Start load test and wait for completion or cancellation
	resultChan := make(chan loadTestOutcome, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		startedAt := time.Now().UTC()
		result, err := loadTester.Run(measurementCtx, cfg.Duration)
		resultChan <- loadTestOutcome{
			Result:     result,
			Err:        err,
			StartedAt:  startedAt,
			FinishedAt: time.Now().UTC(),
		}
	}()

	// Collect metrics and fail the entire run on either a source error or a load
	// test error. A partial successful prefix must never produce a recommendation.
	var allMetrics []metrics.ResourceMetrics
	var runErr error
	var loadTestResult loadtest.RunResult
	var loadTestStartedAt time.Time
	var loadTestFinishedAt time.Time
	loadTestFinished := false
	var finalMetricsTimer *time.Timer
	var finalMetricsDone <-chan time.Time

collectionLoop:
	for {
		select {
		case m, ok := <-metricsChan:
			if !ok {
				metricsChan = nil
				continue
			}
			allMetrics = append(allMetrics, m)
			fmt.Fprintf(
				os.Stderr,
				"Collected metrics for container %s - CPU: %.1fm, Memory: %.1fMi (source: %s, window: %s)\n",
				m.ContainerName,
				m.CPUUsage*1000,
				m.MemoryUsage,
				m.Timestamp.Format(time.RFC3339),
				m.Window,
			)
		case err := <-metricsErrChan:
			runErr = fmt.Errorf("metrics collection failed: %w", err)
			break collectionLoop
		case outcome := <-resultChan:
			loadTestFinished = true
			resultChan = nil
			loadTestResult = outcome.Result
			loadTestStartedAt = outcome.StartedAt
			loadTestFinishedAt = outcome.FinishedAt
			if outcome.Err != nil {
				runErr = fmt.Errorf("load test failed: %w", outcome.Err)
				break collectionLoop
			}

			fmt.Fprintln(os.Stderr, "Load test completed successfully.")
			resolution := metrics.SourceResolution(allMetrics)
			if resolution <= 0 {
				break collectionLoop
			}
			// Give the source one complete window plus one poll interval to
			// publish a final snapshot whose source window is fully contained
			// in the load test.
			finalMetricsTimer = time.NewTimer(resolution + minimumMetricsPollInterval)
			finalMetricsDone = finalMetricsTimer.C
		case <-finalMetricsDone:
			break collectionLoop
		case <-ctx.Done():
			runErr = fmt.Errorf("operation cancelled: %w", ctx.Err())
			break collectionLoop
		}
	}

	if finalMetricsTimer != nil {
		finalMetricsTimer.Stop()
	}
	stopMeasurement()

	// Wait for all goroutines to complete
	wg.Wait()

	// A metrics request may fail at the same instant as the final timer fires.
	// Check the buffered terminal error after the collector has stopped so that
	// this race cannot turn a failed collection into a recommendation.
	select {
	case err := <-metricsErrChan:
		if runErr == nil {
			runErr = fmt.Errorf("metrics collection failed: %w", err)
		}
	default:
	}

	if runErr != nil {
		fmt.Fprintf(os.Stderr, "Cannot generate recommendations: %v\n", runErr)
		os.Exit(1)
	}
	if !loadTestFinished {
		fmt.Fprintln(os.Stderr, "Cannot generate recommendations: load test did not complete")
		os.Exit(1)
	}
	measurementMetrics, err := metrics.FullyContainedSamples(
		allMetrics,
		loadTestStartedAt,
		loadTestFinishedAt,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot validate measurement window: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintln(os.Stderr, "Analyzing metrics and generating recommendations...")
	recommendations, err := buildRecommendations(
		measurementMetrics,
		currentSettings,
		recommendationPolicy(cfg),
		runErr,
		loadTestResult,
		loadTestSLO(cfg),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot generate recommendations: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintln(os.Stderr, "Validating resource patch with Kubernetes server-side dry-run...")
	resourcePatch, err := k8sClient.PrepareResourcePatch(
		ctx,
		cfg.Namespace,
		workload,
		kubernetes.ResourceSettings{
			CPURequest:    recommendations.CPURequest,
			CPULimit:      recommendations.CPULimit,
			MemoryRequest: recommendations.MemoryRequest,
			MemoryLimit:   recommendations.MemoryLimit,
		},
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot generate safe resource patch: %v\n", err)
		os.Exit(1)
	}

	// Output results
	result := output.Result{
		Target:          cfg.Target,
		Namespace:       cfg.Namespace,
		Workload:        workload,
		Duration:        cfg.Duration,
		RPS:             cfg.RPS,
		Concurrency:     cfg.Concurrency,
		LoadTest:        loadTestResult,
		LoadTestSLO:     loadTestSLO(cfg),
		CurrentSettings: currentSettings,
		Metrics:         measurementMetrics,
		Recommendations: recommendations,
		ResourcePatch:   resourcePatch,
	}

	if err := output.PrintResults(result, cfg.OutputFormat); err != nil {
		fmt.Fprintf(os.Stderr, "Cannot write results: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags() Config {
	var (
		target                = flag.String("target", "", "Target service URL or identifier for load testing")
		deploymentName        = flag.String("deployment", "", "Kubernetes Deployment to right-size")
		containerName         = flag.String("container", "", "Container within the Deployment to right-size")
		namespace             = flag.String("namespace", "default", "Kubernetes namespace")
		durationStr           = flag.String("duration", "5m", "Duration of the load test (positive, maximum 24h)")
		rps                   = flag.Int("rps", 50, "Requests per second for load testing (maximum 10000)")
		concurrency           = flag.Int("concurrency", 0, "Alternative to RPS, number of concurrent connections (maximum 1000)")
		minimumActualRPS      = flag.Float64("min-actual-rps", 0, "Minimum actual RPS SLO (default: 95% of --rps; disabled by default in concurrency mode)")
		maximumHTTPErrorRate  = flag.Float64("max-http-error-rate", defaultMaximumErrorPercent, "Maximum HTTP error rate SLO as a percentage (0-100)")
		maximumP95Latency     = flag.Duration("max-p95-latency", defaultMaximumP95Latency, "Maximum p95 latency SLO")
		cpuPercentile         = flag.Float64("cpu-percentile", recommender.DefaultCPUPercentile, "CPU percentile used for the request (0-100]")
		cpuRequestBuffer      = flag.Float64("cpu-request-buffer", recommender.DefaultCPURequestBufferPercent, "Buffer added to the CPU percentile as a percentage")
		memoryBuffer          = flag.Float64("memory-buffer", recommender.DefaultMemoryBufferPercent, "Buffer added to the memory high-water mark as a percentage")
		cpuLimitMode          = flag.String("cpu-limit-policy", string(recommender.LimitNone), "CPU limit policy: none, keep, request-multiplier, or peak-multiplier")
		cpuLimitMultiplier    = flag.Float64("cpu-limit-multiplier", 1.0, "CPU multiplier for multiplier limit policies")
		memoryLimitMode       = flag.String("memory-limit-policy", string(recommender.LimitRequestMultiplier), "Memory limit policy: none, keep, request-multiplier, or peak-multiplier")
		memoryLimitMultiplier = flag.Float64("memory-limit-multiplier", recommender.DefaultMemoryLimitMultiplier, "Memory multiplier for multiplier limit policies")
		minimumSamples        = flag.Int("min-samples", recommender.DefaultMinimumSamples, "Minimum number of non-overlapping Metrics API samples")
		outputFormat          = flag.String("output-format", "text", "Output format: text, json, or yaml")
		kubeconfigPath        = flag.String("kubeconfig", "", "Path to kubeconfig file for external cluster access")
	)

	flag.Parse()

	duration, err := time.ParseDuration(*durationStr)
	if err != nil {
		_, err := fmt.Fprintf(os.Stderr, "Error: invalid duration format: %v\n", err)
		if err != nil {
			return Config{}
		}
		flag.Usage()
		os.Exit(1)
	}

	cfg := Config{
		Target:                  *target,
		DeploymentName:          *deploymentName,
		ContainerName:           *containerName,
		Namespace:               *namespace,
		Duration:                duration,
		RPS:                     *rps,
		Concurrency:             *concurrency,
		MinimumActualRPS:        *minimumActualRPS,
		MaximumHTTPErrorRate:    *maximumHTTPErrorRate,
		MaximumP95Latency:       *maximumP95Latency,
		CPUPercentile:           *cpuPercentile,
		CPURequestBufferPercent: *cpuRequestBuffer,
		MemoryBufferPercent:     *memoryBuffer,
		CPULimitMode:            recommender.LimitMode(*cpuLimitMode),
		CPULimitMultiplier:      *cpuLimitMultiplier,
		MemoryLimitMode:         recommender.LimitMode(*memoryLimitMode),
		MemoryLimitMultiplier:   *memoryLimitMultiplier,
		MinimumSamples:          *minimumSamples,
		OutputFormat:            *outputFormat,
		KubeconfigPath:          *kubeconfigPath,
	}
	if err := validateConfig(cfg); err != nil {
		_, writeErr := fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		if writeErr != nil {
			return Config{}
		}
		flag.Usage()
		os.Exit(1)
	}

	return cfg
}

func validateConfig(cfg Config) error {
	if strings.TrimSpace(cfg.Target) == "" {
		return errors.New("--target parameter is required")
	}

	if err := validateKubernetesName(
		"namespace",
		cfg.Namespace,
		"namespace",
		k8svalidation.IsDNS1123Label,
	); err != nil {
		return err
	}
	if err := validateKubernetesName(
		"deployment",
		cfg.DeploymentName,
		"workload name",
		k8svalidation.IsDNS1123Subdomain,
	); err != nil {
		return err
	}
	if err := validateKubernetesName(
		"container",
		cfg.ContainerName,
		"container name",
		k8svalidation.IsDNS1123Label,
	); err != nil {
		return err
	}

	if cfg.Duration <= 0 {
		return errors.New("--duration must be greater than zero")
	}
	if cfg.Duration > maximumLoadTestDuration {
		return fmt.Errorf("--duration must not exceed %s", maximumLoadTestDuration)
	}
	if cfg.RPS < 0 {
		return errors.New("--rps must not be negative")
	}
	if cfg.RPS > maximumRPS {
		return fmt.Errorf("--rps must not exceed %d", maximumRPS)
	}
	if cfg.Concurrency < 0 {
		return errors.New("--concurrency must not be negative")
	}
	if cfg.Concurrency > maximumConcurrency {
		return fmt.Errorf("--concurrency must not exceed %d", maximumConcurrency)
	}
	if cfg.RPS == 0 && cfg.Concurrency == 0 {
		return errors.New("either --rps or --concurrency must be greater than zero")
	}
	if cfg.RPS > 0 && cfg.Concurrency > 0 {
		return errors.New("--rps and --concurrency are mutually exclusive")
	}
	if math.IsNaN(cfg.MinimumActualRPS) || math.IsInf(cfg.MinimumActualRPS, 0) || cfg.MinimumActualRPS < 0 {
		return errors.New("--min-actual-rps must be a finite non-negative number")
	}
	if math.IsNaN(cfg.MaximumHTTPErrorRate) ||
		math.IsInf(cfg.MaximumHTTPErrorRate, 0) ||
		cfg.MaximumHTTPErrorRate < 0 ||
		cfg.MaximumHTTPErrorRate > 100 {
		return errors.New("--max-http-error-rate must be between 0 and 100")
	}
	if cfg.MaximumP95Latency <= 0 {
		return errors.New("--max-p95-latency must be greater than zero")
	}

	if cfg.MinimumSamples < 2 {
		return errors.New("--min-samples must be at least 2")
	}
	if err := recommendationPolicy(cfg).Validate(); err != nil {
		return fmt.Errorf("invalid recommendation policy: %w", err)
	}
	if cfg.OutputFormat != "text" && cfg.OutputFormat != "json" && cfg.OutputFormat != "yaml" {
		return errors.New("--output-format must be one of: text, json, yaml")
	}

	return nil
}

func validateKubernetesName(
	flagName string,
	value string,
	kind string,
	validate func(string) []string,
) error {
	if value == "" {
		return fmt.Errorf("--%s parameter is required", flagName)
	}
	if problems := validate(value); len(problems) > 0 {
		return fmt.Errorf(
			"--%s must be a valid Kubernetes %s: %s",
			flagName,
			kind,
			strings.Join(problems, "; "),
		)
	}
	return nil
}

func collectMetrics(
	ctx context.Context,
	collector *metrics.Collector,
	metricsChan chan<- metrics.ResourceMetrics,
	errChan chan<- error,
) {
	timer := time.NewTimer(0)
	defer timer.Stop()

	var lastObserved metrics.ResourceMetrics
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		sample, err := collector.CollectMetrics(ctx)
		if err != nil {
			if ctx.Err() != nil && errors.Is(err, context.Canceled) {
				return
			}
			errChan <- err
			return
		}

		if !lastObserved.Timestamp.IsZero() {
			switch {
			case sample.Timestamp.Before(lastObserved.Timestamp):
				err = fmt.Errorf(
					"source timestamp moved backwards from %s to %s",
					lastObserved.Timestamp.Format(time.RFC3339Nano),
					sample.Timestamp.Format(time.RFC3339Nano),
				)
			case sample.Timestamp.Equal(lastObserved.Timestamp) &&
				(sample.Window != lastObserved.Window ||
					sample.CPUUsage != lastObserved.CPUUsage ||
					sample.MemoryUsage != lastObserved.MemoryUsage):
				err = fmt.Errorf(
					"source values changed without a new timestamp for container %q",
					sample.ContainerName,
				)
			}
			if err != nil {
				errChan <- err
				return
			}
		}

		if lastObserved.Timestamp.IsZero() || sample.Timestamp.After(lastObserved.Timestamp) {
			lastObserved = sample
			// Preserve every new source snapshot. IndependentSamples later keeps
			// overlapping windows from inflating evidence while retaining their
			// conservative CPU and memory maxima.
			select {
			case metricsChan <- sample:
			case <-ctx.Done():
				return
			}
		}

		timer.Reset(nextMetricsPollDelay(sample, time.Now()))
	}
}

func nextMetricsPollDelay(sample metrics.ResourceMetrics, now time.Time) time.Duration {
	delay := sample.Window
	untilNextWindow := sample.Timestamp.Add(sample.Window).Sub(now)
	if untilNextWindow > 0 && untilNextWindow < delay {
		delay = untilNextWindow
	}
	if delay < minimumMetricsPollInterval {
		return minimumMetricsPollInterval
	}
	return delay
}

func buildRecommendations(
	samples []metrics.ResourceMetrics,
	currentSettings kubernetes.ResourceSettings,
	policy recommender.Policy,
	runErr error,
	loadTestResult loadtest.RunResult,
	slo loadtest.SLO,
) (recommender.Recommendations, error) {
	if runErr != nil {
		return recommender.Recommendations{}, runErr
	}
	assessment, err := loadTestResult.EvaluateSLO(slo)
	if err != nil {
		return recommender.Recommendations{}, fmt.Errorf("invalid load-test SLO: %w", err)
	}
	if !assessment.Passed {
		return recommender.Recommendations{}, fmt.Errorf(
			"load test did not meet SLO: %s",
			strings.Join(assessment.Violations, "; "),
		)
	}
	return recommender.GenerateRecommendations(samples, currentSettings, policy)
}

func recommendationPolicy(cfg Config) recommender.Policy {
	return recommender.Policy{
		CPUPercentile:           cfg.CPUPercentile,
		CPURequestBufferPercent: cfg.CPURequestBufferPercent,
		MemoryBufferPercent:     cfg.MemoryBufferPercent,
		CPULimit: recommender.LimitPolicy{
			Mode:       cfg.CPULimitMode,
			Multiplier: cfg.CPULimitMultiplier,
		},
		MemoryLimit: recommender.LimitPolicy{
			Mode:       cfg.MemoryLimitMode,
			Multiplier: cfg.MemoryLimitMultiplier,
		},
		MinimumSamples: cfg.MinimumSamples,
	}
}

func loadTestSLO(cfg Config) loadtest.SLO {
	minimumRPS := cfg.MinimumActualRPS
	if minimumRPS == 0 && cfg.Concurrency == 0 {
		minimumRPS = float64(cfg.RPS) * defaultMinimumRPSRatio
	}
	return loadtest.SLO{
		MinimumRPS:           minimumRPS,
		MaximumHTTPErrorRate: cfg.MaximumHTTPErrorRate / 100,
		MaximumP95Latency:    cfg.MaximumP95Latency,
	}
}
