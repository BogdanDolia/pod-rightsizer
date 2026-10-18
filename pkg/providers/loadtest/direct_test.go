package loadtest

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestDirectPodContract(t *testing.T) {
	t.Run("consistent strict outcome", func(t *testing.T) {
		req, out := strictSuccess()
		assessment := AssessExperiment(req, directSpec(), out)
		if !assessment.MeasurementUsable || !assessment.DeploymentValidated {
			t.Fatalf("assessment = %#v", assessment)
		}
		if ResolvedAssurance(out.Route) != AssuranceVerified {
			t.Fatalf("assurance = %s", ResolvedAssurance(out.Route))
		}
	})

	t.Run("session keeps runner clock", func(t *testing.T) {
		adapter := newDirectFake(t)
		req := strictRequest(adapter.spec)
		var clocks []string
		req.OnPhase = func(event PhaseEvent) { clocks = append(clocks, event.Clock) }
		adapter.behavior = func(_ context.Context, session *Session) (TerminationReason, error) {
			start := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
			finish := start.Add(time.Second)
			if err := session.StartPhaseAt(PhaseMeasured, start, ClockRunner); err != nil {
				return TerminationInternalError, err
			}
			setMeasured(session)
			if err := session.FinishPhaseAt(PhaseMeasured, finish, ClockRunner); err != nil {
				return TerminationInternalError, err
			}
			session.SetExperiment(strictFacts(start, finish))
			return TerminationDurationElapsed, nil
		}
		outcome, err := adapter.Run(context.Background(), req)
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if !outcome.Suitability.Usable || outcome.Measured == nil || outcome.Measured.Clock != ClockRunner {
			t.Fatalf("outcome clock = %#v suitability %#v", outcome.Measured, outcome.Suitability)
		}
		if len(clocks) != 2 || clocks[0] != ClockRunner || clocks[1] != ClockRunner {
			t.Fatalf("delivered clocks = %v", clocks)
		}
	})

	t.Run("service proof stays on the service branch", func(t *testing.T) {
		out := serviceOutcome()
		if len(ServiceProofGaps(out)) != 0 {
			t.Fatalf("service gaps = %v", ServiceProofGaps(out))
		}
		if len(DirectProofGaps(out)) == 0 {
			t.Fatal("service proof verified a direct route")
		}
		if ResolvedAssurance(out.Route) != AssuranceVerified {
			t.Fatalf("service assurance = %s", ResolvedAssurance(out.Route))
		}
	})

	t.Run("http success is not proof", func(t *testing.T) {
		req, out := strictSuccess()
		out.Route.Proof.Method = ProofNone
		out.Route.Proof.HTTPStatus = 200
		out.Route.Proof.RequestedURL = req.TargetURL
		out.Route.Proof.UsedAddress = out.Route.Proof.Dial
		if ResolvedAssurance(out.Route) == AssuranceVerified || AssessSuitability(req, directSpec(), out).Usable {
			t.Fatalf("http success became verified: %#v", AssessSuitability(req, directSpec(), out))
		}
	})

	t.Run("declared proof is not promoted", func(t *testing.T) {
		_, out := strictSuccess()
		out.Route.Status = AssuranceDeclared
		out.Route.Proof.Status = AssuranceDeclared
		if ResolvedAssurance(out.Route) != AssuranceDeclared {
			t.Fatalf("declared became %s", ResolvedAssurance(out.Route))
		}
	})

	t.Run("endpoint slice does not verify direct", func(t *testing.T) {
		req, out := strictSuccess()
		out.Route.Proof.Method = ProofEndpointSlice
		if ResolvedAssurance(out.Route) == AssuranceVerified {
			t.Fatal("endpoint slice verified direct_pod")
		}
		reasons := strings.Join(AssessSuitability(req, directSpec(), out).Reasons, " ")
		if !strings.Contains(reasons, "endpoint slice") {
			t.Fatalf("reasons = %s", reasons)
		}
	})

	t.Run("unknown runner and foreign dial", func(t *testing.T) {
		req, out := strictSuccess()
		out.Runner.Identity.Placement = PlacementUnknown
		out.Route.Proof.Runner.Placement = PlacementUnknown
		out.Route.Proof.UsedAddress.IP = "10.9.9.9"
		reasons := strings.Join(AssessSuitability(req, directSpec(), out).Reasons, " ")
		if !strings.Contains(reasons, "in-cluster runner") || !strings.Contains(reasons, "dial address") {
			t.Fatalf("reasons = %s", reasons)
		}
	})

	t.Run("redirect changes backend", func(t *testing.T) {
		req, out := strictSuccess()
		out.Route.Proof.Shifts = []BackendShift{{Kind: "redirect", ToIP: "10.1.1.8", ToPort: 8080}}
		if AssessSuitability(req, directSpec(), out).Usable {
			t.Fatal("redirect stayed usable")
		}
	})

	t.Run("name and ip reuse", func(t *testing.T) {
		before := ObservedTarget{PodName: "api-0", PodUID: KnownString("uid-a"), PodIP: KnownString("10.0.0.8")}
		renamed := ObservedTarget{PodName: "api-0", PodUID: KnownString("uid-b"), PodIP: KnownString("10.0.0.9")}
		reusedIP := ObservedTarget{PodName: "api-1", PodUID: KnownString("uid-c"), PodIP: KnownString("10.0.0.8")}
		if !hasChange(TargetChanges(before, renamed), "name_reused") {
			t.Fatalf("name reuse = %#v", TargetChanges(before, renamed))
		}
		if !hasChange(TargetChanges(before, reusedIP), "ip_reused") {
			t.Fatalf("ip reuse = %#v", TargetChanges(before, reusedIP))
		}
		req, out := strictSuccess()
		out.Route.Proof.Checks = []ObservedTarget{before, renamed}
		if AssessSuitability(req, directSpec(), out).Usable {
			t.Fatal("changed identity stayed usable")
		}
	})

	t.Run("foreign and mixed metrics", func(t *testing.T) {
		req, out := strictSuccess()
		out.Metrics.PodUID = "other-pod"
		out.Metrics.IncludesOtherReplicas = true
		out.Metrics.IncludesRunner = true
		reasons := strings.Join(AssessSuitability(req, directSpec(), out).Reasons, " ")
		if !strings.Contains(reasons, "selected pod") || !strings.Contains(reasons, "mixes") {
			t.Fatalf("reasons = %s", reasons)
		}
	})

	t.Run("unknown clock mapping", func(t *testing.T) {
		req, out := strictSuccess()
		out.ClockMapping.MethodKnown = false
		out.Metrics.ClockProven = false
		start := out.Measured.StartedAt
		end := out.Measured.FinishedAt
		ok, why := WindowContained(end, time.Second, start, end, out.ClockMapping)
		if ok || why != "clock mapping is unknown" {
			t.Fatalf("unknown mapping = %v %s", ok, why)
		}
		if AssessSuitability(req, directSpec(), out).Usable {
			t.Fatal("unknown mapping stayed usable")
		}
	})

	t.Run("boundary window needs the uncertainty", func(t *testing.T) {
		start := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
		end := start.Add(10 * time.Second)
		mapping := ClockMapping{From: ClockRunner, To: ClockSource, Method: "bounded_offset", MethodKnown: true, Uncertainty: time.Second, UncertaintyKnown: true}
		sourceEnd := start.Add(10 * time.Second)
		ok, _ := WindowContained(sourceEnd, 10*time.Second, start, end, mapping)
		if ok {
			t.Fatal("boundary window was accepted by ignoring uncertainty")
		}
		ok, why := WindowContained(start.Add(9*time.Second), 8*time.Second, start, end, mapping)
		if !ok {
			t.Fatalf("contained window rejected: %s", why)
		}
	})

	t.Run("cancellation invents no proof", func(t *testing.T) {
		adapter := newDirectFake(t)
		req := strictRequest(adapter.spec)
		adapter.behavior = func(_ context.Context, session *Session) (TerminationReason, error) {
			return TerminationContextCanceled, context.Canceled
		}
		outcome, err := adapter.Run(context.Background(), req)
		if err == nil || outcome.Executed || outcome.Route.Proof.Method == ProofDirectPod || outcome.Target.PodUID.Known() {
			t.Fatalf("cancel invented proof: err %v route %#v target %#v", err, outcome.Route, outcome.Target)
		}
		adapter.behavior = func(_ context.Context, session *Session) (TerminationReason, error) {
			if err := session.StartPhase(PhaseMeasured); err != nil {
				return TerminationInternalError, err
			}
			setMeasured(session)
			return TerminationContextCanceled, context.Canceled
		}
		outcome, err = adapter.Run(context.Background(), req)
		if err == nil || !outcome.Executed || outcome.Suitability.Usable || outcome.Route.Status == AssuranceVerified {
			t.Fatalf("partial cancel = err %v executed %v suitability %#v", err, outcome.Executed, outcome.Suitability)
		}
	})

	t.Run("control loss is not a confirmed stop", func(t *testing.T) {
		_, out := strictSuccess()
		out = MarkControlLost(out)
		if out.Stop.State == StopConfirmed || out.Termination != TerminationControlLost || out.Suitability.Usable {
			t.Fatalf("control loss = %#v termination %s", out.Stop, out.Termination)
		}
		reasons := strings.Join(AssessSuitability(strictRequest(directSpec()), directSpec(), out).Reasons, " ")
		if !strings.Contains(reasons, "runner stop is not confirmed") && !strings.Contains(reasons, "runner stop is unknown") {
			t.Fatalf("reasons = %s", reasons)
		}
	})

	t.Run("one pod does not validate many replicas", func(t *testing.T) {
		req, out := strictSuccess()
		out.Metrics.ReplicaCount = 3
		out.Metrics.DeploymentWideClaim = false
		assessment := AssessExperiment(req, directSpec(), out)
		if !assessment.MeasurementUsable || assessment.DeploymentValidated {
			t.Fatalf("diagnostic = %#v", assessment)
		}
		out.Metrics.DeploymentWideClaim = true
		assessment = AssessExperiment(req, directSpec(), out)
		if assessment.MeasurementUsable || assessment.DeploymentValidated {
			t.Fatalf("wide claim = %#v", assessment)
		}
	})

	t.Run("comparability ignores ephemeral pod uid", func(t *testing.T) {
		left := comparableConditions("direct_pod", "uid-a")
		right := comparableConditions("direct_pod", "uid-b")
		if mismatches := ConditionMismatches(left, right); len(mismatches) != 0 {
			t.Fatalf("pod uid mismatch = %v", mismatches)
		}
		right.RouteMode = RouteService
		if !hasReason(ConditionMismatches(left, right), "route mode differs") {
			t.Fatal("service matched direct_pod")
		}
		right = comparableConditions(RouteUnknown, "uid-a")
		if !hasReason(ConditionMismatches(left, right), "route mode differs") {
			t.Fatal("unknown route matched")
		}
	})

	t.Run("transport cause stays unknown", func(t *testing.T) {
		req, out := strictSuccess()
		out.Transport = TransportDiagnostic{Symptom: "connection_reset", Cause: "load_balancer"}
		reasons := strings.Join(AssessSuitability(req, directSpec(), out).Reasons, " ")
		if !strings.Contains(reasons, "transport cause") {
			t.Fatalf("reasons = %s", reasons)
		}
	})
}

