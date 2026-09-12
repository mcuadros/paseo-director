# Installation, update, rollback, and compatibility

Director for Paseo is an independent community plugin. It is not affiliated
with, endorsed by, maintained by, or sponsored by Paseo.

> **Authority warning:** on exact Paseo 0.7.2, the connector requires a
> full daemon-operator credential. The trusted, unsandboxed connector can call
> the daemon user's complete public API. Install only after accepting this P2
> residual risk and placing the non-empty mode-`0600` credential outside every
> repository, plugin checkout, engine source, and engine cache path. Missing or
> unsafe credential state fails before SDK-ready, engine attachment, or host
> mutation.

## Supported host

The Director 1.0 packaged target is exactly:

- Linux amd64;
- Paseo `0.7.2`, not another `0.7.x` and not the `0.8` preview;
- Node.js 22 or newer;
- npm with lockfile-version-3 support.

Go 1.26.5 is required for development mode and release builders. It is not a
release-user prerequisite. Git, the separately supervised Director Engine and
its configured TaskStore, and feature-specific GitHub/provider tools remain
declared external prerequisites; Director never installs them implicitly.

Candidate preparation and every connector start check the supported Linux,
architecture, Node, and exact Paseo CLI tuple. Paseo itself compiles the plugin
against its current public runtime on every load. Exact-0.7.2 exposes no public
daemon-version accessor, so Director combines that compile boundary with the
exact CLI check and the locked `@getpaseo/client@0.7.2` contract. Any mismatch
fails closed with a bounded code and never falls back to another connector.

## Install and update

Before installation, create the owner-only runtime file documented below and
its separately protected credential. This is candidate-preparation input, not
daemon-start environment.

After the release coordinator publishes the protected `stable` channel:

```text
paseo plugin add mcuadros/paseo-director --ref stable
paseo plugin status director
paseo plugin update director
paseo plugin reload director
```

Paseo clones a candidate first. Its declared preparation is exactly:

```text
npm ci --omit=dev --ignore-scripts --no-audit --no-fund
node tools/packaging/verify-install.mjs
```

The first command accepts only `package-lock.json`; it omits development
packages and disables dependency lifecycle scripts. The second proves the
installed production versions, real directories, exact registry/integrity
closure, dependency edges, audited lifecycle-script set, and supported host.
CI applies the same audit to every change. Neither step vendors
`@getpaseo/client` or selects an unrecorded dependency.

An update is an explicit trust decision. Paseo reports the prior and resolved
commits, serializes concurrent requests, prepares and compiles outside the
active checkout, and replaces its managed record only after validation. A tag
or exact commit is an immutable pin and does not advance through `update`.
`update` also activates a changed Candidate. If no Git commit changed, it
correctly reports zero changes and is not a configuration repair; use the
public plugin-scoped `reload` command after a runtime-file edit.

## Restart-free runtime configuration

Director reads `$XDG_CONFIG_HOME/director/runtime.json`, or
`~/.config/director/runtime.json` when `XDG_CONFIG_HOME` is unset. Its directory
must be mode `0700`; the regular non-symlink runtime file and its separate
canonicalized credential file must be owned by the daemon user and mode `0600`. The credential file is
the only required private authority input for release mode:

```json
{
  "schemaVersion": 1,
  "paseo": {
    "credentialFile": "/absolute/private/connector.password"
  },
  "engine": {}
}
```

The connector defaults to the same-host exact-0.7.2 public SDK at
`ws://127.0.0.1:6767/ws`, release engine mode, and the loopback engine endpoint
`http://127.0.0.1:7041`. XDG supplies the engine-cache base; otherwise the
standard user cache is used. These defaults select no Project, repository,
Workspace, Organizer, provider, delivery mode, or execution authority.

`engine.mode` (`release` or `development`) and `engine.url` (origin-only
loopback HTTP with an explicit port) are the only non-secret runtime overrides.
Development mode also requires an explicit absolute `engine.sourceRoot` and
may override `engine.moduleCache`; those private development paths have no
release default. Release mode rejects development fields. Unknown, missing,
trailing, non-loopback, relative, overlapping, broadly readable, or replaced
state fails closed.

Project ID/name, repository root, Organizer path, Workspace identity/name, and
working directory never appear in daemon environment or this JSON. M6.10
exposes a strict resolver over one current public Paseo Project/workspace pair;
the M6.11 native selector supplies that pair and owns its UI.

Legacy `DIRECTOR_PASEO_*` and `DIRECTOR_ENGINE_*` process values are ignored so
an installed daemon never needs a restart to remove or change them. The
secret/path-free startup record marks only `legacyEnvironment=ignored`. After
creating or editing the runtime file, activate and verify it live:

```text
paseo plugin reload director
paseo plugin ls --json
paseo plugin logs director --json
```

