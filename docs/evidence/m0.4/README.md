# M0.4 TaskStore mapping evidence

## Decision

**Go for direct Dolt `2.3.2` behind Director's trusted typed `TaskStore`
adapter. No-go for Beads `1.2.2` as the runtime store.**

This decision uses an explicit human-approved trust boundary:

- one trusted Director engine or adapter daemon is the sole TaskStore
  credential holder;
- only that engine may communicate directly with Beads or Dolt;
- agents, Reviewer Agents, helpers, UI and plugin clients, repositories, prompts,
  and configuration never receive SQL, TaskStore credentials, a raw connection,
  or direct store access; and
- all untrusted callers submit typed scoped commands that the engine validates
  and executes through the TaskStore port.

The database is not required to resist deliberately malicious SQL emitted by an
already-compromised trusted engine identity. Such a principal can already
replace or corrupt the trusted adapter and its observations. ADR-0008's separate
requirement to keep engine credentials and raw endpoints inaccessible to agents
remains an M0 stop condition; this evidence does not claim it is solved.

## Selected evidence

The accumulated contract suite proves the direct-Dolt mapping and database
behavior inside that boundary:

- [`taskstore-contract.mjs`](taskstore-contract.mjs) and
  [`observed-output.json`](observed-output.json);
- [`verify-interruption.mjs`](verify-interruption.mjs) and
  [`interruption-output.json`](interruption-output.json);
- [`verify-listener-isolation.mjs`](verify-listener-isolation.mjs) and
  [`listener-isolation-output.json`](listener-isolation-output.json).

The committed output records:

- 18 PASS, 4 intentionally reproduced Beads FAIL, and 5 CONTROL checks;
- explicit mapping for Project, Workspace, Epic, Task, dependency, Run,
  Candidate, Command request/outcome, Event, and Audit records;
- atomic aggregate/Event rollback;
- one barriered same-version winner and one SQLSTATE `40001` / Error `1213`
  loser with zero partials;
- exact command replay and explicit mismatched-payload rejection;
- 128 non-transaction-wrapped immutable-table observations: 118 denials and 10
  controlled appends;
- 117 ledger/guard attacks, 9 two-step attacks, and 18 deferred transaction
  attacks;
- 21 missing-parent probes under enforced, disabled, and relaxed foreign-key
  modes;
- 18 referenced-parent rename probes plus 3 legitimate aggregate updates;
- 9 credential checkpoints; and
- listener identity, interruption, collision isolation, and owned cleanup.

The historical timeout matrix was not rerun for the trust-boundary decision.
Those six executable/output artifacts remain byte-identical to Candidate
`1a5389b7954d5fc8df578f878e8e8d434979b991`, whose complete accumulated suite
was independently reproduced before the human boundary decision.

## Beads result

Beads `1.2.2` remains No-go through supported public interfaces:

- no generic expected-version update;
- the supported batch grammar cannot atomically store structured aggregate,
  Command, and Event facts;
- `bd update` changes an Event record; and
- recreating an explicit ID overwrites conflicting command content.

Beads remains Director's development tracker. Product runtime code must not
write Beads-owned tables or import its internal Go storage package.

## Force-transaction residual

The minimal public-interface reproduction is:

- [`verify-force-transaction-boundary.mjs`](verify-force-transaction-boundary.mjs);
- [`force-transaction-boundary-output.json`](force-transaction-boundary-output.json).

Run from the repository root:

```text
node docs/evidence/m0.4/verify-force-transaction-boundary.mjs
```

It uses exact Dolt `2.3.2`, one disposable loopback server, safe global and
per-user session defaults, and a runtime identity with only the table grants
needed to append an immutable record and conditionally update aggregate state.
The complete reproduction proves:

| Check | Observation |
|---|---|
| Initial global value | `0` |
| Initial engine-session value | `0` |
| Runtime global privilege | None beyond `USAGE`; table grants only |
| Required immutable append and aggregate update | Passed |
| Runtime identity session override | Accepted; read back `1` |
| Runtime identity global override | Accepted; owner read back `1` |
| New connection with per-user safe default after global override | Rejected: variable initialized more than once |
| Owner reset global to `0` | Passed |
| Required writes after reset | Passed |
| Server exit and owned-directory cleanup | Passed |

The result is unchanged: `dolt_force_transaction_commit` is not
privilege-enforceable against the SQL identity that performs writes. Under the
approved trust boundary this is a documented residual against the trusted
engine, not a database No-go. No untrusted agent or client receives that
identity or can issue SQL.

