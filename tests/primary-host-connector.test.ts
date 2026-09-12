// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import test from "node:test";

import type { PaseoAgent, PaseoWorkspace } from "@getpaseo/client";

import {
  PaseoHostConnector,
  PaseoHostEffectError,
  type ConnectorClient,
} from "../connector/paseo.server.ts";
import {
  AGENT_MCP_CONTRACT_SHA256,
  AGENT_MCP_CONTRACT_VERSION,
} from "../generated/agent-mcp-contract.shared.ts";
import {
  WORKER_LABEL,
  type HostCommand,
  type HostCommandArguments,
} from "../generated/host-contract.shared.ts";
import type { EngineSelection } from "../connector/engine-selection.server.ts";

type FakeAgent = PaseoAgent & {
  timelineEntries: {
    item: {
      type: "user_message";
      text: string;
      messageId?: string;
      clientMessageId?: string;
    };
  }[];
};

function fakePaseo() {
  const workspaces: PaseoWorkspace[] = [];
  const agents: FakeAgent[] = [];
  const calls = { connect: 0, workspaceCreates: 0, agentCreates: 0, sends: 0, agentArchives: 0 };
  const createdInputs: unknown[] = [];
  const pageInfo = { hasMore: false, nextCursor: null, previousCursor: null };

  const agentHandle = (id: string) => ({
    id,
    async refresh() {
      const agent = agents.find((entry) => entry.id === id);
      return agent ? { agent, project: null } : null;
    },
    timeline: {
      async refetch() {
        const agent = agents.find((entry) => entry.id === id);
        return {
          requestId: "timeline", agentId: id, agent: agent ?? null,
          direction: "tail", projection: "canonical", epoch: "epoch-1",
          reset: false, staleCursor: false, gap: false,
          window: { minSeq: 0, maxSeq: agent?.timelineEntries.length ?? 0, nextSeq: (agent?.timelineEntries.length ?? 0) + 1 },
          startCursor: null, endCursor: null, hasOlder: false, hasNewer: false,
          entries: (agent?.timelineEntries ?? []).map((entry, index) => ({
            ...entry, provider: "codex", timestamp: "2026-09-10T08:00:00Z",
            seqStart: index + 1, seqEnd: index + 1, sourceSeqRanges: [], collapsed: [],
          })),
          error: null,
        };
      },
    },
    async send(text: string, options?: { messageId?: string }) {
      calls.sends++;
      const agent = agents.find((entry) => entry.id === id)!;
      agent.timelineEntries.push({ item: { type: "user_message", text, messageId: options?.messageId } });
      agent.status = "running";
      agent.activeTurn = { turnId: `turn-${calls.sends}`, startedAt: "2026-09-10T08:00:00Z" };
    },
    async archive() {
      calls.agentArchives++;
      const agent = agents.find((entry) => entry.id === id)!;
      agent.status = "closed";
      agent.archivedAt = "2026-09-10T08:01:00Z";
      return { archivedAt: agent.archivedAt };
    },
  });

  const workspaceHandle = (id: string) => ({
    id,
    async refresh() {
      return workspaces.find((entry) => entry.id === id) ?? null;
    },
    agents: {
      async create(options: {
        config: { provider: string };
        title?: string | null;
        labels?: Record<string, string>;
        prompt?: string;
        clientMessageId?: string;
      }) {
        calls.agentCreates++;
        createdInputs.push(options);
        const workspace = workspaces.find((entry) => entry.id === id)!;
        const [provider, ...model] = options.config.provider.split("/");
        const agent = {
          id: `agent-${calls.agentCreates}`,
          provider,
          cwd: workspace.workspaceDirectory!,
          workspaceId: workspace.id,
          model: model.join("/"),
          createdAt: "2026-09-10T08:00:00Z",
          updatedAt: "2026-09-10T08:00:00Z",
          lastUserMessageAt: "2026-09-10T08:00:00Z",
          status: "running",
          activeTurn: { turnId: "bootstrap-turn", startedAt: "2026-09-10T08:00:00Z" },
          capabilities: { supportsMcpServers: true },
          currentModeId: "default",
          availableModes: [], pendingPermissions: [], persistence: null,
          title: options.title ?? null, labels: { ...(options.labels ?? {}) },
          archivedAt: null, attentionReason: null,
          timelineEntries: [{ item: { type: "user_message", text: options.prompt!, clientMessageId: options.clientMessageId } }],
        } as unknown as FakeAgent;
        agents.push(agent);
        return agentHandle(agent.id);
      },
    },
    async archive() {
      const index = workspaces.findIndex((entry) => entry.id === id);
      if (index >= 0) workspaces.splice(index, 1);
      return { requestId: "archive", workspaceId: id, archivedAt: "2026-09-10T08:02:00Z", error: null };
    },
  });

  const client = {
    async connect() { calls.connect++; },
    async close() {},
    workspaces: {
      async list() { return { requestId: "workspaces", entries: [...workspaces], pageInfo }; },
      ref(value: string) { return workspaceHandle(value); },
      async create(options: { title?: string; source: { kind: string; path: string } }) {
        calls.workspaceCreates++;
        const workspace = {
          id: `workspace-${calls.workspaceCreates}`,
          projectId: "project-native", projectDisplayName: "Project", projectRootPath: options.source.path,
          workspaceDirectory: options.source.path, projectKind: "git", workspaceKind: "worktree",
          name: "Execution", title: options.title ?? null, archivingAt: null,
          status: "done", statusEnteredAt: null, activityAt: null, scripts: [],
          gitRuntime: { currentBranch: "task/task-1", remoteUrl: "https://example.invalid/repo", isPaseoOwnedWorktree: false },
          githubRuntime: null,
        } as unknown as PaseoWorkspace;
        workspaces.push(workspace);
        return workspaceHandle(workspace.id);
      },
    },
    agents: {
      async list(options?: { filter?: { labels?: Record<string, string> } }) {
        const labels = options?.filter?.labels ?? {};
        return {
          requestId: "agents",
          entries: agents.filter((agent) => Object.entries(labels).every(([key, value]) => agent.labels[key] === value)).map((agent) => ({ agent, project: null })),
          pageInfo,
        };
      },
      ref(value: string) { return agentHandle(value); },
    },
  } as unknown as ConnectorClient;
  return { client, workspaces, agents, calls, createdInputs };
}

