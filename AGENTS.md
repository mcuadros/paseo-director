# Repository instructions

All repository content, code, issues, commits, pull requests, and review output must be in English.

## Source of truth

1. Read `docs/PLAN.md` before working.
2. Treat its invariants, milestone gates, and scope boundaries as normative.
3. Use ADRs in `docs/adr/` for an explicitly approved change to a plan decision.
4. Never start M1 product work while an M0 stop condition remains unresolved.

## Decision and verification discipline

- A human decision recorded on a Task binds Task Agents and reviewers. Neither may return or approve an outcome that reverses it. If evidence blocks the decided path, report `Inconclusive`, name the exact obstacle, leave dependent work blocked, and escalate to the project owner. An ADR outcome label governs only the question delegated to that ADR and cannot override an existing owner decision.
- Coordinators report verified observations, not merely attempted or purportedly applied actions. Distinguish `attempted`, `applied`, and `verified` explicitly, and reread the authoritative source and current visible state before reporting a result as done.

## Beads workflow

This repository uses Beads with issue prefix `dir`, a two-level `Epic → Task` hierarchy, and a shared Dolt server.

At the start of every task:

1. Run `bd prime`.
2. Run `bd show <task-id> --json` and inspect blockers.
3. Claim exactly one ready Task with `bd update <task-id> --claim --json`.
4. Work only within its acceptance criteria.

Create discovered work as a sibling Task under the same milestone Epic and link it with `discovered-from`. Never create a child beneath a Task.

Do not edit Beads storage directly. Use `bd` commands. Use `--json` for programmatic reads and writes. At handoff, record commit SHA, tests, review, PR, risks, and cleanup in the Task.

Beads remains in `no-git-ops` mode: `bd` must not stage, commit, or push repository files automatically. The Git operations explicitly required by the repository skills below are performed separately and remain auditable.

## Required skills

- Use `skills/director-task-workflow/SKILL.md` for every implementation, documentation, or maintenance Task.
- Use `skills/director-spike/SKILL.md` for M0 evidence work.
- Use `skills/director-commit/SKILL.md` before every commit.
- Use `skills/director-independent-review/SKILL.md` for review of the exact Candidate SHA.
- The Director Engine or authorized coordinator uses `skills/director-pull-request/SKILL.md` before publishing a PR; Task Agents do not invoke it.

## Safety and delivery

- Each Task is owned by one normal top-level Paseo Task Agent in its isolated Execution Workspace/worktree. Its agent-creation parent is omitted and its visible title is the exact Task title.
- A Task Agent may use internal helper subagents, but they are not Task records or Task owners and must not claim separate Beads work. The Task Agent remains solely accountable for its Task work, branch, Candidate, evidence, structured outcome claim, and authorized correction turns.
- Independent Reviewer Agents are normal top-level agents in detached disposable checkouts, never children or helpers of the Task Agent or any planning context.
- Never review a dirty worktree or a moving branch instead of an exact SHA.
- Never self-review or treat author assertions as review evidence.
- Never push directly to `main` for Task implementation.
- Never bypass CI, hooks, plan gates, or independent review.
- Preserve unrelated/user-owned changes.
- Never use undocumented Paseo internals as product infrastructure.
- Stop on destructive ambiguity, wrong-repository risk, secrets, or possible data loss.
- Leave the worktree clean at handoff.

## Integration policy

A Task Agent produces structured Candidate/outcome claims and may perform correction turns only when authorized. It cannot decide or perform publication, pull-request creation or update, integration, lifecycle cleanup, or Task closure. Only the Director Engine or authorized coordinator reconciles and executes those lifecycle effects.

An `approve_candidate` verdict authorizes the Director Engine or authorized coordinator—not the Task Agent, a model, or a connector—to publish and integrate that exact SHA automatically when all of these remain true immediately before merge:

- the remote head is the independently reviewed Candidate;
- the merge operation atomically asserts that exact head SHA, for example with `gh pr merge --match-head-commit <approved-sha>`;
- the relevant base has not changed, or the Candidate has been revalidated and reviewed against it;
- every configured and Task-required check passes;
- the PR is mergeable and no human feedback remains unresolved;
- the Task does not explicitly require manual integration.

The absence of remote checks is acceptable only when the Task does not require them and the repository has not configured them yet. The Director Engine or authorized coordinator reconfirms every gate from GitHub rather than relying on cached output.

A pre-merge refetch alone is insufficient because the head can change afterward. If the forge cannot atomically reject a changed head, stop rather than merge.

The Director Engine or authorized coordinator asks for human input only to accept P2 residual risk, expand policy/permissions, resolve an ambiguous or explicitly manual gate, rewrite public history, or perform a destructive action not already authorized by the Task and cleanup policy.

## File policy

- Keep generated artifacts, credentials, local databases, logs, and temporary evidence out of Git.
- Keep skills concise and procedural; put durable design rationale in ADRs.
- Update documentation and tests whenever behavior or contracts change.
