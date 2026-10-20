import { randomUUID } from "node:crypto";
import { mkdir, readFile } from "node:fs/promises";
import { hostname, arch, platform } from "node:os";
import { homedir } from "node:os";
import { delimiter, isAbsolute, resolve } from "node:path";
import { randomId } from "./util/ids.js";
import { writeJsonAtomic } from "./util/fs.js";

export interface RunnerConfig {
  serverUrl: string;
  token: string | undefined;
  runnerId: string;
  name: string;
  workspaces: string[];
  dataDir: string;
  logLevel: string;
  version: string;
}

const DEFAULT_DATA_DIR = resolve(homedir(), ".nex-next", "runner");
const DEFAULT_SERVER_URL = "ws://127.0.0.1:3030/runner";

export function resolveConfig(env: NodeJS.ProcessEnv): RunnerConfig {
  const dataDir = env["NEX_RUNNER_DATA_DIR"]
    ? resolve(env["NEX_RUNNER_DATA_DIR"])
    : DEFAULT_DATA_DIR;
  const workspaces = parseWorkspaces(env["NEX_RUNNER_WORKSPACES"]);
  if (workspaces.length === 0) {
    throw new Error(
      "NEX_RUNNER_WORKSPACES is required: give at least one absolute workspace path separated by ',' or the platform path delimiter",
    );
  }
  return {
    serverUrl: normalizeServerUrl(env["NEX_SERVER_URL"] ?? DEFAULT_SERVER_URL),
    token: env["NEX_RUNNER_TOKEN"],
    runnerId: env["NEX_RUNNER_ID"] ?? "",
    name: env["NEX_RUNNER_NAME"] ?? hostname(),
    workspaces,
    dataDir,
    logLevel: env["NEX_RUNNER_LOG_LEVEL"] ?? "info",
    version: env["NEX_RUNNER_VERSION"] ?? "0.1.0",
  };
}

export function parseWorkspaces(value: string | undefined): string[] {
  if (!value) return [];
  const parts = value
    .split(value.includes(",") ? "," : delimiter)
    .map((part) => part.trim())
    .filter((part) => part.length > 0);
  const unique: string[] = [];
  for (const part of parts) {
    if (!isAbsolute(part)) {
      throw new Error(`Workspace path must be absolute: ${part}`);
    }
    const normalized = resolve(part);
    if (!unique.includes(normalized)) unique.push(normalized);
  }
  return unique;
}

function normalizeServerUrl(raw: string): string {
  const trimmed = raw.trim();
  if (!/^wss?:\/\//u.test(trimmed)) {
    throw new Error(`NEX_SERVER_URL must start with ws:// or wss://: ${raw}`);
  }
  return trimmed.endsWith("/runner") ? trimmed : `${trimmed.replace(/\/+$/u, "")}/runner`;
}

/**
 * Persists a random runner id so reconnects keep the same identity. The runner is the
 * only writer of this file.
 */
export async function resolveRunnerId(dataDir: string, explicit: string): Promise<string> {
  if (explicit.trim().length > 0) return explicit.trim();
  await mkdir(dataDir, { recursive: true });
  const path = resolve(dataDir, "runner-id.json");
  try {
    const text = await readFile(path, "utf8");
    const parsed = JSON.parse(text) as { runnerId?: unknown };
    if (typeof parsed.runnerId === "string" && parsed.runnerId.length > 0) {
      return parsed.runnerId;
    }
  } catch {
    // Missing/corrupt id file: generate and replace it.
  }
  const runnerId = `runner_${randomUUID().replaceAll("-", "").slice(0, 16)}`;
  await writeJsonAtomic(path, { runnerId, createdAt: new Date().toISOString() });
  return runnerId;
}

export function labelForWorkspace(path: string): string {
  const base = path.split(/[\\/]/u).filter(Boolean).at(-1);
  return base ?? path;
}

export const runnerPlatform = platform();
export const runnerArch = arch();

export { randomId };