func newDirectFake(t *testing.T) *fakeAdapter {
	t.Helper()
	capabilities := append(successCapabilities(), CapabilityDirectRouteProof, CapabilityInClusterRunner, CapabilitySelectedPodMetrics)
	return newFake(t, "fixture-engine", "1", capabilities, LatencyUntilResponseHeaders)
}

func directSpec() AdapterSpec {
	return AdapterSpec{
		Adapter:          ComponentIdentity{ID: "fixture-adapter", Version: "1"},
		Engine:           ComponentIdentity{ID: "fixture-engine", Version: "1"},
		LatencySemantics: LatencyUntilResponseHeaders,
		ContractVersion:  ContractVersion,
		Capabilities: append(successCapabilities(),
			CapabilityDirectRouteProof,
			CapabilityInClusterRunner,
			CapabilitySelectedPodMetrics,
		),
	}
}

func strictRequest(spec AdapterSpec) Request {
	req := baseRequest(spec, nil)
	req.StrictDirect = true
	req.Target = RequestedTarget{
		Cluster:    ClusterRef{Name: "staging", Context: "staging"},
		Namespace:  "shop",
		Deployment: "orders",
		Pod:        "orders-0",
		Container:  "api",
		Port:       8080,
		HTTPHost:   "orders.internal",
	}
	req.Route = RequestedRoute{Mode: RouteDirectPod}
	req.Runner = RequestedRunner{Placement: PlacementInCluster}
	return req
}

