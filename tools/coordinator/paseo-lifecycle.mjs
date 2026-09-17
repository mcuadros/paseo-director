#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0

import { spawnSync } from "node:child_process";
import http from "node:http";
import https from "node:https";
import { networkInterfaces } from "node:os";

const MAXIMUM_RESPONSE_BYTES = 1_048_576;
const REQUEST_TIMEOUT_MS = 15_000;
const STATUS_TIMEOUT_MS = 10_000;
const PARENT_AGENT_ID_LABEL = "paseo.parent-agent-id";
// The coordinator's Reviewer identity proof reads the agent's own Director
// labels, so exactly that namespace is projected and nothing else. Bounds are
// applied here rather than downstream: a label map is daemon-controlled input.
const DIRECTOR_LABEL_PREFIX = "director.";
const MAXIMUM_DIRECTOR_LABELS = 32;
const MAXIMUM_LABEL_KEY_BYTES = 64;
const MAXIMUM_LABEL_VALUE_BYTES = 256;
// The documented public maxima of the exact `list_agents` request. The page is
// bounded two ways and both bound what its result can establish: a response
// holding `limit` records is indistinguishable from a truncated one, and every
// response whatever its size excludes agents older than the window. Both travel
// with the result so the coordinator states the scope it actually observed
// rather than reading an unsaturated page as a complete one.
const AGENT_LIST_LIMIT = 200;
const AGENT_LIST_SINCE_HOURS = 720;

const EXIT = Object.freeze({
  authRequired: 64,
  authFailed: 65,
  responseRedacted: 66,
  readFailed: 67,
  mutationFailed: 68,
});

/**
 * The five bounded lifecycle operations this child may perform, each mapped to
 * exactly one documented public Paseo 0.7.2 Agent MCP tool. Mutations are
 * dispatched here for the same reason reads are: the HTTP bearer boundary
 * accepts a valid delimiter-rich password that the CLI's WebSocket subprotocol
 * grammar rejects.
 */
const OPERATIONS = new Map([
  ["agent.inspect", { tool: "get_agent_status", argument: "agentId", mutation: false }],
  ["workspace.list", { tool: "list_workspaces", argument: null, mutation: false }],
  [
    "agent.list",
    {
      tool: "list_agents",
      argument: null,
      mutation: false,
      fixedArguments: {
        includeArchived: false,
        limit: AGENT_LIST_LIMIT,
        sinceHours: AGENT_LIST_SINCE_HOURS,
      },
    },
  ],
  ["agent.archive", { tool: "archive_agent", argument: "agentId", mutation: true }],
  ["workspace.archive", { tool: "archive_workspace", argument: "workspaceId", mutation: true }],
]);

// A failure before the operation is known is reported as a read failure; main()
// narrows this once it has selected one bounded operation.
let unavailable = { code: "PASEO_LIFECYCLE_READ_FAILED", status: EXIT.readFailed };

function fail(code, status = unavailable.status) {
  process.stderr.write(`${code}\n`);
  process.exit(status);
}

function isObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function containsSecret(value, secret) {
  if (typeof value === "string") return value.includes(secret);
  if (Array.isArray(value)) return value.some((item) => containsSecret(item, secret));
  if (isObject(value)) {
    return Object.values(value).some((item) => containsSecret(item, secret));
  }
  return false;
}

function selectedStatusEnvironment() {
  const selected = {};
  for (const key of [
    "PATH",
    "LANG",
    "LC_ALL",
    "TMPDIR",
    "HOME",
    "XDG_CONFIG_HOME",
    "PASEO_HOME",
  ]) {
    const value = process.env[key];
    if (value !== undefined) selected[key] = value;
  }
  return selected;
}

function localDaemonTarget() {
  const result = spawnSync("paseo", ["daemon", "status", "--json"], {
    encoding: "utf8",
    env: selectedStatusEnvironment(),
    maxBuffer: MAXIMUM_RESPONSE_BYTES,
    shell: false,
    timeout: STATUS_TIMEOUT_MS,
  });
  if (result.error || result.status !== 0) fail("PASEO_LOCAL_TARGET_UNAVAILABLE");
  let status;
  try {
    status = JSON.parse(result.stdout);
  } catch {
    fail("PASEO_LOCAL_TARGET_INVALID");
  }
  if (
    !isObject(status) || status.localDaemon !== "running" ||
    typeof status.listen !== "string" || status.listen.length === 0
  ) {
    fail("PASEO_LOCAL_TARGET_INVALID");
  }
  return status.listen;
}

function isLoopback(hostname) {
  const normalized = hostname.toLowerCase();
  return normalized === "localhost" || normalized.endsWith(".localhost") ||
    normalized === "::1" || normalized === "0:0:0:0:0:0:0:1" ||
    /^127(?:\.[0-9]{1,3}){3}$/u.test(normalized);
}

