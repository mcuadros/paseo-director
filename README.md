# Director for Paseo

> **Authority warning — read before installation:** on exact Paseo 0.7.2,
> Director for Paseo requires a full daemon-operator credential. The trusted,
> unsandboxed connector can invoke the daemon user's complete public API.
> Install only if you accept this bounded P2 residual risk.

The credential must be a non-empty owner-only file outside both the repository
and every Paseo-managed plugin checkout. Before constructing a Paseo client,
the connector resolves symlinks and proves the credential directory is
bidirectionally disjoint from the checkout, engine source, engine cache, and
every derived engine path. It checks that directory and every canonical
ancestor through the filesystem root, rejecting group/other-writable ancestors
unless Linux sticky-bit semantics protect a trusted-owner child in a directory
owned by the connector user or root (for example, an owner-only credential
directory under root-owned `/tmp`). It also rechecks the credential file and
ancestor identities while loading: the path must match the `O_NOFOLLOW` file
descriptor before the read, mutable file metadata must remain unchanged across
the read, and every checked ancestor must retain its identity afterward. A
detected pre-open substitution, read-time metadata change, or ancestor
substitution fails closed. Its bytes and path never enter Director Engine
arguments, environment, protocol, UI, store, projections, logs, timelines,
diagnostics, or support bundles. Connector startup and reload fail closed before
host mutation when the file is absent, empty, broadly readable, in an unsafe
directory, or overlaps any protected path. The connector advertises only
`credentialScope=full-daemon-operator`, the exact contract version/hash, and the
fixed capability set. Director will narrow this authority when a supported
connector-scoped Paseo mechanism is available and evidenced.

Director Engine is standalone Go software for planning, executing, reviewing,
and delivering medium-to-large software products. It is usable and publishable
without Paseo. Director for Paseo is the TypeScript host package containing the
full React Native UI shell, a minimum policy-free connector, and Paseo-only
integration. All workflow truth and lifecycle decisions remain in Director
Engine.

Director for Paseo is an independent community plugin. It is not affiliated
with, endorsed by, maintained by, or sponsored by Paseo.

This walking skeleton intentionally implements no Task execution workflow. It
establishes the process, package, UI, host contract, credential, distribution,
configuration/revision, direct-Dolt persistence, and CI boundaries on which
later M1 Tasks can build.

## Development

Requirements are Linux, Node.js 22 or newer, npm with lockfile support, Go
1.26.5, and Dolt 2.3.2. Install and run every local check deterministically:

```text
npm ci --ignore-scripts --no-audit --no-fund
npm run ci
```

Useful focused commands:

```text
npm run architecture:check
npm run build
npm run test:engine
npm run test:host
npm run contract:check
npm run smoke
```

`npm run smoke` compiles the standalone development engine into an owned
temporary cache, runs its `version` and no-product-behavior startup paths with
an empty environment, and removes the temporary cache.

## Paseo host configuration

Director for Paseo targets exact Paseo 0.7.2. Its server entry requires these
daemon-process environment values before installation or reload:

- `DIRECTOR_PASEO_URL`: the daemon WebSocket URL.
- `DIRECTOR_PASEO_CREDENTIAL_FILE`: an absolute path to the owner-only
  connector credential outside the checkout.
- `DIRECTOR_ENGINE_MODE`: exactly `release` or `development`.
- `DIRECTOR_ENGINE_SOURCE_ROOT`: an absolute engine source path, required only
  in development mode.
- `XDG_CACHE_HOME`: optional platform cache base; engine artifacts are always
  kept under its `director/engines` subtree outside the plugin checkout.

The committed release descriptor explicitly marks `0.0.0-scaffold` as
unpublished and declares no assets or digests. Release resolution rejects that
state before any fetch. A later coordinator-owned release must replace it with
normalized URLs under the exact
`https://github.com/mcuadros/paseo-director/releases/download/` origin/path
prefix and non-empty SHA-256 pins for both the engine and exact-source notices.
Dot-segment traversal that normalizes outside that prefix, another origin/path,
URL credentials, query, or fragment is invalid. Empty-input digests are invalid.
Release mode never compiles as a fallback.
Development mode always compiles the selected Go source and never downloads or
falls back to release mode. A missing or conflicting mode fails closed.

The plugin manifest declares exact argv for its locked dependency preparation.
Paseo executes those commands as trusted, unsandboxed daemon-host code with the
daemon user's access during installation and update. Review the source,
lockfile, dependencies, and future updates before installing. Host modules and
external executables are neither vendored nor redistributed.

## Organizer configuration

Each Organizer repository is discovered through one strict
`paseo-director.json`. Director Engine owns its version 1 schema, semantic
validation, pending/active revision state, deterministic Preview/Apply boundary,
and immutable Run configuration snapshots. Invalid or unapproved revisions
never become Run inputs. Published-schema validation is necessary but not
sufficient: engine admission also rejects invalid Unicode, canonical expansion,
password-bearing or unsafe Git remotes, unsafe paths, and semantic conflicts.
Director for Paseo only renders the engine projection and submits typed
commands; it owns no activation or snapshot policy.

See [Organizer configuration and revisions](docs/configuration.md) for the
complete minimal document and transition contract.

## Architecture

The Go module is split into inward-pointing boundaries:

```text
cmd (composition only)
├── adapters ────────────────→ ports ──→ domain
├── agent-runtime ──→ application ─────→ domain
│                              ├────────→ ports
│                              ├────────→ projection ──→ domain
│                              └────────→ reducer/* ───→ domain
└── standalone engine executable
```

