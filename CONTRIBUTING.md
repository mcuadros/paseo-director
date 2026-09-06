# Contributing

Director is developed through dependency-ordered Beads Tasks and exact-commit independent review. The approved scope and architecture live in [docs/PLAN.md](docs/PLAN.md).

## Before starting

1. Run `bd prime` and `bd ready --json`.
2. Select one unblocked Task from the active milestone.
3. Read the complete issue, acceptance criteria, blockers, plan sections, and ADRs.
4. Claim it atomically with `bd update <id> --claim --json`.
5. Create an isolated worktree and branch named `task/<beads-id>-<short-slug>`.

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
- Route corrections back to the same primary Task owner.
- Review a changed SHA again from the beginning.

## Pull requests

Follow `skills/director-pull-request/SKILL.md` and the repository template.

- Open at most one active PR per Task.
- Publish only an independently approved Candidate.
- Include test evidence, review SHA, risks, rollback, and Beads ID.
- Do not use PR comments for agent-to-agent conversation.
- Do not merge while CI, review, base, or acceptance evidence is stale.

## M0 spikes

Follow `skills/director-spike/SKILL.md` and use `docs/adr/0000-template.md`.

M0 exists to reject unsafe assumptions. A no-go result is valuable when it prevents a dependency on private APIs or fragile behavior.

## Definition of Done

A Task is complete only after its acceptance criteria, tests, exact-SHA independent review, CI, integration, Beads evidence, and cleanup are all verified. See section 24 of the plan for the complete policy.
