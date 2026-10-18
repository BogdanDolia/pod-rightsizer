package kubernetes

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"
)

func TestResolveWorkloadUsesDeploymentSelectorAndExplicitContainer(t *testing.T) {
	runningPod := testPod("payments-abc", map[string]string{
		"component": "payments",
		"track":     "stable",
	})
	// Simulate a stale pod during rollout; comparison must use the Deployment template.
	runningPod.Spec.Containers[1].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("900m")
	client := newTestClient(
		t,
		[]runtime.Object{
			testDeployment(),
			runningPod,
			testPod("unrelated", map[string]string{"app": "payments"}),
		},
		nil,
	)

	workload, err := client.ResolveWorkload(
		context.Background(),
		"shop",
		"payments",
		"worker",
	)
	if err != nil {
		t.Fatalf("ResolveWorkload() error = %v", err)
	}

	if workload.DeploymentName != "payments" {
		t.Fatalf("DeploymentName = %q, want payments", workload.DeploymentName)
	}
	if workload.ContainerName != "worker" {
		t.Fatalf("ContainerName = %q, want worker", workload.ContainerName)
	}
	if workload.PodSelector == "app=payments" {
		t.Fatal("PodSelector still assumes app=<deployment>")
	}

	selector, err := labels.Parse(workload.PodSelector)
	if err != nil {
		t.Fatalf("resolved selector is invalid: %v", err)
	}
	if !selector.Matches(labels.Set{"component": "payments", "track": "canary"}) {
		t.Fatalf("selector %q does not preserve Deployment match expressions", workload.PodSelector)
	}
	if selector.Matches(labels.Set{"app": "payments"}) {
		t.Fatalf("selector %q unexpectedly matches app=payments", workload.PodSelector)
	}

	settings, err := client.GetResourceSettings(context.Background(), "shop", workload)
	if err != nil {
		t.Fatalf("GetResourceSettings() error = %v", err)
	}
	if settings.CPURequest != 0.25 || settings.CPULimit != 1 {
		t.Fatalf("CPU settings = %#v, want request 0.25 and limit 1", settings)
	}
	if settings.MemoryRequest != 128 || settings.MemoryLimit != 256 {
		t.Fatalf("memory settings = %#v, want request 128 and limit 256", settings)
	}
}