function isLocalInterface(hostname) {
  if (isLoopback(hostname)) return true;
  const normalized = hostname.toLowerCase().split("%")[0];
  return Object.values(networkInterfaces())
    .flatMap((addresses) => addresses ?? [])
    .some((address) => address.address.toLowerCase().split("%")[0] === normalized);
}

function parsedTarget(rawTarget, { discovered }) {
  const trimmed = rawTarget.trim();
  if (trimmed.length === 0 || trimmed.includes("\0")) fail("PASEO_TARGET_INVALID");
  if (trimmed.startsWith("unix://") || trimmed.startsWith("/")) {
    const socketPath = trimmed.startsWith("unix://")
      ? trimmed.slice("unix://".length)
      : trimmed;
    if (!socketPath.startsWith("/") || socketPath.length > 4_096) {
      fail("PASEO_TARGET_INVALID");
    }
    return { kind: "unix", socketPath };
  }
  if (trimmed.startsWith("pipe://") || trimmed.startsWith("ssh://")) {
    fail("PASEO_TARGET_UNSUPPORTED");
  }

  let parsed;
  try {
    parsed = new URL(trimmed.startsWith("tcp://")
      ? trimmed.replace(/^tcp:/u, "http:")
      : `http://${trimmed}`);
  } catch {
    fail("PASEO_TARGET_INVALID");
  }
  if (
    parsed.username.length > 0 || parsed.password.length > 0 ||
    parsed.hash.length > 0 || parsed.pathname !== "/"
  ) {
    fail("PASEO_TARGET_INVALID");
  }
  const keys = [...parsed.searchParams.keys()];
  if (keys.some((key) => key !== "ssl") || keys.filter((key) => key === "ssl").length > 1) {
    fail("PASEO_TARGET_INVALID");
  }
  const ssl = parsed.searchParams.get("ssl");
  if (ssl !== null && ssl !== "true") fail("PASEO_TARGET_INVALID");
  const port = parsed.port.length > 0 ? Number(parsed.port) : null;
  if (!Number.isSafeInteger(port) || port < 1 || port > 65_535) {
    fail("PASEO_TARGET_INVALID");
  }
  let hostname = parsed.hostname;
  if (hostname.startsWith("[") && hostname.endsWith("]")) {
    hostname = hostname.slice(1, -1);
  }
  if (discovered && (hostname === "0.0.0.0" || hostname === "::")) {
    hostname = hostname === "::" ? "::1" : "127.0.0.1";
  }
  if (!isLoopback(hostname) && !(discovered && isLocalInterface(hostname))) {
    fail("PASEO_TARGET_NOT_LOCAL");
  }
  return {
    kind: "tcp",
    hostname,
    port,
    secure: ssl === "true",
  };
}

function parseResponseBody(body, contentType) {
  const text = body.toString("utf8");
  if (!contentType.toLowerCase().includes("text/event-stream")) {
    return JSON.parse(text);
  }
  const documents = [];
  for (const event of text.split(/\r?\n\r?\n/u)) {
    const data = event
      .split(/\r?\n/u)
      .filter((line) => line.startsWith("data:"))
      .map((line) => line.slice("data:".length).trimStart())
      .join("\n");
    if (data.length > 0) documents.push(JSON.parse(data));
  }
  if (documents.length !== 1) throw new Error("unexpected MCP event count");
  return documents[0];
}

function postMcp(target, password, request) {
  return new Promise((resolve, reject) => {
    const body = Buffer.from(JSON.stringify(request));
    const headers = {
      Accept: "application/json, text/event-stream",
      Authorization: `Bearer ${password}`,
      "Content-Length": String(body.length),
      "Content-Type": "application/json",
      "MCP-Protocol-Version": "2025-03-26",
    };
    const requestOptions = target.kind === "unix"
      ? {
          socketPath: target.socketPath,
          path: "/mcp/agents",
          method: "POST",
          headers,
        }
      : {
          hostname: target.hostname,
          port: target.port,
          path: "/mcp/agents",
          method: "POST",
          headers,
        };
    const transport = target.kind === "tcp" && target.secure ? https : http;
    let settled = false;
    const finish = (callback) => {
      if (settled) return;
      settled = true;
      callback();
    };
    const clientRequest = transport.request(requestOptions, (response) => {
      const chunks = [];
      let size = 0;
      response.on("data", (chunk) => {
        size += chunk.length;
        if (size > MAXIMUM_RESPONSE_BYTES) {
          clientRequest.destroy();
          finish(() => reject(new Error("response too large")));
          return;
        }
        chunks.push(chunk);
      });
      response.on("end", () => {
        finish(() => resolve({
          body: Buffer.concat(chunks),
          contentType: String(response.headers["content-type"] ?? ""),
          status: response.statusCode ?? 0,
        }));
      });
      response.on("error", (error) => finish(() => reject(error)));
    });
    clientRequest.setTimeout(REQUEST_TIMEOUT_MS, () => {
      clientRequest.destroy();
      finish(() => reject(new Error("request timed out")));
    });
    clientRequest.on("error", (error) => finish(() => reject(error)));
    clientRequest.end(body);
  });
}

