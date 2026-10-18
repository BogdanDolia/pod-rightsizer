package loadtest

import (
	"fmt"
)

// AssessSuitability reports whether an outcome is usable as a successful
// measurement. Numeric SLO failures are not suitability failures. A missing
// required signal, a known fact outside the contract range, unknown or
// incompatible latency semantics, failed application checks, confirmed
// generator saturation, and a missing, unfinished, repeated, or contradictory
// measured phase are. A usable duration_elapsed result needs one completed
// measured phase even when the request has no phase callback and does not
// list measured_phase_boundaries. An empty phase plan is that measured phase,
// not permission to skip it.
func AssessSuitability(req Request, spec AdapterSpec, out Outcome) Suitability {
	if !out.Executed {
		reasons := []string{"load did not start"}
		if req.StrictDirect {
			reasons = append(reasons, "strict direct experiment did not start")
		}
		return Suitability{Reasons: reasons}
	}
	var reasons []string
	if out.Partial || out.Termination != TerminationDurationElapsed {
		reasons = append(reasons, "load did not finish successfully")
	}
	reasons = append(reasons, signalReasons(req, out)...)
	reasons = append(reasons, reportedFactProblems(out)...)
	reasons = append(reasons, latencyReasons(req, spec, out)...)
	reasons = append(reasons, checkReasons(out)...)
	reasons = append(reasons, saturationReasons(out)...)
	reasons = append(reasons, phaseReasons(req, out)...)
	reasons = append(reasons, presentedRouteProblems(out)...)
	reasons = append(reasons, strictDirectProblems(req, out)...)
	if len(reasons) > 0 {
		return Suitability{Reasons: dedupe(reasons)}
	}
	return Suitability{Usable: true}
}

func signalReasons(req Request, out Outcome) []string {
	var reasons []string
	for _, capability := range requiredCapabilities(req) {
		if signalPresent(capability, out) {
			continue
		}
		if signalUnknown(capability, out) {
			reasons = append(reasons, fmt.Sprintf("unknown required signal %s", capability))
			continue
		}
		reasons = append(reasons, fmt.Sprintf("missing required signal %s", capability))
	}
	return reasons
}

func signalPresent(capability Capability, out Outcome) bool {
	switch capability {
	case CapabilityHTTPRequests:
		return out.HTTPRequests.State == SignalKnown
	case CapabilityHTTPErrors:
		return out.HTTPErrors.State == SignalKnown
	case CapabilityActualHTTPThroughput:
		if out.HTTPRequests.State == SignalKnown && out.HTTPRequests.Value == 0 {
			return out.ActualThroughput.State == SignalMissing
		}
		return out.ActualThroughput.State == SignalKnown
	case CapabilityUsefulThroughput:
		if out.HTTPRequests.State == SignalKnown && out.HTTPRequests.Value == 0 {
			return out.UsefulThroughput.State != SignalKnown
		}
		return out.UsefulThroughput.State == SignalKnown
	case CapabilityDroppedWork:
		return out.DroppedWork.State == SignalKnown
	case CapabilityGeneratorSaturation:
		return out.GeneratorSaturated.State == SignalKnown
	case CapabilityApplicationChecks:
		return out.Checks.State == SignalKnown
	case CapabilityLatencyUntilHeaders:
		if out.Latency.Semantics != LatencyUntilResponseHeaders {
			return false
		}
		if out.HTTPRequests.State == SignalKnown && out.HTTPRequests.Value == 0 {
			return durationsMissing(out.Latency)
		}
		return out.Latency.P50.State == SignalKnown &&
			out.Latency.P95.State == SignalKnown &&
			out.Latency.P99.State == SignalKnown
	case CapabilityMeasuredPhase:
		return out.Measured != nil && !out.Measured.StartedAt.IsZero() && !out.Measured.FinishedAt.IsZero()
	case CapabilityWarmupCooldown, CapabilityCancellation:
		return true
	case CapabilityDirectRouteProof:
		return out.Route.Mode == RouteDirectPod && out.Route.Status == AssuranceVerified && len(DirectProofGaps(out)) == 0
	case CapabilityInClusterRunner:
		return out.Runner.Identity.Placement == PlacementInCluster && out.Runner.Identity.Source == RunnerSourceObserved && uidKnown(out.Runner.Identity.PodUID, "")
	case CapabilitySelectedPodMetrics:
		return out.Metrics.Kind == ScopeSelectedPod && out.Metrics.Status == ScopeStatusVerified && out.Metrics.Attribution == AttributionPerPod
	default:
		return false
	}
}

