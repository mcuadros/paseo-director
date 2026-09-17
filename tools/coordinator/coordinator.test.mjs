// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import {
  chmodSync,
  existsSync,
  lstatSync,
  mkdtempSync,
  mkdirSync,
  readFileSync,
  renameSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import {
  CoordinatorError,
  CoordinatorInterruption,
  canonicalJson,
  defaultCommandRunner,
  digest,
  errorOutput,
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

function createRepositoryFixture({ ownership = OWNERSHIP } = {}) {
  const root = mkdtempSync(join(tmpdir(), "director-coordinator-test-"));
  const origin = join(root, "origin.git");
  const control = join(root, "control");
  const worktreeRoot = join(root, "worktrees");
  const checkout = join(worktreeRoot, "task-checkout");
  mkdirSync(control);
  mkdirSync(worktreeRoot);
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
      ownershipTokenHash: digest(ownership),
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
    ownership,
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
    worktreeRoot,
    base,
    candidate,
    ownership,
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
  return `<!-- director-coordinator task=${TASK} branch=${BRANCH} owner-sha256=${digest(fixture.ownership)} -->`;
}

function fakeExternalCommands(fixture, overrides = {}) {
  const state = {
    agentArchived: false,
    agentArchiveDispatches: 0,
    archiveRemovesWorktree: false,
    // The documented order puts the Reviewer leg first, so the delivery
    // fixtures describe a world in which it already ran. A test that exercises
    // the ordering flips this.
    reviewerArchived: true,
    reviewerArchiveDispatches: 0,
    reviewerStatus: "idle",
    reviewerCheckout: null,
    reviewerLabels: null,
    reviewerParentAgentId: null,
    agentListLimit: 200,
    agentListWindowHours: 720,
    extraListedAgents: [],
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
    taskStatuses: {},
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

  function agentRecord(id) {
    if (id === "agent-0001") {
      return {
        Id: "agent-0001",
        Name: TITLE,
        Status: "idle",
        Archived: state.agentArchived,
        ArchivedAt: state.agentArchived ? "2026-09-08T00:00:00Z" : null,
        Cwd: fixture.checkout,
        Worktree: BRANCH,
        ParentAgentId: null,
        Labels: { "director.role": "task-agent", "director.task": TASK },
      };
    }
    if (id === "reviewer-0001") {
      return {
        Id: "reviewer-0001",
        Name: `Review ${TASK} Candidate ${fixture.candidate.slice(0, 8)}`,
        Status: state.reviewerStatus,
        Archived: state.reviewerArchived,
        ArchivedAt: state.reviewerArchived ? "2026-09-08T00:00:00Z" : null,
        Cwd: state.reviewerCheckout ?? fixture.checkout,
        ParentAgentId: state.reviewerParentAgentId,
        Labels: state.reviewerLabels ?? {
          "director.role": "reviewer",
          "director.task": TASK,
          "director.candidate": fixture.candidate,
        },
      };
    }
    return null;
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
      const requested = args[3];
      if (args.includes("comments")) {
        return ok(
          // A comment without an explicit issue belongs to the Task under test,
          // which keeps the delivery fixtures unchanged while the survey can
          // still model several Tasks at once.
          state.beadsComments.filter(
            (comment) => (comment.issue_id ?? TASK) === requested,
          ),
        );
      }
      return ok([
        {
          id: requested,
          title: TITLE,
          issue_type: "task",
          status: state.taskStatuses[requested] ?? state.taskStatus,
          assignee: TASK_ASSIGNEE,
          acceptance_criteria: "The exact coordinator gates are deterministic.",
        },
      ]);
    }
    if (executable === "paseo") {
      if (args[0] === "inspect") {
        const record = agentRecord(args[1]);
        if (record === null) {
          return {
            error: undefined,
            status: 1,
            stdout: "",
            stderr: "PASEO_LIFECYCLE_READ_FAILED\n",
          };
        }
        return ok(record);
      }
      if (args[0] === "agents" && args[1] === "ls") {
        return ok({
          Limit: state.agentListLimit,
          WindowHours: state.agentListWindowHours,
          Agents: [
            agentRecord("agent-0001"),
            agentRecord("reviewer-0001"),
            ...state.extraListedAgents,
          ],
        });
      }
      if (args[0] === "archive") {
        if (args[1] === "reviewer-0001") {
          state.reviewerArchiveDispatches += 1;
          state.reviewerArchived = true;
          return ok({ agentId: "reviewer-0001", status: "archived" });
        }
        state.agentArchiveDispatches += 1;
        state.agentArchived = true;
        return ok({ agentId: "agent-0001", status: "archived" });
      }
      if (args[0] === "workspace" && args[1] === "ls") return ok(state.workspaces);
      if (args[0] === "workspace" && args[1] === "archive") {
        if (args[2] === "reviewer-workspace-0001") {
          state.workspaces = state.workspaces.filter(
            (workspace) => workspace.workspaceId !== args[2],
          );
          return ok({ workspaceId: args[2], status: "archived" });
        }
        if (state.archiveRemovesWorktree && existsSync(fixture.checkout)) {
          git(fixture.control, ["worktree", "remove", "--", fixture.checkout]);
        }
        state.workspaces = state.workspaces.filter(
          (workspace) => workspace.workspaceId !== "workspace-0001",
        );
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

const REVIEWER_AGENT = "reviewer-0001";
const REVIEWER_WORKSPACE = "reviewer-workspace-0001";

// A disposable Reviewer checkout is an independent clone detached at the exact
// Candidate, never a registered worktree of the control repository. Building it
// that way is what makes the leg's owner-marker checks meaningful.
function createReviewerCheckout(fixture, fake, { candidate, name = "reviewer" } = {}) {
  const path = join(fixture.root, name);
  git(fixture.root, ["clone", "--quiet", "--no-hardlinks", fixture.control, path]);
  git(path, ["remote", "set-url", "origin", "https://github.com/acme/director.git"]);
  if (candidate === undefined) {
    git(path, ["checkout", "--quiet", "--detach", fixture.candidate]);
  } else {
    git(path, ["checkout", "--quiet", "--detach", candidate]);
  }
  fake.state.reviewerCheckout = path;
  fake.state.workspaces = [
    ...fake.state.workspaces,
    {
      workspaceId: REVIEWER_WORKSPACE,
      project: "Director",
      name: "Reviewer",
      isolation: "local",
      cwd: path,
    },
  ];
  return path;
}

// A Candidate that exists only inside the Reviewer clone reproduces the real
// backlog: the commit is in no published history and the control repository has
// never seen it.
function createOrphanedReviewerCheckout(fixture, fake, { name = "orphan-reviewer" } = {}) {
  const path = join(fixture.root, name);
  git(fixture.root, ["clone", "--quiet", "--no-hardlinks", fixture.control, path]);
  git(path, ["remote", "set-url", "origin", "https://github.com/acme/director.git"]);
  git(path, ["config", "user.name", "Director Test"]);
  git(path, ["config", "user.email", "director@example.invalid"]);
  git(path, ["checkout", "--quiet", "--detach", fixture.candidate]);
  writeFileSync(join(path, "superseded.txt"), "superseded candidate\n");
  git(path, ["add", "superseded.txt"]);
  git(path, ["commit", "--quiet", "-m", "superseded candidate"]);
  const candidate = git(path, ["rev-parse", "HEAD"]);
  assert.notEqual(
    defaultCommandRunner("git", ["cat-file", "-e", `${candidate}^{commit}`], {
      cwd: fixture.control,
    }).status,
    0,
    "the control repository must never have seen the orphaned Candidate",
  );
  fake.state.reviewerCheckout = path;
  fake.state.reviewerLabels = {
    "director.role": "reviewer",
    "director.task": TASK,
    "director.candidate": candidate,
  };
  fake.state.workspaces = [
    ...fake.state.workspaces,
    {
      workspaceId: REVIEWER_WORKSPACE,
      project: "Director",
      name: "Reviewer",
      isolation: "local",
      cwd: path,
    },
  ];
  return { candidate, path };
}

// Report forms are supplied verbatim so a fixture can build any comment the
// live record actually holds, including forms every anchor must reject. A
// helper that composes the text itself can only ever emit the shape its author
// already believed in, which is why a suite of them could not express the input
// that defeated the previous reader.
function recordComment(fake, { task = TASK, id, author, text }) {
  fake.state.beadsComments = [
    ...fake.state.beadsComments,
    { id, issue_id: task, author, text, created_at: "2026-09-16T00:00:00Z" },
  ];
}

// The four forms this repository is known to hold, each reproduced from a real
// comment on a real Task rather than invented.
const REPORT_FORMS = {
  // dir-m6.30: heading, then Candidate/Base/Verdict lines.
  headingAndLines: ({ candidate, base, verdict }) =>
    `INDEPENDENT REVIEW\nCandidate: ${candidate}\nBase: ${base}\nVerdict: ${verdict}\n`,
  // dir-m6.35: verdict in the heading, Candidate named in prose, no Verdict line.
  headingOnly: ({ candidate, verdict, agentId }) =>
    `INDEPENDENT REVIEW — ${verdict}.\n\nCANDIDATE ${candidate} over base, reviewer paseo:${agentId}.\n`,
  // dir-m6.13: no heading at all; Verdict and Candidate lines only.
  linesOnly: ({ candidate, base, verdict }) =>
    `Verdict: ${verdict}\nCandidate: ${candidate}\nBase: ${base}\n\nIndependent review complete.\n`,
  // dir-m6.20: a heading that is not the recognised one, plus a Candidate line
  // and a verdict that appears only in that heading.
  otherHeading: ({ candidate, base, verdict, agentId }) =>
    `INDEPENDENT EXACT-SHA REVIEW — ${verdict}\n\nReviewer: paseo:${agentId}, parentAgentId null.\nCandidate: ${candidate}\nBase: ${base}\n`,
  // dir-m6.3: transcribed by the coordinator. No heading the reader knows, no
  // Candidate line; the Reviewer and the Candidate appear only in prose.
  transcribed: ({ candidate, base, verdict, agentId }) =>
    `INDEPENDENT_REVIEW_VERDICT — ${TASK} Candidate ${candidate.slice(0, 8)}\n\nReviewer paseo:${agentId} (parentless; detached disposable checkout) returned\nverdict ${verdict} for Candidate ${candidate} over base ${base}.\n`,
  // dir-m6.3's bootstrap record: names the Reviewer and carries an exact
  // Candidate line, and reports nothing. Every anchor must reject it.
  bootstrapRecord: ({ candidate, base, agentId }) =>
    `REVIEWER_BOOTSTRAP_COMPLETE — ${TASK}\n\nReviewer Agent: paseo:${agentId}\nCandidate: ${candidate}\nBase: ${base}\nParentAgentId: null\n`,
};

function recordReviewReport(
  fake,
  {
    form = "headingAndLines",
    task = TASK,
    candidate,
    base,
    verdict = "approve_candidate",
    id = "review-0001",
    agentId = REVIEWER_AGENT,
    author = `paseo:${REVIEWER_AGENT}`,
  },
) {
  recordComment(fake, {
    task,
    id,
    author,
    text: REPORT_FORMS[form]({ candidate, base, verdict, agentId }),
  });
}

// The default recorded report is the Reviewer's own, in the recognised form.
function recordReviewVerdict(fake, options) {
  recordReviewReport(fake, { ...options, form: "headingAndLines" });
}

// The coordinator's routine record announcing that it CREATED a Reviewer. It
// names the Reviewer, names the Candidate it was created for, and quotes an
// earlier Review's verdict in prose. Copied in shape from the live comment that
// bound a Reviewer which had authored nothing. It must bind nothing.
function recordDispatchRecord(
  fake,
  { task = TASK, id = "dispatch-0001", candidate, agentId = REVIEWER_AGENT },
) {
  recordComment(fake, {
    task,
    id,
    author: "paseo:coordinator-0001",
    text: [
      "COORDINATOR — corrected Candidate published to the draft, authoritative CI recorded,",
      "second independent Review started.",
      "",
      `Draft pull request 106 moved to ${candidate}, remote head read back as the Candidate.`,
      "Manifest f0ae3987 binds ONE prior finding — the Review that returned changes_requested —",
      "which is the correction-turn binding working rather than something anyone asserted.",
      "",
      `Reviewer paseo:${agentId}, the first created outside /tmp. Parentage verified where the`,
      "refusal reads it, and labels frozen with the Candidate.",
      "",
    ].join("\n"),
  });
}

// The coordinator's disposition of a verdict. Its FIRST LINE carries the token
// and it names the Reviewer, so it defeats the narrowed fallback and is stopped
// only by authorship. Copied in shape from the live record.
function recordDispositionRecord(
  fake,
  { task = TASK, id = "disposition-0001", agentId = REVIEWER_AGENT },
) {
  recordComment(fake, {
    task,
    id,
    author: "paseo:coordinator-0001",
    text: [
      "COORDINATOR DISPOSITION — second Review returned approve_candidate on the",
      "corrected Candidate. A correction turn is not required.",
      "",
      `Reviewer paseo:${agentId}, parentless, harness v2 attempt 1.`,
      "",
    ].join("\n"),
  });
}

// A note a Reviewer writes about its own Review without concluding it, CARRYING
// A VERDICT TOKEN. The token is the dangerous property: a Reviewer sits idle
// between turns and routinely quotes the previous round's verdict while forming
// its own, so a note without one tests the case that was never in doubt. The
// clause below is verbatim from the coordinator's real dispatch record.
function recordReviewerProgressNote(
  fake,
  { task = TASK, id = "progress-0001", agentId = REVIEWER_AGENT },
) {
  recordComment(fake, {
    task,
    id,
    author: `paseo:${agentId}`,
    text: [
      "PROGRESS — harness attempt 1 complete, still reviewing.",
      "",
      "I am re-deriving the finding from the Review that returned changes_requested",
      "on the previous Candidate before I form my own conclusion. No verdict yet.",
      "",
    ].join("\n"),
  });
}

function recordAuthoredReviewReport(
  fake,
  { task = TASK, verdict = "approve_candidate", id = "review-authored", agentId = REVIEWER_AGENT, candidate = "0".repeat(40) },
) {
  recordReviewReport(fake, {
    task,
    id,
    agentId,
    candidate,
    base: "1".repeat(40),
    verdict,
    form: "headingOnly",
    author: `paseo:${agentId}`,
  });
}

function reviewerOptions(fixture, changes = {}) {
  return {
    ...fixture.options,
    "state-file": join(fixture.root, "reviewer-state.json"),
    "reviewer-agent-id": REVIEWER_AGENT,
    "reviewer-workspace-id": REVIEWER_WORKSPACE,
    "reviewer-lifecycle-state": "active",
    "reviewer-checkout": fixture.reviewerCheckout,
    "reviewer-checkout-state": "present",
    "reviewer-review-state": "verdict_recorded",
    ...changes,
  };
}

async function reviewerPlanAndApply(fixture, fake, changes = {}, dependencies = {}) {
  const options = reviewerOptions(fixture, changes);
  const plan = await execute("reviewer-cleanup-plan", options, { run: fake.runner });
  const planFile = join(fixture.root, `reviewer-plan-${digest(plan).slice(0, 12)}.json`);
  writeFileSync(planFile, `${canonicalJson(plan)}\n`, { mode: 0o600 });
  const applied = await execute(
    "reviewer-cleanup-apply",
    { ...options, "plan-file": planFile },
    { ...dependencies, run: fake.runner },
  );
  return { applied, options, plan, planFile };
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

async function prepareIntegratedCleanup(fixture, fake) {
  await publishReadyFixture(fixture, fake);
  const integratedOptions = {
    ...gateOptions(fixture),
    "state-file": fixture.stateFile,
    "review-file": fixture.reviewFile,
  };
  await execute("integrate", integratedOptions, { run: fake.runner });
  const plan = await execute("cleanup-plan", integratedOptions, {
    run: fake.runner,
  });
  writeFileSync(fixture.planFile, `${canonicalJson(plan)}\n`, { mode: 0o600 });
  return {
    applyOptions: { ...integratedOptions, "plan-file": fixture.planFile },
    integratedOptions,
    plan,
  };
}

// Reproduces the exact shape a coordinator cleanup leaves behind when it is
// interrupted after its own effects advanced the lifecycle: the Task Agent and
// workspace are bound live, cleanup dispatches for real, and the invocation
// stops immediately after the named effect reaches the external world but
// before its result is recorded.
async function interruptLifecycleCleanup(
  fixture,
  fake,
  effect,
  lifecycleState = "active",
) {
  // A handoff manifest recorded active stays usable once the card is restored,
  // so the routing binding is the same for both live lifecycle states.
  rebindReviewRouting(fixture, {
    agentId: "agent-0001",
    lifecycleState: "active",
    workspaceId: "workspace-0001",
  });
  Object.assign(fixture.options, {
    "agent-id": "agent-0001",
    "lifecycle-state": lifecycleState,
    "workspace-id": "workspace-0001",
  });
  const { applyOptions } = await prepareIntegratedCleanup(fixture, fake);
  await assert.rejects(
    execute("cleanup-apply", applyOptions, {
      run: fake.runner,
      hook(phase, currentEffect) {
        if (phase === "after" && currentEffect === effect) {
          throw new CoordinatorInterruption(effect);
        }
      },
    }),
    (error) => error instanceof CoordinatorInterruption,
  );
  return applyOptions;
}

function transitionedOptions(fixture, applyOptions, suffix) {
  return {
    ...applyOptions,
    "checkout-state": "reclaimed",
    "lifecycle-state": "reclaimed",
    "state-file": join(fixture.root, `reclaimed-state-${suffix}.json`),
    "resume-state-file": fixture.stateFile,
  };
}

// The operator procedure: describe the world as it now is. While the owned
// worktree survives, the original binding remains true and its own state
// resumes. Once cleanup removed it, only the transitioned binding is truthful,
// and the interrupted state is supplied as read-only history.
async function resumeUnderTruthfulBinding(fixture, fake, applyOptions, suffix) {
  if (existsSync(fixture.checkout)) {
    return execute("cleanup-apply", applyOptions, { run: fake.runner });
  }
  const options = transitionedOptions(fixture, applyOptions, suffix);
  const planFile = join(fixture.root, `reclaimed-plan-${suffix}.json`);
  const plan = await execute("cleanup-plan", options, { run: fake.runner });
  writeFileSync(planFile, `${canonicalJson(plan)}\n`, { mode: 0o600 });
  return execute(
    "cleanup-apply",
    { ...options, "plan-file": planFile },
    { run: fake.runner },
  );
}

function guardedDeletions(fake, fixture, kind) {
  const ref = `refs/heads/${BRANCH}`;
  return fake.state.calls.filter((call) => {
    if (call.executable !== "git") return false;
    return kind === "remote"
      ? call.args.includes(`--force-with-lease=${ref}:${fixture.candidate}`) &&
          call.args.includes(`:${ref}`)
      : call.args.includes("update-ref") &&
          call.args.includes("-d") &&
          call.args.includes(ref);
  }).length;
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

test("owned pull request enumeration refuses a saturated bounded page", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, {
      historicalPulls: Array.from({ length: 100 }, (_, index) => ({
        number: index + 1,
        body: marker(fixture),
        closed: true,
        draft: false,
        headSha: fixture.candidate,
        merged: false,
        mergeCommit: null,
      })),
    });
    await assert.rejects(
      execute("snapshot", fixture.options, { run: fake.runner }),
      (error) =>
        error instanceof CoordinatorError &&
        error.code === "PULL_REQUESTS_INCOMPLETE",
    );
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

test("PR title, head metadata, and ownership-file paths never reach child argv", async () => {
  const ownershipCanary = `ownership-${randomBytes(32).toString("hex")}`;
  const fixture = createRepositoryFixture({ ownership: ownershipCanary });
  const fake = fakeExternalCommands(fixture);
  const ownershipFile = join(
    fixture.root,
    `ownership-file-${randomBytes(32).toString("hex")}`,
  );
  writeFileSync(ownershipFile, ownershipCanary, { mode: 0o600 });
  const cliArguments = ["snapshot"];
  for (const [key, value] of Object.entries(fixture.options)) {
    if (key !== "ownership") cliArguments.push(`--${key}`, String(value));
  }
  cliArguments.push("--ownership-file", ownershipFile);
  try {
    for (const changes of [
      { title: ownershipCanary },
      { title: ownershipFile },
      { "head-owner": ownershipCanary },
    ]) {
      const parsed = parseCli(cliArguments);
      const options = Object.assign(parsed.options, draftOptions(fixture, changes));
      await assert.rejects(
        execute("publish-draft", options, { run: fake.runner }),
        (error) => {
          assert.ok(error instanceof CoordinatorError);
          assert.equal(error.code, "PROTECTED_MATERIAL_REDACTED");
          const output = JSON.stringify(
            errorOutput(
              "publish-draft",
              error,
              options.ownership,
              options.ownershipFile,
            ),
          );
          assert.equal(output.includes(ownershipCanary), false);
          assert.equal(output.includes(ownershipFile), false);
          return true;
        },
      );
    }
    assert.equal(fake.state.calls.length, 0);
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

test("high-entropy ownership stays hash-only through migration and response-loss cleanup", async () => {
  const ownershipCanary = `ownership-${randomBytes(32).toString("hex")}`;
  const fixture = createRepositoryFixture({ ownership: ownershipCanary });
  const fake = fakeExternalCommands(fixture);
  const ownershipFile = join(fixture.root, "ownership-input");
  const handoffFile = join(fixture.root, "handoff.json");
  const stateLock = `${fixture.stateFile}.lock`;
  try {
    writeFileSync(ownershipFile, ownershipCanary, { mode: 0o600 });
    const cliArguments = [
      fileURLToPath(new URL("./cli.mjs", import.meta.url)),
      "snapshot",
    ];
    for (const [key, value] of Object.entries(fixture.options)) {
      if (key !== "ownership") cliArguments.push(`--${key}`, String(value));
    }
    cliArguments.push("--ownership-file", ownershipFile);
    const cli = spawnSync(process.execPath, cliArguments, { encoding: "utf8" });
    assert.equal(cli.status, 2);
    assert.equal(cliArguments.includes(ownershipCanary), false);
    assert.equal(cli.stdout.includes(ownershipCanary), false);
    assert.equal(cli.stderr.includes(ownershipCanary), false);
    assert.equal(cli.stdout.includes(ownershipFile), false);
    assert.equal(cli.stderr.includes(ownershipFile), false);

    const handoff = await execute(
      "review-handoff",
      {
        ...fixture.options,
        actor: TASK_ASSIGNEE,
        "handoff-file": handoffFile,
        "validation-file": fixture.validationFile,
      },
      { run: fake.runner },
    );
    assert.equal(JSON.stringify(handoff).includes(ownershipCanary), false);
    assert.equal(readFileSync(handoffFile, "utf8").includes(ownershipCanary), false);
    assert.equal(lstatSync(handoffFile).mode & 0o077, 0);

    await publishReadyFixture(fixture, fake);
    const integratedOptions = {
      ...gateOptions(fixture),
      "state-file": fixture.stateFile,
    };
    await execute("integrate", integratedOptions, { run: fake.runner });
    let state = JSON.parse(readFileSync(fixture.stateFile, "utf8"));
    assert.equal(state.schemaVersion, 2);
    assert.equal(state.binding.ownershipTokenHash, digest(ownershipCanary));
    assert.equal(Object.hasOwn(state.binding, "ownership"), false);
    assert.equal(JSON.stringify(state).includes(ownershipCanary), false);

    const legacyBinding = {
      ...state.binding,
      ownership: ownershipCanary,
    };
    delete legacyBinding.ownershipTokenHash;
    state = { ...state, schemaVersion: 1, binding: legacyBinding };
    state.cleanupPlanHash = digest({ legacy: "schema-v1-cleanup-plan" });
    writeFileSync(fixture.stateFile, `${canonicalJson(state)}\n`, { mode: 0o600 });
    writeFileSync(
      stateLock,
      `${canonicalJson({
        schemaVersion: 1,
        pid: 2_147_483_647,
        processStartTime: "0",
        nonce: randomBytes(16).toString("hex"),
        bindingHash: digest(legacyBinding),
      })}\n`,
      { mode: 0o600 },
    );

    const plan = await execute("cleanup-plan", integratedOptions, {
      run: fake.runner,
    });
    const migrated = JSON.parse(readFileSync(fixture.stateFile, "utf8"));
    assert.equal(migrated.schemaVersion, 2);
    assert.equal(migrated.binding.ownershipTokenHash, digest(ownershipCanary));
    assert.equal(Object.hasOwn(migrated.binding, "ownership"), false);
    assert.equal(Object.hasOwn(migrated, "cleanupPlanHash"), false);
    assert.equal(readFileSync(fixture.stateFile, "utf8").includes(ownershipCanary), false);
    assert.equal(existsSync(stateLock), false);
    assert.equal(plan.result.schemaVersion, 2);
    assert.equal(plan.result.binding.ownershipTokenHash, digest(ownershipCanary));
    assert.equal(Object.hasOwn(plan.result.binding, "ownership"), false);
    assert.equal(JSON.stringify(plan).includes(ownershipCanary), false);

    const mutatedPlan = structuredClone(plan);
    delete mutatedPlan.result.planHash;
    mutatedPlan.result.binding.ownership = ownershipCanary;
    mutatedPlan.result.planHash = digest(mutatedPlan.result);
    const mutatedPlanFile = join(fixture.root, "mutated-cleanup-plan.json");
    writeFileSync(mutatedPlanFile, `${canonicalJson(mutatedPlan)}\n`, {
      mode: 0o600,
    });
    await assert.rejects(
      execute(
        "cleanup-apply",
        { ...integratedOptions, "plan-file": mutatedPlanFile },
        { run: fake.runner },
      ),
      (error) =>
        error instanceof CoordinatorError &&
        error.code === "CLEANUP_PLAN_OWNERSHIP_MATERIAL_FORBIDDEN" &&
        !JSON.stringify(errorOutput("cleanup-apply", error, ownershipCanary)).includes(
          ownershipCanary,
        ),
    );

    writeFileSync(fixture.planFile, `${canonicalJson(plan)}\n`, { mode: 0o600 });
    let observedLock = "";
    await assert.rejects(
      execute(
        "cleanup-apply",
        { ...integratedOptions, "plan-file": fixture.planFile },
        {
          run: fake.runner,
          hook(phase, effect) {
            if (phase === "before" && effect === "cleanup.worktree") {
              observedLock = readFileSync(stateLock, "utf8");
            }
            if (phase === "after" && effect === "cleanup.remote-branch") {
              throw new CoordinatorInterruption(effect);
            }
          },
        },
      ),
      (error) => error instanceof CoordinatorInterruption,
    );
    assert.notEqual(observedLock, "");
    assert.equal(observedLock.includes(ownershipCanary), false);
    assert.equal(existsSync(stateLock), false);
    assert.equal(readFileSync(fixture.stateFile, "utf8").includes(ownershipCanary), false);

    const applied = await execute(
      "cleanup-apply",
      { ...integratedOptions, "plan-file": fixture.planFile },
      { run: fake.runner },
    );
    assert.equal(applied.result.resources, "complete");
    const appliedState = JSON.parse(readFileSync(fixture.stateFile, "utf8"));
    assert.equal(appliedState.cleanupPlanHash, plan.result.planHash);
    assert.equal(JSON.stringify(applied).includes(ownershipCanary), false);
    assert.equal(fake.state.pull.body.includes(ownershipCanary), false);
    assert.match(fake.state.pull.body, /owner-sha256=[0-9a-f]{64}/u);
    assert.equal(
      fake.state.calls.some((call) =>
        JSON.stringify(call).includes(ownershipCanary)),
      false,
    );
    assert.equal(
      fake.state.calls
        .filter((call) => call.executable === "bd")
        .some((call) => JSON.stringify(call).includes(ownershipCanary)),
      false,
    );
    for (const artifact of [
      fixture.stateFile,
      fixture.planFile,
      fixture.manifestFile,
      handoffFile,
    ]) {
      const contents = readFileSync(artifact, "utf8");
      assert.equal(contents.includes(ownershipCanary), false);
      assert.equal(contents.includes(ownershipFile), false);
    }
  } finally {
    fixture.cleanup();
  }
});

test("adversarial ownership copies are rejected with bounded redacted codes", async () => {
  const ownershipCanary = `ownership-${randomBytes(32).toString("hex")}`;
  const fixture = createRepositoryFixture({ ownership: ownershipCanary });
  const fake = fakeExternalCommands(fixture);
  try {
    writeFileSync(fixture.bodyFile, `## Summary\n\n${ownershipCanary}\n`);
    await assert.rejects(
      execute("publish-draft", draftOptions(fixture), { run: fake.runner }),
      (error) =>
        error instanceof CoordinatorError &&
        error.code === "PROTECTED_MATERIAL_REDACTED" &&
        !JSON.stringify(errorOutput("publish-draft", error, ownershipCanary)).includes(
          ownershipCanary,
        ),
    );
    assert.equal(fake.state.calls.length, 0);
    writeFileSync(fixture.bodyFile, "## Summary\n\nCoordinator fixture.\n");

    const mutatedManifest = JSON.parse(
      readFileSync(fixture.manifestFile, "utf8"),
    );
    delete mutatedManifest.manifestHash;
    mutatedManifest.ownership.ownership = ownershipCanary;
    mutatedManifest.manifestHash = digest(mutatedManifest);
    const mutatedManifestFile = join(fixture.root, "mutated-manifest.json");
    writeFileSync(mutatedManifestFile, `${canonicalJson(mutatedManifest)}\n`, {
      mode: 0o600,
    });
    await assert.rejects(
      execute(
        "publish-draft",
        draftOptions(fixture, { "manifest-file": mutatedManifestFile }),
        { run: fake.runner },
      ),
      (error) =>
        error instanceof CoordinatorError &&
        error.code === "REVIEW_MANIFEST_OWNERSHIP_MATERIAL_FORBIDDEN" &&
        !JSON.stringify(errorOutput("publish-draft", error, ownershipCanary)).includes(
          ownershipCanary,
        ),
    );
    assert.equal(fake.state.calls.length, 0);

    await execute("publish-draft", draftOptions(fixture), {
      run: fake.runner,
    });
    const validState = JSON.parse(
      readFileSync(fixture.draftStateFile, "utf8"),
    );
    const mutatedState = structuredClone(validState);
    mutatedState.binding.ownership = ownershipCanary;
    writeFileSync(
      fixture.draftStateFile,
      `${canonicalJson(mutatedState)}\n`,
      { mode: 0o600 },
    );
    await assert.rejects(
      execute(
        "publish-draft",
        draftOptions(fixture, {
          "expected-remote-head": fixture.candidate,
        }),
        { run: fake.runner },
      ),
      (error) =>
        error instanceof CoordinatorError &&
        error.code === "STATE_OWNERSHIP_MATERIAL_FORBIDDEN" &&
        !JSON.stringify(errorOutput("publish-draft", error, ownershipCanary)).includes(
          ownershipCanary,
        ),
    );

    const unsafeLegacy = structuredClone(validState);
    unsafeLegacy.schemaVersion = 1;
    unsafeLegacy.binding.ownership = ownershipCanary;
    delete unsafeLegacy.binding.ownershipTokenHash;
    unsafeLegacy.effects["adversarial.copy"] = {
      class: "store_only",
      phase: "complete",
      attempts: 0,
      evidence: { value: ownershipCanary },
    };
    writeFileSync(
      fixture.draftStateFile,
      `${canonicalJson(unsafeLegacy)}\n`,
      { mode: 0o600 },
    );
    await assert.rejects(
      execute(
        "publish-draft",
        draftOptions(fixture, {
          "expected-remote-head": fixture.candidate,
        }),
        { run: fake.runner },
      ),
      (error) =>
        error instanceof CoordinatorError &&
        error.code === "STATE_LEGACY_OWNERSHIP_UNSAFE" &&
        !JSON.stringify(errorOutput("publish-draft", error, ownershipCanary)).includes(
          ownershipCanary,
        ),
    );

    const refusedDiagnostic = errorOutput(
      "cleanup-apply",
      new CoordinatorError(
        "ADVERSARIAL_DIAGNOSTIC",
        `protected ${ownershipCanary}`,
        { value: ownershipCanary },
      ),
      ownershipCanary,
    );
    assert.equal(refusedDiagnostic.code, "PROTECTED_MATERIAL_REDACTED");
    assert.equal(JSON.stringify(refusedDiagnostic).includes(ownershipCanary), false);
    const interruptedDiagnostic = errorOutput(
      "cleanup-apply",
      new CoordinatorInterruption(ownershipCanary),
      ownershipCanary,
    );
    assert.equal(interruptedDiagnostic.code, "PROTECTED_MATERIAL_REDACTED");
    assert.equal(
      JSON.stringify(interruptedDiagnostic).includes(ownershipCanary),
      false,
    );
  } finally {
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

test("cleanup retries the preserved M5.14 schema-v2 frontier after immediate TLS observation failure", async () => {
  const ownershipCanary = `ownership-${randomBytes(32).toString("hex")}`;
  const fixture = createRepositoryFixture({ ownership: ownershipCanary });
  const fake = fakeExternalCommands(fixture);
  try {
    const { applyOptions, plan } = await prepareIntegratedCleanup(fixture, fake);
    const ref = `refs/heads/${BRANCH}`;
    let injectedFailure = false;
    let observedError;
    const failImmediateRemoteObservation = (executable, args, options = {}) => {
      if (
        !injectedFailure &&
        executable === "git" &&
        args.includes("ls-remote") &&
        args.at(-1) === ref &&
        existsSync(fixture.stateFile) &&
        JSON.parse(readFileSync(fixture.stateFile, "utf8")).effects[
          "cleanup.remote-branch"
        ]?.phase === "dispatching"
      ) {
        injectedFailure = true;
        return {
          error: undefined,
          status: 128,
          stdout: "",
          stderr: `fatal: unable to access remote: GnuTLS recv error (-110): ${"connection ended ".repeat(100)}\n`,
        };
      }
      return fake.runner(executable, args, options);
    };

    try {
      await execute("cleanup-apply", applyOptions, {
        run: failImmediateRemoteObservation,
      });
    } catch (error) {
      observedError = error;
    }
    assert.equal(injectedFailure, true);
    assert.equal(observedError instanceof CoordinatorError, true);
    assert.equal(observedError.code, "REMOTE_OBSERVATION_UNAVAILABLE");
    assert.ok(observedError.details.diagnostic.length <= 800);
    const bounded = errorOutput(
      "cleanup-apply",
      observedError,
      ownershipCanary,
    );
    assert.equal(JSON.stringify(bounded).includes(ownershipCanary), false);
    assert.ok(JSON.stringify(bounded).length < 1_200);

    const frontier = JSON.parse(readFileSync(fixture.stateFile, "utf8"));
    assert.equal(frontier.schemaVersion, 2);
    assert.equal(frontier.effects["cleanup.worktree"].phase, "complete");
    assert.equal(frontier.effects["cleanup.remote-branch"].phase, "dispatching");
    assert.equal(frontier.effects["cleanup.remote-branch"].attempts, 1);
    assert.equal(JSON.stringify(frontier).includes(ownershipCanary), false);
    assert.equal(plan.result.schemaVersion, 2);
    assert.equal(JSON.stringify(plan).includes(ownershipCanary), false);
    assert.equal(
      git(fixture.control, ["ls-remote", "--heads", "origin", ref]).split("\t")[0],
      fixture.candidate,
    );
    assert.equal(
      git(fixture.control, ["rev-parse", "--verify", ref]),
      fixture.candidate,
    );

    const recovered = await execute("cleanup-apply", applyOptions, {
      run: fake.runner,
    });
    assert.equal(recovered.result.resources, "complete");
    const completed = JSON.parse(readFileSync(fixture.stateFile, "utf8"));
    assert.equal(completed.effects["cleanup.remote-branch"].phase, "complete");
    assert.equal(completed.effects["cleanup.remote-branch"].attempts, 2);
    assert.equal(completed.effects["cleanup.local-branch"].phase, "complete");
    assert.equal(JSON.stringify(completed).includes(ownershipCanary), false);
    const remoteDeletes = fake.state.calls.filter(
      (call) =>
        call.executable === "git" &&
        call.args.includes("push") &&
        call.args.includes(`--force-with-lease=${ref}:${fixture.candidate}`) &&
        call.args.includes(`:${ref}`),
    );
    const localDeletes = fake.state.calls.filter(
      (call) =>
        call.executable === "git" &&
        call.args.includes("update-ref") &&
        call.args.includes("-d") &&
        call.args.includes(ref) &&
        call.args.includes(fixture.candidate),
    );
    assert.equal(remoteDeletes.length, 1);
    assert.equal(localDeletes.length, 1);
    assert.equal(
      fake.state.calls.some((call) => JSON.stringify(call).includes(ownershipCanary)),
      false,
    );
  } finally {
    fixture.cleanup();
  }
});

test("cleanup observation failure before a ref intent performs no mutation and remains retryable", async () => {
  const fixture = createRepositoryFixture();
  const fake = fakeExternalCommands(fixture);
  try {
    const { applyOptions } = await prepareIntegratedCleanup(fixture, fake);
    const ref = `refs/heads/${BRANCH}`;
    let failed = false;
    const unavailableBeforeIntent = (executable, args, options = {}) => {
      if (
        !failed &&
        executable === "git" &&
        args.includes("ls-remote") &&
        args.at(-1) === ref
      ) {
        failed = true;
        return {
          error: undefined,
          status: 128,
          stdout: "",
          stderr: "fatal: GnuTLS remote observation unavailable\n",
        };
      }
      return fake.runner(executable, args, options);
    };
    await assert.rejects(
      execute("cleanup-apply", applyOptions, { run: unavailableBeforeIntent }),
      (error) =>
        error instanceof CoordinatorError &&
        error.code === "REMOTE_OBSERVATION_UNAVAILABLE",
    );
    const state = JSON.parse(readFileSync(fixture.stateFile, "utf8"));
    assert.equal(state.effects["cleanup.remote-branch"], undefined);
    assert.equal(existsSync(fixture.checkout), true);
    assert.equal(
      fake.state.calls.some(
        (call) => call.executable === "git" && call.args.includes(`:${ref}`),
      ),
      false,
    );
    const recovered = await execute("cleanup-apply", applyOptions, {
      run: fake.runner,
    });
    assert.equal(recovered.result.resources, "complete");
  } finally {
    fixture.cleanup();
  }
});

test("exact-leased remote and local ref cleanup retries after interruption before mutation", async (context) => {
  for (const kind of ["remote", "local"]) {
    await context.test(kind, async () => {
      const fixture = createRepositoryFixture();
      const fake = fakeExternalCommands(fixture);
      try {
        const { applyOptions } = await prepareIntegratedCleanup(fixture, fake);
        const effect = `cleanup.${kind}-branch`;
        const ref = `refs/heads/${BRANCH}`;
        await assert.rejects(
          execute("cleanup-apply", applyOptions, {
            run: fake.runner,
            hook(phase, currentEffect) {
              if (phase === "before" && currentEffect === effect) {
                throw new CoordinatorInterruption(effect);
              }
            },
          }),
          (error) => error instanceof CoordinatorInterruption,
        );
        const interrupted = JSON.parse(readFileSync(fixture.stateFile, "utf8"));
        assert.equal(interrupted.effects[effect].phase, "dispatching");
        const present =
          kind === "remote"
            ? git(fixture.control, ["ls-remote", "--heads", "origin", ref]).split("\t")[0]
            : git(fixture.control, ["rev-parse", "--verify", ref]);
        assert.equal(present, fixture.candidate);

        const recovered = await execute("cleanup-apply", applyOptions, {
          run: fake.runner,
        });
        assert.equal(recovered.result.resources, "complete");
        const completed = JSON.parse(readFileSync(fixture.stateFile, "utf8"));
        assert.equal(completed.effects[effect].phase, "complete");
        assert.equal(completed.effects[effect].attempts, 2);
        const guardedCalls = fake.state.calls.filter((call) => {
          if (call.executable !== "git") return false;
          return kind === "remote"
            ? call.args.includes(`--force-with-lease=${ref}:${fixture.candidate}`) &&
                call.args.includes(`:${ref}`)
            : call.args.includes("update-ref") &&
                call.args.includes("-d") &&
                call.args.includes(ref) &&
                call.args.includes(fixture.candidate);
        });
        assert.equal(guardedCalls.length, 1);
      } finally {
        fixture.cleanup();
      }
    });
  }
});

test("unknown remote and local ref deletion retries only through the exact guard", async (context) => {
  for (const kind of ["remote", "local"]) {
    await context.test(kind, async () => {
      const fixture = createRepositoryFixture();
      const fake = fakeExternalCommands(fixture);
      try {
        const { applyOptions } = await prepareIntegratedCleanup(fixture, fake);
        const ref = `refs/heads/${BRANCH}`;
        let withheldMutation = false;
        const unknownResultRunner = (executable, args, options = {}) => {
          const isRemoteDelete =
            kind === "remote" &&
            executable === "git" &&
            args.includes(`--force-with-lease=${ref}:${fixture.candidate}`) &&
            args.includes(`:${ref}`);
          const isLocalDelete =
            kind === "local" &&
            executable === "git" &&
            args.includes("update-ref") &&
            args.includes("-d") &&
            args.includes(ref) &&
            args.includes(fixture.candidate);
          if (!withheldMutation && (isRemoteDelete || isLocalDelete)) {
            withheldMutation = true;
            return { error: undefined, status: 0, stdout: "", stderr: "" };
          }
          return fake.runner(executable, args, options);
        };
        await assert.rejects(
          execute("cleanup-apply", applyOptions, {
            run: unknownResultRunner,
          }),
          (error) =>
            error instanceof CoordinatorError &&
            error.code === "DESTRUCTIVE_RESULT_UNKNOWN",
        );
        assert.equal(withheldMutation, true);
        const unknown = JSON.parse(readFileSync(fixture.stateFile, "utf8"));
        assert.equal(unknown.effects[`cleanup.${kind}-branch`].phase, "unknown");

        const recovered = await execute("cleanup-apply", applyOptions, {
          run: fake.runner,
        });
        assert.equal(recovered.result.resources, "complete");
        const completed = JSON.parse(readFileSync(fixture.stateFile, "utf8"));
        assert.equal(completed.effects[`cleanup.${kind}-branch`].phase, "complete");
        assert.equal(completed.effects[`cleanup.${kind}-branch`].attempts, 2);
        const guardedCalls = fake.state.calls.filter((call) => {
          if (call.executable !== "git") return false;
          return kind === "remote"
            ? call.args.includes(`--force-with-lease=${ref}:${fixture.candidate}`) &&
                call.args.includes(`:${ref}`)
            : call.args.includes("update-ref") &&
                call.args.includes("-d") &&
                call.args.includes(ref) &&
                call.args.includes(fixture.candidate);
        });
        assert.equal(guardedCalls.length, 1);
      } finally {
        fixture.cleanup();
      }
    });
  }
});

test("local ref observation failure preserves the dispatching frontier for restart", async () => {
  const fixture = createRepositoryFixture();
  const fake = fakeExternalCommands(fixture);
  try {
    const { applyOptions } = await prepareIntegratedCleanup(fixture, fake);
    const ref = `refs/heads/${BRANCH}`;
    let failed = false;
    const failImmediateLocalObservation = (executable, args, options = {}) => {
      if (
        !failed &&
        executable === "git" &&
        args.includes("rev-parse") &&
        args.includes("--quiet") &&
        args.at(-1) === ref &&
        JSON.parse(readFileSync(fixture.stateFile, "utf8")).effects[
          "cleanup.local-branch"
        ]?.phase === "dispatching"
      ) {
        failed = true;
        return {
          error: undefined,
          status: 128,
          stdout: "",
          stderr: "fatal: local ref observation unavailable\n",
        };
      }
      return fake.runner(executable, args, options);
    };
    await assert.rejects(
      execute("cleanup-apply", applyOptions, {
        run: failImmediateLocalObservation,
      }),
      (error) =>
        error instanceof CoordinatorError && error.code === "LOCAL_REF_UNAVAILABLE",
    );
    const frontier = JSON.parse(readFileSync(fixture.stateFile, "utf8"));
    assert.equal(frontier.effects["cleanup.local-branch"].phase, "dispatching");
    assert.equal(git(fixture.control, ["rev-parse", "--verify", ref]), fixture.candidate);
    const recovered = await execute("cleanup-apply", applyOptions, {
      run: fake.runner,
    });
    assert.equal(recovered.result.resources, "complete");
    const completed = JSON.parse(readFileSync(fixture.stateFile, "utf8"));
    assert.equal(completed.effects["cleanup.local-branch"].attempts, 2);
  } finally {
    fixture.cleanup();
  }
});

test("remote and local ref cleanup adopts absence after mutation response loss", async (context) => {
  for (const kind of ["remote", "local"]) {
    await context.test(kind, async () => {
      const fixture = createRepositoryFixture();
      const fake = fakeExternalCommands(fixture);
      try {
        const { applyOptions } = await prepareIntegratedCleanup(fixture, fake);
        const effect = `cleanup.${kind}-branch`;
        const ref = `refs/heads/${BRANCH}`;
        await assert.rejects(
          execute("cleanup-apply", applyOptions, {
            run: fake.runner,
            hook(phase, currentEffect) {
              if (phase === "after" && currentEffect === effect) {
                throw new CoordinatorInterruption(effect);
              }
            },
          }),
          (error) => error instanceof CoordinatorInterruption,
        );
        const interrupted = JSON.parse(readFileSync(fixture.stateFile, "utf8"));
        assert.equal(interrupted.effects[effect].phase, "dispatching");

        const recovered = await execute("cleanup-apply", applyOptions, {
          run: fake.runner,
        });
        assert.equal(recovered.result.resources, "complete");
        const guardedCalls = fake.state.calls.filter((call) => {
          if (call.executable !== "git") return false;
          return kind === "remote"
            ? call.args.includes(`--force-with-lease=${ref}:${fixture.candidate}`) &&
                call.args.includes(`:${ref}`)
            : call.args.includes("update-ref") &&
                call.args.includes("-d") &&
                call.args.includes(ref) &&
                call.args.includes(fixture.candidate);
        });
        assert.equal(guardedCalls.length, 1);
      } finally {
        fixture.cleanup();
      }
    });
  }
});

test("completed remote and local ref deletion remains terminal after reappearance", async (context) => {
  for (const kind of ["remote", "local"]) {
    await context.test(kind, async () => {
      const fixture = createRepositoryFixture();
      const fake = fakeExternalCommands(fixture);
      try {
        const { applyOptions } = await prepareIntegratedCleanup(fixture, fake);
        await execute("cleanup-apply", applyOptions, { run: fake.runner });
        const ref = `refs/heads/${BRANCH}`;
        if (kind === "remote") {
          git(fixture.control, [
            "push",
            "--quiet",
            "origin",
            `${fixture.candidate}:${ref}`,
          ]);
        } else {
          git(fixture.control, [
            "update-ref",
            ref,
            fixture.candidate,
            "0000000000000000000000000000000000000000",
          ]);
        }
        await assert.rejects(
          execute("cleanup-apply", applyOptions, { run: fake.runner }),
          (error) =>
            error instanceof CoordinatorError &&
            error.code === "DESTRUCTIVE_TARGET_PRESENT_AFTER_HANDOFF",
        );
        const current =
          kind === "remote"
            ? git(fixture.control, ["ls-remote", "--heads", "origin", ref]).split("\t")[0]
            : git(fixture.control, ["rev-parse", "--verify", ref]);
        assert.equal(current, fixture.candidate);
      } finally {
        fixture.cleanup();
      }
    });
  }
});

test("post-handoff commands operate from exact refs after the Task checkout and parent are reclaimed", async () => {
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
    rmSync(fixture.worktreeRoot, { recursive: true, force: true });
    assert.equal(existsSync(fixture.worktreeRoot), false);
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
      "review-file": fixture.reviewFile,
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
    // A reclaimed binding is a statement about the Task Agent's own resources,
    // so nothing asks the daemon about them again. The Reviewer is a separate
    // parentless agent whose liveness the schedule never reclaimed, so ordering
    // the Reviewer leg first costs each cleanup command exactly one read of the
    // Review's own Reviewer and one bounded page for the rest of this Task's
    // Reviewers. Neither names the Task Agent or the Task workspace.
    const afterReclaim = fake.state.calls
      .filter((call) => call.executable === "paseo")
      .slice(paseoCallsBeforeReclaim);
    assert.deepEqual(
      afterReclaim.map((call) => call.args),
      [
        ["inspect", "reviewer-0001", "--json"],
        ["agents", "ls", "--json"],
        ["inspect", "reviewer-0001", "--json"],
        ["agents", "ls", "--json"],
      ],
    );
  } finally {
    fixture.cleanup();
  }
});

test("an interrupted cleanup resumes truthfully across its own lifecycle transition", async () => {
  const fixture = createRepositoryFixture();
  const fake = fakeExternalCommands(fixture, { archiveRemovesWorktree: true });
  try {
    const applyOptions = await interruptLifecycleCleanup(
      fixture,
      fake,
      "cleanup.remote-branch",
    );
    const interruptedBytes = readFileSync(fixture.stateFile, "utf8");
    const interrupted = JSON.parse(interruptedBytes);
    assert.equal(interrupted.effects["cleanup.agent"].phase, "complete");
    assert.equal(interrupted.effects["cleanup.workspace"].phase, "complete");
    assert.equal(interrupted.effects["cleanup.worktree"].phase, "complete");
    assert.equal(
      interrupted.effects["cleanup.remote-branch"].phase,
      "dispatching",
    );
    assert.equal(interrupted.effects["cleanup.local-branch"], undefined);
    assert.equal(existsSync(fixture.checkout), false);
    assert.equal(
      git(fixture.control, ["ls-remote", "--heads", "origin", `refs/heads/${BRANCH}`]),
      "",
    );

    // The half-truthful binding the transitioned world first suggests.
    await assert.rejects(
      execute(
        "cleanup-apply",
        { ...applyOptions, "checkout-state": "reclaimed" },
        { run: fake.runner },
      ),
      (error) => error.code === "LIFECYCLE_CHECKOUT_STATE_MISMATCH",
    );

    // The truthful binding cannot reuse the interrupted state. Its lock holds
    // the binding hash the cleanup invalidated, and the state itself is
    // permanently bound to the description that is no longer assertable.
    const lockPath = `${fixture.stateFile}.lock`;
    writeFileSync(
      lockPath,
      `${canonicalJson({
        schemaVersion: 1,
        pid: process.pid,
        processStartTime: "1",
        nonce: "0".repeat(32),
        bindingHash: digest(interrupted.binding),
      })}\n`,
      { mode: 0o600 },
    );
    const truthful = {
      ...applyOptions,
      "checkout-state": "reclaimed",
      "lifecycle-state": "reclaimed",
    };
    await assert.rejects(
      execute("cleanup-plan", truthful, { run: fake.runner }),
      (error) => error.code === "STATE_LOCK_INVALID",
    );
    rmSync(lockPath);
    await assert.rejects(
      execute("cleanup-plan", truthful, { run: fake.runner }),
      (error) => error.code === "STATE_BINDING_MISMATCH",
    );

    // A fresh state bound truthfully integrates and plans, then correctly
    // refuses an absence that state cannot explain.
    const reclaimedState = join(fixture.root, "reclaimed-state.json");
    const reclaimedPlan = join(fixture.root, "reclaimed-plan.json");
    const fresh = { ...truthful, "state-file": reclaimedState };
    await execute("integrate", fresh, { run: fake.runner });
    const blind = await execute("cleanup-plan", fresh, { run: fake.runner });
    assert.equal(blind.result.resources.remoteBranch, null);
    assert.equal(blind.result.resources.localBranch, fixture.candidate);
    writeFileSync(reclaimedPlan, `${canonicalJson(blind)}\n`, { mode: 0o600 });
    await assert.rejects(
      execute(
        "cleanup-apply",
        { ...fresh, "plan-file": reclaimedPlan },
        { run: fake.runner },
      ),
      (error) => error.code === "DESTRUCTIVE_ABSENCE_AMBIGUOUS",
    );

    // Carrying the recorded intent across the transition releases it.
    const resume = { ...fresh, "resume-state-file": fixture.stateFile };
    const replanned = await execute("cleanup-plan", resume, { run: fake.runner });
    assert.equal(replanned.result.planHash, blind.result.planHash);
    const applied = await execute(
      "cleanup-apply",
      { ...resume, "plan-file": reclaimedPlan },
      { run: fake.runner },
    );
    assert.equal(applied.result.resources, "complete");

    const resumed = JSON.parse(readFileSync(reclaimedState, "utf8"));
    assert.equal(resumed.effects["cleanup.remote-branch"].phase, "complete");
    assert.equal(resumed.effects["cleanup.remote-branch"].evidence.absent, true);
    assert.equal(resumed.effects["cleanup.local-branch"].phase, "complete");
    assert.equal(resumed.continuation.priorCheckoutState, "present");
    assert.equal(resumed.continuation.priorLifecycleState, "active");
    assert.equal(
      resumed.continuation.priorBindingHash,
      digest(interrupted.binding),
    );
    // The refused run had already recorded its own reclaimed adoptions, so the
    // single fact the continuation had to supply is the recorded deletion
    // intent the transitioned binding could no longer reach.
    assert.deepEqual(
      resumed.continuation.adoptedEffects.filter((name) =>
        name.startsWith("cleanup."),
      ),
      ["cleanup.remote-branch"],
    );
    assert.equal(
      resumed.continuation.adoptedEffects.includes("integrate.merge"),
      false,
    );
    for (const name of ["cleanup.agent", "cleanup.workspace", "cleanup.worktree"]) {
      assert.equal(resumed.effects[name].phase, "complete", name);
      assert.equal(resumed.effects[name].evidence.source, "recorded_reclaimed");
    }

    // Nothing was executed twice and the interrupted state was only read.
    assert.equal(guardedDeletions(fake, fixture, "remote"), 1);
    assert.equal(guardedDeletions(fake, fixture, "local"), 1);
    assert.equal(fake.state.agentArchiveDispatches, 1);
    assert.equal(
      git(fixture.control, [
        "for-each-ref",
        "--format=%(refname)",
        `refs/heads/${BRANCH}`,
      ]),
      "",
    );
    assert.equal(readFileSync(fixture.stateFile, "utf8"), interruptedBytes);
  } finally {
    fixture.cleanup();
  }
});

test("cleanup resumes after an interruption at every step without duplicate effects", async (context) => {
  for (const [effect, archiveRemovesWorktree] of [
    ["cleanup.agent", true],
    ["cleanup.workspace", true],
    ["cleanup.worktree", false],
    ["cleanup.remote-branch", true],
    ["cleanup.local-branch", true],
  ]) {
    await context.test(effect, async () => {
      const fixture = createRepositoryFixture();
      const fake = fakeExternalCommands(fixture, { archiveRemovesWorktree });
      try {
        const applyOptions = await interruptLifecycleCleanup(
          fixture,
          fake,
          effect,
        );
        const interruptedBytes = readFileSync(fixture.stateFile, "utf8");
        assert.equal(
          JSON.parse(interruptedBytes).effects[effect].phase,
          "dispatching",
        );
        const resumed = await resumeUnderTruthfulBinding(
          fixture,
          fake,
          applyOptions,
          "step",
        );
        assert.equal(resumed.result.resources, "complete");
        assert.equal(fake.state.agentArchiveDispatches, 1);
        assert.equal(
          fake.state.calls.filter(
            (call) =>
              call.executable === "paseo" &&
              call.args[0] === "workspace" &&
              call.args[1] === "archive",
          ).length,
          1,
        );
        assert.equal(guardedDeletions(fake, fixture, "remote"), 1);
        assert.equal(guardedDeletions(fake, fixture, "local"), 1);
        assert.equal(
          git(fixture.control, ["ls-remote", "--heads", "origin", `refs/heads/${BRANCH}`]),
          "",
        );
        assert.equal(existsSync(fixture.checkout), false);

        const finalState = JSON.parse(
          readFileSync(
            existsSync(join(fixture.root, "reclaimed-state-step.json"))
              ? join(fixture.root, "reclaimed-state-step.json")
              : fixture.stateFile,
            "utf8",
          ),
        );
        for (const name of [
          "cleanup.agent",
          "cleanup.workspace",
          "cleanup.worktree",
          "cleanup.remote-branch",
          "cleanup.local-branch",
        ]) {
          assert.equal(finalState.effects[name].phase, "complete", name);
        }
        if (finalState.continuation !== undefined) {
          assert.equal(readFileSync(fixture.stateFile, "utf8"), interruptedBytes);
        }
      } finally {
        fixture.cleanup();
      }
    });
  }
});

test("resumption advances the lifecycle only to the value cleanup produces", async () => {
  const fixture = createRepositoryFixture();
  const fake = fakeExternalCommands(fixture);
  try {
    // The workspace archive landed; the worktree it did not remove survives,
    // so the interrupted binding is still present/active.
    const applyOptions = await interruptLifecycleCleanup(
      fixture,
      fake,
      "cleanup.workspace",
    );
    const interruptedBytes = readFileSync(fixture.stateFile, "utf8");
    assert.equal(
      JSON.parse(interruptedBytes).effects["cleanup.workspace"].phase,
      "dispatching",
    );
    assert.equal(existsSync(fixture.checkout), true);

    // restored is a recovery fact, not cleanup progress. cleanup-apply has no
    // restored effect path, so admitting it as a target would carry an archive
    // intent nothing can finish while still reporting the cleanup complete.
    const restored = {
      ...applyOptions,
      "checkout-state": "present",
      "lifecycle-state": "restored",
      "state-file": join(fixture.root, "restored-state.json"),
      "resume-state-file": fixture.stateFile,
    };
    await assert.rejects(
      execute("cleanup-plan", restored, { run: fake.runner }),
      (error) => error.code === "RESUME_STATE_LIFECYCLE_NOT_RECLAIMED",
    );
    // No plan can be admitted for that target either, because a plan is bound
    // to the lifecycle binding that produced it, so cleanup-apply cannot reach
    // the stranded state by reusing the interrupted run's plan.
    await assert.rejects(
      execute("cleanup-apply", restored, { run: fake.runner }),
      (error) => error.code === "CLEANUP_PLAN_BINDING_MISMATCH",
    );
    assert.equal(existsSync(restored["state-file"]), false);
    assert.equal(readFileSync(fixture.stateFile, "utf8"), interruptedBytes);
  } finally {
    fixture.cleanup();
  }
});

test("a cleanup interrupted under a restored lifecycle resumes and terminalizes", async () => {
  const fixture = createRepositoryFixture();
  // A restored card is absent from the workspace listing and cannot be
  // archived by identity, so cleanup removes the worktree and the refs only.
  const fake = fakeExternalCommands(fixture, { workspaces: [] });
  try {
    const applyOptions = await interruptLifecycleCleanup(
      fixture,
      fake,
      "cleanup.worktree",
      "restored",
    );
    const interruptedBytes = readFileSync(fixture.stateFile, "utf8");
    const interrupted = JSON.parse(interruptedBytes);
    assert.equal(interrupted.binding.lifecycleState, "restored");
    assert.equal(interrupted.effects["cleanup.worktree"].phase, "dispatching");
    assert.equal(existsSync(fixture.checkout), false);

    const resumed = await resumeUnderTruthfulBinding(
      fixture,
      fake,
      applyOptions,
      "restored",
    );
    assert.equal(resumed.result.resources, "complete");
    const final = JSON.parse(
      readFileSync(join(fixture.root, "reclaimed-state-restored.json"), "utf8"),
    );
    assert.equal(final.continuation.priorLifecycleState, "restored");
    for (const name of [
      "cleanup.agent",
      "cleanup.workspace",
      "cleanup.worktree",
      "cleanup.remote-branch",
      "cleanup.local-branch",
    ]) {
      assert.equal(final.effects[name].phase, "complete", name);
    }
    assert.equal(guardedDeletions(fake, fixture, "remote"), 1);
    assert.equal(guardedDeletions(fake, fixture, "local"), 1);
    assert.equal(
      fake.state.calls.filter(
        (call) =>
          call.executable === "paseo" &&
          call.args[0] === "workspace" &&
          call.args[1] === "archive",
      ).length,
      0,
    );
    assert.equal(readFileSync(fixture.stateFile, "utf8"), interruptedBytes);
  } finally {
    fixture.cleanup();
  }
});

test("a resumed cleanup still refuses a destructive absence nothing recorded", async () => {
  const fixture = createRepositoryFixture();
  const fake = fakeExternalCommands(fixture, { archiveRemovesWorktree: true });
  try {
    const applyOptions = await interruptLifecycleCleanup(
      fixture,
      fake,
      "cleanup.workspace",
    );
    const interrupted = JSON.parse(readFileSync(fixture.stateFile, "utf8"));
    assert.equal(interrupted.effects["cleanup.remote-branch"], undefined);
    // An unrelated deletion this coordinator never recorded anywhere.
    git(fixture.control, ["push", "--quiet", "origin", `:refs/heads/${BRANCH}`]);

    const options = transitionedOptions(fixture, applyOptions, "absence");
    const planFile = join(fixture.root, "reclaimed-plan-absence.json");
    const plan = await execute("cleanup-plan", options, { run: fake.runner });
    writeFileSync(planFile, `${canonicalJson(plan)}\n`, { mode: 0o600 });
    await assert.rejects(
      execute(
        "cleanup-apply",
        { ...options, "plan-file": planFile },
        { run: fake.runner },
      ),
      (error) => error.code === "DESTRUCTIVE_ABSENCE_AMBIGUOUS",
    );
    assert.equal(
      git(fixture.control, ["rev-parse", "--verify", `refs/heads/${BRANCH}`]),
      fixture.candidate,
    );
    assert.equal(guardedDeletions(fake, fixture, "local"), 0);
  } finally {
    fixture.cleanup();
  }
});

test("resumption keeps state identity, ownership, and actor binding immutable", async () => {
  const fixture = createRepositoryFixture();
  const fake = fakeExternalCommands(fixture, { archiveRemovesWorktree: true });
  try {
    const applyOptions = await interruptLifecycleCleanup(
      fixture,
      fake,
      "cleanup.remote-branch",
    );
    const interruptedBytes = readFileSync(fixture.stateFile, "utf8");
    const interrupted = JSON.parse(interruptedBytes);
    const truthful = transitionedOptions(fixture, applyOptions, "binding");

    for (const [changes, code] of [
      [{ actor: "paseo:99999999-8888-4777-8666-555555555555" }, "RESUME_STATE_BINDING_MISMATCH"],
      [{ ownership: "dir-m1.20-run-9999" }, "RESUME_STATE_BINDING_MISMATCH"],
      [{ "agent-id": "agent-0002" }, "RESUME_STATE_BINDING_MISMATCH"],
      [{ "workspace-id": "workspace-0002" }, "RESUME_STATE_BINDING_MISMATCH"],
      [{ "head-owner": "intruder" }, "RESUME_STATE_BINDING_MISMATCH"],
      [{ "resume-state-file": truthful["state-file"] }, "RESUME_STATE_NOT_DISTINCT"],
      [
        { "resume-state-file": join(fixture.control, "state.json") },
        "RESUME_STATE_INSIDE_REPOSITORY",
      ],
      [
        { "resume-state-file": join(fixture.root, "missing-state.json") },
        "RESUME_STATE_MISSING",
      ],
    ]) {
      await assert.rejects(
        execute("cleanup-plan", { ...truthful, ...changes }, { run: fake.runner }),
        (error) => error.code === code,
        JSON.stringify(changes),
      );
    }

    // A resumed state describing the same lifecycle is usable directly, and one
    // describing later progress would move the binding backwards.
    const sameBinding = join(fixture.root, "same-binding.json");
    writeFileSync(sameBinding, `${canonicalJson({
      ...interrupted,
      binding: { ...interrupted.binding, checkoutState: "reclaimed", lifecycleState: "reclaimed" },
    })}\n`, { mode: 0o600 });
    await assert.rejects(
      execute(
        "cleanup-plan",
        { ...truthful, "resume-state-file": sameBinding },
        { run: fake.runner },
      ),
      (error) => error.code === "RESUME_STATE_NOT_A_TRANSITION",
    );
    await assert.rejects(
      execute(
        "cleanup-apply",
        {
          ...applyOptions,
          "state-file": join(fixture.root, "backwards-state.json"),
          "resume-state-file": sameBinding,
        },
        { run: fake.runner },
      ),
      (error) => error.code === "RESUME_STATE_LIFECYCLE_REGRESSION",
    );

    // A structurally unsound private control file is refused, never repaired.
    for (const [index, [document, code]] of [
      [
        { ...interrupted, schemaVersion: 1 },
        "RESUME_STATE_SCHEMA_UNSUPPORTED",
      ],
      [{ ...interrupted, unexpected: true }, "RESUME_STATE_INVALID"],
      [
        {
          ...interrupted,
          effects: {
            ...interrupted.effects,
            "cleanup.remote-branch": {
              ...interrupted.effects["cleanup.remote-branch"],
              phase: "assumed",
            },
          },
        },
        "RESUME_STATE_INVALID",
      ],
      [
        {
          ...interrupted,
          effects: { ...interrupted.effects, "../escape": { class: "store_only", phase: "complete", attempts: 0 } },
        },
        "RESUME_STATE_INVALID",
      ],
      [{ ...interrupted, pullRequestNumber: 8 }, "RESUME_STATE_PULL_REQUEST_MISMATCH"],
    ].entries()) {
      const tampered = join(fixture.root, `tampered-${index}.json`);
      writeFileSync(tampered, `${canonicalJson(document)}\n`, { mode: 0o600 });
      await assert.rejects(
        execute(
          "cleanup-plan",
          { ...truthful, "resume-state-file": tampered },
          { run: fake.runner },
        ),
        (error) => error.code === code,
        code,
      );
    }

    // A readable-by-others private control file is never consumed.
    const exposed = join(fixture.root, "exposed-state.json");
    writeFileSync(exposed, interruptedBytes, { mode: 0o600 });
    chmodSync(exposed, 0o644);
    await assert.rejects(
      execute(
        "cleanup-plan",
        { ...truthful, "resume-state-file": exposed },
        { run: fake.runner },
      ),
      (error) => error.code === "RESUME_STATE_PERMISSIONS_INVALID",
    );

    // One state continues exactly one interrupted lifecycle binding.
    await execute("cleanup-plan", truthful, { run: fake.runner });
    const otherPrior = join(fixture.root, "other-prior.json");
    writeFileSync(otherPrior, `${canonicalJson({
      ...interrupted,
      binding: { ...interrupted.binding, lifecycleState: "restored" },
    })}\n`, { mode: 0o600 });
    await assert.rejects(
      execute(
        "cleanup-plan",
        { ...truthful, "resume-state-file": otherPrior },
        { run: fake.runner },
      ),
      (error) => error.code === "RESUME_STATE_REPLACED",
    );

    // The option exists only where a lifecycle transition can strand cleanup.
    await assert.rejects(
      execute(
        "integrate",
        { ...truthful, "resume-state-file": fixture.stateFile },
        { run: fake.runner },
      ),
      (error) => error.code === "OPTION_INVALID",
    );

    assert.equal(readFileSync(fixture.stateFile, "utf8"), interruptedBytes);
  } finally {
    fixture.cleanup();
  }
});

test("interrupted ref cleanup safely compare-deletes a same-Candidate recreation", async () => {
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
    const recovered = await execute("cleanup-apply", applyOptions, {
      run: fake.runner,
    });
    assert.equal(recovered.result.resources, "complete");
    assert.equal(
      git(fixture.control, ["ls-remote", "--heads", "origin", `refs/heads/${BRANCH}`]),
      "",
    );
    const ref = `refs/heads/${BRANCH}`;
    assert.equal(
      fake.state.calls.filter(
        (call) =>
          call.executable === "git" &&
          call.args.includes(`--force-with-lease=${ref}:${fixture.candidate}`) &&
          call.args.includes(`:${ref}`),
      ).length,
      2,
    );
  } finally {
    fixture.cleanup();
  }
});

test("changed remote and local refs win races against cleanup exact guards", async (context) => {
  for (const kind of ["remote", "local"]) {
    await context.test(kind, async () => {
      const fixture = createRepositoryFixture();
      const fake = fakeExternalCommands(fixture);
      try {
        const { applyOptions } = await prepareIntegratedCleanup(fixture, fake);
        const ref = `refs/heads/${BRANCH}`;
        let raced = false;
        const raceRunner = (executable, args, options = {}) => {
          const isRemoteDelete =
            kind === "remote" &&
            executable === "git" &&
            args.includes(`--force-with-lease=${ref}:${fixture.candidate}`) &&
            args.includes(`:${ref}`);
          const isLocalDelete =
            kind === "local" &&
            executable === "git" &&
            args.includes("update-ref") &&
            args.includes("-d") &&
            args.includes(ref) &&
            args.includes(fixture.candidate);
          if (!raced && (isRemoteDelete || isLocalDelete)) {
            raced = true;
            if (kind === "remote") {
              git(fixture.control, [
                "push",
                "--quiet",
                "--force",
                "origin",
                `${fixture.base}:${ref}`,
              ]);
            } else {
              git(fixture.control, [
                "update-ref",
                ref,
                fixture.base,
                fixture.candidate,
              ]);
            }
          }
          return fake.runner(executable, args, options);
        };

        const expectedCode = kind === "remote" ? "REMOTE_REF_CHANGED" : "LOCAL_REF_CHANGED";
        await assert.rejects(
          execute("cleanup-apply", applyOptions, { run: raceRunner }),
          (error) =>
            error instanceof CoordinatorError && error.code === expectedCode,
        );
        assert.equal(raced, true);
        await assert.rejects(
          execute("cleanup-apply", applyOptions, { run: fake.runner }),
          (error) =>
            error instanceof CoordinatorError && error.code === expectedCode,
        );
        const current =
          kind === "remote"
            ? git(fixture.control, ["ls-remote", "--heads", "origin", ref]).split("\t")[0]
            : git(fixture.control, ["rev-parse", "--verify", ref]);
        assert.equal(current, fixture.base);
        const guardedCalls = fake.state.calls.filter((call) => {
          if (call.executable !== "git") return false;
          return kind === "remote"
            ? call.args.includes(`--force-with-lease=${ref}:${fixture.candidate}`) &&
                call.args.includes(`:${ref}`)
            : call.args.includes("update-ref") &&
                call.args.includes("-d") &&
                call.args.includes(ref) &&
                call.args.includes(fixture.candidate);
        });
        assert.equal(guardedCalls.length, 1);
      } finally {
        fixture.cleanup();
      }
    });
  }
});

test("ambiguous remote observation blocks a dispatching cleanup retry", async () => {
  const fixture = createRepositoryFixture();
  const fake = fakeExternalCommands(fixture);
  try {
    const { applyOptions } = await prepareIntegratedCleanup(fixture, fake);
    const effect = "cleanup.remote-branch";
    const ref = `refs/heads/${BRANCH}`;
    await assert.rejects(
      execute("cleanup-apply", applyOptions, {
        run: fake.runner,
        hook(phase, currentEffect) {
          if (phase === "before" && currentEffect === effect) {
            throw new CoordinatorInterruption(effect);
          }
        },
      }),
      (error) => error instanceof CoordinatorInterruption,
    );
    let ambiguous = false;
    const ambiguousRunner = (executable, args, options = {}) => {
      if (
        !ambiguous &&
        executable === "git" &&
        args.includes("ls-remote") &&
        args.at(-1) === ref
      ) {
        ambiguous = true;
        return {
          error: undefined,
          status: 0,
          stdout: `${fixture.candidate}\t${ref}\n${fixture.candidate}\t${ref}\n`,
          stderr: "",
        };
      }
      return fake.runner(executable, args, options);
    };
    await assert.rejects(
      execute("cleanup-apply", applyOptions, { run: ambiguousRunner }),
      (error) =>
        error instanceof CoordinatorError &&
        error.code === "REMOTE_OBSERVATION_AMBIGUOUS",
    );
    assert.equal(ambiguous, true);
    assert.equal(
      git(fixture.control, ["ls-remote", "--heads", "origin", ref]).split("\t")[0],
      fixture.candidate,
    );
    assert.equal(
      fake.state.calls.filter(
        (call) =>
          call.executable === "git" &&
          call.args.includes(`--force-with-lease=${ref}:${fixture.candidate}`),
      ).length,
      0,
    );
  } finally {
    fixture.cleanup();
  }
});

test("the exact-ref retry exception does not broaden worktree removal", async () => {
  const fixture = createRepositoryFixture();
  const fake = fakeExternalCommands(fixture);
  try {
    const { applyOptions } = await prepareIntegratedCleanup(fixture, fake);
    await assert.rejects(
      execute("cleanup-apply", applyOptions, {
        run: fake.runner,
        hook(phase, effect) {
          if (phase === "before" && effect === "cleanup.worktree") {
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
    assert.equal(existsSync(fixture.checkout), true);
    assert.equal(
      fake.state.calls.filter(
        (call) =>
          call.executable === "git" &&
          call.args.includes("worktree") &&
          call.args.includes("remove"),
      ).length,
      0,
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

test("cleanup adopts terminal Paseo archival without dispatching a duplicate effect", async () => {
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

    // The agent reached its terminal state outside this attempt. Cleanup must
    // adopt that fact instead of archiving an already-archived resource.
    fake.state.agentArchived = true;
    const result = await execute(
      "cleanup-apply",
      { ...integratedOptions, "plan-file": fixture.planFile },
      { run: fake.runner },
    );
    assert.equal(result.result.resources, "complete");
    assert.equal(fake.state.agentArchiveDispatches, 0);
    const state = JSON.parse(readFileSync(fixture.stateFile, "utf8"));
    assert.equal(state.effects["cleanup.agent"].phase, "complete");
    assert.equal(state.effects["cleanup.agent"].attempts, 0);
    assert.equal(state.effects["cleanup.workspace"].phase, "complete");
    assert.deepEqual(fake.state.workspaces, []);
  } finally {
    fixture.cleanup();
  }
});

test("a lost archive response reconciles by authoritative readback, never by re-execution", async () => {
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

    // Both mutations land and then lose their response. The bounded child can
    // only report an unproven outcome; the readback supplies the completion.
    const lostResponse = (executable, args, options = {}) => {
      if (executable === "paseo" && args[0] === "archive") {
        fake.state.agentArchiveDispatches += 1;
        fake.state.agentArchived = true;
        return {
          error: undefined,
          status: 68,
          stdout: "",
          stderr: "PASEO_LIFECYCLE_MUTATION_FAILED\n",
        };
      }
      if (executable === "paseo" && args[0] === "workspace" && args[1] === "archive") {
        fake.state.workspaces = [];
        return {
          error: undefined,
          status: 68,
          stdout: "",
          stderr: "PASEO_LIFECYCLE_MUTATION_FAILED\n",
        };
      }
      return fake.runner(executable, args, options);
    };
    const result = await execute(
      "cleanup-apply",
      { ...integratedOptions, "plan-file": fixture.planFile },
      { run: lostResponse },
    );
    assert.equal(result.result.resources, "complete");
    assert.equal(fake.state.agentArchiveDispatches, 1);
    const state = JSON.parse(readFileSync(fixture.stateFile, "utf8"));
    assert.equal(state.effects["cleanup.agent"].phase, "complete");
    assert.equal(state.effects["cleanup.agent"].attempts, 1);
    assert.equal(state.effects["cleanup.workspace"].phase, "complete");
    assert.equal(state.effects["cleanup.workspace"].attempts, 1);
    assert.equal(existsSync(fixture.checkout), false);
  } finally {
    fixture.cleanup();
  }
});

test("an unproven archive preserves its dispatching intent and completes on the next run", async () => {
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

    let unavailable = true;
    const flaky = (executable, args, options = {}) => {
      if (executable === "paseo" && args[0] === "archive" && unavailable) {
        unavailable = false;
        return {
          error: undefined,
          status: 68,
          stdout: "",
          stderr: "PASEO_LIFECYCLE_MUTATION_FAILED\n",
        };
      }
      return fake.runner(executable, args, options);
    };
    const applyOptions = { ...integratedOptions, "plan-file": fixture.planFile };
    await assert.rejects(
      execute("cleanup-apply", applyOptions, { run: flaky }),
      (error) =>
        error instanceof CoordinatorError && error.code === "AGENT_ARCHIVE_UNKNOWN",
    );
    const parked = JSON.parse(readFileSync(fixture.stateFile, "utf8"));
    assert.equal(parked.effects["cleanup.agent"].phase, "dispatching");
    assert.equal(parked.effects["cleanup.agent"].attempts, 1);
    assert.equal(fake.state.agentArchived, false);
    assert.equal(fake.state.agentArchiveDispatches, 0);
    assert.equal(existsSync(fixture.checkout), true);

    const retried = await execute("cleanup-apply", applyOptions, { run: flaky });
    assert.equal(retried.result.resources, "complete");
    assert.equal(fake.state.agentArchiveDispatches, 1);
    const settled = JSON.parse(readFileSync(fixture.stateFile, "utf8"));
    assert.equal(settled.effects["cleanup.agent"].phase, "complete");
    assert.equal(settled.effects["cleanup.agent"].attempts, 2);
  } finally {
    fixture.cleanup();
  }
});

test("a refused archive credential is reported exactly and preserves every resource", async () => {
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

    // The daemon rejected the credential, so no effect was executed. Reporting
    // this as an unproven archive is what hid the M6.20/M6.4/M6.3 root cause.
    for (const [stderr, code] of [
      ["PASEO_AUTH_REQUIRED\n", "PASEO_AUTH_REQUIRED"],
      ["PASEO_AUTH_FAILED\n", "PASEO_AUTH_FAILED"],
      ["PASEO_LIFECYCLE_RESPONSE_REDACTED\n", "PASEO_LIFECYCLE_RESPONSE_REDACTED"],
    ]) {
      const refusing = (executable, args, options = {}) => {
        if (executable === "paseo" && args[0] === "archive") {
          return { error: undefined, status: 65, stdout: "", stderr };
        }
        return fake.runner(executable, args, options);
      };
      await assert.rejects(
        execute(
          "cleanup-apply",
          { ...integratedOptions, "plan-file": fixture.planFile },
          { run: refusing },
        ),
        (error) => error instanceof CoordinatorError && error.code === code,
      );
    }
    assert.equal(fake.state.agentArchived, false);
    assert.equal(fake.state.agentArchiveDispatches, 0);
    assert.deepEqual(fake.state.workspaces.map((item) => item.workspaceId), ["workspace-0001"]);
    assert.equal(existsSync(fixture.checkout), true);
    const state = JSON.parse(readFileSync(fixture.stateFile, "utf8"));
    assert.equal(state.effects["cleanup.agent"].phase, "dispatching");
    assert.equal(state.effects["cleanup.workspace"], undefined);
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

test("the Reviewer leg archives the agent, archives the host view, then removes the exact checkout", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, { reviewerArchived: false });
    fixture.reviewerCheckout = createReviewerCheckout(fixture, fake);
    recordReviewVerdict(fake, { candidate: fixture.candidate, base: fixture.base });
    const { applied, plan } = await reviewerPlanAndApply(fixture, fake);

    assert.equal(plan.result.command, "reviewer-cleanup-plan");
    assert.equal(plan.result.resources.agent.archived, false);
    assert.equal(plan.result.resources.workspace.id, "reviewer-workspace-0001");
    assert.equal(plan.result.resources.checkout.candidate, fixture.candidate);
    assert.equal(plan.result.resources.review.verdict, "approve_candidate");
    assert.equal(applied.result.resources, "complete");
    assert.equal(existsSync(fixture.reviewerCheckout), false);
    assert.equal(fake.state.reviewerArchived, true);
    assert.equal(fake.state.reviewerArchiveDispatches, 1);

    const state = JSON.parse(
      readFileSync(join(fixture.root, "reviewer-state.json"), "utf8"),
    );
    for (const effect of [
      "cleanup.reviewer-agent",
      "cleanup.reviewer-workspace",
      "cleanup.reviewer-checkout",
    ]) {
      assert.equal(state.effects[effect].phase, "complete", effect);
      assert.equal(state.effects[effect].attempts, 1, effect);
    }
    assert.equal(state.effects["cleanup.reviewer-agent"].class, "idempotent_close");
    assert.equal(
      state.effects["cleanup.reviewer-checkout"].class,
      "destructive_terminal",
    );
    // The documented order inside the leg: agent, then host view, then the
    // owner-marked checkout.
    const dispatched = fake.state.calls
      .filter((call) => call.executable === "paseo")
      .map((call) => call.args.slice(0, 2).join(" "))
      .filter((call) => call === "archive reviewer-0001" || call === "workspace archive");
    assert.deepEqual(dispatched, ["archive reviewer-0001", "workspace archive"]);
  } finally {
    fixture.cleanup();
  }
});

test("the Reviewer leg adopts an already-archived Reviewer without a second dispatch", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, { reviewerArchived: true });
    fixture.reviewerCheckout = createReviewerCheckout(fixture, fake);
    recordReviewVerdict(fake, { candidate: fixture.candidate, base: fixture.base });
    // The host view is already gone too, which is exactly what an interrupted
    // or externally archived Reviewer leaves behind.
    fake.state.workspaces = fake.state.workspaces.filter(
      (workspace) => workspace.workspaceId !== "reviewer-workspace-0001",
    );
    const { applied } = await reviewerPlanAndApply(fixture, fake, {
      "reviewer-workspace-id": "none",
    });
    assert.equal(applied.result.resources, "complete");
    assert.equal(fake.state.reviewerArchiveDispatches, 0);
    assert.equal(existsSync(fixture.reviewerCheckout), false);
    const state = JSON.parse(
      readFileSync(join(fixture.root, "reviewer-state.json"), "utf8"),
    );
    assert.equal(state.effects["cleanup.reviewer-agent"].phase, "complete");
    assert.equal(state.effects["cleanup.reviewer-agent"].attempts, 0);
    assert.equal(state.effects["cleanup.reviewer-workspace"], undefined);
  } finally {
    fixture.cleanup();
  }
});

test("a completed Reviewer leg records no duplicate effect when it is replayed", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, { reviewerArchived: false });
    fixture.reviewerCheckout = createReviewerCheckout(fixture, fake);
    recordReviewVerdict(fake, { candidate: fixture.candidate, base: fixture.base });
    const { options, planFile } = await reviewerPlanAndApply(fixture, fake);
    const replayOptions = {
      ...options,
      "reviewer-checkout-state": "reclaimed",
      "reviewer-lifecycle-state": "reclaimed",
      "plan-file": planFile,
    };
    // The replay is bound to the world the first run produced, so it needs its
    // own plan; the completed effects it carries are never dispatched again.
    const replan = await execute(
      "reviewer-cleanup-plan",
      { ...replayOptions, "state-file": join(fixture.root, "replay-state.json"),
        "resume-state-file": options["state-file"] },
      { run: fake.runner },
    );
    const replanFile = join(fixture.root, "replay-plan.json");
    writeFileSync(replanFile, `${canonicalJson(replan)}\n`, { mode: 0o600 });
    const replayed = await execute(
      "reviewer-cleanup-apply",
      {
        ...replayOptions,
        "state-file": join(fixture.root, "replay-state.json"),
        "resume-state-file": options["state-file"],
        "plan-file": replanFile,
      },
      { run: fake.runner },
    );
    assert.equal(replayed.result.resources, "complete");
    assert.equal(fake.state.reviewerArchiveDispatches, 1);
    const state = JSON.parse(
      readFileSync(join(fixture.root, "replay-state.json"), "utf8"),
    );
    assert.equal(state.effects["cleanup.reviewer-agent"].attempts, 1);
    assert.equal(state.effects["cleanup.reviewer-checkout"].attempts, 1);
    assert.equal(state.effects["cleanup.reviewer-checkout"].phase, "complete");
  } finally {
    fixture.cleanup();
  }
});

test("the Reviewer leg refuses an unmarked or ambiguous disposable checkout", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, { reviewerArchived: false });
    fixture.reviewerCheckout = createReviewerCheckout(fixture, fake);
    recordReviewVerdict(fake, { candidate: fixture.candidate, base: fixture.base });
    const plan = () =>
      execute("reviewer-cleanup-plan", reviewerOptions(fixture), {
        run: fake.runner,
      });

    // Baseline: the marked checkout is admitted.
    await plan();

    const cases = [
      [
        "REVIEWER_CHECKOUT_ATTACHED",
        () => git(fixture.reviewerCheckout, ["checkout", "--quiet", "-B", "attached"]),
        () => git(fixture.reviewerCheckout, ["checkout", "--quiet", "--detach", fixture.candidate]),
      ],
      [
        "REVIEWER_CHECKOUT_CANDIDATE_MISMATCH",
        () => git(fixture.reviewerCheckout, ["checkout", "--quiet", "--detach", fixture.base]),
        () => git(fixture.reviewerCheckout, ["checkout", "--quiet", "--detach", fixture.candidate]),
      ],
      [
        "REVIEWER_CHECKOUT_DIRTY",
        () => writeFileSync(join(fixture.reviewerCheckout, "probe.txt"), "probe\n"),
        () => rmSync(join(fixture.reviewerCheckout, "probe.txt")),
      ],
      [
        "REVIEWER_CHECKOUT_UNMARKED",
        () => {
          rmSync(join(fixture.reviewerCheckout, ".git-moved"), {
            recursive: true,
            force: true,
          });
          renameSync(
            join(fixture.reviewerCheckout, ".git"),
            join(fixture.reviewerCheckout, ".git-moved"),
          );
        },
        () =>
          renameSync(
            join(fixture.reviewerCheckout, ".git-moved"),
            join(fixture.reviewerCheckout, ".git"),
          ),
      ],
      [
        "REVIEWER_IDENTITY_AMBIGUOUS",
        () => {
          fake.state.reviewerLabels = {
            "director.role": "reviewer",
            "director.task": TASK,
            "director.candidate": fixture.base,
          };
        },
        () => {
          fake.state.reviewerLabels = null;
        },
      ],
      [
        "REVIEWER_PARENTED",
        () => {
          fake.state.reviewerParentAgentId = "agent-0001";
        },
        () => {
          fake.state.reviewerParentAgentId = null;
        },
      ],
      [
        "REVIEWER_CHECKOUT_OWNERSHIP_MISMATCH",
        () => {
          fake.state.reviewerCheckout = fixture.control;
        },
        () => {
          fake.state.reviewerCheckout = fixture.reviewerCheckout;
        },
      ],
    ];
    for (const [code, breakIt, restore] of cases) {
      breakIt();
      await assert.rejects(plan(), (error) => {
        assert.equal(error.code, code);
        return true;
      });
      restore();
      await plan();
    }
    // Nothing was removed while identity was in doubt.
    assert.equal(existsSync(fixture.reviewerCheckout), true);
    assert.equal(fake.state.reviewerArchiveDispatches, 0);
  } finally {
    fixture.cleanup();
  }
});

test("the Reviewer leg never treats a Candidate absent from published history as dead", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, { reviewerArchived: false });
    const orphan = createOrphanedReviewerCheckout(fixture, fake);
    fixture.reviewerCheckout = orphan.path;
    recordReviewVerdict(fake, {
      candidate: orphan.candidate,
      base: fixture.base,
      verdict: "changes_requested",
    });
    fake.state.taskStatus = "closed";
    const { applied } = await reviewerPlanAndApply(fixture, fake, {
      candidate: orphan.candidate,
    });
    assert.equal(applied.result.resources, "complete");
    assert.equal(existsSync(orphan.path), false);
    // The control repository was never asked to resolve the superseded
    // Candidate: a pruned object store must not strand the oldest debt.
    const controlReads = fake.state.calls.filter(
      (call) => call.executable === "git" && call.args.includes(orphan.candidate),
    );
    assert.deepEqual(controlReads, []);
  } finally {
    fixture.cleanup();
  }
});

test("the Reviewer leg refuses a Review that is still working", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, { reviewerArchived: false });
    fixture.reviewerCheckout = createReviewerCheckout(fixture, fake);

    // Running: the daemon says a turn is in flight.
    fake.state.reviewerStatus = "running";
    recordReviewVerdict(fake, { candidate: fixture.candidate, base: fixture.base });
    await assert.rejects(
      execute("reviewer-cleanup-plan", reviewerOptions(fixture), { run: fake.runner }),
      (error) => error.code === "REVIEWER_REVIEW_IN_FLIGHT",
    );

    // Idle with no durable verdict while its Task is still in progress: a
    // Review that has not reported is not a Review that is over.
    fake.state.reviewerStatus = "idle";
    fake.state.beadsComments = fake.state.beadsComments.filter(
      (comment) => comment.id !== "review-0001",
    );
    await assert.rejects(
      execute(
        "reviewer-cleanup-plan",
        reviewerOptions(fixture, { "reviewer-review-state": "abandoned" }),
        { run: fake.runner },
      ),
      (error) => error.code === "REVIEWER_REVIEW_IN_FLIGHT",
    );
    // The same binding also cannot claim a verdict that Beads does not hold.
    await assert.rejects(
      execute("reviewer-cleanup-plan", reviewerOptions(fixture), { run: fake.runner }),
      (error) => error.code === "REVIEWER_VERDICT_NOT_DURABLE",
    );
    // An abandoned Review of a closed Task is handled explicitly.
    fake.state.taskStatus = "closed";
    const abandoned = await execute(
      "reviewer-cleanup-plan",
      reviewerOptions(fixture, { "reviewer-review-state": "abandoned" }),
      { run: fake.runner },
    );
    assert.equal(abandoned.result.resources.review.state, "abandoned");
    assert.equal(abandoned.result.resources.review.verdict, null);
    assert.equal(existsSync(fixture.reviewerCheckout), true);
  } finally {
    fixture.cleanup();
  }
});

test("cleanup refuses the Task Agent leg until the Review's exact Reviewer is terminal", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, { reviewerArchived: false });
    rebindReviewRouting(fixture, {
      agentId: "agent-0001",
      lifecycleState: "active",
      workspaceId: "workspace-0001",
    });
    Object.assign(fixture.options, {
      "agent-id": "agent-0001",
      "lifecycle-state": "active",
      "workspace-id": "workspace-0001",
    });
    await publishReadyFixture(fixture, fake);
    const integratedOptions = {
      ...gateOptions(fixture),
      "state-file": fixture.stateFile,
      "review-file": fixture.reviewFile,
    };
    await execute("integrate", integratedOptions, { run: fake.runner });
    await assert.rejects(
      execute("cleanup-plan", integratedOptions, { run: fake.runner }),
      (error) => error.code === "REVIEWER_LEG_PENDING",
    );

    // The Reviewer leg runs first, and only then does the Task Agent leg admit
    // a plan at all.
    fake.state.reviewerArchived = true;

    // A Reviewer of an earlier Candidate of this same Task is still alive.
    // Ordering the Review's own Reviewer is not enough: this is exactly how a
    // Task accumulates Reviewers nobody reconciles.
    fake.state.extraListedAgents = [
      {
        Id: "reviewer-0000",
        Name: "Review of a superseded Candidate",
        Status: "idle",
        Archived: false,
        ArchivedAt: null,
        Cwd: join(fixture.root, "superseded-review"),
        ParentAgentId: null,
        Labels: {
          "director.role": "reviewer",
          "director.task": TASK,
          "director.candidate": fixture.base,
        },
      },
    ];
    await assert.rejects(
      execute("cleanup-plan", integratedOptions, { run: fake.runner }),
      (error) => error.code === "REVIEWER_LEG_PENDING",
    );
    fake.state.extraListedAgents[0].Archived = true;

    const plan = await execute("cleanup-plan", integratedOptions, { run: fake.runner });
    assert.equal(plan.result.resources.reviewer.agentId, "reviewer-0001");
    assert.equal(plan.result.resources.reviewer.enumeration.saturated, false);
    writeFileSync(fixture.planFile, `${canonicalJson(plan)}\n`, { mode: 0o600 });

    // A Reviewer that came back blocks the Task Agent leg before any effect.
    fake.state.reviewerArchived = false;
    await assert.rejects(
      execute(
        "cleanup-apply",
        { ...integratedOptions, "plan-file": fixture.planFile },
        { run: fake.runner },
      ),
      (error) => error.code === "REVIEWER_LEG_PENDING",
    );
    assert.equal(fake.state.agentArchiveDispatches, 0);
    assert.equal(fake.state.agentArchived, false);
    assert.equal(existsSync(fixture.checkout), true);

    fake.state.reviewerArchived = true;
    const applied = await execute(
      "cleanup-apply",
      { ...integratedOptions, "plan-file": fixture.planFile },
      { run: fake.runner },
    );
    assert.equal(applied.result.resources, "complete");
    assert.equal(fake.state.agentArchived, true);
  } finally {
    fixture.cleanup();
  }
});

