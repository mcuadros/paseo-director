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
import { connect, createServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, isAbsolute, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { createPaseoClient } from "@getpaseo/client";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const maximumOutputBytes = 4 * 1024 * 1024;

function run(executable, args, options = {}) {
  const result = spawnSync(executable, args, {
    cwd: options.cwd,
    encoding: "utf8",
    env: options.env,
    shell: false,
    timeout: options.timeout ?? 180_000,
    maxBuffer: maximumOutputBytes,
  });
  if (!options.allowFailure && (result.error || result.status !== 0)) {
    throw new Error(`${executable} failed with a bounded diagnostic`);
  }
  return result;
}

function runAsync(executable, args, options = {}) {
  return new Promise((resolveRun) => {
    const child = spawn(executable, args, {
      cwd: options.cwd,
      env: options.env,
      shell: false,
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    const append = (current, chunk) => `${current}${chunk}`.slice(-maximumOutputBytes);
    child.stdout.on("data", (chunk) => { stdout = append(stdout, chunk); });
    child.stderr.on("data", (chunk) => { stderr = append(stderr, chunk); });
    const timer = setTimeout(() => child.kill("SIGKILL"), options.timeout ?? 180_000);
    child.once("close", (status, signal) => {
      clearTimeout(timer);
      resolveRun({ status, signal, stdout, stderr, child });
    });
    if (options.onStart) options.onStart(child);
  });
}

function jsonOutput(result) {
  try {
    return JSON.parse(result.stdout.trim());
  } catch {
    throw new Error("Paseo did not return one JSON document");
  }
}

function git(cwd, ...args) {
  return run("git", args, {
    cwd,
    env: process.env.PATH
      ? { PATH: process.env.PATH, GIT_CONFIG_NOSYSTEM: "1" }
      : { GIT_CONFIG_NOSYSTEM: "1" },
  }).stdout.trim();
}

function commit(cwd, message, paths) {
  git(cwd, "add", "--", ...paths);
  git(cwd, "commit", "-m", message);
  return git(cwd, "rev-parse", "HEAD");
}

function repositoryCopy(destination) {
  const tracked = run("git", ["ls-files", "-z"], { cwd: repositoryRoot, env: process.env }).stdout
    .split("\0").filter(Boolean);
  const untracked = run("git", ["ls-files", "--others", "--exclude-standard", "-z"], {
    cwd: repositoryRoot,
    env: process.env,
  }).stdout.split("\0").filter(Boolean);
  for (const path of [...new Set([...tracked, ...untracked])].sort()) {
    const source = join(repositoryRoot, path);
    const status = lstatSync(source);
    if (!status.isFile() || status.isSymbolicLink()) {
      throw new Error("unsupported repository entry in activation fixture");
    }
    const target = join(destination, path);
    mkdirSync(dirname(target), { recursive: true });
    copyFileSync(source, target);
    chmodSync(target, status.mode & 0o777);
  }
}

async function availablePort() {
  const server = createServer();
  await new Promise((resolveListen, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolveListen);
  });
  const address = server.address();
  assert(address && typeof address === "object");
  await new Promise((resolveClose, reject) => server.close((error) => error ? reject(error) : resolveClose()));
  return address.port;
}

async function responseDropProxy(targetPort) {
  let resolveDrop;
  const dropped = new Promise((resolveDropped) => { resolveDrop = resolveDropped; });
  const sockets = new Set();
  const server = createServer((downstream) => {
    const upstream = connect({ host: "127.0.0.1", port: targetPort });
    sockets.add(downstream);
    sockets.add(upstream);
    let handshakeComplete = false;
    let dropComplete = false;
    downstream.on("data", (chunk) => {
      upstream.write(chunk);
    });
    upstream.on("data", (chunk) => {
      if (!handshakeComplete) {
        downstream.write(chunk);
        if (chunk.includes(Buffer.from("\r\n\r\n"))) handshakeComplete = true;
        return;
      }
      if (
        !dropComplete &&
        chunk.includes(Buffer.from("plugin.source.update.response"))
      ) {
        dropComplete = true;
        downstream.destroy();
        resolveDrop();
        setTimeout(() => upstream.destroy(), 1_000);
        return;
      }
      downstream.write(chunk);
    });
    const closeBoth = () => {
      downstream.destroy();
      upstream.destroy();
    };
    downstream.on("error", closeBoth);
    upstream.on("error", closeBoth);
    downstream.on("close", () => sockets.delete(downstream));
    upstream.on("close", () => sockets.delete(upstream));
  });
  await new Promise((resolveListen, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolveListen);
  });
  const address = server.address();
  assert(address && typeof address === "object");
  return {
    port: address.port,
    dropped,
    async close() {
      for (const socket of sockets) socket.destroy();
      await new Promise((resolveClose) => server.close(resolveClose));
    },
  };
}

function processStartTime(pid) {
  const stat = readFileSync(`/proc/${pid}/stat`, "utf8");
  const fields = stat.slice(stat.lastIndexOf(")") + 2).split(" ");
  return fields[19];
}

function processAlive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return error?.code !== "ESRCH";
  }
}

