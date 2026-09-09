// SPDX-License-Identifier: Apache-2.0
// Pure view helpers for the Director Workers aggregate.

import type {
  DirectorWorker,
  DirectorWorkerRole,
} from "../rpc/workers.shared.ts";

export type { DirectorWorker, DirectorWorkerRole };

/** Reviewers sort before Task Agents so the review lane reads first. */
const ROLE_ORDER: readonly DirectorWorkerRole[] = ["reviewer", "task-agent"];

export function workerQueryKey(rootWorkspaceId: string): readonly string[] {
  return ["director", "workers", rootWorkspaceId];
}

export function workerRoleLabel(role: DirectorWorkerRole): string {
  return role === "reviewer" ? "Reviewer" : "Task Agent";
}

/**
 * Renders elapsed time at a human scale. The panel derives this from the
 * registered start instant on each render rather than polling the daemon, so
 * duration never becomes a second source of liveness.
 */
export function formatWorkerDuration(
  startedAt: string,
  nowMillis: number,
): string {
  const startedMillis = Date.parse(startedAt);
  if (!Number.isFinite(startedMillis)) return "—";
  const elapsedSeconds = Math.max(
    0,
    Math.floor((nowMillis - startedMillis) / 1_000),
  );
  if (elapsedSeconds < 60) return `${elapsedSeconds}s`;
  const elapsedMinutes = Math.floor(elapsedSeconds / 60);
  if (elapsedMinutes < 60) return `${elapsedMinutes}m`;
  const elapsedHours = Math.floor(elapsedMinutes / 60);
  const remainingMinutes = elapsedMinutes % 60;
  return remainingMinutes === 0
    ? `${elapsedHours}h`
    : `${elapsedHours}h ${remainingMinutes}m`;
}

export function formatWorkerCandidate(candidate: string | null): string {
  return candidate === null ? "—" : candidate.slice(0, 7);
}

/**
 * Orders the aggregate deterministically: role lane, then Task, then the
 * registered start instant, so a re-read after an event never reshuffles rows
 * that did not change.
 */
export function orderWorkers(
  workers: readonly DirectorWorker[],
): readonly DirectorWorker[] {
  return [...workers].sort((left, right) => {
    const lane =
      ROLE_ORDER.indexOf(left.role) - ROLE_ORDER.indexOf(right.role);
    if (lane !== 0) return lane;
    const task = left.taskId.localeCompare(right.taskId);
    if (task !== 0) return task;
    const started = left.startedAt.localeCompare(right.startedAt);
    return started !== 0 ? started : left.agentId.localeCompare(right.agentId);
  });
}