/**
 * A mutation is accepted only on a well-formed non-error envelope for the exact
 * request identity. Its payload is never authoritative, so no shape beyond the
 * envelope is required of it.
 */
function mcpResult(document, requestId) {
  if (
    !isObject(document) || document.jsonrpc !== "2.0" || document.id !== requestId ||
    !isObject(document.result) || document.result.isError === true
  ) {
    throw new Error("invalid MCP response");
  }
  return document.result;
}

function normalizedMcpResult(document, requestId) {
  const result = mcpResult(document, requestId);
  if (!isObject(result.structuredContent)) throw new Error("invalid MCP response");
  return result.structuredContent;
}

function normalizedDirectorLabels(labels) {
  const entries = Object.entries(labels).filter(([key]) =>
    key.startsWith(DIRECTOR_LABEL_PREFIX),
  );
  if (entries.length > MAXIMUM_DIRECTOR_LABELS) {
    throw new Error("invalid agent labels");
  }
  const projected = {};
  for (const [key, label] of entries) {
    if (
      typeof label !== "string" ||
      Buffer.byteLength(key) > MAXIMUM_LABEL_KEY_BYTES ||
      Buffer.byteLength(label) > MAXIMUM_LABEL_VALUE_BYTES
    ) {
      throw new Error("invalid agent labels");
    }
    projected[key] = label;
  }
  return projected;
}

function normalizedAgent(value) {
  if (!isObject(value) || !isObject(value.snapshot) || value.status !== value.snapshot.status) {
    throw new Error("invalid agent snapshot");
  }
  const snapshot = value.snapshot;
  if (
    typeof snapshot.id !== "string" || snapshot.id.length === 0 ||
    typeof snapshot.title !== "string" ||
    typeof snapshot.cwd !== "string" || typeof snapshot.status !== "string" ||
    !isObject(snapshot.labels)
  ) {
    throw new Error("invalid agent snapshot");
  }
  const parent = snapshot.labels[PARENT_AGENT_ID_LABEL];
  if (parent !== undefined && (typeof parent !== "string" || parent.length === 0)) {
    throw new Error("invalid agent parentage");
  }
  const archivedAt = snapshot.archivedAt ?? null;
  if (archivedAt !== null && typeof archivedAt !== "string") {
    throw new Error("invalid agent archive state");
  }
  return {
    Id: snapshot.id,
    Name: snapshot.title,
    Status: snapshot.status,
    Archived: archivedAt !== null,
    ArchivedAt: archivedAt,
    Cwd: snapshot.cwd,
    ParentAgentId: parent ?? null,
    Labels: normalizedDirectorLabels(snapshot.labels),
  };
}

/**
 * Projects one bounded page of live agents. The requested limit and window
 * travel with the records: a full page carries no evidence that the daemon had
 * nothing more to report, and no page at all carries evidence about agents
 * older than the window.
 */
function normalizedAgents(value) {
  if (!isObject(value) || !Array.isArray(value.agents)) {
    throw new Error("invalid agent list");
  }
  if (value.agents.length > AGENT_LIST_LIMIT) throw new Error("invalid agent list");
  return {
    Limit: AGENT_LIST_LIMIT,
    WindowHours: AGENT_LIST_SINCE_HOURS,
    Agents: value.agents.map((agent) => {
      if (
        !isObject(agent) || typeof agent.id !== "string" || agent.id.length === 0 ||
        typeof agent.title !== "string" || typeof agent.cwd !== "string" ||
        typeof agent.status !== "string" || !isObject(agent.labels)
      ) {
        throw new Error("invalid agent list");
      }
      const archivedAt = agent.archivedAt ?? null;
      if (archivedAt !== null && typeof archivedAt !== "string") {
        throw new Error("invalid agent list");
      }
      const parent = agent.labels[PARENT_AGENT_ID_LABEL];
      if (parent !== undefined && (typeof parent !== "string" || parent.length === 0)) {
        throw new Error("invalid agent list");
      }
      return {
        Id: agent.id,
        Name: agent.title,
        Status: agent.status,
        Archived: archivedAt !== null,
        ArchivedAt: archivedAt,
        Cwd: agent.cwd,
        ParentAgentId: parent ?? null,
        Labels: normalizedDirectorLabels(agent.labels),
      };
    }),
  };
}

