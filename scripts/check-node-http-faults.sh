#!/usr/bin/env bash
# Fault injection for scripts/check-node-http.sh resource cleanup.
# Uses stub docker, curl, k6, kind, and kubectl. It does not accept the fixture.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
fixture="$root/test/fixtures/node-http"
check="$root/scripts/check-node-http.sh"
k6_pin="$(tr -d '[:space:]' < "$fixture/scenario/k6-version")"
foreign_container="foreign-container-do-not-touch"
foreign_cluster="staging"
foreign_e2e="pod-rightsizer-e2e"
suite_fail=0

say() {
  printf '%s\n' "$*" >&2
}

fail_case() {
  say "FAIL $*"
  suite_fail=1
}

cleanup_pids() {
  local file="$1"
  local pid
  if [[ ! -f "$file" ]]; then
    return 0
  fi
  while IFS= read -r pid; do
    [[ -z "$pid" ]] && continue
    kill -TERM "$pid" 2>/dev/null || true
  done < "$file"
}

install_stubs() {
  local bin="$1"
  local log="$2"
  local pids="$3"
  cat > "$bin/docker" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$FAULT_LOG"
if [[ "$1" == "info" ]]; then
  if [[ "${FAULT_DOCKER_INFO:-ok}" == "fail" ]]; then exit 1; fi
  exit 0
fi
if [[ "$1" == "build" ]]; then exit 0; fi
if [[ "$1" == "image" && "$2" == "inspect" ]]; then
  echo "sha256:dead"
  exit 0
fi
if [[ "$1" == "run" ]]; then
  echo "cid-fault"
  exit 0
fi
if [[ "$1" == "port" ]]; then
  echo "127.0.0.1:9"
  exit 0
fi
if [[ "$1" == "logs" || "$1" == "exec" ]]; then
  exit 0
fi
if [[ "$1" == "inspect" ]]; then
  if [[ "${FAULT_DOCKER_INSPECT:-missing}" == "present" ]]; then exit 0; fi
  exit 1
fi
if [[ "$1" == "stop" ]]; then
  if [[ "${FAULT_DOCKER_STOP:-ok}" == "fail" ]]; then exit 1; fi
  exit 0
fi
if [[ "$1" == "rm" ]]; then
  exit 0
fi
exit 0
EOF
  cat > "$bin/curl" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$FAULT_LOG"
printf '%s\n' "$$" >> "$FAULT_PIDS"
mode="${FAULT_CURL_MODE:-fail}"
if [[ "$mode" == "hang" ]]; then
  sleep 30
  exit 1
fi
if [[ "$mode" == "bad-quote" ]]; then
  joined="$*"
  if [[ "$joined" == *readyz* ]]; then exit 0; fi
  if [[ "$joined" == *quote* ]]; then
    printf '%s\n' '{"totalCents":1}'
    exit 0
  fi
  printf '%s\n' '{}'
  exit 0
fi
exit 1
EOF
  cat > "$bin/k6" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >> "\$FAULT_LOG"
if [[ "\$1" == "version" ]]; then
  printf '%s\n' "k6 v${k6_pin}"
  exit 0
fi
exit "\${FAULT_K6_EXIT:-9}"
EOF
  cat > "$bin/kind" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$FAULT_LOG"
if [[ "$1" == "get" && "$2" == "clusters" ]]; then
  if [[ -n "${FAULT_FOREIGN_CLUSTER:-}" ]]; then
    printf '%s\n' "$FAULT_FOREIGN_CLUSTER"
  fi
  if [[ "${FAULT_KIND_EXISTS:-0}" == "1" ]]; then
    printf '%s\n' "pod-rightsizer-node-http"
  fi
  exit 0
fi
exit 0
EOF
  cat > "$bin/kubectl" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$FAULT_LOG"
if [[ "$1" == "config" && "$2" == "current-context" ]]; then
  if [[ -n "${FAULT_CONTEXT:-}" ]]; then
    printf '%s\n' "$FAULT_CONTEXT"
    exit 0
  fi
  exit 1
fi
if [[ "$1" == "config" && "$2" == "use-context" ]]; then exit 0; fi
if [[ "$1" == "config" && "$2" == "unset" ]]; then exit 0; fi
joined="$*"
if [[ "$joined" == *port-forward* ]]; then
  printf '%s\n' "$$" > "$FAULT_FORWARD_PID"
  printf '%s\n' "$$" >> "$FAULT_PIDS"
  exec sleep 60
fi
if [[ "$joined" == *jsonpath*replicas* ]]; then
  printf '%s' "1"
  exit 0
fi
if [[ "$joined" == *jsonpath*name* ]]; then
  printf '%s' "pod-a"
  exit 0
fi
if [[ "$joined" == *" exec "* ]]; then
  printf '%s\n' "1000"
  exit 0
fi
if [[ "$joined" == *rollout*status* ]]; then exit 0; fi
exit 0
EOF
  chmod +x "$bin/docker" "$bin/curl" "$bin/k6" "$bin/kind" "$bin/kubectl"
  : > "$log"
  : > "$pids"
}

