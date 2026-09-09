# ADR-0019: Use an event-driven single-CI delivery fast path

- **Status:** Accepted
- **Date:** 2026-09-09
- **Beads Task:** `dir-m2.17`
- **Plan gate:** M2 deterministic planning and scheduler behavior
- **Decision owner:** Project owner through the binding `dir-m2.17` Task decisions
- **Amends:** PLAN §§10.3, 12.4, 13.3, 13.4, 14.2, 20.1, 20.3, 21, 22, and 23
- **Aligned with:** [ADR-0006](0006-exact-sha-github-delivery.md),
  [ADR-0010](0010-top-level-task-agent-parentage.md),
  [ADR-0015](0015-idempotent-command-effect-contract.md),
  [ADR-0017](0017-standalone-engine-connector-authority-boundary.md), and
  [ADR-0018](0018-deterministic-coordination-boundary.md)
- **Amended by:** [ADR-0020](0020-zero-work-bootstrap-and-terminal-event-dispatch.md)

## Context

The approved workflow kept Review and Validation independent but defaulted to
review before public pull-request creation. Its first implementation also let
the review harness start a complete local CI. That shape serializes latency,
can run complete CI twice for one Candidate, and makes Task handoff depend on
per-Task scripts that translate Paseo lifecycle state and protect ownership
input.

M2 also needs visible launch accountability and deterministic capacity
behavior before broader execution and delivery work. A worker that starts
before its root-workspace registration is observable can become an invisible
consumer. Fixed concurrency alone cannot respond to a host approaching its
load, memory, or temporary-space boundary. Polling active agents with a
heartbeat adds noise and can accidentally reset liveness reasoning even though
the PLAN already requires a compound five-minute stall predicate.

The project owner bound `dir-m2.17` to a delivery fast path that resolves these
issues without moving policy into the connector or giving the Task Agent
publication, review, integration, cleanup, or closure authority.

## Decision

### Handoff and private ownership

The repository coordinator is the native handoff boundary. It reads and
normalizes documented Paseo lifecycle facts for live active, restored, and
historical/reclaimed workspaces without a per-Task script. `review-handoff`
can atomically maintain one stable JSON output file outside Git checkouts. The
file and coordinator state use mode `0600`.

The raw ownership token enters the CLI only through an absolute owner-only
regular `--ownership-file`; a raw `--ownership` argument is refused. Only the
token hash may enter the handoff manifest or pull-request marker. The raw token
and its file path never enter command output, public logs, PR content, or Beads.

### Candidate fast path

After exact Candidate admission, the Director Engine persists the unique draft
publication intent. Only a verified owned draft at the exact Candidate permits
the next transition. The engine then atomically records two sibling obligation
intents:

1. one authoritative complete remote Linux CI; and
2. one independent top-level Review of the exact Candidate.

They run concurrently when capacity is available. Recovery dispatches only a
missing intent and never repeats either recorded sibling. The review harness is
version 2 and does not start complete CI. It consumes the one exact remote CI
observation while independently checking detached checkout, manifest, Task,
diff, tree, live base, and current decision/finding identities. An observation
for another Candidate/base, more than one matching workflow run, an incomplete
run page, duplicate required check, pending/failed check, or different
observation ID fails closed.

An approved Review does not authorize merge. Publication readiness, gate, and
integration still require the exact Reviewer UUID, exact Candidate/base and
remote head, the recorded passing remote CI observation, current human
decisions and findings, clean mergeability, no unresolved feedback, all
configured and Task-required checks, and an atomic exact-head merge primitive.
The Task Agent may not execute any of those lifecycle effects.

### Launch visibility and capacity

Every Task Agent and Reviewer launch records a root-workspace Worker entry
before the external start. Missing root workspace, registration failure, or
unverified visibility refuses launch. The Director Workers surface exposes
role, Task, phase, duration, Candidate, and an Open agent action, and refreshes
from event invalidation rather than a worker heartbeat.

The delivery scheduler uses an adaptive `6/2/2` lane: at most six Task Agents,
two Reviewers on non-interfering bases, and two complete CI processes. One
integration remains exclusive per repository. Admission requires a current
machine observation and parks above a one-minute load of `48`, below `24 GiB`
available memory, or below `50 GiB` free `/tmp`. A dimension within its reserve
band contracts each adaptive ceiling by one, never below one. These are
additional delivery-lane limits; existing Project limits and all stricter
budget/security limits still apply, and the first refusal wins.

### Event completion and correction turns

Supported daemon `finished`, `error`, and `permission` lifecycle notifications
are immutable wake signals. Each valid callback synchronously enqueues
coordinator reconciliation in less than one second. A durable event-ID ledger
and idempotent queue produce exactly one logical wake; exact at-least-once replay is a
successful no-op, while reordered, conflicting, forged, unknown, or cross-Run events park. The
event never proves completion, frees capacity, resets a timeout, or substitutes
for a Claim or authoritative observation. Active turns are not polled.
Periodic external reconciliation remains a safety net, and the PLAN
five-minute multi-source `stalled_or_ambiguous` predicate remains the only
lost-event watchdog (ADR-0020).

An authorized correction turn reuses PLAN and skill material only while their
digests match the frozen Run. It refreshes current human decisions and the
current Candidate diff, sends all current Review, Validation, and human
findings as one deterministic batch, and stays with the same Task Agent. An
acknowledgement-only output is rejected once; repetition escalates. A changed
commit is a new Candidate and requires the same draft/CI/Review gates again.

## Consequences

- Draft PR creation moves before Review for this accepted fast path, but
  cannot expose merge authority or weaken any post-review gate.
- One remote CI run is the complete-CI authority for a Candidate. Reviewers
  may run focused read-only checks but cannot start another complete CI.
- Notification loss may delay work until reconciliation; it cannot corrupt
  state or prove a worker stalled. Duplicate events are harmless.
- Adaptive capacity can reduce throughput before a hard threshold. Typed
  refusal codes make the limiting fact visible and auditable.
- The raw ownership token remains available only in private coordinator
  control files. Loss of that control state parks mutation rather than
  reconstructing or publishing the token.
- M2 implements the deterministic delivery ordering and fake/adversarial
  contracts needed by its scheduler exit gate. M4 remains responsible for the
  complete user-facing quality/delivery product and live integration.

## Verification boundary

Maintained tests cover handoff identity, permissions, secret safety and
idempotency; draft create/update/correction; Candidate ordering and sibling
CI/Review dispatch; single remote-CI observation and gate binding; exact merge
refusals; Worker visibility; adaptive throttling; blocked Tasks; completion
event replay; and batched correction behavior. The final exact-SHA independent
Review and post-Candidate publication/integration gates remain coordinator
obligations and are not performed or claimed by the Task Agent.
