// Package setting ports packages/services/src/setting/* (step 3.5):
// ISettingService over ~/.nex/v2/setting.json, with the TS's write queue
// semantics (settingsWriteQueue.ts), merge-patch normalization
// (normalizeSettingsPatch.ts) and the legacy account-connection migration
// (legacyAccountConnectionSettings.ts).
//
// 状态所有者：setting.json（路径规则来自 services/paths）。Node 与 Go 在过渡期
// 可能同时写同一文件 —— 两侧都用同目录 lock 文件 + temp/rename 原子写
// （fsx.AtomicWriteText 与 TS atomicFileUtils.ts 同一套 <path>.lock 约定），
// 读侧以完整 schema 校验后的合并结果为准；损坏文件按 TS 语义隔离备份并回退默认值。
package setting

// ChannelName is ServiceChannels.Setting.
const ChannelName = "setting"

// MAX_RECENT_PROJECTS and DEFAULT_PROJECT_NAME are the TS consts.
const (
	MaxRecentProjects  = 10
	DefaultProjectName = "NexProject"
)

// SettingsParseRetry timing, verbatim from settingService.ts.
const (
	SettingsParseRetryDelayMS = 300
	SettingsParseRetryCount   = 3
)

// SettingsWriteQueueTimeoutMS is SETTINGS_WRITE_QUEUE_TIMEOUT_MS. The env
// override (NEX_SETTING_WRITE_QUEUE_TIMEOUT_MS) is read by the constructor
// once — same as the TS's per-call read, but snapshot per service so tests are
// deterministic. No new env var is introduced (D7).
const SettingsWriteQueueTimeoutMS = 30_000

// SettingWriteQueueTimeoutEnvVar is the pre-existing TS override variable.
const SettingWriteQueueTimeoutEnvVar = "NEX_SETTING_WRITE_QUEUE_TIMEOUT_MS"

// Field order of the parsed settings object. The TS zod object emits keys in
// schema order (probed: appSettingsSchema.parse({}) key order), and
// setting.json bytes are diffed by the differential harness, so the Go port
// writes the same order.
//
// Note: this is the order of the SCHEMA's declared members, which is the order
// parse output uses regardless of input order (JS object spread preserves
// insertion order, but the final appSettingsSchema.parse re-normalizes).
var settingsFieldOrder = []string{
	"recentProjects",
	"locale",
	"shortcutBindings",
	"localePreference",
	"terminalInheritSystemProfile",
	"terminalFontFamily",
	"integratedTerminalShell",
	"httpProxy",
	"httpProxyNoProxy",
	"httpProxyCaCertPath",
	"embeddedBrowserAllowInsecureCertificates",
	"embeddedBrowserViewportPreference",
	"computerUseComposerEntryHidden",
	"taskAutoArchiveEnabled",
	"taskAutoArchiveOlderThanDays",
	"closeToTrayOnWindows",
	"showDockIcon",
	"closeToTrayOnWindowsMigrationInitialized",
	"keepAwakeWhileRunning",
	"desktopZoomLevel",
	"desktopWindowSize",
	"desktopChromiumHardwareAccelerationEnabled",
	"messageStreamShowReasoning",
	"messageStreamShowReasoningMigrationInitialized",
	"messageStreamShowTodos",
	"toolGroupingExploreEnabled",
	"toolGroupingTerminalEnabled",
	"toolGroupingChangesEnabled",
	"nexInteractionBehavior",
	"askUserQuestionAutoResolutionEnabled",
	"modelIoFullRetentionEnabled",
	"startPlanRecommendationDismissed",
	"providerFamilyConnectionSelections",
	"providerFamilyDomain",
	"providerFamilyDomainUpdatedAt",
	"providerFamilyDomainMigrated",
	"nativeSearchEnhancementsEnabled",
	"onboardingOccupation",
	"proactiveSuggestionsEnabled",
	"memoryEnabled",
	"toolSearchEnabled",
	"codemodeEnabled",
	"lastWorkspaceSession",
	"lastActiveTabIndex",
	"lastActiveTaskByWorkspace",
	"dataBaseDir",
	"pendingPostUpdateReleaseNotes",
	"receivePreviewUpdates",
	"autoDownloadAndInstallUpdates",
	"skippedElectronUpdateVersions",
	"settingsSyncFirstRunPromptHandled",
	"nexEndpointOrigin",
	"pet",
}
