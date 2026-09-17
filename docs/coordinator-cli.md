# Coordinator CLI

`director-coordinator` is the repository delivery tool for an authorized
coordinator. It batches the Beads, Git, GitHub, and documented Paseo CLI reads
needed around one exact Task Candidate. It is dependency-free, Linux-only, and
uses direct process argument arrays without a shell. Its state locks use the
Linux `/usr/bin/flock` primitive supplied by util-linux; inability to acquire
that kernel lock fails closed before state is read or changed.

The Task Agent may run only `snapshot` and `review-handoff`. Publication,
integration, and lifecycle cleanup remain coordinator-only.

## Contract

Every command requires an explicit immutable context:

```text
--task --actor --repo --repo-id --remote --base-ref --base
--branch --candidate --head-owner --ownership-file
--checkout --checkout-state --control-repo
--agent-id --workspace-id --lifecycle-state --pr
```

`--base` and `--candidate` are full 40-character object IDs. The branch must
be `task/<task-id>-...` and cannot equal `main`, `master`, or the base ref.
Repository database ID, Git remote, live base, local/remote Task refs, shared
Git common directory, Task record, and any available Paseo agent/workspace are
re-read and matched. `--ownership-file` must be an absolute owner-only regular
file with mode `0600`; raw `--ownership` is refused so the opaque token cannot
enter argv or command logs. Only its SHA-256 is written to the non-secret
manifest and public PR marker. PR repository/base/head metadata, title, body,
and every child argv are rejected when they contain the token or ownership-file
path. Neither the token nor its path belongs in Beads.

Use `--checkout-state present` while the exact owned Task worktree remains.
Use `--checkout-state reclaimed` only when both its path and Git registration
are absent but the exact local Task ref remains at the Candidate. Post-handoff
`publish`, `gate`, `integrate`, `cleanup-plan`, and `cleanup-apply` then operate
from `--control-repo` without recreating a worktree. The checkout's immediate
parent may also be absent: the coordinator canonicalizes the missing suffix
below its nearest existing directory without creating anything, while the
exact local-ref, registration-absence, repository, Candidate, and base checks
remain mandatory. Use
`--lifecycle-state active` or `restored` with two exact Paseo IDs while the
current/recovered resource facts remain observable; a handoff recorded active
may later verify as restored without changing its identity. Use `reclaimed`
with those same recorded IDs for historical facts after the one-shot schedule
reclaims them. Reclaimed lifecycle bindings are reported as recorded inputs
with `verified: false`, never as verified archival. The literal `none` for both
IDs is valid only when the Run never had those resources.

The Reviewer leg binds its own resources, because a Reviewer is a second
parentless agent with its own host view and its own disposable checkout:

```text
--reviewer-agent-id --reviewer-workspace-id --reviewer-lifecycle-state
--reviewer-checkout --reviewer-checkout-state --reviewer-review-state
```

All six are required by `reviewer-cleanup-plan` and `reviewer-cleanup-apply`
and refused by every other command. `--reviewer-workspace-id none` is valid
when the Review had no host view or it is already gone. The Reviewer's two
lifecycle facts are observed independently rather than inferred from one
another: archiving the host view of an independent clone leaves the checkout in
place, so `--reviewer-lifecycle-state reclaimed` with
`--reviewer-checkout-state present` is a real state. A disposable checkout reaped
from a temporary filesystem is bound `--reviewer-checkout-state reclaimed`: the
owner marker exists to authorise a removal, so where there is nothing to remove
it is not required, and its absence is recorded as the reason rather than
standing in for a proof never taken. An agent whose checkout is gone is still
archived normally. `reclaimed` is an assertion
the daemon is asked to confirm, not one taken on trust: the Reviewer agent is
inspected under every lifecycle binding, and a binding that calls a still-live
agent or a still-live workspace card historical refuses
`REVIEWER_LIFECYCLE_NOT_RECLAIMED`. The running guard and the frozen-label
identity binding therefore apply under every binding rather than lapsing at the
one an operator reaches for when they believe the resource is already gone. A Reviewer identity equal
to the Task Agent, the coordinator actor, or the Task workspace is refused, as
is a Reviewer checkout that overlaps the Task or control checkout.
`--reviewer-review-state` is the explicit statement of why the Review is over:
`verdict_recorded` requires a durable report in Beads that binds this exact
Reviewer, and `abandoned` requires that no report binds it and that the Task is
no longer in progress. `abandoned` asserts that no durable report binds this
Reviewer, which is not the same as asserting that the Review never reported; it
is refused outright where either counter is above zero.

