# ADR-0026: Derive Director authorization from project membership

- **Status:** Accepted
- **Date:** 2026-09-15
- **Beads Task:** `dir-m7.1`
- **Decision owner:** Human project owner through the binding decision of
  2026-09-15 recorded in Beads Epic `dir-m7`
- **Amends:** PLAN §12.2 for the source of Project-administration authority;
  [ADR-0005](0005-session-scoped-mcp-provider-matrix.md) for the Run-fixed
  session scope premise and the Run-scoped catalog authority;
  [ADR-0017](0017-standalone-engine-connector-authority-boundary.md) for the
  closed engine-owned host-port capability vocabulary and the normalized
  observations the engine may receive over it; and
  [ADR-0024](0024-project-agent-administration-mcp.md) for the authenticated-agent
  admission premise and the immutable server-side session binding
- **Preserves:** [ADR-0008](0008-director-threat-model.md),
  [ADR-0014](0014-practical-linux-agent-boundary.md),
  [ADR-0018](0018-deterministic-coordination-boundary.md),
  [ADR-0020](0020-zero-work-bootstrap-and-terminal-event-dispatch.md) launch
  ordering, [ADR-0023](0023-plugin-owned-release-runtime.md) handler-scoped
  Paseo authority, [ADR-0025](0025-main-source-testing-and-director-host-identity.md)
  Director-owned host identity, PLAN invariants 7 and 13, Worker/Reviewer
  `director.agent-mcp/v1` least privilege, the closed ADR-0024 administration
  catalog, sole CI, independent Review, and exact-head integration

This ADR is authored as Accepted and becomes authoritative only after
independent review of its exact Candidate and integration with its reviewed
base. It records a decision; it changes no code, schema, contract, or
generated client. The six sibling M7 Tasks implement it.

## Context

The owner requires that an agent's identity before Director derive from living
inside the Project rather than from Director having dispatched it. Any agent
the owner creates inside a Director Project must reach and manipulate Director
without a dispatch record and without holding a pre-shared bearer token.

Both planes close that path today. `director.agent-mcp/v1` is reachable only by
agents Director launches: `ValidateSessionBinding` requires a non-empty Task,
Run and Turn identity, a Candidate identifier and Git OID for the Reviewer
role, an `ExpectedRunStateSHA256`, and the frozen `EffectiveProfilesSHA256`,
`OrganizerRevision`, `ConfigurationSHA256` and `ProviderDiscoveryRevision`
values, and the binding additionally carries four expected aggregate versions
that session authorization reconciles against durable facts. A non-dispatched
member has no value for any of them.
`director.project-admin-mcp/v1` (ADR-0024) admits a caller only when the
plugin-owned supervisor has minted an owner-only lifecycle bearer, registered a
session, and injected both into a new agent at creation.

Two facts already in the engine at base `7695bbae` make this an extension
rather than a rewrite, and both were reconfirmed from source for this decision:

- `projectForNativeWorkspace` in `engine/application/projectadmin/service.go`
  already resolves the Director Project from the caller's native Paseo
  workspace. It scans every Project's Workspaces for
  `NativePaseoWorkspaceID`, and unless it finds exactly one match whose Project
  carries an Organizer in phase Active it fails `MCP_SCOPE_MISMATCH`.
  `RegisterSession` derives the bound `ProjectID` from that resolution, and
  `OpenSession` re-runs the same resolution on every session open and rejects a
  session whose Workspace no longer belongs to the bound Project.
- `forbiddenAuthorityField` in `engine/domain/projectadmin/contract.go` already
  rejects a tool catalog in which any input schema, at any depth, declares a
  `projectId`, `hostId`, `agentId`, `nativeAgentId` or `nativeWorkspaceId`
  property. Combined with `closedSchema`, which requires
  `additionalProperties: false` on every object, a caller can never assert its
  own identity; the server derives it.

Membership is therefore already modelled and already derived safely. It is
merely a scoping step layered beneath a pre-shared bearer token, and the agent
plane has no equivalent derivation at all. The gap is the source of
authorization, not the absence of a membership concept.

## Decision

Director authorization is membership combined with role-at-rest. Dispatch is
demoted from the source of identity to an elevation.

### The membership fact

A native Paseo agent is a member of a Director Project when its native
workspace resolves to exactly one Workspace of exactly one Project whose
Organizer phase is Active. This is exactly what `projectForNativeWorkspace`
computes, and it remains the sole definition.

