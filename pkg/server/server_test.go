package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corek8s "github.com/BogdanDolia/pod-rightsizer/pkg/kubernetes"
	coreloadtest "github.com/BogdanDolia/pod-rightsizer/pkg/loadtest"
	"github.com/BogdanDolia/pod-rightsizer/pkg/recommender"
)

type fakeKubernetesClient struct {
	getResourceSettings  func(context.Context) (corek8s.ResourceSettings, error)
	getPodMetrics        func(context.Context) (corek8s.ContainerMetrics, error)
	prepareResourcePatch func(
		context.Context,
		string,
		corek8s.Workload,
		corek8s.ResourceSettings,
	) (*corek8s.ResourcePatch, error)
}

type fakeLoadTester func(context.Context, time.Duration) (coreloadtest.RunResult, error)

func (tester fakeLoadTester) Run(
	ctx context.Context,
	duration time.Duration,
) (coreloadtest.RunResult, error) {
	return tester(ctx, duration)
}

func (client *fakeKubernetesClient) ResolveWorkload(
	_ context.Context,
	_, deploymentName, containerName string,
) (corek8s.Workload, error) {
	return corek8s.Workload{
		DeploymentName: deploymentName,
		ContainerName:  containerName,
		PodSelector:    "app=" + deploymentName,
	}, nil
}

func (client *fakeKubernetesClient) GetResourceSettings(
	ctx context.Context,
	_ string,
	_ corek8s.Workload,
) (corek8s.ResourceSettings, error) {
	return client.getResourceSettings(ctx)
}

func (client *fakeKubernetesClient) GetPodMetrics(
	ctx context.Context,
	_ string,
	_ corek8s.Workload,
) (corek8s.ContainerMetrics, error) {
	return client.getPodMetrics(ctx)
}

func (client *fakeKubernetesClient) PrepareResourcePatch(
	ctx context.Context,
	namespace string,
	workload corek8s.Workload,
	desired corek8s.ResourceSettings,
) (*corek8s.ResourcePatch, error) {
	if client.prepareResourcePatch != nil {
		return client.prepareResourcePatch(ctx, namespace, workload, desired)
	}
	return corek8s.NewResourcePatch(namespace, workload, desired)
}

