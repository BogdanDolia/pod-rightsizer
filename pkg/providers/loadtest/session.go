package loadtest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Session is one accepted load run. Adapters start phases on it, record
// measured facts, and Finish. Finish stops reporting; the adapter must stop
// the generator before it returns from Run.
type Session struct {
	mu          sync.Mutex
	req         Request
	spec        AdapterSpec
	outcome     Outcome
	now         func() time.Time
	started     bool
	finished    bool
	finishErr   error
	open        *PhaseName
	lastRank    int
	sawMeasured bool
}

// Begin checks the adapter, capabilities, and phase plan. It does not start
// load. On error the session outcome is a not-started result.
func Begin(ctx context.Context, req Request) (*Session, error) {
	session := &Session{
		req:      req,
		now:      func() time.Time { return time.Now().UTC() },
		lastRank: -1,
		outcome: Outcome{
			ContractVersion: ContractVersion,
			Requested:       requestedFrom(req),
			Scenario:        req.Scenario,
		},
	}
	if ctx == nil {
		err := fmt.Errorf("%w: context must not be nil", ErrInvalidRequest)
		session.reject(TerminationInvalidConfiguration, err)
		return session, err
	}
	if err := ctx.Err(); err != nil {
		session.reject(TerminationFromContext(ctx), err)
		return session, err
	}
	if err := validateShape(req); err != nil {
		reason := TerminationInvalidConfiguration
		if req.TargetURL == "" {
			reason = TerminationInvalidTarget
		}
		session.reject(reason, err)
		return session, err
	}
	if err := validatePhases(req.Phases); err != nil {
		wrapped := fmt.Errorf("%w: %v", ErrInvalidRequest, err)
		session.reject(TerminationInvalidConfiguration, wrapped)
		return session, wrapped
	}
	spec, ok := Lookup(req.Adapter.ID)
	if !ok {
		err := fmt.Errorf("%w: %q", ErrUnsupportedAdapter, req.Adapter.ID)
		session.reject(TerminationInvalidConfiguration, err)
		return session, err
	}
	session.spec = spec
	session.outcome.Adapter = spec.Adapter
	session.outcome.Engine = spec.Engine
	session.outcome.Claimed = append([]Capability(nil), spec.Capabilities...)
	session.outcome.Latency.Semantics = spec.LatencySemantics
	if req.Adapter.Version != "" && req.Adapter.Version != spec.Adapter.Version {
		err := fmt.Errorf("%w: adapter %q version %q is not registered version %q", ErrUnsupportedAdapter, req.Adapter.ID, req.Adapter.Version, spec.Adapter.Version)
		session.reject(TerminationInvalidConfiguration, err)
		return session, err
	}
	if err := validateCapabilities(req, spec); err != nil {
		session.reject(TerminationInvalidConfiguration, err)
		return session, err
	}
	if err := validateRequestedLatency(req, spec); err != nil {
		session.reject(TerminationInvalidConfiguration, err)
		return session, err
	}
	return session, nil
}

// Outcome returns the current result. Before Finish it is incomplete.
func (s *Session) Outcome() Outcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.outcome
}

// MeasuredDuration is the planned measured duration. It is not the actual interval.
func (s *Session) MeasuredDuration() (time.Duration, error) {
	if len(s.req.Phases) == 0 {
		if s.req.RequestedDuration <= 0 {
			return 0, errors.New("measured duration is not set")
		}
		return s.req.RequestedDuration, nil
	}
	for _, phase := range s.req.Phases {
		if phase.Name == PhaseMeasured {
			return phase.Duration, nil
		}
	}
	return 0, errors.New("measured phase is not in the plan")
}

// StartPhase emits the start edge and then records the phase.
// The observer runs before this method returns.
func (s *Session) StartPhase(name PhaseName) error {
	s.mu.Lock()
	event, err := s.prepareStart(name)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.open = &name
	s.mu.Unlock()

	if err := s.emit(event); err != nil {
		s.mu.Lock()
		if s.open != nil && *s.open == name {
			s.open = nil
		}
		s.mu.Unlock()
		return err
	}

	s.mu.Lock()
	s.commitStart(name, event.At)
	s.mu.Unlock()
	return nil
}

