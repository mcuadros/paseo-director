# Director for Paseo — Product and Engineering Plan

- **Status:** Approved
- **Plan version:** 0.6
- **Last updated:** 2026-09-12
- **Approved:** 2026-09-06
- **Amended by:** [ADR-0010](adr/0010-top-level-task-agent-parentage.md) for top-level Task Agent and Reviewer Agent parentage; [ADR-0011](adr/0011-linux-only-platform-scope.md) for the Linux-only `1.0` platform scope; [ADR-0014](adr/0014-practical-linux-agent-boundary.md) for the human-approved practical trusted-provider Linux boundary; [ADR-0016](adr/0016-defer-taskstore-scale-proof-to-m5.md) for deferring TaskStore agreed-scale proof to `dir-m5.10`; [ADR-0017](adr/0017-standalone-engine-connector-authority-boundary.md) for the standalone Go engine and accepted exact-0.7.2 connector authority; [ADR-0018](adr/0018-deterministic-coordination-boundary.md) for deterministic coordination decisions and structured agent outcome claims; [ADR-0019](adr/0019-event-driven-delivery-fast-path.md) for the event-driven single-CI delivery fast path, launch visibility, and adaptive delivery capacity; [ADR-0020](adr/0020-zero-work-bootstrap-and-terminal-event-dispatch.md) for zero-work top-level bootstrap, separately notified real work, and synchronous terminal-event reconciliation; [ADR-0021](adr/0021-recover-exact-leased-ref-cleanup.md) for exact-guarded recovery of nonterminal local and remote Task-ref deletion
- **Human consolidation record:** Beads Task `dir-m1.14`, comment `01a07a8a-65f2-7755-a7af-bf5239fffffc`, for current worktree ownership, host-view, Organizer, UI, and repository-process rules
- **Plugin repository:** <https://github.com/mcuadros/paseo-director>
- **Public name:** Director for Paseo
- **Short UI name:** Director
- **Plugin ID:** `director`

This document consolidates the product, architecture, workflow, quality, and release decisions approved during the design sessions. It is normative: implementation must follow it unless a later Architecture Decision Record (ADR) explicitly changes an approved decision.

This plan is approved. Work may proceed only through the authorization in section 28 and the milestone gates defined below.

## 1. Product intent

Director is standalone Go software for planning, executing, reviewing, and delivering medium-to-large software products that span one or more repositories. It is usable and publishable without Paseo. **Director for Paseo** is its Paseo host package: the public plugin containing the planned React Native UI, the minimum policy-free connector, and other Paseo-only integration. Another host requires a new connector package and no Director Engine logic change. This identity and host boundary are decided by ADR-0017.

The Director Engine provides a durable project-management and execution layer; Director for Paseo exposes that layer through Paseo:

- One Project can coordinate several repositories.
- One Organizer repository holds each Project's approved configuration and durable project material; it is project state, not an agent (ADR-0018 and human consolidation record `01a07a8a-65f2-7755-a7af-bf5239fffffc`).
- A Board/List surface exposes planning and execution state together.
- Director launches exactly one normal top-level Paseo Task Agent for each launched Task—one for its active Run—in an isolated Execution Workspace. That Task Agent may create optional helper subagents.
- Pure, versioned Director Engine reducers govern eligibility, launch, retry, escalation, routing, and closure from durable facts and frozen Project policy; model output is only a structured claim (ADR-0018).
- Every execution is tied to exact Git commits and is recoverable after a plugin or daemon interruption.
- Configuration, skills, templates, decisions, and dynamic task state are durable and auditable.

The design goal is simple operation backed by rigorous invariants. The UI should not expose internal complexity unless the user needs it to make a decision or repair a failure.

## 2. Core terminology

Paseo and Director use some similar terms. The following definitions are authoritative within this plan.

### 2.1 Director Project

The product being managed. It contains one or more canonical source-code repositories and exactly one Organizer repository/configuration (ADR-0018).

A Director Project is not the same abstraction as a native Paseo Project. Native Paseo resources remain the execution substrate.

### 2.2 Workspace

Within Director, a Workspace represents exactly one canonical source-code repository, not a local checkout and not an individual Task.

Several Tasks and agents may work concurrently against the same Workspace through isolated execution checkouts.

### 2.3 Execution Workspace

A Task's isolated execution checkout plus its registered host view. The Director Engine deterministically admits, creates or adopts, owns, recovers, and cleans the product Git worktree. A host connector registers that exact directory and translates the host's workspace observations without acquiring worktree policy or ownership. In Director for Paseo the registered native workspace remains visible through Paseo. ADR-0017 and human consolidation record `01a07a8a-65f2-7755-a7af-bf5239fffffc` decide this Director-owned lifecycle and connector view.

### 2.4 Organizer

Every Director Project owns exactly one dedicated Organizer Git repository and approved configuration revision (ADR-0018). The repository/configuration is not a decision-making agent, and no standing planning agent is required.

The Organizer:

- is not a Task;
- does not represent a product repository;
- is administered through human or model-submitted typed commands which only the Director Engine may reduce and authorize;
- stores configuration and project metadata;
- contains or references the dynamic TaskStore;
- is the stable durable context for the complete Project; any UI or agent conversation remains a non-authoritative command/claim source.

### 2.5 Epic, Task, Run, and Candidate

- **Epic:** an optional grouping of Tasks and the only grouping level.
- **Task:** one unit of work targeting exactly one Workspace.
- **Run:** one launched execution of a Task with frozen configuration.
- **Candidate:** one immutable exact commit SHA produced by a Run.

A correction that changes the commit creates a new Candidate in the same Run. A true relaunch or retry creates a new Run.

### 2.6 Task Agent, helper subagent, and Reviewer Agent

- **Task Agent:** the one normal top-level Paseo agent Director launches to own a Task Run. It has no parent agent, its visible title is the exact Task title, and it runs in that Task's isolated Execution Workspace.
- **Helper subagent:** an optional internal orchestration agent that the Task Agent may create for complex work. It is not a Task, Task owner, Run, or Candidate producer of record and is never launched by the Director scheduler as Task work.
- **Reviewer Agent:** a normal top-level Paseo agent Director launches independently for exact-SHA review in a detached disposable checkout. It is never a child or helper of a planning context or Task Agent.

The Task Agent remains the sole Task owner even when helpers contribute. Several Task Agents may run concurrently against the same Director Workspace/repository only through distinct Execution Workspaces.

Every Task Agent turn ends in one closed `AgentOutcomeClaim`. The claim is never evidence and cannot decide eligibility, launch, retry, escalation, routing, or closure; the Director Engine reconciles it against the facts defined in sections 6.3, 12.3, and 13 (ADR-0018).

## 3. Scope and non-goals

### 3.1 Scope for `1.0`

- Single-user operation.
- Multiple Director Projects on one Paseo daemon.
- One active daemon/executor for each Director Project.
- One or more Git repositories per Project.
- One Organizer repository and approved configuration revision per Project (ADR-0018).
- Epic and Task planning with dependencies.
- Board and List views over the same data.
- Manual and automatic scheduling.
- Worktree-isolated top-level Task Agents and controlled helper subagents.
- Independent commit-based review.
- GitHub pull-request, checks, CI feedback, and human-review integration.
- Direct Git delivery without a pull request.
- Human feedback through Paseo or GitHub.
- Recovery, cleanup, budgets, audit, backups, and diagnostics.
- Desktop, web, and mobile plugin UI.
- Headless Paseo daemon operation.
- Linux host support where Paseo and the required external tools are available.

### 3.2 Explicitly outside `1.0`

- GitHub Projects synchronization.
- Periodic ingestion of GitHub Issues as Tasks.
- Forge-specific integrations other than GitHub.
- Multi-user roles or RBAC.
- Multiple daemons concurrently executing the same Project.
- Automatic migration of a Project between daemon hosts.
- More than two planning levels.
- A second policy engine in any host connector or UI; all coordination policy remains in the standalone Go Director Engine (ADR-0017).
- Replacing or extending native Paseo hierarchy and settings screens.
- Using GitHub pull-request comments as an agent-to-agent message bus.

Generic Git remotes remain valid for direct delivery where the required authentication and operations work. Forge-aware `1.0` behavior is GitHub-specific.

## 4. Non-negotiable invariants

The implementation must make the following states impossible or stop safely when it cannot prove them:

Invariant 7 below is the verbatim decision-and-effect ownership text required by ADR-0018.

1. A Task targets exactly one Workspace.
2. One active Run owns at most one top-level Task Agent, one Task branch, and one active pull request.
3. A review always evaluates an exact committed Candidate SHA.
4. A dirty worktree can never enter review or Ready.
5. Any commit change invalidates previous review and validation for readiness purposes.
6. A relevant base-branch change invalidates Ready and forces revalidation.
7. Only the Director Engine decides Director lifecycle eligibility, launch, retry, escalation, routing, and closure, and only the Director Engine performs Director lifecycle side effects such as top-level Task Agent and Reviewer Agent creation, push, PR creation, merge, integration, and cleanup. A model may submit only a closed structured claim; a claim is never evidence and never authorizes a lifecycle state transition. Before any Task, Run, Candidate, Validation, Review, delivery, integration, or cleanup transition, the Director Engine reconciles the decision against named durable and external facts. A Task Agent may request and, only after deterministic Director Engine admission, invoke the existing helper-creation exception within its frozen Run policy; it does not decide admission, capacity, or lifecycle state. Helpers cannot perform Director lifecycle effects or become Task owners.
8. Every side effect is idempotent and reconciled against external facts before retry.
9. No Task Agent, Reviewer Agent, helper subagent, PR, merge, prompt, or workspace is duplicated after recovery.
10. Unknown or unowned branches, worktrees, refs, and directories are never deleted.
11. Dirty or unintegrated work is never destroyed without the configured recovery action.
12. A model, delivery, permission, or provider fallback is never chosen silently.
13. No model or Organizer configuration proposal can expand model, effort, permission, repository, delivery, or security authority; only explicit human Preview/Apply can do so (ADR-0018).
14. Secrets never enter Organizer Git, TaskStore records, prompts, audit payloads, or support bundles.
15. Project state is derived from durable facts rather than editable Kanban columns.

Violation of any invariant affecting data, security, isolation, or integration is a release-blocking defect.

## 5. Domain model

### 5.1 Aggregate relationship

ADR-0018 defines Organizer here as the repository/configuration aggregate rather than an agent.

```text
Project
├── Organizer repository/configuration (exactly one)
├── Workspace (one or more)
├── Epic (zero or more)
│   └── Task (one or more)
└── Task without Epic (zero or more)
    └── Run (zero or more)
        └── Candidate (zero or more)
```

Review, Validation, Feedback, Delivery, Integration, Command, Event, and AuditEntry are immutable supporting records. They do not add planning levels.

### 5.2 Project

A Project contains at least:

- a stable global ID and human-readable name;
- Organizer repository identity;
- the active Organizer configuration revision;
- operational state;
- default launch, execution, review, delivery, cleanup, budget, and concurrency policies;
- references to its Workspaces;
- creation, update, and audit fields.

