# ADR-0004: Select direct Dolt 2.3.2 behind the trusted TaskStore engine

- **Status:** Proposed
- **Date:** 2026-09-06
- **Beads Task:** `dir-m0.4`
- **Plan gate:** M0 TaskStore mapping
- **Decision owner:** Human project owner and Director maintainers
- **Supersedes:** Unreviewed No-go Candidate
  `681c2e2113fe63b5aad6e963f78f55b5832ac1f0`

## Context

Director needs one durable `TaskStore` for Project planning and execution
records. It must map Projects, Workspaces, Epics, Tasks, dependencies, Runs,
Candidates, Commands, Events, and Audit entries; atomically persist aggregate
state with supporting records; reject stale expected-version writes; preserve
idempotent outcomes; and keep immutable history append-only under concurrent
writers.

The approved plan named Beads backed by Dolt as the initial candidate and
allowed one Director-owned direct Dolt schema as its contingency. The Beads
`1.2.2` public CLI is No-go: it has no generic expected-version update, its
supported batch grammar cannot atomically persist structured aggregate,
Command, and Event records, an Event can be changed through `bd update`, and
recreating an explicit ID overwrites a conflicting command payload.

The direct-Dolt evidence evolved through repeated independent review. The final
schema fixture proves explicit mapping, transactional aggregate/Event writes,
barriered optimistic concurrency, idempotent replay, immutable Command,
Candidate, Event, and Audit records, referenced-parent integrity, credential
hygiene, listener ownership, interruption, and owned-resource cleanup on Dolt
`2.3.2`.

A later review found that `dolt_force_transaction_commit=1` lets concurrent
transactions persist secondary-unique constraint violations after merge. The
normal table-scoped SQL identity can set this variable in its own session and
globally; `system_variables` and `user_session_vars` initialize safe defaults
but do not make them privilege-enforceable. Unreviewed Candidate `681c2e2`
therefore proposed No-go by treating the runtime SQL identity as adversarial.

The human project owner has now fixed the applicable trust boundary. Director
uses one trusted engine or adapter daemon as the sole holder of TaskStore
credentials and the sole component allowed to communicate directly with Beads
or Dolt. Agents, Reviewer Agents, helpers, UI and plugin clients, repositories,
prompts, and configuration files receive no SQL, TaskStore credentials, raw
connection, or direct store access. They submit typed, scoped commands to the
engine; the engine validates and executes those commands through the TaskStore
port.

This is a Director product-runtime boundary. It does not change this
repository's development use of Beads through the `bd` workflow in `AGENTS.md`;
that development database is not a Director runtime TaskStore.

A process already compromised as the trusted engine identity is outside this
database threat boundary. Direct Dolt is not required to resist deliberately
malicious SQL emitted by that already-compromised trusted principal. This
matches ADR-0008's actor model, which places the exact trusted plugin revision
and a process already running as the engine identity inside or beyond the
plugin trusted computing base.

This decision does not weaken ADR-0008's separate M0 stop condition. Director
must still prove that agents and repository-controlled processes cannot obtain
engine credentials, reach its raw TaskStore endpoint, or execute as the engine
principal. If that containment cannot be proven, this ADR's precondition fails
and M1 remains blocked.

## Question or hypothesis

Within the human-approved trusted-engine boundary, can supported direct Dolt
`2.3.2` provide Director's TaskStore mapping, transaction, optimistic
concurrency, idempotency, immutable-history, and multiwriter contract, with all
untrusted callers restricted to typed scoped commands and no raw SQL authority?

## Acceptance criteria

- Project, Workspace, Epic, Task, dependency, Run, Candidate, Command, Event,
  and Audit records have an explicit queryable mapping.
- Aggregate state and its supporting Command, Event, and Audit records commit
  or roll back together.
- Two writes from the same expected version produce exactly one winner; the
  loser is explicit, replay-safe, and leaves no partial record.
- Repeating an idempotency key returns the first durable outcome, while a
  conflicting request is rejected without mutation.
- Immutable records and every declared identity are protected against the SQL
  statement classes emitted by the trusted adapter contract.
- Foreign-key relationships and referenced parent identities remain valid when
  foreign-key checks are disabled by tested adapter sessions.
- The engine is the only TaskStore credential holder and raw SQL principal.
- Agents, clients, repositories, prompts, and durable TaskStore payloads never
  receive credentials, SQL, or direct store access.
- The adapter sets and reads back safe global and session
  `dolt_force_transaction_commit` values before writes and fails closed on any
  error or drift.
- Listener identity, credentials, interruption, and cleanup remain bounded and
  reproducible.

