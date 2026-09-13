#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Linux release-runtime supervisor; it receives no secrets or private paths in argv.

import { spawn, spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import {
  chmodSync,
  closeSync,
  constants,
  existsSync,
  fsyncSync,
  lstatSync,
  mkdirSync,
  openSync,
  readFileSync,
  renameSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { createConnection, createServer } from "node:net";
import { dirname, isAbsolute, join, resolve } from "node:path";

const MAXIMUM_MESSAGE_BYTES = 64 * 1024;
const MAXIMUM_RESTARTS = 3;
let configuration = null;
let token = "";
let control = null;
let dolt = null;
let engine = null;
let stopping = false;
let status = "degraded";
let restartCount = 0;
let leaseDeadline = 0;
let restartTimer = null;

function fail(code) {
  const error = new Error(code);
  error.code = code;
  throw error;
}

function processStartTime(pid) {
  try {
    const stat = readFileSync(`/proc/${pid}/stat`, "utf8");
    return stat.slice(stat.lastIndexOf(")") + 2).split(" ")[19] ?? "";
  } catch {
    return "";
  }
}

function ensurePrivateDirectory(path) {
  mkdirSync(path, { recursive: true, mode: 0o700 });
  const state = lstatSync(path);
  const uid = process.geteuid?.();
  if (!state.isDirectory() || state.isSymbolicLink() || (state.mode & 0o077) !== 0 || (uid !== undefined && state.uid !== uid)) {
    fail("DIRECTOR_RUNTIME_PERMISSIONS");
  }
}

function writePrivate(path, bytes, mode = 0o600) {
  const temporary = `${path}.partial-${process.pid}`;
  const descriptor = openSync(
    temporary,
    constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW,
    mode,
  );
  try {
    writeFileSync(descriptor, bytes);
    fsyncSync(descriptor);
  } finally {
    closeSync(descriptor);
  }
  chmodSync(temporary, mode);
  renameSync(temporary, path);
  const directory = openSync(dirname(path), constants.O_RDONLY | constants.O_DIRECTORY | constants.O_NOFOLLOW);
  try {
    fsyncSync(directory);
  } finally {
    closeSync(directory);
  }
}

function privateSecret(path) {
  if (!existsSync(path)) writePrivate(path, randomBytes(32).toString("hex"), 0o600);
  const state = lstatSync(path);
  const uid = process.geteuid?.();
  if (!state.isFile() || state.isSymbolicLink() || state.nlink !== 1 || (state.mode & 0o077) !== 0 || (uid !== undefined && state.uid !== uid)) {
    fail("DIRECTOR_RUNTIME_CREDENTIAL_INVALID");
  }
  const value = readFileSync(path, "utf8").trim();
  if (!/^[0-9a-f]{64}$/u.test(value)) fail("DIRECTOR_RUNTIME_CREDENTIAL_INVALID");
  return value;
}

function boundedSpawn(executable, args, options = {}) {
  const result = spawnSync(executable, args, {
    cwd: options.cwd,
    encoding: "utf8",
    env: options.env ?? {},
    shell: false,
    timeout: options.timeout ?? 120_000,
    maxBuffer: 128 * 1024,
  });
  if (result.status !== 0 || result.error) {
    const stderr = typeof result.stderr === "string" ? result.stderr : "";
    if (/(?:^|\s)DIRECTOR_TASKSTORE_CONFIG_OUTPUT_MISMATCH:/u.test(stderr)) {
      fail("DIRECTOR_TASKSTORE_CONFIG_OUTPUT_MISMATCH");
    }
    fail(options.code ?? "DIRECTOR_RUNTIME_COMMAND_FAILED");
  }
}

function childEnvironment() {
  return {
    ...(process.env.HOME ? { HOME: process.env.HOME } : {}),
    ...(process.env.XDG_RUNTIME_DIR ? { XDG_RUNTIME_DIR: process.env.XDG_RUNTIME_DIR } : {}),
    ...(process.env.XDG_CACHE_HOME ? { XDG_CACHE_HOME: process.env.XDG_CACHE_HOME } : {}),
    ...(process.env.XDG_CONFIG_HOME ? { XDG_CONFIG_HOME: process.env.XDG_CONFIG_HOME } : {}),
    ...(process.env.XDG_DATA_HOME ? { XDG_DATA_HOME: process.env.XDG_DATA_HOME } : {}),
    DOLT_DISABLE_VERSION_CHECK: "1",
    ...(process.env.PATH ? { PATH: process.env.PATH } : {}),
  };
}

async function portAvailable(port) {
  return new Promise((accept) => {
    const socket = createConnection({ host: "127.0.0.1", port });
    const finish = (value) => {
      socket.destroy();
      accept(value);
    };
    socket.setTimeout(250, () => finish(false));
    socket.once("connect", () => finish(true));
    socket.once("error", () => finish(false));
  });
}

async function waitForPort(port, child) {
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    if (child.exitCode !== null || child.signalCode !== null) fail("DIRECTOR_RUNTIME_CHILD_EXITED");
    if (await portAvailable(port)) return;
    await new Promise((accept) => setTimeout(accept, 100));
  }
  fail("DIRECTOR_RUNTIME_CHILD_TIMEOUT");
}

function spawnOwned(executable, args, cwd) {
  return spawn(executable, args, {
    cwd,
    detached: false,
    env: childEnvironment(),
    shell: false,
    stdio: "ignore",
  });
}

function prepareStorePaths() {
  const dataRoot = resolve(configuration.paths.data, "taskstore");
  const doltRoot = join(dataRoot, "dolt");
  const database = join(doltRoot, "director");
  const doltConfig = join(dataRoot, "doltcfg");
  const credentials = join(configuration.paths.config, "credentials");
  const socketRoot = join(configuration.paths.root, "dolt");
  for (const path of [dataRoot, doltRoot, database, doltConfig, credentials, socketRoot]) {
    ensurePrivateDirectory(path);
  }
  return {
    dataRoot,
    doltRoot,
    database,
    doltConfig,
    credentials,
    socket: join(socketRoot, "mysql.sock"),
    taskstore: join(configuration.paths.config, "taskstore.json"),
    privilegeFile: join(doltConfig, "privileges.db"),
  };
}

function initializeDatabase(paths) {
  if (existsSync(join(paths.database, ".dolt"))) return;
  boundedSpawn(configuration.dolt.binaryPath, [
    "init",
    "--name", "Director",
    "--email", "director@localhost.invalid",
  ], { cwd: paths.database, env: childEnvironment(), code: "DIRECTOR_TASKSTORE_INIT_FAILED" });
}

function bootstrapStore(paths) {
  const control = join(paths.credentials, "control.password");
  const writer = join(paths.credentials, "writer.password");
  const maintenance = join(paths.credentials, "maintenance.password");
  privateSecret(control);
  privateSecret(writer);
  privateSecret(maintenance);
  if (existsSync(paths.taskstore)) {
    boundedSpawn(configuration.engine.binaryPath, [
      "bootstrap-taskstore",
      "--taskstore-config", paths.taskstore,
    ], { env: childEnvironment(), code: "DIRECTOR_TASKSTORE_BOOTSTRAP_FAILED" });
    return;
  }
  boundedSpawn(configuration.engine.binaryPath, [
    "bootstrap-taskstore",
    "--config-output", paths.taskstore,
    "--address", `127.0.0.1:${configuration.ports.dolt}`,
    "--database", "director",
    "--store-id", "director-local",
    "--owner-user", "root",
    "--control-user", "director_control",
    "--writer-user", "director_writer",
    "--maintenance-user", "director_maintenance",
    "--control-password-file", control,
    "--writer-password-file", writer,
    "--maintenance-password-file", maintenance,
    "--privilege-file", paths.privilegeFile,
  ], { env: childEnvironment(), code: "DIRECTOR_TASKSTORE_BOOTSTRAP_FAILED" });
}

function watchChild(child) {
  child.once("exit", () => {
    if (stopping) return;
    status = "degraded";
    writeState();
    queueChildRecovery();
  });
}

async function startRuntime() {
  if (dolt && engine && dolt.exitCode === null && engine.exitCode === null) return;
  const paths = prepareStorePaths();
  if (!dolt || dolt.exitCode !== null || dolt.signalCode !== null) {
    if (await portAvailable(configuration.ports.dolt)) fail("DIRECTOR_RUNTIME_EXTERNAL_OWNER");
    initializeDatabase(paths);
    dolt = spawnOwned(configuration.dolt.binaryPath, [
      "sql-server",
      "--host=127.0.0.1",
      `--port=${configuration.ports.dolt}`,
      `--data-dir=${paths.doltRoot}`,
      `--doltcfg-dir=${paths.doltConfig}`,
      `--socket=${paths.socket}`,
      "--loglevel=warning",
    ], paths.database);
    watchChild(dolt);
    await waitForPort(configuration.ports.dolt, dolt);
  }
  bootstrapStore(paths);
  if (!engine || engine.exitCode !== null || engine.signalCode !== null) {
    if (await portAvailable(configuration.ports.engine)) fail("DIRECTOR_RUNTIME_EXTERNAL_OWNER");
    engine = spawnOwned(configuration.engine.binaryPath, [
      "serve-board",
      `--listen=127.0.0.1:${configuration.ports.engine}`,
      `--taskstore-config=${paths.taskstore}`,
      "--host-id=local-paseo",
      "--host-label=Local Paseo",
      `--host-socket=${configuration.hostSocket}`,
      `--runtime-root=${join(configuration.paths.root, "work")}`,
    ], configuration.paths.data);
    watchChild(engine);
    await waitForPort(configuration.ports.engine, engine);
  }
  status = "current";
  restartCount = 0;
  writeState();
}

function queueChildRecovery() {
  if (stopping || restartTimer || restartCount >= MAXIMUM_RESTARTS) return;
  restartCount += 1;
  restartTimer = setTimeout(() => {
    restartTimer = null;
    void startRuntime().catch(() => {
      status = "degraded";
      writeState();
      queueChildRecovery();
    });
  }, Math.min(4_000, 500 * 2 ** (restartCount - 1)));
  restartTimer.unref();
}

function stateDocument() {
  return {
    schemaVersion: 1,
    binding: configuration.binding,
    pid: process.pid,
    processStart: processStartTime(process.pid),
    status,
    enginePid: engine?.exitCode === null ? engine.pid ?? null : null,
    engineProcessStart: engine?.exitCode === null && engine.pid ? processStartTime(engine.pid) : null,
    doltPid: dolt?.exitCode === null ? dolt.pid ?? null : null,
    doltProcessStart: dolt?.exitCode === null && dolt.pid ? processStartTime(dolt.pid) : null,
  };
}

function writeState() {
  if (!configuration) return;
  writePrivate(configuration.paths.state, `${JSON.stringify(stateDocument())}\n`, 0o600);
}

function statusDocument() {
  return {
    schemaVersion: 1,
    state: status,
    binding: configuration.binding,
    enginePid: engine?.exitCode === null ? engine.pid ?? null : null,
    doltPid: dolt?.exitCode === null ? dolt.pid ?? null : null,
    restartCount,
  };
}

function startControl() {
  if (existsSync(configuration.paths.socket)) fail("DIRECTOR_RUNTIME_CONTROL_OWNERSHIP");
  control = createServer((socket) => {
    socket.setEncoding("utf8");
    let input = "";
    socket.on("data", (chunk) => {
      input = `${input}${chunk}`;
      if (input.length > MAXIMUM_MESSAGE_BYTES) socket.destroy();
    });
    socket.on("end", () => {
      try {
        const message = JSON.parse(input);
        if (
          message?.schemaVersion !== 1 || message?.token !== token ||
          !["ensure", "status", "release"].includes(message?.command)
        ) {
          socket.end(`${JSON.stringify({ schemaVersion: 1, code: "DIRECTOR_RUNTIME_CONTROL_REFUSED" })}\n`);
          return;
        }
        if (message.command === "ensure") {
          leaseDeadline = Date.now() + configuration.leaseMillis;
          if (status === "degraded") queueChildRecovery();
        }
        socket.end(`${JSON.stringify(statusDocument())}\n`);
        if (message.command === "release") {
          leaseDeadline = Date.now();
          setTimeout(() => void shutdown(), 25).unref();
        }
      } catch {
        socket.end(`${JSON.stringify({ schemaVersion: 1, code: "DIRECTOR_RUNTIME_CONTROL_INVALID" })}\n`);
      }
    });
  });
  return new Promise((accept, reject) => {
    control.once("error", reject);
    control.listen(configuration.paths.socket, () => {
      control.removeListener("error", reject);
      chmodSync(configuration.paths.socket, 0o600);
      accept();
    });
  });
}

async function terminateChild(child) {
  if (!child || child.exitCode !== null || child.signalCode !== null) return;
  const closed = new Promise((accept) => child.once("exit", accept));
  child.kill("SIGTERM");
  await Promise.race([closed, new Promise((accept) => setTimeout(accept, 5_000))]);
  if (child.exitCode === null && child.signalCode === null) {
    child.kill("SIGKILL");
    await closed;
  }
}

async function shutdown() {
  if (stopping) return;
  stopping = true;
  if (restartTimer) clearTimeout(restartTimer);
  await terminateChild(engine);
  await terminateChild(dolt);
  if (control) await new Promise((accept) => control.close(() => accept()));
  for (const path of [configuration?.paths.socket, configuration?.paths.state, configuration?.paths.token]) {
    if (path && existsSync(path)) rmSync(path, { force: false });
  }
  process.exit(0);
}

function validInitialization(message) {
  const paths = message?.paths;
  return message?.type === "initialize" && message?.schemaVersion === 1 &&
    /^[0-9a-f]{64}$/u.test(message?.binding ?? "") &&
    Number.isSafeInteger(message?.leaseMillis) && message.leaseMillis === 30_000 &&
    Number.isSafeInteger(message?.ports?.dolt) && message.ports.dolt > 0 && message.ports.dolt <= 65_535 &&
    Number.isSafeInteger(message?.ports?.engine) && message.ports.engine > 0 && message.ports.engine <= 65_535 &&
    message.ports.dolt !== message.ports.engine &&
    paths && Object.values(paths).every((path) => typeof path === "string" && isAbsolute(path)) &&
    typeof message?.hostSocket === "string" && isAbsolute(message.hostSocket) &&
    typeof message?.engine?.binaryPath === "string" && isAbsolute(message.engine.binaryPath) &&
    typeof message?.dolt?.binaryPath === "string" && isAbsolute(message.dolt.binaryPath);
}

process.once("message", (message) => {
  void (async () => {
    if (!validInitialization(message)) fail("DIRECTOR_RUNTIME_INITIALIZATION_INVALID");
    configuration = message;
    for (const path of [configuration.paths.root, configuration.paths.data, configuration.paths.config]) ensurePrivateDirectory(path);
    token = randomBytes(32).toString("hex");
    writePrivate(configuration.paths.token, token, 0o600);
    await startControl();
    leaseDeadline = Date.now() + configuration.leaseMillis;
    await startRuntime();
    process.send?.({ type: "ready" });
    process.disconnect();
  })().catch(async (error) => {
    const code = typeof error?.code === "string" && /^[A-Z0-9_]{3,96}$/u.test(error.code)
      ? error.code
      : "DIRECTOR_RUNTIME_START_FAILED";
    process.send?.({ type: "failed", code });
    await shutdown();
  });
});

const leaseTimer = setInterval(() => {
  if (configuration && Date.now() > leaseDeadline) void shutdown();
}, 1_000);
leaseTimer.unref();
process.once("SIGTERM", () => void shutdown());
process.once("SIGINT", () => void shutdown());
