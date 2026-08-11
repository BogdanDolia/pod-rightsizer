package output

import (
	"encoding/json"
	"fmt"
	"math"
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
	Concurrency     int
	LoadTest        loadtest.RunResult
	LoadTestSLO     loadtest.SLO
	CurrentSettings kubernetes.ResourceSettings
	Metrics         []metrics.ResourceMetrics
	Recommendations recommender.Recommendations
	ResourcePatch   *kubernetes.ResourcePatch
}

// PrintResults displays the results in the specified format and writes the
// strategic merge patch to resource-patch.yaml.
func PrintResults(result Result, format string) error {
	switch format {
	case "json":
		return printJSON(result)
	case "yaml":
		return printYAML(result)
	default:
		return printText(result)
	}
}

// printText displays the results in a human-readable text format
func printText(r Result) error {
	avgCPU, avgMemory := metrics.CalculateAverageMetrics(r.Metrics)

	fmt.Println("\n===== Pod Rightsizer Results =====")
	fmt.Printf("\nLoad Test Target: %s\n", r.Target)
	fmt.Printf("Deployment: %s\n", r.Workload.DeploymentName)
	fmt.Printf("Container: %s\n", r.Workload.ContainerName)
	fmt.Printf("Pod Selector: %s\n", r.Workload.PodSelector)
	fmt.Printf("Namespace: %s\n", r.Namespace)
	if r.Concurrency > 0 {
		fmt.Printf("Load test: %d concurrent workers for %s\n", r.Concurrency, r.Duration)
	} else {
		fmt.Printf("Load test: %d RPS for %s\n", r.RPS, r.Duration)
	}
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
	fmt.Printf("Independent Samples: %d\n", r.Recommendations.Observed.IndependentSamples)
	fmt.Printf("Source Resolution: %s\n", metrics.SourceResolution(r.Metrics))
	fmt.Printf("CPU p%.2g: %.0fm\n", r.Recommendations.Observed.CPUPercentile, r.Recommendations.Observed.CPUPercentileValue*1000)
	fmt.Printf("Peak CPU: %.0fm\n", r.Recommendations.Observed.CPUPeak*1000)
	fmt.Printf("Average CPU: %.0fm\n", avgCPU*1000)
	fmt.Printf("Memory high-water mark: %.0fMi\n", r.Recommendations.Observed.MemoryHighWater)
	fmt.Printf("Average Memory: %.0fMi\n", avgMemory)

	fmt.Println("\nRecommended Settings:")
	fmt.Printf("CPU Request: %.0fm\n", math.Ceil(r.Recommendations.CPURequest*1000))
	printOptionalCPU("CPU Limit", r.Recommendations.CPULimit)
	fmt.Printf("Memory Request: %.0fMi\n", math.Ceil(r.Recommendations.MemoryRequest))
	printOptionalMemory("Memory Limit", r.Recommendations.MemoryLimit)
	fmt.Printf("Confidence: %s (%.2f)\n", r.Recommendations.Confidence.Level, r.Recommendations.Confidence.Score)

	fmt.Println("\nCalculation:")
	for _, line := range r.Recommendations.Explanation {
		fmt.Printf("- %s\n", line)
	}

	fmt.Println("\nComparison with Current Settings:")
	printComparison("CPU request", r.Recommendations.Comparison.CPURequest, "cores")
	printComparison("CPU limit", r.Recommendations.Comparison.CPULimit, "cores")
	printComparison("Memory request", r.Recommendations.Comparison.MemoryRequest, "Mi")
	printComparison("Memory limit", r.Recommendations.Comparison.MemoryLimit, "Mi")

	// Generate and save YAML if using text output mode
	patchContent, err := generateYAMLPatch(r)
	if err != nil {
		return fmt.Errorf("generate YAML patch: %w", err)
	}

	if err := os.WriteFile("resource-patch.yaml", []byte(patchContent), 0644); err != nil {
		return fmt.Errorf("write resource-patch.yaml: %w", err)
	}

	fmt.Println("\nYAML patch generated in 'resource-patch.yaml'")
	return nil
}