func TestGetPodMetricsUsesOnlyExplicitContainer(t *testing.T) {
	stableMetrics := testPodMetrics("payments-abc", map[string]string{
		"component": "payments",
		"track":     "stable",
	}, "100m", "64Mi")
	canaryMetrics := testPodMetrics("payments-def", map[string]string{
		"component": "payments",
		"track":     "canary",
	}, "300m", "192Mi")
	canaryMetrics.Timestamp = metav1.NewTime(testMetricsTimestamp.Add(5 * time.Second))
	canaryMetrics.Window = metav1.Duration{Duration: 30 * time.Second}

	client := newTestClient(
		t,
		[]runtime.Object{
			testPod("payments-abc", map[string]string{
				"component": "payments",
				"track":     "stable",
			}),
			testPod("payments-def", map[string]string{
				"component": "payments",
				"track":     "canary",
			}),
		},
		[]runtime.Object{
			stableMetrics,
			canaryMetrics,
			testPodMetrics("unrelated", map[string]string{
				"app": "payments",
			}, "2", "2Gi"),
		},
	)

	workload := Workload{
		DeploymentName: "payments",
		ContainerName:  "worker",
		PodSelector:    "component=payments,track in (stable,canary)",
	}
	metrics, err := client.GetPodMetrics(context.Background(), "shop", workload)
	if err != nil {
		t.Fatalf("GetPodMetrics() error = %v", err)
	}
	if metrics.ContainerName != "worker" {
		t.Fatalf("ContainerName = %q, want worker", metrics.ContainerName)
	}
	if metrics.CPUUsage != 0.3 {
		t.Fatalf("CPU replica high-water = %v, want 0.3", metrics.CPUUsage)
	}
	if metrics.MemoryUsage != 192 {
		t.Fatalf("memory replica high-water = %v, want 192", metrics.MemoryUsage)
	}
	expectedTimestamp := testMetricsTimestamp.Add(5 * time.Second)
	if !metrics.Timestamp.Equal(expectedTimestamp) {
		t.Fatalf("Timestamp = %s, want source envelope end %s", metrics.Timestamp, expectedTimestamp)
	}
	if metrics.Window != 30*time.Second {
		t.Fatalf("Window = %s, want source envelope window 30s", metrics.Window)
	}
	if metrics.PolledAt.IsZero() || metrics.PolledAt.Equal(metrics.Timestamp) {
		t.Fatalf("PolledAt = %s, want a local poll time distinct from the source timestamp", metrics.PolledAt)
	}
	if metrics.CPUSourcePod != "payments-def" || metrics.MemorySourcePod != "payments-def" {
		t.Fatalf("source pods = %s/%s, want payments-def for both maxima", metrics.CPUSourcePod, metrics.MemorySourcePod)
	}
	if !metrics.Coverage.Assessed || !metrics.Coverage.Complete || metrics.Coverage.Expected != 2 || metrics.Coverage.Observed != 2 || len(metrics.Coverage.Missing) != 0 {
		t.Fatalf("coverage = %#v, want a complete 2-pod census", metrics.Coverage)
	}
	if metrics.Workload.DeploymentUIDObserved || metrics.Workload.DeploymentUID != "" {
		t.Fatalf("workload UID = %#v, want no invented UID", metrics.Workload)
	}
	if len(metrics.Pods) != 2 {
		t.Fatalf("pods = %#v, want both replica readings", metrics.Pods)
	}
	for _, pod := range metrics.Pods {
		if pod.UID != "" {
			t.Fatalf("pod %s UID = %q, want empty when the API object has none", pod.Name, pod.UID)
		}
		if pod.SourceTimestamp.IsZero() || pod.SourceWindow <= 0 {
			t.Fatalf("pod %s lost its source window: %#v", pod.Name, pod)
		}
	}
}

func TestResolveWorkloadRejectsUnstableRollout(t *testing.T) {
	deployment := testDeployment()
	deployment.Status.UpdatedReplicas = 0
	deployment.Status.UnavailableReplicas = 1
	client := newTestClient(
		t,
		[]runtime.Object{
			deployment,
			testPod("payments-abc", map[string]string{
				"component": "payments",
				"track":     "stable",
			}),
		},
		nil,
	)

	_, err := client.ResolveWorkload(context.Background(), "shop", "payments", "worker")
	if err == nil || !strings.Contains(err.Error(), "rollout is not stable") {
		t.Fatalf("ResolveWorkload() error = %v, want unstable rollout error", err)
	}
}

func TestGetResourceSettingsRejectsDeploymentGenerationChange(t *testing.T) {
	client := newTestClient(
		t,
		[]runtime.Object{
			testDeployment(),
			testPod("payments-abc", map[string]string{
				"component": "payments",
				"track":     "stable",
			}),
		},
		nil,
	)
	workload, err := client.ResolveWorkload(context.Background(), "shop", "payments", "worker")
	if err != nil {
		t.Fatalf("ResolveWorkload() error = %v", err)
	}
	changed := testDeployment()
	changed.Generation = 3
	changed.Status.ObservedGeneration = 3
	if _, err := client.clientset.AppsV1().Deployments("shop").Update(
		context.Background(),
		changed,
		metav1.UpdateOptions{},
	); err != nil {
		t.Fatalf("update deployment: %v", err)
	}

	_, err = client.GetResourceSettings(context.Background(), "shop", workload)
	if err == nil || !strings.Contains(err.Error(), "changed generation during analysis") {
		t.Fatalf("GetResourceSettings() error = %v, want generation change error", err)
	}
}