Operational states are:

- `active`;
- `paused`;
- `degraded`;
- `archived`.

The product is single-user and does not introduce RBAC. Audit records preserve the server-derived human, native agent/role, connector, or system actor identity separately from command identity; an agent cannot write as the human or turn narration into approval (ADR-0015 and ADR-0018).

### 5.3 Workspace

Each Workspace contains:

- a stable ID and display name;
- canonical Git remote identity;
- the absolute source-checkout path on the Project daemon;
- the default base branch;
- optional policy overrides within the Project envelope;
- health and preflight facts.

The same Director Project remains bound to one daemon during `1.0`. Remote Paseo clients never interpret these paths; the daemon does.

### 5.4 Epic

An Epic contains:

- a stable ID and human-readable key;
- title and description;
- optional priority and labels;
- zero or more child Tasks;
- dependencies on other Epics;
- derived progress and completion.

Epic dependencies are hard dependencies. An incomplete blocking Epic blocks every Task in the dependent Epic unless a human applies an explicit, audited override to a specific Task.

### 5.5 Task

A Task contains:

- a stable internal ID and human-readable key such as `DIR-42`;
- title and objective;
- explicit acceptance criteria;
- exactly one Workspace ID;
- an optional Epic ID;
- dependencies on Tasks or Epics;
- priority: `urgent`, `high`, `normal`, or `low`;
- labels and links to context/specifications;
- a launch-policy override;
- permitted execution overrides;
- external references such as a future GitHub Issue;
- lifecycle and audit metadata.

`normal` is the default priority. External IDs never replace Director's canonical identity.

Dependencies must be acyclic. Dates are stored in UTC. Deletion is logical except for confirmed cleanup operations over owned ephemeral resources.

### 5.6 Run

A Run contains:

- Task and Workspace identities;
- a monotonically increasing Task run number;
- frozen effective configuration;
- the Organizer commit, frozen claim schemas, versioned PreparationPlan, and hashes of every skill/template used (ADR-0018);
- the base ref and resolved base SHA;
- Task Agent and Execution Workspace identity;
- configured budgets and current consumption;
- outstanding reservations, soft-budget warning/acknowledgement state, and hard-limit state (ADR-0018);
- the current Candidate;
- correction-cycle counters;
- delivery state;
- timestamps and terminal outcome.

Corrections requested by review, CI, or pre-Ready human feedback remain in the same Run and are performed by the original Task Agent while it remains usable.

A manual Retry, restart after terminal failure, or explicit new execution creates a new Run.

### 5.7 Candidate

A Candidate is immutable and contains:

- the exact commit SHA;
- parent/base facts;
- sequence within the Run;
- producing agent and timestamp;
- associated Review records;
- associated Validation/CI records;
- publication and delivery facts.

Only the current Candidate can reach Ready. Older Candidates remain historical evidence.

### 5.8 Concurrency control

Mutable aggregates carry a version. Commands include the expected version where relevant. A conflicting write fails explicitly and is reconciled; it never overwrites silently.

## 6. Technical architecture

### 6.1 Architecture style

`1.0` has one standalone Go Director Engine and host-specific packages. This process and ownership boundary is mandatory under ADR-0017; it is not a generic microservice split.

```text
Paseo desktop / web / mobile clients
                  │ typed plugin RPC
                  ▼
┌──────────────────────────────────────────────────────┐
│ Director for Paseo — TypeScript host package         │
│ React Native UI · RPC · minimum policy-free connector│
│ public Paseo SDK client · Paseo-only integration     │
└──────────────────────┬───────────────────────────────┘
                       │ engine-owned versioned host contract
                       ▼
┌──────────────────────────────────────────────────────┐
│ Director Engine — standalone Go process              │
│ domain reducers · application commands · scheduler   │
│ reconciliation · TaskStore · projections · cleanup   │
│ Git/GitHub/process/clock adapters · host ports       │
└───────────────┬───────────────────────┬──────────────┘
                │                       │ fixed-scope stdio MCP
      Organizer Git + TaskStore         └──── agent providers
```

The connector-to-engine channel is same-host REST, WebSocket, Unix-domain IPC, or an equivalent local transport chosen by implementation. It is not a public server and cannot move policy, truth, reducers, or projections into the connector. Director Engine survives connector reload and has its own attributable process identity (ADR-0017).

### 6.2 Module boundaries

- **Director Engine domain:** pure, schema-versioned entities, invariants, facts, state projections, policies, and the six decision reducers. It imports no Paseo or host package (ADR-0017 and ADR-0018).
- **Director Engine application:** typed Commands, queries, use cases, orchestration, PreparationPlans, effect transactions, and TaskStore authority.
- **Director Engine ports/adapters:** one engine-owned host interface plus Git, GitHub, external Dolt, filesystem, clock, disk, and process adapters. Adapters observe or perform one authorized effect and own no policy.
- **Director for Paseo client:** React Native surfaces and panels which render engine projections and submit typed commands.
- **Director for Paseo server/connector:** Paseo RPC handlers, a generated client for the engine-owned contract, public SDK connection, host capability translation, and normalized observations. It contains no domain, application, orchestration, eligibility, scheduling, retry, escalation, routing, reconciliation, state-transition, TaskStore, projection, or closure logic (ADR-0017).
- **Agent runtime:** a fixed-scope stdio MCP façade for Task Agent, helper, and Reviewer roles. It accepts bounded claims and commands but has no independent workflow policy (ADR-0018).

Dependencies point toward Director Engine contracts. A new host implements the same fixed port and generated contract without changing engine logic. Contract version and schema hash are exchanged before use; generated-client drift fails CI and runtime drift fails closed (ADR-0017).

### 6.3 Decision and effect ownership

The UI, host connectors, and MCP endpoints submit typed commands, observations, and claims. Only the Director Engine reduces them into lifecycle state and executes effects. Under ADR-0018, a decision reducer is a pure, versioned mapping from a closed fact set and frozen policy to exactly one permitted command, projection, wait, or refusal. Missing, stale, ambiguous, unavailable, contradictory, or out-of-window facts cause a wait, refusal, or fail-closed park; a model never fills the gap.

The six reducers are:

| Decision | Required facts and deterministic result |
|---|---|
| Eligibility | Project lease/state, approved Organizer revision, complete versioned Task, dependency graph, no active Run, frozen policy, and fresh security/capacity/disk/time/cost/CI/provider facts. All predicates must pass; dependencies wait in Queued, external unavailability backs off, and human-remediable or safety ambiguity routes to Needs you. |
| Launch | Current eligibility, reserved capacity/budget, immutable Run/configuration, exact base/repository, `preparation_ready`, and unique worktree/agent intents. Launch once; stale or consumed facts are re-observed. |
| Retry | Effect class, possible-handoff state, fresh effect observation, unchanged binding, prior-dispatcher absence where required, exact compare predicate, and every remaining attempt/correction/replacement/CI/time/cost/resource budget. Retry only when ADR-0015 permits it. |
| Escalation | One typed unresolved human question, reconciled access/configuration failure, hard-budget exhaustion, terminal drift, unsafe identity/ownership, compound liveness result, or another existing fail-closed predicate. Emit one typed Needs-you record with its exact wake/decision condition. |
| Routing | Current versions, frozen workflow, validated claim kind, outcome facts, Validation/Review, feedback, and delivery facts. Select exactly one next phase, wait, correction, review, delivery, or Needs-you cause. |
| Closure | Current acceptance/terminal rung, exact Candidate/base, clean worktree, current Validation and independent Review, required CI/feedback/publication/integration/deployment, cleanup/ownership facts, and no blocker. Append one audited closure only when every fact is current and linked. |

A Task Agent's optional creation of helper subagents is the only agent-creation exception: the Director Engine authorizes the frozen Run envelope and one-use admission, reserves and reconciles capacity, observes helper identities, and owns containment and cleanup, but does not launch helpers as Tasks. The Task Agent does not decide admission or lifecycle state (ADR-0018).

Each effect follows an intent/evidence pattern:

1. Validate policy and expected aggregate version.
2. Persist a uniquely keyed intent.
3. Inspect current external facts.
4. Perform the effect only when absent.
5. Persist the observed result.
6. Project the new state.

This applies to top-level Task Agent and Reviewer Agent creation, prompting, Director-owned product-worktree creation/registration, branch creation, push, PR creation, merge/integration, remote-branch deletion, and local cleanup. Helper creation must use an admitted agent-scoped mechanism that binds every helper to the creating Task Agent's fixed Project/Task/Run scope, reserves capacity under a per-Run idempotency key, and records the observed helper identity before helper work so accounting and recovery never rely on a blind retry (ADR-0015 and ADR-0018).

### 6.4 Engine-owned host and agent interfaces

- Every Paseo call crosses one engine-owned host interface implemented by the Director for Paseo connector. Its closed exact-0.7.2 capability vocabulary is `executionWorkspace.createManaged`, `executionWorkspace.observe`, `executionWorkspace.archive`, `taskAgent.createWithBootstrap`, `reviewerAgent.createWithBootstrap`, `send_agent_prompt`, `helperAgent.observe`, `agent.observe`, and `agent.archive` (ADR-0017 and ADR-0020). The capability name `executionWorkspace.createManaged` is fixed contract vocabulary, not an ownership claim: for an already admitted Director-owned product worktree it requests registration of a Paseo host view over that exact directory. The connector implementation fixes only an exact-0.7.2 source kind evidenced to preserve Director ownership. Neither the capability name nor host-view registration transfers Git worktree lifecycle or cleanup ownership to the connector (human consolidation record `01a07a8a-65f2-7755-a7af-bf5239fffffc`).
- The connector exposes no generic Paseo operation, raw SDK object, expected-version interpretation, retry choice, or policy field. It advertises only that fixed capability set, the contract version/hash, and its credential scope; missing or stale values fail before mutation (ADR-0017).
- Commands and queries are separate, mutating commands carry an idempotency key and expected version, reads are engine projections, and changes use a monotonic resumable cursor. The engine owns schemas and generated clients (ADR-0017).
- Agents receive a session-scoped custom Director MCP in addition to only those applicable built-in Paseo capabilities admitted by the frozen provider policy.
- Support is capability-detected. Director admits only the exact proven Codex, Claude Code, and OpenCode native-provider tuples; generic ACP is excluded from governed unattended Runs unless a later decision proves both session MCP and exact tool policy (ADR-0005, within ADR-0011 and ADR-0014 bounds).
- The agent-facing transport is `stdio`.
- Each bridge is fixed to one Project, Task, Run, role, and capability scope.
- Tools do not accept arguments that let an agent select another scope.
- MCP writes durable commands and immutable `AgentOutcomeClaim` records through Director Engine application contracts.
- Agents never receive raw TaskStore/Dolt access.
- The bridge contains no independent workflow policy.

The bridge and host transport mechanisms are implementation details. They must preserve fixed scope, schema versioning, immutable claims/observations, idempotency, resumable cursors, and Director Engine decision/effect ownership (ADR-0017 and ADR-0018).

