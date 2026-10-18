package metrics

import (
	"time"

	"github.com/HelixThinker/pod-rightsizer/pkg/kubernetes"
	contract "github.com/HelixThinker/pod-rightsizer/pkg/providers/loadtest"
)

const (
	// DiscardForeignPod means the sample is a different Pod from the one selected.
	DiscardForeignPod = "foreign_pod"
	// DiscardForeignContainer means the sample is a different container.
	DiscardForeignContainer = "foreign_container"
	// DiscardRunnerMetrics means the sample belongs to the load runner.
	DiscardRunnerMetrics = "runner_metrics"
	// DiscardAggregateOnly means the source has no per-pod attribution.
	DiscardAggregateOnly = "aggregate_only"
	// DiscardMissingTarget means the selected Pod did not return metrics.
	DiscardMissingTarget = "missing_target_coverage"
	// DiscardMetricsUID means a PodMetrics UID was used as if it were the Pod UID.
	DiscardMetricsUID = "metrics_uid_unlinked"
	// DiscardMixedReplicas means other replicas were present and removed from the primary row.
	DiscardMixedReplicas = "mixed_replicas"
	// DiscardClockMapping means the source window has no proven clock mapping.
	DiscardClockMapping = "clock_mapping_unknown"
	// DiscardClockBoundary means the window is inside the interval only if uncertainty is ignored.
	DiscardClockBoundary = "clock_uncertainty_boundary"
)

// PodSelector is the Pod and container a direct measurement asked for.
type PodSelector struct {
	PodUID    string
	Container string
}

// Scope is the primary resource attribution of one sample. verified
// selected_pod is not implied by an aggregate maximum or by a PodMetrics UID.
type Scope struct {
	Kind             string
	Status           string
	PodUID           string
	Container        string
	Attribution      string
	ListedPodUID     string
	MetricsObjectUID string
	Expected         int
	Observed         int
	ReplicaCount     int
	Reasons          []string
}

// ScopeFromSnapshot describes the sample the collector already has. A
// Deployment aggregate, including one that also carries pod rows, is not a
// verified selected_pod scope. T05 and T10 call BindSelectedPod to make that
// claim's inputs. This function does not.
func ScopeFromSnapshot(sample ResourceMetrics) Scope {
	scope := Scope{
		Kind:        contract.ScopeDeploymentAggregate,
		Status:      contract.ScopeStatusUnverified,
		Container:   sample.ContainerName,
		Attribution: contract.AttributionUnknown,
		Expected:    sample.Coverage.Expected,
		Observed:    sample.Coverage.Observed,
	}
	if len(sample.Pods) == 0 {
		scope.Status = contract.ScopeStatusAggregateOnly
		scope.Attribution = contract.AttributionAggregateOnly
		scope.Reasons = []string{DiscardAggregateOnly}
		return scope
	}
	scope.Attribution = contract.AttributionPerPod
	scope.ReplicaCount = len(sample.Pods)
	return scope
}

// AllowVerifiedSelectedPod reports whether the sample's own rows are enough
// to name a verified selected_pod scope. Both the listed Pod UID and the
// PodMetrics UID must have been read and must be equal. An empty metrics UID
// is not treated as a match. This is not an owner-chain check.
func AllowVerifiedSelectedPod(sample ResourceMetrics, selector PodSelector) bool {
	if selector.PodUID == "" || selector.Container == "" || sample.ContainerName != selector.Container {
		return false
	}
	if len(sample.Pods) != 1 {
		return false
	}
	pod := sample.Pods[0]
	if pod.Role == "runner" || pod.Role == "sidecar" {
		return false
	}
	if pod.UID == "" || pod.UID != selector.PodUID {
		return false
	}
	if pod.MetricsObjectUID == "" || pod.MetricsObjectUID != pod.UID {
		return false
	}
	return true
}