assert_no_foreign() {
  local log="$1"
  if grep -q "$foreign_container" "$log"; then
    fail_case "cleanup touched foreign container in $log"
  fi
  if grep -E -q "delete cluster --name ${foreign_cluster}($| )" "$log"; then
    fail_case "cleanup deleted foreign cluster ${foreign_cluster}"
  fi
  if grep -E -q "delete cluster --name ${foreign_e2e}($| )" "$log"; then
    fail_case "cleanup deleted ${foreign_e2e}"
  fi
}

assert_stopped() {
  local log="$1"
  local name
  name="$(sed -n 's/.*--name \([^ ]*\).*/\1/p' "$log" | head -n 1)"
  if [[ -z "$name" ]]; then
    fail_case "docker run did not record a container name"
    return
  fi
  if ! grep -q "stop -t 10 ${name}" "$log"; then
    fail_case "missing docker stop for ${name}"
  fi
  if ! grep -q "rm -f ${name}" "$log" && ! grep -q "rm ${name}" "$log"; then
    fail_case "missing docker rm for ${name}"
  fi
}

# pgrep exits 1 when nothing matches. Exit 2 or higher means the list was not read.
capture_pids() {
  local pattern="$1"
  local output status
  set +e
  output="$(pgrep -f "$pattern")"
  status=$?
  set -e
  if [[ "$status" -gt 1 ]]; then
    printf 'pgrep-error\n'
    return 0
  fi
  printf '%s\n' "$output"
}

assert_dead() {
  local pid="$1"
  local label="$2"
  local attempt
  for attempt in 1 2 3 4 5 6 7 8 9 10; do
    if ! kill -0 "$pid" 2>/dev/null; then
      return 0
    fi
    sleep 0.1
  done
  fail_case "${label} pid ${pid} was still running"
}

run_check() {
  local log="$1"
  shift
  local stdout="$1"
  shift
  set +e
  env "$@" bash "$check" >"$stdout" 2>&1
  local exit_code=$?
  set -e
  printf '%s\n' "$exit_code"
}

case_dirs=()
last_exit=0
on_suite_exit() {
  local status=$?
  local dir
  trap - EXIT
  for dir in "${case_dirs[@]-}"; do
    [[ -z "$dir" ]] && continue
    if [[ -f "$dir/pids" ]]; then
      cleanup_pids "$dir/pids"
    fi
    rm -rf "$dir"
  done
  exit "$status"
}
trap on_suite_exit EXIT

run_case() {
  local name="$1"
  local section="$2"
  shift 2
  local case_dir bin log stdout exit_code
  case_dir="$(mktemp -d "${TMPDIR:-/tmp}/node-http-fault-${name}.XXXXXX")"
  case_dirs+=("$case_dir")
  bin="$case_dir/bin"
  mkdir -p "$bin"
  log="$case_dir/commands.log"
  stdout="$case_dir/stdout.txt"
  install_stubs "$bin" "$log" "$case_dir/pids"
  say "case=${name}"
  set +e
  env \
    FAULT_LOG="$log" \
    FAULT_PIDS="$case_dir/pids" \
    FAULT_FORWARD_PID="$case_dir/forward.pid" \
    PATH="$bin:$PATH" \
    NODE_HTTP_READY_SECONDS="${NODE_HTTP_READY_SECONDS:-1}" \
    "$@" \
    bash "$check" --faults "$section" >"$stdout" 2>&1
  exit_code=$?
  set -e
  printf '%s\n' "$exit_code" > "$case_dir/exit"
  last_exit="$exit_code"
}

expect_exit() {
  local actual="$1"
  local expected="$2"
  local name="$3"
  if [[ "$actual" != "$expected" ]]; then
    fail_case "${name} exit ${actual}, expected ${expected}"
  fi
}

say "level=cleanup-faults"

