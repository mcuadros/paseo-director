#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0

import { createHash } from "node:crypto";
import { appendFileSync, readFileSync, writeFileSync } from "node:fs";
import http from "node:http";

const [socketPath, modePath, readyPath, logPath, expectedPasswordHash, fixturePath] =
  process.argv.slice(2);
const fixture = JSON.parse(readFileSync(fixturePath, "utf8"));

function send(response, status, value) {
  const body = Buffer.from(typeof value === "string" ? value : JSON.stringify(value));
  response.writeHead(status, {
    "content-length": String(body.length),
    "content-type": "application/json",
  });
  response.end(body);
}

function result(id, structuredContent) {
  return {
    jsonrpc: "2.0",
    id,
    result: { content: [], structuredContent },
  };
}

const server = http.createServer((request, response) => {
  const chunks = [];
  request.on("data", (chunk) => chunks.push(chunk));
  request.on("end", () => {
    const authorization = request.headers.authorization ?? "";
    const password = authorization.startsWith("Bearer ")
      ? authorization.slice("Bearer ".length)
      : "";
    const passwordHash = createHash("sha256").update(password).digest("hex");
    let body = null;
    try {
      body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
    } catch {
      // The lifecycle reader must not send malformed MCP requests.
    }
    appendFileSync(logPath, `${JSON.stringify({
      authorizationHash: passwordHash,
      authorizationPresent: password.length > 0,
      id: body?.id ?? null,
      method: body?.method ?? null,
      path: request.url,
      tool: body?.params?.name ?? null,
    })}\n`);

    const mode = readFileSync(modePath, "utf8").trim();
    if (mode === "response-loss") {
      request.socket.destroy();
      return;
    }
    if (mode === "timeout") return;
    if (mode === "auth-failed" || passwordHash !== expectedPasswordHash) {
      send(response, 401, { error: "Unauthorized" });
      return;
    }
    if (mode === "malformed-json") {
      send(response, 200, "{not-json");
      return;
    }
    if (mode === "echo-secret") {
      send(response, 200, result(body?.id, { echoed: password }));
      return;
    }
    if (mode === "malformed-output") {
      send(response, 200, result(body?.id, { unexpected: true }));
      return;
    }

    if (body?.method !== "tools/call") {
      send(response, 400, { error: "invalid request" });
      return;
    }
    if (body.params?.name === "get_agent_status") {
      const parent = mode === "parented" ? "parent-agent-0001" : undefined;
      const agentId = mode === "wrong-agent"
        ? "agent-auth-wrong"
        : fixture.agentId;
      send(response, 200, result(body.id, {
        status: "idle",
        snapshot: {
          id: agentId,
          title: fixture.title,
          cwd: fixture.checkout,
          status: "idle",
          archivedAt: null,
          labels: parent === undefined
            ? {}
            : { "paseo.parent-agent-id": parent },
        },
      }));
      return;
    }
    if (body.params?.name === "list_workspaces") {
      send(response, 200, result(body.id, {
        workspaces: [{
          workspaceId: mode === "wrong-workspace"
            ? "workspace-auth-wrong"
            : fixture.workspaceId,
          projectId: "project-auth-0001",
          cwd: fixture.checkout,
          isolation: "worktree",
          kind: "worktree",
          title: fixture.title,
        }],
      }));
      return;
    }
    send(response, 400, { error: "unknown tool" });
  });
});

server.listen(socketPath, () => writeFileSync(readyPath, "ready\n"));

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => server.close(() => process.exit(0)));
}
