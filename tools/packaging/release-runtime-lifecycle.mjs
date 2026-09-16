#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Focused release-runtime exercise. Distribution preparation is covered by the
// bootstrap tests; this fixture proves the prepared release is owned by Go.

import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  chmodSync, closeSync, constants, copyFileSync, existsSync, mkdirSync, mkdtempSync,
  openSync, readFileSync, rmSync, statSync, writeFileSync,
} from "node:fs";
import { createConnection, createServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { ensureBootstrapRuntime } from "../../connector/bootstrap-launcher.server.ts";
import { directorEngineURL } from "../../connector/runtime-configuration.server.mjs";
import { selectInstalledBootstrap } from "../../connector/bootstrap-selection.server.ts";
import { startHostContractServer } from "../../connector/host-ipc.server.ts";
import { EXPECTED_HOST_DESCRIPTOR } from "../../generated/host-contract.shared.ts";
import { PLANNING_CONTRACT_SHA256, PLANNING_CONTRACT_VERSION } from "../../generated/planning-contract.shared.ts";
import { buildRelease } from "../release/build-engine.mjs";
import { renderThirdPartyNotices } from "../release/notices.mjs";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const innerRuntimeArgument = "--director-inner-release-runtime";
const pastaExecutable = "/usr/bin/pasta";
const pastaCapabilityArguments = [
  "--quiet", "--foreground", "--ipv4-only",
  "--tcp-ports", "none", "--udp-ports", "none",
  "--tcp-ns", "none", "--udp-ns", "none",
  "/usr/bin/true",
];
const fixedRuntimePorts = [3307, 7041];
const maximumTransportOutputBytes = 4 * 1024 * 1024;

function exactPastaPermissionDenied(stderr) {
  return /Could(?: not|n't) write (?:to )?\/proc\/self\/uid_map: Operation not permitted/u.test(stderr) &&
    /Could(?: not|n't) configure user mappings/u.test(stderr) &&
    /clone: Operation not permitted/u.test(stderr);
}

export function classifyPastaCapability(probe) {
  if (probe?.status === 0 && !probe.error) return "pasta";
  if (probe?.error?.code === "ENOENT") return "direct";
  if (probe?.status !== 0 && exactPastaPermissionDenied(String(probe?.stderr ?? ""))) return "direct";
  throw new Error("DIRECTOR_RELEASE_RUNTIME_PASTA_PROBE");
}

function probePastaCapability() {
  return spawnSync(pastaExecutable, pastaCapabilityArguments, {
    encoding: "utf8",
    env: process.env,
    shell: false,
    timeout: 10_000,
    maxBuffer: 64 * 1024,
  });
}

function command(executable, args, options = {}) {
  const result = spawnSync(executable, args, { cwd: options.cwd, encoding: "utf8", env: options.env ?? process.env,
    shell: false, timeout: options.timeout ?? 360_000, maxBuffer: 4 * 1024 * 1024 });
  if (result.status !== 0 || result.error) throw new Error("DIRECTOR_RELEASE_RUNTIME_COMMAND");
  return result.stdout.trim();
}

function digest(path) { return createHash("sha256").update(readFileSync(path)).digest("hex"); }

function writePrivate(path, value, mode = 0o600) {
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  const descriptor = openSync(path, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600);
  try { writeFileSync(descriptor, value); } finally { closeSync(descriptor); }
  chmodSync(path, mode);
}

function processIdentity(pid) {
  const stat = readFileSync(`/proc/${pid}/stat`, "utf8");
  const fields = stat.slice(stat.lastIndexOf(")") + 2).split(" ");
  return { pid, parentPid: Number(fields[1]), start: fields[19], uid: statSync(`/proc/${pid}`).uid,
    argv: readFileSync(`/proc/${pid}/cmdline`, "utf8").split("\0").filter(Boolean) };
}

function processAlive(identity) {
  try {
    const stat = readFileSync(`/proc/${identity.pid}/stat`, "utf8");
    const fields = stat.slice(stat.lastIndexOf(")") + 2).split(" ");
    return fields[0] !== "Z" && fields[19] === identity.start;
  } catch { return false; }
}

async function listening(port) {
  return new Promise((accept) => {
    const socket = createConnection({ host: "127.0.0.1", port });
    const finish = (value) => { socket.destroy(); accept(value); };
    socket.setTimeout(200, () => finish(false)); socket.once("connect", () => finish(true)); socket.once("error", () => finish(false));
  });
}

async function waitFor(predicate, timeout = 45_000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (await predicate()) return;
    await new Promise((accept) => setTimeout(accept, 100));
  }
  throw new Error("DIRECTOR_RELEASE_RUNTIME_TIMEOUT");
}

async function reservePort() {
  const server = createServer();
  await new Promise((accept, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", accept);
  });
  const address = server.address();
  assert(address && typeof address === "object");
  await new Promise((accept, reject) => server.close((error) => error ? reject(error) : accept()));
  return address.port;
}

async function fixedRuntimePortsAbsent() {
  const occupied = await Promise.all(fixedRuntimePorts.map((port) => listening(port)));
  return occupied.every((value) => !value);
}

function validatedForwardedArguments(args) {
  if (args.length === 0) return [];
  if (args.length === 2 && args[0] === "--evidence" && resolve(args[1]) === args[1]) return [...args];
  throw new Error("DIRECTOR_RELEASE_RUNTIME_ARGUMENT");
}

export async function selectReleaseRuntimeTransport({
  probe,
  forwardedArguments = [],
  portsAbsent = fixedRuntimePortsAbsent,
  reserve = reservePort,
  nodeExecutable = process.execPath,
  scriptPath = fileURLToPath(import.meta.url),
} = {}) {
  const transport = classifyPastaCapability(probe ?? probePastaCapability());
  const innerArguments = [scriptPath, innerRuntimeArgument, ...validatedForwardedArguments(forwardedArguments)];
  if (transport === "direct") {
    if (!(await portsAbsent())) throw new Error("DIRECTOR_RELEASE_RUNTIME_PORT_OCCUPIED");
    return { transport, executable: nodeExecutable, arguments: innerArguments, shell: false };
  }
  const forwardedPort = await reserve();
  return {
    transport,
    executable: pastaExecutable,
    arguments: [
      "--quiet", "--foreground", "--ipv4-only",
      "--tcp-ports", `127.0.0.1/${forwardedPort}`, "--udp-ports", "none",
      "--tcp-ns", "none", "--udp-ns", "none",
      nodeExecutable, ...innerArguments,
    ],
    shell: false,
  };
}

export function boundedTransportChildCode(stderr, fallback) {
  const code = String(stderr ?? "").split("\n").findLast((line) => /^[A-Z0-9_]{3,96}$/u.test(line));
  return code ?? fallback;
}

async function launchReleaseLifecycle(forwardedArguments) {
  const plan = await selectReleaseRuntimeTransport({ forwardedArguments });
  return new Promise((accept, reject) => {
    let child;
    let childIdentity;
    let stdout = "";
    let stderr = "";
    let pendingFailure = null;
    const failOwnedChild = (code) => {
      pendingFailure ??= new Error(code);
      if (child?.pid) child.kill("SIGKILL");
    };
    try {
      child = spawn(plan.executable, plan.arguments, {
        env: process.env,
        shell: plan.shell,
        stdio: ["ignore", "pipe", "pipe"],
      });
    } catch {
      reject(new Error("DIRECTOR_RELEASE_RUNTIME_TRANSPORT_START"));
      return;
    }
    const timer = setTimeout(() => failOwnedChild("DIRECTOR_RELEASE_RUNTIME_TRANSPORT_TIMEOUT"), 420_000);
    const append = (current, chunk) => {
      const next = current + chunk;
      if (Buffer.byteLength(next) > maximumTransportOutputBytes) {
        failOwnedChild("DIRECTOR_RELEASE_RUNTIME_TRANSPORT_OUTPUT");
        return current;
      }
      return next;
    };
    child.stdout.on("data", (chunk) => { stdout = append(stdout, chunk); });
    child.stderr.on("data", (chunk) => { stderr = append(stderr, chunk); });
    child.once("spawn", () => {
      try {
        childIdentity = processIdentity(child.pid);
        if (typeof process.getuid === "function" && childIdentity.uid !== process.getuid()) {
          failOwnedChild("DIRECTOR_RELEASE_RUNTIME_TRANSPORT_OWNER");
        }
      } catch {
        failOwnedChild("DIRECTOR_RELEASE_RUNTIME_TRANSPORT_IDENTITY");
      }
    });
    child.once("error", () => failOwnedChild("DIRECTOR_RELEASE_RUNTIME_TRANSPORT_START"));
    child.once("close", (status, signal) => {
      clearTimeout(timer);
      if (childIdentity && processAlive(childIdentity)) {
        reject(new Error("DIRECTOR_RELEASE_RUNTIME_TRANSPORT_SURVIVED"));
        return;
      }
      if (pendingFailure) {
        reject(pendingFailure);
        return;
      }
      if (signal !== null) {
        reject(new Error("DIRECTOR_RELEASE_RUNTIME_TRANSPORT_SIGNAL"));
        return;
      }
      if (status !== 0) {
        const fallback = plan.transport === "pasta"
          ? "DIRECTOR_RELEASE_RUNTIME_ISOLATION_EXIT"
          : "DIRECTOR_RELEASE_RUNTIME_DIRECT_EXIT";
        reject(new Error(boundedTransportChildCode(stderr, fallback)));
        return;
      }
      try {
        const result = JSON.parse(stdout);
        assert.equal(result?.schemaVersion, 1);
        process.stdout.write(`${JSON.stringify(result)}\n`);
        accept(0);
      } catch {
        reject(new Error("DIRECTOR_RELEASE_RUNTIME_TRANSPORT_RESULT"));
      }
    });
  });
}

export async function runReleaseRuntimeLifecycle() {
  if (process.platform !== "linux" || process.arch !== "x64") throw new Error("DIRECTOR_RELEASE_RUNTIME_PLATFORM");
  if (command("/usr/bin/git", ["status", "--porcelain"], { cwd: repositoryRoot }) !== "") throw new Error("DIRECTOR_RELEASE_RUNTIME_DIRTY");
  if (await listening(3307)) throw new Error("DIRECTOR_RELEASE_RUNTIME_DOLT_PORT_OCCUPIED");
  if (await listening(7041)) throw new Error("DIRECTOR_RELEASE_RUNTIME_ENGINE_PORT_OCCUPIED");
  const candidate = command("/usr/bin/git", ["rev-parse", "HEAD"], { cwd: repositoryRoot });
  const root = mkdtempSync(join(tmpdir(), `director-release-runtime-${candidate.slice(0, 12)}-`));
  const environment = { HOME: join(root, "home"), XDG_RUNTIME_DIR: join(root, "runtime"), XDG_CACHE_HOME: join(root, "cache"), XDG_CONFIG_HOME: join(root, "config"), XDG_DATA_HOME: join(root, "data") };
  for (const path of Object.values(environment)) mkdirSync(path, { recursive: true, mode: 0o700 });
  const notices = join(root, "THIRD_PARTY_NOTICES.txt");
  const archive = join(root, "dolt-linux-amd64.tar.gz");
  const fixtureDolt = join(root, "fixture", "dolt");
  writePrivate(notices, renderThirdPartyNotices(repositoryRoot, candidate));
  writePrivate(archive, "release builder identity fixture\n");
  mkdirSync(dirname(fixtureDolt), { recursive: true, mode: 0o700 });
  copyFileSync(command("/usr/bin/which", ["dolt"]), fixtureDolt); chmodSync(fixtureDolt, 0o500);
  assert.equal(command(fixtureDolt, ["version"], { env: { ...environment, DOLT_DISABLE_VERSION_CHECK: "1" } }).split("\n")[0], "dolt version 2.3.2");
  const output = join(root, "release", "1.0.0-alpha.0");
  const built = buildRelease(["--source", join(repositoryRoot, "engine"), "--candidate", candidate, "--version", "1.0.0-alpha.0",
    "--notices", notices, "--dolt-version", "2.3.2", "--dolt-archive", archive, "--dolt-executable", fixtureDolt, "--output", output]);
  const privateDolt = join(environment.XDG_CACHE_HOME, "director", "engines", "dolt", "2.3.2", "linux-amd64", digest(fixtureDolt), "dolt");
  mkdirSync(dirname(privateDolt), { recursive: true, mode: 0o700 }); copyFileSync(fixtureDolt, privateDolt); chmodSync(privateDolt, 0o500);
  const bootstrapPath = join(output, "director-bootstrap-linux-amd64");
  const enginePath = join(output, "director-engine-linux-amd64");
  const noticesPath = join(output, "THIRD_PARTY_NOTICES.txt");
  const prepared = {
    schemaVersion: 1, channel: "release", sourceCandidate: candidate, target: "linux-amd64",
    bootstrap: { path: bootstrapPath, sha256: digest(bootstrapPath), size: statSync(bootstrapPath).size },
    engine: { mode: "release", version: built.metadata.version, sourceCandidate: candidate, target: "linux-amd64",
      binary: { path: enginePath, sha256: digest(enginePath), size: statSync(enginePath).size },
      notices: { path: noticesPath, sha256: digest(noticesPath), size: statSync(noticesPath).size },
      contractVersion: built.identity.contractVersion, contractSha256: built.identity.contractSha256 },
    dolt: { version: "2.3.2", target: "linux-amd64", binary: { path: privateDolt, sha256: digest(privateDolt), size: statSync(privateDolt).size }, archiveSha256: digest(archive) },
  };
  writePrivate(join(environment.XDG_CACHE_HOME, "director", "runtime", candidate, "linux-amd64", "prepared.json"), `${JSON.stringify(prepared)}\n`, 0o400);
  const selection = selectInstalledBootstrap({ schemaVersion: 3, state: "prepared", connectorCommit: candidate, channel: "release",
    bootstrap: { schemaVersion: 1, target: "linux-amd64", ...prepared.bootstrap } });
  const hostSocket = join(environment.XDG_RUNTIME_DIR, "director", "runtime", "host.sock");
  const stopHost = await startHostContractServer({ async describe() { return EXPECTED_HOST_DESCRIPTOR; }, async invoke(commandValue) {
    return { schemaVersion: 1, requestId: commandValue.requestId, cursor: 1, result: { effectId: commandValue.arguments.effectId, status: "unavailable", bindingHash: commandValue.arguments.bindingHash, priorDispatcherAbsent: true, maximumAgeMillis: 30_000, factHash: "0".repeat(64) } };
  } }, hostSocket);
  // The Engine placement comes from the same resolver the connector uses, so
  // this gate follows a declared isolation instead of an assumed default.
  const engine = directorEngineURL(environment);
  let first;
  let runtimeIdentities = [];
  try {
    first = await ensureBootstrapRuntime({ selection, environment, hostSocket, engineAddress: engine.address });
    const status = await first.status(); assert.equal(status.state, "current");
    const state = JSON.parse(readFileSync(join(environment.XDG_RUNTIME_DIR, "director", "supervisor", "state.json"), "utf8"));
    const bootstrap = processIdentity(state.pid); const engineChild = processIdentity(state.enginePid); const dolt = processIdentity(state.doltPid);
    runtimeIdentities = [bootstrap, engineChild, dolt];
    if (typeof process.getuid === "function") {
      assert.deepEqual(runtimeIdentities.map((identity) => identity.uid), [process.getuid(), process.getuid(), process.getuid()]);
    }
    assert.equal(engineChild.parentPid, bootstrap.pid); assert.equal(dolt.parentPid, bootstrap.pid);
    assert.equal(engineChild.argv[1], "serve-board"); assert.equal(dolt.argv[1], "sql-server");
    assert.ok(engineChild.argv.includes(`--listen=${engine.address}`), "the Engine child does not serve the resolved address");
    assert.equal(first.engineAddress, engine.address);
    const adopted = await ensureBootstrapRuntime({ selection, environment, hostSocket, engineAddress: engine.address });
    const adoptedStatus = await adopted.status(); assert.deepEqual([adoptedStatus.enginePid, adoptedStatus.doltPid], [status.enginePid, status.doltPid]);
    await adopted.close();
    const response = await fetch(`${engine.url}/v1/planning/home`, { method: "POST", headers: { "content-type": "application/json",
      "x-director-contract-version": PLANNING_CONTRACT_VERSION, "x-director-contract-hash": PLANNING_CONTRACT_SHA256 }, body: JSON.stringify({ hostId: first.host.id, pageSize: 25 }) });
    assert.equal(response.status, 200);
    await first.close();
    await waitFor(() => !processAlive(bootstrap) && !processAlive(engineChild) && !processAlive(dolt));
    return { schemaVersion: 1, sourceCandidate: candidate, engineSha256: prepared.engine.binary.sha256, doltVersion: "2.3.2",
      bootstrapOwnsChildren: true, supervisorAdopted: true, homeStatus: 200, publishedRuntimeCompilerExecutions: 0,
      leaseExpiryStoppedChildren: true, persistentDataPreserved: existsSync(join(environment.XDG_DATA_HOME, "director", "taskstore", "dolt", "director", ".dolt")) };
  } finally {
    await first?.close().catch(() => undefined);
    try {
      if (runtimeIdentities.length > 0) await waitFor(() => runtimeIdentities.every((identity) => !processAlive(identity)));
      await waitFor(fixedRuntimePortsAbsent, 45_000);
    } finally {
      await stopHost();
    }
    rmSync(root, { recursive: true, force: true });
  }
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))) {
  const inner = process.argv[2] === innerRuntimeArgument;
  const forwardedArguments = process.argv.slice(inner ? 3 : 2);
  try {
    if (!inner) {
      process.exitCode = await launchReleaseLifecycle(forwardedArguments);
    } else {
      const result = await runReleaseRuntimeLifecycle();
      if (forwardedArguments.length === 2 && forwardedArguments[0] === "--evidence") writePrivate(resolve(forwardedArguments[1]), `${JSON.stringify(result, null, 2)}\n`);
      else if (forwardedArguments.length !== 0) throw new Error("DIRECTOR_RELEASE_RUNTIME_ARGUMENT");
      process.stdout.write(`${JSON.stringify(result)}\n`);
    }
  } catch (error) {
    const code = error instanceof Error && /^[A-Z0-9_]{3,96}$/u.test(error.message) ? error.message : "DIRECTOR_RELEASE_RUNTIME_FAILED";
    process.stderr.write(`${code}\n`); process.exitCode = 1;
  }
}
