# ADR-0015: Use durable commands, fenced attempts, and observed evidence for every effect

- **Status:** Accepted
- **Date:** 2026-09-06
- **Beads Task:** `dir-m0.10`
- **Plan gate:** M0 idempotent effect and reconciliation design
- **Decision owner:** `dir-m0.10` Task Agent
- **Aligned with:** [ADR-0003](0003-paseo-0.7.2-lifecycle-recovery.md),
  [ADR-0004](0004-select-direct-dolt-taskstore.md),
  [ADR-0005](0005-session-scoped-mcp-provider-matrix.md),
  [ADR-0006](0006-exact-sha-github-delivery.md),
  [ADR-0007](0007-git-worktree-ownership-and-cleanup.md),
  [ADR-0008](0008-director-threat-model.md),
  [ADR-0010](0010-top-level-task-agent-parentage.md),
  [ADR-0011](0011-linux-only-platform-scope.md),
  [ADR-0012](0012-direct-dolt-operational-readiness.md), and
  [ADR-0013](0013-scalable-ignored-tree-recovery-policy.md), and
  [ADR-0014](0014-practical-linux-agent-boundary.md)
- **Amended by:** [ADR-0020](0020-zero-work-bootstrap-and-terminal-event-dispatch.md); [ADR-0021](0021-recover-exact-leased-ref-cleanup.md), which permits a fresh exact-guarded retry for nonterminal local and remote Task-ref deletion only

## Context

Director coordinates state in direct Dolt, Paseo, product Git repositories,
GitHub, Organizer Git, local filesystems, and operating-system processes. None
of those external systems participates in a transaction with the TaskStore.
A crash may therefore happen after an intent commits but before dispatch,
after dispatch but before a response, or after an authoritative observation
but before the aggregate projection commits.

The independently approved M0 evidence supplies effect-specific facts:

- Paseo records and provider sessions survive several plugin and daemon
  interruptions, while `running` without an active turn and `closed` without
  `archivedAt` are ambiguous. ADR-0020 limits top-level creation to a zero-work
  bootstrap; a lost create result must be reconciled by exact labels before
  another create. The separate notified real prompt is nonrepeatable after a
  possible handoff. Archive is retryable, but termination requires `closed`, non-null
  `archivedAt`, and reconciled process facts.
- The admitted native providers accept an exact session-scoped MCP catalog and
  tool policy on the tested tuple. The bridge is fixed to one scope, remains a
  command ingress, and is not a host-security or workflow authority boundary.
- Direct Dolt `2.3.2` supports transactional aggregate, Command, Event, and
  Audit writes; expected-version conflicts; immutable idempotency outcomes;
  and one same-version winner. It is valid only behind the human-approved
  trusted-engine boundary, with the ADR-0004/ADR-0012 identity and safe-commit
  configuration checks before every write.
- Git cleanup requires exact durable ownership plus a fresh unified
  pre-destructive gate. An attempted deletion followed by a present target is
  ambiguous, even when the target has the same SHA. The ADR-0013 Linux
  recovery limits are hard expansion ceilings.
- GitHub delivery can bind refs, PRs, checks, reviews, merge, and cleanup to
  exact Candidate/base SHAs. GitHub PR `base.sha` is not a live-base authority,
  and `merge_commit_sha` does not prove integration. Merge requires an atomic
  expected-head condition.
- Git and Dolt synchronization are two effects. Either can succeed alone, so
  combined success is a projection rather than an atomic transaction.
- Accepted ADR-0014 closes `dir-m0.14` by selecting a human-approved trusted-
  provider Linux boundary with mandatory rootless OCI defense in depth,
  admission of repository lifecycle surfaces before workspace creation, and
  finite operational limits checked before launch and periodically.

The system needs one contract that preserves all of those facts without
claiming exactly-once external execution. It must also stop a stale engine
from updating durable state and must not assume that a TaskStore lease can
fence an external system which does not accept its fencing token.

Superseded ADR-0008 remains the maximal threat analysis, while Accepted
ADR-0014 replaces its Decision with the human-approved Linux `1.0` runtime
boundary. The fully hostile engine/provider cases are outside that guarantee;
the trusted-provider boundary and all ADR-0014 admission, rootless OCI,
credential, lifecycle, and operational-limit controls remain normative. This
decision neither expands nor re-proves that boundary and does not independently
authorize M1 product implementation.

## Question or hypothesis

Within ADR-0004's trusted-engine boundary and the Linux-only Director `1.0`
scope, can one durable command/effect state machine over direct Dolt guarantee
one logical outcome, prevent stale engine owners from dispatching, and recover
fail-closed across crashes, lost responses, duplicate or out-of-order wake-ups,
and external drift for every Director lifecycle effect, without claiming a
distributed transaction or exactly-once external execution?

