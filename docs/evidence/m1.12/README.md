# `dir-m1.12` standalone-engine evidence

- **Captured:** 2026-09-07
- **Task:** `dir-m1.12`
- **Base:** `origin/main` at `77615884f2e0f5e177ee43c6f725739690f0b2c0`
- **Result:** No-go for moving the Director 1.0 engine outside the Paseo 0.7.2
  plugin subprocess; retain the modular TypeScript monolith
- **Scope:** architecture evidence only; no Director product behavior

## Falsifiable question and boundary

Can one standalone Director engine process outside the Paseo Node plugin
complete the ADR-0003 Task lifecycle through a policy-free connector while
preserving ADR-0015 intent, evidence, fencing, idempotency, and fail-closed
recovery across the extra hop, and remain installable through the audited Git
plugin lifecycle without vendoring or silent downloads?

The complete pre-experiment question, criteria, exact bounds, three allowed
outcomes, and deadline were recorded in Beads comment
`01a079da-0f75-7da4-bd8f-c285744d5415` before repository files changed.
Comment `01a079db-803e-704b-8e70-292dda1b326d` preserves the correction that
the claim and frame were performed by this Task Agent but inherited the human
actor; every later Beads operation uses the Task Agent principal explicitly.

The run stayed inside one disposable exact-0.7.2 daemon/plugin installation,
one product repository/worktree family, one standalone-engine artifact build,
and one complete injected-failure matrix. It used no relay, remote MCP,
GitHub mutation, paid model, user-owned product repository, or non-Linux
topology. The only product repository was the disposable fixture below. No
new TaskStore database was needed: accepted ADR-0004/ADR-0012 evidence and
the installed `dolt` public help decide that process-mode question without
repeating their destructive and credential-sensitive experiment.

## Exact environment

```text
Debian GNU/Linux 13.6 (trixie), x86_64
Linux 6.12.107+deb13-amd64
Paseo CLI/server and installed public packages 0.7.2
Node.js v26.7.0 (the decision retains the documented Node >=22 floor)
npm 11.19.0
Go 1.26.5 linux/amd64 (inventory only; no Go implementation was preferred)
Git 2.47.3
Dolt 2.3.2
Beads 1.2.2, commit 6c124203e771
```

Inventory commands:

```sh
date --iso-8601=seconds
uname -a
sed -n '1,80p' /etc/os-release
paseo --version
node --version
npm --version
go version
git --version
dolt version
bd version
```

Installed exact-package hashes:

```text
17f99b5bacf0beab0de98f1b06aba5883d1ac2c061109f6f0c094692a938334d  @getpaseo/plugin/package.json
fc43accea34aed98f5d7269790a279d1f5e3e471854afc1e34c1f438fc2efaa9  @getpaseo/plugin/dist/contracts.d.ts
d709063eaff6708dfe3a0fd9a093ffb02635b0092ab0bc5e33ccdc4726efc65e  @getpaseo/client/package.json
8593bfb59e5ce49b4d337283738df00bdd718d477b3108cee72a6b9861731839  @getpaseo/client/dist/index.d.ts
```

## Primary public sources

These sources were reread on 2026-09-07. The installed immutable 0.7.2
declarations remain the compatibility authority where current website content
has moved ahead of the published artifact, as ADR-0002 already requires.

| Source | Relevant supported fact |
|---|---|
| <https://paseo.sh/docs/plugins> | v0.7 is current and v0.8 is preview; the plugin API is experimental. |
| <https://paseo.sh/docs/plugins/v0.7/reference> | The top-level contribution receives `PluginContext`; backend RPC handlers receive `{ paseo }`; that API connection belongs to the plugin subprocess and closes when it stops. Git preparation commands are explicit direct-argv manifest entries and run before activation. |
| <https://paseo.sh/docs/sdk/reference> | Standalone SDK clients need a URL plus password or authorization header; the injected plugin `PaseoApi` deliberately omits connection lifecycle. |
| <https://paseo.sh/docs/sdk/workspaces> | `source.kind=directory` registers an existing directory; `source.kind=worktree` asks Paseo to create and own a worktree. |
| <https://paseo.sh/docs/worktrees> | Paseo-owned creation runs repository `paseo.json` setup and archive runs teardown/removal. |
| `dolt sql-server --help`, installed 2.3.2 | The approved store is a MySQL-compatible external server over a data directory; listener, privilege, branch-control, and system/session variable configuration are explicit. |
| `dolt backup --help`, installed 2.3.2 | Backup is an external snapshot/sync/restore operation; interruption and destination-idle pruning have explicit semantics. |

