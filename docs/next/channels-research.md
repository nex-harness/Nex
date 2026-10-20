# Web UI ↔ new server channel contract (MVP research)

Scope: the existing React web UI (`packages/web` → `packages/ui`) talking over the existing RPC channel protocol (`packages/rpc`: channel name + method + JSON args; events via `onXxx` / `onDynamicXxx(arg)`) to a new server that implements only `setting`, `broadcast`, `window-controller`, `model-selection`, `provider-settings`, `nex-agent`, `nex-session`, `file` plus cheap stubs.

All paths below are relative to the repository root.

## 0. Executive findings (read this first)

1. **The sidebar in the default view does not read the window-controller task list.** The default sidebar mode is `organizeBy: "project"` (`packages/ui/src/lib/sidebarTaskPreferences.ts:16-19`). In this mode `WorkspaceSidebar` uses `useWorkspaceTaskLists` (`packages/ui/src/WorkspaceSidebar.tsx:614`, render at `:1309-1530`), whose row set is **tasks-index** via the `nex-task` channel, enriched by `sessions-index` activity (`packages/ui/src/hooks/useWorkspaceTaskLists.ts:101-160`, `:349-430`; `packages/ui/src/v4/buildTaskListResultFromSessions.ts:1-4`). `sessions-index` alone never creates a row (`packages/ui/src/v4/buildTaskListResultFromSessions.ts:1-4`, `packages/ui/src/v4/mapSessionSummaryToTaskMeta.ts:1-4`).
2. Therefore the proposed channel set is **missing `nex-task`**. Without it the session will never appear in the project sidebar: the active/archived/pinned-meta membership reads must succeed or the hook throws `"tasks-index task 行读取不完整"` and keeps an empty list (`packages/ui/src/lib/taskListMembershipSets.ts:197-262`, `packages/ui/src/hooks/useWorkspaceTaskLists.ts:584-586`).
3. `window-controller` is still needed on page load because the pinned section is always rendered in project mode and calls `useGlobalTaskList(kind:"pinned")` (`packages/ui/src/WorkspacePinnedTasksSection.tsx:113-120`; `packages/ui/src/WorkspaceSidebar.tsx:1309`), which subscribes to both controller topics (`packages/ui/src/v4/windowControllerTaskListRegistry.ts:186-197`). Timeline/archived/grouped views and the header pin menu also use it (`packages/ui/src/WorkspaceTimelineTasksSection.tsx:140`, `packages/ui/src/WorkspaceArchivedTasksFlatSection.tsx:85`, `packages/ui/src/hooks/useGroupedTaskView.ts:657`, `packages/ui/src/WorkspaceHeaderSections.tsx:137`).
4. **Do not invent a custom `workspaceIdentity`.** The UI treats _any_ non-empty `workspaceIdentity` as a remote workspace and swaps in a disconnected proxy unless a remote session id is registered (`packages/ui/src/hooks/useWorkspaceServices.tsx:90-100`, `:150-215`, `:47-70`). `runner:<id>:<path>` is not a valid `remote:` identity (`packages/shared/src/remote-workspace-identity.ts:68-112`) and will break every workspace RPC. Leave `workspaceIdentity` undefined and use `workspacePath` only.
5. `onDynamicXxx` is a **method call** (`channel.listen` with arg) and `onXxx` is a plain listen (`packages/rpc/src/proxy-channel.ts:115-140`, `:164-172`). Because the UI uses `ProxyChannel.toService`, property-existence checks are always true, so the server should implement unknown events as `Event.None` rather than throwing. An unknown _channel_ promise rejects after 1 s; an unknown channel event listen is silently dropped (`packages/rpc/src/channelServer.ts:100-115`, `:222-246`).
6. The cheapest correct MVP transport strategy for projection updates is to send **full snapshot frames** (snapshot `fromSeq: 0`, `toSeq: newSeq`) for every change. The stores replace state on snapshot and never gap-check snapshots (`packages/ui/src/v4/conversationProjectionStore.ts:648-706`, `packages/ui/src/v4/sessionsIndexStore.ts:94-131`). Deltas are optional. This avoids implementing the append log.

## 1. `setting`

**Interface:** `packages/services/src/setting/setting.ts:5-19`

```ts
get(): Promise<AppSettings>
update(patch: Partial<AppSettings>, expectedAccountSettings?): Promise<void>
updateDataBaseDir(newDir: string | undefined): Promise<void>
ensureDefaultProject(homedir: string): Promise<{ path: string; created: boolean }>
```

There is **no `onDidChange` on the RPC contract**. `createObservableSettingService` adds a host-internal `onDidUpdate`, explicitly “does not widen the Setting RPC contract” (`packages/services/src/setting/observableSettingService.ts:1-31`). UI-side refresh comes from `platform.onSettingsChanged` and the broadcast channel `settings:app-runtime-preferences` (`packages/ui/src/hooks/useSettingService.ts:128-145`, `packages/shared/src/app-runtime-preferences.ts:3`).

**Type:** `AppSettings` at `packages/shared/src/protocol.ts:237`. Only `recentProjects: string[]` and `locale: Locale` are required in TypeScript; everything else is optional. Zod defaults live in `appSettingsObjectSchema` (`packages/shared/src/validationAppSettings.ts:433-492`), wrapped by `appSettingsSchema` (`:494`); patches validate with `appSettingsPatchSchema` (`:510`). The real service returns `appSettingsSchema.parse({})` defaults when the file is missing/corrupt (`packages/services/src/setting/settingService.ts:64`, `:147-156`), and rejects invalid patches (`:299-317`).

**What the flow actually reads:**

- `settingService.get()` on mount (`packages/ui/src/hooks/useSettingService.ts:84-98`).
- Tab restore/persist: `lastWorkspaceSession ?? []`, `lastActiveTabIndex ?? 0`, `recentProjects ?? []` (`packages/ui/src/hooks/useTabPersistence.ts:177-260`, `:213-224`, `:284-302`).
- Locale: `settings.locale`, `settings.localePreference` (`packages/ui/src/i18n/IntlProvider.tsx:219-241`).
- Follow-up behavior: `nexInteractionBehavior` (`packages/ui/src/v4/composer/followupModeSettings.ts:4-9`).
- Reasoning/todo/tool grouping toggles all use `??` fallbacks (`packages/ui/src/v4/SessionPane.tsx:1221-1231`).
- Opening a folder calls `get()` then `update({ recentProjects })` (`packages/ui/src/root/useRootWorkspaceActions.ts:310-320`).

A partial object is _runtime-tolerated_ (all reads use `?.`/`??`), but the TypeScript contract declares `recentProjects` and `locale` required, and the real server always emits the full parsed default. Return the full default.

**Minimal valid default (`appSettingsSchema.parse({})`):**

```json
{
  "recentProjects": [],
  "locale": "zh-CN",
  "localePreference": "system",
  "terminalInheritSystemProfile": true,
  "embeddedBrowserAllowInsecureCertificates": false,
  "embeddedBrowserViewportPreference": {
    "mode": "normal",
    "viewport": { "width": 393, "height": 852 },
    "zoom": "fit"
  },
  "computerUseComposerEntryHidden": true,
  "taskAutoArchiveEnabled": false,
  "taskAutoArchiveOlderThanDays": 7,
  "closeToTrayOnWindows": true,
  "showDockIcon": true,
  "closeToTrayOnWindowsMigrationInitialized": true,
  "keepAwakeWhileRunning": false,
  "desktopChromiumHardwareAccelerationEnabled": true,
  "messageStreamShowReasoning": true,
  "messageStreamShowReasoningMigrationInitialized": true,
  "messageStreamShowTodos": false,
  "toolGroupingExploreEnabled": true,
  "toolGroupingTerminalEnabled": true,
  "toolGroupingChangesEnabled": false,
  "nexInteractionBehavior": "queue",
  "askUserQuestionAutoResolutionEnabled": true,
  "modelIoFullRetentionEnabled": false,
  "startPlanRecommendationDismissed": false,
  "providerFamilyConnectionSelections": {},
  "providerFamilyDomainMigrated": false,
  "nativeSearchEnhancementsEnabled": true,
  "memoryEnabled": false,
  "toolSearchEnabled": false,
  "codemodeEnabled": false,
  "lastWorkspaceSession": [],
  "lastActiveTabIndex": 0,
  "receivePreviewUpdates": false,
  "autoDownloadAndInstallUpdates": false,
  "skippedElectronUpdateVersions": {}
}
```