### 6.5 Execution leases

Only one Director Engine may hold the execution lease for a Project in `1.0`. Lease acquisition and renewal are durable and transactional. After expiry, a replacement Director Engine remains observe-only until it proves the prior engine and dispatch processes absent and completes reconciliation. The standalone Go engine is supervised independently of Director for Paseo, survives connector reload, and reconciles a replacement connector by contract descriptor and cursor. Connector cleanup is an optimization, never lifecycle proof (ADR-0015 and ADR-0017).

## 7. Organizer repository

### 7.1 One Organizer per Project

The Organizer is a dedicated per-Project Git repository plus its approved configuration revision, referenced TaskStore, skills, templates, decisions, and specifications. It is neither the Director Engine nor the Director for Paseo connector, and it requires no standing planning agent (ADR-0018). It may use:

- an existing remote;
- a private GitHub repository created during bootstrap;
- local-only storage.

The user configures its remote destination. It is separate from the public plugin repository and every product repository.

### 7.2 Proposed layout

```text
organizer/
├── paseo-director.json
├── README.md
├── skills/
│   ├── commits/SKILL.md
│   ├── pull-requests/SKILL.md
│   └── reviews/SKILL.md
├── templates/
│   ├── task.md
│   ├── pull-request.md
│   └── review.md
├── decisions/
│   ├── 0001-example.md
│   └── ...
├── specs/
│   └── ...
└── <TaskStore-owned data>
```

`paseo-director.json` is both the discovery marker and the single operational configuration file for `1.0`.

### 7.3 Formats

- Strict JSON plus a published JSON Schema for machine configuration.
- Markdown for skills, templates, decisions, and specifications.
- TaskStore-owned format for dynamic state.
- No YAML configuration in `1.0`.

The JSON contains at least:

- `$schema` and `schemaVersion`;
- Project identity;
- Workspace/repository definitions;
- agent profiles;
- defaults and overrides;
- workflow and delivery policies;
- limits and budgets;
- explicit skill and template references.

Files are referenced explicitly. Director never executes or injects an arbitrary file merely because it appeared in a directory.

### 7.4 Configuration activation

Configuration and metadata revisions are never activated silently.

1. A new Organizer commit is detected as a pending revision.
2. The Director Engine deterministically validates schema, semantics, paths, references, policies, and required capabilities (ADR-0018).
3. The UI presents the exact diff and impact.
4. A human confirms `Apply`.
5. If the proposal is not committed yet, Apply creates exactly one logical commit containing only the previewed files.
6. Director records the exact SHA as the active revision.
7. Only future Runs receive the new revision.

A human or model may edit and commit proposals, but a model's proposal is only a claim and cannot activate them. The active-revision pointer lives in dynamic Project state to avoid self-referential commits. Only the Director Engine executes the human-confirmed Apply command (ADR-0018).

Decisions are superseded through a new record rather than silently rewriting history.

### 7.5 Precedence

```text
Project defaults → Workspace overrides → Task overrides
```

The UI presents each override as `Inherit` or a concrete value. The Director Engine reducer previews the effective configuration before launch, and that configuration, its claim schemas, and its PreparationPlan are frozen into the Run (ADR-0018).

Overrides cannot exceed the human-approved security and policy envelope. A one-off human override requires explicit audited confirmation.

### 7.6 Product repositories

Bootstrap does not add, edit, or commit files in product repositories. Director operates through existing checkouts, Git, registered host views, and Organizer configuration.

## 8. TaskStore and persistence

### 8.1 Abstraction

The domain depends on a `TaskStore` port, never on Beads commands or internal storage layout.

Director `1.0` uses one Director-owned direct Dolt `2.3.2` schema behind the Director Engine's trusted typed `TaskStore` adapter (ADR-0004 and ADR-0012). Dolt remains a separately supervised external process; the engine is the sole raw SQL identity and credential holder. Agents, host connectors, UI, repositories, prompts, and configuration receive only scoped typed contracts, never raw SQL or TaskStore credentials. Beads `1.2.2` is rejected as the product runtime store; this repository's Beads workflow is independent.

The selected mapping supports:

- Epics, Tasks, and dependencies;
- Runs and Candidates;
- commands and idempotency keys;
- immutable events/audit;
- optimistic concurrency;
- multiwriter access through the selected server mode;
- backup, restore, migration, and remote synchronization;
- Linux.

[ADR-0016](adr/0016-defer-taskstore-scale-proof-to-m5.md) defers the
agreed-scale proof to `dir-m5.10`. M1 may use only the exact direct-Dolt
contract and bounded Linux topology approved by ADR-0004 and ADR-0012. The
deferral makes no production-scale claim and changes no correctness or
security invariant.

`1.0` ships that one runtime TaskStore, not interchangeable engines. ADR-0004 requires exact listener/database identity and safe global/session commit values before every write; ADR-0012 requires Git and Dolt synchronization to remain two visible, separately retryable effects plus verified backup/restore/migration and metrics-safe cleanup. Any identity, safe-mode, restore, migration, or partial-sync ambiguity fails closed without a fallback store.

### 8.2 Data ownership

| Data | Canonical owner |
|---|---|
| Configuration, skills, templates, specs, decisions | Organizer Git |
| Epic, Task, Run, Candidate, commands, bounded claims, observations, decisions, events, and audit | TaskStore (ADR-0018) |
| Complete agent conversations and timelines | Paseo |
| Complete PRs, reviews, and CI logs | GitHub |
| Commits, branches, and source code | Product Git repositories |

Director stores references and structured summaries instead of duplicating conversations, PR threads, or complete CI logs.

### 8.3 Synchronization

When the Organizer has a remote, the UX presents one `Sync` action with two explicit results:

1. Normal Git synchronization for Organizer files.
2. TaskStore/Dolt synchronization for dynamic state.

They are separate technical operations and are not falsely presented as an atomic transaction. Partial failure remains visible and retryable.

Both streams synchronize automatically when a remote is configured; `Sync now` remains available. Dynamic state uses an approximately one-minute debounce and flushes at critical transitions.

The target is the same remote: normal Git branches for files and Dolt-managed refs, for example `refs/dolt/data`, for data. M0 must prove that layout, partial-failure recovery, and the supported server/shared-server multiwriter mode before implementation depends on it.

### 8.4 Backup and migration

- Automatic daily TaskStore backup.
- Seven-day default retention.
- Backup before every schema migration.
- Validate the backup and current store before migrating.
- Pause the Project during migration.
- Record schema version and migration result.
- Failure leaves previous data recoverable and the Project paused.

Migration to another daemon is outside `1.0` and requires a later bootstrap/rebinding flow.

## 9. Board, lifecycle, and state projection

### 9.1 One planning surface

Each Project has one Task surface with `Board | List` modes over the same query and filters.

There are no separate Backlog and History products in `1.0`.

Board columns are derived:

1. `Needs you`, visible only when non-empty.
2. `Queued`.
3. `Building`.
4. `Validating`.
5. `In review`.
6. `Ready`.

Completed work is accessed through a `Done` filter instead of permanently occupying horizontal space.

Columns are a semantic projection from the pure, versioned Director Engine routing reducer, not editable states (ADR-0018). Validation and Review are sibling obligations rather than a short-circuiting pipeline, so a Task may return to a phase or traverse them in either order. The Board reflects reconciled facts instead of model claims or a false visual sequence.

### 9.2 Needs you

`Needs you` is reserved for actionable human intervention:

- a permission request;
- an exhausted correction or replacement budget;
- an irreconcilable Git state;
- a configuration or credential decision;
- a policy-override request;
- an acknowledged 85% soft-budget pause or a 100% hard-budget exhaustion;
- ambiguous recovery.

A normal dependency wait remains in `Queued` and shows its blocker. Needs-you records are typed, name the exact human decision or machine-checkable wake condition, and can be emitted only by the Director Engine escalation reducer (ADR-0018).

### 9.3 Derived state

Task state is projected from durable facts:

- launch eligibility;
- active agent turn;
- worktree and Candidate state;
- reviews and validations for the current Candidate;
- publication and integration;
- pending human input;
- terminal outcome.

The seven closed agent outcomes, failure-interpretation claims, and review claims are bounded TaskStore inputs, not state. The Director Engine projects state only after reconciling each claim against its named durable and external facts (ADR-0018). The user cannot drag a card to claim that CI or review occurred. Human interventions are explicit audited commands.

## 10. Scheduler and launch policy

### 10.1 Launch configuration

Project launch policy is `manual` or `automatic`. Each Task can inherit it or apply a permitted `manual`/`automatic` override.

The Director Engine's pure Eligibility reducer allows launch only when (ADR-0018):

- the Project is active;
- policy permits launch or a human requested `Launch now`;
- the Organizer revision is approved;
- dependencies are satisfied or explicitly overridden;
- the Task has no active Run;
- preflight passes;
- concurrency capacity is available;
- time, cost, and CI budgets permit launch.

For each eligible Task, the scheduler requests the Director Engine Launch reducer. Only after `preparation_ready`, the engine persists the unique creation intent and commands the Director for Paseo connector to create the top-level Task Agent through the fixed host interface. It never launches Task work from an Organizer or another agent's subagent API, and it never supplies a parent agent (ADR-0010, ADR-0017, and ADR-0018).

### 10.2 Ordering

The pure, versioned scheduler reducer uses this order (ADR-0018):

1. Progress an already-started Run that needs Reviewer, correction, validation, or integration capacity.
2. Human `Launch now` requests.
3. Eligible Tasks by `urgent`, `high`, `normal`, and `low` priority.
4. FIFO by `queuedAt` within the same priority.

There is no manual rank or queue drag-and-drop in `1.0`.

`Launch now` does not interrupt agents and does not exceed hard safety, disk, cost, CI, or capacity limits. Ignoring a dependency requires a separate explicit confirmation.

### 10.3 Default capacity

- `maxActiveTasks`: 6.
- `maxActiveTasksPerWorkspace`: 2.
- `maxConcurrentAgents`: 8.
- `maxSubagentsPerTask`: 3.

Within those Project limits, the delivery scheduler has separate adaptive
ceilings of six Task Agents, two Reviewers on non-interfering bases, two
complete CI processes, and one integration per repository. It requires a
current machine sample and parks above load `48`, below `24 GiB` available
memory, or below `50 GiB` free `/tmp`. A load, memory, or temporary-space
dimension in its reserve band contracts each `6/2/2` ceiling by one, never
below one. Existing tighter capacity, budget, disk, security, and provider
limits still apply (ADR-0019).

The agent limit includes top-level Task Agents, top-level Reviewer Agents, and helper subagents created by Task Agents. There is no reserved Organizer execution slot or standing planning agent. Helper subagents also consume the creating Task's helper quota, time/cost budget, and any applicable Workspace or provider capacity (ADR-0018).

## 11. Deterministic preflight and environment preparation