`reducer/eligibility`, `launch`, `retry`, `escalation`, `routing`, and
`closure` are the exclusive homes for the six pure decision reducers. The
seven closed agent-outcome schemas live under `domain/agentoutcome`; claims are
inputs, never lifecycle evidence. Infrastructure adapters and the fixed-scope
agent runtime may translate or perform an authorized effect but cannot own a
reducer, policy, projection decision, or lifecycle decision. The typed
TaskStore authority remains inside the engine; its direct-Dolt implementation
is an infrastructure adapter rather than a policy owner.

### TaskStore skeleton

The engine-owned [`TaskStore` port](engine/ports/taskstore/taskstore.go) exposes
only typed Project, Task, Run, Candidate, Command, and Event records. It has no
SQL, connection, credential, database-selection, or Beads surface. The one
runtime implementation is [`DoltTaskStore`](engine/adapters/dolt/taskstore.go),
which talks to a separately supervised Dolt 2.3.2 SQL server through private
control and writer identities.

`Bootstrap` installs schema version 1 only into an empty, explicitly selected
database and verifies the complete expected table and guard-trigger sets plus
the three exact guard seed rows on every later start; a missing or extra
table/trigger, changed guard row, incomplete or foreign schema, wrong version,
wrong database, or wrong store fails closed. Before each write the adapter sets
and reads back the safe global and exact-connection session commit values,
verifies the database and store identity, then commits the aggregate, immutable
Command outcome, and Event in one transaction. Mutable aggregates use exact
expected-version updates. Candidate, Command, and Event identities use
append-only ledgers and triggers, including parent guards which remain active
when a writer disables foreign-key checks.

Applied Event allocation takes one transactional stream lock before assigning
`global_sequence`. The lock is held through commit, so a consumer which resumes
strictly after its last returned cursor cannot miss a lower-sequence Event that
commits later. Commands rejected before Event creation do not take the lock.

Those guards contain the row-level DML emitted by the trusted adapter:
`INSERT`, `UPDATE`, `DELETE`, duplicate/ODKU, and `REPLACE` forms. Dolt/MySQL
`TRUNCATE` is DDL, does not execute row-level delete triggers, and is outside
that containment claim. The production runtime writer identity must therefore
exclude `DROP` privilege, which also makes `TRUNCATE` unavailable. Its DML
grants must be table-scoped so `guard_constants`, `parent_guard`, and
`immutable_write_guard` are read-only: the writer receives `SELECT` but no
direct `INSERT`, `UPDATE`, or `DELETE` on those three tables. Schema bootstrap
and migration authority remains a separate engine-only control path and must
never be exposed to agents, connectors, UI, repositories, or prompts.

All port-facing connection, query, scan, cursor, replay-entry, and schema
failures are typed as `HealthError` or `SchemaError`. Their bounded codes unwrap
to the stable port sentinels and contain no raw driver, listener, credential,
SQL, table, address, or server output. Every singular and collection reload
validates Project, Task, Run, and Candidate values before returning them.

The schema remains ordinary externally inspectable Dolt tables, so the
ADR-0012 pause/inspection, online-backup, fresh-restore, migration, and remote
synchronization procedures remain applicable. This M1 skeleton makes no
production-scale claim; the agreed-scale proof remains assigned to `dir-m5.10`.
Linux contract tests start disposable real Dolt 2.3.2 servers, create and reload
all six record types, exercise idempotent replay and same-version contention,
query the resumable Event cursor, inspect the tables externally, and verify the
append-only and schema/identity fail-closed guards. Adversarial cases invoke
every TaskStore port method with wrong credentials, a stopped backend, and its
relevant missing table; they also cover every missing expected schema table and
guard trigger, exact guard seed rows, invalid singular/collection persisted
records, and the guard-read-only, DROP-free runtime writer grant.

The closed `paseo-director.json` contract lives under
`domain/configuration`. Its optimistic pending/active revision aggregate and
frozen Run snapshots live under `application/configuration`; no host package
owns those transitions.

The Paseo 0.7 host uses the stable mixed `index.ts` entry. The Director
repository convention separates runtime code as `ui/*.client.*`,
`rpc/*.shared.ts`, `generated/*.shared.ts`, and `connector/*.server.ts`. This
keeps the complete React Native UI independent of the minimum connector and
leaves a mechanical path to separate entries if a later stable Paseo version
is explicitly admitted.

The engine owns [the versioned host schema](engine/ports/host/host-interface.v1.json).
It generates [the TypeScript client](generated/host-contract.shared.ts),
and CI rejects any drift. The contract hash is derived from duplicate-key-safe
canonical JSON with sorted object keys, so whitespace and object-key order do
not change identity while semantic edits do. Runtime handshake validation
rejects a stale version, hash, credential scope, missing capability, extra
capability, reordered capability, or extra descriptor field before mutation.

The connector implements only the eight fixed capabilities approved in
[ADR-0017](docs/adr/0017-standalone-engine-connector-authority-boundary.md).
It does not contain domain, application, orchestration, eligibility, scheduling,
retry, escalation, routing, reconciliation, transition, TaskStore, projection,
or closure policy.

- [Approved product and engineering plan](docs/PLAN.md)
- [Contributing workflow](CONTRIBUTING.md)
- [Deterministic coordinator CLI](docs/coordinator-cli.md)

Development is tracked with Beads. Run `bd ready` to inspect unblocked work.
