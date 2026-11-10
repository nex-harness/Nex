import {
  collectVisibleNexBackgroundTaskControlItems,
  getNexBackgroundTaskControlItemElapsedMs,
  isActiveNexBackgroundTaskControlItem,
  parseNexBackgroundTaskControlItems,
  type NexBackgroundTaskControlItem,
  type NexBackgroundTaskControlStatus,
} from "./background-task-controls.js";

export type NexBackgroundBashJobStatus = NexBackgroundTaskControlStatus;
export type NexBackgroundBashJob = NexBackgroundTaskControlItem & {
  taskKind: "bash";
};

export function parseNexBackgroundBashJobs(value: unknown): NexBackgroundBashJob[] {
  return parseNexBackgroundTaskControlItems(value).filter(isBackgroundBashJob);
}

export function isActiveNexBackgroundBashJob(job: NexBackgroundBashJob): boolean {
  return isActiveNexBackgroundTaskControlItem(job);
}

export function getNexBackgroundBashJobElapsedMs(
  job: NexBackgroundBashJob,
  now = Date.now(),
): number {
  return getNexBackgroundTaskControlItemElapsedMs(job, now);
}

export function collectVisibleNexBackgroundBashJobs(
  jobs: readonly NexBackgroundBashJob[],
  now = Date.now(),
  thresholdMs = 30_000,
): Array<NexBackgroundBashJob & { elapsedMs: number }> {
  return collectVisibleNexBackgroundTaskControlItems(jobs, now, thresholdMs) as Array<
    NexBackgroundBashJob & { elapsedMs: number }
  >;
}

function isBackgroundBashJob(job: NexBackgroundTaskControlItem): job is NexBackgroundBashJob {
  return job.taskKind === "bash";
}
