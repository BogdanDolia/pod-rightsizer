package metrics

import (
	"testing"
	"time"

	"github.com/HelixThinker/pod-rightsizer/pkg/kubernetes"
	contract "github.com/HelixThinker/pod-rightsizer/pkg/providers/loadtest"
)

func TestClassifySelectedPodFiltersBeforeFold(t *testing.T) {
	start := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	end := start.Add(20 * time.Second)
	selector := PodSelector{PodUID: "pod-a", Container: "api"}
	mapping := &contract.ClockMapping{
		From:             contract.ClockRunner,
		To:               contract.ClockSource,
		Method:           "bounded_offset",
		MethodKnown:      true,
		Uncertainty:      time.Second,
		UncertaintyKnown: true,
	}
	selected := podSample("api-0", "pod-a", "pod-a", 0.2, start.Add(12*time.Second), 8*time.Second)
	overlap := podSample("api-0", "pod-a", "pod-a", 0.4, start.Add(14*time.Second), 8*time.Second)
	foreign := podSample("api-1", "pod-b", "pod-b", 9, start.Add(12*time.Second), 8*time.Second)
	foreign.CPUUsage = 9
	boundary := podSample("api-0", "pod-a", "pod-a", 8, start.Add(20*time.Second), 20*time.Second)
	runner := podSample("runner", "runner-uid", "runner-uid", 3, start.Add(12*time.Second), 8*time.Second)
	runner.Pods[0].Role = "runner"

	evidence, err := ClassifySelectedPod([]ResourceMetrics{foreign, runner, boundary, selected, overlap}, selector, start, end, mapping)
	if err != nil {
		t.Fatalf("ClassifySelectedPod() error = %v", err)
	}
	if evidence.IndependentCount != 1 {
		t.Fatalf("independent = %d, raw %#v", evidence.IndependentCount, evidence.Raw)
	}
	if evidence.Accepted[0].CPUUsage != 0.4 {
		t.Fatalf("folded cpu = %v, want the selected pod high water", evidence.Accepted[0].CPUUsage)
	}
	if evidence.Accepted[0].CPUUsage == 9 || evidence.Accepted[0].CPUUsage == 8 || evidence.Accepted[0].CPUUsage == 3 {
		t.Fatalf("out of scope cpu entered the fold: %v", evidence.Accepted[0].CPUUsage)
	}
	if !hasDiscard(evidence, DiscardForeignPod) || !hasDiscard(evidence, DiscardRunnerMetrics) || !hasDiscard(evidence, DiscardClockBoundary) {
		t.Fatalf("discards = %#v", evidence.Raw)
	}
}

func TestMixedReplicaDoesNotRaiseTheMaximum(t *testing.T) {
	start := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	end := start.Add(20 * time.Second)
	sourceEnd := start.Add(12 * time.Second)
	mixed := podSample("api-0", "pod-a", "pod-a", 0.2, sourceEnd, 8*time.Second)
	mixed.Pods = append(mixed.Pods, kubernetes.PodResourceUsage{
		Name:             "api-1",
		UID:              "pod-b",
		MetricsObjectUID: "pod-b",
		Role:             "workload",
		SourceTimestamp:  sourceEnd,
		SourceWindow:     8 * time.Second,
		CPUCores:         9,
		MemoryMi:         400,
	})
	mixed.CPUUsage = 9
	mixed.MemoryUsage = 400
	mapping := &contract.ClockMapping{
		From: contract.ClockRunner, To: contract.ClockSource, Method: "bounded_offset",
		MethodKnown: true, Uncertainty: time.Second, UncertaintyKnown: true,
	}
	evidence, err := ClassifySelectedPod([]ResourceMetrics{mixed}, PodSelector{PodUID: "pod-a", Container: "api"}, start, end, mapping)
	if err != nil {
		t.Fatalf("ClassifySelectedPod() error = %v", err)
	}
	if len(evidence.Accepted) != 1 || evidence.Accepted[0].CPUUsage != 0.2 || evidence.Accepted[0].MemoryUsage != 10 {
		t.Fatalf("accepted = %#v", evidence.Accepted)
	}
	if len(evidence.Accepted[0].Pods) != 1 || evidence.Accepted[0].Pods[0].UID != "pod-a" {
		t.Fatalf("primary pods = %#v", evidence.Accepted[0].Pods)
	}
	if len(evidence.Diagnostic) != 1 || evidence.Diagnostic[0].Reason != DiscardMixedReplicas {
		t.Fatalf("diagnostic = %#v", evidence.Diagnostic)
	}
}

