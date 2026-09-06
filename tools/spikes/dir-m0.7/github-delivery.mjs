#!/usr/bin/env node

import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import {
  chmodSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { arch, platform, release, tmpdir } from "node:os";
import { join, resolve, sep } from "node:path";

const SANDBOX_REPOSITORY = "mcuadros/paseo-director-github-delivery-sandbox";
const SANDBOX_REPOSITORY_ID = 1_359_322_331;
const FORBIDDEN_REPOSITORY = "mcuadros/paseo-director";
const API_VERSION = "2022-11-28";
const CHECK_NAME = "exact-candidate";
const WORKFLOW_NAME = "director-m0-7-candidate";
const MAX_COMMAND_OUTPUT = 4 * 1024 * 1024;
const COMMAND_TIMEOUT_MS = 180_000;
const CHECK_TIMEOUT_MS = 300_000;
const POLL_INTERVAL_MS = 5_000;
const OBSERVATION_ATTEMPTS = 3;
const OBSERVATION_RETRY_MS = 2_000;
const BOOTSTRAP_README = [
  "# Director GitHub delivery sandbox",
  "",
  "Private disposable repository for the dir-m0.7 exact-SHA delivery spike.",
  "The stable main branch is not a Task delivery target.",
  "",
].join("\n");

class NeedsYouError extends Error {}
class ExpectedInterruption extends Error {}

const effects = {
  pushDispatches: 0,
  pullRequestCreateDispatches: 0,
  feedbackCreateDispatches: 0,
  staleMergeDispatches: 0,
  mergeDispatches: 0,
  branchDeleteDispatches: 0,
};

function selectedEnvironment() {
  const selected = {};
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
    if (process.env[key] !== undefined) selected[key] = process.env[key];
  }
  selected.GH_PROMPT_DISABLED = "1";
  selected.GH_PAGER = "cat";
  selected.GIT_PAGER = "cat";
  selected.GIT_TERMINAL_PROMPT = "0";
  selected.GIT_CONFIG_NOSYSTEM = "1";
  selected.GCM_INTERACTIVE = "Never";
  return selected;
}

function sanitized(value) {
  return value
    .replaceAll(/https:\/\/[^/@\s]+@github\.com/giu, "https://[redacted]@github.com")
    .replaceAll(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/gu, "?")
    .slice(0, 2_000);
}

function command(executable, args, options = {}) {
  const result = spawnSync(executable, args, {
    cwd: options.cwd,
    encoding: "utf8",
    env: selectedEnvironment(),
    input: options.input,
    maxBuffer: MAX_COMMAND_OUTPUT,
    shell: false,
    timeout: options.timeout ?? COMMAND_TIMEOUT_MS,
  });
  if (options.allowFailure) return result;
  if (result.error || result.status !== 0) {
    const detail = sanitized(result.stderr || result.error?.message || "no diagnostic");
    throw new NeedsYouError(`${executable} failed with status ${result.status}: ${detail}`);
  }
  return result.stdout.trim();
}

function git(cwd, args, options = {}) {
  return command("git", ["-c", "core.hooksPath=/dev/null", ...args], { ...options, cwd });
}

function gh(args, options = {}) {
  return command("gh", args, options);
}

function ghJson(args, options = {}) {
  const output = gh(args, options);
  try {
    return JSON.parse(output);
  } catch {
    throw new NeedsYouError("GitHub returned invalid JSON");
  }
}

function api(path, options = {}) {
  const args = [
    "api",
    path,
    "--method",
    options.method ?? "GET",
    "-H",
    "Accept: application/vnd.github+json",
    "-H",
    `X-GitHub-Api-Version: ${API_VERSION}`,
  ];
  let input;
  if (options.body !== undefined) {
    args.push("--input", "-");
    input = JSON.stringify(options.body);
  }
  return ghJson(args, { input });
}

function exactSha(value, label = "SHA") {
  if (!/^[0-9a-f]{40}$/u.test(value ?? "")) {
    throw new NeedsYouError(`${label} is not an exact SHA-1 object ID`);
  }
  return value;
}

function branchName(value) {
  if (!/^spike\/dir-m0\.7\/[a-z0-9-]+\/(base|head|guard)$/u.test(value)) {
    throw new NeedsYouError("branch is outside the dir-m0.7 owned namespace");
  }
  return value;
}

