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

## P1/P2 as built

### P1 — MCP

1. **Doc shape.** `Layer.mcp: map[name]*McpServer`, merged by name in the same
   `MergeLayers` pass as providers (`null` deletes, `{name, enabled}` toggles an
   inherited entry). One entry:

   ```jsonc
   { "name": "docs", "enabled": true, "exposure": "direct", "timeout": 60,
     "transport": { "kind": "stdio",
                    "command": "npx", "args": ["-y", "server"],
                    "env": {"TOKEN": "sec_mcp_docs_env_TOKEN"},
                    "cwd": "",
                    "variants": { "darwin": {"command": "…"}, "win32": {"command": "…"} } },
     // or { "kind": "http", "url": "https://…", "headers": {"Authorization": "sec_mcp_docs_header_Authorization"},
     //      "oauth": {"clientId": "", "clientSecret": "sec_…", "scope": "", "callbackPort": 0,
     //                "callbackUrl": "", "clientName": "", "authServerMetadataUrl": ""},
     //      "authProvider": "" }   // pi's `auth.provider` (a pi provider token)
   }
   ```

   Any string field that is a `sec_…` reference is a stored secret; `${ENV}` is
   passed through. `transport.variants[<os>]` (darwin/linux/win32) replaces the
   default `command/args/env` on that runner OS; the runner resolves `runtime.GOOS`'s
   counterpart from Node's `process.platform` (`macos`→`darwin`).
2. **Validation at write** (mirrors pi's `validateMcpServerConfig`): name
   `^[A-Za-z0-9_-]+$`, no name collision under pi's namespace rule (`-` ≡ `_`),
   `sse` normalized to `http` + a warning (`warnings[]` on the doc write reply,
   surfaced by the mcp-sync list row as `error` — the settings list has no warning
   slot), stdio needs a command, http needs an http(s) URL, `streamable-http`→`http`,
   exposure ∈ codemode|deferred|direct|hidden (`codemode-deferred`→codemode), timeout>0.
3. **Push.** `config.doc.mcp: []` carries every entry for the runner with secret
   references resolved into `secrets` (ids `sec_mcp_<server>_env_<NAME>` /
   `_header_<Name>` / `_oauth_clientSecret`) — only enabled entries; the runner
   receives nothing for a disabled entry. A doc with MCP entries makes the runner
   *MCP-owned* (`mcpOwned`) even in provider fallback mode: the session's MCP
   extension then loads exactly the doc's servers. With no entries the runner keeps
   pi's default discovery (`~/.pi/agent/mcp.json`, trusted `.pi/mcp.json`).
4. **Runner apply.** Each server's `createMcpExtension({ loadConfig })` is added to a
   new session's `extensionFactories`; `createAgentSession` starts the session and
   fires `session_start`, which loads the config and connects. `loadConfig` returns
   the generation's entries as pi `McpServerEntry[]` (`source: "nex-server"`,
   `scope: "global"`), resolves `${ENV}`/`$ENV`, and `headers`/`env` are built from
   the generation's in-memory secrets. Running sessions keep their generation.
5. **Status.** The extension's `onChange` (state/error/tools) and connection success
   are reported through the engine to the link: facts `mcp: [{name, transport,
   state, toolCount, toolNames, error?, failureKind?}]` in `configApplied`, and a
   `mcpStatus` event when it changes. `mcp-sync.listWorkspaceMcpServerStatuses` is
   forwarded to the owning runner (`TargetWorkspace`), which answers from the live
   per-session registry (newest session; `connect` mode waits up to ~12 s for
   connecting servers); an empty config answers `{}`.
6. **OAuth.** Token presence is a fact: a server that answered 401 is
   `error`/`failureKind: not_authenticated`. Relay of pi's loopback callback is
   **not** in this release (the callback must run on the runner host and the URL
   must be pasted back); the doc's OAuth client fields are pushed so a later P3
   relay only adds the prompt transport. `auth.authorizationUrl` is therefore never
   produced by this server yet.

### P2 — skills, prompt templates, slash commands

1. **Doc shape.** `Layer.skills: []BundleRef` and `Layer.prompts: []BundleRef`
   (arrays replace per layer). `BundleRef {name, digest, kind: "skill"|"prompt",
   enabled, description?}`; `digest = "sha256:<hex>"` addresses file content on the
   server (`<config dir>/bundles/<hex>/files.json` + blobs). The server extracts the
   skill/prompt frontmatter at upload for list views; the runner re-verifies every
   file's digest before use.
