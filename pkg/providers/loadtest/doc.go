// Package loadtest is the engine-independent load-provider contract.
//
// Adapters turn a generator's facts into one Outcome. SLO gates and suitability
// live here. This package does not import a generator, k6, or a process runner.
// The temporary in-process GET adapter is pkg/providers/loadtest/local. It is
// removed in T10 together with pkg/loadtest. k6 is a later adapter, not a type
// in this package.
//
// # Request and outcome
//
// Request is what the caller asked for: target, shape, budget, phase plan,
// scenario identity, required capabilities, and latency semantics. Outcome is
// what the adapter observed. A requested duration is not an observed measured
// interval. HTTP requests, generator iterations, and successful application
// operations are different units and are not copied into each other.
//
// Counts, rates, ratios, durations, and booleans use SignalKnown, SignalMissing,
// or SignalUnknown. A missing or unknown signal is not a measured zero and not
// false. A known count or duration is not negative. A known rate is finite and
// not negative. A known ratio is finite and in the range [0, 1]. Known HTTP
// errors cannot exceed known HTTP requests. An out-of-range fact stays on the
// outcome. It is not replaced with zero, missing, or unknown, and it is not a
// usable measurement or a passed SLO. Zero HTTP requests normalize in-range
// rates, ratios, latency, and status counts to SignalMissing. They do not
// erase an out-of-range fact or the suitability reason that names it. A load
// that never started has Executed false and a nil measured interval.
//
// # Phases
//
// Run reports phase edges through Request.OnPhase. The callback is invoked
// synchronously on the Run goroutine when the edge happens, before that phase's
// load continues, and therefore before Run returns. That is what lets a later
// metrics collector start on the measured interval while load is still running.
// Returning timestamps only after Run would be too late.
//
// A channel was rejected because a missed receive or an unclosed channel can
// block Run. A start/wait handle was rejected because the caller could observe
// a return while load is still running. The callback has no acknowledgement
// step. Adapters must not wait on the observer after calling it. The observer
// must return without waiting for Run, and must not call back into the session.
// A panic from the observer becomes an error, and the adapter stops that run's
// load before Run returns.
//
// Legal phase order is warmup, then measured, then cooldown. Warmup and
// cooldown are optional in the plan. A usable duration_elapsed result still
// has exactly one measured phase that started and finished. That requirement
// does not depend on OnPhase or on listing measured_phase_boundaries in
// Required. An empty phase plan means the measured phase, not permission to
// skip it. The measured interval is that phase's actual start and finish.
// A planned duration is not substituted for those timestamps. Warmup and
// cooldown are outside measured statistics. An adapter that cannot separate
// those phases must not claim warmup_cooldown_phases. Planned durations and
// actual edge times are both kept. Actual times use the process wall clock,
// reported in UTC, with clock domain collector_local. They are not Metrics API
// source timestamps. Durations are nanoseconds.
//
// Cancellation or a deadline before the first phase leaves Executed false,
// termination context_canceled or context_deadline_exceeded, and no measured
// interval. The same stop after a phase has started leaves Executed true and
// may keep a partial outcome, including a warmup phase when measured never
// started. That prefix is not filled in with a measured phase, and it is not
// a usable measurement. After Run returns, that run's load has stopped.
//
// # Capabilities
//
// Register records an adapter id, the engine it wraps, the contract version,
// the capabilities it actually implements, and its latency semantics. Unknown
// ids are rejected. Removing the old closed schema enum is not by itself a
// support check. Required capabilities come from the request. A phase plan
// that includes warmup or cooldown also requires warmup_cooldown_phases.
// Begin rejects a mismatch before load starts. After a run, suitability
// rejects a required signal that was not actually produced, unknown or
// incompatible latency semantics, failed application checks, and confirmed
// generator saturation.
//
// Those suitability failures are not SLO math and are not always a Run error.
// HTTP failures, failed checks, and saturation can be facts of a generator
// that exited cleanly. Caller cancellation and deadlines still return the
// context error. A nil SLO is not_evaluated, not passed.
//
// LatencyUntilResponseHeaders is the legacy client measurement: time until
// http.Client.Do returns response headers, before the body is read. It is not
// full-response latency and it is not k6's http_req_duration.
// LatencyUnknown stays unknown. It does not become a known semantics value
// and it does not publish known percentiles, including when HTTP requests
// are positive. Those percentiles stay missing. An active p95 gate does not
// pass on that outcome.
package loadtest
