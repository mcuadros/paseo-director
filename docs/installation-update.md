# Installation, update, rollback, and compatibility

Director for Paseo is an independent community plugin. It is not affiliated
with, endorsed by, maintained by, or sponsored by Paseo.

> **Authority warning:** on exact Paseo 0.7.2, trusted plugin handlers receive
> the daemon user's complete public Paseo API. Director uses that handler-scoped
> authority directly and never copies the daemon password into a file, process
> argument, secondary WebSocket, or runtime configuration.

## Supported host

The Director 1.0 packaged target is exactly:

- Linux amd64 with glibc;
- Paseo `0.7.2`, not another `0.7.x` and not the `0.8` preview;
- Node.js 22 or newer;
- npm with lockfile-version-3 support.

Published-release users need neither Go nor a system Dolt installation. Release CI builds
the Go bootstrap and Engine and records the canonical Dolt archive before publishing a channel.
The shipped manifest-pinned bootstrap downloads only those precompiled,
digest-bound runtime artifacts. Git and
feature-specific GitHub/provider tools remain explicit feature prerequisites.

Supported activation is live and plugin-scoped through
`paseo plugin reload director`. It preserves unrelated agents/workspaces and
never restarts the daemon or machine. Reload only adopts or retries previously
prepared channel artifacts; reload never compiles.

Candidate preparation and every connector start check the supported Linux,
architecture, Node, and exact Paseo CLI tuple. Paseo itself compiles the plugin
against its current public runtime on every load. Exact-0.7.2 exposes no public
daemon-version accessor, so Director combines that compile boundary with the
exact CLI check and the locked `@getpaseo/client@0.7.2` contract. Any mismatch
fails closed with a bounded code and never falls back to another connector.

## Install and update

Installation does not require a Director runtime file, `DIRECTOR_*` process
values, a connector credential, or Dolt on `PATH`. The committed descriptor is
the complete channel selector. State `unpublished` is the default-main
source-testing channel: candidate preparation requires a trusted fixed/system
Go 1.26.5 executable and existing Go-sum-verified module cache, builds the small
Go bootstrap once, and lets that bootstrap build the exact clean Engine once.
State `published` is the alpha/beta/stable download-only channel, ships the
precompiled bootstrap pinned by `release/bootstrap-linux-amd64.json`, and needs
no Go, Git, or compiler. No missing or invalid release can fall back from one
channel to the other.
It also requires no service unit.

Default-main preparation is the only installed-host compilation boundary.
Both channels download and verify canonical precompiled Dolt `2.3.2`; neither
selects a system Dolt or a `PATH` result.

Install the default-main development channel without `--ref`:

```text
paseo plugin add mcuadros/paseo-director
```

After the release coordinator publishes a tagged channel:

```text
paseo plugin add mcuadros/paseo-director --ref <alpha-beta-or-stable>
paseo plugin ls --json
paseo plugin reload director
paseo plugin update director
```

Paseo clones a candidate first. Every channel starts with the locked production
closure and verifier:

```text
npm ci --omit=dev --ignore-scripts --no-audit --no-fund
node tools/packaging/verify-install.mjs
```

The first command accepts only `package-lock.json`; it omits development
packages and disables dependency lifecycle scripts. The second proves the
installed production versions, real directories, exact registry/integrity
closure, dependency edges, audited lifecycle-script set, supported host, and
exact connector/channel identity. For unpublished main only, a narrow adapter
invokes the fixed direct-argv bootstrap build. The resulting Go bootstrap then
invokes the exact Engine build with `-trimpath`, `-buildvcs=false`, and
`-mod=readonly`, proves both executable identities, and atomically publishes
the private caches. JavaScript never invokes the Engine build. Neither layer
executes a repository lifecycle script or arbitrary command or reads runtime
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

