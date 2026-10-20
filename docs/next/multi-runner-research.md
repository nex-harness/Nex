# Multi-runner workspace identity research

## 1. How the remote workspace mechanism works today

**Web and desktop are different products here.** The remote registry is effectively desktop-only.

`remoteSessionId` is an opaque key into a renderer-local store, not an RPC field: `useRemoteWorkspaceSessionStore.sessionsById` maps it to `{sessionId, target?, services, dispose?}` (`packages/ui/src/store/remoteWorkspaceSessionStore.ts:15-30`). Registration is `registerRemoteWorkspaceSession` (`:117-132`), which also seeds the agent-service generation. `bindRemoteWorkspaceIdentity` / `bindRemoteWorkspacePath` populate the two lookup indexes (`:155-167`). `resolveWorkspaceRemoteSessionId` prefers `target.remoteSessionId`, then `sessionIdByWorkspaceIdentity[identity]`, then path fallback only for legacy identity-less remote tabs (`packages/ui/src/lib/workspaceServiceResolver.ts:23-42`).

`useWorkspaceServicesResolution` marks a target remote if `workspaceIdentity` is non-empty or a `preferredRemoteSessionId` exists (`packages/ui/src/hooks/useWorkspaceServices.tsx:87-136`, check at `:96`). If remote and no registered session resolves, it returns the disconnected proxy (`:54-72`, `:156-215`); `rpcReady` is false only in the `remote-waiting` state (`:194-211`). `useWorkspaceTaskLists` then skips the shard entirely (`packages/ui/src/hooks/useWorkspaceTaskLists.ts:322-343`), so the sidebar is empty.

**Desktop path:** `window.nex.connectRemote` → main `desktopRemoteSessions.connect` (`packages/desktop/src/main/desktopRemoteSessions.ts:600-660`) → host `ConnectRemoteWorkspace` (`packages/desktop/src/host/index.ts:2107-2175`) → `connectRemote` in `packages/server/src/remote/connect.ts:106-217`, which SSHes/WSL/Dockers into the target, deploys a remote server, and builds a `RemoteServiceAccess`. The host sends a `MessagePort` to the renderer; the renderer registers it at `packages/desktop/src/renderer/src/main.tsx:205-223` with merged services from `remoteWorkspaceSessionServices.ts:1-60`. `bindRemoteWorkspaceSessionContext` rebinds canonical path/identity and re-attaches (`desktopRemoteSessions.ts:635-660`). The UI-side connect flow is `useRemoteWorkspaceHistory.ts:523-620` (`selectRemoteWorkspaceProjectFromDialog`), `:750-790` (`connectRemoteWorkspaceTarget`), `:992-1050`; reconnect is `reconnectRemoteWorkspaceHistoryEntry.ts:83-220`. Remote history is persisted in `setting.lastWorkspaceSession` (`packages/ui/src/lib/remoteWorkspaceHistory.ts:400-425`; schema `packages/shared/src/validationAppSettings.ts:105-121`).

**Web path:** `platform.connectRemote` is a stub returning failure (`packages/web/src/main.tsx:183-189`); `Root` is rendered with `allowRemoteWorkspace={false}` (`:471`), which hides the SSH dialog and remote restore. The only web remote support is bootstrap `?remote=<id>` (`:306-340`), which connects `/ws/remote/<id>` as the _base_ service accessor — not a registered remote session. `/api/connect-remote` and `/ws/remote/:id` exist only in the Node server (`packages/server/src/http.ts:361-376`, `:431-457`); that endpoint is one-shot (`:443-446`) and exposes only file/git/system/terminal (`:448-455`), no `nex-agent`/`nex-task`. Nothing in `packages/web` or `packages/ui` calls `registerRemoteWorkspaceSession`; the only caller is the desktop renderer (`packages/desktop/src/renderer/src/main.tsx:215`). Therefore no existing web flow can make a remote session appear in `sessionsById`.

