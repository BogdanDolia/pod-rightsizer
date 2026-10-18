package loadtest

import (
	"time"
)

// FactState is how an identity field was obtained. A name, URL, IP, label
// match, or HTTP status is not a known UID. not_obtained means the read did
// not happen. unknown means the fact was not established. Neither value is a
// substitute UID, and neither is promoted to known.
type FactState string

const (
	FactKnown       FactState = "known"
	FactUnknown     FactState = "unknown"
	FactNotObtained FactState = "not_obtained"
)

// StringFact is one identity string. Value is set only when State is known.
type StringFact struct {
	State FactState
	Value string
}

// IntFact is one integer identity. Value is set only when State is known.
type IntFact struct {
	State FactState
	Value int
}

// KnownString returns a fact that was read.
func KnownString(value string) StringFact {
	return StringFact{State: FactKnown, Value: value}
}

// UnknownString means the fact was not established.
func UnknownString() StringFact {
	return StringFact{State: FactUnknown}
}

// NotObtainedString means the read did not happen.
func NotObtainedString() StringFact {
	return StringFact{State: FactNotObtained}
}

// KnownInt returns an integer that was read. Zero can be a real count.
func KnownInt(value int) IntFact {
	return IntFact{State: FactKnown, Value: value}
}

// NotObtainedInt means the integer was not read.
func NotObtainedInt() IntFact {
	return IntFact{State: FactNotObtained}
}

// Known reports whether value was read and is non-empty.
func (f StringFact) Known() bool {
	return f.State == FactKnown && f.Value != ""
}

// RouteMode is the network path of the load. These modes are different
// experiment conditions. Unknown is not a match for any of them.
type RouteMode string

const (
	RouteDirectPod   RouteMode = "direct_pod"
	RouteService     RouteMode = "service"
	RouteIngress     RouteMode = "ingress"
	RoutePortForward RouteMode = "port_forward"
	RouteUnknown     RouteMode = "unknown"
)

// Known reports whether mode is one of the contract values, including unknown.
func (m RouteMode) Known() bool {
	switch m {
	case RouteDirectPod, RouteService, RouteIngress, RoutePortForward, RouteUnknown:
		return true
	default:
		return false
	}
}

// RunnerPlacement is where the load process runs. external includes a local
// controller. It is not in_cluster.
type RunnerPlacement string

const (
	PlacementInCluster RunnerPlacement = "in_cluster"
	PlacementExternal  RunnerPlacement = "external"
	PlacementUnknown   RunnerPlacement = "unknown"
)

// ProofMethod is how a route was checked. endpoint_slice proves a Service
// route only. direct_pod is a separate proof and does not require a Service.
type ProofMethod string

const (
	ProofDirectPod     ProofMethod = "direct_pod"
	ProofEndpointSlice ProofMethod = "endpoint_slice"
	ProofDeclaredOnly  ProofMethod = "declared_only"
	ProofNone          ProofMethod = "none"
)

// Assurance is a route or proof claim. declared and unknown are not verified.
type Assurance string

const (
	AssuranceVerified   Assurance = "verified"
	AssuranceUnverified Assurance = "unverified"
	AssuranceDeclared   Assurance = "declared"
	AssuranceMismatch   Assurance = "mismatch"
	AssuranceUnknown    Assurance = "unknown"
)

// Knowledge is a condition that may be known or unknown. Unknown is not
// known_absent. A Pod IP does not make a mesh known_absent.
type Knowledge string

const (
	KnowledgeKnownPresent Knowledge = "known_present"
	KnowledgeKnownAbsent  Knowledge = "known_absent"
	KnowledgeUnknown      Knowledge = "unknown"
)

// Relation compares the runner and the target. Unknown is not same.
type Relation string

const (
	RelationSame      Relation = "same"
	RelationDifferent Relation = "different"
	RelationUnknown   Relation = "unknown"
)

// Runner fact sources. A Job template is not an observation.
const (
	RunnerSourceObserved    = "observed"
	RunnerSourceTemplate    = "job_template"
	RunnerSourceInProcess   = "in_process"
	RunnerSourceNotObtained = "not_obtained"
)