// FinishPhase emits the finish edge and records the actual finish time.
func (s *Session) FinishPhase(name PhaseName) error {
	s.mu.Lock()
	event, err := s.prepareFinish(name)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()

	if err := s.emit(event); err != nil {
		return err
	}
	s.mu.Lock()
	s.recordFinish(name, event.At)
	s.mu.Unlock()
	return nil
}

// SetMeasurements records measured-phase HTTP facts. Warmup and cooldown
// work must not be included.
func (s *Session) SetMeasurements(measurements Measurements) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outcome.HTTPRequests = measurements.HTTPRequests
	s.outcome.HTTPErrors = measurements.HTTPErrors
	s.outcome.HTTPErrorRate = measurements.HTTPErrorRate
	s.outcome.ActualThroughput = measurements.ActualThroughput
	s.outcome.TransportErrors = measurements.TransportErrors
	s.outcome.Latency.P50 = measurements.LatencyP50
	s.outcome.Latency.P95 = measurements.LatencyP95
	s.outcome.Latency.P99 = measurements.LatencyP99
	s.outcome.StatusCodes = cloneStatusCodes(measurements.StatusCodes)
}

// SetChecks records application checks. A missing state is not a pass.
func (s *Session) SetChecks(checks Checks) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outcome.Checks = checks
}

// SetDropped records dropped work. Zero is a measurement only when state is known.
func (s *Session) SetDropped(count Count) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outcome.DroppedWork = count
}

// SetUseful records successful application operations per second.
func (s *Session) SetUseful(rate Rate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outcome.UsefulThroughput = rate
}

// SetSaturated records generator saturation. Known false is not the same as missing.
func (s *Session) SetSaturated(value BoolSignal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outcome.GeneratorSaturated = value
}

// SetLatencySemantics records the semantics the adapter actually produced.
func (s *Session) SetLatencySemantics(semantics LatencySemantics) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outcome.Latency.Semantics = semantics
}

// AbortBeforeStart records a refusal that happened before any phase.
func (s *Session) AbortBeforeStart(reason TerminationReason, err error) (Outcome, error) {
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if started {
		return s.Finish(TerminationInternalError, errors.New("load already started"))
	}
	if reason == "" {
		reason = TerminationInvalidConfiguration
	}
	return s.Finish(reason, err)
}

// Finish closes an open phase, checks known numeric facts, normalizes absent
// signals, and stores suitability. It does not wait for the caller. A second
// call returns the same result. Zero-request normalization does not erase an
// invalid fact or the suitability reason that names it.
func (s *Session) Finish(reason TerminationReason, runErr error) (Outcome, error) {
	s.mu.Lock()
	if s.finished {
		outcome, err := s.outcome, s.finishErr
		s.mu.Unlock()
		return outcome, err
	}
	var finishEvent *PhaseEvent
	if s.open != nil {
		event, err := s.prepareFinish(*s.open)
		if err != nil {
			s.mu.Unlock()
			return s.forceFinish(reason, err)
		}
		finishEvent = &event
	}
	s.mu.Unlock()

	if finishEvent != nil {
		if err := s.emit(*finishEvent); err != nil && runErr == nil {
			runErr = err
			reason = TerminationInternalError
		}
		s.mu.Lock()
		if s.open != nil && *s.open == finishEvent.Name {
			s.recordFinish(finishEvent.Name, finishEvent.At)
		}
		s.mu.Unlock()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return s.outcome, s.finishErr
	}
	s.complete(reason, runErr)
	return s.outcome, s.finishErr
}

func (s *Session) forceFinish(reason TerminationReason, err error) (Outcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open != nil {
		s.recordFinish(*s.open, s.now())
	}
	if !s.finished {
		if reason == "" {
			reason = TerminationInternalError
		}
		s.complete(reason, err)
	}
	return s.outcome, s.finishErr
}

