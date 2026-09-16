# Operator guide

Director 1.0 is a single-user Linux service for exact stable Paseo `0.7.2`.
Director for Paseo supplies the trusted TypeScript UI/RPC integration; a
long-lived Go bootstrap owns runtime policy and supervises the standalone Go
Director Engine plus direct Dolt `2.3.2`.

## Supported topology and authority

The supported target is Linux amd64 with glibc, Node.js 22 or newer, and npm
with lockfile-version-3 support. Default `main` add/update needs exact Go
`1.26.5` and an existing Go-sum-verified module cache. A published tag needs no
Go or Git, but no alpha, beta, or stable channel has been published yet.

Each Director server RPC receives the daemon user's complete public
`PaseoApi` from the trusted plugin handler. Director uses that handler-scoped
object only. There is no daemon URL, copied daemon credential, secondary
WebSocket, service unit, system Dolt, or system-runtime `PATH` discovery.

The Go bootstrap owns private XDG state, an opaque persistent Director host
identity, runtime credentials, locks, leases, exact Engine/Dolt children,
TaskStore bootstrap, health/backoff, recovery, and controlled update handoff.
TypeScript launches only the pinned bootstrap control command and owns none of
those policies.

One Director runtime per user is the supported default on a host. It binds
`127.0.0.1:3307` for Dolt and `127.0.0.1:7041` for the Engine, and refuses to
start a second runtime that would contend for either listener or for the same
private state. Running a verification or staging Director beside a production
one is supported only through the isolated second runtime described below;
there is no other supported way to run two of them on one host.

## Install, update, and restart-free activation

Every Git candidate starts with this committed preparation:

```console
npm ci --omit=dev --ignore-scripts --no-audit --no-fund
node tools/packaging/verify-install.mjs
```

The lockfile is authoritative; package lifecycle scripts are disabled;
`@getpaseo/client@0.7.2` is installed from the verified seven-package
production closure and is not vendored.

- Unpublished default `main` is an explicit source-testing channel. Declared
  add/update preparation builds the bootstrap once, then the bootstrap builds
  the exact Engine once. Runtime and reload never compile.
- A published alpha/beta/stable tag ships a pinned precompiled bootstrap. It
  downloads and verifies only the precompiled Engine, exact-source notices,
  and canonical Dolt. It never compiles or falls back to source.

Use only the public plugin lifecycle:

```console
paseo plugin update director
paseo plugin reload director
paseo plugin ls --json
paseo plugin logs director --json
```

Installation, update, configuration, recovery, and troubleshooting must never
stop or restart the Paseo daemon or machine. Plugin-scoped reload/update keeps
unrelated agents and workspaces running. Reload adopts or retries prepared
artifacts; a changed update uses a controlled binding handoff.

Readiness requires plugin state `running` and
`DIRECTOR_ACTIVATION_READY`/`running-current` with the expected connector,
bootstrap, Engine, Dolt, controller, and contract identities.

## TaskStore and private state

The bootstrap provisions a fresh store only when no managed store exists. It
creates three distinct runtime principals:

- control: bounded reads, migrations, and exact validation/recovery databases;
- writer: exact table-scoped reads and DML; and
- maintenance: only the source database's exact `dolt_backup` routine.

The transient bootstrap owner seals the grant-contract and privilege-file
digests, then leaves runtime configuration. The Engine receives only the three
scoped identities. Node, agents, the UI, Organizer Git, logs, diagnostics, and
support bundles receive no SQL credential, raw SQL surface, private path, or
grant text. Operators use the typed bootstrap; they never run raw grant SQL.

Runtime configuration, credential files, privilege attestation, identity,
maintenance state, and backups are owner-only and outside repositories. A
missing, linked, replaced, broad-mode, wrong-owner, stale, or mismatched file
fails closed. Never repair identity or TaskStore ambiguity by deleting or
reseeding state.

## Running a second isolated runtime

A verification or staging Director may run beside a production one on the same
host. The second runtime is isolated rather than shared: it declares its own
loopback ports and keeps its own private state, and the existing instance keeps
running untouched. Starting one never requires stopping, moving, or
reconfiguring the installation already present.

Give the second runtime both loopback ports in its environment:

```text
DIRECTOR_RUNTIME_DOLT_PORT=13307
DIRECTOR_RUNTIME_ENGINE_PORT=17041
```

Set `XDG_RUNTIME_DIR`, `XDG_CACHE_HOME`, `XDG_CONFIG_HOME`, and `XDG_DATA_HOME`
in that same environment to absolute private directories outside the default
installation's Director roots, and give every one of those directories to this
instance alone. Both ports must be present, unprivileged, and different from
each other.

