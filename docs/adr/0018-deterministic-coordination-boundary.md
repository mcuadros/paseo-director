# ADR-0018: Make coordination decisions deterministic

- **Status:** Accepted
- **Date:** 2026-09-07
- **Beads Task:** dir-m1.13
- **Plan gate:** M1 deterministic coordination and agent execution contract
- **Decision owner:** dir-m1.13 Task Agent
- **Amends:** PLAN header and §§1; 2.4–2.6; 4 invariant 7; 5.6; 6.1–6.4; 7.1–7.5; 8.2; 9.1–9.3; 10.1–10.2; 11; 12.2–12.4; 13.1–13.5; 14.1–14.5; 15.1 and 15.4–15.5; 16.1–16.4; 17.1–17.2; 18.1–18.3; 19.1 and 19.4; 20.1–20.2; 21.1–21.3; 22; 23 M1, M3, and M4; 24.1; and 25
- **Aligned with:** [ADR-0003](0003-paseo-0.7.2-lifecycle-recovery.md), [ADR-0010](0010-top-level-task-agent-parentage.md), [ADR-0014](0014-practical-linux-agent-boundary.md), [ADR-0015](0015-idempotent-command-effect-contract.md), and [ADR-0016](0016-defer-taskstore-scale-proof-to-m5.md)

This ADR may be authored as Accepted, but its status becomes authoritative only
after independent review of its exact Candidate and integration with its
reviewed base. This decision is about logical authority and durable contracts.
It is valid whether the Director Engine and its Paseo connector execute in one
process or two, and it does not depend on the outcome of dir-m1.12.

## Context

PLAN invariant 7 and §6.3 make the engine the sole executor of Director
lifecycle effects, but they do not reserve the decisions which cause those
effects. A model can therefore still appear to decide that a Task is eligible,
an agent should launch, an attempt should retry, work should escalate or route,
or a Task should close. An engine that merely carries out those conclusions
would retain deterministic effects while surrendering deterministic workflow.

The two source documents committed with this decision show why that gap is
unsafe:

- [Agent orchestration research](../evidence/dir-m5.12/agent-orchestration-research.md)
  reports that credible systems separate coordination, review, and mechanical
  integration; treat agent reports as claims; use deterministic gates; and
  pause near budget exhaustion.
- [Beads usage retrospective](../evidence/dir-m5.12/brain-beads-retrospective.md)
  records premature self-closure, unproved success, prose-based coordination,
  invisible human gates, and ambiguous audit attribution under an agent
  dispatch contract which delegated lifecycle decisions to agents.

The accepted decisions already establish the required safety floor:

- ADR-0003 makes recovery depend on fresh public and external facts, not plugin
  memory or one native status. Its full termination predicate and one-
  replacement limit remain unchanged.
- ADR-0010 keeps Task Agents and Reviewer Agents independent and top-level,
  while retaining the narrowly admitted Task-Agent-created helper mechanism.
- ADR-0014 keeps the trusted-provider Linux boundary, mandatory rootless OCI,
  lifecycle-surface admission, finite resource observations, and fail-closed
  parking.
- ADR-0015 defines durable Commands, fenced attempts, observations, effect
  classes, and class-specific retry. This ADR extends the same determinism from
  effect execution to the decision which selects an effect or state transition.
- ADR-0016 changes only the timing of TaskStore scale evidence and is not
  reopened here.

The boundary must not assume that a function call and its reducer share memory
or a process. It also must not turn a model's structured response into evidence
merely because parsing succeeds.

## Question or hypothesis

Can Director reserve eligibility, launch, retry, escalation, routing, and
closure to a deterministic engine reducer; limit every model to a closed
structured claim; and reconcile named durable and external facts before every
lifecycle transition, while preserving the approved Linux, lifecycle, helper,
exact-SHA, delivery, recovery, and cleanup gates in either a one-process or a
two-process topology?

## Acceptance criteria