// Scope kinds for primary resource inputs.
const (
	ScopeSelectedPod         = "selected_pod"
	ScopeDeploymentAggregate = "deployment_aggregate"
	ScopeDiagnostic          = "diagnostic"
)

// Scope statuses. aggregate_only cannot be verified selected_pod.
const (
	ScopeStatusVerified      = "verified"
	ScopeStatusUnverified    = "unverified"
	ScopeStatusAggregateOnly = "aggregate_only"
	ScopeStatusMismatch      = "mismatch"
	ScopeStatusUnknown       = "unknown"
)

// Attribution of a metrics sample.
const (
	AttributionPerPod        = "per_pod"
	AttributionAggregateOnly = "aggregate_only"
	AttributionUnknown       = "unknown"
)

// ClusterRef names a cluster without credentials. There is no kubeconfig,
// token, or certificate field.
type ClusterRef struct {
	Name    string
	Context string
}

// RequestedTarget is the workload the caller named. It is not the object
// that was read.
type RequestedTarget struct {
	Cluster       ClusterRef
	Namespace     string
	Deployment    string
	Pod           string
	Container     string
	Port          int
	HTTPHost      string
	TLSServerName string
}

// RequestedRoute is the path the caller asked for. ServiceName is application
// context. direct_pod does not require it.
type RequestedRoute struct {
	Mode        RouteMode
	ServiceName string
	IngressName string
}

// RunnerResources are CPU millicores and memory bytes. Known false means the
// numbers were not observed. A zero Known value is not a measurement.
type RunnerResources struct {
	CPUMilli    int
	MemoryBytes int64
	Known       bool
}

// RequestedRunner is the placement the caller asked for. ImageRef and the
// other fields are the request, not proof the runner started.
type RequestedRunner struct {
	Placement RunnerPlacement
	Namespace string
	ImageRef  string
	Resources RunnerResources
	NodeClass string
	Zone      string
}

// DialAddress is the address a client connects to. HTTPHost and TLSServerName
// are application identity. They are not the dial address, and clearing TLS
// verification is not a way to supply them.
type DialAddress struct {
	State         FactState
	IP            string
	Port          int
	HTTPHost      string
	TLSServerName string
}

// Known reports whether IP and port were read.
func (d DialAddress) Known() bool {
	return d.State == FactKnown && d.IP != "" && d.Port > 0
}

// SameNetwork reports whether two known dials use the same IP and port.
// Host and TLS server name are not compared here.
func (d DialAddress) SameNetwork(other DialAddress) bool {
	return d.Known() && other.Known() && d.IP == other.IP && d.Port == other.Port
}

// ObservedTarget is the workload that was read. Empty UID fields are not
// filled from the name, the IP, or a fixture body.
type ObservedTarget struct {
	DeploymentName string
	DeploymentUID  StringFact
	PodName        string
	PodUID         StringFact
	PodIP          StringFact
	Container      StringFact
	Image          StringFact
	RestartCount   IntFact
	Dial           DialAddress
}

// TargetChange is one identity difference inside a run. A different Pod UID
// on another run is not this value.
type TargetChange struct {
	Kind   string
	Before string
	After  string
}

// BackendShift is a redirect or retry that selected another backend. It cannot
// be ignored because the final HTTP status was 200.
type BackendShift struct {
	Kind   string
	ToIP   string
	ToPort int
}

// PlacementConditions are material runner conditions. Same runner image does
// not make two runs comparable. ResourceContention unknown is not "no contention".
type PlacementConditions struct {
	NodeClass          StringFact
	Zone               StringFact
	TargetNode         Relation
	TargetZone         Relation
	ResourceContention Knowledge
}

// RunnerIdentity is the process that was observed. Source job_template means
// these fields were copied from a spec and were not seen.
type RunnerIdentity struct {
	Placement            RunnerPlacement
	Namespace            StringFact
	PodUID               StringFact
	JobUID               StringFact
	ImageDigest          StringFact
	Engine               ComponentIdentity
	Scenario             ScenarioRef
	Resources            RunnerResources
	PlacementRel         PlacementConditions
	Source               string
	Replacements         int
	Fallback             string
	MutatedTarget        bool
	MutatedReplicas      bool
	MutatedNetworkPolicy bool
}

