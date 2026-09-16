# Local recovery and remote cleanup

Director Engine owns cleanup policy, ordering, retry, recovery, and durable
evidence. Paseo, Git, GitHub, filesystem, and coordinator adapters only observe
or perform one exact authorized effect. A model claim and an adapter response
never authorize deletion.

## Frozen policy

Each Run freezes these Project → Workspace → Task values:

- `terminateOnCompletion`, default `true`;
- `cancellationCleanup`, default `snapshot_then_delete`, with `retain` as the
  non-destructive alternative;
- `deleteRemoteTaskBranch`, default `true`; and
- `recoveryRetentionDays`, default and maximum `7`.

The recovery worker also freezes the ADR-0013 Linux ceilings: 10,000 recovery
entries, 25,000 inspected entries, 512 MiB aggregate ignored content, 256 MiB
per regular file, a 64 KiB sequential buffer, 180-second phases, a 480-second
artifact lifecycle, a 540-second supervisor, 192 MiB measured RSS growth, and
a 10% projected free-space floor. Overrides may tighten these values, shorten
retention, retain more resources, or disable termination/remote deletion. They
cannot expand destructive authority or the measured resource envelope.

## Admission and deterministic order

Integrated cleanup is admitted only from the current Candidate authority and
the exact M4.9 integration evidence. For pull requests, admission freshly
reobserves both GitHub merged state and the remote merge graph. For direct
delivery, it consumes the exact completed direct-integration evidence. The
binding also freezes the Project lease epoch, Task and Run versions, Candidate,
base, tree, repository, canonical remote, source/common/worktree device and
inode identities, task branch, owner digest, Task Agent, every Reviewer this
Task labelled, Paseo workspaces and Reviewer host views, and lifecycle state
(`active`, `restored`, or `reclaimed`).

The Reviewer leg completes before the Task Agent leg begins. That is the
self-contained triple [independent review](independent-review.md) specifies:
archive the Reviewer, archive its host view, then remove its exact owner-marked
detached checkout. Effects run in this order:

1. terminate and verify every live Reviewer Agent this Task labelled;
2. archive and verify each of those Reviewers' exact Paseo host views;
3. remove each exact owner-marked detached Reviewer checkout;
4. terminate and verify the exact Task Agent;
5. inspect and, when required, durably verify recovery;
6. archive and verify the exact Task Paseo workspace;
7. remove the exact registered product worktree;
8. delete the integrated remote Task ref with an explicit expected-OID lease;
9. delete the exact local Task ref after proving no other worktree consumes it;
10. retain recovery material until its separately guarded expiry.

Step 1 is every live Reviewer, not the one Reviewer whose Review authorized
delivery. A Task has one Reviewer per Candidate it produced: each correction
starts a new Reviewer and leaves the previous one alive, so a Task that
corrected nine times ends with ten. Terminating only the last of them is what
lets a Reviewer backlog accumulate silently behind a cleanup that reports
success. Reviewer cleanup is admitted by durable Review verdict evidence rather
than by integration, because the Reviewers of superseded Candidates reviewed
commits that were never integrated and never published.

The existing development coordinator contracts remain the repository handoff
mechanism, as a Reviewer pair and a Task Agent pair. `reviewer-cleanup-plan` /
`reviewer-cleanup-apply` bind one Reviewer, its host view and its disposable
checkout, and use the same Reviewer agent → host view → checkout order on the
authority of durable Review verdict evidence. `cleanup-plan` / `cleanup-apply`
consume the same exact M4.9 integration evidence and use the agent → workspace
→ worktree → remote ref → local ref order for a clean Candidate checkout; they
refuse to begin while the Reviewer named by that Review evidence is unarchived
or any other live agent still carries this Task's Reviewer labels, which is how
the order above is enforced rather than assumed. Product cleanup does not
give a Task Agent permission to invoke that coordinator lifecycle, publish a
Candidate, close a Task, or clean the real development workspace.

## Recovery and failure behavior

A fresh temporary Git index builds the prospective worktree tree instead of
trusting `git status`. The real index tree is preserved separately when it
differs. Hidden refs use the exact
`refs/director/recovery/<task>/<run>/<binding>/...` namespace, point to verified
recovery commits parented by the Candidate, and remain for seven days.

Ignored files and Git-invisible empty directories never enter Git. Eligible
integrated prospective-tree-clean material is streamed into an owner-only
private artifact and byte-verified from its private manifest. Entry names,
individual hashes, contents, and absolute paths remain outside TaskStore,
Board, Organizer, logs, diagnostics, and support output. Dirty work combined
with ignored content, nested repositories, untracked symlinks, devices, FIFOs,
sockets, active filters, ownership changes, limits, timeouts, or disk pressure
stays in place.

Failed or cancelled work has `cleanupAuthorized=false` until its exact snapshot
is durably verified. `retain` terminates the owned agents but retains the
workspace, worktree, and refs. Disk pressure stops preservation and routes a
path-free Needs-you reason; it never selects deletion of unintegrated work.

TaskStore backup expiry and technical-log compaction use the independent
[TaskStore maintenance contract](taskstore-maintenance.md). That boundary has
no access to Git refs, worktrees, host views, agents, or retained recovery
artifacts. Disk pressure therefore cannot broaden this cleanup state machine's
exact integration/ownership authority or remove unintegrated recoverable work.

After seven days, expiry first verifies and removes the exact private artifact,
then atomically compare-deletes the recovery refs. Before the boundary the
state is scheduled `retained`, not Needs you. A changed, partial, foreign,
recreated, or unprovable artifact/ref is preserved.

Every mutation persists intent before handoff and reobserves immediately after
success, error, timeout, or response loss. Exact absence completes an unknown
destructive effect. Under ADR-0021, local and remote Task refs are the narrow
recovery exception: a `dispatching` or `unknown` deletion may create a new
attempt only after authoritative observation proves the ref still equals the
exact Candidate, followed by the same expected-OID `update-ref` or
`force-with-lease` compare-delete guard. A changed, unavailable, or ambiguous
ref parks without mutation. A completed ref deletion remains terminal on any
later reappearance. Present worktrees and every other destructive target after
possible handoff retain the stricter refusal. Project lease, Run
compare-and-swap, and per-repository serialization ensure competing
coordinators have one durable winner without claiming exactly-once external
execution.

Cleanup also transitions the lifecycle it runs from: terminating the agents and
archiving the workspaces makes the frozen lifecycle state historical, and
removing the worktree reclaims the checkout. Both facts are immutable in the
binding, so an interrupted cleanup strands its own recorded intents behind a
description that can no longer be asserted truthfully. Resumption therefore
binds the transitioned truth and carries the earlier recorded effects forward as
read-only history, under the same immutable identity, ownership, and actor
binding and a transition that advances only to the terminal state cleanup itself
produces. A recovery fact cleanup never produces is not a resumable target.
Carrying an intent is not executing or adopting anything: every effect is still
re-observed before its own decision, and a destructive absence stays ambiguous
unless some recorded intent explains it. The development coordinator exposes
this as `--resume-state-file` on `cleanup-plan` and `cleanup-apply`; see the
[coordinator CLI contract](coordinator-cli.md).

## Projection and closure

Cleanup failures populate the ordinary bounded `NeedsYou` record. Board and
Organizer render stable path-free reasons for snapshot, disk, ownership,
repository, and unknown-result failures. Cleanup completion is a current
Candidate evidence binding. The closure reducer may consume it, but cleanup
itself never marks the Task complete; all configured closure gates still apply.
