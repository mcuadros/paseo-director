#!/usr/bin/env node

import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import {
  existsSync,
  mkdtempSync,
  readFileSync,
  renameSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";
import { createServer, createConnection } from "node:net";
import { fileURLToPath } from "node:url";

const scriptPath = fileURLToPath(import.meta.url);
const CONTRACT_VERSION = "director-agent-runtime/v1";
const REQUIRED_CAPABILITIES = [
  "agent.observe",
  "agent.archive",
  "executionWorkspace.archive",
  "executionWorkspace.createManaged",
  "executionWorkspace.observe",
  "helperAgent.observe",
  "reviewerAgent.createWithInitialPrompt",
  "taskAgent.createWithInitialPrompt",
];
const CONTRACT_DOCUMENT = JSON.stringify({
  version: CONTRACT_VERSION,
  framing: "uint32be-json",
  requestIdentity: "requestId",
  capabilities: REQUIRED_CAPABILITIES,
  connectorFields: ["verb", "effectId", "correlationKey", "arguments"],
  eventCursor: "monotonic-sequence",
});
const CONTRACT_HASH = sha256(CONTRACT_DOCUMENT);

function sha256(value) {
  return createHash("sha256").update(value).digest("hex");
}

function readJson(path, fallback) {
  if (!existsSync(path)) return structuredClone(fallback);
  return JSON.parse(readFileSync(path, "utf8"));
}

function writeJson(path, value) {
  const next = `${path}.next`;
  writeFileSync(next, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600 });
  renameSync(next, path);
}

function encodeFrame(value) {
  const payload = Buffer.from(JSON.stringify(value));
  const frame = Buffer.allocUnsafe(payload.length + 4);
  frame.writeUInt32BE(payload.length, 0);
  payload.copy(frame, 4);
  return frame;
}

function frameDecoder(onFrame) {
  let buffered = Buffer.alloc(0);
  return (chunk) => {
    buffered = Buffer.concat([buffered, chunk]);
    while (buffered.length >= 4) {
      const length = buffered.readUInt32BE(0);
      if (length > 1024 * 1024) throw new Error("oversize transport frame");
      if (buffered.length < length + 4) return;
      const payload = buffered.subarray(4, length + 4);
      buffered = buffered.subarray(length + 4);
      onFrame(JSON.parse(payload.toString("utf8")));
    }
  };
}

function connectorMain() {
  const [, , , socketPath, ledgerPath, capabilityMode, faultEffectId] = process.argv;
  const capabilities =
    capabilityMode === "missing-reviewer"
      ? REQUIRED_CAPABILITIES.filter(
          (capability) => capability !== "reviewerAgent.createWithInitialPrompt",
        )
      : REQUIRED_CAPABILITIES;
  const contractHash = capabilityMode === "drift" ? "0".repeat(64) : CONTRACT_HASH;
  const socket = createConnection(socketPath);
  socket.on(
    "data",
    frameDecoder((message) => {
      if (message.type !== "request") return;
      const reply = (result) =>
        socket.write(encodeFrame({ type: "response", id: message.id, result }));
      const reject = (code) =>
        socket.write(encodeFrame({ type: "response", id: message.id, error: { code } }));
      if (message.method === "describe") {
        reply({ contractVersion: CONTRACT_VERSION, contractHash, capabilities });
        return;
      }
      if (message.method === "observe") {
        const ledger = readJson(ledgerPath, { handoffs: [], resources: [] });
        reply({
          matches: ledger.resources.filter(
            (resource) => resource.correlationKey === message.params.correlationKey,
          ),
        });
        return;
      }
      if (message.method !== "invoke") {
        reject("UNKNOWN_METHOD");
        return;
      }
      const params = message.params;
      if (!capabilities.includes(params.verb)) {
        reject("MISSING_CAPABILITY");
        return;
      }
      if ("policy" in params || "retry" in params || "expectedVersion" in params) {
        reject("POLICY_FIELD_AT_CONNECTOR");
        return;
      }
      const ledger = readJson(ledgerPath, { handoffs: [], resources: [] });
      const resource = {
        nativeId: `native-${ledger.handoffs.length + 1}`,
        effectId: params.effectId,
        correlationKey: params.correlationKey,
        verb: params.verb,
        archived: params.verb.endsWith(".archive"),
      };
      ledger.handoffs.push({ effectId: params.effectId, verb: params.verb });
      if (params.verb.endsWith(".archive")) {
        const target = ledger.resources.find(
          (entry) => entry.correlationKey === params.correlationKey,
        );
        if (target) target.archived = true;
      } else {
        ledger.resources.push(resource);
      }
      writeJson(ledgerPath, ledger);
      if (params.effectId === faultEffectId) {
        socket.destroy();
        return;
      }
      reply({ resource });
    }),
  );
  socket.on("error", (error) => {
    if (error.code !== "ECONNRESET") process.stderr.write(`${error.stack}\n`);
  });
}

