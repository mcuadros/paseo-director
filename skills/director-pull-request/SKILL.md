---
name: director-pull-request
description: Guide the Director Engine or authorized coordinator in publishing one reviewed paseo-director Candidate as an idempotent GitHub pull request with complete validation, risk, and rollback evidence. Task Agents must not use this skill to perform publication or integration.
---

# Director Pull Request

The Director Engine or authorized coordinator publishes one independently reviewed Candidate without duplicating branches or pull requests. A Task Agent, model, or connector may provide a structured claim but cannot execute this workflow.

## Preconditions

1. Confirm the caller is the Director Engine or authorized coordinator, then read the Beads Task and current branch state.
2. Require either the exact clean Task worktree or a verified reclaimed path/
   registration with the exact local Task ref at the Candidate. Never recreate
   a checkout or change prior lifecycle IDs merely to satisfy the tool.
3. Require independent approval for that same SHA plus the matching versioned
   review-harness result. Verify that its complete-CI attempt count and any
   second-run reason obey the owner-approved latency contract.
4. Confirm the branch is not `main` and belongs to the Task.
5. Inspect the current remote base and invalidate/review again if the relevant base changed.
6. Require all local Task checks to pass.
7. Query GitHub for an existing open PR for the branch before creating one.

## Publish idempotently

- Use `director-coordinator publish` with the exact review/validation files,
  review-handoff manifest, expected remote head, repository database ID,
  ownership token, and one external durable state file. The manifest routes
  reconciliation but never substitutes for the independent verdict or direct
  gate reads.
- The command pushes only the owned Task branch with an exact lease, never a
  protected or target branch.
- Its public marker binds Task, branch, and the ownership-label SHA-256, never
  raw ownership input, Candidate, or base. A corrected Candidate updates the
  same branch and adopts the same open PR; closed historical PRs do not block a
  new owned PR.
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

1. As the authorized coordinator, record the structured `publish` result in Beads under the explicit coordinator actor.
2. Run `director-coordinator gate` with every configured and Task-required
   check. Observe CI without rerunning the same failed commit automatically.
3. Route human review feedback back to the same top-level Task Agent as an authorized correction turn; the Task Agent returns a new structured Candidate/outcome claim.
4. Do not have agents converse in PR threads or automatically resolve human threads.
5. Run `director-coordinator integrate`; it repeats the complete gate immediately before integration, refuses a missing expected-head primitive, atomically binds merge to the approved head, and verifies the exact merge parents/tree afterward.
6. Never replace that command with a refetch followed by an unguarded merge.
7. Treat no remote checks as valid only when the Task does not require them and the repository has not configured them yet.
8. Ask for human input only for accepted P2 risk, policy/permission expansion, an ambiguous or manual gate, public-history rewrite, or a destructive action outside approved Task cleanup.
9. As the Director Engine or authorized coordinator, write the structured
   `cleanup-plan` output to the private control area, inspect it, then run
   `cleanup-apply` with that exact hashed plan and state file. Close the Beads
   Task only after the structured cleanup result and every closure fact are
   verified.

Every publication and gate re-reads binding human decisions and prior rejected
review references from Beads. Any changed set invalidates the manifest and
stops. If a merge completed across an undetectable base race, preserve the
`needs_manual_reconciliation` state and all resources, block cleanup/closure,
and request the exact owner decision; never narrate the merge as verified.

A refetch followed by an unguarded merge has a race window and is forbidden. If the forge cannot atomically reject a changed head, stop without merging.
