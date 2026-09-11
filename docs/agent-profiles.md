# Agent profiles and provider discovery

The standalone Director Engine owns profile validation and selection for three
roles:

- `organizer` configures an optional planning context. The Organizer remains a
  repository/configuration aggregate, not an agent, and this profile has no
  lifecycle capability.
- `worker` configures the one Task Agent for a Run.
- `reviewer` configures the independent read-only Reviewer Agent.

Each profile names one primary provider/model/effort/mode/permission tuple,
closed non-secret provider options, and a role-filtered MCP capability set.
`fallbackChain` is a required ordered array and is empty by default. Director
never inserts a default provider, model, permission, option, or fallback.

The closed discovery contract is
`director.provider-discovery/v1`. A provider adapter reports only normalized
facts: exact Paseo and CLI versions, availability, discovered models, exact
variants, bounded diagnostic codes, and the three MCP proof booleans. It has no
field for raw provider stderr, credentials, policy, or a selected fallback.
The exact ADR-0005 compatibility points are Codex CLI `0.147.0`, Claude Code
`2.1.258`, and OpenCode `1.18.18` on Paseo `0.7.2`; every selected model and
variant must also be present in the fresh discovery snapshot with session
stdio MCP, exact tool policy, and runtime probe evidence.

The canonical schema hashes for this contract revision are:

- provider discovery: `b24f996491a706c9a1da957dc802bc971f0cdbbd3f0b3d8214546832b03eba6e`;
- frozen effective profiles: `c961897556e407ca8b817522f671b61265574f8a6eabb2ca09d5039528f4b384`;
- Organizer configuration version 1: `c8fce2640c419a6f2473a4466ca4e1fd905ff14304975b0ddd685df0d5d980c8`.

Tests pin these canonical values. Any intentional schema change must update the
version or its pinned hash and the corresponding contract documentation.

The M3.1 frozen-contract audit remains byte-identical except for the in-scope
Run state contract. `engine/domain/execution/state.go` moves from
`157589c5921dcae1bdee163457722ecdcc2c0e0063aa9fc8ff8303fc8f0a93fb` to
`502dbfd087e23481185d0249721a19b1ffea78801619f673eb6c9ec92f14710c`
because it now persists the immutable effective-profile bytes, helper policy,
durable controlled-helper graph, integrated runtime-budget ledger, and exact
current/historical independent-Review state. The
restart and mutation tests verify those fields structurally.

For each role the engine evaluates the primary and then the configured
fallbacks in array order. It selects the first currently supported and
available exact variant. A missing provider/model/combination, version drift,
unavailability, option mismatch, MCP capability loss, or lost proof produces a
closed redacted explanation. If no explicit entry succeeds, Run creation fails
closed. Retrying an existing Run never changes its selected tuple.

The resolver binds its result to the exact active Organizer revision,
configuration SHA-256, discovery revision, and discovery SHA-256. It persists
canonical `director.effective-agent-profiles/v1` bytes and their SHA-256 in the
Run before preparation can complete. Existing Runs use those immutable bytes
after restart and do not re-resolve when provider discovery or Organizer
configuration changes. A new Run must use fresh facts and a newly applied
Organizer revision.

The discovery revision is the SHA-256 of the normalized, non-temporal
capability facts. Provider, model, variant, option, capability, and diagnostic
sets are ordered canonically before hashing; a timestamp refresh does not hide
capability drift, and reordered equivalent facts do not create a false drift.

MCP capability IDs are policy inputs only. The scoped stdio MCP bridge and its
tool implementations are defined by
[`director.agent-mcp/v1`](agent-mcp.md). Existing Runs retain the exact tools
derived from these immutable profile bytes; later configuration or discovery
drift cannot widen their catalogs.
