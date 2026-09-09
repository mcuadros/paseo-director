# Board/List walking-skeleton contract

The minimal Director for Paseo Board and List are two presentations of one
Director Engine snapshot. They never query Dolt, inspect Runs, or infer a Task
state in TypeScript.

## Data path

```text
direct-Dolt TaskStore
  → Go application Board reader
  → engine-owned versioned host contract
  → loopback read-only engine endpoint
  → policy-free TypeScript connector
  → strict Paseo plugin RPC
  → React Native Board or List
```

The Go reader uses only the typed TaskStore port. The connector sends the exact
contract version and canonical schema hash on every request, validates the same
values and final URL on the response, refuses redirects, and strictly validates
the returned snapshot. It makes one request and owns no retry,
state-transition, scheduling, reconciliation, or TaskStore behavior. Errors
crossing into the UI are bounded and contain no backend diagnostics.

The reader samples the TaskStore Event high-water mark before and after a full
read and returns only when both values match. It makes at most three bounded
read attempts when concurrent writes move the cursor or a current Candidate is
temporarily not visible during a torn non-transactional read; continued change
returns an unavailable result instead of binding stale rows to a newer cursor.

Each snapshot has schema version `1`, a decimal-string monotonic Event cursor,
at most 1,000 Task rows, and a 2 MiB serialized-response ceiling. The limits
bound this M1 surface and are not the production-scale proof assigned to
`dir-m5.10`. Rows contain only Task and Project display identity, title,
engine-derived state, optional Run number, and optional exact Candidate SHA.

## Walking-skeleton state

The engine currently has persisted Project, Task, Run, Candidate, Command, and
Event facts. It therefore derives only states those records can prove:

| Persisted facts | State |
|---|---|
| Task or latest Run has a reconciled needs-you fact | `Needs you` |
| Task and no Run | `Queued` |
| Latest Run and no current Candidate | `Building` |
| Latest Run with its current Candidate | `Validating` |

The pure `projection.DeriveBoardTask` function owns this mapping and rejects
Project/Task, Task/Run, and Run/Candidate ownership mismatches. The application
reader performs I/O only and supplies its loaded fact set. The M1 adapter now
normalizes absent later evidence explicitly and delegates to the complete M2
reducer rather than owning a second phase policy. `In review` and `Ready` remain
contract-reserved and are not emitted from the limited M1 source; Candidate
presence does not prove Validation or independent Review.

## M2 engine-only derived-state query

The standalone engine now also owns a separate complete derived-state reducer
and paginated query for future Board/List consumers. It covers Needs you,
Queued, Building, Validating, In review, Ready, and Done membership from closed,
version-bound eligibility, Run, Candidate, claim, Validation, Review, feedback,
delivery, cleanup, human-input, and terminal facts. Missing, stale,
contradictory, or incorrectly bound facts remain visible as stable blocker
codes and never fabricate progress. A current pending human-input fact is the
only input which projects Needs you, and moving or reordering input cards has
no effect on state or canonical order.

The M2 query filters planning metadata and derived state, sorts by lane,
priority, queue time, and Task identity, and uses snapshot/filter-bound opaque
cursors. The application reader accepts engine-side normalized facts and
rechecks the TaskStore event cursor around every read. This is additive engine
behavior: the version-1 loopback response, TypeScript connector, Paseo UI, and
their strict schemas remain unchanged in this Task.

## UI behavior

The panel defaults to Board in a wide layout and List in a compact layout. Wide
Board shows all ordinary lanes and adds `Needs you` only when the engine returns
at least one such row. Compact Board shows one lane at a time through accessible
tabs. Wide List uses fixed Task, Project, State, and Run columns; compact List
uses readable stacked cards.

The Paseo query cache loads immediately and refreshes the full read-only
snapshot every two seconds without automatic retry. This cadence is retained
from the exact Paseo 0.7.2 behavioral measurement recorded in
[the M1.7 refresh evidence](evidence/m1.7/README.md), which observed successive
requests 2,006 ms apart and rendered loading, empty, data, cached-refresh-error,
and initial-error scenes through the real host. A manual `Try again` action
reissues only the read query. Cached data remains visible during a refresh or a
later transient error. Loading, unavailable, empty, updating, and populated
states have visible text and polite live-region announcements. Colors come
only from Paseo theme tokens, compact spacing comes from `layout.compact`, and
no action depends on hover.

Focused Go tests cover fact-to-state mapping, deterministic ordering, empty and
failure results, torn-read retry, ownership mismatches, updates, contract
generation, worst-case response size, HTTP drift rejection, redacted errors,
private runtime configuration, and the real Dolt cursor. TypeScript tests cover
strict RPC/transport validation, redirect refusal, exact response URL,
one-request/no-retry behavior, loopback-only transport, actual ProjectBoard
render scenes, wide/compact lanes, and preservation of engine-supplied state.

The loopback Board listener has no client authentication. Contract headers are
public compatibility metadata. It is admitted only on the single-user Linux
topology with the port confined to loopback and must not be proxied or exposed.