- State the amended decision-and-effect ownership invariant verbatim.
- Define one closed Task Agent outcome vocabulary and the required external
  facts for every outcome.
- Define deterministic environment preparation before the first agent turn,
  including declared steps, numeric timeouts, and per-step failure semantics.
- Separate deterministic validation execution, model failure interpretation,
  and always-independent model review, and require review to cover correctness,
  specification, maintainability, readability, and design whether validation
  passes or fails.
- Define distinct numeric local-liveness and external-observation cadences.
  One failed probe must never prove death, and no single source may declare an
  agent stuck.
- Warn and pause at a numeric soft budget threshold before hard exhaustion.
- Give the logical engine, the Paseo host connector, and each Project's
  Organizer repository/configuration distinct names and responsibilities.
- Identify every PLAN section amended by this ADR without consolidating the
  stale PLAN body owned by dir-m1.14.
- Add no product code and weaken no accepted safety gate.

## Evidence

The two committed source inputs were verified before and after transfer:

| Source | Bytes | SHA-256 |
|---|---:|---|
| [agent-orchestration-research.md](../evidence/dir-m5.12/agent-orchestration-research.md) | 26,194 | a5d30d0ea0d75b7e9e0d94b1d5acd6711329b5adb6d29bfa9298d56b0e91e44a |
| [brain-beads-retrospective.md](../evidence/dir-m5.12/brain-beads-retrospective.md) | 24,905 | f36343c254394c2b1c80c9f8155a83b060c9904b0cabdd779f194b14639575b0 |

Both are planning evidence rather than normative decisions. This governance
Task reuses the independently integrated evidence and contracts in ADR-0003,
ADR-0010, ADR-0014, ADR-0015, and ADR-0016. It performs no new live Paseo,
provider, GitHub, TaskStore, product, or paid-model experiment.

## Decision

**Go.** Director adopts the deterministic coordination boundary and contracts
below.

### Normative names and ownership

The following vocabulary is authoritative:

| Name | Meaning and authority |
|---|---|
| **Director Engine** | The standalone logical coordination identity. It owns domain policy, reducers, scheduling, durable Commands and Observations, lifecycle decisions, and lifecycle effects. “Standalone” identifies an authority boundary, not a required operating-system process. |
| **Director for Paseo** | The Paseo host connector and public plugin. It supplies Paseo UI, RPC, agent, workspace, provider, and event adapters and transports typed Commands and Observations. It contains no independent workflow policy, even if deployed in the same process as the Director Engine. |
| **Organizer** | The per-Project Git repository, approved configuration revision, referenced TaskStore, durable project material, and its persistent Organizer Agent context. It is neither the engine nor the host connector. The Organizer Agent may propose commands and configuration changes but cannot decide or perform lifecycle transitions. |

Generic “Director” remains the umbrella product name. Statements about
coordination authority or lifecycle action mean Director Engine. Statements
about Paseo plugin installation, UI, SDK access, or native host adaptation mean
Director for Paseo. Statements about one Project's repository, configuration,
skills, templates, decisions, or persistent administration context mean its
Organizer. This interpretation amends every listed PLAN section even while
dir-m1.14 owns the later consolidated wording.

### Verbatim amended invariant

PLAN invariant 7 is replaced by the following text, verbatim:

> 7. Only the Director Engine decides Director lifecycle eligibility, launch, retry, escalation, routing, and closure, and only the Director Engine performs Director lifecycle side effects such as top-level Task Agent and Reviewer Agent creation, push, PR creation, merge, integration, and cleanup. A model may submit only a closed structured claim; a claim is never evidence and never authorizes a lifecycle state transition. Before any Task, Run, Candidate, Validation, Review, delivery, integration, or cleanup transition, the Director Engine reconciles the decision against named durable and external facts. A Task Agent may request and, only after deterministic Director Engine admission, invoke the existing helper-creation exception within its frozen Run policy; it does not decide admission, capacity, or lifecycle state. Helpers cannot perform Director lifecycle effects or become Task owners.

