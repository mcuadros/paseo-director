# M0.4 TaskStore mapping evidence

## Final decision

**No-go for Beads `1.2.2` and No-go for direct Dolt `2.3.2` as Director's
runtime `TaskStore`.**

Beads' supported public interfaces fail the required transaction, optimistic
concurrency, idempotency, and immutable-history contract. The direct-Dolt
schema passed the accumulated table, trigger, transaction, credential, and
lifecycle checks, but its normal writable application identity can change
session and global `dolt_force_transaction_commit`. That variable allows
constraint-violating transaction merges, so the database boundary cannot
guarantee secondary-unique immutable identities.

No runtime TaskStore is selected here. Existing contingency Task `dir-m0.16` is
activated, subject to independent approval of the exact decision Candidate.
M1 remains blocked.

## Final root-boundary reproduction

Prerequisites:

- Node.js, tested with `v26.7.0`;
- Dolt `2.3.2`, source tag commit
  `f0feb352b1d3f0919b88ecd28869e515afb60ee0`;
- permission to bind an ephemeral loopback port.

Run from the repository root:

```text
node docs/evidence/m0.4/verify-force-transaction-boundary.mjs
```

The procedure uses one disposable Dolt server and two identities:

- an owner used only for bootstrap, observation, reset, and cleanup; and
- an application user with `USAGE` plus `SELECT, INSERT` on an immutable-record
  table and `SELECT, INSERT, UPDATE` on an aggregate table. It has no global
  administrative privilege or grant option.

The server is configured through the supported public `config.yaml` surface:

```text
system_variables:
  dolt_force_transaction_commit: 0
user_session_vars:
  - name: <application-user>
    vars:
      dolt_force_transaction_commit: 0
```

The exact output is
[`force-transaction-boundary-output.json`](force-transaction-boundary-output.json).
It records:

| Check | Observation |
|---|---|
| Initial global value | `0` |
| Initial application-session value | `0` |
| Required immutable append and aggregate update | Passed |
| Application session override | Accepted; read back `1` |
| Application global override | Accepted; owner read back `1` |
| New app connection after global override | Rejected: variable initialized more than once |
| Owner reset global to `0` | Passed |
| Required writes after reset | Passed |
| Server termination before directory removal | Passed |

The public configuration controls initialize values; they do not make the
values immutable or remove `SET` authority. Combining a global safe default with
a per-user safe default turns the unauthorized global change into a connection
failure rather than containing it. Public read-only mode is not a usable
boundary because it also disables required TaskStore writes. The installed
server surface exposes no per-user statement allowlist or system-variable deny
list.

Primary references:

- [Dolt SQL-server configuration](https://www.dolthub.com/docs/sql-reference/server/configuration/)
- [Dolt system variables](https://www.dolthub.com/docs/sql-reference/version-control/dolt-sysvars/)

The references were read on 2026-09-06. Installed Dolt `2.3.2` behavior was
tested instead of assuming current documentation matched the pinned release.

## Accumulated regression evidence

The final reproduction does not rerun or broaden the previous adversarial
matrix. These existing procedures and outputs are retained because they prove
narrower facts and let another agent reproduce the history:

- [`taskstore-contract.mjs`](taskstore-contract.mjs) and
  [`observed-output.json`](observed-output.json): mapping, Beads failures,
  atomic rollback, barriered CAS, replay, immutable table/ledger guards,
  referential guards, credentials, and normal cleanup;
- [`verify-interruption.mjs`](verify-interruption.mjs) and
  [`interruption-output.json`](interruption-output.json): precise `SIGINT`,
  server termination before storage removal;
- [`verify-listener-isolation.mjs`](verify-listener-isolation.mjs) and
  [`listener-isolation-output.json`](listener-isolation-output.json):
  authenticated listener identity, same-port collision failure, and no
  cross-run attachment.

The `direct_dolt: GO` value embedded in `observed-output.json` is a historical
conclusion from the accumulated schema matrix. It is superseded by
`force-transaction-boundary-output.json`, which tests the later-discovered root
authority failure and records `direct_dolt_2_3_2: NO-GO`. Do not treat the older
field as the final TaskStore decision.

The accumulated default chain remains reproducible when its narrower results
are needed:

```text
DIRECTOR_M04_FOCUS=referenced-parent-identities node docs/evidence/m0.4/taskstore-contract.mjs
node docs/evidence/m0.4/taskstore-contract.mjs
node docs/evidence/m0.4/verify-interruption.mjs
node docs/evidence/m0.4/verify-listener-isolation.mjs
```

It was not rerun in the final decision cycle. The fresh independent Fable review
of Candidate `1a5389b7954d5fc8df578f878e8e8d434979b991` already reproduced the
complete accumulated chain and recorded the force-commit defect separately in
Beads comment `01a07698-a19f-7eb5-8cd6-b38e9a046f53`.

## Accumulated direct-Dolt results

Before the root failure was known, the schema fixture proved:

- explicit mapping for Project, Workspace, Epic, Task, dependency, Run,
  Candidate, Command request/outcome, Event, and Audit records;
- atomic aggregate/Event rollback;
- one barriered same-version winner and one SQLSTATE `40001` / Error `1213`
  loser with zero partials;
- exact command replay and explicit payload-mismatch rejection;
- 128 non-transaction-wrapped immutable-table observations: 118 denials and 10
  controlled appends;
- 117 direct ledger/guard-table attacks and 9 two-step attacks denied;
- 18 deferred transaction attacks denied;
- 21 missing-parent probes denied under enforced, disabled, and relaxed
  foreign-key modes;
- 18 referenced-parent rename probes denied while 3 legitimate aggregate
  version/data updates succeeded;
- 9 clean credential checkpoints;
- listener ownership, interruption, and owned-resource cleanup.

These facts do not compensate for an application-settable commit override. The
force variable acts at transaction merge/commit, after the statement-level
guards that produced the accumulated passes.

## Beads result

Beads `1.2.2` remains No-go through its supported public CLI only:

- no expected-version update flag;
- the supported batch grammar rejects structured metadata, so it cannot
  atomically write aggregate and Command/Event facts;
- `bd update` changes an Event record; and
- recreating an explicit ID overwrites conflicting command content.

Director continues to use Beads for development tracking. Product runtime code
must not write Beads-owned tables or import its internal Go storage package.

## P3 documentation corrections

### Coverage-validator scope

The accumulated harness calls `validateGuardedSchema(immutableTableModel)`. It
does not validate the later `aggregates.id` ledger contained only in
`identityLedgerModel`. The live tested `aggregates.id` column was binary
collated and its mutation probes passed, but the earlier documentation
overstated the migration validator: it does not cover that identity's
collation, byte width, nullability, prefix, or expression properties.

Because direct Dolt is rejected, this final cycle documents the gap rather than
patching or broadening the obsolete schema matrix. Any future investigation
must not reuse the old claim that every referenced parent identity is covered
by the validator.

### Aggregate update operation

The `aggregates` `BEFORE INSERT` identity ledger fires before Dolt chooses the
`ON DUPLICATE KEY UPDATE` path. It therefore denies every duplicate-ID upsert,
including one that changes only aggregate version or data. The accumulated
adapter design can update aggregate state only with a conditional statement of
this form:

```sql
UPDATE aggregates
SET version = ?, data = ?
WHERE id = ? AND version = ?;
```

This constraint was missing from the previous ADR and README. It does not
resolve the force-transaction authority failure.

## Compatibility and cleanup

The final result is bounded to Dolt `2.3.2` on the stated Debian/Linux topology.
It does not claim Windows, synchronization, backup, restore, migration, or
partial-failure proof; those gates must be reassigned to the store selected by
`dir-m0.16`.

Every completed final-boundary run stopped its owned server and removed its
owned directory. Generated credentials were absent from argv, captured output,
and the credential-free server configuration. The previously documented
unattributable temporary artifact remains untouched because ownership cannot be
proven.
