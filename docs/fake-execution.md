# Fake execution vertical path

The M1 execution skeleton is a deterministic Director Engine path for tests and
development fixtures. It exercises the approved architecture without launching
a real agent, connecting to Paseo, using a paid provider, or mutating GitHub.
The fake adapter is not selectable through Organizer configuration.

## Path and ownership

One run follows this sequence:

```text
queued Task
→ Eligibility reducer
→ durable Run and uniquely keyed effect intents
→ Director-owned disposable Git worktree
→ connector-contract host-view registration
→ frozen rootless-OCI fixture
→ optional separately approved fake setup observation
→ preparation_ready barrier
→ top-level Task Agent with omitted parent and exact Task title
→ immutable completed AgentOutcomeClaim
→ independent Git Candidate observation
→ durable Candidate admission
→ agent archive
→ host-view archive
→ Director-owned worktree removal
→ fake Run terminal rung
```

The final rung terminates only this M1 fake Run. It is not Task closure and does
not stand in for Validation, independent Review, delivery, integration, or the
coordinator closure gate owned by later milestones.

Director Engine owns the Run, reducers, facts, TaskStore records, worktree, and
cleanup. The fake host adapter implements the one generated engine-owned host
contract and only translates typed calls. It registers a directory view over
the already owned worktree; it never owns or removes that worktree.

## Admission and preparation

Eligibility is a pure `director.reducer.eligibility/v1` reduction over named
facts. It requires current Project/lease/configuration/Task/dependency/capacity/
budget/provider/repository facts, plus all of the following before the first
worktree or host mutation:

- the canonical digest of `worktree.setup`, `worktree.teardown`, every
  `worktree.terminals[*].command`, and
  `worktree.servicePorts.portScript`;
- an exact server-authenticated human approval bound to Project, Workspace,
  Task, Run, and digest when any surface is non-empty;
- the complete ADR-0014 rootless-OCI observation: rootless runtime, read-only
  root, dropped capabilities, `NoNewPrivileges`, private network namespace,
  absent runtime sockets/control tools, owned-worktree-only mount, and fixed
  stdio MCP;
- positive finite policy and fresh measurements for free disk, worktree bytes,
  process count, memory, elapsed time, output, and temporary storage.

The same lifecycle and OCI facts are re-evaluated at the runtime dispatcher;
neither a stored Boolean nor adapter text can bypass admission. Approved setup
is represented by a fake effect only after admission and after the rootless-OCI
fixture is materialized. No repository command text is executed by this test
adapter.

Each Run freezes `director.preparation-plan/v1`: the nine approved preparation
steps, the 1,200-second whole-plan deadline, the 300-second per dependency
command limit, and the 900-second dependency aggregate. The fake path declares
no dependency commands, but `preparation_ready` still binds the exact plan,
eligibility/security, worktree, host view, isolation, tooling, empty-dependency,
and context hashes. The Task Agent intent does not exist before that barrier.

## Durable effects and recovery

Every external effect has a deterministic ID, class-specific finite attempt
limit, durable `intent_recorded`/`dispatching`/`complete` phase, exact binding,
and an immutable observation. A dispatching effect found after interruption is
observed before any repeat. The pure Retry reducer applies unique-create,
idempotent-close, and destructive-terminal rules; a present destructive target
after possible handoff parks instead of being deleted again.

The contract test intentionally loses the response after every fake mutation
and constructs a new Controller between every step. Exact external observations
adopt one worktree, host view, rootless boundary, setup, Task Agent, agent
archive, host-view archive, and worktree removal. The resulting Candidate and
all Run identities reload through the direct-Dolt TaskStore. General process
startup scanning and connector-reload orchestration remain owned by
`dir-m1.9`.

An `AgentOutcomeClaim` is persisted immutably but changes no Candidate state.
The Routing reducer first requires an exact clean, reachable, owned Git commit
which descends from the frozen base and matches the claim. Only the subsequent
TaskStore transaction admits the Candidate.

## Needs you

Eligibility, Launch, Retry, Routing, and Closure reducers can request a typed
escalation. Only `director.reducer.escalation/v1` materializes the `Needs you`
record. Its `cleanupAuthorized` field is always `false`.

Missing, stale, future-dated, malformed, or exceeded operational facts park at
launch or the next active check. Periodic parking leaves the agent, host view,
and Director-owned worktree intact. Unit contracts cover every missing and
one-unit-exceeded resource; the direct-Dolt vertical contract proves both a
missing and an exceeded periodic fact cause no cleanup call.

## Focused verification

The maintained suite runs these contracts through `npm run ci`. During
development, the narrow checks are:

```text
go -C engine test ./domain/execution ./reducer/...
go -C engine test ./cmd/director-engine -run 'TestFakeExecutionVerticalPath|TestLifecycleAndPeriodicFacts'
npm run contract:check
npm run architecture:check
```
