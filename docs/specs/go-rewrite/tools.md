# Tools: Read, Write, Edit and permission (Go)

Status: built and verified. Owner: Agent E. Scope: the Read/Write/Edit tool logic,
their input validation, the permission classification and the
`interaction/requestPermission` payload. Bash, Glob, Grep and the remaining tools
are out of scope (later).

Packages:

- `go/internal/tools/**` — pure logic, no `os`/`net` imports: path policy, the
  read-before-write guard and read-state, the edit matcher cascade, unified-diff
  hunks, the JSON-schema subset validator, the model-visible result text, the
  tool metadata/budget registry and the 32 KiB head/tail limit.
- `go/internal/adapters/fsx/**` — the only filesystem code: stat, bounded text
  reads (UTF-8/UTF-16LE/GB2312/GBK/GB18030), atomic writes that preserve mode, and
  a `Port` that adapts `*FS` to `tools.FS`.
- `go/internal/permission/**` — rule matching, the mode-based decision
  (`checkPermission`) and the permission-option projection plus the
  `requestPermission` request payload.
- `scripts/go-rewrite/gen-tool-vectors.mts` — the differential vector generator.
- `go/testdata/vectors/tools-*.json` — generated vectors.

## Method: differential vectors from the real TypeScript

Every vector is produced by running the **real TypeScript handler** on a fixture
tree in a temp workspace (`gen-tool-vectors.mts` imports
`handlers/{read,write,edit}.ts`, `edit-matchers.ts`, `diff.ts`,
`tool/executor/validation.ts`, `input-normalization.ts`, `permission/service.ts`,
`bootstrap/src/permission-options.ts`). For each case it records:

- the fixture directory (paths, contents, modes; binary fixtures as byte arrays),
- the model input,
- the model-visible result text (`formatModelContent`) or the failure message,
- the structured output JSON,
- the resulting file tree with contents, sizes and permission bits,
- for the read-then-write cases, the action taken after a preceding `Read`.

The Go tests replay the same fixture tree and require byte-identical results. The
generator is deterministic (regenerating `tools-read.json` / `tools-validation.json`
produces identical bytes), and gate G1.8 re-runs it to prove the committed vectors are
reproducible rather than hand-written.

Two failures were **found by these vectors**, not by reading the code:

1. `Read` keeps the UTF-16LE BOM in its decoded content (`"﻿hi\n"`), and a
   write-back must re-encode that BOM — the whole-file read must **not** strip a
   BOM. Getting this wrong fails `read/utf16le-bom` and `write/utf16le-roundtrip`.
2. `Read`'s own runtime schema rejects an unknown `pages` field, but its JSON
   schema (`ReadInputJsonSchema`, built from the PDF-less base) rejects `pages` as
   an unexpected parameter — so `Read` with `pages` always fails JSON validation.
   `val/read-pages-ok` pins that quirk.

Vector counts: 50 Read, 41 Write, 49 Edit inputs; 35 validation cases; 29
permission cases; plus matcher, diff, encoding, metadata and scenario sections.

Of those, the **asserting** cases — where a real handler outcome (result text, failure
text or output JSON) is compared, not just the fixture tree — are 46 Read, 39 Write,
47 Edit. The remainder are raw ZodError inputs that the real executor rejects at the
JSON-schema stage before the handler runs; the replay skips them because
`tools-validation.json` covers their model-visible text. `replayVectors` counts only
the asserting cases and fails below **30 per tool**, so the step-1.8 minimum is
enforced instead of assumed.

`scripts/go-rewrite/check-tool-vector-drift.mjs` (gate G1.8) regenerates every
`tools-*.json` from the handlers and requires byte-identical output, so a hand-edited
vector file cannot pass. Making that check possible required the generator to be fully
deterministic: the encoding error text embedded `mkdtempSync`'s random suffix, so the
temporary workspace path is now normalized to `<WS>` there too.

## Read

`Read` (`read.go`, `readformat.go`) mirrors `handlers/read.ts` + `read-text.ts`:

- line-numbered output `"<n>\t<line>"` joined by LF; the JS split `/\r?\n/`
  keeps a trailing empty line, so `"hi there\n"` renders `"1\thi there\n2\t"`.
- `offset`/`limit` are resolved through `resolveWorkspacePath` then a range read
  with `maxBytes = READ_MAX_FILE_SIZE_BYTES` (256 KiB) only for a whole-file read.
