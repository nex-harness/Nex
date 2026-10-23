import { useCallback } from "react";
import { useWorkspaceBoundServices } from "@/hooks/useWorkspaceServices.js";
import { TerminalSession } from "@/terminal/TerminalSession.js";

export function SidePaneTerminalPane({
  workspacePath,
  workspaceIdentity,
  sessionId,
  workspaceKey,
  cwd,
  isVisible,
  isWindowsDesktop = false,
  onOpenBrowserUrl,
}: {
  /** terminal 所属 workspace 的绝对路径；create 时携带，server 据此路由到所属 runner。 */
  workspacePath?: string;
  /** terminal 所属 workspace 的路由身份（`runner:<id>:<path>`）；桌面/local host 忽略。 */
  workspaceIdentity?: string;
  /**
   * 保活：sessionId 复用 tab.id（跨 workspace 稳定），同时作为 TerminalSession 的 persistentKey。
   * 让 xterm+PTY 所有权进 sidePaneTerminalSessionRegistry 模块级单例，
   * 组件卸载只 detach、不 dispose；重挂按 key 复用，scrollback 跨 workspace 保活。
   */
  sessionId: string;
  /**
   * workspace 身份隔离 key（= workspaceIdentity?.trim() || workspacePath）。
   * 写入 registry entry.workspaceKey，workspace tab 关闭时按此批量回收 PTY。
   */
  workspaceKey?: string;
  cwd?: string;
  isVisible: boolean;
  isWindowsDesktop?: boolean;
  onOpenBrowserUrl: (url: string) => void;
}) {
  // terminal 只绑定 workspace（不绑定远程 session）：多 runner 下由 server 按身份路由。
  const boundServices = useWorkspaceBoundServices(workspacePath, null, workspaceIdentity);
  const handleShellLabelChange = useCallback(() => {
    // 业务说明：side pane 外层 tab 已经承载终端标题，这里只需要单个 shell 实例，
    // 不再显示或同步第二层 terminal tab 标题，避免形成嵌套 tabs。
  }, []);

  return (
    <section className="h-full min-h-0 overflow-hidden bg-background p-3">
      <TerminalSession
        sessionId={sessionId}
        persistentKey={sessionId}
        terminalPersistentKey={sessionId}
        workspaceKey={workspaceKey}
        services={boundServices}
        cwd={cwd}
        isVisible={isVisible}
        isWindowsDesktop={isWindowsDesktop}
        onShellLabelChange={handleShellLabelChange}
        onOpenBrowserUrl={onOpenBrowserUrl}
      />
    </section>
  );
}