Membership is derived and never claimed. The engine computes it from live
Paseo topology and durable Project state. It is never read from a tool
argument, a request body, a header the caller controls, a working directory, a
prompt, a model assertion, or an inferred client identity. Zero matches,
several matches, or an Organizer that is not Active is a refusal, never a
prompt to disambiguate and never a caller-supplied tiebreak. The refusal code
stays `MCP_SCOPE_MISMATCH`.

Membership is a live fact, not a credential. `OpenSession` already re-derives
it on every session open instead of trusting what registration recorded; that
behaviour is required rather than incidental and extends to every path this
decision adds. Losing membership ends authority without revoking a secret.

### Authorization as membership and role-at-rest

Authorization becomes a function of two inputs:

- membership, derived by the engine from live Paseo topology; and
- role-at-rest, declared per Project in the Organizer manifest, answering what
  a member may do when Director did not dispatch it.

Role-at-rest is Project configuration. It is activated only through explicit
human Preview/Apply, it may only narrow authority within the ceiling below,
and no model, agent, or configuration proposal may widen it. PLAN invariant 13
governs it unchanged.

### Dispatch as elevation

Dispatch is no longer what establishes that a caller may talk to Director. It
becomes an elevation that adds Task, Run, Candidate and Turn scope together
with the frozen expected-version and digest facts which make Run-scoped
operations reconcilable. The existing `agentbridge.SessionBinding` is exactly
that elevated form and keeps its current meaning.

Demoting dispatch does not make membership a new precondition for it. A
dispatch binding remains sufficient on its own for a Director-launched agent.
This is a correctness requirement rather than a convenience: membership
resolves through `Workspace.NativePaseoWorkspaceID`, which identifies the
native workspace of a Director Workspace — one canonical source repository —
while a Task Agent or Reviewer lives in an isolated Execution Workspace or a
detached disposable checkout that has no such mapping. An implementation that
required membership before honouring a dispatch binding would refuse every
Director-launched Run.

ADR-0020's launch ordering is unchanged: it governs how Director starts an
agent it launches, not who may reach Director.

### The membership authority ceiling

A membership session never inherits Task, Run or Candidate authority. This
ceiling is a hard bound on the model, not a default that role-at-rest may
raise.

- A membership session never receives a capability whose exercise asserts a
  fact about work Director scheduled: `task.outcome.submit`,
  `task.helper.request`, `helper.contribution.submit` and
  `review.verdict.submit`, exposed as `director_task_outcome_submit`,
  `director_task_helper_request`, `director_helper_contribution_submit` and
  `director_review_verdict_submit`.
- A membership session never receives the Run-scoped reads that exist only
  because a dispatch binding fixes a Task, Run, Candidate and Turn:
  `task.read` and `candidate.read`.
- The reason is structural rather than stylistic. The engine can reconcile
  those operations only against the frozen Run facts a dispatch binding
  carries, and a membership session has no `TaskID`, `RunID`, `CandidateID`,
  `CandidateSHA`, `TurnID`, `ExpectedRunVersion` or `ExpectedRunStateSHA256` to
  reconcile against. Synthesising those values would make a claim into
  evidence, which PLAN invariant 7 forbids.
- What remains available to a member on the agent plane is at most
  `project.read` and `planning.command.submit`.
- On the administration plane the closed ADR-0024 catalog is unchanged. The
  ceiling forbids adding Task, Run or Candidate authority to it; it does not
  narrow what ADR-0024 already grants, which remains bounded by role-at-rest.
- A membership session never acquires lifecycle-effect authority, human-only
  confirmation, permission expansion, residual-risk acceptance, or a Project
  selector. Those bounds are unchanged.

### The derived-never-claimed invariant extends to every new path

`forbiddenAuthorityField` keeps its exact forbidden set and applies unchanged
to every schema a membership path introduces, on both planes. The
authenticated native agent and workspace identity used to derive membership is
an observation the engine obtains, never an input the caller supplies. Where
that observation must cross the engine/connector boundary it travels as a
normalized host-port fact under ADR-0017; the connector reports topology and
never decides membership, authority, or policy.

### The owner-only bearer token path is retained

The `DIRECTOR_PROJECT_ADMIN_TOKEN` admission path is retained, not removed.
Membership admission is added beside it. The bearer remains the owner's
out-of-band administration channel for cases where membership cannot be
derived or must be bypassed deliberately, and it keeps its current controls:
the secret reaches only the stdio child environment, the engine stores only a
SHA-256 digest, and `TokenMatches` plus the session state check still gate
`OpenSession`. Removing it would replace one closed path with another and
leave the owner without an administration channel when Paseo topology is
unavailable.

