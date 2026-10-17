#!/usr/bin/env bash
# Fast functional checks for the Node HTTP reference fixture.
# Docker, k6, and kind are separate levels. A skipped tool is not a pass.
set -euo pipefail
set +m

root="$(cd "$(dirname "$0")/.." && pwd)"
fixture="$root/test/fixtures/node-http"
experiments=0
run_kind=0

for arg in "$@"; do
  case "$arg" in
    --experiments) experiments=1 ;;
    --kind) run_kind=1; experiments=1 ;;
    -h|--help)
      echo "usage: bash scripts/check-node-http.sh [--experiments] [--kind]"
      exit 0
      ;;
    *)
      echo "unknown argument: $arg" >&2
      exit 2
      ;;
  esac
done

export SOURCE_REVISION=unknown
export SOURCE_TREE_STATE=unknown

fail=0
unverified=()
use_image=0

say() {
  printf '%s\n' "$*"
}

if ! command -v node >/dev/null 2>&1 || [[ "$(node --version)" != "v24.22.0" ]]; then
  node_version="$(node --version 2>/dev/null || echo missing)"
  say "FAIL Node.js v24.22.0 is required, found $node_version"
  exit 1
fi

if ! command -v npm >/dev/null 2>&1 || [[ "$(npm --version)" != "11.19.1" ]]; then
  npm_version="$(npm --version 2>/dev/null || echo missing)"
  say "FAIL npm 11.19.1 is required, found $npm_version"
  exit 1
fi

hash_dist() {
  node --input-type=module -e '
import { createHash } from "node:crypto";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
function walk(dir, out = []) {
  for (const name of readdirSync(dir).sort()) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) walk(path, out);
    else out.push(path);
  }
  return out;
}
for (const path of walk("dist")) {
  const hash = createHash("sha256").update(readFileSync(path)).digest("hex");
  console.log(`${hash}  ${path.split("\\\\").join("/")}`);
}
'
}

cd "$fixture"
say "level=fast"
npm ci
npm test

first="$(mktemp)"
second="$(mktemp)"
hash_dist > "$first"
npm run build
hash_dist > "$second"
if ! diff -u "$first" "$second"; then
  say "FAIL JavaScript, source maps, or build manifest changed between two builds with the same inputs"
  fail=1
else
  say "js_maps_manifest=reproduced"
fi
rm -f "$first" "$second"

if [[ "$experiments" -eq 0 ]]; then
  say "UNVERIFIED docker container smoke and SIGTERM: not requested; not a pass"
  say "UNVERIFIED k6 scenario, wrong-answer gate, baseline repeat, and regression modes: not requested; not a pass"
  say "UNVERIFIED kind deploy: not requested; not a pass"
  say "RESULT level=fast functional=pass"
  say "This command did not accept the T02 container, k6, or kind requirements."
  exit "$fail"
fi

out="$fixture/.experiments/$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$out"
say "experiment_output=$out"

k6_version="$(tr -d '[:space:]' < scenario/k6-version)"
if ! command -v k6 >/dev/null 2>&1 || ! k6 version | grep -q "v${k6_version}"; then
  say "FAIL k6 v${k6_version} is required for --experiments and was not found"
  fail=1
  unverified+=("k6")
else
  say "k6=$(k6 version)"
fi

if ! docker info >/dev/null 2>&1; then
  say "FAIL docker is required for --experiments and is not usable"
  fail=1
  unverified+=("docker")
