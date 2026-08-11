package output

import (
	"strings"
	"testing"

	"github.com/BogdanDolia/pod-rightsizer/pkg/kubernetes"
	"github.com/BogdanDolia/pod-rightsizer/pkg/recommender"
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

func TestGenerateYAMLPatchOmitsLimitsDisabledByPolicy(t *testing.T) {
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
	if !strings.Contains(patch, "limits:\n            memory: \"256Mi\"") {
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
	if strings.Contains(patch, "limits:") {
		t.Fatalf("patch contains an empty limits block:\n%s", patch)
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