## Acceptance criteria

- Define canonical command identity, payload conflict behavior, and expected
  aggregate version semantics.
- Define Project and per-attempt leases, monotonic fencing, transaction
  boundaries, and the limit of lease authority over external systems.
- Define durable effect intent, external observation, evidence freshness,
  unknown outcomes, bounded retry, compensation, recovery, and terminal drift.
- Map every planned effect to an explicit behavior class and effect-specific
  authoritative facts.
- Preserve exact Candidate, base, repository, ownership, policy, and cleanup
  bindings at every relevant boundary.
- Apply the trusted TaskStore health gates and the measured Linux recovery
  limits without fallback.
- Apply ADR-0014 lifecycle-surface admission before Paseo workspace creation
  and finite operational-limit/Needs-you gates before and during every
  applicable Effect.
- Prove the contract with a deterministic executable model that injects every
  critical crash boundary, simulates competing engines, and performs no live
  product or external-service mutation.

## Command contract

### Identity and immutable request

Every mutating UI, RPC, MCP, scheduler, reconciler, and retention request is a
`CommandEnvelope` with these logical fields:

```text
schemaVersion
ingressKind + stableIngressAudience
actorKind + actorInstanceId
requestId
fixedScope { projectId, workspaceId?, taskId?, runId?, role }
commandType
targetAggregateIds
expectedVersions { aggregateId: version }
payload
activeOrganizerRevision/frozenRunRevision when applicable
```

The trusted ingress validates the schema and derives scope from the
authenticated session or frozen Run. An MCP tool never accepts a Project,
Workspace, Task, Run, role, path, remote, agent, database, credential, or other
scope selector when it can be fixed at launch. Identity is not authorization;
the engine reauthorizes the actor, role, capability, policy envelope, Run
state, and target on every admission and before every effect.

The ingress audience is a durable logical UI session, bridge audience, or
system component identity, not an engine PID, lease holder, or restart-specific
process ID. Actor attribution is validated and persisted separately on the
immutable Command and Audit; it is deliberately not part of `commandKey`.
The engine binds each stable ingress audience to exactly one server-derived
actor identity; a different actor presenting the same audience is rejected
before command lookup.
The canonical `commandKey` is the SHA-256 digest of the schema-versioned tuple
`(Project, ingress kind/audience, requestId)`. The
`payloadHash` is a separate SHA-256 digest of canonical JSON containing the
schema version, command type, fixed scope, targets, expected versions, active
revision bindings, and payload. Canonicalization rejects duplicate keys,
non-JSON values, invalid Unicode, oversize values, secrets, and unknown schema
versions before hashing.

Clients mint at least 128 bits of unpredictable request identity before their
first submission and retain it across transport retries. A session MCP bridge
uses the provider tool-call request identity plus its immutable bridge
audience. Engine-originated commands use a deterministic semantic request ID
for one logical slot, such as `(Run, phase, sequence)`, and persist it before
work. Their actor is a restart-stable logical identity such as
`system/scheduler`, never an engine instance, PID, process start, or lease
holder. A replacement engine submits the same audience/request tuple, receives
the first durable result, and retains the original actor attribution. A new
request ID means a new command and never means “retry.”

The TaskStore enforces one row per `commandKey`:

- same key and same `payloadHash`: return the first durable command outcome;
- same key and different `payloadHash`: reject
  `IDEMPOTENCY_KEY_REUSED_WITH_DIFFERENT_PAYLOAD` without mutation;
- new key and matching expected versions: accept once;
- new key and a stale/missing expected version: persist a replayable
  `rejected_version_conflict` outcome without changing the aggregate.

A replay reads the existing result before checking the aggregate's current
version. Otherwise a successful first attempt that advanced the aggregate
would be incorrectly converted to a conflict on retry.

### Expected aggregate versions

Every command that mutates durable domain state supplies the version of every
aggregate it can mutate, sorted by stable aggregate ID. The initial admission
transaction compares all versions and has one winner. It increments each
accepted aggregate version and persists the Command, initial Event/Audit, and
all immediately known Effect intents atomically. There is no unversioned
last-write-wins path and no aggregate upsert; ADR-0004's exact
`UPDATE ... WHERE id = ? AND version = ?` contract applies.

The caller's expected versions govern first admission only. Later state-machine
transitions use compare-and-swap over the current Command, Effect, and
aggregate versions. Before dispatch, the engine also revalidates the immutable
Candidate/base/repository/configuration binding captured by the Effect. A new
Candidate, relevant base, active Organizer revision, repository identity, or
incompatible Run state invalidates the old Effect; the engine never edits an
old Effect to point at a new revision.

### Command and effect identity

A Command may contain an ordered list of Effects. An `effectId` is the SHA-256
digest of `(commandKey, ordinal, kind, exact target identity, immutable
binding, Observation-age policy, operational limits, lifecycle admission when
applicable)`. The Effect record contains:

```text
effectId, commandKey, ordinal, kind, class
fixed scope and exact target identity/hash
Candidate/base/repository/configuration/ownership binding/hash
expected and desired external facts
phase, attempt count, attempt budget
dispatch epoch/attempt/permit
observation references + adapter maximum age
operational-limit policy + current observations when applicable
worktree lifecycle digest/admission when applicable
compensationFor?
terminal outcome/anomaly
```

The identity and binding are immutable. A changed target or binding creates a
new Command/Effect and leaves history intact.

## Lease and fencing contract

### Project execution lease

Direct Dolt stores one execution lease for each Project:

```text
projectId, holderInstance, holderProcessIdentity,
monotonicEpoch, acquiredAt, expiresAt, renewedAt
```

Acquisition and renewal use TaskStore/server time and an expected-version
transaction. Renewal by the same holder preserves the epoch. Takeover after
expiry increments it. Every effect-state write includes the exact holder and
epoch; a stale holder cannot authorize, mark unknown, attach evidence, project
completion, compensate, or release capacity.

Lease expiry alone is not external fencing. Git, GitHub, Paseo, filesystems,
and child processes do not understand a Dolt epoch. On the one-daemon Linux
`1.0` topology, a replacement engine therefore starts in **observe-only** mode
after takeover. It must durably prove the previous engine process identity and
every owned dispatch child/process tree absent, subscribe for wake-ups, and
complete a full reconciliation before dispatch is enabled. If process absence
or the one-daemon identity is ambiguous, the Project becomes Degraded/Needs
you and no new effect is dispatched.

Acquisition never accepts a caller boolean asserting absence. The new epoch is
observe-only. A separate immutable Linux process-identity Observation binds the
prior holder, current holder/epoch, exact process ancestry/absence facts,
TaskStore time, and a finite freshness window. Enabling dispatch consumes that
Observation once. Every later takeover needs new evidence.

An old process that is still alive is not made safe by an expired timestamp.
It must acknowledge shutdown or be proven terminated. Multi-host lease
takeover and a remote fencing service are outside `1.0`.

### Per-effect attempt lease and one-use permit

Immediately before an external mutation, one TaskStore transaction verifies:

- healthy exact TaskStore listener/database and safe global/session commit
  mode;
- current Project holder, unexpired lease, epoch, and takeover dispatch gate;
- Effect phase, compare-and-swap version, and remaining attempt/Run budgets;
- fixed scope, role capability, active/frozen configuration, and policy;
- current aggregate state and immutable Candidate/base/repository binding;
- one fresh effect-specific observation of the exact target, current holder,
  epoch, Effect revision, and attempt which has not authorized another
  transition;
- an adapter-declared positive finite maximum Observation age checked against
  TaskStore time;
- before `paseo.workspace_create`, a canonical digest of
  `worktree.setup`, `worktree.teardown`,
  `worktree.terminals[*].command`, and
  `worktree.servicePorts.portScript`; non-empty surfaces require an
  authenticated engine Command with a server-derived human actor bound to the
  exact scope and lifecycle digest;
- finite configured process, memory, elapsed-time, output, temporary-storage,
  worktree/disk, and free-space limits plus current bounded observations for
  every applicable Effect; and
- any ownership, recovery, integration, consumer, disk, or destructive gate.

It then persists `(effectId, epoch, attempt, expiresAt, observationId,
preconditionHash)` and emits a one-use dispatch permit. The permit is bound to
one exact adapter operation, executable, canonical cwd, argv, target, expected
external value, and epoch. It is consumed durably before the adapter handoff.
A stale, expired, consumed, wrong-target, wrong-binding, wrong-cwd, or
wrong-argv permit is rejected.

The authorization transaction consumes the precondition Observation at the
same time it creates the attempt. Outcome reconciliation consumes a distinct
outcome Observation. A retryable outcome therefore cannot also authorize the
next attempt: every retry performs and persists a new precondition Observation
under the current holder/epoch after the previous transition.

For ADR-0007 destructive operations, this permit additionally embeds the fresh
unified pre-destructive gate result. A refusal before handoff is persisted as
`refused_before_dispatch`; repair may return the same Effect to
`intent_recorded` only after all exact facts are freshly re-proven. A permit
cannot be cached across reconciliation passes.

If the engine loses its lease after handoff, the external operation may still
have happened. The stale engine cannot persist a result. The new holder treats
the Effect as unknown and observes it; it never assumes the newer epoch
cancelled the older external request.

## Transaction boundaries

No transaction is held open across an SDK, process, filesystem, Git, GitHub,
or other network call.

