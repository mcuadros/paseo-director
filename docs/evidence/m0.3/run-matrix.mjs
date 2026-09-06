#!/usr/bin/env node

import { readFile, readdir, writeFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";
import path from "node:path";

const row = process.argv[2];
const clientEntry = requireEnv("DIRECTOR_PASEO_CLIENT_ENTRY");
const runtimeRoot = requireEnv("DIRECTOR_MATRIX_ROOT");
const probeMcpPath = requireEnv("DIRECTOR_PROBE_MCP");
const paseoUrl = requireEnv("DIRECTOR_PASEO_URL");
const fixtureModel = "fixture";
const allowedTool = "read_scope_nonce";

const { createPaseoClient } = await import(pathToFileURL(clientEntry).href);
const client = createPaseoClient({
  url: paseoUrl,
  clientId: `director-m0-3-${row ?? "unknown"}`,
  appVersion: "0.7.2",
  reconnect: { enabled: false },
  connectTimeoutMs: 10_000,
});

function requireEnv(name) {
  const value = process.env[name];
  if (!value) throw new Error(`Missing ${name}`);
  return value;
}

function rowPath(name, suffix) {
  return path.join(runtimeRoot, `${name}.${suffix}`);
}

async function readJsonLines(filePath) {
  try {
    const text = await readFile(filePath, "utf8");
    return text
      .split("\n")
      .filter(Boolean)
      .map((line) => JSON.parse(line));
  } catch (cause) {
    if (cause && typeof cause === "object" && cause.code === "ENOENT") return [];
    throw cause;
  }
}

async function resetFile(filePath) {
  await writeFile(filePath, "", { encoding: "utf8", mode: 0o600 });
}

async function fileManifest(root) {
  const entries = [];
  async function visit(directory, prefix = "") {
    for (const entry of await readdir(directory, { withFileTypes: true })) {
      const relative = path.posix.join(prefix, entry.name);
      if (entry.isDirectory()) await visit(path.join(directory, entry.name), relative);
      else entries.push(relative);
    }
  }
  await visit(root);
  return entries.sort();
}

function providerHomeFor(name) {
  return process.env.DIRECTOR_PROVIDER_HOME ?? path.join(runtimeRoot, "provider-homes", name);
}

function providerEnvFor(name) {
  const home = providerHomeFor(name);
  if (name === "codex") {
    return { HOME: home, CODEX_HOME: path.join(home, ".codex") };
  }
  if (name === "claude") {
    return { HOME: home, CLAUDE_CONFIG_DIR: path.join(home, ".claude") };
  }
  if (name === "opencode") {
    return {
      HOME: home,
      XDG_CONFIG_HOME: path.join(home, ".config"),
      XDG_DATA_HOME: path.join(home, ".local", "share"),
      XDG_CACHE_HOME: path.join(home, ".cache"),
    };
  }
  return { HOME: home };
}

async function assertIsolatedHome(name) {
  const manifest = await fileManifest(providerHomeFor(name));
  const allowed = {
    codex: [".codex/auth.json"],
    claude: [".claude.json", ".claude/.credentials.json"],
    opencode: [".local/share/opencode/auth.json"],
  }[name] ?? [];
  const unexpected = manifest.filter((entry) => !allowed.includes(entry));
  if (unexpected.length > 0) {
    throw new Error(`Provider home is not auth-only: ${JSON.stringify(unexpected)}`);
  }
  return manifest;
}

function summarizeTimeline(payload) {
  return payload.entries.map((entry) => {
    const item = entry.item;
    if (item.type === "tool_call") {
      return {
        type: item.type,
        name: item.name,
        status: item.status,
        detailType: item.detail?.type ?? null,
      };
    }
    if (item.type === "assistant_message" || item.type === "error") {
      return { type: item.type, text: item.text ?? item.message ?? "" };
    }
    return { type: item.type };
  });
}

function assertProbeTranscript(events, expectedRow, expectedText) {
  const receivedMethods = events
    .filter((event) => event.event === "received")
    .map((event) => event.method);
  for (const method of ["initialize", "tools/list", "tools/call"]) {
    if (!receivedMethods.includes(method)) {
      throw new Error(`${expectedRow}: probe did not observe ${method}`);
    }
  }
  const calls = events.filter((event) => event.event === "called");
  if (
    calls.length !== 1 ||
    calls[0].toolName !== allowedTool ||
    calls[0].resultText !== expectedText
  ) {
    throw new Error(`${expectedRow}: unexpected scoped call evidence: ${JSON.stringify(calls)}`);
  }
  const rejected = events.filter(
    (event) => event.event === "rejected_tool_call" || event.event === "rejected_method",
  );
  if (rejected.length > 0) {
    throw new Error(`${expectedRow}: probe rejected an out-of-scope call`);
  }
  return { receivedMethods, calls: calls.length, rejectedCalls: rejected.length };
}

async function chooseModel(provider, explicitModel) {
  const snapshot = await client.providers.waitForReady({
    cwd: path.join(runtimeRoot, "workspace"),
    timeoutMs: 120_000,
  });
  const entry = snapshot.entries.find((candidate) => candidate.provider === provider);
  if (!entry || entry.status !== "ready" || entry.enabled !== true) {
    throw new Error(`${provider}: provider is not ready: ${JSON.stringify(entry ?? null)}`);
  }
  const requested = explicitModel ?? process.env[`DIRECTOR_${provider.toUpperCase()}_MODEL`];
  if (requested) {
    if (!entry.models?.some((model) => model.id === requested)) {
      throw new Error(`${provider}: requested model is absent: ${requested}`);
    }
    return { model: requested, providerEntry: entry };
  }
  const selected = entry.models?.find((model) => model.isDefault) ?? entry.models?.[0];
  if (!selected) throw new Error(`${provider}: no model was discovered`);
  return { model: selected.id, providerEntry: entry };
}

function builtInOptions(provider) {
  if (provider === "codex") {
    return { approval_policy: "never", sandbox_mode: "read-only", web_search: "disabled" };
  }
  if (provider === "claude") {
    return {
      disallowedTools: [
        "Bash",
        "Read",
        "Write",
        "Edit",
        "Glob",
        "Grep",
        "WebSearch",
        "WebFetch",
        "Task",
        "NotebookEdit",
      ],
    };
  }
  if (provider === "opencode") {
    return { permission: "deny" };
  }
  throw new Error(`No built-in options for ${provider}`);
}

async function runBuiltIn(provider) {
  const { model, providerEntry } = await chooseModel(provider);
  const providerHomeManifest = await assertIsolatedHome(provider);
  const server = `director_scope_${provider}`;
  const nonce = `m03-${provider}-scope-v1`;
  const expectedText = `DIRECTOR_SCOPE_OK row=${provider} nonce=${nonce} tools=${allowedTool}`;
  const probeLog = rowPath(provider, "mcp.jsonl");
  await resetFile(probeLog);
  let agent;
  try {
    agent = await client.agents.create({
      config: {
        provider: `${provider}/${model}`,
        options: builtInOptions(provider),
        mcpServers: {
          [server]: {
            type: "stdio",
            command: process.execPath,
            args: [probeMcpPath],
            env: {
              DIRECTOR_PROBE_LOG: probeLog,
              DIRECTOR_PROBE_NONCE: nonce,
              DIRECTOR_PROBE_ROW: provider,
            },
          },
        },
        toolPolicy: {
          preapproved: [{ kind: "mcp", server, tool: allowedTool }],
        },
        systemPrompt:
          "This is a bounded MCP transport probe. Use only the named MCP tool and do not access files, shell, network, other tools, or credentials.",
      },
      cwd: path.join(runtimeRoot, "workspace"),
      env: providerEnvFor(provider),
      title: `Director M0.3 ${provider} probe`,
      labels: { task: "dir-m0.3", evidenceRow: provider },
    });
    if (agent.capabilities?.supportsMcpServers !== true) {
      throw new Error(`${provider}: live session does not report MCP support`);
    }
    const run = await agent.run(
      `Call only ${server}.${allowedTool}. Reply with exactly the returned text. If unavailable, reply UNAVAILABLE without guessing.`,
      { timeoutMs: 180_000 },
    );
    const timeline = await agent.timeline.refetch({ projection: "canonical", limit: 100 });
    const probeEvents = await readJsonLines(probeLog);
    const probe = assertProbeTranscript(probeEvents, provider, expectedText);
    if (run.status !== "idle" || run.error !== null || !run.lastMessage?.includes(expectedText)) {
      throw new Error(`${provider}: terminal result did not contain the observed nonce`);
    }
    return {
      row: provider,
      verdict: "pass",
      providerStatus: providerEntry.status,
      providerSource: providerEntry.source ?? null,
      model,
      providerHomeManifest,
      liveSupportsMcpServers: agent.capabilities.supportsMcpServers,
      requestedServers: [server],
      requestedGrant: `${server}.${allowedTool}`,
      result: { status: run.status, error: run.error, lastMessage: run.lastMessage },
      probe,
      timeline: summarizeTimeline(timeline),
    };
  } finally {
    if (agent) await agent.archive();
  }
}

async function runCompatibleAcp() {
  const provider = "probe-acp";
  const providerHomeManifest = await assertIsolatedHome(provider);
  const server = "director_scope_acp";
  const nonce = "m03-acp-scope-v1";
  const expectedText = `DIRECTOR_SCOPE_OK row=acp-compatible nonce=${nonce} tools=${allowedTool}`;
  const probeLog = rowPath("acp-compatible", "mcp.jsonl");
  const acpLog = rowPath("acp-compatible", "acp.jsonl");
  await Promise.all([resetFile(probeLog), resetFile(acpLog)]);
  let agent;
  try {
    agent = await client.agents.create({
      config: {
        provider: `${provider}/${fixtureModel}`,
        mcpServers: {
          [server]: {
            type: "stdio",
            command: process.execPath,
            args: [probeMcpPath],
            env: {
              DIRECTOR_PROBE_LOG: probeLog,
              DIRECTOR_PROBE_NONCE: nonce,
              DIRECTOR_PROBE_ROW: "acp-compatible",
            },
          },
        },
      },
      cwd: path.join(runtimeRoot, "workspace"),
      env: { HOME: providerHomeFor(provider), DIRECTOR_ACP_LOG: acpLog },
      title: "Director M0.3 compatible ACP probe",
      labels: { task: "dir-m0.3", evidenceRow: "acp-compatible" },
    });
    if (agent.capabilities?.supportsMcpServers !== true) {
      throw new Error("compatible ACP: live session does not report MCP support");
    }
    const run = await agent.run("Run the deterministic session MCP probe.", { timeoutMs: 30_000 });
    const timeline = await agent.timeline.refetch({ projection: "canonical", limit: 100 });
    const probeEvents = await readJsonLines(probeLog);
    const acpEvents = await readJsonLines(acpLog);
    const probe = assertProbeTranscript(probeEvents, "acp-compatible", expectedText);
    const created = acpEvents.filter((event) => event.event === "session_created");
    const completed = acpEvents.filter((event) => event.event === "mcp_call_completed");
    if (
      created.length !== 1 ||
      created[0].mcpServerCount !== 1 ||
      completed.length !== 1 ||
      completed[0].resultText !== expectedText
    ) {
      throw new Error("compatible ACP: session did not accept and call the exact MCP scope");
    }
    if (run.status !== "idle" || run.error !== null || run.lastMessage !== expectedText) {
      throw new Error("compatible ACP: terminal result did not match the observed nonce");
    }
    return {
      row: "acp-compatible-mcp-only",
      verdict: "pass",
      provider,
      model: fixtureModel,
      providerHomeManifest,
      liveSupportsMcpServers: agent.capabilities.supportsMcpServers,
      result: { status: run.status, error: run.error, lastMessage: run.lastMessage },
      probe,
      acp: {
        mcpServerCount: created[0].mcpServerCount,
        mcpServerNames: created[0].mcpServerNames,
        promptCount: acpEvents.filter((event) => event.event === "prompt_received").length,
        completedCalls: completed.length,
      },
      timeline: summarizeTimeline(timeline),
    };
  } finally {
    if (agent) await agent.archive();
  }
}

async function runOpenCodeBillingRejection() {
  const provider = "opencode";
  const model = "opencode/gpt-5-nano";
  const { providerEntry } = await chooseModel(provider, model);
  const providerHomeManifest = await assertIsolatedHome(provider);
  const server = "director_scope_opencode_billing";
  const probeLog = rowPath("opencode-billing-rejected", "mcp.jsonl");
  await resetFile(probeLog);
  let agent;
  try {
    agent = await client.agents.create({
      config: {
        provider: `${provider}/${model}`,
        options: builtInOptions(provider),
        mcpServers: {
          [server]: {
            type: "stdio",
            command: process.execPath,
            args: [probeMcpPath],
            env: {
              DIRECTOR_PROBE_LOG: probeLog,
              DIRECTOR_PROBE_NONCE: "must-not-be-guessed",
              DIRECTOR_PROBE_ROW: "opencode-billing-rejected",
            },
          },
        },
        toolPolicy: { preapproved: [{ kind: "mcp", server, tool: allowedTool }] },
      },
      cwd: path.join(runtimeRoot, "workspace"),
      env: providerEnvFor(provider),
      title: "Director M0.3 explicit OpenCode billing rejection",
      labels: { task: "dir-m0.3", evidenceRow: "opencode-billing-rejected" },
    });
    const run = await agent.run(`Call only ${server}.${allowedTool}.`, { timeoutMs: 60_000 });
    const probeEvents = await readJsonLines(probeLog);
    const receivedMethods = probeEvents
      .filter((event) => event.event === "received")
      .map((event) => event.method);
    const observedError = run.error ?? "";
    if (
      run.status !== "error" ||
      !observedError.includes("No payment method") ||
      probeEvents.some((event) => event.event === "called")
    ) {
      throw new Error("The explicit billing-failure row did not fail in the expected scope");
    }
    return {
      row: "opencode-explicit-gpt-5-nano",
      verdict: "failed-no-fallback",
      providerStatus: providerEntry.status,
      providerSource: providerEntry.source ?? null,
      model,
      providerHomeManifest,
      liveSupportsMcpServers: agent.capabilities?.supportsMcpServers ?? null,
      result: {
        status: run.status,
        error: "No payment method; billing URL redacted",
        lastMessage: run.lastMessage ? "[redacted provider error]" : null,
      },
      probe: {
        receivedMethods,
        calls: 0,
      },
      fallbackCount: 0,
    };
  } finally {
    if (agent) await agent.archive();
  }
}

async function runAcpPolicyRejection() {
  const provider = "probe-acp";
  const providerHomeManifest = await assertIsolatedHome(provider);
  const server = "director_scope_acp_policy";
  const probeLog = rowPath("acp-policy-rejected", "mcp.jsonl");
  const acpLog = rowPath("acp-policy-rejected", "acp.jsonl");
  await Promise.all([resetFile(probeLog), resetFile(acpLog)]);
  let observedError = null;
  try {
    await client.agents.create({
      config: {
        provider: `${provider}/${fixtureModel}`,
        mcpServers: {
          [server]: {
            type: "stdio",
            command: process.execPath,
            args: [probeMcpPath],
            env: {
              DIRECTOR_PROBE_LOG: probeLog,
              DIRECTOR_PROBE_NONCE: "must-not-be-called",
              DIRECTOR_PROBE_ROW: "acp-policy-rejected",
            },
          },
        },
        toolPolicy: { preapproved: [{ kind: "mcp", server, tool: allowedTool }] },
      },
      cwd: path.join(runtimeRoot, "workspace"),
      env: { HOME: providerHomeFor(provider), DIRECTOR_ACP_LOG: acpLog },
      title: "Director M0.3 ACP policy rejection",
      labels: { task: "dir-m0.3", evidenceRow: "acp-policy-rejected" },
    });
  } catch (cause) {
    observedError = cause instanceof Error ? cause.message : String(cause);
  }
  const [probeEvents, acpEvents] = await Promise.all([
    readJsonLines(probeLog),
    readJsonLines(acpLog),
  ]);
  if (
    !observedError?.includes(
      `Provider '${provider}' cannot preapprove exact MCP tools for unattended execution`,
    )
  ) {
    throw new Error(`Unexpected ACP tool-policy rejection: ${observedError}`);
  }
  if (probeEvents.length !== 0 || acpEvents.length !== 0) {
    throw new Error("ACP tool-policy rejection was not pre-launch");
  }
  const registeredAgentCount = await countAgentsForRow("acp-policy-rejected");
  if (registeredAgentCount !== 0) {
    throw new Error("ACP tool-policy rejection registered an agent");
  }
  return {
    row: "acp-compatible-with-exact-tool-policy",
    verdict: "fail-closed-excluded",
    observedError,
    providerHomeManifest,
    acpProcessEvents: acpEvents.length,
    mcpProcessEvents: probeEvents.length,
    registeredAgentCount,
    promptCount: 0,
    fallbackCount: 0,
  };
}

async function runUnsupportedAcp() {
  const provider = "probe-acp-no-mcp";
  const providerHomeManifest = await assertIsolatedHome(provider);
  const server = "director_scope_unsupported";
  const probeLog = rowPath("acp-unsupported", "mcp.jsonl");
  const acpLog = rowPath("acp-unsupported", "acp.jsonl");
  await Promise.all([resetFile(probeLog), resetFile(acpLog)]);
  let observedError = null;
  try {
    await client.agents.create({
      config: {
        provider: `${provider}/${fixtureModel}`,
        mcpServers: {
          [server]: {
            type: "stdio",
            command: process.execPath,
            args: [probeMcpPath],
            env: {
              DIRECTOR_PROBE_LOG: probeLog,
              DIRECTOR_PROBE_NONCE: "must-not-be-called",
              DIRECTOR_PROBE_ROW: "acp-unsupported",
            },
          },
        },
      },
      cwd: path.join(runtimeRoot, "workspace"),
      env: { HOME: providerHomeFor(provider), DIRECTOR_ACP_LOG: acpLog },
      title: "Director M0.3 unsupported-MCP ACP rejection",
      labels: { task: "dir-m0.3", evidenceRow: "acp-unsupported" },
    });
  } catch (cause) {
    observedError = cause instanceof Error ? cause.message : String(cause);
  }
  const [probeEvents, acpEvents] = await Promise.all([
    readJsonLines(probeLog),
    readJsonLines(acpLog),
  ]);
  const created = acpEvents.filter((event) => event.event === "session_created");
  const prompts = acpEvents.filter((event) => event.event === "prompt_received");
  if (!observedError?.includes(`Provider '${provider}' does not support MCP servers`)) {
    throw new Error(`Unexpected unsupported-MCP rejection: ${observedError}`);
  }
  if (
    created.length !== 1 ||
    created[0].mcpServerCount !== 0 ||
    prompts.length !== 0 ||
    probeEvents.length !== 0
  ) {
    throw new Error("Unsupported ACP did not fail closed at the observed boundary");
  }
  const registeredAgentCount = await countAgentsForRow("acp-unsupported");
  if (registeredAgentCount !== 0) {
    throw new Error("Unsupported ACP rejection registered an agent");
  }
  return {
    row: "acp-declared-unsupported-mcp",
    verdict: "fail-closed-excluded",
    observedError,
    providerHomeManifest,
    acp: {
      processStarted: acpEvents.some((event) => event.event === "process_started"),
      sessionCreated: true,
      receivedMcpServerCount: created[0].mcpServerCount,
      promptCount: prompts.length,
    },
    mcpProcessEvents: probeEvents.length,
    registeredAgentCount,
    fallbackCount: 0,
  };
}

async function discover() {
  const [snapshot, daemonConfig] = await Promise.all([
    client.providers.waitForReady({
      cwd: path.join(runtimeRoot, "workspace"),
      timeoutMs: 120_000,
    }),
    client.config.get(),
  ]);
  const testedModels = {
    codex: ["gpt-5.4-mini"],
    claude: ["claude-haiku-4-5"],
    opencode: ["opencode/gpt-5-nano", "opencode/nemotron-3-ultra-free"],
    "probe-acp": [fixtureModel],
    "probe-acp-no-mcp": [fixtureModel],
  };
  return {
    row: "discovery",
    daemonMcp: daemonConfig.config.mcp,
    entries: snapshot.entries
      .filter((entry) =>
        ["codex", "claude", "opencode", "probe-acp", "probe-acp-no-mcp"].includes(
          entry.provider,
        ),
      )
      .map((entry) => ({
        provider: entry.provider,
        status: entry.status,
        enabled: entry.enabled,
        source: entry.source ?? null,
        error: entry.error ?? null,
        modelCount: entry.models?.length ?? 0,
        defaultModels: entry.models?.filter((model) => model.isDefault).map((model) => model.id) ?? [],
        testedModels: testedModels[entry.provider],
        testedModelsPresent: testedModels[entry.provider].every((testedModel) =>
          entry.models?.some((model) => model.id === testedModel),
        ),
      })),
  };
}

async function countAgentsForRow(evidenceRow) {
  const result = await client.agents.list({
    filter: { labels: { task: "dir-m0.3", evidenceRow }, includeArchived: true },
    page: { limit: 20 },
  });
  return result.entries.length;
}

async function verifyAgentCleanup() {
  const activeBefore = await client.agents.list({
    filter: { labels: { task: "dir-m0.3" } },
    page: { limit: 200 },
  });
  for (const entry of activeBefore.entries) {
    await client.agents.ref(entry.agent).archive();
  }
  const [activeAfter, history] = await Promise.all([
    client.agents.list({
      filter: { labels: { task: "dir-m0.3" } },
      page: { limit: 200 },
    }),
    client.agents.list({
      filter: { labels: { task: "dir-m0.3" }, includeArchived: true },
      page: { limit: 200 },
    }),
  ]);
  if (activeAfter.entries.length !== 0) {
    throw new Error(`Cleanup left ${activeAfter.entries.length} active evidence agents`);
  }
  const unarchived = history.entries.filter((entry) => !entry.agent.archivedAt);
  if (unarchived.length !== 0) {
    throw new Error(`Cleanup left ${unarchived.length} unarchived evidence agents`);
  }
  return {
    row: "cleanup",
    activeAgentCountBeforeCleanup: activeBefore.entries.length,
    archivedByCleanup: activeBefore.entries.length,
    activeAgentCount: activeAfter.entries.length,
    archivedAgentCount: history.entries.length,
    archivedProviders: history.entries.map((entry) => entry.agent.provider).sort(),
    allHistoricalRowsArchived: true,
  };
}

const actions = {
  discovery: discover,
  codex: () => runBuiltIn("codex"),
  claude: () => runBuiltIn("claude"),
  opencode: () => runBuiltIn("opencode"),
  "opencode-billing-rejected": runOpenCodeBillingRejection,
  "acp-compatible": runCompatibleAcp,
  "acp-policy-rejected": runAcpPolicyRejection,
  "acp-unsupported": runUnsupportedAcp,
  cleanup: verifyAgentCleanup,
};

if (!row || !actions[row]) {
  throw new Error(`Usage: run-matrix.mjs <${Object.keys(actions).join("|")}>`);
}

await client.connect();
try {
  const result = await actions[row]();
  process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
} finally {
  await client.close();
}
