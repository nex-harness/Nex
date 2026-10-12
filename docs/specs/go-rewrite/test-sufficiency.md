# Are there enough tests to promise the Go rewrite is correct?

**No.** The existing tests cannot support that promise, and they are not the right oracle for it. This was measured, not estimated. See [baseline.md](baseline.md) for the run results.

## What exists (measured 2026-10-09)

Test cases are counted by matching `test(` / `it(` at the start of a line in `*.test.ts[x]` files; non-test source lines exclude `.d.ts`, `dist/` and tests.

| Package (in rewrite scope)                                                                                                             | Test files | Cases   | Non-test source lines |
| -------------------------------------------------------------------------------------------------------------------------------------- | ---------- | ------- | --------------------- |
| `packages/services`                                                                                                                    | 14         | 57      | 87 667                |
| `packages/server`                                                                                                                      | 1          | 19      | 11 776                |
| `apps/nex-cli/packages/core`                                                                                                           | 7          | 35      | 98 740                |
| `apps/nex-cli/packages/bootstrap`                                                                                                      | 4          | 9       | 64 286                |
| `apps/nex-cli/packages/adapters`                                                                                                       | 1          | 3       | 50 955                |
| `packages/shared`, `rpc`, `provider`, `provider-node`, `model-option-map`, `client`, `nex-server-cli`, `contracts`, `dynamic-workflow` | **0**      | **0**   | **~110 000**          |
| **Total in scope**                                                                                                                     | **27**     | **123** | **~425 000**          |

That is roughly one test case per 3 400 lines of backend code. For comparison, the UI packages (out of scope) have 81 + 21 + 27 cases.

## What the existing tests actually cover

All 123 cases pass today. The topics are narrow:

- `packages/server`: the web token login and session behavior (19 cases, good coverage of `webAuth.ts`).
- `core`: codemode sandbox, tool-search, MCP exposure, the nested runner, prompt-context entries.
- `services`: agent command resolution, provider config migration, pets, terminal PTY fallback, legacy data-dir copy, model display names, models.dev catalog.
- `bootstrap`/`adapters`: codemode e2e, provider selection, prompt context, MCP exposure.

I grepped every test file for the topics a Go rewrite must reproduce exactly. **Zero test files mention any of these:**

| Behavior the rewrite must match                                        | Test files mentioning it                                                                            |
| ---------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------- |
| RPC `serialize` / `deserialize` (the binary wire format the UI speaks) | 0                                                                                                   |
| `ChannelServer` / `ChannelClient`                                      | 0                                                                                                   |
| v4 wire fragments, CRC32                                               | 0                                                                                                   |
| Turn state machine                                                     | 0                                                                                                   |
| Edit matchers / file edit application                                  | 0                                                                                                   |
| Bash read-only permission policy (about 35 files)                      | 0                                                                                                   |
| Model failure classifier / retry                                       | 0                                                                                                   |
| Streaming tool-call assembler                                          | 0                                                                                                   |
| Command inbox (serial admission of busy inputs)                        | 0                                                                                                   |
| SQLite migrations                                                      | 3 files mention it, indirectly (provider config / legacy copy), not the 22 session-store migrations |

The v4 conversation protocol, the task/session owner-lease logic, the model layer, the tool handlers and the bash security policy have **no automated tests at all**. They are exactly the parts where a rewrite is most likely to diverge silently.

One more data point: `AGENTS.md` expects behavior changes to come with tests and interaction changes with E2E scenarios, and the only E2E material in the backend is one manual scenario (`bootstrap/test/e2e/README.md` plus a 57-line `mock-model.mjs`). It is a manual procedure, not an automated suite.

## Measured: how much code the existing tests execute

Method: each package's tests were run with `NODE_V8_COVERAGE`, and V8 function-level coverage was merged per source file (a function counts as covered if any test executed it). Only function counts are reported, because the tests run through `tsx`, which transpiles TypeScript, so V8's byte offsets do not map back to the original files and any line or byte percentage would be wrong.

"Loaded" below means a test imported the module (its top level ran). It does not mean the module's logic was exercised.

| Package source                     | Files loaded by any test | Functions executed, in loaded files only |
| ---------------------------------- | ------------------------ | ---------------------------------------- |
| `core/src`                         | 347 / 520                | 885 / 3 168 (28%)                        |
| `bootstrap/src`                    | 3 / 228                  | 14 / 36                                  |
| `adapters/src`                     | 1 / 199                  | 12 / 17                                  |
| `services/src`                     | 64 / 281                 | 441 / 1 113 (40%)                        |
| `server/src`                       | 1 / 51                   | 41 / 43                                  |
| `rpc/src` (no tests of its own)    | 15 / 15                  | see below                                |
| `shared/src` (no tests of its own) | 222 / 231                | not computed                             |

