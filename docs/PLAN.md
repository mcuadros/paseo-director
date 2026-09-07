# Director for Paseo — Product and Engineering Plan

- **Status:** Approved
- **Plan version:** 0.3
- **Last updated:** 2026-09-06
- **Approved:** 2026-09-06
- **Amended by:** [ADR-0010](adr/0010-top-level-task-agent-parentage.md) for top-level Task Agent and Reviewer Agent parentage; [ADR-0011](adr/0011-linux-only-platform-scope.md) for the Linux-only `1.0` platform scope; [ADR-0014](adr/0014-practical-linux-agent-boundary.md) for the human-approved practical trusted-provider Linux boundary; [ADR-0016](adr/0016-defer-taskstore-scale-proof-to-m5.md) for deferring TaskStore agreed-scale proof to `dir-m5.10`; [ADR-0017](adr/0017-standalone-engine-connector-authority-boundary.md) for the standalone Go engine and unresolved Paseo connector authority boundary; [ADR-0018](adr/0018-deterministic-coordination-boundary.md) for deterministic coordination decisions and structured agent outcome claims
- **Plugin repository:** <https://github.com/mcuadros/paseo-director>
- **Public name:** Director for Paseo
- **Short UI name:** Director
- **Plugin ID:** `director`

This document consolidates the product, architecture, workflow, quality, and release decisions approved during the design sessions. It is normative: implementation must follow it unless a later Architecture Decision Record (ADR) explicitly changes an approved decision.

This plan is approved. Work may proceed only through the authorization in section 28 and the milestone gates defined below.

## 1. Product intent

Director is a public Paseo plugin for planning, executing, reviewing, and delivering medium-to-large software products that span one or more repositories.

It adds a durable project-management and execution layer around Paseo:

- One Project can coordinate several repositories.
- A persistent Organizer Agent can discuss the complete Project, administer Tasks, launch work, and report progress.
- A Board/List surface exposes planning and execution state together.
- Director launches exactly one normal top-level Paseo Task Agent for each launched Task—one for its active Run—in an isolated Execution Workspace. That Task Agent may create optional helper subagents.
- Project policy governs models, effort, permissions, review, delivery, CI correction, cleanup, budgets, and concurrency.
- Every execution is tied to exact Git commits and is recoverable after a plugin or daemon interruption.
- Configuration, skills, templates, decisions, and dynamic task state are durable and auditable.

The design goal is simple operation backed by rigorous invariants. The UI should not expose internal complexity unless the user needs it to make a decision or repair a failure.

## 2. Core terminology

Paseo and Director use some similar terms. The following definitions are authoritative within this plan.

### 2.1 Director Project

The product being managed. It contains one or more canonical source-code repositories and exactly one Organizer.

A Director Project is not the same abstraction as a native Paseo Project. Native Paseo resources remain the execution substrate.

### 2.2 Workspace

Within Director, a Workspace represents exactly one canonical source-code repository, not a local checkout and not an individual Task.

Several Tasks and agents may work concurrently against the same Workspace through isolated execution checkouts.

### 2.3 Execution Workspace

A native Paseo workspace created for a Task. It is normally backed by a Paseo-managed Git worktree. It is technical execution state and remains visible through native Paseo.

### 2.4 Organizer

Every Director Project owns exactly one dedicated Organizer Git repository and one persistent Organizer Agent.

The Organizer:

- is not a Task;
- does not represent a product repository;
- is administered by Director and the Organizer Agent;
- stores configuration and project metadata;
- contains or references the dynamic TaskStore;
- is the stable place from which the user discusses the complete Project.

### 2.5 Epic, Task, Run, and Candidate

- **Epic:** an optional grouping of Tasks and the only grouping level.
- **Task:** one unit of work targeting exactly one Workspace.
- **Run:** one launched execution of a Task with frozen configuration.
- **Candidate:** one immutable exact commit SHA produced by a Run.

A correction that changes the commit creates a new Candidate in the same Run. A true relaunch or retry creates a new Run.

### 2.6 Task Agent, helper subagent, and Reviewer Agent

- **Task Agent:** the one normal top-level Paseo agent Director launches to own a Task Run. It has no parent agent, its visible title is the exact Task title, and it runs in that Task's isolated Execution Workspace.
- **Helper subagent:** an optional internal orchestration agent that the Task Agent may create for complex work. It is not a Task, Task owner, Run, or Candidate producer of record and is never launched by the Director scheduler as Task work.
- **Reviewer Agent:** a normal top-level Paseo agent Director launches independently for exact-SHA review in a detached disposable checkout. It is never a child or helper of an Organizer or Task Agent.

