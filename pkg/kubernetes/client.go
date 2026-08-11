package kubernetes

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
)

// ResourceSettings represents the resource requests and limits
type ResourceSettings struct {
	CPURequest    float64
	CPULimit      float64
	MemoryRequest float64
	MemoryLimit   float64
}

// ContainerMetrics is one source snapshot for a container, averaged across
// the replicas selected by a Deployment. Timestamp and Window come from the
// Kubernetes Metrics API rather than from the local polling clock.
type ContainerMetrics struct {
	ContainerName string
	Timestamp     time.Time
	Window        time.Duration
	CPUUsage      float64
	MemoryUsage   float64
}

// Workload identifies a Deployment, its pods, and the container to right-size.
// PodSelector is resolved from the Deployment rather than inferred from its name.
type Workload struct {
	DeploymentName string
	ContainerName  string
	PodSelector    string
}

// Client provides methods to interact with Kubernetes
type Client struct {
	clientset     k8sclient.Interface
	metricsClient metricsclient.Interface
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
	clientset, err := k8sclient.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("error creating Kubernetes client: %v", err)
	}

	// Create metrics client
	metricsClient, err := metricsclient.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("error creating Metrics client: %v", err)
	}

	return &Client{
		clientset:     clientset,
		metricsClient: metricsClient,
	}, nil
}

// ResolveWorkload reads the Deployment selector and verifies that it identifies
// at least one pod containing the explicitly selected container.
func (c *Client) ResolveWorkload(
	ctx context.Context,
	namespace, deploymentName, containerName string,
) (Workload, error) {
	if deploymentName == "" {
		return Workload{}, fmt.Errorf("deployment name must not be empty")
	}
	if containerName == "" {
		return Workload{}, fmt.Errorf("container name must not be empty")
	}

	deployment, err := c.clientset.AppsV1().Deployments(namespace).Get(
		ctx,
		deploymentName,
		metav1.GetOptions{},
	)
	if err != nil {
		return Workload{}, fmt.Errorf("error getting deployment %q: %w", deploymentName, err)
	}

	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil {
		return Workload{}, fmt.Errorf("invalid selector on deployment %q: %w", deploymentName, err)
	}
	if deployment.Spec.Selector == nil ||
		len(deployment.Spec.Selector.MatchLabels)+len(deployment.Spec.Selector.MatchExpressions) == 0 {
		return Workload{}, fmt.Errorf("deployment %q has an empty pod selector", deploymentName)
	}

	if !hasContainer(deployment.Spec.Template.Spec.Containers, containerName) {
		return Workload{}, fmt.Errorf(
			"container %q not found in deployment %q",
			containerName,
			deploymentName,
		)
	}

	workload := Workload{
		DeploymentName: deploymentName,
		ContainerName:  containerName,
		PodSelector:    selector.String(),
	}

	pods, err := c.listWorkloadPods(ctx, namespace, workload)
	if err != nil {
		return Workload{}, err
	}
	if len(pods.Items) == 0 {
		return Workload{}, fmt.Errorf(
			"no pods found for deployment %q using selector %q",
			deploymentName,
			workload.PodSelector,
		)
	}
	for _, pod := range pods.Items {
		if !hasContainer(pod.Spec.Containers, containerName) {
			return Workload{}, fmt.Errorf(
				"container %q not found in pod %q selected by deployment %q",
				containerName,
				pod.Name,
				deploymentName,
			)
		}
	}

	return workload, nil
}

// GetResourceSettings retrieves settings for the selected container in a pod
// belonging to the resolved Deployment.
func (c *Client) GetResourceSettings(
	ctx context.Context,
	namespace string,
	workload Workload,
) (ResourceSettings, error) {
	pods, err := c.listWorkloadPods(ctx, namespace, workload)
	if err != nil {
		return ResourceSettings{}, err
	}

	if len(pods.Items) == 0 {
		return ResourceSettings{}, fmt.Errorf(
			"no pods found for deployment %q using selector %q",
			workload.DeploymentName,
			workload.PodSelector,
		)
	}

	pod := pods.Items[0]
	settings := ResourceSettings{}

	container, ok := findContainer(pod.Spec.Containers, workload.ContainerName)
	if !ok {
		return ResourceSettings{}, fmt.Errorf(
			"container %q not found in pod %q",
			workload.ContainerName,
			pod.Name,
		)
	}

	settings.CPURequest = float64(container.Resources.Requests.Cpu().MilliValue()) / 1000
	settings.CPULimit = float64(container.Resources.Limits.Cpu().MilliValue()) / 1000

	// Parse Memory request
	settings.MemoryRequest = float64(container.Resources.Requests.Memory().Value()) / (1024 * 1024)

	// Parse Memory limit
	settings.MemoryLimit = float64(container.Resources.Limits.Memory().Value()) / (1024 * 1024)

	return settings, nil
}

