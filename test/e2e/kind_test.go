package e2e_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BogdanDolia/pod-rightsizer/pkg/kubernetes"
)

func TestKindResourcePatchDryRunDoesNotMutateDeployment(t *testing.T) {
	if os.Getenv("RUN_KIND_E2E") != "1" {
		t.Skip("set RUN_KIND_E2E=1 to run against a prepared kind cluster")
	}
	kubeconfig := os.Getenv("KUBECONFIG")
	client, err := kubernetes.NewClient(kubeconfig)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	target, err := client.ResolveWorkload(ctx, "pod-rightsizer-e2e", "rightsizer-e2e", "target")
	if err != nil {
		t.Fatalf("ResolveWorkload(target) error = %v", err)
	}
	sidecar, err := client.ResolveWorkload(ctx, "pod-rightsizer-e2e", "rightsizer-e2e", "sidecar")
	if err != nil {
		t.Fatalf("ResolveWorkload(sidecar) error = %v", err)
	}
	targetBefore, err := client.GetResourceSettings(ctx, "pod-rightsizer-e2e", target)
	if err != nil {
		t.Fatalf("GetResourceSettings(target) error = %v", err)
	}
	sidecarBefore, err := client.GetResourceSettings(ctx, "pod-rightsizer-e2e", sidecar)
	if err != nil {
		t.Fatalf("GetResourceSettings(sidecar) error = %v", err)
	}

	patch, err := client.PrepareResourcePatch(
		ctx,
		"pod-rightsizer-e2e",
		target,
		kubernetes.ResourceSettings{
			CPURequest:    0.123,
			CPULimit:      0.25,
			MemoryRequest: 48,
			MemoryLimit:   96,
		},
	)
	if err != nil {
		t.Fatalf("PrepareResourcePatch() error = %v", err)
	}
	patchYAML, err := patch.YAML()
	if err != nil {
		t.Fatalf("YAML() error = %v", err)
	}
	if !strings.Contains(string(patchYAML), "name: target") || strings.Contains(string(patchYAML), "name: sidecar") {
		t.Fatalf("patch does not isolate the target container:\n%s", patchYAML)
	}

	targetAfter, err := client.GetResourceSettings(ctx, "pod-rightsizer-e2e", target)
	if err != nil {
		t.Fatalf("GetResourceSettings(target after dry-run) error = %v", err)
	}
	sidecarAfter, err := client.GetResourceSettings(ctx, "pod-rightsizer-e2e", sidecar)
	if err != nil {
		t.Fatalf("GetResourceSettings(sidecar after dry-run) error = %v", err)
	}
	if targetAfter != targetBefore {
		t.Fatalf("target resources changed after dry-run: before=%#v after=%#v", targetBefore, targetAfter)
	}
	if sidecarAfter != sidecarBefore {
		t.Fatalf("sidecar resources changed after dry-run: before=%#v after=%#v", sidecarBefore, sidecarAfter)
	}
}