// BoolClaim is an explicit boolean claim. Unset is not false.
type BoolClaim struct {
	Set   bool
	Value bool
}

// RouteProof binds the facts that support a route. HTTPStatus and RequestedURL
// are symptoms. They do not verify the route.
type RouteProof struct {
	Method           ProofMethod
	Status           Assurance
	CheckedAt        time.Time
	Clock            string
	WorkloadLinked   bool
	DeploymentUID    StringFact
	PodName          string
	PodUID           StringFact
	Container        StringFact
	Image            StringFact
	Dial             DialAddress
	UsedAddress      DialAddress
	Runner           RunnerIdentity
	Limitations      []string
	Changes          []TargetChange
	Shifts           []BackendShift
	TransparentProxy Knowledge
	MeshBypassed     BoolClaim
	HTTPStatus       int
	RequestedURL     string
	Checks           []ObservedTarget
}

// ObservedRoute is the path that was seen. It is not a copy of the request.
type ObservedRoute struct {
	Mode        RouteMode
	Status      Assurance
	Proof       RouteProof
	ServiceName string
}

// ObservedRunner separates the request from the runner that was seen.
type ObservedRunner struct {
	Requested RequestedRunner
	Identity  RunnerIdentity
}

// StopState separates a stop request, a confirmed stop, and an unknown stop.
type StopState string

const (
	StopNotRequested StopState = "not_requested"
	StopRequested    StopState = "requested"
	StopConfirmed    StopState = "confirmed"
	StopUnknown      StopState = "unknown"
)

// ControlChannel is the controller's link to the runner. lost does not mean
// the runner has stopped.
type ControlChannel string

const (
	ControlConnected ControlChannel = "connected"
	ControlLost      ControlChannel = "lost"
	ControlUnknown   ControlChannel = "unknown"
)

// StopReport is what the controller knows about stopping load. InProcess is
// true only for a generator in the caller process. It is not remote confirmation.
type StopReport struct {
	State     StopState
	Channel   ControlChannel
	InProcess bool
}

// ClockMapping relates two clock domains. MethodKnown false or
// UncertaintyKnown false is not zero uncertainty.
type ClockMapping struct {
	From             string
	To               string
	Method           string
	MethodKnown      bool
	Uncertainty      time.Duration
	UncertaintyKnown bool
}

// Established reports whether both the method and the uncertainty were measured.
func (m ClockMapping) Established() bool {
	return m.MethodKnown && m.Method != "" && m.UncertaintyKnown && m.Uncertainty >= 0
}

// MetricsScopeReport is the primary resource scope attached to an outcome.
// ReplicaCount above 1 with one selected Pod is diagnostic for that Pod. It
// does not validate the Deployment. The export gate is T07 and T15.
type MetricsScopeReport struct {
	Kind                  string
	Status                string
	PodUID                string
	Container             string
	Attribution           string
	Expected              int
	Observed              int
	ReplicaCount          int
	DeploymentWideClaim   bool
	IncludesRunner        bool
	IncludesOtherReplicas bool
	ClockProven           bool
}

// TransportDiagnostic keeps the observed symptom. Cause stays unknown unless
// CauseProven is set from a separate check. A reset is not classified as a
// load balancer or an application error by itself.
type TransportDiagnostic struct {
	Symptom     string
	Cause       string
	CauseProven bool
}

// ExperimentConditions are the T14 match inputs this package can compare.
// The comparison engine that reads two evidence packages is T14. PodUID is
// ephemeral and is not required to match.
type ExperimentConditions struct {
	RouteMode            RouteMode
	MetricsScope         string
	EngineID             string
	ScenarioID           string
	Latency              LatencySemantics
	RunnerCPUMilli       int
	RunnerMemoryBytes    int64
	RunnerResourcesKnown bool
	TargetNode           Relation
	TargetZone           Relation
	NodeClass            string
	Zone                 string
	PodUID               string
}

