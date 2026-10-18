package k8s

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corek8s "github.com/HelixThinker/pod-rightsizer/pkg/kubernetes"
)

type failingMetricsClient struct{}

func (failingMetricsClient) ResolveWorkload(
	context.Context,
	string,
	string,
	string,
) (corek8s.Workload, error) {
	return corek8s.Workload{
		DeploymentName: "api",
		ContainerName:  "app",
		PodSelector:    "app=api",
	}, nil
}

func (failingMetricsClient) GetPodMetrics(
	context.Context,
	string,
	corek8s.Workload,
) (corek8s.ContainerMetrics, error) {
	return corek8s.ContainerMetrics{}, errors.New("replica coverage incomplete")
}

func TestCollectFailsClosedOnMetricsError(t *testing.T) {
	provider := &Provider{
		client:           failingMetricsClient{},
		samplingInterval: time.Millisecond,
	}
	collection, err := provider.Collect(
		context.Background(),
		"default",
		"api",
		"app",
		time.Millisecond,
	)
	if err == nil || !strings.Contains(err.Error(), "replica coverage incomplete") {
		t.Fatalf("Collect() error = %v, want metrics error", err)
	}
	if collection.Samples != nil || collection.Partial {
		t.Fatalf("Collect() collection = %#v, want no samples when the first poll has no facts", collection)
	}
}

func TestCollectKeepsSourceProvenance(t *testing.T) {
	sourceTime := time.Date(2026, time.August, 11, 10, 0, 0, 0, time.UTC)
	polledAt := time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)
	provider := &Provider{
		client: &scriptedMetricsClient{
			delay: 5 * time.Millisecond,
			snapshots: []corek8s.ContainerMetrics{{
				ContainerName: "app",
				Timestamp:     sourceTime,
				Window:        15 * time.Second,
				CPUUsage:      0.25,
				MemoryUsage:   128,
				PolledAt:      polledAt,
				Pods: []corek8s.PodResourceUsage{{
					Name:             "api-0",
					UID:              "22222222-2222-2222-2222-222222222222",
					MetricsObjectUID: "33333333-3333-3333-3333-333333333333",
					Role:             "workload",
					SourceTimestamp:  sourceTime,
					SourceWindow:     15 * time.Second,
					CPUCores:         0.25,
					MemoryMi:         128,
				}},
				Coverage: corek8s.Coverage{Assessed: true, Expected: 1, Observed: 1, Complete: true},
				Workload: corek8s.WorkloadObservation{
					Namespace:             "default",
					DeploymentName:        "api",
					DeploymentUID:         "11111111-1111-1111-1111-111111111111",
					DeploymentUIDObserved: true,
					ContainerName:         "app",
				},
				CPUSourcePod:    "api-0",
				MemorySourcePod: "api-0",
			}},
		},
		samplingInterval: time.Hour,
	}

	collection, err := provider.Collect(context.Background(), "default", "api", "app", time.Millisecond)
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(collection.Samples) != 1 || collection.Partial {
		t.Fatalf("collection = %#v, want one complete sample", collection)
	}
	sample := collection.Samples[0]
	if !sample.SourceTimestamp.Equal(sourceTime) || !sample.Timestamp.Equal(sourceTime) || sample.SourceWindow != 15*time.Second {
		t.Fatalf("source clock = %s window %s", sample.SourceTimestamp, sample.SourceWindow)
	}
	if !sample.PolledAt.Equal(polledAt) || sample.PolledAt.Equal(sample.SourceTimestamp) {
		t.Fatalf("PolledAt = %s, want the poll clock", sample.PolledAt)
	}
	if sample.ContainerName != "app" || sample.CPUm != 250 || sample.MemoryMi != 128 {
		t.Fatalf("sample resources = %#v", sample)
	}
	if !sample.Coverage.Complete || sample.CPUSourcePod != "api-0" || len(sample.Pods) != 1 || sample.Pods[0].UID == "" {
		t.Fatalf("provenance = coverage %#v pods %#v", sample.Coverage, sample.Pods)
	}
	if !sample.Workload.DeploymentUIDObserved || sample.Workload.DeploymentUID == "" {
		t.Fatalf("workload = %#v, want the observed UID", sample.Workload)
	}
	if sample.Pods[0].MetricsObjectUID != "33333333-3333-3333-3333-333333333333" || sample.Pods[0].UID == sample.Pods[0].MetricsObjectUID {
		t.Fatalf("pod identity = %#v", sample.Pods[0])
	}
	if sample.Scope.Kind == "selected_pod" || sample.Scope.Status == "verified" {
		t.Fatalf("aggregate collection claimed selected_pod: %#v", sample.Scope)
	}
	if sample.Scope.Attribution != "per_pod" {
		t.Fatalf("attribution = %#v, want the pod row preserved", sample.Scope)
	}
}