function connector(client: ConnectorClient): PaseoHostConnector {
  return new PaseoHostConnector(
    client,
    { mode: "development" } as EngineSelection,
    { async load() { throw new Error("unused"); } },
    {
      async query() { throw new Error("unused"); },
      async taskDetail() { throw new Error("unused"); },
      async mutate() { throw new Error("unused"); },
    },
  );
}

function primaryArguments(): HostCommandArguments {
  const effectId = "effect-primary-create";
  const profile = {
    provider: "codex", model: "gpt-5.4-mini", effort: "high", mode: "default",
    permissionMode: "workspace-write", providerOptions: [], sha256: "a".repeat(64),
  } as const;
  const session = {
    contractVersion: AGENT_MCP_CONTRACT_VERSION, contractHash: AGENT_MCP_CONTRACT_SHA256,
    sessionSha256: "b".repeat(64), role: "worker", provider: "codex", model: "gpt-5.4-mini",
    tools: ["director_task_outcome_submit", "director_task_read"],
    server: { name: "director-session-mcp", command: "/usr/bin/director-agent-runtime", args: ["serve"], env: {} },
  } as const;
  return {
    scope: { projectId: "project-1", workspaceId: "repository-1", taskId: "task-1", runId: "run-1" },
    effectKind: "task_agent.create_with_bootstrap", effectId, bindingHash: "c".repeat(64),
    worktreeId: "worktree-1", worktreePath: "/srv/runs/run-1", title: "Implement exact Task",
    initialPrompt: "Director bootstrap only. Do not inspect files, call tools, or perform Task or Review work. Finish this turn immediately.",
    lifecycleDigest: "d".repeat(64), isolationDigest: "e".repeat(64), preparationReady: true,
    preparationBarrierHash: "f".repeat(64), clientMessageId: "message-bootstrap",
    boundaryId: "boundary-1", operationalObservationId: "operational-1", profile, session,
    labels: {
      [WORKER_LABEL.project]: "project-1", [WORKER_LABEL.rootWorkspace]: "wks_root_1",
      [WORKER_LABEL.workspace]: "repository-1", [WORKER_LABEL.executionWorkspace]: "workspace-1",
      [WORKER_LABEL.task]: "task-1", [WORKER_LABEL.run]: "run-1", [WORKER_LABEL.role]: "task-agent",
      [WORKER_LABEL.phase]: "building", [WORKER_LABEL.base]: "1".repeat(40),
      [WORKER_LABEL.effect]: effectId, [WORKER_LABEL.profile]: profile.sha256,
      [WORKER_LABEL.session]: session.sessionSha256,
      [WORKER_LABEL.registeredAt]: "2026-09-10T08:00:00Z", [WORKER_LABEL.startedAt]: "2026-09-10T08:00:00Z",
    },
  };
}