While the Task is still in progress, `abandoned` additionally requires a durable
abandonment record, because nothing observable distinguishes a Reviewer that
will never report from one that has not reported yet: both are idle, both carry
the same labels, and neither can be asked. The difference is not a property of
the Reviewer but a decision by the party that stopped waiting for it, so it is
bound the way a report is — by the actor that wrote it. A comment whose own
first line begins `REVIEW ABANDONED`, written under the actor running the
cleanup, naming the Reviewer it abandons, discharges the in-progress refusal and
nothing else. Without it the refusal stands, which is what keeps a Reviewer
idle between turns of a running Review untouchable. Three Reviewers of closed
Tasks need no such record; the case this exists for is the live one, where a
Candidate is superseded after its Reviewer already exists.

A running Reviewer is refused under
either value.

A report binds through exactly one fact: the Reviewer wrote it. The comment's
author must equal the Reviewer's own `paseo:<agent id>`, and the comment must
state a verdict — a `Verdict:` field carrying any token, or one of the three
contract verdicts on the comment's own first line. The first-line restriction is
load-bearing: read over the whole text, a note a Reviewer writes mid-Review
while quoting the previous round's verdict binds as that Review's report, and a
Reviewer sits idle between turns.

Authorship is the strongest available binding, not an unforgeable one. Beads
writes the author row, but the caller supplies the value: `bd` accepts a
free-form `--actor` with no authentication, `$BEADS_ACTOR` does the same, and
`bd export`/`import` round-trips the field verbatim, so a deliberate or scripted
write under another agent's actor is a route. Nothing in this repository writes
a comment programmatically — the coordinator only reads them — and every content
rule this replaced was strictly easier to produce, since anyone could write a
heading or a `Candidate:` line under their own actor. The residual is recorded
as `dir-m6.48`, and this contract rests on it.
Every content rule tried here could be satisfied by a comment that describes a
Review rather than being one: the routine record announcing that a Reviewer was
created names that Reviewer, names the Candidate it was created for, and quotes
an earlier Review's verdict, so three tokens co-occur where no report exists.
Narrowing which tokens, or how near they must stand, makes such a record harder
to mistake without making it distinguishable. The verdict requirement closes the
matching gap on the other side: without it, any note a Reviewer writes
mid-Review — and it sits idle between turns — would read as a concluded Review,
leaving the daemon's `running` status as the only thing protecting a Review in
flight.

Within what a comment contains, this rule fails by missing rather than by
inventing; it does not defend against a comment written under a forged actor.
Three misses are known and none is hypothetical: a verdict the coordinator
transcribed on the Reviewer's behalf binds nothing; a report whose verdict is
stated in neither readable place binds nothing, including the transcribed form
whose verdict sits in prose; and a report concluding outside the contract
vocabulary without a `Verdict:` field binds nothing. Every miss is refused, not merely reported. `unboundEvidence` counts comments
that name this Reviewer — by actor or by bare identifier — and refer to a
verdict without binding it, which is what a transcribed report leaves.
`unreadableReports` counts comments this Reviewer wrote whose verdict could not
be read. Both count a reference to a verdict *anywhere*, deliberately wider than
what binds: if they narrowed alongside the binding rule, the reports the
narrowing newly declines to read would become invisible at the same moment.
Either counter above zero refuses `abandoned` with
`REVIEWER_REPORT_EVIDENCE_UNRESOLVED`, so the surrendered coverage is a refusal
rather than a sentence an operator is trusted to read. Neither counter ever
widens `verdict_recorded`; that would be the withdrawn content anchor returning
through a counter. A Reviewer in this state is reconcilable only after a human
resolves what the evidence means.

Mutating commands also require an absolute `--state-file` outside every Git
checkout. The file is mode `0600`, atomically replaced and filesystem-synced.
It records immutable bindings and effect phases before dispatch. A private
stable guard file holds a nonblocking Linux kernel lock while the exact
process-identity record is inspected or atomically replaced. This gives one
concurrent stale-lock reclaimer; a stale record is replaced only after its
exact PID/start-time identity is absent, and no second process can interleave a
state write. Preserve the state file and its mode-`0600` `.lock.guard` sibling
through publication, integration, and cleanup.

