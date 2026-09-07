# Agent orchestration research: what we learned (2026-09-06)

- **Status:** Research notes, planning input only
- **Beads Task:** `dir-m5.12` (planning placeholder; no implementation authorized)
- **Companion:** [brain-beads-retrospective.md](brain-beads-retrospective.md)

This document consolidates one day of research into how other systems orchestrate coding agents, which tools exist, how sustainable they are, and what Director should take from them. Nothing here is a decision; ADRs and the approved plan remain normative. Numbers were measured on 2026-09-06 and will age.

## 1. Lessons from a sibling project's tracker usage

Eleven days of agent-driven work in a sibling project (see the companion retrospective) showed the same failure shapes that the literature predicts:

- Notes became unbounded journals; receipt tasks were created after the work; closures were batched with pasted evidence; decisions stayed in the tracker while repository documents said "pending".
- No Definition-of-Done rung, so "done" meant code-complete, reviewed, merged or deployed depending on the agent, producing premature closes and reopen churn.
- Status was used as a signal channel and human gates were invisible as state.
- Cross-task coordination happened in prose (migration numbers, worktrees, handoffs), which cost real work.
- Agents wrote as the human because no actor was set, so approvals and narrations of approvals were indistinguishable.
- The dispatch contract given to agents ("complete end to end, including deployment; create and close records; continue with the next task") produced most of the drift.

What worked: an immutable candidate locked first, artifact-citing close reasons, exact-SHA review re-anchored on every push, a negative-scope sentence on every entry, verbatim dated human decisions, and a spend freeze before paid runs.

## 2. How credible systems orchestrate

### Roles

| System | Roles | Writes code | Reviews | Merges |
|---|---|---|---|---|
| OpenAI Symphony (spec) | One worker per issue; the daemon is not an agent | Worker | Human via tracker state | Worker's `land` skill only after a human moves the issue to `Merging` |
| Gas Town | Mayor, Polecat, Refinery, Witness, Deacon, Dogs, Crew | Polecats only | Refinery does not read code: rebase, test, merge | Refinery (Bors-style queue) |
| Agent Orchestrator (AO) | Orchestrator, Worker, Reviewer | Workers only | Read-only Reviewer in its own session | Human |
| no_human | Coder, adversarial Reviewer, deterministic gates, human | Coder | Fresh-session reviewer on a stronger model tier, edit tools refused | Human only |
| open-swe | Agent, Reviewer, Analyzer, general subagent | Agent in a per-thread sandbox | Read-only reviewer with a findings model | Human |
| Claude Code agent teams | Lead, teammates, shared task list | Teammates | Teammates with distinct review lenses | Out of scope |
| Paperclip | Org chart; reviewer and approver stages by policy | Assigned agents | Policy-enforced review stage | Approval stage |

Three separations recur: the coordinator never edits, the reviewer never writes, the merger never judges code. Gas Town additionally separates persistent identity, per-assignment sandbox and ephemeral session.

### Scheduling and lifecycle

- Symphony keeps an internal claim state separate from tracker state (`Unclaimed → Claimed → Running/RetryQueued → Released`), reconciles before every dispatch, applies global and per-state concurrency limits, sorts by priority then age, retries with `10 s × 2^n` up to five minutes, detects stalls after five minutes without events, caps turns per session, persists one workspace per issue, and recovers after restart from tracker and filesystem alone. A successful run may end at a handoff state such as `Human Review`.
- Gas Town dispatches directly or through a daemon heartbeat with a capacity cap and batch size; the Witness patrols in 30–90 s cycles, sends numbered nudges with three permitted responses, and escalates after the maximum; agents are never declared stuck from a single heartbeat store; molecules materialize checkpointed steps only for expensive workflows.
- AO has no scheduler; its auto-review sweep evaluates idle workers every minute and skips with explicit reasons; nudges carry a key, a signature and a maximum of three attempts, and are not marked delivered unless the worker could receive them; failed probes are never proof of death.
- no_human classifies blockers (transient, quota, dependency wait with machine-checkable wake conditions, missing access, ambiguity, scope explosion, impossible, novel) with routing rules, a maximum park time, and a fresh session seeded with the prior diagnosis on resume. A blocker is never resolved by lowering the bar.
- Claude Code teams use a shared task list with dependencies, file-locked claiming, and hooks as quality gates (`TaskCompleted` exit code 2 blocks completion).
- Paperclip runs agents in heartbeats triggered by schedule, assignment, mention or automation, with budget warnings at 80% and pause on exceed, and runtime-enforced execution policies with a maximum number of review rounds before a human decides.

### Review routines

