package output

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BogdanDolia/pod-rightsizer/pkg/kubernetes"
	"github.com/BogdanDolia/pod-rightsizer/pkg/metrics"
	"github.com/BogdanDolia/pod-rightsizer/pkg/recommender"
)

// Result contains all data to be presented in the output
type Result struct {
	Target          string
	ServiceName     string
	Namespace       string
	Duration        time.Duration
	RPS             int
	CurrentSettings kubernetes.ResourceSettings
	Metrics         []metrics.ResourceMetrics
	Recommendations recommender.Recommendations
}

// PrintResults displays the results in the specified format
func PrintResults(result Result, format string) {
	switch format {
	case "json":
		printJSON(result)
	case "yaml":
		printYAML(result)
	default:
		printText(result)
	}
}

// printText displays the results in a human-readable text format
func printText(r Result) {
	avgCPU, avgMemory := metrics.CalculateAverageMetrics(r.Metrics)
	peakCPU, peakMemory := metrics.CalculatePeakMetrics(r.Metrics)

	fmt.Println("\n===== Pod Rightsizer Results =====")
	fmt.Printf("\nLoad Test Target: %s\n", r.Target)
	if r.ServiceName != r.Target {
		fmt.Printf("Service Name: %s\n", r.ServiceName)
	}
	fmt.Printf("Namespace: %s\n", r.Namespace)
	fmt.Printf("Load test: %d RPS for %s\n", r.RPS, r.Duration)

	fmt.Println("\nCurrent Settings:")
	fmt.Printf("CPU Request: %.0fm\n", r.CurrentSettings.CPURequest*1000)
	fmt.Printf("CPU Limit: %.0fm\n", r.CurrentSettings.CPULimit*1000)
	fmt.Printf("Memory Request: %.0fMi\n", r.CurrentSettings.MemoryRequest)
	fmt.Printf("Memory Limit: %.0fMi\n", r.CurrentSettings.MemoryLimit)

	fmt.Println("\nMetrics Collected:")
	fmt.Printf("Peak CPU: %.0fm\n", peakCPU*1000)
	fmt.Printf("Average CPU: %.0fm\n", avgCPU*1000)
	fmt.Printf("Peak Memory: %.0fMi\n", peakMemory)
	fmt.Printf("Average Memory: %.0fMi\n", avgMemory)

	fmt.Println("\nRecommended Settings:")
	fmt.Printf("CPU Request: %.0fm\n", r.Recommendations.CPURequest*1000)
	fmt.Printf("CPU Limit: %.0fm\n", r.Recommendations.CPULimit*1000)
	fmt.Printf("Memory Request: %.0fMi\n", r.Recommendations.MemoryRequest)
	fmt.Printf("Memory Limit: %.0fMi\n", r.Recommendations.MemoryLimit)

	// Generate and save YAML if using text output mode
	patchContent, err := generateYAMLPatch(r)
	if err != nil {
		fmt.Printf("\nError generating YAML patch: %v\n", err)
		return
	}

	err = os.WriteFile("resource-patch.yaml", []byte(patchContent), 0644)
	if err != nil {
		fmt.Printf("\nError writing YAML patch file: %v\n", err)
		return
	}

	fmt.Println("\nYAML patch generated in 'resource-patch.yaml'")
}

