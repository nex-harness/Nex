import { isReadOnlyBashCommand } from "./bashClassifier.js";

/** Submission modes that gate tool calls (spec: docs/next/permissions.md). */
export type PermissionMode = "build" | "edit" | "yolo";

export type PermissionDecision = "allow" | "ask" | "deny";

export interface PermissionPolicyContext {
  mode: PermissionMode;
  planEnabled: boolean;
  /** Tool names the user chose "Allow always" for in this session (in memory only). */
  alwaysAllowed: ReadonlySet<string>;
}

const READ_ONLY_TOOLS = new Set(["read", "grep", "find", "ls"]);

/** `plan` is the composer's plan toggle, not a permission mode; anything else is build. */
export function normalizeMode(mode: string | undefined): PermissionMode {
  return mode === "edit" || mode === "yolo" ? mode : "build";
}

function commandOf(input: unknown): string {
  if (typeof input !== "object" || input === null) return "";
  const command = (input as { command?: unknown }).command;
  return typeof command === "string" ? command : "";
}

/**
 * Policy table from the spec. Plan mode is a hard gate: reads stay available, every
 * mutating or unknown call is denied without an approval card.
 */
export function decideToolCall(
  toolName: string,
  input: unknown,
  context: PermissionPolicyContext,
): PermissionDecision {
  const readOnly =
    READ_ONLY_TOOLS.has(toolName) ||
    (toolName === "bash" && isReadOnlyBashCommand(commandOf(input)));
  if (context.planEnabled) return readOnly ? "allow" : "deny";
  if (readOnly) return "allow";
  if (context.alwaysAllowed.has(toolName)) return "allow";
  if (context.mode === "yolo") return "allow";
  if (toolName === "edit" || toolName === "write") {
    return context.mode === "edit" ? "allow" : "ask";
  }
  return "ask";
}

function stringField(input: unknown, key: string): string | undefined {
  if (typeof input !== "object" || input === null) return undefined;
  const value = (input as Record<string, unknown>)[key];
  return typeof value === "string" && value.trim().length > 0 ? value : undefined;
}

/** One-line human summary for the approval card (the payload also carries the raw input). */
export function summarizeToolCall(toolName: string, input: unknown): string {
  const command = stringField(input, "command");
  if (command) return command.length > 400 ? `${command.slice(0, 400)}…` : command;
  const path = stringField(input, "path") ?? stringField(input, "file_path");
  if (path) return `${toolName} ${path}`;
  return `Allow ${toolName} to run?`;
}
