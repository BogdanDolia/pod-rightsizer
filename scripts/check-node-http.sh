#!/usr/bin/env bash
# Fast functional checks for the Node HTTP reference fixture.
# Docker, k6, and kind are separate levels. A skipped tool is not a pass.
# Containers, local processes, port-forwards, and a kind cluster created by
# this run are removed on normal exit, set -e, INT, and TERM.
set -euo pipefail
set +m

root="$(cd "$(dirname "$0")/.." && pwd)"
fixture="$root/test/fixtures/node-http"
experiments=0
run_kind=0
fault_section=""
ready_seconds="${NODE_HTTP_READY_SECONDS:-20}"
kind_name="pod-rightsizer-node-http"
kind_context="kind-${kind_name}"

usage() {
  cat <<'EOF'
usage: bash scripts/check-node-http.sh [--experiments] [--kind]
       bash scripts/check-node-http-faults.sh

--experiments and --kind run disposable Docker and kind checks.
bash scripts/check-node-http-faults.sh is the cleanup fault-injection suite.
It is required CI and is not a functional pass by itself.
--faults experiments|kind skips the fast suite and is only for that suite.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --experiments) experiments=1; shift ;;
    --kind) run_kind=1; experiments=1; shift ;;
    --faults)
      shift
      fault_section="${1:-}"
      if [[ "$fault_section" != "experiments" && "$fault_section" != "kind" ]]; then
        echo "usage: bash scripts/check-node-http.sh --faults experiments|kind" >&2
        exit 2
      fi
      shift
      ;;
    -h|--help) usage; exit 0 ;;
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if [[ -n "$fault_section" && ( "$experiments" -eq 1 || "$run_kind" -eq 1 ) ]]; then
  echo "--faults does not combine with --experiments or --kind" >&2
  exit 2
fi
if [[ ! "$ready_seconds" =~ ^[1-9][0-9]*$ ]]; then
  echo "NODE_HTTP_READY_SECONDS must be a positive integer" >&2
  exit 2
fi

registry="$(mktemp -d "${TMPDIR:-/tmp}/node-http-check.XXXXXX")"
: > "$registry/containers"
: > "$registry/pids"
cleanup_failed=0
fail=0
unverified=()
use_image=0
out=""

register_container() {
  printf '%s\n' "$1" >> "$registry/containers"
}

register_pid() {
  printf '%s\n' "$1" >> "$registry/pids"
}

save_context() {
  if [[ -f "$registry/context_saved" ]]; then
    return 0
  fi
  : > "$registry/context_saved"
  local previous
  if previous="$(kubectl config current-context 2>/dev/null)"; then
    printf '1\n' > "$registry/context_was_set"
    printf '%s\n' "$previous" > "$registry/context_value"
  else
    printf '0\n' > "$registry/context_was_set"
  fi
}

cleanup_resources() {
  local pid name fpid kind_target previous
  if [[ -f "$registry/forward_pid" ]]; then
    fpid="$(cat "$registry/forward_pid")"
    if [[ -n "$fpid" ]]; then
      kill -TERM "$fpid" 2>/dev/null || true
      wait "$fpid" 2>/dev/null || true
    fi
    rm -f "$registry/forward_pid"
  fi
  if [[ -f "$registry/pids" ]]; then
    while IFS= read -r pid; do
      [[ -z "$pid" ]] && continue
      kill -TERM "$pid" 2>/dev/null || true
    done < "$registry/pids"
    while IFS= read -r pid; do
      [[ -z "$pid" ]] && continue
      wait "$pid" 2>/dev/null || true
    done < "$registry/pids"
    : > "$registry/pids"
  fi
  if [[ -f "$registry/containers" ]]; then
    while IFS= read -r name; do
      [[ -z "$name" ]] && continue
      if ! docker stop -t 10 "$name" >/dev/null 2>&1; then
        if docker inspect "$name" >/dev/null 2>&1; then
          cleanup_failed=1
        fi
      fi
      if ! docker rm -f "$name" >/dev/null 2>&1; then
        if docker inspect "$name" >/dev/null 2>&1; then
          cleanup_failed=1
        fi
      fi
    done < "$registry/containers"
    : > "$registry/containers"
  fi
  if [[ -f "$registry/kind_created" && "$(cat "$registry/kind_created")" == "1" ]]; then
    kind_target="$(cat "$registry/kind_name")"
    if ! kind delete cluster --name "$kind_target"; then
      cleanup_failed=1
    fi
    printf '0\n' > "$registry/kind_created"
  fi
  if [[ -f "$registry/context_saved" ]]; then
    if [[ "$(cat "$registry/context_was_set")" == "1" ]]; then
      previous="$(cat "$registry/context_value")"
      if ! kubectl config use-context "$previous" >/dev/null; then
        cleanup_failed=1
      fi
    else
      kubectl config unset current-context >/dev/null 2>&1 || true
      if kubectl config current-context >/dev/null 2>&1; then
        cleanup_failed=1
      fi
    fi
    rm -f "$registry/context_saved"
  fi
}

