# Batch 1: Terminal, Files, and Git on remote runners — implementation survey and design

Scope: the web UI is not to change. Its three surfaces are served by runners over the existing `nex-agent`/`nex-task` path (browser ⇄ `/ws` ⇄ Go server ⇄ `/runner` ⇄ runner). Everything below cites the current `feature/next` tree.

Summary of the main findings:

- The terminal channel uses string payloads only. Its four methods have no workspace argument, so the server cannot route them by identity today.
- Every `file` and `git` method carries `workspacePath` (some also `workspaceIdentity`), but the server refuses to forward such calls when two runners serve the same path. Chat itself avoids this via `runner:` identities.
- `packages/services` contains reusable, Node-only factories. The runner can import `@nex/services`'s thin node entry and bundle it, or copy the in-tree code. Architecture policy does not govern the runner today, so no hard blocker exists.

---

## 1. Terminal

### 1.1 Contract

`ITerminalService` (`packages/services/src/terminal/terminal.ts:12-27`), channel `"terminal"` (`packages/shared/src/channels.ts:71`):

| Member                             | Payload                                                                             | Notes                                     |
| ---------------------------------- | ----------------------------------------------------------------------------------- | ----------------------------------------- |
| `create({cols,rows,cwd?})`         | returns `{id, shell, fontFamily, fontSize?, theme?, fontFamilySource, windowsPty?}` | `cwd` is an absolute path or absent       |
| `write({id, data:string})`         | void                                                                                | input and paste, raw bytes as a JS string |
| `resize({id, cols, rows})`         | void                                                                                |                                           |
| `dispose({id})`                    | void                                                                                | the UI calls this to kill the PTY         |
| `onDynamicData(id): Event<string>` | one `string` per PTY `onData` chunk                                                 |                                           |
| `onDynamicExit(id): Event<number>` | exit code                                                                           | emitter dies with the process             |

Data is framed as text only. The PTY is spawned with `encoding: "utf8"` (`terminalService.ts:174-186`), so node-pty decodes into JS strings. Multi-byte characters split across read boundaries are reassembled by node-pty before reaching the string. Neither `Buffer` nor `Uint8Array` ever appears. The wire handles strings natively (tag 1 in `rpcwire/value.go:27-34`), but the server JSON-escapes them on the runner link (§5.2).

There is no `list`, `attach`, `detach`, `replay` or `serialize`. An id is valid only while the process lives. Ids are per-service sequential integers (`terminalService.ts:355-357`), and restarting the service resets them.

`system.listIntegratedTerminalShells()` (`packages/services/src/system/system.ts:11-15`) returns `IntegratedTerminalShellOption[]` = `{dialect:"cmd"|"git-bash", id, label, path, source}` (`shared/src/protocol.ts:87-107`). It returns `[]` off Windows (`system/integratedTerminalShells.ts:19-21`), and the real one enumerates `cmd.exe` plus Git Bash. The **only** consumer is the settings page's Windows shell dropdown (`ui/src/SettingsPage.tsx:360-378`). It runs on `localHostServices` (deliberately the _local_ host) and only when `info.platform === "win32"`.

### 1.2 UI behaviour

- **Side-pane terminal.** `SidePaneTerminalPane` (`ui/src/SidePaneTerminalPane.tsx:5-53`) renders `TerminalSession` with `persistentKey = tab.id`, `workspaceKey` and `cwd`. `AnimatedSidePanePanel` passes `cwd={tab.cwd ?? workspaceAbsPath}` and `workspaceKey = workspaceIdentity?.trim() || workspaceAbsPath` (`AnimatedSidePanePanel.tsx:1283-1292`, `:413`). A `TerminalSidePaneTab` has `{id:"terminal:<uuid>", cwd?, remoteSessionId?}` (`ui/src/lib/workspaceSidePane.ts:96-106`, `:694-705`).
- **How many terminals:** unbounded. Each "Open terminal" creates a new tab, thus a new PTY, and the module-level `sidePaneTerminalSessionRegistry` keeps the xterm instance and PTY alive across workspace switches and unmounts (`ui/src/terminal/sidePaneTerminalSessionRegistry.ts:1-45`). Each entry holds `terminalId`, `cwd`, `workspaceKey` and a `dispose` closure (`:25-51`). An entry is dropped only when the tab closes or its workspace is removed. The bottom panel (`ui/src/Terminal.tsx`, `useTerminalService.ts:26-90`) is desktop-style and irrelevant to web.
- **Lifecycle:** create → immediately subscribe `onDynamicData`/`onDynamicExit` → `term.onData → write()` → on unmount of the tab `dispose({id})` (`TerminalSession.tsx:544-600`, `:588-610`, `:644`, `:529-537`). Resize is `fitAddon.fit()` plus `resize()` (`:169`).
- **No replay.** If the data listen is lost, so is output. The registry only survives in-page unmounts, not reloads.

### 1.3 Existing implementation (reuse candidate)

`createTerminalService({settingService})` (`terminalService.ts:353-444`):

