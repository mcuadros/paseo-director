---
name: director-pull-request
description: Guide the Director Engine or authorized coordinator in publishing one reviewed paseo-director Candidate as an idempotent GitHub pull request with complete validation, risk, and rollback evidence. Task Agents must not use this skill to perform publication or integration.
---

# Director Pull Request

The Director Engine or authorized coordinator publishes one independently reviewed Candidate without duplicating branches or pull requests. A Task Agent, model, or connector may provide a structured claim but cannot execute this workflow.

## Preconditions

1. Confirm the caller is the Director Engine or authorized coordinator, then read the Beads Task and current branch state.
2. Require a clean worktree and an exact Candidate SHA.
3. Require independent approval for that same SHA.
4. Confirm the branch is not `main` and belongs to the Task.
5. Inspect the current remote base and invalidate/review again if the relevant base changed.
6. Require all local Task checks to pass.
7. Query GitHub for an existing open PR for the branch before creating one.

## Publish idempotently

- The authorized coordinator pushes only the owned Task branch.
- Never force-push a protected or target branch.
- Reuse the existing Task PR when present.
- Create at most one active PR per Task.
- Do not switch silently to direct delivery when GitHub is unavailable.

## Title and body

Use a concise Conventional-Commit-style title. Populate the repository PR template with:

- Summary;
- Motivation;
- Changes;
- Validation;
- Independent review and exact Candidate SHA;
- Risks and rollback;
- Beads Task;
- checklist.

Describe observable behavior and evidence. Do not paste full agent transcripts, secrets, or noisy command logs.

## After publication

1. As the authorized coordinator, record the PR URL/number and Candidate SHA in Beads.
2. Observe CI without rerunning the same failed commit automatically.
3. Route human review feedback back to the same top-level Task Agent as an authorized correction turn; the Task Agent returns a new structured Candidate/outcome claim.
4. Do not have agents converse in PR threads or automatically resolve human threads.
5. Immediately before integration, refetch the PR and prove that its remote head is the approved Candidate, its relevant base is still valid, every configured and Task-required check passes, it is mergeable, and no human feedback remains unresolved.
6. As the Director Engine or authorized coordinator, merge automatically when those facts hold unless the Task explicitly requires manual integration, and atomically bind the operation to the approved head with `gh pr merge --match-head-commit <approved-sha>` or an equivalent expected-head precondition.
7. Treat no remote checks as valid only when the Task does not require them and the repository has not configured them yet.
8. Ask for human input only for accepted P2 risk, policy/permission expansion, an ambiguous or manual gate, public-history rewrite, or a destructive action outside approved Task cleanup.
9. As the Director Engine or authorized coordinator, perform and verify owned lifecycle cleanup, then close the Beads Task only after integration and every closure fact are verified.

A refetch followed by an unguarded merge has a race window and is forbidden. If the forge cannot atomically reject a changed head, stop without merging.