func (s *Session) reject(reason TerminationReason, err error) {
	s.outcome.Adapter = s.spec.Adapter
	s.outcome.Engine = s.spec.Engine
	s.outcome.Claimed = append([]Capability(nil), s.spec.Capabilities...)
	s.applyNotStarted(reason)
	s.finished = true
	s.finishErr = err
}

func (s *Session) complete(reason TerminationReason, runErr error) {
	if reason == "" {
		reason = TerminationInternalError
	}
	s.finished = true
	s.finishErr = runErr
	s.outcome.Termination = reason
	s.outcome.Executed = s.started
	if !s.started {
		s.applyNotStarted(reason)
		return
	}
	// Read the reported numbers before normalization can replace them.
	// The suitability stored below keeps these problems if a later pass
	// would otherwise see only the normalized signals.
	factProblems := reportedFactProblems(s.outcome)
	s.normalizeMeasured()
	s.outcome.Partial = reason != TerminationDurationElapsed || runErr != nil
	s.outcome.Suitability = retainFactProblems(AssessSuitability(s.req, s.spec, s.outcome), factProblems)
}

func (s *Session) applyNotStarted(reason TerminationReason) {
	semantics := s.outcome.Latency.Semantics
	if semantics == "" {
		semantics = LatencyUnknown
	}
	s.outcome.Executed = false
	s.outcome.Termination = reason
	s.outcome.Partial = false
	s.outcome.HTTPRequests = KnownCount(0)
	s.outcome.HTTPErrors = KnownCount(0)
	s.outcome.HTTPErrorRate = MissingRatio()
	s.outcome.ActualThroughput = MissingRate()
	s.outcome.UsefulThroughput = MissingRate()
	s.outcome.DroppedWork = MissingCount()
	s.outcome.TransportErrors = MissingCount()
	s.outcome.GeneratorSaturated = MissingBool()
	s.outcome.Latency = LatencyReport{
		Semantics: semantics,
		P50:       MissingDuration(),
		P95:       MissingDuration(),
		P99:       MissingDuration(),
	}
	s.outcome.Checks = Checks{State: SignalMissing}
	s.outcome.StatusCodes = nil
	s.outcome.Phases = nil
	s.outcome.Measured = nil
	s.outcome.Suitability = Suitability{Reasons: []string{"load did not start"}}
}