function requireRepositoryIdentity(repository) {
  if (repository.toLowerCase() === FORBIDDEN_REPOSITORY) {
    throw new NeedsYouError("the public plugin repository is forbidden as a spike target");
  }
  if (repository !== SANDBOX_REPOSITORY) {
    throw new NeedsYouError("repository is not the human-authorized sandbox");
  }
}

function currentRepositoryFacts() {
  requireRepositoryIdentity(SANDBOX_REPOSITORY);
  const facts = api(`repos/${SANDBOX_REPOSITORY}`);
  assert.equal(facts.id, SANDBOX_REPOSITORY_ID);
  assert.equal(facts.full_name, SANDBOX_REPOSITORY);
  assert.equal(facts.private, true);
  assert.equal(facts.visibility, "private");
  assert.equal(facts.archived, false);
  assert.equal(facts.disabled, false);
  assert.equal(facts.permissions?.admin, true);
  assert.equal(facts.permissions?.push, true);
  assert.equal(facts.delete_branch_on_merge, false);
  return facts;
}

function parseRunId() {
  const candidate = process.env.DIRECTOR_SPIKE_RUN_ID
    ?? `run-${new Date().toISOString().slice(0, 10).replaceAll("-", "")}-${randomBytes(4).toString("hex")}`;
  if (!/^[a-z0-9][a-z0-9-]{7,47}$/u.test(candidate)) {
    throw new NeedsYouError("DIRECTOR_SPIKE_RUN_ID must be 8-48 lowercase letters, digits, or hyphens");
  }
  return candidate;
}

function sleep(milliseconds) {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, milliseconds));
}

function sleepSync(milliseconds) {
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, milliseconds);
}

function lsRemote(cwd, ref) {
  let lastResult;
  for (let attempt = 1; attempt <= OBSERVATION_ATTEMPTS; attempt += 1) {
    const result = git(cwd, ["ls-remote", "--exit-code", "--heads", "origin", ref], {
      allowFailure: true,
    });
    if (result.status === 0 || (result.status === 2 && result.stdout.trim() === "")) {
      return result;
    }
    lastResult = result;
    if (attempt < OBSERVATION_ATTEMPTS) sleepSync(OBSERVATION_RETRY_MS);
  }
  const detail = sanitized(lastResult.stderr || lastResult.error?.message || "no diagnostic");
  throw new NeedsYouError(`remote-ref observation failed with status ${lastResult.status}: ${detail}`);
}

function observeRemoteRef(cwd, branch) {
  branchName(branch);
  const ref = `refs/heads/${branch}`;
  const result = lsRemote(cwd, ref);
  if (result.status === 2 && result.stdout.trim() === "") return null;
  const rows = result.stdout.trim().split("\n").filter(Boolean);
  if (rows.length !== 1) throw new NeedsYouError("remote-ref observation was ambiguous");
  const [sha, observedRef] = rows[0].split("\t");
  assert.equal(observedRef, ref);
  return exactSha(sha, "remote-ref SHA");
}

function observeMain(cwd) {
  const result = lsRemote(cwd, "refs/heads/main");
  if (result.status === 2 && result.stdout.trim() === "") return null;
  const rows = result.stdout.trim().split("\n").filter(Boolean);
  if (rows.length !== 1) throw new NeedsYouError("main-ref observation was ambiguous");
  const [sha, observedRef] = rows[0].split("\t");
  assert.equal(observedRef, "refs/heads/main");
  return exactSha(sha, "main SHA");
}

function pushExact(cwd, branch, targetSha, expectedSha, options = {}) {
  branchName(branch);
  exactSha(targetSha, "push target");
  if (expectedSha !== null) exactSha(expectedSha, "push expected head");
  const actual = observeRemoteRef(cwd, branch);
  if (actual === targetSha) return { outcome: "already_complete", sha: actual };
  if (actual !== expectedSha) {
    throw new NeedsYouError("remote branch changed before exact push");
  }
  const ref = `refs/heads/${branch}`;
  effects.pushDispatches += 1;
  git(cwd, [
    "push",
    "--porcelain",
    `--force-with-lease=${ref}:${expectedSha ?? ""}`,
    "origin",
    `${targetSha}:${ref}`,
  ]);
  if (options.interruptAfterDispatch) throw new ExpectedInterruption("push result was not persisted");
  assert.equal(observeRemoteRef(cwd, branch), targetSha);
  return { outcome: "dispatched", sha: targetSha };
}

