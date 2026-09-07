# `dir-m1.12` standalone-engine evidence

- **Captured:** 2026-09-07
- **Task:** `dir-m1.12`
- **Old base:** `77615884f2e0f5e177ee43c6f725739690f0b2c0`
- **Superseded unreviewed Candidate:**
  `80f5cf1a3344cf3ac9232ba5e10f1de07b5509a8`
- **Current base:** `origin/main` at
  `66f8b7127543305db0f560fc8106d2d2c0e5b15f`
- **Superseded rebased carrier:**
  `c44312a4846f7b9bdd2d54a0e931035e108f0dc2`
- **Superseded unreviewed Candidate:**
  `87a7238fc82d8c52120b11cbe0fbde407d6d668b`
- **Superseded changes-requested Candidate:**
  `7d6934ca6a36bceb66c901c0feda1464d1ce7877`
- **Result:** Go for the exact Paseo 0.7.2 connector-authority compatibility
  gate under the project-owner-accepted P2 bounds
- **Scope:** architecture evidence; no Director product behavior

No superseded SHA is eligible for further review, publication, integration, or
authority. The sole review of `7d6934ca...` returned `changes_requested`.
The standalone Go engine and prohibition on the monolith are binding inputs,
not outcome choices.

## Falsifiable question and bounds

Can the standalone Director Engine complete the proven Task lifecycle while
Director for Paseo alone owns a public supported Paseo 0.7.2 connection and
the project-owner-accepted credential, exposes only fixed host capabilities and
normalized observations, keeps secrets and policy out of the engine, and
starts the decided release/development engine artifact fail closed with
attributable identity?

The original pre-change frame is Beads comment
`01a079da-0f75-7da4-bd8f-c285744d5415`. Its stale monolith outcome,
install-hook premise, UI split, and distribution choice are superseded by the
append-only amended frame
`01a07a17-98f1-76f1-be52-1f7af7e560c3` and binding human comments listed in
the decision-consistency matrix below. The initial frame and claim were
performed by this Task Agent but misattributed through the inherited human
actor; correction `01a079db-803e-704b-8e70-292dda1b326d` preserves that audit
fact without rewriting prior rows.

Success required a connector-owned public client and credential; no credential
in the engine; fixed Director capabilities; fail-closed missing authority;
preservation of the prior T0–T5, workspace, TaskStore, drift, projection,
cursor, and reload results; and the fixed release-download/development-compile
distribution. If exact 0.7.2 exposed only an over-broad credential and no
accepted least-privilege capability, the frame required an exact project-owner
P2 choice. The prior Candidate recorded the intermediate result before that decision.
The current Task note headed “Human decision 2026-09-07” now accepts the
bounded exact-0.7.2 credential. A monolith was never an allowed result.

The amendment permitted exactly one disposable password-protected loopback
Paseo home/daemon/plugin, one connector-authority probe, one local release
server, one release build, and one development build. It prohibited repeating
the passing interruption, workspace, TaskStore, cursor, or drift matrices.
The deadline was 2026-09-07T11:30:00+02:00. The work completed inside that
boundary.

## Exact environment

```text
Debian GNU/Linux 13.6 (trixie), x86_64
Linux 6.12.107+deb13-amd64
Paseo CLI/server/plugin/client/protocol 0.7.2
Node.js v26.7.0 (documented implementation floor remains Node >=22)
npm 11.19.0
Go go1.26.5 linux/amd64
Git 2.47.3
Dolt 2.3.2
Beads 1.2.2, commit 6c124203e771
```

Installed exact-package hashes retained from the first evidence run:

```text
17f99b5bacf0beab0de98f1b06aba5883d1ac2c061109f6f0c094692a938334d  @getpaseo/plugin/package.json
fc43accea34aed98f5d7269790a279d1f5e3e471854afc1e34c1f438fc2efaa9  @getpaseo/plugin/dist/contracts.d.ts
d709063eaff6708dfe3a0fd9a093ffb02635b0092ab0bc5e33ccdc4726efc65e  @getpaseo/client/package.json
8593bfb59e5ce49b4d337283738df00bdd718d477b3108cee72a6b9861731839  @getpaseo/client/dist/index.d.ts
```

## Public supported sources

Sources were reread on 2026-09-07. Installed immutable 0.7.2 declarations are
the compatibility authority where current documentation moves ahead.