The first Director RPC resolves only the static installed bootstrap pin and
executes its policy-free control command. The long-lived Go bootstrap has
already prepared, or now adopts, canonical Dolt and the selected main/release
Engine. Runtime and reload never invoke Go builds or depend on a source
checkout. Go owns the private XDG paths, loopback Engine and Dolt listeners,
credentials, schema bootstrap, TaskStore authority, locks, health checks,
child restart backoff, controlled update handoff, and 30-second plugin lease.
Reload renews the same controller and children; update hands off only when the
exact binding changes. JavaScript performs no download, Engine build,
supervision, signal, recovery, or fallback effect.

The trusted Go bootstrap generates an opaque public host identity and
persists it mode `0600` outside the checkout. Authenticated Director RPC returns
that identity to the client. Client-supplied host values are ignored; the same
server identity binds TaskStore bootstrap, Engine service, controller adoption,
and all connector requests. Reload, update, removal, and reinstall preserve it.
An identity change invalidates adoption and performs a controlled handoff while
retaining existing credentials and TaskStore data. The one exact legacy
`local-paseo` TaskStore binding migrates through Engine-owned bootstrap
authority with an owner-only backup; a foreign or ambiguous identity fails
closed without deletion or reseeding.

Activate or retry only through the public plugin lifecycle:

```text
paseo plugin reload director
paseo plugin ls --json
paseo plugin logs director --json
```

The daemon and unrelated agents and workspaces remain uninterrupted. Removing
the plugin stops lease renewal; the Go controller then stops its owned children
and preserves persistent data and verified caches.

Success requires plugin status `running` and the latest bounded log record
`code=DIRECTOR_ACTIVATION_READY`, `result=running-current`, the expected exact
connector commit, Engine/Dolt release identities, current supervisor binding,
and positive distinct Engine and Dolt process identities. A supervisor that is
merely present, reports `degraded`, has another binding, or has not proved both
owned children emits its deepest `DIRECTOR_RUNTIME_*` code and never reports
ready. A full Paseo restart or machine reboot is neither a supported
activation step nor a troubleshooting remedy, and reload does not stop
unrelated agents or workspaces.

The authenticated status RPC may return the Director host identity to its
client. Public logs, error text, and generic activation diagnostics never print
the raw identity, credentials, private paths, or compiler output.

## Tagged precompiled release assets

Schema-2 `release/engine.json` plus schema-1
`release/bootstrap-linux-amd64.json` pin the semantic version,
`linux-amd64`, exact reviewed source Candidate, canonical Director-owned GitHub
Release bootstrap/Engine/notices URLs and canonical Dolt release archive, plus
every archive and executable SHA-256. Redirects outside the GitHub asset boundary,
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

Release CI generates and verifies `THIRD_PARTY_NOTICES.txt` from the exact
source Candidate, shipped plugin closure, exact Go modules, and Go `1.26.5`
toolchain before passing it to the release builder with the canonical Dolt
archive and executable. Tagged installed plugins never generate notices or
invoke a compiler. Default-main notice generation and compilation occur only
in declared add/update preparation and never on reload. See
[licensing and third-party notices](licensing.md).

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
run `paseo plugin reload director`; repeating reload is safe.

Historical incident snapshot `3168ea518f4fb400551fca8221d6477466f8a2cd`
is not a reviewed rollback target and must not be installed.

To deliberately roll back, first record the current commit and confirm external
state is healthy, then remove the connector and verify the public registration
is absent before installing a previously reviewed immutable tag or exact
commit. Paseo 0.7.2 can leave the registration behind after the first
successful removal, so issue the same public, name-based removal a bounded
second time:

```text
paseo plugin remove director --json
paseo plugin ls --json
# Only if the director entry remains:
paseo plugin remove director --json
paseo plugin ls --json
paseo plugin add mcuadros/paseo-director --ref <reviewed-tag-or-commit>
```

Run the second remove only when the first `plugin ls` still contains
`director`; a repeated rollback therefore does not turn an already-absent
plugin into an error. For either remove, stop on any other error. Run `add`
only when the following `plugin ls` contains no `director` entry. If the entry
remains after two removes, stop and keep the preserved data untouched; do not
use a private identifier, edit Paseo state, or delete Director data to force
reinstall.

