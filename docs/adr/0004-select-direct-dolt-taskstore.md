# ADR-0004: Select a direct Dolt TaskStore with append-only identity guards

- **Status:** Proposed
- **Date:** 2026-09-06
- **Beads Task:** `dir-m0.4`
- **Plan gate:** M0 TaskStore mapping
- **Decision owner:** Director maintainers

## Context

Director requires a durable `TaskStore` for Project planning and execution
records. The approved plan names Beads backed by Dolt as the initial candidate
and permits one Director-owned direct-Dolt schema behind the same port if Beads
cannot satisfy the contract cleanly.

The store must represent Projects, Workspaces, Epics, Tasks, dependencies,
Runs, immutable Candidates, Commands with immutable idempotency requests and
outcomes, immutable Events, and immutable Audit entries. A logical transition
must atomically persist aggregate state and its supporting records. Mutable
aggregates require expected-version optimistic concurrency so two writers
cannot silently overwrite one another. The selected topology must allow
independent writers against one server.

Beads has a richer Go storage interface, but it is an `internal` package and is
neither importable nor a supported external contract. Depending on it, or
writing Beads-owned SQL tables directly, would violate the TaskStore abstraction
and the prohibition on private storage dependencies.

Five independent review cycles shaped this decision. They established that table
`SELECT, INSERT` grants alone are not append-only on Dolt `2.3.2`, because
`INSERT ... ON DUPLICATE KEY UPDATE` rewrites an existing row without `UPDATE`
privilege; that `BEFORE UPDATE`/`BEFORE DELETE` triggers are active but are
never activated by that update path; and that an owner-definer procedure and an
owner-definer insert-only view cannot append while base-table write privilege is
withheld. A third review showed that a supported `BEFORE INSERT` guard rejects
those statements.

The fourth review then found a P0 in the first guarded Candidate: the identity
ledgers were themselves granted `INSERT` to the application identity, so the
same `ON DUPLICATE KEY UPDATE` defect could rename a ledger identity and free it,
after which a second statement mutated the protected row through a
non-colliding alternative identity. It also found that the application could
disable session foreign-key checks and append orphan rows, that column-set
coverage does not detect a case-insensitive collation, and that a fixed
`VARBINARY(512)` ledger key is too narrow for the declared identifiers.

This revision removes every application write privilege on the ledgers, adds
supported parent-existence guards, derives the ledger key width from the live
schema, and validates coverage on ordered columns, types, widths, NULL
semantics, prefix/expression status, and collation.

The fifth review found that those child-side guards were insufficient when a
referenced aggregate identity itself remained mutable: with
`foreign_key_checks=0`, the application could rename `aggregates.id` and leave
immutable history pointing at the old value. This revision gives every
referenced parent key an identity ledger, rejects changes to `aggregates.id`
through both direct and insert-derived update paths, and still permits ordinary
aggregate version/data updates.

This decision does not change the repository's use of Beads for development
tracking. It does not override ADR-0008's same-user authority-separation No-go
or ADR-0009's rule that external executables remain separately installed,
exact-version prerequisites. ADR-0010's top-level Task Agent and Reviewer Agent
parentage clarification governs who owns and reviews this Task and does not
change any storage observation recorded here.

## Question or hypothesis

Can Director implement its complete runtime `TaskStore` using supported Beads
`1.2.2` interfaces or the plan-approved direct Dolt `2.3.2` contingency while
guaranteeing atomic state/Event transactions, expected-version optimistic
concurrency, immutable unique idempotency records, immutable Candidate/Event/
Audit history, and safe shared-server multiwriter behavior?

## Acceptance criteria

- Epics, Tasks, dependencies, Runs, Candidates, Commands, Events, and Audit
  records have an explicit, queryable mapping.
- Aggregate state and its structured Command/Event records commit or roll back
  together.
- Of two updates based on the same aggregate version, exactly one may advance
  the version; a stale update is explicit and leaves no partial records.
- Repeating an idempotency key returns its first durable outcome; a conflicting
  request hash or payload is explicitly rejected and cannot replace it.
- Command request identity/key/version/hash/payload, Command outcomes,
  Candidates, Events, and Audit records cannot be changed or deleted through any
  SQL statement available to the normal application identity.
