#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Exact Paseo 0.7.2 disposable main/release channel evidence. Resources are
// deliberately preserved for coordinator readback and screenshots.

import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import {
  chmodSync,
  closeSync,
  constants,
  existsSync,
  fsyncSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  openSync,
  readFileSync,
  readdirSync,
  readlinkSync,
  realpathSync,
  statSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { createServer, Socket } from "node:net";
import { tmpdir } from "node:os";
import { dirname, isAbsolute, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { createPaseoApi } from "@getpaseo/client";
import { DaemonClient } from "@getpaseo/client/internal/daemon-client";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const paseoExecutable = (() => {
  const result = spawnSync("/usr/bin/which", ["paseo"], { encoding: "utf8", env: process.env });
  const path = result.status === 0 ? result.stdout.trim() : "";
  if (!isAbsolute(path)) throw new Error("DIRECTOR_MAIN_LIFECYCLE_PASEO_MISSING");
  return realpathSync(path);
})();
const maximumOutputBytes = 4 * 1024 * 1024;
const planningContractSource = readFileSync(join(repositoryRoot, "generated", "planning-contract.shared.ts"), "utf8");
const planningContractVersion = /PLANNING_CONTRACT_VERSION = "([^"]+)"/u.exec(planningContractSource)?.[1];
const planningContractSha256 = /PLANNING_CONTRACT_SHA256 = "([0-9a-f]{64})"/u.exec(planningContractSource)?.[1];
const projectAdminContractSource = readFileSync(join(repositoryRoot, "generated", "project-admin-mcp-contract.shared.ts"), "utf8");
const projectAdminContractVersion = /PROJECT_ADMIN_MCP_CONTRACT_VERSION = "([^"]+)"/u.exec(projectAdminContractSource)?.[1];
const projectAdminContractSha256 = /PROJECT_ADMIN_MCP_CONTRACT_SHA256 = "([0-9a-f]{64})"/u.exec(projectAdminContractSource)?.[1];
assert(planningContractVersion && planningContractSha256);
assert(projectAdminContractVersion && projectAdminContractSha256);
let diagnosticRoot = null;
let sentinelReadbackSequence = 0;
let projectAdminReadbackSequence = 0;

function createLifecycleClient(config) {
  const daemonClient = new DaemonClient({ ...config, clientType: "cli" });
  return {
    ...createPaseoApi(daemonClient),
    connect: () => daemonClient.connect(),
    close: () => daemonClient.close(),
    invokePluginRpc: (pluginId, method, input) => daemonClient.invokePluginRpc(pluginId, method, input),
  };
}

function sha256(value) {
  return createHash("sha256").update(value).digest("hex");
}

function run(executable, args, options = {}) {
  const result = spawnSync(executable, args, {
    cwd: options.cwd,
    encoding: "utf8",
    env: options.env,
    shell: false,
    timeout: options.timeout ?? 300_000,
    maxBuffer: maximumOutputBytes,
  });
  if (!options.allowFailure && (result.error || result.status !== 0)) {
    throw new Error(options.code ?? "DIRECTOR_MAIN_LIFECYCLE_COMMAND_FAILED");
  }
  return result;
}

function runAsync(executable, args, options = {}) {
  return new Promise((accept) => {
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
    const timer = setTimeout(() => child.kill("SIGKILL"), options.timeout ?? 300_000);
    child.once("close", (status, signal) => {
      clearTimeout(timer);
      accept({ status, signal, stdout, stderr });
    });
  });
}

function git(cwd, ...args) {
  return run("/usr/bin/git", ["-C", cwd, ...args], {
    env: { PATH: "/usr/bin:/bin", GIT_CONFIG_NOSYSTEM: "1" },
    code: "DIRECTOR_MAIN_LIFECYCLE_GIT_FAILED",
  }).stdout.trim();
}

function commit(cwd, message, paths) {
  git(cwd, "add", "--", ...paths);
  git(cwd, "commit", "-m", message);
  return git(cwd, "rev-parse", "HEAD");
}