// ExperimentAssessment separates a usable selected-Pod measurement from a
// Deployment-wide validation. SLO is not either of these.
type ExperimentAssessment struct {
	MeasurementUsable   bool
	DeploymentValidated bool
	Reasons             []string
}

// LegacyRoute is the honest result of the temporary local adapter. It is not
// an in-cluster direct proof.
func LegacyRoute() ObservedRoute {
	return ObservedRoute{
		Mode:   RouteUnknown,
		Status: AssuranceUnverified,
		Proof: RouteProof{
			Method:           ProofNone,
			Status:           AssuranceUnverified,
			TransparentProxy: KnowledgeUnknown,
		},
	}
}

// LegacyRunner is the in-process adapter. Placement is external. Namespace,
// Pod UID, Job UID, and image digest were not obtained.
func LegacyRunner() ObservedRunner {
	return ObservedRunner{
		Identity: RunnerIdentity{
			Placement:   PlacementExternal,
			Source:      RunnerSourceInProcess,
			Namespace:   NotObtainedString(),
			PodUID:      NotObtainedString(),
			JobUID:      NotObtainedString(),
			ImageDigest: NotObtainedString(),
		},
	}
}

// LegacyStop is confirmed only because the generator ran in this process.
// A not-started run did not need a stop. Neither value confirms a remote runner.
func LegacyStop(out Outcome) StopReport {
	if out.Stop.State != "" {
		return out.Stop
	}
	if !out.Executed {
		return StopReport{State: StopNotRequested, Channel: ControlConnected, InProcess: true}
	}
	return StopReport{State: StopConfirmed, Channel: ControlConnected, InProcess: true}
}

// ApplyLegacyFacts fills an omitted route with the local adapter's real
// limits and recomputes suitability. It does not change a route the adapter
// already recorded, and it does not mark that route verified.
func ApplyLegacyFacts(req Request, spec AdapterSpec, out Outcome) Outcome {
	if out.Route.Mode == "" && out.Route.Status == "" && out.Route.Proof.Method == "" {
		out.Route = LegacyRoute()
	}
	if out.Runner.Identity.Placement == "" && out.Runner.Identity.Source == "" {
		out.Runner = LegacyRunner()
	}
	if out.Stop.State == "" {
		out.Stop = LegacyStop(out)
	}
	prior := out.Suitability.Reasons
	out.Suitability = AssessSuitability(req, spec, out)
	out.Suitability.Reasons = dedupe(append(append([]string(nil), prior...), out.Suitability.Reasons...))
	if len(out.Suitability.Reasons) > 0 {
		out.Suitability.Usable = false
	}
	return out
}

// MarkControlLost records a failed outcome whose remote stop is unknown.
// The local error is not rewritten into StopConfirmed.
func MarkControlLost(out Outcome) Outcome {
	out.Termination = TerminationControlLost
	out.Partial = true
	if out.Executed {
		out.Executed = true
	}
	out.Stop = StopReport{State: StopUnknown, Channel: ControlLost}
	if out.Suitability.Usable {
		out.Suitability.Usable = false
	}
	out.Suitability.Reasons = append(out.Suitability.Reasons,
		"control channel lost",
		"runner stop is unknown",
	)
	return out
}

// ResolvedAssurance returns the status the facts support. A declared, unknown,
// unverified, or mismatch status is returned unchanged. verified is kept only
// when the proof for that mode is complete.
func ResolvedAssurance(route ObservedRoute) Assurance {
	if route.Status != AssuranceVerified {
		return route.Status
	}
	out := Outcome{Route: route, Runner: ObservedRunner{Identity: route.Proof.Runner}}
	switch route.Mode {
	case RouteDirectPod:
		if len(DirectProofGaps(out)) == 0 {
			return AssuranceVerified
		}
	case RouteService:
		if len(ServiceProofGaps(out)) == 0 {
			return AssuranceVerified
		}
	}
	return AssuranceUnverified
}

