# ADR-0004: Reject Beads and direct Dolt 2.3.2 for the TaskStore

- **Status:** Proposed
- **Date:** 2026-09-06
- **Beads Task:** `dir-m0.4`
- **Plan gate:** M0 TaskStore mapping
- **Decision owner:** Human project owner and Director maintainers

## Context

Director needs one durable `TaskStore` for Project planning and execution
records. It must map Projects, Workspaces, Epics, Tasks, dependencies, Runs,
Candidates, Commands, Events, and Audit entries; atomically persist aggregate
state with supporting records; reject stale expected-version writes; preserve
idempotent outcomes; and keep immutable history append-only under concurrent
writers.

The approved plan named Beads backed by Dolt as the initial candidate and
allowed one Director-owned direct Dolt schema as its contingency. The Beads
`1.2.2` public CLI was rejected earlier in this spike: it has no generic
expected-version update, its supported batch grammar cannot atomically persist
structured aggregate/Command/Event records, an Event can be changed through
`bd update`, and recreating an explicit ID overwrites a conflicting command
payload.

Several direct-Dolt Candidates then proved narrower properties with supported
tables, triggers, constraints, grants, and transactions. They found and fixed
mutable idempotency records, untested concurrent CAS, unauthenticated listener
ownership, credential artifacts, `ON DUPLICATE KEY UPDATE` paths that bypass
ordinary table grants and `BEFORE UPDATE` triggers, app-writable identity
ledgers, disabled foreign-key checks, collation drift, undersized ledger keys,
and mutable referenced parent IDs. Those accumulated results remain useful
compatibility evidence, but they do not establish a safe runtime store.

The final independent review found a root failure outside those schema guards.
On Dolt `2.3.2`, `dolt_force_transaction_commit=1` permits transaction merges
with constraint violations. The normal table-scoped application identity can
set that variable in its own session and globally. A forced concurrent merge
can therefore persist duplicate secondary `UNIQUE` identities even though
each transaction ran the identity-ledger trigger. The global setting also
changes correctness policy for other connections and can leave the store
unwritable for well-behaved clients until an owner repairs immutable data.

The human authorized one final root-cause decision: determine whether supported
public Dolt `2.3.2` privilege or connection configuration can prevent the
normal application identity from changing the session or global force-commit
value while preserving required TaskStore writes. No further schema patch or
adversarial-matrix expansion is allowed.

## Falsifiable boundary question

Can a Dolt `2.3.2` server use only supported public configuration, users, and
grants to satisfy all of the following simultaneously?

1. Initialize global and application-session `dolt_force_transaction_commit`
   to `0`.
2. Permit the application identity to `SELECT` and `INSERT` immutable records
   and to `SELECT`, `INSERT`, and conditionally `UPDATE` aggregate records.
3. Deny that identity both
   `SET @@SESSION.dolt_force_transaction_commit=1` and
   `SET @@GLOBAL.dolt_force_transaction_commit=1`.
4. Keep new and existing application connections writable after a rejected
   override attempt.

Success requires a documented enforceable boundary, not an adapter convention.
Failure of either `SET` denial is No-go because the application identity can
change the database correctness policy that the TaskStore relies on.

## Evidence

### Versions and topology

The final reproduction used:

- Dolt `2.3.2`;
- Debian 13 amd64 Linux, kernel `6.12.107+deb13-amd64`;
- one disposable loopback SQL server;
- one owner and one application user;
- no remote and no shared database;
- `system_variables` setting the global force-commit default to `0`;
- `user_session_vars` setting the application-session default to `0`; and
- only `USAGE` plus required table-level grants for the application user—no
  `SUPER`, `SYSTEM_VARIABLES_ADMIN`, wildcard write, or grant option.

The exact executable and captured output are:

- [`verify-force-transaction-boundary.mjs`](../evidence/m0.4/verify-force-transaction-boundary.mjs);
- [`force-transaction-boundary-output.json`](../evidence/m0.4/force-transaction-boundary-output.json).

Reproduce from the repository root:

```text
node docs/evidence/m0.4/verify-force-transaction-boundary.mjs
```

The procedure creates only disposable local data, passes generated credentials
through process environment or bootstrap standard input rather than argv,
restores the global variable, proves its server exits, and removes its owned
directory.

### Supported public controls

