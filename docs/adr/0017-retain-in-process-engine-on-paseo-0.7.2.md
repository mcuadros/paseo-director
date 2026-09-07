# ADR-0017: Retain the in-process engine because Paseo 0.7.2 has no headless connector authority

- **Status:** Accepted
- **Date:** 2026-09-07
- **Beads Task:** `dir-m1.12`
- **Plan gate:** M1 engine process and language boundary before scaffolding
- **Decision owner:** `dir-m1.12` Task Agent under the project-owner spike mandate
- **Amends:** PLAN header decision record only; no PLAN body section changes
- **Preserves:** PLAN §§3.2, 4, 6.1–6.5, 8, 11–15, 19.2–19.4, 20.1,
  21, 23, 25, and 26; [ADR-0002](0002-paseo-0.7.2-public-surface.md),
  [ADR-0003](0003-paseo-0.7.2-lifecycle-recovery.md),
  [ADR-0004](0004-select-direct-dolt-taskstore.md),
  [ADR-0014](0014-practical-linux-agent-boundary.md),
  [ADR-0015](0015-idempotent-command-effect-contract.md), and
  [ADR-0016](0016-defer-taskstore-scale-proof-to-m5.md)

## Context

PLAN §§6.1 and 19.2 select one modular TypeScript plugin-server monolith for
Director 1.0 and postpone a separate engine unless evidence demonstrates a
concrete reliability, performance, or standalone-headless benefit. The project
owner asked this spike to test the standalone-headless case before M1
scaffolding and explicitly accepted a negative result retaining the monolith.

ADR-0002 is the load-bearing host constraint. On exact stable Paseo 0.7.2 the
host owns the authenticated `PaseoApi` connection. Client callbacks and plugin
backend RPC handlers receive an in-process API object; the plugin contribution
entry point does not. The connection belongs to the plugin subprocess and
closes when that subprocess stops. CLI commands, private protocol exports,
internal endpoints, built-in agent MCP, and direct internal-database access are
not replacement engine authority paths.

A standalone engine would therefore need a policy-free Node connector inside
the plugin process. The engine would own every Director decision and durable
effect state; the connector would advertise capabilities, translate only fixed
Director agent-runtime verbs to the injected public API, and return normalized
observations. This spike tested whether that extra hop and its lifecycle are
sufficient on the supported headless topology.

This is architecture research, not Director product implementation. It does
not modify the parked `dir-m1.2` work, weaken an accepted gate, introduce a Go
preference, or authorize a later milestone.

## Question or hypothesis

Can one standalone Director engine outside the Paseo Node plugin process
complete the ADR-0003 Task lifecycle through a policy-free connector while
preserving ADR-0015 intent, evidence, idempotency, interruption recovery, and
ADR-0014 admission, and remain installable through the audited Git plugin
lifecycle without vendoring or silent downloads?

## Acceptance criteria

- Exercise the extra hop at ADR-0015 boundaries T0–T4, including a durable
  intent while the connector mutation is in flight and its response is lost.
- Require Director-language agent-runtime verbs, a capability descriptor,
  immutable command replay, projections, resumable event cursors, generated-
  client drift detection, and failure before mutation when a capability or
  contract is missing.
- Decide whether an engine-created worktree can be adopted as the required
  Execution Workspace or Paseo must create and own it, without bypassing
  ADR-0014 lifecycle admission.
- Demonstrate a bounded first-party engine artifact path through audited Git
  install/update with package install scripts ignored and no vendored or
  silently downloaded binary.
- Decide the TaskStore process mode without weakening ADR-0004 append-only,
  sole-principal, inspection, backup, or safe-commit requirements.
- Exercise engine survival/reconciliation across plugin cleanup/reload and
  cleanup every owned process, path, workspace, branch, plugin, and database.
- Return exactly one architecture outcome with alternatives, consequences,
  compatibility bounds, and every PLAN section amended.

## Evidence

