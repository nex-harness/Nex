#!/usr/bin/env bash
# G0.9: regenerate the RPC traces (deterministic) and the serialization vectors.
# Records frames from the real @nex/rpc codec over an in-process queue pair, then writes
# go/testdata/rpc-vectors.json from the real serialize()/deserialize().
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

node scripts/go-rewrite/record/record-rpc.mjs
(
  cd packages/shared
  npx --yes tsx ../../scripts/go-rewrite/gen-rpc-vectors.mts >/dev/null
)
echo "rpc traces and vectors regenerated"