“Decides” means that a pure, versioned reducer maps a closed input fact set and
frozen policy to exactly one permitted command, projection, wait, or refusal.
No prompt text, free-form summary, confidence score, model-selected tool,
connector default, or adapter response can replace that reducer.

Recording an immutable claim or Observation is not itself a lifecycle
projection. The subsequent projection uses compare-and-swap versions and the
ADR-0015 command/effect contract. If any required fact is absent, stale,
ambiguous, unavailable, contradictory, or outside its positive freshness
window, the reducer waits, refuses, or parks according to the tables below. It
never asks a model to fill the gap.

### Deterministic decision reducers

The Director Engine owns these six decisions:

| Decision | Named required facts | Deterministic result |
|---|---|---|
| Eligibility | Project lease and active state; approved Organizer revision; Task completeness and expected version; dependency graph; absence of an active Run; frozen policy; preflight, security, capacity, disk, time, cost, CI, and provider facts | Eligible only when every predicate is true. Dependency waits remain Queued; unavailable external facts wait with backoff; a human-remediable or safety ambiguity routes to Needs you. |
| Launch | A still-current eligibility Observation; reserved capacity and budget; immutable Run identity and configuration; exact base/repository binding; complete preparation barrier; uniquely keyed workspace and top-level agent intents | Launch exactly once through ADR-0015. A stale or consumed fact set is re-observed. A model may request launch but cannot make it eligible. |
| Retry | Effect class; possible-handoff classification; fresh effect-specific Observation; unchanged scope and binding; prior dispatcher absence where required; exact compare predicate; remaining attempt, correction, replacement, CI, time, cost, and resource budgets | Retry only when the ADR-0015 class permits it. Nonrepeatable progress with possible handoff, a destructive present target, ambiguous matches, changed bindings, or exhausted limits cannot retry. |
| Escalation | A typed unresolved human question; reconciled access/configuration failure; hard budget exhaustion; terminal drift; unsafe identity/ownership; compound liveness result; or another existing fail-closed predicate | Emit one typed Needs-you record with its exact wake or human-decision condition. A model's concern alone records a claim and triggers observation, not escalation. |
| Routing | Current aggregate versions; frozen workflow policy; the validated claim kind; the outcome-specific facts below; pending Validation and Review facts; feedback and delivery facts | Select one next phase, wait condition, correction command, Reviewer command, delivery command, or Needs-you cause. Free-form prose and editable Board columns are not routing input. |
| Closure | Task acceptance criteria and terminal rung; exact current Candidate and base; clean-worktree proof; current Validation and independent Review records; configured CI and human-feedback facts; publication/integration/deployment facts required by policy; cleanup and ownership facts; no unresolved blocker | Append one audited closure only when every required fact is current and linked to this Task. A completed claim, approved review, merged PR, or pasted evidence block alone can never close a Task. |

These reducers are schema-versioned domain behavior. A new decision input,
outcome, route, or fallback is a contract change with tests; it is not an
adapter convenience.

### Closed Task Agent outcome contract

Every Task Agent turn ends with exactly one AgentOutcomeClaim. Its closed
outcome vocabulary is:

1. completed
2. needs_validation
3. needs_review
4. needs_human_decision
5. blocked_by_dependency
6. blocked_by_access
7. budget_exhausted

There is no other value and no generic success, failure, retry, stuck, route,
close, or free-form fallback. An unknown value, unknown field, schema error,
oversize field, secret, private path, or invalid scope is rejected and stored
only as a bounded malformed-claim audit fact. Claim rejection triggers
reconciliation of the agent turn; it does not infer failure or completion.