on_exit() {
  local status=$?
  trap - EXIT INT TERM
  set +e
  cleanup_resources
  if [[ "$status" -eq 0 && "$cleanup_failed" -ne 0 ]]; then
    echo "FAIL cleanup of this run's resources failed" >&2
    status=1
  elif [[ "$cleanup_failed" -ne 0 ]]; then
    echo "WARN cleanup encountered errors; preserving the original exit status $status" >&2
  fi
  rm -rf "$registry"
  exit "$status"
}
trap on_exit EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

say() {
  printf '%s\n' "$*"
}

if ! command -v node >/dev/null 2>&1 || [[ "$(node --version)" != "v24.22.0" ]]; then
  node_version="$(node --version 2>/dev/null || echo missing)"
  say "FAIL Node.js v24.22.0 is required, found $node_version"
  exit 1
fi

export SOURCE_REVISION=unknown
export SOURCE_TREE_STATE=unknown

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

run_container() {
  local name="$1"
  local outfile="$2"
  local cid mapped deadline
  register_container "$name"
  cid="$(docker run -d --name "$name" -p 127.0.0.1::8080 node-http-reference:local)"
  mapped="$(docker port "$cid" 8080/tcp | head -n 1 | sed 's/.*://')"
  deadline=$((SECONDS + ready_seconds))
  until curl -fsS "http://127.0.0.1:${mapped}/readyz" >/dev/null 2>&1; do
    if (( SECONDS > deadline )); then
      docker logs "$cid" || true
      echo "container did not become ready" >&2
      return 1
    fi
    sleep 0.2
  done
  printf '%s %s\n' "$cid" "$mapped" > "$outfile"
}