function pushMainIfAbsent(cwd, targetSha) {
  exactSha(targetSha, "bootstrap target");
  const actual = observeMain(cwd);
  if (actual === targetSha) return actual;
  if (actual !== null) throw new NeedsYouError("sandbox main already exists with unknown content");
  git(cwd, [
    "push",
    "--porcelain",
    "--force-with-lease=refs/heads/main:",
    "origin",
    `${targetSha}:refs/heads/main`,
  ]);
  assert.equal(observeMain(cwd), targetSha);
  return targetSha;
}

function deleteExact(cwd, branch, expectedSha, options = {}) {
  branchName(branch);
  exactSha(expectedSha, "delete expected head");
  const actual = observeRemoteRef(cwd, branch);
  if (actual === null) return { outcome: "already_absent" };
  if (actual !== expectedSha) {
    return { outcome: "refused_changed_ref", actual };
  }
  const ref = `refs/heads/${branch}`;
  effects.branchDeleteDispatches += 1;
  git(cwd, [
    "push",
    "--porcelain",
    `--force-with-lease=${ref}:${expectedSha}`,
    "origin",
    `:${ref}`,
  ]);
  if (options.interruptAfterDispatch) {
    throw new ExpectedInterruption("branch-delete result was not persisted");
  }
  assert.equal(observeRemoteRef(cwd, branch), null);
  return { outcome: "dispatched" };
}

function pullRequestMarker(runId) {
  return `<!-- director-spike:dir-m0.7:${runId} -->`;
}

function feedbackMarker(runId, candidateSha) {
  return `director-spike-feedback:dir-m0.7:${runId}:${candidateSha}`;
}

function listPullRequests(owner, baseBranch, headBranch, marker) {
  const head = encodeURIComponent(`${owner}:${headBranch}`);
  const base = encodeURIComponent(baseBranch);
  const pulls = api(`repos/${SANDBOX_REPOSITORY}/pulls?state=all&head=${head}&base=${base}&per_page=100`);
  if (!Array.isArray(pulls)) throw new NeedsYouError("pull-request list was invalid");
  return pulls.filter((pull) => {
    const sameRepository = pull.base?.repo?.id === SANDBOX_REPOSITORY_ID
      && pull.head?.repo?.id === SANDBOX_REPOSITORY_ID;
    return sameRepository
      && pull.base.ref === baseBranch
      && pull.head.ref === headBranch
      && pull.body?.includes(marker);
  });
}

function getPullRequest(number) {
  const pull = api(`repos/${SANDBOX_REPOSITORY}/pulls/${number}`);
  assert.equal(pull.base.repo.id, SANDBOX_REPOSITORY_ID);
  assert.equal(pull.head.repo.id, SANDBOX_REPOSITORY_ID);
  return pull;
}

function ensurePullRequest(cwd, owner, baseBranch, headBranch, marker, runId, options = {}) {
  const existing = listPullRequests(owner, baseBranch, headBranch, marker);
  if (existing.length > 1) throw new NeedsYouError("duplicate pull requests exist for one intent");
  if (existing.length === 1) return getPullRequest(existing[0].number);
  effects.pullRequestCreateDispatches += 1;
  gh([
    "pr",
    "create",
    "--repo",
    SANDBOX_REPOSITORY,
    "--base",
    baseBranch,
    "--head",
    `${owner}:${headBranch}`,
    "--title",
    `[dir-m0.7] Exact-SHA delivery ${runId}`,
    "--body",
    `${marker}\n\nDisposable exact-SHA delivery evidence. Do not review as product code.`,
    "--no-maintainer-edit",
  ], { cwd });
  if (options.interruptAfterDispatch) {
    throw new ExpectedInterruption("pull-request result was not persisted");
  }
  const created = listPullRequests(owner, baseBranch, headBranch, marker);
  if (created.length !== 1) throw new NeedsYouError("pull-request creation was not reconciled");
  return getPullRequest(created[0].number);
}

