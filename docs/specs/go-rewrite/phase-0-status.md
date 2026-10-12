# Phase 0 — status (Agent F slice)

Written 2026-10-09. This is a status report, not a plan. It states what passes, how it was
checked, and what does not pass. Phase 0 is **not finished**: `gate.sh 0` still fails for
reasons outside this slice (see "Still failing"), and step 0.13 does not reach its threshold.

Scope of this slice: Phase 0 steps **0.4, 0.5, 0.6, 0.9, 0.11**, plus tooling added for **0.13**.
Everything was verified by running the commands below; failures are reported as failures.

## What now passes

| Step           | What exists                                                                                                                                                                                                                                                                                                                                                                                                                                                       | How it was checked                                                                                                                                                                                              | Result                                 |
| -------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------- |
| 0.4            | `scripts/go-rewrite/export-schemas.mts` exports **138 schemas** (58 roots) to `go/contracts/schema/*.json` with a `manifest.json` (0 unsupported conversions, 8 refinements listed) and `methods.json` (75 legacy methods, 7 legacy notifications, 31 v4 methods, 4 v4 notifications, `PROTOCOL_V4_LIMITS`).                                                                                                                                                      | Run from `packages/shared`; output is stable-key JSON, deterministic.                                                                                                                                           | passing                                |
| 0.5            | `scripts/go-rewrite/gen-contracts.mts` emits `go/contracts/{surface,methods,manifest}.gen.go`; `scripts/go-rewrite/gen-contracts.sh` regenerates everything and runs `gofmt`; `scripts/go-rewrite/check-contracts-drift.sh` re-generates and fails on any change.                                                                                                                                                                                                 | `bash scripts/go-rewrite/check-contracts-drift.sh` → "contracts are up to date"; tampering with `methods.gen.go` makes it exit 1. `gofmt -l go/` is empty; `go vet ./contracts/` clean.                         | passing                                |
| 0.6            | `scripts/go-rewrite/make-schema-corpus.mts` writes **11 885 instances** (1 929 valid / 9 956 invalid) to `go/testdata/schema-corpus/`; `go/contracts/agreement_test.go` requires the Go validator to agree with zod's `safeParse` on every one.                                                                                                                                                                                                                   | `go test ./contracts/` passes all 11 885; 8 of 8 hand-written refinements (`refinements.go`) reproduced.                                                                                                        | passing                                |
| 0.9            | `go/internal/rpc` ports `serialize`/`deserialize` (VQL, tags, nested-Uint8Array marker), the 13-byte frame header, and the ChannelClient/ChannelServer request/response headers. `scripts/go-rewrite/gen-rpc-vectors.mts` writes **35 vectors** from the real codec; `scripts/go-rewrite/record/record-rpc.mjs` records scenarios **R01–R09, R11** as faithful frames; `record-rpc.test.mjs` re-decodes every frame and requires byte-identical re-serialization. | `go test ./internal/rpc/` passes the 35 vectors (serialize byte-exact, deserialize value-exact, incl. negatives, `NaN`/`Infinity`→`null`, multibyte, nested marker); `node --test record-rpc.test.mjs` → 14/14. | passing (R10 not recorded — see below) |
| 0.11           | `scripts/go-rewrite/make-inventory.mts` writes `docs/specs/go-rewrite/inventory.md` (**268 rows**: 82 legacy methods/notifications, 34 v4 commands, 31 provider + 7 local tools, 42 service channels, 22 migrations, 40 HTTP routes) with owner phase and status; `make-inventory.test.mjs` requires byte-identical regeneration and complete owner phases.                                                                                                       | `node --test make-inventory.test.mjs` → 5/5.                                                                                                                                                                    | passing                                |
| 0.13 (tooling) | `scripts/go-rewrite/coverage/map-source-coverage.mjs` maps V8 dumps of tsx-run vector scripts back to `.ts` sources; `check-oracle-coverage.mjs` now merges scenario (bundle) and vector (source) coverage; `run-vector-coverage.sh` runs the vector generators under one `NODE_V8_COVERAGE` dir.                                                                                                                                                                 | `wire-codec.ts` goes from 53% (scenarios) to **98%** via vectors; the coverage unit tests pass.                                                                                                                 | **tooling works, threshold not met**   |