func signalUnknown(capability Capability, out Outcome) bool {
	switch capability {
	case CapabilityHTTPRequests:
		return out.HTTPRequests.State == SignalUnknown
	case CapabilityHTTPErrors:
		return out.HTTPErrors.State == SignalUnknown
	case CapabilityActualHTTPThroughput:
		return out.ActualThroughput.State == SignalUnknown
	case CapabilityUsefulThroughput:
		return out.UsefulThroughput.State == SignalUnknown
	case CapabilityDroppedWork:
		return out.DroppedWork.State == SignalUnknown
	case CapabilityGeneratorSaturation:
		return out.GeneratorSaturated.State == SignalUnknown
	case CapabilityApplicationChecks:
		return out.Checks.State == SignalUnknown
	case CapabilityLatencyUntilHeaders:
		return out.Latency.Semantics == LatencyUnknown
	case CapabilityDirectRouteProof:
		return out.Route.Status == AssuranceUnknown || out.Route.Mode == RouteUnknown
	case CapabilityInClusterRunner:
		return out.Runner.Identity.Placement == PlacementUnknown
	case CapabilitySelectedPodMetrics:
		return out.Metrics.Status == ScopeStatusUnknown
	default:
		return false
	}
}

func durationsMissing(latency LatencyReport) bool {
	return latency.P50.State == SignalMissing &&
		latency.P95.State == SignalMissing &&
		latency.P99.State == SignalMissing
}

func latencyReasons(req Request, spec AdapterSpec, out Outcome) []string {
	var reasons []string
	if !out.Latency.Semantics.Known() || out.Latency.Semantics == LatencyUnknown {
		reasons = append(reasons, "latency semantics are unknown")
	}
	if req.LatencySemantics != "" && out.Latency.Semantics != req.LatencySemantics {
		reasons = append(reasons, fmt.Sprintf(
			"latency semantics %s are not requested %s",
			out.Latency.Semantics,
			req.LatencySemantics,
		))
	}
	if spec.LatencySemantics != "" && out.Latency.Semantics != spec.LatencySemantics {
		reasons = append(reasons, fmt.Sprintf(
			"latency semantics %s are not the adapter semantics %s",
			out.Latency.Semantics,
			spec.LatencySemantics,
		))
	}
	return reasons
}

func checkReasons(out Outcome) []string {
	switch out.Checks.State {
	case SignalUnknown:
		return []string{"application checks are unknown"}
	case SignalKnown:
		if out.Checks.Failed > out.Checks.Defined ||
			(out.Checks.Passed && out.Checks.Failed > 0) ||
			(!out.Checks.Passed && out.Checks.Failed == 0) {
			return []string{"application checks are inconsistent"}
		}
		if !out.Checks.Passed {
			return []string{"application checks failed"}
		}
	}
	return nil
}

func saturationReasons(out Outcome) []string {
	switch out.GeneratorSaturated.State {
	case SignalUnknown:
		return []string{"generator saturation is unknown"}
	case SignalKnown:
		if out.GeneratorSaturated.Value {
			return []string{"generator saturated"}
		}
	}
	return nil
}

func phaseReasons(req Request, out Outcome) []string {
	reasons := phaseOrderProblems(out.Phases)
	reasons = append(reasons, measuredBoundProblems(out)...)
	reasons = append(reasons, measuredCompletionProblems(out)...)
	for _, planned := range req.Phases {
		if planned.Name != PhaseWarmup && planned.Name != PhaseCooldown {
			continue
		}
		actual, ok := findPhase(out.Phases, planned.Name)
		if !ok || actual.StartedAt.IsZero() || actual.FinishedAt.IsZero() {
			reasons = append(reasons, fmt.Sprintf("planned %s phase did not run", planned.Name))
		}
	}
	return reasons
}

