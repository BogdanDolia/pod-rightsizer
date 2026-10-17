#!/usr/bin/env bash
# Validate contracts/v1 schemas, local $ref targets, and testdata/contracts/v1 fixtures.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
go run -C tools/contractcheck .