function reviewerArguments(): HostCommandArguments {
  const value = primaryArguments();
  const effectId = "effect-reviewer-create";
  const profile = {
    provider: "claude-code", model: "claude-sonnet", effort: "high", mode: "default",
    permissionMode: "read-only", providerOptions: [], sha256: "7".repeat(64),
  } as const;
  const session = {
    contractVersion: AGENT_MCP_CONTRACT_VERSION, contractHash: AGENT_MCP_CONTRACT_SHA256,
    sessionSha256: "8".repeat(64), role: "reviewer", provider: profile.provider, model: profile.model,
    tools: ["director_candidate_read", "director_review_verdict_submit"],
    server: { name: "director-review-mcp", command: "/usr/bin/director-agent-runtime", args: ["serve"], env: {} },
  } as const;
  return {
    ...value, effectKind: "reviewer_agent.create_with_bootstrap", effectId,
    worktreeId: "review-candidate-1", worktreePath: "/srv/reviewers/candidate-1",
    title: "Independent review: Implement exact Task", profile, session,
    clientMessageId: "message-reviewer-bootstrap",
    labels: {
      ...value.labels,
      [WORKER_LABEL.executionWorkspace]: "workspace-reviewer-1",
      [WORKER_LABEL.role]: "reviewer", [WORKER_LABEL.phase]: "reviewing",
      [WORKER_LABEL.candidate]: "2".repeat(40), [WORKER_LABEL.effect]: effectId,
      [WORKER_LABEL.profile]: profile.sha256, [WORKER_LABEL.session]: session.sessionSha256,
    },
  };
}

function command(capability: HostCommand["capability"], argumentsValue: HostCommandArguments, afterCursor = 0): HostCommand {
  return {
    requestId: `request-${argumentsValue.effectId}`, idempotencyKey: argumentsValue.effectId,
    expectedVersion: 1, afterCursor, capability, arguments: argumentsValue,
  };
}

function directorMessageId(effectId: string): string {
  return `message-${createHash("sha256").update(effectId).digest("hex").slice(0, 32)}`;
}