run_case readiness experiments \
  FAULT_CURL_MODE=fail \
  FAULT_DOCKER_INFO=ok
expect_exit "$last_exit" 1 readiness
readiness_dir="${case_dirs[0]}"
assert_stopped "$readiness_dir/commands.log"
assert_no_foreign "$readiness_dir/commands.log"
if grep -q "functional=pass" "$readiness_dir/stdout.txt"; then
  fail_case "readiness timeout was reported as a functional pass"
fi

run_case post-create experiments \
  FAULT_CURL_MODE=bad-quote \
  FAULT_DOCKER_INFO=ok
expect_exit "$last_exit" 1 post-create
post_dir="${case_dirs[1]}"
assert_stopped "$post_dir/commands.log"
assert_no_foreign "$post_dir/commands.log"

run_case cleanup-error experiments \
  FAULT_CURL_MODE=fail \
  FAULT_DOCKER_INFO=ok \
  FAULT_DOCKER_STOP=fail \
  FAULT_DOCKER_INSPECT=present
expect_exit "$last_exit" 1 cleanup-error
stop_dir="${case_dirs[2]}"
if ! grep -q "stop -t 10 " "$stop_dir/commands.log"; then
  fail_case "cleanup-error did not attempt docker stop"
fi
if grep -q "functional=pass" "$stop_dir/stdout.txt"; then
  fail_case "cleanup failure was reported as a functional pass"
fi
assert_no_foreign "$stop_dir/commands.log"

if [[ ! -f "$fixture/dist/server.js" || ! -f "$fixture/dist-test/test/wrong-answer-stub.js" ]]; then
  fail_case "local process and k6 fault cases need the fast check build under test/fixtures/node-http"
else
  before_servers="$(capture_pids 'dist/server.js')"
  if [[ "$before_servers" == "pgrep-error" ]]; then
    fail_case "pgrep could not list fixture servers before the local-process case"
  fi
  run_case local-process experiments \
    FAULT_CURL_MODE=fail \
    FAULT_DOCKER_INFO=fail
  expect_exit "$last_exit" 1 local-process
  after_servers="$(capture_pids 'dist/server.js')"
  if [[ "$before_servers" == "pgrep-error" || "$after_servers" == "pgrep-error" || "$after_servers" != "$before_servers" ]]; then
    fail_case "local fixture process remained after the failed check"
  fi
  assert_no_foreign "${case_dirs[3]}/commands.log"
fi

# k6 failure uses the real curl and a failing k6 stub, after the server is listening.
if [[ -f "$fixture/dist/server.js" && -f "$fixture/dist-test/test/wrong-answer-stub.js" ]]; then
  k6_dir="$(mktemp -d "${TMPDIR:-/tmp}/node-http-fault-k6.XXXXXX")"
  case_dirs+=("$k6_dir")
  mkdir -p "$k6_dir/bin"
  install_stubs "$k6_dir/bin" "$k6_dir/commands.log" "$k6_dir/pids"
  rm -f "$k6_dir/bin/curl"
  before_servers="$(capture_pids 'dist/server.js')"
  if [[ "$before_servers" == "pgrep-error" ]]; then
    fail_case "pgrep could not list fixture servers before the k6-failure case"
  fi
  say "case=k6-failure"
  set +e
  env \
    FAULT_LOG="$k6_dir/commands.log" \
    FAULT_PIDS="$k6_dir/pids" \
    FAULT_DOCKER_INFO=fail \
    FAULT_K6_EXIT=9 \
    PATH="$k6_dir/bin:$PATH" \
    NODE_HTTP_READY_SECONDS=1 \
    bash "$check" --faults experiments >"$k6_dir/stdout.txt" 2>&1
  k6_exit=$?
  set -e
  if [[ "$k6_exit" -eq 0 ]]; then
    fail_case "k6 failure exited 0"
  fi
  after_servers="$(capture_pids 'dist/server.js')"
  stub_servers="$(capture_pids 'wrong-answer-stub.js')"
  if [[ "$before_servers" == "pgrep-error" || "$after_servers" == "pgrep-error" || "$after_servers" != "$before_servers" ]]; then
    fail_case "k6 failure left a fixture server running"
  fi
  if [[ "$stub_servers" == "pgrep-error" || -n "$stub_servers" ]]; then
    fail_case "k6 failure left the wrong-answer stub running"
  fi
  say "k6_failure_exit=${k6_exit}"
fi