else
  say "docker_build=start"
  docker build \
    --build-arg SOURCE_REVISION=unknown \
    --build-arg SOURCE_TREE_STATE=unknown \
    --tag node-http-reference:local \
    --file "$fixture/Dockerfile" \
    "$fixture"
  image_id="$(docker image inspect node-http-reference:local --format '{{.Id}}')"
  say "image_id=$image_id"
  cat > "$out/image-record.json" <<EOF
{
  "imageRef": "node-http-reference:local",
  "imageId": "${image_id}",
  "recordedOutsideImage": true,
  "note": "The image digest is recorded here after the build. It is not baked into the image."
}
EOF

  run_container() {
    local name="$1"
    local cid
    cid="$(docker run -d --name "$name" -p 127.0.0.1::8080 node-http-reference:local)"
    local mapped
    mapped="$(docker port "$cid" 8080/tcp | head -n 1 | sed 's/.*://')"
    local deadline=$((SECONDS + 20))
    until curl -fsS "http://127.0.0.1:${mapped}/readyz" >/dev/null 2>&1; do
      if (( SECONDS > deadline )); then
        docker logs "$cid" || true
        echo "container did not become ready" >&2
        return 1
      fi
      sleep 0.2
    done
    printf '%s %s\n' "$cid" "$mapped"
  }

  first_run="$(run_container "node-http-ref-a-$$")"
  cid_a="${first_run%% *}"
  port_a="${first_run##* }"
  curl -fsS "http://127.0.0.1:${port_a}/metadata" > "$out/container-a-metadata.json"
  curl -fsS "http://127.0.0.1:${port_a}/v1/quote" \
    -H 'content-type: application/json' \
    --data-binary @"$fixture/scenario/quote-input.json" > "$out/container-a-quote.json"
  node --input-type=module -e '
import { readFileSync } from "node:fs";
const quote = JSON.parse(readFileSync(process.argv[1], "utf8"));
const metadata = JSON.parse(readFileSync(process.argv[2], "utf8"));
const imageId = process.argv[3];
if (quote.totalCents !== 1142) process.exit(1);
if (metadata.build.verifiedLinkage !== false) process.exit(1);
if (JSON.stringify(metadata).includes(imageId)) process.exit(1);
' "$out/container-a-quote.json" "$out/container-a-metadata.json" "$image_id"
  uid="$(docker exec "$cid_a" id -u | tr -d '[:space:]')"
  node_in_image="$(docker exec "$cid_a" node --version | tr -d '[:space:]')"
  cmdline="$(docker exec "$cid_a" sh -c 'tr "\0" " " < /proc/1/cmdline')"
  say "container_uid=$uid"
  say "container_node=$node_in_image"
  say "container_cmdline=$cmdline"
  if [[ "$uid" == "0" ]]; then
    say "FAIL container process is root"
    fail=1
  fi
  if [[ "$node_in_image" != "v24.22.0" ]]; then
    say "FAIL container Node.js is $node_in_image"
    fail=1
  fi
  if [[ "$cmdline" != *"dist/server.js"* ]]; then
    say "FAIL container command is not the compiled server"
    fail=1
  fi
  if docker exec "$cid_a" sh -c 'command -v k6 >/dev/null 2>&1'; then
    say "FAIL k6 binary is inside the fixture image"
    fail=1
  else
    say "image_k6=absent"
  fi
  docker stop -t 10 "$cid_a" >/dev/null
  exit_a="$(docker inspect "$cid_a" --format '{{.State.ExitCode}}')"
  say "container_a_exit=$exit_a"
  if [[ "$exit_a" != "0" ]]; then
    say "FAIL container SIGTERM exit code was $exit_a"
    fail=1
  fi
  docker rm "$cid_a" >/dev/null

  second_run="$(run_container "node-http-ref-b-$$")"
  cid_b="${second_run%% *}"
  port_b="${second_run##* }"
  curl -fsS "http://127.0.0.1:${port_b}/metadata" > "$out/container-b-metadata.json"
  node --input-type=module -e '
import { readFileSync } from "node:fs";
const local = JSON.parse(readFileSync("dist/build-manifest.json", "utf8"));
const a = JSON.parse(readFileSync(process.argv[1], "utf8"));
const b = JSON.parse(readFileSync(process.argv[2], "utf8"));
if (a.build.buildId !== local.buildId || b.build.buildId !== local.buildId) {
  console.error("container build id does not match the local manifest");
  process.exit(1);
}
if (a.process.id === b.process.id) {
  console.error("two containers reused one process id");
  process.exit(1);
}
' "$out/container-a-metadata.json" "$out/container-b-metadata.json"
  docker stop -t 10 "$cid_b" >/dev/null
  docker rm "$cid_b" >/dev/null
  say "container_smoke=pass"
  use_image=1
fi