// GetPodMetrics retrieves current metrics for the selected container in pods
// belonging to the resolved Deployment.
func (c *Client) GetPodMetrics(
	ctx context.Context,
	namespace string,
	workload Workload,
) (ContainerMetrics, error) {
	podMetrics, err := c.metricsClient.MetricsV1beta1().PodMetricses(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: workload.PodSelector,
	})
	if err != nil {
		return ContainerMetrics{}, fmt.Errorf("error getting pod metrics: %w", err)
	}

	if len(podMetrics.Items) == 0 {
		return ContainerMetrics{}, fmt.Errorf(
			"no metrics found for deployment %q using selector %q",
			workload.DeploymentName,
			workload.PodSelector,
		)
	}

	var totalCPU float64
	var totalMemory float64
	var podCount int
	var sourceTimestamp time.Time
	var sourceWindow time.Duration

	for _, pod := range podMetrics.Items {
		if pod.Timestamp.IsZero() {
			return ContainerMetrics{}, fmt.Errorf(
				"metrics for pod %q have no source timestamp",
				pod.Name,
			)
		}
		if pod.Window.Duration <= 0 {
			return ContainerMetrics{}, fmt.Errorf(
				"metrics for pod %q have invalid source window %s",
				pod.Name,
				pod.Window.Duration,
			)
		}

		container, ok := findMetricsContainer(pod.Containers, workload.ContainerName)
		if !ok {
			return ContainerMetrics{}, fmt.Errorf(
				"metrics for container %q not found in pod %q",
				workload.ContainerName,
				pod.Name,
			)
		}

		totalCPU += float64(container.Usage.Cpu().MilliValue()) / 1000
		totalMemory += float64(container.Usage.Memory().Value()) / (1024 * 1024)
		podCount++

		// A workload snapshot is only as fresh as its oldest replica. Combined
		// with the widest source window, this creates a conservative interval
		// for deciding whether workload samples overlap.
		if sourceTimestamp.IsZero() || pod.Timestamp.Time.Before(sourceTimestamp) {
			sourceTimestamp = pod.Timestamp.Time
		}
		if pod.Window.Duration > sourceWindow {
			sourceWindow = pod.Window.Duration
		}
	}

	return ContainerMetrics{
		ContainerName: workload.ContainerName,
		Timestamp:     sourceTimestamp,
		Window:        sourceWindow,
		CPUUsage:      totalCPU / float64(podCount),
		MemoryUsage:   totalMemory / float64(podCount),
	}, nil
}

// Note: YAML patch generation functionality has been centralized in the output package
// to avoid code duplication. The generateYAMLPatch function there handles this functionality.

func (c *Client) listWorkloadPods(
	ctx context.Context,
	namespace string,
	workload Workload,
) (*corev1.PodList, error) {
	pods, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: workload.PodSelector,
	})
	if err != nil {
		return nil, fmt.Errorf(
			"error listing pods for deployment %q: %w",
			workload.DeploymentName,
			err,
		)
	}
	return pods, nil
}

func findContainer(containers []corev1.Container, name string) (corev1.Container, bool) {
	for _, container := range containers {
		if container.Name == name {
			return container, true
		}
	}
	return corev1.Container{}, false
}

func hasContainer(containers []corev1.Container, name string) bool {
	_, ok := findContainer(containers, name)
	return ok
}

func findMetricsContainer(
	containers []metricsv1beta1.ContainerMetrics,
	name string,
) (metricsv1beta1.ContainerMetrics, bool) {
	for _, container := range containers {
		if container.Name == name {
			return container, true
		}
	}
	return metricsv1beta1.ContainerMetrics{}, false
}
