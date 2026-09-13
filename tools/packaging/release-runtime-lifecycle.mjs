#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  chmodSync,
  closeSync,
  constants,
  copyFileSync,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  openSync,
  readFileSync,
  readdirSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { createConnection, createServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, isAbsolute, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import {
  EXPECTED_HOST_DESCRIPTOR,
} from "../../generated/host-contract.shared.ts";
import {
  PLANNING_CONTRACT_SHA256,
  PLANNING_CONTRACT_VERSION,
} from "../../generated/planning-contract.shared.ts";
import { startHostContractServer } from "../../connector/host-ipc.server.ts";
import {
  ensureRuntimeSupervisor,
  runtimeSupervisorPaths,
} from "../../connector/runtime-supervisor.server.ts";
import { buildRelease } from "../release/build-engine.mjs";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");

function sha256(path) {
  return createHash("sha256").update(readFileSync(path)).digest("hex");
}

function command(executable, args, options = {}) {
  const result = spawnSync(executable, args, {
    cwd: options.cwd,
    encoding: "utf8",
    env: options.env ?? process.env,
    shell: false,
    timeout: options.timeout ?? 120_000,
    maxBuffer: 1024 * 1024,
  });
  if (result.status !== 0 || result.error) throw new Error("release-runtime fixture command failed");
  return result.stdout.trim();
}

function executablePath(name) {
  const result = command("which", [name]);
  if (!isAbsolute(result)) throw new Error("release-runtime fixture executable is unavailable");
  return result;
}

async function portListening(port) {
  return new Promise((accept) => {
    const socket = createConnection({ host: "127.0.0.1", port });
    const finish = (value) => { socket.destroy(); accept(value); };
    socket.setTimeout(250, () => finish(false));
    socket.once("connect", () => finish(true));
    socket.once("error", () => finish(false));
  });
}

async function reserveLoopbackPort() {
  const server = createServer();
  server.unref();
  await new Promise((accept, reject) => {
    server.once("error", reject);
    server.listen({ host: "127.0.0.1", port: 0, exclusive: true }, accept);
  });
  const address = server.address();
  if (address === null || typeof address === "string") throw new Error("release-runtime fixture port is unavailable");
  await new Promise((accept, reject) => server.close((error) => error ? reject(error) : accept()));
  return address.port;
}

async function waitFor(predicate, timeout = 45_000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (await predicate()) return;
    await new Promise((accept) => setTimeout(accept, 100));
  }
  throw new Error("release-runtime fixture timed out");
}

function secretAbsentFromArguments(secrets) {
  for (const entry of readdirSync("/proc", { withFileTypes: true })) {
    if (!entry.isDirectory() || !/^[0-9]+$/u.test(entry.name)) continue;
    try {
      const commandLine = readFileSync(join("/proc", entry.name, "cmdline"));
      if (secrets.some((secret) => commandLine.includes(Buffer.from(secret)))) return false;
    } catch {
      // Processes may exit during the bounded observation.
    }
  }
  return true;
}

function writeEvidence(path, result) {
  if (!isAbsolute(path) || existsSync(path)) throw new Error("evidence path must be a new absolute path");
  const descriptor = openSync(path, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600);
  try {
    writeFileSync(descriptor, `${JSON.stringify(result, null, 2)}\n`);
  } finally {
    closeSync(descriptor);
  }
}

