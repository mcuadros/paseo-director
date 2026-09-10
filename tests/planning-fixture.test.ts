// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import test from "node:test";

import { bindPlanningMutation } from "../generated/planning-contract.shared.ts";
import { DeterministicPlanningFixture } from "./fixtures/planning-fixture.ts";

function query(overrides: Record<string, unknown> = {}) {
  return {
    projectId: null,
    workspaceIds: [],
    epicIds: [],
    states: [],
    priorities: [],
    labels: [],
    attention: [],
    search: null,
    sort: "scheduler_order" as const,
    cursor: null,
    pageSize: 50,
    ...overrides,
  };
}

test("the deterministic fixture has the bounded planning-scale data set", async () => {
  const fixture = new DeterministicPlanningFixture();
  assert.equal(fixture.workspaces.length, 25);
  assert.equal(fixture.openTaskCount, 500);
  assert.equal(fixture.historicalTaskCount, 10_000);
  assert.equal(fixture.tasks.length, 10_500);

  const first = await fixture.query(query());
  assert.equal(first.page.tasks.length, 50);
  assert.equal(first.page.totalTasks, "500");
  assert.match(first.page.nextCursor ?? "", /^[A-Za-z0-9_-]+$/);
  const second = await fixture.query(query({ cursor: first.page.nextCursor }));
  assert.equal(second.page.tasks.length, 50);
  assert.equal(
    new Set([...first.page.tasks, ...second.page.tasks].map(({ id }) => id)).size,
    100,
  );

  const history = await fixture.query(query({ states: ["done"] }));
  assert.equal(history.page.totalTasks, "10000");
  assert.equal(history.page.tasks.length, 50);
});

test("page cursors are bound to the exact snapshot and normalized filter", async () => {
  const fixture = new DeterministicPlanningFixture();
  const first = await fixture.query(query({ pageSize: 7 }));
  assert.ok(first.page.nextCursor);
  await assert.rejects(
    fixture.query(query({ cursor: first.page.nextCursor, pageSize: 8 })),
    /another snapshot or filter/,
  );

  const detail = await fixture.taskDetail({ taskId: "task-0", afterCursor: null });
  const action = detail.detail.summary.allowedActions.find(
    (candidate) => candidate.kind === "configuration.preview",
  );
  assert.ok(action);
  await fixture.mutate(bindPlanningMutation(action, {
    type: "configuration.preview",
    target: detail.detail.configurationTarget,
    overrides: [],
  }));
  await assert.rejects(
    fixture.query(query({ cursor: first.page.nextCursor, pageSize: 7 })),
    /another snapshot or filter/,
  );
});

test("project, workspace, epic, state, priority, label, attention, search, sort, and cursor stay adapter-driven", async () => {
  const fixture = new DeterministicPlanningFixture();
  const result = await fixture.query(query({
    projectId: "project-director",
    workspaceIds: ["workspace-0"],
    epicIds: ["epic-0"],
    states: ["done"],
    priorities: ["normal"],
    labels: ["backend"],
    search: "historical",
    sort: "key_asc",
    pageSize: 7,
  }));
  assert.ok(result.page.tasks.length > 0);
  assert.ok(result.page.tasks.length <= 7);
  for (const task of result.page.tasks) {
    assert.equal(task.projectId, "project-director");
    assert.equal(task.workspaceId, "workspace-0");
    assert.equal(task.epicId, "epic-0");
    assert.equal(task.derivedState, "done");
    assert.equal(task.priority, "normal");
    assert.deepEqual(task.labels, ["backend"]);
    assert.match(task.title, /Historical/i);
  }
  assert.deepEqual(
    result.page.tasks.map(({ key }) => key),
    [...result.page.tasks.map(({ key }) => key)].sort(),
  );
  assert.deepEqual(result.page.appliedQuery, fixture.requests.at(-1));

  const attention = await fixture.query(query({
    states: ["needs_you"],
    attention: ["policy_override_required"],
  }));
  assert.equal(attention.page.totalTasks, "1");
  assert.equal(attention.page.tasks[0]?.id, "task-0");
});

test("configuration preview/apply and dependency override preserve action bindings", async () => {
  const fixture = new DeterministicPlanningFixture();
  const detail = await fixture.taskDetail({ taskId: "task-0", afterCursor: null });
  const previewAction = detail.detail.summary.allowedActions.find(
    (action) => action.kind === "configuration.preview",
  );
  assert.ok(previewAction);
  const previewResult = await fixture.mutate(bindPlanningMutation(previewAction, {
    type: "configuration.preview",
    target: detail.detail.configurationTarget,
    overrides: [{ key: "launchPolicy", mode: "value", value: "manual" }],
  }));
  assert.equal(previewResult.status, "accepted");
  assert.equal(previewResult.preview?.diff[0]?.after, "manual");
  assert.ok(previewResult.preview?.applyAction);

  const applyResult = await fixture.mutate(bindPlanningMutation(
    previewResult.preview.applyAction,
    {
      type: "configuration.apply",
      target: previewResult.preview.target,
      previewId: previewResult.preview.previewId,
    },
  ));
  assert.equal(applyResult.status, "accepted");

  const override = detail.detail.summary.allowedActions.find(
    (action) => action.kind === "dependency.override",
  );
  assert.ok(override);
  const overrideResult = await fixture.mutate(bindPlanningMutation(override, {
    type: "dependency.override",
    taskId: detail.detail.summary.id,
    dependencyKind: "task",
    dependencyId: "dependency-0",
  }));
  assert.equal(overrideResult.status, "accepted");
  assert.equal(fixture.mutations.at(-1)?.humanApprovalRef, override.humanApprovalRef);
  assert.equal(
    fixture.mutations.at(-1)?.acknowledgementRevision,
    override.acknowledgementRevision,
  );
});