The database is not required to contain arbitrary malicious SQL from a
compromised engine process, because that process already controls the trusted
TaskStore adapter and its credentials.

## Trust boundary and authority

```text
Agents / UI / plugin clients / repositories
                 |
                 | typed scoped commands only
                 v
Trusted Director engine + TaskStore adapter
  - sole credential holder
  - sole SQL / Beads / Dolt client
  - validates scope, expected version, and idempotency
  - verifies safe commit variables before writes
                 |
                 | private direct connection
                 v
       Director-owned Dolt 2.3.2 schema
```

Required enforcement:

- TaskStore credentials exist only in engine-owned secret transport and are
  excluded from agents, MCP arguments, prompts, Organizer Git, product
  repositories, TaskStore rows, audit payloads, logs, diagnostics, and support
  bundles.
- The raw SQL listener is bound or authenticated so agent processes and plugin
  clients cannot use it. A typed Director RPC or MCP command never accepts SQL,
  a connection string, credentials, database selection, or caller-selected
  TaskStore scope.
- Every command is reauthorized in the engine against the fixed Project,
  Workspace, Task, Run, role, expected aggregate version, and idempotency key.
- Repository content and model output remain untrusted data. Neither can select
  an executable adapter, alter connection configuration, or receive raw store
  output.
- Exact engine revision and locked adapter dependencies are part of the trusted
  computing base. Compromise of that principal is a total TaskStore compromise,
  not a condition the database grants claim to contain.

## Evidence

### Versions and topology

The accumulated contract evidence used:

- Beads `1.2.2`, build/source commit
  `6c124203e771433a3550c348771a5b5e27fd3c21`;
- Dolt `2.3.2`, source tag commit
  `f0feb352b1d3f0919b88ecd28869e515afb60ee0`;
- Node.js `v26.7.0`;
- Debian 13 amd64 Linux, kernel `6.12.107+deb13-amd64`;
- isolated loopback servers, unique per-run identities, independent clients,
  disposable local data, and no remotes.

The complete accumulated procedures and outputs are in
[`docs/evidence/m0.4`](../evidence/m0.4/README.md).

### Accumulated TaskStore contract

The committed schema evidence records:

- 18 PASS, 4 intentionally reproduced Beads FAIL, and 5 CONTROL checks;
- explicit relational mapping for every required record class;
- atomic aggregate/Event rollback;
- one barriered same-version commit and one SQLSTATE `40001` / Error `1213`
  loser with zero partial records;
- exact command replay and explicit payload-mismatch rejection;
- 128 non-transaction-wrapped immutable-table observations: 118 denials and 10
  controlled appends;
- 117 direct ledger/guard attacks, 9 two-step attacks, and 18 deferred
  transaction attacks;
- 21 child/orphan probes under enforced, disabled, and relaxed foreign-key
  modes;
- 18 referenced-parent rename probes plus 3 legitimate aggregate updates;
- 9 credential checkpoints with no plaintext credential artifact; and
- authenticated listener, precise interruption, server-before-storage cleanup,
  and no cross-run attachment.

The historical timeout matrix was not rerun for this trust-boundary change. Its
six executable/output artifacts remain byte-identical to reviewed Candidate
`1a5389b7954d5fc8df578f878e8e8d434979b991`.

### Force-transaction residual

The minimal supported-interface reproduction is:

- [`verify-force-transaction-boundary.mjs`](../evidence/m0.4/verify-force-transaction-boundary.mjs);
- [`force-transaction-boundary-output.json`](../evidence/m0.4/force-transaction-boundary-output.json).

It configures global and per-user session defaults to `0`, uses an application
identity with only required table-level grants, and proves:

- required immutable appends and aggregate updates work;
- the identity can set its session value to `1`;
- it can set the global value to `1`, visible to the owner;
- a per-user safe default does not contain the global change and can instead
  make new app connections fail with a duplicate-initialization error;
- an owner reset to `0` restores required operations; and
- the owned server exits and its directory is removed.

This is evidence of a residual against the trusted engine identity, not a No-go
under the approved boundary. No untrusted agent or client possesses that
identity or a raw SQL path.

### Required fail-closed adapter control

The adapter must treat safe commit configuration as a checked precondition, not
as a durable privilege boundary:

1. At engine startup and after reconnect, use an engine-only control connection
   to set `@@GLOBAL.dolt_force_transaction_commit = 0` and read it back.
2. Before every write transaction, set
   `@@SESSION.dolt_force_transaction_commit = 0` on that exact connection and
   read back both session and global values.