test("an interrupted Reviewer leg resumes truthfully across its own lifecycle transition", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, { reviewerArchived: false });
    fixture.reviewerCheckout = createReviewerCheckout(fixture, fake);
    recordReviewVerdict(fake, { candidate: fixture.candidate, base: fixture.base });
    const options = reviewerOptions(fixture);
    const plan = await execute("reviewer-cleanup-plan", options, { run: fake.runner });
    const planFile = join(fixture.root, "interrupted-plan.json");
    writeFileSync(planFile, `${canonicalJson(plan)}\n`, { mode: 0o600 });

    // The interruption lands after the agent archive reached the daemon but
    // before its result was recorded, which is the moment the run's own effect
    // makes its lifecycle binding unassertable.
    await assert.rejects(
      execute(
        "reviewer-cleanup-apply",
        { ...options, "plan-file": planFile },
        {
          run: fake.runner,
          hook(phase, effect) {
            if (phase === "after" && effect === "cleanup.reviewer-agent") {
              throw new CoordinatorInterruption(effect);
            }
          },
        },
      ),
      (error) => error instanceof CoordinatorInterruption,
    );
    const interrupted = JSON.parse(readFileSync(options["state-file"], "utf8"));
    assert.equal(interrupted.effects["cleanup.reviewer-agent"].phase, "dispatching");
    assert.equal(fake.state.reviewerArchived, true);

    // The truthful binding now records the archived Reviewer, and the resumed
    // state supplies the intent the transitioned binding cannot hold.
    fake.state.workspaces = fake.state.workspaces.filter(
      (workspace) => workspace.workspaceId !== "reviewer-workspace-0001",
    );
    const resumedOptions = {
      ...options,
      "reviewer-lifecycle-state": "reclaimed",
      "state-file": join(fixture.root, "resumed-reviewer-state.json"),
      "resume-state-file": options["state-file"],
    };
    const resumedPlan = await execute("reviewer-cleanup-plan", resumedOptions, {
      run: fake.runner,
    });
    const resumedPlanFile = join(fixture.root, "resumed-plan.json");
    writeFileSync(resumedPlanFile, `${canonicalJson(resumedPlan)}\n`, { mode: 0o600 });
    const resumed = await execute(
      "reviewer-cleanup-apply",
      { ...resumedOptions, "plan-file": resumedPlanFile },
      { run: fake.runner },
    );
    assert.equal(resumed.result.resources, "complete");
    assert.equal(existsSync(fixture.reviewerCheckout), false);
    assert.equal(fake.state.reviewerArchiveDispatches, 1);

    const state = JSON.parse(readFileSync(resumedOptions["state-file"], "utf8"));
    assert.equal(state.continuation.priorReviewerLifecycleState, "active");
    assert.equal(state.continuation.priorReviewerCheckoutState, "present");
    assert.ok(
      state.continuation.adoptedEffects.includes("cleanup.reviewer-agent"),
      "the interrupted archive intent is carried into the transitioned binding",
    );
    assert.equal(state.effects["cleanup.reviewer-agent"].phase, "complete");
    assert.equal(state.effects["cleanup.reviewer-checkout"].phase, "complete");
  } finally {
    fixture.cleanup();
  }
});

