package main

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/BogdanDolia/pod-rightsizer/pkg/kubernetes"
	"github.com/BogdanDolia/pod-rightsizer/pkg/loadtest"
	"github.com/BogdanDolia/pod-rightsizer/pkg/metrics"
)

func TestBuildRecommendationsRejectsRunError(t *testing.T) {
	baseTime := time.Date(2026, time.August, 11, 10, 0, 0, 0, time.UTC)
	samples := []metrics.ResourceMetrics{
		testResourceMetrics(baseTime),
		testResourceMetrics(baseTime.Add(15 * time.Second)),
		testResourceMetrics(baseTime.Add(30 * time.Second)),
	}

	_, err := buildRecommendations(
		samples,
		kubernetes.ResourceSettings{},
		20,
		3,
		errors.New("metrics API unavailable"),
		loadtest.RunResult{},
		loadtest.SLO{},
	)
	if err == nil || !strings.Contains(err.Error(), "metrics API unavailable") {
		t.Fatalf("buildRecommendations() error = %v, want collection error", err)
	}
}

func TestBuildRecommendationsRejectsFailedLoadTestSLO(t *testing.T) {
	baseTime := time.Date(2026, time.August, 11, 10, 0, 0, 0, time.UTC)
	samples := []metrics.ResourceMetrics{
		testResourceMetrics(baseTime),
		testResourceMetrics(baseTime.Add(15 * time.Second)),
		testResourceMetrics(baseTime.Add(30 * time.Second)),
	}
	loadResult := loadtest.RunResult{
		Requests:          100,
		ActualRPS:         40,
		HTTPErrorRate:     0.05,
		P95Latency:        2 * time.Second,
		TerminationReason: loadtest.TerminationDurationElapsed,
	}

	_, err := buildRecommendations(
		samples,
		kubernetes.ResourceSettings{},
		20,
		3,
		nil,
		loadResult,
		loadtest.SLO{
			MinimumRPS:           47.5,
			MaximumHTTPErrorRate: 0.01,
			MaximumP95Latency:    time.Second,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "load test did not meet SLO") {
		t.Fatalf("buildRecommendations() error = %v, want SLO error", err)
	}
}

func TestNextMetricsPollDelayUsesSourceWindow(t *testing.T) {
	now := time.Date(2026, time.August, 11, 10, 0, 0, 0, time.UTC)
	sample := testResourceMetrics(now)
	sample.Window = 30 * time.Second

	if delay := nextMetricsPollDelay(sample, now); delay != 30*time.Second {
		t.Fatalf("nextMetricsPollDelay() = %s, want source window 30s", delay)
	}
}

func TestValidateConfigAcceptsSupportedBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		update func(*Config)
	}{
		{
			name: "minimum values with RPS mode",
			update: func(cfg *Config) {
				cfg.Duration = time.Nanosecond
				cfg.RPS = 1
				cfg.Margin = minimumMargin
			},
		},
		{
			name: "maximum values with concurrency mode",
			update: func(cfg *Config) {
				cfg.DeploymentName = "api.production"
				cfg.Duration = maximumLoadTestDuration
				cfg.RPS = 0
				cfg.Concurrency = maximumConcurrency
				cfg.Margin = maximumMargin
			},
		},
		{
			name: "maximum RPS",
			update: func(cfg *Config) {
				cfg.RPS = maximumRPS
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig()
			test.update(&cfg)

			if err := validateConfig(cfg); err != nil {
				t.Fatalf("validateConfig() error = %v, want nil", err)
			}
		})
	}
}

func TestValidateConfigRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name    string
		update  func(*Config)
		wantErr string
	}{
		{
			name:    "blank target",
			update:  func(cfg *Config) { cfg.Target = " \t" },
			wantErr: "--target parameter is required",
		},
		{
			name:    "missing namespace",
			update:  func(cfg *Config) { cfg.Namespace = "" },
			wantErr: "--namespace parameter is required",
		},
		{
			name:    "invalid namespace",
			update:  func(cfg *Config) { cfg.Namespace = "Production" },
			wantErr: "--namespace must be a valid Kubernetes namespace",
		},
		{
			name:    "missing workload",
			update:  func(cfg *Config) { cfg.DeploymentName = "" },
			wantErr: "--deployment parameter is required",
		},
		{
			name:    "invalid workload",
			update:  func(cfg *Config) { cfg.DeploymentName = "api/service" },
			wantErr: "--deployment must be a valid Kubernetes workload name",
		},
		{
			name:    "missing container",
			update:  func(cfg *Config) { cfg.ContainerName = "" },
			wantErr: "--container parameter is required",
		},
		{
			name:    "invalid container",
			update:  func(cfg *Config) { cfg.ContainerName = "API" },
			wantErr: "--container must be a valid Kubernetes container name",
		},
		{
			name:    "zero duration",
			update:  func(cfg *Config) { cfg.Duration = 0 },
			wantErr: "--duration must be greater than zero",
		},
		{
			name:    "negative duration",
			update:  func(cfg *Config) { cfg.Duration = -time.Second },
			wantErr: "--duration must be greater than zero",
		},
		{
			name:    "duration above maximum",
			update:  func(cfg *Config) { cfg.Duration = maximumLoadTestDuration + time.Nanosecond },
			wantErr: "--duration must not exceed 24h0m0s",
		},
		{
			name:    "margin below minimum",
			update:  func(cfg *Config) { cfg.Margin = minimumMargin - 1 },
			wantErr: "--margin must be between 0 and 100",
		},
		{
			name:    "margin above maximum",
			update:  func(cfg *Config) { cfg.Margin = maximumMargin + 1 },
			wantErr: "--margin must be between 0 and 100",
		},
		{
			name: "no positive load",
			update: func(cfg *Config) {
				cfg.RPS = 0
				cfg.Concurrency = 0
			},
			wantErr: "either --rps or --concurrency must be greater than zero",
		},
		{
			name: "negative RPS",
			update: func(cfg *Config) {
				cfg.RPS = -1
				cfg.Concurrency = 1
			},
			wantErr: "--rps must not be negative",
		},
		{
			name:    "RPS above maximum",
			update:  func(cfg *Config) { cfg.RPS = maximumRPS + 1 },
			wantErr: "--rps must not exceed 10000",
		},
		{
			name:    "negative concurrency",
			update:  func(cfg *Config) { cfg.Concurrency = -1 },
			wantErr: "--concurrency must not be negative",
		},
		{
			name:    "concurrency above maximum",
			update:  func(cfg *Config) { cfg.Concurrency = maximumConcurrency + 1 },
			wantErr: "--concurrency must not exceed 1000",
		},
		{
			name:    "negative minimum actual RPS",
			update:  func(cfg *Config) { cfg.MinimumActualRPS = -1 },
			wantErr: "--min-actual-rps must be a finite non-negative number",
		},
		{
			name:    "non-finite minimum actual RPS",
			update:  func(cfg *Config) { cfg.MinimumActualRPS = math.NaN() },
			wantErr: "--min-actual-rps must be a finite non-negative number",
		},
		{
			name:    "negative maximum HTTP error rate",
			update:  func(cfg *Config) { cfg.MaximumHTTPErrorRate = -0.1 },
			wantErr: "--max-http-error-rate must be between 0 and 100",
		},
		{
			name:    "maximum HTTP error rate above 100",
			update:  func(cfg *Config) { cfg.MaximumHTTPErrorRate = 100.1 },
			wantErr: "--max-http-error-rate must be between 0 and 100",
		},
		{
			name:    "zero maximum p95 latency",
			update:  func(cfg *Config) { cfg.MaximumP95Latency = 0 },
			wantErr: "--max-p95-latency must be greater than zero",
		},
		{
			name:    "too few minimum samples",
			update:  func(cfg *Config) { cfg.MinimumSamples = 1 },
			wantErr: "--min-samples must be at least 2",
		},
		{
			name:    "invalid output format",
			update:  func(cfg *Config) { cfg.OutputFormat = "xml" },
			wantErr: "--output-format must be one of: text, json, yaml",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig()
			test.update(&cfg)

			err := validateConfig(cfg)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("validateConfig() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func validConfig() Config {
	return Config{
		Target:               "http://localhost:8080",
		DeploymentName:       "api",
		ContainerName:        "server",
		Namespace:            "default",
		Duration:             time.Minute,
		RPS:                  50,
		MaximumHTTPErrorRate: defaultMaximumErrorPercent,
		MaximumP95Latency:    defaultMaximumP95Latency,
		Margin:               20,
		MinimumSamples:       3,
		OutputFormat:         "text",
	}
}

func TestLoadTestSLOUsesSafeDefaults(t *testing.T) {
	cfg := validConfig()
	slo := loadTestSLO(cfg)

	if slo.MinimumRPS != 47.5 {
		t.Fatalf("MinimumRPS = %.2f, want 47.50", slo.MinimumRPS)
	}
	if slo.MaximumHTTPErrorRate != 0.01 {
		t.Fatalf("MaximumHTTPErrorRate = %.4f, want 0.01", slo.MaximumHTTPErrorRate)
	}
	if slo.MaximumP95Latency != time.Second {
		t.Fatalf("MaximumP95Latency = %s, want 1s", slo.MaximumP95Latency)
	}

	cfg.Concurrency = 10
	if minimum := loadTestSLO(cfg).MinimumRPS; minimum != 0 {
		t.Fatalf("concurrency-mode MinimumRPS = %.2f, want disabled", minimum)
	}
}

func testResourceMetrics(timestamp time.Time) metrics.ResourceMetrics {
	return metrics.ResourceMetrics{
		ContainerName: "worker",
		Timestamp:     timestamp,
		Window:        15 * time.Second,
		CPUUsage:      0.1,
		MemoryUsage:   100,
	}
}