- Shell resolution (`:248-263`), cwd fallback (`:272-286`), an env normaliser for macOS GUI PATH, `TERM=xterm-256color`, UTF-8 locale (`:194-240`).
- Windows ConPTY with `useConptyDll` fallback (`:157-193`), and a darwin `spawn-helper` chmod repair (`:104-146`).
- Font and theme from settings via `terminalProfile.ts`.
- A `Map<id, {pty, dataEmitter, exitEmitter}>` is the entire session state (`:358-361`).

The only Electron-adjacent code is the `app.asar` path replacement (`:100-102`) and the SEA/`NEX_PTY_ENTRY` loader (`terminalPtyLoader.ts`, `terminalPtyFallback.ts`). Both are no-ops in a plain Node runner. This service is close to directly reusable.

---

## 2. Files

### 2.1 Contract (`packages/services/src/file/file.ts:14-94`)

| Method                                                                              | Params                                                    | Returns                                                                   | Carries identity? |
| ----------------------------------------------------------------------------------- | --------------------------------------------------------- | ------------------------------------------------------------------------- | ----------------- |
| `searchWorkspaceFiles`                                                              | `{rootPath, workspaceIdentity?, query, limit?, refresh?}` | `WorkspaceFileEntry[]`                                                    | **yes**           |
| `readdir`                                                                           | `{path, includeHidden?}`                                  | `FileEntry[]` `{name,path,type,isSymbolicLink?}` (`protocol.ts:11-17`)    | no                |
| `stat`                                                                              | `{path}`                                                  | `{path,type,size?,mtimeMs?}` (`:19-26`)                                   | no                |
| `checkFilesExist`                                                                   | `{paths}`                                                 | `[{path,exists}]`                                                         | no                |
| `resolvePath`                                                                       | `{path}`                                                  | realpath string                                                           | no                |
| `readTextFile`                                                                      | `{path, offset?, length?}`                                | `FileTextSlice` (`:39-47`)                                                | no                |
| `readMediaPreview`                                                                  | `{path, maxBytes?}`                                       | `{path,mediaType,dataBase64,totalBytes}` (`:49-54`)                       | no                |
| `readFileRange`                                                                     | `{path,offset,length}`                                    | top-level `Uint8Array`                                                    | no                |
| `readBinaryPreview`                                                                 | `{path,maxBytes?}`                                        | `{path,dataBase64,totalBytes}`                                            | no                |
| `listWorkspaceFilesLength`                                                          | `{rootPath}`                                              | number                                                                    | no                |
| `listWorkspaceFilesRange`                                                           | `{rootPath,offset,length}`                                | packed string (4 MB chunks, `shared/src/workspaceFileEntriesCodec.ts:20`) | no                |
| `ensureConversationWorkspace` / `createDefaultWorkspace` / `createScratchWorkspace` | —                                                         | paths                                                                     | n/a               |
| `.nexignore` read/transform/write                                                   | `{rootPath,...}`                                          |                                                                           | no                |

Related channels:

- `file-watcher` — `watch({path, recursive?})→{id}`, `unwatch({id})`, `onDynamicChange(id): Event<{dirPath, changedPath?}>` (`fileWatcher/fileWatcher.ts:11-25`, `shared/src/protocol.ts:29-37`). Path-only.
- `media-preview` — `prepare({path, expectedKind})→{kind:"inline", dataBase64,…} | local-url | host-range-url`, optional `release({previewId})` (`media-preview/mediaPreview.ts:36-50`). Path-only.

### 2.2 Consumers and their arguments

- **Files side pane.** `WorkspaceFileTree` receives `workspacePath` + `workspaceIdentity` (`AnimatedSidePanePanel.tsx:1276-1284`). The data hook uses `useWorkspaceServices(path, remoteSessionId, identity)` (`workspace-file-tree/useWorkspaceFileTreeData.ts:60-68`) and calls:
  - `fileService.readdir({path, includeHidden:true})` (`:222-225`) — **path-only**.
  - `gitService.getIgnoredPaths({workspacePath, paths})` (`:233-236`).
  - `gitService.refresh({workspacePath})` for row badges (`workspace-file-tree/gitStatus.ts:14-18`).
  - Per-expanded-directory `fileWatcherService.watch({path})` plus `onDynamicChange` (`useWorkspaceFileTreeWatchers.ts:60-90`) — **path-only**.
  - Index: `listWorkspaceFilesLength` + `listWorkspaceFilesRange` in 4 MB chunks (`workspace-file-search/fetchWorkspaceFileEntries.ts:10-29`; callers `useWorkspaceFileSearchIndex.ts:69`, `command-center/CommandCenterDialog.tsx:573`).
  - Optional `fileService.readWorkspaceFileSearchIgnore` etc. from settings (`settings/WorkspaceFileSearchSection.tsx:82-156`).
