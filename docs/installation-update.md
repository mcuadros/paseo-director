# Installation, update, rollback, and compatibility

Director for Paseo is an independent community plugin. It is not affiliated
with, endorsed by, maintained by, or sponsored by Paseo.

> **Authority warning:** on exact Paseo 0.7.2, trusted plugin handlers receive
> the daemon user's complete public Paseo API. Director uses that handler-scoped
> authority directly and never copies the daemon password into a file, process
> argument, secondary WebSocket, or runtime configuration.

## Supported host

The Director 1.0 packaged target is exactly:

- Linux amd64;
- Paseo `0.7.2`, not another `0.7.x` and not the `0.8` preview;
- Node.js 22 or newer;
- npm with lockfile-version-3 support.

Release users need neither Go nor a system Dolt installation. Release CI builds
the Engine and records the canonical Dolt archive before publishing a channel.
The plugin downloads only those precompiled, digest-bound artifacts. Git and
feature-specific GitHub/provider tools remain explicit feature prerequisites.

Candidate preparation and every connector start check the supported Linux,
architecture, Node, and exact Paseo CLI tuple. Paseo itself compiles the plugin
against its current public runtime on every load. Exact-0.7.2 exposes no public
daemon-version accessor, so Director combines that compile boundary with the
exact CLI check and the locked `@getpaseo/client@0.7.2` contract. Any mismatch
fails closed with a bounded code and never falls back to another connector.

## Install and update

Installation does not require a Director runtime file, `DIRECTOR_*` process
values, a connector credential, Go, or Dolt on `PATH`. Candidate preparation verifies
only the supported host, locked dependency closure, exact Git identity, and
release metadata needed for Paseo to compile the plugin. An unpublished release
keeps the interface registered with `DIRECTOR_RUNTIME_RELEASE_UNPUBLISHED`; it
never compiles a local fallback.

After the release coordinator publishes the protected `stable` channel:

```text
paseo plugin add mcuadros/paseo-director --ref stable
paseo plugin status director
paseo plugin reload director
paseo plugin update director
```

Paseo clones a candidate first. Its declared preparation is exactly:

```text
npm ci --omit=dev --ignore-scripts --no-audit --no-fund
node tools/packaging/verify-install.mjs
```

The first command accepts only `package-lock.json`; it omits development
packages and disables dependency lifecycle scripts. The second proves the
installed production versions, real directories, exact registry/integrity
closure, dependency edges, audited lifecycle-script set, supported host, and
exact connector/release identity. It does not read or validate runtime
configuration.
CI applies the same audit to every change. Neither step vendors
`@getpaseo/client` or selects an unrecorded dependency.

An update is an explicit trust decision. Paseo reports the prior and resolved
commits, serializes concurrent requests, prepares and compiles outside the
active checkout, and replaces its managed record only after validation. A tag
or exact commit is an immutable pin and does not advance through `update`.
`update` also activates a changed Candidate. If no Git commit changed, it
correctly reports zero changes; use the public plugin-scoped `reload` command
to retry a corrected release or runtime condition.

## Plugin-owned restart-free runtime

Every server RPC receives Paseo's public plugin API from its handler context.
The connector retains that object for its lifetime; it never opens another
daemon connection and therefore works when Paseo listens only on a non-loopback
interface. Legacy `runtime.json`, `DIRECTOR_PASEO_*`, `sourceRoot`, module-cache,
and development-mode values have no runtime authority.

The first Director RPC resolves the published release, downloads the exact
Engine and Dolt artifacts, and creates or adopts one detached Director
supervisor. The supervisor owns the loopback Engine and Dolt listeners, private
credentials, schema bootstrap, health checks, child restart backoff, and a
30-second plugin lease. Reload and update renew the same lease without changing
the supervisor or unrelated Paseo agents.

Activate or retry only through the public plugin lifecycle:

```text
paseo plugin reload director
paseo plugin ls --json
paseo plugin logs director --json
```

The daemon and unrelated agents and workspaces remain uninterrupted. Removing
the plugin stops lease renewal; the supervisor then stops its owned children
and preserves persistent data and verified caches.

