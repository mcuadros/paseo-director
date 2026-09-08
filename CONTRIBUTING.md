# Contributing

Director is developed through dependency-ordered Beads Tasks and exact-commit independent review. The approved scope and architecture live in [docs/PLAN.md](docs/PLAN.md).

Each development Task is owned by one normal top-level Paseo Task Agent whose parent field is omitted and whose visible title exactly matches the Task title. It runs in the Task's isolated Execution Workspace/worktree. The Task Agent may create internal helper subagents, but helpers are not Beads Tasks or Task owners. Independent Reviewer Agents are also top-level agents and never children of the Task Agent or a planning context. Task Agents hand off structured Candidate/outcome claims and may perform only authorized correction turns; the Director Engine or authorized coordinator alone reconciles and executes publication, pull-request creation or update, integration, lifecycle cleanup, and Task closure.

## Before starting

1. Run `bd prime` and `bd ready --json`.
2. Select one unblocked Task from the active milestone.
3. Read the complete issue, acceptance criteria, blockers, plan sections, and ADRs.
4. Claim it atomically with `bd update <id> --claim --json`.
5. Work only in the Task's isolated Execution Workspace/worktree and branch named `task/<beads-id>-<short-slug>`.

Use the dependency-free [coordinator CLI](docs/coordinator-cli.md) for the
fresh Task/Candidate snapshot and exact-SHA review handoff. Keep its immutable
context explicit; Task Agents do not invoke its mutating commands.

Do not implement work from a later milestone or expand a Task silently. Record discovered work as a separate sibling Task.

## Commits

Follow `skills/director-commit/SKILL.md`.

Use Conventional Commits and include the owning Task:

```text
feat(scope): add concise behavior

Explain why when the rationale is not obvious.

Beads: dir-xxxx
```

Each commit must be focused, tested, free of secrets, and independently reviewable.

## Independent review

Every implementation commit requires review under `skills/director-independent-review/SKILL.md`.

- Review the exact SHA in a detached disposable checkout.
- Do not give the reviewer the author's hidden conclusions or conversation.
- Generate `director-coordinator review-handoff` once for the exact Candidate;
  its automatic manifest is routing context, not review evidence.
- In the fresh detached reviewer checkout, run
  `npm ci --ignore-scripts --no-audit --no-fund` and verify `node`, `npm`,
  `git`, and `go` are on `PATH` before starting the harness. Its preflight
  rejects missing preparation without consuming the one complete-CI attempt.
- Run `director-review-harness` with one private review-state file. It runs the
  complete maintained CI at most once per exact review and collects independent
  non-conflicting identity/CI results concurrently and deterministically. A
  second full run requires the recorded invalid-run or failure-confirmation
  exception; a third is forbidden.
- Use the versioned maintained adversarial probes. Do not reconstruct an
  equivalent `/tmp` probe when repository coverage exists.
- Route corrections back to the same Task Agent as an authorized correction turn.
- Review a changed SHA again from the beginning.
- Treat `approve_candidate` as permission for the Director Engine or authorized coordinator to publish the reviewed SHA, not permission for a Task Agent, model, connector, or reviewer to publish or close the Task.
- Keep push, PR, CI, integration, synchronization, and cleanup as explicit post-review gates.

The manifest and harness narrow repeated mechanical work only. Review remains
complete across acceptance, correctness, security, maintainability,
readability, design, quality, and rigor. Focus reasoning on the diff,
surrounding code, affected invariants, prior findings, and identified risks;
use the harness to prove that the Candidate/base identity and unrelated
immutable history did not move.

## Pull requests

The Director Engine or authorized coordinator follows `skills/director-pull-request/SKILL.md` and the repository template after exact-SHA approval. Task Agents do not invoke that skill or perform publication/integration effects.

The authorized coordinator uses `director-coordinator publish`, `gate`, and
`integrate` with one external durable state file. After verified integration,
it separately previews `cleanup-plan` and applies that exact plan with
`cleanup-apply`. Direct `git push`, `gh pr create`, unguarded merge, and ad-hoc
resource deletion are not the normal repository workflow.

If the one-shot schedule reclaims the Task checkout after handoff, the
coordinator uses the exact control repository/local Task ref with explicit
`reclaimed` checkout and lifecycle bindings. It preserves the original
agent/workspace IDs and never claims their live state was verified when it was
not. Corrected Candidates use a stable Task/branch/ownership-hash PR marker so
the same open PR can follow the newly reviewed head.

