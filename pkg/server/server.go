package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
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
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
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
	maximumPublicationGrace = 2 * time.Minute
	maximumRPS              = 10_000
	maximumConcurrency      = 1_000
	defaultMinimumRPSRatio  = 0.95
	defaultMaximumErrorPct  = 1.0
	defaultMaximumP95       = time.Second
	maximumAnalyzeBodyBytes = 1 << 20
)

// AnalyzeRequest represents the input payload for a new analysis run.
type AnalyzeRequest struct {
	Namespace   string              `json:"namespace"`
	Deployment  string              `json:"deployment"`
	Container   string              `json:"container"`
	ServiceName string              `json:"serviceName,omitempty"`
	Duration    string              `json:"duration"` // e.g. "2m"
	RPS         int                 `json:"rps,omitempty"`
	Concurrency int                 `json:"concurrency,omitempty"`
	Policy      *recommender.Policy `json:"policy,omitempty"`
	TargetURL   string              `json:"targetURL,omitempty"`

	MinimumActualRPS     float64  `json:"minimumActualRPS,omitempty"`
	MaximumHTTPErrorRate *float64 `json:"maximumHTTPErrorRate,omitempty"`
	MaximumP95Latency    string   `json:"maximumP95Latency,omitempty"`
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
	MetricsSamples int                     `json:"metricsSamples"`
	Recommendation any                     `json:"recommendation,omitempty"`
	Request        AnalyzeRequest          `json:"request"`
	Advice         []string                `json:"advice,omitempty"`
	Workload       corek8s.Workload        `json:"workload,omitempty"`
	LoadTest       *coreloadtest.RunResult `json:"loadTest,omitempty"`
	PatchDryRun    bool                    `json:"patchDryRun"`
	patchYAML      string
}

type analysisKubernetesClient interface {
	ResolveWorkload(
		ctx context.Context,
		namespace, deploymentName, containerName string,
	) (corek8s.Workload, error)
	GetResourceSettings(
		ctx context.Context,
		namespace string,
		workload corek8s.Workload,
	) (corek8s.ResourceSettings, error)
	GetPodMetrics(
		ctx context.Context,
		namespace string,
		workload corek8s.Workload,
	) (corek8s.ContainerMetrics, error)
	PrepareResourcePatch(
		ctx context.Context,
		namespace string,
		workload corek8s.Workload,
		desired corek8s.ResourceSettings,
	) (*corek8s.ResourcePatch, error)
}

