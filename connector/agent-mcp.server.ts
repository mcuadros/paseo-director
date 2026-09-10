// SPDX-License-Identifier: Apache-2.0
// Policy-free Paseo v0.7 translation for engine-authorized session MCP.

import {
  AGENT_MCP_CAPABILITIES,
  AGENT_MCP_CONTRACT_SHA256,
  AGENT_MCP_CONTRACT_VERSION,
  AGENT_MCP_PROVIDER_CLI_VERSIONS,
  AGENT_MCP_ROLES,
  AGENT_MCP_TOOLS,
  type AgentMCPCapability,
  type AgentMCPProvider,
  type AgentMCPProviderObservation,
  type AgentMCPSessionLaunch,
  type AgentMCPToolName,
} from "../generated/agent-mcp-contract.shared.ts";

const sha256Pattern = /^[0-9a-f]{64}$/;
const tokenPattern = /^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$/;

export class AgentMCPConnectorError extends Error {
  readonly code: string;

  constructor(code: string) {
    super(code);
    this.name = "AgentMCPConnectorError";
    this.code = code;
  }
}

function exactKeys(
  value: Record<string, unknown>,
  expected: readonly string[],
): boolean {
  const actual = Object.keys(value).sort();
  const wanted = [...expected].sort();
  return (
    actual.length === wanted.length &&
    actual.every((key, index) => key === wanted[index])
  );
}

function record(
  value: unknown,
  code = "PASEO_PROVIDER_FACT_INVALID",
): Record<string, unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new AgentMCPConnectorError(code);
  }
  return value as Record<string, unknown>;
}

function boundedToken(value: unknown): value is string {
  return typeof value === "string" && tokenPattern.test(value);
}

function uniqueKnown<T extends string>(
  value: unknown,
  allowed: readonly T[],
  maximum: number,
): value is readonly T[] {
  return (
    Array.isArray(value) &&
    value.length > 0 &&
    value.length <= maximum &&
    value.every((entry) => typeof entry === "string" && allowed.includes(entry as T)) &&
    new Set(value).size === value.length
  );
}

export type PublicPaseoSessionFacts = Readonly<{
  source: "public-paseo-v0.7";
  discoveryRevision: string;
  paseoVersion: string;
  provider: AgentMCPProvider;
  cliVersion: string;
  state: "ready" | "unavailable";
  model: string;
  effort: string;
  mode: string;
  permissionMode: string;
  providerOptions: readonly Readonly<{ name: string; value: string }>[];
  mcpCapabilities: readonly AgentMCPCapability[];
  liveSessionSupportsMcpServers: boolean;
  exactToolPreapprovalObserved: boolean;
  runtimeProbePassed: boolean;
}>;

// translatePublicPaseoSessionFacts preserves only normalized, bounded public
// v0.7 facts. It makes no admission, fallback, or authorization decision.
export function translatePublicPaseoSessionFacts(
  raw: unknown,
): AgentMCPProviderObservation {
  const value = record(raw);
  if (
    !exactKeys(value, [
      "source",
      "discoveryRevision",
      "paseoVersion",
      "provider",
      "cliVersion",
      "state",
      "model",
      "effort",
      "mode",
      "permissionMode",
      "providerOptions",
      "mcpCapabilities",
      "liveSessionSupportsMcpServers",
      "exactToolPreapprovalObserved",
      "runtimeProbePassed",
    ]) ||
    value.source !== "public-paseo-v0.7" ||
    typeof value.discoveryRevision !== "string" ||
    !sha256Pattern.test(value.discoveryRevision) ||
    !boundedToken(value.paseoVersion) ||
    !boundedToken(value.provider) ||
    !(value.provider in AGENT_MCP_PROVIDER_CLI_VERSIONS) ||
    !boundedToken(value.cliVersion) ||
    (value.state !== "ready" && value.state !== "unavailable") ||
    !boundedToken(value.model) ||
    !boundedToken(value.effort) ||
    !boundedToken(value.mode) ||
    !boundedToken(value.permissionMode) ||
    typeof value.liveSessionSupportsMcpServers !== "boolean" ||
    typeof value.exactToolPreapprovalObserved !== "boolean" ||
    typeof value.runtimeProbePassed !== "boolean" ||
    !uniqueKnown(value.mcpCapabilities, AGENT_MCP_CAPABILITIES, 16) ||
    !Array.isArray(value.providerOptions) ||
    value.providerOptions.length > 16
  ) {
    throw new AgentMCPConnectorError("PASEO_PROVIDER_FACT_INVALID");
  }
  const providerOptions = value.providerOptions.map((rawOption) => {
    const option = record(rawOption);
    if (
      !exactKeys(option, ["name", "value"]) ||
      !boundedToken(option.name) ||
      !boundedToken(option.value)
    ) {
      throw new AgentMCPConnectorError("PASEO_PROVIDER_FACT_INVALID");
    }
    return { name: option.name, value: option.value } as const;
  });
  if (new Set(providerOptions.map((option) => option.name)).size !== providerOptions.length) {
    throw new AgentMCPConnectorError("PASEO_PROVIDER_FACT_INVALID");
  }
  return {
    schemaVersion: 1,
    contractVersion: AGENT_MCP_CONTRACT_VERSION,
    contractHash: AGENT_MCP_CONTRACT_SHA256,
    discoveryRevision: value.discoveryRevision,
    paseoVersion: value.paseoVersion,
    provider: value.provider as AgentMCPProvider,
    cliVersion: value.cliVersion,
    state: value.state,
    model: value.model,
    effort: value.effort,
    mode: value.mode,
    permissionMode: value.permissionMode,
    providerOptions,
    mcpCapabilities: value.mcpCapabilities as readonly AgentMCPCapability[],
    sessionStdioMcp: value.liveSessionSupportsMcpServers,
    exactMcpToolPolicy: value.exactToolPreapprovalObserved,
    runtimeProbePassed: value.runtimeProbePassed,
  };
}

