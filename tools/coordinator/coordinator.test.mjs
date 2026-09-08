// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { createHash, randomBytes } from "node:crypto";
import {
  existsSync,
  mkdtempSync,
  mkdirSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  CoordinatorError,
  CoordinatorInterruption,
  canonicalJson,
  defaultCommandRunner,
  digest,
  execute,
} from "./coordinator.mjs";
import { verifyReviewManifest } from "./review-harness.mjs";

const TASK = "dir-m1.20";
const ACTOR = "paseo:11111111-2222-4333-8444-555555555555";
const TASK_ASSIGNEE = "paseo:aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee";
const REPOSITORY = "acme/director";
const REPOSITORY_ID = 42;
const TITLE = "Automate coordinator reconciliation and delivery gates";
const BRANCH = "task/dir-m1.20-coordinator-test";
const OWNERSHIP = "dir-m1.20-run-0001";
const DECISION_TEXT = "HUMAN DECISION — keep exact-head integration";

function command(executable, args, options = {}) {
  const result = defaultCommandRunner(executable, args, options);
  assert.equal(result.error, undefined, result.error?.message);
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim();
}

function git(cwd, args) {
  return command("git", args, { cwd });
}

function createRepositoryFixture() {
  const root = mkdtempSync(join(tmpdir(), "director-coordinator-test-"));
  const origin = join(root, "origin.git");
  const control = join(root, "control");
  const checkout = join(root, "task-checkout");
  mkdirSync(control);
  git(root, ["init", "--bare", "--quiet", origin]);
  git(control, ["init", "--quiet", "--initial-branch=main"]);
  git(control, ["config", "user.name", "Director Test"]);
  git(control, ["config", "user.email", "director@example.invalid"]);
  writeFileSync(join(control, "README.md"), "base\n");
  git(control, ["add", "README.md"]);
  git(control, ["commit", "--quiet", "-m", "base"]);
  const base = git(control, ["rev-parse", "HEAD"]);
  git(control, ["remote", "add", "origin", origin]);
  git(control, ["push", "--quiet", "origin", "main"]);
  git(control, ["worktree", "add", "--quiet", "-b", BRANCH, checkout, "main"]);
  writeFileSync(join(checkout, "candidate.txt"), "candidate\n");
  git(checkout, ["add", "candidate.txt"]);
  git(checkout, ["commit", "--quiet", "-m", "candidate"]);
  const candidate = git(checkout, ["rev-parse", "HEAD"]);

  const reviewFile = join(root, "review.json");
  const validationFile = join(root, "validation.json");
  const bodyFile = join(root, "body.md");
  const manifestFile = join(root, "manifest.json");
  const reviewHarnessFile = join(root, "review-harness.json");
  const stateFile = join(root, "state.json");
  const planFile = join(root, "cleanup-plan.json");
  writeFileSync(
    reviewFile,
    `${JSON.stringify({
      schemaVersion: 1,
      task: TASK,
      candidate,
      base,
      verdict: "approve_candidate",
      reviewer: {
        agentId: "reviewer-0001",
        parentAgentId: null,
        detached: true,
        checkoutCommit: candidate,
      },
      dimensions: [
        "acceptance",
        "correctness",
        "security",
        "maintainability",
        "readability",
        "design",
        "quality",
        "rigor",
      ].map((id) => ({ id, status: "covered" })),
      p2Risks: [],
      humanP2Acceptance: null,
    })}\n`,
  );
  writeFileSync(
    validationFile,
    `${JSON.stringify({
      schemaVersion: 1,
      task: TASK,
      candidate,
      base,
      checks: [
        {
          id: "maintained-linux-ci",
          command: ["npm", "run", "ci"],
          status: "passed",
        },
      ],
    })}\n`,
  );
  writeFileSync(bodyFile, "## Summary\n\nCoordinator fixture.\n");
  const acceptanceCriteria = "The exact coordinator gates are deterministic.";
  const validationDocument = JSON.parse(readFileSync(validationFile, "utf8"));
  const candidateTree = git(checkout, ["show", "-s", "--format=%T", candidate]);
  const baseTree = git(checkout, ["show", "-s", "--format=%T", base]);
  const rawDiff = git(checkout, [
    "diff-tree",
    "--no-commit-id",
    "--raw",
    "-z",
    "--full-index",
    base,
    candidate,
  ]);
  const manifest = {
    schemaVersion: 1,
    authoritative: false,
    task: {
      acceptanceCriteria,
      acceptanceCriteriaHash: digest(acceptanceCriteria),
      id: TASK,
      title: TITLE,
    },
    humanDecisions: [
      {
        author: "paseo:owner-0001",
        createdAt: "2026-09-08T00:00:00Z",
        id: "decision-0001",
        textHash: digest(DECISION_TEXT),
      },
    ],
    candidate: { sha: candidate, tree: candidateTree },
    base: { ref: "main", sha: base, tree: baseTree },
    diff: {
      changedPathCount: 1,
      changedPaths: ["candidate.txt"],
      format: "git-diff-tree-raw-v1",
      sha256: createHash("sha256").update(rawDiff).digest("hex"),
    },
    priorFindings: [],
    authorValidation: {
      checks: ["maintained-linux-ci"],
      digest: digest(validationDocument),
    },
    ownership: {
      actor: TASK_ASSIGNEE,
      agentId: "none",
      branch: BRANCH,
      checkout,
      checkoutState: "present",
      headOwner: "acme",
      ownershipTokenHash: digest(OWNERSHIP),
      remote: "origin",
      repository: REPOSITORY,
      repositoryId: REPOSITORY_ID,
      taskAssignee: TASK_ASSIGNEE,
      lifecycleState: "none",
      workspaceId: "none",
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
  manifest.manifestHash = digest(manifest);
  writeFileSync(manifestFile, `${canonicalJson(manifest)}\n`);
  writeFileSync(
    reviewHarnessFile,
    `${canonicalJson({
      schemaVersion: 1,
      command: "review-harness",
      outcome: "complete",
      result: {
        harnessVersion: 1,
        authoritative: false,
        manifestHash: manifest.manifestHash,
        candidate,
        base,
        attempt: 1,
        reason: "initial",
        reasonRecordHash: null,
        reviewId: "reviewer-0001",
        results: [
          { id: "maintained-linux-ci", status: "passed" },
          { id: "manifest-identity", status: "passed" },
        ],
      },
    })}\n`,
  );

  const options = {
    task: TASK,
    actor: ACTOR,
    repo: REPOSITORY,
    "repo-id": String(REPOSITORY_ID),
    remote: "origin",
    "base-ref": "main",
    base,
    branch: BRANCH,
    candidate,
    "head-owner": "acme",
    ownership: OWNERSHIP,
    checkout,
    "checkout-state": "present",
    "control-repo": control,
    "agent-id": "none",
    "workspace-id": "none",
    "lifecycle-state": "none",
    pr: "absent",
  };
  return {
    root,
    origin,
    control,
    checkout,
    base,
    candidate,
    reviewFile,
    validationFile,
    bodyFile,
    manifestFile,
    reviewHarnessFile,
    stateFile,
    planFile,
    options,
    cleanup() {
      rmSync(root, { recursive: true, force: true });
    },
  };
}

function marker(fixture) {
  return `<!-- director-coordinator task=${TASK} branch=${BRANCH} owner-sha256=${digest(OWNERSHIP)} -->`;
}

function fakeExternalCommands(fixture, overrides = {}) {
  const state = {
    agentArchived: false,
    agentArchiveDispatches: 0,
    archiveRemovesWorktree: false,
    beadsComments: [
      {
        id: "decision-0001",
        issue_id: TASK,
        author: "paseo:owner-0001",
        text: DECISION_TEXT,
        created_at: "2026-09-08T00:00:00Z",
      },
    ],
    comments: [],
    commitStatusResponse: { state: "success", total_count: 0, statuses: [] },
    createDispatches: 0,
    mergeDispatches: 0,
    mergeBaseDriftDuringDispatch: false,
    mergeSupported: true,
    historicalPulls: [],
    nextPullNumber: 7,
    pull: null,
    workspaces: [
      {
        workspaceId: "workspace-0001",
        project: "Director",
        name: TITLE,
        isolation: "worktree",
        cwd: fixture.checkout,
      },
    ],
    calls: [],
    ...overrides,
  };

  function currentRemoteHead() {
    const output = git(fixture.control, [
      "ls-remote",
      "--heads",
      "origin",
      `refs/heads/${BRANCH}`,
    ]);
    return output === "" ? null : output.split("\t")[0];
  }

  function pullObject(record) {
    if (!record) return null;
    const number = record.number;
    const headSha =
      record.closed || record.merged
        ? record.headSha
        : currentRemoteHead() ?? record.headSha;
    return {
      number,
      state: record.closed || record.merged ? "closed" : "open",
      draft: false,
      merged: record.merged,
      merged_at: record.merged ? "2026-09-08T00:00:00Z" : null,
      merge_commit_sha: record.mergeCommit ?? null,
      mergeable: true,
      mergeable_state: "clean",
      requested_reviewers: [],
      requested_teams: [],
      html_url: `https://github.com/acme/director/pull/${number}`,
      body: record.body,
      head: {
        sha: headSha,
        ref: BRANCH,
        user: { login: "acme" },
        repo: { id: REPOSITORY_ID },
      },
      base: { sha: fixture.base, ref: "main" },
    };
  }

  function ok(value = "") {
    return {
      error: undefined,
      status: 0,
      stdout: typeof value === "string" ? value : JSON.stringify(value),
      stderr: "",
    };
  }

  function runner(executable, args, options = {}) {
    state.calls.push({ executable, args: [...args], input: options.input });
    if (executable === "git") {
      const remoteIndex = args.indexOf("remote");
      if (
        remoteIndex >= 0 &&
        args[remoteIndex + 1] === "get-url" &&
        args[remoteIndex + 2] === "origin"
      ) {
        return ok("https://github.com/acme/director.git\n");
      }
      return defaultCommandRunner(executable, args, options);
    }
    if (executable === "bd") {
      if (args.includes("comments")) return ok(state.beadsComments);
      return ok([
        {
          id: TASK,
          title: TITLE,
          issue_type: "task",
          status: "in_progress",
          assignee: TASK_ASSIGNEE,
          acceptance_criteria: "The exact coordinator gates are deterministic.",
        },
      ]);
    }
    if (executable === "paseo") {
      if (args[0] === "inspect") {
        return ok({
          Id: "agent-0001",
          Name: TITLE,
          Status: state.agentArchived ? "idle" : "idle",
          Archived: state.agentArchived,
          ArchivedAt: state.agentArchived ? "2026-09-08T00:00:00Z" : null,
          Cwd: fixture.checkout,
          Worktree: BRANCH,
          ParentAgentId: null,
        });
      }
      if (args[0] === "archive") {
        state.agentArchiveDispatches += 1;
        state.agentArchived = true;
        return ok({ agentId: "agent-0001", status: "archived" });
      }
      if (args[0] === "workspace" && args[1] === "ls") return ok(state.workspaces);
      if (args[0] === "workspace" && args[1] === "archive") {
        if (state.archiveRemovesWorktree && existsSync(fixture.checkout)) {
          git(fixture.control, ["worktree", "remove", "--", fixture.checkout]);
        }
        state.workspaces = [];
        return ok({ workspaceId: "workspace-0001", status: "archived" });
      }
    }
    if (executable === "gh") {
      if (args[0] === "pr" && args[1] === "create") {
        state.createDispatches += 1;
        const number = state.nextPullNumber;
        state.nextPullNumber += 1;
        state.pull = {
          number,
          body: options.input,
          closed: false,
          headSha: currentRemoteHead(),
          merged: false,
          mergeCommit: null,
        };
        return ok(`https://github.com/acme/director/pull/${number}\n`);
      }
      if (args[0] === "pr" && args[1] === "merge" && args[2] === "--help") {
        return ok(state.mergeSupported ? "  --match-head-commit SHA\n" : "merge help\n");
      }
      if (args[0] === "pr" && args[1] === "merge") {
        state.mergeDispatches += 1;
        if (state.mergeBaseDriftDuringDispatch) {
          writeFileSync(
            join(fixture.control, "merge-base-drift.txt"),
            "moved during merge\n",
          );
          git(fixture.control, ["add", "merge-base-drift.txt"]);
          git(fixture.control, [
            "commit",
            "--quiet",
            "-m",
            "move base during merge",
          ]);
          git(fixture.control, ["push", "--quiet", "origin", "main"]);
        }
        git(fixture.control, ["merge", "--quiet", "--no-ff", "--no-edit", fixture.candidate]);
        git(fixture.control, ["push", "--quiet", "origin", "main"]);
        state.pull.merged = true;
        state.pull.headSha = fixture.candidate;
        state.pull.mergeCommit = git(fixture.control, ["rev-parse", "HEAD"]);
        return ok();
      }
      if (args[0] !== "api") return ok();
      const endpoint = args[1];
      if (endpoint === `repos/${REPOSITORY}`) {
        return ok({
          id: REPOSITORY_ID,
          full_name: REPOSITORY,
          archived: false,
          disabled: false,
        });
      }
      if (endpoint.startsWith(`repos/${REPOSITORY}/pulls?`)) {
        return ok([
          ...state.historicalPulls.map((pull) => pullObject(pull)),
          ...(state.pull ? [pullObject(state.pull)] : []),
        ]);
      }
      if (endpoint.startsWith(`repos/${REPOSITORY}/pulls/`)) {
        const number = Number(endpoint.split("/").at(-1));
        const record = [state.pull, ...state.historicalPulls].find(
          (pull) => pull?.number === number,
        );
        if (record) return ok(pullObject(record));
      }
      if (endpoint.endsWith(`/commits/${fixture.candidate}/check-runs?filter=latest&per_page=100`)) {
        return ok({
          total_count: 1,
          check_runs: [
            {
              id: 101,
              name: "Scaffold checks (Linux)",
              head_sha: fixture.candidate,
              status: "completed",
              conclusion: "success",
            },
          ],
        });
      }
      if (endpoint.endsWith(`/commits/${fixture.candidate}/status`)) {
        return ok(state.commitStatusResponse);
      }
      if (endpoint.endsWith(`/commits/${fixture.candidate}/status?per_page=100`)) {
        return ok(state.commitStatusResponse);
      }
      if (endpoint.endsWith("/pulls/7/reviews?per_page=100")) return ok([]);
      if (endpoint.endsWith("/issues/7/comments?per_page=100")) return ok(state.comments);
      if (endpoint === "graphql") {
        return ok({
          data: {
            repository: {
              pullRequest: {
                reviewThreads: { nodes: [], pageInfo: { hasNextPage: false } },
              },
            },
          },
        });
      }
    }
    return {
      error: undefined,
      status: 127,
      stdout: "",
      stderr: `unexpected fake command: ${executable} ${args.join(" ")}`,
    };
  }

  return { runner, state };
}

function publicationOptions(fixture, changes = {}) {
  return {
    ...fixture.options,
    "state-file": fixture.stateFile,
    "review-file": fixture.reviewFile,
    "manifest-file": fixture.manifestFile,
    "review-harness-file": fixture.reviewHarnessFile,
    "validation-file": fixture.validationFile,
    "expected-remote-head": "absent",
    title: "feat(coordinator): automate delivery gates",
    "body-file": fixture.bodyFile,
    ...changes,
  };
}

function gateOptions(fixture, changes = {}) {
  return {
    ...fixture.options,
    pr: "7",
    "review-file": fixture.reviewFile,
    "manifest-file": fixture.manifestFile,
    "review-harness-file": fixture.reviewHarnessFile,
    "validation-file": fixture.validationFile,
    "required-check": ["Scaffold checks (Linux)"],
    ...changes,
  };
}

function rebindReviewRouting(fixture, ownershipChanges) {
  const manifest = JSON.parse(readFileSync(fixture.manifestFile, "utf8"));
  delete manifest.manifestHash;
  manifest.ownership = { ...manifest.ownership, ...ownershipChanges };
  manifest.manifestHash = digest(manifest);
  writeFileSync(fixture.manifestFile, `${canonicalJson(manifest)}\n`);
  const harness = JSON.parse(readFileSync(fixture.reviewHarnessFile, "utf8"));
  harness.result.manifestHash = manifest.manifestHash;
  writeFileSync(fixture.reviewHarnessFile, `${canonicalJson(harness)}\n`);
}

function advanceFixtureCandidate(fixture, suffix) {
  writeFileSync(
    join(fixture.checkout, "candidate.txt"),
    `candidate\n${suffix}\n`,
  );
  git(fixture.checkout, ["add", "candidate.txt"]);
  git(fixture.checkout, ["commit", "--quiet", "-m", `candidate ${suffix}`]);
  const candidate = git(fixture.checkout, ["rev-parse", "HEAD"]);
  fixture.candidate = candidate;
  fixture.options.candidate = candidate;
  fixture.stateFile = join(fixture.root, `state-${suffix}.json`);

  const review = JSON.parse(readFileSync(fixture.reviewFile, "utf8"));
  review.candidate = candidate;
  review.reviewer.checkoutCommit = candidate;
  writeFileSync(fixture.reviewFile, `${canonicalJson(review)}\n`);

  const validation = JSON.parse(readFileSync(fixture.validationFile, "utf8"));
  validation.candidate = candidate;
  writeFileSync(fixture.validationFile, `${canonicalJson(validation)}\n`);

  const manifest = JSON.parse(readFileSync(fixture.manifestFile, "utf8"));
  delete manifest.manifestHash;
  manifest.candidate = {
    sha: candidate,
    tree: git(fixture.checkout, ["show", "-s", "--format=%T", candidate]),
  };
  const rawDiff = git(fixture.checkout, [
    "diff-tree",
    "--no-commit-id",
    "--raw",
    "-z",
    "--full-index",
    fixture.base,
    candidate,
  ]);
  manifest.diff.sha256 = createHash("sha256").update(rawDiff).digest("hex");
  manifest.authorValidation = {
    checks: ["maintained-linux-ci"],
    digest: digest(validation),
  };
  manifest.manifestHash = digest(manifest);
  writeFileSync(fixture.manifestFile, `${canonicalJson(manifest)}\n`);

  const harness = JSON.parse(readFileSync(fixture.reviewHarnessFile, "utf8"));
  harness.result.candidate = candidate;
  harness.result.manifestHash = manifest.manifestHash;
  writeFileSync(fixture.reviewHarnessFile, `${canonicalJson(harness)}\n`);
  return candidate;
}

test("snapshot batches Beads, Git, GitHub, and documented Paseo CLI facts", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture);
    const output = await execute(
      "snapshot",
      {
        ...fixture.options,
        "agent-id": "agent-0001",
        "lifecycle-state": "active",
        "workspace-id": "workspace-0001",
      },
      { run: fake.runner },
    );
    assert.equal(output.outcome, "complete");
    assert.equal(output.result.task.id, TASK);
    assert.equal(output.result.local.candidate, fixture.candidate);
    assert.equal(output.result.repository.id, REPOSITORY_ID);
    assert.equal(output.result.paseo.agent.id, "agent-0001");
    assert.equal(output.result.paseo.workspace.id, "workspace-0001");
  } finally {
    fixture.cleanup();
  }
});

