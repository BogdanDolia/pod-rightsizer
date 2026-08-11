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
	getResourceSettings func(context.Context) (corek8s.ResourceSettings, error)
	getPodMetrics       func(context.Context) (corek8s.ContainerMetrics, error)
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
		}, nil
	}

	body := bytes.NewBufferString(`{
		"namespace":"default",
		"deployment":"api",
		"container":"app",
		"duration":"20ms",
		"margin":20,
		"minimumSamples":2
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
	if !resourceContextHasDeadline.Load() || !metricsContextHasDeadline.Load() {
		t.Fatalf(
			"Kubernetes contexts had deadlines: resource=%t metrics=%t",
			resourceContextHasDeadline.Load(),
			metricsContextHasDeadline.Load(),
		)
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
			"duration":"20ms",
			"margin":20
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
			Namespace:      "default",
			Deployment:     "api",
			Container:      "app",
			Duration:       "50ms",
			Margin:         20,
			MinimumSamples: 2,
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

func TestGenerateResourcePatchUsesResolvedContainer(t *testing.T) {
	patch := generateResourcePatchYAML(
		"default",
		"api",
		"worker",
		recommender.Recommendations{
			CPURequest:    0.1,
			CPULimit:      0.2,
			MemoryRequest: 64,
			MemoryLimit:   128,
		},
	)
	if !strings.Contains(patch, "name: worker") {
		t.Fatalf("patch = %q, want resolved container", patch)
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
