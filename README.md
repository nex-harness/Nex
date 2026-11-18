# Nex-v3

Go-native, zero-CGO multi-runner Agent Harness inspired by `pi-agent`, featuring a centralized Control Plane (`nex-server`), cross-platform autonomous headless Runners (`nex-runner` for macOS, Linux, and Windows), and a unified Web / `egoist/mygo` Desktop & Mobile Workbench.

## Highlights

- **Pure Go (`CGO_ENABLED=0`) Static Binaries**: Cross-compiles cleanly to `darwin/arm64`, `linux/amd64`, and `windows/amd64` (including Windows Job Object process-tree cleanup and native Windows `ConPTY`).
- **Dual Core Streaming Protocols**: Native streaming adapters for **OpenAI / Codex Responses API (`/v1/responses`)** and **Anthropic Messages API (`/v1/messages`)**, preserving encrypted reasoning items and thinking signatures across multi-turn tool calls.
- **Autonomous Offline-Capable Runners**: Built-in `robfig/cron/v3` scheduler, SQLite WAL snapshot + Zstd content-addressable blob store (`blobs/`), and `seq`-ordered Outbox sync on reconnect.
- **Two-Tier Lazy MCP + `goja` Code Mode**: Prompt zero-bloat discovery (`searchTools` / `describeTool`), spawn-on-first-call + idle-TTL reaping for local `stdio` MCP servers, and concurrent `Promise.all(tools.<ns>.<tool>())` inside an isolated zero-privilege `goja` VM.
- **TypeScript Dynamic Plugins + Remote `ctx.ui`**: Server-side pure-Go `esbuild` transpilation, Runner-side `goja` execution, lifecycle hooks (`tool_call`, `tool_result`), `/slash` commands, and remote declarative `ctx.ui` modals/badges.
- **Multi-Tab Workbench + `libghostty` WASM Terminal**: Embedded React 19 UI with `coder/ghostty-web` (`libghostty-vt.wasm`) binary WebSocket PTY, lazy workspace `Files` tree, new-tab file preview, `[± 更改 +X -Y]` Git Diff `Review` tab, 6-category `Context windows` inspector, cold/hot SQLite session archiving (`main.db` <-> `archive.db`), and hourly rollup cost analytics (`Today` / `7D` / `30D`).

## Quick Start (Local End-to-End Web Workbench)

```bash
# Build embedded Web UI + nex-server + nex-runner + nex-app and launch locally on http://127.0.0.1:8080
make run-local
```

Or run verification suites directly:

```bash
make test          # Go race + coverage tests across all packages
make test-web      # Frontend Vitest suite
make verify-nocgo  # Cross-compile darwin/arm64, linux/amd64, windows/amd64 with CGO_ENABLED=0
```

See [`docs/01-architecture-spec.md`](docs/01-architecture-spec.md) through [`docs/08-implementation-roadmap.md`](docs/08-implementation-roadmap.md) for full architecture specifications.
