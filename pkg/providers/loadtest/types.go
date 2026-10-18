package loadtest

import (
	"context"
	"time"
)

// ContractVersion is the adapter contract implemented by this package.
// It is not contracts/v1 schemaVersion.
const ContractVersion = "1"

// MaxPhaseDuration matches the v1 schema maximum of 24 hours.
const MaxPhaseDuration = 24 * time.Hour

// Capability is one thing an adapter claims it can measure or do.
// The closed list is the product vocabulary. Adapter ids are not closed.
type Capability string

const (
	CapabilityHTTPRequests         Capability = "http_requests"
	CapabilityHTTPErrors           Capability = "http_errors"
	CapabilityActualHTTPThroughput Capability = "actual_http_throughput"
	CapabilityLatencyUntilHeaders  Capability = "latency_until_response_headers"
	CapabilityMeasuredPhase        Capability = "measured_phase_boundaries"
	CapabilityWarmupCooldown       Capability = "warmup_cooldown_phases"
	CapabilityCancellation         Capability = "cancellation"
	CapabilityApplicationChecks    Capability = "application_checks"
	CapabilityUsefulThroughput     Capability = "useful_throughput"
	CapabilityDroppedWork          Capability = "dropped_work"
	CapabilityGeneratorSaturation  Capability = "generator_saturation"
	// CapabilityDirectRouteProof is the ability to record a route proof.
	// Claiming it does not make a requested URL or an HTTP 200 verified.
	CapabilityDirectRouteProof Capability = "direct_route_proof"
	// CapabilityInClusterRunner is an observed runner inside the cluster.
	// The temporary local process does not have this capability.
	CapabilityInClusterRunner Capability = "in_cluster_runner"
	// CapabilitySelectedPodMetrics is per-pod attribution for one container.
	// An aggregate Deployment reading does not provide it.
	CapabilitySelectedPodMetrics Capability = "selected_pod_metrics"
)

// Known reports whether capability is part of this contract's vocabulary.
func (c Capability) Known() bool {
	switch c {
	case CapabilityHTTPRequests,
		CapabilityHTTPErrors,
		CapabilityActualHTTPThroughput,
		CapabilityLatencyUntilHeaders,
		CapabilityMeasuredPhase,
		CapabilityWarmupCooldown,
		CapabilityCancellation,
		CapabilityApplicationChecks,
		CapabilityUsefulThroughput,
		CapabilityDroppedWork,
		CapabilityGeneratorSaturation,
		CapabilityDirectRouteProof,
		CapabilityInClusterRunner,
		CapabilitySelectedPodMetrics:
		return true
	default:
		return false
	}
}

// LatencySemantics names how a latency percentile was measured.
type LatencySemantics string

const (
	// LatencyUntilResponseHeaders stops when response headers are available.
	// For the legacy generator that is when http.Client.Do returns, before
	// the body is read. It is not full-response time and not k6 http_req_duration.
	LatencyUntilResponseHeaders LatencySemantics = "until_response_headers"
	// LatencyK6HTTPReqDuration is k6's http_req_duration metric.
	LatencyK6HTTPReqDuration LatencySemantics = "k6_http_req_duration"
	// LatencyNotApplicable is observe mode, where no load latency exists.
	LatencyNotApplicable LatencySemantics = "not_applicable"
	// LatencyUnknown means the definition was not established.
	// It is not a measured zero and not LatencyNotApplicable.
	LatencyUnknown LatencySemantics = "unknown"
)

// Known reports whether semantics is one of the contract values.
func (s LatencySemantics) Known() bool {
	switch s {
	case LatencyUntilResponseHeaders, LatencyK6HTTPReqDuration, LatencyNotApplicable, LatencyUnknown:
		return true
	default:
		return false
	}
}

// TerminationReason is why a run stopped. It is not an SLO verdict.
type TerminationReason string

const (
	TerminationDurationElapsed      TerminationReason = "duration_elapsed"
	TerminationContextCanceled      TerminationReason = "context_canceled"
	TerminationContextDeadline      TerminationReason = "context_deadline_exceeded"
	TerminationInvalidConfiguration TerminationReason = "invalid_configuration"
	TerminationInvalidTarget        TerminationReason = "invalid_target"
	TerminationInternalError        TerminationReason = "internal_error"
	TerminationNotStarted           TerminationReason = "not_started"
	// TerminationControlLost means the controller lost the runner channel.
	// It is not confirmation that the remote load has stopped.
	TerminationControlLost TerminationReason = "control_channel_lost"
)

// LoadShape is the requested arrival pattern. It is not a measured rate.
type LoadShape string

const (
	ShapeRPS         LoadShape = "rps"
	ShapeConcurrency LoadShape = "concurrency"
)

