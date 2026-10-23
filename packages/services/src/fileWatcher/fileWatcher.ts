import type { Event } from "@nex/rpc";
import type { FileWatchEvent } from "@nex/shared";
import { ServiceChannels } from "@nex/shared";
import { createServiceDescriptor } from "../descriptors.js";

/**
 * 文件系统监视服务
 *
 * 按路径粒度管理 watcher 实例。UI 展开目录时调用非递归 watch()，
 * Git 这类工作区级状态可调用递归 watch()。事件通过 onDynamicChange 以 RPC event 流式传输。
 */
export interface IFileWatcherService {
  /**
   * 开始监视路径。返回 watcherId，用于 unwatch 和事件订阅。
   * `workspacePath`/`workspaceIdentity` 可选：多 runner 拓扑下 server 据此把 watch 路由到
   * 对应 runner，并把 watcherId 绑定在该目标上（后续 onDynamicChange 继承同一绑定）。
   * 桌面/local host 实现忽略这两个字段。
   */
  watch(params: {
    path: string;
    recursive?: boolean;
    workspacePath?: string;
    workspaceIdentity?: string;
  }): Promise<{ id: string }>;
  /** 停止监视。释放 watcher 和相关资源 */
  unwatch(params: { id: string }): Promise<void>;
  /** 停止全部监视。host 退出清理时用于统一释放底层 fs.watch 句柄 */
  disposeAll(): void;
  /** 按 watcherId 订阅变更事件（onDynamic* 模式，RPC 自动路由） */
  onDynamicChange(id: string): Event<FileWatchEvent>;
}

export const IFileWatcherService = createServiceDescriptor<IFileWatcherService>(
  ServiceChannels.FileWatcher,
);
