# Contributing

Director is developed through dependency-ordered Beads Tasks and exact-commit independent review. The approved scope and architecture live in [docs/PLAN.md](docs/PLAN.md).

Each development Task is owned by one normal top-level Paseo Task Agent whose parent field is omitted and whose visible title exactly matches the Task title. It runs in the Task's isolated Execution Workspace/worktree. The Task Agent may create internal helper subagents, but helpers are not Beads Tasks or Task owners. Independent Reviewer Agents are also top-level agents and never children of the Task Agent or Organizer.

## Before starting

1. Run `bd prime` and `bd ready --json`.
2. Select one unblocked Task from the active milestone.
3. Read the complete issue, acceptance criteria, blockers, plan sections, and ADRs.
4. Claim it atomically with `bd update <id> --claim --json`.
5. Work only in the Task's isolated Execution Workspace/worktree and branch named `task/<beads-id>-<short-slug>`.

Do not implement work from a later milestone or expand a Task silently. Record discovered work as a separate sibling Task.

## Commits

Follow `skills/director-commit/SKILL.md`.

Use Conventional Commits and include the owning Task:

```text
feat(scope): add concise behavior

Explain why when the rationale is not obvious.

Beads: dir-xxxx
```

Each commit must be focused, tested, free of secrets, and independently reviewable.

## Independent review

Every implementation commit requires review under `skills/director-independent-review/SKILL.md`.

- Review the exact SHA in a detached disposable checkout.
- Do not give the reviewer the author's hidden conclusions or conversation.
- Route corrections back to the same Task Agent.
- Review a changed SHA again from the beginning.
- Treat `approve_candidate` as permission to publish the reviewed SHA, not permission to close the Task.
- Keep push, PR, CI, integration, synchronization, and cleanup as explicit post-review gates.

## Pull requests

Follow `skills/director-pull-request/SKILL.md` and the repository template.

- Open at most one active PR per Task.
- Publish only an independently approved Candidate.
- Include test evidence, review SHA, risks, rollback, and Beads ID.
- Do not use PR comments for agent-to-agent conversation.
- Do not merge while CI, review, base, or acceptance evidence is stale.

After `approve_candidate`, the Task Agent publishes and merges automatically when the reviewed head, relevant base, configured/required checks, mergeability, and human-feedback gates remain valid. The merge command must atomically match the approved head SHA; a pre-merge refetch is not enough. A separate human merge confirmation is not required unless the Task is explicitly manual or one of the risk, policy, ambiguity, history-rewrite, or unauthorized-destructive exceptions in `AGENTS.md` applies.

## M0 spikes

Follow `skills/director-spike/SKILL.md` and use `docs/adr/0000-template.md`.

M0 exists to reject unsafe assumptions. A no-go result is valuable when it prevents a dependency on private APIs or fragile behavior.

## ADR lifecycle

ADR status is normative and uses exactly these values:

- `Proposed`: the decision has not yet completed exact-SHA independent review and integration. It is not an approved plan change or a satisfied milestone gate.
- `Accepted`: an exact Candidate containing the substantively identical decision received `approve_candidate` and was integrated with its reviewed base. A later status-only reconciliation may rely on that integrated evidence, but the reconciliation must itself follow the normal review and integration workflow.
- `Superseded`: a later Accepted ADR replaces the decision. The earlier evidence and analysis remain historical, but its Decision is no longer normative.

Decision outcome and lifecycle status are separate. An Accepted `No-go` is a valid approved decision; an `Inconclusive` outcome cannot satisfy a gate that requires a resolved decision.

A partial change keeps the earlier ADR Accepted and uses reciprocal `Amends` / `Amended by` links. A complete replacement marks the earlier ADR Superseded and uses reciprocal `Supersedes` / `Superseded by` links. These relationships describe normative precedence, not the lines touched by a commit. Preserve the earlier Decision text, adding a prominent resolution note when needed, and put the new decision in the later ADR.

The owning Beads Task records the exact Candidate, reviewed base, independent verdict, integration commit, checks, and residual risks. An author assertion or header edit alone never accepts an ADR.

## Definition of Done

A Task is complete only after its acceptance criteria, tests, exact-SHA independent review, CI, integration, Beads evidence, and cleanup are all verified. See section 24 of the plan for the complete policy.
