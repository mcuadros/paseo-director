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

The `DIRECTOR_PROJECT_ADMIN_TOKEN` path is retained, not removed, and it keeps
its current controls: the secret reaches only the stdio child environment, the
engine stores only a SHA-256 digest, and `TokenMatches` plus the session state
check still gate `OpenSession`.

What changes is what the bearer means. Today it is both the channel
authentication and the authority: holding it plus a registered session is what
lets a caller administer the Project. After this decision it authenticates the
channel only, and membership combined with role-at-rest decides what the
authenticated session may do. A session whose membership has lapsed is refused
while its token is still valid. As the connection-acquisition break below
records, on exact Paseo `0.7.2` the bearer cannot be dropped, because it is the
only thing that binds a session to a native identity the engine did not let the
caller assert. It also remains the owner's deliberate out-of-band channel when
Paseo topology is unavailable.

## Frozen-contract breaks

These breaks are the real cost of the decision, recorded here so that no
implementation Task discovers them late. Each is resolved rather than deferred,
with one exception stated as such: connection acquisition and caller
authentication are a bound on exact Paseo `0.7.2` that this decision cannot
resolve, and pretending otherwise would hand the implementing Tasks a
requirement they could only meet by weakening the derived-never-claimed
invariant.

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

### Admin-plane session bindings are compared by value

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

### The agent-plane session binding cannot express a membership session

`ValidateSessionBinding` requires a non-empty `TaskID`, `RunID` and `TurnID`, an
`ExpectedRunStateSHA256`, and the four frozen `EffectiveProfilesSHA256`,
`OrganizerRevision`, `ConfigurationSHA256` and `ProviderDiscoveryRevision`
values, and `SessionSchemaVersion` is a fixed constant with no discriminator. A
membership session has none of those values, so it cannot use that binding at
all. The bound is stricter than the admin plane's: there the binding merely
compares badly, here it cannot be constructed.

Migration (`dir-m7.5`): the agent-plane binding becomes schema-versioned with a
membership form that carries Project, role-at-rest, audience and native agent
identity and omits every Run-scoped field, while the dispatch form keeps exactly
its current required set. The ceiling above is what makes the omission safe: a
binding with no Run scope is also a binding that may not reach any Run-scoped
tool.

### Connection acquisition and caller authentication are not solved by this decision

This is the break that stands between the decision and its first Consequence,
and it is recorded here rather than discovered by an implementation Task.

Every current admission path binds a caller to a native identity through a
secret Director mints and injects when it creates the agent.

- `createProjectAdminMCPInjection` in the connector admits a launch descriptor
  only with an exact seven-argument shape and an `env` containing exactly the
  one key `DIRECTOR_PROJECT_ADMIN_TOKEN` holding a SHA-256-shaped value. It is
  hand-written connector code, so regenerating the contract does not relax it.
- The admin MCP route requires exactly one `Authorization: Bearer` header and
  authenticates it with `TokenMatches` before `OpenSession` derives membership.
  The derivation is trustworthy precisely because the session-to-native-workspace
  mapping it reads was written under that authentication.
- The registration route that writes that mapping is itself gated by the
  separate owner-only lifecycle bearer, so no unauthenticated caller can assert
  a native workspace or agent identity into the system at any point.
- The agent plane authenticates by comparing the caller's session header with
  `run.Execution.PrimarySession.ReservationSHA256` or
  `run.Execution.Review.ReviewerSessionSHA256`, both Director-minted and
  injected at launch.

Nothing in a request carries an authenticated native caller identity, and the
derived-never-claimed invariant forbids letting the caller supply one, because
a same-user process could send any agent's identifier.

On exact Paseo `0.7.2` this is a bound, not a task. ADR-0024 §Context already
records from installed-0.7.2 evidence that `mcpServers` and `toolPolicy` are
accepted only in `agents.create`, that the live-agent handle has no
configuration mutation, and that the plugin handler context has no session
configurator or interceptor. An agent the owner creates directly through Paseo
therefore has no Director MCP server in its session and no supported way to
acquire one, and no supported 0.7.2 surface would give the engine an
authenticated identity for such a caller even if it did. Process-tree,
current-directory and client-identity inference are excluded by ADR-0024 and are
not a security boundary under ADR-0008.

