# Primary Task Agent lifecycle

The production primary lifecycle is an engine-owned reconciliation sequence.
One active Run owns one Task branch, one Director-owned product worktree, one
registered Paseo Execution Workspace view, and one parentless Task Agent. The
Paseo connector performs only the fixed public `director-host/v1` operations;
it does not choose whether a Run launches, retries, parks, or cleans up.

## Frozen Run binding

Before the first external mutation, the Run persists all of the following:

- the exact Project execution-lease holder, process identity, and epoch;
- the Workspace repository ID/key, credential-free canonical remote, source
  and Git-common-directory filesystem identities, exact base commit, Task
  branch, and absolute owned worktree path;
- the immutable m3.2 effective-profile bytes and SHA-256, including the exact
  Worker provider/model/effort/mode/permission/options selection and its
  explicit fallback result;
- one credential-free m3.3 stdio MCP reservation containing the closed Worker
  tool catalog, exact contract hash, provider/model, and server argv; and
- ADR-0014 lifecycle-surface, rootless-OCI, operational-limit, and preparation
  facts.

The repository and primary-session digests are derived from their complete
typed values. Recovery re-derives both instead of trusting a stored Boolean.
The durable MCP server environment rejects credential-, authentication-, raw
control-, Paseo-, GitHub-, and runtime-socket-shaped fields. Provider
authentication remains Paseo/provider-owned and never enters this contract.

## Ordered effects

The launch reducer permits this order only:

```text
Run + worktree intent
→ Director-owned branch/worktree
→ Paseo directory view over that exact worktree
→ mandatory rootless-OCI boundary
→ approved setup, when configured
→ preparation_ready
→ frozen Director Workers registration
→ parentless Task Agent with only the fixed zero-work bootstrap
→ observed native workspace/agent identity and exact published-label digest
→ native identity bound into the scoped session
→ separate real-prompt intent
→ send_agent_prompt with notifyOnFinish=true
```

The agent label set includes the Project/Workspace/Task/Run, root and Execution
Workspace IDs, role/phase/base, agent-create effect, effective-profile digest,
session reservation digest, and registration/start instants. The native agent
ID is never caller-selected. The engine persists it and the observed exact
label digest before recording the real-prompt intent.

## Public Paseo connector

`connector/paseo.server.ts` implements the five primary host effects with the
exact public Paseo 0.7.2 SDK:

- register an existing Director-owned worktree using
  `source.kind=directory` and verify Paseo did not acquire Git-worktree
  ownership;
- reconcile workspaces by native ID or exact directory/title facts;
- reconcile agents by native ID or the complete frozen label set;
- create the top-level Task Agent with the exact Worker profile, scoped stdio
  MCP injection, exact tool preapproval, and zero-work bootstrap; and
- correlate the separate real prompt through its durable native message ID.

Workspace and agent directory reads are complete, bounded, and cursor-checked.
Timeline observation proves whether the exact bootstrap or real message ID was
accepted. Missing, multiple, changed, stale, incomplete, errored, permission,
or unavailable facts are normalized; they never cause the connector to select
a retry or replacement. Concurrent duplicate mutations with one idempotency
key share one in-flight public call, while different payloads fail closed.

## Recovery and interruption

Every external effect has a durable intent, dispatching record, immutable
observation, exact external identity, and observed fact hash. Only the
TaskStore compare-and-swap winner may hand a mutation to an adapter; replay of
the same durable transition is not a second dispatch permit. Ordinary
reconcilers cannot retry a `dispatching` effect. A startup pass must first scan
every Run, prove the current lease and repository binding, reconcile duplicate
external identities, and observe every external frontier before it may
authorize a class-specific retry.

Unique worktree, host-view, boundary, setup, and bootstrap creation adopts one
exact match. The separate real Task prompt is
`nonrepeatable_progress`: after possible handoff, exact timeline/terminal facts
may prove it accepted, but authoritative absence parks rather than resending.
Daemon terminal events retain their durable event-ID deduplication and
synchronous reconciliation-queue contract; the event is a wake signal, never
completion evidence. The five-minute compound watchdog remains the sole lost-
event fallback.

Every lifecycle dispatch repeats the current lease, repository, lifecycle,
isolation, and finite operational admission. Expiry, takeover, repository/path/
branch/base drift, incomplete workspace registration, profile/MCP drift,
missing enforcement, or missing/exceeded telemetry stops before another
mutation and grants no cleanup authority.

Every bootstrap and real Task prompt now consumes a lease-fenced reservation
from the durable Run budget before host dispatch. Exact public provider usage
is applied at the terminal observation; missing or ambiguous usage parks while
preserving the existing effect and worktree. The complete thresholds and
cross-role accounting are documented in [Runtime time and cost budgets](runtime-budgets.md).

Controlled helper creation and handoff are implemented by the separate
[controlled helper lifecycle](controlled-helpers.md). Helper turns use the
same lease-fenced durable budget and evidence-only paused reconciliation as
other model turns. Pause/cancel/emergency controls, Task Agent replacement,
Reviewer behavior, and delivery remain owned by their later M3/M4 Tasks. The
independent m3.10 execution oracle remains test-only; production code does not
import or duplicate it.