| Boundary | Durable atomic work | External work |
|---|---|---|
| T0 admission | Validate TaskStore health; insert immutable Command/outcome; compare and update aggregates; append initial Event/Audit; insert Effect intents. | None. |
| T1 observation | Append one validated, bounded, unconsumed Observation with holder/epoch, purpose, attempt, Effect revision, TaskStore time/maximum age, source/freshness, and operational metadata. | The read occurs before the transaction through the effect adapter. |
| T2 dispatch claim | Revalidate holder/epoch, state, binding, policy, lifecycle admission, finite limits, budget, and T1 evidence; persist attempt and atomically consume the Observation and one-use permit. | None. |
| T3 handoff | Attempt phase is already durable. A proven failure before OS/API handoff becomes `failed_pre_dispatch`; any failure after possible handoff is `unknown`. | Exactly one adapter invocation for that permit. |
| T4 evidence projection | Append receipt hint if safe, authoritative Observation reference, Effect outcome, aggregate projection, Event, and Audit atomically with current epoch/version. | The authoritative read occurs before the transaction. |
| T5 compensation | Admit another fully scoped Command/Effect with `compensationFor`; preserve the original result. | Uses the ordinary T1-T4 protocol. |

TaskStore-only Effects complete in T0 or another single expected-version
transaction. If the SQL response is lost, recovery queries the immutable
`commandKey`; it does not resubmit a different write. Git and Dolt Sync,
Organizer commit plus activation, backup plus verification, and every other
cross-system sequence remain separate Effects with an explicit combined
projection.

Before every TaskStore write, ADR-0004/ADR-0012 applies: set/read global safe
commit mode on the control connection, set/read session safe mode plus global
mode on the exact write connection, verify listener/database identity, and
start no transaction on any error, drift, reset, duplicate initialization, or
unexpected value. There is no fallback identity, connection, database, store,
or delivery mode.

## Effect state machine

```text
intent_recorded
  -> refused_before_dispatch -> intent_recorded after repair + fresh proof
  -> dispatch_authorized -> dispatching
       -> failed_pre_dispatch -> retry only after fresh proof
       -> observation_required / unknown

observation_required / unknown
  -> complete
  -> retryable
  -> waiting_external
  -> needs_you

complete --terminal; later drift--> anomaly + Needs you (never re-execute)
```

`dispatch_authorized` or `dispatching` found after restart is unknown unless
the adapter can prove that no handoff occurred. Event delivery, an SDK response,
exit status, textual agent claim, cached status, and elapsed lease time are
never by themselves proof of an external result.

## External observation and evidence

Every Observation is immutable and contains:

```text
observationId, effectId, projectLeaseHolder + projectLeaseEpoch
purpose { precondition | outcome | drift } + attempt + Effect revision
adapter kind/version + authoritative source
exact target identity/hash + immutable binding/hash
normalized status + bounded fact hash/identifiers
observedAt using TaskStore time + positive finite maxAgeMs
freshness barrier + event cursor where supported
finite operational-limit facts where applicable
consumedBy transition/attempt/epoch or null
bounded redacted health code
```

Normalized status is one of `desired`, `absent`, `current_expected`,
`owned_present`, `ready`, `different`, `ambiguous`, or `unavailable`.
Adapters define which statuses they can prove. Transport, authentication,
permission, timeout, parse, identity, and rate-limit failures are
`unavailable` or `ambiguous`, never `absent`.

An Observation authorizes at most one transition. Authorization and
reconciliation require its exact Effect, Effect revision, purpose, current or
next attempt as applicable, target, binding, aggregate state, policy, lease
holder, lease epoch, and freshness barrier to match. `observedAt` must not be
in the future and its TaskStore-clock age must be no greater than the positive
finite `maxAgeMs` declared by that adapter/Effect. The consuming transaction
sets `consumedBy` atomically with the state transition.

Candidate/base/configuration/repository changes, an ownership change, another
Effect transition, a consumed Observation/gate, a holder/epoch change, or age
beyond the declared bound invalidates the Observation. An outcome Observation
consumed to classify `unknown -> retryable` cannot authorize that retry; the
engine must re-observe. Subscriptions, filesystem watchers, and provider events
are wake-ups which trigger a fresh authoritative read; they are not evidence.

Only bounded structured facts enter TaskStore, Audit, logs, diagnostics, or
support output. Raw Git/GitHub/Paseo/provider/process output, credentials,
paths, file names, recovery manifests, and source content do not. Redaction or
schema failure aborts evidence persistence.

### Authoritative facts by adapter

