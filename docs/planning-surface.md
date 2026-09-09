# Contract-first M2 planning presentation

The M2 planning shell is a complete React Native presentation over one
transport-neutral Director Engine contract. It does not implement or claim the
runtime Project, Workspace, Epic, Task, dependency, scheduler, or TaskStore
behavior owned by the remaining M2 Tasks. Configuration inheritance,
revision activation and security-envelope admission live in the standalone Go
engine and are never reproduced by this surface.

## Contract ownership

The canonical schema is
[`engine/ports/planning/planning-surface.v1.json`](../engine/ports/planning/planning-surface.v1.json).
The Go `ports/planning` package validates its closed vocabularies, rejects any
object schema that does not set `additionalProperties: false`, and computes its
duplicate-key-safe canonical SHA-256. The generated
[`planning-contract.shared.ts`](../generated/planning-contract.shared.ts) is the
only planning type and validation source consumed by UI and RPC code. CI
regenerates it and fails on drift.

Every snapshot envelope binds:

- schema version;
- planning contract version and canonical hash;
- a decimal-string monotonic cursor; and
- a closed `PlanningPage` or `TaskDetail` projection.

Identifiers are opaque strings. Versions and cursors are canonical unsigned
64-bit decimal strings in TypeScript, avoiding JavaScript numeric precision
loss. Read models include Project, Workspace, Epic, Task summary and detail,
engine-derived state and explanations, engine-returned allowed actions,
scheduler/capacity facts, and configuration inheritance, effective source, and
preview diff.

Queries accept only the declared Project, Workspace, Epic, derived-state,
priority, label, and search filters, one engine-declared stable sort, a cursor,
and a bounded page size. The client does not filter or sort Task results; the
test-only adapter implements those operations as a deterministic stand-in for
future engine readers. Board lanes only group the already-derived state present
on each returned Task.

Every mutation transports one of the closed Project, Epic, Task, dependency,
configuration Preview/Apply, Launch-now, or audited dependency-override
intents. Its envelope carries `requestId`, `idempotencyKey`,
`expectedVersion`, and nullable approval bindings. Configuration Apply and
dependency override fail schema validation unless both the human approval and
acknowledgement revision are present. The UI can construct an envelope only
from an engine-returned `AllowedAction` ticket with the same intent kind.
The generated binder also proves the ticket target matches the intent target.
An Apply ticket is accepted in a Preview only when it is an exact
`configuration.apply` action for that target and contains both approval and
active-revision acknowledgement bindings.

## Presentation behavior

The Project Board plugin panel now supplies:

- Project switching and virtualized Workspace/Epic navigation;
- Board/List modes over the same query;
- Project, Workspace, Epic, state, priority, label, search, and stable-sort
  inputs;
- cursor paging and bounded `FlatList` rendering;
- accessible Task cards/rows and a host-owned compact-sheet/wide-dialog Task
  detail modal;
- blocker, Needs-you, scheduler, capacity, and allowed-action displays; and
- an Inherit/value configuration editor whose values, effective source,
  preview diff, approval ticket, and Apply affordance all come from the engine
  contract.

Apply is deliberately two-step in the client: selecting the engine-provided
Apply action first displays the exact-Preview confirmation, and a separate
human press submits its unchanged ticket. Editing the draft clears an older
Preview. Neither interaction creates authority or recomputes policy in the UI.

The surface uses Paseo v0.7's stable single `index.ts` entry and documented
`*.client.tsx`, `*.server.ts`, and `*.shared.ts` suffix boundaries. It imports
only host-provided v0.7 client modules, borrows the installation's query client,
uses React Native primitives plus Paseo's `Modal`, takes every color from
`theme.colors`, and switches density/layout from `layout.compact`. It uses no
DOM, browser storage, native-route workaround, direct TaskStore access, raw
Paseo domain call, drag-to-state behavior, or TypeScript lifecycle policy.

## Deliberate runtime boundary

The v0.7 RPCs validate generated input and output schemas on both sides. Their
current connector methods fail explicitly with `PLANNING_SURFACE_NOT_WIRED`.
They do not call fixtures, infer a fallback, or pretend a backend exists. The
runtime implementation and transport wiring remain with `dir-m2.3` through
`dir-m2.8` and must implement this generated contract rather than recreate it.

The deterministic adapter under `tests/fixtures/` is compiled only by tests.
It contains exactly 25 Workspaces, 500 open Tasks, and 10,000 historical Tasks,
validates every request/response with the generated schemas, and never ships as
runtime authority. These fixtures prove bounded presentation and cursor/query
behavior, not the M5 TaskStore scale result deferred by ADR-0016.