type analysisLoadTester interface {
	Run(ctx context.Context, duration time.Duration) (coreloadtest.RunResult, error)
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

	r.Body = http.MaxBytesReader(w, r.Body, maximumAnalyzeBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var req AnalyzeRequest
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid body: %v", err), http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid body: expected one JSON object", http.StatusBadRequest)
		return
	}
	if err := validateAnalyzeRequest(req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
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
		duration+maximumPublicationGrace+analysisCleanupTimeout,
	)
	defer cancelOperation()

	k8sClient, err := s.newKubernetesClient()
	if err != nil {
		s.finishWithError(id, fmt.Errorf("k8s client: %w", err))
		return
	}
	workload, err := k8sClient.ResolveWorkload(
		operationCtx,
		req.Namespace,
		req.Deployment,
		req.Container,
	)
	if err != nil {
		s.finishWithError(id, fmt.Errorf("resolve workload: %w", err))
		return
	}

	currentSettings, err := k8sClient.GetResourceSettings(
		operationCtx,
		req.Namespace,
		workload,
	)
	if err != nil {
		s.finishWithError(id, fmt.Errorf("get resource settings: %w", err))
		return
	}

	collector := coremetrics.NewCollector(k8sClient, req.Namespace, workload)
	analysisCtx, cancelAnalysis := context.WithCancel(operationCtx)
	defer cancelAnalysis()
	measurementStartedAt := time.Now().UTC()

	type loadTestOutcome struct {
		result     coreloadtest.RunResult
		err        error
		startedAt  time.Time
		finishedAt time.Time
	}
	var loadTestDone <-chan loadTestOutcome
	if req.TargetURL != "" {
		done := make(chan loadTestOutcome, 1)
		loadTestDone = done
		tester := s.newLoadTester(req.TargetURL, req.RPS, req.Concurrency)
		go func() {
			defer func() {
				if value := recover(); value != nil {
					done <- loadTestOutcome{
						err:        fmt.Errorf("load test panic: %v", value),
						finishedAt: time.Now().UTC(),
					}
				}
			}()
			startedAt := time.Now().UTC()
			result, runErr := tester.Run(analysisCtx, duration)
			done <- loadTestOutcome{
				result:     result,
				err:        runErr,
				startedAt:  startedAt,
				finishedAt: time.Now().UTC(),
			}
		}()
	}

	var samples []coremetrics.ResourceMetrics
	var metricsErr error
	var loadTestErr error
	var loadTestResult coreloadtest.RunResult
	var loadTestStartedAt time.Time
	var loadTestFinishedAt time.Time
	loadTestFinished := loadTestDone == nil
	ticker := time.NewTicker(s.samplingInterval)
	defer ticker.Stop()
	var measurementDone <-chan time.Time
	var measurementTimer *time.Timer
	if loadTestDone == nil {
		measurementTimer = time.NewTimer(duration)
		measurementDone = measurementTimer.C
		defer measurementTimer.Stop()
	}
	var publicationDone <-chan time.Time
	var publicationTimer *time.Timer
	defer func() {
		if publicationTimer != nil {
			publicationTimer.Stop()
		}
	}()
	var measurementFinishedAt time.Time

	startPublicationGrace := func() bool {
		grace, graceErr := metricsPublicationGrace(samples, s.samplingInterval)
		if graceErr != nil {
			metricsErr = graceErr
			cancelAnalysis()
			return false
		}
		publicationTimer = time.NewTimer(grace)
		publicationDone = publicationTimer.C
		return true
	}

	for collect := true; collect; {
		sample, err := collector.CollectMetrics(analysisCtx)
		switch {
		case err == nil:
			samples = append(samples, sample)
		case analysisCtx.Err() == nil:
			metricsErr = err
			cancelAnalysis()
			collect = false
		}
		if !collect {
			continue
		}

		select {
		case outcome := <-loadTestDone:
			loadTestDone = nil
			loadTestFinished = true
			loadTestResult = outcome.result
			loadTestErr = outcome.err
			loadTestStartedAt = outcome.startedAt
			loadTestFinishedAt = outcome.finishedAt
			if loadTestErr != nil {
				cancelAnalysis()
				collect = false
				continue
			}
			measurementFinishedAt = loadTestFinishedAt
			collect = startPublicationGrace()
		case <-measurementDone:
			measurementDone = nil
			measurementFinishedAt = measurementStartedAt.Add(duration)
			collect = startPublicationGrace()
		case <-publicationDone:
			publicationDone = nil
			collect = false
		case <-analysisCtx.Done():
			collect = false
		case <-ticker.C:
		}
	}

	if !loadTestFinished {
		outcome := <-loadTestDone
		loadTestResult = outcome.result
		loadTestErr = outcome.err
		loadTestStartedAt = outcome.startedAt
		loadTestFinishedAt = outcome.finishedAt
	}

	if err := parentCtx.Err(); err != nil {
		s.finishWithError(id, fmt.Errorf("analysis canceled: %w", err))
		return
	}
	if err := operationCtx.Err(); err != nil {
		s.finishWithError(id, fmt.Errorf("analysis timeout: %w", err))
		return
	}
	if metricsErr != nil {
		s.finishWithError(id, fmt.Errorf("collect metrics: %w", metricsErr))
		return
	}
	if loadTestErr != nil {
		s.finishWithError(id, fmt.Errorf("load test: %w", loadTestErr))
		return
	}
	if req.TargetURL != "" {
		assessment, assessmentErr := loadTestResult.EvaluateSLO(loadTestSLO(req))
		if assessmentErr != nil {
			s.finishWithError(id, fmt.Errorf("invalid load-test SLO: %w", assessmentErr))
			return
		}
		if !assessment.Passed {
			s.finishWithError(
				id,
				fmt.Errorf("load test did not meet SLO: %s", strings.Join(assessment.Violations, "; ")),
			)
			return
		}
	}
	measurementIntervalStart := measurementStartedAt
	if req.TargetURL != "" {
		measurementIntervalStart = loadTestStartedAt
	}
	containedSamples, filterErr := coremetrics.FullyContainedSamples(
		samples,
		measurementIntervalStart,
		measurementFinishedAt,
	)
	if filterErr != nil {
		s.finishWithError(id, fmt.Errorf("filter metrics to measurement interval: %w", filterErr))
		return
	}
	samples = containedSamples

	independentSamples, err := coremetrics.IndependentSamples(samples)
	if err != nil {
		s.finishWithError(id, fmt.Errorf("validate metrics: %w", err))
		return
	}
	policy := recommender.DefaultPolicy()
	if req.Policy != nil {
		policy = *req.Policy
	}
	recommendation, err := recommender.GenerateRecommendations(
		independentSamples,
		currentSettings,
		policy,
	)
	if err != nil {
		s.finishWithError(id, fmt.Errorf("generate recommendation: %w", err))
		return
	}
	resourcePatch, err := k8sClient.PrepareResourcePatch(
		operationCtx,
		req.Namespace,
		workload,
		corek8s.ResourceSettings{
			CPURequest:    recommendation.CPURequest,
			CPULimit:      recommendation.CPULimit,
			MemoryRequest: recommendation.MemoryRequest,
			MemoryLimit:   recommendation.MemoryLimit,
		},
	)
	if err != nil {
		s.finishWithError(id, fmt.Errorf("server-side dry-run resource patch: %w", err))
		return
	}
	patchYAML, err := resourcePatch.YAML()
	if err != nil {
		s.finishWithError(id, fmt.Errorf("serialize resource patch: %w", err))
		return
	}
	advice := knowledge.Evaluate(independentSamples, recommendation)
	completed := time.Now().UTC()
	s.updateRun(id, func(run *RunStatus) {
		run.Status = statusComplete
		run.CompletedAt = &completed
		run.Error = ""
		run.MetricsSamples = recommendation.Observed.IndependentSamples
		run.Recommendation = recommendation
		run.Advice = advice
		run.Workload = workload
		run.PatchDryRun = true
		run.patchYAML = string(patchYAML)
		if req.TargetURL != "" {
			resultCopy := loadTestResult
			run.LoadTest = &resultCopy
		}
	})
}

