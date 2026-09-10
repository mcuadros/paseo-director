// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import test from "node:test";

import type { PaseoApi } from "@getpaseo/client";

import { WORKER_LABEL } from "../generated/host-contract.shared.ts";
import {
  DirectorWorkerContractError,
  loadDirectorWorkers,
  projectDirectorWorker,
  type AgentDirectoryReader,
  type RegisteredAgent,
} from "../connector/engine-workers.server.ts";
import { directorWorkersRpc } from "../rpc/workers.shared.ts";
import {
  formatWorkerCandidate,
  formatWorkerDuration,
  orderWorkers,
  workerQueryKey,
  workerRoleLabel,
} from "../ui/director-workers-model.client.ts";

const ROOT_WORKSPACE = "wks_9f01d0202bcf05fa";
const CANDIDATE = "1".repeat(40);
const BASE = "2".repeat(40);

function registryLabels(
  changes: Record<string, string | undefined> = {},
): Record<string, string> {
  const labels: Record<string, string | undefined> = {
    [WORKER_LABEL.project]: "director",
    [WORKER_LABEL.rootWorkspace]: ROOT_WORKSPACE,
    [WORKER_LABEL.workspace]: "repository-1",
    [WORKER_LABEL.executionWorkspace]: "wks_execution_original",
    [WORKER_LABEL.task]: "dir-m2.17",
    [WORKER_LABEL.run]: "run-dir-m2.17-1",
    [WORKER_LABEL.role]: "task-agent",
    [WORKER_LABEL.phase]: "building",
    [WORKER_LABEL.candidate]: CANDIDATE,
    [WORKER_LABEL.base]: BASE,
    [WORKER_LABEL.registeredAt]: "2026-09-09T15:59:59Z",
    [WORKER_LABEL.startedAt]: "2026-09-09T16:00:00Z",
    ...changes,
  };
  return Object.fromEntries(
    Object.entries(labels).filter(
      (entry): entry is [string, string] => entry[1] !== undefined,
    ),
  );
}

function agent(changes: Partial<RegisteredAgent> = {}): RegisteredAgent {
  return {
    id: "agent-parentless-1",
    // The live checkout, which differs from the originally registered one.
    workspaceId: "wks_restored_active_checkout",
    status: "running",
    title: "Implement the Director delivery fast path",
    labels: registryLabels(),
    archivedAt: null,
    ...changes,
  };
}

function directory(
  pages: readonly {
    entries: readonly RegisteredAgent[];
    nextCursor?: string | null;
  }[],
  calls: unknown[] = [],
): AgentDirectoryReader {
  let index = 0;
  return async (query) => {
    calls.push(query);
    const page = pages[index];
    index += 1;
    const nextCursor = page?.nextCursor ?? null;
    return {
      entries: (page?.entries ?? []).map((agent) => ({ agent })),
      pageInfo: { nextCursor, hasMore: nextCursor !== null },
    };
  };
}

test("the root aggregate reads workers by label, not by execution workspace", async () => {
  const calls: unknown[] = [];
  const snapshot = await loadDirectorWorkers(
    directory([{ entries: [agent()] }], calls),
    ROOT_WORKSPACE,
  );

  assert.equal(snapshot.schemaVersion, 1);
  assert.equal(snapshot.rootWorkspaceId, ROOT_WORKSPACE);
  assert.deepEqual(snapshot.workers, [
    {
      agentId: "agent-parentless-1",
      // The restored live checkout, proving the aggregate survives a workspace
      // that is no longer the one recorded at registration.
      workspaceId: "wks_restored_active_checkout",
      title: "Implement the Director delivery fast path",
      status: "running",
      taskId: "dir-m2.17",
      runId: "run-dir-m2.17-1",
      role: "task-agent",
      phase: "building",
      candidate: CANDIDATE,
      base: BASE,
      registeredAt: "2026-09-09T15:59:59Z",
      startedAt: "2026-09-09T16:00:00Z",
    },
  ]);
  assert.deepEqual(calls, [
    {
      scope: "active",
      filter: {
        labels: { [WORKER_LABEL.rootWorkspace]: ROOT_WORKSPACE },
        includeArchived: false,
      },
      sort: [{ key: "created_at", direction: "asc" }],
      page: { limit: 100 },
    },
  ]);
  assert.equal(directorWorkersRpc.output.safeParse(snapshot).success, true);
});

test("a verified replacement remains visible in the recovery phase", () => {
  const projected = projectDirectorWorker(
    agent({
      id: "agent-replacement-1",
      labels: registryLabels({
        [WORKER_LABEL.phase]: "recovering",
        [WORKER_LABEL.effect]: "replacement-effect-1",
      }),
    }),
    ROOT_WORKSPACE,
  );
  assert.equal(projected?.agentId, "agent-replacement-1");
  assert.equal(projected?.phase, "recovering");
  assert.equal(projected?.role, "task-agent");
});