## How the generated Go validators were checked for strength

A test that has never failed proves little, so the agreement test (G0.6) was run against
deliberately broken validators and had to fail:

| Mutation                                       | Caught?                                                                                                                                                      |
| ---------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| integer check made a no-op                     | caught (31 disagreements)                                                                                                                                    |
| strict objects treated as non-strict           | caught (41)                                                                                                                                                  |
| optional properties made required              | caught (41)                                                                                                                                                  |
| string `min_length` ignored                    | caught (41)                                                                                                                                                  |
| `safeint` range ignored                        | caught (31)                                                                                                                                                  |
| `catch()` treated as strict (instead of `any`) | caught (26)                                                                                                                                                  |
| `helloMessage` superRefine disabled            | caught (3)                                                                                                                                                   |
| discriminated-union fast path disabled         | **not caught** — with distinct literal tags a discriminated union is exactly an exclusive `anyOf`, so disabling the fast path is a semantic no-op, not a gap |

The RPC codec (G0.9) was mutation-checked too: signed instead of logical VQL shift (hangs → caught
by timeout, which is why TypeScript uses `>>>`), signed length loop, 64-bit `isInt32`, little-endian
header length, 3-key marker detection, and object key sorting instead of insertion order — all caught.

Two real bugs were **found and fixed** by these checks while building the slice:

1. **`z.optional()` is not `null`** — the first Go validator accepted `null` for optional
   properties. zod's `optional` only means the property may be absent; a present `null` still
   fails the inner type. Caught by the corpus (`rowActions`), fixed in `validate.go`.
2. **`$$` in a `String.replace` replacement** — the emitter appended the refine id with
   `body.replace(/}$/, ...)`, and `String.replace` treats `$$` as an escape, so refine ids like
   `commandPayloadSchemas$$sendText` were silently corrupted to one `$`. Caught by the corpus
   (sendText), fixed by string-slicing instead of `replace`.

## Deliberate scope decisions (honest gaps)

- **0.4/G0.4 — the surface is scoped, not exhaustive.** The exporter covers the schemas the Go
  agent must validate (envelope, v4 handshake/subscribe, command envelope + the 34 payloads,
  conversation snapshot/delta/rows, storage startup state) and everything reachable from them
  (138 schemas). It does **not** export every `*Schema` in `packages/shared` (~487). Widening it
  only requires adding roots in `schema-surface.mts`.
- **0.9/G0.9 — no live server.** The committed `packages/server/dist` is stale (it imports
  `packages/provider-node/src/*.js` that no longer exists) and `entry-http.ts` needs a build-time
  embedded provider config, so a live `/ws` capture was not possible without a full build. The
  traces are recorded over the real `ChannelServer`/`ChannelClient` through `createQueuePair()`,
  which is the same codec the WebSocket uses once framing is stripped. **R10 (auth) is not
  recorded**: it is an HTTP/WebSocket-level concern (401, login, token) that the in-process path
  cannot exercise. R01–R09 and R11 are recorded deterministically (re-running produces identical
  files).
- **0.13/G0.13 — the 80% threshold is not reached.** The tooling now lets vectors count, but only
  `wire-codec.ts` passes on the vector path alone. `apply.ts` and `coalesce.ts` are imported by no
  vector script and are "not measured"; the `dist/*.js` files can only be covered by scenarios,
  which are not run in this measurement. **`must-match.json` thresholds were not relaxed.**

## Still failing (and why)

