# M0.3 session-scoped MCP provider-matrix evidence

- **Beads Task:** `dir-m0.3`
- **Captured:** 2026-09-06
- **Starting repository base:** `3b15f1a11b9c334addc0451223a0d72885f5570e`
- **Evidence topology:** one isolated headless Paseo daemon, one loopback SDK
  client at a time, auth-only provider homes, one disposable no-remote Git
  repository, and session-unique local stdio MCP processes
- **Evidence class:** live provider sessions plus immutable installed-artifact
  and primary-source inspection

This evidence distinguishes configuration sent, live capability reported,
protocol behavior observed by the receiving MCP/ACP fixtures, public terminal
state, and inference. Provider credential values were not read into the
harness, printed, hashed, or retained. Machine-local process, session, agent,
account, and server identifiers are omitted.

Every disposable probe uses a top-level public `agents.create` call with the
parent omitted. These are bounded capability probes, not product Task Agents
or Reviewer Agents; ADR-0010's exact-title, ownership, and independent-review
rules remain unchanged, and this matrix makes no helper-subagent claim.

## Falsifiable method

The probe MCP server advertises exactly one tool:
`read_scope_nonce`. Its result is:

```text
DIRECTOR_SCOPE_OK row=<row> nonce=<row-nonce> tools=read_scope_nonce
```

The server appends a JSONL event for process start, every received MCP method,
the exact advertised catalog, every accepted or rejected call, and stdin
closure. A passing row requires all of:

1. public provider discovery is `ready` for the selected model;
2. the returned live agent capability has `supportsMcpServers: true`;
3. the MCP transcript contains `initialize`, `tools/list`, and exactly one
   `tools/call` for `read_scope_nonce`;
4. `tools/list` returns only `read_scope_nonce`;
5. the public run result is `idle`, has no error or pending permission, and
   contains the exact nonce returned by the probe;
6. no out-of-scope method or tool call reaches the probe; and
7. the agent is archived and all child processes terminate.

The deterministic compatible ACP fixture does not merely echo `session/new`.
During its prompt it spawns the supplied stdio server, performs the MCP
handshake, verifies the one-tool catalog, calls the tool, and forwards the
observed result through ACP `session/update` plus the terminal response.

## Environment

```text
$ paseo --version
0.7.2

$ codex --version
codex-cli 0.147.0

$ claude --version
2.1.258 (Claude Code)

$ opencode --version
1.18.18

$ node --version
v26.7.0

$ uname -srm
Linux 6.12.107+deb13-amd64 x86_64

$ git -C <paseo-v0.7.2-checkout> rev-parse HEAD
9400a49af670fdb5db4af58e73f8df98588dbea9

$ git -C <paseo-v0.7.2-checkout> tag --points-at HEAD
v0.7.2
```

The online docs were read on 2026-09-06, but the exact installed 0.7.2 artifact
and observed Linux runtime behavior control this decision when the moving docs
differ.

## Primary sources

