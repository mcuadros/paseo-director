// SPDX-License-Identifier: Apache-2.0

import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import {
  chmodSync,
  lstatSync,
  mkdirSync,
  type Stats,
  unlinkSync,
} from "node:fs";
import { createConnection } from "node:net";
import { dirname, isAbsolute, resolve } from "node:path";

import {
  HOST_CAPABILITIES,
  HOST_CONTRACT_SHA256,
  HOST_CONTRACT_VERSION,
  HOST_EFFECT_KINDS,
  type HostCommand,
} from "../generated/host-contract.shared.ts";

const MAXIMUM_HOST_BYTES = 1_048_576;
const argumentKeys = new Set([
  "scope", "effectKind", "effectId", "bindingHash", "worktreeId", "worktreePath",
  "workspaceId", "agentId", "title", "initialPrompt", "parentAgentId", "lifecycleDigest",
  "isolationDigest", "preparationReady", "preparationBarrierHash", "notifyOnFinish",
  "clientMessageId", "boundaryId", "operationalObservationId", "profile", "session",
  "sessionBindingSha256", "labels",
]);

type HostInvoker = {
  describe(): Promise<unknown>;
  invoke(command: HostCommand): Promise<unknown>;
};

function record(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function exactKeys(value: Record<string, unknown>, required: readonly string[], optional: readonly string[] = []): boolean {
  const allowed = new Set([...required, ...optional]);
  return required.every((key) => Object.hasOwn(value, key)) && Object.keys(value).every((key) => allowed.has(key));
}

function command(value: unknown): HostCommand {
  if (!record(value) || !exactKeys(value,
    ["requestId", "idempotencyKey", "expectedVersion", "capability", "arguments"], ["afterCursor"]) ||
    !HOST_CAPABILITIES.includes(value.capability as never) || !record(value.arguments) ||
    !exactKeys(value.arguments, ["scope", "effectKind", "effectId", "bindingHash"], [...argumentKeys]) ||
    !HOST_EFFECT_KINDS.includes(value.arguments.effectKind as never)) {
    throw new Error("HOST_COMMAND_INVALID");
  }
  return value as unknown as HostCommand;
}

async function body(request: IncomingMessage): Promise<unknown> {
  const chunks: Buffer[] = [];
  let length = 0;
  for await (const chunk of request) {
    const bytes = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk as Uint8Array);
    length += bytes.length;
    if (length > MAXIMUM_HOST_BYTES) throw new Error("HOST_PAYLOAD_INVALID");
    chunks.push(bytes);
  }
  if (length === 0) throw new Error("HOST_PAYLOAD_INVALID");
  return JSON.parse(Buffer.concat(chunks).toString("utf8")) as unknown;
}

function headersMatch(request: IncomingMessage): boolean {
  return request.headers["x-director-contract-version"] === HOST_CONTRACT_VERSION &&
    request.headers["x-director-contract-hash"] === HOST_CONTRACT_SHA256;
}

function write(response: ServerResponse, status: number, value: unknown): void {
  const encoded = JSON.stringify(value);
  response.statusCode = status;
  response.setHeader("cache-control", "no-store");
  response.setHeader("content-type", "application/json");
  response.setHeader("x-director-contract-version", HOST_CONTRACT_VERSION);
  response.setHeader("x-director-contract-hash", HOST_CONTRACT_SHA256);
  response.end(encoded.length <= MAXIMUM_HOST_BYTES ? encoded : '{"code":"HOST_OUTPUT_INVALID"}');
}

function effectiveUID(): number {
  const value = process.geteuid?.();
  if (value === undefined) throw new Error("Linux effective user identity is unavailable");
  return value;
}

function privateSocketParent(socketPath: string): Stats | null {
  if (!isAbsolute(socketPath) || resolve(socketPath) !== socketPath) {
    throw new Error("DIRECTOR_HOST_SOCKET must be an absolute clean path");
  }
  const parent = dirname(socketPath);
  mkdirSync(parent, { recursive: true, mode: 0o700 });
  const info = lstatSync(parent);
  if (!info.isDirectory() || info.uid !== effectiveUID() || (info.mode & 0o077) !== 0) {
    throw new Error("DIRECTOR_HOST_SOCKET parent is not owner-only");
  }
  try {
    const existing = lstatSync(socketPath);
    if (!existing.isSocket() || existing.uid !== effectiveUID() || (existing.mode & 0o077) !== 0) {
      throw new Error("DIRECTOR_HOST_SOCKET existing path is unsafe");
    }
    return existing;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
    return null;
  }
}

async function clearStaleSocket(socketPath: string, expected: Stats): Promise<void> {
  await new Promise<void>((resolvePromise, rejectPromise) => {
    const probe = createConnection(socketPath);
    let settled = false;
    const finish = (error?: Error) => {
      if (settled) return;
      settled = true;
      probe.destroy();
      if (error) rejectPromise(error);
      else resolvePromise();
    };
    probe.setTimeout(250, () => finish(new Error("DIRECTOR_HOST_SOCKET liveness is ambiguous")));
    probe.once("connect", () => finish(new Error("DIRECTOR_HOST_SOCKET is already active")));
    probe.once("error", (error: NodeJS.ErrnoException) => {
      if (settled) return;
      if (error.code !== "ECONNREFUSED" && error.code !== "ENOENT") {
        finish(new Error("DIRECTOR_HOST_SOCKET liveness is ambiguous"));
        return;
      }
      try {
        const current = lstatSync(socketPath);
        if (!current.isSocket() || current.uid !== effectiveUID() || current.dev !== expected.dev ||
          current.ino !== expected.ino || (current.mode & 0o077) !== 0) {
          finish(new Error("DIRECTOR_HOST_SOCKET identity changed"));
          return;
        }
        unlinkSync(socketPath);
      } catch (inspectionError) {
        if ((inspectionError as NodeJS.ErrnoException).code !== "ENOENT") {
          finish(new Error("DIRECTOR_HOST_SOCKET cannot be recovered safely"));
          return;
        }
      }
      finish();
    });
  });
}

export async function startHostContractServer(invoker: HostInvoker, socketPath: string): Promise<() => Promise<void>> {
  const existing = privateSocketParent(socketPath);
  if (existing) await clearStaleSocket(socketPath, existing);
  const server = createServer(async (request, response) => {
    try {
      if (!headersMatch(request)) {
        write(response, 409, { code: "HOST_CONTRACT_MISMATCH" });
        return;
      }
      if (request.method === "GET" && request.url === "/v1/host/describe") {
        write(response, 200, await invoker.describe());
        return;
      }
      if (request.method === "POST" && request.url === "/v1/host/invoke" && request.headers["content-type"] === "application/json") {
        write(response, 200, await invoker.invoke(command(await body(request))));
        return;
      }
      write(response, 404, { code: "HOST_OPERATION_NOT_FOUND" });
    } catch {
      write(response, 400, { code: "HOST_REQUEST_REFUSED" });
    }
  });
  return new Promise((accept, reject) => {
    server.once("error", reject);
    server.listen(socketPath, () => {
      server.removeListener("error", reject);
      chmodSync(socketPath, 0o600);
      accept(async () => {
        await new Promise<void>((done, failed) => server.close((error) => error ? failed(error) : done()));
        try {
          const info = lstatSync(socketPath);
          if (!info.isSocket() || info.uid !== effectiveUID()) throw new Error("host socket identity changed");
          unlinkSync(socketPath);
        } catch (error) {
          if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
        }
      });
    });
  });
}
