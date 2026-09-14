# ADR-0023: Let the plugin own a portable runtime

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
- **Amended by:** [ADR-0025](0025-main-source-testing-and-director-host-identity.md)
  for the Go-owned two-channel controller, explicit unpublished-main
  source-testing channel, and Director-owned host identity; published releases
  remain precompiled and release-only

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

The first Director RPC asks one manifest-pinned Go bootstrap to start or adopt
the detached plugin-owned runtime. Published packages ship that precompiled
bootstrap. It—not TypeScript—downloads, verifies, atomically caches, and
executes the exact Engine/notices and canonical DoltHub artifacts named by the
release manifests. The published path may not invoke Go, compile either
runtime, discover Engine or Dolt through `PATH`, use a development source, or
fall back when a release is absent.

The Go bootstrap stays alive as the runtime controller, so the Engine remains
standalone and Dolt remains a separately supervised child. Go owns only
Director's loopback listeners, private XDG runtime/data/configuration,
generated scoped database credentials, typed schema bootstrap, child
health/backoff, controlled handoff, and a short plugin lease. A compatible
plugin reload or update adopts the same controller and children. When no plugin
renews the lease, it stops its exact owned processes but preserves identity,
TaskStore, credentials, and verified caches. It never kills a listener or
process whose exact ownership cannot be proved.

Published-release preparation never compiles. ADR-0025 adds the separately
declared unpublished-main path: the install adapter builds only the Go
bootstrap, and that controller builds the exact Engine. It does not weaken the
release no-fallback rule. Linux amd64 remains the only 1.0 support claim;
process, filesystem, and authenticated local-control operations sit behind the
Go platform boundary so another OS can be added only by a future approved
decision and evidence.

## Consequences

- Published-release installation requires no Go toolchain. Neither channel
  requires a system Dolt, service unit, daemon credential file, loopback Paseo
  listener, or machine restart.
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
