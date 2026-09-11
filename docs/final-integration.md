# Final integration

The standalone Director Engine owns pull-request Ready and final integration.
The GitHub connector can observe one exact repository/pull request or invoke
one expected-head merge, and the Git connector can observe exact refs and the
remote merge graph. Neither adapter selects manual versus automatic behavior,
retries a mutation, changes delivery mode, grants cleanup authority, or closes
a Task.

## Ready gate

One immutable integration binding carries the exact Task, Run, Candidate,
base, tree, manifest, Candidate generation, configuration, repository and
remote, owned pull request, Validation policy/evidence, authoritative CI,
independent Review and Reviewer UUID, feedback state, publication evidence,
Project lease epoch, and frozen integration policy. Admission additionally
requires the completed CI-cycle reservation and rejects any direct-delivery
state or history.

Manual mode persists `ready` without a merge effect. Only a separately
server-attributed authenticated Paseo `integrate_pull_request` action whose
actor, session, decision, binding, Candidate, base, pull request, and policy
all match creates the merge intent. GitHub reviews/comments, ordinary Paseo
feedback, model output, restart, or a Ready projection cannot authorize it.

Automatic mode creates the same intent without human approval only when the
frozen policy says `automatic`. Enabling it is a human security-envelope
expansion; manual remains the default.

## Fresh external gate

Immediately before dispatch the engine observes the GitHub repository, owned
pull request, live Task/base/default refs, every configured workflow Check Run
and Commit Status, and all three GitHub feedback streams. It repeats repository,
pull-request, and Git observations after those bounded scans. Only one unique
passing configured workflow and one exact provider-qualified instance of every
required check are accepted. No new CI cycle is started.

The pull request must be open, non-draft, cleanly mergeable, authored by the
authenticated owner, and bound to the exact repository, node, head/base refs,
Candidate, and default branch. A missing `--match-head-commit` capability
refuses integration. Dispatch uses:

```text
gh pr merge <number> --repo github.com/<owner>/<name> --merge \
  --match-head-commit <exact-candidate>
```

No admin bypass, branch deletion, squash/rebase fallback, direct-delivery
fallback, or ambient repository selection is permitted.

## Recovery and proof

Run compare-and-swap and the current Project lease fence each intent,
observation, dispatch, and outcome write. The merge attempt is durable before
handoff. Every response, including success, timeout, TLS/rate/provider error,
or response loss, is followed by an authoritative observation. Restart at an
intent, dispatch, observation-required, or terminal frontier uses the same
path. Thirty-two coordinators converge through the Run CAS; a completed state
never dispatches again.

Integration completes only when GitHub reports `merged=true` with a timestamp
and the exact historical head, and the remote default ref equals the merge
commit whose ordered parents are `[base, Candidate]` and whose tree equals the
Candidate tree. A textual response or non-null merge SHA alone is insufficient.
Moved head/base, stale Validation/Review/publication, changed checks, actionable
feedback, identity drift, ambiguous pagination, missing atomic support, or a
wrong merge graph fails closed with a bounded Board/Organizer reason.

Feedback or base invalidation waits while a merge handoff is unresolved. Once
that frontier is observed, invalidation preserves the prior integration state
as history and clears Ready/integration authority. Feedback after verified
integration creates sibling work through the existing feedback contract.
