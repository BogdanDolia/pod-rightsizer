package server

import (
	"context"
	"encoding/json"
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

// Server is an HTTP handler implementing the API surface.
type Server struct {
	mux  *http.ServeMux
	mu   sync.RWMutex
	runs map[string]*RunStatus
}

// New creates a new Server with routes registered.
func New() *Server {
	s := &Server{
		mux:  http.NewServeMux(),
		runs: make(map[string]*RunStatus),
	}
	s.registerRoutes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
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

	s.mu.Lock()
	s.runs[id] = &RunStatus{
		RunID:          id,
		Status:         "created",
		CreatedAt:      now,
		MetricsSamples: 0,
		Request:        req,
	}
	s.mu.Unlock()

	// Start background analysis orchestration
	go s.runAnalysis(id, req)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(AnalyzeResponse{RunID: id})
}

func (s *Server) runAnalysis(id string, req AnalyzeRequest) {
	s.mu.Lock()
	run := s.runs[id]
	run.Status = "running"
	s.mu.Unlock()

	// Parse duration
	dur, err := time.ParseDuration(req.Duration)
	if err != nil || dur <= 0 {
		dur = 60 * time.Second
	}

	// Prepare clients
	k8sClient, err := corek8s.NewClient("")
	if err != nil {
		s.finishWithError(id, fmt.Errorf("k8s client: %w", err))
		return
	}

	// Current resource settings (best-effort)
	currentSettings, _ := k8sClient.GetResourceSettings(context.Background(), req.Namespace, req.Deployment)

	// Metrics collector over the test window
	collector := coremetrics.NewCollector(k8sClient, req.Namespace, req.Deployment)

	ctx, cancel := context.WithTimeout(context.Background(), dur)
	defer cancel()

	// Start load test if target is provided
	var ltErr error
	doneCh := make(chan struct{})
	if req.TargetURL != "" {
		tester := coreloadtest.NewTester(req.TargetURL, req.RPS, req.Concurrency)
		go func() {
			defer close(doneCh)
			ltErr = tester.Run(ctx, dur)
		}()
	} else {
		// No load test; still close channel when done
		go func() {
			<-ctx.Done()
			close(doneCh)
		}()
	}

	// Sample metrics periodically during the window
	var samples []coremetrics.ResourceMetrics
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			goto after
		case <-ticker.C:
			m, err := collector.CollectMetrics(context.Background())
			if err == nil {
				samples = append(samples, m)
			}
		}
	}
after:
	// Wait for load test to exit
	<-doneCh

	if ltErr != nil && req.TargetURL != "" {
		// Do not fail the whole run if we still collected some metrics, just record the error
		s.mu.Lock()
		run := s.runs[id]
		run.Error = ltErr.Error()
		s.mu.Unlock()
	}

	// Generate recommendations
	rec := recommender.GenerateRecommendations(samples, currentSettings, req.Margin)
	adv := knowledge.Evaluate(samples, rec)

	// Update status
	s.mu.Lock()
	completed := time.Now().UTC()
	run = s.runs[id]
	run.Status = "completed"
	run.CompletedAt = &completed
	run.MetricsSamples = len(samples)
	run.Recommendation = rec
	run.Advice = adv
	s.mu.Unlock()
}

func (s *Server) finishWithError(id string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run, ok := s.runs[id]; ok {
		completed := time.Now().UTC()
		run.Status = "failed"
		run.CompletedAt = &completed
		run.Error = err.Error()
	}
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
	s.mu.RLock()
	run, ok := s.runs[id]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(run)
}

func (s *Server) handleGetYamlPatch(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.RLock()
	run, ok := s.runs[id]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	if run.Status != "completed" {
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
	s.mu.RLock()
	_, ok := s.runs[id]
	s.mu.RUnlock()
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