func TestConfigWithDefaultRequestTimeoutCopiesAndPreservesConfig(t *testing.T) {
	original := &rest.Config{Host: "https://cluster.example"}
	configured := configWithDefaultRequestTimeout(original)
	if configured == original {
		t.Fatal("configWithDefaultRequestTimeout() returned the caller's config")
	}
	if original.Timeout != 0 {
		t.Fatalf("original timeout = %s, want unchanged zero", original.Timeout)
	}
	if configured.Timeout != defaultRequestTimeout {
		t.Fatalf("configured timeout = %s, want %s", configured.Timeout, defaultRequestTimeout)
	}

	original.Timeout = 7 * time.Second
	configured = configWithDefaultRequestTimeout(original)
	if configured.Timeout != 7*time.Second {
		t.Fatalf("configured timeout = %s, want caller value 7s", configured.Timeout)
	}
}

func TestGetPodMetricsRejectsMissingSourceMetadata(t *testing.T) {
	podMetrics := testPodMetrics(
		"payments-abc",
		map[string]string{"component": "payments", "track": "stable"},
		"100m",
		"64Mi",
	)
	podMetrics.Timestamp = metav1.Time{}

	client := newTestClient(
		t,
		[]runtime.Object{testPod("payments-abc", map[string]string{"component": "payments"})},
		[]runtime.Object{podMetrics},
	)
	_, err := client.GetPodMetrics(context.Background(), "shop", Workload{
		DeploymentName: "payments",
		ContainerName:  "worker",
		PodSelector:    "component=payments",
	})
	if err == nil || !strings.Contains(err.Error(), "no source timestamp") {
		t.Fatalf("GetPodMetrics() error = %v, want source metadata error", err)
	}
}

func TestGetPodMetricsRejectsEmptyMetricsResponse(t *testing.T) {
	client := newTestClient(
		t,
		[]runtime.Object{testPod("payments-abc", map[string]string{"component": "payments"})},
		nil,
	)
	_, err := client.GetPodMetrics(context.Background(), "shop", Workload{
		DeploymentName: "payments",
		ContainerName:  "worker",
		PodSelector:    "component=payments",
	})
	if err == nil || !strings.Contains(err.Error(), "no metrics found") {
		t.Fatalf("GetPodMetrics() error = %v, want empty metrics error", err)
	}
}

func TestGetPodMetricsPreservesSubMillicorePrecision(t *testing.T) {
	podMetrics := testPodMetrics(
		"payments-abc",
		map[string]string{"component": "payments"},
		"1500u",
		"64Mi",
	)
	client := newTestClient(
		t,
		[]runtime.Object{testPod("payments-abc", map[string]string{"component": "payments"})},
		[]runtime.Object{podMetrics},
	)

	snapshot, err := client.GetPodMetrics(context.Background(), "shop", Workload{
		DeploymentName: "payments",
		ContainerName:  "worker",
		PodSelector:    "component=payments",
	})
	if err != nil {
		t.Fatalf("GetPodMetrics() error = %v", err)
	}
	if snapshot.CPUUsage != 0.0015 {
		t.Fatalf("CPU usage = %.6f cores, want 0.0015 without millicore rounding", snapshot.CPUUsage)
	}
}