`scripts/go-rewrite/gate.sh 0` was run; these checks fail for reasons **outside this slice**
(other agents' files, changed concurrently):

- **G0.0.gofmt** — `internal/adapters/store/sessions.go` (another agent's file) is unformatted.
  Every file this slice owns is gofmt-clean (`gofmt -l contracts/ internal/rpc/` is empty), and
  `gen-contracts.mts` now runs `gofmt -w` on its own output so the generated files stay clean.
- **G0.10** — a home path in `go/testdata/vectors/context.json` (another agent's file). This
  slice's artifacts are scrub-clean (`scrub-check.mjs` reports 0 findings over
  `go/contracts/schema`, `go/testdata/schema-corpus`, `go/testdata/traces/rpc`).

`G0.0.lint` (warnings/errors in pre-existing vector scripts written by earlier sessions) is also
red; this slice's files pass `oxlint` with 0 warnings. `G0.3` (`go build + test`) passed on the
latest run once the concurrent `modelclient/decoder.go` line-limit issue was fixed upstream.

## Commands run (real output)

```
$ gofmt -l go/                     # (empty)
$ (cd go && go vet ./contracts/ ./internal/rpc/)   # clean
$ (cd go && go test -count=1 ./contracts/ ./internal/rpc/)
ok  github.com/tylinux/nex/go/contracts   0.174s
ok  github.com/tylinux/nex/go/internal/rpc 0.002s
$ bash scripts/go-rewrite/check-contracts-drift.sh
contracts are up to date
$ node --test scripts/go-rewrite/record/record-rpc.test.mjs \
    scripts/go-rewrite/make-inventory.test.mjs \
    scripts/go-rewrite/coverage/check-oracle-coverage.test.mjs   # 24 pass, 0 fail
$ npx --yes pnpm@10.33.2 exec oxfmt --check scripts/go-rewrite docs/specs/go-rewrite
All matched files use the correct format.
```

## gate.sh lines the lead should add

Replace the three `todo` lines for G0.4/G0.5/G0.6, the `todo` for G0.9 and G0.11 in `gate0()`
with these `check` calls (they use the existing `check`/`pnpm_run`/`have` helpers; `tsx` runs
workspace-relative imports from `packages/shared`):

```bash
  check G0.4 "schema export: 138 schemas, manifest, method tables" \
    bash -c 'cd packages/shared && npx --yes tsx ../../scripts/go-rewrite/export-schemas.mts >/dev/null && test -s ../../go/contracts/schema/manifest.json'
  check G0.5 "contracts: regenerated == committed (drift)" \
    bash scripts/go-rewrite/check-contracts-drift.sh
  check G0.6 "Go validators agree with zod on the corpus" \
    bash -c 'cd go && go test -count=1 ./contracts/'
  check G0.9 "RPC codec vectors + recorded traces replay byte-identically" \
    bash -c 'cd go && go test -count=1 ./internal/rpc/ && cd .. && node scripts/go-rewrite/record/record-rpc.mjs >/dev/null && node --test scripts/go-rewrite/record/record-rpc.test.mjs'
  check G0.11 "inventory regenerates byte-identically" \
    bash -c 'cd packages/shared && npx --yes tsx ../../scripts/go-rewrite/make-inventory.mts >/dev/null && cd ../.. && node --test scripts/go-rewrite/make-inventory.test.mjs'
```

For 0.13, keep the check as it is but supply both coverage directories, e.g.:

```bash
  check G0.13 "oracle coverage: scenario + vector sources >= 80% on must-match" \
    bash -c 'bash scripts/go-rewrite/coverage/run-vector-coverage.sh >/dev/null && node scripts/go-rewrite/coverage/check-oracle-coverage.mjs "${SCENARIO_COV_DIR:-} " --vectors "${VECTOR_COV_DIR:-/tmp/nex-vector-cov}"'
```

This 0.13 line will still FAIL until `apply.ts`/`coalesce.ts` get vector generators and the
dist files get scenario coverage; do not add it as green.

## Rollback

All outputs are additive and regenerable: delete `go/contracts/{schema,surface.gen.go,methods.gen.go,manifest.gen.go}`,
`go/testdata/{schema-corpus,rpc-vectors.json,traces/rpc}`, `go/internal/rpc`, and the new
`scripts/go-rewrite/{export-schemas,gen-contracts,make-schema-corpus,make-inventory,schema-surface,zod-walk}.mts`

- `record/{record-rpc,record-rpc.test,record-rpc.sh}` + `coverage/{map-source-coverage.mjs,run-vector-coverage.sh}`.
  No TypeScript product source was changed.