test("review handoff refuses a dirty or moved Candidate", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture);
    writeFileSync(join(fixture.checkout, "untracked.txt"), "dirty\n");
    await assert.rejects(
      execute(
        "review-handoff",
        {
          ...fixture.options,
          actor: TASK_ASSIGNEE,
          "validation-file": fixture.validationFile,
        },
        { run: fake.runner },
      ),
      (error) => error instanceof CoordinatorError && error.code === "WORKTREE_DIRTY",
    );
  } finally {
    fixture.cleanup();
  }
});

test("review handoff generates a non-authoritative exact diff and evidence manifest", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture);
    const output = await execute(
      "review-handoff",
      {
        ...fixture.options,
        actor: TASK_ASSIGNEE,
        "validation-file": fixture.validationFile,
      },
      { run: fake.runner },
    );
    const manifest = output.result.manifest;
    assert.equal(manifest.authoritative, false);
    assert.equal(manifest.task.id, TASK);
    assert.equal(manifest.candidate.sha, fixture.candidate);
    assert.equal(manifest.base.sha, fixture.base);
    assert.deepEqual(manifest.diff.changedPaths, ["candidate.txt"]);
    assert.deepEqual(
      manifest.humanDecisions.map((decision) => decision.id),
      ["decision-0001"],
    );
    assert.match(manifest.manifestHash, /^[0-9a-f]{64}$/u);
    assert.deepEqual(manifest.pendingGates, [
      "independent_review",
      "publication",
      "remote_ci",
      "feedback",
      "integration",
      "cleanup",
      "task_closure",
    ]);
  } finally {
    fixture.cleanup();
  }
});

