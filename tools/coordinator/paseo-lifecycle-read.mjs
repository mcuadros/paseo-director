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

const EXIT = Object.freeze({
  authRequired: 64,
  authFailed: 65,
  responseRedacted: 66,
  readFailed: 67,
});

function fail(code, status = EXIT.readFailed) {
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

function normalizedMcpResult(document, requestId) {
  if (
    !isObject(document) || document.jsonrpc !== "2.0" || document.id !== requestId ||
    !isObject(document.result) || document.result.isError === true ||
    !isObject(document.result.structuredContent)
  ) {
    throw new Error("invalid MCP response");
  }
  return document.result.structuredContent;
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
  if (rest.length > 0) fail("PASEO_READ_ARGUMENT_INVALID");
  if (
    (operation === "agent.inspect" && (typeof identifier !== "string" || identifier.length === 0)) ||
    (operation === "workspace.list" && identifier !== undefined) ||
    !["agent.inspect", "workspace.list"].includes(operation)
  ) {
    fail("PASEO_READ_ARGUMENT_INVALID");
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
  const requestId = operation === "agent.inspect" ? "agent.inspect" : "workspace.list";
  const response = await postMcp(target, password, {
    jsonrpc: "2.0",
    id: requestId,
    method: "tools/call",
    params: {
      name: operation === "agent.inspect" ? "get_agent_status" : "list_workspaces",
      arguments: operation === "agent.inspect" ? { agentId: identifier } : {},
    },
  });
  if (response.body.includes(Buffer.from(password))) {
    fail("PASEO_LIFECYCLE_RESPONSE_REDACTED", EXIT.responseRedacted);
  }
  if (response.status === 401 || response.status === 403) {
    fail("PASEO_AUTH_FAILED", EXIT.authFailed);
  }
  if (response.status < 200 || response.status >= 300) {
    fail("PASEO_LIFECYCLE_READ_FAILED");
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
  let structured;
  try {
    structured = normalizedMcpResult(document, requestId);
  } catch {
    fail("PASEO_LIFECYCLE_OUTPUT_INVALID");
  }
  if (containsSecret(structured, password)) {
    fail("PASEO_LIFECYCLE_RESPONSE_REDACTED", EXIT.responseRedacted);
  }
  let normalized;
  try {
    normalized = operation === "agent.inspect"
      ? normalizedAgent(structured)
      : normalizedWorkspaces(structured);
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
  fail("PASEO_LIFECYCLE_READ_FAILED");
}
