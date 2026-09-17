// SPDX-License-Identifier: Apache-2.0

import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  closeSync,
  constants as fsConstants,
  existsSync,
  fstatSync,
  lstatSync,
  openSync,
  readFileSync,
  realpathSync,
  rmSync,
} from "node:fs";
import { isAbsolute, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import {
  CoordinatorError,
  assertExactKeys,
  boundedText,
  canonicalExistingDirectory,
  canonicalJson,
  canonicalPath,
  canonicalize,
  compareText,
  digest,
  githubRepositoryIdentity,
  isPriorReviewFinding,
  isObject,
  parseBoundedJson,
  pathIsWithin,
  persistPrivateJson,
  readJsonFile,
  refuse,
  withProcessIdentityLock,
} from "./internal.mjs";

export { CoordinatorError, canonicalJson, digest } from "./internal.mjs";

const SHA_PATTERN = /^[0-9a-f]{40}$/u;
const TASK_PATTERN = /^dir-[a-z0-9][a-z0-9.-]*$/u;
const ACTOR_PATTERN = /^paseo:[a-zA-Z0-9-]{8,128}$/u;
const REF_PART_PATTERN = /^(?!.*\.\.)(?!.*[@{\\~^:?*\[])[a-zA-Z0-9][a-zA-Z0-9._/-]{0,199}$/u;
const REPOSITORY_PATTERN = /^[a-zA-Z0-9_.-]+\/[a-zA-Z0-9_.-]+$/u;
const ID_PATTERN = /^[a-zA-Z0-9][a-zA-Z0-9._:-]{2,199}$/u;
const OWNERSHIP_PATTERN = /^[a-zA-Z0-9][a-zA-Z0-9._:-]{15,127}$/u;
const MAX_JSON_BYTES = 1_048_576;
const MAX_COMMAND_OUTPUT = 4 * 1_048_576;
const COMMAND_TIMEOUT_MS = 120_000;
const MAXIMUM_PASEO_CREDENTIAL_BYTES = 4_096;
const PASEO_LIFECYCLE_CHILD = fileURLToPath(
  new URL("./paseo-lifecycle.mjs", import.meta.url),
);
const LEGACY_STATE_SCHEMA_VERSION = 1;
const STATE_SCHEMA_VERSION = 2;
const CLEANUP_PLAN_SCHEMA_VERSION = 2;
const REVIEWER_PLAN_SCHEMA_VERSION = 1;
const OUTPUT_SCHEMA_VERSION = 1;
const PASEO_AGENT_STATUSES = new Set([
  "initializing",
  "idle",
  "running",
  "error",
  "closed",
]);
const REVIEW_DIMENSIONS = [
  "acceptance",
  "correctness",
  "security",
  "maintainability",
  "readability",
  "design",
  "quality",
  "rigor",
];

const COMMON_OPTIONS = [
  "task",
  "actor",
  "repo",
  "repo-id",
  "remote",
  "base-ref",
  "base",
  "branch",
  "candidate",
  "head-owner",
  "ownership",
  "checkout",
  "checkout-state",
  "control-repo",
  "agent-id",
  "workspace-id",
  "lifecycle-state",
  "pr",
];
// The Reviewer leg's immutable inputs. They are a distinct group rather than a
// reuse of the Task Agent's because both resources exist at once and cleanup
// must be able to name each exactly: the Reviewer is a second parentless agent
// with its own host view and its own disposable checkout.
const REVIEWER_OPTIONS = [
  "reviewer-agent-id",
  "reviewer-workspace-id",
  "reviewer-lifecycle-state",
  "reviewer-checkout",
  "reviewer-checkout-state",
  "reviewer-review-state",
];
const REVIEWER_BINDING_KEYS = [
  "reviewerAgentId",
  "reviewerCheckout",
  "reviewerCheckoutState",
  "reviewerLifecycleState",
  "reviewerReviewState",
  "reviewerWorkspaceId",
];
const STATE_BINDING_KEYS = [
  "actor",
  "base",
  "baseRef",
  "branch",
  "candidate",
  "checkout",
  "checkoutState",
  "controlRepo",
  "headOwner",
  "ownershipTokenHash",
  "remote",
  "repo",
  "repoId",
  "task",
  "agentId",
  "lifecycleState",
  "workspaceId",
];
const LEGACY_STATE_BINDING_KEYS = STATE_BINDING_KEYS.map((key) =>
  key === "ownershipTokenHash" ? "ownership" : key,
);
const DELIVERY_BINDING_KEYS = [...STATE_BINDING_KEYS, "pr"];
const HANDOFF_OWNERSHIP_KEYS = [
  "actor",
  "agentId",
  "branch",
  "checkout",
  "checkoutState",
  "headOwner",
  "ownershipTokenHash",
  "remote",
  "repository",
  "repositoryId",
  "taskAssignee",
  "lifecycleState",
  "workspaceId",
];

// A handoff manifest stays usable while its lifecycle binding only ever loses
// live facts: active may become restored, and either may become historical.
const LIVE_LIFECYCLE_STATES = new Set(["active", "restored"]);

// Cleanup's own effects advance these two binding fields and only these two:
// archiving the agent and workspace makes the lifecycle historical, and
// removing the worktree reclaims the checkout. They are the exact fields a
// resumed run may describe differently, while every identity, ownership and
// actor field stays byte-identical. A later cleanup leg that binds its own
// lifecycle progress extends these tables; it does not change how a resumed
// state is admitted.
const LIFECYCLE_PROGRESS_RANKS = Object.freeze({
  checkoutState: Object.freeze({ present: 0, reclaimed: 1 }),
  lifecycleState: Object.freeze({ active: 0, restored: 1, reclaimed: 2 }),
  reviewerCheckoutState: Object.freeze({ present: 0, reclaimed: 1 }),
  reviewerLifecycleState: Object.freeze({ active: 0, restored: 1, reclaimed: 2 }),
});
// Each field advances only to the single terminal value cleanup itself
// produces. `restored` is a recovery fact produced by something other than
// cleanup, so it is an admissible source and never an admissible target:
// cleanupApply has no restored effect path, and a resumed run targeting it
// would carry an archive intent it can neither dispatch nor terminalize while
// still reporting the cleanup complete. A resumed state whose resource came
// back is drift for the coordinator to reconcile, not progress to resume
// across.
const LIFECYCLE_PROGRESS_TARGETS = Object.freeze({
  checkoutState: "reclaimed",
  lifecycleState: "reclaimed",
  reviewerCheckoutState: "reclaimed",
  reviewerLifecycleState: "reclaimed",
});
const LIFECYCLE_PROGRESS_KEYS = Object.freeze(
  Object.keys(LIFECYCLE_PROGRESS_RANKS),
);
const EFFECT_CLASSES = new Set([
  "conditional_update",
  "destructive_terminal",
  "idempotent_close",
  "store_only",
  "unique_create",
]);
const EFFECT_PHASES = new Set([
  "complete",
  "dispatching",
  "intent_recorded",
  "needs_manual_reconciliation",
  "unknown",
]);
const EFFECT_NAME_PATTERN = /^[a-z][a-z_]{0,31}\.[a-z][a-z-]{0,31}$/u;
const MAXIMUM_RESUMED_EFFECTS = 64;

const MUTATING_COMMANDS = new Set([
  "publish-draft",
  "remote-ci",
  "publish",
  "integrate",
  "cleanup-apply",
  "reviewer-cleanup-apply",
]);
const STATE_LOCK_COMMANDS = new Set([
  ...MUTATING_COMMANDS,
  "cleanup-plan",
  "reviewer-cleanup-plan",
]);
const RESUMABLE_COMMANDS = new Set([
  "cleanup-plan",
  "cleanup-apply",
  "reviewer-cleanup-plan",
  "reviewer-cleanup-apply",
]);
// The Reviewer leg's own commands. `reviewer-survey` derives the set to
// reconcile and mutates nothing; the plan/apply pair reconciles exactly one
// bound Reviewer.
const REVIEWER_BOUND_COMMANDS = new Set([
  "reviewer-cleanup-plan",
  "reviewer-cleanup-apply",
]);
const COMMANDS = new Set([
  "snapshot",
  "review-handoff",
  "publish-draft",
  "remote-ci",
  "publish",
  "gate",
  "integrate",
  "reviewer-survey",
  "reviewer-cleanup-plan",
  "reviewer-cleanup-apply",
  "cleanup-plan",
  "cleanup-apply",
]);
// A Reviewer is reconcilable only when its Review is over. `running` is the
// daemon's statement that a turn is in flight, and a Task still in progress may
// start or resume one, so neither fact alone terminates a Review.
const REVIEWER_REVIEW_STATES = new Set(["verdict_recorded", "abandoned"]);
const MAXIMUM_SURVEYED_TASKS = 64;
const MAXIMUM_REPORTED_REVIEWERS = 32;

export class CoordinatorInterruption extends Error {
  constructor(effect) {
    super(`interrupted after ${effect} dispatch`);
    this.name = "CoordinatorInterruption";
    this.effect = effect;
  }
}

/**
 * Resolves a documented Paseo lifecycle verb to the one bounded child operation
 * that may perform it. Reads and mutations share this boundary deliberately:
 * both authenticate over HTTP bearer, which accepts the valid delimiter-rich
 * daemon password that the CLI's WebSocket subprotocol grammar rejects. Any
 * other Paseo verb resolves to null and never sees the password.
 */
function paseoLifecycleCall(executable, args) {
  if (executable !== "paseo") return null;
  if (args.length === 3 && args[2] === "--json") {
    if (args[0] === "inspect" && ID_PATTERN.test(args[1])) {
      return { operation: "agent.inspect", identifier: args[1], mutation: false };
    }
    if (args[0] === "archive" && ID_PATTERN.test(args[1])) {
      return { operation: "agent.archive", identifier: args[1], mutation: true };
    }
    if (args[0] === "workspace" && args[1] === "ls") {
      return { operation: "workspace.list", identifier: null, mutation: false };
    }
    if (args[0] === "agents" && args[1] === "ls") {
      return { operation: "agent.list", identifier: null, mutation: false };
    }
  }
  if (
    args.length === 4 && args[0] === "workspace" && args[1] === "archive" &&
    ID_PATTERN.test(args[2]) && args[3] === "--json"
  ) {
    return { operation: "workspace.archive", identifier: args[2], mutation: true };
  }
  return null;
}

/**
 * A credential-free Paseo address carries no userinfo and no password
 * parameter. The owner-only credential file is the sole password authority;
 * credentials embedded in a host are refused rather than normalized or
 * forwarded.
 */
function credentialShapedHost(value) {
  return /password/iu.test(value) || /^[^/?#]*@/u.test(value.replace(/^[a-z][a-z0-9+.-]*:\/\//iu, ""));
}

function normalizedPaseoHost(host) {
  if (host === undefined) return undefined;
  refuse(
    credentialShapedHost(host),
    "PASEO_AUTH_LOCATION_UNSUPPORTED",
    "Paseo host must be a credential-free address",
  );
  return host;
}

function containsProtectedString(value, protectedValue) {
  if (typeof protectedValue !== "string" || protectedValue.length === 0) {
    return false;
  }
  const visit = (item) => {
    if (typeof item === "string") return item.includes(protectedValue);
    if (Array.isArray(item)) return item.some(visit);
    if (isObject(item)) return Object.values(item).some(visit);
    return false;
  };
  return visit(value);
}

function containsProtectedMaterial(
  value,
  ownership,
  ownershipFile = undefined,
  paseoPassword = undefined,
  paseoCredentialFile = undefined,
) {
  const protectedPaths = [ownershipFile, paseoCredentialFile]
    .filter((path) => typeof path === "string")
    .flatMap((path) => [path, resolve(path)]);
  return [ownership, paseoPassword, ...protectedPaths].some((protectedValue) =>
      containsProtectedString(value, protectedValue),
    );
}

function containsProtectedError(
  error,
  ownership,
  ownershipFile = undefined,
  paseoPassword = undefined,
  paseoCredentialFile = undefined,
) {
  return containsProtectedMaterial(
    {
      code: error?.code,
      details: error?.details,
      effect: error?.effect,
      message: error?.message,
    },
    ownership,
    ownershipFile,
    paseoPassword,
    paseoCredentialFile,
  );
}

function selectedEnvironment(executable, args, paseoPassword = undefined) {
  const selected = {};
  const host = normalizedPaseoHost(process.env.PASEO_HOST);
  for (const key of [
    "PATH",
    "LANG",
    "LC_ALL",
    "TMPDIR",
    "HOME",
    "XDG_CONFIG_HOME",
    "BEADS_DIR",
    "GH_CONFIG_DIR",
    "GH_HOST",
  ]) {
    const value = process.env[key];
    if (value !== undefined && !containsProtectedString(value, paseoPassword)) {
      selected[key] = value;
    }
  }
  if (host !== undefined) {
    refuse(
      containsProtectedString(host, paseoPassword),
      "PASEO_AUTH_LOCATION_UNSUPPORTED",
      "Paseo host carries credential material",
    );
    selected.PASEO_HOST = host;
  }
  if (paseoLifecycleCall(executable, args) !== null) {
    const paseoHome = process.env.PASEO_HOME;
    if (
      paseoHome !== undefined &&
      !containsProtectedString(paseoHome, paseoPassword)
    ) {
      selected.PASEO_HOME = paseoHome;
    }
    if (paseoPassword !== undefined) selected.PASEO_PASSWORD = paseoPassword;
  }
  selected.GH_PROMPT_DISABLED = "1";
  selected.GH_PAGER = "cat";
  selected.GIT_PAGER = "cat";
  selected.GIT_TERMINAL_PROMPT = "0";
  selected.GIT_CONFIG_NOSYSTEM = "1";
  selected.GCM_INTERACTIVE = "Never";
  return selected;
}

export function defaultCommandRunner(executable, args, options = {}) {
  const lifecycle = paseoLifecycleCall(executable, args);
  const paseoPassword = options.paseoPassword;
  if (lifecycle !== null && (typeof paseoPassword !== "string" || paseoPassword.length === 0)) {
    return {
      error: undefined,
      status: 64,
      stdout: "",
      stderr: "PASEO_AUTH_REQUIRED\n",
    };
  }
  const childExecutable = lifecycle === null ? executable : process.execPath;
  const childArgs = lifecycle === null
    ? args
    : [
        PASEO_LIFECYCLE_CHILD,
        lifecycle.operation,
        ...(lifecycle.identifier === null ? [] : [lifecycle.identifier]),
      ];
  const result = spawnSync(childExecutable, childArgs, {
    cwd: options.cwd,
    encoding: "utf8",
    env: selectedEnvironment(executable, args, paseoPassword),
    input: options.input,
    maxBuffer: MAX_COMMAND_OUTPUT,
    shell: false,
    timeout: options.timeout ?? COMMAND_TIMEOUT_MS,
  });
  return {
    error: result.status === null ? result.error : undefined,
    status: result.status,
    stdout: result.stdout ?? "",
    stderr: result.stderr ?? "",
    protectedMaterialDetected:
      lifecycle !== null && containsProtectedString(
        {
          error: result.error?.message,
          stderr: result.stderr,
          stdout: result.stdout,
        },
        paseoPassword,
      ),
  };
}

function paseoProtectedMaterialError(lifecycle, result) {
  return new CoordinatorError(
    "PASEO_LIFECYCLE_RESPONSE_REDACTED",
    "Paseo lifecycle response contained protected material",
    { operation: lifecycle.operation, status: result.status },
  );
}

/**
 * Classifies the deterministic refusals a bounded lifecycle child reports: the
 * daemon either demanded or rejected credentials, or its response echoed the
 * selected one. Each is proven without an executed effect, so a mutation may
 * report it exactly rather than degrading into an unproven-archive refusal.
 * Anything else stays unclassified because it may have reached the daemon.
 */
function paseoLifecycleRefusal(lifecycle, result) {
  const response = `${result.stdout ?? ""}\n${result.stderr ?? ""}`;
  const details = { operation: lifecycle.operation, status: result.status };
  if (/PASEO_AUTH_REQUIRED|Password required/iu.test(response)) {
    return new CoordinatorError(
      "PASEO_AUTH_REQUIRED",
      "Paseo lifecycle authentication is required",
      details,
    );
  }
  if (/PASEO_AUTH_FAILED|Incorrect password/iu.test(response)) {
    return new CoordinatorError(
      "PASEO_AUTH_FAILED",
      "Paseo lifecycle authentication was rejected",
      details,
    );
  }
  if (/PASEO_LIFECYCLE_RESPONSE_REDACTED/iu.test(response)) {
    return paseoProtectedMaterialError(lifecycle, result);
  }
  return null;
}

function paseoLifecycleFailure(lifecycle, result) {
  return paseoLifecycleRefusal(lifecycle, result) ?? new CoordinatorError(
    lifecycle.mutation ? "PASEO_LIFECYCLE_MUTATION_FAILED" : "PASEO_LIFECYCLE_READ_FAILED",
    lifecycle.mutation
      ? "Paseo lifecycle mutation was not acknowledged"
      : "Paseo lifecycle facts were unavailable",
    { operation: lifecycle.operation, status: result.status },
  );
}

function checkedRun(run, executable, args, options = {}) {
  const result = run(executable, args, options);
  const lifecycle = paseoLifecycleCall(executable, args);
  if (lifecycle !== null && result.protectedMaterialDetected === true) {
    throw paseoProtectedMaterialError(lifecycle, result);
  }
  if (result.error || result.status !== 0) {
    if (lifecycle !== null) throw paseoLifecycleFailure(lifecycle, result);
    throw new CoordinatorError(
      "EXTERNAL_COMMAND_FAILED",
      `${executable} could not provide an authoritative result`,
      {
        executable,
        status: result.status,
        diagnostic: boundedText(result.stderr || result.error?.message),
      },
    );
  }
  return result.stdout.trim();
}

function parsedJson(output, source) {
  return parseBoundedJson(output, source, {
    maximum: MAX_JSON_BYTES,
    oversizeCode: "EXTERNAL_JSON_OVERSIZE",
    invalidCode: "EXTERNAL_JSON_INVALID",
  });
}

function runJson(run, executable, args, options = {}) {
  const output = checkedRun(run, executable, args, options);
  const value = parsedJson(output, executable);
  return value;
}

function requireString(value, pattern, label) {
  refuse(
    typeof value !== "string" || !pattern.test(value),
    "INPUT_INVALID",
    `${label} is invalid`,
  );
  return value;
}

function exactSha(value, label) {
  return requireString(value, SHA_PATTERN, label);
}

function parsePositiveInteger(value, label) {
  refuse(
    !/^[1-9][0-9]*$/u.test(String(value ?? "")),
    "INPUT_INVALID",
    `${label} must be a positive integer`,
  );
  const parsed = Number(value);
  refuse(!Number.isSafeInteger(parsed), "INPUT_INVALID", `${label} is too large`);
  return parsed;
}

function optionValues(argv) {
  const options = {};
  for (let index = 0; index < argv.length; index += 1) {
    const token = argv[index];
    refuse(
      !token.startsWith("--") || token === "--",
      "ARGUMENT_INVALID",
      "unexpected argument",
    );
    const key = token.slice(2);
    refuse(key.length === 0, "ARGUMENT_INVALID", "empty option name");
    const value = argv[index + 1];
    refuse(
      value === undefined || value.startsWith("--"),
      "ARGUMENT_MISSING_VALUE",
      "option requires a value",
    );
    index += 1;
    if (key === "required-check") {
      options[key] ??= [];
      options[key].push(value);
    } else {
      refuse(
        options[key] !== undefined,
        "ARGUMENT_DUPLICATE",
        "duplicate option",
      );
      options[key] = value;
    }
  }
  return options;
}

function readPaseoCredentialFile() {
  const credentialPath = process.env.DIRECTOR_PASEO_CREDENTIAL_FILE;
  if (credentialPath === undefined) return null;
  refuse(
    !isAbsolute(credentialPath),
    "PASEO_CREDENTIAL_FILE_INVALID",
    "Paseo credential file must be absolute",
  );
  let descriptor;
  try {
    descriptor = openSync(
      credentialPath,
      fsConstants.O_RDONLY | fsConstants.O_NOFOLLOW,
    );
    const status = fstatSync(descriptor);
    refuse(
      !status.isFile() || (status.mode & 0o077) !== 0 ||
        (typeof process.getuid === "function" && status.uid !== process.getuid()),
      "PASEO_CREDENTIAL_FILE_INVALID",
      "Paseo credential file must be an owner-only regular file with mode 0600",
    );
    refuse(
      status.size < 1 || status.size > MAXIMUM_PASEO_CREDENTIAL_BYTES,
      "PASEO_CREDENTIAL_FILE_INVALID",
      "Paseo credential file has an invalid size",
    );
    const password = readFileSync(descriptor, "utf8");
    refuse(
      Buffer.byteLength(password) !== status.size || password.length === 0 ||
        password.includes("\0"),
      "PASEO_CREDENTIAL_FILE_INVALID",
      "Paseo credential file contains an invalid password",
    );
    return { password, path: credentialPath };
  } catch (error) {
    if (error instanceof CoordinatorError) throw error;
    throw new CoordinatorError(
      "PASEO_CREDENTIAL_FILE_UNAVAILABLE",
      "Paseo credential file is unavailable",
    );
  } finally {
    if (descriptor !== undefined) closeSync(descriptor);
  }
}

export function parseCli(argv) {
  const [command, ...rest] = argv;
  refuse(!COMMANDS.has(command), "COMMAND_INVALID", "unknown coordinator command");
  const options = optionValues(rest);
  refuse(
    options.ownership !== undefined,
    "OWNERSHIP_ARG_FORBIDDEN",
    "use --ownership-file so the ownership token never enters argv or command logs",
  );
  refuse(
    options["ownership-file"] === undefined,
    "OPTION_REQUIRED",
    "--ownership-file is required",
  );
  const ownershipPath = options["ownership-file"];
  refuse(
    !isAbsolute(ownershipPath),
    "PATH_NOT_ABSOLUTE",
    "ownership file must be absolute",
  );
  let ownershipStatus;
  try {
    ownershipStatus = lstatSync(ownershipPath);
  } catch {
    throw new CoordinatorError("OWNERSHIP_FILE_UNAVAILABLE", "ownership file is unavailable");
  }
  refuse(
    !ownershipStatus.isFile() || ownershipStatus.isSymbolicLink() ||
      (ownershipStatus.mode & 0o077) !== 0 ||
      (typeof process.getuid === "function" && ownershipStatus.uid !== process.getuid()),
    "OWNERSHIP_FILE_INVALID",
    "ownership file must be an owner-only regular file with mode 0600",
  );
  refuse(
    ownershipStatus.size < 16 || ownershipStatus.size > 128,
    "OWNERSHIP_FILE_INVALID",
    "ownership file has an invalid size",
  );
  const ownership = readFileSync(ownershipPath, "utf8");
  refuse(
    !OWNERSHIP_PATTERN.test(ownership),
    "OWNERSHIP_FILE_INVALID",
    "ownership file contains an invalid token",
  );
  options.ownership = ownership;
  delete options["ownership-file"];
  Object.defineProperty(options, "ownershipFile", {
    value: ownershipPath,
    enumerable: false,
  });
  const paseoCredential = readPaseoCredentialFile();
  if (paseoCredential !== null) {
    Object.defineProperty(options, "paseoPassword", {
      value: paseoCredential.password,
      enumerable: false,
    });
    Object.defineProperty(options, "paseoCredentialFile", {
      value: paseoCredential.path,
      enumerable: false,
    });
  }
  return { command, options };
}

function validateOptions(command, rawOptions) {
  const allowed = new Set([
    ...COMMON_OPTIONS,
    "state-file",
    "review-file",
    "manifest-file",
    "review-harness-file",
    "validation-file",
    "required-check",
    "expected-remote-head",
    "title",
    "body-file",
    "plan-file",
    "ci-workflow",
    "remote-ci-file",
    "handoff-file",
    "resume-state-file",
    ...REVIEWER_OPTIONS,
  ]);
  const unknown = Object.keys(rawOptions).filter((key) => !allowed.has(key));
  refuse(
    unknown.length > 0,
    "OPTION_UNKNOWN",
    "unknown command options",
    { unknownOptionCount: unknown.length },
  );
  for (const key of COMMON_OPTIONS) {
    refuse(rawOptions[key] === undefined, "OPTION_REQUIRED", `--${key} is required`);
  }

  const task = requireString(rawOptions.task, TASK_PATTERN, "task");
  const actor = requireString(rawOptions.actor, ACTOR_PATTERN, "actor");
  const repo = requireString(rawOptions.repo, REPOSITORY_PATTERN, "repository");
  const repoId = parsePositiveInteger(rawOptions["repo-id"], "repository ID");
  const remote = requireString(rawOptions.remote, ID_PATTERN, "remote");
  const baseRef = requireString(rawOptions["base-ref"], REF_PART_PATTERN, "base ref");
  const base = exactSha(rawOptions.base, "base");
  const branch = requireString(rawOptions.branch, REF_PART_PATTERN, "branch");
  const candidate = exactSha(rawOptions.candidate, "Candidate");
  const headOwner = requireString(rawOptions["head-owner"], ID_PATTERN, "head owner");
  const ownership = requireString(
    rawOptions.ownership,
    OWNERSHIP_PATTERN,
    "ownership token",
  );
  refuse(
    !branch.startsWith(`task/${task}-`),
    "BRANCH_NOT_OWNED",
    "branch is outside the Task-owned namespace",
  );
  refuse(
    branch === baseRef || branch === "main" || branch === "master",
    "PROTECTED_BRANCH",
    "Task branch cannot be a protected target branch",
  );
  refuse(
    !isAbsolute(rawOptions.checkout),
    "PATH_NOT_ABSOLUTE",
    "Task checkout must be absolute",
  );
  const checkoutState = requireString(
    rawOptions["checkout-state"],
    /^(?:present|reclaimed)$/u,
    "checkout state",
  );
  const checkout = canonicalPath(rawOptions.checkout, "Task checkout", {
    mustExist: checkoutState === "present" && command !== "cleanup-apply",
    allowMissingParents: checkoutState === "reclaimed",
  });
  refuse(
    checkoutState === "reclaimed" && existsSync(checkout),
    "RECLAIMED_CHECKOUT_PRESENT",
    "checkout recorded as reclaimed is still present",
  );
  const controlRepo = canonicalExistingDirectory(
    rawOptions["control-repo"],
    "control repository",
  );
  const agentId =
    rawOptions["agent-id"] === "none"
      ? "none"
      : requireString(rawOptions["agent-id"], ID_PATTERN, "agent ID");
  const workspaceId =
    rawOptions["workspace-id"] === "none"
      ? "none"
      : requireString(rawOptions["workspace-id"], ID_PATTERN, "workspace ID");
  const lifecycleState = requireString(
    rawOptions["lifecycle-state"],
    /^(?:active|restored|reclaimed|none)$/u,
    "lifecycle state",
  );
  refuse(
    (agentId === "none") !== (workspaceId === "none") ||
      (lifecycleState === "none") !== (agentId === "none"),
    "LIFECYCLE_BINDING_INCOMPLETE",
    "lifecycle state requires two exact IDs or an explicit none binding",
  );
  refuse(
    ((lifecycleState === "active" || lifecycleState === "restored") &&
      checkoutState !== "present") ||
      (lifecycleState === "reclaimed" && checkoutState !== "reclaimed"),
    "LIFECYCLE_CHECKOUT_STATE_MISMATCH",
    "active/restored/reclaimed lifecycle and checkout states must agree",
  );
  const pr =
    rawOptions.pr === "absent"
      ? "absent"
      : parsePositiveInteger(rawOptions.pr, "pull request number");

  const options = {
    task,
    actor,
    repo,
    repoId,
    remote,
    baseRef,
    base,
    branch,
    candidate,
    headOwner,
    ownership,
    checkout,
    checkoutState,
    controlRepo,
    agentId,
    workspaceId,
    lifecycleState,
    pr,
    command,
  };

  const boundReviewer = REVIEWER_BOUND_COMMANDS.has(command);
  const suppliedReviewerOptions = REVIEWER_OPTIONS.filter(
    (key) => rawOptions[key] !== undefined,
  );
  refuse(
    !boundReviewer && suppliedReviewerOptions.length > 0,
    "OPTION_INVALID",
    "Reviewer options are valid only for reviewer-cleanup-plan and reviewer-cleanup-apply",
  );
  if (boundReviewer) {
    for (const key of REVIEWER_OPTIONS) {
      refuse(rawOptions[key] === undefined, "OPTION_REQUIRED", `--${key} is required`);
    }
    refuse(
      rawOptions["reviewer-agent-id"] === "none",
      "OPTION_INVALID",
      "the Reviewer leg always names an exact Reviewer agent",
    );
    options.reviewerAgentId = requireString(
      rawOptions["reviewer-agent-id"],
      ID_PATTERN,
      "Reviewer agent ID",
    );
    options.reviewerWorkspaceId =
      rawOptions["reviewer-workspace-id"] === "none"
        ? "none"
        : requireString(
            rawOptions["reviewer-workspace-id"],
            ID_PATTERN,
            "Reviewer workspace ID",
          );
    options.reviewerLifecycleState = requireString(
      rawOptions["reviewer-lifecycle-state"],
      /^(?:active|restored|reclaimed)$/u,
      "Reviewer lifecycle state",
    );
    options.reviewerReviewState = requireString(
      rawOptions["reviewer-review-state"],
      /^(?:verdict_recorded|abandoned)$/u,
      "Reviewer review state",
    );
    const reviewerCheckoutState = requireString(
      rawOptions["reviewer-checkout-state"],
      /^(?:present|reclaimed)$/u,
      "Reviewer checkout state",
    );
    options.reviewerCheckoutState = reviewerCheckoutState;
    refuse(
      !isAbsolute(rawOptions["reviewer-checkout"]),
      "PATH_NOT_ABSOLUTE",
      "Reviewer checkout must be absolute",
    );
    options.reviewerCheckout = canonicalPath(
      rawOptions["reviewer-checkout"],
      "Reviewer checkout",
      {
        mustExist: reviewerCheckoutState === "present",
        allowMissingParents: reviewerCheckoutState === "reclaimed",
      },
    );
    refuse(
      reviewerCheckoutState === "reclaimed" && existsSync(options.reviewerCheckout),
      "RECLAIMED_CHECKOUT_PRESENT",
      "Reviewer checkout recorded as reclaimed is still present",
    );
    // The Reviewer's host view is a card over an independent clone, so
    // archiving it leaves the checkout in place. The two lifecycle facts are
    // therefore observed separately and never inferred from one another.
    refuse(
      options.reviewerCheckout === checkout ||
        options.reviewerCheckout === controlRepo ||
        pathIsWithin(controlRepo, options.reviewerCheckout) ||
        pathIsWithin(options.reviewerCheckout, controlRepo) ||
        pathIsWithin(checkout, options.reviewerCheckout) ||
        pathIsWithin(options.reviewerCheckout, checkout),
      "REVIEWER_CHECKOUT_NOT_DISPOSABLE",
      "Reviewer checkout overlaps the Task or control Git checkout",
    );
    refuse(
      options.reviewerAgentId === options.agentId ||
        `paseo:${options.reviewerAgentId}` === options.actor ||
        options.reviewerAgentId === options.actor,
      "REVIEWER_NOT_INDEPENDENT",
      "Reviewer identity matches the Task Agent or coordinator actor",
    );
    refuse(
      options.reviewerWorkspaceId !== "none" &&
        options.reviewerWorkspaceId === options.workspaceId,
      "REVIEWER_NOT_INDEPENDENT",
      "Reviewer workspace identity matches the Task workspace",
    );
  }

  if (
    STATE_LOCK_COMMANDS.has(command) || rawOptions["state-file"] !== undefined
  ) {
    refuse(
      rawOptions["state-file"] === undefined,
      "OPTION_REQUIRED",
      "--state-file is required for commands that consume coordinator state",
    );
    options.stateFile = canonicalPath(rawOptions["state-file"], "state file", {
      mustExist: false,
    });
    refuse(
      pathIsWithin(checkout, options.stateFile) ||
        pathIsWithin(controlRepo, options.stateFile) ||
        (options.reviewerCheckout !== undefined &&
          pathIsWithin(options.reviewerCheckout, options.stateFile)),
      "STATE_INSIDE_REPOSITORY",
      "state file must be outside the Task, control, and Reviewer checkouts",
    );
  }

  if (rawOptions["resume-state-file"] !== undefined) {
    refuse(
      !RESUMABLE_COMMANDS.has(command),
      "OPTION_INVALID",
      "--resume-state-file is valid only for cleanup-plan and cleanup-apply",
    );
    options.resumeStateFile = canonicalPath(
      rawOptions["resume-state-file"],
      "resumed state file",
      { mustExist: false },
    );
    refuse(
      pathIsWithin(checkout, options.resumeStateFile) ||
        pathIsWithin(controlRepo, options.resumeStateFile) ||
        (options.reviewerCheckout !== undefined &&
          pathIsWithin(options.reviewerCheckout, options.resumeStateFile)),
      "RESUME_STATE_INSIDE_REPOSITORY",
      "resumed state file must be outside the Task, control, and Reviewer checkouts",
    );
    refuse(
      options.resumeStateFile === options.stateFile,
      "RESUME_STATE_NOT_DISTINCT",
      "resumed state file must differ from the current state file",
    );
  }

  for (const key of [
    "review-file",
    "manifest-file",
    "review-harness-file",
    "validation-file",
    "body-file",
    "plan-file",
    "remote-ci-file",
    "handoff-file",
  ]) {
    if (rawOptions[key] !== undefined) {
      refuse(!isAbsolute(rawOptions[key]), "PATH_NOT_ABSOLUTE", `--${key} must be absolute`);
      options[key.replaceAll(/-([a-z])/gu, (_, letter) => letter.toUpperCase())] = resolve(
        rawOptions[key],
      );
    }
  }
  if (options.handoffFile !== undefined) {
    refuse(
      command !== "review-handoff",
      "OPTION_INVALID",
      "--handoff-file is valid only for review-handoff",
    );
    refuse(
      pathIsWithin(checkout, options.handoffFile) ||
        pathIsWithin(controlRepo, options.handoffFile),
      "HANDOFF_INSIDE_REPOSITORY",
      "handoff file must be outside the Task and control Git checkouts",
    );
  }
  if (rawOptions["required-check"] !== undefined) {
    options.requiredChecks = rawOptions["required-check"].map((value) =>
      requireString(value, /^[a-zA-Z0-9][a-zA-Z0-9 ._:/()-]{0,199}$/u, "required check"),
    );
    refuse(
      new Set(options.requiredChecks).size !== options.requiredChecks.length,
      "INPUT_INVALID",
      "required checks must be unique",
    );
  }
  if (rawOptions["expected-remote-head"] !== undefined) {
    options.expectedRemoteHead =
      rawOptions["expected-remote-head"] === "absent"
        ? null
        : exactSha(rawOptions["expected-remote-head"], "expected remote head");
  }
  if (rawOptions["ci-workflow"] !== undefined) {
    options.ciWorkflow = requireString(
      rawOptions["ci-workflow"],
      /^[a-zA-Z0-9][a-zA-Z0-9 ._:/()-]{0,199}$/u,
      "CI workflow",
    );
  }
  if (rawOptions.title !== undefined) {
    refuse(
      rawOptions.title.length < 1 || rawOptions.title.length > 200,
      "INPUT_INVALID",
      "title must contain 1-200 characters",
    );
    options.title = rawOptions.title;
  }
  if (typeof rawOptions.ownershipFile === "string") {
    Object.defineProperty(options, "ownershipFile", {
      value: rawOptions.ownershipFile,
      enumerable: false,
    });
  }
  if (typeof rawOptions.paseoPassword === "string") {
    Object.defineProperty(options, "paseoPassword", {
      value: rawOptions.paseoPassword,
      enumerable: false,
    });
  }
  if (typeof rawOptions.paseoCredentialFile === "string") {
    Object.defineProperty(options, "paseoCredentialFile", {
      value: rawOptions.paseoCredentialFile,
      enumerable: false,
    });
  }
  validatePullRequestInputs(options);
  return options;
}

function validatePullRequestInputs(options) {
  const fields = {
    repo: options.repo,
    remote: options.remote,
    baseRef: options.baseRef,
    branch: options.branch,
    headOwner: options.headOwner,
    head: `${options.headOwner}:${options.branch}`,
    ...(options.title === undefined ? {} : { title: options.title }),
    ...(options.bodyFile === undefined ? {} : { bodyFile: options.bodyFile }),
  };
  refuse(
    containsProtectedMaterial(
      fields,
      options.ownership,
      options.ownershipFile,
      options.paseoPassword,
      options.paseoCredentialFile,
    ),
    "PROTECTED_MATERIAL_REDACTED",
    "pull request inputs contained protected material",
  );
}

function stateBinding(options) {
  return {
    actor: options.actor,
    base: options.base,
    baseRef: options.baseRef,
    branch: options.branch,
    candidate: options.candidate,
    checkout: options.checkout,
    checkoutState: options.checkoutState,
    controlRepo: options.controlRepo,
    headOwner: options.headOwner,
    ownershipTokenHash: digest(options.ownership),
    remote: options.remote,
    repo: options.repo,
    repoId: options.repoId,
    task: options.task,
    agentId: options.agentId,
    lifecycleState: options.lifecycleState,
    workspaceId: options.workspaceId,
    ...(options.reviewerAgentId === undefined
      ? {}
      : {
          reviewerAgentId: options.reviewerAgentId,
          reviewerCheckout: options.reviewerCheckout,
          reviewerCheckoutState: options.reviewerCheckoutState,
          reviewerLifecycleState: options.reviewerLifecycleState,
          reviewerReviewState: options.reviewerReviewState,
          reviewerWorkspaceId: options.reviewerWorkspaceId,
        }),
  };
}

/**
 * The exact keys a state bound to these options must carry. A command that
 * binds a Reviewer carries the Reviewer fields and a command that does not must
 * not, so a state file written under one contract is never admitted under the
 * other.
 */
function stateBindingKeys(options) {
  return options.reviewerAgentId === undefined
    ? STATE_BINDING_KEYS
    : [...STATE_BINDING_KEYS, ...REVIEWER_BINDING_KEYS];
}

function legacyStateBinding(options) {
  return {
    ...stateBinding(options),
    ownership: options.ownership,
    ownershipTokenHash: undefined,
  };
}

function newState(options) {
  return {
    schemaVersion: STATE_SCHEMA_VERSION,
    binding: stateBinding(options),
    effects: {},
  };
}

function loadState(options, { required = false } = {}) {
  if (!options.stateFile) {
    refuse(required, "STATE_REQUIRED", "state file is required");
    return null;
  }
  if (!existsSync(options.stateFile)) {
    refuse(required, "STATE_MISSING", "state file does not exist");
    return newState(options);
  }
  const stateStatus = lstatSync(options.stateFile);
  refuse(
    !stateStatus.isFile() ||
      stateStatus.isSymbolicLink() ||
      (stateStatus.mode & 0o077) !== 0 ||
      (typeof process.getuid === "function" && stateStatus.uid !== process.getuid()),
    "STATE_PERMISSIONS_INVALID",
    "state file must be an owner-only regular file with mode 0600",
  );
  const state = readJsonFile(options.stateFile, "coordinator state");
  assertExactKeys(
    state,
    [
      "schemaVersion",
      "binding",
      "effects",
      "cleanupPlanHash",
      "pullRequestNumber",
      "continuation",
    ],
    "state",
  );
  refuse(!isObject(state.effects), "STATE_INVALID", "state effects are invalid");
  if (state.continuation !== undefined) {
    assertExactKeys(
      state.continuation,
      [
        "adoptedEffects",
        "priorBindingHash",
        "priorCheckoutState",
        "priorLifecycleState",
        "priorReviewerCheckoutState",
        "priorReviewerLifecycleState",
      ],
      "state continuation",
      "STATE_INVALID",
    );
    refuse(
      !Array.isArray(state.continuation.adoptedEffects) ||
        !state.continuation.adoptedEffects.every(
          (name) => typeof name === "string" && EFFECT_NAME_PATTERN.test(name),
        ) ||
        !/^[0-9a-f]{64}$/u.test(state.continuation.priorBindingHash ?? ""),
      "STATE_INVALID",
      "state continuation is invalid",
    );
  }
  if (state.schemaVersion === LEGACY_STATE_SCHEMA_VERSION) {
    assertExactKeys(
      state.binding,
      LEGACY_STATE_BINDING_KEYS,
      "legacy state binding",
      "STATE_INVALID",
    );
    refuse(
      canonicalJson(state.binding) !== canonicalJson(legacyStateBinding(options)),
      "STATE_BINDING_MISMATCH",
      "state file is bound to different immutable inputs",
    );
    const { cleanupPlanHash: _legacyCleanupPlanHash, ...withoutCleanupBinding } = state;
    const migrated = {
      ...withoutCleanupBinding,
      schemaVersion: STATE_SCHEMA_VERSION,
      binding: stateBinding(options),
    };
    refuse(
      containsProtectedMaterial(
        migrated,
        options.ownership,
        options.ownershipFile,
        options.paseoPassword,
        options.paseoCredentialFile,
      ),
      "STATE_LEGACY_OWNERSHIP_UNSAFE",
      "legacy coordinator state contains ownership material outside its migratable binding",
    );
    persistState(options, migrated);
    return migrated;
  }
  refuse(
    state.schemaVersion !== STATE_SCHEMA_VERSION,
    "STATE_SCHEMA_UNSUPPORTED",
    "state schema version is unsupported",
  );
  refuse(
    containsProtectedMaterial(
      state,
      options.ownership,
      options.ownershipFile,
      options.paseoPassword,
      options.paseoCredentialFile,
    ),
    "STATE_OWNERSHIP_MATERIAL_FORBIDDEN",
    "coordinator state contains raw ownership material",
  );
  assertExactKeys(
    state.binding,
    stateBindingKeys(options),
    "state binding",
    "STATE_INVALID",
  );
  refuse(
    canonicalJson(state.binding) !== canonicalJson(stateBinding(options)),
    "STATE_BINDING_MISMATCH",
    "state file is bound to different immutable inputs",
  );
  return state;
}

function persistState(options, state) {
  refuse(
    containsProtectedMaterial(
      state,
      options.ownership,
      options.ownershipFile,
      options.paseoPassword,
      options.paseoCredentialFile,
    ),
    "PROTECTED_MATERIAL_REDACTED",
    "coordinator state contained protected material",
  );
  persistPrivateJson(options.stateFile, state, {
    identityCode: "STATE_IDENTITY_INVALID",
    temporaryCode: "STATE_TEMP_EXISTS",
  });
}

function lifecycleProgressRank(key, value) {
  if (value === "none") return "none";
  const ranks = LIFECYCLE_PROGRESS_RANKS[key];
  return Object.hasOwn(ranks, value) ? ranks[value] : undefined;
}

function stableBinding(binding) {
  return Object.fromEntries(
    Object.entries(binding).filter(
      ([key]) => !LIFECYCLE_PROGRESS_KEYS.includes(key),
    ),
  );
}

/**
 * Reads the exact private state an interrupted run left behind, from a
 * lifecycle binding its own effects made unassertable. The file is only ever
 * read: it is admitted when every identity, ownership and actor field matches
 * the current binding byte for byte and the sole difference is ranked
 * lifecycle progress that moved strictly forward. An equal binding is refused
 * because that state is usable directly, and a backward binding is refused
 * because cleanup never restores a reclaimed resource.
 */
function loadResumedState(options) {
  const path = options.resumeStateFile;
  refuse(
    !existsSync(path),
    "RESUME_STATE_MISSING",
    "resumed state file does not exist",
  );
  const status = lstatSync(path);
  refuse(
    !status.isFile() ||
      status.isSymbolicLink() ||
      (status.mode & 0o077) !== 0 ||
      (typeof process.getuid === "function" && status.uid !== process.getuid()),
    "RESUME_STATE_PERMISSIONS_INVALID",
    "resumed state file must be an owner-only regular file with mode 0600",
  );
  const resumed = readJsonFile(path, "resumed coordinator state");
  assertExactKeys(
    resumed,
    [
      "schemaVersion",
      "binding",
      "effects",
      "cleanupPlanHash",
      "pullRequestNumber",
      "continuation",
    ],
    "resumed state",
    "RESUME_STATE_INVALID",
  );
  refuse(
    resumed.schemaVersion !== STATE_SCHEMA_VERSION,
    "RESUME_STATE_SCHEMA_UNSUPPORTED",
    "resumed state schema is unsupported; migrate it under its own binding first",
  );
  refuse(
    containsProtectedMaterial(
      resumed,
      options.ownership,
      options.ownershipFile,
      options.paseoPassword,
      options.paseoCredentialFile,
    ),
    "RESUME_STATE_OWNERSHIP_MATERIAL_FORBIDDEN",
    "resumed coordinator state contains raw ownership material",
  );
  assertExactKeys(
    resumed.binding,
    stateBindingKeys(options),
    "resumed state binding",
    "RESUME_STATE_INVALID",
  );
  const current = stateBinding(options);
  refuse(
    canonicalJson(stableBinding(resumed.binding)) !==
      canonicalJson(stableBinding(current)),
    "RESUME_STATE_BINDING_MISMATCH",
    "resumed state is bound to different immutable inputs",
  );
  let advanced = 0;
  for (const key of LIFECYCLE_PROGRESS_KEYS) {
    const bound = Object.hasOwn(current, key);
    refuse(
      bound !== Object.hasOwn(resumed.binding, key),
      "RESUME_STATE_LIFECYCLE_INVALID",
      "resumed lifecycle progress cannot be compared with the current binding",
    );
    if (!bound) continue;
    const before = lifecycleProgressRank(key, resumed.binding[key]);
    const after = lifecycleProgressRank(key, current[key]);
    refuse(
      before === undefined ||
        after === undefined ||
        (before === "none") !== (after === "none"),
      "RESUME_STATE_LIFECYCLE_INVALID",
      "resumed lifecycle progress cannot be compared with the current binding",
    );
    if (before === "none") continue;
    refuse(
      after < before,
      "RESUME_STATE_LIFECYCLE_REGRESSION",
      "resumed state records later lifecycle progress than the current binding",
    );
    if (after === before) continue;
    refuse(
      current[key] !== LIFECYCLE_PROGRESS_TARGETS[key],
      "RESUME_STATE_LIFECYCLE_NOT_RECLAIMED",
      "resumption advances a lifecycle field only to the reclaimed value cleanup produces",
    );
    advanced += 1;
  }
  refuse(
    advanced === 0,
    "RESUME_STATE_NOT_A_TRANSITION",
    "resumed state has the same lifecycle binding and must be consumed directly",
  );
  refuse(
    !isObject(resumed.effects),
    "RESUME_STATE_INVALID",
    "resumed state effects are invalid",
  );
  const names = Object.keys(resumed.effects);
  refuse(
    names.length > MAXIMUM_RESUMED_EFFECTS,
    "RESUME_STATE_INVALID",
    "resumed state records too many effects",
  );
  for (const name of names) {
    refuse(
      !EFFECT_NAME_PATTERN.test(name),
      "RESUME_STATE_INVALID",
      "resumed state records an invalid effect name",
    );
    const record = resumed.effects[name];
    assertExactKeys(
      record,
      ["class", "phase", "attempts", "evidence"],
      "resumed effect",
      "RESUME_STATE_INVALID",
    );
    refuse(
      !EFFECT_CLASSES.has(record.class) ||
        !EFFECT_PHASES.has(record.phase) ||
        !Number.isSafeInteger(record.attempts) ||
        record.attempts < 0,
      "RESUME_STATE_INVALID",
      "resumed state records an invalid effect",
    );
  }
  refuse(
    resumed.pullRequestNumber !== undefined &&
      options.pr !== "absent" &&
      resumed.pullRequestNumber !== options.pr,
    "RESUME_STATE_PULL_REQUEST_MISMATCH",
    "resumed state is bound to another pull request",
  );
  return resumed;
}

/**
 * Carries an interrupted run's recorded effects into the state bound to the
 * transitioned lifecycle, so an intent this coordinator did record stays
 * discoverable after its own progress invalidated the binding that holds it.
 * Adoption executes nothing and never overwrites a record this state already
 * owns: it only supplies history. Every admission, observation and refusal
 * downstream is unchanged, so an absent ref explained by no recorded intent
 * anywhere still fails closed. The derived `cleanupPlanHash` is deliberately
 * not carried, because a plan is bound to the lifecycle binding that produced
 * it and the transitioned binding must admit its own freshly emitted plan.
 */
function adoptResumedState(options, state) {
  if (options.resumeStateFile === undefined) return null;
  const resumed = loadResumedState(options);
  const priorBindingHash = digest(resumed.binding);
  refuse(
    state.continuation !== undefined &&
      state.continuation.priorBindingHash !== priorBindingHash,
    "RESUME_STATE_REPLACED",
    "state already continued another interrupted lifecycle binding",
  );
  const adopted = [];
  for (const name of Object.keys(resumed.effects).sort(compareText)) {
    if (state.effects[name] !== undefined) continue;
    state.effects[name] = canonicalize(resumed.effects[name]);
    adopted.push(name);
  }
  let mutated = adopted.length > 0;
  if (
    state.pullRequestNumber === undefined &&
    resumed.pullRequestNumber !== undefined
  ) {
    state.pullRequestNumber = resumed.pullRequestNumber;
    mutated = true;
  }
  const continuation = {
    adoptedEffects: [
      ...new Set([...(state.continuation?.adoptedEffects ?? []), ...adopted]),
    ].sort(compareText),
    priorBindingHash,
    priorCheckoutState: resumed.binding.checkoutState,
    priorLifecycleState: resumed.binding.lifecycleState,
    // A Reviewer leg advances its own two lifecycle fields, so the continuation
    // record states which world it resumed from rather than describing only the
    // Task Agent's, which a Reviewer cleanup never moves.
    ...(resumed.binding.reviewerLifecycleState === undefined
      ? {}
      : {
          priorReviewerCheckoutState: resumed.binding.reviewerCheckoutState,
          priorReviewerLifecycleState: resumed.binding.reviewerLifecycleState,
        }),
  };
  if (
    mutated ||
    state.continuation === undefined ||
    canonicalJson(state.continuation) !== canonicalJson(continuation)
  ) {
    state.continuation = continuation;
    persistState(options, state);
  }
  return continuation;
}

async function withStateLock(options, operation) {
  return withProcessIdentityLock(
    {
      lockPath: `${options.stateFile}.lock`,
      bindingHash: digest(stateBinding(options)),
      acceptedBindingHashes: [digest(legacyStateBinding(options))],
      label: "coordinator state lock",
      busyCode: "COORDINATOR_BUSY",
      invalidCode: "STATE_LOCK_INVALID",
      replacedCode: "STATE_LOCK_REPLACED",
      processCode: "PROCESS_IDENTITY_UNAVAILABLE",
    },
    operation,
  );
}

function bindPullRequest(options, state, number) {
  refuse(
    state.pullRequestNumber !== undefined && state.pullRequestNumber !== number,
    "PULL_REQUEST_NUMBER_MISMATCH",
    "state is already bound to another pull request",
  );
  if (state.pullRequestNumber === undefined) {
    state.pullRequestNumber = number;
    persistState(options, state);
  }
}

function deliveryBinding(options) {
  return { ...stateBinding(options), pr: options.pr };
}

function effectRecord(state, effect, effectClass) {
  const existing = state.effects[effect];
  if (existing) {
    refuse(existing.class !== effectClass, "STATE_INVALID", "effect class changed");
    return existing;
  }
  const created = { class: effectClass, phase: "intent_recorded", attempts: 0 };
  state.effects[effect] = created;
  return created;
}

function markDispatch(options, state, effect, effectClass) {
  const record = effectRecord(state, effect, effectClass);
  record.phase = "dispatching";
  record.attempts += 1;
  persistState(options, state);
  return record;
}

function markEffect(options, state, effect, effectClass, phase, evidence = undefined) {
  const record = effectRecord(state, effect, effectClass);
  record.phase = phase;
  if (evidence !== undefined) record.evidence = evidence;
  persistState(options, state);
  return record;
}

function gitArgs(args) {
  return ["-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", ...args];
}

function gitChecked(run, cwd, args) {
  return checkedRun(run, "git", gitArgs(args), { cwd });
}

function gitRaw(run, cwd, args) {
  return run("git", gitArgs(args), { cwd });
}

function commonDirectory(run, cwd) {
  const output = gitChecked(run, cwd, ["rev-parse", "--path-format=absolute", "--git-common-dir"]);
  return canonicalExistingDirectory(output, "Git common directory");
}

function localFacts(run, options) {
  const controlCommon = commonDirectory(run, options.controlRepo);
  const remoteUrl = gitChecked(run, options.controlRepo, [
    "remote",
    "get-url",
    options.remote,
  ]);
  refuse(
    githubRepositoryIdentity(remoteUrl)?.toLowerCase() !== options.repo.toLowerCase(),
    "REMOTE_IDENTITY_MISMATCH",
    "Git remote does not match the explicit GitHub repository",
  );
  const localBranch = gitChecked(run, options.controlRepo, [
    "rev-parse",
    "--verify",
    `refs/heads/${options.branch}`,
  ]);
  refuse(
    localBranch !== options.candidate,
    "LOCAL_REF_CHANGED",
    "local Task ref is not the Candidate",
  );
  const candidateExists = gitRaw(run, options.controlRepo, [
    "cat-file",
    "-e",
    `${options.candidate}^{commit}`,
  ]);
  refuse(
    candidateExists.status !== 0,
    "CANDIDATE_MISSING",
    "Candidate commit is unavailable",
  );
  const descends = gitRaw(run, options.controlRepo, [
    "merge-base",
    "--is-ancestor",
    options.base,
    options.candidate,
  ]);
  refuse(
    descends.status !== 0,
    "CANDIDATE_BASE_MISMATCH",
    "Candidate does not descend from the exact base",
  );
  const records = worktreeRecords(
    gitChecked(run, options.controlRepo, ["worktree", "list", "--porcelain"]),
  );
  const normalizedRecordPath = (record) => {
    try {
      return realpathSync(record.worktree);
    } catch {
      return resolve(record.worktree);
    }
  };
  const targetRecords = records.filter(
    (record) => normalizedRecordPath(record) === options.checkout,
  );
  refuse(
    targetRecords.length > 1,
    "WORKTREE_REGISTRATION_AMBIGUOUS",
    "Task checkout has duplicate Git registrations",
  );
  const target = targetRecords[0] ?? null;
  const foreignConsumers = records.filter(
    (record) =>
      record.branch === `refs/heads/${options.branch}` && record !== target,
  );
  refuse(
    foreignConsumers.length > 0,
    "LOCAL_REF_HAS_FOREIGN_CONSUMER",
    "another worktree consumes the Task branch",
  );

  if (options.checkoutState === "present") {
    const head = gitChecked(run, options.checkout, ["rev-parse", "HEAD"]);
    const branch = gitChecked(run, options.checkout, ["branch", "--show-current"]);
    const status = gitChecked(run, options.checkout, [
      "status",
      "--porcelain=v2",
      "--untracked-files=all",
    ]);
    const checkoutRoot = canonicalExistingDirectory(
      gitChecked(run, options.checkout, ["rev-parse", "--show-toplevel"]),
      "Git checkout root",
    );
    refuse(head !== options.candidate, "CANDIDATE_MOVED", "checkout HEAD is not the Candidate");
    refuse(branch !== options.branch, "BRANCH_MOVED", "checkout is not on the owned Task branch");
    refuse(status !== "", "WORKTREE_DIRTY", "Task checkout is dirty");
    refuse(checkoutRoot !== options.checkout, "CHECKOUT_MOVED", "Task checkout identity changed");
    refuse(
      commonDirectory(run, options.checkout) !== controlCommon,
      "REPOSITORY_IDENTITY_MISMATCH",
      "checkout and control repository do not share one Git common directory",
    );
    refuse(
      target?.HEAD !== options.candidate ||
        target?.branch !== `refs/heads/${options.branch}`,
      "WORKTREE_OWNERSHIP_MISMATCH",
      "Task checkout registration changed",
    );
  } else {
    refuse(
      existsSync(options.checkout) || target !== null,
      "RECLAIMED_CHECKOUT_REAPPEARED",
      "reclaimed Task checkout path or registration reappeared",
    );
  }
  return {
    branch: options.branch,
    candidate: localBranch,
    checkout: options.checkout,
    checkoutState: options.checkoutState,
    commonDirectoryHash: digest(controlCommon),
    registration: target === null ? "absent" : "owned_present",
    remoteIdentity: options.repo,
    clean: true,
  };
}

function parseLsRemote(result, ref, { absentAllowed = false } = {}) {
  if (result.status === 2 && result.stdout.trim() === "" && absentAllowed) return null;
  if (result.error || result.status !== 0) {
    throw new CoordinatorError(
      "REMOTE_OBSERVATION_UNAVAILABLE",
      "remote ref observation was unavailable",
      { status: result.status, diagnostic: boundedText(result.stderr || result.error?.message) },
    );
  }
  const rows = result.stdout.trim().split("\n").filter(Boolean);
  refuse(rows.length !== 1, "REMOTE_OBSERVATION_AMBIGUOUS", "remote ref observation was ambiguous");
  const [sha, observedRef, ...extra] = rows[0].split("\t");
  refuse(
    extra.length > 0 || observedRef !== ref || !SHA_PATTERN.test(sha),
    "REMOTE_OBSERVATION_INVALID",
    "remote ref observation did not match the exact ref",
  );
  return sha;
}

function observeRemoteRef(run, options, branch, absentAllowed = false) {
  const ref = `refs/heads/${branch}`;
  const result = gitRaw(run, options.controlRepo, [
    "ls-remote",
    "--exit-code",
    "--heads",
    options.remote,
    ref,
  ]);
  return parseLsRemote(result, ref, { absentAllowed });
}

function liveRefFacts(run, options) {
  const base = observeRemoteRef(run, options, options.baseRef, false);
  const head = observeRemoteRef(run, options, options.branch, true);
  refuse(base !== options.base, "BASE_MOVED", "live base no longer matches the reviewed base");
  return { base, head };
}

function taskFacts(run, options) {
  const value = runJson(run, "bd", [
    "--actor",
    options.actor,
    "show",
    options.task,
    "--json",
  ], { cwd: options.controlRepo });
  refuse(!Array.isArray(value) || value.length !== 1, "TASK_AMBIGUOUS", "Beads Task lookup was ambiguous");
  const task = value[0];
  refuse(task.id !== options.task, "TASK_IDENTITY_MISMATCH", "Beads returned a different Task");
  refuse(task.issue_type !== "task", "TASK_TYPE_INVALID", "Beads record is not a Task");
  refuse(task.status !== "in_progress", "TASK_STATE_INVALID", "Task is not in progress");
  refuse(
    typeof task.assignee !== "string" || !ACTOR_PATTERN.test(task.assignee),
    "TASK_OWNER_INVALID",
    "Task has no exact Paseo Task Agent assignee",
  );
  refuse(typeof task.title !== "string" || task.title.length === 0, "TASK_TITLE_INVALID", "Task title is missing");
  refuse(
    typeof task.acceptance_criteria !== "string" ||
      task.acceptance_criteria.length === 0 ||
      task.acceptance_criteria.length > 100_000,
    "TASK_ACCEPTANCE_INVALID",
    "Task acceptance criteria are missing or oversized",
  );
  return {
    acceptanceCriteria: task.acceptance_criteria,
    assignee: task.assignee,
    id: task.id,
    status: task.status,
    title: task.title,
  };
}

function durableReviewReferences(run, options) {
  const comments = runJson(run, "bd", [
    "--actor",
    options.actor,
    "comments",
    options.task,
    "--json",
  ], { cwd: options.controlRepo });
  refuse(!Array.isArray(comments), "TASK_COMMENTS_INVALID", "Beads comments are invalid");
  refuse(comments.length > 1_000, "TASK_COMMENTS_OVERSIZE", "Beads comments exceed the bounded review manifest");
  const references = comments.map((comment) => {
    refuse(
      typeof comment.id !== "string" ||
        typeof comment.author !== "string" ||
        typeof comment.text !== "string" ||
        typeof comment.created_at !== "string",
      "TASK_COMMENT_INVALID",
      "a Beads comment lacks bounded identity fields",
    );
    return {
      author: comment.author,
      createdAt: comment.created_at,
      id: comment.id,
      textHash: digest(comment.text),
      text: comment.text,
    };
  });
  const humanDecisions = references
    .filter((reference) => /^HUMAN DECISION\b/mu.test(reference.text))
    .map(({ text, ...reference }) => reference)
    .toSorted((left, right) => compareText(left.id, right.id));
  const priorFindings = references
    .filter((reference) =>
      isPriorReviewFinding(reference.text, options.candidate),
    )
    .map(({ text, ...reference }) => reference)
    .toSorted((left, right) => compareText(left.id, right.id));
  return { humanDecisions, priorFindings };
}

function reviewContextFacts(run, options, task, validation) {
  const { humanDecisions, priorFindings } = durableReviewReferences(
    run,
    options,
  );
  const repository =
    options.checkoutState === "present" ? options.checkout : options.controlRepo;
  const candidateTree = gitChecked(run, repository, [
    "show",
    "-s",
    "--format=%T",
    options.candidate,
  ]);
  const baseTree = gitChecked(run, repository, [
    "show",
    "-s",
    "--format=%T",
    options.base,
  ]);
  const rawDiff = gitChecked(run, repository, [
    "diff-tree",
    "--no-commit-id",
    "--raw",
    "-z",
    "--full-index",
    options.base,
    options.candidate,
  ]);
  const changedPathOutput = gitChecked(run, repository, [
    "diff",
    "--name-only",
    "-z",
    options.base,
    options.candidate,
  ]);
  const changedPaths = changedPathOutput.split("\0").filter(Boolean).toSorted();
  const manifest = {
    schemaVersion: 1,
    authoritative: false,
    task: {
      acceptanceCriteria: task.acceptanceCriteria,
      acceptanceCriteriaHash: digest(task.acceptanceCriteria),
      id: task.id,
      title: task.title,
    },
    humanDecisions,
    candidate: { sha: options.candidate, tree: candidateTree },
    base: { ref: options.baseRef, sha: options.base, tree: baseTree },
    diff: {
      changedPathCount: changedPaths.length,
      changedPaths,
      format: "git-diff-tree-raw-v1",
      sha256: createHash("sha256").update(rawDiff).digest("hex"),
    },
    priorFindings,
    authorValidation: validation,
    ownership: {
      actor: options.actor,
      agentId: options.agentId,
      branch: options.branch,
      checkout: options.checkout,
      checkoutState: options.checkoutState,
      headOwner: options.headOwner,
      ownershipTokenHash: digest(options.ownership),
      remote: options.remote,
      repository: options.repo,
      repositoryId: options.repoId,
      taskAssignee: task.assignee,
      lifecycleState: options.lifecycleState,
      workspaceId: options.workspaceId,
    },
    pendingGates: [
      "independent_review",
      "publication",
      "remote_ci",
      "feedback",
      "integration",
      "cleanup",
      "task_closure",
    ],
  };
  return { ...manifest, manifestHash: digest(manifest) };
}

function repositoryFacts(run, options) {
  const repository = runJson(run, "gh", ["api", `repos/${options.repo}`]);
  refuse(
    repository.id !== options.repoId ||
      String(repository.full_name ?? "").toLowerCase() !== options.repo.toLowerCase(),
    "GITHUB_REPOSITORY_MISMATCH",
    "GitHub repository identity changed",
  );
  refuse(
    repository.archived === true || repository.disabled === true,
    "GITHUB_REPOSITORY_UNAVAILABLE",
    "GitHub repository is archived or disabled",
  );
  return { id: repository.id, name: repository.full_name };
}

function validatedPaseoAgent(agent, options, task) {
  refuse(!isObject(agent), "PASEO_AGENT_RESPONSE_INVALID", "Paseo agent response is invalid");
  refuse(agent.Id !== options.agentId, "PASEO_AGENT_MISMATCH", "Paseo returned a different agent");
  refuse(agent.Name !== task.title, "PASEO_AGENT_TITLE_MISMATCH", "Task Agent title does not match the Task");
  refuse(agent.ParentAgentId !== null, "PASEO_AGENT_PARENTED", "Task Agent is not top-level");
  refuse(
    typeof agent.Archived !== "boolean" ||
      !PASEO_AGENT_STATUSES.has(agent.Status) ||
      typeof agent.Cwd !== "string",
    "PASEO_AGENT_RESPONSE_INVALID",
    "Paseo agent response lacks bounded lifecycle facts",
  );
  refuse(
    agent.Archived
      ? typeof agent.ArchivedAt !== "string" ||
        agent.ArchivedAt.length === 0 ||
        agent.ArchivedAt.length > 64 ||
        !Number.isFinite(Date.parse(agent.ArchivedAt))
      : agent.ArchivedAt !== null && agent.ArchivedAt !== undefined,
    "PASEO_AGENT_RESPONSE_INVALID",
    "Paseo agent archive facts are invalid",
  );
  return agent;
}

function validatedPaseoWorkspaces(workspaces) {
  refuse(!Array.isArray(workspaces), "PASEO_WORKSPACES_INVALID", "Paseo workspace list is invalid");
  refuse(
    workspaces.some(
      (workspace) =>
        !isObject(workspace) ||
        typeof workspace.workspaceId !== "string" ||
        typeof workspace.cwd !== "string" ||
        typeof workspace.isolation !== "string",
    ),
    "PASEO_WORKSPACES_INVALID",
    "Paseo workspace list lacks bounded identity facts",
  );
  return workspaces;
}

function paseoFacts(run, options, task) {
  if (options.lifecycleState === "none") {
    return { binding: "none", agent: null, workspace: null, verified: true };
  }
  if (options.lifecycleState === "reclaimed") {
    return {
      binding: "recorded_reclaimed",
      agent: { id: options.agentId },
      workspace: { id: options.workspaceId },
      verified: false,
    };
  }
  const agent = validatedPaseoAgent(
    runJson(run, "paseo", ["inspect", options.agentId, "--json"]),
    options,
    task,
  );
  refuse(agent.Archived === true, "PASEO_AGENT_NOT_ACTIVE", "Task Agent is already archived");
  refuse(
    canonicalExistingDirectory(agent.Cwd, "Task Agent cwd") !== options.checkout,
    "PASEO_AGENT_CHECKOUT_MISMATCH",
    "Task Agent cwd does not match the Task checkout",
  );
  const workspaces = validatedPaseoWorkspaces(
    runJson(run, "paseo", ["workspace", "ls", "--json"]),
  );
  const matches = workspaces.filter((workspace) => workspace.workspaceId === options.workspaceId);
  // A restored, renamed, or re-created Execution Workspace card no longer
  // resolves at its recorded ID while the Task Agent keeps running in the same
  // checkout. The delivery lane still needs the exact worktree, which the local
  // Git facts already prove, so the workspace is reported as recorded rather
  // than verified instead of failing the whole handoff closed.
  if (options.lifecycleState === "restored") {
    refuse(
      matches.length === 1,
      "PASEO_WORKSPACE_RESTORATION_NOT_OBSERVED",
      "recorded Paseo workspace is still active once and must be bound as active",
    );
    refuse(
      matches.length > 1,
      "PASEO_WORKSPACE_AMBIGUOUS",
      "recorded Paseo workspace identity is duplicated",
    );
    return {
      binding: "restored",
      agent: {
        archived: false,
        archivedAt: null,
        id: agent.Id,
        status: agent.Status,
      },
      workspace: { id: options.workspaceId, verified: false },
      verified: true,
    };
  }
  refuse(matches.length !== 1, "PASEO_WORKSPACE_AMBIGUOUS", "exact Paseo workspace is not active once");
  const workspace = matches[0];
  refuse(
    canonicalExistingDirectory(workspace.cwd, "Paseo workspace cwd") !== options.checkout,
    "PASEO_WORKSPACE_CHECKOUT_MISMATCH",
    "Paseo workspace does not match the Task checkout",
  );
  refuse(workspace.isolation !== "worktree", "PASEO_WORKSPACE_NOT_ISOLATED", "Paseo workspace is not a worktree");
  return {
    binding: "active",
    agent: {
      archived: false,
      archivedAt: null,
      id: agent.Id,
      status: agent.Status,
    },
    workspace: {
      id: workspace.workspaceId,
      isolation: workspace.isolation,
    },
    verified: true,
  };
}

function marker(options) {
  return `<!-- director-coordinator task=${options.task} branch=${options.branch} owner-sha256=${digest(options.ownership)} -->`;
}

function exactPullRequests(run, options) {
  validatePullRequestInputs(options);
  const pulls = runJson(run, "gh", [
    "api",
    `repos/${options.repo}/pulls?state=all&head=${encodeURIComponent(`${options.headOwner}:${options.branch}`)}&base=${encodeURIComponent(options.baseRef)}&per_page=100`,
  ]);
  refuse(!Array.isArray(pulls), "PULL_REQUEST_LIST_INVALID", "GitHub pull request list is invalid");
  refuse(
    pulls.length >= 100,
    "PULL_REQUESTS_INCOMPLETE",
    "GitHub pull requests exceeded the bounded page",
  );
  const sameRefs = pulls.filter(
    (pull) =>
      pull?.head?.ref === options.branch &&
      pull?.base?.ref === options.baseRef &&
      String(pull?.head?.user?.login ?? "").toLowerCase() === options.headOwner.toLowerCase() &&
      pull?.head?.repo?.id === options.repoId,
  );
  const expectedMarker = marker(options);
  const exact = sameRefs.filter((pull) => String(pull.body ?? "").includes(expectedMarker));
  const openUnowned = sameRefs.filter(
    (pull) => pull.state === "open" && !String(pull.body ?? "").includes(expectedMarker),
  );
  refuse(
    openUnowned.length > 0,
    "PULL_REQUEST_OWNERSHIP_AMBIGUOUS",
    "an open pull request with the owned refs lacks the durable ownership marker",
  );
  const open = exact.filter((pull) => pull.state === "open");
  refuse(open.length > 1, "PULL_REQUEST_DUPLICATE", "multiple open pull requests match one Task intent");
  if (options.pr === "absent") return open[0] ?? null;
  const selected = exact.filter((pull) => pull.number === options.pr);
  refuse(selected.length > 1, "PULL_REQUEST_DUPLICATE", "pull request identity is duplicated");
  refuse(
    selected.length === 0 && open.length > 0,
    "PULL_REQUEST_NUMBER_MISMATCH",
    "another owned pull request is open",
  );
  return selected[0] ?? null;
}

function validateReview(options, routing) {
  refuse(!options.reviewFile, "OPTION_REQUIRED", "--review-file is required");
  const review = readJsonFile(options.reviewFile, "review evidence");
  assertExactKeys(
    review,
    [
      "schemaVersion",
      "task",
      "candidate",
      "base",
      "verdict",
      "reviewer",
      "dimensions",
      "p2Risks",
      "humanP2Acceptance",
    ],
    "review evidence",
  );
  refuse(review.schemaVersion !== 1, "REVIEW_SCHEMA_UNSUPPORTED", "review schema version is unsupported");
  refuse(
    review.task !== options.task || review.candidate !== options.candidate || review.base !== options.base,
    "REVIEW_BINDING_MISMATCH",
    "review does not bind the exact Task, Candidate, and base",
  );
  refuse(review.verdict !== "approve_candidate", "REVIEW_NOT_APPROVED", "exact Candidate lacks approval");
  assertExactKeys(
    review.reviewer,
    ["agentId", "parentAgentId", "detached", "checkoutCommit"],
    "reviewer evidence",
  );
  requireString(review.reviewer.agentId, ID_PATTERN, "reviewer agent ID");
  const reviewerActor = review.reviewer.agentId.startsWith("paseo:")
    ? review.reviewer.agentId
    : `paseo:${review.reviewer.agentId}`;
  refuse(
    reviewerActor === routing.manifest.taskAssignee ||
      reviewerActor === options.actor,
    "REVIEWER_NOT_INDEPENDENT",
    "Reviewer Agent identity matches the Task Agent or coordinator actor",
  );
  refuse(
    review.reviewer.agentId !== routing.harness.reviewId,
    "REVIEWER_HARNESS_IDENTITY_MISMATCH",
    "review evidence and review harness identify different Reviewers",
  );
  refuse(review.reviewer.parentAgentId !== null, "REVIEWER_NOT_INDEPENDENT", "Reviewer Agent is not top-level");
  refuse(review.reviewer.detached !== true, "REVIEWER_NOT_DETACHED", "Reviewer checkout was not detached");
  refuse(
    review.reviewer.checkoutCommit !== options.candidate,
    "REVIEW_CHECKOUT_MISMATCH",
    "Reviewer checkout was not the Candidate",
  );
  refuse(
    !Array.isArray(review.dimensions) ||
      canonicalJson(review.dimensions) !==
        canonicalJson(
          REVIEW_DIMENSIONS.map((id) => ({ id, status: "covered" })),
        ),
    "REVIEW_DIMENSIONS_INCOMPLETE",
    "review does not cover every mandatory dimension",
  );
  refuse(!Array.isArray(review.p2Risks), "REVIEW_SCHEMA_INVALID", "review P2 risks must be an array");
  const p2Risks = review.p2Risks.map((risk) => requireString(risk, ID_PATTERN, "P2 risk code"));
  refuse(new Set(p2Risks).size !== p2Risks.length, "REVIEW_SCHEMA_INVALID", "P2 risk codes must be unique");
  if (p2Risks.length > 0) {
    refuse(
      !isObject(review.humanP2Acceptance),
      "P2_ACCEPTANCE_MISSING",
      "P2 risk lacks recorded human acceptance",
    );
    assertExactKeys(
      review.humanP2Acceptance,
      ["actorKind", "recordId", "acceptedRiskCodes"],
      "human P2 acceptance",
    );
    refuse(
      review.humanP2Acceptance.actorKind !== "human",
      "P2_ACCEPTANCE_MISSING",
      "P2 risk lacks recorded human acceptance",
    );
    requireString(review.humanP2Acceptance.recordId, ID_PATTERN, "P2 acceptance record ID");
    refuse(
      canonicalJson(review.humanP2Acceptance.acceptedRiskCodes?.toSorted()) !== canonicalJson(p2Risks.toSorted()),
      "P2_ACCEPTANCE_MISSING",
      "recorded human acceptance does not cover every P2 risk",
    );
  } else {
    refuse(
      review.humanP2Acceptance !== null,
      "REVIEW_SCHEMA_INVALID",
      "humanP2Acceptance must be null when no P2 risk exists",
    );
  }
  return {
    digest: digest(review),
    reviewerAgentId: review.reviewer.agentId,
    verdict: review.verdict,
  };
}

function manifestDocument(value) {
  return value?.schemaVersion === 1 &&
    value?.command === "review-handoff" &&
    value?.outcome === "ready"
    ? value.result?.manifest
    : value;
}

function validateReviewManifestEvidence(options) {
  refuse(!options.manifestFile, "OPTION_REQUIRED", "--manifest-file is required");
  const manifest = manifestDocument(
    readJsonFile(options.manifestFile, "review handoff manifest"),
  );
  refuse(
    containsProtectedMaterial(
      manifest,
      options.ownership,
      options.ownershipFile,
      options.paseoPassword,
      options.paseoCredentialFile,
    ),
    "REVIEW_MANIFEST_OWNERSHIP_MATERIAL_FORBIDDEN",
    "review handoff manifest contains raw ownership material",
  );
  refuse(
    !isObject(manifest) ||
      manifest.schemaVersion !== 1 ||
      manifest.authoritative !== false ||
      manifest.task?.id !== options.task ||
      manifest.candidate?.sha !== options.candidate ||
      manifest.base?.sha !== options.base,
    "REVIEW_MANIFEST_BINDING_MISMATCH",
    "review handoff manifest is not bound to the exact Task/Candidate/base",
  );
  refuse(
    !SHA_PATTERN.test(manifest.candidate?.tree ?? "") ||
      manifest.base?.ref !== options.baseRef ||
      !SHA_PATTERN.test(manifest.base?.tree ?? "") ||
      !/^[0-9a-f]{64}$/u.test(manifest.task?.acceptanceCriteriaHash ?? "") ||
      digest(manifest.task?.acceptanceCriteria) !==
        manifest.task?.acceptanceCriteriaHash ||
      !Array.isArray(manifest.humanDecisions) ||
      !Array.isArray(manifest.priorFindings) ||
      manifest.diff?.format !== "git-diff-tree-raw-v1" ||
      !/^[0-9a-f]{64}$/u.test(manifest.diff?.sha256 ?? "") ||
      !Array.isArray(manifest.diff?.changedPaths) ||
      manifest.diff.changedPathCount !== manifest.diff.changedPaths.length ||
      !/^[0-9a-f]{64}$/u.test(manifest.authorValidation?.digest ?? "") ||
      !Array.isArray(manifest.authorValidation?.checks) ||
      canonicalJson(manifest.pendingGates) !== canonicalJson([
        "independent_review",
        "publication",
        "remote_ci",
        "feedback",
        "integration",
        "cleanup",
        "task_closure",
      ]),
    "REVIEW_MANIFEST_SCHEMA_INVALID",
    "review handoff manifest lacks the complete review routing contract",
  );
  assertExactKeys(
    manifest.ownership,
    HANDOFF_OWNERSHIP_KEYS,
    "review handoff ownership binding",
    "REVIEW_MANIFEST_SCHEMA_INVALID",
  );
  refuse(
    manifest.ownership?.actor !== manifest.ownership?.taskAssignee ||
      !ACTOR_PATTERN.test(manifest.ownership?.taskAssignee ?? "") ||
      manifest.ownership?.agentId !== options.agentId ||
      manifest.ownership?.branch !== options.branch ||
      manifest.ownership?.checkout !== options.checkout ||
      !(
        manifest.ownership?.checkoutState === options.checkoutState ||
        (manifest.ownership?.checkoutState === "present" &&
          options.checkoutState === "reclaimed")
      ) ||
      manifest.ownership?.headOwner !== options.headOwner ||
      manifest.ownership?.ownershipTokenHash !== digest(options.ownership) ||
      manifest.ownership?.remote !== options.remote ||
      manifest.ownership?.repository !== options.repo ||
      manifest.ownership?.repositoryId !== options.repoId ||
      !(
        manifest.ownership?.lifecycleState === options.lifecycleState ||
        (LIVE_LIFECYCLE_STATES.has(manifest.ownership?.lifecycleState) &&
          options.lifecycleState === "reclaimed") ||
        (manifest.ownership?.lifecycleState === "active" &&
          options.lifecycleState === "restored")
      ) ||
      manifest.ownership?.workspaceId !== options.workspaceId,
    "REVIEW_MANIFEST_OWNERSHIP_MISMATCH",
    "review handoff manifest ownership binding changed",
  );
  const withoutHash = { ...manifest };
  delete withoutHash.manifestHash;
  refuse(
    manifest.manifestHash !== digest(withoutHash),
    "REVIEW_MANIFEST_HASH_MISMATCH",
    "review handoff manifest hash is invalid",
  );
  return {
    acceptanceCriteriaHash: manifest.task.acceptanceCriteriaHash,
    authorValidationDigest: manifest.authorValidation.digest,
    digest: manifest.manifestHash,
    humanDecisions: manifest.humanDecisions,
    priorFindings: manifest.priorFindings,
    taskAssignee: manifest.ownership.taskAssignee,
  };
}

function validateReviewHarnessEvidence(options, manifest) {
  refuse(
    !options.reviewHarnessFile,
    "OPTION_REQUIRED",
    "--review-harness-file is required",
  );
  const harness = readJsonFile(options.reviewHarnessFile, "review harness evidence");
  const result = harness?.result;
  refuse(
    harness?.schemaVersion !== 1 ||
      harness?.command !== "review-harness" ||
      harness?.outcome !== "complete" ||
      result?.harnessVersion !== 2 ||
      result?.authoritative !== false ||
      !ID_PATTERN.test(result?.reviewId ?? "") ||
      result?.manifestHash !== manifest.digest ||
      result?.candidate !== options.candidate ||
      result?.base !== options.base,
    "REVIEW_HARNESS_BINDING_MISMATCH",
    "review harness result is not bound to the exact manifest/Candidate/base",
  );
  refuse(
    result.attempt !== 1 || result.reason !== "initial" ||
      result.reasonRecordHash !== null,
    "REVIEW_HARNESS_ATTEMPT_INVALID",
    "review harness attempt violates the full-CI latency contract",
  );
  refuse(
    !Array.isArray(result.results) ||
      canonicalJson(result.results.map((item) => item.id)) !==
        canonicalJson(["maintained-linux-ci", "manifest-identity"]) ||
      result.results.some((item) => item.status !== "passed"),
    "REVIEW_HARNESS_NOT_PASSED",
    "review harness results are incomplete or non-passing",
  );
  const completeCi = result.completeCi;
  refuse(
    !isObject(completeCi) ||
      completeCi.source !== "authoritative-remote" ||
      typeof completeCi.observationId !== "string" ||
      completeCi.observationId.length === 0 ||
      completeCi.startedByReview !== false,
    "REVIEW_HARNESS_CI_SOURCE_INVALID",
    "review harness did not consume the authoritative remote CI",
  );
  return {
    attempt: result.attempt,
    completeCi: {
      observationId: completeCi.observationId ?? null,
      source: completeCi.source,
    },
    digest: digest(harness),
    harnessVersion: result.harnessVersion,
    reason: result.reason,
    reviewId: result.reviewId,
  };
}

/** Binds every post-review gate to the one remote CI the Review consumed. */
function requireAuthoritativeRemoteCi(options, harness) {
  refuse(
    !options.remoteCiFile,
    "OPTION_REQUIRED",
    "--remote-ci-file is required when review consumed the authoritative remote CI",
  );
  const document = readJsonFile(options.remoteCiFile, "remote CI observation");
  const observation =
    document?.schemaVersion === OUTPUT_SCHEMA_VERSION &&
    document?.command === "remote-ci" &&
    document?.outcome === "ready"
      ? document.result
      : document;
  refuse(
    !isObject(observation) ||
      observation.authoritative !== true ||
      observation.task !== options.task ||
      observation.candidate !== options.candidate ||
      observation.base !== options.base,
    "REMOTE_CI_BINDING_MISMATCH",
    "remote CI observation is not bound to the exact Task/Candidate/base",
  );
  refuse(
    observation.observationId !== harness.completeCi.observationId,
    "REMOTE_CI_OBSERVATION_MISMATCH",
    "review consumed a different authoritative remote CI observation",
  );
  return {
    observationId: observation.observationId,
    workflowRunId: observation.remote?.workflow?.id ?? null,
  };
}

function validateReviewRoutingEvidence(options) {
  const manifest = validateReviewManifestEvidence(options);
  const harness = validateReviewHarnessEvidence(options, manifest);
  return { manifest, harness };
}

function requireCurrentReviewContext(run, options, routing, task, validation) {
  const current = durableReviewReferences(run, options);
  refuse(
    routing.manifest.acceptanceCriteriaHash !==
        digest(task.acceptanceCriteria) ||
      routing.manifest.authorValidationDigest !== validation.digest ||
      routing.manifest.taskAssignee !== task.assignee ||
      canonicalJson(routing.manifest.humanDecisions) !==
        canonicalJson(current.humanDecisions) ||
      canonicalJson(routing.manifest.priorFindings) !==
        canonicalJson(current.priorFindings),
    "REVIEW_DURABLE_CONTEXT_MOVED",
    "Task acceptance, human decisions, prior findings, ownership, or validation changed",
  );
}

function validateValidation(options) {
  refuse(!options.validationFile, "OPTION_REQUIRED", "--validation-file is required");
  const validation = readJsonFile(options.validationFile, "validation evidence");
  assertExactKeys(validation, ["schemaVersion", "task", "candidate", "base", "checks"], "validation evidence");
  refuse(
    validation.schemaVersion !== 1 ||
      validation.task !== options.task ||
      validation.candidate !== options.candidate ||
      validation.base !== options.base,
    "VALIDATION_BINDING_MISMATCH",
    "validation does not bind the exact Task, Candidate, and base",
  );
  refuse(
    !Array.isArray(validation.checks) || validation.checks.length === 0,
    "VALIDATION_MISSING",
    "validation must contain at least one check",
  );
  const ids = [];
  for (const check of validation.checks) {
    assertExactKeys(check, ["id", "command", "status"], "validation check");
    ids.push(requireString(check.id, ID_PATTERN, "validation check ID"));
    refuse(
      !Array.isArray(check.command) ||
        check.command.length === 0 ||
        check.command.some((part) => typeof part !== "string" || part.length === 0),
      "VALIDATION_SCHEMA_INVALID",
      "validation command must be a non-empty argv array",
    );
    refuse(check.status !== "passed", "VALIDATION_NOT_PASSED", "a required local validation did not pass");
  }
  refuse(new Set(ids).size !== ids.length, "VALIDATION_SCHEMA_INVALID", "validation check IDs must be unique");
  return { checks: ids, digest: digest(validation) };
}

function baseSnapshot(run, options, { includePaseo = true } = {}) {
  const task = taskFacts(run, options);
  const local = localFacts(run, options);
  const refs = liveRefFacts(run, options);
  const repository = repositoryFacts(run, options);
  const pull = exactPullRequests(run, options);
  const paseo = includePaseo ? paseoFacts(run, options, task) : null;
  return {
    local,
    paseo,
    pullRequest: pull
      ? { number: pull.number, state: pull.state, head: pull.head?.sha ?? null, url: pull.html_url }
      : null,
    refs,
    repository,
    task,
  };
}

function requireRemoteCandidate(snapshot, options) {
  refuse(
    snapshot.refs.head !== options.candidate,
    "REMOTE_HEAD_MISMATCH",
    "remote Task branch is not the exact Candidate",
  );
}

function pullFacts(run, options, number) {
  const pull = runJson(run, "gh", ["api", `repos/${options.repo}/pulls/${number}`]);
  refuse(pull.number !== number, "PULL_REQUEST_NUMBER_MISMATCH", "GitHub returned a different pull request");
  refuse(
    pull?.head?.sha !== options.candidate ||
      pull?.head?.ref !== options.branch ||
      pull?.base?.ref !== options.baseRef ||
      pull?.head?.repo?.id !== options.repoId ||
      !String(pull.body ?? "").includes(marker(options)),
    "PULL_REQUEST_BINDING_MISMATCH",
    "pull request is not bound to the exact owned Candidate",
  );
  return pull;
}

function exactPullNumber(snapshot, options) {
  refuse(!snapshot.pullRequest, "PULL_REQUEST_MISSING", "owned pull request does not exist");
  refuse(
    options.pr === "absent" || snapshot.pullRequest.number !== options.pr,
    "PULL_REQUEST_NUMBER_REQUIRED",
    "an exact pull request number is required",
  );
  return options.pr;
}

function checkFacts(run, options) {
  refuse(
    !Array.isArray(options.requiredChecks) || options.requiredChecks.length === 0,
    "REQUIRED_CHECKS_MISSING",
    "at least one explicit --required-check is required",
  );
  const checks = runJson(
    run,
    "gh",
    ["api", `repos/${options.repo}/commits/${options.candidate}/check-runs?filter=latest&per_page=100`],
  );
  refuse(!Array.isArray(checks.check_runs), "CHECKS_INVALID", "GitHub check runs are invalid");
  refuse(
    checks.total_count !== checks.check_runs.length,
    "CHECKS_INCOMPLETE",
    "GitHub check run result was not complete in one bounded page",
  );
  for (const check of checks.check_runs) {
    refuse(check.head_sha !== options.candidate, "CHECK_SHA_MISMATCH", "check run is bound to another SHA");
    refuse(
      check.status !== "completed" || check.conclusion !== "success",
      "CHECK_NOT_PASSED",
      "a configured check is pending or non-passing",
      { check: boundedText(check.name, 200), status: check.status, conclusion: check.conclusion },
    );
  }
  for (const name of options.requiredChecks) {
    const matches = checks.check_runs.filter((check) => check.name === name);
    refuse(matches.length !== 1, "REQUIRED_CHECK_AMBIGUOUS", "required check is missing or ambiguous", { check: name });
  }
  const statuses = runJson(run, "gh", [
    "api",
    `repos/${options.repo}/commits/${options.candidate}/status?per_page=100`,
  ]);
  refuse(!Array.isArray(statuses.statuses), "STATUSES_INVALID", "GitHub commit statuses are invalid");
  refuse(
    statuses.total_count !== statuses.statuses.length,
    "STATUSES_INCOMPLETE",
    "GitHub commit statuses were not complete in one bounded page",
  );
  const checksOnly = statuses.total_count === 0;
  if (!checksOnly) {
    refuse(
      statuses.state !== "success" ||
        statuses.statuses.some(
          (status) =>
            status.sha !== options.candidate || status.state !== "success",
        ),
      "STATUS_NOT_PASSED",
      "combined commit status or a context is pending or non-passing",
    );
  }
  return {
    checkRuns: checks.check_runs.map((check) => ({ id: check.id, name: check.name })),
    required: options.requiredChecks,
    statusRollup: checksOnly ? "checks_only_no_statuses" : statuses.state,
    statuses: statuses.statuses.map((status) => ({ context: status.context, id: status.id })),
  };
}

/**
 * Observes the one authoritative complete remote Linux CI for the exact
 * Candidate. GitHub is the single execution site: proving exactly one workflow
 * run per Candidate is what lets an independent Review consume this result
 * concurrently instead of starting a second complete CI of its own.
 */
function remoteCiFacts(run, options) {
  refuse(
    !Array.isArray(options.requiredChecks) || options.requiredChecks.length === 0,
    "REQUIRED_CHECKS_MISSING",
    "at least one explicit --required-check is required",
  );
  refuse(!options.ciWorkflow, "OPTION_REQUIRED", "--ci-workflow is required");
  const runs = runJson(run, "gh", [
    "api",
    `repos/${options.repo}/actions/runs?head_sha=${options.candidate}&per_page=100`,
  ]);
  refuse(!Array.isArray(runs.workflow_runs), "REMOTE_CI_RUNS_INVALID", "GitHub workflow run list is invalid");
  refuse(
    runs.total_count !== runs.workflow_runs.length,
    "REMOTE_CI_RUNS_INCOMPLETE",
    "GitHub workflow runs were not complete in one bounded page",
  );
  refuse(
    runs.workflow_runs.some((entry) => entry?.head_sha !== options.candidate),
    "REMOTE_CI_SHA_MISMATCH",
    "a workflow run is bound to another SHA",
  );
  const authoritative = runs.workflow_runs.filter((entry) => entry?.name === options.ciWorkflow);
  refuse(
    authoritative.length === 0,
    "REMOTE_CI_ABSENT",
    "the authoritative complete CI workflow has not run for this Candidate",
  );
  refuse(
    authoritative.length > 1,
    "REMOTE_CI_NOT_AUTHORITATIVE",
    "more than one complete CI workflow run exists for this Candidate",
    { runs: authoritative.length },
  );
  const [authoritativeRun] = authoritative;
  refuse(
    authoritativeRun.status !== "completed",
    "REMOTE_CI_PENDING",
    "the authoritative complete CI workflow is still running",
    { status: boundedText(String(authoritativeRun.status ?? ""), 40) },
  );
  refuse(
    authoritativeRun.conclusion !== "success",
    "REMOTE_CI_FAILED",
    "the authoritative complete CI workflow did not pass",
    { conclusion: boundedText(String(authoritativeRun.conclusion ?? ""), 40) },
  );
  const checks = runJson(run, "gh", [
    "api",
    `repos/${options.repo}/commits/${options.candidate}/check-runs?filter=latest&per_page=100`,
  ]);
  refuse(!Array.isArray(checks.check_runs), "CHECKS_INVALID", "GitHub check runs are invalid");
  refuse(
    checks.total_count !== checks.check_runs.length,
    "CHECKS_INCOMPLETE",
    "GitHub check run result was not complete in one bounded page",
  );
  const observed = [];
  for (const name of options.requiredChecks) {
    const matches = checks.check_runs.filter((check) => check.name === name);
    refuse(matches.length !== 1, "REQUIRED_CHECK_AMBIGUOUS", "required check is missing or ambiguous", { check: name });
    const [check] = matches;
    refuse(check.head_sha !== options.candidate, "CHECK_SHA_MISMATCH", "check run is bound to another SHA");
    refuse(
      check.status !== "completed",
      "REMOTE_CI_PENDING",
      "a required remote check is still running",
      { check: boundedText(name, 200) },
    );
    refuse(
      check.conclusion !== "success",
      "REMOTE_CI_FAILED",
      "a required remote check did not pass",
      { check: boundedText(name, 200) },
    );
    observed.push({ id: check.id, name: check.name });
  }
  return {
    checkRuns: observed,
    required: options.requiredChecks,
    workflow: {
      conclusion: authoritativeRun.conclusion,
      headSha: authoritativeRun.head_sha,
      id: authoritativeRun.id,
      name: authoritativeRun.name,
      runAttempt: authoritativeRun.run_attempt ?? null,
      status: authoritativeRun.status,
    },
  };
}

/**
 * Records the single authoritative remote CI observation for one Candidate.
 * A second call with the same facts is idempotent; a call whose facts differ
 * refuses rather than replacing the recorded authority, so no second complete
 * CI can quietly become the one the gate trusts.
 */
function remoteCi(run, options) {
  const validation = validateValidation(options);
  const state = loadState(options);
  persistState(options, state);
  const snapshot = baseSnapshot(run, options, { includePaseo: false });
  requireRemoteCandidate(snapshot, options);
  const facts = remoteCiFacts(run, options);
  const observation = {
    authoritative: true,
    base: options.base,
    candidate: options.candidate,
    checks: [
      {
        command: ["github-actions", options.ciWorkflow],
        id: "maintained-linux-ci",
        status: "passed",
      },
    ],
    remote: facts,
    task: options.task,
  };
  const observationId = digest(observation);
  const record = effectRecord(state, "remote_ci.observe", "store_only");
  refuse(
    record.phase === "complete" && record.evidence?.observationId !== observationId,
    "REMOTE_CI_OBSERVATION_CHANGED",
    "another authoritative remote CI observation is already recorded for this Candidate",
  );
  markEffect(options, state, "remote_ci.observe", "store_only", "complete", {
    observationId,
    workflowRunId: facts.workflow.id,
  });
  return { ...observation, observationId, validation };
}

const REVIEW_THREADS_QUERY = `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){pullRequest(number:$number){reviewThreads(first:100){nodes{isResolved comments(first:20){nodes{author{login __typename}} pageInfo{hasNextPage}}}pageInfo{hasNextPage}}}}}`;

function feedbackFacts(run, options, pull) {
  refuse(
    (pull.requested_reviewers?.length ?? 0) > 0 || (pull.requested_teams?.length ?? 0) > 0,
    "FEEDBACK_PENDING",
    "pull request still has requested reviewers",
  );
  const reviews = runJson(run, "gh", ["api", `repos/${options.repo}/pulls/${pull.number}/reviews?per_page=100`]);
  refuse(!Array.isArray(reviews), "REVIEWS_INVALID", "GitHub reviews are invalid");
  refuse(reviews.length === 100, "REVIEWS_INCOMPLETE", "GitHub reviews exceed the bounded page");
  const currentReviews = reviews.filter(
    (review) => review.commit_id === options.candidate && review?.user?.type !== "Bot",
  );
  refuse(
    currentReviews.some((review) => review.state === "CHANGES_REQUESTED"),
    "FEEDBACK_UNRESOLVED",
    "current Candidate has a changes-requested review",
  );
  refuse(
    currentReviews.some(
      (review) => review.state === "COMMENTED" && String(review.body ?? "").trim() !== "",
    ),
    "FEEDBACK_UNRESOLVED",
    "current Candidate has ambiguous review feedback",
  );
  const issueComments = runJson(run, "gh", [
    "api",
    `repos/${options.repo}/issues/${pull.number}/comments?per_page=100`,
  ]);
  refuse(!Array.isArray(issueComments), "COMMENTS_INVALID", "GitHub comments are invalid");
  refuse(issueComments.length === 100, "COMMENTS_INCOMPLETE", "GitHub comments exceed the bounded page");
  refuse(
    issueComments.some((comment) => comment?.user?.type !== "Bot"),
    "FEEDBACK_UNRESOLVED",
    "pull request has human issue comments without an exact resolution fact",
  );
  const [owner, name] = options.repo.split("/");
  const threadResult = runJson(run, "gh", [
    "api",
    "graphql",
    "-f",
    `query=${REVIEW_THREADS_QUERY}`,
    "-F",
    `owner=${owner}`,
    "-F",
    `name=${name}`,
    "-F",
    `number=${pull.number}`,
  ]);
  const threads = threadResult?.data?.repository?.pullRequest?.reviewThreads;
  refuse(!threads || !Array.isArray(threads.nodes), "REVIEW_THREADS_INVALID", "review thread facts are invalid");
  refuse(threads.pageInfo?.hasNextPage === true, "REVIEW_THREADS_INCOMPLETE", "review threads exceed the bounded page");
  for (const thread of threads.nodes) {
    refuse(
      thread.comments?.pageInfo?.hasNextPage === true,
      "REVIEW_THREADS_INCOMPLETE",
      "a review thread exceeds the bounded page",
    );
    const human = thread.comments?.nodes?.some(
      (comment) => comment?.author?.__typename !== "Bot",
    );
    refuse(
      thread.isResolved !== true && human,
      "FEEDBACK_UNRESOLVED",
      "pull request has an unresolved human review thread",
    );
  }
  return {
    humanIssueComments: 0,
    unresolvedHumanThreads: 0,
    currentReviews: currentReviews.map((review) => ({ id: review.id, state: review.state })),
  };
}

function gateCore(run, options) {
  const routing = validateReviewRoutingEvidence(options);
  const review = validateReview(options, routing);
  const validation = validateValidation(options);
  const snapshot = baseSnapshot(run, options);
  requireCurrentReviewContext(
    run,
    options,
    routing,
    snapshot.task,
    validation,
  );
  requireRemoteCandidate(snapshot, options);
  const number = exactPullNumber(snapshot, options);
  const pull = pullFacts(run, options, number);
  refuse(pull.state !== "open" || pull.draft === true || pull.merged === true, "PULL_REQUEST_NOT_OPEN", "pull request is not an open non-draft PR");
  refuse(
    pull.mergeable !== true || pull.mergeable_state !== "clean",
    "PULL_REQUEST_NOT_MERGEABLE",
    "pull request is not freshly mergeable and clean",
  );
  const remoteCiBinding = requireAuthoritativeRemoteCi(options, routing.harness);
  const checks = checkFacts(run, options);
  const feedback = feedbackFacts(run, options, pull);
  return {
    checks,
    feedback,
    pullRequest: { number, url: pull.html_url },
    remoteCi: remoteCiBinding,
    review,
    routing,
    snapshot,
    validation,
  };
}

function fetchCommit(run, options, sha) {
  const result = gitRaw(run, options.controlRepo, [
    "fetch",
    "--no-tags",
    "--no-write-fetch-head",
    options.remote,
    sha,
  ]);
  refuse(
    result.error || result.status !== 0,
    "GIT_OBJECT_FETCH_FAILED",
    "exact integration object could not be fetched",
    { status: result.status, diagnostic: boundedText(result.stderr || result.error?.message) },
  );
}

function verifyIntegration(run, options, pull) {
  refuse(
    pull.merged !== true || !pull.merged_at || !SHA_PATTERN.test(pull.merge_commit_sha ?? ""),
    "INTEGRATION_NOT_PROVEN",
    "GitHub does not prove a merged pull request",
  );
  refuse(
    pull?.head?.sha !== options.candidate || pull?.head?.ref !== options.branch,
    "INTEGRATION_HEAD_MISMATCH",
    "merged pull request head is not the Candidate",
  );
  const mergeCommit = pull.merge_commit_sha;
  fetchCommit(run, options, mergeCommit);
  fetchCommit(run, options, options.candidate);
  const parents = gitChecked(run, options.controlRepo, ["show", "-s", "--format=%P", mergeCommit])
    .split(" ")
    .filter(Boolean);
  refuse(
    canonicalJson(parents) !== canonicalJson([options.base, options.candidate]),
    "INTEGRATION_PARENTS_MISMATCH",
    "merge commit does not have the exact base and Candidate parents",
  );
  const mergeTree = gitChecked(run, options.controlRepo, ["show", "-s", "--format=%T", mergeCommit]);
  const candidateTree = gitChecked(run, options.controlRepo, ["show", "-s", "--format=%T", options.candidate]);
  refuse(mergeTree !== candidateTree, "INTEGRATION_TREE_MISMATCH", "merge tree differs from the Candidate tree");
  const liveBase = observeRemoteRef(run, options, options.baseRef, false);
  fetchCommit(run, options, liveBase);
  const containsMerge = gitRaw(run, options.controlRepo, ["merge-base", "--is-ancestor", mergeCommit, liveBase]);
  refuse(containsMerge.status !== 0, "INTEGRATION_NOT_REACHABLE", "live base does not contain the merge commit");
  return { candidateTree, liveBase, mergeCommit, parents };
}

function prAfterPossibleMerge(run, options) {
  refuse(options.pr === "absent", "PULL_REQUEST_NUMBER_REQUIRED", "exact PR number is required");
  return pullFacts(run, options, options.pr);
}

function worktreeRecords(output) {
  const records = [];
  let current = {};
  for (const line of `${output}\n`.split("\n")) {
    if (line === "") {
      if (Object.keys(current).length > 0) records.push(current);
      current = {};
      continue;
    }
    const space = line.indexOf(" ");
    const key = space === -1 ? line : line.slice(0, space);
    const value = space === -1 ? true : line.slice(space + 1);
    current[key] = value;
  }
  return records;
}

function cleanupFacts(run, options, task, { allowAbsentWorkspace = false } = {}) {
  const common = commonDirectory(run, options.controlRepo);
  const records = worktreeRecords(gitChecked(run, options.controlRepo, ["worktree", "list", "--porcelain"]));
  const targetRecords = records.filter((record) => {
    try {
      return realpathSync(record.worktree) === options.checkout;
    } catch {
      return resolve(record.worktree) === options.checkout;
    }
  });
  refuse(targetRecords.length > 1, "WORKTREE_REGISTRATION_AMBIGUOUS", "Task worktree has duplicate registrations");
  const worktree = targetRecords[0] ?? null;
  const foreignConsumers = records.filter(
    (record) =>
      record.branch === `refs/heads/${options.branch}` && record !== worktree,
  );
  refuse(
    foreignConsumers.length > 0,
    "LOCAL_REF_HAS_FOREIGN_CONSUMER",
    "another registered worktree consumes the Task branch",
  );
  refuse(
    worktree === null && existsSync(options.checkout),
    "UNREGISTERED_PATH_PRESENT",
    "Task checkout path exists without its exact Git registration",
  );
  if (worktree) {
    refuse(
      worktree.HEAD !== options.candidate || worktree.branch !== `refs/heads/${options.branch}`,
      "WORKTREE_OWNERSHIP_MISMATCH",
      "Task worktree registration changed",
    );
    const allStatus = gitChecked(run, options.checkout, [
      "status",
      "--porcelain=v2",
      "--untracked-files=all",
      "--ignored=matching",
    ]);
    refuse(allStatus !== "", "WORKTREE_NOT_REMOVABLE", "Task worktree contains dirty or ignored material");
  }
  const localResult = gitRaw(run, options.controlRepo, [
    "rev-parse",
    "--verify",
    "--quiet",
    `refs/heads/${options.branch}`,
  ]);
  let localBranch = null;
  if (localResult.status === 0) localBranch = localResult.stdout.trim();
  else refuse(localResult.status !== 1, "LOCAL_REF_UNAVAILABLE", "local Task ref could not be observed");
  refuse(
    localBranch !== null && localBranch !== options.candidate,
    "LOCAL_REF_CHANGED",
    "local Task ref no longer equals the Candidate",
  );
  const remoteBranch = observeRemoteRef(run, options, options.branch, true);
  refuse(
    remoteBranch !== null && remoteBranch !== options.candidate,
    "REMOTE_REF_CHANGED",
    "remote Task ref no longer equals the Candidate",
  );

  let agent = null;
  let workspace = null;
  if (options.lifecycleState === "reclaimed") {
    agent = {
      id: options.agentId,
      observation: "recorded_reclaimed",
      verified: false,
    };
    workspace = {
      id: options.workspaceId,
      observation: "recorded_reclaimed",
      verified: false,
    };
  } else if (options.lifecycleState === "active" || options.lifecycleState === "restored") {
    const inspected = validatedPaseoAgent(
      runJson(run, "paseo", ["inspect", options.agentId, "--json"]),
      options,
      task,
    );
    if (inspected.Archived !== true) {
      refuse(inspected.Status === "running", "PASEO_AGENT_RUNNING", "Task Agent is still running");
      refuse(
        canonicalExistingDirectory(inspected.Cwd, "Task Agent cwd") !== options.checkout,
        "PASEO_AGENT_CHECKOUT_MISMATCH",
        "Task Agent cwd changed",
      );
    }
    agent = {
      archived: inspected.Archived === true,
      archivedAt: inspected.ArchivedAt ?? null,
      id: inspected.Id,
      status: inspected.Status,
    };
    const workspaces = validatedPaseoWorkspaces(
      runJson(run, "paseo", ["workspace", "ls", "--json"]),
    );
    const matches = workspaces.filter((item) => item.workspaceId === options.workspaceId);
    refuse(matches.length > 1, "PASEO_WORKSPACE_AMBIGUOUS", "Paseo workspace identity is ambiguous");
    if (matches.length === 1) {
      refuse(
        options.lifecycleState === "restored",
        "PASEO_WORKSPACE_RESTORATION_NOT_OBSERVED",
        "recorded Paseo workspace is active and must be bound as active",
      );
      const item = matches[0];
      refuse(
        canonicalExistingDirectory(item.cwd, "Paseo workspace cwd") !== options.checkout || item.isolation !== "worktree",
        "PASEO_WORKSPACE_OWNERSHIP_MISMATCH",
        "Paseo workspace ownership facts changed",
      );
      workspace = { id: item.workspaceId, isolation: item.isolation };
    } else if (options.lifecycleState === "restored") {
      // A restored card cannot be archived by identity, so cleanup records the
      // recorded binding instead of claiming a verified workspace resource.
      workspace = {
        id: options.workspaceId,
        observation: "recorded_restored",
        verified: false,
      };
    } else {
      refuse(
        !allowAbsentWorkspace,
        "PASEO_WORKSPACE_ABSENT",
        "Paseo workspace is absent before a recorded archive attempt",
      );
    }
  }
  return {
    agent,
    commonDirectoryHash: digest(common),
    localBranch,
    remoteBranch,
    worktree: worktree ? { branch: worktree.branch, head: worktree.HEAD } : null,
    workspace,
  };
}

/**
 * The verdict a comment states, if it states one. A `Verdict:` field is read
 * wherever it appears in the comment, including a token outside the contract
 * set, because this record holds one whose verdict is `inconclusive`. Failing
 * that, a contract verdict anywhere in the text is accepted.
 *
 * This is deliberately not a test of whether the comment is a report. A
 * coordinator comment narrating a previous Review states a verdict too. What
 * separates a report from a mention is who wrote it, which is checked
 * elsewhere and cannot be read out of the text at all.
 */
function statedVerdict(text) {
  const field = text.match(/^Verdict:\s*([A-Za-z][A-Za-z0-9_-]{2,31})\b/mu)?.[1];
  if (field !== undefined) return field;
  // The fallback reads the comment's own first line and nothing else. Read over
  // the whole text it matched any comment that merely quotes a verdict, and a
  // Reviewer writing a note mid-Review quotes the previous round's verdict
  // routinely: that made a note that concludes nothing bind as the Review's
  // report, which reduces the two-fact rule to the daemon's status alone on
  // exactly the resource that must not be reclaimed. A report states its
  // verdict where a reader looks first.
  const [firstLine] = text.split("\n", 1);
  return firstLine.match(
    /\b(approve_candidate|changes_requested|needs_human_decision)\b/u,
  )?.[1] ?? null;
}

/**
 * Whether a comment refers to a verdict anywhere at all. This is deliberately
 * wider than what binds, and is used only by the counters that make a missed
 * report visible. The two notions must not be the same one: narrowing what
 * binds is what stops an invention, and if the counters narrowed with it, the
 * reports the narrowing newly declines to read would become invisible at the
 * same moment — which is the coverage the counters exist to hold.
 */
function mentionsVerdict(text) {
  return (
    /^Verdict:\s*[A-Za-z]/mu.test(text) ||
    /\b(approve_candidate|changes_requested|needs_human_decision)\b/u.test(text)
  );
}

/**
 * Every Beads comment on one Task, projected into the facts the report binding
 * reads. Nothing is filtered: a shape gate here would decide, once and for
 * everything downstream, which comments exist at all.
 */
function taskReviewReports(run, options, taskId) {
  const comments = runJson(run, "bd", [
    "--actor",
    options.actor,
    "comments",
    taskId,
    "--json",
  ], { cwd: options.controlRepo });
  refuse(!Array.isArray(comments), "TASK_COMMENTS_INVALID", "Beads comments are invalid");
  refuse(
    comments.length > 1_000,
    "TASK_COMMENTS_OVERSIZE",
    "Beads comments exceed the bounded review manifest",
  );
  return comments.map((comment) => {
    refuse(
      typeof comment?.text !== "string" || typeof comment?.author !== "string",
      "TASK_COMMENT_INVALID",
      "a Beads comment lacks bounded identity fields",
    );
    return {
      author: comment.author,
      mentionsVerdict: mentionsVerdict(comment.text),
      text: comment.text,
      verdict: statedVerdict(comment.text),
    };
  });
}

/**
 * Whether the coordinator running this cleanup has durably abandoned this exact
 * Review.
 *
 * No observable fact distinguishes a Reviewer that will never report from one
 * that has not reported yet. Both are idle, both carry the same labels, both own
 * the same checkout, and neither can be asked. The difference is not a property
 * of the Reviewer at all: it is that some party stopped waiting for it. That
 * party is the coordinator that started the Review, and its decision is
 * knowable only if it records one.
 *
 * So abandonment is bound the same way a report is — by the actor that wrote it,
 * declared on the comment's own first line. The declaring actor must be the
 * actor running this cleanup, which is the coordinator identity frozen into the
 * Review, and the comment must name the Reviewer it abandons. This adds no way
 * to infer abandonment and no way to guess it; it adds a way to state it.
 */
function reviewAbandonment(reports, agentId, actor) {
  const reviewer = agentId.startsWith("paseo:") ? agentId : `paseo:${agentId}`;
  const bare = reviewer.slice("paseo:".length);
  const record = reports.find(
    (comment) =>
      comment.author === actor &&
      /^REVIEW ABANDONED\b/u.test(comment.text) &&
      (comment.text.includes(reviewer) || comment.text.includes(bare)),
  );
  return record === undefined ? null : { by: actor };
}

/**
 * Whether this exact Reviewer reported, and on what evidence.
 *
 * A report is bound by ONE fact: the Reviewer wrote it. Authorship is the only
 * property of a comment that another party cannot produce, and every content
 * rule tried here could be satisfied by a comment that merely describes a
 * Review rather than being one. The coordinator's routine record announcing
 * that it created a Reviewer names that Reviewer, names the Candidate it was
 * created for, and quotes an earlier Review's verdict; three tokens co-occur
 * and no report exists. Narrowing which tokens, or how near they must be, makes
 * that record harder to mistake without making it distinguishable, and the next
 * legible coordinator comment reintroduces it.
 *
 * The comment must also state a verdict. Without that, any note a Reviewer
 * writes mid-Review — and it sits idle between turns — would read as a
 * concluded Review, which reduces the two-fact rule to the daemon's status
 * alone on exactly the resource that must not be reclaimed.
 *
 * Both failures this rule can produce are misses, never inventions:
 *
 *   a report the Reviewer did not write, such as a verdict the coordinator
 *   transcribed on its behalf, binds nothing;
 *   a report by the Reviewer that states no verdict binds nothing.
 *
 * Both are surfaced rather than hidden, and by different counts because they
 * leave different traces. `unboundEvidence` counts comments that name this
 * Reviewer and state a verdict without binding it, which is what a transcribed
 * report leaves behind. `unreadableReports` counts comments this Reviewer wrote
 * whose verdict this rule could not read, which is what a report concluding in
 * a vocabulary outside the contract leaves behind — the record holds one whose
 * heading concludes INCONCLUSIVE and which carries no `Verdict:` field. Either
 * count beside an absent `reportSource` tells an operator that something is
 * there which this rule declined to read, rather than letting the absence read
 * as a Review that never happened. Neither authorises anything; every binding
 * still requires the report itself.
 */
function reviewerReport(reports, agentId) {
  const actor = agentId.startsWith("paseo:") ? agentId : `paseo:${agentId}`;
  // The last report wins: a Reviewer that corrects itself states its operative
  // verdict last, and a note written before the report must not displace it.
  const authored = reports.filter(
    (comment) => comment.author === actor && comment.verdict !== null,
  );
  const report = authored.at(-1);
  const bare = actor.slice("paseo:".length);
  const unboundEvidence = reports.filter(
    (comment) =>
      comment.author !== actor &&
      comment.mentionsVerdict &&
      (comment.text.includes(actor) || comment.text.includes(bare)),
  ).length;
  const unreadableReports = reports.filter(
    (comment) => comment.author === actor && comment.verdict === null,
  ).length;
  if (report !== undefined) {
    return {
      reported: true,
      source: "authored",
      unboundEvidence,
      unreadableReports,
      verdict: report.verdict,
    };
  }
  return {
    reported: false,
    source: null,
    unboundEvidence,
    unreadableReports,
    verdict: null,
  };
}

/**
 * Reads one Task's bounded status without the in-progress requirement the
 * delivery commands impose. Most Reviewers awaiting reconciliation belong to
 * Tasks that are already closed, so refusing those here would make the debt
 * unreachable by the supported path.
 */
function surveyedTaskStatus(run, options, taskId) {
  const value = runJson(run, "bd", [
    "--actor",
    options.actor,
    "show",
    taskId,
    "--json",
  ], { cwd: options.controlRepo });
  refuse(
    !Array.isArray(value) || value.length !== 1,
    "TASK_AMBIGUOUS",
    "Beads Task lookup was ambiguous",
  );
  const task = value[0];
  refuse(task.id !== taskId, "TASK_IDENTITY_MISMATCH", "Beads returned a different Task");
  refuse(
    typeof task.status !== "string" || task.status.length === 0,
    "TASK_STATE_INVALID",
    "Beads Task has no bounded status",
  );
  return task.status;
}

function validatedReviewerAgent(agent, options) {
  refuse(!isObject(agent), "PASEO_AGENT_RESPONSE_INVALID", "Paseo agent response is invalid");
  refuse(
    agent.Id !== options.reviewerAgentId,
    "PASEO_AGENT_MISMATCH",
    "Paseo returned a different agent",
  );
  refuse(
    typeof agent.Archived !== "boolean" ||
      !PASEO_AGENT_STATUSES.has(agent.Status) ||
      typeof agent.Cwd !== "string" ||
      !isObject(agent.Labels),
    "PASEO_AGENT_RESPONSE_INVALID",
    "Paseo agent response lacks bounded lifecycle facts",
  );
  refuse(
    agent.Archived
      ? typeof agent.ArchivedAt !== "string" ||
        agent.ArchivedAt.length === 0 ||
        agent.ArchivedAt.length > 64 ||
        !Number.isFinite(Date.parse(agent.ArchivedAt))
      : agent.ArchivedAt !== null && agent.ArchivedAt !== undefined,
    "PASEO_AGENT_RESPONSE_INVALID",
    "Paseo agent archive facts are invalid",
  );
  refuse(agent.ParentAgentId !== null, "REVIEWER_PARENTED", "Reviewer Agent is not top-level");
  // Identity comes from the agent's own frozen labels, never from its title and
  // never from the Candidate being reachable, because the Reviewers with the
  // oldest debt are bound to commits no published history contains.
  refuse(
    agent.Labels["director.role"] !== "reviewer" ||
      agent.Labels["director.task"] !== options.task ||
      agent.Labels["director.candidate"] !== options.candidate,
    "REVIEWER_IDENTITY_AMBIGUOUS",
    "Reviewer labels do not bind the exact Task and Candidate",
  );
  return agent;
}

/**
 * The owner marker of a disposable Reviewer checkout, stated as the exact facts
 * that make removal safe rather than as a single forgeable token. Every one is
 * read inside the checkout itself or from the control repository's own
 * registration table; none resolves the Candidate against published history.
 */
function reviewerCheckoutFacts(run, options) {
  if (options.reviewerCheckoutState === "reclaimed") {
    refuse(
      existsSync(options.reviewerCheckout),
      "RECLAIMED_CHECKOUT_PRESENT",
      "Reviewer checkout recorded as reclaimed is still present",
    );
    return { absent: true, source: "recorded_reclaimed" };
  }
  if (!existsSync(options.reviewerCheckout)) return { absent: true, source: "observed" };
  const status = lstatSync(options.reviewerCheckout);
  refuse(
    !status.isDirectory() || status.isSymbolicLink(),
    "REVIEWER_CHECKOUT_IDENTITY_INVALID",
    "Reviewer checkout must be a directory",
  );
  const registrations = worktreeRecords(
    gitChecked(run, options.controlRepo, ["worktree", "list", "--porcelain"]),
  ).filter((record) => {
    try {
      return realpathSync(record.worktree) === options.reviewerCheckout;
    } catch {
      return resolve(record.worktree) === options.reviewerCheckout;
    }
  });
  refuse(
    registrations.length > 0,
    "REVIEWER_CHECKOUT_NOT_DISPOSABLE",
    "Reviewer checkout is a registered worktree of the control repository",
  );
  let gitStatus;
  try {
    gitStatus = lstatSync(resolve(options.reviewerCheckout, ".git"));
  } catch {
    throw new CoordinatorError(
      "REVIEWER_CHECKOUT_UNMARKED",
      "Reviewer checkout is not a Git repository",
    );
  }
  refuse(
    !gitStatus.isDirectory() || gitStatus.isSymbolicLink(),
    "REVIEWER_CHECKOUT_NOT_DISPOSABLE",
    "Reviewer checkout does not own its Git directory",
  );
  const common = commonDirectory(run, options.reviewerCheckout);
  refuse(
    !pathIsWithin(options.reviewerCheckout, common),
    "REVIEWER_CHECKOUT_NOT_DISPOSABLE",
    "Reviewer checkout shares a Git directory with another repository",
  );
  refuse(
    gitRaw(run, options.reviewerCheckout, ["symbolic-ref", "--quiet", "HEAD"]).status === 0,
    "REVIEWER_CHECKOUT_ATTACHED",
    "Reviewer checkout is not detached",
  );
  const head = gitChecked(run, options.reviewerCheckout, ["rev-parse", "HEAD"]);
  refuse(
    head !== options.candidate,
    "REVIEWER_CHECKOUT_CANDIDATE_MISMATCH",
    "Reviewer checkout is not the bound Candidate",
  );
  const remoteUrl = gitChecked(run, options.reviewerCheckout, [
    "remote",
    "get-url",
    options.remote,
  ]);
  refuse(
    githubRepositoryIdentity(remoteUrl)?.toLowerCase() !== options.repo.toLowerCase(),
    "REVIEWER_CHECKOUT_REMOTE_MISMATCH",
    "Reviewer checkout remote does not match the explicit GitHub repository",
  );
  refuse(
    gitChecked(run, options.reviewerCheckout, [
      "for-each-ref",
      "--format=%(refname)",
      "refs/stash",
    ]) !== "",
    "REVIEWER_CHECKOUT_NOT_DISPOSABLE",
    "Reviewer checkout holds stashed work",
  );
  // Ignored material is admitted deliberately: a disposable review clone is
  // regenerable by construction, and the tracked/untracked state is what proves
  // the Reviewer changed nothing.
  refuse(
    gitChecked(run, options.reviewerCheckout, [
      "status",
      "--porcelain=v2",
      "--untracked-files=all",
    ]) !== "",
    "REVIEWER_CHECKOUT_DIRTY",
    "Reviewer checkout contains dirty or untracked material",
  );
  return {
    absent: false,
    candidate: head,
    commonDirectoryHash: digest(common),
    // One composite value rather than two fields: a device-only difference
    // cannot be produced at a fixed path in a test, so comparing the halves
    // separately would leave one of them permanently unfalsifiable.
    identity: `${status.dev}:${status.ino}`,
  };
}

/**
 * Classifies whether the bound Reviewer's Review is over, from two independent
 * facts that must agree with the operator's explicit binding. A Candidate that
 * is absent from `main` says nothing here: most of the debt is bound to
 * superseded Candidates whose Reviews completed normally.
 */
function reviewerReviewFacts(run, options, agent) {
  const reports = taskReviewReports(run, options, options.task);
  const report = reviewerReport(reports, options.reviewerAgentId);
  const abandonment = reviewAbandonment(reports, options.reviewerAgentId, options.actor);
  const taskStatus = surveyedTaskStatus(run, options, options.task);
  refuse(
    agent !== null && agent.archived !== true && agent.status === "running",
    "REVIEWER_REVIEW_IN_FLIGHT",
    "Reviewer Agent is still running",
  );
  if (options.reviewerReviewState === "verdict_recorded") {
    refuse(
      !report.reported,
      "REVIEWER_VERDICT_NOT_DURABLE",
      "no durable Review report binds this exact Reviewer or its Candidate",
    );
  } else {
    refuse(
      report.reported,
      "REVIEWER_REVIEW_STATE_MISMATCH",
      "a durable Review report exists and must be bound as verdict_recorded",
    );
    // While the Task is in progress an idle Reviewer that never reported is
    // indistinguishable from the reviewing agent between turns, so the Review
    // being over cannot be inferred — only declared. Without a declaration this
    // stays refused, which is the rule round one proved load-bearing; with one,
    // the coordinator has said it is no longer waiting, and the running guard
    // above still refuses an agent that is mid-turn.
    refuse(
      taskStatus === "in_progress" && abandonment === null,
      "REVIEWER_REVIEW_IN_FLIGHT",
      "an unfinished Review cannot be abandoned while its Task is in progress "
        + "unless this coordinator durably recorded abandoning it",
    );
    // `abandoned` asserts that no report binds this Reviewer. Where something
    // names it with a verdict this rule declined to read, that assertion is not
    // available: the surrendered coverage is a refusal, not a note an operator
    // is trusted to read. This never widens `verdict_recorded`, which would be
    // the withdrawn content anchor returning through a counter.
    refuse(
      report.unboundEvidence > 0 || report.unreadableReports > 0,
      "REVIEWER_REPORT_EVIDENCE_UNRESOLVED",
      "comments name this Reviewer with a verdict this rule did not bind",
      {
        unboundEvidence: report.unboundEvidence,
        unreadableReports: report.unreadableReports,
      },
    );
  }
  return {
    abandonedBy: abandonment?.by ?? null,
    reportSource: report.source,
    state: options.reviewerReviewState,
    taskStatus,
    unboundEvidence: report.unboundEvidence,
    unreadableReports: report.unreadableReports,
    verdict: report.verdict,
  };
}

function reviewerFacts(run, options, { allowAbsentWorkspace = false } = {}) {
  let workspace = null;
  // The Reviewer agent is inspected under every lifecycle binding, including
  // `reclaimed`. Skipping it there silently withdrew both the daemon running
  // guard and the frozen-label identity binding at exactly the binding an
  // operator reaches for when they believe the resource is already historical,
  // leaving `abandoned` resting on one Beads fact. An archived agent remains
  // inspectable, so `reclaimed` is an assertion this read can check rather than
  // one it has to take on trust.
  const inspected = validatedReviewerAgent(
    runJson(run, "paseo", ["inspect", options.reviewerAgentId, "--json"]),
    options,
  );
  refuse(
    options.reviewerLifecycleState === "reclaimed" && inspected.Archived !== true,
    "REVIEWER_LIFECYCLE_NOT_RECLAIMED",
    "Reviewer bound as reclaimed is still live on the daemon",
  );
  if (inspected.Archived !== true) {
    // A reclaimed checkout cannot be canonicalised, but the daemon still
    // reports the cwd the agent was created in, so the binding is compared as a
    // path either way. Without this the plan records, for a reaped checkout, an
    // absolute path never tied to the agent it claims to describe.
    refuse(
      options.reviewerCheckoutState === "present"
        ? canonicalExistingDirectory(inspected.Cwd, "Reviewer Agent cwd") !==
          options.reviewerCheckout
        : resolve(inspected.Cwd) !== options.reviewerCheckout,
      "REVIEWER_CHECKOUT_OWNERSHIP_MISMATCH",
      "Reviewer Agent cwd is not the bound disposable checkout",
    );
  }
  const agent = {
    archived: inspected.Archived === true,
    archivedAt: inspected.ArchivedAt ?? null,
    id: inspected.Id,
    status: inspected.Status,
  };
  {
    if (options.reviewerWorkspaceId !== "none") {
      const workspaces = validatedPaseoWorkspaces(
        runJson(run, "paseo", ["workspace", "ls", "--json"]),
      );
      const matches = workspaces.filter(
        (item) => item.workspaceId === options.reviewerWorkspaceId,
      );
      refuse(
        matches.length > 1,
        "PASEO_WORKSPACE_AMBIGUOUS",
        "Reviewer workspace identity is ambiguous",
      );
      if (matches.length === 1) {
        refuse(
          options.reviewerLifecycleState === "restored",
          "PASEO_WORKSPACE_RESTORATION_NOT_OBSERVED",
          "recorded Reviewer workspace is active and must be bound as active",
        );
        refuse(
          options.reviewerLifecycleState === "reclaimed",
          "REVIEWER_LIFECYCLE_NOT_RECLAIMED",
          "Reviewer workspace bound as reclaimed is still live on the daemon",
        );
        refuse(
          options.reviewerCheckoutState === "present" &&
            canonicalExistingDirectory(matches[0].cwd, "Reviewer workspace cwd") !==
              options.reviewerCheckout,
          "PASEO_WORKSPACE_OWNERSHIP_MISMATCH",
          "Reviewer workspace does not view the bound disposable checkout",
        );
        workspace = { id: matches[0].workspaceId, isolation: matches[0].isolation };
      } else if (options.reviewerLifecycleState === "restored") {
        workspace = {
          id: options.reviewerWorkspaceId,
          observation: "recorded_restored",
          verified: false,
        };
      } else if (options.reviewerLifecycleState === "reclaimed") {
        workspace = {
          id: options.reviewerWorkspaceId,
          observation: "recorded_reclaimed",
          verified: false,
        };
      } else {
        refuse(
          !allowAbsentWorkspace,
          "PASEO_WORKSPACE_ABSENT",
          "Reviewer workspace is absent before a recorded archive attempt",
        );
      }
    }
  }
  return {
    agent,
    checkout: reviewerCheckoutFacts(run, options),
    review: reviewerReviewFacts(run, options, agent),
    workspace,
  };
}

function validateCleanupPlan(options) {
  refuse(!options.planFile, "OPTION_REQUIRED", "--plan-file is required");
  const document = readJsonFile(options.planFile, "cleanup plan");
  refuse(
    containsProtectedMaterial(
      document,
      options.ownership,
      options.ownershipFile,
      options.paseoPassword,
      options.paseoCredentialFile,
    ),
    "CLEANUP_PLAN_OWNERSHIP_MATERIAL_FORBIDDEN",
    "cleanup plan contains raw ownership material",
  );
  const plan =
    document?.schemaVersion === OUTPUT_SCHEMA_VERSION &&
    document?.command === "cleanup-plan" &&
    document?.outcome === "complete"
      ? document.result
      : document;
  assertExactKeys(plan, ["schemaVersion", "command", "binding", "integration", "resources", "planHash"], "cleanup plan");
  refuse(
    plan.schemaVersion !== CLEANUP_PLAN_SCHEMA_VERSION ||
      plan.command !== "cleanup-plan",
    "CLEANUP_PLAN_SCHEMA_UNSUPPORTED",
    "cleanup plan schema is unsupported; regenerate it from current coordinator state",
  );
  assertExactKeys(
    plan.binding,
    DELIVERY_BINDING_KEYS,
    "cleanup plan binding",
    "CLEANUP_PLAN_INVALID",
  );
  const withoutHash = { ...plan };
  delete withoutHash.planHash;
  refuse(plan.planHash !== digest(withoutHash), "CLEANUP_PLAN_HASH_MISMATCH", "cleanup plan hash is invalid");
  refuse(
    canonicalJson(plan.binding) !== canonicalJson(deliveryBinding(options)),
    "CLEANUP_PLAN_BINDING_MISMATCH",
    "cleanup plan is bound to different immutable inputs",
  );
  return plan;
}

function ensureIntegratedState(options, state) {
  const record = state?.effects?.["integrate.merge"];
  refuse(
    record?.phase === "needs_manual_reconciliation",
    "INTEGRATION_MANUAL_RECONCILIATION_REQUIRED",
    "integration changed the reviewed base/tree and requires an owner decision before cleanup",
    record?.evidence,
  );
  refuse(record?.phase !== "complete", "INTEGRATION_STATE_MISSING", "state file lacks complete exact integration evidence");
  const evidence = record.evidence;
  refuse(
    evidence?.mergeCommit === undefined ||
      canonicalJson(evidence.parents) !== canonicalJson([options.base, options.candidate]),
    "INTEGRATION_STATE_INVALID",
    "integration evidence is not bound to the exact Candidate",
  );
  return evidence;
}

function persistIntegrationVerification(run, options, pull, state) {
  try {
    const evidence = verifyIntegration(run, options, pull);
    markEffect(
      options,
      state,
      "integrate.merge",
      "conditional_update",
      "complete",
      evidence,
    );
    return evidence;
  } catch (error) {
    if (
      error instanceof CoordinatorError &&
      [
        "INTEGRATION_PARENTS_MISMATCH",
        "INTEGRATION_TREE_MISMATCH",
        "INTEGRATION_NOT_REACHABLE",
      ].includes(error.code)
    ) {
      const evidence = {
        base: options.base,
        candidate: options.candidate,
        failureCode: error.code,
        mergeCommit: pull.merge_commit_sha ?? null,
        pullRequest: pull.number,
      };
      markEffect(
        options,
        state,
        "integrate.merge",
        "conditional_update",
        "needs_manual_reconciliation",
        evidence,
      );
      throw new CoordinatorError(
        "INTEGRATION_MANUAL_RECONCILIATION_REQUIRED",
        "merge completed with invalidated review identity; preserve all resources and obtain an owner decision",
        evidence,
      );
    }
    throw error;
  }
}

async function dispatchHook(deps, phase, effect, context) {
  if (deps.hook) await deps.hook(phase, effect, context);
}

/**
 * Publishes the owned Task ref at the exact Candidate under a lease on its
 * expected old head. `refresh` re-reads and re-asserts the caller's own
 * pre-push context immediately before the mutation, so publication and
 * draft publication share one push contract without sharing an evidence set.
 */
async function publishLeasedRef(run, options, deps, state, effect, refresh) {
  const pushRecord = effectRecord(state, effect, "conditional_update");
  let snapshot = refresh();
  if (snapshot.refs.head === options.candidate) {
    markEffect(options, state, effect, "conditional_update", "complete", {
      head: options.candidate,
    });
    return refresh();
  }
  refuse(
    snapshot.refs.head !== options.expectedRemoteHead,
    "REMOTE_HEAD_CHANGED",
    "remote Task branch changed before publication",
  );
  refuse(
    pushRecord.phase === "complete",
    "TERMINAL_DRIFT",
    "completed publication branch later changed",
  );
  markDispatch(options, state, effect, "conditional_update");
  await dispatchHook(deps, "before", effect, { options, state });
  snapshot = refresh();
  refuse(
    snapshot.refs.head !== options.expectedRemoteHead,
    "REMOTE_HEAD_CHANGED",
    "remote Task branch changed immediately before publication",
  );
  const ref = `refs/heads/${options.branch}`;
  const pushed = gitRaw(run, options.controlRepo, [
    "push",
    "--porcelain",
    `--force-with-lease=${ref}:${options.expectedRemoteHead ?? ""}`,
    options.remote,
    `${options.candidate}:${ref}`,
  ]);
  await dispatchHook(deps, "after", effect, { options, result: pushed, state });
  const observed = observeRemoteRef(run, options, options.branch, true);
  if (observed !== options.candidate) {
    markEffect(options, state, effect, "conditional_update", "unknown", {
      observed,
    });
    throw new CoordinatorError("PUSH_RESULT_UNKNOWN", "branch publication was not proven");
  }
  markEffect(options, state, effect, "conditional_update", "complete", {
    head: options.candidate,
  });
  return refresh();
}

function publicationBody(options) {
  validatePullRequestInputs(options);
  const bodyStatus = lstatSync(options.bodyFile);
  refuse(!bodyStatus.isFile() || bodyStatus.isSymbolicLink(), "BODY_FILE_INVALID", "PR body must be a regular non-symlink file");
  refuse(bodyStatus.size > 131_072, "BODY_FILE_OVERSIZE", "PR body is too large");
  const body = readFileSync(options.bodyFile, "utf8");
  refuse(body.includes("\0"), "BODY_FILE_INVALID", "PR body contains a NUL byte");
  refuse(
    containsProtectedMaterial(
      body,
      options.ownership,
      options.ownershipFile,
      options.paseoPassword,
      options.paseoCredentialFile,
    ),
    "PROTECTED_MATERIAL_REDACTED",
    "pull request body contained protected material",
  );
  return `${body.trimEnd()}\n\n${marker(options)}\n`;
}

/**
 * Leaves an adopted draft ready for integration. Only the post-review
 * publication path calls this, and only after an approved exact-SHA Review, so
 * the early draft published before review can never reach the merge gate on
 * its own: the gate refuses a draft outright.
 */
async function markReadyForReview(run, options, deps, state, pull, refresh) {
  const record = effectRecord(state, "publish.ready", "conditional_update");
  if (pull.draft !== true) {
    refuse(
      record.phase !== "dispatching" && record.phase !== "complete",
      "PULL_REQUEST_READINESS_UNOWNED",
      "non-draft pull request lacks an owned readiness effect",
    );
    markEffect(options, state, "publish.ready", "conditional_update", "complete", {
      number: pull.number,
    });
    return pull;
  }
  markDispatch(options, state, "publish.ready", "conditional_update");
  await dispatchHook(deps, "before", "publish.ready", { options, state });
  refresh();
  pull = exactPullRequests(run, options);
  refuse(
    !pull || pull.state !== "open" || pull.draft !== true ||
      pull.head?.sha !== options.candidate,
    "PULL_REQUEST_CHANGED",
    "owned draft changed immediately before publication readiness",
  );
  const result = run("gh", ["pr", "ready", String(pull.number), "--repo", options.repo], {
    cwd: options.controlRepo,
  });
  await dispatchHook(deps, "after", "publish.ready", { options, result, state });
  const observed = pullFacts(run, options, pull.number);
  refuse(
    observed.draft !== false || observed.state !== "open" ||
      observed.head?.sha !== options.candidate,
    "PULL_REQUEST_STILL_DRAFT",
    "owned pull request did not leave draft at the exact Candidate",
  );
  markEffect(options, state, "publish.ready", "conditional_update", "complete", {
    number: observed.number,
  });
  return observed;
}

/**
 * Publishes the exact Candidate as one owned draft pull request before review.
 * A draft carries no merge authority: the gate refuses a draft outright, so the
 * only way to integrate this PR is the post-review publication path, which
 * marks it ready after an approved exact-SHA Review and a passing CI. Running
 * it again with the same Candidate-bound state adopts the same draft.
 */
async function publishDraft(run, options, deps) {
  refuse(!options.validationFile, "OPTION_REQUIRED", "--validation-file is required");
  refuse(options.expectedRemoteHead === undefined, "OPTION_REQUIRED", "--expected-remote-head is required");
  refuse(!options.title, "OPTION_REQUIRED", "--title is required");
  refuse(!options.bodyFile, "OPTION_REQUIRED", "--body-file is required");
  refuse(
    options.reviewFile !== undefined,
    "DRAFT_PUBLICATION_CANNOT_CONSUME_REVIEW",
    "draft publication precedes review and refuses review evidence",
  );
  const completeBody = publicationBody(options);
  const manifest = validateReviewManifestEvidence(options);
  const validation = validateValidation(options);
  const state = loadState(options);
  persistState(options, state);
  const refresh = () => {
    const snapshot = baseSnapshot(run, options);
    refuse(
      snapshot.task.assignee !== manifest.taskAssignee,
      "TASK_ASSIGNEE_CHANGED",
      "Task assignee changed after the bound handoff",
    );
    return snapshot;
  };
  let snapshot = await publishLeasedRef(
    run, options, deps, state, "publish_draft.push", refresh,
  );
  requireRemoteCandidate(snapshot, options);
  let pull = exactPullRequests(run, options);
  if (pull) {
    refuse(pull.state !== "open", "PULL_REQUEST_NOT_OPEN", "owned pull request is not open");
    refuse(
      pull.draft !== true,
      "PULL_REQUEST_NOT_DRAFT",
      "owned pull request already left draft and cannot be republished as one",
    );
    refuse(pull.head?.sha !== options.candidate, "PULL_REQUEST_HEAD_CHANGED", "owned draft head changed");
  } else {
    markDispatch(options, state, "publish_draft.pr", "unique_create");
    await dispatchHook(deps, "before", "publish_draft.pr", { options, state });
    snapshot = refresh();
    requireRemoteCandidate(snapshot, options);
    pull = exactPullRequests(run, options);
    if (!pull) {
      const created = run("gh", [
        "pr",
        "create",
        "--draft",
        "--repo",
        options.repo,
        "--base",
        options.baseRef,
        "--head",
        `${options.headOwner}:${options.branch}`,
        "--title",
        options.title,
        "--body-file",
        "-",
      ], { cwd: options.controlRepo, input: completeBody });
      await dispatchHook(deps, "after", "publish_draft.pr", { options, result: created, state });
      pull = exactPullRequests(run, options);
    }
    if (!pull) {
      markEffect(options, state, "publish_draft.pr", "unique_create", "unknown");
      throw new CoordinatorError("PULL_REQUEST_RESULT_UNKNOWN", "draft pull request creation was not proven");
    }
    refuse(
      pull.state !== "open" || pull.draft !== true || pull.head?.sha !== options.candidate,
      "PULL_REQUEST_INVALID",
      "created pull request is not an open draft at the Candidate",
    );
  }
  markEffect(options, state, "publish_draft.pr", "unique_create", "complete", {
    draft: true,
    number: pull.number,
    url: pull.html_url,
  });
  bindPullRequest(options, state, pull.number);
  return {
    candidate: options.candidate,
    draft: true,
    manifest,
    mergeAuthorized: false,
    pendingGates: ["independent_review", "remote_ci", "publication", "feedback", "integration"],
    pullRequest: { number: pull.number, url: pull.html_url },
    remoteHead: options.candidate,
    validation,
  };
}

async function publish(run, options, deps) {
  refuse(!options.reviewFile, "OPTION_REQUIRED", "--review-file is required");
  refuse(!options.validationFile, "OPTION_REQUIRED", "--validation-file is required");
  refuse(options.expectedRemoteHead === undefined, "OPTION_REQUIRED", "--expected-remote-head is required");
  refuse(!options.title, "OPTION_REQUIRED", "--title is required");
  refuse(!options.bodyFile, "OPTION_REQUIRED", "--body-file is required");
  const routing = validateReviewRoutingEvidence(options);
  const review = validateReview(options, routing);
  const remoteCi = requireAuthoritativeRemoteCi(options, routing.harness);
  const validation = validateValidation(options);
  const state = loadState(options);
  persistState(options, state);
  const refresh = () => {
    const snapshot = baseSnapshot(run, options);
    requireCurrentReviewContext(run, options, routing, snapshot.task, validation);
    return snapshot;
  };
  const snapshot = await publishLeasedRef(
    run, options, deps, state, "publish.push", refresh,
  );
  requireRemoteCandidate(snapshot, options);
  let pull = exactPullRequests(run, options);
  refuse(
    !pull,
    "DRAFT_PULL_REQUEST_REQUIRED",
    "post-review publication requires the owned draft created before Review",
  );
  refuse(pull.state !== "open", "PULL_REQUEST_NOT_OPEN", "owned pull request is not open");
  refuse(pull.head?.sha !== options.candidate, "PULL_REQUEST_HEAD_CHANGED", "owned pull request head changed");
  pull = await markReadyForReview(run, options, deps, state, pull, refresh);
  markEffect(options, state, "publish.pr", "conditional_update", "complete", {
    number: pull.number,
    url: pull.html_url,
  });
  bindPullRequest(options, state, pull.number);
  return {
    candidate: options.candidate,
    draft: false,
    pullRequest: { number: pull.number, url: pull.html_url },
    remoteHead: options.candidate,
    remoteCi,
    review,
    routing,
    validation,
  };
}

async function integrate(run, options, deps) {
  const state = loadState(options);
  persistState(options, state);
  refuse(options.pr === "absent", "PULL_REQUEST_NUMBER_REQUIRED", "exact PR number is required");
  bindPullRequest(options, state, options.pr);
  let pull = prAfterPossibleMerge(run, options);
  if (pull.merged === true) {
    return persistIntegrationVerification(run, options, pull, state);
  }
  const gate = gateCore(run, options);
  const help = checkedRun(run, "gh", ["pr", "merge", "--help"]);
  refuse(
    !help.includes("--match-head-commit"),
    "ATOMIC_MERGE_UNSUPPORTED",
    "GitHub CLI lacks the required expected-head merge primitive",
  );
  markDispatch(options, state, "integrate.merge", "conditional_update");
  await dispatchHook(deps, "before", "integrate.merge", { gate, options, state });
  gateCore(run, options);
  const merged = run("gh", [
    "pr",
    "merge",
    String(options.pr),
    "--repo",
    options.repo,
    "--merge",
    "--match-head-commit",
    options.candidate,
  ], { cwd: options.controlRepo });
  await dispatchHook(deps, "after", "integrate.merge", { options, result: merged, state });
  pull = prAfterPossibleMerge(run, options);
  if (pull.merged !== true) {
    markEffect(options, state, "integrate.merge", "conditional_update", "unknown");
    throw new CoordinatorError("MERGE_RESULT_UNKNOWN", "atomic merge was not proven after dispatch");
  }
  return persistIntegrationVerification(run, options, pull, state);
}

/**
 * Dispatches one exact recorded archive through the bounded lifecycle child.
 * The daemon's own answer never proves the effect, so an unclassified failure
 * deliberately falls through to the authoritative readback that follows: the
 * mutation may have landed and its response been lost. A proven credential
 * refusal or an echoed secret is raised exactly, because continuing would
 * report the missing capability as an unproven archive and hide its cause.
 * Every outcome leaves the recorded intent and the resource intact for
 * idempotent reconciliation.
 */
function dispatchPaseoArchive(run, options, args) {
  const lifecycle = paseoLifecycleCall("paseo", args);
  const result = run("paseo", args, { cwd: options.controlRepo });
  if (result.protectedMaterialDetected === true) {
    throw paseoProtectedMaterialError(lifecycle, result);
  }
  if (result.error || result.status !== 0) {
    const refusal = paseoLifecycleRefusal(lifecycle, result);
    if (refusal !== null) throw refusal;
  }
  return result;
}

function reviewerDeliveryBinding(options) {
  return { ...stateBinding(options), pr: options.pr };
}

function validateReviewerPlan(options) {
  refuse(!options.planFile, "OPTION_REQUIRED", "--plan-file is required");
  const document = readJsonFile(options.planFile, "Reviewer cleanup plan");
  refuse(
    containsProtectedMaterial(
      document,
      options.ownership,
      options.ownershipFile,
      options.paseoPassword,
      options.paseoCredentialFile,
    ),
    "CLEANUP_PLAN_OWNERSHIP_MATERIAL_FORBIDDEN",
    "Reviewer cleanup plan contains raw ownership material",
  );
  const plan =
    document?.schemaVersion === OUTPUT_SCHEMA_VERSION &&
    document?.command === "reviewer-cleanup-plan" &&
    document?.outcome === "complete"
      ? document.result
      : document;
  assertExactKeys(
    plan,
    ["schemaVersion", "command", "binding", "resources", "planHash"],
    "Reviewer cleanup plan",
  );
  refuse(
    plan.schemaVersion !== REVIEWER_PLAN_SCHEMA_VERSION ||
      plan.command !== "reviewer-cleanup-plan",
    "CLEANUP_PLAN_SCHEMA_UNSUPPORTED",
    "Reviewer cleanup plan schema is unsupported; regenerate it from current coordinator state",
  );
  assertExactKeys(
    plan.binding,
    [...DELIVERY_BINDING_KEYS, ...REVIEWER_BINDING_KEYS],
    "Reviewer cleanup plan binding",
    "CLEANUP_PLAN_INVALID",
  );
  const withoutHash = { ...plan };
  delete withoutHash.planHash;
  refuse(
    plan.planHash !== digest(withoutHash),
    "CLEANUP_PLAN_HASH_MISMATCH",
    "Reviewer cleanup plan hash is invalid",
  );
  refuse(
    canonicalJson(plan.binding) !== canonicalJson(reviewerDeliveryBinding(options)),
    "CLEANUP_PLAN_BINDING_MISMATCH",
    "Reviewer cleanup plan is bound to different immutable inputs",
  );
  return plan;
}

/**
 * Removes the exact disposable checkout after re-proving, immediately before
 * the call, that the directory still carries the identity the plan bound. The
 * device and inode close the window between the proof and the removal.
 */
function removeReviewerCheckout(options, proven) {
  const status = lstatSync(options.reviewerCheckout);
  refuse(
    !status.isDirectory() ||
      status.isSymbolicLink() ||
      `${status.dev}:${status.ino}` !== proven.identity,
    "REVIEWER_CHECKOUT_IDENTITY_INVALID",
    "Reviewer checkout identity changed before removal",
  );
  rmSync(options.reviewerCheckout, { recursive: true, force: false });
}

async function reviewerCleanupApply(run, options, deps) {
  const plan = validateReviewerPlan(options);
  const state = loadState(options, { required: false });
  adoptResumedState(options, state);
  if (state.cleanupPlanHash !== undefined) {
    refuse(
      state.cleanupPlanHash !== plan.planHash,
      "CLEANUP_PLAN_REPLACED",
      "another Reviewer cleanup plan was already admitted",
    );
  } else {
    state.cleanupPlanHash = plan.planHash;
    persistState(options, state);
  }

  const workspaceDispatched = () =>
    state.effects["cleanup.reviewer-workspace"]?.phase === "dispatching" ||
    state.effects["cleanup.reviewer-workspace"]?.phase === "complete";

  if (options.reviewerLifecycleState === "reclaimed") {
    // The recorded binding is the only authority for a historical resource, so
    // the adopted effects claim nothing observed. Recording them keeps an
    // archive interrupted before its own transition from stranding a
    // nonterminal intent no admitted run could ever finish. The binding is
    // proven first: adoption must never outlive a refusal.
    reviewerFacts(run, options, { allowAbsentWorkspace: true });
    for (const effect of ["cleanup.reviewer-agent", "cleanup.reviewer-workspace"]) {
      if (state.effects[effect]?.phase === "complete") continue;
      if (
        effect === "cleanup.reviewer-workspace" &&
        options.reviewerWorkspaceId === "none"
      ) {
        continue;
      }
      markEffect(options, state, effect, "idempotent_close", "complete", {
        source: "recorded_reclaimed",
        verified: false,
      });
    }
  } else {
    let facts = reviewerFacts(run, options, { allowAbsentWorkspace: workspaceDispatched() });
    if (facts.agent?.archived) {
      markEffect(options, state, "cleanup.reviewer-agent", "idempotent_close", "complete", {
        archivedAt: facts.agent.archivedAt,
      });
    } else {
      markDispatch(options, state, "cleanup.reviewer-agent", "idempotent_close");
      await dispatchHook(deps, "before", "cleanup.reviewer-agent", { options, state });
      const archived = dispatchPaseoArchive(
        run,
        options,
        ["archive", options.reviewerAgentId, "--json"],
      );
      await dispatchHook(deps, "after", "cleanup.reviewer-agent", {
        options,
        result: archived,
        state,
      });
      const observed = validatedReviewerAgent(
        runJson(run, "paseo", ["inspect", options.reviewerAgentId, "--json"]),
        options,
      );
      refuse(
        observed.Archived !== true,
        "AGENT_ARCHIVE_UNKNOWN",
        "Reviewer Agent archive was not proven",
      );
      markEffect(options, state, "cleanup.reviewer-agent", "idempotent_close", "complete", {
        archivedAt: observed.ArchivedAt,
      });
    }

    if (options.reviewerWorkspaceId !== "none") {
      facts = reviewerFacts(run, options, { allowAbsentWorkspace: workspaceDispatched() });
      if (facts.workspace === null) {
        refuse(
          !workspaceDispatched(),
          "WORKSPACE_ARCHIVE_AMBIGUOUS",
          "Reviewer workspace absence is not tied to a recorded archive attempt",
        );
        markEffect(
          options,
          state,
          "cleanup.reviewer-workspace",
          "idempotent_close",
          "complete",
          { absent: true },
        );
      } else {
        markDispatch(options, state, "cleanup.reviewer-workspace", "idempotent_close");
        await dispatchHook(deps, "before", "cleanup.reviewer-workspace", { options, state });
        const archived = dispatchPaseoArchive(
          run,
          options,
          ["workspace", "archive", options.reviewerWorkspaceId, "--json"],
        );
        await dispatchHook(deps, "after", "cleanup.reviewer-workspace", {
          options,
          result: archived,
          state,
        });
        const workspaces = validatedPaseoWorkspaces(
          runJson(run, "paseo", ["workspace", "ls", "--json"]),
        );
        refuse(
          workspaces.some((item) => item.workspaceId === options.reviewerWorkspaceId),
          "WORKSPACE_ARCHIVE_UNKNOWN",
          "Reviewer workspace archive was not proven",
        );
        markEffect(
          options,
          state,
          "cleanup.reviewer-workspace",
          "idempotent_close",
          "complete",
          { absent: true },
        );
      }
    }
  }

  const phase = state.effects["cleanup.reviewer-checkout"]?.phase;
  let checkout = reviewerCheckoutFacts(run, options);
  if (checkout.absent) {
    refuse(
      options.reviewerCheckoutState !== "reclaimed" &&
        phase !== "dispatching" &&
        phase !== "complete",
      "DESTRUCTIVE_ABSENCE_AMBIGUOUS",
      "Reviewer checkout absence is not tied to a recorded removal attempt",
    );
    markEffect(
      options,
      state,
      "cleanup.reviewer-checkout",
      "destructive_terminal",
      "complete",
      { absent: true, source: checkout.source },
    );
  } else {
    refuse(
      phase === "dispatching" || phase === "unknown",
      "DESTRUCTIVE_TARGET_PRESENT_AFTER_HANDOFF",
      "Reviewer checkout remains present after a possible removal handoff",
    );
    markDispatch(options, state, "cleanup.reviewer-checkout", "destructive_terminal");
    // The owner marker is proven, and only then does anything else get a turn.
    // Whatever happens between this proof and the removal is exactly the window
    // the device and inode comparison inside removeReviewerCheckout closes, so
    // the proof has to be taken before that window opens rather than after it.
    checkout = reviewerCheckoutFacts(run, options);
    await dispatchHook(deps, "before", "cleanup.reviewer-checkout", { options, state });
    if (!checkout.absent) {
      removeReviewerCheckout(options, checkout);
      await dispatchHook(deps, "after", "cleanup.reviewer-checkout", { options, state });
      checkout = reviewerCheckoutFacts(run, options);
    }
    if (!checkout.absent) {
      markEffect(
        options,
        state,
        "cleanup.reviewer-checkout",
        "destructive_terminal",
        "unknown",
        { present: true },
      );
      throw new CoordinatorError(
        "DESTRUCTIVE_RESULT_UNKNOWN",
        "Reviewer checkout removal was not proven",
      );
    }
    markEffect(
      options,
      state,
      "cleanup.reviewer-checkout",
      "destructive_terminal",
      "complete",
      { absent: true },
    );
  }
  return { planHash: plan.planHash, resources: "complete" };
}

/**
 * Derives the Reviewers awaiting reconciliation from the daemon and the Beads
 * record at the moment it runs. Nothing here is a count: the set is whatever
 * the two sources currently describe, including Reviewers of closed Tasks, of
 * superseded Candidates, and of Reviews that never produced a verdict.
 */
function liveReviewerPage(run, options, predicate) {
  const listed = runJson(run, "paseo", ["agents", "ls", "--json"]);
  refuse(
    !isObject(listed) ||
      !Array.isArray(listed.Agents) ||
      !Number.isSafeInteger(listed.Limit) ||
      !Number.isSafeInteger(listed.WindowHours),
    "PASEO_AGENTS_INVALID",
    "Paseo agent list is invalid",
  );
  const reviewers = [];
  for (const agent of listed.Agents) {
    refuse(
      !isObject(agent) ||
        typeof agent.Id !== "string" ||
        typeof agent.Cwd !== "string" ||
        !PASEO_AGENT_STATUSES.has(agent.Status) ||
        typeof agent.Archived !== "boolean" ||
        !isObject(agent.Labels),
      "PASEO_AGENTS_INVALID",
      "Paseo agent list lacks bounded lifecycle facts",
    );
    if (agent.Archived === true) continue;
    if (agent.Labels["director.role"] !== "reviewer") continue;
    if (predicate !== undefined && !predicate(agent)) continue;
    reviewers.push(agent);
  }
  return {
    // The scope this enumeration actually establishes, stated in full. A page
    // returned at its own limit carries no evidence that the daemon had nothing
    // further to report; and no page at all, saturated or not, says anything
    // about an agent last active before the window. `complete` is the only
    // field that licenses "and there are no others", and it requires both.
    enumeration: {
      complete: listed.Agents.length < listed.Limit && listed.WindowHours === null,
      limit: listed.Limit,
      returned: listed.Agents.length,
      saturated: listed.Agents.length >= listed.Limit,
      windowHours: listed.WindowHours,
    },
    reviewers,
  };
}

function surveyReviewers(run, options) {
  const { enumeration, reviewers } = liveReviewerPage(run, options);
  const tasks = [...new Set(reviewers.map((agent) => agent.Labels["director.task"]))]
    .filter((task) => typeof task === "string" && TASK_PATTERN.test(task))
    .toSorted(compareText);
  refuse(
    tasks.length > MAXIMUM_SURVEYED_TASKS,
    "REVIEWER_SURVEY_OVERSIZE",
    "surveyed Reviewers span more Tasks than the bounded survey admits",
  );
  const byTask = new Map(
    tasks.map((task) => [
      task,
      {
        status: surveyedTaskStatus(run, options, task),
        reports: taskReviewReports(run, options, task),
      },
    ]),
  );
  const cwdCounts = new Map();
  for (const agent of reviewers) {
    cwdCounts.set(agent.Cwd, (cwdCounts.get(agent.Cwd) ?? 0) + 1);
  }
  const records = reviewers
    .map((agent) => {
      const task = agent.Labels["director.task"];
      const candidate = agent.Labels["director.candidate"];
      const record = byTask.get(task);
      const abandonment =
        record === undefined
          ? null
          : reviewAbandonment(record.reports, agent.Id, options.actor);
      const report =
        record === undefined
          ? {
              reported: false,
              source: null,
              // Unknowable rather than zero: without a readable Task there are
              // no comments to count, and reporting 0 would state that nothing
              // names this Reviewer when nothing was looked at.
              unboundEvidence: null,
              unreadableReports: null,
              verdict: null,
            }
          : reviewerReport(record.reports, agent.Id);
      let classification;
      if (
        record === undefined ||
        typeof candidate !== "string" ||
        !SHA_PATTERN.test(candidate) ||
        agent.ParentAgentId !== null ||
        cwdCounts.get(agent.Cwd) !== 1
      ) {
        classification = "ambiguous";
      } else if (agent.Status === "running") {
        classification = "review_in_flight";
      } else if (report.reported) {
        classification = "reconcilable";
      } else if (record.status === "in_progress" && abandonment === null) {
        // In flight because nothing says otherwise. A declared abandonment is
        // the something: the coordinator has stated it is no longer waiting, so
        // the Review is over even though its Task is not. A running agent is
        // still in flight regardless, which the branch above already decided.
        classification = "review_in_flight";
      } else {
        classification = "review_incomplete";
      }
      return {
        agentId: agent.Id,
        candidate: typeof candidate === "string" ? candidate : null,
        checkout: agent.Cwd,
        checkoutPresent: existsSync(agent.Cwd),
        classification,
        // What bound this Reviewer to its report, so a record says which
        // evidence it rests on rather than only its conclusion, and how many
        // comments name it with a verdict without binding it — the shape a
        // report this rule cannot bind leaves behind.
        abandonedBy: abandonment?.by ?? null,
        reportSource: report.source,
        unboundEvidence: report.unboundEvidence,
        unreadableReports: report.unreadableReports,
        // The `--reviewer-review-state` this record admits, and null where it
        // admits none. Derived from what the leg will actually accept rather
        // than from the classification alone: a row the gate refuses must not
        // display the binding it refuses, because the operator acts on the
        // display. `abandoned` therefore requires both counters clear and
        // either a Task no longer in progress or a recorded abandonment.
        reviewState:
          classification === "reconcilable"
            ? "verdict_recorded"
            : classification === "review_incomplete" &&
                report.unboundEvidence === 0 &&
                report.unreadableReports === 0 &&
                (record?.status !== "in_progress" || abandonment !== null)
              ? "abandoned"
              : null,
        status: agent.Status,
        task: typeof task === "string" ? task : null,
        taskStatus: record?.status ?? null,
        verdict: report.verdict,
      };
    })
    .toSorted((left, right) =>
      compareText(`${left.task} ${left.agentId}`, `${right.task} ${right.agentId}`),
    );
  return {
    enumeration,
    reviewers: records,
    totals: records.reduce(
      (counts, record) => ({
        ...counts,
        [record.classification]: (counts[record.classification] ?? 0) + 1,
      }),
      { reviewers: records.length },
    ),
  };
}

/**
 * The documented order: the exact remaining Reviewer of the Review that
 * authorized this delivery is terminated before the Task Agent leg begins. The
 * Reviewer is named by the durable Review evidence rather than by a fresh
 * option, so the ordering cannot be satisfied by naming a different agent.
 */
function requireReviewerLegComplete(run, options) {
  refuse(
    !options.reviewFile,
    "OPTION_REQUIRED",
    "--review-file is required so cleanup can order the Reviewer leg first",
  );
  const review = readJsonFile(options.reviewFile, "review evidence");
  refuse(
    review?.schemaVersion !== 1,
    "REVIEW_SCHEMA_UNSUPPORTED",
    "review schema version is unsupported",
  );
  refuse(
    review.task !== options.task ||
      review.candidate !== options.candidate ||
      review.base !== options.base,
    "REVIEW_BINDING_MISMATCH",
    "review does not bind the exact Task, Candidate, and base",
  );
  const agentId = requireString(
    review.reviewer?.agentId,
    ID_PATTERN,
    "reviewer agent ID",
  );
  const inspected = runJson(run, "paseo", ["inspect", agentId, "--json"]);
  refuse(!isObject(inspected), "PASEO_AGENT_RESPONSE_INVALID", "Paseo agent response is invalid");
  refuse(inspected.Id !== agentId, "PASEO_AGENT_MISMATCH", "Paseo returned a different agent");
  refuse(
    inspected.Archived !== true,
    "REVIEWER_LEG_PENDING",
    "the Review's exact Reviewer Agent is not archived before the Task Agent leg",
  );
  // Ordering the Review's own Reviewer is not the whole order. A corrected
  // Candidate creates a new Reviewer and leaves the previous one alive, which
  // is how a Task accumulates Reviewers nobody reconciles. Every live Reviewer
  // this Task labelled must be terminal before the Task Agent leg begins.
  const { enumeration, reviewers } = liveReviewerPage(
    run,
    options,
    (agent) =>
      agent.Labels["director.task"] === options.task && agent.Id !== agentId,
  );
  const remaining = reviewers.map((agent) => agent.Id).toSorted(compareText);
  refuse(
    remaining.length > 0,
    "REVIEWER_LEG_PENDING",
    "live Reviewers of this Task remain before the Task Agent leg",
    { remaining: remaining.slice(0, MAXIMUM_REPORTED_REVIEWERS) },
  );
  // A saturated page cannot support the claim that none remain, so the plan
  // records what the enumeration could actually establish and the closure
  // record inherits that scope instead of a summary adjective.
  return { agentId, archivedAt: inspected.ArchivedAt ?? null, enumeration };
}

async function cleanupApply(run, options, deps) {
  const plan = validateCleanupPlan(options);
  const state = loadState(options, {
    required: options.resumeStateFile === undefined,
  });
  adoptResumedState(options, state);
  ensureIntegratedState(options, state);
  requireReviewerLegComplete(run, options);
  if (state.cleanupPlanHash !== undefined) {
    refuse(state.cleanupPlanHash !== plan.planHash, "CLEANUP_PLAN_REPLACED", "another cleanup plan was already admitted");
  } else {
    state.cleanupPlanHash = plan.planHash;
    persistState(options, state);
  }
  const task = taskFacts(run, options);

  if (options.lifecycleState === "active") {
    let facts = cleanupFacts(run, options, task, { allowAbsentWorkspace: state.effects["cleanup.workspace"]?.phase === "dispatching" || state.effects["cleanup.workspace"]?.phase === "complete" });
    if (facts.agent?.archived) {
      markEffect(options, state, "cleanup.agent", "idempotent_close", "complete", { archivedAt: facts.agent.archivedAt });
    } else {
      markDispatch(options, state, "cleanup.agent", "idempotent_close");
      await dispatchHook(deps, "before", "cleanup.agent", { options, state });
      const archived = dispatchPaseoArchive(run, options, ["archive", options.agentId, "--json"]);
      await dispatchHook(deps, "after", "cleanup.agent", { options, result: archived, state });
      const observed = validatedPaseoAgent(
        runJson(run, "paseo", ["inspect", options.agentId, "--json"]),
        options,
        task,
      );
      refuse(observed.Archived !== true, "AGENT_ARCHIVE_UNKNOWN", "Task Agent archive was not proven");
      markEffect(options, state, "cleanup.agent", "idempotent_close", "complete", { archivedAt: observed.ArchivedAt });
    }

    facts = cleanupFacts(run, options, task, { allowAbsentWorkspace: state.effects["cleanup.workspace"]?.phase === "dispatching" || state.effects["cleanup.workspace"]?.phase === "complete" });
    if (facts.workspace === null) {
      refuse(
        state.effects["cleanup.workspace"]?.phase !== "dispatching" && state.effects["cleanup.workspace"]?.phase !== "complete",
        "WORKSPACE_ARCHIVE_AMBIGUOUS",
        "workspace absence is not tied to a recorded archive attempt",
      );
      markEffect(options, state, "cleanup.workspace", "idempotent_close", "complete", { absent: true });
    } else {
      markDispatch(options, state, "cleanup.workspace", "idempotent_close");
      await dispatchHook(deps, "before", "cleanup.workspace", { options, state });
      const archived = dispatchPaseoArchive(
        run,
        options,
        ["workspace", "archive", options.workspaceId, "--json"],
      );
      await dispatchHook(deps, "after", "cleanup.workspace", { options, result: archived, state });
      const workspaces = validatedPaseoWorkspaces(
        runJson(run, "paseo", ["workspace", "ls", "--json"]),
      );
      refuse(
        workspaces.some((workspace) => workspace.workspaceId === options.workspaceId),
        "WORKSPACE_ARCHIVE_UNKNOWN",
        "Paseo workspace archive was not proven",
      );
      markEffect(options, state, "cleanup.workspace", "idempotent_close", "complete", { absent: true });
    }
  } else if (options.lifecycleState === "reclaimed") {
    // A reclaimed lifecycle binding is the recorded statement that both
    // resources are already historical, so neither is dispatched again. Record
    // that adoption instead of skipping silently, exactly as a reclaimed
    // checkout already completes the worktree effect: an archive interrupted
    // before its own progress transitioned this binding would otherwise keep a
    // nonterminal intent no admitted run can ever finish, and the closure
    // record would understate what cleanup actually covered.
    for (const effect of ["cleanup.agent", "cleanup.workspace"]) {
      if (state.effects[effect]?.phase === "complete") continue;
      // The recorded binding is the only authority here, exactly as
      // cleanupFacts reports it, so the evidence claims nothing observed.
      markEffect(options, state, effect, "idempotent_close", "complete", {
        source: "recorded_reclaimed",
        verified: false,
      });
    }
  }

  {
    const phase = state.effects["cleanup.worktree"]?.phase;
    let facts = cleanupFacts(run, options, task, {
      allowAbsentWorkspace:
        options.lifecycleState !== "active" ||
        state.effects["cleanup.workspace"]?.phase === "dispatching" ||
        state.effects["cleanup.workspace"]?.phase === "complete",
    });
    if (facts.worktree === null) {
      refuse(
        options.checkoutState !== "reclaimed" &&
          state.effects["cleanup.workspace"]?.phase !== "dispatching" &&
          state.effects["cleanup.workspace"]?.phase !== "complete" &&
          phase !== "dispatching" &&
          phase !== "complete",
        "WORKTREE_ABSENCE_AMBIGUOUS",
        "worktree absence is not tied to a recorded removal attempt",
      );
      markEffect(
        options,
        state,
        "cleanup.worktree",
        "destructive_terminal",
        "complete",
        {
          absent: true,
          source:
            options.checkoutState === "reclaimed"
              ? "recorded_reclaimed"
              : "workspace_archive_or_prior_attempt",
        },
      );
    } else {
      refuse(
        phase === "dispatching" || phase === "unknown",
        "DESTRUCTIVE_TARGET_PRESENT_AFTER_HANDOFF",
        "worktree remains present after a possible removal handoff",
      );
      markDispatch(options, state, "cleanup.worktree", "destructive_terminal");
      await dispatchHook(deps, "before", "cleanup.worktree", { options, state });
      facts = cleanupFacts(run, options, task, { allowAbsentWorkspace: true });
      if (facts.worktree !== null) {
        const removed = gitRaw(run, options.controlRepo, ["worktree", "remove", "--", options.checkout]);
        await dispatchHook(deps, "after", "cleanup.worktree", { options, result: removed, state });
        facts = cleanupFacts(run, options, task, { allowAbsentWorkspace: true });
      }
      refuse(facts.worktree !== null, "WORKTREE_REMOVAL_UNKNOWN", "worktree removal was not proven");
      markEffect(options, state, "cleanup.worktree", "destructive_terminal", "complete", { absent: true });
    }
  }

  const postLifecycle = cleanupFacts(run, options, task, { allowAbsentWorkspace: true });
  refuse(
    postLifecycle.worktree !== null,
    "LIFECYCLE_CLEANUP_INCOMPLETE",
    "Task worktree remains registered after lifecycle cleanup",
  );

  async function destructiveRef(effect, kind) {
    const record = state.effects[effect];
    const facts = cleanupFacts(run, options, task, { allowAbsentWorkspace: true });
    const current = kind === "remote" ? facts.remoteBranch : facts.localBranch;
    if (current === null) {
      refuse(
        record === undefined,
        "DESTRUCTIVE_ABSENCE_AMBIGUOUS",
        `${kind} ref is absent without a recorded deletion intent`,
      );
      markEffect(options, state, effect, "destructive_terminal", "complete", { absent: true });
      return;
    }
    // Ref deletion is the narrow destructive-terminal recovery case with an
    // atomic exact-value guard. cleanupFacts authoritatively proved this live
    // ref is still the Candidate; the dispatcher below observes once more and
    // compare-deletes only that value. Worktree cleanup has no such retry.
    refuse(
      record?.phase === "complete",
      "DESTRUCTIVE_TARGET_PRESENT_AFTER_HANDOFF",
      `${kind} ref is present after a completed deletion`,
    );
    refuse(
      record !== undefined &&
        !["intent_recorded", "dispatching", "unknown"].includes(record.phase),
      "STATE_INVALID",
      `${kind} ref deletion effect has an invalid phase`,
    );
    markDispatch(options, state, effect, "destructive_terminal");
    await dispatchHook(deps, "before", effect, { options, state });
    const immediate = cleanupFacts(run, options, task, { allowAbsentWorkspace: true });
    const immediateCurrent =
      kind === "remote" ? immediate.remoteBranch : immediate.localBranch;
    if (immediateCurrent === null) {
      markEffect(options, state, effect, "destructive_terminal", "complete", { absent: true });
      return;
    }
    const ref = `refs/heads/${options.branch}`;
    const result =
      kind === "remote"
        ? gitRaw(run, options.controlRepo, [
            "push",
            "--porcelain",
            `--force-with-lease=${ref}:${options.candidate}`,
            options.remote,
            `:${ref}`,
          ])
        : gitRaw(run, options.controlRepo, [
            "update-ref",
            "-d",
            ref,
            options.candidate,
          ]);
    await dispatchHook(deps, "after", effect, { options, result, state });
    const observed = cleanupFacts(run, options, task, { allowAbsentWorkspace: true });
    const after = kind === "remote" ? observed.remoteBranch : observed.localBranch;
    if (after !== null) {
      markEffect(options, state, effect, "destructive_terminal", "unknown", { present: true });
      throw new CoordinatorError("DESTRUCTIVE_RESULT_UNKNOWN", `${kind} ref deletion was not proven`);
    }
    markEffect(options, state, effect, "destructive_terminal", "complete", { absent: true });
  }

  await destructiveRef("cleanup.remote-branch", "remote");
  await destructiveRef("cleanup.local-branch", "local");
  return { planHash: plan.planHash, resources: "complete" };
}

export async function execute(command, rawOptions, dependencies = {}) {
  const options = validateOptions(command, rawOptions);
  const run = dependencies.run ?? ((executable, args, runOptions = {}) =>
    defaultCommandRunner(executable, args, {
      ...runOptions,
      paseoPassword: options.paseoPassword,
    }));
  const deps = { ...dependencies, run };
  const operation = async () => {
    let result;
    if (command === "snapshot") {
      result = baseSnapshot(run, options);
    } else if (command === "review-handoff") {
      refuse(!options.validationFile, "OPTION_REQUIRED", "--validation-file is required");
      const validation = validateValidation(options);
      const snapshot = baseSnapshot(run, options);
      refuse(
        snapshot.task.assignee !== options.actor,
        "TASK_AGENT_ACTOR_MISMATCH",
        "review handoff actor is not the assigned Task Agent",
      );
      const manifest = reviewContextFacts(
        run,
        options,
        {
          ...snapshot.task,
          acceptanceCriteria: snapshot.task.acceptanceCriteria,
        },
        validation,
      );
      result = {
        base: options.base,
        candidate: options.candidate,
        criteriaSource: options.task,
        manifest,
        reviewerCheckout: { detached: true, commit: options.candidate },
        snapshot,
      };
    } else if (command === "publish-draft") {
      result = await publishDraft(run, options, deps);
    } else if (command === "remote-ci") {
      result = remoteCi(run, options);
    } else if (command === "publish") {
      result = await publish(run, options, deps);
    } else if (command === "gate") {
      result = gateCore(run, options);
    } else if (command === "integrate") {
      result = await integrate(run, options, deps);
    } else if (command === "cleanup-plan") {
      const state = loadState(options, {
        required: options.resumeStateFile === undefined,
      });
      adoptResumedState(options, state);
      const integration = ensureIntegratedState(options, state);
      const reviewer = requireReviewerLegComplete(run, options);
      const task = taskFacts(run, options);
      const resources = { ...cleanupFacts(run, options, task), reviewer };
      const plan = {
        schemaVersion: CLEANUP_PLAN_SCHEMA_VERSION,
        command: "cleanup-plan",
        binding: deliveryBinding(options),
        integration,
        resources,
      };
      result = { ...plan, planHash: digest(plan) };
    } else if (command === "cleanup-apply") {
      result = await cleanupApply(run, options, deps);
    } else if (command === "reviewer-survey") {
      result = surveyReviewers(run, options);
    } else if (command === "reviewer-cleanup-plan") {
      const state = loadState(options, { required: false });
      adoptResumedState(options, state);
      const plan = {
        schemaVersion: REVIEWER_PLAN_SCHEMA_VERSION,
        command: "reviewer-cleanup-plan",
        binding: reviewerDeliveryBinding(options),
        resources: reviewerFacts(run, options),
      };
      result = { ...plan, planHash: digest(plan) };
    } else if (command === "reviewer-cleanup-apply") {
      result = await reviewerCleanupApply(run, options, deps);
    }
    const output = {
      schemaVersion: OUTPUT_SCHEMA_VERSION,
      command,
      outcome:
        command === "gate" || command === "review-handoff" || command === "remote-ci"
          ? "ready"
          : "complete",
      result,
    };
    refuse(
      containsProtectedMaterial(
        output,
        options.ownership,
        options.ownershipFile,
        options.paseoPassword,
        options.paseoCredentialFile,
      ),
      "PROTECTED_MATERIAL_REDACTED",
      "coordinator output contained protected material",
    );
    if (command === "review-handoff" && options.handoffFile !== undefined) {
      persistPrivateJson(options.handoffFile, output, {
        identityCode: "HANDOFF_IDENTITY_INVALID",
        temporaryCode: "HANDOFF_TEMP_EXISTS",
      });
    }
    return output;
  };
  const protectedOperation = async () => {
    try {
      return await operation();
    } catch (error) {
      if (
        containsProtectedError(
          error,
          options.ownership,
          options.ownershipFile,
          options.paseoPassword,
          options.paseoCredentialFile,
        )
      ) {
        throw new CoordinatorError(
          "PROTECTED_MATERIAL_REDACTED",
          "coordinator failure contained protected material",
        );
      }
      throw error;
    }
  };
  return STATE_LOCK_COMMANDS.has(command)
    ? withStateLock(options, protectedOperation)
    : protectedOperation();
}

export function errorOutput(
  command,
  error,
  ownership = undefined,
  ownershipFile = undefined,
  paseoPassword = undefined,
  paseoCredentialFile = undefined,
) {
  if (error instanceof CoordinatorInterruption) {
    const output = {
      schemaVersion: OUTPUT_SCHEMA_VERSION,
      command,
      outcome: "interrupted",
      code: "EXPECTED_INTERRUPTION",
      message: error.message,
      effect: error.effect,
    };
    if (
      containsProtectedMaterial(
        output,
        ownership,
        ownershipFile,
        paseoPassword,
        paseoCredentialFile,
      )
    ) {
      return {
        schemaVersion: OUTPUT_SCHEMA_VERSION,
        command,
        outcome: "refused",
        code: "PROTECTED_MATERIAL_REDACTED",
        message: "coordinator interruption contained protected material",
      };
    }
    return output;
  }
  if (error instanceof CoordinatorError) {
    const output = {
      schemaVersion: OUTPUT_SCHEMA_VERSION,
      command,
      outcome: "refused",
      code: error.code,
      message: boundedText(error.message),
      ...(error.details === undefined ? {} : { details: canonicalize(error.details) }),
    };
    if (
      containsProtectedMaterial(
        output,
        ownership,
        ownershipFile,
        paseoPassword,
        paseoCredentialFile,
      )
    ) {
      return {
        schemaVersion: OUTPUT_SCHEMA_VERSION,
        command,
        outcome: "refused",
        code: "PROTECTED_MATERIAL_REDACTED",
        message: "coordinator refusal contained protected material",
      };
    }
    return output;
  }
  return {
    schemaVersion: OUTPUT_SCHEMA_VERSION,
    command,
    outcome: "error",
    code: "INTERNAL_ERROR",
    message: "coordinator failed without an authoritative result",
  };
}

export const coordinatorContract = Object.freeze({
  commands: [...COMMANDS],
  commonOptions: COMMON_OPTIONS,
  mutatingCommands: [...MUTATING_COMMANDS],
  outputSchemaVersion: OUTPUT_SCHEMA_VERSION,
  stateSchemaVersion: STATE_SCHEMA_VERSION,
});
