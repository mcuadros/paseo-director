---
name: director-pull-request
description: Publish one reviewed paseo-director Beads Task as an idempotent GitHub pull request with complete validation, risk, and rollback evidence. Use when a candidate is independently approved and ready to push, open, update, or hand off as a PR.
---

# Director Pull Request

Publish one independently reviewed Candidate without duplicating branches or pull requests.

## Preconditions

1. Read the Beads Task and current branch state.
2. Require a clean worktree and an exact Candidate SHA.
3. Require independent approval for that same SHA.
4. Confirm the branch is not `main` and belongs to the Task.
5. Inspect the current remote base and invalidate/review again if the relevant base changed.
6. Require all local Task checks to pass.
7. Query GitHub for an existing open PR for the branch before creating one.

## Publish idempotently

- Push only the owned Task branch.
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

1. Record the PR URL/number and Candidate SHA in Beads.
2. Observe CI without rerunning the same failed commit automatically.
3. Route human review feedback back to the primary Task owner.
4. Do not have agents converse in PR threads or automatically resolve human threads.
5. Immediately before integration, refetch the PR and prove that its remote head is the approved Candidate, its relevant base is still valid, every configured and Task-required check passes, it is mergeable, and no human feedback remains unresolved.
6. Merge automatically when those facts hold unless the Task explicitly requires manual integration.
7. Treat no remote checks as valid only when the Task does not require them and the repository has not configured them yet.
8. Ask for human input only for accepted P2 risk, policy/permission expansion, an ambiguous or manual gate, public-history rewrite, or a destructive action outside approved Task cleanup.
9. Close the Beads Task only after integration and cleanup are verified.