export type PaseoSessionMCPInjection = Readonly<{
  mcpServers: Readonly<
    Record<
      string,
      Readonly<{
        type: "stdio";
        command: string;
        args: readonly string[];
        env: Readonly<Record<string, string>>;
      }>
    >
  >;
  toolPolicy: Readonly<{
    preapproved: readonly Readonly<{
      kind: "mcp";
      server: string;
      tool: AgentMCPToolName;
    }>[];
  }>;
}>;

function safeProcessValue(value: unknown, maximum: number): value is string {
  return (
    typeof value === "string" &&
    value.length > 0 &&
    value.length <= maximum &&
    !/[\0\r\n]/.test(value)
  );
}

// createPaseoSessionMCPInjection translates one engine-authorized launch into
// the documented Paseo v0.7 per-session mcpServers and exact preapproval form.
// It never chooses a provider, fallback, role, tool, command, or credential.
export function createPaseoSessionMCPInjection(
  launch: AgentMCPSessionLaunch,
): PaseoSessionMCPInjection {
  const value = record(launch, "PASEO_SESSION_MCP_DESCRIPTOR_INVALID");
  if (
    !exactKeys(value, [
      "contractVersion",
      "contractHash",
      "sessionSha256",
      "role",
      "provider",
      "model",
      "tools",
      "server",
    ]) ||
    launch.contractVersion !== AGENT_MCP_CONTRACT_VERSION ||
    launch.contractHash !== AGENT_MCP_CONTRACT_SHA256 ||
    !sha256Pattern.test(launch.sessionSha256) ||
    !AGENT_MCP_ROLES.includes(launch.role) ||
    !boundedToken(launch.provider) ||
    !boundedToken(launch.model) ||
    !uniqueKnown(
      launch.tools,
      AGENT_MCP_TOOLS.map((tool) => tool.name),
      AGENT_MCP_TOOLS.length,
    )
  ) {
    throw new AgentMCPConnectorError("PASEO_SESSION_MCP_DESCRIPTOR_INVALID");
  }
  const server = record(
    launch.server,
    "PASEO_SESSION_MCP_DESCRIPTOR_INVALID",
  );
  if (
    !exactKeys(server, ["name", "command", "args", "env"]) ||
    !boundedToken(launch.server.name) ||
    !safeProcessValue(launch.server.command, 4096) ||
    !Array.isArray(launch.server.args) ||
    launch.server.args.length > 64 ||
    !launch.server.args.every((argument) => safeProcessValue(argument, 4096))
  ) {
    throw new AgentMCPConnectorError("PASEO_SESSION_MCP_DESCRIPTOR_INVALID");
  }
  const environment = record(
    launch.server.env,
    "PASEO_SESSION_MCP_DESCRIPTOR_INVALID",
  );
  if (
    Object.keys(environment).length > 32 ||
    Object.entries(environment).some(
      ([name, entry]) =>
        !/^[A-Z][A-Z0-9_]{0,127}$/.test(name) ||
        typeof entry !== "string" ||
        entry.length > 4096 ||
        /[\0\r\n]/.test(entry),
    )
  ) {
    throw new AgentMCPConnectorError("PASEO_SESSION_MCP_DESCRIPTOR_INVALID");
  }
  const stdio = {
    type: "stdio" as const,
    command: launch.server.command,
    args: [...launch.server.args],
    env: { ...launch.server.env },
  };
  return {
    mcpServers: { [launch.server.name]: stdio },
    toolPolicy: {
      preapproved: launch.tools.map((tool) => ({
        kind: "mcp" as const,
        server: launch.server.name,
        tool,
      })),
    },
  };
}