Preflight and preparation are mandatory and blocking before every launch. ADR-0018 requires a versioned `PreparationPlan`, frozen into the Run before any agent creation. Each step declares a stable ID, ordinal, kind, exact input hashes, canonical executable/argv and working directory where applicable, environment allowlist, expected outputs, ADR-0015 effect class, timeout, attempt budget, and closed failure mapping. Shell interpolation, undeclared installers or lifecycle hooks, model-selected commands, and silent fallback are prohibited.

The whole plan has a controlling 1,200-second deadline. Dependency preparation has a 300-second per-command ceiling and a 900-second aggregate ceiling. Before every step or dependency command, the effective timeout is the minimum of its declared ceiling, the remaining dependency aggregate where applicable, and the whole-plan time remaining after reserving the declared maxima of later mandatory steps. These are simultaneous ceilings, not independent time entitlements: every mandatory step retains a deterministic opportunity to run, and preparation can never consume more than 1,200 seconds even though the individual ceilings sum to 1,250 seconds (ADR-0018 P3 consolidation).

Projects may tighten these limits. Expanding any limit requires an explicit human-approved configuration revision within the security envelope (ADR-0018).

| Order | Declared deterministic step | Timeout | Failure semantics |
|---:|---|---:|---|
| 1 | Freeze Task, policy, Organizer revision, TaskStore versions, Run identity, budgets, and exact base/repository inputs | 10 s | TaskStore identity, safe-mode, lease, or version failure pauses/degrades before launch; reload a version conflict, never overwrite it. |
| 2 | Reconcile eligibility, dependencies, capacity, credentials/capabilities, and external health | 30 s | Dependencies stay Queued; transient unavailable facts wait with backoff; missing human-owned configuration/authority routes to Needs you; no partial preparation follows. |
| 3 | Verify source/common-directory/remote/base identity, ownership, disk/resource limits, rootless-OCI capability, and ADR-0014 lifecycle-surface admission | 30 s | Mismatch, missing finite observation, unapproved non-empty lifecycle surface, or ownership ambiguity routes to Needs you; a secret observation is a P0 stop. |
| 4 | Create or adopt the uniquely keyed Director-owned product worktree and register its host Execution Workspace view | 120 s | Use ADR-0015 `unique_create`: a proven pre-handoff failure may retry after fresh proof; possible handoff is unknown and reconciled; one exact match is adopted; zero unproved or multiple matches park. Worktree ownership never transfers to the connector (ADR-0017 consolidation). |
| 5 | Materialize the frozen rootless-OCI profile, private Candidate Git path, fixed MCP scope, and process/memory/output/temp/worktree limits | 60 s | A proven pre-handoff failure may retry within budget; possible partial mutation is reconciled; missing isolation, scope, or finite limit parks without running less isolated. |
| 6 | Probe every declared executable, exact version/capability, provider tuple, and authentication mode without mutation | 60 s total | Missing/different tooling or capability routes to Needs you; transient service unavailability waits with backoff; no implicit install or replacement. |
| 7 | Run project-declared dependency preparation commands in order | 300 s per command; 900 s aggregate, both subject to the remaining 1,200 s plan deadline | Exit 0 plus declared output hashes completes a command. Nonzero exit records `preparation_failed` and routes to Needs you. Proven pre-handoff failure may retry only after fresh authorization; a timeout/lost result after possible side effects is unknown and follows its ADR-0015 class. No blind rerun or model setup diagnosis. |
| 8 | Build, redact, size-check, and hash Task context, acceptance criteria, skills/templates, and output schemas | 30 s | Schema, reference, size, redaction, or secret-safety failure prevents creation and routes to Needs you; detected secret exposure is a P0 stop. |
| 9 | Commit `preparation_ready` with every output hash and a fresh eligibility/version check | 10 s | Transaction/lease/version failure leaves no barrier; reload or pause rather than prompting from partial preparation. |

If the 900-second dependency aggregate or 1,200-second whole-plan deadline expires, the Director Engine withholds `preparation_ready`. A proven pre-handoff expiry records `preparation_failed` and routes to Needs you. If the active step may have handed off side effects, its Effect first becomes `unknown` and is reconciled under ADR-0015; after that bounded reconciliation the Run records `preparation_failed` and routes to Needs you without retrying blindly. This is the deterministic aggregate-expiry route required by ADR-0018.

Only after `preparation_ready` may the Director Engine persist the uniquely keyed top-level agent-create intent. Creation carries only the exact zero-work bootstrap. After the bootstrap finishes, the engine persists the returned agent/workspace identity and frozen labels, then records a separate real-prompt intent. Task or Review work starts exclusively through `send_agent_prompt` with `notifyOnFinish=true`. A lost response is reconciled by exact facts rather than repeated blindly (ADR-0020).

## 12. Agent profiles and authority

### 12.1 Profiles

Each Project defines separate profiles for:

- Task Agent (`Worker` role);
- Reviewer Agent (`Reviewer` role).

A profile includes provider/model, effort or thinking option, operating/permission mode, provider-native options, MCP policy, budgets, and an optional ordered fallback chain.

Fallback chains are empty by default. Every fallback must be declared and ordered explicitly.

### 12.2 Planning and administrative authority

A human or a model-backed planning context can:

- inspect Project, Workspace, Epic, Task, and Run state;
- discuss progress and risks;
- create and update planning work;
- submit permitted operational and launch commands;
- prepare configuration and metadata proposals.

No model can activate configuration, decide a lifecycle transition, or change its model, effort, permissions, repositories, delivery, or security authority. It can only submit a bounded claim or propose those changes for human Preview/Apply. The Director Engine alone validates, reduces, and executes the resulting command (ADR-0018).

Any planning context receives only typed Project-administration commands within the approved envelope. Task Agents receive least-privilege Task/Run tools. Reviewer Agents receive read-only Candidate/review access plus one operation for submitting a structured verdict. None receives Director lifecycle-effect authority.

### 12.3 Closed Task Agent outcome contract

Every Task Agent turn ends with exactly one schema-versioned `AgentOutcomeClaim` from this closed vocabulary (ADR-0018):

| Outcome | Claim and reconciled reduction |
|---|---|
| `completed` | Claims exact Candidate/base SHAs, one result per criterion ID, and bounded residual-risk codes. The engine proves ended turn, scope/revision, clean worktree, owned/reachable Candidate descending from base, no conflict, and current budgets; it records/adopts the Candidate, queues the owned draft, and then queues Validation and Review as sibling obligations, never closure. |
| `needs_validation` | Claims exact Candidate/base and frozen check IDs. After the same Candidate proof plus current check/environment/capacity facts and absence of current results, queue the owned draft and then the configured deterministic checks plus independent Review. |
| `needs_review` | Claims exact Candidate/base and criterion IDs. After Candidate proof plus reviewer-independence, detached-checkout, model/profile, capacity, and current-review facts, queue the owned draft and then one exact-Candidate Review plus configured Validation. |
| `needs_human_decision` | Claims one typed question code, bounded question, closed options, affected scope, and machine-checkable resume condition. Only if no current decision or frozen policy answers it, persist one human-decision request and route to Needs you. |
| `blocked_by_dependency` | Claims exact dependency IDs and wake predicate. The TaskStore graph must prove each dependency and absence of an override; keep Queued, or discard a stale claim and recompute. |
| `blocked_by_access` | Claims a typed capability/resource code, operation code, and redacted fingerprint. Fresh Doctor/preflight/effect observations route transient unavailability to waiting, human-owned credentials/configuration to Needs you, and scope/identity conflict to fail-closed refusal. |
| `budget_exhausted` | Claims budget dimension and observed amount/unit. The engine uses its usage ledger, provider facts, reservations, limit, and TaskStore time: below 85% reject as unproved; 85% to below 100% apply the soft pause; at 100% route to Needs you. |

There is no generic success, failure, retry, stuck, route, close, or free-form fallback. Unknown values/fields, schema or scope errors, oversize fields, secrets, or private paths are retained only as bounded malformed-claim audit facts and trigger reconciliation, not an inferred outcome. The connector fixes Project, Workspace, Task, Run, role, native agent, turn/request identity, revision, schema version, and server time; the model cannot select them. Accepted claims are immutable and idempotent, and they never change state before fresh reconciliation (ADR-0018).

### 12.4 Task Agent and helper subagents

- Exactly one normal top-level Task Agent corresponds to one active Task Run. The Director Engine commands the host connector to create it as a top-level client, omits the parent field, sets its visible title to the exact Task title, and places it in the Task's isolated Execution Workspace (ADR-0010 and ADR-0017).
- No planning model or scheduler creates a Task Agent as a child or subagent. Concurrent Tasks against one Director Workspace/repository use distinct Execution Workspaces.
- The Task Agent may create helper subagents when it considers them useful. Those helpers are internal to its Run and never become Task records, Task owners, separate Runs, or Director-launched work.
- Writer helpers always use isolated checkouts and return commits to the Task Agent. Read-only helpers may share the checkout only when provider and permissions make it safe.
- The default maximum is three helper subagents per Task. Director reserves and reconciles their capacity, identities, and budgets, requires fixed Project/Task/Run scope and containment, and includes them in interruption, recovery, and cleanup without becoming their launcher. An unknown helper-creation result parks rather than being retried blindly.
- The mandatory top-level Reviewer Agent does not consume the helper quota, but it does consume global agent capacity.
- Before any Task Agent or Reviewer starts, the engine records and verifies its
  root-workspace Worker entry. Missing visibility refuses launch. The Director
  Workers projection exposes role, Task, phase, duration, Candidate, and Open
  agent action (ADR-0019).

The Director Engine decides helper admission, capacity, and every later lifecycle transition. The Task Agent may invoke only the one admitted parent-bound creation; an unknown result parks rather than recreating blindly (ADR-0018).

### 12.5 Reviewer profile

The Reviewer Agent may use the same model as the Task Agent, with a warning. `requireDifferentReviewerModel` is optional and disabled by default.

Review remains mandatory regardless of model equality or Validation outcome. Every review must cover acceptance/specification, correctness, security, maintainability, readability, design, quality, and rigor. The profile and tool boundary cannot waive any dimension (ADR-0017 and ADR-0018).

## 13. Execution workflow

### 13.1 Workspace and branch ownership

For every Run, the Director Engine (ADR-0017 and ADR-0018):

1. resolves and records the exact base SHA;
2. creates or adopts one owned Task branch;
3. persists a uniquely keyed worktree intent, applies ADR-0014 lifecycle admission, creates or adopts the isolated product worktree through its Git/worktree adapter, verifies ownership, and then has the host connector register/translate that exact directory as the Execution Workspace view without transferring lifecycle or cleanup ownership;
4. persists a uniquely keyed Task Agent creation intent and commands the Director for Paseo connector to create exactly one normal top-level Task Agent in that registered workspace, with the parent omitted, the exact Task title, frozen public labels, and only the zero-work bootstrap;
5. observes bootstrap completion and persists the returned Paseo agent ID, workspace ID, and frozen labels before any later effect; and
6. persists a separate real-prompt intent and starts Task work only through `send_agent_prompt` with `notifyOnFinish=true`, bound to that persisted identity, frozen profile, policy, skills, templates, claims, PreparationPlan, and MCP (ADR-0020).

