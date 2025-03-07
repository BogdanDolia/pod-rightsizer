package kubernetes

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
)

// ResourceSettings represents the resource requests and limits
type ResourceSettings struct {
	CPURequest    float64
	CPULimit      float64
	MemoryRequest float64
	MemoryLimit   float64
}

// Client provides methods to interact with Kubernetes
type Client struct {
	clientset     *kubernetes.Clientset
	metricsClient *metricsv.Clientset
}

// NewClient creates a new Kubernetes client
func NewClient(kubeconfigPath string) (*Client, error) {
	var config *rest.Config
	var err error

	// Try to use in-cluster config if no kubeconfig path provided
	if kubeconfigPath == "" {
		config, err = rest.InClusterConfig()
		if err != nil {
			// Fall back to kubeconfig file if not in cluster
			if home := homedir.HomeDir(); home != "" {
				kubeconfigPath = filepath.Join(home, ".kube", "config")
			} else {
				return nil, fmt.Errorf("could not find kubeconfig file and not running in-cluster")
			}
		}
	}

	// If we need to use kubeconfig file
	if config == nil {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			return nil, fmt.Errorf("error building kubeconfig: %v", err)
		}
	}

	// Create clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("error creating Kubernetes client: %v", err)
	}

	// Create metrics client
	metricsClient, err := metricsv.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("error creating Metrics client: %v", err)
	}

	return &Client{
		clientset:     clientset,
		metricsClient: metricsClient,
	}, nil
}

// GetResourceSettings retrieves the current resource settings for pods matching the target
func (c *Client) GetResourceSettings(ctx context.Context, namespace, target string) (ResourceSettings, error) {
	// Handle different target formats (service name, deployment name, or label selector)
	selector := extractSelector(target)

	// Get pods using the selector
	pods, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return ResourceSettings{}, fmt.Errorf("error listing pods: %v", err)
	}

	if len(pods.Items) == 0 {
		return ResourceSettings{}, fmt.Errorf("no pods found matching the target: %s", target)
	}

	// Just use the first pod to get resource settings
	pod := pods.Items[0]
	settings := ResourceSettings{}

	// Find the main container
	if len(pod.Spec.Containers) == 0 {
		return ResourceSettings{}, fmt.Errorf("pod has no containers")
	}

	container := pod.Spec.Containers[0]

	// Parse CPU request
	if val, ok := container.Resources.Requests.Cpu().AsInt64(); ok {
		settings.CPURequest = float64(val) / 1000
	} else {
		settings.CPURequest = float64(container.Resources.Requests.Cpu().MilliValue()) / 1000
	}

	// Parse CPU limit
	if val, ok := container.Resources.Limits.Cpu().AsInt64(); ok {
		settings.CPULimit = float64(val) / 1000
	} else {
		settings.CPULimit = float64(container.Resources.Limits.Cpu().MilliValue()) / 1000
	}

	// Parse Memory request
	settings.MemoryRequest = float64(container.Resources.Requests.Memory().Value()) / (1024 * 1024)

	// Parse Memory limit
	settings.MemoryLimit = float64(container.Resources.Limits.Memory().Value()) / (1024 * 1024)

	return settings, nil
}

// GetPodMetrics retrieves current metrics for pods in the namespace matching the target
func (c *Client) GetPodMetrics(ctx context.Context, namespace, target string) (float64, float64, error) {
	// Handle different target formats (service name, deployment name, or label selector)
	selector := extractSelector(target)

	// Get pod metrics
	podMetrics, err := c.metricsClient.MetricsV1beta1().PodMetricses(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return 0, 0, fmt.Errorf("error getting pod metrics: %v", err)
	}

	if len(podMetrics.Items) == 0 {
		return 0, 0, fmt.Errorf("no metrics found for target: %s", target)
	}

	var totalCPU float64
	var totalMemory float64
	var podCount int

	// Sum up metrics across all pods
	for _, pod := range podMetrics.Items {
		for _, container := range pod.Containers {
			cpuQuantity := container.Usage.Cpu()
			memQuantity := container.Usage.Memory()

			// Convert CPU to cores (as float)
			cpuValue := float64(cpuQuantity.MilliValue()) / 1000

			// Convert memory to Mi
			memoryValue := float64(memQuantity.Value()) / (1024 * 1024)

			totalCPU += cpuValue
			totalMemory += memoryValue
		}
		podCount++
	}

	// Calculate averages
	avgCPU := totalCPU / float64(podCount)
	avgMemory := totalMemory / float64(podCount)

	return avgCPU, avgMemory, nil
}

// Note: YAML patch generation functionality has been centralized in the output package
// to avoid code duplication. The generateYAMLPatch function there handles this functionality.

// Helper functions

// extractSelector attempts to create a label selector from the target
func extractSelector(target string) string {
	// If target is a URL, extract the host part
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		parts := strings.Split(target, "//")
		if len(parts) > 1 {
			hostPort := strings.Split(parts[1], ":")
			target = hostPort[0]
		}
	}

	// If target already looks like a selector, return it
	if strings.Contains(target, "=") {
		return target
	}

	// Default to app=target as a common label pattern
	return fmt.Sprintf("app=%s", target)
}

// extractResourceName gets a resource name from the target
func extractResourceName(target string) string {
	// If target is a URL, extract the host part
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		parts := strings.Split(target, "//")
		if len(parts) > 1 {
			hostPort := strings.Split(parts[1], ":")
			target = hostPort[0]
		}
	}

	// If target is a label selector, use the value part
	if strings.Contains(target, "=") {
		parts := strings.Split(target, "=")
		if len(parts) > 1 {
			return parts[1]
		}
	}

	return target
}
