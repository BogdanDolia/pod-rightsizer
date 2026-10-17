# Contract v1 fixtures

Every JSON document here is synthetic. `dataClass` is `synthetic_fixture`. Nothing in this tree was collected from a cluster, and the numbers are examples of the v1 shape rather than production evidence.

`manifestSha256` and artifact `sha256` values are lowercase hex claims so the schema's hash pattern can be checked. They are not digests of the files beside them. Byte verification belongs to T08. `testdata/contracts/v1/semantic-checks.json` records the cross-file checks that are not executed; its `executedBy` value is `none`.

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
| `valid/failed-run/` | Execution `failed`, evidence `invalid`, load `not_run`, autoscaler discovery not attempted so the gate is `unknown`. |
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

`valid/resource-candidate/resource-patch.yaml` is a synthetic strategic-merge example. `cpu: null` means the limit is absent. It is not an unknown quantity and it was not applied.

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

## Checks left to later tasks

`semantic-checks.json` is validated against `semantic-checks.schema.json` and does not run its statements. Examples:

- T03 must fold overlapping windows and keep `independentWindowCount` honest.
- T09 must recompute `recommender-core-1` from `replayInputs`.
- T14 must derive `checkedConditions` from the two packages. A schema-valid comparison can still lie about those conditions until that task exists.
- T08 must hash artifact bytes and compare `outcome` with `analysis.json`.