function normalizedWorkspaces(value) {
  if (!isObject(value) || !Array.isArray(value.workspaces)) {
    throw new Error("invalid workspace list");
  }
  return value.workspaces.map((workspace) => {
    if (
      !isObject(workspace) || typeof workspace.workspaceId !== "string" ||
      typeof workspace.projectId !== "string" || typeof workspace.cwd !== "string" ||
      !["local", "worktree"].includes(workspace.isolation) ||
      !["directory", "local_checkout", "worktree"].includes(workspace.kind) ||
      (workspace.title !== null && typeof workspace.title !== "string")
    ) {
      throw new Error("invalid workspace entry");
    }
    return {
      workspaceId: workspace.workspaceId,
      cwd: workspace.cwd,
      isolation: workspace.isolation,
    };
  });
}

async function main() {
  const [operation, identifier, ...rest] = process.argv.slice(2);
  if (rest.length > 0) fail("PASEO_LIFECYCLE_ARGUMENT_INVALID");
  const selected = OPERATIONS.get(operation);
  if (
    selected === undefined ||
    (selected.argument === null
      ? identifier !== undefined
      : typeof identifier !== "string" || identifier.length === 0)
  ) {
    fail("PASEO_LIFECYCLE_ARGUMENT_INVALID");
  }
  if (selected.mutation) {
    unavailable = {
      code: "PASEO_LIFECYCLE_MUTATION_FAILED",
      status: EXIT.mutationFailed,
    };
  }
  const password = process.env.PASEO_PASSWORD;
  if (typeof password !== "string" || password.length === 0) {
    fail("PASEO_AUTH_REQUIRED", EXIT.authRequired);
  }
  const explicitTarget = process.env.PASEO_HOST;
  if (typeof explicitTarget === "string" && explicitTarget.includes(password)) {
    fail("PASEO_TARGET_INVALID");
  }
  const target = parsedTarget(
    explicitTarget === undefined ? localDaemonTarget() : explicitTarget,
    { discovered: explicitTarget === undefined },
  );
  const response = await postMcp(target, password, {
    jsonrpc: "2.0",
    id: operation,
    method: "tools/call",
    params: {
      name: selected.tool,
      arguments: selected.argument === null
        ? { ...(selected.fixedArguments ?? {}) }
        : { [selected.argument]: identifier },
    },
  });
  if (response.body.includes(Buffer.from(password))) {
    fail("PASEO_LIFECYCLE_RESPONSE_REDACTED", EXIT.responseRedacted);
  }
  if (response.status === 401 || response.status === 403) {
    fail("PASEO_AUTH_FAILED", EXIT.authFailed);
  }
  if (response.status < 200 || response.status >= 300) {
    fail(unavailable.code);
  }
  let document;
  try {
    document = parseResponseBody(response.body, response.contentType);
  } catch {
    fail("PASEO_LIFECYCLE_OUTPUT_INVALID");
  }
  if (containsSecret(document, password)) {
    fail("PASEO_LIFECYCLE_RESPONSE_REDACTED", EXIT.responseRedacted);
  }
  if (selected.mutation) {
    try {
      mcpResult(document, operation);
    } catch {
      fail("PASEO_LIFECYCLE_OUTPUT_INVALID");
    }
    // The daemon's mutation payload proves nothing and is not forwarded. The
    // coordinator's authoritative readback is the sole completion evidence, so
    // only a fixed acknowledgement of the accepted dispatch leaves this child.
    process.stdout.write(`${JSON.stringify({ operation, dispatched: true })}\n`);
    return;
  }
  let structured;
  try {
    structured = normalizedMcpResult(document, operation);
  } catch {
    fail("PASEO_LIFECYCLE_OUTPUT_INVALID");
  }
  if (containsSecret(structured, password)) {
    fail("PASEO_LIFECYCLE_RESPONSE_REDACTED", EXIT.responseRedacted);
  }
  let normalized;
  try {
    if (operation === "agent.inspect") normalized = normalizedAgent(structured);
    else if (operation === "agent.list") normalized = normalizedAgents(structured);
    else normalized = normalizedWorkspaces(structured);
  } catch {
    fail("PASEO_LIFECYCLE_OUTPUT_INVALID");
  }
  if (containsSecret(normalized, password)) {
    fail("PASEO_LIFECYCLE_RESPONSE_REDACTED", EXIT.responseRedacted);
  }
  process.stdout.write(`${JSON.stringify(normalized)}\n`);
}

try {
  await main();
} catch {
  fail(unavailable.code);
}