// DirectProofGaps reports why a direct_pod proof is not verified. An empty
// result does not mean a live cluster was checked. T05 and T10 perform that
// check. HTTP status and the requested URL are ignored on purpose.
func DirectProofGaps(out Outcome) []string {
	proof := out.Route.Proof
	var reasons []string
	if out.Route.Mode != RouteDirectPod {
		reasons = append(reasons, "route mode is not direct_pod")
	}
	if proof.Method != ProofDirectPod {
		reasons = append(reasons, "direct route proof method is not direct_pod")
	}
	if proof.Method == ProofEndpointSlice {
		reasons = append(reasons, "endpoint slice does not prove a direct pod route")
	}
	if !proof.WorkloadLinked {
		reasons = append(reasons, "workload identity was not checked")
	}
	if !uidKnown(proof.DeploymentUID, "") {
		reasons = append(reasons, "deployment uid is not a known uid")
	}
	if !uidKnown(proof.PodUID, proof.PodName) {
		reasons = append(reasons, "pod uid is not a known uid")
	}
	if !proof.Container.Known() {
		reasons = append(reasons, "container identity is not known")
	}
	if !proof.Image.Known() {
		reasons = append(reasons, "image identity is not known")
	}
	if !proof.Dial.Known() || !proof.UsedAddress.Known() {
		reasons = append(reasons, "dial address is not known")
	} else if !proof.Dial.SameNetwork(proof.UsedAddress) {
		reasons = append(reasons, "used dial address does not match the fixed pod address")
	}
	if proof.CheckedAt.IsZero() || proof.Clock == "" || proof.Clock == ClockUnknown {
		reasons = append(reasons, "proof check time is not known")
	}
	if len(proof.Changes) > 0 {
		reasons = append(reasons, "target changed during the run")
	}
	if len(proof.Shifts) > 0 {
		reasons = append(reasons, "backend changed after a redirect or retry")
	}
	reasons = append(reasons, runnerObservationGaps(proof.Runner)...)
	if proof.MeshBypassed.Set && proof.MeshBypassed.Value && proof.TransparentProxy != KnowledgeKnownAbsent {
		reasons = append(reasons, "pod ip does not prove a mesh bypass")
	}
	if proof.TransparentProxy == "" {
		reasons = append(reasons, "transparent proxy condition is omitted")
	}
	reasons = append(reasons, driftReasons(proof.Checks)...)
	return reasons
}

// ServiceProofGaps reports why a Service route is not verified. direct_pod
// proof does not satisfy this branch.
func ServiceProofGaps(out Outcome) []string {
	proof := out.Route.Proof
	var reasons []string
	if out.Route.Mode != RouteService {
		reasons = append(reasons, "route mode is not service")
	}
	if proof.Method != ProofEndpointSlice {
		reasons = append(reasons, "service route is verified only by endpoint slice")
	}
	if proof.Method == ProofDirectPod {
		reasons = append(reasons, "direct pod proof does not verify a service route")
	}
	if !proof.WorkloadLinked {
		reasons = append(reasons, "service membership was not checked")
	}
	if out.Route.ServiceName == "" {
		reasons = append(reasons, "service name was not read")
	}
	return reasons
}

// ExperimentFacts are the target, route, runner, clock, and scope an adapter
// records before Finish. They are observations, not the request.
type ExperimentFacts struct {
	Target       ObservedTarget
	Route        ObservedRoute
	Runner       ObservedRunner
	Stop         StopReport
	ClockMapping ClockMapping
	Metrics      MetricsScopeReport
	Transport    TransportDiagnostic
}

