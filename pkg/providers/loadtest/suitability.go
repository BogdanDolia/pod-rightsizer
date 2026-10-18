package loadtest

import (
	"fmt"
	"time"
)

// AssessSuitability reports whether an outcome is usable as a successful
// measurement. Numeric SLO failures are not suitability failures. A missing
// required signal, unknown or incompatible latency semantics, failed
// application checks, and confirmed generator saturation are.
func AssessSuitability(req Request, spec AdapterSpec, out Outcome) Suitability {
	if !out.Executed {
		return Suitability{Reasons: []string{"load did not start"}}
	}
	var reasons []string
	if out.Partial || out.Termination != TerminationDurationElapsed {
		reasons = append(reasons, "load did not finish successfully")
	}
	reasons = append(reasons, signalReasons(req, out)...)
	reasons = append(reasons, latencyReasons(req, spec, out)...)
	reasons = append(reasons, checkReasons(out)...)
	reasons = append(reasons, saturationReasons(out)...)
	reasons = append(reasons, phaseReasons(req, out)...)
	if len(reasons) > 0 {
		return Suitability{Reasons: reasons}
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
	var reasons []string
	if err := measuredBounds(out); err != nil {
		reasons = append(reasons, err.Error())
	}
	for _, planned := range req.Phases {
		if planned.Name != PhaseWarmup && planned.Name != PhaseCooldown {
			continue
		}
		actual, ok := findPhase(out.Phases, planned.Name)
		if !ok || actual.StartedAt.IsZero() || actual.FinishedAt.IsZero() {
			reasons = append(reasons, fmt.Sprintf("planned %s phase did not run", planned.Name))
		}
	}
	if containsCapability(requiredCapabilities(req), CapabilityMeasuredPhase) &&
		out.Termination == TerminationDurationElapsed &&
		(out.Measured == nil || out.Measured.StartedAt.IsZero() || out.Measured.FinishedAt.Before(out.Measured.StartedAt)) {
		reasons = append(reasons, "measured interval is missing")
	}
	return reasons
}

func measuredBounds(out Outcome) error {
	var warmupFinish, measuredStart, measuredFinish, cooldownStart time.Time
	var warmupSet, measuredStartSet, measuredFinishSet, cooldownSet bool
	for _, phase := range out.Phases {
		switch phase.Name {
		case PhaseWarmup:
			warmupFinish = phase.FinishedAt
			warmupSet = !phase.FinishedAt.IsZero()
		case PhaseMeasured:
			measuredStart = phase.StartedAt
			measuredStartSet = !phase.StartedAt.IsZero()
			measuredFinish = phase.FinishedAt
			measuredFinishSet = !phase.FinishedAt.IsZero()
		case PhaseCooldown:
			cooldownStart = phase.StartedAt
			cooldownSet = !phase.StartedAt.IsZero()
		}
	}
	if warmupSet && measuredStartSet && measuredStart.Before(warmupFinish) {
		return fmt.Errorf("measured phase overlaps warmup")
	}
	if cooldownSet && measuredFinishSet && cooldownStart.Before(measuredFinish) {
		return fmt.Errorf("measured phase overlaps cooldown")
	}
	if out.Measured != nil && measuredStartSet && !out.Measured.StartedAt.Equal(measuredStart) {
		return fmt.Errorf("measured interval does not match the measured phase")
	}
	if out.Measured != nil && measuredFinishSet && !out.Measured.FinishedAt.Equal(measuredFinish) {
		return fmt.Errorf("measured interval does not match the measured phase")
	}
	return nil
}

func findPhase(phases []PhaseActual, name PhaseName) (PhaseActual, bool) {
	for _, phase := range phases {
		if phase.Name == name {
			return phase, true
		}
	}
	return PhaseActual{}, false
}

func containsCapability(capabilities []Capability, capability Capability) bool {
	for _, candidate := range capabilities {
		if candidate == capability {
			return true
		}
	}
	return false
}
