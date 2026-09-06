# ADR-0010: Launch Task Agents and Reviewers as independent top-level agents

- **Status:** Accepted
- **Date:** 2026-09-06
- **Beads Task:** `dir-m0.15`
- **Plan gate:** M0 governance and Paseo lifecycle
- **Decision owner:** Human project owner
- **Amends:** PLAN §§1, 2, 3, 4, 5, 6, 10, 12, 13, 15, 16, 22, and 23; [ADR-0001](0001-automatic-integration-after-review.md), [ADR-0002](0002-paseo-0.7.2-public-surface.md), and [ADR-0008](0008-director-threat-model.md)

## Context

The approved plan used “primary agent,” “Worker,” and “subagent” without making native Paseo parentage explicit. Because Paseo agent creation accepts an optional parent, that wording could be implemented by having the Organizer or scheduler create Task work through an agent-scoped subagent operation. That would give Task Agents the wrong lifecycle owner, visible hierarchy, replacement semantics, and archive behavior.

The human project owner confirmed that a Task is owned by one normal top-level Paseo Task Agent in an isolated Execution Workspace. The Organizer coordinates Projects but is never that agent's parent. A Task Agent may choose its own internal helpers, while Reviewers must remain independent of both the Organizer and the Task Agent.

This is a governance and SDK-contract clarification. It does not modify historical raw evidence, select an unproven Paseo capability, authorize M1 product work, or weaken worktree, exact-SHA, containment, security, recovery, or cleanup gates.

## Decision

### Director-launched Task Agents

For every active Task Run, Director launches exactly one normal top-level Paseo Task Agent:

1. The scheduler submits a durable launch command to the engine. It never asks the Organizer Agent or another agent to create Task work through agent-scoped subagent orchestration.
2. The engine acts as a top-level Paseo SDK client. It creates the Task Agent in the Task's isolated Execution Workspace and omits the agent-creation parent field rather than supplying an Organizer, scheduler, Task Agent, or synthetic parent ID.
3. The creation request sets the visible agent title to the exact Task title, with no prefix, suffix, run number, key, or status decoration. Durable Task, Run, Workspace, request, and agent IDs provide correlation instead of overloading the title.
4. The engine persists a uniquely keyed creation intent before the SDK call. It persists the returned native Workspace and agent IDs immediately after each creation and before prompting or performing any dependent effect. Recovery reconciles those durable IDs and external facts before retry, so it cannot duplicate an agent or workspace.
5. Several top-level Task Agents may execute concurrently for the same Director Workspace/repository only in distinct Execution Workspaces/worktrees. One active Run still owns at most one active Task Agent, Task branch, and pull request.

A recoverable replacement follows the same top-level, omitted-parent, exact-title, intent/evidence, and workspace-isolation contract. The previous Task Agent must no longer be active, and the replacement receives durable Run facts rather than becoming a child of the previous agent.

### Task-Agent-created helpers

A Task Agent may choose to create helper subagents through an admitted agent-scoped orchestration mechanism when work is complex. Director does not schedule or launch those helpers as Tasks, and they are not Epics, Tasks, Task owners, Runs, Candidates of record, or independent development assignments.

That launcher distinction does not exempt helpers from governance:

- helper creation is fixed to the creating Task Agent's Project/Task/Run scope and cannot select another Workspace or acquire Director lifecycle authority;
- helpers consume the Task's helper quota and time/cost budget and the Project's global agent capacity;
- each helper uses a per-Run idempotency key; its capacity is reserved before creation, and its observed native identity is persisted before helper work, so an unknown result parks instead of being retried blindly;
- helper identities remain observable and reconcilable for interruption, replacement safety, diagnostics, and cleanup;
- writer helpers use separate owned checkouts and return work to the Task Agent; read-only sharing requires proven provider and filesystem safety;
- helpers cannot push, publish, merge, integrate, mutate TaskStore ownership, submit the independent verdict, or replace the Task Agent; and
- Director may authorize, limit, observe, contain, terminate, and clean helper resources without becoming their launcher.

The general mutable Paseo operations that could create arbitrary agents or select another agent/workspace remain unavailable to governed sessions. Only the narrowly scoped helper mechanism is admitted.

### Independent Reviewer Agents

The engine creates every mandatory Reviewer Agent as a normal independent top-level Paseo agent and omits the parent field. A Reviewer Agent is never a child, helper, or continuation of an Organizer or Task Agent. It receives no Task Agent conversation history, uses a detached disposable checkout of the exact Candidate, has read-only review authority plus structured-verdict submission, and consumes global agent capacity without consuming the Task's helper quota.

The engine persists the Reviewer creation intent and native agent/workspace IDs before prompting or review work. A changed Candidate requires a newly bound review; parentage does not replace any exact-SHA gate.

### Director development workflow

Director's own implementation, documentation, maintenance, and M0 Tasks use the same ownership model: one normal top-level Paseo Task Agent in one isolated Execution Workspace/worktree owns the Beads Task. Internal orchestration helpers may assist that Task Agent but do not claim Beads Tasks, become Task records, own branches or Candidates, or satisfy independent review.

Repository references to a “primary agent” or “primary worker” are amended to mean this top-level Task Agent. They must not be interpreted as permitting Task launch through subagent orchestration.

## Alternatives considered

### Make the Organizer the Task Agent parent

Rejected. It couples Task lifecycle and visible hierarchy to the persistent Organizer and makes concurrent Task execution appear to be Organizer-owned internal delegation.

### Have the scheduler launch Tasks through a subagent API

Rejected. The engine is a top-level client, not an agent, and Task records are independent scheduled work. Agent-scoped orchestration is reserved for optional helpers selected by the Task Agent.

### Have Director launch all helpers

Rejected. It would erase the boundary between scheduled Task ownership and a Task Agent's internal decomposition. Director retains policy, accounting, containment, observation, and cleanup authority without becoming the helper launcher.

### Make the Reviewer a child of the Task Agent

Rejected. Parentage would weaken visible and lifecycle independence even if the checkout and prompt were otherwise isolated.

## Consequences

- Agent and workspace adapters must expose top-level creation with an omitted parent and idempotent request correlation.
- The exact Task title is user-facing agent identity; Project/Task/Run correlation remains in durable IDs and labels.
- Scheduler tests must reject any Task launch request carrying a parent or using agent-scoped subagent orchestration.
- Lifecycle tests must cover concurrent Task Agents in separate worktrees, replacement without overlap, independent top-level Reviewers, helper accounting/containment, interruption, reconciliation, and cleanup.
- ADR-0002's observation that the public `0.7.2` creation input accepts a parent remains historical evidence; Director deliberately omits that optional field for Task Agents and Reviewers.
- ADR-0008's engine-effect and authority-separation requirements remain in force, with Task-Agent-created helpers as the narrow governed launcher distinction defined here.
- All unresolved M0 stop conditions remain unresolved. This decision does not authorize product implementation or relax exact-SHA/security gates.

## Independent verification

Pending. Review must evaluate the exact Candidate SHA in a detached disposable checkout and verify that PLAN, every amended ADR, and every affected repository workflow use this model consistently. Publication, merge, Task closure, and cleanup remain post-review gates.
