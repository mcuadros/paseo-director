# M1 walking-skeleton exit audit

## Outcome

M1 exit criteria: **pass**. Task Agent outcome: `needs_review`.

The technical M1 walking skeleton satisfies its implemented acceptance
contracts at the audited base, and the cleanup mismatch found during this
audit has been reconciled:

- The audit initially observed
  `refs/heads/task/dir-m1.9-startup-reconciliation` at exact Candidate
  `44364e74e4d58c6a050088c06cbb2b2e91d685ec` after `dir-m1.9` had closed.
- Sibling blocker `dir-m1.25`, discovered from and blocking `dir-m1.10`,
  independently re-observed that exact ref and proved its ownership and
  Candidate reachability.
- The authorized coordinator deleted only that ref with an exact SHA lease.
  Beads comment `01a080af-de29-7748-aa78-6a050669d8e4`, fresh
  `git ls-remote`, and the GitHub matching-refs API now agree that no
  `task/dir-m1*` remote branch remains. Remote `main` remains the audited base.
- `dir-m1.25` is closed, and every blocking dependency of `dir-m1.10` is
  closed.

The exit conclusion still requires fresh independent approval of the exact
replacement audit Candidate before the coordinator may advance M2. No product
behavior, architecture, PLAN decision, compatibility boundary, external
resource, or accepted risk is changed by this evidence-only Candidate.

## Exact target and authority

- Repository: `mcuadros/paseo-director`, GitHub database ID `1358627520`.
- Refetched base: `origin/main` at
  `9e5dc2782aabbac08868f75fb62d62e92bdaadfd`.
- Base tree: `6b836ed08d827317c70f08105119e7d99c932b9d`.
- M1 entry base:
  `516332cb0fdd072ace7b64e7c9337346e855ba6e`.
- History from the M1 entry base contains 18 serialized first-parent merge
  commits and 40 total commits.
- Normative product authority: approved PLAN v0.4 and accepted ADR-0001
  through ADR-0018, with superseded ADR-0008 retained only as the maximal
  threat analysis governed by ADR-0014.
- Platform: Linux only. No other operating system was tested or claimed.

The project-owner decision recorded on `dir-m1.12` remains binding. Director
Engine is standalone Go software; Director for Paseo owns the React Native UI
and the minimum policy-free connector. Exact Paseo 0.7.2 is admitted with the
already accepted P2 that the connector holds a full daemon-operator
credential. The audit preserves every bound recorded in comments
`01a07a50-19ca-760b-8085-7a823e82def8` and
`01a07a56-856e-734d-84a8-5433b39d364a`:

1. credential storage is outside repositories and Paseo-managed plugin
   checkouts and is connector-process-only;
2. neither credential bytes nor location reach engine argv, environment,
   inherited descriptors, protocol, UI, TaskStore, projections, logs,
   timelines, diagnostics, or support bundles;
3. initial startup and every reload fail closed before readiness, attachment,
   or host mutation when the credential is absent or empty;
4. the connector advertises `credentialScope=full-daemon-operator`, the exact
   contract version/hash, and only the fixed capability set before commands;
5. deployment documentation discloses this authority before installation;
6. the acceptance is exact-0.7.2-only; later authority narrowing changes only
   connector authority and compatibility after new supported evidence.

No new P2 was found or accepted.

## Superseded audit attempt

Candidate `7ecfc8615864468b2bd361f7cee5d64a360b4773`, tree
`644ecbbe525bc77ccb8687dbe3e53d6b3d1c4cc2`, directly parented the same
audited base and consumed one complete passing maintained Linux CI run. It is
superseded because the coordinator then changed the external cleanup fact its
evidence recorded. Beads comment
`01a080b1-6a31-7296-98d5-ba7760119f7f` preserves that supersession. Neither
the Candidate nor its CI result is current validation, review, publication, or
integration evidence for this replacement.

## Method

The audit used current facts rather than Task Agent handoff assertions:

- complete Beads Task acceptance, comments, final review records, closure
  records, and current issue relationships;
- the current source, surrounding tests, generated contracts, committed M0/M1
  evidence, and Git object graph;
- fresh GitHub PR, check-rollup, merge, feedback-count, repository, branch,
  and remote-ref reads;
- current supported Paseo read-only agent/workspace facts for this Task;
- one clean locked dependency install and one complete maintained Linux CI on
  the final audit Candidate; because a commit cannot contain its own SHA, the
  exact post-commit command/result belongs in the assignee-authored Beads
  handoff and private author-validation record.

No past independent review was rebuilt. The audit verified each stored final
review binding, the corresponding Git object graph, exact GitHub head and CI,
and the resulting merge tree proportionally.

