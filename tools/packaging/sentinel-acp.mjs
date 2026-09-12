#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0

import { randomUUID } from "node:crypto";
import { writeFileSync } from "node:fs";
import { createInterface } from "node:readline";

if (process.argv.includes("--version")) {
  process.stdout.write("director-sentinel-acp 1.0.0\n");
  process.exit(0);
}

const sessions = new Set();
const prompts = new Map();

function send(message) {
  process.stdout.write(`${JSON.stringify(message)}\n`);
}

function result(id, value) {
  send({ jsonrpc: "2.0", id, result: value });
}

function error(id, code, message) {
  send({ jsonrpc: "2.0", id, error: { code, message } });
}

function promptText(value) {
  if (typeof value === "string") return value;
  if (Array.isArray(value)) return value.map(promptText).join(" ");
  if (value && typeof value === "object") {
    return Object.values(value).map(promptText).join(" ");
  }
  return "";
}

function handle(message) {
  switch (message.method) {
    case "initialize":
      result(message.id, {
        protocolVersion: 1,
        agentCapabilities: { loadSession: false },
        agentInfo: { name: "director-sentinel-acp", version: "1.0.0" },
      });
      return;
    case "authenticate":
      result(message.id, {});
      return;
    case "session/new": {
      const sessionId = randomUUID();
      sessions.add(sessionId);
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
    case "session/prompt": {
      const sessionId = message.params?.sessionId;
      if (!sessions.has(sessionId)) {
        error(message.id, -32602, "Unknown session");
        return;
      }
      const match = /DIRECTOR_SENTINEL\s+(\/[A-Za-z0-9_./-]+)\s+([A-Za-z0-9-]+)/u.exec(
        promptText(message.params?.prompt),
      );
      if (!match) {
        error(message.id, -32602, "Invalid sentinel prompt");
        return;
      }
      writeFileSync(match[1], `${JSON.stringify({ pid: process.pid, token: match[2] })}\n`, {
        encoding: "utf8",
        mode: 0o600,
      });
      prompts.set(sessionId, message.id);
      send({
        jsonrpc: "2.0",
        method: "session/update",
        params: {
          sessionId,
          update: {
            sessionUpdate: "agent_message_chunk",
            content: { type: "text", text: "sentinel-active" },
          },
        },
      });
      return;
    }
    case "session/cancel": {
      const sessionId = message.params?.sessionId;
      const promptId = prompts.get(sessionId);
      if (promptId !== undefined) {
        prompts.delete(sessionId);
        result(promptId, { stopReason: "cancelled" });
      }
      if (message.id !== undefined) result(message.id, {});
      return;
    }
    case "session/set_mode":
    case "session/set_model":
      result(message.id, {});
      return;
    default:
      if (message.id !== undefined && message.id !== null) {
        error(message.id, -32601, "Unsupported method");
      }
  }
}

const lines = createInterface({ input: process.stdin, crlfDelay: Infinity });
lines.on("line", (line) => {
  try {
    handle(JSON.parse(line));
  } catch {
    // The disposable sentinel deliberately exposes no input in diagnostics.
  }
});