function parseJSON(result) {
  try { return JSON.parse(result.stdout.trim()); }
  catch { throw new Error("DIRECTOR_MAIN_LIFECYCLE_PASEO_JSON"); }
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

async function listenerPresent(port) {
  return new Promise((accept) => {
    const socket = new Socket();
    const finish = (value) => { socket.destroy(); accept(value); };
    socket.setTimeout(250, () => finish(false));
    socket.once("connect", () => finish(true));
    socket.once("error", () => finish(false));
    socket.connect(port, "127.0.0.1");
  });
}

function isolatedNetworkRoute() {
  const routeResult = run("/usr/bin/ip", ["-json", "-4", "route", "get", "1.1.1.1"], {
    env: { PATH: "/usr/bin:/bin" },
    code: "DIRECTOR_MAIN_LIFECYCLE_NETWORK_ROUTE",
  });
  const route = JSON.parse(routeResult.stdout)?.[0];
  if (
    typeof route?.dev !== "string" || !/^[A-Za-z0-9_.-]{1,32}$/u.test(route.dev) ||
    typeof route?.prefsrc !== "string" || !/^(?:[0-9]{1,3}\.){3}[0-9]{1,3}$/u.test(route.prefsrc) ||
    typeof route?.gateway !== "string" || !/^(?:[0-9]{1,3}\.){3}[0-9]{1,3}$/u.test(route.gateway)
  ) throw new Error("DIRECTOR_MAIN_LIFECYCLE_NETWORK_ROUTE");
  const addressResult = run("/usr/bin/ip", ["-json", "-4", "address", "show", "dev", route.dev], {
    env: { PATH: "/usr/bin:/bin" },
    code: "DIRECTOR_MAIN_LIFECYCLE_NETWORK_ADDRESS",
  });
  const address = JSON.parse(addressResult.stdout)?.[0]?.addr_info?.find(
    (entry) => entry?.family === "inet" && entry?.local === route.prefsrc,
  );
  if (!Number.isSafeInteger(address?.prefixlen) || address.prefixlen < 1 || address.prefixlen > 32) {
    throw new Error("DIRECTOR_MAIN_LIFECYCLE_NETWORK_ADDRESS");
  }
  return { interfaceName: route.dev, address: route.prefsrc, prefix: address.prefixlen, gateway: route.gateway };
}

function configureIsolatedNetwork() {
  const interfaceName = process.env.DIR_M620_NETWORK_INTERFACE;
  const address = process.env.DIR_M620_NETWORK_ADDRESS;
  const prefix = Number(process.env.DIR_M620_NETWORK_PREFIX);
  const gateway = process.env.DIR_M620_NETWORK_GATEWAY;
  if (
    typeof interfaceName !== "string" || !/^[A-Za-z0-9_.-]{1,32}$/u.test(interfaceName) ||
    typeof address !== "string" || !/^(?:[0-9]{1,3}\.){3}[0-9]{1,3}$/u.test(address) ||
    !Number.isSafeInteger(prefix) || prefix < 1 || prefix > 32 ||
    typeof gateway !== "string" || !/^(?:[0-9]{1,3}\.){3}[0-9]{1,3}$/u.test(gateway)
  ) throw new Error("DIRECTOR_MAIN_LIFECYCLE_NETWORK_CONFIGURATION");
  run("/usr/bin/ip", ["link", "set", interfaceName, "up"], {
    env: { PATH: "/usr/bin:/bin" }, code: "DIRECTOR_MAIN_LIFECYCLE_NETWORK_CONFIGURATION",
  });
  run("/usr/bin/ip", ["address", "add", `${address}/${prefix}`, "dev", interfaceName], {
    env: { PATH: "/usr/bin:/bin" }, code: "DIRECTOR_MAIN_LIFECYCLE_NETWORK_CONFIGURATION",
  });
  run("/usr/bin/ip", ["route", "add", "default", "via", gateway], {
    env: { PATH: "/usr/bin:/bin" }, code: "DIRECTOR_MAIN_LIFECYCLE_NETWORK_CONFIGURATION",
  });
}

async function launchIsolatedLifecycle() {
  const route = isolatedNetworkRoute();
  const port = await reservePort();
  return new Promise((accept, reject) => {
    const child = spawn("/usr/bin/pasta", [
      "--quiet", "--foreground", "--ipv4-only",
      "--tcp-ports", `127.0.0.1/${port}`, "--udp-ports", "none",
      "--tcp-ns", "none", "--udp-ns", "none",
      process.execPath, fileURLToPath(import.meta.url),
    ], {
      env: {
        ...process.env,
        DIR_M620_ISOLATED_NETWORK: "1",
        DIR_M620_MAIN_PORT: String(port),
        DIR_M620_NETWORK_INTERFACE: route.interfaceName,
        DIR_M620_NETWORK_ADDRESS: route.address,
        DIR_M620_NETWORK_PREFIX: String(route.prefix),
        DIR_M620_NETWORK_GATEWAY: route.gateway,
      },
      shell: false,
      stdio: "inherit",
    });
    child.once("error", reject);
    child.once("close", (status, signal) => {
      if (signal !== null) reject(new Error("DIRECTOR_MAIN_LIFECYCLE_NETWORK_EXIT"));
      else accept(status ?? 1);
    });
  });
}

function daemonConfig(source) {
  return {
    version: 1,
    daemon: {
      mcp: { enabled: false, injectIntoAgents: false },
      browserTools: { enabled: false },
      relay: { enabled: false },
    },
    agents: {
      providers: {
        codex: {
          extends: "acp",
          label: "Director lifecycle sentinel",
          command: [process.execPath, join(source, "tools", "packaging", "sentinel-acp.mjs")],
          params: { supportsMcpServers: true },
          models: [{ id: "fixture", label: "Fixture", isDefault: true }],
        },
      },
      metadataGeneration: { providers: [] },
    },
    features: {
      dictation: { enabled: false },
      voiceMode: { enabled: false },
      webUi: { enabled: true },
    },
    pluginsEnabled: true,
  };
}

function startDaemon(home, port, environment, listenHost = "127.0.0.1") {
  const child = spawn(paseoExecutable, [
    "daemon", "start", "--home", home, "--listen", `${listenHost}:${port}`,
    "--foreground", "--no-relay", "--no-mcp", "--web-ui",
  ], { env: environment, shell: false, stdio: ["ignore", "pipe", "pipe"] });
  let output = "";
  const capture = (chunk) => { output = `${output}${chunk}`.slice(-maximumOutputBytes); };
  child.stdout.on("data", capture);
  child.stderr.on("data", capture);
  return { child, output: () => output };
}

async function paseo(port, environment, action, allowFailure = false) {
  const result = await runAsync(
    paseoExecutable,
    ["plugin", ...action, "--host", `127.0.0.1:${port}`, "--json"],
    { env: environment },
  );
  if (!allowFailure && result.status !== 0) throw new Error("DIRECTOR_MAIN_LIFECYCLE_PASEO_FAILED");
  return result;
}

async function waitForDaemon(port, daemon, environment) {
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    if (daemon.child.exitCode !== null || daemon.child.signalCode !== null) throw new Error("DIRECTOR_MAIN_LIFECYCLE_DAEMON_EXITED");
    const result = await runAsync(paseoExecutable, ["plugin", "ls", "--host", `127.0.0.1:${port}`, "--json"], {
      env: environment, timeout: 5_000,
    });
    if (result.status === 0) return;
    await new Promise((accept) => setTimeout(accept, 200));
  }
  throw new Error("DIRECTOR_MAIN_LIFECYCLE_DAEMON_TIMEOUT");
}