**Cheapest minimal that still keeps this flow working** (if you do not want to emit every default):

```json
{
  "recentProjects": [],
  "locale": "zh-CN",
  "localePreference": "system",
  "lastWorkspaceSession": [],
  "lastActiveTabIndex": 0,
  "nexInteractionBehavior": "queue",
  "messageStreamShowReasoning": true,
  "askUserQuestionAutoResolutionEnabled": true,
  "modelIoFullRetentionEnabled": false
}
```

`update` is called with small patches (`recentProjects`, locale, interaction behavior). Validate or ignore; the UI sends only known keys. If `update` rejects, the caller logs (e.g. `packages/ui/src/hooks/useTabPersistence.ts:290-297`, `packages/ui/src/i18n/IntlProvider.tsx:196-205`). No zod schema is applied by the UI to `get()`’s result.

## 2. `broadcast`

**Interface:** `packages/services/src/broadcast/broadcast.ts:9-50`

```ts
send(message: {channel: string; payload: unknown; sourceWindowId?: number}): Promise<void>
acquireClaim(key): Promise<...>; commitClaim(lease); releaseClaim(lease); tryClaim(key): Promise<boolean>
onMessage: Event<BroadcastMessage>
```

Flow usage: `send` only when settings with runtime preferences change (`packages/ui/src/hooks/useSettingService.ts:158-167`; channel `settings:app-runtime-preferences`, `packages/shared/src/app-runtime-preferences.ts:3`); `onMessage` listeners at startup (`packages/ui/src/Root.tsx:188-207`, `packages/ui/src/store/index.ts:273-296`, `packages/ui/src/i18n/IntlProvider.tsx:320`). The claim methods are not used by the UI (no hits under `packages/ui/src`).

**Minimal stub:** `send → null`; `onMessage → Event.None`; claim methods may reject `Error("unsupported")` (never called).

## 3. `window-controller`

**Interface:** `packages/services/src/window-controller/windowController.ts:52-67`

```ts
deleteArchivedTask / deleteArchivedTasks
listTaskList(params: NexTaskListQuery): Promise<{items: WindowHostControllerTaskListItem[]; total: number; hasMore: boolean}>
mutateTask({address, mutation}): Promise<NexTaskMeta | null>
subscribeControllerV4({topic, base?, visibility?}): Promise<{ack:{subscriptionId, mode:"snapshot"|"resume", logEpoch}}>
resyncControllerV4({subscriptionId, base, forceSnapshot?}): Promise<{ack}>
unsubscribeControllerV4({subscriptionId}): Promise<void>
onDynamicControllerFrame(): Event<WindowHostControllerFrame>
```

`NexTaskListQuery` = `{kind:"pinned"|"archived"|"timeline"|"active"; workspaceScopes:{workspacePath, workspaceIdentity?, workspacePurpose?}[]; sortBy:"created"|"updated"; search?; limit?}` (`packages/services/src/session/nexTaskListTypes.ts:7-20`). Item = `NexTaskMeta & {searchSnippet?, searchSnippets?}` plus controller fields `remoteSessionId?`, `sourceAvailability`, `liveStatus`, `activity?` (`packages/services/src/window-controller/windowController.ts:33-42`).

Controller schemas: address `packages/shared/src/nex-protocol-v4/controller.ts:13-33`; task row `:90-140`; task snapshot `:152-158`; frame `:205-252`; subscribe params/result `:254-269`; resync `:272-288`; gap predicate `:307-316`.

**What the sidebar renders from (detailed):**

| Surface                                                          | Row source                                                                                                                                                                                                            |
| ---------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Default “project” sidebar, per-workspace task rows               | `nex-task` tasks-index rows + `sessions-index` activity (`useWorkspaceTaskLists.ts:101-160`, `:349-430`)                                                                                                              |
| Pinned section (always rendered)                                 | `window-controller.listTaskList({kind:"pinned"})` (`WorkspacePinnedTasksSection.tsx:113-120`)                                                                                                                         |
| Timeline / archived / grouped / command center / header pin menu | `window-controller.listTaskList` (`WorkspaceTimelineTasksSection.tsx:140`, `WorkspaceArchivedTasksFlatSection.tsx:85`, `useGroupedTaskView.ts:657`, `CommandCenterDialog.tsx:471`, `WorkspaceHeaderSections.tsx:137`) |
| Sessions index                                                   | Only activity/detail join and controller frame invalidation; **not** row existence (`buildTaskListResultFromSessions.ts:1-4`)                                                                                         |

The controller registry subscribes to both topics (`controller/workspaces`, `controller/tasks-index`) on first `useGlobalTaskList` mount (`packages/ui/src/v4/windowControllerTaskListRegistry.ts:186-197`). Gap detection: snapshot frames bypass the check; delta frames require same `subscriptionId`, same `logEpoch`, and `cursor.seq === frame.fromSeq`. On gap it calls `resyncControllerV4({subscriptionId, base:{logEpoch,seq}, forceSnapshot:true})` (`:124-148`, `controller.ts:307-316`). A snapshot frame replaces all cached rows (`:158-163`). Controller frames are **not** wire-wrapped; the event payload is the frame itself (`WindowHostControllerFrame`).

`mutateTask` is **never called by the UI**. Sidebar pin/rename/archive/unread use `nexTaskService` (`packages/ui/src/WorkspaceSidebarItem.tsx:491`, `:568`, `:634`, `:672`); only desktop host code calls `mutateTask` (`packages/desktop/src/host/index.ts:1496`). Reference-service behavior: `packages/services/src/window-controller/windowHostControllerService.ts:621-647`.

**Minimal controller frame with one task** (emit from `onDynamicControllerFrame` after `subscribeControllerV4`; `sentAt` is Unix ms):

```json
{
  "topic": "controller/tasks-index",
  "subscriptionId": "ctl-sub-1",
  "logEpoch": "ctl-epoch-1",
  "fromSeq": 0,
  "toSeq": 1,
  "sentAt": 1760000000000,
  "payload": {
    "kind": "snapshot",
    "snapshot": {
      "protocolVersion": 1,
      "logEpoch": "ctl-epoch-1",
      "tasks": [
        {
          "address": { "workspacePath": "/workspace/demo", "taskId": "sess-1" },
          "meta": {
            "taskId": "sess-1",
            "traceId": "trace-sess-1",
            "title": "Hello",
            "workspacePath": "/workspace/demo",
            "createdAt": 1760000000000,
            "updatedAt": 1760000000000,
            "mode": "build"
          },
          "membership": { "pinned": false, "archived": false, "active": true },
          "sourceAvailability": "online",
          "liveStatus": "completed"
        }
      ]
    }
  }
}
```

`listTaskList` minimal result (one session):

```json
{
  "items": [
    {
      "taskId": "sess-1",
      "traceId": "trace-sess-1",
      "title": "Hello",
      "workspacePath": "/workspace/demo",
      "createdAt": 1760000000000,
      "updatedAt": 1760000000000,
      "mode": "build",
      "sourceAvailability": "online",
      "liveStatus": "completed"
    }
  ],
  "total": 1,
  "hasMore": false
}
```

