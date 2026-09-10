// SPDX-License-Identifier: Apache-2.0
// Paseo-specific adapter for the engine-owned host port.

import {
  createPaseoClient,
  type PaseoClient,
  type PaseoAgent,
  type PaseoAgentConfig,
  type PaseoWorkspace,
  type PaseoClientConfig,
} from "@getpaseo/client";
import { createHash } from "node:crypto";
import { dirname, isAbsolute, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import {
  assertHostDescriptor,
  EXPECTED_HOST_DESCRIPTOR,
  HOST_CAPABILITIES,
  WORKER_LABEL,
  type DirectorHost,
  type HostCommand,
  type HostDescriptor,
  type HostObservation,
  type HostObservationStatus,
  type HostNativeAgentRecoveryFact,
  type HostPrimaryRecoveryInventory,
  type HostProviderFailureSignal,
  type HostProviderUsage,
} from "../generated/host-contract.shared.ts";
import type { AgentMCPSessionLaunch } from "../generated/agent-mcp-contract.shared.ts";
import type {
  PlanningMutationInput,
  PlanningMutationResult,
  PlanningQueryInput,
  PlanningSnapshot,
  TaskDetailQueryInput,
  TaskDetailSnapshot,
} from "../generated/planning-contract.shared.ts";
import type { ConnectorStartupStatus } from "../rpc/startup.shared.ts";
import { loadConnectorCredential } from "./credential.server.ts";
import { createPaseoSessionMCPInjection } from "./agent-mcp.server.ts";
import {
  createBoardTransport,
  type BoardTransport,
} from "./engine-board.server.ts";
import {
  createPlanningTransport,
  type PlanningTransport,
} from "./engine-planning.server.ts";
import { engineBoundaryPaths } from "./engine-distribution.server.ts";
import {
  selectEngine,
  type EngineSelection,
} from "./engine-selection.server.ts";

export type ConnectorClient = Pick<PaseoClient, "close"> &
  Partial<Pick<PaseoClient, "connect" | "workspaces" | "agents">>;

export class PaseoHostEffectError extends Error {
  readonly code: string;

  constructor(code: string) {
    super(code);
    this.name = "PaseoHostEffectError";
    this.code = code;
  }
}

const HASH_PATTERN = /^[0-9a-f]{64}$/u;
const GIT_SHA_PATTERN = /^[0-9a-f]{40}$/u;
const IDENTITY_PATTERN = /^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,255}$/u;
const MAXIMUM_OBSERVATION_AGE_MILLIS = 30_000;
const MUTATING_CAPABILITIES = new Set([
  "executionWorkspace.createManaged",
  "executionWorkspace.archive",
  "taskAgent.createWithBootstrap",
  "reviewerAgent.createWithBootstrap",
  "send_agent_prompt",
  "agent.archive",
]);

function sortedRecord(input: Readonly<Record<string, string>>): Record<string, string> {
  return Object.fromEntries(Object.keys(input).sort().map((key) => [key, input[key]!]));
}

function sha256(value: string): string {
  return createHash("sha256").update(value).digest("hex");
}

function canonicalValue(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(canonicalValue);
  if (value === null || typeof value !== "object") return value;
  return Object.fromEntries(
    Object.entries(value as Record<string, unknown>)
      .filter(([, entry]) => entry !== undefined)
      .sort(([left], [right]) => left < right ? -1 : left > right ? 1 : 0)
      .map(([key, entry]) => [key, canonicalValue(entry)]),
  );
}

function labelDigest(labels: Readonly<Record<string, string>>): string {
  return sha256(JSON.stringify(sortedRecord(labels)));
}

function paseoProvider(provider: string): string {
  switch (provider) {
    case "codex":
      return "codex";
    case "claude-code":
      return "claude";
    case "opencode":
      return "opencode";
    default:
      throw new PaseoHostEffectError("HOST_PROVIDER_UNSUPPORTED");
  }
}

function resultDigest(result: Omit<HostObservation["result"], "factHash">): string {
  const wire: Record<string, unknown> = {
    effectId: result.effectId,
    status: result.status,
    ...(result.externalId ? { externalId: result.externalId } : {}),
    bindingHash: result.bindingHash,
    ...(result.correlationHash ? { correlationHash: result.correlationHash } : {}),
    priorDispatcherAbsent: result.priorDispatcherAbsent,
    maximumAgeMillis: result.maximumAgeMillis,
    ...(result.usage ? { usage: result.usage } : {}),
    ...(result.nativeAgent ? { nativeAgent: result.nativeAgent } : {}),
    ...(result.inventory ? { inventory: result.inventory } : {}),
    factHash: "",
  };
  return sha256(JSON.stringify(wire));
}

function exactCostMicrousd(value: number): number | null {
  if (!Number.isFinite(value) || value < 0) return null;
  const decimal = String(value);
  if (!/^(?:0|[1-9][0-9]*)(?:\.[0-9]{1,6})?$/u.test(decimal)) return null;
  const [whole, fraction = ""] = decimal.split(".");
  const result = Number(BigInt(whole!) * 1_000_000n + BigInt(fraction.padEnd(6, "0")));
  return Number.isSafeInteger(result) ? result : null;
}

