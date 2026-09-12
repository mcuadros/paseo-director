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
  homeQueryInputSchema,
  homeSnapshotSchema,
  doctorQueryInputSchema,
  doctorReportSchema,
  organizerBootstrapInputSchema,
  organizerBootstrapResultSchema,
  repairInputSchema,
  repairResultSchema,
  operationsQueryInputSchema,
  operationsReportSchema,
  operationsMutationInputSchema,
  operationsMutationResultSchema,
  type AllowedAction,
} from "../generated/planning-contract.shared.ts";
import {
  planningMutationRpc,
  planningQueryRpc,
  planningTaskDetailRpc,
  homeQueryRpc,
  doctorQueryRpc,
  organizerBootstrapRpc,
  repairProjectRpc,
  operationsQueryRpc,
  operationsMutationRpc,
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

const taskDetailQuery = {
  hostId: "host-a",
  context: "board",
  taskId: "task-0",
  paseoWorkspaceId: null,
  paseoAgentId: null,
  afterCursor: null,
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
  assert.equal(PLANNING_CONFIGURATION_KEYS.length, 8);
  assert.ok(PLANNING_CONFIGURATION_KEYS.includes("requireDifferentReviewerModel"));
});

test("operations contract fixes exact host scope, manual support confirmation, and closed safe output", () => {
  const queryInput = { hostId: "host-a", projectId: "project-a", expectedProjectVersion: "3" };
  assert.equal(operationsQueryInputSchema.safeParse(queryInput).success, true);
  assert.equal(operationsQueryRpc.input.safeParse(queryInput).success, true);
  assert.equal(operationsQueryInputSchema.safeParse({ ...queryInput, path: "/home/private" }).success, false);
  assert.equal(operationsReportSchema.safeParse({}).success, false);

  const base = {
    schemaVersion: PLANNING_SCHEMA_VERSION,
    contractVersion: PLANNING_CONTRACT_VERSION,
    contractHash: PLANNING_CONTRACT_SHA256,
    hostId: "host-a",
    requestId: "operations-request-0001",
    projectId: "project-a",
    expectedProjectVersion: "3",
  };
  const preview = { ...base, kind: "support.preview", previewId: null, confirmed: false };
  const previewId = "a".repeat(64);
  const generate = { ...base, kind: "support.generate", previewId, confirmed: true };
  const sync = { ...base, kind: "sync.now", previewId: null, confirmed: true };
  assert.equal(operationsMutationInputSchema.safeParse(preview).success, true);
  assert.equal(operationsMutationInputSchema.safeParse(generate).success, true);
  assert.equal(operationsMutationRpc.input.safeParse(sync).success, true);
  assert.equal(operationsMutationInputSchema.safeParse({ ...generate, confirmed: false }).success, false);
  assert.equal(operationsMutationInputSchema.safeParse({ ...sync, previewId }).success, false);
  assert.equal(operationsMutationResultSchema.safeParse({}).success, false);
  assert.equal(operationsQueryRpc.output, operationsReportSchema);
  assert.equal(operationsMutationRpc.output, operationsMutationResultSchema);
});

