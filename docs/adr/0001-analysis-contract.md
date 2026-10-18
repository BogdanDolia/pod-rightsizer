# ADR 0001: Analysis contract v1

## Status

Accepted as the T01 design contract. `contracts/v1` is not emitted by the current CLI or Advisor API. Production flags, routes, formulas, stdout, and exit behavior are unchanged.

## 1. Current state

The program is a Go tool. The pilot target is one Node.js backend HTTP API, later a TypeScript Express service on one pinned Node.js LTS line. Go is the language of this tool, not the selected application runtime. .NET, Go, Python, and JVM profiles are later stages and have no manifests in this contract.

Verified on the historical base `324d1c52b3c38345380395eb13cccddd49e8adf9`, which was `main` when this branch was created. The following gaps are in that code:

| Area | Files | Contract problem |
| --- | --- | --- |
| CLI | `cmd/pod-rightsizer/main.go` | One invocation always load-tests, gates on SLO, server-side dry-runs, and writes `resource-patch.yaml`. Failure and cancellation both exit 1. There is no cancelled document and no separate report-only result. |
| API | `pkg/server/server.go` | `POST /api/analyze` returns `{runId}`. `GET /api/runs/{id}` has one `status` (`created`, `running`, `completed`, `failed`) plus `patchDryRun`. A completed run with `patchDryRun: true` is the only success shape. Cancellation is stored as `failed`. An empty `targetURL` observes without load and without an SLO. The decoder rejects unknown fields. |
| Recommendation | `pkg/recommender/recommender.go` | CPU is a core `float64`. Memory is mebibytes as `float64`. A limit of `0` means no limit. Confidence is `0.8 * min(samples/30, 1) + 0.2 * min(seconds/1800, 1)`, rounded to two decimals. The comment already says this is evidence volume, not safety. |
| Load | `pkg/loadtest/tester.go` | The public error-rate flag is a percent. `SLO.MaximumHTTPErrorRate` is a ratio. Latency stops when `http.Client.Do` returns headers, before the body is read. A zero latency limit disables that gate. |
| Metrics | `pkg/metrics/collector.go` | Evidence is the source timestamp and window, not the poll time. Duplicate timestamps are dropped. Overlapping windows fold into the previous maximum and do not increase the independent count. Only fully contained windows count. |
| Identity | `pkg/kubernetes/client.go`, `pkg/kubernetes/patch.go` | `Workload` has name, generation, container, and selector. It has no UID, owner chain, or image digest. Current resources are core and Mi floats; an unset limit is `0`. The patch ceils to millicores and whole Mi, and writes JSON `null` for a CPU limit of 0. Dry-run is always `dryRun=All`. |
| Output | `pkg/output/printer.go` | JSON and YAML stdout are the legacy result. Text uses `%.0f` while the patch uses `math.Ceil`. The file write always happens after a successful dry-run. |
| Providers | `pkg/providers/metrics`, `pkg/providers/loadtest` and their local/k8s adapters | `metrics.Sample` keeps timestamp, `cpu_m`, and `memory_mi`. It drops container, window, pod identity, and coverage. The k8s adapter converts cores to millicores and drops the window. `loadtest.Run` returns only an error, so the typed `RunResult` is discarded. |
| Knowledge | `pkg/knowledge/knowledge.go` | `Evaluate` returns unconditional strings. It is not a profile, a rule id, or a missing-signal record. |

These shapes cannot be the v1 contract. One boolean, or the current `status` plus `patchDryRun`, collapses execution, evidence, SLO, source, candidate, and patch into a single success.

T03 closes the provider row in code. The load contract and the metrics sample now keep the outcome, phase bounds, source window, and coverage that row said were dropped. The CLI and Advisor API still call the legacy tester and collector directly. Switching them is T04. T03 does not add a UID, owner chain, or image digest; that remains T05.

## 2. Decision

One versioned document family in `contracts/v1`, draft 2020-12, shared by the future CLI, API, and assistant handoff. There is no plugin framework and no separate result model per interface.

| Document | Role |
| --- | --- |
| `RunSpec` | Target, one Deployment/container, mode, measured phase, SLO, policy, and runtime profile. No secrets. |
| `Observations` | Identity, source windows, coverage, load outcome, and discard reasons. |
| `Analysis` | Independent axes, current and tested settings, recommendation, provenance, explanations. |
| `RuntimeFindings` | Node.js or generic profile data: capability, rule ids, evidence refs, missing signals. Not executable rules. |
| `SourceContext` | Image, build, and source mapping, including dirty and unverified states. |
| `Manifest` | Versions, run id, artifact names, and claimed content hashes. |
| `ComparisonRequest` / `ComparisonResult` | Future T14 shape. Not a comparison engine. |

