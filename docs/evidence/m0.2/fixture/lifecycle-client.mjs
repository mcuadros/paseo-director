import { readFile, writeFile } from "node:fs/promises";
import { createPaseoClient } from "@getpaseo/client";

const command = process.argv[2];
const url = process.env.DIRECTOR_PASEO_URL;
const password = process.env.DIRECTOR_PASEO_PASSWORD;
const statePath = process.env.DIRECTOR_LIFECYCLE_STATE;
const workspacePath = process.env.DIRECTOR_WORKSPACE_PATH;
const provider = process.env.DIRECTOR_TEST_PROVIDER;
const initialPrompt = process.env.DIRECTOR_INITIAL_PROMPT;

if (!command) throw new Error("A lifecycle command is required");
for (const [name, value] of Object.entries({ url, password, statePath })) {
  if (!value) throw new Error(`Missing required environment variable: ${name}`);
}

const labels = {
  "director.project": "lifecycle-probe",
  "director.task": "dir-m0.2",
  "director.run": "run-1",
};

function requireMethods(value, path, methods) {
  for (const method of methods) {
    if (typeof value?.[method] !== "function") {
      throw new Error(`Missing required public method: ${path}.${method}`);
    }
  }
}

function assertStableApi(api) {
  requireMethods(api.projects, "projects", ["list"]);
  requireMethods(api.workspaces, "workspaces", ["list", "ref", "open", "create", "archive", "subscribe"]);
  requireMethods(api.agents, "agents", ["list", "ref", "create", "subscribe"]);
  requireMethods(api.providers, "providers", [
    "listModels",
    "listModes",
    "listFeatures",
    "listAvailable",
    "snapshot",
    "waitForReady",
    "refresh",
    "diagnostic",
    "subscribe",
  ]);
  requireMethods(api.config, "config", ["get", "patch"]);
}

async function connect() {
  const client = createPaseoClient({
    url,
    password,
    reconnect: { enabled: false },
  });
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

async function run() {
  if (command === "negative-structural") {
    let sideEffects = 0;
    const fake = {
      projects: { list() {} },
      workspaces: { list() {}, ref() {}, open() {}, create() {}, archive() {}, subscribe() {} },
      agents: { list() {}, ref() {}, create() {}, subscribe() {} },
      providers: {
        listModels() {},
        listModes() {},
        listFeatures() {},
        listAvailable() {},
        snapshot() {},
        waitForReady() {},
        refresh() {},
        diagnostic() {},
        subscribe() {},
      },
      config: { get() {} },
    };
    let error = null;
    try {
      assertStableApi(fake);
      sideEffects += 1;
    } catch (caught) {
      error = caught instanceof Error ? caught.message : String(caught);
    }
    console.log(JSON.stringify({ error, sideEffects }));
    return;
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
      let agentEvents = 0;
      const removeAgentListener = client.agents.subscribe(() => {
        agentEvents += 1;
      });
      await client.agents.list({ filter: { labels }, page: { limit: 100 }, subscribe: {} });
      const agent = client.agents.ref(state.agentId);
      await agent.refresh();
      await agent.send("Run the shell command sleep 90 and wait for it to finish. Do nothing else.");
      const running = await waitForAgent(
        agent,
        (snapshot) => snapshot.status === "running" && snapshot.activeTurn !== null,
      );
      const { firstArchive, secondArchive } = await archiveTwice(agent);
      await new Promise((resolve) => setTimeout(resolve, 500));
      removeAgentListener();
      console.log(
        JSON.stringify({
          running: agentSummary(running),
          firstArchive,
          secondArchive,
          recovered: await recover(client, state),
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
