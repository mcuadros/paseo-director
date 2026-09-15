// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { createHash, randomBytes } from "node:crypto";
import {
  accessSync,
  chmodSync,
  constants,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  realpathSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { basename, delimiter, join } from "node:path";
import { fileURLToPath } from "node:url";
import { spawn } from "node:child_process";
import { setTimeout as delay } from "node:timers/promises";
import test from "node:test";

import {
  CoordinatorError,
  defaultCommandRunner,
  errorOutput,
  execute,
  parseCli,
} from "./coordinator.mjs";

const TASK = "dir-m1.23";
const TITLE = "Support protected Paseo daemon authentication in coordinator handoff";
const ACTOR = "paseo:11111111-2222-4333-8444-555555555555";
const BRANCH = "task/dir-m1.23-paseo-auth-test";
const AGENT_ID = "agent-auth-0001";
const WORKSPACE_ID = "workspace-auth-0001";
const REPOSITORY = "acme/director";
const MCP_FIXTURE = fileURLToPath(new URL("./paseo-mcp.fixture.mjs", import.meta.url));

function command(executable, args, options = {}) {
  const result = defaultCommandRunner(executable, args, options);
  assert.equal(result.error, undefined, result.error?.message);
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim();
}

function git(cwd, args) {
  return command("git", args, { cwd });
}

function executableOnPath(name) {
  for (const directory of (process.env.PATH ?? "").split(delimiter)) {
    const path = join(directory, name);
    try {
      accessSync(path, constants.X_OK);
      return realpathSync(path);
    } catch {
      // Keep searching the caller's selected PATH.
    }
  }
  throw new Error(`${name} is unavailable on PATH`);
}

function filesBelow(root) {
  const files = [];
  for (const entry of readdirSync(root, { withFileTypes: true })) {
    const path = join(root, entry.name);
    if (entry.isDirectory()) files.push(...filesBelow(path));
    else if (entry.isFile()) files.push(path);
  }
  return files;
}

function replaceEnvironment(changes) {
  const previous = new Map();
  for (const [key, value] of Object.entries(changes)) {
    previous.set(key, process.env[key]);
    if (value === undefined) delete process.env[key];
    else process.env[key] = value;
  }
  return () => {
    for (const [key, value] of previous) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
  };
}

function writeFakeCommands(fixture) {
  const realGit = executableOnPath("git");
  const script = `#!${process.execPath}
const { appendFileSync, writeFileSync } = require("node:fs");
const { basename } = require("node:path");
const { spawnSync } = require("node:child_process");

const logPath = ${JSON.stringify(fixture.processLogPath)};
const socketPath = ${JSON.stringify(fixture.socketPath)};
const checkout = ${JSON.stringify(fixture.checkout)};
const title = ${JSON.stringify(TITLE)};
const task = ${JSON.stringify(TASK)};
const actor = ${JSON.stringify(ACTOR)};
const repository = ${JSON.stringify(REPOSITORY)};
const realGit = ${JSON.stringify(realGit)};
const name = basename(process.argv[1]);
const args = process.argv.slice(2);
const forbiddenAmbient = [
  "AWS_SECRET_ACCESS_KEY",
  "DIRECTOR_PRIVATE_TOKEN",
  "GH_TOKEN",
  "GITHUB_TOKEN",
  "GOAUTH",
  "NPM_TOKEN",
  "PASEO_PASSWORD",
  "PASEO_PASSWORD_FILE",
  "DIRECTOR_PASEO_CREDENTIAL_FILE",
].filter((key) => process.env[key] !== undefined);
const beadsDir = process.env.BEADS_DIR ?? null;
appendFileSync(logPath, JSON.stringify({ name, args, beadsDir, forbiddenAmbient }) + "\\n");
if (forbiddenAmbient.length > 0) process.exit(86);

function output(value) {
  writeFileSync(1, value);
  process.exit(0);
}

if (name === "git") {
  const remoteIndex = args.indexOf("remote");
  if (
    remoteIndex >= 0 && args[remoteIndex + 1] === "get-url" &&
    args[remoteIndex + 2] === "origin"
  ) {
    output("https://github.com/acme/director.git\\n");
  }
  const result = spawnSync(realGit, args, {
    cwd: process.cwd(), env: process.env, stdio: "inherit",
  });
  process.exit(result.status ?? 88);
}

if (name === "bd") {
  if (args.includes("comments")) output("[]");
  output(JSON.stringify([{
    id: task,
    title,
    issue_type: "task",
    status: "in_progress",
    assignee: actor,
    acceptance_criteria: "Protected lifecycle reads are secret-safe.",
  }]));
}

if (name === "gh") {
  const endpoint = args[1] ?? "";
  if (endpoint === "repos/" + repository) {
    output(JSON.stringify({
      id: 42,
      full_name: repository,
      archived: false,
      disabled: false,
    }));
  }
  if (endpoint.startsWith("repos/" + repository + "/pulls?")) output("[]");
  process.exit(91);
}

if (name === "paseo") {
  if (
    args.length === 3 && args[0] === "daemon" && args[1] === "status" &&
    args[2] === "--json"
  ) {
    output(JSON.stringify({ localDaemon: "running", listen: "unix://" + socketPath }));
  }
  // The corrected path must never return to these subprotocol-based reads or
  // mutations; only password-free target discovery may reach the CLI.
  if (args[0] === "inspect" || args[0] === "workspace" || args[0] === "archive") {
    process.exit(92);
  }
  output("{}");
}

output("{}");
`;
  for (const name of ["bd", "gh", "git", "go", "npm", "paseo", "paseo-sibling"]) {
    const path = join(fixture.bin, name);
    writeFileSync(path, script);
    chmodSync(path, 0o755);
  }
}

function createFixture() {
  const root = mkdtempSync(join(tmpdir(), "director-paseo-auth-test-"));
  const origin = join(root, "origin.git");
  const control = join(root, "control");
  const checkout = join(root, "checkout");
  const bin = join(root, "bin");
  const validationFile = join(root, "validation.json");
  const processLogPath = join(root, "process-log.jsonl");
  const requestLogPath = join(root, "request-log.jsonl");
  const modePath = join(root, "mode");
  const readyPath = join(root, "ready");
  const socketPath = join(root, "paseo.sock");
  const credentialFile = join(root, "paseo-password");
  const ownershipFile = join(root, "ownership");
  const serverFixturePath = join(root, "server-fixture.json");
  const password = `valid,slash/equal=semi;colon:quote"backslash\\plus+at@${randomBytes(16).toString("hex")}`;
  const wrongPassword = `wrong,credential/${randomBytes(16).toString("hex")}`;
  const ambientPassword = `ambient,ignored/${randomBytes(16).toString("hex")}`;
  const ownership = "dir-m1.23-auth-fixture-0001";
  mkdirSync(control);
  mkdirSync(bin);
  git(root, ["init", "--bare", "--quiet", origin]);
  git(control, ["init", "--quiet", "--initial-branch=main"]);
  git(control, ["config", "user.name", "Director Test"]);
  git(control, ["config", "user.email", "director@example.invalid"]);
  writeFileSync(join(control, "README.md"), "base\n");
  git(control, ["add", "README.md"]);
  git(control, ["commit", "--quiet", "-m", "base"]);
  const base = git(control, ["rev-parse", "HEAD"]);
  git(control, ["remote", "add", "origin", origin]);
  git(control, ["push", "--quiet", "origin", "main"]);
  git(control, ["worktree", "add", "--quiet", "-b", BRANCH, checkout, "main"]);
  writeFileSync(join(checkout, "candidate.txt"), "candidate\n");
  git(checkout, ["add", "candidate.txt"]);
  git(checkout, ["commit", "--quiet", "-m", "candidate"]);
  const candidate = git(checkout, ["rev-parse", "HEAD"]);
  writeFileSync(
    validationFile,
    `${JSON.stringify({
      schemaVersion: 1,
      task: TASK,
      candidate,
      base,
      checks: [{ id: "focused", command: ["node", "--test"], status: "passed" }],
    })}\n`,
  );
  writeFileSync(processLogPath, "");
  writeFileSync(requestLogPath, "");
  writeFileSync(modePath, "success\n");
  writeFileSync(credentialFile, password, { mode: 0o600 });
  writeFileSync(ownershipFile, ownership, { mode: 0o600 });
  writeFileSync(serverFixturePath, JSON.stringify({
    agentId: AGENT_ID,
    checkout,
    title: TITLE,
    workspaceId: WORKSPACE_ID,
  }));
  const fixture = {
    root,
    origin,
    control,
    checkout,
    bin,
    validationFile,
    processLogPath,
    requestLogPath,
    modePath,
    readyPath,
    socketPath,
    credentialFile,
    ownershipFile,
    serverFixturePath,
    password,
    wrongPassword,
    ambientPassword,
    ownership,
    base,
    candidate,
  };
  fixture.options = {
    task: TASK,
    actor: ACTOR,
    repo: REPOSITORY,
    "repo-id": "42",
    remote: "origin",
    "base-ref": "main",
    base,
    branch: BRANCH,
    candidate,
    "head-owner": "acme",
    ownership,
    checkout,
    "checkout-state": "present",
    "control-repo": control,
    "agent-id": AGENT_ID,
    "workspace-id": WORKSPACE_ID,
    "lifecycle-state": "active",
    pr: "absent",
    "validation-file": validationFile,
  };
  writeFakeCommands(fixture);
  return fixture;
}

function cliArguments(fixture) {
  const argv = ["review-handoff"];
  for (const [key, value] of Object.entries(fixture.options)) {
    if (key !== "ownership") argv.push(`--${key}`, String(value));
  }
  argv.push("--ownership-file", fixture.ownershipFile);
  return argv;
}

function processLog(fixture) {
  return readFileSync(fixture.processLogPath, "utf8")
    .trim()
    .split("\n")
    .filter(Boolean)
    .map((line) => JSON.parse(line));
}

function requestLog(fixture) {
  return readFileSync(fixture.requestLogPath, "utf8")
    .trim()
    .split("\n")
    .filter(Boolean)
    .map((line) => JSON.parse(line));
}

async function startMcpServer(fixture) {
  rmSync(fixture.readyPath, { force: true });
  rmSync(fixture.socketPath, { force: true });
  const passwordHash = createHash("sha256").update(fixture.password).digest("hex");
  const child = spawn(process.execPath, [
    MCP_FIXTURE,
    fixture.socketPath,
    fixture.modePath,
    fixture.readyPath,
    fixture.requestLogPath,
    passwordHash,
    fixture.serverFixturePath,
  ], {
    env: { PATH: process.env.PATH },
    stdio: ["ignore", "pipe", "pipe"],
  });
  let diagnostic = "";
  child.stderr.on("data", (chunk) => {
    diagnostic += chunk.toString("utf8");
  });
  assert.equal(child.spawnargs.some((value) => value.includes(fixture.password)), false);
  const deadline = Date.now() + 5_000;
  while (!existsSync(fixture.readyPath)) {
    if (child.exitCode !== null) {
      throw new Error(`Paseo MCP fixture exited before ready: ${diagnostic}`);
    }
    if (Date.now() >= deadline) throw new Error("Paseo MCP fixture did not become ready");
    await delay(10);
  }
  return child;
}

async function stopMcpServer(child) {
  if (child.exitCode !== null) return;
  child.kill("SIGTERM");
  await new Promise((resolve) => child.once("exit", resolve));
}

function assertRefusal(error, code, options, secrets) {
  assert.ok(error instanceof CoordinatorError);
  assert.equal(error.code, code);
  const output = JSON.stringify(errorOutput(
    "review-handoff",
    error,
    options.ownership,
    options.ownershipFile,
    options.paseoPassword,
    options.paseoCredentialFile,
  ));
  for (const secret of secrets) assert.equal(output.includes(secret), false);
  assert.ok(Buffer.byteLength(output) < 1_024, output);
  return true;
}

test("exact Paseo 0.7.2 WebSocket bearer shape rejects delimiter-rich valid passwords", () => {
  const password = "valid,password/with=delimiters;and:quotes\"";
  assert.throws(
    () => new WebSocket("ws://127.0.0.1:1", [`paseo.bearer.${password}`]),
    { name: "SyntaxError" },
  );
});

test("public local Paseo MCP lifecycle reads isolate credentials and fail closed", async () => {
  const fixture = createFixture();
  let server;
  const previousPath = process.env.PATH;
  const restore = replaceEnvironment({
    PATH: `${fixture.bin}:${previousPath}`,
    PASEO_HOST: undefined,
    PASEO_PASSWORD: fixture.ambientPassword,
    DIRECTOR_PASEO_CREDENTIAL_FILE: fixture.credentialFile,
    AWS_SECRET_ACCESS_KEY: "ambient-aws-secret",
    DIRECTOR_PRIVATE_TOKEN: "ambient-director-secret",
    GH_TOKEN: "ambient-gh-secret",
    GITHUB_TOKEN: "ambient-github-secret",
    GOAUTH: "ambient-go-secret",
    NPM_TOKEN: "ambient-npm-secret",
  });
  try {
    server = await startMcpServer(fixture);
    let parsed = parseCli(cliArguments(fixture));
    assert.equal(parsed.options.paseoPassword, fixture.password);
    assert.equal(parsed.options.paseoCredentialFile, fixture.credentialFile);
    assert.equal(JSON.stringify(parsed).includes(fixture.password), false);
    assert.equal(JSON.stringify(parsed).includes(fixture.credentialFile), false);

    const first = await execute(parsed.command, parsed.options);
    assert.equal(first.result.snapshot.paseo.verified, true);
    assert.equal(first.result.snapshot.paseo.agent.id, AGENT_ID);
    assert.equal(first.result.snapshot.paseo.workspace.id, WORKSPACE_ID);
    assert.equal(JSON.stringify(first).includes(fixture.password), false);
    writeFileSync(
      join(fixture.root, "review-handoff-manifest.json"),
      `${JSON.stringify(first)}\n`,
    );

    for (const executable of ["npm", "go", "paseo-sibling"]) {
      const result = defaultCommandRunner(executable, ["probe"], {
        paseoPassword: parsed.options.paseoPassword,
      });
      assert.equal(result.status, 0, `${executable}: ${result.stderr}`);
    }
    let childEntries = processLog(fixture);
    assert.deepEqual(
      childEntries
        .filter((entry) => entry.name === "paseo")
        .map((entry) => entry.args),
      [
        ["daemon", "status", "--json"],
        ["daemon", "status", "--json"],
      ],
    );
    assert.equal(childEntries.every((entry) => entry.forbiddenAmbient.length === 0), true);
    const mcpEntries = requestLog(fixture);
    assert.deepEqual(mcpEntries.map((entry) => entry.tool), [
      "get_agent_status",
      "list_workspaces",
    ]);
    assert.equal(mcpEntries.every((entry) => entry.authorizationPresent), true);
    assert.equal(
      mcpEntries.every((entry) =>
        entry.authorizationHash === createHash("sha256").update(fixture.password).digest("hex")),
      true,
    );
    assert.equal(JSON.stringify(mcpEntries).includes(fixture.password), false);

    // An explicit same-host Unix socket remains supported without putting the
    // target or credential in child argv.
    process.env.PASEO_HOST = `unix://${fixture.socketPath}`;
    const beforeExplicit = processLog(fixture).filter(
      (entry) => entry.name === "paseo" && entry.args[0] === "daemon",
    ).length;
    const explicit = await execute(parsed.command, parsed.options);
    assert.equal(explicit.result.snapshot.paseo.verified, true);
    assert.equal(
      processLog(fixture).filter(
        (entry) => entry.name === "paseo" && entry.args[0] === "daemon",
      ).length,
      beforeExplicit,
    );

    // Credentials in a connection URI are a second input authority and are
    // refused before any child process starts.
    process.env.PASEO_HOST = `tcp://127.0.0.1:17693/?password=${encodeURIComponent(fixture.password)}`;
    assert.throws(
      () => defaultCommandRunner("git", ["--version"], {
        paseoPassword: parsed.options.paseoPassword,
      }),
      (error) => assertRefusal(
        error,
        "PASEO_AUTH_LOCATION_UNSUPPORTED",
        parsed.options,
        [fixture.password, fixture.credentialFile],
      ),
    );
    delete process.env.PASEO_HOST;

    process.env.PASEO_HOST = "198.51.100.10:6767";
    await assert.rejects(
      execute(parsed.command, parsed.options),
      (error) => assertRefusal(
        error,
        "PASEO_LIFECYCLE_READ_FAILED",
        parsed.options,
        [fixture.password, fixture.credentialFile],
      ),
    );
    delete process.env.PASEO_HOST;

    // Ambient plaintext is ignored; removing the owner-only credential-file
    // authority fails before the lifecycle helper starts.
    delete process.env.DIRECTOR_PASEO_CREDENTIAL_FILE;
    parsed = parseCli(cliArguments(fixture));
    await assert.rejects(
      execute(parsed.command, parsed.options),
      (error) => assertRefusal(
        error,
        "PASEO_AUTH_REQUIRED",
        parsed.options,
        [fixture.ambientPassword],
      ),
    );

    process.env.DIRECTOR_PASEO_CREDENTIAL_FILE = fixture.credentialFile;
    writeFileSync(fixture.credentialFile, fixture.wrongPassword, { mode: 0o600 });
    parsed = parseCli(cliArguments(fixture));
    await assert.rejects(
      execute(parsed.command, parsed.options),
      (error) => assertRefusal(
        error,
        "PASEO_AUTH_FAILED",
        parsed.options,
        [fixture.wrongPassword],
      ),
    );

    writeFileSync(fixture.credentialFile, fixture.password, { mode: 0o600 });
    parsed = parseCli(cliArguments(fixture));
    for (const mode of ["malformed-json", "malformed-output", "response-loss"]) {
      writeFileSync(fixture.modePath, `${mode}\n`);
      await assert.rejects(
        execute(parsed.command, parsed.options),
        (error) => assertRefusal(
          error,
          "PASEO_LIFECYCLE_READ_FAILED",
          parsed.options,
          [fixture.password],
        ),
      );
    }

    writeFileSync(fixture.modePath, "echo-secret\n");
    await assert.rejects(
      execute(parsed.command, parsed.options),
      (error) => assertRefusal(
        error,
        "PASEO_LIFECYCLE_RESPONSE_REDACTED",
        parsed.options,
        [fixture.password],
      ),
    );

    for (const [mode, code] of [
      ["wrong-agent", "PASEO_AGENT_MISMATCH"],
      ["wrong-workspace", "PASEO_WORKSPACE_AMBIGUOUS"],
      ["parented", "PASEO_AGENT_PARENTED"],
    ]) {
      writeFileSync(fixture.modePath, `${mode}\n`);
      await assert.rejects(
        execute(parsed.command, parsed.options),
        (error) => assertRefusal(error, code, parsed.options, [fixture.password]),
      );
    }

    writeFileSync(fixture.modePath, "timeout\n");
    const timeoutRun = (executable, args, options = {}) => defaultCommandRunner(
      executable,
      args,
      { ...options, paseoPassword: parsed.options.paseoPassword, timeout: 100 },
    );
    await assert.rejects(
      execute(parsed.command, parsed.options, { run: timeoutRun }),
      (error) => assertRefusal(
        error,
        "PASEO_LIFECYCLE_READ_FAILED",
        parsed.options,
        [fixture.password],
      ),
    );

    writeFileSync(fixture.modePath, "success\n");
    await stopMcpServer(server);
    server = undefined;
    await assert.rejects(
      execute(parsed.command, parsed.options),
      (error) => assertRefusal(
        error,
        "PASEO_LIFECYCLE_READ_FAILED",
        parsed.options,
        [fixture.password],
      ),
    );

    server = await startMcpServer(fixture);
    const retry = await execute(parsed.command, parsed.options);
    assert.deepEqual(retry, first);

    childEntries = processLog(fixture);
    assert.equal(childEntries.every((entry) => entry.forbiddenAmbient.length === 0), true);
    assert.equal(lstatSync(fixture.credentialFile).mode & 0o077, 0);
    assert.equal(readFileSync(fixture.credentialFile, "utf8"), fixture.password);
    for (const path of filesBelow(fixture.root)) {
      if (path === fixture.credentialFile || !lstatSync(path).isFile()) continue;
      const contents = readFileSync(path);
      for (const secret of [fixture.password, fixture.wrongPassword, fixture.ambientPassword]) {
        assert.equal(contents.includes(Buffer.from(secret)), false, basename(path));
      }
    }
  } finally {
    if (server !== undefined) await stopMcpServer(server);
    restore();
    rmSync(fixture.root, { recursive: true, force: true });
  }
});

test("public local Paseo MCP lifecycle mutations authenticate and fail closed", async () => {
  const fixture = createFixture();
  let server;
  const previousPath = process.env.PATH;
  const restore = replaceEnvironment({
    PATH: `${fixture.bin}:${previousPath}`,
    PASEO_HOST: undefined,
    PASEO_PASSWORD: fixture.ambientPassword,
    DIRECTOR_PASEO_CREDENTIAL_FILE: fixture.credentialFile,
    AWS_SECRET_ACCESS_KEY: "ambient-aws-secret",
    DIRECTOR_PRIVATE_TOKEN: "ambient-director-secret",
    GH_TOKEN: "ambient-gh-secret",
    GITHUB_TOKEN: "ambient-github-secret",
    GOAUTH: "ambient-go-secret",
    NPM_TOKEN: "ambient-npm-secret",
  });
  const archiveAgent = ["archive", AGENT_ID, "--json"];
  const archiveWorkspace = ["workspace", "archive", WORKSPACE_ID, "--json"];
  const mutate = (args, password, options = {}) =>
    defaultCommandRunner("paseo", args, { ...options, paseoPassword: password });
  const requestsFor = (tool) => requestLog(fixture).filter((entry) => entry.tool === tool);
  try {
    server = await startMcpServer(fixture);

    // This is the exact failing case: a valid configured password whose
    // delimiters the CLI's WebSocket subprotocol grammar cannot carry.
    assert.throws(
      () => new WebSocket("ws://127.0.0.1:1", [`paseo.bearer.${fixture.password}`]),
      { name: "SyntaxError" },
    );

    // Without the owner-only credential authority a mutation refuses before any
    // child starts, so no effect can land unauthenticated.
    delete process.env.DIRECTOR_PASEO_CREDENTIAL_FILE;
    const unauthenticated = parseCli(cliArguments(fixture));
    assert.equal(unauthenticated.options.paseoPassword, undefined);
    for (const args of [archiveAgent, archiveWorkspace]) {
      const refused = mutate(args, unauthenticated.options.paseoPassword);
      assert.equal(refused.status, 64);
      assert.match(refused.stderr, /PASEO_AUTH_REQUIRED/u);
    }
    assert.equal(requestsFor("archive_agent").length, 0);
    assert.equal(requestsFor("archive_workspace").length, 0);

    // A rejected credential is proven without an executed effect.
    process.env.DIRECTOR_PASEO_CREDENTIAL_FILE = fixture.credentialFile;
    writeFileSync(fixture.credentialFile, fixture.wrongPassword, { mode: 0o600 });
    const rejected = parseCli(cliArguments(fixture));
    for (const args of [archiveAgent, archiveWorkspace]) {
      const refused = mutate(args, rejected.options.paseoPassword);
      assert.equal(refused.status, 65);
      assert.match(refused.stderr, /PASEO_AUTH_FAILED/u);
    }

    writeFileSync(fixture.credentialFile, fixture.password, { mode: 0o600 });
    const parsed = parseCli(cliArguments(fixture));
    const password = parsed.options.paseoPassword;
    assert.equal(password, fixture.password);

    // An echoed credential is refused even though the mutation may have landed;
    // the recorded intent and the resource are preserved for reconciliation.
    writeFileSync(fixture.modePath, "echo-secret\n");
    for (const args of [archiveAgent, archiveWorkspace]) {
      const redacted = mutate(args, password);
      assert.equal(redacted.status, 66);
      assert.match(redacted.stderr, /PASEO_LIFECYCLE_RESPONSE_REDACTED/u);
      assert.equal(redacted.stdout.includes(fixture.password), false);
    }

    // Response loss, malformed output, and a refused tool call are all
    // unproven outcomes that report the same bounded mutation failure.
    for (const mode of ["response-loss", "malformed-json", "mutation-refused"]) {
      writeFileSync(fixture.modePath, `${mode}\n`);
      for (const args of [archiveAgent, archiveWorkspace]) {
        const failed = mutate(args, password);
        assert.equal(failed.status, 68, `${mode}: ${failed.stderr}`);
        assert.match(failed.stderr, /PASEO_LIFECYCLE_MUTATION_FAILED|PASEO_LIFECYCLE_OUTPUT_INVALID/u);
      }
    }

    writeFileSync(fixture.modePath, "timeout\n");
    for (const args of [archiveAgent, archiveWorkspace]) {
      const timedOut = mutate(args, password, { timeout: 100 });
      assert.notEqual(timedOut.status, 0);
    }

    // A mutation aimed at an identity the daemon does not own fails closed and
    // leaves the bound resources untouched.
    writeFileSync(fixture.modePath, "success\n");
    const wrongAgent = mutate(["archive", "agent-auth-wrong", "--json"], password);
    assert.equal(wrongAgent.status, 68);
    const wrongWorkspace = mutate(
      ["workspace", "archive", "workspace-auth-wrong", "--json"],
      password,
    );
    assert.equal(wrongWorkspace.status, 68);
    const beforeSuccess = JSON.parse(
      mutate(["inspect", AGENT_ID, "--json"], password).stdout,
    );
    assert.equal(beforeSuccess.Archived, false);
    assert.equal(
      JSON.parse(mutate(["workspace", "ls", "--json"], password).stdout).length,
      1,
    );

    // The exact recorded effects now authenticate and execute.
    const agentArchive = mutate(archiveAgent, password);
    assert.equal(agentArchive.status, 0, agentArchive.stderr);
    assert.deepEqual(JSON.parse(agentArchive.stdout), {
      operation: "agent.archive",
      dispatched: true,
    });
    const workspaceArchive = mutate(archiveWorkspace, password);
    assert.equal(workspaceArchive.status, 0, workspaceArchive.stderr);
    assert.deepEqual(JSON.parse(workspaceArchive.stdout), {
      operation: "workspace.archive",
      dispatched: true,
    });

    // Completion is proven only by the authoritative readback, which is what
    // lets cleanup continue to worktree and ref removal without intervention.
    const inspected = JSON.parse(mutate(["inspect", AGENT_ID, "--json"], password).stdout);
    assert.equal(inspected.Archived, true);
    assert.equal(inspected.ArchivedAt, "2026-09-15T00:00:00Z");
    assert.deepEqual(
      JSON.parse(mutate(["workspace", "ls", "--json"], password).stdout),
      [],
    );

    // Only the bounded child is authenticated: the Paseo CLI is used solely for
    // password-free target discovery and never receives a lifecycle verb.
    const childEntries = processLog(fixture);
    assert.equal(
      childEntries
        .filter((entry) => entry.name === "paseo")
        .every((entry) => entry.args[0] === "daemon"),
      true,
    );
    assert.equal(childEntries.every((entry) => entry.forbiddenAmbient.length === 0), true);

    const expectedHash = createHash("sha256").update(fixture.password).digest("hex");
    for (const [tool, args] of [
      ["archive_agent", { agentId: AGENT_ID }],
      ["archive_workspace", { workspaceId: WORKSPACE_ID }],
    ]) {
      const last = requestsFor(tool).at(-1);
      assert.deepEqual(last.arguments, args);
      assert.equal(last.authorizationHash, expectedHash);
      assert.equal(last.authorizationPresent, true);
      assert.equal(last.method, "tools/call");
      assert.equal(last.path, "/mcp/agents");
    }
    assert.equal(JSON.stringify(requestLog(fixture)).includes(fixture.password), false);

    assert.equal(lstatSync(fixture.credentialFile).mode & 0o077, 0);
    assert.equal(readFileSync(fixture.credentialFile, "utf8"), fixture.password);
    for (const path of filesBelow(fixture.root)) {
      if (path === fixture.credentialFile || !lstatSync(path).isFile()) continue;
      const contents = readFileSync(path);
      for (const secret of [fixture.password, fixture.wrongPassword, fixture.ambientPassword]) {
        assert.equal(contents.includes(Buffer.from(secret)), false, basename(path));
      }
    }
  } finally {
    if (server !== undefined) await stopMcpServer(server);
    restore();
    rmSync(fixture.root, { recursive: true, force: true });
  }
});

test("Beads discovery reaches the bd child without widening credential exposure", () => {
  const fixture = createFixture();
  const previousPath = process.env.PATH;
  const beadsDir = join(fixture.root, "beads-workspace");
  const restore = replaceEnvironment({
    PATH: `${fixture.bin}:${previousPath}`,
    BEADS_DIR: beadsDir,
    DIRECTOR_PASEO_CREDENTIAL_FILE: fixture.credentialFile,
    PASEO_PASSWORD: fixture.ambientPassword,
  });
  const lastBeadsEntry = () => processLog(fixture).filter((entry) => entry.name === "bd").at(-1);
  try {
    const parsed = parseCli(cliArguments(fixture));
    const result = defaultCommandRunner("bd", ["show", TASK, "--json"], {
      paseoPassword: parsed.options.paseoPassword,
    });
    assert.equal(result.status, 0, result.stderr);
    assert.equal(lastBeadsEntry().beadsDir, beadsDir);
    assert.equal(lastBeadsEntry().forbiddenAmbient.length, 0);

    // The allowlist is not a credential channel: a Beads location carrying the
    // selected password is dropped rather than forwarded.
    process.env.BEADS_DIR = join(fixture.root, fixture.password);
    const poisoned = defaultCommandRunner("bd", ["show", TASK, "--json"], {
      paseoPassword: parsed.options.paseoPassword,
    });
    assert.equal(poisoned.status, 0, poisoned.stderr);
    assert.equal(lastBeadsEntry().beadsDir, null);
    assert.equal(lastBeadsEntry().forbiddenAmbient.length, 0);
  } finally {
    restore();
    rmSync(fixture.root, { recursive: true, force: true });
  }
});

test("Paseo credential authority requires an absolute owner-only regular file", () => {
  const fixture = createFixture();
  const restore = replaceEnvironment({
    DIRECTOR_PASEO_CREDENTIAL_FILE: fixture.credentialFile,
  });
  try {
    chmodSync(fixture.credentialFile, 0o644);
    assert.throws(
      () => parseCli(cliArguments(fixture)),
      (error) => error instanceof CoordinatorError &&
        error.code === "PASEO_CREDENTIAL_FILE_INVALID",
    );
    chmodSync(fixture.credentialFile, 0o600);
    process.env.DIRECTOR_PASEO_CREDENTIAL_FILE = "relative-password";
    assert.throws(
      () => parseCli(cliArguments(fixture)),
      (error) => error instanceof CoordinatorError &&
        error.code === "PASEO_CREDENTIAL_FILE_INVALID",
    );
  } finally {
    restore();
    rmSync(fixture.root, { recursive: true, force: true });
  }
});