test("public connector creates one host view and one parentless primary then sends one notified prompt", async () => {
  const world = fakePaseo();
  const host = connector(world.client);
  const base = primaryArguments();
  const workspaceCreate = command("executionWorkspace.createManaged", {
    ...base, effectKind: "host_view.create", effectId: "effect-host-view", workspaceId: undefined,
  });
  const createdWorkspace = await host.invoke(workspaceCreate);
  assert.equal(createdWorkspace.result.status, "desired");
  assert.equal(world.calls.workspaceCreates, 1);
  const replayedWorkspace = await host.invoke(workspaceCreate);
  assert.equal(replayedWorkspace.result.status, "desired");
  assert.equal(world.calls.workspaceCreates, 1);

  const workspaceId = createdWorkspace.result.externalId!;
  const labels = { ...base.labels!, [WORKER_LABEL.executionWorkspace]: workspaceId };
  const createArguments = { ...base, workspaceId, labels };
  const primaryCreate = command("taskAgent.createWithBootstrap", createArguments);
  const createdAgents = await Promise.all(
    Array.from({ length: 16 }, () => host.invoke(primaryCreate)),
  );
  const createdAgent = createdAgents[0]!;
  assert.ok(createdAgents.every((observation) => observation === createdAgent));
  assert.equal(createdAgent.result.status, "owned_present");
  assert.equal(world.calls.agentCreates, 1);
  const agent = world.agents[0]!;
  assert.equal(agent.labels["paseo.parent-agent-id"], undefined);
  assert.equal(agent.title, base.title);
  assert.equal(agent.cwd, base.worktreePath);
  assert.deepEqual(
    (world.createdInputs[0] as { config: { modeId: string; thinkingOptionId: string; featureValues: unknown; mcpServers: unknown; toolPolicy: { preapproved: unknown[] } } }).config,
    {
      provider: "codex/gpt-5.4-mini",
      modeId: "default",
      thinkingOptionId: "high",
      featureValues: { permissionMode: "workspace-write" },
      options: {},
      mcpServers: {
        "director-session-mcp": {
          type: "stdio", command: "/usr/bin/director-agent-runtime", args: ["serve"], env: {},
        },
      },
      toolPolicy: {
        preapproved: [
          { kind: "mcp", server: "director-session-mcp", tool: "director_task_outcome_submit" },
          { kind: "mcp", server: "director-session-mcp", tool: "director_task_read" },
        ],
      },
    },
  );

  agent.status = "idle";
  agent.activeTurn = null;
  agent.attentionReason = "finished";
  const observedAgent = await host.invoke(command("agent.observe", createArguments, createdAgent.cursor));
  assert.equal(observedAgent.result.status, "desired");
  assert.equal(observedAgent.result.externalId, agent.id);
  assert.equal(observedAgent.result.correlationHash?.length, 64);
  assert.deepEqual(observedAgent.result.usage, {
    state: "unavailable", inputTokensPresent: false, inputTokens: 0,
    cachedInputTokens: 0, outputTokensPresent: false, outputTokens: 0,
    costMicrousdPresent: false, costMicrousd: 0,
  });

  const promptArguments: HostCommandArguments = {
    ...createArguments, effectKind: "agent.send_prompt", effectId: "effect-real-prompt",
    agentId: agent.id, labels: undefined, initialPrompt: "Implement only the frozen Task.",
    notifyOnFinish: true, clientMessageId: "message-real-prompt",
    sessionBindingSha256: "9".repeat(64),
  };
  const prompt = command("send_agent_prompt", promptArguments, observedAgent.cursor);
  const firstPrompt = await host.invoke(prompt);
  assert.equal(firstPrompt.result.status, "owned_present");
  assert.equal(world.calls.sends, 1);
  const replayedPrompt = await host.invoke(prompt);
  assert.equal(replayedPrompt.result.status, "owned_present");
  assert.equal(world.calls.sends, 1, "nonrepeatable real prompt must not be sent twice");

  agent.status = "idle";
  agent.activeTurn = null;
  agent.lastUsage = {
    inputTokens: 100, cachedInputTokens: 40, outputTokens: 20, totalCostUsd: 0.123456,
  };
  const completedPrompt = await host.invoke(command("agent.observe", promptArguments, replayedPrompt.cursor));
  assert.equal(completedPrompt.result.status, "desired");
  assert.ok(completedPrompt.cursor > replayedPrompt.cursor);
  assert.deepEqual(completedPrompt.result.usage, {
    state: "current", sourceRevision: agent.updatedAt, inputTokensPresent: true, inputTokens: 100,
    cachedInputTokens: 40, outputTokensPresent: true, outputTokens: 20,
    costMicrousdPresent: true, costMicrousd: 123456,
  });

  agent.lastUsage = { inputTokens: 1, outputTokens: 2 };
  const noEstimatedCost = await host.invoke(command("agent.observe", promptArguments, completedPrompt.cursor));
  assert.equal(noEstimatedCost.result.usage?.state, "current");
  assert.equal(noEstimatedCost.result.usage?.costMicrousdPresent, false);
  agent.lastUsage = { inputTokens: 1, outputTokens: 2, totalCostUsd: 0.0000001 };
  const ambiguousCost = await host.invoke(command("agent.observe", promptArguments, noEstimatedCost.cursor));
  assert.equal(ambiguousCost.result.usage?.state, "ambiguous");

  const correctionArguments: HostCommandArguments = {
    ...promptArguments,
    effectId: "correction-prompt-exact-batch",
    initialPrompt: JSON.stringify({
      schemaVersion: "director.correction-prompt/v1",
      originalTaskAgentUuid: agent.id,
      batchSha256: "6".repeat(64),
    }),
    clientMessageId: "message-correction-exact-batch",
  };
  const correctionPrompt = command("send_agent_prompt", correctionArguments, ambiguousCost.cursor);
  const corrected = await host.invoke(correctionPrompt);
  const correctionReplay = await host.invoke(correctionPrompt);
  assert.equal(corrected.result.status, "owned_present");
  assert.equal(correctionReplay.result.status, "owned_present");
  assert.equal(world.calls.sends, 2, "correction must use the same primary exactly once");
  assert.equal(world.calls.agentCreates, 1, "connector must not create a replacement correction agent");
  assert.equal(agent.timelineEntries.at(-1)?.item.clientMessageId, undefined);
  assert.equal(agent.timelineEntries.at(-1)?.item.messageId, correctionArguments.clientMessageId);

  world.agents.push({
    ...agent,
    id: "agent-duplicate",
    labels: { ...agent.labels },
    timelineEntries: [...agent.timelineEntries],
  });
  const duplicate = await host.invoke(command("agent.observe", {
    ...createArguments,
    agentId: agent.id,
  }, ambiguousCost.cursor));
  assert.equal(duplicate.result.status, "ambiguous");
});