func validateAnalyzeRequest(req AnalyzeRequest) error {
	for _, check := range []struct {
		name     string
		value    string
		validate func(string) []string
	}{
		{name: "namespace", value: req.Namespace, validate: k8svalidation.IsDNS1123Label},
		{name: "deployment", value: req.Deployment, validate: k8svalidation.IsDNS1123Subdomain},
		{name: "container", value: req.Container, validate: k8svalidation.IsDNS1123Label},
	} {
		if check.value == "" {
			return fmt.Errorf("%s must not be empty", check.name)
		}
		if problems := check.validate(check.value); len(problems) > 0 {
			return fmt.Errorf("invalid %s: %s", check.name, strings.Join(problems, "; "))
		}
	}

	duration, err := time.ParseDuration(req.Duration)
	if err != nil || duration <= 0 {
		return errors.New("duration must be a positive Go duration")
	}
	if duration > maximumAnalysisDuration {
		return fmt.Errorf("duration must not exceed %s", maximumAnalysisDuration)
	}
	if req.RPS < 0 || req.RPS > maximumRPS {
		return fmt.Errorf("rps must be between 0 and %d", maximumRPS)
	}
	if req.Concurrency < 0 || req.Concurrency > maximumConcurrency {
		return fmt.Errorf("concurrency must be between 0 and %d", maximumConcurrency)
	}
	if req.TargetURL != "" && req.RPS == 0 && req.Concurrency == 0 {
		return errors.New("rps or concurrency must be positive when targetURL is set")
	}
	if req.RPS > 0 && req.Concurrency > 0 {
		return errors.New("rps and concurrency are mutually exclusive")
	}
	if math.IsNaN(req.MinimumActualRPS) ||
		math.IsInf(req.MinimumActualRPS, 0) ||
		req.MinimumActualRPS < 0 {
		return errors.New("minimumActualRPS must be a finite non-negative number")
	}
	if req.MaximumHTTPErrorRate != nil &&
		(math.IsNaN(*req.MaximumHTTPErrorRate) ||
			math.IsInf(*req.MaximumHTTPErrorRate, 0) ||
			*req.MaximumHTTPErrorRate < 0 ||
			*req.MaximumHTTPErrorRate > 100) {
		return errors.New("maximumHTTPErrorRate must be between 0 and 100")
	}
	if req.MaximumP95Latency != "" {
		maximumP95, parseErr := time.ParseDuration(req.MaximumP95Latency)
		if parseErr != nil || maximumP95 <= 0 {
			return errors.New("maximumP95Latency must be a positive Go duration")
		}
	}
	if req.Policy != nil {
		if err := req.Policy.Validate(); err != nil {
			return fmt.Errorf("invalid policy: %w", err)
		}
	}
	return nil
}