test("generated query, detail, and mutation RPC schemas reject drift", async () => {
  const fixture = new DeterministicPlanningFixture();
  const snapshot = await fixture.query(query);
  const detail = await fixture.taskDetail(taskDetailQuery);
  assert.equal(planningSnapshotSchema.safeParse(snapshot).success, true);
  assert.equal(taskDetailSnapshotSchema.safeParse(detail).success, true);
  assert.equal(planningQueryRpc.input.safeParse(query).success, true);
  assert.equal(planningQueryRpc.output.safeParse(snapshot).success, true);
  assert.equal(planningTaskDetailRpc.output.safeParse(detail).success, true);
  assert.equal(planningTaskDetailRpc.input.safeParse(taskDetailQuery).success, true);
  assert.equal(planningTaskDetailRpc.input.safeParse({ ...taskDetailQuery, hostId: "" }).success, false);
  assert.equal(planningTaskDetailRpc.input.safeParse({ ...taskDetailQuery, context: "agent" }).success, false);

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

test("generated Home and Organizer contracts reject cross-host and Preview/Apply drift", () => {
  assert.equal(homeQueryInputSchema.safeParse({ hostId: "host-a", cursor: null, pageSize: 50 }).success, true);
  assert.equal(homeQueryInputSchema.safeParse({ hostId: "host-a", cursor: null, pageSize: 51 }).success, false);
  assert.equal(homeQueryRpc.input.safeParse({ hostId: "host-a", cursor: null, pageSize: 25 }).success, true);
  assert.equal(homeSnapshotSchema.safeParse({}).success, false);

  const base = {
    schemaVersion: PLANNING_SCHEMA_VERSION,
    contractVersion: PLANNING_CONTRACT_VERSION,
    contractHash: PLANNING_CONTRACT_SHA256,
    hostId: "host-a",
    requestId: "request-organizer-0001",
    projectId: "project-a",
    projectName: "Project A",
    repositoryPath: "/srv/director/project-a",
  } as const;
  const preview = { ...base, kind: "create.preview", configurationJson: "{}", previewId: null };
  assert.equal(organizerBootstrapInputSchema.safeParse(preview).success, true);
  assert.equal(organizerBootstrapRpc.input.safeParse(preview).success, true);
  assert.equal(organizerBootstrapInputSchema.safeParse({ ...preview, confirmed: true }).success, false);
  assert.equal(organizerBootstrapInputSchema.safeParse({ ...preview, configurationJson: null }).success, false);
  assert.equal(organizerBootstrapInputSchema.safeParse({ ...preview, kind: "create.apply", previewId: "a".repeat(64) }).success, true);
  assert.equal(organizerBootstrapInputSchema.safeParse({ ...preview, kind: "adopt.preview", configurationJson: null }).success, true);
  assert.equal(organizerBootstrapResultSchema.safeParse({}).success, false);
  assert.equal(organizerBootstrapRpc.output, organizerBootstrapResultSchema);
});

test("generated Doctor and Repair contracts require concrete guidance and exact confirmed effects", () => {
  const doctor = {
    schemaVersion: PLANNING_SCHEMA_VERSION,
    contractVersion: PLANNING_CONTRACT_VERSION,
    contractHash: PLANNING_CONTRACT_SHA256,
    cursor: "9",
    hostId: "host-a",
    hostInstanceId: "engine-a",
    projectId: "project-a",
    projectName: "Project A",
    projectVersion: "3",
    observationId: "a".repeat(64),
    observedAt: "2026-09-11T16:00:00Z",
    maximumAgeMillis: "30000",
    status: "blocking",
    readOnly: true,
    assurance: "Doctor reads exact bounded engine facts and never mutates any system.",
    checks: [{
      id: "preflight-provider-codex",
      category: "provider",
      status: "blocking",
      code: "provider_codex_missing",
      title: "OpenAI Codex CLI 0.147.0",
      detail: "The exact required provider tuple is unavailable.",
      blocking: true,
      missingCapability: "OpenAI Codex CLI 0.147.0",
      installationGuidance: ["Install the official exact version on this daemon and rerun Doctor."],
    }],
    blockingCount: "1",
    repair: { available: false, reason: { code: "repair_unavailable", message: "No exact engine repair effect is available.", wakeCondition: null, humanActionRequired: false } },
  } as const;
  assert.equal(doctorQueryInputSchema.safeParse({ hostId: "host-a", projectId: "project-a", expectedProjectVersion: "3" }).success, true);
  assert.equal(doctorReportSchema.safeParse(doctor).success, true);
  assert.equal(doctorQueryRpc.output.safeParse(doctor).success, true);
  assert.equal(doctorReportSchema.safeParse({ ...doctor, readOnly: false }).success, false);
  assert.equal(doctorReportSchema.safeParse({ ...doctor, checks: [{ ...doctor.checks[0], missingCapability: null, installationGuidance: [] }] }).success, false);
  assert.equal(doctorReportSchema.safeParse({ ...doctor, checks: [{ ...doctor.checks[0], status: "passed", blocking: false }] }).success, false);

  const repairBase = {
    schemaVersion: PLANNING_SCHEMA_VERSION,
    contractVersion: PLANNING_CONTRACT_VERSION,
    contractHash: PLANNING_CONTRACT_SHA256,
    hostId: "host-a",
    requestId: "repair-request-0001",
    projectId: "project-a",
    expectedProjectVersion: "3",
  } as const;
  const previewInput = { ...repairBase, kind: "repair.preview", previewId: null, confirmed: false } as const;
  assert.equal(repairInputSchema.safeParse(previewInput).success, true);
  assert.equal(repairInputSchema.safeParse({ ...previewInput, confirmed: true }).success, false);
  const preview = {
    id: "b".repeat(64), requestId: repairBase.requestId, hostId: "host-a", hostInstanceId: "engine-a",
    projectId: "project-a", projectName: "Project A", projectVersion: "3", cursor: "9", observationId: "a".repeat(64),
    operations: [{ id: "repair-dynamic-state", kind: "reconcile_dynamic_state", description: "Reconcile only the configured TaskStore synchronization stream.", affectedResource: "TaskStore synchronization stream", effectClass: "conditional_update", destructive: false, automaticInstall: false }],
    valid: true, issues: [], confirmation: "Applying this exact Preview requires a fresh server-authenticated human confirmation.",
  } as const;
  const previewResult = {
    schemaVersion: PLANNING_SCHEMA_VERSION, contractVersion: PLANNING_CONTRACT_VERSION, contractHash: PLANNING_CONTRACT_SHA256,
    hostId: "host-a", projectId: "project-a", cursor: "9", requestId: repairBase.requestId,
    status: "preview", message: "Preview ready", preview, projectVersion: null, refusalCode: null,
  };
  assert.equal(repairResultSchema.safeParse(previewResult).success, true);
  assert.equal(repairProjectRpc.output.safeParse(previewResult).success, true);
  assert.equal(repairResultSchema.safeParse({ ...previewResult, preview: { ...preview, operations: [{ ...preview.operations[0], automaticInstall: true }] } }).success, false);
  assert.equal(repairInputSchema.safeParse({ ...repairBase, kind: "repair.apply", previewId: preview.id, confirmed: true }).success, true);
  assert.equal(repairInputSchema.safeParse({ ...repairBase, kind: "repair.apply", previewId: preview.id, confirmed: false }).success, false);
});

test("every mutation is bound to an engine-returned action ticket", async () => {
  const fixture = new DeterministicPlanningFixture();
  const detail = await fixture.taskDetail(taskDetailQuery);
  assert.ok(detail.detail);
  const task = detail.detail;
  const launch = task.summary.allowedActions.find(
    (action) => action.kind === "configuration.preview",
  );
  assert.ok(launch);
  const mutation = bindPlanningMutation(launch, {
    type: "configuration.preview",
    target: task.configurationTarget,
    overrides: task.configuration.map((entry) => entry.configured),
  });
  assert.equal(mutation.requestId, launch.requestId);
  assert.equal(mutation.idempotencyKey, launch.idempotencyKey);
  assert.equal(mutation.expectedVersion, launch.expectedVersion);
  assert.equal(planningMutationRpc.input.safeParse(mutation).success, true);

  assert.throws(() =>
    bindPlanningMutation(launch, {
      type: "task.launch-now",
      taskId: task.summary.id,
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
      target: task.configurationTarget,
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
    "homeQueryInput",
    "homeSnapshot",
    "homeProject",
    "homeAction",
    "doctorQueryInput",
    "doctorReport",
    "repairInput",
    "repairPreview",
    "repairResult",
    "organizerBootstrapInput",
    "organizerBootstrapPreview",
    "organizerBootstrapResult",
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
    "plugin.handle(homeQueryRpc",
    "plugin.handle(doctorQueryRpc",
    "plugin.handle(repairProjectRpc",
    "plugin.handle(organizerBootstrapRpc",
  ]) {
    assert.ok(entry.includes(registration), registration);
  }
  const connector = readFileSync("connector/paseo.server.ts", "utf8");
  assert.match(connector, /return this\.#planningTransport\.query\(input\)/);
  assert.match(connector, /return this\.#planningTransport\.taskDetail\(input\)/);
  assert.doesNotMatch(connector, /tests\/fixtures|DeterministicPlanningFixture/);
  assert.match(entry, /locations:\s*\["workspace", "explorer"\]/);
  assert.match(entry, /title: "Return to Director Board"/);
  assert.match(entry, /plugin\.addClientSide\(contributeTaskNavigation\)/);
  const navigation = readFileSync("ui/task-navigation.client.tsx", "utf8");
  assert.match(navigation, /client\.addComposerPill\(/);
  assert.match(navigation, /client\.openPanel\("task-inspector"/);
  assert.doesNotMatch(navigation, /clipboard|localStorage|window\.|document\./);
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