Coordinator state schema v2 stores `ownershipTokenHash` in its immutable
binding; it never stores the raw ownership value or ownership-file path. A
schema-v1 private state whose sole raw copy is the exact legacy
`binding.ownership` value is atomically migrated under the state lock after the
current mode-`0600` input proves the same ownership. Migration drops any
legacy derived `cleanupPlanHash`; after restart or response loss, a new v2
`cleanup-plan` must be generated and can then be admitted by `cleanup-apply`.
A legacy lock is adopted only by its hash and only after its recorded process
identity is absent. Raw
ownership anywhere else in legacy state refuses with
`STATE_LEGACY_OWNERSHIP_UNSAFE`; preserve all resources and the private file,
restrict access to its owner, and have the authorized coordinator inspect and
repair or replace that control state before retrying. A mismatched ownership
input or binding refuses without disclosing either value.

Output is one compact JSON document. Exit `0` means the requested result is
proven. Exit `2` is a structured fail-closed refusal or interruption. Exit `1`
is an unexpected internal failure. Raw command output, credentials, paths from
GitHub feedback, and comment bodies are not copied into refusals.

For an active lifecycle binding, for every Reviewer command, and for the
Reviewer-leg ordering check the two cleanup commands perform regardless of
their own lifecycle binding, a password-protected exact Paseo `0.7.2` daemon
requires that `DIRECTOR_PASEO_CREDENTIAL_FILE` name the existing absolute
owner-only regular credential file with mode `0600`. The file contains only
the exact password bytes, with no added line ending. It is the sole password
authority: plaintext `PASEO_PASSWORD` is ignored, and a password in
`PASEO_HOST`, an option, or a connection URI is refused. Keep this credential
file separate from the required `--ownership-file`; neither raw value nor
either file path may enter output, diagnostics, evidence, handoff/state, PR
content, or Beads.

The coordinator uses exact Paseo `0.7.2`'s public Agent MCP `get_agent_status`,
`list_workspaces` and `list_agents` reads and its `archive_agent` and
`archive_workspace` mutations. The enumeration is issued with fixed bounded
arguments and projects only each agent's lifecycle fields and its own
`director.` labels. Without `PASEO_HOST`, a password-free `paseo daemon status --json`
child discovers the documented same-host Unix socket, loopback, or active
local-interface listener. An explicit host is bounded to a local Unix socket or
loopback TCP target and must remain credential-free. Only the dedicated Agent
MCP lifecycle child receives the password, through its exact `PASEO_PASSWORD`
environment; its argv contains only the fixed child, one of those five
operations, and the public agent or workspace ID. Git, GitHub CLI, Beads, npm,
Go, Paseo status, other Paseo verbs, and sibling executables receive neither
the password nor credential-file path. That child authenticates over HTTP
bearer, which carries a valid delimiter-rich password the CLI's WebSocket
subprotocol grammar cannot express. A read emits only the lifecycle fields
needed for the existing exact parentless-agent/workspace/worktree verification.
A mutation emits only a fixed acknowledgement that it was accepted, never
daemon payload, because completion is proven solely by the authoritative
readback that follows. It size-bounds and scrubs every response and error
before returning anything to the coordinator.

Missing or rejected authentication returns `PASEO_AUTH_REQUIRED` or
`PASEO_AUTH_FAILED`; malformed output, process failure, timeout, response loss,
or an unavailable target returns `PASEO_LIFECYCLE_READ_FAILED` for a read and
`PASEO_LIFECYCLE_MUTATION_FAILED` for a mutation; and a response that echoes
the selected credential returns `PASEO_LIFECYCLE_RESPONSE_REDACTED`. Each
refusal is bounded and retains no daemon response content. An archive dispatch
raises a proven credential refusal or an echoed credential exactly, and leaves
any other unproven outcome to the readback, so a lost response reconciles
instead of re-executing and no refusal is reported as an unproven archive.

## Commands

