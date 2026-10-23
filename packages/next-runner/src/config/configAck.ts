import type { EngineCatalog } from "../engine/types.js";
import type { Logger } from "../logger.js";
import type { AppliedResult, RuntimeConfigManager } from "./applyRuntimeConfig.js";
import { fallbackCatalogFacts } from "./catalogFacts.js";

/**
 * What the runner reports back after one `config` frame (spec docs/next/
 * config-mcp-skills.md decision 4).
 *
 * Server-owned mode: `RuntimeConfigManager.apply` builds facts from the applied
 * runtime. Fallback mode (decision 2: no server providers ⇒ the runner keeps
 * using its own `~/.pi/agent`): the runtime is the user's own catalog, so the
 * facts are the models pi actually exposes. The server compiles the composer's
 * model-selection view from `facts.modelsByProvider` in both modes.
 *
 * Facts never carry a credential: only provider/model ids and public metadata.
 */
export interface ConfigExecutorOptions {
  manager: RuntimeConfigManager;
  /** The engine's current catalog (fallback: the user's ~/.pi/agent runtime). */
  catalog: () => Promise<EngineCatalog>;
  /** pi's own credential status for one provider (fallback mode). */
  providerConfigured: (providerId: string) => boolean;
  logger: Logger;
}

export class ConfigExecutor {
  constructor(private readonly options: ConfigExecutorOptions) {}

  async apply(frame: unknown): Promise<AppliedResult> {
    const result = await this.options.manager.apply(frame);
    if (!result.ok || result.facts["mode"] !== "fallback") return result;
    try {
      const catalog = await this.options.catalog();
      return {
        ...result,
        facts: fallbackCatalogFacts(catalog, {
          version: result.version,
          configured: this.options.providerConfigured,
        }),
      };
    } catch (error) {
      // The fallback runtime always exists, so this only costs the server its
      // picker until the next apply; the config itself stays applied.
      this.options.logger.warn("fallback catalog facts unavailable", {
        error: error instanceof Error ? error.message : String(error),
      });
      return result;
    }
  }
}
