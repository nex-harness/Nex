import type {
  EngineApprovalOutcome,
  EngineApprovalRequest,
  EngineEvent,
  PermissionMode,
} from "../types.js";
import { decideToolCall, summarizeToolCall } from "./policy.js";

export interface GateDecision {
  block: boolean;
  reason?: string;
}

export interface AuthorizeInput {
  toolCallId: string;
  toolName: string;
  input: unknown;
  /** The run's abort signal (`ctx.signal` in a pi `tool_call` handler). */
  signal: AbortSignal | undefined;
}

export interface ResolveAnswer {
  optionId?: string;
  freeText?: string;
}

export interface ResolveOutcome {
  resolved: boolean;
  outcome?: EngineApprovalOutcome;
}

interface PendingApproval {
  request: EngineApprovalRequest;
  settle: (decision: GateDecision, outcome: EngineApprovalOutcome) => void;
  signal: AbortSignal | undefined;
  onAbort: (() => void) | null;
}

const OPTION_ALLOW_ONCE = "allowOnce";
const OPTION_ALLOW_ALWAYS = "allowAlways";
const OPTION_DENY = "deny";

export interface PermissionGateOptions {
  emit: (event: EngineEvent) => void;
  now?: () => number;
  newInteractionId?: () => string;
  /**
   * 无人值守会话（automation 派发的会话）标记。见 {@link UNATTENDED_DENY_REASON}：
   * 这类会话没有用户可回答审批，`ask` 决策必须在 runner 内立即以 deny 结束。
   */
  unattended?: boolean;
}

/**
 * automation 无人值守会话的固定拒绝文案。server 侧 automations 引擎按此文案识别
 * “因需审批而终止”的一轮，因此措辞是契约，不得按工具名或模式改写。
 */
export const UNATTENDED_DENY_REASON = "approval needed: automation runs unattended";

/**
 * Per-session permission gate. It is the single writer of pending approval state: the pi
 * inline extension calls {@link authorize} for every `tool_call`, projection only mirrors
 * the `approval_request`/`approval_settled` events it emits.
 *
 * Fail-closed invariants:
 * - ask never resolves without a user decision or an abort (no timeout);
 * - abort settles the pending approval as `aborted` so the hook always returns;
 * - "allow always" is in-memory and scoped to this gate (= this session).
 */
export class PermissionGate {
  private mode: PermissionMode = "build";
  private planEnabled = false;
  private disposed = false;
  private readonly alwaysAllowed = new Set<string>();
  private readonly pending = new Map<string, PendingApproval>();
  private readonly emit: (event: EngineEvent) => void;
  private readonly now: () => number;
  private readonly newInteractionId: () => string;
  /** automation 会话：ask 直接 deny，不创建 pending promise（否则这一轮永久挂起）。 */
  private readonly unattended: boolean;
  /**
   * 本轮是否无人值守（automation 通过 sendText 派发到既有会话的那一轮）。会话级标记
   * `unattended` 只由创建/恢复路径决定，这里只覆盖单轮，见 {@link setTurnUnattended}。
   */
  private turnUnattended = false;

  constructor(options: PermissionGateOptions) {
    this.emit = options.emit;
    this.unattended = options.unattended === true;
    this.now = options.now ?? Date.now;
    this.newInteractionId =
      options.newInteractionId ??
      (() => `perm_${Math.random().toString(36).slice(2, 12)}${Date.now().toString(36)}`);
  }

  getMode(): { mode: PermissionMode; planEnabled: boolean } {
    return { mode: this.mode, planEnabled: this.planEnabled };
  }

  /**
   * 标记“即将开始的这一轮”是否无人值守。
   *
   * 修复依据（docs/next/automations-views-search.md §A）：automation 绑定既有会话时用
   * `sendText` 派发，会话本身仍是用户的普通会话。无人值守只属于 automation 那一轮：
   * 每次提交（含 rewind 重新提示）都在开跑前重设本标记，用户随后的输入自然回到有人值守，
   * 不会把会话永久改造成 automation 会话（也不写 automationId 元数据）。
   */
  setTurnUnattended(unattended: boolean): void {
    this.turnUnattended = unattended;
  }

  /** Applies the mode of one submission; returns true when the projection must be updated. */
  setMode(mode: PermissionMode, planEnabled: boolean): boolean {
    const changed = this.mode !== mode || this.planEnabled !== planEnabled;
    this.mode = mode;
    this.planEnabled = planEnabled;
    if (changed && !this.disposed) {
      this.emit({ type: "mode_changed", at: this.now(), mode, planEnabled });
    }
    return changed;
  }

  pendingApprovals(): EngineApprovalRequest[] {
    return [...this.pending.values()].map((entry) => entry.request);
  }

  isAlwaysAllowed(toolName: string): boolean {
    return this.alwaysAllowed.has(toolName);
  }