test("review harness mechanically verifies detached Candidate and unchanged base identity", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture);
    const handoff = await execute(
      "review-handoff",
      {
        ...fixture.options,
        actor: TASK_ASSIGNEE,
        "validation-file": fixture.validationFile,
      },
      { run: fake.runner },
    );
    const reviewer = join(fixture.root, "reviewer");
    git(fixture.control, [
      "worktree",
      "add",
      "--quiet",
      "--detach",
      reviewer,
      fixture.candidate,
    ]);
    const verified = verifyReviewManifest(handoff, reviewer, ACTOR, fake.runner);
    assert.equal(verified.status, "passed");
    assert.equal(verified.candidate, fixture.candidate);

    writeFileSync(join(fixture.control, "drift.txt"), "base drift\n");
    git(fixture.control, ["add", "drift.txt"]);
    git(fixture.control, ["commit", "--quiet", "-m", "base drift"]);
    git(fixture.control, ["push", "--quiet", "origin", "main"]);
    assert.throws(
      () => verifyReviewManifest(handoff, reviewer, ACTOR, fake.runner),
      (error) => error instanceof CoordinatorError && error.code === "REVIEW_BASE_MOVED",
    );
  } finally {
    fixture.cleanup();
  }
});

test("publication recovers after push-result interruption and creates one marked PR", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture);
    await assert.rejects(
      execute("publish", publicationOptions(fixture), {
        run: fake.runner,
        hook(phase, effect) {
          if (phase === "after" && effect === "publish.push") {
            throw new CoordinatorInterruption(effect);
          }
        },
      }),
      (error) => error instanceof CoordinatorInterruption,
    );
    assert.equal(
      git(fixture.control, ["ls-remote", "--heads", "origin", `refs/heads/${BRANCH}`]).split("\t")[0],
      fixture.candidate,
    );
    const output = await execute("publish", publicationOptions(fixture), {
      run: fake.runner,
    });
    assert.equal(output.result.pullRequest.number, 7);
    assert.equal(fake.state.createDispatches, 1);
    assert.match(fake.state.pull.body, new RegExp(marker(fixture).replace(/[.*+?^${}()|[\]\\]/gu, "\\$&"), "u"));

    const retry = await execute("publish", publicationOptions(fixture), {
      run: fake.runner,
    });
    assert.equal(retry.result.pullRequest.number, 7);
    assert.equal(fake.state.createDispatches, 1);
  } finally {
    fixture.cleanup();
  }
});