For a brand-new running session use `liveStatus:"running"` and `meta.status:"running"`.

**Minimal stub:** `subscribeControllerV4 → {ack:{subscriptionId:"ctl",mode:"snapshot",logEpoch:"ctl-epoch"}}` then emit the snapshot above; `resyncControllerV4` → same ack plus snapshot; `unsubscribeControllerV4 → null`; `listTaskList` filtered by `kind`: pinned/archived empty, timeline/active include the row; `onDynamicControllerFrame → Event.None` is not enough if you want pin state, but `{items:[],total:0,hasMore:false}` is enough to avoid errors.

## 4. `model-selection` and `provider-settings`

**Interfaces:** `packages/services/src/model-provider/providerFacadeServices.ts:39-99` (provider settings), `:108-111` (model selection), descriptors `:118-120`.

```ts
IModelSelectionService { onDidChange: Event<ModelSelectionView>; getView(input?: {selection: ModelSelection|null}): Promise<ModelSelectionView> }
IProviderSettingsService { onDidChange: Event<ProviderSettingsView>; getView(): Promise<ProviderSettingsView>; ... }
```

View types: `ModelSelectionView` / `ModelSelectionProviderView` / `ModelSelectionModelView` at `packages/provider/src/facades.ts:187-206`; `ProviderSettingsView` / provider / model candidate at `packages/provider/src/facades.ts:106-176`. The real projection is `getView` at `packages/provider/src/facades.ts:522-566` and `createProviderSettingsView` at `:605-700`.

**Composer readiness:** `useDraftModelReadinessGate` treats the view as ready iff at least one provider has `models.length > 0` (`packages/ui/src/v4/composer/useDraftModelReadinessGate.ts:28-30`). It reads `getView()` and subscribes `onDidChange` (`:87-100`), and re-checks on send (`:136-166`). A read error becomes `check-failed`, which still permits agent startup (`:174-177`). Root subscribes to the same service and blocks workspace-session restore until it is hydrated; a read error shows a bottom-right toast (`packages/ui/src/Root.tsx:147-168`, `:329-333`; `packages/ui/src/hooks/useModelSelectionView.ts:100-160`).

Root also connects the provider-settings view at startup (`packages/ui/src/Root.tsx:318`; `packages/ui/src/root/useRootProviderSettingsSnapshot.ts:6-20`; `packages/ui/src/lib/providerSettingsSnapshot.ts:31-84`), but a failure is only logged. It is needed for full settings UI, not the chat path.

**Why one reasoning level is mandatory:** `createComposerSubmissionConfig` returns null unless `validateModelSelectionOptions` passes (`packages/ui/src/v4/composer/composerSubmissionConfig.ts:13-46`), and that validator rejects a selection without `options.reasoningLevel` or with a level not in `model.config.optionSpecs.reasoningLevel.values` (`packages/provider/src/registry.ts:129-160`). `completeNewModelSelection` picks `values.at(-1)` (`packages/provider/src/model-selection-config.ts:58-72`), and a new draft starts from `view.preferredSelection` only when it is valid (`packages/ui/src/lib/composerRecent.ts:101-140`). So the minimal view must include at least one reasoning level and should include a valid `preferredSelection`.

**What the UI sends back:** `ModelSelection` = `{providerId: string; modelId: string; options?: {reasoningLevel?: string}}` (strict; `packages/shared/src/model-selection.ts:4-14`). It is placed into `sendText` (`packages/ui/src/v4/SessionPane.tsx:2486-2495`, `:2517-2530`) and into `createSession.firstInput.modelSelection` / `createSession.config.modelSelection` (`packages/ui/src/v4/composer/useDraftConfigControl.ts:500-509`; schema `packages/shared/src/nex-protocol-v4/command.ts:25-35`, `:48-74`).

**Minimal `getView()` JSON that makes the composer ready and shows one model:**

```json
{
  "revision": 1,
  "providers": [
    {
      "providerId": "anthropic",
      "providerName": "Anthropic",
      "templateId": "anthropic",
      "config": {
        "group": "standard-personal",
        "access": { "type": "api-key", "apiKey": "sk-ant-placeholder" },
        "api": { "type": "anthropic-messages", "baseUrl": "https://api.anthropic.com" }
      },
      "models": [
        {
          "modelId": "claude-sonnet-4-5",
          "config": {
            "enabled": true,
            "name": "Claude Sonnet 4.5",
            "properties": {
              "requiresMfjsToolSchema": false,
              "contextWindow": 200000,
              "inputFormat": {
                "supportsText": true,
                "supportsImage": true,
                "supportsVideo": false,
                "supportsAudio": false,
                "supportsPdf": true
              },
              "outputFormat": { "supportsText": true },
              "supportsToolCall": true,
              "supportsJsonSchemaOutput": true,
              "supportsNativeWebSearch": false,
              "supportsMidConversationSystem": false
            },
            "optionSpecs": {
              "reasoningLevel": {
                "values": ["low", "medium", "high"],
                "map": "{\"reasoning_effort\": reasoningLevel}"
              },
              "maxOutputTokens": { "max": 8192, "map": "{\"max_tokens\": maxOutputTokens}" }
            }
          }
        }
      ]
    }
  ],
  "preferredSelection": {
    "providerId": "anthropic",
    "modelId": "claude-sonnet-4-5",
    "options": { "reasoningLevel": "high" }
  }
}
```

Shape constraints: `ProviderConfigObject` requires `group`, `access`, `api` (`packages/provider/src/config/provider-data-schema.ts:14-80`); the model config is the complete form from `packages/shared/src/model-config.ts:1-120`; the option maps must compile (`packages/model-option-map/src/option-maps.ts:1-40`). `providerName`, `templateId`, `config`, `models` are required on the provider view (`packages/provider/src/facades.ts:187-195`).

If you call `getView({selection})`, also include `effectiveSelection` (same shape) or `selectionIssue`; the composer uses `effectiveSelection` for the draft (`packages/ui/src/v4/composer/useDraftConfigControl.ts:140-190`). For the no-arg call at startup, `preferredSelection` is enough.

**Minimal `provider-settings.getView()` JSON** (one provider, one model):

```json
{
  "revision": 1,
  "providerTemplates": [],
  "providerOrder": [],
  "providers": [
    {
      "providerId": "anthropic",
      "providerName": "Anthropic",
      "templateId": "anthropic",
      "enabled": true,
      "executable": true,
      "effectiveConfig": {
        "group": "standard-personal",
        "access": { "type": "api-key", "apiKey": "sk-ant-placeholder" },
        "api": { "type": "anthropic-messages", "baseUrl": "https://api.anthropic.com" }
      },
      "issues": [],
      "models": [
        {
          "kind": "candidate",
          "modelId": "claude-sonnet-4-5",
          "builtin": true,
          "effectiveBuiltinConfig": {},
          "effectiveConfig": {
            "enabled": true,
            "properties": {
              "requiresMfjsToolSchema": false,
              "contextWindow": 200000,
              "inputFormat": {
                "supportsText": true,
                "supportsImage": true,
                "supportsVideo": false,
                "supportsAudio": false,
                "supportsPdf": true
              },
              "outputFormat": { "supportsText": true },
              "supportsToolCall": true,
              "supportsJsonSchemaOutput": true,
              "supportsNativeWebSearch": false,
              "supportsMidConversationSystem": false
            },
            "optionSpecs": {
              "reasoningLevel": {
                "values": ["low", "medium", "high"],
                "map": "{\"reasoning_effort\": reasoningLevel}"
              },
              "maxOutputTokens": { "max": 8192, "map": "{\"max_tokens\": maxOutputTokens}" }
            }
          },
          "enabled": true,
          "executable": true,
          "selectable": true,
          "issues": []
        }
      ]
    }
  ]
}
```

