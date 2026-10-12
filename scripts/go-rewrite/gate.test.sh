#!/usr/bin/env bash
# G0.12: the gate runner must fail when what it guards is broken. Each case damages one thing in a
# throwaway copy of the guarded files, runs only the relevant gate check, and expects a FAIL line.
set -u
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
fail=0
expect_fail() { # expect_fail <description> <check-id> <setup-cmd> <restore-cmd>
  local desc="$1" id="$2" setup="$3" restore="$4"
  eval "$setup"
  out="$(GATE_SELFTEST=1 GATE_LOG_DIR=/tmp/go-rewrite-gate-selftest scripts/go-rewrite/gate.sh 0 2>&1)"
  eval "$restore"
  if echo "$out" | grep -q "^FAIL $id "; then echo "ok   $desc -> $id reported FAIL"
  else echo "BAD  $desc -> $id did NOT fail"; echo "$out" | grep "$id" | sed 's/^/       /'; fail=1; fi
}
T=go/testdata/traces/agent/S02/out.normalized.json
cp "$T" /tmp/gate-selftest-trace.bak
expect_fail "a recorded trace no longer matches the Node agent" G0.8 \
  "sed -i 's/Hello from the mock model/Hello from the mock modeI/' $T" "cp /tmp/gate-selftest-trace.bak $T"
expect_fail "a fixture contains a credential" G0.10 \
  "echo '{\"k\":\"sk-live-abcdefghijklmnopqrstuv\"}' > go/testdata/zz_secret.json" "rm -f go/testdata/zz_secret.json"
expect_fail "a Go file is badly formatted" G0.0.gofmt \
  "printf 'package protocol\nvar   x=1\n' > go/internal/protocol/zz_fmt.go" "rm -f go/internal/protocol/zz_fmt.go"
expect_fail "a Go test fails" G0.0.test \
  "printf 'package protocol\nimport \"testing\"\nfunc TestZZ(t *testing.T){ t.Fatal(\"boom\") }\n' > go/internal/protocol/zz_fail_test.go" "rm -f go/internal/protocol/zz_fail_test.go"
expect_fail "decisions.md has a TBD cell" G0.2 \
  "cp docs/specs/go-rewrite/decisions.md /tmp/gate-selftest-dec.bak; echo '| D99 | x | TBD | y |' >> docs/specs/go-rewrite/decisions.md" "cp /tmp/gate-selftest-dec.bak docs/specs/go-rewrite/decisions.md"
exit $fail