- AO: one review run per PR, head SHA and reviewer harness; a new push supersedes the previous run; `changes_requested` is nudged to the worker with the GitHub review id; the worker replies and resolves threads.
- Symphony: a "PR feedback sweep" where every actionable comment blocks until code changes or an explicit justified pushback is posted; per-comment mode (accept, clarify, push back); reply before change; `Rework` is a full reset with a new branch and a new workpad.
- no_human: deterministic gates run before the model reviewer (verifiers per path, tamper guard for deleted tests, new skips, tautological assertions and autouse fixtures, reproduction gate requiring the tests to fail at the merge base and pass on the new tree); the reviewer runs in a fresh session on a different model tier, is told to refute "done", has its citations checked against the tree, and its verdict is recomputed deterministically from the findings; anything unparseable fails closed; the merge policy is advisory and a diff that edits the policy is flagged.
- open-swe: findings must anchor to changed lines inside the diff; exactly one evolving review per PR; existing threads are reconciled first; author-controlled text is wrapped as data; a nightly analyzer learns the repository's review style.
- Gas Town: the Refinery rebases, tests and merges mechanically; a conflict spawns a fresh polecat to re-implement; failing tests reopen the issue; pre-existing failures are filed, never fixed in the queue; the `gh-pr-review` formula captures the head SHA and re-verifies it before any mutation, transplants patches onto a clean base before running gates, and keeps a human gate on fix-merge.

### Skills and procedures given to agents

- Symphony's repository-owned `WORKFLOW.md`: configuration front matter plus a per-state prompt; one persistent workpad comment per issue with Plan, Acceptance Criteria, Validation, Notes and Confusions; an environment stamp; reproduce before changing; ticket-provided validation copied as non-negotiable criteria; an explicit completion bar before handoff. Skills `land`, `pull`, `push`, `commit` carry a pushback template, an ambiguity gate and a short list of when to ask the user.
- Gas Town's role templates, command bodies (`done` with preflight, `handoff`, `review` with CRITICAL/MAJOR/MINOR), nudge template, the propulsion principle ("if work is on your hook, run it; there is no approval step") and an explicit `no-changes` close to avoid respawn storms.
- AO's CLI catalog skill materialized by the daemon on every boot, role prompts with publishing scope, and standing-instruction confidentiality.
- no_human's per-repository `verifiers.yaml`, a JSON blocker schema (hypothesis, confidence, tried, one question), a six-part escalation report, and a human-confirmed learnings queue.
- open-swe ships reviewer skills in the repository and reads them from the trusted base ref, never from the PR branch.

## 3. Cross-cutting evidence

- MAST (NeurIPS 2025) classifies multi-agent failures into system design (44%), inter-agent misalignment (32%) and verification (24%); the failures are coordination and specification defects, not model capability.
- "Fail-plausible" failures: an agent converts an error into a convincing narrative; most such failures were found by humans, so an agent's claim is never evidence.
- Anthropic's research system: each delegation needs an objective, an output format, tool guidance and boundaries; agents resume from checkpoints rather than restart; multi-agent costs about fifteen times a chat, so it must earn its cost. Cognition: share full traces, not messages; actions carry implicit decisions; one main loop carries state while workers stay stateless and narrow.
- Durable execution: persist execution boundaries, idempotency keys and outbox for external mutations, approvals as durable events with the reviewed artifact hash and an expiry, and a crash-test matrix at every boundary.

## 4. Tool decisions examined

- No agent issue tracker provides the four TaskStore guarantees the M0 spike measured (expected-version CAS, atomic aggregate-plus-event writes, immutable events, immutable idempotency records). Beads 1.3 (release candidate) adds compare-and-set updates, work leases and an append-only provenance log; PlanDB adds none. Event stores and durable-execution engines provide them by design but are servers with their own synchronization story.
- A direct Dolt schema with append-only guards measured on this host: about 100 ms server start, 2.5 ms per single-row insert, 6.2 ms per committed effect (versioned update plus event plus Dolt commit), 15.6 ms per paginated Board query over 12,000 tasks, 0.9 ms per timeline read, 347 MB resident after load. Public sysbench numbers put Dolt at roughly MySQL latency. The real risks are one Dolt commit per event (Beads moved its events off the versioned plane for exactly that churn), the server process lifecycle, and offset pagination.
- AO persists everything in one SQLite file with trigger-fed change-data-capture and no synchronization; that fits a single-machine daemon. Director needs Dolt only because dynamic state must synchronize over the Organizer's Git remote.

## 5. Sustainability signals