// printJSON displays the results in JSON format
func printJSON(r Result) {
	avgCPU, avgMemory := metrics.CalculateAverageMetrics(r.Metrics)
	peakCPU, peakMemory := metrics.CalculatePeakMetrics(r.Metrics)

	// Create a map with the relevant data
	data := map[string]interface{}{
		"loadTestTarget": r.Target,
		"serviceName":    r.ServiceName,
		"namespace":      r.Namespace,
		"duration":       r.Duration.String(),
		"rps":            r.RPS,
		"current": map[string]interface{}{
			"cpuRequest":    fmt.Sprintf("%.0fm", r.CurrentSettings.CPURequest*1000),
			"cpuLimit":      fmt.Sprintf("%.0fm", r.CurrentSettings.CPULimit*1000),
			"memoryRequest": fmt.Sprintf("%.0fMi", r.CurrentSettings.MemoryRequest),
			"memoryLimit":   fmt.Sprintf("%.0fMi", r.CurrentSettings.MemoryLimit),
		},
		"metrics": map[string]interface{}{
			"peakCPU":    fmt.Sprintf("%.0fm", peakCPU*1000),
			"averageCPU": fmt.Sprintf("%.0fm", avgCPU*1000),
			"peakMemory": fmt.Sprintf("%.0fMi", peakMemory),
			"avgMemory":  fmt.Sprintf("%.0fMi", avgMemory),
		},
		"recommendations": map[string]interface{}{
			"cpuRequest":    fmt.Sprintf("%.0fm", r.Recommendations.CPURequest*1000),
			"cpuLimit":      fmt.Sprintf("%.0fm", r.Recommendations.CPULimit*1000),
			"memoryRequest": fmt.Sprintf("%.0fMi", r.Recommendations.MemoryRequest),
			"memoryLimit":   fmt.Sprintf("%.0fMi", r.Recommendations.MemoryLimit),
		},
	}

	// Marshal to JSON and print
	jsonBytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling JSON: %v\n", err)
		return
	}

	fmt.Println(string(jsonBytes))

	// Generate and save YAML if using json output mode
	patchContent, err := generateYAMLPatch(r)
	if err != nil {
		fmt.Printf("\nError generating YAML patch: %v\n", err)
		return
	}

	err = os.WriteFile("resource-patch.yaml", []byte(patchContent), 0644)
	if err != nil {
		fmt.Printf("\nError writing YAML patch file: %v\n", err)
		return
	}

	fmt.Println("\nYAML patch generated in 'resource-patch.yaml'")
}

// printYAML displays and saves the results in YAML format (the patch file)
func printYAML(r Result) {
	patchContent, err := generateYAMLPatch(r)
	if err != nil {
		fmt.Printf("Error generating YAML patch: %v\n", err)
		return
	}

	fmt.Println(patchContent)

	err = os.WriteFile("resource-patch.yaml", []byte(patchContent), 0644)
	if err != nil {
		fmt.Printf("\nError writing YAML patch file: %v\n", err)
		return
	}

	fmt.Println("\nYAML patch saved to 'resource-patch.yaml'")
}

// generateYAMLPatch creates a YAML patch for the resources
func generateYAMLPatch(r Result) (string, error) {
	// Create a simple deployment patch with the new resource settings
	patch := fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  namespace: %s
  name: %s # This assumes the deployment name matches the service name
spec:
  template:
    spec:
      containers:
      - name: app # This assumes the container name is "app"
        resources:
          requests:
            cpu: "%dm"
            memory: "%dMi"
          limits:
            cpu: "%dm"
            memory: "%dMi"
`,
		r.Namespace,
		extractResourceName(r.ServiceName),
		int(r.Recommendations.CPURequest*1000),
		int(r.Recommendations.MemoryRequest),
		int(r.Recommendations.CPULimit*1000),
		int(r.Recommendations.MemoryLimit),
	)

	return patch, nil
}

// extractResourceName extracts a resource name from a URL or label selector
func extractResourceName(target string) string {
	// If target is a URL, extract the host part
	if strings.HasPrefix(target, "http://") {
		hostPart := strings.TrimPrefix(target, "http://")
		hostParts := strings.Split(hostPart, ":")
		return hostParts[0]
	} else if strings.HasPrefix(target, "https://") {
		hostPart := strings.TrimPrefix(target, "https://")
		hostParts := strings.Split(hostPart, ":")
		return hostParts[0]
	}

	// If target is a label selector, use the value part
	if strings.Contains(target, "=") {
		parts := strings.Split(target, "=")
		if len(parts) > 1 {
			return parts[1]
		}
	}

	return target
}
