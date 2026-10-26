import type { AgentSession } from "@earendil-works/pi-coding-agent";

/**
 * Releases a pi session the way pi's own runtime does (`AgentSessionRuntime.dispose`):
 * extensions get `session_shutdown` first, then the session is disposed.
 *
 * 修复：`AgentSession.dispose()` 本身不发 `session_shutdown`。MCP 扩展只在这个事件里关闭
 * stdio 子进程和连接，直接 dispose 会把 MCP server 子进程留在后台（测试进程因此无法退出，
 * 长跑的 runner 每次关闭会话都泄漏一个子进程）。
 */
export async function disposePiSession(
  session: AgentSession,
  reason: "quit" | "reload" = "quit",
): Promise<void> {
  try {
    const runner = session.extensionRunner;
    if (runner.hasHandlers("session_shutdown")) {
      await runner.emit({ type: "session_shutdown", reason });
    }
  } catch {
    // Shutdown hooks are best-effort; the session is released regardless.
  }
  session.dispose();
}
