// shell / cwd / env 解析：最小逻辑拷贝自
// packages/services/src/terminal/terminalService.ts（resolveTerminalShell、
// resolveTerminalCwd、resolveTerminalEnv、spawnTerminalProcess:248-300 / 157-193 / 194-240）。
// 这里是消重险用的临时副本：spec 决策 3 之后应改为通过 @nex/services 的终端入口复用，
// 而不是长期维护两份。

import { accessSync, constants, statSync } from "node:fs";
import { homedir } from "node:os";
import { delimiter, join } from "node:path";

function isExecutable(command: string): boolean {
  try {
    if (/[\\/]/.test(command)) {
      accessSync(command, constants.X_OK);
      return true;
    }
    const pathEnv = process.env.PATH;
    if (!pathEnv) return false;
    return pathEnv.split(delimiter).some((dir) => {
      if (!dir) return false;
      try {
        accessSync(join(dir, command), constants.X_OK);
        return true;
      } catch {
        return false;
      }
    });
  } catch {
    return false;
  }
}

function isUsableDirectory(path: string): boolean {
  try {
    return statSync(path).isDirectory();
  } catch {
    return false;
  }
}

export function resolveTerminalShell(): string {
  if (process.platform === "win32") {
    // 与 terminalService 一致：优先 pwsh（PSReadLine 在 ConPTY 下更稳），再回退系统 shell。
    const candidates = ["pwsh.exe", "powershell.exe", process.env.ComSpec, "cmd.exe"];
    for (const candidate of candidates) {
      if (candidate && isExecutable(candidate)) return candidate;
    }
    throw new Error("No usable Windows shell found for terminal startup");
  }

  // 不盲信 SHELL：残留的坏路径会让 node-pty 在 posix_spawnp 阶段直接失败。
  const candidates = [process.env.SHELL, "/bin/zsh", "/bin/bash", "/bin/sh"];
  for (const candidate of candidates) {
    if (candidate && isExecutable(candidate)) return candidate;
  }
  throw new Error("No usable shell found for terminal startup");
}

export function resolveTerminalCwd(cwd?: string): string {
  // 工作区可能已被删除/移动；cwd 不可用时回退 HOME / 系统 home / 根目录。
  const candidates = [cwd, process.env.HOME, homedir(), "/"];
  for (const candidate of candidates) {
    if (candidate && isUsableDirectory(candidate)) return candidate;
  }
  throw new Error("No usable working directory found for terminal startup");
}

function isUtf8Locale(value: string | undefined): boolean {
  return /utf-?8/i.test(value ?? "");
}

function isMissingOrCLocale(value: string | undefined): boolean {
  const normalized = (value ?? "").trim().toUpperCase();
  return normalized === "" || normalized === "C" || normalized === "POSIX";
}

const DARWIN_GUI_FALLBACK_PATHS = [
  "/opt/homebrew/bin",
  "/opt/homebrew/sbin",
  "/usr/local/bin",
  "/usr/local/sbin",
  "/usr/bin",
  "/bin",
  "/usr/sbin",
  "/sbin",
] as const;

function mergePathEntries(entries: readonly (string | undefined)[]): string {
  const seen = new Set<string>();
  const merged: string[] = [];
  for (const value of entries) {
    for (const entry of value?.split(delimiter) ?? []) {
      const trimmed = entry.trim();
      if (!trimmed || seen.has(trimmed)) continue;
      seen.add(trimmed);
      merged.push(trimmed);
    }
  }
  return merged.join(delimiter);
}

export function resolveTerminalEnv(base: NodeJS.ProcessEnv = process.env): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = { ...base };
  // macOS 从 GUI 启动的进程 PATH 往往只有 /usr/bin:/bin；补 Homebrew 与系统路径，
  // 保留用户已有顺序，不改全局 process.env。
  if (process.platform === "darwin") {
    env.PATH = mergePathEntries([base.PATH, ...DARWIN_GUI_FALLBACK_PATHS]);
  }
  // 交互终端：TERM/COLORTERM 决定 starship/p10k 与颜色能力探测。
  env.TERM = "xterm-256color";
  env.COLORTERM = env.COLORTERM?.trim() || "truecolor";
  if (env.CI === "1" && base.TERM === "dumb") delete env.CI;

  // 缺 locale 时补 UTF-8，避免中文路径被 shell 显示成 \M-^ 转义。
  const fallbackLocale =
    [base.LC_ALL, base.LC_CTYPE, base.LANG].find(isUtf8Locale) ??
    (process.platform === "darwin" ? "en_US.UTF-8" : "C.UTF-8");
  if (isMissingOrCLocale(env.LANG)) env.LANG = fallbackLocale;
  if (isMissingOrCLocale(env.LC_CTYPE)) env.LC_CTYPE = fallbackLocale;
  if (env.LC_ALL !== undefined && isMissingOrCLocale(env.LC_ALL)) env.LC_ALL = fallbackLocale;
  return env;
}
