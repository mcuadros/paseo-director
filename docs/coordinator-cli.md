# Coordinator CLI

`director-coordinator` is the repository delivery tool for an authorized
coordinator. It batches the Beads, Git, GitHub, and documented Paseo CLI reads
needed around one exact Task Candidate. It is dependency-free, Linux-only, and
uses direct process argument arrays without a shell.

The Task Agent may run only `snapshot` and `review-handoff`. Publication,
integration, and lifecycle cleanup remain coordinator-only.

## Contract

Every command requires an explicit immutable context:

```text
--task --actor --repo --repo-id --remote --base-ref --base
--branch --candidate --head-owner --ownership
--checkout --checkout-state --control-repo
--agent-id --workspace-id --lifecycle-state --pr
```

`--base` and `--candidate` are full 40-character object IDs. The branch must
be `task/<task-id>-...` and cannot equal `main`, `master`, or the base ref.
Repository database ID, Git remote, live base, local/remote Task refs, shared
Git common directory, Task record, and any available Paseo agent/workspace are
re-read and matched. `--ownership` is a private opaque label: only its SHA-256
is written to the non-secret manifest and public PR marker.

Use `--checkout-state present` while the exact owned Task worktree remains.
Use `--checkout-state reclaimed` only when both its path and Git registration
are absent but the exact local Task ref remains at the Candidate. Post-handoff
`publish`, `gate`, `integrate`, `cleanup-plan`, and `cleanup-apply` then operate
from `--control-repo` without recreating a worktree. Use
`--lifecycle-state active` with two exact Paseo IDs only while their live facts
remain observable; use `reclaimed` with those same recorded IDs after the
one-shot schedule reclaims them. Reclaimed lifecycle bindings are reported as
recorded inputs with `verified: false`, never as verified archival. The literal
`none` for both IDs is valid only when the Run never had those resources.

Mutating commands also require an absolute `--state-file` outside every Git
checkout. The file is mode `0600`, atomically replaced and filesystem-synced.
It records immutable bindings and effect phases before dispatch. A Linux
process-identity lock prevents concurrent PR creation; a stale lock is removed
only after its exact process identity is absent. Preserve this state file
through publication, integration, and cleanup.

Output is one compact JSON document. Exit `0` means the requested result is
proven. Exit `2` is a structured fail-closed refusal or interruption. Exit `1`
is an unexpected internal failure. Raw command output, credentials, paths from
GitHub feedback, and comment bodies are not copied into refusals.

For an active lifecycle binding on a password-protected exact Paseo `0.7.2`
daemon, inject the documented `PASEO_PASSWORD` into the coordinator process
environment and keep `PASEO_HOST` separate from it. Never place the password
in an option, connection URI, evidence file, or state file. The minimized
command runner selects the password only into the child environment of the
exact public `paseo inspect <agent-id> --json` and
`paseo workspace ls --json` reads. Git, GitHub CLI, Beads, npm, Go, other Paseo
verbs, and sibling executables never receive it. Raw Paseo responses are not
copied into coordinator output or diagnostics. Missing or rejected
authentication returns `PASEO_AUTH_REQUIRED` or `PASEO_AUTH_FAILED`; an
interrupted or unavailable read returns `PASEO_LIFECYCLE_READ_FAILED`; and a
response that echoes the selected credential returns
`PASEO_LIFECYCLE_RESPONSE_REDACTED`. Each is bounded and retains no daemon
response content.

## Commands

- `snapshot` returns the bound Task, clean local Candidate, live base/head,
  canonical GitHub repository and owned PR, plus exact top-level Task Agent and
  isolated workspace facts when supplied.
- `review-handoff` repeats the fresh snapshot and emits the exact detached
  Candidate/base handoff. It refuses a dirty worktree, moved branch, changed
  base, missing commit, wrong repository, parented agent, or mismatched
  workspace. `--validation-file` is required. The result includes an automatic
  `authoritative: false` manifest binding the Task acceptance text, current
  human-decision and prior-finding comment hashes, Candidate/base trees, raw
  diff hash and changed paths, author validation references, repository/branch
  ownership, and every pending post-review gate. Its manifest hash accelerates
  routing but is never review evidence.
- `publish` requires `--state-file`, `--review-file`, `--manifest-file`,
  `--review-harness-file`, `--validation-file`, `--expected-remote-head`,
  `--title`, and `--body-file`. The manifest/harness pair must bind the same
  Task/Candidate/base and prove a latency-contract-compliant passing attempt.
  It pushes only the owned Task ref with an exact lease, then adopts or creates
  one PR carrying a Candidate/base-independent Task/branch/ownership-hash
  marker. A corrected Candidate uses a new Candidate-bound state file, pushes
  the same Task branch with an exact old-head lease, and adopts the same open
  PR at its fresh live head. Closed historical PRs do not alias or block a new
  owned PR; an unmarked open PR still fails closed.
- `gate` requires the exact PR number, review/manifest/harness/validation
  files, and one or more repeated `--required-check` values. It requires the live base/head tuple,
  open non-draft mergeability, every observed check/status to pass, each named
  check exactly once, and a complete bounded commit-status page. When that
  page contains no legacy status contexts, the successful required Check Runs
  produce an explicit `checks_only_no_statuses` rollup; otherwise the combined
  status and every exact-Candidate context must be successful. The gate also requires
  no requested reviewers, no current changes-requested or
  ambiguous review, no human issue comment, and no unresolved human review
  thread. It re-reads Beads and refuses if the manifest's binding human-decision
  or prior rejected-review reference set changed. `publish` and the final
  pre-merge gate apply the same durable-context comparison.
