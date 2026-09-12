# Linux release-scale validation

This evidence contract closes the production TaskStore scale proof deferred by
ADR-0016. The retained author artifact is candidate-bound, owner-only mode
`0600`, and intentionally kept outside Git because it records host paths and
runtime measurements. The exact path, SHA-256, Candidate, tree, command,
measurements, screenshot manifest, and disposition are recorded on Beads Task
`dir-m5.10`.

## Supported topology and scale

The explicit evidence test `TestReleaseLinuxPerformanceScale` runs only when
`DIRECTOR_SCALE_EVIDENCE_FILE` is set. An ordinary package or complete CI run
compiles the test and skips its expensive disposable topology. The evidence
run requires:

- Debian GNU/Linux 13 on `amd64`;
- direct Dolt 2.3.2 on an explicit loopback listener with metrics disabled;
- one typed `DoltTaskStore`, with four-connection control and writer pools;
- 25 native disposable Git repositories with independent canonical remotes
  and real source/common-directory filesystem identities;
- one Project, 25 Workspaces, 500 open Tasks, and 10,000 complete historical
  Tasks;
- 10,501 immutable Command/Event/outcome records and 10,526 aggregate identity
  records, including the Project command and all Task records;
- eight simultaneous planning reads, the configured six-Task-Agent and
  two-Reviewer lanes, and deterministic refusal beyond configured capacity;
  and
- exact Paseo 0.7.2 for the separately retained real-client visual evidence.

Fixture construction is deliberately separated from product validation.
Creating 10,500 independent Tasks through the mutation API would repeatedly
validate an ever-growing complete planning graph and measure fixture setup
rather than the runtime read path. The test therefore bootstraps a canonical,
foreign-key-valid, append-only Command/Event/aggregate ledger in bounded
100-row transactions through a test-only SQL connection. It computes the same
canonical command hashes and advances the same global Event sequence as the
adapter. That connection is closed before every measured read. All scale
queries, graph validation, projection, cursor generation, backup, fresh
restore verification, restart recovery, and post-restart reads then use the
production typed direct-Dolt adapter.

The 10,000 historical fixture records carry `Complete=true` but do not forge
10,000 completed Run, Candidate, CI, independent Review, integration, and
cleanup histories. Their List query consequently selects the complete Task
population through the all-membership scope plus its fixed scale label. This
preserves the reducer's fail-closed rule that Task completion alone cannot
fabricate Done authority. The real Paseo visual fixture separately exercises
the generated Done List contract and its pagination.

## Deterministic gates

Every threshold is encoded in the test and copied into the retained JSON. A
failure makes the test fail; it is not converted into a warning.

| Measurement | Ceiling |
|---|---:|
| bounded fixture seed | 180 s |
| warmed Board/List page p95 | 5 s |
| any warmed Board/List page | 7.5 s |
| eight-request concurrent batch | 30 s |
| exact store observation | 45 s |
| backup creation | 90 s |
| fresh restore validation | 180 s |
| Dolt restart plus typed recovery read | 30 s |
| author process peak RSS | 1 GiB |
| Dolt peak RSS | 2 GiB |
| author/Dolt peak file descriptors | 256 each |
| author goroutines | 160 |
| Dolt threads | 256 |
| complete disposable fixture disk | 3 GiB |

Each surface is warmed twice and then sampled five times. The JSON retains
each sample, minimum, median, p95, maximum, mean, standard deviation, and
coefficient of variation. It also retains concurrent request distribution,
response sizes, page counts, peak resources, backup/recovery fingerprints,
secret-canary booleans, and exact resource-removal observations. The test
enforces 100-row pages, stable cursor binding, no overlap between adjacent
pages, the 4 MiB response bound, exact backup fingerprint equality, exact
post-restart fingerprint equality, backup expiry, and removal of only its
disposable root.

The Candidate-bound command is:

```sh
cd engine
env \
  GOCACHE=/tmp/dir-m5.10-go-cache \
  DIRECTOR_SCALE_EVIDENCE_FILE=/absolute/owner-only/scale-evidence.json \
  DIRECTOR_CANDIDATE_SHA=<candidate-sha> \
  DIRECTOR_BASE_SHA=<base-sha> \
  DIRECTOR_TREE_SHA=<tree-sha> \
  go test ./cmd/director-engine \
    -run '^TestReleaseLinuxPerformanceScale$' \
    -count=1 -v -timeout=20m
```

## Bounded Board/List path

The release-scale run exposed four redundant complete-graph validations per
page. The direct-Dolt adapter now offers optional typed planning reads that
return the Project list and each complete set of individually validated
Project records, including server-owned Task update timestamps.
`PlanningReader` uses them when available and retains its legacy typed
interface fallback. The application layer validates the assembled graph,
computes the dependency report, and owns projection input. It samples the Event
high-water mark before and after the complete read and makes at most three
attempts; the optimization
does not cache across snapshots, expose SQL, or move projection authority into
the adapter.

The projection scan remains linear in the immutable Task input while retained
sortable rows are limited to the requested 100 plus one sentinel. The host
response is limited to 4 MiB. The real Paseo Board and List retain one cursor
page at a time and use `FlatList` windows of 12 Board rows and 16 List/group
rows. Direct-Dolt pools, scheduling capacity, helper capacity, retry counts,
response bytes, and UI render windows all remain finite.

## Maintenance, security, and visual evidence

The run observes and fingerprints the full ledger, creates a daily backup,
restores it into a fresh generated database, verifies constraints and exact
fingerprint equality, drops the validation database, restarts the direct Dolt
server, reopens the same typed store, verifies the same fingerprint, and runs
a post-restart page query. It then expires the exact backup and removes only
its disposable Dolt/Git root. Existing M5.8 migration/maintenance focused
tests and M5.9 safe-data/threat checks remain mandatory author checks.

The test verifies that the M5.9 classifier recognizes a private secret canary
and that the canary is absent from the retained evidence. Evidence reports
only closed booleans and bounded measurements, never credentials or raw SQL
diagnostics.

The real Paseo 0.7.2 screenshot manifest is retained outside Git in a mode
`0600` directory and bound to the same Candidate, tree, source digests, client
bundle, exact viewport, screenshot SHA-256, and visual-inspection result. It
covers desktop dark/light Board and List, compact List, compact single-lane
Board, adjacent List pagination, bounded rendered-row observations, and the
loading, stale, unavailable/error, and degraded states actually exercised.
