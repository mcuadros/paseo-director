# TaskStore backup, migration, retention, and disk safety

The standalone Director Engine owns TaskStore maintenance. The Paseo connector,
UI, agents, repositories, and Organizer configuration never receive a Dolt
credential, raw SQL surface, backup path, or maintenance-state path.

## Startup and recurring operation

`director-engine serve-board` opens the exact configured direct-Dolt store and
an owner-only maintenance root under the platform cache. The root is bound to a
SHA-256 digest of the store ID, loopback listener, and database. Reusing a store
ID for another listener or database does not adopt old maintenance authority.
The root and backup directories are mode `0700`; the CAS ledger, lock, and
backup manifests are mode `0600`. Symlinks, replacements, owner drift, mode
drift, malformed JSON, checksum drift, and stale CAS revisions fail closed.

Before the Board listener opens, the Engine performs these steps:

1. Observe available blocks on the maintenance filesystem. At less than 1,000
   basis points (10 percent) free, expire only eligible backups and compact
   only known bounded technical logs, then observe again. Startup is refused if
   the floor is still violated or the observation is unavailable.
2. Inspect the exact TaskStore identity, Dolt `2.3.2`, schema, triggers,
   immutable guards, aggregate identity width/collation/index facts, data,
   branches, and working-set status.
3. Recover or run the one supported schema-1-to-schema-2 migration.
4. Expire validated backups at their exact seven-day deadline.
5. Reconcile the daily backup. A migration backup does not satisfy the daily
   schedule.

The Engine repeats disk, expiry, and daily reconciliation hourly. The daily
decision still uses the exact 24-hour interval, so an hourly wake cannot create
extra backups. Outside low-space refusal, recurring failures use the existing
path-free `taskstore_unhealthy` technical-log code. A low-space refusal writes
no log because the no-more-disk rule takes precedence.

The Run operational-limit evaluator independently rejects every configured
free-space floor below 10 percent and parks launch at 999 basis points. Exact
10 percent remains admissible. Project and Task configuration may raise the
floor but cannot lower it.

## Backup and restore proof

Before `DOLT_BACKUP` runs, the application records a sealed intent and exact
source schema/fingerprint. The adapter rechecks that source under the same
exclusive in-process maintenance fence used by TaskStore writes, creates one
unique owner-only directory, and writes an identity manifest outside the
backup directory. Response loss is reconciled from that exact directory and
manifest; an ambiguous, empty, replaced, or mismatched artifact is never
adopted.

A successful backup call is not validation. The Engine restores it without
force into a fresh deterministic validation database, verifies constraints,
and compares one SHA-256 fingerprint covering schema columns and indexes,
trigger bodies, every application table row in primary-key order, store
identity, schema metadata, Dolt branches, and working-set status. Only an exact
match becomes `validated`. The temporary validation database is removed only
after the proof succeeds.

## Migration and interruption recovery

Migration is an application-owned durable state machine:

1. Record the complete Project pause plan, including original state and CAS
   version, before changing a Project.
2. Apply audited `project.control.maintenance_pause` commands. Active and
   degraded Projects become paused; already-paused and archived Projects are
   unchanged.
3. Observe the paused store and create a migration-purpose backup.
4. Restore and validate that exact backup.
5. Immediately re-observe the live store. Schema SQL is unreachable unless the
   Project set is still exactly paused and the live schema/fingerprint equals
   the validated backup's source binding.
6. Apply schema SQL under the exclusive TaskStore mutation fence, verify the
   resulting schema and constraints, then apply audited resume commands.

Every effect records intent before handoff. A lost response is observed before
the two-attempt bound can be used; an already-applied exact schema is adopted
without applying it twice. A partial or contradictory schema never resumes a
Project. The validated backup is restored to a fresh recovery database, its
fingerprint is proven equal to the paused source, the Project remains paused,
and the state routes to human attention. The recovery database is not
automatically deleted.

Schema 1 Candidates predate exact admission evidence. Migration preserves each
row as explicit `director.candidate-legacy/v1` non-authoritative history and
clears its Run's current-Candidate projection. If an execution record existed,
its incompatible observation is removed and it receives the path-free
`schema_migration_candidate_revalidation_required` attention code. Migration
does not invent a claim or manifest; downstream work requires a fresh exact
Candidate.

## Retention and deletion boundaries

Validated daily and migration backups expire exactly seven days after capture.
Expiry first records a durable dispatch state, then verifies the private
manifest, store binding, directory device/inode, owner, mode, and canonical
location. Rename-to-quarantine and deletion are restart-safe. Unknown files,
aliases, replacements, validation databases with conflicting contents, and
fresh recovery databases are retained and route to attention.

Technical-log compaction touches only pseudonymous `project-<sha256>.jsonl`
files with the expected owner and mode, retains the bounded current tail, and
physically removes entries older than 14 days. Unknown files are ignored and a
known-name symlink or replacement refuses the pass.

This maintenance cleanup has no Git, GitHub, worktree, host-view, agent, or
recovery-ref port. The existing integration/closure cleanup state machine
remains the sole authority for those resources, so disk pressure cannot delete
unintegrated recoverable work.
