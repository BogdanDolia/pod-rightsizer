package local

import (
	"context"
	"fmt"

	corelt "github.com/HelixThinker/pod-rightsizer/pkg/loadtest"
	contract "github.com/HelixThinker/pod-rightsizer/pkg/providers/loadtest"
)

const (
	// AdapterID is the temporary in-process GET adapter.
	// T10 removes this adapter together with pkg/loadtest. Historical runs may
	// still record this id. It is not a permanent engine.
	AdapterID = "builtin-get"
	// AdapterVersion is the legacy adapter version recorded for this generator.
	AdapterVersion = "legacy-1"
	// EngineID is the in-process GET generator. It is the same id as the adapter
	// because this temporary bridge does not wrap a separate engine.
	EngineID = "builtin-get"
	// EngineVersion is the legacy generator version.
	EngineVersion = "legacy-1"
)

func init() {
	if err := contract.Register(legacySpec()); err != nil {
		panic(err)
	}
}

func legacySpec() contract.AdapterSpec {
	return contract.AdapterSpec{
		Adapter: contract.ComponentIdentity{ID: AdapterID, Version: AdapterVersion},
		Engine:  contract.ComponentIdentity{ID: EngineID, Version: EngineVersion},
		Capabilities: []contract.Capability{
			contract.CapabilityHTTPRequests,
			contract.CapabilityHTTPErrors,
			contract.CapabilityActualHTTPThroughput,
			contract.CapabilityLatencyUntilHeaders,
			contract.CapabilityMeasuredPhase,
			contract.CapabilityCancellation,
		},
		LatencySemantics: contract.LatencyUntilResponseHeaders,
		ContractVersion:  contract.ContractVersion,
	}
}

// Tester is the temporary bridge from the provider contract to the in-process
// GET generator. T10 deletes this package. It does not implement warmup,
// cooldown, application checks, useful throughput, dropped work, or saturation.
type Tester struct{}

// New returns the temporary local adapter.
func New() *Tester { return &Tester{} }

// Run executes the legacy generator and returns its typed outcome.
// Load started by this call is stopped before Run returns.
func (t *Tester) Run(ctx context.Context, req contract.Request) (contract.Outcome, error) {
	if req.Adapter.ID == "" {
		req.Adapter.ID = AdapterID
		req.Adapter.Version = AdapterVersion
	}
	if req.Adapter.ID != AdapterID {
		err := fmt.Errorf("%w: local adapter cannot run %q", contract.ErrUnsupportedAdapter, req.Adapter.ID)
		return contract.ApplyLegacyFacts(req, legacySpec(), notStarted(req, contract.TerminationInvalidConfiguration, err)), err
	}
	session, err := contract.Begin(ctx, req)
	if err != nil {
		return contract.ApplyLegacyFacts(req, legacySpec(), session.Outcome()), err
	}

	duration, err := session.MeasuredDuration()
	if err != nil {
		outcome, abortErr := session.AbortBeforeStart(contract.TerminationInvalidConfiguration, err)
		return contract.ApplyLegacyFacts(req, legacySpec(), outcome), abortErr
	}
	generator := corelt.NewTester(req.TargetURL, req.RequestedRPS, req.Concurrency)
	if reason, preflightErr := generator.Preflight(ctx, duration); preflightErr != nil {
		outcome, err := session.AbortBeforeStart(reason, preflightErr)
		return contract.ApplyLegacyFacts(req, legacySpec(), outcome), err
	}
	if err := session.StartPhase(contract.PhaseMeasured); err != nil {
		outcome, abortErr := session.AbortBeforeStart(contract.TerminationInvalidConfiguration, err)
		return contract.ApplyLegacyFacts(req, legacySpec(), outcome), abortErr
	}

	result, runErr := generator.Run(ctx, duration)
	applyCoreResult(session, result)
	reason := result.TerminationReason
	if reason == "" {
		reason = contract.TerminationFromContext(ctx)
	}
	outcome, err := session.Finish(reason, runErr)
	return contract.ApplyLegacyFacts(req, legacySpec(), outcome), err
}

func applyCoreResult(session *contract.Session, result corelt.RunResult) {
	measurements := contract.Measurements{
		HTTPRequests:    contract.KnownCount(result.Requests),
		HTTPErrors:      contract.KnownCount(result.HTTPErrors),
		TransportErrors: contract.MissingCount(),
		StatusCodes:     result.StatusCodes,
	}
	if result.Requests > 0 {
		measurements.HTTPErrorRate = contract.KnownRatio(result.HTTPErrorRate)
		measurements.ActualThroughput = contract.KnownRate(result.ActualRPS)
		measurements.LatencyP50 = contract.KnownDuration(result.P50Latency)
		measurements.LatencyP95 = contract.KnownDuration(result.P95Latency)
		measurements.LatencyP99 = contract.KnownDuration(result.P99Latency)
	} else {
		measurements.HTTPErrorRate = contract.MissingRatio()
		measurements.ActualThroughput = contract.MissingRate()
		measurements.LatencyP50 = contract.MissingDuration()
		measurements.LatencyP95 = contract.MissingDuration()
		measurements.LatencyP99 = contract.MissingDuration()
	}
	session.SetMeasurements(measurements)
	session.SetChecks(contract.Checks{State: contract.SignalMissing})
	session.SetDropped(contract.MissingCount())
	session.SetUseful(contract.MissingRate())
	session.SetSaturated(contract.MissingBool())
	session.SetLatencySemantics(contract.LatencyUntilResponseHeaders)
}

func notStarted(req contract.Request, reason contract.TerminationReason, err error) contract.Outcome {
	return contract.Outcome{
		ContractVersion: contract.ContractVersion,
		Adapter:         contract.ComponentIdentity{ID: AdapterID, Version: AdapterVersion},
		Engine:          contract.ComponentIdentity{ID: EngineID, Version: EngineVersion},
		Scenario:        req.Scenario,
		Requested: contract.RequestedLoad{
			TargetURL:        req.TargetURL,
			Shape:            req.Shape,
			RPS:              req.RequestedRPS,
			Concurrency:      req.Concurrency,
			Duration:         req.RequestedDuration,
			LatencySemantics: req.LatencySemantics,
		},
		Executed:           false,
		Termination:        reason,
		HTTPRequests:       contract.KnownCount(0),
		HTTPErrors:         contract.KnownCount(0),
		HTTPErrorRate:      contract.MissingRatio(),
		ActualThroughput:   contract.MissingRate(),
		UsefulThroughput:   contract.MissingRate(),
		DroppedWork:        contract.MissingCount(),
		TransportErrors:    contract.MissingCount(),
		GeneratorSaturated: contract.MissingBool(),
		Latency: contract.LatencyReport{
			Semantics: contract.LatencyUntilResponseHeaders,
			P50:       contract.MissingDuration(),
			P95:       contract.MissingDuration(),
			P99:       contract.MissingDuration(),
		},
		Checks:      contract.Checks{State: contract.SignalMissing},
		Suitability: contract.Suitability{Reasons: []string{err.Error()}},
	}
}
