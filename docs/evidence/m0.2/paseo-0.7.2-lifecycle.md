# M0.2 evidence: Paseo 0.7.2 lifecycle and recovery

- **Captured:** 2026-09-06
- **Beads Task:** `dir-m0.2`
- **Host:** Debian 13, Linux 6.12.107 x86_64
- **Topology:** isolated password-protected loopback daemon, headless, relay/MCP disabled
- **Versions:** Paseo CLI/plugin/client/protocol 0.7.2, Node.js 26.7.0, TypeScript 5.9.3
- **Provider sample:** Codex 0.147.0, `codex/gpt-5.4-mini`, disposable full-access directory

The Node.js runtime was 26.7.0 only because that was the installed host. The supported implementation bound remains Node.js 22 or newer, as established by ADR-0002. No observation here authorizes Node.js 26-only APIs.

## Falsifiable question

Can an exact-0.7.2 public plugin and SDK recover deterministically from plugin reload, disable, subprocess crash, and daemon restart without losing or duplicating native Paseo agents/workspaces, while using subscriptions only as wake-up signals and public list/ref/refresh state as authority?

Success additionally required active-agent archive to close the runtime, stable IDs and Director labels to survive interruption, workspace archive to be independent and idempotent, missing capabilities to fail before a side effect, and complete cleanup of the isolated topology.

Failure meant that recovery required a private endpoint, direct database access, UI automation, or log parsing; that identities disappeared or duplicated; that active archive did not close the runtime; that an incompatible host could perform an effect before rejection; or that owned resources could not be cleaned.

## Evidence fixture

The checked-in [fixture](./fixture/) is copied over an official `paseo plugin init` scaffold. It contains:

- a mixed v0.7 entry with one generated-style client surface and one Zod-validated server RPC;
- a server lifecycle marker used only to distinguish process starts, returned cleanup callbacks, and the deliberate crash;
- an incompatible entry that checks a missing method before starting the marker;
- a public `@getpaseo/client` harness for structural checks, subscriptions, create/list/ref/refresh, send/wait, and archive.

The process marker is test instrumentation, not Director product state. Product conclusions below come from documented CLI or SDK results. Daemon output was used only to debug the fixture and is not an authority.

Final fixture SHA-256 values:

| File | SHA-256 |
|---|---|
| `incompatible-index.ts` | `921857d1114e41bb956473e4ac60e8643aa8c6351a7697e347c445580f05f35e` |
| `index.ts` | `1a8009ed1d00ed3554a7ceb18a5cfb6aa89195e1fc600bb4cb72c782cc10c52c` |
| `lifecycle-client.mjs` | `77f3e85323f8c1f0415e3cdfc7399dc3b0c46c06ec64d9a71c642112fc48fe02` |
| `main.client.tsx` | `bf234c037be01c75b5a0fe2329c0d09ee39ebe47a216daf90916b0e3b109c8a0` |
| `probe.server.ts` | `4cb67ba49003ea7fb28d4d9aedfc571635ff470eddb03949207833607b1f65aa` |
| `probe.shared.ts` | `20405f579f488b1a2adf73d764810ab5d08cafe1d54e23f5075e1fddaa6cbd21` |

## Authentication and structural compatibility

An unauthenticated documented CLI request failed with `Password required`. The same request and every subsequent operation succeeded only with the ephemeral password. The password is not retained in this repository.

The official scaffold and fixture typechecked against exact `@getpaseo/plugin@0.7.2`. A fake SDK object missing `config.patch` produced:

~~~json
{"error":"Missing required public method: config.patch","sideEffects":0}
~~~

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
| Daemon restart, active turn | Agent and workspace returned as `running`, but `activeTurn` was absent and did not self-resolve after five seconds | A new public `send` resumed the same provider session and completed; no duplicate agent appeared | Previous turn outcome is ambiguous: reconcile durable effects first; never blindly retry; enter `Needs you` if ambiguity remains |
| Active agent archive | First archive returned an `archivedAt`; public state immediately became `closed` | Second archive succeeded with the identical timestamp; workspace remained active | Archive is the supported close/interrupt operation |
| Workspace archive | First archive returned no error; ref/active-list no longer returned the workspace | Second archive succeeded with the identical timestamp; archived closed agent remained recoverable by ref and label-filtered include-archived list | Archive agents first, then workspace; keep durable audit references |

The returned plugin cleanup callback ran on reload, disable, and explicit plugin removal. It did not write its marker during any of three graceful daemon shutdown/restart cycles. A daemon stop must therefore be treated like abrupt engine loss: cleanup callbacks are best effort, not a correctness boundary.

## Agent creation and recovery edges

The initial no-prompt agent became public `idle` state and exposed a provider session ID. After daemon restart, its first send failed explicitly:

~~~text
Failed to resume Codex thread <session>: no rollout found for thread id <session>
~~~

Archiving that record twice was idempotent. A replacement created with its initial prompt in the same public create request completed `READY`, survived another daemon restart, and later resumed on the same provider session. Director must include the initial task prompt atomically in agent creation. If the host still fails before the first provider rollout becomes durable, the explicit resume error is recoverable only by archiving the unusable record and applying the plan's one-replacement limit after label/ID reconciliation.

For the active-restart case, the first prompt was a bounded `sleep 90`. The daemon was stopped while the public agent showed `running` with a non-null active turn. After restart, list/ref/refresh preserved the record as `running` but omitted `activeTurn`. Sending `Reply with exactly AFTER_RESTART.` through the same handle resumed the same session and ended in `idle` with `AFTER_RESTART`.

This demonstrates transport recovery, not proof that the interrupted turn had no external effect. Director must compare TaskStore, Git, GitHub, and workspace facts before deciding whether a recovery message, archive/replacement, or `Needs you` is safe.

