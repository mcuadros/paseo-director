#!/usr/bin/env node

import { spawn } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import {
  access,
  copyFile,
  lstat,
  mkdir,
  mkdtemp,
  readFile,
  readlink,
  realpath,
  readdir,
  rm,
  writeFile,
} from "node:fs/promises";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const mode = process.argv[2] ?? "--full";
if (process.argv.length > 3 || !new Set(["--full", "--git-only"]).has(mode)) {
  throw new Error("Usage: node docs/evidence/m0.14/reproduce.mjs [--full | --git-only]");
}
if (process.platform !== "linux" || process.arch !== "x64") {
  throw new Error("This evidence contract supports Linux x86_64 only");
}

const evidenceRoot = path.dirname(fileURLToPath(import.meta.url));
const checkoutRoot = path.resolve(evidenceRoot, "../../..");
const image =
  "docker.io/library/node@sha256:83f487e0a63425e5b4d146fb5e5be574bcbe1b7b843d3ebafdd95eaf7767a7e5";
const expected = {
  paseo: "0.7.2",
  codex: "codex-cli 0.147.0",
  claude: "2.1.258 (Claude Code)",
  opencode: "1.18.18",
  podman: "5.4.2",
};
const ownerNonce = randomUUID();
const ownerLabel = `director.m0.14=${ownerNonce}`;
const markerName = ".director-m0.14-owned.json";

let runtimeRoot;
let paseoHome;
let podmanRoot;
let podmanRunRoot;
let podmanRuntime;
let podmanHome;
let daemonPort = null;
let daemonStarted = false;
let workspaceHandle = null;
let runtimeRemoved = false;
let executables;

function delay(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
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

function sanitize(value) {
  let result = value.replaceAll(checkoutRoot, "<checkout>").replaceAll(os.homedir(), "<host-home>");
  if (runtimeRoot) result = result.replaceAll(runtimeRoot, "<runtime>");
  return result.replace(/https?:\/\/\S+/gu, "[url redacted]").trim().slice(-2_000);
}

function run(executable, args, options = {}) {
  const acceptedCodes = new Set(options.acceptedCodes ?? [0]);
  const timeoutMs = options.timeoutMs ?? 60_000;
  const outputLimit = options.outputLimit ?? 4 * 1024 * 1024;
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, {
      cwd: options.cwd ?? checkoutRoot,
      env: options.env ?? process.env,
      shell: false,
      stdio: [options.stdin === undefined ? "ignore" : "pipe", "pipe", "pipe"],
      detached: options.detached ?? false,
    });
    const stdout = [];
    const stderr = [];
    let outputBytes = 0;
    let timedOut = false;
    let settled = false;
    if (options.stdin !== undefined) {
      child.stdin.end(options.stdin);
    }
    const timer = setTimeout(() => {
      timedOut = true;
      child.kill("SIGKILL");
    }, timeoutMs);
    function collect(target, chunk) {
      outputBytes += chunk.length;
      if (outputBytes > outputLimit) child.kill("SIGKILL");
      else target.push(chunk);
    }
    child.stdout.on("data", (chunk) => collect(stdout, chunk));
    child.stderr.on("data", (chunk) => collect(stderr, chunk));
    child.once("error", (cause) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      reject(cause);
    });
    function finish(code, signal) {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      child.stdout.destroy();
      child.stderr.destroy();
      const stdoutText = Buffer.concat(stdout).toString("utf8");
      const stderrText = Buffer.concat(stderr).toString("utf8");
      if (timedOut) {
        reject(new Error(`Command timed out: ${options.label ?? path.basename(executable)}`));
      } else if (outputBytes > outputLimit) {
        reject(
          new Error(`Command exceeded output limit: ${options.label ?? path.basename(executable)}`),
        );
      } else if (
        !acceptedCodes.has(code) &&
        !new Set(options.acceptedSignals ?? []).has(signal)
      ) {
        reject(
          new Error(
            `Command failed: ${options.label ?? path.basename(executable)} ` +
              `(exit=${code ?? "null"}, signal=${signal ?? "none"})\n${sanitize(stderrText)}`,
          ),
        );
      } else {
        resolve({ code, signal, stdout: stdoutText, stderr: stderrText });
      }
    }
    child.once("close", finish);
    child.once("exit", (code, signal) => {
      const drainTimer = setTimeout(() => finish(code, signal), 100);
      drainTimer.unref();
    });
    if (typeof options.onSpawn === "function") options.onSpawn(child);
  });
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

async function commandVersion(executable, args = ["--version"]) {
  return (await run(executable, args)).stdout.trim();
}

async function sha256(filePath) {
  return createHash("sha256").update(await readFile(filePath)).digest("hex");
}

function podmanEnv() {
  return {
    PATH: process.env.PATH,
    HOME: podmanHome,
    XDG_RUNTIME_DIR: podmanRuntime,
  };
}

function podmanArgs(args) {
  return ["--root", podmanRoot, "--runroot", podmanRunRoot, ...args];
}

async function podman(args, options = {}) {
  return run(executables.podman, podmanArgs(args), {
    ...options,
    env: podmanEnv(),
    label: options.label ?? `podman ${args.slice(0, 4).join(" ")}`,
  });
}