That environment must be the one the Paseo daemon hosting this second Director
runs with. The declaration places the whole instance, not only its child
processes: Director resolves its Engine endpoint from the same two variables
its bootstrap consumes, so the second instance's Board, Home, Task detail,
Inspector, Doctor, Operations and Repair all reach the Engine on the declared
port. Director then refuses to adopt a runtime whose Engine serves a different
address, so the surface and the runtime cannot drift apart.

Isolation is all or nothing, because a partial one would contend instead of
coexist:

- one port without the other, an invalid port, or ports without all four
  private XDG bases fail closed as `DIRECTOR_BOOTSTRAP_ISOLATION_INCOMPLETE`,
  in the connector as well as in the runtime, so a partial declaration never
  falls back to the default port where a running instance answers;
- an isolated runtime never releases a live runtime it finds at its own paths.
  It refuses with `DIRECTOR_BOOTSTRAP_ISOLATION_OCCUPIED`; only the default
  installation performs the controlled update handoff;
- an isolated runtime refuses a directory another Director runtime on this
  host is keeping its state in, with `DIRECTOR_BOOTSTRAP_ISOLATION_PATH_IN_USE`.
  Director reads where a running instance actually is from its own supervised
  children rather than assuming it sits at the default location;
- an isolated runtime refuses a data directory that already holds a TaskStore
  which is not its own, with `DIRECTOR_BOOTSTRAP_ISOLATION_FOREIGN_TASKSTORE`.
  Reusing another instance's `XDG_DATA_HOME` is caught there rather than at the
  Dolt server lock;
- an isolated runtime refuses an existing TaskStore configuration that does not
  name the Dolt listener it declared, with
  `DIRECTOR_BOOTSTRAP_ISOLATION_TASKSTORE_ADDRESS`. That is what stops a
  configuration reached through another instance's `XDG_CONFIG_HOME` from
  putting this Engine on that instance's TaskStore, and it also refuses a
  declared Dolt port that has changed under an existing configuration;
- the occupied-listener refusal is unchanged for an isolated runtime. A
  declared port that is already held refuses exactly as the fixed pair does.

Every one of those refusals happens before any child process starts, so a
rejected declaration leaves nothing running and nothing written.

The two runtimes then share no TaskStore, host identity, credentials, control
socket, launch lock or XDG state path, and neither can read or change the
other's state. A prepared-engine cache is the one thing they may share safely:
it is content-addressed and read only while a runtime is running. An isolated
runtime resolves against its own TaskStore and serves its own Director surface
with its own content; a new one starts empty and shows none of the other
instance's Projects or Tasks.

Director enforces that separation rather than assuming it, with one exception
worth knowing: a configuration directory belonging to an instance that is **not
running** and has not yet written a TaskStore configuration carries nothing
Director can read to tell it apart from an unused one. Give each runtime its
own four directories and that case cannot arise.

## Health, Doctor, and Repair

Director Home derives `Healthy`, `Degraded`, `Paused`, and `Needs you` from
current Engine facts. Missing or unavailable evidence is not healthy.

- **Doctor** is read-only and reports exact host/runtime, TaskStore, Organizer,
  lease, sync, Workspace, provider, MCP, isolation, and resource facts.
- **Repair Project…** is a two-step non-destructive Preview/Apply for only the
  advertised Git sync, TaskStore sync, lease, or owned Workspace recovery.
- **Reconcile now** requests the ordinary fact scan and cannot bypass an
  unknown in-flight effect.
- Terminal events are wake signals. External facts are observed every 30
  seconds while work is active and every five minutes while idle. The
  five-minute multi-source watchdog is the only lost-event recovery path.

Do not use plugin memory, one native status, log text, or elapsed time as
completion, termination, retry, or cleanup evidence.

## Sync, backup, restore, and retention

**Sync now** preserves Organizer Git and TaskStore/Dolt as two separate
effects. If one succeeds and one fails, the result is Partial; recovery does
not repeat the successful half, overwrite newer state, or switch remotes.

Director creates one daily TaskStore backup and retains validated backups for
seven days. A backup is accepted only after a fresh restore matches schema,
triggers, rows, identities, branches, and working-set state. Migration pauses
Projects, creates and validates its own backup, rechecks the paused source,
applies one supported migration, and resumes only after proof. Never copy a
live Dolt data directory or force a restore over the active store.

Below the 10% free-space floor, maintenance may expire only eligible backups
and compact only known bounded logs. It then observes again and stops startup
or launches if the floor remains violated. Disk pressure never authorizes
deletion of agents, Workspaces, worktrees, refs, recovery artifacts, or
unintegrated work.

## Audit, logs, and support bundles

Audit shows at most a bounded Project-scoped window of immutable event
categories, actor class when proven, outcomes, and pseudonymous fingerprints.
It contains no event payload, source, path, or raw value.

Technical logs use a closed level/component/code/timestamp/count shape. Each
Project log is mode `0600`, retained for 14 days, and capped at 100 MiB. Raw
Git, Dolt, provider, process, MCP, or command output has no field.

