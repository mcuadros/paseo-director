# Coordinator CLI

`director-coordinator` is the repository delivery tool for an authorized
coordinator. It batches the Beads, Git, GitHub, and documented Paseo CLI reads
needed around one exact Task Candidate. It is dependency-free, Linux-only, and
uses direct process argument arrays without a shell. Its state locks use the
Linux `/usr/bin/flock` primitive supplied by util-linux; inability to acquire
that kernel lock fails closed before state is read or changed.

The Task Agent may run only `snapshot` and `review-handoff`. Publication,
integration, and lifecycle cleanup remain coordinator-only.

## Contract

Every command requires an explicit immutable context:

```text
--task --actor --repo --repo-id --remote --base-ref --base
--branch --candidate --head-owner --ownership-file
--checkout --checkout-state --control-repo
--agent-id --workspace-id --lifecycle-state --pr
```

`--base` and `--candidate` are full 40-character object IDs. The branch must
be `task/<task-id>-...` and cannot equal `main`, `master`, or the base ref.
Repository database ID, Git remote, live base, local/remote Task refs, shared
Git common directory, Task record, and any available Paseo agent/workspace are
re-read and matched. `--ownership-file` must be an absolute owner-only regular
file with mode `0600`; raw `--ownership` is refused so the opaque token cannot
enter argv or command logs. Only its SHA-256 is written to the non-secret
manifest and public PR marker. PR repository/base/head metadata, title, body,
and every child argv are rejected when they contain the token or ownership-file
path. Neither the token nor its path belongs in Beads.

Use `--checkout-state present` while the exact owned Task worktree remains.
Use `--checkout-state reclaimed` only when both its path and Git registration
are absent but the exact local Task ref remains at the Candidate. Post-handoff
`publish`, `gate`, `integrate`, `cleanup-plan`, and `cleanup-apply` then operate
from `--control-repo` without recreating a worktree. The checkout's immediate
parent may also be absent: the coordinator canonicalizes the missing suffix
below its nearest existing directory without creating anything, while the
exact local-ref, registration-absence, repository, Candidate, and base checks
remain mandatory. Use
`--lifecycle-state active` or `restored` with two exact Paseo IDs while the
current/recovered resource facts remain observable; a handoff recorded active
may later verify as restored without changing its identity. Use `reclaimed`
with those same recorded IDs for historical facts after the one-shot schedule
reclaims them. Reclaimed lifecycle bindings are reported as recorded inputs
with `verified: false`, never as verified archival. The literal `none` for both
IDs is valid only when the Run never had those resources.

Mutating commands also require an absolute `--state-file` outside every Git
checkout. The file is mode `0600`, atomically replaced and filesystem-synced.
It records immutable bindings and effect phases before dispatch. A private
stable guard file holds a nonblocking Linux kernel lock while the exact
process-identity record is inspected or atomically replaced. This gives one
concurrent stale-lock reclaimer; a stale record is replaced only after its
exact PID/start-time identity is absent, and no second process can interleave a
state write. Preserve the state file and its mode-`0600` `.lock.guard` sibling
through publication, integration, and cleanup.

Coordinator state schema v2 stores `ownershipTokenHash` in its immutable
binding; it never stores the raw ownership value or ownership-file path. A
schema-v1 private state whose sole raw copy is the exact legacy
`binding.ownership` value is atomically migrated under the state lock after the
current mode-`0600` input proves the same ownership. Migration drops any
legacy derived `cleanupPlanHash`; after restart or response loss, a new v2
`cleanup-plan` must be generated and can then be admitted by `cleanup-apply`.
A legacy lock is adopted only by its hash and only after its recorded process
identity is absent. Raw
ownership anywhere else in legacy state refuses with
`STATE_LEGACY_OWNERSHIP_UNSAFE`; preserve all resources and the private file,
restrict access to its owner, and have the authorized coordinator inspect and
repair or replace that control state before retrying. A mismatched ownership
input or binding refuses without disclosing either value.

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
  routing but is never review evidence. Optional `--handoff-file` atomically
  maintains one mode-`0600` copy outside the Task and control checkouts.
- `publish-draft` requires `--state-file`, `--manifest-file`,
  `--validation-file`, `--expected-remote-head`, `--title`, and `--body-file`.
  It refuses blocked Tasks, pushes only the owned branch under an exact lease,
  and creates or updates one marked draft. A corrected Candidate updates that
  same open draft with an exact old-head lease. Its result always reports
  `mergeAuthorized: false`. Owned-PR discovery is limited to one 100-entry
  GitHub page and refuses a saturated page as incomplete before reasoning
  about ownership or creating a PR.
- `remote-ci` requires `--state-file`, `--validation-file`, `--ci-workflow`,
  and one or more `--required-check` values. It records the unique completed
  GitHub workflow run for the exact Candidate/base plus exactly one instance of
  every passing required check. Missing, duplicate, pending, failed, or
  incompletely paginated facts fail closed. The observation ID is the sole
  complete-CI authority for that Candidate.
