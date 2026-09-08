---
name: director-independent-review
description: Independently review one exact paseo-director candidate commit against its Beads Task, approved plan, tests, and safety invariants. Use when acting as a reviewer or deciding whether a candidate SHA may proceed to pull request or integration.
---

# Director Independent Review

Review evidence, not the Task Agent's confidence or conversation.

## Establish the review target

Require:

- Beads Task ID and complete acceptance criteria;
- exact Candidate SHA;
- exact base ref/SHA;
- approved Organizer/plan revision when relevant;
- a clean committed target.

Stop if the Candidate is missing, the checkout is dirty, the SHA moved, or the Task cannot be identified unambiguously.

## Isolate the review

1. Run as a normal top-level Reviewer Agent with no Organizer or Task Agent parent; never run as the Task Agent's helper or child.
2. Use a detached disposable checkout of the exact Candidate.
3. Do not reuse or mutate the Task Agent's checkout.
4. Do not receive hidden conclusions or conversational context from the Task Agent.
5. Read the diff, surrounding code, tests, Task, `docs/PLAN.md`, and applicable ADRs directly.
6. Read every current human decision directly from Beads. Use the automatic
   review-handoff manifest only to route attention; it is explicitly
   non-authoritative and is never review evidence.
7. Run `director-review-harness` with the exact manifest, detached checkout,
   Reviewer Agent identity/actor, and a private state file outside the
   checkout. It mechanically proves unchanged Candidate/base/tree/diff and
   durable-context identity while running independent non-conflicting checks
   concurrently and collecting them deterministically.
8. Before the harness, run
   `npm ci --ignore-scripts --no-audit --no-fund` and prove `node`, `npm`,
   `git`, and `go` are on `PATH`. The harness checks these without consuming a
   complete-CI attempt; do not classify missing preparation as a Candidate
   failure.
9. Allow at most one complete maintained CI run for this exact review. A
   second requires the harness's recorded `invalid_environment` or
   `failure_confirmation` reason; never run a third.
10. Use the versioned maintained adversarial harnesses. Do not reconstruct an
   equivalent probe in `/tmp`; promote any novel probe finding to Candidate
   regression coverage or a scheduled follow-up.
11. Leave implementation changes to the Task Agent.

## Review dimensions

Evaluate:

- every acceptance criterion;
- correctness and failure behavior;
- state-machine and idempotency invariants;
- exact-SHA and Git ownership safety;
- recovery, cleanup, and data-loss behavior;
- security, secrets, path traversal, and permission boundaries;
- Linux portability where relevant;
- tests, fixtures, schemas, migrations, and documentation;
- scope discipline and compatibility with the active milestone.

Run independent focused tests when they materially increase confidence. Do not accept test output quoted only by the author as sufficient evidence.

Keep every review dimension complete, but focus model reasoning on the exact
diff, surrounding code, affected invariants, prior findings, and identified
risks. Use the harness's mechanical unchanged-base result instead of repeatedly
re-deriving unrelated immutable history.

## Report

Return this structure:

```text
Verdict: approve_candidate | changes_requested
Candidate: <sha>
Base: <sha>

Findings:
- [P0|P1|P2|P3] <title>
  Evidence: <file:line, command, or observed behavior>
  Impact: <why it matters>
  Required action: <specific correction>

Validation:
- <command/check and result>
- review harness version, attempt, reason, and deterministically collected results

Residual risks:
- <risk or none>

Post-review gates:
- <push, PR, CI, integration, sync, or cleanup gate that must remain pending>
```

Use P0–P3 exactly as defined in `docs/PLAN.md`.

Distinguish Candidate approval from final Task completion. Some acceptance criteria necessarily occur after independent review, including branch publication, PR creation, remote CI, integration, synchronization, and final cleanup. Mark each such criterion as a pending post-review gate; never waive it or claim the Task is Done.

Approve the Candidate only when there are no unresolved P0/P1 findings, every acceptance criterion that can be satisfied before publication is met, and any P2 acceptance has explicit human authority. Task closure remains forbidden to the Reviewer and Task Agent; only the Director Engine or authorized coordinator may close after independently reconciling every post-review gate.

A changed commit requires a completely new review. Record the disposable checkout identity and ownership after reporting; leave lifecycle cleanup to the Director Engine or authorized coordinator.

Also return the closed machine-readable review evidence consumed by
`director-coordinator publish` and later gates:

```json
{"schemaVersion":1,"task":"<task>","candidate":"<sha>","base":"<sha>","verdict":"approve_candidate","reviewer":{"agentId":"<exact-id>","parentAgentId":null,"detached":true,"checkoutCommit":"<sha>"},"dimensions":[{"id":"acceptance","status":"covered"},{"id":"correctness","status":"covered"},{"id":"security","status":"covered"},{"id":"maintainability","status":"covered"},{"id":"readability","status":"covered"},{"id":"design","status":"covered"},{"id":"quality","status":"covered"},{"id":"rigor","status":"covered"}],"p2Risks":[],"humanP2Acceptance":null}
```

For `changes_requested`, do not produce an approval evidence file. A P2 risk
requires an exact separately recorded human acceptance object before the
coordinator CLI will publish. The Reviewer cannot create that acceptance.
