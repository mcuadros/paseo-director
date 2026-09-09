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
const MAX_JSON_BYTES = 1_048_576;
const ACTOR_PATTERN = /^paseo:[a-zA-Z0-9-]{8,128}$/u;
const ID_PATTERN = /^[a-zA-Z0-9][a-zA-Z0-9._:-]{2,199}$/u;

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

function loadReviewState(path) {
  if (!existsSync(path)) return { schemaVersion: 1, reviews: {} };
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
    ["schemaVersion", "reviews"],
    "review state",
    "HARNESS_SCHEMA_INVALID",
  );
  refuse(state.schemaVersion !== 1 || !isObject(state.reviews), "REVIEW_STATE_INVALID", "review state schema is invalid");
  return state;
}

function persistReviewState(path, state) {
  persistPrivateJson(path, state, {
    identityCode: "REVIEW_STATE_INVALID",
    temporaryCode: "REVIEW_STATE_TEMP_EXISTS",
  });
}

async function withReviewStateLock(stateFile, reviewKey, operation) {
  return withProcessIdentityLock(
    {
      lockPath: `${stateFile}.lock`,
      bindingHash: reviewKey,
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
    manifestHash: manifest.manifestHash,
  });
  const run = dependencies.run ?? defaultCommandRunner;
  const remoteCi = (dependencies.remoteCiResult ?? remoteCiResult)(
    options.remoteCiFile,
    manifest,
  );
  const environment = (
    dependencies.preflightRemoteReviewEnvironment ?? preflightRemoteReviewEnvironment
  )(options.checkout, run);
  return withReviewStateLock(options.stateFile, reviewKey, async () => {
    const state = loadReviewState(options.stateFile);
    const review = state.reviews[reviewKey] ?? { attempts: [] };
    let attempt;
    // The authoritative observation is consumed once per exact Candidate.
    // Replay by the same Reviewer adopts the completed record; a different
    // observation or Reviewer can never become another attempt.
    refuse(
      typeof review.remoteObservationId === "string" &&
        review.remoteObservationId !== remoteCi.observationId,
      "REVIEW_REMOTE_CI_CHANGED",
      "another authoritative remote CI observation was already consumed for this Candidate",
    );
    refuse(
      review.attempts.length > 1 ||
        review.attempts.some((item) => item.source !== "authoritative-remote"),
      "REVIEW_REMOTE_CI_ALREADY_CONSUMED",
      "the exact review already consumed another complete-CI source",
    );
    if (review.attempts.length === 1) {
      [attempt] = review.attempts;
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
        refuse(
          !isObject(review.output),
          "REVIEW_STATE_INVALID",
          "completed remote review lacks its idempotent result",
        );
        return review.output;
      }
    } else {
      attempt = {
        number: 1,
        reviewId: options.reviewId,
        reason: options.reason,
        reasonRecord: options.reasonRecord,
        source: "authoritative-remote",
        remoteObservationId: remoteCi.observationId,
        phase: "running",
      };
      review.attempts.push(attempt);
      review.remoteObservationId = remoteCi.observationId;
      state.reviews[reviewKey] = review;
      persistReviewState(options.stateFile, state);
    }

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
    attempt.phase = "complete";
    attempt.results = results;
    persistReviewState(options.stateFile, state);
    const failed = results.filter((result) => result.status !== "passed");
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
        attempt: attempt.number,
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
          "tools/coordinator/paseo-auth.test.mjs",
          "tools/coordinator/review-harness.test.mjs",
        ],
        environment,
        results,
      },
    };
    review.output = output;
    persistReviewState(options.stateFile, state);
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
    refuse(options[key] !== undefined, "HARNESS_ARGUMENT_INVALID", `duplicate --${key}`);
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