The plan's separate `scenario.json` is folded into `RunSpec`. A run has one scenario; a second file would duplicate the load mode and SLO without a second owner.

### Why this shape

- Axes stay independent, so `execution: completed` cannot imply valid evidence, a passed SLO, verified source, a validated candidate, or a validated patch.
- `additionalProperties: false` and an exact `schemaVersion` of `1.0.0` reject unknown fields and unknown majors or minors. Readers select a schema by exact version. They do not ignore unknown fields.
- Local `$ref` only. The `$schema` URL names draft 2020-12. The validator embeds that meta-schema and loads only `file` URLs.
- Canonical analysis quantities are integer millicores and integer bytes. Legacy core and Mi floats live only in `replayInputs`, so T09 can rerun `recommender-core-1` without making those floats the stored recommendation.
- Comparison is a separate pair of documents. A run package does not contain the other run, and two linked runs may have different Deployment UIDs.

### Rejected alternatives

| Alternative | Why it was rejected |
| --- | --- |
| One success boolean, or treating API `completed` as full success | It hides failed SLO, missing signals, unverified source, and an untested candidate. |
| Encoding `unknown` or `missing_signal` as `0` | Zero is a real measurement, and a zero limit already means "no limit" in the recommender. |
| Reusing the current CLI JSON as v1 | It has no identity, windows, source status, or separate axes, and it writes limits as floats. |
| A different result schema for CLI, API, and the assistant | The assistant would then invent a fourth verdict. `report.md` and `agent-brief.md` are renderings, not new models. |
| Requiring the same Deployment UID for every comparison | A candidate Deployment is a different object. UID stability is required only inside one measurement. |
| A plugin framework or five runtime profiles now | The pilot is one Node.js HTTP API profile plus a `generic` bucket. Adding a profile id is a schema change. |
| Fetching schemas from the network | Validation must work offline. Remote `$ref` is rejected before compile. |
| Draft-04, or a hand-written subset of JSON Schema | The `if`/`then` matrix and formats need a draft 2020-12 implementation. |
| Putting the validator in the main module | It would become a runtime dependency of a program that does not emit these documents yet. |
| Changing current flags in T01 | Wiring is T04 and T07. This ADR does not change behavior. |
| Treating an OCI label or a Git SHA as a verified image-to-source link | Those are declarations. Verification requires `reproducible_build_record` and a clean tree. |
| A third MVP intent for runtime setting or version changes | That difference is `not_comparable` with `runtime_settings_differ` or `runtime_version_differs`. It is not a hidden allowance inside the two intents. |
| Storing canonical CPU only as core floats | Patch rounding is defined on millicores and mebibytes. Floats remain replay inputs. |

## 3. Fields, units, and absence

| Quantity | Canonical field | Rule |
| --- | --- | --- |
| CPU | integer `cpuMilli` | 1000 millicores = 1 core. Not a Kubernetes quantity string such as `198m`. |
| Memory | integer `memoryBytes` | 1 Mi = 1048576 bytes. Mebibytes are not stored in this field. |
| Recommendation memory | `roundedMemoryBytes` | `multipleOf` 1048576. Rounding is `ceil` of the legacy Mi value to a whole mebibyte, then times 1048576. |
| Recommendation CPU request | integer | Minimum 10 millicores, matching the 0.01 core floor. |
| Set CPU limit | integer | Minimum 50 millicores when `state` is `set`. |
| Set memory limit | integer bytes | Minimum 64 Mi (67108864) when `state` is `set`. |
| Duration | integer nanoseconds | Legacy Go duration strings stay at the CLI and API boundary until T04. |
| Error rate | number 0 through 1 | Legacy percent flags are divided by 100 before they enter this contract. `50` is invalid. |
| Timestamp | RFC 3339 with offset | `date-time`. Source window time is not `polledAt`. |
| Confidence | `evidenceConfidence` | `meaning` is exactly `evidence_volume`. Level bands: high at least 0.8, medium at least 0.45, otherwise low. |

`replayInputs` keeps `cpuCores`, `memoryMi`, `cpuLimitCores`, and `memoryLimitMi` as JSON numbers. T09 must parse them with Go `encoding/json` into `float64` and rerun `recommender-core-1`. T01 fixtures are schema examples. The report-only example uses four windows, p95 rank 3, request 0.198 cores (198 millicores), memory high-water 145 Mi, request 174 Mi (182452224 bytes), and a limit raw value whose decimal example is 208.8 Mi, ceiled to 209 Mi (219152384 bytes). Confidence score 0.12 is low. They are not claimed as bitwise golden output; T09 owns that.