The Task Agent remains the sole Task owner even when helpers contribute. Several Task Agents may run concurrently against the same Director Workspace/repository only through distinct Execution Workspaces.

## 3. Scope and non-goals

### 3.1 Scope for `1.0`

- Single-user operation.
- Multiple Director Projects on one Paseo daemon.
- One active daemon/executor for each Director Project.
- One or more Git repositories per Project.
- One persistent Organizer Agent per Project.
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
- A Go orchestration sidecar.
- Replacing or extending native Paseo hierarchy and settings screens.
- Using GitHub pull-request comments as an agent-to-agent message bus.

Generic Git remotes remain valid for direct delivery where the required authentication and operations work. Forge-aware `1.0` behavior is GitHub-specific.

## 4. Non-negotiable invariants

The implementation must make the following states impossible or stop safely when it cannot prove them:

1. A Task targets exactly one Workspace.
2. One active Run owns at most one top-level Task Agent, one Task branch, and one active pull request.
3. A review always evaluates an exact committed Candidate SHA.
4. A dirty worktree can never enter review or Ready.
5. Any commit change invalidates previous review and validation for readiness purposes.
6. A relevant base-branch change invalidates Ready and forces revalidation.
7. Only the engine performs Director lifecycle side effects such as top-level Task Agent and Reviewer Agent creation, push, PR creation, merge, integration, and cleanup. A Task Agent may create only optional helper subagents within its frozen Run policy; helpers cannot perform Director lifecycle effects or become Task owners.
8. Every side effect is idempotent and reconciled against external facts before retry.
9. No Task Agent, Reviewer Agent, helper subagent, PR, merge, prompt, or workspace is duplicated after recovery.
10. Unknown or unowned branches, worktrees, refs, and directories are never deleted.
11. Dirty or unintegrated work is never destroyed without the configured recovery action.
12. A model, delivery, permission, or provider fallback is never chosen silently.
13. The Organizer cannot expand its own model, effort, permission, repository, or delivery authority.
14. Secrets never enter Organizer Git, TaskStore records, prompts, audit payloads, or support bundles.
15. Project state is derived from durable facts rather than editable Kanban columns.

Violation of any invariant affecting data, security, isolation, or integration is a release-blocking defect.

## 5. Domain model

### 5.1 Aggregate relationship

```text
Project
├── Organizer (exactly one)
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
- Organizer identity and repository;
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

The product is single-user and does not introduce RBAC. Audit records use only three actor types: `human`, `organizer`, and `system`.

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
- the Organizer commit and hashes of every skill/template used;
- the base ref and resolved base SHA;
- Task Agent and Execution Workspace identity;
- configured budgets and current consumption;
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

`1.0` is a modular TypeScript monolith, not a set of microservices.

```text
Paseo desktop / web / mobile clients
                  │ typed plugin RPC
                  ▼
┌──────────────────────────────────────────────────────┐
│ Director plugin server — one process per daemon      │
│                                                      │
│ Application API                                      │
│ Domain rules and policy engine                       │
│ Scheduler and workflow engine                        │
│ Reconciler, preflight, cleanup, diagnostics          │
│                                                      │
│ Adapters: Paseo · Git · GitHub · TaskStore · clock   │
└──────────────────────┬───────────────────────────────┘
                       │
              Organizer Git + TaskStore
                       ▲
                       │ durable scoped commands