function boundaryArgs({ memory = "536870912", pids = "64", cpus = "1" } = {}) {
  return [
    "--network=none",
    "--read-only",
    "--cap-drop=all",
    "--security-opt=no-new-privileges",
    "--userns=keep-id",
    `--user=${process.getuid()}:${process.getgid()}`,
    `--memory=${memory}`,
    `--pids-limit=${pids}`,
    `--cpus=${cpus}`,
    "--ulimit=fsize=1048576:1048576",
    "--ulimit=nofile=256:256",
    "--tmpfs=/tmp:rw,noexec,nosuid,nodev,size=8388608",
    `--label=${ownerLabel}`,
  ];
}

async function initGitRepository(directory, files) {
  await mkdir(directory, { mode: 0o700 });
  for (const [name, contents] of Object.entries(files)) {
    const destination = path.join(directory, name);
    await mkdir(path.dirname(destination), { recursive: true, mode: 0o700 });
    await writeFile(destination, contents, { encoding: "utf8", mode: 0o600 });
  }
  const gitEnv = {
    PATH: process.env.PATH,
    HOME: path.join(runtimeRoot, "git-home"),
    GIT_CONFIG_NOSYSTEM: "1",
    GIT_CONFIG_GLOBAL: "/dev/null",
    GIT_TERMINAL_PROMPT: "0",
  };
  await mkdir(gitEnv.HOME, { recursive: true, mode: 0o700 });
  await run(executables.git, ["init", "--initial-branch=main", directory], { env: gitEnv });
  await run(executables.git, ["-C", directory, "add", "."], { env: gitEnv });
  await run(
    executables.git,
    [
      "-C",
      directory,
      "-c",
      "commit.gpgsign=false",
      "-c",
      "user.name=Director M0.14",
      "-c",
      "user.email=director-m0.14.invalid",
      "commit",
      "-m",
      "test: initialize disposable authority fixture",
    ],
    { env: gitEnv },
  );
  const remotes = await run(executables.git, ["-C", directory, "remote"], { env: gitEnv });
  if (remotes.stdout !== "") throw new Error("A disposable repository unexpectedly has a remote");
  return gitEnv;
}

async function allocatePort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      if (!address || typeof address === "string") {
        server.close(() => reject(new Error("Could not allocate a loopback port")));
        return;
      }
      server.close((cause) => (cause ? reject(cause) : resolve(address.port)));
    });
  });
}

async function loopbackReachable(port) {
  return new Promise((resolve) => {
    const socket = net.createConnection({ host: "127.0.0.1", port });
    const timer = setTimeout(() => {
      socket.destroy();
      resolve(false);
    }, 500);
    socket.once("connect", () => {
      clearTimeout(timer);
      socket.destroy();
      resolve(true);
    });
    socket.once("error", () => {
      clearTimeout(timer);
      socket.destroy();
      resolve(false);
    });
  });
}

async function waitFor(predicate, timeoutMs, message) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (await predicate()) return;
    await delay(100);
  }
  throw new Error(message);
}

