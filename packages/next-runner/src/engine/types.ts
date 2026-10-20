import type { ModelSelection } from "@nex/shared";

/**
 * Engine boundary. The rest of the runner (projection, publishers, channels) only sees
 * these types; `engine/pi/` is the single implementation that touches the pi SDK.
 * Keeping this narrow is what makes a later pi-durable swap possible.
 */

export type EnginePhase =
  | "draft"
  | "prewarming"
  | "running"
  | "completedSuccess"
  | "completedInterrupted"
  | "error";

export type EngineTitleSource = "default" | "generated" | "custom";

export interface EngineUsage {
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
}

export interface EngineModelState {
  providerId: string;
  modelId: string;
  displayName?: string;
  thought: string;
  thoughtLevels: string[];
}

export interface EngineSessionState {
  sessionId: string;
  workspacePath: string;
  title: string;
  titleSource: EngineTitleSource;
  createdAt: number;
  lastActivityAt: number;
  phase: EnginePhase;
  model: EngineModelState | null;
  usage: EngineUsage;
  isRunning: boolean;
}

/** Normalized transcript used for restore; mirrors the pi message shapes we consume. */
export type EngineMessage =
  | { role: "user"; at: number; text: string }
  | {
      role: "assistant";
      at: number;
      model?: string;
      usage?: EngineUsage;
      stopReason: EngineStopReason;
      errorMessage?: string;
      parts: EngineAssistantPart[];
    }
  | {
      role: "toolResult";
      at: number;
      toolCallId: string;
      toolName: string;
      text: string;
      isError: boolean;
      durationMs?: number;
    };

export type EngineAssistantPart =
  | { type: "text"; text: string }
  | { type: "thinking"; text: string }
  | { type: "toolCall"; id: string; name: string; arguments: unknown };

export type EngineStopReason =
  | "pending"
  | "stop"
  | "length"
  | "toolUse"
  | "error"
  | "aborted"
  | "deferred";

/**
 * Live engine events. Deliberately close to pi's AgentSessionEvent but stripped of
 * cumulative `partial` payloads, so projection stays pure and cheap.
 */
export type EngineEvent =
  | { type: "user_input"; at: number; text: string }
  | { type: "assistant_start"; at: number; model?: string }
  | { type: "text_delta"; at: number; delta: string }
  | { type: "thinking_delta"; at: number; delta: string }
  | { type: "tool_call_start"; at: number; toolCallId: string; toolName: string; input?: unknown }
  | {
      type: "tool_input_delta";
      at: number;
      toolCallId: string;
      toolName: string;
      delta: string;
    }
  | {
      type: "tool_call_update";
      at: number;
      toolCallId: string;
      toolName: string;
      text?: string;
    }
  | {
      type: "tool_call_end";
      at: number;
      toolCallId: string;
      toolName: string;
      text: string;
      isError: boolean;
      input?: unknown;
    }
  | {
      type: "assistant_end";
      at: number;
      stopReason: EngineStopReason;
      errorMessage?: string;
      model?: string;
      usage?: EngineUsage;
    }
  | { type: "settled"; at: number; aborted: boolean }
  | { type: "phase"; at: number; phase: EnginePhase }
  | { type: "error"; at: number; message: string };

export interface EngineModelInfo {
  providerId: string;
  providerName: string;
  modelId: string;
  modelName: string;
  apiType: "anthropic-messages" | "openai-chat-completions" | "openai-responses";
  baseUrl: string;
  reasoningLevels: string[];
  maxOutputTokens: number;
  contextWindow: number;
  supportsImage: boolean;
  supportsVideo: boolean;
  supportsAudio: boolean;
  supportsPdf: boolean;
}

export interface EngineCatalog {
  models: EngineModelInfo[];
  defaultSelection: ModelSelection | null;
}

export interface CreateEngineSessionOptions {
  modelSelection?: ModelSelection;
}

export interface SubmitOptions {
  requestId: string;
  modelSelection?: ModelSelection;
}

export interface SubmitOutcome {
  status: "accepted" | "duplicate";
  inputId: string;
}

export interface EngineEventSubscription {
  dispose(): void;
}

export interface Engine {
  init(): Promise<void>;

  /** Creates a pi session immediately (draft prewarm relies on this). */
  createSession(
    workspacePath: string,
    options: CreateEngineSessionOptions,
  ): Promise<{ sessionId: string; inputId?: string }>;

  /** Lazy restore from disk. Idempotent for an already-open session. */
  openSession(workspacePath: string, sessionId: string): Promise<void>;

  deleteSession(workspacePath: string, sessionId: string): Promise<void>;

  /** Non-deleted sessions, newest activity first. */
  listSessions(workspacePath: string): Promise<EngineSessionState[]>;

  /** Tombstoned session ids (deleted, kept for the sidebar's negative membership join). */
  listDeletedSessionIds(workspacePath: string): Promise<string[]>;

  history(sessionId: string): Promise<EngineMessage[]>;

  subscribe(sessionId: string, listener: (event: EngineEvent) => void): EngineEventSubscription;

  submit(sessionId: string, text: string, options: SubmitOptions): Promise<SubmitOutcome>;

  abort(sessionId: string): Promise<void>;

  rename(sessionId: string, title: string): Promise<void>;

  setModel(sessionId: string, selection: ModelSelection | undefined): Promise<void>;

  catalog(): Promise<EngineCatalog>;

  /** Fires when session metadata (create/rename/delete/phase/title) changes. */
  onSessionsChanged(listener: (workspacePath: string) => void): () => void;

  dispose(): Promise<void>;
}