- an empty file normalizes `numLines`/`totalLines` from 0 to 1, which turns the
  empty-file reminder into the "shorter than the provided offset" reminder — both
  texts are `<system-reminder>` wrapped.
- missing file → `File does not exist…` with a `did you mean` suggestion (same
  stem, else Levenshtein ≤ 3 over the sorted directory listing).
- a directory → `Cannot read directory as text file: <path>`.
- **binary/image/PDF/video are out of scope.** Instead of stubbing the media
  results, the port reproduces the two _refusals_ that happen before any media
  handling: the binary-extension preflight and the blocking-device preflight, both
  as `<tool_use_error>` text. Text encodings are handled (below). A real image or
  PDF body is neither read nor fabricated; the port never returns a media block.
- read-state is recorded under `CreateReadStateKey(path, offset, limit)`; a repeat
  read of an unchanged file returns `file_unchanged` and the "Wasted call…" stub.

### Encodings (`adapters/fsx/text.go`)

Detection order matches `text-metadata.ts`: UTF-8 BOM → UTF-16LE BOM → binary
refusal (a NUL byte anywhere, or > 30 % control bytes) → valid UTF-8 → legacy
Chinese candidates `gb2312`, `gbk`, `gb18030` (round-trip with up to 3 trailing
bytes dropped). `read/gbk-legacy` (`中文测试`) is detected as `gb2312`,
`read/gb18030-only` (`㐀`, a 4-byte GB18030 sequence) as `gb18030`, and
`tools-encodings.json` pins each detection name and content. Go has no plain
GB2312 codec, so the `gb2312`/`gbk` slots use `golang.org/x/text`'s GBK codec
(GB18030 for the third); the sampled fixtures decode identically to iconv-lite.
`x/text` was already in `go.mod`; no new dependency was added.

### Long lines and the 32 KiB rule

`toolOutputFinalHeadBytes`/`toolOutputFinalTailBytes` are read from
`PROTOCOL_V4_LIMITS` (32 KiB each) and reproduced in `limits.go`. The exact
truncation _marker_ is owned by the v4 projection layer (out of scope); the byte
budget is ported and tested. The model-visible text these tools produce is **not**
token-capped in the Go port: the `READ_MAX_OUTPUT_TOKENS` partial-view path in
`read-text.ts` needs the token estimator, which is not ported, so very large reads
return the full content where Node would return a partial view. No vector covers
that path; it is a recorded gap (see below), not a silent approximation.

## Write

`Write` (`write.go`) mirrors `handlers/write.ts`:

- create vs overwrite is decided by a preceding read; the structured patch compares
  the decoded, LF-normalized original to the raw input content (so a CRLF file
  re-serialized from LF shows the LF→CRLF difference in the diff, exactly like
  Node — `write/lf-preserved`).
- the read-before-write guard: an existing file that was never read, or whose read
  is a partial view, fails with `File has not been read yet. Read it first before
writing to it.`; a changed file fails with `File has been modified since read…`.
  A pure mtime bump with identical content is **not** stale (both stacks compare
  content for a strict full read) — `write/overwrite-stale` vs
  `write/overwrite-stale-content` pin this.
- atomic write: temp file in the **same directory** → write → chmod to the
  existing mode → fsync → rename. A symlink target is refused. `write/preserve-mode-0755`
  and `TestWritePreservesMode` prove the executable bit survives; `TestAtomicWriteLeavesNoTempFiles`
  proves no `.tmp.*` is left behind.
- parent directories are created; encoding and line endings are preserved from the
  existing file (default UTF-8/LF for a new file).

## Edit

`Edit` (`edit.go`, `editmatchers.go`, `editmatchers_normalize.go`) mirrors
`handlers/edit.ts` + `edit-matchers.ts`:

- the matcher cascade is ported in order: `exact`, `quote_normalized`,
  `line_number_prefix_stripped`, `escape_normalized`,
  `unicode_escape_normalized`, `line_trimmed`, `indentation_flexible`,
  `block_anchor`. With `replace_all` the three broad matchers
  (`line_trimmed`, `indentation_flexible`, `block_anchor`) are skipped.
- normalized-strategy candidate slicing is done on runes because JS indexes by
  UTF-16 code unit and quote normalization changes the byte length of the
  surrounding text (`edit/quote-normalized`).