// PhaseName is one planned or actual load phase.
type PhaseName string

const (
	PhaseWarmup   PhaseName = "warmup"
	PhaseMeasured PhaseName = "measured"
	PhaseCooldown PhaseName = "cooldown"
)

// PhaseEdge is the start or finish of an actual phase.
type PhaseEdge string

const (
	EdgeStarted  PhaseEdge = "started"
	EdgeFinished PhaseEdge = "finished"
)

// Clock domains of an actual timestamp. A runner timestamp stays on the
// runner clock. It is not renamed to collector_local.
const (
	ClockCollectorLocal = "collector_local"
	ClockRunner         = "runner"
	ClockSource         = "source"
	ClockUnknown        = "unknown"
)

// SignalState distinguishes a measurement from an absent or unresolved signal.
type SignalState string

const (
	SignalKnown   SignalState = "known"
	SignalMissing SignalState = "missing_signal"
	SignalUnknown SignalState = "unknown"
)

// Count is an integer signal. A missing or unknown count is not zero.
type Count struct {
	State SignalState
	Value int
}

// Rate is a per-second signal. The unit is documented by the field that holds it.
type Rate struct {
	State SignalState
	Value float64
}

// Ratio is a fraction from 0 to 1 when known. It is not a percentage.
type Ratio struct {
	State SignalState
	Value float64
}

// DurationSignal is a latency or duration in nanoseconds when known.
type DurationSignal struct {
	State       SignalState
	Nanoseconds int64
}

// BoolSignal is a boolean fact. Missing and unknown are not false.
type BoolSignal struct {
	State SignalState
	Value bool
}

// KnownCount returns a measured count. Zero is a real measurement.
func KnownCount(value int) Count {
	return Count{State: SignalKnown, Value: value}
}

// MissingCount means the adapter does not report this count.
func MissingCount() Count {
	return Count{State: SignalMissing}
}

// UnknownCount means the count was not established. It is not a missing capability.
func UnknownCount() Count {
	return Count{State: SignalUnknown}
}

// KnownRate returns a measured per-second rate.
func KnownRate(value float64) Rate {
	return Rate{State: SignalKnown, Value: value}
}

// MissingRate means the adapter does not report this rate.
func MissingRate() Rate {
	return Rate{State: SignalMissing}
}

// KnownRatio returns a measured ratio in the range [0, 1].
func KnownRatio(value float64) Ratio {
	return Ratio{State: SignalKnown, Value: value}
}

// MissingRatio means the adapter does not report this ratio.
func MissingRatio() Ratio {
	return Ratio{State: SignalMissing}
}

// KnownDuration returns a measured duration.
func KnownDuration(value time.Duration) DurationSignal {
	return DurationSignal{State: SignalKnown, Nanoseconds: int64(value)}
}

// MissingDuration means the adapter does not report this duration.
func MissingDuration() DurationSignal {
	return DurationSignal{State: SignalMissing}
}

// KnownBool returns a measured boolean.
func KnownBool(value bool) BoolSignal {
	return BoolSignal{State: SignalKnown, Value: value}
}

// MissingBool means the adapter does not report this boolean.
func MissingBool() BoolSignal {
	return BoolSignal{State: SignalMissing}
}

// UnknownBool means the boolean was not established. It is not false.
func UnknownBool() BoolSignal {
	return BoolSignal{State: SignalUnknown}
}

// ComponentIdentity is an engine or adapter id and version.
type ComponentIdentity struct {
	ID      string
	Version string
}

// ScenarioRef identifies the scenario. Empty version or hash fields are
// unknown unless the matching Known flag is set. An empty hash is not the
// hash of empty content.
type ScenarioRef struct {
	ID               string
	Version          string
	VersionKnown     bool
	ContentHash      string
	ContentHashKnown bool
}

// PhasePlan is one requested phase. Duration is the plan, not the actual time.
type PhasePlan struct {
	Name     PhaseName
	Duration time.Duration
}

// PhaseEvent is one actual phase edge delivered to Request.OnPhase.
type PhaseEvent struct {
	Name  PhaseName
	Edge  PhaseEdge
	At    time.Time
	Clock string
}

// PhaseObserver receives phase edges. See the package comment for the
// delivery rules.
type PhaseObserver func(PhaseEvent)

// Request is the load a caller asked an adapter to run.
type Request struct {
	TargetURL         string
	Shape             LoadShape
	RequestedRPS      int
	Concurrency       int
	RequestedDuration time.Duration
	Phases            []PhasePlan
	Scenario          ScenarioRef
	Adapter           ComponentIdentity
	Required          []Capability
	LatencySemantics  LatencySemantics
	OnPhase           PhaseObserver
	Target            RequestedTarget
	Route             RequestedRoute
	Runner            RequestedRunner
	StrictDirect      bool
}