test("a resumed Reviewer leg still refuses a removal absence nothing recorded", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, { reviewerArchived: true });
    fixture.reviewerCheckout = createReviewerCheckout(fixture, fake);
    recordReviewVerdict(fake, { candidate: fixture.candidate, base: fixture.base });
    const options = reviewerOptions(fixture);
    const plan = await execute("reviewer-cleanup-plan", options, { run: fake.runner });
    const planFile = join(fixture.root, "unrecorded-plan.json");
    writeFileSync(planFile, `${canonicalJson(plan)}\n`, { mode: 0o600 });
    // Something outside cleanup removes the checkout while the leg is running.
    // No recorded intent explains the absence, so it is refused rather than
    // reported as a completed removal.
    await assert.rejects(
      execute(
        "reviewer-cleanup-apply",
        { ...options, "plan-file": planFile },
        {
          run: fake.runner,
          hook(phase, effect) {
            if (phase === "before" && effect === "cleanup.reviewer-workspace") {
              rmSync(fixture.reviewerCheckout, { recursive: true, force: true });
            }
          },
        },
      ),
      (error) => error.code === "DESTRUCTIVE_ABSENCE_AMBIGUOUS",
    );
    const state = JSON.parse(readFileSync(options["state-file"], "utf8"));
    assert.equal(state.effects["cleanup.reviewer-checkout"], undefined);

    // Binding a checkout that is already gone as still present is refused
    // before any effect, rather than being reconciled into a removal.
    await assert.rejects(
      execute(
        "reviewer-cleanup-apply",
        { ...options, "plan-file": planFile },
        { run: fake.runner },
      ),
      (error) => error.code === "PATH_UNAVAILABLE",
    );
  } finally {
    fixture.cleanup();
  }
});

