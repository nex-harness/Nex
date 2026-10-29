/**
 * Pull-based self-update (docs/next/runner-update.md decision 5).
 *
 * The server sends `{t:"update", buildId}` when this runner's build is older than
 * the bundle staged for its platform. The runner downloads the allow-listed files
 * with its own runner token, verifies the content hash, and swaps them in place.
 * The supervisor or a detached respawn brings it back; the process never rewrites
 * itself in memory.
 *
 * Ordering constraints:
 * - Nothing is written before the hash and dependency checks pass, so a bad bundle
 *   cannot leave a half-installed runner.
 * - `.update-pending` is written before the swap; the first `welcome` after the
 *   restart deletes it (see `markStartupComplete`). A build that crashes before
 *   `welcome` leaves it for `rollback.ts`.
 * - `runner.mjs` is renamed last: the running process only reads it at start, so a
 *   rename over it succeeds, and a partially swapped install still runs the old
 *   code until the very last step.
 */
import { copyFile, mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import { spawn } from "node:child_process";
import { dirname, join } from "node:path";
import type { Logger } from "../logger.js";
import { writeJsonAtomic } from "../util/fs.js";
import { BUILD_ID_FILES, computeBuildId, DEV_BUILD_ID } from "./buildId.js";
import {
  PENDING_FILE,
  PREV_DIR,
  UPDATE_DIR,
  UPDATE_FILES,
  type PendingUpdate,
} from "./rollback.js";
import { respawnDetached, resolveSupervisorMode, type SpawnFn } from "./supervisor.js";

/** Files the server serves for a bundle; the runner never asks for anything else. */
export const BUNDLE_FILES = [
  "runner.mjs",
  "pty-host.mjs",
  "lib/ptyHostAssets.mjs",
  "build.json",
  "package.json",
] as const;

/**
 * Rename order for the swap. `runner.mjs` last (see the module comment);
 * `build.json` after the code it describes.
 */
const SWAP_ORDER = [
  "package.json",
  "lib/ptyHostAssets.mjs",
  "pty-host.mjs",
  "build.json",
  "runner.mjs",
];

export type UpdateState = "downloading" | "restarting" | "failed";

export interface SelfUpdateStatus {
  state: UpdateState;
  buildId: string;
  error?: string;
}

export interface SelfUpdateOptions {
  /** Directory holding the running bundle (`.prev/` and `.update/` live here). */
  installDir: string;
  /** This process's build id (`"dev"` when running from source). */
  buildId: string;
  /** `NEX_SERVER_URL` (`ws://host/runner` / `wss://host/runner`). */
  serverUrl: string;
  token: string | undefined;
  logger: Logger;
  /** Reports progress on the link; no-op when the link is down. */
  sendStatus: (status: SelfUpdateStatus) => void;
  /** Stops the link cleanly before the process exits. */
  stopLink: () => Promise<void>;
  /** `os-arch`, the same key the staged bundles use. */
  platform?: string;
  env?: NodeJS.ProcessEnv;
  fetch?: typeof fetch;
  exit?: (code: number) => void;
  spawn?: SpawnFn;
  execPath?: string;
  argv?: readonly string[];
  cwd?: string;
  now?: () => number;
  /** Download timeout per file. */
  timeoutMs?: number;
}

/** Exit code that supervisors and wrapper loops restart on (decision 5). */
export const RESTART_EXIT_CODE = 75;

export class SelfUpdater {
  private updating = false;
  private started = false;

  constructor(private readonly options: SelfUpdateOptions) {}

  /**
   * Handles one `{t:"update", buildId}` frame. Ignores a target equal to the running
   * build, a dev process, and a second frame while an update is in flight — the
   * server sends at most one per connection, but a reconnect can race a slow swap.
   */
  async handleUpdate(targetBuildId: string): Promise<void> {
    const { buildId, logger } = this.options;
    if (this.updating) {
      logger.debug("self-update ignored: already running", { targetBuildId });
      return;
    }
    if (buildId === DEV_BUILD_ID) {
      logger.info("self-update ignored: dev build");
      return;
    }
    if (targetBuildId === buildId) {
      logger.debug("self-update ignored: already current", { buildId });
      return;
    }
    this.updating = true;
    try {
      await this.run(targetBuildId);
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      logger.warn("self-update failed", { targetBuildId, message });
      this.options.sendStatus({ state: "failed", buildId: targetBuildId, error: message });
    } finally {
      this.updating = false;
    }
  }

  /**
   * Called on the first `welcome` after a (re)start: the new build reached the
   * server, so the pending marker and the staging directory are no longer needed.
   */
  async markStartupComplete(): Promise<void> {
    if (this.started) return;
    this.started = true;
    const { installDir, logger } = this.options;
    try {
      await rm(join(installDir, PENDING_FILE), { force: true });
      await rm(join(installDir, UPDATE_DIR), { recursive: true, force: true });
    } catch (error) {
      logger.warn("self-update: cannot clear pending marker", {
        message: error instanceof Error ? error.message : String(error),
      });
    }
  }

  private async run(targetBuildId: string): Promise<void> {
    const { buildId, logger } = this.options;
    this.options.sendStatus({ state: "downloading", buildId: targetBuildId });
    const platform = this.options.platform ?? `${process.platform}-${process.arch}`;
    const downloaded = await this.download(platform, targetBuildId);
    // 内容校验：先算哈希再落盘，否则坏包会覆盖可运行的安装。
    const actual = computeBuildId(BUILD_ID_FILES.map((file) => downloaded.get(file) as Buffer));
    if (actual !== targetBuildId) {
      throw new Error(`downloaded build id ${actual} does not match ${targetBuildId}`);
    }
    await this.assertDependenciesUnchanged(downloaded.get("package.json") as Buffer);
    await this.swap(downloaded, { buildId: targetBuildId, previousBuildId: buildId });
    logger.info("self-update swapped files", { from: buildId, to: targetBuildId });
    this.options.sendStatus({ state: "restarting", buildId: targetBuildId });
    await this.restart();
  }

  /** GET /api/runner-bundle/<platform>/<file> for every allow-listed file. */
  private async download(platform: string, targetBuildId: string): Promise<Map<string, Buffer>> {
    const origin = bundleOrigin(this.options.serverUrl);
    const token = this.options.token;
    if (!token) throw new Error("NEX_RUNNER_TOKEN is required to download a bundle");
    const doFetch = this.options.fetch ?? fetch;
    const timeoutMs = this.options.timeoutMs ?? 60_000;
    const files = new Map<string, Buffer>();
    for (const file of BUNDLE_FILES) {
      const url = `${origin}/api/runner-bundle/${encodeURIComponent(platform)}/${file}`;
      const response = await doFetch(url, {
        headers: { Authorization: `Bearer ${token}` },
        signal: AbortSignal.timeout(timeoutMs),
      });
      if (!response.ok) {
        throw new Error(`download ${file} failed: HTTP ${String(response.status)}`);
      }
      files.set(file, Buffer.from(await response.arrayBuffer()));
    }
    this.options.logger.debug("self-update downloaded bundle", {
      platform,
      targetBuildId,
      files: [...files.keys()],
    });
    return files;
  }

  /**
   * A bundle whose dependency set differs cannot be installed by a file swap: the
   * private Node and `node_modules` stay as they are, so the new code may import
   * something that is not there. Report the spec's reinstall error instead.
   */
  private async assertDependenciesUnchanged(downloadedPackage: Buffer): Promise<void> {
    const installed = await this.readInstalledDependencies();
    let incoming: unknown;
    try {
      incoming = (JSON.parse(downloadedPackage.toString("utf8")) as { dependencies?: unknown })
        .dependencies;
    } catch {
      throw new Error("downloaded package.json is not valid JSON");
    }
    if (!deepEqual(installed, incoming ?? {})) {
      throw new Error("dependencies changed; reinstall required");
    }
  }

  private async readInstalledDependencies(): Promise<unknown> {
    let raw: string;
    try {
      raw = await readFile(join(this.options.installDir, "package.json"), "utf8");
    } catch {
      // 安装目录里没有 package.json：无法证明依赖一致，按需要重装处理。
      throw new Error("dependencies changed; reinstall required");
    }
    try {
      return (JSON.parse(raw) as { dependencies?: unknown }).dependencies ?? {};
    } catch {
      throw new Error("installed package.json is not valid JSON");
    }
  }

  /**
   * Stage, mark pending, keep the old files in `.prev/`, then rename into place.
   * A crash between renames leaves `.update-pending`, which the startup check
   * turns into a rollback.
   */
  private async swap(
    downloaded: Map<string, Buffer>,
    ids: { buildId: string; previousBuildId: string },
  ): Promise<void> {
    const { installDir, now } = this.options;
    const staging = join(installDir, UPDATE_DIR, ids.buildId);
    for (const file of BUNDLE_FILES) {
      const target = join(staging, file);
      await mkdir(dirname(target), { recursive: true });
      await writeFile(target, downloaded.get(file) as Buffer);
    }
    const pending: PendingUpdate = {
      buildId: ids.buildId,
      previousBuildId: ids.previousBuildId,
      at: (now ?? Date.now)(),
      attempts: 0,
    };
    // 先写标记再动安装文件：中途崩溃时启动检查能发现这次未完成的更新。
    await writeJsonAtomic(join(installDir, PENDING_FILE), pending);
    await this.backupCurrentFiles();
    for (const file of SWAP_ORDER) {
      await rename(join(staging, file), join(installDir, file));
    }
  }

  /** Copies the currently installed files into `.prev/` (absent ones are skipped). */
  private async backupCurrentFiles(): Promise<void> {
    const prevDir = join(this.options.installDir, PREV_DIR);
    await rm(prevDir, { recursive: true, force: true });
    for (const file of UPDATE_FILES) {
      const source = join(this.options.installDir, file);
      const target = join(prevDir, file);
      try {
        await mkdir(dirname(target), { recursive: true });
        await copyFile(source, target);
      } catch {
        // 旧安装缺少 build.json 等新文件：只备份存在的部分。
      }
    }
  }

  /**
   * Stops the link, then either exits 75 for a supervisor/wrapper or starts a
   * detached successor first. Exiting 75 in both cases keeps one restart path.
   */
  private async restart(): Promise<void> {
    const exit = this.options.exit ?? ((code: number) => process.exit(code));
    try {
      await this.options.stopLink();
    } catch (error) {
      this.options.logger.warn("self-update: link stop failed", {
        message: error instanceof Error ? error.message : String(error),
      });
    }
    const env = this.options.env ?? process.env;
    if (resolveSupervisorMode(env) === "respawn") {
      try {
        respawnDetached({
          spawn: this.options.spawn ?? defaultSpawn,
          execPath: this.options.execPath,
          argv: this.options.argv,
          cwd: this.options.cwd,
          env,
        });
      } catch (error) {
        // 自举失败也不能留在旧进程里假装更新成功：退出码 75 仍会让 wrapper 重试。
        this.options.logger.error("self-update: detached respawn failed", {
          message: error instanceof Error ? error.message : String(error),
        });
      }
    }
    exit(RESTART_EXIT_CODE);
  }
}

/** `ws://host/runner` → `http://host`, `wss://` → `https://` (decision 5). */
export function bundleOrigin(serverUrl: string): string {
  const parsed = new URL(serverUrl);
  const protocol = parsed.protocol === "wss:" ? "https:" : "http:";
  return `${protocol}//${parsed.host}`;
}

/** Structural equality for the JSON dependency maps (key order must not matter). */
export function deepEqual(left: unknown, right: unknown): boolean {
  if (left === right) return true;
  if (typeof left !== "object" || typeof right !== "object" || left === null || right === null) {
    return false;
  }
  if (Array.isArray(left) !== Array.isArray(right)) return false;
  if (Array.isArray(left) && Array.isArray(right)) {
    if (left.length !== right.length) return false;
    return left.every((value, index) => deepEqual(value, right[index]));
  }
  const leftRecord = left as Record<string, unknown>;
  const rightRecord = right as Record<string, unknown>;
  const leftKeys = Object.keys(leftRecord).sort();
  const rightKeys = Object.keys(rightRecord).sort();
  if (leftKeys.length !== rightKeys.length) return false;
  return leftKeys.every(
    (key, index) => key === rightKeys[index] && deepEqual(leftRecord[key], rightRecord[key]),
  );
}

/** Default detached spawn. */
const defaultSpawn: SpawnFn = (command, args, options) => spawn(command, [...args], options);