**Identity format:** `remote:ssh:<host>:<port>:<user>:<path>` built by `buildRemoteWorkspaceIdentity` (`packages/ui/src/lib/remoteWorkspaceHistory.ts:137-147`; shared twin `packages/shared/src/remote-workspace-identity.ts:46-63`) and parsed only for `ssh|wsl|docker` (`:68-112`). Host is lowercased, port defaults to 22, username is trimmed, path is separator-normalized, trailing slashes removed, and always starts with `/`. Parsing is strict: `runner:<id>:<path>` returns null.

## 2. Options evaluated

### A. Per-runner `/ws/remote/<runnerId>` speaking the channel RPC

Server work is moderate: add an endpoint that authenticates the browser and bridges the same channel server to one runner. But the UI side is the problem. The web client has no code to open such a socket and register it: `connectRemote` is stubbed (`packages/web/src/main.tsx:185-189`), and `?remote=` only replaces the _base_ accessor, not a remote session. You would need new web bootstrap/registration code, a synthetic session id per runner, and lifecycle handling. The existing desktop design is one session per workspace, not per runner; a shared per-runner session would be unregistered when any tab closes (`packages/ui/src/root/useRemoteWorkspaceTabLifecycle.ts:113,190`) and would call `disposeRemoteSession` for the whole runner. The Node `/ws/remote/:id` is one-shot and service-subset, so it cannot be reused as-is. Effort: high, and still leaves web-specific UI work.

### B. Synthetic `remote:ssh:<runnerName>:22:<user>:<path>` identities

The server can route by parsing the identity, and the UI will correctly treat the tab as remote. But it still requires a registered session in `sessionsById`; otherwise every call hits the disconnected proxy (`useWorkspaceServices.tsx:54-72`). On web there is no registration path (see above). To persist such a tab, `RemoteWorkspaceSessionEntry.target` is mandatory (`packages/shared/src/validationAppSettings.ts:112-120`), so you must invent an SSH target; reconnect then calls the stubbed `platform.connectRemote` and fails, and the SSH dialog shows fake hosts (`packages/ui/src/remote-connection/sshHistorySuggestions.ts:24-40`). Effort: high and semantically wrong.

### C. `runner:<runnerId>:<path>` identity routed through the main connection

Keep the single `/ws` connection. The server parses `workspaceIdentity` and routes to the runner id; the UI must stop treating every non-empty identity as remote. This is the smallest change that preserves one sidebar for all machines and correctly separates identical paths.

The UI misbehaves today at these exact points (all identity-truthiness checks):

- `packages/ui/src/hooks/useWorkspaceServices.tsx:81-85` (`hasRemoteWorkspaceMetadata`), `:96` (`resolveWorkspaceServiceIsRemoteTarget`), `:54-72` (disconnected proxy fallback).
- `packages/ui/src/lib/workspaceServiceResolver.ts:45-50` (`isRemoteWorkspaceTarget`), `:52-77`.
- `packages/ui/src/lib/workspaceRpcAvailability.ts:6-12` (disables workspace RPC until `remoteSessionId`).
- `packages/ui/src/store/tabWorkspaceIdentity.ts:12-18` (`hasRemoteTabIdentity` / `isLocalWorkspaceTab`; affects tab matching when no identity option is passed).
- `packages/ui/src/hooks/useLocalWorkspaceScopes.ts:4-6` (excludes identity tabs from pinned/timeline/grouped/automation scopes).
- `packages/ui/src/lib/remoteWorkspaceHistory.ts:50-56` (`hasRemoteWorkspaceIdentity`) and `:400-425` (drops identity tabs from persistence unless a remote entry exists).
- `packages/ui/src/root/useRemoteWorkspaceTabLifecycle.ts:13-20` (treats identity tabs as remote and calls `platform.disposeRemoteSession` on close).
- `packages/ui/src/ChatEmptyState.tsx:134-140` (filters identity-only tabs out of the workspace menu), `:225-238`.
- `packages/ui/src/Root.tsx:300-310` (excludes identity tabs from `localWorkspacePathForRemoteConnection`).
- Cosmetic only: `packages/ui/src/WorkspaceSidebar.tsx:180`, `packages/ui/src/WorkspaceArchivedTasksFlatSection.tsx:139`, `packages/ui/src/WorkspaceHeaderSections.tsx:181-183`.

