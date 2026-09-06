---
name: director-task-workflow
description: Execute one implementation, documentation, or maintenance Task as its top-level Paseo Task Agent through Beads, isolated Git work, validation, independent review, and pull-request handoff. Use whenever a Task Agent is asked to claim, implement, continue, hand off, or complete a Beads Task in this repository.
---

# Director Task Workflow

Deliver exactly one Beads Task without crossing its approved milestone gate.

## Establish the task

1. Read `docs/PLAN.md`, `AGENTS.md`, relevant ADRs, and the complete Beads issue.
2. Run `bd show <id> --json` and inspect blockers before changing files.
3. Require a parent milestone Epic, a non-empty description, and explicit acceptance criteria.
4. Claim atomically with `bd update <id> --claim --json`.
5. Stop if the Task is blocked, belongs to a later milestone, or requires an unresolved M0 decision.

## Isolate the work

1. Operate as the Task's one normal top-level Paseo Task Agent in its isolated Execution Workspace/worktree. The Task Agent has no parent agent and its visible title is the exact Task title.
2. Name the branch `task/<beads-id>-<short-slug>` unless an approved ADR or Task says otherwise.
3. Start from the current remote base recorded by the Task.
4. Preserve user changes and unrelated work. Never repurpose another Task's worktree.
5. Keep Beads connected to the repository's shared-server database.
6. Treat internal helper subagents as optional assistance only; do not let them become Task records or owners, claim sibling work, or change the Task Agent's accountability.

## Execute the scope

1. Restate the acceptance criteria as verifiable checks.
2. Inspect existing code and tests before editing.
3. Make the smallest cohesive change that satisfies the Task.
4. Add or update tests in proportion to risk.
5. Update documentation, schemas, fixtures, and migrations when behavior changes.
6. Record material discoveries in Beads rather than expanding scope silently.

When new work is discovered, create a sibling Task under the same milestone Epic and link it with `discovered-from`. Do not create a child of a Task; repository hierarchy is limited to `Epic → Task`.

## Validate and deliver

1. Run focused checks while iterating and the full Task-required checks before handoff.
2. Use `$director-commit` to create focused commits.
3. Require `$director-independent-review` to approve the exact final Candidate SHA.
4. Address every blocking finding through the same Task Agent and obtain a fresh review for a changed SHA.
5. Use `$director-pull-request` only after independent approval.
6. After `approve_candidate`, the Task Agent publishes and integrates automatically when the exact remote head, relevant base, configured/required checks, mergeability, and feedback gates remain valid, the merge operation atomically matches the approved head SHA, and the Task is not explicitly manual.
7. Update Beads with commit SHA, validation evidence, review verdict, PR link, integration evidence, and residual risks.
8. Close the Task only after integration is verified and owned temporary resources are clean.

Candidate approval is not Task completion. Keep every reviewer-listed post-review gate pending until push, PR, CI, integration, synchronization, and cleanup evidence exists.

## Stop safely

Stop and update Beads instead of improvising when:

- an acceptance criterion is ambiguous;
- a public Paseo capability is absent;
- a plan invariant would be weakened;
- a secret, data-loss risk, wrong-repository risk, or destructive ambiguity appears;
- required tests cannot run;
- the worktree is dirty for reasons outside the Task;
- implementation needs work from a later milestone.

Never self-approve, bypass hooks or required CI, force-push a protected branch, push directly to `main`, or hide incomplete work behind a passing status.