- **Code viewer.** `PreviewPane` resolves services from the _source's_ workspace path/identity (`PreviewPane.tsx:503-521`) and calls `readTextFile({path, offset:0, length:MAX})` (`:896-901`), `readMediaPreview` (`:953`), `stat` + chunked `readFileRange` for PDF/Office/media (`:1161-1215`, `:1348-1378`), `readBinaryPreview` (`:1257`), `mediaPreviewService.prepare/release` (`:1023-1074`), and a `usePptxFileWatch` watcher. **All path-only.**
- **@-mentions.** `useFileMentionProvider(path, identity, query, …)` calls `searchWorkspaceFiles({rootPath, workspaceIdentity, query, limit})`, then a `refresh:true` retry on zero hits (`mentions/providers/fileMentionProvider.ts:31-91`). It uses `useServices()` (the context accessor), **not** `useWorkspaceServices`, but it forwards the identity explicitly. This is the _only_ file method the server can route by identity today.
- **Inline chat images.** `markdown-image.tsx:185-189` calls `readMediaPreview` through the ambient `useOptionalServices()` — no workspace context at all.
- **Assistant preview cards.** `assistantPreviewCardValidation.ts:57` calls `checkFilesExist` (ambient services).
- **DirectoryBrowser.** Opens at `system.info().homedir` and calls `fileService.readdir({path})` per navigation, hidden files off by default (`DirectoryBrowser.tsx:55-100`). It is mounted with platform `services` (`Root.tsx:688-696`), i.e. the base/local accessor. It cannot be routed per workspace: it is a host-wide folder picker.
- **`system.info()`** is called from the git auto-refresh (platform detection), workspace home path, and the header path (`useGitAutoRefresh.ts:69-76`, `useWorkspaceHomePath.ts:58-61`, `WorkspaceContextPath.tsx:19`).

### 2.3 Routing problem

Server routing (`router.go:217-233`, `:245-325`) only inspects the arg's top-level `workspacePath`/`workspaceIdentity`, and only for `nex-agent`/`nex-session`/`nex-task`/`model-selection`/`provider-settings` (special-cased). Everything else falls to `local(channel)` or `TargetUnknownChannel` (`router.go:217-229`). Today `file`, `system`, `git` are local stubs (`local/stubs.go:60-72`, `stubs_file.go:9-17`, `stubs_git.go:9-31`, `stubs_system.go:9-14`). `file-watcher`, `media-preview`, `git-checkpoint` and `terminal` are **not registered at all**, so the web UI receives "Channel name '…' not found".

Path-only calls that cannot be routed by identity:

- `file.readdir/stat/checkFilesExist/readTextFile/readMediaPreview/readFileRange/readBinaryPreview/resolvePath/*.nexignore`, plus `listWorkspaceFilesLength/Range` (no `rootPath` identity).
- `file-watcher.watch/unwatch` and `media-preview.prepare/release`.
- `terminal.*` (no workspace anywhere, §1.1).
- `git.*`, which carry `workspacePath` but no identity except the mention path.
- `DirectoryBrowser` readdir and `system.info`.

Registry behaviour today (`runners/registry.go:472-500`, `:552-640`):

- Identity → runner fast path; a live runner id wins even if the path is no longer catalogued.
- Path with **0** owners → `ErrNoRunnerForWorkspace`.
- Path with **>1** owner → `ErrAmbiguousWorkspacePath`.

So a naive "forward `file.*` by `workspacePath`" breaks the moment two runners share `/home/me/projects`, which `multi-runner.md` explicitly declares legal (two clients, e.g. a Mac and a WSL box with the same path).

---

## 3. Git

### 3.1 Contract

`IGitService` (`git/git.ts:26-61`), channel `"git"`:

| Method                                                                        | Args                                                              | Returns                                                                                                                                        |
| ----------------------------------------------------------------------------- | ----------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------- |
| `refresh`                                                                     | `{workspacePath, includeIdentity?, includeBranchComparison?}`     | `{summary, identity, unstagedChanges, stagedChanges, branchComparison}` — one `git status` snapshot split into three (`gitService.ts:321-355`) |
| `getRepositorySummary` / `getWorkspaceRepositoryInfo` / `getBranchComparison` | `{workspacePath}`                                                 | summary shapes                                                                                                                                 |
| `getLocalBranches` / `getCommitGraph`                                         | `{workspacePath[, limit…]}`                                       |                                                                                                                                                |
| `switchBranch` / `createBranchAndSwitch`                                      | `{workspacePath,targetBranchName}` / `{…,branchName}`             | `GitBranchMutationResult`                                                                                                                      |
| `getChanges` / `getIgnoredPaths`                                              | `{workspacePath[,paths]}`                                         |                                                                                                                                                |
| `getDiff`                                                                     | `{workspacePath, path, sourceId}`                                 | `GitDiffResult`                                                                                                                                |
| `stagePaths` / `unstagePaths` / `discardPaths`                                | `{workspacePath, paths}`                                          | void                                                                                                                                           |
| `generateCommitMessage`                                                       | `{workspacePath, …, conversationContext?}`                        | `{message, providerId, model}`                                                                                                                 |
| `commit` / `push`                                                             | `{workspacePath, message, paths, stagedOnly}` / `{workspacePath}` |                                                                                                                                                |
| `getIdentity`                                                                 | `{workspacePath}`                                                 |                                                                                                                                                |

`GitRepositorySummary` includes `autoRefreshWatchPaths[{path,recursive}]`, `isGitAvailable`, `isRepository`, `repoRoot`, branch/ahead/behind (stub shape `stubs_git.go:10-27`).

`git-checkpoint` (`git/gitCheckpoint.ts:15-24`): `createCheckpoint`, `diffCheckpoints`, `restoreBetweenCheckpoints`, `deleteCheckpoint`. **No UI consumer exists** in `packages/ui/src` (checkpoint/rewind is done by runner-side pi hooks, `next-runner/src/engine/pi/piFileFactsOps.ts`, `engine/checkpoints/*`). Out of batch scope; register an empty stub at most.

