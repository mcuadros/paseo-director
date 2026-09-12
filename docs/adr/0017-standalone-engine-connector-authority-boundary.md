# ADR-0017: Bound the standalone engine connector authority on Paseo 0.7.2

- **Status:** Accepted
- **Amended by:** [ADR-0020](0020-zero-work-bootstrap-and-terminal-event-dispatch.md); project-owner decision `dir-m6.9` comment `01a09682-1168-70ca-841a-4ef6a4aabedb` for deterministic locked npm candidate preparation
- **Date:** 2026-09-07
- **Beads Task:** `dir-m1.12`
- **Plan gate:** M1 engine/process/host boundary before scaffolding
- **Decision owners:** project owner for the frozen architecture and residual-risk
  choice; `dir-m1.12` Task Agent for the scoped evidence result
- **Amends:** PLAN §§1, 3.2, 6.1, 6.2, 6.4, 6.5, 19.2, 19.4, and
  20.1
- **Amends:** [ADR-0002](0002-paseo-0.7.2-public-surface.md) for the
  project-owner-accepted connector-owned daemon credential on exact 0.7.2
- **Refines:** [ADR-0018](0018-deterministic-coordination-boundary.md) by
  requiring its logical Director Engine boundary to be a separate Go process
- **Preserves:** [ADR-0003](0003-paseo-0.7.2-lifecycle-recovery.md),
  [ADR-0004](0004-select-direct-dolt-taskstore.md),
  [ADR-0009](0009-public-licensing-and-distribution.md),
  [ADR-0014](0014-practical-linux-agent-boundary.md),
  [ADR-0015](0015-idempotent-command-effect-contract.md), and
  [ADR-0016](0016-defer-taskstore-scale-proof-to-m5.md)

This ADR may be authored as Accepted, but it becomes authoritative only after
independent review of its exact Candidate and integration with its reviewed
base. The project-owner architecture decisions below are inputs to this ADR,
not alternatives which its evidence outcome may reverse.

## Context

The project owner requires Director Engine to be standalone Go software usable
and publishable without Paseo. Director for Paseo is one host package: it owns
the planned React Native UI, a minimum policy-free connector, and other
Paseo-only integration. The engine owns every domain, application,
orchestration, policy, eligibility, scheduling, retry, escalation, routing,
reconciliation, transition, TaskStore, projection, and closure decision.
Supporting another host adds a connector package without changing engine
logic. Retaining an in-process TypeScript engine is prohibited.

This topology has two separate hops. Paseo fixes the Zod-validated plugin RPC
between a client surface and its Node plugin server. Director specifies only
the same-machine interface from that plugin server to Director Engine. The UI
renders engine projections and submits typed commands; neither the UI nor the
connector derives truth or decides a transition. REST, WebSocket, or another
local channel is an implementation detail beneath that contract.

ADR-0002 found that the top-level Paseo 0.7.2 plugin context has no injected
`PaseoApi`; only an inbound handler receives one. The missing startup authority
does not make the standalone boundary optional. The verified headless mechanism
has Director for Paseo create its own public supported Paseo SDK client and keep
all host credential material on the connector side. The project owner accepted
that mechanism and its bounded full daemon-operator authority.

The owner also fixed engine distribution. Release mode downloads a versioned,
digest-pinned Director binary from GitHub Releases. Development mode compiles
the Go engine. Mode selection is explicit and deterministic, and neither mode
falls back to the other. Paseo plugin installation is a Git clone followed by
the manifest's reviewed direct-argv candidate preparation: the exact locked
production npm closure is installed with package lifecycle scripts disabled,
then compatibility and dependency integrity are verified before Paseo compiles
or loads the candidate. Preparation failure never replaces the prior install.

This is evidence-driven architecture research, not product implementation.
Candidate `80f5cf1a3344cf3ac9232ba5e10f1de07b5509a8` and rebased carrier
`c44312a4846f7b9bdd2d54a0e931035e108f0dc2` remain superseded unreviewed;
Candidate `87a7238fc82d8c52120b11cbe0fbde407d6d668b` is also superseded
unreviewed because it left the P2 choice open. Candidate
`7d6934ca6a36bceb66c901c0feda1464d1ce7877` is superseded after its exact-SHA
review requested correction of a nonexistent PLAN subsection reference. None is
authority or eligible for further review, publication, or integration.