test("a report authored by another Reviewer binds this one nothing", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, { reviewerArchived: false });
    fixture.reviewerCheckout = createReviewerCheckout(fixture, fake);
    fake.state.taskStatus = "closed";
    // Two Reviewers of the same Candidate. One reported; the other did not, and
    // must not inherit the report through the Candidate they share.
    recordAuthoredReviewReport(fake, { agentId: "reviewer-9999", id: "review-other" });
    await assert.rejects(
      execute("reviewer-cleanup-plan", reviewerOptions(fixture), { run: fake.runner }),
      (error) => error.code === "REVIEWER_VERDICT_NOT_DURABLE",
    );
    const abandoned = await execute(
      "reviewer-cleanup-plan",
      reviewerOptions(fixture, { "reviewer-review-state": "abandoned" }),
      { run: fake.runner },
    );
    assert.equal(abandoned.result.resources.review.reportSource, null);
    assert.equal(abandoned.result.resources.review.unboundEvidence, 0);
  } finally {
    fixture.cleanup();
  }
});

test("every report form the live record holds binds its Reviewer, and nothing else does", async () => {
  // Each form is taken from a real comment on a real Task. Written by the
  // Reviewer, every one binds; written by anyone else, the identical text binds
  // nothing, because what makes a comment a report is who wrote it.
  // The three that name the Reviewer in their text leave unbound evidence when
  // someone else writes them; the two that do not, leave none.
  // `binds` is whether the form states its verdict where this rule reads: a
  // `Verdict:` field, or the comment's own first line. The transcribed form
  // states it in neither, so narrowing the fallback to the first line costs it
  // even when the Reviewer writes it — a miss, and it is counted as one.
  const forms = [
    ["headingAndLines", 0, true],
    ["headingOnly", 1, true],
    ["linesOnly", 0, true],
    ["otherHeading", 1, true],
    ["transcribed", 1, false],
  ];
  for (const [form, namesReviewer, binds] of forms) {
    for (const authoredByReviewer of [true, false]) {
      const fixture = createRepositoryFixture();
      try {
        const fake = fakeExternalCommands(fixture, { reviewerArchived: false });
        fixture.reviewerCheckout = createReviewerCheckout(fixture, fake);
        fake.state.taskStatus = "closed";
        recordReviewReport(fake, {
          form,
          author: authoredByReviewer ? `paseo:${"reviewer-0001"}` : "paseo:coordinator-0001",
          candidate: fixture.candidate,
          base: fixture.base,
          verdict: "approve_candidate",
        });
        if (authoredByReviewer && !binds) {
          // Written by the Reviewer but stating its verdict nowhere this rule
          // reads: refused, and counted as a report it could not read.
          await assert.rejects(
            execute("reviewer-cleanup-plan", reviewerOptions(fixture), { run: fake.runner }),
            (error) => error.code === "REVIEWER_VERDICT_NOT_DURABLE",
            form,
          );
          await assert.rejects(
            execute(
              "reviewer-cleanup-plan",
              reviewerOptions(fixture, { "reviewer-review-state": "abandoned" }),
              { run: fake.runner },
            ),
            (error) => {
              assert.equal(error.code, "REVIEWER_REPORT_EVIDENCE_UNRESOLVED", form);
              assert.equal(error.details.unreadableReports, 1, form);
              return true;
            },
          );
        } else if (authoredByReviewer) {
          const plan = await execute("reviewer-cleanup-plan", reviewerOptions(fixture), {
            run: fake.runner,
          });
          assert.equal(plan.result.resources.review.reportSource, "authored", form);
          assert.equal(plan.result.resources.review.verdict, "approve_candidate", form);
          // The Reviewer's own bound report is not also counted as evidence
          // that something unbound names it.
          assert.equal(plan.result.resources.review.unboundEvidence, 0, form);
        } else {
          await assert.rejects(
            execute("reviewer-cleanup-plan", reviewerOptions(fixture), { run: fake.runner }),
            (error) => error.code === "REVIEWER_VERDICT_NOT_DURABLE",
            form,
          );
          // The miss is not merely surfaced, it is refused: a form that names
          // this Reviewer with a verdict makes `abandoned` unavailable, so the
          // surrendered coverage cannot be bound away by an operator.
          const abandon = () =>
            execute(
              "reviewer-cleanup-plan",
              reviewerOptions(fixture, { "reviewer-review-state": "abandoned" }),
              { run: fake.runner },
            );
          if (namesReviewer > 0) {
            await assert.rejects(abandon(), (error) => {
              assert.equal(error.code, "REVIEWER_REPORT_EVIDENCE_UNRESOLVED", form);
              assert.equal(error.details.unboundEvidence, namesReviewer, form);
              return true;
            });
          } else {
            const abandoned = await abandon();
            assert.equal(abandoned.result.resources.review.reportSource, null, form);
            assert.equal(abandoned.result.resources.review.unboundEvidence, 0, form);
          }
        }
      } finally {
        fixture.cleanup();
      }
    }
  }
});