- `snapshot` returns the bound Task, clean local Candidate, live base/head,
  canonical GitHub repository and owned PR, plus exact top-level Task Agent and
  isolated workspace facts when supplied.
- `review-handoff` repeats the fresh snapshot and emits the exact detached
  Candidate/base handoff. It refuses a dirty worktree, moved branch, changed
  base, missing commit, wrong repository, parented agent, or mismatched
  workspace. `--validation-file` is required. The result includes an automatic
  `authoritative: false` manifest binding the Task acceptance text, current
  human-decision and prior-finding comment hashes, Candidate/base trees, raw
  diff hash and changed paths, author validation references, repository/branch
  ownership, and every pending post-review gate. Its manifest hash accelerates
  routing but is never review evidence. Optional `--handoff-file` atomically
  maintains one mode-`0600` copy outside the Task and control checkouts.
- `publish-draft` requires `--state-file`, `--manifest-file`,
  `--validation-file`, `--expected-remote-head`, `--title`, and `--body-file`.
  It refuses blocked Tasks, pushes only the owned branch under an exact lease,
  and creates or updates one marked draft. A corrected Candidate updates that
  same open draft with an exact old-head lease. Its result always reports
  `mergeAuthorized: false`. Owned-PR discovery is limited to one 100-entry
  GitHub page and refuses a saturated page as incomplete before reasoning
  about ownership or creating a PR.
- `remote-ci` requires `--state-file`, `--validation-file`, `--ci-workflow`,
  and one or more `--required-check` values. It records the unique completed
  GitHub workflow run for the exact Candidate/base plus exactly one instance of
  every passing required check. Missing, duplicate, pending, failed, or
  incompletely paginated facts fail closed. The observation ID is the sole
  complete-CI authority for that Candidate.
- `publish` requires `--state-file`, `--review-file`, `--manifest-file`,
  `--review-harness-file`, `--remote-ci-file`, `--validation-file`,
  `--expected-remote-head`, `--title`, and `--body-file`. The
  manifest/harness/CI tuple must bind the same Task/Candidate/base and prove
  that harness version 2 consumed the exact authoritative remote observation.
  It pushes only the owned Task ref with an exact lease, adopts the one owned
  draft, and marks it ready. Closed historical PRs do not alias or block a new
  owned PR; an unmarked open PR still fails closed.
- `gate` requires the exact PR number, review/manifest/harness/remote-CI/
  validation files, and one or more repeated `--required-check` values. It
  requires the live base/head tuple,
  open non-draft mergeability, every observed check/status to pass, each named
  check exactly once, and a complete bounded commit-status page. When that
  page contains no legacy status contexts, the successful required Check Runs
  produce an explicit `checks_only_no_statuses` rollup; otherwise the combined
  status and every exact-Candidate context must be successful. The gate also requires
  no requested reviewers, no current changes-requested or
  ambiguous review, no human issue comment, and no unresolved human review
  thread. It re-reads Beads and refuses if the manifest's binding human-decision
  or prior rejected-review reference set changed. `publish` and the final
  pre-merge gate apply the same durable-context comparison. The Reviewer UUID
  and remote observation ID must match exactly.
- `integrate` adds `--state-file`, repeats the complete gate immediately before
  mutation, proves that installed `gh` supports `--match-head-commit`, and
  invokes merge mode with that exact Candidate guard. Success requires GitHub
  merged state plus an exact `[base, Candidate]` parent list, a merge tree equal
  to the Candidate tree, and reachability from the fresh live base. If the
  forge merges after an undetectable base race, the state becomes
  `needs_manual_reconciliation`; cleanup remains blocked while the coordinator
  preserves every resource, records the observed merge/failure code, and asks
  the owner to decide repair or acceptance. Retrying cannot convert that
  anomaly into verified integration.
