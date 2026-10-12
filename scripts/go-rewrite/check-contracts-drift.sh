#!/usr/bin/env bash
# G0.5 drift check: regenerate the contracts and fail if the committed files changed.
#
# This is what CI runs; a stale generated file (or a hand-edit of a .gen.go) fails here.
# It works on untracked trees too: it hashes the generated artifacts before and after
# regeneration and compares, instead of relying on `git diff` (which ignores untracked
# files until they are added).
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

ARTIFACTS=(
  go/contracts/schema
  go/contracts/surface.gen.go
  go/contracts/methods.gen.go
  go/contracts/manifest.gen.go
  go/testdata/schema-corpus
)

hash_artifacts() {
  find "${ARTIFACTS[@]}" -type f -print0 2>/dev/null | sort -z | xargs -0 sha256sum | sha256sum
}

before="$(hash_artifacts)"
scripts/go-rewrite/gen-contracts.sh >/dev/null
after="$(hash_artifacts)"

if [ "$before" != "$after" ]; then
  echo "DRIFT: contracts changed after regeneration" >&2
  echo "before=$before" >&2
  echo "after=$after" >&2
  echo "run scripts/go-rewrite/gen-contracts.sh and commit the result" >&2
  exit 1
fi

# Also fail if git sees modifications to committed artifacts (when the tree is tracked).
if git rev-parse --git-dir >/dev/null 2>&1; then
  for path in "${ARTIFACTS[@]}"; do
    if ! git diff --quiet -- "$path" 2>/dev/null; then
      echo "DRIFT: tracked file modified: $path" >&2
      exit 1
    fi
  done
fi

echo "contracts are up to date"