async function waitForStatus(client, candidate) {
  const deadline = Date.now() + 240_000;
  let lastCode = "DIRECTOR_MAIN_LIFECYCLE_STATUS_TIMEOUT";
  while (Date.now() < deadline) {
    try {
      const status = await client.invokePluginRpc("director", "director.startup-status", {});
      if (status?.engine?.sourceCandidate === candidate && status?.runtime?.supervisorState === "current") return status;
    } catch (error) {
      const candidateCode = error && typeof error === "object" ? Reflect.get(error, "code") : undefined;
      if (typeof candidateCode === "string" && /^[A-Z0-9_]{3,96}$/u.test(candidateCode)) lastCode = candidateCode;
    }
    await new Promise((accept) => setTimeout(accept, 500));
  }
  throw new Error(lastCode);
}

function processStart(pid) {
  const stat = readFileSync(`/proc/${pid}/stat`, "utf8");
  const fields = stat.slice(stat.lastIndexOf(")") + 2).split(" ");
  if (fields[0] === "Z") return "";
  return fields[19];
}

function processIdentity(pid) {
  const stat = readFileSync(`/proc/${pid}/stat`, "utf8");
  const fields = stat.slice(stat.lastIndexOf(")") + 2).split(" ");
  return {
    pid,
    parentPid: Number(fields[1]),
    start: fields[19],
    executable: readlinkSync(`/proc/${pid}/exe`),
    argv: readFileSync(`/proc/${pid}/cmdline`, "utf8").split("\0").filter(Boolean),
  };
}

function processAlive(identity) {
  try { return processStart(identity.pid) === identity.start; } catch { return false; }
}

async function waitForProcessExit(identity, timeout = 10_000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (!processAlive(identity)) return;
    await new Promise((accept) => setTimeout(accept, 50));
  }
  throw new Error("DIRECTOR_MAIN_LIFECYCLE_HANDOFF_TIMEOUT");
}

async function waitForAgent(agent) {
  const deadline = Date.now() + 240_000;
  while (Date.now() < deadline) {
    let current;
    try {
      current = (await agent.refresh())?.agent;
    } catch {
      current = agent.current();
    }
    if (current?.status === "running" && current.activeTurn?.turnId) return current;
    await new Promise((accept) => setTimeout(accept, 100));
  }
  throw new Error("DIRECTOR_MAIN_LIFECYCLE_SENTINEL_TIMEOUT");
}

async function waitForMarker(path, token) {
  const deadline = Date.now() + 240_000;
  while (Date.now() < deadline) {
    if (existsSync(path)) {
      const marker = JSON.parse(readFileSync(path, "utf8"));
      if (marker.token === token && Number.isSafeInteger(marker.pid)) {
        return { pid: marker.pid, processStart: processStart(marker.pid) };
      }
    }
    await new Promise((accept) => setTimeout(accept, 100));
  }
  throw new Error("DIRECTOR_MAIN_LIFECYCLE_SENTINEL_TIMEOUT");
}

async function createSentinels(client, root) {
  const readiness = await client.providers.waitForReady({ cwd: root, timeoutMs: 120_000, requestId: "main-lifecycle-providers" });
  assert.ok(readiness.entries.every((entry) => entry.status !== "loading"));
  const models = await client.providers.listModels("codex", { cwd: root, requestId: "main-lifecycle-models" });
  const model = models.models?.find((entry) => entry.isDefault) ?? models.models?.[0];
  assert(model);
  const handles = [];
  for (let index = 0; index < 2; index += 1) {
    const directory = join(root, `workspace-${index + 1}`);
    mkdirSync(directory, { mode: 0o700 });
    git(directory, "init", "--initial-branch=main");
    git(directory, "config", "user.name", "Director lifecycle sentinel");
    git(directory, "config", "user.email", "director-lifecycle@example.invalid");
    writeFileSync(join(directory, "README.md"), `sentinel ${index + 1}\n`, { mode: 0o600 });
    commit(directory, "fixture: seed sentinel workspace", ["README.md"]);
    git(directory, "remote", "add", "origin", `https://example.invalid/director-sentinel-${index + 1}.git`);
    const workspace = await client.workspaces.create({
      requestId: `main-lifecycle-workspace-${index + 1}`,
      title: `Unrelated sentinel workspace ${index + 1}`,
      source: { kind: "directory", path: directory },
    });
    const readyDeadline = Date.now() + 30_000;
    while (Date.now() < readyDeadline) {
      const state = await workspace.refresh();
      if (state?.status === "done" || state?.status === "attention") break;
      if (state?.status === "failed") throw new Error("DIRECTOR_MAIN_LIFECYCLE_SENTINEL_WORKSPACE");
      await new Promise((accept) => setTimeout(accept, 100));
    }
    const token = randomUUID();
    const marker = join(root, `sentinel-${index + 1}.json`);
    const agent = await workspace.agents.create({
      requestId: `main-lifecycle-agent-${index + 1}`,
      clientMessageId: `main-lifecycle-message-${index + 1}`,
      title: `Unrelated sentinel agent ${index + 1}`,
      labels: { fixture: "main-source-lifecycle", role: "unrelated-sentinel" },
      config: { provider: `codex/${model.id}` },
      prompt: `DIRECTOR_SENTINEL ${marker} ${token}`,
      autoArchive: false,
    });
    const [current, process] = await Promise.all([waitForAgent(agent), waitForMarker(marker, token)]);
    handles.push({ agent, identity: {
      agentId: current.id,
      workspaceId: current.workspaceId,
      turnId: current.activeTurn.turnId,
      startedAt: current.activeTurn.startedAt,
      pid: process.pid,
      processStart: process.processStart,
    } });
  }
  return handles;
}

