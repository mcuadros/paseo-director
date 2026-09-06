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
6. Leave implementation changes to the Task Agent.

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

Residual risks:
- <risk or none>

Post-review gates:
- <push, PR, CI, integration, sync, or cleanup gate that must remain pending>
```

Use P0–P3 exactly as defined in `docs/PLAN.md`.

Distinguish Candidate approval from final Task completion. Some acceptance criteria necessarily occur after independent review, including branch publication, PR creation, remote CI, integration, synchronization, and final cleanup. Mark each such criterion as a pending post-review gate; never waive it or claim the Task is Done.

Approve the Candidate only when there are no unresolved P0/P1 findings, every acceptance criterion that can be satisfied before publication is met, and any P2 acceptance has explicit human authority. Task closure remains forbidden until every post-review gate is verified.

A changed commit requires a completely new review. Clean up the disposable checkout after reporting.