No private endpoint, internal Paseo database, UI automation, or log parsing was
used as a product authority path. Plugin logs were inspected only to diagnose
disposable fixture-load failures.

## Decisive public-authority result

The checked-in
[`authority-probe.ts`](../../../tools/spikes/dir-m1.12/live-fixture/authority-probe.ts)
compiled against exact `@getpaseo/plugin@0.7.2` and
`@getpaseo/client@0.7.2`. It establishes:

```text
"paseo" is not a key of PluginContext
PluginHandlerContext["paseo"] is PaseoApi
"connect" and "close" are not keys of the injected PaseoApi
```

The installed declarations agree. `PluginContext` exposes contribution
registration and `handle`; only the handler context has `paseo`. The official
reference states that the handler connection belongs to the subprocess and
closes when the plugin stops.

The live no-web-UI daemon loaded the connector twice around a supported plugin
reload. Both top-level connector descriptions recorded:

```json
{"hasTopLevelPaseoApi":false}
{"hasPaseoApi":false,"contractHash":"9ee14489ce72ddace0df4550b11ad03ce3ac946bd9392e67e5cd047c210715e4"}
```

No inbound UI RPC existed in the headless topology, so no handler context
could supply the API. Retaining an API obtained by a prior handler cannot fix
reload: the public contract says its owning subprocess connection closes. The
standalone engine therefore cannot initiate startup reconciliation, scheduler
effects, Organizer MCP effects, or periodic observation through a connector
after headless load/reload. Engine process survival does not convey the
non-serializable in-process authority object across the hop.

The public standalone-client alternative requires daemon URL and password or
authorization-header authority in the engine. It bypasses the required thin
plugin connector and the ADR-0002 injected authority path, so it was not used
as a fallback. A connected desktop/web/mobile client could call a bootstrap
RPC, but that is not headless and makes correctness depend on client presence.

This single absent supported authority path falsifies the full standalone
boundary even though the isolated transport mechanics below pass.

## Extra-hop contract and interruption matrix

[`standalone-engine-contract.mjs`](../../../tools/spikes/dir-m1.12/standalone-engine-contract.mjs)
is a deterministic, product-free model. It starts separate engine/connector
processes over an owned Unix-domain socket with uint32be length-prefixed JSON
frames. Its connector accepts only Director agent-runtime verbs and exact
arguments; it rejects policy, retry, and expected-version fields. The engine
owns command identity, policy, intent, observation, retry classification,
projection, and cursors.

The modeled agent-runtime capabilities are:

```text
executionWorkspace.createManaged
executionWorkspace.observe
executionWorkspace.archive
taskAgent.createWithInitialPrompt
reviewerAgent.createWithInitialPrompt
helperAgent.observe
agent.observe
agent.archive
```

The happy path covered eight lifecycle effects: Paseo-managed Execution
Workspace creation, Task Agent creation with its initial prompt, observation
of a Task-Agent-created helper without engine launch, independent Reviewer
creation, explicit helper/Reviewer/Task Agent archive, and workspace archive.
It produced seven connector mutation handoffs, eight completed effects,
seventeen ordered events, and a stable resumable cursor page.

The failure matrix injected:

| Boundary | Durable/external condition | Result after restart |
|---|---|---|
| T0 before intent | no durable intent | one admitted intent, one handoff, one completion |
| T0 after intent | intent durable, no observation | one handoff, one completion |
| T1 after observation | precondition observation durable | fresh reconciliation, one handoff, one completion |
| T2 after dispatch claim | permit/attempt durable, no handoff | one handoff, one completion |
| T3 connector call in flight | intent and dispatch durable; connector persisted external object then dropped response | engine observed/adopted exact correlation; one handoff, one completion |
| T4 before projection | response recorded as observation-required, completion event absent | fresh observation; zero repeat handoff; one completion event |

ADR-0015 T5 compensation is not another connector state. The model admitted a
separate command linked to the preserved original Effect, then ran that
compensating archive through the same T0–T4 protocol. It produced one archive
handoff, kept the original result, and retained the `compensationFor` link.

