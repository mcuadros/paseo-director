# M0.2 evidence: Paseo 0.7.2 lifecycle and recovery

- **Captured:** 2026-09-06
- **Beads Task:** `dir-m0.2`
- **Host:** Debian 13, Linux 6.12.107 x86_64
- **Topology:** isolated password-protected loopback daemon, headless, relay/MCP and automatic MCP injection disabled
- **Versions:** Paseo CLI/plugin/client/protocol 0.7.2, Node.js 26.7.0, TypeScript 5.9.3
- **Provider sample:** Codex 0.147.0, `codex/gpt-5.4-mini`, disposable full-access directory
- **Delivery base:** `58dba422562c820b60fd1a214b66577b9dbbb83e` (PLAN v0.3, ADR-0005, ADR-0010)

The Node.js runtime was 26.7.0 only because that was the installed host. The supported implementation bound remains Node.js 22 or newer, as established by ADR-0002. No observation here authorizes Node.js 26-only APIs. ADR-0005 independently admits this exact Codex tuple for session-scoped MCP and tool preapproval; this evidence reuses it only as a lifecycle witness and does not weaken ADR-0008's authority-separation No-go.

The deployed [v0.7 plugin reference](https://paseo.sh/docs/plugins/reference), [SDK reference](https://paseo.sh/docs/sdk/reference), [provider SDK guide](https://paseo.sh/docs/sdk/providers), and [orchestration guide](https://paseo.sh/docs/orchestration) were reread on 2026-09-06. The immutable installed 0.7.2 packages and live public behavior remain controlling when moving documentation describes a later surface.

## Falsifiable question

Can an exact-0.7.2 public plugin and SDK recover deterministically from plugin reload, disable, subprocess crash, and daemon restart without losing or duplicating PLAN v0.3's top-level Task Agents, Reviewer Agents, parent-bound helpers, prompts, or Execution Workspaces, while using subscriptions only as wake-up signals and public list/ref/refresh state as authority?

Success additionally required active-agent archive to close the runtime; stable IDs and Director labels to survive interruption; exact undecorated Task titles; omitted Task/Reviewer parentage; concurrent worktree isolation; non-overlapping replacement; helper parentage, scope, accounting, interruption, reconciliation, and cleanup; workspace archive independence and idempotency; missing capabilities to fail before a side effect; and complete cleanup of the isolated topology.

Failure meant that recovery required a private endpoint, direct database access, UI automation, or log parsing; that identities disappeared or duplicated; that active archive did not close the runtime; that an incompatible host could perform an effect before rejection; or that owned resources could not be cleaned.

## Evidence fixture

The checked-in [fixture](./fixture/) is copied over an official `paseo plugin init` scaffold. It contains:

- a mixed v0.7 entry with one generated-style client surface and one Zod-validated server RPC;
- a server lifecycle marker used only to distinguish process starts, returned cleanup callbacks, and the deliberate crash;
- a unique long-running Node child that writes its exact PID before waiting indefinitely;
- an incompatible entry that checks a missing method before starting the marker;
- one shared 43-method structural preflight used by both public clients before and after connection;
- a public `@getpaseo/client` harness for subscriptions, create/list/ref/refresh, send/wait, and archive;
- a second public-client harness for top-level Task/Reviewer creation, concurrent worktree topology, parent-bound helpers, durable reservation/accounting facts, replacement guards, reconciliation, and cleanup.

The process marker, `/proc`, `ps`, and `ss` are Linux-only test instrumentation used to validate lifecycle claims; they are not Director product state or production dependencies. Product recovery conclusions come from documented CLI or SDK results. Daemon output was used only to debug the fixture and is not an authority.

Final fixture SHA-256 values:

| File | SHA-256 |
|---|---|
| `archive-child.mjs` | `ccaef84a16f42f9b956272de7406eb092e194648872b51037b2f906fc5d7233e` |
| `incompatible-index.ts` | `921857d1114e41bb956473e4ac60e8643aa8c6351a7697e347c445580f05f35e` |
| `index.ts` | `1a8009ed1d00ed3554a7ceb18a5cfb6aa89195e1fc600bb4cb72c782cc10c52c` |
| `lifecycle-client.mjs` | `e69ca2b8926f5eb40f8a31381c16a6d4c5f67dc135fd172b295f06121b5d97bf` |
| `main.client.tsx` | `bf234c037be01c75b5a0fe2329c0d09ee39ebe47a216daf90916b0e3b109c8a0` |
| `paseo-preflight.mjs` | `1e04e533b65b3d3de12f9c8e821beec5f3955364aa5c1bcb6747e2b80e21b43e` |
| `probe.server.ts` | `4cb67ba49003ea7fb28d4d9aedfc571635ff470eddb03949207833607b1f65aa` |
| `probe.shared.ts` | `20405f579f488b1a2adf73d764810ab5d08cafe1d54e23f5075e1fddaa6cbd21` |
| `topology-client.mjs` | `16060894b8e9880b66bfcbce7b613a7e401877238a30c54a55d876a25b4acc86` |

## Authentication and structural compatibility

An unauthenticated documented CLI request failed with `Password required`. The same request and every subsequent operation succeeded only with the ephemeral password. The password is not retained in this repository.

The official scaffold and fixture typechecked against exact `@getpaseo/plugin@0.7.2`. Both harnesses import the same preflight. Before connecting, each real client checks client lifecycle, every SDK root, and effect-free `ref()`-derived Workspace, scoped-agent, Agent, and timeline handles. The same checks repeat after connection and before any create.

The offline negative suite removes each required method in turn. All 43 cases failed with the precise missing path, two baseline `ref()` calls, and no create, archive, send, plugin, marker, or state-write effect:

~~~json
{"harness":"lifecycle","checkedSurfaces":43,"failures":43,"sideEffects":0,"baselineRefCalls":2}
{"harness":"topology","checkedSurfaces":43,"failures":43,"sideEffects":0,"baselineRefCalls":2}
~~~

The checked methods include `connect`/`close`; all five roots; Workspace `current`, `refresh`, `setTitle`, `archive`, `subscribe`, and scoped `agents.create`; Agent `current`, `refresh`, `send`, `run`, `waitForFinish`, `commands`, `archive`, `detach`, and `subscribe`; and timeline `refetch`/`subscribe`.

Seven adapter-policy cases drove the real helper reservation, reconciliation, idempotency, archive, and replacement-gate functions through an injected fake client/effect recorder. Caller-supplied scope was rejected with zero creates/prompts; a reserved helper with zero observed matches parked after one reconciliation with zero creates/prompts; first admission created and prompted once while the repeated adapter call returned the same observed identity with no second create; both closed/unarchived and closed/archived-but-unreconciled replacement paths made zero creates/prompts; and closed/unarchived cleanup invoked archive exactly once. The idempotency result is Director adapter behavior from its durable reservation/observed-ID ledger, not a native Paseo `requestId` or create guarantee.

Installing the incompatible plugin produced public status `failed` with:

~~~text
Missing required public PluginContext method: definitelyMissing
~~~

Its lifecycle marker did not exist, proving that this fixture rejected the missing capability before its first test side effect.

One v0.7-specific constraint was discovered. The mixed entry compiler extracts recognized UI contribution calls such as `plugin.addSurface(...)`; those methods are not dynamically present in the server execution context. Dynamic inspection of `addSurface` therefore falsely failed even though the untouched official scaffold installed as `running`. The corrected fixture kept the direct generated-style `addSurface` call, checked the UI contract at compile time, and checked only `handle` in the server context. It installed as `running` both with the normal web assets mounted and with `--no-web-ui`.

Director must therefore split compatibility checks:

1. exact-package compilation for transformed client contributions;
2. runtime structural checks for the server `PluginContext` and SDK roots actually present in those runtimes;
3. the lifecycle suite for every admitted Paseo host.

## Lifecycle matrix

All IDs below were stable exact values in the raw local run. They are represented by role to avoid publishing opaque local identifiers.

| Interruption | Public observation | Native resource result | Required recovery rule |
|---|---|---|---|
| Plugin reload | `running` to `running`; old marker cleanup, new instance start | Same workspace ID, agent ID, provider session, and labels returned by ref/list/refresh | Reconcile before resuming effects |
| Plugin disable | `running` to `disabled`; cleanup callback ran | Workspace and idle agent remained independently recoverable | Disabled means no engine effects; native resources are not deleted |
| Plugin enable | `disabled` to `running`; new instance started | Same native resources remained | Full startup reconciliation |
| Plugin subprocess exit 23 | Public status became `failed` with `Plugin process exited`; no cleanup marker | Same workspace/agent remained recoverable | Do not expect automatic restart; explicit reload after reconciliation |
| Explicit reload after crash | Public status returned to `running` | Same workspace/agent remained | Treat crash as an interrupted engine attempt |
| Authenticated `paseo daemon stop --home`, then restart | `usedLifecycleRpc=true`; all unarchived no-prompt, active Task/helper, idle Reviewer, and idle replacement rows returned `closed`, null `archivedAt`, and no `activeTurn` | Exact IDs/workspaces/sessions remained; topology capacity stayed 4; safe sends resumed all initialized sessions only after reconciliation | `closed`/null-`archivedAt` is un-terminated, ambiguous, resumable, and capacity-consuming; it authorizes no retry/replacement/cleanup/new capacity |
| Abrupt worker `SIGKILL` with supervisor alive | Supervisor PID stayed stable and auto-started a new worker; plugin reloaded without old cleanup | Idle Reviewer/replacement stayed `idle`; interrupted Task/helper/lifecycle rows returned `running` with no `activeTurn`; every old child/provider/worker PID was absent; safe sends resumed the same sessions after reconciliation | Attribute stale `running` to abrupt worker loss; never infer outcome or retry from status |
| Active agent archive | Public `running` plus non-null `activeTurn`, a durable child start marker, and exact `/proc` ancestry proved one child and two provider processes live beneath the isolated daemon | Final archive returned in 396 ms with all three PIDs gone; they stayed absent after two seconds; no child signal handler ran; repeated archive returned one timestamp; public state was `closed` with non-null `archivedAt`; workspace remained active | Archive is the supported hard close/interrupt operation, not graceful pause or safe-boundary parking |
| Workspace archive | First archive cascaded a non-null agent `archivedAt`, then removed the workspace from active ref/list | Retry kept the workspace timestamp; archived agent remained recoverable; managed worktree directories disappeared, but Task branches and one empty worktree-root directory remained | Record cascaded agent facts; Director separately cleans owned branch/ref/residual paths |

Cleanup callback behavior was path-specific:

| Stop path | Observed callback result |
|---|---|
| Reload, disable, explicit removal | Cleanup marker written |
| Authenticated documented `paseo daemon stop --home` | `usedLifecycleRpc=true`, graceful result, cleanup marker written |
| Explicit `SIGINT` to worker and supervisor | Both exited; cleanup marker written |
| Process-group `SIGINT` | Supervisor, worker, plugin, and group exited; no cleanup marker because the plugin process received the group signal directly |
| Worker `SIGKILL` | Supervisor auto-restarted a new worker/plugin; old plugin wrote no cleanup marker |
| Deliberate plugin exit 23 | Public plugin state became `failed`; no cleanup marker |

The callback remains best effort. Durable correctness cannot depend on it even for paths that invoked it in this run.

## PLAN v0.3 agent topology

The refreshed run created three workspaces from one disposable no-remote repository: two Paseo-managed Task worktrees at the same exact base SHA and one independently prepared detached review checkout at that SHA. Both Task Agents were simultaneously public `running` with non-null active turns. Each Task prompt produced exactly one unique durable child marker, so recovery could not hide a duplicate prompt or agent.

| Role | Exact visible title | Parent | Workspace/lifecycle result |
|---|---|---|---|
| Task Agent alpha | `Implement concurrent topology alpha` | omitted; observed `null` | Unique Task workspace/worktree; ran concurrently with beta; later closed with its helper cascade |
| Task Agent beta | `Validate concurrent topology beta` | omitted; observed `null` | Different unique Task workspace/worktree for the same repository; original reached closed plus non-null `archivedAt` with absent processes before replacement |
| Reviewer Agent | `Review topology candidate` | omitted; observed `null` | Independent workspace over the detached exact-SHA checkout; unique prompt completed; identity survived both restart paths; workspace archive later cascaded its agent timestamp |
| Helper explicit | `Topology explicit helper` | exact alpha Task Agent ID | Requested beta's directory but public placement was forced to alpha's workspace/cwd; explicit archive closed its live child/provider chain without affecting alpha |
| Helper cascade | `Topology cascade helper` | exact alpha Task Agent ID | Requested the review directory but public placement was forced to alpha's workspace/cwd; parent archive cascaded closed state and process termination |

Every top-level creation options object was asserted to have no `parent` property before the public workspace-handle `agents.create` call. Public list/ref/refresh then observed no `paseo.parent-agent-id` label on Task or Reviewer records. Helper calls used the public `parent` option; their returned and recovered records carried exactly `paseo.parent-agent-id=<task-alpha-agent>`.

The helper fixture exercises the public SDK's agent-parent/caller contract without enabling daemon-wide MCP injection. It proves the native lifecycle substrate used by a Task-Agent-scoped helper request: parent attribution, parent-bound placement, cascade, ref/list/refresh recovery, and archive. It does not claim that a general top-level client may originate helper policy, or that Paseo's broad built-in MCP catalog is the admitted production ingress. Director must still expose only a narrowly authorized Task-Agent-scoped helper command and enforce ADR-0008's separate authority boundary.

The helper adapter fixed Project/Workspace/Task/Run/role/intent labels from the owning Run rather than accepting those selectors from helper input. It wrote a per-Run reservation before create and the returned native identity before dependent work. Repeating the adapter call after that identity was observed returned the persisted identity without a second create or prompt. That is Director adapter idempotency, not a native Paseo create guarantee. The real unknown-result path reconciled once and parked with zero creates/prompts when no match existed.

At peak concurrency, public records plus the fixture's durable role ledger yielded:

~~~json
{
  "limits":{"maxActiveTasks":4,"maxActiveTasksPerWorkspace":2,"maxConcurrentAgents":8,"maxSubagentsPerTask":3},
  "globalConsumed":5,
  "organizerCounted":false,
  "byRole":{"task-agent":2,"reviewer":1,"helper":2},
  "helperByRun":{"topology-alpha/run-alpha-1":2,"topology-beta/run-beta-1":0},
  "activeTasksInDirectorWorkspace":2
}
~~~

The Reviewer consumed global capacity but not helper quota. Both helpers consumed the owning Task's quota plus global capacity. Their durable role remained `helper`; the Task Agent IDs remained the Task, Run, and Candidate owners of record. Paseo supplies native identity, parentage, placement, lifecycle, and usage observations, but these Director ownership/quota/budget facts remain TaskStore responsibilities.

The explicit helper's child/provider PIDs `2446961`, `2445295`, and `2445288` were live before archive and absent at the response. Its parent remained `running`. The final cascade helper's PIDs `2643574`, `2631897`, and `2631840` and parent Task Agent PIDs `2642896`, `2608423`, and `2608368` were live before parent archive and absent at the response. None wrote a termination-signal marker. Public refresh returned both parent and cascade helper as `closed` with non-null `archivedAt` and preserved parentage. Explicit helper-first cleanup remains Director's normal rule; the observed parent cascade is an additional safety net.

The beta original archived at `2026-09-06T06:37:09.023Z`; its replacement was created at `2026-09-06T06:37:09.839Z`, 816 ms later. The replacement had a different native ID, the same exact title and workspace, no parent, one completed initial prompt, and was the only unarchived Task Agent for that Task/Run. The real replacement gate also denied closed/unarchived and archived-but-unreconciled fakes with zero creates/prompts.

Reload, disable/enable, and failed-plugin reconciliation preserved all five simultaneously active topology identities, exact titles, parentage, workspace assignments, provider sessions, four unique prompt markers, and peak capacity facts. Authenticated documented stop then recovered alpha, the beta replacement, Reviewer, and cascade helper as `closed`/unarchived and capacity 4. Each initialized session resumed only after reconciliation. After worker `SIGKILL`, interrupted alpha/helper rows were `running` without active turns while idle replacement/Reviewer rows stayed `idle`; capacity remained 4 and every session resumed with the same identity. Reviewer workspace archive later cascaded its agent `archivedAt` 378 ms before the workspace archive completed and reduced capacity to 3.

## Agent creation and recovery edges

The initial no-prompt agent became public `idle` state and exposed a provider session ID. After daemon restart, its first send failed explicitly:

~~~console
$ DIRECTOR_PASEO_URL=ws://127.0.0.1:17693/ws \
  DIRECTOR_PASEO_PASSWORD='<ephemeral-password>' \
  DIRECTOR_LIFECYCLE_STATE=/tmp/director-m0.2/state.json \
  DIRECTOR_INITIAL_PROMPT='Reply with exactly SHOULD_NOT_RUN.' \
  node lifecycle-client.mjs resume
exit 1
~~~

~~~text
Failed to resume Codex thread <session>: no rollout found for thread id <session>
~~~

Before that failed send, the orderly-restart record was `closed` with null `archivedAt`, no `activeTurn`, and its original session. The fixture marked it non-terminated, capacity-bearing, and unauthorized for replacement, duplicate prompt, or cleanup. Only after the explicit non-resumability was observed did archiving it twice yield one non-null timestamp. A replacement created with its initial prompt in the same public create request completed `READY`. Director must include the initial task prompt atomically in agent creation. If the host still fails before the first provider rollout becomes durable, archive the unusable record, reconcile full termination, and only then apply the plan's one-replacement limit.

For the final abrupt-restart case, one lifecycle prompt launched child PID `2533286` under provider PIDs `2531920` and `2531906`; alpha/helper prompts added two independently marked chains under worker PID `2510123`. Worker `SIGKILL` left supervisor PID `2510112` alive and it started worker PID `2570076`. Every old worker/child/provider PID was absent and the old plugin instance wrote no cleanup marker. The lifecycle, alpha, and helper records returned `running` with no `activeTurn`; idle Reviewer and beta replacement records stayed `idle`. Reconciliation preserved every native/provider session ID and capacity. Only then did public sends resume all five initialized sessions and complete on the same identities.

This demonstrates transport recovery, not proof that an interrupted turn had no external effect. Director must compare TaskStore, Git, GitHub, workspace, process, native ID/session, and archive facts before deciding whether a recovery message, archive/replacement, or `Needs you` is safe. Neither `closed`/unarchived nor `running`/no-active-turn releases capacity or authorizes another effect.

## Subscriptions and authority

SDK subscriptions emitted nonzero events during all exercised mutations:

- initial create: one workspace event and three agent events;
- active-restart create: two workspace events and four agent events;
- final exact-fixture active archive: eight agent events.

Counts are timing-dependent and are not used as state. After every interruption, the fixture discarded in-memory events and reconstructed state with persisted IDs/labels plus `list`, `ref`, and `refresh`. Subscriptions are suitable only to wake reconciliation.

## Selected sanitized results

Initial create and recovery:

~~~json
{
  "workspace": {"id":"<workspace-1>","status":"done","archivedAt":null},
  "agent": {
    "id":"<agent-1>",
    "workspaceId":"<workspace-1>",
    "status":"idle",
    "activeTurn":null,
    "labels":{
      "director.project":"lifecycle-probe",
      "director.task":"dir-m0.2",
      "director.run":"run-1"
    }
  },
  "listedWorkspaceIds":["<workspace-1>"]
}
~~~

Normal post-restart projection after authenticated documented stop:

~~~json
{
  "rows":["no-prompt","task-alpha","task-beta-replacement","reviewer","cascade-helper"],
  "status":"closed",
  "archivedAt":null,
  "activeTurn":null,
  "sameNativeAndProviderSessionIds":true,
  "topologyCapacity":{"globalConsumed":4,"taskAgents":2,"reviewers":1,"helpers":1},
  "terminated":false,
  "replacementAuthorized":false,
  "duplicatePromptAuthorized":false,
  "cleanupAuthorized":false
}
~~~

Active archive:

~~~json
{
  "before":{"status":"running","activeTurn":{"turnId":"codex-turn-0"}},
  "processEvidence":{
    "daemonPid":2570076,
    "childPid":2645388,
    "providerPids":[2638215,2638208],
    "aliveBeforeArchive":true,
    "archiveResponseMs":396,
    "postResponseTerminationMs":0,
    "absentAfterArchive":true,
    "stillAbsentAfterDelay":true
  },
  "terminationSignal":null,
  "after":{"status":"closed","archivedAt":"2026-09-06T06:42:47.129Z"},
  "secondArchive":{"ok":true,"archivedAt":"2026-09-06T06:42:47.129Z"},
  "workspaceAfterArchive":{"status":"done","archivedAt":null}
}
~~~

Active daemon-restart recovery:

~~~json
{
  "beforeKill":{"supervisorPid":2510112,"workerPid":2510123,"childPid":2533286,"providerPids":[2531920,2531906]},
  "afterKill":{"newWorkerPid":2570076,"allOldWorkerAndChildPidsAbsent":true,"cleanupCallback":false},
  "afterRestart":{"status":"running","activeTurn":"<absent>"},
  "afterReconciledPublicSend":{"status":"idle","activeTurn":null,"lastMessage":"LIFECYCLE_AFTER_WORKER_KILL"},
  "sameAgentId":true,
  "sameProviderSession":true
}
~~~

## Reproduction

Use an isolated directory and daemon home. Do not point these commands at a user's normal daemon. Replace placeholders locally and never commit the password.

~~~sh
paseo --version
node --version
uname -a
mkdir -p /tmp/director-m0.2/runtime /tmp/director-m0.2/agent-work
git init -b main /tmp/director-m0.2/source-repository
git -C /tmp/director-m0.2/source-repository \
  -c user.name='Director Fixture' -c user.email='fixture@example.invalid' \
  commit --allow-empty -m 'fixture: initial source'
git -C /tmp/director-m0.2/source-repository worktree add --detach \
  /tmp/director-m0.2/review-checkout HEAD

paseo plugin init /tmp/director-m0.2/plugin --id director-lifecycle-probe --json
# Copy index.ts, main.client.tsx, probe.server.ts, probe.shared.ts,
# lifecycle-client.mjs, topology-client.mjs, paseo-preflight.mjs, and
# archive-child.mjs from the checked-in fixture.
cd /tmp/director-m0.2/plugin
npm install --ignore-scripts
npm run typecheck
node --check paseo-preflight.mjs
node --check lifecycle-client.mjs
node --check topology-client.mjs
node --check archive-child.mjs
node lifecycle-client.mjs negative-structural
# Expected: checkedSurfaces=43, failures=43, sideEffects=0, baselineRefCalls=2.
DIRECTOR_TEST_PROVIDER=codex/gpt-5.4-mini \
  node topology-client.mjs topology-negative-structural
DIRECTOR_TEST_PROVIDER=codex/gpt-5.4-mini \
  node topology-client.mjs topology-policy-negatives
# Expected: cases=7, sideEffects=0, deniedPathCreates=0,
# deniedPathPrompts=0, adapterIdempotency creates=1/prompts=1.

paseo plugin init /tmp/director-m0.2/incompatible --id director-lifecycle-incompatible --json
# Copy incompatible-index.ts to incompatible/index.ts, then copy
# probe.server.ts and probe.shared.ts beside it. Install and typecheck it too.
cd /tmp/director-m0.2/incompatible
npm install --ignore-scripts
npm run typecheck

# Run this foreground daemon in a dedicated terminal.
PASEO_PASSWORD='<ephemeral-password>' DIRECTOR_LIFECYCLE_ROOT=/tmp/director-m0.2/runtime paseo daemon start --home /tmp/director-m0.2/home --listen 127.0.0.1:17693 --foreground --no-relay --no-mcp --no-inject-mcp --no-web-ui

# This must fail with "Password required".
paseo plugin ls --host 127.0.0.1:17693 --json

export DIRECTOR_PASEO_URL='ws://127.0.0.1:17693/ws'
export DIRECTOR_PASEO_PASSWORD='<ephemeral-password>'
export DIRECTOR_LIFECYCLE_STATE=/tmp/director-m0.2/state.json
export DIRECTOR_LIFECYCLE_ROOT=/tmp/director-m0.2/runtime
export DIRECTOR_WORKSPACE_PATH=/tmp/director-m0.2/agent-work
export DIRECTOR_TEST_PROVIDER=codex/gpt-5.4-mini
export DIRECTOR_TOPOLOGY_STATE=/tmp/director-m0.2/topology-state.json
export DIRECTOR_SOURCE_REPOSITORY=/tmp/director-m0.2/source-repository
export DIRECTOR_REVIEW_PATH=/tmp/director-m0.2/review-checkout
export DIRECTOR_REVIEW_SHA='<exact-detached-HEAD-SHA>'
export DIRECTOR_DAEMON_PID='<exact-live-daemon-pid-from-ss>'

cd /tmp/director-m0.2/plugin
node lifecycle-client.mjs enable-plugins
PASEO_PASSWORD='<ephemeral-password>' paseo plugin install /tmp/director-m0.2/incompatible --host 127.0.0.1:17693 --json
test ! -e /tmp/director-m0.2/runtime/director-lifecycle-incompatible.events.jsonl
PASEO_PASSWORD='<ephemeral-password>' paseo plugin remove director-lifecycle-incompatible --host 127.0.0.1:17693 --json
PASEO_PASSWORD='<ephemeral-password>' paseo plugin install /tmp/director-m0.2/plugin --host 127.0.0.1:17693 --json
node lifecycle-client.mjs create
node lifecycle-client.mjs recover
node topology-client.mjs topology-create
git -C /tmp/director-m0.2/source-repository worktree list --porcelain

PASEO_PASSWORD='<ephemeral-password>' paseo plugin reload director-lifecycle-probe --host 127.0.0.1:17693 --json
node lifecycle-client.mjs recover
node topology-client.mjs topology-recover
PASEO_PASSWORD='<ephemeral-password>' paseo plugin disable director-lifecycle-probe --host 127.0.0.1:17693 --json
node lifecycle-client.mjs recover
node topology-client.mjs topology-recover
PASEO_PASSWORD='<ephemeral-password>' paseo plugin enable director-lifecycle-probe --host 127.0.0.1:17693 --json
node topology-client.mjs topology-recover

# Deliberate test-only crash; observe "failed", reconcile, then reload.
touch /tmp/director-m0.2/runtime/director-lifecycle-probe.crash
PASEO_PASSWORD='<ephemeral-password>' paseo plugin ls --host 127.0.0.1:17693 --json
node lifecycle-client.mjs recover
node topology-client.mjs topology-recover
PASEO_PASSWORD='<ephemeral-password>' paseo plugin reload director-lifecycle-probe --host 127.0.0.1:17693 --json
node topology-client.mjs topology-recover

# Both helpers have proven live child/provider chains. Exercise normal explicit
# helper cleanup and guarded same-title Task Agent replacement. Keep alpha and
# the cascade helper active for the orderly-stop row.
node topology-client.mjs topology-explicit-helper-cleanup
node topology-client.mjs topology-replace-beta

# Authenticated documented stop. This must report usedLifecycleRpc=true and
# graceful lifecycle_shutdown_rpc; restart the identical foreground command.
PASEO_PASSWORD='<ephemeral-password>' paseo daemon stop \
  --home /tmp/director-m0.2/home --json
PASEO_PASSWORD='<ephemeral-password>' DIRECTOR_LIFECYCLE_ROOT=/tmp/director-m0.2/runtime paseo daemon start --home /tmp/director-m0.2/home --listen 127.0.0.1:17693 --foreground --no-relay --no-mcp --no-inject-mcp --no-web-ui
DIRECTOR_EXPECTED_STATUS=closed \
  node lifecycle-client.mjs assert-unarchived-projection
node topology-client.mjs topology-assert-orderly-restart
# This exact command must exit 1 with "no rollout found".
DIRECTOR_INITIAL_PROMPT='Reply with exactly SHOULD_NOT_RUN.' node lifecycle-client.mjs resume
DIRECTOR_INITIAL_PROMPT='Reply with exactly READY.' node lifecycle-client.mjs replace-started
node topology-client.mjs topology-resume-orderly

# Preserve the READY control as archived evidence, then create a separately
# initialized active child for abrupt worker loss.
node lifecycle-client.mjs archive-current
node lifecycle-client.mjs archive-workspace
export DIRECTOR_RESTART_TOKEN='<new-uuid>'
export DIRECTOR_INITIAL_PROMPT="Run this exact command and wait for it to finish: '/usr/bin/node' '/tmp/director-m0.2/plugin/archive-child.mjs' '/tmp/director-m0.2/runtime/restart-${DIRECTOR_RESTART_TOKEN}.started.json' '${DIRECTOR_RESTART_TOKEN}'"
node lifecycle-client.mjs create-active

ss -ltnp 'sport = :17693'
export DIRECTOR_DAEMON_PID='<exact-live-daemon-pid>'
DIRECTOR_RESTART_LABEL=worker-kill \
  node topology-client.mjs topology-start-restart-pair
sed -n '1,20p' "/tmp/director-m0.2/runtime/restart-${DIRECTOR_RESTART_TOKEN}.started.json"
ps -p '<worker-and-all-three-child/provider-chains>' -o pid=,ppid=,pgid=,stat=,args=

# Kill only the listener/worker. The supervisor must retain its PID and start a
# different worker; every old worker/child/provider PID must be absent.
kill -KILL "${DIRECTOR_DAEMON_PID}"
sleep 3
ss -ltnp 'sport = :17693'
export DIRECTOR_DAEMON_PID='<new-exact-live-daemon-pid>'
ps -p '<old-worker-and-child/provider-pids>' -o pid=,ppid=,pgid=,stat=,args=
DIRECTOR_EXPECTED_STATUS=running \
  node lifecycle-client.mjs assert-unarchived-projection
node topology-client.mjs topology-assert-abrupt-restart
node topology-client.mjs topology-resume-abrupt
DIRECTOR_INITIAL_PROMPT='Reply with exactly LIFECYCLE_AFTER_WORKER_KILL.' \
  node lifecycle-client.mjs resume

# Workspace cascade archives the unarchived Reviewer. Recreate a final live
# parent/helper pair, prove agent-archive hard kill/cascade, then repeat the
# independent active archive and workspace idempotency cases.
node topology-client.mjs topology-workspace-cascade-reviewer
DIRECTOR_RESTART_LABEL=cascade-final \
  node topology-client.mjs topology-start-restart-pair
node topology-client.mjs topology-cascade-parent-cleanup
node lifecycle-client.mjs archive-active
node lifecycle-client.mjs archive-workspace

# Native cleanup removes the two managed worktree directories, not their Task
# branches or the empty worktree-root directory. Prove and remove those exact
# Director-owned residuals explicitly.
node topology-client.mjs topology-cleanup
PASEO_PASSWORD='<ephemeral-password>' paseo ls --host 127.0.0.1:17693 --json
PASEO_PASSWORD='<ephemeral-password>' paseo workspace ls --host 127.0.0.1:17693 --json
git -C /tmp/director-m0.2/source-repository worktree list --porcelain
git -C /tmp/director-m0.2/source-repository branch -a
find /tmp/director-m0.2/home/worktrees -mindepth 1 -maxdepth 2 -print
git -C /tmp/director-m0.2/source-repository branch -D \
  task/topology-alpha task/topology-beta
rmdir '<exact-empty-owned-worktree-root-directory>'

# Record callback differences. First signal the exact worker and supervisor;
# after restarting, signal their exact process group. The first path writes a
# cleanup marker; the group path does not because it signals the plugin too.
kill -INT '<worker-pid>' '<supervisor-pid>'
PASEO_PASSWORD='<ephemeral-password>' DIRECTOR_LIFECYCLE_ROOT=/tmp/director-m0.2/runtime paseo daemon start --home /tmp/director-m0.2/home --listen 127.0.0.1:17693 --foreground --no-relay --no-mcp --no-inject-mcp --no-web-ui
ps -p '<worker-pid>' -o pid=,ppid=,pgid=,stat=,args=
kill -INT -- '-<exact-daemon-process-group-id>'

# Restart once for public plugin removal, then use documented authenticated
# stop. Verify public lists, every recorded PID, and the listener are empty.
PASEO_PASSWORD='<ephemeral-password>' DIRECTOR_LIFECYCLE_ROOT=/tmp/director-m0.2/runtime paseo daemon start --home /tmp/director-m0.2/home --listen 127.0.0.1:17693 --foreground --no-relay --no-mcp --no-inject-mcp --no-web-ui
PASEO_PASSWORD='<ephemeral-password>' paseo plugin remove \
  director-lifecycle-probe --host 127.0.0.1:17693 --json
PASEO_PASSWORD='<ephemeral-password>' paseo plugin ls --host 127.0.0.1:17693 --json
PASEO_PASSWORD='<ephemeral-password>' paseo daemon stop \
  --home /tmp/director-m0.2/home --json
ss -ltnp 'sport = :17693'
ps -p '<comma-separated-recorded-owned-pids>' -o pid=,ppid=,pgid=,stat=,args=
git -C /tmp/director-m0.2/source-repository worktree remove \
  /tmp/director-m0.2/review-checkout
rm -rf /tmp/director-m0.2
test ! -e /tmp/director-m0.2
~~~

## Cleanup evidence

The final clean reportable run used port 17693. Nine owned agents and five owned workspaces covered the preserved lifecycle controls plus the expanded restart topology. Cleanup explicitly handled helpers first and archived every remaining unarchived record. The live matrix separately proved that a closed/unarchived fake invokes archive once and that Reviewer workspace archive cascades an agent timestamp. Workspace retries retained stable workspace timestamps. The public global agent and workspace lists then each returned `[]`.

Plugin listing contained only the owned running probe. Plugin removal returned `disabled`, and the next plugin list returned `[]`:

~~~console
$ PASEO_PASSWORD='<ephemeral-password>' paseo ls --host 127.0.0.1:17693 --json
[]
$ PASEO_PASSWORD='<ephemeral-password>' paseo workspace ls --host 127.0.0.1:17693 --json
[]
$ PASEO_PASSWORD='<ephemeral-password>' paseo plugin ls --host 127.0.0.1:17693 --json
[{"id":"director-lifecycle-probe","path":"<owned-plugin>","enabled":true,"status":"running"}]
$ PASEO_PASSWORD='<ephemeral-password>' paseo plugin remove director-lifecycle-probe --host 127.0.0.1:17693 --json
{"id":"director-lifecycle-probe","path":"<owned-plugin>","enabled":false,"status":"disabled"}
$ PASEO_PASSWORD='<ephemeral-password>' paseo plugin ls --host 127.0.0.1:17693 --json
[]
~~~

After workspace archive, `git worktree list --porcelain` contained only the disposable source repository and detached review checkout: Paseo removed both managed Task worktree directories. `git branch -a` still listed `task/topology-alpha` and `task/topology-beta`, and the empty owned directory `home/worktrees/2umivkad` remained. Director deleted both exact branches and removed that exact empty directory, then proved only `main` and no worktree residual remained. This corrects the earlier assumption that Paseo workspace archive owns branch/ref cleanup.

After the explicit worker-plus-supervisor and process-group signal rows, the daemon was restarted only to remove the plugin. The final authenticated documented stop reported `usedLifecycleRpc=true`, `reason=lifecycle_shutdown_rpc`, and `forced=false`. The exact PID probe then returned no rows for every recorded Task/helper/lifecycle child, provider, plugin, worker, or supervisor PID; port 17693 had no listener.

~~~console
$ PASEO_PASSWORD='<ephemeral-password>' paseo daemon stop --home <owned-home> --json
{"action":"stopped","forced":false,"usedLifecycleRpc":true,"reason":"lifecycle_shutdown_rpc"}
$ ss -ltnp 'sport = :17693'
State Recv-Q Send-Q Local Address:Port Peer Address:Port Process
$ ps -p '<all-recorded-owned-pids>' -o pid=,ppid=,pgid=,stat=,args=
exit 1; no rows
$ git -C <source-repository> worktree remove <detached-review-checkout>
$ rm -rf <owned-1.5-GiB-experiment-root>
$ test ! -e <owned-experiment-root>
exit 0
~~~

Both plugin fixtures, every agent/workspace/worktree/Task branch/residual directory/process/listener, the detached review checkout, source repository, daemon home, and 1.5 GiB experiment root were removed. No public Beads/Dolt ref or primary workspace was touched.

## Independent-review corrections preserved

- **Structural preflight findings:** resolved with one imported 43-method preflight invoked before and after connection by both lifecycle and topology clients. Both offline harness rows removed every method in turn and reported 43 precise failures, two effect-free refs, and zero effects.
- **Active archive P1:** preserved with the durable unique child marker, exact child/provider/daemon ancestry, public active-turn observation, 396 ms final archive result, exact-PID absence at response and after delay, idempotent retry/reconciliation, and independently usable workspace. New signal assertions prove archive hard-killed the child tree without a graceful handler.
- **Reproduction P2:** resolved by the exact no-prompt failure command/output and the explicit active-list, plugin-removal, PID, listener, daemon-stop, owned-root deletion, and absence commands/results above.
- **PLAN v0.3 / ADR-0010 P1:** resolved by the successful clean topology run: omitted top-level parentage, exact titles, concurrent Task worktrees, detached Reviewer, non-overlapping replacement, helper parentage/scope/accounting/interruption/reconciliation/cleanup, and zero final resources.
- **Advanced base P1:** resolved by preserving the two prior commits and rebasing them onto exact `58dba422562c820b60fd1a214b66577b9dbbb83e` before the refreshed run. ADR-0005's provider/MCP result is cross-referenced without making a new provider admission or helper security claim.
- **Closed/unarchived P1:** resolved by live authenticated documented-stop projections for no-prompt, Task, Reviewer, replacement, helper, and active-child rows; explicit non-termination assertions; capacity 4; same-session resume only after reconciliation; closed-plus-archived-plus-external-facts replacement; and explicit archive of a closed/unarchived fake.
- **Policy-negative P2:** resolved by driving the real helper reservation/unknown/idempotency functions and replacement gate with injected clients/recorders. Denied paths made zero creates/prompts; adapter idempotency made exactly one create/prompt across two calls.
- **Workspace cleanup P2:** corrected and proven: Paseo removed managed worktree directories but left both Task branches and one empty worktree-root directory; Director removed those exact owned residuals.
- **Stop/signal P3:** reproduction now uses authenticated `paseo daemon stop --home` and exact process signalling. Documented stop and explicit worker-plus-supervisor `SIGINT` wrote cleanup; worker `SIGKILL` and process-group `SIGINT` did not.
- **Archive-signal P3:** all archive assertions found no `SIGINT`/`SIGTERM`/`SIGHUP` termination marker. Archive is documented as hard termination, not graceful pause or safe-boundary parking.

## Result

The evidence supports **Go with mandatory recovery constraints**:

- persist Task/Run intent before creation and persist returned native IDs/labels immediately;
- include the initial prompt atomically in create;
- use subscriptions only to schedule list/ref/refresh reconciliation;
- treat plugin crash as failed until an explicit reload after reconciliation;
- treat cleanup callbacks as path-specific best effort and never a correctness boundary;
- treat every null-`archivedAt` record as un-terminated and capacity-consuming, including `closed`/no-active-turn after orderly restart;
- attribute `running`/no-active-turn to abrupt worker loss in the tested topology, without inferring completion or retry permission;
- reconcile durable external effects before any recovery send or retry;
- archive and apply at most one replacement when a provider record cannot resume, only after `closed` plus non-null `archivedAt` and external termination facts reconcile;
- create Task Agents and Reviewers as top-level agents with omitted parents; use exact Task titles and separate Task worktrees;
- reserve and reconcile helper identity, fixed scope, capacity, quota, and budget before helper work; never promote a helper into Task/Run/Candidate ownership;
- require the previous top-level Task Agent to satisfy the full termination predicate before one same-title replacement;
- enter `Needs you` whenever the interrupted effect remains ambiguous;
- archive agents before workspaces and retain their durable audit references;
- treat agent archive as hard termination, never graceful safe-boundary parking;
- clean owned Task branches/refs and empty residual directories explicitly after native workspace archive.

These constraints align ADR-0003 with PLAN v0.3 and ADR-0010. They do not authorize M1 while another M0 stop condition remains, and they do not weaken ADR-0008: labels, parentage, worktrees, provider settings, and MCP policy are not an OS or credential boundary.