Text display `%.0f` and patch `math.Ceil` can disagree in the legacy program. T01 does not change that.

### `absent`, JSON `null`, zero, and unknown

| Situation | Representation |
| --- | --- |
| Recommendation not produced | `recommendation` and `evidenceConfidence` are JSON `null`. |
| Completed run | `diagnostics` is JSON `null`. Failed and cancelled runs require a diagnostics object. |
| No current resources because the run failed before settings were read | `currentSettings` is JSON `null`. |
| Limit not set | Authoritative `cpuLimit.state` or `memoryLimit.state` is `absent`. There is no number. For `recommender-core-1` only, the matching replay field is `0`. |
| Limit set | `state` is `set` plus a positive rounded quantity. Replay limit is `exclusiveMinimum` 0. |
| Legacy patch YAML `cpu: null` | A serializer detail of `absent` in `resource-patch.yaml`. It is not stored as an analysis number and it is not unknown. |
| Unknown limit | Not representable beside a recommendation. Unknown is a signal or discovery state, not a quantity of 0. |
| Missing runtime signal | `state: missing_signal` and no numeric sibling. A known measurement may be 0. |
| Current CPU or memory request unset | `cpuRequest.state` or `memoryRequest.state` is `absent`. There is no number. `GetResourceSettings` returns 0 for a missing quantity; that 0 is not stored. |
| Current request or limit not read | `state` is `unknown`. This is not `absent`. |
| Recommendation beside current settings | Both current requests and both current limits are `absent` or `set`. `unknown` requires `recommendation` null. |
| Absent request inside a comparison | `currentMilli` or `currentBytes` is 0. That 0 is the delta input only. The recommended side keeps the recommendation floor: at least 10 millicores, or at least 32Mi on a mebibyte boundary. |
| CPU or memory limit comparison | Each side is `absent` with no number, or `set` with only `cpuMilli` on `cpuLimit` and only `memoryBytes` on `memoryLimit`. |
| Load not started | `executed` is false. `requests` and `httpErrors` are 0. `interval` is null. `phases` is absent or empty. Termination is `not_started`, `context_canceled`, `invalid_configuration`, or `invalid_target`. Rates and latency follow the zero-request row. |
| Zero requests, including a load that did not start | In-range `actualRps`, `httpErrorRate`, and `usefulThroughputRps` are JSON `null`. In-range latency states are `missing_signal`, not 0. An invalid known fact is not rewritten into that shape. |
| Load started, then cancelled or failed | `executed` stays true. Partial requests, rates, and latency may be recorded. `phases`, when present, is the prefix that actually ran. Warmup alone is allowed, and `interval` is null when measured did not start. A measured phase is not invented. |
| Successful load phases | When `phases` is present and termination is `duration_elapsed`, the array is one completed order with exactly one measured phase. Omitting `phases` does not invalidate an older document. |
| Unknown latency with requests | `latency.semantics` stays `unknown`. Every percentile stays `missing_signal`. Requests and known rates remain. This is not `until_response_headers`, not `k6_http_req_duration`, and not a zero duration. |
| Invalid known load fact | A known count or duration below zero, a non-finite or negative rate, a ratio outside `[0, 1]`, or HTTP errors greater than HTTP requests is not a measurement. It is not rewritten to zero, `missing_signal`, or `unknown`, including when HTTP requests are zero. |
| Autoscaler not readable | `discovery` is `denied` or `not_attempted` and `state` is `unknown`. `absent` requires `discovery: read`. |
| Source not confirmed | `SourceContext.status` is `unverified` or `declared`. It is not `verified`. |
| Hash | Lowercase hex SHA-256 of presented bytes. `hashMeaning` is `content_immutability_not_measurement_truth`. |

JSON `null` is never used as a synonym for unknown, for a missing signal, or for an absent limit. An unset current request is `absent`, not `unknown` and not the legacy 0. Recommendation requests stay at their floors. `testedSettings` requests stay positive: at least 1 millicore and at least 1 Mi. A comparison may use 0 as the numeric current side of an absent request; that does not make 0 the stored request or a recommended request.

A package must not contain kubeconfig, tokens, authorization headers, a full environment, secret payloads, or hashes of secrets. Scenario auth and data fixtures use non-secret identifiers and `secretMaterial: excluded`. Unknown fixture conditions use `comparability: unknown`, which makes a comparison `not_comparable`. URLs reject userinfo.

## 4. Axes, reason codes, and invariants

These fields are independent. A writer sets each one. The schema then rejects combinations that contradict the table.

