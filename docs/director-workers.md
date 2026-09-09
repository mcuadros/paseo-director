# Director Workers visibility contract

Director launches every Task Agent and Reviewer as a top-level Paseo agent in
its own isolated Execution Workspace. That isolation is deliberate — one
Task's checkout must never be reachable from another's — but on its own it
leaves the owner without a single place to see what is running. The Director
Workers surface closes that gap without weakening the isolation it observes.

## Launch registration is a precondition, not a side effect

The engine freezes a `WorkerRegistration` before it asks Paseo to create the
agent, and the connector refuses the create when the registration is absent,
incomplete, or altered. Visibility is therefore not best-effort telemetry that
may lag or be skipped under load: a worker that cannot be seen is a worker that
does not start.

The registration carries the root workspace, the Execution Workspace, the
Project/Workspace/Task/Run scope, the role, the phase, the base commit, the
optional Candidate commit, and the registration and start instants. Paseo
assigns the native agent ID. Creation carries only the fixed zero-work
bootstrap. After that bootstrap finishes, the engine persists the native agent
ID, workspace ID, and frozen labels. Only then may a separate
`send_agent_prompt` effect start Task or Review work, and that call must set
`notifyOnFinish=true`.

`engine/ports/host/worker_registry.go` owns the label vocabulary and its
validation. The names themselves live in the engine-owned host schema, so
`generated/host-contract.shared.ts` publishes one `WORKER_LABEL` constant that
the RPC and the connector share. Neither side restates a label string.

## The aggregate reads labels, not workspace membership

`loadDirectorWorkers` queries the public Paseo v0.7 agent directory filtered by
the `director.root-workspace` label. This matters for correctness rather than
convenience: an aggregate built by walking an Execution Workspace card would
lose a worker whose workspace was restored, renamed, or archived while the
agent kept running. The label filter is stable across all three, and the live
`workspaceId` is read back from the agent as a Paseo fact rather than trusted
from the caller-supplied label recorded at launch.

Two facts are deliberately never sourced from labels, because a label is
caller-controlled and could be forged by a mis-registered worker:

- **Parentage.** A Director Task Agent or Reviewer is top-level. A registration
  claiming a parent is refused rather than displayed.
- **Archival.** The active query excludes archived agents; an archived agent
  appearing in it is a contract violation, not a row to render.

## Failing closed beats rendering a half-truth

Every inconsistency except one raises `DirectorWorkerContractError`, and the
panel shows "Worker visibility unavailable" instead of a partial list. The one
exception is a worker registered to a different root workspace, which is simply
not this surface's business and is skipped.

Pagination is held to the same standard. A page that reports more results must
return a usable cursor; an absent or empty one is refused rather than treated as
the end of the list, because silently truncating the aggregate would hide a
running worker from its owner — the precise failure this surface exists to
prevent. The read is bounded at 1,000 workers and rejects a duplicate agent ID.

## Liveness is event-driven

The panel subscribes to Paseo agent updates and re-reads the aggregate when the
daemon reports movement. The daemon's terminal callback synchronously enqueues
coordinator reconciliation with an exclusive target below one second and a
durable event-ID deduplication ledger. There is no active-turn refetch interval.
Displayed duration is derived from the registered start instant
at render time, so it never becomes a second, disagreeing source of truth about
whether a worker is alive. Detecting a genuinely stuck worker remains the job of
the PLAN five-minute multi-source stall predicate, which is used only to
recover a lost event and is the only watchdog.

## Layering

The surface spans four boundaries and keeps each one's authority intact:

| Boundary | File | Owns |
| --- | --- | --- |
| Engine | `engine/ports/host/worker_registry.go` | Label vocabulary and launch validation |
| Generated | `generated/host-contract.shared.ts` | The published `WORKER_LABEL` / `WORKER_ROLES` constants |
| RPC | `rpc/workers.shared.ts` | The validated `director.workers` transport schema |
| Connector | `connector/engine-workers.server.ts` | The Paseo directory read and its fail-closed projection |
| UI | `ui/director-workers-panel.client.tsx` | Rendering, ordering, and the Open agent action |

The connector adapter takes an injected `AgentDirectoryReader` port and imports
no SDK type, so the Paseo client keeps its single entry point in
`connector/paseo.server.ts` as ADR-0017 requires. The composition root supplies
the reader over the public agent directory, and `tests/director-workers.test.ts`
holds a compile-time guard that the real Paseo surface still satisfies the port
— which is exactly when the connector needs revisiting if v0.7 drifts.
