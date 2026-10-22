import type { AttachmentRef } from "@nex/shared/nex-protocol-v4";
import type { ProjectionState } from "../projection/state.js";
import { attachmentsOfProjection } from "./authority.js";
import { AttachmentStore } from "./store.js";
import { removeSessionAttachments } from "./helpers.js";

/**
 * Process-wide attachment state shared by the engine (which feeds pi) and the v4 runtime
 * (which owns refs and authorizes reads). One instance per runner (`main.ts` builds it).
 *
 * ```
 * projection (rows/queue) ──setListed──▶ posted refs of a session
 * live admission ──addAdmitted──▶ refs of an in-flight prompt (not a row yet)
 * engine.submit/rewind/steer ──materialize──▶ AttachmentStore.read / describe (path)
 * attachmentReadV4 ──authorize──▶ posted ⊇ ref, bytes from the store
 * sweep ──isReferenced──▶ posted ∪ admitted, else delete after 24 h
 * ```
 *
 * The store is the only blob writer; this registry is the only ref-authority keeper.
 */
export class AttachmentRegistry {
  readonly store: AttachmentStore;
  private readonly posted = new Map<string, Map<string, number>>();
  private readonly admitted = new Map<string, Map<string, number>>();

  readonly dataDir: string;

  constructor(options: { dataDir: string; now?: () => number; sessionQuotaBytes?: number }) {
    this.dataDir = options.dataDir;
    this.store = new AttachmentStore({
      dataDir: options.dataDir,
      ...(options.now ? { now: options.now } : {}),
      ...(options.sessionQuotaBytes !== undefined
        ? { sessionQuotaBytes: options.sessionQuotaBytes }
        : {}),
    });
  }

  /** The session's projection is the authority; called after every fold/publish. */
  setPosted(sessionId: string, refs: readonly AttachmentRef[]): void {
    const map = this.posted.get(sessionId) ?? new Map<string, number>();
    for (const ref of refs) if (!map.has(ref.ref)) map.set(ref.ref, Date.now());
    this.posted.set(sessionId, map);
  }

  /** Convenience for a projection (rows + queue items). */
  setPostedFromProjection(sessionId: string, state: ProjectionState): void {
    const seen = new Set<string>();
    const refs: AttachmentRef[] = [];
    for (const list of attachmentsOfProjection(state)) {
      for (const ref of list) {
        if (seen.has(ref.ref)) continue;
        seen.add(ref.ref);
        refs.push(ref);
      }
    }
    this.setPosted(sessionId, refs);
  }

  /** An accepted input's refs (queued or about to prompt); cleared when its row lands. */
  addAdmitted(sessionId: string, refs: readonly AttachmentRef[] | undefined): void {
    if (!refs || refs.length === 0) return;
    const map = this.admitted.get(sessionId) ?? new Map<string, number>();
    for (const ref of refs) if (!map.has(ref.ref)) map.set(ref.ref, Date.now());
    this.admitted.set(sessionId, map);
  }

  /** The session's admitted inputs became rows: the projection owns those refs now. */
  clearAdmitted(sessionId: string, refs?: readonly AttachmentRef[]): void {
    if (!refs || refs.length === 0) {
      this.admitted.delete(sessionId);
      return;
    }
    const map = this.admitted.get(sessionId);
    if (!map) return;
    for (const ref of refs) map.delete(ref.ref);
    if (map.size === 0) this.admitted.delete(sessionId);
  }

  clearAdmittedSession(sessionId: string): void {
    this.admitted.delete(sessionId);
  }

  /** True when this session lists the ref (posted row/queue or an admitted input). */
  isListed(sessionId: string, ref: string): boolean {
    return (
      this.posted.get(sessionId)?.has(ref) === true ||
      this.admitted.get(sessionId)?.has(ref) === true
    );
  }

  /**
   * Restore-time listing: the `nex.attachments` entries of a branch force the refs into
   * the posted set before the projection is folded (sweep protection during hydration).
   */
  adoptFromBranch(sessionId: string, refs: readonly AttachmentRef[]): void {
    this.setPosted(sessionId, refs);
  }

  /** A ref is readable only through its session's listing (read/preview authorization). */
  async authorize(sessionId: string, ref: string): Promise<boolean> {
    if (!this.isListed(sessionId, ref)) return false;
    return (await this.store.describe(ref)) !== null;
  }

  startSweeper(): void {
    this.store.startSweeper((ref) => this.isReferencedAnywhere(ref));
  }

  dispose(): void {
    this.store.stopSweeper();
  }

  async purgeSession(sessionId: string): Promise<void> {
    this.posted.delete(sessionId);
    this.admitted.delete(sessionId);
    await removeSessionAttachments(this.dataDir, sessionId);
  }

  private isReferencedAnywhere(ref: string): boolean {
    for (const map of this.posted.values()) if (map.has(ref)) return true;
    for (const map of this.admitted.values()) if (map.has(ref)) return true;
    return false;
  }
}
