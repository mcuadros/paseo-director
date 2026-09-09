// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { createHash, randomBytes } from "node:crypto";
import {
  chmodSync,
  existsSync,
  lstatSync,
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
  parseCli,
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
const PR_38_CHECKS_ONLY_OBSERVATION = Object.freeze({
  candidate: "542031762ce37a6de99ba5ad479e77fda0012770",
  checkRuns: Object.freeze({
    total_count: 1,
    check_runs: Object.freeze([
      Object.freeze({
        id: 102199975762,
        name: "Scaffold checks (Linux)",
        head_sha: "542031762ce37a6de99ba5ad479e77fda0012770",
        status: "completed",
        conclusion: "success",
      }),
    ]),
  }),
  commitStatus: Object.freeze({ state: "pending", total_count: 0, statuses: Object.freeze([]) }),
});

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
  const draftStateFile = join(root, "draft-state.json");
  const remoteCiStateFile = join(root, "remote-ci-state.json");
  const remoteCiFile = join(root, "remote-ci.json");
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
  const remoteObservationId = digest({ candidate, base, workflowRunId: 900001 });
  writeFileSync(
    remoteCiFile,
    `${canonicalJson({
      schemaVersion: 1,
      command: "remote-ci",
      outcome: "ready",
      result: {
        authoritative: true,
        task: TASK,
        candidate,
        base,
        observationId: remoteObservationId,
        checks: [
          {
            command: ["github-actions", "CI"],
            id: "maintained-linux-ci",
            status: "passed",
          },
        ],
        remote: { workflow: { id: 900001, name: "CI" } },
      },
    })}\n`,
  );
  writeFileSync(
    reviewHarnessFile,
    `${canonicalJson({
      schemaVersion: 1,
      command: "review-harness",
      outcome: "complete",
      result: {
        harnessVersion: 2,
        authoritative: false,
        manifestHash: manifest.manifestHash,
        candidate,
        base,
        attempt: 1,
        reason: "initial",
        reasonRecordHash: null,
        reviewId: "reviewer-0001",
        completeCi: {
          source: "authoritative-remote",
          observationId: remoteObservationId,
          startedByReview: false,
        },
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
    draftStateFile,
    remoteCiStateFile,
    remoteCiFile,
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
    checkRunsResponse: {
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
    },
    commitStatusResponse: { state: "success", total_count: 0, statuses: [] },
    workflowRunsResponse: {
      total_count: 1,
      workflow_runs: [
        {
          id: 900001,
          name: "CI",
          head_sha: fixture.candidate,
          status: "completed",
          conclusion: "success",
          run_attempt: 1,
        },
      ],
    },
    createDispatches: 0,
    draftCreateDispatches: 0,
    readyDispatches: 0,
    taskStatus: "in_progress",
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
      draft: record.draft === true,
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
          status: state.taskStatus,
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
        const draft = args.includes("--draft");
        state.createDispatches += 1;
        if (draft) state.draftCreateDispatches += 1;
        const number = state.nextPullNumber;
        state.nextPullNumber += 1;
        state.pull = {
          number,
          body: options.input,
          closed: false,
          draft,
          headSha: currentRemoteHead(),
          merged: false,
          mergeCommit: null,
        };
        return ok(`https://github.com/acme/director/pull/${number}\n`);
      }
      if (args[0] === "pr" && args[1] === "ready") {
        state.readyDispatches += 1;
        if (state.pull?.number === Number(args[2]) && !state.readyRefused) {
          state.pull.draft = false;
        }
        return ok();
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
        return ok(state.checkRunsResponse);
      }
      if (endpoint.startsWith(`repos/${REPOSITORY}/actions/runs?head_sha=`)) {
        return ok(state.workflowRunsResponse);
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
    "remote-ci-file": fixture.remoteCiFile,
    "validation-file": fixture.validationFile,
    "expected-remote-head": "absent",
    title: "feat(coordinator): automate delivery gates",
    "body-file": fixture.bodyFile,
    ...changes,
  };
}

function draftOptions(fixture, changes = {}) {
  return {
    ...fixture.options,
    "state-file": fixture.draftStateFile,
    "manifest-file": fixture.manifestFile,
    "validation-file": fixture.validationFile,
    "expected-remote-head": "absent",
    title: "feat(coordinator): automate delivery gates",
    "body-file": fixture.bodyFile,
    ...changes,
  };
}