Success requires plugin status `running` and the latest bounded log record
`code=DIRECTOR_ACTIVATION_READY`, `result=running-current`, the expected exact
connector commit, and the intended configuration SHA-256. The record reports
only `defaulted` versus `overridden` for non-secret settings; it never reports
their values. A full Paseo restart or machine reboot is neither a supported
activation step nor a troubleshooting remedy, and reload does not stop
unrelated agents or workspaces.

## Engine modes and release assets

Runtime `engine.mode` defaults to `release` and, when present, must be literally
`release` or `development`.

In release mode, schema-2 `release/engine.json` (defined by
`release/engine.schema.json`) pins the semantic version,
`linux-amd64`, exact reviewed source Candidate, canonical Director-owned GitHub
Release asset URLs, and binary/notices SHA-256 values. Redirects, alternate
origins, credentials, query strings, fragments, names, targets, sources, empty
digests, or extra fields fail closed. The connector downloads bounded assets,
verifies both digests and the exact-source notices marker, probes the binary's
side-effect-free identity, and then renames the complete directory into:

```text
$XDG_CACHE_HOME/director/engines/release/<version>/linux-amd64/<binary-sha256>/
```

If `XDG_CACHE_HOME` is absent, the standard user cache base is used. Symlinks,
foreign ownership, broad permissions, stale identity, or poisoned content are
refused and preserved for inspection. An owned interrupted staging directory
is recovered only after its Linux process identity is absent. Release failure
never compiles and never executes an unverified product path.

Development mode requires `engine.sourceRoot`, resolves one exact
clean local Git Candidate, uses the local Go toolchain with `GOTOOLCHAIN=local`
and `GOWORK=off`, and atomically caches that Candidate. It never downloads a
release or falls back to release mode.

Startup diagnostics contain only mode, version, exact source Candidate,
target, executable/notices SHA-256, connector commit, and contract version/hash,
plus the bounded host tuple. They contain no credential, credential path,
repository path, cache path, URL, or raw tool output. Director has no telemetry.

## Failure, rollback, and live recovery

Registry/network failure, corrupt or stale package/lock state, dependency or
lifecycle audit failure, incompatible host, partial download/write, digest or
identity mismatch, unsafe cache state, and plugin compile/load failure all stop
the candidate. The previous Paseo commit, external engine cache, configuration,
TaskStore state, and recovery data remain untouched. Repeating a completed
request adopts the exact verified state; a conflicting or unsafe state fails
closed.

After correcting the channel with a new reviewed fast-forward commit, rerun
`paseo plugin update director`. After correcting only runtime configuration,
run `paseo plugin reload director`; repeating reload is safe. To deliberately roll back, first record the
current commit and confirm external state is healthy, then remove the connector
and install a previously reviewed immutable tag or exact commit:

```text
paseo plugin remove director
paseo plugin add mcuadros/paseo-director --ref <reviewed-tag-or-commit>
```

This replaces only Paseo's managed connector checkout. The external XDG engine
cache, owner-managed credential, engine configuration, TaskStore, Organizer,
Project configuration, and recovery material are preserved; no hidden data
migration is required. If their identity cannot be proved, stop instead of
deleting or overwriting them.

For pre-load or activation failure, use `paseo plugin ls --json` and
`paseo plugin logs director --json`. `DIRECTOR_RUNTIME_CONFIG_*`,
`DIRECTOR_RUNTIME_CREDENTIAL_*`, `ENGINE_INSTALL_NOT_PREPARED`, and
`DIRECTOR_ACTIVATION_FAILED` are bounded path-free causes. Fix the private file
or publish a corrected Candidate, then invoke only the public plugin-scoped
reload/update command. Director Home, Doctor, and Repair are unavailable until
the connector loads; once available, Doctor remains read-only and Repair does
not install, reload, restart, signal, or rewrite plugin state.

## Removal

```text
paseo plugin remove director
```

Paseo removes its managed Git checkout. It intentionally does not delete the
external credential, XDG engine cache, configuration, TaskStore, Organizer,
Project repositories, or recovery state. Their later removal is a separate,
explicit owner operation with its own identity, backup, and cleanup checks.

Common bounded preparation codes include
`DIRECTOR_INSTALL_PASEO_UNSUPPORTED`, `DIRECTOR_INSTALL_PLATFORM_UNSUPPORTED`,
`DIRECTOR_INSTALL_TARGET_UNSUPPORTED`, `DIRECTOR_INSTALL_NODE_UNSUPPORTED`,
and `DIRECTOR_INSTALL_DEPENDENCY_AUDIT`. Engine resolution uses corresponding
`ENGINE_*` codes for mode, metadata, fetch, size, digest, notices, identity,
source, permission, and cache failures.
