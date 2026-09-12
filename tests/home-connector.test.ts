// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import test from "node:test";

import {
  PLANNING_CONTRACT_SHA256,
  PLANNING_CONTRACT_VERSION,
  PLANNING_HOME_QUERY_PATH,
  PLANNING_DOCTOR_QUERY_PATH,
  PLANNING_MUTATION_ACTOR_HEADERS,
  PLANNING_ORGANIZER_MUTATION_PATH,
  PLANNING_REPAIR_MUTATION_PATH,
  doctorReportSchema,
  homeSnapshotSchema,
  type HomeQueryInput,
  type DoctorQueryInput,
  type OrganizerBootstrapInput,
  type RepairInput,
} from "../generated/planning-contract.shared.ts";
import {
  createPlanningTransport,
  PlanningTransportError,
} from "../connector/engine-planning.server.ts";

const query: HomeQueryInput = { hostId: "host-a", cursor: null, pageSize: 25 };

function responseAt(url: string, body: string, init: ResponseInit): Response {
  const response = new Response(body, init);
  Object.defineProperty(response, "url", { value: url });
  return response;
}

function snapshot(hostId = "host-a") {
  return homeSnapshotSchema.parse({
    schemaVersion: 1,
    contractVersion: PLANNING_CONTRACT_VERSION,
    contractHash: PLANNING_CONTRACT_SHA256,
    cursor: "12",
    page: {
      host: { id: hostId, label: "Exact host", instanceId: "engine-instance", state: "current", observedAt: "2026-09-11T16:00:00Z", maximumAgeMillis: "30000" },
      projects: [],
      totals: { projects: "0", healthy: "0", degraded: "0", paused: "0", needsYou: "0", activeWork: "0" },
      surfaceActions: [
        { kind: "create_project", label: "Create Project", hostId, projectId: null, enabled: true, unavailableReason: null, paseoWorkspaceId: null, command: null, emphasis: "primary" },
        { kind: "adopt_organizer", label: "Adopt Organizer", hostId, projectId: null, enabled: true, unavailableReason: null, paseoWorkspaceId: null, command: null, emphasis: "secondary" },
      ],
      totalProjects: "0",
      nextCursor: null,
    },
  });
}

test("the connector posts one host-bound Home query and validates the exact response", async () => {
  let calls = 0;
  const transport = createPlanningTransport({
    baseUrl: "http://127.0.0.1:7041",
    fetch: async (input, init) => {
      calls++;
      assert.equal(String(input), `http://127.0.0.1:7041${PLANNING_HOME_QUERY_PATH}`);
      assert.deepEqual(JSON.parse(String(init?.body)), query);
      assert.equal((init?.headers as Record<string, string>)["x-director-contract-version"], PLANNING_CONTRACT_VERSION);
      assert.equal((init?.headers as Record<string, string>)["x-director-contract-hash"], PLANNING_CONTRACT_SHA256);
      return responseAt(`http://127.0.0.1:7041${PLANNING_HOME_QUERY_PATH}`, JSON.stringify(snapshot()), {
        status: 200,
        headers: {
          "x-director-contract-version": PLANNING_CONTRACT_VERSION,
          "x-director-contract-hash": PLANNING_CONTRACT_SHA256,
        },
      });
    },
  });
  assert.deepEqual(await transport.home!(query), snapshot());
  assert.equal(calls, 1);
});

test("the Home transport refuses another host, contract drift, stale cursors, and retries", async () => {
  for (const fixture of [
    {
      response: responseAt(`http://127.0.0.1:7041${PLANNING_HOME_QUERY_PATH}`, JSON.stringify(snapshot("host-b")), {
        status: 200,
        headers: { "x-director-contract-version": PLANNING_CONTRACT_VERSION, "x-director-contract-hash": PLANNING_CONTRACT_SHA256 },
      }),
      code: "ENGINE_HOME_HOST_MISMATCH",
    },
    {
      response: responseAt(`http://127.0.0.1:7041${PLANNING_HOME_QUERY_PATH}`, JSON.stringify(snapshot()), {
        status: 200,
        headers: { "x-director-contract-version": PLANNING_CONTRACT_VERSION, "x-director-contract-hash": "0".repeat(64) },
      }),
      code: "ENGINE_HOME_CONTRACT",
    },
    {
      response: responseAt(`http://127.0.0.1:7041${PLANNING_HOME_QUERY_PATH}`, '{"code":"HOME_CURSOR_INVALIDATED"}', { status: 409 }),
      code: "ENGINE_HOME_CURSOR_INVALIDATED",
    },
    {
      response: responseAt(`http://127.0.0.1:7041${PLANNING_HOME_QUERY_PATH}`, '{"code":"HOME_HOST_MISMATCH"}', { status: 409 }),
      code: "ENGINE_HOME_HOST_MISMATCH",
    },
  ]) {
    let calls = 0;
    const transport = createPlanningTransport({
      baseUrl: "http://127.0.0.1:7041",
      fetch: async () => {
        calls++;
        return fixture.response;
      },
    });
    await assert.rejects(
      transport.home!(query),
      (error: unknown) => error instanceof PlanningTransportError && error.code === fixture.code,
    );
    assert.equal(calls, 1, `${fixture.code} must not trigger connector retry or host fallback`);
  }

  let calls = 0;
  const invalid = createPlanningTransport({
    baseUrl: "http://127.0.0.1:7041",
    fetch: async () => {
      calls++;
      throw new Error("must not fetch");
    },
  });
  await assert.rejects(invalid.home!({ ...query, pageSize: 51 } as HomeQueryInput));
  assert.equal(calls, 0);
});

