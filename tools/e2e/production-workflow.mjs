// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { createConnection, createServer } from "node:net";
import {
  chmodSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { createHash, randomBytes } from "node:crypto";
import { pathToFileURL } from "node:url";
import { createPaseoClient } from "@getpaseo/client";

const repositoryRoot = resolve(dirname(new URL(import.meta.url).pathname), "../..");
const statePath = process.env.DIRECTOR_E2E_STATE || "/tmp/dir-m6.11-production-workflow-state.json";
const hold = process.argv.includes("--hold");
const bootstrapOnly = process.argv.includes("--bootstrap-only");
const externalSelfTest = process.argv.includes("--self-test-external");
let ownedRoot = "";
const ownedProcesses = [];

function environment(extra = {}) {
  return { PATH: process.env.PATH, HOME: process.env.HOME, LANG: "C", LC_ALL: "C", ...extra };
}

function run(command, args, options = {}) {
  const result = spawnSync(command, args, { cwd: options.cwd ?? repositoryRoot,
    env: environment(options.env), encoding: "utf8", maxBuffer: 4 * 1024 * 1024 });
  if (result.status !== 0) {
    if (process.env.DIRECTOR_E2E_DEBUG_LOG) {
      writePrivate(process.env.DIRECTOR_E2E_DEBUG_LOG, `${result.stdout}\n${result.stderr}`);
    }
    throw new Error(`${options.label ?? command} failed with status ${result.status}`);
  }
  return result.stdout.trim();
}

async function port() {
  const server = createServer();
  await new Promise((accept, reject) => { server.once("error", reject); server.listen(0, "127.0.0.1", accept); });
  const value = server.address();
  assert(value && typeof value === "object");
  await new Promise((accept, reject) => server.close((error) => error ? reject(error) : accept()));
  return value.port;
}

function start(command, args, options = {}) {
  const child = spawn(command, args, { cwd: options.cwd ?? repositoryRoot, env: environment(options.env),
    shell: false, stdio: ["ignore", "pipe", "pipe"] });
  let output = "";
  const capture = (chunk) => { output = `${output}${chunk}`.slice(-128 * 1024); };
  child.stdout.on("data", capture);
  child.stderr.on("data", capture);
  return { child, output: () => output };
}

async function waitFor(label, check, process, milliseconds = 30_000) {
  const deadline = Date.now() + milliseconds;
  while (Date.now() < deadline) {
    if (process.child.exitCode !== null || process.child.signalCode !== null) throw new Error(`${label} process exited before readiness`);
    if (await check()) return;
    await new Promise((accept) => setTimeout(accept, 100));
  }
  throw new Error(`${label} did not become ready`);
}

function removeOwnedRoot(path) {
  if (!path || !existsSync(path)) return;
  const chmod = spawnSync("chmod", ["-R", "u+w", path], { env: environment(), encoding: "utf8" });
  if (chmod.status !== 0) throw new Error("owned root permissions could not be normalized for cleanup");
  rmSync(path, { recursive: true, force: true });
}

function ownedProcessIds(path) {
  const result = [];
  for (const entry of readdirSync("/proc")) {
    if (!/^[1-9][0-9]*$/.test(entry)) continue;
    try {
      const command = readFileSync(`/proc/${entry}/cmdline`);
      if (command.length <= 64 * 1024 && command.includes(Buffer.from(path))) result.push(Number(entry));
    } catch {}
  }
  return result;
}

async function stopOwnedDescendants(path) {
  for (const signal of ["SIGTERM", "SIGKILL"]) {
    const deadline = Date.now() + 10_000;
    while (Date.now() < deadline) {
      const pids = ownedProcessIds(path);
      if (pids.length === 0) return;
      for (const pid of pids) {
        try { process.kill(pid, signal); } catch (error) { if (error.code !== "ESRCH") throw error; }
      }
      await new Promise((accept) => setTimeout(accept, 100));
    }
  }
  if (ownedProcessIds(path).length !== 0) throw new Error("owned descendant process cleanup is ambiguous");
}

async function removeOwnedRootAfterQuiescence(path) {
  const deadline = Date.now() + 30_000;
  let absentChecks = 0;
  while (Date.now() < deadline) {
    await stopOwnedDescendants(path);
    removeOwnedRoot(path);
    await new Promise((accept) => setTimeout(accept, 250));
    if (!existsSync(path) && ownedProcessIds(path).length === 0) {
      absentChecks++;
      if (absentChecks === 8) return;
    } else {
      absentChecks = 0;
    }
  }
  throw new Error("owned root did not remain quiescent after cleanup");
}

async function stop(process) {
  if (!process || process.child.exitCode !== null || process.child.signalCode !== null) return;
  process.child.kill("SIGTERM");
  await Promise.race([new Promise((accept) => process.child.once("close", accept)), new Promise((accept) => setTimeout(accept, 10_000))]);
  if (process.child.exitCode === null && process.child.signalCode === null) {
    process.child.kill("SIGKILL");
    await new Promise((accept) => process.child.once("close", accept));
  }
}

function git(cwd, ...args) { return run("git", args, { cwd }); }

function writePrivate(path, value) {
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  writeFileSync(path, value, { mode: 0o600 });
  chmodSync(path, 0o600);
}

function boundedDirectorCode(value) {
  const matches = value.match(/\b(?:DIRECTOR|HOST|ENGINE|PASEO)_[A-Z0-9_]{2,92}\b/gu) ?? [];
  return matches.at(-1) ?? "DIRECTOR_ACTIVATION_UNAVAILABLE";
}

const planningContractSource = readFileSync(join(repositoryRoot, "generated", "planning-contract.shared.ts"), "utf8");
const planningContractHash = planningContractSource.match(/PLANNING_CONTRACT_SHA256 = "([0-9a-f]{64})"/)?.[1];
assert(planningContractHash, "generated planning contract hash is unavailable");
const planningHeaders = {
  "content-type": "application/json",
  "x-director-contract-version": "director-planning/v1",
  "x-director-contract-hash": planningContractHash,
};
const humanHeaders = {
  "x-director-actor-kind": "human",
  "x-director-actor-id": "production-workflow-owner",
  "x-director-actor-session": "production-workflow-human-session",
};

async function engineJSON(enginePort, path, value, { human = false, expectedStatus = 200 } = {}) {
  const response = await fetch(`http://127.0.0.1:${enginePort}${path}`, {
    method: value === undefined ? "GET" : "POST",
    redirect: "error",
    headers: value === undefined ? {} : { ...planningHeaders, ...(human ? humanHeaders : {}) },
    ...(value === undefined ? {} : { body: JSON.stringify(value) }),
  });
  assert.equal(response.status, expectedStatus, `${path} returned ${response.status}`);
  const body = await response.json();
  if (expectedStatus === 200) {
    assert.equal(response.headers.get("x-director-contract-version"), "director-planning/v1");
    assert.equal(response.headers.get("x-director-contract-hash"), planningContractHash);
  }
  return body;
}

function requestID(kind, ...parts) {
  return `${kind}-${createHash("sha256").update(parts.join("\u001f")).digest("hex").slice(0, 32)}`;
}

function mutation(intent, expectedVersion, approval = null, acknowledgement = null) {
  const id = requestID("e2e", intent.type, JSON.stringify(intent), String(expectedVersion), approval ?? "");
  return { schemaVersion: 1, contractVersion: "director-planning/v1", contractHash: planningContractHash,
    requestId: id, idempotencyKey: id, expectedVersion: String(expectedVersion),
    humanApprovalRef: approval, acknowledgementRevision: acknowledgement, intent };
}

async function allWorkspaces(client) {
  const result = [];
  let cursor;
  do {
    const page = await client.workspaces.list({ page: { limit: 100, ...(cursor ? { cursor } : {}) } });
    result.push(...page.entries);
    cursor = page.pageInfo.nextCursor || undefined;
  } while (cursor);
  return result;
}

async function nativeProjectFact(client, projectID) {
  const projects = await client.projects.list({ requestId: requestID("native-projects", projectID) });
  const project = projects.projects.find((value) => value.projectId === projectID);
  assert(project, "created native Paseo Project disappeared");
  const listed = await allWorkspaces(client);
  const refreshed = [];
  for (const workspace of listed.filter((value) => value.projectId === projectID && !value.archivingAt)) {
    const current = await client.workspaces.ref(workspace.id).refresh({ requestId: requestID("native-refresh", workspace.id) });
    if (current && !current.archivingAt) refreshed.push(current);
  }
  const projectRootPath = project.projectRootPath;
  const value = {
    projectId: project.projectId,
    name: project.projectCustomName || project.projectDisplayName,
    projectRootPath,
    projectKind: project.projectKind,
    organizerCandidate: join(dirname(projectRootPath), `.${projectRootPath.split("/").at(-1)}-director-organizer`),
    workspaces: refreshed.map((workspace) => ({ id: workspace.id, name: workspace.title || workspace.name,
      projectRootPath: workspace.projectRootPath, workspaceDirectory: workspace.workspaceDirectory || workspace.projectRootPath,
      workspaceKind: workspace.workspaceKind, remoteUrl: workspace.gitRuntime?.remoteUrl || null,
      baseBranch: workspace.gitRuntime?.currentBranch || null })).sort((left, right) => left.id.localeCompare(right.id)),
    factsRevision: "",
  };
  value.factsRevision = createHash("sha256").update(JSON.stringify(value)).digest("hex");
  return value;
}

function planningAggregateID(prefix, ...parts) {
  return `${prefix}-${createHash("sha256").update(parts.join("\u001f")).digest("hex").slice(0, 32)}`;
}

function directorWorkspaceID(projectID, workspaceKey) {
  return `workspace-${createHash("sha256").update(`director-workspace\u001f${projectID}\u001f${workspaceKey}`).digest("hex")}`;
}

function planningQuery(projectID) {
  return { projectId: projectID, workspaceIds: [], epicIds: [], states: [], priorities: [], labels: [], attention: [],
    search: null, sort: "scheduler_order", cursor: null, pageSize: 100 };
}

async function taskDetail(enginePort, taskID) {
  return engineJSON(enginePort, "/v1/planning/task-detail", { hostId: "production-host", context: "board",
    taskId: taskID, paseoWorkspaceId: null, paseoAgentId: null, afterCursor: null });
}

async function waitForTask(label, enginePort, taskID, processValue, predicate, milliseconds = 12 * 60_000) {
  let snapshot;
  const currentProcess = typeof processValue === "function" ? processValue() : processValue;
  await waitFor(label, async () => {
    snapshot = await taskDetail(enginePort, taskID);
    return snapshot.detail !== null && predicate(snapshot.detail);
  }, currentProcess, milliseconds);
  return snapshot.detail;
}

async function postMutation(enginePort, intent, expectedVersion, approval = null, acknowledgement = null) {
  return engineJSON(enginePort, "/v1/planning/mutate", mutation(intent, expectedVersion, approval, acknowledgement), { human: true });
}

async function applyOrganizerWithResponseLoss(enginePort, previewInput, applyKind, preview) {
  const apply = { ...previewInput, kind: applyKind, previewId: preview.id };
  const response = await fetch(`http://127.0.0.1:${enginePort}/v1/planning/organizer-bootstrap`, {
    method: "POST", redirect: "error", headers: { ...planningHeaders, ...humanHeaders }, body: JSON.stringify(apply),
  });
  assert.equal(response.status, 200, "Organizer Apply response-loss handoff failed");
  await response.body?.cancel();
  const replay = await engineJSON(enginePort, "/v1/planning/organizer-bootstrap", apply, { human: true });
  assert.equal(replay.status, "applied");
  assert.equal(replay.projectVersion !== null, true);
  return replay;
}

async function createOrganizer(enginePort, nativeFact) {
  const requestId = requestID("native-organizer", nativeFact.projectId, nativeFact.factsRevision);
  const input = { schemaVersion: 1, contractVersion: "director-planning/v1", contractHash: planningContractHash,
    hostId: "production-host", requestId, kind: "native.create.preview", nativeProject: nativeFact,
    projectId: null, projectName: null, repositoryPath: null, configurationJson: null, previewId: null };
  await engineJSON(enginePort, "/v1/planning/organizer-bootstrap", input, { expectedStatus: 401 });
  const previewResult = await engineJSON(enginePort, "/v1/planning/organizer-bootstrap", input, { human: true });
  assert.equal(previewResult.status, "preview");
  assert.equal(previewResult.preview?.valid, true);
  assert.equal(typeof previewResult.preview?.configurationJson, "string");
  const configuration = JSON.parse(previewResult.preview.configurationJson);
  assert.equal(configuration.workspaces.length, 1);
  assert.equal(configuration.workspaces[0].nativePaseoWorkspaceId, nativeFact.workspaces[0].id);
  const applied = await applyOrganizerWithResponseLoss(enginePort, input, "native.create.apply", previewResult.preview);
  return { projectID: previewResult.preview.projectId, projectVersion: Number(applied.projectVersion),
    workspaceID: directorWorkspaceID(previewResult.preview.projectId, configuration.workspaces[0].id) };
}

async function adoptOrganizer(enginePort, repositoryPath) {
  const projectID = "pr-project";
  const requestId = requestID("adopt-organizer", projectID);
  const input = { schemaVersion: 1, contractVersion: "director-planning/v1", contractHash: planningContractHash,
    hostId: "production-host", requestId, kind: "advanced.adopt.preview", nativeProject: null,
    projectId: projectID, projectName: "Pull request project", repositoryPath, configurationJson: null, previewId: null };
  const previewResult = await engineJSON(enginePort, "/v1/planning/organizer-bootstrap", input, { human: true });
  assert.equal(previewResult.status, "preview");
  assert.equal(previewResult.preview?.valid, true);
  const applied = await applyOrganizerWithResponseLoss(enginePort, input, "advanced.adopt.apply", previewResult.preview);
  return { projectID, projectVersion: Number(applied.projectVersion), workspaceID: directorWorkspaceID(projectID, "product") };
}

async function createTaskSet(enginePort, project, keyPrefix, withDependency) {
  const epicKey = `${keyPrefix}-epic`;
  const epic = await postMutation(enginePort, { type: "epic.create", projectId: project.projectID, key: epicKey,
    title: `${keyPrefix} production Epic`, description: "Exercise the maintained production workflow", priority: "high", labels: ["production"] }, project.projectVersion);
  assert.equal(epic.status, "accepted");
  const epicID = planningAggregateID("epic", project.projectID, epicKey);
  let prerequisiteID = null;
  if (withDependency) {
    const prerequisiteKey = `${keyPrefix}-prerequisite`;
    const prerequisite = await postMutation(enginePort, { type: "task.create", projectId: project.projectID,
      workspaceId: project.workspaceID, epicId: epicID, key: prerequisiteKey, title: "Deferred prerequisite",
      objective: "Remain queued so the authenticated dependency override is exercised.", acceptanceCriteria: ["Remain queued"],
      priority: "normal", labels: ["production"] }, project.projectVersion);
    assert.equal(prerequisite.status, "accepted");
    prerequisiteID = planningAggregateID("task", project.projectID, prerequisiteKey);
  }
  const taskKey = `${keyPrefix}-task`;
  const filename = withDependency ? "RESULT.md" : "PR_RESULT.md";
  const task = await postMutation(enginePort, { type: "task.create", projectId: project.projectID,
    workspaceId: project.workspaceID, epicId: epicID, key: taskKey, title: `${keyPrefix} production Task`,
    objective: `Create ${filename} containing the line '${keyPrefix} workflow complete', commit it, and submit the exact completed Candidate through Director.`,
    acceptanceCriteria: [`${filename} contains '${keyPrefix} workflow complete'`], priority: "urgent", labels: ["production"] }, project.projectVersion);
  assert.equal(task.status, "accepted");
  const taskID = planningAggregateID("task", project.projectID, taskKey);
  if (prerequisiteID) {
    const dependency = await postMutation(enginePort, { type: "dependency.add", taskId: taskID,
      dependencyKind: "task", dependencyId: prerequisiteID }, 0);
    assert.equal(dependency.status, "accepted");
  }
  return { taskID, prerequisiteID, filename, version: prerequisiteID ? 1 : 0 };
}

async function launchTask(enginePort, task, withDependency) {
  if (withDependency) {
    const refused = await postMutation(enginePort, { type: "task.launch-now", taskId: task.taskID }, task.version);
    assert.equal(refused.status, "rejected", "blocked dependency launch was not refused");
    const approval = requestID("dependency-human-approval", task.taskID, task.prerequisiteID);
    const overridden = await postMutation(enginePort, { type: "dependency.override", taskId: task.taskID,
      dependencyKind: "task", dependencyId: task.prerequisiteID }, task.version, approval, String(task.version));
    assert.equal(overridden.status, "accepted");
  }
  const launched = await postMutation(enginePort, { type: "task.launch-now", taskId: task.taskID }, task.version);
  if (launched.status !== "accepted") {
    const detail = await taskDetail(enginePort, task.taskID);
    const facts = detail.detail ? {
      message: launched.message,
      disposition: detail.detail.summary.schedulingFacts.launchDisposition,
      explanations: detail.detail.summary.schedulingFacts.explanations.map((value) => value.code),
      needsYou: detail.detail.summary.needsYou.map((value) => value.code),
    } : { message: launched.message, detail: "unavailable" };
    throw new Error(`real Run was not admitted: ${JSON.stringify(facts)}`);
  }
}

async function completeTask(enginePort, processValue, task, { feedback }) {
  await launchTask(enginePort, task, feedback);
  const active = await waitForTask("real parentless Task Agent", enginePort, task.taskID, processValue,
    (detail) => detail.binding.runId !== null && detail.binding.paseoAgentId !== null, 3 * 60_000);
  const runID = active.binding.runId;
  assert(runID);
  const firstCandidate = await waitForTask("exact Candidate admission", enginePort, task.taskID, processValue,
    (detail) => detail.binding.candidateSha !== null, 12 * 60_000);
  let candidateSHA = firstCandidate.binding.candidateSha;
  if (feedback) {
    let response;
    for (let attempt = 0; attempt < 20; attempt++) {
      const current = await taskDetail(enginePort, task.taskID);
      assert(current.detail?.binding.runVersion, "current feedback Run version is unavailable");
      response = await postMutation(enginePort, { type: "task.feedback", projectId: current.detail.binding.projectId,
        taskId: task.taskID, runId: runID, body: `Add a second line 'feedback applied' to ${task.filename}, commit it, and submit the changed exact Candidate.`,
        severity: "P1" }, Number(current.detail.binding.runVersion));
      if (response.status === "accepted") break;
      await new Promise((accept) => setTimeout(accept, 100));
    }
    assert.equal(response?.status, "accepted", "authenticated human feedback was not admitted after fresh public readback");
    const corrected = await waitForTask("feedback correction Candidate", enginePort, task.taskID, processValue,
      (detail) => detail.binding.candidateSha !== null && detail.binding.candidateSha !== candidateSHA, 12 * 60_000);
    candidateSHA = corrected.binding.candidateSha;
  }
  const ready = await waitForTask("authoritative CI and independent Review", enginePort, task.taskID, processValue,
    (detail) => detail.summary.derivedState === "ready" && detail.summary.allowedActions.some((action) => action.kind === "task.integrate"), 12 * 60_000);
  assert.equal(ready.binding.candidateSha, candidateSHA);
  const integrated = await postMutation(enginePort, { type: "task.integrate", projectId: ready.binding.projectId,
    taskId: task.taskID, runId: runID }, Number(ready.binding.runVersion));
  assert.equal(integrated.status, "accepted", "manual exact-Candidate integration was not authorized");
  await waitForTask("guarded delivery cleanup", enginePort, task.taskID, processValue,
    (detail) => detail.summary.derivedState === "done", 12 * 60_000);
  return { runID, candidateSHA };
}

async function runFunctionalWorkflow(options) {
  const client = createPaseoClient({ url: `ws://${options.paseoHost}:${options.paseoPort}/ws`, password: options.password,
    clientId: "director-production-workflow", reconnect: { enabled: false } });
  await client.connect();
  try {
    const nativeFact = await nativeProjectFact(client, options.nativeProjectID);
    assert.equal(nativeFact.workspaces.length, 1);
    assert.equal(nativeFact.workspaces[0].id, options.nativeWorkspaceID);
    assert.equal(nativeFact.workspaces[0].remoteUrl, "ssh://git@github.com/example/director-production-harness.git");
    assert.equal(nativeFact.workspaces[0].baseBranch, "main");
    const directProject = await createOrganizer(options.enginePort, nativeFact);
    const directTask = await createTaskSet(options.enginePort, directProject, "direct", true);
    await options.restartEngine();
    const directResult = await completeTask(options.enginePort, options.engineProcess, directTask, { feedback: true });
    const pullRequestProject = await adoptOrganizer(options.enginePort, options.pullRequestOrganizer);
    const pullRequestTask = await createTaskSet(options.enginePort, pullRequestProject, "pull-request", false);
    const pullRequestResult = await completeTask(options.enginePort, options.engineProcess, pullRequestTask, { feedback: false });
    const remoteMain = git(options.remote, "rev-parse", "refs/heads/main");
    assert.equal(remoteMain, pullRequestResult.candidateSHA);
    assert.match(git(options.remote, "show", `${remoteMain}:RESULT.md`), /direct workflow complete\nfeedback applied/u);
    assert.match(git(options.remote, "show", `${remoteMain}:PR_RESULT.md`), /pull-request workflow complete/u);
    const activeWorkspaces = await allWorkspaces(client);
    assert.deepEqual(activeWorkspaces.map((workspace) => workspace.id), [options.nativeWorkspaceID]);
    const planning = await engineJSON(options.enginePort, "/v1/planning/query", planningQuery(null));
    assert.equal(planning.page.tasks.filter((taskValue) => taskValue.derivedState === "done").length, 2);
    return { directCandidate: directResult.candidateSHA, pullRequestCandidate: pullRequestResult.candidateSHA,
      projects: 2, completedTasks: 2, activeWorkspaces: activeWorkspaces.length };
  } finally {
    await client.close();
  }
}

function initializeProduct(root) {
  const remote = join(root, "product-remote.git");
  mkdirSync(remote, { mode: 0o700 });
  git(remote, "init", "--bare");
  const binaryRoot = join(root, "bin");
  mkdirSync(binaryRoot, { mode: 0o700 });
  const ssh = join(binaryRoot, "ssh");
  const quotedRemote = `'${remote.replaceAll("'", `'\\''`)}'`;
  writeFileSync(ssh, `#!/bin/sh
command=
for argument in "$@"; do command=$argument; done
case "$command" in
  git-upload-pack*) exec git-upload-pack ${quotedRemote} ;;
  git-receive-pack*) exec git-receive-pack ${quotedRemote} ;;
