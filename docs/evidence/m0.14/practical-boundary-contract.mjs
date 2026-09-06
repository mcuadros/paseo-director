#!/usr/bin/env node

import { createHash } from "node:crypto";

if (process.argv.length !== 2) {
  throw new Error("Usage: node docs/evidence/m0.14/practical-boundary-contract.mjs");
}
if (process.platform !== "linux" || process.arch !== "x64") {
  throw new Error("This practical-boundary contract supports Linux x86_64 only");
}

const scope = Object.freeze({
  projectId: "project-fixture",
  workspaceId: "workspace-fixture",
  taskId: "task-fixture",
  runId: "run-fixture",
});

const operationalPolicy = Object.freeze({
  minFreePercent: 10,
  maxAggregateWorkspaceBytes: 1_048_576,
  maxProcesses: 32,
  maxMemoryBytes: 268_435_456,
  maxElapsedMs: 60_000,
});

function requireAssertion(condition, message) {
  if (!condition) throw new Error(message);
}

function normalizeCommands(value) {
  if (typeof value === "string") return value.trim() ? [value.trim()] : [];
  if (!Array.isArray(value)) return [];
  return value
    .filter((command) => typeof command === "string" && command.trim())
    .map((command) => command.trim());
}

function lifecycleEnvelope(config) {
  const worktree = config?.worktree;
  const terminals = Array.isArray(worktree?.terminals)
    ? worktree.terminals
        .map((entry) => (typeof entry?.command === "string" ? entry.command.trim() : ""))
        .filter(Boolean)
    : [];
  const portScript =
    typeof worktree?.servicePorts?.portScript === "string" &&
    worktree.servicePorts.portScript.trim()
      ? [worktree.servicePorts.portScript.trim()]
      : [];
  return {
    setup: normalizeCommands(worktree?.setup),
    teardown: normalizeCommands(worktree?.teardown),
    terminals,
    portScript,
  };
}

function lifecycleDigest(config) {
  return createHash("sha256").update(JSON.stringify(lifecycleEnvelope(config))).digest("hex");
}

function sameScope(left, right) {
  return Object.keys(scope).every((key) => left?.[key] === right?.[key]);
}

function evaluateLifecycle(config, approval = null) {
  const envelope = lifecycleEnvelope(config);
  const configuredSurfaces = Object.entries(envelope).filter(([, commands]) => commands.length > 0);
  const commandCount = configuredSurfaces.reduce((total, [, commands]) => total + commands.length, 0);
  const summary = {
    configuredSurfaceCount: configuredSurfaces.length,
    commandCount,
    commandsExecuted: 0,
    automaticExecutionAuthorized: false,
  };
  if (configuredSurfaces.length === 0) {
    return { decision: "admit", executionMode: "no_lifecycle_commands", ...summary };
  }
  if (!approval) {
    return {
      decision: "park",
      route: "needs_you",
      reason: "lifecycle_human_approval_required",
      ...summary,
    };
  }
  if (
    approval.actor !== "human" ||
    approval.source !== "authenticated_engine_command" ||
    approval.lifecycleDigest !== lifecycleDigest(config) ||
    !sameScope(approval.scope, scope)
  ) {
    return {
      decision: "park",
      route: "needs_you",
      reason: "lifecycle_human_approval_invalid",
      ...summary,
    };
  }
  return {
    decision: "admit",
    executionMode: "explicit_human_approval",
    approvalActor: "human",
    ...summary,
  };
}

const limitFields = [
  "freePercent",
  "aggregateWorkspaceBytes",
  "processes",
  "memoryBytes",
  "elapsedMs",
];

function parkForLimit(reason) {
  return {
    decision: "park",
    route: "needs_you",
    reason,
    launchAllowed: false,
    continueAllowed: false,
    cleanupAuthorized: false,
    enforcement: "operational",
    hostileProviderGuarantee: false,
  };
}

