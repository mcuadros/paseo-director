# ADR-0023: Let the plugin own a release-only portable runtime

- **Status:** Accepted
- **Date:** 2026-09-13
- **Beads Task:** `dir-m6.14`
- **Decision owner:** Human project owner through binding Task comment
  `01a09b27-f706-7ede-bb0e-2f89d3d58b45`
- **Amends:** PLAN header and §§1, 8.1, 19.2, 19.4, 21.1, 23, and 24;
  [ADR-0004](0004-select-direct-dolt-taskstore.md),
  [ADR-0012](0012-direct-dolt-operational-readiness.md),
  [ADR-0017](0017-standalone-engine-connector-authority-boundary.md), and
  [ADR-0022](0022-least-privilege-taskstore-maintenance.md)
- **Preserves:** [ADR-0011](0011-linux-only-platform-scope.md), the standalone
  Go Engine boundary, direct-Dolt schema and least-privilege identities,
  exact-source release identity, no-fallback failures, and all cleanup and
  secret-safety requirements

## Context

The first maintained installation showed that a running Paseo plugin did not
own either required backend process. Machine-local systemd units and a separate
development source checkout had to start Dolt and compile/start Engine. Removing
those machine files left no product component responsible for recovery. The
connector also opened a second password-authenticated WebSocket to loopback,
which failed when Paseo listened only on a Tailscale address even though every
plugin handler already received Paseo's public API.

This topology made installation host-specific, required manual coordination,
and contradicted the owner's no-Paseo-restart and future-portability goals.

## Decision

The installed plugin uses only the public `PaseoApi` supplied to its server RPC
handlers. It never copies the daemon password or opens a second daemon
connection.

The first Director RPC resolves one closed release descriptor and starts or
adopts one detached plugin-owned supervisor. The descriptor pins a precompiled
Director Engine from the matching Director GitHub Release and a canonical
DoltHub Dolt archive, including archive, executable, notices, source, target,
and contract digests. The installed plugin may download, verify, atomically
cache, and execute those artifacts. It may not invoke Go, compile either
runtime, discover Engine or Dolt through `PATH`, use a development source, or
fall back when a release is absent.

The supervisor is a separate process, so the Engine remains standalone and
Dolt remains separately supervised. It owns only Director's loopback listeners,
private XDG runtime/data/configuration, generated scoped database credentials,
typed schema bootstrap, child health/backoff, and a short plugin lease. A
compatible plugin reload or update adopts the same supervisor and children.
When no plugin renews the lease, the supervisor stops its owned processes but
preserves the TaskStore and verified caches. It never kills a listener or
process whose exact ownership cannot be proved.

Release CI is the only Engine compiler. An unpublished descriptor keeps the UI
registered with a bounded failure and cannot trigger local compilation. Linux
amd64 remains the only 1.0 support claim; process, filesystem, and control
operations sit behind a platform boundary so another OS can be added only by a
future approved decision and evidence.

## Consequences

- Installation requires no Go toolchain, system Dolt, service unit, daemon
  credential file, loopback Paseo listener, or machine restart.
- Release publication must verify and disclose both Engine and Dolt artifacts
  before promoting a channel.
- Existing machine services are migration inputs only. Unknown occupied ports
  fail closed as `DIRECTOR_RUNTIME_EXTERNAL_OWNER`.
- `plugin remove` is not data deletion. Purge remains a separate explicitly
  confirmed operation.

## Verification

Maintained tests cover handler-scoped Paseo authority, absence of compiler and
PATH fallbacks, exact Engine/Dolt metadata, safe archive extraction, atomic
cache adoption, supervisor lease/adoption/handoff, foreign listeners, fresh
schema-v2 bootstrap, Home cursor zero, child recovery, secret-free arguments,
data preservation, and unchanged unrelated Paseo agents across plugin reload.