async function assertSentinels(handles) {
  const readback = [];
  const snapshots = [];
  for (const handle of handles) {
    const current = handle.agent.current();
    if (!current) throw new Error("DIRECTOR_MAIN_LIFECYCLE_SENTINEL_MISSING");
    snapshots.push({ current, handle });
    readback.push({
      status: current.status,
      activeTurn: Boolean(current.activeTurn),
      workspaceMatched: current.workspaceId === handle.identity.workspaceId,
      turnMatched: current.activeTurn?.turnId === handle.identity.turnId,
      processMatched: processAlive({ pid: handle.identity.pid, start: handle.identity.processStart }),
    });
  }
  if (diagnosticRoot !== null) {
    sentinelReadbackSequence += 1;
    writeJSON(join(diagnosticRoot, `sentinel-readback-${sentinelReadbackSequence}.json`), readback);
  }
  for (const { current, handle } of snapshots) {
    assert.equal(current.id, handle.identity.agentId);
    assert.equal(current.workspaceId, handle.identity.workspaceId);
    assert.equal(current.status, "running");
    assert.equal(current.activeTurn?.turnId, handle.identity.turnId);
    assert.equal(processStart(handle.identity.pid), handle.identity.processStart);
  }
}

async function createProjectAdminSession(client, sourceAgent, hostIdentity, environment) {
  const native = await client.invokePluginRpc("director", "director.native-paseo-projects", {
    hostId: "untrusted-client-host",
  });
  assert.equal(native.hostId, hostIdentity);
  const matches = native.projects.filter((project) =>
    project.workspaces.some((workspace) => workspace.id === sourceAgent.identity.workspaceId));
  assert.equal(matches.length, 1);
  const nativeProject = matches[0];
  const requestId = "main-lifecycle-project-bootstrap";
  const input = {
    schemaVersion: 1,
    contractVersion: planningContractVersion,
    contractHash: planningContractSha256,
    hostId: "untrusted-client-host",
    requestId,
    kind: "native.create.preview",
    nativeProject,
    projectId: null,
    projectName: null,
    repositoryPath: null,
    configurationJson: null,
    previewId: null,
  };
  const preview = await client.invokePluginRpc("director", "director.organizer-bootstrap", input);
  assert.equal(preview.status, "preview");
  assert.equal(preview.hostId, hostIdentity);
  assert.equal(preview.preview?.valid, true);
  const applied = await client.invokePluginRpc("director", "director.organizer-bootstrap", {
    ...input,
    kind: "native.create.apply",
    previewId: preview.preview.id,
  });
  assert.equal(applied.status, "applied");
  assert.equal(applied.hostId, hostIdentity);

  const recreationRequest = "main-lifecycle-project-admin-session";
  const recreated = await client.invokePluginRpc("director", "director.recreate-project-admin-session", {
    requestId: recreationRequest,
    sourceWorkspaceId: sourceAgent.identity.workspaceId,
    sourceAgentId: sourceAgent.identity.agentId,
  });
  assert.equal(recreated.status, "created");
  const created = (await client.agents.ref(recreated.agentId).refresh())?.agent;
  assert.equal(created?.workspaceId, sourceAgent.identity.workspaceId);
  assert.equal(created?.labels?.["director.project-admin-session"]?.startsWith("admin-session-"), true);

  const authorization = readFileSync(join(
    environment.XDG_CONFIG_HOME,
    "director",
    "managed-runtime",
    "project-admin.token",
  ), "utf8").trim();
  assert.match(authorization, /^[0-9a-f]{64}$/u);
  const sessionDigest = sha256(`${authorization}\x1f${recreationRequest}\x1f${sourceAgent.identity.workspaceId}\x1f${sourceAgent.identity.agentId}`);
  return {
    projectId: preview.preview.projectId,
    sessionId: `admin-session-${sessionDigest.slice(0, 32)}`,
    audience: `paseo-session-${sessionDigest.slice(32)}`,
    token: sha256(`${authorization}\x1fproject-admin-token\x1f${sessionDigest}`),
    nativeAgentId: recreated.agentId,
  };
}