func (s *Session) normalizeMeasured() {
	if s.outcome.HTTPRequests.State == "" {
		s.outcome.HTTPRequests = MissingCount()
	}
	if s.outcome.HTTPErrors.State == "" {
		s.outcome.HTTPErrors = MissingCount()
	}
	if s.outcome.HTTPErrorRate.State == "" {
		s.outcome.HTTPErrorRate = MissingRatio()
	}
	if s.outcome.ActualThroughput.State == "" {
		s.outcome.ActualThroughput = MissingRate()
	}
	if s.outcome.UsefulThroughput.State == "" {
		s.outcome.UsefulThroughput = MissingRate()
	}
	if s.outcome.DroppedWork.State == "" {
		s.outcome.DroppedWork = MissingCount()
	}
	if s.outcome.TransportErrors.State == "" {
		s.outcome.TransportErrors = MissingCount()
	}
	if s.outcome.GeneratorSaturated.State == "" {
		s.outcome.GeneratorSaturated = MissingBool()
	}
	if s.outcome.Checks.State == "" {
		s.outcome.Checks.State = SignalMissing
	}
	if s.outcome.Latency.P50.State == "" {
		s.outcome.Latency.P50 = MissingDuration()
	}
	if s.outcome.Latency.P95.State == "" {
		s.outcome.Latency.P95 = MissingDuration()
	}
	if s.outcome.Latency.P99.State == "" {
		s.outcome.Latency.P99 = MissingDuration()
	}
	// No completed requests make an in-range rate, ratio, latency, or status
	// count meaningless. Replace those with missing signals. Keep a value
	// when replacing it would drop a problem reportedFactProblems already
	// named, so the invalid fact stays on the outcome.
	if s.outcome.HTTPRequests.State == SignalKnown && s.outcome.HTTPRequests.Value == 0 {
		errorRate := s.outcome.HTTPErrorRate
		s.replaceMeasuredFact(
			func() { s.outcome.HTTPErrorRate = MissingRatio() },
			func() { s.outcome.HTTPErrorRate = errorRate },
		)
		throughput := s.outcome.ActualThroughput
		s.replaceMeasuredFact(
			func() { s.outcome.ActualThroughput = MissingRate() },
			func() { s.outcome.ActualThroughput = throughput },
		)
		useful := s.outcome.UsefulThroughput
		s.replaceMeasuredFact(
			func() { s.outcome.UsefulThroughput = MissingRate() },
			func() { s.outcome.UsefulThroughput = useful },
		)
		p50 := s.outcome.Latency.P50
		s.replaceMeasuredFact(
			func() { s.outcome.Latency.P50 = MissingDuration() },
			func() { s.outcome.Latency.P50 = p50 },
		)
		p95 := s.outcome.Latency.P95
		s.replaceMeasuredFact(
			func() { s.outcome.Latency.P95 = MissingDuration() },
			func() { s.outcome.Latency.P95 = p95 },
		)
		p99 := s.outcome.Latency.P99
		s.replaceMeasuredFact(
			func() { s.outcome.Latency.P99 = MissingDuration() },
			func() { s.outcome.Latency.P99 = p99 },
		)
		statusCodes := s.outcome.StatusCodes
		s.replaceMeasuredFact(
			func() { s.outcome.StatusCodes = nil },
			func() { s.outcome.StatusCodes = statusCodes },
		)
	}
}

// replaceMeasuredFact applies one zero-request replacement. It restores the
// previous value when the replacement drops a reported fact problem.
func (s *Session) replaceMeasuredFact(apply func(), restore func()) {
	before := reportedFactProblems(s.outcome)
	apply()
	if factProblemsLost(before, reportedFactProblems(s.outcome)) {
		restore()
	}
}

// retainFactProblems keeps problems discovered on the original facts.
// Normalization and the suitability pass must not turn those into a usable result.
func retainFactProblems(suitability Suitability, factProblems []string) Suitability {
	if len(factProblems) == 0 {
		return suitability
	}
	seen := make(map[string]struct{}, len(suitability.Reasons)+len(factProblems))
	for _, reason := range suitability.Reasons {
		seen[reason] = struct{}{}
	}
	for _, problem := range factProblems {
		if _, exists := seen[problem]; exists {
			continue
		}
		suitability.Reasons = append(suitability.Reasons, problem)
		seen[problem] = struct{}{}
	}
	suitability.Usable = false
	return suitability
}

func factProblemsLost(before, after []string) bool {
	kept := make(map[string]struct{}, len(after))
	for _, problem := range after {
		kept[problem] = struct{}{}
	}
	for _, problem := range before {
		if _, exists := kept[problem]; !exists {
			return true
		}
	}
	return false
}

func (s *Session) prepareStart(name PhaseName) (PhaseEvent, error) {
	if s.finished {
		return PhaseEvent{}, errors.New("load run is already finished")
	}
	if s.open != nil {
		return PhaseEvent{}, fmt.Errorf("load phase %s is still open", *s.open)
	}
	rank, ok := phaseRank(name)
	if !ok {
		return PhaseEvent{}, fmt.Errorf("unknown load phase %q", name)
	}
	if rank <= s.lastRank {
		return PhaseEvent{}, fmt.Errorf("load phase %s is out of order", name)
	}
	if name == PhaseCooldown && !s.sawMeasured {
		return PhaseEvent{}, errors.New("cooldown cannot start before the measured phase")
	}
	if err := s.skippedPhase(name); err != nil {
		return PhaseEvent{}, err
	}
	if !s.phaseAllowed(name) {
		return PhaseEvent{}, fmt.Errorf("load phase %s is not in the plan", name)
	}
	return PhaseEvent{
		Name:  name,
		Edge:  EdgeStarted,
		At:    s.now(),
		Clock: ClockCollectorLocal,
	}, nil
}

