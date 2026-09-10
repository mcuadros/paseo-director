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

- provider discovery: `5b504ccc87ab1600953edb007e67d7e8f1acec21e140f08ea4478470479163b7`;
- frozen effective profiles: `c2883458d6c1f69dfd729d4eb7b2aab71a50a5b46f7aaaca9cad76d9a8dffd0c`;
- Organizer configuration version 1: `74f76edc3a0cc01fbebe4f4ecacea0e7b6287d2a492847fc96ece0ba76a58fd9`.

Tests pin these canonical values. Any intentional schema change must update the
version or its pinned hash and the corresponding contract documentation.

The M3.1 frozen-contract audit remains byte-identical except for the in-scope
Run state contract. `engine/domain/execution/state.go` moves from
`157589c5921dcae1bdee163457722ecdcc2c0e0063aa9fc8ff8303fc8f0a93fb` to
`ed3051c4bda72f2807e9ecdfc13c3a851ae73127c1878c32c0cdd74f286f54de`
because it now persists the immutable effective-profile bytes and their
SHA-256. The restart and mutation tests verify those fields structurally; all
other audited M3 contract digests are unchanged.

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
tool implementations remain owned by M3.3.