function remoteCiOptions(fixture, changes = {}) {
  return {
    ...fixture.options,
    "state-file": fixture.remoteCiStateFile,
    "validation-file": fixture.validationFile,
    "ci-workflow": "CI",
    "required-check": ["Scaffold checks (Linux)"],
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
    "remote-ci-file": fixture.remoteCiFile,
    "validation-file": fixture.validationFile,
    "required-check": ["Scaffold checks (Linux)"],
    ...changes,
  };
}

async function publishReadyFixture(fixture, fake, changes = {}, dependencies = {}) {
  await execute("publish-draft", draftOptions(fixture, changes), {
    run: fake.runner,
  });
  return execute(
    "publish",
    publicationOptions(fixture, {
      ...changes,
      "expected-remote-head": fixture.candidate,
    }),
    { ...dependencies, run: fake.runner },
  );
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
  const remote = JSON.parse(readFileSync(fixture.remoteCiFile, "utf8"));
  remote.result.candidate = candidate;
  remote.result.observationId = digest({
    candidate,
    base: fixture.base,
    workflowRunId: 900001,
  });
  harness.result.completeCi.observationId = remote.result.observationId;
  writeFileSync(fixture.remoteCiFile, `${canonicalJson(remote)}\n`);
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

test("review handoff atomically maintains one private coordinator-native file", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture);
    const handoffFile = join(fixture.root, "handoff.json");
    const options = {
      ...fixture.options,
      actor: TASK_ASSIGNEE,
      "validation-file": fixture.validationFile,
      "handoff-file": handoffFile,
    };
    const first = await execute("review-handoff", options, { run: fake.runner });
    assert.deepEqual(JSON.parse(readFileSync(handoffFile, "utf8")), first);
    assert.equal(lstatSync(handoffFile).mode & 0o077, 0);

    const replay = await execute("review-handoff", options, { run: fake.runner });
    assert.deepEqual(replay, first);
    assert.deepEqual(JSON.parse(readFileSync(handoffFile, "utf8")), first);

    await assert.rejects(
      execute("review-handoff", {
        ...options,
        "handoff-file": join(fixture.checkout, "handoff.json"),
      }, { run: fake.runner }),
      (error) => error instanceof CoordinatorError && error.code === "HANDOFF_INSIDE_REPOSITORY",
    );
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

test("draft publication recovers after push-result interruption and creates one marked PR", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture);
    await assert.rejects(
      execute("publish-draft", draftOptions(fixture), {
        run: fake.runner,
        hook(phase, effect) {
          if (phase === "after" && effect === "publish_draft.push") {
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
    const output = await execute("publish-draft", draftOptions(fixture, {
      "expected-remote-head": fixture.candidate,
    }), {
      run: fake.runner,
    });
    assert.equal(output.result.pullRequest.number, 7);
    assert.equal(fake.state.createDispatches, 1);
    assert.match(fake.state.pull.body, new RegExp(marker(fixture).replace(/[.*+?^${}()|[\]\\]/gu, "\\$&"), "u"));

    const retry = await execute("publish-draft", draftOptions(fixture, {
      "expected-remote-head": fixture.candidate,
    }), {
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
    const first = await execute("publish-draft", draftOptions(fixture), {
      run: fake.runner,
    });
    const firstCandidate = fixture.candidate;
    assert.equal(first.result.pullRequest.number, 7);

    const secondCandidate = advanceFixtureCandidate(fixture, "second");
    fixture.draftStateFile = join(fixture.root, "draft-state-second.json");
    const second = await execute(
      "publish-draft",
      draftOptions(fixture, {
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
    await execute("publish-draft", draftOptions(fixture), { run: fake.runner });
    const firstCandidate = fixture.candidate;
    fake.state.historicalPulls.push({
      ...fake.state.pull,
      body: "closed historical PR without the current ownership marker",
      closed: true,
    });
    fake.state.pull = null;

    advanceFixtureCandidate(fixture, "after-closed");
    fixture.draftStateFile = join(fixture.root, "draft-state-after-closed.json");
    const output = await execute(
      "publish-draft",
      draftOptions(fixture, {
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
      "publish-draft",
      draftOptions(fixture, { title: hostileTitle }),
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

test("selected Paseo credentials and ownership never enter output, commands, or PR bodies", async () => {
  const fixture = createRepositoryFixture();
  const fake = fakeExternalCommands(fixture);
  const password = `director-state-${randomBytes(24).toString("hex")}`;
  const previous = process.env.PASEO_PASSWORD;
  process.env.PASEO_PASSWORD = password;
  try {
    const output = await execute("publish-draft", draftOptions(fixture), {
      run: fake.runner,
    });
    assert.equal(JSON.stringify(output).includes(password), false);
    assert.equal(readFileSync(fixture.draftStateFile, "utf8").includes(password), false);
    assert.equal(lstatSync(fixture.draftStateFile).mode & 0o077, 0);
    assert.equal(fake.state.pull.body.includes(password), false);
    assert.equal(fake.state.pull.body.includes(OWNERSHIP), false);
    assert.equal(JSON.stringify(output).includes(OWNERSHIP), false);
    assert.equal(
      fake.state.calls.some((call) => JSON.stringify(call).includes(password)),
      false,
    );
    assert.equal(
      fake.state.calls.some((call) => JSON.stringify(call).includes(OWNERSHIP)),
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
    const first = execute("publish-draft", draftOptions(fixture), {
      run: fake.runner,
      async hook(phase, effect) {
        if (phase === "before" && effect === "publish_draft.pr") {
          enterHook();
          await released;
        }
      },
    });
    await entered;
    await assert.rejects(
      execute("publish-draft", draftOptions(fixture), { run: fake.runner }),
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
      execute("publish-draft", draftOptions(fixture), {
        run: fake.runner,
        hook(phase, effect) {
          if (phase === "before" && effect === "publish_draft.push") {
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
    await publishReadyFixture(fixture, fake);
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
    await execute("publish-draft", draftOptions(publishFixture), {
      run: fake.runner,
    });
    await assert.rejects(
      execute("publish", publicationOptions(publishFixture, {
        "expected-remote-head": publishFixture.candidate,
      }), {
        run: fake.runner,
        hook(phase, effect) {
          if (phase === "before" && effect === "publish.ready") {
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
    assert.equal(fake.state.readyDispatches, 0);
  } finally {
    publishFixture.cleanup();
  }

  const gateFixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(gateFixture);
    await publishReadyFixture(gateFixture, fake);
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

test("gate admits the exact PR #38 checks-only GitHub fact with an explicit no-status rollup", async () => {
  assert.equal(
    PR_38_CHECKS_ONLY_OBSERVATION.checkRuns.check_runs[0].head_sha,
    PR_38_CHECKS_ONLY_OBSERVATION.candidate,
  );
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, {
      checkRunsResponse: {
        ...PR_38_CHECKS_ONLY_OBSERVATION.checkRuns,
        check_runs: PR_38_CHECKS_ONLY_OBSERVATION.checkRuns.check_runs.map((check) => ({
          ...check,
          head_sha: fixture.candidate,
        })),
      },
      commitStatusResponse: PR_38_CHECKS_ONLY_OBSERVATION.commitStatus,
    });
    await publishReadyFixture(fixture, fake);
    const output = await execute("gate", gateOptions(fixture), { run: fake.runner });
    assert.equal(output.outcome, "ready");
    assert.equal(output.result.checks.statusRollup, "checks_only_no_statuses");
    assert.deepEqual(output.result.checks.statuses, []);
  } finally {
    fixture.cleanup();
  }
});

test("gate retains exact-Candidate and complete-success requirements when status contexts exist", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, {
      commitStatusResponse: {
        state: "success",
        total_count: 2,
        statuses: [
          { id: 201, context: "legacy/unit", sha: fixture.candidate, state: "success" },
          { id: 202, context: "legacy/lint", sha: fixture.candidate, state: "success" },
        ],
      },
    });
    await publishReadyFixture(fixture, fake);
    const output = await execute("gate", gateOptions(fixture), { run: fake.runner });
    assert.equal(output.result.checks.statusRollup, "success");
    assert.deepEqual(output.result.checks.statuses, [
      { context: "legacy/unit", id: 201 },
      { context: "legacy/lint", id: 202 },
    ]);

    for (const response of [
      {
        state: "pending",
        total_count: 1,
        statuses: [
          { id: 203, context: "legacy/combined-pending", sha: fixture.candidate, state: "success" },
        ],
      },
      {
        state: "failure",
        total_count: 1,
        statuses: [
          { id: 204, context: "legacy/combined-failure", sha: fixture.candidate, state: "success" },
        ],
      },
      {
        state: "success",
        total_count: 1,
        statuses: [
          { id: 205, context: "legacy/context-failure", sha: fixture.candidate, state: "failure" },
        ],
      },
      {
        state: "success",
        total_count: 1,
        statuses: [
          { id: 206, context: "legacy/wrong-sha", sha: fixture.base, state: "success" },
        ],
      },
    ]) {
      fake.state.commitStatusResponse = response;
      await assert.rejects(
        execute("gate", gateOptions(fixture), { run: fake.runner }),
        (error) => error instanceof CoordinatorError && error.code === "STATUS_NOT_PASSED",
      );
    }
  } finally {
    fixture.cleanup();
  }
});

test("check and status pagination, check completion, and required-check identity fail closed", async () => {
  const cases = [
    {
      code: "CHECKS_INCOMPLETE",
      checks: { total_count: 2, check_runs: [] },
    },
    {
      code: "CHECK_NOT_PASSED",
      checks: {
        total_count: 1,
        check_runs: [
          {
            id: 301,
            name: "Scaffold checks (Linux)",
            status: "in_progress",
            conclusion: null,
          },
        ],
      },
    },
    {
      code: "CHECK_NOT_PASSED",
      checks: {
        total_count: 1,
        check_runs: [
          {
            id: 302,
            name: "Scaffold checks (Linux)",
            status: "completed",
            conclusion: "failure",
          },
        ],
      },
    },
    {
      code: "CHECK_SHA_MISMATCH",
      checks: {
        total_count: 1,
        check_runs: [
          {
            id: 306,
            name: "Scaffold checks (Linux)",
            head_sha: "0000000000000000000000000000000000000000",
            status: "completed",
            conclusion: "success",
          },
        ],
      },
    },
    {
      code: "REQUIRED_CHECK_AMBIGUOUS",
      checks: {
        total_count: 1,
        check_runs: [
          {
            id: 303,
            name: "Different check",
            status: "completed",
            conclusion: "success",
          },
        ],
      },
    },
    {
      code: "REQUIRED_CHECK_AMBIGUOUS",
      checks: {
        total_count: 2,
        check_runs: [
          {
            id: 304,
            name: "Scaffold checks (Linux)",
            status: "completed",
            conclusion: "success",
          },
          {
            id: 305,
            name: "Scaffold checks (Linux)",
            status: "completed",
            conclusion: "success",
          },
        ],
      },
    },
  ];
  for (const testCase of cases) {
    const fixture = createRepositoryFixture();
    try {
      const fake = fakeExternalCommands(fixture, {
        checkRunsResponse: {
          ...testCase.checks,
          check_runs: testCase.checks.check_runs.map((check) => ({
            head_sha: fixture.candidate,
            ...check,
          })),
        },
      });
      await publishReadyFixture(fixture, fake);
      await assert.rejects(
        execute("gate", gateOptions(fixture), { run: fake.runner }),
        (error) => error instanceof CoordinatorError && error.code === testCase.code,
      );
    } finally {
      fixture.cleanup();
    }
  }
});

test("nonzero legacy status pagination remains complete and bounded", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, {
      commitStatusResponse: {
        state: "success",
        total_count: 101,
        statuses: Array.from({ length: 100 }, (_, index) => ({
          id: 400 + index,
          context: `legacy/page-${index}`,
          sha: fixture.candidate,
          state: "success",
        })),
      },
    });
    await publishReadyFixture(fixture, fake);
    await assert.rejects(
      execute("gate", gateOptions(fixture), { run: fake.runner }),
      (error) => error instanceof CoordinatorError && error.code === "STATUSES_INCOMPLETE",
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
    await publishReadyFixture(fixture, fake);
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
    await publishReadyFixture(fixture, fake);
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
    await publishReadyFixture(fixture, fake);
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
    await publishReadyFixture(fixture, fake);
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
    await publishReadyFixture(fixture, fake);
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
    await publishReadyFixture(fixture, fake);
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
    await execute(
      "publish-draft",
      draftOptions(fixture, {
        "agent-id": "agent-0001",
        "lifecycle-state": "active",
        "workspace-id": "workspace-0001",
      }),
      { run: fake.runner },
    );
    const paseoCallsBeforeReclaim = fake.state.calls.filter(
      (call) => call.executable === "paseo",
    ).length;
    git(fixture.control, ["worktree", "remove", "--", fixture.checkout]);
    Object.assign(fixture.options, {
      "agent-id": "agent-0001",
      "checkout-state": "reclaimed",
      "lifecycle-state": "reclaimed",
      "workspace-id": "workspace-0001",
    });

    const published = await execute(
      "publish",
      publicationOptions(fixture, {
        "expected-remote-head": fixture.candidate,
      }),
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
      fake.state.calls.filter((call) => call.executable === "paseo").length,
      paseoCallsBeforeReclaim,
    );
  } finally {
    fixture.cleanup();
  }
});

test("interrupted destructive cleanup preserves a same-SHA recreation", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture);
    await publishReadyFixture(fixture, fake);
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
    await publishReadyFixture(fixture, fake, lifecycle);
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
    await publishReadyFixture(fixture, fake);
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

test("the CLI reads ownership only from an owner-only private file", () => {
  const fixture = createRepositoryFixture();
  const ownershipFile = join(fixture.root, "ownership");
  try {
    writeFileSync(ownershipFile, OWNERSHIP, { mode: 0o600 });
    const argv = ["snapshot"];
    for (const [key, value] of Object.entries(fixture.options)) {
      if (key !== "ownership") argv.push(`--${key}`, String(value));
    }
    argv.push("--ownership-file", ownershipFile);
    const parsed = parseCli(argv);
    assert.equal(parsed.options.ownership, OWNERSHIP);
    assert.equal(JSON.stringify(parsed).includes(ownershipFile), false);

    assert.throws(
      () => parseCli([...argv, "--ownership", OWNERSHIP]),
      (error) => error instanceof CoordinatorError && error.code === "OWNERSHIP_ARG_FORBIDDEN",
    );

    chmodSync(ownershipFile, 0o644);
    assert.throws(
      () => parseCli(argv),
      (error) => error instanceof CoordinatorError && error.code === "OWNERSHIP_FILE_INVALID",
    );
  } finally {
    fixture.cleanup();
  }
});

test("draft publication is idempotent, pre-review, and carries no merge authority", async () => {
  const fixture = createRepositoryFixture();
  const fake = fakeExternalCommands(fixture);
  try {
    const first = await execute("publish-draft", draftOptions(fixture), {
      run: fake.runner,
    });
    assert.equal(first.result.draft, true);
    assert.equal(first.result.mergeAuthorized, false);
    assert.equal(first.result.pullRequest.number, 7);
    assert.equal(first.result.remoteHead, fixture.candidate);
    assert.equal(fake.state.draftCreateDispatches, 1);
    assert.ok(first.result.pendingGates.includes("independent_review"));

    // Re-running with the same Candidate-bound state adopts the same draft
    // instead of opening a second pull request.
    const again = await execute(
      "publish-draft",
      draftOptions(fixture, { "expected-remote-head": fixture.candidate }),
      { run: fake.runner },
    );
    assert.equal(again.result.pullRequest.number, 7);
    assert.equal(fake.state.createDispatches, 1);

    // The gate refuses a draft outright, so an early draft cannot be merged on
    // its own no matter what other evidence exists.
    await assert.rejects(
      execute("gate", gateOptions(fixture), { run: fake.runner }),
      (error) => {
        assert.ok(error instanceof CoordinatorError);
        assert.equal(error.code, "PULL_REQUEST_NOT_OPEN");
        return true;
      },
    );

    // Review evidence has no place in a pre-review publication.
    await assert.rejects(
      execute(
        "publish-draft",
        draftOptions(fixture, {
          "expected-remote-head": fixture.candidate,
          "review-file": fixture.reviewFile,
        }),
        { run: fake.runner },
      ),
      (error) => {
        assert.ok(error instanceof CoordinatorError);
        assert.equal(error.code, "DRAFT_PUBLICATION_CANNOT_CONSUME_REVIEW");
        return true;
      },
    );
  } finally {
    fixture.cleanup();
  }
});

test("a corrected Candidate updates the same owned draft under an exact lease", async () => {
  const fixture = createRepositoryFixture();
  const fake = fakeExternalCommands(fixture);
  try {
    const firstCandidate = fixture.candidate;
    const first = await execute("publish-draft", draftOptions(fixture), {
      run: fake.runner,
    });
    assert.equal(first.result.pullRequest.number, 7);

    const corrected = advanceFixtureCandidate(fixture, "correction");
    fixture.draftStateFile = join(fixture.root, "draft-state-correction.json");
    const updated = await execute(
      "publish-draft",
      draftOptions(fixture, { "expected-remote-head": firstCandidate }),
      { run: fake.runner },
    );
    assert.equal(updated.result.candidate, corrected);
    assert.equal(updated.result.remoteHead, corrected);
    assert.equal(updated.result.pullRequest.number, 7);
    assert.equal(updated.result.draft, true);
    assert.equal(updated.result.mergeAuthorized, false);
    assert.equal(fake.state.createDispatches, 1);
    assert.equal(fake.state.draftCreateDispatches, 1);
  } finally {
    fixture.cleanup();
  }
});

test("handoff and draft publication refuse a blocked Task before mutation", async () => {
  const fixture = createRepositoryFixture();
  const fake = fakeExternalCommands(fixture, { taskStatus: "blocked" });
  try {
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
      (error) => error instanceof CoordinatorError && error.code === "TASK_STATE_INVALID",
    );
    await assert.rejects(
      execute("publish-draft", draftOptions(fixture), { run: fake.runner }),
      (error) => error instanceof CoordinatorError && error.code === "TASK_STATE_INVALID",
    );
    assert.equal(fake.state.createDispatches, 0);
    assert.equal(git(fixture.control, ["ls-remote", "--heads", "origin", `refs/heads/${BRANCH}`]), "");
  } finally {
    fixture.cleanup();
  }
});

test("post-review publication marks the owned draft ready and then gates", async () => {
  const fixture = createRepositoryFixture();
  const fake = fakeExternalCommands(fixture);
  try {
    await execute("publish-draft", draftOptions(fixture), { run: fake.runner });
    assert.equal(fake.state.pull.draft, true);

    const published = await execute(
      "publish",
      publicationOptions(fixture, { "expected-remote-head": fixture.candidate }),
      { run: fake.runner },
    );
    assert.equal(published.result.draft, false);
    assert.equal(published.result.pullRequest.number, 7);
    assert.equal(fake.state.readyDispatches, 1);
    assert.equal(fake.state.createDispatches, 1);

    const gated = await execute("gate", gateOptions(fixture), {
      run: fake.runner,
    });
    assert.equal(gated.result.pullRequest.number, 7);
    assert.equal(
      gated.result.remoteCi.observationId,
      JSON.parse(readFileSync(fixture.remoteCiFile, "utf8")).result.observationId,
    );
  } finally {
    fixture.cleanup();
  }
});

test("post-review publication adopts readiness after an interrupted dispatch", async () => {
  const fixture = createRepositoryFixture();
  const fake = fakeExternalCommands(fixture);
  try {
    await execute("publish-draft", draftOptions(fixture), { run: fake.runner });
    await assert.rejects(
      execute(
        "publish",
        publicationOptions(fixture, {
          "expected-remote-head": fixture.candidate,
        }),
        {
          run: fake.runner,
          hook(phase, effect) {
            if (phase === "after" && effect === "publish.ready") {
              throw new Error("simulated interruption after readiness dispatch");
            }
          },
        },
      ),
      /simulated interruption/,
    );
    assert.equal(fake.state.pull.draft, false);

    const recovered = await execute(
      "publish",
      publicationOptions(fixture, {
        "expected-remote-head": fixture.candidate,
      }),
      { run: fake.runner },
    );
    assert.equal(recovered.result.draft, false);
    assert.equal(recovered.result.pullRequest.number, 7);
    assert.equal(fake.state.readyDispatches, 1);
  } finally {
    fixture.cleanup();
  }
});

test("post-review publication cannot bypass the pre-review draft", async () => {
  const fixture = createRepositoryFixture();
  const fake = fakeExternalCommands(fixture);
  try {
    git(fixture.control, [
      "push",
      "--quiet",
      "origin",
      `${fixture.candidate}:refs/heads/${BRANCH}`,
    ]);
    await assert.rejects(
      execute(
        "publish",
        publicationOptions(fixture, {
          "expected-remote-head": fixture.candidate,
        }),
        { run: fake.runner },
      ),
      (error) => error instanceof CoordinatorError &&
        error.code === "DRAFT_PULL_REQUEST_REQUIRED",
    );
    assert.equal(fake.state.createDispatches, 0);
    assert.equal(fake.state.readyDispatches, 0);
  } finally {
    fixture.cleanup();
  }
});

test("publication refuses a pull request that will not leave draft", async () => {
  const fixture = createRepositoryFixture();
  const fake = fakeExternalCommands(fixture, { readyRefused: true });
  try {
    await execute("publish-draft", draftOptions(fixture), { run: fake.runner });
    await assert.rejects(
      execute(
        "publish",
        publicationOptions(fixture, { "expected-remote-head": fixture.candidate }),
        { run: fake.runner },
      ),
      (error) => {
        assert.ok(error instanceof CoordinatorError);
        assert.equal(error.code, "PULL_REQUEST_STILL_DRAFT");
        return true;
      },
    );
  } finally {
    fixture.cleanup();
  }
});

test("exactly one authoritative remote CI observation is recorded per Candidate", async () => {
  const fixture = createRepositoryFixture();
  const fake = fakeExternalCommands(fixture);
  try {
    await execute("publish-draft", draftOptions(fixture), { run: fake.runner });
    const observed = await execute("remote-ci", remoteCiOptions(fixture), {
      run: fake.runner,
    });
    assert.equal(observed.outcome, "ready");
    assert.equal(observed.result.authoritative, true);
    assert.equal(observed.result.candidate, fixture.candidate);
    assert.equal(observed.result.remote.workflow.id, 900001);
    assert.deepEqual(observed.result.checks, [
      {
        command: ["github-actions", "CI"],
        id: "maintained-linux-ci",
        status: "passed",
      },
    ]);

    const again = await execute("remote-ci", remoteCiOptions(fixture), {
      run: fake.runner,
    });
    assert.equal(again.result.observationId, observed.result.observationId);

    // A different authoritative run for the same Candidate must not quietly
    // replace the observation the recorded authority already points at.
    fake.state.workflowRunsResponse = {
      total_count: 1,
      workflow_runs: [
        {
          id: 900002,
          name: "CI",
          head_sha: fixture.candidate,
          status: "completed",
          conclusion: "success",
          run_attempt: 2,
        },
      ],
    };
    await assert.rejects(
      execute("remote-ci", remoteCiOptions(fixture), { run: fake.runner }),
      (error) => {
        assert.ok(error instanceof CoordinatorError);
        assert.equal(error.code, "REMOTE_CI_OBSERVATION_CHANGED");
        return true;
      },
    );
  } finally {
    fixture.cleanup();
  }
});

test("remote CI observation fails closed on absent, duplicated, pending, and failed runs", async () => {
  for (const [name, mutate, code] of [
    [
      "absent",
      (state) => {
        state.workflowRunsResponse = { total_count: 0, workflow_runs: [] };
      },
      "REMOTE_CI_ABSENT",
    ],
    [
      "duplicated",
      (state) => {
        state.workflowRunsResponse = {
          total_count: 2,
          workflow_runs: [
            { id: 1, name: "CI", head_sha: state.candidate, status: "completed", conclusion: "success" },
            { id: 2, name: "CI", head_sha: state.candidate, status: "completed", conclusion: "success" },
          ],
        };
      },
      "REMOTE_CI_NOT_AUTHORITATIVE",
    ],
    [
      "pending",
      (state) => {
        state.workflowRunsResponse.workflow_runs[0].status = "in_progress";
        state.workflowRunsResponse.workflow_runs[0].conclusion = null;
      },
      "REMOTE_CI_PENDING",
    ],
    [
      "failed",
      (state) => {
        state.workflowRunsResponse.workflow_runs[0].conclusion = "failure";
      },
      "REMOTE_CI_FAILED",
    ],
    [
      "pending required check",
      (state) => {
        state.checkRunsResponse.check_runs[0].status = "in_progress";
        state.checkRunsResponse.check_runs[0].conclusion = null;
      },
      "REMOTE_CI_PENDING",
    ],
    [
      "incomplete run page",
      (state) => {
        state.workflowRunsResponse.total_count = 2;
      },
      "REMOTE_CI_RUNS_INCOMPLETE",
    ],
  ]) {
    const fixture = createRepositoryFixture();
    const fake = fakeExternalCommands(fixture);
    fake.state.candidate = fixture.candidate;
    try {
      await execute("publish-draft", draftOptions(fixture), { run: fake.runner });
      mutate(fake.state);
      await assert.rejects(
        execute("remote-ci", remoteCiOptions(fixture), { run: fake.runner }),
        (error) => {
          assert.ok(error instanceof CoordinatorError, name);
          assert.equal(error.code, code, name);
          return true;
        },
      );
    } finally {
      fixture.cleanup();
    }
  }
});

test("the gate binds an authoritative-remote review to the exact recorded observation", async () => {
  const fixture = createRepositoryFixture();
  const fake = fakeExternalCommands(fixture);
  try {
    await execute("publish-draft", draftOptions(fixture), { run: fake.runner });
    const observed = await execute("remote-ci", remoteCiOptions(fixture), {
      run: fake.runner,
    });
    writeFileSync(fixture.remoteCiFile, `${canonicalJson(observed)}\n`);

    const harness = JSON.parse(readFileSync(fixture.reviewHarnessFile, "utf8"));
    harness.result.completeCi = {
      observationId: observed.result.observationId,
      source: "authoritative-remote",
      startedByReview: false,
    };
    writeFileSync(fixture.reviewHarnessFile, `${canonicalJson(harness)}\n`);

    await execute(
      "publish",
      publicationOptions(fixture, { "expected-remote-head": fixture.candidate }),
      { run: fake.runner },
    );

    // A Review that started no complete CI of its own must name the one
    // authoritative remote observation, or the gate cannot know which run the
    // approval rests on.
    await assert.rejects(
      execute("gate", gateOptions(fixture, { "remote-ci-file": undefined }), { run: fake.runner }),
      (error) => {
        assert.ok(error instanceof CoordinatorError);
        assert.equal(error.code, "OPTION_REQUIRED");
        return true;
      },
    );

    const gated = await execute(
      "gate",
      gateOptions(fixture, { "remote-ci-file": fixture.remoteCiFile }),
      { run: fake.runner },
    );
    assert.equal(gated.result.remoteCi.observationId, observed.result.observationId);
    assert.equal(gated.result.remoteCi.workflowRunId, 900001);

    const foreign = JSON.parse(readFileSync(fixture.remoteCiFile, "utf8"));
    foreign.result.observationId = digest("another observation");
    const foreignFile = join(fixture.root, "foreign-remote-ci.json");
    writeFileSync(foreignFile, `${canonicalJson(foreign)}\n`);
    await assert.rejects(
      execute("gate", gateOptions(fixture, { "remote-ci-file": foreignFile }), {
        run: fake.runner,
      }),
      (error) => {
        assert.ok(error instanceof CoordinatorError);
        assert.equal(error.code, "REMOTE_CI_OBSERVATION_MISMATCH");
        return true;
      },
    );
  } finally {
    fixture.cleanup();
  }
});

test("handoff supports active, restored, and historical workspace facts", async () => {
  const fixture = createRepositoryFixture();
  const fake = fakeExternalCommands(fixture);
  const bound = {
    ...fixture.options,
    "agent-id": "agent-0001",
    "workspace-id": "workspace-0001",
    "validation-file": fixture.validationFile,
  };
  try {
    const active = await execute(
      "review-handoff",
      { ...bound, "lifecycle-state": "active", actor: TASK_ASSIGNEE },
      { run: fake.runner },
    );
    assert.equal(active.result.snapshot.paseo.binding, "active");
    assert.equal(active.result.snapshot.paseo.verified, true);

    // A restored, renamed, or re-created Execution Workspace card no longer
    // resolves at its recorded ID while the Task Agent keeps running.
    await assert.rejects(
      execute(
        "review-handoff",
        { ...bound, "lifecycle-state": "restored", actor: TASK_ASSIGNEE },
        { run: fake.runner },
      ),
      (error) => {
        assert.ok(error instanceof CoordinatorError);
        assert.equal(error.code, "PASEO_WORKSPACE_RESTORATION_NOT_OBSERVED");
        return true;
      },
    );

    fake.state.workspaces = [];
    const restored = await execute(
      "review-handoff",
      { ...bound, "lifecycle-state": "restored", actor: TASK_ASSIGNEE },
      { run: fake.runner },
    );
    assert.equal(restored.result.snapshot.paseo.binding, "restored");
    assert.equal(restored.result.snapshot.paseo.verified, true);
    assert.equal(restored.result.snapshot.paseo.workspace.verified, false);
    assert.equal(restored.result.snapshot.paseo.agent.id, "agent-0001");

    // The active card being absent is not enough to claim it: an active
    // binding still fails closed on the missing workspace fact.
    await assert.rejects(
      execute(
        "review-handoff",
        { ...bound, "lifecycle-state": "active", actor: TASK_ASSIGNEE },
        { run: fake.runner },
      ),
      (error) => {
        assert.ok(error instanceof CoordinatorError);
        assert.equal(error.code, "PASEO_WORKSPACE_AMBIGUOUS");
        return true;
      },
    );

    // A restored binding still requires the exact owned checkout to be present:
    // the restored class only ever loses the workspace card, never the worktree.
    await assert.rejects(
      execute(
        "review-handoff",
        {
          ...bound,
          "lifecycle-state": "restored",
          "checkout-state": "reclaimed",
          actor: TASK_ASSIGNEE,
        },
        { run: fake.runner },
      ),
      (error) => {
        assert.ok(error instanceof CoordinatorError);
        assert.equal(error.code, "RECLAIMED_CHECKOUT_PRESENT");
        return true;
      },
    );

    // The historical class keeps both recorded IDs and claims no live fact.
    rmSync(fixture.checkout, { recursive: true, force: true });
    git(fixture.control, ["worktree", "prune"]);
    const historical = await execute(
      "review-handoff",
      {
        ...bound,
        "lifecycle-state": "reclaimed",
        "checkout-state": "reclaimed",
        actor: TASK_ASSIGNEE,
      },
      { run: fake.runner },
    );
    assert.equal(historical.result.snapshot.paseo.binding, "recorded_reclaimed");
    assert.equal(historical.result.snapshot.paseo.verified, false);
  } finally {
    fixture.cleanup();
  }
});
