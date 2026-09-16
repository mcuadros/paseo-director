# Director for Paseo

> **Authority warning — read before installation:** on exact Paseo 0.7.2,
> trusted plugin handlers receive the daemon user's complete public Paseo API.
> Director uses only that handler-scoped object. It never copies the daemon
> password into a runtime file, process argument, or secondary WebSocket.

The plugin ships a portable Go runtime controller. An unpublished default-main
checkout builds that bootstrap once during add/update; the bootstrap builds its
exact Engine once. A published alpha/beta/stable package ships a manifest-pinned
precompiled bootstrap that downloads only pinned precompiled Engine/notices and
canonical Dolt. Go starts and supervises only private cached executables; Node
does not download or supervise them. Neither channel uses `PATH`, a system
service, or a Paseo restart. Reload never builds, and a release never compiles
or falls back to source.

Director Engine is standalone Go software for planning, executing, reviewing,
and delivering medium-to-large software products. It is usable and publishable
without Paseo. Director for Paseo is the TypeScript host package containing the
full React Native UI shell, a minimum policy-free connector, and Paseo-only
integration. All workflow truth and lifecycle decisions remain in Director
Engine.

Director for Paseo is an independent community plugin. It is not affiliated
with, endorsed by, maintained by, or sponsored by Paseo.

## Documentation

The [public documentation index](docs/README.md) provides a complete journey
for [new users](docs/getting-started.md), [operators](docs/operator-guide.md),
and [developers](docs/developer-guide.md), together with the
[security boundary](docs/security-boundaries.md) and a maintained
[multi-repository example Organizer](examples/organizer/README.md).

Director 1.0 supports Linux amd64 and exact stable Paseo 0.7.2 only. Another
0.7.x release is not inferred compatible and the Paseo 0.8 preview is not a
production target. Default `main` is the explicit source-testing channel. Use
an immutable reviewed tag only after its descriptor and assets are published.

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

The maintained production entry path includes engine-owned direct-Dolt
bootstrap, native Paseo Project selection and generated Organizer Preview,
closed planning mutations, atomic scheduling, real host/recovery callbacks,
Candidate/CI/independent-Review delivery, feedback correction, maintenance,
and guarded cleanup. See [Production onboarding and workflow](docs/production-onboarding.md).

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
The fake adapter remains test-only and is not a production fallback. See
[Fake execution vertical path](docs/fake-execution.md).

Alongside that fake path, the walking skeleton establishes the process,
package, UI, host contract, credential, distribution, configuration/revision,
previewed local Create/Adopt Organizer, direct-Dolt persistence, and CI
boundaries on which later M1 Tasks can build.

Director Home now provides host-bound Create/Adopt Preview/Apply entry points,
cross-Project health and active-work summaries, Needs-you aggregation,
Organizer/Board access, read-only engine Doctor reports, and exact
server-confirmed Repair Preview/Apply. It keeps offline cached facts visibly
stale and disabled and never selects another host.
See [Director Home and Project health](docs/director-home.md).

Project Operations adds engine-owned hybrid reconciliation status, independent
Organizer Git and TaskStore/Dolt partial-sync reasons, payload/path-free audit,
14-day/100-MiB code-only technical logs, and a two-press local redacted support
bundle flow. Bundles are mode `0600`, contain only four allowlisted JSON files,
and are never uploaded automatically. See
[Sync, reconciliation, audit, logs, and support bundles](docs/operations-diagnostics.md).

The standalone engine reconciles an exact daily direct-Dolt backup, proves
every backup through a fresh restore, expires validated backups after seven
days, recovers the schema-1-to-schema-2 migration around audited Project
pause/resume commands, and refuses startup or Run launch below the fixed
10-percent free-space floor. Low-space cleanup can expire only eligible backups
and compact known bounded logs; it cannot address Git refs, worktrees, or other
unintegrated recovery material. See
[TaskStore backup, migration, retention, and disk safety](docs/taskstore-maintenance.md).

## Installation and updates

Director uses Paseo's supported Git source lifecycle and targets only exact
Paseo 0.7.2 on Linux amd64 with glibc and Node.js 22 or newer. Before Paseo
compiles a candidate, the manifest installs only the production dependency closure with
`npm ci --omit=dev --ignore-scripts --no-audit --no-fund` and runs the committed
host/dependency verifier. The lock audit permits only integrity-pinned npm
registry artifacts and executes no package lifecycle scripts. A registry,
lock, dependency, compatibility, or compile failure leaves the prior installed
commit active.

To test the exact current default main before a release exists:

```text
paseo plugin add mcuadros/paseo-director
paseo plugin status director
paseo plugin update director
paseo plugin reload director
```

Main add/update requires trusted system Go 1.26.5 and an existing
Go-sum-verified module cache. Once a channel is published, install and update
without Go:

```text
paseo plugin add mcuadros/paseo-director --ref <published-alpha-beta-or-stable-ref>
paseo plugin status director
paseo plugin update director
paseo plugin reload director
```

