package metrics

import (
	"context"
	"time"

	"github.com/BogdanDolia/pod-rightsizer/pkg/kubernetes"
)

// ResourceMetrics represents a point-in-time metrics collection
type ResourceMetrics struct {
	Timestamp   time.Time
	CPUUsage    float64 // in cores
	MemoryUsage float64 // in Mi
}

// Collector is responsible for collecting Kubernetes pod metrics
type Collector struct {
	k8sClient *kubernetes.Client
	namespace string
	target    string
}

// NewCollector creates a new metrics collector
func NewCollector(k8sClient *kubernetes.Client, namespace, target string) *Collector {
	return &Collector{
		k8sClient: k8sClient,
		namespace: namespace,
		target:    target,
	}
}

// CollectMetrics collects a single metrics point
func (c *Collector) CollectMetrics(ctx context.Context) (ResourceMetrics, error) {
	cpu, memory, err := c.k8sClient.GetPodMetrics(ctx, c.namespace, c.target)
	if err != nil {
		return ResourceMetrics{}, err
	}

	return ResourceMetrics{
		Timestamp:   time.Now(),
		CPUUsage:    cpu,
		MemoryUsage: memory,
	}, nil
}

// CalculateAverageMetrics calculates average metrics from a collection
func CalculateAverageMetrics(metrics []ResourceMetrics) (float64, float64) {
	if len(metrics) == 0 {
		return 0, 0
	}

	var totalCPU, totalMemory float64
	for _, m := range metrics {
		totalCPU += m.CPUUsage
		totalMemory += m.MemoryUsage
	}

	return totalCPU / float64(len(metrics)), totalMemory / float64(len(metrics))
}

// CalculatePeakMetrics finds the peak CPU and memory usage
func CalculatePeakMetrics(metrics []ResourceMetrics) (float64, float64) {
	if len(metrics) == 0 {
		return 0, 0
	}

	peakCPU := metrics[0].CPUUsage
	peakMemory := metrics[0].MemoryUsage

	for _, m := range metrics {
		if m.CPUUsage > peakCPU {
			peakCPU = m.CPUUsage
		}
		if m.MemoryUsage > peakMemory {
			peakMemory = m.MemoryUsage
		}
	}

	return peakCPU, peakMemory
}