| Source | Verified supported fact |
|---|---|
| [Paseo plugin v0.7 reference](https://paseo.sh/docs/plugins/v0.7/reference) | Git plugin installation clones the repository. A manifest build is an explicit argv preparation step. Top-level `PluginContext` and handler context differ; only handlers receive the injected host API. |
| [Paseo SDK reference](https://paseo.sh/docs/sdk/reference) | A standalone public client accepts daemon URL plus password or a complete authorization header; the injected plugin API omits connection lifecycle. |
| [Paseo security](https://paseo.sh/docs/security) | Connected clients are trusted operators of the daemon user's resources; password authentication protects the daemon surface rather than individual operations. |
| [Paseo configuration](https://paseo.sh/docs/configuration) | The daemon password is a server-level setting, not a verb-scoped capability. |
| [Paseo workspace SDK](https://paseo.sh/docs/sdk/workspaces) | `source.kind=directory` registers an existing directory; `source.kind=worktree` requests Paseo-owned creation. |
| [Paseo worktrees](https://paseo.sh/docs/worktrees) | Paseo-owned worktrees execute admitted repository setup/teardown and are removed on archive. |
| Installed `dolt sql-server --help` and `dolt backup --help`, 2.3.2 | The selected TaskStore and backup interfaces are external processes/commands, not an admitted embedded library. |

No private endpoint, internal database, CLI orchestration, agent MCP, UI
automation, or log parser was used as product authority.

## Prior evidence preserved without repetition

The deterministic
[`standalone-engine-contract.mjs`](../../../tools/spikes/dir-m1.12/standalone-engine-contract.mjs)
already ran a separate engine and connector over an owned Unix-domain socket.
Its normalized committed output remains
[`observed-output.json`](observed-output.json). It proved:

- a closed eight-verb Director host port and descriptor;
- command/query separation, idempotency-key payload conflict, expected version,
  engine projections, a monotonic resumable cursor, and generated-client drift;
- zero handoff for missing capability, stale contract, connector policy, or a
  stale epoch holder;
- ADR-0015 T0–T5, including a durable intent while the connector persisted an
  external object then dropped the response; restart observed/adopted it with
  one logical completion and no second handoff;
- linked compensation as a new Command which preserved the original Effect;
- engine survival across plugin reload and finite exit after connector absence.

The first run also proved workspace registration is not managed creation.
`source.kind=directory` reported a Git worktree with
`isPaseoOwnedWorktree=false` and left it after archive. After engine-side
ADR-0014 digest admission, `source.kind=worktree` ran setup once, reported
Paseo ownership, and removed the worktree on archive. The standalone engine
must retain that admission before the connector call.

ADR-0004/ADR-0012 evidence and installed Dolt public help remain decisive for
TaskStore mode. Director Engine owns the typed store adapter and sole raw SQL
identity; `dolt sql-server` remains external. Append-only guards and global and
session safe-commit checks remain mandatory. Online consumers use engine
projections; raw offline inspection requires pause and server absence. Backup
uses supported online backup to a unique destination and fresh restore
verification, never a live directory copy. No new database was created.

These passing matrices were deliberately not rerun.

## Focused connector-owned-authority probe

The committed source fixture is
[`connector-authority-fixture`](../../../tools/spikes/dir-m1.12/connector-authority-fixture/).
The live installed fixture commit was
`f9336225f22a75b641fe1f82746db9d10ef97286`; its Git tree excluded
`node_modules`.

### Reproduction shape

The following is the sanitized command shape. The credential value is
intentionally absent. The run used port 17713 and one owned
`/tmp/director-m1.12-auth.XXXXXX` root.

```sh
paseo plugin init "$PROBE_ROOT/plugin-src" \
  --id director-connector-authority-probe --json
cp tools/spikes/dir-m1.12/connector-authority-fixture/* \
  "$PROBE_ROOT/plugin-src/"

# Add exact @getpaseo/client@0.7.2 and the scaffold's exact public dependencies,
# generate a lockfile, typecheck, then commit only sources and the lockfile.
npm install --save-exact @getpaseo/client@0.7.2
npm run typecheck
git add package.json package-lock.json paseo-plugin.json '*.ts' '*.mjs'
git commit -m 'fixture: connector-owned authority probe'

PASEO_PASSWORD_FILE="$PROBE_ROOT/credential" \
  paseo daemon start --home "$PROBE_ROOT/home" \
  --listen 127.0.0.1:17713 --foreground --no-relay --no-mcp \
  --no-inject-mcp --no-web-ui

paseo plugin add "file://$PROBE_ROOT/plugin-src" --ref main --json
# The manifest applies: npm ci --ignore-scripts
# Public SDK control then enables, reloads, checks status, and removes the plugin.
```

Paseo cloned the Git repository into staging and applied the declared manifest
build `npm ci --ignore-scripts` before activation. This is not an install hook.
It materialized the locked connector SDK dependency because the plugin server
host-provides only `@getpaseo/plugin`, `@getpaseo/plugin/server`, and `zod`.

The connector read its own credential file, created `@getpaseo/client`,
connected headlessly, and performed only read-only `workspaces.list`. It then
sent the engine a normalized descriptor with contract version/hash, the eight
fixed Director capabilities, authority owner `director-for-paseo`, credential
kind `daemon-password`, and scope `full-daemon-operator`. The engine child was
spawned with a sanitized allowlist environment and received no raw client.

[`connector-authority-output.json`](connector-authority-output.json) records:

```text
SDK-ready events: 2; read-only workspace counts: 0, 0
connector reload: applied; engine starts: 1; same engine survived: true
engine credential environment keys: []
descriptors: 2; contract version: 1; fixed capabilities: 8
secret in engine events: false; host mutations: 0
```

After deleting the connector credential file, the requested reload was
attempted and failed: command exit 1, plugin status `failed`, error
`connector credential file is absent`, zero later SDK-ready events, and zero
host mutations. It was not a successfully applied reload and no alternate
authority or mode was selected.

### Exact authority boundary

Installed public `PaseoClientConfig` has URL, optional daemon password, and
optional complete proxy authorization header. It has no scope, permission, or
capability field. The resulting high-level client includes agent, workspace,
and daemon configuration operations, including configuration mutation. The
official security model makes a connected client a trusted daemon operator.

Therefore these facts are both true:

1. Connector ownership is mechanically verified. The engine did not receive
   the disposable password in environment or protocol, and replacement
   connectors reconnected around engine survival.
2. Exact 0.7.2 does not expose a least-privileged headless credential. The
   exercised credential was deliberately labelled `full-daemon-operator`; a
   proxy header or passwordless loopback does not add Paseo verb scopes.

### Accepted P2 authority bounds

The current Task note headed “Human decision 2026-09-07” records the project
owner’s acceptance of the full daemon-operator credential on exact Paseo 0.7.2
and rejection of waiting for a vendor roadmap without a committed date. This
resolves the authority choice; it does not turn the credential into least
privilege.

The accepted deployment contract requires, one bound at a time:

1. Credential material is outside every repository and Paseo-managed plugin
   checkout and readable only by the Director for Paseo connector process.
2. Credential bytes and location never reach Director Engine argv,
   environment, inherited file descriptors, protocol messages/events, UI,
   TaskStore, projections, support bundles, logs, or timeline records.
3. Startup and reload fail closed before SDK-ready, engine attachment, or host
   mutation when credential material is absent or empty.
4. Before command handling, the connector advertises
   `credentialScope=full-daemon-operator`, contract version and contract hash,
   and only its fixed capability set; missing or stale values fail closed.
5. User-facing documentation discloses the full daemon-operator requirement
   and controls before installation.

The fixture positively verified connector-owned credential loading, an engine
environment with no credential keys, secret-free engine protocol events, the
required descriptor, replacement-connector reload, and fail-closed credential
absence. It did not claim a hostile same-UID sandbox. Connector-only
readability is a mandatory product/deployment control for M1 credential
provisioning.

This exact-0.7.2 choice makes ADR-0017’s bounded ADR-0002 amendment effective.
Recorded intent is to narrow authority as soon as Paseo exposes and Director
evidences a supported headless connector-scoped credential or equivalently
scoped startup-injected API. That revision changes only connector authority and
compatibility, never engine logic.

### Attempted versus applied corrections

- An initial setup command reached the dependency-install phase inside the
  filesystem sandbox and stopped before any daemon, plugin installation, or
  fixture commit was applied. Its exact stale setup process group was later
  terminated and proven absent.
- The permitted rerun installed 315 locked packages and reported zero audit
  vulnerabilities; exact TypeScript checking passed.
- An initial disposable fixture commit accidentally tracked `node_modules`.
  A second disposable commit removed it from the Git index before plugin
  installation. Only final source commit `f9336225...` was installed, and its
  tree excluded `node_modules`.
- The successful initial load and reload were applied. The later reload after
  credential deletion was attempted and failed closed, as intended.
- No host mutation was attempted by the connector probe.

## Focused release/development distribution probe

[`engine-distribution-contract.mjs`](../../../tools/spikes/dir-m1.12/engine-distribution-contract.mjs)
created one disposable minimal Go source repository, a statically linked
release artifact and exact-source notice file, one local HTTP release server,
one separately committed installed pin, one external XDG-style cache, and one
development build. It ran offline with `CGO_ENABLED=0`, `GOPROXY=off`,
`GOSUMDB=off`, `GOTOOLCHAIN=local`, `-trimpath`, and `-buildvcs=false`.

Command:

```sh
node tools/spikes/dir-m1.12/engine-distribution-contract.mjs
```

[`distribution-output.json`](distribution-output.json) records the applied
result:

```text
source Candidate E: 5b65533269a6d2c62f5dbed81a5658d405ed782f
installed pin P:     349b49aa08de97183bcf07de166c4ceb77c48fc1
version/target:      0.0.0-spike / linux-amd64
binary SHA-256:      484b48f8272f663327c0fa73003034eee5b9ad5f334c05a3f1efc015e075ab23
notices SHA-256:     24dd766e93559a6b45a8e8bbedafce70f0f4ef2275c6ab3b5601bdea5b1e3ad5
cache outside plugin checkout: true; downloaded assets: 2
```

The released binary self-reported release mode, version, E, notice digest, and
the linked fixture dependency. A corrupted download produced
`ENGINE_DIGEST_MISMATCH`, did not execute, and triggered zero compilations.
Explicit development mode compiled exactly once and self-reported
`development/dev` with a distinct binary digest. A missing mode produced
`ENGINE_MODE_REQUIRED` before either launch path ran. The same selector routes
the exercised release and development paths; the development run did not fetch
release assets, and release failure did not compile.

The fixture notices name the linked third-party module/license and exact E.
The production contract requires review of E; generation of binary and notices
from E; immutable GitHub Release assets; and a separately reviewed installed
connector commit P pinning version, target, E, and both digests. The cache is
outside Paseo's managed checkout. Released and local identities must appear in
logs, Doctor, and support bundles. Go is required for development and release
building, not for release users.

The local fixture proved mechanics, not a public release, GitHub permissions,
CI provenance service, production target matrix, or production dependencies.

Review correction made the fixture evidence more direct without rerunning the
live connector probe. The distribution fixture now exercises valid release,
valid development, and missing-mode input through the same selector, and names
its counter `developmentCompilations`. The connector-authority fixture derives
its advertised hash from its explicit descriptor contract document instead of
an opaque literal. The latter source correction changes no normalized observed
field in `connector-authority-output.json`; it received only a syntax/static
check in this correction run, not a new daemon or connector execution.

The first distribution-fixture invocation in this correction run was attempted
inside the filesystem sandbox and stopped at `spawnSync git EPERM`; no Git child
started, and the fixture's `finally` cleanup removed its owned root. The
permitted rerun applied the release and development paths, reproduced every
previous value, added `missingModeRejectedBeforeLaunch: true`, and again
reported `ownedRootAbsent: true`.

## UI liveness and two-hop boundary

The React Native surface reaches the plugin server through Paseo's fixed
Zod-validated RPC. Only plugin-server-to-engine is Director's interface. The
existing scaffold inventory contains per-client subscriptions and TanStack
Query, suggesting query invalidation may bound perceived liveness. This run did
not measure refresh behavior and makes no engine-push latency claim. M1 must
measure Paseo query invalidation/subscription behavior before selecting UI
refresh cadence. The UI still renders only engine projections and submits
typed engine commands.

The Agent Orchestrator Go-daemon/client shape in the architecture-session note
was considered structural precedent only. No private interface, code, decision,
or compatibility fact was copied from it.

## Cleanup and absence proof

The connector plugin was removed through the public API and the final plugin
list was empty. The daemon handled graceful SIGINT and closed. Exact PID and
listener checks proved the engine process, connector process, stale initial
setup process, and port 17713 absent. The credential, socket, plugin checkout,
daemon home, dependency tree, release server, source repositories, build
artifacts, caches, and complete owned roots
`/tmp/director-m1.12-auth.FdntKB` and the distribution fixture root were absent.
The normalized outputs record every cleanup predicate as true.

No workspace, branch, database, remote ref, release, PR, or paid service was
created by the added focused runs. The earlier bounded run had already removed
its owned plugin, daemon, standalone process/socket, workspace directories,
exact temporary branches, generated client, artifact root, and product
fixture. The live Director TaskStore, `dir-m1.2`, its preserved ref and parked
branch, `dir-m1.3`, `dir-m1.5`, `dir-m1.14`, and primary untracked research
documents were untouched.

## Result and accepted boundary

**Go.** All tested connector isolation, reload, contract, distribution,
interruption, workspace, and TaskStore mechanics pass. Public Paseo 0.7.2
offers only a full daemon-operator password or unscoped proxy authorization
header for a headless connector; under “Human decision 2026-09-07,” the project
owner explicitly accepts that P2 residual risk with the five mandatory bounds
above and rejects waiting for an undated vendor capability.

The connector-authority question is therefore resolved for exact 0.7.2. The
standalone Go engine, one host interface, UI in Director for Paseo,
release-download/development-compile distribution, mandatory review, and
monolith prohibition remain unchanged. This Task Agent does not start or resume
dependent work.

For `dir-m1.14`, the exact amended PLAN list is §§1, 3.2, 6.1, 6.2, 6.4, 6.5,
19.2, 19.4, and 20.1. The standalone-headless evidence fulfills the existing
§19.2 trigger. Flat §27 has no subsection, remains an external-fact
revalidation gate, and is not amended by ADR-0017.

## Decision-consistency matrix

The Task Agent reread the complete current Task note headed “Human decision
2026-09-07.” The stopped-text audit maps each bound and frozen decision to
exact ADR, evidence, and user-facing deployment lines.

| Binding record/fact | ADR lines | Evidence lines | User-doc lines | Result |
|---|---:|---:|---:|---|
| P2 selection: accept exact-0.7.2 full daemon-operator connector credential; reject waiting | 204–210, 291–319, 337–351 | 222–226, 371–376 | 9–11, 26–29 | Verified; chosen risk and rejected wait agree |
| Bound 1: credential outside checkout and connector-process-readable only | 214–215 | 230–231 | 15–16 | Verified; exact |
| Bound 2: no engine environment/protocol, UI, TaskStore, log, or timeline propagation | 216–218, 378–380 | 232–234 | 17–19 | Verified; exact or stricter |
| Bound 3: startup and reload fail closed when absent | 219–220 | 235–236 | 20–21 | Verified; exact |
| Bound 4: advertise full scope, contract version/hash, and only fixed capabilities | 221–223 | 237–239 | 22–24 | Verified; exact |
| Bound 5: disclose requirement before installation | 224–225, 373–374 | 240–241 | 7–13 | Verified; warning precedes install links |
| Revision intent: narrow on supported scoped/startup-injected capability; connector-only change | 312–319, 345–351 | 250–254 | 26–29 | Verified; no engine-logic change |
| Effective ADR-0002 amendment is exact-0.7.2 bounded | 0017:11–12, 204–210; 0002:8, 86–99 | 250–254 | 26–29, 38 | Verified; reciprocal and visible |
| Frozen architecture: standalone Go, single interface, UI host package, fixed distribution, mandatory review, monolith prohibited | 29–43, 52–56, 330–366, 375–382 | 378–381 | 31–36 | Verified; no frozen decision reversed |
| Exact PLAN amendment target for `dir-m1.14` | 9–10, 353–361 | 384–387 | — | Verified; §§1, 3.2, 6.1, 6.2, 6.4, 6.5, 19.2, 19.4, and 20.1; §27 unchanged |

## Independent verification

Stored verdict `01a07a64-a0a6-7493-b9d5-b34a88d2b38a` records independent
review of superseded Candidate `7d6934ca...` against the exact current base.
The reviewer verified every binding human decision and all five accepted P2
bounds, then returned `changes_requested` only for a nonexistent PLAN subsection
reference; its three P3 notes motivated the deterministic fixture corrections
above. This replacement Candidate awaits a new coordinator-owned independent
exact-SHA review. This Task Agent performed no self-review and created no
Reviewer Agent. Nothing was pushed, published, merged, or closed, and no
dependent Task was started.

The initial final static assertion was attempted and rejected these three
historical uses of the nonexistent subsection number before commit. Replacing
them with non-normative review-history wording was applied; the corrected
reference scan and assertion suite then passed.
