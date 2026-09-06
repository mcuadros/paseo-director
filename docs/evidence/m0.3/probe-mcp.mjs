#!/usr/bin/env node

import { appendFileSync } from "node:fs";
import { createInterface } from "node:readline";

const logPath = requireEnv("DIRECTOR_PROBE_LOG");
const nonce = requireEnv("DIRECTOR_PROBE_NONCE");
const row = requireEnv("DIRECTOR_PROBE_ROW");
const toolName = "read_scope_nonce";

function requireEnv(name) {
  const value = process.env[name];
  if (!value) {
    process.stderr.write(`Missing ${name}\n`);
    process.exit(2);
  }
  return value;
}

function append(event) {
  appendFileSync(
    logPath,
    `${JSON.stringify({ source: "mcp", row, pid: process.pid, ...event })}\n`,
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

append({ event: "process_started", advertisedTools: [toolName] });

const lines = createInterface({ input: process.stdin, crlfDelay: Infinity });
lines.on("line", (line) => {
  let message;
  try {
    message = JSON.parse(line);
  } catch {
    append({ event: "invalid_json" });
    return;
  }

  const method = typeof message.method === "string" ? message.method : null;
  append({
    event: "received",
    method,
    requestId: message.id ?? null,
    toolName: method === "tools/call" ? (message.params?.name ?? null) : undefined,
  });

  if (message.id === undefined || message.id === null) {
    return;
  }

  if (method === "initialize") {
    result(message.id, {
      protocolVersion: message.params?.protocolVersion ?? "2025-06-18",
      capabilities: { tools: { listChanged: false } },
      serverInfo: { name: "director-session-scope-probe", version: "1.0.0" },
    });
    append({ event: "responded", method, requestId: message.id });
    return;
  }

  if (method === "ping") {
    result(message.id, {});
    append({ event: "responded", method, requestId: message.id });
    return;
  }

  if (method === "tools/list") {
    result(message.id, {
      tools: [
        {
          name: toolName,
          description: "Return the non-secret nonce for this isolated Director M0.3 session.",
          inputSchema: { type: "object", properties: {}, additionalProperties: false },
        },
      ],
    });
    append({
      event: "responded",
      method,
      requestId: message.id,
      advertisedTools: [toolName],
    });
    return;
  }

  if (method === "tools/call") {
    if (message.params?.name !== toolName) {
      error(message.id, -32602, "Tool is outside the session scope");
      append({
        event: "rejected_tool_call",
        method,
        requestId: message.id,
        toolName: message.params?.name ?? null,
      });
      return;
    }

    const text = `DIRECTOR_SCOPE_OK row=${row} nonce=${nonce} tools=${toolName}`;
    result(message.id, { content: [{ type: "text", text }], isError: false });
    append({ event: "called", method, requestId: message.id, toolName, resultText: text });
    return;
  }

  error(message.id, -32601, `Unsupported method: ${method ?? "unknown"}`);
  append({ event: "rejected_method", method, requestId: message.id });
});

lines.on("close", () => {
  append({ event: "process_stdin_closed" });
});
