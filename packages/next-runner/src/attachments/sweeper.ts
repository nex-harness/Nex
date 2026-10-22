import type { AttachmentStore } from "./store.js";

/**
 * Periodic cleanup driver (spec A cleanup): an hourly sweep of unreferenced blobs, plus the
 * staging TTL. The cadence lives here so the store stays synchronous-API sized.
 */
const SWEEP_INTERVAL_MS = 60 * 60_000;

export interface SweeperHandle {
  stop(): void;
}

export function startAttachmentSweeper(
  store: AttachmentStore,
  isReferenced: (ref: string, mtimeMs: number) => boolean,
  intervalMs = SWEEP_INTERVAL_MS,
): SweeperHandle {
  const timer = setInterval(() => {
    void store.sweep({ isReferenced }).catch(() => undefined);
  }, intervalMs);
  timer.unref?.();
  return {
    stop: () => clearInterval(timer),
  };
}