`update` activates a changed Git candidate and `reload` creates or adopts the
same plugin-owned Go controller without restarting Paseo or interrupting other
agents or workspaces. Tags and exact commits are immutable pins and do not advance through update.
See [installation, update, rollback, and compatibility](docs/installation-update.md)
before installing; it includes release artifacts, diagnostics, failure
recovery, and removal behavior.

## Public policies

- [Apache-2.0 licensing and exact dependency notices](docs/licensing.md)
- [Security and private vulnerability reporting](SECURITY.md)
- [Support and escalation](SUPPORT.md)
- [Exact compatibility](docs/compatibility.md)
- [Changelog](CHANGELOG.md)
- [Release process and channel gates](docs/release-process.md)

No tagged alpha, public beta, or stable channel has been published. Activation
is live and plugin-scoped and preserves unrelated agents/workspaces. Stopping
the Paseo daemon or host is not a supported workaround. The first usable
`1.0.0-alpha.1` still requires release-CI-built assets, a later reviewed
metadata commit, and separately authorized tag, upload, and alpha-channel
effects.

## Development

Requirements are Linux, Node.js 22 or newer, npm with lockfile support, Go
1.26.5, and Dolt 2.3.2. Install and run every local check deterministically:

```text
npm ci --ignore-scripts --no-audit --no-fund
npm run ci
```

Useful focused commands:

```text
npm run docs:check
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

## Paseo host and managed runtime

Director for Paseo targets exact Paseo 0.7.2. Each RPC receives the public
Paseo API from its plugin handler, so no daemon URL or connector credential is
configured and a Tailscale-only listener works unchanged. Legacy runtime files,
`DIRECTOR_PASEO_*`, `sourceRoot`, module-cache, and development-mode values have
no runtime authority.

Add/update preparation resolves the descriptor-selected channel and writes a
static verified Go-bootstrap pin. The first RPC launches only that bootstrap's
control command. The long-lived Go controller owns canonical Dolt and the
prepared-main or published-release Engine, private XDG paths, identity,
credentials, TaskStore bootstrap, locks, health, backoff, controlled handoff,
and lease expiry. JavaScript does not download runtime assets, build the Engine,
or supervise children. No Paseo daemon or machine restart is supported or
required.

```text
paseo plugin reload director
paseo plugin ls --json
paseo plugin logs director --json
```

The bounded `DIRECTOR_ACTIVATION_READY` record reports the exact connector,
Engine, Dolt, and controller identities with a `running-current` result. It
contains no URL, private path, credential, raw environment, or secret. The native selection resolver accepts
one current public Paseo Project/workspace pair and derives its Project ID/name,
repository root, Workspace identity/name, and working directory for the M6.11
selector; none of those facts belongs in this runtime JSON.

See [Getting started](docs/getting-started.md) and the
[operator guide](docs/operator-guide.md) for the supported activation and
recovery path.

The schema-2 committed release descriptor explicitly marks `0.0.0-scaffold` as
unpublished and declares no Engine assets or digests. This is the explicit
default-main source-testing channel: declared add/update preparation builds the
small Go bootstrap once from the exact checkout; that bootstrap builds the
exact Engine once into its private content-addressed cache. Runtime and reload
use only the static installed bootstrap pin and never compile.
A later coordinator-owned release commit must ship and pin the precompiled Go
bootstrap and pin the semantic engine version, `linux-amd64` target, exact
reviewed source Candidate, canonical asset names, non-empty
bootstrap/Engine/notices SHA-256 values, and the exact
Dolt version/archive/executable SHA-256 under the exact Director and DoltHub
GitHub Release origin/path prefixes. The executable-reported version, mode, source, target, notices digest,
and engine contract must match before the cache is published or product work runs.
Dot-segment traversal that normalizes outside that prefix, another origin/path,
URL credentials, query, or fragment is invalid. Empty-input digests are invalid.
Published descriptors never compile or fall back. The shipped Go bootstrap,
not Node, performs verified downloads and runtime supervision. All content-addressed caches
remain outside the checkout and expose only bounded identity diagnostics.

## Director Home and Board/List runtime

The plugin-owned Go bootstrap starts the exact prepared Engine only after its
managed direct-Dolt store passes bootstrap and maintenance readback. It serves
the host-bound Home plus Board/List contracts on loopback. Users do not invoke
`serve-board`, select an executable, or maintain a service unit.

The Home, Board, and Doctor query endpoints are unauthenticated. Organizer
Preview/Apply and operational mutations require the connector's
server-authenticated human headers. The Repair connector transport carries the
same server identity, and Repair Apply requires it together with the exact
Preview digest and explicit confirmation. Their public contract version/hash are
compatibility checks, not credentials. Run it only on the accepted single-user
Linux host and keep its port confined to loopback; never proxy or expose it to
another host or user. Any local process able to reach the port can read Project
names, Task titles, Run numbers, and Candidate SHAs.

The Go bootstrap generates one opaque persistent public host identity and
stores it owner-only outside the checkout. Authenticated Director RPC
supplies it to the UI; client `hostId` values have no authority. The same
identity binds TaskStore bootstrap, controller adoption, Engine launch, and
every query. Reload/update/remove/reinstall preserve it; a verified identity
change forces one controlled handoff without deleting TaskStore data.

The configuration file and any referenced password files must be absolute,
regular, owner-only files outside repositories. Unknown, duplicate, missing,
oversized, trailing, or unsupported fields fail closed. The closed version 2
shape is:

```json
{
  "schemaVersion": 2,
  "storeId": "director-project-store",
  "authoritySha256": "<bootstrap grant-contract SHA-256>",
  "privilegeFile": "/absolute/private/director-dolt/privileges.db",
  "privilegeFileSha256": "<bootstrap privilege-file SHA-256>",
  "control": {
    "address": "127.0.0.1:3307",
    "database": "director",
    "user": "director_control",
    "principal": "director_control@%",
    "passwordFile": "/absolute/private/dolt-control.password"
  },
  "writer": {
    "address": "127.0.0.1:3307",
    "database": "director",
    "user": "director_writer",
    "principal": "director_writer@%",
    "passwordFile": "/absolute/private/dolt-writer.password"
  },
  "maintenance": {
    "address": "127.0.0.1:3307",
    "database": "director",
    "user": "director_maintenance",
    "principal": "director_maintenance@%",
    "passwordFile": "/absolute/private/dolt-maintenance.password"
  }
}
```

Use the supported `bootstrap-taskstore` command in the production runbook to
initialize the schema, provision and verify three distinct runtime principals,
and atomically install this private configuration without user-run raw SQL.
Runtime password files must be non-empty owner-only inputs. A passwordless
identity is accepted only for the transient owner-local bootstrap connection,
never by `serve-board`. The serving process never guesses, bootstraps, repairs,
or replaces a store; missing identity, authority, schema, or connection facts
fail startup or the
query closed with a bounded error. An unavailable Repair executor disables
Repair and fails closed; it never turns a client Preview into an effect. The
endpoint accepts only the generated
contract version and hash. Home summaries return no SQL, credentials,
repository paths/remotes, objectives, acceptance-criteria text, or raw adapter
output. The authenticated Organizer Preview returns only the exact path and
file/operation metadata selected by that human for Preview/Apply.

The Board/List surface — the Director Console's `Board` tab and the Project
Board workspace panel — loads the snapshot with TanStack Query, performs no
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

The plugin manifest declares exact argv for its locked production dependency
preparation and the subsequent compatibility/integrity verification.
Paseo executes those commands as trusted, unsandboxed daemon-host code with the
daemon user's access during installation and update. Review the source,
lockfile, dependencies, and future updates before installing. Package lifecycle
scripts are disabled; `@getpaseo/client` is installed from the exact lock and is
not vendored. Host modules and external executables are not redistributed.

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
which talks to the Go-controller-owned Dolt 2.3.2 SQL server through distinct
private control, writer, and exact-routine maintenance identities.

`Bootstrap` installs schema version 2 only into an empty, explicitly selected
database and verifies the complete expected table and guard-trigger sets plus
the three exact guard seed rows on every later start; a missing or extra
table/trigger, changed guard row, incomplete or foreign schema, wrong version,
wrong database, or wrong store fails closed. Supported bootstrap also
provisions and attests the exact runtime identities and owner-only Dolt
privilege file without exposing raw SQL. Before each write the adapter sets the
exact-connection session commit value and reads back both the supervised global
and session values,
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
and migration authority remains a separate scoped engine-only control path and must
never be exposed to agents, connectors, UI, repositories, or prompts.

The maintenance identity has only exact `dolt_backup` routine execution. It has
no TaskStore read/DDL/grant authority and cannot call another admin-only Dolt
routine; control and writer cannot invoke backup. No runtime principal receives
global privilege, `ALL`, `SUPER`, user/role administration, or grant option.
The transient bootstrap owner is not retained by production. See
[ADR-0022](docs/adr/0022-least-privilege-taskstore-maintenance.md) and the
[maintenance runbook](docs/taskstore-maintenance.md) for the privilege-file
attestation, fixed restore-database scopes, and fresh-empty recovery rule.

All port-facing connection, query, scan, cursor, replay-entry, and schema
failures are typed as `HealthError` or `SchemaError`. Their bounded codes unwrap
to the stable port sentinels and contain no raw driver, listener, credential,
SQL, table, address, or server output. Every singular and collection reload
validates Project, Task, Run, and Candidate values before returning them.

Command and Event payloads are limited to 64 KiB before and after canonical
JSON normalization. Structural or encoding failures return bounded
`JSON_INVALID`; canonical expansion, including a well-formed numeric magnitude
which cannot fit, returns bounded `PAYLOAD_TOO_LARGE`. This shared
canonicalization is deliberately schema- and secret-agnostic. The owning typed
ingress must reject unsupported schema versions and apply its own secret-safety
contract before calling TaskStore; the generic M1 port does not re-infer either
property from arbitrary JSON. Complete cross-ingress secret-safety hardening
remains assigned to `dir-m5.9`.

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
- [GitHub checks and base invalidation](docs/github-checks.md)
- [Manual and automatic final integration](docs/final-integration.md)
- [Director Workers visibility contract](docs/director-workers.md)

Development is tracked with Beads. Run `bd ready` to inspect unblocked work.