A support bundle requires **Preview local redacted support bundle**, then
**Generate local redacted support bundle** over the unchanged Preview. The
mode-`0600` ZIP contains only `manifest.json`, `health.json`, `audit.json`, and
`logs.json`. It excludes source/diffs, prompts/conversations, environment,
credentials, raw output, TaskStore rows, Organizer contents, full paths, and
remote URLs. Director has no telemetry and never uploads the bundle.

## Troubleshooting without restarts

| Symptom or code | Safe response |
|---|---|
| `DIRECTOR_MAIN_GO_TOOLCHAIN_MISSING` or `DIRECTOR_MAIN_GO_TOOLCHAIN_VERSION` | Install exact Go `1.26.5` for default-main add/update; do not change channels implicitly. |
| `DIRECTOR_MAIN_BUILD_FAILED` | Preserve the prior candidate and verified cache; correct the exact source/module-cache condition and retry update. |
| `DIRECTOR_BOOTSTRAP_INSTALL_NOT_PREPARED` | The installed checkout lacks the exact prepared bootstrap pin; rerun the same public update, then plugin-scoped reload. |
| `DIRECTOR_BOOTSTRAP_EXTERNAL_OWNER` | Another Director runtime holds a required listener; the refusal names the port and the holding process. Preserve it, and start this runtime as an isolated second runtime instead. Never stop, signal, or delete the instance that owns it. |
| `DIRECTOR_BOOTSTRAP_PORT_OCCUPIED` | A required listener named in the refusal is held by a process that is not a Director runtime, or by one this user cannot read. Nothing was started or signalled. Free that port or give this runtime its own isolated ports. |
| `DIRECTOR_BOOTSTRAP_ISOLATION_INCOMPLETE` | The isolated runtime declaration is partial. Declare both ports, valid and different, together with all four private XDG bases outside the default installation. The existing instance is unaffected. |
| `DIRECTOR_BOOTSTRAP_ISOLATION_OCCUPIED` | A live runtime with a different binding already owns these isolated paths. An isolated runtime never releases it; use that runtime's own lifecycle, or choose a different private XDG root. |
| `DIRECTOR_BOOTSTRAP_ISOLATION_FOREIGN_TASKSTORE` | The declared `XDG_DATA_HOME` already holds a TaskStore this runtime does not own. Give the isolated runtime its own data directory; never point two runtimes at one TaskStore. |
| `DIRECTOR_BOOTSTRAP_ISOLATION_TASKSTORE_ADDRESS` | The TaskStore configuration in the declared `XDG_CONFIG_HOME` names a Dolt listener this runtime did not declare, so it belongs to another runtime or to an earlier declaration. Give this runtime its own configuration directory, or restore the port it was provisioned with. |
| `DIRECTOR_BOOTSTRAP_ISOLATION_PATH_IN_USE` | A Director runtime already running on this host keeps its state in one of the declared directories. Choose directories no other runtime is using; never make a second runtime share them. |
| `DIRECTOR_BOOTSTRAP_ENGINE_ADDRESS_MISMATCH` | The runtime serves an Engine address this instance's surface is not bound to. Correct the declared ports so the whole environment agrees; Director refuses rather than query another instance's Engine. |
| `DIRECTOR_BOOTSTRAP_FOREIGN_OWNER` | Controller state or handoff identifies another owner. Preserve all state and reconcile the exact owner; never kill or delete it. |
| `DIRECTOR_RUNTIME_BINDING_MISMATCH` | The connector and controller bindings differ. Preserve both and retry the supported update/handoff path. |
| `DIRECTOR_RUNTIME_CHILDREN_NOT_READY` | Engine or Dolt ownership/readiness is incomplete. Inspect the deepest bounded bootstrap code and retry plugin-scoped reload after correction. |
| `DIRECTOR_TASKSTORE_CONFIG_OUTPUT_MISMATCH` | Back up the existing private output, regenerate through the identical bootstrap, compare without printing credentials, and deliberately choose which owner-only file to retain. Do not delete the database. |
| `ENGINE_HOME_HOST_FACT_REJECTED` | Correct the named closed `field`/`expected`/`observed` producer and retry the same Director host. |
| UI is stale or offline | Refresh the exact host, run Doctor, then use Reconcile now when offered. Never select another host implicitly. |
| Git/Dolt sync is Partial | Inspect both stream results and retry Sync; never manually repeat or overwrite the successful half. |
| A Run is ambiguous | Preserve every agent, Workspace, worktree, ref, and artifact and follow the typed Needs-you action. Never resend a possibly delivered prompt. |

For rollback/removal, follow the bounded public procedure in
[Installation and update](installation-update.md). For ordinary support and
private security reporting, use [SUPPORT.md](../SUPPORT.md) and
[SECURITY.md](../SECURITY.md).