- The immutability boundary covers every declared unique identity of every
  immutable table: each primary key, each `UNIQUE` key, each composite, and each
  auto-increment key. Coverage is proven against `information_schema` rather
  than asserted.
- Adversarial statements cover exact and mismatched plain duplicates,
  `INSERT IGNORE`, `INSERT ... ON DUPLICATE KEY UPDATE` in literal, `VALUES()`,
  `IGNORE`, `INSERT ... SELECT`, primary-key-targeted, multi-row, and
  transaction-wrapped forms, `REPLACE`, direct `UPDATE`, `DELETE`, `TRUNCATE`,
  and schema/trigger control.
- A positive control proves each guard is active, distinct appends succeed
  before and after the matrix, and the application identity cannot drop, alter,
  replace, or disable a guard or its ledger.
- The application identity has no write privilege on any identity ledger. Every
  direct ledger statement, including the two-step rename-then-mutate attack and
  unused-identity reservation, is rejected with byte-identical ledger contents.
- Referential children are rejected when their parent is absent, with session
  foreign-key checks enforced, disabled, and disabled under a relaxed
  `sql_mode`.
- Every key referenced by a declared foreign key is immutable. Direct and
  insert-derived rename attempts against each referenced parent key are denied
  with foreign-key checks enforced, disabled, and disabled under a relaxed
  `sql_mode`, while legitimate aggregate version/data updates remain available.
- Coverage validation compares ordered index columns, column type, octet width,
  NULL semantics, prefix/expression status, and collation, and fails closed on a
  collation that can match values the ledger stores as distinct identities.
- The ledger key width is derived from the live schema so a maximum-length
  multibyte identifier can neither be truncated nor rejected.
- No privilege operation available to the application identity expands its own
  authority or reaches another identity.
- Public Dolt version-control procedures that could rewrite committed state are
  denied to the application identity.
- Two barriered clients updating the same aggregate from the same expected
  version produce exactly one state/Event/Audit transition. A Dolt SQLSTATE
  `40001` / Error `1213` loser rolls back completely, reconciles to a
  replay-safe explicit conflict, and produces no duplicate or partial state.
- Two barriered clients appending the same immutable identity produce exactly
  one durable row, and a rolled-back transaction leaves no guard residue that
  would block a later legitimate append.
- Every test run authenticates a unique server/database identity, denies
  unauthenticated root, and proves its spawned processes exit before deleting
  owned storage.
- No generated secret appears in argv, output, or temporary files, or remains in
  a live persistent process environment at readiness, after each client phase,
  or before cleanup.
- The experiment uses disposable data and cleans every owned resource.

## Evidence

### Versions and topology

The contract was run on Debian 13 amd64 Linux, kernel `6.12.107+deb13-amd64`,
with:

- Beads `1.2.2`, build/source commit
  `6c124203e771433a3550c348771a5b5e27fd3c21`;
- Dolt `2.3.2`, source commit
  `f0feb352b1d3f0919b88ecd28869e515afb60ee0`;
- Node.js `v26.7.0` as the test driver;
- one isolated loopback Dolt SQL server;
- unique random database, user, and authentication identities per run;
- a pre-seeded HMAC database marker bound to the live spawned PID;
- unauthenticated root disabled and negatively probed;
- independent barriered client transactions and no remotes.

