// SPDX-License-Identifier: Apache-2.0
// Plugin-owned runtime supervisor client: exact adoption, lease renewal, and release-only launch.

import { fork } from "node:child_process";
import { createHash } from "node:crypto";
import {
  chmodSync,
  closeSync,
  constants,
  existsSync,
  lstatSync,
  mkdirSync,
  openSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { connect } from "node:net";
import { homedir } from "node:os";
import { dirname, isAbsolute, join, resolve } from "node:path";

import type { ResolvedDolt } from "./dolt-distribution.server.ts";
import type { ResolvedEngine } from "./engine-distribution.server.ts";
import { runtimePlatform } from "./runtime-platform.server.ts";

const LEASE_MILLIS = 30_000;
const HEARTBEAT_MILLIS = 5_000;
const START_TIMEOUT_MILLIS = 120_000;
const SHA256_PATTERN = /^[0-9a-f]{64}$/u;

type SupervisorState = {
  schemaVersion: 1;
  binding: string;
  pid: number;
  processStart: string;
  status: "current" | "degraded";
  enginePid: number | null;
  engineProcessStart: string | null;
  doltPid: number | null;
  doltProcessStart: string | null;
};

export type RuntimeSupervisorStatus = {
  state: "current" | "degraded";
  binding: string;
  enginePid: number | null;
  doltPid: number | null;
  restartCount: number;
};

export type RuntimeSupervisorHandle = {
  binding: string;
  status(): Promise<RuntimeSupervisorStatus>;
  close(): Promise<void>;
  release(): Promise<void>;
};

export type RuntimeSupervisorDependencies = {
  request?: typeof controlRequest;
  forkProcess?: typeof fork;
};

export class RuntimeSupervisorError extends Error {
  readonly code: string;

  constructor(code: string, message: string) {
    super(message);
    this.name = "RuntimeSupervisorError";
    this.code = code;
  }
}

function sha256(value: string): string {
  return createHash("sha256").update(value).digest("hex");
}

function ensurePrivateDirectory(path: string): void {
  mkdirSync(path, { recursive: true, mode: 0o700 });
  const status = lstatSync(path);
  const uid = process.geteuid?.();
  if (
    !status.isDirectory() || status.isSymbolicLink() ||
    (status.mode & 0o077) !== 0 || (uid !== undefined && status.uid !== uid)
  ) {
    throw new RuntimeSupervisorError("DIRECTOR_RUNTIME_PERMISSIONS", "runtime supervisor directory is not private and owned");
  }
}

function readPrivateFile(path: string, code: string): string {
  const status = lstatSync(path);
  const uid = process.geteuid?.();
  if (
    !status.isFile() || status.isSymbolicLink() || status.nlink !== 1 ||
    (status.mode & 0o077) !== 0 || (uid !== undefined && status.uid !== uid) ||
    status.size <= 0 || status.size > 64 * 1024
  ) {
    throw new RuntimeSupervisorError(code, "runtime supervisor state is unsafe");
  }
  return readFileSync(path, "utf8");
}

export function runtimeSupervisorPaths(environment: NodeJS.ProcessEnv): {
  root: string;
  socket: string;
  state: string;
  token: string;
  lock: string;
  data: string;
  config: string;
} {
  const home = homedir();
  const runtimeBase = environment.XDG_RUNTIME_DIR ?? environment.XDG_CACHE_HOME ?? join(home, ".cache");
  const dataBase = environment.XDG_DATA_HOME ?? join(home, ".local", "share");
  const configBase = environment.XDG_CONFIG_HOME ?? join(home, ".config");
  for (const value of [runtimeBase, dataBase, configBase]) {
    if (!isAbsolute(value)) {
      throw new RuntimeSupervisorError("DIRECTOR_RUNTIME_PATH_BASE", "runtime supervisor base paths must be absolute");
    }
  }
  const root = resolve(runtimeBase, "director", "supervisor");
  const platform = runtimePlatform();
  return {
    root,
    socket: join(root, platform.controlEndpointName),
    state: join(root, "state.json"),
    token: join(root, "control.token"),
    lock: join(root, "launch.lock"),
    data: resolve(dataBase, "director"),
    config: resolve(configBase, "director", "managed-runtime"),
  };
}

function readState(path: string): SupervisorState | null {
  if (!existsSync(path)) return null;
  let value: unknown;
  try {
    value = JSON.parse(readPrivateFile(path, "DIRECTOR_RUNTIME_STATE"));
  } catch (error) {
    if (error instanceof RuntimeSupervisorError) throw error;
    throw new RuntimeSupervisorError("DIRECTOR_RUNTIME_STATE", "runtime supervisor state is invalid");
  }
  if (
    value === null || typeof value !== "object" || Array.isArray(value) ||
    JSON.stringify(Object.keys(value).sort()) !== JSON.stringify([
      "binding", "doltPid", "doltProcessStart", "enginePid", "engineProcessStart",
      "pid", "processStart", "schemaVersion", "status",
    ])
  ) {
    throw new RuntimeSupervisorError("DIRECTOR_RUNTIME_STATE", "runtime supervisor state schema is invalid");
  }
  const state = value as SupervisorState;
  if (
    state.schemaVersion !== 1 || !SHA256_PATTERN.test(state.binding) ||
    !Number.isSafeInteger(state.pid) || state.pid <= 0 ||
    typeof state.processStart !== "string" || state.processStart === "" ||
    (state.status !== "current" && state.status !== "degraded") ||
    ((state.enginePid === null) !== (state.engineProcessStart === null)) ||
    ((state.doltPid === null) !== (state.doltProcessStart === null)) ||
    (state.enginePid !== null && (!Number.isSafeInteger(state.enginePid) || typeof state.engineProcessStart !== "string")) ||
    (state.doltPid !== null && (!Number.isSafeInteger(state.doltPid) || typeof state.doltProcessStart !== "string"))
  ) {
    throw new RuntimeSupervisorError("DIRECTOR_RUNTIME_STATE", "runtime supervisor state identity is invalid");
  }
  return state;
}

async function controlRequest(
  socketPath: string,
  token: string,
  command: "ensure" | "status" | "release",
): Promise<RuntimeSupervisorStatus> {
  return new Promise((accept, reject) => {
    const socket = connect(socketPath);
    let output = "";
    const timer = setTimeout(() => {
      socket.destroy();
      reject(new RuntimeSupervisorError("DIRECTOR_RUNTIME_CONTROL_TIMEOUT", "runtime supervisor control timed out"));
    }, 3_000);
    socket.setEncoding("utf8");
    socket.once("connect", () => socket.end(`${JSON.stringify({ schemaVersion: 1, command, token })}\n`));
    socket.on("data", (chunk) => { output = `${output}${chunk}`.slice(-64 * 1024); });
    socket.once("error", () => {
      clearTimeout(timer);
      reject(new RuntimeSupervisorError("DIRECTOR_RUNTIME_CONTROL_UNAVAILABLE", "runtime supervisor control is unavailable"));
    });
    socket.once("close", () => {
      clearTimeout(timer);
      try {
        const value = JSON.parse(output) as RuntimeSupervisorStatus & { schemaVersion?: number };
        if (
          value.schemaVersion !== 1 || !SHA256_PATTERN.test(value.binding) ||
          (value.state !== "current" && value.state !== "degraded") ||
          !Number.isSafeInteger(value.restartCount) ||
          (value.enginePid !== null && !Number.isSafeInteger(value.enginePid)) ||
          (value.doltPid !== null && !Number.isSafeInteger(value.doltPid))
        ) {
          throw new Error("invalid response");
        }
        accept(value);
      } catch {
        reject(new RuntimeSupervisorError("DIRECTOR_RUNTIME_CONTROL_INVALID", "runtime supervisor control response is invalid"));
      }
    });
  });
}

function acquireLaunchLock(path: string): () => void {
  for (let attempt = 0; attempt < 2; attempt += 1) {
    try {
      const descriptor = openSync(path, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600);
      const marker = `${JSON.stringify({ schemaVersion: 1, pid: process.pid, processStart: runtimePlatform().processIdentity(process.pid) })}\n`;
      writeFileSync(descriptor, marker);
      closeSync(descriptor);
      return () => {
        try {
          rmSync(path, { force: false });
        } catch {
          throw new RuntimeSupervisorError("DIRECTOR_RUNTIME_LOCK_CHANGED", "runtime supervisor launch lock changed");
        }
      };
    } catch (error) {
      if (!existsSync(path) || attempt > 0) {
        throw new RuntimeSupervisorError("DIRECTOR_RUNTIME_LAUNCH_BUSY", "runtime supervisor launch is already in progress");
      }
      let marker: { schemaVersion?: number; pid?: number; processStart?: string };
      try {
        marker = JSON.parse(readPrivateFile(path, "DIRECTOR_RUNTIME_LAUNCH_LOCK"));
      } catch {
        throw new RuntimeSupervisorError("DIRECTOR_RUNTIME_LAUNCH_LOCK", "runtime supervisor launch lock is invalid");
      }
      if (
        marker.schemaVersion !== 1 || !Number.isSafeInteger(marker.pid) ||
        typeof marker.processStart !== "string" || marker.processStart === "" ||
        runtimePlatform().processIdentity(marker.pid!) === marker.processStart
      ) {
        throw new RuntimeSupervisorError("DIRECTOR_RUNTIME_LAUNCH_BUSY", "runtime supervisor launch is already in progress");
      }
      rmSync(path, { force: false });
    }
  }
  throw new RuntimeSupervisorError("DIRECTOR_RUNTIME_LAUNCH_BUSY", "runtime supervisor launch is already in progress");
}

async function waitForProcessExit(pid: number, processStart: string): Promise<void> {
  const deadline = Date.now() + 15_000;
  while (Date.now() < deadline) {
    if (runtimePlatform().processIdentity(pid) !== processStart) return;
    await new Promise((accept) => setTimeout(accept, 100));
  }
  throw new RuntimeSupervisorError("DIRECTOR_RUNTIME_HANDOFF_TIMEOUT", "runtime supervisor did not complete its handoff");
}

function removeStaleControl(paths: ReturnType<typeof runtimeSupervisorPaths>): void {
  for (const path of [paths.socket, paths.state, paths.token]) {
    if (!existsSync(path)) continue;
    const status = lstatSync(path);
    const uid = process.geteuid?.();
    const expectedKind = path === paths.socket ? status.isSocket() : status.isFile();
    if (!expectedKind || status.isSymbolicLink() || (status.mode & 0o077) !== 0 || (uid !== undefined && status.uid !== uid)) {
      throw new RuntimeSupervisorError("DIRECTOR_RUNTIME_RECOVERY_REFUSED", "stale runtime control identity is unsafe");
    }
    rmSync(path, { force: false });
  }
}

async function reclaimOwnedChildren(
  state: SupervisorState,
  enginePath: string,
  doltPath: string,
): Promise<void> {
  const platform = runtimePlatform();
  for (const child of [
    { pid: state.enginePid, start: state.engineProcessStart, executable: enginePath },
    { pid: state.doltPid, start: state.doltProcessStart, executable: doltPath },
  ]) {
    if (child.pid === null || child.start === null) continue;
    if (
      platform.processIdentity(child.pid) !== child.start ||
      platform.processExecutable(child.pid) !== resolve(child.executable)
    ) continue;
    process.kill(child.pid, "SIGTERM");
    const deadline = Date.now() + 5_000;
    while (Date.now() < deadline && platform.processIdentity(child.pid) === child.start) {
      await new Promise((accept) => setTimeout(accept, 100));
    }
    if (
      platform.processIdentity(child.pid) === child.start &&
      platform.processExecutable(child.pid) === resolve(child.executable)
    ) {
      process.kill(child.pid, "SIGKILL");
    }
  }
}

export function runtimeSupervisorBinding(
  engine: ResolvedEngine,
  dolt: ResolvedDolt,
  ports = { dolt: 3307, engine: 7041 },
): string {
  return sha256(JSON.stringify({
    schemaVersion: 1,
    engine: {
      binarySha256: engine.binarySha256,
      contractSha256: engine.contractSha256,
      sourceCandidate: engine.sourceCandidate,
      version: engine.version,
    },
    dolt: {
      binarySha256: dolt.binarySha256,
      version: dolt.version,
    },
    ports,
  }));
}

export async function ensureRuntimeSupervisor(options: {
  pluginRoot: string;
  environment: NodeJS.ProcessEnv;
  engine: ResolvedEngine;
  dolt: ResolvedDolt;
  hostSocket: string;
  ports?: { dolt: number; engine: number };
  dependencies?: RuntimeSupervisorDependencies;
}): Promise<RuntimeSupervisorHandle> {
  const platform = runtimePlatform();
  const paths = runtimeSupervisorPaths(options.environment);
  for (const path of [paths.root, paths.data, paths.config]) ensurePrivateDirectory(path);
  const ports = options.ports ?? { dolt: 3307, engine: 7041 };
  if (
    !Number.isSafeInteger(ports.dolt) || ports.dolt < 1 || ports.dolt > 65_535 ||
    !Number.isSafeInteger(ports.engine) || ports.engine < 1 || ports.engine > 65_535 ||
    ports.dolt === ports.engine
  ) {
    throw new RuntimeSupervisorError("DIRECTOR_RUNTIME_PORTS_INVALID", "runtime supervisor ports are invalid");
  }
  const binding = runtimeSupervisorBinding(options.engine, options.dolt, ports);
  const request = options.dependencies?.request ?? controlRequest;
  let existing = readState(paths.state);
  if (existing !== null) {
    const token = readPrivateFile(paths.token, "DIRECTOR_RUNTIME_CONTROL_TOKEN");
    if (existing.binding !== binding) {
      if (platform.processIdentity(existing.pid) !== existing.processStart) {
        await reclaimOwnedChildren(existing, options.engine.binaryPath, options.dolt.binaryPath);
        removeStaleControl(paths);
        existing = null;
      } else {
        await request(paths.socket, token, "release");
        await waitForProcessExit(existing.pid, existing.processStart);
        removeStaleControl(paths);
        existing = null;
      }
    }
    if (existing !== null) {
      if (platform.processIdentity(existing.pid) !== existing.processStart) {
        await reclaimOwnedChildren(existing, options.engine.binaryPath, options.dolt.binaryPath);
        removeStaleControl(paths);
        existing = null;
      } else {
        await request(paths.socket, token, "ensure");
        return leasedHandle(paths.socket, token, binding, request);
      }
    }
  }

  const releaseLock = acquireLaunchLock(paths.lock);
  try {
    const entry = resolve(options.pluginRoot, "connector", platform.supervisorEntry);
    const child = (options.dependencies?.forkProcess ?? fork)(entry, [], {
      detached: true,
      env: {
        HOME: homedir(),
        ...(options.environment.PATH ? { PATH: options.environment.PATH } : {}),
        ...(options.environment.XDG_RUNTIME_DIR ? { XDG_RUNTIME_DIR: options.environment.XDG_RUNTIME_DIR } : {}),
        ...(options.environment.XDG_CACHE_HOME ? { XDG_CACHE_HOME: options.environment.XDG_CACHE_HOME } : {}),
        ...(options.environment.XDG_CONFIG_HOME ? { XDG_CONFIG_HOME: options.environment.XDG_CONFIG_HOME } : {}),
        ...(options.environment.XDG_DATA_HOME ? { XDG_DATA_HOME: options.environment.XDG_DATA_HOME } : {}),
      },
      execPath: process.execPath,
      execArgv: [],
      serialization: "advanced",
      stdio: ["ignore", "ignore", "ignore", "ipc"],
    });
    const ready = await new Promise<void>((accept, reject) => {
      const timer = setTimeout(() => {
        child.kill("SIGTERM");
        reject(new RuntimeSupervisorError("DIRECTOR_RUNTIME_START_TIMEOUT", "runtime supervisor did not become ready"));
      }, START_TIMEOUT_MILLIS);
      child.once("error", () => {
        clearTimeout(timer);
        reject(new RuntimeSupervisorError("DIRECTOR_RUNTIME_START_FAILED", "runtime supervisor process could not start"));
      });
      child.once("exit", () => {
        clearTimeout(timer);
        reject(new RuntimeSupervisorError("DIRECTOR_RUNTIME_START_FAILED", "runtime supervisor exited before readiness"));
      });
      child.on("message", (message) => {
        if (message && typeof message === "object" && Reflect.get(message, "type") === "ready") {
          clearTimeout(timer);
          accept();
        } else if (message && typeof message === "object" && Reflect.get(message, "type") === "failed") {
          clearTimeout(timer);
          const code = Reflect.get(message, "code");
          reject(new RuntimeSupervisorError(
            typeof code === "string" && /^[A-Z0-9_]{3,96}$/u.test(code) ? code : "DIRECTOR_RUNTIME_START_FAILED",
            "runtime supervisor refused startup",
          ));
        }
      });
      child.send({
        type: "initialize",
        schemaVersion: 1,
        binding,
        leaseMillis: LEASE_MILLIS,
        ports,
        paths,
        hostSocket: options.hostSocket,
        engine: options.engine,
        dolt: options.dolt,
      });
    });
    void ready;
    child.disconnect();
    child.unref();
    const token = readPrivateFile(paths.token, "DIRECTOR_RUNTIME_CONTROL_TOKEN");
    const status = await request(paths.socket, token, "ensure");
    if (status.binding !== binding) {
      throw new RuntimeSupervisorError("DIRECTOR_RUNTIME_BINDING_MISMATCH", "runtime supervisor started with another binding");
    }
    return leasedHandle(paths.socket, token, binding, request);
  } finally {
    releaseLock();
  }
}

function leasedHandle(
  socketPath: string,
  token: string,
  binding: string,
  request: typeof controlRequest,
): RuntimeSupervisorHandle {
  let closed = false;
  const timer = setInterval(() => {
    if (!closed) void request(socketPath, token, "ensure").catch(() => undefined);
  }, HEARTBEAT_MILLIS);
  timer.unref();
  return {
    binding,
    status: () => request(socketPath, token, "status"),
    async close() {
      if (closed) return;
      closed = true;
      clearInterval(timer);
    },
    async release() {
      if (!closed) {
        closed = true;
        clearInterval(timer);
      }
      await request(socketPath, token, "release");
    },
  };
}
