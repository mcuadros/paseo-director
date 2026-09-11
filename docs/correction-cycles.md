# Review and CI correction cycles

Director Engine owns correction policy and persists one `director.correction-state/v1` lineage inside the active Run. The Paseo connector continues to translate only the existing `send_agent_prompt` and `agent.observe` capabilities. Git observation uses the exact-Candidate port, and no correction path can publish, integrate, clean up, create a hidden Task Agent, or select a replacement provider.

## Whole-batch input

One correction batch includes four source snapshots in fixed order: independent Review, deterministic Validation, authoritative complete Linux CI, and current human feedback. A zero-count snapshot is required when a source has no current findings, so omission is distinguishable from an empty result.

The human-feedback snapshot is produced by the exact-context ingestion contract
in [Paseo and GitHub human feedback](human-feedback.md). It is routed through
this correction lineage rather than a second feedback-specific prompt loop.

Every finding is bound to the current Candidate ID and SHA and contains only a bounded class, P0-P3 severity, summary, acceptance-criterion IDs, and one to eight content-addressed evidence references. Raw CI logs, archived agent histories, paths, credentials, and transcripts have no field in the schema. Exact duplicate source rows collapse; conflicting identity reuse fails closed. The engine sorts the unique findings and separately fingerprints the findings, blocking classes, Candidate, acceptance coverage, and complete batch.

The regression fixtures under `engine/domain/correction/testdata` retain only sanitized classifications derived from the M0.4 and M0.6 journal analysis. They contain no raw history or credential-shaped value.

## Dispatch and accounting

A correction stays with the Run's persisted original top-level Task Agent UUID. Before dispatch, the engine rereads the Project lease, Run version, current Candidate authority, primary session, PLAN/skill digests, current decision and diff digests, provider state, and all m3.6 budgets. One CAS winner reserves wall time, tokens, one turn, optional cost, and one of exactly three correction attempts before the nonrepeatable prompt can cross the host boundary.

Prompt intent, dispatch, terminal observation, provider usage, output, Candidate observation, Candidate append, and fresh-gate projection are separate durable frontiers. A possible prompt handoff is observed and never blindly resent. Missing, stale, conflicting, unavailable, or ambiguous facts park with an exact `Needs you` reason and `cleanupAuthorized=false`.

An unchanged SHA never creates a Candidate and never starts CI or Review. The first acknowledgement-only or unchanged-SHA output is rejected; repetition escalates. A changed SHA goes through the complete m4.2 claim, Git observation, manifest, and append path. The append atomically records the new current Candidate generation, snapshots the prior Candidate authority as history, clears Validation/Review/CI/publication/feedback/Ready/integration authority, and carries the old Candidate and evidence records forward as immutable history.

The resulting gate plan has deterministic complete-CI and Review-binding keys and requires both gates fresh for the new Candidate. The runtime budget ledger independently refuses another complete Validation cycle for the same Candidate.

When the live relevant base moves, its exact observed SHA is an engine-owned
correction input. The unchanged Candidate cannot be revalidated against that
base. Candidate observation uses the new base and repository-binding hash, and
the atomic append updates the Run base only after ancestry and clean-worktree
proof pass; see [GitHub checks and base invalidation](github-checks.md).

## Loop stops and visibility

The automatic lineage limit is exactly three attempts. Time, token, turn, optional cost, CI, provider-usage, and correction-count limits can stop it earlier. A repeated root-class fingerprint without productive acceptance-coverage or finding-resolution progress is churn and stops. A new blocking class after the cap also stops. P2 or other explicit human decisions, provider unavailability, frozen-context drift, primary identity drift, and ambiguous nonrepeatable progress route to `Needs you`.

The Board keeps its stable attention categories for filtering while Organizer and Task details expose the exact correction reason, human-action flag, and machine-readable wake condition. Productive attempts record added/removed acceptance coverage; unchanged acknowledgement and no-progress changed commits are classified as churn.
