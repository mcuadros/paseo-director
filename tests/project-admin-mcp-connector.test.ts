// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import test from "node:test";

import {
  createProjectAdminMCPInjection,
  ProjectAdminMCPConnectorError,
  registerProjectAdminSession,
  revokeProjectAdminSession,
} from "../connector/project-admin-mcp.server.ts";
import {
  PROJECT_ADMIN_MCP_CONTRACT_SHA256,
  PROJECT_ADMIN_MCP_CONTRACT_VERSION,
  PROJECT_ADMIN_MCP_MAXIMUM_RESPONSE_BYTES,
  PROJECT_ADMIN_MCP_TOOLS,
  type ProjectAdminMCPSessionLaunch,
} from "../generated/project-admin-mcp-contract.shared.ts";
import { PROJECT_ADMIN_RECONNECT_INSTRUCTION } from "../rpc/project-admin.shared.ts";

function launch(): ProjectAdminMCPSessionLaunch {
  return {
    contractVersion: PROJECT_ADMIN_MCP_CONTRACT_VERSION,
    contractHash: PROJECT_ADMIN_MCP_CONTRACT_SHA256,
    sessionSha256: "a".repeat(64),
    sessionId: "session-1",
    audience: "audience-1",
    tools: PROJECT_ADMIN_MCP_TOOLS.map((tool) => tool.name),
    server: {
      name: "director-project-admin",
      command: "/opt/director/director-engine",
      args: ["project-admin-mcp", "--engine-url", "http://127.0.0.1:7041/", "--session", "session-1", "--audience", "audience-1"],
      env: { DIRECTOR_PROJECT_ADMIN_TOKEN: "b".repeat(64) },
    },
  };
}

function response(url: string, body: string, options: ResponseInit): Response {
  const value = new Response(body, options);
  Object.defineProperty(value, "url", { value: url });
  return value;
}

const contractHeaders = {
  "content-type": "application/json",
  "x-director-contract-version": PROJECT_ADMIN_MCP_CONTRACT_VERSION,
  "x-director-contract-sha256": PROJECT_ADMIN_MCP_CONTRACT_SHA256,
};

test("project administration injection is exact and grants the complete closed catalog", () => {
  const value = launch();
  const injection = createProjectAdminMCPInjection(value);
  assert.deepEqual(Object.keys(injection.mcpServers), ["director-project-admin"]);
  assert.deepEqual(
    injection.toolPolicy.preapproved.map((entry) => entry.tool),
    PROJECT_ADMIN_MCP_TOOLS.map((tool) => tool.name),
  );
  assert.deepEqual(injection.mcpServers["director-project-admin"], {
    type: "stdio",
    command: value.server.command,
    args: value.server.args,
    env: value.server.env,
  });
  const serializedSchemas = JSON.stringify(PROJECT_ADMIN_MCP_TOOLS.map((tool) => tool.inputSchema));
  for (const selector of ["projectId", "hostId", "agentId", "nativeAgentId", "nativeWorkspaceId"]) {
    assert.equal(serializedSchemas.includes(`\"${selector}\"`), false);
  }
});

test("project administration injection rejects drift, selectors, non-loopback transport, and environment expansion", () => {
  const baseline = launch();
  for (const mutate of [
    (value: any) => { value.extra = true; },
    (value: any) => { value.contractHash = "0".repeat(64); },
    (value: any) => { value.tools = [...value.tools].reverse(); },
    (value: any) => { value.tools[0] = "raw_taskstore_query"; },
    (value: any) => { value.server.extra = true; },
    (value: any) => { value.server.command = "director-engine"; },
    (value: any) => { value.server.args[2] = "http://director.invalid:7041/"; },
    (value: any) => { value.server.args.push("--project", "project-b"); },
    (value: any) => { value.server.env.PROJECT_ID = "project-b"; },
  ]) {
    const changed = structuredClone(baseline) as any;
    mutate(changed);
    assert.throws(
      () => createProjectAdminMCPInjection(changed),
      (error: unknown) => error instanceof ProjectAdminMCPConnectorError && error.code === "PROJECT_ADMIN_MCP_DESCRIPTOR_INVALID",
    );
  }
});