  /**
   * Called from pi's `tool_call` hook. Resolves only when the call may proceed, the user
   * denied it, or the run was aborted.
   */
  async authorize(input: AuthorizeInput): Promise<GateDecision> {
    const decision = decideToolCall(input.toolName, input.input, {
      mode: this.mode,
      planEnabled: this.planEnabled,
      alwaysAllowed: this.alwaysAllowed,
    });
    if (decision === "allow") return { block: false };
    if (decision === "deny") {
      return {
        block: true,
        reason: this.planEnabled
          ? `Plan mode does not allow ${input.toolName}. Disable plan mode to run it.`
          : `Permission denied for ${input.toolName}.`,
      };
    }
    if (this.disposed) return { block: true, reason: "The session is shutting down." };
    // 新增：automation 会话无人值守。`ask` 走到这里说明这次调用需要人工审批，但没有任何
    // 用户会回答；若落到下面的 pending promise，pi 的这一轮会一直等（stop 之外的唯一出口
    // 是 abort）。所以直接按现有 deny 形状返回 block，并固定 reason 供 server 侧识别。
    // yolo 模式在 policy 里已是 allow，不会进到这个分支；这里保护的是 build/edit 模式。
    // 不 emit approval_request：UI 不该出现一张永远无法点击的审批卡。
    // 会话级标记（automation 派发的会话）与单轮标记（automation sendText 进用户会话）
    // 都按无人值守处理；后者只覆盖 automation 那一轮。
    if (this.unattended || this.turnUnattended)
      return { block: true, reason: UNATTENDED_DENY_REASON };

    const request: EngineApprovalRequest = {
      interactionId: this.newInteractionId(),
      toolCallId: input.toolCallId,
      toolName: input.toolName,
      input: input.input,
      summary: summarizeToolCall(input.toolName, input.input),
      createdAt: this.now(),
    };
    const decisionPromise = new Promise<GateDecision>((resolve) => {
      const entry: PendingApproval = {
        request,
        signal: input.signal,
        onAbort: null,
        settle: (gateDecision) => resolve(gateDecision),
      };
      this.pending.set(request.interactionId, entry);
      const onAbort = (): void => {
        this.settle(request.interactionId, "aborted");
      };
      entry.onAbort = onAbort;
      if (input.signal) {
        if (input.signal.aborted) {
          // The run was stopped between the hook call and this registration; do not show a
          // card the user can never act on.
          queueMicrotask(onAbort);
        } else {
          input.signal.addEventListener("abort", onAbort, { once: true });
        }
      }
    });
    this.emit({ type: "approval_request", at: this.now(), approval: request });
    return decisionPromise;
  }

  /**
   * Resolves one pending approval. Unknown or already-resolved ids are an idempotent
   * no-op (returns `resolved:false`); an unknown optionId fails closed as deny.
   */
  resolve(interactionId: string, answer: ResolveAnswer): ResolveOutcome {
    const entry = this.pending.get(interactionId);
    if (!entry) return { resolved: false };
    const optionId = answer.optionId ?? OPTION_DENY;
    if (optionId === OPTION_ALLOW_ALWAYS) {
      this.alwaysAllowed.add(entry.request.toolName);
    }
    const outcome: EngineApprovalOutcome =
      optionId === OPTION_ALLOW_ONCE || optionId === OPTION_ALLOW_ALWAYS ? "allow" : "deny";
    const feedback = answer.freeText?.trim();
    const reason =
      outcome === "deny"
        ? feedback && feedback.length > 0
          ? `User denied ${entry.request.toolName}: ${feedback}`
          : `User denied ${entry.request.toolName}.`
        : undefined;
    this.settle(interactionId, outcome, reason);
    return { resolved: true, outcome };
  }

  /** Resolves every pending approval as aborted; used on abort/delete/dispose. */
  dispose(): void {
    this.disposed = true;
    this.abortPending();
  }

  /** Settles live approvals as aborted but keeps the gate usable for the next run. */
  abortPending(): void {
    // settle() removes from this.pending, so iterate over a snapshot of the keys.
    for (const interactionId of Array.from(this.pending.keys())) {
      this.settle(interactionId, "aborted");
    }
  }

  private settle(interactionId: string, outcome: EngineApprovalOutcome, reason?: string): void {
    const entry = this.pending.get(interactionId);
    if (!entry) return;
    this.pending.delete(interactionId);
    if (entry.signal && entry.onAbort) {
      entry.signal.removeEventListener("abort", entry.onAbort);
    }
    if (!this.disposed) {
      this.emit({
        type: "approval_settled",
        at: this.now(),
        interactionId,
        toolCallId: entry.request.toolCallId,
        outcome,
      });
    }
    if (outcome === "allow") {
      entry.settle({ block: false }, outcome);
      return;
    }
    entry.settle(
      {
        block: true,
        reason:
          outcome === "aborted"
            ? "Approval cancelled: the run was stopped before the tool ran."
            : (reason ?? `User denied ${entry.request.toolName}.`),
      },
      outcome,
    );
  }
}

export { OPTION_ALLOW_ALWAYS, OPTION_ALLOW_ONCE, OPTION_DENY };
