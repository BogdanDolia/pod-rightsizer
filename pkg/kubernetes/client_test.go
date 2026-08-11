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
		nil,
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
	if metrics.CPUUsage != 0.2 {
		t.Fatalf("CPU average = %v, want 0.2", metrics.CPUUsage)
	}
	if metrics.MemoryUsage != 128 {
		t.Fatalf("memory average = %v, want 128", metrics.MemoryUsage)
	}
	if !metrics.Timestamp.Equal(testMetricsTimestamp) {
		t.Fatalf("Timestamp = %s, want source timestamp %s", metrics.Timestamp, testMetricsTimestamp)
	}
	if metrics.Window != 30*time.Second {
		t.Fatalf("Window = %s, want conservative source window 30s", metrics.Window)
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

	client := newTestClient(t, nil, []runtime.Object{podMetrics})
	_, err := client.GetPodMetrics(context.Background(), "shop", Workload{
		DeploymentName: "payments",
		ContainerName:  "worker",
		PodSelector:    "component=payments",
	})
	if err == nil || !strings.Contains(err.Error(), "no source timestamp") {
		t.Fatalf("GetPodMetrics() error = %v, want source metadata error", err)
	}
}

func TestGetPodMetricsPreservesSubMillicorePrecision(t *testing.T) {
	podMetrics := testPodMetrics(
		"payments-abc",
		map[string]string{"component": "payments"},
		"1500u",
		"64Mi",
	)
	client := newTestClient(t, nil, []runtime.Object{podMetrics})

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
		for _, object := range metricObjects {
			metric := object.(*metricsv1beta1.PodMetrics)
			if metric.Namespace == action.GetNamespace() {
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
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "shop"},
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