function secretAbsentFromProcessArguments(secret) {
  for (const entry of readdirSync("/proc", { withFileTypes: true })) {
    if (!entry.isDirectory() || !/^[0-9]+$/u.test(entry.name)) continue;
    try {
      if (readFileSync(join("/proc", entry.name, "cmdline")).includes(Buffer.from(secret))) {
        return false;
      }
    } catch {
      // A process may exit between directory enumeration and the bounded read.
    }
  }
  return true;
}

function startDaemon(home, port, environment) {
  const child = spawn("paseo", [
    "daemon", "start", "--home", home, "--listen", `127.0.0.1:${port}`,
    "--foreground", "--no-relay", "--no-mcp", "--no-web-ui",
  ], {
    env: environment,
    shell: false,
    stdio: ["ignore", "pipe", "pipe"],
  });
  let output = "";
  const capture = (chunk) => { output = `${output}${chunk}`.slice(-128 * 1024); };
  child.stdout.on("data", capture);
  child.stderr.on("data", capture);
  return { child, home, output: () => output };
}

function closed(child) {
  return child.exitCode !== null || child.signalCode !== null;
}

async function waitForDaemon(port, started, environment) {
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    if (closed(started.child)) throw new Error("Paseo daemon exited before readiness");
    const result = await runAsync("paseo", ["plugin", "ls", "--host", `127.0.0.1:${port}`, "--json"], {
      environment,
      env: environment,
      timeout: 5_000,
    });
    if (result.status === 0) return;
    await new Promise((resolveWait) => setTimeout(resolveWait, 200));
  }
  throw new Error("Paseo daemon did not become ready");
}

async function stopDaemon(started, environment) {
  if (closed(started.child)) return;
  await runAsync("paseo", ["daemon", "stop", "--home", started.home], { env: environment, timeout: 30_000 });
  if (!closed(started.child)) started.child.kill("SIGINT");
  await Promise.race([
    closed(started.child) ? Promise.resolve() : new Promise((resolveClose) => started.child.once("close", resolveClose)),
    new Promise((resolveTimeout) => setTimeout(resolveTimeout, 15_000)),
  ]);
  if (!closed(started.child)) {
    started.child.kill("SIGKILL");
    await new Promise((resolveClose) => started.child.once("close", resolveClose));
  }
}

function paseo(port, environment, action, allowFailure = false) {
  return run("paseo", ["plugin", ...action, "--host", `127.0.0.1:${port}`, "--json"], {
    env: environment,
    allowFailure,
  });
}

function installed(home) {
  return JSON.parse(readFileSync(join(home, "plugins", "sources.json"), "utf8").toString()).director;
}

