# ADR-0005: Admit the three proven native providers and exclude generic ACP from governed runs

- **Status:** Accepted
- **Date:** 2026-09-06
- **Beads Task:** `dir-m0.3`
- **Plan gate:** M0 per-session MCP provider matrix
- **Decision owner:** `dir-m0.3` Task owner
- **Amended by:** [ADR-0011](0011-linux-only-platform-scope.md), which establishes Linux as the sole `1.0` platform; [ADR-0014](0014-practical-linux-agent-boundary.md), which admits the exact proven native-provider tuples only under the practical trusted-provider boundary and mandatory rootless-OCI defense in depth

## Context

Director must give every governed agent a session-scoped stdio MCP bridge fixed
to one Project, Task, Run, role, and capability set. Provider profiles also
carry an exact MCP tool preapproval policy. The plan forbids silent provider,
model, mode, delivery, or permission fallback and requires an unsupported
provider to be excluded with a precise explanation.

[ADR-0002](0002-paseo-0.7.2-public-surface.md) permits only the verified public
Paseo 0.7.2 SDK subset. A declaration or a `supportsMcpServers` flag is not
enough to admit a provider: the selected provider must actually initialize,
list, and call the session MCP server, and the call must finish without an
unresolved permission. [ADR-0008](0008-director-threat-model.md) separately
establishes that MCP scope and provider tool settings are defense in depth,
not an operating-system or credential boundary.

[ADR-0010](0010-top-level-task-agent-parentage.md) remains authoritative for
Task Agent and Reviewer Agent lifecycle. These disposable capability probes
use top-level `agents.create` calls with the parent omitted, but they are not
product Task Agents or Reviewer Agents and make no helper-subagent claim.

## Question or hypothesis

On exact Paseo 0.7.2 and the observed Linux host, can Codex, Claude Code,
OpenCode, and a compatible generic ACP agent consume a one-tool, session-only
stdio MCP server and honor the exact tool policy without inherited MCP
configuration or implicit fallback?

A row passes only when a live disposable session:

- publicly reports MCP support;
- initializes the session-unique probe server;
- receives a catalog containing only `read_scope_nonce`;
- calls that tool exactly once and returns its non-secret row nonce;
- finishes without a pending permission; and
- is archived, leaving no live evidence agent or probe process.

A provider that cannot meet the MCP and exact-policy requirements must fail
before a governed prompt or MCP call and must not select another provider,
model, or permission mode.

## Acceptance criteria

- Record pass/fail evidence for Codex, Claude Code, OpenCode, compatible ACP,
  and a declared unsupported-MCP provider.
- Prove accepted runtime behavior rather than treating sent configuration as
  evidence.
- Remove built-in Paseo MCP injection and authored provider-global MCP config
  from the experiment.
- State the exact exclusion point and diagnostic for every failed row.
- Preserve an empty fallback chain by default and distinguish retry from
  fallback.
- Preserve ADR-0008's authority boundary.

## Evidence

The complete sanitized transcript, commands, fixture hashes, raw observations,
and cleanup record are in
[the M0.3 evidence bundle](../evidence/m0.3/provider-mcp-matrix.md). The
reproducible fixtures are:

- [`probe-mcp.mjs`](../evidence/m0.3/probe-mcp.mjs), a one-tool stdio MCP
  server that logs protocol acceptance and the exact nonce-bearing call;
- [`probe-acp.mjs`](../evidence/m0.3/probe-acp.mjs), a deterministic ACP v1
  agent that connects to the MCP server supplied in `session/new`; and
- [`paseo-config.example.json`](../evidence/m0.3/paseo-config.example.json),
  the isolated daemon and custom-provider configuration template;
- [`run-matrix.mjs`](../evidence/m0.3/run-matrix.mjs), which uses only the
  public `@getpaseo/client` 0.7.2 package root; and
- [`reproduce-matrix.mjs`](../evidence/m0.3/reproduce-matrix.mjs), the complete
  ordered clean-environment setup, row execution, and cleanup orchestrator.

