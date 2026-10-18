# Contract v1 fixtures

Every JSON document here is synthetic. `dataClass` is `synthetic_fixture`. Nothing in this tree was collected from a cluster, and the numbers are examples of the v1 shape rather than production evidence.

`manifestSha256` and artifact `sha256` values are lowercase hex claims so the schema's hash pattern can be checked. They are not digests of the files beside them. Byte verification belongs to T08. `testdata/contracts/v1/semantic-checks.json` records checks the schema does not execute. `executedBy` is `none` except where a named Go test runs that statement.

The expected result of each document is `catalog.json`. From the repository root:

```bash
bash scripts/check-contracts.sh
```

A `valid` row must validate. An `invalid` row must fail, and the validator message must contain every `errorContains` fragment.

## Shared synthetic identity

Namespace `rightsizer-synthetic`, Deployment `orders-api`, container `api`, logical service `orders`, two replicas. Policy defaults match the current recommender: CPU p95 plus 10 percent, memory high-water plus 20 percent, CPU limit mode `none`, memory limit mode `request-multiplier` at 1.2. The report-only recommendation example is 198 millicores, 174 Mi (182452224 bytes), CPU limit `absent`, memory limit 209 Mi (219152384 bytes), evidence confidence low at 0.12.

## Schema-valid business outcomes

These documents are valid. A failed SLO, a missing signal, or an unverified source is data the contract must be able to describe.

