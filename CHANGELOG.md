# Changelog

All notable changes to Director for Paseo will be recorded here. Director uses
[Semantic Versioning](https://semver.org/); `0.x` releases may change contracts,
while supported `1.x` releases preserve documented configuration and data
compatibility or provide a migration.

## [Unreleased]

No tagged alpha, public beta, stable Director release, or moving release
channel has been published. `1.0.0-alpha.1` remains a subsequent release
effect; this entry does not publish it.

### Added

- Apache-2.0 licensing guidance and a deterministic exact-Candidate
  third-party notice generator/verifier for the Go release binary and shipped
  plugin closure, backed by a complete 580-entry npm lock audit and a
  cache-independent `go.mod`/`go.sum` inventory.
- Private vulnerability reporting, supported-version, safe-evidence, response,
  and coordinated-disclosure policy.
- Exact Linux amd64/Paseo `0.7.2` compatibility and public support boundaries,
  including deterministic npm preparation and explicit default-main versus
  tagged-release Engine behavior.
- Release governance for internal alpha, manually admitted public beta, and
  manually admitted stable promotion.
- Handler-scoped Paseo authority and a plugin-owned detached Engine/Dolt
  Go controller with no secondary daemon connection, system service, or
  restart.
- A supported isolated second Director runtime, declaring its own loopback
  ports together with its own private XDG state, which starts beside a running
  instance without stopping or reconfiguring it. The declaration places the
  whole instance: the connector resolves its Engine endpoint from it and
  refuses a runtime serving a different address. A partial declaration fails
  closed on both sides, an isolated runtime never releases a live one it finds,
  and it refuses a data directory holding a TaskStore it does not own, a
  TaskStore configuration naming a Dolt listener it did not declare, and any
  directory a Director runtime already running on the host keeps its state in.
  Where a running instance actually is now comes from its own supervised
  children rather than from where a default installation would be.

### Security

- License, public-policy, local-support-path, and release-gate checks now fail
  closed on stale or unsafe content and scan generated/public material for
  private paths, credentials, and internal runtime identities.

### Fixed

- The operator guide now states what a runtime ended without being released
  leaves behind, and when ending the process the refusal names is the recovery
  rather than something never to touch.
- The occupied-listener refusal now names the port and distinguishes a
  supervised Director child from an unrelated process, instead of reporting
  both as another Director. A holder that cannot be identified is reported as
  unproved rather than asserted to be something else, and the closed port and
  holder facts reach the operator's plugin log rather than only a direct
  invocation.
- Preserve the M6.17 client contributions, bounded runtime diagnostics, Home
  host-fact fingerprint correction, non-destructive TaskStore configuration
  mismatch guidance, and guarded public-CLI rollback procedure.

### Compatibility

- Supported activation is restart-free and plugin-scoped. Default `main`
  compiles its exact bootstrap and Engine only during declared add/update
  preparation; reload never compiles. Tagged alpha/beta/stable
  releases always use verified precompiled Engine artifacts. Canonical Dolt
  `2.3.2` remains precompiled in every channel.

[Unreleased]: https://github.com/mcuadros/paseo-director/compare/main...HEAD
