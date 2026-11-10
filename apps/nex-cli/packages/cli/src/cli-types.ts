import type { TuiReadClipboardImage, TuiWriteClipboardText } from "@nex/tui";
import type { UiLocale } from "@nex/i18n";
import type { Logger } from "@nex/contracts";
import type {
  createManagedCdpBrowserRuntime,
  ManagedCdpBrowserRuntimeOptions,
} from "@nex/adapters/browser";
import type {
  createDefaultProviderEndpointRoutingPort,
  createModelAdapter,
  createNexApp,
  CreateDefaultProviderEndpointRoutingPortOptions,
  CreateModelAdapterOptions,
  configureCodingPlanApiKey,
  ConfigureCodingPlanApiKeyOptions,
  inspectNexSkill,
  inspectWorkspaceHookTrust,
  grantWorkspaceHookTrust,
  revokeWorkspaceHookTrustCli,
  inspectNexCustomCommand,
  InspectNexCustomCommandOptions,
  InspectNexSkillOptions,
  loginNexCli,
  loginBigmodelCodingPlan,
  LoginBigmodelCodingPlanOptions,
  LoginNexCliOptions,
  listNexCustomCommands,
  ListNexCustomCommandsOptions,
  loadNexCustomCommand,
  listNexSessions,
  listNexSkills,
  ListNexSessionsOptions,
  ListNexSkillsOptions,
  logoutNexCli,
  LogoutNexCliOptions,
  resolveLatestSession,
  ResolveLatestSessionOptions,
  RunNexProtocolAgentOptions,
  prepareNexTelemetryEnv,
  startProcessProviderRegistryRuntime,
  shutdownNexTelemetry,
  NexAppOptions,
} from "@nex/bootstrap";
import type { CliEnv, DotenvLoadResult, LoadCliDotenvOptions } from "./env.js";
import type { PluginsCommandOverrides } from "./plugins-command.js";
import type { CliShutdownProcess } from "./shutdown.js";
import type { resolveWorkspaceGitBranch } from "./tui-workspace-git.js";

export type BootstrapModule = typeof import("@nex/bootstrap");

export interface RunDependencies extends PluginsCommandOverrides {
  migrateStorage?: () => Promise<import("@nex/shared/sqlite-migrations").MigrationResult>;
  protocolLifecycle?: RunNexProtocolAgentOptions["lifecycle"];
  protocolInput?: NodeJS.ReadableStream;
  createManagedCdpBrowserRuntime?: (
    options?: ManagedCdpBrowserRuntimeOptions,
  ) => ReturnType<typeof createManagedCdpBrowserRuntime>;
  createModelAdapter?: (
    options?: CreateModelAdapterOptions,
  ) => ReturnType<typeof createModelAdapter>;
  createProviderEndpointRoutingPort?: (
    options: CreateDefaultProviderEndpointRoutingPortOptions,
  ) => ReturnType<typeof createDefaultProviderEndpointRoutingPort>;
  createNexApp?: (
    options?: NexAppOptions,
  ) => Awaited<ReturnType<typeof createNexApp>> | ReturnType<typeof createNexApp>;
  /**
   * Session-event shaper for --output-format stream-json. Defaults to the
   * bootstrap module's, which is also what the protocol server uses; injectable
   * so a caller that supplies its own `createNexApp` (tests, embedders) can
   * still stream, since the bootstrap module is not loaded on that path.
   */
  mapSessionEvent?: BootstrapModule["mapSessionEvent"];
  cwd?: () => string;
  env?: CliEnv;
  inspectSkill?: (options: InspectNexSkillOptions) => ReturnType<typeof inspectNexSkill>;
  inspectWorkspaceHookTrust?: typeof inspectWorkspaceHookTrust;
  grantWorkspaceHookTrust?: typeof grantWorkspaceHookTrust;
  revokeWorkspaceHookTrustCli?: typeof revokeWorkspaceHookTrustCli;
  inspectCustomCommand?: (
    options: InspectNexCustomCommandOptions,
  ) => ReturnType<typeof inspectNexCustomCommand>;
  loginNexCli?: (options?: LoginNexCliOptions) => ReturnType<typeof loginNexCli>;
  loginBigmodelCodingPlan?: (
    options?: LoginBigmodelCodingPlanOptions,
  ) => ReturnType<typeof loginBigmodelCodingPlan>;
  configureCodingPlanApiKey?: (
    options: ConfigureCodingPlanApiKeyOptions,
  ) => ReturnType<typeof configureCodingPlanApiKey>;
  loadDotenv?: (options?: LoadCliDotenvOptions) => DotenvLoadResult;
  prepareNexTelemetryEnv?: typeof prepareNexTelemetryEnv;
  projectConfigPath?: string;
  listSessions?: (options: ListNexSessionsOptions) => ReturnType<typeof listNexSessions>;
  listCustomCommands?: (
    options: ListNexCustomCommandsOptions,
  ) => ReturnType<typeof listNexCustomCommands>;
  loadCustomCommand?: (
    options: InspectNexCustomCommandOptions,
  ) => ReturnType<typeof loadNexCustomCommand>;
  // headless slash 路由要和 app facade 的保留名 gate 用同一个判据；默认取 bootstrap 的，
  // 注入点只为让单测不必拉起整个 bootstrap 模块。见 prompt-command.ts。
  isReservedSlashCommandName?: BootstrapModule["isReservedNexSlashCommandName"];
  listSkills?: (options: ListNexSkillsOptions) => ReturnType<typeof listNexSkills>;
  logger?: Logger;
  readClipboardImage?: TuiReadClipboardImage;
  writeClipboardText?: TuiWriteClipboardText;
  resolveLatestSession?: (
    options: ResolveLatestSessionOptions,
  ) => ReturnType<typeof resolveLatestSession>;
  resolveWorkspaceGitBranch?: typeof resolveWorkspaceGitBranch;
  logoutNexCli?: (options?: LogoutNexCliOptions) => ReturnType<typeof logoutNexCli>;
  runNexProtocolAgent?: (options?: RunNexProtocolAgentOptions) => Promise<void>;
  runTui?: typeof import("@nex/tui").runTui;
  skipUserConfig?: boolean;
  userConfigPath?: string;
  exitProcess?: (code: number) => void;
  shutdownCleanupTimeoutMs?: number;
  shutdownProcess?: CliShutdownProcess;
  startProcessProviderRegistryRuntime?: typeof startProcessProviderRegistryRuntime;
  shutdownNexTelemetry?: typeof shutdownNexTelemetry;
}

export type CliPermissionMode = "build" | "plan" | "edit" | "yolo" | "guarded";
export type CliRuntimeMode = CliPermissionMode | "auto";

export interface CliModeState {
  current?: CliRuntimeMode;
  override?: CliPermissionMode;
}

export interface CliTargetRequest {
  objective: string;
  replaceExisting: boolean;
}

export type ModeCapableApp = Awaited<ReturnType<typeof createNexApp>> & {
  getMode?: () => CliRuntimeMode;
  setLocale?: (locale: UiLocale) => Promise<{ locale: "en-US" | "zh-CN" }>;
  setMode?: (mode: CliRuntimeMode) => Promise<{ mode: CliRuntimeMode }>;
};

export interface CliResumeRequest {
  continueSession: boolean;
  resumeSessionId?: string;
}
