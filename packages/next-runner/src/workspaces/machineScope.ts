/**
 * Machine-level browse scope (docs/next/runner-folders.md decision 2).
 *
 * The server routes the identity `runner:<id>:~` to this runner with
 * `workspacePath: "~"`. Only directory listing, stat and host info are allowed
 * under it — everything else (sessions, terminals, git, file contents, writes)
 * still requires a served folder, so the machine scope cannot be used to read
 * or write files.
 */
import type { ChannelRegistration } from "../link/registry.js";

export const MACHINE_SCOPE_PATH = "~";

/**
 * Channel/method pairs that accept the machine target. `runner-workspaces.*`
 * is included because adding a folder is made from the machine scope.
 */
const MACHINE_SCOPE_CALLS: ReadonlySet<string> = new Set([
  "file.readdir",
  "file.stat",
  "system.info",
]);

export function isMachineScopeAllowedCall(channel: string, method: string): boolean {
  if (channel === "runner-workspaces") return true;
  return MACHINE_SCOPE_CALLS.has(`${channel}.${method}`);
}

/**
 * Reads `workspacePath` (top level, or nested under `workspace`) and reports
 * whether the call targets the machine scope.
 */
export function isMachineScopeTarget(arg: unknown): boolean {
  if (typeof arg !== "object" || arg === null || Array.isArray(arg)) return false;
  const record = arg as Record<string, unknown>;
  if (record["workspacePath"] === MACHINE_SCOPE_PATH) return true;
  const nested = record["workspace"];
  if (typeof nested !== "object" || nested === null || Array.isArray(nested)) return false;
  return (nested as Record<string, unknown>)["workspacePath"] === MACHINE_SCOPE_PATH;
}

/** Rejects a machine target on a channel/method that is not browse-only. */
export function assertMachineScopeAllowed(channel: string, method: string, arg: unknown): void {
  if (!isMachineScopeTarget(arg)) return;
  if (isMachineScopeAllowedCall(channel, method)) return;
  throw new Error(`machine scope is not available for ${channel}.${method}`);
}

/**
 * Machine-scope gate for a raw registration surface (the workspace-tool channels
 * are registered without the workspace guard, because their handlers enforce
 * path containment themselves). Without this, `file.readTextFile` or a terminal
 * call carrying `workspacePath: "~"` would reach a handler that ignores the
 * field instead of being refused.
 */
export function createMachineScopeGuardedRegistration(
  registry: ChannelRegistration,
): ChannelRegistration {
  return {
    call(channel, method, handler) {
      registry.call(channel, method, async (arg) => {
        assertMachineScopeAllowed(channel, method, arg);
        return handler(arg);
      });
    },
    listen(channel, event, handler) {
      registry.listen(channel, event, (arg, emit) => {
        // 订阅没有浏览语义：机器级目标永远不适用于 listen。
        if (isMachineScopeTarget(arg)) {
          throw new Error(`machine scope is not available for ${channel}.${event}`);
        }
        return handler(arg, emit);
      });
    },
  };
}
