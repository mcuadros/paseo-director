# User guide

Director exposes one deterministic path from planning to delivery. The UI
renders Director Engine projections and submits typed commands; cards, model
messages, connector responses, and editable columns never decide lifecycle
state. This guide applies only to Linux with exact stable Paseo `0.7.2`; it
does not infer another `0.7.x` or the `0.8` preview.

## Director Home

Home is bound to the bootstrap-owned persistent Director host identity supplied
through authenticated RPC. A client value cannot replace it. Home shows
Project health, current work, Needs-you counts, Organizer revision, lease, and
the independent Organizer Git and TaskStore sync results. A stale cached view
remains visible but disables actions; Director never falls through to another
host.

Use **Create Project** as described in [Getting started](getting-started.md).
The standard modal selects one native Paseo Project and derives the Director
Project/Workspace identities, paths, and complete configuration before Preview.
It never asks for duplicate Project fields or raw JSON. **Advanced import** is
separately named and accepts only an already configured Organizer. Both paths
require authenticated Apply over the unchanged Preview.

## Planning from an existing agent

Every authenticated Paseo agent inside a Director-managed Project can open
**Create Director administration session**. Exact Paseo `0.7.2` cannot attach
a new MCP server to the current live session, so Director creates a new
top-level session in the same Project with the fixed
`director.project-admin-mcp/v1` catalog. Re-send the planning request there.
Do not restart Paseo.

This administration session can read bounded Project/Organizer/Run/Worker
state and create or update Epics, Tasks, dependencies, and launch requests in
its own Project. It has no Project selector, raw TaskStore or SDK access,
human-only confirmation, Review verdict, integration, destructive purge, or
cross-Project authority. The ordinary Task Agent and Reviewer MCP contracts
remain unchanged.

## Configuration and inheritance

Configuration precedence is `Project → Workspace → Task`. An override is
either **Inherit** or one concrete value, and the UI identifies which scope
supplied the effective value. Overrides cannot exceed the human-approved
security envelope. Expanding repositories, provider/model/permission
authority, budgets, automatic launch, direct delivery, or automatic
integration requires a separate human Preview/Apply.

Configuration changes affect future Runs only. An active Run keeps its exact
Organizer commit, canonical configuration hash, profiles, budgets, skills,
templates, preparation plan, and policy. A model can propose a revision but
cannot activate it.

See [Organizer configuration](configuration.md) and the
[example Organizer](../examples/organizer/README.md) for the complete version 1
shape.

## Board and List

Board and List are two presentations of one snapshot:

- Wide layouts default to a flat Board. Compact/mobile layouts default to an
  Epic-grouped List and show one Board lane at a time.
- Filters cover Project, Workspace, Epic, state, priority, label, attention,
  search, and an engine-declared stable sort.
- **Done history** is an exclusive List filter, not a draggable lane.
- **Needs you** appears only for an actionable human decision. Dependencies
  and capacity waits remain Queued.

The derived states are Needs you, Queued, Building, Validating, In review, and
Ready. Completion is separate Done membership. Moving or reordering a card
cannot change any state. A Candidate alone never proves Validation, Review, or
Ready.

## Task detail and Inspector

Select a card or row to open Task detail. The shared view has three tabs:

- **Details** shows objective, acceptance criteria, dependencies, and effective
  configuration.
- **Execution** shows the exact Workspace, Run, base, Candidate, native agent
  binding, scheduler disposition, budgets, Review, and feedback summary.
- **Activity** shows bounded semantic events without raw payloads, logs,
  paths, credentials, or provider output.

**Open agent** is separate and is enabled only when the persisted Director and
native Paseo bindings agree. In an agent context, Task Inspector resolves the
exact native workspace/agent pair. The composer pill and **Return to Director
Board** action preserve that binding; an unbound agent never opens the first
Task by accident.

## Launch and execution

Manual Projects launch only after **Launch now**. Automatic Projects use the
same eligibility reducer. In both cases Director requires an active Project,
approved Organizer revision, satisfied dependencies, no active Run, current
repository/base identity, capacity, budgets, required credentials, provider facts,
rootless OCI, finite resource observations, and the complete preparation
barrier.