async function verifyProjectAdminSession(session) {
  const request = async (body) => {
    const response = await fetch("http://127.0.0.1:7041/v1/project-admin-mcp", {
      method: "POST",
      redirect: "error",
      headers: {
        authorization: `Bearer ${session.token}`,
        "content-type": "application/json",
        "x-director-admin-session": session.sessionId,
        "x-director-admin-audience": session.audience,
        "x-director-contract-version": projectAdminContractVersion,
        "x-director-contract-sha256": projectAdminContractSha256,
      },
      body: JSON.stringify(body),
    });
    assert.equal(response.status, 200);
    assert.equal(response.headers.get("x-director-contract-version"), projectAdminContractVersion);
    assert.equal(response.headers.get("x-director-contract-sha256"), projectAdminContractSha256);
    return response.json();
  };
  const catalog = await request({ jsonrpc: "2.0", id: "catalog", method: "tools/list" });
  assert.equal(catalog.result?.tools?.length, 6);
  assert.deepEqual(catalog.result.tools.map((tool) => tool.name), [
    "director_admin_project_read",
    "director_admin_planning_read",
    "director_admin_execution_read",
    "director_admin_planning_command",
    "director_admin_control_command",
    "director_admin_diagnostics_read",
  ]);
  const project = await request({
    jsonrpc: "2.0",
    id: "project-read",
    method: "tools/call",
    params: { name: "director_admin_project_read", arguments: {} },
  });
  assert.equal(project.result?.isError, false);
  const projection = JSON.parse(project.result.content[0].text);
  if (diagnosticRoot !== null) {
    projectAdminReadbackSequence += 1;
    writeJSON(join(diagnosticRoot, `project-admin-readback-${projectAdminReadbackSequence}.json`), projection);
  }
  assert.equal(projection.project?.id, session.projectId);
  assert.equal(projection.scope?.kind, "own-project");
  const serialized = JSON.stringify(projection);
  assert.equal(/(?:\/tmp\/|credential|token|nativeAgentId|nativeWorkspaceId|repositoryPath|sourcePath)/iu.test(serialized), false);
  return { toolCount: catalog.result.tools.length, projectVersion: projection.project.version };
}

function mainCacheEntry(cacheBase, candidate) {
  const root = join(cacheBase, "director", "engines", "main", candidate, "linux-amd64");
  const contracts = readdirSync(root, { withFileTypes: true }).filter((entry) => entry.isDirectory() && !entry.isSymbolicLink());
  assert.equal(contracts.length, 1);
  const contractRoot = join(root, contracts[0].name);
  const metadataPath = join(contractRoot, "prepared.json");
  const metadata = JSON.parse(readFileSync(metadataPath, "utf8"));
  return { contractRoot, metadataPath, metadata, mtimeMs: statSync(metadataPath).mtimeMs };
}

function buildRecords(output) {
  const buildOutput = output.split("\n").flatMap((line) => {
    try {
      const value = JSON.parse(line);
      return typeof value?.output === "string" ? [value.output] : [];
    } catch { return []; }
  }).join("\n");
  return buildOutput.split("\n").flatMap((line) => {
    const start = line.indexOf('{"code":"DIRECTOR_INSTALL_CANDIDATE_READY"');
    if (start < 0) return [];
    try {
      const value = JSON.parse(line.slice(start));
      if (value.code !== "DIRECTOR_INSTALL_CANDIDATE_READY") return [];
      return [{
        channel: value.channel,
        metadataState: value.metadataState,
        bootstrapCacheState: value.bootstrapCacheState,
        bootstrapCompilerInvocations: value.bootstrapCompilerInvocations,
        engineCompilerInvocations: value.engineCompilerInvocations,
      }];
    } catch { return []; }
  });
}

function writePrivate(path, value) {
  const descriptor = openSync(path, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600);
  try { writeFileSync(descriptor, value); fsyncSync(descriptor); } finally { closeSync(descriptor); }
  chmodSync(path, 0o600);
}

function writeJSON(path, value) {
  writePrivate(path, `${JSON.stringify(value, null, 2)}\n`);
}

function sourceRepository(destination, candidate) {
  run("/usr/bin/git", ["clone", "--quiet", "--no-hardlinks", repositoryRoot, destination], {
    env: { PATH: "/usr/bin:/bin", GIT_CONFIG_NOSYSTEM: "1" },
    code: "DIRECTOR_MAIN_LIFECYCLE_CLONE_FAILED",
  });
  git(destination, "checkout", "-B", "main", candidate);
  git(destination, "config", "user.name", "Director main lifecycle fixture");
  git(destination, "config", "user.email", "director-main-lifecycle@example.invalid");
  assert.equal(git(destination, "status", "--porcelain=v1"), "");
}

function environmentFor(root, bin, password, moduleCache, suffix = "main") {
  const environment = {
    ...process.env,
    HOME: join(root, `${suffix}-home`),
    PATH: `${bin}:/usr/bin:/bin`,
    GOMODCACHE: moduleCache,
    PASEO_PASSWORD: password,
    PASEO_DICTATION_ENABLED: "false",
    PASEO_VOICE_MODE_ENABLED: "false",
    PASEO_LOG_LEVEL: "info",
    XDG_CONFIG_HOME: join(root, `${suffix}-config`),
    XDG_CACHE_HOME: join(root, `${suffix}-cache`),
    XDG_RUNTIME_DIR: join(root, `${suffix}-runtime`),
    XDG_DATA_HOME: join(root, `${suffix}-data`),
  };
  for (const key of Object.keys(environment)) {
    if (key.startsWith("DIRECTOR_")) delete environment[key];
  }
  for (const path of [environment.HOME, environment.XDG_CONFIG_HOME, environment.XDG_CACHE_HOME,
    environment.XDG_RUNTIME_DIR, environment.XDG_DATA_HOME]) mkdirSync(path, { recursive: true, mode: 0o700 });
  return environment;
}

