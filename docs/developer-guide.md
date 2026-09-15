# Developer guide

Director Engine is standalone Go software. Director for Paseo is the
TypeScript/Node UI and policy-free host integration. A Go bootstrap owns the
portable runtime. A new host implements the engine-owned contracts; it does not
copy reducers or lifecycle policy.

## Repository boundaries

- `engine/domain`, `engine/reducer`, and `engine/projection` are pure,
  schema-versioned behavior.
- `engine/application` owns typed use cases and orchestration.
- `engine/ports` owns external contracts; `engine/adapters` observes or
  performs one admitted effect.
- `engine/cmd/director-bootstrap` owns channel preparation, private XDG state,
  host identity, TaskStore bootstrap, child supervision, and controlled
  handoff.
- `connector` and `rpc` translate exact Paseo `0.7.2` handler facts and strict
  RPC without runtime or workflow policy.
- `ui` uses React Native and Paseo chrome/theme.
- `generated` contains Engine-generated TypeScript and must not be hand-edited.

The product direct-Dolt TaskStore is unrelated to this repository's Beads
development database.

## Deterministic local setup

Development supports Linux amd64 with glibc and requires Node.js 22 or newer,
npm with lockfile-version-3 support, exact Go `1.26.5`, Git, and Dolt `2.3.2`
for applicable integration tests.

```console
npm ci --ignore-scripts --no-audit --no-fund
npm run docs:check
npm run public:check
npm run typecheck
npm run contract:check
npm run architecture:check
```

Use focused checks while authoring. GitHub PR CI is the one authoritative
complete Candidate validation and runs `npm run ci`, including the maintained
published-runtime gate. Package lifecycle scripts remain disabled.

Default-`main` add/update is the only installed-host compilation boundary. It
builds one exact bootstrap and one exact Engine from the clean selected
Candidate with fixed Go and readonly-module arguments. Reload never compiles.
A tagged release uses a precompiled bootstrap/Engine/notices closure and never
falls back to source. Activating a connector change uses plugin-scoped reload;
it must not stop or restart Paseo or interrupt unrelated agents/workspaces.

## Generated contracts and APIs

The Engine owns four public schema boundaries:

- `engine/ports/host/host-interface.v1.json` →
  `generated/host-contract.shared.ts` for fixed connector capabilities,
  observations, Worker labels, and handler-scoped authority descriptors.
- `engine/ports/planning/planning-surface.v1.json` →
  `generated/planning-contract.shared.ts` for Home, native onboarding,
  Board/List, Task detail, Doctor/Repair, Operations, and planning commands.
- `engine/domain/agentbridge/schemas/director-agent-mcp.v1.json` →
  `generated/agent-mcp-contract.shared.ts` for Run-scoped role tools.
- `engine/domain/projectadmin/schemas/director-project-admin-mcp.v1.json` →
  `generated/project-admin-mcp-contract.shared.ts` for own-Project
  administration sessions.

Regenerate after an intentional Engine-schema change:

```console
npm run contract:generate
npm run contract:check
```

`contract:check` regenerates private temporary output and requires
byte-for-byte equality. Runtime requests exchange the exact contract version
and canonical SHA-256 and fail closed on drift.

The maintained [planning query](../examples/api/planning-query.json) and
[Task-create command](../examples/api/task-create.json) are parsed by the
generated Zod schemas. A mutation example illustrates the connector transport;
it cannot manufacture server-derived human identity or lifecycle authority.

## HTTP and Paseo RPC boundary

Engine planning endpoints include `/v1/planning/home`, `/query`,
`/task-detail`, `/doctor`, `/operations`, `/operations-mutate`,
`/organizer-bootstrap`, `/repair`, and `/mutate` under the same
`/v1/planning` prefix. Native Paseo Project selection is a separate strict
plugin RPC backed by the handler-scoped public API. Agent and Project-admin MCP use separate
authenticated Engine endpoints. Requests are strict size-bounded JSON;
redirect, origin, contract, host, scope, actor, duplicate-key, unknown-field,
and echoed-response drift is rejected.

The plugin-owned bootstrap confines Engine/Dolt listeners to loopback and an
owner-only authenticated control socket. Browser/client input is not host or
actor authority. The connector makes bounded calls without hidden retry or
fallback. `tests/fixtures` never becomes runtime authority.

## Example Organizer contract

The fixture under `examples/organizer` is the advanced-import example. The
authoritative Go configuration parser checks its complete schema and semantics;
maintained tests also prove multi-repository identity, explicit references,
example-only paths/remotes, and absence of secret-shaped content. Its skill,
template, decision, and specification are ordinary Organizer data and are not
executed merely because they exist.

When configuration changes, update the parser, JSON Schema, generated
consumers, example, docs, and tests in one Candidate. Never add YAML as a
second format or silently repair an unknown field.

## Contribution workflow

Follow [CONTRIBUTING.md](../CONTRIBUTING.md), `AGENTS.md`, and the repository
skills. One top-level Paseo Task Agent owns one claimed Beads Task in its
isolated worktree. Helpers do not claim Tasks. A separate parentless Reviewer
examines one exact Candidate in a detached read-only checkout.

The Task Agent creates one focused Conventional Commit with exactly one
`Beads:` trailer, runs focused author checks, writes private mode-`0600`
validation/evidence, and performs coordinator-native `review-handoff`. It does
not push, mutate a PR, start complete CI, launch a Reviewer, integrate, clean
lifecycle resources, or close the Task.

Every Review covers acceptance, correctness, security, maintainability,
readability, design, quality, and rigor. A changed Candidate or base needs
fresh remote CI and independent Review. Human P2 acceptance, permission
expansion, ambiguous/manual gates, history rewrite, and out-of-policy
destruction remain explicit human decisions.