- `reviewer-survey` derives the set of Reviewers awaiting reconciliation from
  the daemon and the Beads record at the moment it runs, and mutates nothing.
  It reads one bounded page of live agents, keeps those whose own
  `director.role` label is `reviewer`, and joins each with its Task's status and
  durable Review verdicts. Every Reviewer is classified `reconcilable`,
  `review_in_flight`, `review_incomplete`, or `ambiguous`, including Reviewers
  of closed Tasks, of superseded Candidates, and of Reviews that never reported.
  Each record carries `reportSource`, which is `authored` or `null`, plus
  `unboundEvidence`, `unreadableReports` and `abandonedBy`, so a record says
  what its conclusion rests on and what it could not read. `reviewState` is
  derived from what the leg will actually accept rather than from the
  classification, so a row the leg refuses never displays the binding it
  refuses — an operator acts on the display.
  Each record's `reviewState` is the `--reviewer-review-state` that record
  admits, and `null` where it admits none, so a Review still in flight is never
  described as finished. The result carries the requested page limit and a
  `saturated` flag: a page returned at its own limit is evidence of nothing
  beyond itself, so a truncated enumeration never reads as a complete one. Adopt
  nothing from an `ambiguous` record; it names a resource this contract cannot
  identify.
- `reviewer-cleanup-plan` emits a hashed plan for exactly one bound Reviewer:
  its agent, its host view, and its owner-marked disposable checkout. It
  requires no integration evidence and no in-progress Task, because Reviewer
  cleanup is authorized by durable verdict evidence rather than by delivery.
  Plan schema v1 binds the Reviewer fields alongside the delivery binding.
- `reviewer-cleanup-apply` requires `--state-file` and `--plan-file` and
  performs the leg in the documented order: archive the Reviewer, archive the
  host view, then remove the exact owner-marked detached checkout. Each effect
  records intent before dispatch, an already-terminal resource is adopted
  without another dispatch, and the destructive removal accepts absence only
  after a recorded attempt or an explicit reclaimed binding. Removal re-proves
  the marker and the directory's device and inode immediately before it runs. It
  accepts the same optional `--resume-state-file`.
- `cleanup-plan` requires the completed integration state and emits a hashed
  plan for only the exact agent, workspace/worktree, local Task ref, and remote
  Task ref. It also requires `--review-file` and refuses `REVIEWER_LEG_PENDING`
  unless the exact Reviewer named by that durable Review evidence is already
  archived and no other live agent carries this Task's own Reviewer labels.
  That second rule is what a corrected Candidate needs: each correction creates
  a new Reviewer and leaves the previous one alive, so ordering only the last
  Review would let every earlier Reviewer outlive the Task. The emitted plan
  records the enumeration's own `saturated` flag, so a closure record derived
  from it inherits the scope the check could actually establish. Dirty or ignored data, a running agent, ambiguous ownership, or a
  changed ref refuses the plan. Cleanup-plan schema v2 binds only
  `ownershipTokenHash`. Schema-v1 plans are not migrated because they are
  derived artifacts. State migration also invalidates any legacy
  `cleanupPlanHash`; after migration or response loss, rerun `cleanup-plan` and
  use that newly emitted v2 document without editing private state. Optional
  `--resume-state-file` carries an interrupted run's recorded effects across its
  own lifecycle transition; see Resuming an interrupted cleanup.
- `cleanup-apply` requires `--state-file`, `--plan-file`, and the same
  `--review-file`, and repeats the Reviewer-leg ordering check before its first
  effect, so a Reviewer that came back preserves every Task resource. The plan file may
  be the complete JSON emitted by `cleanup-plan`. Every resource is re-read
  before its effect. Agent and workspace archival are dispatched through the
  same bounded authenticated Agent MCP child as the lifecycle reads and are
  proven only by the readback that follows, so an already-terminal resource is
  adopted without another dispatch. Archival is idempotent; destructive
  worktree/ref deletion accepts absence only after a recorded attempt or an
  explicit reclaimed binding. If Paseo workspace archival leaves an exact
  clean owned Git worktree registered, the CLI performs the separately recorded
  non-force Git removal step before touching refs. A
  present worktree after possible handoff is preserved and routed to a
  refusal. A nonterminal `dispatching` or `unknown` local/remote Task-ref
  deletion may retry only after authoritative observation proves the ref still
  equals the exact Candidate, and only through the same explicit expected-OID
  `force-with-lease`/`update-ref` guard. Absence adopts completion; a changed,
  unavailable, or ambiguous ref fails closed. Completion remains terminal on
  later reappearance. It accepts the same optional `--resume-state-file`.

`publish` and `integrate` additionally consume these closed evidence files:

```json
{"schemaVersion":1,"task":"dir-x","candidate":"<sha>","base":"<sha>","verdict":"approve_candidate","reviewer":{"agentId":"<id>","parentAgentId":null,"detached":true,"checkoutCommit":"<sha>"},"dimensions":[{"id":"acceptance","status":"covered"},{"id":"correctness","status":"covered"},{"id":"security","status":"covered"},{"id":"maintainability","status":"covered"},{"id":"readability","status":"covered"},{"id":"design","status":"covered"},{"id":"quality","status":"covered"},{"id":"rigor","status":"covered"}],"p2Risks":[],"humanP2Acceptance":null}
```

```json
{"schemaVersion":1,"task":"dir-x","candidate":"<sha>","base":"<sha>","checks":[{"id":"maintained-linux-ci","command":["npm","run","ci"],"status":"passed"}]}
```

If review lists a P2 risk, `humanP2Acceptance` must instead contain
`actorKind: "human"`, a durable `recordId`, and the exact set of accepted risk
codes. Unknown fields or looser verdict/status values are rejected.

## Resuming an interrupted cleanup

A cleanup's own effects advance the binding it runs from. Archiving the Task
Agent and the Paseo workspace makes `--lifecycle-state` historical, and removing
the worktree reclaims `--checkout-state`. Both fields are immutable in a state
file and the state lock binds their hash, so an interrupted cleanup leaves its
recorded intents in a state whose binding can no longer be asserted truthfully,
while a fresh state bound to the transitioned truth knows nothing about them and
correctly refuses `DESTRUCTIVE_ABSENCE_AMBIGUOUS` for a ref this coordinator had
already recorded deleting.

The Reviewer leg advances its own two fields the same way: archiving the
Reviewer makes `--reviewer-lifecycle-state` historical, and removing the
disposable checkout reclaims `--reviewer-checkout-state`. Lifecycle progress is
compared only over the fields a binding actually carries, so a command that
binds no Reviewer keeps exactly its original two-field comparison.

`cleanup-plan`, `cleanup-apply`, `reviewer-cleanup-plan` and
`reviewer-cleanup-apply` accept `--resume-state-file` to close that gap. It names the interrupted run's absolute owner-only mode-`0600` state file
outside every Git checkout, and that file is only ever read: the resumed run
writes exclusively to its own `--state-file`, which it may create. The resumed
state is admitted only when every identity, ownership, actor, repository,
branch, Candidate, and base field equals the current binding exactly and the
sole difference is lifecycle progress that advanced to the terminal value
cleanup itself produces: `present` to `reclaimed` for the checkout, and `active`
or `restored` to `reclaimed` for the lifecycle. `restored` is a recovery fact
produced by something other than cleanup, so it is an admissible source and
never an admissible target; `cleanup-apply` has no `restored` effect path, and a
resumed run that targeted it would carry an archive intent it can neither
dispatch nor terminalize. A non-terminal target refuses
`RESUME_STATE_LIFECYCLE_NOT_RECLAIMED`, an identical binding refuses
`RESUME_STATE_NOT_A_TRANSITION` because that state is usable directly, a
backward binding refuses `RESUME_STATE_LIFECYCLE_REGRESSION`, and any other
difference refuses `RESUME_STATE_BINDING_MISMATCH`. A state continues exactly
one interrupted binding; a second refuses `RESUME_STATE_REPLACED`. A missing,
world-readable, foreign-owned, oversized, legacy-schema, or structurally invalid
file is refused without being written to or repaired.

Admission copies only the recorded effects the current state does not already
own. It executes nothing, adopts no resource, and relaxes no observation: every
downstream admission, re-read, and refusal is unchanged, so an absent ref
explained by a carried deletion intent completes through the existing
[ADR-0021](adr/0021-recover-exact-leased-ref-cleanup.md) rule while an absence
no recorded intent explains anywhere still fails closed. The derived
`cleanupPlanHash` is deliberately not carried, because a plan is bound to the
lifecycle binding that produced it: run `cleanup-plan` under the transitioned
binding and admit that document. A `--lifecycle-state reclaimed` cleanup now
also records the agent and workspace effects it adopts as recorded-reclaimed
rather than skipping them silently, so an archive interrupted before the
transition still reaches a terminal phase and the closure record states the
scope cleanup actually covered.

