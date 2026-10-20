package local

// defaultSettingsJSON is appSettingsSchema.parse({}) from
// packages/shared/src/validationAppSettings.ts:433-507, as reported by
// docs/next/channels-research.md §1.
//
// Keep this as the single source of defaults: Settings.Get merges persisted
// values over it, so a later pass can add a default without touching the
// persistence code. recentProjects is handled separately (see Settings.Get).
const defaultSettingsJSON = `{
  "recentProjects": [],
  "locale": "zh-CN",
  "localePreference": "system",
  "terminalInheritSystemProfile": true,
  "embeddedBrowserAllowInsecureCertificates": false,
  "embeddedBrowserViewportPreference": { "mode": "normal", "viewport": { "width": 393, "height": 852 }, "zoom": "fit" },
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
}`
