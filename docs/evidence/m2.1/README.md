# M2 entry-gate evidence

## Result

The M2 entry gate passes at refetched `origin/main`
`ec1f50cb1b6104f9cf64f47d7aca17a2445601c3`. M1 is independently approved,
validated on Linux, integrated, closed, and no longer blocks M2. The
checks-only coordinator blocker `dir-m2.10` is also independently approved,
integrated, and closed. No unresolved P0 or P1, and no new P2, was found. No
sibling blocker is required.

This record freezes the contracts that planning and scheduling may extend. It
does not implement an M2 feature, change an architecture or PLAN decision,
accept a risk, close `dir-m2.1` or `dir-m2`, or start another M2 Task.

## Authority and method

- The normative product source is approved
  [PLAN v0.4](../../PLAN.md), especially sections 2, 4–13, 21–25.
- The accepted ADR set relevant to the M1 exit and M2 contracts was reread in
  full: ADR-0001 through ADR-0007 and ADR-0009 through ADR-0018. ADR-0008 was
  also reread as the historical maximal threat analysis superseded by
  [ADR-0014](../../adr/0014-practical-linux-agent-boundary.md).
- The gate was derived from current Git objects, Beads records and comments,
  direct GitHub PR/Actions reads, and current source and tests. The prior M1
  audit narrative was not used as authority for these observations.
- The repository is `mcuadros/paseo-director`, GitHub database ID
  `1358627520`. A fresh `git fetch --prune origin main` left both the clean
  checkout base and `origin/main` at
  `ec1f50cb1b6104f9cf64f47d7aca17a2445601c3`.
- `dir-m2.1` remains `in_progress`, assigned to original Task Agent
  `paseo:449c5064-8bcc-4870-87c7-72bc717cfc68`. Its complete current Task and
  comment record was reread under that explicit actor. Its two `blocks`
  dependencies are closed `dir-m1.10` and closed `dir-m2.10`.

## Base supersession and coordinator unblock

The prior evidence Candidate
`542031762ce37a6de99ba5ad479e77fda0012770`, tree
`821c2710833981df75332fa3a8fb4f0a808cf19f`, against base
`c719b1749d3f8537449dab7222ddb123655b6294` is base-stale. Its reviews in
Beads comments `01a08188-0600-7f18-9b1e-7818952898eb` and
`01a08268-36d1-758c-88c5-d6ccd59055e9`, validation, publication, and PR #38
remain immutable historical evidence only. The Sol Reviewer fallback in human
decision `01a08251-185c-7857-83fe-5a815155632e` was explicitly scoped to that
superseded Candidate and supplies no authority for a replacement review.