test("Organizer Preview/Apply transport is human-authenticated and exact-host bound", async () => {
  const input: OrganizerBootstrapInput = {
    schemaVersion: 1,
    contractVersion: PLANNING_CONTRACT_VERSION,
    contractHash: PLANNING_CONTRACT_SHA256,
    hostId: "host-a",
    requestId: "request-organizer-preview",
    kind: "create.preview",
    projectId: "project-new",
    projectName: "New Project",
    repositoryPath: "/srv/director/organizers/project-new",
    configurationJson: "{}",
    previewId: null,
  };
  const expected = {
    schemaVersion: 1,
    contractVersion: PLANNING_CONTRACT_VERSION,
    contractHash: PLANNING_CONTRACT_SHA256,
    hostId: "host-a",
    cursor: "4",
    requestId: input.requestId,
    status: "preview",
    message: "Create Preview contains blocking issues and cannot be applied",
    preview: {
      id: "c".repeat(64),
      kind: "create",
      requestId: input.requestId,
      projectId: input.projectId,
      projectName: input.projectName,
      repositoryPath: input.repositoryPath,
      organizerRevision: null,
      configurationSha256: null,
      files: [],
      operations: [],
      valid: false,
      issues: [{ code: "configuration.invalid", field: "configuration", message: "Configuration is invalid" }],
    },
    projectVersion: null,
  } as const;
  const transport = createPlanningTransport({
    baseUrl: "http://127.0.0.1:7041",
    mutationActor: { kind: "human", id: "server-owner", sessionId: "server-session" },
    fetch: async (url, init) => {
      assert.equal(String(url), `http://127.0.0.1:7041${PLANNING_ORGANIZER_MUTATION_PATH}`);
      assert.deepEqual(JSON.parse(String(init?.body)), input);
      const headers = init?.headers as Record<string, string>;
      assert.equal(headers[PLANNING_MUTATION_ACTOR_HEADERS.kind], "human");
      assert.equal(headers[PLANNING_MUTATION_ACTOR_HEADERS.id], "server-owner");
      assert.equal(headers[PLANNING_MUTATION_ACTOR_HEADERS.session], "server-session");
      return responseAt(`http://127.0.0.1:7041${PLANNING_ORGANIZER_MUTATION_PATH}`, JSON.stringify(expected), {
        status: 200,
        headers: { "x-director-contract-version": PLANNING_CONTRACT_VERSION, "x-director-contract-hash": PLANNING_CONTRACT_SHA256 },
      });
    },
  });
  assert.deepEqual(await transport.bootstrapOrganizer!(input), expected);

  const unauthenticated = createPlanningTransport({ baseUrl: "http://127.0.0.1:7041" });
  await assert.rejects(unauthenticated.bootstrapOrganizer!(input), (error: unknown) =>
    error instanceof PlanningTransportError && error.code === "ENGINE_ORGANIZER_ACTOR");
});

function doctorReport(hostId = "host-a", projectId = "project-a") {
  return doctorReportSchema.parse({
    schemaVersion: 1, contractVersion: PLANNING_CONTRACT_VERSION, contractHash: PLANNING_CONTRACT_SHA256,
    cursor: "12", hostId, hostInstanceId: "engine-a", projectId, projectName: "Project A", projectVersion: "3",
    observationId: "a".repeat(64), observedAt: "2026-09-11T16:00:00Z", maximumAgeMillis: "30000",
    status: "healthy", readOnly: true, assurance: "Doctor reads exact bounded engine facts and never mutates any system.",
    checks: [{ id: "taskstore-read", category: "dynamic_state", status: "passed", code: "taskstore_read_current", title: "Director TaskStore", detail: "The read-only snapshot is current.", blocking: false, missingCapability: null, installationGuidance: [] }],
    blockingCount: "0", repair: { available: false, reason: { code: "repair_unavailable", message: "No exact repair effect is available.", wakeCondition: null, humanActionRequired: false } },
  });
}

