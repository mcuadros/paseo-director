// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { createHash, randomBytes } from "node:crypto";
import {
  accessSync,
  chmodSync,
  constants,
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
import test from "node:test";

import {
  CoordinatorError,
  defaultCommandRunner,
  errorOutput,
  execute,
} from "./coordinator.mjs";

const TASK = "dir-m1.23";
const TITLE = "Support protected Paseo daemon authentication in coordinator handoff";
const ACTOR = "paseo:11111111-2222-4333-8444-555555555555";
const BRANCH = "task/dir-m1.23-paseo-auth-test";
const AGENT_ID = "agent-auth-0001";
const WORKSPACE_ID = "workspace-auth-0001";
const REPOSITORY = "acme/director";

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
  const passwordHash = createHash("sha256")
    .update(fixture.password)
    .digest("hex");
  const realGit = executableOnPath("git");
  const script = `#!${process.execPath}
const { createHash } = require("node:crypto");
const { appendFileSync, readFileSync, writeFileSync } = require("node:fs");
const { basename } = require("node:path");
const { spawnSync } = require("node:child_process");

const expectedPasswordHash = ${JSON.stringify(passwordHash)};
const logPath = ${JSON.stringify(fixture.logPath)};
const modePath = ${JSON.stringify(fixture.modePath)};
const checkout = ${JSON.stringify(fixture.checkout)};
const title = ${JSON.stringify(TITLE)};
const task = ${JSON.stringify(TASK)};
const actor = ${JSON.stringify(ACTOR)};
const agentId = ${JSON.stringify(AGENT_ID)};
const workspaceId = ${JSON.stringify(WORKSPACE_ID)};
const repository = ${JSON.stringify(REPOSITORY)};
const realGit = ${JSON.stringify(realGit)};
const name = basename(process.argv[1]);
const args = process.argv.slice(2);
const password = process.env.PASEO_PASSWORD;
const forbiddenAmbient = [
  "AWS_SECRET_ACCESS_KEY",
  "DIRECTOR_PRIVATE_TOKEN",
  "GH_TOKEN",
  "GITHUB_TOKEN",
  "GOAUTH",
  "NPM_TOKEN",
].filter((key) => process.env[key] !== undefined);
const passwordInArgv =
  password !== undefined && args.some((value) => value.includes(password));
appendFileSync(
  logPath,
  JSON.stringify({
    name,
    args,
    hasPassword: password !== undefined,
    forbiddenAmbient,
    passwordInArgv,
  }) + "\\n",
);

if (forbiddenAmbient.length > 0 || passwordInArgv) process.exit(86);

function output(value) {
  writeFileSync(1, value);
  process.exit(0);
}

function diagnostic(value, status) {
  writeFileSync(2, value);
  process.exit(status);
}

if (name === "git") {
  if (password !== undefined) process.exit(87);
  const remoteIndex = args.indexOf("remote");
  if (
    remoteIndex >= 0 && args[remoteIndex + 1] === "get-url" &&
    args[remoteIndex + 2] === "origin"
  ) {
    output("https://github.com/acme/director.git\\n");
  }
  const result = spawnSync(realGit, args, {
    cwd: process.cwd(),
    env: process.env,
    stdio: "inherit",
  });
  process.exit(result.status ?? 88);
}

if (name === "bd") {
  if (password !== undefined) process.exit(89);
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
  if (password !== undefined) process.exit(90);
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
  const inspect =
    args.length === 3 && args[0] === "inspect" && args[2] === "--json";
  const workspaceList =
    args.length === 3 && args[0] === "workspace" && args[1] === "ls" &&
    args[2] === "--json";
  if (!inspect && !workspaceList) {
    if (password !== undefined) process.exit(92);
    output("{}");
  }
  if (password === undefined) diagnostic("Password required", 19);
  if (createHash("sha256").update(password).digest("hex") !== expectedPasswordHash) {
    diagnostic("Incorrect password: " + password, 20);
  }
  const mode = readFileSync(modePath, "utf8").trim();
  if (mode === "interrupted") process.kill(process.pid, "SIGTERM");
  if (mode === "hostile-error") {
    diagnostic("Incorrect password: " + password.repeat(50_000), 21);
  }
  if (inspect) {
    output(JSON.stringify({
      Id: agentId,
      Name: title,
      Status: mode === "hostile-output" ? password : "idle",
      Archived: false,
      ArchivedAt: null,
      Cwd: checkout,
      ParentAgentId: null,
    }));
  }
  output(JSON.stringify([{
    workspaceId,
    isolation: "worktree",
    cwd: checkout,
  }]));
}

if (password !== undefined) process.exit(93);
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
  const logPath = join(root, "process-log.jsonl");
  const modePath = join(root, "mode");
  const password = `director-adversarial-${randomBytes(24).toString("hex")}-"\\\nline`;
  const wrongPassword = `director-wrong-${randomBytes(24).toString("hex")}`;
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
  writeFileSync(logPath, "");
  writeFileSync(modePath, "success\n");
  const fixture = {
    root,
    origin,
    control,
    checkout,
    bin,
    validationFile,
    logPath,
    modePath,
    password,
    wrongPassword,
    base,
    candidate,
  };
  writeFakeCommands(fixture);
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
    ownership: "dir-m1.23-auth-fixture-0001",
    checkout,
    "checkout-state": "present",
    "control-repo": control,
    "agent-id": AGENT_ID,
    "workspace-id": WORKSPACE_ID,
    "lifecycle-state": "active",
    pr: "absent",
    "validation-file": validationFile,
  };
  return fixture;
}

