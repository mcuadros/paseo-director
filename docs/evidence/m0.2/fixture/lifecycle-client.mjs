import { randomUUID } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { createPaseoClient } from "@getpaseo/client";

const command = process.argv[2];
const url = process.env.DIRECTOR_PASEO_URL;
const password = process.env.DIRECTOR_PASEO_PASSWORD;
const statePath = process.env.DIRECTOR_LIFECYCLE_STATE;
const workspacePath = process.env.DIRECTOR_WORKSPACE_PATH;
const provider = process.env.DIRECTOR_TEST_PROVIDER;
const initialPrompt = process.env.DIRECTOR_INITIAL_PROMPT;
const lifecycleRoot = process.env.DIRECTOR_LIFECYCLE_ROOT;
const daemonPid = Number(process.env.DIRECTOR_DAEMON_PID);
const archiveChildPath = fileURLToPath(new URL("./archive-child.mjs", import.meta.url));

if (!command) throw new Error("A lifecycle command is required");

const labels = {
  "director.project": "lifecycle-probe",
  "director.task": "dir-m0.2",
  "director.run": "run-1",
};

const requiredMethods = {
  client: ["connect", "close", "ensureConnected", "getConnectionState"],
  projects: ["list"],
  workspaces: ["list", "ref", "open", "create", "archive", "subscribe"],
  agents: ["list", "ref", "create", "subscribe"],
  providers: [
    "listModels",
    "listModes",
    "listFeatures",
    "listAvailable",
    "snapshot",
    "waitForReady",
    "refresh",
    "diagnostic",
    "subscribe",
  ],
  config: ["get", "patch"],
  workspaceHandle: ["current", "refresh", "setTitle", "archive", "subscribe"],
  workspaceAgents: ["create"],
  agentHandle: [
    "current",
    "refresh",
    "send",
    "run",
    "waitForFinish",
    "commands",
    "archive",
    "detach",
    "subscribe",
  ],
  agentTimeline: ["refetch", "subscribe"],
};

function requireMethods(value, path, methods) {
  for (const method of methods) {
    if (typeof value?.[method] !== "function") {
      throw new Error(`Missing required public method: ${path}.${method}`);
    }
  }
}

function assertStableApi(client) {
  for (const path of ["client", "projects", "workspaces", "agents", "providers", "config"]) {
    requireMethods(path === "client" ? client : client[path], path, requiredMethods[path]);
  }

  const workspaceHandle = client.workspaces.ref("wks_director_structural_preflight");
  const agentHandle = client.agents.ref("00000000-0000-4000-8000-000000000000");
  const targets = {
    workspaceHandle,
    workspaceAgents: workspaceHandle?.agents,
    agentHandle,
    agentTimeline: agentHandle?.timeline,
  };
  for (const path of Object.keys(targets)) {
    requireMethods(targets[path], path, requiredMethods[path]);
  }

  return targets;
}

async function connect() {
  const client = createPaseoClient({
    url,
    password,
    reconnect: { enabled: false },
  });
  assertStableApi(client);
  await client.connect();
  assertStableApi(client);
  return client;
}

async function loadState() {
  return JSON.parse(await readFile(statePath, "utf8"));
}

async function saveState(state) {
  await writeFile(statePath, `${JSON.stringify(state, null, 2)}\n`, "utf8");
}

function agentSummary(snapshot) {
  if (!snapshot) return null;
  return {
    id: snapshot.id,
    workspaceId: snapshot.workspaceId,
    provider: snapshot.provider,
    status: snapshot.status,
    activeTurn: snapshot.activeTurn,
    labels: snapshot.labels,
    archivedAt: snapshot.archivedAt ?? null,
    runtimeInfo: snapshot.runtimeInfo ?? null,
  };
}

function workspaceSummary(snapshot) {
  if (!snapshot) return null;
  return {
    id: snapshot.id,
    projectId: snapshot.projectId,
    directory: snapshot.directory,
    name: snapshot.name,
    status: snapshot.status,
    archivedAt: snapshot.archivedAt ?? null,
  };
}