The complete commands, exact versions, public sources, observed failures,
fixture corrections, outputs, and cleanup proof are in
[`docs/evidence/m1.12`](../evidence/m1.12/README.md). The committed
deterministic output is
[`observed-output.json`](../evidence/m1.12/observed-output.json).

The bounded tuple was Debian 13.6 x86-64, Linux
`6.12.107+deb13-amd64`, exact Paseo CLI/server/plugin/client/protocol 0.7.2,
Node.js 26.7.0 while retaining the documented Node 22 floor, npm 11.19.0,
Git 2.47.3, Dolt 2.3.2, Beads 1.2.2 at `6c124203e771`, and Go 1.26.5 only as
inventory. It used one password-protected loopback no-web-UI daemon, one Git
plugin installation, one standalone Node engine, one policy-free Node
connector, and disposable local Git fixtures. It used no paid model, GitHub
mutation, relay, remote MCP, public ref, or live Director TaskStore.

### The extra-hop mechanics pass

The separate-process model used an owned Unix-domain socket with uint32be
length-prefixed JSON frames and a versioned contract hash. The engine, not the
connector, owned command admission, payload conflict, lifecycle admission,
intent, observations, retry class, projection, and event cursor.

One happy path completed eight lifecycle effects: Paseo-managed workspace,
Task Agent with initial prompt, Task-Agent-created helper observation,
independent Reviewer, explicit agent archives, and workspace archive. Six
injected interruption cases covered before intent, after intent, after
observation, after dispatch claim, connector handoff followed by a lost
response, and after outcome receipt before projection. Every recovered case
had one external handoff and one completion. Same-key replay was stable; a
different payload was rejected. Missing capability, contract-hash drift, and
policy in the connector each caused zero handoffs. A stale epoch-1 holder also
failed before observation or handoff after durable takeover advanced the
engine fence to epoch 2; the connector never interpreted a TaskStore epoch.
ADR-0015 T5 admitted a separately linked compensating command, preserved the
original Effect, and reused the same T0–T4 protocol. Cursor replay had no gap
or duplicate, and a temporary generated-client mutation failed drift
comparison.

The tested connector vocabulary was entirely in Director terms:

```text
executionWorkspace.createManaged / observe / archive
taskAgent.createWithInitialPrompt
reviewerAgent.createWithInitialPrompt
helperAgent.observe
agent.observe / archive
```

These results show that the transport and ADR-0015 preservation problem is
solvable. They do not supply a missing Paseo authority object.

### Workspace adoption does not replace managed creation

Public `source.kind=directory` accepted an engine-created Git worktree and
reported `workspaceKind=worktree`, but
`gitRuntime.isPaseoOwnedWorktree=false`; archive left the directory. Public
`source.kind=worktree` reported Paseo ownership, ran admitted setup, and
removed the directory at archive.

PLAN §13.1 continues to require the latter Paseo-managed topology. The engine
must canonicalize and admit ADR-0014's four lifecycle surfaces before it asks
the policy-free connector to call public worktree creation. A directory
workspace is not an implicit lifecycle-admission bypass.

### Distribution and process lifecycle are feasible

The live Git update used one explicit direct-argv preparation command over a
committed first-party engine source. It verified the source SHA-256 and copied
the artifact into a content-addressed engine-owned runtime path. Package
`preinstall`, `install`, and `postinstall` sentinels remained absent. The build
downloaded and vendored zero artifacts. Paseo activated exact fixture commit
`69664251de1187f7809ce38f5bc40ba7c9278ee2` only after preparation and
validation.

The standalone engine PID survived a supported plugin cleanup/reload, observed
the old connector close, and accepted the replacement connector. After plugin
removal it exited on a finite connector-absence lease. The process, socket,
artifact root, managed checkout, source fixtures, workspace directories,
branches, marker, daemon, and entire owned temporary root were then proven
absent.