Provider-settings `onDidChange` should be an `Event.None` unless you actually mutate the registry; the settings page also refreshes on mutations. `getView` failure keeps the settings page in an error state with retry (`packages/ui/src/hooks/useProviderSettingsView.ts:84-110`).

## 5. `nex-session`

**Interface:** `packages/services/src/nex-session/nexSession.ts:135-153`. The flow uses only `readWorkspacePresentation` (`packages/ui/src/hooks/workspacePrepareRpc.ts:28-31`); `readSession` is used only when opening a selection side-chat (`packages/ui/src/v4/SessionPane.tsx:1669`) and `closeSession` only for draft skill invalidation (`packages/ui/src/lib/nexDraftSkillInvalidation.ts:34`).

```ts
readWorkspacePresentation(params: {workspacePath: string; workspaceIdentity?: string; remoteSessionId?: string}): Promise<NexWorkspacePresentation>
```

Return schema: `packages/shared/src/nex-protocol/index.ts:2007-2015`; `nexWorkspaceRefSchema` at `packages/shared/src/nex-protocol-legacy-types.ts:34-42`; mode enum `:74`.

**Minimal valid return:**

```json
{
  "workspace": { "workspacePath": "/workspace/demo", "workspaceKey": "/workspace/demo" },
  "mode": "build",
  "slashCommands": []
}
```

`workspacePath` and `workspaceKey` are required and strict; `workspaceIdentity`/`remoteSessionId` are optional. The UI converts `mode` into a synthetic `mode` config option (`packages/ui/src/hooks/workspacePrepareRpc.ts:45-60`; `packages/ui/src/lib/nexSessionProjection.ts:142-155`) and passes `slashCommands` straight through. Failure is caught during draft catalog hydration, logs, and sets `configOptionsStatus:"error"`; the composer still renders (`packages/ui/src/v4/composer/useDraftConfigControl.ts:371-412`).

## 6. `nex-agent` (v4 subset)

**Interface:** `packages/services/src/nex-agent/nexAgent.ts:573-869`; workspace target `{workspacePath, workspaceIdentity?, remoteSessionId?}` at `packages/services/src/nex-agent/nexAgentPluginParams.ts:8-13`.

### 6.1 Every method the UI calls in this flow

| Method                                                                                                         | Called from                                                                       | Matters for MVP                                                                                                                  |
| -------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| `helloConversationV4()`                                                                                        | `packages/ui/src/v4/agentV4ConnectionHandshake.ts:16-30`                          | **Must**. Return strict `HelloMessage` (`transport.ts:41-66`).                                                                   |
| `initializeConversationV4(clientHello)`                                                                        | same, `:32-52`                                                                    | **Must** (can accept and return null).                                                                                           |
| `subscribeConversationV4({workspacePath,workspaceIdentity?,sessionId,base?,visibility?})`                      | `agentConversationTransport.ts:167-176`                                           | **Must**. Ack-only; initial frame is a notification.                                                                             |
| `resyncConversationV4({workspacePath,subscriptionId,base,forceSnapshot?})`                                     | `agentConversationTransport.ts:256-263`                                           | **Must** for gap/fault recovery.                                                                                                 |
| `unsubscribeConversationV4({workspacePath,workspaceIdentity?,subscriptionId})`                                 | `agentConversationTransport.ts:281-285`                                           | **Must**.                                                                                                                        |
| `onDynamicConversationFrame(workspace)`                                                                        | `agentConversationTransport.ts:495`                                               | **Must**.                                                                                                                        |
| `sendConversationCommandV4({workspacePath,envelope})`                                                          | `agentConversationTransport.ts:325`                                               | **Must** (`createSession`, `sendText`, `deleteSession`, `renameSession`, `stop`, `resolveInteraction`).                          |
| `conversationRowsRangeV4({workspacePath,sessionId,beforeRowId?,limit})`                                        | `agentConversationTransport.ts:337-342`                                           | Optional but cheap; automatic only when an incomplete leading turn is detected (`SessionPane.tsx:3379-3397`, store `:987-1040`). |
| `conversationPlansV4({workspacePath,sessionId})`                                                               | `agentConversationTransport.ts:346-350`                                           | Called unconditionally after a session snapshot (`SessionPane.tsx:2171-2175`). Stub `{plans:[],atSeq,atLogEpoch}`.               |
| `queryConversationCommandsV4({workspacePath,commands})`                                                        | `agentConversationTransport.ts:330-333`; `usePendingCommandRecovery.ts:40-50`     | Only when the pending ledger is non-empty on going live. Stub `unknown`.                                                         |
| `onDynamicSessionsIndexFrame(workspace)`                                                                       | `agentSessionsIndexTransport.ts:187-190`                                          | **Must**.                                                                                                                        |
| `subscribeSessionsIndexV4({workspacePath,workspaceIdentity?,runtimePolicy:"existing-only",base?,visibility?})` | `agentSessionsIndexTransport.ts:121-129`                                          | **Must**.                                                                                                                        |
| `resyncSessionsIndexV4({workspacePath,subscriptionId,base,forceSnapshot?})`                                    | `agentSessionsIndexTransport.ts:165-172`                                          | **Must** for gaps.                                                                                                               |
| `unsubscribeSessionsIndexV4({workspacePath,subscriptionId})`                                                   | `agentSessionsIndexTransport.ts:179-184`                                          | **Must**.                                                                                                                        |
| `onAgentRuntimeRestarted(listener)`                                                                            | `agentConversationTransport.ts:529`, `agentSessionsIndexTransport.ts:204`         | Should return `Event.None`.                                                                                                      |
| `onAgentRuntimeLifecycle(listener)`                                                                            | `agentConversationTransport.ts:546-558`, `agentSessionsIndexTransport.ts:220-240` | Optional but proxy makes it truthy; must be listenable.                                                                          |
| `syncAppRuntimePreferences({askUserQuestionAutoResolutionEnabled,modelIoFullRetentionEnabled?})`               | `packages/ui/src/Root.tsx:214-236`; `useSettingService.ts:158-162`                | **Must** (void).                                                                                                                 |

Interaction-only, not needed for the happy path: `getSkillReferenceCatalog` (`useSkills.ts:94`, only when `/` or skill mention is open), `listSessionSubagents` (subagent directory), `readSessionDebug`, `conversationFileChangesV4`, `attachmentBegin/Chunk/Commit/Abort/PreviewSource/Read`, `conversationWorkflowRun*`, `conversationAttachmentRead/Stat`. `onDynamicCuaPermissionObservation` is mounted unconditionally but guarded by `platform.openCuaPermissionOnboarding`, which the web platform does not provide (`packages/ui/src/cua-permission/CuaPermissionObservationAttachment.tsx:112-133`; `packages/rpc/src/proxy-channel.ts:115-140` makes the method always truthy). `onDynamicLocalTtftFacts` and `onDynamicConversationTelemetryFact` are not called by the UI.

### 6.2 Wire envelope

`V4_WIRE_PROTOCOL_VERSION = 3` (`packages/shared/src/nex-protocol-v4/core.ts:7`). Envelope type at `wire.ts:19-30`; validation `:84-150`.

```json
{
  "wireVersion": 3,
  "kind": "complete",
  "deliveryKind": "initial",
  "logicalFrameId": "sub-1-lf-1",
  "logicalFrameOrdinal": 1,
  "topic": "conversation/sess-1",
  "subscriptionId": "sub-1",
  "frame": {
    "topic": "conversation/sess-1",
    "subscriptionId": "sub-1",
    "fromSeq": 0,
    "toSeq": 5,
    "sentAt": 1760000000000,
    "payload": { "kind": "snapshot", "snapshot": {} }
  }
}
```

