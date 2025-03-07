package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/BogdanDolia/pod-rightsizer/pkg/kubernetes"
	"github.com/BogdanDolia/pod-rightsizer/pkg/loadtest"
	"github.com/BogdanDolia/pod-rightsizer/pkg/metrics"
	"github.com/BogdanDolia/pod-rightsizer/pkg/output"
	"github.com/BogdanDolia/pod-rightsizer/pkg/recommender"
)

// Config holds the CLI configuration
type Config struct {
	Target         string // Load test target
	ServiceName    string // Kubernetes service name for metrics collection
	Namespace      string
	Duration       time.Duration
	RPS            int
	Concurrency    int
	Margin         int
	OutputFormat   string
	KubeconfigPath string
}

func main() {
	// Parse command line arguments
	cfg := parseFlags()

	// Set up context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up signal handling for clean shutdown
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-signalChan
		fmt.Println("\nReceived termination signal. Stopping gracefully...")
		cancel()
	}()

	// Initialize Kubernetes client
	k8sClient, err := kubernetes.NewClient(cfg.KubeconfigPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing Kubernetes client: %v\n", err)
		os.Exit(1)
	}

	// Get initial resource settings to compare against
	fmt.Println("Fetching current resource settings...")
	currentSettings, err := k8sClient.GetResourceSettings(ctx, cfg.Namespace, cfg.ServiceName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting current resource settings: %v\n", err)
		os.Exit(1)
	}

	// Initialize metrics collector
	fmt.Printf("Initializing metrics collector for service '%s' in namespace '%s'...\n",
		cfg.ServiceName, cfg.Namespace)
	metricsCollector := metrics.NewCollector(k8sClient, cfg.Namespace, cfg.ServiceName)

	// Initialize load tester
	fmt.Println("Initializing load test...")
	loadTester := loadtest.NewTester(cfg.Target, cfg.RPS, cfg.Concurrency)

	// Run load test and collect metrics
	fmt.Printf("Starting load test (%d RPS for %s)...\n", cfg.RPS, cfg.Duration)
	metricsChan := make(chan metrics.ResourceMetrics)

	// Start metrics collection in a goroutine
	go func() {
		defer close(metricsChan)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m, err := metricsCollector.CollectMetrics(ctx)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error collecting metrics: %v\n", err)
					continue
				}
				metricsChan <- m
			}
		}
	}()

	// Use a WaitGroup to track when all goroutines are done
	var wg sync.WaitGroup

	// Start load test and wait for completion or cancellation
	resultChan := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		resultChan <- loadTester.Run(ctx, cfg.Duration)
	}()

	// Collect all metrics during the test
	var allMetrics []metrics.ResourceMetrics
	metricsCollectionDone := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(metricsCollectionDone)

		for m := range metricsChan {
			allMetrics = append(allMetrics, m)
			fmt.Printf("Collected metrics - CPU: %.1fm, Memory: %.1fMi\n", m.CPUUsage*1000, m.MemoryUsage)
		}
	}()

	// Wait for load test to complete or context cancellation
	loadTestFinished := false
	select {
	case err := <-resultChan:
		loadTestFinished = true
		if err != nil {
			fmt.Fprintf(os.Stderr, "Load test failed: %v\n", err)
		} else {
			fmt.Println("Load test completed successfully.")
		}

		// Allow final metrics to be collected
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
		}

		cancel() // Stop metrics collection
	case <-ctx.Done():
		fmt.Println("Operation was cancelled.")
	}

	// Wait for metrics collection to finish
	<-metricsCollectionDone

	// Wait for all goroutines to complete
	wg.Wait()

	// Handle case where load test was cancelled
	if !loadTestFinished {
		fmt.Println("Load test did not complete properly.")
	}

	// Generate recommendations based on collected metrics
	if len(allMetrics) == 0 {
		fmt.Fprintf(os.Stderr, "No metrics collected. Cannot generate recommendations.\n")
		os.Exit(1)
	}

	fmt.Println("Analyzing metrics and generating recommendations...")
	recommendations := recommender.GenerateRecommendations(allMetrics, currentSettings, cfg.Margin)

	// Output results
	result := output.Result{
		Target:          cfg.Target,
		ServiceName:     cfg.ServiceName,
		Namespace:       cfg.Namespace,
		Duration:        cfg.Duration,
		RPS:             cfg.RPS,
		CurrentSettings: currentSettings,
		Metrics:         allMetrics,
		Recommendations: recommendations,
	}

	output.PrintResults(result, cfg.OutputFormat)
}

func parseFlags() Config {
	var (
		target         = flag.String("target", "", "Target service URL or identifier for load testing")
		serviceName    = flag.String("service-name", "", "Kubernetes service name for metrics collection (defaults to target if not specified)")
		namespace      = flag.String("namespace", "default", "Kubernetes namespace")
		durationStr    = flag.String("duration", "5m", "Duration of the load test")
		rps            = flag.Int("rps", 50, "Requests per second for load testing")
		concurrency    = flag.Int("concurrency", 0, "Alternative to RPS, number of concurrent connections")
		margin         = flag.Int("margin", 20, "Safety margin percentage to add to recommendations")
		outputFormat   = flag.String("output-format", "text", "Output format: text, json, or yaml")
		kubeconfigPath = flag.String("kubeconfig", "", "Path to kubeconfig file for external cluster access")
	)

	flag.Parse()

	if *target == "" {
		_, err := fmt.Fprintf(os.Stderr, "Error: --target parameter is required\n")
		if err != nil {
			return Config{}
		}
		flag.Usage()
		os.Exit(1)
	}

	if *outputFormat != "text" && *outputFormat != "json" && *outputFormat != "yaml" {
		_, err := fmt.Fprintf(os.Stderr, "Error: --output-format must be one of: text, json, yaml\n")
		if err != nil {
			return Config{}
		}
		flag.Usage()
		os.Exit(1)
	}

	duration, err := time.ParseDuration(*durationStr)
	if err != nil {
		_, err := fmt.Fprintf(os.Stderr, "Error: invalid duration format: %v\n", err)
		if err != nil {
			return Config{}
		}
		flag.Usage()
		os.Exit(1)
	}

	// If service-name is not specified, use the target value
	serviceNameValue := *serviceName
	if serviceNameValue == "" {
		serviceNameValue = *target
		fmt.Printf("Note: Using target value '%s' as service name for metrics collection.\n", serviceNameValue)
		fmt.Printf("To specify a different service name, use the --service-name flag.\n")
	} else {
		fmt.Printf("Using '%s' as service name for metrics collection, and '%s' as load test target.\n",
			serviceNameValue, *target)
	}

	return Config{
		Target:         *target,
		ServiceName:    serviceNameValue,
		Namespace:      *namespace,
		Duration:       duration,
		RPS:            *rps,
		Concurrency:    *concurrency,
		Margin:         *margin,
		OutputFormat:   *outputFormat,
		KubeconfigPath: *kubeconfigPath,
	}
}