Observed matrix:

| Row | Exact tested selection | MCP result | Exact tool-policy result | Admission result |
|---|---|---|---|---|
| Codex | Paseo 0.7.2; Codex CLI 0.147.0; `gpt-5.4-mini` | Pass | Pass | Admitted at this exact compatibility point |
| Claude Code | Paseo 0.7.2; Claude Code 2.1.258; `claude-haiku-4-5` | Pass | Pass | Admitted at this exact compatibility point |
| OpenCode | Paseo 0.7.2; OpenCode 1.18.18; `opencode/nemotron-3-ultra-free` | Pass | Pass | Admitted at this exact compatibility point |
| Compatible custom ACP | Paseo 0.7.2; deterministic ACP v1 fixture | Pass without `toolPolicy` | Fail closed before process start | Excluded from governed unattended runs |
| Declared no-MCP custom ACP | Paseo 0.7.2; deterministic ACP v1 fixture with `supportsMcpServers: false` | Fail closed | Not attempted because MCP already fails | Excluded |

For each passing native row, the public provider snapshot was `ready`, the live
agent capability was `supportsMcpServers: true`, the probe observed
`initialize`, `tools/list`, and one `tools/call`, the final public status was
`idle`, and the returned text contained the unique row nonce. Codex ran with
`approval_policy: "never"` and `sandbox_mode: "read-only"`; OpenCode ran with
global `permission: "deny"`; their successful granted calls therefore also
exercise the policy translation. Claude's translated bare `allowedTools` grant
auto-approved the exact MCP tool, and the run returned no pending permission.

The compatible ACP fixture received the exact MCP server in `session/new`,
listed one tool, called it, and returned the nonce. Adding the required
`toolPolicy` produced this public creation error before either fixture process
started:

```text
Provider 'probe-acp' cannot preapprove exact MCP tools for unattended execution;
select Claude, Codex, or OpenCode
```

The declared no-MCP ACP fixture was allowed to initialize and received
`session/new` with an empty MCP list. Paseo then closed it and rejected agent
creation with:

```text
Provider 'probe-acp-no-mcp' does not support MCP servers
```

No prompt, MCP process, tool call, registered agent, or fallback followed. The
process initialization is part of capability probing; it is not a claim that
Paseo rejects this row before launching any process.

OpenCode's separately selected `opencode/gpt-5-nano` attempt returned an
explicit 401 billing error after MCP initialization/listing and before any tool
call. It did not select another model. The successful free-model attempt was a
new explicit run with a fresh provider home, not a fallback. Retryable 502
responses occurred within successful runs and then completed without changing
provider, model, policy, MCP server, or nonce; retrying the unchanged selection
is not a provider/model fallback.

The daemon's public configuration reported both `mcp.enabled: false` and
`mcp.injectIntoAgents: false`. Each native attempt, including both explicit
OpenCode selections, received its own fresh home whose pre-run manifest
contained only its provider authentication file and, for Claude, a minimal
local onboarding record. Each ACP attempt received its own empty home. The
disposable Git repository had no remote, `.mcp.json`, provider settings,
product code, or Git or GitHub credential. The provider credentials were never
printed, hashed, placed in an argument, prompt, log, repository file, or
evidence artifact.

Claude used its native `ToolSearch` helper before the MCP call. This does not
expand the custom MCP catalog, which still contained one tool, but it proves
that `toolPolicy` is not a complete native-tool or host boundary. Native tools
also remain a concern for the other providers even when they were not selected
in these turns. ADR-0008's P0 stop condition is unchanged.

## Alternatives considered

### Trust capability declarations or sent configuration

Rejected. A provider can advertise or accept a configuration object while
ignoring it, failing authentication, presenting a different catalog, asking
for permission, or never calling the tool. The nonce-bearing MCP transcript and
public terminal result are required together.

### Admit all ACP providers because ACP v1 requires stdio MCP