2. **Upload.** The unchanged Skills settings page has list/setEnabled/delete only,
   so bundles arrive through `POST /api/config/skills?name=<name>` (skill body =
   `SKILL.md` text, multipart `files[]`, or `{"files":[{path,contentBase64}]}`;
   optional `scope=global|runner&runnerId=`; **upsert by name** so a re-upload is
   the update path) and `POST /api/config/prompts?name=<name>` (`{name, content}`
   → `<name>.md`). Both are session-authenticated like the import helper.
   `next/config.sh` wraps them for an operator. No UI change.
   `deleteSkill`/`setEnabled` are immediate doc edits (version bump → push).
3. **Runner materialization.** Bundles are written to
   `<runner data>/bundles/<hex>/` (atomic temp dir + rename) with the digest verified;
   a mismatch is `bundle-integrity` in `errors[]` and skips that bundle. New sessions
   pass `additionalSkillPaths: [<bundle>/files/<name>]` and
   `additionalPromptTemplatePaths: [<bundle>/files]` to `DefaultResourceLoader`;
   enabled-only, per generation.
4. **Catalog + `/` menu.** The `skills` channel is server-owned: `list` (doc bundles
   per runner scope, `capability.userScopeAvailable=false`), `setEnabled`,
   `deleteSkill`, `copyToCommon`/`removeFromCommon` as no-ops. `getSkillReferenceCatalog`
   (nex-agent) is forwarded (workspace target) to the runner, which reads pi's own
   loaded skills from a probe reloader (so fallback-mode `~/.pi/agent/skills`,
   `.pi/skills`, `.agents/skills` still appear) and returns them with `scope:"user"`.
   `nex-session.readWorkspacePresentation.slashCommands` lists prompt templates and
   `/skill:<name>` entries from the same reloaded resources. pi itself expands
   `/skill:<name>` and `/<template>` at prompt time, so no new protocol is needed.
5. **`commands` channel** answers `list → {commands:[]}`, `getPrimaryUserCommandsDirectory
   → {path:"","exists":false}` so the settings page does not error.

### Fallback

No providers / no MCP entries / no skill bundles in the doc → the runner behaves as
today (its own `~/.pi/agent` models, pi's `mcp.json` discovery, pi's skill
discovery). This is what keeps a `RemoteBuilder` runner (no server config) usable.

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

## As built (verification round)

### Decisions

1. **MCP status is answered by a hidden status session, not by "wait for a chat".**
   pi connects MCP servers per *session* (`session_start` → `mcp.json`/`loadConfig`,
   `session_shutdown` → close). Before any chat existed there was nothing to sample, so
   the runner answered the settings query with `connecting` placeholders forever. The
   engine now keeps one status session per applied MCP list (`mcpStatusOps.ensureProbe`,
   list identity = `McpWiring.key` = name + resolved pi config): the same server-owned
   `mcp` extension a chat uses, an in-memory session (no file, no title, never prompted),
   opened in the runner's first workspace. A chat built from the same list is used
   instead when it already exists. A new generation or shutdown disposes it through
   `session_shutdown`, so pi closes the stdio children (no leaked `npx`/fixture
   processes). `mcp-sync.listWorkspaceMcpServerStatuses` is now async: "connect" waits
   up to ~8 s for servers still starting, then answers what it has.
2. **A draft is rebuilt by replacing the pi session under the same id**, once pi's own
   start finished. pi's first background write can stamp the session file with a
   different id than the one the engine registered; `SessionManager.open` then adopts
   that id, so a rebuild used to produce a session the engine did not own (`send_failed:
   Session … is not open`). `openPiSession` now carries the engine's id
   (`OpenSession.sessionId`, used for events, metadata and the session map) separately
   from pi's `session.sessionId`, and the rebuild passes `session_start` reason `reload`
   so the MCP extension closes its old connections before loading the new list. Drafts
   are compared by list key (not just `owned`), and rebuilds are serialized per session;
   a session that was closed concurrently is rejected instead of rebuilt.
3. **Skills upload path**: unchanged from P2 (`POST /api/config/skills`,
   `POST /api/config/prompts`, `next/config.sh`); no UI button, the settings page lists
   what the server owns.
4. **OAuth state**: still a fact only (`error`/`not_authenticated` on a 401); no relay.
5. **Deferred**: OAuth callback relay on the runner host, a status row for servers with
   no workspace declared (tests without `workspacePaths` read sessions only), test
   connectivity/remote model listing.

### Known limit found in the E2E (not fixed here)

With a server-owned MCP list, the *welcome draft* composer's first send in the browser
E2E does not start a turn: the CLI accepts the input (the provider receives the prompt on
the sidebar "New task" chat), but the chat view that opened from Settings is the welcome
view: its timeline stays at zero rows and the composer clears to a disabled `Send`.
Selecting an existing task and sending works (draft rebuild + MCP tool call are covered by
`test/mcp-skills-pi-integration.test.ts`; the fallback-mode MCP E2E runs before this batch
verified the echo tool card and the `/skill:` menu end to end).
