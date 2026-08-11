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
