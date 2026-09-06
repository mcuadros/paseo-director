import { randomUUID } from "node:crypto";
import { readFile, rename, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { createPaseoClient } from "@getpaseo/client";
import { assertStableApi, runStructuralNegatives } from "./paseo-preflight.mjs";

const command = process.argv[2];
const url = process.env.DIRECTOR_PASEO_URL;
const password = process.env.DIRECTOR_PASEO_PASSWORD;
const statePath = process.env.DIRECTOR_TOPOLOGY_STATE;
const lifecycleRoot = process.env.DIRECTOR_LIFECYCLE_ROOT;
const sourceRepository = process.env.DIRECTOR_SOURCE_REPOSITORY;
const reviewPath = process.env.DIRECTOR_REVIEW_PATH;
const reviewSha = process.env.DIRECTOR_REVIEW_SHA;
const provider = process.env.DIRECTOR_TEST_PROVIDER;
const daemonPid = Number(process.env.DIRECTOR_DAEMON_PID);
const archiveChildPath = fileURLToPath(new URL("./archive-child.mjs", import.meta.url));

const parentLabel = "paseo.parent-agent-id";
const projectId = "adr-0010-topology-probe";
const directorWorkspaceId = "shared-repository";
const limits = {
  maxActiveTasks: 4,
  maxActiveTasksPerWorkspace: 2,
  maxConcurrentAgents: 8,
  maxSubagentsPerTask: 3,
};
const taskDefinitions = {
  alpha: {
    taskId: "topology-alpha",
    runId: "run-alpha-1",
    title: "Implement concurrent topology alpha",
    branch: "task/topology-alpha",
    worktreeSlug: "topology-alpha",
  },
  beta: {
    taskId: "topology-beta",
    runId: "run-beta-1",
    title: "Validate concurrent topology beta",
    branch: "task/topology-beta",
    worktreeSlug: "topology-beta",
  },
};
const reviewerTitle = "Review topology candidate";

if (!command) throw new Error("A topology command is required");

function requireObservation(condition, message) {
  if (!condition) throw new Error(`Unexpected topology observation: ${message}`);
}

function hasOwn(value, key) {
  return Object.prototype.hasOwnProperty.call(value, key);
}

function roleLabels({ taskId, runId, role, intent }) {
  return {
    "director.project": projectId,
    "director.workspace": directorWorkspaceId,
    "director.task": taskId,
    "director.run": runId,
    "director.role": role,
    "director.intent": intent,
  };
}

function buildTopLevelOptions({ cwd, title, labels, prompt, requestId, clientMessageId }) {
  const options = {
    config: { provider, modeId: "full-access" },
    title,
    labels,
    prompt,
    requestId,
    clientMessageId,
  };
  if (cwd !== undefined) options.cwd = cwd;
  requireObservation(!hasOwn(options, "parent"), "top-level create options contained parent");
  return options;
}

function buildHelperOptions({ parent, requestedCwd, title, labels, prompt, requestId, clientMessageId }) {
  const options = {
    config: { provider, modeId: "full-access" },
    cwd: requestedCwd,
    parent,
    title,
    labels,
    prompt,
    requestId,
    clientMessageId,
  };
  requireObservation(options.parent === parent, "helper create omitted its exact parent handle");
  return options;
}

function assertFixedLabels(snapshot, expected) {
  for (const [key, value] of Object.entries(expected)) {
    requireObservation(snapshot.labels?.[key] === value, `${snapshot.id} changed fixed label ${key}`);
  }
}

function parentAgentId(snapshot) {
  const value = snapshot?.labels?.[parentLabel];
  return typeof value === "string" && value.length > 0 ? value : null;
}

function terminationIsReconciled(snapshot, externalFactsReconciled) {
  return Boolean(snapshot?.status === "closed" && snapshot.archivedAt && externalFactsReconciled);
}

function agentSummary(snapshot) {
  if (!snapshot) return null;
  return {
    id: snapshot.id,
    workspaceId: snapshot.workspaceId,
    cwd: snapshot.cwd,
    title: snapshot.title,
    provider: snapshot.provider,
    status: snapshot.status,
    activeTurn: snapshot.activeTurn,
    parentAgentId: parentAgentId(snapshot),
    labels: snapshot.labels,
    createdAt: snapshot.createdAt,
    archivedAt: snapshot.archivedAt ?? null,
    providerSessionId: snapshot.persistence?.sessionId ?? snapshot.runtimeInfo?.sessionId ?? null,
  };
}

function workspaceSummary(snapshot) {
  if (!snapshot) return null;
  return {
    id: snapshot.id,
    projectId: snapshot.projectId,
    directory: snapshot.workspaceDirectory ?? snapshot.directory,
    name: snapshot.name,
    status: snapshot.status,
    archivedAt: snapshot.archivedAt ?? null,
  };
}

async function saveState(state) {
  const temporaryPath = `${statePath}.next`;
  await writeFile(temporaryPath, `${JSON.stringify(state, null, 2)}\n`, {
    encoding: "utf8",
    mode: 0o600,
  });
  await rename(temporaryPath, statePath);
}

async function loadState() {
  return JSON.parse(await readFile(statePath, "utf8"));
}

function pidIsAlive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    if (error?.code === "ESRCH") return false;
    throw error;
  }
}

