// SPDX-License-Identifier: Apache-2.0

import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  existsSync,
  lstatSync,
  readFileSync,
  realpathSync,
} from "node:fs";
import { isAbsolute, resolve } from "node:path";

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
const STATE_SCHEMA_VERSION = 1;
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

// A handoff manifest stays usable while its lifecycle binding only ever loses
// live facts: active may become restored, and either may become historical.
const LIVE_LIFECYCLE_STATES = new Set(["active", "restored"]);

const MUTATING_COMMANDS = new Set(["publish-draft", "remote-ci", "publish", "integrate", "cleanup-apply"]);
const COMMANDS = new Set([
  "snapshot",
  "review-handoff",
  "publish-draft",
  "remote-ci",
  "publish",
  "gate",
  "integrate",
  "cleanup-plan",
  "cleanup-apply",
]);

export class CoordinatorInterruption extends Error {
  constructor(effect) {
    super(`interrupted after ${effect} dispatch`);
    this.name = "CoordinatorInterruption";
    this.effect = effect;
  }
}

function isPaseoLifecycleRead(executable, args) {
  return executable === "paseo" &&
    ((args.length === 3 &&
      args[0] === "inspect" &&
      ID_PATTERN.test(args[1]) &&
      args[2] === "--json") ||
      (args.length === 3 &&
        args[0] === "workspace" &&
        args[1] === "ls" &&
        args[2] === "--json"));
}

function paseoLifecycleOperation(args) {
  return args[0] === "inspect" ? "agent.inspect" : "workspace.list";
}

/**
 * Splits a Paseo host into the credential-free address every child process may
 * see and the credential only the two public lifecycle reads may receive. The
 * documented local secret profile exports the password inside the connection
 * URI as well as in its own variable, and refusing that form forced every Task
 * to wrap this CLI in a per-Task shell script. Normalizing it here removes the
 * script without weakening the invariant: the raw host never reaches a child.
 */
/**
 * A credential-free Paseo address carries no userinfo and no password
 * parameter. Anything still matching this shape after normalization is refused
 * rather than forwarded, so an unparsed or unexpected credential location can
 * never reach a child process.
 */