Agent provider ─ stdio MCP bridge
```

There is no additional public HTTP server and no Go sidecar in `1.0`.

### 6.2 Module boundaries

- **Domain:** pure entities, value objects, invariants, state projection, scheduling, and policies.
- **Application:** commands, queries, use cases, orchestration, and transaction boundaries.
- **Client:** React Native surfaces and panels.
- **Plugin server:** Paseo RPC handlers, lifecycle, reconciliation, and adapter wiring.
- **Shared contracts:** Zod schemas and JSON-safe values shared across client/server/MCP boundaries.
- **Adapters:** Paseo SDK, Git processes, GitHub CLI/API, TaskStore, filesystem, clock, disk, and process execution.
- **MCP bridge:** a scoped façade for Organizer, Task Agent, helper-subagent, and Reviewer Agent roles.

Dependencies point inward. Domain code cannot import Paseo, React, GitHub, Beads, Dolt, Node process APIs, or filesystem APIs.

### 6.3 Effect ownership

The UI and MCP endpoints submit commands. The engine is the sole executor of Director lifecycle effects. A Task Agent's optional creation of helper subagents is the only agent-creation exception: Director authorizes the frozen Run envelope, reserves and reconciles capacity, observes the helper identities, and owns containment and cleanup, but the scheduler and engine do not launch those helpers as Tasks.

Each effect follows an intent/evidence pattern:

1. Validate policy and expected aggregate version.
2. Persist a uniquely keyed intent.
3. Inspect current external facts.
4. Perform the effect only when absent.
5. Persist the observed result.
6. Project the new state.

This applies to top-level Task Agent and Reviewer Agent creation, prompting, workspace creation, branch creation, push, PR creation, merge/integration, remote-branch deletion, and local cleanup. Helper creation must use an admitted agent-scoped mechanism that binds every helper to the creating Task Agent's fixed Project/Task/Run scope, reserves capacity under a per-Run idempotency key, and records the observed helper identity before helper work so accounting and recovery never rely on a blind retry.

### 6.4 MCP architecture

- Agents receive a session-scoped custom Director MCP in addition to the applicable built-in Paseo MCP.
- Support is capability-detected; Director only offers providers that support the required per-session MCP configuration.
- The agent-facing transport is `stdio`.
- Each bridge is fixed to one Project, Task, Run, role, and capability scope.
- Tools do not accept arguments that let an agent select another scope.
- MCP writes durable commands through shared application contracts.
- Agents never receive raw Beads/Dolt access.
- The bridge contains no independent workflow policy.

The exact bridge-to-engine mechanism is an M0 spike, but it must preserve effect ownership and idempotency.

### 6.5 Execution leases

Only one engine may hold the execution lease for a Project in `1.0`. Lease acquisition and renewal are durable and transactional. A replacement engine waits for expiry and reconciles before producing effects.

## 7. Organizer repository

### 7.1 One Organizer per Project

The Organizer is a dedicated Git repository. It may use:

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
2. Director validates schema, semantics, paths, references, policies, and required capabilities.
3. The UI presents the exact diff and impact.
4. A human confirms `Apply`.
5. If the proposal is not committed yet, Apply creates exactly one logical commit containing only the previewed files.
6. Director records the exact SHA as the active revision.
7. Only future Runs receive the new revision.

The Organizer Agent may edit and commit proposals but cannot activate them. The active-revision pointer lives in dynamic Project state to avoid self-referential commits.

Decisions are superseded through a new record rather than silently rewriting history.

### 7.5 Precedence

```text
Project defaults → Workspace overrides → Task overrides
```

The UI presents each override as `Inherit` or a concrete value. It previews the effective configuration before launch, and that configuration is frozen into the Run.

Overrides cannot exceed the human-approved security and policy envelope. A one-off human override requires explicit audited confirmation.

### 7.6 Product repositories

Bootstrap does not add, edit, or commit files in product repositories. Director operates through existing checkouts, Git, Paseo workspaces, and Organizer configuration.

## 8. TaskStore and persistence

### 8.1 Abstraction

The domain depends on a `TaskStore` port, never on Beads commands or internal storage layout.

The initial candidate is Beads backed by Dolt. Any previous Beads implementation experiments are ignored; M0 starts from a clean design and current Beads behavior.

M0 must prove that the mapping safely supports:

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

If Beads cannot meet the contract cleanly, an alternative is selected before M1. One possible fallback is a single direct Dolt schema behind the same port. `1.0` ships one runtime TaskStore, not two interchangeable engines.

This runtime decision is independent from using Beads to manage Director's own development.

### 8.2 Data ownership

| Data | Canonical owner |
|---|---|
| Configuration, skills, templates, specs, decisions | Organizer Git |
| Epic, Task, Run, Candidate, commands, audit | TaskStore |
| Complete agent conversations and timelines | Paseo |
| Complete PRs, reviews, and CI logs | GitHub |
| Commits, branches, and source code | Product Git repositories |

Director stores references and structured summaries instead of duplicating conversations, PR threads, or complete CI logs.

### 8.3 Synchronization

When the Organizer has a remote, the UX presents one `Sync` action with two explicit results:

1. Normal Git synchronization for Organizer files.
2. Beads/Dolt synchronization for dynamic state.

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

Columns are a semantic projection, not editable states. Because review/PR order is configurable, a Task may return to a phase or traverse validation and review in a different order. The Board reflects facts instead of inventing a false visual sequence.

### 9.2 Needs you

`Needs you` is reserved for actionable human intervention:

- a permission request;
- an exhausted correction or replacement budget;
- an irreconcilable Git state;
- a configuration or credential decision;
- a policy-override request;
- ambiguous recovery.

A normal dependency wait remains in `Queued` and shows its blocker.

### 9.3 Derived state

Task state is projected from durable facts:

- launch eligibility;
- active agent turn;
- worktree and Candidate state;
- reviews and validations for the current Candidate;
- publication and integration;
- pending human input;
- terminal outcome.

The user cannot drag a card to claim that CI or review occurred. Human interventions are explicit audited commands.

## 10. Scheduler and launch policy

### 10.1 Launch configuration

Project launch policy is `manual` or `automatic`. Each Task can inherit it or apply a permitted `manual`/`automatic` override.

The scheduler only launches when:

- the Project is active;
- policy permits launch or a human requested `Launch now`;
- the Organizer revision is approved;
- dependencies are satisfied or explicitly overridden;
- the Task has no active Run;
- preflight passes;
- concurrency capacity is available;
- time, cost, and CI budgets permit launch.

For each eligible Task, the scheduler asks the engine to create the Task Agent through the top-level Paseo client API. It never launches Task work from the Organizer Agent or another agent's subagent API, and it never supplies a parent agent.

### 10.2 Ordering

Scheduler order is:

1. Progress an already-started Run that needs Reviewer, correction, validation, or integration capacity.
2. Human `Launch now` requests.
3. Eligible Tasks by `urgent`, `high`, `normal`, and `low` priority.
4. FIFO by `queuedAt` within the same priority.

There is no manual rank or queue drag-and-drop in `1.0`.

`Launch now` does not interrupt agents and does not exceed hard safety, disk, cost, CI, or capacity limits. Ignoring a dependency requires a separate explicit confirmation.

### 10.3 Default capacity

- `maxActiveTasks`: 4.
- `maxActiveTasksPerWorkspace`: 2.
- `maxConcurrentAgents`: 8.
- `maxSubagentsPerTask`: 3.

The agent limit includes top-level Task Agents, top-level Reviewer Agents, and helper subagents created by Task Agents. The persistent Organizer Agent does not count and remains available to administer the Project. Helper subagents also consume the creating Task's helper quota, time/cost budget, and any applicable Workspace or provider capacity.

## 11. Preflight

Preflight is mandatory and blocking before any automatic launch. It validates:

- approved and valid Organizer configuration;
- a complete and consistent Task;
- Workspace checkout, identity, remote, and base branch;
- Git worktree capability and absence of conflicting ownership;
- disk threshold and cleanup health;
- TaskStore availability and transactional writes;
- selected provider, model, effort, and mode availability;
- support for the required custom MCP;
- permission/sandbox compatibility;
- Git/GitHub authentication when applicable;
- the Project execution lease;
- Project, Workspace, Task, agent, time, cost, and CI capacity.

A failed preflight does not launch partially. It returns a specific cause and only uses `Needs you` when human intervention is actually required.

## 12. Agent profiles and authority

### 12.1 Profiles

Each Project defines separate profiles for:

- Organizer;
- Task Agent (`Worker` role);
- Reviewer Agent (`Reviewer` role).

A profile includes provider/model, effort or thinking option, operating/permission mode, provider-native options, MCP policy, budgets, and an optional ordered fallback chain.

Fallback chains are empty by default. Every fallback must be declared and ordered explicitly.

### 12.2 Organizer authority

The Organizer Agent can:

- inspect Project, Workspace, Epic, Task, and Run state;
- discuss progress and risks;
- create and update planning work;
- submit permitted operational and launch commands;
- prepare configuration and metadata proposals.

It cannot activate configuration or change its own model, effort, permissions, repositories, delivery, or security authority. It can only propose those changes for human Preview/Apply.

The Organizer receives broad Project-administration tools within the approved envelope. Task Agents receive least-privilege Task/Run tools. Reviewer Agents receive read-only Candidate/review access plus one operation for submitting a structured verdict.

### 12.3 Task Agent and helper subagents

- Exactly one normal top-level Task Agent corresponds to one active Task Run. The Director engine creates it as a top-level client, omits the parent field, sets its visible title to the exact Task title, and places it in the Task's isolated Execution Workspace.
- The Organizer and scheduler never create a Task Agent as a child or subagent. Concurrent Tasks against one Director Workspace/repository use distinct Execution Workspaces.
- The Task Agent may create helper subagents when it considers them useful. Those helpers are internal to its Run and never become Task records, Task owners, separate Runs, or Director-launched work.
- Writer helpers always use isolated checkouts and return commits to the Task Agent. Read-only helpers may share the checkout only when provider and permissions make it safe.
- The default maximum is three helper subagents per Task. Director reserves and reconciles their capacity, identities, and budgets, requires fixed Project/Task/Run scope and containment, and includes them in interruption, recovery, and cleanup without becoming their launcher. An unknown helper-creation result parks rather than being retried blindly.
- The mandatory top-level Reviewer Agent does not consume the helper quota, but it does consume global agent capacity.

### 12.4 Reviewer profile

The Reviewer Agent may use the same model as the Task Agent, with a warning. `requireDifferentReviewerModel` is optional and disabled by default.

## 13. Execution workflow

### 13.1 Workspace and branch ownership

For every Run, Director:

1. resolves and records the exact base SHA;
2. creates or adopts one owned Task branch;
3. persists a uniquely keyed workspace-creation intent, creates an isolated Paseo-managed Execution Workspace/worktree, and records the returned workspace ID before another effect;
4. acting as a top-level Paseo SDK client, persists a uniquely keyed Task Agent creation intent and creates exactly one normal top-level Task Agent in that workspace with the frozen profile, policy, skills, templates, and MCP;
5. omits the parent field from the agent-creation request, sets the visible agent title to the exact Task title without prefixes or suffixes, and records the returned Paseo agent ID before prompting or any later effect.

One Task has at most one active branch and one active pull request.

### 13.2 Candidate production

The Task Agent must produce a commit. Director independently inspects the worktree and Git graph.

- A dirty worktree cannot be reviewed.
- An absent or unreachable commit cannot be reviewed.
- A Candidate is only recorded after proving its SHA and ownership.
- A correction with a different SHA creates a new Candidate.

### 13.3 Independent review

The engine always creates the Reviewer Agent as a normal top-level Paseo agent with the parent field omitted; this is not left to the Task Agent. The Reviewer Agent is independent of the Organizer and Task Agent, and its identity is persisted before it receives a prompt or performs review work.

The Reviewer Agent:

- receives the objective, acceptance criteria, relevant policy, and Candidate/base facts;
- does not receive the Task Agent's conversation history;
- uses a detached, disposable checkout of the exact Candidate;
- cannot mutate the Task Agent's worktree;
- returns a structured verdict and findings.

Independent review occurs before opening a public pull request by default. An explicit Project/Task setting may publish first and review afterward.

Changing the Candidate invalidates the previous verdict for readiness purposes.

### 13.4 Validation and correction

`autoFixCiFailures` and `autoFixReviewFeedback` are enabled by default and can be inherited/overridden by Project/Task.

Defaults:

- three correction attempts;
- four total CI cycles: the initial cycle plus three corrections;
- never automatically rerun the same failed commit;
- batch related findings and comments before requesting a correction;
- corrections are performed by the Task Agent;
- every new commit requires fresh review and validation.

Every Run has a mandatory time limit, an optional cost limit, and an explicit CI budget. Exhaustion sends the Task to `Needs you` instead of continuing indefinitely.

### 13.5 Feedback

Human feedback can arrive through:

- a direct Paseo message/action associated with the Task;
- a GitHub PR review or comment.

Direct Paseo feedback reopens the Task. GitHub feedback also reopens current work when it is not Done and is routed to the same Task Agent when automatic correction is enabled.

Agents do not converse through GitHub, publish agent-authored review discussions, or automatically resolve human threads.

Feedback received after Done creates a new linked Task and does not mutate completed history.

## 14. Delivery and integration

### 14.1 Delivery modes

Only two modes exist in `1.0`:

- `pull_request`;
- `direct`.

Director creates, supervises, and verifies branches, pushes, PRs, integration, and cleanup. An agent's textual claim is not evidence that an effect occurred.

### 14.2 Pull request

- Director pushes the Task branch and opens at most one PR.
- Publication occurs after independent review by default; an explicit policy can publish earlier.
- Director observes GitHub CI and human feedback against the current Candidate.
- Manual merge is the default.
- Automatic merge is configurable by Project/Task.
- Automatic mode requires independent review and configured CI gates but no human approval.
- The engine performs final merge/integration.

### 14.3 Direct

- Director validates the Candidate and base according to policy.
- In manual mode, Ready waits for a human integration action.
- In automatic mode, the engine integrates and pushes to the remote target branch.
- Direct is never an implicit fallback for failed or unavailable PR delivery.

### 14.4 Base changes

A relevant base change while a Task is Ready invalidates readiness. Director updates/rebases according to configured Git policy and repeats review/validation against the resulting Candidate.

### 14.5 Remote branch cleanup

After verified integration, Director automatically deletes a remote Task branch only when it proves that:

- Director created/owns the branch;
- the exact Candidate is integrated;
- no active Task/PR still needs it;
- policy permits deletion.

This behavior is configurable by Project/Task and enabled by default.

## 15. Pause, cancellation, recovery, and cleanup

### 15.1 Pause

`Pause Project` prevents new launches, retries, Reviewers, PR publication, and integration.

An active agent may finish its current turn. The Task parks at the next safe boundary without killing the process or deleting the worktree.

`Resume Project` reconciles all facts before continuing and does not repeat completed effects.

### 15.2 Emergency stop

`Emergency stop`:

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

Director automatically attempts at most one replacement top-level Task Agent after a recoverable failure and only after the previous Task Agent is no longer active. The engine uses the same no-parent creation contract and exact Task title, persists the replacement ID before further effects, and supplies durable Task/Run/Candidate facts rather than an assumed conversation transcript.

If the replacement fails or recovery is ambiguous, the Task enters `Needs you`.

### 15.5 Cleanup defaults

- `terminateOnCompletion`: `true`.
- Task completion, cancellation, and replacement reconcile and terminate helper subagents before removing an owned Execution Workspace.
- Successfully integrated worktrees are removed automatically.
- Dirty work from failed/cancelled Runs uses `snapshot_then_delete` by default.
- The snapshot creates a hidden local Git ref tied to Task/Run/Candidate metadata.
- Recovery retention defaults to seven days.
- Failure/cancellation cleanup is configurable by Project/Task.
- Only material proven to be Director-owned is deleted.

Below 10% free space on the relevant filesystem, launches stop and cleanup runs first. If the threshold remains violated, the Project becomes degraded/Needs you and consumes no more disk.

The system prioritizes cleanliness without sacrificing recoverability.

## 16. User experience

### 16.1 Surfaces

Director contributes:

- a global Director Home sidebar surface;
- the Project Board/List in Organizer context;
- a Task Inspector in workspace/agent context;
- Command Center actions for common operations;
- plugin-owned timeline items where useful and supported.

The interaction model is inspired by the supplied AO screenshots—a dense dark Board, compact cards, and explicit execution detail—but adapts them to Paseo theme tokens, native plugin boundaries, accessibility, and compact/mobile layouts instead of copying them literally.

The persistent Organizer Agent remains a normal Paseo agent tab.

Director hierarchy lives in its own UI. Technical Paseo workspaces and agents remain visible. A Task Agent's visible title is exactly its Task title; other resources receive clear names linking them to Project/Task/Run.

### 16.2 Director Home

Home provides:

- `Create Project`;
- `Adopt Organizer`;
- health and active-work summaries by Project;
- quick access to Board, Organizer Agent, Doctor, Sync, Pause, and Needs-you items.

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

Nothing is applied before human confirmation.

### 17.2 Adopt Organizer

Adoption:

1. selects or clones the Organizer on the daemon;
2. validates its marker, schema, Git state, and TaskStore;
3. validates every Workspace path/remote;
4. displays unresolved capabilities;
5. only activates after Preview/Apply.

### 17.3 Removal

Removing or archiving a Project never physically deletes the Organizer, product repositories, TaskStore history, or unknown worktrees. Cleaning proven ephemeral resources is a separate previewed action.

## 18. Security and credentials

Paseo plugins are trusted, unsandboxed code on the daemon host. Director states this during onboarding. Its protections are defense in depth and policy enforcement, not a claim of operating-system isolation.

### 18.1 Credentials

- Git uses the host's existing credential mechanism.
- GitHub uses the existing authenticated `gh` session.
- Provider authentication remains owned by Paseo and provider CLIs.
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

The Organizer operates within this envelope and may propose changes. Only human Preview/Apply can expand it. One-off human overrides are explicit and audited.

### 18.3 Process and Git safety

- Executables are spawned with argument arrays; untrusted values are never interpolated into shell strings.
- Paths are canonicalized and verified before mutation.
- Repository and remote identity are verified before Git effects.
- Target/protected branches are never force-pushed.
- Refs, branches, worktrees, and paths are never deleted without ownership proof.
- Provider sandbox options are useful constraints, not a host boundary.

## 19. Platform, runtime, and distribution

### 19.1 Paseo compatibility

Director targets the latest stable Paseo plugin API available at implementation/release time.

At the time of this plan:

- Paseo `0.7` is the current stable API.
- Paseo `0.8` is a preview with explicit client/server entrypoints and incompatible packaging.

Director does not publish production builds against a preview API. Source maintains strict client/server/shared boundaries so migration is mechanical. If incompatible stable generations must coexist, they receive separate release lines rather than a fragile hybrid entrypoint.

### 19.2 Language

`1.0` uses TypeScript for:

- React Native UI;
- plugin server;
- Domain/Application layers;
- shared Zod contracts;
- custom MCP bridge.

The plugin entry must follow Paseo's TypeScript/Node contract. A Go engine is postponed unless later evidence demonstrates a concrete reliability, performance, or standalone-headless benefit.

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
- Node path/temp/process APIs and direct argv execution;
- correct canonical-path, symlink, permission, and locked-file handling;
- MCP over `stdio`;
- Linux worktree-cleanup tests.

### 19.4 Public distribution

- Public repository: `mcuadros/paseo-director`.
- Installation/update through Paseo's Git plugin lifecycle.
- No automatic download of undeclared external binaries.
- Doctor explains missing prerequisites and how to install them.
- No telemetry.
- Apache-2.0 license, subject to M0 legal/distribution compatibility verification with the Paseo SDK.
- Channels: internal alpha, public beta, and stable.

## 20. Observability, reconciliation, and retention

### 20.1 Reconciliation

The engine is event-driven and uses periodic reconciliation as a safety net.

- Immediate wake on Paseo events and UI/MCP commands.
- Approximately 30-second checks while Runs are active.
- Lower frequency while idle.
- Exponential backoff for unavailable external systems.
- Full reconciliation at startup/reload before resuming effects.
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

### 20.3 Retention

| Information | Retention |
|---|---|
| Organizer configuration/metadata | Permanent Git history |
| Essential Task/Run/audit facts | Permanent and logically archivable |
| Agent conversations | Paseo-owned; not duplicated |
| Complete PR/CI data | GitHub-owned; referenced/summarized |
| Detailed Director technical logs | 14 days and 100 MB per Project |
| Heartbeats/poll samples | Latest replaceable value only |
| Dirty-work recovery refs | Seven days by default |
| Daily TaskStore backups | Seven days by default |

A diagnostic bundle is generated only through a human action, is redacted, and is never uploaded automatically.

## 21. Testing strategy

### 21.1 Pull-request suite

- Typecheck, lint, and formatting.
- Domain unit tests.
- Exhaustive/model-based/property tests for transitions and dependency cycles.
- Policy inheritance and overrides.
- Scheduler, budgets, and idempotency.
- Zod contracts across UI/server/MCP.
- Responsive components.
- Linux path, process, and filesystem coverage.
- Deterministic fake provider with no paid model calls.

### 21.2 Main-branch integration

Tests use real disposable Git repositories/worktrees and the selected TaskStore. They cover:

- worktree creation, ownership, and cleanup;
- commits, branches, push, and recovery refs;
- multiwriter TaskStore behavior;
- backup, restore, and migrations;
- plugin interruption at every boundary;
- duplicate and out-of-order events;
- base changes during validation/review/Ready;
- dirty worktrees and absent commits;
- disk pressure, offline systems, and expired credentials;
- no duplication of agents, PRs, merges, or feedback.

### 21.3 Release candidate

- Clean installation on a supported stable Paseo version.
- Upgrade from the previous Director version.
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
- One normal top-level Paseo Task Agent owns one development Task and runs in that Task's isolated Execution Workspace/worktree. Director does not launch it through Organizer or agent-scoped subagent orchestration.
- A development Task Agent may use internal helper subagents, but they do not claim Tasks, own Beads records, replace the Task Agent, or become development Task owners.
- Every result is committed and independently reviewed at the exact SHA.
- The repository is clean before handoff.
- Beads records outcome, tests, review, and links.
- A spike closes with reproducible evidence and an ADR even when it concludes “do not build.”
- No milestone starts with an unresolved entry gate.

Initial development skills define commit, branch, PR, review, testing, and documentation rules before parallel implementation begins.

For Director's own repository, the Task Agent integrates automatically after an independent `approve_candidate` verdict when the remote head still equals the reviewed SHA, the merge operation atomically asserts that exact head, the relevant base remains valid, every configured and Task-required check passes, the PR is mergeable, no human feedback is unresolved, and the Task is not explicitly manual. A pre-merge refetch without an expected-head condition is insufficient. This repository workflow does not change Director's product default of manual merge described in section 14.

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
Organizer → Task → Queue → Worktree → Fake agent
→ Candidate commit → Durable state → Cleanup
```