start_target() {
  local mode="$1"
  local intensity="$2"
  local log="$3"
  local outfile="$4"
  if [[ "$use_image" -eq 1 ]]; then
    local name="node-http-k6-${mode}-$$"
    local cid mapped deadline
    register_container "$name"
    cid="$(docker run -d --name "$name" \
      -e "NODE_HTTP_MODE=${mode}" \
      -e "NODE_HTTP_INTENSITY=${intensity}" \
      -p 127.0.0.1::8080 \
      node-http-reference:local)"
    mapped=""
    deadline=$((SECONDS + ready_seconds))
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
    printf 'container %s %s\n' "$cid" "$mapped" > "$outfile"
    return 0
  fi
  NODE_HTTP_MODE="$mode" NODE_HTTP_INTENSITY="$intensity" PORT=0 \
    node --enable-source-maps dist/server.js >"$log" 2>&1 &
  local pid=$!
  register_pid "$pid"
  local deadline=$((SECONDS + 10))
  local port=""
  while (( SECONDS < deadline )); do
    port="$(sed -n 's/.*listening port=\([0-9][0-9]*\).*/\1/p' "$log" | head -n 1)"
    if [[ -n "$port" ]]; then
      printf 'process %s %s\n' "$pid" "$port" > "$outfile"
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

run_experiments() {
  out="$fixture/.experiments/$(date -u +%Y%m%dT%H%M%SZ)"
  mkdir -p "$out"
  say "experiment_output=$out"

  local k6_version
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
    local image_id name_a name_b cid_a port_a cid_b port_b uid node_in_image cmdline exit_a
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

    name_a="node-http-ref-a-$$"
    run_container "$name_a" "$out/container-a.txt"
    read -r cid_a port_a < "$out/container-a.txt"
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

    name_b="node-http-ref-b-$$"
    run_container "$name_b" "$out/container-b.txt"
    read -r cid_b port_b < "$out/container-b.txt"
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
    local target_file kind id port base status stop_code stub_log stub_pid stub_port wrong_status
    if [[ "$use_image" -eq 1 ]]; then
      say "k6_target=image node-http-reference:local"
    else
      say "k6_target=local_node"
      say "UNVERIFIED k6 against the built image: docker image was not available; local Node is not that proof"
    fi
    target_file="$out/baseline-target.txt"
    start_target baseline 0 "$out/baseline-server.log" "$target_file"
    read -r kind id port < "$target_file"
    base="http://127.0.0.1:${port}"
    curl -fsS "$base/metadata" > "$out/baseline-metadata.json"
    status="$(run_k6 baseline-1 "$base" baseline 0)"
    say "k6_baseline_1_exit=$status"
    if [[ "$status" != "0" ]]; then
      say "FAIL baseline k6 run 1 exited $status"
      fail=1
    fi
    # The ledger is cumulative for the process. Keep the first run's phase
    # boundaries; a second run on the same process is a repeatability check.
    curl -fsS "$base/load-phases" > "$out/baseline-phases.json"
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
    curl -fsS "$base/load-phases" > "$out/baseline-run2-phases.json"
    stop_code="$(stop_target "$kind" "$id")"
    say "baseline_target_exit=$stop_code"

    local mode
    for mode in cpu heap buffer latency; do
      target_file="$out/${mode}-target.txt"
      start_target "$mode" 4 "$out/${mode}-server.log" "$target_file"
      read -r kind id port < "$target_file"
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
      curl -fsS "$base/load-phases" > "$out/${mode}-phases.json"
      stop_code="$(stop_target "$kind" "$id")"
      say "${mode}_target_exit=$stop_code"
    done

    stub_log="$out/wrong-answer.log"
    PORT=0 node dist-test/test/wrong-answer-stub.js >"$stub_log" 2>&1 &
    stub_pid=$!
    register_pid "$stub_pid"
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
        say "baseline_pair image=node-http-reference:local scenario=node-http-quote/2 mode=baseline intensity=0 runs=2"
      else
        say "baseline_pair image=local_node scenario=node-http-quote/2 mode=baseline intensity=0 runs=2"
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
  const phases = read(`${mode}-phases.json`);
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
  if (oldSpace.state === "known" && oldSpace.v8ZeroMeansDefault === true && Object.hasOwn(oldSpace, "bytes")) fail(`${mode} zero old space converted to bytes`);
  if (oldSpace.state === "known" && oldSpace.v8ZeroMeansDefault !== true && (oldSpace.unit !== "mebibytes" || oldSpace.bytes !== oldSpace.value * 1048576)) fail(`${mode} old space unit`);
  const warmupDone = phases.warmup?.lastWorkFinishedOffsetMs;
  const measuredStart = phases.measured?.firstStartedOffsetMs;
  if (!(phases.warmup?.count > 0) || !(phases.measured?.count > 0)) fail(`${mode} phase counts`);
  if (phases.warmupInFlightAtFirstMeasuredStart !== 0) fail(`${mode} measured started while warmup work was in flight`);
  if (typeof warmupDone !== "number" || typeof measuredStart !== "number" || !(warmupDone <= measuredStart)) fail(`${mode} warmup work overlapped measured requests`);
  console.log(`${mode}_phases warmup_done_offset_ms=${warmupDone} measured_start_offset_ms=${measuredStart} gap_ms=${measuredStart - warmupDone}`);
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
}

run_kind_deploy() {
  if [[ -z "$out" ]]; then
    out="$fixture/.experiments/$(date -u +%Y%m%dT%H%M%SZ)"
    mkdir -p "$out"
  fi
  if ! command -v kind >/dev/null 2>&1 || ! command -v kubectl >/dev/null 2>&1; then
    say "FAIL kind and kubectl are required for --kind"
    fail=1
  elif [[ " ${unverified[*]-} " == *" docker "* ]]; then
    say "FAIL kind was requested but docker was not usable"
    fail=1
  else
    save_context
    printf '%s\n' "$kind_name" > "$registry/kind_name"
    local clusters
    if ! clusters="$(kind get clusters)"; then
      say "FAIL kind get clusters failed"
      fail=1
    elif printf '%s\n' "$clusters" | grep -qx "$kind_name"; then
      printf '0\n' > "$registry/kind_created"
    else
      printf '1\n' > "$registry/kind_created"
      kind create cluster --name "$kind_name"
    fi
    if [[ "$fail" -ne 0 && ! -f "$registry/kind_created" ]]; then
      return 0
    fi
    kind load docker-image node-http-reference:local --name "$kind_name"
    kubectl --context "$kind_context" apply -k "$fixture/manifests/overlays/baseline"
    if ! kubectl --context "$kind_context" -n pod-rightsizer-node-http rollout status deployment/node-http-reference --timeout=180s; then
      say "FAIL kind deployment did not become ready"
      fail=1
    else
      local replicas pod_name pod_uid forward_pid deadline
      replicas="$(kubectl --context "$kind_context" -n pod-rightsizer-node-http get deployment/node-http-reference -o jsonpath='{.spec.replicas}')"
      pod_name="$(kubectl --context "$kind_context" -n pod-rightsizer-node-http get pod -l app.kubernetes.io/name=node-http-reference -o jsonpath='{.items[0].metadata.name}')"
      pod_uid="$(kubectl --context "$kind_context" -n pod-rightsizer-node-http exec "$pod_name" -- id -u | tr -d '[:space:]')"
      say "kind_replicas=$replicas"
      say "kind_pod_uid=$pod_uid"
      if [[ "$replicas" != "1" || "$pod_uid" == "0" || -z "$pod_uid" ]]; then
        say "FAIL kind workload replicas or user were not the fixture settings"
        fail=1
      fi
      kubectl --context "$kind_context" -n pod-rightsizer-node-http port-forward svc/node-http-reference 18081:80 >"$out/port-forward.log" 2>&1 &
      forward_pid=$!
      printf '%s\n' "$forward_pid" > "$registry/forward_pid"
      deadline=$((SECONDS + ready_seconds))
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
  fi
}

if [[ -n "$fault_section" ]]; then
  say "level=fault section=$fault_section"
  cd "$fixture"
  if [[ "$fault_section" == "experiments" ]]; then
    run_experiments
  else
    run_kind_deploy
  fi
  if [[ "$fail" -eq 0 ]]; then
    say "RESULT level=fault functional=pass"
  else
    say "RESULT level=fault functional=fail"
  fi
  exit "$fail"
fi

if ! command -v npm >/dev/null 2>&1 || [[ "$(npm --version)" != "11.19.1" ]]; then
  npm_version="$(npm --version 2>/dev/null || echo missing)"
  say "FAIL npm 11.19.1 is required, found $npm_version"
  exit 1
fi

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
  say "UNVERIFIED cleanup fault injection: not run by this command; run bash scripts/check-node-http-faults.sh"
  if [[ "$fail" -eq 0 ]]; then
    say "RESULT level=fast functional=pass"
  else
    say "RESULT level=fast functional=fail"
  fi
  say "This command did not accept the T02 container, k6, kind, or cleanup-fault requirements."
  exit "$fail"
fi

run_experiments

if [[ "$run_kind" -eq 1 ]]; then
  run_kind_deploy
else
  say "UNVERIFIED kind deploy: not requested; not a pass"
fi

if [[ "$fail" -eq 0 ]]; then
  say "RESULT level=experiments functional=pass"
else
  say "RESULT level=experiments functional=fail"
fi
say "T14 comparison was not run. A mode switch is not a code_regression or resource_candidate verdict."
say "UNVERIFIED cleanup fault injection: not run by this command; run bash scripts/check-node-http-faults.sh"
exit "$fail"