test("public connector creates one parentless read-only Reviewer with only Candidate/verdict MCP", async () => {
  const world = fakePaseo();
  const host = connector(world.client);
  const base = reviewerArguments();
  const workspace = await host.invoke(command("executionWorkspace.createManaged", {
    ...base, effectKind: "host_view.create", effectId: "effect-review-host-view", workspaceId: undefined,
  }));
  const workspaceId = workspace.result.externalId!;
  const labels = { ...base.labels!, [WORKER_LABEL.executionWorkspace]: workspaceId };
  const createArguments = { ...base, workspaceId, labels };
  const created = await host.invoke(command("reviewerAgent.createWithBootstrap", createArguments));
  assert.equal(created.result.status, "owned_present");
  assert.equal(world.calls.agentCreates, 1);
  const reviewer = world.agents[0]!;
  assert.equal(reviewer.labels["paseo.parent-agent-id"], undefined);
  assert.equal(reviewer.cwd, createArguments.worktreePath);
  const input = world.createdInputs[0] as { config: { featureValues: { permissionMode: string }; toolPolicy: { preapproved: { tool: string }[] } } };
  assert.equal(input.config.featureValues.permissionMode, "read-only");
  assert.deepEqual(input.config.toolPolicy.preapproved.map(({ tool }) => tool), [
    "director_candidate_read", "director_review_verdict_submit",
  ]);

  reviewer.status = "idle";
  reviewer.activeTurn = null;
  const bootstrap = await host.invoke(command("agent.observe", createArguments, created.cursor));
  assert.equal(bootstrap.result.status, "desired");
  const promptArguments: HostCommandArguments = {
    ...createArguments, effectKind: "agent.send_prompt", effectId: "effect-review-prompt",
    agentId: reviewer.id, labels: undefined, initialPrompt: "Review the frozen exact Candidate.",
    notifyOnFinish: true, clientMessageId: "message-review-prompt", sessionBindingSha256: "9".repeat(64),
  };
  const prompted = await host.invoke(command("send_agent_prompt", promptArguments, bootstrap.cursor));
  assert.equal(prompted.result.status, "owned_present");
  assert.equal(world.calls.sends, 1);

  reviewer.status = "idle";
  reviewer.activeTurn = null;
  const completed = await host.invoke(command("agent.observe", promptArguments, prompted.cursor));
  assert.equal(completed.result.status, "desired");
  const archived = await host.invoke(command("agent.archive", {
    ...promptArguments, effectKind: "reviewer_agent.archive", effectId: "effect-reviewer-archive",
    initialPrompt: undefined, clientMessageId: undefined, profile: undefined, session: undefined,
    sessionBindingSha256: undefined,
  }, completed.cursor));
  assert.equal(archived.result.status, "desired");
  assert.equal(world.calls.agentArchives, 1);

  for (const changed of [
    { ...createArguments, profile: { ...createArguments.profile!, permissionMode: "workspace-write" } },
    { ...createArguments, parentAgentId: "task-agent-1" },
    { ...createArguments, session: { ...createArguments.session!, tools: [...createArguments.session!.tools, "director_task_read"] } },
  ]) {
    await assert.rejects(
      host.invoke(command("reviewerAgent.createWithBootstrap", changed)),
      (error: unknown) => error instanceof PaseoHostEffectError && error.code === "HOST_REVIEWER_CREATE_INVALID",
    );
  }
  assert.equal(world.calls.agentCreates, 1, "permission or scope escape must fail before Paseo");
});

