#!/usr/bin/env bash
# G0.4 + G0.5: export the protocol surface and generate the Go contracts.
# Runs the JSON Schema exporter (G0.4), then the Go data emitter (G0.5), then gofmt.
# Deterministic: re-running on a clean tree produces byte-identical output
# (checked by scripts/go-rewrite/check-contracts-drift.sh).
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

TSX="${TSX:-npx --yes tsx}"
# The schemas import workspace-relative modules, so run from packages/shared.
(
  cd packages/shared
  $TSX ../../scripts/go-rewrite/export-schemas.mts
  $TSX ../../scripts/go-rewrite/gen-contracts.mts
  # G0.6 corpus is an input to the agreement test, not to the generator, but regenerating
  # it here keeps the committed corpus in step with the surface.
  $TSX ../../scripts/go-rewrite/make-schema-corpus.mts
)

(cd go && gofmt -w ./contracts/)
echo "contracts generated"
