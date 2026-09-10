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

The engine now includes the production primary Task Agent launch lifecycle:
one exact lease/repository/base/branch/worktree binding, one registered Paseo
Execution Workspace view over the Director-owned worktree, one frozen Worker
profile and scoped MCP session, and one parentless Task Agent per Run. Native
identity and the full Director Workers correlation are persisted after the
zero-work bootstrap and before the separately notified real prompt. The thin
Paseo 0.7.2 connector implements these public workspace/agent effects and
reconciles exact native facts without owning retry or workflow policy. See
[Primary Task Agent lifecycle](docs/primary-lifecycle.md).

Primary replacement now reconciles durable and exact native facts after
provider, connector, daemon, callback, lease, or coordinator interruption. It
adopts a correct persistent session, refuses to re-prompt a poisoned one,
consumes at most one lease-fenced replacement authority, preserves unrelated
Reviewer/helper/orphan resources, and routes every ambiguity or second failure
to Needs you. See [Primary replacement and orphan recovery](docs/primary-recovery.md).

The walking skeleton also retains one explicitly fake execution path. It moves one
queued Task through engine-owned Eligibility, Launch, Retry, Escalation,
Routing, and fake-terminal Closure reductions; a Director-owned disposable Git
worktree; a fake registered host view and top-level Task Agent; an externally
observed exact Candidate; direct-Dolt state; and terminal fixture cleanup. It
never selects a real provider or connects to Paseo. Engine startup now scans
every persisted walking-skeleton Run, validates its immutable Command/Event and
Candidate graph, recovers exact execution IDs and cleanup intents, refreshes
the external frontier through a replaceable policy-free connector, and resumes
one reducer-authorized transition without duplicating an unknown effect.
The fake adapter remains test-only; validation/review/delivery remain later
milestone work. See
[Fake execution vertical path](docs/fake-execution.md).

Alongside that fake path, the walking skeleton establishes the process,
package, UI, host contract, credential, distribution, configuration/revision,
previewed local Create/Adopt Organizer, direct-Dolt persistence, and CI
boundaries on which later M1 Tasks can build.

Alongside that engine-owned fake path, the Board/List slice adds only read-only
presentation behavior. Director Engine reads persisted Tasks through its typed
TaskStore port, derives their current walking-skeleton state, and serves one
bounded snapshot. Director for Paseo transports that snapshot through a strict
plugin RPC and renders it without owning TaskStore access or lifecycle truth.
Later Tasks extend real execution behavior on these boundaries.

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
temporary cache, runs its `version` and side-effect-free startup paths with an
empty environment, and removes the temporary cache.

## Paseo host configuration

Director for Paseo targets exact Paseo 0.7.2. Its server entry requires these
daemon-process environment values before installation or reload:

- `DIRECTOR_PASEO_URL`: the daemon WebSocket URL.
- `DIRECTOR_PASEO_CREDENTIAL_FILE`: an absolute path to the owner-only
  connector credential outside the checkout.
- `DIRECTOR_ENGINE_URL`: an origin-only loopback HTTP URL with an explicit
  port, such as `http://127.0.0.1:7041`, for the separately supervised Director
  Engine Board endpoint.
- `DIRECTOR_ENGINE_MODE`: exactly `release` or `development`.
- `DIRECTOR_ENGINE_SOURCE_ROOT`: an absolute engine source path, required only
  in development mode.
- `XDG_CACHE_HOME`: optional platform cache base; engine artifacts are always
  kept under its `director/engines` subtree outside the plugin checkout.
- `GOMODCACHE`: optional absolute development module-cache path; otherwise the
  Go toolchain's standard user module cache is used. The path is included in
  the connector credential-disjointness boundary and only reaches the
  development compiler environment.

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

## Minimal Board/List runtime

The separately supervised engine opens an existing, already bootstrapped
direct-Dolt TaskStore and serves the read-only Board contract on an explicit
loopback address:

```text
director-engine serve-board \
  --listen 127.0.0.1:7041 \
  --taskstore-config /absolute/private/director-engine.json
```

The Board endpoint is unauthenticated. Its public contract version/hash are
compatibility checks, not credentials. Run it only on the accepted single-user
Linux host and keep its port confined to loopback; never proxy or expose it to
another host or user. Any local process able to reach the port can read Project
names, Task titles, Run numbers, and Candidate SHAs.

The configuration file and any referenced password files must be absolute,
regular, owner-only files outside repositories. Unknown, duplicate, missing,
oversized, trailing, or unsupported fields fail closed. The closed version 1
shape is:

```json
{
  "schemaVersion": 1,
  "storeId": "director-project-store",
  "control": {
    "address": "127.0.0.1:3307",
    "database": "director",
    "user": "director_control",
    "passwordFile": "/absolute/private/dolt-control.password"
  },
  "writer": {
    "address": "127.0.0.1:3307",
    "database": "director",
    "user": "director_writer",
    "passwordFile": "/absolute/private/dolt-writer.password"
  }
}
```