- `integrate` adds `--state-file`, repeats the complete gate immediately before
  mutation, proves that installed `gh` supports `--match-head-commit`, and
  invokes merge mode with that exact Candidate guard. Success requires GitHub
  merged state plus an exact `[base, Candidate]` parent list, a merge tree equal
  to the Candidate tree, and reachability from the fresh live base. If the
  forge merges after an undetectable base race, the state becomes
  `needs_manual_reconciliation`; cleanup remains blocked while the coordinator
  preserves every resource, records the observed merge/failure code, and asks
  the owner to decide repair or acceptance. Retrying cannot convert that
  anomaly into verified integration.
- `cleanup-plan` requires the completed integration state and emits a hashed
  plan for only the exact agent, workspace/worktree, local Task ref, and remote
  Task ref. Dirty or ignored data, a running agent, ambiguous ownership, or a
  changed ref refuses the plan.
- `cleanup-apply` requires `--state-file` and `--plan-file`. The plan file may
  be the complete JSON emitted by `cleanup-plan`. Every resource is re-read
  before its effect. Agent/workspace archival is idempotent; destructive
  worktree/ref deletion accepts absence only after a recorded attempt or an
  explicit reclaimed binding. If Paseo workspace archival leaves an exact
  clean owned Git worktree registered, the CLI performs the separately recorded
  non-force Git removal step before touching refs. A
  present target after possible handoff is preserved and routed to a refusal,
  even if it reappears at the same SHA.

`publish` and `integrate` additionally consume these closed evidence files:

```json
{"schemaVersion":1,"task":"dir-x","candidate":"<sha>","base":"<sha>","verdict":"approve_candidate","reviewer":{"agentId":"<id>","parentAgentId":null,"detached":true,"checkoutCommit":"<sha>"},"dimensions":[{"id":"acceptance","status":"covered"},{"id":"correctness","status":"covered"},{"id":"security","status":"covered"},{"id":"maintainability","status":"covered"},{"id":"readability","status":"covered"},{"id":"design","status":"covered"},{"id":"quality","status":"covered"},{"id":"rigor","status":"covered"}],"p2Risks":[],"humanP2Acceptance":null}
```

```json
{"schemaVersion":1,"task":"dir-x","candidate":"<sha>","base":"<sha>","checks":[{"id":"maintained-linux-ci","command":["npm","run","ci"],"status":"passed"}]}
```

If review lists a P2 risk, `humanP2Acceptance` must instead contain
`actorKind: "human"`, a durable `recordId`, and the exact set of accepted risk
codes. Unknown fields or looser verdict/status values are rejected.

## Typical handoff

Run the executable through the repository package:

```text
node tools/coordinator/cli.mjs review-handoff <immutable context options>
node tools/coordinator/cli.mjs publish <immutable context options and publication evidence>
node tools/coordinator/cli.mjs gate <immutable context options and required checks>
node tools/coordinator/cli.mjs integrate <same gate options plus state file>
node tools/coordinator/cli.mjs cleanup-plan <immutable context plus state file> > /private/control/cleanup-plan.json
node tools/coordinator/cli.mjs cleanup-apply <same context plus state and plan files>
```

The coordinator records each returned JSON result in Beads under its explicit
`paseo:<agent-id>` actor. It never pushes `main`, uses `--admin`/`--auto`,
resolves GitHub feedback, force-removes a worktree, deletes an unknown ref, or
silently changes repository, delivery mode, merge strategy, or authority.

## Independent review harness

The versioned `director-review-harness` is the standard entrypoint for an
exact-Candidate review. Give it the complete `review-handoff` JSON, the
detached reviewer checkout, a private state file outside that checkout, the
exact Reviewer Agent ID, its Paseo actor, and `--reason initial`:

Before invoking it in a fresh detached checkout, run exactly:

```text
npm ci --ignore-scripts --no-audit --no-fund
```

Ensure `node`, `npm`, `git`, and `go` are all available on `PATH`. The harness
verifies the root and installed lockfiles plus those four executables before it
creates the attempt record. Missing preparation or PATH is therefore an
invalid precondition and does not consume the single complete-CI attempt.

```text
node tools/coordinator/review-harness.mjs run --manifest /private/control/handoff.json --checkout /exact/detached/reviewer --state-file /private/control/review-state.json --review-id reviewer-agent-id --actor paseo:reviewer-agent-id --reason initial
```

Harness version 1 starts the complete maintained Linux CI and mechanical
manifest-identity check concurrently under a declared parallelism of two, then
sorts their structured results by check ID. The identity check directly
re-reads the Beads acceptance/human-decision/finding references, detached
Candidate and trees, raw diff, clean state, GitHub remote, and live base SHA.
This mechanically proves unchanged immutable history; it does not turn the
manifest into evidence.

The private review state admits one complete CI run for an exact
review/Candidate/base/manifest tuple. A second is possible only with
`--reason invalid_environment` or `--reason failure_confirmation` and a
non-empty `--reason-record`; the previous recorded result must support that
classification. A third is refused. An interrupted first run is recorded as
running and may resume only through the explicit invalid-environment exception.
The harness uses an exact Linux process-identity lock so concurrent invocations
cannot consume duplicate full-CI attempts.

The maintained adversarial probes under `tools/coordinator/*.test.mjs` use
disposable repositories internally and run through CI. Reviewers invoke these
versioned probes through the harness and do not reconstruct equivalent `/tmp`
experiments. A genuinely novel one-off probe is permitted only when a finding
promotes regression coverage into the Candidate or a scheduled follow-up.
