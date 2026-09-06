#!/usr/bin/env node

import { spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import {
  access,
  chmod,
  copyFile,
  lstat,
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  realpath,
  rm,
  writeFile,
} from "node:fs/promises";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

if (process.argv.length !== 2) {
  throw new Error("Usage: node docs/evidence/m0.3/reproduce-matrix.mjs");
}
if (process.platform !== "linux" || process.arch !== "x64") {
  throw new Error("This bounded evidence procedure supports Linux x86_64 only");
}

const evidenceRoot = path.dirname(fileURLToPath(import.meta.url));
const checkoutRoot = path.resolve(evidenceRoot, "../../..");
const runMatrixPath = path.join(evidenceRoot, "run-matrix.mjs");
const probeMcpPath = path.join(evidenceRoot, "probe-mcp.mjs");
const probeAcpPath = path.join(evidenceRoot, "probe-acp.mjs");
const configTemplatePath = path.join(evidenceRoot, "paseo-config.example.json");
const ownerNonce = randomUUID();
const markerName = ".director-m0.3-owned.json";
const port = parsePort(process.env.DIRECTOR_MATRIX_PORT ?? "16783");
const listen = `127.0.0.1:${port}`;
const paseoUrl = `ws://${listen}/ws`;
const expectedVersions = {
  paseo: "0.7.2",
  codex: "codex-cli 0.147.0",
  claude: "2.1.258 (Claude Code)",
  opencode: "1.18.18",
};

let runtimeRoot;
let paseoHome;
let paseoBin;
let clientEntry;
let daemonPid = null;
let daemonMayBeRunning = false;
let runtimeRemoved = false;

function parsePort(value) {
  const parsed = Number(value);
  if (!Number.isInteger(parsed) || parsed < 1024 || parsed > 65535) {
    throw new Error("DIRECTOR_MATRIX_PORT must be an integer from 1024 through 65535");
  }
  return parsed;
}

function delay(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

function sanitizeDiagnostic(value) {
  let sanitized = value
    .replaceAll(checkoutRoot, "<checkout>")
    .replaceAll(os.homedir(), "<user-home>")
    .replace(/https?:\/\/\S+/gu, "[url redacted]")
    .replace(/\bBearer\s+\S+/giu, "Bearer [redacted]")
    .replace(/\b(?:sk|sess|token)-[A-Za-z0-9_-]{8,}\b/gu, "[token redacted]");
  if (runtimeRoot) sanitized = sanitized.replaceAll(runtimeRoot, "<runtime>");
  return sanitized.trim().slice(-2_000);
}

async function exists(filePath) {
  try {
    await access(filePath);
    return true;
  } catch (cause) {
    if (cause && typeof cause === "object" && cause.code === "ENOENT") return false;
    throw cause;
  }
}

async function resolveExecutable(name) {
  const candidates = path.isAbsolute(name)
    ? [name]
    : (process.env.PATH ?? "")
        .split(path.delimiter)
        .filter(Boolean)
        .map((directory) => path.join(directory, name));
  for (const candidate of candidates) {
    try {
      await access(candidate, 1);
      return await realpath(candidate);
    } catch (cause) {
      if (cause && typeof cause === "object" && ["EACCES", "ENOENT"].includes(cause.code)) {
        continue;
      }
      throw cause;
    }
  }
  throw new Error(`Required executable is unavailable: ${name}`);
}

function runCommand(executable, args, options = {}) {
  const timeoutMs = options.timeoutMs ?? 60_000;
  const outputLimit = options.outputLimit ?? 4 * 1024 * 1024;
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {
      cwd: options.cwd ?? checkoutRoot,
      env: options.env ?? process.env,
      shell: false,
      stdio: ["ignore", "pipe", "pipe"],
    });
    const stdout = [];
    const stderr = [];
    let outputBytes = 0;
    let timedOut = false;
    let settled = false;
    const timer = setTimeout(() => {
      timedOut = true;
      child.kill("SIGTERM");
    }, timeoutMs);

    function collect(target, chunk) {
      outputBytes += chunk.length;
      if (outputBytes > outputLimit) {
        child.kill("SIGTERM");
        return;
      }
      target.push(chunk);
    }

    child.stdout.on("data", (chunk) => collect(stdout, chunk));
    child.stderr.on("data", (chunk) => collect(stderr, chunk));
    child.once("error", (cause) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      reject(cause);
    });
    child.once("close", (code, signal) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      if (timedOut) {
        reject(new Error(`Command timed out: ${path.basename(executable)}`));
        return;
      }
      if (outputBytes > outputLimit) {
        reject(new Error(`Command exceeded its output limit: ${path.basename(executable)}`));
        return;
      }
      if (code !== 0) {
        const diagnostic = sanitizeDiagnostic(Buffer.concat(stderr).toString("utf8"));
        reject(
          new Error(
            `Command failed: ${options.label ?? path.basename(executable)} ` +
              `(exit=${code ?? "null"}, signal=${signal ?? "none"})` +
              (diagnostic ? `\n${diagnostic}` : ""),
          ),
        );
        return;
      }
      resolve({
        stdout: Buffer.concat(stdout).toString("utf8"),
        stderr: Buffer.concat(stderr).toString("utf8"),
      });
    });
  });
}

