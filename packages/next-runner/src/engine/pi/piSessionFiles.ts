import { createHash } from "node:crypto";
import { readdir } from "node:fs/promises";
import { join } from "node:path";

/** pi session files live under `<dataDir>/sessions/<sha1(workspacePath)>/`. */
export function sessionDirFor(dataDir: string, workspacePath: string): string {
  const digest = createHash("sha1").update(workspacePath).digest("hex");
  return join(dataDir, "sessions", digest);
}

/** pi names files `<timestamp>_<sessionId>.jsonl`; the id is the only stable part. */
export async function findSessionFile(dir: string, sessionId: string): Promise<string | undefined> {
  const files = await readdir(dir).catch(() => [] as string[]);
  const match = files.find((name) => name.endsWith(`_${sessionId}.jsonl`));
  return match ? join(dir, match) : undefined;
}
