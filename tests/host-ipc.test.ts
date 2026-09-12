// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { chmodSync, existsSync, lstatSync, mkdtempSync, rmSync } from "node:fs";
import { request } from "node:http";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { startHostContractServer } from "../connector/host-ipc.server.ts";
import {
  EXPECTED_HOST_DESCRIPTOR,
  HOST_CONTRACT_SHA256,
  HOST_CONTRACT_VERSION,
  type HostCommand,
} from "../generated/host-contract.shared.ts";

function call(socketPath: string, path: string, method: string, body?: unknown): Promise<{ status: number; body: unknown }> {
  return new Promise((resolve, reject) => {
    const value = body === undefined ? undefined : JSON.stringify(body);
    const outgoing = request({ socketPath, path, method, headers: {
      "x-director-contract-version": HOST_CONTRACT_VERSION,
      "x-director-contract-hash": HOST_CONTRACT_SHA256,
      ...(value === undefined ? {} : { "content-type": "application/json", "content-length": Buffer.byteLength(value) }),
    } }, (incoming) => {
      const chunks: Buffer[] = [];
      incoming.on("data", (chunk) => chunks.push(Buffer.from(chunk)));
      incoming.on("end", () => resolve({ status: incoming.statusCode ?? 0, body: JSON.parse(Buffer.concat(chunks).toString("utf8")) }));
    });
    outgoing.once("error", reject);
    if (value !== undefined) outgoing.write(value);
    outgoing.end();
  });
}

test("owner-only host IPC exposes only descriptor and strict invoke contracts", async () => {
  const root = mkdtempSync(join(tmpdir(), "director-host-ipc-"));
  const socketPath = join(root, "private", "host.sock");
  let invokes = 0;
  const close = await startHostContractServer({
    async describe() { return EXPECTED_HOST_DESCRIPTOR; },
    async invoke(command: HostCommand) {
      invokes++;
      return { requestId: command.requestId, cursor: 1, observedAt: "2026-09-12T19:30:00Z",
        result: { effectId: command.arguments.effectId, status: "absent", bindingHash: command.arguments.bindingHash,
          priorDispatcherAbsent: true, maximumAgeMillis: 30000, factHash: "a".repeat(64) } };
    },
  }, socketPath);
  try {
    const info = lstatSync(socketPath);
    assert.equal(info.isSocket(), true);
    assert.equal(info.mode & 0o777, 0o600);
    assert.deepEqual((await call(socketPath, "/v1/host/describe", "GET")).body, EXPECTED_HOST_DESCRIPTOR);
    const invalid = await call(socketPath, "/v1/host/invoke", "POST", {
      requestId: "request-1", idempotencyKey: "request-1", expectedVersion: 0,
      capability: "agent.observe", arguments: { scope: { projectId: "p", workspaceId: "w", taskId: "t", runId: "r" },
        effectKind: "agent.send_prompt", effectId: "effect-1", bindingHash: "b".repeat(64), policy: "forbidden" },
    });
    assert.equal(invalid.status, 400);
    assert.equal(invokes, 0);
  } finally {
    await close();
    assert.equal(existsSync(socketPath), false);
    rmSync(root, { recursive: true, force: true });
  }
});

test("host IPC recovers only an exact owner-only stale socket and refuses an active listener", async () => {
  const root = mkdtempSync(join(tmpdir(), "director-host-restart-"));
  const privateRoot = join(root, "private");
  const socketPath = join(privateRoot, "host.sock");
  const child = spawn(process.execPath, ["-e", `
    const { createServer } = require("node:net");
    const { chmodSync, mkdirSync } = require("node:fs");
    mkdirSync(require("node:path").dirname(process.argv[1]), { recursive: true, mode: 0o700 });
    const server = createServer();
    server.listen(process.argv[1], () => { chmodSync(process.argv[1], 0o600); process.stdout.write("ready"); });
  `, socketPath], { stdio: ["ignore", "pipe", "pipe"] });
  await once(child.stdout!, "data");
  child.kill("SIGKILL");
  await once(child, "exit");
  assert.equal(lstatSync(socketPath).isSocket(), true);

  const invoker = {
    async describe() { return EXPECTED_HOST_DESCRIPTOR; },
    async invoke() { return {}; },
  };
  const close = await startHostContractServer(invoker, socketPath);
  await assert.rejects(startHostContractServer(invoker, socketPath), /already active/);
  await close();

  const active = createServer();
  await new Promise<void>((resolve, reject) => {
    active.once("error", reject);
    active.listen(socketPath, resolve);
  });
  try {
    chmodSync(socketPath, 0o600);
    await assert.rejects(startHostContractServer(invoker, socketPath), /already active/);
  } finally {
    await new Promise<void>((resolve, reject) => active.close((error) => error ? reject(error) : resolve()));
    rmSync(root, { recursive: true, force: true });
  }
});