Primary documentation and exact source tags were read on 2026-09-06. The
[Beads v1.2.2 release](https://github.com/gastownhall/beads/releases/tag/v1.2.2)
states that it is the tested 1.1 line re-released and that the 1.2-only Event
journal and HTTP API are absent. The
[metadata contract](https://github.com/gastownhall/beads/blob/6c124203e771433a3550c348771a5b5e27fd3c21/docs/METADATA.md)
describes metadata as the integration extension point and execution hints as
advisory. The release's
[concurrency design](https://github.com/gastownhall/beads/blob/6c124203e771433a3550c348771a5b5e27fd3c21/docs/design/dolt-concurrency.md)
lists lost-update protection as an open question and suggests application-level
conditional updates where needed.

Dolt's primary documentation describes
[transactions](https://www.dolthub.com/docs/concepts/dolt/sql/transaction/),
[supported statements](https://www.dolthub.com/docs/sql-reference/sql-support/supported-statements/),
[users and grants](https://www.dolthub.com/docs/concepts/dolt/sql/users-grants/),
and the
[version-control procedures](https://www.dolthub.com/docs/sql-reference/version-control/dolt-sql-procedures/).
The exact `v2.3.2`
[credential parser](https://github.com/dolthub/dolt/blob/f0feb352b1d3f0919b88ecd28869e515afb60ee0/go/cmd/dolt/cli/credentials.go)
reads credentials from process environment when flags are absent, and its
[public constants](https://github.com/dolthub/dolt/blob/f0feb352b1d3f0919b88ecd28869e515afb60ee0/go/libraries/doltcore/dconfig/envvars.go)
name `DOLT_CLI_USER` and `DOLT_CLI_PASSWORD`. The fixture verified these exact
paths against the installed binary and created no credential-bearing profile.

### Reproduction

From the repository root:

```text
DIRECTOR_M04_FOCUS=referenced-parent-identities node docs/evidence/m0.4/taskstore-contract.mjs
node docs/evidence/m0.4/taskstore-contract.mjs
node docs/evidence/m0.4/verify-interruption.mjs
node docs/evidence/m0.4/verify-listener-isolation.mjs
```

The commands exit zero only when all positive and negative observations match.
The focused referenced-parent fixture was run first without the unchanged slow
timeout cases. One complete three-procedure chain was then run on the final
content. The procedures and captured outputs are in
[`docs/evidence/m0.4`](../evidence/m0.4/README.md).

### The selected immutability boundary

Each immutable table has an owner-definer `BEFORE INSERT` trigger and an
identity ledger on which the application identity holds `SELECT` and nothing
else. The width shown here is the current schema's derived value—535 bytes are
required and the provisioning rule rounds that budget to 640—not a fixed schema
constant:

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
that `ON DUPLICATE KEY UPDATE` would otherwise convert into an update. A new
record contributes new identities and is appended; any statement that would
touch an existing record repeats one identity and fails on the ledger's primary
key. Dolt assigns the auto-increment value before the trigger body runs, so
auto-increment identities are ledgered with their real value.

Dolt `2.3.2` rejects `SIGNAL` and compound `BEGIN ... END` trigger bodies, which
the contract probes and records, so each guard is one supported multi-row
`INSERT`. Dolt runs a trigger body's ledger `INSERT` as long as the invoker holds
some privilege on the ledger, so `SELECT` is both sufficient for genuine appends
and the minimal posture: the application can never write a ledger row directly.

Referential integrity uses the same shape. Every declared foreign key has an
owner-definer guard that inserts a pre-seeded sentinel identity when the parent
row is absent, which raises a duplicate-key error:

```sql
CREATE DEFINER = '<owner>'@'%' TRIGGER fk_audit_event_present
  BEFORE INSERT ON audit_entries FOR EACH ROW
  INSERT INTO parent_guard (identity) SELECT 'guard.parent_missing' FROM guard_constants
    LEFT JOIN events AS guarded_parent ON guarded_parent.event_id = NEW.event_id
    WHERE NEW.event_id IS NOT NULL AND guarded_parent.event_id IS NULL;
```

This holds when the application disables `foreign_key_checks`, because it is a
trigger rather than a constraint. The ledger key width is derived from
`information_schema` octet lengths rather than fixed, and coverage validation
rejects any unique key whose collation, nullability, prefix, expression, type,
or width would let the base key match values the binary ledger keeps apart.

The mutable `aggregates` table uses the same `BEFORE INSERT` identity-ledger
guard for its referenced primary key. A separate conditional `BEFORE UPDATE`
guard inserts a duplicate sentinel only when `NEW.id` differs from `OLD.id`.
The application holds `SELECT` only on that sentinel table, which is sufficient
for the owner-definer trigger under the proven Dolt `2.3.2` semantics. This
rejects direct identity changes without blocking version or data updates.

### Observations

| Contract | Beads `1.2.2` | Direct Dolt `2.3.2` with identity guards |
|---|---|---|
| Planning graph | Epic, Task, parent-child, and blocking dependency round-trip passed | Explicit relational mapping passed |
| Run/Candidate/Command/Event/Audit mapping | Requires issue types plus advisory metadata conventions | Explicit mapping passed |
| Narrow batch rollback | Passed | Passed |
| Aggregate plus structured Event transaction | Failed: `bd batch` rejects metadata and accepts only narrow issue fields | Passed: a rejected duplicate Event identity rolled back aggregate state and Event count |
| Optimistic concurrency | Failed: no supported expected-version update; two stale writes both succeeded | Passed: barriered clients produced one commit and one SQLSTATE `40001` / Error `1213` rollback |
| Idempotency | Failed: recreating an explicit issue ID overwrote conflicting content | Passed: exact replays return the stored outcome, a different canonical request raises `IDEMPOTENCY_PAYLOAD_MISMATCH`, and the records cannot be rewritten |
| Immutable records | Failed: a public `bd update` changed an Event | Passed: 118 non-transaction-wrapped base-table statements denied, 0 mutations; deferred transaction attacks reported separately |
| Identity coverage | Not applicable | Passed: all 9 declared unique identities guarded, 0 uncovered, verified on ordered columns, type, width, NULL semantics, prefix/expression status, and collation |
| Ledger boundary | Not applicable | Passed: 117 direct ledger/guard statements and 9 two-step rename-then-mutate attacks denied with byte-identical guard state |
| Referenced parent identities | Not applicable | Passed: all 3 referenced keys survived 18 direct and insert-derived rename attempts under enabled, disabled, and relaxed foreign-key modes; every child remained linked and 3 legitimate aggregate updates succeeded |
| Referential integrity | Not applicable | Passed: 21 child/orphan probes denied with foreign-key checks enforced, disabled, and disabled under relaxed `sql_mode`; 0 orphan rows |
| Ledger key width | Not applicable | Passed: 535 bytes required, 640 provisioned, 528-byte identity stored intact |
| Privilege boundary | Not applicable | Passed: 8 privilege-expansion operations denied; self credential rotation permitted but grants nothing |
| Concurrent identity uniqueness | Not applicable | Passed: two barriered clients appending one Event identity left exactly one row |
| Version-control procedures | Not applicable | Passed: `DOLT_RESET`, `DOLT_CHECKOUT`, `DOLT_REVERT`, `DOLT_BRANCH`, and `DOLT_COMMIT` denied |
| Credential transport | Beads password environment worked | Passed: environment-only clients; no profile or plaintext secret at nine checkpoints |
| Listener ownership and cleanup | Not sufficient for Director | Passed: authenticated per-run identity, unauthenticated-root denial, PID binding, collision isolation, precise SIGINT, client settlement, and termination before storage removal |

The non-transaction-wrapped base-table matrix produced 128 observations: 118
denials and 10 controlled appends. For every immutable table and every declared
identity it ran a plain duplicate, `INSERT IGNORE`, and
`ON DUPLICATE KEY UPDATE` in literal, `VALUES()`, `IGNORE`,
`INSERT ... SELECT`, and multi-row forms; per table it also ran `REPLACE`,
`UPDATE`, `DELETE`, `TRUNCATE`, `ALTER`, guard drop and replacement, and ledger
delete, truncate, drop, and primary-key removal. Every attempt failed, the
protected row was byte-identical afterwards, and the table row count was
unchanged, including for the multi-row form whose new companion row was not
appended. A distinct append before and after each table's matrix succeeded,
proving the guard blocks collisions rather than all writes.

The ledger boundary was then attacked directly: 117 statements against the six
identity ledgers plus `parent_guard`, `guard_constants`, and
`immutable_write_guard` covering unused-identity reservation, `INSERT IGNORE`,
`ON DUPLICATE KEY UPDATE` in literal, `VALUES()`, `IGNORE`,
`INSERT ... SELECT` and multi-row forms, `REPLACE`, `UPDATE`, `DELETE`,
`TRUNCATE`, `DROP PRIMARY KEY`, and `DROP TABLE`. Every one was rejected with
byte-identical guard state. The reviewer's two-step attack was then run for all
nine immutable table/identity pairs: the ledger rename was denied and the
follow-up base `ON DUPLICATE KEY UPDATE` with an otherwise non-colliding row was
denied by the guard, leaving both the row and the ledger unchanged.

Eighteen deferred transaction-wrapped attacks run separately and last, because
Dolt `2.3.2` keeps the
write locks of a transaction whose client disconnects after a failed statement,
which blocks later writers to the same table until the server stops.

A focused referenced-parent fixture runs before the broad matrices. It derives
the three parent keys from the declared foreign-key model, proves each has an
identity guard, seeds every declared child relationship, and performs 18 direct
and insert-derived rename attempts across enforced, disabled, and relaxed
foreign-key modes. Every parent identity and child link is preserved. Three
positive-control updates advance aggregate version/data while retaining its ID.

Seventy-five denied statements did not return within their four-second bound and
were killed instead of producing a privilege or constraint error. In every case
the target was byte-identical afterwards, so this is a liveness defect in Dolt
`2.3.2`, not a mutation path. The exact statements are recorded in
`boundary_evidence.blocked_without_returning`.

Two negative controls explain why weaker boundaries were rejected and are kept
as evidence: a `BEFORE UPDATE` guard on a probe table rejects a direct owner
`UPDATE` yet is never activated by that owner's `ON DUPLICATE KEY UPDATE`; and a
table without a unique index survives that statement only by giving up the
uniqueness the contract requires.

Every subprocess is rejected before spawn if a generated secret is present in
argv, every captured stdout/stderr is scanned, all regular files below the owned
temporary root are rescanned at each checkpoint, and `/proc` verifies that
neither the harness nor the long-lived server retains a generated secret in argv
or environment. Dolt still creates a mode-`0777` global config, but it contains
no profile and no generated credential. Only short-lived client processes
receive credentials in their supported process environment.

## Alternatives considered

### Beads issues plus namespaced metadata

Rejected. Correctness would depend on advisory metadata conventions; generic
expected-version updates are absent; explicit IDs upsert conflicting content;
structured state, Command, and Event writes do not fit the public batch
transaction; and Event issues remain mutable.

### Beads-owned SQL tables or the internal Go storage package

Rejected. This would couple Director to private schema/migrations or an
unsupported internal package while preserving Beads only in name.

### Direct table grants alone

Rejected. `SELECT, INSERT` is not append-only on Dolt `2.3.2`: `ON DUPLICATE KEY
UPDATE` changes existing rows without `UPDATE` privilege. This is why the
`BEFORE INSERT` guard is part of the selected schema rather than an optional
hardening step.

### `BEFORE UPDATE` and `BEFORE DELETE` triggers

Rejected as the boundary. The contract's control proves such a guard is active
for a direct `UPDATE` and is never activated by the `ON DUPLICATE KEY UPDATE`
path. They are retained only as defense in depth.

### Granting the application `INSERT` on the identity ledgers

Rejected, and previously wrong. An earlier Candidate granted `SELECT, INSERT` on
each ledger because a trigger body runs with invoker privileges. Independent
review proved that this reopens the whole boundary: the application renames a
ledger identity with `ON DUPLICATE KEY UPDATE`, freeing it, and then mutates the
protected row through an otherwise non-colliding alternative identity. `SELECT`
alone is sufficient for the trigger's ledger write on Dolt `2.3.2` and is the
posture this ADR selects.

### Relying on foreign-key constraints for referential integrity

Rejected. The application identity can set `foreign_key_checks=0` for its own
session and append orphan children. The declared foreign keys stay in the schema
for owner-side integrity, but the enforced boundary is a parent-existence
trigger that a session variable cannot disable.

### Definer procedure or insert-only view

Rejected on Dolt `2.3.2`. Both objects were created and granted through normal
SQL, but neither could append while base-table write privilege was withheld.

### Append-only tables without a unique index

Rejected. Dropping the unique index defeats the update path but also removes
database-enforced idempotency-key and event-id uniqueness, which the Task and
plan require.

### Detect tampering with hashes, commits, or audit reconciliation

Rejected as the required boundary. Detection after an application-authorized
statement has rewritten a record is not immutability. It remains available as an
additional audit control.

### Use a private or version-specific Dolt mechanism

Rejected. Every object in the selected schema is created through normal
supported SQL: tables, triggers, grants, and constraints.

## Decision

**Go for a single Director-owned direct Dolt schema, with per-table
`BEFORE INSERT` identity guards, behind the `TaskStore` port. No-go for Beads as
Director's runtime `TaskStore`.**

Beads remains the development issue tracker only. Product code must not read or
write Beads-owned tables or invoke Beads for runtime storage.

The guards are part of the storage contract, not an optional hardening step. The
schema is only compliant when all of the following hold:

- every declared unique identity of every immutable table is covered by that
  table's guard, proven against `information_schema` on ordered columns, type,
  octet width, NULL semantics, prefix/expression status, and collation;
- the application identity holds `SELECT` and nothing else on every identity
  ledger, on `parent_guard`, on `guard_constants`, and on
  `immutable_write_guard`, and holds no `UPDATE`, `DELETE`, `TRUNCATE`, `ALTER`,
  `DROP`, or `TRIGGER` privilege on the immutable tables;
- every declared foreign key has a parent-existence guard, so referential
  integrity survives `foreign_key_checks=0`;
- every referenced parent identity has an identity ledger, and mutable
  aggregates have a conditional update guard that rejects ID changes while
  allowing version/data changes;
- the ledger key width is derived from the live schema rather than fixed.

`1.0` ships this one runtime TaskStore, not two interchangeable engines.

## Consequences

- `dir-m1.5` may implement the TaskStore skeleton against this schema once the
  remaining M0 stop conditions clear. This ADR resolves only the TaskStore
  mapping gate.
- `dir-m0.5` proves sync, backup, restore, migration, and partial-failure
  recovery against this schema, including that a restored or migrated database
  keeps its guards and ledgers consistent with its data.
- `dir-m0.16`, which was opened from the previous premature No-go, is not
  needed. It should be closed as superseded once this Candidate is approved,
  rather than launched.
- Every migration must re-run the identity-coverage check, and it is a hard gate
  for `dir-m0.5`. Adding a `UNIQUE` key without extending the guard, or changing
  a guarded column to a case- or accent-insensitive collation, silently reopens
  the update path; the contract reproduces that bypass on a deliberately drifted
  table to prove the check earns its place.
- Each immutable row costs one ledger row per declared identity. Retention,
  backup size, and migration must account for the ledger as part of the data.
- Aggregate IDs also cost one ledger row because they are referenced identities;
  migrations must prove both child-side parent guards and parent-key
  immutability from live foreign-key metadata.
- The application identity holds only `SELECT` on the ledgers. It cannot reserve,
  rename, or remove an identity, so the earlier claim that ledger `INSERT` was a
  monitored availability residual is withdrawn: that grant was the integrity
  defect, and it is gone.
- Dolt `2.3.2` does not always deny promptly. Seventy-five denied statements in the
  matrix blocked instead of returning an error and were killed at a four-second
  bound, and a transaction whose client disconnects after a failed statement
  keeps its write locks until the server stops. Neither mutates data, but both
  are availability defects that Director must bound with statement timeouts and
  connection lifecycle limits, and that `dir-m0.5` must cover in recovery.
- The application identity can rotate its own password with an explicit
  `ALTER USER` on itself. It gains no privilege by doing so and cannot touch the
  owner, but it can lock the engine out of its own store until an operator
  resets the credential. Director must treat that as an operator-visible
  recovery event.
- Guard violations surface as duplicate-key errors naming the ledger identity.
  The adapter must map that class to an explicit domain conflict.
- The barriered CAS loser returns SQLSTATE `40001` / Error `1213` and must be
  reconciled into a replay-safe conflict rather than retried blindly.
- Compatibility proven here is limited to the stated Debian/Linux topology,
  Beads `1.2.2`, and Dolt `2.3.2`. A different Dolt version must re-run this
  matrix before it is adopted, because the boundary depends on observed trigger
  and statement behavior.
- The environment-only credential path is evidence hygiene, not an approved
  production credential design. ADR-0008 continues to block same-user authority
  and credential isolation, and M1 remains blocked until all M0 stop conditions
  pass independent review.

## Independent verification

Pending independent review of the exact changed Candidate SHA. An independent
top-level Reviewer Agent must run the focused referenced-parent mode and all
three complete procedures from a detached disposable checkout, inspect the
identity-coverage proof, the complete adversarial matrix, and the credential
checkpoints, and record the exact SHA and verdict in `dir-m0.4`. No publication,
integration, or Task closure is authorized by this Candidate.