func runnerObservationGaps(runner RunnerIdentity) []string {
	var reasons []string
	if runner.Placement != PlacementInCluster {
		reasons = append(reasons, "in-cluster runner was not observed")
	}
	if runner.Source != RunnerSourceObserved {
		reasons = append(reasons, "runner identity was not observed")
	}
	if runner.Source == RunnerSourceTemplate {
		reasons = append(reasons, "job template is not an observed runner")
	}
	if !uidKnown(runner.PodUID, "") || !uidKnown(runner.JobUID, "") {
		reasons = append(reasons, "runner uid is not known")
	}
	if !runner.ImageDigest.Known() {
		reasons = append(reasons, "runner image digest is not known")
	}
	if runner.Engine.ID == "" || runner.Engine.Version == "" {
		reasons = append(reasons, "runner engine identity is not known")
	}
	if runner.Scenario.ID == "" || !runner.Scenario.ContentHashKnown {
		reasons = append(reasons, "runner scenario identity is not known")
	}
	if !runner.Resources.Known {
		reasons = append(reasons, "runner resources were not observed")
	}
	if runner.Replacements > 0 {
		reasons = append(reasons, "runner was replaced after start")
	}
	if runner.Fallback != "" {
		reasons = append(reasons, "load fell back to another route")
	}
	if runner.MutatedTarget || runner.MutatedReplicas || runner.MutatedNetworkPolicy {
		reasons = append(reasons, "runner mutated the application")
	}
	return reasons
}

func uidKnown(fact StringFact, name string) bool {
	if !fact.Known() {
		return false
	}
	if name != "" && fact.Value == name {
		return false
	}
	return true
}

// TargetChanges reports identity differences inside one run. The same name or
// IP with a new known UID is reuse by another Pod. Missing facts are not
// rewritten into a difference.
func TargetChanges(before, after ObservedTarget) []TargetChange {
	var changes []TargetChange
	if before.PodName != "" && before.PodName == after.PodName &&
		before.PodUID.Known() && after.PodUID.Known() && before.PodUID.Value != after.PodUID.Value {
		changes = append(changes, TargetChange{Kind: "name_reused", Before: before.PodUID.Value, After: after.PodUID.Value})
	}
	if before.PodIP.Known() && after.PodIP.Known() && before.PodIP.Value == after.PodIP.Value &&
		before.PodUID.Known() && after.PodUID.Known() && before.PodUID.Value != after.PodUID.Value {
		changes = append(changes, TargetChange{Kind: "ip_reused", Before: before.PodUID.Value, After: after.PodUID.Value})
	}
	if before.PodUID.Known() && after.PodUID.Known() && before.PodUID.Value != after.PodUID.Value {
		changes = append(changes, TargetChange{Kind: "pod_uid", Before: before.PodUID.Value, After: after.PodUID.Value})
	}
	if before.PodIP.Known() && after.PodIP.Known() && before.PodIP.Value != after.PodIP.Value {
		changes = append(changes, TargetChange{Kind: "pod_ip", Before: before.PodIP.Value, After: after.PodIP.Value})
	}
	if before.Container.Known() && after.Container.Known() && before.Container.Value != after.Container.Value {
		changes = append(changes, TargetChange{Kind: "container", Before: before.Container.Value, After: after.Container.Value})
	}
	if before.Image.Known() && after.Image.Known() && before.Image.Value != after.Image.Value {
		changes = append(changes, TargetChange{Kind: "image", Before: before.Image.Value, After: after.Image.Value})
	}
	if before.RestartCount.State == FactKnown && after.RestartCount.State == FactKnown &&
		after.RestartCount.Value > before.RestartCount.Value {
		changes = append(changes, TargetChange{Kind: "container_restart"})
	}
	if before.Dial.Known() && after.Dial.Known() && !before.Dial.SameNetwork(after.Dial) {
		changes = append(changes, TargetChange{Kind: "dial"})
	}
	return changes
}

func driftReasons(checks []ObservedTarget) []string {
	var reasons []string
	for i := 1; i < len(checks); i++ {
		changes := TargetChanges(checks[i-1], checks[i])
		if len(changes) > 0 {
			reasons = append(reasons, "target identity changed between checks")
			break
		}
	}
	return reasons
}

