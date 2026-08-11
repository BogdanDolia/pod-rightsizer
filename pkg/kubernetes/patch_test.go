package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sclient "k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"
)

func TestNewResourcePatchBuildsStructuredKubernetesObject(t *testing.T) {
	patch, err := NewResourcePatch(
		"shop",
		Workload{DeploymentName: "payments", ContainerName: "worker"},
		ResourceSettings{
			CPURequest:    0.1001,
			MemoryRequest: 128.1,
			MemoryLimit:   256.1,
		},
	)
	if err != nil {
		t.Fatalf("NewResourcePatch() error = %v", err)
	}
	object := patch.Object()
	if object.GetAPIVersion() != "apps/v1" || object.GetKind() != "Deployment" {
		t.Fatalf("GVK = %s %s, want apps/v1 Deployment", object.GetAPIVersion(), object.GetKind())
	}
	if object.GetNamespace() != "shop" || object.GetName() != "payments" {
		t.Fatalf("identity = %s/%s, want shop/payments", object.GetNamespace(), object.GetName())
	}
	containers, found, err := unstructuredContainers(object.Object)
	if err != nil || !found || len(containers) != 1 {
		t.Fatalf("containers = %#v, found = %v, error = %v", containers, found, err)
	}
	container := containers[0]
	if container["name"] != "worker" {
		t.Fatalf("container name = %#v, want worker", container["name"])
	}
	resources := container["resources"].(map[string]interface{})
	requests := resources["requests"].(map[string]interface{})
	limits := resources["limits"].(map[string]interface{})
	if requests["cpu"] != "101m" || requests["memory"] != "129Mi" {
		t.Fatalf("requests = %#v, want rounded 101m/129Mi", requests)
	}
	if limits["cpu"] != nil || limits["memory"] != "257Mi" {
		t.Fatalf("limits = %#v, want nil CPU and 257Mi memory", limits)
	}

	yamlData, err := patch.YAML()
	if err != nil {
		t.Fatalf("YAML() error = %v", err)
	}
	if !strings.Contains(string(yamlData), "name: worker") {
		t.Fatalf("YAML does not contain resolved container:\n%s", yamlData)
	}
}

func TestNewResourcePatchRejectsInvalidIdentityAndResources(t *testing.T) {
	validWorkload := Workload{DeploymentName: "payments", ContainerName: "worker"}
	validResources := ResourceSettings{CPURequest: 0.1, MemoryRequest: 64}
	tests := []struct {
		name      string
		namespace string
		workload  Workload
		resources ResourceSettings
	}{
		{name: "namespace injection", namespace: "shop\nmetadata:", workload: validWorkload, resources: validResources},
		{name: "invalid container", namespace: "shop", workload: Workload{DeploymentName: "payments", ContainerName: "Worker"}, resources: validResources},
		{name: "zero request", namespace: "shop", workload: validWorkload, resources: ResourceSettings{}},
		{name: "limit below request", namespace: "shop", workload: validWorkload, resources: ResourceSettings{CPURequest: 0.2, CPULimit: 0.1, MemoryRequest: 64}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewResourcePatch(test.namespace, test.workload, test.resources); err == nil {
				t.Fatal("NewResourcePatch() error = nil, want rejection")
			}
		})
	}
}

func TestPrepareResourcePatchUsesFakeClientPatchOnly(t *testing.T) {
	client := newTestClient(t, []runtime.Object{testDeployment()}, nil)
	var patchCalls atomic.Int32
	client.clientset.(*k8sfake.Clientset).PrependReactor(
		"patch",
		"deployments",
		func(action k8stesting.Action) (bool, runtime.Object, error) {
			patchCalls.Add(1)
			patchAction := action.(k8stesting.PatchAction)
			if patchAction.GetPatchType() != types.StrategicMergePatchType {
				t.Fatalf("patch type = %q, want strategic merge", patchAction.GetPatchType())
			}
			if patchAction.GetName() != "payments" || action.GetNamespace() != "shop" {
				t.Fatalf("patch target = %s/%s, want shop/payments", action.GetNamespace(), patchAction.GetName())
			}
			if !strings.Contains(string(patchAction.GetPatch()), `"name":"worker"`) {
				t.Fatalf("patch does not target worker: %s", patchAction.GetPatch())
			}
			return true, deploymentWithDesiredResources(), nil
		},
	)

	patch, err := client.PrepareResourcePatch(
		context.Background(),
		"shop",
		Workload{
			DeploymentName:       "payments",
			DeploymentGeneration: 2,
			ContainerName:        "worker",
		},
		desiredPatchResources(),
	)
	if err != nil {
		t.Fatalf("PrepareResourcePatch() error = %v", err)
	}
	if patch == nil || patchCalls.Load() != 1 {
		t.Fatalf("patch = %#v, patch calls = %d, want one", patch, patchCalls.Load())
	}
	for _, action := range client.clientset.(*k8sfake.Clientset).Actions() {
		if action.GetVerb() == "update" || action.GetVerb() == "create" {
			t.Fatalf("unexpected mutating action: %s %s", action.GetVerb(), action.GetResource().Resource)
		}
	}
}

