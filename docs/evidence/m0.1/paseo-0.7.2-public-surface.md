# M0.1 evidence: Paseo 0.7.2 public surface

- **Captured:** 2026-09-06
- **Beads Task:** `dir-m0.1`
- **Host topology:** headless Linux daemon with remote Paseo clients
- **Scope:** public plugin package, TypeScript SDK, CLI, injected Paseo MCP, and official documentation
- **Excluded:** lifecycle/crash tests (`dir-m0.2`), provider MCP matrix (`dir-m0.3`), and product code

This transcript includes exact output where it is safe to publish. Hostnames, addresses, user data, unrelated agent data, opaque IDs, and private paths are replaced with angle-bracket placeholders. Field selection and redaction do not alter the capability values reported below.

## Evidence priority

For a specific stable version, Director uses this order when sources disagree:

1. the generated scaffold and type declarations in the immutable published npm artifact;
2. behavior observed against the installed daemon through a documented public surface;
3. version-specific official documentation;
4. unversioned documentation and Paseo `main` only as forward-looking evidence.

A method shown on the documentation website is not admitted until the published package used for the build declares it and the target daemon passes a public behavior probe.

## Versions and topology

~~~console
$ paseo --version
0.7.2

$ node --version
v26.7.0

$ uname -a
Linux <host> 6.12.107+deb13-amd64 #1 SMP PREEMPT_DYNAMIC Debian 6.12.107-1 (2026-08-29) x86_64 GNU/Linux
~~~

The SDK quickstart requires Node.js 22 or newer. The audited host ran Node.js 26.7.0, while the generated plugin compiler target was ES2020. Director must remain inside the documented Node.js 22 baseline and may not infer support for Node.js 26-only APIs from this host.

The official download page reported Paseo `0.7.2`. npm returned `0.7.2` as the latest version for all four artifacts; the client package's `latest` and `beta` tags both resolved to `0.7.2`. All four artifacts were published on 2026-09-02 UTC.

| Artifact | Version | npm integrity |
|---|---:|---|
| `@getpaseo/cli` | `0.7.2` | `sha512-JjjDIMU2/Nxa37cSRmLOBQ0nlNL4JNit9D1xlwE2cTtvrmD5r+aBYMN5Zk7Kb9Qv5LLPUDvgwP60rynqUDmLNg==` |
| `@getpaseo/plugin` | `0.7.2` | `sha512-uOhaMdxqgIkETjw+yAGRyZ84yJrpb1ECwBRj9f6cSNHeUwNj/1ohwa/oCfmdnoiX57s3oYYkY+7uE8bUy2Q/Fg==` |
| `@getpaseo/client` | `0.7.2` | `sha512-BD3uNBdp4wz79uDIJPYqAnaLXbMlZ1V/Nb9RFv0cEUcrFzk0kKAQ1NOaquD3Lz4MUJFTR8Eh/VxJB+OlbHSsiQ==` |
| `@getpaseo/protocol` | `0.7.2` | `sha512-bLegKuZIqO34Az8eKi0y3W0CcwwICipd7s4iFOUdOAiMwUYybzyXEpVaxuMcSzstNAtGqP+FMRteljFXAJ8T8w==` |

The installed CLI resolved to a global npm installation. Its bundled plugin, client, protocol, and server package manifests all reported `0.7.2`.

The exact installed declarations inspected by the type probe had these SHA-256 hashes:

- `@getpaseo/plugin/dist/contracts.d.ts`: `fc43accea34aed98f5d7269790a279d1f5e3e471854afc1e34c1f438fc2efaa9`
- `@getpaseo/client/dist/index.d.ts`: `8593bfb59e5ce49b4d337283738df00bdd718d477b3108cee72a6b9861731839`

A public status query outside the workspace network sandbox returned this selected result:

~~~json
{
  "localDaemon": "running",
  "connectedDaemon": "auth_required",
  "listen": "<non-loopback-host>:6767",
  "cliVersion": "0.7.2",
  "daemonVersion": null,
  "desktopManaged": false
}
~~~

The daemon was a Node child process supervised by a Paseo process and reported `desktopManaged: false`. The same query from inside the restricted workspace network sandbox reported `stale_pid` and `unreachable`; the unrestricted read-only query reported `running` and `auth_required`. That difference is a sandbox network boundary, not evidence that the daemon stopped.

The stable public status surface does not disclose the running daemon's semantic version in this topology. `daemonVersion: null` is a recorded missing capability, not permission to infer `0.7.2` from the CLI version.

## Official scaffold probe

The `paseo-plugin` skill was applied to a disposable directory. The exact public generator succeeded:

~~~console
$ paseo plugin init --id director-audit /tmp/director-m0-1.xFitsA --json
{
  "id": "director-audit",
  "directory": "/tmp/director-m0-1.xFitsA"
}
~~~

