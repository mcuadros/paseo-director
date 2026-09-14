// SPDX-License-Identifier: Apache-2.0
// Paseo-specific adapter for the engine-owned host port.

import {
  createPaseoClient,
  type PaseoApi,
  type PaseoClient,
  type PaseoAgent,
  type PaseoAgentConfig,
  type PaseoAgentUpdate,
  type PaseoProject,
  type PaseoWorkspace,
  type PaseoClientConfig,
} from "@getpaseo/client";
import { createHash } from "node:crypto";
import { basename, dirname, isAbsolute, join, resolve } from "node:path";

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
import {
  PLANNING_CONTRACT_SHA256,
  PLANNING_CONTRACT_VERSION,
  nativePaseoProjectsSnapshotSchema,
  type NativePaseoProject,
  type NativePaseoProjectsInput,
  type NativePaseoProjectsSnapshot,
  type PlanningMutationInput,
  type PlanningMutationResult,
  type PlanningQueryInput,
  type PlanningSnapshot,
  type TaskDetailQueryInput,
  type TaskDetailSnapshot,
  type HomeQueryInput,
  type HomeSnapshot,
  type DoctorQueryInput,
  type DoctorReport,
  type OperationsQueryInput,
  type OperationsReport,
  type OperationsMutationInput,
  type OperationsMutationResult,
  type OrganizerBootstrapInput,
  type OrganizerBootstrapResult,
  type RepairInput,
  type RepairResult,
} from "../generated/planning-contract.shared.ts";
import type { ConnectorStartupStatus } from "../rpc/startup.shared.ts";
import { loadConnectorCredential } from "./credential.server.ts";
import { createPaseoSessionMCPInjection } from "./agent-mcp.server.ts";
import {
  createProjectAdminMCPInjection,
  registerProjectAdminSession,
  revokeProjectAdminSession,
} from "./project-admin-mcp.server.ts";
import { PROJECT_ADMIN_RECONNECT_INSTRUCTION } from "../rpc/project-admin.shared.ts";
import {
  PROJECT_ADMIN_MCP_CONTRACT_SHA256,
  PROJECT_ADMIN_MCP_CONTRACT_VERSION,
  PROJECT_ADMIN_MCP_TOOLS,
  type ProjectAdminMCPSessionLaunch,
} from "../generated/project-admin-mcp-contract.shared.ts";
import {
  createBoardTransport,
  type BoardTransport,
} from "./engine-board.server.ts";
import {
  createPlanningTransport,
  type PlanningTransport,
} from "./engine-planning.server.ts";
import {
  ensureBootstrapRuntime,
  type BootstrapRuntimeHandle,
  type DirectorHostIdentity,
  type ResolvedDolt,
  type ResolvedEngine,
} from "./bootstrap-launcher.server.ts";
import { selectInstalledBootstrap, type InstalledBootstrapSelection } from "./bootstrap-selection.server.ts";
import {
  assertHostCompatibility,
  type HostCompatibility,
} from "./compatibility.server.ts";
import { INSTALLED_CONNECTOR_METADATA } from "./install-metadata.server.ts";
import {
  DEFAULT_ENGINE_URL,
  directorRuntimePaths,
} from "./runtime-configuration.server.mjs";
import { startHostContractServer } from "./host-ipc.server.ts";

export type ConnectorEngineSelection = { readonly mode: "main" | "release" };

export type ConnectorClient = Partial<
  Pick<PaseoClient, "connect" | "close" | "projects" | "workspaces" | "agents" | "config">
>;

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
  hostCompatibility?: () => HostCompatibility;
  resolveEngine?: (selection: ConnectorEngineSelection) => Promise<ResolvedEngine>;
  ensureBootstrapRuntime?: typeof ensureBootstrapRuntime;
};

