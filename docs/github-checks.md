# GitHub checks and base invalidation

Director Engine records one authoritative remote Linux CI observation for an
immutable Candidate/base/manifest generation. The GitHub connector is a
read-only translator for this phase: it cannot trigger, retry, interpret, or
waive a workflow or check.

This configured GitHub validation path belongs only to pull-request delivery.
Direct delivery continues to consume the exact complete CI observation frozen
into Review; a Run cannot carry both direct-delivery state and GitHub
validation policy or history.

## Frozen check identity

Each Run freezes one workflow database ID and name plus one to 32 required
checks. A Check Run is identified by its configured name, GitHub App database
ID, and App slug. A legacy Commit Status is identified by its context, creator
database ID, and creator login. Display names alone never carry authority.

The connector uses GitHub REST API version `2022-11-28` and retains the
canonical repository database/node identity and authenticated viewer. It reads
workflow runs, Check Runs, and Commit Statuses in independently bounded pages:
at most ten pages of 100 rows. The total count must remain stable and equal the
accumulated rows. A full first page is complete only when GitHub's total count
says it is complete.

The canonical repository database/node identity and authenticated viewer are
re-read after pagination. A repository replacement or identity drift during
the collection invalidates the observation rather than attributing results
from the replacement repository to the configured Workspace.

Every workflow run, Check Run, check suite, and status must name the exact
Candidate SHA. The configured workflow must occur exactly once. Every required
check must have exactly one provider-identity match, and a required Check Run
must belong to that workflow's exact check suite. Duplicate IDs, a name shared
with a legacy status, incomplete pagination, or a different SHA is ambiguous
and produces no authority. When no legacy statuses exist, the durable rollup
is explicitly `checks_only_no_statuses`; otherwise the combined rollup and
every context must pass.

Only bounded identifiers, timestamps, conclusions, numeric GitHub IDs, and
hashes of external URLs enter durable state. Raw responses, logs, URLs,
credentials, private paths, and secret-shaped provider names do not.

## CI and recovery budgets

The validation intent reserves its CI cycle and declared maximum runtime in
the Run's durable runtime-budget ledger before observation. Product policy
caps a lineage at four total cycles even if a wider configuration value is
present. A terminal observation consumes one cycle; an outstanding reservation
survives restart. The same Candidate SHA cannot reserve another completed
cycle, so an unchanged failed SHA is never rerun automatically.

Observation is safe to repeat. Intent, reservation, pending state, terminal
evidence, and downstream authority use Run compare-and-swap writes. Lost
responses are adopted from durable state, and competing coordinators converge
on the same reservation and observation. Exhausted CI or runtime budgets expose
an exact `Needs you` reason and machine-readable wake condition.

## Relevant base changes

Director reads the live base ref immediately before and after the three GitHub
collections. A base different from the Candidate binding, or a ref change
during those reads, atomically:

- removes Validation, CI, Review, publication, Ready, feedback, and integration
  authority from the current Candidate generation;
- invalidates current Validation and moves Review, publication, direct-delivery,
  and feedback state to immutable history while retaining the owned draft
  identity and bounded audit facts; and
- records the prior complete authority snapshot and new live base for the
  correction lineage.

An external publication, direct-delivery, or feedback-to-correction dispatch
which may already have happened is reconciled before invalidation can replace
its binding. Human feedback on current work likewise clears every Validation,
CI, Review, publication, Ready, and integration authority in the same Run CAS;
feedback after integration creates sibling work and never reopens or mutates
the completed Task.

The existing Task Agent receives the base as a deterministic correction input.
The unchanged Candidate fails the disposable-Git ancestry check. Only a new
clean Candidate which descends from the observed live base can replace it; that
append updates the Run/base repository binding atomically, preserves the old
Validation as history, and emits fresh CI and Review keys. Reconciliation of
that new Candidate then converges through the ordinary draft/CI/Review path.

Board and Organizer projections expose `base_revalidation_required`,
`validation_external_wait`, or `validation_ambiguous`; human-action cases also
retain the exact bounded `Needs you` reason and wake condition.