test("a corrected Candidate updates the branch and adopts the same open Task PR", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture);
    const first = await execute("publish", publicationOptions(fixture), {
      run: fake.runner,
    });
    const firstCandidate = fixture.candidate;
    assert.equal(first.result.pullRequest.number, 7);

    const secondCandidate = advanceFixtureCandidate(fixture, "second");
    const second = await execute(
      "publish",
      publicationOptions(fixture, {
        pr: "7",
        "expected-remote-head": firstCandidate,
      }),
      { run: fake.runner },
    );
    assert.notEqual(secondCandidate, firstCandidate);
    assert.equal(second.result.pullRequest.number, 7);
    assert.equal(second.result.remoteHead, secondCandidate);
    assert.equal(fake.state.createDispatches, 1);
    assert.match(fake.state.pull.body, /owner-sha256=[0-9a-f]{64}/u);
    assert.doesNotMatch(fake.state.pull.body, new RegExp(firstCandidate, "u"));
  } finally {
    fixture.cleanup();
  }
});

test("closed previous PRs do not block a new owned PR for a corrected Candidate", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture);
    await execute("publish", publicationOptions(fixture), { run: fake.runner });
    const firstCandidate = fixture.candidate;
    fake.state.historicalPulls.push({
      ...fake.state.pull,
      body: "closed historical PR without the current ownership marker",
      closed: true,
    });
    fake.state.pull = null;

    advanceFixtureCandidate(fixture, "after-closed");
    const output = await execute(
      "publish",
      publicationOptions(fixture, {
        "expected-remote-head": firstCandidate,
        pr: "absent",
      }),
      { run: fake.runner },
    );
    assert.equal(output.result.pullRequest.number, 8);
    assert.equal(fake.state.createDispatches, 2);
    assert.equal(fake.state.historicalPulls[0].headSha, firstCandidate);
  } finally {
    fixture.cleanup();
  }
});