## M1 acceptance map

| Area | Independent evidence | Result |
|---|---|---|
| Entry and governance | `dir-m1.1` and `dir-m1.11` are closed; the exact M1 entry SHA is an ancestor of the audited base; PLAN v0.4, AGENTS.md, maintained Linux CI, and coordinator-only delivery rules are present. | Pass |
| Standalone engine and thin host | `engine/go.mod`, `engine/cmd/director-engine`, architecture guards, `connector/`, `ui/`, and the single engine-owned `engine/ports/host/host-interface.v1.json` keep policy in Go and Paseo/UI adaptation in TypeScript. The Go dependency closure admits only the engine and pinned direct-Dolt adapter packages. | Pass |
| Host contract | The closed eight-capability exact-0.7.2 vocabulary, full-daemon-operator descriptor, strict version/hash handshake, generated TypeScript client, and intentional drift tests are present. Host lifecycle invocation remains deliberately unimplemented outside the M1 fake path. | Pass for M1 |
| Six reducers | Exactly one pure, schema-versioned home exists for Eligibility, Launch, Retry, Escalation, Routing, and Closure. Architecture tests reject additional or outward policy homes. | Pass |
| Seven outcome claims | Exactly seven closed schemas exist for `completed`, `needs_validation`, `needs_review`, `needs_human_decision`, `blocked_by_dependency`, `blocked_by_access`, and `budget_exhausted`; unknown extensions fail tests. Claims do not admit Candidates without external observations. | Pass |
| Preparation | The frozen nine-step PreparationPlan carries the 1,200-second whole-plan, 900-second dependency aggregate, 300-second per-command, closed failure codes, and a hash barrier over every required output. Agent intent follows `preparation_ready`. | Pass for M1 skeleton |
| Configuration Preview/Apply | Engine-only strict parsing, deterministic preview identity/impact, optimistic versions, exact human-confirmed Apply, invalid/unapproved isolation, and immutable Run snapshots are implemented and tested. | Pass |
| Direct-Dolt TaskStore | The typed port exposes no SQL/backend selection. Direct Dolt 2.3.2 checks exact listener/database/store/version and safe global/session commit modes before writes; Project/Task/Run/Candidate/Command/Event create, reload, version, idempotency, append-only, referential, cursor, redaction, and reopen contracts are tested with disposable servers. | Pass within ADR-0016 bounds |
| Organizer Create/Adopt | Read-only previews, exact human Apply, local-only Create, clean Adopt, product-repository non-mutation, 14 interruption cut points, exact one-commit recovery, rejection without partial activation, and TaskStore/Git reopen are covered by the Go application and disposable fixtures. | Pass |
| Board/List | The Go reader derives one bounded cursor-stable snapshot from TaskStore facts. The connector makes one strict loopback request without retry or redirects. React Native tests cover loading, initial error, empty, data, cached-refresh error, Board/List, wide lanes, one-lane compact Board, and compact List. | Pass |
| Paseo UI cadence | The exact current `ProjectBoard` blob SHA-256 is `31945847b6ad7d5ddd9e3a50496f1ac65b4954fdc5c5a630f3330c06701202e8`, matching the live exact-0.7.2 measurement input. Normalized output SHA-256 is `4fa123fdaaee678ec434c5cc85b4aefa3f8cfb577da9b5be3e43e3609b294d5c` and records requests 2,006 ms apart for a 2,000 ms interval. | Pass; measurement-artifact P3 remains routed |
| Fake vertical path | A disposable real Git repository plus direct Dolt and policy-free fake host/runtime traverses Organizer facts to Task, eligibility, worktree, registered host view, rootless boundary, approved setup, top-level fake agent, claim, externally observed Candidate, durable state, and ordered cleanup. | Pass |
| Interruption and startup | Restart tests create a new Controller and connector instance before/after every walking-skeleton boundary, lose every first mutation response, recover TaskStore-only transitions, and require one external mutation for each of the eight external effects. Drift, stale cursor, duplicate identity, malformed command/event history, and Candidate dirt park or fail closed without cleanup authority. | Pass |
| One-effect idempotency | Every external effect persists `dispatching` before handoff, observes unknown results before reducer action, adopts exact desired facts, applies class-specific retry, and records exactly one mutation per vertical effect. Start and completed-claim replay are idempotent. | Pass for the M1 effect catalog |
| Security and limits | Exact human lifecycle-digest admission covers all four installed automatic surfaces. Rootless OCI facts require read-only root, dropped capabilities, `NoNewPrivileges`, private network, absent runtime/control sockets, owned worktree only, and fixed stdio MCP. Positive finite disk/worktree/process/memory/time/output/temp limits run at launch and periodic reconciliation; missing or exceeded facts create path-free Needs-you state with `cleanupAuthorized=false`. Connector credential, loopback Board, bounded output, URL, path, remote-helper, redaction, and secret tests remain in CI. | Pass within ADR-0014 trusted-component bounds |
| Contract drift and Linux CI | Contract regeneration, intentional schema mutation, architecture homes, symlink/workflow enforcement, Go build/vet/test, TypeScript typecheck/tests, standalone smoke, and retained M0 safety baselines are the maintained `npm run ci` path on Ubuntu 24.04. No Windows workflow or claim exists. | Pass after exact-Candidate validation recorded in Beads |
| Historical delivery | Every final merged M1 Candidate has a stored independent exact-SHA approval, one successful GitHub Linux check on that head, a merge with base/Candidate parents in order, and a merge tree identical to the Candidate tree. Current GitHub issue comments and PR reviews are zero for PRs #19-#36. | Pass |
| Owned cleanup | No closed M1 local Task branch or worktree remains. The intentional `refs/director/preserved/dir-m1.2` recovery ref is still inside its recorded seven-day retention window. After exact-SHA reconciliation in `dir-m1.25`, fresh `git ls-remote` and GitHub matching-refs report zero `task/dir-m1*` branches while remote `main` remains unchanged. | Pass |