// RequestedLoad is the request copied onto the outcome. It is not overwritten
// with observed values.
type RequestedLoad struct {
	TargetURL        string
	Shape            LoadShape
	RPS              int
	Concurrency      int
	Duration         time.Duration
	Phases           []PhasePlan
	LatencySemantics LatencySemantics
	Required         []Capability
	Target           RequestedTarget
	Route            RequestedRoute
	Runner           RequestedRunner
	StrictDirect     bool
}

// PhaseActual is one phase that started. Planned is the requested duration
// when the plan had one. Clock is the domain of StartedAt and FinishedAt.
// The in-process session uses collector_local. A remote runner event keeps
// the runner domain.
type PhaseActual struct {
	Name         PhaseName
	Planned      time.Duration
	PlannedKnown bool
	StartedAt    time.Time
	FinishedAt   time.Time
	Clock        string
}

// Interval is an actual time span. It is not a Metrics API source window.
type Interval struct {
	StartedAt  time.Time
	FinishedAt time.Time
	Clock      string
}

// Checks is the application-check signal. HTTP 200 does not make Passed true.
// SignalMissing means the adapter did not evaluate checks. That is not a pass.
type Checks struct {
	State   SignalState
	Passed  bool
	Defined int
	Failed  int
}

// LatencyReport is the percentile signal and the definition of those percentiles.
type LatencyReport struct {
	Semantics LatencySemantics
	P50       DurationSignal
	P95       DurationSignal
	P99       DurationSignal
}

// Measurements are the numeric facts an adapter records for the measured phase.
// HTTPRequests counts HTTP attempts. It is not an iteration count and not a
// count of successful application operations.
type Measurements struct {
	HTTPRequests     Count
	HTTPErrors       Count
	HTTPErrorRate    Ratio
	ActualThroughput Rate
	TransportErrors  Count
	LatencyP50       DurationSignal
	LatencyP95       DurationSignal
	LatencyP99       DurationSignal
	StatusCodes      map[int]int
}

// Outcome is the typed result of one provider run.
type Outcome struct {
	ContractVersion string
	Engine          ComponentIdentity
	Adapter         ComponentIdentity
	Scenario        ScenarioRef
	Claimed         []Capability
	Requested       RequestedLoad

	Executed    bool
	Termination TerminationReason
	Partial     bool

	HTTPRequests       Count
	HTTPErrors         Count
	HTTPErrorRate      Ratio
	ActualThroughput   Rate
	UsefulThroughput   Rate
	DroppedWork        Count
	TransportErrors    Count
	GeneratorSaturated BoolSignal
	Latency            LatencyReport
	Checks             Checks
	StatusCodes        map[int]int

	Phases   []PhaseActual
	Measured *Interval

	Target       ObservedTarget
	Route        ObservedRoute
	Runner       ObservedRunner
	Stop         StopReport
	ClockMapping ClockMapping
	Metrics      MetricsScopeReport
	Transport    TransportDiagnostic

	Suitability Suitability
}

// Provider runs one load request.
//
// An in-process adapter stops that run's load before returning, and it may
// record StopConfirmed with InProcess set. A remote runner's return, including
// an error, does not prove the remote process stopped. Stop records whether
// stop was requested, confirmed, or unknown. A usable strict direct experiment
// requires a confirmed stop. A lost control channel leaves stop unknown and
// the outcome failed and unsuitable. The remote Job's own deadline bounds
// load after the controller is gone. T10 enforces that deadline. This package
// does not start a Job and does not treat a local error as remote confirmation.
type Provider interface {
	Run(ctx context.Context, req Request) (Outcome, error)
}

// SLO is the numeric gate for a measured phase.
// MaximumHTTPErrorRate is a ratio in [0, 1]. A zero MinimumRPS disables the
// throughput gate. A zero MaximumP95Latency disables the latency gate.
type SLO struct {
	MinimumRPS           float64
	MaximumHTTPErrorRate float64
	MaximumP95Latency    time.Duration
}

// SLOStatus is the numeric SLO verdict. NotEvaluated is not a pass.
type SLOStatus string

const (
	SLOPassed       SLOStatus = "passed"
	SLOFailed       SLOStatus = "failed"
	SLONotEvaluated SLOStatus = "not_evaluated"
)

// SLOAssessment is the numeric gate result. It does not decide suitability.
type SLOAssessment struct {
	Status     SLOStatus
	Passed     bool
	Violations []string
}

// Suitability says whether the outcome may be used as a successful measurement.
// It is separate from SLO math and from the error returned by Run.
type Suitability struct {
	Usable  bool
	Reasons []string
}