test("argument arrays preserve hostile PR title text without shell execution", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture);
    const sentinel = join(fixture.root, "injected");
    const hostileTitle = `$(touch ${sentinel})`;
    await execute(
      "publish",
      publicationOptions(fixture, { title: hostileTitle }),
      { run: fake.runner },
    );
    assert.equal(existsSync(sentinel), false);
    const createCall = fake.state.calls.find(
      (call) => call.executable === "gh" && call.args[0] === "pr" && call.args[1] === "create",
    );
    assert.equal(createCall.args[createCall.args.indexOf("--title") + 1], hostileTitle);
  } finally {
    fixture.cleanup();
  }
});

test("selected Paseo credentials never enter state, output, or PR bodies", async () => {
  const fixture = createRepositoryFixture();
  const fake = fakeExternalCommands(fixture);
  const password = `director-state-${randomBytes(24).toString("hex")}`;
  const previous = process.env.PASEO_PASSWORD;
  process.env.PASEO_PASSWORD = password;
  try {
    const output = await execute("publish", publicationOptions(fixture), {
      run: fake.runner,
    });
    assert.equal(JSON.stringify(output).includes(password), false);
    assert.equal(readFileSync(fixture.stateFile, "utf8").includes(password), false);
    assert.equal(fake.state.pull.body.includes(password), false);
    assert.equal(
      fake.state.calls.some((call) => JSON.stringify(call).includes(password)),
      false,
    );
  } finally {
    if (previous === undefined) delete process.env.PASEO_PASSWORD;
    else process.env.PASEO_PASSWORD = previous;
    fixture.cleanup();
  }
});

test("one exact state lock prevents concurrent PR creation", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture);
    let enterHook;
    let releaseHook;
    const entered = new Promise((resolvePromise) => {
      enterHook = resolvePromise;
    });
    const released = new Promise((resolvePromise) => {
      releaseHook = resolvePromise;
    });
    const first = execute("publish", publicationOptions(fixture), {
      run: fake.runner,
      async hook(phase, effect) {
        if (phase === "before" && effect === "publish.pr") {
          enterHook();
          await released;
        }
      },
    });
    await entered;
    await assert.rejects(
      execute("publish", publicationOptions(fixture), { run: fake.runner }),
      (error) => error instanceof CoordinatorError && error.code === "COORDINATOR_BUSY",
    );
    releaseHook();
    await first;
    assert.equal(fake.state.createDispatches, 1);
  } finally {
    fixture.cleanup();
  }
});

test("an adversarial remote-head race is rejected by the exact Git lease", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture);
    await assert.rejects(
      execute("publish", publicationOptions(fixture), {
        run: fake.runner,
        hook(phase, effect) {
          if (phase === "before" && effect === "publish.push") {
            git(fixture.control, [
              "push",
              "--quiet",
              "origin",
              `${fixture.base}:refs/heads/${BRANCH}`,
            ]);
          }
        },
      }),
      (error) =>
        error instanceof CoordinatorError && error.code === "REMOTE_HEAD_CHANGED",
    );
    assert.equal(
      git(fixture.control, ["ls-remote", "--heads", "origin", `refs/heads/${BRANCH}`]).split("\t")[0],
      fixture.base,
    );
    assert.equal(fake.state.createDispatches, 0);
  } finally {
    fixture.cleanup();
  }
});

test("gate fails closed on human feedback and P2 without recorded acceptance", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture);
    await execute("publish", publicationOptions(fixture), { run: fake.runner });
    fake.state.comments = [{ id: 1, user: { login: "human", type: "User" } }];
    await assert.rejects(
      execute("gate", gateOptions(fixture), { run: fake.runner }),
      (error) => error instanceof CoordinatorError && error.code === "FEEDBACK_UNRESOLVED",
    );

    fake.state.comments = [];
    const review = JSON.parse(readFileSync(fixture.reviewFile, "utf8"));
    review.p2Risks = ["P2-RISK"];
    writeFileSync(fixture.reviewFile, `${JSON.stringify(review)}\n`);
    await assert.rejects(
      execute("gate", gateOptions(fixture), { run: fake.runner }),
      (error) => error instanceof CoordinatorError && error.code === "P2_ACCEPTANCE_MISSING",
    );
  } finally {
    fixture.cleanup();
  }
});

