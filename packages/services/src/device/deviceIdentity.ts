/**
 * 设备身份（deviceMid）的唯一落盘实现。
 *
 * Nex 不上报遥测；这里保留的只是 `X-Device-Mid` 请求头需要的本机稳定设备标识，
 * 与 Desktop main（desktopDeviceMid）和 CLI（cli-device-mid）共用同一个
 * `telemetry-state.json` 的 `deviceMid` 字段（文件名是历史遗留，不再承担上报语义）。
 */
import { mkdir, open, readFile, stat, unlink, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { createUuid } from "@nex/shared";
import { getAppConfigDir } from "../paths.js";

const LOCK_RETRY_DELAY_MS = 10;
const LOCK_RETRY_COUNT = 200;
const LOCK_STALE_MS = 5 * 60 * 1000;

interface DeviceIdentityState {
  deviceMid?: string;
}

interface DeviceIdentityLockOwner {
  pid: number;
  createdAt: number;
}

const deviceMidCacheByStateFile = new Map<string, Promise<string>>();

function resolveDeviceIdentityStateFile(homeDir?: string): string {
  if (homeDir) {
    return join(homeDir, ".nex", "v2", "telemetry-state.json");
  }
  return join(getAppConfigDir(), "telemetry-state.json");
}

function resolveDeviceIdentityLockFile(homeDir?: string): string {
  if (homeDir) {
    return join(homeDir, ".nex", "v2", "telemetry-state.lock");
  }
  return join(getAppConfigDir(), "telemetry-state.lock");
}

async function readDeviceIdentityState(homeDir?: string): Promise<DeviceIdentityState> {
  try {
    const raw = await readFile(resolveDeviceIdentityStateFile(homeDir), "utf-8");
    const parsed = JSON.parse(raw) as DeviceIdentityState;
    return typeof parsed === "object" && parsed ? parsed : {};
  } catch {
    return {};
  }
}

async function writeDeviceIdentityState(
  state: DeviceIdentityState,
  homeDir?: string,
): Promise<void> {
  const stateFile = resolveDeviceIdentityStateFile(homeDir);
  await mkdir(dirname(stateFile), { recursive: true });
  await writeFile(stateFile, JSON.stringify(state, null, 2), "utf-8");
}

async function sleep(ms: number): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, ms));
}

async function readDeviceIdentityLockOwner(
  lockFile: string,
): Promise<DeviceIdentityLockOwner | null> {
  try {
    const raw = await readFile(lockFile, "utf-8");
    const parsed = JSON.parse(raw) as Partial<DeviceIdentityLockOwner>;
    if (
      typeof parsed.pid === "number" &&
      Number.isInteger(parsed.pid) &&
      parsed.pid > 0 &&
      typeof parsed.createdAt === "number" &&
      Number.isFinite(parsed.createdAt)
    ) {
      return { pid: parsed.pid, createdAt: parsed.createdAt };
    }
  } catch {
    return null;
  }
  return null;
}

function isProcessAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return (
      error instanceof Error && "code" in error && (error as NodeJS.ErrnoException).code !== "ESRCH"
    );
  }
}

async function removeStaleDeviceIdentityLockIfNeeded(
  lockFile: string,
  timestamp: number,
): Promise<boolean> {
  try {
    const metadata = await stat(lockFile);
    if (timestamp - metadata.mtimeMs < LOCK_STALE_MS) {
      const owner = await readDeviceIdentityLockOwner(lockFile);
      if (!owner || isProcessAlive(owner.pid)) {
        return false;
      }
    }
    await unlink(lockFile).catch(() => {});
    return true;
  } catch {
    return false;
  }
}

async function withDeviceIdentityStateLock<T>(
  homeDir: string | undefined,
  run: (state: DeviceIdentityState) => Promise<T>,
): Promise<T> {
  const lockFile = resolveDeviceIdentityLockFile(homeDir);
  await mkdir(dirname(lockFile), { recursive: true });

  for (let attempt = 0; attempt < LOCK_RETRY_COUNT; attempt += 1) {
    try {
      const handle = await open(lockFile, "wx");
      try {
        // 锁文件写入 owner pid，让后续进程能安全回收“刚残留但持有进程已退出”的锁。
        await handle.writeFile(
          JSON.stringify({ pid: process.pid, createdAt: Date.now() }),
          "utf-8",
        );
        const state = await readDeviceIdentityState(homeDir);
        return await run(state);
      } finally {
        await handle.close();
        await unlink(lockFile).catch(() => {});
      }
    } catch (error) {
      const isLockConflict =
        error instanceof Error &&
        "code" in error &&
        (error as NodeJS.ErrnoException).code === "EEXIST";
      if (!isLockConflict) {
        throw error;
      }
      // 崩溃/强退后锁文件可能遗留；按 mtime 识别明显过期的孤儿锁并自动回收。
      const removedStaleLock = await removeStaleDeviceIdentityLockIfNeeded(lockFile, Date.now());
      if (removedStaleLock) {
        continue;
      }
      await sleep(LOCK_RETRY_DELAY_MS);
    }
  }

  throw new Error("Device identity state lock timeout");
}

export interface EnsureTelemetryDeviceMidOptions {
  homeDir?: string;
  randomUUID?: () => string;
}

function rememberDeviceMid(stateFile: string, deviceMid: string): string {
  deviceMidCacheByStateFile.set(stateFile, Promise.resolve(deviceMid));
  return deviceMid;
}

async function ensureDeviceMidInLockedState(
  state: DeviceIdentityState,
  options: EnsureTelemetryDeviceMidOptions,
): Promise<string> {
  const stateFile = resolveDeviceIdentityStateFile(options.homeDir);
  if (state.deviceMid) {
    return rememberDeviceMid(stateFile, state.deviceMid);
  }

  const deviceMid = (options.randomUUID ?? createUuid)();
  state.deviceMid = deviceMid;
  await writeDeviceIdentityState(state, options.homeDir);
  return rememberDeviceMid(stateFile, deviceMid);
}

/**
 * 确保 telemetry-state.json 里存在 deviceMid 并返回它。
 *
 * 远端 nex-server 所在主机没有 Desktop main 进程，过去没有任何进程在那里写该文件，
 * 而 `buildNexSourceHeaders()` 只读不生成；这里供远端 server 启动时调用，与同机 CLI、
 * Desktop 得到同一个本机设备身份。
 */
export function ensureTelemetryDeviceMid(
  options: EnsureTelemetryDeviceMidOptions = {},
): Promise<string> {
  const stateFile = resolveDeviceIdentityStateFile(options.homeDir);
  const cached = deviceMidCacheByStateFile.get(stateFile);
  if (cached) {
    return cached;
  }

  const pending = withDeviceIdentityStateLock(options.homeDir, async (state) =>
    ensureDeviceMidInLockedState(state, options),
  ).catch((error) => {
    if (deviceMidCacheByStateFile.get(stateFile) === pending) {
      deviceMidCacheByStateFile.delete(stateFile);
    }
    throw error;
  });
  deviceMidCacheByStateFile.set(stateFile, pending);
  return pending;
}
