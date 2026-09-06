# Repository instructions

All repository content, code, issues, commits, pull requests, and review output must be in English.

## Source of truth

1. Read `docs/PLAN.md` before working.
2. Treat its invariants, milestone gates, and scope boundaries as normative.
3. Use ADRs in `docs/adr/` for an explicitly approved change to a plan decision.
4. Never start M1 product work while an M0 stop condition remains unresolved.

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
- Use `skills/director-pull-request/SKILL.md` before publishing a PR.

## Safety and delivery

- One Task, branch, and worktree per primary agent.
- Never review a dirty worktree or a moving branch instead of an exact SHA.
- Never self-review or treat author assertions as review evidence.
- Never push directly to `main` for Task implementation.
- Never bypass CI, hooks, plan gates, or independent review.
- Preserve unrelated/user-owned changes.
- Never use undocumented Paseo internals as product infrastructure.
- Stop on destructive ambiguity, wrong-repository risk, secrets, or possible data loss.
- Leave the worktree clean at handoff.

## File policy

- Keep generated artifacts, credentials, local databases, logs, and temporary evidence out of Git.
- Keep skills concise and procedural; put durable design rationale in ADRs.
- Update documentation and tests whenever behavior or contracts change.