One Task has at most one active branch and one active pull request.

### 13.2 Candidate production

The Task Agent must produce a commit and return one closed outcome claim. The Director Engine independently inspects the worktree and Git graph; the claim is not Candidate evidence (ADR-0018).

- A dirty worktree cannot be reviewed.
- An absent or unreachable commit cannot be reviewed.
- A Candidate is only recorded after proving its SHA and ownership.
- A correction with a different SHA creates a new Candidate.

### 13.3 Validation, failure interpretation, and independent review

Every admitted Candidate first enters one uniquely owned draft-publication
effect. After the draft is observed at the exact Candidate, the engine records
one authoritative complete remote Linux CI and one independent Review as
sibling obligations and dispatches them concurrently when capacity allows.
Recovery dispatches only a missing sibling intent. A passed, failed, timed-out,
or unavailable Validation never suppresses Review (ADR-0018 and ADR-0019).

1. **Validation execution is deterministic.** The Director Engine selects only frozen check IDs, runs exact argv in the exact Candidate environment, and records a `ValidationObservation` bound to Candidate/base/configuration hashes, check identity, timestamps, exit status or signal/timeout, and a bounded redacted output digest. Exactly one complete remote Linux CI is authoritative for each Candidate. Its bounded observation identifies the unique workflow run and exact required checks. Exit zero means only that the declared check passed; any other result means only that it did not pass. The executor cannot explain, waive, retry, reroute, or correct.
2. **Failure interpretation is model work.** After a non-passing Validation, the original Task Agent may receive a separate read-only interpretation turn bound to that Observation and Candidate. It returns exactly one `FailureInterpretationClaim`: `candidate_defect`, `base_failure`, `environment_failure`, or `indeterminate`, with bounded cited facts and proposed correction scope. This claim grants no edit turn. The Director Engine reconciles Git/base/environment/check/budget/attempt facts and alone decides correction, wait, refusal, or escalation.
3. **Review is always independent model work.** For every admitted Candidate, the Director Engine commands one normal top-level Reviewer Agent through the host connector when capacity permits, with the parent omitted and only the zero-work bootstrap on create. It persists the native identity and labels before starting Review exclusively through `send_agent_prompt` with `notifyOnFinish=true`. Pause, Emergency-stop, security, identity, and resource gates may delay or forbid all execution; a Validation result may not. The Reviewer receives objective, acceptance/specification, policy, and Candidate/base facts but no author transcript; it uses a detached disposable exact-SHA checkout, has no write tools, and must cover acceptance/specification, correctness, security, maintainability, readability, design, quality, and rigor (ADR-0017, ADR-0018, and ADR-0020). The versioned review harness consumes the one authoritative remote CI observation and does not start another complete CI. It independently rechecks manifest, Task, diff, tree, base, and current durable context. The Reviewer returns exactly one `ReviewClaim` verdict—`approve_candidate`, `changes_requested`, or `needs_human_decision`—with exact Candidate/base bindings and structured cited findings. The engine validates identity, independence, checkout, citations, schema, current SHA, and remote-CI observation binding before projection (ADR-0019).

The accepted fast path opens or updates its owned draft before Review, without
merge authority. It marks that same draft ready only after exact Review and CI
evidence are present. Changing the Candidate invalidates both previous
Validation and Review for readiness purposes and updates the same owned draft
under an exact lease (ADR-0019).

### 13.4 Validation and correction

`autoFixCiFailures` and `autoFixReviewFeedback` are enabled by default and can be inherited/overridden by Project/Task.

The correction reducer uses these defaults (ADR-0018):

- three correction attempts;
- four total CI cycles: the initial cycle plus three corrections;
- never automatically rerun the same failed commit;
- batch all current validation failures, failure interpretation, Review findings, and human feedback before authorizing a correction;
- corrections are performed by the Task Agent;
- every new commit requires fresh review and validation.

Correction turns reuse PLAN and skill content only while their digests match
the frozen Run, refresh current human decisions and the Candidate diff, and
carry all current Review, Validation, and human-feedback findings in one
deterministically ordered batch. An acknowledgement-only output is rejected
once; repetition escalates instead of consuming serial correction turns
(ADR-0019).

Every Run has mandatory finite time, token, and turn limits, an optional finite cost limit, and an explicit CI budget. For each finite consumptive budget, usage is durable consumption plus outstanding reservations. At 85% of any hard limit, or before a dispatch whose reservation would reach 85%, the Director Engine atomically records one `soft_budget_reached` event, warns with the measured dimension/ratio, and pauses new model-consuming turns, helpers, retries, corrections, and Reviews at the next safe boundary (ADR-0018). Correction, replacement, CI, and effect-attempt counts retain their own integer hard limits rather than fractional soft limits.

An active turn may finish. Observation, reconciliation, evidence persistence, safe containment, and required non-destructive cleanup continue, while the 100% hard limit remains enforced. Only an explicit human command may resume within the unchanged limit or apply a permitted budget revision; acknowledgement suppresses repeat warning for that exact revision but does not enlarge it. At 100%, the engine starts no budget-consuming work and routes the Task to `Needs you` with the exact exhausted dimension. Restart or model claim never resets consumption, reservations, acknowledgement, or exhaustion. This explicitly maps ADR-0018's soft/hard budget decision to validation and correction.

### 13.5 Feedback

Human feedback can arrive through:

- a direct Paseo message/action associated with the Task;
- a GitHub PR review or comment.

The Director Engine Routing reducer reconciles direct Paseo and GitHub feedback against the exact current Candidate and frozen policy. Current actionable feedback reopens/reroutes work and, when automatic correction is enabled, can authorize a bounded correction by the same Task Agent; feedback text itself cannot decide the transition (ADR-0018).

Agents do not converse through GitHub, publish agent-authored review discussions, or automatically resolve human threads.

Feedback received after Done creates a new linked Task and does not mutate completed history.

## 14. Delivery and integration

### 14.1 Delivery modes

Only two modes exist in `1.0`:

- `pull_request`;
- `direct`.

Only the Director Engine decides and performs branch, push, PR, integration, and cleanup lifecycle effects. It reconciles exact external facts under ADR-0006 and ADR-0015; an agent or connector claim is never evidence (ADR-0018).

### 14.2 Pull request

- The Director Engine pushes the Task branch and opens at most one PR after its delivery reducer admits the exact effect.
- The accepted fast path creates or updates the one owned draft immediately
  after Candidate admission, under an exact force-with-lease. Draft creation
  carries no merge authority. Publication readiness still waits for the exact
  independent Review and authoritative remote CI observation (ADR-0019).
- The Director Engine observes GitHub CI and human feedback against the current Candidate.
- Manual merge is the default.
- Automatic merge is configurable by Project/Task.
- Automatic mode requires the exact approved Reviewer UUID, the recorded
  authoritative remote CI, and configured CI gates but no human approval.
- The Director Engine performs final merge/integration only after fresh exact-head/base/check/feedback/mergeability reconciliation and an atomic expected-head precondition (ADR-0006, ADR-0015, and ADR-0018).

### 14.3 Direct

- The Director Engine validates the Candidate and base according to frozen policy.
- In manual mode, Ready waits for a human integration action.
- In automatic mode, the Director Engine integrates and pushes to the remote target branch after the same deterministic routing and exact-fact gates.
- Direct is never an implicit fallback for failed or unavailable PR delivery.

### 14.4 Base changes

A relevant base change while a Task is Ready invalidates readiness. The Director Engine reducer authorizes update/rebase only under configured Git policy and requires new Validation and independent Review for the resulting Candidate; no model or connector chooses the route (ADR-0006 and ADR-0018).

### 14.5 Remote branch cleanup

After verified integration, the Director Engine may automatically delete a remote Task branch only when its ADR-0007/ADR-0015 destructive-terminal gate proves that:

- Director created/owns the branch;
- the exact Candidate is integrated;
- no active Task/PR still needs it;
- policy permits deletion.

This behavior is configurable by Project/Task and enabled by default.

## 15. Pause, cancellation, recovery, and cleanup

### 15.1 Pause

`Pause Project` is a Director Engine state which prevents new launches, retries, Reviewers, PR publication, and integration. The 85% soft-budget pause uses the same next-safe-boundary behavior for model-consuming work without blocking observation or safety cleanup (ADR-0018).

An active agent may finish its current turn. The Task parks at the next safe boundary without killing the process or deleting the worktree.

`Resume Project` reconciles all facts through the Director Engine before continuing and does not repeat completed effects.

### 15.2 Emergency stop

Only the Director Engine executes an authenticated human `Emergency stop` command (ADR-0018). It:

- requires human confirmation;
- pauses the Project;
- terminates all active Task Agents and Reviewer Agents plus every observed helper subagent in their Runs;
- marks their Runs cancelled;
- applies snapshot/cleanup policy to unintegrated work;
- returns Tasks to `Queued`;
- does not relaunch anything until explicit Resume.

### 15.3 Task cancellation

`Cancel Task` only affects the selected Task/Run. Its cleanup is configurable. The record and audit history remain.

### 15.4 Agent failure recovery

The Director Engine may authorize at most one replacement top-level Task Agent after a reconciled recoverable failure and only when the previous Task Agent is `closed`, has non-null `archivedAt`, and external process/effect facts prove termination. The same no-parent creation contract and exact Task title apply; the engine persists the replacement ID before further effects and supplies durable Task/Run/Candidate facts rather than conversation history or a model claim (ADR-0003 and ADR-0018).

If the replacement fails or recovery is ambiguous, the Task enters `Needs you`.

### 15.5 Cleanup defaults

- `terminateOnCompletion`: `true`.
- Task completion, cancellation, and replacement reconcile and terminate helper subagents before removing a registered host workspace view or an owned product worktree.
- Successfully integrated Director-owned product worktrees are removed automatically only after the full exact ownership/recovery gate; connectors translate registration/archive observations but do not own cleanup (ADR-0017).
- Dirty work from failed/cancelled Runs uses `snapshot_then_delete` by default.
- The snapshot creates a hidden local Git ref tied to Task/Run/Candidate metadata.
- Recovery retention defaults to seven days.
- Failure/cancellation cleanup is configurable by Project/Task.
- Only material proven by the Director Engine to be Director-owned is deleted.

Under ADR-0021, a nonterminal `dispatching` or `unknown` local or remote
Task-ref deletion may make a new attempt only after a fresh authoritative
observation proves that the exact ref still equals the immutable Candidate.
The retry repeats the same explicit expected-OID `force-with-lease` or
`update-ref` compare-delete guard. Absence completes the recorded intent;
changed, unavailable, or ambiguous facts preserve the ref. This narrow rule
does not apply to worktrees or other destructive targets, and `complete`
remains terminal on later reappearance.

Automatic ignored-tree recovery uses ADR-0013's measured Linux release envelope, which replaces ADR-0007's provisional ceilings:

| Resource | Default and hard expansion ceiling |
|---|---:|
| Recovery entries, including files and directories | 10,000 |
| Inspected worktree entries | 25,000 |
| Aggregate ignored content | 512 MiB |
| One regular file | 256 MiB |
| Sequential stream buffer | 64 KiB |
| One copy/hash/verify/restore phase | 180 seconds |
| Full artifact lifecycle | 480 seconds |
| External worker supervisor | 540 seconds |
| Measured worker RSS growth | 192 MiB |
| Recovery retention | Seven days |
| Projected free-space floor | 10% of the relevant filesystem |
| Disk reservation | Remaining content + 64 KiB + 4 KiB per entry |
| Full pre-destructive artifact revalidations | Five |

Project and Task policy may only tighten a maximum, raise the free-space floor, or shorten retention. It cannot expand a maximum, lower the floor, extend retention, parallelize streaming, or skip a gate without new Linux evidence and a superseding ADR. Any over-limit, timed-out, memory-exhausted, disk-pressured, changed, unsupported, unowned, replaced, unreadable, or unprovable tree remains in place and routes to Needs you with a path/content-free reason. A killed worker never authorizes deletion, and retry adopts only an exact verified artifact.

Below 10% free space on the relevant filesystem, launches stop and cleanup runs first. If the threshold remains violated, the Project becomes degraded/Needs you and consumes no more disk.

The system prioritizes cleanliness without sacrificing recoverability.

## 16. User experience

### 16.1 Surfaces

Director contributes:

- a global Director Home sidebar surface;
- the Project Board/List in Project/Organizer-repository context;
- a Task Inspector in workspace/agent context;
- Command Center actions for common operations;
- plugin-owned timeline items where useful and supported.

The interaction model is inspired by the supplied AO screenshots—a dense dark Board, compact cards, and explicit execution detail—but adapts them to Paseo theme tokens, native plugin boundaries, accessibility, and compact/mobile layouts instead of copying them literally.

The UI requires no standing Organizer conversation or dedicated agent tab. A user may open ordinary planning conversations, but they submit only typed commands/claims and cannot become lifecycle authority (ADR-0018).

Director hierarchy lives in its own UI. Technical Paseo workspaces and agents remain visible. A Task Agent's visible title is exactly its Task title; other resources receive clear names linking them to Project/Task/Run.

### 16.2 Director Home

Home provides:

- `Create Project`;
- `Adopt Organizer`;
- health and active-work summaries by Project;
- quick access to Board, Organizer repository/configuration, Doctor, Sync, Pause, and Needs-you items (ADR-0018).

### 16.3 Board and List

- One surface with a Board/List toggle.
- Flat Board by default with optional grouping by Epic.
- List expresses Epic/Task hierarchy and supports filtering/sorting.
- Compact cards and fixed, purposeful List columns.
- Filters for state, Workspace, Epic, priority, labels, and attention.

### 16.4 Task detail

Selecting a card opens a modal instead of immediately navigating to the agent. The modal has:

- `Details`;
- `Execution`;
- `Activity`.

`Open agent` is a separate action. A composer pill and Command Center action return to the corresponding Task/Board.

On desktop, the Task Inspector can appear in Explorer for the selected agent/workspace.

### 16.5 Mobile

Mobile is a first-class client:

- List is the default compact view.
- Board shows one lane at a time.
- Task detail is a bottom sheet or full-screen sheet.
- Actions never depend on hover.
- Themes and text use Paseo tokens and React Native primitives.

The backend remains on the daemon; the phone is only a client.

The current plugin API does not expose a general persistent native-notification contribution. `1.0` uses Director Home/Board and Paseo's agent-attention surfaces.

## 17. Bootstrap and removal

### 17.1 Create Project

The wizard supports:

- an existing Organizer remote;
- creating a private GitHub Organizer repository;
- local-only operation;
- adding Workspaces from an existing checkout, directory, or clone URL.

Before applying, it presents a complete preview of:

- Organizer files and Git operations;
- repository identities and paths;
- provider/MCP requirements;
- TaskStore initialization;
- authentication;
- defaults and security implications.

Nothing is applied before human confirmation. After confirmation, only the Director Engine reducer may authorize and perform the effects; UI or model narration is not applied-state evidence (ADR-0018).

### 17.2 Adopt Organizer

Adoption:

1. selects or clones the Organizer on the daemon;
2. has the Director Engine validate its marker, schema, Git state, and TaskStore;
3. has the Director Engine validate every Workspace path/remote;
4. displays unresolved capabilities;
5. only activates after human Preview/Apply and deterministic Director Engine reduction (ADR-0018).

### 17.3 Removal

Removing or archiving a Project never physically deletes the Organizer, product repositories, TaskStore history, or unknown worktrees. Cleaning proven ephemeral resources is a separate previewed action.

## 18. Security and credentials

Director for Paseo is trusted, unsandboxed host code on the daemon host. ADR-0014 supersedes ADR-0008's M1-blocking decision while retaining its maximal threat analysis. Under that approved practical boundary, the reviewed Director Engine, Paseo daemon, and exact admitted provider CLIs are trusted components; model output, repository/dependency/test content, and external tool output remain untrusted inputs. Rootless OCI and engine-side authorization are mandatory defense in depth, not a claim against a compromised trusted component.

### 18.1 Credentials

- Git uses the host's existing credential mechanism.
- GitHub uses the existing authenticated `gh` session.
- Provider authentication remains owned by Paseo and provider CLIs.
- On exact Paseo `0.7.2`, the Director for Paseo connector owns the project-owner-accepted full daemon-operator credential and public SDK client; Director Engine never receives either (ADR-0017).
- That credential lives outside repositories and Paseo-managed plugin checkouts and is readable only by the connector process. Its bytes and location never enter engine argv/environment/inherited descriptors, protocol messages/events, UI, TaskStore, projections, logs, timelines, diagnostics, or support bundles.
- Initial startup and every reload fail before SDK-ready, engine attachment, or host mutation when the credential is absent or empty. Before accepting a command, the connector advertises `credentialScope=full-daemon-operator`, contract version/hash, and only the fixed capability set; missing or stale descriptors fail closed.
- User-facing deployment documentation discloses this exact-0.7.2 P2 authority and all controls before installation. Director narrows connector authority when a supported headless connector-scoped credential or equivalently scoped startup-injected API is evidenced; that revision changes no engine logic.
- `1.0` has no custom vault or PAT entry form.
- Organizer, TaskStore, prompts, events, logs, diagnostics, and support bundles never contain plaintext credentials.

### 18.2 Human-owned security envelope

The effective envelope includes:

- permitted repositories and remotes;
- authorized target branches;
- delivery modes;
- permission for automatic integration;
- provider/model/mode/permission bounds;
- provider network/filesystem options;
- concurrency and budget ceilings;
- cleanup and destructive-operation policy.

The Organizer configuration and any human/model planning context operate within this envelope and may propose changes. Only human Preview/Apply can expand it, and only the Director Engine applies the exact confirmed revision. One-off human overrides are explicit and audited (ADR-0018).

### 18.3 Process and Git safety

- Executables are spawned with argument arrays; untrusted values are never interpolated into shell strings.
- Every governed provider path runs with the frozen ADR-0014 rootless-OCI profile and finite process, memory, elapsed-time, output, temporary-storage, worktree/disk, and free-space limits; missing or exceeded facts park without a weaker fallback.
- Paths are canonicalized and verified before mutation.
- Repository and remote identity are verified before Git effects.
- Target/protected branches are never force-pushed.
- Refs, branches, worktrees, and paths are never deleted without ownership proof.
- The Director Engine owns worktree admission/lifecycle/cleanup and all delivery effects. Host connectors translate only typed capabilities and observations. Provider sandbox options and rootless OCI are defense in depth inside the trusted-provider boundary, not a guarantee after provider/engine/kernel/root compromise (ADR-0014 and ADR-0017).

## 19. Platform, runtime, and distribution

### 19.1 Paseo compatibility

Director for Paseo initially targets only the exact stable Paseo `0.7.2` public contract admitted by ADR-0002 and ADR-0017. Another `0.7.x` or stable generation is not admitted by semver inference; it must pass the generated scaffold, contract, lifecycle, security, authority, and runtime suite and receive an explicit compatibility decision.

At the time of the deciding evidence:

- Paseo `0.7` is the current stable API.
- Paseo `0.8` is a preview with explicit client/server entrypoints and incompatible packaging.

Director does not publish production builds against a preview API. The fixed engine-owned host contract keeps a later host-package migration isolated from Director Engine logic. If incompatible stable generations must coexist, they receive separate release lines rather than a fragile hybrid entrypoint (ADR-0017).

### 19.2 Language

`1.0` uses Go for the standalone Director Engine, including domain/application logic, pure decision reducers, orchestration, TaskStore, scheduling, reconciliation, projections, and lifecycle effects (ADR-0017). Director for Paseo uses TypeScript/Node for its required Paseo plugin entry, React Native UI, plugin RPC, generated host-contract client, minimum policy-free connector, and other Paseo-only integration. The custom stdio MCP boundary uses engine-owned schemas regardless of implementation language.

The original language gate required concrete reliability, performance, or standalone-headless evidence before selecting a Go engine. ADR-0017 preserves and fulfills that exact §19.2 evidence trigger with the measured independently usable/publishable standalone-headless engine and interruption-safe connector contract. The Go Director Engine is therefore mandatory for `1.0`, not postponed or an optional host-embedded alternative.

### 19.3 Operating system

Director `1.0` supports Linux only. Other host operating systems are outside
the `1.0` scope and require a new explicit plan decision before they can be
claimed, implemented as supported targets, or added to release gates.

Director works on a Linux host where:

- Paseo supports the daemon/plugin runtime;
- required Git, GitHub, TaskStore, and provider executables are available;
- Doctor/preflight proves the capabilities.

Linux platform rules:

- do not interpolate untrusted values into shell commands;
- Go and Node path/temp/process APIs with direct argv execution in their owning process;
- correct canonical-path, symlink, permission, and locked-file handling;
- MCP over `stdio`;
- Linux worktree-cleanup tests.

### 19.4 Public distribution

