package k8s

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corek8s "github.com/BogdanDolia/pod-rightsizer/pkg/kubernetes"
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
	samples, err := provider.Collect(
		context.Background(),
		"default",
		"api",
		"app",
		time.Millisecond,
	)
	if err == nil || !strings.Contains(err.Error(), "replica coverage incomplete") {
		t.Fatalf("Collect() error = %v, want metrics error", err)
	}
	if samples != nil {
		t.Fatalf("Collect() samples = %#v, want nil on partial collection", samples)
	}
}
