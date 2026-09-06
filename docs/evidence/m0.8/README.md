# M0.8 threat-model evidence

- **Beads Task:** `dir-m0.8`
- **Captured:** 2026-09-06
- **Repository base:** `6a18cced112c75b12fc115bde03bb73a3a95ac66`
- **Topology:** one headless Paseo daemon host, one trusted Director plugin
  subprocess, provider agents launched by that daemon, and remote Paseo clients
  acting only as user interfaces
- **Evidence class:** read-only documentation and installed-artifact inspection;
  no product implementation and no live credential, provider, repository, or
  destructive experiment

This evidence distinguishes published documentation, immutable installed
artifacts, observed runtime state, and inference. Online documentation is not
treated as proof that an installed artifact implements the same contract.
Machine-specific paths, addresses, process identifiers, and owner identifiers
are omitted. No credential values were read or recorded.

## Environment

| Component | Observed value |
|---|---|
| Operating system | Debian GNU/Linux 13 (trixie), x86-64 |
| Kernel | Linux 6.12.107 |
| Paseo CLI | 0.7.2 |
| `@getpaseo/client` | 0.7.2 |
| `@getpaseo/plugin` | 0.7.2 |
| `@getpaseo/protocol` | 0.7.2 |
| `@getpaseo/server` | 0.7.2 |
| Node.js | 26.7.0 |
| Git | 2.47.3 |
| GitHub CLI | 2.97.0 |
| Windows execution | Not available in this spike; no Windows behavior is claimed |

The local status command reported a stale daemon PID and an unreachable
daemon. That failure state was preserved rather than starting, restarting, or
mutating a shared daemon. This spike therefore does not claim that a provider
applies a configured sandbox, MCP policy, or wrapper at runtime. Those are
separate M0 proofs.

## Primary sources

All web sources were read on 2026-09-06.

