# ADR-0012: Approve direct Dolt operations on the supported Linux topology

- **Status:** Accepted
- **Date:** 2026-09-06
- **Beads Task:** `dir-m0.5`
- **Plan gate:** M0 TaskStore synchronization, backup, restore, and migration
- **Decision owners:** Human project owner and Director maintainers

## Context

ADR-0004 selected one Director-owned direct Dolt `2.3.2` schema behind the
human-approved trusted-engine boundary. ADR-0011 fixes Linux as the sole
Director `1.0` platform. Before M1, PLAN sections 8.1, 8.3, 8.4, 21.2, 23, and
25 require evidence that this store synchronizes, survives partial effects and
server interruption, backs up and restores without loss, and migrates safely
on that supported topology.

ADR-0004 also assigned two hard requirements to this spike:

- set and verify global safe commit configuration at startup/reconnect and set
  plus verify session/global configuration on the exact write connection;
  fail closed on every error, drift, or identity ambiguity; and
- extend migration validation to the later `aggregates.id` identity ledger,
  including collation, width, nullability, prefix, and expression semantics.

The human project owner fixed the product scope to the tested Linux topology.
The decision therefore evaluates only the supported runtime and does not infer
behavior for any untested operating system.

## Question or hypothesis

Under the ADR-0004 trusted-engine boundary, can direct Dolt `2.3.2` preserve
all Director TaskStore data through the selected combined remote layout,
partial Git/Dolt failure, shared-server restart, verified backup/restore, and
fail-closed schema migration on the supported Linux topology?

## Acceptance criteria

- Normal Git and Dolt state coexist at the selected remote without either
  stream overwriting the other.
- Either partial-failure direction stays visible and retryable; reconciliation
  never force-overwrites newer state or loses either writer.
- Shared-server restart preserves committed data, excludes interrupted
  transactions, supports replay-safe retry, and leaves owned resources clean.
- A complete backup is validated by a fresh restore, logical/schema/history
  comparison, and integrity check before it can authorize migration.
- Migration remains paused until configuration, constraints, schema guards,
  and the restored backup pass; failure retains a recoverable prior version.
- The exact write connection verifies safe session/global commit mode, and
  every configured error/drift path performs zero writes with no fallback.
- `aggregates.id` validation covers binary collation, octet width, non-null
  semantics, prefix and expression absence, and exact unique-key ordering.
- Exact installed tool and Linux runtime facts are recorded with complete
  Task-owned cleanup and no secret disclosure.

## Evidence

The complete procedure and operational runbook are in
[`docs/evidence/m0.5`](../evidence/m0.5/README.md). The executable is
[`operations-contract.mjs`](../evidence/m0.5/operations-contract.mjs), and the
minimum captured output is
[`observed-output.json`](../evidence/m0.5/observed-output.json).

### Versions and topology

- Dolt `2.3.2`, release/source commit `f0feb35`;
- Git `2.47.3`;
- Node.js `v26.7.0`;
- Debian 13 amd64, Linux `6.12.107+deb13-amd64`;
- a seeded Task-owned bare Git remote containing Organizer and Dolt refs; and
- unique authenticated loopback SQL servers with one or two disposable
  databases and independent client processes.

Primary Dolt documentation and the `2.3.2` release were reread on 2026-09-06.
The installed binary help was inspected separately to bind CLI semantics to
the selected exact version.

### Results

Eight focused Linux properties pass:

1. Organizer `refs/heads/main`, Dolt `refs/dolt/data`, and required Dolt
   metadata `refs/heads/__dolt_remote_info__` coexist. Git and Dolt clones
   recover only their own content.
2. Git-failed/Dolt-succeeded sync reports partial failure. A newer Git head
   rejects blind retry; fetch/rebase plus push retains local and remote Git
   changes and the already-synced Dolt state.
3. Dolt-failed/Git-succeeded sync behaves symmetrically. A newer Dolt commit
   rejects blind retry; pull/merge plus push retains every row and the
   already-synced Git state.
4. Two independent writers committed to two databases on one server. A forced
   server termination plus restart kept both commits, omitted the open
   transaction, accepted one retry, and passed offline `fsck`.
5. Online `DOLT_BACKUP` captured committed history and an uncommitted Dolt
   working set. A fresh restore matched one schema/branch/row/status digest and
   both stores passed offline `fsck`.
6. Migration paused the Project, verified safe configuration, constraints,
   live `aggregates.id` facts, and a restored backup. Mutations of the captured
   fact model covered non-binary and empty collation, width, nullability,
   prefix, and expression cases; all blocked execution. Injected migration
   failure recovered the exact paused version-one fingerprint; the valid path
   advanced schema and aggregate versions once before resume.
7. Exact-connection configuration checks allowed only two safe writes. Global
   and session drift, duplicate initialization, set/read errors, unexpected
   values, connection loss, and listener identity mismatch produced bounded
   health codes, no fallback, and zero unauthorized rows.
8. The installed Dolt/Git/Node versions, Git-ref support, working-set backup
   semantics, direct-argv process construction, and exact Linux host tuple were
   recorded for the supported topology.

The aggregate correction run made 163 Dolt process launches. Every launch had
matching persisted-metrics and event-flush preflight checks. Across 222 process
snapshots it observed zero Task-owned metrics processes and zero generated
credential markers in surviving environments. Each property proved that its
owned root remained absent at seven checkpoints through 500 milliseconds after
removal. This resolves the Fable P2 without accepting it as residual risk.