The direct-Dolt scale target remains assigned to `dir-m5.10`; this audit makes
no production-scale claim. Real providers, full Validation/Review/delivery,
and non-fake agent lifecycle remain M3/M4 work and are not silently pulled
into M1.

## Integrated Candidate ledger

Every row below was re-read from Beads, the local Git graph, and GitHub. The
merge parent order is `[base, Candidate]`; the merge tree equals the Candidate
tree in every row. PR #21 and PR #28 contain reviewed correction history, so
their final Candidates descend from rather than directly parent the listed
base. The other 16 final Candidates directly parent the base.

| Task | PR | Base | Final Candidate | Merge | Candidate/merge tree |
|---|---:|---|---|---|---|
| `dir-m1.11` | 19 | `516332cb0fdd072ace7b64e7c9337346e855ba6e` | `f6cf9bc321e1a6ac6d7044b725a57d41fde785f8` | `77615884f2e0f5e177ee43c6f725739690f0b2c0` | `62f57c4566ae072ba24d998d3704e4e02dc20edc` |
| `dir-m1.13` | 20 | `77615884f2e0f5e177ee43c6f725739690f0b2c0` | `55b5a60a9b8eac20e2e1844e4a7537f4ed2a84d2` | `66f8b7127543305db0f560fc8106d2d2c0e5b15f` | `4557e9a43a70940cd2902fe0b113388dc5070c83` |
| `dir-m1.12` | 21 | `66f8b7127543305db0f560fc8106d2d2c0e5b15f` | `79d0093c79694b4adffc4904c1cb5b55c507918a` | `747d4a1a67b384901a61ff553eac8f1ba70d4fb1` | `d20fd2e5eef6286e48e9f8e703e6a06991712ab4` |
| `dir-m1.14` | 22 | `747d4a1a67b384901a61ff553eac8f1ba70d4fb1` | `09499d81ba7db428c6f1357c9c012d6c8c2b2c94` | `58c77afe85fc386f1693dd5e1d1311c44b237e47` | `ebd9bc523f228e788aa175accd0c1f870193cd68` |
| `dir-m1.15` | 23 | `58c77afe85fc386f1693dd5e1d1311c44b237e47` | `92a285c0c9930b36c0f19bac64472ad63e00d648` | `1aa32cc2cb7ff4f084ce9f9597b86ce18a514b8a` | `8b8abef1f648104a126d50ba3f477f6ba03fcf81` |
| `dir-m1.2` | 24 | `1aa32cc2cb7ff4f084ce9f9597b86ce18a514b8a` | `044e8d367f284585838e2e67b14a563dbae8ef1c` | `d2fdb8e8a3b6026dc5c5fb4a2fe27b12e5c7b31c` | `f0aa80daa69fc9f834620f04afcd9997b5b0e528` |
| `dir-m1.16` | 25 | `d2fdb8e8a3b6026dc5c5fb4a2fe27b12e5c7b31c` | `2b892052408bbbb838eb41f247b8e662d8b16d0e` | `b22fb268b4023c77ce7ed61d6765a9a818645187` | `cad84e263b621f1552b67a6fa1abc89b4b6a481c` |
| `dir-m1.3` | 26 | `b22fb268b4023c77ce7ed61d6765a9a818645187` | `6905f11e78bd7e26c81e12af61685901f1c01b40` | `4c3839befc7f49ed1bdf16d6b2b2c506bd6807fe` | `b6981ada36617374a8f34e117bfe250e81c451ef` |
| `dir-m1.17` | 27 | `4c3839befc7f49ed1bdf16d6b2b2c506bd6807fe` | `a5502c4f960807bbdb7949d727600ff52610b957` | `43d26dde24b5bcc0c91d74f93df870b3e6a27375` | `00f5d6946496ba79925f4c1165df681425110a53` |
| `dir-m1.4` | 28 | `43d26dde24b5bcc0c91d74f93df870b3e6a27375` | `b7401da7e993eba849614fa5a3a27316000546f2` | `8da76bce190c6e7d6ea6d9f968bee33b9c224174` | `b993962bfda783bd7b9d1bcbcc69e50ec27cb288` |
| `dir-m1.18` | 29 | `8da76bce190c6e7d6ea6d9f968bee33b9c224174` | `fc14e6d77da8e43ef0dc90c9cd625ca354d6331a` | `c763a14b13283bfb14d77ef801fdbcb63acf0848` | `95b17f157084523e2e0990016c211269ba9cf96c` |
| `dir-m1.5` | 30 | `c763a14b13283bfb14d77ef801fdbcb63acf0848` | `0347777b1726618941598acab7a65e967749f8c8` | `1a383b0914f63be4b1fc295dd928103402f39ed2` | `aa58d2d3954fd13cd93c91c755d1f9ed6bda608c` |
| `dir-m1.20` | 31 | `1a383b0914f63be4b1fc295dd928103402f39ed2` | `21ce62569fb3403623cd5bac618d3f9f7dab3f9c` | `629a4ad5a1f958b629ada2d8caacf85a8ea737c3` | `3f835663582999a3ee3a393ebff6adbea5d14aac` |
| `dir-m1.8` | 32 | `629a4ad5a1f958b629ada2d8caacf85a8ea737c3` | `d2c83ce24dc67d72d167a66ce699b6da511cf00b` | `76f6d28676678bdc5a38c653615b1020499b52f1` | `edce4e059b3cea65a2457e9ebbd9893bd5064a06` |
| `dir-m1.23` | 33 | `76f6d28676678bdc5a38c653615b1020499b52f1` | `f3646199e2d5f782724af78e34645cb64ea86b15` | `ada7a05582161ecffe5179cedc8718490f5163a5` | `3b771ce5dae17c5201fba13ac9462f63fa59769a` |
| `dir-m1.6` | 34 | `ada7a05582161ecffe5179cedc8718490f5163a5` | `7de29c0d935a0058080ca210f147300106d210b2` | `18047c80ee16c5b487298329e11ad026f47a8aef` | `bd98311af5e36c3314ffd848796adb977ec921ee` |
| `dir-m1.7` | 35 | `18047c80ee16c5b487298329e11ad026f47a8aef` | `7a782e0f62223230798ee82dc4405bdffd70ca21` | `d2a4db293316eb2af7713ed134ecd9f452f26f76` | `afa2e31798b7733cf6c1fe327a218d9102f276f5` |
| `dir-m1.9` | 36 | `d2a4db293316eb2af7713ed134ecd9f452f26f76` | `44364e74e4d58c6a050088c06cbb2b2e91d685ec` | `9e5dc2782aabbac08868f75fb62d62e92bdaadfd` | `6b836ed08d827317c70f08105119e7d99c932b9d` |