// WindowContained reports whether a source window lies entirely inside the
// measured interval after applying a known clock uncertainty. An unknown
// mapping is not treated as zero uncertainty. The measured interval is shrunk
// by the uncertainty on both sides. There is no universal sync allowance.
func WindowContained(sourceEnd time.Time, window time.Duration, measuredStart, measuredEnd time.Time, mapping ClockMapping) (bool, string) {
	if !mapping.Established() {
		return false, "clock mapping is unknown"
	}
	if window <= 0 || measuredStart.IsZero() || measuredEnd.IsZero() || !measuredEnd.After(measuredStart) {
		return false, "measured interval is not known"
	}
	start := measuredStart.Add(mapping.Uncertainty)
	end := measuredEnd.Add(-mapping.Uncertainty)
	if !end.After(start) {
		return false, "clock uncertainty covers the measured interval"
	}
	sourceStart := sourceEnd.Add(-window)
	if sourceStart.Before(start) || sourceEnd.After(end) {
		return false, "source window is not inside the measured interval after clock uncertainty"
	}
	return true, ""
}

// ConditionMismatches reports T14 inputs that do not match. An empty result
// is not a comparison verdict and does not read an evidence package. Ephemeral
// Pod UIDs are not compared. Unknown route mode does not match.
func ConditionMismatches(left, right ExperimentConditions) []string {
	var reasons []string
	if left.RouteMode == "" || right.RouteMode == "" || left.RouteMode == RouteUnknown || right.RouteMode == RouteUnknown || left.RouteMode != right.RouteMode {
		reasons = append(reasons, "route mode differs")
	}
	if left.MetricsScope == "" || right.MetricsScope == "" || left.MetricsScope != right.MetricsScope {
		reasons = append(reasons, "metrics scope differs")
	}
	if left.EngineID == "" || right.EngineID == "" || left.EngineID != right.EngineID {
		reasons = append(reasons, "engine differs")
	}
	if left.ScenarioID == "" || right.ScenarioID == "" || left.ScenarioID != right.ScenarioID {
		reasons = append(reasons, "scenario differs")
	}
	if !left.Latency.Known() || left.Latency == LatencyUnknown || left.Latency != right.Latency {
		reasons = append(reasons, "latency semantics differ")
	}
	if !left.RunnerResourcesKnown || !right.RunnerResourcesKnown ||
		left.RunnerCPUMilli != right.RunnerCPUMilli || left.RunnerMemoryBytes != right.RunnerMemoryBytes {
		reasons = append(reasons, "runner resources differ")
	}
	if left.TargetNode == "" || left.TargetNode == RelationUnknown || left.TargetNode != right.TargetNode ||
		left.TargetZone == "" || left.TargetZone == RelationUnknown || left.TargetZone != right.TargetZone ||
		left.NodeClass == "" || left.NodeClass != right.NodeClass ||
		left.Zone == "" || left.Zone != right.Zone {
		reasons = append(reasons, "runner placement differs")
	}
	return reasons
}

func presentedRouteProblems(out Outcome) []string {
	if routeOmitted(out) {
		return nil
	}
	var reasons []string
	if out.Route.Status == AssuranceVerified {
		switch out.Route.Mode {
		case RouteDirectPod:
			reasons = append(reasons, DirectProofGaps(out)...)
		case RouteService:
			reasons = append(reasons, ServiceProofGaps(out)...)
		default:
			reasons = append(reasons, "route mode cannot be verified")
		}
	}
	if out.Route.Mode == RouteDirectPod && out.Route.Proof.Method == ProofEndpointSlice {
		reasons = append(reasons, "endpoint slice does not prove a direct pod route")
	}
	if out.Stop.State == StopConfirmed && out.Stop.Channel == ControlLost {
		reasons = append(reasons, "confirmed stop contradicts a lost control channel")
	}
	if out.Transport.Symptom != "" && out.Transport.Cause != "" && out.Transport.Cause != "unknown" && !out.Transport.CauseProven {
		reasons = append(reasons, "transport cause is not proven")
	}
	return dedupe(reasons)
}