- `deliveryKind` is `"initial" | "online" | "recovery"` (`wire.ts:16`). Publisher-authoritative; do not guess from RPC timing.
- `logicalFrameOrdinal` is a per-route positive integer. The assembler drops lower ordinals and faults on same-ordinal/different-id (`wire-assembler.ts:192-240`). The real publisher starts at 1 and increments (`conversation-topic-publisher.ts:778`, `:1071`).
- For MVP send only `kind:"complete"` frames (no fragments). Max complete JSON frame is 1 MiB (`core.ts` `PROTOCOL_V4_LIMITS.maxFrameBytes`).
- Conversation topic is `conversation/<sessionId>` (`transport.ts:1143-1156`); sessions index topic is `sessions-index/<workspaceId>` where the UI uses `workspaceIdentity?.trim() || workspacePath` (`agentSessionsIndexTransport.ts:89`; `sessions-index.ts:76-84`).

### 6.3 Subscribe ACK and initial snapshot ordering

`v4/conversation/subscribe` result is strictly `{ack}` (`transport.ts:439-447`); `ack` is `{subscriptionId, mode:"snapshot"|"resume", logEpoch, openTiming?}` (`transport.ts:121-156`). The initial snapshot is emitted **after** the response as an owned notification (`transport.ts:439-441`). A pre-ACK notification is also tolerated: the transport stages physical frames per topic until the store records the ACK and calls `activate` (`packages/ui/src/v4/ackActivationBarrier.ts:1-140`; `agentConversationTransport.ts:174-199`; store `connect` at `conversationProjectionStore.ts:455-467`).

The first frame must have `deliveryKind:"initial"` to consume `awaitingInitial`; otherwise a later delta can trigger recovery. Duplicate/early frames are dropped by ordinal and `toSeq <= current.seq` rules.

### 6.4 `ConversationSnapshot` (full field list)

Schema: `packages/shared/src/nex-protocol-v4/snapshot.ts:470-509`.

```json
{
  "protocolVersion": 1,
  "sessionId": "sess-1",
  "logEpoch": "epoch-1",
  "seq": 5,
  "revision": 7,
  "control": {
    "phase": "running",
    "sessionEnded": false,
    "canStop": true,
    "stopState": "stoppable",
    "stopTargetKind": "assistant",
    "activeWorks": [],
    "lastError": null,
    "apiRetry": null
  },
  "availability": {
    "fork": { "allowed": true },
    "compact": { "allowed": true },
    "switchModelConfig": { "allowed": true },
    "setFollowupMode": { "allowed": true },
    "queueEdit": { "allowed": true },
    "sendQueuedNow": { "allowed": true },
    "pauseGoal": { "allowed": true },
    "resumeGoal": { "allowed": true }
  },
  "inputRouting": { "mode": "startNow" },
  "meta": { "title": "Hello", "titleSource": "default" },
  "config": {
    "provider": "anthropic",
    "model": "claude-sonnet-4-5",
    "thought": "high",
    "thoughtLevels": ["low", "medium", "high"],
    "followupMode": "queue",
    "mode": "build"
  },
  "modelTransition": null,
  "usage": {
    "contextWindow": null,
    "cumulative": {
      "inputTokens": 0,
      "outputTokens": 0,
      "cacheReadTokens": 0,
      "cacheWriteTokens": 0
    }
  },
  "queue": { "items": [], "autoDrain": true },
  "pendingInteractions": [],
  "pendingCommands": [],
  "backgroundWorks": [],
  "goal": null,
  "plan": null,
  "rows": {
    "window": [
      {
        "rowId": 1,
        "turnId": "turn-1",
        "kind": "turnHeader",
        "origin": "userInput",
        "executionKind": "agent",
        "state": "running",
        "startedAt": 1760000000000,
        "createdAt": 1760000000000,
        "createdAtSeq": 1
      },
      {
        "rowId": 2,
        "turnId": "turn-1",
        "kind": "userInput",
        "text": "hi",
        "origin": "realUser",
        "sourceCommandId": "cmd-1",
        "createdAt": 1760000000000,
        "createdAtSeq": 2
      },
      {
        "rowId": 3,
        "turnId": "turn-1",
        "kind": "assistantText",
        "assistantResponseId": "resp-1",
        "text": "Hello!",
        "state": "streaming",
        "model": "claude-sonnet-4-5",
        "createdAt": 1760000000000,
        "createdAtSeq": 3
      }
    ],
    "totalCount": 3,
    "firstRowId": 1
  }
}
```

Key sub-shapes:

- `control`: `phase` ∈ draft/prewarming/running/completedSuccess/completedInterrupted/error; `lastError` nullable; `apiRetry` nullable (`snapshot.ts:24-33`, `:128-141`).
- `availability`: eight actions, each `{allowed:true}` or `{allowed:false,reasonCode}` (`snapshot.ts:143-170`).
- `inputRouting.mode` ∈ startNow/enqueue/guide/reject/choice (`:172-183`).
- `meta` has defaults so it may be omitted, but new snapshots should include it (`:186-191`).
- `config.provider/model/thought/followupMode` are required; `thoughtLevels` and `mode` have defaults (`session-config.ts:5-33`).
- `usage`, `queue`, `goal`, `plan`, `rows` are required; `subagents`, `workflowRuns`, `workspaceHookAdmission`, `sharedContextImport` are optional (`snapshot.ts:470-509`).
- `rows.firstRowId` should equal the first window row id (or `null` if empty) so `hasOlderRows` is false (`conversationProjectionStore.ts:227-232`). `totalCount` is the full row count.

**Row variants** (`packages/shared/src/nex-protocol-v4/rows.ts`): base requires `rowId`, `turnId`, `createdAt`, `createdAtSeq`; `entityId`/`productTurnId`/`visibility`/`actions` optional (`:7-31`). Union kinds at `:420-432`: turnHeader, userInput, assistantText, reasoning, toolCall, artifact, subagent, hookInvocation, timelineMarker.

```json
{
  "rowId": 4,
  "turnId": "turn-1",
  "kind": "reasoning",
  "assistantResponseId": "resp-1",
  "text": "Thinking about it…",
  "state": "complete",
  "createdAt": 1760000000001,
  "createdAtSeq": 4
}
```

```json
{
  "rowId": 5,
  "turnId": "turn-1",
  "kind": "toolCall",
  "assistantResponseId": "resp-1",
  "toolCallId": "tool-1",
  "toolName": "Bash",
  "status": "success",
  "inputText": "{\"command\":\"ls\"}",
  "input": { "command": "ls" },
  "output": { "text": "a.txt\n" },
  "outputPreview": {
    "text": "a.txt",
    "fullText": "a.txt",
    "totalLines": 1,
    "totalBytes": 6,
    "linesEstimated": false
  },
  "startedAt": 1760000000002,
  "endedAt": 1760000000003,
  "createdAt": 1760000000002,
  "createdAtSeq": 5
}
```

Read/Edit/Write cards are selected by tool name, not by `display`: `Read` → `file-read` family, `Write`/`Edit`/`ApplyPatch` → `file-write`, `Bash` → `shell` (`packages/shared/src/tool-identity.ts:1-70`; `packages/ui/src/ToolCallBlocks/resolveRenderer.ts:150-190`). Minimal `Read` row: `toolName:"Read"`, `input:{"file_path":"/workspace/demo/a.txt"}`, `output:{"text":"..."}`. Minimal `Edit`/`Write` row: `toolName:"Edit"` or `"Write"`, `input:{"file_path":"...","old_string":"...","new_string":"..."}` or `{"file_path":"...","content":"..."}`, `output:{"text":"..."}`.

