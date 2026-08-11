package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/BogdanDolia/pod-rightsizer/pkg/knowledge"
	corek8s "github.com/BogdanDolia/pod-rightsizer/pkg/kubernetes"
	coreloadtest "github.com/BogdanDolia/pod-rightsizer/pkg/loadtest"
	coremetrics "github.com/BogdanDolia/pod-rightsizer/pkg/metrics"
	"github.com/BogdanDolia/pod-rightsizer/pkg/recommender"
	"github.com/google/uuid"
)

const (
	statusCreated  = "created"
	statusRunning  = "running"
	statusComplete = "completed"
	statusFailed   = "failed"

	defaultAnalysisDuration = 60 * time.Second
	maximumAnalysisDuration = 24 * time.Hour
	analysisCleanupTimeout  = 30 * time.Second
	defaultSamplingInterval = 5 * time.Second
)

// AnalyzeRequest represents the input payload for a new analysis run.
type AnalyzeRequest struct {
	Namespace   string `json:"namespace"`
	Deployment  string `json:"deployment"`
	ServiceName string `json:"serviceName,omitempty"`
	Duration    string `json:"duration"` // e.g. "2m"
	RPS         int    `json:"rps,omitempty"`
	Concurrency int    `json:"concurrency,omitempty"`
	Margin      int    `json:"margin"`
	TargetURL   string `json:"targetURL,omitempty"`
}

// AnalyzeResponse contains the created run identifier.
type AnalyzeResponse struct {
	RunID string `json:"runId"`
}