## Frozen-contract breaks

These breaks are the real cost of the decision. They are recorded here so that
no implementation Task discovers them late, and each is resolved rather than
deferred.

### Definition admission is equality, not superset

`agentbridge.ParseDefinition` admits a catalog only when
`len(definition.Tools)` is exactly 8, `definition.Capabilities` is
`slices.Equal` to `expectedCapabilities()`, `definition.Roles` is
`slices.Equal` to the exact quadruple organizer, worker, helper, reviewer, and
`len(definition.ProviderCLIVersions)` is exactly 3. Any added capability, tool,
or role is a hard rejection rather than a compatible extension.
`projectadmin.ParseDefinition` is equally strict: it requires
`len(definition.Tools) == len(exactToolNames)` and an exact ordered name match.

Migration: definition admission becomes versioned superset admission
(`dir-m7.2`). An older client must keep parsing the catalog it knows, and a
newer catalog must not be admitted by an engine that cannot enforce the
capabilities it adds. The contract version and schema hash exchanged before
use, and the CI drift gate on generated clients, both remain mandatory.

### Session bindings are compared by value

`projectadmin.SessionBinding` is a flat comparable struct, and
`RegisterSession` decides idempotency with `persisted.Record.Binding != binding`
and `session == persisted.Record` over the stored event payload. Adding a field
silently changes the comparison against every session persisted before the
change, so a struct change alone would turn replayed registrations into
`MCP_IDEMPOTENCY_CONFLICT`.

Migration: the session binding gains an explicit schema version and persisted
admin sessions are migrated (`dir-m7.3`). `SessionSchemaVersion`, currently
`director.project-admin-mcp-session/v1`, becomes the discriminator rather than
an unchecked constant, comparison becomes version-aware rather than whole-struct
equality, and the existing `MaximumSessions` limit of 128, revocation, and the
event-sourced session records are preserved.

### Every profile digest changes and in-flight Runs are invalidated

`FrozenSet.SHA256()` covers the canonical frozen profile document, which
embeds `ConfigurationSHA256`, the exact active Organizer configuration hash.
Declaring role-at-rest in the Organizer manifest therefore changes
`EffectiveProfilesSHA256` for every Project at the moment of rollout.

The consequence is immediate and must not be misattributed. The digest gate is
in the agent-bridge session authorization, which requires
`profiles.SHA256()` to equal both `run.Execution.EffectiveProfilesSHA256` and
`binding.EffectiveProfilesSHA256`, and also requires `OrganizerRevision` and
`ConfigurationSHA256` to match, failing `MCP_SCOPE_MISMATCH`. Helper admission
compares `Admission.ProfileSHA256` against the Run's
`EffectiveProfilesSHA256`, and primary-session validation compares the same
pair. The preflight code `EFFECTIVE_PROFILE_MISMATCH` is a different gate: it
reports an unadmitted provider CLI version. A rollout that chases the preflight
code will not find the failure.

A second equality bound applies here. The frozen profile document admits
exactly three roles in the exact order organizer, worker, reviewer. A
per-Project role-at-rest declaration may not silently enter that triple; it
must either stay outside the frozen role set or move `FrozenSchemaVersion`.

Migration: rollout must not strand in-flight Runs (`dir-m7.7`). Every Run
launched before rollout carries a frozen digest that no longer matches the
active configuration, so the rollout owns an explicit transition for those Runs
rather than letting their next MCP call fail closed.

### Provider compatibility bounds the rollout

`ParseDefinition` pins exactly three provider CLI versions and preflight
rejects an unadmitted tuple, so a capability the three admitted provider CLIs
cannot carry cannot be rolled out by changing Director alone. ADR-0005's
requirement that a live capability probe, not sent configuration, admits a row
is unchanged.

## Amendments

The exact amended sections are:

- **ADR-0005 §Context**, first paragraph: every governed agent receives a
  session-scoped stdio MCP bridge "fixed to one Project, Task, Run, role, and
  capability set". A membership session is fixed to a Project, role-at-rest and
  capability set, and has no Task or Run.
