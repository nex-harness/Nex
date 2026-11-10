import type { NexBackgroundTaskControlItem } from "./background-task-controls.js";

export function mergeNexBackgroundTaskControlItems(
  current: readonly NexBackgroundTaskControlItem[],
  updates: readonly NexBackgroundTaskControlItem[],
): NexBackgroundTaskControlItem[] {
  const jobsById = new Map(current.map((job) => [job.jobId, job] as const));
  for (const job of updates) {
    jobsById.set(job.jobId, job);
  }
  return Array.from(jobsById.values());
}