test("primary recovery inventory is complete, bounded, and redacts provider failure text", async () => {
  const world = fakePaseo();
  const host = connector(world.client);
  const base = primaryArguments();
  const workspace = await host.invoke(command("executionWorkspace.createManaged", {
    ...base, effectKind: "host_view.create", effectId: "effect-recovery-workspace", workspaceId: undefined,
  }));
  const workspaceId = workspace.result.externalId!;
  const labels = { ...base.labels!, [WORKER_LABEL.executionWorkspace]: workspaceId };
  const createArguments = { ...base, workspaceId, labels };
  const created = await host.invoke(command("taskAgent.createWithBootstrap", createArguments));
  const agent = world.agents[0]!;
  agent.timelineEntries.push({
    item: {
      type: "user_message", text: "fixed bootstrap marker",
      messageId: directorMessageId(createArguments.effectId),
    },
  });
  agent.timelineEntries.push({
    item: { type: "user_message", text: "fixed real work marker", messageId: "message-recovery-real" },
  });
  agent.status = "error";
  agent.activeTurn = null;
  agent.attentionReason = "error";
  agent.lastError = "request denied by policy";
  agent.persistence = { provider: "codex", sessionId: "opaque-session", nativeHandle: "opaque-session", metadata: {} };

  const recoveryArguments: HostCommandArguments = {
    ...createArguments,
    effectKind: "primary_recovery.observe",
    effectId: "effect-primary-recovery-observe",
    agentId: agent.id,
    clientMessageId: "message-recovery-real",
    labels: {
      [WORKER_LABEL.project]: createArguments.scope.projectId,
      [WORKER_LABEL.workspace]: createArguments.scope.workspaceId,
      [WORKER_LABEL.task]: createArguments.scope.taskId,
      [WORKER_LABEL.run]: createArguments.scope.runId,
    },
  };
  const observation = await host.invoke(command("agent.observe", recoveryArguments, created.cursor));
  assert.equal(observation.result.status, "desired");
  assert.equal(observation.result.inventory?.complete, true);
  assert.equal(observation.result.inventory?.workspaces.length, 1);
  assert.deepEqual(observation.result.inventory?.agents[0], {
    agentId: agent.id,
    workspaceId,
    role: "task_agent",
    effectId: createArguments.effectId,
    status: "error",
    activeTurnPresent: false,
    archivedAtPresent: false,
    parentPresent: false,
    titleExact: true,
    worktreeExact: true,
    labelsRunExact: true,
    profileExact: true,
    sessionExact: true,
    bootstrapPresent: true,
    promptPresent: true,
    persistenceReferencePresent: true,
    failureSignals: ["policy_rejection"],
  });
  assert.equal(JSON.stringify(observation).includes(agent.lastError), false);
  assert.equal(world.calls.agentArchives, 0);

  const matrix = [
    ["authentication required", "authentication_rejection"],
    ["unsupported model configuration", "configuration_rejection"],
    ["provider crashed", "provider_terminal"],
    ["temporarily unavailable 503", "transient_service"],
    ["opaque terminal failure", "unclassified"],
  ] as const;
  let cursor = observation.cursor;
  for (const [message, signal] of matrix) {
    agent.lastError = message;
    const next = await host.invoke(command("agent.observe", {
      ...recoveryArguments,
      effectId: `effect-primary-recovery-${signal}`,
    }, cursor));
    cursor = next.cursor;
    assert.deepEqual(next.result.inventory?.agents[0]?.failureSignals, [signal]);
    assert.equal(JSON.stringify(next).includes(message), false);
  }

  world.agents.push({ ...agent, id: "agent-orphan", timelineEntries: [...agent.timelineEntries] });
  const orphan = await host.invoke(command("agent.observe", {
    ...recoveryArguments, effectId: "effect-primary-recovery-orphan",
  }, cursor));
  assert.equal(orphan.result.inventory?.agents.length, 2);
  assert.equal(world.calls.agentArchives, 0, "read-only recovery inventory must not contain resources");
});