**Bash/Read/Edit/Write `display`:** there is **no** `bash`, `read`, `edit`, or `write` variant in `toolCallDisplaySchema`. Its variants are `node_repl_images`, `task_output`, `respond_to_coordinator`, `mcp_tool`, and workflow displays (`packages/shared/src/nex-protocol-v4/toolDisplay.ts:251-263`). `bash_output` and `file_diff` are variants of **`output.display`** (`toolResultDisplaySchema`, `toolDisplay.ts:28-140`), and are optional. For MVP omit `row.display` entirely; the renderers read `row.input`, `row.output.text`, and `row.outputPreview` (`packages/ui/src/ToolCallBlocks/renderers/execute.tsx:271-290`; adapter `packages/ui/src/v4/toolCallRowAdapter.ts:93-131`). If a Bash card needs structured output, use `output.display = {"kind":"bash_output","output":"...","truncated":false}` (`packages/shared/src/bash-output-display.ts:1-11`).

**Delta frames** (optional in MVP): `conversationDeltaSchema` supports `row.appended`, `row.upserted`, `row.removed {fromRowId}`, `row.delta {rowId,path,append}` where path ∈ text/inputText/output.text/summaryText, `state.updated {patch}`, `workflowRun.updated`, `workflowRun.removed` (`packages/shared/src/nex-protocol-v4/delta.ts:94-170`; paths `core.ts:17-18`). The state patch keys are the top-level snapshot keys (`delta.ts:39-66`).

Example streamed reply using deltas:

```json
{
  "wireVersion": 3,
  "kind": "complete",
  "deliveryKind": "online",
  "logicalFrameId": "sub-1-lf-2",
  "logicalFrameOrdinal": 2,
  "topic": "conversation/sess-1",
  "subscriptionId": "sub-1",
  "frame": {
    "topic": "conversation/sess-1",
    "subscriptionId": "sub-1",
    "fromSeq": 5,
    "toSeq": 6,
    "sentAt": 1760000000100,
    "payload": {
      "kind": "deltas",
      "deltas": [{ "op": "row.delta", "rowId": 3, "path": "text", "append": " How can I help?" }]
    }
  }
}
```

### 6.5 seq / logEpoch / revision semantics the UI enforces

- `logEpoch` identifies an append-log generation. A new epoch means the old `seq` is meaningless; the client must resync and accept a snapshot (`conversationProjectionStore.ts:602-646`, `sessionsIndexStore.ts:340-392`).
- `seq` is the watermark of the applied interval. Snapshot frames must have `fromSeq:0`; the snapshot’s `snapshot.seq` should equal frame `toSeq` (the publisher does this: `apps/nex-cli/packages/bootstrap/src/nex-protocol-v4/conversation-topic-publisher.ts:428-429`). The store sets `seq = snapshot.seq`, then requires every delta frame’s `fromSeq === current.seq`; after applying it sets `seq = toSeq` (`conversationProjectionStore.ts:694-730`).
- Duplicate/stale: `toSeq <= current.seq` is silently dropped (`:706-712`).
- Gap: `frame.fromSeq !== current.seq` → if this was the resume frame of a fresh subscribe, reconnect with `forceSnapshot:true`; otherwise same-sub `resyncConversationV4` (`:712-721`). Sessions index does the same (`sessionsIndexStore.ts:375-392`).
- A delta before any applied base triggers recovery, even if its numeric `fromSeq` happens to match (`conversationProjectionStore.ts:627-632`; sessions index `:366-371`).
- `revision` is the session content revision used as the CAS base (`baseRevision`) for row-targeting/model/mode commands (`command.ts:365-400`). The UI does not gap-check revision; it passes it as `baseRevision` for commands that require it and reads `revisionAtDecision` from the ACK.
- `subscribeAck.mode`: `"resume"` is only valid if the client sent a `base` with the same `logEpoch`; the store then marks `subscriptionHasAppliedBase=true` (`conversationProjectionStore.ts:460-466`). For MVP always answer `"snapshot"` and send a full initial snapshot; this is always safe.
- If you send a full snapshot on every change, use `deliveryKind:"online"` after the first and keep `fromSeq:0`, `toSeq` increasing. The store replaces state without a gap check (`:648-706`). Do not reuse `logicalFrameOrdinal` for a different frame.

### 6.6 Commands used by this flow

Envelope: `{ttft?, commandId, clientId, sessionId: string|null, baseRevision?, baseLogEpoch?, type, payload, issuedAt}` (`command.ts:323-336`). ACK: `{memoryEnabled?, commandId, status:"accepted"|"rejected"|"stale"|"duplicate"|"noop"|"failed", reasonCode?, message?, revisionAtDecision, result?}` (`command.ts:430-441`).

- `createSession`: payload `{workspaceId, firstInput?:{text,attachments?,modelSelection?,mode?,planEnabled?}, config?:{modelSelection?,provider?,model?,thought?,followupMode?,mode?,planEnabled?}, mcpServers?, offPeakToolEnabled?, dynamicWorkflowEnabled?}` (`command.ts:48-74`). The mock must answer `status:"accepted"` and `result:{type:"createSession",sessionId}`; the UI throws otherwise (`packages/ui/src/v4/SessionPane.tsx:2450-2464`, `:2510-2525`). Draft prewarm sends `createSession` even before the user types (`packages/ui/src/v4/composer/useDraftSessionPrewarm.ts:60-140`).
- `sendText`: payload `{text,attachments?,requestedDelivery?,modelSelection?,mode?,planEnabled?,...}` (`command.ts:86-165`). Answer `accepted` with `result:{type:"inputAccepted",delivery:"startNow",inputId}` (the UI only checks `status`). Send the streamed rows after the ACK (or before; the store was subscribed when the pane opened).
- `deleteSession`: payload `{}`, used only to clean up unused draft prewarm sessions (`useDraftSessionPrewarm.ts:66-82`). Answer `accepted` or `noop`.
- `renameSession`: `{title}`; used by sidebar/header rename (`WorkspaceSidebarItem.tsx:491`, `WorkspaceHeaderSections.tsx:263`).
- `stop`: `{expectedForegroundExecutionId?}`; `resolveInteraction`: `{interactionId,answer}`; both can be accepted noops for MVP.

The UI creates a prewarm `createSession` while the composer is idle. Because the draft pane has `sessionId:null`, it does **not** subscribe to the prewarm session until the first send promotes it (`SessionPane.tsx:2532-2540`). Your server must therefore accept a `sendText` for a session the UI has not yet subscribed to, and emit the rows when the subscription arrives.

### 6.7 Sessions index

Schema: `packages/shared/src/nex-protocol-v4/sessions-index.ts:29-74`. Subscribe params from the UI: `{workspacePath, workspaceIdentity?, runtimePolicy:"existing-only", base?, visibility?}` (`agentSessionsIndexTransport.ts:121-129`); result is strict `{ack}` (`transport.ts:450-455`). The initial snapshot is a notification after the ACK, same rules as conversation.

```json
{
  "wireVersion": 3,
  "kind": "complete",
  "deliveryKind": "initial",
  "logicalFrameId": "si-sub-1-lf-1",
  "logicalFrameOrdinal": 1,
  "topic": "sessions-index//workspace/demo",
  "subscriptionId": "si-sub-1",
  "frame": {
    "topic": "sessions-index//workspace/demo",
    "subscriptionId": "si-sub-1",
    "fromSeq": 0,
    "toSeq": 3,
    "sentAt": 1760000000000,
    "payload": {
      "kind": "snapshot",
      "snapshot": {
        "protocolVersion": 1,
        "workspaceId": "/workspace/demo",
        "logEpoch": "si-epoch-1",
        "sessions": [
          {
            "sessionId": "sess-1",
            "workspaceId": "/workspace/demo",
            "title": "Hello",
            "titleSource": "generated",
            "phase": "completedSuccess",
            "sessionEnded": true,
            "hasBackgroundWork": false,
            "lastActivityAt": 1760000000000,
            "createdAt": 1760000000000
          }
        ]
      }
    }
  }
}
```