function credentialShapedHost(value) {
  return /password/iu.test(value) || /^[^/?#]*@/u.test(value.replace(/^[a-z][a-z0-9+.-]*:\/\//iu, ""));
}

/**
 * Best-effort split of a Paseo host into its address and any credential it
 * carries. It never refuses, because the redaction guard runs on every output
 * path and must be total.
 */
function splitPaseoHost(host) {
  if (host === undefined) return { host: undefined, query: null, embedded: null };
  try {
    const parsed = new URL(host);
    const query = parsed.searchParams.get("password");
    const embedded = parsed.password.length > 0 ? parsed.password : null;
    parsed.searchParams.delete("password");
    parsed.password = "";
    parsed.username = "";
    return { host: parsed.toString(), query, embedded };
  } catch {
    return { host, query: null, embedded: null };
  }
}

/**
 * The address a child process may see. Unlike splitPaseoHost this refuses a
 * host whose credential this CLI cannot separate, so an unexpected credential
 * location fails closed instead of being forwarded.
 */
function normalizedPaseoHost(host) {
  const split = splitPaseoHost(host);
  if (split.host === undefined) return { host: undefined, password: null };
  refuse(
    split.query !== null && split.embedded !== null && split.query !== split.embedded,
    "PASEO_AUTH_LOCATION_UNSUPPORTED",
    "Paseo host declares two different credentials",
  );
  refuse(
    credentialShapedHost(split.host),
    "PASEO_AUTH_LOCATION_UNSUPPORTED",
    "Paseo host carries a credential this CLI cannot separate",
  );
  return { host: split.host, password: split.query ?? split.embedded };
}

function selectedPaseoPassword() {
  const password = process.env.PASEO_PASSWORD;
  if (password !== undefined && password.length > 0) return password;
  const split = splitPaseoHost(process.env.PASEO_HOST);
  const embedded = split.query ?? split.embedded;
  return embedded !== null && embedded.length > 0 ? embedded : null;
}

function containsSelectedPaseoPassword(value) {
  const password = selectedPaseoPassword();
  if (password === null) return false;
  const visit = (item) => {
    if (typeof item === "string") return item.includes(password);
    if (Array.isArray(item)) return item.some(visit);
    if (isObject(item)) return Object.values(item).some(visit);
    return false;
  };
  return visit(value);
}

function selectedEnvironment(executable, args) {
  const selected = {};
  const password = selectedPaseoPassword();
  const host = normalizedPaseoHost(process.env.PASEO_HOST);
  for (const key of [
    "PATH",
    "LANG",
    "LC_ALL",
    "TMPDIR",
    "HOME",
    "XDG_CONFIG_HOME",
    "GH_CONFIG_DIR",
    "GH_HOST",
  ]) {
    const value = process.env[key];
    if (
      value !== undefined &&
      (password === null || !value.includes(password))
    ) {
      selected[key] = value;
    }
  }
  if (host.host !== undefined) {
    refuse(
      password !== null && host.host.includes(password),
      "PASEO_AUTH_LOCATION_UNSUPPORTED",
      "Paseo host still carries a credential after normalization",
    );
    selected.PASEO_HOST = host.host;
  }
  if (isPaseoLifecycleRead(executable, args) && password !== null) {
    selected.PASEO_PASSWORD = password;
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
  const result = spawnSync(executable, args, {
    cwd: options.cwd,
    encoding: "utf8",
    env: selectedEnvironment(executable, args),
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
  };
}

function checkedRun(run, executable, args, options = {}) {
  const result = run(executable, args, options);
  if (result.error || result.status !== 0) {
    if (isPaseoLifecycleRead(executable, args)) {
      const response = `${result.stdout ?? ""}\n${result.stderr ?? ""}`;
      const operation = paseoLifecycleOperation(args);
      if (/Password required/iu.test(response)) {
        throw new CoordinatorError(
          "PASEO_AUTH_REQUIRED",
          "Paseo lifecycle authentication is required",
          { operation, status: result.status },
        );
      }
      if (/Incorrect password/iu.test(response)) {
        throw new CoordinatorError(
          "PASEO_AUTH_FAILED",
          "Paseo lifecycle authentication was rejected",
          { operation, status: result.status },
        );
      }
      throw new CoordinatorError(
        "PASEO_LIFECYCLE_READ_FAILED",
        "Paseo lifecycle facts were unavailable",
        { operation, status: result.status },
      );
    }
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
  const lifecycleRead = isPaseoLifecycleRead(executable, args);
  refuse(
    lifecycleRead && containsSelectedPaseoPassword(output),
    "PASEO_LIFECYCLE_RESPONSE_REDACTED",
    "Paseo lifecycle response contained protected material",
  );
  const value = parsedJson(output, executable);
  refuse(
    lifecycleRead && containsSelectedPaseoPassword(value),
    "PASEO_LIFECYCLE_RESPONSE_REDACTED",
    "Paseo lifecycle response contained protected material",
  );
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
      `unexpected argument ${boundedText(token, 120)}`,
    );
    const key = token.slice(2);
    refuse(key.length === 0, "ARGUMENT_INVALID", "empty option name");
    const value = argv[index + 1];
    refuse(
      value === undefined || value.startsWith("--"),
      "ARGUMENT_MISSING_VALUE",
      `--${key} requires a value`,
    );
    index += 1;
    if (key === "required-check") {
      options[key] ??= [];
      options[key].push(value);
    } else {
      refuse(
        options[key] !== undefined,
        "ARGUMENT_DUPLICATE",
        `--${key} may appear only once`,
      );
      options[key] = value;
    }
  }
  return options;
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
  ]);
  const unknown = Object.keys(rawOptions).filter((key) => !allowed.has(key));
  refuse(
    unknown.length > 0,
    "OPTION_UNKNOWN",
    "unknown command options",
    { options: unknown.toSorted() },
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

  if (
    MUTATING_COMMANDS.has(command) || rawOptions["state-file"] !== undefined
  ) {
    refuse(
      rawOptions["state-file"] === undefined,
      "OPTION_REQUIRED",
      "--state-file is required for mutating commands",
    );
    options.stateFile = canonicalPath(rawOptions["state-file"], "state file", {
      mustExist: false,
    });
    refuse(
      pathIsWithin(checkout, options.stateFile) ||
        pathIsWithin(controlRepo, options.stateFile),
      "STATE_INSIDE_REPOSITORY",
      "state file must be outside the Task and control Git checkouts",
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
  return options;
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
    ownership: options.ownership,
    remote: options.remote,
    repo: options.repo,
    repoId: options.repoId,
    task: options.task,
    agentId: options.agentId,
    lifecycleState: options.lifecycleState,
    workspaceId: options.workspaceId,
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
  const state = readJsonFile(options.stateFile, "coordinator state");
  const stateStatus = lstatSync(options.stateFile);
  refuse(
    (stateStatus.mode & 0o077) !== 0 ||
      (typeof process.getuid === "function" && stateStatus.uid !== process.getuid()),
    "STATE_PERMISSIONS_INVALID",
    "state file must be owned by the current user with mode 0600",
  );
  assertExactKeys(
    state,
    ["schemaVersion", "binding", "effects", "cleanupPlanHash", "pullRequestNumber"],
    "state",
  );
  refuse(
    state.schemaVersion !== STATE_SCHEMA_VERSION,
    "STATE_SCHEMA_UNSUPPORTED",
    "state schema version is unsupported",
  );
  refuse(
    canonicalJson(state.binding) !== canonicalJson(stateBinding(options)),
    "STATE_BINDING_MISMATCH",
    "state file is bound to different immutable inputs",
  );
  refuse(!isObject(state.effects), "STATE_INVALID", "state effects are invalid");
  return state;
}

function persistState(path, state) {
  refuse(
    containsSelectedPaseoPassword(state),
    "PROTECTED_MATERIAL_REDACTED",
    "coordinator state contained protected material",
  );
  persistPrivateJson(path, state, {
    identityCode: "STATE_IDENTITY_INVALID",
    temporaryCode: "STATE_TEMP_EXISTS",
  });
}

async function withStateLock(options, operation) {
  return withProcessIdentityLock(
    {
      lockPath: `${options.stateFile}.lock`,
      bindingHash: digest(stateBinding(options)),
      label: "coordinator state lock",
      busyCode: "COORDINATOR_BUSY",
      invalidCode: "STATE_LOCK_INVALID",
      replacedCode: "STATE_LOCK_REPLACED",
      processCode: "PROCESS_IDENTITY_UNAVAILABLE",
    },
    operation,
  );
}

function bindPullRequest(stateFile, state, number) {
  refuse(
    state.pullRequestNumber !== undefined && state.pullRequestNumber !== number,
    "PULL_REQUEST_NUMBER_MISMATCH",
    "state is already bound to another pull request",
  );
  if (state.pullRequestNumber === undefined) {
    state.pullRequestNumber = number;
    persistState(stateFile, state);
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

function markDispatch(stateFile, state, effect, effectClass) {
  const record = effectRecord(state, effect, effectClass);
  record.phase = "dispatching";
  record.attempts += 1;
  persistState(stateFile, state);
  return record;
}

function markEffect(stateFile, state, effect, effectClass, phase, evidence = undefined) {
  const record = effectRecord(state, effect, effectClass);
  record.phase = phase;
  if (evidence !== undefined) record.evidence = evidence;
  persistState(stateFile, state);
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
  const pulls = runJson(run, "gh", [
    "api",
    `repos/${options.repo}/pulls?state=all&head=${encodeURIComponent(`${options.headOwner}:${options.branch}`)}&base=${encodeURIComponent(options.baseRef)}&per_page=100`,
  ]);
  refuse(!Array.isArray(pulls), "PULL_REQUEST_LIST_INVALID", "GitHub pull request list is invalid");
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
  persistState(options.stateFile, state);
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
  markEffect(options.stateFile, state, "remote_ci.observe", "store_only", "complete", {
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

function validateCleanupPlan(options) {
  refuse(!options.planFile, "OPTION_REQUIRED", "--plan-file is required");
  const document = readJsonFile(options.planFile, "cleanup plan");
  const plan =
    document?.schemaVersion === OUTPUT_SCHEMA_VERSION &&
    document?.command === "cleanup-plan" &&
    document?.outcome === "complete"
      ? document.result
      : document;
  assertExactKeys(plan, ["schemaVersion", "command", "binding", "integration", "resources", "planHash"], "cleanup plan");
  refuse(plan.schemaVersion !== 1 || plan.command !== "cleanup-plan", "CLEANUP_PLAN_INVALID", "cleanup plan schema is invalid");
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
      options.stateFile,
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
        options.stateFile,
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
    markEffect(options.stateFile, state, effect, "conditional_update", "complete", {
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
  markDispatch(options.stateFile, state, effect, "conditional_update");
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
    markEffect(options.stateFile, state, effect, "conditional_update", "unknown", {
      observed,
    });
    throw new CoordinatorError("PUSH_RESULT_UNKNOWN", "branch publication was not proven");
  }
  markEffect(options.stateFile, state, effect, "conditional_update", "complete", {
    head: options.candidate,
  });
  return refresh();
}

function publicationBody(options) {
  const bodyStatus = lstatSync(options.bodyFile);
  refuse(!bodyStatus.isFile() || bodyStatus.isSymbolicLink(), "BODY_FILE_INVALID", "PR body must be a regular non-symlink file");
  refuse(bodyStatus.size > 131_072, "BODY_FILE_OVERSIZE", "PR body is too large");
  const body = readFileSync(options.bodyFile, "utf8");
  refuse(body.includes("\0"), "BODY_FILE_INVALID", "PR body contains a NUL byte");
  refuse(
    containsSelectedPaseoPassword(body),
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
    markEffect(options.stateFile, state, "publish.ready", "conditional_update", "complete", {
      number: pull.number,
    });
    return pull;
  }
  markDispatch(options.stateFile, state, "publish.ready", "conditional_update");
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
  markEffect(options.stateFile, state, "publish.ready", "conditional_update", "complete", {
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
  const manifest = validateReviewManifestEvidence(options);
  const validation = validateValidation(options);
  const state = loadState(options);
  persistState(options.stateFile, state);
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
    const completeBody = publicationBody(options);
    markDispatch(options.stateFile, state, "publish_draft.pr", "unique_create");
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
      markEffect(options.stateFile, state, "publish_draft.pr", "unique_create", "unknown");
      throw new CoordinatorError("PULL_REQUEST_RESULT_UNKNOWN", "draft pull request creation was not proven");
    }
    refuse(
      pull.state !== "open" || pull.draft !== true || pull.head?.sha !== options.candidate,
      "PULL_REQUEST_INVALID",
      "created pull request is not an open draft at the Candidate",
    );
  }
  markEffect(options.stateFile, state, "publish_draft.pr", "unique_create", "complete", {
    draft: true,
    number: pull.number,
    url: pull.html_url,
  });
  bindPullRequest(options.stateFile, state, pull.number);
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
  persistState(options.stateFile, state);
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
  markEffect(options.stateFile, state, "publish.pr", "conditional_update", "complete", {
    number: pull.number,
    url: pull.html_url,
  });
  bindPullRequest(options.stateFile, state, pull.number);
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
  persistState(options.stateFile, state);
  refuse(options.pr === "absent", "PULL_REQUEST_NUMBER_REQUIRED", "exact PR number is required");
  bindPullRequest(options.stateFile, state, options.pr);
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
  markDispatch(options.stateFile, state, "integrate.merge", "conditional_update");
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
    markEffect(options.stateFile, state, "integrate.merge", "conditional_update", "unknown");
    throw new CoordinatorError("MERGE_RESULT_UNKNOWN", "atomic merge was not proven after dispatch");
  }
  return persistIntegrationVerification(run, options, pull, state);
}

async function cleanupApply(run, options, deps) {
  const plan = validateCleanupPlan(options);
  const state = loadState(options, { required: true });
  ensureIntegratedState(options, state);
  if (state.cleanupPlanHash !== undefined) {
    refuse(state.cleanupPlanHash !== plan.planHash, "CLEANUP_PLAN_REPLACED", "another cleanup plan was already admitted");
  } else {
    state.cleanupPlanHash = plan.planHash;
    persistState(options.stateFile, state);
  }
  const task = taskFacts(run, options);

  if (options.lifecycleState === "active") {
    let facts = cleanupFacts(run, options, task, { allowAbsentWorkspace: state.effects["cleanup.workspace"]?.phase === "dispatching" || state.effects["cleanup.workspace"]?.phase === "complete" });
    if (facts.agent?.archived) {
      markEffect(options.stateFile, state, "cleanup.agent", "idempotent_close", "complete", { archivedAt: facts.agent.archivedAt });
    } else {
      markDispatch(options.stateFile, state, "cleanup.agent", "idempotent_close");
      await dispatchHook(deps, "before", "cleanup.agent", { options, state });
      const archived = run("paseo", ["archive", options.agentId, "--json"], { cwd: options.controlRepo });
      await dispatchHook(deps, "after", "cleanup.agent", { options, result: archived, state });
      const observed = validatedPaseoAgent(
        runJson(run, "paseo", ["inspect", options.agentId, "--json"]),
        options,
        task,
      );
      refuse(observed.Archived !== true, "AGENT_ARCHIVE_UNKNOWN", "Task Agent archive was not proven");
      markEffect(options.stateFile, state, "cleanup.agent", "idempotent_close", "complete", { archivedAt: observed.ArchivedAt });
    }

    facts = cleanupFacts(run, options, task, { allowAbsentWorkspace: state.effects["cleanup.workspace"]?.phase === "dispatching" || state.effects["cleanup.workspace"]?.phase === "complete" });
    if (facts.workspace === null) {
      refuse(
        state.effects["cleanup.workspace"]?.phase !== "dispatching" && state.effects["cleanup.workspace"]?.phase !== "complete",
        "WORKSPACE_ARCHIVE_AMBIGUOUS",
        "workspace absence is not tied to a recorded archive attempt",
      );
      markEffect(options.stateFile, state, "cleanup.workspace", "idempotent_close", "complete", { absent: true });
    } else {
      markDispatch(options.stateFile, state, "cleanup.workspace", "idempotent_close");
      await dispatchHook(deps, "before", "cleanup.workspace", { options, state });
      const archived = run("paseo", ["workspace", "archive", options.workspaceId, "--json"], { cwd: options.controlRepo });
      await dispatchHook(deps, "after", "cleanup.workspace", { options, result: archived, state });
      const workspaces = validatedPaseoWorkspaces(
        runJson(run, "paseo", ["workspace", "ls", "--json"]),
      );
      refuse(
        workspaces.some((workspace) => workspace.workspaceId === options.workspaceId),
        "WORKSPACE_ARCHIVE_UNKNOWN",
        "Paseo workspace archive was not proven",
      );
      markEffect(options.stateFile, state, "cleanup.workspace", "idempotent_close", "complete", { absent: true });
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
        options.stateFile,
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
      markDispatch(options.stateFile, state, "cleanup.worktree", "destructive_terminal");
      await dispatchHook(deps, "before", "cleanup.worktree", { options, state });
      facts = cleanupFacts(run, options, task, { allowAbsentWorkspace: true });
      if (facts.worktree !== null) {
        const removed = gitRaw(run, options.controlRepo, ["worktree", "remove", "--", options.checkout]);
        await dispatchHook(deps, "after", "cleanup.worktree", { options, result: removed, state });
        facts = cleanupFacts(run, options, task, { allowAbsentWorkspace: true });
      }
      refuse(facts.worktree !== null, "WORKTREE_REMOVAL_UNKNOWN", "worktree removal was not proven");
      markEffect(options.stateFile, state, "cleanup.worktree", "destructive_terminal", "complete", { absent: true });
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
      markEffect(options.stateFile, state, effect, "destructive_terminal", "complete", { absent: true });
      return;
    }
    refuse(
      record?.phase === "dispatching" || record?.phase === "unknown" || record?.phase === "complete",
      "DESTRUCTIVE_TARGET_PRESENT_AFTER_HANDOFF",
      `${kind} ref is present after a possible or completed deletion`,
    );
    markDispatch(options.stateFile, state, effect, "destructive_terminal");
    await dispatchHook(deps, "before", effect, { options, state });
    const immediate = cleanupFacts(run, options, task, { allowAbsentWorkspace: true });
    const immediateCurrent =
      kind === "remote" ? immediate.remoteBranch : immediate.localBranch;
    if (immediateCurrent === null) {
      markEffect(options.stateFile, state, effect, "destructive_terminal", "complete", { absent: true });
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
      markEffect(options.stateFile, state, effect, "destructive_terminal", "unknown", { present: true });
      throw new CoordinatorError("DESTRUCTIVE_RESULT_UNKNOWN", `${kind} ref deletion was not proven`);
    }
    markEffect(options.stateFile, state, effect, "destructive_terminal", "complete", { absent: true });
  }

  await destructiveRef("cleanup.remote-branch", "remote");
  await destructiveRef("cleanup.local-branch", "local");
  return { planHash: plan.planHash, resources: "complete" };
}

export async function execute(command, rawOptions, dependencies = {}) {
  const options = validateOptions(command, rawOptions);
  const run = dependencies.run ?? defaultCommandRunner;
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
      const state = loadState(options, { required: true });
      const integration = ensureIntegratedState(options, state);
      const task = taskFacts(run, options);
      const resources = cleanupFacts(run, options, task);
      const plan = {
        schemaVersion: 1,
        command: "cleanup-plan",
        binding: deliveryBinding(options),
        integration,
        resources,
      };
      result = { ...plan, planHash: digest(plan) };
    } else if (command === "cleanup-apply") {
      result = await cleanupApply(run, options, deps);
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
      containsSelectedPaseoPassword(output),
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
  return MUTATING_COMMANDS.has(command)
    ? withStateLock(options, operation)
    : operation();
}

export function errorOutput(command, error) {
  if (error instanceof CoordinatorInterruption) {
    return {
      schemaVersion: OUTPUT_SCHEMA_VERSION,
      command,
      outcome: "interrupted",
      code: "EXPECTED_INTERRUPTION",
      message: error.message,
      effect: error.effect,
    };
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
    if (containsSelectedPaseoPassword(output)) {
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