Same-key/same-payload replay returned the original command. Same key with a
different payload failed with
`IDEMPOTENCY_KEY_REUSED_WITH_DIFFERENT_PAYLOAD`. Removing the Reviewer
capability, changing the contract hash, or placing policy in a connector
request each failed before any external handoff. A stale holder at lease epoch
1 also failed before observation or handoff after durable takeover advanced
the engine fence to epoch 2; the connector never interprets a TaskStore epoch.
A generated client was built only in the owned temporary directory;
regeneration matched, a framing-schema mutation produced drift, and no
generated client was committed. The captured result is
[`observed-output.json`](observed-output.json).

Initial execution inside the filesystem sandbox failed with `listen EPERM` for
the owned Unix socket. The exact same bounded command was rerun under the
approved local-socket permission. One fixture assertion initially counted
archive observations as mutations; the correction distinguished desired
archive state and helper observation. The successful matrix above is the only
evidence result, and its owned temporary root was absent afterward.

Reproduction:

```sh
node --check tools/spikes/dir-m1.12/standalone-engine-contract.mjs
node tools/spikes/dir-m1.12/standalone-engine-contract.mjs
```

The second command needs permission to create a Unix-domain socket under its
owned temporary root.

## Workspace adoption, creation, and ADR-0014

The live product fixture committed a non-empty `worktree.setup` command. The
engine-side harness computed lifecycle digest
`f84e9135f403a74dabab552192b8e8e51a150e112085196666832443875ef9a3`.
Without the fixture human approval it made no SDK call: the workspace count and
setup marker were unchanged. With the exact digest admitted, public workspace
creation ran setup once.

Observed public result:

```json
{
  "refusalBeforeSdkCall": true,
  "adoptedKind": "worktree",
  "adoptedPaseoOwned": false,
  "adoptedDirectoryRemained": true,
  "managedKind": "worktree",
  "managedPaseoOwned": true,
  "setupRanOnlyAfterApproval": true,
  "managedDirectoryRemoved": true
}
```

Registering the engine-created Git worktree through
`source.kind=directory` identifies it as a Git worktree but reports
`gitRuntime.isPaseoOwnedWorktree=false`; archive leaves the directory. That is
adoption, not the Paseo-managed topology required by PLAN §13.1. The current
plan therefore continues to require `source.kind=worktree`. Because that call
runs repository lifecycle configuration, ADR-0014 admission remains an engine
precondition before the policy-free connector invokes Paseo. Adoption cannot
be used as an implicit bypass.

The first workspace run exposed only fixture-shape issues: the supported setup
form is a command string, snapshots use `workspaceKind`, and Git runtime
enrichment settles asynchronously. Each partial attempt was followed by
archive and exact worktree/branch cleanup before the corrected observation.

### Live reproduction commands

Use a dedicated host port and owned temporary root. Keep the generated password
out of transcripts and evidence. These commands intentionally target only the
disposable daemon and repositories:

```sh
DIRECTOR_M112_SOURCE_ROOT="$(pwd)"
DIRECTOR_M112_ROOT="$(mktemp -d /tmp/director-m1.12-live.XXXXXX)"
DIRECTOR_M112_RUNTIME_ROOT="$DIRECTOR_M112_ROOT/runtime"
DIRECTOR_LIFECYCLE_MARKER="$DIRECTOR_M112_ROOT/lifecycle-ran"
DIRECTOR_PASEO_PASSWORD="$(openssl rand -hex 24)"
DIRECTOR_PASEO_URL="ws://127.0.0.1:17712/ws"
export DIRECTOR_M112_SOURCE_ROOT DIRECTOR_M112_ROOT
export DIRECTOR_M112_RUNTIME_ROOT DIRECTOR_LIFECYCLE_MARKER
export DIRECTOR_PASEO_PASSWORD DIRECTOR_PASEO_URL
mkdir -p "$DIRECTOR_M112_RUNTIME_ROOT"

paseo plugin init "$DIRECTOR_M112_ROOT/plugin-src" \
  --id director-standalone-probe --json
cp "$DIRECTOR_M112_SOURCE_ROOT"/tools/spikes/dir-m1.12/live-fixture/* \
  "$DIRECTOR_M112_ROOT/plugin-src/"
cd "$DIRECTOR_M112_ROOT/plugin-src"
npm pkg set scripts.preinstall='node install-marker.mjs preinstall'
npm pkg set scripts.install='node install-marker.mjs install'
npm pkg set scripts.postinstall='node install-marker.mjs postinstall'
npm install --ignore-scripts
npm run typecheck
node --check engine-source.mjs
node --check live-client.mjs
node --check prepare-engine.mjs
test ! -e "$DIRECTOR_M112_RUNTIME_ROOT/install-script-ran"

git init -b main
git config user.name 'Director Spike'
git config user.email 'director-spike@example.invalid'
git add authority-probe.ts connector.server.ts engine-source.mjs index.ts \
  install-marker.mjs live-client.mjs main.client.tsx package.json \
  package-lock.json paseo-plugin.json prepare-engine.mjs tsconfig.json
git commit -m 'fixture: standalone connector probe'

mkdir -p "$DIRECTOR_M112_ROOT/product"
cp "$DIRECTOR_M112_SOURCE_ROOT"/tools/spikes/dir-m1.12/product-fixture/* \
  "$DIRECTOR_M112_ROOT/product/"
git -C "$DIRECTOR_M112_ROOT/product" init -b main
git -C "$DIRECTOR_M112_ROOT/product" config user.name 'Director Spike'
git -C "$DIRECTOR_M112_ROOT/product" \
  config user.email 'director-spike@example.invalid'
git -C "$DIRECTOR_M112_ROOT/product" add README.md paseo.json setup.mjs
git -C "$DIRECTOR_M112_ROOT/product" commit -m 'fixture: lifecycle surface'
DIRECTOR_PRODUCT_REPOSITORY="$DIRECTOR_M112_ROOT/product"
DIRECTOR_ENGINE_WORKTREE="$DIRECTOR_M112_ROOT/engine-created-worktree"
export DIRECTOR_PRODUCT_REPOSITORY DIRECTOR_ENGINE_WORKTREE
git -C "$DIRECTOR_PRODUCT_REPOSITORY" worktree add \
  -b spike/dir-m1.12-engine-created "$DIRECTOR_ENGINE_WORKTREE" main

PASEO_PASSWORD="$DIRECTOR_PASEO_PASSWORD" \
  paseo daemon start --home "$DIRECTOR_M112_ROOT/home" \
  --listen 127.0.0.1:17712 --foreground --no-relay --no-mcp \
  --no-inject-mcp --no-web-ui \
  >"$DIRECTOR_M112_ROOT/daemon.log" 2>&1 &
DIRECTOR_DAEMON_PID="$!"
export DIRECTOR_DAEMON_PID

for DIRECTOR_WAIT_ATTEMPT in $(seq 1 100); do
  if node "$DIRECTOR_M112_ROOT/plugin-src/live-client.mjs" enable-plugins; then
    break
  fi
  sleep 0.1
done
kill -0 "$DIRECTOR_DAEMON_PID"
PASEO_PASSWORD="$DIRECTOR_PASEO_PASSWORD" paseo plugin add \
  "file://$DIRECTOR_M112_ROOT/plugin-src" --ref main \
  --host 127.0.0.1:17712 --json
test ! -e "$DIRECTOR_M112_RUNTIME_ROOT/install-script-ran"
cat "$DIRECTOR_M112_RUNTIME_ROOT/prepared.json"
for DIRECTOR_WAIT_ATTEMPT in $(seq 1 100); do
  if test -s "$DIRECTOR_M112_RUNTIME_ROOT/engine.pid"; then break; fi
  sleep 0.1
done
test -s "$DIRECTOR_M112_RUNTIME_ROOT/engine.pid"
DIRECTOR_ENGINE_PID_BEFORE="$(cat "$DIRECTOR_M112_RUNTIME_ROOT/engine.pid")"
node "$DIRECTOR_M112_ROOT/plugin-src/live-client.mjs" workspace-probe
PASEO_PASSWORD="$DIRECTOR_PASEO_PASSWORD" paseo plugin reload \
  director-standalone-probe --host 127.0.0.1:17712 --json
test "$(cat "$DIRECTOR_M112_RUNTIME_ROOT/engine.pid")" = \
  "$DIRECTOR_ENGINE_PID_BEFORE"
cat "$DIRECTOR_M112_RUNTIME_ROOT/events.jsonl"
```

For the recorded run, the manifest/build preparation was introduced through
the same installation's `paseo plugin update` path so failed-candidate
preservation and final exact-SHA activation were visible. A clean reproduction
may include the final manifest in its initial commit as shown above; it exercises
the same documented preparation and activation sequence.