- Public repository: `mcuadros/paseo-director`.
- Director for Paseo installs/updates through Paseo's Git-clone plugin lifecycle, which performs no dependency installation or install hook (ADR-0009 and ADR-0017).
- In explicit `release` mode, the installed connector commit pins the Director Engine version, target, exact reviewed source Candidate, and SHA-256 of both the binary and exact-source third-party notices. It downloads those Director-owned GitHub Release assets, verifies them before execution, and atomically caches them outside the plugin checkout at the platform XDG cache location. Missing/mismatched identity, asset, notice, target, source, or digest fails closed and never compiles (ADR-0017).
- In explicit `development` mode, the connector builds the Go engine from the selected local source with the declared local Go toolchain and never downloads or falls back to release mode. A missing explicit mode fails closed; ambient Git state, cache, toolchain, or network never selects or changes it (ADR-0017).
- Every engine start reports mode, version, exact source Candidate, target, executable/notices SHA-256, connector commit, and contract version/hash in structured diagnostics. Go is a development/release-builder prerequisite, not a release-user prerequisite. Statically linked releases ship verified notices generated from the exact source Candidate (ADR-0009 and ADR-0017).
- Director never automatically downloads undeclared external third-party binaries. Its own declared, pinned, verified release artifact follows the preceding accepted distribution chain.
- Doctor explains missing prerequisites and how to install them.
- No telemetry.
- ADR-0009 completed the M0 legal/distribution compatibility verification with a Go outcome for Apache-2.0 on exact Paseo `0.7.2`. Before public beta, `dir-m6.4` must revalidate the license file and implement the final dependency inventory/notices, prerequisite disclosures, compatibility statement, naming disclaimer, and channel instructions.
- Channels: internal alpha, public beta, and stable.

## 20. Observability, reconciliation, and retention

### 20.1 Reconciliation

The standalone Director Engine is event-driven and uses two independent clocks plus periodic external reconciliation as a safety net (ADR-0017 and ADR-0018).

- The daemon's `finished`, `error`, and `permission` terminal events
  synchronously enqueue coordinator reconciliation with an exclusive latency
  target below 1,000 ms. Events are never evidence. Durable event-ID
  deduplication makes exact replay a no-op; conflicting identity reuse, stale,
  forged, unknown, or cross-Run events fail closed (ADR-0019 and ADR-0020).
- Active turns are not polled. Local owned-process observation may still
  trigger reconciliation, but cannot extend liveness or prove death,
  stuckness, or free capacity. Only the five-minute compound multi-source
  stall watchdog may recover a lost terminal event.
- External fact observation every 30 seconds while any Run is active and every 5 minutes while all Runs are idle.
- Exponential backoff for unavailable external systems.
- Full reconciliation at engine startup, connector reload/replacement, reconnect, and lease takeover before resuming effects. The engine process survives connector reload and resumes from the monotonic cursor and durable facts.
- Intervals are internal `1.0` defaults, not user-facing configuration noise.

### 20.2 Health

The UI displays:

- `Healthy`;
- `Degraded`;
- `Paused`;
- `Needs you` with the exact reason.

Operational actions:

- `Sync now`;
- `Reconcile now`;
- `Doctor`, read-only;
- `Repair…`, always with preview;
- `Pause` / `Resume`;
- `Emergency stop`.

A network, authentication, provider, or forge outage degrades/parks work. It does not arbitrarily cancel unrelated Tasks.

No single source declares an agent stuck (ADR-0018). A typed `stalled_or_ambiguous` escalation requires all of: no new durable turn/command/effect/Candidate/Validation/Review/bounded-progress fact for at least 5 minutes; at least three local samples and two fresh external cycles; corroboration across at least two independent source classes; no declared long-running step, outage, Pause, or known wait; and complete ADR-0003/ADR-0015 reconciliation. Even then, replacement still requires the full termination predicate. Otherwise the engine parks without killing, retrying, replacing, freeing capacity, or cleaning.

### 20.3 Retention

| Information | Retention |
|---|---|
| Organizer configuration/metadata | Permanent Git history |
| Essential Task/Run/audit facts | Permanent and logically archivable |
| Agent conversations | Paseo-owned; not duplicated |
| Complete PR/CI data | GitHub-owned; referenced/summarized |
| Detailed Director technical logs | 14 days and 100 MB per Project |
| Process/external poll samples | Latest replaceable value only |
| Dirty-work recovery refs | Seven days by default |
| Daily TaskStore backups | Seven days by default |

A diagnostic bundle is generated only through a human action, is redacted, and is never uploaded automatically.

## 21. Testing strategy

### 21.1 Pull-request suite

- Go and TypeScript type/build checks, lint, and formatting.
- Director Engine domain unit tests for pure, versioned reducers.
- Exhaustive/model-based/property tests for all six decision reducers, all seven Agent outcome claims, transitions, malformed claims, missing/stale/contradictory facts, and dependency cycles (ADR-0018).
- Policy inheritance and overrides.
- Scheduler, 85% soft/100% hard budgets, reservations, acknowledgement revisions, and idempotency.
- Candidate-before-draft-before-sibling ordering, exactly one authoritative
  remote Linux CI, concurrent independent Review, adaptive `6/2/2` capacity,
  Worker pre-start visibility, event replay, and whole-batch correction
  behavior (ADR-0019).
- Every PreparationPlan timeout/failure/unknown route, including simultaneous per-step/aggregate/whole-plan ceilings and expiry without `preparation_ready`.
- Validation/interpretation/Review separation, failed-validation non-short-circuit, exact bindings, and mandatory review coverage for acceptance/specification, correctness, security, maintainability, readability, design, quality, and rigor.
- Local/external cadence and freshness tests, one-probe non-authority, compound stall detection, and full termination predicates.
- Engine-owned schema, Zod-validated Paseo plugin RPC, and generated-client drift checks across Go engine, TypeScript connector/UI RPC, host transport, and MCP.
- Responsive components.
- Linux path, process, and filesystem coverage.
- Deterministic fake provider with no paid model calls.

These additions are the exact ADR-0018 quality contract; deterministic checks never replace the independent Reviewer.

### 21.2 Main-branch integration

Tests use real disposable Git repositories/worktrees and the selected TaskStore. They cover:

- Director-owned product-worktree admission, creation, host registration/translation, ownership, recovery, and cleanup (ADR-0017);
- commits, branches, push, and recovery refs;
- multiwriter TaskStore behavior;
- backup, restore, and migrations;
- standalone engine, connector, and plugin interruption/reload at every effect boundary, including an in-flight connector call and resumable cursor;
- duplicate and out-of-order events;
- draft create/update recovery, remote-CI/Review ordering, and refusal of a
  second complete CI for one Candidate;
- base changes during validation/review/Ready;
- dirty worktrees and absent commits;
- disk pressure, offline systems, and expired credentials;
- no duplication of agents, PRs, merges, feedback, worktrees, connector observations, or outcome claims.

### 21.3 Release candidate

- Clean installation on exact supported Paseo `0.7.2`, including the disclosed full-daemon-operator connector authority and fail-closed missing-credential behavior (ADR-0017).
- Upgrade from the previous Director version.
- Release-mode version/source/digest/notices download and external-cache verification, development-mode Go compilation, attributable engine identity, and no cross-mode fallback.
- Real PR and CI in a GitHub sandbox repository.
- Manual smoke with compatible Codex, Claude Code, and OpenCode installations.
- Desktop, browser, and mobile UI.
- Pause, emergency stop, recovery, cleanup, backup, and restore.

Real models and external GitHub Actions run manually or for release candidates, not on every PR.

### 21.4 Validation scale

This is a test target, not an artificial product hard cap:

- 25 Workspaces per Project;
- 10,000 historical Tasks;
- 500 open Tasks;
- eight concurrent execution agents;
- paginated or virtualized Board/List.

`dir-m5.10` is the mandatory owner of this validation. These targets are not
an M0 evidence result or an M1 production-scale support claim.

### 21.5 Mandatory release blockers

No release may ship with a known defect that can:

- review/validate the wrong SHA;
- integrate incorrectly or twice;
- write in another Workspace;
- irreversibly lose dirty/unintegrated work;
- delete an unowned branch/worktree/ref/path;
- persist or log a secret;
- fail to recover a Run after restart;
- migrate without verified backup and restore.

## 22. Development process with Beads

After this plan is approved, development begins by initializing a new, clean Beads database in the plugin repository.

- Ignore all previous Beads implementation experiments.
- One Beads Epic per roadmap milestone.
- Development hierarchy remains `Epic → Task`.
- Every Task has explicit acceptance criteria, dependencies, risk, and evidence requirements.
- One normal top-level Paseo Task Agent owns one development Task and runs in that Task's isolated Execution Workspace/worktree. Director does not launch it through a planning context or agent-scoped subagent orchestration.
- A development Task Agent may use internal helper subagents, but they do not claim Tasks, own Beads records, replace the Task Agent, or become development Task owners.
- Every result is committed and independently reviewed at the exact SHA regardless of deterministic validation success or failure.
- The Task Agent uses the coordinator-native mode-`0600` handoff contract. Raw
  ownership input comes only from an owner-only file and never enters argv,
  public output, logs, PR content, or Beads (ADR-0019).
- The repository is clean before handoff.
- Beads records outcome, tests, review, and links.
- A spike hands off reproducible evidence and an ADR. When evidence obstructs a binding human-decided path, its scoped outcome is Inconclusive with the exact obstacle and owner escalation; an outcome label cannot reinstate a prohibited alternative.
- No milestone starts with an unresolved entry gate.

Initial development skills define commit, branch, PR, review, testing, and documentation rules before parallel implementation begins.

For Director's own repository, the Task Agent ends by handing off a Candidate claim; that claim never closes the Task. Independent exact-SHA review, CI, publication/integration, cleanup, and Task closure remain separately evidenced and engine/coordinator-owned (ADR-0018). After `approve_candidate`, the authorized coordinator may integrate automatically only when the remote head still equals the reviewed SHA, the merge operation atomically asserts that exact head, the relevant base remains valid, every configured and Task-required check passes, the PR is mergeable, no human feedback is unresolved, and the Task is not explicitly manual (ADR-0001 as amended by ADR-0010, with effect ownership consolidated by ADR-0018). A pre-merge refetch without an expected-head condition is insufficient. This repository workflow does not change Director's product default of manual merge described in section 14.

Additional human confirmation is reserved for accepting P2 residual risk, expanding policy or permissions, resolving an ambiguous/manual gate, rewriting public history, or performing a destructive action outside the Task's approved cleanup scope.

## 23. Implementation roadmap

### M0 — Spikes and irreversible decisions

Prove before building product functionality:

- plugin lifecycle and target stable Paseo version;
- agent/workspace creation, observation, interruption, reload, and recovery;
- per-session MCP for every admitted provider;
- TaskStore model, concurrency, sync, backups, and migrations on the approved
  bounded Linux topology;
- exact-SHA GitHub PR/CI/review behavior;
- Linux headless and worktree lifecycle;
- idempotent effect and reconciliation design;
- public licensing and distribution compatibility.

**Exit gate:** reproducible demonstration and ADR for every critical risk. M1 cannot begin with an unresolved stop condition.

### M1 — Walking skeleton

Build the smallest vertical path:

```text
Organizer repository/configuration → Task → pure Eligibility/Launch reducers
→ Director-owned worktree → connector-registered host view → Fake agent claim
→ Candidate admission → Durable state → Cleanup
```

Includes the standalone Go Director Engine scaffold, minimum TypeScript Director for Paseo UI/connector, engine-owned host contract/generated client, six reducer skeletons, closed claim schemas, deterministic PreparationPlan barrier, Director-owned worktree lifecycle, configuration schema, Create/Adopt Organizer, selected TaskStore, minimal Board/List, and fake-adapter restart reconciliation (ADR-0017 and ADR-0018).