function processLog(fixture) {
  return readFileSync(fixture.logPath, "utf8")
    .trim()
    .split("\n")
    .filter(Boolean)
    .map((line) => JSON.parse(line));
}

function assertRefusal(error, code, secret) {
  assert.ok(error instanceof CoordinatorError);
  assert.equal(error.code, code);
  const output = JSON.stringify(errorOutput("review-handoff", error));
  assert.equal(output.includes(secret), false);
  assert.ok(Buffer.byteLength(output) < 1_024, output);
  return true;
}

test("protected Paseo lifecycle reads isolate credentials and fail closed", async () => {
  const fixture = createFixture();
  const previousPath = process.env.PATH;
  const restore = replaceEnvironment({
    PATH: `${fixture.bin}:${previousPath}`,
    PASEO_HOST: "127.0.0.1:17693",
    PASEO_PASSWORD: fixture.password,
    AWS_SECRET_ACCESS_KEY: "ambient-aws-secret",
    DIRECTOR_PRIVATE_TOKEN: "ambient-director-secret",
    GH_TOKEN: "ambient-gh-secret",
    GITHUB_TOKEN: "ambient-github-secret",
    GOAUTH: "ambient-go-secret",
    NPM_TOKEN: "ambient-npm-secret",
  });
  try {
    const first = await execute("review-handoff", fixture.options);
    assert.equal(first.result.snapshot.paseo.verified, true);
    assert.equal(first.result.snapshot.paseo.agent.id, AGENT_ID);
    assert.equal(first.result.snapshot.paseo.workspace.id, WORKSPACE_ID);
    assert.equal(JSON.stringify(first).includes(fixture.password), false);
    writeFileSync(
      join(fixture.root, "review-handoff-manifest.json"),
      `${JSON.stringify(first)}\n`,
    );

    for (const executable of ["npm", "go", "paseo-sibling"]) {
      const result = defaultCommandRunner(executable, ["probe"]);
      assert.equal(result.status, 0, `${executable}: ${result.stderr}`);
    }
    const archive = defaultCommandRunner("paseo", ["archive", AGENT_ID, "--json"]);
    assert.equal(archive.status, 0, archive.stderr);
    process.env.PASEO_HOST = "ssh://operator@example.invalid";
    const sshArchive = defaultCommandRunner("paseo", [
      "archive",
      AGENT_ID,
      "--json",
    ]);
    assert.equal(sshArchive.status, 0, sshArchive.stderr);
    process.env.PASEO_HOST = "127.0.0.1:17693";

    let entries = processLog(fixture);
    assert.deepEqual(
      entries
        .filter((entry) => entry.hasPassword)
        .map((entry) => [entry.name, entry.args]),
      [
        ["paseo", ["inspect", AGENT_ID, "--json"]],
        ["paseo", ["workspace", "ls", "--json"]],
      ],
    );
    assert.equal(entries.every((entry) => entry.forbiddenAmbient.length === 0), true);
    assert.equal(entries.every((entry) => entry.passwordInArgv === false), true);

    delete process.env.PASEO_PASSWORD;
    process.env.PASEO_HOST = `tcp://127.0.0.1:17693?password=${fixture.password}`;
    assert.throws(
      () => defaultCommandRunner("git", ["--version"]),
      (error) =>
        assertRefusal(
          error,
          "PASEO_AUTH_LOCATION_UNSUPPORTED",
          fixture.password,
        ),
    );
    process.env.PASEO_HOST = "127.0.0.1:17693";

    await assert.rejects(
      execute("review-handoff", fixture.options),
      (error) =>
        assertRefusal(error, "PASEO_AUTH_REQUIRED", fixture.password),
    );

    process.env.PASEO_PASSWORD = fixture.wrongPassword;
    await assert.rejects(
      execute("review-handoff", fixture.options),
      (error) =>
        assertRefusal(error, "PASEO_AUTH_FAILED", fixture.wrongPassword),
    );

    process.env.PASEO_PASSWORD = fixture.password;
    writeFileSync(fixture.modePath, "hostile-error\n");
    await assert.rejects(
      execute("review-handoff", fixture.options),
      (error) =>
        assertRefusal(error, "PASEO_AUTH_FAILED", fixture.password),
    );

    writeFileSync(fixture.modePath, "hostile-output\n");
    await assert.rejects(
      execute("review-handoff", fixture.options),
      (error) =>
        assertRefusal(
          error,
          "PASEO_LIFECYCLE_RESPONSE_REDACTED",
          fixture.password,
        ),
    );

    writeFileSync(fixture.modePath, "interrupted\n");
    await assert.rejects(
      execute("review-handoff", fixture.options),
      (error) =>
        assertRefusal(
          error,
          "PASEO_LIFECYCLE_READ_FAILED",
          fixture.password,
        ),
    );
    writeFileSync(fixture.modePath, "success\n");
    const retry = await execute("review-handoff", fixture.options);
    assert.deepEqual(retry, first);

    entries = processLog(fixture);
    assert.equal(entries.every((entry) => entry.passwordInArgv === false), true);
    for (const path of filesBelow(fixture.root)) {
      if (!lstatSync(path).isFile()) continue;
      const contents = readFileSync(path);
      assert.equal(
        contents.includes(Buffer.from(fixture.password)),
        false,
        basename(path),
      );
      assert.equal(
        contents.includes(Buffer.from(fixture.wrongPassword)),
        false,
        basename(path),
      );
    }
  } finally {
    restore();
    rmSync(fixture.root, { recursive: true, force: true });
  }
});