class ConnectorSession {
  constructor(socket) {
    this.socket = socket;
    this.sequence = 0;
    this.pending = new Map();
    socket.on(
      "data",
      frameDecoder((message) => {
        if (message.type !== "response") return;
        const pending = this.pending.get(message.id);
        if (!pending) return;
        this.pending.delete(message.id);
        if (message.error) pending.reject(Object.assign(new Error(message.error.code), message.error));
        else pending.resolve(message.result);
      }),
    );
    const rejectAll = () => {
      for (const pending of this.pending.values()) pending.reject(new Error("CONNECTION_LOST"));
      this.pending.clear();
    };
    socket.on("close", rejectAll);
    socket.on("error", rejectAll);
  }

  request(method, params = {}) {
    const id = `rpc-${++this.sequence}`;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.socket.write(encodeFrame({ type: "request", id, method, params }));
    });
  }

  close() {
    this.socket.destroy();
  }
}

async function startConnector(root, options = {}) {
  const socketPath = join(root, "connector.sock");
  const ledgerPath = join(root, "external-ledger.json");
  rmSync(socketPath, { force: true });
  const server = createServer();
  const accepted = new Promise((resolve, reject) => {
    server.once("connection", (socket) => resolve(new ConnectorSession(socket)));
    server.once("error", reject);
  });
  await new Promise((resolve, reject) => server.listen(socketPath, resolve).once("error", reject));
  const child = spawn(
    process.execPath,
    [scriptPath, "--connector", socketPath, ledgerPath, options.capabilityMode ?? "full", options.faultEffectId ?? "none"],
    { stdio: ["ignore", "pipe", "pipe"] },
  );
  let stderr = "";
  child.stderr.setEncoding("utf8");
  child.stderr.on("data", (chunk) => {
    stderr += chunk;
  });
  const session = await accepted;
  return {
    session,
    child,
    ledgerPath,
    async stop() {
      session.close();
      child.kill("SIGTERM");
      await new Promise((resolve) => child.once("exit", resolve));
      await new Promise((resolve) => server.close(resolve));
      assert.equal(stderr, "");
      rmSync(socketPath, { force: true });
    },
  };
}

function initialStore() {
  return {
    lease: { holder: "engine-current", epoch: 1 },
    commands: {},
    effects: {},
    observations: [],
    events: [],
    nextSequence: 1,
  };
}

class DurableEngineModel {
  constructor(
    statePath,
    connector,
    identity = { holder: "engine-current", epoch: 1 },
  ) {
    this.statePath = statePath;
    this.connector = connector;
    this.identity = identity;
    this.store = readJson(statePath, initialStore());
  }

  persist() {
    writeJson(this.statePath, this.store);
  }

  appendEvent(type, data) {
    this.store.events.push({ sequence: this.store.nextSequence++, type, ...data });
  }

  assertCurrentFence() {
    const durable = readJson(this.statePath, initialStore());
    if (
      durable.lease.holder !== this.identity.holder ||
      durable.lease.epoch !== this.identity.epoch
    ) {
      const error = new Error("STALE_ENGINE_FENCE");
      error.code = "STALE_ENGINE_FENCE";
      throw error;
    }
  }

  async preflight() {
    const descriptor = await this.connector.request("describe");
    assert.equal(descriptor.contractVersion, CONTRACT_VERSION);
    assert.equal(descriptor.contractHash, CONTRACT_HASH);
    assert.deepEqual([...descriptor.capabilities].sort(), [...REQUIRED_CAPABILITIES].sort());
    return descriptor;
  }

