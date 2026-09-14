# ADR-0025: Let a Go bootstrap own the two-channel runtime

- **Status:** Accepted
- **Date:** 2026-09-14
- **Beads Task:** `dir-m6.20`
- **Decision owner:** Human project owner through binding Task comment
  `01a09de1-5424-706a-9cd2-d13d86194a4c`
- **Amends:** PLAN §§19.4 and 24.6 and
  [ADR-0023](0023-plugin-owned-release-runtime.md)
- **Preserves:** handler-scoped Paseo authority, M6.22 authenticated coordinator
  lifecycle reads, M6.21 Engine-owned own-Project administration, no system
  service or restart, safe removal/data preservation, exact identity, and all
  release no-fallback rules

## Context

Default-main needs to execute the exact checked-out Engine before a release
exists. The earlier connector-owned implementation made Node responsible for
artifact distribution, process supervision, credentials, recovery, and host
identity. That duplicated portable runtime policy in the Paseo adapter and left
published packages without a small independently pinned runtime controller.

The former supervisor also used the literal host `local-paseo`; Home then
rejected any request made with the real Director identity. Paseo 0.7.2 exposes
no authenticated daemon identity to a server handler, and client input cannot
be runtime authority.

## Decision

One small Linux-amd64 Go program, `director-bootstrap`, owns Director runtime
policy. It reads the closed Engine, bootstrap, and Dolt manifests; verifies and
atomically publishes private caches; creates the persistent opaque Director
host identity and credentials; provisions or validates TaskStore; starts,
adopts, monitors, backs off, and hands off Engine and Dolt; and owns locks,
leases, child identity, recovery, and bounded diagnostics. Its authenticated
owner-only Unix control socket is the sole runtime control plane. The
TypeScript connector remains Paseo UI/RPC integration. Its launcher executes
only the exact installed bootstrap control command and contains no downloader,
Engine compiler, child supervisor, fallback, or recovery policy.

The committed descriptor is the complete channel selector:

- `unpublished` is the explicit default-main source-testing channel. During
  Paseo add/update preparation, a narrow install-only Node adapter performs the
  unavoidable fixed-argv build of `engine/cmd/director-bootstrap` from the
  exact clean checkout. That bootstrap—not Node—then builds exactly
  `engine/cmd/director-engine` once with fixed Go 1.26.5,
  `-trimpath -buildvcs=false -mod=readonly`, and an existing Go-sum-verified
  offline module closure. It binds Candidate, target, contract, notices, and
  executable digest before atomically publishing the cache. Reload never
  compiles and neither runtime process depends on the checkout.
- `published` alpha/beta/stable packages ship a release-built bootstrap pinned
  by `release/bootstrap-linux-amd64.json`. With no Go, Git, or compiler, that
  bootstrap downloads and verifies only the manifest-pinned precompiled
  Engine, notices, and canonical Dolt 2.3.2 assets. Missing bootstrap, platform,
  descriptor, asset, size, digest, identity, or cache closure fails closed.
  Published execution never calls Go and never falls back to source.

Trusted Go code generates one random `director-…` identity with label
`Director` and persists it mode `0600` below private XDG configuration. The
same value binds TaskStore, Engine launch, every host-bound RPC, controller
state, and handoff. Client host values are ignored. Existing exact identities,
credentials, TaskStore data, Project-admin sessions, and caches survive
reload/update/removal/reinstall. A current legacy `local-paseo` TaskStore
binding is migrated by Engine-owned bootstrap authority into the new identity
with an owner-only readback backup; foreign or ambiguous identities fail
closed. Identity-file poisoning is never repaired by deletion or reseeding.

The shared Paseo contribution entry does not evaluate server-only handlers in
the client catalog, treats optional client registration capabilities as
optional, and imports only modules accepted by the exact 0.7.2 desktop client.

## Consequences

- Published installation and runtime need no Go, Git, compiler, system Dolt,
  daemon credential copy, secondary Paseo connection, service unit, or restart.
- Main add/update needs the exact fixed system Go toolchain and offline module
  closure for the bootstrap and Engine builds; a failure leaves the previous
  installed Candidate and controller usable.
- Runtime readiness performs no Git lookup. Later Engine-owned Organizer Git
  effects use the fixed `/usr/bin/git` executable directly, never an inherited
  or user-controlled `PATH` result.
- Removing the plugin stops lease renewal; the Go controller stops only its
  exact children and preserves identity, credentials, TaskStore, and caches.
- Public logs contain bounded codes and public artifact digests, never raw host
  identity, credentials, compiler output, or private paths.

## Verification

Maintained Go tests cover fixed build argv, release downloads, safe extraction,
atomic/replayed caches, toolchain diagnostics, identity poisoning and legacy
TaskStore migration, binding changes, foreign ownership, and controller child
ownership. Node tests prove the launcher can execute only the pinned bootstrap,
published preparation invokes no compiler, failure preserves installed
metadata, CommonJS loads, and the real 0.7.2 client catalog survives absent
server capabilities and its rejected bridge module. Focused deterministic Go
and Node tests record one bootstrap plus one Engine build per main add/update,
zero builds on replay/reload, controller-owned Engine/Dolt effects,
identity/data/Project-admin preservation, and bounded failure behavior.

Owner decision `dir-m6.20` comment
`01a0a088-ae2a-7c9b-89fc-7d8b08593623` removes the former disposable
`test:activation:real` Paseo lifecycle from M6.20, CI, and release gates. The
entry and code used only by it are retired rather than skipped or renamed;
the separately scoped published `test:runtime:real` gate remains unchanged.
