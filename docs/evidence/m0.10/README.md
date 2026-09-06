# M0.10 idempotent command/effect contract evidence

- Evidence date: 2026-09-06
- Task: `dir-m0.10`
- Outcome: Go for the bounded contract model
- Decision: [ADR-0015](../../adr/0015-idempotent-command-effect-contract.md)
- Executable: [`effect-contract.mjs`](../../../tools/spikes/dir-m0.10/effect-contract.mjs)
- Guard mutations: [`guard-mutations.mjs`](../../../tools/spikes/dir-m0.10/guard-mutations.mjs)
- Captured output: [`observed-output.json`](observed-output.json)
- Mutation output: [`mutation-output.json`](mutation-output.json)

## Result and boundary

The deterministic model proves the command, intent, expected-version,
Project-lease, fenced-attempt, Observation, effect-class, retry,
forward-compensation, recovery, and fail-closed contract selected by ADR-0015.
It synthesizes the independently approved lifecycle, provider MCP, direct Dolt
TaskStore, worktree cleanup, ignored-tree recovery, operational, and exact-SHA
GitHub evidence already present on the Task base.

This is an M0 contract fixture, not Director product code. It uses in-memory
TaskStore and external adapters so every schedule is deterministic and no live
Paseo, provider, Dolt, Git, GitHub, repository, credential, paid-model, or
product effect occurs. The only filesystem mutation is one owner-scoped
temporary sentinel created and removed by the fixture to prove its own cleanup.

The evidence does not re-prove or expand an adapter compatibility tuple.
Accepted ADR-0014 and closed `dir-m0.14` select the practical trusted-provider
Linux boundary and amend ADR-0008; this fixture carries its lifecycle-surface,
finite operational-limit, rootless-OCI, and Needs-you requirements without
expanding them. It does not independently authorize M1. A later product adapter
must pass the same state-machine cases with the real disposable mechanism and
all stricter source-ADR tests. The separate M0 exit-gate audit remains open.

## Versions and topology

The installed runtime was inspected on 2026-09-06:

```text
node --version
v26.7.0

git --version
git version 2.47.3

dolt version
dolt version 2.3.2

gh --version
gh version 2.97.0 (2026-07-31)

paseo --version
0.7.2

uname -srmo
Linux 6.12.107+deb13-amd64 x86_64 GNU/Linux

/etc/os-release
Debian GNU/Linux 13 (trixie), Debian 13.6
```

Director `1.0` is Linux-only under ADR-0011. The executable uses Node.js
built-ins supported by the documented Node.js 22 floor; Node.js `v26.7.0` is
the exact evidence runtime, not a new minimum.

The live source evidence remains bounded to its recorded tuples:

- Paseo `0.7.2` and the exact provider CLI/model combinations in ADR-0003 and
  ADR-0005;
- Dolt `2.3.2` and the trusted-engine boundary in ADR-0004/ADR-0012;
- Git `2.47.3` for worktree/ref/explicit-lease behavior;
- GitHub CLI `2.97.0` and REST API `2022-11-28` for ADR-0006; and
- Debian 13.6 / Linux `6.12.107+deb13-amd64` x86-64 for cleanup and recovery.

## Primary contracts reread

These public primary contracts were reread on 2026-09-06 and compared with
the installed versions and the approved evidence:

- [Paseo v0.7 plugin reference](https://paseo.sh/docs/plugins/v0.7/reference)
  identifies v0.7 as current, documents schema-validated backend RPC and SDK
  access, and states that plugin backend code is trusted/unsandboxed on the
  daemon machine. The immutable installed `0.7.2` declarations and M0.2/M0.3
  live runs remain the compatibility authority where online docs have moved.
- [Git `2.47.3` push](https://git-scm.com/docs/git-push/2.47.3/)
  documents explicit expected-value `--force-with-lease=<ref>:<expect>` and
  absent-ref creation. ADR-0006/ADR-0007 provide the exact live outcomes and
  status-2 absence boundary.
- [GitHub CLI `gh pr merge`](https://cli.github.com/manual/gh_pr_merge)
  exposes `--match-head-commit <SHA>` as the PR-head precondition. Installed
  CLI `2.97.0` supplied the tested operation.
- [GitHub REST pull requests, API `2022-11-28`](https://docs.github.com/en/rest/pulls/pulls?apiVersion=2022-11-28),
  [check runs](https://docs.github.com/en/rest/checks/runs?apiVersion=2022-11-28),
  and [pull-request reviews](https://docs.github.com/en/rest/pulls/reviews?apiVersion=2022-11-28)
  define the separate PR, check `head_sha`, and review `commit_id` facts.
  ADR-0006's live sandbox proves their safe composition and the limits of PR
  base and merge projections.
- [Dolt system variables](https://www.dolthub.com/docs/sql-reference/version-control/dolt-sysvars/)
  defines `dolt_force_transaction_commit=1` as permitting conflicts and
  constraint violations. ADR-0004/ADR-0012 therefore require exact listener,
  global, and per-write-session safe-mode checks before every transaction.
- Dolt [backups](https://www.dolthub.com/docs/sql-reference/server/backups/) and
  [remotes](https://www.dolthub.com/docs/sql-reference/version-control/remotes/)
  provide the public verified-backup and separate synchronization substrate
  exercised by M0.4/M0.5; the exact direct-SQL transaction behavior is carried
  by the independently reproduced M0.4 fixture rather than inferred from a
  moving online page.

No online statement is used to widen an exact-version live result.

## Source decisions synthesized

| Source | Contract fact carried into ADR-0015 |
|---|---|
| ADR-0003 lifecycle | Persist before create; create Task/Reviewer Agents top-level with initial prompt; relist exact labels after result loss; treat stale status as ambiguous; require full archive/process termination; resume only from durable facts; replace at most once. |
| ADR-0005 MCP | Bridge scope is immutable; unsupported tuple fails before governed work; exact MCP policy is defense in depth; bridge writes commands and cannot own workflow/effects; no implicit fallback. |
| ADR-0004 TaskStore | One trusted engine owns SQL credentials; immutable command payload/key; transactional aggregate/Event/Audit; one expected-version winner; safe-mode and identity failure stops writes. |
| ADR-0012 operations | Git/Dolt Sync are separate effects; partial success stays visible; restart queries durable commands; backup is verified through fresh restore; migration stays paused and restores to fresh state on failure. |
| ADR-0007 cleanup | Exact identity and recovery facts are revalidated before each destructive command; one-use gate/argv binding; attempted-present delete is ambiguous; terminal reappearance is preserved; no force fallback. |
| ADR-0013 recovery | 10,000/25,000 entries, 512/256 MiB, 64 KiB sequential buffer, 180/480/540 seconds, 192 MiB RSS growth, seven days, 10% disk floor, and five full gates are hard expansion ceilings. |
| ADR-0006 GitHub | Canonical repository ID; exact ref leases; unique PR marker; check/review SHA binding; live base observation; expected-head merge; merged state plus exact graph; compare-delete cleanup. |
| ADR-0008 security | Maximal threat analysis as amended by ADR-0014; engine/adapter is trusted; all external facts are untrusted and bounded; no secrets/raw outputs in durable evidence; exact JIT ownership. |
| ADR-0014 Linux boundary | Trusted-provider/rootless-OCI scope; canonical admission of four automatic worktree lifecycle surfaces before workspace create; exact server-derived human approval for non-empty surfaces; finite operational limits before launch and periodically; missing/exceeded facts park in Needs you. |

## Executable model

The fixture contains a small reference state machine rather than a product
adapter:

- restart-stable ingress command identity, separate actor attribution, and
  canonical payload hashes;
- expected-version transactions with injected rollback before commit;
- deterministic Effect identities and six closed effect classes;
- monotonic Project epochs, observe-only takeover, consumed prior-process
  liveness evidence, and per-attempt one-use permits;
- immutable, single-use holder/epoch/purpose/attempt/Effect-revision/age-bound
  external Observations;
- canonical worktree lifecycle-surface admission and finite operational-limit
  Needs-you gates before dispatch and during reconciliation;
- effect-specific fake external state and handoff counters;
- forward compensation as a new linked Command/Effect;
- separate Git and Dolt synchronization effects; and
- exact ADR-0013 recovery ceilings plus one temporary-root cleanup assertion.

The model deliberately separates a proven pre-handoff failure from a possible
handoff. An adapter result is not terminal evidence; the state completes only
after an authoritative observation. It also leaves an old effect bound to its
original Candidate/base rather than mutating history to a new revision.

## Minimum reproduction

From the repository root:

```text
node --check tools/spikes/dir-m0.10/effect-contract.mjs
node --check tools/spikes/dir-m0.10/guard-mutations.mjs
node tools/spikes/dir-m0.10/effect-contract.mjs
node tools/spikes/dir-m0.10/guard-mutations.mjs
```

The third command prints the complete deterministic report captured in
`observed-output.json`. The fourth creates eight owner-only mutated copies,
deletes one named guard from each, proves every mutation fails its focused
negative fixture, and prints `mutation-output.json`. One invocation of each
exercises the full matrix. No paid model, server, network call, live repository,
or external service is needed. The mutation command needs permission to spawn
its bounded child Node syntax/test processes; a sandbox-level `EPERM` is a
harness failure and is never counted as a killed mutant.

To verify deterministic output without adding a committed artifact:

```text
run_one="$(mktemp)"
run_two="$(mktemp)"
node tools/spikes/dir-m0.10/effect-contract.mjs >"$run_one"
node tools/spikes/dir-m0.10/effect-contract.mjs >"$run_two"
cmp "$run_one" "$run_two"
sha256sum "$run_one" docs/evidence/m0.10/observed-output.json
node tools/spikes/dir-m0.10/guard-mutations.mjs >"$run_one"
node tools/spikes/dir-m0.10/guard-mutations.mjs >"$run_two"
cmp "$run_one" "$run_two"
sha256sum "$run_one" docs/evidence/m0.10/mutation-output.json
rm "$run_one" "$run_two"
```

The disposable paths above contain only the public JSON report. The evidence
run removed both exact files after comparison.

## Observed result

The captured run reports 13 passing groups and zero failed assertions. The
separate mutation run kills all eight targeted guard deletions with zero
survivors:

| Group | Exact observation |
|---|---|
| Command identity | One atomic Effect intent; aggregate version `7 -> 8`; injected pre-commit fault left no rows; same request replayed; different and non-JSON payloads rejected; stale-version outcome remained replayable; executor attribution changed from `engine-a` to `engine-b` while the restart-stable `system/scheduler` actor and ingress command key replayed the original result; a different actor on an already-bound ingress audience was rejected. |
| Crash boundaries | Six cut-points from before intent through terminal replay; no partial intent, duplicate handoff, accepted stale token, or duplicate completion Event. |
| Lease fencing | Epochs `1,2,3`; takeover with live prior executor remained observe-only; three durable liveness Observations were recorded and two exact absence proofs consumed to enable dispatch; stale dispatch and stale finalization failed; one external handoff. |
| Observation guards | Exact age `10` is accepted; six negative cases reject stale epoch, different holder, age `11`, reuse, wrong attempt/Effect revision, and a post-unknown retry without a new precondition Observation. The fixture maximum is 10 deterministic model-ms, not a product default. |
| ADR-0014 gates | Missing and stale lifecycle admission park before workspace creation; one exact server-derived human approval permits one handoff; missing/exceeded finite operational limits park; a periodic over-limit outcome enters Needs you; all 28 externally mutating catalog kinds require finite limits. |
| Unknown outcomes | Lost create response adopted the exact created state; ambiguous agent continuation parked after one handoff; lost successful delete adopted absence; attempted-present delete parked after one handoff; idempotent archive retried twice; exhausted retry budget parked. |
| External evidence | Candidate/base change invalidated old evidence; repository identity stayed bound; terminal drift created an anomaly without re-execution; outage did not become absence. |
| TaskStore health | Global/session unsafe modes, connection reset, duplicate initialization, and listener mismatch produced the five captured codes and zero unsafe writes; one `store_only` Effect completed atomically with zero attempts; proven pre-handoff failure made zero external handoffs. |
| MCP ingress | Caller-selected scope and revoked capability rejected; same request replayed; conflicting payload rejected; measured Effect-count delta was zero. |
| Compensation | Original and forward-compensation evidence both remained terminal; a foreign target made zero handoffs; original audit-entry counts were `3` before and after compensation. |
| Composite Sync | Git success remained durable while Dolt failed before handoff; Git was not repeated; each stream made one handoff; combined success projected only after both exact outcomes. |
| Cleanup recovery | Exact Linux limits accepted and expansion refused; destructive gate was one-use; lost deletion reconciled from exact absence; owned temporary root absent. |
| Catalog | Six effect classes; 32 unique planned Effect kinds; all 14 acceptance-critical plan effects present; absent-ref explicit lease exercised. |

Targeted mutation results:

| Deleted guard | Fixture which killed it |
|---|---|
| Observation lease epoch | `OBSERVATION_EPOCH_MISMATCH` negative case |
| Observation lease holder | `OBSERVATION_HOLDER_MISMATCH` negative case |
| Observation single use | `OBSERVATION_ALREADY_CONSUMED` negative case |
| Observation maximum age | `OBSERVATION_EXPIRED` negative case |
| Observation attempt/Effect revision | `OBSERVATION_ATTEMPT_MISMATCH` negative case |
| Exact write-session commit mode | `UNSAFE_SESSION_COMMIT_MODE` negative case |
| Worktree lifecycle admission | `WORKTREE_LIFECYCLE_ADMISSION_MISSING` negative case |
| Finite operational limits | `OPERATIONAL_LIMIT_EXCEEDED` negative case |

### Crash cut-points

The six deterministic schedules are the minimum distinct durable/external
boundaries:

1. before T0 Command/intent commit;
2. after intent commit and before dispatch authorization;
3. after T2 authorization and before permit consumption/handoff;
4. after durable handoff marker and before an externally visible effect;
5. after external effect and before response/result persistence; and
6. after authoritative Observation and after terminal projection, including a
   repeated reconciliation pass.

The model additionally injects known failure before handoff, external outage,
lease expiry during an in-flight result, lost destructive results, external
drift, ownership mismatch, retry exhaustion, and TaskStore unsafe-mode/
listener mismatch.

## Effect catalog coverage

The executable catalog contains 32 kinds:

```text
configuration.apply_commit
project.execution_lease
taskstore.aggregate_write
taskstore.git_sync
taskstore.dolt_sync
taskstore.backup_publish
taskstore.restore_fresh
taskstore.schema_migrate
paseo.workspace_create
paseo.task_agent_create_with_prompt
paseo.reviewer_create_with_prompt
paseo.helper_reserve
paseo.helper_observe
paseo.agent_continue
paseo.agent_archive
paseo.workspace_archive
git.branch_create
git.recovery_ref_create
git.private_artifact_publish
git.worktree_quarantine
git.worktree_remove
git.local_ref_delete
git.remote_ref_push
git.remote_ref_delete
git.recovery_artifact_expire
git.recovery_ref_expire
github.pull_request_create
github.pull_request_close
github.pull_request_merge
direct.target_integrate
diagnostic.bundle_publish
diagnostic.bundle_expire
```

Read-only queries, check/review/feedback polling, Doctor, and reconciliation
scans create Observations rather than mutation Effects. Events and subscriptions
only wake the scan. Candidate recording is a TaskStore-only aggregate command;
the engine derives it from Git facts rather than an agent claim.

## Integrity, secrecy, and cleanup

The executable SHA-256 at evidence capture is:

```text
7411338c6de764c334201ec9a09f32345e5ee6a99031103c59227841e4ab5347  tools/spikes/dir-m0.10/effect-contract.mjs
490e59e5d5c8f751e908609afdfb3ce856370004c71854ed38f9bda8ff5f0447  tools/spikes/dir-m0.10/guard-mutations.mjs
f8df28109dcf1fe4a329d5e1197e4aafa5026dd2f7515062194c24457b99cc66  docs/evidence/m0.10/observed-output.json
5ab3fbfd6ad591f374da59eef7cd93b6c09f412cb33ed999a7f7d22991cdeb5e  docs/evidence/m0.10/mutation-output.json
```

The reports contain deterministic fictional IDs and no absolute temporary
path, environment, credential, SQL, raw command output, repository content, or
agent conversation. The contract fixture starts no child process. The mutation
fixture starts only bounded Node syntax/test processes over its eight owner-only
copies, opens no listener, and removes its exact `mkdtemp` root before reporting
`temporaryRootAbsent: true`.

The determinism procedure produced byte-identical reports, matched the
committed `observed-output.json`, and removed its two exact comparison files.
The repository Task worktree and development Beads/Dolt server are outside the
fixture and are not cleanup targets.

## Limitations and fail-closed implications

- This is executable specification/model evidence, not a live adapter test.
  Product work must retain the exact source-ADR fixtures and add real adapter
  fault injection at T0-T5.
- A Dolt epoch fences TaskStore state, not an external system. Linux takeover
  must consume a new bounded liveness Observation proving the previous engine
  and dispatch process trees absent; ambiguity leaves the new holder
  observe-only.
- Every Effect Observation is single-use and bound to holder, epoch, purpose,
  attempt, Effect revision, immutable target/binding, and a positive finite
  adapter maximum age against TaskStore time. An unknown-outcome observation
  cannot authorize the retry it classified.
- Authoritative absence is adapter-specific. A timeout, 404 from an ambiguous
  endpoint, authentication failure, parse error, or inaccessible path is not
  generalized to absence.
- A unique-create retry after authoritative absence also requires the old
  dispatcher absent and the source adapter's bounded consistency rule. If that
  rule cannot be proven, the Effect parks.
- Nonrepeatable agent progress is never resent after a possible handoff.
  Destructive presence after a possible handoff is never deleted again.
- The effect catalog cannot admit a new kind by analogy. It needs an explicit
  class, identity, facts, handoff point, compare/ownership predicate, limits,
  and tests.
- Accepted ADR-0014 and closed `dir-m0.14` select the trusted-provider Linux
  boundary which this contract consumes. Workspace creation parks before any
  Paseo call when lifecycle admission is absent/stale, and every applicable
  Effect parks when finite operational policy or current/periodic facts are
  missing or exceeded. The fixture does not broaden ADR-0014's trust scope.

## Independent verification

Pending a fresh top-level Opus xhigh Reviewer Agent in a detached disposable
checkout of the exact Candidate SHA. The Reviewer must run the minimum
reproduction, verify deterministic output and executable hash, inspect all
ADR-0015 links and source-ADR consistency, and confirm that no M1 product code
or live external effect is present.

Publication, merge, Task closure, and removal of the author Execution Workspace
remain explicitly outside this handoff.