func (s *Session) prepareFinish(name PhaseName) (PhaseEvent, error) {
	if s.open == nil || *s.open != name {
		return PhaseEvent{}, fmt.Errorf("load phase %s is not open", name)
	}
	return PhaseEvent{
		Name:  name,
		Edge:  EdgeFinished,
		At:    s.now(),
		Clock: ClockCollectorLocal,
	}, nil
}

func (s *Session) commitStart(name PhaseName, at time.Time) {
	planned, plannedKnown := s.plannedDuration(name)
	s.started = true
	s.open = &name
	if name == PhaseMeasured {
		s.sawMeasured = true
	}
	s.outcome.Phases = append(s.outcome.Phases, PhaseActual{
		Name:         name,
		Planned:      planned,
		PlannedKnown: plannedKnown,
		StartedAt:    at,
		Clock:        ClockCollectorLocal,
	})
}

func (s *Session) recordFinish(name PhaseName, at time.Time) {
	for i := range s.outcome.Phases {
		phase := &s.outcome.Phases[i]
		if phase.Name != name || !phase.FinishedAt.IsZero() {
			continue
		}
		phase.FinishedAt = at
		if name == PhaseMeasured {
			s.outcome.Measured = &Interval{
				StartedAt:  phase.StartedAt,
				FinishedAt: at,
				Clock:      ClockCollectorLocal,
			}
		}
		if rank, ok := phaseRank(name); ok {
			s.lastRank = rank
		}
		break
	}
	if s.open != nil && *s.open == name {
		s.open = nil
	}
}

func (s *Session) skippedPhase(name PhaseName) error {
	rank, ok := phaseRank(name)
	if !ok {
		return nil
	}
	for _, phase := range s.req.Phases {
		earlier, earlierOK := phaseRank(phase.Name)
		if !earlierOK || earlier >= rank || earlier <= s.lastRank {
			continue
		}
		return fmt.Errorf("load phase %s cannot start before %s finishes", name, phase.Name)
	}
	return nil
}

func (s *Session) phaseAllowed(name PhaseName) bool {
	if len(s.req.Phases) == 0 {
		return name == PhaseMeasured
	}
	for _, phase := range s.req.Phases {
		if phase.Name == name {
			return true
		}
	}
	return false
}

func (s *Session) plannedDuration(name PhaseName) (time.Duration, bool) {
	for _, phase := range s.req.Phases {
		if phase.Name == name {
			return phase.Duration, true
		}
	}
	if name == PhaseMeasured && len(s.req.Phases) == 0 && s.req.RequestedDuration > 0 {
		return s.req.RequestedDuration, true
	}
	return 0, false
}

func (s *Session) emit(event PhaseEvent) (err error) {
	s.mu.Lock()
	observer := s.req.OnPhase
	s.mu.Unlock()
	if observer == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("phase observer panic: %v", recovered)
		}
	}()
	observer(event)
	return nil
}

func validateShape(req Request) error {
	switch req.Shape {
	case ShapeRPS:
		if req.RequestedRPS <= 0 || req.RequestedRPS > 10_000 || req.Concurrency != 0 {
			return fmt.Errorf("%w: RPS mode requires requested RPS from 1 to 10000 and no concurrency", ErrInvalidRequest)
		}
	case ShapeConcurrency:
		if req.Concurrency <= 0 || req.Concurrency > 1_000 || req.RequestedRPS != 0 {
			return fmt.Errorf("%w: concurrency mode requires concurrency from 1 to 1000 and no RPS", ErrInvalidRequest)
		}
	default:
		return fmt.Errorf("%w: load shape must be rps or concurrency", ErrInvalidRequest)
	}
	if req.TargetURL == "" {
		return errors.New("load target URL is required")
	}
	if len(req.Phases) == 0 && (req.RequestedDuration <= 0 || req.RequestedDuration > MaxPhaseDuration) {
		return fmt.Errorf("%w: requested duration must be greater than zero and at most 24h", ErrInvalidRequest)
	}
	return nil
}

