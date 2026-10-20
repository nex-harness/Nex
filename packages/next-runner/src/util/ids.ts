import { randomBytes, randomUUID } from "node:crypto";

/** Short opaque id, safe in JSON and topic strings. */
export function randomId(prefix: string): string {
  return `${prefix}_${randomUUID().replaceAll("-", "").slice(0, 20)}`;
}

/** Per-process log epoch for one session; a restart legitimately starts a new epoch. */
export function newLogEpoch(): string {
  return `epoch-${randomBytes(8).toString("hex")}`;
}
