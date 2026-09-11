# GitHub pull-request publication

The standalone Go Director Engine owns pull-request publication policy and
state. Git and GitHub connectors expose only bounded observations and one exact
operation. Neither connector may select a delivery mode, adopt a pull request,
retry a mutation, grant merge authority, resolve a human thread, or fall back
to direct delivery.

## Frozen intent

One publication intent is stored in the current Run and binds all of these
facts:

- Task, Run, Candidate, base, tree, manifest, Candidate generation, and Task
  version;
- owned Task branch and base ref;
- canonical Git remote plus Director repository identity;
- GitHub repository database/node identity, owner/name, and authenticated head
  owner;
- the public SHA-256 of the private ownership value; and
- the exact frozen delivery policy.

The raw ownership value has no field in the Go publication contract. It cannot
enter connector argv, logs, the PR template, TaskStore, or Beads. The stable
public marker contains only Task/Run/branch identity and the ownership hash, so
one open PR can survive a corrected Candidate without making the old Candidate
binding current.

The Run expected-version update is the publication state lock. Every state
transition is compare-and-swap persisted before another coordinator can
dispatch it. This complements the repository coordinator's owner-only process
identity lock: both serialize the same intent/dispatch/observe order at their
respective durable boundaries.

## Publication timings

`publishBeforeReview` defaults to `false`:

```text
Candidate -> exact approved Review -> leased branch push -> one ready PR
```

An explicitly frozen `publishBeforeReview: true` policy enables the draft fast
path:

```text
Candidate -> leased branch push -> one owned draft -> Review -> mark ready
```

A draft is publication evidence only. It always reports
`mergeAuthorized=false`; M4.9 alone may later implement merge authority. A
corrected Candidate first makes the same owned PR a draft, invalidates old
Candidate/Review/Validation/publication readiness, pushes under the exact old
head lease, and deterministically updates that same PR.

M4.4 correction state is a publication gate. An active or parked correction,
Run-level `NeedsYou`, unchanged-SHA rejection, attempt exhaustion, budget stop,
or provider ambiguity permits no Git/GitHub publication operation. A changed
correction Candidate becomes eligible only after its exact `gates_required`
record proves prior authority invalidation and binds fresh CI and Review keys
to the new Candidate generation. Candidate replacement itself refuses while a
publication dispatch remains unobserved.

## Recovery frontiers

Push, create, update, draft, and ready operations use the same durable shape:

1. persist intent;
2. persist fresh authenticated repository/capability and target observations;
3. win the Run compare-and-swap transition to `dispatching`;
4. invoke exactly one connector operation; and
5. observe Git/GitHub before accepting completion.

A mutation response is never completion evidence. Restart or response loss
finds and adopts the exact desired ref/PR state. If a dispatch may have crossed
the handoff boundary but the desired result is absent, the effect parks instead
of retrying blindly.

Branch pushes use only
`--force-with-lease=refs/heads/<task-branch>:<exact-expected-oid>`; an empty
expected OID is valid only after authoritative absence. `main`, `master`, the
configured base, all configured protected branches, non-`task/` refs, changed
heads, and remote identity drift are refused.

## GitHub observation and ownership

Before each mutation, the connector observes the exact API version,
repository database/node identity, authenticated viewer, push/PR capability,
archived/disabled state, TLS result, and rate budget. PR lookup paginates at
most ten pages of one hundred rows. An incomplete page chain fails closed.

The engine adopts exactly one open PR only when repository IDs, head/base refs,
head SHA, author/viewer identity, and exactly one public marker all match. An
unowned PR on the same refs, a copied marker from another author, duplicate
owned PRs, an expected PR which became closed, or ambiguous pagination parks
publication. Closed historical PRs are retained as history and never alias a
new Candidate.

Authentication failures, HTTP 401/403/404/409/422/5xx, rate exhaustion, TLS
failure, timeout, parse failure, and service unavailability are typed
fail-closed results. Observation may back off, but policy remains
`pull_request`; there is no direct-delivery fallback.

## Deterministic public content

The engine renders title/body from bounded facts only: Candidate/base/tree/
manifest, exact Review and Validation IDs/status, residual risk codes, rollback
behavior, and Beads Task/Run/branch identity. It rejects credentials, selected
private-path forms, control text, oversize content, and user-supplied marker
fragments. Author transcripts, full check output, local evidence paths, and
human thread contents are never copied.

This M4.5 boundary intentionally excludes GitHub check/Ready invalidation
policy (M4.6), feedback/thread handling (M4.7), direct delivery (M4.8), and
merge/integration (M4.9). The ports contain no operation for resolving a human
thread or merging a PR.