func strictSuccess() (Request, Outcome) {
	spec := directSpec()
	req := strictRequest(spec)
	start := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	finish := start.Add(time.Second)
	facts := strictFacts(start, finish)
	out := Outcome{
		ContractVersion:    ContractVersion,
		Engine:             spec.Engine,
		Adapter:            spec.Adapter,
		Executed:           true,
		Termination:        TerminationDurationElapsed,
		HTTPRequests:       KnownCount(4),
		HTTPErrors:         KnownCount(0),
		HTTPErrorRate:      KnownRatio(0),
		ActualThroughput:   KnownRate(4),
		UsefulThroughput:   MissingRate(),
		DroppedWork:        MissingCount(),
		TransportErrors:    MissingCount(),
		GeneratorSaturated: MissingBool(),
		Latency: LatencyReport{
			Semantics: LatencyUntilResponseHeaders,
			P50:       KnownDuration(time.Millisecond),
			P95:       KnownDuration(2 * time.Millisecond),
			P99:       KnownDuration(3 * time.Millisecond),
		},
		Checks: Checks{State: SignalMissing},
		Phases: []PhaseActual{{
			Name:       PhaseMeasured,
			StartedAt:  start,
			FinishedAt: finish,
			Clock:      ClockRunner,
		}},
		Measured:     &Interval{StartedAt: start, FinishedAt: finish, Clock: ClockRunner},
		Target:       facts.Target,
		Route:        facts.Route,
		Runner:       facts.Runner,
		Stop:         facts.Stop,
		ClockMapping: facts.ClockMapping,
		Metrics:      facts.Metrics,
	}
	return req, out
}

