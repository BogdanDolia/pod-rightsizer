package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/yaml"
)

const (
	resourcePatchFieldManager = "pod-rightsizer"
	bytesPerMi                = 1024 * 1024
)

// ResourcePatch is a structured Kubernetes strategic merge patch for exactly
// one container in one Deployment. It is intentionally not an apply-capable
// type: callers can serialize it, while Client.PrepareResourcePatch is the only
// network operation and always uses server-side dry-run.
type ResourcePatch struct {
	object        *unstructured.Unstructured
	containerName string
}

// NewResourcePatch builds a Kubernetes object without rendering YAML by hand.
// The selected Deployment and container must come from a resolved Workload.
func NewResourcePatch(
	namespace string,
	workload Workload,
	desired ResourceSettings,
) (*ResourcePatch, error) {
	if problems := k8svalidation.IsDNS1123Label(namespace); len(problems) > 0 {
		return nil, fmt.Errorf("invalid namespace %q: %v", namespace, problems)
	}
	if problems := k8svalidation.IsDNS1123Subdomain(workload.DeploymentName); len(problems) > 0 {
		return nil, fmt.Errorf("invalid deployment %q: %v", workload.DeploymentName, problems)
	}
	if problems := k8svalidation.IsDNS1123Label(workload.ContainerName); len(problems) > 0 {
		return nil, fmt.Errorf("invalid container %q: %v", workload.ContainerName, problems)
	}

	resources, err := desiredResourceRequirements(desired)
	if err != nil {
		return nil, err
	}
	requests := map[string]interface{}{
		string(corev1.ResourceCPU):    resources.Requests.Cpu().String(),
		string(corev1.ResourceMemory): resources.Requests.Memory().String(),
	}
	limits := map[string]interface{}{
		string(corev1.ResourceCPU):    nil,
		string(corev1.ResourceMemory): nil,
	}
	if quantity, ok := resources.Limits[corev1.ResourceCPU]; ok {
		limits[string(corev1.ResourceCPU)] = quantity.String()
	}
	if quantity, ok := resources.Limits[corev1.ResourceMemory]; ok {
		limits[string(corev1.ResourceMemory)] = quantity.String()
	}

	object := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]interface{}{
			"name":      workload.DeploymentName,
			"namespace": namespace,
		},
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"spec": map[string]interface{}{
					"containers": []interface{}{
						map[string]interface{}{
							"name": workload.ContainerName,
							"resources": map[string]interface{}{
								"requests": requests,
								"limits":   limits,
							},
						},
					},
				},
			},
		},
	}}

	return &ResourcePatch{object: object, containerName: workload.ContainerName}, nil
}

// Object returns a deep copy so callers cannot mutate a validated patch.
func (p *ResourcePatch) Object() *unstructured.Unstructured {
	if p == nil || p.object == nil {
		return nil
	}
	return p.object.DeepCopy()
}

// Namespace returns the API target encoded in the patch.
func (p *ResourcePatch) Namespace() string {
	if p == nil || p.object == nil {
		return ""
	}
	return p.object.GetNamespace()
}

// DeploymentName returns the Deployment encoded in the patch.
func (p *ResourcePatch) DeploymentName() string {
	if p == nil || p.object == nil {
		return ""
	}
	return p.object.GetName()
}

// ContainerName returns the one container encoded in the patch.
func (p *ResourcePatch) ContainerName() string {
	if p == nil {
		return ""
	}
	return p.containerName
}

// JSON serializes the structured patch for the Kubernetes Patch API.
func (p *ResourcePatch) JSON() ([]byte, error) {
	if p == nil || p.object == nil {
		return nil, fmt.Errorf("resource patch must not be nil")
	}
	return json.Marshal(p.object.Object)
}

// YAML serializes the same structured object for review and manual use.
func (p *ResourcePatch) YAML() ([]byte, error) {
	data, err := p.JSON()
	if err != nil {
		return nil, err
	}
	return yaml.JSONToYAML(data)
}