| Adapter | Minimum evidence used by this contract |
|---|---|
| TaskStore | Exact listener/database identity; safe commit variables; immutable Command row/payload hash; aggregate/effect versions; committed Event/Audit records. |
| Paseo | Before workspace creation, the canonical four-surface lifecycle digest and empty/exact human-approved admission from ADR-0014. For created resources, fresh public list/ref/refresh; exact IDs, workspace, role, parent contract, labels, provider tuple, persistence reference, and prompt marker. Termination additionally needs `closed`, non-null `archivedAt`, and process absence. `closed`/null-archive and `running`/no-turn remain ambiguous and consume capacity. |
| MCP | Fixed bridge audience/scope, unexpired/revocable capability, schema, role permission, request identity, and the engine's durable Command result. MCP output never proves an external Effect. |
| Git/worktree/filesystem | Re-resolved source/common-dir/origin/filesystem identity, registrations, exact OIDs/trees/refs, owner nonce, recovery-ref/artifact digest, live base ancestry, consumers, and one-use gate. Remote absence is only exact `ls-remote --exit-code` status `2`; other failure is not absence. |
| GitHub | Canonical repository database ID, owner/name, exact refs and marker, PR identity/head/base repositories, head SHA, check `head_sha`, review `commit_id`, live base ref, feedback/mergeability, `merged` plus `merged_at`, merge commit and Git graph. PR `base.sha` and non-null `merge_commit_sha` alone are insufficient. |
| Process/cleanup | Exact engine/child process identity and ancestry, process absence, canonical owned paths/filesystem identity, restrictive owner metadata, complete recovery hashes, postcondition absence, and finite configured/current process, memory, elapsed-time, output, temporary-storage, worktree/disk, and free-space facts. A killed worker never authorizes deletion. |

## Effect classes and retry decision

Every adapter mutation declares one of six closed classes. Adding a new class
or an Effect without authoritative observation is an ADR/schema change, not an
implementation convenience.

| Class | Examples | Unknown-result rule |
|---|---|---|
| `store_only` | Command admission, aggregate/Event/Audit write, lease, helper capacity reservation, schema migration transaction | Query the immutable key and versions. Repeat only the same transaction through the same command identity. |
| `unique_create` | Workspace; Task/Reviewer Agent with zero-work bootstrap; parent-bound helper evidence; PR; verified backup/fresh restore; private recovery artifact; diagnostic bundle | Adopt exactly one matching desired object. Retry only after effect-specific authoritative absence, prior dispatcher/process absence, unchanged binding, and budget. Zero or multiple ambiguous matches parks. |
| `conditional_update` | Exact ref create/update/push; direct integration; expected-head PR merge; Organizer commit; Git/Dolt sync; recovery ref; quarantine move | Adopt desired exact state. Retry only while the authoritative current value still equals the persisted expected value and the adapter mutation carries that compare condition. A changed value parks/invalidates. |
| `idempotent_close` | Agent/workspace archive; close an exact owned unmerged PR | Adopt the full terminal predicate. An exact owned active target may be retried within budget; an identity mismatch or incomplete termination parks. |
| `nonrepeatable_progress` | Continue/send an already-created agent turn or correction prompt | If downstream facts prove acceptance/completion, adopt. Otherwise any possible handoff parks in Needs you. Never resend automatically. |
| `destructive_terminal` | Remove worktree; delete/expire local or remote ref; delete recovery artifact; expire diagnostic bundle | Exact absence after a possible handoff completes. Any present, recreated, changed, unowned, or unprovable target parks without another delete. Completion is terminal; later reappearance is an anomaly, not permission to delete. |

Initial Task/Reviewer Agent creation with the zero-work bootstrap is
`unique_create`. ADR-0020's separate real Task/Review send is
`nonrepeatable_progress` and requires terminal notification. Helper creation is the single launcher exception:
the engine transactionally reserves scope/capacity and issues a one-use
parent-bound admission; the Task Agent invokes the admitted mechanism; the
engine observes the exact helper identity before helper work. An unknown result
parks and is never blindly recreated.

An ordinary retry never changes provider, model, mode, permissions, delivery,
repository, identity, or cleanup policy. Such a change is a new explicitly
authorized command, not retry or compensation.

### Bounded retry

No generic transport retry surrounds a mutation. The adapter distinguishes:

1. **Proven before handoff:** no syscall, child, request, rename, or remote
   operation began. Persist `failed_pre_dispatch`; retry after fresh proof.
2. **Possible handoff:** persist or infer `unknown`; observe before deciding.
3. **Authoritative desired state:** complete without another mutation.
4. **Authoritative class-specific retry predicate:** create a new attempt under
   a fresh Project lease, observation, permit, and exact expected condition.
5. **Unavailable observation:** back off observation only. Do not mutate.
6. **Ambiguous/different fact or nonrepeatable/destructive present target:**
   `Needs you` with no mutation.

Effect attempt limits, Run correction/replacement/CI limits, time/cost budgets,
provider limits, and cleanup resource limits all apply; the first exhausted
limit wins. Exhaustion is durable and cannot be reset by restart. Repeated
unchanged failure fingerprints or setup/test thrash also parks rather than
consuming another nominal attempt.