if [[ " ${unverified[*]-} " != *" k6 "* ]]; then
  start_target() {
    local mode="$1"
    local intensity="$2"
    local log="$3"
    if [[ "$use_image" -eq 1 ]]; then
      local name="node-http-k6-${mode}-$$"
      local cid mapped deadline
      cid="$(docker run -d --name "$name" \
        -e "NODE_HTTP_MODE=${mode}" \
        -e "NODE_HTTP_INTENSITY=${intensity}" \
        -p 127.0.0.1::8080 \
        node-http-reference:local)"
      mapped=""
      deadline=$((SECONDS + 20))
      until [[ -n "$mapped" ]] && curl -fsS "http://127.0.0.1:${mapped}/readyz" >/dev/null 2>&1; do
        mapped="$(docker port "$cid" 8080/tcp 2>/dev/null | head -n 1 | sed 's/.*://')"
        if (( SECONDS > deadline )); then
          docker logs "$cid" >"$log" 2>&1 || true
          echo "container ${mode} did not become ready" >&2
          return 1
        fi
        sleep 0.2
      done
      docker logs "$cid" >"$log" 2>&1 || true
      printf 'container %s %s\n' "$cid" "$mapped"
      return 0
    fi
    NODE_HTTP_MODE="$mode" NODE_HTTP_INTENSITY="$intensity" PORT=0 \
      node --enable-source-maps dist/server.js >"$log" 2>&1 &
    local pid=$!
    local deadline=$((SECONDS + 10))
    local port=""
    while (( SECONDS < deadline )); do
      port="$(sed -n 's/.*listening port=\([0-9][0-9]*\).*/\1/p' "$log" | head -n 1)"
      if [[ -n "$port" ]]; then
        printf 'process %s %s\n' "$pid" "$port"
        return 0
      fi
      if ! kill -0 "$pid" 2>/dev/null; then
        return 1
      fi
      sleep 0.1
    done
    return 1
  }
  stop_target() {
    local kind="$1"
    local id="$2"
    if [[ "$kind" == "container" ]]; then
      docker stop -t 10 "$id" >/dev/null
      local exit_code
      exit_code="$(docker inspect "$id" --format '{{.State.ExitCode}}')"
      docker rm "$id" >/dev/null
      printf '%s\n' "$exit_code"
      return 0
    fi
    kill -TERM "$id" || true
    wait "$id" || true
    printf '0\n'
  }
  wait_port() {
    local log="$1"
    local pid="$2"
    local deadline=$((SECONDS + 10))
    local port=""
    while (( SECONDS < deadline )); do
      port="$(sed -n 's/.*listening port=\([0-9][0-9]*\).*/\1/p' "$log" | head -n 1)"
      if [[ -n "$port" ]]; then
        printf '%s\n' "$port"
        return 0
      fi
      if ! kill -0 "$pid" 2>/dev/null; then
        return 1
      fi
      sleep 0.1
    done
    return 1
  }
  run_k6() {
    local name="$1"
    local base="$2"
    local mode="$3"
    local intensity="$4"
    local summary="$out/${name}-summary.json"
    set +e
    k6 run \
      --summary-export "$summary" \
      --out "json=$out/${name}-raw.json" \
      -e "BASE_URL=$base" \
      -e "EXPECTED_MODE=$mode" \
      -e "EXPECTED_INTENSITY=$intensity" \
      scenario/quote.js > "$out/${name}-stdout.txt" 2>&1
    local status=$?
    set -e
    printf '%s\n' "$status"
  }
  if [[ "$use_image" -eq 1 ]]; then
    say "k6_target=image node-http-reference:local"
  else
    say "k6_target=local_node"
    say "UNVERIFIED k6 against the built image: docker image was not available; local Node is not that proof"
  fi
  target="$(start_target baseline 0 "$out/baseline-server.log")"
  kind="${target%% *}"
  rest="${target#* }"
  id="${rest%% *}"
  port="${rest##* }"
  base="http://127.0.0.1:${port}"
  curl -fsS "$base/metadata" > "$out/baseline-metadata.json"
  status="$(run_k6 baseline-1 "$base" baseline 0)"
  say "k6_baseline_1_exit=$status"
  if [[ "$status" != "0" ]]; then
    say "FAIL baseline k6 run 1 exited $status"
    fail=1
  fi
  status="$(run_k6 baseline-2 "$base" baseline 0)"
  say "k6_baseline_2_exit=$status"
  if [[ "$status" != "0" ]]; then
    say "FAIL baseline k6 run 2 exited $status"
    fail=1
  fi
  curl -fsS "$base/v1/quote" \
    -H 'content-type: application/json' \
    --data-binary @"$fixture/scenario/quote-input.json" > "$out/baseline-quote.json"
  curl -fsS "$base/telemetry" > "$out/baseline-telemetry.json"
  stop_code="$(stop_target "$kind" "$id")"
  say "baseline_target_exit=$stop_code"

  for mode in cpu heap buffer latency; do
    target="$(start_target "$mode" 4 "$out/${mode}-server.log")"
    kind="${target%% *}"
    rest="${target#* }"
    id="${rest%% *}"
    port="${rest##* }"
    base="http://127.0.0.1:${port}"
    curl -fsS "$base/metadata" > "$out/${mode}-metadata.json"
    status="$(run_k6 "$mode" "$base" "$mode" 4)"
    say "k6_${mode}_exit=$status"
    if [[ "$status" != "0" ]]; then
      say "FAIL ${mode} k6 run exited $status"
      fail=1
    fi
    curl -fsS "$base/v1/quote" \
      -H 'content-type: application/json' \
      --data-binary @"$fixture/scenario/quote-input.json" > "$out/${mode}-quote.json"
    curl -fsS "$base/telemetry" > "$out/${mode}-telemetry.json"
    stop_code="$(stop_target "$kind" "$id")"
    say "${mode}_target_exit=$stop_code"
  done

  stub_log="$out/wrong-answer.log"
  PORT=0 node dist-test/test/wrong-answer-stub.js >"$stub_log" 2>&1 &
  stub_pid=$!
  stub_port="$(wait_port "$stub_log" "$stub_pid")"
  set +e
  k6 run \
    --summary-export "$out/wrong-answer-summary.json" \
    --out "json=$out/wrong-answer-raw.json" \
    -e "BASE_URL=http://127.0.0.1:${stub_port}" \
    -e EXPECTED_MODE=baseline \
    -e EXPECTED_INTENSITY=0 \
    scenario/quote.js > "$out/wrong-answer-stdout.txt" 2>&1
  wrong_status=$?
  set -e
  kill -TERM "$stub_pid" || true
  wait "$stub_pid" || true
  say "k6_wrong_answer_exit=$wrong_status"
  if [[ "$wrong_status" == "0" ]]; then
    say "FAIL wrong HTTP 200 answer did not fail the k6 scenario"
    fail=1
  fi
  node --input-type=module -e '