Rejected. The protocol gives ACP clients a standard way to pass MCP servers,
and the compatible fixture proves that path. Paseo 0.7.2 nevertheless rejects
exact MCP preapproval for ordinary custom ACP provider contracts. Enabling a
broader provider-native auto-accept mode would be a permission expansion, not
an equivalent implementation of the frozen profile.

### Fall back from a failed OpenCode model automatically

Rejected. The billing failure is evidence about that exact selection. A later
free-model run was separately selected and recorded. Director's default
fallback chain remains empty; a future ordered chain may contain only rows that
have each been admitted independently and approved in configuration.

### Treat MCP filtering as the agent security boundary

Rejected by ADR-0008 and by this experiment's native-tool observations. The
MCP bridge can constrain Director commands, but a same-user provider process
can still attempt direct filesystem, process, credential, network, Git, GitHub,
or Paseo access unless a later M0 decision proves an OS-enforced boundary.

## Decision

**Go**, for a deliberately narrow native-provider subset.

On exact Paseo 0.7.2 for the tested Linux topology, Director may treat the
following provider adapter/CLI combinations as having a proven public path for
session-scoped stdio MCP and exact tool preapproval:

- Codex CLI 0.147.0;
- Claude Code 2.1.258; and
- OpenCode 1.18.18.

The named tested models are the runtime witnesses for this decision. This ADR
does not generalize model behavior: a model still must be discovered on the
target host, and a release that enables additional model/provider combinations
must retain a reproducible capability probe. Any change to Paseo, provider CLI,
adapter mode, or policy translation invalidates this compatibility evidence
until the matrix is rerun.

Ordinary custom ACP providers are excluded from governed unattended Runs on
Paseo 0.7.2 even when session MCP itself works, because the public contract
cannot apply the required exact `toolPolicy`. A provider declared without MCP
support is excluded with the exact diagnostic above. No implicit fallback is
permitted.

This decision proves only the provider/MCP plan gate. It does not authorize M1,
enable built-in Paseo MCP, resolve the ADR-0008 authority stop condition, or
approve a production credential topology.

## Consequences

- Doctor/preflight must match exact Paseo and provider CLI versions, discover
  the selected model, and fail closed on an unproven tuple.
- The Director MCP server remains the catalog authority: it exposes only the
  tools fixed for the Run scope and accepts no caller-selected scope.
- A live capability probe requires all of public session capability, MCP
  handshake/list/call, expected nonce, terminal result, and cleanup. Sent
  configuration alone never admits a row.
- Generic ACP stays unavailable for governed unattended Runs until a later
  stable public Paseo contract proves exact preapproval or the approved plan
  explicitly changes its permission requirement.
- Unsupported rows produce one exact diagnostic and no task prompt. A Doctor
  probe may initialize a disposable provider process; it must not be confused
  with a partially launched Task Run.
- Fallback chains remain empty by default. Explicit chains must be ordered and
  may reference only independently admitted rows; retries cannot change the
  frozen provider, model, mode, or permission policy.
- MCP scope remains defense in depth. ADR-0014 resolves the separate authority
  decision only for its explicit trusted-engine/trusted-provider Linux boundary
  and mandatory rootless-OCI profile; this MCP result alone still grants no
  host authority claim.

P0 stop conditions for this gate are: an ignored MCP or policy setting; an
unexpected custom MCP server or advertised tool; a missing or mismatched nonce;
an unapproved permission; a prompt or tool call after an unsupported result; a
silent selection change; residual live agent/probe state; or any claim that
this evidence establishes host containment.

## Independent verification

Exact Candidate `b40b9829a25edbd650dccdef2e60b389e662d164` received an
independent `approve_candidate` verdict and was integrated with its reviewed
base as merge `58dba422562c820b60fd1a214b66577b9dbbb83e`. `dir-m0.3`
records the clean-environment matrix reproduction, exact tuple bounds,
review, integration, cleanup, and residual defense-in-depth limits. This
status reconciliation uses that integrated evidence and does not rerun paid
provider sessions.