test("Doctor transport performs one read-only exact-host request and rejects binding drift", async () => {
  const input: DoctorQueryInput = { hostId: "host-a", projectId: "project-a", expectedProjectVersion: "3" };
  let calls = 0;
  const transport = createPlanningTransport({
    baseUrl: "http://127.0.0.1:7041",
    fetch: async (url, init) => {
      calls++;
      assert.equal(String(url), `http://127.0.0.1:7041${PLANNING_DOCTOR_QUERY_PATH}`);
      assert.deepEqual(JSON.parse(String(init?.body)), input);
      assert.equal((init?.headers as Record<string, string>)[PLANNING_MUTATION_ACTOR_HEADERS.kind], undefined);
      return responseAt(String(url), JSON.stringify(doctorReport()), { status: 200, headers: { "x-director-contract-version": PLANNING_CONTRACT_VERSION, "x-director-contract-hash": PLANNING_CONTRACT_SHA256 } });
    },
  });
  assert.deepEqual(await transport.doctor!(input), doctorReport());
  assert.equal(calls, 1);

  const drifted = createPlanningTransport({
    baseUrl: "http://127.0.0.1:7041",
    fetch: async (url) => responseAt(String(url), JSON.stringify(doctorReport("host-b")), { status: 200, headers: { "x-director-contract-version": PLANNING_CONTRACT_VERSION, "x-director-contract-hash": PLANNING_CONTRACT_SHA256 } }),
  });
  await assert.rejects(drifted.doctor!(input), (error: unknown) => error instanceof PlanningTransportError && error.code === "ENGINE_DOCTOR_BINDING");
});

test("Repair transport attaches only server human identity and preserves exact Preview bindings", async () => {
  const input: RepairInput = {
    schemaVersion: 1, contractVersion: PLANNING_CONTRACT_VERSION, contractHash: PLANNING_CONTRACT_SHA256,
    hostId: "host-a", requestId: "repair-request-0001", kind: "repair.preview", projectId: "project-a",
    expectedProjectVersion: "3", previewId: null, confirmed: false,
  };
  const preview = {
    id: "b".repeat(64), requestId: input.requestId, hostId: input.hostId, hostInstanceId: "engine-a", projectId: input.projectId,
    projectName: "Project A", projectVersion: "3", cursor: "12", observationId: "a".repeat(64),
    operations: [{ id: "repair-dynamic-state", kind: "reconcile_dynamic_state", description: "Reconcile only the configured TaskStore synchronization stream.", affectedResource: "TaskStore synchronization stream", effectClass: "conditional_update", destructive: false, automaticInstall: false }],
    valid: true, issues: [], confirmation: "Applying this exact Preview requires a fresh server-authenticated human confirmation.",
  } as const;
  const result = { schemaVersion: 1, contractVersion: PLANNING_CONTRACT_VERSION, contractHash: PLANNING_CONTRACT_SHA256,
    hostId: input.hostId, projectId: input.projectId, cursor: "12", requestId: input.requestId, status: "preview", message: "Preview ready", preview, projectVersion: null, refusalCode: null } as const;
  let calls = 0;
  const transport = createPlanningTransport({
    baseUrl: "http://127.0.0.1:7041",
    mutationActor: { kind: "human", id: "server-owner", sessionId: "server-session" },
    fetch: async (url, init) => {
      calls++;
      assert.equal(String(url), `http://127.0.0.1:7041${PLANNING_REPAIR_MUTATION_PATH}`);
      assert.deepEqual(JSON.parse(String(init?.body)), input);
      const headers = init?.headers as Record<string, string>;
      assert.equal(headers[PLANNING_MUTATION_ACTOR_HEADERS.kind], "human");
      assert.equal(headers[PLANNING_MUTATION_ACTOR_HEADERS.id], "server-owner");
      assert.equal(headers[PLANNING_MUTATION_ACTOR_HEADERS.session], "server-session");
      return responseAt(String(url), JSON.stringify(result), { status: 200, headers: { "x-director-contract-version": PLANNING_CONTRACT_VERSION, "x-director-contract-hash": PLANNING_CONTRACT_SHA256 } });
    },
  });
  assert.deepEqual(await transport.repair!(input), result);
  assert.equal(calls, 1);

  const unauthenticated = createPlanningTransport({ baseUrl: "http://127.0.0.1:7041", fetch: async () => { throw new Error("must not fetch"); } });
  await assert.rejects(unauthenticated.repair!(input), (error: unknown) => error instanceof PlanningTransportError && error.code === "ENGINE_REPAIR_ACTOR");
});