| Axis | Values | Does not mean |
| --- | --- | --- |
| `execution` | `completed`, `failed`, `cancelled` | Valid evidence, passed SLO, verified source, validated candidate, or validated patch. |
| `evidence` | `valid`, `insufficient`, `invalid` | A passed SLO, or that no candidate test ran. `insufficient` and `invalid` may be `rejected` and keep `testedSettings` plus comparison ids. They are not `validated`, not a recommendation or confidence, and not a ready export. |
| `target` | `verified`, `unverified`, `mismatch` | Image-to-source verification. |
| `slo` | `passed`, `failed`, `not_evaluated` | `not_evaluated` is not a pass. |
| `source` | `verified`, `declared`, `unverified`, `mismatch` | `declared` is not verified. |
| `runtimeRules` | `evaluated`, `missing_signal`, `not_applicable` | `missing_signal` is not "healthy" or "problem absent". |
| `candidate.state` | `not_tested`, `validated`, `rejected` | `not_tested` is the state before a later comparable run: `testedSettings` is null and comparison ids are absent. It is not required for every `insufficient` or `invalid` result. `rejected` keeps the tested settings and comparison ids. |
| `patch.state` | `not_requested`, `dry_run_passed`, `failed` | `dry_run_passed` is API and admission only. |
| `patch.purpose` | `none`, `experimental`, `review` | `experimental` may exist before the candidate is tested. `review` requires `validated`. |
| `gitopsExport` | `not_requested`, `blocked`, `ready` | Replay does not make an export fresh. |
| `autoscalerGate` | `clear`, `present`, `unknown` | `unknown` is not "no autoscaler". |
| `loadMode` | `rps`, `concurrency`, `observe`, `not_run` | `observe` has no SLO. |

`reasonCodes` are blockers. `limitations` are non-blocking caveats and must not be read as passed checks.

Closed reason codes: `insufficient_data`, `slo_failed`, `target_unverified`, `target_mismatch`, `identity_changed`, `source_unverified`, `source_mismatch`, `source_dirty`, `not_comparable`, `policy_blocked`, `patch_validation_failed`, `artifact_write_failed`, `cancelled`, `run_failed`, `autoscaler_unknown`, `autoscaler_present`, `candidate_rejected`, `freshness_expired`, `evidence_invalid`, `runtime_version_differs`, `runtime_settings_differ`, `condition_unknown`.

Limitations: `missing_signal`, `source_declared_not_verified`, `observe_without_slo`, `experimental_patch_not_a_candidate_test`, `confidence_is_evidence_volume`.

`missing_signal` is a limitation and a `runtimeRules` value. It is not a reason code.

Invariants the schema enforces when the triggering value is present:

- Non-completed execution: recommendation and confidence are null, candidate is `not_tested`, patch is `not_requested`, export is `not_requested`, diagnostics is an object.
- `cancelled`: SLO is `not_evaluated` and reason `cancelled` is present. `failed`: reason `run_failed` is present.
- Evidence `insufficient` or `invalid`: no recommendation, no confidence, candidate `not_tested` or `rejected`, patch not requested, export not ready, plus `insufficient_data` or `evidence_invalid`. `rejected` keeps `testedSettings` and the comparison ids. `validated` and ready export stay forbidden.
- SLO `failed`: no recommendation, candidate is `not_tested` or `rejected`, patch not requested, reason `slo_failed`.
- A recommendation object requires completed execution, valid evidence, a confidence object, current settings, at least one explanation, and either SLO `passed` or observe mode with SLO `not_evaluated`. Both current requests and both current limits on that object are `absent` or `set`.
- A confidence object requires limitation `confidence_is_evidence_volume`.
- Target `unverified` or `mismatch`, and source `mismatch`, block `validated` and ready export and require their reason codes.
- Source `unverified` requires `source_unverified`, blocks ready export, and does not by itself remove a numeric recommendation.
- Source `declared` requires limitation `source_declared_not_verified`, forbids reason `source_unverified`, and blocks ready export.
- Source `verified` forbids source unverified, mismatch, and dirty reasons, and forbids the declared limitation.
- Reason `source_dirty` requires source `mismatch`. Reason `identity_changed` requires evidence `invalid`.
- Autoscaler `unknown` or `present`: candidate `not_tested`, export not ready, matching reason code.
- `runtimeRules: missing_signal` requires limitation `missing_signal`.
- Observe mode: SLO `not_evaluated`, candidate `not_tested`, patch not requested, export not ready. A recommendation in that mode also requires `observe_without_slo`.
- Experimental `dry_run_passed`: completed, valid evidence, SLO passed, recommendation object, export not ready, candidate `not_tested` or `validated`. If the candidate is still `not_tested`, limitation `experimental_patch_not_a_candidate_test` is required.
- Patch purpose `review` requires candidate `validated`. Patch `failed` requires `patch_validation_failed` and blocks ready export.
- `validated` and `rejected` require `testedSettings`. `not_tested` requires `testedSettings` null.
- `validated` also requires completed, valid evidence, SLO passed, target verified, source other than mismatch, autoscaler `clear`, environment `staging`, load mode `rps` or `concurrency`, and none of the blocker reason codes listed in the schema. `source_unverified` is not in that blocker list.
- `rejected` requires `candidate_rejected` and either SLO failed or evidence insufficient or invalid.
- `gitopsExport: ready` requires completed, valid evidence, SLO passed, target verified, source verified, autoscaler clear, staging, candidate validated, patch `dry_run_passed` with purpose `review` and `admissionMeaning: api_admission_only`, and an empty reason-code list.