async function runJsonCommand(executable, args, options) {
  const result = await runCommand(executable, args, options);
  try {
    return JSON.parse(result.stdout);
  } catch {
    throw new Error(`Command did not return one JSON document: ${path.basename(executable)}`);
  }
}

async function commandVersion(executable) {
  return (await runCommand(executable, ["--version"])).stdout.trim();
}

async function assertPrivateRegularFile(filePath) {
  const file = await lstat(filePath);
  if (!file.isFile()) throw new Error("A provider authentication source is not a regular file");
  if ((file.mode & 0o077) !== 0) {
    throw new Error("A provider authentication source grants group or other access");
  }
}

async function copyPrivateFile(source, destination) {
  await assertPrivateRegularFile(source);
  await mkdir(path.dirname(destination), { recursive: true, mode: 0o700 });
  await copyFile(source, destination);
  await chmod(destination, 0o600);
}

async function prepareProviderHome(row) {
  const providerHome = path.join(runtimeRoot, "provider-homes", row);
  if (await exists(providerHome)) throw new Error(`Provider home already exists for ${row}`);
  await mkdir(providerHome, { recursive: true, mode: 0o700 });
  const sourceHome = os.homedir();
  if (row === "codex") {
    await copyPrivateFile(
      process.env.DIRECTOR_CODEX_AUTH_SOURCE ?? path.join(sourceHome, ".codex", "auth.json"),
      path.join(providerHome, ".codex", "auth.json"),
    );
  } else if (row === "claude") {
    await copyPrivateFile(
      process.env.DIRECTOR_CLAUDE_AUTH_SOURCE ??
        path.join(sourceHome, ".claude", ".credentials.json"),
      path.join(providerHome, ".claude", ".credentials.json"),
    );
    await writeFile(
      path.join(providerHome, ".claude.json"),
      `${JSON.stringify({ hasCompletedOnboarding: true, installMethod: "native", projects: {} }, null, 2)}\n`,
      { encoding: "utf8", mode: 0o600 },
    );
  } else if (row === "opencode-billing-rejected" || row === "opencode") {
    await copyPrivateFile(
      process.env.DIRECTOR_OPENCODE_AUTH_SOURCE ??
        path.join(sourceHome, ".local", "share", "opencode", "auth.json"),
      path.join(providerHome, ".local", "share", "opencode", "auth.json"),
    );
  }
  return providerHome;
}

async function prepareConfig() {
  const config = JSON.parse(await readFile(configTemplatePath, "utf8"));
  for (const provider of ["probe-acp", "probe-acp-no-mcp"]) {
    config.agents.providers[provider].command[0] = process.execPath;
    config.agents.providers[provider].command[1] = probeAcpPath;
  }
  await writeFile(path.join(paseoHome, "config.json"), `${JSON.stringify(config, null, 2)}\n`, {
    encoding: "utf8",
    mode: 0o600,
  });
}