## Review and remote CI ledger

The independent-review comments below bind the exact final Candidate. Each PR
currently has zero GitHub issue comments and zero GitHub reviews; Director's
required pre-publication review evidence is in Beads. GitHub check rollup
reports one completed successful Linux check on every exact head.

| Task | Review comment | GitHub Actions run | Remote branch now |
|---|---|---:|---|
| `dir-m1.11` | `01a0799f-c073-7c8f-a0a0-b552483f95e4` | `34076543885` | absent |
| `dir-m1.13` | `01a07a00-72d5-7610-8cfa-12a6eb646caf` | `34081445782` | absent |
| `dir-m1.12` | `01a07a7e-0a1f-7b96-aa6d-fc1187d77f2b` | `34090102246` | absent |
| `dir-m1.14` | `01a07c79-edb3-7798-a14d-330499a2a07c` | `34141685493` | absent |
| `dir-m1.15` | `01a07cc8-423d-73f3-b4b3-8df64a9e761e` | `34145600444` | absent |
| `dir-m1.2` | `01a07d21-ea32-7473-988c-b8ea20ca837e` | `34152146935` | absent |
| `dir-m1.16` | `01a07d5b-ae7b-7f3a-8e37-82196b80cf99` | `34156402007` | absent |
| `dir-m1.3` | `01a07d81-e008-71b1-92c2-7347a1fe27c2` | `34158921831` | absent |
| `dir-m1.17` | `01a07da8-14c7-70b5-8fc3-9a73ae64b896` | `34161711133` | absent |
| `dir-m1.4` | `01a07e01-6e91-7db1-a595-8cd14241b532` | `34167294853` | absent |
| `dir-m1.18` | `01a07e34-4199-760b-912e-31dc301db84f` | `34170637292` | absent |
| `dir-m1.5` | `01a07eda-8875-791d-ab74-6581e6ee78f9` | `34180794825` | absent |
| `dir-m1.20` | `01a07f01-e1f9-79ae-a23b-5e64c0f73bb8` | `34188363337` | absent |
| `dir-m1.8` | `01a07f91-39ab-7869-b89b-523b1e409242` | `34192677711` | absent |
| `dir-m1.23` | `01a08003-8ced-763c-ba0d-9d43461849c9` | `34202020851` | absent |
| `dir-m1.6` | `01a0802f-48be-7551-8637-48f1858513a8` | `34206896754` | absent |
| `dir-m1.7` | `01a08055-f7b4-7d71-8e8d-2a7ceca6ea21` | `34210500790` | absent |
| `dir-m1.9` | `01a08083-0180-70a1-a382-f17af1460b16` | `34215065392` | absent after `dir-m1.25` |

