# ADR-0021: Recover exact-leased Task-ref cleanup

- **Status:** Accepted
- **Date:** 2026-09-12
- **Beads Task:** `dir-m5.17`
- **Decision owner:** Project owner through the binding `dir-m5.17` Task decision
- **Amends:** PLAN §§4, 6.3, and 15.5;
  [ADR-0007](0007-git-worktree-ownership-and-cleanup.md) and
  [ADR-0015](0015-idempotent-command-effect-contract.md) only for exact local
  and remote Task-ref deletion recovery
- **Aligned with:** [ADR-0006](0006-exact-sha-github-delivery.md),
  [ADR-0018](0018-deterministic-coordination-boundary.md), and
  [ADR-0019](0019-event-driven-delivery-fast-path.md)

## Context

The `dir-m5.14` coordinator cleanup persisted the schema-v2
`cleanup.remote-branch` effect as `dispatching`, then its immediate
authoritative `ls-remote` observation failed with a transient GnuTLS error.
No deletion was dispatched or proven. The exact remote Task ref remained at
the immutable Candidate, but restart refused it as
`DESTRUCTIVE_TARGET_PRESENT_AFTER_HANDOFF`. The local Task ref remained behind
it and could not progress.

ADR-0007 and ADR-0015 deliberately treated every present destructive target
after possible handoff as ambiguous. That rule is still required for
worktrees, recovery artifacts, and any target without an atomic exact-value
compare primitive. It is unnecessarily strict for the two Task-ref deletion
operations: both can freshly prove the ref's current full object ID and ask
Git to delete only if it still has that exact value.

## Decision

A nonterminal possible-handoff local or remote Task-ref deletion (`dispatching`
or `unknown` in coordinator state; `dispatching` or `observation_required` in
the Engine aggregate) may create a new bounded attempt only when all existing
cleanup admission, integration, repository, ownership, no-consumer,
state-lock, and plan-binding checks remain valid and a fresh authoritative
observation proves the exact ref currently equals the immutable Candidate.

The retry preserves intent before mutation and must use the original atomic
compare-delete operation unchanged:

- remote: `git push --force-with-lease=<ref>:<Candidate> <remote> :<ref>`;
- local: `git update-ref -d <ref> <Candidate>`.

The coordinator observes the target again after persisting the new
`dispatching` attempt and before invoking that command. A ref which changes in
either observation window is preserved by the observation check or the Git
compare guard. Authentication, transport, timeout, parse, multiple-row, or
other unavailable/ambiguous observations never mean absence and never
authorize mutation. Unavailable observations remain waiting facts and are
refreshed by a later reconciler after ordinary backoff; they are not permanent
terminal state.

Authoritative absence completes a previously recorded deletion intent without
another mutation. A different value fails closed. `complete` remains terminal:
a later ref reappearance, including the same Candidate, is preserved as drift
and is not deleted again.

This amendment does not apply to worktree removal, recovery-artifact removal,
recovery-ref expiry, diagnostic-bundle expiry, or a future destructive effect
without an equally exact atomic compare primitive. Those effects retain the
ADR-0015 present-after-possible-handoff refusal.

## Consequences

- A transient observation failure between durable intent and ref deletion no
  longer strands an integrated Task cleanup.
- A response lost before ref mutation can converge by one new exact-leased
  attempt; a response lost after mutation converges by authoritative absence.
- A same-Candidate ref still present in a nonterminal attempt is treated as
  the immutable cleanup target, not as proof that the previous process handed
  off or did not hand off.
- Schema-v2 state and cleanup-plan bindings do not change. Raw ownership input
  remains confined to the mode-`0600` ownership file; durable state, plans,
  diagnostics, locks, command arguments, and public evidence retain only its
  SHA-256 where required.
- Maintained adversarial tests must cover pre-observation failure,
  `dispatching` and `unknown` restart, response loss before and after mutation,
  local/remote symmetry, exact-guard races, unavailable/ambiguous observation,
  terminal reappearance, bounded diagnostics, and the unchanged worktree
  refusal.