func TestPrepareResourcePatchSendsServerSideDryRun(t *testing.T) {
	var patchCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/apps/v1/namespaces/shop/deployments/payments" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(deploymentForAPI(testDeployment()))
		case http.MethodPatch:
			patchCalls.Add(1)
			if r.URL.Query().Get("dryRun") != metav1.DryRunAll {
				http.Error(w, "dryRun=All is required", http.StatusBadRequest)
				return
			}
			if r.URL.Query().Get("fieldManager") != resourcePatchFieldManager {
				http.Error(w, "fieldManager is required", http.StatusBadRequest)
				return
			}
			if contentType := r.Header.Get("Content-Type"); !strings.Contains(contentType, "strategic-merge-patch") {
				http.Error(w, "strategic merge content type is required", http.StatusUnsupportedMediaType)
				return
			}
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"name":"worker"`) {
				http.Error(w, "worker patch is required", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(deploymentForAPI(deploymentWithDesiredResources()))
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	clientset, err := k8sclient.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatalf("NewForConfig() error = %v", err)
	}
	client := &Client{clientset: clientset, metricsClient: metricsfake.NewSimpleClientset()}
	_, err = client.PrepareResourcePatch(
		context.Background(),
		"shop",
		Workload{
			DeploymentName:       "payments",
			DeploymentGeneration: 2,
			ContainerName:        "worker",
		},
		desiredPatchResources(),
	)
	if err != nil {
		t.Fatalf("PrepareResourcePatch() error = %v", err)
	}
	if patchCalls.Load() != 1 {
		t.Fatalf("PATCH calls = %d, want 1", patchCalls.Load())
	}
}

func TestPrepareResourcePatchRejectsDryRunError(t *testing.T) {
	client := newTestClient(t, []runtime.Object{testDeployment()}, nil)
	client.clientset.(*k8sfake.Clientset).PrependReactor(
		"patch",
		"deployments",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, fmt.Errorf("admission denied")
		},
	)
	_, err := client.PrepareResourcePatch(
		context.Background(),
		"shop",
		Workload{
			DeploymentName:       "payments",
			DeploymentGeneration: 2,
			ContainerName:        "worker",
		},
		desiredPatchResources(),
	)
	if err == nil || !strings.Contains(err.Error(), "server-side dry-run") {
		t.Fatalf("PrepareResourcePatch() error = %v, want dry-run error", err)
	}
}

func desiredPatchResources() ResourceSettings {
	return ResourceSettings{
		CPURequest:    0.125,
		MemoryRequest: 192,
		MemoryLimit:   384,
	}
}

func deploymentWithDesiredResources() *appsv1.Deployment {
	deployment := testDeployment()
	for index := range deployment.Spec.Template.Spec.Containers {
		if deployment.Spec.Template.Spec.Containers[index].Name != "worker" {
			continue
		}
		deployment.Spec.Template.Spec.Containers[index].Resources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("125m"),
				corev1.ResourceMemory: resource.MustParse("192Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("384Mi"),
			},
		}
	}
	return deployment
}

func deploymentForAPI(deployment *appsv1.Deployment) *appsv1.Deployment {
	copy := deployment.DeepCopy()
	copy.TypeMeta = metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}
	return copy
}

func unstructuredContainers(object map[string]interface{}) ([]map[string]interface{}, bool, error) {
	items, found, err := unstructured.NestedSlice(object, "spec", "template", "spec", "containers")
	if err != nil || !found {
		return nil, found, err
	}
	containers := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		container, ok := item.(map[string]interface{})
		if !ok {
			return nil, true, fmt.Errorf("container has type %T", item)
		}
		containers = append(containers, container)
	}
	return containers, true, nil
}