The `dir-m1.11` review comment has historical Beads author `mcuadros`, while
its review payload identifies independent top-level Reviewer Agent
`6229e7c5-6ae0-4620-adf8-3a62bff6cc22` in a detached exact-Candidate
checkout. Later review records use explicit Paseo actors. The audit does not
rewrite this historical attribution.

## Routed nonblocking P3 work

Current review findings already have sibling owners and safe fail-closed
behavior. None changes an M1 acceptance result or grants cleanup authority:

- `dir-m1.19`: architecture-guard path/vocabulary/diagnostic blind spots plus
  the absent worktree-config diagnostic from `dir-m1.6`.
- `dir-m1.21`: bounded shared canonicalization, canonicalization wording and
  numeric classification, plus Board worst-case escaped-size and measurement
  artifact provenance from `dir-m1.7`.
- `dir-m1.22`: coordinator pagination, stale-lock reclaim, Candidate/base CI
  budget identity, reclaimed-parent handling, and protected-daemon auth
  hardening from `dir-m1.23`.
- `dir-m1.24`: generated execution vocabulary parity, negative timestamps,
  source/worktree containment, immutable Run base, prior-dispatcher coverage,
  and startup cursor/recovery diagnostic hardening from `dir-m1.9`.

All four records are explicitly marked nonblocking P3 follow-up. This audit
does not claim their acceptance criteria are already complete.

## Current Task lifecycle facts

Supported Paseo readback found:

- Task Agent `0e0fa239-08b0-42d5-b33a-825b2a429696`, status `running`, no
  pending permission, no parent field or parent label;
- active worktree workspace `wks_8a2d7677554baf52` at this exact checkout;
- workspace title `Audit M1 walking-skeleton exit dir-m1.10`;
- the native agent title is
  `Own exactly Beads Task dir-m1.10, visible title \"Run the M1`, not the
  required exact Task title `Run the M1 walking-skeleton exit audit`.

The agent-title mismatch was created outside this checkout. This Task Agent
must not mutate the current daemon, so the authorized coordinator must correct
and re-read it before native review handoff can succeed. The final private
runner remains bound to the exact agent/workspace IDs and must fail closed
until that lifecycle fact is exact.

## Pending gates

1. Exact audit Candidate commit, tree, direct parent/base, locked install,
   maintained Linux CI, author-validation hash, and final cleanup facts are
   recorded in the assignee-authored Beads handoff.
2. The authorized coordinator corrects and verifies the native Task Agent
   title if it is still mismatched, then executes native review handoff with
   its protected Paseo environment.
3. A fresh independent top-level Reviewer evaluates the exact audit Candidate
   in a detached disposable checkout. This Task Agent does not self-review or
   launch that Reviewer.
4. Publication, remote CI, feedback reconciliation, atomic integration,
   synchronization, lifecycle cleanup, and closure of `dir-m1.10` and
   `dir-m1` remain coordinator-only.
5. M2 remains blocked until the exact audit Candidate is independently
   approved and the coordinator verifies every current post-review gate.