test("a comment that reports nothing binds nothing, whoever wrote it", async () => {
  // Three records that name the Reviewer, or the Candidate, or both, and report
  // no verdict about this Review. Every one must bind nothing; the first is the
  // live comment that bound a Reviewer which had authored nothing.
  const records = [
    ["coordinator dispatch record", recordDispatchRecord],
    ["coordinator disposition record", recordDispositionRecord],
    ["the Reviewer's own progress note", recordReviewerProgressNote],
    [
      "the bootstrap record",
      (fake, options) => recordReviewReport(fake, { ...options, form: "bootstrapRecord", author: "paseo:coordinator-0001" }),
    ],
  ];
  for (const [label, record] of records) {
    const fixture = createRepositoryFixture();
    try {
      const fake = fakeExternalCommands(fixture, { reviewerArchived: false });
      fixture.reviewerCheckout = createReviewerCheckout(fixture, fake);
      record(fake, { candidate: fixture.candidate, base: fixture.base });
      await assert.rejects(
        execute("reviewer-cleanup-plan", reviewerOptions(fixture), { run: fake.runner }),
        (error) => error.code === "REVIEWER_VERDICT_NOT_DURABLE",
        label,
      );
      // And while its Task is in progress it cannot be abandoned either, so a
      // Review that is still running has no admissible binding at all.
      await assert.rejects(
        execute(
          "reviewer-cleanup-plan",
          reviewerOptions(fixture, { "reviewer-review-state": "abandoned" }),
          { run: fake.runner },
        ),
        (error) => error.code === "REVIEWER_REVIEW_IN_FLIGHT",
        label,
      );
    } finally {
      fixture.cleanup();
    }
  }
});