Required `SessionSummary` fields: `sessionId`, `workspaceId`, `title`, `phase`, `sessionEnded`, `hasBackgroundWork`, `lastActivityAt`, `createdAt`; `titleSource`, `pendingInteraction*`, `goalStatus`, `lastAssistantPreview`, `workflowActivity`, `parentSessionId` are optional (`sessions-index.ts:29-57`). `phase` ∈ draft/prewarming/running/completedSuccess/completedInterrupted/error (`snapshot.ts:24-33`). Deltas are `session.upserted`/`session.removed` (`sessions-index.ts:68-74`), but full snapshots on every change are legal and simpler (`packages/ui/src/v4/sessionsIndexStore.ts:94-131`).

`session.removed` only removes live detail; the persisted sidebar row is removed by tasks-index tombstones (`mapSessionSummaryToTaskMeta.ts:1-4`; `buildTaskListResultFromSessions.ts:169-195`). So implement both.

## 7. `file` (minimal)

**Interface:** `packages/services/src/file/file.ts:21-82`. Calls in this flow:

- `ensureConversationWorkspace(): Promise<{path, created, workspacePurpose:"conversation"}>` — called on startup when no workspace tab is restored (`packages/ui/src/Root.tsx:585-612`; fallback workspace creation); failure shows the workspace error banner. Impl `packages/services/src/file/fileService.ts:439-464`.
- `readdir({path, includeHidden?}): Promise<FileEntry[]>` — only when the directory browser opens (web picker). `DirectoryBrowser` first calls `systemService.info()` for `homedir`, then `readdir` (`packages/ui/src/DirectoryBrowser.tsx:79-110`). `FileEntry` = `{name,path,type:"file"|"directory",isSymbolicLink?}` (`packages/shared/src/protocol.ts:11-18`).
- `searchWorkspaceFiles({rootPath, workspaceIdentity?, query, limit?, refresh?}): Promise<WorkspaceFileEntry[]>` — only when the `@` file mention menu is opened (`packages/ui/src/mentions/providers/fileMentionProvider.ts:59-90`; params `file.ts:12-19`; entry `{name,path,relativePath,type}` at `protocol.ts:62-68`).
- Workspace file tree uses `readdir`; command center uses `listWorkspaceFilesLength`/`listWorkspaceFilesRange` (`packages/ui/src/workspace-file-search/fetchWorkspaceFileEntries.ts:10-30`) — not needed in the minimal flow unless those panes are opened.

**Minimal returns:**

```json
{ "path": "/workspace/conversation", "created": true, "workspacePurpose": "conversation" }
```

```json
[]
```

```json
[]
```

`ensureConversationWorkspace` must return a path the server can actually use as a workspace; the UI treats it as a local workspace tab (`handleSelectConversationWorkspace`).

## 8. Workspace identity and picking

**Web bootstrap:** `GET /api/server-info` (`packages/server/src/http.ts:300`, shape `packages/shared/src/server-remote.ts:11-27`) is read only for `workspaces[0]`; its `path` and optional `workspaceIdentity` become `initialWorkspaceAbsPath`/`initialWorkspaceIdentity` (`packages/web/src/main.tsx:313-340`, `:452-461`). The web client sets `preferDirectoryBrowser` (`main.tsx:464`), so “Open folder” uses the server-side `DirectoryBrowser` (`packages/ui/src/root/useRootWorkspaceActions.ts:355-390`), which browses the **connected server’s** filesystem.

- With one advertised workspace, the user is taken straight into it.
- With multiple advertised workspaces, only index 0 is auto-opened. The others are reachable only through the folder picker (and thus must be present on the connected server). There is no UI that enumerates `server-info.workspaces`.
- Least-invasive multi-runner design: run **one new server per runner**, each advertising one workspace path (or distinct virtual paths routed by one server inside the same process). Do not use one server to advertise several machines’ paths unless all those paths are visible on that server.

**`workspaceIdentity` vs `workspacePath`:** the UI’s workspace key is `identity?.trim() || path` (`packages/shared/src/task-realtime-core.ts:78-83`; `packages/ui/src/lib/taskQueryCache.ts:40-48`). But `workspaceIdentity` has a second, load-bearing meaning: any non-empty identity marks the tab as remote in `useWorkspaceServicesResolution` (`packages/ui/src/hooks/useWorkspaceServices.tsx:90-100`). With no registered `remoteSessionId`, `resolveWorkspaceServices` returns null and the hook returns a disconnected proxy (`:47-70`, `:178-207`; `packages/ui/src/lib/workspaceServiceResolver.ts:31-72`). The task-list shard is skipped entirely (`packages/ui/src/hooks/useWorkspaceTaskLists.ts:322-343`).

`parseRemoteWorkspaceIdentity` only accepts `remote:ssh:…`, `remote:wsl:…`, `remote:docker:…` (`packages/shared/src/remote-workspace-identity.ts:1-112`). A custom `runner:<id>:<path>` returns null, while services that use `isRemoteWorkspaceIdentity` would treat it as local (`packages/services/src/nex-agent/nexAgentService.ts:1635-1645`, `:2455-2462`; `packages/services/src/session/taskIndexRepo.ts:189-202`). The result is inconsistent: UI thinks remote, host thinks local.

**Recommendation:** advertise `{path:"/workspace/demo"}` with **no** `workspaceIdentity`. Use distinct absolute paths (or virtual mount paths) per runner and let the server route internally. If you must use multiple machines, either give each runner its own URL/server or implement the full remote-session registration (`bindRemoteWorkspaceIdentity`, `sessionsById`) — not part of the MVP.

Opening a folder does not set an identity: `addTab(path)` then `startDraftInWorkspace(path)` (`packages/ui/src/root/useRootWorkspaceActions.ts:292-300`); `addTab` only passes `workspaceIdentity` when explicitly supplied (`packages/ui/src/store/tabStore.ts:257-281`).

## 9. Other channels called unconditionally and their severity

