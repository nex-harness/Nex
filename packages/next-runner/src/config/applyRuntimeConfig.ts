import { mkdir, rename, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { ModelRuntime, SettingsManager } from "@earendil-works/pi-coding-agent";
import {
  type AuthOperationOptions,
  type Credential,
  type CredentialInfo,
  type CredentialStore,
} from "@earendil-works/pi-ai";
import type { Logger } from "../logger.js";
import { materializeBundles, type MaterializedBundles } from "./bundles.js";
import { createServerRuntime } from "./credentials.js";
import { appliedCacheFor, pickSettings, type AppliedCache } from "./runtimeSettings.js";
import { modelsJsonFor } from "./modelsJson.js";
import {
  buildRuntimeConfig,
  ConfigRejectedError,
  validateFrame,
  type ConfigFrame,
  type ConfigIssue,
  type RuntimeConfig,
} from "./runtimeConfig.js";

/**
 * Applies one `config` version to pi (spec docs/next/config-mcp-skills.md
 * decisions 4-5, research §C4).
 *
 * - Server mode: a derived models.json under `<data>/derived/<version>/` plus an
 *   in-memory credential store (`setRuntimeApiKey`); the key is also exported to
 *   the process env under a per-provider name so pi's own tool subprocesses
 *   (bash) see it, and models.json references it as `$NAME` — the file on disk
 *   never carries a secret. Settings come from `SettingsManager.inMemory`
 *   seeded from the doc's settings layer and pinned to the workspace cwd.
 * - Fallback mode: no providers in the doc → the runner keeps today's behavior
 *   (`ModelRuntime.create()` reads `~/.pi/agent`, settings from the user's
 *   files).
 *
 * A new RuntimeConfig is built for every version; the engine hands it only to
 * NEW sessions, so running sessions keep the ModelRuntime and settings manager
 * they were created with. Sessions holding an older generation stay valid until
 * they close (their provider env vars are dropped only once no live generation
 * references them).
 */

/** One MCP server's live state as reported to the server (P1). */
export interface McpServerStatus {
  name: string;
  transport: "stdio" | "http";
  state: "connecting" | "connected" | "error" | "disabled";
  toolCount: number;
  toolNames?: string[];
  error?: string;
  failureKind?: string;
}

/** Stores the per-server statuses of the newest session's MCP extension. */
export class McpStatusRegistry {
  private statuses = new Map<string, McpServerStatus>();
  private listener: ((statuses: McpServerStatus[]) => void) | undefined;

  /** Replaces the status set (called by an engine reporting session status). */
  replace(statuses: McpServerStatus[]): void {
    this.statuses = new Map(statuses.map((status) => [status.name, status]));
    this.listener?.([...this.statuses.values()]);
  }

  list(): McpServerStatus[] {
    return [...this.statuses.values()];
  }

  onChange(listener: ((statuses: McpServerStatus[]) => void) | undefined): void {
    this.listener = listener;
  }
}

/**
 * The runner's MCP observation of passing sessions: what the live sessions were
 * opened with, and that at least one answered with the server doc's own list.
 * It looks at the sessions only (never at the generation), so it never swaps a
 * running session's model runtime or skills behind its back.
 */
export interface McpLiveSession {
  workspacePath: string;
  sessionId: string;
  generation: number;
  sample: () => void;
}

export interface RuntimeConfigManagerOptions {
  dataDir: string;
  logger: Logger;
  /** Directory used for the fallback `SettingsManager.create(cwd)`. */
  fallbackCwd?: string;
  /** Test seam: build a runtime for either mode. */
  createRuntime?: (config: RuntimeConfig, modelsPath: string) => Promise<ModelRuntime>;
}

export interface AppliedResult {
  version: number;
  ok: boolean;
  errors: ConfigIssue[];
  facts: Record<string, unknown>;
}

interface Generation {
  config: RuntimeConfig;
  runtime: ModelRuntime | null;
  settings: SettingsManager;
  modelsPath?: string;
  /** Materialized skill/prompt bundle directories (P2). */
  resources: MaterializedBundles;
  disposed: boolean;
}

const NO_RESOURCES: MaterializedBundles = Object.freeze({
  skillPaths: Object.freeze([]),
  promptPaths: Object.freeze([]),
  issues: Object.freeze([]),
});

/**
 * Credential store that never writes auth.json (research §C4). Reads the user's
 * own auth.json once at construction (a fresh object, not the live store), so
 * subscription logins in ~/.pi/agent keep working in server-owned mode without
 * the runner ever writing them.
 */
export class MemoryCredentialStore implements CredentialStore {
  private readonly entries: Map<string, Credential>;

  constructor(entries: ReadonlyMap<string, Credential> = new Map()) {
    this.entries = new Map(entries);
  }

  set(providerId: string, credential: Credential | undefined): void {
    if (credential) this.entries.set(providerId, credential);
    else this.entries.delete(providerId);
  }

  has(providerId: string): boolean {
    return this.entries.has(providerId);
  }

  async read(providerId: string, options?: AuthOperationOptions): Promise<Credential | undefined> {
    options?.signal?.throwIfAborted();
    return this.entries.get(providerId);
  }

  async list(options?: AuthOperationOptions): Promise<readonly CredentialInfo[]> {
    options?.signal?.throwIfAborted();
    return [...this.entries.entries()].map(([providerId, credential]) => ({
      providerId,
      type: credential.type,
    }));
  }

  async modify(
    providerId: string,
    fn: (current: Credential | undefined) => Promise<Credential | undefined>,
    options?: AuthOperationOptions,
  ): Promise<Credential | undefined> {
    options?.signal?.throwIfAborted();
    const next = await fn(this.entries.get(providerId));
    if (next === undefined) return this.entries.get(providerId);
    this.entries.set(providerId, next);
    return next;
  }

  async delete(providerId: string, options?: AuthOperationOptions): Promise<void> {
    options?.signal?.throwIfAborted();
    this.entries.delete(providerId);
  }
}

export class RuntimeConfigManager {
  private generation: Generation;
  /** Older generations still referenced by open sessions (by version). */
  private readonly retained = new Map<number, Generation>();
  private readonly exports = new Set<string>();
  private applied: AppliedResult | null = null;
  private lastAppliedVersion: number;
  /** Live MCP statuses of the newest session (reported by the engine). */
  readonly mcpStatus = new McpStatusRegistry();
  /**
   * P1: newest session whose MCP statuses are live (set by the engine when a
   * session reports). `sample()` re-reads it; `generation` is the applied
   * config version it was created with.
   */
  latestMcpSession: McpLiveSession | undefined;
  /** How every session of this process was opened (P1). */

  constructor(private readonly options: RuntimeConfigManagerOptions) {
    this.lastAppliedVersion = 0;

    const fallback = emptyFallbackConfig();
    this.generation = {
      config: fallback,
      runtime: null,
      settings: SettingsManager.create(options.fallbackCwd ?? process.cwd()),
      resources: NO_RESOURCES,
      disposed: false,
    };
  }

  /** Version this runner reports as applied (0 before the first frame). */
  get appliedVersion(): number {
    return this.applied?.version ?? this.lastAppliedVersion;
  }

  get current(): Generation {
    return this.generation;
  }

  /** Materialized skill/prompt directories of the latest generation. */
  resources(): MaterializedBundles {
    return this.generation.resources;
  }

  get appliedResult(): AppliedResult | null {
    return this.applied;
  }

  /** The latest generation's runtime (fallback: the user's ~/.pi/agent runtime). */
  runtime(): ModelRuntime | null {
    return this.generation.runtime;
  }

  settingsManager(): SettingsManager {
    return this.generation.settings;
  }

  /**
   * Applies one frame. Invalid envelopes are `ok:false` and keep the previous
   * generation; per-provider problems are reported in `errors` with `ok:true`.
   */
  async apply(frame: unknown): Promise<AppliedResult> {
    let parsed: ConfigFrame;
    try {
      parsed = validateFrame(frame);
    } catch (error) {
      const issues =
        error instanceof ConfigRejectedError
          ? error.issues
          : [{ scope: "frame", path: "", code: "config-invalid", message: String(error) }];
      this.options.logger.warn("config rejected", {
        error: error instanceof Error ? error.message : String(error),
      });
      return { version: this.lastAppliedVersion, ok: false, errors: issues, facts: this.facts() };
    }
    const config = buildRuntimeConfig(parsed, this.options.logger);

    const resources = await materializeBundles(this.options.dataDir, config.bundles);

    if (resources.issues.length > 0) {
      this.options.logger.warn("config bundles rejected", { issues: resources.issues.length });
    }
    let next: Generation;
    try {
      next = await this.build(config, resources);
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      this.options.logger.error("config generation could not be built", {
        version: config.version,
        error: message,
      });
      return {
        version: this.lastAppliedVersion,
        ok: false,
        errors: [{ scope: "runtime", path: "", code: "runtime-build-failed", message }],
        facts: this.facts(),
      };
    }
    this.swap(next);
    this.applied = {
      version: config.version,
      ok: true,
      errors: [...config.issues, ...resources.issues],
      facts: this.facts(),
    };
    this.lastAppliedVersion = config.version;
    await this.writeAppliedCache(config);
    this.options.logger.info("runtime config applied", {
      version: config.version,
      mode: config.mode,
      providers: config.providers.length,
    });
    return this.applied;
  }

  /** Facts sent with `configApplied` (models actually available + auth status). */
  facts(): Record<string, unknown> {
    const generation = this.generation;
    const config = generation.config;
    const modelsByProvider: Record<string, unknown> = {};
    for (const provider of config.providers) {
      // An enabled, keyed provider must actually exist in the runtime; a silent
      // registration failure becomes a fact the UI surfaces instead of a
      // mysterious empty catalog.
      const registered =
        generation.runtime === null ||
        provider.models.length === 0 ||
        generation.runtime.getModel(provider.id, provider.models[0]?.id ?? "") !== undefined;
      modelsByProvider[provider.id] = {
        configured: provider.configured,
        ...(provider.error ? { error: provider.error } : {}),
        ...(provider.configured && !registered ? { error: "provider-not-registered" } : {}),
        models: provider.models.map((model) => ({
          providerId: provider.id,
          modelId: model.id,
          name: model.name ?? model.id,
          api: provider.api,
          baseUrl: provider.baseUrl,
          reasoningLevels: model.reasoningLevels ?? ["off"],
          contextWindow: model.contextWindow ?? 128_000,
          maxTokens: model.maxTokens ?? 16_384,
          input: model.input ?? ["text"],
        })),
      };
    }
    return {
      mode: config.mode,
      version: config.version,
      modelsByProvider,
      mcpOwned: config.mcp.owned,
      mcp: mcpFacts(config),
      skills: config.skills.map((ref) => ref.name),
      prompts: config.prompts.map((ref) => ref.name),
      skillsEnabled: config.skillsEnabled,
      ...(config.warnings.length > 0 ? { warnings: [...config.warnings] } : {}),
    };
  }

  /** The user's local ~/.pi/agent runtime (fallback mode only). */
  async ensureFallbackRuntime(create: () => Promise<ModelRuntime>): Promise<ModelRuntime> {
    if (this.generation.runtime) return this.generation.runtime;
    const runtime = await create();
    this.generation.runtime = runtime;
    return runtime;
  }

  async dispose(): Promise<void> {
    this.generation.disposed = true;
    this.retained.clear();
    for (const key of this.exports) delete process.env[key];
    this.exports.clear();
  }

  private swap(next: Generation): void {
    const previous = this.generation;
    // Open sessions keep the generation object they were created with. This map
    // keeps the newest older server generation discoverable (diagnostics) and
    // drops the rest once it is superseded twice.
    if (previous.config.mode === "server") this.retained.set(previous.config.version, previous);
    // Provider env vars are per runner process, not per generation: the newest
    // values win, so a running session on an older version keeps its keys.
    this.generation = next;
    this.publishSecrets(next.config);
    const versions = [...this.retained.keys()].sort((left, right) => right - left);
    for (const version of versions.slice(1)) this.retained.delete(version);
  }

  private async build(config: RuntimeConfig, resources: MaterializedBundles): Promise<Generation> {
    if (config.mode === "fallback") {
      const runtime = this.options.createRuntime
        ? await this.options.createRuntime(config, "")
        : await ModelRuntime.create();
      // Fallback keeps today's behavior: the user's own settings files, with the
      // cwd the runner process was started in (sessions pass the workspace to
      // pi's resource loader separately).
      return {
        config,
        runtime,
        settings: SettingsManager.create(this.options.fallbackCwd ?? process.cwd()),
        resources,
        disposed: false,
      };
    }
    const modelsPath = join(this.options.dataDir, "derived", String(config.version), "models.json");
    await mkdir(join(this.options.dataDir, "derived", String(config.version)), { recursive: true });
    await writeFileAtomic(modelsPath, JSON.stringify(modelsJsonFor(config), null, 2) + "\n");
    const runtime = this.options.createRuntime
      ? await this.options.createRuntime(config, modelsPath)
      : await createServerRuntime(config, modelsPath);
    const settings = SettingsManager.inMemory(pickSettings(config.settings), {
      projectTrusted: true,
    });
    return { config, runtime, settings, modelsPath, resources, disposed: false };
  }

  /** Publishes the generation's keys to the env (models.json `$NAME` refs). */
  private publishSecrets(config: RuntimeConfig): void {
    for (const [name, value] of Object.entries(config.env)) {
      process.env[name] = value;
      this.exports.add(name);
    }
  }

  private async writeAppliedCache(config: RuntimeConfig): Promise<void> {
    const cache: AppliedCache = appliedCacheFor(config);
    const path = join(this.options.dataDir, "applied-config.json");
    await mkdir(this.options.dataDir, { recursive: true });
    await writeFileAtomic(path, JSON.stringify(cache, null, 2) + "\n");
  }
}

/** facts.mcp: live statuses overlaid on the doc's servers (disabled ones included). */
function mcpFacts(config: RuntimeConfig): McpServerStatus[] {
  const live = new Map(config.mcp.servers.map((server) => [server.name, server]));
  return config.mcp.servers.map((server) => {
    const transport = "url" in server.config ? ("http" as const) : ("stdio" as const);
    void live;
    return {
      name: server.name,
      transport,
      state: "connecting" as const,
      toolCount: 0,
    };
  });
}

function emptyFallbackConfig(): RuntimeConfig {
  return Object.freeze({
    version: 0,
    owned: false,
    mode: "fallback" as const,
    providers: Object.freeze([]),
    settings: Object.freeze({}),
    credentials: new Map(),
    issues: Object.freeze([]),
    env: Object.freeze({}),
    mcp: Object.freeze({
      owned: false,
      servers: Object.freeze([]),
      issues: Object.freeze([]),
      key: "",
    }),
    bundles: Object.freeze([]),
    skills: Object.freeze([]),
    prompts: Object.freeze([]),
    skillsEnabled: true,
    warnings: Object.freeze([]),
  });
}

async function writeFileAtomic(path: string, content: string): Promise<void> {
  const temp = `${path}.${process.pid}.${Date.now()}.tmp`;
  try {
    await writeFile(temp, content, { mode: 0o600 });
    await rename(temp, path);
  } catch (error) {
    await rm(temp, { force: true });
    throw error;
  }
}

export type { RuntimeConfig } from "./runtimeTypes.js";
