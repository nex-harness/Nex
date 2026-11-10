import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { NexStdioTapDevState } from "@nex/shared";
import { getAppConfigDir } from "#src/paths.js";
import { isEffectiveDevelopmentNodeEnv } from "#src/runtime-tools/nodeEnv.js";

interface NexStdioTapStateFile {
  enabled?: boolean;
}

export function isNexStdioTapDevVisible(): boolean {
  return isEffectiveDevelopmentNodeEnv();
}

export function getNexStdioTapDevDir(): string {
  return join(getAppConfigDir(), "dev");
}

export function getNexStdioTapDevLogDir(): string {
  return join(getNexStdioTapDevDir(), "stdio-traffic");
}

export function getNexStdioTapDevStatePath(): string {
  return join(getNexStdioTapDevDir(), "nex-stdio-tap.json");
}

function readStateFile(path: string): NexStdioTapStateFile {
  if (!existsSync(path)) {
    return {};
  }

  try {
    const parsed = JSON.parse(readFileSync(path, "utf-8")) as unknown;
    return parsed && typeof parsed === "object" ? (parsed as NexStdioTapStateFile) : {};
  } catch {
    return {};
  }
}

export function readNexStdioTapDevState(): NexStdioTapDevState {
  const visible = isNexStdioTapDevVisible();
  const statePath = getNexStdioTapDevStatePath();
  const fileState = readStateFile(statePath);
  return {
    enabled: visible && fileState.enabled === true,
    visible,
    logDir: getNexStdioTapDevLogDir(),
    statePath,
  };
}

export function setNexStdioTapDevEnabled(enabled: boolean): NexStdioTapDevState {
  const visible = isNexStdioTapDevVisible();
  const statePath = getNexStdioTapDevStatePath();
  mkdirSync(getNexStdioTapDevDir(), { recursive: true });
  writeFileSync(
    statePath,
    `${JSON.stringify(
      {
        // 开发态 stdio 抓包是高频原始协议帧，只能通过显式开关写旁路文件，避免误进生产日志。
        enabled: visible && enabled,
        updatedAt: new Date().toISOString(),
      },
      null,
      2,
    )}\n`,
  );
  return readNexStdioTapDevState();
}