One Run owns one Director-created Task branch and product worktree, one
registered Paseo Execution Workspace view, and one parentless top-level Task
Agent whose visible title is exactly the Task title. The fixed zero-work
bootstrap completes and its identity is stored before the real prompt is sent
with terminal notification.

Optional helpers are internal to that Task Agent. They consume Run/global
capacity and budget, cannot become Task owners or Reviewers, and cannot push,
publish, integrate, clean resources, or close the Task.

At 85% of a time, token, turn, or configured cost limit, Director pauses new
model-consuming work at the next safe boundary and asks for an authenticated
human acknowledgement. At 100%, no new budget-consuming work starts. Usage
and reservations survive recovery.

## Candidate, Validation, and Review

A Task Agent's completed response is only a claim. Director independently
requires a clean, owned, reachable exact Git commit descending from the frozen
base and records its tree, diff, changed paths, and complete binding.

For every admitted Candidate:

1. Director owns at most one draft or ready pull request for pull-request
   delivery.
2. Exactly one authoritative remote Linux CI observation and one independent
   top-level Review are recorded as sibling obligations.
3. The Reviewer uses a detached read-only checkout of the exact Candidate and
   covers acceptance, correctness, security, maintainability, readability,
   design, quality, and rigor.
4. A changed Candidate or relevant base invalidates the previous CI, Review,
   feedback, publication readiness, and integration authority.

A failure does not suppress independent Review. When correction is allowed,
the original Task Agent receives one sorted batch containing all current
Validation, CI, Review, and human-feedback findings. The default limit is
three correction attempts and four total CI cycles. An unchanged or
acknowledgement-only result does not create a new Candidate.

## Delivery

Director supports `pull_request` and `direct`; one never silently falls back to
the other.

Pull-request delivery uses one owned branch and at most one open PR. Manual
integration is the product default. Automatic integration is an explicit
human-approved authority expansion. Immediately before merging, Director
rechecks exact head/base, required checks, Review identity, feedback,
mergeability, and repository identity, then uses an atomic exact-head guard.

Direct delivery uses the same exact Candidate, authoritative CI, independent
Review, feedback, repository, and base gates but creates no PR. Manual direct
delivery waits for a separately authenticated Paseo integration action;
automatic direct delivery requires an explicitly authorized target ref.

## Human feedback

Current feedback can arrive through the bound Paseo Task context or GitHub PR.
Only a GitHub account of type User is treated as human feedback; text never
grants Director authority. Director does not reply as an agent, publish agent
review discussions, or automatically resolve human threads.

Actionable feedback on current work invalidates Ready and joins the next
correction batch. Feedback after verified integration creates one deterministic
sibling Task and does not rewrite completed history.

## Pause, cancellation, and recovery

**Pause Project** stops new launches and lifecycle dispatch at a safe boundary;
an active turn may finish. **Resume Project** first reconciles TaskStore, Git,
Paseo, provider, budgets, and outstanding effects. Completed effects and
possibly delivered prompts are observed rather than repeated.

**Cancel Task** affects only the selected Run. **Emergency stop** requires a
fresh short-lived human confirmation and contains every active Run in stable
order. Neither operation implies that unknown work is safe to delete.

Plugin-scoped reload, update, callback loss, and bootstrap-owned Engine/Dolt
recovery use durable identity and reconciliation. Do not stop or restart the
Paseo daemon or machine as a recovery step. Use **Reconcile now**, Doctor, a
non-destructive Repair Preview, or the exact Needs-you action.

## Cleanup

After verified integration, Director terminates exact owned agents, verifies
recovery, archives host views, removes the owned worktree, and compare-deletes
exact Task refs in that order. Dirty or ignored work is never discarded from a
model claim.

Cancellation defaults to `snapshot_then_delete`; `retain` leaves the
workspace, worktree, and refs. Eligible ignored material is preserved in an
owner-only non-Git artifact within the measured Linux limits. Ambiguous,
changed, over-limit, unsupported, or unowned state remains in place and enters
Needs you without exposing paths or content.

Recovery material and TaskStore backups default to seven-day retention.
Cleanup completion is evidence for the closure reducer; it does not itself
close the Task.