## Compensation

Director uses forward compensation, never distributed rollback. A
compensation is a new Command/Effect with its own identity, expected versions,
policy authorization, intent, evidence, retry budget, and `compensationFor`
reference. The original outcome, Events, Observations, and Audit remain
immutable.

Examples include closing an exact owned unmerged PR, archiving an exact owned
agent after a failed Run, restoring a verified backup into a fresh store,
restoring a private artifact, or removing an exact branch only after verified
integration. Compensation cannot erase external history, reinterpret a failed
effect as success, overwrite a live store/repository, delete unknown state, or
make dirty work unrecoverable. If compensation is itself unknown, it follows
the same class-specific reconciliation and may also enter Needs you.

Project cancellation and Emergency stop are sagas of such Effects. They do
not roll back commits, reviews, or conversations. Cleanup order remains:

1. stop or reconcile Task/Reviewer Agents and every observed helper;
2. preserve and verify worktree/index/ignored data and persist
   `removal_ready`;
3. archive and reconcile the Paseo workspace;
4. run fresh unified gates for exact residual worktree/ref/artifact cleanup;
5. retain recovery material until its separately guarded expiry.

## Recovery and reconciliation

Startup, reload, reconnect, lease takeover, event wake-up, and manual
`Reconcile now` all run the same procedure:

1. Verify exact trusted engine revision, TaskStore identity, safe commit mode,
   supported Linux/runtime tuple, and one-daemon topology.
2. Acquire or renew the Project lease. After takeover remain observe-only
   until previous engine/dispatch processes are proven absent.
3. Subscribe to supported wake-ups, then load all nonterminal Commands/Effects,
   active Runs, capacities, and terminal Effects requiring drift checks.
4. Perform a complete effect-specific external scan using immutable scope and
   bindings. Process wake-ups which arrived during the scan by observing again.
5. Append Observations and deterministically project `complete`, `retryable`,
   `waiting_external`, `needs_you`, or terminal anomaly.
6. Only after the full pass may the current epoch dispatch a retryable/new
   Effect, each with a fresh attempt transaction and one-use permit.

Recovery never trusts plugin memory, cached status, event counts, a previous
gate, an agent claim, lease expiry, or cleanup callbacks. `closed` with null
`archivedAt`, `running` without an active turn, an unexplained missing
worktree, multiple correlation matches, lost nonrepeatable progress, and a
present destructive target after possible handoff are explicit ambiguity.

Pause permits observation and durable projection but blocks new dispatch,
retry, Reviewers, PR publication, and integration. Resume performs the full
procedure first. An external outage remains visible and retryable with
backoff; it never selects a different provider, delivery mode, repository, or
store.

## Linux cleanup and recovery limits

ADR-0013's supported Linux envelope is part of every recovery-artifact intent
and fresh destructive gate:

| Resource | Hard expansion ceiling |
|---|---:|
| Recovery entries | 10,000 |
| Inspected worktree entries | 25,000 |
| Aggregate ignored content | 512 MiB |
| One regular file | 256 MiB |
| Sequential buffer | 64 KiB |
| One copy/hash/verify/restore phase | 180 seconds |
| Full artifact lifecycle | 480 seconds |
| External worker supervisor | 540 seconds |
| Measured worker RSS growth | 192 MiB |
| Recovery retention | Seven days |
| Projected free-space floor | 10% |
| Full pre-destructive artifact revalidations | Five |

Policy may tighten limits, raise the disk floor, or shorten retention. It
cannot expand/skip them without new Linux evidence and a superseding ADR. An
over-limit, timed-out, killed, changed, unsupported, unowned, unreadable, or
unprovable tree stays in place and enters Needs you with path/content-free
evidence. A killed worker is not cleanup evidence.

## Fail-closed behavior

The engine performs no new mutation when any of these is unproven:

- trusted engine/adapter revision, supported runtime, or one-daemon identity;
- active Project lease epoch or prior executor/process absence after takeover;
- TaskStore listener/database identity, transaction health, or safe commit
  mode;
- authenticated actor, fixed scope, role capability, expected versions,
  frozen configuration, or explicit human authority;
- ADR-0014's rootless OCI/admitted provider boundary, or the canonical
  worktree lifecycle surfaces and exact empty/server-derived-human approval
  predicate before Paseo workspace creation;
- exact repository/path/common-dir/remote/ref/Workspace/agent identity and
  ownership at mutation time;
- current Candidate/base/check/review/feedback/mergeability binding or atomic
  expected-head integration;
- effect-specific fresh observation, authoritative absence, retry predicate,
  recovery artifact, integration, no-consumer, disk, or destructive gate;