## Question or hypothesis

On the recorded exact Paseo 0.7.2 Linux tuple, can Director Engine complete the
already-proven Task lifecycle through Director for Paseo while the Node
connector alone owns a public supported SDK connection and the
project-owner-accepted credential, exposes only fixed engine-owned host
capabilities and normalized observations, leaks no secret or policy to the
engine, and starts the owner-selected release/development artifact fail closed
with attributable identity?

## Acceptance criteria

- Preserve ADR-0015 at T0–T5, including durable intent while a connector call
  is in flight, immutable idempotent replay, evidence projections, resumable
  cursors, stale fencing, and failure before mutation on contract/capability
  drift.
- Keep exactly one engine-owned host port in Director verbs. The connector may
  translate capabilities, commands, and observations, but may not accept a
  generic Paseo operation or own a workflow rule.
- Show the connector, not the engine, establishing and closing a public
  supported Paseo client at headless startup and after reload; prove the engine
  receives neither the credential nor a raw host API.
- Preserve Paseo-managed Execution Workspace creation and ADR-0014 admission;
  distinguish it from registration of an engine-created directory worktree.
- Keep TaskStore logic and authority in the engine while retaining the
  ADR-0004/ADR-0012 external Dolt process, append-only guards, safe commits,
  inspection, backup, and restore boundaries.
- Prove the selected release digest chain, cache location, explicit modes,
  no-fallback failures, build identity, development-only Go prerequisite, and
  exact-source third-party notices.
- Make the engine survive connector reload and reconcile a replacement
  connector without relying on the best-effort plugin cleanup callback.
- Return one scoped outcome, exact compatibility bounds, alternatives,
  consequences, amended PLAN sections, and owned-resource absence proof.

## Evidence

The complete public sources, exact versions, commands, attempted versus
applied actions, normalized outputs, prior interruption results, and cleanup
proof are in [`docs/evidence/m1.12`](../evidence/m1.12/README.md).

The tuple was Debian 13.6 x86-64, Linux `6.12.107+deb13-amd64`, exact Paseo
CLI/server/plugin/client/protocol 0.7.2, Node.js 26.7.0 while retaining the
documented Node 22 floor, npm 11.19.0, Go 1.26.5 linux/amd64, Git 2.47.3,
Dolt 2.3.2, and Beads 1.2.2 at `6c124203e771`. The focused topology used one
password-protected loopback daemon, one Git-installed plugin fixture, one Node
connector, and one separately surviving engine process. No product repository,
live Director TaskStore, relay, remote MCP, paid model, or GitHub mutation was
used.

### The engine/connector contract passes

Prior bounded evidence already proved a separate engine and policy-free
connector can preserve the Task lifecycle through interruption. The engine
owned command admission, expected versions, idempotency, policy, effects,
observations, projections, compensation, and monotonic cursors. T0–T5 included
the case where the connector persisted a host effect and lost its response;
reconciliation observed and adopted the uniquely correlated result without a
second handoff. Missing capability, stale contract, connector policy, payload
conflict, and stale lease holder all failed before mutation. Generated-client
regeneration matched, and an intentional schema mutation produced CI drift.

The one engine-owned host port has this closed capability vocabulary:

```text
executionWorkspace.createManaged / observe / archive
taskAgent.createWithBootstrap
reviewerAgent.createWithBootstrap
send_agent_prompt
helperAgent.observe
agent.observe / archive
```

The production contract must preserve five channel-independent properties:

1. Commands and queries are distinct. Every mutating command carries an
   idempotency key and expected aggregate version.
2. All query responses are engine-computed projections.
3. Changes have a monotonic cursor and resume strictly after the last accepted
   cursor.
4. The engine owns the schema; host clients are generated and CI rejects drift.
5. Contract version and schema hash are exchanged before use; stale or missing
   capabilities fail closed.

The host port offers no generic method name, raw SDK object, retry choice,
expected-version interpretation, or policy field. An implementation may map
this contract to local HTTP, WebSocket, or Unix-domain IPC without moving
authority. The UI-to-plugin RPC remains Paseo's separate fixed hop.