`validated` with source `declared` or `unverified` is allowed. Ready GitOps export is not. That is intentional: a staging experiment may record a candidate result before the image-to-source link is proven, and a review diff still requires `verified`.

Findings:

- `missing_signal` conclusion is `not_inferred`, with at least one missing signal and no evidence refs.
- `evaluated` conclusion is `observed`, with evidence refs and no missing signals.
- `not_applicable` conclusion is `not_applicable`.
- There is no `healthy` or `problem_absent` conclusion.
- Profile `generic` is `generic_only`, rules `not_applicable`, and `nodejs` is null.
- Profile `nodejs` is applicable. Capability `context_only` cannot contain an evaluated finding. `rules_available` needs an evaluated finding and still has aggregate `missing_signal` when any finding is missing. `telemetry_validated` needs aggregate `evaluated`, at least one evaluated finding, and no missing-signal finding.
- Node.js support status is `pinned_lts` and the LTS line is `24`. T02 selected Node.js 24 (Krypton), the Active LTS line on 2026-10-17. The reference fixture patch `24.22.0` and the base image digest live in `test/fixtures/node-http/README.md`. A synthetic fixture may still record an observed process version such as `22.14.0`. That value is example data with `dataClass: synthetic_fixture`, not a measurement and not the support pin.
- Memory counters are separate. `arrayBuffers` is a subset of `external` (`counterOverlap: array_buffers_subset_of_external`) and must not be added to it.

Source context:

- `verified` only with `confirmationMethod: reproducible_build_record`, `dirty: false`, a commit id, and `causeUse: verified_linkage`.
- An OCI revision label, a Git SHA declaration, or a manual claim is `declared` or `mismatch`, `causeUse: hypothesis_only`, never `verified`.
- `confirmation: none` is `unverified` and `causeUse: unavailable`.
- `dirty: true` is `mismatch`, `hypothesis_only`, and at least one dirty path. A reproducible build record of the commit does not make a dirty worktree verified.

Observations:

- Workload `discovery` `read` is a completed discovery. It requires the Deployment UID, generation, image digest, stability, a non-empty owner chain, and at least one pod. `discovery` `not_obtained` records a closed `unreadReason`, plus the namespace, name, and container the run asked to read. The codes are `forbidden`, `not_found`, `cancelled_before_discovery`, `timeout`, `transport_error`, and `api_server_5xx`. `forbidden` and `not_found` are Kubernetes status responses. `cancelled_before_discovery` is caller cancellation before the Deployment object was read. `timeout` is a client deadline. `transport_error` is a request that received no Kubernetes status, such as connection refused. `api_server_5xx` is an API server response in the 5xx class, including 503. The code is not a free-form message, and a raw symptom such as `connection_refused` is not a code. A timeout, a transport error, or an API server 5xx is not `partial`: `partial` requires a UID and generation from a Deployment object that was already read. `not_obtained` stores no UID, generation, digest, owner, pod, or window. Service mapping on that document is `unverified` with `routeProof` `none`. Coverage pod lists are empty and `complete` is false. `identityStable` is false. That false value means stability was not observed. It does not mean the identity changed, and it does not by itself require reason `identity_changed`.
- Workload `discovery` `partial` is a successful Deployment read followed by an incomplete pod census. It stores the UID, generation, pod selector, and replica counts taken from that Deployment object. `replicas.desired` is `spec.replicas` and `replicas.observed` is `status.replicas`. Zero is that spec or status value. It is not a substitute for a pod list that was not returned, and a status count is not cleared to zero when the list fails. `image.reference` is the template image. `image.digest` is `known` only with the digest that was actually read, or `unknown` with no value. A tag, an empty string, or a fabricated `sha256` is not a digest. `podList.discovery` `empty` means ListPods succeeded and returned no pods. `podList.discovery` `not_obtained` means the list was not returned, with `unreadReason` `list_failed` or `cancelled_before_pods`. `cancelled_before_discovery` stays on `not_obtained`, because that cancellation happened before the Deployment read. In both partial cases `pods` and `ownerChain` are empty. The empty pod array is not the census by itself: `podList.discovery` distinguishes a successful empty list from pods that were not obtained. The empty owner chain is not a ReplicaSet census and is not a claim that no ReplicaSet exists. There is no `stability` object. `identityStable` is false because stability was not established, not because a UID changed. Windows are empty. Service mapping is `unverified` with `routeProof` `none`. Coverage pod lists are empty and `complete` is false. Partial discovery cannot be joined to candidate `validated` or `gitopsExport` `ready`. That join is deferred. This schema does not execute it.
- `identityStable` is true only when discovery is `read` and UID, generation, image digest, container restart, and replica count are all unchanged. Any of those changes makes the run's evidence invalid for MVP. The deferred T05 join applies `identity_changed` only to that read case. `not_obtained` and `partial` stay false without `identity_changed`.
- Service mapping is `verified` only with `routeProof: endpoint_slice`. `declared_only` and `none` are unverified.
- A discarded window has a reason (`duplicate_poll`, `overlapping_window`, `outside_measured_interval`, `missing_pod`, `identity_changed`, `container_restart`, `conflicting_snapshot`). An accepted window has a null discard reason.
- The schema does not recompute independent counts or the max-fold. `pkg/metrics.ClassifyWindows` does that fold. T09 replays the stored inputs. The fixture window values stay pre-fold.

## 5. Versioning

| Version | v1 value | Rule |
| --- | --- | --- |
| Schema | `1.0.0` | Exact const on every document. `1.1.0` and `2.0.0` fail this schema file. |
| Policy | `policy-core-1` | The current percentile, buffers, floors, and limit modes. |
| Calculation | `recommender-core-1` | The current formula, including legacy replay floats and zero-means-absent in `replayInputs` only. |
| Profile | `g5-contract-1` | Data shape for `nodejs` and `generic`. |
| Rules | `nodejs-rules-0`, finding `ruleVersion` `0.0.0` | Rules are not executed. T12 replaces this. |
| Node support | `pinned_lts`, line `24` | The supported major. The fixture patch and base image are not this field. |
| Noise policy | JSON `null` while outcome is `not_evaluated` | A string is required only for `within_budget` or `exceeded`. |

Before the first release that ships these schemas, a later task may edit `1.0.0` together with the fixtures. After that release, an incompatible change bumps the major version and lives in a new directory. An additive change bumps the minor version. Old readers reject the unknown version because the const does not match. New fields are not silently ignored.

T02 keeps `schemaVersion` `1.0.0`. No git tag has been published, the CLI and Advisor API do not emit these documents, and the GoReleaser archives do not contain `contracts/v1`. Replacing `pending_t02` with the shared `nodeSupport` pin is an in-place clarification of the unshipped schema, together with the fixtures. It does not relax execution, evidence, source, or comparison gates.

T03 keeps `schemaVersion` `1.0.0` for the same shipping fact, rechecked on this branch: `git tag` and `git ls-remote --tags origin` are empty, and `.goreleaser.yml` archive files are `LICENSE`, `README.md`, and `web/ui/**`. Adapter ids, capabilities, actual phases, checks, and `unknown` latency are clarifications of that unshipped document. `wrapper.name` stays the field name so existing fixtures remain valid. The closed enum `builtin-get | k6-wrapper | none` is replaced by the `componentId` pattern. An id that matches the pattern is not registered until `pkg/providers/loadtest.Register` accepts it. `builtin-get` / `legacy-1` remains a valid historical identity. It is the temporary local adapter, removed in T10, not a generator this contract promises to keep.

The four T03 review corrections stay on `schemaVersion` `1.0.0`. Rechecked again before this edit: `git tag` is empty, `git ls-remote --tags origin` is empty, `gh release list` is empty, and the GoReleaser archive files are unchanged. The CLI and Advisor API still do not emit these documents. That is the delivery record. It is not an inference from the absence of users. The corrections clarify unshipped lifecycle and diagnostic representation: a failed or cancelled run may record a real phase prefix, unknown latency may accompany positive requests with `missing_signal` percentiles, and a usable measurement requires one completed measured phase plus in-range known facts. They do not add a field, and they do not relax execution, evidence, source, or comparison gates.

