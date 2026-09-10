# Runtime time and cost budgets

Director Engine owns one immutable `director.runtime-budget/v1` policy and one
durable ledger for every Run. The Paseo connector translates bounded public
`lastUsage` fields; it does not select a limit, price tokens, acknowledge a
warning, resume work, or decide a lifecycle transition.

## Threshold provenance

The numeric policy is fixed by the approved PLAN and its accepted ADRs:

- consumptive wall-time, token, turn, and optional cost budgets warn and pause
  at 85%, including a reservation which would reach 85%;
- 100% is a hard boundary and no new budget-consuming work starts;
- at most three correction attempts and four total CI cycles are allowed by
  default;
- at most one replacement Task Agent is allowed; and
- the integrated preparation/setup effect retains its two-attempt ceiling.

The preserved `dir-m0.4` and `dir-m0.6` journals are category evidence rather
than policy authority. They show why correction wall time, repeated validation,
setup attempts, replacement, restart/replay, and multiwriter contention must be
accounted: repeated exact-SHA review corrections, provider-session replacement,
long direct-Dolt validation, and repeated cleanup/recovery verification all
consumed real Run time. The later preserved orchestration analysis recommends
the 85% safe boundary and records the owner decision making that threshold
relevant to `dir-m3.6`. Director retains only typed counters, hashes, and bounded
facts; it does not copy those conversations or any credential-bearing output.

Elapsed-time, token, turn, and CI limits come from the frozen effective
Project/Workspace/Task configuration. There is deliberately no universal
token or dollar value hidden in the engine. `costMicrousd` is optional and uses
integer millionths of a US dollar. The example Organizer configuration chooses
7,200 seconds, 200,000 tokens, 32 turns, four CI cycles, and USD 5; those are
example Project values, not provider estimates.

## Accounting

Run wall time uses monotonic TaskStore time from Run creation, so setup,
ordinary Worker turns, helper turns, Reviewer turns, correction cycles,
Validation, recovery, and safe-boundary pauses cannot disappear on restart.
Every dispatch first persists an exact lease-epoch-bound reservation. Provider
usage is applied once to the matching effect, then its reservation is released.
Input plus output tokens are consumed; cached input is retained for visibility
but is not counted twice.

The ledger separately counts setup attempts, correction attempts, CI cycles,
and replacements. A second Validation of the same Candidate is refused. A
setup or correction request with an already-recorded unchanged failure
fingerprint is treated as churn and parks instead of consuming another nominal
attempt. Worker, helper, Reviewer, and correction turns remain individually
visible in the Organizer projection.

Snapshots and activity facts are immutable, bounded, self-hashed, scoped to
one effect, and ordered by a per-agent monotonic cursor. Exact replay is a
no-op. Reused identities, regressing cursors or clocks, changed facts,
arithmetic overflow, missing required token fields, missing cost while a cost
limit is enabled, and provider-unavailable or ambiguous data fail closed.
Director never reconstructs missing cost from model names, pricing tables, or
token counts. The version 1 ledger retains at most 256 model-turn snapshots, so
configuration validates `turns` and `ciCycles` in the closed range 1–256
instead of accepting a limit it cannot durably replay.

## Pause and exhaustion

The first unacknowledged soft threshold for a policy revision records exactly
one warning with dimension, measured amount, limit, and basis-point ratio. It
routes to `Needs you` at the next safe boundary and blocks new model turns,
helpers, Reviews, retries, corrections, setup retries, Validation cycles, and
replacements. A running turn may finish. Its observation and usage persistence
continue while paused; recoverable work is neither discarded nor cleaned.

Only a server-authenticated human command can acknowledge the exact warning
under the unchanged policy revision. Acknowledgement suppresses that revision's
repeat warning but does not raise its limit. A changed limit is a new policy
revision.

At a hard boundary, or on count/churn exhaustion, Director starts no new
budget-consuming work and preserves the Run in `Needs you`. Exact bounded reason
codes distinguish wall time, tokens, turns, cost, correction attempts, CI
cycles, replacement attempts, setup attempts, repeated Validation, setup or
correction churn, unavailable/ambiguous provider usage, clock regression,
overflow, invalid ledger state, and lease fencing. All budget parking has
`cleanupAuthorized=false`.

Organizer planning results expose the policy revision, current/soft/hard/
unavailable state, exact reason code, all four consumptive dimensions,
outstanding reservations, the four count budgets, and Worker/helper/Reviewer/
correction turn counts. Raw provider payloads and source conversations are not
part of that projection.