## Subscriptions and authority

SDK subscriptions emitted nonzero events during all exercised mutations:

- initial create: one workspace event and three agent events;
- active-restart create: two workspace events and four agent events;
- active archive: nine agent events.

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
  "before":{"status":"running","activeTurn":{"turnId":"<turn>"}},
  "after":{"status":"closed","archivedAt":"<timestamp>"},
  "secondArchive":{"ok":true,"archivedAt":"<same-timestamp>"}
}
~~~

Active daemon-restart recovery:

~~~json
{
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

paseo plugin init /tmp/director-m0.2/plugin --id director-lifecycle-probe --json
# Copy docs/evidence/m0.2/fixture/index.ts, main.client.tsx,
# probe.server.ts, probe.shared.ts, and lifecycle-client.mjs into the scaffold.
cd /tmp/director-m0.2/plugin
npm install --ignore-scripts
npm run typecheck
node --check lifecycle-client.mjs

paseo plugin init /tmp/director-m0.2/incompatible --id director-lifecycle-incompatible --json
# Copy incompatible-index.ts to incompatible/index.ts, then copy
# probe.server.ts and probe.shared.ts beside it. Install and typecheck it too.
cd /tmp/director-m0.2/incompatible
npm install --ignore-scripts
npm run typecheck

# Run this foreground daemon in a dedicated terminal.
PASEO_PASSWORD='<ephemeral-password>' DIRECTOR_LIFECYCLE_ROOT=/tmp/director-m0.2/runtime paseo daemon start --home /tmp/director-m0.2/home --listen 127.0.0.1:17682 --foreground --no-relay --no-mcp --no-web-ui

# This must fail with "Password required".
paseo plugin ls --host 127.0.0.1:17682 --json

export DIRECTOR_PASEO_URL='ws://127.0.0.1:17682/ws'
export DIRECTOR_PASEO_PASSWORD='<ephemeral-password>'
export DIRECTOR_LIFECYCLE_STATE=/tmp/director-m0.2/state.json
export DIRECTOR_WORKSPACE_PATH=/tmp/director-m0.2/agent-work
export DIRECTOR_TEST_PROVIDER=codex/gpt-5.4-mini

cd /tmp/director-m0.2/plugin
node lifecycle-client.mjs negative-structural
node lifecycle-client.mjs enable-plugins
PASEO_PASSWORD='<ephemeral-password>' paseo plugin install /tmp/director-m0.2/incompatible --host 127.0.0.1:17682 --json
test ! -e /tmp/director-m0.2/runtime/director-lifecycle-incompatible.events.jsonl
PASEO_PASSWORD='<ephemeral-password>' paseo plugin remove director-lifecycle-incompatible --host 127.0.0.1:17682 --json
PASEO_PASSWORD='<ephemeral-password>' paseo plugin install /tmp/director-m0.2/plugin --host 127.0.0.1:17682 --json
node lifecycle-client.mjs create
node lifecycle-client.mjs recover

PASEO_PASSWORD='<ephemeral-password>' paseo plugin reload director-lifecycle-probe --host 127.0.0.1:17682 --json
node lifecycle-client.mjs recover
PASEO_PASSWORD='<ephemeral-password>' paseo plugin disable director-lifecycle-probe --host 127.0.0.1:17682 --json
node lifecycle-client.mjs recover
PASEO_PASSWORD='<ephemeral-password>' paseo plugin enable director-lifecycle-probe --host 127.0.0.1:17682 --json

# Deliberate test-only crash; observe "failed", reconcile, then reload.
touch /tmp/director-m0.2/runtime/director-lifecycle-probe.crash
PASEO_PASSWORD='<ephemeral-password>' paseo plugin ls --host 127.0.0.1:17682 --json
node lifecycle-client.mjs recover
PASEO_PASSWORD='<ephemeral-password>' paseo plugin reload director-lifecycle-probe --host 127.0.0.1:17682 --json

# Stop the foreground daemon with SIGINT and restart the identical command/home.
node lifecycle-client.mjs recover
DIRECTOR_INITIAL_PROMPT='Reply with exactly READY.' node lifecycle-client.mjs replace-started

# Restart again, then verify active archive.
node lifecycle-client.mjs archive-active
node lifecycle-client.mjs archive-workspace

# Create an atomically prompted active turn, restart during it, and reconcile.
DIRECTOR_INITIAL_PROMPT='Run the shell command sleep 90 and wait for it to finish. Do nothing else.' node lifecycle-client.mjs create-active
# Stop/restart daemon here.
node lifecycle-client.mjs recover
DIRECTOR_INITIAL_PROMPT='Reply with exactly AFTER_RESTART.' node lifecycle-client.mjs resume
node lifecycle-client.mjs archive-current
node lifecycle-client.mjs archive-workspace
~~~

## Cleanup evidence

Before shutdown, public active lists for plugins, agents, and workspaces were each `[]`. All three test agents were archived, both test workspaces were archived, and the plugin/control fixtures were removed from daemon configuration. The foreground daemon was stopped. A host listener check returned only the header for port 17682, and exact-path process checks found no fixture or `sleep 90` process. Finally, the 1.2 GiB isolated experiment tree containing the daemon home, generated plugins, provider state, downloaded models, workspace, and marker was deleted. Every process carrying the ephemeral password environment had exited. No public Beads/Dolt ref or primary workspace was touched.

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
- enter `Needs you` whenever the interrupted effect remains ambiguous;
- archive agents before workspaces and retain their durable audit references.

These constraints preserve the existing PLAN behavior; they do not authorize M1 while another M0 stop condition remains.
