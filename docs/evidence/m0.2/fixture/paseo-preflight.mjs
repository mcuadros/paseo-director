export const requiredMethods = {
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

export function assertStableApi(client) {
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

export function runStructuralNegatives(harness) {
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
    harness,
    checkedSurfaces: results.length,
    failures: results.length,
    sideEffects: totalEffects,
    baselineRefCalls: baseline.counters.refs,
    results,
  };
}
