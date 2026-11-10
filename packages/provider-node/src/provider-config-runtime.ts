import {
  ProviderConfigService,
  type ProviderConfigLayerSnapshot,
  type ProviderConfigLayerUpdate,
} from "@nex/provider";
import { NodeNexBuiltinProviderConfigSource } from "./nex-builtin-provider-config-source.js";
import {
  EndpointScopedNexBuiltinSource,
  type EndpointScopedNexBuiltinSourceOptions,
} from "./endpoint-scoped-nex-builtin-source.js";
import {
  NexBuiltinRemoteSynchronizer,
  type NexBuiltinRemoteSynchronizerOptions,
  type NexBuiltinRefreshResult,
} from "./nex-builtin-remote-synchronizer.js";
import {
  NodePersonalProviderConfigRepository,
  type PersonalProviderConfigRecoveryEvent,
} from "./personal-provider-config-repository.js";

export interface NodeProviderConfigRuntimeOptions {
  readonly nexBuiltinFilePath: string;
  readonly nexBuiltinActiveFilePath?: string;
  readonly nexBuiltinRemote?: Omit<NexBuiltinRemoteSynchronizerOptions, "source">;
  readonly nexBuiltinEnvironment?: Omit<
    EndpointScopedNexBuiltinSourceOptions,
    "bundledFilePath"
  >;
  readonly onNexBuiltinRefreshError?: (error: unknown) => void;
  readonly onPersonalConfigRecovery?: (event: PersonalProviderConfigRecoveryEvent) => void;
  readonly onPersonalConfigPollingError?: (error: unknown) => void;
  readonly personalFilePath: string;
  readonly personalPollingIntervalMs?: number | false;
  readonly importLegacy?: (
    nexBuiltin: ProviderConfigLayerSnapshot,
  ) => Promise<ProviderConfigLayerUpdate | null>;
  readonly watch?: boolean;
}

/** 组装一个 Node.js 进程内共享的 Nex Built-in/Personal Config 运行边界。 */
export class NodeProviderConfigRuntime {
  readonly configService: ProviderConfigService;
  readonly #nexBuiltinSource:
    | NodeNexBuiltinProviderConfigSource
    | EndpointScopedNexBuiltinSource;
  readonly #personalRepository: NodePersonalProviderConfigRepository;
  readonly #remoteSynchronizer?: NexBuiltinRemoteSynchronizer;
  readonly #onRemoteRefreshError?: (error: unknown) => void;
  #startPromise: Promise<void> | null = null;
  #disposed = false;
  readonly #checkListeners = new Set<() => Promise<void>>();
  #checkTimer: ReturnType<typeof setInterval> | null = null;
  #checkInFlight: Promise<void> | null = null;

  constructor(options: NodeProviderConfigRuntimeOptions) {
    this.#nexBuiltinSource = options.nexBuiltinEnvironment
      ? new EndpointScopedNexBuiltinSource({
          bundledFilePath: options.nexBuiltinFilePath,
          ...options.nexBuiltinEnvironment,
        })
      : new NodeNexBuiltinProviderConfigSource({
          bundledFilePath: options.nexBuiltinFilePath,
          activeFilePath: options.nexBuiltinActiveFilePath,
          watch: options.watch,
        });
    this.#remoteSynchronizer =
      options.nexBuiltinRemote &&
      this.#nexBuiltinSource instanceof NodeNexBuiltinProviderConfigSource
        ? new NexBuiltinRemoteSynchronizer({
            source: this.#nexBuiltinSource,
            ...options.nexBuiltinRemote,
          })
        : undefined;
    this.#onRemoteRefreshError = options.onNexBuiltinRefreshError;
    this.#personalRepository = new NodePersonalProviderConfigRepository({
      filePath: options.personalFilePath,
      onRecovery: options.onPersonalConfigRecovery,
      onPollingError: options.onPersonalConfigPollingError,
      pollingIntervalMs: options.personalPollingIntervalMs,
      ...(options.importLegacy
        ? {
            importLegacy: async () => options.importLegacy!(await this.#nexBuiltinSource.read()),
          }
        : {}),
    });
    this.configService = new ProviderConfigService({
      nexBuiltinSource: this.#nexBuiltinSource,
      personalRepository: this.#personalRepository,
    });
  }

  resolveNexBuiltinActiveFilePath(): Promise<string> {
    return this.#nexBuiltinSource instanceof NodeNexBuiltinProviderConfigSource
      ? Promise.resolve(this.#nexBuiltinSource.activeFilePath)
      : this.#nexBuiltinSource.resolveActiveFilePath();
  }

  get personalRepository(): import("@nex/provider").PersonalProviderConfigRepository {
    return this.#personalRepository;
  }

  /** Environment 同一周期检查中恢复未对齐依赖，不被下载 TTL 或失败挡住。 */
  onDidCheckNexBuiltin(listener: () => Promise<void>): () => void {
    this.#checkListeners.add(listener);
    return () => this.#checkListeners.delete(listener);
  }

  start(): Promise<void> {
    if (this.#disposed) throw new Error("NodeProviderConfigRuntime 已 dispose");
    if (this.#startPromise) return this.#startPromise;
    const startPromise = this.configService.read().then(() => {
      if (this.#disposed) return;
      void this.#checkBackground();
      // Managed Worker 无下载配置也无恢复 owner，不建立周期任务。
      if (
        this.#remoteSynchronizer ||
        this.#nexBuiltinSource instanceof EndpointScopedNexBuiltinSource ||
        this.#checkListeners.size > 0
      ) {
        this.#checkTimer = setInterval(() => {
          void this.#checkBackground();
        }, 60_000);
        this.#checkTimer.unref?.();
      }
    });
    this.#startPromise = startPromise;
    void startPromise.catch(() => {
      if (this.#startPromise === startPromise) this.#startPromise = null;
    });
    return startPromise;
  }

  refreshNexBuiltin(options?: { readonly force?: boolean }): Promise<NexBuiltinRefreshResult> {
    if (this.#disposed) return Promise.resolve("disposed");
    if (this.#nexBuiltinSource instanceof EndpointScopedNexBuiltinSource) {
      return this.#nexBuiltinSource.refresh(options);
    }
    return this.#remoteSynchronizer?.refresh(options) ?? Promise.resolve("skipped");
  }

  #checkBackground(): Promise<void> {
    if (this.#disposed) return Promise.resolve();
    if (this.#checkInFlight) return this.#checkInFlight;
    const check = Promise.allSettled([
      this.refreshNexBuiltin(),
      ...[...this.#checkListeners].map((listener) => Promise.resolve().then(listener)),
    ])
      .then((results) => {
        if (this.#disposed) return;
        for (const result of results)
          if (result.status === "rejected") this.#onRemoteRefreshError?.(result.reason);
      })
      .finally(() => {
        if (this.#checkInFlight === check) this.#checkInFlight = null;
      });
    this.#checkInFlight = check;
    return check;
  }

  dispose(): void {
    if (this.#disposed) return;
    this.#disposed = true;
    if (this.#checkTimer) clearInterval(this.#checkTimer);
    this.#checkTimer = null;
    this.#checkListeners.clear();
    this.#remoteSynchronizer?.dispose();
    this.configService.dispose();
    this.#personalRepository.dispose();
    this.#nexBuiltinSource.dispose();
  }
}

export function createNodeProviderConfigRuntime(
  options: NodeProviderConfigRuntimeOptions,
): NodeProviderConfigRuntime {
  return new NodeProviderConfigRuntime(options);
}