func TestGetPodMetricsRejectsPartialReplicaCoverage(t *testing.T) {
	deployment := testDeployment()
	replicas := int32(2)
	deployment.Spec.Replicas = &replicas
	deployment.Status.Replicas = replicas
	deployment.Status.UpdatedReplicas = replicas
	deployment.Status.ReadyReplicas = replicas
	deployment.Status.AvailableReplicas = replicas

	client := newTestClient(
		t,
		[]runtime.Object{
			deployment,
			testPod("payments-abc", map[string]string{
				"component": "payments",
				"track":     "stable",
			}),
			testPod("payments-def", map[string]string{
				"component": "payments",
				"track":     "canary",
			}),
		},
		[]runtime.Object{
			testPodMetrics("payments-abc", map[string]string{
				"component": "payments",
				"track":     "stable",
			}, "100m", "64Mi"),
		},
	)
	workload, err := client.ResolveWorkload(
		context.Background(),
		"shop",
		"payments",
		"worker",
	)
	if err != nil {
		t.Fatalf("ResolveWorkload() error = %v", err)
	}

	snapshot, err := client.GetPodMetrics(context.Background(), "shop", workload)
	if err == nil || !strings.Contains(err.Error(), "metrics missing") {
		t.Fatalf("GetPodMetrics() error = %v, want missing replica metrics", err)
	}
	if snapshot.Coverage.Complete || snapshot.Coverage.Expected != 2 || snapshot.Coverage.Observed != 1 {
		t.Fatalf("coverage = %#v, want incomplete facts with the error", snapshot.Coverage)
	}
	if len(snapshot.Pods) != 1 || snapshot.Pods[0].Name != "payments-abc" || snapshot.CPUUsage != 0.1 {
		t.Fatalf("partial snapshot = %#v, want the replica that was read", snapshot.Pods)
	}
	if snapshot.Workload.DeploymentUIDObserved {
		t.Fatalf("partial snapshot invented deployment UID %q", snapshot.Workload.DeploymentUID)
	}
}

func TestGetPodMetricsKeepsObservedUIDs(t *testing.T) {
	deployment := testDeployment()
	deployment.UID = "11111111-1111-1111-1111-111111111111"
	pod := testPod("payments-abc", map[string]string{
		"component": "payments",
		"track":     "stable",
	})
	pod.UID = "22222222-2222-2222-2222-222222222222"
	client := newTestClient(
		t,
		[]runtime.Object{deployment, pod},
		[]runtime.Object{testPodMetrics("payments-abc", map[string]string{
			"component": "payments",
			"track":     "stable",
		}, "100m", "64Mi")},
	)
	workload, err := client.ResolveWorkload(context.Background(), "shop", "payments", "worker")
	if err != nil {
		t.Fatalf("ResolveWorkload() error = %v", err)
	}
	if workload.DeploymentUID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("DeploymentUID = %q, want the UID that was read", workload.DeploymentUID)
	}

	snapshot, err := client.GetPodMetrics(context.Background(), "shop", workload)
	if err != nil {
		t.Fatalf("GetPodMetrics() error = %v", err)
	}
	if !snapshot.Workload.DeploymentUIDObserved || snapshot.Workload.DeploymentUID != workload.DeploymentUID {
		t.Fatalf("workload observation = %#v, want the read deployment UID", snapshot.Workload)
	}
	if len(snapshot.Pods) != 1 || snapshot.Pods[0].UID != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("pod usage = %#v, want the read pod UID", snapshot.Pods)
	}
}

func TestGetPodMetricsRejectsGenerationChangeDuringCollection(t *testing.T) {
	client := newTestClient(
		t,
		[]runtime.Object{
			testDeployment(),
			testPod("payments-abc", map[string]string{
				"component": "payments",
				"track":     "stable",
			}),
		},
		[]runtime.Object{
			testPodMetrics("payments-abc", map[string]string{
				"component": "payments",
				"track":     "stable",
			}, "100m", "64Mi"),
		},
	)
	workload, err := client.ResolveWorkload(
		context.Background(),
		"shop",
		"payments",
		"worker",
	)
	if err != nil {
		t.Fatalf("ResolveWorkload() error = %v", err)
	}
	client.metricsClient.(*metricsfake.Clientset).PrependReactor(
		"list",
		"pods",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			changed := testDeployment()
			changed.Generation = 3
			changed.Status.ObservedGeneration = 3
			if _, updateErr := client.clientset.AppsV1().Deployments("shop").Update(
				context.Background(),
				changed,
				metav1.UpdateOptions{},
			); updateErr != nil {
				return true, nil, updateErr
			}
			return false, nil, nil
		},
	)

	_, err = client.GetPodMetrics(context.Background(), "shop", workload)
	if err == nil || !strings.Contains(err.Error(), "changed generation during analysis") {
		t.Fatalf("GetPodMetrics() error = %v, want in-flight generation change", err)
	}
}