| Path | What it shows |
| --- | --- |
| `valid/report-only/` | Completed analysis. Recommendation is present. Patch was not requested. Candidate is `not_tested`. Source is `declared` through an OCI label, so cause use is `hypothesis_only`. Event-loop and GC signals are `missing_signal` with conclusion `not_inferred`. Four accepted windows plus discarded overlap, duplicate, and outside-interval windows. |
| `valid/dry-run/` | `patch.state` is `dry_run_passed`, purpose `experimental`, `admissionMeaning` `api_admission_only`. Candidate remains `not_tested`, with limitation `experimental_patch_not_a_candidate_test`. |
| `valid/slo-failed/` | SLO `failed`, error ratio 0.08. Recommendation and confidence are null. No patch. |
| `valid/insufficient/` | One independent window. Evidence `insufficient`. SLO can still be `passed`. No recommendation. |
| `valid/cancelled/` | Execution `cancelled`, SLO `not_evaluated`, diagnostics present, load termination `context_canceled`. |
| `valid/failed-run/` | Execution `failed`, evidence `invalid`, load `not_run`, autoscaler discovery not attempted so the gate is `unknown`. Workload identity was already read. |
| `valid/early-failure/` | Execution `failed` before Deployment discovery. `unreadReason` is `forbidden`. The document has the requested name and no UID, digest, owner, or pod. Load `executed` is false and termination is `not_started`. |
| `valid/early-cancel/` | Execution `cancelled` before discovery. `unreadReason` is `cancelled_before_discovery`. Load termination is `context_canceled` and `executed` is false. |
| `valid/early-not-found/` | Execution `failed` before discovery. `unreadReason` is `not_found`. Load `executed` is false and termination is `not_started`. |
| `valid/early-timeout/` | Execution `failed` before the Deployment object was returned. `unreadReason` is `timeout`. The document has the requested name and no UID, generation, digest, owner, or pod. Load `executed` is false and termination is `not_started`. |
| `valid/early-transport-error/` | Execution `failed` before discovery. `unreadReason` is `transport_error`. The synthetic case is connection refused. That symptom is not its own code. No UID, generation, digest, owner, or pod. |
| `valid/early-api-server-5xx/` | Execution `failed` before discovery. `unreadReason` is `api_server_5xx`. The synthetic case is HTTP 503. The status number is not a separate field. No UID, generation, digest, owner, or pod. |
| `valid/pods-empty/` | Execution `failed` after the Deployment read. `podList.discovery` is `empty`: ListPods succeeded and returned no pods. UID, generation, and image digest are kept. `replicas.observed` is 0 because that is the Deployment status. No pod UID, owner UID, or stability confirmation. Candidate is `not_tested` and export is `not_requested`. |
| `valid/pods-list-failed/` | Execution `failed` after the Deployment read. `podList.discovery` is `not_obtained` with `unreadReason` `list_failed`. Status replica count stays 2. The empty `pods` array is not a census. |
| `valid/pods-cancelled/` | Execution `cancelled` after the Deployment read and before ListPods returned. `unreadReason` is `cancelled_before_pods`. Load termination is `context_canceled` and `executed` is false. |
| `valid/pods-digest-unknown/observations.json` | The Deployment image had no digest. `image.digest.state` is `unknown` and has no value. The pod list was not obtained. |
| `valid/requests-both-absent/analysis.json` | Both current requests are `absent`. The recommendation remains. Comparison currents are 0. |
| `valid/requests-cpu-absent/analysis.json` | CPU request `absent`, memory request `set`. |
| `valid/requests-memory-absent/analysis.json` | Memory request `absent`, CPU request `set`. |
| `valid/partial-settings/analysis.json` | No recommendation. CPU request and CPU limit are `unknown`. Memory request is `set`. Memory limit is `absent`. |
| `valid/rejected-insufficient/analysis.json` | Candidate `rejected` after a completed test whose evidence is `insufficient`. `testedSettings` and comparison ids remain. Recommendation and confidence are null. Export is not ready. |
| `valid/rejected-invalid/analysis.json` | Candidate `rejected` with evidence `invalid` and reason `identity_changed`. |
| `valid/limit-transition/analysis.json` | CPU limit moves from `set` to `absent`. Memory limit moves from `absent` to `set`. |
| `valid/load-cancelled-partial/observations.json` | The load started (`executed` true) and then ended with `context_canceled`. Requests, error rate, and known latency remain. |
| `valid/example-engine/run-spec.json` | Adapter id `example-engine`, not only `builtin-get` or `k6-wrapper`. Warmup and cooldown are requested, so `warmup_cooldown_phases` is required. |
| `valid/provider-outcome/observations.json` | Actual warmup, measured, and cooldown bounds. Checks are `missing_signal`. Dropped work and saturation are null. `contractVersion` is `1`. |
| `valid/identity-changed/` | Image digest changed inside the run. `identityStable` is false. Evidence `invalid` with `identity_changed`. |
| `valid/observe/` | No load URL and no SLO. A recommendation is allowed only with limitation `observe_without_slo`. Candidate stays `not_tested`. |
| `valid/autoscaler-unknown/` | HPA discovery `denied` is `unknown`, not absent. A recommendation may exist. Candidate validation is blocked. |
| `valid/source-unverified/` | `confirmation: none`, `causeUse: unavailable`. This is not a confirmed code cause. A numeric recommendation may still be present. |
| `valid/source-dirty/` | Reproducible build record of a commit, dirty worktree. Status is `mismatch` with `source_dirty`. Cause use is `hypothesis_only`. |
| `valid/code-change/` | Different image digest and declared commit. Current resources stay the report-only settings so a code-regression comparison can match resources. |
| `valid/runtime-peer/runtime-findings.json` | Same profile with observed Node.js 20.18.0, used as the peer that makes runtime version `not_comparable`. |
| `valid/resource-baseline/` | Staging baseline. Source `verified` by `reproducible_build_record`, clean tree, `verified_cause` ref. Candidate `not_tested`. |
| `valid/resource-candidate/` | Different Deployment UID, same image digest and verified commit. `testedSettings` are the deployed candidate, `candidate.state` is `validated`, patch purpose is `review`, `gitopsExport` is `ready`. |
| `valid/candidate-rejected/` | Same resource change shape with SLO `failed`. Candidate `rejected`. Comparison id is the failing comparison below, not the passing one. |
| `valid/comparison-code/` | Intent `code_regression`. Image and source differ, resources match, Deployment UID may differ. Comparable, budget not evaluated, `ciVerdict` `inconclusive`. |
| `valid/comparison-resource/` | Intent `resource_candidate`. Resources differ, image and source match. Both SLOs passed, `ciVerdict` `pass`, budget still `not_evaluated`. |
| `valid/comparison-rejected/` | Intent `resource_candidate`. Candidate SLO failed, `ciVerdict` `fail`. |
| `valid/comparison-not-comparable/` | Runtime version differs. `comparability` `not_comparable`, deltas null, `ciVerdict` `not_comparable`. |