func TestRunStatusSnapshotsAreRaceFree(t *testing.T) {
	api := New()
	api.mu.Lock()
	api.runs["run-1"] = RunStatus{RunID: "run-1", Status: statusCreated}
	api.mu.Unlock()

	var workers sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for iteration := 0; iteration < 500; iteration++ {
				completed := time.Now().UTC()
				api.updateRun("run-1", func(run *RunStatus) {
					run.Status = statusRunning
					run.CompletedAt = &completed
					run.MetricsSamples = worker*500 + iteration
					run.Advice = []string{"concurrent update"}
				})
			}
		}(worker)

		workers.Add(1)
		go func() {
			defer workers.Done()
			for iteration := 0; iteration < 500; iteration++ {
				run, ok := api.runSnapshot("run-1")
				if !ok {
					t.Error("runSnapshot() did not find run")
					return
				}
				if _, err := json.Marshal(run); err != nil {
					t.Errorf("json.Marshal() error = %v", err)
					return
				}
			}
		}()
	}
	workers.Wait()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := api.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestAnalysisPassesDeadlineContextsToKubernetes(t *testing.T) {
	api := New()
	api.samplingInterval = 2 * time.Millisecond

	var resourceContextHasDeadline atomic.Bool
	var metricsContextHasDeadline atomic.Bool
	var patchContextHasDeadline atomic.Bool
	var sampleNumber atomic.Int64
	api.newKubernetesClient = func() (analysisKubernetesClient, error) {
		return &fakeKubernetesClient{
			getResourceSettings: func(ctx context.Context) (corek8s.ResourceSettings, error) {
				_, hasDeadline := ctx.Deadline()
				resourceContextHasDeadline.Store(hasDeadline)
				return corek8s.ResourceSettings{}, nil
			},
			getPodMetrics: func(ctx context.Context) (corek8s.ContainerMetrics, error) {
				_, hasDeadline := ctx.Deadline()
				metricsContextHasDeadline.Store(hasDeadline)
				return corek8s.ContainerMetrics{
					ContainerName: "app",
					Timestamp:     time.Now().Add(time.Duration(sampleNumber.Add(1)) * time.Millisecond),
					Window:        time.Millisecond,
					CPUUsage:      0.1,
					MemoryUsage:   64,
				}, nil
			},
			prepareResourcePatch: func(
				ctx context.Context,
				namespace string,
				workload corek8s.Workload,
				desired corek8s.ResourceSettings,
			) (*corek8s.ResourcePatch, error) {
				_, hasDeadline := ctx.Deadline()
				patchContextHasDeadline.Store(hasDeadline)
				return corek8s.NewResourcePatch(namespace, workload, desired)
			},
		}, nil
	}

	body := bytes.NewBufferString(`{
		"namespace":"default",
		"deployment":"api",
		"container":"app",
		"duration":"20ms"
	}`)
	request := httptest.NewRequest(http.MethodPost, "/api/analyze", body)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("POST /api/analyze status = %d, body = %q", response.Code, response.Body.String())
	}

	var created AnalyzeResponse
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatalf("decode AnalyzeResponse: %v", err)
	}
	run := waitForTerminalRun(t, api, created.RunID)
	if run.Status != statusComplete {
		t.Fatalf("run status = %q, error = %q", run.Status, run.Error)
	}
	if !resourceContextHasDeadline.Load() || !metricsContextHasDeadline.Load() || !patchContextHasDeadline.Load() {
		t.Fatalf(
			"Kubernetes contexts had deadlines: resource=%t metrics=%t patch=%t",
			resourceContextHasDeadline.Load(),
			metricsContextHasDeadline.Load(),
			patchContextHasDeadline.Load(),
		)
	}
	if !run.PatchDryRun {
		t.Fatal("completed run did not record successful patch dry-run")
	}
	patchResponse := httptest.NewRecorder()
	api.ServeHTTP(
		patchResponse,
		httptest.NewRequest(http.MethodGet, "/api/runs/"+created.RunID+"/yaml-patch", nil),
	)
	if patchResponse.Code != http.StatusOK || !strings.Contains(patchResponse.Body.String(), "name: app") {
		t.Fatalf("GET yaml-patch status/body = %d/%q", patchResponse.Code, patchResponse.Body.String())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := api.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestAnalysisDryRunErrorSuppressesRecommendationAndPatch(t *testing.T) {
	api := New()
	api.samplingInterval = 2 * time.Millisecond
	var sampleNumber atomic.Int64
	api.newKubernetesClient = func() (analysisKubernetesClient, error) {
		return &fakeKubernetesClient{
			getResourceSettings: func(context.Context) (corek8s.ResourceSettings, error) {
				return corek8s.ResourceSettings{}, nil
			},
			getPodMetrics: func(context.Context) (corek8s.ContainerMetrics, error) {
				return corek8s.ContainerMetrics{
					ContainerName: "app",
					Timestamp:     time.Now().Add(time.Duration(sampleNumber.Add(1)) * time.Millisecond),
					Window:        time.Millisecond,
					CPUUsage:      0.1,
					MemoryUsage:   64,
				}, nil
			},
			prepareResourcePatch: func(
				context.Context,
				string,
				corek8s.Workload,
				corek8s.ResourceSettings,
			) (*corek8s.ResourcePatch, error) {
				return nil, errors.New("admission policy denied dry-run")
			},
		}, nil
	}

	initial := RunStatus{
		RunID:     "dry-run-error",
		Status:    statusCreated,
		CreatedAt: time.Now().UTC(),
		Request: AnalyzeRequest{
			Namespace:  "default",
			Deployment: "api",
			Container:  "app",
			Duration:   "20ms",
		},
	}
	if !api.startAnalysis(initial) {
		t.Fatal("startAnalysis() = false")
	}
	run := waitForTerminalRun(t, api, initial.RunID)
	if run.Status != statusFailed || !strings.Contains(run.Error, "server-side dry-run") {
		t.Fatalf("run = %#v, want dry-run failure", run)
	}
	if run.Recommendation != nil || run.PatchDryRun {
		t.Fatalf("recommendation/dry-run = %#v/%t, want suppressed", run.Recommendation, run.PatchDryRun)
	}

	response := httptest.NewRecorder()
	api.ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, "/api/runs/"+initial.RunID+"/yaml-patch", nil),
	)
	if response.Code != http.StatusConflict {
		t.Fatalf("YAML patch status = %d, want %d", response.Code, http.StatusConflict)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := api.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestAnalyzeRejectsMissingContainer(t *testing.T) {
	api := New()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/analyze",
		bytes.NewBufferString(`{
			"namespace":"default",
			"deployment":"api",
			"duration":"20ms"
		}`),
	)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/analyze status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	if !strings.Contains(response.Body.String(), "container must not be empty") {
		t.Fatalf("response body = %q, want container validation", response.Body.String())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := api.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestAnalyzeRejectsUnknownLegacyFieldsAndInvalidPolicy(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "legacy margin is not silently ignored",
			body: `{
				"namespace":"default",
				"deployment":"api",
				"container":"app",
				"duration":"1s",
				"margin":20
			}`,
			want: "unknown field",
		},
		{
			name: "invalid policy",
			body: `{
				"namespace":"default",
				"deployment":"api",
				"container":"app",
				"duration":"1s",
				"policy":{
					"cpuPercentile":0,
					"cpuRequestBufferPercent":10,
					"memoryBufferPercent":20,
					"cpuLimit":{"mode":"none"},
					"memoryLimit":{"mode":"request-multiplier","multiplier":1.2},
					"minimumSamples":3
				}
			}`,
			want: "invalid policy",
		},
		{
			name: "ambiguous load mode",
			body: `{
				"namespace":"default",
				"deployment":"api",
				"container":"app",
				"duration":"1s",
				"targetURL":"http://example.test",
				"rps":50,
				"concurrency":2
			}`,
			want: "rps and concurrency are mutually exclusive",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			api := New()
			response := httptest.NewRecorder()
			api.ServeHTTP(
				response,
				httptest.NewRequest(http.MethodPost, "/api/analyze", bytes.NewBufferString(test.body)),
			)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), test.want) {
				t.Fatalf("status/body = %d/%q, want 400 containing %q", response.Code, response.Body.String(), test.want)
			}
			shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := api.Shutdown(shutdownCtx); err != nil {
				t.Fatalf("Shutdown() error = %v", err)
			}
		})
	}
}

