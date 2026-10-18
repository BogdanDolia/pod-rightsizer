#!/usr/bin/env bash
# Install an unchanged upstream k6 release outside the Pod Rightsizer package.
set -euo pipefail

if [[ "$#" -ne 1 || -z "$1" ]]; then
  echo "usage: bash scripts/install-fixture-k6.sh <bin-directory>" >&2
  exit 2
fi

root="$(cd "$(dirname "$0")/.." && pwd)"
scenario="$root/test/fixtures/node-http/scenario"
version="$(tr -d '[:space:]' < "$scenario/k6-version")"
case "$(uname -s)" in
  Linux) platform=linux; extension=tar.gz ;;
  Darwin) platform=macos; extension=zip ;;
  *) echo "unsupported k6 fixture platform" >&2; exit 2 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) architecture=amd64 ;;
  arm64|aarch64) architecture=arm64 ;;
  *) echo "unsupported k6 fixture architecture" >&2; exit 2 ;;
esac

name="k6-v${version}-${platform}-${architecture}"
archive="${name}.${extension}"
checksum="$(awk -v archive="$archive" '$2 == archive { print $1 }' "$scenario/k6-checksums.txt")"
if [[ ! "$checksum" =~ ^[a-f0-9]{64}$ ]]; then
  echo "missing pinned SHA-256 for $archive" >&2
  exit 1
fi

temporary="$(mktemp -d)"
trap 'rm -rf "$temporary"' EXIT
curl --fail --location --silent --show-error --retry 3 \
  "https://github.com/grafana/k6/releases/download/v${version}/${archive}" \
  --output "$temporary/$archive"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$temporary/$archive")"
else
  actual="$(shasum -a 256 "$temporary/$archive")"
fi
if [[ "${actual%% *}" != "$checksum" ]]; then
  echo "SHA-256 mismatch for $archive" >&2
  exit 1
fi

if [[ "$extension" == zip ]]; then
  unzip -q "$temporary/$archive" -d "$temporary"
else
  tar -xzf "$temporary/$archive" -C "$temporary"
fi
mkdir -p "$1"
install -m 755 "$temporary/$name/k6" "$1/k6"
"$1/k6" version