Every property used a fresh owned data root and process. Each owned
`DOLT_ROOT_PATH` set `metrics.disabled=true` before its first CLI call and
verified that setting before every later call. The exact-version harness also
sets and checks Dolt's implemented, undocumented event-flush environment kill
switch as defense in depth. Generated credentials were
absent from argv, surviving same-user process environments, server
configuration/output, and all scanned files. Cleanup observed no owned
`dolt send-metrics` process, stopped every owned process before deletion, and
proved the root did not reappear. The shared development Beads server,
unrelated repositories, and public remotes were not touched.

### Review correction

The Fable review of the predecessor Candidate found a detached Dolt telemetry
process that inherited the CLI environment and recreated an owned root after
cleanup. This Candidate rejects that P2 by configuring and checking
`metrics.disabled=true` before Dolt CLI use, inspecting same-user Linux process
environments, and checking root absence repeatedly after cleanup. The affected
properties and aggregate evidence are rerun; ADR renumbering and base
reconciliation do not cause additional long-property executions.

### Operational constraints discovered

- A Git-backed Dolt remote requires a seeded ordinary Git branch and uses both
  `refs/dolt/data` and `refs/heads/__dolt_remote_info__`. Both Dolt-owned refs
  must be reserved from Organizer policy and protected from unrelated cleanup.
- Git and Dolt sync are separate effects. Combined success is a projection of
  two observed exact heads, never an atomic transaction.
- Every engine-owned Dolt root must contain `metrics.disabled=true` before any
  CLI command. A command never receives credentials until this precondition is
  verified, and cleanup fails on an owned metrics process, a surviving
  credential marker, or root recreation.
- ADR-0004 append-only ledgers and triggers contain the row-level `INSERT`,
  `UPDATE`, `DELETE`, duplicate/ODKU, and `REPLACE` statement classes emitted by
  the trusted adapter. Dolt/MySQL `TRUNCATE` is DDL and does not execute
  row-level delete triggers, so it is outside that containment claim. The
  production runtime writer grant must exclude `DROP`, which also denies
  `TRUNCATE`. Its DML grants must be table-scoped: `guard_constants`,
  `parent_guard`, and `immutable_write_guard` are read-only to that identity,
  and bootstrap verifies their three exact seed rows before accepting an
  existing schema. Bootstrap/migration authority remains a separate
  engine-only control path. A privileged operator performing offline SQL
  inspection must keep the Project paused and preserve the verified
  backup/restore gate.
- Backup synchronization replaces all branches and working sets at its
  destination. Seven-day retention therefore requires distinct owned
  point-in-time destinations and verification before expiry cleanup.
- The measured backup contained no exact digest or reserved filename from the
  source server/configuration control files and no plaintext credential marker.
  Dolt documentation separately defines server configuration, users/grants,
  per-database state/config, and branch-control data as outside the database
  backup. Restore must rebootstrap credentials through the engine secret
  boundary and separately restore required non-secret authority data.
- The evidence proves restart of one shared server, not multi-primary or
  automatic failover. Those are outside Director `1.0` and not safely implied
  by Dolt remote synchronization.

## Alternatives considered

### Treat Git and Dolt synchronization as one atomic effect

Rejected. The supported tools expose separate operations and either half may
succeed alone. Persisting and reconciling two exact outcomes is the only result
that matches observed behavior.

### Copy live database files for backup

Rejected. Dolt documents live filesystem copying as potentially inconsistent.
The selected procedure uses online `DOLT_BACKUP`, restores to a fresh database,
compares logical and versioned state, and performs offline `fsck`.

### Use multi-primary or automatic failover

Rejected for `1.0`. The tested design has one active engine and one shared SQL
server. Remote synchronization is disaster-recovery state, not a safe
multi-primary transaction protocol.

## Decision

**Go.**

Use direct Dolt `2.3.2` for TaskStore operations on the exact supported Linux
topology under ADR-0004's trusted-engine boundary and the procedures below.
The public supported mechanism meets every in-scope acceptance criterion.

## Consequences

- The M0 TaskStore synchronization, backup, restore, migration, and
  shared-server recovery risk is resolved after independent approval of the
  exact Candidate.
- Director must model Git and Dolt synchronization as two independently
  journaled/reconciled effects and reserve the two observed Dolt-owned refs.
- Automatic daily retention needs unique backup destinations; migration needs
  a freshly restored and verified pre-migration backup.
- Database restore alone is insufficient server disaster recovery. Credential
  and authority bootstrap remains inside the trusted engine boundary and out
  of Git, TaskStore, prompts, evidence, and diagnostics.
- Metrics disablement and post-cleanup process/root checks are mandatory for
  every engine-run Dolt CLI operation.
- The `aggregates.id` coverage gap from ADR-0004 is closed for the tested
  validator contract and is a mandatory migration precondition.
- Other unresolved M0 risks and the normal independent-review/integration gates
  remain blocking; this ADR does not authorize M1 by itself.

## Independent verification

Exact Candidate `d55f7dbccad902bab15261ef98d22f8dcea2e2e3` received an
independent `approve_candidate` verdict and was integrated with its reviewed
base as merge `212dcbab176ac0ebbb84eec37610fdf23d9efe45`. `dir-m0.5`
records the synchronization, backup/restore, migration, telemetry, review,
integration, cleanup, and residual topology bounds. This status
reconciliation uses that integrated evidence and does not rerun the
operational experiment.