import { readFileSync } from "node:fs";
const summary = JSON.parse(readFileSync(process.argv[1], "utf8"));
const key = Object.keys(summary.metrics ?? {}).find((name) => name.startsWith("checks") && name.includes("scenario:measured"));
const metric = key ? summary.metrics[key] : undefined;
const fails = metric?.fails ?? metric?.values?.fails;
if (typeof fails !== "number" || !(fails > 0)) {
  console.error("wrong answer did not fail measured checks");
  process.exit(1);
}
' "$out/wrong-answer-summary.json"

  if [[ "$fail" -eq 0 ]]; then
    if [[ "$use_image" -eq 1 ]]; then
      say "baseline_pair image=node-http-reference:local scenario=node-http-quote/1 mode=baseline intensity=0 runs=2"
    else
      say "baseline_pair image=local_node scenario=node-http-quote/1 mode=baseline intensity=0 runs=2"
    fi
    node --input-type=module -e '
import { readFileSync } from "node:fs";
const out = process.argv[1];
function read(name) {
  return JSON.parse(readFileSync(`${out}/${name}`, "utf8"));
}
function fail(message) {
  console.error(message);
  process.exit(1);
}
const modes = ["baseline", "cpu", "heap", "buffer", "latency"];
const builds = new Set();
const processIds = new Set();
for (const mode of modes) {
  const metadata = read(`${mode}-metadata.json`);
  const telemetry = read(`${mode}-telemetry.json`);
  const quote = read(`${mode}-quote.json`);
  const regression = quote.regression;
  const intensity = mode === "baseline" ? 0 : 4;
  if (quote.subtotalCents !== 1057 || quote.taxCents !== 85 || quote.totalCents !== 1142) fail(`${mode} quote totals`);
  if (regression.mode !== mode || regression.intensity !== intensity) fail(`${mode} regression identity`);
  if (metadata.regression.mode !== mode || metadata.regression.intensity !== intensity) fail(`${mode} metadata identity`);
  if (metadata.process.id !== telemetry.identity.processId || metadata.process.pid !== telemetry.identity.pid) fail(`${mode} process identity`);
  if (metadata.process.startedAt !== telemetry.identity.startedAt) fail(`${mode} start time`);
  if (!metadata.process.id || metadata.process.id === String(metadata.process.pid)) fail(`${mode} pid is the only identity`);
  if (metadata.build.buildId !== telemetry.build.buildId || metadata.build.verifiedLinkage !== false) fail(`${mode} build linkage`);
  if (metadata.node.version !== "v24.22.0") fail(`${mode} node version`);
  const memory = telemetry.snapshot.memory;
  if (!(memory.arrayBuffersBytes <= memory.externalBytes)) fail(`${mode} arrayBuffers is not inside external`);
  if (memory.rssIsNotContainerMemory !== true) fail(`${mode} rss presented as container memory`);
  if (telemetry.missingSignals.gc.state !== "missing_signal" || telemetry.missingSignals.containerMemory.state !== "missing_signal") fail(`${mode} missing signal encoded as a number`);
  const oldSpace = telemetry.settings.oldSpace.maxOldSpaceSize;
  const heapLimit = telemetry.settings.v8HeapSizeLimit;
  if (oldSpace.appliesTo !== "v8_old_space" || heapLimit.source !== "v8.getHeapStatistics().heap_size_limit" || heapLimit.unit !== "bytes") fail(`${mode} old space confused with heap limit`);
  if (oldSpace.state === "unset" && Object.hasOwn(oldSpace, "bytes")) fail(`${mode} unset old space has a byte value`);
  if (oldSpace.state === "known" && (oldSpace.unit !== "mebibytes" || oldSpace.bytes !== oldSpace.value * 1048576)) fail(`${mode} old space unit`);
  builds.add(metadata.build.buildId);
  processIds.add(metadata.process.id);
}
if (builds.size !== 1) fail("baseline and regression runs did not share one build");
if (processIds.size !== modes.length) fail("mode runs did not use separate processes");
const baseline = read("baseline-quote.json").regression;
const cpu = read("cpu-quote.json").regression;
const heap = read("heap-quote.json").regression;
const buffer = read("buffer-quote.json").regression;
const latency = read("latency-quote.json").regression;
if (baseline.cpuIterations !== 0 || baseline.delayMs !== 0 || baseline.heapObjects !== 0 || baseline.bufferBytes !== 0) fail("baseline performed regression work");
if (cpu.cpuIterations !== 32000000 || typeof cpu.cpuDigest !== "number" || cpu.delayMs !== 0 || cpu.heapObjects !== 0 || cpu.bufferBytes !== 0) fail("cpu mode did not report synchronous work");
if (latency.delayMs !== 160 || latency.cpuIterations !== 0 || latency.heapObjects !== 0 || latency.bufferBytes !== 0) fail("latency mode did not report the async delay");
if (heap.heapObjects !== 4000 || typeof heap.heapChecksum !== "number" || heap.bufferBytes !== 0 || heap.cpuIterations !== 0) fail("heap mode did not report object work");
if (buffer.bufferBytes !== 524288 || typeof buffer.bufferChecksum !== "number" || buffer.heapObjects !== 0 || buffer.cpuIterations !== 0) fail("buffer mode did not report Buffer work");
const heapRetention = read("heap-telemetry.json").snapshot.retention;
const bufferRetention = read("buffer-telemetry.json").snapshot.retention;
const baselineRetention = read("baseline-telemetry.json").snapshot.retention;
const cap = 2 * 1024 * 1024;
if (!(heapRetention.heap.blocks > 0) || heapRetention.buffer.blocks !== 0 || heapRetention.heap.accountedBytes > cap) fail("heap retention");
if (!(bufferRetention.buffer.accountedBytes > 0) || bufferRetention.heap.blocks !== 0 || bufferRetention.buffer.accountedBytes > cap) fail("buffer retention");
if (baselineRetention.heap.blocks !== 0 || baselineRetention.buffer.blocks !== 0) fail("baseline retained regression blocks");
console.log(`mode_work=pass build=${[...builds][0]} heap_blocks=${heapRetention.heap.blocks} buffer_accounted_bytes=${bufferRetention.buffer.accountedBytes}`);
' "$out"
  fi
  say "comparison_verdict=not_claimed"
  say "comparison_reason=changing NODE_HTTP_MODE changes runtime settings; T14 owns comparability and noise policy"