This validates content-addressed declared preparation, not committed binaries,
vendored dependencies, silent downloads, adjacent-file discovery, or install
scripts. Failed disposable candidates also showed that `import.meta.url` is
not a usable adjacent-artifact locator in the 0.7 server bundle; the prior
installed commit remained selected.

### Direct Dolt remains an external TaskStore process

Accepted ADR-0004/ADR-0012 and installed Dolt 2.3.2 expose the selected store
as an external `dolt sql-server` plus supported SQL/CLI backup operations. No
supported embedded library is admitted. Embedding Dolt in either a Node or Go
engine would change the reviewed schema, trigger, safe-commit, identity,
interruption, and backup boundary and requires new evidence.

The trusted in-process Director engine retains the typed TaskStore adapter and
sole raw credential/SQL identity. An engine-owned external Dolt process retains
append-only database guards and the global/session safe-commit checks before
every write. Online inspection is through typed read-only Director
projections; a second raw external identity would violate ADR-0004. Offline
inspection requires Project pause and proven server absence. Backup remains an
engine-controlled online backup to a unique destination followed by fresh
restore and verification; a live data-directory copy remains invalid.

### The supported headless authority path fails

Exact 0.7.2 typechecking proves:

- `PluginContext` at contribution/startup has no `paseo` member;
- `PluginHandlerContext` has the injected `PaseoApi`;
- the injected API has no client `connect` or `close` lifecycle.

The official v0.7 reference says backend handlers receive `{ paseo }` and that
the owning connection closes with the plugin subprocess. In the no-web-UI live
run, both the initial and reloaded top-level connector reported
`hasPaseoApi=false`. No inbound client RPC existed to produce a handler
context. The engine process survived, but the non-serializable host authority
did not cross the process boundary.

Consequently the standalone engine cannot initiate startup/reload
reconciliation, periodic observation, automatic scheduling, or Organizer MCP
lifecycle effects in headless mode through the required connector. Waiting for
a desktop/web/mobile client to bootstrap a handler makes correctness depend on
client presence and is not headless. Giving the engine a daemon password or
authorization header and creating its own public client bypasses the mandated
injected plugin authority/connector path. Private protocols, CLI orchestration,
agent MCP, and internal storage remain forbidden.

This is a hard missing capability, not an idempotency or transport defect.

## Alternatives considered

### Separate Node engine with a Unix-socket connector

This is the tested process shape. It preserves ADR-0015 across the hop, can use
Director verbs and generated contracts, can survive reload, and can be
distributed through declared content-addressed preparation. It is rejected on
exact 0.7.2 because the headless connector cannot obtain the public host API at
startup or after reload.

### Separate Go engine

Go can produce an independent executable and was available as 1.26.5 on the
test host. It has no advantage over Node at the failing authority boundary: a
Go process also cannot receive the in-process `PaseoApi`. It would additionally
introduce a new toolchain, generated cross-language client, dependency/build
provenance, and TaskStore adapter implementation before any measured
reliability or performance benefit. It is not selected or preferred.

### Give the engine a direct authenticated Paseo SDK client

The public SDK supports URL plus password or authorization-header clients, but
this moves daemon credentials and raw Paseo authority into the standalone
engine and bypasses the required thin plugin connector. It contradicts the
task's ADR-0002 authority premise and was rejected without mutation.

### Bootstrap authority from a connected Paseo app

A UI callback or plugin RPC handler receives `PaseoApi` and could keep it while
that subprocess lives. This makes startup, recovery, scheduling, and MCP work
depend on a desktop/web/mobile client producing an inbound call. It does not
survive plugin reload and is not the PLAN's headless daemon behavior.

### Embed or copy the live Dolt store into the engine

Rejected. No selected public embedded interface has the independently approved
ADR-0004/ADR-0012 semantics. Live directory copying is not a backup, and a
second raw identity violates the trusted TaskStore boundary.

### Retain the modular TypeScript plugin-server monolith