test("the operative verdict is the last one the Reviewer stated", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, { reviewerArchived: false });
    fixture.reviewerCheckout = createReviewerCheckout(fixture, fake);
    fake.state.taskStatus = "closed";
    // A note before the report, and a correction after it. The report in the
    // middle must not be displaced by the note, and the correction supersedes.
    recordReviewerProgressNote(fake, {});
    recordReviewVerdict(fake, {
      candidate: fixture.candidate,
      base: fixture.base,
      verdict: "changes_requested",
    });
    recordReviewReport(fake, {
      form: "headingOnly",
      id: "review-corrected",
      author: `paseo:${"reviewer-0001"}`,
      candidate: fixture.candidate,
      base: fixture.base,
      verdict: "approve_candidate",
    });
    const plan = await execute("reviewer-cleanup-plan", reviewerOptions(fixture), {
      run: fake.runner,
    });
    assert.equal(plan.result.resources.review.verdict, "approve_candidate");
    // The note it wrote before reporting is counted as something this rule
    // declined to read, not silently dropped.
    assert.equal(plan.result.resources.review.unreadableReports, 1);
  } finally {
    fixture.cleanup();
  }
});

test("a report this rule cannot read leaves a visible trace rather than an absence", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, { reviewerArchived: false });
    fixture.reviewerCheckout = createReviewerCheckout(fixture, fake);
    fake.state.taskStatus = "closed";
    // Copied from the live record: the Reviewer's own report, concluding in a
    // vocabulary outside the contract, with no Verdict field to read.
    recordComment(fake, {
      id: "review-heading-only-verdict",
      author: `paseo:${"reviewer-0001"}`,
      text: `INDEPENDENT REVIEW — INCONCLUSIVE (not an approval)\n\nReviewer: paseo:reviewer-0001, parentless.\nCandidate: ${fixture.candidate}. Base: ${fixture.base}.\n`,
    });
    await assert.rejects(
      execute("reviewer-cleanup-plan", reviewerOptions(fixture), { run: fake.runner }),
      (error) => error.code === "REVIEWER_VERDICT_NOT_DURABLE",
    );
    await assert.rejects(
      execute(
        "reviewer-cleanup-plan",
        reviewerOptions(fixture, { "reviewer-review-state": "abandoned" }),
        { run: fake.runner },
      ),
      (error) => {
        assert.equal(error.code, "REVIEWER_REPORT_EVIDENCE_UNRESOLVED");
        assert.equal(error.details.unreadableReports, 1);
        return true;
      },
    );
  } finally {
    fixture.cleanup();
  }
});

