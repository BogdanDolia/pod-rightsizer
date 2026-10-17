# Analysis contract v1

These schemas are the T01 design contract. The current CLI and Advisor API do not emit them, and this directory is not imported by the `pod-rightsizer` module.

## Draft and validator

Dialect: JSON Schema 2020-12. Each file sets `$schema` to `https://json-schema.org/draft/2020-12/schema`. That URL identifies the dialect. The checker does not fetch it. Meta-schemas are embedded in the library.

Validator: `github.com/santhosh-tekuri/jsonschema/v6` v6.0.2, in the separate module `tools/contractcheck`. It implements draft 2020-12, including `if`/`then` and format assertions. `AssertFormat` is enabled, so `uuid` and `date-time` are checked. The compiler loader allows only `file` URLs. A `$ref` that contains `://` fails before compilation.

This library is dev-only. It is not a dependency of the root `go.mod`. A partial hand-written checker would not implement the draft, so it was not used.

Shared definitions live in `defs.schema.json` and are referenced with relative `$ref`. Document-local definitions use `#/$defs`.

## Check

From the repository root:

```bash
bash scripts/check-contracts.sh
```

The script runs `go run -C tools/contractcheck .`. It compiles every `*.schema.json` in this directory, then validates `testdata/contracts/v1` using `catalog.json`.

## Documents

| Schema | Document |
| --- | --- |
| `run-spec.schema.json` | Target, workload, mode, measured phase, SLO, policy, profile |
| `observations.schema.json` | Identity, windows, coverage, load outcome, discard reasons |
| `analysis.schema.json` | Axes, recommendation, provenance |
| `runtime-findings.schema.json` | Profile capability and findings data |
| `source-context.schema.json` | Image and source mapping |
| `manifest.schema.json` | Versions and artifact names |
| `comparison-request.schema.json` | Future T14 request |
| `comparison-result.schema.json` | Future T14 result |
| `semantic-checks.schema.json` | Catalog of checks this schema does not run |
| `defs.schema.json` | Shared definitions, not a document |

`schemaVersion` is the exact string `1.0.0`. Unknown fields are rejected.

## What validation guarantees

The schemas check types, enums, formats, ranges, required properties, local references, and the contradictions encoded with `if`/`then`. Examples: a failed SLO cannot carry a recommendation object; `missing_signal` cannot carry a number or a `problem_absent` conclusion; an OCI label cannot be `verified`; `code_regression` cannot pass without a noise-policy outcome; a review export cannot be `ready` before candidate validation. An unread workload cannot carry a UID or `identityStable: true`. A partial discovery keeps the Deployment UID, generation, selector, and replica counts from that object, and an image digest only when `image.digest.state` is `known`. `podList.discovery` `empty` is a successful empty census. `podList.discovery` `not_obtained` is a list that was not returned. Partial discovery cannot set `identityStable` true, and it cannot carry a pod UID, an owner UID, a stability object, or a digest on `unknown`. Completed discovery `read` still requires a non-empty owner chain and at least one pod. Partial discovery cannot be a validated candidate or a ready export. That join is listed in `semantic-checks.json` and is not executed here. An unset request is `absent`, not the number 0. A CPU limit comparison cannot carry `memoryBytes`, and a memory limit comparison cannot carry `cpuMilli`. `absent` still has no number. `unknown` current limits cannot sit beside a recommendation. A generator that did not start has `requests` and `httpErrors` 0, null rates, `missing_signal` latency, and a null interval. A load that started and was then cancelled keeps `executed` true and may keep partial measurements. `evidence` `insufficient` or `invalid` can still record `candidate.state` `rejected` with `testedSettings` and comparison ids. That result cannot be `validated` or `gitopsExport` `ready`, and its recommendation and confidence stay null.

Validation does not recompute the recommendation, SLO, or confidence. It does not fold metric windows, read a cluster, verify hashes, or prove that an image was built from a commit. Those checks are listed in `testdata/contracts/v1/semantic-checks.json` with `executedBy: none` and an owner task.

See `docs/adr/0001-analysis-contract.md` for units, axes, and migration.