| Source | Relevant contract |
|---|---|
| [Providers with the SDK](https://paseo.sh/docs/sdk/providers) | Public agent configuration carries session-scoped `mcpServers` and exact MCP `toolPolicy`; installed provider/model IDs must be discovered. |
| [SDK reference](https://paseo.sh/docs/sdk/reference) | `agents.create`, provider discovery, live agent capabilities, terminal wait, timeline fetch, and archive are public package-root operations. |
| [Provider options](https://paseo.sh/docs/sdk/provider-options) | Codex, Claude, and OpenCode have strict provider-native options; other providers reject non-empty options; these settings are not a host boundary. |
| [Custom providers](https://paseo.sh/docs/custom-providers) | An ACP stdio agent is configured under `agents.providers` with `extends: "acp"` and an argv command. |
| [Paseo MCP reference](https://paseo.sh/docs/mcp) | Daemon MCP injection is configurable and catalog filtering is not a security boundary. |
| [ACP v1 initialization](https://agentclientprotocol.com/protocol/v1/initialization) | Capability omission means unsupported; client and agent negotiate protocol v1 before session creation. |
| [ACP v1 session setup](https://agentclientprotocol.com/protocol/v1/session-setup) | `session/new` carries the working directory and MCP server list; every ACP agent must support stdio MCP. |
| [Paseo v0.7.2 provider registry](https://github.com/getpaseo/paseo/blob/9400a49af670fdb5db4af58e73f8df98588dbea9/packages/server/src/server/agent/provider-registry.ts) | Exact preapproval contracts exist for Claude, Codex, and OpenCode; ordinary custom ACP uses the unsupported contract. |
| [Paseo v0.7.2 agent manager](https://github.com/getpaseo/paseo/blob/9400a49af670fdb5db4af58e73f8df98588dbea9/packages/server/src/server/agent/agent-manager.ts) | Tool grants must name a same-request MCP server; a created session that reports no external MCP support is closed and rejected. |
| [Paseo v0.7.2 ACP adapter](https://github.com/getpaseo/paseo/blob/9400a49af670fdb5db4af58e73f8df98588dbea9/packages/server/src/server/agent/providers/acp-agent.ts) | The adapter passes normalized session MCP servers when enabled and an empty list when its provider contract declares no support. |

The ACP protocol proves how an MCP list is transported. It has no Paseo
`toolPolicy` concept; that separate guarantee belongs to the Paseo provider
contract and must be tested independently.

## Installed artifact fingerprints

```text
fe4ec3c04fe9903df87b4d218f207b842e1a808b8909df20bacbee8841b04714  server/agent/provider-registry.js
a2da11a86853255f3074e18c60af0b036f63a1e5a202c34513fe6a401db39f4c  server/agent/agent-manager.js
9856856cdb2d88ca89c639e011d94d05bf23878ce19fd04af103bb14dbcb4477  server/agent/providers/acp-agent.js
da459217eff8bde64a9b8902b07061e330384382da080ec828f8730e36462ff7  server/agent/provider-options.js
094da78bf87aea70d3e1f76a1a1996c1d418f6de6584ed69b01125080e3a5e00  server/agent/providers/codex/options.js
4c0a2b6713c8dc7912f451ad9caa52a9ec46a2244ef1f6fd99dd13c4c79e84c0  server/agent/providers/claude/options.js
bdd91290eba92f122ded2e8d21f5c0198f755049b631b1d78e84934eabe53297  server/agent/providers/opencode/options.js
```

Selected installed lines were inspected without using them as product APIs:

```text
agent-manager.js: Provider '<id>' does not support MCP servers
agent-manager.js: toolPolicy preapproval '<server>.<tool>' requires MCP server
                  '<server>' in the same agent request
provider-options.js: Provider '<id>' cannot preapprove exact MCP tools for
                     unattended execution; select Claude, Codex, or OpenCode
acp-agent.js: return supportsMcpServers ? normalizeMcpServers(config.mcpServers) : []
```

## Isolation setup

The daemon was started with a dedicated `PASEO_HOME`, loopback-only listener,
relay off, Web UI off, daemon MCP off, and automatic MCP injection off:

```text
$ paseo daemon start \
    --home <runtime>/paseo-home \
    --listen 127.0.0.1:16783 \
    --no-relay --no-mcp --no-inject-mcp --no-web-ui
Daemon starting in background (PID <omitted>).
Logs: <runtime>/paseo-home/daemon.log
```

The public SDK later returned:

```json
{
  "daemonMcp": {
    "enabled": false,
    "injectIntoAgents": false
  }
}
```

Each native attempt received its own fresh provider home. The failed and
successful explicit OpenCode selections therefore could not populate or depend
on one another's home. Before the corresponding process started, recursive
manifests contained only:

```text
Codex:      .codex/auth.json
Claude:     .claude/.credentials.json
            .claude.json  # locally synthesized onboarding flags; no MCP/project entries
OpenCode:   .local/share/opencode/auth.json
```

The synthesized Claude file contained only:

```json
{
  "hasCompletedOnboarding": true,
  "installMethod": "native",
  "projects": {}
}
```

Authentication files were copied with mode `0600` into the disposable tree so
the session could authenticate without reading the user's provider-global MCP
configuration. Their content was never emitted or inspected by the harness.
The final cleanup deleted the entire disposable tree. This is an experiment
technique, not a production credential design.

The test repository contained one inert README and an initial commit. Its
remote list was empty, its worktree was clean, and it contained no `.mcp.json`,
`AGENTS.md`, `CLAUDE.md`, `paseo.json`, provider settings, product code, Git
credential, or GitHub credential.

This removes inherited configuration as an explanation for the observed MCP
calls. It does not contain a malicious same-user process: absolute host paths,
credentials, executables, sockets, and the network remain reachable unless a
separate OS boundary denies them.

## Reproduction command

Run the complete orchestrator from a clean detached evidence checkout on Linux
x86_64. The exact provider versions listed above must already be installed and
authenticated in their standard homes:

```text
$ node docs/evidence/m0.3/reproduce-matrix.mjs
```

The orchestrator fails if port `16783` is occupied; a reviewer may select a
different dedicated port with `DIRECTOR_MATRIX_PORT`. Nonstandard executable,
client-entry, or authentication-file locations can be supplied through
`DIRECTOR_<PROVIDER>_BIN`, `DIRECTOR_PASEO_CLIENT_ENTRY`, and
`DIRECTOR_<PROVIDER>_AUTH_SOURCE` environment variables. These values are
paths, not credential contents, and are never emitted.

The command performs one deterministic ordered procedure:

```text
discovery
codex
claude
opencode-billing-rejected
opencode
acp-compatible
acp-policy-rejected
acp-unsupported
cleanup
graceful daemon shutdown
exact owner-PID, process-tree, and listener absence checks
nonce-owned temporary-path removal
```

It creates the isolated config and no-remote Git repository, seeds a different
auth-only or empty home immediately before every row, starts the loopback-only
daemon, runs every row through the public package-root harness, archives every
registered agent in `finally`, runs a public cleanup query, and removes the
runtime only after exact ownership and process checks. An assertion failure
still attempts the same agent, daemon, process, listener, and path cleanup. It
never prints or hashes provider credentials or raw daemon/provider logs.

`run-matrix.mjs` advertises `appVersion: "0.7.2"` during the public SDK hello.
Without that compatibility declaration, a 0.7.2 server intentionally filters
custom provider IDs from legacy clients; the first fixture smoke test exposed
that compatibility gate and was discarded before the final matrix.

## Public discovery result

Sanitized `providers.waitForReady()` plus `config.get()` output:

```json
{
  "daemonMcp": { "enabled": false, "injectIntoAgents": false },
  "entries": [
    {
      "provider": "claude",
      "status": "ready",
      "source": "builtin",
      "modelCount": 15,
      "testedModels": ["claude-haiku-4-5"],
      "testedModelsPresent": true
    },
    {
      "provider": "codex",
      "status": "ready",
      "source": "builtin",
      "modelCount": 6,
      "testedModels": ["gpt-5.4-mini"],
      "testedModelsPresent": true
    },
    {
      "provider": "opencode",
      "status": "ready",
      "source": "builtin",
      "modelCount": 69,
      "testedModels": ["opencode/gpt-5-nano", "opencode/nemotron-3-ultra-free"],
      "testedModelsPresent": true
    },
    {
      "provider": "probe-acp",
      "status": "ready",
      "source": "custom",
      "modelCount": 1,
      "testedModels": ["fixture"],
      "testedModelsPresent": true
    },
    {
      "provider": "probe-acp-no-mcp",
      "status": "ready",
      "source": "custom",
      "modelCount": 1,
      "testedModels": ["fixture"],
      "testedModelsPresent": true
    }
  ]
}
```

Discovery establishes availability only. It is not counted as MCP acceptance.

## Native-provider results

### Codex

```json
{
  "row": "codex",
  "verdict": "pass",
  "providerStatus": "ready",
  "model": "gpt-5.4-mini",
  "providerHomeManifest": [".codex/auth.json"],
  "liveSupportsMcpServers": true,
  "requestedGrant": "director_scope_codex.read_scope_nonce",
  "result": {
    "status": "idle",
    "error": null,
    "lastMessageContains": "DIRECTOR_SCOPE_OK row=codex nonce=m03-codex-scope-v1 tools=read_scope_nonce"
  },
  "probe": {
    "receivedMethods": ["initialize", "notifications/initialized", "tools/list", "tools/call"],
    "calls": 1,
    "rejectedCalls": 0
  },
  "publicTimelineTool": {
    "name": "director_scope_codex.read_scope_nonce",
    "status": "completed"
  }
}
```

The session options were `approval_policy: "never"`,
`sandbox_mode: "read-only"`, and `web_search: "disabled"`. The successful
exact MCP call therefore could not be explained by approving an interactive
permission request.

### Claude Code

```json
{
  "row": "claude",
  "verdict": "pass",
  "providerStatus": "ready",
  "model": "claude-haiku-4-5",
  "providerHomeManifest": [".claude.json", ".claude/.credentials.json"],
  "liveSupportsMcpServers": true,
  "requestedGrant": "director_scope_claude.read_scope_nonce",
  "result": {
    "status": "idle",
    "error": null,
    "lastMessage": "DIRECTOR_SCOPE_OK row=claude nonce=m03-claude-scope-v1 tools=read_scope_nonce"
  },
  "probe": {
    "receivedMethods": ["initialize", "notifications/initialized", "tools/list", "tools/call"],
    "calls": 1,
    "rejectedCalls": 0
  },
  "publicTimelineTools": [
    { "name": "ToolSearch", "status": "completed" },
    { "name": "mcp__director_scope_claude__read_scope_nonce", "status": "completed" }
  ]
}
```

The installed adapter emitted a diagnostic that the translated bare
`allowedTools` entry auto-approved this exact MCP tool before its permission
callback. The native `ToolSearch` helper remained available and was used to
load/discover the MCP tool. It is not a second MCP server or an MCP scope
expansion, but it prevents any claim that the policy is a total provider-tool
boundary.

### OpenCode successful explicit model

```json
{
  "row": "opencode",
  "verdict": "pass",
  "providerStatus": "ready",
  "model": "opencode/nemotron-3-ultra-free",
  "providerHomeManifest": [".local/share/opencode/auth.json"],
  "liveSupportsMcpServers": true,
  "requestedGrant": "director_scope_opencode.read_scope_nonce",
  "result": {
    "status": "idle",
    "error": null,
    "lastMessage": "DIRECTOR_SCOPE_OK row=opencode nonce=m03-opencode-scope-v1 tools=read_scope_nonce"
  },
  "probe": {
    "receivedMethods": [
      "initialize",
      "notifications/initialized",
      "tools/list",
      "tools/call",
      "notifications/cancelled"
    ],
    "calls": 1,
    "rejectedCalls": 0
  },
  "publicTimelineTool": {
    "name": "director_scope_opencode_read_scope_nonce",
    "status": "completed"
  }
}
```

OpenCode ran with bare `permission: "deny"`; the Paseo `toolPolicy` grant was
the explicit exception. Across clean ordered reproductions, the same unchanged
provider/model selection reported one or two retryable 502 overloads before
completing. No fallback field or second model was involved in any run.

## MCP receiver transcripts

PIDs are removed. The complete semantic transcript for each admitted native
row was:

```jsonl
{"source":"mcp","row":"codex","event":"process_started","advertisedTools":["read_scope_nonce"]}
{"source":"mcp","row":"codex","event":"received","method":"initialize","requestId":0}
{"source":"mcp","row":"codex","event":"received","method":"notifications/initialized","requestId":null}
{"source":"mcp","row":"codex","event":"received","method":"tools/list","requestId":1}
{"source":"mcp","row":"codex","event":"responded","method":"tools/list","advertisedTools":["read_scope_nonce"]}
{"source":"mcp","row":"codex","event":"received","method":"tools/call","requestId":2,"toolName":"read_scope_nonce"}
{"source":"mcp","row":"codex","event":"called","toolName":"read_scope_nonce","resultText":"DIRECTOR_SCOPE_OK row=codex nonce=m03-codex-scope-v1 tools=read_scope_nonce"}

{"source":"mcp","row":"claude","event":"process_started","advertisedTools":["read_scope_nonce"]}
{"source":"mcp","row":"claude","event":"received","method":"initialize","requestId":0}
{"source":"mcp","row":"claude","event":"received","method":"notifications/initialized","requestId":null}
{"source":"mcp","row":"claude","event":"received","method":"tools/list","requestId":1}
{"source":"mcp","row":"claude","event":"responded","method":"tools/list","advertisedTools":["read_scope_nonce"]}
{"source":"mcp","row":"claude","event":"received","method":"tools/call","requestId":2,"toolName":"read_scope_nonce"}
{"source":"mcp","row":"claude","event":"called","toolName":"read_scope_nonce","resultText":"DIRECTOR_SCOPE_OK row=claude nonce=m03-claude-scope-v1 tools=read_scope_nonce"}

{"source":"mcp","row":"opencode","event":"process_started","advertisedTools":["read_scope_nonce"]}
{"source":"mcp","row":"opencode","event":"received","method":"initialize","requestId":0}
{"source":"mcp","row":"opencode","event":"received","method":"notifications/initialized","requestId":null}
{"source":"mcp","row":"opencode","event":"received","method":"tools/list","requestId":1}
{"source":"mcp","row":"opencode","event":"responded","method":"tools/list","advertisedTools":["read_scope_nonce"]}
{"source":"mcp","row":"opencode","event":"received","method":"tools/call","requestId":2,"toolName":"read_scope_nonce"}
{"source":"mcp","row":"opencode","event":"called","toolName":"read_scope_nonce","resultText":"DIRECTOR_SCOPE_OK row=opencode nonce=m03-opencode-scope-v1 tools=read_scope_nonce"}
```

No transcript contained `rejected_tool_call`, another advertised tool, a
second call, or the built-in `paseo` MCP server.

## ACP results and exact exclusions

### Compatible ACP, MCP only

```json
{
  "row": "acp-compatible-mcp-only",
  "verdict": "pass",
  "provider": "probe-acp",
  "model": "fixture",
  "providerHomeManifest": [],
  "liveSupportsMcpServers": true,
  "result": {
    "status": "idle",
    "error": null,
    "lastMessage": "DIRECTOR_SCOPE_OK row=acp-compatible nonce=m03-acp-scope-v1 tools=read_scope_nonce"
  },
  "probe": {
    "receivedMethods": ["initialize", "notifications/initialized", "tools/list", "tools/call"],
    "calls": 1,
    "rejectedCalls": 0
  },
  "acp": {
    "mcpServerCount": 1,
    "mcpServerNames": ["director_scope_acp"],
    "promptCount": 1,
    "completedCalls": 1
  }
}
```

Sanitized ACP receiver log:

```jsonl
{"source":"acp","mode":"compatible","event":"process_started"}
{"source":"acp","mode":"compatible","event":"received","method":"initialize","requestId":0}
{"source":"acp","mode":"compatible","event":"received","method":"session/new","requestId":1}
{"source":"acp","mode":"compatible","event":"session_created","mcpServerCount":1,"mcpServerNames":["director_scope_acp"]}
{"source":"acp","mode":"compatible","event":"received","method":"session/prompt","requestId":2}
{"source":"acp","mode":"compatible","event":"prompt_received"}
{"source":"acp","mode":"compatible","event":"mcp_call_completed","tools":["read_scope_nonce"],"resultText":"DIRECTOR_SCOPE_OK row=acp-compatible nonce=m03-acp-scope-v1 tools=read_scope_nonce"}
```

### Compatible ACP plus required exact policy

```json
{
  "row": "acp-compatible-with-exact-tool-policy",
  "verdict": "fail-closed-excluded",
  "observedError": "Provider 'probe-acp' cannot preapprove exact MCP tools for unattended execution; select Claude, Codex, or OpenCode",
  "providerHomeManifest": [],
  "acpProcessEvents": 0,
  "mcpProcessEvents": 0,
  "registeredAgentCount": 0,
  "promptCount": 0,
  "fallbackCount": 0
}
```

This rejection occurs during public agent configuration validation, before
provider availability probing or process launch. The MCP-only pass therefore
does not admit ordinary custom ACP for Director's governed unattended profile.

### Provider declared without MCP support

```json
{
  "row": "acp-declared-unsupported-mcp",
  "verdict": "fail-closed-excluded",
  "observedError": "Provider 'probe-acp-no-mcp' does not support MCP servers",
  "providerHomeManifest": [],
  "acp": {
    "processStarted": true,
    "sessionCreated": true,
    "receivedMcpServerCount": 0,
    "promptCount": 0
  },
  "mcpProcessEvents": 0,
  "registeredAgentCount": 0,
  "fallbackCount": 0
}
```

The exact boundary matters: Paseo starts and initializes the ACP process and
sends `session/new` with `mcpServers: []`; after session construction the agent
manager closes it and rejects the create request. It fails before a governed
prompt, MCP child, registered public agent, or fallback, but not before process
start. Director must use pre-certified exact provider tuples for Task preflight
rather than partially launching a Task to discover this fact.

## Explicit OpenCode model failure, not fallback

`opencode/gpt-5-nano` was separately selected to test the advertised catalog
entry. It produced:

```json
{
  "row": "opencode-explicit-gpt-5-nano",
  "verdict": "failed-no-fallback",
  "model": "opencode/gpt-5-nano",
  "providerHomeManifest": [".local/share/opencode/auth.json"],
  "liveSupportsMcpServers": true,
  "result": {
    "status": "error",
    "error": "No payment method; billing URL redacted",
    "lastMessage": "[redacted provider error]"
  },
  "probe": {
    "receivedMethods": ["initialize", "notifications/initialized", "tools/list"],
    "calls": 0
  },
  "fallbackCount": 0
}
```

No MCP call occurred and no alternative model ran. The later
`opencode/nemotron-3-ultra-free` pass was a new command with a different,
explicit model variable after the prior attempt had terminated and been
archived.

## Fallback and authority findings

- Every public `agents.create` named exactly one provider/model. The SDK has no
  fallback parameter in these calls.
- Rejected ACP rows produced zero prompt and MCP-call events; no other provider
  process was launched by the harness.
- The OpenCode 401 preserved the exact failed model. The later model selection
  was explicit and separately evidenced.
- Every successful OpenCode 502 retry preserved provider, model, permission
  policy, MCP server, and nonce; it was a retry sequence, not fallback.
- This evidence supports an empty default fallback chain. It does not authorize
  any particular future chain.

The custom MCP catalog is exact. The total native agent tool catalog is not:
Claude visibly used `ToolSearch`, and the provider processes still run as the
daemon user. Consequently this matrix proves scoped Director command ingress,
not engine-only host authority, filesystem confinement, credential isolation,
or protection from a malicious provider process. ADR-0008 remains authoritative.

## Cleanup evidence

Every successful or failed live agent that reached registration was archived
in a `finally` block. A public SDK query filtered by the Task label returned:

```json
{
  "row": "cleanup",
  "activeAgentCountBeforeCleanup": 0,
  "archivedByCleanup": 0,
  "activeAgentCount": 0,
  "archivedAgentCount": 5,
  "allHistoricalRowsArchived": true
}
```

The isolated fresh run registered exactly Codex, Claude, both explicit OpenCode
attempts, and the compatible ACP attempt. Both rejected ACP attempts registered
zero agents. The row-level `finally` archives left nothing for the cleanup
action to archive.

Before daemon shutdown, an exact `/proc` scan found no provider-home, MCP-probe,
or ACP-probe process. Every remaining runtime-associated PID formed one parent
chain rooted at the owner PID returned by public `paseo daemon status`: Paseo
Supervisor, Paseo Daemon, and one Node worker. Machine-local PIDs are omitted.
After the graceful lifecycle command, status had no owner PID, the exact prior
owner PID was absent, the listener refused connections, and the runtime scan
was empty:

```text
before stop: 3 Paseo-owned processes; 0 provider/probe processes
after stop:  0 runtime-associated processes; listener closed
```

The runtime directory was deleted only after its canonical parent, generated
prefix, nonce marker, empty process set, and closed listener were rechecked.
The final path check returned absent. No shared daemon, provider config,
repository, Git ref, GitHub state, or user credential source was modified.

## Fixture hashes

```text
6a2baa520d96e57538f09ae0b441f3b19b9cfbac71c7221758e39def215bcf75  probe-mcp.mjs
e3017065e67eaa351d2325ba4923c58e1ebd8e616b93a93d574ea760a896a326  probe-acp.mjs
142f920a8c49b0cec1c8027cceb91608679d14b8d3a9b851682a77f6f212220a  paseo-config.example.json
4f94133f4670551510e3bbed3006e5f0a7dec7b38c901ef16f968db1f68b7165  run-matrix.mjs
99206bcc3f770c5aa428cc00e3a7792fbf06bab5e64c62d4ff3546e9a37f9a98  reproduce-matrix.mjs
```

These hashes bind the evidence run before the documentation commit. They must
be regenerated if any fixture changes; a mismatch invalidates the recorded
matrix until rerun.

## Result

The experiment supports a scoped **Go** for the three native provider/CLI
compatibility points. Ordinary custom ACP is excluded from governed unattended
runs on Paseo 0.7.2 because exact MCP tool preapproval fails closed even though
ACP session MCP transport itself works. A declared no-MCP provider is excluded
before any Task prompt or MCP call. No fallback is implicit, and no security or
platform claim extends beyond this evidence topology.