An unknown profile id, framework, intent, or verdict is a schema change, not an open string.

## 6. Comparison

Intents are only `code_regression` and `resource_candidate`. `runtime_change` is not an enum value.

A material condition of `unknown`, including Deployment UID or generation, forces `not_comparable`. `not_comparable` has null deltas, budget `not_evaluated`, `ciVerdict: not_comparable`, and reason `not_comparable`. It is not a green verdict.

Shared conditions that must match for both intents when the pair is comparable: logical service, scenario, load shape, replicas, runtime settings, runtime version, profile, policy, node class, auth fixture, data fixture, latency semantics, and environment role. Deployment UID and generation may match or differ.

| Intent | Allowed difference | Required match | Verdict |
| --- | --- | --- | --- |
| `code_regression` | Image digest and source revision may differ. | Resources match. No `declaredResourceChanges`. | `pass` only with budget `within_budget` and a noise policy version. `fail` only with `exceeded`. `inconclusive` only with budget `not_evaluated`. |
| `resource_candidate` | Only the declared request or limit fields. Resources must differ. | Image digest and source revision match. | Budget stays `not_evaluated` in v1. `pass` requires both SLOs `passed`. `fail` requires at least one SLO `failed`. |

`runtimeSettings: differ` forces `not_comparable` and `runtime_settings_differ`. `runtimeVersion: differ` forces `not_comparable` and `runtime_version_differs`. Those are not extra allowances of the two MVP intents.

Numeric noise percentages are not defined here. Owner: T14, after control runs in T02 and T18. The code-regression fixture is comparable and `inconclusive` for that reason. The resource-candidate fixture is comparable and `pass` because both SLOs passed, not because a noise budget passed.

`dry_run_passed` confirms API and admission. It does not confirm SLO, scheduling, apply, or production safety. An experimental dry-run is allowed before the candidate is tested and carries that limitation. A review GitOps diff requires a validated candidate. The candidate test does not require the review diff, so there is no cycle.

Replay of saved inputs does not extend freshness and does not replace an identity check at export time. `freshness_expired` blocks `validated`. Export-time recheck is T07, not this schema.

## 7. Migration without a behavior change

T01 does not add subcommands, routes, or output fields. The names below are the contract for later tasks.

Future CLI, specified and not implemented:

| Command | Meaning |
| --- | --- |
| `doctor` | Preconditions: identity, metrics access, autoscaler discovery, source mapping. No recommendation. |
| `analyze` | One run. `report_only` or `validate_export` comes from flags, not from a guessed default. |
| `replay` | Rerun `recommender-core-1` from saved `replayInputs`. Does not refresh cluster identity. |
| `compare` | T14. Reads two packages and writes a comparison result. |
| `export` | Local Kustomize or patch rendering. Review purpose only after candidate validation. |

The legacy invocation, with today's flags and no subcommand, keeps today's behavior: load test, SLO gate, server-side dry-run, `resource-patch.yaml`, and exit 1 on failure. It must not silently become report-only.

API evolution is a new `/api/v1/` surface. `POST /api/analyze` and `GET /api/runs/{id}` stay as they are, including `status` and `patchDryRun`. Reason: those two fields cannot carry independent axes, and changing them would make current clients treat `completed` as full success. The existing decoder already rejects unknown fields, so new fields on the old request body are not an additive path.

| Existing model | v1 field | Wiring task |
| --- | --- | --- |
| CLI flags in `cmd/pod-rightsizer/main.go` | `RunSpec` workload, load, SLO, policy | T04. Legacy flags stay. |
| `output.Result` JSON, YAML, and text | `Analysis` plus rendered `report.md` | T07. Stdout stays legacy until an explicit versioned output is selected. |
| `server.AnalyzeRequest`, `RunStatus` | `RunSpec`, `Analysis` under `/api/v1/` | T04 and T07. Old routes unchanged. |
| `recommender.Policy`, `Recommendations`, `ObservedStatistics`, `Confidence` | `policy`, `recommendation`, `replayInputs`, `evidenceConfidence` | T09. Formula unchanged. |
| `loadtest.RunResult`, percent flag versus ratio SLO | `observations.load`, `Analysis.slo` | T04 wires the pipeline. Numeric SLO gates already live in `pkg/providers/loadtest`. Latency remains until response headers for `builtin-get` / `legacy-1`. |
| Collector windows, `IndependentSamples`, `FullyContainedSamples` | `observations.windows` and discard reasons | T03 classifies windows in Go. The schema still does not recompute the fold. |
| `kubernetes.Workload`, `ResourceSettings`, strategic merge patch | workload identity, `resourceSetting`, `patch` | T05 and T07. `null` in YAML remains the absent-limit encoding. T03 stores a UID only when the object that was read has one. |
| `metrics.Sample` and `loadtest.Run` error-only adapters | provider outcome and metric provenance | T03. CLI and API orchestration stay on the legacy tester until T04. |
| `knowledge.Evaluate` strings | not migrated | T12 replaces them with rule records. T01 does not call them. |