  admitCommand(requestId, payload) {
    const commandKey = sha256(`project-1:system/scheduler:${requestId}`);
    const payloadHash = sha256(JSON.stringify(payload));
    const existing = this.store.commands[commandKey];
    if (existing) {
      if (existing.payloadHash !== payloadHash) {
        const error = new Error("IDEMPOTENCY_KEY_REUSED_WITH_DIFFERENT_PAYLOAD");
        error.code = "IDEMPOTENCY_KEY_REUSED_WITH_DIFFERENT_PAYLOAD";
        throw error;
      }
      return existing;
    }
    const command = { commandKey, requestId, payloadHash, status: "accepted" };
    this.store.commands[commandKey] = command;
    this.appendEvent("command.accepted", { commandKey });
    this.persist();
    return command;
  }

  recordIntent(command, ordinal, spec) {
    const effectId = sha256(`${command.commandKey}:${ordinal}:${spec.verb}:${spec.correlationKey}`);
    if (!this.store.effects[effectId]) {
      if (spec.verb === "executionWorkspace.createManaged" && spec.lifecycleDigest !== "empty") {
        assert.equal(spec.approval?.actorKind, "human", "LIFECYCLE_APPROVAL_REQUIRED");
        assert.equal(spec.approval?.digest, spec.lifecycleDigest, "LIFECYCLE_APPROVAL_MISMATCH");
      }
      this.store.effects[effectId] = {
        effectId,
        commandKey: command.commandKey,
        ordinal,
        verb: spec.verb,
        correlationKey: spec.correlationKey,
        arguments: spec.arguments ?? {},
        compensationFor: spec.compensationFor ?? null,
        phase: "intent_recorded",
      };
      this.appendEvent("effect.intent_recorded", { effectId });
      this.persist();
    }
    return this.store.effects[effectId];
  }

  async reconcile(effect, crashAt) {
    if (effect.phase === "complete") return effect;
    this.assertCurrentFence();
    const outcome = await this.connector.request("observe", {
      verb: effect.verb,
      correlationKey: effect.correlationKey,
    });
    this.store.observations.push({
      effectId: effect.effectId,
      purpose: effect.phase === "dispatching" ? "outcome" : "precondition",
      matches: outcome.matches.length,
    });
    const desired =
      outcome.matches.length === 1 &&
      (effect.verb.endsWith(".archive")
        ? outcome.matches[0].archived === true
        : outcome.matches[0].archived === false);
    effect.phase = desired ? "observation_required" : "observed_absent";
    this.persist();
    if (crashAt === "T1_AFTER_OBSERVATION") throw new Error("INJECTED_T1");
    if (outcome.matches.length > 1) throw new Error("AMBIGUOUS_MATCHES");
    if (desired) {
      effect.nativeId = outcome.matches[0].nativeId;
      effect.phase = "complete";
      this.appendEvent("effect.complete", { effectId: effect.effectId });
      this.persist();
      return effect;
    }
    if (effect.verb === "helperAgent.observe") throw new Error("HELPER_IDENTITY_NOT_OBSERVED");
    effect.phase = "dispatch_authorized";
    this.persist();
    if (crashAt === "T2_AFTER_DISPATCH_CLAIM") throw new Error("INJECTED_T2");
    effect.phase = "dispatching";
    this.persist();
    let result;
    try {
      result = await this.connector.request("invoke", {
        verb: effect.verb,
        effectId: effect.effectId,
        correlationKey: effect.correlationKey,
        arguments: effect.arguments,
      });
    } catch (error) {
      if (error.message === "CONNECTION_LOST") throw new Error("INJECTED_T3_IN_FLIGHT");
      throw error;
    }
    effect.nativeId = result.resource.nativeId;
    effect.phase = "observation_required";
    this.persist();
    if (crashAt === "T4_BEFORE_PROJECTION") throw new Error("INJECTED_T4");
    return this.reconcile(effect);
  }

