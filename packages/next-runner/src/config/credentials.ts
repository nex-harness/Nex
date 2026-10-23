import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { ModelRuntime, getAgentDir } from "@earendil-works/pi-coding-agent";
import { InMemoryModelsStore } from "@earendil-works/pi-ai";
import type {
  AuthOperationOptions,
  Credential,
  CredentialInfo,
  CredentialStore,
} from "@earendil-works/pi-ai";
import type { RuntimeConfig } from "./runtimeConfig.js";
import { MemoryCredentialStore } from "./applyRuntimeConfig.js";

/**
 * Credential plumbing of the server-owned runtime (split out of
 * applyRuntimeConfig.ts for the 400-line file budget): an in-memory key
 * overlay on top of a read-only snapshot of the user's auth.json, so pi's own
 * subscription logins keep working without the runner ever writing that file.
 */

/**
 * Read-only view of the user's auth.json (null when it cannot be opened). pi
 * writes auth.json through its own lock; the runner must never write it, so the
 * stored credentials (OAuth subscriptions, stored keys) are copied here and the
 * pushed keys live in the overlay above them.
 */
async function defaultCredentialStore(): Promise<CredentialStore | null> {
  try {
    return await FileCredentialSnapshot.open(join(getAgentDir(), "auth.json"));
  } catch {
    return null;
  }
}

/** A plain in-memory snapshot of auth.json entries. */
class FileCredentialSnapshot implements CredentialStore {
  private constructor(private readonly entries: Map<string, Credential>) {}

  static async open(path: string): Promise<FileCredentialSnapshot> {
    const entries = new Map<string, Credential>();
    const raw = JSON.parse(await readFile(path, "utf8")) as Record<string, Credential>;
    for (const [providerId, credential] of Object.entries(raw)) {
      if (credential && typeof credential === "object" && typeof credential.type === "string") {
        entries.set(providerId, credential);
      }
    }
    return new FileCredentialSnapshot(entries);
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
    _fn: unknown,
    options?: AuthOperationOptions,
  ): Promise<undefined | Credential> {
    options?.signal?.throwIfAborted();
    return this.entries.get(providerId);
  }

  async delete(_providerId: string, options?: AuthOperationOptions): Promise<void> {
    options?.signal?.throwIfAborted();
  }
}

/** Server-owned runtime: derived models.json + in-memory keys, no auth.json. */
export async function createServerRuntime(
  config: RuntimeConfig,
  modelsPath: string,
): Promise<ModelRuntime> {
  // Seed from the stored auth (oauth subscriptions, stored api keys) and then
  // overlay the pushed keys; models.json is the derived one.
  const stored = await defaultCredentialStore();
  const seeded = new Map<string, Credential>();
  for (const info of (await stored?.list()) ?? []) {
    const credential = await stored?.read(info.providerId);
    if (credential) seeded.set(info.providerId, credential);
  }
  const credentials = new MemoryCredentialStore(seeded);
  const runtime = await ModelRuntime.create({
    credentials,
    modelsPath,
    modelsStore: new InMemoryModelsStore(),
    refreshOnCreate: false,
  });
  for (const [providerId, key] of config.credentials) {
    await runtime.setRuntimeApiKey(providerId, key);
  }
  await runtime.refresh({ allowNetwork: false });
  return runtime;
}
