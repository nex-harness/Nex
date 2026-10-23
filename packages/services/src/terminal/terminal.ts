import type { Event } from "@nex/rpc";
import { ServiceChannels } from "@nex/shared";
import { createServiceDescriptor } from "../descriptors.js";
import type { TerminalFontFamilySource, TerminalThemeProfile } from "./terminalProfile.js";

export interface TerminalWindowsPtyInfo {
  backend: "conpty" | "winpty";
  buildNumber?: number;
}

export interface TerminalCreateParams {
  cols: number;
  rows: number;
  cwd?: string;
  /**
   * 目标 workspace 的绝对路径与路由身份（`runner:<runnerId>:<path>`）。
   * 多 runner 拓扑下 terminal 按此路由；桌面/local host 实现忽略。
   */
  workspacePath?: string;
  workspaceIdentity?: string;
  /**
   * 重连目标（docs/next/workspace-tools.md Decision 5）：若该终端在 runner 上仍存活，
   * 以相同 id 重挂并 resize，而不是新建 PTY；返回的 id 与传入一致即视为重挂成功。
   * 桌面/local host 实现忽略该字段，总是返回新 id（调用方按新建处理）。
   */
  terminalId?: string;
}

export interface ITerminalService {
  create(params: TerminalCreateParams): Promise<{
    id: string;
    shell: string;
    fontFamily: string;
    fontSize?: number;
    theme?: TerminalThemeProfile;
    fontFamilySource: TerminalFontFamilySource;
    windowsPty?: TerminalWindowsPtyInfo;
  }>;
  write(params: { id: string; data: string }): Promise<void>;
  resize(params: { id: string; cols: number; rows: number }): Promise<void>;
  dispose(params: { id: string }): Promise<void>;
  onDynamicData(id: string): Event<string>;
  onDynamicExit(id: string): Event<number>;
}

export const ITerminalService = createServiceDescriptor<ITerminalService>(ServiceChannels.Terminal);