export async function runReleaseRuntimeLifecycle() {
  if (process.platform !== "linux" || process.arch !== "x64") throw new Error("release-runtime fixture requires linux-amd64");
  if (command("git", ["status", "--porcelain"], { cwd: repositoryRoot }) !== "") {
    throw new Error("release-runtime evidence requires a clean exact Candidate");
  }
  const ports = { dolt: await reserveLoopbackPort(), engine: await reserveLoopbackPort() };
  assert.notEqual(ports.dolt, ports.engine);

  const root = mkdtempSync(join(tmpdir(), "director-release-runtime-"));
  const releaseRoot = join(root, "release", "1.0.0-alpha.0");
  const notices = join(root, "THIRD_PARTY_NOTICES.txt");
  const doltArchive = join(root, "dolt-linux-amd64.tar.gz");
  const doltBinary = join(root, "artifacts", "dolt");
  const environment = {
    ...process.env,
    XDG_RUNTIME_DIR: join(root, "runtime"),
    XDG_CACHE_HOME: join(root, "cache"),
    XDG_CONFIG_HOME: join(root, "config"),
    XDG_DATA_HOME: join(root, "data"),
  };
  for (const path of [environment.XDG_RUNTIME_DIR, environment.XDG_CACHE_HOME, environment.XDG_CONFIG_HOME, environment.XDG_DATA_HOME, dirname(doltBinary)]) {
    mkdirSync(path, { recursive: true, mode: 0o700 });
    chmodSync(path, 0o700);
  }
  const candidate = command("git", ["rev-parse", "HEAD"], { cwd: repositoryRoot });
  writeFileSync(notices, `Director runtime lifecycle\nsource-candidate: ${candidate}\n`, { mode: 0o600 });
  writeFileSync(doltArchive, "canonical Dolt archive identity fixture\n", { mode: 0o600 });
  copyFileSync(executablePath("dolt"), doltBinary);
  chmodSync(doltBinary, 0o500);
  const version = command(doltBinary, ["version"], { env: { DOLT_DISABLE_VERSION_CHECK: "1" } })
    .split("\n")[0]
    .replace(/^dolt version /u, "");
  assert.equal(version, "2.3.2");

  const built = buildRelease([
    "--source", join(repositoryRoot, "engine"),
    "--candidate", candidate,
    "--version", "1.0.0-alpha.0",
    "--notices", notices,
    "--dolt-version", version,
    "--dolt-archive", doltArchive,
    "--dolt-executable", doltBinary,
    "--output", releaseRoot,
  ]);
  const engine = {
    mode: "release",
    version: built.metadata.version,
    sourceCandidate: built.metadata.sourceCandidate,
    target: "linux-amd64",
    binaryPath: join(releaseRoot, "director-engine-linux-amd64"),
    noticesPath: join(releaseRoot, "THIRD_PARTY_NOTICES.txt"),
    binarySha256: built.metadata.binary.sha256,
    noticesSha256: built.metadata.notices.sha256,
    connectorCommit: candidate,
    contractVersion: built.identity.contractVersion,
    contractSha256: built.identity.contractSha256,
  };
  const dolt = {
    version,
    target: "linux-amd64",
    binaryPath: doltBinary,
    binarySha256: sha256(doltBinary),
    archiveSha256: sha256(doltArchive),
  };
  const supervisorPaths = runtimeSupervisorPaths(environment);
  const hostSocket = join(supervisorPaths.root, "host.sock");
  mkdirSync(dirname(hostSocket), { recursive: true, mode: 0o700 });
  const stopHost = await startHostContractServer({
    async describe() { return EXPECTED_HOST_DESCRIPTOR; },
    async invoke(commandValue) {
      return {
        schemaVersion: 1,
        requestId: commandValue.requestId,
        cursor: 1,
        result: {
          effectId: commandValue.arguments.effectId,
          status: "unavailable",
          bindingHash: commandValue.arguments.bindingHash,
          priorDispatcherAbsent: true,
          maximumAgeMillis: 30_000,
          factHash: "0".repeat(64),
        },
      };
    },
  }, hostSocket);
  let first;
  let second;
  try {
    first = await ensureRuntimeSupervisor({ pluginRoot: repositoryRoot, environment, engine, dolt, hostSocket, ports });
    const firstStatus = await first.status();
    assert.equal(firstStatus.state, "current");
    assert.ok(firstStatus.enginePid && firstStatus.doltPid);
    const initialSupervisor = JSON.parse(readFileSync(supervisorPaths.state, "utf8"));

    second = await ensureRuntimeSupervisor({ pluginRoot: repositoryRoot, environment, engine, dolt, hostSocket, ports });
    const secondStatus = await second.status();
    const adoptedSupervisor = JSON.parse(readFileSync(supervisorPaths.state, "utf8"));
    assert.equal(adoptedSupervisor.pid, initialSupervisor.pid);
    assert.equal(secondStatus.enginePid, firstStatus.enginePid);
    assert.equal(secondStatus.doltPid, firstStatus.doltPid);

    const response = await fetch(`http://127.0.0.1:${ports.engine}/v1/planning/home`, {
      method: "POST",
      headers: {
        "content-type": "application/json",
        "x-director-contract-version": PLANNING_CONTRACT_VERSION,
        "x-director-contract-hash": PLANNING_CONTRACT_SHA256,
      },
      body: JSON.stringify({ hostId: "local-paseo", pageSize: 25 }),
    });
    assert.equal(response.status, 200);
    const home = await response.json();
    assert.equal(home.page.totalProjects, "0");
    assert.equal(home.cursor, "0");

    const credentialRoot = join(environment.XDG_CONFIG_HOME, "director", "managed-runtime", "credentials");
    const secrets = ["control", "writer", "maintenance"].map((name) => readFileSync(join(credentialRoot, `${name}.password`), "utf8").trim());
    assert.equal(secretAbsentFromArguments(secrets), true);
    await first.close();
    await second.close();
    await waitFor(() => !existsSync(supervisorPaths.state), 45_000);
    assert.equal(await portListening(ports.dolt), false);
    assert.equal(await portListening(ports.engine), false);
    assert.equal(existsSync(join(environment.XDG_DATA_HOME, "director", "taskstore", "dolt", "director", ".dolt")), true);
    return {
      schemaVersion: 1,
      sourceCandidate: candidate,
      sourceTree: command("git", ["rev-parse", "HEAD^{tree}"], { cwd: repositoryRoot }),
      engineVersion: engine.version,
      engineSha256: engine.binarySha256,
      doltVersion: dolt.version,
      doltSha256: dolt.binarySha256,
      supervisorAdopted: true,
      engineProcessPreserved: true,
      doltProcessPreserved: true,
      homeStatus: 200,
      emptyCursor: "0",
      pluginCompilerExecutions: 0,
      systemServiceCommands: 0,
      secretFreeArguments: true,
      leaseExpiryStoppedChildren: true,
      persistentDataPreserved: true,
    };
  } finally {
    await first?.release().catch(() => undefined);
    await second?.release().catch(() => undefined);
    await waitFor(async () => !(await portListening(ports.dolt)) && !(await portListening(ports.engine)), 15_000).catch(() => undefined);
    await stopHost().catch(() => undefined);
    rmSync(root, { recursive: true, force: true });
  }
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))) {
  try {
    const result = await runReleaseRuntimeLifecycle();
    if (process.argv.length === 4 && process.argv[2] === "--evidence") {
      writeEvidence(resolve(process.argv[3]), result);
    } else if (process.argv.length !== 2) {
      throw new Error("usage: release-runtime-lifecycle.mjs [--evidence <absolute-new-path>]");
    }
    process.stdout.write(`${JSON.stringify(result)}\n`);
  } catch (error) {
    const code = typeof error?.code === "string" && /^[A-Z0-9_]{3,96}$/u.test(error.code)
      ? error.code
      : "DIRECTOR_RELEASE_RUNTIME_FAILED";
    process.stderr.write(`${code}: ${error instanceof Error ? error.message : "unknown failure"}\n`);
    process.exitCode = 1;
  }
}