interrupt_dir="$(mktemp -d "${TMPDIR:-/tmp}/node-http-fault-interrupt.XXXXXX")"
case_dirs+=("$interrupt_dir")
mkdir -p "$interrupt_dir/bin"
install_stubs "$interrupt_dir/bin" "$interrupt_dir/commands.log" "$interrupt_dir/pids"
say "case=interrupt"
set -m
env \
  FAULT_LOG="$interrupt_dir/commands.log" \
  FAULT_PIDS="$interrupt_dir/pids" \
  FAULT_CURL_MODE=hang \
  FAULT_DOCKER_INFO=ok \
  PATH="$interrupt_dir/bin:$PATH" \
  NODE_HTTP_READY_SECONDS=30 \
  bash "$check" --faults experiments >"$interrupt_dir/stdout.txt" 2>&1 &
interrupt_pid=$!
seen_run=0
for _ in $(seq 1 50); do
  if grep -q '^run ' "$interrupt_dir/commands.log"; then
    seen_run=1
    break
  fi
  sleep 0.1
done
if [[ "$seen_run" -ne 1 ]]; then
  kill -TERM "$interrupt_pid" 2>/dev/null || true
  fail_case "interrupt case did not reach docker run"
else
  # Signal the script's process group so a foreground stub curl does not
  # delay the TERM trap until its own sleep ends.
  kill -TERM -"$interrupt_pid"
fi
set +e
wait "$interrupt_pid"
interrupt_exit=$?
set -e
set +m
cleanup_pids "$interrupt_dir/pids"
expect_exit "$interrupt_exit" 143 interrupt
assert_stopped "$interrupt_dir/commands.log"
assert_no_foreign "$interrupt_dir/commands.log"

run_case kind-created kind \
  FAULT_CURL_MODE=fail \
  FAULT_KIND_EXISTS=0 \
  FAULT_FOREIGN_CLUSTER="$foreign_cluster" \
  FAULT_CONTEXT=
expect_exit "$last_exit" 1 kind-created
kind_created_dir="${case_dirs[${#case_dirs[@]}-1]}"
if ! grep -q "delete cluster --name pod-rightsizer-node-http" "$kind_created_dir/commands.log"; then
  fail_case "kind-created did not delete the cluster this run created"
fi
assert_no_foreign "$kind_created_dir/commands.log"
if ! grep -q "config unset current-context" "$kind_created_dir/commands.log"; then
  fail_case "kind-created did not restore an unset kubectl context"
fi
if grep -q "config use-context" "$kind_created_dir/commands.log"; then
  fail_case "kind-created guessed a previous kubectl context"
fi
if [[ -f "$kind_created_dir/forward.pid" ]]; then
  assert_dead "$(cat "$kind_created_dir/forward.pid")" "port-forward"
else
  fail_case "kind-created did not start a port-forward"
fi

run_case kind-existing kind \
  FAULT_CURL_MODE=fail \
  FAULT_KIND_EXISTS=1 \
  FAULT_FOREIGN_CLUSTER="$foreign_cluster" \
  FAULT_CONTEXT=fault-previous-context
expect_exit "$last_exit" 1 kind-existing
kind_existing_dir="${case_dirs[${#case_dirs[@]}-1]}"
if grep -q "delete cluster --name" "$kind_existing_dir/commands.log"; then
  fail_case "kind-existing deleted a cluster this run did not create"
fi
if ! grep -q "config use-context fault-previous-context" "$kind_existing_dir/commands.log"; then
  fail_case "kind-existing did not restore the saved kubectl context"
fi
assert_no_foreign "$kind_existing_dir/commands.log"
if [[ -f "$kind_existing_dir/forward.pid" ]]; then
  assert_dead "$(cat "$kind_existing_dir/forward.pid")" "existing-cluster port-forward"
else
  fail_case "kind-existing did not start a port-forward"
fi

if [[ "$suite_fail" -ne 0 ]]; then
  local_dir=""
  for local_dir in "${case_dirs[@]-}"; do
    say "---- ${local_dir}"
    say "exit=$(cat "$local_dir/exit" 2>/dev/null || true)"
    say "-- stdout"
    cat "$local_dir/stdout.txt" 2>/dev/null >&2 || true
    say "-- commands"
    cat "$local_dir/commands.log" 2>/dev/null >&2 || true
  done
  unset local_dir
  say "RESULT level=cleanup-faults functional=fail"
  exit 1
fi
say "RESULT level=cleanup-faults functional=pass"
say "These cases expect the inner check to fail. A pass here means each expected failure was observed and cleaned up."