func strictFacts(start, finish time.Time) ExperimentFacts {
	dial := DialAddress{State: FactKnown, IP: "10.0.0.8", Port: 8080, HTTPHost: "orders.internal"}
	runner := RunnerIdentity{
		Placement:   PlacementInCluster,
		Namespace:   KnownString("load"),
		PodUID:      KnownString("runner-uid"),
		JobUID:      KnownString("job-uid"),
		ImageDigest: KnownString("sha256:runner"),
		Engine:      ComponentIdentity{ID: "example-engine", Version: "1"},
		Scenario:    ScenarioRef{ID: "items-get", Version: "1", VersionKnown: true, ContentHash: "abc", ContentHashKnown: true},
		Resources:   RunnerResources{CPUMilli: 100, MemoryBytes: 128 << 20, Known: true},
		Source:      RunnerSourceObserved,
		PlacementRel: PlacementConditions{
			NodeClass:          KnownString("standard"),
			Zone:               KnownString("a"),
			TargetNode:         RelationDifferent,
			TargetZone:         RelationSame,
			ResourceContention: KnowledgeUnknown,
		},
	}
	return ExperimentFacts{
		Target: ObservedTarget{
			DeploymentName: "orders",
			DeploymentUID:  KnownString("deploy-uid"),
			PodName:        "orders-0",
			PodUID:         KnownString("pod-uid"),
			PodIP:          KnownString("10.0.0.8"),
			Container:      KnownString("api"),
			Image:          KnownString("sha256:app"),
			RestartCount:   KnownInt(0),
			Dial:           dial,
		},
		Route: ObservedRoute{
			Mode:   RouteDirectPod,
			Status: AssuranceVerified,
			Proof: RouteProof{
				Method:           ProofDirectPod,
				Status:           AssuranceVerified,
				CheckedAt:        start,
				Clock:            ClockRunner,
				WorkloadLinked:   true,
				DeploymentUID:    KnownString("deploy-uid"),
				PodName:          "orders-0",
				PodUID:           KnownString("pod-uid"),
				Container:        KnownString("api"),
				Image:            KnownString("sha256:app"),
				Dial:             dial,
				UsedAddress:      dial,
				Runner:           runner,
				TransparentProxy: KnowledgeUnknown,
			},
		},
		Runner: ObservedRunner{Identity: runner},
		Stop:   StopReport{State: StopConfirmed, Channel: ControlConnected},
		ClockMapping: ClockMapping{
			From:             ClockRunner,
			To:               ClockSource,
			Method:           "bounded_offset",
			MethodKnown:      true,
			Uncertainty:      time.Millisecond,
			UncertaintyKnown: true,
		},
		Metrics: MetricsScopeReport{
			Kind:         ScopeSelectedPod,
			Status:       ScopeStatusVerified,
			PodUID:       "pod-uid",
			Container:    "api",
			Attribution:  AttributionPerPod,
			Expected:     1,
			Observed:     1,
			ReplicaCount: 1,
			ClockProven:  true,
		},
	}
}

func serviceOutcome() Outcome {
	return Outcome{
		Route: ObservedRoute{
			Mode:        RouteService,
			Status:      AssuranceVerified,
			ServiceName: "orders",
			Proof: RouteProof{
				Method:         ProofEndpointSlice,
				Status:         AssuranceVerified,
				WorkloadLinked: true,
			},
		},
	}
}

func comparableConditions(mode RouteMode, podUID string) ExperimentConditions {
	return ExperimentConditions{
		RouteMode:            mode,
		MetricsScope:         ScopeSelectedPod,
		EngineID:             "example-engine",
		ScenarioID:           "items-get",
		Latency:              LatencyUntilResponseHeaders,
		RunnerCPUMilli:       100,
		RunnerMemoryBytes:    128 << 20,
		RunnerResourcesKnown: true,
		TargetNode:           RelationDifferent,
		TargetZone:           RelationSame,
		NodeClass:            "standard",
		Zone:                 "a",
		PodUID:               podUID,
	}
}

func hasChange(changes []TargetChange, kind string) bool {
	for _, change := range changes {
		if change.Kind == kind {
			return true
		}
	}
	return false
}

func hasReason(reasons []string, want string) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}