// BindSelectedPod returns the primary sample for one Pod and container.
// Other replicas are not copied into the CPU or memory values. A missing
// target is not replaced with another Pod. An aggregate without pod rows
// cannot become selected_pod.
func BindSelectedPod(sample ResourceMetrics, selector PodSelector) (ResourceMetrics, string) {
	if sample.ContainerName != "" && selector.Container != "" && sample.ContainerName != selector.Container {
		return sample, DiscardForeignContainer
	}
	if len(sample.Pods) == 0 {
		return sample, DiscardAggregateOnly
	}
	var match *kubernetes.PodResourceUsage
	foreign := 0
	for i := range sample.Pods {
		pod := &sample.Pods[i]
		if pod.Role == "runner" {
			return sample, DiscardRunnerMetrics
		}
		if pod.Role == "sidecar" {
			continue
		}
		if pod.MetricsObjectUID != "" && pod.UID != "" && pod.MetricsObjectUID != pod.UID && pod.MetricsObjectUID == selector.PodUID {
			return sample, DiscardMetricsUID
		}
		if pod.UID == selector.PodUID {
			match = pod
			continue
		}
		foreign++
	}
	if match == nil {
		if foreign > 0 {
			return sample, DiscardForeignPod
		}
		return sample, DiscardMissingTarget
	}
	bound := sample
	bound.Pods = []kubernetes.PodResourceUsage{*match}
	bound.CPUUsage = match.CPUCores
	bound.MemoryUsage = match.MemoryMi
	bound.CPUSourcePod = match.Name
	bound.MemorySourcePod = match.Name
	if !match.SourceTimestamp.IsZero() {
		bound.Timestamp = match.SourceTimestamp
	}
	if match.SourceWindow > 0 {
		bound.Window = match.SourceWindow
	}
	bound.ContainerName = selector.Container
	if foreign > 0 {
		return bound, ""
	}
	return bound, ""
}

// ClassifySelectedPod keeps only the selected Pod, then drops windows that
// are not proven to lie inside the measured interval, and only then dedups
// and folds high-water marks. mapping nil uses the same-clock containment of
// ClassifyWindows. It is not zero uncertainty for a runner clock. An unknown
// mapping discards every window.
func ClassifySelectedPod(
	samples []ResourceMetrics,
	selector PodSelector,
	intervalStart, intervalEnd time.Time,
	mapping *contract.ClockMapping,
) (WindowEvidence, error) {
	if intervalStart.IsZero() || intervalEnd.IsZero() || !intervalEnd.After(intervalStart) {
		return WindowEvidence{}, errInterval()
	}
	discarded := make([]ClassifiedSample, 0)
	diagnostic := make([]ClassifiedSample, 0)
	kept := make([]ResourceMetrics, 0, len(samples))
	for _, sample := range samples {
		original := sample
		if mixed(sample, selector) {
			diagnostic = append(diagnostic, ClassifiedSample{
				Sample:      original,
				Disposition: DispositionDiscarded,
				Reason:      DiscardMixedReplicas,
			})
		}
		bound, reason := BindSelectedPod(sample, selector)
		if reason != "" {
			discarded = append(discarded, ClassifiedSample{
				Sample:      original,
				Disposition: DispositionDiscarded,
				Reason:      reason,
			})
			continue
		}
		kept = append(kept, bound)
	}
	var containment func(ResourceMetrics) (bool, string)
	if mapping != nil {
		containment = func(sample ResourceMetrics) (bool, string) {
			ok, why := contract.WindowContained(sample.Timestamp, sample.Window, intervalStart, intervalEnd, *mapping)
			if ok {
				return true, ""
			}
			if why == "clock mapping is unknown" || why == "clock uncertainty covers the measured interval" || why == "measured interval is not known" {
				return false, DiscardClockMapping
			}
			return false, DiscardClockBoundary
		}
	}
	evidence, err := classifySamples(kept, intervalStart, intervalEnd, true, containment)
	if err != nil {
		return WindowEvidence{}, err
	}
	evidence.Raw = append(discarded, evidence.Raw...)
	evidence.Diagnostic = append(diagnostic, evidence.Diagnostic...)
	evidence.DiscardedCount += len(discarded)
	return evidence, nil
}

func mixed(sample ResourceMetrics, selector PodSelector) bool {
	found := false
	others := 0
	for _, pod := range sample.Pods {
		if pod.Role == "runner" || pod.Role == "sidecar" {
			continue
		}
		if pod.UID == selector.PodUID {
			found = true
			continue
		}
		others++
	}
	return found && others > 0
}

func errInterval() error {
	return errString("measurement interval must have a non-zero end after its start")
}

type errString string

func (e errString) Error() string { return string(e) }
