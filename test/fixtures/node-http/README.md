# Node HTTP reference fixture

Controlled cart-quote HTTP service for later Pod Rightsizer measurement work. It is a synthetic fixture, not a production service and not the pilot workload. T18 chooses the real pilot.

The Go CLI, Advisor API, recommendation formulas, providers, and the existing dry-run end-to-end workload are unchanged. This directory does not implement the k6 adapter (T10), runtime profile import (T12), comparison engine (T14), or a full measured Pod Rightsizer run (T17).

## Pinned toolchain

Chosen on 2026-10-17 from the Node.js release schedule:

| Piece | Pin |
| --- | --- |
| Support line | Node.js 24, Active LTS, codename Krypton |
| Fixture patch | 24.22.0 |
| npm | 11.19.1, the npm shipped with that patch |
| Express | 5.3.0 |
| TypeScript | 7.0.2 |
| `@types/express` | 5.0.6 |
| `@types/node` | 24.19.2 |
| k6 | 1.8.2 |
| Base image | `node:24.22.0-bookworm-slim@sha256:a5f4366e834695d5f363b920c104953a535d3a9864d1bdf74566a0ebe01daee5` |

Node.js 22 was not selected. It is Maintenance LTS, and the contract fixtures already use `22.14.0` as an example of an observed process version. Node.js 26 was not selected. On this date it was still Current; Active LTS for 26 starts on 2026-10-28. The support object stores only the line `24`. The patch and the image digest stay here and in the Dockerfile.

Local, CI, and the container all use Node.js 24.22.0. Dependencies are exact and installed with `npm ci`. The package manager pin is `npm@11.19.1`. The process runs compiled JavaScript with source maps enabled. It does not use ts-node, tsx, or a file watcher.

## Application

`POST /v1/quote` prices a cart from a fixed catalog. Prices are integer cents. Tax is 800 basis points, rounded half up with `floor((subtotal * 800 + 5000) / 10000)`. There is no database, clock, or random result.

The scenario cart is two apples, one bread, and three milks:

| Field | Cents |
| --- | --- |
| subtotal | 1057 |
| tax | 85 |
| total | 1142 |

Catalog cents: apple 125, bread 240, milk 189, cheese 640. The body may contain only `items`, and each item only `sku` and `quantity`. Quantity is an integer from 1 to 20, with 1 to 8 lines and no duplicate SKU. Unknown SKUs and malformed JSON return HTTP 400. Health is not the business check.

`GET /healthz` returns `{"status":"ok"}`. `GET /readyz` returns `{"status":"ready"}` until shutdown, then HTTP 503 `{"status":"draining"}`. On SIGTERM or SIGINT the process marks itself not ready, keeps serving until in-flight connections finish, and exits 0. `NODE_HTTP_SHUTDOWN_GRACE_MS` (default 5000, range 100 to 10000) force-exits if work is still running.

## Regression modes

One image contains every mode. The mode is selected at process start with `NODE_HTTP_MODE` and `NODE_HTTP_INTENSITY`, then the process is restarted. There is no admin API. Baseline is the default: mode `baseline`, intensity `0`. Other modes use an integer intensity from 1 to 8. The quote inputs stay the same. The effective mode is in `/metadata` and in the quote's `regression` object.

Invalid values are rejected before listen. The process exits 2 and writes `invalid configuration:` to stderr. Rejected input includes an unknown mode, a blank value, NaN, Infinity, a fraction, a negative number, intensity outside the mode's range, and surrounding spaces. Baseline intensity must be 0.

| Mode | Work in the HTTP process | Cap |
| --- | --- | --- |
| baseline | Quote only | No heavy slot |
| cpu | Synchronous loop on the request thread. Each intensity step is 8,000,000 iterations. Each iteration updates one slot of a 1024-element `Uint32Array` with `Math.imul`. The digest is returned. This is not a sleep | 64,000,000 iterations per request |
| heap | Ordinary objects, 1000 per intensity, each with 16 numeric cells that are written and read. Accounting budget is 256 bytes per object, which is an application budget, not a V8 size | 2 MiB retained |
| buffer | `Buffer.allocUnsafe`, 128 KiB per intensity. Every 4096th byte and the last byte are written and read back, so the memory is touched. Node accounts this in `arrayBuffers`, which is already inside `external` | 2 MiB retained |
| latency | `setTimeout` for 40 ms per intensity. The event loop is not spun | 320 ms per request |

