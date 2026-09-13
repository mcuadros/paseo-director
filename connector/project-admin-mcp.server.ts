// SPDX-License-Identifier: Apache-2.0

import { isIP } from "node:net";
import { isAbsolute } from "node:path";

import {
  PROJECT_ADMIN_MCP_CONTRACT_SHA256,
  PROJECT_ADMIN_MCP_CONTRACT_VERSION,
  PROJECT_ADMIN_MCP_MAXIMUM_RESPONSE_BYTES,
  PROJECT_ADMIN_MCP_TOOLS,
  type ProjectAdminMCPSessionLaunch,
  type ProjectAdminMCPToolName,
} from "../generated/project-admin-mcp-contract.shared.ts";
const IDENTITY_PATTERN = /^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,127}$/u;
const SHA256_PATTERN = /^[0-9a-f]{64}$/u;

function exactKeys(value: Record<string, unknown>, expected: readonly string[]): boolean {
  const actual = Object.keys(value).sort();
  const wanted = [...expected].sort();
  return actual.length === wanted.length && actual.every((key, index) => key === wanted[index]);
}

export class ProjectAdminMCPConnectorError extends Error {
  readonly code: string;

  constructor(code: string) {
    super(code);
    this.name = "ProjectAdminMCPConnectorError";
    this.code = code;
  }
}

function exactTools(tools: readonly ProjectAdminMCPToolName[]): boolean {
  const expected = PROJECT_ADMIN_MCP_TOOLS.map((tool) => tool.name);
  return JSON.stringify(tools) === JSON.stringify(expected);
}

export function createProjectAdminMCPInjection(launch: ProjectAdminMCPSessionLaunch) {
  if (
    !exactKeys(launch as unknown as Record<string, unknown>, [
      "contractVersion", "contractHash", "sessionSha256", "sessionId", "audience", "tools", "server",
    ]) ||
    !exactKeys(launch.server as unknown as Record<string, unknown>, ["name", "command", "args", "env"]) ||
    launch.contractVersion !== PROJECT_ADMIN_MCP_CONTRACT_VERSION ||
    launch.contractHash !== PROJECT_ADMIN_MCP_CONTRACT_SHA256 ||
    !SHA256_PATTERN.test(launch.sessionSha256) ||
    !IDENTITY_PATTERN.test(launch.sessionId) ||
    !IDENTITY_PATTERN.test(launch.audience) ||
    !exactTools(launch.tools) ||
    launch.server.name !== "director-project-admin" ||
    !isAbsolute(launch.server.command) || launch.server.command.length > 4096 || /[\0\r\n]/u.test(launch.server.command) ||
    launch.server.args.length !== 7 ||
    launch.server.args[0] !== "project-admin-mcp" ||
    launch.server.args[1] !== "--engine-url" ||
    (() => { try { sessionEndpoint(launch.server.args[2]!); return false; } catch { return true; } })() ||
    launch.server.args[3] !== "--session" ||
    launch.server.args[4] !== launch.sessionId ||
    launch.server.args[5] !== "--audience" ||
    launch.server.args[6] !== launch.audience ||
    !exactKeys(launch.server.env as unknown as Record<string, unknown>, ["DIRECTOR_PROJECT_ADMIN_TOKEN"]) ||
    !SHA256_PATTERN.test(launch.server.env.DIRECTOR_PROJECT_ADMIN_TOKEN)
  ) {
    throw new ProjectAdminMCPConnectorError("PROJECT_ADMIN_MCP_DESCRIPTOR_INVALID");
  }
  return {
    mcpServers: {
      [launch.server.name]: {
        type: "stdio" as const,
        command: launch.server.command,
        args: [...launch.server.args],
        env: { ...launch.server.env },
      },
    },
    toolPolicy: {
      preapproved: launch.tools.map((tool) => ({ kind: "mcp" as const, server: launch.server.name, tool })),
    },
  };
}

export type ProjectAdminRegistration = Readonly<{
  requestId: string;
  sessionId: string;
  nativeWorkspaceId: string;
  nativeAgentId: string;
  audience: string;
  tokenSha256: string;
}>;

async function boundedJSON(response: Response): Promise<unknown> {
  const advertised = response.headers.get("content-length");
  if (advertised !== null && (!/^(?:0|[1-9][0-9]{0,19})$/u.test(advertised) ||
    Number(advertised) > PROJECT_ADMIN_MCP_MAXIMUM_RESPONSE_BYTES)) {
    throw new ProjectAdminMCPConnectorError("PROJECT_ADMIN_MCP_RESPONSE_INVALID");
  }
  if (response.body === null) {
    throw new ProjectAdminMCPConnectorError("PROJECT_ADMIN_MCP_RESPONSE_INVALID");
  }
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  try {
    for (;;) {
      const current = await reader.read();
      if (current.done) break;
      size += current.value.byteLength;
      if (size > PROJECT_ADMIN_MCP_MAXIMUM_RESPONSE_BYTES) {
        await reader.cancel();
        throw new ProjectAdminMCPConnectorError("PROJECT_ADMIN_MCP_RESPONSE_INVALID");
      }
      chunks.push(current.value);
    }
  } finally {
    reader.releaseLock();
  }
  const content = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) {
    content.set(chunk, offset);
    offset += chunk.byteLength;
  }
  try { return JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(content)); }
  catch { throw new ProjectAdminMCPConnectorError("PROJECT_ADMIN_MCP_RESPONSE_INVALID"); }
}