async function prepareWorkspace(gitBin) {
  const workspace = path.join(runtimeRoot, "workspace");
  await mkdir(workspace, { mode: 0o700 });
  await writeFile(path.join(workspace, "README.md"), "# Director M0.3 disposable probe\n", {
    encoding: "utf8",
    mode: 0o600,
  });
  const gitEnv = {
    ...process.env,
    HOME: path.join(runtimeRoot, "git-home"),
    GIT_CONFIG_NOSYSTEM: "1",
    GIT_TERMINAL_PROMPT: "0",
  };
  await mkdir(gitEnv.HOME, { mode: 0o700 });
  await runCommand(gitBin, ["init", "--initial-branch=main", workspace], { env: gitEnv });
  await runCommand(gitBin, ["-C", workspace, "add", "README.md"], { env: gitEnv });
  await runCommand(
    gitBin,
    [
      "-C",
      workspace,
      "-c",
      "commit.gpgsign=false",
      "-c",
      "user.name=Director M0.3",
      "-c",
      "user.email=director-m0.3.invalid",
      "commit",
      "-m",
      "chore: initialize disposable probe",
    ],
    { env: gitEnv },
  );
  const remotes = (await runCommand(gitBin, ["-C", workspace, "remote"], { env: gitEnv })).stdout;
  const dirty = (await runCommand(gitBin, ["-C", workspace, "status", "--porcelain"], {
    env: gitEnv,
  })).stdout;
  if (remotes !== "" || dirty !== "") {
    throw new Error("Disposable repository has a remote or a dirty worktree");
  }
}

function listenerAcceptsConnections() {
  return new Promise((resolve, reject) => {
    const socket = net.createConnection({ host: "127.0.0.1", port });
    const timer = setTimeout(() => {
      socket.destroy();
      resolve(false);
    }, 750);
    socket.once("connect", () => {
      clearTimeout(timer);
      socket.destroy();
      resolve(true);
    });
    socket.once("error", (cause) => {
      clearTimeout(timer);
      socket.destroy();
      if (cause && typeof cause === "object" && cause.code === "ECONNREFUSED") {
        resolve(false);
      } else {
        reject(cause);
      }
    });
  });
}

async function waitForListener(expected, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if ((await listenerAcceptsConnections()) === expected) return;
    await delay(200);
  }
  throw new Error(`Listener ${listen} did not become ${expected ? "reachable" : "closed"}`);
}

function processIsAlive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch (cause) {
    if (cause && typeof cause === "object" && cause.code === "ESRCH") return false;
    throw cause;
  }
}

async function assertDaemonIdentity(pid) {
  const [commandLine, environment] = await Promise.all([
    readFile(`/proc/${pid}/cmdline`),
    readFile(`/proc/${pid}/environ`),
  ]);
  const runtimeBytes = Buffer.from(runtimeRoot);
  const homeEntry = Buffer.from(`PASEO_HOME=${paseoHome}\0`);
  if (!commandLine.includes(runtimeBytes) && !environment.includes(runtimeBytes)) {
    throw new Error("Daemon owner process does not reference the owned runtime");
  }
  if (!environment.includes(homeEntry)) {
    throw new Error("Daemon owner process does not have the exact isolated PASEO_HOME");
  }
}

async function runtimeProcessIds() {
  const matches = [];
  const needle = Buffer.from(runtimeRoot);
  for (const entry of await readdir("/proc", { withFileTypes: true })) {
    if (!entry.isDirectory() || !/^\d+$/.test(entry.name)) continue;
    const pid = Number(entry.name);
    if (pid === process.pid) continue;
    try {
      const [commandLine, environment] = await Promise.all([
        readFile(`/proc/${pid}/cmdline`),
        readFile(`/proc/${pid}/environ`),
      ]);
      if (commandLine.includes(needle) || environment.includes(needle)) matches.push(pid);
    } catch (cause) {
      if (
        cause &&
        typeof cause === "object" &&
        ["EACCES", "ENOENT", "EPERM", "ESRCH"].includes(cause.code)
      ) {
        continue;
      }
      throw cause;
    }
  }
  return matches.sort((left, right) => left - right);
}

async function runtimeProcessDetails(pids) {
  const details = [];
  for (const pid of pids) {
    const [commandLine, environment, statusText, executablePath] = await Promise.all([
      readFile(`/proc/${pid}/cmdline`),
      readFile(`/proc/${pid}/environ`),
      readFile(`/proc/${pid}/status`, "utf8"),
      realpath(`/proc/${pid}/exe`),
    ]);
    const commandTokens = commandLine
      .toString("utf8")
      .split("\0")
      .filter(Boolean);
    let kind = "runtime-associated";
    if (pid === daemonPid) kind = "daemon-owner";
    else if (commandLine.includes(Buffer.from(probeMcpPath))) kind = "mcp-probe";
    else if (commandLine.includes(Buffer.from(probeAcpPath))) kind = "acp-probe";
    else if (commandTokens.some((token) => path.basename(token).includes("opencode"))) {
      kind = "opencode-descendant";
    } else if (commandTokens.some((token) => path.basename(token).includes("codex"))) {
      kind = "codex-descendant";
    } else if (commandTokens.some((token) => path.basename(token).includes("claude"))) {
      kind = "claude-descendant";
    } else {
      for (const row of ["codex", "claude", "opencode-billing-rejected", "opencode"]) {
        if (environment.includes(Buffer.from(path.join(runtimeRoot, "provider-homes", row)))) {
          kind = `${row}-provider-descendant`;
          break;
        }
      }
    }
    details.push({
      pid,
      parentPid: Number(/^PPid:\s+(\d+)$/m.exec(statusText)?.[1] ?? 0),
      processName: /^Name:\s+(.+)$/m.exec(statusText)?.[1] ?? "unknown",
      executable: path.basename(executablePath),
      kind,
    });
  }
  return details;
}

