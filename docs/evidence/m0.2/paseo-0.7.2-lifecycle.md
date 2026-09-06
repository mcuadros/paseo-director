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
- a public `@getpaseo/client` harness for structural checks, subscriptions, create/list/ref/refresh, send/wait, and archive.
- a second public-client harness for top-level Task/Reviewer creation, concurrent worktree topology, parent-bound helpers, durable reservation/accounting facts, replacement guards, reconciliation, and cleanup.

The process marker, `/proc`, `ps`, and `ss` are Linux-only test instrumentation used to validate lifecycle claims; they are not Director product state or production dependencies. Product recovery conclusions come from documented CLI or SDK results. Daemon output was used only to debug the fixture and is not an authority.

Final fixture SHA-256 values:

| File | SHA-256 |
|---|---|
| `archive-child.mjs` | `ccaef84a16f42f9b956272de7406eb092e194648872b51037b2f906fc5d7233e` |
| `incompatible-index.ts` | `921857d1114e41bb956473e4ac60e8643aa8c6351a7697e347c445580f05f35e` |
| `index.ts` | `1a8009ed1d00ed3554a7ceb18a5cfb6aa89195e1fc600bb4cb72c782cc10c52c` |
| `lifecycle-client.mjs` | `7f43ba54c04c4c79df112559e8b71f9f7cb9a48189cab3cac1c6b040e943a085` |
| `main.client.tsx` | `bf234c037be01c75b5a0fe2329c0d09ee39ebe47a216daf90916b0e3b109c8a0` |
| `probe.server.ts` | `4cb67ba49003ea7fb28d4d9aedfc571635ff470eddb03949207833607b1f65aa` |
| `probe.shared.ts` | `20405f579f488b1a2adf73d764810ab5d08cafe1d54e23f5075e1fddaa6cbd21` |
| `topology-client.mjs` | `517d0d2e6417cc4bd896374f127cde13b6f177fb35ae609e5195ccc2b69b5b59` |

## Authentication and structural compatibility

An unauthenticated documented CLI request failed with `Password required`. The same request and every subsequent operation succeeded only with the ephemeral password. The password is not retained in this repository.

The official scaffold and fixture typechecked against exact `@getpaseo/plugin@0.7.2`. Before connecting, the real client now checks client lifecycle, every SDK root, and effect-free `ref()`-derived Workspace, scoped-agent, Agent, and timeline handles. The same checks repeat after connection and before any create.

The offline negative suite removes each required method in turn. All 43 cases failed with the precise missing path, two baseline `ref()` calls, and no create, archive, send, plugin, marker, or state-write effect:

~~~json
{"checkedSurfaces":43,"failures":43,"sideEffects":0,"baselineRefCalls":2}
~~~

The checked methods include `connect`/`close`; all five roots; Workspace `current`, `refresh`, `setTitle`, `archive`, `subscribe`, and scoped `agents.create`; Agent `current`, `refresh`, `send`, `run`, `waitForFinish`, `commands`, `archive`, `detach`, and `subscribe`; and timeline `refetch`/`subscribe`.

Four additional effect-free policy negatives passed: top-level creation omitted `parent`; caller-supplied helper Task/Run/role overrides could not alter the frozen adapter labels; an unknown helper creation result parked without calling create; and an active prior Task Agent rejected replacement before create. Total reported side effects remained zero.

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
| Daemon restart, idle initialized agent | Enabled plugin automatically returned to `running`; native records reloaded | Same IDs, labels, and provider session; a later send completed on the same session | Reconnect and reconcile from durable references |
| Daemon restart, active turn | A unique child and two provider PIDs were live before shutdown; after restart the agent/workspace returned as `running`, but `activeTurn` was absent and did not self-resolve after five seconds | All old PIDs were absent; a new public `send` resumed the same provider session and completed without a duplicate agent | Previous turn outcome is ambiguous: reconcile durable effects first; never blindly retry; enter `Needs you` if ambiguity remains |
| Active agent archive | Public `running` plus non-null `activeTurn`, a durable child start marker, and exact `/proc` ancestry proved one child and two provider processes live beneath the isolated daemon | Refreshed exact-fixture archive returned in 450 ms with all three PIDs gone; they stayed absent after two seconds, repeated archive returned the identical timestamp, public state was `closed`, and the workspace remained active | Archive is the supported close/interrupt operation |
| Workspace archive | First archive returned no error; ref/active-list no longer returned the workspace | Second archive succeeded with the identical timestamp; archived closed agent remained recoverable by ref and label-filtered include-archived list | Archive agents first, then workspace; keep durable audit references |

