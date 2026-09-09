// SPDX-License-Identifier: Apache-2.0
// Policy-free Paseo adapter for the root-workspace Director Workers aggregate.

import {
  WORKER_LABEL,
  WORKER_REGISTRY_SCHEMA_VERSION,
  WORKER_ROLES,
  type WorkerRole,
} from "../generated/host-contract.shared.ts";
import {
  WORKER_MAXIMUM,
  WORKER_PAGE_LIMIT,
  type DirectorWorker,
  type DirectorWorkersSnapshot,
} from "../rpc/workers.shared.ts";

/**
 * Paseo owns agent parentage as a public fact. A Director-launched Task Agent
 * or Reviewer is top-level, so a registry label claiming a parent is a forged
 * or mis-registered worker and the aggregate refuses it rather than showing it.
 */
const PARENT_LABEL = "paseo.parent-agent-id";

const IDENTITY_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,199}$/u;
const PHASE_PATTERN = /^[a-z][a-z0-9_]{0,63}$/u;
const SHA_PATTERN = /^[0-9a-f]{40}$/u;
const INSTANT_PATTERN =
  /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/u;

export class DirectorWorkerContractError extends Error {
  readonly code: string;

  constructor(code: string, message: string) {
    super(message);
    this.name = "DirectorWorkerContractError";
    this.code = code;
  }
}

/**
 * The minimum agent-directory facts this aggregate reads. Declaring the shape
 * structurally keeps the Paseo SDK behind its single connector entry point
 * while the reader stays exactly typed at the call site.
 */
export type RegisteredAgent = {
  readonly id: string;
  readonly workspaceId?: string | undefined;
  readonly status: "initializing" | "idle" | "running" | "error" | "closed";
  readonly title: string | null;
  readonly labels: Readonly<Record<string, string>>;
  readonly archivedAt?: string | null | undefined;
};

export type WorkerDirectoryQuery = {
  scope: "active";
  filter: { labels: Record<string, string>; includeArchived: false };
  sort: { key: "created_at"; direction: "asc" }[];
  page: { limit: number; cursor?: string };
};

export type WorkerDirectoryPage = {
  readonly entries: readonly { readonly agent: RegisteredAgent }[];
  readonly pageInfo: {
    readonly nextCursor: string | null;
    readonly hasMore: boolean;
  };
};

/** Injected by the composition root over the public Paseo agent directory. */
export type AgentDirectoryReader = (
  query: WorkerDirectoryQuery,
) => Promise<WorkerDirectoryPage>;

function requiredLabel(
  labels: Readonly<Record<string, string>>,
  key: string,
): string {
  const value = labels[key];
  if (value === undefined || !IDENTITY_PATTERN.test(value)) {
    throw new DirectorWorkerContractError(
      "WORKER_LABEL_INVALID",
      `Director worker registry label ${key} is absent or invalid`,
    );
  }
  return value;
}

function requiredInstant(
  labels: Readonly<Record<string, string>>,
  key: string,
): string {
  const value = labels[key];
  if (
    value === undefined ||
    value.length > 64 ||
    !INSTANT_PATTERN.test(value) ||
    !Number.isFinite(Date.parse(value))
  ) {
    throw new DirectorWorkerContractError(
      "WORKER_TIMESTAMP_INVALID",
      `Director worker registry timestamp ${key} is absent or invalid`,
    );
  }
  return value;
}

function optionalSha(
  labels: Readonly<Record<string, string>>,
  key: string,
): string | null {
  const value = labels[key];
  if (value === undefined) return null;
  if (!SHA_PATTERN.test(value)) {
    throw new DirectorWorkerContractError(
      "WORKER_SHA_INVALID",
      `Director worker registry label ${key} is not an exact commit`,
    );
  }
  return value;
}

/**
 * Projects one registered agent onto the owner-visible worker record. Returns
 * null only when the agent belongs to a different root workspace; every other
 * inconsistency fails closed so an incomplete registration can never be
 * displayed as a healthy worker.
 */