| Channel                                                                                                                                                              | Call site                                                                                                                                                                                             | Severity if missing                                                                           | Cheapest stub                                                                                                                                                                                                                                                                                                                                                       |
| -------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `nex-task`                                                                                                                                                           | `useWorkspaceTaskLists` membership (`packages/ui/src/lib/taskListMembershipSets.ts:197-262`; `nexTaskService.ts:379-460`), plus `onDynamicWorkspaceEvent` listen (`useWorkspaceTaskLists.ts:673-716`) | **High**: project sidebar stays empty; no session row                                         | `listTasks`/`listPinnedTasks` → one meta for the session (pinned returns []), `listArchivedTasks` → [], `listPinnedTaskIds` → [], `listDeletedTaskIds` → [], `onDynamicWorkspaceEvent` → `Event.None`; optionally `listTaskList`                                                                                                                                    |
| `window-controller`                                                                                                                                                  | pinned section at startup; timeline/archived/grouped/header menu                                                                                                                                      | Medium: pinned list stays empty, console errors                                               | `listTaskList → {items:[],total:0,hasMore:false}` (or the one-item result), subscribe ack + empty snapshot, `onDynamicControllerFrame → Event.None`, resync ack, unsubscribe void                                                                                                                                                                                   |
| `system`                                                                                                                                                             | `systemService.info()` in DirectoryBrowser (`DirectoryBrowser.tsx:85-90`), `useWorkspaceHomePath` (`SessionPane.tsx:522`; `useWorkspaceHomePath.ts:50-90`), header path                               | Low: folder picker cannot start; home-path display only                                       | `info → {"homedir":"/workspace","platform":"linux"}`; `listIntegratedTerminalShells → []`; `probeIntranet` may reject                                                                                                                                                                                                                                               |
| `git`                                                                                                                                                                | `useGitRepository` unconditionally in `App` (`App.tsx:407-426`; `useGitRepository.ts:463-522`)                                                                                                        | Low: Git pane error only, no toast                                                            | `refresh → {summary:{workspacePath:"…",repoRoot:"…",workspaceInRepoPath:"",autoRefreshWatchPaths:[],branchName:null,trackingBranchName:null,headRefType:"branch",ahead:0,behind:0,isDirty:false,isGitAvailable:false,isRepository:false},identity:null,unstagedChanges:[],stagedChanges:[],branchComparison:null}` (`packages/shared/src/git.ts:37-51`, `:250-256`) |
| `file-watcher`                                                                                                                                                       | `useGitAutoRefresh` only when `gitSummary.isGitAvailable && isRepository` (`useGitAutoRefresh.ts:55-120`)                                                                                             | None with the git stub above                                                                  | not needed                                                                                                                                                                                                                                                                                                                                                          |
| `mcp-sync`                                                                                                                                                           | `useEnsureWorkspaceMcpLoaded` on workspace ready (`App.tsx:790`; `mcpStore.ts:270-345`)                                                                                                               | Low: logged warning, MCP list empty                                                           | `loadMcpFromUserDirectory → {servers:[]}`, `listWorkspaceMcpServerStatuses → {servers:[]}`-shaped empty result                                                                                                                                                                                                                                                      |
| `coding-plan-subscription`                                                                                                                                           | `useDynamicWorkflowAvailabilityLoader` in Root (`Root.tsx:143`; `dynamicWorkflowAvailabilityStore.ts:49-66`)                                                                                          | Low: logged warning, feature disabled                                                         | `getDynamicWorkflowClientConfig → {"mode":"disabled","enabled":false,"source":"default"}` (`packages/shared/src/dynamic-workflow-feature.ts:44-49`)                                                                                                                                                                                                                 |
| `settings-sync`                                                                                                                                                      | `OnboardingDialog` mounted always; `useSettingsSync` runs on first workspace (`Root.tsx:799`; `useSettingsSync.ts:236-292`)                                                                           | **Medium-high**: missing channel opens the onboarding dialog with an error on first workspace | `getFirstRunPromptState → {"handled":true}` (then it returns early and never opens) (`settingsSyncService.ts:2387-2392`); `getClaudeAgentsFileMigrationStatus` may return `{supported:false}`                                                                                                                                                                       |
| `subagents`                                                                                                                                                          | `useSubagents` in the slash plugin (`SlashCommandPlugin.tsx:77-93`; `useSubagents.ts:86-102`)                                                                                                         | Low-medium: `@` menu shows a load error                                                       | `list → {"agents":[],"userAgents":[],"pluginAgents":[],"capability":{"userScopeAvailable":false}}` (`packages/shared/src/subagents-types.ts:77-88`)                                                                                                                                                                                                                 |
| `client-scenes`                                                                                                                                                      | suggested prompts in draft (`SessionPane.tsx:4176-4195`; `useClientScenesResource.ts:63-95`)                                                                                                          | Low: logged warning, empty suggestions                                                        | `list → {"code":0,"msg":"","data":[]}` (`packages/services/src/client-scenes/clientScenes.ts:4-16`)                                                                                                                                                                                                                                                                 |
| `cua-permission` / CUA observation                                                                                                                                   | guard in `CuaPermissionObservationAttachment.tsx:112-133`                                                                                                                                             | None on web (`platform.openCuaPermissionOnboarding` absent)                                   | not needed                                                                                                                                                                                                                                                                                                                                                          |
| `pets`                                                                                                                                                               | `PetRuntime` only when `settings.pet.enabled` (`PetRuntime.tsx:101-105`)                                                                                                                              | None with default settings                                                                    | not needed                                                                                                                                                                                                                                                                                                                                                          |
| `usage-stats`, `skills`, `commands`, `hooks`, `memory`, `plugins`, `feedback`, `onboarding-record`, `client-config`, `conversation-share`, `client-scenes` elsewhere | settings pages / dedicated UI                                                                                                                                                                         | None in this flow                                                                             | not needed                                                                                                                                                                                                                                                                                                                                                          |
| `broadcast`                                                                                                                                                          | settings change + startup listeners                                                                                                                                                                   | Low                                                                                           | `send` void, `onMessage` Event.None                                                                                                                                                                                                                                                                                                                                 |

`onDynamicWorkspaceEvent` is an `onDynamic*` method: the UI calls `nexTaskService.onDynamicWorkspaceEvent({workspacePath,workspaceIdentity?})` and expects an Event (`packages/services/src/session/nexTaskService.ts:736-742`; called at `useWorkspaceTaskLists.ts:673-686`). Returning `Event.None` is fine; the server does not need to emit task-changed events if lists are refreshed from index frames.

## 10. Minimum server behavior checklist

1. Register `setting`, `broadcast`, `window-controller`, `model-selection`, `provider-settings`, `nex-agent`, `nex-session`, `file`, **`nex-task`**, plus `system`, `git`, `mcp-sync`, `coding-plan-subscription`, `settings-sync`, `subagents`, `client-scenes`.
2. For every channel, unknown event listens must return an empty event (or be omitted only if the UI never listens). Unknown channel promises should reject after 1 s; unknown channel event listens are already silently dropped by the router.
3. `helloConversationV4` must return `kind:"hello"`, `protocolVersion:3`, `clientMode:"desktop-continuous"`, `deliveryProfile:"continuous"`, `serverTime`, `connectionId`, `capabilities:{nativeDialogs:false,localTerminal:false,binaryFrames:false,compression:"none",independentPlanState:true,workflowRunDeltas:false}`, `auth:{}`. The UI parses this strictly (`packages/ui/src/v4/agentV4ConnectionHandshake.ts:16-30`; schema `transport.ts:41-66`).
4. `createSession` ACK returns a `sessionId`; then sessions-index gets an upsert and `nex-task.listTasks` includes the same session. `sendText` ACK follows, then conversation snapshot/deltas.
5. Subscribe responses are ACK-only; initial snapshots are notifications with `deliveryKind:"initial"` and the same `subscriptionId`.
6. Keep `snapshot.seq == frame.toSeq`; use a new `logicalFrameOrdinal` per emitted frame; keep `logicalFrameId` stable for fragments only.
7. Emit a controller snapshot for `controller/tasks-index` and (optionally) `controller/workspaces` after each controller subscribe, and resync.
8. Return `getFirstRunPromptState {handled:true}` so onboarding never opens; return the default AppSettings; return a ready model view with one reasoning level.

## Appendix: exact `nex-agent` interface citations

- `helloConversationV4` `:737`; `initializeConversationV4` `:739`; `setConnectionFlowStateV4` `:741`; `subscribeConversationV4` `:742`; `resyncConversationV4` `:745`; `unsubscribeConversationV4` `:748`; `conversationRowsRangeV4` `:750`; `conversationPlansV4` `:753`; `sendConversationCommandV4` `:793`; `queryConversationCommandsV4` `:794`; `onDynamicConversationFrame` `:814`; `subscribeSessionsIndexV4` `:827`; `resyncSessionsIndexV4` `:830`; `unsubscribeSessionsIndexV4` `:833`; `onDynamicSessionsIndexFrame` `:835`; `onAgentRuntimeRestarted` `:849`; `onAgentRuntimeLifecycle` `:855`; `syncAppRuntimePreferences` `:586` (all in `packages/services/src/nex-agent/nexAgent.ts`).
- Params: session subscribe `:357-362`; unsubscribe/resync `:363-374`; rows range `:376-382`; command `:454-458`; sessions-index subscribe `:519-534`; app runtime preferences `:555-558`.
- UI transport call sites: `packages/ui/src/v4/agentConversationTransport.ts:167`, `:256`, `:281`, `:325`, `:337`, `:346`, `:495`; `packages/ui/src/v4/agentSessionsIndexTransport.ts:121`, `:165`, `:179`, `:187`.
