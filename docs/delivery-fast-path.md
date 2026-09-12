# Delivery fast path

ADR-0019 defines the deterministic M2 delivery contract. This page is the
operator-facing map of its order, evidence, and diagnostics.

The repository coordinator uses the explicitly configured publish-before-
review path below. The product engine also supports the safer
`publishBeforeReview: false` default described in
[GitHub pull-request publication](github-pr-publication.md); both paths retain
the same exact-SHA, no-merge-authority, and recovery gates.

## Order and authority

```text
exact Candidate admitted
  -> owned draft published under exact lease (no merge authority)
  -> remote Linux CI intent + independent Review intent recorded together
  -> CI and Review run concurrently
  -> Review harness consumes that exact CI observation
  -> publication readiness
  -> fresh gate
  -> atomic exact-head integration
```

Recovery observes each durable intent before acting. It dispatches only a
missing sibling and never starts a second complete CI for the Candidate. A
corrected commit is a new Candidate; it updates the same owned draft and gets a
new CI observation and independent Review.

The repository review harness keys that complete-CI budget to the exact
Candidate/base pair. Regenerated handoff manifests remain recorded history
inside the same budget and reuse its exact remote observation; changing
ownership or lifecycle routing fields cannot reset CI consumption. Legacy
manifest-keyed private state and its recorded exception history migrate without
becoming new authority.

The product-side observation, pagination, budget, and base-race contract is
documented in [GitHub checks and base invalidation](github-checks.md).

The Task Agent stops at Candidate and coordinator-native handoff. Only the
Director Engine or authorized coordinator may publish a draft, record remote
CI, mark the draft ready, gate, integrate, clean resources, or close the Task.

## Handoff

`review-handoff` accepts active, restored, and reclaimed/historical lifecycle
bindings. Use `--ownership-file` for the raw opaque token and
`--handoff-file` for a stable private output. Both files stay outside Git
checkouts; the ownership input, handoff, and coordinator state are owner-only
mode `0600`. Raw ownership exists only in the ownership input. Coordinator
state v2, cleanup-plan v2, the handoff manifest, command output, diagnostics,
locks, PR content/markers, and Beads may contain only its SHA-256. Exact schema
v1 state is migrated under the ownership-bound state lock, invalidating any
legacy derived cleanup-plan hash; derived schema-v1 cleanup plans must be
regenerated and admitted as v2 plans after restart or response loss. PR
metadata and child argv reject the token or ownership-file path.

The daemon's `finished`, `error`, and `permission` terminal callbacks
synchronously enqueue coordinator reconciliation in less than one second.
They are never Candidate, completion, liveness, capacity, or cleanup evidence.
A durable event-ID ledger and idempotent queue make exact replay a no-op and
refuse changed or reordered duplicates. Active turns are not polled. The
five-minute multi-source PLAN predicate remains the only lost-event watchdog.

## Capacity diagnostics

The default delivery lane is six Task Agents, two independent Reviewers on
non-interfering bases, two complete CI processes, and one integration per
repository. A fresh host sample is mandatory. Reserve-band pressure contracts
the adaptive ceilings one slot per pressured dimension, never below one.

| Diagnostic | Meaning and wake condition |
|---|---|
| `dispatch_fact_missing` | A machine sample or reviewer base is absent/stale; observe again. |
| `dispatch_load_ceiling_reached` | Load is above `48`; wait for a fresh lower sample. |
| `dispatch_memory_floor_reached` | Available memory is below `24 GiB`; wait or free memory. |
| `dispatch_temporary_floor_reached` | Free `/tmp` is below `50 GiB`; wait or free owned temporary data. |
| `task_agent_concurrency_reached` | The current adaptive Task-Agent ceiling is full. |
| `reviewer_concurrency_reached` | The current adaptive Reviewer ceiling is full. |
| `reviewer_base_interference` | Another Review targets the same base; wait for it to finish. |
| `complete_ci_concurrency_reached` | The current adaptive complete-CI ceiling is full. |
| `repository_integration_not_exclusive` | Another integration owns this repository lane. |
| `delivery_task_blocked` | The Beads Task is blocked; no draft or sibling work may start. |
| `delivery_candidate_binding_mismatch` | A durable effect belongs to another Candidate; reconcile and park. |

Coordinator refusals are also closed and bounded. The most important fast-path
codes are `TASK_STATE_INVALID`, `OWNERSHIP_ARG_FORBIDDEN`,
`OWNERSHIP_FILE_INVALID`, `HANDOFF_INSIDE_REPOSITORY`,
`REMOTE_CI_ABSENT`, `REMOTE_CI_NOT_AUTHORITATIVE`, `REMOTE_CI_PENDING`,
`REMOTE_CI_FAILED`, `REQUIRED_CHECK_AMBIGUOUS`,
`REMOTE_CI_OBSERVATION_MISMATCH`, and `REVIEWER_IDENTITY_CHANGED`. A refusal
never authorizes fallback, retry, merge, or cleanup.

## Correction turns

One authorized correction turn receives every current Review, Validation, and
human-feedback finding in a sorted unique batch. Frozen PLAN and skill digests
must remain unchanged; current human decisions and Candidate diff are always
refreshed. The first acknowledgement-only response is rejected without
Candidate admission. A second acknowledgement-only response escalates. Any
changed SHA restarts Candidate-bound draft/CI/Review evidence.

Direct delivery has a separate no-PR route after the same exact Review and CI
quality gates. Its manual mode waits for an explicit bound human action; its
automatic mode uses one exact remote-ref lease. See
[Direct delivery](direct-delivery.md). A pull-request failure or unavailable
GitHub fact never enters that route.
