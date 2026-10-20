/** Leveled stderr logger for the runner. Never pass tokens or API keys as fields. */

export type LogLevel = "debug" | "info" | "warn" | "error";

const LEVEL_WEIGHT: Record<LogLevel, number> = { debug: 10, info: 20, warn: 30, error: 40 };

export interface Logger {
  debug(message: string, fields?: Record<string, unknown>): void;
  info(message: string, fields?: Record<string, unknown>): void;
  warn(message: string, fields?: Record<string, unknown>): void;
  error(message: string, fields?: Record<string, unknown>): void;
  child(scope: string): Logger;
}

function formatFields(fields: Record<string, unknown> | undefined): string {
  if (!fields || Object.keys(fields).length === 0) return "";
  const parts: string[] = [];
  for (const [key, value] of Object.entries(fields)) {
    parts.push(`${key}=${formatValue(value)}`);
  }
  return ` ${parts.join(" ")}`;
}

function formatValue(value: unknown): string {
  if (value instanceof Error) return JSON.stringify(value.message);
  if (typeof value === "string") return JSON.stringify(value);
  if (value === undefined) return "undefined";
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}

export function createLogger(minLevel: LogLevel = "info", scope?: string): Logger {
  const threshold = LEVEL_WEIGHT[minLevel];
  const emit = (level: LogLevel, message: string, fields?: Record<string, unknown>) => {
    if (LEVEL_WEIGHT[level] < threshold) return;
    const prefix = `[${new Date().toISOString()}] [${level}]`;
    const scopePart = scope ? ` [${scope}]` : "";
    process.stderr.write(`${prefix}${scopePart} ${message}${formatFields(fields)}\n`);
  };
  return {
    debug: (message, fields) => emit("debug", message, fields),
    info: (message, fields) => emit("info", message, fields),
    warn: (message, fields) => emit("warn", message, fields),
    error: (message, fields) => emit("error", message, fields),
    child: (childScope) => createLogger(minLevel, scope ? `${scope}.${childScope}` : childScope),
  };
}

export function parseLogLevel(value: string | undefined): LogLevel {
  return value === "debug" || value === "warn" || value === "error" ? value : "info";
}
