# ADR-0017: Bound the standalone engine connector authority on Paseo 0.7.2

- **Status:** Accepted
- **Date:** 2026-09-07
- **Beads Task:** `dir-m1.12`
- **Plan gate:** M1 engine/process/host boundary before scaffolding
- **Decision owners:** project owner for the frozen architecture and residual-risk
  choice; `dir-m1.12` Task Agent for the scoped evidence result
- **Amends:** PLAN §§1, 3.2, 6.1, 6.2, 6.4, 6.5, 19.2, 19.4, 20.1,
  and 27.2
- **Conditional change:** [ADR-0002](0002-paseo-0.7.2-public-surface.md) only if the
  project owner admits a connector-owned public SDK credential
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
does not make the standalone boundary optional. The remaining question is
whether Director for Paseo may create its own public supported Paseo SDK client
and keep all host credential material on the connector side.

The owner also fixed engine distribution. Release mode downloads a versioned,
digest-pinned Director binary from GitHub Releases. Development mode compiles
the Go engine. Mode selection is explicit and deterministic, and neither mode
falls back to the other. Paseo plugin installation itself is a Git clone; it
does not run dependency installation or install hooks.

This is evidence-driven architecture research, not product implementation.
Candidate `80f5cf1a3344cf3ac9232ba5e10f1de07b5509a8` and rebased carrier
`c44312a4846f7b9bdd2d54a0e931035e108f0dc2` remain superseded unreviewed;
neither is authority or eligible for review or publication.

## Question or hypothesis

On the recorded exact Paseo 0.7.2 Linux tuple, can Director Engine complete the
already-proven Task lifecycle through Director for Paseo while the Node
connector alone owns a public supported SDK connection and acceptably scoped
credential, exposes only fixed engine-owned host capabilities and normalized
observations, leaks no secret or policy to the engine, and starts the
owner-selected release/development artifact fail closed with attributable
identity?

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
taskAgent.createWithInitialPrompt
reviewerAgent.createWithInitialPrompt
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
dependency. Because Git installation is only a clone, a manifest preparation
step ran direct argv `npm ci --ignore-scripts` from the committed lockfile
before activation. This is a declared auditable build step, not a Paseo install
hook, and it does not vendor dependencies.

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

Thus connector ownership and secret isolation are verified, but acceptable
least privilege is not. Adopting this path would amend ADR-0002 from
host-injected authority to a connector-owned broad daemon credential and would
accept a new P2 residual risk. This Task Agent cannot make that human security
decision.

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
fail-closed, and lifecycle mechanics. It is not selected without the project
owner explicitly accepting the P2 risk that a trusted Director for Paseo
connector holds full daemon-operator authority.

### Connector-owned proxy authorization header

The public client accepts one, but Paseo 0.7.2 defines no message-level scope
behind it. It changes credential form without proving least privilege and is
not selected.

### Handler-injected `PaseoApi`

This avoids a separately stored daemon credential, but it exists only during
an inbound handler in exact 0.7.2. It cannot establish headless startup or
reload reconciliation and dies with the old plugin subprocess.

### A later stable Paseo compatibility floor

A stable supported version could resolve the boundary if it exposes either a
headless connector-scoped credential restricted to the required operations or
a startup-injected supported API with equivalent scope. No such version is
assumed. Exact declarations, security semantics, headless startup/reload, and
the focused contract must be evidenced before changing the compatibility floor.

### Direct SDK authority in Director Engine

Rejected. It silently gives the engine a daemon credential, couples standalone
engine code to Paseo, and prevents another host from changing only a connector.

### CLI, MCP, private protocol, or internal storage

Rejected by ADR-0002. These are not equivalent supported host capability paths.

### In-process TypeScript engine

Prohibited by the binding project-owner decision. It is neither selected nor a
fallback under any evidence outcome.

## Decision

**Inconclusive.** The standalone Go engine, separate process, one engine-owned
host interface, Paseo UI host package, and release-download/development-compile
distribution remain mandatory. The scoped connector/compatibility gate cannot
yet admit exact Paseo 0.7.2 because its only reproduced headless public client
authority is a full daemon-operator password, and no project-owner P2 decision
accepts that residual risk. Transport, lifecycle, distribution, interruption,
workspace, and TaskStore mechanics passed and are not the obstacle.

The precise project-owner escalation is one of:

1. Explicitly accept the P2 residual risk that trusted Director for Paseo on
   exact 0.7.2 stores and uses the shared full daemon-operator password,
   constrained to the connector process, sanitized from the engine, and backed
   by the declared locked SDK preparation step; or
2. Select a later stable Paseo compatibility floor only after evidence proves
   a supported headless connector-scoped credential or startup-injected API
   restricted to Director's required host operations.

Until one choice is reviewed and integrated, `dir-m1.2`, `dir-m1.3`, and
`dir-m1.5` remain blocked. Failure of the 0.7.2 credential mechanism may never
reinstate the monolith.

The frozen architecture requires these PLAN amendments when consolidated by
`dir-m1.14`: §1 product identity; §3.2 removal of the Go-sidecar non-goal;
§§6.1 and 6.2 standalone process and module ownership; §6.4 the single host
port; §6.5 engine lease/supervision; §19.2 Go for the engine and TypeScript for
the Paseo host; §19.4 pinned release/dev distribution; §20.1 cross-process
reconciliation; and §27.2 fulfillment of the evidence trigger. The PLAN body
is intentionally not rewritten by this Task.

ADR-0018 remains authoritative for deterministic coordination content. This
ADR refines only its process-permissive sentence: the logical Director Engine
must execute as the separate standalone Go process. Its reducer, fact,
idempotency, review, and no-fat-controller requirements remain unchanged.

## Consequences

- Implementation cannot start while the authority decision is unresolved; the
  accepted passing fixtures do not authorize product scaffolding.
- A later owner choice changes only the connector authority/compatibility
  premise. It does not reopen standalone identity, UI ownership, engine policy
  ownership, the single host port, distribution mode, or mandatory review.
- Independent exact-SHA review remains mandatory even when deterministic checks
  pass and must cover correctness, security, maintainability, readability,
  quality, and rigor.
- The connector becomes a high-trust host adapter if option 1 is accepted. Its
  credential must never enter engine argv, environment, protocol, logs,
  TaskStore, projection, UI, or support bundle.
- New hosts implement the same engine-owned port and generated contract; they
  do not fork engine policy or projections.
- No performance, production-scale, non-Linux, multi-host, remote-engine, or
  future Paseo compatibility claim is made. ADR-0016 still assigns agreed-scale
  TaskStore proof to M5.

## Compatibility bounds

This Inconclusive result is bounded to Debian 13.6 x86-64, Linux
`6.12.107+deb13-amd64`, exact public Paseo 0.7.2 packages and security model,
one same-user password-protected loopback daemon, one Git-installed plugin,
one Node connector, and one local standalone engine. Node 26.7.0 ran the
fixture while the documented implementation floor remains Node 22. Go 1.26.5
proved only linux/amd64 static release and development identities.

It does not admit Paseo preview releases, another stable version, passwordless
production operation, proxy-scoped authority, multi-user daemon isolation,
another OS/architecture, a remote engine, or embedded Dolt. Any compatibility
change requires an explicit reviewed decision and evidence, not semver
inference.

## Independent verification

This Candidate intentionally awaits coordinator-owned independent exact-SHA
review. This Task Agent did not self-review or create a Reviewer Agent and does
not push, publish, merge, close the Task, or start dependent work. Candidate,
base, checks, decision-consistency audit, cleanup, review, delivery, and
residual risk are recorded in Beads by the authorized actors.
