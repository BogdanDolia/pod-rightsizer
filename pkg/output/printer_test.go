package output

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/BogdanDolia/pod-rightsizer/pkg/kubernetes"
	"github.com/BogdanDolia/pod-rightsizer/pkg/recommender"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

func TestGenerateYAMLPatchUsesResolvedDeploymentAndContainer(t *testing.T) {
	patch, err := generateYAMLPatch(Result{
		Namespace: "shop",
		Workload: kubernetes.Workload{
			DeploymentName: "payments-api",
			ContainerName:  "worker",
		},
		Recommendations: recommender.Recommendations{
			CPURequest:    0.1,
			CPULimit:      0.2,
			MemoryRequest: 128,
			MemoryLimit:   256,
		},
	})
	if err != nil {
		t.Fatalf("generateYAMLPatch() error = %v", err)
	}

	for _, expected := range []string{
		"  name: payments-api\n",
		"      - name: worker\n",
	} {
		if !strings.Contains(patch, expected) {
			t.Fatalf("patch does not contain %q:\n%s", expected, patch)
		}
	}
	if strings.Contains(patch, "name: app") {
		t.Fatalf("patch still assumes container app:\n%s", patch)
	}
}

func TestGenerateYAMLPatchClearsLimitsDisabledByPolicy(t *testing.T) {
	patch, err := generateYAMLPatch(Result{
		Namespace: "shop",
		Workload: kubernetes.Workload{
			DeploymentName: "payments-api",
			ContainerName:  "worker",
		},
		Recommendations: recommender.Recommendations{
			CPURequest:    0.1,
			MemoryRequest: 128,
			MemoryLimit:   256,
		},
	})
	if err != nil {
		t.Fatalf("generateYAMLPatch() error = %v", err)
	}
	if strings.Contains(patch, "cpu: \"0m\"") {
		t.Fatalf("patch contains a disabled CPU limit:\n%s", patch)
	}
	if !strings.Contains(patch, "limits:\n            cpu: null\n            memory: \"256Mi\"") {
		t.Fatalf("patch does not preserve enabled memory limit:\n%s", patch)
	}

	patch, err = generateYAMLPatch(Result{
		Namespace: "shop",
		Workload: kubernetes.Workload{
			DeploymentName: "payments-api",
			ContainerName:  "worker",
		},
		Recommendations: recommender.Recommendations{
			CPURequest:    0.1,
			MemoryRequest: 128,
		},
	})
	if err != nil {
		t.Fatalf("generateYAMLPatch() without limits error = %v", err)
	}
	if !strings.Contains(patch, "limits:\n            cpu: null\n            memory: null") {
		t.Fatalf("patch does not clear both disabled limits:\n%s", patch)
	}
}

func TestGenerateYAMLPatchRoundsUpToPreserveBuffers(t *testing.T) {
	patch, err := generateYAMLPatch(Result{
		Namespace: "shop",
		Workload: kubernetes.Workload{
			DeploymentName: "payments-api",
			ContainerName:  "worker",
		},
		Recommendations: recommender.Recommendations{
			CPURequest:    0.1001,
			CPULimit:      0.2001,
			MemoryRequest: 128.1,
			MemoryLimit:   256.1,
		},
	})
	if err != nil {
		t.Fatalf("generateYAMLPatch() error = %v", err)
	}
	for _, expected := range []string{`cpu: "101m"`, `cpu: "201m"`, `memory: "129Mi"`, `memory: "257Mi"`} {
		if !strings.Contains(patch, expected) {
			t.Fatalf("patch does not contain rounded-up %q:\n%s", expected, patch)
		}
	}
}

