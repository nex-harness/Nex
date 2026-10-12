# Phase 0 step 0.1 — TypeScript baseline

Measured 2026-10-09 on the dev host (linux x64, Node v24.20.0, pnpm 10.33.2 via `npx`). Repository is **not** a usable git checkout from this shell
(`dubious ownership`); commands that call git were run with `GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=safe.directory GIT_CONFIG_VALUE_0=/mnt/MX500/Developer/Nex`.

Note: the host Node is 24.20.0, not the 24.14.0 pinned in `mise.toml`. Results below may differ slightly on the pinned version.

## Commands

| Command                                                               | Exit | Time | Result                                                                                                                                                        |
| --------------------------------------------------------------------- | ---- | ---- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `pnpm install --frozen-lockfile`                                      | 0    | 64 s | 1 064 entries in `node_modules`                                                                                                                               |
| `node scripts/check-workspace-freshness.mjs`                          | 1    | 1 s  | **Pre-existing/environmental:** runs `git fetch origin --prune`; this checkout has no reachable `origin` ("Could not read from remote repository")            |
| `pnpm typecheck`                                                      | 0    | 61 s | 0 errors                                                                                                                                                      |
| `pnpm lint` (oxlint)                                                  | 0    | 1 s  | 0 errors, **80 warnings**, 2 415 files                                                                                                                        |
| `pnpm fmt:check` (oxfmt)                                              | 1    | 4 s  | **Pre-existing:** 138 files fail outside this plan (packages/ui 52, services 42, shared 14, desktop 10, server 6, nex-server-cli 3, provider-node 2, other 9) |
| `pnpm architecture:check`                                             | 0    | 1 s  | `violations: 0, baseline: 0, new: 0`                                                                                                                          |
| `pnpm -r --filter @nex/{core,bootstrap,adapters}... build` (CLI libs) | 0    | 25 s | needed before the CLI package tests can import `@nex/contracts` `dist/`                                                                                       |

The 7 files in `docs/specs/go-rewrite/` were also failing `fmt:check` when first written; they were formatted with `oxfmt` and now pass. They are not counted above.

## Package tests (existing)

Run with `pnpm test` in each package (all use `tsx --test test/*.test.ts`), after the CLI library build above. `packages/services` has no `test` script; it was run with `npx tsx --test test/*.test.ts`.

| Package                           | Files | Tests | Pass | Fail | Skip |
| --------------------------------- | ----- | ----- | ---- | ---- | ---- |
| `packages/services`               | 14    | 58    | 57   | 0    | 1    |
| `packages/server`                 | 1     | 19    | 19   | 0    | 0    |
| `apps/nex-cli/packages/core`      | 7     | 35    | 35   | 0    | 0    |
| `apps/nex-cli/packages/bootstrap` | 4     | 9     | 9    | 0    | 0    |
| `apps/nex-cli/packages/adapters`  | 1     | 3     | 3    | 0    | 0    |
| `packages/web`                    | 3     | 21    | 21   | 0    | 0    |

Not run: `packages/ui` (13 test files) and `packages/desktop` (3), both out of scope for the rewrite.

Operational findings that affect later phases:

1. Running `pnpm test` in `core`/`bootstrap` **before** building the CLI libraries fails with `ERR_MODULE_NOT_FOUND` (`@nex/contracts/dist/index.js`). The recorder and replay harness must run the build first (already implied by G0.8).
2. `packages/services/package.json` has no `test` script, so `pnpm -r test` would silently skip its 58 tests.
3. There is no root `test` script (as noted in the verify-first list).

## Pre-existing failure list used by gate `G0.0`

- `check-workspace-freshness.mjs` exits 1 without a reachable git remote.
- `fmt:check` fails on 138 non-plan files.
- `lint` passes with 80 warnings (not failures).

Any other failure in a later gate is new and must be fixed or explained.