- Open at most one active PR per Task.
- Publish only an independently approved Candidate.
- Include test evidence, review SHA, risks, rollback, and Beads ID.
- Do not use PR comments for agent-to-agent conversation.
- Do not merge while CI, review, base, or acceptance evidence is stale.

After `approve_candidate`, only the Director Engine or authorized coordinator publishes and merges automatically when the reviewed head, relevant base, configured/required checks, mergeability, and human-feedback gates remain valid. The merge command must atomically match the approved head SHA; a pre-merge refetch is not enough. A separate human merge confirmation is not required unless the Task is explicitly manual or one of the risk, policy, ambiguity, history-rewrite, or unauthorized-destructive exceptions in `AGENTS.md` applies.

## M0 spikes

Follow `skills/director-spike/SKILL.md` and use `docs/adr/0000-template.md`.

M0 exists to reject unsafe assumptions. A no-go result is valuable when it prevents a dependency on private APIs or fragile behavior.

## ADR lifecycle

ADR status is normative and uses exactly these values:

- `Proposed`: the decision has not yet completed exact-SHA independent review and integration. It is not an approved plan change or a satisfied milestone gate.
- `Accepted`: an exact Candidate containing the substantively identical decision received `approve_candidate` and was integrated with its reviewed base. An ADR may be authored as Accepted in that Candidate, but the status is not authoritative until those gates complete. A later lifecycle or conditional-resolution reconciliation may rely on already integrated evidence, including changing an Inconclusive outcome after its recorded conditions are satisfied, but the reconciliation must cite that evidence and itself follow the normal review and integration workflow.
- `Superseded`: a later Accepted ADR replaces the decision. The earlier evidence and analysis remain historical, but its Decision is no longer normative.

Decision outcome and lifecycle status are separate. An Accepted `No-go` is a valid approved decision; an `Inconclusive` outcome cannot satisfy a gate that requires a resolved decision.

A partial change keeps the earlier ADR Accepted and uses reciprocal `Amends` / `Amended by` links. A complete replacement marks the earlier ADR Superseded and uses reciprocal `Supersedes` / `Superseded by` links. These relationships describe normative precedence, not the lines touched by a commit. Preserve the earlier Decision text, adding a prominent resolution note when needed, and put the new decision in the later ADR.

The owning Beads Task records the exact Candidate, reviewed base, independent verdict, integration commit, checks, and residual risks. An author assertion or header edit alone never accepts an ADR.

## Repository CI

The Director maintainers own [the repository CI workflow](.github/workflows/ci.yml)
and its checks under `tools/ci/`. Every pull request and update to `main` runs
the same pinned Linux job. Workflow changes follow the normal Beads Task,
exact-SHA review, and integration process; required checks cannot use
`continue-on-error` or another silent skip.

The first product scaffold replaces the pre-product guard with deterministic
locked installation, Go and TypeScript build/type checks, formatting, lint,
unit, contract, responsive-shell, Linux boundary, distribution, and startup
smoke checks. The suite also retains the bounded deterministic M0 contracts for
policy and idempotency, state-file durability, exact-SHA delivery guards, the
practical runtime boundary, operational limits, and worker-timeout cleanup. It
never runs a live provider, live forge mutation, paid model, or Windows job.

The scaffold validator inspects every workflow, follows its own symlinked entry
path, and rejects path/event filters, non-main push coverage, missing pull
request coverage, silent skips, mutable action references, and replacement of
the real suite. Product reducers, claims, policies, scheduling, preparation,
validation routing, liveness, TaskStore, and fake-agent behavior do not exist in
this Task; the validator rejects those product packages until their owning M1
Tasks add the corresponding PLAN section 21.1 tests. Actions plus the Node and
Go runtimes remain pinned to exact versions. An upgrade is an explicit reviewed
workflow change.

## Definition of Done

A Task is complete only after its acceptance criteria, tests, exact-SHA independent review, CI, integration, Beads evidence, and cleanup are all verified. Only the Director Engine or authorized coordinator reconciles those facts, executes lifecycle cleanup, and closes the Task; the Task Agent hands off its structured claim. See section 24 of the plan for the complete policy.