The loaded-file counts are inflated by imports (`core` and `shared` are pulled in by other packages' tests). The function counts cover loaded files only, so they overstate coverage: the 173 `core` files and 217 `services` files that no test loads would count as 0%.

The files the rewrite must reproduce most exactly, from the same run:

| File                                                             | Functions executed by existing tests |
| ---------------------------------------------------------------- | ------------------------------------ |
| `rpc/src/serialization.ts` (the UI wire format)                  | 4 / 19                               |
| `rpc/src/channelServer.ts`                                       | 2 / 15                               |
| `rpc/src/channelClient.ts`                                       | 2 / 13                               |
| `shared/.../nex-protocol-v4/wire-codec.ts`                       | 2 / 12                               |
| `shared/.../nex-protocol-v4/wire-assembler.ts`                   | 2 / 22                               |
| `shared/.../nex-protocol-v4/apply.ts`                            | 1 / 7                                |
| `shared/.../nex-protocol-v4/coalesce.ts`                         | 1 / 6                                |
| `core/src/agent/turn-machine.ts`                                 | 2 / 22                               |
| `core/src/tool/edit-matchers.ts`                                 | 1 / 32                               |
| `core/src/tool/handlers/edit.ts`                                 | 1 / 20                               |
| `core/src/tool/handlers/bash.ts`                                 | 2 / 16                               |
| `core/src/tool/handlers/bash-command-parser.ts`                  | 1 / 16                               |
| `core/src/tool/handlers/bash-command-permission-policy.ts`       | 1 / 20                               |
| `core/src/tool/handlers/bash-readonly-policy-argv.ts`            | 1 / 7                                |
| `services/src/nex-agent/nexTaskServiceAdapter.ts` (5.7k lines)   | 41 / 249                             |
| `adapters/src/model/failure-classifier.ts`                       | not loaded by any test               |
| `adapters/src/model/streaming-tool-call-assembler.ts`            | not loaded by any test               |
| `adapters/src/storage/session-store/migration-runner.ts`         | not loaded by any test               |
| `bootstrap/src/nex-protocol-v4/command-inbox.ts`                 | not loaded by any test               |
| `bootstrap/src/nex-protocol/server.ts`                           | not loaded by any test               |
| `services/src/nex-agent/nexAgentService.ts` (4.9k lines)         | not loaded by any test               |
| `services/src/nex-agent/nexAgentConnectionScope.ts` (1.1k lines) | not loaded by any test               |

The executed functions in the loaded files are mostly module-level helpers that run at import time or through unrelated tests, not behavior under test. This is consistent with the grep result above: the model layer, the v4 session protocol, the migration runner, and the agent and task services are never executed by any existing test. (I did not inspect which specific functions ran, so "mostly import-time helpers" is an inference from the low counts, not an observation.)

## What this means

1. **Existing tests are not a safety net for the rewrite.** Even if the Go code passed every one of them (translated), it would still be unverified for >95% of the behavior.
2. **They are still useful, as a small part of the plan.** The 19 `webAuth` cases translate directly into Go tests for Phase 2 step 2.6. The codemode/tool-search/nested-runner cases become sidecar acceptance tests in Phase 4. The `services` pty/path/config cases become Phase 3 starting points.
3. **The plan already assumed this.** Decision D5 says the oracle is recorded TypeScript behavior, not unit tests. This measurement confirms D5 and makes it a hard requirement rather than a preference.

## What "enough" has to mean for this project

A promise that the rewrite is correct cannot be made from test counts. It can be made from **differential evidence**: the Go and Node implementations produce the same outputs on the same inputs, over a corpus large enough and varied enough to cover the code. That requires three things, none of which exist yet:

| Need                                                                                                  | Where it is planned                            | Status    |
| ----------------------------------------------------------------------------------------------------- | ---------------------------------------------- | --------- |
| Recorded end-to-end traces (agent stdio, RPC frames)                                                  | Phase 0 steps 0.7, 0.8                         | Not built |
| Value-level vectors for pure logic (wire codec, policy, classifier, option-map, cron, apply/coalesce) | Phase 0 step 0.8, Phases 1–4 "vectors" steps   | Not built |
| **Measured coverage of the oracle itself**                                                            | **Phase 0 step 0.13 (added in this revision)** | Not built |

The third row is the new addition. Without it, "18 traces pass" says nothing about how much of the 98k-line `core` those traces exercise. Step 0.13 measures V8 line coverage of the Node code while it runs the traces, and requires ≥ 80% on a named "must match" list of files. Files below that get new scenarios or vector scripts before the phase that ports them starts.

## What can be promised

- **Can promise:** for every behavior covered by a trace or vector, the Go implementation matches Node on that input, in CI, on three operating systems. The coverage report states exactly which code that is.
- **Cannot promise:** behavior outside the corpus. This includes rare provider quirks, unusual shell syntax, and timing-dependent races. Mitigations in the plan are fuzzing, the chaos suites in Phases 3–4, the canary period in Phase 5 (2 weeks, no P1) and the Node fallback for one release.
- **Should not promise:** a date. The baseline is cheap to measure, but writing the recorder (step 0.7) and finding out how much of the code the traces reach are the real unknowns.

## Recommended order inside Phase 0

1. Finish 0.1 (done: `baseline.md`).
2. Build the recorder and the first three scenarios (S01 handshake, S02 chat, S03 tool call) **and immediately run them under V8 coverage** (a thin slice of 0.13). The coverage number from this slice tells us how many scenarios the 80% target needs. That number should replace the "18 scenarios" figure in the plan.
3. Only then commit to the schedule for Phases 1–4.

## Verify-first

- The case counts come from a line-start regex and undercount tests written in other styles (`describe` blocks nesting `it(` with leading text on the same line are counted, but table-driven tests that generate cases in loops count as one). Treat them as lower bounds.
- Test files under `packages/ui` were counted for context only; they were not run.
- The coverage section is function-level only (see its method note). The "0 test files mention X" table is a grep result, but the function table confirms it independently for the same files. Step 0.13 will replace both with proper line coverage of the **trace-based** oracle, using source maps.
- Coverage was measured on the host Node 24.20.0, not the pinned 24.14.0, and only for packages that have tests. `shared` and `rpc` have no tests of their own; their numbers come from other packages' tests importing them.
- `bootstrap` and `adapters` function counts cover only the 3 and 1 files their tests load; the other 225 and 198 files contribute nothing to those counts.
- The measuring scripts live in `/tmp/nex-p0/` and are not committed. Step 0.13 replaces them with a committed, tested tool.
