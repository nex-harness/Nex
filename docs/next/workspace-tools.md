# Terminal, files and git on runners (batch 1) — spec

Status: planned on `feature/next`. Research with citations: `workspace-tools-research.md`.

## Goal

The web UI's terminal side pane, Files tab / code viewer / `@` mentions, and git panel work
against the machine a workspace lives on, including when two machines serve the same path.

## Decisions

1. **Routing by explicit identity (research §5.2 option 1).** Every path-bearing call of
   `file`, `file-watcher`, `media-preview`, `git`, `git-checkpoint` and `terminal` carries
   `{workspacePath, workspaceIdentity}` from the workspace the UI component belongs to. The
   UI change is one wrapper: `useWorkspaceServices` returns services whose path-bearing
   methods inject the bound workspace target; call sites that already hold a workspace use
   it. The server forwards these channels like `nex-agent` (identity → runner; unique path
   → runner; otherwise `ambiguous workspace path`). No per-connection "active tab" state on
   the server.
   - Host-global callers: `system.info` / `listIntegratedTerminalShells` route to the
     workspace's runner when a target is given, else the first runner. `DirectoryBrowser`
     gets a machine selector (runners from `/api/server-info`).
   - `file-watcher.onDynamicChange(id)` inherits the binding of the `watch` call that
     created `id` (server keeps `watchId → target`).
2. **The runner is the security boundary for files**: every path argument is resolved
   (realpath) and must stay inside one of the runner's served workspace roots; symlinks
   escaping a root are refused. Terminals are shells by design: access = being allowed to
   use that runner's workspaces. A terminal's `cwd` must be inside a served root.
3. **Reuse `@nex/services` implementations** (terminal, file, file-watcher, git,
   media-preview) through new fine-grained package entry points, bundled into the runner;
   `node-pty` stays an external runtime dependency with the platform prebuild copied in at
   install (same mechanism as desktop/server). An `architecture-policy.yaml` entry for
   `next-runner` records the allowed dependencies.
4. **Terminals live in a pty-host child process** of the runner (node-pty, ConPTY on
   Windows) with an `@xterm/headless` + serialize mirror per terminal. They survive browser
   disconnects, page reloads, server restarts and runner link drops; they end on `dispose`,
   tab close, or runner shutdown. After a runner restart, attaching to an old id yields an
   exited terminal with a short notice.
5. **Reattach after reload (small contract extension).** `terminal.create` accepts an
   optional `terminalId`; if that terminal is alive on the runner, it is reattached (and
   resized) instead of spawning. The UI stores `terminalId` per side-pane tab in
   `sessionStorage`. The first `onDynamicData` listen after create/reattach receives the
   serialized screen, then live output. Several viewers may watch one terminal; input from
   any viewer is written in arrival order per terminal.
6. **Terminal data stays UTF-8 strings** on both hops. The runner coalesces output per
   terminal (≈16 ms window, ≤ 64 KiB per event) and pauses the pty when the link's buffered
   amount crosses a watermark.
7. **Binary results** (`readFileRange`, `readBinaryPreview`) cross the runner link as the
   rpcwire JSON bridge's `{"$buf": base64}` form, which the Go server already converts to
   real buffers for the browser. Media previews are `inline` (capped) plus ranged reads;
   no host URLs.
8. **Git**: `refresh` rate-limited per workspace (≥ 1 s), commit-message generation uses
   the runner's model runtime (falls back to a deterministic summary on failure).

## Event order: open terminal, reload, reattach

```
UI create({cols,rows,cwd,workspace}) ─▶ server (route by identity) ─▶ runner
runner: pty-host spawn → id T → ACK {id:T}
UI listen onDynamicData(T) ─▶ runner: serialize(T) first, then live chunks
UI stores T for side-pane tab in sessionStorage
── page reload ──
UI create({..., terminalId:T}) ─▶ runner: T alive → resize, ACK {id:T}
UI listen onDynamicData(T) ─▶ serialized screen + live
```

## Acceptance

- Go: routing tests for every new channel (identity, ambiguity, offline), watcher binding.
- Runner: path containment (traversal, symlink escape), terminal reattach + replay + two
  viewers, link backpressure, `$buf` round trip, git on a temp repo.
- UI: wrapper injects targets; existing tests stay green.
- Browser E2E `next/e2e/workspace-tools.mjs` (`--e2e=tools`, in `all`): Files tab lists the
  workspace on machine B (two runners, same path) and opens a file in the viewer; `@`
  mention finds it; git panel shows a modified file and its diff, stage + commit works;
  terminal runs `echo` and shows output, survives a page reload with the earlier output
  visible.
- Windows: node-pty prebuild packaging verified by a bundle smoke on the build host;
  ConPTY run noted as untested until a Windows runner exists.