Includes scaffold, modular architecture, configuration schema, Create/Adopt Organizer, selected TaskStore, minimal Board/List, and restart reconciliation.

**Exit gate:** interrupt every boundary without losing state or duplicating effects.

### M2 — Planning and scheduler

- Multiple Projects and Workspaces.
- Epics, Tasks, dependencies, priorities, labels, and filters.
- Project/Workspace/Task inheritance and overrides.
- Preview/Apply configuration.
- Manual/automatic launch and concurrency.
- Agreed planning scale.

**Exit gate:** complete deterministic planning and scheduler behavior with fake adapters.

### M3 — Agent execution

- Organizer/Task Agent/Reviewer Agent profiles.
- Scoped MCP.
- Top-level Task Agent and helper-subagent lifecycle.
- Frozen Run configuration and budgets.
- Pause/Resume/Cancel/Emergency stop.
- One top-level Task Agent replacement and recovery.

**Exit gate:** concurrent Runs and fault injection without duplicate or orphaned execution.

### M4 — Quality and delivery

- Exact-SHA Candidate review.
- Detached Reviewer checkout and structured verdict.
- CI/review correction loops.
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
| Paseo plugin API changes | Thin adapters and explicit compatibility range | Separate release line for another stable API |
| Operation absent from the public SDK | M0 proof before depending on it | Request upstream, reduce scope, or postpone |
| Beads cannot implement TaskStore cleanly | Real contract, concurrency, and migration tests | Select one direct Dolt/other store before M1 |
| MCP is incomplete in a provider | Capability detection and provider-specific proof | Exclude it with an exact explanation |
| Duplicate, missing, or out-of-order events | Durable commands and idempotency | Reconcile from persisted/external facts |
| Agent leaves dirty/no-commit work | Engine-owned Git verification | Correct or route to Needs you |
| Base/CI/review/merge race | Candidate/base SHA binding | Invalidate and revalidate |
| Crash during push/PR/merge | Intent before effect and observed result after | Inspect remote before retrying |
| Disk growth | Retention, ownership, cleanup, and 10% threshold | Stop launches and clean first |
| OS/tool differences | Portable APIs and capability-based Doctor | Disable only the missing capability |
| Secret in durable output | Central redaction and adversarial tests | Stop workflow and treat as P0 |
| GitHub/network/auth outage | Backoff and Degraded state | Park; never change delivery silently |

M0 stops and M1 cannot begin until public, supported mechanisms prove that Director can:

- create, observe, recover, and terminate top-level Task Agents and Reviewer Agents, and observe, account for, contain, and clean Task-Agent-created helpers;
- create, own, and clean worktrees;
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

Every risk becomes a Beads Task with an owner, evidence, state, and ADR. When a critical capability is absent, Director reduces scope before depending on fragile internals.

## 26. Post-`1.0` roadmap

Ordered candidates, subject to new planning:

1. Field-owned synchronization with GitHub Projects.
2. Periodic discovery/ingestion of GitHub Issues.
3. Additional forges.
4. Multi-daemon execution and Project migration.
5. Teams and RBAC.
6. Richer native notifications if Paseo exposes the necessary contribution.
7. An alternative/Go engine only with measured justification.

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
