# Server-owned config, MCP and skills (batch 4) — spec

Status: in progress on `feature/next`. Research with citations:
`config-mcp-skills-research.md` (sections A–D).

## Goal

Configure model providers, API keys, MCP servers and skills once in the web UI's Settings
and have every runner use them, without editing files on each machine.

## Decisions

1. **The server owns desired config; runners own effective config and local facts.**
   One `ConfigDoc` (schema 1, monotonic `version`) with layered scopes
   global → runner → workspace → session (maps merge by key, arrays replace, `null`
   deletes). This release fills global and runner scopes; workspace/session keys exist in
   the schema but stay empty.
2. **Migration: local fallback until configured, then server wins.** A runner whose
   entitled config has no providers keeps using its own `~/.pi/agent` (today's behavior).
   As soon as the server config defines providers, the runner uses only the server's
   providers and keys. Settings offers "Import from <runner>" (one shot, per runner): the
   runner exports its models.json providers, keys (as secrets, never shown to the browser)
   and mcp.json into that runner's scope. Nothing is imported silently.
3. **Secrets**: stored on the server encrypted with AES-256-GCM (stdlib), key file
   `<data>/keys/config.key` (0600, created on first use, outside the config file); AAD =
   secret id. The browser only ever sees `hasSecret` and a masked hint. Runners receive
   only the secrets of providers/MCP servers enabled for them and keep them in memory.
4. **Push protocol** on the runner link: S→R `config {version, doc, secrets, bundles}`
   (full merged snapshot for that runner, never deltas); R→S `configApplied {version, ok,
errors[], facts}`. `hello` gains optional `lastAppliedVersion`; the server pushes after
   `welcome` when stale. Unknown frames are ignored by both sides, so old peers keep
   working. Per-item failures are reported in `errors[]` with `ok:true`; a structurally
   invalid doc is `ok:false` and the runner keeps its previous config.
5. **Applying in the runner**: an immutable `RuntimeConfig` per version; new sessions use
   the latest one, running sessions keep theirs (a `ModelRuntime` per config generation).
   Settings via `SettingsManager.inMemory`; providers via derived models.json +
   `setRuntimeApiKey` / `registerProvider`; MCP via `createMcpExtension({loadConfig})`;
   skills via `additionalSkillPaths` pointing at materialized bundles.
6. **Channels**: the server serves `provider-settings` writes (create/save/delete provider,
   add/edit/enable/delete model, refresh) and builds views from the doc plus runner facts
   (available models, auth status). `model-selection.getView` likewise. `mcp-sync`
   load/save are served by the server; `listWorkspaceMcpServerStatuses` is forwarded to
   the owning runner (live status, tool counts, OAuth URL as a fact). `skills.list /
setEnabled / deleteSkill` are server-owned; `getSkillReferenceCatalog` and
   `readWorkspacePresentation.slashCommands` come from the runner's loaded pi resources
   (so `/skill:name` and prompt templates work through pi's own expansion).
7. **MCP** transports are stdio (with optional per-OS command variants) and http;
   `sse` entries are normalized to http with a UI-visible warning (pi rejects SSE).

## Event order: user edits a provider

```
Browser ─ provider-settings.savePersonalProviderOverlay ─▶ Server
Server: validate → merge → ConfigDoc v(n) → reply View(revision++)
Server ─ config{v(n), merged doc, secrets} ─▶ each entitled runner
Runner: build RuntimeConfig v(n) → refresh models / runtime keys ─▶ configApplied{v(n), facts}
Server: merge facts → provider-settings.onDidChange / model-selection.onDidChange ─▶ Browser
Offline runner: on reconnect hello.lastAppliedVersion < v(n) ⇒ immediate push
```

## Phases (this batch)

- **P0** providers + models + keys (server config store, push protocol, runner apply,
  provider-settings writes, model-selection from doc+facts, import-from-runner).
- **P1** MCP (global list + per-OS variants, live status forwarded, paste-back OAuth fact).
- **P2** skills + prompt templates + slash commands (content-addressed bundles uploaded
  through Settings, runner materialization, pi expansion).

Deferred: OAuth login relay for subscription providers (local `~/.pi` OAuth keeps working
in fallback mode), `listRemoteModels` / `testModelConnectivity` (need server→runner
calls), provider templates, remote SSH sync dialogs, workspace/session scope editors.

## Acceptance

- Go: config store merge/validation/encryption round-trip, entitlement filtering, push on
  change and on stale reconnect.
- Runner: apply/rollback on invalid doc, fallback vs server-owned modes, running sessions
  unaffected by a config change, MCP via loadConfig, skills from bundles; schema checks.
- Browser E2E `next/e2e/settings.mjs` (`--e2e=settings`, in `all`): add a provider with a
  key in Settings → its model appears in the composer picker on a runner → chat with it;
  disable the model → it disappears; add an MCP server (a tiny stdio test server in the
  repo) → status connected with tool count → the model calls its tool; add a skill → it
  appears in the `/` menu and `/skill:name` works.
