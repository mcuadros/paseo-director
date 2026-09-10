// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import {
  bindPlanningMutation,
  configurationPreviewSchema,
  PLANNING_ALLOWED_ACTIONS,
  PLANNING_ATTENTION_CODES,
  PLANNING_CONFIGURATION_KEYS,
  PLANNING_CONTRACT_SHA256,
  PLANNING_CONTRACT_VERSION,
  PLANNING_DERIVED_STATES,
  PLANNING_PRIORITIES,
  PLANNING_SCHEMA_VERSION,
  PLANNING_STABLE_SORTS,
  planningMutationInputSchema,
  planningQueryInputSchema,
  planningSnapshotSchema,
  taskDetailSnapshotSchema,
  type AllowedAction,
} from "../generated/planning-contract.shared.ts";
import {
  planningMutationRpc,
  planningQueryRpc,
  planningTaskDetailRpc,
} from "../rpc/planning.shared.ts";
import { DeterministicPlanningFixture } from "./fixtures/planning-fixture.ts";

const query = {
  projectId: null,
  workspaceIds: [],
  epicIds: [],
  states: [],
  priorities: [],
  labels: [],
  attention: [],
  search: null,
  sort: "scheduler_order",
  cursor: null,
  pageSize: 50,
} as const;

test("planning identity and vocabularies are closed and versioned", () => {
  assert.equal(PLANNING_SCHEMA_VERSION, 1);
  assert.equal(PLANNING_CONTRACT_VERSION, "director-planning/v1");
  assert.match(PLANNING_CONTRACT_SHA256, /^[0-9a-f]{64}$/);
  assert.deepEqual(PLANNING_DERIVED_STATES, [
    "needs_you",
    "queued",
    "building",
    "validating",
    "in_review",
    "ready",
    "done",
  ]);
  assert.deepEqual(PLANNING_PRIORITIES, ["urgent", "high", "normal", "low"]);
  assert.deepEqual(PLANNING_STABLE_SORTS, [
    "scheduler_order",
    "updated_desc",
    "priority_fifo",
    "key_asc",
  ]);
  assert.equal(PLANNING_ALLOWED_ACTIONS.length, 17);
  assert.equal(PLANNING_ATTENTION_CODES.length, 12);
  assert.equal(PLANNING_CONFIGURATION_KEYS.length, 7);
});

test("generated query, detail, and mutation RPC schemas reject drift", async () => {
  const fixture = new DeterministicPlanningFixture();
  const snapshot = await fixture.query(query);
  const detail = await fixture.taskDetail({ taskId: "task-0", afterCursor: null });
  assert.equal(planningSnapshotSchema.safeParse(snapshot).success, true);
  assert.equal(taskDetailSnapshotSchema.safeParse(detail).success, true);
  assert.equal(planningQueryRpc.input.safeParse(query).success, true);
  assert.equal(planningQueryRpc.output.safeParse(snapshot).success, true);
  assert.equal(planningTaskDetailRpc.output.safeParse(detail).success, true);

  for (const drift of [
    { ...query, extra: true },
    { ...query, cursor: 2 },
    { ...query, cursor: "bad.cursor" },
    { ...query, cursor: "x".repeat(5501) },
    { ...query, pageSize: 101 },
    { ...query, states: ["client-invented"] },
  ]) {
    assert.equal(planningQueryInputSchema.safeParse(drift).success, false);
  }
  assert.equal(
    planningSnapshotSchema.safeParse({ ...snapshot, extra: true }).success,
    false,
  );
  assert.equal(
    planningSnapshotSchema.safeParse({ ...snapshot, cursor: 1 }).success,
    false,
  );
  assert.equal(
    planningSnapshotSchema.safeParse({
      ...snapshot,
      page: {
        ...snapshot.page,
        tasks: [{ ...snapshot.page.tasks[0], derivedState: "client-invented" }],
      },
    }).success,
    false,
  );
});

