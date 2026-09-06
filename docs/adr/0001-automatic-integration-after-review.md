# ADR-0001: Automatically integrate independently approved Candidates

- **Status:** Accepted
- **Date:** 2026-09-06
- **Beads Task:** `dir-m0.13`
- **Plan gate:** M0 governance
- **Decision owner:** Human project owner

## Context

The initial repository workflow required an extra human confirmation after an exact-SHA independent review. The project owner explicitly decided that this repeated confirmation adds no useful gate when review, base, CI, mergeability, and feedback facts are already valid.

This decision governs development of Director itself. It does not change the Director product's configurable delivery behavior or its default manual merge policy.

## Decision

After an independent reviewer returns `approve_candidate`, the primary agent may publish and integrate that exact Candidate without another human confirmation when, immediately before merge:

- the remote head equals the reviewed SHA;
- the merge operation atomically asserts that exact SHA, using `gh pr merge --match-head-commit <approved-sha>` or an equivalent expected-head precondition;
- the relevant base is unchanged or the Candidate was freshly revalidated and reviewed;
- every configured and Task-required check passes;
- the PR is mergeable;
- no human feedback remains unresolved;
- the Task does not explicitly require manual integration.

No configured remote checks is acceptable only before the repository defines them and only when the Task does not require remote CI.

A pre-merge refetch alone is not sufficient because the head may change between observation and integration. If the forge cannot atomically reject a different head, automatic integration stops.

Human input remains mandatory for:

- accepting a P2 residual risk;
- expanding policy, permissions, repository, or delivery authority;
- an ambiguous or explicitly manual gate;
- rewriting public history;
- destructive actions outside approved Task and cleanup scope.

## Alternatives considered

### Require confirmation for every merge

This duplicates the independent-review gate and repeatedly interrupts safe, routine delivery.

### Merge immediately after review without refetching facts

Rejected because the branch, base, CI, mergeability, or feedback state may change after review.

## Consequences

- Routine reviewed work can complete without waiting for another human message.
- The primary remains responsible for refetching and proving all remote facts immediately before integration.
- Any Candidate or relevant base change invalidates the previous approval.
- Missing or ambiguous evidence stops integration rather than assuming success.
- Task closure still waits for post-merge verification and cleanup.

## Independent verification

Record the exact Candidate SHA, reviewer verdict, PR facts, merge commit, and post-merge verification in `dir-m0.13`.