function activationEntries(port, environment) {
  const entries = jsonOutput(paseo(port, environment, ["logs", "director"]));
  return entries.flatMap((entry) => {
    if (entry.stream !== "stdout") return [];
    try {
      const value = JSON.parse(entry.message);
      return value?.code === "DIRECTOR_ACTIVATION_READY" ? [value] : [];
    } catch {
      return [];
    }
  });
}

async function waitForActivation(port, environment, afterCount, expectedCommit) {
  const deadline = Date.now() + 180_000;
  while (Date.now() < deadline) {
    const list = jsonOutput(paseo(port, environment, ["ls"]));
    const activations = activationEntries(port, environment);
    const latest = activations.at(-1);
    if (
      list.length === 1 &&
      list[0].id === "director" &&
      list[0].status === "running" &&
      activations.length > afterCount &&
      latest?.connectorCommit === expectedCommit &&
      latest?.result === "running-current"
    ) {
      return { count: activations.length, latest };
    }
    await new Promise((resolveWait) => setTimeout(resolveWait, 250));
  }
  throw new Error("Director activation did not become running-current");
}

async function waitForInstalledCommit(port, environment, expectedCommit, timeout = 45_000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    const plugin = jsonOutput(paseo(port, environment, ["ls"]))[0];
    if (plugin?.commit === expectedCommit && plugin.status === "running") return true;
    await new Promise((resolveWait) => setTimeout(resolveWait, 250));
  }
  return false;
}

async function waitForAgentState(agent, predicate) {
  const deadline = Date.now() + 240_000;
  while (Date.now() < deadline) {
    let current;
    try {
      current = (await agent.refresh())?.agent;
    } catch {
      current = agent.current();
    }
    if (current && predicate(current)) return current;
    await new Promise((resolveWait) => setTimeout(resolveWait, 100));
  }
  throw new Error("sentinel agent did not reach the required public state");
}

async function waitForAgent(agent) {
  return waitForAgentState(
    agent,
    (current) => current.status === "running" && Boolean(current.activeTurn?.turnId),
  );
}

async function waitForWorkspace(workspace) {
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    const current = await workspace.refresh();
    if (current?.status === "done" || current?.status === "attention") return current;
    if (current?.status === "failed") throw new Error("sentinel workspace setup failed");
    await new Promise((resolveWait) => setTimeout(resolveWait, 100));
  }
  throw new Error("sentinel workspace did not become ready");
}

function sentinelIdentity(agent, processIdentity) {
  assert(agent.activeTurn?.turnId);
  const processPid = processIdentity.processPid ?? processIdentity.pid;
  return {
    agentId: agent.id,
    workspaceId: agent.workspaceId,
    turnId: agent.activeTurn.turnId,
    startedAt: agent.activeTurn.startedAt,
    processPid,
    processStart: processIdentity.processStart,
  };
}

async function assertSentinels(handles, expected) {
  const observed = [];
  for (let index = 0; index < handles.length; index += 1) {
    const current = await waitForAgent(handles[index].agent);
    assert.equal(processAlive(expected[index].processPid), true);
    assert.equal(processStartTime(expected[index].processPid), expected[index].processStart);
    const identity = sentinelIdentity(current, expected[index]);
    assert.deepEqual(identity, expected[index]);
    observed.push(identity);
  }
  return observed;
}

async function waitForMarker(path, token) {
  const deadline = Date.now() + 240_000;
  while (Date.now() < deadline) {
    if (existsSync(path)) {
      const marker = JSON.parse(readFileSync(path, "utf8"));
      if (marker.token === token && Number.isSafeInteger(marker.pid) && processAlive(marker.pid)) {
        return { pid: marker.pid, processStart: processStartTime(marker.pid) };
      }
    }
    await new Promise((resolveWait) => setTimeout(resolveWait, 100));
  }
  throw new Error("sentinel process marker did not become live");
}

function writeRuntimeConfig(path, value) {
  writeFileSync(path, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600 });
  chmodSync(path, 0o600);
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