  projection(commandKey) {
    const effects = Object.values(this.store.effects).filter(
      (effect) => effect.commandKey === commandKey,
    );
    return {
      commandKey,
      complete: effects.length > 0 && effects.every((effect) => effect.phase === "complete"),
      effectCount: effects.length,
      nativeIds: effects.map((effect) => effect.nativeId).filter(Boolean),
    };
  }

  eventsAfter(cursor) {
    assert(Number.isSafeInteger(cursor) && cursor >= 0 && cursor < this.store.nextSequence);
    return this.store.events.filter((event) => event.sequence > cursor);
  }
}

const LIFECYCLE = [
  { verb: "executionWorkspace.createManaged", correlationKey: "run-1/workspace", lifecycleDigest: "empty" },
  { verb: "taskAgent.createWithInitialPrompt", correlationKey: "run-1/task-agent" },
  { verb: "helperAgent.observe", correlationKey: "run-1/helper-agent" },
  { verb: "reviewerAgent.createWithInitialPrompt", correlationKey: "run-1/reviewer-agent" },
  { verb: "agent.archive", correlationKey: "run-1/helper-agent" },
  { verb: "agent.archive", correlationKey: "run-1/reviewer-agent" },
  { verb: "agent.archive", correlationKey: "run-1/task-agent" },
  { verb: "executionWorkspace.archive", correlationKey: "run-1/workspace" },
];

async function runHappyPath(root) {
  const statePath = join(root, "happy-state.json");
  const connector = await startConnector(root);
  try {
    writeJson(connector.ledgerPath, {
      handoffs: [],
      resources: [
        {
          nativeId: "native-helper",
          effectId: "task-agent-owned",
          correlationKey: "run-1/helper-agent",
          verb: "helperAgent.createByTaskAgent",
          archived: false,
        },
      ],
    });
    const engine = new DurableEngineModel(statePath, connector.session);
    await engine.preflight();
    const command = engine.admitCommand("lifecycle-1", { type: "run.lifecycle" });
    const replay = engine.admitCommand("lifecycle-1", { type: "run.lifecycle" });
    assert.equal(replay.commandKey, command.commandKey);
    assert.throws(
      () => engine.admitCommand("lifecycle-1", { type: "different" }),
      /IDEMPOTENCY_KEY_REUSED_WITH_DIFFERENT_PAYLOAD/,
    );
    for (const [ordinal, spec] of LIFECYCLE.entries()) {
      await engine.reconcile(engine.recordIntent(command, ordinal, spec));
    }
    const projection = engine.projection(command.commandKey);
    assert.equal(projection.complete, true);
    assert.equal(projection.effectCount, LIFECYCLE.length);
    const firstPage = engine.eventsAfter(0).slice(0, 4);
    const cursor = firstPage.at(-1).sequence;
    const resumed = engine.eventsAfter(cursor);
    assert.deepEqual(resumed, engine.eventsAfter(cursor));
    assert.equal(new Set([...firstPage, ...resumed].map((event) => event.sequence)).size, engine.store.events.length);
    const ledger = readJson(connector.ledgerPath, { handoffs: [] });
    assert.equal(ledger.handoffs.length, LIFECYCLE.length - 1);
    return {
      effects: projection.effectCount,
      handoffs: ledger.handoffs.length,
      helperObservedWithoutEngineLaunch: true,
      events: engine.store.events.length,
      cursorResumedEvents: resumed.length,
      idempotentReplay: true,
      payloadConflictRejected: true,
    };
  } finally {
    await connector.stop();
  }
}