async function runtimeProcessIds() {
  const matches = [];
  const needle = Buffer.from(runtimeRoot);
  for (const entry of await readdir("/proc", { withFileTypes: true })) {
    if (!entry.isDirectory() || !/^\d+$/u.test(entry.name)) continue;
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

async function directoryDigest(root, excludedPath) {
  const digest = createHash("sha256");
  const excluded = path.resolve(excludedPath);
  async function visit(current, relative) {
    const absolute = path.resolve(current);
    if (absolute === excluded || absolute.startsWith(`${excluded}${path.sep}`)) return;
    const stat = await lstat(current);
    const type = stat.isDirectory()
      ? "directory"
      : stat.isFile()
        ? "file"
        : stat.isSymbolicLink()
          ? "symlink"
          : "other";
    digest.update(`${type}\0${relative}\0${stat.mode & 0o7777}\0`);
    if (stat.isDirectory()) {
      const entries = await readdir(current);
      entries.sort();
      for (const entry of entries) {
        await visit(path.join(current, entry), path.posix.join(relative, entry));
      }
    } else if (stat.isFile()) {
      digest.update(await readFile(current));
    } else if (stat.isSymbolicLink()) {
      digest.update(await readlink(current));
    } else {
      throw new Error("Shared Git common directory contains an unsupported file type");
    }
  }
  await visit(root, ".");
  return digest.digest("hex");
}

async function prepareRuntime({ withPodman = true } = {}) {
  runtimeRoot = await realpath(await mkdtemp(path.join(os.tmpdir(), "director-m0.14-")));
  paseoHome = path.join(runtimeRoot, "paseo-home");
  podmanRoot = path.join(runtimeRoot, "podman-root");
  podmanRunRoot = path.join(runtimeRoot, "podman-runroot");
  podmanRuntime = path.join(runtimeRoot, "podman-runtime");
  podmanHome = path.join(runtimeRoot, "podman-home");
  const runtimeDirectories = withPodman
    ? [paseoHome, podmanRoot, podmanRunRoot, podmanRuntime, podmanHome]
    : [];
  for (const directory of runtimeDirectories) {
    await mkdir(directory, { recursive: true, mode: 0o700 });
  }
  await writeFile(path.join(runtimeRoot, markerName), `${JSON.stringify({ ownerNonce })}\n`, {
    mode: 0o600,
  });
}

async function providerVersionsInBoundary() {
  const workspace = path.join(runtimeRoot, "provider-workspace");
  await mkdir(workspace, { mode: 0o700 });
  const homes = {};
  for (const provider of ["codex", "claude", "opencode"]) {
    homes[provider] = path.join(runtimeRoot, `${provider}-home`);
    await mkdir(homes[provider], { mode: 0o700 });
  }
  const common = ["run", "--rm", ...boundaryArgs(), `--workdir=${workspace}`];
  const mountWorkspace = [`--mount=type=bind,src=${workspace},dst=${workspace},rw`];
  const codexPackage = path.resolve(path.dirname(executables.codex), "..");
  const versions = {};
  versions.codex = (
    await podman([
      ...common,
      ...mountWorkspace,
      `--mount=type=bind,src=${homes.codex},dst=${homes.codex},rw`,
      "--mount=type=bind,src=" + codexPackage + ",dst=/opt/codex,ro",
      `--env=HOME=${homes.codex}`,
      image,
      "node",
      "/opt/codex/bin/codex.js",
      "--version",
    ])
  ).stdout.trim();
  versions.claude = (
    await podman([
      ...common,
      ...mountWorkspace,
      `--mount=type=bind,src=${homes.claude},dst=${homes.claude},rw`,
      `--mount=type=bind,src=${executables.claude},dst=/opt/claude,ro`,
      `--env=HOME=${homes.claude}`,
      image,
      "/opt/claude",
      "--version",
    ])
  ).stdout.trim();
  versions.opencode = (
    await podman([
      ...common,
      ...mountWorkspace,
      `--mount=type=bind,src=${homes.opencode},dst=${homes.opencode},rw`,
      `--mount=type=bind,src=${executables.opencode},dst=/opt/opencode,ro`,
      `--env=HOME=${homes.opencode}`,
      `--env=XDG_CONFIG_HOME=${path.join(homes.opencode, ".config")}`,
      `--env=XDG_DATA_HOME=${path.join(homes.opencode, ".local", "share")}`,
      `--env=XDG_CACHE_HOME=${path.join(homes.opencode, ".cache")}`,
      image,
      "/opt/opencode",
      "--version",
    ])
  ).stdout.trim();
  for (const provider of Object.keys(versions)) {
    if (versions[provider] !== expected[provider]) {
      throw new Error(`${provider} changed inside the OCI boundary`);
    }
  }
  return versions;
}

async function containerAuthorityProbe() {
  const workspace = path.join(runtimeRoot, "authority-workspace");
  const source = path.join(runtimeRoot, "source-sentinel");
  const sibling = path.join(runtimeRoot, "sibling-sentinel");
  const engine = path.join(runtimeRoot, "engine-state");
  const providerHome = path.join(runtimeRoot, "authority-provider-home");
  for (const directory of [workspace, source, sibling, engine, providerHome]) {
    await mkdir(directory, { mode: 0o700 });
  }
  const engineCredential = path.join(engine, "credential-sentinel");
  const providerCredential = path.join(providerHome, "provider-auth-sentinel");
  await writeFile(engineCredential, "not-a-real-credential\n", { mode: 0o600 });
  await writeFile(providerCredential, "not-a-real-provider-credential\n", { mode: 0o600 });
  await copyFile(path.join(evidenceRoot, "container-probe.mjs"), path.join(workspace, "probe.mjs"));
  const nonce = `m014-${ownerNonce}`;
  const response = await podman(
    [
      "run",
      "--rm",
      "--interactive",
      ...boundaryArgs({ memory: "268435456", pids: "32", cpus: "0.5" }),
      `--workdir=${workspace}`,
      `--mount=type=bind,src=${workspace},dst=${workspace},rw`,
      `--mount=type=bind,src=${providerHome},dst=${providerHome},rw`,
      `--mount=type=bind,src=${providerCredential},dst=${providerCredential},ro`,
      `--env=DIRECTOR_M014_NONCE=${nonce}`,
      image,
      "node",
      path.join(workspace, "probe.mjs"),
    ],
    {
      stdin: JSON.stringify({
        nonce,
        workspace,
        source,
        sibling,
        engine,
        engineCredential,
        providerCredential,
        hostHome: os.homedir(),
        daemonPort: 6767,
      }),
      timeoutMs: 90_000,
    },
  );
  const result = JSON.parse(response.stdout);
  const denied = (value) => value !== "allowed";
  if (
    !result.stdioNonceMatched ||
    result.filesystem.workspaceWrite !== "allowed" ||
    !Object.entries(result.filesystem)
      .filter(([name]) => !new Set(["workspaceWrite", "providerCredentialRead"]).has(name))
      .every(([, value]) => denied(value)) ||
    result.filesystem.providerCredentialRead !== "allowed" ||
    !Object.values(result.control).every((value) => value === "absent" || denied(value)) ||
    !denied(result.network.publicIpv4) ||
    !result.identity.noNewPrivileges ||
    !result.identity.effectiveCapabilitiesZero ||
    result.limits.memoryMax !== "268435456" ||
    result.limits.pidsMax !== "32" ||
    result.limits.cpuMax !== "50000 100000" ||
    !result.limits.fileSizeLimitPresent ||
    !result.limits.processLimit.refused
  ) {
    throw new Error(`The OCI authority contract did not fail closed: ${JSON.stringify(result)}`);
  }
  if ((await exists(path.join(source, "escaped"))) || (await exists(path.join(engine, "escaped")))) {
    throw new Error("The container mutated an unmounted host path");
  }
  return result;
}

async function stdioMcpProbe() {
  const workspace = path.join(runtimeRoot, "mcp-workspace");
  await mkdir(workspace, { mode: 0o700 });
  const server = path.join(workspace, "probe-mcp.mjs");
  const logPath = path.join(workspace, "probe-mcp.jsonl");
  await copyFile(path.resolve(evidenceRoot, "../m0.3/probe-mcp.mjs"), server);
  await writeFile(logPath, "", { mode: 0o600 });
  const nonce = `m014-mcp-${ownerNonce}`;
  const requests = [
    {
      jsonrpc: "2.0",
      id: 1,
      method: "initialize",
      params: {
        protocolVersion: "2025-06-18",
        capabilities: {},
        clientInfo: { name: "director-m0.14-probe", version: "1" },
      },
    },
    { jsonrpc: "2.0", method: "notifications/initialized", params: {} },
    { jsonrpc: "2.0", id: 2, method: "tools/list", params: {} },
    { jsonrpc: "2.0", id: 3, method: "tools/call", params: { name: "read_scope_nonce", arguments: {} } },
    { jsonrpc: "2.0", id: 4, method: "tools/call", params: { name: "forbidden", arguments: {} } },
  ];
  const response = await podman(
    [
      "run",
      "--rm",
      "--interactive",
      ...boundaryArgs(),
      `--workdir=${workspace}`,
      `--mount=type=bind,src=${workspace},dst=${workspace},rw`,
      `--env=DIRECTOR_PROBE_LOG=${logPath}`,
      `--env=DIRECTOR_PROBE_NONCE=${nonce}`,
      "--env=DIRECTOR_PROBE_ROW=m0.14",
      image,
      "node",
      server,
    ],
    { stdin: `${requests.map((request) => JSON.stringify(request)).join("\n")}\n` },
  );
  const responses = response.stdout
    .trim()
    .split("\n")
    .filter(Boolean)
    .map((line) => JSON.parse(line));
  const events = (await readFile(logPath, "utf8"))
    .trim()
    .split("\n")
    .filter(Boolean)
    .map((line) => JSON.parse(line));
  const listed = responses.find((entry) => entry.id === 2)?.result?.tools?.map((tool) => tool.name);
  const returnedText = responses.find((entry) => entry.id === 3)?.result?.content?.[0]?.text;
  const rejected = responses.find((entry) => entry.id === 4)?.error?.code;
  const called = events.filter((event) => event.event === "called");
  if (
    responses.find((entry) => entry.id === 1)?.result?.protocolVersion !== "2025-06-18" ||
    JSON.stringify(listed) !== JSON.stringify(["read_scope_nonce"]) ||
    !returnedText?.includes(nonce) ||
    rejected !== -32602 ||
    called.length !== 1
  ) {
    throw new Error("The stdio MCP contract did not preserve exact scope through OCI");
  }
  return {
    initialized: true,
    advertisedTools: listed,
    exactNonceReturned: true,
    allowedCallCount: called.length,
    forbiddenCallRejected: true,
    stdinClosed: events.some((event) => event.event === "process_stdin_closed"),
  };
}

async function gitCandidateBundleProbe() {
  const source = path.join(runtimeRoot, "git-source");
  const linked = path.join(runtimeRoot, "git-linked-worktree");
  const privateRoot = path.join(runtimeRoot, "git-agent-private");
  const privateObjects = path.join(privateRoot, "objects");
  const agentHome = path.join(privateRoot, "home");
  const hooks = path.join(privateRoot, "hooks");
  const engineTransferRoot = path.join(runtimeRoot, "git-engine-transfer");
  const bundle = path.join(engineTransferRoot, "candidate.bundle");
  const receiver = path.join(runtimeRoot, "git-engine-receiver");
  const gitEnv = await initGitRepository(source, { "README.md": "# disposable\n" });
  await run(executables.git, ["-C", source, "worktree", "add", "--detach", linked, "main"], {
    env: gitEnv,
  });
  for (const directory of [privateObjects, agentHome, hooks]) {
    await mkdir(directory, { recursive: true, mode: 0o700 });
  }
  for (const directory of [path.join(privateObjects, "info"), path.join(privateObjects, "pack")]) {
    await mkdir(directory, { mode: 0o700 });
  }
  const agentScript = path.join(privateRoot, "git-candidate-agent.mjs");
  const namespaceScript = path.join(privateRoot, "git-candidate-namespace.mjs");
  await copyFile(path.join(evidenceRoot, "git-candidate-agent.mjs"), agentScript);
  await copyFile(path.join(evidenceRoot, "git-candidate-namespace.mjs"), namespaceScript);
  const dotGitText = (await readFile(path.join(linked, ".git"), "utf8")).trim();
  const gitDir = await realpath(dotGitText.replace(/^gitdir:\s*/u, ""));
  const commonDir = await realpath(
    path.resolve(gitDir, (await readFile(path.join(gitDir, "commondir"), "utf8")).trim()),
  );
  const base = (
    await run(executables.git, ["-C", source, "rev-parse", "HEAD"], { env: gitEnv })
  ).stdout.trim();
  const sharedHeadsBefore = (
    await run(
      executables.git,
      ["-C", source, "for-each-ref", "--format=%(refname)%00%(objectname)", "refs/heads"],
      { env: gitEnv },
    )
  ).stdout;
  const sharedDigestBefore = await directoryDigest(commonDir, gitDir);
  const sharedWriteProbe = path.join(commonDir, "refs", "heads", "forbidden-agent-write");
  const agentResult = JSON.parse(
    (
      await run(
        executables.unshare,
        ["--user", "--map-root-user", "--mount", "--fork", process.execPath, namespaceScript],
        {
          cwd: linked,
          env: {
            PATH: "/usr/bin:/bin",
            HOME: agentHome,
            LANG: "C.UTF-8",
            DIRECTOR_M014_COMMON_DIR: commonDir,
            DIRECTOR_M014_WORKTREE_GIT_DIR: gitDir,
            DIRECTOR_M014_GIT_AGENT_SCRIPT: agentScript,
            DIRECTOR_M014_WORKTREE: linked,
            DIRECTOR_M014_PRIVATE_OBJECTS: privateObjects,
            DIRECTOR_M014_SHARED_OBJECTS: path.join(commonDir, "objects"),
            DIRECTOR_M014_HOOKS: hooks,
            DIRECTOR_M014_BASE: base,
            DIRECTOR_M014_NONCE: ownerNonce,
            DIRECTOR_M014_SHARED_WRITE_PROBE: sharedWriteProbe,
          },
          label: "isolated Git Candidate producer",
          timeoutMs: 90_000,
        },
      )
    ).stdout,
  );
  const candidate = agentResult.candidate;
  const sharedHeadsAfter = (
    await run(
      executables.git,
      ["-C", source, "for-each-ref", "--format=%(refname)%00%(objectname)", "refs/heads"],
      { env: gitEnv },
    )
  ).stdout;
  const sharedDigestAfter = await directoryDigest(commonDir, gitDir);
  const privateCandidateObject = path.join(privateObjects, candidate.slice(0, 2), candidate.slice(2));
  const sharedCandidateObject = path.join(commonDir, "objects", candidate.slice(0, 2), candidate.slice(2));
  const privateGitEnv = {
    ...gitEnv,
    GIT_DIR: gitDir,
    GIT_WORK_TREE: linked,
    GIT_OBJECT_DIRECTORY: privateObjects,
    GIT_ALTERNATE_OBJECT_DIRECTORIES: path.join(commonDir, "objects"),
  };
  await mkdir(engineTransferRoot, { mode: 0o700 });
  await run(
    executables.git,
    [
      "-C",
      linked,
      "-c",
      `core.hooksPath=${hooks}`,
      "-c",
      "core.fsmonitor=false",
      "bundle",
      "create",
      bundle,
      "refs/worktree/director-candidate",
    ],
    { env: privateGitEnv },
  );
  await run(executables.git, ["clone", "--bare", "--no-local", source, receiver], {
    env: gitEnv,
  });
  await run(executables.git, ["-C", receiver, "bundle", "verify", bundle], { env: gitEnv });
  const advertised = (
    await run(
      executables.git,
      ["-C", receiver, "bundle", "list-heads", bundle, "refs/worktree/director-candidate"],
      { env: gitEnv },
    )
  ).stdout.trim();
  const importedRef = `refs/director/candidates/m014-${ownerNonce}`;
  await run(
    executables.git,
    [
      "-C",
      receiver,
      "fetch",
      "--no-write-fetch-head",
      bundle,
      `refs/worktree/director-candidate:${importedRef}`,
    ],
    { env: gitEnv },
  );
  const importedCandidate = (
    await run(executables.git, ["-C", receiver, "rev-parse", importedRef], { env: gitEnv })
  ).stdout.trim();
  const importedContent = (
    await run(executables.git, ["-C", receiver, "show", `${candidate}:candidate.txt`], {
      env: gitEnv,
    })
  ).stdout;
  await run(executables.git, ["-C", receiver, "fsck", "--strict", "--no-reflogs", candidate], {
    env: gitEnv,
  });
  if (
    agentResult.gitVersion !== "git version 2.47.3" ||
    agentResult.parent !== base ||
    agentResult.worktreeRef !== candidate ||
    agentResult.sharedWriteDenied !== true ||
    agentResult.worktreeClean !== true ||
    sharedHeadsAfter !== sharedHeadsBefore ||
    sharedDigestAfter !== sharedDigestBefore ||
    !(await exists(privateCandidateObject)) ||
    (await exists(sharedCandidateObject)) ||
    advertised !== `${candidate} refs/worktree/director-candidate` ||
    importedCandidate !== candidate ||
    importedContent !== `candidate ${ownerNonce}\n`
  ) {
    throw new Error("Private-object Candidate bundle/import contract did not pass");
  }
  return {
    gitVersion: agentResult.gitVersion,
    candidateProducedWithCommonReadOnly: true,
    candidateParentMatched: true,
    perWorktreeRefMatched: true,
    privateCandidateObjectPresent: true,
    sharedCandidateObjectAbsent: true,
    sharedHeadsUnchangedDuringAgentPhase: true,
    sharedCommonUnchangedOutsideOwnedGitdir: true,
    directSharedWriteDenied: true,
    bundleVerified: true,
    bundleHeadMatched: true,
    engineImported: true,
    importedExactCandidate: true,
    importedContentMatched: true,
    agentWorktreeClean: true,
  };
}

async function startDaemon(engineRoot) {
  daemonPort = await allocatePort();
  const daemonHome = path.join(runtimeRoot, "daemon-home");
  await mkdir(daemonHome, { mode: 0o700 });
  await writeFile(
    path.join(paseoHome, "config.json"),
    `${JSON.stringify({
      version: 1,
      daemon: {
        mcp: { enabled: false, injectIntoAgents: false },
        browserTools: { enabled: false },
        relay: { enabled: false },
      },
      agents: { metadataGeneration: { providers: [] } },
      features: { dictation: { enabled: false }, voiceMode: { enabled: false }, webUi: { enabled: false } },
      pluginsEnabled: false,
    }, null, 2)}\n`,
    { mode: 0o600 },
  );
  const env = {
    PATH: process.env.PATH,
    HOME: daemonHome,
    LANG: "C.UTF-8",
    PASEO_HOME: paseoHome,
    DIRECTOR_M014_ENGINE_ROOT: engineRoot,
    DIRECTOR_M014_DAEMON_PORT: String(daemonPort),
  };
  await run(
    executables.paseo,
    [
      "daemon",
      "start",
      "--home",
      paseoHome,
      "--listen",
      `127.0.0.1:${daemonPort}`,
      "--no-relay",
      "--no-mcp",
      "--no-inject-mcp",
      "--no-web-ui",
    ],
    { env, timeoutMs: 60_000 },
  );
  daemonStarted = true;
  await waitFor(() => loopbackReachable(daemonPort), 30_000, "Isolated Paseo listener did not start");
  return env;
}

async function lifecycleProbe() {
  const source = path.join(runtimeRoot, "lifecycle-source");
  const engine = path.join(runtimeRoot, "lifecycle-engine-state");
  await mkdir(engine, { mode: 0o700 });
  await writeFile(path.join(engine, "credential-sentinel"), "not-a-real-credential\n", { mode: 0o600 });
  const lifecycleScript = await readFile(path.join(evidenceRoot, "lifecycle-escape.mjs"), "utf8");
  await initGitRepository(source, {
    "README.md": "# disposable lifecycle repository\n",
    "lifecycle-escape.mjs": lifecycleScript,
    "paseo.json": `${JSON.stringify({
      worktree: {
        setup: ["node lifecycle-escape.mjs setup"],
        teardown: ["node lifecycle-escape.mjs teardown"],
      },
    }, null, 2)}\n`,
  });
  await startDaemon(engine);
  const clientEntry = path.resolve(
    path.dirname(executables.paseo),
    "..",
    "node_modules",
    "@getpaseo",
    "client",
    "dist",
    "index.js",
  );
  await access(clientEntry);
  const { createPaseoClient } = await import(pathToFileURL(clientEntry).href);
  const client = createPaseoClient({
    url: `ws://127.0.0.1:${daemonPort}/ws`,
    clientId: `director-m0-14-${ownerNonce}`,
    appVersion: "0.7.2",
    reconnect: { enabled: false },
    connectTimeoutMs: 10_000,
  });
  await client.connect();
  try {
    workspaceHandle = await client.workspaces.create({
      title: "Director M0.14 lifecycle escape probe",
      source: {
        kind: "worktree",
        cwd: source,
        action: "branch-off",
        refName: "main",
        branchName: "lifecycle-probe",
        worktreeSlug: `m014-${ownerNonce.slice(0, 8)}`,
      },
    });
    await waitFor(
      () => exists(path.join(source, "setup-observation.json")),
      30_000,
      "Repository setup command did not execute",
    );
    const setup = JSON.parse(await readFile(path.join(source, "setup-observation.json"), "utf8"));
    await workspaceHandle.archive();
    await waitFor(
      () => exists(path.join(source, "teardown-observation.json")),
      30_000,
      "Repository teardown command did not execute",
    );
    const teardown = JSON.parse(
      await readFile(path.join(source, "teardown-observation.json"), "utf8"),
    );
    workspaceHandle = null;
    for (const observation of [setup, teardown]) {
      if (
        !observation.credentialReadable ||
        !observation.engineWritable ||
        !observation.sourceWritable ||
        !observation.rawDaemonReachable ||
        !observation.paseoExecutableReachable
      ) {
        throw new Error(`Lifecycle authority escape was not reproduced: ${JSON.stringify(observation)}`);
      }
    }
    return { setup, teardown, providerWrapperStartedBeforeEscape: false };
  } finally {
    await client.close();
  }
}

async function interruptionRecoveryProbe() {
  const workspace = path.join(runtimeRoot, "interruption-workspace");
  await mkdir(workspace, { mode: 0o700 });
  await copyFile(
    path.join(evidenceRoot, "long-running-probe.mjs"),
    path.join(workspace, "long-running-probe.mjs"),
  );
  const readyPath = path.join(workspace, "ready");
  const cidfile = path.join(runtimeRoot, "interrupted.cid");
  const launch = await podman([
    "run",
    "--detach",
    ...boundaryArgs(),
    `--cidfile=${cidfile}`,
    `--workdir=${workspace}`,
    `--mount=type=bind,src=${workspace},dst=${workspace},rw`,
    `--env=DIRECTOR_M014_READY_PATH=${readyPath}`,
    image,
    "node",
    path.join(workspace, "long-running-probe.mjs"),
  ]);
  await waitFor(() => exists(readyPath), 30_000, "Long-running container did not become ready");
  const containerId = (await readFile(cidfile, "utf8")).trim();
  if (!containerId.startsWith(launch.stdout.trim())) {
    throw new Error("Detached launch did not return its exact durable container ID");
  }
  const discovered = (
    await podman(["ps", "-a", "--filter", `label=${ownerLabel}`, "--format", "{{.ID}}"])
  ).stdout
    .trim()
    .split("\n")
    .filter(Boolean);
  if (discovered.length !== 1 || !containerId.startsWith(discovered[0])) {
    throw new Error("Recovery did not discover exactly the interrupted owned container");
  }
  const state = JSON.parse((await podman(["inspect", containerId])).stdout)[0]?.State?.Status;
  if (state === "running") await podman(["stop", "--time", "1", containerId]);
  await podman(["rm", containerId]);
  const retry = await podman(["container", "exists", containerId], { acceptedCodes: [1] });
  const after = (
    await podman(["ps", "-a", "--filter", `label=${ownerLabel}`, "--format", "{{.ID}}"])
  ).stdout.trim();
  if (retry.code !== 1 || after !== "") throw new Error("Owned container cleanup was not idempotent");
  return {
    detachedLauncherExited: true,
    exactOwnedContainerDiscovered: true,
    stateAfterControllerDisconnect: state,
    reconciledBeforeTermination: true,
    idempotentAbsenceStatus: retry.code,
    residualOwnedContainers: 0,
  };
}

async function stopDaemon() {
  if (!daemonStarted) return;
  await run(
    executables.paseo,
    ["daemon", "stop", "--json", "--home", paseoHome, "--timeout", "30"],
    { timeoutMs: 45_000 },
  );
  await waitFor(
    () => loopbackReachable(daemonPort).then((value) => !value),
    15_000,
    "Paseo listener remained open",
  );
  daemonStarted = false;
}

async function removeRuntime() {
  const canonicalRoot = await realpath(runtimeRoot);
  const canonicalTmp = await realpath(os.tmpdir());
  const marker = JSON.parse(await readFile(path.join(canonicalRoot, markerName), "utf8"));
  if (
    canonicalRoot !== runtimeRoot ||
    path.dirname(canonicalRoot) !== canonicalTmp ||
    !path.basename(canonicalRoot).startsWith("director-m0.14-") ||
    marker.ownerNonce !== ownerNonce
  ) {
    throw new Error("Refusing to remove a runtime without exact ownership proof");
  }
  const residualContainers = (
    await podman(["ps", "-a", "--filter", `label=${ownerLabel}`, "--format", "{{.ID}}"])
  ).stdout.trim();
  if (residualContainers !== "") throw new Error("Refusing cleanup with an owned container present");
  let runtimeReset = "completed";
  try {
    await podman(["system", "reset", "--force"], { timeoutMs: 20_000 });
  } catch (cause) {
    if (
      !(cause instanceof Error) ||
      !cause.message.startsWith("Command timed out:") ||
      (await exists(podmanRoot))
    ) {
      throw cause;
    }
    runtimeReset = "timeout_reconciled_store_absent";
  }
  const residualProcesses = await runtimeProcessIds();
  if (residualProcesses.length !== 0) {
    throw new Error(`Refusing cleanup with ${residualProcesses.length} owned processes present`);
  }
  await rm(canonicalRoot, { recursive: true, force: false });
  if (await exists(canonicalRoot)) throw new Error("Owned temporary runtime remains");
  runtimeRemoved = true;
  return { runtimeProcessCount: 0, runtimeReset };
}

async function cleanupAfterFailure() {
  const failures = [];
  if (!runtimeRoot || runtimeRemoved || !(await exists(runtimeRoot))) return failures;
  if (mode === "--git-only") {
    try {
      await completeFocusedGitCleanup();
    } catch (cause) {
      failures.push(cause);
    }
    return failures;
  }
  if (workspaceHandle) {
    try {
      await workspaceHandle.archive();
      workspaceHandle = null;
    } catch (cause) {
      failures.push(cause);
    }
  }
  try {
    await stopDaemon();
  } catch (cause) {
    failures.push(cause);
  }
  try {
    const ids = (
      await podman(["ps", "-a", "--filter", `label=${ownerLabel}`, "--format", "{{.ID}}"])
    ).stdout
      .trim()
      .split("\n")
      .filter(Boolean);
    for (const id of ids) {
      await podman(["rm", "--force", id]);
    }
  } catch (cause) {
    failures.push(cause);
  }
  try {
    await removeRuntime();
  } catch (cause) {
    failures.push(cause);
  }
  return failures;
}

async function completeRuntimeCleanup(daemonStopped) {
  const cleanup = {
    daemonStopped,
    ownedContainerCount: Number(
      (
        await podman(["ps", "-a", "--filter", `label=${ownerLabel}`, "--format", "{{.ID}}"])
      ).stdout
        .trim()
        .split("\n")
        .filter(Boolean).length,
    ),
  };
  const removal = await removeRuntime();
  cleanup.runtimeProcessCount = removal.runtimeProcessCount;
  cleanup.runtimeReset = removal.runtimeReset;
  cleanup.temporaryRuntimeRemoved = true;
  return cleanup;
}

async function completeFocusedGitCleanup() {
  const residualProcesses = await runtimeProcessIds();
  if (residualProcesses.length !== 0) {
    throw new Error(`Refusing cleanup with ${residualProcesses.length} owned processes present`);
  }
  const canonicalRoot = await realpath(runtimeRoot);
  const canonicalTmp = await realpath(os.tmpdir());
  const marker = JSON.parse(await readFile(path.join(canonicalRoot, markerName), "utf8"));
  if (
    canonicalRoot !== runtimeRoot ||
    path.dirname(canonicalRoot) !== canonicalTmp ||
    !path.basename(canonicalRoot).startsWith("director-m0.14-") ||
    marker.ownerNonce !== ownerNonce
  ) {
    throw new Error("Refusing to remove a focused runtime without exact ownership proof");
  }
  await rm(canonicalRoot, { recursive: true, force: false });
  if (await exists(canonicalRoot)) throw new Error("Owned focused runtime remains");
  runtimeRemoved = true;
  return { runtimeProcessCount: 0, temporaryRuntimeRemoved: true };
}

async function execute() {
  executables = {
    git: await resolveExecutable(process.env.DIRECTOR_GIT_BIN ?? "git"),
    unshare: await resolveExecutable(process.env.DIRECTOR_UNSHARE_BIN ?? "unshare"),
  };
  const versions = {
    git: await commandVersion(executables.git),
    node: process.version,
    kernel: os.release(),
    platform: `${process.platform}-${process.arch}`,
  };
  if (mode === "--git-only") {
    await prepareRuntime({ withPodman: false });
    const gitCandidateBundle = await gitCandidateBundleProbe();
    const cleanup = await completeFocusedGitCleanup();
    return {
      procedure: "dir-m0.14-private-object-candidate-bundle-v1",
      result: "pass",
      versions: {
        git: versions.git,
        node: versions.node,
        kernel: versions.kernel,
        platform: versions.platform,
        unshare: await commandVersion(executables.unshare),
      },
      gitCandidateBundle,
      cleanup,
    };
  }
  Object.assign(executables, {
    paseo: await resolveExecutable(process.env.DIRECTOR_PASEO_BIN ?? "paseo"),
    podman: await resolveExecutable(process.env.DIRECTOR_PODMAN_BIN ?? "podman"),
    codex: await resolveExecutable(process.env.DIRECTOR_CODEX_BIN ?? "codex"),
    claude: await resolveExecutable(process.env.DIRECTOR_CLAUDE_BIN ?? "claude"),
    opencode: await resolveExecutable(process.env.DIRECTOR_OPENCODE_BIN ?? "opencode"),
  });
  Object.assign(versions, {
    paseo: await commandVersion(executables.paseo),
    codex: await commandVersion(executables.codex),
    claude: await commandVersion(executables.claude),
    opencode: await commandVersion(executables.opencode),
  });
  await prepareRuntime();
  versions.podman = (
    await run(executables.podman, ["--version"], {
      env: podmanEnv(),
      label: "podman --version",
    })
  ).stdout.trim().replace(/^podman version\s+/u, "");
  const podmanTopology = {
    rootless:
      (await podman(["info", "--format", "{{.Host.Security.Rootless}}"])).stdout.trim() ===
      "true",
    cgroupsVersion: (
      await podman(["info", "--format", "{{.Host.CgroupsVersion}}"])
    ).stdout.trim(),
    cgroupManager: (
      await podman(["info", "--format", "{{.Host.CgroupManager}}"])
    ).stdout.trim(),
    graphDriver: (
      await podman(["info", "--format", "{{.Store.GraphDriverName}}"])
    ).stdout.trim(),
  };
  if (!podmanTopology.rootless || podmanTopology.cgroupsVersion !== "v2") {
    throw new Error("The host lacks the required rootless OCI and cgroup-v2 topology");
  }
  for (const name of ["paseo", "codex", "claude", "opencode", "podman"]) {
    if (versions[name] !== expected[name]) {
      throw new Error(`${name} version changed: expected ${expected[name]}, observed ${versions[name]}`);
    }
  }
  await podman(["pull", image], { timeoutMs: 120_000, outputLimit: 16 * 1024 * 1024 });
  const imageFacts = JSON.parse((await podman(["image", "inspect", image])).stdout)[0];
  if (imageFacts?.Id !== "6e6261159fd399ebe5a3d556b7d89da9c85c873f3f270918aad6c8107da8b411") {
    throw new Error("Pinned image manifest resolved to an unexpected image ID");
  }
  const codexNative = path.resolve(
    path.dirname(executables.codex),
    "..",
    "node_modules",
    "@openai",
    "codex-linux-x64",
    "vendor",
    "x86_64-unknown-linux-musl",
    "bin",
    "codex",
  );
  const providerBinaryHashes = {
    codexLauncher: await sha256(executables.codex),
    codexNative: await sha256(codexNative),
    claude: await sha256(executables.claude),
    opencode: await sha256(executables.opencode),
  };
  const providerVersions = await providerVersionsInBoundary();
  const authority = await containerAuthorityProbe();
  const stdioMcp = await stdioMcpProbe();
  const gitCandidateBundle = await gitCandidateBundleProbe();
  const interruptionRecovery = await interruptionRecoveryProbe();
  const lifecycle = await lifecycleProbe();
  await stopDaemon();
  const cleanup = await completeRuntimeCleanup(true);
  return {
    procedure: "dir-m0.14-linux-authority-contract-v1",
    result: "observations-recorded",
    versions,
    podmanTopology,
    image: {
      reference: image,
      id: imageFacts.Id,
      os: imageFacts.Os,
      architecture: imageFacts.Architecture,
    },
    providerBinaryHashes,
    providerVersions,
    authority,
    stdioMcp,
    gitCandidateBundle,
    interruptionRecovery,
    lifecycle,
    practicalBoundaryInputs: [
      "automatic_lifecycle_configuration_requires_preflight_refusal_or_human_approval",
      "provider_authentication_is_available_only_to_the_trusted_provider_cli",
      "provider_network_is_defense_in_depth_under_the_trusted_cli_assumption",
      "aggregate_disk_and_resource_ceilings_are_operational_fail_closed_limits",
    ],
    cleanup,
  };
}

let result;
try {
  result = await execute();
} catch (cause) {
  const cleanupFailures = await cleanupAfterFailure();
  if (cleanupFailures.length > 0) {
    throw new AggregateError([cause, ...cleanupFailures], "Evidence failed and cleanup was incomplete");
  }
  throw cause;
}

process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