test("control observations wait at an active boundary and archive exactly once", async () => {
  const world = fakePaseo();
  const host = connector(world.client);
  const base = primaryArguments();
  const workspace = await host.invoke(command("executionWorkspace.createManaged", {
    ...base, effectKind: "host_view.create", effectId: "effect-control-host-view",
  }));
  const labels = { ...base.labels!, [WORKER_LABEL.executionWorkspace]: workspace.result.externalId! };
  const createArguments = { ...base, workspaceId: workspace.result.externalId, labels };
  const created = await host.invoke(command("taskAgent.createWithBootstrap", createArguments));
  const agent = world.agents[0]!;
  const boundaryArguments: HostCommandArguments = {
    ...createArguments,
    effectKind: "control_agent.observe_safe_boundary",
    effectId: "effect-control-boundary",
    agentId: created.result.externalId,
    initialPrompt: undefined,
    clientMessageId: undefined,
    profile: undefined,
    session: undefined,
  };
  const active = await host.invoke(command("agent.observe", boundaryArguments, created.cursor));
  assert.equal(active.result.status, "owned_present");
  assert.equal(active.result.priorDispatcherAbsent, false);
  assert.equal(world.calls.agentArchives, 0);

  agent.status = "idle";
  agent.activeTurn = null;
  const safe = await host.invoke(command("agent.observe", boundaryArguments, active.cursor));
  assert.equal(safe.result.status, "desired");
  const archiveArguments: HostCommandArguments = {
    ...boundaryArguments,
    effectKind: "control_agent.archive",
    effectId: "effect-control-archive",
  };
  agent.status = "running";
  agent.activeTurn = { turnId: "turn-control", startedAt: "2026-09-10T08:00:00Z" };
  const archived = await host.invoke(command("agent.archive", archiveArguments, safe.cursor));
  assert.equal(archived.result.status, "desired");
  assert.equal(world.calls.agentArchives, 1);
  const replay = await host.invoke(command("agent.archive", archiveArguments, archived.cursor));
  assert.equal(replay.result.status, "desired");
  assert.equal(world.calls.agentArchives, 1);
});

test("connector replacement advances the durable cursor and refuses partial workspace registration", async () => {
  const world = fakePaseo();
  const first = connector(world.client);
  const base = primaryArguments();
  const create = command("executionWorkspace.createManaged", {
    ...base, effectKind: "host_view.create", effectId: "effect-host-view",
  });
  const initial = await first.invoke(create);
  world.workspaces[0] = { ...world.workspaces[0]!, title: "foreign title" };

  const replacement = connector(world.client);
  const observed = await replacement.invoke(command("executionWorkspace.observe", {
    ...create.arguments, workspaceId: initial.result.externalId,
  }, initial.cursor));
  assert.ok(observed.cursor > initial.cursor);
  assert.equal(observed.result.status, "different");
  const replay = await replacement.invoke({ ...create, afterCursor: observed.cursor });
  assert.equal(replay.result.status, "different");
  assert.equal(world.calls.workspaceCreates, 1);

  world.workspaces.push({ ...world.workspaces[0]!, id: "workspace-duplicate" });
  const ambiguous = await replacement.invoke(command("executionWorkspace.observe", {
    ...create.arguments, workspaceId: initial.result.externalId,
  }, replay.cursor));
  assert.equal(ambiguous.result.status, "ambiguous");
});

test("primary creation fails before Paseo when frozen profile or session correlation drifts", async () => {
  const world = fakePaseo();
  const host = connector(world.client);
  const base = primaryArguments();
  const workspace = await host.invoke(command("executionWorkspace.createManaged", {
    ...base, effectKind: "host_view.create", effectId: "effect-host-view",
  }));
  const changed = {
    ...base,
    workspaceId: workspace.result.externalId,
    labels: { ...base.labels!, [WORKER_LABEL.executionWorkspace]: workspace.result.externalId!, [WORKER_LABEL.profile]: "0".repeat(64) },
  };
  await assert.rejects(
    host.invoke(command("taskAgent.createWithBootstrap", changed)),
    (error: unknown) => error instanceof PaseoHostEffectError && error.code === "HOST_PRIMARY_CREATE_INVALID",
  );
  assert.equal(world.calls.agentCreates, 0);
});