func validatePhases(phases []PhasePlan) error {
	if len(phases) == 0 {
		return nil
	}
	if len(phases) > 3 {
		return errors.New("load phase plan has more than three phases")
	}
	rank := -1
	measured := 0
	for _, phase := range phases {
		next, ok := phaseRank(phase.Name)
		if !ok {
			return fmt.Errorf("unknown load phase %q", phase.Name)
		}
		if next <= rank {
			return fmt.Errorf("load phase %s is out of order", phase.Name)
		}
		if phase.Duration <= 0 || phase.Duration > MaxPhaseDuration {
			return fmt.Errorf("load phase %s duration must be greater than zero and at most 24h", phase.Name)
		}
		rank = next
		if phase.Name == PhaseMeasured {
			measured++
		}
	}
	if measured != 1 {
		return errors.New("load phase plan must include one measured phase")
	}
	return nil
}

func validateCapabilities(req Request, spec AdapterSpec) error {
	required := requiredCapabilities(req)
	var missing []Capability
	for _, capability := range required {
		if !capability.Known() {
			return fmt.Errorf("%w: unknown capability %q", ErrInvalidRequest, capability)
		}
		if !HasCapability(spec, capability) {
			missing = append(missing, capability)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrMissingCapability, joinCapabilities(missing))
}

func validateRequestedLatency(req Request, spec AdapterSpec) error {
	if req.LatencySemantics == "" {
		return nil
	}
	if !req.LatencySemantics.Known() || req.LatencySemantics == LatencyUnknown {
		return fmt.Errorf("%w: %q", ErrIncompatibleLatency, req.LatencySemantics)
	}
	if req.LatencySemantics != spec.LatencySemantics {
		return fmt.Errorf("%w: requested %s, adapter declares %s", ErrIncompatibleLatency, req.LatencySemantics, spec.LatencySemantics)
	}
	return nil
}

func requiredCapabilities(req Request) []Capability {
	required := append([]Capability{}, req.Required...)
	for _, phase := range req.Phases {
		if phase.Name == PhaseWarmup || phase.Name == PhaseCooldown {
			required = append(required, CapabilityWarmupCooldown)
		}
	}
	if req.OnPhase != nil {
		required = append(required, CapabilityMeasuredPhase)
	}
	return uniqueCapabilities(required)
}

func uniqueCapabilities(capabilities []Capability) []Capability {
	seen := make(map[Capability]struct{}, len(capabilities))
	out := make([]Capability, 0, len(capabilities))
	for _, capability := range capabilities {
		if _, ok := seen[capability]; ok {
			continue
		}
		seen[capability] = struct{}{}
		out = append(out, capability)
	}
	return out
}

func joinCapabilities(capabilities []Capability) string {
	parts := make([]string, len(capabilities))
	for i, capability := range capabilities {
		parts[i] = string(capability)
	}
	return fmt.Sprint(parts)
}

func phaseRank(name PhaseName) (int, bool) {
	switch name {
	case PhaseWarmup:
		return 0, true
	case PhaseMeasured:
		return 1, true
	case PhaseCooldown:
		return 2, true
	default:
		return 0, false
	}
}

func requestedFrom(req Request) RequestedLoad {
	return RequestedLoad{
		TargetURL:        req.TargetURL,
		Shape:            req.Shape,
		RPS:              req.RequestedRPS,
		Concurrency:      req.Concurrency,
		Duration:         req.RequestedDuration,
		Phases:           append([]PhasePlan(nil), req.Phases...),
		LatencySemantics: req.LatencySemantics,
		Required:         append([]Capability(nil), req.Required...),
	}
}

func cloneStatusCodes(codes map[int]int) map[int]int {
	if len(codes) == 0 {
		return nil
	}
	cloned := make(map[int]int, len(codes))
	for code, count := range codes {
		cloned[code] = count
	}
	return cloned
}
