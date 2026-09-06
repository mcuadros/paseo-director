#!/usr/bin/env node

import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { access, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

const FIXED_TIME = 1_780_000_000_000;
const FIXTURE_OBSERVATION_MAX_AGE_MS = 10;
const FIXTURE_OPERATIONAL_LIMITS = Object.freeze({
  maxProcessCount: 4,
  maxMemoryBytes: 4_096,
  maxElapsedMs: 20,
  maxOutputBytes: 2_048,
  maxTemporaryBytes: 8_192,
  maxWorktreeBytes: 16_384,
  minFreeSpaceBasisPoints: 1_000,
});
const FIXTURE_OPERATIONAL_FACTS = Object.freeze({
  processCount: 1,
  memoryBytes: 1_024,
  elapsedMs: 5,
  outputBytes: 512,
  temporaryBytes: 2_048,
  worktreeBytes: 4_096,
  freeSpaceBasisPoints: 2_000,
});

const EMPTY_WORKTREE_LIFECYCLE_SURFACES = Object.freeze({
  "worktree.setup": [],
  "worktree.teardown": [],
  "worktree.terminals[*].command": [],
  "worktree.servicePorts.portScript": [],
});

class ContractError extends Error {
  constructor(code, message = code) {
    super(message);
    this.name = "ContractError";
    this.code = code;
  }
}

function canonical(value) {
  if (
    value === undefined ||
    typeof value === "bigint" ||
    typeof value === "function" ||
    typeof value === "symbol" ||
    (typeof value === "number" && !Number.isFinite(value))
  ) {
    throw new ContractError("NON_JSON_VALUE");
  }
  if (Array.isArray(value)) return `[${value.map(canonical).join(",")}]`;
  if (value && typeof value === "object") {
    return `{${Object.keys(value)
      .sort()
      .map((key) => `${JSON.stringify(key)}:${canonical(value[key])}`)
      .join(",")}}`;
  }
  return JSON.stringify(value);
}

function digest(value) {
  return createHash("sha256").update(canonical(value)).digest("hex");
}

function throwsCode(fn, code) {
  assert.throws(fn, (error) => error instanceof ContractError && error.code === code);
}

function captureCode(fn, expectedCode) {
  try {
    fn();
  } catch (error) {
    assert.ok(error instanceof ContractError);
    assert.equal(error.code, expectedCode);
    return error.code;
  }
  assert.fail(`expected ${expectedCode}`);
}

function effectKey(commandKey, ordinal, spec) {
  return digest({
    commandKey,
    ordinal,
    kind: spec.kind,
    target: spec.target,
    binding: spec.binding,
    contract: {
      observationMaxAgeMs: spec.observationMaxAgeMs,
      operationalLimits: spec.operationalLimits,
      workspaceLifecycleAdmissionHash: digest(spec.workspaceLifecycleAdmission ?? null),
    },
  });
}

const EFFECT_CLASSES = Object.freeze({
  store_only: {
    mutatesExternalState: false,
    retryAfterUnknown: "not_applicable",
  },
  unique_create: {
    mutatesExternalState: true,
    retryAfterUnknown: "only_after_authoritative_absence",
  },
  conditional_update: {
    mutatesExternalState: true,
    retryAfterUnknown: "only_while_exact_precondition_remains",
  },
  idempotent_close: {
    mutatesExternalState: true,
    retryAfterUnknown: "after_exact_owned_present_observation",
  },
  nonrepeatable_progress: {
    mutatesExternalState: true,
    retryAfterUnknown: "never_automatic",
  },
  destructive_terminal: {
    mutatesExternalState: true,
    retryAfterUnknown: "never_when_target_is_present",
  },
});

const EFFECT_CATALOG = Object.freeze([
  ["configuration.apply_commit", "conditional_update"],
  ["project.execution_lease", "store_only"],
  ["taskstore.aggregate_write", "store_only"],
  ["taskstore.git_sync", "conditional_update"],
  ["taskstore.dolt_sync", "conditional_update"],
  ["taskstore.backup_publish", "unique_create"],
  ["taskstore.restore_fresh", "unique_create"],
  ["taskstore.schema_migrate", "store_only"],
  ["paseo.workspace_create", "unique_create"],
  ["paseo.task_agent_create_with_prompt", "unique_create"],
  ["paseo.reviewer_create_with_prompt", "unique_create"],
  ["paseo.helper_reserve", "store_only"],
  ["paseo.helper_observe", "unique_create"],
  ["paseo.agent_continue", "nonrepeatable_progress"],
  ["paseo.agent_archive", "idempotent_close"],
  ["paseo.workspace_archive", "idempotent_close"],
  ["git.branch_create", "conditional_update"],
  ["git.recovery_ref_create", "conditional_update"],
  ["git.private_artifact_publish", "unique_create"],
  ["git.worktree_quarantine", "conditional_update"],
  ["git.worktree_remove", "destructive_terminal"],
  ["git.local_ref_delete", "destructive_terminal"],
  ["git.remote_ref_push", "conditional_update"],
  ["git.remote_ref_delete", "destructive_terminal"],
  ["git.recovery_artifact_expire", "destructive_terminal"],
  ["git.recovery_ref_expire", "destructive_terminal"],
  ["github.pull_request_create", "unique_create"],
  ["github.pull_request_close", "idempotent_close"],
  ["github.pull_request_merge", "conditional_update"],
  ["direct.target_integrate", "conditional_update"],
  ["diagnostic.bundle_publish", "unique_create"],
  ["diagnostic.bundle_expire", "destructive_terminal"],
]);

class ModelStore {
  constructor() {
    this.state = {
      aggregates: {
        "run:run-7": { version: 7, phase: "queued", candidate: "candidate-1" },
        "project:project-1": { version: 3, phase: "active" },
      },
      currentBindings: {
        "run:run-7": { candidate: "candidate-1", base: "base-1", repositoryId: "repo-1" },
      },
      ingressActors: {},
      commands: {},
      effects: {},
      observations: [],
      events: [],
      audits: [],
      lease: null,
      now: FIXED_TIME,
      health: {
        expectedIdentity: "director-taskstore",
        actualIdentity: "director-taskstore",
        globalCommitMode: 0,
        sessionCommitMode: 0,
        connectionState: "ready",
        initializationCount: 1,
      },
    };
  }

  assertHealthy(state = this.state) {
    const health = state.health;
    if (health.actualIdentity !== health.expectedIdentity) {
      throw new ContractError("TASKSTORE_IDENTITY_MISMATCH");
    }
    if (health.globalCommitMode !== 0) {
      throw new ContractError("UNSAFE_GLOBAL_COMMIT_MODE");
    }
    if (health.sessionCommitMode !== 0) {
      // MUTATION_GUARD_START session-commit-mode
      throw new ContractError("UNSAFE_SESSION_COMMIT_MODE");
      // MUTATION_GUARD_END session-commit-mode
    }
    if (health.connectionState !== "ready") {
      throw new ContractError("TASKSTORE_CONNECTION_RESET");
    }
    if (health.initializationCount !== 1) {
      throw new ContractError("TASKSTORE_DUPLICATE_INITIALIZATION");
    }
  }

  transaction(change, { fault = null } = {}) {
    this.assertHealthy();
    const next = structuredClone(this.state);
    const result = change(next);
    if (fault === "before_commit") throw new ContractError("INJECTED_BEFORE_COMMIT");
    this.state = next;
    return structuredClone(result);
  }

  commandIdentity(envelope) {
    return digest({
      projectId: envelope.scope.projectId,
      ingressKind: envelope.ingress.kind,
      ingressAudience: envelope.ingress.audience,
      requestId: envelope.requestId,
    });
  }

  payloadFingerprint(envelope) {
    return digest({
      schemaVersion: envelope.schemaVersion,
      type: envelope.type,
      scope: envelope.scope,
      target: envelope.target,
      expectedVersions: envelope.expectedVersions,
      payload: envelope.payload,
    });
  }

  admit(envelope, specs, options = {}) {
    const commandKey = this.commandIdentity(envelope);
    const payloadHash = this.payloadFingerprint(envelope);
    return this.transaction((state) => {
      const ingressActorKey = digest({
        projectId: envelope.scope.projectId,
        ingressKind: envelope.ingress.kind,
        ingressAudience: envelope.ingress.audience,
      });
      const boundActor = state.ingressActors[ingressActorKey];
      if (boundActor && canonical(boundActor) !== canonical(envelope.actor)) {
        throw new ContractError("INGRESS_ACTOR_MISMATCH");
      }
      if (!boundActor) state.ingressActors[ingressActorKey] = structuredClone(envelope.actor);
      const existing = state.commands[commandKey];
      if (existing) {
        if (existing.payloadHash !== payloadHash) {
          throw new ContractError("IDEMPOTENCY_KEY_REUSED_WITH_DIFFERENT_PAYLOAD");
        }
        return { replay: true, command: existing };
      }

      for (const [aggregateId, expectedVersion] of Object.entries(envelope.expectedVersions)) {
        const aggregate = state.aggregates[aggregateId];
        if (!aggregate || aggregate.version !== expectedVersion) {
          const rejected = {
            commandKey,
            payloadHash,
            outcome: "rejected_version_conflict",
            expectedVersions: envelope.expectedVersions,
            actorAttribution: structuredClone(envelope.actor),
            firstSubmissionAttribution: structuredClone(envelope.submission ?? null),
            effectIds: [],
          };
          state.commands[commandKey] = rejected;
          state.audits.push({ commandKey, code: "EXPECTED_VERSION_CONFLICT" });
          return { replay: false, command: rejected };
        }
      }

      const effectIds = specs.map((spec, ordinal) => effectKey(commandKey, ordinal, spec));
      const command = {
        commandKey,
        payloadHash,
        outcome: "accepted",
        expectedVersions: envelope.expectedVersions,
        actorAttribution: structuredClone(envelope.actor),
        firstSubmissionAttribution: structuredClone(envelope.submission ?? null),
        effectIds,
      };
      state.commands[commandKey] = command;

      for (const aggregateId of Object.keys(envelope.expectedVersions)) {
        state.aggregates[aggregateId].version += 1;
      }
      specs.forEach((spec, ordinal) => {
        const id = effectIds[ordinal];
        state.effects[id] = {
          id,
          commandKey,
          ordinal,
          kind: spec.kind,
          class: spec.class,
          target: structuredClone(spec.target),
          targetHash: digest(spec.target),
          binding: structuredClone(spec.binding),
          bindingHash: digest(spec.binding),
          runAggregateId: `run:${envelope.scope.runId}`,
          expectedExternal: structuredClone(spec.expectedExternal),
          desiredExternal: structuredClone(spec.desiredExternal),
          phase: spec.class === "store_only" ? "complete" : "intent_recorded",
          revision: 0,
          attempts: 0,
          maxAttempts: spec.maxAttempts ?? 2,
          observationMaxAgeMs: spec.observationMaxAgeMs,
          operationalLimits: structuredClone(spec.operationalLimits),
          workspaceLifecycleAdmission: structuredClone(spec.workspaceLifecycleAdmission),
          attempted: false,
          terminal: spec.class === "store_only",
          compensationFor: spec.compensationFor ?? null,
          dispatch: null,
          evidenceObservationId: null,
          anomaly: null,
        };
      });
      state.events.push({ commandKey, type: "command.accepted", effectIds });
      state.audits.push({ commandKey, code: "COMMAND_ACCEPTED" });
      return { replay: false, command };
    }, options);
  }

  acquireLease(holder, ttl) {
    return this.transaction((state) => {
      const current = state.lease;
      if (current && current.expiresAt > state.now && current.holder !== holder) {
        throw new ContractError("PROJECT_LEASE_HELD");
      }
      if (current?.holder === holder && current.expiresAt > state.now) {
        current.expiresAt = state.now + ttl;
        return current;
      }
      const priorHolder = current?.holder ?? null;
      state.lease = {
        holder,
        epoch: (current?.epoch ?? 0) + 1,
        expiresAt: state.now + ttl,
        priorHolder,
        dispatchEnabled: priorHolder === null,
      };
      state.audits.push({
        code: "PROJECT_LEASE_ACQUIRED",
        holder,
        epoch: state.lease.epoch,
        dispatchEnabled: state.lease.dispatchEnabled,
      });
      return state.lease;
    });
  }

  recordExecutorLiveness(holder, epoch, priorHolder, status) {
    return this.transaction((state) => {
      this.assertLease(state, holder, epoch, false);
      if (priorHolder !== state.lease.priorHolder) throw new ContractError("PREVIOUS_EXECUTOR_IDENTITY_MISMATCH");
      const observationId = `observation-${state.observations.length + 1}`;
      state.observations.push({
        id: observationId,
        effectId: null,
        epoch,
        holder,
        source: "linux_process_identity",
        authority: "linux_process_identity",
        status,
        purpose: "takeover",
        attempt: null,
        effectRevision: null,
        consumedBy: null,
        targetHash: digest({ holder: priorHolder }),
        bindingHash: digest({ priorHolder: state.lease.priorHolder }),
        factsHash: digest({ processIdentity: priorHolder, status }),
        observedAt: state.now,
        freshnessBarrier: canonical({ epoch, priorHolder: state.lease.priorHolder }),
      });
      return state.observations.at(-1);
    });
  }

  enableDispatchAfterTakeover(holder, epoch, observationId) {
    return this.transaction((state) => {
      this.assertLease(state, holder, epoch, false);
      const observation = state.observations.find((item) => item.id === observationId);
      if (
        !observation ||
        observation.effectId !== null ||
        observation.holder !== holder ||
        observation.epoch !== epoch ||
        observation.purpose !== "takeover" ||
        observation.authority !== "linux_process_identity" ||
        observation.status !== "absent" ||
        observation.targetHash !== digest({ holder: state.lease.priorHolder }) ||
        state.now - observation.observedAt < 0 ||
        state.now - observation.observedAt > FIXTURE_OBSERVATION_MAX_AGE_MS ||
        observation.consumedBy !== null
      ) {
        throw new ContractError("PREVIOUS_EXECUTOR_ABSENCE_UNPROVEN");
      }
      observation.consumedBy = { transition: "takeover_dispatch", epoch, holder };
      state.lease.dispatchEnabled = true;
      state.lease.takeoverObservationId = observationId;
      state.audits.push({ code: "TAKEOVER_DISPATCH_ENABLED", holder, epoch, observationId });
      return state.lease;
    });
  }

  assertLease(state, holder, epoch, requireDispatch = true) {
    const lease = state.lease;
    if (!lease || lease.holder !== holder || lease.epoch !== epoch || lease.expiresAt <= state.now) {
      throw new ContractError("STALE_PROJECT_LEASE");
    }
    if (requireDispatch && !lease.dispatchEnabled) {
      throw new ContractError("TAKEOVER_OBSERVE_ONLY");
    }
  }

  advance(ms) {
    this.state.now += ms;
  }

  changeRunBinding(aggregateId, binding) {
    return this.transaction((state) => {
      const aggregate = state.aggregates[aggregateId];
      if (!aggregate) throw new ContractError("UNKNOWN_AGGREGATE");
      aggregate.version += 1;
      aggregate.candidate = binding.candidate;
      state.currentBindings[aggregateId] = structuredClone(binding);
      state.events.push({ type: "candidate_or_base.changed", aggregateId, bindingHash: digest(binding) });
      return aggregate.version;
    });
  }

  observe(holder, epoch, effectId, observation) {
    return this.transaction((state) => {
      this.assertLease(state, holder, epoch, false);
      const effect = state.effects[effectId];
      if (!effect) throw new ContractError("UNKNOWN_EFFECT");
      if (!["precondition", "outcome", "drift"].includes(observation.purpose)) {
        throw new ContractError("OBSERVATION_PURPOSE_INVALID");
      }
      const record = {
        id: `observation-${state.observations.length + 1}`,
        effectId,
        epoch,
        holder,
        purpose: observation.purpose,
        attempt: observation.purpose === "precondition" ? effect.attempts + 1 : effect.attempts,
        effectRevision: effect.revision,
        consumedBy: null,
        source: observation.source,
        authority: observation.authority,
        status: observation.status,
        targetHash: observation.targetHash,
        bindingHash: observation.bindingHash,
        factsHash: digest(observation.facts),
        observedAt: state.now,
        freshnessBarrier: observation.freshnessBarrier,
        operationalFacts: structuredClone(observation.operationalFacts),
      };
      state.observations.push(record);
      return record;
    });
  }

  assertObservationForTransition(state, effect, observation, holder, epoch, purpose, attempt) {
    if (!observation || observation.effectId !== effect.id) {
      throw new ContractError("FRESH_OBSERVATION_REQUIRED");
    }
    // MUTATION_GUARD_START observation-epoch
    if (observation.epoch !== epoch) throw new ContractError("OBSERVATION_EPOCH_MISMATCH");
    // MUTATION_GUARD_END observation-epoch
    // MUTATION_GUARD_START observation-holder
    if (observation.holder !== holder) throw new ContractError("OBSERVATION_HOLDER_MISMATCH");
    // MUTATION_GUARD_END observation-holder
    // MUTATION_GUARD_START observation-single-use
    if (observation.consumedBy !== null) throw new ContractError("OBSERVATION_ALREADY_CONSUMED");
    // MUTATION_GUARD_END observation-single-use
    // MUTATION_GUARD_START observation-max-age
    const age = state.now - observation.observedAt;
    if (
      !Number.isInteger(effect.observationMaxAgeMs) ||
      effect.observationMaxAgeMs <= 0 ||
      age < 0 ||
      age > effect.observationMaxAgeMs
    ) {
      throw new ContractError("OBSERVATION_EXPIRED");
    }
    // MUTATION_GUARD_END observation-max-age
    // MUTATION_GUARD_START observation-attempt-binding
    if (
      observation.purpose !== purpose ||
      observation.attempt !== attempt ||
      observation.effectRevision !== effect.revision
    ) {
      throw new ContractError("OBSERVATION_ATTEMPT_MISMATCH");
    }
    // MUTATION_GUARD_END observation-attempt-binding
    if (
      observation.targetHash !== effect.targetHash ||
      observation.bindingHash !== effect.bindingHash ||
      observation.freshnessBarrier !== canonical(effect.binding)
    ) {
      throw new ContractError("OBSERVATION_BINDING_MISMATCH");
    }
  }

  operationalLimitDenial(effect, observation) {
    if (effect.class === "store_only") return null;
    // MUTATION_GUARD_START operational-limits
    const limits = effect.operationalLimits;
    const facts = observation.operationalFacts;
    const finiteMaxima = [
      "maxProcessCount",
      "maxMemoryBytes",
      "maxElapsedMs",
      "maxOutputBytes",
      "maxTemporaryBytes",
      "maxWorktreeBytes",
    ];
    if (
      !limits ||
      finiteMaxima.some((key) => !Number.isFinite(limits[key]) || limits[key] <= 0) ||
      !Number.isFinite(limits.minFreeSpaceBasisPoints) ||
      limits.minFreeSpaceBasisPoints <= 0 ||
      limits.minFreeSpaceBasisPoints > 10_000
    ) {
      return "OPERATIONAL_LIMITS_MISSING";
    }
    const requiredFacts = [
      "processCount",
      "memoryBytes",
      "elapsedMs",
      "outputBytes",
      "temporaryBytes",
      "worktreeBytes",
      "freeSpaceBasisPoints",
    ];
    if (!facts || requiredFacts.some((key) => !Number.isFinite(facts[key]) || facts[key] < 0)) {
      return "OPERATIONAL_FACTS_MISSING";
    }
    if (
      facts.processCount > limits.maxProcessCount ||
      facts.memoryBytes > limits.maxMemoryBytes ||
      facts.elapsedMs > limits.maxElapsedMs ||
      facts.outputBytes > limits.maxOutputBytes ||
      facts.temporaryBytes > limits.maxTemporaryBytes ||
      facts.worktreeBytes > limits.maxWorktreeBytes ||
      facts.freeSpaceBasisPoints < limits.minFreeSpaceBasisPoints
    ) {
      return "OPERATIONAL_LIMIT_EXCEEDED";
    }
    // MUTATION_GUARD_END operational-limits
    return null;
  }

  workspaceLifecycleDenial(effect) {
    if (effect.kind !== "paseo.workspace_create") return null;
    // MUTATION_GUARD_START workspace-lifecycle-admission
    const admission = effect.workspaceLifecycleAdmission;
    if (!admission?.surfaces || admission.digest !== digest(admission.surfaces)) {
      return "WORKTREE_LIFECYCLE_ADMISSION_MISSING";
    }
    if (
      canonical(Object.keys(admission.surfaces).sort()) !==
      canonical(Object.keys(EMPTY_WORKTREE_LIFECYCLE_SURFACES).sort())
    ) {
      return "WORKTREE_LIFECYCLE_ADMISSION_MISSING";
    }
    const values = Object.values(admission.surfaces);
    const isEmpty = values.every((value) => Array.isArray(value) && value.length === 0);
    if (isEmpty) return null;
    const approval = admission.approval;
    if (
      approval?.actorKind !== "human" ||
      approval.serverDerived !== true ||
      approval.lifecycleDigest !== admission.digest ||
      approval.targetHash !== effect.targetHash ||
      approval.bindingHash !== effect.bindingHash
    ) {
      return "WORKTREE_LIFECYCLE_APPROVAL_REQUIRED";
    }
    // MUTATION_GUARD_END workspace-lifecycle-admission
    return null;
  }

  parkNeedsYou(state, effect, observation, code) {
    if (observation) {
      observation.consumedBy = {
        transition: "needs_you",
        effectId: effect.id,
        attempt: observation.attempt,
        epoch: observation.epoch,
      };
    }
    effect.phase = "needs_you";
    effect.anomaly = code;
    effect.revision += 1;
    state.audits.push({ code, effectId: effect.id });
  }

  authorizeDispatch(holder, epoch, effectId, observationId, gate = null) {
    let persistedDenial = null;
    const dispatch = this.transaction((state) => {
      this.assertLease(state, holder, epoch, true);
      const effect = state.effects[effectId];
      if (!effect || effect.terminal) throw new ContractError("EFFECT_NOT_DISPATCHABLE");
      if (digest(state.currentBindings[effect.runAggregateId]) !== effect.bindingHash) {
        throw new ContractError("EFFECT_BINDING_STALE");
      }
      if (!["intent_recorded", "retryable", "failed_pre_dispatch", "refused_before_dispatch"].includes(effect.phase)) {
        throw new ContractError("EFFECT_NOT_DISPATCHABLE");
      }
      if (effect.attempts >= effect.maxAttempts) throw new ContractError("EFFECT_BUDGET_EXHAUSTED");
      const observation = state.observations.find((item) => item.id === observationId);
      this.assertObservationForTransition(
        state,
        effect,
        observation,
        holder,
        epoch,
        "precondition",
        effect.attempts + 1,
      );
      const lifecycleDenial = this.workspaceLifecycleDenial(effect);
      const operationalDenial = this.operationalLimitDenial(effect, observation);
      persistedDenial = lifecycleDenial ?? operationalDenial;
      if (persistedDenial) {
        this.parkNeedsYou(state, effect, observation, persistedDenial);
        return null;
      }
      if (observation.status === "unavailable") throw new ContractError("EXTERNAL_UNAVAILABLE");
      if (observation.status === "different" || observation.status === "ambiguous") {
        throw new ContractError("EXTERNAL_FACTS_AMBIGUOUS");
      }
      const allowed = {
        unique_create: ["absent"],
        conditional_update: ["current_expected", "absent"],
        idempotent_close: ["owned_present"],
        nonrepeatable_progress: ["ready"],
        destructive_terminal: ["owned_present"],
      }[effect.class];
      if (!allowed?.includes(observation.status)) {
        throw new ContractError("EXTERNAL_PRECONDITION_FAILED");
      }
      if (
        effect.class === "conditional_update" &&
        observation.status === "absent" &&
        effect.expectedExternal?.presence !== "absent"
      ) {
        throw new ContractError("EXTERNAL_PRECONDITION_FAILED");
      }
      if (effect.class === "destructive_terminal") {
        if (
          !gate ||
          gate.effectId !== effectId ||
          gate.epoch !== epoch ||
          gate.targetHash !== effect.targetHash ||
          gate.bindingHash !== effect.bindingHash ||
          gate.consumed
        ) {
          throw new ContractError("DESTRUCTIVE_GATE_REQUIRED");
        }
        gate.consumed = true;
      }
      observation.consumedBy = {
        transition: "dispatch",
        effectId,
        attempt: effect.attempts + 1,
        epoch,
      };
      effect.attempts += 1;
      effect.phase = "dispatch_authorized";
      effect.revision += 1;
      effect.dispatch = {
        epoch,
        attempt: effect.attempts,
        observationId,
        expiresAt: state.lease.expiresAt,
        token: digest({ effectId, epoch, attempt: effect.attempts, observationId }),
        consumed: false,
      };
      state.audits.push({
        code: "EFFECT_DISPATCH_AUTHORIZED",
        effectId,
        epoch,
        attempt: effect.attempts,
      });
      return effect.dispatch;
    });
    if (persistedDenial) throw new ContractError(persistedDenial);
    return dispatch;
  }

  beginDispatch(holder, epoch, effectId, token) {
    return this.transaction((state) => {
      this.assertLease(state, holder, epoch, true);
      const effect = state.effects[effectId];
      if (
        effect?.phase !== "dispatch_authorized" ||
        effect.dispatch?.token !== token ||
        effect.dispatch.consumed
      ) {
        throw new ContractError("DISPATCH_TOKEN_INVALID");
      }
      effect.dispatch.consumed = true;
      effect.attempted = true;
      effect.phase = "dispatching";
      effect.revision += 1;
      return structuredClone(effect.dispatch);
    });
  }

  markPreDispatchFailure(holder, epoch, effectId, code) {
    return this.transaction((state) => {
      this.assertLease(state, holder, epoch, true);
      const effect = state.effects[effectId];
      if (effect?.phase !== "dispatching") throw new ContractError("EFFECT_NOT_DISPATCHING");
      effect.phase = "failed_pre_dispatch";
      effect.attempted = false;
      effect.revision += 1;
      state.audits.push({ code, effectId });
      return effect;
    });
  }

  recordRefusal(holder, epoch, effectId, code) {
    return this.transaction((state) => {
      this.assertLease(state, holder, epoch, false);
      const effect = state.effects[effectId];
      if (!effect || effect.attempted || effect.terminal) {
        throw new ContractError("REFUSAL_NOT_RECORDABLE");
      }
      effect.phase = "refused_before_dispatch";
      effect.anomaly = code;
      effect.revision += 1;
      state.audits.push({ code: "EFFECT_REFUSED_BEFORE_DISPATCH", effectId, reason: code });
      return effect;
    });
  }

  markUnknown(holder, epoch, effectId) {
    return this.transaction((state) => {
      this.assertLease(state, holder, epoch, false);
      const effect = state.effects[effectId];
      if (!["dispatching", "dispatch_authorized"].includes(effect?.phase)) {
        throw new ContractError("EFFECT_NOT_IN_FLIGHT");
      }
      effect.phase = "unknown";
      effect.revision += 1;
      state.audits.push({ code: "EFFECT_OUTCOME_UNKNOWN", effectId });
      return effect;
    });
  }

  markObservationRequired(holder, epoch, effectId) {
    return this.transaction((state) => {
      this.assertLease(state, holder, epoch, false);
      const effect = state.effects[effectId];
      if (effect?.phase !== "dispatching") throw new ContractError("EFFECT_NOT_DISPATCHING");
      effect.phase = "observation_required";
      effect.revision += 1;
      state.audits.push({ code: "ADAPTER_RECEIPT_RECORDED", effectId });
      return effect;
    });
  }

  reconcileDecision(holder, epoch, effectId, observationId) {
    return this.transaction((state) => {
      this.assertLease(state, holder, epoch, false);
      const effect = state.effects[effectId];
      const observation = state.observations.find((item) => item.id === observationId);
      if (!effect || !observation || observation.effectId !== effectId) {
        throw new ContractError("RECONCILIATION_EVIDENCE_MISSING");
      }
      if (effect.terminal) return effect;
      this.assertObservationForTransition(
        state,
        effect,
        observation,
        holder,
        epoch,
        "outcome",
        effect.attempts,
      );
      if (digest(state.currentBindings[effect.runAggregateId]) !== effect.bindingHash) {
        this.parkNeedsYou(state, effect, observation, "EFFECT_BINDING_STALE");
        return effect;
      }
      const operationalDenial = this.operationalLimitDenial(effect, observation);
      if (operationalDenial) {
        this.parkNeedsYou(state, effect, observation, operationalDenial);
        return effect;
      }
      observation.consumedBy = {
        transition: "reconcile",
        effectId,
        attempt: observation.attempt,
        epoch,
      };
      if (observation.status === "desired") {
        effect.phase = "complete";
        effect.terminal = true;
        effect.evidenceObservationId = observationId;
        effect.revision += 1;
        state.events.push({ type: "effect.completed", effectId, observationId });
        return effect;
      }
      if (observation.status === "unavailable") {
        effect.phase = "waiting_external";
        effect.revision += 1;
        return effect;
      }
      if (observation.status === "different" || observation.status === "ambiguous") {
        effect.phase = "needs_you";
        effect.anomaly = "EXTERNAL_FACTS_AMBIGUOUS";
        effect.revision += 1;
        return effect;
      }

      if (!effect.attempted) {
        effect.phase = "intent_recorded";
        effect.revision += 1;
        return effect;
      }

      const retryable =
        (effect.class === "unique_create" && observation.status === "absent") ||
        (effect.class === "conditional_update" &&
          (observation.status === "current_expected" ||
            (observation.status === "absent" && effect.expectedExternal?.presence === "absent"))) ||
        (effect.class === "idempotent_close" && observation.status === "owned_present");
      if (retryable && effect.attempts < effect.maxAttempts) {
        effect.phase = "retryable";
        effect.revision += 1;
        return effect;
      }

      effect.phase = "needs_you";
      effect.anomaly =
        effect.attempts >= effect.maxAttempts
          ? "EFFECT_BUDGET_EXHAUSTED"
          : "UNKNOWN_EFFECT_NOT_SAFE_TO_REPEAT";
      effect.revision += 1;
      return effect;
    });
  }

  recordTerminalDrift(holder, epoch, effectId, observationId) {
    return this.transaction((state) => {
      this.assertLease(state, holder, epoch, false);
      const effect = state.effects[effectId];
      const observation = state.observations.find((item) => item.id === observationId);
      if (!effect?.terminal || !observation || observation.status === "desired") {
        throw new ContractError("TERMINAL_DRIFT_NOT_PROVEN");
      }
      this.assertObservationForTransition(
        state,
        effect,
        observation,
        holder,
        epoch,
        "drift",
        effect.attempts,
      );
      observation.consumedBy = { transition: "terminal_drift", effectId, attempt: effect.attempts, epoch };
      effect.anomaly = "TERMINAL_EXTERNAL_DRIFT";
      effect.revision += 1;
      state.audits.push({ code: "TERMINAL_EXTERNAL_DRIFT", effectId, observationId });
      return effect;
    });
  }
}

class FakeExternal {
  constructor() {
    this.targets = {};
    this.handoffs = {};
    this.unavailable = new Set();
  }

  seed(target, value) {
    this.targets[canonical(target)] = structuredClone(value);
  }

  handoffCount(effectId) {
    return this.handoffs[effectId] ?? 0;
  }

  observation(effect, purpose, statusOverride = null, operationalFacts = FIXTURE_OPERATIONAL_FACTS) {
    const targetKey = canonical(effect.target);
    const current = this.targets[targetKey];
    let status = statusOverride;
    if (!status) {
      if (this.unavailable.has(targetKey)) status = "unavailable";
      else if (current === undefined) {
        status = effect.desiredExternal?.presence === "absent" ? "desired" : "absent";
      } else if (canonical(current) === canonical(effect.desiredExternal)) status = "desired";
      else if (canonical(current) === canonical(effect.expectedExternal)) {
        status = effect.class === "conditional_update" ? "current_expected" : "owned_present";
      } else status = "different";
    }
    return {
      source: "fake_authoritative_adapter",
      authority: "effect_specific_public_fact",
      purpose,
      status,
      targetHash: effect.targetHash,
      bindingHash: effect.bindingHash,
      facts: current === undefined ? { presence: "absent" } : { presence: "present", value: current },
      freshnessBarrier: canonical(effect.binding),
      operationalFacts,
    };
  }

  dispatch(effect, mode = "apply") {
    const targetKey = canonical(effect.target);
    if (mode === "proven_pre_dispatch_failure") {
      throw new ContractError("PROVEN_NO_EXTERNAL_HANDOFF");
    }
    this.handoffs[effect.id] = this.handoffCount(effect.id) + 1;
    if (mode === "ambiguous_without_visible_result") {
      throw new ContractError("LOST_RESPONSE");
    }
    const current = this.targets[targetKey];
    if (effect.class === "conditional_update") {
      const matchesExpected =
        current === undefined
          ? effect.expectedExternal?.presence === "absent"
          : canonical(current) === canonical(effect.expectedExternal);
      if (!matchesExpected) {
        throw new ContractError("EXTERNAL_COMPARE_FAILED");
      }
      this.targets[targetKey] = structuredClone(effect.desiredExternal);
    } else if (effect.class === "unique_create") {
      if (current !== undefined) throw new ContractError("EXTERNAL_ALREADY_EXISTS");
      this.targets[targetKey] = structuredClone(effect.desiredExternal);
    } else if (effect.class === "idempotent_close") {
      this.targets[targetKey] = structuredClone(effect.desiredExternal);
    } else if (effect.class === "nonrepeatable_progress") {
      this.targets[targetKey] = structuredClone(effect.desiredExternal);
    } else if (effect.class === "destructive_terminal") {
      if (canonical(current) !== canonical(effect.expectedExternal)) {
        throw new ContractError("EXTERNAL_COMPARE_FAILED");
      }
      delete this.targets[targetKey];
    } else {
      throw new ContractError("UNKNOWN_EFFECT_CLASS");
    }
    if (mode === "apply_then_lose_response") throw new ContractError("LOST_RESPONSE");
    return { accepted: true };
  }
}

function envelope({ requestId, payload, expectedVersion = 7, type = "run.advance" }) {
  return {
    schemaVersion: 1,
    ingress: { kind: "mcp", audience: "bridge-9" },
    actor: { kind: "task_agent", instanceId: "agent-44" },
    requestId,
    type,
    scope: {
      projectId: "project-1",
      workspaceId: "workspace-2",
      taskId: "task-3",
      runId: "run-7",
      role: "task_agent",
    },
    target: { aggregateType: "run", aggregateId: "run-7" },
    expectedVersions: { "run:run-7": expectedVersion },
    payload,
  };
}

function spec({
  kind,
  effectClass,
  target,
  expectedExternal,
  desiredExternal,
  binding = { candidate: "candidate-1", base: "base-1", repositoryId: "repo-1" },
  maxAttempts = 2,
  compensationFor = null,
  observationMaxAgeMs = FIXTURE_OBSERVATION_MAX_AGE_MS,
  operationalLimits = FIXTURE_OPERATIONAL_LIMITS,
  workspaceLifecycleAdmission = null,
}) {
  return {
    kind,
    class: effectClass,
    target,
    binding,
    expectedExternal,
    desiredExternal,
    maxAttempts,
    compensationFor,
    observationMaxAgeMs: effectClass === "store_only" ? null : observationMaxAgeMs,
    operationalLimits: effectClass === "store_only" ? null : operationalLimits,
    workspaceLifecycleAdmission,
  };
}

function activeLease(store, holder = "engine-a") {
  return store.acquireLease(holder, 100);
}

function recordCurrentObservation(
  store,
  external,
  holder,
  epoch,
  effectId,
  { purpose = "precondition", status = null, operationalFacts = FIXTURE_OPERATIONAL_FACTS } = {},
) {
  const effect = store.state.effects[effectId];
  return store.observe(
    holder,
    epoch,
    effectId,
    external.observation(effect, purpose, status, operationalFacts),
  );
}

function recordOutcomeObservation(store, external, holder, epoch, effectId, options = {}) {
  return recordCurrentObservation(store, external, holder, epoch, effectId, {
    ...options,
    purpose: "outcome",
  });
}

function recordDriftObservation(store, external, holder, epoch, effectId, options = {}) {
  return recordCurrentObservation(store, external, holder, epoch, effectId, {
    ...options,
    purpose: "drift",
  });
}

function emptyWorkspaceLifecycleAdmission() {
  return {
    surfaces: structuredClone(EMPTY_WORKTREE_LIFECYCLE_SURFACES),
    digest: digest(EMPTY_WORKTREE_LIFECYCLE_SURFACES),
    approval: null,
  };
}

function approvedWorkspaceLifecycleAdmission(target, binding) {
  const surfaces = {
    ...structuredClone(EMPTY_WORKTREE_LIFECYCLE_SURFACES),
    "worktree.setup": ["approved-setup-digest"],
  };
  const lifecycleDigest = digest(surfaces);
  return {
    surfaces,
    digest: lifecycleDigest,
    approval: {
      actorKind: "human",
      serverDerived: true,
      lifecycleDigest,
      targetHash: digest(target),
      bindingHash: digest(binding),
    },
  };
}

function dispatchOnce(store, external, holder, epoch, effectId, observation, mode, gate = null) {
  const auth = store.authorizeDispatch(holder, epoch, effectId, observation.id, gate);
  store.beginDispatch(holder, epoch, effectId, auth.token);
  try {
    external.dispatch(store.state.effects[effectId], mode);
  } catch (error) {
    if (error.code === "PROVEN_NO_EXTERNAL_HANDOFF") {
      store.markPreDispatchFailure(holder, epoch, effectId, error.code);
    } else {
      store.markUnknown(holder, epoch, effectId);
    }
    return error.code;
  }
  store.markObservationRequired(holder, epoch, effectId);
  return "response_received_observation_still_required";
}

function destructiveGate(effect, epoch) {
  return {
    effectId: effect.id,
    epoch,
    targetHash: effect.targetHash,
    bindingHash: effect.bindingHash,
    consumed: false,
  };
}

const results = {};

function testCommandIdentityAndTransactions() {
  const store = new ModelStore();
  const createSpec = spec({
    kind: "github.pull_request_create",
    effectClass: "unique_create",
    target: { repositoryId: "repo-1", marker: "run-7" },
    expectedExternal: null,
    desiredExternal: { pr: 42, head: "candidate-1" },
  });
  const command = envelope({ requestId: "request-1", payload: { title: "Exact candidate" } });

  const before = structuredClone(store.state);
  throwsCode(() => store.admit(command, [createSpec], { fault: "before_commit" }), "INJECTED_BEFORE_COMMIT");
  assert.deepEqual(store.state, before);

  const first = store.admit(command, [createSpec]);
  assert.equal(first.replay, false);
  assert.equal(store.state.aggregates["run:run-7"].version, 8);
  assert.equal(store.state.events.length, 1);
  assert.equal(store.state.audits.length, 1);
  assert.equal(Object.keys(store.state.effects).length, 1);

  const replay = store.admit(command, [createSpec]);
  assert.equal(replay.replay, true);
  assert.deepEqual(replay.command, first.command);
  assert.equal(store.state.aggregates["run:run-7"].version, 8);
  throwsCode(
    () => store.admit({ ...command, payload: { title: "Changed" } }, [createSpec]),
    "IDEMPOTENCY_KEY_REUSED_WITH_DIFFERENT_PAYLOAD",
  );
  throwsCode(
    () => store.admit(envelope({ requestId: "non-json", payload: { invalid: undefined } }), []),
    "NON_JSON_VALUE",
  );

  const stale = store.admit(
    envelope({ requestId: "request-stale", payload: { phase: "review" }, expectedVersion: 7 }),
    [],
  );
  assert.equal(stale.command.outcome, "rejected_version_conflict");
  const staleReplay = store.admit(
    envelope({ requestId: "request-stale", payload: { phase: "review" }, expectedVersion: 7 }),
    [],
  );
  assert.equal(staleReplay.replay, true);
  assert.equal(store.state.aggregates["run:run-7"].version, 8);

  const systemEnvelopeA = {
    ...envelope({ requestId: "system-slot-run-7-review-1", payload: { reconcile: true }, expectedVersion: 8 }),
    ingress: { kind: "engine", audience: "scheduler:project-1" },
    actor: { kind: "system", instanceId: "scheduler" },
    submission: { executorInstanceId: "engine-a" },
  };
  const systemFirst = store.admit(systemEnvelopeA, []);
  const versionAfterSystemFirst = store.state.aggregates["run:run-7"].version;
  const systemEnvelopeB = {
    ...systemEnvelopeA,
    submission: { executorInstanceId: "engine-b" },
  };
  const systemReplay = store.admit(systemEnvelopeB, []);
  assert.equal(systemReplay.replay, true);
  assert.equal(systemReplay.command.commandKey, systemFirst.command.commandKey);
  assert.deepEqual(systemReplay.command.actorAttribution, systemEnvelopeA.actor);
  assert.equal(store.state.aggregates["run:run-7"].version, versionAfterSystemFirst);
  const ingressActorMismatch = captureCode(
    () =>
      store.admit(
        {
          ...envelope({ requestId: "wrong-actor", payload: { reconcile: true }, expectedVersion: 9 }),
          actor: { kind: "task_agent", instanceId: "agent-other" },
        },
        [],
      ),
    "INGRESS_ACTOR_MISMATCH",
  );

  results.commandIdentity = {
    acceptedEffects: first.command.effectIds.length,
    aggregateVersion: 8,
    atomicFaultLeftNoRows: true,
    durableReplay: true,
    payloadMismatchRejected: true,
    nonJsonPayloadRejected: true,
    staleVersionOutcomeDurable: true,
    restartStableIngressReplay: systemReplay.replay,
    actorAttribution: systemReplay.command.actorAttribution,
    firstSubmissionAttribution: systemReplay.command.firstSubmissionAttribution,
    replaySubmissionAttribution: systemEnvelopeB.submission,
    ingressActorMismatch,
  };
}

function testCrashBoundaryMatrix() {
  const makeCreate = (requestId) => {
    const store = new ModelStore();
    const external = new FakeExternal();
    const effectSpec = spec({
      kind: "paseo.workspace_create",
      effectClass: "unique_create",
      target: { labels: `project-1/run-7/${requestId}` },
      expectedExternal: null,
      desiredExternal: { workspaceId: `workspace-${requestId}` },
      workspaceLifecycleAdmission: emptyWorkspaceLifecycleAdmission(),
    });
    return { store, external, effectSpec };
  };

  {
    const { store, effectSpec } = makeCreate("before-intent");
    throwsCode(
      () =>
        store.admit(
          envelope({ requestId: "crash-before-intent", payload: { create: true } }),
          [effectSpec],
          { fault: "before_commit" },
        ),
      "INJECTED_BEFORE_COMMIT",
    );
    assert.equal(Object.keys(store.state.effects).length, 0);
  }

  {
    const { store, external, effectSpec } = makeCreate("after-intent");
    const admitted = store.admit(
      envelope({ requestId: "crash-after-intent", payload: { create: true } }),
      [effectSpec],
    );
    const effectId = admitted.command.effectIds[0];
    const lease = activeLease(store);
    const absent = recordOutcomeObservation(store, external, "engine-a", lease.epoch, effectId);
    assert.equal(store.reconcileDecision("engine-a", lease.epoch, effectId, absent.id).phase, "intent_recorded");
    assert.equal(external.handoffCount(effectId), 0);
  }

  {
    const { store, external, effectSpec } = makeCreate("after-authorization");
    const admitted = store.admit(
      envelope({ requestId: "crash-after-authorization", payload: { create: true } }),
      [effectSpec],
    );
    const effectId = admitted.command.effectIds[0];
    const lease = activeLease(store);
    const absent = recordCurrentObservation(store, external, "engine-a", lease.epoch, effectId);
    const token = store.authorizeDispatch("engine-a", lease.epoch, effectId, absent.id).token;
    const afterCrash = recordOutcomeObservation(store, external, "engine-a", lease.epoch, effectId);
    assert.equal(store.reconcileDecision("engine-a", lease.epoch, effectId, afterCrash.id).phase, "intent_recorded");
    throwsCode(() => store.beginDispatch("engine-a", lease.epoch, effectId, token), "DISPATCH_TOKEN_INVALID");
    assert.equal(external.handoffCount(effectId), 0);
  }

  {
    const { store, external, effectSpec } = makeCreate("after-handoff-marker");
    const admitted = store.admit(
      envelope({ requestId: "crash-after-handoff-marker", payload: { create: true } }),
      [effectSpec],
    );
    const effectId = admitted.command.effectIds[0];
    const lease = activeLease(store);
    const absent = recordCurrentObservation(store, external, "engine-a", lease.epoch, effectId);
    const auth = store.authorizeDispatch("engine-a", lease.epoch, effectId, absent.id);
    store.beginDispatch("engine-a", lease.epoch, effectId, auth.token);
    store.markUnknown("engine-a", lease.epoch, effectId);
    const afterCrash = recordOutcomeObservation(store, external, "engine-a", lease.epoch, effectId);
    assert.equal(store.reconcileDecision("engine-a", lease.epoch, effectId, afterCrash.id).phase, "retryable");
    assert.equal(external.handoffCount(effectId), 0);
  }

  {
    const { store, external, effectSpec } = makeCreate("after-external-effect");
    const admitted = store.admit(
      envelope({ requestId: "crash-after-external-effect", payload: { create: true } }),
      [effectSpec],
    );
    const effectId = admitted.command.effectIds[0];
    const lease = activeLease(store);
    const absent = recordCurrentObservation(store, external, "engine-a", lease.epoch, effectId);
    dispatchOnce(store, external, "engine-a", lease.epoch, effectId, absent, "apply_then_lose_response");
    const desired = recordOutcomeObservation(store, external, "engine-a", lease.epoch, effectId);
    assert.equal(store.reconcileDecision("engine-a", lease.epoch, effectId, desired.id).phase, "complete");
    assert.equal(external.handoffCount(effectId), 1);
  }

  {
    const { store, external, effectSpec } = makeCreate("after-observation");
    const admitted = store.admit(
      envelope({ requestId: "crash-after-observation", payload: { create: true } }),
      [effectSpec],
    );
    const effectId = admitted.command.effectIds[0];
    const lease = activeLease(store);
    const absent = recordCurrentObservation(store, external, "engine-a", lease.epoch, effectId);
    dispatchOnce(store, external, "engine-a", lease.epoch, effectId, absent, "apply_then_lose_response");
    const desired = recordOutcomeObservation(store, external, "engine-a", lease.epoch, effectId);
    const eventCount = store.state.events.length;
    assert.equal(store.reconcileDecision("engine-a", lease.epoch, effectId, desired.id).phase, "complete");
    assert.equal(store.state.events.length, eventCount + 1);
    assert.equal(store.reconcileDecision("engine-a", lease.epoch, effectId, desired.id).phase, "complete");
    assert.equal(store.state.events.length, eventCount + 1);
  }

  results.crashBoundaries = {
    durableBoundariesChecked: 6,
    beforeIntentPartials: 0,
    intentWithoutAttemptHandoffs: 0,
    staleAuthorizationTokensAccepted: 0,
    handoffMarkerWithoutVisibleEffect: "retryable_only_by_class",
    effectBeforeResultAdopted: true,
    repeatedTerminalProjectionEvents: 0,
  };
}

function testLeaseFencingAndTakeover() {
  const store = new ModelStore();
  const external = new FakeExternal();
  const effectSpec = spec({
    kind: "git.remote_ref_push",
    effectClass: "conditional_update",
    target: { repositoryId: "repo-1", ref: "refs/heads/task-3" },
    expectedExternal: { oid: "old" },
    desiredExternal: { oid: "candidate-1" },
  });
  const admitted = store.admit(envelope({ requestId: "lease-command", payload: { push: true } }), [effectSpec]);
  const effectId = admitted.command.effectIds[0];
  external.seed(effectSpec.target, effectSpec.expectedExternal);

  const leaseA = activeLease(store, "engine-a");
  const obsA = recordCurrentObservation(store, external, "engine-a", leaseA.epoch, effectId);
  assert.equal(obsA.status, "current_expected");
  store.advance(101);
  const leaseB = store.acquireLease("engine-b", 100);
  assert.equal(leaseB.epoch, 2);
  assert.equal(leaseB.dispatchEnabled, false);
  throwsCode(
    () => store.authorizeDispatch("engine-b", leaseB.epoch, effectId, obsA.id),
    "TAKEOVER_OBSERVE_ONLY",
  );
  throwsCode(
    () => store.authorizeDispatch("engine-a", leaseA.epoch, effectId, obsA.id),
    "STALE_PROJECT_LEASE",
  );

  const liveA = store.recordExecutorLiveness("engine-b", leaseB.epoch, "engine-a", "owned_present");
  throwsCode(
    () => store.enableDispatchAfterTakeover("engine-b", leaseB.epoch, liveA.id),
    "PREVIOUS_EXECUTOR_ABSENCE_UNPROVEN",
  );
  const absentA = store.recordExecutorLiveness("engine-b", leaseB.epoch, "engine-a", "absent");
  store.enableDispatchAfterTakeover("engine-b", leaseB.epoch, absentA.id);
  const obsB = recordCurrentObservation(store, external, "engine-b", leaseB.epoch, effectId);
  assert.equal(dispatchOnce(store, external, "engine-b", leaseB.epoch, effectId, obsB, "apply"), "response_received_observation_still_required");
  assert.equal(external.handoffCount(effectId), 1);

  store.advance(101);
  const leaseC = store.acquireLease("engine-c", 100);
  throwsCode(() => store.markUnknown("engine-b", leaseB.epoch, effectId), "STALE_PROJECT_LEASE");
  const absentB = store.recordExecutorLiveness("engine-c", leaseC.epoch, "engine-b", "absent");
  store.enableDispatchAfterTakeover("engine-c", leaseC.epoch, absentB.id);
  const obsC = recordOutcomeObservation(store, external, "engine-c", leaseC.epoch, effectId);
  assert.equal(store.reconcileDecision("engine-c", leaseC.epoch, effectId, obsC.id).phase, "complete");

  results.leaseFencing = {
    epochs: [leaseA.epoch, leaseB.epoch, leaseC.epoch],
    livePriorExecutorWasObserveOnly: true,
    livenessObservations: store.state.observations.filter((item) => item.purpose === "takeover").length,
    consumedAbsenceProofs: store.state.observations.filter(
      (item) => item.purpose === "takeover" && item.consumedBy?.transition === "takeover_dispatch",
    ).length,
    staleDispatchRejected: true,
    staleFinalizeRejected: true,
    externalHandoffs: external.handoffCount(effectId),
  };
}

function testObservationAuthorizationGuards() {
  const makeEffect = (requestId, effectClass = "conditional_update", options = {}) => {
    const store = new ModelStore();
    const external = new FakeExternal();
    const effectSpec = spec({
      kind: effectClass === "idempotent_close" ? "paseo.agent_archive" : "git.remote_ref_push",
      effectClass,
      target: { repositoryId: "repo-1", ref: `refs/heads/${requestId}` },
      expectedExternal: effectClass === "idempotent_close" ? { status: "active" } : { oid: "old" },
      desiredExternal: effectClass === "idempotent_close" ? { status: "closed" } : { oid: "candidate-1" },
      ...options,
    });
    const admitted = store.admit(envelope({ requestId, payload: { guarded: true } }), [effectSpec]);
    const effectId = admitted.command.effectIds[0];
    external.seed(effectSpec.target, effectSpec.expectedExternal);
    return { store, external, effectId };
  };

  const observedCodes = [];

  {
    const { store, external, effectId } = makeEffect(
      "stale-observation-epoch",
      "conditional_update",
      { observationMaxAgeMs: 1_000 },
    );
    const leaseA = activeLease(store, "engine-a");
    const oldObservation = recordCurrentObservation(store, external, "engine-a", leaseA.epoch, effectId);
    store.advance(101);
    const leaseB = store.acquireLease("engine-b", 100);
    const absence = store.recordExecutorLiveness("engine-b", leaseB.epoch, "engine-a", "absent");
    store.enableDispatchAfterTakeover("engine-b", leaseB.epoch, absence.id);
    const storedOldObservation = store.state.observations.find((item) => item.id === oldObservation.id);
    storedOldObservation.holder = "engine-b";
    observedCodes.push(
      captureCode(
        () => store.authorizeDispatch("engine-b", leaseB.epoch, effectId, oldObservation.id),
        "OBSERVATION_EPOCH_MISMATCH",
      ),
    );
  }

  {
    const { store, external, effectId } = makeEffect("different-observation-holder");
    const lease = activeLease(store, "engine-a");
    const observation = recordCurrentObservation(store, external, "engine-a", lease.epoch, effectId);
    store.state.observations.find((item) => item.id === observation.id).holder = "engine-b";
    observedCodes.push(
      captureCode(
        () => store.authorizeDispatch("engine-a", lease.epoch, effectId, observation.id),
        "OBSERVATION_HOLDER_MISMATCH",
      ),
    );
  }

  {
    const { store, external, effectId } = makeEffect("expired-observation");
    const lease = activeLease(store, "engine-a");
    const observation = recordCurrentObservation(store, external, "engine-a", lease.epoch, effectId);
    store.advance(FIXTURE_OBSERVATION_MAX_AGE_MS + 1);
    observedCodes.push(
      captureCode(
        () => store.authorizeDispatch("engine-a", lease.epoch, effectId, observation.id),
        "OBSERVATION_EXPIRED",
      ),
    );
  }

  let acceptedBoundaryAge;
  {
    const { store, external, effectId } = makeEffect("freshness-boundary");
    const lease = activeLease(store, "engine-a");
    const observation = recordCurrentObservation(store, external, "engine-a", lease.epoch, effectId);
    store.advance(FIXTURE_OBSERVATION_MAX_AGE_MS);
    const dispatch = store.authorizeDispatch("engine-a", lease.epoch, effectId, observation.id);
    acceptedBoundaryAge = store.state.now - observation.observedAt;
    assert.equal(dispatch.attempt, 1);
    assert.equal(acceptedBoundaryAge, FIXTURE_OBSERVATION_MAX_AGE_MS);
  }

  {
    const { store, external, effectId } = makeEffect("reused-observation");
    const lease = activeLease(store, "engine-a");
    const observation = recordCurrentObservation(store, external, "engine-a", lease.epoch, effectId);
    store.state.observations.find((item) => item.id === observation.id).consumedBy = {
      transition: "prior_dispatch",
      effectId,
      attempt: 1,
      epoch: lease.epoch,
    };
    observedCodes.push(
      captureCode(
        () => store.authorizeDispatch("engine-a", lease.epoch, effectId, observation.id),
        "OBSERVATION_ALREADY_CONSUMED",
      ),
    );
  }

  {
    const { store, external, effectId } = makeEffect("wrong-observation-attempt");
    const lease = activeLease(store, "engine-a");
    const observation = recordCurrentObservation(store, external, "engine-a", lease.epoch, effectId);
    store.state.observations.find((item) => item.id === observation.id).attempt += 1;
    observedCodes.push(
      captureCode(
        () => store.authorizeDispatch("engine-a", lease.epoch, effectId, observation.id),
        "OBSERVATION_ATTEMPT_MISMATCH",
      ),
    );
  }

  let postUnknownConsumedBy;
  {
    const { store, external, effectId } = makeEffect("unknown-retry", "idempotent_close");
    const lease = activeLease(store, "engine-a");
    const before = recordCurrentObservation(store, external, "engine-a", lease.epoch, effectId);
    dispatchOnce(
      store,
      external,
      "engine-a",
      lease.epoch,
      effectId,
      before,
      "ambiguous_without_visible_result",
    );
    const after = recordOutcomeObservation(store, external, "engine-a", lease.epoch, effectId);
    assert.equal(store.reconcileDecision("engine-a", lease.epoch, effectId, after.id).phase, "retryable");
    observedCodes.push(
      captureCode(
        () => store.authorizeDispatch("engine-a", lease.epoch, effectId, after.id),
        "OBSERVATION_ALREADY_CONSUMED",
      ),
    );
    const freshRetry = recordCurrentObservation(store, external, "engine-a", lease.epoch, effectId);
    assert.equal(
      dispatchOnce(store, external, "engine-a", lease.epoch, effectId, freshRetry, "apply"),
      "response_received_observation_still_required",
    );
    postUnknownConsumedBy = store.state.observations.find((item) => item.id === after.id).consumedBy;
  }

  assert.equal(postUnknownConsumedBy.transition, "reconcile");
  results.observationGuards = {
    freshnessWindowModelMs: FIXTURE_OBSERVATION_MAX_AGE_MS,
    acceptedBoundaryAgeModelMs: acceptedBoundaryAge,
    rejectedCodes: observedCodes,
    negativeCases: observedCodes.length,
    postUnknownRetryRequiredNewObservation: postUnknownConsumedBy.transition,
  };
}

function testAdr0014AdmissionAndOperationalLimits() {
  const denialCodes = [];

  {
    const store = new ModelStore();
    const external = new FakeExternal();
    const lease = activeLease(store);
    const effectSpec = spec({
      kind: "paseo.workspace_create",
      effectClass: "unique_create",
      target: { labels: "workspace-lifecycle-missing" },
      expectedExternal: null,
      desiredExternal: { workspaceId: "workspace-missing" },
    });
    const admitted = store.admit(
      envelope({ requestId: "workspace-lifecycle-missing", payload: { create: true } }),
      [effectSpec],
    );
    const effectId = admitted.command.effectIds[0];
    const observation = recordCurrentObservation(store, external, "engine-a", lease.epoch, effectId);
    denialCodes.push(
      captureCode(
        () => store.authorizeDispatch("engine-a", lease.epoch, effectId, observation.id),
        "WORKTREE_LIFECYCLE_ADMISSION_MISSING",
      ),
    );
    assert.equal(store.state.effects[effectId].phase, "needs_you");
    assert.equal(external.handoffCount(effectId), 0);
  }

  {
    const store = new ModelStore();
    const external = new FakeExternal();
    const lease = activeLease(store);
    const target = { labels: "workspace-lifecycle-approved" };
    const binding = { candidate: "candidate-1", base: "base-1", repositoryId: "repo-1" };
    const admission = approvedWorkspaceLifecycleAdmission(target, binding);
    const staleApproval = structuredClone(admission);
    staleApproval.approval.lifecycleDigest = "stale-lifecycle-digest";
    const effectSpec = spec({
      kind: "paseo.workspace_create",
      effectClass: "unique_create",
      target,
      binding,
      expectedExternal: null,
      desiredExternal: { workspaceId: "workspace-stale-approval" },
      workspaceLifecycleAdmission: staleApproval,
    });
    const admitted = store.admit(
      envelope({ requestId: "workspace-lifecycle-stale", payload: { create: true } }),
      [effectSpec],
    );
    const effectId = admitted.command.effectIds[0];
    const observation = recordCurrentObservation(store, external, "engine-a", lease.epoch, effectId);
    denialCodes.push(
      captureCode(
        () => store.authorizeDispatch("engine-a", lease.epoch, effectId, observation.id),
        "WORKTREE_LIFECYCLE_APPROVAL_REQUIRED",
      ),
    );
    assert.equal(store.state.effects[effectId].phase, "needs_you");
    assert.equal(external.handoffCount(effectId), 0);
  }

  let approvedWorkspaceHandoffs;
  {
    const store = new ModelStore();
    const external = new FakeExternal();
    const lease = activeLease(store);
    const target = { labels: "workspace-lifecycle-approved" };
    const binding = { candidate: "candidate-1", base: "base-1", repositoryId: "repo-1" };
    const effectSpec = spec({
      kind: "paseo.workspace_create",
      effectClass: "unique_create",
      target,
      binding,
      expectedExternal: null,
      desiredExternal: { workspaceId: "workspace-approved" },
      workspaceLifecycleAdmission: approvedWorkspaceLifecycleAdmission(target, binding),
    });
    const admitted = store.admit(
      envelope({ requestId: "workspace-lifecycle-approved", payload: { create: true } }),
      [effectSpec],
    );
    const effectId = admitted.command.effectIds[0];
    const observation = recordCurrentObservation(store, external, "engine-a", lease.epoch, effectId);
    dispatchOnce(store, external, "engine-a", lease.epoch, effectId, observation, "apply");
    approvedWorkspaceHandoffs = external.handoffCount(effectId);
    assert.equal(approvedWorkspaceHandoffs, 1);
  }

  {
    const store = new ModelStore();
    const external = new FakeExternal();
    const lease = activeLease(store);
    const effectSpec = spec({
      kind: "git.remote_ref_push",
      effectClass: "conditional_update",
      target: { repositoryId: "repo-1", ref: "refs/heads/over-limit" },
      expectedExternal: { oid: "old" },
      desiredExternal: { oid: "candidate-1" },
    });
    const admitted = store.admit(
      envelope({ requestId: "operational-limit-exceeded", payload: { push: true } }),
      [effectSpec],
    );
    const effectId = admitted.command.effectIds[0];
    external.seed(effectSpec.target, effectSpec.expectedExternal);
    const overLimit = {
      ...FIXTURE_OPERATIONAL_FACTS,
      processCount: FIXTURE_OPERATIONAL_LIMITS.maxProcessCount + 1,
    };
    const observation = recordCurrentObservation(store, external, "engine-a", lease.epoch, effectId, {
      operationalFacts: overLimit,
    });
    denialCodes.push(
      captureCode(
        () => store.authorizeDispatch("engine-a", lease.epoch, effectId, observation.id),
        "OPERATIONAL_LIMIT_EXCEEDED",
      ),
    );
    assert.equal(store.state.effects[effectId].phase, "needs_you");
    assert.equal(external.handoffCount(effectId), 0);
  }

  {
    const store = new ModelStore();
    const external = new FakeExternal();
    const lease = activeLease(store);
    const effectSpec = spec({
      kind: "github.pull_request_create",
      effectClass: "unique_create",
      target: { repositoryId: "repo-1", marker: "missing-operational-limits" },
      expectedExternal: null,
      desiredExternal: { pr: 77 },
      operationalLimits: null,
    });
    const admitted = store.admit(
      envelope({ requestId: "operational-limits-missing", payload: { create: true } }),
      [effectSpec],
    );
    const effectId = admitted.command.effectIds[0];
    const observation = recordCurrentObservation(store, external, "engine-a", lease.epoch, effectId);
    denialCodes.push(
      captureCode(
        () => store.authorizeDispatch("engine-a", lease.epoch, effectId, observation.id),
        "OPERATIONAL_LIMITS_MISSING",
      ),
    );
    assert.equal(store.state.effects[effectId].phase, "needs_you");
  }

  {
    const store = new ModelStore();
    const external = new FakeExternal();
    const lease = activeLease(store);
    const effectSpec = spec({
      kind: "github.pull_request_create",
      effectClass: "unique_create",
      target: { repositoryId: "repo-1", marker: "missing-operational-facts" },
      expectedExternal: null,
      desiredExternal: { pr: 78 },
    });
    const admitted = store.admit(
      envelope({ requestId: "operational-facts-missing", payload: { create: true } }),
      [effectSpec],
    );
    const effectId = admitted.command.effectIds[0];
    const observation = recordCurrentObservation(store, external, "engine-a", lease.epoch, effectId, {
      operationalFacts: null,
    });
    denialCodes.push(
      captureCode(
        () => store.authorizeDispatch("engine-a", lease.epoch, effectId, observation.id),
        "OPERATIONAL_FACTS_MISSING",
      ),
    );
    assert.equal(store.state.effects[effectId].phase, "needs_you");
  }

  let periodicLimitPhase;
  {
    const store = new ModelStore();
    const external = new FakeExternal();
    const lease = activeLease(store);
    const effectSpec = spec({
      kind: "paseo.agent_continue",
      effectClass: "nonrepeatable_progress",
      target: { agentId: "agent-periodic-limit", promptMarker: "turn-1" },
      expectedExternal: { state: "ready" },
      desiredExternal: { state: "accepted" },
    });
    const admitted = store.admit(
      envelope({ requestId: "periodic-operational-limit", payload: { continue: true } }),
      [effectSpec],
    );
    const effectId = admitted.command.effectIds[0];
    external.seed(effectSpec.target, effectSpec.expectedExternal);
    const before = recordCurrentObservation(store, external, "engine-a", lease.epoch, effectId, {
      status: "ready",
    });
    dispatchOnce(store, external, "engine-a", lease.epoch, effectId, before, "apply");
    const overLimit = {
      ...FIXTURE_OPERATIONAL_FACTS,
      elapsedMs: FIXTURE_OPERATIONAL_LIMITS.maxElapsedMs + 1,
    };
    const during = recordOutcomeObservation(store, external, "engine-a", lease.epoch, effectId, {
      operationalFacts: overLimit,
    });
    periodicLimitPhase = store.reconcileDecision("engine-a", lease.epoch, effectId, during.id).phase;
    assert.equal(periodicLimitPhase, "needs_you");
  }

  const externallyMutatingKinds = EFFECT_CATALOG.filter(
    ([, effectClass]) => EFFECT_CLASSES[effectClass].mutatesExternalState,
  ).length;
  assert.ok(externallyMutatingKinds > 0);
  results.adr0014Gates = {
    denialCodes,
    approvedWorkspaceHandoffs,
    periodicLimitPhase,
    externallyMutatingKindsRequiringFiniteLimits: externallyMutatingKinds,
  };
}

function testUnknownOutcomesAndRetryClasses() {
  const store = new ModelStore();
  const external = new FakeExternal();
  const lease = activeLease(store);
  const specs = [
    spec({
      kind: "paseo.workspace_create",
      effectClass: "unique_create",
      target: { labels: "project-1/run-7/workspace" },
      expectedExternal: null,
      desiredExternal: { workspaceId: "native-workspace-1" },
      workspaceLifecycleAdmission: emptyWorkspaceLifecycleAdmission(),
    }),
    spec({
      kind: "paseo.agent_continue",
      effectClass: "nonrepeatable_progress",
      target: { agentId: "native-agent-1", promptMarker: "correction-2" },
      expectedExternal: { state: "ready" },
      desiredExternal: { state: "accepted" },
    }),
    spec({
      kind: "git.remote_ref_delete",
      effectClass: "destructive_terminal",
      target: { repositoryId: "repo-1", ref: "refs/heads/run-7" },
      expectedExternal: { oid: "candidate-1", ownerNonce: "nonce-1" },
      desiredExternal: { presence: "absent" },
    }),
    spec({
      kind: "git.local_ref_delete",
      effectClass: "destructive_terminal",
      target: { commonDirIdentity: "common-1", ref: "refs/heads/run-7-local" },
      expectedExternal: { oid: "candidate-1", ownerNonce: "nonce-1" },
      desiredExternal: { presence: "absent" },
    }),
    spec({
      kind: "paseo.agent_archive",
      effectClass: "idempotent_close",
      target: { agentId: "native-agent-2", ownerNonce: "nonce-1" },
      expectedExternal: { status: "active", archivedAt: null, processAbsent: false },
      desiredExternal: { status: "closed", archivedAt: "recorded", processAbsent: true },
    }),
    spec({
      kind: "paseo.workspace_archive",
      effectClass: "idempotent_close",
      target: { workspaceId: "native-workspace-budget", ownerNonce: "nonce-1" },
      expectedExternal: { status: "active" },
      desiredExternal: { status: "archived" },
      maxAttempts: 1,
    }),
  ];
  const admitted = store.admit(envelope({ requestId: "unknown-matrix", payload: { matrix: true } }), specs);
  const [createId, progressId, deleteAppliedId, deletePresentId, closeId, exhaustedId] = admitted.command.effectIds;
  external.seed(specs[1].target, specs[1].expectedExternal);
  external.seed(specs[2].target, specs[2].expectedExternal);
  external.seed(specs[3].target, specs[3].expectedExternal);
  external.seed(specs[4].target, specs[4].expectedExternal);
  external.seed(specs[5].target, specs[5].expectedExternal);

  const createObs = recordCurrentObservation(store, external, "engine-a", lease.epoch, createId);
  assert.equal(dispatchOnce(store, external, "engine-a", lease.epoch, createId, createObs, "apply_then_lose_response"), "LOST_RESPONSE");
  const createAfter = recordOutcomeObservation(store, external, "engine-a", lease.epoch, createId);
  assert.equal(store.reconcileDecision("engine-a", lease.epoch, createId, createAfter.id).phase, "complete");

  const progressObs = recordCurrentObservation(store, external, "engine-a", lease.epoch, progressId, { status: "ready" });
  assert.equal(dispatchOnce(store, external, "engine-a", lease.epoch, progressId, progressObs, "ambiguous_without_visible_result"), "LOST_RESPONSE");
  const progressAfter = recordOutcomeObservation(store, external, "engine-a", lease.epoch, progressId, { status: "ready" });
  const progressDecision = store.reconcileDecision("engine-a", lease.epoch, progressId, progressAfter.id);
  assert.equal(progressDecision.phase, "needs_you");

  const appliedEffect = store.state.effects[deleteAppliedId];
  const appliedObs = recordCurrentObservation(store, external, "engine-a", lease.epoch, deleteAppliedId);
  assert.equal(
    dispatchOnce(
      store,
      external,
      "engine-a",
      lease.epoch,
      deleteAppliedId,
      appliedObs,
      "apply_then_lose_response",
      destructiveGate(appliedEffect, lease.epoch),
    ),
    "LOST_RESPONSE",
  );
  const appliedAfter = recordOutcomeObservation(store, external, "engine-a", lease.epoch, deleteAppliedId);
  assert.equal(store.reconcileDecision("engine-a", lease.epoch, deleteAppliedId, appliedAfter.id).phase, "complete");

  const presentEffect = store.state.effects[deletePresentId];
  const presentObs = recordCurrentObservation(store, external, "engine-a", lease.epoch, deletePresentId);
  assert.equal(
    dispatchOnce(
      store,
      external,
      "engine-a",
      lease.epoch,
      deletePresentId,
      presentObs,
      "ambiguous_without_visible_result",
      destructiveGate(presentEffect, lease.epoch),
    ),
    "LOST_RESPONSE",
  );
  const presentAfter = recordOutcomeObservation(store, external, "engine-a", lease.epoch, deletePresentId);
  const presentDecision = store.reconcileDecision("engine-a", lease.epoch, deletePresentId, presentAfter.id);
  assert.equal(presentDecision.phase, "needs_you");

  const closeObs = recordCurrentObservation(store, external, "engine-a", lease.epoch, closeId);
  assert.equal(dispatchOnce(store, external, "engine-a", lease.epoch, closeId, closeObs, "ambiguous_without_visible_result"), "LOST_RESPONSE");
  const closeAfter = recordOutcomeObservation(store, external, "engine-a", lease.epoch, closeId);
  assert.equal(store.reconcileDecision("engine-a", lease.epoch, closeId, closeAfter.id).phase, "retryable");
  const closeRetryObs = recordCurrentObservation(store, external, "engine-a", lease.epoch, closeId);
  assert.equal(dispatchOnce(store, external, "engine-a", lease.epoch, closeId, closeRetryObs, "apply"), "response_received_observation_still_required");
  const closeDoneObs = recordOutcomeObservation(store, external, "engine-a", lease.epoch, closeId);
  assert.equal(store.reconcileDecision("engine-a", lease.epoch, closeId, closeDoneObs.id).phase, "complete");

  const exhaustedObs = recordCurrentObservation(store, external, "engine-a", lease.epoch, exhaustedId);
  dispatchOnce(store, external, "engine-a", lease.epoch, exhaustedId, exhaustedObs, "ambiguous_without_visible_result");
  const exhaustedAfter = recordOutcomeObservation(store, external, "engine-a", lease.epoch, exhaustedId);
  const exhaustedDecision = store.reconcileDecision("engine-a", lease.epoch, exhaustedId, exhaustedAfter.id);
  assert.equal(exhaustedDecision.phase, "needs_you");
  assert.equal(exhaustedDecision.anomaly, "EFFECT_BUDGET_EXHAUSTED");
  throwsCode(
    () => store.authorizeDispatch("engine-a", lease.epoch, exhaustedId, exhaustedAfter.id),
    "EFFECT_NOT_DISPATCHABLE",
  );

  results.unknownOutcomes = {
    uniqueCreateAdoptedAfterLostResponse: true,
    nonrepeatableProgressParked: progressDecision.anomaly,
    destructiveAbsenceAdopted: true,
    destructivePresentParked: presentDecision.anomaly,
    idempotentCloseHandoffs: external.handoffCount(closeId),
    exhaustedRetryBudget: exhaustedDecision.anomaly,
    nonrepeatableProgressHandoffs: external.handoffCount(progressId),
    destructivePresentHandoffs: external.handoffCount(deletePresentId),
  };
}

function testObservationBindingAndTerminalDrift() {
  const store = new ModelStore();
  const external = new FakeExternal();
  const lease = activeLease(store);
  const oldMergeSpec = spec({
    kind: "github.pull_request_merge",
    effectClass: "conditional_update",
    target: { repositoryId: "repo-1", pullRequest: 42 },
    expectedExternal: { head: "candidate-1", base: "base-1", merged: false },
    desiredExternal: { head: "candidate-1", base: "base-1", merged: true, mergeCommit: "merge-1" },
  });
  const admitted = store.admit(envelope({ requestId: "sha-binding-old", payload: { merge: true } }), [oldMergeSpec]);
  const oldEffectId = admitted.command.effectIds[0];
  external.seed(oldMergeSpec.target, oldMergeSpec.expectedExternal);
  const oldObservation = recordCurrentObservation(store, external, "engine-a", lease.epoch, oldEffectId);
  store.changeRunBinding("run:run-7", {
    candidate: "candidate-2",
    base: "base-2",
    repositoryId: "repo-1",
  });
  throwsCode(
    () => store.authorizeDispatch("engine-a", lease.epoch, oldEffectId, oldObservation.id),
    "EFFECT_BINDING_STALE",
  );

  const mergeSpec = spec({
    kind: "github.pull_request_merge",
    effectClass: "conditional_update",
    target: { repositoryId: "repo-1", pullRequest: 42 },
    expectedExternal: { head: "candidate-2", base: "base-2", merged: false },
    desiredExternal: { head: "candidate-2", base: "base-2", merged: true, mergeCommit: "merge-2" },
    binding: { candidate: "candidate-2", base: "base-2", repositoryId: "repo-1" },
  });
  external.seed(mergeSpec.target, mergeSpec.expectedExternal);
  const current = store.admit(
    envelope({ requestId: "sha-binding-current", payload: { merge: true }, expectedVersion: 9 }),
    [mergeSpec],
  );
  const effectId = current.command.effectIds[0];
  const currentObservation = recordCurrentObservation(store, external, "engine-a", lease.epoch, effectId);
  assert.equal(dispatchOnce(store, external, "engine-a", lease.epoch, effectId, currentObservation, "apply"), "response_received_observation_still_required");
  const doneObservation = recordOutcomeObservation(store, external, "engine-a", lease.epoch, effectId);
  assert.equal(store.reconcileDecision("engine-a", lease.epoch, effectId, doneObservation.id).phase, "complete");

  external.seed(mergeSpec.target, { head: "candidate-3", base: "base-2", merged: false });
  const driftObservation = recordDriftObservation(store, external, "engine-a", lease.epoch, effectId);
  const terminal = store.recordTerminalDrift("engine-a", lease.epoch, effectId, driftObservation.id);
  assert.equal(terminal.phase, "complete");
  assert.equal(terminal.anomaly, "TERMINAL_EXTERNAL_DRIFT");
  assert.equal(external.handoffCount(effectId), 1);

  external.unavailable.add(canonical({ repositoryId: "repo-1", pullRequest: 43 }));
  const unavailableSpec = spec({
    kind: "github.pull_request_create",
    effectClass: "unique_create",
    target: { repositoryId: "repo-1", pullRequest: 43 },
    expectedExternal: null,
    desiredExternal: { head: "candidate-2" },
    binding: { candidate: "candidate-2", base: "base-2", repositoryId: "repo-1" },
  });
  const next = store.admit(
    envelope({ requestId: "external-unavailable", payload: { create: true }, expectedVersion: 10 }),
    [unavailableSpec],
  );
  const unavailableId = next.command.effectIds[0];
  const unavailableObservation = recordCurrentObservation(store, external, "engine-a", lease.epoch, unavailableId);
  assert.equal(unavailableObservation.status, "unavailable");
  throwsCode(
    () => store.authorizeDispatch("engine-a", lease.epoch, unavailableId, unavailableObservation.id),
    "EXTERNAL_UNAVAILABLE",
  );

  results.externalEvidence = {
    candidateBaseChangeInvalidatedOldEvidence: true,
    repositoryIdentityBound: true,
    terminalSuccessNotReexecutedAfterDrift: true,
    terminalDrift: terminal.anomaly,
    outageWasNotAbsence: true,
  };
}

function testTaskStoreHealthAndPreDispatchFailure() {
  const store = new ModelStore();
  const external = new FakeExternal();
  store.state.health.globalCommitMode = 1;
  const healthCodes = [
    captureCode(
      () => store.admit(envelope({ requestId: "unsafe-store", payload: { phase: "review" } }), []),
      "UNSAFE_GLOBAL_COMMIT_MODE",
    ),
  ];
  assert.equal(Object.keys(store.state.commands).length, 0);
  const unsafeCommandCounts = [Object.keys(store.state.commands).length];

  const unsafeSessionStore = new ModelStore();
  unsafeSessionStore.state.health.sessionCommitMode = 1;
  healthCodes.push(
    captureCode(
      () => unsafeSessionStore.admit(envelope({ requestId: "unsafe-session", payload: { phase: "review" } }), []),
      "UNSAFE_SESSION_COMMIT_MODE",
    ),
  );
  assert.equal(Object.keys(unsafeSessionStore.state.commands).length, 0);
  unsafeCommandCounts.push(Object.keys(unsafeSessionStore.state.commands).length);

  const resetStore = new ModelStore();
  resetStore.state.health.connectionState = "reset";
  healthCodes.push(
    captureCode(
      () => resetStore.admit(envelope({ requestId: "reset-store", payload: { phase: "review" } }), []),
      "TASKSTORE_CONNECTION_RESET",
    ),
  );
  assert.equal(Object.keys(resetStore.state.commands).length, 0);
  unsafeCommandCounts.push(Object.keys(resetStore.state.commands).length);

  const duplicateInitializationStore = new ModelStore();
  duplicateInitializationStore.state.health.initializationCount = 2;
  healthCodes.push(
    captureCode(
      () =>
        duplicateInitializationStore.admit(
          envelope({ requestId: "duplicate-initialization", payload: { phase: "review" } }),
          [],
        ),
      "TASKSTORE_DUPLICATE_INITIALIZATION",
    ),
  );
  assert.equal(Object.keys(duplicateInitializationStore.state.commands).length, 0);
  unsafeCommandCounts.push(Object.keys(duplicateInitializationStore.state.commands).length);

  store.state.health.globalCommitMode = 0;
  const lease = activeLease(store);
  const createSpec = spec({
    kind: "paseo.reviewer_create_with_prompt",
    effectClass: "unique_create",
    target: { labels: "review/run-7/candidate-1" },
    expectedExternal: null,
    desiredExternal: { reviewerId: "reviewer-1" },
  });
  const admitted = store.admit(envelope({ requestId: "pre-dispatch", payload: { review: true } }), [createSpec]);
  const effectId = admitted.command.effectIds[0];
  const observation = recordCurrentObservation(store, external, "engine-a", lease.epoch, effectId);
  assert.equal(
    dispatchOnce(store, external, "engine-a", lease.epoch, effectId, observation, "proven_pre_dispatch_failure"),
    "PROVEN_NO_EXTERNAL_HANDOFF",
  );
  assert.equal(external.handoffCount(effectId), 0);
  assert.equal(store.state.effects[effectId].phase, "failed_pre_dispatch");

  store.state.health.actualIdentity = "wrong-listener";
  healthCodes.push(
    captureCode(
      () => store.markUnknown("engine-a", lease.epoch, effectId),
      "TASKSTORE_IDENTITY_MISMATCH",
    ),
  );
  assert.equal(external.handoffCount(effectId), 0);

  const storeOnlyStore = new ModelStore();
  const storeOnlySpec = spec({
    kind: "taskstore.aggregate_write",
    effectClass: "store_only",
    target: { aggregateId: "run:run-7" },
    expectedExternal: { version: 7 },
    desiredExternal: { version: 8 },
  });
  const storeOnlyCommand = storeOnlyStore.admit(
    envelope({ requestId: "store-only-effect", payload: { phase: "building" } }),
    [storeOnlySpec],
  );
  const storeOnlyEffect = storeOnlyStore.state.effects[storeOnlyCommand.command.effectIds[0]];
  assert.equal(storeOnlyEffect.class, "store_only");
  assert.equal(storeOnlyEffect.phase, "complete");
  assert.equal(storeOnlyEffect.terminal, true);
  assert.equal(storeOnlyEffect.attempts, 0);

  results.taskStoreHealth = {
    rejectedHealthCodes: healthCodes.sort(),
    commandsPersistedUnderUnsafeHealth: unsafeCommandCounts.reduce((sum, count) => sum + count, 0),
    provenPreDispatchFailureHandoffs: external.handoffCount(effectId),
    storeOnly: {
      class: storeOnlyEffect.class,
      phase: storeOnlyEffect.phase,
      terminal: storeOnlyEffect.terminal,
      attempts: storeOnlyEffect.attempts,
    },
  };
}

function testMcpIngressScopeAndReplay() {
  const store = new ModelStore();
  const initialEffectCount = Object.keys(store.state.effects).length;
  const fixedScope = {
    projectId: "project-1",
    workspaceId: "workspace-2",
    taskId: "task-3",
    runId: "run-7",
    role: "task_agent",
  };
  const capability = {
    audience: "bridge-9",
    expiresAt: FIXED_TIME + 1_000,
    revoked: false,
    allowedCommands: ["run.report_candidate"],
  };

  function submitMcp(requestId, args, cap = capability) {
    const forbidden = ["projectId", "workspaceId", "taskId", "runId", "role", "path", "remote", "credential"];
    if (forbidden.some((field) => Object.hasOwn(args, field))) {
      throw new ContractError("MCP_CALLER_SELECTED_SCOPE");
    }
    if (cap.revoked || cap.expiresAt <= store.state.now || cap.audience !== "bridge-9") {
      throw new ContractError("MCP_CAPABILITY_INVALID");
    }
    if (!cap.allowedCommands.includes("run.report_candidate")) {
      throw new ContractError("MCP_COMMAND_FORBIDDEN");
    }
    return store.admit(
      {
        ...envelope({ requestId, payload: args, type: "run.report_candidate" }),
        scope: fixedScope,
      },
      [],
    );
  }

  throwsCode(
    () => submitMcp("mcp-scope", { candidate: "candidate-1", runId: "other-run" }),
    "MCP_CALLER_SELECTED_SCOPE",
  );
  const first = submitMcp("mcp-replay", { candidate: "candidate-1" });
  const replay = submitMcp("mcp-replay", { candidate: "candidate-1" });
  assert.equal(first.replay, false);
  assert.equal(replay.replay, true);
  throwsCode(
    () => submitMcp("mcp-replay", { candidate: "candidate-2" }),
    "IDEMPOTENCY_KEY_REUSED_WITH_DIFFERENT_PAYLOAD",
  );
  capability.revoked = true;
  throwsCode(
    () => submitMcp("mcp-revoked", { candidate: "candidate-1" }),
    "MCP_CAPABILITY_INVALID",
  );

  results.mcpIngress = {
    callerSelectedScopeRejected: true,
    engineDurableReplay: true,
    payloadConflictRejected: true,
    revokedCapabilityRejected: true,
    directEffectDelta: Object.keys(store.state.effects).length - initialEffectCount,
  };
}

function testForwardCompensation() {
  const store = new ModelStore();
  const external = new FakeExternal();
  const lease = activeLease(store);
  const createSpec = spec({
    kind: "github.pull_request_create",
    effectClass: "unique_create",
    target: { repositoryId: "repo-1", marker: "run-7" },
    expectedExternal: null,
    desiredExternal: { pr: 42, state: "open", ownerNonce: "nonce-1" },
  });
  const create = store.admit(envelope({ requestId: "create-pr", payload: { create: true } }), [createSpec]);
  const createId = create.command.effectIds[0];
  const originalCommandKey = create.command.commandKey;
  const absent = recordCurrentObservation(store, external, "engine-a", lease.epoch, createId);
  dispatchOnce(store, external, "engine-a", lease.epoch, createId, absent, "apply");
  const open = recordOutcomeObservation(store, external, "engine-a", lease.epoch, createId);
  assert.equal(store.reconcileDecision("engine-a", lease.epoch, createId, open.id).phase, "complete");
  const originalAuditCountBeforeCompensation = store.state.audits.filter(
    (item) => item.commandKey === originalCommandKey || item.effectId === createId,
  ).length;

  const closeSpec = spec({
    kind: "github.pull_request_close",
    effectClass: "idempotent_close",
    target: createSpec.target,
    expectedExternal: createSpec.desiredExternal,
    desiredExternal: { pr: 42, state: "closed", ownerNonce: "nonce-1" },
    compensationFor: createId,
  });
  const compensation = store.admit(
    envelope({ requestId: "cancel-pr", payload: { compensate: createId }, expectedVersion: 8 }),
    [closeSpec],
  );
  const closeId = compensation.command.effectIds[0];
  assert.equal(store.state.effects[closeId].compensationFor, createId);
  const exactOpen = recordCurrentObservation(store, external, "engine-a", lease.epoch, closeId);
  dispatchOnce(store, external, "engine-a", lease.epoch, closeId, exactOpen, "apply_then_lose_response");
  const closed = recordOutcomeObservation(store, external, "engine-a", lease.epoch, closeId);
  assert.equal(store.reconcileDecision("engine-a", lease.epoch, closeId, closed.id).phase, "complete");
  assert.equal(store.state.effects[createId].phase, "complete");

  const foreignSpec = spec({
    kind: "github.pull_request_close",
    effectClass: "idempotent_close",
    target: { repositoryId: "repo-foreign", marker: "run-7" },
    expectedExternal: { pr: 99, state: "open", ownerNonce: "foreign" },
    desiredExternal: { pr: 99, state: "closed", ownerNonce: "foreign" },
    compensationFor: createId,
  });
  const foreign = store.admit(
    envelope({ requestId: "foreign-compensation", payload: { compensate: createId }, expectedVersion: 9 }),
    [foreignSpec],
  );
  const foreignId = foreign.command.effectIds[0];
  external.seed(foreignSpec.target, { pr: 99, state: "open", ownerNonce: "somebody-else" });
  const foreignObservation = recordCurrentObservation(store, external, "engine-a", lease.epoch, foreignId);
  assert.equal(foreignObservation.status, "different");
  throwsCode(
    () => store.authorizeDispatch("engine-a", lease.epoch, foreignId, foreignObservation.id),
    "EXTERNAL_FACTS_AMBIGUOUS",
  );
  assert.equal(external.handoffCount(foreignId), 0);
  const originalAuditCountAfterCompensation = store.state.audits.filter(
    (item) => item.commandKey === originalCommandKey || item.effectId === createId,
  ).length;
  assert.equal(originalAuditCountAfterCompensation, originalAuditCountBeforeCompensation);

  results.compensation = {
    model: "forward_effect",
    originalEvidencePreserved: store.state.effects[createId].terminal,
    compensationEvidencePreserved: store.state.effects[closeId].terminal,
    foreignTargetHandoffs: external.handoffCount(foreignId),
    originalAuditEntriesBefore: originalAuditCountBeforeCompensation,
    originalAuditEntriesAfter: originalAuditCountAfterCompensation,
  };
}

function testCompositeSyncProjection() {
  const store = new ModelStore();
  const external = new FakeExternal();
  const lease = activeLease(store);
  const specs = [
    spec({
      kind: "taskstore.git_sync",
      effectClass: "conditional_update",
      target: { repositoryId: "organizer-1", ref: "refs/heads/main" },
      expectedExternal: { oid: "git-old" },
      desiredExternal: { oid: "git-new" },
    }),
    spec({
      kind: "taskstore.dolt_sync",
      effectClass: "conditional_update",
      target: { repositoryId: "organizer-1", ref: "refs/dolt/data" },
      expectedExternal: { oid: "dolt-old" },
      desiredExternal: { oid: "dolt-new" },
    }),
  ];
  const admitted = store.admit(envelope({ requestId: "sync", payload: { sync: true } }), specs);
  const [gitId, doltId] = admitted.command.effectIds;
  external.seed(specs[0].target, specs[0].expectedExternal);
  external.seed(specs[1].target, specs[1].expectedExternal);

  const gitBefore = recordCurrentObservation(store, external, "engine-a", lease.epoch, gitId);
  dispatchOnce(store, external, "engine-a", lease.epoch, gitId, gitBefore, "apply");
  const gitAfter = recordOutcomeObservation(store, external, "engine-a", lease.epoch, gitId);
  store.reconcileDecision("engine-a", lease.epoch, gitId, gitAfter.id);

  const doltBefore = recordCurrentObservation(store, external, "engine-a", lease.epoch, doltId);
  dispatchOnce(store, external, "engine-a", lease.epoch, doltId, doltBefore, "proven_pre_dispatch_failure");
  assert.equal(store.state.effects[gitId].phase, "complete");
  assert.equal(store.state.effects[doltId].phase, "failed_pre_dispatch");
  assert.equal(
    admitted.command.effectIds.every((id) => store.state.effects[id].phase === "complete"),
    false,
  );

  const doltRetry = recordCurrentObservation(store, external, "engine-a", lease.epoch, doltId);
  dispatchOnce(store, external, "engine-a", lease.epoch, doltId, doltRetry, "apply");
  const doltAfter = recordOutcomeObservation(store, external, "engine-a", lease.epoch, doltId);
  store.reconcileDecision("engine-a", lease.epoch, doltId, doltAfter.id);
  assert.equal(
    admitted.command.effectIds.every((id) => store.state.effects[id].phase === "complete"),
    true,
  );
  assert.equal(external.handoffCount(gitId), 1);
  assert.equal(external.handoffCount(doltId), 1);

  results.compositeSync = {
    atomicAcrossGitAndDolt: false,
    partialSuccessStayedDurable: true,
    successfulHalfRedispatched: false,
    gitHandoffs: external.handoffCount(gitId),
    doltHandoffs: external.handoffCount(doltId),
    combinedProjectionAfterBothEvidence: true,
  };
}

async function testCleanupLimitsAndOneUseGate() {
  const LIMITS = {
    recoveryEntries: 10_000,
    inspectedEntries: 25_000,
    aggregateBytes: 512 * 1024 * 1024,
    perFileBytes: 256 * 1024 * 1024,
    bufferBytes: 64 * 1024,
    phaseMs: 180_000,
    lifecycleMs: 480_000,
    supervisorMs: 540_000,
    rssGrowthBytes: 192 * 1024 * 1024,
    retentionMs: 7 * 24 * 60 * 60 * 1000,
    freeSpaceBasisPoints: 1_000,
    fullRevalidations: 5,
  };
  function admitRecovery(observed) {
    for (const [key, limit] of Object.entries(LIMITS)) {
      if (key === "freeSpaceBasisPoints") {
        if (observed[key] < limit) throw new ContractError("RECOVERY_LIMIT_EXCEEDED");
      } else if (observed[key] > limit) throw new ContractError("RECOVERY_LIMIT_EXCEEDED");
    }
    if (observed.fullRevalidations !== LIMITS.fullRevalidations) {
      throw new ContractError("RECOVERY_GATE_COUNT_MISMATCH");
    }
    return digest({ owner: "project-1/run-7/nonce-1", limits: observed });
  }
  const exact = admitRecovery(LIMITS);
  throwsCode(
    () => admitRecovery({ ...LIMITS, aggregateBytes: LIMITS.aggregateBytes + 1 }),
    "RECOVERY_LIMIT_EXCEEDED",
  );
  throwsCode(
    () => admitRecovery({ ...LIMITS, freeSpaceBasisPoints: LIMITS.freeSpaceBasisPoints - 1 }),
    "RECOVERY_LIMIT_EXCEEDED",
  );

  const store = new ModelStore();
  const external = new FakeExternal();
  const lease = activeLease(store);
  const cleanupSpec = spec({
    kind: "git.worktree_remove",
    effectClass: "destructive_terminal",
    target: { filesystemId: "fs-1", pathIdentity: "owned-worktree-1", ownerNonce: "nonce-1" },
    expectedExternal: { registration: "owned", artifactDigest: exact },
    desiredExternal: { presence: "absent" },
  });
  const admitted = store.admit(envelope({ requestId: "cleanup", payload: { cleanup: true } }), [cleanupSpec]);
  const effectId = admitted.command.effectIds[0];
  external.seed(cleanupSpec.target, cleanupSpec.expectedExternal);
  const observation = recordCurrentObservation(store, external, "engine-a", lease.epoch, effectId);
  const effect = store.state.effects[effectId];
  const gate = destructiveGate(effect, lease.epoch);
  assert.equal(
    dispatchOnce(
      store,
      external,
      "engine-a",
      lease.epoch,
      effectId,
      observation,
      "proven_pre_dispatch_failure",
      gate,
    ),
    "PROVEN_NO_EXTERNAL_HANDOFF",
  );
  assert.equal(gate.consumed, true);
  const retryObservation = recordCurrentObservation(store, external, "engine-a", lease.epoch, effectId);
  throwsCode(
    () => store.authorizeDispatch("engine-a", lease.epoch, effectId, retryObservation.id, gate),
    "DESTRUCTIVE_GATE_REQUIRED",
  );
  const refusal = store.recordRefusal("engine-a", lease.epoch, effectId, "DESTRUCTIVE_GATE_REQUIRED");
  assert.equal(refusal.phase, "refused_before_dispatch");
  assert.equal(refusal.attempted, false);
  const freshRetryObservation = recordCurrentObservation(store, external, "engine-a", lease.epoch, effectId);
  const freshGate = destructiveGate(store.state.effects[effectId], lease.epoch);
  dispatchOnce(
    store,
    external,
    "engine-a",
    lease.epoch,
    effectId,
    freshRetryObservation,
    "apply_then_lose_response",
    freshGate,
  );
  const after = recordOutcomeObservation(store, external, "engine-a", lease.epoch, effectId);
  assert.equal(store.reconcileDecision("engine-a", lease.epoch, effectId, after.id).phase, "complete");

  const root = await mkdtemp(join(tmpdir(), "director-m010-contract-"));
  const sentinel = join(root, "owned-sentinel");
  await writeFile(sentinel, "owned\n", { mode: 0o600 });
  await rm(root, { recursive: true });
  let rootAbsent = false;
  try {
    await access(root);
  } catch (error) {
    if (error.code === "ENOENT") rootAbsent = true;
    else throw error;
  }
  assert.equal(rootAbsent, true);

  results.cleanupRecovery = {
    policy: {
      recoveryEntries: 10_000,
      inspectedEntries: 25_000,
      aggregateMiB: 512,
      perFileMiB: 256,
      bufferKiB: 64,
      phaseSeconds: 180,
      lifecycleSeconds: 480,
      supervisorSeconds: 540,
      rssGrowthMiB: 192,
      retentionDays: 7,
      freeSpaceFloorPercent: 10,
      fullRevalidations: 5,
    },
    exactLimitsAccepted: true,
    expansionsRejected: true,
    oneUseDestructiveGate: true,
    lostDeleteAdoptedFromAbsence: true,
    temporaryRootAbsent: rootAbsent,
  };
}

async function runCleanupLimitsAndOneUseGate() {
  await testCleanupLimitsAndOneUseGate();
}

function testCatalogCoverage() {
  const seen = new Set();
  for (const [kind, effectClass] of EFFECT_CATALOG) {
    assert.equal(typeof kind, "string");
    assert.ok(EFFECT_CLASSES[effectClass]);
    assert.equal(seen.has(kind), false);
    seen.add(kind);
  }
  const required = [
    "paseo.workspace_create",
    "paseo.task_agent_create_with_prompt",
    "paseo.reviewer_create_with_prompt",
    "paseo.agent_continue",
    "paseo.agent_archive",
    "git.remote_ref_push",
    "github.pull_request_create",
    "github.pull_request_merge",
    "git.remote_ref_delete",
    "git.worktree_remove",
    "taskstore.git_sync",
    "taskstore.dolt_sync",
    "taskstore.backup_publish",
    "taskstore.schema_migrate",
  ];
  for (const kind of required) assert.ok(seen.has(kind), `catalog missing ${kind}`);

  const store = new ModelStore();
  const external = new FakeExternal();
  const lease = activeLease(store);
  const branchSpec = spec({
    kind: "git.branch_create",
    effectClass: "conditional_update",
    target: { repositoryId: "repo-1", ref: "refs/heads/new-run-7" },
    expectedExternal: { presence: "absent" },
    desiredExternal: { oid: "candidate-1" },
  });
  const admitted = store.admit(envelope({ requestId: "absent-ref-lease", payload: { create: true } }), [branchSpec]);
  const effectId = admitted.command.effectIds[0];
  const absent = recordCurrentObservation(store, external, "engine-a", lease.epoch, effectId);
  dispatchOnce(store, external, "engine-a", lease.epoch, effectId, absent, "apply");
  const desired = recordOutcomeObservation(store, external, "engine-a", lease.epoch, effectId);
  assert.equal(store.reconcileDecision("engine-a", lease.epoch, effectId, desired.id).phase, "complete");
  results.catalog = {
    effectClasses: Object.keys(EFFECT_CLASSES).length,
    effectKinds: EFFECT_CATALOG.length,
    duplicateKinds: 0,
    requiredPlanEffectsCovered: required.length,
    absentRefLeaseCovered: true,
  };
}

async function main() {
  testCommandIdentityAndTransactions();
  testCrashBoundaryMatrix();
  testLeaseFencingAndTakeover();
  testObservationAuthorizationGuards();
  testAdr0014AdmissionAndOperationalLimits();
  testUnknownOutcomesAndRetryClasses();
  testObservationBindingAndTerminalDrift();
  testTaskStoreHealthAndPreDispatchFailure();
  testMcpIngressScopeAndReplay();
  testForwardCompensation();
  testCompositeSyncProjection();
  await runCleanupLimitsAndOneUseGate();
  testCatalogCoverage();

  const report = {
    task: "dir-m0.10",
    outcome: "go",
    scope: "deterministic contract model; Linux-only Director 1.0",
    modelVersion: 1,
    results,
    totals: {
      testGroups: Object.keys(results).length,
      failedAssertions: 0,
      liveExternalEffects: 0,
      productImplementationFiles: 0,
    },
  };
  process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
}

main().catch((error) => {
  process.stderr.write(`${error.stack ?? error}\n`);
  process.exitCode = 1;
});
