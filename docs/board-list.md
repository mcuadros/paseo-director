# Board/List product contract

Director for Paseo Board and List are two presentations of one
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

The legacy M1 snapshot has schema version `1`, a decimal-string monotonic Event
cursor, at most 1,000 Task rows, and a 2 MiB serialized-response ceiling. It
remains available to the Home shell. The integrated planning query described
below is the Board/List path used by the Project panel.

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

## M2 integrated derived-state query

The standalone engine owns the complete derived-state reducer and the runtime
paginated query consumed by Director for Paseo. It covers Needs you,
Queued, Building, Validating, In review, Ready, and Done membership from closed,
version-bound eligibility, Run, Candidate, claim, Validation, Review, feedback,
delivery, cleanup, human-input, and terminal facts. Missing, stale,
contradictory, or incorrectly bound facts remain visible as stable blocker
codes and never fabricate progress. A current pending human-input fact is the
only input which projects Needs you, and moving or reordering input cards has
no effect on state or canonical order.

`POST /v1/planning/query` accepts the generated planning contract's Project,
Workspace, Epic, derived-state, priority, label, attention, search, and
stable-sort inputs. The engine supports scheduler order, updated-descending,
priority/FIFO, and Task-key order, with the complete comparison key bound into
an opaque cursor together with the normalized filter and TaskStore Event
snapshot. A cursor from another filter, sort, page-size binding, or Event
snapshot is rejected and the UI refreshes from the first page.

The application reader samples the Event high-water mark around each read and
makes at most three attempts. The direct-Dolt adapter loads one complete set
of individually validated Project records together with server-owned Task
update times, then loads Project Runs and current Candidates through two
further typed bulk reads. It does not issue a per-Task query loop or load all
historical Candidate rows. The ordinary TaskStore methods retain their complete-graph validation
contract; the application validates the assembled cross-record graph once.
This optional typed path avoids reloading the same records once for Projects,
Epics, Tasks, and dependency overrides.

Projection scans each immutable Task input once, retains at most the requested
100 rows plus one next-page sentinel, and returns at most 4 MiB. The 25
Workspace / 500 open / 10,000 historical fixture remains the deterministic
presentation fixture. The production direct-Dolt proof, thresholds, recovery
checks, and retained candidate-bound evidence contract are documented in
[Linux release-scale validation](evidence/m5.10/README.md).

## UI behavior

The panel defaults to a flat Board in a wide layout and an Epic-grouped List in
a compact layout. Users can switch between flat and Epic grouping without
changing engine state or order. Wide Board shows all ordinary lanes and adds
`Needs you` only when the current engine page contains such work. Compact
Board shows one lane at a time through accessible tabs. Done history is an
exclusive query scope and List presentation, never a draggable Board lane.
List and grouped Board use the Task/Epic/Workspace identities already supplied
by the engine; they never infer a lane or rank. Wide List has the fixed columns
Task, State, Workspace, Epic, Priority, and Updated. Compact List retains fixed
Task and Status columns with Workspace/Epic metadata in the Task cell.

Only one cursor page is retained and rendered at a time. Previous/Next actions
preserve an opaque cursor history while every Task and group row uses a stable
engine identity. React Native `FlatList` windows render at most 16 initial Task
or group rows in List mode (and 12 Task rows in one lane), even for the full
historical result set.

The Paseo query cache loads one read-only page immediately and refreshes after
an accepted action, host query invalidation, or an explicit retry, without
automatic retry. A manual `Try again` action reissues only the read query.
Cached data remains visible during a refresh or a later transient error and is
explicitly marked as the last, potentially stale engine snapshot. Loading,
offline, unavailable, empty, updating, stale, and populated states have visible
text and polite live-region announcements. Colors come only from Paseo theme
tokens, compact spacing comes from `layout.compact`, and no action depends on
hover. Pressable controls and Task rows are focusable for keyboard clients,
have semantic accessibility roles, expose selected states, and keep 44-point
touch targets. Paseo supplies the surrounding shell and opens the full filter
matrix in its native desktop-dialog/compact-sheet `Modal`; the plugin body
does not reproduce host chrome. The primary surface uses one compact toolbar,
an unboxed capacity/query summary, and direct Board/List content. Semantic
lane/card accents come only from the active Paseo theme's accent, warning,
success, and muted tokens, so the same information hierarchy works in dark and
light themes without a plugin palette.

Focused Go tests cover fact-to-state mapping, deterministic ordering, empty and
failure results, torn-read retry, ownership mismatches, updates, contract
generation, worst-case response size, HTTP drift rejection, redacted errors,
typed bulk Run/Candidate reads, private runtime configuration, and the real
Dolt cursor. Deterministic scale/property tests assert the exact agreed fixture,
page boundaries, filter/sort invariance, conservative timing, allocation,
retained-row bounds, and the single typed Project-graph read. The explicit
Linux release-scale test adds real direct-Dolt/Git measurements without being
repeated by ordinary local or complete CI package runs. TypeScript tests cover
strict RPC/transport validation, redirect refusal, exact response URL,
one-request/no-retry behavior, loopback-only transport, actual ProjectBoard
render scenes, grouping,
cursor invalidation, wide/compact layouts, and bounded rendered rows.

The loopback Board listener has no client authentication. Contract headers are
public compatibility metadata. It is admitted only on the single-user Linux
topology with the port confined to loopback and must not be proxied or exposed.
