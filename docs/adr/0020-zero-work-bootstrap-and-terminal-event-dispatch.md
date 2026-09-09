# ADR-0020: Split zero-work bootstrap from notified work and dispatch terminal events synchronously

- **Status:** Accepted
- **Date:** 2026-09-09
- **Beads Task:** `dir-m2.17`
- **Plan gate:** M2 deterministic planning and scheduler behavior
- **Decision owner:** Project owner through binding Task comment `01a087be-bf19-70b2-a5c3-2f40ef6a8fe7`
- **Amends:** PLAN §§6.4, 10.3, 12.1, 12.4, 13.1, 13.3, 20.1, 21, 22, and 23; [ADR-0003](0003-paseo-0.7.2-lifecycle-recovery.md), [ADR-0010](0010-top-level-task-agent-parentage.md), [ADR-0015](0015-idempotent-command-effect-contract.md), [ADR-0017](0017-standalone-engine-connector-authority-boundary.md), and [ADR-0019](0019-event-driven-delivery-fast-path.md)

## Context

Prompted creation makes native identity persistence and real work start one
indivisible host action. It therefore cannot prove that the parentless agent,
workspace, and public labels were durable before Task or Review work began.
Periodic observation also adds avoidable transition latency after the daemon
has already persisted a terminal result.

The owner requires an instantaneous launcher contract: the create turn does no
work, the native worker facts are persisted before the real prompt, and daemon
terminal callbacks synchronously wake coordinator reconciliation in under one
second. The callback remains a wake signal rather than completion evidence.

## Decision

Every Director-launched Task Agent and Reviewer follows this exact order:

1. persist the root-workspace registration and complete preparation barrier;
2. persist a uniquely keyed parentless create intent carrying the exact Task or
   Review title, workspace, labels, and the fixed zero-work bootstrap only;
3. observe bootstrap completion, then persist the returned native agent ID,
   workspace ID, and frozen labels before any dependent effect;
4. persist a separate prompt intent; and
5. start real Task or Review work only through `send_agent_prompt` with
   `notifyOnFinish=true` and the persisted agent/workspace binding.

The fixed bootstrap authorizes no file inspection, tool call, Task work, or
Review work and tells the agent to finish immediately. Real work text is
invalid on create. The bootstrap text is invalid as the real work prompt.

The daemon's `finished`, `error`, and `permission` terminal callbacks enter an
engine-owned handler. For each callback the engine:

1. validates Run, agent, notified-effect, cursor, binding, event hash, and kind;
2. durably records an enqueue intent keyed by the immutable event ID;
3. synchronously calls the coordinator reconciliation queue;
4. requires an exact queue receipt timestamped less than 1,000 ms after the
   daemon terminal timestamp; and
5. durably records the receipt and measured latency.

The queue deduplicates by event ID. An exact replay is a no-op, including after
response loss; reuse of an ID with changed fields fails closed. Later callbacks
cannot make an older callback admissible. The callback clears only the latest
replaceable observation and causes a fresh authoritative host read. It never
proves that a turn succeeded, admits a Candidate, frees capacity, or authorizes
cleanup.

No active-agent timer or status polling drives this state machine. While a
bootstrap or real turn is active, the reducer waits for a terminal callback.
The sole lost-event recovery is the existing PLAN watchdog after five minutes
with all sources agreeing: unchanged agent state, no tool activity, no
worktree change, no usage movement, and the exact pending notified effect. It
only schedules a fresh authoritative observation.

## Consequences

- Native identity and root-workspace visibility are durable before real work.
- The create effect and prompt effect have independent idempotency and recovery
  boundaries; a real prompt is nonrepeatable after a possible handoff.
- Normal terminal transitions enqueue reconciliation in less than one second,
  independent of the periodic external-reconciliation cadence.
- Event loss can delay a transition until the compound watchdog, but cannot
  manufacture completion. Duplicate or reordered callbacks cannot duplicate a
  coordinator wake.

## Verification

Maintained tests cover bootstrap/prompt ordering, exact parentless labels,
identity persistence before prompt dispatch, mandatory `notifyOnFinish`, no
active-turn polling, all three terminal kinds, 999 ms acceptance and 1,000 ms
rejection, exact duplicate callback replay, conflicting identity reuse, stale
event order, and the complete five-minute multi-source lost-event predicate.
