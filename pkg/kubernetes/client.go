package kubernetes

import (
	"context"
	"fmt"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
)

const defaultRequestTimeout = 30 * time.Second

// ResourceSettings represents the resource requests and limits
type ResourceSettings struct {
	CPURequest    float64
	CPULimit      float64
	MemoryRequest float64
	MemoryLimit   float64
}

// ContainerMetrics is one conservative source snapshot for a container. CPU
// and memory are the per-resource maxima across the replicas selected by a
// Deployment. Those maxima may come from different pods. Timestamp and Window
// envelope every replica's Metrics API source interval. PolledAt is the local
// poll clock and is not a source timestamp.
type ContainerMetrics struct {
	ContainerName   string
	Timestamp       time.Time
	Window          time.Duration
	CPUUsage        float64
	MemoryUsage     float64
	PolledAt        time.Time
	Pods            []PodResourceUsage
	Coverage        Coverage
	Workload        WorkloadObservation
	CPUSourcePod    string
	MemorySourcePod string
}

// PodResourceUsage is one replica's Metrics API reading for the selected container.
// UID is empty when the API object did not carry one.
type PodResourceUsage struct {
	Name string
	UID  string
	// MetricsObjectUID is the PodMetrics object UID when that object carried
	// one. It is not assumed to equal UID. Empty means it was not present.
	MetricsObjectUID string
	// Role is workload, runner, or sidecar. Empty means a workload row.
	Role            string
	SourceTimestamp time.Time
	SourceWindow    time.Duration
	CPUCores        float64
	MemoryMi        float64
}

// Coverage is the pod census for one poll. Complete is true only when the
// census was assessed and every expected pod returned metrics. A zero Coverage
// means the census was not assessed. Missing UIDs stay empty.
type Coverage struct {
	Assessed bool
	Expected int
	Observed int
	Missing  []string
	Complete bool
}

// WorkloadObservation records identifiers that were actually read. Empty UID
// fields are absent facts, not verified matches.
type WorkloadObservation struct {
	Namespace             string
	DeploymentName        string
	DeploymentUID         string
	DeploymentUIDObserved bool
	Generation            int64
	GenerationObserved    bool
	ContainerName         string
	PodSelector           string
}

