// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import test from "node:test";

import {
  PLANNING_CONTRACT_SHA256,
  PLANNING_CONTRACT_VERSION,
  PLANNING_MAXIMUM_RESPONSE_BYTES,
  PLANNING_MUTATION_ACTOR_HEADERS,
  PLANNING_MUTATION_PATH,
  PLANNING_QUERY_PATH,
  PLANNING_TASK_DETAIL_QUERY_PATH,
  bindPlanningMutation,
  type PlanningMutationInput,
  type PlanningQueryInput,
  type TaskDetailQueryInput,
} from "../generated/planning-contract.shared.ts";
import {
  createPlanningTransport,
  PlanningTransportError,
} from "../connector/engine-planning.server.ts";
import { DeterministicPlanningFixture } from "./fixtures/planning-fixture.ts";

const query: PlanningQueryInput = {
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
  pageSize: 100,
};

function responseAt(url: string, body: string, init: ResponseInit): Response {
  const response = new Response(body, init);
  Object.defineProperty(response, "url", { value: url });
  return response;
}

test("the connector posts and validates one engine-owned planning query", async () => {
  const fixture = new DeterministicPlanningFixture();
  const snapshot = await fixture.query(query);
  let calls = 0;
  const transport = createPlanningTransport({
    baseUrl: "http://127.0.0.1:7041",
    mutationActor: { kind: "human", id: "server-owner", sessionId: "server-session" },
    fetch: async (input, init) => {
      calls += 1;
      assert.equal(String(input), `http://127.0.0.1:7041${PLANNING_QUERY_PATH}`);
      assert.equal(init?.method, "POST");
      assert.equal(init?.redirect, "error");
      assert.deepEqual(JSON.parse(String(init?.body)), query);
      assert.equal(
        (init?.headers as Record<string, string>)["x-director-contract-version"],
        PLANNING_CONTRACT_VERSION,
      );
      assert.equal(
        (init?.headers as Record<string, string>)["x-director-contract-hash"],
        PLANNING_CONTRACT_SHA256,
      );
      return responseAt(
        `http://127.0.0.1:7041${PLANNING_QUERY_PATH}`,
        JSON.stringify(snapshot),
        {
          status: 200,
          headers: {
            "content-type": "application/json",
            "x-director-contract-version": PLANNING_CONTRACT_VERSION,
            "x-director-contract-hash": PLANNING_CONTRACT_SHA256,
          },
        },
      );
    },
  });
  assert.deepEqual(await transport.query(query), snapshot);
  assert.equal(calls, 1);
});

test("the connector binds Task detail to the exact host and Board context", async () => {
  const fixture = new DeterministicPlanningFixture();
  const input: TaskDetailQueryInput = {
    hostId: "host-a",
    context: "board",
    taskId: "task-0",
    paseoWorkspaceId: null,
    paseoAgentId: null,
    afterCursor: null,
  };
  const snapshot = await fixture.taskDetail(input);
  const transport = createPlanningTransport({
    baseUrl: "http://127.0.0.1:7041",
    fetch: async (url, init) => {
      assert.equal(String(url), `http://127.0.0.1:7041${PLANNING_TASK_DETAIL_QUERY_PATH}`);
      assert.deepEqual(JSON.parse(String(init?.body)), input);
      return responseAt(
        `http://127.0.0.1:7041${PLANNING_TASK_DETAIL_QUERY_PATH}`,
        JSON.stringify(snapshot),
        {
          status: 200,
          headers: {
            "content-type": "application/json",
            "x-director-contract-version": PLANNING_CONTRACT_VERSION,
            "x-director-contract-hash": PLANNING_CONTRACT_SHA256,
          },
        },
      );
    },
  });
  const result = await transport.taskDetail(input);
  assert.equal(result.hostId, "host-a");
  assert.equal(result.detail?.binding.taskId, "task-0");
  assert.equal(result.detail?.binding.paseoAgentId, "paseo-agent-task-0");
});

test("the connector fails closed on Task detail host and cursor drift", async () => {
  const input: TaskDetailQueryInput = {
    hostId: "host-a",
    context: "agent",
    taskId: null,
    paseoWorkspaceId: "paseo-workspace-task-0",
    paseoAgentId: "paseo-agent-task-0",
    afterCursor: null,
  };
  for (const [responseCode, expectedCode] of [
    ["TASK_DETAIL_HOST_MISMATCH", "ENGINE_TASK_DETAIL_HOST_MISMATCH"],
    ["TASK_DETAIL_CURSOR_INVALIDATED", "ENGINE_TASK_DETAIL_CURSOR_INVALIDATED"],
  ] as const) {
    const transport = createPlanningTransport({
      baseUrl: "http://127.0.0.1:7041",
      fetch: async () => responseAt(
        `http://127.0.0.1:7041${PLANNING_TASK_DETAIL_QUERY_PATH}`,
        JSON.stringify({ code: responseCode }),
        { status: 409 },
      ),
    });
    await assert.rejects(
      transport.taskDetail(input),
      (error: unknown) => error instanceof PlanningTransportError && error.code === expectedCode,
    );
  }
});

