// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
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
  const calls = { connect: 0, workspaceCreates: 0, agentCreates: 0, sends: 0 };
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
    { async query() { throw new Error("unused"); } },
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

function command(capability: HostCommand["capability"], argumentsValue: HostCommandArguments, afterCursor = 0): HostCommand {
  return {
    requestId: `request-${argumentsValue.effectId}`, idempotencyKey: argumentsValue.effectId,
    expectedVersion: 1, afterCursor, capability, arguments: argumentsValue,
  };
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
  const completedPrompt = await host.invoke(command("agent.observe", promptArguments, replayedPrompt.cursor));
  assert.equal(completedPrompt.result.status, "desired");
  assert.ok(completedPrompt.cursor > replayedPrompt.cursor);

  world.agents.push({
    ...agent,
    id: "agent-duplicate",
    labels: { ...agent.labels },
    timelineEntries: [...agent.timelineEntries],
  });
  const duplicate = await host.invoke(command("agent.observe", {
    ...createArguments,
    agentId: agent.id,
  }, completedPrompt.cursor));
  assert.equal(duplicate.result.status, "ambiguous");
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
