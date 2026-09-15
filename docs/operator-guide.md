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
| `DIRECTOR_BOOTSTRAP_EXTERNAL_OWNER` | A required listener belongs to an unproved process. Preserve it, identify ownership, and retry only after the conflict is safely resolved. |
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