- ambiguity: a strategy yielding more than one distinct candidate value is
  `ambiguous`; with `replace_all` false, more than one occurrence of the matched
  text is also `ambiguous`, with the "Found N matches…" message.
- the error codes (`EditErrorCode`) and all messages match: no-op, not-read,
  stale, missing file, notebook, old-string-not-found, ambiguous, too-large,
  invalid path. `replace_all` uses the `semanticBoolean` coercion (`"true"`,
  `"1"`, `"yes"`, `"y"`, `"on"` → true; trimmed, case-insensitive).
- the diff/hunk metadata is the jsdiff `structuredPatch(filePath, filePath, old,
new, {context:3})` ported from `diff@9` (Myers + hunk builder), pinned by 12
  diff vectors and by the `hunkCount`/`matchAttempts` values the S04 recording
  carries (`hunkCount:1`, `matchAttempts:1`).

## Filesystem error text

`toError` in `internal/adapters/fsx/fsx.go` mirrors `toFileSystemError` in
`adapters/src/fs/index.ts`. Codes Node maps explicitly (`ENOENT` → `not_found`,
`EACCES`/`EPERM` → `permission_denied`, `EISDIR` → `is_directory`,
`ENAMETOOLONG` → `invalid_path`) keep their own message. Every other errno falls
through to libuv's raw text, which the model sees verbatim:
`ENOTDIR: not a directory, stat '<path>'`. Go's `os.PathError` formats the same
condition differently (`stat <path>: not a directory`), so `nodeErrnoMessage`
rebuilds the Node string from the errno and the syscall name. Found by the
`write/parent-is-file` vector and pinned by `TestUnmappedErrnoMessageMatchesNode`.

## Tool execution contract

- A JSON-schema subset validator (`jsonschema.go`, `jsonschemautil.go`) mirrors
  `core/src/tool/json-schema.ts`; the invalid-input text the model sees is
  produced by `inputvalidation.go`/`inputprojection.go` and validated against 35
  `tools-validation.json` cases, including the issue field order and the
  `exact:false` key the recorded zod issues always carry.
- `inputnormalization.go` mirrors `prepareInitialToolExecutionInput`: a
  top-level JSON string is parsed, the tool's runtime schema strips unknown keys
  and applies `replace_all: false`, and runtime issues are recorded for the
  projection.
- readOnly/destructive/sideEffectScope/riskLevel/needsApproval and the result
  budget (`truncate`, preview `head`, byte budget) are asserted equal to the
  TypeScript metadata in `TestToolMetadataMatchesTypeScript` /
  `TestResultBudgetMatchesTypeScript`; the same numbers live in
  `registry.go`.
- cancellation is threaded through `context.Context` on every `FS` method; the
  `Port` checks `ctx.Err()` first and returns the `cancelled` code, and
  `TestReadCancellation` proves a cancelled context fails rather than reading.

## Permission

`permission/rules.go` ports `rule-matching.ts` and the matching half of
`service.ts`: wildcard→regexp (anchored), the `prefix:*` prefix form, subject
extraction (`command`/`url`/`file_path`/`path`/`pattern`/`patch_text`), and the
`Write`↔`Edit` rule-name equivalence. `service.go` ports `checkPermission` for the
build/edit/plan/yolo/auto modes, project allow/ask/deny rules and the
disallowed-tools list. 29 decisions in `tools-permission.json` pin the result,
`ruleId` and `reason` for each mode; the `anchor` cases prove the regex is
anchored at both ends.

`permission/options.go` ports `bootstrap/src/permission-options.ts`: the classic
`allow_once` / `allow_project` (+ `permissionUpdates` with `ruleContent` = path) /
`deny` options, the `no-always-allow` and `session-always-allow` policies, and the
denial text. `permission/request.go` builds the `interaction/requestPermission`
params (`input`, `reason` = "Tool has side effects and requires approval",
`requestId` = `perm_<uuid>`, `riskLevel` = medium, `sessionId`, `options`,
`toolCallId`, `toolName`, `turnId`). Two tests tie this to the golden traces:

- `TestRequestPayloadMatchesS04Frame` builds the Write and Edit payloads and
  compares them (minus the generated `requestId`) with the recorded S04
  `interaction/requestPermission` frames — field for field.
- `TestDenialContentMatchesS05Request` asserts the deny option reason equals the
  exact text the S05 recording delivered to the model.