function evaluateOperationalLimits(observation) {
  if (
    limitFields.some(
      (field) =>
        typeof observation?.[field] !== "number" || !Number.isFinite(observation[field]),
    )
  ) {
    return parkForLimit("resource_observation_missing");
  }
  if (observation.freePercent < operationalPolicy.minFreePercent) {
    return parkForLimit("disk_free_floor_exceeded");
  }
  if (observation.aggregateWorkspaceBytes > operationalPolicy.maxAggregateWorkspaceBytes) {
    return parkForLimit("aggregate_workspace_bytes_exceeded");
  }
  if (observation.processes > operationalPolicy.maxProcesses) {
    return parkForLimit("process_limit_exceeded");
  }
  if (observation.memoryBytes > operationalPolicy.maxMemoryBytes) {
    return parkForLimit("memory_limit_exceeded");
  }
  if (observation.elapsedMs > operationalPolicy.maxElapsedMs) {
    return parkForLimit("elapsed_time_limit_exceeded");
  }
  return {
    decision: "continue",
    route: null,
    reason: null,
    launchAllowed: true,
    continueAllowed: true,
    cleanupAuthorized: false,
    enforcement: "operational",
    hostileProviderGuarantee: false,
  };
}

const lifecycleFixtures = {
  setup: { worktree: { setup: ["fixture setup command"] } },
  teardown: { worktree: { teardown: ["fixture teardown command"] } },
  terminals: { worktree: { terminals: [{ command: "fixture terminal command" }] } },
  portScript: { worktree: { servicePorts: { portScript: "fixture port command" } } },
};
const lifecycleAssertions = [];

const empty = evaluateLifecycle({});
requireAssertion(empty.decision === "admit", "Empty lifecycle configuration did not admit");
requireAssertion(empty.commandCount === 0, "Empty lifecycle configuration contained a command");
lifecycleAssertions.push("empty_lifecycle_admitted_without_execution");

for (const [surface, config] of Object.entries(lifecycleFixtures)) {
  const result = evaluateLifecycle(config);
  requireAssertion(result.decision === "park", `${surface} did not park without approval`);
  requireAssertion(result.route === "needs_you", `${surface} did not route to Needs you`);
  requireAssertion(result.commandCount === 1, `${surface} command was not detected`);
  requireAssertion(result.commandsExecuted === 0, `${surface} command executed during admission`);
  requireAssertion(
    !JSON.stringify(result).includes("fixture"),
    `${surface} result exposed command content`,
  );
  lifecycleAssertions.push(`${surface}_requires_exact_human_approval`);
}

const combinedLifecycle = {
  worktree: {
    setup: lifecycleFixtures.setup.worktree.setup,
    teardown: lifecycleFixtures.teardown.worktree.teardown,
    terminals: lifecycleFixtures.terminals.worktree.terminals,
    servicePorts: lifecycleFixtures.portScript.worktree.servicePorts,
  },
};
const approval = {
  actor: "human",
  source: "authenticated_engine_command",
  scope,
  lifecycleDigest: lifecycleDigest(combinedLifecycle),
};
const approved = evaluateLifecycle(combinedLifecycle, approval);
requireAssertion(approved.decision === "admit", "Exact human approval did not admit lifecycle");
requireAssertion(
  approved.executionMode === "explicit_human_approval",
  "Approved lifecycle was classified as automatic",
);
requireAssertion(approved.commandCount === 4, "Approved lifecycle surface count changed");
requireAssertion(approved.commandsExecuted === 0, "Admission contract executed lifecycle commands");
lifecycleAssertions.push("exact_human_scope_and_digest_admitted_without_automatic_execution");

for (const mutation of [
  { ...approval, actor: "system" },
  { ...approval, source: "agent_payload" },
  { ...approval, lifecycleDigest: "0".repeat(64) },
  { ...approval, scope: { ...scope, runId: "another-run" } },
]) {
  const result = evaluateLifecycle(combinedLifecycle, mutation);
  requireAssertion(result.decision === "park", "Invalid lifecycle approval was accepted");
  requireAssertion(result.route === "needs_you", "Invalid approval did not route to Needs you");
  requireAssertion(result.commandsExecuted === 0, "Invalid approval executed lifecycle commands");
}
lifecycleAssertions.push("agent_supplied_non_human_stale_and_cross_scope_approvals_parked");

const changedLifecycle = structuredClone(combinedLifecycle);
changedLifecycle.worktree.setup.push("changed fixture command");
const changed = evaluateLifecycle(changedLifecycle, approval);
requireAssertion(changed.decision === "park", "Changed lifecycle reused stale approval");
requireAssertion(changed.reason === "lifecycle_human_approval_invalid", "Stale approval reason changed");
lifecycleAssertions.push("configuration_change_invalidated_prior_approval");