// RunStatus represents current status and results for a run.
type RunStatus struct {
	RunID       string     `json:"runId"`
	Status      string     `json:"status"` // created | running | completed | failed
	CreatedAt   time.Time  `json:"createdAt"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	Error       string     `json:"error,omitempty"`
	// Placeholders for future fields
	MetricsSamples int            `json:"metricsSamples"`
	Recommendation any            `json:"recommendation,omitempty"`
	Request        AnalyzeRequest `json:"request"`
	Advice         []string       `json:"advice,omitempty"`
}

type analysisKubernetesClient interface {
	GetResourceSettings(ctx context.Context, namespace, target string) (corek8s.ResourceSettings, error)
	GetPodMetrics(ctx context.Context, namespace, target string) (float64, float64, error)
}

type analysisLoadTester interface {
	Run(ctx context.Context, duration time.Duration) error
}

// Server is an HTTP handler implementing the API surface.
type Server struct {
	mux *http.ServeMux

	mu   sync.RWMutex
	runs map[string]RunStatus

	ctx    context.Context
	cancel context.CancelFunc

	lifecycleMu sync.Mutex
	stopping    bool
	runsWG      sync.WaitGroup

	newKubernetesClient func() (analysisKubernetesClient, error)
	newLoadTester       func(target string, rps, concurrency int) analysisLoadTester
	samplingInterval    time.Duration
	analyze             func(context.Context, string, AnalyzeRequest)
}

// New creates a new Server with routes registered.
func New() *Server {
	return NewWithContext(context.Background())
}

// NewWithContext creates a Server whose background analyses are canceled when
// parent is canceled.
func NewWithContext(parent context.Context) *Server {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	s := &Server{
		mux:              http.NewServeMux(),
		runs:             make(map[string]RunStatus),
		ctx:              ctx,
		cancel:           cancel,
		samplingInterval: defaultSamplingInterval,
		newKubernetesClient: func() (analysisKubernetesClient, error) {
			return corek8s.NewClient("")
		},
		newLoadTester: func(target string, rps, concurrency int) analysisLoadTester {
			return coreloadtest.NewTester(target, rps, concurrency)
		},
	}
	s.analyze = s.runAnalysis
	s.registerRoutes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if value := recover(); value != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
	}()
	s.mux.ServeHTTP(w, r)
}

// Shutdown cancels all active analyses and waits until their goroutines exit.
func (s *Server) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("shutdown context must not be nil")
	}

	s.lifecycleMu.Lock()
	s.stopping = true
	s.cancel()
	s.lifecycleMu.Unlock()

	done := make(chan struct{})
	go func() {
		s.runsWG.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) registerRoutes() {
	// API routes first
	s.mux.HandleFunc("/api/analyze", s.handleAnalyze)
	s.mux.HandleFunc("/api/runs/", s.dispatchRuns)

	// Static UI (served from local directory web/ui)
	fileServer := http.FileServer(http.Dir("web/ui"))
	s.mux.Handle("/", fileServer)
}

func (s *Server) handleAnalyze(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req AnalyzeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid body: %v", err), http.StatusBadRequest)
		return
	}

	id := uuid.NewString()
	now := time.Now().UTC()

	initial := RunStatus{
		RunID:          id,
		Status:         statusCreated,
		CreatedAt:      now,
		MetricsSamples: 0,
		Request:        req,
	}
	if !s.startAnalysis(initial) {
		http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(AnalyzeResponse{RunID: id})
}

func (s *Server) startAnalysis(initial RunStatus) bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stopping || s.ctx.Err() != nil {
		return false
	}

	s.mu.Lock()
	s.runs[initial.RunID] = initial
	s.mu.Unlock()

	s.runsWG.Add(1)
	go func() {
		defer s.runsWG.Done()
		defer func() {
			if value := recover(); value != nil {
				s.finishWithError(initial.RunID, fmt.Errorf("analysis panic: %v", value))
			}
		}()
		s.analyze(s.ctx, initial.RunID, initial.Request)
	}()
	return true
}

func (s *Server) runAnalysis(parentCtx context.Context, id string, req AnalyzeRequest) {
	s.updateRun(id, func(run *RunStatus) {
		run.Status = statusRunning
	})

	duration := analysisDuration(req.Duration)
	operationCtx, cancelOperation := context.WithTimeout(
		parentCtx,
		duration+analysisCleanupTimeout,
	)
	defer cancelOperation()

	k8sClient, err := s.newKubernetesClient()
	if err != nil {
		s.finishWithError(id, fmt.Errorf("k8s client: %w", err))
		return
	}

	currentSettings, err := k8sClient.GetResourceSettings(
		operationCtx,
		req.Namespace,
		req.Deployment,
	)
	if err != nil {
		s.finishWithError(id, fmt.Errorf("get resource settings: %w", err))
		return
	}

	collector := coremetrics.NewCollector(k8sClient, req.Namespace, req.Deployment)
	measurementCtx, stopMeasurement := context.WithTimeout(operationCtx, duration)
	defer stopMeasurement()

	var loadTestDone <-chan error
	if req.TargetURL != "" {
		done := make(chan error, 1)
		loadTestDone = done
		tester := s.newLoadTester(req.TargetURL, req.RPS, req.Concurrency)
		go func() {
			defer func() {
				if value := recover(); value != nil {
					done <- fmt.Errorf("load test panic: %v", value)
				}
			}()
			done <- tester.Run(operationCtx, duration)
		}()
	}

	var samples []coremetrics.ResourceMetrics
	var metricsErr error
	var loadTestErr error
	loadTestFinished := loadTestDone == nil
	ticker := time.NewTicker(s.samplingInterval)
	defer ticker.Stop()

	for collect := true; collect; {
		sample, err := collector.CollectMetrics(measurementCtx)
		switch {
		case err == nil:
			samples = append(samples, sample)
		case measurementCtx.Err() == nil:
			metricsErr = err
		}

		select {
		case <-measurementCtx.Done():
			collect = false
		case loadTestErr = <-loadTestDone:
			loadTestDone = nil
			loadTestFinished = true
			if loadTestErr != nil {
				stopMeasurement()
				collect = false
			}
		case <-ticker.C:
		}
	}

	if !loadTestFinished {
		loadTestErr = <-loadTestDone
	}

	if err := parentCtx.Err(); err != nil {
		s.finishWithError(id, fmt.Errorf("analysis canceled: %w", err))
		return
	}
	if err := operationCtx.Err(); err != nil {
		s.finishWithError(id, fmt.Errorf("analysis timeout: %w", err))
		return
	}
	if loadTestErr != nil {
		s.finishWithError(id, fmt.Errorf("load test: %w", loadTestErr))
		return
	}
	if len(samples) == 0 && metricsErr != nil {
		s.finishWithError(id, fmt.Errorf("collect metrics: %w", metricsErr))
		return
	}

	recommendation := recommender.GenerateRecommendations(samples, currentSettings, req.Margin)
	advice := knowledge.Evaluate(samples, recommendation)
	completed := time.Now().UTC()
	s.updateRun(id, func(run *RunStatus) {
		run.Status = statusComplete
		run.CompletedAt = &completed
		run.Error = ""
		run.MetricsSamples = len(samples)
		run.Recommendation = recommendation
		run.Advice = advice
	})
}

func analysisDuration(value string) time.Duration {
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return defaultAnalysisDuration
	}
	if duration > maximumAnalysisDuration {
		return maximumAnalysisDuration
	}
	return duration
}

func (s *Server) finishWithError(id string, err error) {
	completed := time.Now().UTC()
	s.updateRun(id, func(run *RunStatus) {
		run.Status = statusFailed
		run.CompletedAt = &completed
		run.Error = err.Error()
	})
}

func (s *Server) updateRun(id string, update func(*RunStatus)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	if !ok {
		return false
	}
	update(&run)
	s.runs[id] = run
	return true
}

func (s *Server) runSnapshot(id string) (RunStatus, bool) {
	s.mu.RLock()
	run, ok := s.runs[id]
	s.mu.RUnlock()
	if !ok {
		return RunStatus{}, false
	}

	if run.CompletedAt != nil {
		completed := *run.CompletedAt
		run.CompletedAt = &completed
	}
	run.Advice = append([]string(nil), run.Advice...)
	return run, true
}

func (s *Server) dispatchRuns(w http.ResponseWriter, r *http.Request) {
	// Expecting paths:
	// GET /api/runs/{id}
	// GET /api/runs/{id}/yaml-patch
	// GET /api/runs/{id}/hpa-behavior
	path := strings.TrimPrefix(r.URL.Path, "/api/runs/")
	if path == "" {
		http.NotFound(w, r)
		return
	}

	parts := strings.Split(path, "/")
	id := parts[0]
	tail := ""
	if len(parts) > 1 {
		tail = strings.Join(parts[1:], "/")
	}

	switch tail {
	case "":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleGetRun(w, r, id)
		return
	case "yaml-patch":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleGetYamlPatch(w, r, id)
		return
	case "hpa-behavior":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleGetHPABehavior(w, r, id)
		return
	default:
		http.NotFound(w, r)
		return
	}
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request, id string) {
	run, ok := s.runSnapshot(id)
	if !ok {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(run)
}

func (s *Server) handleGetYamlPatch(w http.ResponseWriter, r *http.Request, id string) {
	run, ok := s.runSnapshot(id)
	if !ok {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	if run.Status != statusComplete {
		http.Error(w, "run not completed yet", http.StatusConflict)
		return
	}

	// Extract recommendation
	rec, ok := run.Recommendation.(recommender.Recommendations)
	if !ok {
		// In case of JSON-marshaled type, try to re-map via json
		var tmp recommender.Recommendations
		b, _ := json.Marshal(run.Recommendation)
		_ = json.Unmarshal(b, &tmp)
		rec = tmp
	}

	ns := run.Request.Namespace
	name := run.Request.Deployment
	if name == "" {
		name = "deployment-name"
	}

	yaml := generateResourcePatchYAML(ns, name, rec)
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	_, _ = io.WriteString(w, yaml)
}

func (s *Server) handleGetHPABehavior(w http.ResponseWriter, r *http.Request, id string) {
	_, ok := s.runSnapshot(id)
	if !ok {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	// Basic default behavior; UI can customize later
	_, _ = io.WriteString(w, defaultHPABehaviorYAML())
}

func formatCPUToMilli(cores float64) int {
	// Round to nearest milli
	return int(cores*1000 + 0.5)
}

func formatMemToMi(mi float64) int {
	return int(mi + 0.5)
}

func generateResourcePatchYAML(namespace, deploy string, r recommender.Recommendations) string {
	reqCPU := formatCPUToMilli(r.CPURequest)
	limCPU := formatCPUToMilli(r.CPULimit)
	reqMem := formatMemToMi(r.MemoryRequest)
	limMem := formatMemToMi(r.MemoryLimit)

	// Container name is set to 'app' as a common default; adjust as needed by the user
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  namespace: %s
  name: %s
spec:
  template:
    spec:
      containers:
      - name: app
        resources:
          requests:
            cpu: "%dm"
            memory: "%dMi"
          limits:
            cpu: "%dm"
            memory: "%dMi"`, namespace, deploy, reqCPU, reqMem, limCPU, limMem)
}

func defaultHPABehaviorYAML() string {
	return `behavior:
  scaleDown:
    stabilizationWindowSeconds: 300
    policies:
    - type: Percent
      value: 100
      periodSeconds: 15
    selectPolicy: Max
  scaleUp:
    stabilizationWindowSeconds: 0
    policies:
    - type: Percent
      value: 100
      periodSeconds: 15
    - type: Pods
      value: 4
      periodSeconds: 15
    selectPolicy: Max`
}