test("connector observes and archives only the exact parent-bound helper", async () => {
  const world = fakePaseo();
  const directorHost = connector(world.client);
  const base = primaryArguments();
  const workspace = await directorHost.invoke(command("executionWorkspace.createManaged", {
    ...base, effectKind: "host_view.create", effectId: "effect-helper-host-view",
  }));
  const primaryLabels = { ...base.labels!, [WORKER_LABEL.executionWorkspace]: workspace.result.externalId! };
  const primary = await directorHost.invoke(command("taskAgent.createWithBootstrap", {
    ...base, workspaceId: workspace.result.externalId, labels: primaryLabels,
  }));
  const parent = world.agents.find((agent) => agent.id === primary.result.externalId)!;
  const helperLabels = {
    [WORKER_LABEL.project]: "project-1", [WORKER_LABEL.rootWorkspace]: "wks_root_1",
    [WORKER_LABEL.workspace]: "repository-1", [WORKER_LABEL.executionWorkspace]: workspace.result.externalId!,
    [WORKER_LABEL.task]: "task-1", [WORKER_LABEL.run]: "run-1", [WORKER_LABEL.role]: "helper",
    [WORKER_LABEL.phase]: "building", [WORKER_LABEL.base]: "1".repeat(40),
    [WORKER_LABEL.effect]: "effect-helper-observe", [WORKER_LABEL.profile]: "a".repeat(64),
    [WORKER_LABEL.session]: "7".repeat(64), [WORKER_LABEL.registeredAt]: "2026-09-10T08:00:00Z",
    [WORKER_LABEL.startedAt]: "2026-09-10T08:00:00Z",
  };
  world.agents.push({
    ...parent,
    id: "helper-native-1",
    title: "Helper helper-1",
    labels: { ...helperLabels, "paseo.parent-agent-id": parent.id },
    timelineEntries: [{ item: {
      type: "user_message",
      text: "Director helper bootstrap only. Do not inspect files, call tools, or perform Task work. Finish immediately.",
      clientMessageId: "message-helper-bootstrap",
    } }],
  });
  world.agents.at(-1)!.lastUsage = {
    inputTokens: 9, cachedInputTokens: 4, outputTokens: 3, totalCostUsd: 0.0004,
  };
  const helperArguments: HostCommandArguments = {
    scope: base.scope, effectKind: "helper_agent.observe", effectId: "effect-helper-observe",
    bindingHash: base.bindingHash, worktreeId: "worktree-1", worktreePath: base.worktreePath,
    workspaceId: workspace.result.externalId, title: "Helper helper-1",
    parentAgentId: parent.id, labels: helperLabels,
    initialPrompt: "Director helper bootstrap only. Do not inspect files, call tools, or perform Task work. Finish immediately.",
    clientMessageId: "message-helper-bootstrap",
  };
  const observed = await directorHost.invoke(command("helperAgent.observe", helperArguments));
  assert.equal(observed.result.status, "owned_present");
  assert.equal(observed.result.externalId, "helper-native-1");
  assert.equal(observed.result.correlationHash?.length, 64);
  assert.deepEqual(observed.result.usage, {
    state: "current", sourceRevision: world.agents.at(-1)!.updatedAt,
    inputTokensPresent: true, inputTokens: 9, cachedInputTokens: 4,
    outputTokensPresent: true, outputTokens: 3,
    costMicrousdPresent: true, costMicrousd: 400,
  });

  const wrongParent = await directorHost.invoke(command("helperAgent.observe", {
    ...helperArguments, parentAgentId: "another-parent", effectId: "effect-helper-wrong-parent",
  }, observed.cursor));
  assert.equal(wrongParent.result.status, "different");

  const controlBoundary = await directorHost.invoke(command("agent.observe", {
    ...helperArguments, effectKind: "control_agent.observe_safe_boundary",
    effectId: "effect-helper-control-boundary", agentId: "helper-native-1",
  }, wrongParent.cursor));
  assert.equal(controlBoundary.result.status, "owned_present");
  assert.equal(controlBoundary.result.priorDispatcherAbsent, false);
  const controlArchived = await directorHost.invoke(command("agent.archive", {
    ...helperArguments, effectKind: "control_agent.archive",
    effectId: "effect-helper-control-archive", agentId: "helper-native-1",
  }, controlBoundary.cursor));
  assert.equal(controlArchived.result.status, "desired");
  assert.deepEqual(controlArchived.result.usage, undefined);

  const archiveArguments: HostCommandArguments = {
    ...helperArguments, effectKind: "helper_agent.archive", effectId: "effect-helper-archive",
    agentId: "helper-native-1",
  };
  const archived = await directorHost.invoke(command("agent.archive", archiveArguments, controlArchived.cursor));
  assert.equal(archived.result.status, "desired");
  const helper = world.agents.find((agent) => agent.id === "helper-native-1")!;
  assert.equal(helper.status, "closed");
  assert.ok(helper.archivedAt);

  const replayed = await directorHost.invoke(command("agent.archive", archiveArguments, archived.cursor));
  assert.equal(replayed.result.status, "desired");
  assert.equal(world.agents.filter((agent) => agent.labels["paseo.parent-agent-id"] === parent.id).length, 1);
});
