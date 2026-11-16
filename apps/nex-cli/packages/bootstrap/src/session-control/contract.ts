import type { AppSessionControl, AppSessionTrustedContext } from "@nex/session-control/contracts";
import type { NexProtocolAgentServerContext } from "../nex-protocol/server-types.js";

export interface AppSessionControlBindingOptions {
  requestClient: NexProtocolAgentServerContext["requestClient"];
  sessionId: string;
  workspacePath: string;
  workspaceIdentity?: string;
  sourceServerId?: string;
}
export interface AppSessionControlBinding {
  kind: "app";
  control: AppSessionControl;
  /** 模型工具是会话类调用方：上下文总带根会话。 */
  context(): AppSessionTrustedContext;
}