function listFeedback(number, owner, marker, candidateSha) {
  const reviews = api(`repos/${SANDBOX_REPOSITORY}/pulls/${number}/reviews?per_page=100`);
  if (!Array.isArray(reviews)) throw new NeedsYouError("review list was invalid");
  return reviews.filter((review) => review.user?.login === owner
    && review.commit_id === candidateSha
    && review.body?.includes(marker));
}

function ensureFeedback(number, owner, marker, candidateSha, options = {}) {
  const existing = listFeedback(number, owner, marker, candidateSha);
  if (existing.length > 1) throw new NeedsYouError("duplicate human feedback exists for one intent");
  if (existing.length === 1) return existing[0];
  effects.feedbackCreateDispatches += 1;
  const review = api(`repos/${SANDBOX_REPOSITORY}/pulls/${number}/reviews`, {
    method: "POST",
    body: {
      body: `${marker}\nFixture feedback: move the owned base and refresh the Candidate before integration.`,
      event: "COMMENT",
      commit_id: candidateSha,
    },
  });
  assert.equal(review.commit_id, candidateSha);
  assert.equal(review.user?.login, owner);
  if (options.interruptAfterDispatch) {
    throw new ExpectedInterruption("feedback result was not persisted");
  }
  const created = listFeedback(number, owner, marker, candidateSha);
  if (created.length !== 1) throw new NeedsYouError("feedback creation was not reconciled");
  return created[0];
}

async function waitForCandidateCheck(candidateSha) {
  const deadline = Date.now() + CHECK_TIMEOUT_MS;
  for (;;) {
    const response = api(`repos/${SANDBOX_REPOSITORY}/commits/${candidateSha}/check-runs?per_page=100`);
    const matching = response.check_runs?.filter((check) => check.name === CHECK_NAME
      && check.head_sha === candidateSha
      && check.app?.slug === "github-actions") ?? [];
    if (matching.length > 1) throw new NeedsYouError("duplicate check runs exist for one Candidate push");
    if (matching.length === 1 && matching[0].status === "completed") {
      if (matching[0].conclusion !== "success") {
        throw new NeedsYouError("Candidate check completed unsuccessfully");
      }
      return matching[0];
    }
    if (Date.now() >= deadline) throw new NeedsYouError("Candidate check did not complete in time");
    await sleep(POLL_INTERVAL_MS);
  }
}

async function waitForPullHead(number, expectedBaseBranch, expectedHead) {
  const deadline = Date.now() + 60_000;
  for (;;) {
    const pull = getPullRequest(number);
    if (pull.base.ref === expectedBaseBranch && pull.head.sha === expectedHead) return pull;
    if (Date.now() >= deadline) throw new NeedsYouError("pull-request head SHA did not converge");
    await sleep(POLL_INTERVAL_MS);
  }
}

function mergePullRequest(number, expectedCandidate, options = {}) {
  const before = getPullRequest(number);
  if (before.merged) {
    if (before.head.sha !== expectedCandidate || !before.merge_commit_sha) {
      throw new NeedsYouError("merged pull request does not match the expected Candidate");
    }
    return before;
  }
  if (before.head.sha !== expectedCandidate) {
    throw new NeedsYouError("pull-request head changed before merge");
  }
  effects.mergeDispatches += 1;
  gh([
    "pr",
    "merge",
    String(number),
    "--repo",
    SANDBOX_REPOSITORY,
    "--merge",
    "--match-head-commit",
    expectedCandidate,
  ]);
  if (options.interruptAfterDispatch) throw new ExpectedInterruption("merge result was not persisted");
  const after = getPullRequest(number);
  assert.equal(after.merged, true);
  assert.equal(after.head.sha, expectedCandidate);
  exactSha(after.merge_commit_sha, "merge commit");
  return after;
}

function assertStableBootstrap(cwd, mainSha) {
  exactSha(mainSha, "bootstrap main");
  git(cwd, ["fetch", "--no-write-fetch-head", "origin", "refs/heads/main"]);
  assert.equal(git(cwd, ["show", `${mainSha}:README.md`]), BOOTSTRAP_README.trim());
  assert.deepEqual(git(cwd, ["ls-tree", "-r", "--name-only", mainSha]).split("\n"), ["README.md"]);
}