**Exit gate:** interrupt every boundary without losing state or duplicating effects.

### M2 — Planning and scheduler

- Multiple Projects and Workspaces.
- Epics, Tasks, dependencies, priorities, labels, and filters.
- Project/Workspace/Task inheritance and overrides.
- Preview/Apply configuration.
- Manual/automatic launch and concurrency.
- Candidate fast-path ordering, adaptive `6/2/2` delivery lanes, event-driven
  completion wakes, and one authoritative remote CI concurrent with Review
  (ADR-0019).
- Registered launch visibility: every top-level Task Agent and Reviewer is
  recorded against its root workspace before it starts, and is refused a launch
  when that record cannot be written. See
  [Director Workers visibility contract](director-workers.md).
- Agreed planning scale.

**Exit gate:** complete deterministic planning and scheduler behavior with fake adapters.

### M3 — Agent execution

- Task Agent and Reviewer Agent profiles; planning contexts have no lifecycle authority.
- Scoped MCP.
- Closed Task Agent outcomes and external-fact reconciliation.
- Top-level Task Agent and helper-subagent lifecycle.
- Frozen Run configuration, event-driven agent completion wakes,
  30-second active external/5-minute idle reconciliation, the sole
  five-minute compound stall watchdog, and 85% soft/100% hard budgets.
- Pause/Resume/Cancel/Emergency stop.
- One top-level Task Agent replacement and recovery.

**Exit gate:** concurrent Runs and fault injection without duplicate or orphaned execution.

### M4 — Quality and delivery

- Exact-SHA Candidate review.
- Detached Reviewer checkout and structured verdict.
- Deterministic Validation, separate model failure interpretation, always-mandatory independent Review, and batched correction loops.
- Paseo/GitHub human feedback.
- Pull-request and direct delivery.
- Manual/automatic integration.
- Base-change invalidation and revalidation.
- Local/remote cleanup.

**Exit gate:** complete workflows against a real GitHub sandbox, including failure and recovery.

### M5 — UX and operations

- Final Director Home, Board/List, Task modal, and Inspector.
- Desktop/web/mobile layouts and accessibility.
- Needs-you routing.
- Doctor, Repair, Sync, health, audit, and diagnostics.
- Backups, migrations, retention, disk-pressure behavior, and security hardening.
- Performance and platform validation, including the TaskStore agreed-scale
  proof owned by `dir-m5.10`.

**Exit gate:** complete Linux suite, the `dir-m5.10` scale proof, and every
agreed acceptance scenario.

### M6 — Public release

- Git installation/update lifecycle.
- Pinned release-download/development-compile Director Engine distribution, notices, identity, and exact-0.7.2 connector authority disclosure.
- Stable Paseo compatibility declaration.
- Public user, operator, and developer documentation.
- Example Organizer.
- Changelog, Apache-2.0 license, security policy, and support guide.
- Internal alpha, public beta, stabilization, and `1.0.0`.

**Exit gate:** all stable criteria below.

## 24. Definition of Done and release policy

### 24.1 Development Task Definition of Done

A Task is Done only when:

- it meets its acceptance criteria;
- tests are proportional to risk;
- the worktree is clean;
- the result is committed;
- independent review approves the exact SHA;
- findings are resolved;
- relevant documentation/schema/migrations are updated;
- CI is green;
- Beads contains evidence and links;
- owned temporary resources are clean according to policy.

The Task Agent may claim a Candidate or any closed outcome but cannot decide closure. The Director Engine/coordinator Closure reducer must reconcile every current acceptance, Candidate/base, Validation, independent Review, CI/feedback, integration/deployment, cleanup/ownership, and blocker fact before appending the audited closure (ADR-0018).

### 24.2 Milestone Definition of Done

- Every blocking child Task is Done.
- The milestone's vertical path works end to end.
- Unit, integration, recovery, and platform gates pass.
- It works from a clean installation.
- Documentation and behavior agree.
- No hidden manual migration or temporary production step remains.
- No unrecorded unknown blocks the next milestone.

### 24.3 Severity

| Severity | Meaning | Policy |
|---|---|---|
| P0 | Data loss, secret exposure, or incorrect merge/integration | Stops every release |
| P1 | Duplicate effect or broken core workflow without a safe workaround | Blocks release |
| P2 | Degraded behavior with a safe workaround | Requires explicit acceptance and documentation |
| P3 | Minor or cosmetic defect | May be scheduled later |

No P0 is accepted as ordinary technical debt.

### 24.4 Alpha

- Installable walking skeleton.
- Basic persistence and recovery.
- Internal use only.
- No data-compatibility promise between builds.

### 24.5 Public beta

- `1.0` scope is feature-complete.
- Backups and migrations are operational.
- Linux gates are green.
- Installation, upgrade, and diagnostics are documented.
- No known P0/P1; P2 only when explicit and documented.

### 24.6 Stable `1.0.0`

Requires:

- zero known P0/P1;
- 14 consecutive beta days without a critical incident;
- at least 20 complete real workflows;
- several Projects and multi-repository cases;
- verified upgrade from the previous beta;
- verified restore of a real backup;
- real PR/direct/review/CI/feedback/cleanup flows;
- the agreed scale target;
- a declared compatible Paseo API range;
- complete public documentation, changelog, license, and security policy.

Director follows Semantic Versioning. `0.x` may change contracts; `1.x` guarantees supported configuration and data compatibility. A later incompatible change requires migration and a major version.

## 25. Risk register and stop conditions

| Risk | Primary prevention | Permitted contingency |
|---|---|---|
| Paseo plugin API changes | Fixed engine-owned host contract, thin connectors, and exact compatibility bounds | Evidence and an explicit connector-only compatibility decision; never move policy into a connector |
| Operation absent from the public SDK | Proof before depending on it | Inconclusive with the exact obstacle and owner decision; never silently reverse a binding architecture |
| Exact-0.7.2 connector credential is over-broad | Connector-only storage/non-propagation, descriptor/fail-closed checks, and pre-install P2 disclosure | Narrow only after a supported scoped mechanism is evidenced; no engine credential or in-connector engine fallback |
| Direct Dolt TaskStore is unhealthy or drifts | ADR-0004 safe-mode/identity checks and ADR-0012 backup, restore, migration, and partial-sync reconciliation | Pause/Degrade and repair the selected store; never switch stores silently |
| MCP is incomplete in a provider | Capability detection and provider-specific proof | Exclude it with an exact explanation |
| Duplicate, missing, or out-of-order events | Durable commands and idempotency | Reconcile from persisted/external facts |
| Model or connector claim conflicts with facts | Closed schemas and pure versioned reducers | Reject/park and observe; never project from narration |
| Agent leaves dirty/no-commit work | Engine-owned Git verification | Correct or route to Needs you |
| Base/CI/review/merge race | Candidate/base SHA binding | Invalidate and revalidate |
| Crash during push/PR/merge | Intent before effect and observed result after | Inspect remote before retrying |
| Disk growth | Retention, ownership, cleanup, and 10% threshold | Stop launches and clean first |
| OS/tool differences | Portable APIs and capability-based Doctor | Disable only the missing capability |
| Secret in durable output | Central redaction and adversarial tests | Stop workflow and treat as P0 |
| GitHub/network/auth outage | Backoff and Degraded state | Park; never change delivery silently |
| Engine/connector contract or capability drift | Version/hash exchange, fixed vocabulary, and generated-client CI | Fail before mutation; no transport or policy fallback |

M0 stops and M1 cannot begin until public, supported mechanisms prove that Director can:

- create, observe, recover, and terminate top-level Task Agents and Reviewer Agents, and observe, account for, contain, and clean Task-Agent-created helpers;
- deterministically admit, create/adopt, own, register as host views, and clean product worktrees;
- inject per-session MCP into admitted providers;
- correlate Project/Task/Run/Workspace/agent identities;
- bind review, CI, publication, and integration to exact SHAs;
- recover after plugin shutdown at every critical effect boundary;
- persist and synchronize the selected TaskStore;
- restore a verified backup.

Forbidden shortcuts:

- undocumented Paseo endpoints;
- direct writes to Paseo's internal database;
- UI click automation as product infrastructure;
- log parsing as a source of truth;
- silent model/delivery/permission fallbacks;
- domain logic with OS-specific hacks.

Every risk becomes a Beads Task with an owner, evidence, state, and ADR. Missing or contradictory facts fail closed through the applicable pure reducer. When a critical capability blocks a binding human-decided path, the result is Inconclusive with its exact obstacle and owner escalation; neither an ADR label nor a model selects a prohibited fallback (ADR-0018).

## 26. Post-`1.0` roadmap

Ordered candidates, subject to new planning:

1. Field-owned synchronization with GitHub Projects.
2. Periodic discovery/ingestion of GitHub Issues.
3. Additional forges.
4. Multi-daemon execution and Project migration.
5. Teams and RBAC.
6. Richer native notifications if Paseo exposes the necessary contribution.

The standalone Go Director Engine is mandatory `1.0` architecture under ADR-0017 and is therefore not a post-`1.0` candidate awaiting justification.

Future GitHub synchronization will retain a portable model close to GitHub without making it the `1.0` source of truth. Intended ownership:

- GitHub owns externally collaborative planning fields.
- Director owns execution fields and Run state.
- Conflicts are resolved through field ownership, never whole-record last-write-wins.

## 27. External facts M0 must revalidate

These facts were true when the plan was written and are not permanent assumptions:

- Paseo `0.7` documentation is marked current/stable.
- `0.8` documentation is marked preview and introduces incompatible client/server entrypoints.
- The public plugin API remains experimental.
- The plugin server runs trusted, unsandboxed Node code on the daemon.
- Plugin UI uses React Native surfaces on desktop/web/mobile.
- Plugin storage and general native navigation, hierarchy, and notification contributions are limited or absent.
- The current SDK exposes Project/Workspace/Agent/provider operations and per-session MCP.
- GitHub CLI, Beads, and Dolt publish builds for the supported Linux topology.

M0 must reread the deployed official documentation and test the installed stable release before creating Director's scaffold.

Primary references:

- <https://paseo.sh/docs/plugins>
- <https://paseo.sh/docs/plugins/v0.7/reference>
- <https://paseo.sh/docs/plugins/v0.8/migration>
- <https://paseo.sh/docs/sdk/reference>
- <https://paseo.sh/docs/worktrees>
- <https://paseo.sh/docs/security>
- <https://github.com/gastownhall/beads>
- <https://github.com/dolthub/dolt>
- <https://github.com/cli/cli>

## 28. Final approval gate

Approving this document authorizes only the next planned actions:

1. Commit the approved plan.
2. Initialize a fresh development Beads database.
3. Materialize M0–M6 as Epics and dependency-ordered Tasks.
4. Define repository development skills and the contribution workflow.
5. Begin M0 evidence-gathering spikes.

Approval does not authorize skipping M0, starting later milestones, weakening review, or accepting a stop condition without an ADR and explicit human decision.
