/**
 * E2E-only provider (see packages/next-runner/test/fixtures/e2e-provider).
 *
 * pi-ai has no public hook for registering a second runtime provider API, so
 * the E2E harness registers one when the runner process starts (main.ts calls
 * `registerE2EProvider` when PI_CODING_AGENT_DIR carries an `extensions/
 * e2eProvider.ts` marker). It exposes the OpenAI-completions provider table
 * under a private API name; the E2E models.json points at the local faux
 * server, giving every suite one scripted, deterministic model with no network
 * access. Normal builds never call it and the config stays in fallback mode.
 */
import { registerApiProvider } from "@earendil-works/pi-ai/compat";
import { stream, streamSimple } from "@earendil-works/pi-ai/api/openai-completions";
import { getAgentDir } from "@earendil-works/pi-coding-agent";
import { existsSync } from "node:fs";
import { join } from "node:path";
import type { Logger } from "../logger.js";

export const E2E_PROVIDER_API = "e2e-openai-completions";

let registered = false;

/**
 * Registers the E2E provider API when the active agent dir declares the marker
 * file `extensions/e2eProvider.ts` (written by next/dev.sh for --e2e runs).
 */
export function registerE2EProvider(logger: Logger): boolean {
  if (registered) return true;
  const marker = join(getAgentDir(), "extensions", "e2eProvider.ts");
  if (!existsSync(marker)) return false;
  // The non-lazy table keeps the provider functions in this module's scope, so
  // no lazy import runs later on a separate module instance.
  registerApiProvider(
    {
      api: E2E_PROVIDER_API,
      stream: stream as never,
      streamSimple: streamSimple as never,
    },
    "e2e-provider",
  );
  registered = true;
  logger.info("registered E2E provider api", { api: E2E_PROVIDER_API });
  return true;
}
