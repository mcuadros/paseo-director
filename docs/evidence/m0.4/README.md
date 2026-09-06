# M0.4 TaskStore mapping evidence

This evidence evaluates Beads and the plan-approved direct-Dolt contingency as
Director's runtime `TaskStore`. It does not question Beads as the development
tracker for this repository.

## Reproduce

Prerequisites:

- Node.js (tested with `v26.7.0`);
- Beads `1.2.2` at commit `6c124203e771433a3550c348771a5b5e27fd3c21`;
- Dolt `2.3.2` at source commit
  `f0feb352b1d3f0919b88ecd28869e515afb60ee0`;
- Git;
- permission to bind an ephemeral loopback TCP port.

From the repository root, run:

```text
DIRECTOR_M04_FOCUS=referenced-parent-identities node docs/evidence/m0.4/taskstore-contract.mjs
node docs/evidence/m0.4/taskstore-contract.mjs
node docs/evidence/m0.4/verify-interruption.mjs
node docs/evidence/m0.4/verify-listener-isolation.mjs
```

The focused mode stops after the referenced-parent identity fixture and cleanup;
it does not run the unchanged slow adversarial timeout cases. The remaining
three commands are the complete Candidate chain.

The main contract creates one owned temporary directory containing an isolated
Dolt server, Beads and direct-Dolt databases, a credential-free Dolt client
configuration root, and a temporary Git repository. It configures no remote. It
settles every spawned client, terminates its recorded server child, and proves
the directory stays removed on success, assertion failure, `SIGINT`, or
`SIGTERM`.

The process exits zero only when every positive and negative observation
matches. A reported `FAIL` is a successfully reproduced product contract
failure, and `CONTROL` marks a deliberate negative control that justifies a
design choice. The captured outputs are
[`observed-output.json`](observed-output.json),
[`interruption-output.json`](interruption-output.json), and
[`listener-isolation-output.json`](listener-isolation-output.json). The three
executable `.mjs` files are the source of truth for commands, SQL, inputs, and
assertions.

## Tested topology

- Debian 13 amd64 Linux, kernel `6.12.107+deb13-amd64`;
- one isolated Dolt SQL server on an ephemeral `127.0.0.1` port;
- unique random database, user, and authentication identities per run;
- a pre-seeded HMAC marker verified through environment-authenticated owner
  credentials while the spawned PID remains live, before network DDL;
- no credential profile and no password in argv or captured output;
- short-lived clients receive `DOLT_CLI_USER` and `DOLT_CLI_PASSWORD` only in
  their process environment;
- unauthenticated root disabled and negatively probed;
- barriered streaming transactions for same-version contention and for
  same-identity appends;
- no Dolt remote, Git remote, shared Beads database, or public ref.

The test is a Linux runtime proof. Windows and sync, backup, restore, migration,
partial-failure recovery, and platform operations remain outside this Task and
belong to `dir-m0.5`.

## The append-only identity guard

The application identity holds `SELECT, INSERT` and no `UPDATE`, `DELETE`,
`TRUNCATE`, `ALTER`, `DROP`, or trigger-control privilege on the five immutable
tables `command_requests`, `command_outcomes`, `candidates`, `events`, and
`audit_entries`. It holds **`SELECT` only** on every identity ledger, on
`parent_guard`, on `guard_constants`, and on `immutable_write_guard`.

Grants alone are not enough. On Dolt `2.3.2`, `INSERT ... ON DUPLICATE KEY
UPDATE` rewrites an existing row through that identity, and a `BEFORE UPDATE`
trigger is never activated by that path. The contract therefore installs one
owner-definer `BEFORE INSERT` guard per immutable table. The current schema
derives a 535-byte maximum identity and rounds its provisioned width to 640; the
example uses that derived result, not a fixed-width contract:

```sql
CREATE TABLE events_identity (identity VARBINARY(640) NOT NULL PRIMARY KEY);

CREATE DEFINER = '<owner>'@'%' TRIGGER events_append_only
  BEFORE INSERT ON events FOR EACH ROW
  INSERT INTO events_identity (identity) VALUES
    (CONCAT('events.pk', CHAR(31), NEW.global_sequence)),
    (CONCAT('events.event_id', CHAR(31), NEW.event_id)),
    (CONCAT('events.run_sequence', CHAR(31), NEW.run_id, CHAR(31), NEW.sequence));
```

The guard runs before the row is written, so it intercepts the insert attempt
that the update path is derived from. A genuinely new record contributes new
identities and is appended. Any statement that would touch an existing record
repeats one identity and fails on the ledger's primary key. Dolt assigns the
auto-increment value before the trigger body runs, so auto-increment identities
carry their real value. Dolt `2.3.2` rejects `SIGNAL` and compound
`BEGIN ... END` trigger bodies, which the contract probes and records, so each
guard is one supported multi-row `INSERT`.

The ledger grant is `SELECT` only. Dolt `2.3.2` executes a trigger body's ledger
`INSERT` whenever the invoker holds some privilege on the ledger, so `SELECT` is
both sufficient for genuine appends and the minimal posture. An earlier revision
of this evidence granted `INSERT` and was wrong: with that grant the application
could rename a ledger identity through `ON DUPLICATE KEY UPDATE`, free it, and
then mutate the protected row through an otherwise non-colliding alternative
identity. That two-step attack is now part of the matrix and is denied at both
steps.

The ledger key width is derived from `information_schema` octet lengths instead
of being fixed. The current schema needs 535 bytes and provisions 640, so a
128-character four-byte identifier stores its full 528-byte identity under both
strict and relaxed session `sql_mode`; the previous fixed `VARBINARY(512)` would
have rejected or truncated it.

## Referential guards

Foreign-key constraints alone are not a boundary here, because the application
identity can set `foreign_key_checks=0` for its own session. Every declared
foreign key therefore also has an owner-definer guard that inserts a pre-seeded
sentinel identity when the parent row is absent:

```sql
CREATE DEFINER = '<owner>'@'%' TRIGGER fk_audit_event_present
  BEFORE INSERT ON audit_entries FOR EACH ROW
  INSERT INTO parent_guard (identity) SELECT 'guard.parent_missing' FROM guard_constants
    LEFT JOIN events AS guarded_parent ON guarded_parent.event_id = NEW.event_id
    WHERE NEW.event_id IS NOT NULL AND guarded_parent.event_id IS NULL;
```

The contract verifies that all 11 declared foreign keys, read back from
`SHOW CREATE TABLE`, have a guard, and that `aggregates` carries the same guard
on `UPDATE` because it is the one application-updatable table.

Child-side existence guards do not stop a referenced key from being renamed
when foreign-key checks are disabled. Every referenced parent key therefore has
an identity ledger. For mutable `aggregates`, a `BEFORE INSERT` ledger guard
rejects insert-derived ID changes and a conditional `BEFORE UPDATE` guard
rejects only `NEW.id != OLD.id`; unchanged-ID version/data updates remain valid.
The application has `SELECT` only on the aggregate ledger and the conditional
update sentinel.

## Guarded identities

The contract reads every unique index of the five immutable tables from
`information_schema.statistics` and fails unless the guarded set is exactly the
declared set. Comparison is not limited to column membership: it checks ordered
index position, column data type, octet width, `NOT NULL` on both the index and
the column, absence of prefix and expression indexes, and collation. A column
whose collation does not end in `_bin` fails closed, because a case- or
accent-insensitive key matches values that the binary ledger keeps apart. The
current schema declares nine identities and leaves none uncovered:

| Table | Declared unique identities |
|---|---|
| `command_requests` | `idempotency_key` (primary key) |
| `command_outcomes` | `idempotency_key` (primary key) |
| `candidates` | `id` (primary key); `run_id, sequence` (composite) |
| `events` | `global_sequence` (auto-increment primary key); `event_id`; `run_id, sequence` (composite) |
| `audit_entries` | `sequence` (auto-increment primary key); `audit_id` |

The `events` composite exists precisely because guarding `event_id` alone is
bypassable through an explicit run-scoped sequence or an explicit primary key.

## Adversarial matrix

For every table and every declared identity the contract executes and snapshots:

1. plain duplicate;
2. `INSERT IGNORE`;
3. `INSERT ... ON DUPLICATE KEY UPDATE` with literal assignments;
4. the same with `VALUES()` assignments;
5. `INSERT IGNORE ... ON DUPLICATE KEY UPDATE`;
6. `INSERT ... SELECT ... ON DUPLICATE KEY UPDATE`;
7. multi-row `ON DUPLICATE KEY UPDATE` pairing a brand-new row with a colliding
   row.

Per table it also executes `REPLACE`, `UPDATE`, `DELETE`, `TRUNCATE`, `ALTER`,
`DROP TRIGGER` on the guard, `CREATE TRIGGER` to replace the guard, and
`DELETE`, `TRUNCATE`, `DROP`, and `DROP PRIMARY KEY` against the identity
ledger.

The run captured in [`observed-output.json`](observed-output.json) records 128
base-table observations: 118 denials and 10 controlled appends. Every denial
preserved the protected row byte for byte and left the table row count
unchanged, including the multi-row form whose new companion row was not
appended. A distinct append before and after each table's matrix succeeded,
which proves the guard rejects collisions rather than all writes.

The ledgers and guard tables are attacked directly as well: 117 statements
against the six identity ledgers, `parent_guard`, `guard_constants`, and
`immutable_write_guard` covering unused-identity reservation, `INSERT IGNORE`,
`ON DUPLICATE KEY UPDATE` in literal, `VALUES()`, `IGNORE`,
`INSERT ... SELECT` and multi-row forms, `REPLACE`, `UPDATE`, `DELETE`,
`TRUNCATE`, `DROP PRIMARY KEY`, and `DROP TABLE`. All were denied with
byte-identical guard state. The reviewer's two-step attack runs for all nine
immutable table/identity pairs: the ledger rename is denied, the follow-up base
`ON DUPLICATE KEY UPDATE` is denied by the guard, and both the row and the
ledger are unchanged.

Eighteen transaction-wrapped attacks are reported separately from the 128 base
observations and run last on purpose. Dolt `2.3.2` keeps the write locks of a
transaction whose client disconnects after a failed statement, so running them
earlier blocks later writers to the same table for the rest of the run.

Seventy-five denied statements did not return within their four-second bound and
were killed rather than producing an error. Their targets were byte-identical
afterwards, so this is a liveness defect rather than a mutation path; each one
is listed in `boundary_evidence.blocked_without_returning`.

Public Dolt version-control procedures that could rewrite committed state are
denied to the application identity: `DOLT_RESET`, `DOLT_CHECKOUT`,
`DOLT_REVERT`, `DOLT_BRANCH`, and `DOLT_COMMIT`.

## Referential and privilege coverage

Twenty-one orphan probes cover a Command outcome without its request, an Audit
entry without its Event, an Event and a Candidate without their Run, a
dependency without its aggregate, an aggregate whose parent is missing, and an
`UPDATE` that would repoint an aggregate at a missing parent. Each runs with
foreign-key checks enforced, disabled, and disabled under a relaxed `sql_mode`,
and all are denied; a valid append still succeeds with checks disabled, and no
orphan row exists afterwards. A child cannot commit against an uncommitted
parent, a rolled-back parent/child transaction leaves no row and no ledger
residue, and the identical work replays successfully afterwards.

