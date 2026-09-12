# Task modal, Inspector, and native navigation

Director Task detail is an exact-host projection over the engine-owned
`director-planning/v1` contract. Board cards and List rows open Paseo's v0.7
`Modal`; on compact clients the same host primitive becomes a bottom sheet.
Paseo owns the route, modal header and dismissal, panel tabs, Explorer,
navigation, active host, query client, and error boundary. Director owns only
the React Native body.

## Exact binding

Every Task-detail request declares one of two disjoint contexts:

- `board` names one Director Task ID and forbids native agent/workspace IDs;
- `agent` names one native Paseo agent/workspace pair and forbids a caller-
  selected Task ID.

Both include the exact public Paseo host ID. The standalone Director Engine
rejects a different configured host before reading TaskStore state. It either
returns one projection whose response echoes the complete query, or returns a
bounded unavailable reason. It never substitutes a different host, Task, Run,
agent, or Workspace.

An available projection binds the host, Project, Director Workspace, Task and
Task version, latest Run number/version, current Candidate ID/commit when one
exists, and the exact native Paseo Execution Workspace/agent pair. `Open agent`
is enabled only when the persisted Run scope, host-view effect, agent effect,
primary or replacement session, and visible Worker registration agree. Missing,
archived, partial, or contradictory bindings leave the action visible but
disabled with the engine's bounded reason.

The connector sends the generated Zod-validated request to the loopback-only
`POST /v1/planning/task-detail` endpoint. It rejects redirects, oversized
payloads, contract drift, response-origin drift, a different echoed query, and
host/cursor mismatch. It has no retry or resolution policy.

## Details, Execution, and Activity

The modal and Inspector share `TaskDetailView`:

- **Details** presents the objective, acceptance state, dependencies, and only
  configuration entries/actions returned by Director Engine. Configuration
  Preview/Apply keeps the existing exact-ticket and separate confirmation
  contract.
- **Execution** presents the exact Task/Workspace, Run, Candidate, host/native
  agent binding, engine-derived scheduling disposition, runtime-budget facts,
  and bounded Review/feedback summary.
- **Activity** presents at most 100 Task/Run events after the requested global
  cursor. The engine exposes a semantic category, stable code, sequence, and
  humanized summary. It never returns raw payloads, logs, paths, credentials,
  or provider output. The current TaskStore event model has no authoritative
  event timestamp, so `occurredAt` is nullable instead of fabricated.

Loading, empty, initial error, offline, stale-cache, and failed-refresh states
remain distinct. Cached content is marked as last-known; a disconnected host
does not fall through to another installation. Retry only repeats the same
host-bound read.

## Inspector, Command Center, and composer

`Task Inspector` is an agent-context workspace panel available in both the
workspace tab strip and Explorer. It resolves from its native `workspaceId` and
`agentId`; it never feeds either value into Director Workspace filters. An
unbound agent receives an explicit empty Inspector rather than the first Task
from a query page.

The agent-context Command Center contributes `Return to Director Board`, which
opens the plugin's existing Project Board panel through Paseo's contextual
`openPanel`. Director Task Agents also receive a native composer pill through
`addClientSide`/`addComposerPill`. The pill is admitted only when the current
Paseo agent and fixed Director Worker labels agree on the execution workspace,
Task, Run, top-level role, and native identity. Paseo owns its pressable chrome,
pending/error handling, placement, and teardown; pressing it opens the exact
Task Inspector context in Explorer.

## Presentation and accessibility

All UI uses React Native primitives plus Paseo v0.7 `Modal` and `Icon`. Colors
come only from the active `theme.colors`; density and stacking come from
`layout.compact`. Details use the same dense, AO-inspired hierarchy established
by Director Home and Board/List without adding another application header or a
hardcoded visual system. Interactive controls expose native roles, selected or
disabled state, focusability, live-region updates, and a minimum 44-point touch
target. Nothing depends on hover; web Enter/Space activation and host-owned
Escape/back/sheet dismissal remain native Paseo behavior.

The compact bottom-sheet, focus return/trap, large-text reflow, keyboard/touch,
screen-reader, contrast, and reduced-motion checks are shared with Home and
Board/List and specified in
[Mobile and accessibility behavior](mobile-accessibility.md).
