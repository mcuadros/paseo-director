# Real-incident conformance corpus

This Candidate preserves the safely publishable part of the coordinating
agent's 2026-09-06 through 2026-09-11 incident record in
`engine/domain/execution/testdata/real-incident-corpus.v1.json`. The corpus is deliberately
marked partial. It does not turn excerpts into purported whole provider
messages and does not cross the Task's raw-state, archived-session, ownership,
header, token, or secret-bearing-record boundary.

## Coverage and exact obstacle

The documented Paseo list, status, and curated-activity surfaces report 11
provider failures, including two content-filter refusals and two occurrences
of the same app-server/bubblewrap failure, plus 55 historical permission
stalls and one shared TaskStore outage. Those surfaces omit worker-notification
bodies, historical permission payloads, archived-agent `lastError`, and the
complete store-outage observation. The archived app-server agent's curated
activity is unavailable after its workspace was archived. Targeted Beads
searches contain no additional trace.

The fixture therefore contains stable aggregate records for every reported
occurrence, but only six runnable probes: the two bubblewrap excerpts, the two
content-filter excerpts, the aggregate permission behavior, and the bounded
store-outage refusal phrase. Seven provider-error bodies cannot be probed at
all. Every partial or missing record names what is absent. This is
`DIR-M5.13-GAP-SOURCE-BOUNDARY`, not completed whole-trace acceptance.

## Findings

- `DIR-M5.13-F001`: the bubblewrap excerpt produces
  `policy_rejection` / `terminal_policy` because it contains `sandbox`. The
  observed cause was a missing host dependency, corresponding to
  `configuration_rejection` / `terminal_configuration`. The wrong class
  selects the wrong frozen operator-policy switch.
- `DIR-M5.13-F002`: with the produced terminal class, the reducer selects
  `archive_original` and then `authorize_replacement`; `adopt_existing` is
  unavailable. The coordinator's observed-correct action preserved and
  adopted the existing agent/worktree, while the immediate replacement-path
  retry failed before Task execution.

This Candidate records those divergences and changes no classifier regular
expression, failure vocabulary, recovery branch, or policy default.

## Repeatable redaction proof

Run:

```sh
npm run check:real-incident-corpus
```

The verifier rejects agent UUIDs, absolute home/worktree paths, Paseo
workspace identifiers, IP addresses, common DNS host names, email addresses,
authorization headers, bearer tokens, and common secret assignments. It also
requires every identity field to use a stable placeholder and requires every
partial/missing trace to keep `providerText` null with an explicit
`missingTrace` explanation.

## Provisional focused validation

The provisional worktree passed these focused checks:

```text
npm run check:real-incident-corpus
  redaction=verified; fixtures=7; reported occurrences=11/55/1;
  divergence findings=DIR-M5.13-F001,DIR-M5.13-F002
node --experimental-strip-types --test tests/primary-host-connector.test.ts
  pass
GOCACHE=<TEMPORARY_CACHE> go -C engine test ./domain/execution
  pass
npm run typecheck
  pass
npm run format:check
  pass
```

The `11/55/1` counts are provider failures, permission stalls, and TaskStore
outages respectively. `GOCACHE` names a disposable local build cache, not a
committed path. Complete CI, independent Review, publication, and integration
remain pending until the final base is established.
