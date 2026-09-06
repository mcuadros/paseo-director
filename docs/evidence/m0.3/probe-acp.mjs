#!/usr/bin/env node

import { appendFileSync } from "node:fs";
import { spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import { createInterface } from "node:readline";

if (process.argv.includes("--version")) {
  process.stdout.write("director-probe-acp 1.0.0\n");
  process.exit(0);
}

const logPath = process.env.DIRECTOR_ACP_LOG;
const mode = process.argv.includes("--unsupported-mcp") ? "unsupported-mcp" : "compatible";
const sessions = new Map();

function append(event) {
  if (!logPath) return;
  appendFileSync(
    logPath,
    `${JSON.stringify({ source: "acp", mode, pid: process.pid, ...event })}\n`,
    { encoding: "utf8", mode: 0o600 },
  );
}

function send(message) {
  process.stdout.write(`${JSON.stringify(message)}\n`);
}

function result(id, value) {
  send({ jsonrpc: "2.0", id, result: value });
}

function error(id, code, message) {
  send({ jsonrpc: "2.0", id, error: { code, message } });
}

function notify(sessionId, update) {
  send({ jsonrpc: "2.0", method: "session/update", params: { sessionId, update } });
}

function envFromAcp(entries) {
  return Object.fromEntries(
    Array.isArray(entries)
      ? entries.flatMap((entry) =>
          typeof entry?.name === "string" && typeof entry?.value === "string"
            ? [[entry.name, entry.value]]
            : [],
        )
      : [],
  );
}

async function connectAndCall(server) {
  if (!server || server.type === "http" || server.type === "sse") {
    throw new Error("The deterministic ACP fixture accepts one stdio MCP server only");
  }

  const child = spawn(server.command, server.args ?? [], {
    env: { ...process.env, ...envFromAcp(server.env) },
    stdio: ["pipe", "pipe", "pipe"],
  });
  const pending = new Map();
  const childLines = createInterface({ input: child.stdout, crlfDelay: Infinity });
  childLines.on("line", (line) => {
    let message;
    try {
      message = JSON.parse(line);
    } catch (cause) {
      for (const waiter of pending.values()) waiter.reject(cause);
      pending.clear();
      return;
    }
    const waiter = pending.get(message.id);
    if (!waiter) return;
    pending.delete(message.id);
    if (message.error) waiter.reject(new Error(message.error.message ?? "MCP error"));
    else waiter.resolve(message.result);
  });

  let nextId = 1;
  function request(method, params) {
    const id = nextId++;
    child.stdin.write(`${JSON.stringify({ jsonrpc: "2.0", id, method, params })}\n`);
    return new Promise((resolve, reject) => pending.set(id, { resolve, reject }));
  }

  try {
    await request("initialize", {
      protocolVersion: "2025-06-18",
      capabilities: {},
      clientInfo: { name: "director-probe-acp", version: "1.0.0" },
    });
    child.stdin.write(
      `${JSON.stringify({ jsonrpc: "2.0", method: "notifications/initialized", params: {} })}\n`,
    );
    const catalog = await request("tools/list", {});
    const names = Array.isArray(catalog?.tools)
      ? catalog.tools.flatMap((tool) => (typeof tool?.name === "string" ? [tool.name] : []))
      : [];
    if (names.length !== 1 || names[0] !== "read_scope_nonce") {
      throw new Error(`Unexpected scoped tool catalog: ${JSON.stringify(names)}`);
    }
    const call = await request("tools/call", { name: "read_scope_nonce", arguments: {} });
    const text = Array.isArray(call?.content)
      ? call.content
          .filter((item) => item?.type === "text" && typeof item.text === "string")
          .map((item) => item.text)
          .join("")
      : "";
    if (!text.startsWith("DIRECTOR_SCOPE_OK ")) {
      throw new Error(`Unexpected MCP result: ${JSON.stringify(text)}`);
    }
    return { names, text };
  } finally {
    child.stdin.end();
    await new Promise((resolve) => {
      const timer = setTimeout(() => {
        child.kill("SIGTERM");
        resolve();
      }, 2_000);
      child.once("exit", () => {
        clearTimeout(timer);
        resolve();
      });
    });
  }
}

async function handleRequest(message) {
  const method = message.method;
  append({ event: "received", method, requestId: message.id ?? null });

  if (method === "initialize") {
    result(message.id, {
      protocolVersion: 1,
      agentCapabilities: { loadSession: false },
      agentInfo: { name: `director-probe-acp-${mode}`, version: "1.0.0" },
    });
    return;
  }

  if (method === "authenticate") {
    result(message.id, {});
    return;
  }

  if (method === "session/new") {
    const sessionId = randomUUID();
    const mcpServers = Array.isArray(message.params?.mcpServers) ? message.params.mcpServers : [];
    sessions.set(sessionId, { mcpServers });
    append({
      event: "session_created",
      sessionId,
      mcpServerCount: mcpServers.length,
      mcpServerNames: mcpServers.map((server) => server?.name ?? null),
    });
    result(message.id, {
      sessionId,
      modes: null,
      models: {
        currentModelId: "fixture",
        availableModels: [{ modelId: "fixture", name: "Fixture" }],
      },
      configOptions: [],
    });
    return;
  }

  if (method === "session/prompt") {
    const session = sessions.get(message.params?.sessionId);
    append({ event: "prompt_received", sessionId: message.params?.sessionId ?? null });
    if (!session) {
      error(message.id, -32602, "Unknown session");
      return;
    }
    if (mode !== "compatible") {
      error(message.id, -32603, "Unsupported fixture must never receive a prompt");
      return;
    }
    if (session.mcpServers.length !== 1) {
      error(message.id, -32602, "Expected exactly one session MCP server");
      return;
    }

    try {
      const observation = await connectAndCall(session.mcpServers[0]);
      const toolCallId = randomUUID();
      notify(message.params.sessionId, {
        sessionUpdate: "tool_call",
        toolCallId,
        title: "Read isolated session scope nonce",
        kind: "read",
        status: "in_progress",
        rawInput: { tool: observation.names[0] },
      });
      notify(message.params.sessionId, {
        sessionUpdate: "tool_call_update",
        toolCallId,
        status: "completed",
        content: [{ type: "content", content: { type: "text", text: observation.text } }],
        rawOutput: { text: observation.text },
      });
      notify(message.params.sessionId, {
        sessionUpdate: "agent_message_chunk",
        content: { type: "text", text: observation.text },
      });
      append({
        event: "mcp_call_completed",
        sessionId: message.params.sessionId,
        tools: observation.names,
        resultText: observation.text,
      });
      result(message.id, { stopReason: "end_turn" });
    } catch (cause) {
      append({
        event: "mcp_call_failed",
        sessionId: message.params.sessionId,
        message: cause instanceof Error ? cause.message : String(cause),
      });
      error(message.id, -32603, cause instanceof Error ? cause.message : String(cause));
    }
    return;
  }

  if (method === "session/cancel") {
    return;
  }

  if (method === "session/set_mode" || method === "session/set_model") {
    result(message.id, {});
    return;
  }

  if (message.id !== undefined && message.id !== null) {
    error(message.id, -32601, `Unsupported method: ${method}`);
  }
}

append({ event: "process_started" });
const lines = createInterface({ input: process.stdin, crlfDelay: Infinity });
let queue = Promise.resolve();
lines.on("line", (line) => {
  queue = queue
    .then(async () => {
      const message = JSON.parse(line);
      await handleRequest(message);
    })
    .catch((cause) => {
      append({ event: "request_failed", message: cause instanceof Error ? cause.message : String(cause) });
    });
});
lines.on("close", () => {
  append({ event: "process_stdin_closed" });
});