export async function runRestartFreeLifecycle() {
  const sourceCandidate = git(repositoryRoot, "rev-parse", "HEAD");
  const sourceTree = git(repositoryRoot, "rev-parse", "HEAD^{tree}");
  assert.equal(
    git(repositoryRoot, "status", "--porcelain=v1", "--untracked-files=no"),
    "",
    "restart-free evidence requires a clean exact Candidate",
  );
  const root = mkdtempSync(join(tmpdir(), "director-restart-free-"));
  const source = join(root, "source");
  const engineSource = join(root, "engine-source");
  const home = join(root, "paseo-home");
  const configBase = join(root, "operator-config");
  const cacheBase = join(root, "engine-cache");
  const authority = join(root, "authority");
  const credentialFile = join(authority, "connector.password");
  const sentinelRoot = join(root, "sentinels");
  const password = `${randomUUID()}${randomUUID()}`;
  const port = 6767;
  for (const path of [source, home, configBase, cacheBase, authority, sentinelRoot]) {
    mkdirSync(path, { recursive: true, mode: 0o700 });
    chmodSync(path, 0o700);
  }
  repositoryCopy(source);
  git(source, "init", "-b", "stable");
  git(source, "config", "user.name", "Director Restart-Free Fixture");
  git(source, "config", "user.email", "director-restart-free@example.invalid");
  const initialCommit = commit(source, "fixture: initial restart-free candidate", ["."]);
  run("git", ["clone", "--no-hardlinks", source, engineSource], {
    env: process.env.PATH
      ? { PATH: process.env.PATH, GIT_CONFIG_NOSYSTEM: "1" }
      : { GIT_CONFIG_NOSYSTEM: "1" },
  });
  writeFileSync(credentialFile, `${password}\n`, { mode: 0o600 });
  const runtimeDirectory = join(configBase, "director");
  mkdirSync(runtimeDirectory, { mode: 0o700 });
  const runtimePath = join(runtimeDirectory, "runtime.json");
  const runtimeConfig = {
    schemaVersion: 1,
    paseo: { credentialFile },
    engine: { mode: "development", sourceRoot: join(engineSource, "engine") },
  };
  writeRuntimeConfig(runtimePath, runtimeConfig);
  const daemonConfig = {
    version: 1,
    daemon: {
      mcp: { enabled: false, injectIntoAgents: false },
      browserTools: { enabled: false },
      relay: { enabled: false },
    },
    agents: {
      providers: {
        "director-sentinel": {
          extends: "acp",
          label: "Director restart-free sentinel",
          command: [process.execPath, join(source, "tools", "packaging", "sentinel-acp.mjs")],
          params: { supportsMcpServers: false },
          models: [{ id: "fixture", label: "Fixture", isDefault: true }],
        },
      },
      metadataGeneration: { providers: [] },
    },
    features: {
      dictation: { enabled: false },
      voiceMode: { enabled: false },
      webUi: { enabled: false },
    },
    pluginsEnabled: true,
  };
  writeFileSync(join(home, "config.json"), `${JSON.stringify(daemonConfig, null, 2)}\n`, { mode: 0o600 });
  const environment = {
    ...process.env,
    PASEO_PASSWORD: password,
    PASEO_DICTATION_ENABLED: "false",
    PASEO_VOICE_MODE_ENABLED: "false",
    PASEO_LOG_LEVEL: "warn",
    XDG_CONFIG_HOME: configBase,
    XDG_CACHE_HOME: cacheBase,
  };
  for (const name of [
    "DIRECTOR_ENGINE_MODE",
    "DIRECTOR_ENGINE_RELEASE_METADATA",
    "DIRECTOR_ENGINE_SOURCE_ROOT",
    "DIRECTOR_ENGINE_URL",
    "DIRECTOR_PASEO_CREDENTIAL_FILE",
    "DIRECTOR_PASEO_PASSWORD",
    "DIRECTOR_PASEO_URL",
  ]) delete environment[name];
  const daemon = startDaemon(home, port, environment);
  const sentinelHandles = [];
  let client;
  const checkpoints = [];
  try {
    await waitForDaemon(port, daemon, environment);
    const daemonPID = daemon.child.pid;
    const daemonStart = processStartTime(daemonPID);
    assert.deepEqual(jsonOutput(paseo(port, environment, ["ls"])), []);

    client = createPaseoClient({
      url: `ws://127.0.0.1:${port}/ws`,
      password,
      clientId: "director-restart-free-sentinel-controller",
      reconnect: { enabled: false },
    });
    await client.connect();
    const providerSnapshot = await client.providers.waitForReady({
      cwd: sentinelRoot,
      timeoutMs: 120_000,
      requestId: "restart-free-provider-readiness",
    });
    assert.ok(providerSnapshot.entries.every((entry) => entry.status !== "loading"));
    const modelResult = await client.providers.listModels("director-sentinel", {
      cwd: sentinelRoot,
      requestId: "restart-free-provider-models",
    });
    assert.equal(modelResult.error ?? null, null);
    const sentinelModel = modelResult.models?.find((model) => model.isDefault) ??
      modelResult.models?.find((model) => model.isSelectable !== false);
    assert.ok(sentinelModel);
    for (let index = 0; index < 2; index += 1) {
      const directory = join(sentinelRoot, `workspace-${index + 1}`);
      mkdirSync(directory, { mode: 0o700 });
      const workspace = await client.workspaces.create({
        requestId: `restart-free-workspace-${index + 1}`,
        title: `Unrelated sentinel workspace ${index + 1}`,
        source: { kind: "directory", path: directory },
      });
      await waitForWorkspace(workspace);
      const token = randomUUID();
      const markerPath = join(sentinelRoot, `turn-${index + 1}-${token}.json`);
      const prompt = `DIRECTOR_SENTINEL ${markerPath} ${token}`;
      const agent = await workspace.agents.create({
        requestId: `restart-free-agent-${index + 1}`,
        clientMessageId: `restart-free-message-${index + 1}`,
        title: `Unrelated sentinel agent ${index + 1}`,
        labels: { fixture: "restart-free", role: "unrelated-sentinel" },
        config: { provider: `director-sentinel/${sentinelModel.id}` },
        prompt,
        autoArchive: false,
      });
      sentinelHandles.push({ workspace, agent, markerPath, token });
    }
    const sentinels = [];
    for (const handle of sentinelHandles) {
      const [agent, processIdentity] = await Promise.all([
        waitForAgent(handle.agent),
        waitForMarker(handle.markerPath, handle.token),
      ]);
      sentinels.push(sentinelIdentity(agent, processIdentity));
    }
    checkpoints.push({ phase: "absent", sentinels: await assertSentinels(sentinelHandles, sentinels) });

    const installedResult = jsonOutput(paseo(port, environment, ["add", `file://${source}`, "--ref", "stable"]));
    assert.equal(installedResult.commit, initialCommit);
    let activation = await waitForActivation(port, environment, 0, initialCommit);
    checkpoints.push({ phase: "installed", sentinels: await assertSentinels(sentinelHandles, sentinels) });

    for (let index = 0; index < 2; index += 1) {
      jsonOutput(paseo(port, environment, ["reload", "director"]));
      activation = await waitForActivation(port, environment, activation.count, initialCommit);
    }
    checkpoints.push({ phase: "repeated-reload", sentinels: await assertSentinels(sentinelHandles, sentinels) });

    const concurrent = await Promise.all([
      runAsync("paseo", ["plugin", "reload", "director", "--host", `127.0.0.1:${port}`, "--json"], { env: environment }),
      runAsync("paseo", ["plugin", "reload", "director", "--host", `127.0.0.1:${port}`, "--json"], { env: environment }),
    ]);
    assert.ok(concurrent.every((result) => result.status === 0));
    activation = await waitForActivation(port, environment, activation.count, initialCommit);
    checkpoints.push({ phase: "concurrent-reload", sentinels: await assertSentinels(sentinelHandles, sentinels) });

    writeFileSync(join(source, "release", "response-loss-probe.txt"), "response loss target\n");
    const responseLossCommit = commit(source, "fixture: response-loss update target", [
      "release/response-loss-probe.txt",
    ]);
    const proxy = await responseDropProxy(port);
    const lostResult = await runAsync("paseo", ["plugin", "update", "director", "--host", `127.0.0.1:${proxy.port}`, "--json"], {
      env: environment,
    });
    await proxy.dropped;
    await proxy.close();
    const responseLossAttempts = 1;
    const responseLossHandoffObserved =
      lostResult.status !== 0 &&
      await waitForInstalledCommit(port, environment, responseLossCommit);
    assert.equal(responseLossHandoffObserved, true, "lost reload response did not follow server handoff");
    const responseLossReplay = jsonOutput(paseo(port, environment, ["update", "director"]));
    assert.equal(responseLossReplay[0].updated, false);
    activation = await waitForActivation(port, environment, activation.count, responseLossCommit);
    checkpoints.push({ phase: "response-loss-reconciled", sentinels: await assertSentinels(sentinelHandles, sentinels) });

    writeRuntimeConfig(runtimePath, { ...runtimeConfig, engine: { ...runtimeConfig.engine, unsupported: true } });
    assert.notEqual(paseo(port, environment, ["reload", "director"], true).status, 0);
    const failedRecord = jsonOutput(paseo(port, environment, ["ls"]))[0];
    assert.equal(failedRecord.status, "failed");
    assert.equal(failedRecord.commit, responseLossCommit);
    checkpoints.push({ phase: "configuration-drift-refused", sentinels: await assertSentinels(sentinelHandles, sentinels) });

    const enginePort = await availablePort();
    const updatedRuntime = {
      ...runtimeConfig,
      engine: { ...runtimeConfig.engine, url: `http://127.0.0.1:${enginePort}` },
    };
    writeRuntimeConfig(runtimePath, updatedRuntime);
    jsonOutput(paseo(port, environment, ["reload", "director"]));
    activation = await waitForActivation(port, environment, activation.count, responseLossCommit);
    assert.equal(activation.latest.settings.find((setting) => setting.name === "engine.url")?.source, "overridden");
    checkpoints.push({ phase: "configuration-reloaded", sentinels: await assertSentinels(sentinelHandles, sentinels) });

    const connectorPath = join(source, "connector", "paseo.server.ts");
    const goodConnector = readFileSync(connectorPath, "utf8");
    writeFileSync(
      connectorPath,
      `import { fileURLToPath as brokenFileURLToPath } from "node:url";\nconst brokenCheckout = brokenFileURLToPath(import.meta.url);\nvoid brokenCheckout;\n${goodConnector}`,
    );
    const brokenCommit = commit(source, "fixture: rejected CommonJS candidate", ["connector/paseo.server.ts"]);
    assert.notEqual(paseo(port, environment, ["update", "director"], true).status, 0);
    assert.equal(installed(home).commit, responseLossCommit);
    const preserved = jsonOutput(paseo(port, environment, ["ls"]))[0];
    assert.equal(preserved.commit, responseLossCommit);
    assert.equal(preserved.status, "running");
    checkpoints.push({ phase: "failed-candidate-preserved", sentinels: await assertSentinels(sentinelHandles, sentinels) });

    writeFileSync(connectorPath, goodConnector);
    writeFileSync(join(source, "release", "restart-free-probe.txt"), "fresh compiled output\n");
    const currentCommit = commit(source, "fixture: recover with current compiled backend", [
      "connector/paseo.server.ts",
      "release/restart-free-probe.txt",
    ]);
    const updated = jsonOutput(paseo(port, environment, ["update", "director"]));
    assert.equal(updated[0].updated, true);
    assert.equal(updated[0].currentCommit, currentCommit);
    activation = await waitForActivation(port, environment, activation.count, currentCommit);
    assert.equal(installed(home).commit, currentCommit);
    checkpoints.push({ phase: "updated-current", sentinels: await assertSentinels(sentinelHandles, sentinels) });

    const noChange = jsonOutput(paseo(port, environment, ["update", "director"]));
    assert.equal(noChange[0].updated, false);
    jsonOutput(paseo(port, environment, ["reload", "director"]));
    activation = await waitForActivation(port, environment, activation.count, currentCommit);
    checkpoints.push({ phase: "current-reloaded", sentinels: await assertSentinels(sentinelHandles, sentinels) });

    assert.equal(processAlive(daemonPID), true);
    assert.equal(processStartTime(daemonPID), daemonStart);
    const finalPlugin = jsonOutput(paseo(port, environment, ["ls"]))[0];
    assert.equal(finalPlugin.status, "running");
    assert.equal(finalPlugin.commit, currentCommit);
    const finalPluginLogs = jsonOutput(paseo(port, environment, ["logs", "director"]));
    assert.equal(daemon.output().includes(password), false);
    assert.equal(JSON.stringify(finalPluginLogs).includes(password), false);
    assert.equal(secretAbsentFromProcessArguments(password), true);
    return {
      schemaVersion: 1,
      sourceCandidate,
      sourceTree,
      paseoVersion: "0.7.2",
      platform: "linux-amd64",
      sentinelProvider: `director-sentinel/${sentinelModel.id}`,
      daemon: { pid: daemonPID, processStart: daemonStart, restarted: false },
      commits: {
        initial: initialCommit,
        responseLoss: responseLossCommit,
        rejected: brokenCommit,
        current: currentCommit,
      },
      sentinels,
      checkpoints,
      activation: {
        lifecycle: activation.latest.lifecycle,
        result: activation.latest.result,
        configurationSchemaVersion: activation.latest.configurationSchemaVersion,
        configurationSha256: activation.latest.configurationSha256,
        legacyEnvironment: activation.latest.legacyEnvironment,
        settings: activation.latest.settings,
        connectorCommit: activation.latest.connectorCommit,
      },
      responseLossReconciled: true,
      responseLossHandoffObserved,
      responseLossAttempts,
      repeatedReloadSafe: true,
      concurrentReloadSafe: true,
      configurationDriftRefused: true,
      failedCandidatePreserved: true,
      staleCompiledOutputRejected: true,
      pluginOnlyReloadVerified: true,
      daemonRestarted: false,
      secretFreeLogsArgumentsAndEvidence: true,
    };
  } finally {
    for (const handle of sentinelHandles.reverse()) {
      await handle.agent.archive().catch(() => undefined);
      await handle.workspace.archive().catch(() => undefined);
    }
    await client?.close().catch(() => undefined);
    if (!closed(daemon.child)) {
      paseo(port, environment, ["remove", "director"], true);
    }
    await stopDaemon(daemon, environment);
    rmSync(root, { recursive: true, force: true });
    if (existsSync(root)) throw new Error("owned restart-free lifecycle root survived cleanup");
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const result = await runRestartFreeLifecycle();
    if (process.argv.length === 4 && process.argv[2] === "--evidence") {
      writeEvidence(resolve(process.argv[3]), result);
    } else if (process.argv.length !== 2) {
      throw new Error("usage: paseo-restart-free-lifecycle.mjs [--evidence <absolute-new-path>]");
    }
    process.stdout.write(`${JSON.stringify(result)}\n`, () => process.exit(0));
  } catch (error) {
    process.stderr.write(
      `DIRECTOR_RESTART_FREE_LIFECYCLE_FAILED: ${error instanceof Error ? error.message : "unknown failure"}\n`,
      () => process.exit(1),
    );
  }
}