export class PaseoHostConnector implements DirectorHost {
  readonly #client: ConnectorClient;
  readonly #selection: ConnectorEngineSelection;
  readonly #compatibility: HostCompatibility;
  readonly #engine: Promise<ResolvedEngine>;
  readonly #boardTransport: BoardTransport;
  readonly #planningTransport: PlanningTransport;
  readonly #ready: Promise<void>;
  readonly #activation: ConnectorStartupStatus["activation"];
  readonly #runtimeRequired: boolean;
  readonly #engineURL: string;
  readonly #hostIdentity: Promise<DirectorHostIdentity>;
  #hostServer: Promise<() => Promise<void>> | null = null;
  #runtimeBootstrap: Promise<{
    bootstrap: BootstrapRuntimeHandle;
    dolt: ResolvedDolt;
  }> | null = null;
  #terminalUnsubscribe: (() => void) | null = null;
  #terminalCursor = 0;
  readonly #inflight = new Map<string, { digest: string; promise: Promise<HostObservation> }>();
  #cursor = 0;

  constructor(
    client: ConnectorClient,
    selection: ConnectorEngineSelection,
    boardTransport: BoardTransport,
    planningTransport: PlanningTransport,
    engine: Promise<ResolvedEngine> = Promise.resolve({
      mode: selection.mode,
      version: "0.0.0-dev",
      sourceCandidate: "0".repeat(40),
      target: "linux-amd64",
      binaryPath: "/not-exposed/director-engine",
      noticesPath: "/not-exposed/THIRD_PARTY_NOTICES.txt",
      binarySha256: "0".repeat(64),
      noticesSha256: "0".repeat(64),
      connectorCommit: "0".repeat(40),
      contractVersion: "test",
      contractSha256: "0".repeat(64),
    }),
    compatibility: HostCompatibility = {
      paseoVersion: "0.7.2",
      nodeVersion: "22.0.0",
      platform: "linux",
      architecture: "x64",
      target: "linux-amd64",
    },
    clientReady?: Promise<void>,
    activation: ConnectorStartupStatus["activation"] = {
      lifecycle: "plugin-reload",
      result: "running-current",
      configurationSchemaVersion: 2,
      configurationSha256: "0".repeat(64),
      legacyEnvironment: "absent",
      settings: [],
    },
    runtimeRequired = false,
    engineURL: string = DEFAULT_ENGINE_URL,
    hostIdentity: DirectorHostIdentity | Promise<DirectorHostIdentity> = {
      schemaVersion: 1,
      id: `director-${"0".repeat(32)}`,
      label: "Director",
    },
  ) {
    this.#client = client;
    this.#selection = selection;
    this.#boardTransport = boardTransport;
    this.#planningTransport = planningTransport;
    this.#engine = engine;
    this.#compatibility = compatibility;
    this.#activation = activation;
    this.#runtimeRequired = runtimeRequired;
    this.#engineURL = engineURL;
    this.#hostIdentity = Promise.resolve(hostIdentity);
    assertHostDescriptor(EXPECTED_HOST_DESCRIPTOR);
    this.#ready = Promise.all([
      clientReady ?? (client.connect ? client.connect() : Promise.resolve()),
      engine,
      this.#hostIdentity,
    ]).then(
      () => undefined,
      async (error: unknown) => {
        await client.close?.().catch(() => undefined);
        throw error;
      },
    );
  }

  async describe(): Promise<HostDescriptor> {
    await this.#ready;
    return EXPECTED_HOST_DESCRIPTOR;
  }

  attachHostServer(server: Promise<() => Promise<void>>): void {
    if (this.#hostServer !== null) {
      throw new Error("HOST_CONTRACT_SERVER_ALREADY_ATTACHED");
    }
    this.#hostServer = server;
  }

  attachBootstrapRuntime(runtime: Promise<{
    bootstrap: BootstrapRuntimeHandle;
    dolt: ResolvedDolt;
  }>): void {
    if (this.#runtimeBootstrap !== null) {
      throw new Error("RUNTIME_BOOTSTRAP_ALREADY_ATTACHED");
    }
    this.#runtimeBootstrap = runtime;
  }

  attachTerminalEvents(baseUrl: string): void {
    const endpoint = new URL("/v1/host/terminal-event", baseUrl);
    const hostname = endpoint.hostname.startsWith("[") ? endpoint.hostname.slice(1, -1) : endpoint.hostname;
    if (endpoint.protocol !== "http:" || endpoint.port === "" ||
      !(hostname === "::1" || hostname.startsWith("127."))) {
      throw new Error("DIRECTOR_ENGINE_URL must be an exact loopback origin for terminal callbacks");
    }
    // Startup methods retain and expose the authoritative rejection. The
    // background attachment consumes only that source rejection; failures in
    // its fulfillment callback remain observable instead of being suppressed.
    void this.#ready.then(async () => {
      if (!this.#client.agents || this.#terminalUnsubscribe) return;
      this.#terminalUnsubscribe = this.#client.agents.subscribe((update: PaseoAgentUpdate) => {
        if (update.kind !== "upsert") return;
        const agent = update.agent;
        const runId = agent.labels?.[WORKER_LABEL.run];
        if (!runId || !agent.labels?.[WORKER_LABEL.role]) return;
        let kind: "agent.finished" | "agent.error" | "agent.permission" | null = null;
        if (agent.pendingPermissions.length > 0 || agent.attentionReason === "permission") kind = "agent.permission";
        else if (agent.status === "error" || agent.attentionReason === "error") kind = "agent.error";
        else if (agent.status === "idle" && !agent.activeTurn && agent.lastUserMessageAt) kind = "agent.finished";
        if (!kind) return;
        const cursor = Number.isSafeInteger(update.seq) && (update.seq ?? 0) > 0
          ? update.seq!
          : ++this.#terminalCursor;
        this.#terminalCursor = Math.max(this.#terminalCursor, cursor);
        const observedAtMillis = Date.now();
        const eventId = `terminal-${sha256(JSON.stringify({ agentId: agent.id, runId, kind, generation: update.generation ?? "", cursor })).slice(0, 32)}`;
        void globalThis.fetch(endpoint, {
          method: "POST",
          redirect: "error",
          headers: {
            "content-type": "application/json",
            "x-director-contract-version": EXPECTED_HOST_DESCRIPTOR.contractVersion,
            "x-director-contract-hash": EXPECTED_HOST_DESCRIPTOR.contractHash,
          },
          body: JSON.stringify({ schemaVersion: 1, eventId, kind, runId, agentId: agent.id, cursor, observedAtMillis }),
          signal: AbortSignal.timeout(900),
        }).catch(() => undefined);
      });
      await this.#client.agents.list({ page: { limit: 100 }, subscribe: {} });
    }, () => undefined);
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

  async #projects(): Promise<PaseoProject[]> {
    if (!this.#client.projects) {
      throw new PaseoHostEffectError("HOST_PUBLIC_PROJECTS_UNAVAILABLE");
    }
    const result = await this.#client.projects.list({
      requestId: `director-native-projects-${process.pid}`,
    });
    if (result.projects.length > 100) {
      throw new PaseoHostEffectError("HOST_PROJECT_DIRECTORY_LIMIT_EXCEEDED");
    }
    return [...result.projects];
  }

  async queryNativePaseoProjects(
    input: NativePaseoProjectsInput,
  ): Promise<NativePaseoProjectsSnapshot> {
    await this.#ready;
    const hostId = (await this.#hostIdentity).id;
    const [projects, listedWorkspaces] = await Promise.all([
      this.#projects(),
      this.#workspaces(),
    ]);
    // Paseo 0.7.2's directory page intentionally omits current gitRuntime
    // details. Re-observe each bounded active Workspace through the public
    // handle before generating a selector/Preview; a stale list row which has
    // disappeared is excluded rather than treated as a current Git fact.
    const allWorkspaces: PaseoWorkspace[] = [];
    for (const workspace of listedWorkspaces) {
      if (workspace.archivingAt) continue;
      const current = await this.#roots().workspaces.ref(workspace.id).refresh({
        requestId: `director-native-refresh-${sha256(`${hostId}\u0000${workspace.id}`).slice(0, 32)}`,
      });
      if (current && !current.archivingAt) allWorkspaces.push(current);
    }
    const values: NativePaseoProject[] = projects.map((project) => {
      const root = project.projectRootPath;
      if (!isAbsolute(root) || resolve(root) !== root) {
        throw new PaseoHostEffectError("HOST_NATIVE_PROJECT_PATH_INVALID");
      }
      const workspaces = allWorkspaces
        .filter(
          (workspace) =>
            workspace.projectId === project.projectId && !workspace.archivingAt,
        )
        .map((workspace) => ({
          id: workspace.id,
          name: workspace.title || workspace.name,
          projectRootPath: workspace.projectRootPath,
          workspaceDirectory:
            workspace.workspaceDirectory || workspace.projectRootPath,
          workspaceKind: workspace.workspaceKind,
          remoteUrl: workspace.gitRuntime?.remoteUrl || null,
          baseBranch: workspace.gitRuntime?.currentBranch || null,
        }))
        .sort((left, right) => left.id.localeCompare(right.id));
      const value = {
        projectId: project.projectId,
        name: project.projectCustomName || project.projectDisplayName,
        projectRootPath: root,
        projectKind: project.projectKind,
        organizerCandidate: join(
          dirname(root),
          `.${basename(root)}-director-organizer`,
        ),
        workspaces,
        factsRevision: "",
      } satisfies NativePaseoProject;
      return { ...value, factsRevision: sha256(JSON.stringify(value)) };
    });
    values.sort((left, right) => left.projectId.localeCompare(right.projectId));
    return nativePaseoProjectsSnapshotSchema.parse({
      schemaVersion: 1,
      contractVersion: PLANNING_CONTRACT_VERSION,
      contractHash: PLANNING_CONTRACT_SHA256,
      hostId,
      observedAt: new Date().toISOString(),
      projects: values,
    });
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
    let workspace = await this.#roots().workspaces.ref(matches[0]!.id).refresh({
      requestId: `${command.requestId}-workspace-fact-0`,
    });
    for (let attempt = 1; workspace && attempt <= 20; attempt++) {
      const coreExact = workspace.id === (argumentsValue.workspaceId || workspace.id) &&
        workspace.title === expectedTitle && workspace.workspaceDirectory === argumentsValue.worktreePath &&
        !workspace.archivingAt;
      if (!coreExact || (workspace.workspaceKind === "worktree" && workspace.gitRuntime?.isPaseoOwnedWorktree === false)) break;
      await new Promise((accept) => setTimeout(accept, 100));
      workspace = await this.#roots().workspaces.ref(matches[0]!.id).refresh({
        requestId: `${command.requestId}-workspace-fact-${attempt}`,
      });
    }
    if (!workspace) {
      return this.#observation(
        command,
        command.arguments.effectKind === "host_view.archive" ? "desired" : "absent",
        command.arguments.effectKind === "host_view.archive" ? (argumentsValue.workspaceId ?? "") : "",
      );
    }
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
    const hasHostPrerequisiteFailure = /\b(?:could not find \S+ on path|missing (?:host )?(?:dependency|prerequisite)|install \S+ with your (?:os )?package manager|(?:host|sandbox) prerequisites?)\b/u.test(value);
    if (hasHostPrerequisiteFailure || /\b(?:configuration|invalid option|unsupported model|model not found|provider not configured)\b/u.test(value)) {
      signals.add("configuration_rejection");
    }
    if (/\b(?:policy|permission denied|not allowed|approval required|sandbox (?:policy|violation|restriction|denied|disallowed|refused)|tool policy|tool denied)\b/u.test(value) ||
        (!hasHostPrerequisiteFailure && /\bsandbox\b/u.test(value))) {
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
      if (agent.status === "idle" && agent.attentionReason === "finished") {
        return this.#observation(command, "desired", agent.id, correlation, true, providerUsage(agent));
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
    if (agent.status === "idle" && agent.attentionReason === "finished") {
      return this.#observation(command, "desired", agent.id, correlation, true, providerUsage(agent));
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
            args: [...server.args, "--role", session.role, "--session-sha", session.sessionSha256],
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
        await this.#roots().workspaces.create({
          requestId: command.idempotencyKey,
          title: `${value.title} execution workspace`,
          source: { kind: "directory", path: value.worktreePath },
        });
        return this.#workspace(command);
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
        const agent = this.#roots().agents.ref(value.agentId);
        const refreshed = await agent.refresh(`${command.requestId}-prompt-boundary`);
        if (!refreshed?.agent || refreshed.agent.status !== "idle" ||
          (refreshed.agent.activeTurn && refreshed.agent.attentionReason !== "finished")) {
          throw new PaseoHostEffectError("HOST_PRIMARY_PROMPT_BOUNDARY_INVALID");
        }
        // Paseo/Codex sessions close their stdio MCP child when one turn ends.
        // Detaching the idle provider session before the next engine-owned
        // prompt preserves the native agent identity while reapplying its
        // frozen MCP configuration to the new turn.
        await agent.detach();
        await agent.send(value.initialPrompt, {
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

  async status(): Promise<ConnectorStartupStatus> {
    await this.#ready;
    const engine = await this.#engine;
    if (this.#runtimeRequired && this.#runtimeBootstrap === null) {
      throw new PaseoHostEffectError("DIRECTOR_RUNTIME_NOT_ATTACHED");
    }
    const runtime = await this.#runtimeBootstrap ?? {
      bootstrap: {
        binding: "0".repeat(64),
        host: await this.#hostIdentity,
        engine,
        dolt: {
          version: "2.3.2" as const,
          target: "linux-amd64" as const,
          binaryPath: "/not-exposed/dolt",
          binarySha256: "0".repeat(64),
          archiveSha256: "0".repeat(64),
        },
        projectAdminAuthorization() { return "0".repeat(64); },
        async status() {
          return { state: "current" as const, binding: "0".repeat(64), enginePid: 1, doltPid: 2, restartCount: 0 };
        },
        async close() {},
      },
      dolt: {
        version: "2.3.2" as const,
        target: "linux-amd64" as const,
        binaryPath: "/not-exposed/dolt",
        binarySha256: "0".repeat(64),
        archiveSha256: "0".repeat(64),
      },
    };
    const supervisor = await runtime.bootstrap.status();
    if (supervisor.state !== "current") {
      throw new PaseoHostEffectError("DIRECTOR_RUNTIME_DEGRADED");
    }
    if (supervisor.binding !== runtime.bootstrap.binding) {
      throw new PaseoHostEffectError("DIRECTOR_RUNTIME_BINDING_MISMATCH");
    }
    if (
      this.#runtimeRequired &&
      (!Number.isSafeInteger(supervisor.enginePid) || supervisor.enginePid! <= 0 ||
        !Number.isSafeInteger(supervisor.doltPid) || supervisor.doltPid! <= 0 ||
        supervisor.enginePid === supervisor.doltPid)
    ) {
      throw new PaseoHostEffectError("DIRECTOR_RUNTIME_CHILDREN_NOT_READY");
    }
    return {
      state: "board-ready",
      engineMode: this.#selection.mode,
      productBehavior: true,
      activation: this.#activation,
      compatibility: this.#compatibility,
      host: await this.#hostIdentity,
      engine: {
        mode: engine.mode,
        version: engine.version,
        sourceCandidate: engine.sourceCandidate,
        target: engine.target,
        binarySha256: engine.binarySha256,
        noticesSha256: engine.noticesSha256,
        connectorCommit: engine.connectorCommit,
        contractVersion: engine.contractVersion,
        contractSha256: engine.contractSha256,
      },
      runtime: {
        supervisorBinding: runtime.bootstrap.binding,
        supervisorState: "current",
        doltVersion: runtime.dolt.version,
        doltTarget: runtime.dolt.target,
        doltBinarySha256: runtime.dolt.binarySha256,
        doltArchiveSha256: runtime.dolt.archiveSha256,
      },
      descriptor: {
        ...EXPECTED_HOST_DESCRIPTOR,
        capabilities: [...HOST_CAPABILITIES],
      },
    };
  }

  async loadBoard() {
    await this.#ready;
    return this.#boardTransport.load();
  }

  async queryPlanning(input: PlanningQueryInput): Promise<PlanningSnapshot> {
    await this.#ready;
    return this.#planningTransport.query(input);
  }

  async queryHome(input: HomeQueryInput): Promise<HomeSnapshot> {
    await this.#ready;
    if (!this.#planningTransport.home) {
      throw new Error("HOME_SURFACE_NOT_WIRED");
    }
    return this.#planningTransport.home({ ...input, hostId: (await this.#hostIdentity).id });
  }

  async queryDoctor(input: DoctorQueryInput): Promise<DoctorReport> {
    await this.#ready;
    if (!this.#planningTransport.doctor) {
      throw new Error("DOCTOR_SURFACE_NOT_WIRED");
    }
    return this.#planningTransport.doctor({ ...input, hostId: (await this.#hostIdentity).id });
  }

  async queryOperations(input: OperationsQueryInput): Promise<OperationsReport> {
    await this.#ready;
    if (!this.#planningTransport.operations) {
      throw new Error("OPERATIONS_SURFACE_NOT_WIRED");
    }
    return this.#planningTransport.operations({ ...input, hostId: (await this.#hostIdentity).id });
  }

  async mutateOperations(input: OperationsMutationInput): Promise<OperationsMutationResult> {
    await this.#ready;
    if (!this.#planningTransport.mutateOperations) {
      throw new Error("OPERATIONS_MUTATION_NOT_WIRED");
    }
    return this.#planningTransport.mutateOperations({ ...input, hostId: (await this.#hostIdentity).id });
  }

  async repairProject(input: RepairInput): Promise<RepairResult> {
    await this.#ready;
    if (!this.#planningTransport.repair) {
      throw new Error("REPAIR_SURFACE_NOT_WIRED");
    }
    return this.#planningTransport.repair({ ...input, hostId: (await this.#hostIdentity).id });
  }

  async bootstrapOrganizer(input: OrganizerBootstrapInput): Promise<OrganizerBootstrapResult> {
    await this.#ready;
    if (!this.#planningTransport.bootstrapOrganizer) {
      throw new Error("ORGANIZER_BOOTSTRAP_NOT_WIRED");
    }
    if (input.kind === "native.create.preview" || input.kind === "native.create.apply") {
      if (!input.nativeProject) {
        throw new Error("NATIVE_PROJECT_FACTS_REQUIRED");
      }
      const hostId = (await this.#hostIdentity).id;
      const current = await this.queryNativePaseoProjects({ hostId });
      const project = current.projects.find(
        (value) => value.projectId === input.nativeProject?.projectId,
      );
      if (!project || project.factsRevision !== input.nativeProject.factsRevision) {
        throw new Error("NATIVE_PROJECT_FACTS_CHANGED");
      }
      return this.#planningTransport.bootstrapOrganizer({
        ...input,
        hostId,
        nativeProject: project,
      });
    }
    return this.#planningTransport.bootstrapOrganizer({ ...input, hostId: (await this.#hostIdentity).id });
  }

  async queryPlanningTask(
    input: TaskDetailQueryInput,
  ): Promise<TaskDetailSnapshot> {
    await this.#ready;
    return this.#planningTransport.taskDetail({ ...input, hostId: (await this.#hostIdentity).id });
  }

  async recreateProjectAdminSession(input: {
    readonly requestId: string;
    readonly sourceWorkspaceId: string;
    readonly sourceAgentId: string;
  }): Promise<{ status: "created"; agentId: string; instruction: typeof PROJECT_ADMIN_RECONNECT_INSTRUCTION }> {
    await this.#ready;
    if (!IDENTITY_PATTERN.test(input.requestId) || !IDENTITY_PATTERN.test(input.sourceWorkspaceId) ||
      !IDENTITY_PATTERN.test(input.sourceAgentId) || !this.#runtimeBootstrap) {
      throw new PaseoHostEffectError("PROJECT_ADMIN_SESSION_INPUT_INVALID");
    }
    const roots = this.#roots();
    const [source, workspace, engine, runtime] = await Promise.all([
      roots.agents.ref(input.sourceAgentId).refresh(`${input.requestId}-source-agent`),
      roots.workspaces.ref(input.sourceWorkspaceId).refresh({ requestId: `${input.requestId}-source-workspace` }),
      this.#engine,
      this.#runtimeBootstrap,
    ]);
    if (!source?.agent || !workspace || source.agent.id !== input.sourceAgentId ||
      source.agent.workspaceId !== workspace.id || workspace.id !== input.sourceWorkspaceId ||
      source.agent.status === "closed" || source.agent.archivedAt || !source.agent.model ||
      source.agent.capabilities?.supportsMcpServers !== true ||
      !/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/u.test(source.agent.provider) ||
      !/^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$/u.test(source.agent.model)) {
      throw new PaseoHostEffectError("PROJECT_ADMIN_NATIVE_IDENTITY_REFUSED");
    }
    const authorization = runtime.bootstrap.projectAdminAuthorization();
    if (!HASH_PATTERN.test(authorization)) {
      throw new PaseoHostEffectError("PROJECT_ADMIN_AUTHORIZATION_UNAVAILABLE");
    }
    const sessionDigest = sha256(`${authorization}\x1f${input.requestId}\x1f${workspace.id}\x1f${source.agent.id}`);
    const sessionId = `admin-session-${sessionDigest.slice(0, 32)}`;
    const audience = `paseo-session-${sessionDigest.slice(32)}`;
    const token = sha256(`${authorization}\x1fproject-admin-token\x1f${sessionDigest}`);
    const launch: ProjectAdminMCPSessionLaunch = {
      contractVersion: PROJECT_ADMIN_MCP_CONTRACT_VERSION,
      contractHash: PROJECT_ADMIN_MCP_CONTRACT_SHA256,
      sessionSha256: sha256(`${sessionId}\x1f${audience}`),
      sessionId,
      audience,
      tools: PROJECT_ADMIN_MCP_TOOLS.map((tool) => tool.name),
      server: {
        name: "director-project-admin",
        command: engine.binaryPath,
        args: ["project-admin-mcp", "--engine-url", this.#engineURL, "--session", sessionId, "--audience", audience],
        env: { DIRECTOR_PROJECT_ADMIN_TOKEN: token },
      },
    };
    const injection = createProjectAdminMCPInjection(launch);
    const created = await roots.workspaces.ref(workspace).agents.create({
      config: {
        provider: `${source.agent.provider}/${source.agent.model}`,
        ...(source.agent.currentModeId ? { modeId: source.agent.currentModeId } : {}),
        ...(source.agent.thinkingOptionId ? { thinkingOptionId: source.agent.thinkingOptionId } : {}),
        featureValues: { permissionMode: "read-only" },
        mcpServers: injection.mcpServers,
        toolPolicy: injection.toolPolicy,
      },
      title: `${source.agent.title ?? "Agent"} — Director administration`,
      labels: { "director.project-admin-session": sessionId },
      requestId: input.requestId,
      autoArchive: false,
    });
    try {
      await registerProjectAdminSession({
        baseUrl: this.#engineURL,
        authorization,
        registration: {
          requestId: input.requestId,
          sessionId,
          nativeWorkspaceId: workspace.id,
          nativeAgentId: created.id,
          audience,
          tokenSha256: sha256(token),
        },
      });
      await created.send(
        "Director Project administration MCP is connected to this immutable Project scope. Ask the user to re-send the request they want performed in this session. Never suggest restarting Paseo, forging headers, or using direct storage.",
        { messageId: `${input.requestId}-connected` },
      );
    } catch (error) {
      await revokeProjectAdminSession({ baseUrl: this.#engineURL, authorization,
        requestId: `${input.requestId}-revoke`, sessionId }).catch(() => undefined);
      await created.archive().catch(() => undefined);
      throw error;
    }
    return { status: "created", agentId: created.id, instruction: PROJECT_ADMIN_RECONNECT_INSTRUCTION };
  }

  async mutatePlanning(
	input: PlanningMutationInput,
  ): Promise<PlanningMutationResult> {
	await this.#ready;
	return this.#planningTransport.mutate(input);
  }

  async close(): Promise<void> {
	this.#terminalUnsubscribe?.();
	this.#terminalUnsubscribe = null;
	if (this.#hostServer) {
		const closeHostServer = await this.#hostServer;
		await closeHostServer();
	}
    await this.#runtimeBootstrap?.then((runtime) => runtime.bootstrap.close()).catch(() => undefined);
    await this.#client.close?.();
  }
}

export function startConnectorShell(options: {
  environment: NodeJS.ProcessEnv;
  checkoutRoot: string;
  dependencies?: ConnectorDependencies;
}): PaseoHostConnector {
  const compatibility = (options.dependencies?.hostCompatibility ?? assertHostCompatibility)();
  const selection: ConnectorEngineSelection = { mode: "release" };
  const credential = loadConnectorCredential({
    credentialPath: options.environment.DIRECTOR_PASEO_CREDENTIAL_FILE,
    checkoutRoot: options.checkoutRoot,
    disjointEnginePaths: [],
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
  const engine = options.dependencies?.resolveEngine?.(selection) ?? Promise.reject(new PaseoHostEffectError("DIRECTOR_EXTERNAL_ENGINE_IDENTITY_REQUIRED"));
  const connector = new PaseoHostConnector(
    client,
    selection,
    boardTransport,
    planningTransport,
    engine,
    compatibility,
    undefined,
    undefined,
    false,
    options.environment.DIRECTOR_ENGINE_URL,
  );
  if (options.environment.DIRECTOR_HOST_SOCKET) {
    connector.attachHostServer(startHostContractServer(connector, options.environment.DIRECTOR_HOST_SOCKET));
  }
  if (options.environment.DIRECTOR_ENGINE_URL) {
    connector.attachTerminalEvents(options.environment.DIRECTOR_ENGINE_URL);
  }
  return connector;
}

export function startInstalledConnectorShell(options: {
  paseo: PaseoApi;
  environment?: NodeJS.ProcessEnv;
  dependencies?: ConnectorDependencies;
  installation?: typeof INSTALLED_CONNECTOR_METADATA | Readonly<Record<string, unknown>>;
  reportActivation?: boolean;
}): PaseoHostConnector {
  const environment = options.environment ?? process.env;
  const runtime = directorRuntimePaths(environment);
  const selection: InstalledBootstrapSelection = selectInstalledBootstrap(
    options.installation ?? INSTALLED_CONNECTOR_METADATA,
  );
  const connectorSelection: ConnectorEngineSelection = { mode: selection.channel };
  const configurationBytes = Buffer.from(`director-go-bootstrap/v1\nengine.mode=${selection.channel}\n`);
  const configuration = {
    schemaVersion: 2 as const,
    engine: {
      mode: selection.channel,
      url: DEFAULT_ENGINE_URL,
      hostSocket: runtime.hostSocket,
      runtimeRoot: runtime.workRoot,
    },
    diagnostics: {
      schemaVersion: 2 as const,
      sha256: createHash("sha256").update(configurationBytes).digest("hex"),
      legacyEnvironment: "absent",
      settings: [
        { name: "engine.mode", source: "defaulted" },
        { name: "engine.url", source: "defaulted" },
        { name: "engine.cache-base", source: environment.XDG_CACHE_HOME === undefined ? "defaulted" : "overridden" },
        { name: "engine.runtime-base", source: environment.XDG_RUNTIME_DIR === undefined && environment.XDG_CACHE_HOME === undefined ? "defaulted" : "overridden" },
      ],
    },
  } as const;
  const compatibility = (options.dependencies?.hostCompatibility ?? assertHostCompatibility)();
  const boardTransport = options.dependencies?.boardTransport ??
    createBoardTransport({ baseUrl: configuration.engine.url });
  const planningTransport = options.dependencies?.planningTransport ??
    createPlanningTransport({
      baseUrl: configuration.engine.url,
      mutationActor: {
        kind: "human",
        id: "local-project-owner",
        sessionId: `paseo-connector-${process.pid}`,
      },
    });
  const client = options.paseo as ConnectorClient;
  const runtimeBootstrap = Promise.resolve().then(async () => {
    const bootstrap = await (options.dependencies?.ensureBootstrapRuntime ?? ensureBootstrapRuntime)({
      selection,
      environment,
      hostSocket: configuration.engine.hostSocket,
    });
    return { engine: bootstrap.engine, dolt: bootstrap.dolt, bootstrap };
  });
  const engine = runtimeBootstrap.then((runtime) => runtime.engine);
  const hostIdentity = runtimeBootstrap.then((runtime) => runtime.bootstrap.host);
  const deploymentReady = runtimeBootstrap.then(() => undefined);
  const activation: ConnectorStartupStatus["activation"] = {
    lifecycle: "plugin-reload",
    result: "running-current",
    configurationSchemaVersion: configuration.schemaVersion,
    configurationSha256: configuration.diagnostics.sha256,
    legacyEnvironment: configuration.diagnostics.legacyEnvironment,
    settings: [...configuration.diagnostics.settings],
  };
  const connector = new PaseoHostConnector(
    client,
    connectorSelection,
    boardTransport,
    planningTransport,
    engine,
    compatibility,
    deploymentReady,
    activation,
    true,
    configuration.engine.url,
    hostIdentity,
  );
  connector.attachBootstrapRuntime(runtimeBootstrap);
  connector.attachHostServer(startHostContractServer(connector, configuration.engine.hostSocket));
  connector.attachTerminalEvents(configuration.engine.url);
  if (options.reportActivation !== false) void connector.status().then(
    (status) => {
      console.log(JSON.stringify({
        code: "DIRECTOR_ACTIVATION_READY",
        lifecycle: status.activation.lifecycle,
        result: status.activation.result,
        configurationSchemaVersion: status.activation.configurationSchemaVersion,
        configurationSha256: status.activation.configurationSha256,
        legacyEnvironment: status.activation.legacyEnvironment,
        settings: status.activation.settings,
        connectorCommit: status.engine.connectorCommit,
        engineSha256: status.engine.binarySha256,
        doltVersion: status.runtime.doltVersion,
        doltSha256: status.runtime.doltBinarySha256,
        supervisorBinding: status.runtime.supervisorBinding,
      }));
    },
    (error: unknown) => {
      const candidate = error && typeof error === "object"
        ? Reflect.get(error, "code")
        : undefined;
      const code = typeof candidate === "string" && /^[A-Z0-9_]{3,96}$/u.test(candidate)
        ? candidate
        : "DIRECTOR_ACTIVATION_FAILED";
      console.error(JSON.stringify({ code, lifecycle: "plugin-reload", result: "failed" }));
    },
  );
  return connector;
}