func loadTestSLO(req AnalyzeRequest) coreloadtest.SLO {
	minimumRPS := req.MinimumActualRPS
	if minimumRPS == 0 && req.Concurrency == 0 {
		minimumRPS = float64(req.RPS) * defaultMinimumRPSRatio
	}
	maximumErrorPercent := defaultMaximumErrorPct
	if req.MaximumHTTPErrorRate != nil {
		maximumErrorPercent = *req.MaximumHTTPErrorRate
	}
	maximumP95 := defaultMaximumP95
	if req.MaximumP95Latency != "" {
		maximumP95, _ = time.ParseDuration(req.MaximumP95Latency)
	}
	return coreloadtest.SLO{
		MinimumRPS:           minimumRPS,
		MaximumHTTPErrorRate: maximumErrorPercent / 100,
		MaximumP95Latency:    maximumP95,
	}
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

func metricsPublicationGrace(
	samples []coremetrics.ResourceMetrics,
	samplingInterval time.Duration,
) (time.Duration, error) {
	if samplingInterval <= 0 {
		return 0, errors.New("sampling interval must be greater than zero")
	}
	resolution := coremetrics.SourceResolution(samples)
	if resolution <= 0 {
		return 0, errors.New("metrics source did not report a positive window")
	}
	if resolution > maximumPublicationGrace-samplingInterval {
		return 0, fmt.Errorf(
			"metrics source window %s plus polling interval %s exceeds maximum publication grace %s",
			resolution,
			samplingInterval,
			maximumPublicationGrace,
		)
	}
	return resolution + samplingInterval, nil
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
	if run.LoadTest != nil {
		loadTest := *run.LoadTest
		loadTest.StatusCodes = make(map[int]int, len(run.LoadTest.StatusCodes))
		for code, count := range run.LoadTest.StatusCodes {
			loadTest.StatusCodes[code] = count
		}
		run.LoadTest = &loadTest
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

	if !run.PatchDryRun || run.patchYAML == "" {
		http.Error(w, "resource patch did not pass server-side dry-run", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	_, _ = io.WriteString(w, run.patchYAML)
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
