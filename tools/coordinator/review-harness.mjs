#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0

import { createHash } from "node:crypto";
import { existsSync, lstatSync, realpathSync } from "node:fs";
import { isAbsolute, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { defaultCommandRunner, errorOutput } from "./coordinator.mjs";
import {
  CoordinatorError,
  assertExactKeys,
  canonicalJson,
  canonicalPath,
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

const HARNESS_VERSION = 2;
const REVIEW_STATE_SCHEMA_VERSION = 2;
const MAX_JSON_BYTES = 1_048_576;
const ACTOR_PATTERN = /^paseo:[a-zA-Z0-9-]{8,128}$/u;
const ID_PATTERN = /^[a-zA-Z0-9][a-zA-Z0-9._:-]{2,199}$/u;
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

function manifestFromDocument(document) {
  const manifest =
    document?.schemaVersion === 1 &&
    document?.command === "review-handoff" &&
    document?.outcome === "ready"
      ? document.result?.manifest
      : document;
  assertExactKeys(
    manifest,
    [
      "schemaVersion",
      "authoritative",
      "task",
      "humanDecisions",
      "candidate",
      "base",
      "diff",
      "priorFindings",
      "authorValidation",
      "ownership",
      "pendingGates",
      "manifestHash",
    ],
    "review manifest",
    "HARNESS_SCHEMA_INVALID",
  );
  refuse(
    manifest.schemaVersion !== 1 || manifest.authoritative !== false,
    "REVIEW_MANIFEST_INVALID",
    "review manifest must be schema v1 and explicitly non-authoritative",
  );
  refuse(
    typeof manifest.task?.acceptanceCriteria !== "string" ||
      digest(manifest.task.acceptanceCriteria) !==
        manifest.task.acceptanceCriteriaHash ||
      !Array.isArray(manifest.diff?.changedPaths) ||
      manifest.diff.changedPathCount !== manifest.diff.changedPaths.length,
    "REVIEW_MANIFEST_INVALID",
    "review manifest acceptance or diff summary is invalid",
  );
  assertExactKeys(
    manifest.ownership,
    HANDOFF_OWNERSHIP_KEYS,
    "review manifest ownership binding",
    "HARNESS_SCHEMA_INVALID",
  );
  const withoutHash = { ...manifest };
  delete withoutHash.manifestHash;
  refuse(
    manifest.manifestHash !== digest(withoutHash),
    "REVIEW_MANIFEST_HASH_MISMATCH",
    "review manifest hash is invalid",
  );
  return manifest;
}

function checked(run, executable, args, options = {}) {
  const result = run(executable, args, options);
  if (result.error || result.status !== 0) {
    throw new CoordinatorError(
      "REVIEW_CHECK_FAILED",
      `${executable} did not prove the review identity check`,
      { status: result.status },
    );
  }
  return result.stdout.trim();
}

function gitArgs(args) {
  return ["-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", ...args];
}

function git(run, cwd, args) {
  return checked(run, "git", gitArgs(args), { cwd });
}

function commentsReferences(comments, matcher) {
  refuse(!Array.isArray(comments), "REVIEW_TASK_COMMENTS_INVALID", "Beads comments are invalid");
  return comments
    .filter((comment) => {
      const text = comment.text ?? "";
      return typeof matcher === "function" ? matcher(text) : matcher.test(text);
    })
    .map((comment) => ({
      author: comment.author,
      createdAt: comment.created_at,
      id: comment.id,
      textHash: digest(comment.text),
    }))
    .toSorted((left, right) => compareText(left.id, right.id));
}

function parseJsonOutput(output, source) {
  return parseBoundedJson(output, source, {
    maximum: MAX_JSON_BYTES,
    oversizeCode: "REVIEW_EXTERNAL_JSON_OVERSIZE",
    invalidCode: "REVIEW_EXTERNAL_JSON_INVALID",
  });
}

export function verifyReviewManifest(document, checkoutPath, actor, run = defaultCommandRunner) {
  refuse(
    typeof actor !== "string" || !ACTOR_PATTERN.test(actor),
    "REVIEW_ACTOR_INVALID",
    "review actor must be an exact Paseo identity",
  );
  refuse(!isAbsolute(checkoutPath), "HARNESS_PATH_INVALID", "review checkout must be absolute");
  const checkout = realpathSync(checkoutPath);
  const manifest = manifestFromDocument(document);
  const head = git(run, checkout, ["rev-parse", "HEAD"]);
  const branch = git(run, checkout, ["branch", "--show-current"]);
  const status = git(run, checkout, ["status", "--porcelain=v2", "--untracked-files=all"]);
  refuse(head !== manifest.candidate.sha, "REVIEW_CANDIDATE_MOVED", "review checkout is not the Candidate");
  refuse(branch !== "", "REVIEW_CHECKOUT_ATTACHED", "review checkout is not detached");
  refuse(status !== "", "REVIEW_CHECKOUT_DIRTY", "review checkout is dirty");
  const remoteUrl = git(run, checkout, ["remote", "get-url", manifest.ownership.remote ?? "origin"]);
  refuse(
    githubRepositoryIdentity(remoteUrl)?.toLowerCase() !== manifest.ownership.repository.toLowerCase(),
    "REVIEW_REPOSITORY_MISMATCH",
    "review checkout remote identity changed",
  );
  const liveBaseResult = run("git", gitArgs([
    "ls-remote",
    "--exit-code",
    "--heads",
    manifest.ownership.remote ?? "origin",
    `refs/heads/${manifest.base.ref}`,
  ]), { cwd: checkout });
  refuse(
    liveBaseResult.error || liveBaseResult.status !== 0,
    "REVIEW_BASE_UNAVAILABLE",
    "live base identity is unavailable",
  );
  const liveRows = liveBaseResult.stdout.trim().split("\n").filter(Boolean);
  refuse(
    liveRows.length !== 1 || liveRows[0].split("\t")[0] !== manifest.base.sha,
    "REVIEW_BASE_MOVED",
    "live base no longer matches the review manifest",
  );
  const candidateTree = git(run, checkout, ["show", "-s", "--format=%T", manifest.candidate.sha]);
  const baseTree = git(run, checkout, ["show", "-s", "--format=%T", manifest.base.sha]);
  refuse(
    candidateTree !== manifest.candidate.tree || baseTree !== manifest.base.tree,
    "REVIEW_TREE_MISMATCH",
    "Candidate or base tree identity changed",
  );
  const rawDiff = git(run, checkout, [
    "diff-tree",
    "--no-commit-id",
    "--raw",
    "-z",
    "--full-index",
    manifest.base.sha,
    manifest.candidate.sha,
  ]);
  refuse(
    createHash("sha256").update(rawDiff).digest("hex") !== manifest.diff.sha256,
    "REVIEW_DIFF_MISMATCH",
    "Candidate diff identity changed",
  );
  const changedPaths = git(run, checkout, [
    "diff",
    "--name-only",
    "-z",
    manifest.base.sha,
    manifest.candidate.sha,
  ])
    .split("\0")
    .filter(Boolean)
    .toSorted();
  refuse(
    canonicalJson(changedPaths) !== canonicalJson(manifest.diff.changedPaths),
    "REVIEW_DIFF_MISMATCH",
    "Candidate changed-path identity changed",
  );
  const diffCheck = run("git", gitArgs([
    "diff",
    "--check",
    manifest.base.sha,
    manifest.candidate.sha,
  ]), { cwd: checkout });
  refuse(diffCheck.error || diffCheck.status !== 0, "REVIEW_DIFF_CHECK_FAILED", "Candidate diff check failed");

  const taskOutput = checked(run, "bd", [
    "--actor",
    actor,
    "show",
    manifest.task.id,
    "--json",
  ], { cwd: checkout });
  const tasks = parseJsonOutput(taskOutput, "bd show");
  refuse(
    !Array.isArray(tasks) ||
      tasks.length !== 1 ||
      tasks[0].id !== manifest.task.id ||
      digest(tasks[0].acceptance_criteria) !== manifest.task.acceptanceCriteriaHash,
    "REVIEW_TASK_MISMATCH",
    "Task acceptance identity changed",
  );
  const commentsOutput = checked(run, "bd", [
    "--actor",
    actor,
    "comments",
    manifest.task.id,
    "--json",
  ], { cwd: checkout });
  const comments = parseJsonOutput(commentsOutput, "bd comments");
  const humanDecisions = commentsReferences(comments, /^HUMAN DECISION\b/mu);
  const priorFindings = commentsReferences(comments, (text) =>
    isPriorReviewFinding(text, manifest.candidate.sha),
  );
  refuse(
    canonicalJson(humanDecisions) !== canonicalJson(manifest.humanDecisions) ||
      canonicalJson(priorFindings) !== canonicalJson(manifest.priorFindings),
    "REVIEW_DURABLE_CONTEXT_MOVED",
    "human decisions or prior finding references changed",
  );
  return {
    id: "manifest-identity",
    status: "passed",
    candidate: manifest.candidate.sha,
    base: manifest.base.sha,
    manifestHash: manifest.manifestHash,
  };
}

/**
 * Parses the coordinator's single authoritative remote CI observation. The
 * Review consumes this result instead of starting a second complete CI, which
 * is what lets Review and CI run concurrently on the exact same Candidate.
 * The document is bound to the manifest, so an observation for another
 * Candidate or base cannot be substituted.
 */
function remoteCiResult(path, manifest) {
  const document = readJsonFile(path, "remote CI observation", {
    maximum: MAX_JSON_BYTES,
    identityCode: "HARNESS_FILE_INVALID",
    invalidCode: "HARNESS_JSON_INVALID",
  });
  const result =
    document?.schemaVersion === 1 &&
    document?.command === "remote-ci" &&
    document?.outcome === "ready"
      ? document.result
      : document;
  refuse(
    !isObject(result) || result.authoritative !== true ||
      result.candidate !== manifest.candidate.sha ||
      result.base !== manifest.base.sha ||
      !Array.isArray(result.checks) || result.checks.length !== 1 ||
      typeof result.observationId !== "string" || result.observationId.length === 0,
    "REVIEW_REMOTE_CI_INVALID",
    "remote CI observation is not an authoritative result for this exact Candidate",
  );
  const [check] = result.checks;
  refuse(
    check?.id !== "maintained-linux-ci" || check?.status !== "passed" ||
      !Array.isArray(check?.command),
    "REVIEW_REMOTE_CI_NOT_PASSED",
    "remote CI observation does not record a passing complete Linux CI",
  );
  return {
    observationId: result.observationId,
    result: {
      command: check.command,
      id: "maintained-linux-ci",
      source: "authoritative-remote",
      status: "passed",
      workflowRunId: result.remote?.workflow?.id ?? null,
    },
  };
}

function preflightRemoteReviewEnvironment(checkout, run) {
  // A Review that consumes the remote CI observation runs no build, so it needs
  // only the Git facts the identity check reads. Requiring a prepared
  // node_modules here would reintroduce the cost the fast path removes.
  const result = run("git", ["--version"], { cwd: checkout });
  refuse(
    result.error || result.status !== 0,
    "REVIEW_TOOLCHAIN_NOT_PREPARED",
    "git is unavailable on the review harness PATH",
  );
  return {
    id: "review-environment",
    status: "passed",
    dependencyPreparation: null,
    toolchain: ["git"],
  };
}

function statePath(path, checkout) {
  const canonical = canonicalPath(path, "review state", { mustExist: false });
  refuse(
    pathIsWithin(checkout, canonical),
    "REVIEW_STATE_INSIDE_CHECKOUT",
    "review state must be outside the checkout",
  );
  return canonical;
}

function emptyReviewState() {
  return {
    schemaVersion: REVIEW_STATE_SCHEMA_VERSION,
    budgets: {},
    legacyReviews: {},
  };
}

function newCandidateBudget(candidate, base) {
  return {
    binding: { candidate, base },
    reviewIds: [],
    remoteObservationIds: [],
    observedManifestHashes: [],
    exceptions: [],
    manifests: {},
  };
}

function appendUnique(values, value) {
  if (!values.includes(value)) values.push(value);
}

function legacyReviewBinding(key, review, manifest) {
  const result = review?.output?.result;
  if (
    /^[0-9a-f]{40}$/u.test(result?.candidate ?? "") &&
    /^[0-9a-f]{40}$/u.test(result?.base ?? "") &&
    /^[0-9a-f]{64}$/u.test(result?.manifestHash ?? "") &&
    key === digest({
      candidate: result.candidate,
      base: result.base,
      manifestHash: result.manifestHash,
    })
  ) {
    return {
      candidate: result.candidate,
      base: result.base,
      manifestHash: result.manifestHash,
    };
  }
  const current = {
    candidate: manifest.candidate.sha,
    base: manifest.base.sha,
    manifestHash: manifest.manifestHash,
  };
  return key === digest(current) ? current : null;
}

function acceptedLegacyLockBindings(path, manifest) {
  const current = digest({
    candidate: manifest.candidate.sha,
    base: manifest.base.sha,
    manifestHash: manifest.manifestHash,
  });
  if (!existsSync(path)) return [current];
  const state = readJsonFile(path, "review state", {
    maximum: MAX_JSON_BYTES,
    identityCode: "REVIEW_STATE_INVALID",
    invalidCode: "HARNESS_JSON_INVALID",
  });
  if (state?.schemaVersion !== 1 || !isObject(state.reviews)) return [current];
  const accepted = [current];
  for (const [legacyKey, review] of Object.entries(state.reviews)) {
    const binding = legacyReviewBinding(legacyKey, review, manifest);
    if (
      binding?.candidate === manifest.candidate.sha &&
      binding.base === manifest.base.sha
    ) {
      appendUnique(accepted, legacyKey);
    }
  }
  return accepted;
}

function migrateLegacyReviewState(state, manifest) {
  const migrated = emptyReviewState();
  migrated.legacyReviews = structuredClone(state.reviews);
  for (const [legacyKey, review] of Object.entries(state.reviews)) {
    const binding = legacyReviewBinding(legacyKey, review, manifest);
    if (binding === null) continue;
    refuse(
      !isObject(review) || !Array.isArray(review.attempts),
      "REVIEW_STATE_INVALID",
      "bound legacy review state is invalid",
    );
    const budgetKey = digest({
      candidate: binding.candidate,
      base: binding.base,
    });
    const budget = migrated.budgets[budgetKey] ??
      newCandidateBudget(binding.candidate, binding.base);
    appendUnique(budget.observedManifestHashes, binding.manifestHash);
    for (const attempt of review.attempts) {
      if (typeof attempt?.reviewId === "string") {
        appendUnique(budget.reviewIds, attempt.reviewId);
      }
      if (typeof attempt?.remoteObservationId === "string") {
        appendUnique(
          budget.remoteObservationIds,
          attempt.remoteObservationId,
        );
      }
      if (attempt?.reason !== undefined && attempt.reason !== "initial") {
        budget.exceptions.push({
          manifestHash: binding.manifestHash,
          attempt: attempt.number ?? null,
          reason: attempt.reason,
          reasonRecordHash:
            attempt.reasonRecord === null || attempt.reasonRecord === undefined
              ? null
              : digest(attempt.reasonRecord),
        });
      }
    }
    if (
      review.attempts.length === 1 &&
      review.attempts[0]?.source === "authoritative-remote"
    ) {
      const [attempt] = review.attempts;
      const output =
        review.output === undefined
          ? undefined
          : structuredClone(review.output);
      if (isObject(output?.result)) output.result.reviewKey = budgetKey;
      budget.manifests[binding.manifestHash] = {
        manifestHash: binding.manifestHash,
        reviewId: attempt.reviewId,
        remoteObservationId: attempt.remoteObservationId,
        reason: attempt.reason,
        reasonRecord: attempt.reasonRecord,
        phase: attempt.phase,
        ...(attempt.results === undefined ? {} : { results: attempt.results }),
        ...(output === undefined ? {} : { output }),
      };
    }
    migrated.budgets[budgetKey] = budget;
  }
  return migrated;
}

function validateCandidateBudget(budget, budgetKey) {
  assertExactKeys(
    budget,
    [
      "binding",
      "reviewIds",
      "remoteObservationIds",
      "observedManifestHashes",
      "exceptions",
      "manifests",
    ],
    "review Candidate budget",
    "HARNESS_SCHEMA_INVALID",
  );
  if (isObject(budget.binding)) {
    assertExactKeys(
      budget.binding,
      ["candidate", "base"],
      "review Candidate binding",
      "HARNESS_SCHEMA_INVALID",
    );
  }
  refuse(
    !isObject(budget.binding) ||
      !/^[0-9a-f]{40}$/u.test(budget.binding.candidate ?? "") ||
      !/^[0-9a-f]{40}$/u.test(budget.binding.base ?? "") ||
      digest(budget.binding) !== budgetKey ||
      !Array.isArray(budget.reviewIds) ||
      !Array.isArray(budget.remoteObservationIds) ||
      !Array.isArray(budget.observedManifestHashes) ||
      !Array.isArray(budget.exceptions) ||
      !isObject(budget.manifests) ||
      budget.reviewIds.some(
        (reviewId) =>
          typeof reviewId !== "string" || !ID_PATTERN.test(reviewId),
      ) ||
      budget.remoteObservationIds.some(
        (observationId) =>
          typeof observationId !== "string" ||
          observationId.length < 1 ||
          observationId.length > 200,
      ) ||
      budget.observedManifestHashes.some(
        (manifestHash) => !/^[0-9a-f]{64}$/u.test(manifestHash),
      ) ||
      new Set(budget.reviewIds).size !== budget.reviewIds.length ||
      new Set(budget.remoteObservationIds).size !==
        budget.remoteObservationIds.length ||
      new Set(budget.observedManifestHashes).size !==
        budget.observedManifestHashes.length,
    "REVIEW_STATE_INVALID",
    "review Candidate budget is invalid",
  );
  for (const [manifestHash, attempt] of Object.entries(budget.manifests)) {
    if (isObject(attempt)) {
      assertExactKeys(
        attempt,
        [
          "manifestHash",
          "reviewId",
          "remoteObservationId",
          "reason",
          "reasonRecord",
          "phase",
          "results",
          "output",
        ],
        "review manifest attempt",
        "HARNESS_SCHEMA_INVALID",
      );
    }
    refuse(
      !isObject(attempt) ||
        attempt.manifestHash !== manifestHash ||
        !budget.observedManifestHashes.includes(manifestHash) ||
        !ID_PATTERN.test(attempt.reviewId ?? "") ||
        typeof attempt.remoteObservationId !== "string" ||
        !budget.remoteObservationIds.includes(attempt.remoteObservationId) ||
        attempt.reason !== "initial" ||
        attempt.reasonRecord !== null ||
        !["running", "complete"].includes(attempt.phase) ||
        (attempt.results !== undefined && !Array.isArray(attempt.results)) ||
        (attempt.output !== undefined && !isObject(attempt.output)),
      "REVIEW_STATE_INVALID",
      "review manifest attempt is invalid",
    );
  }
  for (const exception of budget.exceptions) {
    if (isObject(exception)) {
      assertExactKeys(
        exception,
        ["manifestHash", "attempt", "reason", "reasonRecordHash"],
        "review budget exception",
        "HARNESS_SCHEMA_INVALID",
      );
    }
    refuse(
      !isObject(exception) ||
        !/^[0-9a-f]{64}$/u.test(exception.manifestHash ?? "") ||
        (exception.attempt !== null &&
          !Number.isSafeInteger(exception.attempt)) ||
        typeof exception.reason !== "string" ||
        exception.reason.length < 1 ||
        exception.reason.length > 100 ||
        (exception.reasonRecordHash !== null &&
          !/^[0-9a-f]{64}$/u.test(exception.reasonRecordHash ?? "")),
      "REVIEW_STATE_INVALID",
      "review budget exception is invalid",
    );
  }
}

function loadReviewState(path, manifest) {
  if (!existsSync(path)) return emptyReviewState();
  const status = lstatSync(path);
  refuse(
    !status.isFile() || status.isSymbolicLink() || (status.mode & 0o077) !== 0,
    "REVIEW_STATE_INVALID",
    "review state must be a private regular file",
  );
  const state = readJsonFile(path, "review state", {
    maximum: MAX_JSON_BYTES,
    identityCode: "REVIEW_STATE_INVALID",
    invalidCode: "HARNESS_JSON_INVALID",
  });
  assertExactKeys(
    state,
    state.schemaVersion === 1
      ? ["schemaVersion", "reviews"]
      : ["schemaVersion", "budgets", "legacyReviews"],
    "review state",
    "HARNESS_SCHEMA_INVALID",
  );
  if (state.schemaVersion === 1) {
    refuse(!isObject(state.reviews), "REVIEW_STATE_INVALID", "legacy review state is invalid");
    return migrateLegacyReviewState(state, manifest);
  }
  refuse(
    state.schemaVersion !== REVIEW_STATE_SCHEMA_VERSION ||
      !isObject(state.budgets) ||
      !isObject(state.legacyReviews),
    "REVIEW_STATE_INVALID",
    "review state schema is invalid",
  );
  for (const [budgetKey, budget] of Object.entries(state.budgets)) {
    validateCandidateBudget(budget, budgetKey);
  }
  return state;
}

function persistReviewState(path, state) {
  persistPrivateJson(path, state, {
    identityCode: "REVIEW_STATE_INVALID",
    temporaryCode: "REVIEW_STATE_TEMP_EXISTS",
  });
}

async function withReviewStateLock(
  stateFile,
  reviewKey,
  acceptedBindingHashes,
  operation,
) {
  return withProcessIdentityLock(
    {
      lockPath: `${stateFile}.lock`,
      bindingHash: reviewKey,
      acceptedBindingHashes,
      label: "review state lock",
      busyCode: "REVIEW_HARNESS_BUSY",
      invalidCode: "REVIEW_STATE_LOCK_INVALID",
      replacedCode: "REVIEW_STATE_LOCK_REPLACED",
      processCode: "REVIEW_PROCESS_IDENTITY_UNAVAILABLE",
    },
    operation,
  );
}

function validateHarnessOptions(raw) {
  for (const key of ["manifest", "checkout", "state-file", "review-id", "actor", "reason", "remote-ci-file"]) {
    refuse(raw[key] === undefined, "HARNESS_OPTION_REQUIRED", `--${key} is required`);
  }
  refuse(
    raw.reason !== "initial" || raw["reason-record"] !== undefined,
    "HARNESS_INPUT_INVALID",
    "an authoritative remote CI observation is consumed once with reason initial",
  );
  refuse(!ID_PATTERN.test(raw["review-id"]), "HARNESS_INPUT_INVALID", "review ID is invalid");
  refuse(!ACTOR_PATTERN.test(raw.actor), "HARNESS_INPUT_INVALID", "actor is invalid");
  const checkout = realpathSync(raw.checkout);
  return {
    manifestPath: resolve(raw.manifest),
    checkout,
    stateFile: statePath(raw["state-file"], checkout),
    reviewId: raw["review-id"],
    actor: raw.actor,
    reason: raw.reason,
    reasonRecord: null,
    remoteCiFile: resolve(raw["remote-ci-file"]),
  };
}

export async function runReviewHarness(rawOptions, dependencies = {}) {
  const options = validateHarnessOptions(rawOptions);
  const document = readJsonFile(options.manifestPath, "review manifest", {
    maximum: MAX_JSON_BYTES,
    identityCode: "HARNESS_FILE_INVALID",
    invalidCode: "HARNESS_JSON_INVALID",
  });
  const manifest = manifestFromDocument(document);
  const reviewKey = digest({
    candidate: manifest.candidate.sha,
    base: manifest.base.sha,
  });
  const acceptedBindingHashes = acceptedLegacyLockBindings(
    options.stateFile,
    manifest,
  );
  const run = dependencies.run ?? defaultCommandRunner;
  const remoteCi = (dependencies.remoteCiResult ?? remoteCiResult)(
    options.remoteCiFile,
    manifest,
  );
  const environment = (
    dependencies.preflightRemoteReviewEnvironment ?? preflightRemoteReviewEnvironment
  )(options.checkout, run);
  return withReviewStateLock(
    options.stateFile,
    reviewKey,
    acceptedBindingHashes,
    async () => {
    const state = loadReviewState(options.stateFile, manifest);
    const budget = state.budgets[reviewKey] ??
      newCandidateBudget(manifest.candidate.sha, manifest.base.sha);
    validateCandidateBudget(budget, reviewKey);
    // Complete-CI authority is budgeted by the immutable Candidate/base pair.
    // Manifest hashes are history within that budget, not new budget keys.
    refuse(
      budget.remoteObservationIds.length > 1 ||
        (budget.remoteObservationIds.length === 1 &&
          budget.remoteObservationIds[0] !== remoteCi.observationId),
      "REVIEW_REMOTE_CI_CHANGED",
      "another authoritative remote CI observation was already consumed for this Candidate",
    );
    refuse(
      budget.reviewIds.length > 1 ||
        (budget.reviewIds.length === 1 &&
          budget.reviewIds[0] !== options.reviewId),
      "REVIEWER_IDENTITY_CHANGED",
      "another Reviewer already consumed the authoritative remote CI observation",
    );
    appendUnique(budget.remoteObservationIds, remoteCi.observationId);
    appendUnique(budget.reviewIds, options.reviewId);
    appendUnique(budget.observedManifestHashes, manifest.manifestHash);
    let attempt = budget.manifests[manifest.manifestHash];
    state.budgets[reviewKey] = budget;
    if (attempt !== undefined) {
      refuse(
        attempt.reviewId !== options.reviewId,
        "REVIEWER_IDENTITY_CHANGED",
        "another Reviewer already consumed the authoritative remote CI observation",
      );
      refuse(
        attempt.remoteObservationId !== remoteCi.observationId,
        "REVIEW_REMOTE_CI_CHANGED",
        "another authoritative remote CI observation was already consumed for this Candidate",
      );
      if (attempt.phase === "complete") {
        if (isObject(attempt.output)) return attempt.output;
        const failed = Array.isArray(attempt.results)
          ? attempt.results.filter((result) => result.status !== "passed")
          : [];
        refuse(
          failed.length > 0,
          "REVIEW_HARNESS_FAILED",
          "one or more independent review checks did not pass",
          { results: attempt.results },
        );
        throw new CoordinatorError(
          "REVIEW_STATE_INVALID",
          "completed remote review lacks its idempotent result",
        );
      }
    } else {
      attempt = {
        manifestHash: manifest.manifestHash,
        reviewId: options.reviewId,
        remoteObservationId: remoteCi.observationId,
        reason: options.reason,
        reasonRecord: options.reasonRecord,
        phase: "running",
      };
      budget.manifests[manifest.manifestHash] = attempt;
    }
    persistReviewState(options.stateFile, state);

    const ci = () => Promise.resolve(remoteCi.result);
    const verify = dependencies.verifyReviewManifest ?? verifyReviewManifest;
    const capture = async (id, operation) => {
      try {
        return await operation();
      } catch (error) {
        return {
          id,
          status: "invalid",
          code: error instanceof CoordinatorError ? error.code : "UNEXPECTED_CHECK_ERROR",
        };
      }
    };
    const [ciResult, identityResult] = await Promise.all([
      capture("maintained-linux-ci", () => ci(options.checkout)),
      capture("manifest-identity", () =>
        Promise.resolve().then(() =>
          verify(document, options.checkout, options.actor, run),
        ),
      ),
    ]);
    const results = [ciResult, identityResult].toSorted((left, right) =>
      compareText(left.id, right.id),
    );
    const failed = results.filter((result) => result.status !== "passed");
    attempt.phase = "complete";
    attempt.results = results;
    if (failed.length > 0) {
      persistReviewState(options.stateFile, state);
    }
    refuse(
      failed.length > 0,
      "REVIEW_HARNESS_FAILED",
      "one or more independent review checks did not pass",
      { results },
    );
    const output = {
      schemaVersion: 1,
      command: "review-harness",
      outcome: "complete",
      result: {
        harnessVersion: HARNESS_VERSION,
        authoritative: false,
        manifestHash: manifest.manifestHash,
        candidate: manifest.candidate.sha,
        base: manifest.base.sha,
        reviewId: options.reviewId,
        reviewKey,
        attempt: 1,
        reason: attempt.reason,
        reasonRecordHash:
          attempt.reasonRecord === null ? null : digest(attempt.reasonRecord),
        completeCi: {
          source: "authoritative-remote",
          observationId: remoteCi.observationId,
          startedByReview: false,
        },
        limits: { maxParallel: 2, maxOutputBytes: null, ciTimeoutMs: null },
        maintainedAdversarialHarness: [
          "tools/coordinator/coordinator.test.mjs",
          "tools/coordinator/internal.test.mjs",
          "tools/coordinator/paseo-auth.test.mjs",
          "tools/coordinator/review-harness.test.mjs",
        ],
        environment,
        results,
      },
    };
    attempt.output = output;
    persistReviewState(options.stateFile, state);
    if (typeof dependencies.afterPersist === "function") {
      await dependencies.afterPersist({
        candidate: manifest.candidate.sha,
        base: manifest.base.sha,
        manifestHash: manifest.manifestHash,
      });
    }
    return output;
  });
}

function parseArguments(argv) {
  const [command, ...tokens] = argv;
  refuse(command !== "run", "HARNESS_COMMAND_INVALID", "review harness command must be run");
  const options = {};
  for (let index = 0; index < tokens.length; index += 2) {
    const option = tokens[index];
    const value = tokens[index + 1];
    refuse(
      !option?.startsWith("--") || value === undefined || value.startsWith("--"),
      "HARNESS_ARGUMENT_INVALID",
      "review harness options require explicit values",
    );
    const key = option.slice(2);
    refuse(options[key] !== undefined, "HARNESS_ARGUMENT_INVALID", "duplicate option");
    options[key] = value;
  }
  return options;
}

const isMain = process.argv[1] && realpathSync(process.argv[1]) === fileURLToPath(import.meta.url);
if (isMain) {
  try {
    const output = await runReviewHarness(parseArguments(process.argv.slice(2)));
    process.stdout.write(`${canonicalJson(output)}\n`);
  } catch (error) {
    process.stdout.write(`${canonicalJson(errorOutput("review-harness", error))}\n`);
    process.exitCode = error instanceof CoordinatorError ? 2 : 1;
  }
}