- `publish` requires `--state-file`, `--review-file`, `--manifest-file`,
  `--review-harness-file`, `--remote-ci-file`, `--validation-file`,
  `--expected-remote-head`, `--title`, and `--body-file`. The
  manifest/harness/CI tuple must bind the same Task/Candidate/base and prove
  that harness version 2 consumed the exact authoritative remote observation.
  It pushes only the owned Task ref with an exact lease, adopts the one owned
  draft, and marks it ready. Closed historical PRs do not alias or block a new
  owned PR; an unmarked open PR still fails closed.
- `gate` requires the exact PR number, review/manifest/harness/remote-CI/
  validation files, and one or more repeated `--required-check` values. It
  requires the live base/head tuple,
  open non-draft mergeability, every observed check/status to pass, each named
  check exactly once, and a complete bounded commit-status page. When that
  page contains no legacy status contexts, the successful required Check Runs
  produce an explicit `checks_only_no_statuses` rollup; otherwise the combined
  status and every exact-Candidate context must be successful. The gate also requires
  no requested reviewers, no current changes-requested or
  ambiguous review, no human issue comment, and no unresolved human review
  thread. It re-reads Beads and refuses if the manifest's binding human-decision
  or prior rejected-review reference set changed. `publish` and the final
  pre-merge gate apply the same durable-context comparison. The Reviewer UUID
  and remote observation ID must match exactly.
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
  changed ref refuses the plan. Cleanup-plan schema v2 binds only
  `ownershipTokenHash`. Schema-v1 plans are not migrated because they are
  derived artifacts. State migration also invalidates any legacy
  `cleanupPlanHash`; after migration or response loss, rerun `cleanup-plan` and
  use that newly emitted v2 document without editing private state.
- `cleanup-apply` requires `--state-file` and `--plan-file`. The plan file may
  be the complete JSON emitted by `cleanup-plan`. Every resource is re-read
  before its effect. Agent/workspace archival is idempotent; destructive
  worktree/ref deletion accepts absence only after a recorded attempt or an
  explicit reclaimed binding. If Paseo workspace archival leaves an exact
  clean owned Git worktree registered, the CLI performs the separately recorded
  non-force Git removal step before touching refs. A
  present worktree after possible handoff is preserved and routed to a
  refusal. A nonterminal `dispatching` or `unknown` local/remote Task-ref
  deletion may retry only after authoritative observation proves the ref still
  equals the exact Candidate, and only through the same explicit expected-OID
  `force-with-lease`/`update-ref` guard. Absence adopts completion; a changed,
  unavailable, or ambiguous ref fails closed. Completion remains terminal on
  later reappearance.

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
node tools/coordinator/cli.mjs publish-draft <immutable context and draft evidence>
node tools/coordinator/cli.mjs remote-ci <immutable context and CI workflow/checks>
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
exact Reviewer Agent ID, its Paseo actor, the authoritative remote-CI JSON, and
`--reason initial`. Only `git` is required on the harness child `PATH`; the
remote-only review checkout needs no dependency installation.

```text
node tools/coordinator/review-harness.mjs run --manifest /private/control/handoff.json --checkout /exact/detached/reviewer --state-file /private/control/review-state.json --remote-ci-file /private/control/remote-ci.json --review-id reviewer-agent-id --actor paseo:reviewer-agent-id --reason initial
```

Harness version 2 consumes the authoritative complete remote Linux CI and runs
the mechanical manifest-identity check concurrently, then sorts their
structured results by check ID. It never starts local or remote CI. The
identity check directly re-reads the Beads acceptance/human-decision/finding
references, detached Candidate and trees, raw diff, clean state, GitHub remote,
and live base SHA. This mechanically proves unchanged immutable history; it
does not turn the manifest into evidence.

Private review-state schema v2 keys the one complete-CI authority to the exact
Candidate/base pair. Manifest hashes are append-only observations inside that
budget, so a lifecycle or ownership rebinding cannot create another CI budget;
the same Reviewer can verify the new manifest while reusing the exact recorded
remote observation. Replay after response loss adopts the manifest-bound
stored result. A different Reviewer, observation, or complete-CI source is
refused. Schema-v1 manifest-keyed state is migrated in place: its original
entries, observed manifest hashes, and recorded invalid-environment or
failure-confirmation exception reasons remain private history. The harness
uses the same kernel-serialized exact process-identity lock, so concurrent
stale reclaimers cannot consume observations or lose a state record.

The maintained adversarial probes under `tools/coordinator/*.test.mjs` use
disposable repositories internally and run through CI. Reviewers invoke these
versioned probes through the harness and do not reconstruct equivalent `/tmp`
experiments. A genuinely novel one-off probe is permitted only when a finding
promotes regression coverage into the Candidate or a scheduled follow-up.