async function runCrashCase(root, label, crashAt, connectorFault = false) {
  const caseRoot = join(root, label);
  writeFileSync(caseRoot, "", { flag: "a" });
  rmSync(caseRoot, { force: true });
  const statePath = `${caseRoot}.state.json`;
  const commandPayload = { type: "effect.boundary", label };
  let effectId;
  {
    const initialConnector = await startConnector(root);
    const engine = new DurableEngineModel(statePath, initialConnector.session);
    await engine.preflight();
    if (crashAt === "T0_BEFORE_INTENT") {
      await initialConnector.stop();
    } else {
      const command = engine.admitCommand(label, commandPayload);
      const effect = engine.recordIntent(command, 0, {
        verb: "taskAgent.createWithInitialPrompt",
        correlationKey: label,
      });
      effectId = effect.effectId;
      if (crashAt === "T0_AFTER_INTENT") {
        await initialConnector.stop();
      } else {
        await initialConnector.stop();
        const faultingConnector = await startConnector(root, {
          faultEffectId: connectorFault ? effectId : "none",
        });
        const faultingEngine = new DurableEngineModel(statePath, faultingConnector.session);
        try {
          await assert.rejects(() => faultingEngine.reconcile(faultingEngine.store.effects[effectId], crashAt));
        } finally {
          await faultingConnector.stop();
        }
      }
    }
  }
  const recoveryConnector = await startConnector(root);
  try {
    const recovered = new DurableEngineModel(statePath, recoveryConnector.session);
    await recovered.preflight();
    const command = recovered.admitCommand(label, commandPayload);
    const effect = recovered.recordIntent(command, 0, {
      verb: "taskAgent.createWithInitialPrompt",
      correlationKey: label,
    });
    await recovered.reconcile(effect);
    await recovered.reconcile(effect);
    const ledger = readJson(recoveryConnector.ledgerPath, { handoffs: [] });
    const handoffs = ledger.handoffs.filter((entry) => entry.effectId === effect.effectId).length;
    assert.equal(handoffs, 1);
    assert.equal(
      recovered.store.events.filter(
        (event) => event.type === "effect.complete" && event.effectId === effect.effectId,
      ).length,
      1,
    );
    return { boundary: crashAt, handoffs, completions: 1 };
  } finally {
    await recoveryConnector.stop();
  }
}

async function runCompensationCase(root) {
  const statePath = join(root, "compensation-state.json");
  const connector = await startConnector(root);
  try {
    writeJson(connector.ledgerPath, {
      handoffs: [],
      resources: [
        {
          nativeId: "native-compensation-target",
          effectId: "external-original",
          correlationKey: "compensation-target",
          verb: "taskAgent.createWithInitialPrompt",
          archived: false,
        },
      ],
    });
    const engine = new DurableEngineModel(statePath, connector.session);
    await engine.preflight();
    const originalCommand = engine.admitCommand("compensation-original", {
      type: "effect.original",
    });
    const original = engine.recordIntent(originalCommand, 0, {
      verb: "taskAgent.createWithInitialPrompt",
      correlationKey: "compensation-target",
    });
    await engine.reconcile(original);
    const compensationCommand = engine.admitCommand("compensation-followup", {
      type: "effect.compensate",
      compensationFor: original.effectId,
    });
    const compensation = engine.recordIntent(compensationCommand, 0, {
      verb: "agent.archive",
      correlationKey: "compensation-target",
      compensationFor: original.effectId,
    });
    await engine.reconcile(compensation);
    const ledger = readJson(connector.ledgerPath, { handoffs: [] });
    assert.equal(original.phase, "complete");
    assert.equal(compensation.compensationFor, original.effectId);
    assert.equal(ledger.handoffs.length, 1);
    return {
      originalPreserved: true,
      linkedCompensatingCommand: true,
      ordinaryBoundaryProtocolReused: true,
      handoffs: 1,
    };
  } finally {
    await connector.stop();
  }
}