function workflowSource(headBranch) {
  return [
    `name: ${WORKFLOW_NAME}`,
    "",
    "on:",
    "  push:",
    "    branches:",
    `      - \"${headBranch}\"`,
    "",
    "permissions:",
    "  contents: read",
    "",
    "jobs:",
    `  ${CHECK_NAME}:`,
    "    runs-on: ubuntu-latest",
    "    steps:",
    "      - name: Bind check to pushed Candidate",
    "        shell: bash",
    "        env:",
    "          EVENT_AFTER: ${{ github.event.after }}",
    "          GITHUB_SHA_VALUE: ${{ github.sha }}",
    "        run: test \"$GITHUB_SHA_VALUE\" = \"$EVENT_AFTER\"",
    "",
  ].join("\n");
}

function assertNoRunRefs(cwd, prefix) {
  const output = git(cwd, ["ls-remote", "--heads", "origin", `refs/heads/${prefix}/*`]);
  assert.equal(output, "");
}

function selfTest() {
  assert.throws(() => requireRepositoryIdentity(FORBIDDEN_REPOSITORY), NeedsYouError);
  assert.throws(() => requireRepositoryIdentity("mcuadros/another-repository"), NeedsYouError);
  assert.doesNotThrow(() => requireRepositoryIdentity(SANDBOX_REPOSITORY));
  assert.equal(exactSha("a".repeat(40)), "a".repeat(40));
  assert.throws(() => exactSha("main"), NeedsYouError);
  assert.equal(branchName("spike/dir-m0.7/fixture-123/head"), "spike/dir-m0.7/fixture-123/head");
  assert.throws(() => branchName("main"), NeedsYouError);
  return {
    assertions: [
      "public_repository_denied",
      "non_allowlisted_repository_denied",
      "exact_repository_allowed",
      "exact_sha_required",
      "owned_branch_namespace_required",
    ],
  };
}