test("the aggregate refuses a registration it cannot trust", () => {
  const cases: readonly [string, RegisteredAgent, string][] = [
    [
      "parented",
      agent({
        labels: registryLabels({ "paseo.parent-agent-id": "agent-parent" }),
      }),
      "WORKER_PARENTED",
    ],
    [
      "archived",
      agent({ archivedAt: "2026-09-09T16:11:00Z" }),
      "WORKER_ARCHIVED",
    ],
    [
      "missing phase",
      agent({ labels: registryLabels({ [WORKER_LABEL.phase]: undefined }) }),
      "WORKER_LABEL_INVALID",
    ],
    [
      "missing run",
      agent({ labels: registryLabels({ [WORKER_LABEL.run]: undefined }) }),
      "WORKER_LABEL_INVALID",
    ],
    [
      "unknown role",
      agent({ labels: registryLabels({ [WORKER_LABEL.role]: "helper" }) }),
      "WORKER_ROLE_INVALID",
    ],
    [
      "short candidate",
      agent({ labels: registryLabels({ [WORKER_LABEL.candidate]: "abc1234" }) }),
      "WORKER_SHA_INVALID",
    ],
    [
      "unparseable registration instant",
      agent({
        labels: registryLabels({ [WORKER_LABEL.registeredAt]: "yesterday" }),
      }),
      "WORKER_TIMESTAMP_INVALID",
    ],
    [
      "no live checkout",
      agent({ workspaceId: undefined }),
      "WORKER_WORKSPACE_INVALID",
    ],
  ];
  for (const [name, value, code] of cases) {
    assert.throws(
      () => projectDirectorWorker(value, ROOT_WORKSPACE),
      (error: unknown) =>
        error instanceof DirectorWorkerContractError && error.code === code,
      name,
    );
  }
});

test("a worker registered to another root workspace is not shown here", () => {
  assert.equal(
    projectDirectorWorker(
      agent({
        labels: registryLabels({
          [WORKER_LABEL.rootWorkspace]: "wks_other_root",
        }),
      }),
      ROOT_WORKSPACE,
    ),
    null,
  );
});

test("the aggregate refuses an exact root workspace it cannot bind", async () => {
  await assert.rejects(
    loadDirectorWorkers(directory([{ entries: [] }]), "not a workspace id"),
    (error: unknown) =>
      error instanceof DirectorWorkerContractError &&
      error.code === "ROOT_WORKSPACE_INVALID",
  );
});

test("pagination is complete, bounded, and duplicate-free", async () => {
  const calls: unknown[] = [];
  const second = agent({
    id: "agent-parentless-2",
    labels: registryLabels({
      [WORKER_LABEL.role]: "reviewer",
      [WORKER_LABEL.phase]: "reviewing",
      [WORKER_LABEL.candidate]: undefined,
    }),
  });
  const snapshot = await loadDirectorWorkers(
    directory(
      [
        { entries: [agent()], nextCursor: "cursor-2" },
        { entries: [second] },
      ],
      calls,
    ),
    ROOT_WORKSPACE,
  );
  assert.deepEqual(
    snapshot.workers.map((worker) => worker.agentId),
    ["agent-parentless-1", "agent-parentless-2"],
  );
  assert.equal(snapshot.workers[1]?.candidate, null);
  assert.deepEqual((calls[1] as { page: unknown }).page, {
    limit: 100,
    cursor: "cursor-2",
  });

  await assert.rejects(
    loadDirectorWorkers(
      directory([{ entries: [agent(), agent()] }]),
      ROOT_WORKSPACE,
    ),
    (error: unknown) =>
      error instanceof DirectorWorkerContractError &&
      error.code === "WORKER_DUPLICATE",
  );

  await assert.rejects(
    loadDirectorWorkers(
      directory([{ entries: [agent()], nextCursor: "" }]),
      ROOT_WORKSPACE,
    ),
    (error: unknown) =>
      error instanceof DirectorWorkerContractError &&
      error.code === "WORKER_CURSOR_INVALID",
  );
});

test("the panel model is deterministic and free of liveness polling", () => {
  const start = Date.parse("2026-09-09T16:00:00Z");
  assert.equal(formatWorkerDuration("2026-09-09T16:00:00Z", start - 1), "0s");
  assert.equal(formatWorkerDuration("2026-09-09T16:00:00Z", start + 59_000), "59s");
  assert.equal(formatWorkerDuration("2026-09-09T16:00:00Z", start + 90_000), "1m");
  assert.equal(
    formatWorkerDuration("2026-09-09T16:00:00Z", start + 3_600_000),
    "1h",
  );
  assert.equal(
    formatWorkerDuration("2026-09-09T16:00:00Z", start + 3_900_000),
    "1h 5m",
  );
  assert.equal(formatWorkerDuration("not-an-instant", start), "—");

  assert.equal(formatWorkerCandidate(null), "—");
  assert.equal(formatWorkerCandidate(CANDIDATE), CANDIDATE.slice(0, 7));
  assert.equal(workerRoleLabel("reviewer"), "Reviewer");
  assert.equal(workerRoleLabel("task-agent"), "Task Agent");
  assert.deepEqual(workerQueryKey(ROOT_WORKSPACE), [
    "director",
    "workers",
    ROOT_WORKSPACE,
  ]);

  const taskAgent = {
    agentId: "a",
    workspaceId: "w",
    title: "t",
    status: "running",
    taskId: "dir-m2.17",
    runId: "r",
    role: "task-agent",
    phase: "building",
    candidate: null,
    base: null,
    registeredAt: "2026-09-09T16:00:00Z",
    startedAt: "2026-09-09T16:00:00Z",
  } as const;
  const reviewer = { ...taskAgent, agentId: "b", role: "reviewer" } as const;
  assert.deepEqual(
    orderWorkers([taskAgent, reviewer]).map((worker) => worker.role),
    ["reviewer", "task-agent"],
  );
});

test("the public Paseo agent directory satisfies the injected reader port", () => {
  // A compile-time guard: if Paseo v0.7 changed the agent directory shape this
  // assignment would stop type-checking, which is exactly when the connector
  // needs to be revisited rather than silently drifting.
  const reader = (paseo: PaseoApi): AgentDirectoryReader => (query) =>
    paseo.agents.list(query);
  assert.equal(typeof reader, "function");
});