Package file names, fixed: `run-spec.json`, `observations.json`, `analysis.json`, `runtime-findings.json`, `source-context.json`, `manifest.json`, `report.md`, `agent-brief.md`, `resource-patch.yaml`. Comparison documents live outside the run package.

Manifest requirements the schema can see as names, not as files on disk:

- Always: `run-spec.json`, `analysis.json`, `observations.json`. A failed or cancelled run that never read the Deployment still includes `observations.json`. That includes Forbidden, NotFound, caller cancellation, a client timeout, a transport error, and an API server 5xx. That file uses workload `discovery` `not_obtained` and one closed `unreadReason`. It does not invent a UID, generation, digest, or pod, and the manifest does not drop the file. A failed or cancelled run that read the Deployment and then got an empty pod list, a pod-list error, or a cancellation before that list still includes `observations.json`. That file uses workload `discovery` `partial`. It keeps the Deployment UID, generation, and a known image digest, and it does not invent a pod UID, an owner UID, or a stability confirmation.
- `execution: completed` also requires `runtime-findings.json`, `source-context.json`, and `report.md`.
- Completed, valid evidence, and SLO `passed` also require `agent-brief.md`.
- Patch `dry_run_passed` requires `resource-patch.yaml`. Any other patch state forbids that name.

`agent-brief.md` is required only for that successful completed case. It is optional on failure. A later task may always write it; the schema does not demand it then.

## 8. T01 boundary

In scope: this ADR, the schemas, the synthetic fixtures, `scripts/check-contracts.sh`, the dev module `tools/contractcheck`, and a CI job that runs that script.

Out of scope, and not started: the Express reference app, k6, collectors, the analysis pipeline, provider refactors, runtime rules, profiling, an assistant provider, MCP, skills, replay, comparison, GitOps engines, test Deployments, PATCH or apply, formula changes, UI, release, and site behavior.

Adjacent notes, owned by the next task rather than fixed here:

| Item | Owner |
| --- | --- |
| Node.js LTS line and base image | Decided in T02. Line `24`, fixture patch `24.22.0`, image `node:24.22.0-bookworm-slim`. See `test/fixtures/node-http/README.md`. |
| External k6, measured-phase orchestration, and deletion of the temporary `builtin-get` adapter and `pkg/loadtest`. Historical documents may keep that identity. | T10 |
| Numeric noise budget | T14 after T02 and T18 |
| Float replay equality against `GenerateRecommendations` | T09 |
| `/api/v1/` implementation | T04 and T07 |
| Manifest byte verification and secret scanning of free text | T08 |
| Cross-file joins listed in `testdata/contracts/v1/semantic-checks.json` | T04 through T16 stay `executedBy: none`. T03 names a Go test only for the window fold, the load-provider conformance suite, and metrics provenance. |
| Whether a pilot can produce `reproducible_build_record`. Until that is proven, review export stays blocked unless source is `verified`. | T16 may revisit the ready-export rule only with a schema change |

### How to check the fixtures

From the repository root:

```bash
bash scripts/check-contracts.sh
```

That runs `go run -C tools/contractcheck .`. The tool compiles every `contracts/v1/*.schema.json` with `github.com/santhosh-tekuri/jsonschema/v6` v6.0.2, `AssertFormat`, and a file-only loader. It rejects any `$ref` containing `://`. It loads `testdata/contracts/v1/catalog.json` with unknown fields rejected, requires every other JSON file under that tree to be listed, and checks each document. Invalid fixtures must fail with an error string containing every `errorContains` fragment.

All fixture documents set `dataClass` to `synthetic_fixture`. They are not cluster measurements. `manifestSha256` values and artifact hashes are lowercase hex claims. They are not computed digests of the files. T08 owns byte verification.

## What the schema guarantees

JSON Schema in this change checks structure, enums, formats, numeric bounds, required names, and the `if`/`then` contradictions above.

It does not recheck a cluster, recompute SLO or confidence, fold overlapping windows, verify hashes, prove an image was built from a commit, or decide that a candidate is safe. `semantic-checks.json` is itself schema-valid and sets `executedBy` to `none`, so it cannot claim those checks ran.