// Workload identifies a Deployment, its pods, and the container to right-size.
// PodSelector is resolved from the Deployment rather than inferred from its name.
// DeploymentUID is set only when the object that was read has a UID.
type Workload struct {
	DeploymentName       string
	DeploymentUID        string
	DeploymentGeneration int64
	ContainerName        string
	PodSelector          string
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

	// Explicit paths take precedence. Otherwise keep in-cluster discovery first.
	if kubeconfigPath == "" {
		config, err = rest.InClusterConfig()
	}

	// Use client-go's standard file loading rules: KUBECONFIG (including a
	// path list), or ~/.kube/config when the environment variable is unset.
	if config == nil {
		if kubeconfigPath != "" {
			config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		} else {
			config, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
				clientcmd.NewDefaultClientConfigLoadingRules(),
				&clientcmd.ConfigOverrides{},
			).ClientConfig()
		}
		if err != nil {
			return nil, fmt.Errorf("error building kubeconfig: %v", err)
		}
	}
	config = configWithDefaultRequestTimeout(config)

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
	if err := validateStableDeployment(deployment); err != nil {
		return Workload{}, err
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
		DeploymentName:       deploymentName,
		DeploymentGeneration: deployment.Generation,
		ContainerName:        containerName,
		PodSelector:          selector.String(),
	}
	if deployment.UID != "" {
		workload.DeploymentUID = string(deployment.UID)
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

// GetResourceSettings retrieves the desired settings from the Deployment pod
// template. Reading the controller spec avoids comparing against a stale pod
// during a rollout.
func (c *Client) GetResourceSettings(
	ctx context.Context,
	namespace string,
	workload Workload,
) (ResourceSettings, error) {
	deployment, err := c.clientset.AppsV1().Deployments(namespace).Get(
		ctx,
		workload.DeploymentName,
		metav1.GetOptions{},
	)
	if err != nil {
		return ResourceSettings{}, fmt.Errorf(
			"error getting deployment %q: %w",
			workload.DeploymentName,
			err,
		)
	}
	if err := validateResolvedDeployment(deployment, workload); err != nil {
		return ResourceSettings{}, err
	}

	settings := ResourceSettings{}
	container, ok := findContainer(deployment.Spec.Template.Spec.Containers, workload.ContainerName)
	if !ok {
		return ResourceSettings{}, fmt.Errorf(
			"container %q not found in deployment %q",
			workload.ContainerName,
			workload.DeploymentName,
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
	var resolvedDeployment *appsv1.Deployment
	if workload.DeploymentGeneration > 0 {
		deployment, err := c.clientset.AppsV1().Deployments(namespace).Get(
			ctx,
			workload.DeploymentName,
			metav1.GetOptions{},
		)
		if err != nil {
			return ContainerMetrics{}, fmt.Errorf(
				"error rechecking deployment %q: %w",
				workload.DeploymentName,
				err,
			)
		}
		if err := validateResolvedDeployment(deployment, workload); err != nil {
			return ContainerMetrics{}, err
		}
		resolvedDeployment = deployment
	}

	pods, err := c.listWorkloadPods(ctx, namespace, workload)
	if err != nil {
		return ContainerMetrics{}, err
	}
	expectedPods, err := expectedMetricPods(pods.Items, workload)
	if err != nil {
		return ContainerMetrics{}, err
	}
	if resolvedDeployment != nil {
		desiredReplicas := desiredDeploymentReplicas(resolvedDeployment)
		if len(expectedPods) != int(desiredReplicas) {
			return ContainerMetrics{}, fmt.Errorf(
				"deployment %q pod set changed during analysis: found %d active selected pods, want %d",
				workload.DeploymentName,
				len(expectedPods),
				desiredReplicas,
			)
		}
	}

	polledAt := time.Now().UTC()
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

	var maximumCPU float64
	var maximumMemory float64
	var cpuSourcePod string
	var memorySourcePod string
	var earliestSourceStart time.Time
	var latestSourceEnd time.Time
	usages := make([]PodResourceUsage, 0, len(podMetrics.Items))
	seenPods := make(map[string]struct{}, len(podMetrics.Items))
	partial := func(cause error) (ContainerMetrics, error) {
		snapshot := assembleContainerMetrics(
			namespace,
			workload,
			resolvedDeployment,
			polledAt,
			usages,
			expectedPods,
			seenPods,
			maximumCPU,
			maximumMemory,
			cpuSourcePod,
			memorySourcePod,
			earliestSourceStart,
			latestSourceEnd,
			nil,
			false,
		)
		if !containerMetricsHasFacts(snapshot) {
			return ContainerMetrics{}, cause
		}
		return snapshot, cause
	}

	for _, pod := range podMetrics.Items {
		if _, ok := expectedPods[pod.Name]; !ok {
			return partial(fmt.Errorf(
				"metrics returned unexpected pod %q for deployment %q",
				pod.Name,
				workload.DeploymentName,
			))
		}
		if _, duplicate := seenPods[pod.Name]; duplicate {
			return partial(fmt.Errorf("metrics returned pod %q more than once", pod.Name))
		}
		seenPods[pod.Name] = struct{}{}
		if pod.Timestamp.IsZero() {
			return partial(fmt.Errorf(
				"metrics for pod %q have no source timestamp",
				pod.Name,
			))
		}
		if pod.Window.Duration <= 0 {
			return partial(fmt.Errorf(
				"metrics for pod %q have invalid source window %s",
				pod.Name,
				pod.Window.Duration,
			))
		}

		container, ok := findMetricsContainer(pod.Containers, workload.ContainerName)
		if !ok {
			return partial(fmt.Errorf(
				"metrics for container %q not found in pod %q",
				workload.ContainerName,
				pod.Name,
			))
		}

		// Requests and limits are applied to each replica, so averaging can hide a
		// hot pod and produce an unsafe recommendation. Keep the maximum observed
		// usage for each resource instead. The two maxima may come from different pods.
		cpuUsage := container.Usage.Cpu().AsApproximateFloat64()
		memoryUsage := float64(container.Usage.Memory().Value()) / (1024 * 1024)
		if cpuUsage < 0 || memoryUsage < 0 {
			return partial(fmt.Errorf(
				"metrics for container %q in pod %q contain negative usage",
				workload.ContainerName,
				pod.Name,
			))
		}
		if len(usages) == 0 || cpuUsage > maximumCPU {
			maximumCPU = cpuUsage
			cpuSourcePod = pod.Name
		}
		if len(usages) == 0 || memoryUsage > maximumMemory {
			maximumMemory = memoryUsage
			memorySourcePod = pod.Name
		}

		sourceStart := pod.Timestamp.Time.Add(-pod.Window.Duration)
		if earliestSourceStart.IsZero() || sourceStart.Before(earliestSourceStart) {
			earliestSourceStart = sourceStart
		}
		if latestSourceEnd.IsZero() || pod.Timestamp.Time.After(latestSourceEnd) {
			latestSourceEnd = pod.Timestamp.Time
		}
		usages = append(usages, PodResourceUsage{
			Name:             pod.Name,
			UID:              observedPodUID(expectedPods[pod.Name], string(pod.UID)),
			MetricsObjectUID: string(pod.UID),
			Role:             "workload",
			SourceTimestamp:  pod.Timestamp.Time,
			SourceWindow:     pod.Window.Duration,
			CPUCores:         cpuUsage,
			MemoryMi:         memoryUsage,
		})
	}

	missingPods := make([]string, 0)
	for podName := range expectedPods {
		if _, ok := seenPods[podName]; !ok {
			missingPods = append(missingPods, podName)
		}
	}
	sort.Strings(missingPods)
	var missing []string
	if len(missingPods) > 0 {
		missing = missingPods
	}
	snapshot := assembleContainerMetrics(
		namespace,
		workload,
		resolvedDeployment,
		polledAt,
		usages,
		expectedPods,
		seenPods,
		maximumCPU,
		maximumMemory,
		cpuSourcePod,
		memorySourcePod,
		earliestSourceStart,
		latestSourceEnd,
		missing,
		len(missingPods) == 0,
	)
	if len(missingPods) > 0 {
		return snapshot, fmt.Errorf(
			"metrics missing for deployment %q pods %q",
			workload.DeploymentName,
			missingPods,
		)
	}
	if workload.DeploymentGeneration > 0 {
		deployment, err := c.clientset.AppsV1().Deployments(namespace).Get(
			ctx,
			workload.DeploymentName,
			metav1.GetOptions{},
		)
		if err != nil {
			snapshot.Coverage.Complete = false
			return snapshot, fmt.Errorf(
				"error rechecking deployment %q after metrics collection: %w",
				workload.DeploymentName,
				err,
			)
		}
		if err := validateResolvedDeployment(deployment, workload); err != nil {
			snapshot.Coverage.Complete = false
			snapshot.Workload = observeWorkload(namespace, workload, deployment)
			return snapshot, err
		}
		snapshot.Workload = observeWorkload(namespace, workload, deployment)
	}

	return snapshot, nil
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

func configWithDefaultRequestTimeout(config *rest.Config) *rest.Config {
	configCopy := rest.CopyConfig(config)
	if configCopy.Timeout <= 0 {
		configCopy.Timeout = defaultRequestTimeout
	}
	return configCopy
}

func validateStableDeployment(deployment *appsv1.Deployment) error {
	desiredReplicas := desiredDeploymentReplicas(deployment)
	status := deployment.Status
	if status.ObservedGeneration < deployment.Generation ||
		status.Replicas != desiredReplicas ||
		status.UpdatedReplicas != desiredReplicas ||
		status.ReadyReplicas != desiredReplicas ||
		status.AvailableReplicas != desiredReplicas ||
		status.UnavailableReplicas != 0 {
		return fmt.Errorf(
			"deployment %q rollout is not stable: desired=%d replicas=%d updated=%d ready=%d available=%d unavailable=%d observedGeneration=%d generation=%d",
			deployment.Name,
			desiredReplicas,
			status.Replicas,
			status.UpdatedReplicas,
			status.ReadyReplicas,
			status.AvailableReplicas,
			status.UnavailableReplicas,
			status.ObservedGeneration,
			deployment.Generation,
		)
	}
	return nil
}

func desiredDeploymentReplicas(deployment *appsv1.Deployment) int32 {
	if deployment.Spec.Replicas == nil {
		return 1
	}
	return *deployment.Spec.Replicas
}

func assembleContainerMetrics(
	namespace string,
	workload Workload,
	deployment *appsv1.Deployment,
	polledAt time.Time,
	usages []PodResourceUsage,
	expected map[string]string,
	seen map[string]struct{},
	maximumCPU, maximumMemory float64,
	cpuSourcePod, memorySourcePod string,
	earliestSourceStart, latestSourceEnd time.Time,
	missing []string,
	complete bool,
) ContainerMetrics {
	ordered := append([]PodResourceUsage(nil), usages...)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].Name < ordered[j].Name
	})
	var timestamp time.Time
	var window time.Duration
	if !latestSourceEnd.IsZero() && !earliestSourceStart.IsZero() {
		timestamp = latestSourceEnd
		window = latestSourceEnd.Sub(earliestSourceStart)
	}
	return ContainerMetrics{
		ContainerName: workload.ContainerName,
		Timestamp:     timestamp,
		Window:        window,
		CPUUsage:      maximumCPU,
		MemoryUsage:   maximumMemory,
		PolledAt:      polledAt,
		Pods:          ordered,
		Coverage: Coverage{
			Assessed: len(expected) > 0,
			Expected: len(expected),
			Observed: len(seen),
			Missing:  missing,
			Complete: complete && len(missing) == 0,
		},
		Workload:        observeWorkload(namespace, workload, deployment),
		CPUSourcePod:    cpuSourcePod,
		MemorySourcePod: memorySourcePod,
	}
}

func containerMetricsHasFacts(metrics ContainerMetrics) bool {
	return len(metrics.Pods) > 0 || !metrics.Timestamp.IsZero()
}

func observedPodUID(listedUID, metricsUID string) string {
	if listedUID != "" {
		return listedUID
	}
	return metricsUID
}

func observeWorkload(
	namespace string,
	workload Workload,
	deployment *appsv1.Deployment,
) WorkloadObservation {
	observation := WorkloadObservation{
		Namespace:          namespace,
		DeploymentName:     workload.DeploymentName,
		Generation:         workload.DeploymentGeneration,
		GenerationObserved: workload.DeploymentGeneration > 0,
		ContainerName:      workload.ContainerName,
		PodSelector:        workload.PodSelector,
	}
	if workload.DeploymentUID != "" {
		observation.DeploymentUID = workload.DeploymentUID
		observation.DeploymentUIDObserved = true
	}
	if deployment == nil {
		return observation
	}
	observation.Generation = deployment.Generation
	observation.GenerationObserved = true
	if deployment.UID != "" {
		observation.DeploymentUID = string(deployment.UID)
		observation.DeploymentUIDObserved = true
	}
	return observation
}

func expectedMetricPods(
	pods []corev1.Pod,
	workload Workload,
) (map[string]string, error) {
	expected := make(map[string]string, len(pods))
	for _, pod := range pods {
		if pod.DeletionTimestamp != nil {
			continue
		}
		if !hasContainer(pod.Spec.Containers, workload.ContainerName) {
			return nil, fmt.Errorf(
				"container %q not found in active pod %q selected by deployment %q",
				workload.ContainerName,
				pod.Name,
				workload.DeploymentName,
			)
		}
		expected[pod.Name] = string(pod.UID)
	}
	if len(expected) == 0 {
		return nil, fmt.Errorf(
			"no active pods found for deployment %q using selector %q",
			workload.DeploymentName,
			workload.PodSelector,
		)
	}
	return expected, nil
}

func validateResolvedDeployment(deployment *appsv1.Deployment, workload Workload) error {
	if err := validateStableDeployment(deployment); err != nil {
		return err
	}
	if workload.DeploymentGeneration > 0 &&
		deployment.Generation != workload.DeploymentGeneration {
		return fmt.Errorf(
			"deployment %q changed generation during analysis: started at %d, now %d",
			deployment.Name,
			workload.DeploymentGeneration,
			deployment.Generation,
		)
	}
	return nil
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