export async function runMainSourceLifecycle() {
  if (process.env.DIR_M620_ISOLATED_NETWORK === "1") configureIsolatedNetwork();
  const candidate = git(repositoryRoot, "rev-parse", "HEAD");
  assert.equal(git(repositoryRoot, "status", "--porcelain=v1"), "", "exact Candidate lifecycle requires a clean worktree");
  assert.equal(await listenerPresent(3307), false, "disposable Dolt port is already owned");
  assert.equal(await listenerPresent(7041), false, "disposable Engine port is already owned");
  const root = mkdtempSync(join(tmpdir(), `dir-m6.20-${candidate.slice(0, 12)}-`));
  diagnosticRoot = root;
  const bin = join(root, "bin");
  const source = join(root, "main-source");
  const paseoHome = join(root, "paseo-main");
  const sentinelRoot = join(root, "sentinels");
  for (const path of [bin, paseoHome, sentinelRoot]) mkdirSync(path, { recursive: true, mode: 0o700 });
  symlinkSync(paseoExecutable, join(bin, "paseo"));
  sourceRepository(source, candidate);
  const moduleCache = run("/usr/local/go/bin/go", ["env", "GOMODCACHE"], { env: process.env }).stdout.trim();
  const passwordPath = join(root, "paseo-main.password");
  const password = `${randomUUID()}${randomUUID()}`;
  writePrivate(passwordPath, `${password}\n`);
  const environment = environmentFor(root, bin, password, moduleCache);
  writeJSON(join(paseoHome, "config.json"), daemonConfig(source));
  const port = process.env.DIR_M620_ISOLATED_NETWORK === "1"
    ? Number(process.env.DIR_M620_MAIN_PORT)
    : await reservePort();
  assert.ok(Number.isSafeInteger(port) && port > 0 && port <= 65_535);
  const daemon = startDaemon(
    paseoHome,
    port,
    environment,
    process.env.DIR_M620_ISOLATED_NETWORK === "1" ? "0.0.0.0" : "127.0.0.1",
  );
  await waitForDaemon(port, daemon, environment);
  const daemonIdentity = { pid: daemon.child.pid, start: processStart(daemon.child.pid) };
  const clientOptions = {
    url: `ws://127.0.0.1:${port}/ws`, password,
    clientId: "director-main-source-lifecycle", reconnect: { enabled: false },
  };
  const client = createLifecycleClient(clientOptions);
  await client.connect();
  const sentinels = await createSentinels(client, sentinelRoot);

  const added = parseJSON(await paseo(port, environment, ["add", `file://${source}`]));
  assert.equal(added.commit, candidate);
  const first = await waitForStatus(client, candidate);
  assert.equal(first.engineMode, "main");
  assert.equal(first.engine.mode, "main");
  assert.equal(first.engine.connectorCommit, candidate);
  assert.equal(first.engine.sourceCandidate, candidate);
  assert.match(first.host.id, /^director-[0-9a-f]{32}$/u);
  assert.equal(first.host.label, "Director");
  assert.equal(first.runtime.doltVersion, "2.3.2");
  assert.equal(first.runtime.doltBinarySha256, "eb50b0e7c4ce2303486deaf850f9dc5630ffcfd8d9aa4a40a2b3e0b23e2ac2f0");
  const firstRuntimeState = JSON.parse(readFileSync(join(environment.XDG_RUNTIME_DIR, "director", "supervisor", "state.json"), "utf8"));
  const firstProcesses = {
    bootstrap: processIdentity(firstRuntimeState.pid),
    engine: processIdentity(firstRuntimeState.enginePid),
    dolt: processIdentity(firstRuntimeState.doltPid),
  };
  assert.notEqual(firstProcesses.engine.pid, firstProcesses.dolt.pid);
  assert.equal(firstProcesses.engine.parentPid, firstProcesses.bootstrap.pid);
  assert.equal(firstProcesses.dolt.parentPid, firstProcesses.bootstrap.pid);
  assert.equal(firstProcesses.bootstrap.argv[1], "runtime");
  assert.equal(firstProcesses.engine.argv[1], "serve-board");
  assert.equal(firstProcesses.dolt.argv[1], "sql-server");
  const firstCache = mainCacheEntry(environment.XDG_CACHE_HOME, candidate);
  assert.equal(firstCache.metadata.binary.sha256, first.engine.binarySha256);
  assert.equal(resolve(firstProcesses.engine.executable), resolve(join(firstCache.contractRoot, first.engine.binarySha256, "director-engine")));
  const doltPath = join(environment.XDG_CACHE_HOME, "director", "engines", "dolt", "2.3.2", "linux-amd64", first.runtime.doltBinarySha256, "dolt");
  assert.equal(resolve(firstProcesses.dolt.executable), resolve(doltPath));
  const taskstorePath = join(environment.XDG_CONFIG_HOME, "director", "managed-runtime", "taskstore.json");
  const taskstoreBefore = readFileSync(taskstorePath);
  assert.equal(JSON.parse(taskstoreBefore).storeId, first.host.id);
  const home = await client.invokePluginRpc("director", "director.home-query", { hostId: "local-paseo", cursor: null, pageSize: 25 });
  assert.equal(home?.page?.host?.id, first.host.id);
  const directHeaders = {
    "content-type": "application/json",
    "x-director-contract-version": planningContractVersion,
    "x-director-contract-hash": planningContractSha256,
  };
  const mismatchedHome = await fetch("http://127.0.0.1:7041/v1/planning/home", {
    method: "POST", headers: directHeaders,
    body: JSON.stringify({ hostId: "local-paseo", cursor: null, pageSize: 25 }),
  });
  assert.equal(mismatchedHome.status, 409);
  assert.equal((await mismatchedHome.json()).code, "HOME_HOST_MISMATCH");
  const correctedHome = await fetch("http://127.0.0.1:7041/v1/planning/home", {
    method: "POST", headers: directHeaders,
    body: JSON.stringify({ hostId: first.host.id, cursor: null, pageSize: 25 }),
  });
  assert.equal(correctedHome.status, 200);
  await assertSentinels(sentinels);
  const projectAdmin = await createProjectAdminSession(client, sentinels[0], first.host.id, environment);
  const projectAdminBeforeReload = await verifyProjectAdminSession(projectAdmin);
  const replayedStatuses = await Promise.all(Array.from({ length: 4 }, () => client.invokePluginRpc("director", "director.startup-status", {})));
  assert.ok(replayedStatuses.every((status) => status.runtime.supervisorBinding === first.runtime.supervisorBinding));
  const replayedState = JSON.parse(readFileSync(join(environment.XDG_RUNTIME_DIR, "director", "supervisor", "state.json"), "utf8"));
  assert.deepEqual([replayedState.pid, replayedState.enginePid, replayedState.doltPid], [firstProcesses.bootstrap.pid, firstProcesses.engine.pid, firstProcesses.dolt.pid]);
  const addRecords = buildRecords(daemon.output());
  assert.equal(addRecords.filter((record) => record.channel === "main" && record.bootstrapCompilerInvocations === 1 && record.engineCompilerInvocations === 1).length, 1);

  for (let index = 0; index < 2; index += 1) {
    parseJSON(await paseo(port, environment, ["reload", "director"]));
    await waitForStatus(client, candidate);
    await verifyProjectAdminSession(projectAdmin);
    const reloadedState = JSON.parse(readFileSync(join(environment.XDG_RUNTIME_DIR, "director", "supervisor", "state.json"), "utf8"));
    assert.deepEqual([reloadedState.pid, reloadedState.enginePid, reloadedState.doltPid], [firstProcesses.bootstrap.pid, firstProcesses.engine.pid, firstProcesses.dolt.pid]);
  }
  assert.equal(buildRecords(daemon.output()).filter((record) => record.channel === "main").length, 1);
  assert.equal(mainCacheEntry(environment.XDG_CACHE_HOME, candidate).mtimeMs, firstCache.mtimeMs);
  assert.equal(processStart(daemonIdentity.pid), daemonIdentity.start);
  await assertSentinels(sentinels);

  writeFileSync(join(source, "release", "main-update-probe.txt"), "exact main update\n", { mode: 0o600 });
  const updatedCandidate = commit(source, "fixture: exact main update", ["release/main-update-probe.txt"]);
  const updateResult = parseJSON(await paseo(port, environment, ["update", "director"]));
  assert.equal(updateResult[0].updated, true);
  assert.equal(updateResult[0].currentCommit, updatedCandidate);
  const updated = await waitForStatus(client, updatedCandidate);
  assert.equal(updated.host.id, first.host.id);
  assert.notEqual(updated.engine.binarySha256, first.engine.binarySha256);
  assert.notEqual(updated.runtime.supervisorBinding, first.runtime.supervisorBinding);
  const updatedState = JSON.parse(readFileSync(join(environment.XDG_RUNTIME_DIR, "director", "supervisor", "state.json"), "utf8"));
  const updatedProcesses = { bootstrap: processIdentity(updatedState.pid), engine: processIdentity(updatedState.enginePid), dolt: processIdentity(updatedState.doltPid) };
  assert.notEqual(updatedProcesses.engine.pid, updatedProcesses.dolt.pid);
  assert.equal(updatedProcesses.engine.parentPid, updatedProcesses.bootstrap.pid);
  assert.equal(updatedProcesses.dolt.parentPid, updatedProcesses.bootstrap.pid);
  await Promise.all([
    waitForProcessExit(firstProcesses.engine),
    waitForProcessExit(firstProcesses.dolt),
    waitForProcessExit(firstProcesses.bootstrap),
  ]);
  assert.equal(processAlive(firstProcesses.engine), false);
  assert.equal(processAlive(firstProcesses.dolt), false);
  assert.equal(processAlive(firstProcesses.bootstrap), false);
  assert.equal(readFileSync(taskstorePath).equals(taskstoreBefore), true);
  await assertSentinels(sentinels);
  const projectAdminAfterUpdate = await verifyProjectAdminSession(projectAdmin);
  const updatedRecords = buildRecords(daemon.output());
  assert.equal(updatedRecords.filter((record) => record.channel === "main" && record.bootstrapCompilerInvocations === 1 && record.engineCompilerInvocations === 1).length, 2);
  assert.equal(mainCacheEntry(environment.XDG_CACHE_HOME, updatedCandidate).metadata.sourceCandidate, updatedCandidate);

  const failurePath = join(source, "engine", "cmd", "director-engine", "dir_m6_20_failure.go");
  writeFileSync(failurePath, "package main\nfunc dirM620Failure( {\n", { mode: 0o600 });
  commit(source, "fixture: compiler failure", ["engine/cmd/director-engine/dir_m6_20_failure.go"]);
  const failed = await paseo(port, environment, ["update", "director"], true);
  assert.notEqual(failed.status, 0);
  assert.match(`${failed.stdout}\n${failed.stderr}\n${daemon.output()}`, /DIRECTOR_MAIN_BUILD_FAILED/u);
  const installedAfterFailure = JSON.parse(readFileSync(join(paseoHome, "plugins", "sources.json"), "utf8")).director;
  assert.equal(installedAfterFailure.commit, updatedCandidate);
  const afterFailure = await waitForStatus(client, updatedCandidate);
  assert.equal(afterFailure.runtime.supervisorBinding, updated.runtime.supervisorBinding);
  assert.equal(readFileSync(taskstorePath).equals(taskstoreBefore), true);
  await assertSentinels(sentinels);

  const evidence = {
    schemaVersion: 1,
    candidate,
    updatedCandidate,
    paseoVersion: run(paseoExecutable, ["--version"], { env: environment }).stdout.trim(),
    target: "linux-amd64",
    fixtureRoot: root,
    main: {
      daemonPid: daemonIdentity.pid,
      daemonStart: daemonIdentity.start,
      daemonPort: port,
      webUiUrl: `http://127.0.0.1:${port}`,
      passwordFile: passwordPath,
      compilerInvocations: {
        add: { bootstrap: 1, engine: 1 },
        reload: { bootstrap: 0, engine: 0 },
        update: { bootstrap: 1, engine: 1 },
      },
      fixedGoOutsideDaemonPath: !environment.PATH.split(":").includes("/usr/local/go/bin"),
      bootstrap: { firstPid: firstProcesses.bootstrap.pid, updatedPid: updatedProcesses.bootstrap.pid, ownsRuntimeChildren: true },
      engine: { firstPid: firstProcesses.engine.pid, updatedPid: updatedProcesses.engine.pid, firstDigest: first.engine.binarySha256, updatedDigest: updated.engine.binarySha256, parentIsBootstrap: true },
      dolt: { firstPid: firstProcesses.dolt.pid, updatedPid: updatedProcesses.dolt.pid, version: updated.runtime.doltVersion, binarySha256: updated.runtime.doltBinarySha256, archiveSha256: updated.runtime.doltArchiveSha256, parentIsBootstrap: true },
      hostIdentitySha256: sha256(first.host.id),
      homeHttp: { literalMismatchStatus: mismatchedHome.status, correctedStatus: correctedHome.status },
      taskstoreIdentityMatched: JSON.parse(taskstoreBefore).storeId === first.host.id,
      taskstoreSha256: sha256(taskstoreBefore),
      projectAdmin: {
        projectSha256: sha256(projectAdmin.projectId),
        sessionSha256: sha256(projectAdmin.sessionId),
        nativeAgentSha256: sha256(projectAdmin.nativeAgentId),
        toolCount: projectAdminAfterUpdate.toolCount,
        beforeReloadVersion: projectAdminBeforeReload.projectVersion,
        afterUpdateVersion: projectAdminAfterUpdate.projectVersion,
        survivedReloadAndUpdate: true,
      },
      sentinelsPreserved: true,
      daemonRestarted: false,
      failedCompilerUpdatePreserved: true,
      connectorStatus: "running",
    },
    release: { coveredBy: "release-runtime-lifecycle plus zero-compiler packaging regressions" },
    prohibited: { systemd: false, secondaryProductPaseoConnection: false, paseoRestart: false, machineRestart: false, runtimeJson: false, sourceRoot: false },
    isolation: { userNetworkNamespace: process.env.DIR_M620_ISOLATED_NETWORK === "1", hostRuntimePortsTouched: false, forwardedHostPort: "Paseo UI only" },
    resourcesPreserved: true,
  };
  const evidencePath = join(root, "real-paseo-main-lifecycle.json");
  writeJSON(evidencePath, evidence);
  const screenshotManifest = join(root, "screenshot-manifest.json");
  writeJSON(screenshotManifest, {
    schemaVersion: 1,
    status: "pending",
    reason: "Real rendered screenshots are captured after lifecycle readback.",
    webUiUrl: evidence.main.webUiUrl,
    requiredStates: [
      "Director Home ready identity copy",
      "selector-first Create Project modal",
      "five-command catalog with Project administration",
    ],
    screenshots: [],
  });
  return { evidencePath, screenshotManifest, root, webUiUrl: evidence.main.webUiUrl, passwordPath };
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))) {
  const isolated = process.env.DIR_M620_ISOLATED_NETWORK === "1";
  const lifecycle = isolated ? runMainSourceLifecycle() : launchIsolatedLifecycle();
  lifecycle.then(
    async (result) => {
      if (!isolated) {
        process.exitCode = Number(result);
        return;
      }
      process.stdout.write(`${JSON.stringify(result)}\n`);
      await new Promise(() => undefined);
    },
    (error) => {
      const code = error instanceof Error && /^[A-Z0-9_]{3,96}$/u.test(error.message)
        ? error.message : "DIRECTOR_MAIN_LIFECYCLE_FAILED";
      if (diagnosticRoot !== null) {
        try {
          writePrivate(join(diagnosticRoot, "failure.txt"), `${error instanceof Error ? error.stack : code}\n`);
        } catch {
          // Keep the public failure bounded even when private diagnostics cannot be written.
        }
      }
      process.stderr.write(`${code}\n`);
      process.exitCode = 1;
    },
  );
}