test("a verdict token outside the closed set is still a stated verdict", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, { reviewerArchived: false });
    fixture.reviewerCheckout = createReviewerCheckout(fixture, fake);
    // dir-m6.20 holds exactly this: a report whose Verdict line reads
    // "inconclusive". A reader that admits only the three contract verdicts
    // reports it as a Review that never happened.
    recordComment(fake, {
      id: "review-inconclusive",
      author: `paseo:${"reviewer-0001"}`,
      text: `INDEPENDENT REVIEW — INCONCLUSIVE (not an approval)\n\nVerdict: inconclusive\nCandidate: ${fixture.candidate}\nBase: ${fixture.base}\n`,
    });
    const plan = await execute("reviewer-cleanup-plan", reviewerOptions(fixture), {
      run: fake.runner,
    });
    assert.equal(plan.result.resources.review.reportSource, "authored");
    assert.equal(plan.result.resources.review.verdict, "inconclusive");
  } finally {
    fixture.cleanup();
  }
});

test("the disposable checkout is re-proved between the owner marker and the removal", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, { reviewerArchived: true });
    fixture.reviewerCheckout = createReviewerCheckout(fixture, fake);
    recordReviewVerdict(fake, { candidate: fixture.candidate, base: fixture.base });
    const options = reviewerOptions(fixture, { "reviewer-workspace-id": "none" });
    const plan = await execute("reviewer-cleanup-plan", options, { run: fake.runner });
    const planFile = join(fixture.root, "toctou-plan.json");
    writeFileSync(planFile, `${canonicalJson(plan)}\n`, { mode: 0o600 });

    // The directory is replaced by a different one at the same path, after the
    // marker was proved and before the removal runs. Path identity is not
    // identity; the device and inode are.
    await assert.rejects(
      execute(
        "reviewer-cleanup-apply",
        { ...options, "plan-file": planFile },
        {
          run: fake.runner,
          hook(phase, effect) {
            if (phase === "before" && effect === "cleanup.reviewer-checkout") {
              // Moved aside rather than deleted, so the original inode stays
              // allocated and the substitute cannot be handed the same number.
              renameSync(fixture.reviewerCheckout, `${fixture.reviewerCheckout}-moved`);
              mkdirSync(fixture.reviewerCheckout, { recursive: true });
              writeFileSync(join(fixture.reviewerCheckout, "not-the-checkout"), "x\n");
            }
          },
        },
      ),
      (error) => error.code === "REVIEWER_CHECKOUT_IDENTITY_INVALID",
    );
    // The substituted directory is still there: nothing was removed on a proof
    // that no longer described it.
    assert.equal(existsSync(join(fixture.reviewerCheckout, "not-the-checkout")), true);
  } finally {
    fixture.cleanup();
  }
});

test("a reclaimed Reviewer binding is checked against the daemon rather than trusted", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, { reviewerArchived: false });
    fixture.reviewerCheckout = createReviewerCheckout(fixture, fake);
    recordReviewVerdict(fake, { candidate: fixture.candidate, base: fixture.base });
    const reclaimed = reviewerOptions(fixture, {
      "reviewer-lifecycle-state": "reclaimed",
    });
    // No workspace is bound for the agent assertions, so only the agent read
    // can produce the refusal and the guard is not masked by the workspace one.
    const agentOnly = reviewerOptions(fixture, {
      "reviewer-lifecycle-state": "reclaimed",
      "reviewer-workspace-id": "none",
    });

    // The agent is live. Binding it as historical is refused rather than
    // accepted as a statement about a resource nobody looked at.
    await assert.rejects(
      execute("reviewer-cleanup-plan", agentOnly, { run: fake.runner }),
      (error) => error.code === "REVIEWER_LIFECYCLE_NOT_RECLAIMED",
    );

    // A running Review is refused under the reclaimed binding too, not only
    // under active: the daemon is read either way.
    fake.state.reviewerStatus = "running";
    await assert.rejects(
      execute("reviewer-cleanup-plan", agentOnly, { run: fake.runner }),
      (error) => error.code === "REVIEWER_LIFECYCLE_NOT_RECLAIMED",
    );

    // And a live workspace card under a reclaimed binding is refused on its own
    // terms, with the agent already archived so only that read can refuse.
    fake.state.reviewerStatus = "idle";
    fake.state.reviewerArchived = true;
    await assert.rejects(
      execute("reviewer-cleanup-plan", reclaimed, { run: fake.runner }),
      (error) => error.code === "REVIEWER_LIFECYCLE_NOT_RECLAIMED",
    );
    fake.state.reviewerArchived = false;

    // The frozen labels bind under the reclaimed path as well.
    fake.state.reviewerStatus = "idle";
    fake.state.reviewerArchived = true;
    fake.state.reviewerLabels = {
      "director.role": "reviewer",
      "director.task": TASK,
      "director.candidate": fixture.base,
    };
    await assert.rejects(
      execute("reviewer-cleanup-plan", reclaimed, { run: fake.runner }),
      (error) => error.code === "REVIEWER_IDENTITY_AMBIGUOUS",
    );

    // With the agent genuinely archived and its labels intact, the binding is
    // admitted and the workspace card is expected to be gone with it.
    fake.state.reviewerLabels = null;
    fake.state.workspaces = fake.state.workspaces.filter(
      (workspace) => workspace.workspaceId !== "reviewer-workspace-0001",
    );
    const plan = await execute("reviewer-cleanup-plan", reclaimed, { run: fake.runner });
    assert.equal(plan.result.resources.agent.archived, true);
    assert.equal(plan.result.resources.workspace.observation, "recorded_reclaimed");
  } finally {
    fixture.cleanup();
  }
});

test("a running Reviewer stays in flight even once its report is recorded", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, { reviewerArchived: false });
    fixture.reviewerCheckout = createReviewerCheckout(fixture, fake);
    recordReviewVerdict(fake, { candidate: fixture.candidate, base: fixture.base });
    fake.state.reviewerStatus = "running";
    fake.state.taskStatus = "closed";

    // A recorded report does not end a turn that the daemon says is still
    // running, so the survey must rank the daemon fact first.
    const survey = await execute("reviewer-survey", fixture.options, { run: fake.runner });
    const record = survey.result.reviewers.find((item) => item.agentId === "reviewer-0001");
    assert.equal(record.classification, "review_in_flight");
    assert.equal(record.reviewState, null);
    // And the leg refuses it outright.
    await assert.rejects(
      execute("reviewer-cleanup-plan", reviewerOptions(fixture), { run: fake.runner }),
      (error) => error.code === "REVIEWER_REVIEW_IN_FLIGHT",
    );
  } finally {
    fixture.cleanup();
  }
});

test("a Reviewer whose checkout was reaped is reconciled without an owner marker", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, { reviewerArchived: false });
    // The machine reaped the disposable checkout from /tmp. The agent and its
    // host view are still live, and there is no marker left to read.
    const reaped = join(fixture.root, "reaped-reviewer");
    fake.state.reviewerCheckout = reaped;
    fake.state.workspaces = [
      ...fake.state.workspaces,
      {
        workspaceId: "reviewer-workspace-0001",
        project: "Director",
        name: "Reviewer",
        isolation: "local",
        cwd: reaped,
      },
    ];
    recordReviewVerdict(fake, { candidate: fixture.candidate, base: fixture.base });
    fake.state.taskStatus = "closed";
    assert.equal(existsSync(reaped), false);

    const options = reviewerOptions(fixture, {
      "reviewer-checkout": reaped,
      "reviewer-checkout-state": "reclaimed",
    });
    // A path the agent never worked in is refused even though nothing would be
    // removed, so the plan cannot record a checkout never tied to its agent.
    await assert.rejects(
      execute(
        "reviewer-cleanup-plan",
        { ...options, "reviewer-checkout": join(fixture.root, "never-the-agents-cwd") },
        { run: fake.runner },
      ),
      (error) => error.code === "REVIEWER_CHECKOUT_OWNERSHIP_MISMATCH",
    );

    const plan = await execute("reviewer-cleanup-plan", options, { run: fake.runner });
    // The marker exists to authorise a removal. With nothing to remove it is
    // not required, and its absence is recorded as the reason rather than
    // standing in for a proof that was never taken.
    assert.equal(plan.result.resources.checkout.absent, true);
    assert.equal(plan.result.resources.checkout.source, "recorded_reclaimed");
    const planFile = join(fixture.root, "reaped-plan.json");
    writeFileSync(planFile, `${canonicalJson(plan)}\n`, { mode: 0o600 });

    const applied = await execute(
      "reviewer-cleanup-apply",
      { ...options, "plan-file": planFile },
      { run: fake.runner },
    );
    assert.equal(applied.result.resources, "complete");
    assert.equal(fake.state.reviewerArchived, true);
    const state = JSON.parse(readFileSync(options["state-file"], "utf8"));
    assert.equal(state.effects["cleanup.reviewer-agent"].phase, "complete");
    assert.equal(state.effects["cleanup.reviewer-workspace"].phase, "complete");
    assert.equal(state.effects["cleanup.reviewer-checkout"].phase, "complete");
    assert.equal(
      state.effects["cleanup.reviewer-checkout"].evidence.source,
      "recorded_reclaimed",
    );

    // A reaped checkout is still refused if the path came back as something
    // else: absence is bound, not assumed.
    mkdirSync(reaped, { recursive: true });
    await assert.rejects(
      execute("reviewer-cleanup-plan", options, { run: fake.runner }),
      (error) => error.code === "RECLAIMED_CHECKOUT_PRESENT",
    );
  } finally {
    fixture.cleanup();
  }
});