The connector fixes schema version, Project, Workspace, Task, Run, role,
native agent, turn, request identity, frozen revision, and server timestamp.
The model cannot select them. The model supplies the outcome and only the
outcome-specific bounded fields. Candidate identifiers are full object IDs;
criteria and check identifiers refer to the frozen Task and configuration;
summaries are redacted, bounded context and never evidence. Every accepted
claim is immutable and idempotent by its fixed turn/request identity.

The required claim fields, reconciled facts, and reduction are:

| Outcome | Required fields in the claim | Required durable and external facts before reduction | Reduction |
|---|---|---|---|
| completed | Exact claimed Candidate SHA and base SHA; one claimed result for every acceptance-criterion ID; bounded residual-risk codes | Turn ended; scope/revision match; worktree is clean; Candidate exists, is reachable, owned, and descends from the recorded base under the approved Git transfer contract; no conflicting Candidate; current resource/budget facts | Record or adopt the Candidate and queue both deterministic Validation and independent Review. Never close the Task. |
| needs_validation | Exact Candidate/base SHAs and frozen validation-check IDs | The completed-row Candidate facts plus current check definitions, execution environment identity, capacity, and no existing current result for each check | Queue only the configured deterministic checks, and also queue independent Review. The model cannot add, remove, weaken, pass, or interpret a check. |
| needs_review | Exact Candidate/base SHAs and claimed acceptance-criterion IDs to inspect | The completed-row Candidate facts plus Reviewer independence, detached-checkout, exact-SHA, model/profile, capacity, and existing-review facts | Queue one independent exact-Candidate Review and the configured deterministic Validation. The model cannot select its reviewer or make Validation optional. |
| needs_human_decision | One typed question code; one bounded question; closed options; affected policy/scope; machine-checkable resume condition | The question is in the Task/Run scope; no current decision answers it; policy provides no deterministic answer; actor and authority facts identify who may answer | Persist one human-decision request and route to Needs you. A prose question or a model-selected policy change is rejected. |
| blocked_by_dependency | Exact dependency IDs and the declared wake predicate | TaskStore graph and versions prove each dependency exists, is permitted, and is not satisfied; no approved override exists | Keep the Task Queued with a typed dependency wait. If the graph disproves the claim, discard it as stale and recompute the route. |
| blocked_by_access | Typed capability/resource code, bounded operation code, and redacted failure fingerprint | Fresh Doctor/preflight and effect-specific authentication, authorization, provider, repository, or external-service Observations; unchanged scope and policy | Waiting_external for a transient unavailable service; Needs you for a credential, permission, or configuration decision; fail closed for scope/identity conflict. No credential material is accepted. |
| budget_exhausted | Budget dimension and the agent-observed amount/unit | TaskStore usage ledger, provider usage facts, reservations, frozen limit, and current TaskStore time | At or above the hard limit, route to Needs you. At or above the 85% soft threshold but below hard exhaustion, apply the soft pause below. Below 85%, reject the claim as unproved and continue from engine facts. |

The engine may reach any of these routes without receiving a claim; crashes,
timeouts, events, budget observations, and external changes are engine inputs.
Conversely, a syntactically valid claim changes no Task, Run, Candidate,
Validation, Review, delivery, or closure state until its row's complete fact
set is freshly reconciled.

### Deterministic environment preparation

Preparation is a versioned PreparationPlan frozen into the Run before any
agent creation. Each step declares its stable ID, ordinal, kind, exact input
hashes, canonical executable and argv when applicable, canonical working
directory, environment allowlist, expected outputs, effect class, timeout,
attempt budget, and closed failure mapping. No shell interpolation, undeclared
installer, lifecycle hook, model-selected command, or silent fallback is
allowed.

The whole plan has a 1,200-second deadline. Dependency preparation may consume
at most 900 seconds of that deadline. The steps are:

| Order | Declared deterministic step | Timeout | Failure semantics |
|---:|---|---:|---|
| 1 | Freeze Task, policy, Organizer revision, TaskStore versions, Run identity, budgets, and exact base/repository inputs | 10 s | Any TaskStore identity, safe-mode, lease, or version failure pauses/degrades the Project before a Run can launch. A version conflict is reloaded, never overwritten. |
| 2 | Reconcile eligibility, dependencies, capacity, credentials/capabilities, and current external health | 30 s | Unsatisfied dependencies remain Queued. Unavailable external observations wait with backoff. Missing human-owned configuration or authority routes to Needs you. No partial preparation follows. |
| 3 | Verify source/common-directory/remote/base identity, ownership, disk/resource limits, rootless-OCI capability, and ADR-0014 lifecycle-surface admission | 30 s | Any mismatch, missing finite observation, unapproved non-empty lifecycle surface, or ownership ambiguity routes to Needs you with bounded facts. A secret observation is a P0 stop. |
| 4 | Create or adopt the uniquely keyed Paseo Execution Workspace/worktree | 120 s | Use the ADR-0015 unique_create class. Proven failure before handoff may retry after fresh proof; possible handoff becomes unknown and is reconciled; exactly one match is adopted; zero unproved or multiple matches park. |
| 5 | Materialize the frozen rootless-OCI profile, private Candidate Git path, fixed MCP scope, and process/memory/output/temp/worktree limits | 60 s | A proven pre-handoff failure may retry within budget. A possible partial mutation is reconciled. Missing isolation, scope, or a finite limit parks in Needs you; it never runs less isolated. |
| 6 | Probe every declared executable, exact version/capability, provider tuple, and authentication mode without mutation | 60 s total | Missing or different required tooling/capability routes to Needs you. Transient service unavailability waits with backoff. Director never installs or selects a replacement implicitly. |
| 7 | Run project-declared dependency preparation commands in order | 300 s per command; 900 s aggregate | Exit 0 plus declared output hashes completes a command. A completed nonzero exit records preparation_failed and routes to Needs you. A failure proven before process/API handoff may retry only under fresh engine authorization. A timeout or lost result after possible side effects is unknown and follows its declared ADR-0015 class. No model diagnoses setup before launch and no command is blindly rerun. |
| 8 | Build, redact, size-check, and hash the Task context, acceptance criteria, skill/template set, and output schemas | 30 s | Schema, reference, size, redaction, or secret-safety failure prevents agent creation and routes to Needs you; detected secret exposure is a P0 stop. |
| 9 | Commit the preparation_ready barrier with every output hash and a fresh eligibility/version check | 10 s | Transaction/lease/version failure leaves no readiness barrier. The engine reloads or pauses; it never prompts from a partially prepared environment. |

Only after preparation_ready may the Director Engine persist the uniquely keyed
top-level agent-create intent. ADR-0003 still requires Task Agent creation and
its initial prompt in one public create request. A lost response is reconciled
by exact facts rather than repeated blindly.

The local 30–120 second limits bound operations expected to use already
installed host capabilities. The five-minute per-command and fifteen-minute
aggregate dependency limits allow ordinary locked dependency materialization
without letting setup consume an unbounded Run. The twenty-minute whole-plan
deadline leaves bounded overhead and makes slow setup a visible environment
problem rather than model work. Projects may tighten these limits; expanding
them requires an explicit human-approved configuration revision within the
security envelope.

### Validation, failure interpretation, and review

Every admitted Candidate enters three distinct stages with distinct actors and
records. A failed or timed-out Validation never suppresses Review.

1. **Validation execution is deterministic.** A Director Engine process
   adapter selects only the frozen check IDs, runs their exact argv in the exact
   Candidate environment, and records a ValidationObservation containing
   Candidate/base/configuration hashes, check identity, start/end timestamps,
   exit status or signal/timeout, and bounded redacted output digest. Exit zero
   means the declared check passed; any other result means only that it did not
   pass. The executor does not explain, waive, reroute, retry, or correct it.