test("publish and gate refuse changed human-decision or prior-finding sets", async () => {
  const publishFixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(publishFixture);
    await assert.rejects(
      execute("publish", publicationOptions(publishFixture), {
        run: fake.runner,
        hook(phase, effect) {
          if (phase === "before" && effect === "publish.pr") {
            fake.state.beadsComments.push({
              id: "decision-after-review",
              author: "paseo:owner-0001",
              text: "HUMAN DECISION — stop publication",
              created_at: "2026-09-08T01:00:00Z",
            });
          }
        },
      }),
      (error) =>
        error instanceof CoordinatorError &&
        error.code === "REVIEW_DURABLE_CONTEXT_MOVED",
    );
    assert.equal(fake.state.createDispatches, 0);
  } finally {
    publishFixture.cleanup();
  }

  const gateFixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(gateFixture);
    await execute("publish", publicationOptions(gateFixture), { run: fake.runner });
    fake.state.beadsComments.push({
      id: "prior-finding-after-review",
      author: "paseo:reviewer-0002",
      text: `INDEPENDENT REVIEW — prior\nVerdict: changes_requested\nCandidate: ${"f".repeat(40)}`,
      created_at: "2026-09-08T01:00:00Z",
    });
    await assert.rejects(
      execute("gate", gateOptions(gateFixture), { run: fake.runner }),
      (error) =>
        error instanceof CoordinatorError &&
        error.code === "REVIEW_DURABLE_CONTEXT_MOVED",
    );
  } finally {
    gateFixture.cleanup();
  }
});

test("commit-status pagination and rolled-up state fail closed", async () => {
  for (const response of [
    { state: "success", total_count: 101, statuses: [] },
    { state: "pending", total_count: 0, statuses: [] },
    {
      state: "failure",
      total_count: 1,
      statuses: [
        {
          id: 1,
          context: "hidden-failure",
          sha: null,
          state: "failure",
        },
      ],
    },
  ]) {
    const fixture = createRepositoryFixture();
    try {
      const fake = fakeExternalCommands(fixture);
      await execute("publish", publicationOptions(fixture), { run: fake.runner });
      fake.state.commitStatusResponse = response;
      await assert.rejects(
        execute("gate", gateOptions(fixture), { run: fake.runner }),
        (error) =>
          error instanceof CoordinatorError &&
          ["STATUSES_INCOMPLETE", "STATUS_NOT_PASSED"].includes(error.code),
      );
      const statusCall = fake.state.calls.find(
        (call) =>
          call.executable === "gh" &&
          call.args.some((value) => value.endsWith("/status?per_page=100")),
      );
      assert.ok(statusCall);
    } finally {
      fixture.cleanup();
    }
  }
});

test("review evidence rejects Task-Agent, coordinator, and harness identity aliasing", async () => {
  for (const [reviewerId, harnessId, code] of [
    [TASK_ASSIGNEE.slice(6), TASK_ASSIGNEE.slice(6), "REVIEWER_NOT_INDEPENDENT"],
    [ACTOR.slice(6), ACTOR.slice(6), "REVIEWER_NOT_INDEPENDENT"],
    ["reviewer-0001", "reviewer-0002", "REVIEWER_HARNESS_IDENTITY_MISMATCH"],
  ]) {
    const fixture = createRepositoryFixture();
    try {
      const fake = fakeExternalCommands(fixture);
      const review = JSON.parse(readFileSync(fixture.reviewFile, "utf8"));
      review.reviewer.agentId = reviewerId;
      writeFileSync(fixture.reviewFile, `${canonicalJson(review)}\n`);
      const harness = JSON.parse(
        readFileSync(fixture.reviewHarnessFile, "utf8"),
      );
      harness.result.reviewId = harnessId;
      writeFileSync(
        fixture.reviewHarnessFile,
        `${canonicalJson(harness)}\n`,
      );
      await assert.rejects(
        execute("publish", publicationOptions(fixture), { run: fake.runner }),
        (error) => error instanceof CoordinatorError && error.code === code,
      );
      assert.equal(fake.state.createDispatches, 0);
    } finally {
      fixture.cleanup();
    }
  }
});

test("integration uses the atomic expected-head primitive and verifies parents and tree", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture);
    await execute("publish", publicationOptions(fixture), { run: fake.runner });
    const options = {
      ...gateOptions(fixture),
      "state-file": fixture.stateFile,
    };
    const gated = await execute("gate", options, { run: fake.runner });
    assert.equal(gated.outcome, "ready");
    const output = await execute("integrate", options, { run: fake.runner });
    assert.deepEqual(output.result.parents, [fixture.base, fixture.candidate]);
    assert.equal(output.result.candidateTree, git(fixture.checkout, ["show", "-s", "--format=%T", fixture.candidate]));
    assert.equal(fake.state.mergeDispatches, 1);
    const mergeCall = fake.state.calls.find(
      (call) => call.executable === "gh" && call.args[0] === "pr" && call.args[1] === "merge" && call.args[2] === "7",
    );
    assert.deepEqual(mergeCall.args.slice(-3), [
      "--merge",
      "--match-head-commit",
      fixture.candidate,
    ]);

    const retry = await execute("integrate", options, { run: fake.runner });
    assert.equal(retry.result.mergeCommit, output.result.mergeCommit);
    assert.equal(fake.state.mergeDispatches, 1);
  } finally {
    fixture.cleanup();
  }
});