function providerUsage(agent: PaseoAgent): HostProviderUsage {
  const usage = agent.lastUsage;
  if (!usage) {
    return {
      state: "unavailable", inputTokensPresent: false, inputTokens: 0,
      cachedInputTokens: 0, outputTokensPresent: false, outputTokens: 0,
      costMicrousdPresent: false, costMicrousd: 0,
    };
  }
  const validToken = (value: number | undefined) =>
    value === undefined || (Number.isSafeInteger(value) && value >= 0);
  const cost = usage.totalCostUsd === undefined ? undefined : exactCostMicrousd(usage.totalCostUsd);
  if (
    !validToken(usage.inputTokens) || !validToken(usage.cachedInputTokens) ||
    !validToken(usage.outputTokens) || cost === null
  ) {
    return {
      state: "ambiguous", inputTokensPresent: false, inputTokens: 0,
      cachedInputTokens: 0, outputTokensPresent: false, outputTokens: 0,
      costMicrousdPresent: false, costMicrousd: 0,
    };
  }
  if (usage.inputTokens === undefined || usage.outputTokens === undefined) {
    return {
      state: "unavailable", inputTokensPresent: false, inputTokens: 0,
      cachedInputTokens: 0, outputTokensPresent: false, outputTokens: 0,
      costMicrousdPresent: false, costMicrousd: 0,
    };
  }
  return {
    state: "current",
    sourceRevision: agent.updatedAt,
    inputTokensPresent: true,
    inputTokens: usage.inputTokens,
    cachedInputTokens: usage.cachedInputTokens ?? 0,
    outputTokensPresent: true,
    outputTokens: usage.outputTokens,
    costMicrousdPresent: cost !== undefined,
    costMicrousd: cost ?? 0,
  };
}

function exactLabels(
  actual: Readonly<Record<string, string>>,
  expected: Readonly<Record<string, string>>,
): boolean {
  return JSON.stringify(sortedRecord(actual)) === JSON.stringify(sortedRecord(expected));
}

function exactHelperLabels(
  actual: Readonly<Record<string, string>>,
  expected: Readonly<Record<string, string>>,
): boolean {
  const directorLabels = Object.fromEntries(
    Object.entries(actual).filter(([key]) => key !== "paseo.parent-agent-id"),
  );
  return exactLabels(directorLabels, expected);
}

function pageCursor(page: unknown): string | undefined {
  if (page === null || typeof page !== "object") return undefined;
  const value = page as { hasMore?: unknown; nextCursor?: unknown };
  if (value.hasMore !== true) return undefined;
  if (typeof value.nextCursor !== "string" || value.nextCursor.length === 0) {
    throw new PaseoHostEffectError("HOST_DIRECTORY_CURSOR_INVALID");
  }
  return value.nextCursor;
}

export type ConnectorDependencies = {
  createClient?: (configuration: PaseoClientConfig) => ConnectorClient;
  boardTransport?: BoardTransport;
  planningTransport?: PlanningTransport;
};

export class PaseoHostConnector implements DirectorHost {
  readonly #client: ConnectorClient;
  readonly #selection: EngineSelection;
  readonly #boardTransport: BoardTransport;
  readonly #planningTransport: PlanningTransport;
  readonly #ready: Promise<void>;
  readonly #inflight = new Map<string, { digest: string; promise: Promise<HostObservation> }>();
  #cursor = 0;

  constructor(
    client: ConnectorClient,
    selection: EngineSelection,
    boardTransport: BoardTransport,
    planningTransport: PlanningTransport,
  ) {
    this.#client = client;
    this.#selection = selection;
    this.#boardTransport = boardTransport;
    this.#planningTransport = planningTransport;
    assertHostDescriptor(EXPECTED_HOST_DESCRIPTOR);
    this.#ready = client.connect ? client.connect() : Promise.resolve();
  }

  async describe(): Promise<HostDescriptor> {
    return EXPECTED_HOST_DESCRIPTOR;
  }

