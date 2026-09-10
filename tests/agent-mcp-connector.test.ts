// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import test from "node:test";

import {
  createPaseoSessionMCPInjection,
  translatePublicPaseoSessionFacts,
} from "../connector/agent-mcp.server.ts";
import {
  AGENT_MCP_CONTRACT_SHA256,
  AGENT_MCP_CONTRACT_VERSION,
  type AgentMCPSessionLaunch,
} from "../generated/agent-mcp-contract.shared.ts";

const publicFacts = {
  source: "public-paseo-v0.7" as const,
  discoveryRevision: "a".repeat(64),
  paseoVersion: "0.7.2",
  provider: "codex" as const,
  cliVersion: "0.147.0",
  state: "ready" as const,
  model: "gpt-5.4-mini",
  effort: "high",
  mode: "default",
  permissionMode: "workspace-write",
  providerOptions: [{ name: "networkAccess", value: "disabled" }],
  mcpCapabilities: [
    "project.read" as const,
    "task.read" as const,
    "task.outcome.submit" as const,
  ],
  liveSessionSupportsMcpServers: true,
  exactToolPreapprovalObserved: true,
  runtimeProbePassed: true,
};

test("public Paseo v0.7 session facts translate without policy or raw output", () => {
  const observation = translatePublicPaseoSessionFacts(publicFacts);
  assert.deepEqual(observation, {
    schemaVersion: 1,
    contractVersion: AGENT_MCP_CONTRACT_VERSION,
    contractHash: AGENT_MCP_CONTRACT_SHA256,
    discoveryRevision: "a".repeat(64),
    paseoVersion: "0.7.2",
    provider: "codex",
    cliVersion: "0.147.0",
    state: "ready",
    model: "gpt-5.4-mini",
    effort: "high",
    mode: "default",
    permissionMode: "workspace-write",
    providerOptions: [{ name: "networkAccess", value: "disabled" }],
    mcpCapabilities: [
      "project.read",
      "task.read",
      "task.outcome.submit",
    ],
    sessionStdioMcp: true,
    exactMcpToolPolicy: true,
    runtimeProbePassed: true,
  });
  assert.equal("rawError" in observation, false);
  assert.equal("credential" in observation, false);
  const capabilityLoss = translatePublicPaseoSessionFacts({
    ...publicFacts,
    liveSessionSupportsMcpServers: false,
    exactToolPreapprovalObserved: false,
  });
  assert.equal(capabilityLoss.sessionStdioMcp, false);
  assert.equal(capabilityLoss.exactMcpToolPolicy, false);
});

test("connector emits only documented per-session stdio MCP and exact grants", () => {
  const launch: AgentMCPSessionLaunch = {
    contractVersion: AGENT_MCP_CONTRACT_VERSION,
    contractHash: AGENT_MCP_CONTRACT_SHA256,
    sessionSha256: "b".repeat(64),
    role: "worker",
    provider: "codex",
    model: "gpt-5.4-mini",
    tools: [
      "director_project_read",
      "director_task_read",
      "director_task_outcome_submit",
    ],
    server: {
      name: "director_run_scope",
      command: "/opt/director/bin/director-mcp-bridge",
      args: ["--session-fd=3"],
      env: {},
    },
  };
  assert.deepEqual(createPaseoSessionMCPInjection(launch), {
    mcpServers: {
      director_run_scope: {
        type: "stdio",
        command: "/opt/director/bin/director-mcp-bridge",
        args: ["--session-fd=3"],
        env: {},
      },
    },
    toolPolicy: {
      preapproved: [
        { kind: "mcp", server: "director_run_scope", tool: "director_project_read" },
        { kind: "mcp", server: "director_run_scope", tool: "director_task_read" },
        {
          kind: "mcp",
          server: "director_run_scope",
          tool: "director_task_outcome_submit",
        },
      ],
    },
  });
});

test("connector rejects contract drift, unknown tools, unknown fields, and generic providers", () => {
  const launch: AgentMCPSessionLaunch = {
    contractVersion: AGENT_MCP_CONTRACT_VERSION,
    contractHash: AGENT_MCP_CONTRACT_SHA256,
    sessionSha256: "b".repeat(64),
    role: "reviewer",
    provider: "opencode",
    model: "opencode/nemotron-3-ultra-free",
    tools: ["director_candidate_read", "director_review_verdict_submit"],
    server: {
      name: "director_review_scope",
      command: "/opt/director/bin/director-mcp-bridge",
      args: ["--session-fd=3"],
      env: {},
    },
  };
  assert.throws(
    () =>
      createPaseoSessionMCPInjection({
        ...launch,
        contractHash: "0".repeat(64) as typeof AGENT_MCP_CONTRACT_SHA256,
      }),
    /PASEO_SESSION_MCP_DESCRIPTOR_INVALID/,
  );
  assert.throws(
    () =>
      createPaseoSessionMCPInjection({
        ...launch,
        tools: ["raw_taskstore_query" as "director_candidate_read"],
      }),
    /PASEO_SESSION_MCP_DESCRIPTOR_INVALID/,
  );
  assert.throws(
    () => translatePublicPaseoSessionFacts({ ...publicFacts, rawError: "provider stderr" }),
    /PASEO_PROVIDER_FACT_INVALID/,
  );
  assert.throws(
    () => translatePublicPaseoSessionFacts({ ...publicFacts, provider: "generic-acp" }),
    /PASEO_PROVIDER_FACT_INVALID/,
  );
});