test("the connector forwards a control mutation without actor or policy fields", async () => {
  const fixture = new DeterministicPlanningFixture();
  const mutation = bindPlanningMutation(
    {
      kind: "project.emergency-stop.prepare",
      label: "Emergency stop…",
      targetId: "project-scale",
      requestId: "control-mutation-request",
      idempotencyKey: "control-mutation-request",
      expectedVersion: "9",
      humanApprovalRef: null,
      acknowledgementRevision: null,
      emphasis: "danger",
    },
    { type: "project.emergency-stop.prepare", projectId: "project-scale" },
  );
  const expected = await fixture.mutate(mutation);
  const transport = createPlanningTransport({
    baseUrl: "http://127.0.0.1:7041",
    mutationActor: { kind: "human", id: "server-owner", sessionId: "server-session" },
    fetch: async (input, init) => {
      assert.equal(String(input), `http://127.0.0.1:7041${PLANNING_MUTATION_PATH}`);
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
      assert.equal(body.actorId, undefined);
      assert.equal(body.humanConfirmed, undefined);
      assert.deepEqual(body, mutation);
      assert.equal(
        (init?.headers as Record<string, string>)[PLANNING_MUTATION_ACTOR_HEADERS.kind],
        "human",
      );
      assert.equal(
        (init?.headers as Record<string, string>)[PLANNING_MUTATION_ACTOR_HEADERS.id],
        "server-owner",
      );
      assert.equal(
        (init?.headers as Record<string, string>)[PLANNING_MUTATION_ACTOR_HEADERS.session],
        "server-session",
      );
      return responseAt(
        `http://127.0.0.1:7041${PLANNING_MUTATION_PATH}`,
        JSON.stringify(expected),
        {
          status: 200,
          headers: {
            "content-type": "application/json",
            "x-director-contract-version": PLANNING_CONTRACT_VERSION,
            "x-director-contract-hash": PLANNING_CONTRACT_SHA256,
          },
        },
      );
    },
  });
  assert.deepEqual(await transport.mutate(mutation as PlanningMutationInput), expected);
});

test("the planning transport fails closed on origin, contract, cursor, input, and payload drift", async () => {
  for (const baseUrl of [
    "https://127.0.0.1:7041",
    "http://example.com:7041",
    "http://127.0.0.1:7041/path",
    "http://user:pass@127.0.0.1:7041",
  ]) {
    assert.throws(() => createPlanningTransport({ baseUrl }), PlanningTransportError);
  }

  let invalidCalls = 0;
  const invalidInput = createPlanningTransport({
    baseUrl: "http://127.0.0.1:7041",
    fetch: async () => {
      invalidCalls += 1;
      throw new Error("must not fetch");
    },
  });
  await assert.rejects(
    invalidInput.query({ ...query, pageSize: 101 } as PlanningQueryInput),
  );
  const unauthenticatedMutation = bindPlanningMutation(
    {
      kind: "project.pause", label: "Pause Project", targetId: "project-scale",
      requestId: "missing-actor-mutation", idempotencyKey: "missing-actor-mutation",
      expectedVersion: "9", humanApprovalRef: null, acknowledgementRevision: null, emphasis: "secondary",
    },
    { type: "project.pause", projectId: "project-scale" },
  );
  await assert.rejects(
    invalidInput.mutate(unauthenticatedMutation),
    (error: unknown) => error instanceof PlanningTransportError && error.code === "ENGINE_PLANNING_ACTOR",
  );
  const oversizedEpicIds = Array.from({ length: 500 }, (_, index) =>
    `epic-${String(index).padStart(4, "0")}-${"x".repeat(118)}`
  );
  await assert.rejects(
    invalidInput.query({ ...query, epicIds: oversizedEpicIds }),
    (error: unknown) =>
      error instanceof PlanningTransportError &&
      error.code === "ENGINE_PLANNING_INPUT",
  );
  assert.equal(invalidCalls, 0);

  const fixture = new DeterministicPlanningFixture();
  const snapshot = await fixture.query(query);
  for (const response of [
    responseAt(`http://[::1]:7041${PLANNING_QUERY_PATH}`, JSON.stringify(snapshot), {
      status: 200,
      headers: {
        "x-director-contract-version": PLANNING_CONTRACT_VERSION,
        "x-director-contract-hash": "0".repeat(64),
      },
    }),
    responseAt(`http://[::1]:7041${PLANNING_QUERY_PATH}`, JSON.stringify({ ...snapshot, extra: true }), {
      status: 200,
      headers: {
        "x-director-contract-version": PLANNING_CONTRACT_VERSION,
        "x-director-contract-hash": PLANNING_CONTRACT_SHA256,
      },
    }),
    responseAt(`http://[::1]:7041${PLANNING_QUERY_PATH}`, "x".repeat(PLANNING_MAXIMUM_RESPONSE_BYTES + 1), {
      status: 200,
      headers: {
        "x-director-contract-version": PLANNING_CONTRACT_VERSION,
        "x-director-contract-hash": PLANNING_CONTRACT_SHA256,
      },
    }),
  ]) {
    let calls = 0;
    const transport = createPlanningTransport({
      baseUrl: "http://[::1]:7041",
      fetch: async () => {
        calls += 1;
        return response;
      },
    });
    await assert.rejects(transport.query(query), PlanningTransportError);
    assert.equal(calls, 1, "planning transport must not own retry policy");
  }

  const invalidated = createPlanningTransport({
    baseUrl: "http://127.0.0.1:7041",
    fetch: async () => responseAt(
      `http://127.0.0.1:7041${PLANNING_QUERY_PATH}`,
      '{"code":"PLANNING_CURSOR_INVALIDATED"}',
      { status: 409 },
    ),
  });
  await assert.rejects(
    invalidated.query(query),
    (error: unknown) =>
      error instanceof PlanningTransportError &&
      error.code === "ENGINE_PLANNING_CURSOR_INVALIDATED",
  );
});
