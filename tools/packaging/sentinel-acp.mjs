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
      const text = promptText(message.params?.prompt);
      if (text.startsWith("Director Project administration MCP is connected")) {
        result(message.id, { stopReason: "end_turn" });
        return;
      }
      const match = /DIRECTOR_SENTINEL\s+(\/[A-Za-z0-9_./-]+)\s+([A-Za-z0-9-]+)/u.exec(
        text,
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
    case "session/set_config_option":
      result(message.id, { configOptions: [] });
      return;
    case "config/read":
      result(message.id, { config: {} });
      return;
    case "collaborationMode/list":
    case "skills/list":
      result(message.id, { data: [] });
      return;
    case "model/list":
      result(message.id, {
        data: [{ id: "fixture", isDefault: true, defaultReasoningEffort: "medium" }],
      });
      return;
    case "thread/start": {
      const threadId = randomUUID();
      sessions.add(threadId);
      result(message.id, { thread: { id: threadId }, sandbox: { type: "readOnly" } });
      return;
    }
    case "thread/loaded/list":
      result(message.id, { data: [] });
      return;
    case "thread/resume": {
      const threadId = message.params?.threadId;
      if (typeof threadId !== "string" || threadId.length === 0) {
        error(message.id, -32602, "Unknown thread");
        return;
      }
      sessions.add(threadId);
      result(message.id, { thread: { id: threadId }, sandbox: { type: "readOnly" } });
      return;
    }
    case "turn/start": {
      const threadId = message.params?.threadId;
      if (!sessions.has(threadId)) {
        error(message.id, -32602, "Unknown thread");
        return;
      }
      const turnId = randomUUID();
      const text = promptText(message.params?.input);
      const match = /DIRECTOR_SENTINEL\s+(\/[A-Za-z0-9_./-]+)\s+([A-Za-z0-9-]+)/u.exec(
        text,
      );
      if (match) {
        writeFileSync(match[1], `${JSON.stringify({ pid: process.pid, token: match[2] })}\n`, {
          encoding: "utf8",
          mode: 0o600,
        });
      }
      result(message.id, { turn: { id: turnId } });
      send({
        jsonrpc: "2.0",
        method: "turn/started",
        params: { threadId, turn: { id: turnId, status: "inProgress" } },
      });
      return;
    }
    case "turn/interrupt":
    case "thread/archive":
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