fi

if [[ "$run_kind" -eq 1 ]]; then
  if ! command -v kind >/dev/null 2>&1 || ! command -v kubectl >/dev/null 2>&1; then
    say "FAIL kind and kubectl are required for --kind"
    fail=1
  elif [[ " ${unverified[*]-} " == *" docker "* ]]; then
    say "FAIL kind was requested but docker was not usable"
    fail=1
  else
    created=0
    previous=""
    forward_pid=""
    cleanup_kind() {
      if [[ -n "${forward_pid}" ]]; then
        kill "$forward_pid" >/dev/null 2>&1 || true
        wait "$forward_pid" >/dev/null 2>&1 || true
        forward_pid=""
      fi
      if [[ -n "${previous}" && "$previous" != "kind-pod-rightsizer-node-http" ]]; then
        kubectl config use-context "$previous" >/dev/null || true
      fi
      if [[ "${created}" -eq 1 ]]; then
        kind delete cluster --name pod-rightsizer-node-http || true
        created=0
      fi
    }
    trap cleanup_kind EXIT
    if kubectl config current-context >/dev/null 2>&1; then
      previous="$(kubectl config current-context)"
    fi
    if ! kind get clusters | grep -qx "pod-rightsizer-node-http"; then
      created=1
      kind create cluster --name pod-rightsizer-node-http
    fi
    kind load docker-image node-http-reference:local --name pod-rightsizer-node-http
    kubectl --context kind-pod-rightsizer-node-http apply -k "$fixture/manifests/overlays/baseline"
    if ! kubectl --context kind-pod-rightsizer-node-http -n pod-rightsizer-node-http rollout status deployment/node-http-reference --timeout=180s; then
      say "FAIL kind deployment did not become ready"
      fail=1
    else
      replicas="$(kubectl --context kind-pod-rightsizer-node-http -n pod-rightsizer-node-http get deployment/node-http-reference -o jsonpath='{.spec.replicas}')"
      pod_name="$(kubectl --context kind-pod-rightsizer-node-http -n pod-rightsizer-node-http get pod -l app.kubernetes.io/name=node-http-reference -o jsonpath='{.items[0].metadata.name}')"
      pod_uid="$(kubectl --context kind-pod-rightsizer-node-http -n pod-rightsizer-node-http exec "$pod_name" -- id -u | tr -d '[:space:]')"
      say "kind_replicas=$replicas"
      say "kind_pod_uid=$pod_uid"
      if [[ "$replicas" != "1" || "$pod_uid" == "0" || -z "$pod_uid" ]]; then
        say "FAIL kind workload replicas or user were not the fixture settings"
        fail=1
      fi
      kubectl --context kind-pod-rightsizer-node-http -n pod-rightsizer-node-http port-forward svc/node-http-reference 18081:80 >"$out/port-forward.log" 2>&1 &
      forward_pid=$!
      deadline=$((SECONDS + 20))
      until curl -fsS http://127.0.0.1:18081/readyz >/dev/null 2>&1; do
        if (( SECONDS > deadline )); then
          say "FAIL kind service did not become ready"
          fail=1
          break
        fi
        sleep 0.3
      done
      if curl -fsS http://127.0.0.1:18081/v1/quote -H 'content-type: application/json' --data-binary @"$fixture/scenario/quote-input.json" > "$out/kind-quote.json"; then
        node --input-type=module -e 'import { readFileSync } from "node:fs"; const q=JSON.parse(readFileSync(process.argv[1],"utf8")); if (q.totalCents!==1142) process.exit(1);' "$out/kind-quote.json"
        say "kind_quote=pass"
      else
        say "FAIL kind quote request failed"
        fail=1
      fi
    fi
    cleanup_kind
    trap - EXIT
  fi
else
  say "UNVERIFIED kind deploy: not requested; not a pass"
fi

if [[ "$fail" -eq 0 ]]; then
  say "RESULT level=experiments functional=pass"
else
  say "RESULT level=experiments functional=fail"
fi
say "T14 comparison was not run. A mode switch is not a code_regression or resource_candidate verdict."
exit "$fail"