- **ADR-0005 §Consequences**, second bullet: "The Director MCP server remains
  the catalog authority: it exposes only the tools fixed for the Run scope and
  accepts no caller-selected scope." The catalog authority and the
  no-caller-selected-scope property are preserved exactly; the scope a catalog
  is fixed to is a Run scope under dispatch and a membership scope otherwise.
  ADR-0005's provider matrix, exclusions, fallback rules and defense-in-depth
  bounds are untouched.
- **ADR-0017 §Evidence**, subsection "The engine/connector contract passes":
  the closed engine-owned host-port capability vocabulary. Any capability the
  engine needs to obtain an authenticated native agent and workspace
  observation extends that closed vocabulary through the ordinary
  engine-owned-schema and generated-client process. The five
  channel-independent properties are unchanged.
- **ADR-0017 §Decision**, the paragraph stating that Director Engine "receives
  only the versioned fixed-capability host contract and normalized
  observations": those normalized observations may now carry the caller
  identity evidence membership derivation needs. The same paragraph's rule that
  the connector never receives or decides Director policy is preserved, and the
  accepted P2 `full-daemon-operator` credential bound is unchanged.
- **ADR-0024 §Context**, the premise that "every authenticated Paseo agent
  working in a Director-managed Project can administer that one Project":
  admission is membership, not the possession of a pre-shared bearer.
- **ADR-0024 §Decision**, the paragraph fixing the immutable server-side
  binding to one Project, native Workspace, native Agent, session identity and
  audience: that binding is versioned and records how the session was
  admitted. The paragraph
  describing the plugin-owned supervisor's owner-only Project-admin lifecycle
  bearer is amended only in that the bearer becomes one admission path beside
  membership rather than the sole one; it is retained.
- **ADR-0024 §Consequences**, first bullet: an ordinary *member* agent, not
  merely an authenticated one, can propose and create a real Task in its own
  Project.
- **PLAN §12.2**, the paragraph beginning "Any authenticated Paseo agent in a
  Director-managed Project can acquire the closed typed Project-administration
  MCP": authority derives from membership combined with role-at-rest. The PLAN
  body is intentionally not rewritten by this Task; the implementation Tasks
  carry it.

ADR-0020 is deliberately **not** amended. It governs the order in which
Director launches an agent it dispatches — zero-work bootstrap, persisted
native identity, separately notified real work, synchronous terminal-event
dispatch — which is orthogonal to who may reach Director. Membership changes
the authorization source, not the launch sequence.

## Consequences

- An agent the owner creates inside a Director Project, never dispatched and
  holding no pre-shared token, can read the Project and submit planning
  commands on both planes, bounded by role-at-rest and by the ceiling.
- Same-instance knowledge of another Project's identifiers still grants
  nothing. Membership is derived per caller and refuses ambiguity.
- Losing membership, or an Organizer leaving the Active phase, removes
  authority without a revocation step. Explicit revocation remains available
  and unchanged.
- Rollout invalidates every profile digest at once and therefore has a real
  cost for Runs already in flight. This is why the owner placed the full model
  in M7 with no minimal slice in M6, accepting that the originating use case
  has no entry path until M7.
- The three admitted provider CLI versions bound how fast any new capability
  reaches agents.
- Director still has no generic Project-agent administrative SDK, no
  cross-Project surface, and no path by which a model widens its own authority.

## Verification

The two load-bearing findings were confirmed directly in the source at base
`7695bbae5caa5e81a3e429b7eb9535a15d2e576f` rather than taken from analysis:
`projectForNativeWorkspace` with its exactly-one-match and Active-Organizer
conditions and its use by both `RegisterSession` and `OpenSession`, and
`forbiddenAuthorityField` with its exact forbidden property set and its
enforcement inside `ParseDefinition`. The equality admission in both
`ParseDefinition` implementations, the by-value session-binding comparison, the
frozen-profile role triple, the digest comparison sites, and the retained
`DIRECTOR_PROJECT_ADMIN_TOKEN` path were confirmed the same way.

This ADR records a decision and carries no runtime evidence of its own. The
implementing Tasks own the proof: `dir-m7.2` versioned superset admission,
`dir-m7.3` session-binding versioning and persisted-session migration,
`dir-m7.4` admin-plane membership authorization, `dir-m7.5` agent-plane
membership authorization with an enforced and tested ceiling, `dir-m7.6`
role-at-rest declaration, and `dir-m7.7` digest-invalidation rollout that does
not strand in-flight Runs. Each must prove the derived-never-claimed invariant
on the path it adds, and the ceiling must be enforced by a test that fails when
a membership session is granted Task, Run or Candidate authority.
