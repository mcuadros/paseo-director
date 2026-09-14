# Paseo 0.7.2 Project-admin MCP lifecycle evidence

Observed installed package: exact public Paseo `0.7.2`, 2026-09-14.

## Public API observations

- `@getpaseo/client/dist/index.d.ts:125-155` defines
  `PaseoAgentSessionConfig` and exposes `mcpServers` and `toolPolicy` through
  `PaseoAgentConfig`, consumed by `agents.create`.
- `@getpaseo/client/dist/index.d.ts:205-256` and its public implementation
  `dist/index.js:227-256` expose refresh, send/run, commands, archive, detach,
  and subscribe on an existing agent handle. No configuration or MCP mutation
  exists.
- `@getpaseo/plugin/dist/contracts.d.ts:206-255` gives a native agent command
  trusted Agent and Workspace snapshots plus public `PaseoApi`. Its server
  handler context exposes `PaseoApi` and no session configurator or
  interceptor.
- Paseo's global protocol configuration only controls the built-in Paseo MCP;
  it is not a supported per-session plugin-server injection boundary.

## Conclusion

An existing live session cannot acquire a new Director MCP through Paseo
`0.7.2` public APIs. The supported implementation creates a replacement agent
through public `agents.create` with exact per-session `mcpServers` and
`toolPolicy`, registers the actual returned native Agent against the refreshed
native Workspace in Director Engine, then starts the first turn. The exact
user instruction is maintained in `rpc/project-admin.shared.ts` and ADR-0024.

The existing `director.agent-mcp/v1` implementation is structurally Run scoped:
`engine/domain/agentbridge/contract.go:97-121,365-394` binds Project,
Workspace, Task, and Run, while
`engine/cmd/director-engine/agent_mcp_http.go:65-103` rebuilds authority from a
Run. Project administration therefore uses a separate contract, application
service, HTTP ingress, stdio proxy, Project-level session ledger, and receipt
ledger. Worker and Reviewer v1 behavior is unchanged.

## Composition provenance

The historical provisional Candidate
`e6355d46e59dd0210ccd275b7bcf3b8526348a99` (tree
`a267bbc088240bd974b4c92b1e6cf2b237d25683`) was produced as one commit over
frozen base `d70df1438236bafa11e208f513ef770c124c4d76`; its raw diff SHA-256 was
`036f48fdbf0403be4f773fdb3d69fd6480192a1373fc1ba1b7f24530f38c3b09`.
The final composition rebases that implementation once onto exact main merge
`9648a4c395439cf76a437dd0104c3e4b84e6595d`, preserving M6.22's public local
Paseo Agent-MCP lifecycle reader and owner-only credential-file authority.
That first final Candidate was
`20b6c3cdf9488a6a6d53521f5bc94be1fd90bc08` (tree
`ad60561866890b579a49d201ab80442525e96e56`, raw diff SHA-256
`df65fbd1b3a609a30196a6c1ba0ef4f8ae88993ea8cf5df0554a007f4ccc8b46`).
Its sole remote workflow `34796875921` on PR `#94` terminated after the host
suite passed 142 of 143 tests: the maintained client-bundle catalog expected
the pre-M6.21 command list even though the real bundle correctly registered
`create-director-administration-session` once. The replacement Candidate keeps
the exact base above and strengthens that regression without changing runtime
behavior. Both earlier Candidates remain historical evidence.

## Isolated runtime observation

An isolated installed Paseo `0.7.2` daemon was started with an owner-only
temporary home and the current plugin build. Its authoritative daemon log
reported `daemonVersion: "0.7.2"`, loaded plugin `director`, and listed
`director.recreate-project-admin-session` among the registered methods before
reporting `Plugin ready`. The isolated daemon and plugin were then stopped; the
installed user daemon was not restarted or modified.

The final composition used a fresh passwordless loopback-only Paseo `0.7.2`
home, a disposable Git clone of the exact Candidate, and a fresh headless
Chrome profile. Paseo loaded the real `director` plugin and an ordinary
read-only agent in a disposable native Project/Workspace. The real agent
Command Center rendered `Create Director administration session` alongside
the existing Director actions at a `1440x1000` viewport. The screenshot was
visually inspected; it showed the exact action once, with no browser/console
error and no credential, private path, or private endpoint. Its Candidate,
tree, source hashes, viewport, PNG digest, and inspection result are retained
in an owner-only mode-`0600` manifest outside Git.
