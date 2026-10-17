#!/usr/bin/env bash
set -euo pipefail

# Run on the Linux amd64 CI runner, from the repository root after GoReleaser.
archives=(dist/*_linux_amd64.tar.gz)
if [[ ${#archives[@]} -ne 1 || ! -f "${archives[0]}" ]]; then
  echo "Expected exactly one Linux amd64 release archive" >&2
  exit 1
fi

release_dir=$(mktemp -d)
release_pid=
cleanup() {
  if [[ -n "$release_pid" ]]; then
    kill "$release_pid" 2>/dev/null || true
    wait "$release_pid" 2>/dev/null || true
  fi
  rm -rf "$release_dir"
}
trap cleanup EXIT

tar -xzf "${archives[0]}" -C "$release_dir"
cd "$release_dir"
./pod-rightsizer --help >/dev/null 2>&1
test -s web/ui/index.html

PORT=18080 ./advisor-api >advisor.log 2>&1 &
release_pid=$!
for attempt in {1..30}; do
  if curl --fail --silent --max-time 2 http://127.0.0.1:18080/ -o served-index.html; then
    cmp web/ui/index.html served-index.html
    echo "Packaged CLI, Advisor API, and UI passed smoke checks"
    exit 0
  fi
  if ! kill -0 "$release_pid" 2>/dev/null; then
    break
  fi
  sleep 0.2
done

cat advisor.log >&2
echo "Packaged Advisor UI did not become ready" >&2
exit 1