function sessionEndpoint(baseUrl: string): URL {
  let parsed: URL;
  try { parsed = new URL(baseUrl); } catch {
    throw new ProjectAdminMCPConnectorError("PROJECT_ADMIN_MCP_ENDPOINT_INVALID");
  }
  const hostname = parsed.hostname.startsWith("[") ? parsed.hostname.slice(1, -1) : parsed.hostname;
  const loopback = (isIP(hostname) === 4 && hostname.startsWith("127.")) || hostname === "::1";
  if (parsed.protocol !== "http:" || !loopback || parsed.port === "" || parsed.username !== "" ||
    parsed.password !== "" || parsed.pathname !== "/" || parsed.search !== "" || parsed.hash !== "") {
    throw new ProjectAdminMCPConnectorError("PROJECT_ADMIN_MCP_ENDPOINT_INVALID");
  }
  return new URL("/v1/project-admin-mcp/session", parsed);
}

function versionedHeaders(authorization: string): Record<string, string> {
  return {
    "content-type": "application/json",
    authorization: `Bearer ${authorization}`,
    "x-director-contract-version": PROJECT_ADMIN_MCP_CONTRACT_VERSION,
    "x-director-contract-sha256": PROJECT_ADMIN_MCP_CONTRACT_SHA256,
  };
}

function exactContractResponse(response: Response): boolean {
  const versions = response.headers.get("x-director-contract-version");
  const hashes = response.headers.get("x-director-contract-sha256");
  return versions === PROJECT_ADMIN_MCP_CONTRACT_VERSION && hashes === PROJECT_ADMIN_MCP_CONTRACT_SHA256 &&
    !versions.includes(",") && !hashes.includes(",");
}

export async function registerProjectAdminSession(options: {
  baseUrl: string;
  authorization: string;
  registration: ProjectAdminRegistration;
  fetch?: typeof globalThis.fetch;
}): Promise<void> {
  if (!SHA256_PATTERN.test(options.authorization) ||
    !exactKeys(options.registration as unknown as Record<string, unknown>, [
      "requestId", "sessionId", "nativeWorkspaceId", "nativeAgentId", "audience", "tokenSha256",
    ]) || !IDENTITY_PATTERN.test(options.registration.requestId) || !IDENTITY_PATTERN.test(options.registration.sessionId) ||
    !IDENTITY_PATTERN.test(options.registration.nativeWorkspaceId) || !IDENTITY_PATTERN.test(options.registration.nativeAgentId) ||
    !IDENTITY_PATTERN.test(options.registration.audience) || !SHA256_PATTERN.test(options.registration.tokenSha256)) {
    throw new ProjectAdminMCPConnectorError("PROJECT_ADMIN_MCP_REGISTRATION_INVALID");
  }
  const endpoint = sessionEndpoint(options.baseUrl);
  const fetcher = options.fetch ?? globalThis.fetch;
  const response = await fetcher(endpoint, {
    method: "POST",
    redirect: "error",
    headers: versionedHeaders(options.authorization),
    body: JSON.stringify({ action: "register", registration: options.registration, requestId: "", sessionId: "" }),
    signal: AbortSignal.timeout(10_000),
  });
  if (response.url !== endpoint.href || !response.ok || !exactContractResponse(response)) {
    throw new ProjectAdminMCPConnectorError("PROJECT_ADMIN_MCP_REGISTRATION_REFUSED");
  }
  const result = await boundedJSON(response) as Record<string, unknown>;
  if (!exactKeys(result, ["status", "sessionId", "projectVersion", "contractVersion"]) ||
    result.status !== "active" || result.sessionId !== options.registration.sessionId ||
    result.contractVersion !== PROJECT_ADMIN_MCP_CONTRACT_VERSION ||
    typeof result.projectVersion !== "string" || !/^(?:0|[1-9][0-9]{0,19})$/u.test(result.projectVersion)) {
    throw new ProjectAdminMCPConnectorError("PROJECT_ADMIN_MCP_RESPONSE_INVALID");
  }
}

export async function revokeProjectAdminSession(options: {
  baseUrl: string;
  authorization: string;
  requestId: string;
  sessionId: string;
  fetch?: typeof globalThis.fetch;
}): Promise<void> {
  if (!SHA256_PATTERN.test(options.authorization) || !IDENTITY_PATTERN.test(options.requestId) || !IDENTITY_PATTERN.test(options.sessionId)) {
    throw new ProjectAdminMCPConnectorError("PROJECT_ADMIN_MCP_REVOCATION_INVALID");
  }
  const endpoint = sessionEndpoint(options.baseUrl);
  const response = await (options.fetch ?? globalThis.fetch)(endpoint, {
    method: "POST", redirect: "error",
    headers: versionedHeaders(options.authorization),
    body: JSON.stringify({ action: "revoke", registration: {}, requestId: options.requestId, sessionId: options.sessionId }),
    signal: AbortSignal.timeout(10_000),
  });
  if (response.url !== endpoint.href || !response.ok || !exactContractResponse(response)) {
    throw new ProjectAdminMCPConnectorError("PROJECT_ADMIN_MCP_REVOCATION_REFUSED");
  }
  const result = await boundedJSON(response) as Record<string, unknown>;
  if (!exactKeys(result, ["status", "sessionId"]) || result.status !== "revoked" || result.sessionId !== options.sessionId) {
    throw new ProjectAdminMCPConnectorError("PROJECT_ADMIN_MCP_RESPONSE_INVALID");
  }
}