func TestCollectReturnsPartialSamplesAfterALaterError(t *testing.T) {
	sourceTime := time.Date(2026, time.August, 11, 10, 0, 0, 0, time.UTC)
	provider := &Provider{
		client: &scriptedMetricsClient{
			snapshots: []corek8s.ContainerMetrics{{
				ContainerName: "app",
				Timestamp:     sourceTime,
				Window:        15 * time.Second,
				CPUUsage:      0.1,
				MemoryUsage:   64,
				PolledAt:      sourceTime.Add(time.Minute),
				Pods: []corek8s.PodResourceUsage{{
					Name:            "api-0",
					SourceTimestamp: sourceTime,
					SourceWindow:    15 * time.Second,
					CPUCores:        0.1,
					MemoryMi:        64,
				}},
				Coverage: corek8s.Coverage{Assessed: true, Expected: 1, Observed: 1, Complete: true},
			}},
			errors: []error{nil, errors.New("metrics unavailable")},
		},
		samplingInterval: time.Millisecond,
	}

	collection, err := provider.Collect(context.Background(), "default", "api", "app", time.Second)
	if err == nil || !strings.Contains(err.Error(), "metrics unavailable") {
		t.Fatalf("Collect() error = %v", err)
	}
	if !collection.Partial || len(collection.Samples) != 1 || collection.Samples[0].CPUm != 100 {
		t.Fatalf("collection = %#v, want the first sample with the later error", collection)
	}
}

func TestCollectReturnsPartialSamplesOnCancellation(t *testing.T) {
	sourceTime := time.Date(2026, time.August, 11, 10, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	provider := &Provider{
		client: &scriptedMetricsClient{
			snapshots: []corek8s.ContainerMetrics{{
				ContainerName: "app",
				Timestamp:     sourceTime,
				Window:        15 * time.Second,
				CPUUsage:      0.1,
				MemoryUsage:   64,
				PolledAt:      sourceTime.Add(time.Minute),
				Pods: []corek8s.PodResourceUsage{{
					Name:            "api-0",
					SourceTimestamp: sourceTime,
					SourceWindow:    15 * time.Second,
				}},
			}},
		},
		samplingInterval: time.Hour,
	}

	collection, err := provider.Collect(ctx, "default", "api", "app", time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Collect() error = %v, want cancellation", err)
	}
	if !collection.Partial || len(collection.Samples) != 1 {
		t.Fatalf("collection = %#v, want the sample collected before cancellation", collection)
	}
	if collection.Samples[0].SourceTimestamp.Equal(collection.Samples[0].PolledAt) {
		t.Fatal("cancellation replaced the source timestamp with the poll time")
	}
}

type scriptedMetricsClient struct {
	snapshots []corek8s.ContainerMetrics
	errors    []error
	calls     int
	delay     time.Duration
}

func (c *scriptedMetricsClient) ResolveWorkload(context.Context, string, string, string) (corek8s.Workload, error) {
	return corek8s.Workload{DeploymentName: "api", ContainerName: "app", PodSelector: "app=api"}, nil
}

func (c *scriptedMetricsClient) GetPodMetrics(context.Context, string, corek8s.Workload) (corek8s.ContainerMetrics, error) {
	call := c.calls
	c.calls++
	if call == 0 && c.delay > 0 {
		time.Sleep(c.delay)
	}
	var snapshot corek8s.ContainerMetrics
	if call < len(c.snapshots) {
		snapshot = c.snapshots[call]
	}
	var err error
	if call < len(c.errors) {
		err = c.errors[call]
	}
	return snapshot, err
}