async function waitForRuntimeProcesses(expected, timeoutMs) {
  const expectedSorted = [...expected].sort((left, right) => left - right);
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const observed = await runtimeProcessIds();
    if (JSON.stringify(observed) === JSON.stringify(expectedSorted)) return observed;
    await delay(200);
  }
  const observed = await runtimeProcessIds();
  const details = await runtimeProcessDetails(observed);
  throw new Error(
    `Owned runtime process mismatch: expected ${expectedSorted.length}, ` +
      `observed ${observed.length} ${JSON.stringify(details)}`,
  );
}

async function waitForDaemonOwnedQuiescence(timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  let details = [];
  while (Date.now() < deadline) {
    const observed = await runtimeProcessIds();
    details = await runtimeProcessDetails(observed);
    const byPid = new Map(details.map((detail) => [detail.pid, detail]));
    const hasOnlyDaemonInfrastructure = details.every(
      (detail) => detail.kind === "daemon-owner" || detail.kind === "runtime-associated",
    );
    const allReachOwner = details.every((detail) => {
      let current = detail;
      const seen = new Set();
      while (current.pid !== daemonPid) {
        if (seen.has(current.pid)) return false;
        seen.add(current.pid);
        current = byPid.get(current.parentPid);
        if (!current) return false;
      }
      return true;
    });
    if (details.length > 0 && hasOnlyDaemonInfrastructure && allReachOwner) return details;
    await delay(200);
  }
  throw new Error(`Runtime did not quiesce to the exact daemon-owned tree: ${JSON.stringify(details)}`);
}

async function daemonStatus() {
  return runJsonCommand(paseoBin, ["daemon", "status", "--json", "--home", paseoHome], {
    timeoutMs: 30_000,
  });
}

async function startDaemon() {
  if (await listenerAcceptsConnections()) {
    throw new Error(`Refusing to use occupied listener ${listen}`);
  }
  await runCommand(
    paseoBin,
    [
      "daemon",
      "start",
      "--home",
      paseoHome,
      "--listen",
      listen,
      "--no-relay",
      "--no-mcp",
      "--no-inject-mcp",
      "--no-web-ui",
    ],
    { timeoutMs: 60_000 },
  );
  daemonMayBeRunning = true;
  await waitForListener(true, 30_000);
  const status = await daemonStatus();
  if (status.localDaemon !== "running" || status.listen !== listen || !Number.isInteger(status.pid)) {
    throw new Error("Paseo status did not identify the exact isolated daemon owner and listener");
  }
  daemonPid = status.pid;
  if (!processIsAlive(daemonPid)) throw new Error("Paseo owner PID is not alive after startup");
  await assertDaemonIdentity(daemonPid);
  return { ownerPidAlive: true, ownerIdentityMatched: true, listenerReachable: true };
}

async function runRow(action, providerHome) {
  const env = {
    ...process.env,
    DIRECTOR_PASEO_CLIENT_ENTRY: clientEntry,
    DIRECTOR_MATRIX_ROOT: runtimeRoot,
    DIRECTOR_PROBE_MCP: probeMcpPath,
    DIRECTOR_PASEO_URL: paseoUrl,
    DIRECTOR_CODEX_MODEL: "gpt-5.4-mini",
    DIRECTOR_CLAUDE_MODEL: "claude-haiku-4-5",
    DIRECTOR_OPENCODE_MODEL: "opencode/nemotron-3-ultra-free",
  };
  if (providerHome) env.DIRECTOR_PROVIDER_HOME = providerHome;
  return runJsonCommand(process.execPath, [runMatrixPath, action], {
    env,
    label: `matrix row ${action}`,
    timeoutMs: 300_000,
    outputLimit: 8 * 1024 * 1024,
  });
}

