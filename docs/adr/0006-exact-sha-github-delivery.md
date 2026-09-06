# ADR-0006: Bind GitHub delivery to live exact Candidate and base SHAs

- **Status:** Proposed
- **Date:** 2026-09-06
- **Beads Task:** `dir-m0.7`
- **Plan gate:** M0 exact-SHA GitHub delivery
- **Decision owner:** `dir-m0.7` Task Agent

## Context

Director must not push, validate, publish, merge, or clean a different revision
from the immutable Candidate that was reviewed. It must also invalidate
readiness when the relevant base changes, reconcile an unknown effect result
before retry, create at most one pull request, and delete only exact owned
remote refs after verified integration.

GitHub exposes several related but non-equivalent facts: Git refs, pull-request
head/base projections, check-run head SHAs, review commit IDs, merged state, and
merge commit SHAs. A cached or synthetic projection can be internally
consistent while no longer matching the live branch. The delivery contract
therefore needs an explicit authority and freshness rule for every effect.

The public plugin repository cannot be a destructive test target. The human
project owner authorized one private sandbox, exact repository database ID
`1359322331`, for this Task and required it to remain available through
independent review.

## Question or hypothesis

On the Linux-only Director 1.0 topology, using supported Git `2.47.3`, GitHub
CLI `2.97.0`, and REST API version `2022-11-28`, can an engine bind owned-branch
push, one PR, checks, human review feedback, base movement, merge, retry, and
remote cleanup to immutable Candidate/base SHAs without duplicating an effect
or mutating a stale/unowned ref?

## Acceptance criteria

- The effect sequence verifies the canonical repository identity, every
  mutation explicitly selects it, and each ref mutation verifies the exact
  expected OID immediately before dispatch.
- Same-intent push, PR, review-feedback, merge, and delete retries reconcile
  authoritative external state before deciding whether to dispatch.
- PR lookup cannot alias a different repository, head, base, owner, or durable
  intent marker.
- Check and human-review observations retain their exact Candidate SHA.
- Live base movement invalidates the old Candidate/base readiness tuple.
- A stale expected-head merge is atomically rejected, and a current
  expected-head merge occurs at most once.
- Integration proof uses merged state and the exact remote Git graph rather
  than a textual claim or one ambiguous GitHub field.
- Cleanup accepts exact absence, refuses a changed ref without dispatch, and
  removes only exact run-owned refs.
- Both successful and failed experiments leave no open PR, run branch, local
  clone, credential artifact, or persistent process.

## Evidence

The harness, exact commands and versions, safe-failure record, aggregate
output, primary contracts, live GitHub identifiers, and cleanup proof are in
[`docs/evidence/m0.7/README.md`](../evidence/m0.7/README.md).

The successful private-sandbox run recorded:

- Base 1 `4aa02c6282987e798bce6a9c944ebaae1e7bd971` and Candidate 1
  `a30da7c1f8b90021c0302265151aa53f7af634c2`;
- Base 2 `134bc4b30ac651114fffd9393bae0299627d6b18` and Candidate 2
  `2411bb4e6f83ac32f8db8da62222f67e66689439`;
- a PR base projection that still reported Base 1 immediately after the live
  ref reached Base 2;
- successful check runs `101521834928` and `101521881953`, each with the exact
  corresponding Candidate as `head_sha`;
- exactly one `COMMENTED` review, ID `5125969416`, with Candidate 1 as its
  `commit_id`, which remained historical after Candidate 2 appeared;
- nonzero rejection of a Candidate-1 `--match-head-commit` merge while the
  current head was Candidate 2;
- one successful Candidate-2 merge, producing
  `34786ee2643eca9419f2b5d1b8e262d53ea9bbd8` with exact parents
  `[Base 2, Candidate 2]`;
- refusal to delete a guard ref after its OID changed, followed by fixture
  cleanup only after recording that new exact owned OID; and
- result-loss retries that dispatched one PR creation, one review creation,
  one successful merge, and one deletion for each of three run refs.

The first logical run is retained as a safe failure. It closed unmerged PR #1
and removed its exact refs after a bounded wait incorrectly expected the PR
`base.sha` projection to track live base movement. It did not infer absence,
readiness, or integration from that stale projection.

Final reconciliation found stable `main` unchanged, no open PR, no run-owned
ref, no local temporary root, no issue comment on the successful PR, and no
credential-bearing artifact or persistent process.

## Alternatives considered

### Trust pull-request base and head projections

Rejected. The experiment observed `base.sha` remain at Base 1 after the live
base ref moved to Base 2. PR projections are useful context, but the live base
must be fetched and compared separately at every readiness/integration gate.

### Use remote-tracking refs and shorthand force-with-lease