### 3.2 Git panel behaviour

- `useGitRepository({workspacePath, workspaceIdentity, …})` uses ambient `useServices()` (`hooks/useGitRepository.ts:390`) and calls `gitService.refresh({workspacePath, includeIdentity, includeBranchComparison})` once per open/refresh (`:466-469`). It notes the old 3-call pattern caused a triple `git status` per agent write (`:464-466`).
- `GitPane` diff expand calls `gitService.getDiff({workspacePath, path, sourceId})` (`GitPane.tsx:204-208`).
- Stage/unstage/discard/commit are **placeholders** in `hooks/useGitActions.ts:28-56` (toast only). The real mutations live in `GitActionMenu` (`gitService.refresh` `:929`, `generateCommitMessage` `:1001`, `push` `:1080`, `stagePaths` `:1172`, `commit` `:1177`) and `useGitBranchSwitcher` (`getLocalBranches` `:71`, `switchBranch` `:252`, `createBranchAndSwitch` `:284`, `stagePaths` `:341`, `commit` `:346`), both ambient services and path-only.
- Branch switcher / graph: `GitGraphDialog.tsx:59-128` calls `getCommitGraph({workspacePath,…})`.
- **Auto refresh:** `useGitAutoRefresh` watches `summary.autoRefreshWatchPaths` plus the workspace root (recursive except on Linux) through `fileWatcherService` with 60 s debounce, and also reads `systemService.info().platform` (`useGitAutoRefresh.ts:15-140`, `lib/gitAutoRefresh.ts:27-70`). So git freshness depends on **file-watcher and system.info being routed to the same runner as git**.

### 3.3 Existing implementation

`createGitService({commitMessageGenerator?})` → `createGitCliRepo()` (`gitService.ts:170-174`): spawn-based git CLI, per-workspace promise coalescing and caching (`git/repo/gitCliRepo.ts:539-544`). The commit-message generator is LLM-backed and injected (`gitCommitMessageGenerator.ts:24-56`). Pure Node + `git` binary + `@nex/shared`; reusable.

---

## 4. Reuse assessment: `@nex/services` in `packages/next-runner`

- **Dependency weight.** `@nex/services` depends on `@larksuiteoapi/node-sdk`, `croner`, `ignore`, `node-forge`, `undici`, `yaml`, `yazl`, `@nex/provider`, `@nex/provider-node`, `@nex/nex-cua` (`services/package.json:13-30`). However, `services/src/node.ts` is the Node assembly boundary and already exports the factories (`:26`, `:77`, `:82`, `:127`, `:35`). Only the transitive graph of these five factories is pulled in: pure `node:fs`/`node:path` code + `@nex/shared` + `node-pty` loaders. `node.ts`'s _imports_ it does not use would still be bundled unless fine-grained entry points are added. Today `exports` only expose `.`, `./node`, `./cua-permission-broker`, etc. (`services/package.json:5-12`). Recommendation: add `./file/impl`, `./git/impl`, `./fileWatcher/impl`, `./terminal/impl` entries, or import the source files by relative path when typechecking, then bundle.
- **Electron/host assumptions.** None in the path (grep shows no `electron` import in these modules). `terminalService` handles SEA/asar defensively; those branches are inert under `node dist/runner.mjs`.
- **Node-only.** Yes (fs, child_process, `fs.watch`).
- **Arch-policy.** `architecture-policy.yaml:1-70` lists modules; `packages/next-runner` is **absent**, and `services` is `managed:false`. `global.managedOnly:true` means the policy only enforces managed modules (exceptions empty). So importing `@nex/services` from the runner is not currently a policy violation. It does cross a domain boundary (CLI→services), so a new `next-runner` module entry with `managed:true, requires:[shared, services, rpc]` would codify it. `next-runner` already depends on `@nex/shared` (`next-runner/package.json:19`) and `@nex/rpc`/`@nex/provider` as dev deps (`:23-24`).
- **Licences.** MIT throughout: `node-pty@1.1.0` (`THIRD-PARTY-NOTICES.md:1639`), `@lydell/node-pty-*` (`:287-293`). Adding DSH/`@nex/services` sources to the runner bundle needs no licence change.
- **Runner-link incompatibility.** The runner's `send` is `JSON.stringify` (`next-runner/src/link/client.ts:277-282`). It says so itself: "Buffers are not used by this runner… they never occur in channel results" (`link/frames.ts:1-6`). `readFileRange`/`readBinaryPreview` return binary and `listWorkspaceFilesRange` may be MBs; both need base64 or a bridge change. The server bridge already maps `{"$buf":base64,"$vs":bool}` both ways (`rpcwire/jsonbridge.go:13-23`, `:173-217`), but the runner does not encode/decode it. `FromJSON` emits `Buffer.Data` on the browser side, which becomes a real `Uint8Array` tag 2/3 (`value.go:108-124`). So **a runner returning `{ $buf, $vs }` works through the Go bridge today**; the runner only needs to emit/decode that form, and the TS types it receives must be unwrapped before calling services.
- **Verdict.** Import (with granular entry points) instead of reimplementing:
  - Terminal: ~15 KB, tests exist (`services/test/terminalPtyFallback.test.ts`).
  - File: 25 KB + search/ignore (~hard to rewrite faithfully: TTL cache, `.nexignore`, packed index).
  - Git: 12 KB + 59 KB CLI repo (status parsing, untracked-dir collapsing — expensive to reimplement).
  - Watcher: 5 KB.
  - Media-preview: trivial, but needs a URL strategy (§5.5).