2. **Failure interpretation is model work.** After a non-passing Validation,
   the original Task Agent may receive a separate read-only interpretation turn
   with the exact ValidationObservation and Candidate. It returns one closed
   FailureInterpretationClaim: candidate_defect, base_failure,
   environment_failure, or indeterminate. It must bind the Validation and
   Candidate IDs, cite bounded facts, and state a proposed correction scope.
   The claim is not evidence and grants no edit turn. The Director Engine
   reconciles the cited Git, base, environment, check, budget, and attempt
   facts, then alone decides correction, wait, refusal, or escalation.
3. **Review is always independent model work.** For every admitted Candidate,
   whether Validation passed, failed, timed out, or remains unavailable, the
   Director Engine creates one normal top-level Reviewer Agent under ADR-0010
   when capacity permits. Existing Pause, Emergency-stop, security, identity,
   and resource gates may delay or forbid all execution; Validation result may
   not. The Reviewer receives no author transcript, uses a detached disposable
   checkout of the exact Candidate, and has no write tools. Review must cover
   acceptance/specification, correctness and security, maintainability,
   readability, and design. It returns one closed ReviewClaim verdict:
   approve_candidate, changes_requested, or needs_human_decision, with exact
   Candidate/base bindings and structured cited findings. The engine validates
   identity, independence, checkout, citations, schema, and current SHA before
   projecting the verdict.

Validation and Review are sibling quality obligations, not a pipeline in which
one substitutes for or short-circuits the other. A correction command is not
eligible until the current Candidate's available validation failures,
interpretation, Review findings, and human feedback are batched under frozen
policy. Any changed commit creates a new Candidate and invalidates both prior
Validation and Review for readiness exactly as the PLAN already requires.

### Liveness and external-fact cadences

Events remain wake-ups only. While any Run is active, the Director Engine uses
two independent clocks:

| Clock | Cadence | Purpose and justification |
|---|---:|---|
| Local session liveness | Every 10 seconds while a local provider turn or owned child process is expected | Cheap same-host process identity, ancestry, turn, resource, and supervisor observations detect loss promptly. Three consecutive failures require at least 30 seconds and trigger reconciliation, but do not prove death or stuckness. |
| External fact observation | Every 30 seconds while a Run is active; every 5 minutes while all Runs are idle; immediately on a supported event or command | Preserves the PLAN's approximately 30-second active safety net without polling remote/Paseo/Git/GitHub systems at local-heartbeat frequency. Idle polling limits load while bounding unnoticed drift. |

One failed probe never proves death, authorizes replacement, frees capacity, or
changes Task state. A local suspicion after three consecutive failed probes
only requests a full reconciliation. Transport, authentication, timeout, parse,
or unavailable responses are not absence.

No single source declares an agent stuck. The engine may project a typed
stalled_or_ambiguous escalation only when all of these hold:

1. no new durable turn, command, effect, Candidate, Validation, Review, or
   bounded progress fact exists for at least 5 minutes;
2. at least three consecutive local samples and at least two fresh external
   observation cycles corroborate lack of progress;
3. the corroboration spans at least two independent source classes, such as
   the owned process supervisor plus fresh public Paseo list/ref/refresh, or
   Paseo plus exact Git/worktree facts;
4. no declared long-running deterministic step, external outage, Pause, or
   known wait condition explains the interval; and
5. the complete ADR-0003/ADR-0015 reconciliation has run.

Even that predicate does not prove death. Proven termination still requires
closed status, non-null archivedAt, and reconciled external process/effect
facts under ADR-0003. Full termination plus the existing recoverable-failure
and budget predicates may authorize the one allowed replacement. Otherwise
the engine parks in Needs you without killing, retrying, replacing, releasing
capacity, or cleaning.

### Soft and hard budget behavior

