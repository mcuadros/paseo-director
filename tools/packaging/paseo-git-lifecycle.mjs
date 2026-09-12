#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import {
  chmodSync,
  closeSync,
  constants,
  copyFileSync,
  existsSync,
  fsyncSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  openSync,
  readFileSync,
  readdirSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, isAbsolute, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { verifyInstall } from "./verify-install.mjs";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const commandEnvironment = {
  ...process.env,
  PASEO_DICTATION_ENABLED: "false",
  PASEO_VOICE_MODE_ENABLED: "false",
  PASEO_LOG_LEVEL: "warn",
};
for (const name of [
  "DIRECTOR_ENGINE_MODE",
  "DIRECTOR_ENGINE_RELEASE_METADATA",
  "DIRECTOR_ENGINE_SOURCE_ROOT",
  "DIRECTOR_ENGINE_URL",
  "DIRECTOR_PASEO_CREDENTIAL_FILE",
  "DIRECTOR_PASEO_PASSWORD",
  "DIRECTOR_PASEO_URL",
]) delete commandEnvironment[name];

function run(executable, args, options = {}) {
  const result = spawnSync(executable, args, {
    cwd: options.cwd,
    encoding: "utf8",
    env: options.env ?? commandEnvironment,
    shell: false,
    timeout: options.timeout ?? 180_000,
    maxBuffer: 4 * 1024 * 1024,
  });
  if (!options.allowFailure && (result.error || result.status !== 0)) {
    throw new Error(`${executable} failed: ${(result.stderr ?? "").slice(-4_096)}`);
  }
  return result;
}