func TestAggregateSampleCannotClaimSelectedPod(t *testing.T) {
	aggregate := ResourceMetrics{ContainerName: "api", CPUUsage: 1.5, MemoryUsage: 200, Timestamp: time.Now()}
	scope := ScopeFromSnapshot(aggregate)
	if scope.Kind == contract.ScopeSelectedPod || scope.Status == contract.ScopeStatusVerified || scope.Attribution != contract.AttributionAggregateOnly {
		t.Fatalf("aggregate scope = %#v", scope)
	}
	_, reason := BindSelectedPod(aggregate, PodSelector{PodUID: "pod-a", Container: "api"})
	if reason != DiscardAggregateOnly {
		t.Fatalf("bind = %s", reason)
	}
	if AllowVerifiedSelectedPod(aggregate, PodSelector{PodUID: "pod-a", Container: "api"}) {
		t.Fatal("aggregate sample was verified")
	}
}

func TestMetricsObjectUIDIsNotThePodUID(t *testing.T) {
	sample := podSample("api-0", "pod-a", "metrics-object", 0.2, time.Now(), time.Second)
	if AllowVerifiedSelectedPod(sample, PodSelector{PodUID: "pod-a", Container: "api"}) {
		t.Fatal("different metrics uid was treated as the pod uid")
	}
	_, reason := BindSelectedPod(sample, PodSelector{PodUID: "metrics-object", Container: "api"})
	if reason != DiscardMetricsUID {
		t.Fatalf("bind by metrics uid = %s", reason)
	}
	matched := podSample("api-0", "pod-a", "pod-a", 0.2, time.Now(), time.Second)
	if !AllowVerifiedSelectedPod(matched, PodSelector{PodUID: "pod-a", Container: "api"}) {
		t.Fatal("equal observed uids were rejected")
	}
}

func TestUnknownMappingDoesNotUseZeroUncertainty(t *testing.T) {
	start := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	end := start.Add(10 * time.Second)
	sample := podSample("api-0", "pod-a", "pod-a", 0.2, start.Add(5*time.Second), time.Second)
	mapping := &contract.ClockMapping{From: contract.ClockRunner, To: contract.ClockSource}
	evidence, err := ClassifySelectedPod([]ResourceMetrics{sample}, PodSelector{PodUID: "pod-a", Container: "api"}, start, end, mapping)
	if err != nil {
		t.Fatalf("ClassifySelectedPod() error = %v", err)
	}
	if evidence.IndependentCount != 0 || !hasDiscard(evidence, DiscardClockMapping) {
		t.Fatalf("unknown mapping evidence = %#v", evidence)
	}
}

func podSample(name, listedUID, metricsUID string, cpu float64, sourceEnd time.Time, window time.Duration) ResourceMetrics {
	return ResourceMetrics{
		ContainerName: "api",
		Timestamp:     sourceEnd,
		Window:        window,
		CPUUsage:      cpu,
		MemoryUsage:   10,
		Pods: []kubernetes.PodResourceUsage{{
			Name:             name,
			UID:              listedUID,
			MetricsObjectUID: metricsUID,
			Role:             "workload",
			SourceTimestamp:  sourceEnd,
			SourceWindow:     window,
			CPUCores:         cpu,
			MemoryMi:         10,
		}},
	}
}

func hasDiscard(evidence WindowEvidence, reason string) bool {
	for _, sample := range evidence.Raw {
		if sample.Reason == reason {
			return true
		}
	}
	return false
}