- positive finite operational limits or current/periodic bounded process,
  memory, elapsed-time, output, temporary-storage, worktree/disk, and free-
  space facts for an applicable Effect;
- redaction, schema, size, secret-safety, or output bounds; or
- remaining attempt, correction, replacement, CI, time, cost, process, disk,
  and recovery budgets.

TaskStore identity/health failure degrades and pauses the Project because
durable intent cannot be trusted. External outage usually becomes
`waiting_external`; a credential/configuration decision becomes Needs you.
Identity conflict, unknown nonrepeatable/destructive outcome, multiple matches,
terminal drift, unsafe cleanup, or exhausted budget becomes Needs you.
Observed secret exposure, wrong integration, unowned deletion, or dirty-work
loss is a P0 incident, not a compensatable warning.

Accepted ADR-0014 supersedes ADR-0008's No-go Decision by selecting the
practical trusted-provider Linux boundary and excluding fully hostile engine/
provider cases from the `1.0` guarantee. This contract enforces that decision;
it does not reopen or expand it. It still fails closed if the admitted trusted-provider/rootless-OCI
boundary, repository lifecycle admission, credentials, raw control endpoints,
Workspace scope, delivery authority, or finite operational observations do not
match ADR-0014. Durable idempotency cannot make an out-of-contract mutation
safe.

## Evidence

The deterministic model, complete output, commands, source references, and
scope/cleanup proof are in
[`docs/evidence/m0.10`](../evidence/m0.10/README.md). The executable is
[`tools/spikes/dir-m0.10/effect-contract.mjs`](../../tools/spikes/dir-m0.10/effect-contract.mjs),
with deletion mutations in
[`guard-mutations.mjs`](../../tools/spikes/dir-m0.10/guard-mutations.mjs).

The bounded run on Debian 13.6, Linux `6.12.107+deb13-amd64` x86-64, Node.js
`v26.7.0`, Git `2.47.3`, Dolt `2.3.2`, GitHub CLI `2.97.0`, and Paseo `0.7.2`
reported:

- 13 passing test groups and zero failed assertions;
- atomic rollback before command commit and durable same-key replay;
- stale expected-version and different-payload rejection, plus restart-stable
  ingress replay with separate original actor attribution;
- two-engine epochs `1`, `2`, and `3`, observe-only takeover while the prior
  executor was live, three durable liveness Observations, two consumed absence
  proofs, stale dispatch/finalization rejection, and one external handoff;
- six negative Observation cases for stale epoch, different holder, expired
  age, reuse, wrong attempt/Effect revision, and post-unknown retry without
  re-observation;
- ADR-0014 empty/exact-human lifecycle admission, missing/stale denial before
  workspace creation, finite operational-limit and missing-fact denial before
  dispatch, and periodic over-limit Needs-you parking across all 28 externally
  mutating catalog kinds;
- six critical crash cut-points from before intent through repeated terminal
  projection, with no partial durable record, duplicate handoff, stale permit,
  or duplicate completion Event;
- create-result adoption, nonrepeatable ambiguity parking, destructive
  absence adoption, destructive-present parking, idempotent-close retry, and
  exhausted-budget parking with measured handoff counts;
- Candidate/base evidence invalidation, terminal drift without re-execution,
  and external outage distinct from absence;
- five captured TaskStore health failures for global/session unsafe mode,
  connection reset, duplicate initialization, and listener identity with zero
  unsafe writes; one atomic `store_only` Effect; and zero handoff after a
  proven pre-dispatch failure;
- fixed MCP scope, revoked capability rejection, durable replay, payload
  conflict, and a measured zero Effect-count delta;
- forward compensation preserving both original and compensating evidence and
  refusing a foreign target without handoff;
- separate Git/Dolt Sync outcomes with a durable partial result and no repeat
  of the successful half;
- exact ADR-0013 limits, expansion refusal, one-use destructive gate, lost
  deletion reconciliation, and absence of the harness-owned temporary root;
  and
- a closed catalog of six classes and 32 planned Effect kinds.

The focused mutation run deletes the Observation epoch, holder, single-use,
maximum-age, and attempt/Effect-revision guards plus exact write-session mode,
workspace lifecycle-admission, and operational-limit guards one at a time.
All eight parse and then fail their corresponding negative fixture; zero
mutants survive.

Both outputs are byte-identical across two complete runs. The fixtures use only
Node built-ins, deterministic in-memory adapters, and bounded child Node
processes for mutated copies. They remove their owner-scoped temporary roots,
perform no live Paseo, provider, Dolt, Git, GitHub, repository, credential,
paid-model, or product effect, and contain no Director implementation.

## Alternatives considered

### Depend on external exactly-once behavior

Rejected. The approved systems do not share an idempotency protocol or
transaction. PR creation, prompts, local cleanup, and other effects have
different observation and retry properties.

