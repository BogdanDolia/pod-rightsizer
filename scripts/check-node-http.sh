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
  measured_p95() {
    node --input-type=module -e '
import { readFileSync } from "node:fs";
const summary = JSON.parse(readFileSync(process.argv[1], "utf8"));
const metrics = summary.metrics ?? {};
const key = Object.keys(metrics).find((name) => name.startsWith("http_req_duration") && name.includes("scenario:measured"));
if (!key) process.exit(2);
const stats = metrics[key].values ?? metrics[key];
const value = stats["p(95)"];
if (typeof value !== "number") process.exit(2);
console.log(value);
' "$1"
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
    baseline_p95="$(measured_p95 "$out/baseline-1-summary.json")"
    cpu_p95="$(measured_p95 "$out/cpu-summary.json")"
    latency_p95="$(measured_p95 "$out/latency-summary.json")"
    say "baseline_p95_ms=$baseline_p95"
    say "cpu_p95_ms=$cpu_p95"
    say "latency_p95_ms=$latency_p95"
    node --input-type=module -e '
const baseline = Number(process.argv[1]);
const cpu = Number(process.argv[2]);
const latency = Number(process.argv[3]);
if (!(cpu > baseline)) {
  console.error(`cpu p95 ${cpu} did not exceed baseline p95 ${baseline}`);
  process.exit(1);
}
if (!(latency > baseline)) {
  console.error(`latency p95 ${latency} did not exceed baseline p95 ${baseline}`);
  process.exit(1);
}
' "$baseline_p95" "$cpu_p95" "$latency_p95"
    node --input-type=module -e '
import { readFileSync } from "node:fs";
function memory(path) {
  const body = JSON.parse(readFileSync(path, "utf8"));
  return body.snapshot.memory;
}
function retention(path) {
  return JSON.parse(readFileSync(path, "utf8")).snapshot.retention;
}
const baseline = memory(process.argv[1]);
const buffer = memory(process.argv[2]);
const heap = retention(process.argv[3]);
const bufferRetention = retention(process.argv[2]);
if (!(buffer.arrayBuffersBytes > baseline.arrayBuffersBytes)) {
  console.error(`buffer arrayBuffers ${buffer.arrayBuffersBytes} did not exceed baseline ${baseline.arrayBuffersBytes}`);
  process.exit(1);
}
if (!(bufferRetention.buffer.accountedBytes > 0)) process.exit(1);
if (!(heap.heap.blocks > 0)) process.exit(1);
console.log(`buffer_arrayBuffers=${buffer.arrayBuffersBytes} baseline_arrayBuffers=${baseline.arrayBuffersBytes} heap_blocks=${heap.heap.blocks} heap_accounted_bytes=${heap.heap.accountedBytes}`);
' "$out/baseline-telemetry.json" "$out/buffer-telemetry.json" "$out/heap-telemetry.json"
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
    if kubectl config current-context >/dev/null 2>&1; then
      previous="$(kubectl config current-context)"
    fi
    if ! kind get clusters | grep -qx "pod-rightsizer-node-http"; then
      kind create cluster --name pod-rightsizer-node-http
      created=1
    fi
    kind load docker-image node-http-reference:local --name pod-rightsizer-node-http
    kubectl --context kind-pod-rightsizer-node-http apply -k "$fixture/manifests/overlays/baseline"
    kubectl --context kind-pod-rightsizer-node-http -n pod-rightsizer-node-http rollout status deployment/node-http-reference --timeout=180s
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
    kill "$forward_pid" || true
    wait "$forward_pid" || true
    if [[ -n "$previous" && "$previous" != "kind-pod-rightsizer-node-http" ]]; then
      kubectl config use-context "$previous" >/dev/null
    fi
    if [[ "$created" -eq 1 ]]; then
      kind delete cluster --name pod-rightsizer-node-http
    fi
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