function runAsync(executable, args, options = {}) {
  return new Promise((resolveRun) => {
    const child = spawn(executable, args, {
      cwd: options.cwd,
      env: options.env ?? commandEnvironment,
      shell: false,
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    const append = (current, chunk) => `${current}${chunk}`.slice(-4 * 1024 * 1024);
    child.stdout.on("data", (chunk) => { stdout = append(stdout, chunk); });
    child.stderr.on("data", (chunk) => { stderr = append(stderr, chunk); });
    const timer = setTimeout(() => child.kill("SIGKILL"), options.timeout ?? 180_000);
    child.once("close", (status) => {
      clearTimeout(timer);
      resolveRun({ status, stdout, stderr });
    });
  });
}

function JSONOutput(result) {
  const text = result.stdout.trim();
  try {
    return JSON.parse(text);
  } catch {
    throw new Error(`Paseo output was not JSON: ${text.slice(-1_024)}`);
  }
}

function git(cwd, ...args) {
  return run("git", args, { cwd }).stdout.trim();
}

function commit(cwd, message, paths) {
  git(cwd, "add", "--", ...paths);
  git(cwd, "commit", "-m", message);
  return git(cwd, "rev-parse", "HEAD");
}

function repositoryCopy(destination) {
  const tracked = run("git", ["ls-files", "-z"], { cwd: repositoryRoot }).stdout.split("\0").filter(Boolean);
  const untracked = run("git", ["ls-files", "--others", "--exclude-standard", "-z"], { cwd: repositoryRoot }).stdout.split("\0").filter(Boolean);
  for (const path of [...new Set([...tracked, ...untracked])].sort()) {
    const source = join(repositoryRoot, path);
    const status = lstatSync(source);
    if (!status.isFile() || status.isSymbolicLink()) throw new Error(`unsupported repository entry ${path}`);
    const target = join(destination, path);
    mkdirSync(dirname(target), { recursive: true });
    copyFileSync(source, target);
    chmodSync(target, status.mode & 0o777);
  }
}

function startDaemon(home, port) {
  const child = spawn("paseo", ["daemon", "start", "--home", home, "--listen", `127.0.0.1:${port}`, "--foreground", "--no-relay", "--no-mcp", "--no-web-ui"], {
    env: commandEnvironment,
    shell: false,
    stdio: ["ignore", "pipe", "pipe"],
  });
  let output = "";
  const capture = (chunk) => { output = `${output}${chunk}`.slice(-128 * 1024); };
  child.stdout.on("data", capture);
  child.stderr.on("data", capture);
  return { child, home, output: () => output };
}

function childClosed(child) {
  return child.exitCode !== null || child.signalCode !== null;
}

async function waitForDaemon(port, started) {
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    if (childClosed(started.child)) throw new Error(`Paseo daemon exited: ${started.output()}`);
    const result = await runAsync("paseo", ["plugin", "ls", "--host", `127.0.0.1:${port}`, "--json"], { timeout: 5_000 });
    if (result.status === 0) return;
    await new Promise((resolveWait) => setTimeout(resolveWait, 200));
  }
  throw new Error(`Paseo daemon did not become ready: ${started.output()}`);
}

async function stopDaemon(started) {
  if (childClosed(started.child)) return;
  await runAsync("paseo", ["daemon", "stop", "--home", started.home], { timeout: 30_000 });
  if (!childClosed(started.child)) started.child.kill("SIGINT");
  await Promise.race([
    childClosed(started.child)
      ? Promise.resolve()
      : new Promise((resolveClose) => started.child.once("close", resolveClose)),
    new Promise((resolveTimeout) => setTimeout(resolveTimeout, 15_000)),
  ]);
  if (!childClosed(started.child)) {
    started.child.kill("SIGKILL");
    await new Promise((resolveClose) => started.child.once("close", resolveClose));
  }
}

function paseo(port, action, allowFailure = false) {
  return run("paseo", ["plugin", ...action, "--host", `127.0.0.1:${port}`, "--json"], { allowFailure });
}

function installedRecord(home) {
  return JSON.parse(readFileSync(join(home, "plugins", "sources.json"), "utf8")).director;
}

function assertPreserved(home, port, commitSHA) {
  const list = JSONOutput(paseo(port, ["ls"]));
  assert.equal(list.length, 1);
  assert.equal(list[0].commit, commitSHA);
  assert.equal(installedRecord(home).commit, commitSHA);
  const staging = join(home, "plugins", ".staging");
  assert.deepEqual(existsSync(staging) ? readdirSync(staging) : [], []);
}

export async function runLifecycle() {
  const root = mkdtempSync(join(tmpdir(), "director-paseo-lifecycle-"));
  const source = join(root, "source");
  const home = join(root, "paseo-home");
  const external = join(root, "operator-state");
  const configBase = join(root, "operator-config");
  const cacheBase = join(root, "engine-cache");
  const authority = join(root, "authority");
  const credentialFile = join(authority, "connector.password");
  const stateSentinel = join(external, "director-state.json");
  const cacheSentinel = join(external, "engine-cache.marker");
  const port = 6767;
  mkdirSync(source, { recursive: true, mode: 0o700 });
  mkdirSync(external, { mode: 0o700 });
  for (const path of [configBase, cacheBase, authority]) mkdirSync(path, { mode: 0o700 });
  writeFileSync(stateSentinel, "preserved-state\n", { mode: 0o600 });
  writeFileSync(cacheSentinel, "preserved-cache\n", { mode: 0o600 });
  repositoryCopy(source);
  git(source, "init", "-b", "stable");
  git(source, "config", "user.name", "Director Lifecycle Fixture");
  git(source, "config", "user.email", "director-lifecycle@example.invalid");
  const initial = commit(source, "fixture: clean Director install", ["."]);
  const password = `${randomUUID()}${randomUUID()}`;
  writeFileSync(credentialFile, `${password}\n`, { mode: 0o600 });
  const runtimeDirectory = join(configBase, "director");
  mkdirSync(runtimeDirectory, { mode: 0o700 });
  writeFileSync(join(runtimeDirectory, "runtime.json"), `${JSON.stringify({
    schemaVersion: 1,
    paseo: { credentialFile },
    engine: { mode: "development", sourceRoot: join(source, "engine") },
  }, null, 2)}\n`, { mode: 0o600 });
  Object.assign(commandEnvironment, {
    PASEO_PASSWORD: password,
    XDG_CONFIG_HOME: configBase,
    XDG_CACHE_HOME: cacheBase,
  });
  const packageBytes = readFileSync(join(source, "package.json"));
  const lockBytes = readFileSync(join(source, "package-lock.json"));
  let daemon = startDaemon(home, port);
  const observed = { cleanInstall: initial, compatibleUpdate: "", recoveryUpdate: "" };
  try {
    await waitForDaemon(port, daemon);
    const installed = JSONOutput(paseo(port, ["add", `file://${source}`, "--ref", "stable"]));
    assert.equal(installed.id, "director");
    assert.equal(installed.commit, initial);
    const firstRecord = installedRecord(home);
    assert.equal(firstRecord.commit, initial);
    assert.equal(existsSync(join(firstRecord.checkoutRoot, "node_modules/@getpaseo/client/package.json")), true);
    assert.equal(existsSync(join(firstRecord.checkoutRoot, "node_modules/typescript")), false, "Paseo installation must omit development dependencies");

    writeFileSync(join(source, "release", "lifecycle-probe.txt"), "compatible update\n");
    observed.compatibleUpdate = commit(source, "fixture: compatible update", ["release/lifecycle-probe.txt"]);
    const concurrent = await Promise.all([
      runAsync("paseo", ["plugin", "update", "director", "--host", `127.0.0.1:${port}`, "--json"]),
      runAsync("paseo", ["plugin", "update", "director", "--host", `127.0.0.1:${port}`, "--json"]),
    ]);
    assert.ok(concurrent.every((result) => result.status === 0), concurrent.map((result) => result.stderr).join("\n"));
    assert.equal(concurrent.flatMap((result) => JSONOutput(result)).filter((result) => result.updated === true).length, 1);
    assertPreserved(home, port, observed.compatibleUpdate);

    const stalePackage = JSON.parse(packageBytes.toString("utf8"));
    stalePackage.dependencies["@getpaseo/client"] = "0.7.1";
    writeFileSync(join(source, "package.json"), `${JSON.stringify(stalePackage, null, 2)}\n`);
    commit(source, "fixture: stale package lock", ["package.json"]);
    assert.notEqual(paseo(port, ["update", "director"], true).status, 0);
    assertPreserved(home, port, observed.compatibleUpdate);

    writeFileSync(join(source, "package.json"), packageBytes);
    const networkLock = JSON.parse(lockBytes.toString("utf8"));
    networkLock.packages["node_modules/@getpaseo/client"].resolved = "http://127.0.0.1:9/client.tgz";
    writeFileSync(join(source, "package-lock.json"), `${JSON.stringify(networkLock, null, 2)}\n`);
    commit(source, "fixture: unavailable registry asset", ["package.json", "package-lock.json"]);
    assert.notEqual(paseo(port, ["update", "director"], true).status, 0);
    assertPreserved(home, port, observed.compatibleUpdate);

    writeFileSync(join(source, "package-lock.json"), lockBytes);
    const lifecyclePackage = JSON.parse(packageBytes.toString("utf8"));
    lifecyclePackage.scripts.preinstall = `node -e require('node:fs').writeFileSync(${JSON.stringify(join(root, "forbidden-lifecycle"))},'executed')`;
    writeFileSync(join(source, "package.json"), `${JSON.stringify(lifecyclePackage, null, 2)}\n`);
    commit(source, "fixture: forbidden package lifecycle", ["package.json", "package-lock.json"]);
    assert.notEqual(paseo(port, ["update", "director"], true).status, 0);
    assert.equal(existsSync(join(root, "forbidden-lifecycle")), false, "npm package lifecycle scripts must remain disabled");
    assertPreserved(home, port, observed.compatibleUpdate);

    writeFileSync(join(source, "package.json"), packageBytes);
    writeFileSync(join(source, "package-lock.json"), lockBytes);
    observed.recoveryUpdate = commit(source, "fixture: recover exact locked update", ["package.json", "package-lock.json"]);
    const recovered = JSONOutput(paseo(port, ["update", "director"]));
    assert.equal(recovered[0].updated, true);
    assert.equal(recovered[0].currentCommit, observed.recoveryUpdate);
    assertPreserved(home, port, observed.recoveryUpdate);

    await stopDaemon(daemon);
    daemon = startDaemon(home, port);
    await waitForDaemon(port, daemon);
    assertPreserved(home, port, observed.recoveryUpdate);

    const incompatible = verifyInstall({ platform: "linux", architecture: "x64", nodeVersion: "26.7.0", paseoVersion: "0.8.0", repositoryRoot });
    assert.deepEqual(incompatible, { code: "DIRECTOR_INSTALL_PASEO_UNSUPPORTED", message: "Director supports exact Paseo 0.7.2; observed 0.8.0" });
    assert.doesNotMatch(JSON.stringify(incompatible), new RegExp(root.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"), "u"));

    JSONOutput(paseo(port, ["remove", "director"]));
    assert.deepEqual(JSONOutput(paseo(port, ["ls"])), []);
    assert.equal(existsSync(join(home, "plugins", "director")), false);
    assert.equal(readFileSync(stateSentinel, "utf8"), "preserved-state\n");
    assert.equal(readFileSync(cacheSentinel, "utf8"), "preserved-cache\n");
    return {
      schemaVersion: 1,
      paseoVersion: "0.7.2",
      target: "linux-amd64",
      nodeVersion: process.versions.node,
      ...observed,
      failedUpdatePreserved: true,
      restartPreserved: true,
      incompatibleDiagnostic: incompatible.code,
      lifecycleScriptsExecuted: 0,
      managedCleanup: true,
      externalStatePreserved: true,
    };
  } finally {
    await stopDaemon(daemon);
    await new Promise((resolveWait) => setTimeout(resolveWait, 1_000));
    rmSync(root, { recursive: true, force: true });
    await new Promise((resolveWait) => setTimeout(resolveWait, 1_000));
    if (existsSync(root)) {
      rmSync(root, { recursive: true, force: true });
      await new Promise((resolveWait) => setTimeout(resolveWait, 250));
    }
    if (existsSync(root)) throw new Error("owned lifecycle root reappeared after cleanup");
  }
}

function writeEvidence(path, result) {
  if (!isAbsolute(path) || existsSync(path)) throw new Error("evidence path must be a new absolute path");
  const descriptor = openSync(path, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600);
  try {
    writeFileSync(descriptor, `${JSON.stringify(result, null, 2)}\n`);
    fsyncSync(descriptor);
  } finally {
    closeSync(descriptor);
  }
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))) {
  try {
    const result = await runLifecycle();
    if (process.argv.length === 4 && process.argv[2] === "--evidence") {
      writeEvidence(resolve(process.argv[3]), result);
    } else if (process.argv.length !== 2) {
      throw new Error("usage: paseo-git-lifecycle.mjs [--evidence <absolute-new-path>]");
    }
    process.stdout.write(`${JSON.stringify(result)}\n`);
  } catch (error) {
    process.stderr.write(`PASEO_LIFECYCLE_FAILED: ${error instanceof Error ? error.message : "unknown failure"}\n`);
    process.exitCode = 1;
  }
}