Rejected. A background fetch can change tracking refs. The selected form names
the one exact ref and expected OID; an empty expected value is used only for a
proved create-if-absent operation.

### Create a PR without lookup, or blindly retry after an error

Rejected. Result loss after PR creation would duplicate an external effect.
The selected lookup includes canonical repository IDs, exact base/head refs,
head owner, and a unique Run-scoped durable marker before create.

### Treat PR-level check summaries as Candidate proof

Rejected. Checks are queried by exact commit and admitted only when the check
run's own `head_sha`, identity, terminal status, and conclusion match. A new
Candidate must produce and pass a new check.

### Treat all PR feedback as current after a new commit

Rejected. A review exposes `commit_id`; the experiment preserved Candidate-1
feedback as history instead of silently rebinding it to Candidate 2. Feedback
without an exact commit relationship must invalidate readiness or stop as
ambiguous rather than become SHA-specific approval evidence.

### Infer integration from merge_commit_sha alone

Rejected. GitHub returned a non-null test merge SHA for closed, unmerged PR #1.
The selected proof requires merged state/timestamp, exact stored PR head,
fresh live base ref, Candidate reachability, and the expected merge graph.

### Delete by branch name after merge

Rejected. Name ownership does not prove current OID ownership. The engine must
re-observe the exact live ref and compare-delete it with an explicit lease.
Changed, ambiguous, or externally unavailable state parks without deletion.

## Decision

**Go.** GitHub delivery is admitted on the recorded Linux/authenticated-owner
private-repository topology only when the engine applies this exact contract:

1. Verify canonical GitHub repository identity and policy before the effect
   sequence, select that identity explicitly for every operation, and apply the
   `dir-m0.10` just-in-time identity gate before each product effect.
2. Persist immutable Candidate, resolved base, exact refs, owner, and Run
   idempotency marker before publication.
3. Reconcile the live exact ref first. Adopt the desired OID; dispatch an update
   only when the current OID equals the explicit expected OID; accept absence
   only from a successful authoritative absence result.
4. Lookup PR/review intents before creation and require one exact repository,
   base, head, owner, and durable marker match.
5. Bind checks to their own exact `head_sha` and reviews to their `commit_id`.
   Any Candidate change invalidates prior readiness evidence.
6. Observe/fetch the live relevant base independently of PR `base.sha`. Any
   base change invalidates readiness and requires a new Candidate, validation,
   and review.
7. Immediately before integration, re-read exact head/base/check/feedback/
   mergeability facts and require an atomic expected-head precondition such as
   `gh pr merge --match-head-commit <Candidate>`.
8. After an unknown merge result, observe first. Accept success only from
   merged state plus exact head, merge commit, live base, and graph facts.
9. Delete a remote ref only after full integration/ownership/consumer policy
   passes and only with `--force-with-lease=<ref>:<exact-expected-oid>`.
   Reconcile exact absence; preserve a changed or ambiguous ref.

This Go result proves the public mechanisms needed by a future adapter. It does
not authorize M1 while another M0 stop condition remains unresolved.

## Consequences

- Domain records need distinct durable fields for Candidate SHA, resolved base
  SHA, live observed base SHA/time, exact remote ref expectations, repository
  database ID, PR node/number, check-run IDs/head SHAs, review IDs/commit IDs,
  and merge state/commit.
- PR base projections cannot replace Git observation. A readiness check and a
  pre-merge check both fetch/observe the relevant live base.
- `merge_commit_sha` without `merged=true` and `merged_at` is explicitly
  non-authoritative.
- A changed Candidate or relevant base invalidates prior check/review/
  validation records for readiness, even though those records remain durable
  history.
- All mutations use exact argv values and explicit repository selection; no
  ambient cwd, default repository, default branch, or tracking-ref lease is an
  authority source.
- The sandbox remains private and retained through exact-SHA independent
  review. Its deletion is a post-delivery coordinator cleanup action, not part
  of this Candidate.
- This spike did not fault-inject repository rename/replacement between the
  initial identity proof and each Git transport. `dir-m0.10` must revalidate
  repository identity at each effect boundary and stop on any mismatch.
- `dir-m0.10` must generalize the demonstrated effect-before-result protocol
  into durable intents and recovery at every critical boundary.
- Later tests must add forks, merge queues, protected branches, configured
  approvals/checks, squash/rebase strategies, GitHub Enterprise, credential
  rotation/outage, and the exact release topology. Unsupported or ambiguous
  configurations fail closed; they never silently select direct delivery.

## Independent verification

Pending review of the exact committed Task Candidate in a detached disposable
checkout by a normal top-level Reviewer Agent. The reviewer must reconcile the
retained private sandbox and verify the source/evidence hash. Publication, PR,
integration, Task closure, sandbox deletion, and workspace removal remain
outside this Candidate handoff.
