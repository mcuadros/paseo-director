#!/usr/bin/env node

import {
  chmodSync,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { createHash, createHmac, randomBytes } from "node:crypto";
import { arch, platform, release, tmpdir, type } from "node:os";
import { basename, dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { spawn, spawnSync } from "node:child_process";
import { createServer } from "node:net";

const doltBin = process.env.DOLT_BIN ?? "dolt";
const gitBin = process.env.GIT_BIN ?? "git";
const scriptPath = fileURLToPath(import.meta.url);
const properties = [
  "remote-layout",
  "partial-git-failure",
  "partial-dolt-failure",
  "shared-server-recovery",
  "backup-restore",
  "schema-migration",
  "configuration-drift",
  "supported-topology",
];
const statementTimeoutMs = 60_000;
const secretValues = new Set();
const liveChildren = new Set();
const ownedDoltRoots = new Set();
const processEvidence = {
  dolt_cli_calls: 0,
  metrics_disabled_preflight_checks: 0,
  event_flush_disabled_preflight_checks: 0,
  scans: 0,
  same_user_environments_read: 0,
  owned_surviving_environments_read: 0,
  unreadable_unrelated_same_user_environments: 0,
  owned_metrics_processes_observed: 0,
  credential_markers_observed: 0,
};

function assert(condition, message) {
  if (!condition) {
    throw new Error(`unexpected observation: ${message}`);
  }
}

function registerSecret(value) {
  assert(typeof value === "string" && value.length >= 32, "generated credential is too short");
  secretValues.add(value);
  return value;
}

function containsSecret(value) {
  const text = Buffer.isBuffer(value) ? value : Buffer.from(String(value ?? ""));
  return [...secretValues].some((secret) => text.includes(Buffer.from(secret)));
}

function metricsConfigPath(root) {
  return join(root, ".dolt", "config_global.json");
}

function initializeDoltRoot(root) {
  assert(platform() === "linux", `Dolt evidence requires Linux, observed ${platform()}`);
  mkdirSync(root, { recursive: true, mode: 0o700 });
  chmodSync(root, 0o700);
  const configDirectory = join(root, ".dolt");
  mkdirSync(configDirectory, { recursive: true, mode: 0o700 });
  chmodSync(configDirectory, 0o700);
  const configPath = metricsConfigPath(root);
  if (!existsSync(configPath)) {
    writeFileSync(configPath, `${JSON.stringify({ "metrics.disabled": "true" }, null, 2)}\n`, {
      mode: 0o600,
    });
  }
  chmodSync(configPath, 0o600);
  const config = JSON.parse(readFileSync(configPath, "utf8"));
  assert(config["metrics.disabled"] === "true", "metrics.disabled was not true before Dolt use");
  ownedDoltRoots.add(root);
  return root;
}

function discoverMetricsPids() {
  assert(platform() === "linux", "metrics process discovery requires Linux");
  const pids = new Set();
  for (const name of readdirSync("/proc")) {
    if (!/^\d+$/.test(name)) {
      continue;
    }
    try {
      const argumentsList = readFileSync(join("/proc", name, "cmdline"))
        .toString("utf8")
        .split("\u0000");
      if (argumentsList.some((argument) => argument === "send-metrics")) {
        pids.add(Number(name));
      }
    } catch (error) {
      if (!["EACCES", "ENOENT", "ESRCH"].includes(error?.code)) {
        throw error;
      }
    }
  }
  return pids;
}

function assertMetricsDisabled(root) {
  assert(ownedDoltRoots.has(root), "Dolt root was not initialized by the harness");
  const configPath = metricsConfigPath(root);
  const config = JSON.parse(readFileSync(configPath, "utf8"));
  assert(config["metrics.disabled"] === "true", "metrics.disabled drifted before Dolt use");
  assert((statSync(configPath).mode & 0o777) === 0o600, "metrics config permissions drifted");
  processEvidence.metrics_disabled_preflight_checks += 1;
}

function assertDoltProcessPreflight(environment) {
  processEvidence.dolt_cli_calls += 1;
  const root = environment?.DOLT_ROOT_PATH;
  assert(typeof root === "string" && root.length > 0, "Dolt invocation omitted its owned root");
  assertMetricsDisabled(root);
  assert(
    environment?.DOLT_DISABLE_EVENT_FLUSH === "1",
    "Dolt invocation omitted the event-flush kill switch",
  );
  processEvidence.event_flush_disabled_preflight_checks += 1;
}

function scanSurvivingProcessEnvironments(phase) {
  assert(platform() === "linux", `process environment scan requires Linux during ${phase}`);
  assert(typeof process.getuid === "function", "Linux process environment scan requires getuid");
  const currentUid = process.getuid();
  let readableEnvironments = 0;
  let ownedEnvironments = 0;
  let unreadableUnrelatedEnvironments = 0;
  let ownedMetrics = 0;
  let credentialMarkers = 0;
  const metricsPids = discoverMetricsPids();
  const requiredPids = new Set([
    process.pid,
    ...[...liveChildren].filter(isRunning).map((child) => child.pid),
  ]);
  for (const name of readdirSync("/proc")) {
    if (!/^\d+$/.test(name)) {
      continue;
    }
    const processRoot = join("/proc", name);
    try {
      if (statSync(processRoot).uid !== currentUid) {
        continue;
      }
      const environment = readFileSync(join(processRoot, "environ"));
      readableEnvironments += 1;
      const environmentText = environment.toString("utf8");
      if (requiredPids.has(Number(name))) {
        ownedEnvironments += 1;
      }
      if (
        metricsPids.has(Number(name)) &&
        [...ownedDoltRoots].some((root) => environmentText.includes(`DOLT_ROOT_PATH=${root}`))
      ) {
        ownedMetrics += 1;
      }
      if (containsSecret(environment)) {
        credentialMarkers += 1;
      }
    } catch (error) {
      if (["ENOENT", "ESRCH"].includes(error?.code)) {
        continue;
      }
      if (error?.code === "EACCES" && !requiredPids.has(Number(name))) {
        unreadableUnrelatedEnvironments += 1;
        continue;
      }
      if (error?.code !== "EACCES") {
        throw error;
      }
      throw new Error(`owned surviving process environment was unreadable during ${phase}`);
    }
  }
  processEvidence.scans += 1;
  processEvidence.same_user_environments_read += readableEnvironments;
  processEvidence.owned_surviving_environments_read += ownedEnvironments;
  processEvidence.unreadable_unrelated_same_user_environments +=
    unreadableUnrelatedEnvironments;
  processEvidence.owned_metrics_processes_observed += ownedMetrics;
  processEvidence.credential_markers_observed += credentialMarkers;
  assert(ownedMetrics === 0, `owned Dolt metrics process appeared during ${phase}`);
  assert(
    ownedEnvironments === requiredPids.size,
    `not every owned surviving process environment was scanned during ${phase}`,
  );
  assert(credentialMarkers === 0, `credential marker survived in a process environment during ${phase}`);
  return {
    readable_same_user_environments: readableEnvironments,
    scanned_owned_surviving_environments: ownedEnvironments,
    unreadable_unrelated_same_user_environments: unreadableUnrelatedEnvironments,
    owned_metrics_processes: 0,
    credential_markers: 0,
  };
}

function execute(command, args, options = {}) {
  assert(!containsSecret(args.join("\u0000")), "credential entered subprocess argv");
  if (command === doltBin) {
    assertDoltProcessPreflight(options.env);
  }
  const result = spawnSync(command, args, {
    cwd: options.cwd,
    env: { ...process.env, ...options.env },
    input: options.input,
    encoding: "utf8",
    timeout: options.timeout ?? statementTimeoutMs,
    killSignal: "SIGKILL",
    maxBuffer: 16 * 1024 * 1024,
  });
  if (result.error) {
    throw result.error;
  }
  assert(
    !containsSecret(`${result.stdout ?? ""}\n${result.stderr ?? ""}`),
    "credential entered subprocess output",
  );
  if (command === doltBin) {
    scanSurvivingProcessEnvironments("after Dolt CLI exit");
  }
  return {
    code: result.status ?? 1,
    signal: result.signal,
    stdout: result.stdout ?? "",
    stderr: result.stderr ?? "",
  };
}

function successfully(command, args, options = {}) {
  const result = execute(command, args, options);
  if (result.code !== 0) {
    throw new Error(`${command} ${args.join(" ")} failed with exit ${result.code}: ${result.stderr || result.stdout}`);
  }
  return result;
}

function classifyFailure(result) {
  const output = `${result.stdout}\n${result.stderr}`;
  if (/non-fast-forward|fetch first|tip of your current branch is behind|cannot be fast-forwarded/i.test(output)) {
    return "NON_FAST_FORWARD";
  }
  if (/does not appear to be a git repository|failed to load|no such file|unable to access|could not read/i.test(output)) {
    return "REMOTE_UNREACHABLE";
  }
  if (/initialized more than 1x/i.test(output)) {
    return "DUPLICATE_INITIALIZATION";
  }
  if (/connect|connection|dial tcp|refused|server/i.test(output)) {
    return "CONNECTION_UNAVAILABLE";
  }
  return `EXIT_${result.code}`;
}

function parseJson(text) {
  return JSON.parse(text.trim());
}

function firstRow(result) {
  const parsed = parseJson(result.stdout);
  assert(Array.isArray(parsed.rows) && parsed.rows.length > 0, "query returned no rows");
  return parsed.rows[0];
}

function hashJson(value) {
  return createHash("sha256").update(JSON.stringify(value)).digest("hex");
}

function fileRemote(path) {
  return pathToFileURL(path).href;
}

function git(args, options = {}) {
  return execute(gitBin, args, options);
}

function gitSuccessfully(args, options = {}) {
  return successfully(gitBin, args, options);
}

function dolt(args, options = {}) {
  return execute(doltBin, args, options);
}

function doltSuccessfully(args, options = {}) {
  return successfully(doltBin, args, options);
}

function makeClientEnv(root, extra = {}) {
  initializeDoltRoot(root);
  return { ...extra, DOLT_ROOT_PATH: root, DOLT_DISABLE_EVENT_FLUSH: "1" };
}

function initGitRemote(root) {
  const remote = join(root, "organizer-and-taskstore.git");
  const organizer = join(root, "organizer");
  mkdirSync(organizer, { mode: 0o700 });
  gitSuccessfully(["init", "--bare", remote]);
  gitSuccessfully(["init", "-b", "main"], { cwd: organizer });
  gitSuccessfully(["config", "user.name", "Director operations evidence"], { cwd: organizer });
  gitSuccessfully(["config", "user.email", "evidence@example.invalid"], { cwd: organizer });
  writeFileSync(join(organizer, "paseo-director.json"), '{"schemaVersion":1}\n', { mode: 0o600 });
  gitSuccessfully(["add", "paseo-director.json"], { cwd: organizer });
  gitSuccessfully(["commit", "-m", "seed organizer"], { cwd: organizer });
  gitSuccessfully(["remote", "add", "origin", fileRemote(remote)], { cwd: organizer });
  gitSuccessfully(["push", "-u", "origin", "main"], { cwd: organizer });
  gitSuccessfully(["symbolic-ref", "HEAD", "refs/heads/main"], { cwd: remote });
  return { remote, organizer };
}

function initDoltStore(root, clientEnv, name = "taskstore") {
  const store = join(root, name);
  mkdirSync(store, { mode: 0o700 });
  doltSuccessfully(
    ["init", "--name=Director operations evidence", "--email=evidence@example.invalid"],
    { cwd: store, env: clientEnv },
  );
  doltSuccessfully(
    [
      "sql",
      "-q",
      "CREATE TABLE evidence (id VARCHAR(64) PRIMARY KEY, payload VARCHAR(128) NOT NULL); INSERT INTO evidence VALUES ('seed','initial');",
    ],
    { cwd: store, env: clientEnv },
  );
  doltSuccessfully(["add", "."], { cwd: store, env: clientEnv });
  doltSuccessfully(["commit", "-m", "seed taskstore"], { cwd: store, env: clientEnv });
  return store;
}

function addDoltGitRemote(store, clientEnv, remote) {
  doltSuccessfully(
    ["remote", "add", "--ref=refs/dolt/data", "origin", fileRemote(remote)],
    { cwd: store, env: clientEnv },
  );
}

function seedCombinedRemote(root) {
  const clientRoot = join(root, "client-root");
  mkdirSync(clientRoot, { mode: 0o700 });
  const clientEnv = makeClientEnv(clientRoot);
  const { remote, organizer } = initGitRemote(root);
  const taskstore = initDoltStore(root, clientEnv);
  addDoltGitRemote(taskstore, clientEnv, remote);
  doltSuccessfully(["push", "-u", "origin", "main"], { cwd: taskstore, env: clientEnv });
  return { clientEnv, remote, organizer, taskstore };
}

function doltRows(store, clientEnv) {
  return parseJson(
    doltSuccessfully(
      ["sql", "-r", "json", "-q", "SELECT id, payload FROM evidence ORDER BY id"],
      { cwd: store, env: clientEnv },
    ).stdout,
  ).rows;
}

function gitRefs(remote) {
  return gitSuccessfully(
    ["for-each-ref", "--format=%(refname) %(objecttype)", "refs/heads", "refs/dolt"],
    { cwd: remote },
  )
    .stdout.trim()
    .split("\n")
    .filter(Boolean)
    .sort();
}

function createGitCommit(repository, filename, contents, message) {
  writeFileSync(join(repository, filename), contents, { mode: 0o600 });
  gitSuccessfully(["add", filename], { cwd: repository });
  gitSuccessfully(["commit", "-m", message], { cwd: repository });
}

function createDoltCommit(store, clientEnv, id, payload, message) {
  assert(/^[a-z0-9-]+$/.test(id), "unsafe Dolt fixture id");
  assert(/^[a-z0-9-]+$/.test(payload), "unsafe Dolt fixture payload");
  doltSuccessfully(["config", "--local", "--set", "user.name", "Director operations evidence"], {
    cwd: store,
    env: clientEnv,
  });
  doltSuccessfully(["config", "--local", "--set", "user.email", "evidence@example.invalid"], {
    cwd: store,
    env: clientEnv,
  });
  doltSuccessfully(
    ["sql", "-q", `INSERT INTO evidence VALUES ('${id}','${payload}')`],
    { cwd: store, env: clientEnv },
  );
  doltSuccessfully(["add", "."], { cwd: store, env: clientEnv });
  doltSuccessfully(["commit", "-m", message], { cwd: store, env: clientEnv });
}

function cloneDolt(remote, destination, clientEnv) {
  doltSuccessfully(
    ["clone", "--ref=refs/dolt/data", fileRemote(remote), destination],
    { cwd: dirname(destination), env: clientEnv },
  );
}

async function withOwnedRoot(property, operation) {
  const runId = randomBytes(8).toString("hex");
  const root = mkdtempSync(join(tmpdir(), `director-m0.5-${property}-${runId}-`));
  chmodSync(root, 0o700);
  let outcome;
  let cleanupEvidence;
  let cleanupFailure;
  try {
    outcome = await operation({ root, runId });
  } finally {
    try {
      await stopTrackedChildren();
      scanSurvivingProcessEnvironments("before owned-root cleanup");
    } catch (error) {
      cleanupFailure = error;
    }
    rmSync(root, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
    const reappearanceCheckpointsMs = [0, 10, 25, 50, 100, 250, 500];
    let previousCheckpoint = 0;
    for (const checkpoint of reappearanceCheckpointsMs) {
      const waitMs = checkpoint - previousCheckpoint;
      if (waitMs > 0) {
        await new Promise((resolve) => setTimeout(resolve, waitMs));
      }
      previousCheckpoint = checkpoint;
      try {
        scanSurvivingProcessEnvironments(`owned-root cleanup +${checkpoint}ms`);
      } catch (error) {
        cleanupFailure ??= error;
      }
      if (existsSync(root)) {
        rmSync(root, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
        cleanupFailure ??= new Error(`owned ${property} temporary directory reappeared after cleanup`);
      }
    }
    cleanupEvidence = {
      owned_processes_stopped: true,
      task_owned_dolt_roots_configured: ownedDoltRoots.size,
      dolt_cli_calls: processEvidence.dolt_cli_calls,
      metrics_disabled_preflight_checks: processEvidence.metrics_disabled_preflight_checks,
      event_flush_disabled_preflight_checks:
        processEvidence.event_flush_disabled_preflight_checks,
      owned_metrics_processes_observed: processEvidence.owned_metrics_processes_observed,
      credential_markers_in_surviving_process_environments:
        processEvidence.credential_markers_observed,
      process_environment_scans: processEvidence.scans,
      same_user_process_environments_read: processEvidence.same_user_environments_read,
      owned_surviving_process_environments_read:
        processEvidence.owned_surviving_environments_read,
      unreadable_unrelated_same_user_process_environments:
        processEvidence.unreadable_unrelated_same_user_environments,
      root_reappearance_checkpoints_ms: reappearanceCheckpointsMs,
      owned_directory_removed: true,
      owned_directory_reappeared: false,
    };
    assert(
      cleanupEvidence.dolt_cli_calls === cleanupEvidence.metrics_disabled_preflight_checks,
      "a Dolt CLI call lacked a metrics.disabled preflight check",
    );
    assert(
      cleanupEvidence.dolt_cli_calls === cleanupEvidence.event_flush_disabled_preflight_checks,
      "a Dolt CLI call lacked the event-flush kill-switch preflight check",
    );
    if (cleanupFailure) {
      throw cleanupFailure;
    }
  }
  return {
    property,
    contract_result: "PASS",
    ...outcome,
    cleanup: cleanupEvidence,
  };
}

function isRunning(child) {
  return child && child.exitCode === null && child.signalCode === null;
}

function trackChild(child) {
  liveChildren.add(child);
  child.once("close", () => liveChildren.delete(child));
  return child;
}

async function waitForExit(child, timeoutMs = 10_000) {
  if (!isRunning(child)) {
    return;
  }
  await Promise.race([
    new Promise((resolve) => child.once("close", resolve)),
    new Promise((_, reject) => setTimeout(() => reject(new Error("child did not exit in time")), timeoutMs)),
  ]);
}

async function stopTrackedChildren() {
  for (const child of [...liveChildren]) {
    if (isRunning(child)) {
      child.kill("SIGTERM");
    }
  }
  const deadline = Date.now() + 5_000;
  while ([...liveChildren].some(isRunning) && Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  for (const child of [...liveChildren]) {
    if (isRunning(child)) {
      child.kill("SIGKILL");
    }
  }
  const forcedDeadline = Date.now() + 5_000;
  while ([...liveChildren].some(isRunning) && Date.now() < forcedDeadline) {
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  assert(![...liveChildren].some(isRunning), "an owned child process survived cleanup");
}

async function remoteLayout() {
  return withOwnedRoot("remote-layout", async ({ root }) => {
    const { clientEnv, remote, organizer } = seedCombinedRemote(root);
    const organizerHead = gitSuccessfully(["rev-parse", "refs/heads/main"], { cwd: remote }).stdout.trim();
    const refs = gitRefs(remote);
    assert(refs.includes("refs/dolt/data commit"), "refs/dolt/data was not created");
    assert(refs.includes("refs/heads/__dolt_remote_info__ commit"), "Dolt metadata branch was not created");
    assert(refs.includes("refs/heads/main commit"), "Organizer main branch disappeared");
    assert(
      organizerHead === gitSuccessfully(["rev-parse", "HEAD"], { cwd: organizer }).stdout.trim(),
      "Dolt push changed Organizer main",
    );

    const gitClone = join(root, "organizer-clone");
    gitSuccessfully(["clone", fileRemote(remote), gitClone]);
    assert(existsSync(join(gitClone, "paseo-director.json")), "Organizer clone lost configuration");
    assert(!existsSync(join(gitClone, ".dolt")), "ordinary Git clone exposed a Dolt working store");

    const doltClone = join(root, "taskstore-clone");
    cloneDolt(remote, doltClone, clientEnv);
    assert(JSON.stringify(doltRows(doltClone, clientEnv)) === JSON.stringify([{ id: "seed", payload: "initial" }]), "Dolt clone content differed");
    assert(!existsSync(join(doltClone, ".git")), "Dolt clone exposed Organizer Git history as a worktree");

    return {
      checks: [
        "normal Git main and Dolt refs coexist in one initialized Git remote",
        "refs/dolt/data is the selected data ref",
        "refs/heads/__dolt_remote_info__ is required Dolt metadata",
        "ordinary Git and Dolt clones recover only their respective content",
      ],
      evidence: {
        remote_refs: refs,
        organizer_main_preserved: true,
        independent_clone_fingerprints_match: true,
      },
    };
  });
}

async function partialGitFailure() {
  return withOwnedRoot("partial-git-failure", async ({ root }) => {
    const { clientEnv, remote, organizer, taskstore } = seedCombinedRemote(root);
    createGitCommit(organizer, "local-change.txt", "local\n", "local organizer change");
    createDoltCommit(taskstore, clientEnv, "local-dolt", "local", "local taskstore change");

    const correctRemote = fileRemote(remote);
    const missingRemote = fileRemote(join(root, "unreachable.git"));
    gitSuccessfully(["remote", "set-url", "origin", missingRemote], { cwd: organizer });
    const gitAttempt = git(["push", "origin", "main"], { cwd: organizer });
    const doltAttempt = dolt(["push", "origin", "main"], { cwd: taskstore, env: clientEnv });
    assert(gitAttempt.code !== 0 && doltAttempt.code === 0, "Git failure was not isolated from Dolt success");

    const externalGit = join(root, "external-organizer");
    gitSuccessfully(["clone", correctRemote, externalGit]);
    gitSuccessfully(["config", "user.name", "External evidence writer"], { cwd: externalGit });
    gitSuccessfully(["config", "user.email", "external@example.invalid"], { cwd: externalGit });
    createGitCommit(externalGit, "external-change.txt", "external\n", "external organizer change");
    gitSuccessfully(["push", "origin", "main"], { cwd: externalGit });

    gitSuccessfully(["remote", "set-url", "origin", correctRemote], { cwd: organizer });
    const unsafeRetry = git(["push", "origin", "main"], { cwd: organizer });
    assert(unsafeRetry.code !== 0 && classifyFailure(unsafeRetry) === "NON_FAST_FORWARD", "Git retry overwrote a newer remote head");
    gitSuccessfully(["pull", "--rebase", "origin", "main"], { cwd: organizer });
    gitSuccessfully(["push", "origin", "main"], { cwd: organizer });

    const verifiedGit = join(root, "verified-organizer");
    gitSuccessfully(["clone", correctRemote, verifiedGit]);
    assert(existsSync(join(verifiedGit, "local-change.txt")) && existsSync(join(verifiedGit, "external-change.txt")), "reconciled Git remote lost a change");
    const verifiedDolt = join(root, "verified-taskstore");
    cloneDolt(remote, verifiedDolt, clientEnv);
    assert(doltRows(verifiedDolt, clientEnv).some((row) => row.id === "local-dolt"), "successful Dolt half was lost");

    return {
      checks: [
        "Git failure and Dolt success are reported independently",
        "a blind Git retry refuses to overwrite a newer remote head",
        "fetch/rebase reconciliation preserves local and remote Organizer changes",
        "the already-successful Dolt state remains unchanged",
      ],
      evidence: {
        first_attempt: { git: classifyFailure(gitAttempt), dolt: "SUCCESS", combined: "PARTIAL_FAILURE" },
        blind_retry: classifyFailure(unsafeRetry),
        reconciled: true,
        no_force_used: true,
      },
    };
  });
}

async function partialDoltFailure() {
  return withOwnedRoot("partial-dolt-failure", async ({ root }) => {
    const { clientEnv, remote, organizer, taskstore } = seedCombinedRemote(root);
    createGitCommit(organizer, "local-change.txt", "local\n", "local organizer change");
    createDoltCommit(taskstore, clientEnv, "local-dolt", "local", "local taskstore change");

    const gitAttempt = git(["push", "origin", "main"], { cwd: organizer });
    doltSuccessfully(["remote", "remove", "origin"], { cwd: taskstore, env: clientEnv });
    doltSuccessfully(
      ["remote", "add", "--ref=refs/dolt/data", "origin", fileRemote(join(root, "unreachable.git"))],
      { cwd: taskstore, env: clientEnv },
    );
    const doltAttempt = dolt(["push", "origin", "main"], { cwd: taskstore, env: clientEnv });
    assert(gitAttempt.code === 0 && doltAttempt.code !== 0, "Dolt failure was not isolated from Git success");

    const externalDolt = join(root, "external-taskstore");
    cloneDolt(remote, externalDolt, clientEnv);
    createDoltCommit(externalDolt, clientEnv, "external-dolt", "external", "external taskstore change");
    doltSuccessfully(["push", "origin", "main"], { cwd: externalDolt, env: clientEnv });

    doltSuccessfully(["remote", "remove", "origin"], { cwd: taskstore, env: clientEnv });
    addDoltGitRemote(taskstore, clientEnv, remote);
    const unsafeRetry = dolt(["push", "origin", "main"], { cwd: taskstore, env: clientEnv });
    assert(unsafeRetry.code !== 0 && classifyFailure(unsafeRetry) === "NON_FAST_FORWARD", "Dolt retry overwrote a newer remote head");
    doltSuccessfully(["pull", "--no-edit", "origin", "main"], { cwd: taskstore, env: clientEnv });
    doltSuccessfully(["push", "origin", "main"], { cwd: taskstore, env: clientEnv });

    const verifiedDolt = join(root, "verified-taskstore");
    cloneDolt(remote, verifiedDolt, clientEnv);
    const rows = doltRows(verifiedDolt, clientEnv);
    assert(rows.some((row) => row.id === "local-dolt") && rows.some((row) => row.id === "external-dolt"), "reconciled Dolt remote lost a row");
    const verifiedGit = join(root, "verified-organizer");
    gitSuccessfully(["clone", fileRemote(remote), verifiedGit]);
    assert(existsSync(join(verifiedGit, "local-change.txt")), "successful Git half was lost");

    return {
      checks: [
        "Git success and Dolt failure are reported independently",
        "a blind Dolt retry refuses to overwrite a newer remote commit",
        "pull/merge reconciliation preserves local and remote TaskStore rows",
        "the already-successful Git state remains unchanged",
      ],
      evidence: {
        first_attempt: { git: "SUCCESS", dolt: classifyFailure(doltAttempt), combined: "PARTIAL_FAILURE" },
        blind_retry: classifyFailure(unsafeRetry),
        reconciled_row_ids: rows.map((row) => row.id),
        no_force_used: true,
      },
    };
  });
}

async function reservePort() {
  const probe = createServer();
  probe.unref();
  await new Promise((resolve, reject) => {
    probe.once("error", reject);
    probe.listen(0, "127.0.0.1", resolve);
  });
  const address = probe.address();
  assert(address && typeof address !== "string", "could not reserve a loopback port");
  const port = address.port;
  await new Promise((resolve, reject) => probe.close((error) => (error ? reject(error) : resolve())));
  return port;
}

function remoteSqlArgs(server, database, sql) {
  return [
    "--host=127.0.0.1",
    `--port=${server.port}`,
    "--no-tls",
    `--use-db=${database}`,
    "sql",
    "-r",
    "json",
    "-q",
    sql,
  ];
}

function serverRoleEnv(server, role) {
  const identity = server.identities[role];
  assert(identity, `unknown server role ${role}`);
  return makeClientEnv(server.clientRoot, {
    DOLT_CLI_USER: identity.user,
    DOLT_CLI_PASSWORD: identity.password,
  });
}

function serverQuery(server, role, database, sql) {
  return dolt(remoteSqlArgs(server, database, sql), { env: serverRoleEnv(server, role) });
}

function serverQuerySuccessfully(server, role, database, sql) {
  const result = serverQuery(server, role, database, sql);
  if (result.code !== 0) {
    throw new Error(`redacted SQL operation failed with exit ${result.code}: ${classifyFailure(result)}`);
  }
  return result;
}

function bootstrapServerIdentity(server, bootstrapSql) {
  const result = dolt(
    [
      `--data-dir=${server.root}`,
      `--doltcfg-dir=${server.configDir}`,
      `--use-db=${server.databases[0]}`,
      "sql",
      "-r",
      "json",
    ],
    { env: makeClientEnv(server.clientRoot), input: bootstrapSql },
  );
  if (result.code !== 0) {
    throw new Error(`redacted server bootstrap failed with exit ${result.code}`);
  }
}

async function createServerFixture(root, databaseCount = 1, options = {}) {
  const runId = randomBytes(8).toString("hex");
  const clientRoot = join(root, "client-root");
  const configDir = join(root, ".doltcfg");
  mkdirSync(clientRoot, { mode: 0o700 });
  mkdirSync(configDir, { mode: 0o700 });
  const databases = Array.from({ length: databaseCount }, (_, index) => `store_${index + 1}_${runId}`);
  const identities = {
    control: {
      user: `control_${runId}`,
      password: registerSecret(randomBytes(32).toString("base64url")),
    },
    writer: {
      user: `writer_${runId}`,
      password: registerSecret(randomBytes(32).toString("base64url")),
    },
  };
  const identityProof = createHmac("sha256", identities.control.password).update(runId).digest("hex");

  for (const database of databases) {
    const directory = join(root, database);
    mkdirSync(directory, { mode: 0o700 });
    doltSuccessfully(
      ["init", "--name=Director operations evidence", "--email=evidence@example.invalid"],
      { cwd: directory, env: makeClientEnv(clientRoot) },
    );
  }

  const grants = databases
    .map(
      (database) =>
        `GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, ALTER, REFERENCES, EXECUTE ON \`${database}\`.* TO '${identities.writer.user}'@'%';`,
    )
    .join("\n");
  const bootstrap = `
    CREATE TABLE harness_identity (
      singleton TINYINT PRIMARY KEY,
      run_id CHAR(16) NOT NULL UNIQUE,
      identity_proof CHAR(64) NOT NULL UNIQUE
    );
    INSERT INTO harness_identity VALUES (1, '${runId}', '${identityProof}');
    CREATE USER '${identities.control.user}'@'%' IDENTIFIED WITH mysql_native_password BY '${identities.control.password}';
    GRANT ALL ON *.* TO '${identities.control.user}'@'%' WITH GRANT OPTION;
    CREATE USER '${identities.writer.user}'@'%' IDENTIFIED WITH mysql_native_password BY '${identities.writer.password}';
    ${grants}
    DROP USER IF EXISTS 'root'@'%';
    FLUSH PRIVILEGES;
    DROP USER IF EXISTS 'root'@'localhost';
    ${bootstrapSqlSafe(options.bootstrapSql ?? "")}
  `;
  const server = {
    root,
    clientRoot,
    configDir,
    databases,
    identities,
    identityProof,
    runId,
    process: undefined,
    stdout: "",
    stderr: "",
    port: await reservePort(),
    configPath: join(root, "server.yaml"),
  };
  bootstrapServerIdentity(server, bootstrap);

  const userSessionBlock = options.writerSessionDefault
    ? `
user_session_vars:
  - name: ${JSON.stringify(identities.writer.user)}
    vars:
      dolt_force_transaction_commit: 0`
    : "";
  writeFileSync(
    server.configPath,
    `log_level: warning
data_dir: ${JSON.stringify(root)}
cfg_dir: ${JSON.stringify(configDir)}
privilege_file: ${JSON.stringify(join(configDir, "privileges.db"))}
branch_control_file: ${JSON.stringify(join(configDir, "branch_control.db"))}
behavior:
  read_only: false
listener:
  host: 127.0.0.1
  port: ${server.port}
system_variables:
  dolt_force_transaction_commit: 0${userSessionBlock}
`,
    { mode: 0o600 },
  );
  chmodSync(server.configPath, 0o600);
  await startOwnedServer(server);
  return server;
}

function bootstrapSqlSafe(sql) {
  assert(typeof sql === "string", "bootstrap SQL must be a string");
  return sql;
}

async function startOwnedServer(server) {
  assert(!isRunning(server.process), "attempted to start an already-running server");
  server.stdout = "";
  server.stderr = "";
  const serverEnvironment = makeClientEnv(server.clientRoot);
  assertDoltProcessPreflight(serverEnvironment);
  const child = trackChild(
    spawn(doltBin, ["sql-server", `--config=${server.configPath}`], {
      cwd: server.root,
      env: { ...process.env, ...serverEnvironment },
      stdio: ["ignore", "pipe", "pipe"],
    }),
  );
  server.process = child;
  child.stdout.setEncoding("utf8");
  child.stderr.setEncoding("utf8");
  child.stdout.on("data", (chunk) => {
    server.stdout += chunk;
  });
  child.stderr.on("data", (chunk) => {
    server.stderr += chunk;
  });
  await new Promise((resolve, reject) => {
    child.once("spawn", resolve);
    child.once("error", reject);
  });
  assert(Number.isSafeInteger(child.pid) && child.pid > 1 && isRunning(child), "owned server did not start");

  for (let attempt = 0; attempt < 100; attempt += 1) {
    assert(isRunning(child), "owned server exited before readiness");
    const result = serverQuery(
      server,
      "control",
      server.databases[0],
      "SELECT run_id, identity_proof FROM harness_identity WHERE singleton = 1",
    );
    if (result.code === 0) {
      const row = firstRow(result);
      assert(row.run_id === server.runId && row.identity_proof === server.identityProof, "listener identity mismatch");
      assert(isRunning(child), "owned server exited during identity verification");
      assertNoCredentialArtifacts(server, "readiness");
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error("owned server did not become ready");
}

async function stopOwnedServer(server, signal = "SIGTERM") {
  if (!isRunning(server.process)) {
    return { requested_signal: signal, exit_observed: true };
  }
  const child = server.process;
  child.kill(signal);
  await waitForExit(child);
  assert(!isRunning(child), "owned server remained live after stop");
  assert(!containsSecret(`${server.stdout}\n${server.stderr}`), "server output exposed a credential");
  return {
    requested_signal: signal,
    exit_observed: true,
    termination: child.signalCode ?? child.exitCode,
  };
}

function assertNoCredentialArtifacts(server, phase) {
  assert(platform() === "linux", `credential artifact scan requires Linux during ${phase}`);
  const regularFiles = [];
  const visit = (path) => {
    const entry = lstatSync(path);
    if (entry.isDirectory()) {
      for (const child of readdirSync(path)) {
        visit(join(path, child));
      }
    } else if (entry.isFile()) {
      regularFiles.push(path);
    }
  };
  visit(server.root);
  const plaintextFiles = regularFiles.filter((path) => containsSecret(readFileSync(path)));
  assert(plaintextFiles.length === 0, `plaintext credential persisted during ${phase}`);
  assert((statSync(server.configPath).mode & 0o777) === 0o600, `server config mode drifted during ${phase}`);
  assert(!containsSecret(`${server.stdout}\n${server.stderr}`), `server output exposed a credential during ${phase}`);
  const argvBuffers = [readFileSync(`/proc/${process.pid}/cmdline`)];
  const environmentBuffers = [readFileSync(`/proc/${process.pid}/environ`)];
  if (isRunning(server.process)) {
    argvBuffers.push(readFileSync(`/proc/${server.process.pid}/cmdline`));
    environmentBuffers.push(readFileSync(`/proc/${server.process.pid}/environ`));
  }
  assert(!argvBuffers.some(containsSecret), `credential entered a persistent argv during ${phase}`);
  assert(!environmentBuffers.some(containsSecret), `credential entered a persistent environment during ${phase}`);
  const processScan = scanSurvivingProcessEnvironments(`credential artifact scan: ${phase}`);
  return {
    phase,
    regular_files_scanned: regularFiles.length,
    plaintext_credentials: 0,
    config_mode: "0600",
    linux_process_scan_asserted: true,
    readable_same_user_process_environments: processScan.readable_same_user_environments,
    scanned_owned_surviving_process_environments:
      processScan.scanned_owned_surviving_environments,
    unreadable_unrelated_same_user_process_environments:
      processScan.unreadable_unrelated_same_user_environments,
    owned_metrics_processes: processScan.owned_metrics_processes,
    credential_markers_in_surviving_process_environments: processScan.credential_markers,
  };
}

function concurrentServerQuery(server, role, database, sql) {
  return new Promise((resolve, reject) => {
    const args = remoteSqlArgs(server, database, sql);
    assert(!containsSecret(args.join("\u0000")), "credential entered concurrent subprocess argv");
    const clientEnvironment = serverRoleEnv(server, role);
    assertDoltProcessPreflight(clientEnvironment);
    const child = trackChild(
      spawn(doltBin, args, {
        env: { ...process.env, ...clientEnvironment },
        stdio: ["ignore", "pipe", "pipe"],
      }),
    );
    let stdout = "";
    let stderr = "";
    child.stdout.setEncoding("utf8");
    child.stderr.setEncoding("utf8");
    child.stdout.on("data", (chunk) => {
      stdout += chunk;
    });
    child.stderr.on("data", (chunk) => {
      stderr += chunk;
    });
    child.once("error", reject);
    child.once("close", (code, signal) => {
      if (containsSecret(`${stdout}\n${stderr}`)) {
        reject(new Error("credential entered concurrent subprocess output"));
      } else {
        resolve({ code: code ?? 1, signal, stdout, stderr });
      }
    });
  });
}

function openSqlSession(server, role, database) {
  const args = [
    "--host=127.0.0.1",
    `--port=${server.port}`,
    "--no-tls",
    `--use-db=${database}`,
    "sql",
    "-r",
    "csv",
  ];
  assert(!containsSecret(args.join("\u0000")), "credential entered streaming subprocess argv");
  const clientEnvironment = serverRoleEnv(server, role);
  assertDoltProcessPreflight(clientEnvironment);
  const child = trackChild(
    spawn(doltBin, args, {
      env: { ...process.env, ...clientEnvironment },
      stdio: ["pipe", "pipe", "pipe"],
    }),
  );
  const session = { child, stdout: "", stderr: "", error: undefined, closed: undefined };
  child.stdout.setEncoding("utf8");
  child.stderr.setEncoding("utf8");
  child.stdout.on("data", (chunk) => {
    session.stdout += chunk;
  });
  child.stderr.on("data", (chunk) => {
    session.stderr += chunk;
  });
  child.on("error", (error) => {
    session.error = error;
  });
  session.closed = new Promise((resolve) => {
    child.once("close", (code, signal) => {
      assert(!containsSecret(`${session.stdout}\n${session.stderr}`), "credential entered streaming subprocess output");
      resolve({ code: code ?? 1, signal, stdout: session.stdout, stderr: session.stderr });
    });
  });
  return session;
}

async function waitForSessionText(session, text, timeoutMs = 15_000) {
  const deadline = Date.now() + timeoutMs;
  while (!session.stdout.includes(text) && Date.now() < deadline) {
    if (session.error) {
      throw session.error;
    }
    assert(isRunning(session.child), `SQL session exited before marker ${text}`);
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  assert(session.stdout.includes(text), `SQL session missed marker ${text}`);
}

async function sharedServerRecovery() {
  return withOwnedRoot("shared-server-recovery", async ({ root }) => {
    const server = await createServerFixture(root, 2);
    const [firstDatabase, secondDatabase] = server.databases;
    for (const database of server.databases) {
      serverQuerySuccessfully(
        server,
        "control",
        database,
        "CREATE TABLE recovery_facts (id VARCHAR(64) PRIMARY KEY, payload VARCHAR(64) NOT NULL)",
      );
    }

    const [firstWriter, secondWriter] = await Promise.all([
      concurrentServerQuery(
        server,
        "writer",
        firstDatabase,
        "SET @@SESSION.dolt_force_transaction_commit=0; START TRANSACTION; INSERT INTO recovery_facts VALUES ('committed-a','database-a'); COMMIT",
      ),
      concurrentServerQuery(
        server,
        "writer",
        secondDatabase,
        "SET @@SESSION.dolt_force_transaction_commit=0; START TRANSACTION; INSERT INTO recovery_facts VALUES ('committed-b','database-b'); COMMIT",
      ),
    ]);
    assert(firstWriter.code === 0 && secondWriter.code === 0, "independent shared-server writers did not commit");

    const inflight = openSqlSession(server, "writer", firstDatabase);
    inflight.child.stdin.write(
      "SET @@SESSION.dolt_force_transaction_commit=0; START TRANSACTION; INSERT INTO recovery_facts VALUES ('interrupted','retry-me'); SELECT 'INFLIGHT_OPEN' AS marker;\n",
    );
    await waitForSessionText(inflight, "INFLIGHT_OPEN");
    const crash = await stopOwnedServer(server, "SIGKILL");
    inflight.child.stdin.end();
    const interruptedClient = await inflight.closed;
    assert(!isRunning(inflight.child), "in-flight client remained live after server termination and stdin close");

    await startOwnedServer(server);
    const firstAfterRestart = firstRow(
      serverQuerySuccessfully(
        server,
        "control",
        firstDatabase,
        "SELECT COUNT(*) AS rows_present, SUM(id='committed-a') AS committed_rows, SUM(id='interrupted') AS interrupted_rows FROM recovery_facts",
      ),
    );
    const secondAfterRestart = firstRow(
      serverQuerySuccessfully(
        server,
        "control",
        secondDatabase,
        "SELECT COUNT(*) AS rows_present, SUM(id='committed-b') AS committed_rows FROM recovery_facts",
      ),
    );
    assert(
      firstAfterRestart.rows_present === "1" && firstAfterRestart.committed_rows === "1" && firstAfterRestart.interrupted_rows === "0",
      "restart changed committed or uncommitted state in the first database",
    );
    assert(secondAfterRestart.rows_present === "1" && secondAfterRestart.committed_rows === "1", "restart changed the second database");

    serverQuerySuccessfully(
      server,
      "writer",
      firstDatabase,
      "SET @@SESSION.dolt_force_transaction_commit=0; START TRANSACTION; INSERT INTO recovery_facts VALUES ('interrupted','retry-me'); COMMIT",
    );
    const retried = firstRow(
      serverQuerySuccessfully(
        server,
        "control",
        firstDatabase,
        "SELECT COUNT(*) AS total_rows, SUM(id='interrupted') AS retry_rows FROM recovery_facts",
      ),
    );
    assert(retried.total_rows === "2" && retried.retry_rows === "1", "retry duplicated or lost the interrupted write");
    const credentialCheck = assertNoCredentialArtifacts(server, "after restart and retry");
    const graceful = await stopOwnedServer(server, "SIGTERM");

    const clientEnv = makeClientEnv(server.clientRoot);
    for (const database of server.databases) {
      doltSuccessfully(["fsck", "--quiet"], { cwd: join(root, database), env: clientEnv });
    }
    return {
      checks: [
        "two independent clients commit to two databases on one server",
        "SIGKILL restart preserves both committed databases",
        "the uncommitted transaction is absent after restart",
        "retry creates exactly one durable row",
        "offline fsck passes after graceful shutdown",
      ],
      evidence: {
        crash,
        graceful,
        committed_databases_after_restart: 2,
        uncommitted_rows_after_restart: 0,
        retry_rows: 1,
        credential_check: credentialCheck,
      },
    };
  });
}

function databaseFingerprint(server, database) {
  const tables = parseJson(
    serverQuerySuccessfully(
      server,
      "control",
      database,
      "SELECT table_name, column_name, ordinal_position, column_type, is_nullable, column_key FROM information_schema.columns WHERE table_schema=DATABASE() ORDER BY table_name, ordinal_position",
    ).stdout,
  ).rows;
  const rows = parseJson(
    serverQuerySuccessfully(
      server,
      "control",
      database,
      "SELECT id, payload FROM backup_facts ORDER BY id",
    ).stdout,
  ).rows;
  const branches = parseJson(
    serverQuerySuccessfully(
      server,
      "control",
      database,
      "SELECT name, hash FROM dolt_branches ORDER BY name",
    ).stdout,
  ).rows;
  const status = parseJson(
    serverQuerySuccessfully(
      server,
      "control",
      database,
      "SELECT table_name, staged, status FROM dolt_status ORDER BY table_name, staged, status",
    ).stdout,
  ).rows;
  return {
    digest: hashJson({ tables, rows, branches, status }),
    tables,
    rows,
    branches,
    status,
  };
}

function regularFilesUnder(root) {
  const files = [];
  const visit = (path) => {
    const entry = lstatSync(path);
    if (entry.isDirectory()) {
      for (const child of readdirSync(path)) {
        visit(join(path, child));
      }
    } else if (entry.isFile()) {
      files.push(path);
    }
  };
  visit(root);
  return files.sort();
}

function fileDigest(path) {
  return createHash("sha256").update(readFileSync(path)).digest("hex");
}

function measureBackupControlExclusions(server, database, backupDirectory) {
  const candidateControlFiles = [
    server.configPath,
    join(server.configDir, "privileges.db"),
    join(server.configDir, "branch_control.db"),
    metricsConfigPath(server.clientRoot),
    join(server.root, database, ".dolt", "config.json"),
    join(server.root, database, ".dolt", "repo_state.json"),
  ];
  const sourceControlFiles = candidateControlFiles.filter(
    (path) => existsSync(path) && statSync(path).size > 0,
  );
  const backupFiles = regularFilesUnder(backupDirectory);
  const sourceDigests = new Set(sourceControlFiles.map(fileDigest));
  const matchingDigests = backupFiles.filter((path) => sourceDigests.has(fileDigest(path)));
  const controlNames = new Set(sourceControlFiles.map((path) => basename(path)));
  const matchingNames = backupFiles.filter((path) => controlNames.has(basename(path)));
  const plaintextCredentialFiles = backupFiles.filter((path) => containsSecret(readFileSync(path)));
  assert(sourceControlFiles.length >= 4, "too few server/configuration control files were measured");
  assert(backupFiles.length > 0, "backup destination contains no regular files");
  assert(matchingDigests.length === 0, "backup copied an exact server/configuration control file");
  assert(matchingNames.length === 0, "backup contains a server/configuration control filename");
  assert(plaintextCredentialFiles.length === 0, "backup contains a plaintext credential marker");
  return {
    backup_regular_files_scanned: backupFiles.length,
    source_control_files_measured: sourceControlFiles.length,
    matching_source_control_file_digests: 0,
    matching_source_control_filenames: 0,
    plaintext_credential_files: 0,
  };
}

async function backupRestore() {
  return withOwnedRoot("backup-restore", async ({ root }) => {
    const server = await createServerFixture(root, 1);
    const database = server.databases[0];
    serverQuerySuccessfully(
      server,
      "control",
      database,
      "CREATE TABLE backup_facts (id VARCHAR(64) PRIMARY KEY, payload VARCHAR(128) NOT NULL); INSERT INTO backup_facts VALUES ('committed','history')",
    );
    serverQuerySuccessfully(server, "control", database, "CALL DOLT_ADD('-A'); CALL DOLT_COMMIT('-m','committed backup row')");
    serverQuerySuccessfully(
      server,
      "writer",
      database,
      "SET @@SESSION.dolt_force_transaction_commit=0; INSERT INTO backup_facts VALUES ('working-set','uncommitted-dolt-history')",
    );
    const source = databaseFingerprint(server, database);
    assert(source.status.length > 0, "source did not contain an uncommitted working-set change");

    const backupDirectory = join(root, "validated-backup");
    mkdirSync(backupDirectory, { mode: 0o700 });
    const backupUrl = fileRemote(backupDirectory);
    assert(!backupUrl.includes("'"), "backup URL cannot be safely represented in fixture SQL");
    serverQuerySuccessfully(
      server,
      "control",
      database,
      `CALL DOLT_BACKUP('sync-url', '${backupUrl}')`,
    );
    const backupControlExclusions = measureBackupControlExclusions(
      server,
      database,
      backupDirectory,
    );

    const restoredDatabase = `restored_${server.runId}`;
    serverQuerySuccessfully(
      server,
      "control",
      database,
      `CALL DOLT_BACKUP('restore', '${backupUrl}', '${restoredDatabase}')`,
    );
    const restored = databaseFingerprint(server, restoredDatabase);
    assert(source.digest === restored.digest, "restored logical/schema/history fingerprint differed from source");
    assert(restored.rows.length === 2, "restore omitted committed or working-set data");
    assert(restored.status.length > 0, "restore omitted the uncommitted working set");
    const credentialCheck = assertNoCredentialArtifacts(server, "after online backup and restore");

    const graceful = await stopOwnedServer(server, "SIGTERM");
    const clientEnv = makeClientEnv(server.clientRoot);
    doltSuccessfully(["fsck", "--quiet"], { cwd: join(root, database), env: clientEnv });
    doltSuccessfully(["fsck", "--quiet"], { cwd: join(root, restoredDatabase), env: clientEnv });
    return {
      checks: [
        "online DOLT_BACKUP snapshot completes against a running server",
        "restore targets a fresh database without force",
        "schema, branches, rows, and working-set status have one matching digest",
        "both source and restored stores pass offline fsck",
      ],
      evidence: {
        source_and_restore_digest_match: true,
        restored_committed_rows: 1,
        restored_working_set_rows: 1,
        restored_to_fresh_database: true,
        force_restore_used: false,
        measured_backup_control_exclusions: backupControlExclusions,
        credential_check: credentialCheck,
        graceful,
      },
    };
  });
}

function isEmpty(value) {
  return value === undefined || value === null || value === "";
}

function validateAggregateIdentitySchema(schema) {
  const violations = [];
  const rows = schema.index_rows
    .filter((row) => row.idx_table === "aggregates" && String(row.idx_non_unique) === "0")
    .sort((left, right) => Number(left.idx_position) - Number(right.idx_position));
  const primaryRows = rows.filter((row) => row.idx_name === "PRIMARY");
  if (primaryRows.length !== 1 || primaryRows[0].idx_column !== "id") {
    violations.push("aggregate_identity_not_exact_primary_id");
  }
  for (const row of primaryRows) {
    if (!isEmpty(row.idx_prefix)) {
      violations.push("prefix_index");
    }
    if (!isEmpty(row.idx_expression)) {
      violations.push("expression_index");
    }
    if (String(row.idx_nullable).toUpperCase() === "YES") {
      violations.push("nullable_index_column");
    }
    const column = schema.column_rows.find(
      (candidate) => candidate.fact_table === "aggregates" && candidate.fact_column === row.idx_column,
    );
    if (!column) {
      violations.push("missing_column_fact");
      continue;
    }
    if (String(column.fact_nullable).toUpperCase() !== "NO") {
      violations.push("nullable_column");
    }
    if (isEmpty(column.fact_collation)) {
      violations.push("missing_collation");
    } else if (!String(column.fact_collation).endsWith("_bin")) {
      violations.push("non_binary_collation");
    }
    if (![
      "varchar",
      "char",
      "binary",
      "varbinary",
      "bigint",
      "int",
      "smallint",
      "tinyint",
    ].includes(String(column.fact_type))) {
      violations.push("unsupported_identity_type");
    }
    const requiredWidth = Buffer.byteLength("aggregates.pk", "utf8") + 1 + Number(column.fact_octet_width);
    if (requiredWidth > Number(schema.ledger_width)) {
      violations.push("identity_width_overflow");
    }
  }
  return [...new Set(violations)].sort();
}

function aggregateSchemaFacts(server, database) {
  const indexRows = parseJson(
    serverQuerySuccessfully(
      server,
      "control",
      database,
      `SELECT table_name AS idx_table, index_name AS idx_name, non_unique AS idx_non_unique,
        seq_in_index AS idx_position, column_name AS idx_column, sub_part AS idx_prefix,
        expression AS idx_expression, nullable AS idx_nullable
       FROM information_schema.statistics
       WHERE table_schema=DATABASE() AND table_name='aggregates'
       ORDER BY index_name, seq_in_index`,
    ).stdout,
  ).rows;
  const columnRows = parseJson(
    serverQuerySuccessfully(
      server,
      "control",
      database,
      `SELECT table_name AS fact_table, column_name AS fact_column, data_type AS fact_type,
        character_octet_length AS fact_octet_width, is_nullable AS fact_nullable,
        collation_name AS fact_collation
       FROM information_schema.columns
       WHERE table_schema=DATABASE() AND table_name='aggregates'
       ORDER BY ordinal_position`,
    ).stdout,
  ).rows;
  const ledger = firstRow(
    serverQuerySuccessfully(
      server,
      "control",
      database,
      `SELECT character_maximum_length AS ledger_width
       FROM information_schema.columns
       WHERE table_schema=DATABASE() AND table_name='aggregates_identity' AND column_name='identity'`,
    ),
  );
  return { index_rows: indexRows, column_rows: columnRows, ledger_width: Number(ledger.ledger_width) };
}

function migrationFingerprint(server, database) {
  const facts = {
    metadata: parseJson(
      serverQuerySuccessfully(
        server,
        "control",
        database,
        "SELECT schema_version FROM schema_metadata WHERE singleton=1",
      ).stdout,
    ).rows,
    state: parseJson(
      serverQuerySuccessfully(
        server,
        "control",
        database,
        "SELECT project_id, state FROM project_runtime_state ORDER BY project_id",
      ).stdout,
    ).rows,
    aggregates: parseJson(
      serverQuerySuccessfully(
        server,
        "control",
        database,
        "SELECT * FROM aggregates ORDER BY id",
      ).stdout,
    ).rows,
    columns: parseJson(
      serverQuerySuccessfully(
        server,
        "control",
        database,
        "SELECT table_name, column_name, ordinal_position, column_type, is_nullable, collation_name FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name IN ('aggregates','schema_metadata','project_runtime_state','migration_history') ORDER BY table_name, ordinal_position",
      ).stdout,
    ).rows,
    history: parseJson(
      serverQuerySuccessfully(
        server,
        "control",
        database,
        "SELECT migration_id, from_version, to_version, outcome FROM migration_history ORDER BY migration_id",
      ).stdout,
    ).rows,
  };
  return { ...facts, digest: hashJson(facts) };
}

function factModelDriftMatrix(schema) {
  const clone = () => structuredClone(schema);
  const mutations = {
    collation: () => {
      const value = clone();
      value.column_rows.find((row) => row.fact_column === "id").fact_collation = "utf8mb4_0900_ai_ci";
      return value;
    },
    empty_collation: () => {
      const value = clone();
      value.column_rows.find((row) => row.fact_column === "id").fact_collation = "";
      return value;
    },
    width: () => {
      const value = clone();
      value.column_rows.find((row) => row.fact_column === "id").fact_octet_width = "800";
      return value;
    },
    nullability: () => {
      const value = clone();
      value.column_rows.find((row) => row.fact_column === "id").fact_nullable = "YES";
      value.index_rows.find((row) => row.idx_column === "id").idx_nullable = "YES";
      return value;
    },
    prefix: () => {
      const value = clone();
      value.index_rows.find((row) => row.idx_column === "id").idx_prefix = "16";
      return value;
    },
    expression: () => {
      const value = clone();
      value.index_rows.find((row) => row.idx_column === "id").idx_expression = "lower(`id`)";
      return value;
    },
  };
  return Object.fromEntries(
    Object.entries(mutations).map(([name, mutate]) => [name, validateAggregateIdentitySchema(mutate())]),
  );
}

async function schemaMigration() {
  return withOwnedRoot("schema-migration", async ({ root }) => {
    const server = await createServerFixture(root, 1);
    const database = server.databases[0];
    serverQuerySuccessfully(
      server,
      "control",
      database,
      `CREATE TABLE aggregates_identity (identity VARBINARY(640) NOT NULL PRIMARY KEY);
       CREATE TABLE aggregates (
         id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL PRIMARY KEY,
         version BIGINT NOT NULL,
         data JSON NOT NULL
       );
       CREATE TRIGGER aggregates_identity_guard BEFORE INSERT ON aggregates FOR EACH ROW
         INSERT INTO aggregates_identity(identity) VALUES (CONCAT('aggregates.pk', CHAR(31), NEW.id));
       CREATE TABLE schema_metadata (singleton TINYINT PRIMARY KEY, schema_version INT NOT NULL);
       CREATE TABLE project_runtime_state (project_id VARCHAR(64) PRIMARY KEY, state VARCHAR(32) NOT NULL);
       CREATE TABLE migration_history (
         migration_id VARCHAR(64) PRIMARY KEY,
         from_version INT NOT NULL,
         to_version INT NOT NULL,
         outcome VARCHAR(32) NOT NULL
       );
       INSERT INTO aggregates VALUES ('project-1',0,JSON_OBJECT('title','before'));
       INSERT INTO schema_metadata VALUES (1,1);
       INSERT INTO project_runtime_state VALUES ('project-1','active')`,
    );
    serverQuerySuccessfully(server, "control", database, "CALL DOLT_ADD('-A'); CALL DOLT_COMMIT('-m','schema version one')");
    serverQuerySuccessfully(
      server,
      "control",
      database,
      "UPDATE project_runtime_state SET state='paused' WHERE project_id='project-1'",
    );
    serverQuerySuccessfully(server, "control", database, "CALL DOLT_ADD('-A'); CALL DOLT_COMMIT('-m','pause for migration')");
    serverQuerySuccessfully(
      server,
      "control",
      database,
      "SET @@GLOBAL.dolt_force_transaction_commit=0; SET @@SESSION.dolt_force_transaction_commit=0",
    );
    const safeVariables = firstRow(
      serverQuerySuccessfully(
        server,
        "control",
        database,
        "SELECT @@SESSION.dolt_force_transaction_commit AS session_value, @@GLOBAL.dolt_force_transaction_commit AS global_value",
      ),
    );
    assert(safeVariables.session_value === "0" && safeVariables.global_value === "0", "migration safe variables were not zero");
    serverQuerySuccessfully(server, "control", database, "CALL DOLT_VERIFY_CONSTRAINTS() ");

    const liveSchema = aggregateSchemaFacts(server, database);
    assert(validateAggregateIdentitySchema(liveSchema).length === 0, "live aggregates.id schema failed validation");
    const factModelDrift = factModelDriftMatrix(liveSchema);
    const expectedFactModelDrift = {
      collation: "non_binary_collation",
      empty_collation: "missing_collation",
      width: "identity_width_overflow",
      nullability: "nullable_column",
      prefix: "prefix_index",
      expression: "expression_index",
    };
    for (const [name, expected] of Object.entries(expectedFactModelDrift)) {
      assert(
        factModelDrift[name].includes(expected),
        `${name} fact-model drift did not fail closed with ${expected}`,
      );
    }
    let migrationStarts = 0;
    for (const violations of Object.values(factModelDrift)) {
      if (violations.length === 0) {
        migrationStarts += 1;
      }
    }
    assert(migrationStarts === 0, "a drifted schema fact model reached migration execution");

    const versionOne = migrationFingerprint(server, database);
    const backupDirectory = join(root, "pre-migration-backup");
    mkdirSync(backupDirectory, { mode: 0o700 });
    const backupUrl = fileRemote(backupDirectory);
    assert(!backupUrl.includes("'"), "migration backup URL is unsafe");
    serverQuerySuccessfully(server, "control", database, `CALL DOLT_BACKUP('sync-url', '${backupUrl}')`);
    const validationDatabase = `validation_${server.runId}`;
    serverQuerySuccessfully(
      server,
      "control",
      database,
      `CALL DOLT_BACKUP('restore', '${backupUrl}', '${validationDatabase}')`,
    );
    assert(
      migrationFingerprint(server, validationDatabase).digest === versionOne.digest,
      "pre-migration backup validation restore differed",
    );

    const failureDatabase = `failure_${server.runId}`;
    serverQuerySuccessfully(
      server,
      "control",
      database,
      `CALL DOLT_BACKUP('restore', '${backupUrl}', '${failureDatabase}')`,
    );
    const failedMigration = serverQuery(
      server,
      "control",
      failureDatabase,
      `SET @@SESSION.dolt_force_transaction_commit=0;
       START TRANSACTION;
       ALTER TABLE aggregates ADD COLUMN abandoned VARCHAR(64) NULL;
       INSERT INTO aggregates (id,version,data) VALUES ('project-1',99,JSON_OBJECT('title','duplicate'));
       COMMIT`,
    );
    assert(failedMigration.code !== 0, "injected migration failure unexpectedly succeeded");
    const recoveryDatabase = `recovery_${server.runId}`;
    serverQuerySuccessfully(
      server,
      "control",
      database,
      `CALL DOLT_BACKUP('restore', '${backupUrl}', '${recoveryDatabase}')`,
    );
    const recovery = migrationFingerprint(server, recoveryDatabase);
    assert(recovery.digest === versionOne.digest, "verified backup did not recover exact version-one state");
    assert(recovery.state[0].state === "paused", "failure recovery did not remain paused");

    migrationStarts += 1;
    serverQuerySuccessfully(
      server,
      "control",
      database,
      `SET @@SESSION.dolt_force_transaction_commit=0;
       START TRANSACTION;
       ALTER TABLE aggregates ADD COLUMN summary VARCHAR(64) NULL;
       UPDATE aggregates SET version=version+1, summary='migrated'
         WHERE id='project-1' AND version=0;
       UPDATE schema_metadata SET schema_version=2 WHERE singleton=1 AND schema_version=1;
       INSERT INTO migration_history VALUES ('migrate-1-to-2',1,2,'applied');
       COMMIT`,
    );
    serverQuerySuccessfully(server, "control", database, "CALL DOLT_ADD('-A'); CALL DOLT_COMMIT('-m','schema version two')");
    const migrated = migrationFingerprint(server, database);
    assert(migrated.metadata[0].schema_version === "2", "schema version did not advance");
    assert(migrated.aggregates[0].version === "1" && migrated.aggregates[0].summary === "migrated", "aggregate migration did not apply exactly once");
    assert(migrated.state[0].state === "paused", "project resumed before post-migration verification");
    assert(validateAggregateIdentitySchema(aggregateSchemaFacts(server, database)).length === 0, "post-migration aggregate identity drifted");
    serverQuerySuccessfully(
      server,
      "control",
      database,
      "UPDATE project_runtime_state SET state='active' WHERE project_id='project-1' AND state='paused'",
    );
    serverQuerySuccessfully(server, "control", database, "CALL DOLT_ADD('-A'); CALL DOLT_COMMIT('-m','resume after verified migration')");
    const finalState = firstRow(
      serverQuerySuccessfully(
        server,
        "control",
        database,
        "SELECT state FROM project_runtime_state WHERE project_id='project-1'",
      ),
    );
    assert(finalState.state === "active", "project did not resume after successful verification");
    const credentialCheck = assertNoCredentialArtifacts(server, "after migration");
    const graceful = await stopOwnedServer(server, "SIGTERM");
    const clientEnv = makeClientEnv(server.clientRoot);
    for (const name of [database, validationDatabase, recoveryDatabase]) {
      doltSuccessfully(["fsck", "--quiet"], { cwd: join(root, name), env: clientEnv });
    }
    return {
      checks: [
        "project is paused and current store constraints/configuration are valid before migration",
        "live information_schema facts satisfy the aggregates.id validator baseline",
        "fact-model mutations cover non-binary and empty collation, width, nullability, prefix, and expression semantics",
        "every fact-model mutation prevents migration execution",
        "pre-migration backup is restored and fingerprinted before schema SQL",
        "injected migration failure is recoverable to exact paused version-one state",
        "successful migration advances schema and aggregate versions once before resume",
      ],
      evidence: {
        baseline_schema_violations: [],
        baseline_live_schema_facts: {
          collation: liveSchema.column_rows.find((row) => row.fact_column === "id")
            .fact_collation,
          octet_width: Number(
            liveSchema.column_rows.find((row) => row.fact_column === "id").fact_octet_width,
          ),
          nullable: liveSchema.column_rows.find((row) => row.fact_column === "id")
            .fact_nullable,
          index_nullable:
            liveSchema.index_rows.find((row) => row.idx_column === "id").idx_nullable ?? null,
          index_prefix:
            liveSchema.index_rows.find((row) => row.idx_column === "id").idx_prefix ?? null,
          index_expression:
            liveSchema.index_rows.find((row) => row.idx_column === "id").idx_expression ?? null,
        },
        fact_model_drift_violations: factModelDrift,
        blocked_fact_model_drift_migration_starts: 0,
        total_valid_migration_starts: migrationStarts,
        backup_validated_before_migration: true,
        failed_migration_exit: failedMigration.code,
        failure_recovery_digest_match: true,
        failure_recovery_state: "paused",
        final_schema_version: 2,
        final_aggregate_version: 1,
        expected_version_update_used: true,
        aggregate_odku_used: false,
        credential_check: credentialCheck,
        graceful,
      },
    };
  });
}

function prewriteDecision(observation, expectedIdentity) {
  if (observation.error_code) {
    return { allow_write: false, health_code: observation.error_code };
  }
  if (observation.identity !== expectedIdentity) {
    return { allow_write: false, health_code: "TASKSTORE_IDENTITY_MISMATCH" };
  }
  if (observation.session_value !== "0") {
    return { allow_write: false, health_code: "UNSAFE_SESSION_COMMIT_MODE" };
  }
  if (observation.global_value !== "0") {
    return { allow_write: false, health_code: "UNSAFE_GLOBAL_COMMIT_MODE" };
  }
  return { allow_write: true, health_code: "TASKSTORE_HEALTHY" };
}

async function guardedWrite(server, role, database, id, options = {}) {
  assert(/^[a-z0-9-]+$/.test(id), "unsafe guarded-write id");
  const session = openSqlSession(server, role, database);
  const expectedIdentity = options.expectedIdentity ?? server.runId;
  const initial = options.driftSessionFirst ? "SET @@SESSION.dolt_force_transaction_commit=1;" : "";
  session.child.stdin.write(
    `${initial}
     SET @@SESSION.dolt_force_transaction_commit=0;
     SELECT CONCAT('DIRECTOR_PREFLIGHT|', @@SESSION.dolt_force_transaction_commit, '|',
       @@GLOBAL.dolt_force_transaction_commit, '|', run_id) AS marker
     FROM harness_identity WHERE singleton=1;\n`,
  );
  try {
    await waitForSessionText(session, "DIRECTOR_PREFLIGHT|");
  } catch {
    if (isRunning(session.child)) {
      session.child.kill("SIGKILL");
    }
    const failed = await session.closed;
    return {
      written: false,
      health_code: classifyFailure(failed),
      exact_connection_checked: false,
    };
  }
  const match = session.stdout.match(/DIRECTOR_PREFLIGHT\|([^|\r\n"]+)\|([^|\r\n"]+)\|([a-z0-9]+)/);
  assert(match, "prewrite marker could not be parsed");
  const decision = prewriteDecision(
    { session_value: match[1], global_value: match[2], identity: match[3] },
    expectedIdentity,
  );
  if (!decision.allow_write) {
    session.child.stdin.end("ROLLBACK;\n");
    await session.closed;
    return { written: false, health_code: decision.health_code, exact_connection_checked: true };
  }
  session.child.stdin.end(
    `START TRANSACTION; INSERT INTO guarded_writes VALUES ('${id}'); COMMIT; SELECT 'DIRECTOR_WRITE_COMMITTED' AS marker;\n`,
  );
  const completed = await session.closed;
  assert(completed.code === 0 && completed.stdout.includes("DIRECTOR_WRITE_COMMITTED"), "guarded write did not commit");
  return { written: true, health_code: decision.health_code, exact_connection_checked: true };
}

async function configurationDrift() {
  return withOwnedRoot("configuration-drift", async ({ root }) => {
    const server = await createServerFixture(root, 1, { writerSessionDefault: true });
    const database = server.databases[0];
    serverQuerySuccessfully(server, "control", database, "CREATE TABLE guarded_writes (id VARCHAR(64) PRIMARY KEY)");
    serverQuerySuccessfully(
      server,
      "control",
      database,
      "SET @@GLOBAL.dolt_force_transaction_commit=0; SELECT @@GLOBAL.dolt_force_transaction_commit AS global_value",
    );

    const safe = await guardedWrite(server, "writer", database, "safe");
    assert(safe.written, "safe prewrite configuration was rejected");
    const correctedSession = await guardedWrite(server, "control", database, "session-corrected", {
      driftSessionFirst: true,
    });
    assert(correctedSession.written, "session drift was not reset and verified on the exact write connection");
    const identityMismatch = await guardedWrite(server, "control", database, "identity-mismatch", {
      expectedIdentity: "wrongidentity",
    });
    assert(!identityMismatch.written && identityMismatch.health_code === "TASKSTORE_IDENTITY_MISMATCH", "identity mismatch did not fail closed");

    serverQuerySuccessfully(server, "control", database, "SET @@GLOBAL.dolt_force_transaction_commit=1");
    const globalDrift = await guardedWrite(server, "control", database, "global-drift");
    assert(!globalDrift.written && globalDrift.health_code === "UNSAFE_GLOBAL_COMMIT_MODE", "global drift did not fail closed");
    const duplicateInitialization = await guardedWrite(server, "writer", database, "duplicate-init");
    assert(
      !duplicateInitialization.written && duplicateInitialization.health_code === "DUPLICATE_INITIALIZATION",
      "duplicate initialization did not fail closed",
    );
    serverQuerySuccessfully(server, "control", database, "SET @@GLOBAL.dolt_force_transaction_commit=0");

    const stopped = await stopOwnedServer(server, "SIGTERM");
    const connectionReset = await guardedWrite(server, "writer", database, "connection-reset");
    assert(!connectionReset.written && connectionReset.health_code === "CONNECTION_UNAVAILABLE", "connection reset did not fail closed");
    await startOwnedServer(server);

    const faultMatrix = {
      set_error: prewriteDecision({ error_code: "SESSION_SET_FAILED" }, server.runId),
      read_error: prewriteDecision({ error_code: "COMMIT_MODE_READ_FAILED" }, server.runId),
      unexpected_session: prewriteDecision(
        { identity: server.runId, session_value: "unexpected", global_value: "0" },
        server.runId,
      ),
      unexpected_global: prewriteDecision(
        { identity: server.runId, session_value: "0", global_value: "unexpected" },
        server.runId,
      ),
      identity_mismatch: prewriteDecision(
        { identity: "other", session_value: "0", global_value: "0" },
        server.runId,
      ),
    };
    assert(Object.values(faultMatrix).every((decision) => !decision.allow_write), "fault matrix permitted a write");
    const rows = parseJson(
      serverQuerySuccessfully(
        server,
        "control",
        database,
        "SELECT id FROM guarded_writes ORDER BY id",
      ).stdout,
    ).rows;
    assert(
      JSON.stringify(rows.map((row) => row.id)) === JSON.stringify(["safe", "session-corrected"]),
      `a fail-closed probe wrote data: ${JSON.stringify(rows)}`,
    );
    const credentialCheck = assertNoCredentialArtifacts(server, "after configuration drift matrix");
    const graceful = await stopOwnedServer(server, "SIGTERM");
    return {
      checks: [
        "startup control connection sets and reads global safe mode",
        "the exact write connection resets and reads session/global mode before SQL",
        "global drift and duplicate initialization prevent writes",
        "connection loss and listener identity mismatch prevent writes without fallback",
        "set/read errors and unexpected values fail closed in the adapter decision matrix",
        "only the two safe writes are durable and health output is bounded",
      ],
      evidence: {
        safe_write: safe,
        corrected_session_write: correctedSession,
        denied_health_codes: [
          identityMismatch.health_code,
          globalDrift.health_code,
          duplicateInitialization.health_code,
          connectionReset.health_code,
          ...Object.values(faultMatrix).map((decision) => decision.health_code),
        ].sort(),
        durable_row_ids: rows.map((row) => row.id),
        fallback_connections_used: 0,
        raw_sql_or_credentials_exposed_to_caller: false,
        first_stop: stopped,
        credential_check: credentialCheck,
        graceful,
      },
    };
  });
}

async function supportedTopology() {
  return withOwnedRoot("supported-topology", async ({ root }) => {
    const clientRoot = join(root, "client-root");
    const clientEnv = makeClientEnv(clientRoot);
    const doltVersion = doltSuccessfully(["version"], { env: clientEnv }).stdout.trim();
    const gitVersion = gitSuccessfully(["--version"]).stdout.trim();
    const remoteHelp = doltSuccessfully(["remote", "--help"], { env: clientEnv }).stdout;
    const backupHelp = doltSuccessfully(["backup", "--help"], { env: clientEnv }).stdout;
    assert(doltVersion === "dolt version 2.3.2", `unexpected Dolt version: ${doltVersion}`);
    assert(platform() === "linux", `unsupported evidence host: ${platform()}`);
    assert(remoteHelp.includes("refs/dolt/data") && remoteHelp.includes("git remotes"), "installed remote help lacks Git-ref support");
    assert(backupHelp.includes("file schemes") && backupHelp.includes("working sets"), "installed backup help lacks required snapshot behavior");
    return {
      contract_result: "PASS",
      checks: [
        "installed Dolt is the exact selected 2.3.2 release",
        "installed CLI documents refs/dolt/data Git remotes and working-set backups",
        "fixture uses direct argv spawning and Node path/file-URL APIs",
        "the process is running on the supported Linux topology",
      ],
      evidence: {
        dolt: doltVersion,
        git: gitVersion,
        node: process.version,
        host: `${type()} ${release()} ${arch()}`,
        process_platform: platform(),
        supported_runtime_executed: true,
        common_process_contract: "direct argv; no shell",
        common_path_contract: "Node path and pathToFileURL; no hard-coded separator or temporary directory",
      },
    };
  });
}

const propertyRunners = {
  "remote-layout": remoteLayout,
  "partial-git-failure": partialGitFailure,
  "partial-dolt-failure": partialDoltFailure,
  "shared-server-recovery": sharedServerRecovery,
  "backup-restore": backupRestore,
  "schema-migration": schemaMigration,
  "configuration-drift": configurationDrift,
  "supported-topology": supportedTopology,
};

function environmentEvidence(results) {
  const topology = results.find((result) => result.property === "supported-topology");
  assert(topology?.evidence, "supported-topology evidence is absent");
  return {
    dolt: topology.evidence.dolt,
    git: topology.evidence.git,
    node: topology.evidence.node,
    operating_system: topology.evidence.host,
  };
}

async function runAll() {
  const results = [];
  for (const property of properties) {
    const result = execute(process.execPath, [scriptPath, property], { timeout: 10 * 60_000 });
    if (result.code !== 0) {
      throw new Error(`focused property ${property} failed with exit ${result.code}: ${result.stderr || result.stdout}`);
    }
    results.push(parseJson(result.stdout));
  }
  return {
    schema_version: 1,
    task: "dir-m0.5",
    environment: environmentEvidence(results),
    execution_contract: "one fresh disposable process and owned data root per property",
    properties: results,
    summary: {
      focused_properties: results.length,
      pass: results.filter((result) => result.contract_result === "PASS").length,
      failed: results.filter((result) => result.contract_result === "FAIL").length,
      credentials_or_raw_sql_exposed_to_callers: false,
      shared_development_beads_server_touched: false,
      unrelated_repositories_touched: false,
    },
    decision_evidence: {
      supported_linux_operations: "GO",
      overall: "GO",
      blocker: null,
    },
  };
}

async function main() {
  const requested = process.argv[2];
  if (requested === "all") {
    process.stdout.write(`${JSON.stringify(await runAll(), null, 2)}\n`);
    return;
  }
  const runner = propertyRunners[requested];
  assert(runner, `usage: ${basename(scriptPath)} <${["all", ...properties].join("|")}>`);
  process.stdout.write(`${JSON.stringify(await runner(), null, 2)}\n`);
}

main().catch(async (error) => {
  try {
    await stopTrackedChildren();
  } catch {
    // The original failure is more actionable; owned roots are still removed by their finally blocks.
  }
  const message = [...secretValues].reduce(
    (redacted, secret) => redacted.replaceAll(secret, "<redacted>"),
    error?.stack ?? String(error),
  );
  process.stderr.write(`${message}\n`);
  process.exitCode = 1;
});
