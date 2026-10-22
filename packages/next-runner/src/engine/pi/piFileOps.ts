import type { Logger } from "../../logger.js";
import type {
  EngineApplyFileRewindOutcome,
  EngineEvent,
  EngineFileChanges,
  EngineFileFacts,
} from "../types.js";
import { CheckpointService } from "../checkpoints/checkpointService.js";
import { RewindStore } from "../checkpoints/rewindStore.js";
import { CHECKPOINTS_CUSTOM_TYPE } from "../checkpoints/types.js";
import type { OpenSession } from "./sessionLifecycle.js";
import {
  applyOf,
  factsOf,
  fileChangesOf,
  previewOf,
  type FileFactsDeps,
} from "./piFileFactsOps.js";
import { PiCheckpointHook } from "./piCheckpointHook.js";

/**
 * The engine's file surface (spec B): one checkpoint service per workspace, the git-backed
 * reads (changed files, rewind preview/apply), and the per-session checkpoint hook factory.
 * Kept out of `PiEngine` for the file budget; ownership of the session map stays there.
 */
export interface PiFileOpsDeps {
  dataDir: string;
  logger: Logger;
  gitExecutable?: string;
  maxSnapshotFiles?: number;
  requireOpen: (sessionId: string) => OpenSession;
  openFor: (sessionId: string) => OpenSession | undefined;
  emit: (sessionId: string, event: EngineEvent) => void;
}

export class PiFileOps {
  private readonly checkpoints = new Map<string, CheckpointService>();
  private readonly rewindStores = new Map<string, RewindStore>();

  constructor(private readonly deps: PiFileOpsDeps) {}

  /** Checkpoint service of a workspace (created on first use; never throws). */
  service(workspacePath: string): CheckpointService {
    let service = this.checkpoints.get(workspacePath);
    if (!service) {
      service = new CheckpointService(workspacePath, {
        dataDir: this.deps.dataDir,
        logger: this.deps.logger,
        ...(this.deps.gitExecutable ? { gitExecutable: this.deps.gitExecutable } : {}),
        ...(this.deps.maxSnapshotFiles !== undefined
          ? { maxSnapshotFiles: this.deps.maxSnapshotFiles }
          : {}),
      });
      this.checkpoints.set(workspacePath, service);
    }
    return service;
  }

  /** Preview/apply side of the checkpoints (changed files, rewind). */
  rewindStore(workspacePath: string): RewindStore {
    let store = this.rewindStores.get(workspacePath);
    if (!store) {
      store = new RewindStore(this.service(workspacePath), this.deps.logger);
      this.rewindStores.set(workspacePath, store);
    }
    return store;
  }

  facts(workspacePath: string, sessionId: string, userEntryId: string): Promise<EngineFileFacts> {
    return factsOf(this.fileDeps(workspacePath), this.deps.requireOpen(sessionId), userEntryId);
  }

  changes(
    workspacePath: string,
    sessionId: string,
    userEntryId: string,
  ): Promise<EngineFileChanges> {
    return fileChangesOf(
      this.fileDeps(workspacePath),
      this.deps.requireOpen(sessionId),
      userEntryId,
    );
  }

  preview(workspacePath: string, sessionId: string, userEntryId: string) {
    return previewOf(this.fileDeps(workspacePath), this.deps.requireOpen(sessionId), userEntryId);
  }

  apply(
    workspacePath: string,
    sessionId: string,
    userEntryId: string,
    options: { commitAfterApply?: () => Promise<void>; sourceCommandId?: string },
  ): Promise<EngineApplyFileRewindOutcome> {
    return applyOf(
      this.fileDeps(workspacePath),
      this.deps.requireOpen(sessionId),
      userEntryId,
      options,
    );
  }

  enabled(workspacePath: string): Promise<boolean> {
    return this.service(workspacePath).available();
  }

  /** Per-session checkpoint hook; `undefined` while git is unavailable (Undo stays hidden). */
  checkpointHookFor(
    workspacePath: string,
    sessionIdRef: { readonly current: string },
  ): PiCheckpointHook | undefined {
    const service = this.service(workspacePath);
    if (service.disabled) return undefined;
    return new PiCheckpointHook({
      service,
      logger: this.deps.logger,
      persistCheckpoints: async (data) => {
        this.deps
          .openFor(sessionIdRef.current)
          ?.sessionManager.appendCustomEntry(CHECKPOINTS_CUSTOM_TYPE, data);
      },
      emit: (event) => this.deps.emit(sessionIdRef.current, event),
    });
  }

  private fileDeps(workspacePath: string): FileFactsDeps {
    return { checkpoints: this.service(workspacePath), rewind: this.rewindStore(workspacePath) };
  }
}