export function projectDirectorWorker(
  agent: RegisteredAgent,
  rootWorkspaceId: string,
): DirectorWorker | null {
  const labels: Readonly<Record<string, string>> = agent.labels ?? {};
  if (labels[WORKER_LABEL.rootWorkspace] !== rootWorkspaceId) return null;
  if (agent.archivedAt) {
    throw new DirectorWorkerContractError(
      "WORKER_ARCHIVED",
      "The active Director Workers query returned an archived agent",
    );
  }
  if (labels[PARENT_LABEL] !== undefined && labels[PARENT_LABEL] !== "") {
    throw new DirectorWorkerContractError(
      "WORKER_PARENTED",
      "A Director Task Agent or Reviewer must be top-level",
    );
  }
  const role = requiredLabel(labels, WORKER_LABEL.role);
  if (!WORKER_ROLES.includes(role as WorkerRole)) {
    throw new DirectorWorkerContractError(
      "WORKER_ROLE_INVALID",
      "Director worker role is outside the launch registry contract",
    );
  }
  const phase = requiredLabel(labels, WORKER_LABEL.phase);
  if (!PHASE_PATTERN.test(phase)) {
    throw new DirectorWorkerContractError(
      "WORKER_PHASE_INVALID",
      "Director worker phase is invalid",
    );
  }
  if (!IDENTITY_PATTERN.test(agent.id)) {
    throw new DirectorWorkerContractError(
      "WORKER_AGENT_INVALID",
      "Director worker has no valid native agent identity",
    );
  }
  // The current checkout is a live Paseo fact, not a caller-supplied label, so
  // a restored or renamed execution workspace still resolves correctly.
  const workspaceId = agent.workspaceId;
  if (workspaceId === undefined || !IDENTITY_PATTERN.test(workspaceId)) {
    throw new DirectorWorkerContractError(
      "WORKER_WORKSPACE_INVALID",
      "Director worker has no active checkout workspace identity",
    );
  }
  const taskId = requiredLabel(labels, WORKER_LABEL.task);
  const title = agent.title ?? "";
  return {
    agentId: agent.id,
    workspaceId,
    title: title.length > 0 && title.length <= 512 ? title : taskId,
    status: agent.status,
    taskId,
    runId: requiredLabel(labels, WORKER_LABEL.run),
    role: role as WorkerRole,
    phase,
    candidate: optionalSha(labels, WORKER_LABEL.candidate),
    base: optionalSha(labels, WORKER_LABEL.base),
    registeredAt: requiredInstant(labels, WORKER_LABEL.registeredAt),
    startedAt: requiredInstant(labels, WORKER_LABEL.startedAt),
  };
}

/**
 * Reads every live parentless worker registered to one root workspace. The
 * label filter is a public Paseo v0.7 agent-directory capability, so the
 * aggregate never joins through an execution-workspace card and never depends
 * on the worker's own checkout remaining present.
 */
export async function loadDirectorWorkers(
  readAgents: AgentDirectoryReader,
  rootWorkspaceId: string,
): Promise<DirectorWorkersSnapshot> {
  if (!IDENTITY_PATTERN.test(rootWorkspaceId)) {
    throw new DirectorWorkerContractError(
      "ROOT_WORKSPACE_INVALID",
      "Director Workers requires an exact root workspace identity",
    );
  }
  const workers: DirectorWorker[] = [];
  const seen = new Set<string>();
  let cursor: string | undefined;
  do {
    const response = await readAgents({
      scope: "active",
      filter: {
        labels: { [WORKER_LABEL.rootWorkspace]: rootWorkspaceId },
        includeArchived: false,
      },
      sort: [{ key: "created_at", direction: "asc" }],
      page: { limit: WORKER_PAGE_LIMIT, ...(cursor ? { cursor } : {}) },
    });
    for (const { agent } of response.entries) {
      if (seen.has(agent.id)) {
        throw new DirectorWorkerContractError(
          "WORKER_DUPLICATE",
          "Director Workers received a duplicate native agent identity",
        );
      }
      seen.add(agent.id);
      const worker = projectDirectorWorker(agent, rootWorkspaceId);
      if (worker === null) continue;
      workers.push(worker);
      if (workers.length > WORKER_MAXIMUM) {
        throw new DirectorWorkerContractError(
          "WORKER_LIMIT_EXCEEDED",
          "Director Workers exceeded its bounded result set",
        );
      }
    }
    if (!response.pageInfo.hasMore) break;
    // A page that promises more entries must hand back a usable cursor.
    // Accepting an absent or empty one would silently truncate the aggregate
    // and hide a running worker from its owner.
    const nextCursor = response.pageInfo.nextCursor;
    if (typeof nextCursor !== "string" || nextCursor.length === 0) {
      throw new DirectorWorkerContractError(
        "WORKER_CURSOR_INVALID",
        "Director Workers pagination is incomplete",
      );
    }
    cursor = nextCursor;
  } while (true);
  return {
    schemaVersion: WORKER_REGISTRY_SCHEMA_VERSION,
    rootWorkspaceId,
    workers,
  };
}