A separate referenced-parent fixture derives all three parent keys from the
foreign-key model and seeds every declared child relationship. Eighteen direct
and insert-derived rename attempts cover each parent identity with foreign-key
checks enforced, disabled, and disabled under a relaxed `sql_mode`; every
attempt is denied and every child stays linked. Three positive controls update
aggregate version/data in those same modes while preserving the ID.

Eight privilege-expansion operations are denied: reading `mysql.user`, creating
a user, granting itself any privilege, granting itself ledger `INSERT`, dropping
or resetting the owner, and `ALTER USER CURRENT_USER()`. The identity can rotate
its own password with an explicit `ALTER USER` naming itself. The contract
performs that rotation, proves the old password stops working, the new one works,
no privilege changed, and the owner is unaffected, then restores the original
credential. It is an availability and recovery concern, not a privilege
boundary failure.

## Negative controls

These are recorded as `CONTROL` because they justify the design, not because the
contract fails:

- a `BEFORE UPDATE` guard on a probe table rejects a direct owner `UPDATE` and
  preserves the row, then the same owner changes that row through
  `ON DUPLICATE KEY UPDATE` with exit `0` and an unchanged guard row, so the
  update path activates no `BEFORE UPDATE` trigger for any identity;
- a table with no unique index survives that statement only by appending a
  second row for the same identifier, which removes the uniqueness the contract
  requires;
- a deliberately drifted table whose `UNIQUE` key uses a case-insensitive
  collation is rejected by the coverage validator, and its
  `ON DUPLICATE KEY UPDATE` bypass is reproduced live, so the validator is shown
  to earn its place rather than asserted to;
- an owner-definer stored procedure granted `EXECUTE` and an owner-definer
  insert-only view granted `SELECT, INSERT` are both denied while base-table
  write privilege is withheld, so neither can replace the base-table grant;
- Dolt `2.3.2` rejects `SIGNAL` and compound trigger bodies.

## Concurrency, replay, and rollback

- Two barriered clients both read aggregate version `0`; one transaction commits
  one state/Event/Audit/outcome and the other exits with SQLSTATE `40001` /
  Error `1213`.
- The losing transaction leaves zero request/Event/Audit/outcome partials **and
  zero identity-ledger residue**, so its idempotency key and Event identity
  remain available to the reconciliation that follows.
- Reconciliation records one immutable conflict request/outcome. Exact winner
  and conflict replays return the stored outcome, and a different canonical
  payload raises `IDEMPOTENCY_PAYLOAD_MISMATCH` without writing, both before and
  after the adversarial matrix.
- Two barriered clients appending the same Event identity leave exactly one
  durable row and one ledger identity.
- A transaction whose Event identity is rejected rolls back the aggregate
  update with it.

## Credential evidence

The harness uses the exact Dolt `v2.3.2` credential environment contract:

- [`credentials.go`](https://github.com/dolthub/dolt/blob/f0feb352b1d3f0919b88ecd28869e515afb60ee0/go/cmd/dolt/cli/credentials.go)
  reads the environment when user/password flags are absent;
- [`envvars.go`](https://github.com/dolthub/dolt/blob/f0feb352b1d3f0919b88ecd28869e515afb60ee0/go/libraries/doltcore/dconfig/envvars.go)
  names `DOLT_CLI_USER` and `DOLT_CLI_PASSWORD`.

Each subprocess is rejected before spawn if its argv contains a generated
secret, and every captured stdout/stderr is scanned. At authenticated readiness,
after every Beads/direct-Dolt/client phase, and before cleanup, the harness:

- recursively scans every regular file under the owned temporary root;
- verifies no credential-bearing `profile` entry exists;
- reads the harness and server `/proc` argv and environment;
- scans accumulated server output.

Every checkpoint reports zero plaintext credential files and no generated secret
in argv, output, or a persistent live-process environment. Dolt creates a
mode-`0777` global config, but it is credential-free. Credentials exist only in
the short-lived client process environment.

## Lifecycle and cleanup

- Each run uses a random database/user/password/HMAC identity and verifies it
  while the recorded server PID is live.
- Unauthenticated root is denied.
- A same-port contender fails closed and cannot attach to the owner.
- Precise `SIGINT` returns `130`, proves server termination, and then proves
  storage removal.
- Cleanup now settles every tracked client process before terminating the
  server, and re-checks the owned root after removal so a late-writing client
  cannot leave a recreated directory behind.
- The focused parent-identity fixture passed first without running the slow
  adversarial matrix. One final three-procedure chain then exited zero and
  produced 128 base-table observations, 117 ledger/guard attacks, 9 two-step
  attacks, 18 deferred transaction-wrapped attacks, 21 child/orphan probes,
  18 parent-key mutation probes, 8 privilege probes, and nine credential
  checkpoints.
- Every statement is bounded. Adversarial probes use a four-second bound and
  record a blocked denial explicitly; other statements use a sixty-second bound.
  A blocked statement can no longer wedge the suite.

## Primary sources

Sources were read on 2026-09-06, and installed behavior was tested instead of
assuming current documentation matched the exact releases.

- [Beads v1.2.2 release](https://github.com/gastownhall/beads/releases/tag/v1.2.2)
- [Beads v1.2.2 metadata contract](https://github.com/gastownhall/beads/blob/6c124203e771433a3550c348771a5b5e27fd3c21/docs/METADATA.md)
- [Beads v1.2.2 concurrency design](https://github.com/gastownhall/beads/blob/6c124203e771433a3550c348771a5b5e27fd3c21/docs/design/dolt-concurrency.md)
- [Beads v1.2.2 internal storage interface](https://github.com/gastownhall/beads/blob/6c124203e771433a3550c348771a5b5e27fd3c21/internal/storage/storage.go)
- [Dolt transactions](https://www.dolthub.com/docs/concepts/dolt/sql/transaction/)
- [Dolt supported statements](https://www.dolthub.com/docs/sql-reference/sql-support/supported-statements/)
- [Dolt users and grants](https://www.dolthub.com/docs/concepts/dolt/sql/users-grants/)
- [Dolt SQL version-control procedures](https://www.dolthub.com/docs/sql-reference/version-control/dolt-sql-procedures/)
- [Dolt v2.3.2 credential parser](https://github.com/dolthub/dolt/blob/f0feb352b1d3f0919b88ecd28869e515afb60ee0/go/cmd/dolt/cli/credentials.go)
- [Dolt v2.3.2 environment constants](https://github.com/dolthub/dolt/blob/f0feb352b1d3f0919b88ecd28869e515afb60ee0/go/libraries/doltcore/dconfig/envvars.go)

The exact tags were inspected in disposable shallow source clones, and the tag
SHAs were reconfirmed with `git ls-remote`.

## Interpretation

Beads' stable public interfaces cannot provide Director's transaction, CAS,
immutable idempotency, and append-only history contract; that result is
unchanged and reproduced here.

Direct Dolt `2.3.2` does provide it, but only with the complete posture proven
here: a `BEFORE INSERT` identity guard on every immutable table covering every
declared unique identity, ledgers the application can only read, a
parent-existence guard on every declared foreign key, an identity guard on every
referenced parent key, a conditional update guard on mutable aggregate IDs, a
ledger key width derived from the live schema, and coverage validation that
fails closed on a collation or index shape that would reopen the update path.
With that posture the
application identity can append and read and can do nothing else to an existing
Command request, Command outcome, Candidate, Event, or Audit record. The outcome
is therefore Go for the Director-owned direct Dolt schema behind the `TaskStore`
port and No-go for Beads as the runtime store.

Two Dolt `2.3.2` availability defects are recorded rather than hidden: some
denied statements block instead of returning an error, and a transaction whose
client disconnects after a failed statement keeps its write locks. Both preserve
data, and both must be bounded by Director and covered by `dir-m0.5`.