For every finite consumptive Run time, cost, token, and turn budget, the engine
computes usage as durable consumption plus outstanding reservations. At 85% of
any hard limit, or before a proposed dispatch whose reservation would reach
85%, it atomically records one soft_budget_reached event, warns the user with
the dimension and measured ratio, and pauses new model-consuming turns,
helpers, retries, corrections, and Reviews at the next safe boundary. The
existing correction, replacement, CI, and effect-attempt counts retain their
own hard limits and are not converted into fractional soft limits.

An already active turn may finish. Observation, reconciliation, evidence
persistence, safe containment, and required non-destructive cleanup continue.
They are not optional budget-consuming progress. The hard limit remains
enforced during the active turn.

Only an explicit human command may resume within the unchanged limit or apply
a permitted budget revision. A resume acknowledges the one soft warning for
that exact limit revision so it does not loop at every sample; the warning
remains visible, reservations still cannot exceed the hard limit, and a changed
limit creates a new threshold. At 100%, the engine starts no budget-consuming
work and routes the Task to Needs you with the exact exhausted dimension.
Neither a model claim nor a process restart resets usage, reservations,
warnings, or exhaustion.

### Process-boundary-independent transport

All claims, Commands, Observations, preparation barriers, reducer inputs, and
results are schema-versioned, immutable or expected-versioned, bounded, and
durable before a dependent action. The same contracts are used across an
in-process module call and an inter-process transport. In-memory callback
order, shared heap state, connector uptime, and model conversation history are
never correctness inputs.

Director for Paseo authenticates and fixes host/session scope, translates
public Paseo facts into typed Observations, and transports them. Only the
Director Engine consumes them in decision reducers. If engine and connector
are separated, loss or duplication of transport messages is handled through
the same durable identities and reconciliation; if they are colocated, the
implementation may optimize transport but may not bypass the contract.

## PLAN amendments

The PLAN body intentionally remains textually stale in this Candidate because
dir-m1.14 owns consolidation after both pending decision ADRs close. ADR-0018
is added only to the PLAN header. Until consolidation, the normative effects
are:

| PLAN sections | Amended effect |
|---|---|
| Header; §§1; 2.4–2.6; 6.1–6.4; 7.1–7.5; 16.1–16.4; 17.1–17.2; 19.1 and 19.4 | Apply the Director Engine / Director for Paseo / per-Project Organizer naming and responsibility split. The split is logical and does not select a process topology or language. |
| §4 invariant 7 | Replace invariant 7 with the verbatim decision-and-effect ownership text above. Invariants 1–6 and 8–15 are unchanged. |
| §§5.6; 8.2; 9.1–9.3; 12.2–12.4 | Freeze claim schemas and preparation policy into the Run; store claims as bounded TaskStore inputs; derive Board, human-attention, and role behavior only through engine reducers. |
| §§10.1–10.2; 11; 13.1–13.2 | Make eligibility, ordering, launch, preparation, agent creation, and Candidate admission deterministic under the named facts and preparation barrier. |
| §§13.3–13.5; 14.1–14.5 | Require sibling deterministic Validation and independent Review for every admitted Candidate; reserve correction, delivery, routing, and closure decisions to the engine; preserve exact-Candidate/base and feedback gates. |
| §§15.1 and 15.4–15.5; 20.1–20.2 | Apply the 10-second local, 30-second active external, 5-minute idle, compound 5-minute stall, and 85% soft-budget rules while preserving pause, termination, replacement, and cleanup predicates. |
| §§18.1–18.3 | Treat model text and repository/tool output as untrusted claim input; enforce fixed scope, redaction, bounded facts, rootless OCI, credential separation, and engine-only lifecycle/delivery authority. |
| §§21.1–21.3 | Add exhaustive outcome/reducer tests, every preparation timeout/failure path, validation-review non-short-circuit tests, cadence/freshness tests, compound-liveness tests, soft/hard budget boundaries, and one-/two-process contract conformance. |
| §22; §24.1 | Apply the same no-self-closure rule to Director development: a Task Agent hands off a Candidate claim, while exact review, CI, integration, cleanup, and Task closure remain independently evidenced and engine/coordinator owned. |
| §23 M1, M3, and M4; §25 | M1 owns the typed contracts, reducer skeleton, preparation barrier, and fake-adapter recovery path; M3 owns live agent outcomes, liveness, and budgets; M4 owns Validation, failure interpretation, Review, correction, delivery, and closure. Missing or contradictory facts remain fail-closed risks, never model fallbacks. |