The returned plugin cleanup callback ran on reload, disable, and explicit plugin removal. It did not write its marker during any of three graceful daemon shutdown/restart cycles. A daemon stop must therefore be treated like abrupt engine loss: cleanup callbacks are best effort, not a correctness boundary.

## PLAN v0.3 agent topology

The refreshed run created three workspaces from one disposable no-remote repository: two Paseo-managed Task worktrees at the same exact base SHA and one independently prepared detached review checkout at that SHA. Both Task Agents were simultaneously public `running` with non-null active turns. Each Task prompt produced exactly one unique durable child marker, so recovery could not hide a duplicate prompt or agent.

| Role | Exact visible title | Parent | Workspace/lifecycle result |
|---|---|---|---|
| Task Agent alpha | `Implement concurrent topology alpha` | omitted; observed `null` | Unique Task workspace/worktree; ran concurrently with beta; later closed with its helper cascade |
| Task Agent beta | `Validate concurrent topology beta` | omitted; observed `null` | Different unique Task workspace/worktree for the same repository; original closed before replacement |
| Reviewer Agent | `Review topology candidate` | omitted; observed `null` | Independent workspace over the detached exact-SHA checkout; unique prompt completed; identity survived restart; explicitly archived |
| Helper explicit | `Topology explicit helper` | exact alpha Task Agent ID | Requested beta's directory but public placement was forced to alpha's workspace/cwd; explicit archive closed its live child/provider chain without affecting alpha |
| Helper cascade | `Topology cascade helper` | exact alpha Task Agent ID | Requested the review directory but public placement was forced to alpha's workspace/cwd; parent archive cascaded closed state and process termination |

Every top-level creation options object was asserted to have no `parent` property before the public workspace-handle `agents.create` call. Public list/ref/refresh then observed no `paseo.parent-agent-id` label on Task or Reviewer records. Helper calls used the public `parent` option; their returned and recovered records carried exactly `paseo.parent-agent-id=<task-alpha-agent>`.

The helper fixture exercises the public SDK's agent-parent/caller contract without enabling daemon-wide MCP injection. It proves the native lifecycle substrate used by a Task-Agent-scoped helper request: parent attribution, parent-bound placement, cascade, ref/list/refresh recovery, and archive. It does not claim that a general top-level client may originate helper policy, or that Paseo's broad built-in MCP catalog is the admitted production ingress. Director must still expose only a narrowly authorized Task-Agent-scoped helper command and enforce ADR-0008's separate authority boundary.

The helper adapter fixed Project/Workspace/Task/Run/role/intent labels from the owning Run rather than accepting those selectors from helper input. It wrote a per-Run reservation before create and the returned native identity before dependent work. Repeating the same observed helper intent returned the same identity without a second create. The unknown-result negative parked with zero create effects.

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

The explicit helper's child/provider PIDs `3928339`, `3924943`, and `3924932` were live before archive and absent at the response. Its parent remained `running`. The cascade helper's PIDs `3929582`, `3928501`, and `3928494` and the parent Task Agent's PIDs `3921051`, `3919834`, and `3919827` were live before parent archive and absent at the response. Public refresh returned both parent and cascade helper as `closed` with preserved parentage. Explicit helper-first cleanup remains Director's normal rule; the observed parent cascade is an additional safety net.

The beta original archived at `2026-09-06T05:17:43.377Z`; its replacement was created at `2026-09-06T05:17:44.181Z`, 804 ms later. The replacement had a different native ID, the same exact title and workspace, no parent, one completed initial prompt, and was the only active Task Agent for that Task/Run. The offline active-prior negative also proved zero create effects.

Reload, disable/enable, and failed-plugin reconciliation preserved all five simultaneously active topology identities, exact titles, parentage, workspace assignments, provider sessions, four unique prompt markers, and peak capacity facts. Same-home daemon restart later recovered the replacement and Reviewer plus the closed Task/helper records without duplication.

## Agent creation and recovery edges

The initial no-prompt agent became public `idle` state and exposed a provider session ID. After daemon restart, its first send failed explicitly:

~~~console
$ DIRECTOR_PASEO_URL=ws://127.0.0.1:17688/ws \
  DIRECTOR_PASEO_PASSWORD='<ephemeral-password>' \
  DIRECTOR_LIFECYCLE_STATE=/tmp/director-m0.2/state.json \
  DIRECTOR_INITIAL_PROMPT='Reply with exactly SHOULD_NOT_RUN.' \
  node lifecycle-client.mjs resume
exit 1
~~~

~~~text
Failed to resume Codex thread <session>: no rollout found for thread id <session>
~~~

Archiving that record twice was idempotent. A replacement created with its initial prompt in the same public create request completed `READY`, survived another daemon restart, and later resumed on the same provider session. Director must include the initial task prompt atomically in agent creation. If the host still fails before the first provider rollout becomes durable, the explicit resume error is recoverable only by archiving the unusable record and applying the plan's one-replacement limit after label/ID reconciliation.

For the refreshed active-restart case, the first prompt launched the fixture-owned long-running child. Its durable marker identified child PID `4040598`; `/proc` ancestry identified provider PIDs `4033014` and `4032977` under daemon PID `3950751`. All four were absent after daemon shutdown. After restart, list/ref/refresh preserved the record as `running` but omitted `activeTurn` for more than five seconds. Sending `Reply with exactly AFTER_RESTART.` through the same handle resumed the same agent and provider-session IDs and ended in `idle` with `AFTER_RESTART`.

This demonstrates transport recovery, not proof that the interrupted turn had no external effect. Director must compare TaskStore, Git, GitHub, and workspace facts before deciding whether a recovery message, archive/replacement, or `Needs you` is safe.

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

Active archive:

~~~json
{
  "before":{"status":"running","activeTurn":{"turnId":"codex-turn-0"}},
  "processEvidence":{
    "daemonPid":3950751,
    "childPid":4008549,
    "providerPids":[3982929,3982871],
    "aliveBeforeArchive":true,
    "archiveResponseMs":450,
    "postResponseTerminationMs":0,
    "absentAfterArchive":true,
    "stillAbsentAfterDelay":true
  },
  "after":{"status":"closed","archivedAt":"2026-09-06T05:19:03.764Z"},
  "secondArchive":{"ok":true,"archivedAt":"2026-09-06T05:19:03.764Z"},
  "workspaceAfterArchive":{"status":"done","archivedAt":null}
}
~~~

Active daemon-restart recovery:

~~~json
{
  "beforeStop":{"daemonPid":3950751,"childPid":4040598,"providerPids":[4033014,4032977]},
  "afterStop":{"allRecordedPidsAbsent":true},
  "afterRestart":{"status":"running","activeTurn":"<absent>"},
  "afterPublicSend":{"status":"idle","activeTurn":null,"lastMessage":"AFTER_RESTART"},
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
# lifecycle-client.mjs, topology-client.mjs, and archive-child.mjs from the
# checked-in fixture.
cd /tmp/director-m0.2/plugin
npm install --ignore-scripts
npm run typecheck
node --check lifecycle-client.mjs
node --check topology-client.mjs
node --check archive-child.mjs
node lifecycle-client.mjs negative-structural
# Expected: checkedSurfaces=43, failures=43, sideEffects=0, baselineRefCalls=2.
DIRECTOR_TEST_PROVIDER=codex/gpt-5.4-mini \
  node topology-client.mjs topology-policy-negatives
# Expected: cases=4, sideEffects=0.

paseo plugin init /tmp/director-m0.2/incompatible --id director-lifecycle-incompatible --json
# Copy incompatible-index.ts to incompatible/index.ts, then copy
# probe.server.ts and probe.shared.ts beside it. Install and typecheck it too.
cd /tmp/director-m0.2/incompatible
npm install --ignore-scripts
npm run typecheck

# Run this foreground daemon in a dedicated terminal.
PASEO_PASSWORD='<ephemeral-password>' DIRECTOR_LIFECYCLE_ROOT=/tmp/director-m0.2/runtime paseo daemon start --home /tmp/director-m0.2/home --listen 127.0.0.1:17682 --foreground --no-relay --no-mcp --no-inject-mcp --no-web-ui

# This must fail with "Password required".
paseo plugin ls --host 127.0.0.1:17682 --json

export DIRECTOR_PASEO_URL='ws://127.0.0.1:17682/ws'
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
PASEO_PASSWORD='<ephemeral-password>' paseo plugin install /tmp/director-m0.2/incompatible --host 127.0.0.1:17682 --json
test ! -e /tmp/director-m0.2/runtime/director-lifecycle-incompatible.events.jsonl
PASEO_PASSWORD='<ephemeral-password>' paseo plugin remove director-lifecycle-incompatible --host 127.0.0.1:17682 --json
PASEO_PASSWORD='<ephemeral-password>' paseo plugin install /tmp/director-m0.2/plugin --host 127.0.0.1:17682 --json
node lifecycle-client.mjs create
node lifecycle-client.mjs recover
node topology-client.mjs topology-create
git -C /tmp/director-m0.2/source-repository worktree list --porcelain

PASEO_PASSWORD='<ephemeral-password>' paseo plugin reload director-lifecycle-probe --host 127.0.0.1:17682 --json
node lifecycle-client.mjs recover
node topology-client.mjs topology-recover
PASEO_PASSWORD='<ephemeral-password>' paseo plugin disable director-lifecycle-probe --host 127.0.0.1:17682 --json
node lifecycle-client.mjs recover
node topology-client.mjs topology-recover
PASEO_PASSWORD='<ephemeral-password>' paseo plugin enable director-lifecycle-probe --host 127.0.0.1:17682 --json
node topology-client.mjs topology-recover

# Deliberate test-only crash; observe "failed", reconcile, then reload.
touch /tmp/director-m0.2/runtime/director-lifecycle-probe.crash
PASEO_PASSWORD='<ephemeral-password>' paseo plugin ls --host 127.0.0.1:17682 --json
node lifecycle-client.mjs recover
node topology-client.mjs topology-recover
PASEO_PASSWORD='<ephemeral-password>' paseo plugin reload director-lifecycle-probe --host 127.0.0.1:17682 --json
node topology-client.mjs topology-recover

# Both helpers have proven live child/provider chains. Exercise normal explicit
# helper cleanup, the parent-cascade safety net, then guarded same-title Task
# Agent replacement.
node topology-client.mjs topology-explicit-helper-cleanup
node topology-client.mjs topology-cascade-parent-cleanup
node topology-client.mjs topology-replace-beta

# Stop the foreground daemon with SIGINT, then rerun this in its dedicated terminal.
PASEO_PASSWORD='<ephemeral-password>' DIRECTOR_LIFECYCLE_ROOT=/tmp/director-m0.2/runtime paseo daemon start --home /tmp/director-m0.2/home --listen 127.0.0.1:17682 --foreground --no-relay --no-mcp --no-inject-mcp --no-web-ui
node lifecycle-client.mjs recover
node topology-client.mjs topology-recover
# This exact command must exit 1 with "no rollout found".
DIRECTOR_INITIAL_PROMPT='Reply with exactly SHOULD_NOT_RUN.' node lifecycle-client.mjs resume
DIRECTOR_INITIAL_PROMPT='Reply with exactly READY.' node lifecycle-client.mjs replace-started

# Stop and restart the identical foreground daemon command again. Read the new
# worker PID from this exact listener probe.
PASEO_PASSWORD='<ephemeral-password>' DIRECTOR_LIFECYCLE_ROOT=/tmp/director-m0.2/runtime paseo daemon start --home /tmp/director-m0.2/home --listen 127.0.0.1:17682 --foreground --no-relay --no-mcp --no-inject-mcp --no-web-ui
ss -ltnp 'sport = :17682'
export DIRECTOR_DAEMON_PID='<exact-live-daemon-pid>'
node lifecycle-client.mjs archive-active
# Substitute the PIDs printed by archive-active. Only the daemon row may remain.
export DIRECTOR_CHILD_PID='<child-pid>'
export DIRECTOR_PROVIDER_PIDS='<provider-pid-1>,<provider-pid-2>'
ps -p "${DIRECTOR_CHILD_PID},${DIRECTOR_PROVIDER_PIDS},${DIRECTOR_DAEMON_PID}" -o pid=,ppid=,stat=,args=
node lifecycle-client.mjs archive-workspace

# Create another atomically prompted long-running child, then record its marker
# and exact process ancestry before stopping the daemon.
export DIRECTOR_RESTART_TOKEN='<new-uuid>'
export DIRECTOR_INITIAL_PROMPT="Run this exact command and wait for it to finish: '/usr/bin/node' '/tmp/director-m0.2/plugin/archive-child.mjs' '/tmp/director-m0.2/runtime/restart-${DIRECTOR_RESTART_TOKEN}.started.json' '${DIRECTOR_RESTART_TOKEN}'"
node lifecycle-client.mjs create-active
sed -n '1,20p' "/tmp/director-m0.2/runtime/restart-${DIRECTOR_RESTART_TOKEN}.started.json"
export DIRECTOR_CHILD_PID='<child-pid-from-marker>'
export DIRECTOR_PROVIDER_PIDS='<provider-pid-1>,<provider-pid-2>'
ps -p "${DIRECTOR_CHILD_PID},${DIRECTOR_PROVIDER_PIDS},${DIRECTOR_DAEMON_PID}" -o pid=,ppid=,stat=,args=

# Stop the daemon with SIGINT. The exact child/provider/daemon PID probe must
# return no rows. Restart the identical daemon command/home.
ps -p "${DIRECTOR_CHILD_PID},${DIRECTOR_PROVIDER_PIDS},${DIRECTOR_DAEMON_PID}" -o pid=,ppid=,stat=,args=
PASEO_PASSWORD='<ephemeral-password>' DIRECTOR_LIFECYCLE_ROOT=/tmp/director-m0.2/runtime paseo daemon start --home /tmp/director-m0.2/home --listen 127.0.0.1:17682 --foreground --no-relay --no-mcp --no-inject-mcp --no-web-ui
node lifecycle-client.mjs recover
node topology-client.mjs topology-recover
sleep 5
node lifecycle-client.mjs recover
DIRECTOR_INITIAL_PROMPT='Reply with exactly AFTER_RESTART.' node lifecycle-client.mjs resume
node lifecycle-client.mjs archive-current
node lifecycle-client.mjs archive-workspace

# Exact final public and process cleanup probes.
node topology-client.mjs topology-cleanup
PASEO_PASSWORD='<ephemeral-password>' paseo ls --host 127.0.0.1:17682 --json
PASEO_PASSWORD='<ephemeral-password>' paseo workspace ls --host 127.0.0.1:17682 --json
PASEO_PASSWORD='<ephemeral-password>' paseo plugin ls --host 127.0.0.1:17682 --json
PASEO_PASSWORD='<ephemeral-password>' paseo plugin remove director-lifecycle-probe --host 127.0.0.1:17682 --json
PASEO_PASSWORD='<ephemeral-password>' paseo plugin ls --host 127.0.0.1:17682 --json
ss -ltnp 'sport = :17682'
# Stop the foreground daemon with SIGINT.
ss -ltnp 'sport = :17682'
ps -p '<comma-separated-recorded-owned-pids>' -o pid=,ppid=,stat=,args=
git -C /tmp/director-m0.2/source-repository worktree list --porcelain
git -C /tmp/director-m0.2/source-repository worktree remove \
  /tmp/director-m0.2/review-checkout
rm -rf /tmp/director-m0.2
test ! -e /tmp/director-m0.2
~~~

## Cleanup evidence

The refreshed clean reportable run used port 17688. Nine owned agents and five owned workspaces covered the preserved lifecycle controls plus the expanded topology. The topology cleanup explicitly handled helpers first, archived the remaining Task Agent and Reviewer, retried each workspace archive with one stable timestamp, and returned empty owned active-ID arrays. The public global agent and workspace lists then each returned `[]`.

Plugin listing contained only the owned running probe. Plugin removal returned `disabled`, and the next plugin list returned `[]`:

~~~console
$ PASEO_PASSWORD='<ephemeral-password>' paseo ls --host 127.0.0.1:17688 --json
[]
$ PASEO_PASSWORD='<ephemeral-password>' paseo workspace ls --host 127.0.0.1:17688 --json
[]
$ PASEO_PASSWORD='<ephemeral-password>' paseo plugin ls --host 127.0.0.1:17688 --json
[{"id":"director-lifecycle-probe","path":"<owned-plugin>","enabled":true,"status":"running"}]
$ PASEO_PASSWORD='<ephemeral-password>' paseo plugin remove director-lifecycle-probe --host 127.0.0.1:17688 --json
{"id":"director-lifecycle-probe","path":"<owned-plugin>","enabled":false,"status":"disabled"}
$ PASEO_PASSWORD='<ephemeral-password>' paseo plugin ls --host 127.0.0.1:17688 --json
[]
~~~

After workspace archive, `git worktree list --porcelain` contained only the disposable source repository and detached review checkout; both Paseo-managed Task worktrees and branches were gone. The detached checkout was unregistered explicitly. Immediately before final shutdown, the exact PID probe returned only current daemon PID `4107645`; every recorded Task/helper/lifecycle child, provider, prior daemon, and plugin PID was absent. The listener bound `127.0.0.1:17688` only to that daemon. After SIGINT, the listener and exact PID probes returned no rows.

~~~console
$ ps -p '<all-recorded-owned-pids>,4107645' -o pid=,ppid=,stat=,args=
4107645 <supervisor-pid> Sl+ Paseo Daemon
$ ss -ltnp 'sport = :17688'
LISTEN 0 511 127.0.0.1:17688 0.0.0.0:* users:(("Paseo Daemon",pid=4107645,fd=27))
# After SIGINT:
$ ss -ltnp 'sport = :17688'
State Recv-Q Send-Q Local Address:Port Peer Address:Port Process
$ ps -p '<all-recorded-owned-pids>,4107645' -o pid=,ppid=,stat=,args=
exit 1; no rows
$ git -C <source-repository> worktree remove <detached-review-checkout>
$ rm -rf <owned-1.5-GiB-experiment-root>
$ test ! -e <owned-experiment-root>
exit 0
~~~

Both plugin fixtures, every agent/workspace/worktree/process/listener, the detached review checkout, source repository, daemon home, and 1.5 GiB experiment root were removed. No public Beads/Dolt ref or primary workspace was touched.

## Independent-review corrections preserved

- **Structural preflight P1:** resolved by checking 43 client/root/ref-handle methods before connection/create and by independently removing every method in an offline suite that reported 43 precise failures and zero effects.
- **Active archive P1:** resolved by the durable unique child marker, exact child/provider/daemon ancestry, public active-turn observation, the prior 428 ms and refreshed 450 ms exact-fixture archive results, exact-PID absence at response and after delay, idempotent retry/reconciliation, and independently usable workspace.
- **Reproduction P2:** resolved by the exact no-prompt failure command/output and the explicit active-list, plugin-removal, PID, listener, daemon-stop, owned-root deletion, and absence commands/results above.
- **PLAN v0.3 / ADR-0010 P1:** resolved by the successful clean topology run: omitted top-level parentage, exact titles, concurrent Task worktrees, detached Reviewer, non-overlapping replacement, helper parentage/scope/accounting/interruption/reconciliation/cleanup, and zero final resources.
- **Advanced base P1:** resolved by preserving the two prior commits and rebasing them onto exact `58dba422562c820b60fd1a214b66577b9dbbb83e` before the refreshed run. ADR-0005's provider/MCP result is cross-referenced without making a new provider admission or helper security claim.

## Result

The evidence supports **Go with mandatory recovery constraints**:

- persist Task/Run intent before creation and persist returned native IDs/labels immediately;
- include the initial prompt atomically in create;
- use subscriptions only to schedule list/ref/refresh reconciliation;
- treat plugin crash as failed until an explicit reload after reconciliation;
- assume no cleanup callback on daemon loss;
- never infer completion from stale `running` or from missing `activeTurn`;
- reconcile durable external effects before any recovery send or retry;
- archive and apply at most one replacement when a provider record cannot resume;
- create Task Agents and Reviewers as top-level agents with omitted parents; use exact Task titles and separate Task worktrees;
- reserve and reconcile helper identity, fixed scope, capacity, quota, and budget before helper work; never promote a helper into Task/Run/Candidate ownership;
- require the previous top-level Task Agent to be closed before one same-title replacement;
- enter `Needs you` whenever the interrupted effect remains ambiguous;
- archive agents before workspaces and retain their durable audit references.

These constraints align ADR-0003 with PLAN v0.3 and ADR-0010. They do not authorize M1 while another M0 stop condition remains, and they do not weaken ADR-0008: labels, parentage, worktrees, provider settings, and MCP policy are not an OS or credential boundary.