esac
exit 64
`, { mode: 0o700 });
  const source = join(root, "product");
  mkdirSync(source, { mode: 0o700 });
  git(source, "init", "--initial-branch=main");
  git(source, "config", "user.name", "Director production harness");
  git(source, "config", "user.email", "director@example.invalid");
  writeFileSync(join(source, "README.md"), "production harness\n", { mode: 0o600 });
  git(source, "add", "README.md");
  git(source, "commit", "-m", "test: initialize production harness");
  git(source, "push", remote, "main:refs/heads/main");
  git(source, "remote", "add", "origin", "ssh://git@github.com/example/director-production-harness.git");
  return { source, remote, binaryRoot };
}

function installFakeGitHub(root, remote, binaryRoot) {
  const statePath = join(root, "private", "fake-github.json");
  writePrivate(statePath, JSON.stringify({ pull: null }));
  const executable = join(binaryRoot, "gh");
  writeFileSync(executable, `#!/usr/bin/env node
const fs = require("node:fs");
const { execFileSync } = require("node:child_process");
const remote = ${JSON.stringify(remote)};
const statePath = ${JSON.stringify(statePath)};
const args = process.argv.slice(2);
const load = () => JSON.parse(fs.readFileSync(statePath, "utf8"));
const save = (value) => { fs.writeFileSync(statePath, JSON.stringify(value), { mode: 0o600 }); fs.chmodSync(statePath, 0o600); };
const git = (...values) => execFileSync("/usr/bin/git", ["--git-dir", remote, ...values], { encoding: "utf8" }).trim();
const now = () => new Date().toISOString().replace(/\\.\\d{3}Z$/, "Z");
const include = (body, status = 200) => process.stdout.write(
  "HTTP/2.0 " + status + " OK\\r\\nx-ratelimit-remaining: 5000\\r\\nx-ratelimit-reset: 2000000000\\r\\n\\r\\n" + JSON.stringify(body));