// PrepareResourcePatch rechecks the resolved Deployment, builds the patch, and
// asks the API server to validate it with dryRun=All. It never applies changes.
func (c *Client) PrepareResourcePatch(
	ctx context.Context,
	namespace string,
	workload Workload,
	desired ResourceSettings,
) (*ResourcePatch, error) {
	deployment, err := c.clientset.AppsV1().Deployments(namespace).Get(
		ctx,
		workload.DeploymentName,
		metav1.GetOptions{},
	)
	if err != nil {
		return nil, fmt.Errorf("get deployment %q before patch dry-run: %w", workload.DeploymentName, err)
	}
	if err := validateResolvedDeployment(deployment, workload); err != nil {
		return nil, err
	}
	if !hasContainer(deployment.Spec.Template.Spec.Containers, workload.ContainerName) {
		return nil, fmt.Errorf(
			"container %q not found in deployment %q before patch dry-run",
			workload.ContainerName,
			workload.DeploymentName,
		)
	}

	patch, err := NewResourcePatch(namespace, workload, desired)
	if err != nil {
		return nil, fmt.Errorf("build resource patch: %w", err)
	}
	payload, err := patch.JSON()
	if err != nil {
		return nil, fmt.Errorf("marshal resource patch: %w", err)
	}

	dryRunResult, err := c.clientset.AppsV1().Deployments(namespace).Patch(
		ctx,
		workload.DeploymentName,
		types.StrategicMergePatchType,
		payload,
		metav1.PatchOptions{
			DryRun:       []string{metav1.DryRunAll},
			FieldManager: resourcePatchFieldManager,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("server-side dry-run resource patch: %w", err)
	}
	if dryRunResult == nil {
		return nil, fmt.Errorf("server-side dry-run returned no Deployment")
	}
	container, ok := findContainer(dryRunResult.Spec.Template.Spec.Containers, workload.ContainerName)
	if !ok {
		return nil, fmt.Errorf(
			"server-side dry-run result omitted container %q",
			workload.ContainerName,
		)
	}
	expected, err := desiredResourceRequirements(desired)
	if err != nil {
		return nil, err
	}
	if !resourceRequirementsEqual(container.Resources, expected) {
		return nil, fmt.Errorf(
			"server-side dry-run returned unexpected resources for container %q",
			workload.ContainerName,
		)
	}

	return patch, nil
}

func desiredResourceRequirements(settings ResourceSettings) (corev1.ResourceRequirements, error) {
	for name, value := range map[string]float64{
		"CPU request":    settings.CPURequest,
		"CPU limit":      settings.CPULimit,
		"memory request": settings.MemoryRequest,
		"memory limit":   settings.MemoryLimit,
	} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return corev1.ResourceRequirements{}, fmt.Errorf("%s must be a finite non-negative value", name)
		}
	}
	if settings.CPURequest <= 0 || settings.MemoryRequest <= 0 {
		return corev1.ResourceRequirements{}, fmt.Errorf("CPU and memory requests must be greater than zero")
	}
	if settings.CPULimit > 0 && settings.CPULimit < settings.CPURequest {
		return corev1.ResourceRequirements{}, fmt.Errorf("CPU limit must be at least the CPU request")
	}
	if settings.MemoryLimit > 0 && settings.MemoryLimit < settings.MemoryRequest {
		return corev1.ResourceRequirements{}, fmt.Errorf("memory limit must be at least the memory request")
	}

	cpuMilli, err := roundedPositiveInt64(settings.CPURequest * 1000)
	if err != nil {
		return corev1.ResourceRequirements{}, fmt.Errorf("CPU request: %w", err)
	}
	memoryMi, err := roundedPositiveInt64(settings.MemoryRequest)
	if err != nil {
		return corev1.ResourceRequirements{}, fmt.Errorf("memory request: %w", err)
	}
	if memoryMi > math.MaxInt64/bytesPerMi {
		return corev1.ResourceRequirements{}, fmt.Errorf("memory request is outside the supported range")
	}
	resources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewMilliQuantity(cpuMilli, resource.DecimalSI),
			corev1.ResourceMemory: *resource.NewQuantity(memoryMi*bytesPerMi, resource.BinarySI),
		},
		Limits: corev1.ResourceList{},
	}
	if settings.CPULimit > 0 {
		limit, limitErr := roundedPositiveInt64(settings.CPULimit * 1000)
		if limitErr != nil {
			return corev1.ResourceRequirements{}, fmt.Errorf("CPU limit: %w", limitErr)
		}
		resources.Limits[corev1.ResourceCPU] = *resource.NewMilliQuantity(limit, resource.DecimalSI)
	}
	if settings.MemoryLimit > 0 {
		limit, limitErr := roundedPositiveInt64(settings.MemoryLimit)
		if limitErr != nil {
			return corev1.ResourceRequirements{}, fmt.Errorf("memory limit: %w", limitErr)
		}
		if limit > math.MaxInt64/bytesPerMi {
			return corev1.ResourceRequirements{}, fmt.Errorf("memory limit is outside the supported range")
		}
		resources.Limits[corev1.ResourceMemory] = *resource.NewQuantity(limit*bytesPerMi, resource.BinarySI)
	}
	return resources, nil
}

func roundedPositiveInt64(value float64) (int64, error) {
	rounded := math.Ceil(value)
	if rounded < 1 || rounded > math.MaxInt64 {
		return 0, fmt.Errorf("rounded quantity is outside the supported range")
	}
	return int64(rounded), nil
}

func resourceRequirementsEqual(actual, expected corev1.ResourceRequirements) bool {
	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		actualRequest, actualOK := actual.Requests[name]
		expectedRequest, expectedOK := expected.Requests[name]
		if actualOK != expectedOK || (actualOK && actualRequest.Cmp(expectedRequest) != 0) {
			return false
		}
		actualLimit, actualOK := actual.Limits[name]
		expectedLimit, expectedOK := expected.Limits[name]
		if actualOK != expectedOK || (actualOK && actualLimit.Cmp(expectedLimit) != 0) {
			return false
		}
	}
	return true
}