async function runCapabilityNegatives(root) {
  const results = {};
  for (const mode of ["missing-reviewer", "drift"]) {
    const connector = await startConnector(root, { capabilityMode: mode });
    try {
      const engine = new DurableEngineModel(join(root, `${mode}.json`), connector.session);
      await assert.rejects(() => engine.preflight());
      const ledger = readJson(connector.ledgerPath, { handoffs: [] });
      assert.equal(ledger.handoffs.length, 0);
      results[mode] = { rejectedBeforeHandoff: true };
    } finally {
      await connector.stop();
    }
    rmSync(join(root, "external-ledger.json"), { force: true });
  }
  const connector = await startConnector(root);
  try {
    await assert.rejects(
      () =>
        connector.session.request("invoke", {
          verb: "agent.archive",
          effectId: "policy-test",
          correlationKey: "policy-test",
          arguments: {},
          policy: { retry: true },
        }),
      /POLICY_FIELD_AT_CONNECTOR/,
    );
    const ledger = readJson(connector.ledgerPath, { handoffs: [] });
    assert.equal(ledger.handoffs.length, 0);
    results.policyField = { rejectedBeforeHandoff: true };
  } finally {
    await connector.stop();
  }
  rmSync(join(root, "external-ledger.json"), { force: true });
  const fenceConnector = await startConnector(root);
  try {
    const statePath = join(root, "stale-fence.json");
    const stale = new DurableEngineModel(statePath, fenceConnector.session, {
      holder: "engine-stale",
      epoch: 1,
    });
    stale.store.lease = { holder: "engine-stale", epoch: 1 };
    const command = stale.admitCommand("stale-fence", { type: "fence.negative" });
    const effect = stale.recordIntent(command, 0, {
      verb: "taskAgent.createWithInitialPrompt",
      correlationKey: "stale-fence",
    });
    const takeover = readJson(statePath, initialStore());
    takeover.lease = { holder: "engine-current", epoch: 2 };
    writeJson(statePath, takeover);
    await assert.rejects(() => stale.reconcile(effect), /STALE_ENGINE_FENCE/);
    const ledger = readJson(fenceConnector.ledgerPath, { handoffs: [] });
    assert.equal(ledger.handoffs.length, 0);
    results.staleFence = { rejectedBeforeHandoff: true };
  } finally {
    await fenceConnector.stop();
  }
  return results;
}

function runGeneratedClientCheck(root) {
  const generatedPath = join(root, "generated-client.mjs");
  const generate = (contractDocument) => {
    const parsed = JSON.parse(contractDocument);
    return `export const contractVersion = ${JSON.stringify(parsed.version)};\nexport const contractHash = ${JSON.stringify(sha256(contractDocument))};\n`;
  };
  writeFileSync(generatedPath, generate(CONTRACT_DOCUMENT));
  assert.equal(readFileSync(generatedPath, "utf8"), generate(CONTRACT_DOCUMENT));
  const drifted = JSON.stringify({ ...JSON.parse(CONTRACT_DOCUMENT), framing: "newline-json" });
  assert.notEqual(readFileSync(generatedPath, "utf8"), generate(drifted));
  return { cleanGeneration: true, driftRejected: true, generatedOutputCommitted: false };
}

async function main() {
  const root = mkdtempSync(join(tmpdir(), "director-m1.12-contract-"));
  const rootName = basename(root);
  const output = {
    contractVersion: CONTRACT_VERSION,
    contractHash: CONTRACT_HASH,
    transport: "Unix-domain socket with uint32be length-prefixed JSON frames",
    happyPath: null,
    faultMatrix: [],
    compensation: null,
    negatives: null,
    generatedClient: null,
    cleanup: null,
  };
  try {
    output.happyPath = await runHappyPath(root);
    rmSync(join(root, "external-ledger.json"), { force: true });
    for (const [label, boundary, connectorFault] of [
      ["t0-before-intent", "T0_BEFORE_INTENT", false],
      ["t0-after-intent", "T0_AFTER_INTENT", false],
      ["t1-observation", "T1_AFTER_OBSERVATION", false],
      ["t2-dispatch-claim", "T2_AFTER_DISPATCH_CLAIM", false],
      ["t3-in-flight", "T3_IN_FLIGHT", true],
      ["t4-projection", "T4_BEFORE_PROJECTION", false],
    ]) {
      output.faultMatrix.push(await runCrashCase(root, label, boundary, connectorFault));
      rmSync(join(root, "external-ledger.json"), { force: true });
    }
    output.compensation = await runCompensationCase(root);
    rmSync(join(root, "external-ledger.json"), { force: true });
    output.negatives = await runCapabilityNegatives(root);
    output.generatedClient = runGeneratedClientCheck(root);
  } finally {
    rmSync(root, { recursive: true, force: true });
    output.cleanup = {
      ownedRoot: rootName.replace(/-[^-]+$/, "-<random>"),
      absent: !existsSync(root),
    };
  }
  assert.equal(output.cleanup.absent, true);
  process.stdout.write(`${JSON.stringify(output, null, 2)}\n`);
}

if (process.argv[2] === "--connector") connectorMain();
else await main();