### Treat a durable Project lease as external fencing

Rejected. External systems do not validate its epoch, and an old process may
have handed off a request before or after lease expiry. The selected same-host
takeover remains observe-only until old process absence and reconciliation are
proven.

### Retry every error with the same arguments

Rejected. A transport error after possible handoff is unknown. Repeating an
agent prompt or destructive delete can duplicate work or remove a recreated
resource. Retry is effect-class-specific and follows authoritative observation.

### Use one generic “done” flag or adapter response

Rejected. Exact-SHA delivery, termination, backup verification, worktree
ownership, and absence all need different compound facts. Immutable typed
Observations preserve provenance and freshness.

### Roll back external work on command failure

Rejected. Git commits, PR history, reviews, conversations, and remote actions
cannot be transactionally rolled back. Forward compensation is explicit,
auditable, ownership-checked, and itself recoverable.

### Hold a TaskStore transaction open during the external call

Rejected. It does not make the external operation atomic, increases lock and
failure exposure, and cannot resolve a lost response. Intent before handoff and
evidence after observation are separate transactions.

### Let MCP, an agent, or adapter choose retry and workflow policy

Rejected. The bridge and adapters validate/translate within fixed scopes.
Only the trusted engine owns authorization, state transitions, retry budgets,
fallback policy, compensation, and lifecycle Effects.

## Decision

**Go.** Adopt the command, intent, fenced-attempt, external Observation,
effect-class, reconciliation, forward-compensation, recovery, and fail-closed
contract in this ADR for every Director `1.0` Effect.

The Go is bounded to the tested Linux topology and the independently approved
source contracts. It proves the durable orchestration design with a
deterministic model; it does not re-prove or expand any adapter's live
compatibility tuple. An adapter may implement only an Effect kind for which it
can produce the required authoritative facts and enforce the declared compare,
ownership, or terminal predicate. Unsupported facts keep that capability
disabled.

Accepted ADR-0014 and closed `dir-m0.14` resolve their trusted-provider Linux
scope question. This decision still does not authorize M1 while the M0 exit-
gate audit or another M0 stop condition remains unresolved. It does not approve
multi-host execution, another TaskStore, another forge, a new provider tuple,
non-Linux support, or silent fallback.

## Consequences

- Shared contracts in M1 must represent Command, Effect, Attempt,
  Observation, Compensation, ProjectLease, version conflict, unknown outcome,
  and Needs-you reason as separate typed values.
- Observation schemas must include holder, epoch, purpose, attempt, Effect
  revision, TaskStore-clock timestamp, adapter maximum age, operational facts,
  and single-use consumption. Every retry needs a newly persisted precondition
  Observation.
- The TaskStore schema must enforce immutable command/payload identity,
  deterministic effect identity, expected-version transactions, append-only
  evidence/audit, monotonic Project epochs, and stale-epoch rejection.
- Adapters remain thin and effect-specific. Each documents its authoritative
  observation, exact identity/freshness rules, compare primitive, handoff
  point, retry class, bounded error codes, and cleanup predicate.
- `dir-m1.8` owns the ADR-0014 rootless-OCI execution boundary, four-surface
  lifecycle admission before Paseo workspace creation, and finite operational-
  limit gates. Applicable active Effects repeat operational observations
  periodically and park in Needs you on missing/exceeded facts.
- Reconciliation is a first-class engine loop. Events only wake it. Terminal
  drift creates an anomaly and never silently replays a completed Effect.
- Cross-system workflows are sagas with explicit projections and forward
  compensation. Git/Dolt Sync and Organizer commit/activation never claim
  atomicity.
- The Linux engine supervisor must expose exact process identity/absence for a
  safe lease takeover. If later topology cannot prove it, that topology needs
  a real external fencing mechanism or remains unsupported.
- Product tests must reuse the six-class fault matrix with real disposable
  adapters, inject every T0-T5 boundary, and retain the stricter source ADR
  tests for lifecycle, TaskStore, cleanup, and exact-SHA delivery.
- The effect catalog is versioned. A new kind must select a class and evidence
  contract before implementation; a new class requires an ADR.
- M0 evidence remains version-bounded. Changes to Paseo, provider, Dolt, Git,
  GitHub, Linux containment, recovery policy, or their observed behavior
  invalidate only the affected adapter admission, not permission to guess a
  fallback.

## Independent verification

Exact Candidate `2b838a1c3599f847eae3f35d8be8f37e0fbe8c2f` received an
independent `approve_candidate` verdict and was integrated with its reviewed
base as merge `6c4d4cbde94401e1850adfa8470c53a37070367b`. `dir-m0.10`
records the byte-identical deterministic checks, mutation suite, review,
integration, cleanup, and residual topology bounds. This status
reconciliation uses that integrated evidence and does not rerun the effect
experiments.