### Workspace and TaskStore boundaries remain valid

Public `source.kind=directory` can register an engine-created Git worktree, but
Paseo reports it as not Paseo-owned and archive leaves it on disk. Public
`source.kind=worktree` creates the PLAN-required Paseo-owned worktree, runs the
repository lifecycle setup after admission, and removes it on archive.
Director Engine must therefore canonicalize and admit all ADR-0014 lifecycle
surfaces before commanding the connector to create the managed workspace.
Directory registration is not an admission bypass.

Dolt remains a separately supervised `dolt sql-server`; no supported embedded
mode was admitted. Director Engine contains the TaskStore adapter and is the
sole raw credential/SQL identity. Append-only database guards and global and
session safe-commit checks precede every write. Online inspection uses typed
read-only engine projections; offline raw inspection requires Project pause
and proven server absence. Backup remains an engine-controlled online backup
to a unique destination followed by fresh restore verification. Copying a live
data directory remains invalid.

### Connector-owned authority works mechanically but is over-broad

The focused live fixture declared `@getpaseo/client@0.7.2` as a connector-only
dependency. Git candidate preparation now runs direct argv
`npm ci --omit=dev --ignore-scripts --no-audit --no-fund` from the committed
lockfile before activation, followed by a committed exact-host and installed-
closure verifier. This is declared trusted daemon-host preparation; it does not
vendor dependencies or execute package lifecycle scripts. Registry, lock,
integrity, dependency, or verification failure rejects the candidate while
Paseo retains the prior installed commit.

At startup the connector read its own credential file, constructed the public
SDK client, connected headlessly, made one read-only `workspaces.list`, and
then attached to the engine. After supported plugin reload a replacement
connector repeated that sequence while the engine PID survived. The engine
process environment contained no credential key; its protocol events contained
no disposable secret. The connector advertised `credentialScope` as
`full-daemon-operator`, the contract version/hash, and only the eight fixed
capabilities. Removing the credential caused reload to fail before SDK-ready,
engine attachment, or host mutation. Cleanup proved the plugin, daemon,
listener, engine, plugin process, stale setup process, credential, and entire
owned root absent.

Exact 0.7.2 exposes no supported narrower authority. Public
`PaseoClientConfig` accepts a daemon `password` or a complete proxy
`authHeader`; it has no scope, permission, or capability field. The high-level
client reachable with that credential includes agent, workspace, and daemon
configuration APIs. Official security documentation treats connected clients
as trusted operators of the daemon user, and the daemon password protects the
HTTP/WebSocket surface as a whole. Passwordless loopback grants the same
reachability rather than lesser capability. A proxy header controls proxy
admission, not Paseo message-level verbs.

Thus connector ownership and secret non-propagation are verified, while exact
0.7.2 least privilege is unavailable. Under the Task note headed “Human
decision 2026-09-07,” the project owner accepted this P2 residual risk rather
than block M1 on a vendor roadmap without a committed date. This makes the
bounded ADR-0002 amendment effective: on exact 0.7.2 the public headless SDK
authority is a connector-owned full daemon-operator credential, not an
engine-owned credential or a top-level host-injected `PaseoApi`.

The acceptance has these mandatory controls:

1. Credential material lives outside every repository and Paseo-managed plugin
   checkout and is readable only by the Director for Paseo connector process.
2. Credential bytes and location never enter Director Engine argv,
   environment, inherited file descriptors, protocol messages/events, UI,
   TaskStore, projection, support bundle, log, or timeline record.
3. Initial startup and every reload fail closed before SDK-ready, engine
   attachment, or host mutation when the credential is absent or empty.
4. Before accepting a command, the connector advertises
   `credentialScope=full-daemon-operator`, contract version and contract hash,
   and only the fixed capability set. Missing or stale values fail closed.
5. User-facing deployment documentation discloses the daemon-operator
   requirement and these controls before installation.

These are product/deployment requirements, not claims that the research fixture
implemented a hostile same-UID sandbox. The fixture positively verified
connector-only loading, sanitized engine environment/protocol, descriptor
contents, reload behavior, and fail-closed absence. M1 must preserve the
connector-only read boundary when it implements credential provisioning.