test("a transcription naming the Reviewer by a bare identifier is counted", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, { reviewerArchived: false });
    fixture.reviewerCheckout = createReviewerCheckout(fixture, fake);
    fake.state.taskStatus = "closed";
    // The live record holds a Reviewer referred to by bare UUID. Bound by
    // nothing and counted by nothing, it would be the one case where a miss is
    // invisible on both counters.
    recordComment(fake, {
      id: "transcript-bare-id",
      author: "paseo:coordinator-0001",
      text: `INDEPENDENT_REVIEW_VERDICT — ${TASK}\n\nReviewer reviewer-0001 returned verdict approve_candidate for Candidate ${fixture.candidate}.\n`,
    });
    await assert.rejects(
      execute("reviewer-cleanup-plan", reviewerOptions(fixture), { run: fake.runner }),
      (error) => error.code === "REVIEWER_VERDICT_NOT_DURABLE",
    );
    await assert.rejects(
      execute(
        "reviewer-cleanup-plan",
        reviewerOptions(fixture, { "reviewer-review-state": "abandoned" }),
        { run: fake.runner },
      ),
      (error) => {
        assert.equal(error.code, "REVIEWER_REPORT_EVIDENCE_UNRESOLVED");
        assert.equal(error.details.unboundEvidence, 1);
        return true;
      },
    );
  } finally {
    fixture.cleanup();
  }
});

test("a superseded Review on a live Task is reclaimable only once its coordinator records abandoning it", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, { reviewerArchived: false });
    fixture.reviewerCheckout = createReviewerCheckout(fixture, fake);
    // The deadlock: the Task is in progress because cleanup requires it, the
    // Reviewer never reported, and `abandoned` was refused for exactly that
    // reason. No ordering discharges all three, and it recurs for every
    // Candidate superseded after its Reviewer exists.
    assert.equal(fake.state.taskStatus, "in_progress");
    const options = reviewerOptions(fixture, { "reviewer-review-state": "abandoned" });
    await assert.rejects(
      execute("reviewer-cleanup-plan", options, { run: fake.runner }),
      (error) => error.code === "REVIEWER_REVIEW_IN_FLIGHT",
    );

    // A declaration by someone else does not discharge it.
    recordComment(fake, {
      id: "abandon-wrong-actor",
      author: "paseo:someone-else-0001",
      text: `REVIEW ABANDONED — paseo:${"reviewer-0001"} superseded.\n`,
    });
    await assert.rejects(
      execute("reviewer-cleanup-plan", options, { run: fake.runner }),
      (error) => error.code === "REVIEWER_REVIEW_IN_FLIGHT",
    );

    // Nor one that abandons a different Reviewer.
    recordComment(fake, {
      id: "abandon-other-reviewer",
      author: ACTOR,
      text: "REVIEW ABANDONED — paseo:reviewer-9999 superseded.\n",
    });
    await assert.rejects(
      execute("reviewer-cleanup-plan", options, { run: fake.runner }),
      (error) => error.code === "REVIEWER_REVIEW_IN_FLIGHT",
    );

    // Nor a coordinator comment that merely mentions an abandonment below the
    // first line, which is how it would be narrated rather than declared.
    recordComment(fake, {
      id: "abandon-narrated",
      author: ACTOR,
      text: `COORDINATOR — the Candidate was superseded.\n\nREVIEW ABANDONED for paseo:${"reviewer-0001"}.\n`,
    });
    await assert.rejects(
      execute("reviewer-cleanup-plan", options, { run: fake.runner }),
      (error) => error.code === "REVIEWER_REVIEW_IN_FLIGHT",
    );

    // Declared by this coordinator, on the first line, naming this Reviewer.
    recordComment(fake, {
      id: "abandon-0001",
      author: ACTOR,
      text: `REVIEW ABANDONED — paseo:${"reviewer-0001"} on a superseded Candidate.\n\nNo verdict was produced and none is awaited.\n`,
    });
    const plan = await execute("reviewer-cleanup-plan", options, { run: fake.runner });
    assert.equal(plan.result.resources.review.abandonedBy, ACTOR);
    assert.equal(plan.result.resources.review.taskStatus, "in_progress");

    // And the guard that made this hard is untouched: an agent mid-turn is
    // still refused, declaration or not.
    fake.state.reviewerStatus = "running";
    await assert.rejects(
      execute("reviewer-cleanup-plan", options, { run: fake.runner }),
      (error) => error.code === "REVIEWER_REVIEW_IN_FLIGHT",
    );
    fake.state.reviewerStatus = "idle";

    // Unresolved evidence still refuses, so a declaration cannot bind away a
    // report the rule could not read.
    recordComment(fake, {
      id: "unbound-transcript",
      author: "paseo:coordinator-0001",
      text: `INDEPENDENT_REVIEW_VERDICT — paseo:${"reviewer-0001"} returned verdict approve_candidate.\n`,
    });
    await assert.rejects(
      execute("reviewer-cleanup-plan", options, { run: fake.runner }),
      (error) => error.code === "REVIEWER_REPORT_EVIDENCE_UNRESOLVED",
    );
  } finally {
    fixture.cleanup();
  }
});

test("the survey shows only the binding the leg will accept", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, { reviewerArchived: false });
    fixture.reviewerCheckout = createReviewerCheckout(fixture, fake);
    fake.state.taskStatus = "closed";
    // The cell nothing previously reached: review_incomplete crossed with a
    // non-zero counter. It must display no binding, because the binding a
    // classification alone would suggest is the one the leg refuses, and the
    // operator acts on the display.
    recordComment(fake, {
      id: "unbound-transcript",
      author: "paseo:coordinator-0001",
      text: `INDEPENDENT_REVIEW_VERDICT — paseo:${"reviewer-0001"} returned verdict approve_candidate.\n`,
    });
    const survey = await execute("reviewer-survey", fixture.options, { run: fake.runner });
    const row = survey.result.reviewers.find((item) => item.agentId === "reviewer-0001");
    assert.equal(row.classification, "review_incomplete");
    assert.equal(row.unboundEvidence, 1);
    assert.equal(row.reviewState, null);
    // And the leg does refuse exactly that binding, so the record agrees with
    // the gate rather than contradicting it.
    await assert.rejects(
      execute(
        "reviewer-cleanup-plan",
        reviewerOptions(fixture, { "reviewer-review-state": "abandoned" }),
        { run: fake.runner },
      ),
      (error) => error.code === "REVIEWER_REPORT_EVIDENCE_UNRESOLVED",
    );

    // The same cell on a live Task with clean counters: no binding until the
    // coordinator declares one, and `abandoned` the moment it does.
    fake.state.beadsComments = fake.state.beadsComments.filter(
      (comment) => comment.id !== "unbound-transcript",
    );
    fake.state.taskStatus = "in_progress";
    const live = await execute("reviewer-survey", fixture.options, { run: fake.runner });
    assert.equal(
      live.result.reviewers.find((item) => item.agentId === "reviewer-0001").reviewState,
      null,
    );
    recordComment(fake, {
      id: "abandon-0001",
      author: ACTOR,
      text: `REVIEW ABANDONED — paseo:${"reviewer-0001"} on a superseded Candidate.\n`,
    });
    const declared = await execute("reviewer-survey", fixture.options, { run: fake.runner });
    const row2 = declared.result.reviewers.find((item) => item.agentId === "reviewer-0001");
    assert.equal(row2.reviewState, "abandoned");
    assert.equal(row2.abandonedBy, ACTOR);
  } finally {
    fixture.cleanup();
  }
});

test("the Reviewer survey derives the set to reconcile from the daemon and Beads", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, { reviewerArchived: false });
    fixture.reviewerCheckout = createReviewerCheckout(fixture, fake);
    recordReviewVerdict(fake, { candidate: fixture.candidate, base: fixture.base });
    fake.state.extraListedAgents = [
      {
        Id: "reviewer-closed-task",
        Name: "Review dir-m6.30",
        Status: "idle",
        Archived: false,
        ArchivedAt: null,
        Cwd: join(fixture.root, "closed-task-review"),
        ParentAgentId: null,
        Labels: {
          "director.role": "reviewer",
          "director.task": "dir-m6.30",
          "director.candidate": "a".repeat(40),
        },
      },
      {
        Id: "reviewer-running",
        Name: "Review dir-m6.35",
        Status: "running",
        Archived: false,
        ArchivedAt: null,
        Cwd: join(fixture.root, "running-review"),
        ParentAgentId: null,
        Labels: {
          "director.role": "reviewer",
          "director.task": "dir-m6.35",
          "director.candidate": "b".repeat(40),
        },
      },
      {
        // Idle between turns, on a Task still in progress, with nothing durable
        // recorded: this is the exact shape of a Review that is working.
        Id: "reviewer-idle-in-flight",
        Name: "Review dir-m6.35",
        Status: "idle",
        Archived: false,
        ArchivedAt: null,
        Cwd: join(fixture.root, "idle-in-flight-review"),
        ParentAgentId: null,
        Labels: {
          "director.role": "reviewer",
          "director.task": "dir-m6.35",
          "director.candidate": "c".repeat(40),
        },
      },
      {
        // The same shape once its Task is no longer in progress: a Review that
        // never reported, handled explicitly rather than assumed reconcilable.
        Id: "reviewer-never-reported",
        Name: "Review dir-m6.30",
        Status: "idle",
        Archived: false,
        ArchivedAt: null,
        Cwd: join(fixture.root, "never-reported-review"),
        ParentAgentId: null,
        Labels: {
          "director.role": "reviewer",
          "director.task": "dir-m6.30",
          "director.candidate": "d".repeat(40),
        },
      },
      {
        // No readable Task at all: the counters are unknowable rather than
        // zero, and the record must still carry both keys the contract
        // promises rather than omitting them.
        Id: "reviewer-taskless",
        Name: "Review of nothing",
        Status: "idle",
        Archived: false,
        ArchivedAt: null,
        Cwd: join(fixture.root, "taskless-review"),
        ParentAgentId: null,
        Labels: { "director.role": "reviewer", "director.candidate": "e".repeat(40) },
      },
      {
        Id: "reviewer-unlabelled",
        Name: "Review of nothing",
        Status: "idle",
        Archived: false,
        ArchivedAt: null,
        Cwd: join(fixture.root, "unlabelled-review"),
        ParentAgentId: null,
        Labels: { "director.role": "reviewer", "director.task": "dir-m6.30" },
      },
    ];
    fake.state.taskStatuses = { "dir-m6.30": "closed", "dir-m6.35": "in_progress" };
    fake.state.beadsComments = [
      ...fake.state.beadsComments,
      {
        id: "review-m630",
        issue_id: "dir-m6.30",
        author: "paseo:reviewer-closed-task",
        text: `INDEPENDENT REVIEW\nCandidate: ${"a".repeat(40)}\nBase: ${fixture.base}\nVerdict: changes_requested\n`,
        created_at: "2026-09-16T00:00:00Z",
      },
    ];

    const survey = await execute("reviewer-survey", fixture.options, {
      run: fake.runner,
    });
    const byId = new Map(
      survey.result.reviewers.map((record) => [record.agentId, record]),
    );
    assert.equal(survey.result.totals.reviewers, 7);
    // A superseded Candidate of a closed Task whose verdict is durable is
    // reconcilable; the Task Agent in the same list is not a Reviewer at all.
    assert.equal(byId.get("reviewer-closed-task").classification, "reconcilable");
    assert.equal(byId.get("reviewer-closed-task").reviewState, "verdict_recorded");
    assert.equal(byId.get("reviewer-0001").classification, "reconcilable");
    assert.equal(byId.get("reviewer-running").classification, "review_in_flight");
    assert.equal(byId.get("reviewer-idle-in-flight").classification, "review_in_flight");
    // A record that admits no binding suggests none, rather than describing a
    // Review that is still going as abandoned.
    assert.equal(byId.get("reviewer-idle-in-flight").reviewState, null);
    assert.equal(byId.get("reviewer-running").reviewState, null);
    assert.equal(byId.get("reviewer-unlabelled").reviewState, null);
    assert.equal(byId.get("reviewer-never-reported").classification, "review_incomplete");
    assert.equal(byId.get("reviewer-never-reported").reviewState, "abandoned");
    assert.equal(byId.get("reviewer-never-reported").reportSource, null);
    assert.equal(byId.get("reviewer-closed-task").reportSource, "authored");
    assert.equal(byId.get("reviewer-0001").reportSource, "authored");
    assert.equal(byId.get("reviewer-unlabelled").classification, "ambiguous");
    const taskless = byId.get("reviewer-taskless");
    assert.equal(taskless.classification, "ambiguous");
    assert.ok("unboundEvidence" in taskless && "unreadableReports" in taskless);
    assert.equal(taskless.unboundEvidence, null);
    assert.equal(taskless.unreadableReports, null);
    assert.equal(byId.has("agent-0001"), false);
    assert.equal(survey.result.enumeration.saturated, false);
    // An unsaturated page is not a complete enumeration: it still excludes
    // every agent last active before the window, and `complete` is the only
    // field that would license "and there are no others".
    assert.equal(survey.result.enumeration.windowHours, 720);
    assert.equal(survey.result.enumeration.complete, false);

    // The survey adopts and removes nothing.
    assert.equal(fake.state.reviewerArchiveDispatches, 0);
    assert.equal(existsSync(fixture.reviewerCheckout), true);

    // A page returned at its own limit cannot support a claim that the set is
    // complete, and the survey says so instead of reading as exhaustive.
    fake.state.agentListLimit = 8;
    const saturated = await execute("reviewer-survey", fixture.options, {
      run: fake.runner,
    });
    assert.equal(saturated.result.enumeration.saturated, true);
    assert.equal(saturated.result.enumeration.returned, 8);
  } finally {
    fixture.cleanup();
  }
});

test("Reviewer options are bound immutably and refused outside the Reviewer leg", async () => {
  const fixture = createRepositoryFixture();
  try {
    const fake = fakeExternalCommands(fixture, { reviewerArchived: false });
    fixture.reviewerCheckout = createReviewerCheckout(fixture, fake);
    recordReviewVerdict(fake, { candidate: fixture.candidate, base: fixture.base });
    await assert.rejects(
      execute(
        "snapshot",
        { ...fixture.options, "reviewer-agent-id": "reviewer-0001" },
        { run: fake.runner },
      ),
      (error) => error.code === "OPTION_INVALID",
    );
    const { "reviewer-checkout-state": _omitted, ...incomplete } =
      reviewerOptions(fixture);
    await assert.rejects(
      execute("reviewer-cleanup-plan", incomplete, { run: fake.runner }),
      (error) => error.code === "OPTION_REQUIRED",
    );
    await assert.rejects(
      execute(
        "reviewer-cleanup-plan",
        reviewerOptions(fixture, { "reviewer-checkout": fixture.control }),
        { run: fake.runner },
      ),
      (error) => error.code === "REVIEWER_CHECKOUT_NOT_DISPOSABLE",
    );
    await assert.rejects(
      execute(
        "reviewer-cleanup-plan",
        reviewerOptions(fixture, {
          "agent-id": "agent-0001",
          "workspace-id": "workspace-0001",
          "lifecycle-state": "active",
          "reviewer-agent-id": "agent-0001",
        }),
        { run: fake.runner },
      ),
      (error) => error.code === "REVIEWER_NOT_INDEPENDENT",
    );
    await assert.rejects(
      execute(
        "reviewer-cleanup-plan",
        reviewerOptions(fixture, {
          "agent-id": "agent-0001",
          "workspace-id": "workspace-0001",
          "lifecycle-state": "active",
          "reviewer-workspace-id": "workspace-0001",
        }),
        { run: fake.runner },
      ),
      (error) => error.code === "REVIEWER_NOT_INDEPENDENT",
    );

    // A plan admitted under one Reviewer binding is never admitted under
    // another.
    const options = reviewerOptions(fixture);
    const plan = await execute("reviewer-cleanup-plan", options, { run: fake.runner });
    const planFile = join(fixture.root, "bound-plan.json");
    writeFileSync(planFile, `${canonicalJson(plan)}\n`, { mode: 0o600 });
    fake.state.workspaces = [
      ...fake.state.workspaces,
      {
        workspaceId: "reviewer-workspace-0002",
        project: "Director",
        name: "Reviewer",
        isolation: "local",
        cwd: fixture.reviewerCheckout,
      },
    ];
    await assert.rejects(
      execute(
        "reviewer-cleanup-apply",
        {
          ...options,
          "reviewer-workspace-id": "reviewer-workspace-0002",
          "state-file": join(fixture.root, "other-binding-state.json"),
          "plan-file": planFile,
        },
        { run: fake.runner },
      ),
      (error) => error.code === "CLEANUP_PLAN_BINDING_MISMATCH",
    );
    assert.equal(existsSync(fixture.reviewerCheckout), true);
  } finally {
    fixture.cleanup();
  }
});
