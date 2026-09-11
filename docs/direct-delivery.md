# Direct delivery

Direct delivery integrates one admitted Candidate into its configured remote
target branch without creating, updating, or using a pull request. The
Director Engine owns the policy and every transition. The Git adapter can only
observe one exact ref or perform one authorized conditional push.

## Admission

The engine records a direct-delivery state only when all of these facts are
current and bound to the same Candidate generation:

- the frozen Run selected `deliveryMode=direct` from its approved
  configuration; there is no PR-failure selection source;
- the Task, Run, repository, Candidate, base, tree, manifest, configuration,
  target ref, and Project lease identities match;
- the independent Review verdict is `approve_candidate` from the exact
  Reviewer UUID;
- the one authoritative complete Linux CI observation is for the same
  Candidate/base/tree/manifest and passed;
- no correction turn is active and any corrected Candidate has its fresh
  Review and CI gates;
- the target is an explicitly authorized full `refs/heads/...` ref; and
- no PR-publication authority or prior integration authority exists.

A PR publication failure, unavailable GitHub observation, or missing PR fact
cannot satisfy these predicates and cannot create a direct-delivery intent.
Changing delivery authority requires a separately approved configuration; it
is never retry or fallback behavior.

## Manual and automatic behavior

Manual direct delivery persists `waiting_human` with no Git effect. Only a
server-attributed, authenticated Paseo `direct integration` action bound to the
exact Candidate, base, target, policy, and direct-delivery identity records the
integration intent. A GitHub review/comment identity or ordinary Paseo feedback
record cannot be converted into that authorization. A restart,
ready projection, model claim, or adapter result cannot substitute for that
action.

Automatic direct delivery records the same integration intent immediately,
but only when the target also appears in the frozen automatic-target set. An
unknown mode, unsealed policy, unauthorized target, or fallback-shaped source
fails before mutation.

## Effect and recovery

`direct.integration` is an ADR-0015 `conditional_update` effect:

1. Persist the immutable direct intent and exact bindings.
2. Re-observe the complete local Candidate/repository identity and the exact
   live remote target ref.
3. Persist that bounded, credential-free observation.
4. CAS the Run to `dispatching`, consuming the observation and one bounded
   attempt.
5. Push exactly `Candidate:target` with
   `--force-with-lease=target:base`. Only one ref is selected.
6. Persist `observation_required`; a lost response may leave `dispatching`.
7. Re-observe after success, response loss, or restart. Only the exact
   Candidate at the target completes integration.

An exact base still at the target may authorize a bounded retry after a fresh
observation. Candidate-at-target is adopted without another push. A changed or
absent target invalidates readiness and requires fresh Candidate/Review/CI
facts. An unavailable remote waits with backoff; it is never treated as
absence. Ambiguous repository, branch, Candidate, parsing, or identity facts
park without mutation.

The Project lease and Run expected version fence stale engines and concurrent
coordinators. The remote ref lease is the external compare-and-swap boundary,
so concurrent Candidates based on one old base cannot both update the target.
Persisted events and errors contain only closed codes, hashes, object IDs, and
bounded identities—never raw Git output, paths, remote URLs, or credentials.

Correction to a new Candidate moves the old direct state into invalidated
history and clears all old downstream authority. Cleanup, PR merge, Task
closure, and delivery-mode changes remain separate engine/coordinator work.
Unresolved current human feedback applies the same pre-dispatch invalidation:
an in-flight handoff must first be observed, while a not-yet-dispatched direct
state moves to immutable history and cannot resume until a fresh Candidate,
Review, and CI binding exists.