test("integration refuses an unavailable expected-head merge primitive", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, { mergeSupported: false });
    await execute("publish", publicationOptions(fixture), { run: fake.runner });
    await assert.rejects(
      execute(
        "integrate",
        { ...gateOptions(fixture), "state-file": fixture.stateFile },
        { run: fake.runner },
      ),
      (error) => error instanceof CoordinatorError && error.code === "ATOMIC_MERGE_UNSUPPORTED",
    );
    assert.equal(fake.state.mergeDispatches, 0);
  } finally {
    fixture.cleanup();
  }
});

test("integration rechecks and refuses base drift immediately before dispatch", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture);
    await execute("publish", publicationOptions(fixture), { run: fake.runner });
    await assert.rejects(
      execute(
        "integrate",
        { ...gateOptions(fixture), "state-file": fixture.stateFile },
        {
          run: fake.runner,
          hook(phase, effect) {
            if (phase === "before" && effect === "integrate.merge") {
              writeFileSync(join(fixture.control, "base-race.txt"), "moved\n");
              git(fixture.control, ["add", "base-race.txt"]);
              git(fixture.control, ["commit", "--quiet", "-m", "move base"]);
              git(fixture.control, ["push", "--quiet", "origin", "main"]);
            }
          },
        },
      ),
      (error) => error instanceof CoordinatorError && error.code === "BASE_MOVED",
    );
    assert.equal(fake.state.mergeDispatches, 0);
  } finally {
    fixture.cleanup();
  }
});

test("a post-gate merge-base race persists manual reconciliation and blocks cleanup", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, {
      mergeBaseDriftDuringDispatch: true,
    });
    await execute("publish", publicationOptions(fixture), { run: fake.runner });
    const options = {
      ...gateOptions(fixture),
      "state-file": fixture.stateFile,
    };
    await assert.rejects(
      execute("integrate", options, { run: fake.runner }),
      (error) =>
        error instanceof CoordinatorError &&
        error.code === "INTEGRATION_MANUAL_RECONCILIATION_REQUIRED",
    );
    const state = JSON.parse(readFileSync(fixture.stateFile, "utf8"));
    assert.equal(
      state.effects["integrate.merge"].phase,
      "needs_manual_reconciliation",
    );
    assert.equal(
      state.effects["integrate.merge"].evidence.failureCode,
      "INTEGRATION_PARENTS_MISMATCH",
    );
    await assert.rejects(
      execute("cleanup-plan", options, { run: fake.runner }),
      (error) =>
        error instanceof CoordinatorError &&
        error.code === "INTEGRATION_MANUAL_RECONCILIATION_REQUIRED",
    );
    assert.equal(fake.state.mergeDispatches, 1);
  } finally {
    fixture.cleanup();
  }
});

test("integration rechecks binding human decisions immediately before dispatch", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture);
    await execute("publish", publicationOptions(fixture), { run: fake.runner });
    await assert.rejects(
      execute(
        "integrate",
        { ...gateOptions(fixture), "state-file": fixture.stateFile },
        {
          run: fake.runner,
          hook(phase, effect) {
            if (phase === "before" && effect === "integrate.merge") {
              fake.state.beadsComments.push({
                id: "decision-before-merge",
                author: "paseo:owner-0001",
                text: "HUMAN DECISION — stop integration",
                created_at: "2026-09-08T01:00:00Z",
              });
            }
          },
        },
      ),
      (error) =>
        error instanceof CoordinatorError &&
        error.code === "REVIEW_DURABLE_CONTEXT_MOVED",
    );
    assert.equal(fake.state.mergeDispatches, 0);
  } finally {
    fixture.cleanup();
  }
});

test("cleanup plan/apply removes only exact owned disposable resources and retries absence", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture);
    await execute("publish", publicationOptions(fixture), { run: fake.runner });
    const integratedOptions = {
      ...gateOptions(fixture),
      "state-file": fixture.stateFile,
    };
    await execute("integrate", integratedOptions, { run: fake.runner });
    const plan = await execute("cleanup-plan", integratedOptions, { run: fake.runner });
    writeFileSync(fixture.planFile, `${canonicalJson(plan)}\n`);
    const applyOptions = { ...integratedOptions, "plan-file": fixture.planFile };
    const applied = await execute("cleanup-apply", applyOptions, { run: fake.runner });
    assert.equal(applied.result.resources, "complete");
    assert.equal(existsSync(fixture.checkout), false);
    assert.equal(
      git(fixture.control, ["ls-remote", "--heads", "origin", `refs/heads/${BRANCH}`]),
      "",
    );
    assert.notEqual(
      defaultCommandRunner("git", ["show-ref", "--verify", `refs/heads/${BRANCH}`], {
        cwd: fixture.control,
      }).status,
      0,
    );

    const retry = await execute("cleanup-apply", applyOptions, { run: fake.runner });
    assert.equal(retry.result.resources, "complete");
  } finally {
    fixture.cleanup();
  }
});

test("post-handoff commands operate from exact refs after the Task checkout is reclaimed", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture);
    rebindReviewRouting(fixture, {
      agentId: "agent-0001",
      lifecycleState: "active",
      workspaceId: "workspace-0001",
    });
    git(fixture.control, ["worktree", "remove", "--", fixture.checkout]);
    Object.assign(fixture.options, {
      "agent-id": "agent-0001",
      "checkout-state": "reclaimed",
      "lifecycle-state": "reclaimed",
      "workspace-id": "workspace-0001",
    });

    const published = await execute(
      "publish",
      publicationOptions(fixture),
      { run: fake.runner },
    );
    assert.equal(published.result.pullRequest.number, 7);
    const integratedOptions = {
      ...gateOptions(fixture),
      "state-file": fixture.stateFile,
    };
    const gated = await execute("gate", integratedOptions, {
      run: fake.runner,
    });
    assert.equal(gated.result.snapshot.local.checkoutState, "reclaimed");
    assert.equal(gated.result.snapshot.paseo.verified, false);
    await execute("integrate", integratedOptions, { run: fake.runner });
    const plan = await execute("cleanup-plan", integratedOptions, {
      run: fake.runner,
    });
    assert.equal(plan.result.resources.worktree, null);
    assert.equal(
      plan.result.resources.agent.observation,
      "recorded_reclaimed",
    );
    writeFileSync(fixture.planFile, `${canonicalJson(plan)}\n`);
    await execute(
      "cleanup-apply",
      { ...integratedOptions, "plan-file": fixture.planFile },
      { run: fake.runner },
    );
    assert.equal(
      fake.state.calls.some((call) => call.executable === "paseo"),
      false,
    );
  } finally {
    fixture.cleanup();
  }
});

