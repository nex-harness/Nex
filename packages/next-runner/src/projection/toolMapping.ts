import { asNumber, asRecord, asString, jsonText as stateJsonText } from "./state.js";

export function jsonText(value: unknown): string {
  return stateJsonText(value);
}

export function mapToolName(piName: string): string {
  switch (piName) {
    case "bash":
    case "powershell":
      return "Bash";
    case "read":
      return "Read";
    case "write":
      return "Write";
    case "edit":
      return "Edit";
    case "grep":
      return "Grep";
    case "find":
      return "Glob";
    // `ls` has no canonical Nex identity; keep the pi name so the fallback card is honest.
    case "ls":
      return "ls";
    default:
      return piName;
  }
}

/**
 * Maps pi tool arguments to the UI's canonical input shapes.
 * For `edit` with multiple replacements the UI has no multi-hunk single-file renderer,
 * so the first replacement fills `old_string`/`new_string` and all originals are kept
 * in `edits` for the fallback card.
 */
export function mapToolInput(piName: string, rawArgs: unknown): unknown {
  const args = asRecord(rawArgs) ?? {};
  switch (piName) {
    case "bash":
    case "powershell":
      return { command: asString(args["command"]) ?? "" };
    case "read": {
      const input: Record<string, unknown> = {
        file_path: asString(args["path"]) ?? asString(args["file_path"]) ?? "",
      };
      const offset = asNumber(args["offset"]);
      const limit = asNumber(args["limit"]);
      if (offset !== undefined) input["offset"] = offset;
      if (limit !== undefined) input["limit"] = limit;
      return input;
    }
    case "write":
      return {
        file_path: asString(args["path"]) ?? asString(args["file_path"]) ?? "",
        content: asString(args["content"]) ?? "",
      };
    case "edit": {
      const path = asString(args["path"]) ?? asString(args["file_path"]) ?? "";
      const edits = Array.isArray(args["edits"]) ? args["edits"] : [];
      const first = asRecord(edits[0]);
      if (edits.length > 1) {
        return {
          file_path: path,
          edits: edits.map((edit) => {
            const record = asRecord(edit) ?? {};
            return {
              oldText: asString(record["oldText"]) ?? asString(record["old_string"]) ?? "",
              newText: asString(record["newText"]) ?? asString(record["new_string"]) ?? "",
            };
          }),
        };
      }
      return {
        file_path: path,
        old_string: asString(first?.["oldText"]) ?? asString(first?.["old_string"]) ?? "",
        new_string: asString(first?.["newText"]) ?? asString(first?.["new_string"]) ?? "",
      };
    }
    case "grep": {
      const input: Record<string, unknown> = { pattern: asString(args["pattern"]) ?? "" };
      const path = asString(args["path"]);
      if (path) input["path"] = path;
      const glob = asString(args["glob"]);
      if (glob) input["glob"] = glob;
      return input;
    }
    case "find": {
      const input: Record<string, unknown> = { pattern: asString(args["pattern"]) ?? "" };
      const path = asString(args["path"]);
      if (path) input["path"] = path;
      return input;
    }
    case "ls": {
      const path = asString(args["path"]);
      return path ? { path } : {};
    }
    default:
      return rawArgs ?? {};
  }
}

export function toolDisplayName(piName: string): string {
  return mapToolName(piName);
}