`valid/resource-candidate/resource-patch.yaml` is a synthetic strategic-merge example. `cpu: null` means the limit is absent. It is not an unknown quantity and it was not applied. The repository ignore rule for tool-written `resource-patch.yaml` files also matches this path. The fixture is tracked anyway; `.gitignore` is unchanged so real tool output stays ignored.

Markdown files are fixture bodies so completed manifests that name `report.md` and `agent-brief.md` have those files. The schema checks the names inside `manifest.json`, not that the files exist. T08 checks the bytes.

## Schema-invalid documents

| Document | Schema | Rejection the catalog requires |
| --- | --- | --- |
| `invalid/unknown-execution.json` | analysis | `execution` must be `completed`, `failed`, or `cancelled` |
| `invalid/unknown-schema-version.json` | analysis | `schemaVersion` must be `1.0.0` |
| `invalid/unknown-minor-version.json` | analysis | `1.1.0` is also rejected |
| `invalid/missing-run-id.json` | analysis | missing `runId` |
| `invalid/cpu-quantity-string.json` | analysis | `cpuRequestMilli` must be an integer, not `198m` |
| `invalid/negative-memory.json` | analysis | memory request below the floor |
| `invalid/error-rate-as-percent.json` | run spec | error rate 50 is above the ratio maximum of 1 |
| `invalid/adapter-id-pattern.json` | run spec | `wrapper.name` must match the component id pattern |
| `invalid/latency-full-response.json` | run spec | `full_response` is not a latency semantics value |
| `invalid/capability-shell-hook.json` | run spec | `shell_hook` is not a capability |
| `invalid/checks-missing-with-passed.json` | observations | `missing_signal` checks cannot also carry `passed` |
| `invalid/two-measured-phases.json` | observations | two measured phases are not a legal actual order |
| `invalid/unknown-latency-known-p95.json` | observations | unknown latency semantics requires `missing_signal` percentiles |
| `invalid/zero-encodes-missing-signal.json` | runtime findings | `missing_signal` cannot also carry `nanoseconds` |
| `invalid/missing-signal-means-absent.json` | runtime findings | conclusion cannot be `problem_absent` |
| `invalid/slo-failed-with-recommendation.json` | analysis | failed SLO cannot carry a recommendation object |
| `invalid/cancelled-but-validated.json` | analysis | cancelled execution cannot have candidate `validated` |
| `invalid/gitops-ready-before-validation.json` | analysis | ready export requires a validated candidate and review purpose |
| `invalid/verified-source-from-oci-label.json` | source context | an OCI label cannot be `verified` |
| `invalid/verified-source-dirty.json` | source context | a dirty tree cannot be `verified` |
| `invalid/code-regression-resources-differ.json` | comparison result | code regression requires resources `match` |
| `invalid/resource-candidate-image-differs.json` | comparison result | resource candidate requires image `match` |
| `invalid/not-comparable-but-pass.json` | comparison result | `not_comparable` cannot be `ciVerdict` `pass` |
| `invalid/autoscaler-denied-means-absent.json` | observations | denied discovery cannot be state `absent` |
| `invalid/unknown-field.json` | analysis | additional property `success` is rejected |
| `invalid/telemetry-validated-with-missing-signal.json` | runtime findings | `telemetry_validated` cannot keep a missing-signal finding |
| `invalid/dry-run-with-failed-slo.json` | analysis | experimental dry-run requires SLO `passed` and cannot keep a recommendation beside a failed SLO |
| `invalid/confidence-level-mismatch.json` | analysis | level `high` requires score at least 0.8 |
| `invalid/confidence-means-safety.json` | analysis | `meaning` must be `evidence_volume` |
| `invalid/runtime-change-intent.json` | comparison request | intent must be `code_regression` or `resource_candidate` |
| `invalid/zero-limit-number.json` | analysis | a limit is an object with `state`, not the number 0 |
| `invalid/absent-limit-replay-nonzero.json` | analysis | an absent limit's replay core value must be 0 |
| `invalid/manifest-patch-without-dry-run.json` | manifest | `resource-patch.yaml` is forbidden unless patch state is `dry_run_passed` |
| `invalid/secret-field.json` | run spec | additional property `authorization` is rejected |
| `invalid/code-regression-pass-without-noise-policy.json` | comparison result | `pass` requires budget `within_budget` |
| `invalid/unread-identity-stable.json` | observations | unread discovery cannot set `identityStable` true |
| `invalid/unread-with-deployment-uid.json` | observations | an unread workload cannot carry `deploymentUid` |
| `invalid/unread-with-pod.json` | observations | an unread run cannot carry a pod |
| `invalid/unread-service-verified.json` | observations | an unread run cannot claim a verified route |
| `invalid/read-missing-deployment-uid.json` | observations | a read workload requires `deploymentUid` |
| `invalid/read-without-pods.json` | observations | a read workload requires at least one pod |
| `invalid/read-without-owner.json` | observations | a read workload requires a non-empty owner chain |
| `invalid/identity-stable-after-change.json` | observations | a changed image digest cannot set `identityStable` true |
| `invalid/unread-with-window.json` | observations | an unread run cannot carry a metric window or a pod UID |
| `invalid/unread-unknown-reason.json` | observations | `connection_refused` is not a closed `unreadReason` |
| `invalid/partial-coverage-uid.json` | observations | partial discovery cannot invent a coverage pod UID |
| `invalid/partial-empty-with-reason.json` | observations | a successful empty census cannot carry `unreadReason` |
| `invalid/partial-identity-stable.json` | observations | partial discovery cannot set `identityStable` true |
| `invalid/partial-known-digest-missing.json` | observations | a known digest requires the value that was read |
| `invalid/partial-missing-deployment-uid.json` | observations | partial discovery must keep `deploymentUid` |
| `invalid/partial-not-obtained-without-reason.json` | observations | an unobtained pod list requires `unreadReason` |
| `invalid/partial-pre-discovery-cancel-reason.json` | observations | `cancelled_before_discovery` is not a reason after the Deployment was read |
| `invalid/partial-unknown-digest-with-value.json` | observations | an unknown digest cannot carry a value |
| `invalid/partial-verified-service.json` | observations | partial discovery cannot claim a verified route |
| `invalid/partial-with-owner.json` | observations | partial discovery cannot invent an owner UID |
| `invalid/partial-with-pod.json` | observations | partial discovery cannot invent a pod UID |
| `invalid/partial-with-stability.json` | observations | partial discovery cannot carry a stability confirmation |
| `invalid/partial-with-window.json` | observations | partial discovery cannot carry a metric window |
| `invalid/partial-zero-generation.json` | observations | generation 0 is not an unread generation |
| `invalid/negative-cpu-request.json` | analysis | a set CPU request cannot be negative |
| `invalid/negative-memory-request.json` | analysis | a set memory request cannot be negative |
| `invalid/request-encoded-as-zero.json` | analysis | the number 0 is not an absent request |
| `invalid/request-set-to-zero.json` | analysis | a set CPU request cannot be 0 |
| `invalid/absent-request-nonzero-comparison.json` | analysis | an absent CPU request compares as `currentMilli` 0 |
| `invalid/set-request-zero-comparison.json` | analysis | a set CPU request cannot compare as `currentMilli` 0 |
| `invalid/tested-settings-zero-request.json` | analysis | `testedSettings` keeps a positive CPU request |
| `invalid/tested-settings-zero-memory.json` | analysis | `testedSettings` keeps a positive memory request |
| `invalid/absent-memory-request-nonzero-comparison.json` | analysis | an absent memory request compares as `currentBytes` 0 |
| `invalid/set-memory-request-zero-comparison.json` | analysis | a set memory request cannot compare as `currentBytes` 0 |
| `invalid/unknown-memory-request-with-recommendation.json` | analysis | `unknown` memory request cannot sit beside a recommendation |
| `invalid/comparison-recommended-cpu-zero.json` | analysis | the recommended CPU request keeps the 10 millicore floor |
| `invalid/unknown-cpu-request-with-recommendation.json` | analysis | `unknown` CPU request cannot sit beside a recommendation |
| `invalid/unknown-cpu-limit-with-recommendation.json` | analysis | `unknown` CPU limit cannot sit beside a recommendation |
| `invalid/unknown-memory-limit-with-recommendation.json` | analysis | `unknown` memory limit cannot sit beside a recommendation |
| `invalid/validated-with-insufficient-evidence.json` | analysis | `validated` still requires evidence `valid` |
| `invalid/validated-with-invalid-evidence.json` | analysis | `validated` still requires evidence `valid` |
| `invalid/ready-export-insufficient-evidence.json` | analysis | ready export still requires evidence `valid` |
| `invalid/ready-export-invalid-evidence.json` | analysis | ready export still requires evidence `valid` when evidence is `invalid` |
| `invalid/insufficient-with-recommendation.json` | analysis | `insufficient` evidence keeps recommendation null |
| `invalid/invalid-evidence-with-confidence.json` | analysis | `invalid` evidence keeps confidence null |
| `invalid/cpu-limit-current-memory-bytes.json` | analysis | CPU limit `current` cannot use `memoryBytes` |
| `invalid/cpu-limit-recommended-memory-bytes.json` | analysis | CPU limit `recommended` cannot use `memoryBytes` |
| `invalid/memory-limit-current-cpu-milli.json` | analysis | memory limit `current` cannot use `cpuMilli` |
| `invalid/memory-limit-recommended-cpu-milli.json` | analysis | memory limit `recommended` cannot use `cpuMilli` |
| `invalid/load-not-started-with-requests.json` | observations | `executed` false requires `requests` 0 |
| `invalid/load-not-started-with-errors.json` | observations | `executed` false requires `httpErrors` 0 |
| `invalid/load-not-started-with-latency.json` | observations | a load that did not start cannot report known latency |
| `invalid/load-not-started-with-rates.json` | observations | zero requests keep `actualRps`, `httpErrorRate`, and `usefulThroughputRps` null |
| `invalid/load-not-started-with-interval.json` | observations | a load that did not start keeps `interval` null |

## Checks left to later tasks

`semantic-checks.json` is validated against `semantic-checks.schema.json` and does not run its statements. Examples:

- T03 must fold overlapping windows and keep `independentWindowCount` honest.
- T05 must join `identityStable: false` to `identity_changed` only after discovery `read`. `not_obtained` and `partial` are false because stability was not established. Partial discovery cannot join to a validated candidate or a ready export.
- T07 must refuse `gitopsExport` `ready` unless workload discovery is `read`.
- T15 must refuse candidate `validated` unless workload discovery is `read`.
- T09 must recompute `recommender-core-1` from `replayInputs`.
- T14 must derive `checkedConditions` from the two packages. A schema-valid comparison can still lie about those conditions until that task exists.
- T08 must hash artifact bytes and compare `outcome` with `analysis.json`.
