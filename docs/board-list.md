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
reader performs I/O only and supplies its loaded fact set. When later milestones
expand the routing reducer to emit complete quality phases, this pure projection
becomes an adapter from that reducer result rather than a second phase policy.
`In review` and `Ready` remain contract-reserved and are not emitted in M1;
Candidate presence does not prove Validation or independent Review.

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