Cleanup uses public workspace archive and plugin remove first, waits for the
exact engine PID to disappear, removes only the two named disposable branches
and engine-created worktree after ownership checks, gracefully stops the daemon
with `paseo daemon stop --home`, validates process/socket absence, and finally
removes the exact `mktemp` root:

```sh
node "$DIRECTOR_M112_ROOT/plugin-src/live-client.mjs" cleanup-workspaces
PASEO_PASSWORD="$DIRECTOR_PASEO_PASSWORD" paseo plugin remove \
  director-standalone-probe --host 127.0.0.1:17712 --json
PASEO_PASSWORD="$DIRECTOR_PASEO_PASSWORD" paseo plugin ls \
  --host 127.0.0.1:17712 --json

DIRECTOR_ENGINE_PID="$(cat "$DIRECTOR_M112_RUNTIME_ROOT/engine.pid")"
for DIRECTOR_WAIT_ATTEMPT in $(seq 1 60); do
  if ! kill -0 "$DIRECTOR_ENGINE_PID" 2>/dev/null; then break; fi
  sleep 0.1
done
if kill -0 "$DIRECTOR_ENGINE_PID" 2>/dev/null; then exit 1; fi
test ! -e "$DIRECTOR_M112_RUNTIME_ROOT/engine.sock"

git -C "$DIRECTOR_PRODUCT_REPOSITORY" worktree list --porcelain
git -C "$DIRECTOR_PRODUCT_REPOSITORY" worktree remove \
  "$DIRECTOR_ENGINE_WORKTREE"
if git -C "$DIRECTOR_PRODUCT_REPOSITORY" show-ref --verify --quiet \
  refs/heads/spike/dir-m1.12-engine-created; then
  git -C "$DIRECTOR_PRODUCT_REPOSITORY" branch --delete --force \
    spike/dir-m1.12-engine-created
fi
if git -C "$DIRECTOR_PRODUCT_REPOSITORY" show-ref --verify --quiet \
  refs/heads/spike/dir-m1.12-managed; then
  git -C "$DIRECTOR_PRODUCT_REPOSITORY" branch --delete --force \
    spike/dir-m1.12-managed
fi
rm -f -- "$DIRECTOR_LIFECYCLE_MARKER"
test "$(git -C "$DIRECTOR_PRODUCT_REPOSITORY" worktree list --porcelain | \
  grep -c '^worktree ')" -eq 1
test "$(git -C "$DIRECTOR_PRODUCT_REPOSITORY" branch --format='%(refname:short)')" = main
test ! -e "$DIRECTOR_ENGINE_WORKTREE"
test ! -e "$DIRECTOR_LIFECYCLE_MARKER"

PASEO_PASSWORD="$DIRECTOR_PASEO_PASSWORD" paseo daemon stop \
  --home "$DIRECTOR_M112_ROOT/home" --json
for DIRECTOR_WAIT_ATTEMPT in $(seq 1 100); do
  if ! kill -0 "$DIRECTOR_DAEMON_PID" 2>/dev/null; then break; fi
  sleep 0.1
done
if kill -0 "$DIRECTOR_DAEMON_PID" 2>/dev/null; then exit 1; fi
case "$DIRECTOR_M112_ROOT" in
  /tmp/director-m1.12-live.*) rm -rf -- "$DIRECTOR_M112_ROOT" ;;
  *) exit 1 ;;
esac
test ! -e "$DIRECTOR_M112_ROOT"
```

Do not substitute a normal Paseo home or an existing repository. The recorded
run validated the exact root and ownership before each removal; the complete
absence observations appear below.

## Audited engine-artifact distribution

The successful disposable Git update installed exact fixture commit
`69664251de1187f7809ce38f5bc40ba7c9278ee2`. Its manifest declared one
direct-argv preparation command:

```json
{"build":[["node","prepare-engine.mjs"]]}
```

The command verified committed engine source hash
`fb29fe79c1632f8921dff2d681e9e8879e0b782e8bb13d3394ec15aeea032ca1`
and copied it to a content-addressed engine-owned runtime path. The connector
contains that expected hash and never discovers or executes an arbitrary
artifact. `preinstall`, `install`, and `postinstall` sentinel scripts existed
in `package.json`; Paseo inferred no package-manager action and the sentinels
remained absent. The preparation observation was:

```json
{
  "engineSha": "fb29fe79c1632f8921dff2d681e9e8879e0b782e8bb13d3394ec15aeea032ca1",
  "artifactHash": "fb29fe79c1632f8921dff2d681e9e8879e0b782e8bb13d3394ec15aeea032ca1",
  "installScriptsIgnored": true,
  "downloadedArtifacts": 0,
  "vendoredArtifacts": 0
}
```

This proves a first-party source artifact can traverse the audited Git
lifecycle without a committed binary, vendored runtime, implicit package
install, or download. Content-addressed preparation avoids overwriting the
running artifact when a later candidate fails. Unreferenced candidate
artifacts require the ordinary ownership/retention cleanup gate.

Two preceding disposable update candidates failed and Paseo preserved the
installed commit. They attempted to locate an adjacent artifact through
`import.meta.url`; the 0.7 server bundler reported `Invalid URL` and then an
undefined path. That route is not supported. The manifest preparation path is
the reproduced mechanism.

## Engine reload and cleanup

The prepared standalone Node engine PID `1201734` accepted connector PID
`1201676`, observed its cleanup/disconnect, and accepted replacement connector
PID `1207940` after supported plugin reload. The engine PID did not change.
Both connectors reported no top-level Paseo authority. On plugin removal the
second connector closed; after the bounded three-second connector-absence
lease the engine recorded `engine-exit-no-connector` and PID absence was
proven. Thus process survival/reconciliation is technically feasible, but it
does not repair the missing host-authority path.

## TaskStore process mode

No embedded TaskStore is admitted. ADR-0004 and ADR-0012 prove exact Dolt
2.3.2 through its supported external SQL-server and CLI surfaces. Replacing
that with a Go or Node in-process Dolt library would change the audited schema,
safe-commit, identity, backup, and interruption boundary and requires new
evidence.

Under the retained plugin monolith, the typed TaskStore adapter stays in the
trusted engine process and is the sole credential/raw-SQL principal; Dolt is
an external engine-owned server process. The adapter must perform the existing
global/session safe-commit and listener/database identity checks before every
write. Append-only enforcement remains in the selected schema and does not
move into a connector or an embedded library.

External inspection while the server owns the data directory is allowed only
through a typed read-only Director diagnostic/projection. Giving an operator,
agent, connector, or UI a second raw SQL identity would violate ADR-0004.
Offline inspection requires a paused Project and reconciled server absence.
Online backup remains an engine-controlled `DOLT_BACKUP`/supported backup
effect to a unique destination, followed by fresh restore and verification as
ADR-0012 requires. Direct copying of a live data directory remains forbidden.

## Cleanup and absence proof

The final live cleanup observed:

```text
active owned workspaces before final cleanup: 0
plugin list after remove: []
engine PID after connector-absence deadline: absent
engine socket after exit: absent
product worktrees after cleanup: source repository only
product branches after cleanup: main only
engine-created and Paseo-managed worktree directories: absent
lifecycle marker: absent
daemon stop: graceful, usedLifecycleRpc=true
daemon process: absent
/tmp/director-m1.12-live.ZInbnd: absent
```

The model independently removed its socket, child connector processes, state,
ledger, generated client, and owned random root. No TaskStore database/listener
was created. The shared development Beads server, `dir-m1.2`, its parked branch
and preserved ref, and primary untracked research documents were never
touched.

## Result against criteria

| Criterion | Result |
|---|---|
| Extra-hop intent/evidence/idempotency, T0-T4 interruption, T5 compensation, in-flight lost response | Pass in deterministic separate-process model |
| Policy-free Director-verb connector, capabilities, stale fencing, fail-closed missing capability | Pass in model |
| Projection, resumable cursor, generated-client drift check | Pass in model |
| Existing worktree adoption versus Paseo-owned creation and ADR-0014 admission | Decided; adoption is not Paseo-owned, managed creation retains admission gate |
| Audited first-party engine artifact, install scripts ignored, no vendor/download | Pass in live Git update |
| External TaskStore mode and ADR-0004/ADR-0012 implications | Decided from accepted evidence and installed public interface |
| Engine process survival/reconciliation across plugin reload/removal | Pass in live process probe |
| Headless connector can acquire supported `PaseoApi` at startup/reload | **Fail; decisive** |

The protocol, artifact, workspace, TaskStore, and process-lifecycle pieces do
not compensate for the absent public headless authority path. The selected
outcome is therefore the ADR-0017 No-go, not an inference that a standalone
boundary is almost supported.