It generated `paseo-plugin.json`, `index.ts`, `main.client.tsx`, `package.json`, and `tsconfig.json`. The manifest contained only the plugin ID. The package pinned `@getpaseo/plugin` to exact `0.7.2`, used the mixed `index.ts` entry expected by v0.7, and declared `tsc --noEmit` as its typecheck.

~~~console
$ npm install --ignore-scripts
added 315 packages, and audited 316 packages in 8s
found 0 vulnerabilities

$ npm run typecheck
> director-audit@0.0.0 typecheck
> tsc --noEmit
~~~

The install resolved `@getpaseo/plugin`, `@getpaseo/client`, and `@getpaseo/protocol` to `0.7.2`. The generated ranges also resolved TypeScript `5.9.3`, React `19.1.0`, React Native `0.81.5`, TanStack Query `5.102.8`, and Zod `4.5.4`. Director must commit a lockfile instead of relying on these open dependency ranges.

The checked-in [type probe](./api-contract-probe.ts) was copied into the generated project and the same typecheck passed. It proves the positive and negative contract assertions below against the immutable npm artifact. The disposable directory consumed 229 MiB after installation and was deleted; an existence check confirmed removal.

## Published v0.7.2 plugin contract

| Area | Public stable surface | Director use |
|---|---|---|
| UI | `addSurface`, `addSidebarItem`, `PluginSurfaceProps` | Global Board/List surface and sidebar entry |
| Context panels | `addWorkspacePanel` for workspace or agent context | Task Inspector in native workspace/explorer locations |
| Commands | `addCommandCenterItem` | Open Director and contextual Task actions |
| Client lifecycle | `addClientSide` and cleanup callback | Per-client subscriptions and composer contributions |
| Composer | `addComposerPill` from `PluginClientContext` | Optional affordance on correlated agents |
| Data access | `usePaseo`, `useWorkspace`, `useAgent` | Host snapshots and public SDK calls |
| Plugin RPC | `defineRpc`, `handle`, `useRpc`; Zod input/output | Schema-validated UI-to-engine commands |
| Timeline rendering | `addTimelineTransformer`, `addTimelineRenderer` | Optional rendering of existing timeline items |
| External resources | `addAttachmentSource` | Potential Organizer resource picker; not required by M1 |
| Host UI | `Icon`, `Modal`, `useToast`, React Native host modules | Cross-platform controls without DOM coupling |
| Appearance | `addTheme` and typed theme/layout props | Theme-safe desktop, web, iOS, and Android rendering |
| Server | trusted unsandboxed Node subprocess and async cleanup | Engine, filesystem, Git, TaskStore, and child processes |

The plugin server receives an authenticated `PaseoApi` from the host. Client surfaces run in Paseo clients and use React Native; `layout.compact` and `layout.platform` are the supported responsive/platform inputs. Navigation to an agent or workspace is optional and must be hidden when absent.

There is no plugin storage API, no general native-route API, and no manifest field for a minimum/maximum host version in the generated v0.7 manifest. Director's Organizer repository and TaskStore remain its durable authority.

## Published v0.7.2 TypeScript SDK contract

| Root | Public methods and handles needed by Director |
|---|---|
| `projects` | `list` |
| `workspaces` | `list`, `ref`, `open`, `create`, `archive`, `subscribe` |
| workspace handle | stable ID; `projectId`, directory, name, status; `current`, `refresh`, `setTitle`, `archive`, `subscribe`; scoped `agents.create` |
| `agents` | `list`, `ref`, `create`, `subscribe` |
| agent handle | stable ID and placement; state/capability/usage/error/runtime snapshots; `current`, `refresh`, `send`, `run`, `waitForFinish`, `commands`, `archive`, `detach`, `subscribe`; timeline `refetch` and `subscribe` |
| `providers` | `listModels`, `listModes`, `listFeatures`, `listAvailable`, `snapshot`, `waitForReady`, `refresh`, `diagnostic`, `subscribe` |
| `config` | raw typed `get` and validated `patch` |

Agent creation accepts a frozen provider/model, mode, thinking option, feature values, validated provider-native options, system prompt, tool policy, session-scoped MCP servers, parent agent, title, environment, prompt, output schema, attachments, Git/worktree input, auto-archive, request ID, and labels.

This is sufficient to map a Director Run to persisted Paseo agent/workspace IDs and agent labels, recover by list/ref/refresh, observe by subscriptions plus reconciliation, send corrections, and close the provider runtime by archiving. Whether archive and workspace cleanup satisfy every cancellation and crash invariant is deliberately deferred to `dir-m0.2`.

## Installed public runtime observations

The authenticated injected Paseo MCP exposed 61 Paseo tools in the current Codex session: 39 non-browser orchestration tools and 22 browser tools. Representative public read-only calls succeeded for workspaces, agents, agent status, providers, models, profiles, schedules, pending permissions, and terminals. Payloads belonging to unrelated projects and agents are not published.