## Alternatives considered

### Keep deterministic effects but let models choose transitions

Rejected. A model-selected retry, route, or closure can duplicate work, bypass
evidence, or convert an error into a plausible success narrative even when the
subsequent effect is idempotent.

### Accept structured model output as authoritative state

Rejected. Schema validity proves syntax, not worktree cleanliness, Candidate
ownership, dependency completion, access, usage, review independence,
integration, or cleanup. Claims are useful bounded inputs only after external
reconciliation.

### Let each adapter or host connector own its local workflow

Rejected. It creates different policy in Paseo, Git, GitHub, and TaskStore
adapters and makes routing depend on deployment topology. Adapters observe and
perform one authorized effect; the Director Engine alone reduces workflow.

### Run deterministic Validation and skip model Review when it passes

Rejected. Tests and linters do not cover the whole acceptance contract,
maintainability, readability, or design. Conversely, model Review cannot
replace an exit status from an exact configured check. Both obligations remain.

### Use one heartbeat or one native status to declare stuck or dead

Rejected. ADR-0003 proved that running without an active turn and closed
without archivedAt are ambiguous. Separate clocks and compound sources bound
detection without converting one failed probe into destructive authority.

### Warn only at hard budget exhaustion

Rejected. A warning at 100% leaves no safe opportunity to stop another
expensive turn. The 85% threshold is early enough to reserve a bounded final
margin and matches the reviewed orchestration evidence, while hard limits
remain authoritative.

### Select a one-process or two-process implementation here

Rejected as out of scope. Persisted identities and reducers are required in
either topology. dir-m1.12 may decide deployment mechanics without changing
this contract.

## Consequences

- Model autonomy is limited to producing work, analysis, helper requests, and
  bounded claims. It cannot move lifecycle state by narration or tool choice.
- The engine has more schemas and reducer tests, but every transition becomes
  reproducible from durable inputs and named observations.
- Some valid claims wait while external facts are unavailable. This is
  deliberate fail-closed behavior, not a reason to accept the claim.
- Review cost is paid for every admitted Candidate even when Validation fails.
  This preserves the required independent design/quality signal and lets the
  engine batch all findings before a correction.
- Environment preparation can park before the first model turn. That makes
  setup failure visible and prevents reasoning budget from being consumed on
  undeclared installation or host repair.
- The two monitoring clocks add bounded polling. Events still wake immediate
  reads, active external polling stays at the existing 30-second plan cadence,
  and idle polling falls to five minutes.
- Soft budget pause may require human action before hard exhaustion. It does
  not weaken hard limits or stop safety reconciliation and cleanup.
- The helper exception remains exactly bounded by ADR-0010 and ADR-0015: a Task
  Agent can request and invoke one admitted parent-bound creation, while the
  engine decides admission and every subsequent lifecycle transition.
- ADR-0014's trusted-provider, rootless-OCI, lifecycle-admission, resource,
  credential, and authority bounds remain mandatory. No claim expands them.
- ADR-0016's M5 scale deferral is unchanged. This ADR makes no scale claim.
- No product code is authorized or added by this Task. dir-m1.14 owns the
  consolidated PLAN body after the relevant ADRs close.

## Independent verification

Independent exact-SHA review, publication, integration, and Task closure are
owned by the coordinator. The dir-m1.13 Task Agent must record its Candidate
SHA, focused checks, source hashes, risks, and clean-worktree handoff, but must
not self-review, spawn a Reviewer, publish, merge, close this Task, start
dir-m1.14, or resume another Task.
