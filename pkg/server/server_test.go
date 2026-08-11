package server

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
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
	getPodMetrics       func(context.Context) (float64, float64, error)
	metricCalls         atomic.Int64
}

type fakeLoadTester func(context.Context, time.Duration) error

func (tester fakeLoadTester) Run(ctx context.Context, duration time.Duration) (coreloadtest.RunResult, error) {
	err := tester(ctx, duration)
	return coreloadtest.RunResult{TerminationReason: coreloadtest.TerminationDurationElapsed}, err
}

func (client *fakeKubernetesClient) ResolveWorkload(
	_ context.Context,
	_, deployment, container string,
) (corek8s.Workload, error) {
	if container == "" {
		container = "app"
	}
	return corek8s.Workload{
		DeploymentName: deployment,
		ContainerName:  container,
		PodSelector:    "app=" + deployment,
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
	workload corek8s.Workload,
) (corek8s.ContainerMetrics, error) {
	cpu, memory, err := client.getPodMetrics(ctx)
	if err != nil {
		return corek8s.ContainerMetrics{}, err
	}
	sequence := client.metricCalls.Add(1)
	return corek8s.ContainerMetrics{
		ContainerName: workload.ContainerName,
		Timestamp:     time.Unix(0, sequence).UTC(),
		Window:        time.Nanosecond,
		CPUUsage:      cpu,
		MemoryUsage:   memory,
	}, nil
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
	api.newKubernetesClient = func() (analysisKubernetesClient, error) {
		return &fakeKubernetesClient{
			getResourceSettings: func(ctx context.Context) (corek8s.ResourceSettings, error) {
				_, hasDeadline := ctx.Deadline()
				resourceContextHasDeadline.Store(hasDeadline)
				return corek8s.ResourceSettings{}, nil
			},
			getPodMetrics: func(ctx context.Context) (float64, float64, error) {
				_, hasDeadline := ctx.Deadline()
				metricsContextHasDeadline.Store(hasDeadline)
				return 0.1, 64, nil
			},
		}, nil
	}

	body := bytes.NewBufferString(`{
		"namespace":"default",
		"deployment":"api",
		"container":"app",
		"duration":"20ms",
		"policy":{
			"cpuPercentile":95,
			"cpuRequestBufferPercent":10,
			"memoryBufferPercent":20,
			"cpuLimit":{"mode":"none"},
			"memoryLimit":{"mode":"request-multiplier","multiplier":1.2},
			"minimumSamples":3
		}
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
	recommendation, ok := run.Recommendation.(recommender.Recommendations)
	if !ok {
		t.Fatalf("recommendation type = %T, want recommender.Recommendations", run.Recommendation)
	}
	if math.Abs(recommendation.CPURequest-0.11) > 1e-9 || math.Abs(recommendation.MemoryRequest-76.8) > 1e-9 {
		t.Fatalf("recommendation = %#v, want p95 CPU + buffer and memory HWM + buffer", recommendation)
	}
	if recommendation.CPULimit != 0 || math.Abs(recommendation.MemoryLimit-92.16) > 1e-9 {
		t.Fatalf("limits = %.3f/%.3f, want none/92.16", recommendation.CPULimit, recommendation.MemoryLimit)
	}
	if recommendation.Confidence.Level == "" || len(recommendation.Explanation) == 0 {
		t.Fatalf("recommendation lacks confidence/explanation: %#v", recommendation)
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
			getPodMetrics: func(context.Context) (float64, float64, error) {
				return 0.1, 64, nil
			},
		}, nil
	}
	api.newLoadTester = func(string, int, int) analysisLoadTester {
		return fakeLoadTester(func(context.Context, time.Duration) error {
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

func TestGenerateResourcePatchUsesContainerAndOmitsDisabledLimit(t *testing.T) {
	patch := generateResourcePatchYAML(
		"shop",
		"payments-api",
		"worker",
		recommender.Recommendations{
			CPURequest:    0.1,
			MemoryRequest: 128,
			MemoryLimit:   256,
		},
	)
	for _, expected := range []string{
		"  name: payments-api",
		"      - name: worker",
		"            memory: \"256Mi\"",
	} {
		if !strings.Contains(patch, expected) {
			t.Fatalf("patch does not contain %q:\n%s", expected, patch)
		}
	}
	if strings.Contains(patch, "cpu: \"0m\"") {
		t.Fatalf("patch contains disabled CPU limit:\n%s", patch)
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