The decision application (allow ⇒ the tool runs; deny ⇒ the model receives the
denial text) is shown end-to-end by
`TestScenarioToolResultsMatchRecording`: S03's Read result (`"1\thi there\n2\t"`),
S04's Write→Edit result texts, and S05's denial text are compared with what the
real Node agent sent in its next model request (captured with
`NEX_REC_BODY_DIR`).

## Verification

Commands (from `go/`):

```
gofmt -l internal/tools internal/permission internal/adapters/fsx   # no output
go vet ./...                                                        # clean
go test -race -count=1 ./...                                        # all packages ok
GOOS=windows GOARCH=amd64 go build ./... && GOOS=darwin GOARCH=arm64 go build ./...  # ok
```

184 test cases pass across `internal/tools` and `internal/permission`. The
architecture checker accepts every file (≤ 400 lines, no `os`/`net` outside
adapters).

### Mutation checks

Each group was run against deliberately broken code; a "caught" line means a test
failed. Equivalent mutants (semantically identical changes) are marked.

| Group                | Mutation                                                                                                                               | Result                                                                                                        |
| -------------------- | -------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------- | ------ |
| Read                 | line-number off-by-one; empty-file normalization removed; reminder wrapper removed; "did you mean" filter disabled; `\r` strip removed | caught                                                                                                        |
| Read encoding        | NUL detection narrowed to `0xFF`; CRLF→LF normalization removed; CRLF detection forced to LF                                           | caught (after adding `read/nul-in-ascii` and CRLF vectors)                                                    |
| Write                | not-read message; storage suffix; mode preservation disabled; CRLF re-apply removed                                                    | caught                                                                                                        |
| Edit                 | ambiguity guard removed; no-op message; `replace_all` coalescing narrowed to `"true"` only                                             | caught (after adding the `"1"`/`"yes"`/`"off"` vectors)                                                       |
| Matchers             | ambiguity `!=1`→`>1`; empty-search guard; candidate dedupe                                                                             | caught; the `!=1`→`>1` case is an **equivalent mutant** (only reached with ≥1 candidate)                      |
| Diff                 | trailing newline strip; hunk context offset; `@`-range start shift                                                                     | caught; the `context` clamp mutation is **equivalent** (only reached when the clamp yields exactly `context`) |
| Validation           | `undefined`→`missing`; semantic-boolean set narrowed; empty-string guard in `isPositiveInt`                                            | caught; the `isPositiveInt("")` guard is defensive/unreachable via vectors (equivalent)                       |
| Permission decisions | reason text; `RuleID`; regex anchoring (`$` removed)                                                                                   | caught (after adding the mid-wildcard anchor vectors)                                                         |
| Permission options   | `allow_project` optionId                                                                                                               | caught                                                                                                        |
| Read-state key       | separator `\0`→`                                                                                                                       | `                                                                                                             | caught |
| Head/tail            | small-text fast path                                                                                                                   | caught                                                                                                        |
| Result budget        | strategy/direction                                                                                                                     | caught                                                                                                        |

Three uncaught mutations are reported as **equivalent mutants**, not test holes:
the matcher `!=1` guard, the diff `context` clamp, and the defensive
`isPositiveInt("")`. The `revisionId` mtime field is compared only for equality
between two reads of the same inode, so a consistent change to it is equivalent.

## Known gaps (honest limits)

- **Read media (image/PDF/video/notebook) is not ported.** The two pre-handler
  refusals (binary extension, blocking device) are reproduced; a valid image/PDF
  read returns neither the media block nor the Node text, because the port has no
  image/PDF runtime. This is a scope decision, stated rather than stubbed.
- **Read token-cap partial view** is not ported (needs the token estimator); very
  large reads return full content.
- **Write `stampMemoryOriginSessionId`** is a no-op in the port (memory root is not
  wired), so content is written verbatim. No vector exercises memory stamping.
- **`Read`'s streaming range path** for files > 10 MiB is replaced by a whole-file
  read. The two paths are equivalent in Node except for memory; documented in
  `range.go`.
- **`gb2312` uses the GBK codec**; identical on the sampled fixtures, but a
  character that iconv-lite maps differently under GB2312 could diverge.
- Only the classic (v3) `requestPermission` option shape is built. The v4
  interaction projection (allowOnce/allowAlways/deny reduced from the v4
  registry) is a separate layer, not in this package.
- `Read`'s `pages` field and the PDF pages validator are ported only enough to
  reproduce the recorded validation text; no PDF is opened.
