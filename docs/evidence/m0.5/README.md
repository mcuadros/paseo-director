# M0.5 direct Dolt operations evidence

## Decision

**Go for the supported Linux topology under the ADR-0004 trusted-engine
boundary.**

The selected direct Dolt `2.3.2` TaskStore from
[ADR-0004](../../adr/0004-select-direct-dolt-taskstore.md) passed every in-scope
sync, partial-failure, shared-server recovery, backup/restore, migration,
configuration-drift, and installed-topology property.

The platform scope is fixed by
[ADR-0011](../../adr/0011-linux-only-platform-scope.md), and the operational Go
decision is [ADR-0012](../../adr/0012-direct-dolt-operational-readiness.md).

This result preserves the human-approved trusted-engine boundary. The fixture
generates credentials inside its own process and uses the implemented but
undocumented `DOLT_CLI_USER` and `DOLT_CLI_PASSWORD` environment interface only
inside the harness. Before any Dolt CLI invocation it creates and verifies an
owned `DOLT_ROOT_PATH` with `metrics.disabled=true` and applies the implemented
but undocumented `DOLT_DISABLE_EVENT_FLUSH` kill switch as exact-version
defense in depth. It scans owned files and same-user process environments,
emits no secret, and destroys every owned store and listener. The production
runbook does not depend on the undocumented credential variables. Agents and
prompts never receive generated values, raw connections, or database selection
authority.

## Reproduce

Prerequisites on the supported host:

- Dolt `2.3.2`;
- Git `2.47.3`;
- Node.js `v26.7.0`;
- Debian 13 amd64 with Linux `6.12.107+deb13-amd64`; and
- permission to bind an ephemeral loopback TCP port and create disposable data
  under the operating-system temporary directory.

Run one focused process per property from the repository root:

```text
node docs/evidence/m0.5/operations-contract.mjs remote-layout
node docs/evidence/m0.5/operations-contract.mjs partial-git-failure
node docs/evidence/m0.5/operations-contract.mjs partial-dolt-failure
node docs/evidence/m0.5/operations-contract.mjs shared-server-recovery
node docs/evidence/m0.5/operations-contract.mjs backup-restore
node docs/evidence/m0.5/operations-contract.mjs schema-migration
node docs/evidence/m0.5/operations-contract.mjs configuration-drift
node docs/evidence/m0.5/operations-contract.mjs supported-topology
```

The minimum reproduction launches those same eight isolated processes once
each and emits one redacted aggregate document:

```text
node docs/evidence/m0.5/operations-contract.mjs all
```

The captured Candidate output is
[`observed-output.json`](observed-output.json). Every property creates unique
mode-restricted storage, database names, users, credentials, and a listener; it
removes that storage only after all owned processes exit. No property opens the
repository development Beads database or port `3308`.

## Review correction and preserved evidence

The predecessor Candidate recorded in Beads ran the complete suite. The Fable
review found that Dolt CLI telemetry could detach after a client exited,
inherit the client environment, and recreate an owned root after cleanup. The
correction rejects that P2: every owned Dolt root now disables metrics before
its first CLI command, every command rechecks the setting, and cleanup asserts
that no owned metrics process or credential marker survives and that the root
does not reappear through bounded post-removal checkpoints.

The correction retains the prior operational scenarios and reruns only the
properties affected by the common Dolt-root/process boundary. Policy-only ADR
renumbering and base reconciliation do not trigger a duplicate long run.

## Primary sources

Sources were reread on 2026-09-06 and compared with the installed CLI:

- [Dolt 2.3.2 release](https://github.com/dolthub/dolt/releases/tag/v2.3.2)
  identifies exact source commit `f0feb35` and publishes the selected version.
- [Using remotes](https://www.dolthub.com/docs/sql-reference/version-control/remotes/)
  documents Git-backed remotes, default `refs/dolt/data`, the requirement for
  a seeded Git branch, and accepted `.git` URL forms.
- [CLI reference](https://www.dolthub.com/docs/cli-reference/cli/) documents
  `--ref`, `clone`, `fetch`, `push`, `pull`, `backup sync`, and `backup restore`.
- [Backups](https://www.dolthub.com/docs/sql-reference/server/backups/)
  distinguishes remotes from complete working-set backups, documents online
  `DOLT_BACKUP`, per-database backup configuration, fresh restore, overwrite
  behavior, and server state excluded from a database backup.
- [SQL procedures](https://www.dolthub.com/docs/sql-reference/version-control/dolt-sql-procedures/)
  defines `DOLT_BACKUP` and the explicit administrative grant boundary.
- [Running the server](https://www.dolthub.com/docs/sql-reference/server/)
  documents process-control shutdown, while
  [replication](https://www.dolthub.com/docs/sql-reference/server/replication/)
  documents multi-database `--data-dir` operation and the absence of a
  supported multi-primary OLTP solution.

The installed `dolt remote --help` independently names the Git-ref default;
`dolt backup --help` says snapshots include branches, tags, working sets, and
remote-tracking refs and warns that interrupted file-backup uploads can leave
unreferenced files. These exact-version observations are encoded in the
supported-topology property instead of assuming current web documentation
matches the binary.

## Evidence summary

| Property | Exact observation |
|---|---|
| Selected remote layout | A seeded bare Git repository simultaneously retained Organizer `refs/heads/main`, Dolt `refs/dolt/data`, and Dolt metadata `refs/heads/__dolt_remote_info__`. Ordinary Git and Dolt clones recovered only their respective data. |
| Git partial failure | Git failed while Dolt succeeded. A newer Git writer made blind retry reject non-fast-forward; fetch/rebase and push preserved both Git changes and the already-synced Dolt state. |
| Dolt partial failure | Dolt failed while Git succeeded. A newer Dolt writer made blind retry reject non-fast-forward; Dolt pull/merge and push preserved all rows and the already-synced Git state. |
| Shared-server recovery | Two independent clients committed to two databases on one authenticated server. `SIGKILL` plus restart retained both commits, omitted the open transaction, and allowed one exact retry. Offline `fsck` passed after graceful stop. |
| Backup and restore | Online `DOLT_BACKUP` restored to a fresh name without force. Schema, branch hashes, rows, and working-set status had one digest; both databases passed offline `fsck`. The backup scan found no source control-file digest, reserved filename, or plaintext credential marker. |
| Schema migration | Project state was paused; safe variables, constraints, live `information_schema` facts, and a restored backup were validated first. Mutations of the captured fact model covered non-binary and empty collation, width, nullability, prefix, and expression cases; all were rejected before migration. An injected migration failure recovered the exact paused version-one fingerprint. |
| Configuration drift | Startup and exact-connection checks allowed only safe values. Global/session drift, duplicate initialization, set/read failures, unexpected values, connection loss, and identity mismatch produced bounded health codes and zero writes or fallbacks. |
| Supported topology | Exact Dolt/Git/Node versions, installed CLI capabilities, direct-argv construction, and the Linux host tuple were recorded. |

Across the eight properties, all 163 Dolt process launches had matching
`metrics.disabled` and event-flush preflight checks. The 222 process snapshots
observed zero Task-owned metrics processes and zero generated credential
markers in surviving environments. Every property proved its owned root absent
at `0`, `10`, `25`, `50`, `100`, `250`, and `500` milliseconds after removal.
The P2 is corrected rather than accepted as residual risk.

## Required operational procedures

### Metrics and credential containment

1. Before the first Dolt CLI command, create the engine-owned
   `DOLT_ROOT_PATH/.dolt/config_global.json` with `metrics.disabled=true` and
   restrictive directory permissions.
2. Read back `metrics.disabled=true` before every CLI spawn. A missing,
   malformed, or drifted setting fails closed before the command. The Dolt
   `2.3.2` harness also asserts its implemented, undocumented event-flush
   environment kill switch as defense in depth.
3. Never place TaskStore credentials in an environment inherited by a Dolt CLI
   unless metrics have already been disabled in that exact root. The evidence
   harness uses undocumented CLI credential environment variables only for its
   disposable authenticated clients; production credential transport must be
   selected and documented independently.
4. After CLI exit and before cleanup, inspect same-user Linux processes for
   `dolt send-metrics`, the owned root, and generated credential markers. Stop
   all owned processes before deleting storage.
5. Recheck both the process table and root absence at bounded checkpoints after
   deletion. Any metrics process, surviving credential marker, or root
   reappearance is a failed cleanup, not an acceptable residual.

### Combined synchronization

1. Verify the configured Organizer repository identity and the selected Dolt
   database/ref before either effect.
2. Treat normal Git and Dolt push/pull as two independently durable effects.
   Persist `pending`, `success`, or a bounded failure code for each; never
   report the combined action successful until both are observed at their
   intended heads.
3. Reserve both `refs/dolt/data` and
   `refs/heads/__dolt_remote_info__` for Dolt. The latter is a required metadata
   branch in `2.3.2`, not Organizer content. Seed at least one ordinary Git
   branch before adding the Dolt Git remote.
4. On partial failure, retain the successful half. Reinspect both remote heads
   before retry. Never force; a non-fast-forward requires fetch plus explicit
   Git rebase/merge policy or Dolt merge/conflict handling.
5. Keep partial status visible until reconciliation proves both exact results.
   Git success is not evidence of Dolt success and vice versa.

### Shared-server restart

1. Stop effects, identify the owned server process and data/config directories,
   and wait for process termination. Preserve storage if termination or
   ownership is uncertain.
2. Restart exactly one server over the same data and configuration, then verify
   its database marker and authenticated identity before any write.
3. Set and verify the safe global commit mode; reconnect every client and run
   the exact-connection prewrite check.
4. Reconcile committed Command/Event facts. Treat an interrupted transaction as
   unknown until queried; retry only through the durable idempotency contract.
5. Run offline `dolt fsck --quiet` only after the server has stopped.

This proves restart of one shared server, not multi-primary replication or
automatic failover. Dolt documentation explicitly requires an external
transactional layer for multi-primary OLTP, and Director `1.0` permits only one
active Project engine.

### Daily backup and verified restore

1. Use `DOLT_BACKUP` online or a supported point-in-time block snapshot. Never
   copy live filesystem files directly.
2. Back up each database independently. Allocate a distinct owned destination
   for each retained daily point because synchronizing a backup replaces its
   branches and working sets; keep seven validated destinations for the
   default seven-day policy.
3. Restore each new backup to a fresh database name without `--force`, compare
   schema, branches, rows, working-set status, and application invariants, then
   run offline `fsck` before declaring it verified.
4. Delete an expired destination only after ownership, age, a newer verified
   backup, and retention policy are proven. Interrupted file-backup garbage is
   not deleted until Dolt's documented idle/grace-period rule is satisfied.
5. Measure the backup boundary: compare exact digests and filenames for the
   source server configuration, privilege/branch-control stores, client global
   config, and per-database config/state against every backup file, and scan the
   backup for credential markers. Dolt documentation defines those server and
   per-database control files as separate from database backup. Back up required
   non-secret configuration separately and rebootstrap credentials from the
   engine secret boundary; never place credential material in Organizer Git,
   TaskStore, prompts, evidence, logs, or support bundles.

### Schema migration

1. Pause the Project and reject new writers.
2. On the engine-only control connection set and read global
   `dolt_force_transaction_commit=0`. On the exact future write connection set
   session value `0`, then read both session and global values.
3. Validate constraints, current schema version, and every guarded identity
   from live `information_schema` facts. The `aggregates.id` gate requires a
   non-empty binary collation and covers ordered identity, octet width,
   `NOT NULL`, prefix absence, and expression absence. The focused negative
   matrix mutates the captured fact model; it does not claim to alter each live
   schema form. Any missing or unexpected fact blocks SQL.
4. Create and verify a fresh pre-migration backup as above.
5. Execute the declared migration once, using
   `UPDATE ... WHERE id = ? AND version = ?` for aggregates. ADR-0004 excludes
   aggregate `ON DUPLICATE KEY UPDATE`.
6. Verify schema version, data, constraints, guarded identities, and migration
   outcome before resuming. On any error, retain `paused`, mark TaskStore
   unhealthy, and restore the verified pre-migration backup to a fresh store.

### Fail-closed drift behavior

No write or migration begins after a failed set/read, unexpected value/type,
duplicate initialization, connection reset, database/listener identity
mismatch, or guarded-schema violation. There is no fallback identity,
connection, database, store, or delivery mode. Durable health output contains
only bounded codes such as `UNSAFE_GLOBAL_COMMIT_MODE` or
`TASKSTORE_IDENTITY_MISMATCH`; it excludes SQL, connection strings, raw server
output, and credentials.

## Supported topology and limits

Claims are bounded to Dolt `2.3.2`, Git `2.47.3`, Node `v26.7.0`, Debian 13
amd64 kernel `6.12.107+deb13-amd64`, a local seeded Git-backed remote, and an
authenticated loopback SQL server. No public remote was tested, so
forge-specific custom-ref protection and authentication remain deployment
preflight concerns rather than evidence here.
