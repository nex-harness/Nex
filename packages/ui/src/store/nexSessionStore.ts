/**
 * Nex session UI 状态 store
 *
 * 一个 tab 对应一个 workspace，所以聊天相关状态也必须按 workspace 分桶保存。
 * 这样切换标签页时，当前任务、输入中的草稿态和初始化状态才不会互相串台。
 */
import { create } from "zustand";
import { shouldExposeE2EStoreBridge } from "@/lib/e2eStoreBridge.js";
import { type NexSessionStoreState } from "./nexSessionStoreTypes.js";
import { getWorkspaceState } from "./nexSessionStoreSelectors.js";
import { createNavigationSlice } from "./nexSessionStoreNavigation.js";
import { createTaskSlice } from "./nexSessionStoreTaskSlice.js";
import { createWorkspaceSlice } from "./nexSessionStoreWorkspaceSlice.js";
import { uiMemoryDiagnosticsRegistry } from "@/lib/memoryDiagnostics.js";

export const useNexSessionStore = create<NexSessionStoreState>()((set, get) => ({
  workspaces: {},
  ...createNavigationSlice(set, get),
  ...createWorkspaceSlice(set),
  ...createTaskSlice(set),
  getWorkspaceState: (workspacePath: string, workspaceIdentity?: string) =>
    getWorkspaceState(get(), workspacePath, workspaceIdentity),
}));

type NexSessionStoreE2EBridge = typeof useNexSessionStore;

declare global {
  interface Window {
    __nexSessionStoreE2E?: NexSessionStoreE2EBridge;
  }
}

if (shouldExposeE2EStoreBridge()) {
  // E2E 诊断入口必须由 WDIO 显式打开，不能复用 NEX_ENV=test，避免产品测试环境暴露可变全局 store。
  window.__nexSessionStoreE2E = useNexSessionStore;
}

// ────────────────────────────────────────────
// Re-exports: 保持外部 `from '@/store/nexSessionStore'` 的导入路径继续工作
// ────────────────────────────────────────────
export * from "./nexSessionStoreTypes.js";
export * from "./nexSessionStoreSelectors.js";
// Re-export navigation types used externally:
export type {
  TaskNavigationHistory,
  TaskNavEntry,
  WorkspaceNavEntry,
} from "@/lib/taskNavigationHistory.js";

// 内存诊断计数器：workspace 桶全仓无删除路径，先落日志。
uiMemoryDiagnosticsRegistry.register("sessionStore", () => ({
  workspaces: Object.keys(useNexSessionStore.getState().workspaces).length,
}));
