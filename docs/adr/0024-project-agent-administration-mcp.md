# ADR-0024: Give every Project agent a fixed-scope administration MCP

- **Status:** Accepted
- **Date:** 2026-09-14
- **Beads Task:** `dir-m6.21`
- **Decision owner:** Human project owner through the binding HUMAN DECISION on
  `dir-m6.21`
- **Amended by:** [ADR-0026](0026-project-membership-authorization.md) for
  membership-derived admission beside the retained owner-only bearer path and
  for the versioned server-side session binding
- **Amends:** PLAN §§6.4 and 12.2 and the Organizer-only administration portion
  of [ADR-0005](0005-session-scoped-mcp-provider-matrix.md)
- **Preserves:** [ADR-0017](0017-standalone-engine-connector-authority-boundary.md),
  [ADR-0018](0018-deterministic-coordination-boundary.md), Worker/Reviewer
  `director.agent-mcp/v1` least privilege, sole CI, independent Review,
  exact-head integration, owner decisions, and every security/capacity/manual
  confirmation gate

## Context

PLAN §§6.4 and 12.2 describe the original Run-scoped MCP as fixed to one
Project, Task, Run, role, and capability set, give Organizer the planning
command, and say that Task Agents retain only Task/Run tools. That is still the
right contract for a Director-managed Run, but it incorrectly makes the
ability to administer a Project depend on a pre-designated Director role.

The owner decided that every authenticated Paseo agent working in a
Director-managed Project can administer that one Project. The administration
surface must neither widen the Run MCP nor become a generic SDK, cross-Project,
human-RPC, raw-storage, or lifecycle-effect surface.

Exact installed Paseo `0.7.2` evidence also constrains connection lifecycle.
The public client accepts `mcpServers` and `toolPolicy` only in
`agents.create`. The public live-agent handle has refresh, send/run, command,
archive, detach, and subscription operations but no configuration mutation.
The public plugin handler context has only `PaseoApi`; it has no session
configurator or interceptor. Consequently a live session cannot hot-acquire
this MCP through a supported API.

## Decision

Director owns a second contract, `director.project-admin-mcp/v1`. It is
separate from `director.agent-mcp/v1`. Its immutable server-side binding is
exactly one Director Project, native Paseo Workspace, native Paseo Agent,
session identity, and audience. It contains no Task, Run, Candidate, host,
path, or model-selected Project authority. A Task, Epic, Workspace, or Run ID
may occur only as an operation target and is reread for membership in the
bound Project before use. An unknown, ambiguous, or foreign target returns
`MCP_SCOPE_MISMATCH`.

The canonical Project-admin catalog is closed and versioned. It exposes
bounded Project/Organizer/configuration and planning projections; bounded
Run/Worker/Review/queue projections; real planning commands for Projects,
Epics, Tasks, dependencies, and launch; Project pause/resume, Task cancellation,
one bounded Run-reconciliation step; and redacted diagnostics. Mutations enter
the existing Engine planning, execution-control, and reconciliation services
with an authenticated `agent` actor. Current allowed actions, target
membership, expected versions, idempotency, reducers, authoritative readback,
and durable Project-level receipts remain mandatory.

The contract has no Project/host/agent selector, human approval reference,
permission expansion, dependency override, integration, review-verdict,
destructive purge, residual-risk acceptance, or public-history rewrite
operation. It returns no credential, repository/worktree/private path, raw
output, event payload, TaskStore handle, or Dolt primitive. Existing
Worker/Reviewer v1 tools and bindings are unchanged.

The plugin-owned supervisor maintains a dedicated owner-only Project-admin
lifecycle bearer, distinct from its runtime-control bearer. The trusted plugin
server refreshes the command's public native Agent and Workspace facts, creates
a new top-level agent in that exact Workspace with the versioned per-create
MCP and exact tool grants, durably registers the returned native agent identity
against the one Director Workspace mapping, and only then sends the first
prompt. Registration fails if the native Workspace maps to zero or multiple
Director Projects. Session tokens are delivered only to the stdio child
environment and are persisted only as SHA-256 digests. Revocation and command
receipts are Project-level state, not Run receipts.

For a session that already exists, the one supported instruction is:

> Open ‘Create Director administration session’ from this agent. Paseo 0.7.2
> cannot add MCP servers to the current live session, so Director creates a new
> session in the same Project; re-send your request there. Do not restart Paseo.

This public recreation boundary is the exact connection behavior until Paseo
adds a documented live-session MCP configuration operation. Restarting
Director, restarting Paseo, global MCP configuration, forged headers, client
identity inference, current-directory inference, raw protocol calls, and
direct storage writes are not alternatives.

## Consequences

- An ordinary authenticated agent can propose and create a real Task in its
  own Project without becoming Organizer or a Director-launched Worker.
- Same-instance knowledge of another Project's IDs grants no read or mutation
  authority.
- Project administration is broadly discoverable but remains a closed Engine
  command surface; there is still no generic Project-agent administrative SDK.
- Runtime process repair and restart remain plugin-supervisor-owned and are not
  exposed as MCP tools.
- A future Paseo version may replace recreation only after its public live
  session lifecycle is separately evidenced and admitted.

## Verification

Maintained tests cover the exact schema/hash and grant list, absence of scope
selectors and human-only operations, public `agents.create` injection before
the first turn, explicit reconnect behavior, lifecycle authentication and
versioning, two-Project isolation, anonymous/audience/refused identity,
revocation, replay and conflicting replay, expected-version conflicts,
agent-actor gates, every exposed administrative command, real Task creation,
redaction, and input/output limits.