async function run() {
  if (process.argv.includes("--self-test")) {
    process.stdout.write(`${JSON.stringify(selfTest(), null, 2)}\n`);
    return;
  }
  if (platform() !== "linux") throw new NeedsYouError("Director 1.0 supports Linux only");

  const runId = parseRunId();
  const owner = SANDBOX_REPOSITORY.split("/")[0];
  const prefix = `spike/dir-m0.7/${runId}`;
  const baseBranch = `${prefix}/base`;
  const headBranch = `${prefix}/head`;
  const guardBranch = `${prefix}/guard`;
  for (const branch of [baseBranch, headBranch, guardBranch]) branchName(branch);

  const repository = currentRepositoryFacts();
  const viewer = api("user");
  assert.equal(viewer.login, owner);

  const root = mkdtempSync(join(tmpdir(), "director-m0.7-"));
  chmodSync(root, 0o700);
  const checkout = join(root, "sandbox");
  const remoteUrl = `https://github.com/${SANDBOX_REPOSITORY}.git`;
  const expectedRemoteHeads = new Map();
  let pullNumber = null;
  let succeeded = false;
  let report;

  try {
    mkdirSync(checkout, { mode: 0o700 });
    git(checkout, ["init", "--initial-branch=main"]);
    git(checkout, ["config", "user.name", "Director Spike Fixture"]);
    git(checkout, ["config", "user.email", "director-spike@users.noreply.github.com"]);
    git(checkout, ["remote", "add", "origin", remoteUrl]);
    const observedRemote = git(checkout, ["remote", "get-url", "origin"]);
    assert.equal(observedRemote, remoteUrl);
    assert.equal(/https:\/\/[^/@]+@/u.test(observedRemote), false);

    let mainSha = observeMain(checkout);
    if (mainSha === null) {
      writeFileSync(join(checkout, "README.md"), BOOTSTRAP_README, { mode: 0o600 });
      git(checkout, ["add", "--", "README.md"]);
      git(checkout, ["commit", "-m", "Initialize private delivery sandbox"]);
      mainSha = exactSha(git(checkout, ["rev-parse", "HEAD"]), "bootstrap main");
      pushMainIfAbsent(checkout, mainSha);
    }
    assertStableBootstrap(checkout, mainSha);

    git(checkout, ["checkout", "-b", baseBranch, mainSha]);
    mkdirSync(join(checkout, ".github", "workflows"), { recursive: true, mode: 0o700 });
    writeFileSync(
      join(checkout, ".github", "workflows", "director-m0.7.yml"),
      workflowSource(headBranch),
      { mode: 0o600 },
    );
    writeFileSync(join(checkout, "base.txt"), `base-1 ${runId}\n`, { mode: 0o600 });
    git(checkout, ["add", "--", ".github/workflows/director-m0.7.yml", "base.txt"]);
    git(checkout, ["commit", "-m", `Create owned base for ${runId}`]);
    const base1 = exactSha(git(checkout, ["rev-parse", "HEAD"]), "base 1");
    assert.throws(
      () => pushExact(checkout, baseBranch, base1, null, { interruptAfterDispatch: true }),
      ExpectedInterruption,
    );
    assert.equal(pushExact(checkout, baseBranch, base1, null).outcome, "already_complete");
    expectedRemoteHeads.set(baseBranch, base1);

    git(checkout, ["checkout", "-b", headBranch, base1]);
    writeFileSync(join(checkout, "candidate.txt"), `candidate-1 ${runId}\n`, { mode: 0o600 });
    git(checkout, ["add", "--", "candidate.txt"]);
    git(checkout, ["commit", "-m", `Create first Candidate for ${runId}`]);
    const candidate1 = exactSha(git(checkout, ["rev-parse", "HEAD"]), "Candidate 1");
    assert.throws(
      () => pushExact(checkout, headBranch, candidate1, null, { interruptAfterDispatch: true }),
      ExpectedInterruption,
    );
    assert.equal(pushExact(checkout, headBranch, candidate1, null).outcome, "already_complete");
    expectedRemoteHeads.set(headBranch, candidate1);

    const marker = pullRequestMarker(runId);
    assert.throws(
      () => ensurePullRequest(checkout, owner, baseBranch, headBranch, marker, runId, {
        interruptAfterDispatch: true,
      }),
      ExpectedInterruption,
    );
    const pull = ensurePullRequest(checkout, owner, baseBranch, headBranch, marker, runId);
    pullNumber = pull.number;
    assert.equal(pull.state, "open");
    assert.equal(pull.base.sha, base1);
    assert.equal(pull.head.sha, candidate1);
    assert.equal(listPullRequests(owner, baseBranch, headBranch, marker).length, 1);

    const check1 = await waitForCandidateCheck(candidate1);
    const humanMarker = feedbackMarker(runId, candidate1);
    assert.throws(
      () => ensureFeedback(pullNumber, owner, humanMarker, candidate1, {
        interruptAfterDispatch: true,
      }),
      ExpectedInterruption,
    );
    const feedback = ensureFeedback(pullNumber, owner, humanMarker, candidate1);
    assert.equal(feedback.state, "COMMENTED");
    assert.equal(listFeedback(pullNumber, owner, humanMarker, candidate1).length, 1);

    git(checkout, ["checkout", baseBranch]);
    writeFileSync(join(checkout, "base-movement.txt"), `base-2 ${runId}\n`, { mode: 0o600 });
    git(checkout, ["add", "--", "base-movement.txt"]);
    git(checkout, ["commit", "-m", `Move owned base for ${runId}`]);
    const base2 = exactSha(git(checkout, ["rev-parse", "HEAD"]), "base 2");
    assert.throws(
      () => pushExact(checkout, baseBranch, base2, base1, { interruptAfterDispatch: true }),
      ExpectedInterruption,
    );
    assert.equal(pushExact(checkout, baseBranch, base2, base1).outcome, "already_complete");
    expectedRemoteHeads.set(baseBranch, base2);
    assert.equal(observeRemoteRef(checkout, baseBranch), base2);
    const movedPull = await waitForPullHead(pullNumber, baseBranch, candidate1);
    assert.equal(movedPull.base.ref, baseBranch);
    assert.notEqual(base2, base1);

    git(checkout, ["checkout", headBranch]);
    git(checkout, ["merge", "--no-ff", baseBranch, "-m", `Refresh Candidate after base movement for ${runId}`]);
    const candidate2 = exactSha(git(checkout, ["rev-parse", "HEAD"]), "Candidate 2");
    assert.notEqual(candidate2, candidate1);
    assert.equal(git(checkout, ["merge-base", "--is-ancestor", base2, candidate2], { allowFailure: true }).status, 0);
    assert.throws(
      () => pushExact(checkout, headBranch, candidate2, candidate1, { interruptAfterDispatch: true }),
      ExpectedInterruption,
    );
    assert.equal(pushExact(checkout, headBranch, candidate2, candidate1).outcome, "already_complete");
    expectedRemoteHeads.set(headBranch, candidate2);
    const refreshedPull = await waitForPullHead(pullNumber, baseBranch, candidate2);
    assert.equal(refreshedPull.merged, false);
    const check2 = await waitForCandidateCheck(candidate2);
    const historicalFeedback = listFeedback(pullNumber, owner, humanMarker, candidate1);
    assert.equal(historicalFeedback.length, 1);
    assert.notEqual(historicalFeedback[0].commit_id, candidate2);

    effects.staleMergeDispatches += 1;
    const staleMerge = gh([
      "pr",
      "merge",
      String(pullNumber),
      "--repo",
      SANDBOX_REPOSITORY,
      "--merge",
      "--match-head-commit",
      candidate1,
    ], { allowFailure: true });
    assert.notEqual(staleMerge.status, 0);
    const afterStaleMerge = getPullRequest(pullNumber);
    assert.equal(afterStaleMerge.merged, false);
    assert.equal(afterStaleMerge.head.sha, candidate2);

    assert.throws(
      () => mergePullRequest(pullNumber, candidate2, { interruptAfterDispatch: true }),
      ExpectedInterruption,
    );
    const mergedPull = mergePullRequest(pullNumber, candidate2);
    const mergeCommit = exactSha(mergedPull.merge_commit_sha, "merge commit");
    assert.equal(effects.mergeDispatches, 1);
    assert.equal(observeRemoteRef(checkout, baseBranch), mergeCommit);
    expectedRemoteHeads.set(baseBranch, mergeCommit);
    git(checkout, ["fetch", "--no-write-fetch-head", "origin", `refs/heads/${baseBranch}`]);
    assert.equal(git(checkout, ["merge-base", "--is-ancestor", candidate2, mergeCommit], {
      allowFailure: true,
    }).status, 0);
    const mergeParents = git(checkout, ["show", "-s", "--format=%P", mergeCommit]).split(" ");
    assert.deepEqual(mergeParents, [base2, candidate2]);

    assert.throws(
      () => pushExact(checkout, guardBranch, candidate2, null, { interruptAfterDispatch: true }),
      ExpectedInterruption,
    );
    assert.equal(pushExact(checkout, guardBranch, candidate2, null).outcome, "already_complete");
    pushExact(checkout, guardBranch, mergeCommit, candidate2);
    expectedRemoteHeads.set(guardBranch, mergeCommit);
    const changedRefRefusal = deleteExact(checkout, guardBranch, candidate2);
    assert.equal(changedRefRefusal.outcome, "refused_changed_ref");
    assert.equal(changedRefRefusal.actual, mergeCommit);
    assert.equal(observeRemoteRef(checkout, guardBranch), mergeCommit);

    const deletesBeforeCleanup = effects.branchDeleteDispatches;
    assert.throws(
      () => deleteExact(checkout, guardBranch, mergeCommit, { interruptAfterDispatch: true }),
      ExpectedInterruption,
    );
    assert.equal(deleteExact(checkout, guardBranch, mergeCommit).outcome, "already_absent");
    expectedRemoteHeads.delete(guardBranch);
    assert.throws(
      () => deleteExact(checkout, headBranch, candidate2, { interruptAfterDispatch: true }),
      ExpectedInterruption,
    );
    assert.equal(deleteExact(checkout, headBranch, candidate2).outcome, "already_absent");
    expectedRemoteHeads.delete(headBranch);
    assert.throws(
      () => deleteExact(checkout, baseBranch, mergeCommit, { interruptAfterDispatch: true }),
      ExpectedInterruption,
    );
    assert.equal(deleteExact(checkout, baseBranch, mergeCommit).outcome, "already_absent");
    expectedRemoteHeads.delete(baseBranch);
    assert.equal(effects.branchDeleteDispatches - deletesBeforeCleanup, 3);

    assertNoRunRefs(checkout, prefix);
    assert.equal(observeMain(checkout), mainSha);
    const finalPull = getPullRequest(pullNumber);
    assert.equal(finalPull.merged, true);
    assert.equal(finalPull.head.sha, candidate2);
    assert.equal(finalPull.merge_commit_sha, mergeCommit);
    assert.equal(listPullRequests(owner, baseBranch, headBranch, marker).length, 1);
    assert.equal(listFeedback(pullNumber, owner, humanMarker, candidate1).length, 1);
    assert.equal(check1.head_sha, candidate1);
    assert.equal(check2.head_sha, candidate2);
    assert.equal(effects.pullRequestCreateDispatches, 1);
    assert.equal(effects.feedbackCreateDispatches, 1);
    assert.equal(effects.staleMergeDispatches, 1);

    report = {
      outcome: "go",
      task: "dir-m0.7",
      host: {
        os: platform(),
        release: release(),
        arch: arch(),
        node: process.version,
        git: git(checkout, ["--version"]).replace(/^git version /u, ""),
        gh: gh(["--version"]).split("\n")[0].replace(/^gh version /u, ""),
      },
      topology: {
        repository: repository.full_name,
        repositoryId: repository.id,
        repositoryNodeId: repository.node_id,
        private: repository.private,
        apiVersion: API_VERSION,
        authenticatedOwner: owner,
        runId,
      },
      exactFacts: {
        main: mainSha,
        baseBeforeMovement: base1,
        candidateBeforeMovement: candidate1,
        baseAfterMovement: base2,
        pullBaseProjectionAfterMovement: movedPull.base.sha,
        candidateAfterMovement: candidate2,
        mergeCommit,
        pullRequestNumber: pullNumber,
        pullRequestUrl: finalPull.html_url,
        feedbackReviewId: feedback.id,
        feedbackCommit: feedback.commit_id,
        firstCheckRunId: check1.id,
        firstCheckHead: check1.head_sha,
        secondCheckRunId: check2.id,
        secondCheckHead: check2.head_sha,
      },
      assertions: [
        "public_plugin_repository_refused",
        "owned_branch_push_retry_reconciled_without_second_dispatch",
        "pull_request_retry_found_one_exact_repo_head_base_marker",
        "candidate_checks_completed_successfully_at_exact_head_shas",
        "human_feedback_retry_found_one_review_bound_to_candidate_1",
        "live_base_movement_invalidated_cached_base_sha",
        "pull_base_sha_projection_was_not_used_as_live_base_authority",
        "candidate_2_contains_moved_base",
        "candidate_1_feedback_is_historical_for_candidate_2",
        "stale_expected_head_merge_rejected",
        "current_expected_head_merge_dispatched_once",
        "merge_commit_has_exact_base_2_and_candidate_2_parents",
        "changed_ref_cleanup_refused_before_dispatch_and_preserved",
        "exact_branch_cleanup_retries_reconciled_from_absence",
        "stable_main_preserved",
        "all_run_owned_remote_refs_absent",
        "pull_request_left_merged_not_open",
      ],
      effects: { ...effects },
      cleanup: {
        runOwnedRemoteRefsAbsent: true,
        openPullRequestStateAbsent: true,
        stableMainPreserved: true,
        localRootRemoved: false,
        credentialBearingArtifactCreated: false,
        persistentProcessCreated: false,
        sandboxRepositoryRetainedForIndependentReview: true,
      },
    };
    succeeded = true;
  } finally {
    try {
      if (!succeeded && existsSync(checkout)) {
        if (pullNumber !== null) {
          try {
            const pull = getPullRequest(pullNumber);
            if (!pull.merged && pull.state === "open") {
              gh(["pr", "close", String(pullNumber), "--repo", SANDBOX_REPOSITORY], {
                cwd: checkout,
                allowFailure: true,
              });
            }
          } catch {
            // A network/identity ambiguity cannot authorize another PR effect.
          }
        }
        for (const [branch, expectedSha] of [...expectedRemoteHeads.entries()].reverse()) {
          try {
            deleteExact(checkout, branch, expectedSha);
          } catch {
            // Preserve an ambiguous or changed ref for explicit human recovery.
          }
        }
      }
    } finally {
      const resolvedRoot = resolve(root);
      const expectedPrefix = `${resolve(tmpdir())}${sep}director-m0.7-`;
      if (!resolvedRoot.startsWith(expectedPrefix)) {
        throw new NeedsYouError("temporary cleanup root lost its owned identity");
      }
      rmSync(resolvedRoot, { recursive: true, force: true });
    }
  }

  assert.equal(existsSync(root), false);
  report.cleanup.localRootRemoved = true;
  process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
}

await run();