Selected. It preserves the approved language and process boundary and avoids
adding an uncallable connector. Domain/Application and adapter dependencies
remain inward-facing and host-specific behavior remains thin, so a later
supported host boundary can be reconsidered without moving policy into an
adapter.

## Decision

**No-go.** Do not move the Director 1.0 engine outside the Paseo 0.7.2 plugin
subprocess. Retain the modular TypeScript monolith selected by PLAN §§6.1 and
19.2.

The standalone-headless benefit is real but unreachable through the exact
supported host authority contract. Passing transport, distribution,
workspace, TaskStore, and process-lifecycle sub-experiments do not permit the
engine to guess, synthesize, retain across reload, or bypass the missing
`PaseoApi` authority.

No cross-process agent-runtime protocol, Unix socket, generated client,
standalone Node artifact, Go artifact, embedded TaskStore, or direct SDK
credential path is adopted for Director 1.0 by this ADR.

This decision amends no PLAN body section. The sole PLAN edit is the header
amendment link recording that the §19.2 evidence trigger was exercised and its
existing monolith decision retained. In particular, §§3.2, 6.1–6.5, 8,
11–15, 19.2–19.4, 20.1, 21, 23, 25, and 26 remain textually and normatively
unchanged.

## Consequences

- `dir-m1.2` may scaffold only the approved in-plugin modular TypeScript
  monolith after this exact Candidate passes the coordinator-owned independent
  review and delivery gates. This Task Agent does not start or modify it.
- Director 1.0 does not satisfy a claim that its engine is separately usable or
  publishable without Paseo. Documentation must not make that claim.
- Domain, Application, TaskStore, and effect contracts remain independent of
  Paseo types; only the in-process adapter consumes the injected public API.
  This preserves a future connector rewrite without pretending the current
  connector can operate headlessly.
- Engine correctness still cannot rely on plugin memory or cleanup callbacks.
  The monolith reconstructs durable work from TaskStore and public facts when
  an authenticated supported API context exists, following ADR-0003 and
  ADR-0015. Ambiguity still parks.
- The runtime TaskStore remains external Dolt 2.3.2 behind the in-process
  trusted adapter. Append-only, safe-commit, listener identity, backup,
  restore, migration, and external-inspection limits do not change.
- Existing Paseo-managed workspace creation and ADR-0014 admission remain
  mandatory. Engine-created directory-workspace adoption remains unselected.
- A future stable Paseo release may reopen the boundary only after it exposes a
  supported daemon-side startup/reload API context or an equivalently scoped,
  revocable, headless connector capability. That release must rerun the full
  live lifecycle, T0–T4/in-flight fault matrix, provider admission, artifact,
  TaskStore, generated-client, drift, and cleanup evidence. Semver or current
  documentation alone is insufficient.
- No performance, non-Linux, multi-host, provider, or production-scale claim is
  created. ADR-0016 still assigns agreed-scale proof to `dir-m5.10`.

## Compatibility bounds

This No-go is authoritative only for the recorded Debian 13.6 x86-64,
Linux `6.12.107+deb13-amd64`, exact Paseo 0.7.2 public artifact and headless
one-daemon topology. It does not claim that another stable Paseo generation
lacks a suitable startup authority surface. It also does not admit preview
0.8, another OS, multiple daemon hosts, another TaskStore, or a direct engine
credential.

The transport model is bounded to Node 26.7.0 on this Linux host and is not a
production protocol selection. The implementation floor remains Node 22 and
ES2020 until ordinary release validation says otherwise.

## Independent verification

This Candidate is intentionally awaiting the coordinator-owned independent
Opus xhigh review at its exact SHA. This Task Agent does not self-review,
create a Reviewer Agent, push, publish, merge, close the Task, or begin another
Task. Candidate SHA, reviewed base, verdict, checks, delivery, residual risks,
and final cleanup are recorded in Beads by the authorized workflow actors.