[PR #38](https://github.com/mcuadros/paseo-director/pull/38) remains open and
non-draft with exact head `542031762ce37a6de99ba5ad479e77fda0012770`, base
`c719b1749d3f8537449dab7222ddb123655b6294`, no GitHub comments or reviews,
and its successful Linux check. Neither that PR nor its remote branch is an
input to the replacement Candidate, and neither is changed by this Task Agent.

`dir-m2.10` resolved the checks-only coordinator obstruction without changing
product behavior:

| Fact | Current authoritative observation |
|---|---|
| Task and Candidate | Beads reads `dir-m2.10` closed at `2026-09-08T21:10:11Z`. Exact Candidate `9616709223d4a4356eebc3d60523c5108f09470f`, tree `06f663d43baa43a2b81213c8a3f15a9c04041361`, was independently approved in comment `01a082c8-5a0d-7cbf-a4af-0538d08ebfad`. |
| Publication and CI | [PR #39](https://github.com/mcuadros/paseo-director/pull/39) reads back `MERGED`, non-draft, exact head `9616709223d4a4356eebc3d60523c5108f09470f`, exact base `c719b1749d3f8537449dab7222ddb123655b6294`, no GitHub comments/reviews, and merge commit `ec1f50cb1b6104f9cf64f47d7aca17a2445601c3`. [Actions run 34277501984](https://github.com/mcuadros/paseo-director/actions/runs/34277501984) is `completed/success` for that exact head and its sole `Scaffold checks (Linux)` job succeeded. |
| Checks-only behavior | Current `checkFacts` first requires a complete Check Run page, exact Candidate binding, completed/success status for every Check Run, and exactly one match for every configured required check. Only then does zero legacy Commit Status contexts produce explicit `checks_only_no_statuses`; any nonzero status set still requires complete pagination, combined success, exact Candidate binding, and every context success. Missing, duplicate, pending, failing, wrong-SHA, or incomplete facts remain fail-closed. |
| Integration | Beads coordinator comment `01a082db-6123-7b36-b052-0d0a51154c9b` records the structured checks-only gate, atomic integration, and cleanup. Git proves merge `ec1f50cb1b6104f9cf64f47d7aca17a2445601c3` has parents `[c719b1749d3f8537449dab7222ddb123655b6294, 9616709223d4a4356eebc3d60523c5108f09470f]` and tree `06f663d43baa43a2b81213c8a3f15a9c04041361`, equal to the Candidate tree. The same comment explicitly records PR #38 and its coordinator state as preserved and untouched. |

## Verified M1 exit facts

| Fact | Current authoritative observation |
|---|---|
| Reviewed M1 exit Candidate | Beads review comment `01a080d8-88e2-7b62-9049-86bdba49d4df` records `approve_candidate` for exact Candidate `020f3164902504776a8811d7069d09b64266b5ba` against base `9e5dc2782aabbac08868f75fb62d62e92bdaadfd`. The Reviewer was distinct, top-level, parentless, and detached at that Candidate. |
| Publication and CI | [PR #37](https://github.com/mcuadros/paseo-director/pull/37) reads back `MERGED`, non-draft, head `020f3164902504776a8811d7069d09b64266b5ba`, base `9e5dc2782aabbac08868f75fb62d62e92bdaadfd`, no comments, and no GitHub reviews. [Actions run 34222934496](https://github.com/mcuadros/paseo-director/actions/runs/34222934496) is `completed/success`, event `pull_request`, workflow `CI`, head SHA `020f3164902504776a8811d7069d09b64266b5ba`; its sole job is `Scaffold checks (Linux)` and succeeded. |
| Atomic integration | Beads coordinator comment `01a080e3-7795-7cce-ba50-2b15aff1e81b` records the immediately pre-merge exact-head/base/check/feedback gates and `--match-head-commit 020f3164902504776a8811d7069d09b64266b5ba`. GitHub reports merge commit `c719b1749d3f8537449dab7222ddb123655b6294`. The local Git object has parents `[9e5dc2782aabbac08868f75fb62d62e92bdaadfd, 020f3164902504776a8811d7069d09b64266b5ba]`, and its tree `6fd53597e047b0b6268fffab5f75e2a2cb73c211` equals the Candidate tree. |
| Exit Task | Beads reads `dir-m1.10` as `closed` at `2026-09-08T11:59:46Z`: the exit Candidate was approved, Linux CI passed, PR #37 integrated, all blocking children closed, and owned resources were cleaned. |
| M1 Epic | Beads reads `dir-m1` as `closed` at `2026-09-08T12:51:57Z`. Its close reason explicitly records human authorization to force-close the Epic after the approved/integrated exit while preserving four priority-3 non-blocking hardening children. |
| M2 dependency | Beads reads the sole `blocks` dependency of Epic `dir-m2` as closed Epic `dir-m1`; the two `blocks` dependencies of entry Task `dir-m2.1` are closed `dir-m1.10` and closed `dir-m2.10`. There is no unresolved M2 entry dependency. |
| Remote cleanup | Fresh `git ls-remote --heads origin 'refs/heads/task/dir-m1*'` returns no ref. This agrees with the M1 coordinator cleanup record without recreating or deleting anything. |

The previously accepted exact-Paseo-0.7.2 connector-authority P2 remains
unchanged. The controlling human decision in `dir-m1.12` accepts the trusted
Director for Paseo connector's full daemon-operator credential only with the
exact bounds consolidated by
[ADR-0017](../../adr/0017-standalone-engine-connector-authority-boundary.md).
Beads comments `01a07a50-19ca-760b-8085-7a823e82def8` and
`01a07a56-856e-734d-84a8-5433b39d364a` preserve the decision-consistency
readback. This gate neither broadens nor reopens that P2.

## Preserved non-blocking M1 work

These are the four unclosed M1 priority-3 hardening Tasks named by the M1 exit
review and Epic closure. Current Beads relationships contain only
`parent-child` and `discovered-from` edges for each; none has a `blocks` edge.
Their current manual-hold/blocked status is preserved.

| Task | Preserved scope |
|---|---|
| `dir-m1.19` | Architecture guard and deterministic diagnostic blind spots. |
| `dir-m1.21` | Canonicalization bounds/wording, numeric classification, and Board evidence hardening. |
| `dir-m1.22` | Coordinator pagination, locking, Candidate/base CI budgeting, and reclaimed-checkout hardening. |
| `dir-m1.24` | Execution vocabulary, timestamp, containment, immutable base, and prior-dispatcher hardening. |

They are not silently dropped, promoted, declared complete, or converted into
M2 blockers by this gate.

## Frozen contracts M2 may extend

“Extend” below means implement the planned M2 fields, reducers, queries, and
tests inside the existing engine-owned versioned contracts. It does not mean
reinterpret an M1 field, move authority across a boundary, add a fallback, or
change an accepted decision. A genuine contract change requires its own
approved Task and, where applicable, an ADR.

### 1. Project, Workspace, Epic, and Task model

The normative model remains PLAN sections 2 and 5:

- A Project is the managed product, has exactly one Organizer
  repository/configuration, contains one or more canonical-repository
  Workspaces, and is distinct from a native Paseo Project.
- A Workspace identifies exactly one canonical source repository. It is not a
  checkout or a Task Execution Workspace. Several Tasks may target it only
  through distinct isolated Execution Workspaces.
- Epic is optional and is the only grouping level. A Task may belong to at
  most one Epic or be standalone; a third planning level is forbidden.
- A Task targets exactly one Workspace and carries stable identity, title,
  objective, explicit acceptance criteria, optional Epic, dependency links,
  priority, labels/context links, permitted overrides, external references,
  lifecycle metadata, and an optimistic version.
- Dependencies form an acyclic graph over Tasks and Epics. Epic dependencies
  are hard: an incomplete blocking Epic blocks every Task in the dependent
  Epic unless a human applies an explicit audited override to that exact Task.
- Task priority is the closed order `urgent`, `high`, `normal`, `low`, with
  `normal` the default. Dates are UTC and deletion is logical except for
  separately authorized cleanup of proved-owned ephemeral resources.

The M1 persistence seed in
[`engine/domain/taskstore.go`](../../../engine/domain/taskstore.go) contains
minimal versioned `Project`, `Task`, `Run`, and immutable `Candidate` records.
The configuration seed in
[`engine/domain/configuration/configuration.go`](../../../engine/domain/configuration/configuration.go)
already distinguishes Project from canonical-repository Workspace. M2 may add
the planned Workspace, Epic, dependency, priority, label, reference, and
override persistence required by `dir-m2.2` and `dir-m2.3`; it must preserve
existing IDs, ownership relationships, optimistic versions, Run numbering and
base binding, and immutable Candidate history.

### 2. Configuration precedence and Preview/Apply

The fixed precedence is:

```text
Project defaults -> Workspace overrides -> Task overrides
```

Every override is either `Inherit` or a concrete value within the
human-approved Project security/policy envelope. A one-off expansion requires
separate explicit audited human confirmation. Effective configuration, the
approved Organizer revision, schemas, PreparationPlan, and referenced
skill/template hashes are frozen into a Run; a later revision cannot mutate
that Run.

PLAN section 7.4 fixes the complete activation protocol, including the rule
that an uncommitted proposal is committed as exactly one logical change
containing only previewed files. The engine-only M1 revision aggregate in
[`engine/application/configuration/revisions.go`](../../../engine/application/configuration/revisions.go)
already implements the in-memory revision-state portion:

1. Preview receives the exact proposed Organizer object ID and submitted
   bytes, validates and canonicalizes them, and produces deterministic content,
   configuration, issue, impact, and preview identities without activating the
   revision.
2. Invalid proposals remain visible pending previews while the active revision
   stays unchanged.
3. Apply accepts only the exact current preview at the expected aggregate
   version and only with a confirmed server-authenticated human actor. It does
   not reparse caller-supplied replacement bytes.
4. Successful Apply activates the already validated revision, clears pending,
   and affects only future Runs. The active pointer is dynamic Project state,
   not a self-referential Organizer commit.

The general proposal-commit effect is not implemented by that revision
aggregate. It remains an application/adapter obligation for `dir-m2.5`, which
must preserve the complete PLAN protocol and test the exact one-commit effect
without moving activation authority out of the Director Engine.

The version-1 schema currently contains Project, Workspaces, agent profiles,
Project defaults, Workspace overrides, and explicit skill/template references.
M2 may add Task-level override representation and effective-configuration
reduction only through the engine-owned, schema-versioned path. UI and
connector code may render previews and transport typed commands; they may not
validate, activate, freeze, or derive policy.

### 3. Pure Eligibility and Launch reducers

All six lifecycle decisions remain exclusive pure, versioned Director Engine
reducers; M2 directly extends only scheduling inputs around Eligibility and
Launch. The closed reducer set is defined in
[`engine/reducer/doc.go`](../../../engine/reducer/doc.go).

[`engine/reducer/eligibility`](../../../engine/reducer/eligibility/doc.go)
currently reduces named observations for the current Project lease/state,
approved Organizer revision, Task completeness, dependencies, absence of an
active Run, launch policy, capacity, budgets, provider admission, exact
repository identity, lifecycle admission, rootless isolation, and finite
operational limits. It returns only `eligible`, `wait_queued`, or `escalate`,
with a stable fact hash/decision ID and `cleanupAuthorized=false`. Missing or
unsafe facts fail closed; dependency, active-Run, policy, and capacity waits
remain Queued.

[`engine/reducer/launch`](../../../engine/reducer/launch/doc.go) requires the
still-current Eligibility decision, reserved capacity and budget, immutable
Run and exact repository bindings, lifecycle/isolation/operational admission,
the valid frozen preparation plan and barrier, and uniquely keyed effect
intents. It orders observation/adoption/dispatch for the Director-owned
worktree, registered host view, rootless boundary, approved setup when needed,
and top-level agent. It may create the agent intent only after
`preparation_ready`; it performs no I/O itself.

M2's scheduler may select an eligible record and ask these reducers for a
decision. It may not create a workspace/agent directly, trust a model claim,
read a clock or adapter from a reducer, reuse a consumed observation, or grant
cleanup authority because launch parked.

### 4. Dependencies, ordering, capacity, and budgets

The scheduler contract is fixed by PLAN section 10 and ADR-0018:

1. Progress an already-started Run needing Reviewer, correction, Validation,
   or integration capacity.
2. Process explicit human `Launch now` requests.
3. Order other eligible Tasks by `urgent`, `high`, `normal`, then `low`.
4. Use FIFO `queuedAt` within a priority.

There is no manual rank or queue drag-and-drop. `Launch now` does not preempt
an agent, bypass a dependency without its separate audited confirmation, or
exceed a hard security, disk, time, cost, CI, or capacity limit.

Default capacity is `maxActiveTasks=4`,
`maxActiveTasksPerWorkspace=2`, `maxConcurrentAgents=8`, and
`maxSubagentsPerTask=3`. The global agent count includes top-level Task Agents,
top-level Reviewer Agents, and helpers. There is no reserved Organizer agent
slot or standing planning agent. Capacity and budget reservations must be
atomic so concurrent scheduler claims cannot exceed a limit.

Every Run has finite time, token, and turn limits, an explicit CI budget, and
an optional finite cost limit. Usage is durable consumption plus outstanding
reservations. Reaching or reserving through 85% records one soft-budget event
and pauses new model-consuming work at the next safe boundary; 100% forbids
new budget-consuming work and routes the exact exhausted dimension to Needs
you. Restart never resets consumption, reservations, warnings, or hard
exhaustion. M2's launch-budget admission must preserve those later runtime
semantics rather than inventing a second ledger.

### 5. TaskStore boundary

The selected runtime store remains one Director-owned direct Dolt `2.3.2`
schema behind the trusted Director Engine's typed TaskStore port. The engine is
the sole raw SQL identity and credential holder. Agents, UI, connectors,
repositories, prompts, configuration, and support output receive neither SQL,
credentials, backend selection, nor a raw connection.

The current typed interface is
[`engine/ports/taskstore/taskstore.go`](../../../engine/ports/taskstore/taskstore.go).
It atomically binds mutable aggregate writes to immutable Command outcomes and
Events, uses expected-version optimistic concurrency, makes Candidate,
Command, and Event records append-only, and exposes a monotonic committed Event
cursor. The adapter must retain exact listener/database/store/schema identity,
safe global/session commit-mode checks before every write, bounded redacted
errors, and fail-closed health behavior. Git and Dolt synchronization remain
two separately journaled and retryable effects.

M2 may add typed Workspace, Epic, dependency, priority, label, override, and
query methods plus their direct-Dolt mappings. It may not expose the adapter,
weaken immutable/history/expected-version rules, switch stores, or claim an
atomic cross-system transaction. ADR-0016 still assigns the direct-Dolt
production-scale proof to `dir-m5.10`. The M2 planning/query/render envelope
in `dir-m2.8` may be exercised with fake or bounded adapters, but it cannot be
reported as that deferred TaskStore production-scale proof.

### 6. UI projection ownership

Board and List remain two renderings of one Director Engine query. The data
path is TaskStore facts -> Go application reader -> engine-owned projection ->
versioned host contract -> policy-free connector/RPC -> React Native view.

The M1 implementation in
[`engine/projection/board.go`](../../../engine/projection/board.go) and
[`engine/application/board/query.go`](../../../engine/application/board/query.go)
already reserves the closed lane vocabulary `needs_you`, `queued`, `building`,
`validating`, `in_review`, and `ready`, derives only the states current facts
prove, checks ownership joins, and binds a stable snapshot to the monotonic
TaskStore cursor. Candidate presence proves quality work is pending, not Ready.

M2 may add complete routing-derived phases, filters, Epic grouping, sorting,
pagination/virtualization, and the Done filter. State is never a mutable Kanban
field: card movement, TypeScript inference, a model outcome, or connector
defaults cannot route a Task. Needs you remains reserved for an engine-derived
typed human action; an ordinary dependency wait remains Queued and explains
its blocker.

### 7. Preparation and claim schemas

The frozen M1 preparation schema is
`director.preparation-plan/v1`, implemented in
[`engine/domain/execution/preparation.go`](../../../engine/domain/execution/preparation.go).
It has a 1,200-second whole-plan deadline and 900-second dependency aggregate.
Its exact ordered steps are:

| Ordinal | Step ID | Effect class | Timeout |
|---:|---|---|---:|
| 1 | `freeze-inputs` | `store_only` | 10 s |
| 2 | `reconcile-eligibility` | `store_only` | 30 s |
| 3 | `admit-security` | `store_only` | 30 s |
| 4 | `create-worktree-view` | `unique_create` | 120 s |
| 5 | `materialize-boundary` | `unique_create` | 60 s |
| 6 | `probe-tooling` | `store_only` | 60 s total |
| 7 | `prepare-dependencies` | `unique_create` | 300 s per command, 900 s aggregate |
| 8 | `build-context` | `store_only` | 30 s |
| 9 | `commit-ready` | `store_only` | 10 s |

The barrier binds the exact plan ID plus hashes for frozen inputs,
Eligibility, security admission, worktree, host view, isolation, tooling,
dependencies, and context. PLAN section 11 additionally requires each
applicable prepared step to carry exact input/output hashes, canonical
executable/argv/cwd, environment allowlist, attempt budget, and closed failure
mapping. The M1 fake-path representation is a walking-skeleton seed, not
permission for M2 or later work to omit or model-select those facts. The
simultaneous per-step, dependency-aggregate, and whole-plan ceilings and the
deterministic aggregate-expiry route remain fixed.

The Director Engine owns exactly seven closed AgentOutcomeClaim kinds in
[`engine/domain/agentoutcome`](../../../engine/domain/agentoutcome/claim.go).
Every JSON schema rejects additional properties:

| Outcome | Required model-supplied fields |
|---|---|
| `completed` | `candidateSha`, `baseSha`, `criteriaResults`, `residualRiskCodes` |
| `needs_validation` | `candidateSha`, `baseSha`, `checkIds` |
| `needs_review` | `candidateSha`, `baseSha`, `criterionIds` |
| `needs_human_decision` | `questionCode`, `question`, `options`, `affectedScope`, `resumeCondition` |
| `blocked_by_dependency` | `dependencyIds`, `wakePredicate` |
| `blocked_by_access` | `operationCode`, `failureFingerprint`, and exactly one applicable bounded capability/resource classification |
| `budget_exhausted` | `budgetDimension`, `observedAmount`, `unit` |

The connector fixes schema version, Project, Workspace, Task, Run, role,
native agent, turn/request identity, frozen revision, and server time. The
model cannot select them. Claims are immutable bounded inputs, never evidence
or lifecycle decisions. M2 may store, query, and route only after the named
durable/external facts are reconciled; it may not add a generic success,
failure, retry, stuck, route, close, or free-form fallback.

## Gate disposition

- `dir-m1.10`: closed and independently approved at exact Candidate
  `020f3164902504776a8811d7069d09b64266b5ba`.
- Epic `dir-m1`: closed under the recorded explicit human authorization.
- Epic `dir-m2`: its only milestone dependency is satisfied.
- `dir-m2.10`: closed after its checks-only coordinator fix was independently
  approved, Linux-CI validated, and integrated as
  `ec1f50cb1b6104f9cf64f47d7aca17a2445601c3`.
- `dir-m2.1`: claimed, with no unresolved blocker.
- Prior `dir-m2.1` Candidate `542031762ce37a6de99ba5ad479e77fda0012770`
  and both reviews: base-stale historical evidence only; the Candidate-scoped
  Sol fallback is not reusable.
- P0/P1/new P2: none observed; no sibling blocker created.
- Existing M1 P3s: preserved exactly as `dir-m1.19`, `dir-m1.21`,
  `dir-m1.22`, and `dir-m1.24`.
- Scope: evidence only; all M2 implementation and Task/Epic closure remain
  outside this Candidate.
