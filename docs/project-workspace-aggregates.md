# Project and Workspace aggregate contract

The standalone Go Director Engine owns the M2 Project and Workspace contract.
Director for Paseo may transport typed commands and render projections, but it
does not canonicalize repositories, arbitrate a lease, interpret a conflict,
or select a recovery action.

Each persisted Project has one stable global ID, one mutable display name, one
embedded Organizer record with the engine-derived stable Organizer ID, and
between 1 and 128 canonical Workspaces. Initial Project, Organizer, and
Workspace records commit atomically through the typed TaskStore port. The
existing generic `aggregates` mapping already supports the new Workspace kind,
so this change requires no direct-Dolt schema migration and preserves the
version-1 schema guards.

Configuration Workspace IDs are stable only within their Project. The durable
Workspace aggregate ID is derived from `(Project ID, configuration Workspace
ID)`, so two Projects may both use `product` without a global collision.
Display-name, source-checkout-path, and supported remote-transport changes do
not change that ID. The Project ID, Project-local Workspace key, and canonical
repository ID are immutable after creation.

The repository identity parser accepts only the already-approved HTTPS, SSH,
Git, and safe SCP-like forms. HTTPS and Git transports reject all userinfo;
SSH URL and SCP forms admit only the closed non-secret `git` username. Hosts,
IPv4, bracketed IPv6, and ports are parsed structurally. The parser lowercases
DNS hosts, rejects ambiguous leading-zero IPv4 and invalid embedded-IPv4 IPv6
positions, canonicalizes valid IPs and default ports, normalizes the repository
path and `.git` suffix, and maps equivalent transport spellings to one host/path
key and stable hash ID. Percent escapes are decoded exactly once and literal
percent data is re-encoded, so even nested percent input has an idempotent
canonical output and stable key. A terminal chain containing two or more
case-insensitive `.git` suffixes, including percent-encoded spellings, is
rejected rather than creating a new alias class. Non-default ports remain
distinct. Credentials, helper dispatch, local paths, query/fragment
data, traversal, command syntax, malformed authorities, and malformed encoding
fail closed through bounded diagnostics which never retain raw input.

The Git adapter records the exact canonical source root and filesystem
identity, Git common directory and filesystem identity, canonical observed
`origin`, repository key, and repository ID. A source path which is a symlink,
contains lexical traversal, names a nested repository directory, or resolves
to a Git common directory outside the source checkout is refused. Within one
Project, no two Workspaces may share a stable key, repository key, source root
or filesystem identity, or Git common directory or filesystem identity. Every
Project and Workspace read validates the complete
1–128 set and fails with the closed stored-record health code on cross-row
drift. Project-row locking and expected Workspace versions make concurrent
create/update conflicts explicit; stale versions are durably rejected before
replacement-only checks, and idempotent replay returns the first result.

Workspace policy in this Task is intentionally limited to the frozen intrinsic
override vocabulary: launch is `inherit`, `manual`, or `automatic`, and
delivery is `inherit`, `pull_request`, or `direct`. Effective
Project-to-Workspace-to-Task inheritance and security-envelope comparison
remain owned by `dir-m2.5`; scheduler capacity and launch-budget enforcement
remain owned by `dir-m2.7`.

## Project execution lease

One optional lease record lives in the versioned Project aggregate. It binds
the holder instance, Linux process identity, monotonic epoch, TaskStore-clock
timestamps, and dispatch gate. Lease commands contain a bounded duration but
no caller clock. The direct-Dolt adapter reads server time inside the same
expected-version transaction which applies the transition. A first acquisition
uses epoch 1 and may dispatch. An exact live holder may renew without changing
epoch. The Project independently retains the last allocated epoch when a lease
is released; every reacquisition increments it, including after restart and
under concurrent contention, so an old holder/epoch can never become current
again. Exhausting the epoch fails closed. Another holder is refused until
expiry, and expiry alone never grants dispatch.

An expired takeover increments the epoch and remains observe-only. Dispatch is
enabled only after the authorized Linux process adapter proves the previous
process and all dispatch children absent, confirms the one-daemon identity,
and completes reconciliation. Generic commands accept no authority booleans.
The TaskStore timestamps the typed adapter result, persists it in an immutable
Event, and atomically consumes its exact fresh ID while enabling dispatch in
the Project aggregate. Stale, mismatched, fabricated, reused, or out-of-window
observations are refused. Every transition uses an expected Project version
and an immutable request key, so concurrent claims have one winner,
restarts preserve the epoch and result, and stale writers cannot overwrite the
current holder.

This aggregate-level lease contract does not dispatch a product effect or
implement M2 scheduler capacity. Later effects must still apply the complete
ADR-0015 holder/epoch, attempt-permit, observation, process-absence, and
reconciliation gates.