The consequence must be stated plainly rather than engineered around. What M7
can deliver on exact `0.7.2`:

- membership combined with role-at-rest decides what a session may do, so
  holding a valid token stops being authority and losing membership ends
  authority even while the token remains valid;
- dispatch stops conferring identity;
- an owner-created agent reaches Director without any dispatch record, through
  the ADR-0024 session-creation flow the owner triggers.

What M7 cannot deliver on exact `0.7.2`: an agent equipped by nothing Director
did, holding no Director-minted secret, reaching either plane. The "no dispatch
record" half of the Epic's acceptance criterion is reachable; the "no
pre-shared bearer token" half is not, because the secret is what authenticates
the channel, not what grants the authority. This bound belongs to the owner to
accept or to answer, exactly as the provider-CLI compatibility bound does, and
it does not reopen the owner's decision to ship the full model in M7.

Migration (`dir-m7.4` and `dir-m7.5`): both planes move authority to membership
and role-at-rest while retaining their existing channel authentication. Neither
Task may substitute a caller-asserted identity for the missing authenticated
one. The bound lifts only when a future Paseo version exposes an authenticated
session identity to a plugin handler, or a documented live-session MCP
configuration operation, and that evidence is separately admitted the way
ADR-0024 requires for its recreation boundary.

### Newly frozen profile digests change, and the frozen schema version can strand Runs

`FrozenSet.SHA256()` covers the canonical frozen profile document, which embeds
`ConfigurationSHA256`, the exact active Organizer configuration hash.
Declaring role-at-rest in the Organizer manifest therefore changes the digest
of every profile set frozen after rollout.

Runs already in flight are not invalidated by that change, and this ADR
previously recorded the opposite. Every operand of the agent-bridge digest gate
is Run-internal: `immutableScope` compares `facts.run.Execution.EffectiveProfiles`
against `run.Execution.EffectiveProfilesSHA256` and against
`binding.EffectiveProfilesSHA256`, and `currentMCPBinding` re-mints the binding
on every request from the Run's own frozen values, so a binding cannot disagree
with its Run. Helper admission and primary-session validation compare the same
Run-internal pair. No live Organizer value appears anywhere on that path, so a
Run launched before rollout keeps working against the configuration it froze.

The real costs sit elsewhere, and a rollout owns all three.

- A launch command frozen before rollout is refused. `validStart` requires
  `command.EffectiveProfiles.OrganizerRevision()` and `ConfigurationSHA256()` to
  equal the Project's current Organizer values, so every queued launch must be
  re-frozen against the Organizer revision rollout creates.
- Re-freezing is not free. Run-create idempotency replay compares
  `existing.Execution.EffectiveProfilesSHA256` with
  `command.EffectiveProfiles.SHA256()` and returns "existing Run conflicts with
  start command", so a command re-frozen after rollout can no longer replay
  against the Run its earlier form created.
- The genuine stranding path is `FrozenSchemaVersion`. `Run.Execution.EffectiveProfiles`
  is a persisted `FrozenSet`, and `FrozenSet.UnmarshalJSON` restores it through
  `ParseFrozenSet` and `decodeFrozen`, which reject any document whose
  `SchemaVersion` is not the current `FrozenSchemaVersion`. Moving that constant
  makes every persisted Run's stored frozen document unparseable and the Run
  unloadable from TaskStore, which is a far worse failure than a refused MCP
  call.

That last point settles an option this ADR left open. The frozen profile
document admits exactly three roles in the exact order organizer, worker,
reviewer. A per-Project role-at-rest declaration must therefore stay outside the
frozen role triple: moving `FrozenSchemaVersion` is not an admissible
alternative, because it would strand every persisted Run unless the same change
also rewrote every stored frozen document.

Migration (`dir-m7.7`): rollout re-freezes queued launch commands against the
new Organizer revision, retires the request identities whose replay can no
longer match, and does not move `FrozenSchemaVersion`.