test("interrupted destructive cleanup preserves a same-SHA recreation", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture);
    await execute("publish", publicationOptions(fixture), { run: fake.runner });
    const integratedOptions = {
      ...gateOptions(fixture),
      "state-file": fixture.stateFile,
    };
    await execute("integrate", integratedOptions, { run: fake.runner });
    const plan = await execute("cleanup-plan", integratedOptions, { run: fake.runner });
    writeFileSync(fixture.planFile, `${canonicalJson(plan.result)}\n`);
    const applyOptions = { ...integratedOptions, "plan-file": fixture.planFile };
    await assert.rejects(
      execute("cleanup-apply", applyOptions, {
        run: fake.runner,
        hook(phase, effect) {
          if (phase === "after" && effect === "cleanup.remote-branch") {
            git(fixture.control, [
              "push",
              "--quiet",
              "origin",
              `${fixture.candidate}:refs/heads/${BRANCH}`,
            ]);
            throw new CoordinatorInterruption(effect);
          }
        },
      }),
      (error) => error instanceof CoordinatorInterruption,
    );
    await assert.rejects(
      execute("cleanup-apply", applyOptions, { run: fake.runner }),
      (error) =>
        error instanceof CoordinatorError &&
        error.code === "DESTRUCTIVE_TARGET_PRESENT_AFTER_HANDOFF",
    );
    assert.equal(
      git(fixture.control, ["ls-remote", "--heads", "origin", `refs/heads/${BRANCH}`]).split("\t")[0],
      fixture.candidate,
    );
  } finally {
    fixture.cleanup();
  }
});

test("cleanup reconciles exact public Paseo agent and workspace archival", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture);
    const lifecycle = {
      "agent-id": "agent-0001",
      "lifecycle-state": "active",
      "workspace-id": "workspace-0001",
    };
    rebindReviewRouting(fixture, {
      agentId: "agent-0001",
      lifecycleState: "active",
      workspaceId: "workspace-0001",
    });
    await execute(
      "publish",
      publicationOptions(fixture, lifecycle),
      { run: fake.runner },
    );
    const integratedOptions = {
      ...gateOptions(fixture, lifecycle),
      "state-file": fixture.stateFile,
    };
    await execute("integrate", integratedOptions, { run: fake.runner });
    const plan = await execute("cleanup-plan", integratedOptions, { run: fake.runner });
    writeFileSync(fixture.planFile, `${canonicalJson(plan)}\n`);
    const result = await execute(
      "cleanup-apply",
      { ...integratedOptions, "plan-file": fixture.planFile },
      { run: fake.runner },
    );
    assert.equal(result.result.resources, "complete");
    assert.equal(fake.state.agentArchiveDispatches, 1);
    assert.equal(fake.state.agentArchived, true);
    assert.deepEqual(fake.state.workspaces, []);
    assert.equal(existsSync(fixture.checkout), false);
    assert.equal(
      fake.state.calls.some(
        (call) =>
          call.executable === "git" && call.args.includes("worktree") &&
          call.args.includes("remove"),
      ),
      true,
    );
  } finally {
    fixture.cleanup();
  }
});

test("cleanup preserves a Task ref acquired by a foreign worktree", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture);
    await execute("publish", publicationOptions(fixture), { run: fake.runner });
    const integratedOptions = {
      ...gateOptions(fixture),
      "state-file": fixture.stateFile,
    };
    await execute("integrate", integratedOptions, { run: fake.runner });
    const plan = await execute("cleanup-plan", integratedOptions, { run: fake.runner });
    writeFileSync(fixture.planFile, `${canonicalJson(plan.result)}\n`);
    const foreign = join(fixture.root, "foreign-consumer");
    await assert.rejects(
      execute(
        "cleanup-apply",
        { ...integratedOptions, "plan-file": fixture.planFile },
        {
          run: fake.runner,
          hook(phase, effect) {
            if (phase === "before" && effect === "cleanup.local-branch") {
              git(fixture.control, ["worktree", "add", "--quiet", foreign, BRANCH]);
            }
          },
        },
      ),
      (error) =>
        error instanceof CoordinatorError &&
        error.code === "LOCAL_REF_HAS_FOREIGN_CONSUMER",
    );
    assert.equal(existsSync(foreign), true);
    assert.equal(git(foreign, ["rev-parse", "HEAD"]), fixture.candidate);
  } finally {
    fixture.cleanup();
  }
});

test("invalid branch and actor input is rejected before command dispatch", async () => {
  const fixture = createRepositoryFixture();
  try {
    let dispatches = 0;
    const run = () => {
      dispatches += 1;
      return { status: 0, stdout: "", stderr: "", error: undefined };
    };
    await assert.rejects(
      execute("snapshot", { ...fixture.options, branch: `${BRANCH};touch-pwned` }, { run }),
      (error) => error instanceof CoordinatorError && error.code === "INPUT_INVALID",
    );
    await assert.rejects(
      execute("snapshot", { ...fixture.options, actor: "human;bd" }, { run }),
      (error) => error instanceof CoordinatorError && error.code === "INPUT_INVALID",
    );
    assert.equal(dispatches, 0);
  } finally {
    fixture.cleanup();
  }
});