The installed `dolt sql-server --help` and the public
[server-configuration reference](https://www.dolthub.com/docs/sql-reference/server/configuration/)
expose:

- `system_variables`, a map that initializes server system-variable values;
- `user_session_vars`, a per-user map applied when a connection is created; and
- `behavior.read_only`, which disables database modification.

The configuration reference explicitly describes Dolt system variables as
settable through SQL `SET`. It exposes no per-user statement allowlist, denied
system-variable list, or immutable per-user variable policy. The public
[system-variable reference](https://www.dolthub.com/docs/sql-reference/version-control/dolt-sysvars/)
states that `dolt_force_transaction_commit=1` ignores merge conflicts,
constraint violations, and other correctness failures.

Read-only mode is not a TaskStore boundary: it also denies the required
application `INSERT` and `UPDATE` operations.

### Minimal reproduction result

| Check | Result |
|---|---|
| Global configured default | `0` |
| Application configured session default | `0` |
| Application global privilege | None beyond `USAGE`; table grants only |
| Required immutable append | Passed |
| Required aggregate insert and conditional update | Passed |
| Application `SET @@SESSION...=1` | Accepted; session read back `1` |
| Application `SET @@GLOBAL...=1` | Accepted; owner read global `1` |
| New app connection with per-user safe default after global override | Rejected with `Variable 'dolt_force_transaction_commit' was initialized more than 1x` |
| Owner reset global to `0` | Passed |
| Required app writes after owner reset | Passed |
| Owned server/directory cleanup | Passed |

The two public initialization controls are therefore not enforcement controls.
The application can override both. Combining the global default with a per-user
safe default does not contain a malicious global change; on the tested version,
it converts that change into denial of new application connections until the
owner resets the variable.

### Accumulated evidence and corrected limitations

The prior contract suite and lifecycle outputs remain in
[`docs/evidence/m0.4`](../evidence/m0.4/README.md). They prove the narrower
mapping, transaction, CAS, trigger, ledger, referential, credential, listener,
interruption, and cleanup observations they record. Their embedded direct-Dolt
`GO` field is superseded by this final root-boundary result and must not be used
as the TaskStore decision.

Two P3 documentation gaps from the final review are corrected here:

1. The accumulated schema-coverage validator calls
   `validateGuardedSchema(immutableTableModel)`. It does not validate the later
   `aggregates.id` identity ledger for collation, width, nullability, prefix, or
   expression drift. The live tested column was binary-collated and mutation
   probes passed, but the earlier claim that one validator covered every
   referenced parent identity was too broad. Because the schema is rejected,
   the validator is not expanded in this decision cycle.
2. The `aggregates` `BEFORE INSERT` identity ledger rejects every duplicate-ID
   `INSERT ... ON DUPLICATE KEY UPDATE`, including an upsert that changes only
   version or data. The accumulated design therefore requires aggregate state
   changes to use `UPDATE ... WHERE id = ? AND version = ?`; ODKU is not an
   available adapter operation. This constraint does not rescue the rejected
   force-commit boundary.

## Alternatives considered

### Beads `1.2.2` public interfaces

Rejected by the earlier reproducible failures. Depending on Beads-owned SQL
tables or its internal Go storage package would violate the supported-interface
and TaskStore-port requirements.

### Direct Dolt table grants, triggers, and identity ledgers

Rejected. They constrain statements before each transaction attempts to merge,
but cannot stop the application identity from changing the merge correctness
policy. Identical ledger inserts can merge while a secondary unique violation
is force-committed.

### `system_variables` plus `user_session_vars`

Rejected by the final reproduction. They initialize safe values but do not
prevent the application from setting either scope. After the global override,
the per-user setting causes connection initialization failure instead of
restoring the safe value.

### Server read-only mode

Rejected. It prevents the required TaskStore writes as well as unsafe ones.

### Application convention or an external SQL proxy

Rejected as the selected boundary. An application promise not to issue `SET`
is not database enforcement. Dolt `2.3.2` exposes no supported per-user
statement filter in its SQL-server surface, and an external proxy would be a
new unapproved runtime component outside this spike's direct-Dolt contingency.

### Another schema redesign around primary-key conflicts

Not selected. The final root-cause question is whether Dolt can contain the
application's authority to change correctness policy. Re-encoding every
identity as a primary-key conflict would be another schema patch, would not
remove the global policy authority, and is outside the human-authorized final
cycle.

## Decision

**No-go for Beads `1.2.2` and No-go for a Director-owned direct Dolt `2.3.2`
runtime TaskStore.**

No supported enforceable Dolt `2.3.2` privilege or connection boundary prevents
the required writable application identity from changing session and global
`dolt_force_transaction_commit`. The plan-approved direct-Dolt contingency is
therefore rejected. No runtime TaskStore is selected by this ADR.

The existing contingency Task `dir-m0.16` is activated to select exactly one
supported replacement behind the same TaskStore port. It remains gated on fresh
independent approval of this exact decision Candidate; it must not be executed
from an unreviewed SHA.

## Consequences

- The M0 TaskStore gate remains blocked.
- `dir-m0.16` must evaluate a replacement through documented public interfaces
  and the complete mapping, atomicity, CAS, idempotency, immutability,
  concurrency, credential, lifecycle, synchronization, backup, migration, and
  Linux/Windows criteria already recorded on that Task.
- `dir-m0.5` cannot finalize operations evidence until the replacement store is
  selected.
- `dir-m1.5` and all M1 product implementation remain blocked.
- Beads remains the development issue tracker only.
- Direct Dolt `2.3.2` remains useful evidence and may remain a separately
  installed tool, but it is not Director's runtime TaskStore.
- The prior direct-Dolt Go artifacts are retained as historical evidence rather
  than rewritten to imply they tested this later-discovered root condition.

## Independent verification

Pending fresh independent review of the exact changed Candidate SHA. The
Reviewer must reproduce the single force-transaction boundary procedure from a
detached disposable checkout, inspect the public configuration/grant surface,
confirm both P3 corrections, and verify that the ADR activates `dir-m0.16`
without authorizing M1. Publication, integration, Task closure, and workspace
cleanup remain post-review gates.