test("every mutation is bound to an engine-returned action ticket", async () => {
  const fixture = new DeterministicPlanningFixture();
  const detail = await fixture.taskDetail({ taskId: "task-0", afterCursor: null });
  const launch = detail.detail.summary.allowedActions.find(
    (action) => action.kind === "configuration.preview",
  );
  assert.ok(launch);
  const mutation = bindPlanningMutation(launch, {
    type: "configuration.preview",
    target: detail.detail.configurationTarget,
    overrides: detail.detail.configuration.map((entry) => entry.configured),
  });
  assert.equal(mutation.requestId, launch.requestId);
  assert.equal(mutation.idempotencyKey, launch.idempotencyKey);
  assert.equal(mutation.expectedVersion, launch.expectedVersion);
  assert.equal(planningMutationRpc.input.safeParse(mutation).success, true);

  assert.throws(() =>
    bindPlanningMutation(launch, {
      type: "task.launch-now",
      taskId: detail.detail.summary.id,
    }),
  );
  assert.throws(() =>
    bindPlanningMutation(launch, {
      type: "configuration.preview",
      target: { scope: "task", id: "another-task" },
      overrides: [],
    }),
  );
  assert.equal(
    planningMutationInputSchema.safeParse({ ...mutation, expectedVersion: 1 }).success,
    false,
  );
  assert.equal(
    planningMutationInputSchema.safeParse({ ...mutation, extra: true }).success,
    false,
  );

  const unapprovedApply: AllowedAction = {
    ...launch,
    kind: "configuration.apply",
    humanApprovalRef: null,
    acknowledgementRevision: null,
  };
  assert.throws(() =>
    bindPlanningMutation(unapprovedApply, {
      type: "configuration.apply",
      target: detail.detail.configurationTarget,
      previewId: "preview-1",
    }),
  );

  const previewResult = await fixture.mutate(mutation);
  assert.ok(previewResult.preview?.applyAction);
  assert.equal(
    configurationPreviewSchema.safeParse({
      ...previewResult.preview,
      applyAction: { ...previewResult.preview.applyAction, kind: "task.launch-now" },
    }).success,
    false,
  );
});

test("the engine schema closes every object and is the generated source", () => {
  const schema = JSON.parse(
    readFileSync("engine/ports/planning/planning-surface.v1.json", "utf8"),
  ) as Record<string, unknown>;
  const open: string[] = [];
  function inspect(value: unknown, path: string) {
    if (Array.isArray(value)) {
      value.forEach((child, index) => inspect(child, `${path}/${index}`));
      return;
    }
    if (value === null || typeof value !== "object") return;
    const record = value as Record<string, unknown>;
    if (record.type === "object" && record.additionalProperties !== false) {
      open.push(path);
    }
    for (const [key, child] of Object.entries(record)) inspect(child, `${path}/${key}`);
  }
  inspect(schema, "$");
  assert.deepEqual(open, []);
  for (const definition of [
    "projectSummary",
    "workspaceSummary",
    "epicSummary",
    "taskSummary",
    "taskDetail",
    "planningPage",
    "planningSnapshot",
    "planningQueryInput",
    "planningMutationInput",
    "planningMutationResult",
  ]) {
    assert.ok((schema.$defs as Record<string, unknown>)[definition], definition);
  }
  const generated = readFileSync("generated/planning-contract.shared.ts", "utf8");
  assert.match(generated, /Code generated by Director Engine\. DO NOT EDIT\./);
  assert.match(generated, new RegExp(PLANNING_CONTRACT_SHA256));
});

test("the stable v0.7 plugin registers strict planning RPCs without runtime fixtures", () => {
  const entry = readFileSync("index.ts", "utf8");
  for (const registration of [
    "plugin.handle(planningQueryRpc",
    "plugin.handle(planningTaskDetailRpc",
    "plugin.handle(planningMutationRpc",
  ]) {
    assert.ok(entry.includes(registration), registration);
  }
  const connector = readFileSync("connector/paseo.server.ts", "utf8");
  assert.match(connector, /return this\.#planningTransport\.query\(input\)/);
  assert.match(connector, /runtime task-detail queries are owned by later M2 Tasks/);
  assert.doesNotMatch(connector, /tests\/fixtures|DeterministicPlanningFixture/);
  const client = readFileSync("ui/planning-surface.client.tsx", "utf8");
  for (const moduleName of [
    "@getpaseo/plugin",
    "@getpaseo/plugin/react-native",
    "@tanstack/react-query",
    "react",
    "react-native",
  ]) {
    assert.ok(client.includes(`from \"${moduleName}\"`), moduleName);
  }
  assert.doesNotMatch(client, /@getpaseo\/client|node:|window\.|document\./);
});