Applying a simple rubric (public repository, at least three contributors with more than five commits, more than 20% external merged pull requests, a release within 90 days, measurable adoption): among trackers only Beads passes; among orchestrators AO, OpenHands, open-swe and paperclip pass, Gas Town fails on release cadence, and most others are single-author projects at AI velocity, useful for ideas rather than dependencies.

The largest sustainability risk for Director is its substrate: a single maintainer authors about 94% of Paseo's commits, hundreds of issues and pull requests are open, and releases ship several times per week with an incompatible next major already in preview. The plan covers API change with thin adapters and a compatibility range; it does not cover maintainer disappearance.

## 6. Candidate inputs for the plan

- Organizer task template: single workpad with Plan, Acceptance Criteria (with DoD rung), Validation, Notes and a completion bar.
- Pull-request skill: feedback sweep with per-comment mode and reply-before-change.
- Independent-review skill: deterministic gates before the model reviewer, verified citations, fail-closed verdicts, a reviewer that refutes "done".
- Needs-you model: blocker taxonomy with machine-checkable wake conditions and a maximum park time.
- Scheduler: internal claim state, per-state limits, reconciliation before dispatch, stall detection.
- Correction loops: keyed, signed, bounded nudges to the same Task Agent.
- Integration: the merger never judges code; conflicts go back to the Task, not to the integrator.
- Risk register: Paseo maintainer concentration with watched signals and a declared contingency.

## 7. Best practices from posts, products and talks (2026-09-06 pass)

Focused pass on four questions: scheduling, reviews, model and effort strategy, and skills. Sources are primary where possible (official docs, specs, repositories, annotated talks) and are listed at the end.

### 7.1 Scheduling work

- **Size by reviewability and independence.** "If the diff is too large to inspect carefully, the task was probably too large" (Kilo Code). A task is "the smallest unit that carries its own test cycle and is worth a fresh reviewer's gate" (superpowers); steps of 2–5 minutes; one item per loop (Ralph); split anything over two hours (long-running backlog runs).
- **Dependency-aware ready queue with atomic claims** is the common substrate (Beads `ready`, Claude Code shared task list with file-locked claiming, Symphony eligibility rules, Gas Town capacity governor). Symphony adds per-state concurrency limits so review capacity is not overwhelmed, reconciliation before every dispatch, stall detection after five minutes, exponential retries capped at five minutes, and persistent per-issue workspaces.
- **WIP limits**: 3–5 parallel agents is the repeated sweet spot (Osmani, Claude Code docs); 2–4 foreground plus background fire-and-forget agents (Kilo); one reviewer per 3–4 builders.
- **Kill and escalation criteria**: stop and reassign after three stuck iterations on the same error; iteration caps around eight; a forced reflection step before each retry; per-agent token budgets with auto-pause at 85% (Osmani); superpowers keeps the same implementer for rounds 1–3 and dispatches a fresh implementer on a stronger model for rounds 4–5, capped at five.
- **Human gates at the highest-leverage point**: plan approval before code (Claude Code agent teams, superpowers, HumanLayer). "A bad line of a plan could lead to hundreds of bad lines of code"; humans review research and plans, code review becomes mental alignment. Keep context utilization in the 40–60% range; research/plan/implement with intentional compaction shipped 35k lines in seven hours on a 300k-line codebase.
- **Deterministic stop gates**: a Stop hook that blocks the turn until a check passes (Claude Code overrides after eight consecutive blocks); `/goal` conditions re-checked by an evaluator each turn.
- **Slow loops**: nightly runs where "four agents open a total of four PRs by the morning" and a person still reads all of them.

### 7.2 Reviews

- **Independence is structural, not prompted**: a fresh context that never sees the author's transcript (Claude Code docs, superpowers, no_human, AO); the reviewer is told to refute "done" and to treat the implementer's report as unverified claims. Counter-lesson: a reviewer asked to find gaps always finds some; tell it to flag only correctness and stated requirements.
- **Anchor to the exact head**: one review run per PR and SHA, superseded on a new push (AO); one evolving review per PR with findings that must cite changed lines (open-swe); citations verified against the tree and verdicts recomputed deterministically from the findings (no_human).
- **Deterministic gates before the model reviewer**: red/green tests ("it's like five tokens"), tamper guard against deleted or weakened tests, reproduction gate (fail at base, pass on the new tree), per-path verifiers, lint output as input rather than gate.
- **Two-stage review**: spec compliance first, then code quality, then a whole-branch review on the strongest model at the end (superpowers).
- **Feedback handling**: every actionable comment blocks until code changes or an explicit justified pushback is posted; per-comment mode accept, clarify or push back; reply before changing; rework as a full reset (Symphony); route corrections to the same worker with bounded nudges (AO: three).
- **Precision beats recall for trust**: Google calibrated its ML review-comment resolver at 50% precision, resolved 52% of comments, and reached over 70% application after UX changes. Martian's independent benchmark (13–17 tools, 200–300k real PRs, adoption-based scoring) puts the best tools at F1 of about 51–62%; a tool with roughly two false positives per run is preferred over one with eleven.
- **Measure the reviewer**: seeded-defect corpora with classes such as logic, security, test-tamper, spec-miss and wiring, never planted by the models under test (no_human).
- **Verification over review**: give the agent a check it can run and demand evidence, not assertions ("no completion claims without fresh verification evidence").