function assertDiscovery(result) {
  if (result.daemonMcp?.enabled !== false || result.daemonMcp?.injectIntoAgents !== false) {
    throw new Error("Discovery found built-in Paseo MCP enabled or injected");
  }
  const expected = new Map([
    ["codex", ["gpt-5.4-mini"]],
    ["claude", ["claude-haiku-4-5"]],
    ["opencode", ["opencode/gpt-5-nano", "opencode/nemotron-3-ultra-free"]],
    ["probe-acp", ["fixture"]],
    ["probe-acp-no-mcp", ["fixture"]],
  ]);
  if (result.entries?.length !== expected.size) {
    throw new Error("Discovery did not return every required native and ACP provider");
  }
  for (const entry of result.entries) {
    if (
      !expected.has(entry.provider) ||
      entry.status !== "ready" ||
      entry.enabled !== true ||
      entry.testedModelsPresent !== true ||
      JSON.stringify(entry.testedModels) !== JSON.stringify(expected.get(entry.provider))
    ) {
      throw new Error(`Discovery failed closed for provider ${entry.provider ?? "unknown"}`);
    }
  }
}

function assertRowResult(action, result) {
  const expected = {
    codex: ["codex", "pass"],
    claude: ["claude", "pass"],
    "opencode-billing-rejected": ["opencode-explicit-gpt-5-nano", "failed-no-fallback"],
    opencode: ["opencode", "pass"],
    "acp-compatible": ["acp-compatible-mcp-only", "pass"],
    "acp-policy-rejected": ["acp-compatible-with-exact-tool-policy", "fail-closed-excluded"],
    "acp-unsupported": ["acp-declared-unsupported-mcp", "fail-closed-excluded"],
  }[action];
  if (!expected || result.row !== expected[0] || result.verdict !== expected[1]) {
    throw new Error(`Unexpected matrix result for ${action}`);
  }
}

async function stopAndVerifyDaemon() {
  await runJsonCommand(
    paseoBin,
    ["daemon", "stop", "--json", "--home", paseoHome, "--timeout", "30"],
    { timeoutMs: 45_000 },
  );
  await waitForListener(false, 15_000);
  const status = await daemonStatus();
  if (status.localDaemon === "running" || status.pid !== null) {
    throw new Error("Paseo status still reports an owner PID after graceful shutdown");
  }
  if (daemonPid !== null && processIsAlive(daemonPid)) {
    throw new Error("Exact daemon owner PID remains alive after graceful shutdown");
  }
  await waitForRuntimeProcesses([], 15_000);
  daemonMayBeRunning = false;
  return {
    gracefulStopCompleted: true,
    ownerPidAbsent: true,
    listenerClosed: true,
    runtimeProcessCount: 0,
  };
}

async function removeRuntime() {
  const canonicalRoot = await realpath(runtimeRoot);
  const canonicalTmp = await realpath(os.tmpdir());
  const marker = JSON.parse(await readFile(path.join(canonicalRoot, markerName), "utf8"));
  if (
    canonicalRoot !== runtimeRoot ||
    path.dirname(canonicalRoot) !== canonicalTmp ||
    !path.basename(canonicalRoot).startsWith("director-m0.3-") ||
    marker.ownerNonce !== ownerNonce
  ) {
    throw new Error("Refusing to remove a runtime without exact ownership proof");
  }
  if ((await runtimeProcessIds()).length !== 0) {
    throw new Error("Refusing to remove a runtime still referenced by a process");
  }
  await rm(canonicalRoot, { recursive: true, force: false });
  if (await exists(canonicalRoot)) throw new Error("Temporary runtime path remains after removal");
  runtimeRemoved = true;
  return { ownershipMatched: true, temporaryPathRemoved: true };
}

async function cleanupAfterFailure() {
  const failures = [];
  if (!runtimeRoot || runtimeRemoved || !(await exists(runtimeRoot))) return failures;
  if (daemonMayBeRunning && (await listenerAcceptsConnections())) {
    try {
      await runRow("cleanup");
    } catch (cause) {
      failures.push(cause);
    }
  }
  if (daemonMayBeRunning) {
    try {
      await runJsonCommand(
        paseoBin,
        ["daemon", "stop", "--json", "--home", paseoHome, "--timeout", "30"],
        { timeoutMs: 45_000 },
      );
      await waitForListener(false, 15_000);
      await waitForRuntimeProcesses([], 15_000);
      daemonMayBeRunning = false;
    } catch (cause) {
      failures.push(cause);
    }
  }
  try {
    if ((await runtimeProcessIds()).length === 0 && !(await listenerAcceptsConnections())) {
      await removeRuntime();
    }
  } catch (cause) {
    failures.push(cause);
  }
  return failures;
}

