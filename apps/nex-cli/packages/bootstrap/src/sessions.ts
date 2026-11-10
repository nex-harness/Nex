import { createConfig } from "@nex/adapters/config";
import { openStartupSqliteSessionStore } from "@nex/adapters/storage";
import type { SessionInfo } from "@nex/contracts";
import type { ListNexSessionsOptions, ResolveLatestSessionOptions } from "./app/types.js";
import { getSessionDbPath, isClosableSessionStore } from "./app/session-store.js";

export async function resolveLatestSession(
  options: ResolveLatestSessionOptions,
): Promise<SessionInfo | null> {
  const ownsStore = options.sessionStore === undefined;
  let sessionStore = options.sessionStore;
  if (!sessionStore) {
    const configResult = createConfig({ env: options.env });
    sessionStore = await openStartupSqliteSessionStore({ dbPath: getSessionDbPath(configResult) });
  }

  try {
    const sessions = await sessionStore.listSessions({
      directory: options.directory,
      limit: 1,
      roots: true,
    });
    return sessions[0] ?? null;
  } finally {
    if (ownsStore && isClosableSessionStore(sessionStore)) {
      sessionStore.close();
    }
  }
}

export async function listNexSessions(
  options: ListNexSessionsOptions = {},
): Promise<SessionInfo[]> {
  const ownsStore = options.sessionStore === undefined;
  let sessionStore = options.sessionStore;
  if (!sessionStore) {
    const configResult = createConfig({ env: options.env });
    sessionStore = await openStartupSqliteSessionStore({ dbPath: getSessionDbPath(configResult) });
  }

  try {
    return await sessionStore.listSessions({
      directory: options.directory,
      limit: options.limit ?? 50,
      roots: true,
    });
  } finally {
    if (ownsStore && isClosableSessionStore(sessionStore)) {
      sessionStore.close();
    }
  }
}
