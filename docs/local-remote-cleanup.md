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
inode identities, task branch, owner digest, Task Agent, optional Reviewer,
Paseo workspaces, and lifecycle state (`active`, `restored`, or `reclaimed`).

Effects run in this order:

1. terminate and verify the exact remaining Reviewer Agent, then Task Agent;
2. inspect and, when required, durably verify recovery;
3. archive and verify the exact Reviewer and Task Paseo workspaces;
4. remove the exact registered product worktree;
5. delete the integrated remote Task ref with an explicit expected-OID lease;
6. delete the exact local Task ref after proving no other worktree consumes it;
7. retain recovery material until its separately guarded expiry.

The existing development coordinator `cleanup-plan` / `cleanup-apply` contract
remains the repository handoff mechanism. It consumes the same exact M4.9
integration evidence and uses the same agent → workspace → worktree → remote
ref → local ref order for a clean Candidate checkout. Product cleanup does not
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

## Projection and closure

Cleanup failures populate the ordinary bounded `NeedsYou` record. Board and
Organizer render stable path-free reasons for snapshot, disk, ownership,
repository, and unknown-result failures. Cleanup completion is a current
Candidate evidence binding. The closure reducer may consume it, but cleanup
itself never marks the Task complete; all configured closure gates still apply.