async function waitForAgent(agent, predicate, timeoutMs = 60_000) {
  const deadline = Date.now() + timeoutMs;
  let snapshot = null;
  while (Date.now() < deadline) {
    snapshot = (await agent.refresh())?.agent ?? null;
    if (snapshot && predicate(snapshot)) return snapshot;
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  throw new Error(`Timed out waiting for agent state: ${JSON.stringify(agentSummary(snapshot))}`);
}

function requireObservation(condition, message) {
  if (!condition) throw new Error(`Unexpected lifecycle observation: ${message}`);
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

async function waitForJsonFile(path, timeoutMs = 60_000) {
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
  throw new Error(`Timed out waiting for durable child marker: ${lastError}`);
}

async function readProcessIdentity(pid) {
  const [stat, commandLineBuffer] = await Promise.all([
    readFile(`/proc/${pid}/stat`, "utf8"),
    readFile(`/proc/${pid}/cmdline`),
  ]);
  const closeParen = stat.lastIndexOf(")");
  requireObservation(closeParen > 0, `could not parse /proc/${pid}/stat`);
  const statFields = stat.slice(closeParen + 1).trim().split(/\s+/);
  const commandLine = commandLineBuffer.toString("utf8").split("\0").filter(Boolean).join(" ");
  return {
    pid,
    ppid: Number(statFields[1]),
    commandLine,
  };
}

async function collectOwnedProcessChain(childPid, token, expectedDaemonPid) {
  requireObservation(
    Number.isSafeInteger(expectedDaemonPid) && expectedDaemonPid > 1 && pidIsAlive(expectedDaemonPid),
    "exact isolated daemon PID was not live",
  );
  const daemonIdentity = await readProcessIdentity(expectedDaemonPid);
  requireObservation(
    daemonIdentity.commandLine.includes("Paseo Daemon"),
    "process ancestry boundary was not the isolated Paseo daemon",
  );
  const chain = [];
  let pid = childPid;
  let reachedDaemon = false;
  for (let depth = 0; depth < 24 && pid > 1 && pid !== expectedDaemonPid; depth += 1) {
    const identity = await readProcessIdentity(pid);
    chain.push(identity);
    if (identity.ppid === expectedDaemonPid) {
      reachedDaemon = true;
      break;
    }
    pid = identity.ppid;
  }
  requireObservation(
    chain.length > 0 && chain[0].pid === childPid && reachedDaemon,
    "child ancestry did not terminate at the exact isolated daemon PID",
  );
  requireObservation(
    chain[0].commandLine.includes(archiveChildPath) && chain[0].commandLine.includes(token),
    "durable marker PID did not identify the exact archive child",
  );
  const providerPids = chain
    .filter(({ pid: candidatePid, commandLine }) =>
      candidatePid !== childPid && /(^|[ /])codex(?:[- ]|$)/i.test(commandLine),
    )
    .map(({ pid: candidatePid }) => candidatePid);
  requireObservation(providerPids.length > 0, "no Codex provider process was found in owned ancestry");
  return { chain, providerPids };
}

async function waitForPidsGone(pids, timeoutMs = 15_000) {
  const startedAt = Date.now();
  while (Date.now() - startedAt < timeoutMs) {
    if (pids.every((pid) => !pidIsAlive(pid))) {
      return Date.now() - startedAt;
    }
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  const survivors = pids.filter(pidIsAlive);
  throw new Error(`Owned child/provider processes survived archive: ${survivors.join(",")}`);
}

async function listOwned(client, ownedLabels, includeArchived = true) {
  const agents = await client.agents.list({
    filter: { labels: ownedLabels, includeArchived },
    page: { limit: 100 },
  });
  return agents.entries.map(({ agent }) => agentSummary(agent));
}

async function recover(client, state) {
  const workspace = client.workspaces.ref(state.workspaceId);
  const agent = client.agents.ref(state.agentId);
  const [workspaceSnapshot, agentResult, ownedAgents, listedWorkspaces] = await Promise.all([
    workspace.refresh(),
    agent.refresh(),
    listOwned(client, state.labels),
    client.workspaces.list({
      filter: { idPrefix: state.workspaceId },
      page: { limit: 100 },
    }),
  ]);
  return {
    workspace: workspaceSummary(workspaceSnapshot),
    agent: agentSummary(agentResult?.agent ?? null),
    ownedAgents,
    listedWorkspaceIds: listedWorkspaces.entries.map(({ id }) => id),
  };
}

async function archiveTwice(agent) {
  const firstArchive = await agent.archive();
  let secondArchive;
  try {
    secondArchive = { ok: true, result: await agent.archive() };
  } catch (error) {
    secondArchive = { ok: false, error: error instanceof Error ? error.message : String(error) };
  }
  return { firstArchive, secondArchive };
}

function createFakeClient() {
  const counters = { effects: 0, refs: 0 };
  const method = () => {
    counters.effects += 1;
  };
  const target = (path) =>
    Object.fromEntries(requiredMethods[path].map((name) => [name, method]));
  const workspaceHandle = {
    ...target("workspaceHandle"),
    agents: target("workspaceAgents"),
  };
  const agentHandle = {
    ...target("agentHandle"),
    timeline: target("agentTimeline"),
  };
  const client = {
    ...target("client"),
    projects: target("projects"),
    workspaces: {
      ...target("workspaces"),
      ref() {
        counters.refs += 1;
        return workspaceHandle;
      },
    },
    agents: {
      ...target("agents"),
      ref() {
        counters.refs += 1;
        return agentHandle;
      },
    },
    providers: target("providers"),
    config: target("config"),
  };
  return {
    client,
    counters,
    targets: {
      client,
      projects: client.projects,
      workspaces: client.workspaces,
      agents: client.agents,
      providers: client.providers,
      config: client.config,
      workspaceHandle,
      workspaceAgents: workspaceHandle.agents,
      agentHandle,
      agentTimeline: agentHandle.timeline,
    },
  };
}

function runStructuralNegatives() {
  const baseline = createFakeClient();
  assertStableApi(baseline.client);
  if (baseline.counters.effects !== 0) {
    throw new Error("Structural preflight invoked a side effect in the complete baseline");
  }

  const results = [];
  let totalEffects = baseline.counters.effects;
  for (const [path, methods] of Object.entries(requiredMethods)) {
    for (const method of methods) {
      const fixture = createFakeClient();
      delete fixture.targets[path][method];
      let error = null;
      try {
        assertStableApi(fixture.client);
      } catch (caught) {
        error = caught instanceof Error ? caught.message : String(caught);
      }
      if (error !== `Missing required public method: ${path}.${method}`) {
        throw new Error(`Structural negative did not fail precisely for ${path}.${method}: ${error}`);
      }
      if (fixture.counters.effects !== 0) {
        throw new Error(`Structural negative invoked a side effect for ${path}.${method}`);
      }
      totalEffects += fixture.counters.effects;
      results.push({ surface: `${path}.${method}`, error, sideEffects: fixture.counters.effects });
    }
  }

  return {
    checkedSurfaces: results.length,
    failures: results.length,
    sideEffects: totalEffects,
    baselineRefCalls: baseline.counters.refs,
    results,
  };
}

async function run() {
  if (command === "negative-structural") {
    console.log(JSON.stringify(runStructuralNegatives()));
    return;
  }

  for (const [name, value] of Object.entries({ url, password, statePath })) {
    if (!value) throw new Error(`Missing required environment variable: ${name}`);
  }

  const client = await connect();
  try {
    if (command === "enable-plugins") {
      const result = await client.config.patch({ pluginsEnabled: true });
      console.log(JSON.stringify({ pluginsEnabled: result.config.pluginsEnabled }));
      return;
    }

    if (command === "create") {
      if (!workspacePath || !provider) throw new Error("Workspace path and provider are required");
      let workspaceEvents = 0;
      let agentEvents = 0;
      const removeWorkspaceListener = client.workspaces.subscribe(() => {
        workspaceEvents += 1;
      });
      const removeAgentListener = client.agents.subscribe(() => {
        agentEvents += 1;
      });
      await client.workspaces.list({ page: { limit: 100 }, subscribe: {} });
      await client.agents.list({ filter: { labels }, page: { limit: 100 }, subscribe: {} });

      const workspace = await client.workspaces.create({
        title: "Director lifecycle probe",
        source: { kind: "directory", path: workspacePath },
      });
      const workspaceSnapshot = await workspace.refresh();
      const agent = await workspace.agents.create({
        config: { provider, modeId: "full-access" },
        title: "Director lifecycle probe agent",
        labels,
      });
      const agentSnapshot = await waitForAgent(agent, (snapshot) => snapshot.status !== "initializing");
      await new Promise((resolve) => setTimeout(resolve, 500));
      removeWorkspaceListener();
      removeAgentListener();

      const state = {
        workspaceId: workspace.id,
        agentId: agent.id,
        labels,
        provider,
      };
      await saveState(state);
      console.log(
        JSON.stringify({
          state,
          workspace: workspaceSummary(workspaceSnapshot),
          agent: agentSummary(agentSnapshot),
          workspaceEvents,
          agentEvents,
          ownedAgents: await listOwned(client, labels),
        }),
      );
      return;
    }

    if (command === "create-active") {
      if (!workspacePath || !provider || !initialPrompt) {
        throw new Error("Workspace path, provider, and initial prompt are required");
      }
      let workspaceEvents = 0;
      let agentEvents = 0;
      const activeLabels = { ...labels, "director.run": "run-3" };
      const removeWorkspaceListener = client.workspaces.subscribe(() => {
        workspaceEvents += 1;
      });
      const removeAgentListener = client.agents.subscribe(() => {
        agentEvents += 1;
      });
      await client.workspaces.list({ page: { limit: 100 }, subscribe: {} });
      await client.agents.list({ filter: { labels: activeLabels }, page: { limit: 100 }, subscribe: {} });
      const workspace = await client.workspaces.create({
        title: "Director active-restart probe",
        source: { kind: "directory", path: workspacePath },
      });
      const agent = await workspace.agents.create({
        config: { provider, modeId: "full-access" },
        title: "Director active-restart agent",
        labels: activeLabels,
        prompt: initialPrompt,
      });
      const running = await waitForAgent(
        agent,
        (snapshot) => snapshot.status === "running" && snapshot.activeTurn !== null,
      );
      const activeState = {
        workspaceId: workspace.id,
        agentId: agent.id,
        labels: activeLabels,
        provider,
      };
      await saveState(activeState);
      removeWorkspaceListener();
      removeAgentListener();
      console.log(
        JSON.stringify({ activeState, running: agentSummary(running), workspaceEvents, agentEvents }),
      );
      return;
    }

    const state = await loadState();
    if (command === "recover") {
      console.log(JSON.stringify(await recover(client, state)));
      return;
    }

    if (command === "replace-started") {
      if (!initialPrompt) throw new Error("DIRECTOR_INITIAL_PROMPT is required");
      const previous = client.agents.ref(state.agentId);
      const previousArchive = await archiveTwice(previous);
      const replacementLabels = { ...state.labels, "director.run": "run-2" };
      const workspace = client.workspaces.ref(state.workspaceId);
      await workspace.refresh();
      const replacement = await workspace.agents.create({
        config: { provider: state.provider, modeId: "full-access" },
        title: "Director lifecycle replacement agent",
        labels: replacementLabels,
        prompt: initialPrompt,
      });
      const finish = await replacement.waitForFinish(120_000);
      const replacementSnapshot = (await replacement.refresh())?.agent ?? null;
      const replacementState = {
        ...state,
        agentId: replacement.id,
        labels: replacementLabels,
      };
      await saveState(replacementState);
      console.log(
        JSON.stringify({
          previousArchive,
          replacementState,
          finish,
          replacement: agentSummary(replacementSnapshot),
          recovered: await recover(client, replacementState),
        }),
      );
      return;
    }

    if (command === "resume") {
      if (!initialPrompt) throw new Error("DIRECTOR_INITIAL_PROMPT is required");
      const agent = client.agents.ref(state.agentId);
      const before = (await agent.refresh())?.agent ?? null;
      await agent.send(initialPrompt);
      const finish = await agent.waitForFinish(120_000);
      console.log(
        JSON.stringify({ before: agentSummary(before), finish, recovered: await recover(client, state) }),
      );
      return;
    }

    if (command === "archive-current") {
      const agent = client.agents.ref(state.agentId);
      const archived = await archiveTwice(agent);
      console.log(JSON.stringify({ archived, recovered: await recover(client, state) }));
      return;
    }

    if (command === "archive-active") {
      if (!lifecycleRoot || !Number.isSafeInteger(daemonPid)) {
        throw new Error("Lifecycle root and exact daemon PID are required");
      }
      const archiveToken = randomUUID();
      const markerPath = `${lifecycleRoot}/archive-${archiveToken}.started.json`;
      const terminationPath = `${lifecycleRoot}/archive-${archiveToken}.terminated.json`;
      const commandArguments = [process.execPath, archiveChildPath, markerPath, archiveToken];
      requireObservation(
        commandArguments.every((value) => /^[A-Za-z0-9_./-]+$/.test(value)),
        "archive child command contained an unsafe fixture argument",
      );
      const exactCommand = commandArguments.map((value) => `'${value}'`).join(" ");
      let agentEvents = 0;
      const removeAgentListener = client.agents.subscribe(() => {
        agentEvents += 1;
      });
      await client.agents.list({ filter: { labels: state.labels }, page: { limit: 100 }, subscribe: {} });
      const agent = client.agents.ref(state.agentId);
      await agent.refresh();
      await agent.send(`Run this exact command and wait for it to finish: ${exactCommand}`);
      const [running, marker] = await Promise.all([
        waitForAgent(
          agent,
          (snapshot) => snapshot.status === "running" && snapshot.activeTurn !== null,
        ),
        waitForJsonFile(markerPath),
      ]);
      requireObservation(
        marker.token === archiveToken &&
          Number.isSafeInteger(marker.pid) &&
          marker.pid > 1 &&
          pidIsAlive(marker.pid),
        "durable child marker did not identify a live owned process",
      );
      const { chain, providerPids } = await collectOwnedProcessChain(
        marker.pid,
        archiveToken,
        daemonPid,
      );
      requireObservation(marker.ppid === chain[0].ppid, "marker parent PID changed before archive");
      const trackedPids = [...new Set(chain.map(({ pid }) => pid))];
      const archiveStartedAt = Date.now();
      const firstArchive = await agent.archive();
      const archiveResponseMs = Date.now() - archiveStartedAt;
      const postResponseTerminationMs = await waitForPidsGone(trackedPids);
      const totalTerminationMs = Date.now() - archiveStartedAt;
      let secondArchive;
      try {
        secondArchive = { ok: true, result: await agent.archive() };
      } catch (error) {
        secondArchive = { ok: false, error: error instanceof Error ? error.message : String(error) };
      }
      const recovered = await recover(client, state);
      requireObservation(
        recovered.agent?.status === "closed" && recovered.agent.archivedAt === firstArchive.archivedAt,
        "archived agent did not reconcile to the first durable outcome",
      );
      const workspaceAfterArchive = await client.workspaces.ref(state.workspaceId).refresh();
      requireObservation(workspaceAfterArchive !== null, "active archive removed its workspace");
      await new Promise((resolve) => setTimeout(resolve, 2_000));
      const stillAbsent = trackedPids.every((pid) => !pidIsAlive(pid));
      requireObservation(stillAbsent, "owned child/provider process reappeared after archive");
      requireObservation(pidIsAlive(daemonPid), "isolated daemon exited during agent archive");
      let terminationMarker = null;
      try {
        terminationMarker = JSON.parse(await readFile(terminationPath, "utf8"));
      } catch (error) {
        if (error?.code !== "ENOENT") throw error;
      }
      removeAgentListener();
      console.log(
        JSON.stringify({
          running: agentSummary(running),
          processEvidence: {
            daemonPid,
            childPid: marker.pid,
            childPpid: marker.ppid,
            providerPids,
            tracked: chain.map(({ pid, ppid, commandLine }) => ({
              pid,
              ppid,
              role:
                pid === marker.pid
                  ? "long-running-child"
                  : /(^|[ /])codex(?:[- ]|$)/i.test(commandLine)
                    ? "provider"
                    : "provider-wrapper",
            })),
            aliveBeforeArchive: true,
            archiveResponseMs,
            postResponseTerminationMs,
            totalTerminationMs,
            terminationSignal: terminationMarker?.signal ?? null,
            absentAfterArchive: true,
            stillAbsentAfterDelay: stillAbsent,
          },
          firstArchive,
          secondArchive,
          recovered,
          workspaceAfterArchive: workspaceSummary(workspaceAfterArchive),
          agentEvents,
        }),
      );
      return;
    }

    if (command === "archive-workspace") {
      const workspace = client.workspaces.ref(state.workspaceId);
      const firstArchive = await workspace.archive();
      let secondArchive;
      try {
        secondArchive = { ok: true, result: await workspace.archive() };
      } catch (error) {
        secondArchive = { ok: false, error: error instanceof Error ? error.message : String(error) };
      }
      console.log(JSON.stringify({ firstArchive, secondArchive, recovered: await recover(client, state) }));
      return;
    }

    throw new Error(`Unknown lifecycle command: ${command}`);
  } finally {
    await client.close();
  }
}

await run();