// printJSON displays the results in JSON format
func printJSON(r Result) error {
	avgCPU, avgMemory := metrics.CalculateAverageMetrics(r.Metrics)

	// Create a map with the relevant data
	data := map[string]interface{}{
		"loadTestTarget": r.Target,
		"deploymentName": r.Workload.DeploymentName,
		"containerName":  r.Workload.ContainerName,
		"podSelector":    r.Workload.PodSelector,
		"namespace":      r.Namespace,
		"duration":       r.Duration.String(),
		"rps":            r.RPS,
		"concurrency":    r.Concurrency,
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
			"independentSamples": r.Recommendations.Observed.IndependentSamples,
			"sourceResolution":   metrics.SourceResolution(r.Metrics).String(),
			"cpuPercentile":      r.Recommendations.Observed.CPUPercentile,
			"cpuPercentileValue": fmt.Sprintf("%.0fm", r.Recommendations.Observed.CPUPercentileValue*1000),
			"peakCPU":            fmt.Sprintf("%.0fm", r.Recommendations.Observed.CPUPeak*1000),
			"averageCPU":         fmt.Sprintf("%.0fm", avgCPU*1000),
			"memoryHighWater":    fmt.Sprintf("%.0fMi", r.Recommendations.Observed.MemoryHighWater),
			"avgMemory":          fmt.Sprintf("%.0fMi", avgMemory),
		},
		"recommendation": r.Recommendations,
	}

	// Marshal to JSON and print
	jsonBytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON output: %w", err)
	}

	fmt.Println(string(jsonBytes))

	// Generate and save YAML if using json output mode
	patchContent, err := generateYAMLPatch(r)
	if err != nil {
		return fmt.Errorf("generate YAML patch: %w", err)
	}

	if err := os.WriteFile("resource-patch.yaml", []byte(patchContent), 0644); err != nil {
		return fmt.Errorf("write resource-patch.yaml: %w", err)
	}

	fmt.Fprintln(os.Stderr, "YAML patch generated in 'resource-patch.yaml'")
	return nil
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
func printYAML(r Result) error {
	patchContent, err := generateYAMLPatch(r)
	if err != nil {
		return fmt.Errorf("generate YAML patch: %w", err)
	}

	fmt.Print(patchContent)

	if err := os.WriteFile("resource-patch.yaml", []byte(patchContent), 0644); err != nil {
		return fmt.Errorf("write resource-patch.yaml: %w", err)
	}

	fmt.Fprintln(os.Stderr, "YAML patch saved to 'resource-patch.yaml'")
	return nil
}

// generateYAMLPatch creates a YAML patch for the resources
func generateYAMLPatch(r Result) (string, error) {
	if r.ResourcePatch == nil {
		return "", fmt.Errorf("server-side dry-run resource patch is missing")
	}
	if r.ResourcePatch.Namespace() != r.Namespace ||
		r.ResourcePatch.DeploymentName() != r.Workload.DeploymentName ||
		r.ResourcePatch.ContainerName() != r.Workload.ContainerName {
		return "", fmt.Errorf("resource patch identity does not match the resolved workload")
	}
	patch, err := r.ResourcePatch.YAML()
	if err != nil {
		return "", err
	}
	return string(patch), nil
}

func printOptionalCPU(label string, value float64) {
	if value == 0 {
		fmt.Printf("%s: none\n", label)
		return
	}
	fmt.Printf("%s: %.0fm\n", label, math.Ceil(value*1000))
}

func printOptionalMemory(label string, value float64) {
	if value == 0 {
		fmt.Printf("%s: none\n", label)
		return
	}
	fmt.Printf("%s: %.0fMi\n", label, math.Ceil(value))
}

func printComparison(label string, comparison recommender.SettingComparison, unit string) {
	percentage := "n/a"
	if comparison.DeltaPercent != nil {
		percentage = fmt.Sprintf("%+.1f%%", *comparison.DeltaPercent)
	}
	fmt.Printf(
		"%s: %.3g -> %.3g %s (%s, %s)\n",
		label,
		comparison.Current,
		comparison.Recommended,
		unit,
		comparison.Direction,
		percentage,
	)
}