`passwordFile` may be `null` only for an explicitly configured passwordless
local test store. The server never bootstraps or repairs the selected database;
missing identity, schema, or connection facts fail startup or the
query closed with a bounded error. The endpoint accepts only the generated
contract version and hash and returns no SQL, credentials, paths, objectives,
or acceptance-criteria text.

The Project Board panel loads the snapshot with TanStack Query, performs no
automatic request retry, and refreshes the read-only query every two seconds.
It renders loading, error, empty, cached-update, and data states. Wide mode
defaults to Board and shows every applicable lane; compact mode defaults to
List and shows one selected Board lane at a time. All text and surfaces use
Paseo theme tokens, and controls expose roles, labels, selected state, and live
status announcements.

The M1 state mapping is intentionally narrow: persisted Task/Run attention is
`Needs you`, no Run is `Queued`, a latest Run without a Candidate is `Building`,
and a latest Run with a persisted Candidate is `Validating`. A Candidate alone
can never produce `In review` or `Ready`; those states require later persisted
Review and Validation facts. See
[Board/List walking-skeleton contract](docs/board-list.md).

The contract-first M2 presentation extends the plugin UI without claiming its
later runtime implementation. Its separate engine-owned planning schema and
generated Zod client cover Project/Workspace/Epic navigation, closed filters
and stable sorts, cursor paging, Task detail, configuration Preview/Apply,
scheduler/capacity facts, Launch now, dependency override, and every
engine-returned allowed action/explanation. Runtime planning handlers currently
fail explicitly instead of using fixture authority; deterministic fixtures are
test-only. See the
[M2 planning presentation contract](docs/planning-surface.md).

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

The Go Organizer application also supports read-only Create/Adopt previews,
verified-human Apply, interruption-safe local repository creation, read-only
adoption, durable TaskStore reopen, and exact revision/configuration drift
checks. Both flows validate configured Workspace roots and origin remotes
without modifying product repositories. The minimal Create mode is local-only;
remote Organizer creation is not implied. Organizer Git calls neutralize
repository-local and worktree-scoped executable configuration without following
external includes, bound output during streaming, use raw NUL-delimited paths
for non-ASCII-safe recovery, and treat the committed request marker only as
integrity correlation beneath an owner-controlled parent directory.

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
The same high-water mark is exposed through the typed `LatestEventSequence`
read and serialized as decimal text in Board snapshots, avoiding JavaScript
integer truncation.

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

Project records now also carry the bounded Organizer repository projection.
During a confirmed Create, the Project is paused and temporarily retains the
canonical pending configuration needed to recover the approved effect saga;
after the exact initial Git revision is committed, activation clears that
payload and retains only repository identity, revision, and configuration hash.
Adopt persists the same active projection atomically after a read-only exact
repository check. Existing non-Organizer walking-skeleton Project rows remain
readable, while malformed or contradictory Organizer projections fail closed.

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
owns those transitions. Organizer repository Preview/Apply/recovery lives under
`application/organizer`, calls only the typed TaskStore and Organizer repository
ports, and uses the policy-free `adapters/organizergit` implementation for
direct-argv Git and atomic filesystem effects.

The Paseo 0.7 host uses the stable mixed `index.ts` entry. The Director
repository convention separates runtime code as `ui/*.client.*`,
`rpc/*.shared.ts`, `generated/*.shared.ts`, and `connector/*.server.ts`. This
keeps the complete React Native UI independent of the minimum connector and
leaves a mechanical path to separate entries if a later stable Paseo version
is explicitly admitted.

The engine owns [the versioned host schema](engine/ports/host/host-interface.v1.json),
including the Board query shape and closed state vocabulary.
It generates [the TypeScript client](generated/host-contract.shared.ts),
and CI rejects any drift. The contract hash is derived from duplicate-key-safe
canonical JSON with sorted object keys, so whitespace and object-key order do
not change identity while semantic edits do. Runtime handshake validation
rejects a stale version, hash, credential scope, missing capability, extra
capability, reordered capability, or extra descriptor field before mutation.

The adjacent engine-owned
[planning schema](engine/ports/planning/planning-surface.v1.json) has its own
version and canonical hash and generates the only
[planning client](generated/planning-contract.shared.ts) used by the planning
UI and RPC boundary. It extends presentation contracts without adding a second
Paseo lifecycle host port.

The connector implements only the eight fixed capabilities approved in
[ADR-0017](docs/adr/0017-standalone-engine-connector-authority-boundary.md).
It does not contain domain, application, orchestration, eligibility, scheduling,
retry, escalation, routing, reconciliation, transition, TaskStore, projection,
or closure policy.

The M1 fake adapter implements that same generated typed host contract entirely
inside the Go test fixture. Host-view registration never transfers product
worktree ownership, and the production TypeScript connector remains a
policy-free boundary rather than a second execution engine.

- [Approved product and engineering plan](docs/PLAN.md)
- [Contributing workflow](CONTRIBUTING.md)
- [Deterministic coordinator CLI](docs/coordinator-cli.md)
- [Event-driven delivery fast path](docs/delivery-fast-path.md)
- [Director Workers visibility contract](docs/director-workers.md)

Development is tracked with Beads. Run `bd ready` to inspect unblocked work.