---

## 5. Proposed design

### 5.1 The terminal channel

Two viable designs:

**A. Strictly on-contract (no UI change), "attach" via deterministic ids.**

- Channels: `terminal` + `system` routed to a runner. Routing key: `cwd` (§5.3) is too weak (null `cwd`). Better: route `terminal.*` by an **explicit tab binding** (below).
- Runner-side: `create` spawns a pty-host child (`node-pty`) keyed `tern:<runnerId>:<uuid>`. A `@xterm/headless` `Terminal` + `SerializeAddon` attaches to each pty in the host.
- `onDynamicData(id)` behaves as:
  1. `attach(id, clientConnId)` registers the viewer; if first viewer in this generation, emit `serialize()` snapshot then live chunks.
  2. On a second viewer, no snapshot (single-writer, multiple viewers: one writer = the pty, all viewers receive the same bytes).
- Reconnect after reload is the hard part: the UI loses `map` state, so a reload creates a **new** terminal because `SidePaneTerminalSessionRegistry` is module state (`sidePaneTerminalSessionRegistry.ts:21-23`). Core terminal panes (VS Code / VS Code Server per research `REPORT.md` §一-4, `DESIGN.md:449-474`) solve this with a persisted tab↔terminalId map. On web, that means either:
  - persist `terminalId` in `localStorage` keyed by `tab.id`, or
  - a server-side per-connection "active terminal" binding (§5.3 option 2) so after reload the reconnect call re-attaches by tab id.
- Without a UI change: after reload a new PTY is born; the old one must be reaped (idle TTL + no viewers). That leaks a shell per reload for ≤ TTL.

**B. Minimal UI change (recommended).** Add an optional `terminalId` to `create`/`attach` semantics in the contract (still no visual change):