| Source | Version or source revision | Relevant published contract |
|---|---|---|
| [Plugin versions](https://paseo.sh/docs/plugins) | Site labels v0.7 current and v0.8 preview | The plugin API is experimental and version-specific. |
| [v0.7 plugin reference](https://paseo.sh/docs/plugins/v0.7/reference) | `getpaseo/paseo@25defbafb4a9f67d633a850cff9825bf279f800f`, 2026-09-03 | Backend plugin code is trusted and unsandboxed, has daemon-host file/process/credential/network access, and captures stdout/stderr in retained logs. Installing or updating trusts source, dependencies, preparation commands, and future tracked revisions. |
| [Security](https://paseo.sh/docs/security) | `getpaseo/paseo@a8734a972495cf343f628d1017e87775767aade5`, 2026-08-27 | Agents run in the daemon user's context with provider-owned credentials. Scoped container mounts are recommended, and daemon network authentication/encryption remain operator responsibilities. |
| [Provider options](https://paseo.sh/docs/sdk/provider-options) | `getpaseo/paseo@7954babca6f9e62e55b4108a63a6cbbc05ac54e4`, 2026-08-10 | Provider-native sandbox and permission options are not a host boundary; the documented hardening fallback is a container or separate machine. Some options can silently run unsandboxed unless fail-closed settings are selected. |
| [SDK providers](https://paseo.sh/docs/sdk/providers) | `getpaseo/paseo@7954babca6f9e62e55b4108a63a6cbbc05ac54e4`, 2026-08-10 | Agent configuration can carry provider options, a system prompt, session-scoped MCP servers, and exact MCP preapproval rules. |
| [MCP reference](https://paseo.sh/docs/mcp) | `getpaseo/paseo@53c96074791089949f6917cfa55db1d11f212521`, 2026-09-03 | Built-in tools can create or mutate agents, workspaces, terminals, schedules, permissions, and provider state. Per-provider catalog filtering is documented but explicitly is not a security boundary for an agent with shell access. |
| [Custom providers](https://paseo.sh/docs/custom-providers) | `getpaseo/paseo@53c96074791089949f6917cfa55db1d11f212521`, 2026-09-03 | A provider command may be replaced by an argv array and may invoke a wrapper or Docker image. This is a possible isolation integration point, not proof that an isolated Director profile works. |
| [Git worktrees](https://paseo.sh/docs/worktrees) | `getpaseo/paseo@a39959219fc5f4e7f06d4f13f57f9fc55641bdb8`, 2026-08-11 | Committed `paseo.json` setup and teardown values are shell scripts run in the worktree, and receive the source-checkout path. Repository-controlled lifecycle commands therefore cross the worktree boundary unless independently constrained. |
| [systemd.exec](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html) | Page last modified 2026-07-23 | Linux service processes can use a distinct user plus mount, home, privilege, and network restrictions. |
| [Docker Engine security](https://docs.docker.com/engine/security/) | Page served 2026-09-06; page metadata updated 2025-07-25 | Linux containers use kernel namespaces/cgroups, and access to the Docker daemon is itself privileged authority that must not be exposed inside an agent container. |
| [Windows restricted tokens](https://learn.microsoft.com/en-us/windows/win32/secauthz/restricted-tokens) | Page last modified 2025-03-12 | A restricted token can reduce access to securable objects and privileged operations; it is not sufficient without appropriate ACL, desktop, process, and network isolation. |
| [Windows AppContainer isolation](https://learn.microsoft.com/en-us/windows/win32/secauthz/appcontainer-isolation) | Source revision `804c358a9c4bce679f7de9c1ef714669cb168959` | AppContainer provides capability-based credential, file, network, process, and window isolation. |
| [Windows container isolation modes](https://learn.microsoft.com/en-us/virtualization/windowscontainers/manage-containers/hyperv-container) | Page last modified 2025-09-17 | Hyper-V-isolated containers use a dedicated kernel and hardware-level VM boundary; process-isolated containers share the host kernel. |

## Reproducible local commands and selected output

### Versions and topology

```text
$ paseo --version
0.7.2

$ node --version
v26.7.0

$ git --version
git version 2.47.3

$ gh --version
gh version 2.97.0 (2026-07-31)

$ uname -srm
Linux 6.12.107+deb13-amd64 x86_64
```

The following package query was run against the packages bundled below the
installed Paseo CLI root:

```text
$ jq -r '[.name,.version] | @tsv' \
    <cli>/package.json \
    <cli>/node_modules/@getpaseo/client/package.json \
    <cli>/node_modules/@getpaseo/plugin/package.json \
    <cli>/node_modules/@getpaseo/protocol/package.json \
    <cli>/node_modules/@getpaseo/server/package.json
@getpaseo/cli       0.7.2
@getpaseo/client    0.7.2
@getpaseo/plugin    0.7.2
@getpaseo/protocol  0.7.2
@getpaseo/server    0.7.2
```

`paseo status --json` was read without changing daemon state. Sanitized result:

```json
{
  "localDaemon": "stale_pid",
  "connectedDaemon": "unreachable",
  "cliVersion": "0.7.2",
  "daemonVersion": null,
  "desktopManaged": false,
  "providers": ["Claude Code", "Codex", "OpenCode"]
}
```

### Installed public declarations

The installed 0.7.2 declarations contain:

- `PaseoAgentConfig.options`, `toolPolicy`, and `mcpServers`;
- stdio MCP `command`, `args`, and `env` fields;
- provider-profile `command`, `env`, and `disallowedTools` fields;
- plugin RPC input/output schemas and a server handler context containing the
  full `PaseoApi`;
- Codex `read-only`, `workspace-write`, and `danger-full-access` modes; and
- Claude `failIfUnavailable`, `allowUnsandboxedCommands`, filesystem, and
  network settings.

The installed provider-configuration declaration does **not** contain the
online documentation's newer `paseoTools`/`disabledTools` policy. The exact
search below returned no matches. This is version-skew evidence, not a claim
about a future stable artifact.

```text
$ rg -n 'paseoTools|disabledTools' \
    <cli>/node_modules/@getpaseo/protocol/dist/provider-config.d.ts
[no matches; exit 1]
```

Relevant artifact hashes:

```text
813d36c47df2268ce97525adfd2a0924f1c4494151f97559c2107722fb5ae145  cli/package.json
8593bfb59e5ce49b4d337283738df00bdd718d477b3108cee72a6b9861731839  client/dist/index.d.ts
fc43accea34aed98f5d7269790a279d1f5e3e471854afc1e34c1f438fc2efaa9  plugin/dist/contracts.d.ts
2a1f31b193068e42e614f3fdf4710d8a24fb12ad658cb5dff0e6c1068e347e1a  protocol/dist/agent-types.d.ts
df160878fa43a0d3b397873e21053b1b8ac2dc3ccf48afc707203b46751b3a16  protocol/dist/provider-config.d.ts
97f626fd8bbfa1e9771dbb97cd2981d5eb5146c1275c2cc5bd097884ebbf8848  server/providers/codex/options.d.ts
8a60def6d032269e0dfc7ea7abd27a89d18f89fd265b136cca0401809e944f06  server/providers/claude/options.d.ts
```

### CLI surface

Selected installed help output:

```text
$ paseo plugin --help
Manage trusted, unsandboxed plugins

$ paseo plugin install --help
Trust and install a plugin from a directory or Git repository

$ paseo run --help
--mode <mode>       Provider-specific mode (for example, bypass)
--cwd <path>        Working directory
--env <key=value>   Agent-process environment entry

$ paseo workspace create --help
--path <path>       Local directory or source checkout
--isolation <...>   local or worktree
--base <ref>        Base ref for branch-off mode
```

These commands demonstrate configuration surface only. They do not prove that
an agent is contained or that a path belongs to Director.

### Documentation retrieval and source metadata

The documentation was retrieved from its markdown endpoints, for example:

```text
$ curl -fsSL https://paseo.sh/docs/plugins/v0.7/reference.md
$ curl -fsSL https://paseo.sh/docs/security.md
$ curl -fsSL https://paseo.sh/docs/sdk/provider-options.md
$ curl -fsSL https://paseo.sh/docs/mcp.md
$ curl -fsSL https://paseo.sh/docs/worktrees.md
$ curl -fsSL https://paseo.sh/docs/custom-providers.md
```

File-specific upstream revisions were resolved with the public GitHub commits
API, for example:

```text
$ curl -fsSL \
  'https://api.github.com/repos/getpaseo/paseo/commits?path=public-docs/mcp.md&per_page=1'
first result: 53c96074791089949f6917cfa55db1d11f212521
author date: 2026-09-03T18:10:47Z
```

The material assertions were then checked against immutable raw files rather
than the moving documentation site:

```text
$ curl -fsSL https://raw.githubusercontent.com/getpaseo/paseo/25defbafb4a9f67d633a850cff9825bf279f800f/public-docs/plugins/v0.7/reference.md |
    rg -n 'trusted and unsandboxed|Trust every plugin|daemon log persists'
25: Plugin code is trusted and unsandboxed ... access to ... files, processes,
    credentials, and network.
872: daemon log persists it.
974: Trust every plugin you add ... preparation commands run unsandboxed ...

$ curl -fsSL https://raw.githubusercontent.com/getpaseo/paseo/7954babca6f9e62e55b4108a63a6cbbc05ac54e4/public-docs/sdk/provider-options.md |
    rg -n 'not a host boundary|failIfUnavailable|full access'
15: Provider options are not a host boundary ... runs as your user ...
59: Setting never without a sandbox mode gives an unattended agent full access.
109: Leave failIfUnavailable off and Claude runs unsandboxed instead.

$ curl -fsSL https://raw.githubusercontent.com/getpaseo/paseo/53c96074791089949f6917cfa55db1d11f212521/public-docs/mcp.md |
    rg -n 'not a security boundary|create_agent|respond_to_permission'
69: Catalog filtering is not a security boundary for shell-capable agents.
89: create_agent can select an existing workspace by workspaceId.
166: respond_to_permission can approve or deny a pending permission request.

$ curl -fsSL https://raw.githubusercontent.com/getpaseo/paseo/a39959219fc5f4e7f06d4f13f57f9fc55641bdb8/public-docs/worktrees.md |
    rg -n 'multiline shell script|PASEO_SOURCE_CHECKOUT_PATH'
111: Setup and teardown fields accept multiline shell scripts or command arrays.
113: Commands can use PASEO_SOURCE_CHECKOUT_PATH to reach the source checkout.
```

Text is shortened only to remove irrelevant prose; line numbers and security
meaning are preserved.

## Evidence-backed findings

1. The Director plugin process is part of the trusted computing base. A
   malicious plugin revision or dependency has the daemon user's authority;
   no in-process validation can contain it.
2. Paseo worktree separation prevents ordinary concurrent path collisions but
   is not a credential, process, network, daemon-control, or repository-policy
   boundary.
3. Provider-native sandboxes and tool policies can reduce accidental or
   prompt-driven behavior only when the exact provider/version applies them
   fail-closed. The vendor documentation explicitly disclaims them as a host
   boundary.
4. A shell-capable same-user agent can attempt `git`, `gh`, `paseo`, direct
   daemon access, credential-store access, or repository lifecycle scripts
   outside the Director MCP. Schema-valid custom MCP does not remove those
   ambient paths.
5. The installed stable API exposes a potential wrapper integration point and
   session-scoped MCP, but this read-only inspection does not prove Linux or
   Windows authority separation. The exact public mechanism must be exercised
   before relying on it.
6. Current web documentation can be newer than the immutable 0.7.2 artifact.
   Capability detection must inspect the installed runtime and fail closed.

## Cleanup

No daemon, agent, provider, credential, branch, remote, repository hook, or
external service was created or mutated for this evidence. Network activity
was limited to read-only official-document requests. The dedicated Task branch
and worktree are delivery resources and are recorded separately at handoff.