Success requires plugin status `running` and the latest bounded log record
`code=DIRECTOR_ACTIVATION_READY`, `result=running-current`, the expected exact
connector commit, Engine/Dolt release identities, and current supervisor
binding. A full Paseo restart or machine reboot is neither a supported
activation step nor a troubleshooting remedy, and reload does not stop
unrelated agents or workspaces.

## Precompiled release assets

Schema-2 `release/engine.json` (defined by
`release/engine.schema.json`) pins the semantic version,
`linux-amd64`, exact reviewed source Candidate, canonical Director-owned GitHub
Release Engine/notices URLs and canonical Dolt release archive, plus every
archive and executable SHA-256. Redirects outside the GitHub asset boundary,
credentials, query strings, fragments, names, targets, unsafe archive paths,
links, empty digests, or extra fields fail closed.

```text
$XDG_CACHE_HOME/director/engines/release/<version>/linux-amd64/<binary-sha256>/
```

If `XDG_CACHE_HOME` is absent, the standard user cache base is used. Symlinks,
foreign ownership, broad permissions, stale identity, or poisoned content are
refused and preserved for inspection. An owned interrupted staging directory
is recovered only after its Linux process identity is absent. Release failure
never compiles and never executes an unverified product path.

Startup diagnostics contain only mode, version, exact source Candidate,
target, Engine/notices/Dolt SHA-256, connector commit, supervisor state, and
contract version/hash. They contain no credential, private path, URL, or raw
tool output. Director has no telemetry.

## Failure, rollback, and live recovery

Registry/network failure, corrupt or stale package/lock state, dependency or
lifecycle audit failure, incompatible host, partial download/write, digest or
identity mismatch, unsafe cache state, and plugin compile/load failure all stop
the candidate. The previous Paseo commit, verified runtime cache, configuration,
TaskStore state, and recovery data remain untouched. Repeating a completed
request adopts the exact verified state; a conflicting or unsafe state fails
closed.

After correcting the channel with a new reviewed fast-forward commit, rerun
`paseo plugin update director`. After correcting a transient runtime condition,
run `paseo plugin reload director`; repeating reload is safe. To deliberately roll back, first record the
current commit and confirm external state is healthy, then remove the connector
and install a previously reviewed immutable tag or exact commit:

```text
paseo plugin remove director
paseo plugin add mcuadros/paseo-director --ref <reviewed-tag-or-commit>
```

This replaces only Paseo's managed connector checkout. The XDG release cache,
managed TaskStore, Organizer,
Project configuration, and recovery material are preserved; no hidden data
migration is required. If their identity cannot be proved, stop instead of
deleting or overwriting them.

For pre-load or activation failure, use `paseo plugin ls --json` and
`paseo plugin logs director --json`. `DIRECTOR_RUNTIME_RELEASE_UNPUBLISHED`,
`DIRECTOR_RUNTIME_EXTERNAL_OWNER`, `ENGINE_INSTALL_NOT_PREPARED`, and
`DIRECTOR_ACTIVATION_FAILED` are bounded path-free causes. Correct the release
or runtime condition, then invoke only the public plugin-scoped
reload/update command. Director Home, Doctor, and Repair are unavailable until
the connector loads; once available, Doctor remains read-only and Repair does
not install, reload, restart, signal, or rewrite plugin state.

## Removal

```text
paseo plugin remove director
```

Paseo removes its managed Git checkout. Lease expiry stops the owned supervisor,
Engine, and Dolt processes. It intentionally does not delete the XDG release
cache, managed TaskStore, Organizer,
Project repositories, or recovery state. Their later removal is a separate,
explicit owner operation with its own identity, backup, and cleanup checks.

Common bounded preparation codes include
`DIRECTOR_INSTALL_PASEO_UNSUPPORTED`, `DIRECTOR_INSTALL_PLATFORM_UNSUPPORTED`,
`DIRECTOR_INSTALL_TARGET_UNSUPPORTED`, `DIRECTOR_INSTALL_NODE_UNSUPPORTED`,
and `DIRECTOR_INSTALL_DEPENDENCY_AUDIT`. Engine resolution uses corresponding
`ENGINE_*`, `DOLT_*`, and `DIRECTOR_RUNTIME_*` codes for metadata, fetch, size,
digest, archive, identity, permission, cache, ownership, and supervision failures.