- `create({cols, rows, cwd, terminalId?})`: if `terminalId` exists on runner → re-attach, return the same id.
- `TerminalSession` stores `terminalId` next to `persistentKey` in `sessionStorage` (tab.id is stable) and passes it back on mount, then asks the runner for a replay snapshot (extend `onDynamicData` to fire a first synthetic chunk on a fresh listen id; the runner can do this _without a contract change_ by having the server send a private `terminal.replay` call before the listen, or simpler: encode replay as `create`'s _side effect_: the first `onDynamicData` listen registered after `create` gets `serialize()` first).
- This is 30-60 LOC of UI and a small contract addition, versus losing terminal fidelity (vim/TUI state) that the design (headless xterm) exists to protect.

Either way, the pty host must own the screen emulator so replay is byte-exact (`@xterm/headless`). `@xterm/xterm@^5.5` is already a UI dep (`ui/package.json:44-46`); the headless + serialize packages are MIT and tiny; add to `next-runner` deps.

**Transport (high-frequency stream).**

- The runner pushes `{"t":"event","id":<listenId>,"value":"<chunk str>"}` frames on the shared `/runner` WS. The server's `Conn.write` is one mutex-guarded WS write per frame (`next/server/internal/runnerws/runnerws.go:317-324`) → server re-`enqueue`s per browser connection (`browserws.go:271-312`). Per-op overhead ≈ 3 JSON encodes + 3 WS frames.
- Batch: coalesce PTY reads per terminal in a 8-16 ms window into one string (target ≤ 60 msg/s). Reuse `SnapshotThrottle` (`next-runner/src/v4/throttle.ts:14-58`) with a byte cap (e.g. 64 KB per event) and, for high-volume output (`yes`, `cat` of a big file), fall back to truncating intermediate chunks and sending `serialize()` deltas only. (Full-screen redraw is the safe alternative; partial truncation corrupts escape sequences. Practical rule: batch, cap, and drop middle chunks only between escape boundaries, or simply cap throughput and let the pty block — node-pty stops reading when `pause()` is used.)
- **UTF-8 strings, not base64.** Strings are native on both hops. The server JSON-escapes non-ASCII and control bytes (`rpcwire/jsonbridge.go:55-59` → `appendJSString`). Overhead for ansi-heavy output is mostly `\u001b` → 6 bytes per ESC; 1-3× inflation. Base64 costs 1.33× uniformly _plus_ the browser-side `Uint8Array` tag path, which `onDynamicData(string)` cannot carry without a UI change (xterm also needs a string). Keep strings, and **apply server-side framing discipline** (long JSON frames are fine: browser limit 32 MiB/frame, 64 MiB queue, `browserws.go:37-38`).
- Backpressure:
  - Runner → server: WS `bufferedAmount`; if above a watermark, `pty.pause()` and resume when drained. Never queue unbounded.
  - Server → browser: existing byte budget closes slow clients (`browserws.go:286-300`); a closed browser merely leaves the viewer list. Terminals keep running (vs VS Code's 3 h disconnect lifetime, `REPORT.md` §一-4).
- Input: `write({id,data})` is a normal call. It must be ordered with `create` and `resize`; Channel RPC promises on one `/ws` are dispatched concurrently (`browserws.go:364-369`), so the runner should serialize per-terminal input through a chain, because the pty write order is what matters.

**Dedicated channel vs JSON link:** A second WS (`/runner/terminal`-style) would avoid JSON marshalling of ≤ 64 MiB frames and let the Go server splice bytes. It is more server work (registry by runnerId:terminalId, auth, ownership) and breaks the "one link" invariant (`README.md` "Runner link protocol"). **Recommendation: stay on the JSON link for batch 1**, add batching; revisit only after measuring.

**Lifecycle.**

- `pty-host` child survives runner link drops and browser disconnects. Terminals die only on `dispose`, tab/workspace close, or runner shutdown (`main.ts:60-70` kills the child).
- Runner restart (or upgrade) kills pty hosts. After restart the server-forced `dispose` surfaces as "Terminal not found: <id>". Recommended: persist ids → short-lived (10 min) "dead terminal" markers so `create` returns a clear `[Terminal exited]` replay instead of a spinner. Cold restart of shells is acceptable and documented.
- Server restart / runner disconnect: the browser's listens are dropped and re-bound on reconnect (`registry.go:546-640`), so viewers re-attach and get a fresh serialize snapshot — **only if** replay is triggered on listen (design A/B), which makes server restarts transparent.

**Security.**

- Terminal is an arbitrary shell by design. Gate it on the same runner token (already the machine boundary). No workspace containment possible or intended. Document that a runner grants shell access to anyone the server authorizes for that runner's workspaces.
- `cwd` must be absolute and exist (the service already falls back to `HOME`, `terminalService.ts:272-286`) but should additionally be restricted to a served workspace root when supplied by a tab, so a forged call cannot read other users' trees on the machine beyond what the shell could anyway. (Low value; optional.) §5.3 gives the real routing guard.

### 5.2 File / watcher / media-preview

1. **Run on the runner** (`createFileService({workspaceFileSearchFilter?})`, `createFileWatcherService()`, `createMediaPreviewService({fileService, inlineMaxBytes})`). Register in `register.ts` behind the same `createWorkspaceGuardedRegistry` wrapper (`register.ts:268-298`, currently only guards `workspacePath`).
2. **Add identity-bearing arguments** where the UI already holds identity:
   - `listWorkspaceFilesLength/Range`, `readdir` (file tree already has `workspaceIdentity` in `useWorkspaceFileTreeData.ts:60`), `readTextFile`/`stat`/`readFileRange`/`readMediaPreview` (PreviewPane has `source.workspaceIdentity`, `PreviewPane.tsx:503-519`), `file-watcher.watch` (the tree/git hooks have the identity in scope: `useWorkspaceFileTreeWatchers.ts:6`, `useGitAutoRefresh.ts:43-46`), `git.*` (GitPane/GitActionMenu already take `workspaceIdentity` props, `GitPane.tsx:46-77`, `GitActionMenu.tsx:822`).
   - Methods that take a _path_ rather than a root cannot be bound by prefix identity safely. The clean UI change is a per-workspace **service accessor** that injects `workspaceIdentity` as an extra arg, plus Go-side routing that accepts it.
3. **Recommended routing (avoid per-connection active-tab state on the server):**
   - **Option 1 (recommended): add `workspaceIdentity` (and `workspacePath` for `rootPath`-less methods) to every path-bearing argument at the UI call sites above, via a small `useWorkspaceServices` wrapper that binds `{workspacePath, workspaceIdentity}` into calls.** Rationale:
     - Callers already have identity in the hook/props.
     - `multi-runner.md` "Key rule stays `identity?.trim() || path`". This change mirrors the existing `searchWorkspaceFiles` precedent.
     - It is stateless, survives reconnects, supports two tabs on different machines simultaneously, and keeps listens routeable (`RegisterListen` + identity survives reconnects, `registry.go:552-560`).
   - **Option 2 (fallback, no UI change): server-side per-connection tab map.** The server learns "this connection's active workspace = identity X" from existing traffic (every forwarded `nex-agent` call carries it) and routes identity-less `file/git/terminal` calls there. Risks: parallel workspace tabs in one connection (allowed: `tabs[]`), background indexes for non-active workspaces (`useWorkspaceFileSearchIndex`), `DirectoryBrowser` (host-global), and mention provider using ambient services. It is a fragile heuristic. **Not recommended.**
   - **Host-global callers** (`DirectoryBrowser`, `system.info` for the picker, `ensureConversationWorkspace`) need a target. Proposal:
     - Route `system.info` to the **first runner** (like model-selection).
     - `DirectoryBrowser` gets a machine selector from `/api/server-info` (it already lists runners) or is scoped to the chosen runner. This is a small UI change consistent with the picker work in `multi-runner.md` "UI changes".
     - `ensureConversationWorkspace` stays local (web already stubs it, `stubs_file.go:12-15`).
4. **Payloads across the JSON link.**
   - `readFileRange`/`readBinaryPreview` → `Uint8Array` becomes `{$buf: base64, $vs:false}`. Go bridge already translates to the browser (verified above). The runner must produce that sentinel, and its link decoder must convert `$buf` back for _args_ (arguments rarely carry buffers).
   - `listWorkspaceFilesRange` returns a 4 MB string; this crosses the runner↔server link intact (Go limit 32 MiB/64 MiB, `runnerws.go:33`, `rpcwire/frame.go:106`) and the browser (32 MiB frame). OK, but each 4 MB chunk gets JSON-escaped twice. Acceptable (chunks are rare).
   - `readMediaPreview.dataBase64` at ≤ 8 MB is fine.
5. **Media preview URLs.** The desktop implementation can mint `local-url`/`host-range-url`, which browsers cannot use. Use `inline` (base64) with a lower cap (e.g. 2-4 MB) plus `readFileRange` chunking for PDFs and large media (PreviewPane already chunks PDFs when `stat.size` exists, `:1186-1215`). For video/audio playback, the realistic batch-1 answer is inline or "unsupported"; a ranged HTTP endpoint on the Go server would be a later item.
6. **Watcher fidelity.** Reuse `createFileWatcherService()`; add an idle cap (e.g. 256 watchers/runner) because each expanded dir = 1 watcher.

### 5.3 Git

- Register `git` on the runner with `createGitService({commitMessageGenerator})`. `generateCommitMessage` needs the runner's model SDK. Provide a generator adapter that calls the runner's pi/LLM path, or return a deterministic fallback (subject = branch + file list) in batch 1. The UI degrades with an error toast (`GitActionMenu.tsx:1001-1060`).
- Route by identity with the same injected-arg approach as files. `refresh` is the hot path; it is one `git status` per call and `gitCliRepo` already coalesces concurrent status requests (`gitCliRepo.ts:543`). Add a per-workspace rate limit (e.g. ≥ 1 s between refreshes) on the runner to absorb many tabs.
- **Auto-refresh correctness** needs `file-watcher` + `system.info` + `git` on the **same runner**. With identity-bearing args this holds by construction. With option 2 it would be a hazard (an ambient `system.info` returning the server's linux → Linux-rule `autoRefreshWatchPaths`, `gitAutoRefresh.ts:45-50`, chosen for the wrong host).
- `stubs_git.go`'s "isGitAvailable:false" empty state must be replaced by real forwarding; otherwise the panel is permanently empty.
- `git-checkpoint`: register a no-op stub on the same runner (UI has no callers; keep `git-checkpoint` routed by identity for future).

### 5.4 Windows / packaging notes

- **node-pty builds required:** `node-pty@1.1.0` (N-API, MIT) ships prebuilds for darwin-x64/arm64, linux-x64/arm64, win32-x64/arm64. node-pty's own package does not include linux prebuilds; the repo pulls them from `@lydell/node-pty-<platform>-<arch>@1.2.0-beta.10` (`desktop/scripts/node-pty-package-assets.mjs:12-45`, `packages/server/package.json:22-25`) and copies `prebuilds/<plat>/pty.node` into place. On darwin the `spawn-helper` binary must be executable (the service repairs 0644→0755, `terminalService.ts:104-146`). On Windows, ConPTY needs `conpty.node` + `conpty.dll` (or the system ConPTY via `useConpty:true, useConptyDll:false` — the service's fallback, `terminalService.ts:157-193`). `winpty` is only a legacy fallback.
- **Runner bundle.** `scripts/bundle.mjs:15-49` bundles `@nex/shared` + `ws` into `dist/runner.mjs` and keeps `@earendil-works/*` external with a pinned `dist/package.json` (`npm install --omit=dev` on target). For node-pty:
  - Keep **`node-pty` external** and add it to `runtimeDependencies`, exactly like pi. Bundling native addons through esbuild inlines JS but the `require("../prebuilds/…")` probes break (the very problem `terminalPtyFallback.ts:1-25` describes for SEA). With `node-pty` external, `npm install` on the target fetches the right prebuild, but the linux postinstall compile path (needs `make`/`g++`) is a risk.
  - Better: add `@lydell/node-pty-*` for the target platform as optional deps (the `@lydell/node-pty` wrapper resolves the platform package itself, verified at `/Users/tylinux/.npm/_npx/…/@lydell/node-pty/package.json:18-27`), and copy the prebuild into `node_modules/node-pty/prebuilds/<plat>/` in a postinstall step (reuse `node-pty-package-assets.mjs` logic). This is the same flow the desktop/server use today.
  - The pty-host child must be resolved relative to `dist/` (e.g. `dist/pty-host.mjs`, spawned with `process.execPath`), so `NEX_PTY_ENTRY`-style indirection from the SEA path (`server/src/seaEntry.ts:83-134`) is not needed.
- **Windows shells.** The runner should use `ComSpec`/`pwsh` resolution from `resolveTerminalShell()` (`terminalService.ts:248-263`) and expose `listIntegratedTerminalShells` from the runner's `system` channel. The settings page uses the _local_ host's shells (`SettingsPage.tsx:360-378`), so a Windows runner's shells appear only after moving that call; batch 1 can leave settings as-is and resolve the shell server-side.
- **Paths.** Use the shared path predicate (`toWorkspaceRelativeGitPath`, `git/config.ts`) and forward slashes for git; the services already do.

### 5.5 Server changes (Go)

1. `router.go`: treat `file`, `git`, `file-watcher`, `media-preview`, `git-checkpoint`, `terminal`, `system` as forwardable (`TargetWorkspace`) when the arg carries a runner identity (or path), instead of `local()` → `TargetLocal`. Keep a small local table for `system.info` fallback → `TargetFirstRunner`, and `terminal` needs an explicit target (§5.6).
   - `ResolveEvent` must also forward `file-watcher.onDynamicChange` (the listen arg is the watcher id, so it must inherit the _watch_'s binding; simplest is to make the listen argument `{id, workspaceIdentity}` or maintain a server-side id→target map populated at `watch`, `browserws.go:563-671`).
2. `browserws.go`: no protocol change; the runner's `{ $buf }` values decode via `rpcwire.FromJSON` (`:633-642`) and encode to the browser as tag 2/3 buffers (`value.go:104-124`).
3. Runner `register.ts`: add `file`, `git`, `file-watcher`, `media-preview`, `system` (info/shells), `terminal` handlers with the guarded registry; extend the guard to check `rootPath` prefixes against served workspaces and to reject path-bearing calls whose normalized path escapes every served root (`register.ts:268-298`). This matters for a stale/forged identity. The server also allows an identity carrying an uncatalogued path for a live runner (`registry.go:476-484`), so the runner is the true boundary.
4. Server must register these channels for **local** mode too (otherwise `Lookup` misses and every call 404s). Simplest: a `ForwardOnly` route kind that returns `runner offline` when no runner.

### 5.6 Estimates (engineer-days, one senior dev, including tests)

| Item                                                                                                                             | Est. | Notes                                                                        |
| -------------------------------------------------------------------------------------------------------------------------------- | ---- | ---------------------------------------------------------------------------- |
| Runner: pty-host child (node-pty external, ConPTY fallback, spawn-helper repair, headless xterm + serialize, idle TTL, shutdown) | 3–4  | Reuses `terminalService` logic; the child IPC is the new work                |
| Terminal channel + replay/attach semantics + batching/backpressure                                                               | 2–3  | Contract extension (§5.1 B) ≈ 0.5 d incl. UI                                 |
| UI minimal change: tab↔terminalId persistence + identity injection wrapper                                                       | 1–2  |                                                                              |
| File/git/watcher/media services on runner + `$buf` codec in link                                                                 | 2–3  | Mostly wiring; risk = `@nex/services` granular entrypoints + bundle size     |
| Go routing for new channels + listen binding for watchers + tests                                                                | 2–3  | Ambiguity/offline/identity tests mirror `router_test.go`, `registry_test.go` |
| Windows: prebuild packaging, bundle postinstall, ConPTY smoke                                                                    | 2–3  | Highest unknown; needs a Windows VM                                          |
| DirectoryBrowser machine scoping (UI)                                                                                            | 1    |                                                                              |
| Runner restart "dead terminal" UX + docs                                                                                         | 1    |                                                                              |
| E2E (`next/e2e/…`): terminal survives reload + second viewer; files on machine B; git on machine B                               | 2–3  |                                                                              |

**Total ≈ 16–24 days.** Critical path: Go routing + runner terminal host (parallel), then Windows packaging.

---

## 6. Interplay notes (explicitly asked)

- **Agent "Bash" tool vs terminal:** none. The runner's pi engine executes bash through its own tool path and a read-only classifier (`next-runner/src/engine/permissions/bashClassifier.ts:1-40`) and records shell-touched paths from checkpoints (`engine/pi/piFileFactsOps.ts:26-33`). No shared PTY, no `pid`, no output stream to the terminal panel. The desktop's `integratedTerminalShell` setting feeds only agent runtime env (`services/src/node.ts:1927-1929`), not the terminal service.
- **File change events:** the runner emits `nex-task` workspace events (task/session state, `channels/nexTask.ts`), **not** file-change events. Files/git freshness after agent edits comes from `fileWatcherService` on the tree/git hooks (debounce 150 ms tree, 60 s git), so a runner that edits files will show changes as long as the watcher is routed to that runner (§5.3).
- **Read-only workspaces:** `App.tsx:189-192` gates opening the code viewer/git for read-only tabs; the server does not need to enforce read-only (the runner guard is the security boundary).

## 7. Key risks

1. **Path-only calls with duplicate paths** (two runners serving the same path) are the main design trap. The injected-identity option removes it; option 2 does not.
2. **Runner-link JSON + `JSON.stringify`** cannot carry `Uint8Array` until the `{$buf}` codec is added runner-side. Required for PDF/image previews.
3. **Bundling `@nex/services`** pulls a large graph through `node.ts` unless entry points are added; typecheck cost and bundle size must be measured.
4. **node-pty on Linux without a compiler** (npm install fallback) needs the `@lydell` prebuild copy step; without it, terminals silently fail (VS Code's `remoteAssetDeployDecision.ts:137-166` is the precedent).
5. **Terminal replay** is impossible without headless-xterm serialization; the current xterm-only UI state (`TerminalSession.tsx`, `sidePaneTerminalSessionRegistry.ts`) is the reason a reload loses the screen today.
