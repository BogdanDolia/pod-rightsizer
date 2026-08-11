package output

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/BogdanDolia/pod-rightsizer/pkg/kubernetes"
	"github.com/BogdanDolia/pod-rightsizer/pkg/loadtest"
	"github.com/BogdanDolia/pod-rightsizer/pkg/metrics"
	"github.com/BogdanDolia/pod-rightsizer/pkg/recommender"
)

// Result contains all data to be presented in the output
type Result struct {
	Target          string
	Namespace       string
	Workload        kubernetes.Workload
	Duration        time.Duration
	RPS             int
	LoadTest        loadtest.RunResult
	LoadTestSLO     loadtest.SLO
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
	fmt.Printf("Deployment: %s\n", r.Workload.DeploymentName)
	fmt.Printf("Container: %s\n", r.Workload.ContainerName)
	fmt.Printf("Pod Selector: %s\n", r.Workload.PodSelector)
	fmt.Printf("Namespace: %s\n", r.Namespace)
	fmt.Printf("Load test: %d RPS for %s\n", r.RPS, r.Duration)
	fmt.Println("\nLoad Test Result:")
	fmt.Printf("Actual RPS: %.2f req/s\n", r.LoadTest.ActualRPS)
	fmt.Printf("HTTP Error Rate: %.2f%%\n", r.LoadTest.HTTPErrorRate*100)
	fmt.Printf("Latency p50/p95/p99: %s / %s / %s\n",
		r.LoadTest.P50Latency, r.LoadTest.P95Latency, r.LoadTest.P99Latency)
	fmt.Printf("Termination Reason: %s\n", r.LoadTest.TerminationReason)
	fmt.Println("Status Codes:")
	printStatusCodes(r.LoadTest.StatusCodes)
	fmt.Printf(
		"SLO: minimum RPS %.2f, maximum HTTP error rate %.2f%%, maximum p95 %s\n",
		r.LoadTestSLO.MinimumRPS,
		r.LoadTestSLO.MaximumHTTPErrorRate*100,
		r.LoadTestSLO.MaximumP95Latency,
	)

	fmt.Println("\nCurrent Settings:")
	fmt.Printf("CPU Request: %.0fm\n", r.CurrentSettings.CPURequest*1000)
	fmt.Printf("CPU Limit: %.0fm\n", r.CurrentSettings.CPULimit*1000)
	fmt.Printf("Memory Request: %.0fMi\n", r.CurrentSettings.MemoryRequest)
	fmt.Printf("Memory Limit: %.0fMi\n", r.CurrentSettings.MemoryLimit)

	fmt.Println("\nMetrics Collected:")
	fmt.Printf("Independent Samples: %d\n", len(r.Metrics))
	fmt.Printf("Source Resolution: %s\n", metrics.SourceResolution(r.Metrics))
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
		"deploymentName": r.Workload.DeploymentName,
		"containerName":  r.Workload.ContainerName,
		"podSelector":    r.Workload.PodSelector,
		"namespace":      r.Namespace,
		"duration":       r.Duration.String(),
		"rps":            r.RPS,
		"loadTest": map[string]interface{}{
			"requests":          r.LoadTest.Requests,
			"httpErrors":        r.LoadTest.HTTPErrors,
			"actualRPS":         r.LoadTest.ActualRPS,
			"httpErrorRate":     r.LoadTest.HTTPErrorRate,
			"p50Latency":        r.LoadTest.P50Latency.String(),
			"p95Latency":        r.LoadTest.P95Latency.String(),
			"p99Latency":        r.LoadTest.P99Latency.String(),
			"statusCodes":       r.LoadTest.StatusCodes,
			"duration":          r.LoadTest.Duration.String(),
			"terminationReason": r.LoadTest.TerminationReason,
			"slo": map[string]interface{}{
				"minimumRPS":           r.LoadTestSLO.MinimumRPS,
				"maximumHTTPErrorRate": r.LoadTestSLO.MaximumHTTPErrorRate,
				"maximumP95Latency":    r.LoadTestSLO.MaximumP95Latency.String(),
			},
		},
		"current": map[string]interface{}{
			"cpuRequest":    fmt.Sprintf("%.0fm", r.CurrentSettings.CPURequest*1000),
			"cpuLimit":      fmt.Sprintf("%.0fm", r.CurrentSettings.CPULimit*1000),
			"memoryRequest": fmt.Sprintf("%.0fMi", r.CurrentSettings.MemoryRequest),
			"memoryLimit":   fmt.Sprintf("%.0fMi", r.CurrentSettings.MemoryLimit),
		},
		"metrics": map[string]interface{}{
			"independentSamples": len(r.Metrics),
			"sourceResolution":   metrics.SourceResolution(r.Metrics).String(),
			"peakCPU":            fmt.Sprintf("%.0fm", peakCPU*1000),
			"averageCPU":         fmt.Sprintf("%.0fm", avgCPU*1000),
			"peakMemory":         fmt.Sprintf("%.0fMi", peakMemory),
			"avgMemory":          fmt.Sprintf("%.0fMi", avgMemory),
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

func printStatusCodes(statusCodes map[int]int) {
	codes := make([]int, 0, len(statusCodes))
	for code := range statusCodes {
		codes = append(codes, code)
	}
	sort.Ints(codes)
	if len(codes) == 0 {
		fmt.Println("  no HTTP responses recorded")
		return
	}
	for _, code := range codes {
		fmt.Printf("  %d: %d\n", code, statusCodes[code])
	}
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
	patch := fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  namespace: %s
  name: %s
spec:
  template:
    spec:
      containers:
      - name: %s
        resources:
          requests:
            cpu: "%dm"
            memory: "%dMi"
          limits:
            cpu: "%dm"
            memory: "%dMi"
`,
		r.Namespace,
		r.Workload.DeploymentName,
		r.Workload.ContainerName,
		int(r.Recommendations.CPURequest*1000),
		int(r.Recommendations.MemoryRequest),
		int(r.Recommendations.CPULimit*1000),
		int(r.Recommendations.MemoryLimit),
	)

	return patch, nil
}