func phaseOrderProblems(phases []PhaseActual) []string {
	var reasons []string
	last := -1
	seen := map[PhaseName]int{}
	for _, phase := range phases {
		rank, ok := phaseRank(phase.Name)
		if !ok {
			reasons = append(reasons, fmt.Sprintf("unknown load phase %q", phase.Name))
			continue
		}
		seen[phase.Name]++
		if rank <= last {
			reasons = append(reasons, fmt.Sprintf("load phase %s is out of order", phase.Name))
		}
		last = rank
		if !phase.StartedAt.IsZero() && !phase.FinishedAt.IsZero() && phase.FinishedAt.Before(phase.StartedAt) {
			reasons = append(reasons, fmt.Sprintf("%s phase finished before it started", phase.Name))
		}
	}
	for _, name := range []PhaseName{PhaseWarmup, PhaseMeasured, PhaseCooldown} {
		if seen[name] > 1 {
			reasons = append(reasons, fmt.Sprintf("%s phase is repeated", name))
		}
	}
	return reasons
}

func measuredBoundProblems(out Outcome) []string {
	var reasons []string
	warmup, warmupOK := singlePhase(out.Phases, PhaseWarmup)
	measured, measuredOK := singlePhase(out.Phases, PhaseMeasured)
	cooldown, cooldownOK := singlePhase(out.Phases, PhaseCooldown)
	if warmupOK && measuredOK && !warmup.FinishedAt.IsZero() && !measured.StartedAt.IsZero() && measured.StartedAt.Before(warmup.FinishedAt) {
		reasons = append(reasons, "measured phase overlaps warmup")
	}
	if cooldownOK && measuredOK && !cooldown.StartedAt.IsZero() && !measured.FinishedAt.IsZero() && cooldown.StartedAt.Before(measured.FinishedAt) {
		reasons = append(reasons, "measured phase overlaps cooldown")
	}
	finished := measuredOK && phaseFinished(measured)
	if finished {
		if out.Measured == nil || !out.Measured.StartedAt.Equal(measured.StartedAt) || !out.Measured.FinishedAt.Equal(measured.FinishedAt) {
			reasons = append(reasons, "measured interval does not match the measured phase")
		}
		return reasons
	}
	if out.Measured != nil {
		reasons = append(reasons, "measured interval does not match the measured phase")
	}
	return reasons
}

func measuredCompletionProblems(out Outcome) []string {
	if !out.Executed || out.Partial || out.Termination != TerminationDurationElapsed {
		return nil
	}
	measuredPhases := phasesNamed(out.Phases, PhaseMeasured)
	if len(measuredPhases) == 0 {
		return []string{"measured phase is missing"}
	}
	if len(measuredPhases) != 1 {
		return nil
	}
	measured := measuredPhases[0]
	if !phaseFinished(measured) {
		return []string{"measured phase did not finish"}
	}
	if out.Measured == nil {
		return nil
	}
	if measured.Clock == ClockCollectorLocal && out.Measured.Clock == ClockCollectorLocal {
		return nil
	}
	if measured.Clock == out.Measured.Clock && (measured.Clock == ClockRunner || measured.Clock == ClockSource) &&
		out.ClockMapping.Established() && out.ClockMapping.From == measured.Clock {
		return nil
	}
	return []string{"measured phase clock is not collector_local and has no proven mapping"}
}

func phaseFinished(phase PhaseActual) bool {
	return !phase.StartedAt.IsZero() && !phase.FinishedAt.IsZero() && !phase.FinishedAt.Before(phase.StartedAt)
}

func singlePhase(phases []PhaseActual, name PhaseName) (PhaseActual, bool) {
	found := phasesNamed(phases, name)
	if len(found) != 1 {
		return PhaseActual{}, false
	}
	return found[0], true
}

func phasesNamed(phases []PhaseActual, name PhaseName) []PhaseActual {
	var found []PhaseActual
	for _, phase := range phases {
		if phase.Name == name {
			found = append(found, phase)
		}
	}
	return found
}

func findPhase(phases []PhaseActual, name PhaseName) (PhaseActual, bool) {
	for _, phase := range phases {
		if phase.Name == name {
			return phase, true
		}
	}
	return PhaseActual{}, false
}