The shared parser consumers (`packages/shared/src/remote-workspace-identity.ts`; `packages/services/src/session/taskIndexRepo.ts:197,592,1408`; `packages/services/src/nex-agent/nexAgentService.ts:1641,2460`; `packages/server/src/windowController.ts:22`; `packages/desktop/src/host/index.ts:362,1454`; CLI `v4-bridge.ts:1366,1570`, `workspace.ts:19`) all return null/false for a `runner:` identity and treat it as local. In the next topology those host services are not on the web path (the Go server and next-runner do not parse identity), so this is safe. It is exactly the inconsistency §8 warns about for arbitrary identities — but here it is deliberate and contained.

## 3. How workspaces are presented and picked

`GET /api/server-info` is read only for `workspaces[0]` (`packages/web/src/main.tsx:313-340`); its `path` and optional `workspaceIdentity` become the initial tab (`packages/ui/src/root/useRootPlatformEffects.ts:96-120`). The schema is `packages/shared/src/server-remote.ts:5-27`; the Go shape is `next/server/internal/httpapi/httpapi.go:46-62,290-320`. There is no UI that enumerates the array.

On web, `preferDirectoryBrowser` is true (`packages/web/src/main.tsx:464`), so “Open folder” opens `DirectoryBrowser` (`packages/ui/src/root/useRootWorkspaceActions.ts:340-390`), which calls `systemService.info()` for `homedir` and `fileService.readdir` (`packages/ui/src/DirectoryBrowser.tsx:74-110`) against the connected server. In the Go server those are stubs returning `/` and `[]` (`next/server/internal/local/stubs_system.go`, `stubs_file.go`). `onSelect` returns only a path; `handleSelectProject(path)` then calls `addTab(path)` and `startDraftInWorkspace(path)` with no identity (`useRootWorkspaceActions.ts:292-300`).

`recentProjects` is written by `useRootWorkspaceActions.ts:310-320` and `packages/ui/src/hooks/useTabPersistence.ts:204-220`, and the Go server fills it with routed paths (`next/server/internal/local/settings.go`). But on web it is not rendered anywhere: the only consumers are the Settings memory-scope list (`packages/ui/src/SettingsPage.tsx:258-269`) and desktop startup (`packages/desktop/src/main/startupWorkspace.ts:75-80`). It is not a project picker.

There is no existing host list to feed. The SSH dialog only shows connection-history suggestions derived from `remoteWorkspaceSessions` (`packages/ui/src/RemoteConnectionFields.tsx:144`; `sshHistorySuggestions.ts:24-40`). The sidebar and composer workspace menus list only currently open tabs (`WorkspaceSidebar.tsx:357-360`; `ChatEmptyState.tsx:325-380`). The remote directory step browses a _connected_ remote session (`RemoteConnectionDialogContent.tsx:428-430`), which web cannot create. So a machine picker must be added (small), fed either by extended `/api/server-info` or a new local channel.

## 4. Channel calls that carry identity

