import type { AttachmentRef, ModelSelection } from "@nex/shared/nex-protocol-v4";

/**
 * createSession envelope 的 payload 形状（runner 侧读取视图）。单独成文件是为了让
 * `commands.ts` 留在文件预算内；字段仍以 shared 的 command schema 为权威。
 */
export interface CreateSessionPayload {
  firstInput?: {
    text?: string;
    /** Admitted attachment refs (spec A); registered before the prompt starts. */
    attachments?: AttachmentRef[];
    modelSelection?: ModelSelection;
    mode?: string;
    planEnabled?: boolean;
    /**
     * automations 引擎派发的自动化 id（可选）。runner 据此把会话标记为无人值守
     * （审批一律 deny）并写入会话元数据（侧栏 cron 徽章）。
     */
    automationId?: string;
  };
  config?: { modelSelection?: ModelSelection; mode?: string; planEnabled?: boolean };
}