On this development machine, intensity 8 of the CPU loop took about 200 ms and intensity 4 took about 100 ms. That is a local observation, not a noise budget and not a required CI number.

At most 8 heavy operations run at once. One more is rejected immediately with HTTP 429 `capacity_exceeded` and `behavior: reject_immediately`. Nothing is queued. Retention drops the oldest blocks until the new block fits. A single block larger than the cap is not retained (`single_block_exceeds_cap`) and the quote is still returned. There is no process-lifetime leak.

## Metadata and telemetry

`GET /metadata` is `node-http-metadata/v1`. It reports the logical service `node-http-reference`, the repository-relative app root `test/fixtures/node-http`, the running Node, V8, platform, and architecture, and a process identity made of a UUID, the PID, and the start time from `performance.timeOrigin`. The PID alone is not the identity. It also reports the effective mode, intensity, and the non-secret limits above.

The build section comes from `dist/build-manifest.json` (`node-http-build-manifest/v1`), which is loaded from beside the compiled server. `buildId` is the SHA-256 of the canonical manifest body without `buildId`. The body hashes each compiled `.js` and sibling `.map`. Source revision and tree state are the `SOURCE_REVISION` and `SOURCE_TREE_STATE` build inputs (`unknown`, or a 40-character lowercase git SHA, and `clean`, `dirty`, or `unknown`). The method is `explicit_build_input`. `verifiedLinkage` is always false. A git SHA, an OCI label, or a matching metadata line does not verify source linkage.

`GET /telemetry` is `node-http-telemetry/v1`, collected inside the measured process. It is raw diagnostics. It is not a RuntimeFindings document and it does not mean healthy, a validated candidate, or `telemetry_validated`.

| Signal | Meaning |
| --- | --- |
| `snapshot.memory` | Instant `process.memoryUsage()` bytes: `heapUsed`, `heapTotal`, `rss`, `external`, `arrayBuffers`. `arrayBuffers` is included in `external` and is not added to it. RSS is not container memory |
| `snapshot.retention` | Instant gauge of retained blocks and application-accounted bytes. A heap block is one request's object array, not one object |
| `cumulative.eventLoopDelay` | `perf_hooks.monitorEventLoopDelay` since enable. Delays are nanoseconds. Resolution is 10 ms. `resetOnRead` is false, and a scrape does not reset the histogram. An empty histogram is `missing_signal` / `histogram_empty` with no numeric delay |
| `cumulative.eventLoopUtilization` | `perf_hooks.eventLoopUtilization` delta since collector start. `idle` and `active` are high-resolution milliseconds. `utilization` is a ratio from 0 to 1. Zero idle and zero active is `missing_signal` / `no_elapsed_time`, not a known zero |
| `interval.eventLoopUtilization` | Delta across the last completed 1000 ms sample. The two-argument call uses the later sample first and the earlier sample second, matching Node.js 24.22.0. Two scrapes in the same interval share `startedAt`. Before the first interval completes the state is `missing_signal` / `interval_not_complete` |
| `settings.maxOldSpaceSize` | Parsed from `process.execArgv` when `--max-old-space-size` is set. The unit is mebibytes. That flag sizes V8 old space. It is not the container memory limit. The percentage flag is reported separately and is not converted to an invented byte value |
| `settings.v8HeapSizeLimit` | `v8.getHeapStatistics().heap_size_limit` in bytes. It is not the flag |
| `missingSignals.gc` | `not_collected` |
| `missingSignals.containerMemory` | `not_collected`. RSS is not a substitute |

The endpoint does not publish the full environment, raw argv, or request headers.

## Source maps