- `nex-agent`: `NexAgentWorkspaceTarget = {workspacePath, workspaceIdentity?, remoteSessionId?}` (`packages/services/src/nex-agent/nexAgentPluginParams.ts:8-13`); session target adds `sessionId` (`nexAgent.ts:148`). This covers conversation subscribe/resync/unsubscribe, command send/query, rowsRange, plans, file changes/rewind, workflow runs, attachments, sessions-index and workspace-config subscribe/resync/unsubscribe, `setConnectionFlowStateV4`, and all legacy session/plugin/automation methods. `listSessionSubagents` also carries `remoteSessionId` (`:219-224`).
- `nex-task`: nearly every method takes `{workspacePath, workspaceIdentity?}` (`packages/services/src/session/nexTaskService.ts:208-217`, `:379-400`, `:440-470`, `:580-700`); `sendPrompt` carries `remoteSessionId?` (`:246`); `onDynamicWorkspaceEvent` takes the workspace object (`:725`); `listTaskList` takes `workspaceScopes[]` with identity (`packages/services/src/session/nexTaskListTypes.ts:6-20`).
- `nex-session`: `NexSessionWorkspaceTarget = {workspacePath, workspaceIdentity?, remoteSessionId?}` (`packages/services/src/nex-session/nexSession.ts:20-27`).
- `file`: `searchWorkspaceFiles` has `workspaceIdentity?` (`packages/services/src/file/file.ts:11-19`); `readdir`/`stat`/preview are path-only (`:22-40`).
- `git`: all requests are path-only (`packages/shared/src/git.ts:96-98`); identity appears only inside commit-message generation (`packages/services/src/git/gitService.ts:292`).
- `window-controller`: task-list scopes carry identity and items may carry `remoteSessionId` (`packages/services/src/window-controller/windowController.ts:33-42`; `nexTaskListTypes.ts:6-20`).
- Events: `nex-agent.onDynamicConversationFrame`, `onDynamicSessionsIndexFrame`, `onDynamicWorkspaceConfigFrame`, `nex-task.onDynamicWorkspaceEvent` (`packages/next-runner/src/channels/register.ts`), and the v4 transports spread `{workspacePath, workspaceIdentity}` into every call (`packages/ui/src/v4/agentConversationTransport.ts:89-94`, `agentSessionsIndexTransport.ts:71-84`).

In the Go router, `workspacePathFromArg` reads only `workspacePath` (`next/server/internal/router/router.go:230-273`). `workspaceIdentity` is never read, by design.

## 5. Recommendation: Option C

### Server changes (`next/server`)

1. `internal/runners/registry.go`: add `Workspace.Identity` and `Info.RunnerName`; replace the single-owner `paths map[string]*runner` with `identities map[string]*runner` plus `paths map[string][]*runner` (or keep `paths` for unique paths). Accept duplicate paths across runners; reject only duplicate identities. Add `RunnerByIdentity`, `CallTarget(ctx, identity, path, ...)`, and identity-aware `RegisterListen`. `Workspaces()` returns `{path, label, workspaceIdentity, runnerId, runnerName}`.
2. `internal/router/router.go`: add `Route.WorkspaceIdentity`; add `workspaceIdentityFromArg` mirroring the path helper. Prefer a valid `runner:<runnerId>:<path>` identity for `nex-agent`/`nex-task`/`nex-session` calls and listens; fall back to path when absent (backward compatibility, unique paths).
3. `internal/browserws/browserws.go`: `forwardCall` and `handleListen` pass both identity and path to the registry. Event bindings stay keyed by listen id, so runner reconnects keep working.
4. `internal/httpapi/httpapi.go`: extend the server-info workspace object with `workspaceIdentity`, `runnerId`, `runnerName` (extra fields are ignored by the current web client).
5. `internal/local/settings.go`: keep `recentProjects` as deduped paths; do not put identities there.

### Runner changes (`packages/next-runner`)

1. `src/channels/register.ts`: read `workspaceIdentity` from the arg and pass it to task/presentation handlers (the runner may ignore it for I/O).
2. `src/channels/nexTask.ts`: `listTasks({workspacePath, workspaceIdentity})`; `toNexTaskMeta(..., identity)` includes `workspaceIdentity` in rows and in `workspace_task_list_changed.taskMeta`. This is required because the sidebar joins task rows by `buildTaskWorkspaceKey(path, identity)` (`packages/ui/src/hooks/useWorkspaceTaskLists.ts:100-160`); without the echo, rows from `listTasks` are filtered out when the scope key is the identity.
3. `src/channels/nexSession.ts`: return `workspace: {workspacePath, workspaceKey: identity ?? path}`.
4. `src/v4/sessionsIndex.ts`: set snapshot `workspaceId` to `identity ?? path` for consistency (the UI does not filter on it, but stores it).