const atLimit = {
  freePercent: operationalPolicy.minFreePercent,
  aggregateWorkspaceBytes: operationalPolicy.maxAggregateWorkspaceBytes,
  processes: operationalPolicy.maxProcesses,
  memoryBytes: operationalPolicy.maxMemoryBytes,
  elapsedMs: operationalPolicy.maxElapsedMs,
};
const operationalAssertions = [];
const acceptedBoundary = evaluateOperationalLimits(atLimit);
requireAssertion(acceptedBoundary.decision === "continue", "Exact operational boundary parked");
requireAssertion(
  acceptedBoundary.hostileProviderGuarantee === false,
  "Operational limit claimed hostile-provider containment",
);
operationalAssertions.push("exact_operational_boundaries_continue");

const exceededCases = [
  ["freePercent", operationalPolicy.minFreePercent - 0.001, "disk_free_floor_exceeded"],
  [
    "aggregateWorkspaceBytes",
    operationalPolicy.maxAggregateWorkspaceBytes + 1,
    "aggregate_workspace_bytes_exceeded",
  ],
  ["processes", operationalPolicy.maxProcesses + 1, "process_limit_exceeded"],
  ["memoryBytes", operationalPolicy.maxMemoryBytes + 1, "memory_limit_exceeded"],
  ["elapsedMs", operationalPolicy.maxElapsedMs + 1, "elapsed_time_limit_exceeded"],
];
for (const [field, value, reason] of exceededCases) {
  const result = evaluateOperationalLimits({ ...atLimit, [field]: value });
  requireAssertion(result.decision === "park", `${field} excess did not park`);
  requireAssertion(result.route === "needs_you", `${field} excess did not route to Needs you`);
  requireAssertion(result.reason === reason, `${field} excess returned the wrong reason`);
  requireAssertion(result.cleanupAuthorized === false, `${field} excess authorized cleanup`);
  requireAssertion(result.enforcement === "operational", `${field} claimed OS enforcement`);
}
operationalAssertions.push("every_limit_excess_parked_without_cleanup_authority");

for (const field of limitFields) {
  const result = evaluateOperationalLimits({ ...atLimit, [field]: null });
  requireAssertion(result.decision === "park", `${field} missing telemetry did not park`);
  requireAssertion(
    result.reason === "resource_observation_missing",
    `${field} missing telemetry returned the wrong reason`,
  );
  requireAssertion(result.cleanupAuthorized === false, `${field} missing telemetry authorized cleanup`);
}
operationalAssertions.push("every_missing_observation_failed_closed_to_needs_you");

const trustBoundary = {
  trustedComponents: ["director-engine", "paseo-daemon", "installed-provider-cli"],
  untrustedInputs: ["model-output", "repository-content", "external-tool-output"],
  excludedCompromiseClaims: [
    "director-engine-compromise",
    "paseo-daemon-compromise",
    "provider-cli-compromise",
    "kernel-or-root-compromise",
  ],
  rootlessOciMandatory: true,
  providerAuthenticationAvailableToTrustedCli: true,
  providerCredentialSafeFromCompromisedCli: false,
  agentDeniedAuthorities: [
    "engine-state",
    "taskstore",
    "github-credentials",
    "delivery-authority",
    "source-checkout",
    "sibling-workspaces",
    "raw-paseo-control",
    "container-runtime-control",
  ],
};
requireAssertion(trustBoundary.rootlessOciMandatory, "Rootless OCI became optional");
requireAssertion(
  trustBoundary.providerCredentialSafeFromCompromisedCli === false,
  "Contract promised compromised-provider credential safety",
);

process.stdout.write(
  `${JSON.stringify(
    {
      procedure: "dir-m0.14-practical-linux-boundary-v1",
      result: "go",
      platform: `${process.platform}-${process.arch}`,
      trustBoundary,
      lifecycle: {
        installedAutomaticSurfaces: Object.keys(lifecycleFixtures),
        assertions: lifecycleAssertions,
      },
      operationalLimits: {
        policy: operationalPolicy,
        enforcement: "operational_fail_closed",
        hostileProviderGuarantee: false,
        assertions: operationalAssertions,
      },
    },
    null,
    2,
  )}\n`,
);