TypeScript emits a sibling map with `sourcesContent`. The manifest records the SHA-256 of the JavaScript, the map, and each embedded source. A test throws from compiled `stacktrace-probe.js` under `node --enable-source-maps` and requires the stack to name `stacktrace-probe.ts` at the source line of `controlled-stack-probe`. A map or JavaScript file whose bytes no longer match the manifest is not this build. The probe is not an HTTP route.

The image digest is written to an experiment record after `docker build`. It is not inserted back into the image.

## Build

From this directory, with the two build inputs set:

```bash
export SOURCE_REVISION=unknown
export SOURCE_TREE_STATE=unknown
npm ci
npm run build
```

`SOURCE_REVISION` and `SOURCE_TREE_STATE` are part of the manifest. Leave them unset and the build records `unknown`. Repeating the build with the same inputs reproduces the compiled JavaScript, source maps, and manifest bytes. That does not prove the OCI image is byte-for-byte reproducible. The image id can be run again after it is built.

Container build, from the repository root. The context is this directory, not the repository:

```bash
docker build \
  --build-arg SOURCE_REVISION=unknown \
  --build-arg SOURCE_TREE_STATE=unknown \
  --tag node-http-reference:local \
  --file test/fixtures/node-http/Dockerfile \
  test/fixtures/node-http
```

The Dockerfile is multi-stage. Both build stages and the runtime stage use the digest above. The runtime stage copies production `node_modules` and `dist`, then runs as the image's `node` user. It does not copy the git checkout, tests, or local reports. The official image still contains the Node.js toolchain; application dependencies are production-only.

## Install the external k6 runner