This replaces only Paseo's managed connector checkout. The XDG release cache,
managed TaskStore, Organizer,
Project configuration, and recovery material are preserved; no hidden data
migration is required. If their identity cannot be proved, stop instead of
deleting or overwriting them.

For pre-load or activation failure, use `paseo plugin ls --json` and
`paseo plugin logs director --json`. `DIRECTOR_MAIN_GO_TOOLCHAIN_MISSING`,
`DIRECTOR_MAIN_GO_TOOLCHAIN_VERSION`, `DIRECTOR_MAIN_BUILD_FAILED`,
`DIRECTOR_BOOTSTRAP_EXTERNAL_OWNER`, `DIRECTOR_BOOTSTRAP_PORT_OCCUPIED`,
`DIRECTOR_BOOTSTRAP_ENGINE_ADDRESS_MISMATCH`,
`DIRECTOR_BOOTSTRAP_FOREIGN_OWNER`,
`DIRECTOR_BOOTSTRAP_INSTALL_NOT_PREPARED`,
`DIRECTOR_RUNTIME_CHILDREN_NOT_READY`, `DIRECTOR_RUNTIME_BINDING_MISMATCH`, and
`DIRECTOR_ACTIVATION_FAILED` are bounded path-free causes. A host already
running a Director accepts a second one only as the isolated runtime described
in the [Operator guide](operator-guide.md); `DIRECTOR_BOOTSTRAP_EXTERNAL_OWNER`
reports another Director runtime holding a required listener, while
`DIRECTOR_BOOTSTRAP_PORT_OCCUPIED` reports a named port held by something that
is not one. Correct the release
or runtime condition, then invoke only the public plugin-scoped
reload/update command. Director Home, Doctor, and Repair are unavailable until
the connector loads; once available, Doctor remains read-only and Repair does
not install, reload, restart, signal, or rewrite plugin state.

## Removal

```text
paseo plugin remove director
```

Paseo removes its managed Git checkout. Lease expiry stops the owned Go
controller, Engine, and Dolt processes. It intentionally does not delete the XDG release
cache, managed TaskStore, Organizer,
Project repositories, or recovery state. Their later removal is a separate,
explicit owner operation with its own identity, backup, and cleanup checks.

If activation reports `DIRECTOR_TASKSTORE_CONFIG_OUTPUT_MISMATCH`, the existing
private configuration output differs from the configuration the verified
runtime would write. This refusal concerns only that output file: the
TaskStore/database is not the cause and must not be deleted. After the failed
startup has stopped its owned children, move the existing configuration file
to an owner-only backup, repeat the same plugin reload to generate a
replacement, compare the two files without printing credentials, then
deliberately restore the backup or adopt the replacement and reload once.
Director never overwrites, removes, or silently chooses between the files.

`ENGINE_HOME_HOST_FACT_REJECTED` identifies an invalid operational observation
without collapsing it into generic `HOME_UNAVAILABLE`. Its `field`, `expected`,
and `observed` values come from closed vocabularies and contain no rejected raw
value, identifier, path, or credential. Correct the named observation producer
and retry the same exact host; Director never selects another host.

Common bounded preparation codes include
`DIRECTOR_INSTALL_PASEO_UNSUPPORTED`, `DIRECTOR_INSTALL_PLATFORM_UNSUPPORTED`,
`DIRECTOR_INSTALL_TARGET_UNSUPPORTED`, `DIRECTOR_INSTALL_LIBC_UNSUPPORTED`,
`DIRECTOR_INSTALL_NODE_UNSUPPORTED`,
`DIRECTOR_INSTALL_DEPENDENCY_AUDIT`, and `DIRECTOR_INSTALL_LICENSE_AUDIT`.
Engine resolution uses corresponding
`ENGINE_*`, `DOLT_*`, and `DIRECTOR_RUNTIME_*` codes for metadata, fetch, size,
digest, archive, identity, permission, cache, ownership, and supervision failures.

The complete support boundary and reporting routes are in
[SUPPORT.md](../SUPPORT.md), [the compatibility policy](compatibility.md), and
[SECURITY.md](../SECURITY.md).