### Distribution and lifecycle pass the decided shape

A bounded release fixture proved this chain:

```text
reviewed engine-source Candidate E
  -> release build and exact-E third-party notices
  -> GitHub Release assets B and N
  -> separately reviewed connector commit P pins version, target, E,
     SHA-256(B), and SHA-256(N)
  -> Paseo Git-installs P
  -> explicit release mode downloads and verifies B and N
  -> atomic content-addressed cache outside the plugin checkout
  -> executable reports released identity
```

Separate source and pin commits avoid a self-referential binary hash. The
production release URL is fixed to the Director GitHub Releases repository;
the installed pin chooses only a version, target, source Candidate, and asset
digests. The cache is
`$XDG_CACHE_HOME/director/engines/release/<version>/<target>/<sha256>/`
(using the platform's defined XDG cache default when that variable is absent),
never the Paseo-managed plugin checkout. A partial asset is not executable;
rename into the digest path occurs only after verification.

Release mode requires the literal mode plus the installed pin. Missing asset,
notice, target, version, source identity, or digest fails closed. A binary
digest mismatch produced `ENGINE_DIGEST_MISMATCH`, executed nothing, and
performed zero compilations. Development mode requires the literal development
mode plus the engine source and local Go toolchain. Its compile ran once and
never downloaded a release. A missing mode produced `ENGINE_MODE_REQUIRED`.
Ambient Git state, cache contents, toolchain presence, or network availability
never selects or changes mode.

Every engine start must expose in structured logs, Doctor output, and support
bundles: mode, engine version, exact source Candidate, target, executable
SHA-256, notices SHA-256, connector commit, and contract version/hash. The
fixture distinguished `release/0.0.0-spike` from `development/dev`. Go is a
declared development and release-builder prerequisite, never a release-user
prerequisite. The statically linked release is incomplete unless notices are
generated from exact E, shipped beside it, pinned by P, verified, retained in
cache, and included in support material, satisfying ADR-0009 bound 3.

Engine life is not tied to the connector callback. ADR-0003 observed that
daemon shutdown may skip plugin cleanup. The engine therefore survives plugin
reload; the replacement connector reauthenticates, exchanges descriptor and
cursor, and reconciles durable Commands and Observations. Finite absence
lease/supervision handles connector removal or daemon loss. Plugin cleanup is
an optimization, never proof that the engine should exit or that work ended.

The prior UI inventory found per-client subscriptions and TanStack Query, which
suggests query invalidation rather than direct engine push. No liveness result
was measured here. M1 must measure the Paseo UI refresh path before choosing
invalidation cadence or claiming push latency; that does not change the
engine/connector contract or make a React Native client an engine consumer.

## Alternatives considered

### Connector-owned 0.7.2 daemon password

This is the only fully exercised headless public mechanism. It keeps the
credential and SDK out of the engine and passes startup, reload, capability,
fail-closed, and lifecycle mechanics. Selected after the project owner
explicitly accepted the P2 risk and mandatory controls above.

### Connector-owned proxy authorization header

The public client accepts one, but Paseo 0.7.2 defines no message-level scope
behind it. It changes credential form without proving least privilege and is
not selected.

### Handler-injected `PaseoApi`

This avoids a separately stored daemon credential, but it exists only during
an inbound handler in exact 0.7.2. It cannot establish headless startup or
reload reconciliation and dies with the old plugin subprocess.

### Wait for a later stable Paseo compatibility floor

Rejected as the current path because no vendor delivery date is committed. The
recorded intent is still to narrow authority as soon as a stable supported
version exposes either a headless connector-scoped credential restricted to the
required operations or a startup-injected supported API with equivalent scope.
Exact declarations, security semantics, headless startup/reload, and the
focused contract must be evidenced before changing the compatibility floor;
that change touches connector authority and compatibility only, not engine
logic.

### Direct SDK authority in Director Engine

Rejected. It silently gives the engine a daemon credential, couples standalone
engine code to Paseo, and prevents another host from changing only a connector.

### CLI, MCP, private protocol, or internal storage

Rejected by ADR-0002. These are not equivalent supported host capability paths.

### In-process TypeScript engine

Prohibited by the binding project-owner decision. It is neither selected nor a
fallback under any evidence outcome.

## Decision

**Go.** Exact Paseo 0.7.2 is admitted for Director for Paseo using the public
SDK with the project-owner-accepted full daemon-operator credential, subject to
every mandatory control above. The credential and SDK connection belong only
to the policy-free Node connector. Director Engine remains a separate
standalone Go process and receives only the versioned fixed-capability host
contract and normalized observations. The connector never receives or decides
Director policy.

The accepted P2 choice resolves the connector-authority gate. Waiting for an
unknown later compatibility floor is not the selected path. The project still
intends to narrow authority immediately after Paseo provides and Director
evidences a supported headless connector-scoped credential or equivalently
scoped startup-injected API. Such a change may alter only connector authority
and compatibility bounds. It may not move engine logic into the connector or
reinstate the monolith.

The frozen architecture requires these PLAN amendments when consolidated by
`dir-m1.14`: §1 product identity; §3.2 removal of the Go-sidecar non-goal;
§§6.1 and 6.2 standalone process and module ownership; §6.4 the single host
port; §6.5 engine lease/supervision; §19.2 Go for the engine and TypeScript for
the Paseo host; §19.4 pinned release/dev distribution; §20.1 cross-process
reconciliation. The concrete standalone-headless evidence in this ADR fulfills
the existing §19.2 trigger. Flat §27 remains unchanged: its external-fact
revalidation gate still applies and is not an amended section. The PLAN body is
intentionally not rewritten by this Task.

ADR-0018 remains authoritative for deterministic coordination content. This
ADR refines only its process-permissive sentence: the logical Director Engine
must execute as the separate standalone Go process. Its reducer, fact,
idempotency, review, and no-fat-controller requirements remain unchanged.

## Consequences

- The connector-authority architecture gate is resolved, but this Task Agent
  does not start dependent implementation. Candidate review and integration
  remain coordinator-owned gates.
- The accepted broad credential is an explicit P2 residual risk, not a claim of
  least privilege. Exact-0.7.2 deployment must surface it before installation.
- Independent exact-SHA review remains mandatory even when deterministic checks
  pass and must cover correctness, security, maintainability, readability,
  quality, and rigor.
- The connector is a high-trust host adapter. Its credential must never enter
  engine argv, environment, inherited descriptors, protocol, logs, timeline,
  TaskStore, projection, UI, or support bundle.
- New hosts implement the same engine-owned port and generated contract; they
  do not fork engine policy or projections.
- No performance, production-scale, non-Linux, multi-host, remote-engine, or
  future Paseo compatibility claim is made. ADR-0016 still assigns agreed-scale
  TaskStore proof to M5.

## Compatibility bounds

This Go result is bounded to Debian 13.6 x86-64, Linux
`6.12.107+deb13-amd64`, exact public Paseo 0.7.2 packages and security model,
one same-user password-protected loopback daemon, one Git-installed plugin,
one Node connector, and one local standalone engine. Node 26.7.0 ran the
fixture while the documented implementation floor remains Node 22. Go 1.26.5
proved only linux/amd64 static release and development identities.

It admits the public password-authenticated SDK only through Director for
Paseo under the mandatory controls above. It does not admit Paseo preview
releases, another stable version, passwordless production operation,
proxy-scoped authority, multi-user daemon isolation, another OS/architecture,
a remote engine, or embedded Dolt. Any compatibility or authority change
requires an explicit reviewed decision and evidence, not semver inference.

## Independent verification

Independent review of superseded Candidate `7d6934ca6a36bceb66c901c0feda1464d1ce7877`
against base `66f8b7127543305db0f560fc8106d2d2c0e5b15f` returned
`changes_requested`; stored verdict `01a07a64-a0a6-7493-b9d5-b34a88d2b38a`
verified every human decision and P2 bound and blocked only on the invalid PLAN
subsection reference. This replacement Candidate intentionally awaits a new
coordinator-owned independent exact-SHA review. This Task Agent did not
self-review or create a Reviewer Agent and does not push, publish, merge, close
the Task, or start dependent work. Candidate, base, checks,
decision-consistency audit, cleanup, review, delivery, and residual risk are
recorded in Beads by the authorized actors.