  #observation(
    command: HostCommand,
    status: HostObservationStatus,
    externalId = "",
    correlationHash = "",
    priorDispatcherAbsent = true,
    usage?: HostProviderUsage,
    nativeAgent?: HostNativeAgentRecoveryFact,
    inventory?: HostPrimaryRecoveryInventory,
  ): HostObservation {
    const after = command.afterCursor ?? 0;
    this.#cursor = Math.max(this.#cursor + 1, after + 1);
    const resultWithoutHash = {
      effectId: command.arguments.effectId,
      status,
      ...(externalId ? { externalId } : {}),
      bindingHash: command.arguments.bindingHash,
      ...(correlationHash ? { correlationHash } : {}),
      priorDispatcherAbsent,
      maximumAgeMillis: MAXIMUM_OBSERVATION_AGE_MILLIS,
      ...(usage ? { usage } : {}),
      ...(nativeAgent ? { nativeAgent } : {}),
      ...(inventory ? { inventory } : {}),
    } as const;
    return {
      requestId: command.requestId,
      cursor: this.#cursor,
      observedAt: new Date().toISOString(),
      result: { ...resultWithoutHash, factHash: resultDigest(resultWithoutHash) },
    };
  }

  #roots(): Pick<PaseoClient, "workspaces" | "agents"> {
    if (!this.#client.workspaces || !this.#client.agents) {
      throw new PaseoHostEffectError("HOST_PUBLIC_SDK_UNAVAILABLE");
    }
    return { workspaces: this.#client.workspaces, agents: this.#client.agents };
  }

  async #workspaces(): Promise<PaseoWorkspace[]> {
    const { workspaces } = this.#roots();
    const result: PaseoWorkspace[] = [];
    let cursor: string | undefined;
    do {
      const page = await workspaces.list({
        page: { limit: 100, ...(cursor ? { cursor } : {}) },
      });
      result.push(...page.entries);
      if (result.length > 1_000) {
        throw new PaseoHostEffectError("HOST_DIRECTORY_LIMIT_EXCEEDED");
      }
      cursor = pageCursor(page.pageInfo);
    } while (cursor);
    return result;
  }

  async #workspace(command: HostCommand): Promise<HostObservation> {
    const argumentsValue = command.arguments;
    const expectedTitle = `${argumentsValue.title ?? argumentsValue.scope.taskId} execution workspace`;
    let matches: PaseoWorkspace[];
    if (argumentsValue.workspaceId && argumentsValue.effectKind === "host_view.archive") {
      const current = await this.#roots().workspaces
        .ref(argumentsValue.workspaceId)
        .refresh({ requestId: `${command.requestId}-workspace` });
      matches = current ? [current] : [];
    } else {
      const listed = await this.#workspaces();
      matches = listed.filter(
        (workspace) =>
          workspace.workspaceDirectory === argumentsValue.worktreePath ||
          workspace.projectRootPath === argumentsValue.worktreePath,
      );
    }
    if (matches.length === 0) {
      return this.#observation(
        command,
        command.arguments.effectKind === "host_view.archive" ? "desired" : "absent",
        command.arguments.effectKind === "host_view.archive"
          ? (argumentsValue.workspaceId ?? "")
          : "",
      );
    }
    if (matches.length !== 1) return this.#observation(command, "ambiguous");
    const workspace = matches[0]!;
    if (
      workspace.id !== (argumentsValue.workspaceId || workspace.id) ||
      workspace.title !== expectedTitle ||
      workspace.workspaceDirectory !== argumentsValue.worktreePath ||
      workspace.workspaceKind !== "worktree" ||
      workspace.gitRuntime?.isPaseoOwnedWorktree !== false
    ) {
      return this.#observation(command, "different", workspace.id);
    }
    if (command.arguments.effectKind === "host_view.archive") {
      return this.#observation(
        command,
        workspace.archivingAt ? "desired" : "owned_present",
        workspace.id,
      );
    }
    return this.#observation(command, "desired", workspace.id);
  }

  async #agents(command: HostCommand): Promise<PaseoAgent[]> {
    const { agents } = this.#roots();
    const labels = command.arguments.labels;
    if (command.arguments.agentId && !labels) {
      const refreshed = await agents
        .ref(command.arguments.agentId)
        .refresh(`${command.requestId}-agent`);
      return refreshed?.agent ? [refreshed.agent] : [];
    }
    if (!labels) return [];
    const result: PaseoAgent[] = [];
    let cursor: string | undefined;
    do {
      const page = await agents.list({
        filter: { labels: { ...labels }, includeArchived: true },
        page: { limit: 100, ...(cursor ? { cursor } : {}) },
      });
      result.push(...page.entries.map(({ agent }) => agent));
      if (result.length > 1_000) {
        throw new PaseoHostEffectError("HOST_DIRECTORY_LIMIT_EXCEEDED");
      }
      cursor = pageCursor(page.pageInfo);
    } while (cursor);
    return result;
  }

  async #promptPresent(agentId: string, messageId: string, requestId: string): Promise<boolean> {
    const timeline = await this.#roots().agents.ref(agentId).timeline.refetch({
      projection: "canonical",
      direction: "tail",
      limit: 200,
      requestId,
    });
    if (timeline.error || timeline.gap || timeline.staleCursor) {
      throw new PaseoHostEffectError("HOST_TIMELINE_UNAVAILABLE");
    }
    return timeline.entries.some(({ item }) =>
      item.type === "user_message" &&
      (item.messageId === messageId || item.clientMessageId === messageId),
    );
  }

  #failureSignals(agent: PaseoAgent): readonly HostProviderFailureSignal[] {
    const value = (agent.lastError ?? "").slice(0, 4_096).toLowerCase();
    if (agent.pendingPermissions.length > 0 || agent.attentionReason === "permission") {
      return ["unclassified"];
    }
    if (!value && agent.status !== "error" && agent.attentionReason !== "error") {
      return ["none"];
    }
    const signals = new Set<HostProviderFailureSignal>();
    if (/\b(?:401|unauthori[sz]ed|authentication required|login required|invalid api key|expired credential)\b/u.test(value)) {
      signals.add("authentication_rejection");
    }
    if (/\b(?:configuration|invalid option|unsupported model|model not found|provider not configured)\b/u.test(value)) {
      signals.add("configuration_rejection");
    }
    if (/\b(?:policy|permission denied|not allowed|approval required|sandbox|tool policy|tool denied)\b/u.test(value)) {
      signals.add("policy_rejection");
    }
    if (/\b(?:timeout|temporarily unavailable|rate limit|429|502|503|connection reset)\b/u.test(value)) {
      signals.add("transient_service");
    }
    if (/\b(?:session (?:terminated|closed|corrupt)|provider (?:exited|crashed)|process exited)\b/u.test(value)) {
      signals.add("provider_terminal");
    }
    if (signals.size === 0) signals.add("unclassified");
    return [...signals].sort();
  }

  #normalizedStatus(agent: PaseoAgent): HostNativeAgentRecoveryFact["status"] {
    if (agent.status === "idle" || agent.status === "running" || agent.status === "initializing" ||
      agent.status === "closed" || agent.status === "error") {
      return agent.status;
    }
    return "error";
  }

  async #recoveryInventory(command: HostCommand): Promise<HostObservation> {
    const value = command.arguments;
    if (!value.workspaceId || !value.agentId || !value.worktreePath || !value.title || !value.labels ||
      !value.profile || !value.session || !value.clientMessageId) {
      throw new PaseoHostEffectError("HOST_PRIMARY_RECOVERY_CONTEXT_REQUIRED");
    }
    const workspaceById = new Map<string, PaseoWorkspace>();
    for (const workspace of await this.#workspaces()) {
      if (workspace.workspaceDirectory === value.worktreePath || workspace.projectRootPath === value.worktreePath ||
        workspace.id === value.workspaceId) {
        workspaceById.set(workspace.id, workspace);
      }
    }
    const refreshedWorkspace = await this.#roots().workspaces.ref(value.workspaceId)
      .refresh({ requestId: `${command.requestId}-recovery-workspace` });
    if (refreshedWorkspace) workspaceById.set(refreshedWorkspace.id, refreshedWorkspace);
    const workspaces = [...workspaceById.values()].map((workspace) => ({
      workspaceId: workspace.id,
      active: !workspace.archivingAt,
      archived: Boolean(workspace.archivingAt),
      worktreeExact: workspace.workspaceDirectory === value.worktreePath,
      titleExact: workspace.title === `${value.title} execution workspace`,
      kindExact: workspace.workspaceKind === "worktree" && workspace.gitRuntime?.isPaseoOwnedWorktree === false,
    })).sort((left, right) => left.workspaceId.localeCompare(right.workspaceId));

    const agents: HostNativeAgentRecoveryFact[] = [];
    for (const agent of await this.#agents(command)) {
      const effectId = agent.labels[WORKER_LABEL.effect] || "unbound";
      const roleLabel = agent.labels[WORKER_LABEL.role];
      const role = roleLabel === "reviewer" ? "reviewer" : roleLabel === "helper" ? "helper" : "task_agent";
      let bootstrapPresent = false;
      let promptPresent = false;
      if (agent.id === value.agentId && effectId !== "unbound" && role !== "helper") {
        bootstrapPresent = await this.#promptPresent(
          agent.id,
          `message-${sha256(`${effectId}`).slice(0, 32)}`,
          `${command.requestId}-recovery-bootstrap-${agent.id}`,
        );
      }
      if (agent.id === value.agentId) {
        promptPresent = await this.#promptPresent(
          agent.id,
          value.clientMessageId,
          `${command.requestId}-recovery-prompt-${agent.id}`,
        );
      }
      agents.push({
        agentId: agent.id,
        workspaceId: agent.workspaceId ?? "unbound",
        role,
        effectId,
        status: this.#normalizedStatus(agent),
        activeTurnPresent: Boolean(agent.activeTurn),
        archivedAtPresent: Boolean(agent.archivedAt),
        parentPresent: Boolean(agent.labels["paseo.parent-agent-id"]),
        titleExact: agent.title === value.title,
        worktreeExact: agent.cwd === value.worktreePath,
        labelsRunExact: Object.entries(value.labels).every(([key, expected]) => agent.labels[key] === expected),
        profileExact: agent.labels[WORKER_LABEL.profile] === value.profile.sha256 &&
          (agent.provider === paseoProvider(value.profile.provider) || agent.provider.startsWith(`${paseoProvider(value.profile.provider)}/`)) &&
          (agent.model === null || agent.model === value.profile.model),
        sessionExact: agent.labels[WORKER_LABEL.session] === value.session.sessionSha256,
        bootstrapPresent,
        promptPresent,
        persistenceReferencePresent: agent.persistence !== null,
        failureSignals: this.#failureSignals(agent),
      });
    }
    agents.sort((left, right) => left.agentId.localeCompare(right.agentId));
    const inventory: HostPrimaryRecoveryInventory = { complete: true, workspaces, agents };
    return this.#observation(command, "desired", value.agentId, "", true, undefined, undefined, inventory);
  }

  async #agent(command: HostCommand): Promise<HostObservation> {
    const argumentsValue = command.arguments;
    const matches = await this.#agents(command);
    if (matches.length === 0) return this.#observation(command, "absent");
    if (matches.length !== 1) return this.#observation(command, "ambiguous");
    const agent = matches[0]!;
    const expectedLabels = argumentsValue.labels;
    const correlation = expectedLabels ? labelDigest(expectedLabels) : "";
    const profile = argumentsValue.profile;
    const exact =
      agent.id === (argumentsValue.agentId || agent.id) &&
      agent.workspaceId === argumentsValue.workspaceId &&
      agent.cwd === argumentsValue.worktreePath &&
      agent.title === argumentsValue.title &&
      agent.labels["paseo.parent-agent-id"] === argumentsValue.parentAgentId &&
      (!expectedLabels || exactLabels(agent.labels, expectedLabels)) &&
      (!profile ||
        ((agent.provider === paseoProvider(profile.provider) || agent.provider.startsWith(`${paseoProvider(profile.provider)}/`)) &&
          (agent.model === null || agent.model === profile.model)));
    if (!exact) return this.#observation(command, "different", agent.id, correlation);
    if (
      command.arguments.effectKind === "task_agent.archive" ||
      command.arguments.effectKind === "reviewer_agent.archive" ||
      command.arguments.effectKind === "control_agent.archive"
    ) {
      if (agent.status === "closed" && agent.archivedAt) {
        return this.#observation(command, "desired", agent.id, correlation);
      }
      return this.#observation(command, "owned_present", agent.id, correlation);
    }
    if (command.arguments.effectKind === "control_agent.observe_safe_boundary") {
      if (agent.status === "closed" && agent.archivedAt) {
        return this.#observation(command, "desired", agent.id, correlation);
      }
      if (agent.pendingPermissions.length > 0 || agent.attentionReason === "permission") {
        return this.#observation(command, "permission", agent.id, correlation, true, providerUsage(agent));
      }
      if (agent.status === "error" || agent.attentionReason === "error") {
        return this.#observation(command, "errored", agent.id, correlation, true, providerUsage(agent));
      }
      if (agent.status === "running" || agent.status === "initializing" || agent.activeTurn) {
        return this.#observation(command, "owned_present", agent.id, correlation, false, providerUsage(agent));
      }
      if (agent.status === "idle") {
        return this.#observation(command, "desired", agent.id, correlation, true, providerUsage(agent));
      }
      return this.#observation(command, "ambiguous", agent.id, correlation);
    }
    if (!argumentsValue.clientMessageId) {
      return this.#observation(command, "different", agent.id, correlation);
    }
    const promptPresent = await this.#promptPresent(
      agent.id,
      argumentsValue.clientMessageId,
      `${command.requestId}-timeline`,
    );
    if (!promptPresent) return this.#observation(command, "absent", agent.id, correlation);
    if (agent.pendingPermissions.length > 0 || agent.attentionReason === "permission") {
      return this.#observation(command, "permission", agent.id, correlation, true, providerUsage(agent));
    }
    if (agent.status === "error" || agent.attentionReason === "error") {
      return this.#observation(command, "errored", agent.id, correlation, true, providerUsage(agent));
    }
    if (agent.status === "running" || agent.status === "initializing" || agent.activeTurn) {
      return this.#observation(command, "owned_present", agent.id, correlation, false, providerUsage(agent));
    }
    if (agent.status === "idle") {
      return this.#observation(command, "desired", agent.id, correlation, true, providerUsage(agent));
    }
    return this.#observation(command, "ambiguous", agent.id, correlation);
  }

  async #helper(command: HostCommand): Promise<HostObservation> {
    const value = command.arguments;
    const correlation = value.labels ? labelDigest(value.labels) : "";
    if (!value.labels || !value.parentAgentId || !value.workspaceId || !value.title) {
      throw new PaseoHostEffectError("HOST_HELPER_OBSERVE_INVALID");
    }
    const matches = await this.#agents(command);
    if (matches.length === 0) return this.#observation(command, "absent", "", correlation);
    if (matches.length !== 1) return this.#observation(command, "ambiguous", "", correlation);
    const agent = matches[0]!;
    const exact =
      agent.id === (value.agentId || agent.id) &&
      agent.workspaceId === value.workspaceId &&
      agent.cwd === value.worktreePath &&
      agent.title === value.title &&
      agent.labels["paseo.parent-agent-id"] === value.parentAgentId &&
      exactHelperLabels(agent.labels, value.labels);
    if (!exact) return this.#observation(command, "different", agent.id, correlation);
    if (!value.clientMessageId || value.initialPrompt !==
      "Director helper bootstrap only. Do not inspect files, call tools, or perform Task work. Finish immediately.") {
      throw new PaseoHostEffectError("HOST_HELPER_BOOTSTRAP_INVALID");
    }
    const bootstrapPresent = await this.#promptPresent(
      agent.id,
      value.clientMessageId,
      `${command.requestId}-helper-timeline`,
    );
    if (!bootstrapPresent) return this.#observation(command, "different", agent.id, correlation);
    if (
      (value.effectKind === "helper_agent.archive" || value.effectKind === "control_agent.archive") &&
      !(agent.status === "closed" && agent.archivedAt)
    ) {
      return this.#observation(command, "owned_present", agent.id, correlation, false);
    }
    const usage = value.effectKind === "helper_agent.observe" ||
      value.effectKind === "control_agent.observe_safe_boundary" ||
      value.effectKind === "control_agent.archive"
      ? providerUsage(agent)
      : undefined;
    if (agent.pendingPermissions.length > 0 || agent.attentionReason === "permission") {
      return this.#observation(command, "permission", agent.id, correlation, true, usage);
    }
    if (agent.status === "error" || agent.attentionReason === "error") {
      return this.#observation(command, "errored", agent.id, correlation, true, usage);
    }
    if (agent.status === "closed") {
      return this.#observation(
        command,
        agent.archivedAt ? "desired" : "ambiguous",
        agent.id,
        correlation,
        Boolean(agent.archivedAt),
        usage,
      );
    }
    if (agent.status === "idle") return this.#observation(command, "desired", agent.id, correlation, true, usage);
    if (agent.status === "running" || agent.status === "initializing" || agent.activeTurn) {
      return this.#observation(command, "owned_present", agent.id, correlation, false, usage);
    }
    return this.#observation(command, "ambiguous", agent.id, correlation);
  }

  #createInput(command: HostCommand): PaseoAgentConfig {
    const profile = command.arguments.profile;
    const session = command.arguments.session;
    if (!profile || !session) {
      throw new PaseoHostEffectError("HOST_PRIMARY_CONTEXT_REQUIRED");
    }
    const injection = createPaseoSessionMCPInjection(session as AgentMCPSessionLaunch);
    const options = Object.fromEntries(
      profile.providerOptions.map(({ name, value }) => [name, value]),
    );
    return {
      provider: `${paseoProvider(profile.provider)}/${profile.model}`,
      modeId: profile.mode,
      thinkingOptionId: profile.effort,
      featureValues: { permissionMode: profile.permissionMode },
      options,
      mcpServers: Object.fromEntries(
        Object.entries(injection.mcpServers).map(([name, server]) => [
          name,
          {
            type: server.type,
            command: server.command,
            args: [...server.args],
            env: { ...server.env },
          },
        ]),
      ),
      toolPolicy: {
        preapproved: injection.toolPolicy.preapproved.map((entry) => ({ ...entry })),
      },
    };
  }

  #assertCreate(command: HostCommand): void {
    const value = command.arguments;
    const reviewer = command.capability === "reviewerAgent.createWithBootstrap";
    const expectedCapability = reviewer
      ? "reviewerAgent.createWithBootstrap"
      : "taskAgent.createWithBootstrap";
    const expectedEffectKind = reviewer
      ? "reviewer_agent.create_with_bootstrap"
      : "task_agent.create_with_bootstrap";
    const expectedRole = reviewer ? "reviewer" : "worker";
    const expectedReviewerTools = ["director_candidate_read", "director_review_verdict_submit"];
    const labelKeys = value.labels ? Object.keys(value.labels) : [];
    const expectedLabelKeys = new Set<string>(Object.values(WORKER_LABEL));
    const requiredLabels = Object.entries(WORKER_LABEL)
      .filter(([name]) => name !== "candidate")
      .map(([, label]) => label);
    const environmentKeys = value.session ? Object.keys(value.session.server.env) : [];
    if (
      command.capability !== expectedCapability ||
      value.effectKind !== expectedEffectKind ||
      value.parentAgentId !== undefined ||
      value.initialPrompt !==
        "Director bootstrap only. Do not inspect files, call tools, or perform Task or Review work. Finish this turn immediately." ||
      value.notifyOnFinish === true ||
      !value.preparationReady ||
      !HASH_PATTERN.test(value.preparationBarrierHash ?? "") ||
      !HASH_PATTERN.test(value.lifecycleDigest ?? "") ||
      !HASH_PATTERN.test(value.isolationDigest ?? "") ||
      !IDENTITY_PATTERN.test(value.boundaryId ?? "") ||
      !IDENTITY_PATTERN.test(value.operationalObservationId ?? "") ||
      !HASH_PATTERN.test(value.profile?.sha256 ?? "") ||
      !HASH_PATTERN.test(value.session?.sessionSha256 ?? "") ||
      value.sessionBindingSha256 !== undefined ||
      !value.labels ||
      value.labels[WORKER_LABEL.effect] !== value.effectId ||
      value.labels[WORKER_LABEL.profile] !== value.profile?.sha256 ||
      value.labels[WORKER_LABEL.session] !== value.session?.sessionSha256 ||
      value.labels[WORKER_LABEL.executionWorkspace] !== value.workspaceId ||
      requiredLabels.some((label) => !IDENTITY_PATTERN.test(value.labels?.[label] ?? "")) ||
      labelKeys.some((key) => !expectedLabelKeys.has(key)) ||
      (reviewer && !GIT_SHA_PATTERN.test(value.labels?.[WORKER_LABEL.candidate] ?? "")) ||
      value.labels[WORKER_LABEL.role] !== (reviewer ? "reviewer" : "task-agent") ||
      value.labels[WORKER_LABEL.phase] !== (reviewer ? "reviewing" : "building") ||
      value.session?.role !== expectedRole ||
      value.session?.provider !== value.profile?.provider ||
      value.session?.model !== value.profile?.model ||
      (reviewer && JSON.stringify(value.session?.tools) !== JSON.stringify(expectedReviewerTools)) ||
      (reviewer && value.profile?.permissionMode !== "read-only") ||
      !isAbsolute(value.session?.server.command ?? "") ||
      value.session?.server.args.some((argument) =>
        /^(?:--?)(?:token|secret|password|credential|authorization|github|paseo|socket)(?:=|$)|(?:gh[pousr]_|sk-)[A-Za-z0-9_-]{16,}/iu.test(
          argument,
        ),
      ) ||
      environmentKeys.length !== 0
    ) {
      throw new PaseoHostEffectError(reviewer ? "HOST_REVIEWER_CREATE_INVALID" : "HOST_PRIMARY_CREATE_INVALID");
    }
  }

  async #invokeOnce(command: HostCommand): Promise<HostObservation> {
    await this.#ready;
    if (
      !IDENTITY_PATTERN.test(command.requestId) ||
      !IDENTITY_PATTERN.test(command.idempotencyKey) ||
      !Number.isSafeInteger(command.expectedVersion) ||
      command.expectedVersion < 0 ||
      !IDENTITY_PATTERN.test(command.arguments.effectId) ||
      !HASH_PATTERN.test(command.arguments.bindingHash)
    ) {
      throw new PaseoHostEffectError("HOST_COMMAND_INVALID");
    }
    switch (command.capability) {
      case "executionWorkspace.observe":
        return this.#workspace(command);
      case "executionWorkspace.createManaged": {
        const observed = await this.#workspace(command);
        if (observed.result.status !== "absent") return observed;
        const value = command.arguments;
        if (!value.worktreePath || !value.worktreeId || !value.title || !value.lifecycleDigest) {
          throw new PaseoHostEffectError("HOST_WORKSPACE_CREATE_INVALID");
        }
        const workspace = await this.#roots().workspaces.create({
          requestId: command.idempotencyKey,
          title: `${value.title} execution workspace`,
          source: { kind: "directory", path: value.worktreePath },
        });
        return this.#observation(command, "desired", workspace.id);
      }
      case "taskAgent.createWithBootstrap":
      case "reviewerAgent.createWithBootstrap": {
        this.#assertCreate(command);
        const observed = await this.#agent(command);
        if (observed.result.status !== "absent") return observed;
        const value = command.arguments;
        const workspaceFact = await this.#roots().workspaces.ref(value.workspaceId!)
          .refresh({ requestId: `${command.requestId}-create-workspace` });
        if (!workspaceFact || workspaceFact.archivingAt || workspaceFact.title !== `${value.title} execution workspace` ||
          workspaceFact.workspaceDirectory !== value.worktreePath ||
          workspaceFact.workspaceKind !== "worktree" || workspaceFact.gitRuntime?.isPaseoOwnedWorktree !== false) {
          throw new PaseoHostEffectError("HOST_PRIMARY_WORKSPACE_NOT_ACTIVE");
        }
        const workspace = this.#roots().workspaces.ref(value.workspaceId!);
        const agent = await workspace.agents.create({
          config: this.#createInput(command),
          title: value.title,
          labels: { ...value.labels! },
          prompt: value.initialPrompt,
          clientMessageId: value.clientMessageId,
          requestId: command.idempotencyKey,
          autoArchive: false,
        });
        return this.#observation(command, "owned_present", agent.id, labelDigest(value.labels!), false);
      }
      case "agent.observe":
        if (command.arguments.effectKind === "primary_recovery.observe") {
          return this.#recoveryInventory(command);
        }
        return command.arguments.effectKind.startsWith("control_agent.") && command.arguments.parentAgentId
          ? this.#helper(command)
          : this.#agent(command);
      case "helperAgent.observe":
        return this.#helper(command);
      case "send_agent_prompt": {
        const value = command.arguments;
        if (
          value.effectKind !== "agent.send_prompt" ||
          !value.notifyOnFinish ||
          !value.agentId ||
          !value.initialPrompt ||
          value.initialPrompt.startsWith("Director bootstrap only.") ||
          !HASH_PATTERN.test(value.sessionBindingSha256 ?? "") ||
          !value.clientMessageId
        ) {
          throw new PaseoHostEffectError("HOST_PRIMARY_PROMPT_INVALID");
        }
        const observed = await this.#agent(command);
        if (observed.result.status !== "absent") return observed;
        await this.#roots().agents.ref(value.agentId).send(value.initialPrompt, {
          messageId: value.clientMessageId,
        });
        return this.#observation(command, "owned_present", value.agentId, "", false);
      }
      case "agent.archive": {
        const observed = command.arguments.effectKind === "helper_agent.archive" ||
          (command.arguments.effectKind === "control_agent.archive" && command.arguments.parentAgentId)
          ? await this.#helper(command)
          : await this.#agent(command);
        if (observed.result.status !== "owned_present") return observed;
        await this.#roots().agents.ref(command.arguments.agentId!).archive();
        return this.#observation(command, "desired", command.arguments.agentId!);
      }
      case "executionWorkspace.archive": {
        const observed = await this.#workspace(command);
        if (observed.result.status !== "owned_present") return observed;
        await this.#roots().workspaces
          .ref(command.arguments.workspaceId!)
          .archive(command.idempotencyKey);
        return this.#observation(command, "desired", command.arguments.workspaceId!);
      }
      default:
        throw new PaseoHostEffectError("HOST_CAPABILITY_UNSUPPORTED");
    }
  }

  async invoke(command: HostCommand): Promise<HostObservation> {
    if (!MUTATING_CAPABILITIES.has(command.capability)) {
      return this.#invokeOnce(command);
    }
    const digest = sha256(JSON.stringify(canonicalValue(command)));
    const current = this.#inflight.get(command.idempotencyKey);
    if (current) {
      if (current.digest !== digest) {
        throw new PaseoHostEffectError("HOST_IDEMPOTENCY_CONFLICT");
      }
      return current.promise;
    }
    const promise = this.#invokeOnce(command);
    this.#inflight.set(command.idempotencyKey, { digest, promise });
    try {
      return await promise;
    } finally {
      if (this.#inflight.get(command.idempotencyKey)?.promise === promise) {
        this.#inflight.delete(command.idempotencyKey);
      }
    }
  }

  status(): ConnectorStartupStatus {
    return {
      state: "board-ready",
      engineMode: this.#selection.mode,
      productBehavior: true,
      descriptor: {
        ...EXPECTED_HOST_DESCRIPTOR,
        capabilities: [...HOST_CAPABILITIES],
      },
    };
  }

  async loadBoard() {
    return this.#boardTransport.load();
  }

  async queryPlanning(input: PlanningQueryInput): Promise<PlanningSnapshot> {
    return this.#planningTransport.query(input);
  }

  async queryPlanningTask(
    _input: TaskDetailQueryInput,
  ): Promise<TaskDetailSnapshot> {
    throw new Error(
      "PLANNING_SURFACE_NOT_WIRED: runtime task-detail queries are owned by later M2 Tasks",
    );
  }

  async mutatePlanning(
	input: PlanningMutationInput,
  ): Promise<PlanningMutationResult> {
	return this.#planningTransport.mutate(input);
  }

  async close(): Promise<void> {
    await this.#client.close();
  }
}

