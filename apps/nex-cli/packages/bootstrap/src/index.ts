// Bootstrap public API surface.

export * from "./app/create-app.js";
export type {
  ListNexSessionsOptions,
  PromptInput,
  ResolveLatestSessionOptions,
  ResumeOptions,
  RunNexProtocolAgentOptions,
  SendInputOptions,
  SendInputResult,
  SetLocaleResult,
  SteerTurnOptions,
  SubmitPromptOptions,
  UserPromptInput,
  NexApp,
  NexAppOptions,
  NexModelOption,
} from "./app/types.js";
export * from "./auth-login.js";
export {
  inspectNexCustomCommand,
  listNexCustomCommands,
  loadNexCustomCommand,
} from "./custom-commands.js";
export type {
  InspectNexCustomCommandOptions,
  ListNexCustomCommandsOptions,
  NexCustomCommandInspection,
} from "./custom-commands.js";
export { createModelAdapter } from "./model-factory.js";
export type { CreateModelAdapterOptions } from "./model-factory.js";
export {
  createDefaultProviderEndpointRoutingPort,
  resolveProcessProviderEndpointRoutingPort,
} from "./provider-endpoint-routing.js";
export type {
  CreateDefaultProviderEndpointRoutingPortOptions,
  ResolveProcessProviderEndpointRoutingPortOptions,
} from "./provider-endpoint-routing.js";
export { startProcessProviderRegistryRuntime } from "./app/process-provider-registry-runtime.js";
export type { ProcessProviderRegistryRuntimeOptions } from "./app/process-provider-registry-runtime.js";
export {
  addNexPluginMarketplace,
  getNexPluginsOverview,
  installNexMarketplacePlugin,
  listNexPlugins,
  removeNexPluginMarketplace,
  resolveNexPlugins,
  setNexPluginEnabled,
  uninstallNexMarketplacePlugin,
  updateNexMarketplacePlugin,
  updateNexPluginMarketplace,
  validateNexPluginPath,
} from "./plugins.js";
export type {
  AddNexMarketplaceOptions,
  InstallNexMarketplacePluginOptions,
  ListNexPluginsOptions,
  RemoveNexMarketplaceOptions,
  ResolveNexPluginsOptions,
  SetNexPluginEnabledOptions,
  SetNexPluginEnabledResult,
  UninstallNexMarketplacePluginOptions,
  UpdateNexMarketplaceOptions,
  UpdateNexMarketplacePluginOptions,
  ValidateNexPluginPathOptions,
  NexAvailablePluginData,
  NexInstalledPluginData,
  NexMarketplaceSummaryData,
  NexMarketplaceUpdateData,
  NexPluginInstallData,
  NexPluginUpdateData,
  NexPluginsOverviewData,
} from "./plugins.js";
export { runNexProtocolAgent } from "./nex-protocol-entrypoint.js";
// Exposed for the CLI's --output-format stream-json: it needs the same event
// shape the protocol server emits, rather than inventing a second one.
export { mapSessionEvent } from "./nex-protocol/session-mapper.js";
export type { SessionTranscriptMessage, SessionTranscriptPart } from "./session-transcript.js";
export { listNexSessions, resolveLatestSession } from "./sessions.js";
export { inspectNexSkill, listNexSkills } from "./skills.js";
export type {
  InspectNexSkillOptions,
  ListNexSkillsOptions,
  NexSkillInspection,
} from "./skills.js";
// Exposed for the CLI's headless slash routing: it must decide "is this a real
// custom command?" with the *same* reserved-name gate the app facade's
// customCommandPromptResolver applies, or the two disagree and a reserved name
// reaches the model as literal prompt text. See prompt-command.ts.
export { isReservedNexSlashCommandName } from "./slash-command-surface.js";
export {
  grantWorkspaceHookTrust,
  inspectWorkspaceHookTrust,
  revokeWorkspaceHookTrustCli,
} from "./workspace-hook-trust-cli.js";
export type {
  WorkspaceHookTrustCliItem,
  WorkspaceHookTrustCliStatus,
  WorkspaceHookTrustCliTarget,
} from "./workspace-hook-trust-cli.js";

// 嵌入式调用复用产品请求装配，避免来源头与网络配置漂移。
export { createRuntimeAiSdkModelExecutionConfig } from "./model-config.js";