### 7.3 Models and effort

- **Split reasoning from editing**: Aider's architect/editor pairing raised pass rates (o1-preview plus DeepSeek 85.0% versus Claude 3.5 Sonnet alone 77.4%); "a cheap executor with an expensive advisor more than doubled the cheap model's solo score" at up to 14x lower cost.
- **Role-based tiers**: strong reasoning model for planning and architecture, mid or fast model for implementation, a dedicated reviewer model, optionally from another family (Osmani, Kilo, no_human with a Sonnet-tier coder and an Opus-tier reviewer, Amp's oracle for hard reasoning only). Amp routes by "mode" (low/medium/high/ultra) that bundles model, effort, tools and oracle: "choose a mode based on the task rather than the model name."
- **Least powerful model per role, with escalation**: cheapest tier when the plan already contains the code (transcription plus tests); mid-tier floor for reviewers and prose-based implementers; one tier up after three failed rounds; always set the model explicitly because an omitted model inherits the most expensive one; "turn count beats token price: the cheapest models routinely take 2–3× the turns" (superpowers).
- **Effort levels**: Claude Code defaults to `high`, with `max` "prone to overthinking"; Codex defaults to `medium` and advises "start with the default effort and increase it when the task needs deeper planning or analysis"; Anthropic's research system scales subagent count and tool calls to query complexity; Symphony's reference runs everything at `xhigh`, which is a cost-insensitive choice.
- **Never a silent fallback** (AO's newest rule, Director's invariant 12).
- **Token economics**: input tokens dominate (94.5% cached reads in a documented session); context re-reading is the cost; subagents are for context control, not role-play; routing without verification "pushes risk downstream"; the metric is cost per verified change.

### 7.4 Skills

- **Format and discipline** (Anthropic): metadata, body and files as three levels of progressive disclosure; body under 500 lines; references one level deep; descriptions in third person stating what and when; degrees of freedom matched to fragility (exact scripts for fragile operations, heuristics for open ones); scripts that solve rather than defer, no unexplained constants; three evaluations written before the documentation; one model authors, another tests.
- **Skills that work in practice**: superpowers (brainstorm, plan with exact files and 2–5 minute steps, subagent-driven TDD, two-stage review, verification before completion, rationalization tables that pre-empt the excuses agents make); Symphony's land/pull/push/commit with pushback templates and ambiguity gates; Gas Town command bodies and formulas; no_human verifiers; Claude Code `/code-review`, `/security-review` and `/verify`.
- **Context files are overrated**: repository-level AGENTS.md files did not improve success and raised cost by over 20% on average; LLM-generated files reduced success by about 3%, developer-written ones improved it by about 4% at 19% more cost; instructions are followed, repository overviews are not useful. Keep them short (under 80 lines), start empty and add a line only when an agent repeatedly makes the same mistake. Across 2,853 repositories, skills and subagents are rarely adopted and skills are mostly static text.
- **Deterministic things belong in hooks and scripts, not prose**: "hooks are deterministic; CLAUDE.md instructions are advisory."

### 7.5 Candidate inputs for Director

- Scheduler: ready queue with per-state limits, reconciliation before dispatch, stall detection, kill criteria (three stuck rounds), budget pause at 85%, plan approval as an optional launch gate.
- Review: deterministic pre-gates, structured findings with verified citations, spec-then-quality stages, superseded-on-push, bounded corrections, a seeded-defect corpus to measure the reviewer, and a precision target.
- Profiles: per-phase effort inside a Run, escalation one tier up on stuck, explicit model on every helper, budgets in turns as well as tokens, reviewer from another family as an option.
- Skills: evaluations per skill, bodies under 500 lines, deterministic checks moved to hooks and scripts, verification-before-completion and receiving-review patterns, Organizer skills read from the trusted base ref.

### Sources for section 7

Osmani "Orchestrating Coding Agents" (O'Reilly CodeCon 2026) and "The Code Agent Orchestra"; HumanLayer "Advanced Context Engineering for Coding Agents" (YC talk write-up) and the Pragmatic Engineer interview; Simon Willison's Pragmatic Summit talk notes; Kilo Code engineering post; Iron Traveler Labs backlog runs; Claude Code best practices, model configuration and agent teams docs; Codex model and AGENTS.md docs; Anthropic Agent Skills post and skill authoring guide; obra/superpowers skills; OpenAI Symphony; Gas Town; Agent Orchestrator; no_human verification and blockers docs; open-swe reviewer architecture; Aider architect/editor post; Amp models and subagents docs; Sonar model routing; Augment token spend analysis; Martian Code Review Bench and vendor summaries; Google "Resolving code review comments with ML"; arXiv 2602.11988, 2607.27250, 2602.14690, 2604.03515.

## 8. Gap analysis against the approved plan (2026-09-07)

### 8.1 Already covered

| Lesson | Where |
|---|---|
| Review anchored to an exact SHA; any change invalidates it | Invariants 3 and 5, PLAN §13.3 |
| Independent reviewer, detached disposable checkout, no author conversation | PLAN §13.3, ADR-0010, `director-independent-review` |
| An agent's claim is not evidence | "Do not accept test output quoted only by the author", `director-independent-review` |
| Candidate approval is not Task completion | Post-review gates in `director-independent-review` and `director-task-workflow` |
| Never a silent model or delivery fallback | Invariant 12 |
| One agent per Task, isolated worktree, no parent | Invariant 1, ADR-0010 |
| Bounded correction loops instead of infinite retry | PLAN §13.4: three corrections, four CI cycles, mandatory time limit |
| Never rerun the same failed commit; batch findings before correcting | PLAN §13.4 |
| Deterministic effects live outside the prompt | Engine-owned effects with intent/evidence, PLAN §6.3 |
| Reproducible instruction set per Run | Run freezes profile, policy and skill/template hashes, PLAN §5.6 |
| Keep context files short | AGENTS.md is 72 lines; skills are 45–78 lines; "concise and procedural, rationale in ADRs" |

Two areas where this plan is ahead of the surveyed field: the intent/evidence pattern per effect, and freezing skill and template hashes into the Run, which makes it possible to know exactly which instructions produced a Candidate.

### 8.2 Worth evaluating later

Cheap, process-sized:

- Require an explicit model on every helper subagent; an omitted model inherits the session's most expensive one and defeats any cost policy.
- Calibrate the reviewer to report only findings affecting correctness or stated requirements; treat the rest as optional.
- State a task-sizing rule based on reviewability of the diff.
- Name verification-before-completion as one rule with a gate function instead of spreading it across four skills.
- **Soft budget threshold: warn and pause at a fraction of the budget before hard exhaustion sends the Task to `Needs you`.** Natural home: `dir-m3.6` (runtime time and cost budgets), with PLAN §13.4 as the affected text.

Needs a spike and an ADR, in leverage order:

1. Deterministic gates before the model reviewer: tamper guard for deleted tests, new skips and tautological assertions; a reproduction gate requiring the test to fail at the base and pass on the Candidate; per-path verifiers. Fits beside `dir-m4.2`.
2. Per-phase model and effort inside a Run (plan, implement, review), which requires deciding whether a frozen Run can hold more than one model entry.
3. Per-state concurrency limits, so review capacity is scheduled rather than assumed.
4. Stall detection distinct from failure; today replacement is failure-driven and a silent but live agent triggers nothing.
5. Model-tier escalation after repeated failed correction rounds.
6. Plan approval as an optional launch gate, the highest-leverage human review point in the literature.
7. Measuring the reviewer: a seeded-defect corpus and a declared precision target.
8. Three evaluations per skill before writing its documentation.

### 8.3 Deliberately not adopted

Growing AGENTS.md (measured to raise cost without improving success), inter-agent messaging or shared task lists between agents (inter-agent misalignment is the largest catalogued failure class, and the plan already excludes PR comments as a bus), and Ralph-style unbounded loops (incompatible with evidence-and-gate delivery).

### 8.4 Owner triage

On 2026-09-07 the project owner reviewed this analysis and marked only the soft budget threshold as relevant a priori. Everything else in 8.2 is parked as reference, not queued. No task was created from this document.

On 2026-09-07 the owner also deferred the deterministic maintainability ratchet (AST-based structural budget with frozen offenders) to a later version: the thresholds are an opinion about someone else's code and do not belong in a general orchestration product. The non-opinionated form is already expressible: a project declares the gate as one of its configured checks, and Director treats its exit code as evidence without knowing what it measures.