func TestAnalysisRejectsPartialMetricsAfterError(t *testing.T) {
	api := New()
	api.samplingInterval = time.Millisecond
	var sampleNumber atomic.Int64
	api.newKubernetesClient = func() (analysisKubernetesClient, error) {
		return &fakeKubernetesClient{
			getResourceSettings: func(context.Context) (corek8s.ResourceSettings, error) {
				return corek8s.ResourceSettings{}, nil
			},
			getPodMetrics: func(context.Context) (corek8s.ContainerMetrics, error) {
				number := sampleNumber.Add(1)
				if number > 2 {
					return corek8s.ContainerMetrics{}, errors.New("metrics source unavailable")
				}
				return corek8s.ContainerMetrics{
					ContainerName: "app",
					Timestamp:     time.Unix(0, number*int64(time.Millisecond)),
					Window:        time.Millisecond,
					CPUUsage:      0.1,
					MemoryUsage:   64,
				}, nil
			},
		}, nil
	}

	initial := RunStatus{
		RunID:     "metrics-error-run",
		Status:    statusCreated,
		CreatedAt: time.Now().UTC(),
		Request: AnalyzeRequest{
			Namespace:  "default",
			Deployment: "api",
			Container:  "app",
			Duration:   "50ms",
		},
	}
	if !api.startAnalysis(initial) {
		t.Fatal("startAnalysis() = false")
	}
	run := waitForTerminalRun(t, api, initial.RunID)
	if run.Status != statusFailed || !strings.Contains(run.Error, "metrics source unavailable") {
		t.Fatalf("run = %#v, want metrics failure", run)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := api.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestAnalysisPanicBecomesFailedRun(t *testing.T) {
	api := New()
	api.analyze = func(context.Context, string, AnalyzeRequest) {
		panic("analysis exploded")
	}

	initial := RunStatus{
		RunID:     "panic-run",
		Status:    statusCreated,
		CreatedAt: time.Now().UTC(),
	}
	if !api.startAnalysis(initial) {
		t.Fatal("startAnalysis() = false")
	}

	run := waitForTerminalRun(t, api, initial.RunID)
	if run.Status != statusFailed || !strings.Contains(run.Error, "analysis panic: analysis exploded") {
		t.Fatalf("run = %#v, want recovered panic failure", run)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := api.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestLoadTestPanicBecomesFailedRun(t *testing.T) {
	api := New()
	api.samplingInterval = time.Millisecond
	api.newKubernetesClient = func() (analysisKubernetesClient, error) {
		return &fakeKubernetesClient{
			getResourceSettings: func(context.Context) (corek8s.ResourceSettings, error) {
				return corek8s.ResourceSettings{}, nil
			},
			getPodMetrics: func(context.Context) (corek8s.ContainerMetrics, error) {
				return corek8s.ContainerMetrics{
					ContainerName: "app",
					Timestamp:     time.Now(),
					Window:        time.Millisecond,
					CPUUsage:      0.1,
					MemoryUsage:   64,
				}, nil
			},
		}, nil
	}
	api.newLoadTester = func(string, int, int) analysisLoadTester {
		return fakeLoadTester(func(context.Context, time.Duration) (coreloadtest.RunResult, error) {
			panic("load test exploded")
		})
	}

	initial := RunStatus{
		RunID:     "load-panic-run",
		Status:    statusCreated,
		CreatedAt: time.Now().UTC(),
		Request: AnalyzeRequest{
			Namespace:  "default",
			Deployment: "api",
			Container:  "app",
			Duration:   time.Second.String(),
			TargetURL:  "http://example.test",
		},
	}
	if !api.startAnalysis(initial) {
		t.Fatal("startAnalysis() = false")
	}

	run := waitForTerminalRun(t, api, initial.RunID)
	if run.Status != statusFailed || !strings.Contains(run.Error, "load test panic: load test exploded") {
		t.Fatalf("run = %#v, want recovered load-test panic failure", run)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := api.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestFailedLoadTestSLOSuppressesRecommendationAndPatch(t *testing.T) {
	api := New()
	api.samplingInterval = time.Millisecond
	api.newKubernetesClient = func() (analysisKubernetesClient, error) {
		return &fakeKubernetesClient{
			getResourceSettings: func(context.Context) (corek8s.ResourceSettings, error) {
				return corek8s.ResourceSettings{}, nil
			},
			getPodMetrics: func(context.Context) (corek8s.ContainerMetrics, error) {
				return corek8s.ContainerMetrics{
					ContainerName: "app",
					Timestamp:     time.Now().UTC(),
					Window:        time.Nanosecond,
					CPUUsage:      0.1,
					MemoryUsage:   64,
				}, nil
			},
		}, nil
	}
	api.newLoadTester = func(string, int, int) analysisLoadTester {
		return fakeLoadTester(func(context.Context, time.Duration) (coreloadtest.RunResult, error) {
			time.Sleep(10 * time.Millisecond)
			return coreloadtest.RunResult{
				Requests:          100,
				HTTPErrors:        100,
				ActualRPS:         100,
				HTTPErrorRate:     1,
				P95Latency:        time.Millisecond,
				Duration:          10 * time.Millisecond,
				TerminationReason: coreloadtest.TerminationDurationElapsed,
			}, nil
		})
	}

	initial := RunStatus{
		RunID:     "failed-slo-run",
		Status:    statusCreated,
		CreatedAt: time.Now().UTC(),
		Request: AnalyzeRequest{
			Namespace:  "default",
			Deployment: "api",
			Container:  "app",
			Duration:   "30ms",
			RPS:        50,
			TargetURL:  "http://example.test",
		},
	}
	if !api.startAnalysis(initial) {
		t.Fatal("startAnalysis() = false")
	}
	run := waitForTerminalRun(t, api, initial.RunID)
	if run.Status != statusFailed || !strings.Contains(run.Error, "did not meet SLO") {
		t.Fatalf("run = %#v, want failed SLO", run)
	}
	if run.Recommendation != nil {
		t.Fatalf("recommendation = %#v, want none after failed SLO", run.Recommendation)
	}

	response := httptest.NewRecorder()
	api.ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, "/api/runs/"+initial.RunID+"/yaml-patch", nil),
	)
	if response.Code != http.StatusConflict {
		t.Fatalf("YAML patch status = %d, want %d", response.Code, http.StatusConflict)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := api.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestAnalysisUsesOnlyMetricsWindowsInsideLoadTest(t *testing.T) {
	api := New()
	api.samplingInterval = 2 * time.Millisecond
	loadStarted := make(chan struct{})
	var loadStart time.Time
	var metricCall atomic.Int64

	api.newKubernetesClient = func() (analysisKubernetesClient, error) {
		return &fakeKubernetesClient{
			getResourceSettings: func(context.Context) (corek8s.ResourceSettings, error) {
				return corek8s.ResourceSettings{}, nil
			},
			getPodMetrics: func(context.Context) (corek8s.ContainerMetrics, error) {
				<-loadStarted
				call := metricCall.Add(1)
				snapshot := corek8s.ContainerMetrics{
					ContainerName: "app",
					CPUUsage:      0.1,
					MemoryUsage:   64,
					Window:        5 * time.Millisecond,
				}
				switch call {
				case 1:
					snapshot.Timestamp = loadStart.Add(time.Millisecond)
					snapshot.Window = 10 * time.Millisecond
					snapshot.CPUUsage = 10
					snapshot.MemoryUsage = 1000
				case 2, 3, 4:
					snapshot.Timestamp = loadStart.Add(time.Duration(call-1) * 10 * time.Millisecond)
				default:
					snapshot.Timestamp = loadStart.Add(70 * time.Millisecond)
					snapshot.CPUUsage = 8
					snapshot.MemoryUsage = 800
				}
				return snapshot, nil
			},
		}, nil
	}
	api.newLoadTester = func(string, int, int) analysisLoadTester {
		return fakeLoadTester(func(context.Context, time.Duration) (coreloadtest.RunResult, error) {
			loadStart = time.Now().UTC()
			close(loadStarted)
			time.Sleep(60 * time.Millisecond)
			return coreloadtest.RunResult{
				Requests:          3000,
				ActualRPS:         50,
				P95Latency:        time.Millisecond,
				Duration:          60 * time.Millisecond,
				TerminationReason: coreloadtest.TerminationDurationElapsed,
			}, nil
		})
	}
	policy := recommender.DefaultPolicy()
	policy.CPURequestBufferPercent = 0
	policy.MemoryBufferPercent = 0
	policy.MemoryLimit.Multiplier = 1
	initial := RunStatus{
		RunID:     "window-filter-run",
		Status:    statusCreated,
		CreatedAt: time.Now().UTC(),
		Request: AnalyzeRequest{
			Namespace:  "default",
			Deployment: "api",
			Container:  "app",
			Duration:   "80ms",
			RPS:        50,
			TargetURL:  "http://example.test",
			Policy:     &policy,
		},
	}
	if !api.startAnalysis(initial) {
		t.Fatal("startAnalysis() = false")
	}
	run := waitForTerminalRun(t, api, initial.RunID)
	if run.Status != statusComplete {
		t.Fatalf("run = %#v, want completed", run)
	}
	recommendation, ok := run.Recommendation.(recommender.Recommendations)
	if !ok {
		t.Fatalf("recommendation type = %T, want recommender.Recommendations", run.Recommendation)
	}
	if recommendation.CPURequest != 0.1 || recommendation.MemoryRequest != 64 {
		t.Fatalf("recommendation = %#v, want only in-load low samples", recommendation)
	}
	if recommendation.Observed.IndependentSamples != 3 {
		t.Fatalf("independent samples = %d, want 3", recommendation.Observed.IndependentSamples)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := api.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestAnalysisCollectsFinalWindowPublishedAfterLoadTest(t *testing.T) {
	api := New()
	api.samplingInterval = 2 * time.Millisecond
	loadStarted := make(chan struct{})
	finalAvailable := make(chan struct{})
	var loadStart time.Time
	var loadEnd time.Time

	api.newKubernetesClient = func() (analysisKubernetesClient, error) {
		return &fakeKubernetesClient{
			getResourceSettings: func(context.Context) (corek8s.ResourceSettings, error) {
				return corek8s.ResourceSettings{}, nil
			},
			getPodMetrics: func(context.Context) (corek8s.ContainerMetrics, error) {
				<-loadStarted
				snapshot := corek8s.ContainerMetrics{
					ContainerName: "app",
					Timestamp:     time.Now().UTC(),
					Window:        3 * time.Millisecond,
					CPUUsage:      0.1,
					MemoryUsage:   64,
				}
				select {
				case <-finalAvailable:
					snapshot.Timestamp = loadEnd
					snapshot.CPUUsage = 1
					snapshot.MemoryUsage = 1024
				default:
				}
				return snapshot, nil
			},
		}, nil
	}
	api.newLoadTester = func(string, int, int) analysisLoadTester {
		return fakeLoadTester(func(ctx context.Context, duration time.Duration) (coreloadtest.RunResult, error) {
			loadStart = time.Now().UTC()
			close(loadStarted)
			timer := time.NewTimer(duration)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return coreloadtest.RunResult{}, ctx.Err()
			case <-timer.C:
			}
			loadEnd = time.Now().UTC()
			go func() {
				time.Sleep(2 * time.Millisecond)
				close(finalAvailable)
			}()
			return coreloadtest.RunResult{
				Requests:          2000,
				ActualRPS:         50,
				P95Latency:        time.Millisecond,
				Duration:          duration,
				TerminationReason: coreloadtest.TerminationDurationElapsed,
			}, nil
		})
	}
	policy := recommender.DefaultPolicy()
	policy.CPUPercentile = 100
	policy.CPURequestBufferPercent = 0
	policy.MemoryBufferPercent = 0
	policy.MinimumSamples = 2
	initial := RunStatus{
		RunID:     "late-final-window-run",
		Status:    statusCreated,
		CreatedAt: time.Now().UTC(),
		Request: AnalyzeRequest{
			Namespace:  "default",
			Deployment: "api",
			Container:  "app",
			Duration:   "40ms",
			RPS:        50,
			TargetURL:  "http://example.test",
			Policy:     &policy,
		},
	}
	if !api.startAnalysis(initial) {
		t.Fatal("startAnalysis() = false")
	}
	run := waitForTerminalRun(t, api, initial.RunID)
	if run.Status != statusComplete {
		t.Fatalf("run = %#v, want completed", run)
	}
	recommendation, ok := run.Recommendation.(recommender.Recommendations)
	if !ok {
		t.Fatalf("recommendation type = %T, want recommender.Recommendations", run.Recommendation)
	}
	if recommendation.CPURequest != 1 || recommendation.MemoryRequest != 1024 {
		t.Fatalf("recommendation = %#v, want delayed final high-water sample", recommendation)
	}
	if !loadEnd.After(loadStart) {
		t.Fatalf("load interval = %s..%s, want positive duration", loadStart, loadEnd)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := api.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestShutdownCancelsAndWaitsForAnalyses(t *testing.T) {
	api := New()
	started := make(chan struct{})
	stopped := make(chan struct{})
	api.analyze = func(ctx context.Context, _ string, _ AnalyzeRequest) {
		close(started)
		<-ctx.Done()
		close(stopped)
	}

	if !api.startAnalysis(RunStatus{RunID: "long-run", Status: statusCreated}) {
		t.Fatal("startAnalysis() = false")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("analysis did not start")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := api.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("Shutdown() returned before analysis goroutine stopped")
	}

	if api.startAnalysis(RunStatus{RunID: "late-run"}) {
		t.Fatal("startAnalysis() accepted work after shutdown")
	}
}

func TestServeHTTPRecoversPanic(t *testing.T) {
	api := New()
	api.mux.HandleFunc("/panic", func(http.ResponseWriter, *http.Request) {
		panic("handler exploded")
	})

	response := httptest.NewRecorder()
	api.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := api.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func waitForTerminalRun(t *testing.T, api *Server, id string) RunStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run, ok := api.runSnapshot(id)
		if ok && (run.Status == statusComplete || run.Status == statusFailed) {
			return run
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("run %q did not reach a terminal state", id)
	return RunStatus{}
}