The installed `dolt sql-server --help` and public
[configuration reference](https://www.dolthub.com/docs/sql-reference/server/configuration/)
show that `system_variables` and `user_session_vars` initialize values but do
not provide a per-user statement or variable-deny policy. The public
[system-variable reference](https://www.dolthub.com/docs/sql-reference/version-control/dolt-sysvars/)
states that `dolt_force_transaction_commit=1` allows merges with conflicts,
constraint violations, and other correctness failures. Server read-only mode
is not a usable boundary because it also disables required TaskStore writes.

## Required adapter control

The engine adapter must use the force variable as a checked fail-closed
precondition:

1. At startup and after reconnect, use an engine-only control connection to set
   `@@GLOBAL.dolt_force_transaction_commit = 0` and read it back.
2. Before every write transaction, set
   `@@SESSION.dolt_force_transaction_commit = 0` on that exact connection and
   read back both the session and global values.
3. Begin no write unless both values are exactly `0`.
4. On any failed set/read, unexpected value, duplicate-initialization error,
   connection reset, or drift, roll back when possible, perform no fallback or
   retrying write, mark the TaskStore unhealthy, and route the Project to
   Degraded/Needs you.
5. Emit only a bounded redacted health code. Never log SQL payloads,
   credentials, connection strings, or raw server output.

This control addresses operator drift, connection reuse mistakes, and adapter
bugs. It is not presented as privilege enforcement against the trusted engine.

## Raw-access exclusion

- A typed Director RPC or MCP tool never accepts SQL, credentials, connection
  strings, database selection, or caller-chosen TaskStore scope.
- The engine reauthorizes Project, Workspace, Task, Run, role, expected version,
  and idempotency key from its own durable context.
- TaskStore credentials are excluded from prompts, MCP arguments, Organizer
  Git, product repositories, TaskStore rows, events, audit payloads, logs,
  diagnostics, and support bundles.
- Repository content and model output cannot select the adapter executable,
  mutate connection configuration, or receive raw database output.
- Failure to prove OS/process separation between agent execution and the engine
  credential/raw endpoint fails ADR-0008 and keeps M1 blocked.

## P3 documentation constraints

### Coverage-validator scope

The accumulated harness calls `validateGuardedSchema(immutableTableModel)`. It
does not validate the later `aggregates.id` ledger in `identityLedgerModel` for
collation, byte width, nullability, prefix, or expression drift. The tested live
column is binary-collated and its mutation probes pass. `dir-m0.5` must extend
this coverage as a hard migration gate before schema evolution; the current
evidence does not claim the aggregate identity is covered by that validator.

### Aggregate update operation

The `aggregates` `BEFORE INSERT` identity ledger fires before Dolt selects the
`ON DUPLICATE KEY UPDATE` path. Every duplicate-ID upsert is denied, including a
version/data-only upsert. The adapter must use an expected-version update:

```sql
UPDATE aggregates
SET version = ?, data = ?
WHERE id = ? AND version = ?;
```

ODKU is not part of the aggregate adapter contract.

## Reproduction and integrity checks

The minimal boundary procedure is the only live database reproduction required
for this trust-boundary revision. The historical slow suite is retained and can
be reproduced later when its narrower database behavior must be revalidated:

```text
DIRECTOR_M04_FOCUS=referenced-parent-identities node docs/evidence/m0.4/taskstore-contract.mjs
node docs/evidence/m0.4/taskstore-contract.mjs
node docs/evidence/m0.4/verify-interruption.mjs
node docs/evidence/m0.4/verify-listener-isolation.mjs
```

For this Candidate, validate instead:

- all four `.mjs` files pass `node --check`;
- all four committed JSON outputs parse;
- the minimal output records the raw force-variable observations and the
  human-approved trusted-engine Go boundary; and
- the six accumulated executable/output artifacts are byte-identical to
  Candidate `1a5389b7954d5fc8df578f878e8e8d434979b991`.

## Compatibility, cleanup, and follow-up

Compatibility is bounded to the exact Beads, Dolt, Node, and Debian/Linux
versions recorded above. Synchronization, backup, restore, migration, partial-
failure recovery, the aggregate-validator extension, and safe-variable
reconciliation remain gates for `dir-m0.5` and `dir-m0.10`.

The minimal reproduction stops its owned server, restores the global variable,
and removes its owned directory. Generated credentials do not enter argv,
captured output, or the credential-free server configuration.

Existing contingency `dir-m0.16` remains open, conditional, unclaimed, and
blocked by `dir-m0.4`. Do not close it until the new direct-Dolt Go Candidate is
independently approved; then close it as not needed with the exact reviewed SHA
linked. If review rejects the boundary or evidence, retain the contingency.