```text
node tools/coordinator/cli.mjs cleanup-plan <transitioned context> --state-file /private/control/resumed-state.json --resume-state-file /private/control/interrupted-state.json > /private/control/cleanup-plan.json
node tools/coordinator/cli.mjs cleanup-apply <same context> --state-file /private/control/resumed-state.json --resume-state-file /private/control/interrupted-state.json --plan-file /private/control/cleanup-plan.json
```

## Typical handoff

Run the executable through the repository package:

```text
node tools/coordinator/cli.mjs review-handoff <immutable context options>
node tools/coordinator/cli.mjs publish-draft <immutable context and draft evidence>
node tools/coordinator/cli.mjs remote-ci <immutable context and CI workflow/checks>
node tools/coordinator/cli.mjs publish <immutable context options and publication evidence>
node tools/coordinator/cli.mjs gate <immutable context options and required checks>
node tools/coordinator/cli.mjs integrate <same gate options plus state file>
node tools/coordinator/cli.mjs reviewer-survey <immutable context options>
node tools/coordinator/cli.mjs reviewer-cleanup-plan <immutable context plus Reviewer binding and state file> > /private/control/reviewer-plan.json
node tools/coordinator/cli.mjs reviewer-cleanup-apply <same context plus state and plan files>
node tools/coordinator/cli.mjs cleanup-plan <immutable context plus state and review files> > /private/control/cleanup-plan.json
node tools/coordinator/cli.mjs cleanup-apply <same context plus state, review, and plan files>
```

The Reviewer leg precedes the Task Agent leg, and `cleanup-plan` refuses until
it has run. Reviewers left behind by earlier Runs are reconciled through the
same two commands: `reviewer-survey` derives the current set, and each
`reconcilable` record supplies the binding for one plan/apply pair. Nothing in
that path requires the Task to be open or the Candidate to be reachable.

The coordinator records each returned JSON result in Beads under its explicit
`paseo:<agent-id>` actor. It never pushes `main`, uses `--admin`/`--auto`,
resolves GitHub feedback, force-removes a worktree, deletes an unknown ref, or
silently changes repository, delivery mode, merge strategy, or authority.

## Independent review harness

The versioned `director-review-harness` is the standard entrypoint for an
exact-Candidate review. Give it the complete `review-handoff` JSON, the
detached reviewer checkout, a private state file outside that checkout, the
exact Reviewer Agent ID, its Paseo actor, the authoritative remote-CI JSON, and
`--reason initial`. Only `git` is required on the harness child `PATH`; the
remote-only review checkout needs no dependency installation.

```text
node tools/coordinator/review-harness.mjs run --manifest /private/control/handoff.json --checkout /exact/detached/reviewer --state-file /private/control/review-state.json --remote-ci-file /private/control/remote-ci.json --review-id reviewer-agent-id --actor paseo:reviewer-agent-id --reason initial
```

Harness version 2 consumes the authoritative complete remote Linux CI and runs
the mechanical manifest-identity check concurrently, then sorts their
structured results by check ID. It never starts local or remote CI. The
identity check directly re-reads the Beads acceptance/human-decision/finding
references, detached Candidate and trees, raw diff, clean state, GitHub remote,
and live base SHA. This mechanically proves unchanged immutable history; it
does not turn the manifest into evidence.

Private review-state schema v2 keys the one complete-CI authority to the exact
Candidate/base pair. Manifest hashes are append-only observations inside that
budget, so a lifecycle or ownership rebinding cannot create another CI budget;
the same Reviewer can verify the new manifest while reusing the exact recorded
remote observation. Replay after response loss adopts the manifest-bound
stored result. A different Reviewer, observation, or complete-CI source is
refused. Schema-v1 manifest-keyed state is migrated in place: its original
entries, observed manifest hashes, and recorded invalid-environment or
failure-confirmation exception reasons remain private history. The harness
uses the same kernel-serialized exact process-identity lock, so concurrent
stale reclaimers cannot consume observations or lose a state record.

The maintained adversarial probes under `tools/coordinator/*.test.mjs` use
disposable repositories internally and run through CI. Reviewers invoke these
versioned probes through the harness and do not reconstruct equivalent `/tmp`
experiments. A genuinely novel one-off probe is permitted only when a finding
promotes regression coverage into the Candidate or a scheduled follow-up.