Relevant non-browser tools observed in the loaded catalog were:

~~~text
archive_agent, archive_workspace, cancel_agent, create_agent, create_workspace,
get_agent_activity, get_agent_status, inspect_provider, kill_agent, list_agents,
list_models, list_pending_permissions, list_profiles, list_providers,
list_workspaces, rename_workspace, respond_to_permission, send_agent_prompt,
set_agent_mode, update_agent
~~~

The current Codex agent reported:

~~~json
{
  "supportsStreaming": true,
  "supportsSessionPersistence": true,
  "supportsSessionListing": true,
  "supportsDynamicModes": false,
  "supportsMcpServers": true,
  "supportsReasoningStream": true,
  "supportsToolInvocations": true,
  "supportsRewindConversation": true,
  "supportsRewindFiles": false,
  "supportsRewindBoth": false
}
~~~

Provider discovery returned structured provider, model, mode, thinking-option, and feature metadata. This proves capability-driven selection for the installed Codex provider only. It does not establish the multi-provider matrix; `dir-m0.3` owns that proof.

The built-in MCP and plugin SDK are distinct authority surfaces. MCP offers cancel, hard kill, mode changes, and permission responses that are not in the published `PaseoApi` passed to plugin handlers. Director's engine must not ask a worker agent to perform engine-owned effects, so MCP-only operations do not fill SDK gaps.

## Missing or unsafe-to-assume surfaces

The immutable `0.7.2` public declarations do not expose:

- a daemon version or server-feature accessor on `PaseoApi`;
- a plugin manifest compatibility range;
- agent stop/cancel, hard delete, reload, or post-creation setting updates;
- `PaseoAgentHandle.respondToPermission`;
- `PaseoAgentTimelineHandle.append`;
- a stable `addSlashCommand` contribution;
- SDK roots for terminals, schedules, workspace scripts, or native notifications;
- plugin-owned durable storage or a general native navigation API.

The SDK does expose `agent.archive()`, documented to soft-delete the agent and close its runtime. Director may treat it as the candidate termination mechanism only after `dir-m0.2` proves interruption, persistence, workspace independence, idempotency, and recovery.

## Documentation and source skew

The deployed docs marked v0.7 as current and v0.8 as preview on 2026-09-06. The public Paseo Git `main` ref was `78b285059f6ebd0b257c98bd191df4626721270a` during the audit. The website's source links point to unversioned `main`, not an immutable npm artifact.

At that ref, files still declared package version `0.7.2` while containing changes absent from the published `0.7.2` tarball:

- the SDK docs and `main` source contain `respondToPermission` and timeline `append`, while npm declarations do not;
- v0.7 docs describe a timeline-transform `phase` and optional replacement `id`, while npm declarations do not;
- docs describe a slash-command contribution absent from npm `PluginContext`;
- the `main` plugin package exports `./provider` and `./acp`, while the published/installed package exports only `.`, `./server`, `./react-native`, and `./host`.

The v0.8 preview replaces mixed `index.ts` with separate client/server entries and says not to retain a compatibility entry. Director must treat v0.8 as a separate migration and release line until it becomes stable and passes M0-equivalent evidence.

## Reproduction commands

These commands reproduce the non-destructive audit on a supported host. Never print a daemon password or complete daemon config into evidence.

~~~sh
paseo --version
paseo daemon status --json
paseo plugin --help
paseo agent --help
paseo workspace --help
paseo provider ls --json
npm view @getpaseo/cli version dist.integrity --json
npm view @getpaseo/plugin version dist.integrity --json
npm view @getpaseo/client version dist.integrity --json
npm view @getpaseo/protocol version dist.integrity --json
git ls-remote https://github.com/getpaseo/paseo.git refs/heads/main
paseo plugin init --id director-audit /tmp/director-m0-1.PROBE --json
cd /tmp/director-m0-1.PROBE
npm install --ignore-scripts
# Copy api-contract-probe.ts into this generated directory.
npm run typecheck
~~~

Authenticated runtime reproduction uses the documented MCP calls `list_workspaces`, `list_agents`, `get_agent_status`, `list_providers`, `inspect_provider`, and `list_models`. A password-protected headless daemon correctly rejects unauthenticated CLI data queries; use an authorized client rather than bypassing authentication.

## Primary references

- <https://paseo.sh/docs/plugins>
- <https://paseo.sh/docs/plugins/v0.7/reference>
- <https://paseo.sh/docs/plugins/v0.8/migration>
- <https://paseo.sh/docs/sdk/reference>
- <https://paseo.sh/docs/sdk/workspaces>
- <https://paseo.sh/docs/sdk/agents>
- <https://paseo.sh/docs/sdk/providers>
- <https://paseo.sh/docs/mcp>
- <https://paseo.sh/download>