async function executeProcedure() {
  const gitBin = await resolveExecutable(process.env.DIRECTOR_GIT_BIN ?? "git");
  paseoBin = await resolveExecutable(process.env.DIRECTOR_PASEO_BIN ?? "paseo");
  const codexBin = await resolveExecutable(process.env.DIRECTOR_CODEX_BIN ?? "codex");
  const claudeBin = await resolveExecutable(process.env.DIRECTOR_CLAUDE_BIN ?? "claude");
  const opencodeBin = await resolveExecutable(process.env.DIRECTOR_OPENCODE_BIN ?? "opencode");
  clientEntry =
    process.env.DIRECTOR_PASEO_CLIENT_ENTRY ??
    path.resolve(path.dirname(paseoBin), "..", "node_modules", "@getpaseo", "client", "dist", "index.js");
  await access(clientEntry);

  const versions = {
    paseo: await commandVersion(paseoBin),
    codex: await commandVersion(codexBin),
    claude: await commandVersion(claudeBin),
    opencode: await commandVersion(opencodeBin),
    node: process.version,
    platform: `${process.platform}-${process.arch}`,
  };
  for (const [name, expected] of Object.entries(expectedVersions)) {
    if (versions[name] !== expected) {
      throw new Error(`${name} version changed; expected ${expected}, observed ${versions[name]}`);
    }
  }

  runtimeRoot = await realpath(await mkdtemp(path.join(os.tmpdir(), "director-m0.3-")));
  paseoHome = path.join(runtimeRoot, "paseo-home");
  await mkdir(paseoHome, { mode: 0o700 });
  await writeFile(path.join(runtimeRoot, markerName), `${JSON.stringify({ ownerNonce })}\n`, {
    encoding: "utf8",
    mode: 0o600,
  });
  await prepareConfig();
  await prepareWorkspace(gitBin);

  const lifecycle = {
    listenerFreeBeforeStart: !(await listenerAcceptsConnections()),
    daemon: await startDaemon(),
  };
  const results = [];
  const discovery = await runRow("discovery");
  assertDiscovery(discovery);
  results.push(discovery);

  const orderedActions = [
    "codex",
    "claude",
    "opencode-billing-rejected",
    "opencode",
    "acp-compatible",
    "acp-policy-rejected",
    "acp-unsupported",
  ];
  for (const action of orderedActions) {
    const providerHome = await prepareProviderHome(action);
    const result = await runRow(action, providerHome);
    assertRowResult(action, result);
    results.push(result);
  }

  const cleanup = await runRow("cleanup");
  if (
    cleanup.activeAgentCountBeforeCleanup !== 0 ||
    cleanup.archivedByCleanup !== 0 ||
    cleanup.activeAgentCount !== 0 ||
    cleanup.archivedAgentCount !== 5 ||
    cleanup.allHistoricalRowsArchived !== true
  ) {
    throw new Error("Public cleanup query did not find exactly five archived matrix agents");
  }
  results.push(cleanup);
  const daemonOwnedProcesses = await waitForDaemonOwnedQuiescence(15_000);
  lifecycle.beforeDaemonStop = {
    ownerPidAlive: processIsAlive(daemonPid),
    runtimeProcessCount: daemonOwnedProcesses.length,
    exactParentChainMatched: true,
    providerAndProbeProcessCount: 0,
    processes: daemonOwnedProcesses.map(({ processName, executable, kind }) => ({
      processName,
      executable,
      kind,
    })),
  };
  lifecycle.afterDaemonStop = await stopAndVerifyDaemon();
  lifecycle.removal = await removeRuntime();

  return {
    procedure: "dir-m0.3-ordered-provider-matrix-v2",
    versions,
    order: ["discovery", ...orderedActions, "cleanup"],
    results,
    lifecycle,
  };
}

let result;
try {
  result = await executeProcedure();
} catch (cause) {
  const cleanupFailures = await cleanupAfterFailure();
  if (cleanupFailures.length > 0) {
    throw new AggregateError([cause, ...cleanupFailures], "Matrix failed and cleanup was incomplete");
  }
  throw cause;
}

process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