async function waitForJsonFile(path, timeoutMs = 90_000) {
  const deadline = Date.now() + timeoutMs;
  let lastError;
  while (Date.now() < deadline) {
    try {
      return JSON.parse(await readFile(path, "utf8"));
    } catch (error) {
      lastError = error;
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
  }
  throw new Error(`Timed out waiting for durable prompt marker: ${lastError}`);
}

async function readOptionalJson(path) {
  try {
    return JSON.parse(await readFile(path, "utf8"));
  } catch (error) {
    if (error?.code === "ENOENT") return null;
    throw error;
  }
}

function terminationPathFor(markerPath) {
  return markerPath.replace(/\.started\.json$/, ".terminated.json");
}

async function requireNoGracefulSignal(markerPaths) {
  const observed = await Promise.all(markerPaths.map((path) => readOptionalJson(terminationPathFor(path))));
  requireObservation(
    observed.every((marker) => marker === null),
    "agent archive delivered a graceful child signal instead of hard-killing the process tree",
  );
  return { gracefulSignalsObserved: 0, hardKillObserved: true };
}

async function readProcessIdentity(pid) {
  const [stat, commandLineBuffer] = await Promise.all([
    readFile(`/proc/${pid}/stat`, "utf8"),
    readFile(`/proc/${pid}/cmdline`),
  ]);
  const closeParen = stat.lastIndexOf(")");
  requireObservation(closeParen > 0, `could not parse /proc/${pid}/stat`);
  const statFields = stat.slice(closeParen + 1).trim().split(/\s+/);
  return {
    pid,
    ppid: Number(statFields[1]),
    commandLine: commandLineBuffer.toString("utf8").split("\0").filter(Boolean).join(" "),
  };
}

async function collectOwnedProcessChain(childPid, token) {
  requireObservation(
    Number.isSafeInteger(daemonPid) && daemonPid > 1 && pidIsAlive(daemonPid),
    "exact isolated daemon PID was not live",
  );
  const daemonIdentity = await readProcessIdentity(daemonPid);
  requireObservation(
    daemonIdentity.commandLine.includes("Paseo Daemon"),
    "process ancestry boundary was not the isolated Paseo daemon",
  );
  const chain = [];
  let pid = childPid;
  let reachedDaemon = false;
  for (let depth = 0; depth < 24 && pid > 1 && pid !== daemonPid; depth += 1) {
    const identity = await readProcessIdentity(pid);
    chain.push(identity);
    if (identity.ppid === daemonPid) {
      reachedDaemon = true;
      break;
    }
    pid = identity.ppid;
  }
  requireObservation(
    chain.length > 0 && chain[0].pid === childPid && reachedDaemon,
    "prompt child ancestry did not terminate at the exact isolated daemon PID",
  );
  requireObservation(
    chain[0].commandLine.includes(archiveChildPath) && chain[0].commandLine.includes(token),
    "durable marker PID did not identify the exact prompt child",
  );
  const providerPids = chain
    .filter(
      ({ pid: candidatePid, commandLine }) =>
        candidatePid !== childPid && /(^|[ /])codex(?:[- ]|$)/i.test(commandLine),
    )
    .map(({ pid: candidatePid }) => candidatePid);
  requireObservation(providerPids.length > 0, "no Codex provider process was found in owned ancestry");
  return {
    childPid,
    providerPids,
    trackedPids: [...new Set(chain.map(({ pid: candidatePid }) => candidatePid))],
  };
}

async function waitForPidsGone(pids, timeoutMs = 15_000) {
  const startedAt = Date.now();
  while (Date.now() - startedAt < timeoutMs) {
    if (pids.every((pid) => !pidIsAlive(pid))) return Date.now() - startedAt;
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  throw new Error(`Owned processes survived archive: ${pids.filter(pidIsAlive).join(",")}`);
}

async function waitForAgent(agent, predicate, timeoutMs = 120_000) {
  const deadline = Date.now() + timeoutMs;
  let snapshot = null;
  while (Date.now() < deadline) {
    snapshot = (await agent.refresh())?.agent ?? null;
    if (snapshot && predicate(snapshot)) return snapshot;
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  throw new Error(`Timed out waiting for agent: ${JSON.stringify(agentSummary(snapshot))}`);
}

async function waitForWorkspace(workspace, timeoutMs = 120_000) {
  const deadline = Date.now() + timeoutMs;
  let snapshot = null;
  while (Date.now() < deadline) {
    snapshot = await workspace.refresh();
    if (snapshot?.status === "failed") {
      throw new Error(`Execution workspace failed: ${JSON.stringify(workspaceSummary(snapshot))}`);
    }
    if (snapshot?.status === "done" || snapshot?.status === "attention") return snapshot;
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  throw new Error(`Timed out waiting for workspace: ${JSON.stringify(workspaceSummary(snapshot))}`);
}

function markerPrompt(role) {
  const token = randomUUID();
  const markerPath = `${lifecycleRoot}/${role}-${token}.started.json`;
  const argumentsList = [process.execPath, archiveChildPath, markerPath, token];
  requireObservation(
    argumentsList.every((value) => /^[A-Za-z0-9_./-]+$/.test(value)),
    `${role} command contained an unsafe fixture argument`,
  );
  const exactCommand = argumentsList.map((value) => `'${value}'`).join(" ");
  return {
    token,
    markerPath,
    prompt: `Run this exact command and wait for it to finish: ${exactCommand}`,
  };
}

async function observePromptProcess(agent, promptEvidence) {
  const [running, marker] = await Promise.all([
    waitForAgent(agent, (snapshot) => snapshot.status === "running" && snapshot.activeTurn !== null),
    waitForJsonFile(promptEvidence.markerPath),
  ]);
  requireObservation(
    marker.token === promptEvidence.token &&
      Number.isSafeInteger(marker.pid) &&
      marker.pid > 1 &&
      pidIsAlive(marker.pid),
    "prompt marker did not identify one exact live child",
  );
  const processEvidence = await collectOwnedProcessChain(marker.pid, promptEvidence.token);
  return { running: agentSummary(running), marker, processEvidence };
}

async function connect() {
  const client = createPaseoClient({ url, password, reconnect: { enabled: false } });
  assertStableApi(client);
  await client.connect();
  assertStableApi(client);
  return client;
}

async function listByLabels(client, labels, includeArchived = true) {
  const result = await client.agents.list({
    filter: { labels, includeArchived },
    page: { limit: 100 },
  });
  return result.entries.map(({ agent }) => agent);
}

async function refreshRequired(client, agentId) {
  const result = await client.agents.ref(agentId).refresh();
  requireObservation(result?.agent, `agent ${agentId} was not recoverable by ref/refresh`);
  return result.agent;
}

async function createTaskWorkspace(client, state, key) {
  const task = state.tasks[key];
  const workspace = await client.workspaces.create({
    requestId: task.workspaceIntent.requestId,
    title: `${task.title} execution workspace`,
    source: {
      kind: "worktree",
      cwd: sourceRepository,
      action: "branch-off",
      refName: reviewSha,
      branchName: task.branch,
      worktreeSlug: task.worktreeSlug,
    },
  });
  const snapshot = await waitForWorkspace(workspace);
  task.workspaceId = workspace.id;
  task.workspaceDirectory = snapshot.workspaceDirectory ?? snapshot.directory;
  task.workspaceObservedAt = new Date().toISOString();
  await saveState(state);
  return workspaceSummary(snapshot);
}

async function createTaskAgent(client, state, key) {
  const task = state.tasks[key];
  const promptEvidence = markerPrompt(`task-${key}`);
  task.agentIntent.promptToken = promptEvidence.token;
  task.agentIntent.markerPath = promptEvidence.markerPath;
  await saveState(state);
  const options = buildTopLevelOptions({
    title: task.title,
    labels: task.labels,
    prompt: promptEvidence.prompt,
    requestId: task.agentIntent.requestId,
    clientMessageId: task.agentIntent.clientMessageId,
  });
  const agent = await client.workspaces.ref(task.workspaceId).agents.create(options);
  task.originalAgentId = agent.id;
  task.currentAgentId = agent.id;
  task.agentObservedAt = new Date().toISOString();
  await saveState(state);
  const observed = await observePromptProcess(agent, promptEvidence);
  task.originalProcess = observed.processEvidence;
  task.currentProcess = observed.processEvidence;
  task.currentMarkerPath = promptEvidence.markerPath;
  await saveState(state);
  const snapshot = (await agent.refresh())?.agent;
  requireObservation(snapshot?.title === task.title, `${key} Task Agent title was decorated`);
  requireObservation(parentAgentId(snapshot) === null, `${key} Task Agent acquired a parent`);
  requireObservation(snapshot.workspaceId === task.workspaceId, `${key} Task Agent changed workspace`);
  assertFixedLabels(snapshot, task.labels);
  return observed;
}

async function createReviewer(client, state) {
  const reviewer = state.reviewer;
  const options = buildTopLevelOptions({
    title: reviewer.title,
    labels: reviewer.labels,
    prompt: `Reply with exactly ${reviewer.promptToken}.`,
    requestId: reviewer.agentIntent.requestId,
    clientMessageId: reviewer.agentIntent.clientMessageId,
  });
  const agent = await client.workspaces.ref(reviewer.workspaceId).agents.create(options);
  reviewer.agentId = agent.id;
  reviewer.agentObservedAt = new Date().toISOString();
  await saveState(state);
  const finish = await agent.waitForFinish(120_000);
  const snapshot = (await agent.refresh())?.agent;
  requireObservation(snapshot?.title === reviewer.title, "Reviewer title changed");
  requireObservation(parentAgentId(snapshot) === null, "Reviewer acquired a parent");
  requireObservation(snapshot.workspaceId === reviewer.workspaceId, "Reviewer changed workspace");
  requireObservation(snapshot.cwd === reviewPath, "Reviewer changed detached checkout path");
  requireObservation(finish.lastMessage?.trim() === reviewer.promptToken, "Reviewer prompt result changed");
  assertFixedLabels(snapshot, reviewer.labels);
  return { finish, reviewer: agentSummary(snapshot) };
}

function providerSessionId(snapshot) {
  return snapshot?.persistence?.sessionId ?? snapshot?.runtimeInfo?.sessionId ?? null;
}

async function resumeSameSession(client, record, promptToken) {
  const agent = client.agents.ref(record.agentId);
  const before = await refreshRequired(client, record.agentId);
  requireObservation(!before.archivedAt, `${record.title} was archived before resume`);
  requireObservation(before.activeTurn == null, `${record.title} still had an active turn before resume`);
  const beforeSession = providerSessionId(before);
  requireObservation(beforeSession, `${record.title} had no resumable provider session`);
  await agent.send(`Reply with exactly ${promptToken}.`);
  const finish = await agent.waitForFinish(120_000);
  const after = await refreshRequired(client, record.agentId);
  requireObservation(finish.lastMessage?.trim() === promptToken, `${record.title} resume prompt changed`);
  requireObservation(providerSessionId(after) === beforeSession, `${record.title} changed provider session`);
  requireObservation(after.id === before.id, `${record.title} changed native identity`);
  return { before: agentSummary(before), finish, after: agentSummary(after), sameSession: true };
}

async function startExistingProcess(client, record, role) {
  const agent = client.agents.ref(record.agentId);
  const before = await refreshRequired(client, record.agentId);
  requireObservation(!before.archivedAt && before.activeTurn == null, `${record.title} was not resumable`);
  const promptEvidence = markerPrompt(role);
  await agent.send(promptEvidence.prompt);
  const observed = await observePromptProcess(agent, promptEvidence);
  record.currentProcess = observed.processEvidence;
  record.currentMarkerPath = promptEvidence.markerPath;
  record.processHistory = [
    ...(record.processHistory ?? []),
    {
      markerPath: promptEvidence.markerPath,
      token: promptEvidence.token,
      processEvidence: observed.processEvidence,
    },
  ];
  return { ...observed, promptEvidence };
}

function requireAdmittedHelperRequest(request) {
  for (const forbidden of ["projectId", "workspaceId", "taskId", "runId", "role", "labels", "parent"]) {
    requireObservation(!hasOwn(request, forbidden), `helper request selected forbidden scope field ${forbidden}`);
  }
  requireObservation(typeof request.requestedCwd === "string", "helper request cwd was missing");
}

function helperAdapter(client, injected = {}) {
  return {
    save: injected.save ?? saveState,
    capacity: injected.capacity ?? ((state) => capacityFacts(client, state)),
    list: injected.list ?? ((labels) => listByLabels(client, labels)),
    refresh: injected.refresh ?? ((agentId) => refreshRequired(client, agentId)),
    parentRef: injected.parentRef ?? ((agentId) => client.agents.ref(agentId)),
    marker: injected.marker ?? markerPrompt,
    create: injected.create ?? ((options) => client.agents.create(options)),
    observe: injected.observe ?? observePromptProcess,
    now: injected.now ?? (() => new Date().toISOString()),
  };
}

async function reserveAndCreateHelper(client, state, key, request, injected = {}) {
  requireAdmittedHelperRequest(request);
  const adapter = helperAdapter(client, injected);
  const helper = state.helpers[key];
  const parentTask = state.tasks.alpha;
  if (helper.agentId) {
    const existing = await adapter.refresh(helper.agentId);
    return { created: false, helper: agentSummary(existing), adapterIdempotencyHit: true };
  }

  const reservedAtEntry = helper.reservedAt;
  if (!reservedAtEntry) {
    const reservedHelpers = Object.values(state.helpers).filter((candidate) => candidate.reservedAt).length;
    requireObservation(reservedHelpers < limits.maxSubagentsPerTask, "Task helper quota was exhausted");
    const accountingBefore = await adapter.capacity(state);
    requireObservation(
      accountingBefore.globalConsumed < limits.maxConcurrentAgents,
      "global agent capacity was exhausted",
    );
    helper.reservedAt = adapter.now();
    helper.reservationStatus = "reserved";
    await adapter.save(state);
  } else {
    const matches = await adapter.list({ "director.intent": helper.labels["director.intent"] });
    if (matches.length === 1) {
      helper.agentId = matches[0].id;
      helper.observedAt = adapter.now();
      helper.reservationStatus = "observed";
      await adapter.save(state);
      return { created: false, helper: agentSummary(matches[0]), reconciledUnknownResult: true };
    }
    throw new Error(
      `Helper creation result is unknown; parked with ${matches.length} observed matches and no retry`,
    );
  }

  const promptEvidence = adapter.marker(`helper-${key}`);
  helper.promptToken = promptEvidence.token;
  helper.markerPath = promptEvidence.markerPath;
  await adapter.save(state);
  const parent = adapter.parentRef(parentTask.currentAgentId);
  const options = buildHelperOptions({
    parent,
    requestedCwd: request.requestedCwd,
    title: helper.title,
    labels: helper.labels,
    prompt: promptEvidence.prompt,
    requestId: helper.requestId,
    clientMessageId: helper.clientMessageId,
  });
  const agent = await adapter.create(options);
  helper.agentId = agent.id;
  helper.workspaceId = parentTask.workspaceId;
  helper.workspaceDirectory = parentTask.workspaceDirectory;
  helper.observedAt = adapter.now();
  helper.reservationStatus = "observed";
  await adapter.save(state);
  const observed = await adapter.observe(agent, promptEvidence);
  helper.process = observed.processEvidence;
  helper.currentProcess = observed.processEvidence;
  helper.currentMarkerPath = promptEvidence.markerPath;
  await adapter.save(state);
  const snapshot = await adapter.refresh(agent.id);
  requireObservation(parentAgentId(snapshot) === parentTask.currentAgentId, `${key} helper parent changed`);
  requireObservation(snapshot.workspaceId === parentTask.workspaceId, `${key} helper escaped parent workspace`);
  requireObservation(snapshot.cwd === parentTask.workspaceDirectory, `${key} helper escaped parent cwd`);
  assertFixedLabels(snapshot, helper.labels);
  return { created: true, helper: agentSummary(snapshot), requestedCwd: request.requestedCwd, observed };
}

async function reconcileRecord(client, record, expectedParentId = null) {
  const snapshot = await refreshRequired(client, record.agentId);
  requireObservation(snapshot.title === record.title, `${record.labels["director.intent"]} title changed`);
  requireObservation(
    parentAgentId(snapshot) === expectedParentId,
    `${record.labels["director.intent"]} parent changed`,
  );
  requireObservation(snapshot.workspaceId === record.workspaceId, `${record.labels["director.intent"]} workspace changed`);
  assertFixedLabels(snapshot, record.labels);
  const matches = await listByLabels(client, { "director.intent": record.labels["director.intent"] });
  requireObservation(matches.length === 1, `${record.labels["director.intent"]} duplicated during recovery`);
  return snapshot;
}

async function capacityFacts(client, state) {
  const records = await listByLabels(client, { "director.project": projectId }, true);
  const capacityBearing = records.filter((snapshot) => !snapshot.archivedAt);
  const byRole = Object.fromEntries(
    ["task-agent", "reviewer", "helper"].map((role) => [
      role,
      capacityBearing.filter((snapshot) => snapshot.labels?.["director.role"] === role).length,
    ]),
  );
  const helperByRun = Object.fromEntries(
    Object.values(state.tasks).map((task) => [
      `${task.taskId}/${task.runId}`,
      capacityBearing.filter(
        (snapshot) =>
          snapshot.labels?.["director.role"] === "helper" &&
          snapshot.labels?.["director.task"] === task.taskId &&
          snapshot.labels?.["director.run"] === task.runId,
      ).length,
    ]),
  );
  const taskAgentsInDirectorWorkspace = capacityBearing.filter(
    (snapshot) =>
      snapshot.labels?.["director.role"] === "task-agent" &&
      snapshot.labels?.["director.workspace"] === directorWorkspaceId,
  ).length;
  return {
    limits,
    globalConsumed: byRole["task-agent"] + byRole.reviewer + byRole.helper,
    organizerCounted: false,
    byRole,
    helperByRun,
    activeTasksInDirectorWorkspace: taskAgentsInDirectorWorkspace,
  };
}

function ownershipFacts(state) {
  const helperFacts = Object.fromEntries(
    Object.entries(state.helpers).map(([key, helper]) => [
      key,
      {
        agentId: helper.agentId,
        role: helper.labels["director.role"],
        taskOwner: false,
        runOwner: false,
        candidateOwnerOfRecord: false,
        ownerTaskAgentId: state.tasks.alpha.currentAgentId,
      },
    ]),
  );
  for (const helper of Object.values(helperFacts)) {
    requireObservation(helper.role === "helper", "helper was promoted to a Task Agent role");
    requireObservation(
      helper.agentId !== helper.ownerTaskAgentId,
      "helper identity replaced the Task/Run/Candidate owner",
    );
  }
  return {
    taskOwners: Object.fromEntries(
      Object.entries(state.tasks).map(([key, task]) => [key, task.currentAgentId]),
    ),
    runOwners: Object.fromEntries(
      Object.entries(state.tasks).map(([key, task]) => [key, task.currentAgentId]),
    ),
    candidateOwnersOfRecord: Object.fromEntries(
      Object.entries(state.tasks).map(([key, task]) => [key, task.currentAgentId]),
    ),
    helpers: helperFacts,
  };
}

async function recoverTopology(client, state) {
  const tasks = {};
  for (const [key, task] of Object.entries(state.tasks)) {
    const snapshot = await reconcileRecord(
      client,
      {
        agentId: task.currentAgentId,
        title: task.title,
        workspaceId: task.workspaceId,
        labels: task.currentLabels ?? task.labels,
      },
      null,
    );
    tasks[key] = agentSummary(snapshot);
  }
  const reviewer = await reconcileRecord(client, state.reviewer, null);
  const helpers = {};
  for (const [key, helper] of Object.entries(state.helpers)) {
    if (!helper.agentId) continue;
    helpers[key] = agentSummary(
      await reconcileRecord(
        client,
        {
          ...helper,
          workspaceId: helper.workspaceId ?? state.tasks.alpha.workspaceId,
        },
        state.tasks.alpha.originalAgentId,
      ),
    );
  }

  const workspaces = {};
  for (const [key, record] of [
    ...Object.entries(state.tasks),
    ["reviewer", state.reviewer],
  ]) {
    const snapshot = await client.workspaces.ref(record.workspaceId).refresh();
    requireObservation(snapshot, `${key} workspace was not recoverable by ref/refresh`);
    workspaces[key] = workspaceSummary(snapshot);
  }
  requireObservation(
    new Set(Object.values(workspaces).map(({ id }) => id)).size === 3,
    "Task/Reviewer workspace identity duplicated",
  );
  requireObservation(
    new Set([workspaces.alpha.directory, workspaces.beta.directory]).size === 2,
    "concurrent Task Agents shared an Execution Workspace/worktree",
  );
  requireObservation(
    workspaces.reviewer.directory === state.reviewer.workspaceDirectory,
    "Reviewer moved away from its detached disposable checkout",
  );
  return {
    tasks,
    reviewer: agentSummary(reviewer),
    helpers,
    workspaces,
    capacity: await capacityFacts(client, state),
    ownership: ownershipFacts(state),
  };
}

function restartRecords(state) {
  return {
    alpha: {
      agentId: state.tasks.alpha.currentAgentId,
      title: state.tasks.alpha.title,
    },
    betaReplacement: {
      agentId: state.tasks.beta.currentAgentId,
      title: state.tasks.beta.title,
    },
    reviewer: {
      agentId: state.reviewer.agentId,
      title: state.reviewer.title,
    },
    cascadeHelper: {
      agentId: state.helpers.cascade.agentId,
      title: state.helpers.cascade.title,
    },
  };
}

async function assertRestartProjection(client, state, kind) {
  const recovered = await recoverTopology(client, state);
  const projections = {
    alpha: recovered.tasks.alpha,
    betaReplacement: recovered.tasks.beta,
    reviewer: recovered.reviewer,
    cascadeHelper: recovered.helpers.cascade,
  };
  const expectedStatus =
    kind === "orderly"
      ? {
          alpha: "closed",
          betaReplacement: "closed",
          reviewer: "closed",
          cascadeHelper: "closed",
        }
      : {
          alpha: "running",
          betaReplacement: "idle",
          reviewer: "idle",
          cascadeHelper: "running",
        };
  requireObservation(kind === "orderly" || kind === "abrupt", `unknown restart kind ${kind}`);
  for (const [key, snapshot] of Object.entries(projections)) {
    requireObservation(snapshot.status === expectedStatus[key], `${kind} ${key} status changed`);
    requireObservation(snapshot.archivedAt === null, `${kind} ${key} was incorrectly archived`);
    requireObservation(snapshot.activeTurn == null, `${kind} ${key} retained an active turn`);
    requireObservation(
      !terminationIsReconciled(snapshot, true),
      `${kind} ${key} was incorrectly treated as terminated`,
    );
  }
  requireObservation(recovered.capacity.globalConsumed === 4, `${kind} freed unarchived capacity`);
  requireObservation(recovered.capacity.byRole["task-agent"] === 2, `${kind} Task capacity changed`);
  requireObservation(recovered.capacity.byRole.reviewer === 1, `${kind} Reviewer capacity changed`);
  requireObservation(recovered.capacity.byRole.helper === 1, `${kind} helper capacity changed`);
  return { kind, projections, capacity: recovered.capacity, terminated: false };
}

async function resumeRestartRecords(client, state, kind) {
  const before = await assertRestartProjection(client, state, kind);
  const resumed = {};
  for (const [key, record] of Object.entries(restartRecords(state))) {
    const token = `${key.toUpperCase()}_AFTER_${kind.toUpperCase()}_${randomUUID().replaceAll("-", "_")}`;
    resumed[key] = await resumeSameSession(client, record, token);
  }
  state.restartHistory = [
    ...(state.restartHistory ?? []),
    {
      kind,
      reconciledAt: new Date().toISOString(),
      agentIds: Object.fromEntries(
        Object.entries(resumed).map(([key, result]) => [key, result.after.id]),
      ),
    },
  ];
  await saveState(state);
  return { before, resumed, capacity: await capacityFacts(client, state) };
}

async function startRestartPair(client, state, label) {
  const alpha = await startExistingProcess(
    client,
    { ...state.tasks.alpha, agentId: state.tasks.alpha.currentAgentId },
    `task-alpha-${label}`,
  );
  state.tasks.alpha.currentProcess = alpha.processEvidence;
  state.tasks.alpha.currentMarkerPath = alpha.promptEvidence.markerPath;
  await saveState(state);
  const helper = await startExistingProcess(
    client,
    state.helpers.cascade,
    `helper-cascade-${label}`,
  );
  state.helpers.cascade.currentProcess = helper.processEvidence;
  state.helpers.cascade.currentMarkerPath = helper.promptEvidence.markerPath;
  await saveState(state);
  return { label, alpha, helper, capacity: await capacityFacts(client, state) };
}

async function archiveReviewerWorkspaceCascade(client, state) {
  const reviewerBefore = await refreshRequired(client, state.reviewer.agentId);
  requireObservation(!reviewerBefore.archivedAt, "Reviewer was already archived before workspace cascade");
  const workspace = client.workspaces.ref(state.reviewer.workspaceId);
  const first = await workspace.archive();
  const reviewerAfter = await refreshRequired(client, state.reviewer.agentId);
  const cascadeOffsetMs = Date.parse(first.archivedAt) - Date.parse(reviewerAfter.archivedAt);
  requireObservation(
    reviewerAfter.status === "closed" &&
      reviewerAfter.archivedAt &&
      Number.isFinite(cascadeOffsetMs) &&
      cascadeOffsetMs >= 0 &&
      cascadeOffsetMs < 5_000,
    "workspace archive did not cascade an archivedAt to the Reviewer before completing",
  );
  const second = await workspace.archive();
  requireObservation(second.archivedAt === first.archivedAt, "workspace cascade retry changed archivedAt");
  const workspaceAfter = await workspace.refresh();
  requireObservation(workspaceAfter === null, "archived Reviewer workspace remained active");
  state.reviewer.workspaceCascadeArchivedAt = first.archivedAt;
  await saveState(state);
  return {
    reviewerBefore: agentSummary(reviewerBefore),
    first,
    second,
    reviewerAfter: agentSummary(reviewerAfter),
    cascadeOffsetMs,
    workspaceAfter,
    capacity: await capacityFacts(client, state),
  };
}

async function archiveIfActive(client, agentId) {
  const agent = client.agents.ref(agentId);
  const before = (await agent.refresh())?.agent;
  if (!before || before.archivedAt) {
    return { before: agentSummary(before), archived: false };
  }
  const result = await agent.archive();
  return { before: agentSummary(before), archived: true, result };
}

async function createReplacementAfterTermination(previous, externalFactsReconciled, create) {
  requireObservation(
    terminationIsReconciled(previous, externalFactsReconciled),
    "replacement requires closed plus archivedAt and reconciled external facts",
  );
  return create();
}

function policyFixture({ reserved = false, matches = [] } = {}) {
  const helperLabels = roleLabels({
    taskId: taskDefinitions.alpha.taskId,
    runId: taskDefinitions.alpha.runId,
    role: "helper",
    intent: "helper-policy-fixture",
  });
  const state = {
    tasks: {
      alpha: {
        ...taskDefinitions.alpha,
        currentAgentId: "task-alpha-agent-id",
        originalAgentId: "task-alpha-agent-id",
        workspaceId: "task-alpha-workspace-id",
        workspaceDirectory: "/owned/task-alpha",
      },
      beta: { ...taskDefinitions.beta },
    },
    reviewer: {},
    helpers: {
      policy: {
        title: "Policy helper",
        labels: helperLabels,
        requestId: "helper-request-id",
        clientMessageId: "helper-message-id",
        ...(reserved
          ? { reservedAt: "2026-09-06T00:00:00.000Z", reservationStatus: "reserved" }
          : {}),
      },
    },
  };
  const counters = { creates: 0, prompts: 0, saves: 0, lists: 0, refreshes: 0 };
  const snapshot = {
    id: "helper-agent-id",
    workspaceId: state.tasks.alpha.workspaceId,
    cwd: state.tasks.alpha.workspaceDirectory,
    title: state.helpers.policy.title,
    provider: "codex",
    status: "running",
    activeTurn: { turnId: "turn-1", startedAt: "2026-09-06T00:00:01.000Z" },
    labels: { ...helperLabels, [parentLabel]: state.tasks.alpha.currentAgentId },
    createdAt: "2026-09-06T00:00:01.000Z",
    archivedAt: null,
    persistence: { sessionId: "helper-session-id" },
  };
  const injected = {
    async save() {
      counters.saves += 1;
    },
    async capacity() {
      return { globalConsumed: 1 };
    },
    async list() {
      counters.lists += 1;
      return matches;
    },
    async refresh() {
      counters.refreshes += 1;
      return snapshot;
    },
    parentRef(agentId) {
      return { id: agentId };
    },
    marker() {
      return {
        token: "helper-token",
        markerPath: "/owned/helper.started.json",
        prompt: "helper-prompt",
      };
    },
    async create(options) {
      counters.creates += 1;
      if (options.prompt) counters.prompts += 1;
      return { id: snapshot.id };
    },
    async observe() {
      return {
        running: agentSummary(snapshot),
        marker: { token: "helper-token", pid: 2 },
        processEvidence: { childPid: 2, providerPids: [3], trackedPids: [2, 3] },
      };
    },
    now() {
      return "2026-09-06T00:00:00.000Z";
    },
  };
  return { state, counters, snapshot, injected };
}

async function captureExpectedFailure(runCase, expectedFragment) {
  let error = null;
  try {
    await runCase();
  } catch (caught) {
    error = caught instanceof Error ? caught.message : String(caught);
  }
  requireObservation(error?.includes(expectedFragment), `expected policy failure containing ${expectedFragment}`);
  return error;
}

async function runPolicyNegatives() {
  const totals = { creates: 0, prompts: 0 };
  const topLevel = buildTopLevelOptions({
    cwd: "/owned/task",
    title: taskDefinitions.alpha.title,
    labels: roleLabels({
      taskId: taskDefinitions.alpha.taskId,
      runId: taskDefinitions.alpha.runId,
      role: "task-agent",
      intent: "task-alpha-agent",
    }),
    prompt: "prompt-token",
    requestId: randomUUID(),
    clientMessageId: randomUUID(),
  });
  requireObservation(!hasOwn(topLevel, "parent"), "top-level negative acquired a parent");

  const deniedScope = policyFixture();
  const deniedScopeError = await captureExpectedFailure(
    () =>
      reserveAndCreateHelper(
        null,
        deniedScope.state,
        "policy",
        { requestedCwd: "/another/workspace", taskId: "another-task" },
        deniedScope.injected,
      ),
    "forbidden scope field taskId",
  );
  requireObservation(
    deniedScope.counters.creates === 0 && deniedScope.counters.prompts === 0,
    "denied helper scope produced a create or prompt",
  );

  const unknown = policyFixture({ reserved: true, matches: [] });
  const unknownError = await captureExpectedFailure(
    () =>
      reserveAndCreateHelper(
        null,
        unknown.state,
        "policy",
        { requestedCwd: "/owned/task-alpha" },
        unknown.injected,
      ),
    "unknown; parked",
  );
  requireObservation(
    unknown.counters.lists === 1 && unknown.counters.creates === 0 && unknown.counters.prompts === 0,
    "unknown helper result performed a blind retry",
  );

  const idempotent = policyFixture();
  const first = await reserveAndCreateHelper(
    null,
    idempotent.state,
    "policy",
    { requestedCwd: "/owned/task-alpha" },
    idempotent.injected,
  );
  const second = await reserveAndCreateHelper(
    null,
    idempotent.state,
    "policy",
    { requestedCwd: "/owned/task-alpha" },
    idempotent.injected,
  );
  requireObservation(
    first.helper.id === second.helper.id &&
      second.adapterIdempotencyHit &&
      idempotent.counters.creates === 1 &&
      idempotent.counters.prompts === 1,
    "Director adapter idempotency did not suppress a second create/prompt",
  );

  const replacementEffects = { creates: 0, prompts: 0 };
  const closedUnarchived = { status: "closed", archivedAt: null };
  const replacementError = await captureExpectedFailure(
    () =>
      createReplacementAfterTermination(closedUnarchived, true, async () => {
        replacementEffects.creates += 1;
        replacementEffects.prompts += 1;
      }),
    "closed plus archivedAt",
  );
  requireObservation(
    replacementEffects.creates === 0 && replacementEffects.prompts === 0,
    "closed-unarchived Task Agent allowed replacement create/prompt",
  );

  const unreconciledEffects = { creates: 0, prompts: 0 };
  const closedArchived = {
    status: "closed",
    archivedAt: "2026-09-06T00:00:00.000Z",
  };
  const unreconciledError = await captureExpectedFailure(
    () =>
      createReplacementAfterTermination(closedArchived, false, async () => {
        unreconciledEffects.creates += 1;
        unreconciledEffects.prompts += 1;
      }),
    "reconciled external facts",
  );
  requireObservation(
    unreconciledEffects.creates === 0 && unreconciledEffects.prompts === 0,
    "unreconciled archived Task Agent allowed replacement create/prompt",
  );

  const archiveEffects = { archives: 0 };
  const closedUnarchivedSnapshot = {
    id: "closed-unarchived-agent-id",
    status: "closed",
    archivedAt: null,
    labels: {},
  };
  const archiveClient = {
    agents: {
      ref() {
        return {
          async refresh() {
            return { agent: closedUnarchivedSnapshot };
          },
          async archive() {
            archiveEffects.archives += 1;
            return { archivedAt: "2026-09-06T00:00:01.000Z" };
          },
        };
      },
    },
  };
  const closedArchiveResult = await archiveIfActive(
    archiveClient,
    closedUnarchivedSnapshot.id,
  );
  requireObservation(
    closedArchiveResult.archived && archiveEffects.archives === 1,
    "closed-unarchived record was skipped by explicit archive",
  );

  for (const counters of [
    deniedScope.counters,
    unknown.counters,
    replacementEffects,
    unreconciledEffects,
  ]) {
    totals.creates += counters.creates;
    totals.prompts += counters.prompts;
  }

  return {
    cases: 7,
    sideEffects: totals.creates + totals.prompts,
    deniedPathCreates: totals.creates,
    deniedPathPrompts: totals.prompts,
    topLevelParentFieldOmitted: !hasOwn(topLevel, "parent"),
    helperScopeOverride: {
      error: deniedScopeError,
      creates: deniedScope.counters.creates,
      prompts: deniedScope.counters.prompts,
    },
    unknownHelperResult: {
      error: unknownError,
      reconciliations: unknown.counters.lists,
      creates: unknown.counters.creates,
      prompts: unknown.counters.prompts,
    },
    adapterIdempotency: {
      sameAgentId: first.helper.id === second.helper.id,
      creates: idempotent.counters.creates,
      prompts: idempotent.counters.prompts,
      reservationStatus: idempotent.state.helpers.policy.reservationStatus,
    },
    closedUnarchivedReplacement: {
      error: replacementError,
      creates: replacementEffects.creates,
      prompts: replacementEffects.prompts,
    },
    archivedButUnreconciledReplacement: {
      error: unreconciledError,
      creates: unreconciledEffects.creates,
      prompts: unreconciledEffects.prompts,
    },
    closedUnarchivedArchive: {
      archived: closedArchiveResult.archived,
      archiveCalls: archiveEffects.archives,
    },
  };
}

async function run() {
  if (command === "topology-negative-structural") {
    console.log(JSON.stringify(runStructuralNegatives("topology")));
    return;
  }

  if (command === "topology-policy-negatives") {
    console.log(JSON.stringify(await runPolicyNegatives()));
    return;
  }

  for (const [name, value] of Object.entries({ url, password, statePath })) {
    if (!value) throw new Error(`Missing required environment variable: ${name}`);
  }

  const client = await connect();
  try {
    if (command === "topology-create") {
      for (const [name, value] of Object.entries({
        lifecycleRoot,
        sourceRepository,
        reviewPath,
        reviewSha,
        provider,
      })) {
        if (!value) throw new Error(`Missing required environment variable: ${name}`);
      }
      requireObservation(Number.isSafeInteger(daemonPid) && daemonPid > 1, "daemon PID is required");
      const state = {
        version: 1,
        projectId,
        directorWorkspaceId,
        sourceRepository,
        reviewSha,
        limits,
        tasks: Object.fromEntries(
          Object.entries(taskDefinitions).map(([key, definition]) => [
            key,
            {
              ...definition,
              labels: roleLabels({
                taskId: definition.taskId,
                runId: definition.runId,
                role: "task-agent",
                intent: `task-${key}-agent`,
              }),
              workspaceIntent: { requestId: randomUUID(), recordedAt: new Date().toISOString() },
              agentIntent: {
                requestId: randomUUID(),
                clientMessageId: randomUUID(),
                recordedAt: new Date().toISOString(),
              },
            },
          ]),
        ),
        reviewer: {
          title: reviewerTitle,
          taskId: taskDefinitions.beta.taskId,
          runId: taskDefinitions.beta.runId,
          candidateSha: reviewSha,
          promptToken: `REVIEWER_READY_${randomUUID().replaceAll("-", "_")}`,
          labels: roleLabels({
            taskId: taskDefinitions.beta.taskId,
            runId: taskDefinitions.beta.runId,
            role: "reviewer",
            intent: "reviewer-agent",
          }),
          workspaceIntent: { requestId: randomUUID(), recordedAt: new Date().toISOString() },
          agentIntent: {
            requestId: randomUUID(),
            clientMessageId: randomUUID(),
            recordedAt: new Date().toISOString(),
          },
        },
        helpers: Object.fromEntries(
          ["explicit", "cascade"].map((key) => [
            key,
            {
              title: `Topology ${key} helper`,
              labels: roleLabels({
                taskId: taskDefinitions.alpha.taskId,
                runId: taskDefinitions.alpha.runId,
                role: "helper",
                intent: `helper-${key}`,
              }),
              requestId: randomUUID(),
              clientMessageId: randomUUID(),
              idempotencyKey: `${taskDefinitions.alpha.runId}/helper-${key}`,
              taskOwner: false,
              runOwner: false,
              candidateOwnerOfRecord: false,
            },
          ]),
        ),
      };
      await saveState(state);

      const workspaceAlpha = await createTaskWorkspace(client, state, "alpha");
      const workspaceBeta = await createTaskWorkspace(client, state, "beta");
      const reviewWorkspace = await client.workspaces.create({
        requestId: state.reviewer.workspaceIntent.requestId,
        title: `${reviewerTitle} detached workspace`,
        source: { kind: "directory", path: reviewPath },
      });
      const reviewWorkspaceSnapshot = await waitForWorkspace(reviewWorkspace);
      state.reviewer.workspaceId = reviewWorkspace.id;
      state.reviewer.workspaceDirectory =
        reviewWorkspaceSnapshot.workspaceDirectory ?? reviewWorkspaceSnapshot.directory;
      state.reviewer.workspaceObservedAt = new Date().toISOString();
      await saveState(state);

      const alpha = await createTaskAgent(client, state, "alpha");
      const beta = await createTaskAgent(client, state, "beta");
      const alphaNow = await refreshRequired(client, state.tasks.alpha.currentAgentId);
      const betaNow = await refreshRequired(client, state.tasks.beta.currentAgentId);
      requireObservation(
        alphaNow.status === "running" && betaNow.status === "running",
        "Task Agents were not concurrently active",
      );
      requireObservation(
        state.tasks.alpha.workspaceId !== state.tasks.beta.workspaceId &&
          state.tasks.alpha.workspaceDirectory !== state.tasks.beta.workspaceDirectory,
        "concurrent Task Agents shared workspace identity or path",
      );
      const reviewer = await createReviewer(client, state);
      const explicit = await reserveAndCreateHelper(
        client,
        state,
        "explicit",
        { requestedCwd: state.tasks.beta.workspaceDirectory },
      );
      const explicitRetry = await reserveAndCreateHelper(
        client,
        state,
        "explicit",
        { requestedCwd: state.tasks.beta.workspaceDirectory },
      );
      requireObservation(
        explicit.helper.id === explicitRetry.helper.id && !explicitRetry.created,
        "helper idempotency key created a duplicate",
      );
      const cascade = await reserveAndCreateHelper(client, state, "cascade", {
        requestedCwd: reviewPath,
      });
      const recovered = await recoverTopology(client, state);
      requireObservation(recovered.capacity.byRole["task-agent"] === 2, "Task Agent capacity count changed");
      requireObservation(recovered.capacity.byRole.reviewer === 1, "Reviewer capacity count changed");
      requireObservation(recovered.capacity.byRole.helper === 2, "helper capacity count changed");
      requireObservation(recovered.capacity.globalConsumed === 5, "global capacity count changed");
      requireObservation(
        recovered.capacity.helperByRun[`${taskDefinitions.alpha.taskId}/${taskDefinitions.alpha.runId}`] === 2,
        "Task helper quota count changed",
      );
      console.log(
        JSON.stringify({
          workspaceAlpha,
          workspaceBeta,
          reviewWorkspace: workspaceSummary(reviewWorkspaceSnapshot),
          concurrent: { alpha: alpha.running, beta: beta.running },
          reviewer,
          helperPlacement: {
            explicitRequestedCwd: state.tasks.beta.workspaceDirectory,
            cascadeRequestedCwd: reviewPath,
            observedParentCwd: state.tasks.alpha.workspaceDirectory,
          },
          helpers: { explicit, explicitRetry, cascade },
          recovered,
        }),
      );
      return;
    }

    const state = await loadState();
    if (command === "topology-recover") {
      console.log(JSON.stringify(await recoverTopology(client, state)));
      return;
    }

    if (command === "topology-assert-orderly-restart") {
      console.log(JSON.stringify(await assertRestartProjection(client, state, "orderly")));
      return;
    }

    if (command === "topology-resume-orderly") {
      console.log(JSON.stringify(await resumeRestartRecords(client, state, "orderly")));
      return;
    }

    if (command === "topology-assert-abrupt-restart") {
      console.log(JSON.stringify(await assertRestartProjection(client, state, "abrupt")));
      return;
    }

    if (command === "topology-resume-abrupt") {
      console.log(JSON.stringify(await resumeRestartRecords(client, state, "abrupt")));
      return;
    }

    if (command === "topology-start-restart-pair") {
      const label = process.env.DIRECTOR_RESTART_LABEL;
      if (!label || !/^[a-z0-9-]+$/.test(label)) {
        throw new Error("DIRECTOR_RESTART_LABEL is required and must be safe");
      }
      console.log(JSON.stringify(await startRestartPair(client, state, label)));
      return;
    }

    if (command === "topology-workspace-cascade-reviewer") {
      console.log(JSON.stringify(await archiveReviewerWorkspaceCascade(client, state)));
      return;
    }

    if (command === "topology-explicit-helper-cleanup") {
      const helper = state.helpers.explicit;
      const parentBefore = await refreshRequired(client, state.tasks.alpha.currentAgentId);
      const result = await archiveIfActive(client, helper.agentId);
      const terminationMs = await waitForPidsGone(helper.currentProcess.trackedPids);
      const signalEvidence = await requireNoGracefulSignal([helper.currentMarkerPath]);
      const after = await refreshRequired(client, helper.agentId);
      const parentAfter = await refreshRequired(client, state.tasks.alpha.currentAgentId);
      requireObservation(after.status === "closed" && after.archivedAt, "explicit helper did not close");
      requireObservation(
        parentBefore.id === parentAfter.id && parentAfter.status === "running",
        "explicit helper cleanup interrupted its Task Agent",
      );
      helper.explicitlyArchivedAt = after.archivedAt;
      await saveState(state);
      console.log(
        JSON.stringify({
          result,
          terminationMs,
          signalEvidence,
          helperAfter: agentSummary(after),
          parentAfter: agentSummary(parentAfter),
          capacity: await capacityFacts(client, state),
        }),
      );
      return;
    }

    if (command === "topology-cascade-parent-cleanup") {
      const task = state.tasks.alpha;
      const helper = state.helpers.cascade;
      const parent = client.agents.ref(task.currentAgentId);
      const parentBefore = await refreshRequired(client, task.currentAgentId);
      const helperBefore = await refreshRequired(client, helper.agentId);
      requireObservation(
        parentBefore.status === "running" && helperBefore.status === "running",
        "cascade proof did not start with a live parent/helper pair",
      );
      const archiveResult = await parent.archive();
      const trackedPids = [
        ...new Set([...task.currentProcess.trackedPids, ...helper.currentProcess.trackedPids]),
      ];
      const terminationMs = await waitForPidsGone(trackedPids);
      const signalEvidence = await requireNoGracefulSignal([
        task.currentMarkerPath,
        helper.currentMarkerPath,
      ]);
      const parentAfter = await refreshRequired(client, task.currentAgentId);
      const helperAfter = await refreshRequired(client, helper.agentId);
      requireObservation(parentAfter.status === "closed" && parentAfter.archivedAt, "parent did not close");
      requireObservation(
        helperAfter.status === "closed" && helperAfter.archivedAt,
        "parent archive did not cascade to helper",
      );
      task.archivedAt = parentAfter.archivedAt;
      helper.cascadeArchivedAt = helperAfter.archivedAt;
      await saveState(state);
      console.log(
        JSON.stringify({
          parentBefore: agentSummary(parentBefore),
          helperBefore: agentSummary(helperBefore),
          archiveResult,
          terminationMs,
          signalEvidence,
          allTrackedPidsAbsent: trackedPids.every((pid) => !pidIsAlive(pid)),
          parentAfter: agentSummary(parentAfter),
          helperAfter: agentSummary(helperAfter),
          capacity: await capacityFacts(client, state),
        }),
      );
      return;
    }

    if (command === "topology-replace-beta") {
      const task = state.tasks.beta;
      const previous = await refreshRequired(client, task.currentAgentId);
      requireObservation(
        previous.status === "running" && !previous.archivedAt,
        "replacement guard expected one active prior Task Agent",
      );
      const previousHandle = client.agents.ref(previous.id);
      const previousArchive = await previousHandle.archive();
      const terminationMs = await waitForPidsGone(task.originalProcess.trackedPids);
      const signalEvidence = await requireNoGracefulSignal([task.agentIntent.markerPath]);
      const previousAfter = await refreshRequired(client, previous.id);
      requireObservation(
        previousAfter.status === "closed" && previousAfter.archivedAt,
        "prior Task Agent was still active before replacement",
      );

      const replacementLabels = {
        ...task.labels,
        "director.intent": "task-beta-replacement",
        "director.replacement": "1",
      };
      task.replacementIntent = {
        requestId: randomUUID(),
        clientMessageId: randomUUID(),
        recordedAt: new Date().toISOString(),
        priorArchivedAt: previousAfter.archivedAt,
      };
      await saveState(state);
      const replacementToken = `BETA_REPLACED_${randomUUID().replaceAll("-", "_")}`;
      const options = buildTopLevelOptions({
        title: task.title,
        labels: replacementLabels,
        prompt: `Reply with exactly ${replacementToken}.`,
        requestId: task.replacementIntent.requestId,
        clientMessageId: task.replacementIntent.clientMessageId,
      });
      const externalFactsReconciled = task.originalProcess.trackedPids.every(
        (pid) => !pidIsAlive(pid),
      );
      const replacement = await createReplacementAfterTermination(
        previousAfter,
        externalFactsReconciled,
        () => client.workspaces.ref(task.workspaceId).agents.create(options),
      );
      task.currentAgentId = replacement.id;
      task.currentLabels = replacementLabels;
      task.replacementObservedAt = new Date().toISOString();
      await saveState(state);
      const finish = await replacement.waitForFinish(120_000);
      const replacementSnapshot = await refreshRequired(client, replacement.id);
      requireObservation(replacementSnapshot.title === task.title, "replacement title changed");
      requireObservation(parentAgentId(replacementSnapshot) === null, "replacement acquired a parent");
      requireObservation(
        Date.parse(previousAfter.archivedAt) <= Date.parse(replacementSnapshot.createdAt),
        "replacement overlapped the prior Task Agent",
      );
      requireObservation(finish.lastMessage?.trim() === replacementToken, "replacement prompt result changed");
      const activeMatches = await listByLabels(
        client,
        {
          "director.project": projectId,
          "director.task": task.taskId,
          "director.run": task.runId,
          "director.role": "task-agent",
        },
        false,
      );
      requireObservation(activeMatches.length === 1, "replacement left overlapping active Task Agents");
      console.log(
        JSON.stringify({
          previous: agentSummary(previous),
          previousArchive,
          previousAfter: agentSummary(previousAfter),
          terminationMs,
          signalEvidence,
          replacement: agentSummary(replacementSnapshot),
          finish,
          sameExactTitle: replacementSnapshot.title === previous.title,
          nonOverlapping: true,
          activeTaskAgentsForRun: activeMatches.map(agentSummary),
          capacity: await capacityFacts(client, state),
        }),
      );
      return;
    }

    if (command === "topology-cleanup") {
      const cleanup = { agents: [], workspaces: [] };
      const owned = await listByLabels(client, { "director.project": projectId }, true);
      const ordered = owned.toSorted((left, right) => {
        const leftHelper = left.labels?.["director.role"] === "helper" ? 0 : 1;
        const rightHelper = right.labels?.["director.role"] === "helper" ? 0 : 1;
        return leftHelper - rightHelper;
      });
      for (const snapshot of ordered) {
        cleanup.agents.push({ id: snapshot.id, ...(await archiveIfActive(client, snapshot.id)) });
      }
      const workspaceIds = [
        ...new Set([
          state.reviewer.workspaceId,
          state.tasks.alpha.workspaceId,
          state.tasks.beta.workspaceId,
          ...owned.map((snapshot) => snapshot.workspaceId),
        ].filter(Boolean)),
      ];
      for (const workspaceId of workspaceIds) {
        const workspace = client.workspaces.ref(workspaceId);
        const first = await workspace.archive();
        const second = await workspace.archive();
        cleanup.workspaces.push({ workspaceId, first, second });
      }
      const activeAgents = await listByLabels(client, { "director.project": projectId }, false);
      const activeWorkspaces = await client.workspaces.list({ page: { limit: 100 } });
      const ownedActiveWorkspaces = activeWorkspaces.entries.filter((entry) =>
        workspaceIds.includes(entry.id),
      );
      requireObservation(activeAgents.length === 0, "owned active agents remained after cleanup");
      requireObservation(ownedActiveWorkspaces.length === 0, "owned active workspaces remained after cleanup");
      console.log(
        JSON.stringify({
          cleanup,
          activeAgentIds: activeAgents.map(({ id }) => id),
          ownedActiveWorkspaceIds: ownedActiveWorkspaces.map(({ id }) => id),
        }),
      );
      return;
    }

    throw new Error(`Unknown topology command: ${command}`);
  } finally {
    await client.close();
  }
}

await run();