const repository = { id: 123, node_id: "R_node", name: "director-production-harness", default_branch: "main",
  owner: { login: "example" }, archived: false, disabled: false, permissions: { pull: true, push: true } };
const pullValue = (pull) => pull && ({ number: 1, node_id: "PR_node_1", state: pull.state, draft: pull.draft,
  title: pull.title, body: pull.body, html_url: "https://github.com/example/director-production-harness/pull/1",
  updated_at: pull.updatedAt, merged: pull.merged, merged_at: pull.mergedAt || "", merge_commit_sha: pull.mergeCommit || "",
  mergeable: true, mergeable_state: "clean", user: { login: "example" },
  head: { sha: pull.headSha, ref: pull.headRef, user: { login: "example" }, repo: { id: 123 } },
  base: { ref: pull.baseRef, repo: { id: 123 } } });
if (args[0] === "api") {
  const endpoint = args[args.length - 1];
  const methodIndex = args.indexOf("--method");
  const method = methodIndex >= 0 ? args[methodIndex + 1] : "GET";
  const state = load();
  const input = method === "GET" ? null : JSON.parse(fs.readFileSync(0, "utf8"));
  if (endpoint === "user") include({ login: "example" });
  else if (endpoint === "repos/example/director-production-harness") include(repository);
  else if (endpoint === "repos/example/director-production-harness/pulls" && method === "POST") {
    const headRef = input.head.split(":").slice(1).join(":");
    state.pull = { state: "open", draft: input.draft, title: input.title, body: input.body, headRef, baseRef: input.base,
      headSha: git("rev-parse", "refs/heads/" + headRef), updatedAt: now(), merged: false };
    save(state); include(pullValue(state.pull), 201);
  } else if (/pulls\\?/.test(endpoint)) include(state.pull ? [pullValue(state.pull)] : []);
  else if (endpoint === "repos/example/director-production-harness/pulls/1" && method === "GET") include(pullValue(state.pull));
  else if (endpoint === "repos/example/director-production-harness/pulls/1" && method === "PATCH") {
    state.pull.title = input.title; state.pull.body = input.body; state.pull.updatedAt = now(); save(state); include(pullValue(state.pull));
  } else if (/pulls\\/1\\/(?:reviews|comments)/.test(endpoint) || /issues\\/1\\/comments/.test(endpoint)) include([]);
  else if (/actions\\/runs\\?/.test(endpoint)) {
    const sha = new URL("https://local/?" + endpoint.split("?")[1]).searchParams.get("head_sha");
    const observed = now();
    include({ total_count: 1, workflow_runs: [{ id: 88, workflow_id: 99, name: "maintained-linux-ci", head_sha: sha,
      head_repository: { id: 123 }, check_suite_id: 77, status: "completed", conclusion: "success", run_attempt: 1,
      run_started_at: observed, updated_at: observed }] });
  } else if (/commits\\/[0-9a-f]+\\/check-runs\\?/.test(endpoint)) {
    const sha = endpoint.split("/commits/")[1].split("/")[0]; const observed = now();
    include({ total_count: 1, check_runs: [{ id: 66, name: "Linux CI", head_sha: sha, status: "completed", conclusion: "success",
      details_url: "https://github.com/example/director-production-harness/actions/runs/88", started_at: observed,
      completed_at: observed, check_suite: { id: 77, head_sha: sha }, app: { id: 15368, slug: "github-actions" } }] });
  } else if (/commits\\/[0-9a-f]+\\/status\\?/.test(endpoint)) include({ state: "success", total_count: 0, statuses: [] });
  else { process.stderr.write("HTTP 404\\n"); process.exitCode = 1; }
} else if (args[0] === "pr" && args[1] === "ready") {
  const state = load(); state.pull.draft = args.includes("--undo"); state.pull.updatedAt = now(); save(state);
} else if (args[0] === "pr" && args[1] === "merge" && args.includes("--help")) {
  process.stdout.write("--match-head-commit SHA\\n");
} else if (args[0] === "pr" && args[1] === "merge") {
  const state = load(); const expected = args[args.indexOf("--match-head-commit") + 1];
  if (!state.pull || state.pull.headSha !== expected) process.exitCode = 1;
  else { const base = git("rev-parse", "refs/heads/" + state.pull.baseRef); git("update-ref", "refs/heads/" + state.pull.baseRef, expected, base);
    state.pull.state = "closed"; state.pull.draft = false; state.pull.merged = true; state.pull.mergedAt = now();
    state.pull.mergeCommit = expected; state.pull.updatedAt = now(); save(state); }
} else process.exitCode = 1;
`, { mode: 0o700 });
  chmodSync(executable, 0o700);
  return statePath;
}

function initializePullRequestOrganizer(root, product, nativeWorkspaceID) {
  const organizer = join(root, "pr-organizer");
  mkdirSync(join(organizer, "skills", "commits"), { recursive: true, mode: 0o700 });
  mkdirSync(join(organizer, "templates"), { recursive: true, mode: 0o700 });
  const selection = (permissionMode, mcpCapabilities) => ({ provider: "codex", model: "gpt-5.6-sol",
    effort: "high", mode: "auto-review", permissionMode, providerOptions: [], mcpCapabilities, fallbackChain: [] });
  const configuration = {
    $schema: "https://github.com/mcuadros/paseo-director/engine/domain/configuration/paseo-director.schema.json",
    schemaVersion: 1,
    project: { id: "pr-project", name: "Pull request project" },
    workspaces: [{ id: "product", name: "Product repository", nativePaseoWorkspaceId: nativeWorkspaceID,
      remote: "ssh://git@github.com/example/director-production-harness.git", sourcePath: product, defaultBaseBranch: "main" }],
    agentProfiles: {
      organizer: selection("read-only", ["project.read", "planning.command.submit"]),
      worker: selection("workspace-write", ["task.read", "task.outcome.submit"]),
      reviewer: selection("read-only", ["candidate.read", "review.verdict.submit"]),
    },
    defaults: { launchPolicy: "manual", deliveryMode: "pull_request", integrationMode: "manual",
      limits: { maxActiveTasks: 6, maxActiveTasksPerWorkspace: 2, maxConcurrentAgents: 8, maxSubagentsPerTask: 3 },
      runBudget: { elapsedSeconds: 7200, tokens: 200000, turns: 32, ciCycles: 4 },
      autoFixCiFailures: true, autoFixReviewFeedback: true, requireDifferentReviewerModel: false,
      publishBeforeReview: true, terminateOnCompletion: true, cancellationCleanup: "snapshot_then_delete",
      deleteRemoteTaskBranch: true, recoveryRetentionDays: 7,
      githubCi: { workflowId: 99, workflowName: "maintained-linux-ci", cycleRuntimeSeconds: 1800,
        requiredChecks: [{ id: "linux-ci", kind: "check_run", name: "Linux CI", appId: 15368, appSlug: "github-actions" }] } },
    workspaceOverrides: [], skills: [{ id: "commits", path: "skills/commits/SKILL.md" }],
    templates: [{ id: "task", path: "templates/task.md" }],
  };
  writeFileSync(join(organizer, "paseo-director.json"), JSON.stringify(configuration, null, 2) + "\n", { mode: 0o600 });
  writeFileSync(join(organizer, "README.md"), "# Pull request Organizer\n", { mode: 0o600 });
  writeFileSync(join(organizer, "skills", "commits", "SKILL.md"), "# Commits\n\nCreate one exact Task commit.\n", { mode: 0o600 });
  writeFileSync(join(organizer, "templates", "task.md"), "# Task\n\nComplete every acceptance criterion.\n", { mode: 0o600 });
  git(organizer, "init", "--initial-branch=main");
  git(organizer, "config", "user.name", "Director production harness");
  git(organizer, "config", "user.email", "director@example.invalid");
  git(organizer, "add", "README.md", "paseo-director.json", "skills/commits/SKILL.md", "templates/task.md");
  git(organizer, "commit", "-m", "chore: initialize pull request Organizer");
  return organizer;
}

function initializeDolt(root, clientRoot) {
  const database = "director_workflow";
  const directory = join(root, database);
  mkdirSync(directory, { recursive: true, mode: 0o700 });
  run("dolt", ["init", "--name=Director production harness", "--email=director@example.invalid"],
    { cwd: directory, env: { DOLT_ROOT_PATH: clientRoot, DOLT_DISABLE_EVENT_FLUSH: "1" } });
  return database;
}

async function main() {
  if (!externalSelfTest) {
    assert.equal(run("paseo", ["--version"]), "0.7.2");
    assert.match(run("dolt", ["version"]), /dolt version 2\.3\.2/);
  }
  const root = mkdtempSync(join(tmpdir(), "director-production-workflow-"));
  ownedRoot = root;
  chmodSync(root, 0o700);
  const sourceCandidate = git(repositoryRoot, "rev-parse", "HEAD");
  const pluginSource = join(root, "plugin-source");
  run("git", ["clone", "--no-hardlinks", repositoryRoot, pluginSource], { cwd: root, label: "exact plugin source clone" });
  git(pluginSource, "checkout", "--detach", sourceCandidate);
  const productGit = initializeProduct(root);
  const product = productGit.source;
  const fakeGitHubState = installFakeGitHub(root, productGit.remote, productGit.binaryRoot);
  let pullRequestOrganizer = "";
  const harnessPath = `${productGit.binaryRoot}:${process.env.PATH}`;
  assert.match(run(join(productGit.binaryRoot, "gh"), ["api", "--hostname", "github.com", "--include",
    "--header", "Accept: application/vnd.github+json", "repos/example/director-production-harness"]), /R_node/);
  assert.match(run(join(productGit.binaryRoot, "gh"), ["pr", "merge", "--help"]), /--match-head-commit/);
  if (externalSelfTest) {
    removeOwnedRoot(root);
    ownedRoot = "";
    process.stdout.write(JSON.stringify({ event: "production-workflow.external-self-test-passed" }) + "\n");
    return;
  }
  const engineBinary = join(root, "director-engine");
  run("go", ["build", "-trimpath", "-buildvcs=false", "-o", engineBinary, "./cmd/director-engine"],
    { cwd: join(repositoryRoot, "engine"), env: { GOCACHE: join(root, "go-cache"), GOPATH: "/tmp/dir-m6.11-go-path" } });
  const clientRoot = join(root, "dolt-client");
  mkdirSync(join(clientRoot, ".dolt"), { recursive: true, mode: 0o700 });
  writePrivate(join(clientRoot, ".dolt", "config_global.json"), '{"metrics.disabled":"true"}\n');
  const database = initializeDolt(root, clientRoot);
  const doltPort = await port();
  const serverConfig = join(root, "dolt-server.yaml");
  writePrivate(serverConfig, `log_level: warning\ndata_dir: ${JSON.stringify(root)}\ncfg_dir: ${JSON.stringify(join(root, "dolt-cfg"))}\nbehavior:\n  read_only: false\nlistener:\n  host: 127.0.0.1\n  port: ${doltPort}\nsystem_variables:\n  dolt_force_transaction_commit: 0\n`);
  const dolt = start("dolt", ["sql-server", "--config=" + serverConfig], { cwd: root,
    env: { DOLT_ROOT_PATH: clientRoot, DOLT_DISABLE_EVENT_FLUSH: "1" } });
  ownedProcesses.push(dolt);
  await waitFor("Dolt", async () => {
    return await new Promise((accept) => {
      const socket = createConnection({ host: "127.0.0.1", port: doltPort });
      socket.once("connect", () => { socket.destroy(); accept(true); });
      socket.once("error", () => accept(false));
    });
  }, dolt);
  const taskstoreConfig = join(root, "private", "taskstore.json");
  run(engineBinary, ["bootstrap-taskstore", "--config-output", taskstoreConfig, "--address", `127.0.0.1:${doltPort}`,
    "--database", database, "--store-id", "production-workflow-store", "--control-user", "root",
    "--maintenance-root", join(root, "maintenance")]);
  const enginePort = await port();
  const paseoHost = "127.0.0.2";
  const paseoPort = 6767;
  const cacheRoot = join(root, "cache");
  const hostSocket = join(cacheRoot, "director", "runtime", "host.sock");
  const runtimeRoot = join(cacheRoot, "director", "runtime", "work");
  const password = randomBytes(32).toString("hex");
  const credential = join(root, "private", "paseo.credential");
  writePrivate(credential, password + "\n");
  const projectAdminToken = join(root, "private", "project-admin.token");
  writePrivate(projectAdminToken, randomBytes(32).toString("hex") + "\n");
  const configRoot = join(root, "config");
  writePrivate(join(configRoot, "director", "runtime.json"), JSON.stringify({ schemaVersion: 1,
    paseo: { credentialFile: credential, url: `ws://${paseoHost}:${paseoPort}/ws` }, engine: { mode: "development", url: `http://127.0.0.1:${enginePort}`,
      sourceRoot: join(repositoryRoot, "engine"), moduleCache: join(root, "go-path", "pkg", "mod") } }) + "\n");
  const engineArguments = ["serve-board", "--listen", `127.0.0.1:${enginePort}`,
    "--taskstore-config", taskstoreConfig, "--host-id", "production-host", "--host-label", "Production harness",
    "--host-socket", hostSocket, "--runtime-root", runtimeRoot, "--project-admin-token-file", projectAdminToken];
  const engineEnvironment = { PATH: harnessPath, XDG_CACHE_HOME: cacheRoot, XDG_CONFIG_HOME: configRoot,
    XDG_STATE_HOME: join(root, "state") };
  const startEngine = () => {
    const value = start(engineBinary, engineArguments, { env: engineEnvironment });
    ownedProcesses.push(value);
    return value;
  };
  let engine = startEngine();
  const paseoHome = join(root, "paseo-home");
  mkdirSync(paseoHome, { mode: 0o700 });
  writePrivate(join(paseoHome, "config.json"), JSON.stringify({ version: 1,
    daemon: { mcp: { enabled: false, injectIntoAgents: false }, browserTools: { enabled: false }, relay: { enabled: false } },
    features: { dictation: { enabled: false }, voiceMode: { enabled: false }, webUi: { enabled: true } },
    pluginsEnabled: true }, null, 2) + "\n");
  const daemon = start("paseo", ["daemon", "start", "--home", paseoHome, "--listen", `${paseoHost}:${paseoPort}`,
    "--foreground", "--no-relay", "--no-mcp", "--no-inject-mcp", "--web-ui"], { env: {
      PASEO_PASSWORD: password,
      PASEO_LOG_LEVEL: process.env.DIRECTOR_E2E_DEBUG_LOG ? "debug" : "warn",
      XDG_CACHE_HOME: cacheRoot, XDG_CONFIG_HOME: configRoot, PATH: harnessPath,
    } });
  ownedProcesses.push(daemon);
  await waitFor("Paseo daemon", async () => spawnSync("paseo", ["plugin", "ls", "--host", `${paseoHost}:${paseoPort}`, "--json"],
    { env: environment({ PASEO_PASSWORD: password }), encoding: "utf8" }).status === 0, daemon);
  const nativeProject = JSON.parse(run("paseo", ["project", "create", product, "--host", `${paseoHost}:${paseoPort}`, "--json"],
    { env: { PASEO_PASSWORD: password }, label: "Paseo Project creation" }));
  const nativeProjectID = nativeProject.projectId ?? nativeProject.id;
  assert(typeof nativeProjectID === "string" && nativeProjectID !== "", "Paseo Project creation returned no identity");
  const nativeWorkspace = JSON.parse(run("paseo", ["workspace", "create", "--isolation", "local", "--path", product,
    "--project", nativeProjectID, "--title", "Product", "--host", `${paseoHost}:${paseoPort}`, "--json"],
  { env: { PASEO_PASSWORD: password }, label: "Paseo root Workspace creation" }));
  const nativeWorkspaceID = nativeWorkspace.workspaceId ?? nativeWorkspace.id ?? nativeWorkspace.workspace?.id;
  assert(typeof nativeWorkspaceID === "string" && nativeWorkspaceID !== "", "Paseo root Workspace creation returned no identity");
  pullRequestOrganizer = initializePullRequestOrganizer(root, product, nativeWorkspaceID);
  try {
    run("paseo", ["plugin", "add", pathToFileURL(pluginSource).href, "--ref", sourceCandidate,
      "--host", `${paseoHost}:${paseoPort}`, "--json"],
      { env: { PASEO_PASSWORD: password }, label: "Paseo plugin installation" });
  } catch (error) {
    if (process.env.DIRECTOR_E2E_DEBUG_LOG) writePrivate(process.env.DIRECTOR_E2E_DEBUG_LOG, daemon.output());
    throw error;
  }
  try {
    await waitFor("Director host socket", async () => existsSync(hostSocket), daemon);
  } catch {
    const diagnose = (argumentsValue) => spawnSync("paseo", argumentsValue,
      { env: environment({ PASEO_PASSWORD: password }), encoding: "utf8", maxBuffer: 1024 * 1024 });
    const diagnostics = [
      diagnose(["plugin", "logs", "director", "--host", `${paseoHost}:${paseoPort}`, "--json"]),
      diagnose(["plugin", "ls", "--host", `${paseoHost}:${paseoPort}`, "--json"]),
      diagnose(["plugin", "status", "director", "--host", `${paseoHost}:${paseoPort}`, "--json"]),
    ];
    const diagnosticText = diagnostics.map((value) => `${value.stdout}\n${value.stderr}`).join("\n");
    if (process.env.DIRECTOR_E2E_DEBUG_LOG) {
      writePrivate(process.env.DIRECTOR_E2E_DEBUG_LOG, diagnosticText);
    }
    throw new Error(`Director host socket did not become ready: ${boundedDirectorCode(diagnosticText)}`);
  }
  try {
    await waitFor("Director Engine", async () => {
      try { return (await fetch(`http://127.0.0.1:${enginePort}/v1/board`)).status !== 0; } catch { return false; }
    }, engine);
  } catch (error) {
    if (process.env.DIRECTOR_E2E_DEBUG_LOG) writePrivate(process.env.DIRECTOR_E2E_DEBUG_LOG, engine.output());
    throw error;
  }
  const state = { schemaVersion: 1, root, product, remote: productGit.remote, fakeGitHubState, pullRequestOrganizer, enginePort, paseoHost, paseoPort, hostSocket,
    enginePID: engine.child.pid, daemonPID: daemon.child.pid, doltPID: dolt.child.pid,
    sourceCandidate, tree: git(repositoryRoot, "rev-parse", "HEAD^{tree}"),
    paseoVersion: "0.7.2", doltVersion: "2.3.2" };
  writePrivate(statePath, JSON.stringify(state));
  process.stdout.write(JSON.stringify({ event: "production-workflow.ready", enginePort, paseoPort,
    stateSHA256: createHash("sha256").update(JSON.stringify(state)).digest("hex") }) + "\n");
  if (!bootstrapOnly) {
    const workflow = await runFunctionalWorkflow({ enginePort, paseoHost, paseoPort, password, nativeProjectID,
      nativeWorkspaceID, pullRequestOrganizer, remote: productGit.remote, engineProcess: () => engine,
      restartEngine: async () => {
        await stop(engine);
        engine = startEngine();
        try {
          await waitFor("restarted Director Engine", async () => {
            try { return (await fetch(`http://127.0.0.1:${enginePort}/v1/board`)).status !== 0; } catch { return false; }
          }, engine);
        } catch (error) {
          if (process.env.DIRECTOR_E2E_DEBUG_LOG) writePrivate(process.env.DIRECTOR_E2E_DEBUG_LOG, engine.output());
          throw error;
        }
      } });
    state.enginePID = engine.child.pid;
    writePrivate(statePath, JSON.stringify(state));
    process.stdout.write(JSON.stringify({ event: "production-workflow.completed", paseoVersion: "0.7.2",
      doltVersion: "2.3.2", directCandidate: workflow.directCandidate, pullRequestCandidate: workflow.pullRequestCandidate,
      projects: workflow.projects, completedTasks: workflow.completedTasks, activeWorkspaces: workflow.activeWorkspaces }) + "\n");
  }
  if (hold) {
    await new Promise((accept) => process.stdin.once("data", accept));
    process.stdin.pause();
  }
  for (const process of ownedProcesses.reverse()) await stop(process);
  ownedProcesses.length = 0;
  await stopOwnedDescendants(root);
  if (existsSync(statePath)) rmSync(statePath);
  await removeOwnedRootAfterQuiescence(root);
  ownedRoot = "";
  assert.equal(existsSync(root), false);
  process.stdout.write(JSON.stringify({ event: "production-workflow.cleaned" }) + "\n");
}

try {
  await main();
} catch (error) {
  const code = error instanceof Error ? error.message.replaceAll(/\/[^ ]+/g, "[path]").slice(0, 512) : "unknown";
  process.stderr.write(`production workflow harness failed: ${code}\n`);
  for (const process of ownedProcesses.reverse()) await stop(process);
  if (ownedRoot) await stopOwnedDescendants(ownedRoot);
  if (existsSync(statePath)) rmSync(statePath);
  if (ownedRoot && process.env.DIRECTOR_E2E_PRESERVE_ON_FAILURE !== "1") {
    await removeOwnedRootAfterQuiescence(ownedRoot);
  }
  process.exitCode = 1;
}