### Provider compatibility bounds the rollout

`ParseDefinition` pins exactly three provider CLI versions and preflight
rejects an unadmitted tuple, so a capability the three admitted provider CLIs
cannot carry cannot be rolled out by changing Director alone. ADR-0005's
requirement that a live capability probe, not sent configuration, admits a row
is unchanged.

Two preflight codes are easy to confuse here and neither is a profile-digest
gate. `EFFECTIVE_PROFILE_MISMATCH` fires when the selected provider is not an
admitted provider at all; an admitted provider discovered at the wrong CLI
version fires `PROVIDER_CLI_VERSION_UNSUPPORTED`. The digest comparisons
described above live in agent-bridge session authorization and fail
`MCP_SCOPE_MISMATCH`.

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
  working in a Director-managed Project can administer that one Project": what
  a session may do derives from membership rather than from possessing the
  bearer. The separate ADR-0024 §Context paragraph recording that exact Paseo
  `0.7.2` cannot hot-acquire this MCP in a live session is **preserved
  unchanged**; this decision inherits that bound rather than amending it, which
  is why the connection-acquisition break above is a bound and not a Task.
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

- An agent the owner creates inside a Director Project and never dispatched can
  read the Project and submit planning commands on both planes, with authority
  derived from membership and bounded by role-at-rest and by the ceiling. On
  exact Paseo `0.7.2` it still acquires its session through the Director-mediated
  flow that injects a channel secret; the secret authenticates the channel and
  no longer grants the authority.
- Possession of a valid token stops being authority. A session whose membership
  lapses loses authority while its token is still valid, and an Organizer
  leaving the Active phase has the same effect without a revocation step.
  Explicit revocation remains available and unchanged.
- Same-instance knowledge of another Project's identifiers still grants
  nothing. Membership is derived per caller and refuses ambiguity.
- Rollout costs are real but narrower than an earlier revision of this ADR
  claimed. Runs already in flight keep working. Queued launch commands must be
  re-frozen, re-frozen commands lose their earlier replay identity, and
  `FrozenSchemaVersion` must not move. The owner's decision to ship the full
  model in M7 with no minimal slice in M6 is unchanged by this correction; it
  is the owner's decision and this ADR does not reopen it.
- The three admitted provider CLI versions bound how fast any new capability
  reaches agents, and the absence of an authenticated native caller identity on
  exact `0.7.2` bounds who can reach Director at all. Both are compatibility
  bounds on a fixed Paseo version rather than defects in this model.
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

An earlier revision of this ADR recorded that rollout invalidates in-flight
Runs. That was false and independent review caught it. The correction was
verified in source rather than accepted: the digest gate's operands are all
Run-internal, `currentMCPBinding` re-mints the binding from the Run's own frozen
values, the only live-versus-frozen comparisons are `validStart` and run-create
idempotency replay, and the genuine stranding path is `FrozenSchemaVersion`
through `FrozenSet.UnmarshalJSON`, `ParseFrozenSet` and `decodeFrozen` over the
persisted `Run.Execution.EffectiveProfiles` document. The
connection-acquisition bound was verified the same way, in the connector
descriptor admission, the bearer-authenticated admin route, the
lifecycle-bearer-gated registration route, and the Run-held session digests the
agent plane compares.

This ADR records a decision and carries no runtime evidence of its own. The
implementing Tasks own the proof: `dir-m7.2` versioned superset admission,
`dir-m7.3` admin-plane session-binding versioning and persisted-session
migration, `dir-m7.4` admin-plane membership authorization, `dir-m7.5`
agent-plane membership authorization with an enforced and tested ceiling and the
versioned agent-plane binding, `dir-m7.6` role-at-rest declaration, and
`dir-m7.7` rollout that re-freezes queued launch commands without moving
`FrozenSchemaVersion`. Each must prove the derived-never-claimed invariant on
the path it adds, the ceiling must be enforced by a test that fails when a
membership session is granted Task, Run or Candidate authority, and no Task may
close the connection-acquisition bound by accepting a caller-asserted identity.
