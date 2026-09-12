# ADR-0022: Use an exact routine grant for TaskStore maintenance

- **Status:** Accepted
- **Date:** 2026-09-12
- **Beads Task:** `dir-m6.12`
- **Decision owner:** Human project owner
- **Amends:** PLAN header and §8.1;
  [ADR-0004](0004-select-direct-dolt-taskstore.md) and
  [ADR-0012](0012-direct-dolt-operational-readiness.md) for runtime global-safe-mode
  correction and backup authority
- **Preserves:** [ADR-0014](0014-practical-linux-agent-boundary.md),
  [ADR-0015](0015-idempotent-command-effect-contract.md), M5.8 maintenance and
  retention, M5.9 secret safety, and the separately supervised Dolt topology

## Context

The first maintained installation exposed two release blockers. A database-
scoped `ALL` grant on the normal Engine control identity could not execute
`DOLT_BACKUP`; the prior evidence had used global `ALL` with grant option.
When that first call was denied, the exact owner-created backup directory and
manifest remained empty and the durable maintenance record entered
`backup_handoff_ambiguous`. Correcting the grant and restarting could not
recover it.

Dolt 2.3.2 registers `dolt_backup` as `AdminOnly`. Its exact pinned
go-mysql-server dependency implements `AdminOnly` as either global `SUPER` or
an explicit routine-scoped `EXECUTE` grant. It deliberately does not accept a
database-wide `EXECUTE` grant for an admin-only routine. See the exact
[Dolt 2.3.2 registration](https://github.com/dolthub/dolt/blob/v2.3.2/go/libraries/doltcore/sqle/dprocedures/init.go)
and pinned
[routine authorization](https://github.com/dolthub/go-mysql-server/blob/da4d8ec733de/sql/planbuilder/auth_default.go).

## Decision

Production uses four authority lifetimes and three persistent runtime
principals:

1. The TaskStore bootstrap owner is transient. It creates or repairs the schema
   and runtime identities, reads the resulting grants, seals the exact grant
   digest, makes the Dolt privilege file an owner-only regular mode-`0600`
   file, seals its digest, and then leaves production configuration.
2. The control principal receives only the source-table read/migration
   operations and the two exact store-derived validation/recovery database
   capabilities. It receives no global privilege, `ALL`, `SUPER`, user/role
   administration, grant option, or `DOLT_BACKUP` execution.
3. The writer principal retains ADR-0012's exact table-scoped reads and DML. It
   receives no DDL, global privilege, grant option, or backup execution.
4. The maintenance principal receives only explicit `EXECUTE` on the source
   database's `dolt_backup` routine. It cannot select TaskStore rows, create or
   alter tables, grant privileges, or call another admin-only Dolt routine.

The three runtime principals and their server-resolved `user@host` identities
must be distinct. All connect to the same exact database on an explicit
loopback listener. The private production configuration binds those identities,
the grant-contract digest, and the canonical owner-only Dolt privilege file and
digest. Runtime startup, every TaskStore write, and every maintenance control
connection fail closed if the privilege file is missing, replaced, linked,
not mode `0600`, too large, changed, or no longer matches the bootstrap
attestation. Operator grant repair must therefore use the supported typed
TaskStore bootstrap; users never receive or run raw grant SQL.

ADR-0004 previously required the runtime control principal to set the global
safe-commit value. Dolt 2.3.2 authorizes that through global administrator
authority, which conflicts with this Task. The separately supervised Dolt
configuration now owns the global value of zero. Each runtime connection sets
its own session value to zero and reads back both values before mutation. Any
global drift fails closed and must be corrected through supervised bootstrap;
the Engine does not acquire global authority to repair it.

Backup and restore calls use only the maintenance connection. Source and
restored-state fingerprinting remains on the scoped control connection.
Validation and retained recovery use two fixed database names derived from the
store identity so bootstrap can grant their exact scope before they exist.
Dolt 2.3.2 requires database-scoped `SHOW TRIGGERS` for least-privilege trigger
inspection; direct `information_schema.triggers` reads are not admitted.
Validation-database deletion remains bound to that selected exact database,
matching Dolt 2.3.2's observed authorization behavior.

The filesystem boundary requires the Engine and its Director-owned Dolt server
to run under the same supported service identity: Dolt writes the Engine-
created mode-`0700` destination. The maintenance root, `backups`, and
`manifests` directories are canonical, owner-only, symlink-free, and pinned by
device/inode. Manifests, validation receipts, lock, state, credentials, and the
privilege file are mode `0600`. Another OS identity, ACL topology, remote
listener, or arbitrary backup root is not admitted.

## Denied-first-backup recovery

The only automatic recovery is the exact historical incident shape:

- one daily backup record, no migration and no other backup;
- the first backup attempt in `backup_handoff_ambiguous`;
- an exact schema with no aggregate, Candidate, Command, Event, audit, or
  append-only identity row, one initial Dolt commit, and only `main`;
- the unchanged source schema and fingerprint;
- the exact manifest-bound backup directory still owner-only and empty; and
- a current exact bootstrap privilege-file attestation and matching runtime
  principals.

The application persists a CAS transition and chained recovery-audit entry
before retry. The adapter revalidates every fact under its owner-only lock and
retries `DOLT_BACKUP` into that same proven-empty destination. It deletes and
overwrites nothing. A populated exact result is adopted even when the response
was lost. Validation is separately intent-recorded, restores into the fixed
fresh database, writes an exact owner-only receipt, and adopts that receipt
after response loss or restart. Recovery and validation each have two attempts.

Any row, extra branch/history, changed fingerprint, second backup, migration,
missing or changed attestation, absent/replaced/non-empty directory, malformed
manifest/receipt, symlink, owner/mode/device/inode drift, unavailable fact, or
exhausted attempt remains `Needs you`. A valid backup is never overwritten and
unowned content is never removed. The private recovery audit is hash-chained,
append-only across maintenance CAS, and contains only bounded codes, attempts,
times, and evidence digests.

## Evidence and tradeoff

The maintained direct test starts only disposable Dolt 2.3.2 servers and proves
the exact routine grant, denied control/writer calls, denied maintenance data/
DDL/grant/other-admin calls, full backup and fresh restore, broad-grant repair,
privilege-file mode/drift refusal, the denied-first frontier, in-place recovery,
response-loss adoption, a hard process kill and restart, 32 reconcilers,
non-empty refusal, symlink/mode attacks, and exact validation cleanup.

This is least privilege within ADR-0014's trusted Engine boundary, not a sandbox
against a compromised Engine or root. The Engine still holds three scoped
runtime credentials and can invoke the typed maintenance adapter. A compromised
Engine could misuse the exact backup routine's arguments; containing that case
would require the separate closed maintenance process described in the Task and
a new trust-boundary decision. No agent, connector, UI, repository, prompt,
TaskStore row, diagnostic, or support bundle receives a credential, SQL surface,
private path, raw grant, or raw Dolt output.