k6 is a separate, unchanged upstream program. It is not imported into the Go module, copied into the Express image, or bundled in Pod Rightsizer release archives. The fixture runner pin is `scenario/k6-version`; archive SHA-256 values are pinned in `scenario/k6-checksums.txt` from the [upstream v1.8.2 release](https://github.com/grafana/k6/releases/tag/v1.8.2).

From the repository root on Linux or macOS, amd64 or arm64:

```bash
bash scripts/install-fixture-k6.sh "$PWD/.tools/k6/bin"
export PATH="$PWD/.tools/k6/bin:$PATH"
k6 version
```

The installer requires `curl`, a SHA-256 utility (`sha256sum` or `shasum`), and `tar` on Linux or `unzip` on macOS. It downloads only the pinned official archive, verifies its committed checksum, and installs the binary in the requested directory. Updating k6 requires reviewing its version, archive checksums, scenario compatibility, and license together. CI runs the same installer in its temporary directory. Checks do not install k6 automatically, and a missing or wrong-version runner is an error rather than a fallback to the built-in Go tester.

Pod Rightsizer is licensed under [Apache 2.0](../../../LICENSE). The separately installed k6 1.8.2 is licensed under [AGPLv3](https://github.com/grafana/k6/blob/v1.8.2/LICENSE.md), and its license applies to that upstream program. This integration invokes the documented CLI; it does not modify or embed k6. Redistributing k6 or modifying/embedding its code requires a separate review of the applicable license obligations. Local execution does not require a Grafana Cloud account.

## Checks

From the repository root:

```bash
bash scripts/check-node-http.sh
```

That is the fast level: the Node and npm pins, `npm ci`, the fixture tests, and a second build whose JavaScript, maps, and manifest hashes match the first. It prints `RESULT level=fast`. Missing Docker, k6, or kind is listed as `UNVERIFIED` and is not a pass.

After the fast check has built the fixture and test helpers, run the standalone k6 smoke check:

```bash
npm --prefix test/fixtures/node-http run test:k6
```

This uses the separately installed runner to execute the real scenario against the baseline and then against a local HTTP 200 stub that changes only the total. The baseline must exit successfully; the stub run must exit unsuccessfully with failed measured application checks and no HTTP errors. Summaries, raw metrics, and logs are saved under `.experiments/k6-smoke-*`. The smoke check cleans up both servers and fails on a missing/wrong-version runner or a k6 timeout. It does not run Docker, kind, or performance comparisons. CI requires both the fast and k6 smoke levels.

```bash
bash scripts/check-node-http.sh --experiments
```

adds the image build, two container runs, SIGTERM, a non-root process check, two baseline k6 runs, one k6 run for each regression mode, and a wrong HTTP 200 answer that must fail the scenario. With a usable Docker daemon the k6 runs target `node-http-reference:local`. Each mode is checked through its quote payload and the telemetry of that same process. p95 and RSS are not regression gates. Raw summaries and metadata are written under `test/fixtures/node-http/.experiments/`, which is gitignored.

```bash
bash scripts/check-node-http.sh --kind
```

also creates or reuses the disposable kind cluster `pod-rightsizer-node-http`, loads the local image, applies `manifests/overlays/baseline`, and checks one replica, a numeric non-root uid, and the quote. The pod sets `runAsNonRoot` with `runAsUser` 1000 because the image user name `node` is not a uid Kubernetes can verify by itself. If this command created the cluster, it deletes that cluster and restores the previous kubectl context even when the deployment does not become ready. It uses kubectl context `kind-pod-rightsizer-node-http` only. It does not touch the dry-run pause workload in namespace `pod-rightsizer-e2e`, and it does not change any other cluster.

Manifests are a fixture example: one container named `node-http`, one replica, an exact selector, and small requests and limits. Those numbers are not a production recommendation. `manifests/overlays/cpu`, `heap`, `buffer`, and `latency` set intensity 4. Switching mode with an environment variable changes runtime settings. That shows the fixture is controllable. It is not a `code_regression` comparison. A `resource_candidate` comparison keeps the image, source, and runtime settings the same and changes only declared resources. T14 owns the noise policy and the CI verdict. This fixture does not emit one.

## k6 scenario

`scenario/quote.js` is scenario `node-http-quote` version 1. The runner is k6 1.8.2. Warm-up is 3 seconds and the measured phase is the following 10 seconds, both at 5 iterations per second. Thresholds apply only to `scenario:measured`: every check passes, no failed HTTP requests, no dropped iterations, and `http_req_duration` p95 is under 2000 ms. The 2000 ms line is a smoke ceiling for this fixture, not a service SLO.

`http_req_duration` includes sending the request, waiting, and receiving the body. It is not the legacy Pod Rightsizer `until_response_headers` measurement, which stops when headers arrive.

`dropped_iterations` counts arrival-rate iterations the runner did not start. A non-zero count means the planned load did not run. It is not evidence that the service met a rate.

Checks require status 200, the currency, the line count, subtotal, tax, total, mode, and intensity. k6 checks alone do not fail the process. The thresholds do. `test/wrong-answer-stub.ts` returns HTTP 200 with the wrong total and must produce a non-zero k6 exit. It is not in the image.

Example, after the server is listening:

```bash
k6 run \
  --summary-export /tmp/node-http-summary.json \
  --out json=/tmp/node-http-raw.json \
  -e BASE_URL=http://127.0.0.1:8080 \
  -e EXPECTED_MODE=baseline \
  -e EXPECTED_INTENSITY=0 \
  scenario/quote.js
```

Run that twice against one process or one container for the baseline repeat. For a regression mode, start a new process with `NODE_HTTP_MODE` and `NODE_HTTP_INTENSITY=4` and set the matching `EXPECTED_*` values. Do not treat a higher p95 or a larger `arrayBuffers` reading as a universal regression percentage.

## Later owners

| Task | Owns |
| --- | --- |
| T03/T04 | Engine-independent load-provider types, capabilities, common SLO evaluation, and CLI/API orchestration through that interface |
| T10 | External unchanged k6 CLI adapter, canonical scenario integration, typed outcome, measured-phase/metrics linkage, and removal of the built-in Go generator/local adapter without a compatibility period or runtime fallback |
| T12 | Runtime profile, `runtime.json` import, and executable Node.js rules |
| T14 | Comparison engine, noise policy, and the CI regression verdict |
| T17 | Full measured Pod Rightsizer end-to-end run |
| T18 | Pilot service choice and the resource and noise budget for that service |