export function startConnectorShell(options: {
  environment: NodeJS.ProcessEnv;
  checkoutRoot: string;
  dependencies?: ConnectorDependencies;
}): PaseoHostConnector {
  const selection = selectEngine(options.environment, options.checkoutRoot);
  const credential = loadConnectorCredential({
    credentialPath: options.environment.DIRECTOR_PASEO_CREDENTIAL_FILE,
    checkoutRoot: options.checkoutRoot,
    disjointEnginePaths: engineBoundaryPaths(selection),
  });
  const url = options.environment.DIRECTOR_PASEO_URL;
  if (!url) {
    throw new Error("DIRECTOR_PASEO_URL is required");
  }
  const boardTransport =
    options.dependencies?.boardTransport ??
    createBoardTransport({
      baseUrl: options.environment.DIRECTOR_ENGINE_URL,
    });
  const planningTransport =
    options.dependencies?.planningTransport ??
    createPlanningTransport({
      baseUrl: options.environment.DIRECTOR_ENGINE_URL,
      mutationActor: {
        kind: "human",
        id: "local-project-owner",
        sessionId: `paseo-connector-${process.pid}`,
      },
    });
  const createClient = options.dependencies?.createClient ?? createPaseoClient;
  const client = createClient({
    url,
    password: credential,
    clientId: `director-connector-${process.pid}`,
    reconnect: { enabled: false },
  });
  return new PaseoHostConnector(
    client,
    selection,
    boardTransport,
    planningTransport,
  );
}

export function startConnectorShellFromEnvironment(): PaseoHostConnector {
  return startConnectorShell({
    environment: process.env,
    checkoutRoot: resolve(dirname(fileURLToPath(import.meta.url)), ".."),
  });
}