func TestResolveWorkloadRejectsUnknownContainer(t *testing.T) {
	client := newTestClient(
		t,
		[]runtime.Object{
			testDeployment(),
			testPod("payments-abc", map[string]string{
				"component": "payments",
				"track":     "stable",
			}),
		},
		nil,
	)

	_, err := client.ResolveWorkload(context.Background(), "shop", "payments", "app")
	if err == nil || !strings.Contains(err.Error(), `container "app" not found`) {
		t.Fatalf("ResolveWorkload() error = %v, want unknown container error", err)
	}
}

func TestResolveWorkloadDoesNotFallbackToAppDeploymentLabel(t *testing.T) {
	client := newTestClient(
		t,
		[]runtime.Object{
			testDeployment(),
			testPod("unrelated", map[string]string{"app": "payments"}),
		},
		nil,
	)

	_, err := client.ResolveWorkload(context.Background(), "shop", "payments", "worker")
	if err == nil || !strings.Contains(err.Error(), "no pods found") {
		t.Fatalf("ResolveWorkload() error = %v, want no matching pods error", err)
	}
}

func newTestClient(
	t *testing.T,
	objects []runtime.Object,
	metricObjects []runtime.Object,
) *Client {
	t.Helper()
	metricsClient := metricsfake.NewSimpleClientset()
	metricsClient.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		list := &metricsv1beta1.PodMetricsList{}
		selector := action.(k8stesting.ListAction).GetListRestrictions().Labels
		for _, object := range metricObjects {
			metric := object.(*metricsv1beta1.PodMetrics)
			if metric.Namespace == action.GetNamespace() && selector.Matches(labels.Set(metric.Labels)) {
				list.Items = append(list.Items, *metric)
			}
		}
		return true, list, nil
	})
	return &Client{
		clientset:     k8sfake.NewSimpleClientset(objects...),
		metricsClient: metricsClient,
	}
}

func testDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "shop", Generation: 2},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"component": "payments"},
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      "track",
						Operator: metav1.LabelSelectorOpIn,
						Values:   []string{"stable", "canary"},
					},
				},
			},
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: testContainers()},
			},
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration:  2,
			Replicas:            1,
			UpdatedReplicas:     1,
			ReadyReplicas:       1,
			AvailableReplicas:   1,
			UnavailableReplicas: 0,
		},
	}
}

func testPod(name string, podLabels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", Labels: podLabels},
		Spec:       corev1.PodSpec{Containers: testContainers()},
	}
}

func testContainers() []corev1.Container {
	return []corev1.Container{
		{
			Name: "sidecar",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("3"),
					corev1.ResourceMemory: resource.MustParse("2Gi"),
				},
			},
		},
		{
			Name: "worker",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("250m"),
					corev1.ResourceMemory: resource.MustParse("128Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("256Mi"),
				},
			},
		},
	}
}

func testPodMetrics(
	name string,
	podLabels map[string]string,
	cpu, memory string,
) *metricsv1beta1.PodMetrics {
	return &metricsv1beta1.PodMetrics{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", Labels: podLabels},
		Timestamp:  metav1.NewTime(testMetricsTimestamp),
		Window:     metav1.Duration{Duration: 15 * time.Second},
		Containers: []metricsv1beta1.ContainerMetrics{
			{
				Name: "sidecar",
				Usage: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("900m"),
					corev1.ResourceMemory: resource.MustParse("512Mi"),
				},
			},
			{
				Name: "worker",
				Usage: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(cpu),
					corev1.ResourceMemory: resource.MustParse(memory),
				},
			},
		},
	}
}

var testMetricsTimestamp = time.Date(2026, time.August, 11, 10, 0, 0, 0, time.UTC)
