const NEX_PROCESS_PREFIX = "nex";
const MAX_PROCESS_NAME_SEGMENT_LENGTH = 24;

function sanitizeProcessNameSegment(value: string | null | undefined): string | null {
  if (!value) {
    return null;
  }

  const normalized = value
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
  if (!normalized) {
    return null;
  }

  return normalized.slice(0, MAX_PROCESS_NAME_SEGMENT_LENGTH);
}

function joinNexProcessName(...segments: Array<string | null | undefined>): string {
  const sanitizedSegments = segments
    .map((segment) => sanitizeProcessNameSegment(segment))
    .filter((segment): segment is string => Boolean(segment));
  return [NEX_PROCESS_PREFIX, ...sanitizedSegments].join("-");
}

function pickWorkspaceTag(workspacePath: string | null | undefined): string | undefined {
  const trimmedPath = workspacePath?.trim();
  if (!trimmedPath) {
    return undefined;
  }

  const parts = trimmedPath.split(/[\\/]+/).filter(Boolean);
  return parts.at(-1) ?? trimmedPath;
}

export function formatNexMainProcessName(): string {
  return joinNexProcessName("main");
}

export function formatNexGpuProcessName(): string {
  return joinNexProcessName("gpu");
}

export function formatNexHostProcessName(label?: string): string {
  return joinNexProcessName("host", label);
}

export function formatNexRendererProcessName(windowTitle?: string): string {
  const normalizedTitle = windowTitle?.trim();
  if (!normalizedTitle || normalizedTitle === "Nex") {
    return joinNexProcessName("renderer", "main");
  }

  if (normalizedTitle === "Resource Manager") {
    return joinNexProcessName("renderer", "resource-manager");
  }

  const remoteWindowPrefix = "Nex - ";
  if (normalizedTitle.startsWith(remoteWindowPrefix)) {
    return joinNexProcessName(
      "renderer",
      "remote",
      normalizedTitle.slice(remoteWindowPrefix.length),
    );
  }

  return joinNexProcessName("renderer", normalizedTitle);
}

export function formatNexAgentProcessName(provider: string, workspacePath?: string): string {
  return joinNexProcessName("agent", provider, pickWorkspaceTag(workspacePath));
}

export function formatNexUtilityProcessName(name?: string, type = "utility"): string {
  return joinNexProcessName(type, name);
}