test("registration and revocation use authenticated versioned no-redirect lifecycle calls", async () => {
  const calls: Array<{ url: string; init: RequestInit }> = [];
  const fetcher = (async (input: URL | RequestInfo, init?: RequestInit) => {
    calls.push({ url: String(input), init: init! });
    const parsed = JSON.parse(String(init?.body)) as Record<string, unknown>;
    const body = parsed.action === "register"
      ? JSON.stringify({ status: "active", sessionId: "session-1", projectVersion: "2", contractVersion: PROJECT_ADMIN_MCP_CONTRACT_VERSION })
      : JSON.stringify({ status: "revoked", sessionId: "session-1" });
    return response("http://127.0.0.1:7041/v1/project-admin-mcp/session", body, { status: 200, headers: contractHeaders });
  }) as typeof globalThis.fetch;
  const registration = { requestId: "request-1", sessionId: "session-1", nativeWorkspaceId: "workspace-1",
    nativeAgentId: "agent-1", audience: "audience-1", tokenSha256: "c".repeat(64) };
  await registerProjectAdminSession({ baseUrl: "http://127.0.0.1:7041/", authorization: "d".repeat(64), registration, fetch: fetcher });
  await revokeProjectAdminSession({ baseUrl: "http://127.0.0.1:7041/", authorization: "d".repeat(64), requestId: "revoke-1", sessionId: "session-1", fetch: fetcher });
  assert.equal(calls.length, 2);
  for (const call of calls) {
    assert.equal(call.url, "http://127.0.0.1:7041/v1/project-admin-mcp/session");
    assert.equal(call.init.method, "POST");
    assert.equal(call.init.redirect, "error");
    const headers = call.init.headers as Record<string, string>;
    assert.equal(headers.authorization, `Bearer ${"d".repeat(64)}`);
    assert.equal(headers["x-director-contract-version"], PROJECT_ADMIN_MCP_CONTRACT_VERSION);
    assert.equal(headers["x-director-contract-sha256"], PROJECT_ADMIN_MCP_CONTRACT_SHA256);
  }
});

test("lifecycle transport refuses confused endpoints, redirects, contract drift, and oversized output", async () => {
  const registration = { requestId: "request-1", sessionId: "session-1", nativeWorkspaceId: "workspace-1",
    nativeAgentId: "agent-1", audience: "audience-1", tokenSha256: "c".repeat(64) };
  await assert.rejects(
    registerProjectAdminSession({ baseUrl: "https://example.com/", authorization: "d".repeat(64), registration }),
    /PROJECT_ADMIN_MCP_ENDPOINT_INVALID/u,
  );
  const cases = [
    response("http://127.0.0.1:7041/elsewhere", `{}`, { status: 200, headers: contractHeaders }),
    response("http://127.0.0.1:7041/v1/project-admin-mcp/session", `{}`, { status: 200 }),
    response("http://127.0.0.1:7041/v1/project-admin-mcp/session", "x".repeat(PROJECT_ADMIN_MCP_MAXIMUM_RESPONSE_BYTES + 1), { status: 200, headers: contractHeaders }),
  ];
  for (const current of cases) {
    await assert.rejects(registerProjectAdminSession({ baseUrl: "http://127.0.0.1:7041/", authorization: "d".repeat(64), registration,
      fetch: (async () => current) as typeof globalThis.fetch }));
  }
});

test("exact Paseo 0.7.2 public lifecycle requires recreation and supplies one exact instruction", () => {
  const clientTypes = readFileSync(resolve("node_modules/@getpaseo/client/dist/index.d.ts"), "utf8");
  const pluginTypes = readFileSync(resolve("node_modules/@getpaseo/plugin/dist/contracts.d.ts"), "utf8");
  const createConfig = clientTypes.slice(clientTypes.indexOf("export interface PaseoAgentConfig"), clientTypes.indexOf("export interface PaseoAgentCreateOptions"));
  const liveHandle = clientTypes.slice(clientTypes.indexOf("export interface PaseoAgentHandle"), clientTypes.indexOf("export interface PaseoAgentActions"));
  const handlerContext = pluginTypes.slice(pluginTypes.indexOf("export interface PluginHandlerContext"), pluginTypes.indexOf("export interface PluginContext"));
  assert.match(createConfig, /toolPolicy\?:/u);
  assert.match(createConfig, /mcpServers\?:/u);
  assert.doesNotMatch(liveHandle, /mcpServers|toolPolicy|config(?:ure|uration)?\s*\(/u);
  assert.doesNotMatch(handlerContext, /session|interceptor|configurator/iu);
  assert.equal(
    PROJECT_ADMIN_RECONNECT_INSTRUCTION,
    "Open ‘Create Director administration session’ from this agent. Paseo 0.7.2 cannot add MCP servers to the current live session, so Director creates a new session in the same Project; re-send your request there. Do not restart Paseo.",
  );
  assert.doesNotMatch(PROJECT_ADMIN_RECONNECT_INSTRUCTION, /Restart Paseo (?:to|and)|forge|storage/u);
});