func TestGenerateYAMLPatchRemovesDisabledLimitWhenApplied(t *testing.T) {
	original := appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "worker",
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:              resource.MustParse("500m"),
									corev1.ResourceMemory:           resource.MustParse("128Mi"),
									corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
								},
							},
						},
					},
				},
			},
		},
	}
	patch, err := generateYAMLPatch(Result{
		Namespace: "shop",
		Workload: kubernetes.Workload{
			DeploymentName: "payments-api",
			ContainerName:  "worker",
		},
		Recommendations: recommender.Recommendations{
			CPURequest:    0.1,
			MemoryRequest: 128,
			MemoryLimit:   256,
		},
	})
	if err != nil {
		t.Fatalf("generateYAMLPatch() error = %v", err)
	}
	originalJSON, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal original deployment: %v", err)
	}
	patchJSON, err := utilyaml.ToJSON([]byte(patch))
	if err != nil {
		t.Fatalf("convert patch to JSON: %v", err)
	}
	mergedJSON, err := strategicpatch.StrategicMergePatch(originalJSON, patchJSON, appsv1.Deployment{})
	if err != nil {
		t.Fatalf("apply strategic merge patch: %v", err)
	}
	var merged appsv1.Deployment
	if err := json.Unmarshal(mergedJSON, &merged); err != nil {
		t.Fatalf("unmarshal merged deployment: %v", err)
	}
	limits := merged.Spec.Template.Spec.Containers[0].Resources.Limits
	if _, exists := limits[corev1.ResourceCPU]; exists {
		t.Fatalf("CPU limit remains after policy none: %#v", limits)
	}
	if got := limits.Memory().String(); got != "256Mi" {
		t.Fatalf("memory limit = %s, want 256Mi", got)
	}
	if got := limits.StorageEphemeral().String(); got != "1Gi" {
		t.Fatalf("ephemeral-storage limit = %s, want preserved 1Gi", got)
	}
}

func TestStructuredOutputWritesOnlyRequestedDocumentToStdout(t *testing.T) {
	result := Result{
		Namespace: "shop",
		Workload: kubernetes.Workload{
			DeploymentName: "payments-api",
			ContainerName:  "worker",
		},
		Recommendations: recommender.Recommendations{
			CPURequest:    0.1,
			MemoryRequest: 128,
		},
	}
	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			useTemporaryWorkingDirectory(t)
			stdout, err := captureStdout(func() error {
				return PrintResults(result, format)
			})
			if err != nil {
				t.Fatalf("PrintResults() error = %v", err)
			}
			if strings.Contains(stdout, "patch generated") || strings.Contains(stdout, "patch saved") {
				t.Fatalf("stdout contains operational status: %q", stdout)
			}
			if format == "json" {
				if !json.Valid([]byte(stdout)) {
					t.Fatalf("stdout is not one JSON document: %q", stdout)
				}
				return
			}
			yamlJSON, err := utilyaml.ToJSON([]byte(stdout))
			if err != nil || !json.Valid(yamlJSON) {
				t.Fatalf("stdout is not one YAML document: %q, error = %v", stdout, err)
			}
		})
	}
}

func TestPrintResultsReturnsPatchWriteFailure(t *testing.T) {
	useTemporaryWorkingDirectory(t)
	if err := os.Mkdir("resource-patch.yaml", 0755); err != nil {
		t.Fatalf("create blocking directory: %v", err)
	}
	_, err := captureStdout(func() error {
		return PrintResults(Result{
			Namespace: "shop",
			Workload: kubernetes.Workload{
				DeploymentName: "payments-api",
				ContainerName:  "worker",
			},
		}, "yaml")
	})
	if err == nil || !strings.Contains(err.Error(), "write resource-patch.yaml") {
		t.Fatalf("PrintResults() error = %v, want patch write failure", err)
	}
}

func useTemporaryWorkingDirectory(t *testing.T) {
	t.Helper()
	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("change working directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(original); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
}

func captureStdout(run func() error) (string, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return "", err
	}
	original := os.Stdout
	os.Stdout = writer
	runErr := run()
	_ = writer.Close()
	os.Stdout = original
	output, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if readErr != nil {
		return "", readErr
	}
	return string(output), runErr
}