### UI changes (smallest diff)

1. Add a shared helper, e.g. `isUiRemoteWorkspaceIdentity(identity) = isRemoteWorkspaceIdentity(identity.trim())` (from `@nex/shared`), and replace identity-truthiness at the sites listed in §2C: `useWorkspaceServices.tsx:81-85,96`; `workspaceServiceResolver.ts:45-50`; `workspaceRpcAvailability.ts:6-12`; `tabWorkspaceIdentity.ts:12-18`; `useLocalWorkspaceScopes.ts:4-6`; `remoteWorkspaceHistory.ts:50-56,400-425`; `useRemoteWorkspaceTabLifecycle.ts:13-20`; `ChatEmptyState.tsx:134-140,225-238`; `Root.tsx:300-310`. Leave the cosmetic Cloud icons or switch them to the same helper.
2. Persistence: add optional `workspaceIdentity` to the local `appWorkspaceSessionEntrySchema` (`packages/shared/src/validationAppSettings.ts:105-110`); in `buildPersistedWorkspaceSessionEntries` (`remoteWorkspaceHistory.ts:400-425`) emit it for server-routed tabs; restore it in `remoteWorkspaceSessionPersistence.ts:100-150` and `useTabPersistence.ts:89-107`. `restoreTabs` already accepts `RestorableWorkspaceTab.workspaceIdentity` (`packages/ui/src/store/tabStore.ts:44-61`).
3. Picker: add a small `RunnerWorkspacePicker` (new file under `packages/ui/src/`) fed by `GET /api/server-info` (now with runner fields), listing machine → workspace. On select call `addTab(path, {workspaceIdentity})` and `startDraftInWorkspace(path, identity)`. Wire it into `useRootWorkspaceActions.handleOpenWorkspace` when `preferDirectoryBrowser` (`useRootWorkspaceActions.ts:340-390`), or into `DirectoryBrowser` as an optional machine header. Extend `handleSelectProject(path, identity?)` (`:264-325`) to pass identity. This is the only genuinely new UI surface; without it, “Open folder” cannot convey which machine.
4. Model catalog: `model-selection.getView` and `provider-settings.getView` have no workspace in their args and currently go to the first runner (`router.go:200-215`). Add optional `{workspacePath, workspaceIdentity}` to the `getView` input from `packages/ui/src/hooks/useModelSelectionView.ts:128` and `packages/ui/src/v4/composer/useDraftModelReadinessGate.ts:96,136`, and route by identity when present. Otherwise a workspace on machine B shows machine A's models.
5. Header: optionally derive a machine label from `workspaceIdentity` in `WorkspaceHeaderSections.tsx:181-183` so the user sees which machine a tab belongs to.

### What still breaks or remains limited

- `file`/`git` are server-local stubs in the Go server today; if they are later forwarded, their params lack identity (`git.ts:96-98`, `file.ts:22`). Add optional identity to those params or route by a per-connection active identity.
- `window-controller` pinned/timeline lists remain server-local stubs, so those sections stay empty on web regardless of runner count; the project sidebar uses `nex-task`, which works.
- `server-info.workspaces[0]` still auto-opens only one workspace; others require the new picker.
- Duplicate paths collapse in `recentProjects`; the picker must be the source of truth.
- The `runner:` identity is intentionally not parseable by `parseRemoteWorkspaceIdentity`; any host-side code that must treat it as remote (future desktop use) needs an explicit branch.

### Effort

Server: 1-2 days (registry/router/dispatcher/httpapi + tests). Runner: ~0.5 day (identity echo). UI: 2-3 days (predicate sweep, persistence schema, picker, model-selection arg). Option A/B: 4-7 days plus semantic debt.
