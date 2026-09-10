# Scoped Director agent MCP

The standalone Director Engine owns contract `director.agent-mcp/v1`. Its
canonical schema is
`engine/domain/agentbridge/schemas/director-agent-mcp.v1.json`; the current
canonical SHA-256 is
`b157d836e22470c155327425669c913a2923222527d37e2627b899c3bb929bf6`.
The generated TypeScript transport constants are checked for byte-for-byte
drift in CI.

## Closed role surfaces

| Effective role | Frozen capability | Advertised tool |
|---|---|---|
| Organizer | `project.read` | `director_project_read` |
| Organizer | `planning.command.submit` | `director_planning_command_submit` |
| Worker | `project.read` when configured | `director_project_read` |
| Worker | `task.read` | `director_task_read` |
| Worker | `task.outcome.submit` | `director_task_outcome_submit` |
| Worker | `task.helper.request` when configured | `director_task_helper_request` |
| Helper | `task.read` | `director_task_read` |
| Helper | `helper.contribution.submit` | `director_helper_contribution_submit` |
| Reviewer | `candidate.read` | `director_candidate_read` |
| Reviewer | `review.verdict.submit` | `director_review_verdict_submit` |

The Worker catalog is derived only from its immutable effective role stored in
the Run. A helper inherits the exact Worker provider tuple but receives the
strict helper catalog only after a one-use engine admission binds its native
identity and parent. There is no tool for arbitrary queries, raw TaskStore access, filesystem,
Git, process, credentials, provider selection, lifecycle effects, or another
Run. Unknown tools and fields fail closed.

The Organizer mutation is a bounded update proposal for the Task already fixed
to the session. The Worker mutation accepts exactly the seven frozen
`AgentOutcomeClaim` shapes. The Reviewer mutation accepts one exact-Candidate
verdict with the complete frozen acceptance-criterion set, all eight mandatory
review dimensions, bounded cited P0-P3 findings, and residual-risk codes. These are durable inputs,
not lifecycle evidence or transition authority.

## Session and command binding

Before stdio starts, the engine creates one immutable session binding containing
the Project, Workspace, Task, Run, optional exact Candidate, native agent, turn,
expected aggregate versions, expected Run-state SHA-256, effective-profile
SHA-256, Organizer revision, configuration SHA-256, and fresh provider-discovery
revision. None of these selectors appears in a model-facing tool schema.

Mutations derive their idempotency identity from the fixed Project, bridge
audience, and provider tool-call request identity. The complete canonical
payload includes every scope, role, version, expected-state, profile,
configuration, Candidate, tool, and argument binding. The existing typed
TaskStore command transaction records either `applied` or a replayable
`rejected_version_conflict`. A compact per-Run receipt binds the immutable
Command row without duplicating its model-supplied payload.

Same-key/same-payload replay returns the first durable outcome before checking
the current aggregate version. Same-key/different-payload fails with
`MCP_IDEMPOTENCY_CONFLICT`. A changed Run, Candidate, profile, or expected state
cannot authorize a new call, and recovery never recomputes a wider catalog from
new Organizer configuration or provider discovery.

Requests and responses are each limited to 64 KiB. Duplicate JSON keys,
unknown fields, invalid Unicode, private paths, and secret-shaped values are
rejected before persistence. Read projections omit repository and worktree
paths and replace unsafe text with `[REDACTED]`. Transport and adapter errors
are mapped to closed codes; raw errors are never returned.

## Exact provider preflight

Before a catalog is available, the Go engine rechecks the selected immutable
role against fresh normalized facts for exact Paseo `0.7.2`, exact admitted CLI
version, provider availability, model, effort, mode, permission, non-secret
options, required MCP capabilities, public per-session stdio MCP proof, exact
tool-preapproval proof, and runtime probe proof. Failures use deterministic
codes such as `PASEO_VERSION_UNSUPPORTED`,
`PROVIDER_CLI_VERSION_UNSUPPORTED`, `PROVIDER_UNAVAILABLE`,
`MCP_CAPABILITY_UNSUPPORTED`, `SESSION_STDIO_MCP_UNPROVEN`,
`EXACT_MCP_TOOL_POLICY_UNPROVEN`, and `MCP_RUNTIME_PROBE_UNPROVEN`.
No fallback is selected during this preflight.

The policy-free Paseo connector translates only bounded public v0.7 facts and
an engine-authorized launch descriptor. It produces the documented per-session
`mcpServers` stdio entry and one exact `toolPolicy.preapproved` grant per
engine-authorized tool. It owns no provider selection, workflow decision,
TaskStore access, or lifecycle implementation. Primary agent/worktree lifecycle
and injection dispatch remain with M3.4. Controlled helper admission, fixed
helper scope, and contribution handoff are defined in
[Controlled helper lifecycle](controlled-helpers.md).
