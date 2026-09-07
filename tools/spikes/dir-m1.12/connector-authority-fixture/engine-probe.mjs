#!/usr/bin/env node

import {
  appendFileSync,
  existsSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { createServer } from "node:net";
import { join } from "node:path";

const runtimeRoot = process.env.DIRECTOR_M112_AUTH_RUNTIME_ROOT;
if (!runtimeRoot) throw new Error("DIRECTOR_M112_AUTH_RUNTIME_ROOT is required");

const eventsPath = join(runtimeRoot, "events.jsonl");
const pidPath = join(runtimeRoot, "engine.pid");
const socketPath = join(runtimeRoot, "engine.sock");
const clients = new Set();
let exitTimer;

function event(type, detail = {}) {
  appendFileSync(
    eventsPath,
    `${JSON.stringify({ type, pid: process.pid, ...detail })}\n`,
    { encoding: "utf8" },
  );
}

function scheduleExit() {
  clearTimeout(exitTimer);
  exitTimer = setTimeout(() => {
    if (clients.size !== 0) return;
    event("engine-exit-no-connector");
    server.close(() => process.exit(0));
  }, 10000);
}

if (existsSync(socketPath)) rmSync(socketPath);
writeFileSync(pidPath, `${process.pid}\n`, { mode: 0o600 });
const credentialEnvKeys = Object.keys(process.env).filter((key) =>
  /(?:PASEO.*(?:PASSWORD|AUTH)|DIRECTOR_M112_AUTH_PASEO)/u.test(key),
);
event("engine-start", { credentialEnvKeys });

const server = createServer((socket) => {
  clearTimeout(exitTimer);
  clients.add(socket);
  event("connector-attached", { connectors: clients.size });
  socket.on("data", (chunk) => {
    for (const line of chunk.toString("utf8").split("\n").filter(Boolean)) {
      const message = JSON.parse(line);
      if (message.type === "connector-describe") {
        event("connector-described", { descriptor: message });
      }
    }
  });
  socket.on("close", () => {
    clients.delete(socket);
    event("connector-detached", { connectors: clients.size });
    if (clients.size === 0) scheduleExit();
  });
});

server.listen(socketPath, () => scheduleExit());

function stop(signal) {
  event("engine-stop", { signal });
  clearTimeout(exitTimer);
  for (const socket of clients) socket.destroy();
  server.close(() => process.exit(0));
}

process.on("SIGINT", () => stop("SIGINT"));
process.on("SIGTERM", () => stop("SIGTERM"));