3. Start no write until both reads equal `0`.
4. On a failed `SET`, failed read, unexpected type/value, connection reset,
   duplicate-initialization error, or later observed drift, roll back when
   possible, perform no retrying write, mark the TaskStore unhealthy, and route
   the Project to Degraded/Needs you.
5. Do not accept a fallback connection, identity, database, or delivery mode.
6. Record only a bounded redacted health code; never log credentials, SQL
   payloads, or raw server output.

Because the engine is the only raw principal, a value cannot drift through an
untrusted SQL caller without first violating the separate engine/agent
containment boundary. The checks still protect against operator drift,
connection reuse mistakes, and adapter bugs.

## P3 documentation corrections

Two limitations remain explicit:

1. The accumulated schema validator calls
   `validateGuardedSchema(immutableTableModel)` and does not cover the later
   `aggregates.id` ledger for collation, width, nullability, prefix, or
   expression drift. The live tested column was binary-collated and its
   mutation probes passed. `dir-m0.5` must extend this as a hard migration gate
   before any schema evolution; the current evidence does not claim otherwise.
2. The `aggregates` `BEFORE INSERT` identity guard rejects every duplicate-ID
   `INSERT ... ON DUPLICATE KEY UPDATE`, including version/data-only upserts.
   The adapter must mutate aggregate state only through
   `UPDATE ... WHERE id = ? AND version = ?`; ODKU is not part of the adapter
   contract.

## Alternatives considered

### Beads `1.2.2` public interfaces

Rejected by the reproducible public-CLI failures. Director product code must
not write Beads-owned tables or import its internal Go storage package.

### Treat the engine SQL identity as an adversarial principal

Rejected by explicit human trust-boundary decision. The engine and its adapter
are the trusted computing base and sole credential holder. A process that can
emit arbitrary SQL as that identity can already replace or corrupt the adapter,
its intent log, and its state observations.

### Rely on Dolt privileges to lock `dolt_force_transaction_commit`

Rejected. The minimal reproduction proves no such enforcement for the runtime
identity. The result is retained as a documented residual and fail-closed
adapter precondition.

### Expose restricted SQL to agents

Rejected. Statement filtering or an application promise would enlarge the
threat boundary and recreate the force-variable problem. Agents receive only
typed commands; SQL and credentials never cross the engine boundary.

### Server read-only mode

Rejected. It prevents required TaskStore writes.

### Another TaskStore

Not selected. Existing contingency `dir-m0.16` remains conditional and blocked.
It may be closed as not needed only after independent approval of this exact Go
Candidate; it must remain available if review rejects the boundary or evidence.

## Decision

**Go for one Director-owned direct Dolt `2.3.2` schema behind the trusted
engine's typed `TaskStore` adapter. No-go for Beads `1.2.2` as the runtime
store.**

This Go is valid only under the human-approved boundary and all enforcement
requirements above. The engine/adapter daemon is the sole raw store client and
credential holder. Agents, UI/plugin clients, repositories, and prompts have no
SQL, credential, connection, or direct TaskStore authority.

`dolt_force_transaction_commit` is not privilege-enforceable against the engine
identity. That fact is accepted as residual risk because deliberate compromise
of the trusted engine is outside the database boundary. The adapter must set and
verify session and global safe values before writes and fail closed on drift.

## Consequences

- `1.0` has one runtime TaskStore: the Director-owned direct Dolt schema behind
  the trusted typed adapter.
- `dir-m0.5` must prove synchronization, backup, restore, migration,
  partial-failure recovery, safe-value verification, and coverage of the
  `aggregates.id` migration gap against this schema.
- ADR-0008's agent/engine credential and authority-separation stop condition
  remains unresolved until independently proven. This ADR does not authorize
  M1 by itself.
- `dir-m0.10` must include the fail-closed variable check in interruption and
  reconciliation boundaries.
- `dir-m0.16` is conditional and blocked. Do not close it until this Candidate
  receives an independent `approve_candidate`; after approval, close it as not
  needed with the exact reviewed SHA linked.
- The unreviewed No-go Candidate `681c2e2` and its cancelled review remain
  historical audit records. They carry no verdict and no readiness authority.

## Independent verification

Pending fresh independent review of the exact changed Candidate SHA. The
Reviewer must verify the human boundary is stated consistently with ADR-0008,
run the minimal force-variable reproduction, confirm the six accumulated
evidence artifacts are unchanged and parseable, inspect both P3 constraints,
and confirm `dir-m0.16` remains conditional and blocked. Publication,
integration, Task closure, contingency closure, and workspace cleanup remain
post-review gates.