func strictDirectProblems(req Request, out Outcome) []string {
	if !req.StrictDirect {
		return nil
	}
	var reasons []string
	if !out.Executed {
		reasons = append(reasons, "strict direct experiment did not start")
		return reasons
	}
	if out.Route.Mode != RouteDirectPod || out.Route.Status != AssuranceVerified {
		reasons = append(reasons, "direct route is not verified")
	}
	reasons = append(reasons, DirectProofGaps(out)...)
	if out.Runner.Identity.Placement != PlacementInCluster || out.Runner.Identity.Source != RunnerSourceObserved {
		reasons = append(reasons, "in-cluster runner is not confirmed")
	}
	if out.Stop.InProcess {
		reasons = append(reasons, "in-process stop is not an in-cluster runner confirmation")
	}
	if out.Stop.State != StopConfirmed || out.Stop.Channel != ControlConnected {
		reasons = append(reasons, "runner stop is not confirmed")
	}
	if out.Stop.Channel == ControlLost || out.Termination == TerminationControlLost {
		reasons = append(reasons, "control channel lost")
	}
	if out.Metrics.Kind != ScopeSelectedPod || out.Metrics.Status != ScopeStatusVerified || out.Metrics.Attribution != AttributionPerPod {
		reasons = append(reasons, "metrics scope is not the selected pod")
	}
	if out.Metrics.IncludesRunner || out.Metrics.IncludesOtherReplicas {
		reasons = append(reasons, "metrics scope mixes other pods or the runner")
	}
	if out.Metrics.PodUID == "" || out.Metrics.PodUID != out.Route.Proof.PodUID.Value || out.Metrics.Container != out.Route.Proof.Container.Value {
		reasons = append(reasons, "metrics scope does not match the selected pod")
	}
	if out.Metrics.Expected <= 0 || out.Metrics.Observed != out.Metrics.Expected {
		reasons = append(reasons, "selected pod coverage is incomplete")
	}
	if !out.Metrics.ClockProven || !out.ClockMapping.Established() || out.ClockMapping.From != ClockRunner {
		reasons = append(reasons, "measured interval is not proven")
	}
	if out.Measured == nil || out.Measured.Clock != ClockRunner {
		reasons = append(reasons, "strict direct experiment requires runner phase clocks")
	}
	if out.Partial || out.Termination != TerminationDurationElapsed {
		reasons = append(reasons, "strict direct outcome is incomplete")
	}
	return dedupe(reasons)
}

// AssessExperiment reports measurement usability and whether that measurement
// validates the whole Deployment. One Pod of several replicas can be a usable
// diagnostic of that Pod and still leave DeploymentValidated false.
func AssessExperiment(req Request, spec AdapterSpec, out Outcome) ExperimentAssessment {
	suitability := AssessSuitability(req, spec, out)
	assessment := ExperimentAssessment{
		MeasurementUsable: suitability.Usable,
		Reasons:           append([]string(nil), suitability.Reasons...),
	}
	if !req.StrictDirect {
		return assessment
	}
	if out.Metrics.ReplicaCount == 1 && suitability.Usable && out.Metrics.Kind == ScopeSelectedPod && out.Metrics.Status == ScopeStatusVerified {
		assessment.DeploymentValidated = true
	}
	if out.Metrics.ReplicaCount > 1 {
		assessment.DeploymentValidated = false
		assessment.Reasons = append(assessment.Reasons, "single pod measurement does not validate the deployment")
		if out.Metrics.DeploymentWideClaim {
			assessment.MeasurementUsable = false
			assessment.Reasons = append(assessment.Reasons, "deployment-wide claim is not supported")
		}
	}
	if out.Metrics.ReplicaCount <= 0 && req.StrictDirect {
		assessment.DeploymentValidated = false
	}
	assessment.Reasons = dedupe(assessment.Reasons)
	return assessment
}

func routeOmitted(out Outcome) bool {
	return out.Route.Mode == "" && out.Route.Status == "" && out.Route.Proof.Method == "" &&
		out.Runner.Identity.Placement == "" && out.Stop.State == "" && out.Transport.Symptom == ""
}

func dedupe(reasons []string) []string {
	if len(reasons) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(reasons))
	out := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if reason == "" {
			continue
		}
		if _, ok := seen[reason]; ok {
			continue
		}
		seen[reason] = struct{}{}
		out = append(out, reason)
	}
	return out
}
