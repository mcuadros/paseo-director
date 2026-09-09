# Organizer configuration and revisions

`paseo-director.json` is the discovery marker and the only operational
configuration document in an Organizer repository. The Organizer is the
repository, its configuration and durable Project material. It is not an
agent, does not need a standing conversation, and cannot decide a lifecycle
transition.

The Director Engine owns the closed JSON Schema at
[`engine/domain/configuration/paseo-director.schema.json`](../engine/domain/configuration/paseo-director.schema.json).
The schema version is `1` and requires the exact engine-owned `$schema`
identifier. Unknown fields, duplicate object keys, trailing JSON values,
non-integer numeric spellings, invalid UTF-8, unpaired escaped Unicode
surrogates and unsupported versions are rejected. Both the source document and
its canonical form are limited to 1 MiB so accepted configuration can always be
embedded in and restored from a bounded Run snapshot.

Validation against the published JSON Schema is necessary but insufficient for
engine admission. JSON Schema establishes the portable closed shape and basic
field constraints; the Go semantic validator is authoritative for byte-level
Unicode fidelity, canonical-size limits, Git transports, credential-bearing
userinfo, filesystem/reference paths and cross-field invariants.

## Version 1 document

This is a complete minimal document:

```json
{
  "$schema": "https://github.com/mcuadros/paseo-director/engine/domain/configuration/paseo-director.schema.json",
  "schemaVersion": 1,
  "project": {
    "id": "example",
    "name": "Example Project"
  },
  "workspaces": [
    {
      "id": "product",
      "name": "Product repository",
      "remote": "https://github.com/example/product.git",
      "sourcePath": "/srv/example/product",
      "defaultBaseBranch": "main"
    }
  ],
  "agentProfiles": {
    "taskAgent": {
      "provider": "codex",
      "model": "gpt-5.6",
      "effort": "high",
      "permissionMode": "workspace-write"
    },
    "reviewerAgent": {
      "provider": "opencode",
      "model": "reviewer-1",
      "effort": "high",
      "permissionMode": "read-only"
    }
  },
  "defaults": {
    "launchPolicy": "manual",
    "deliveryMode": "pull_request",
    "limits": {
      "maxActiveTasks": 6,
      "maxActiveTasksPerWorkspace": 2,
      "maxConcurrentAgents": 8,
      "maxSubagentsPerTask": 3
    },
    "runBudget": {
      "elapsedSeconds": 7200,
      "tokens": 200000,
      "turns": 32,
      "ciCycles": 4
    },
    "autoFixCiFailures": true,
    "autoFixReviewFeedback": true
  },
  "workspaceOverrides": [],
  "skills": [
    {
      "id": "commits",
      "path": "skills/commits/SKILL.md"
    }
  ],
  "templates": [
    {
      "id": "task",
      "path": "templates/task.md"
    }
  ]
}
```

Schema checks are followed by deterministic semantic validation. Project,
Workspace and reference identifiers are bounded; the optional Workspace
display name is independent of its stable Project-local ID; Workspace IDs,
canonical repository identities, canonical source paths, and explicit
references are unique inside one Project; source paths are clean absolute
Linux paths; Git branch names are safe; Workspace remotes use only `https://`,
`ssh://`, `git://`, or safe scp-like SSH syntax. HTTPS and Git URL userinfo is
always rejected; SSH URL and SCP userinfo is limited to the closed `git`
username. Password-bearing URL/scp userinfo, Git remote-helper
`token::address` dispatch, and command-bearing or local transports are rejected
while the non-secret SSH form
`git@github.com:owner/repository.git` remains valid and maps to the same
repository identity as an equivalent HTTPS or SSH URL. Remote percent escapes
are decoded once and canonical output re-encodes literal percent data, while
both submitted and canonical output are limited to 2,048 UTF-8 bytes. Every
accepted canonical output reparses to the identical canonical value, key, and
repository ID; an expanding normalization which would cross the limit is
rejected rather than truncated. Repeated `.git` suffix chains, ambiguous IPv4,
and malformed embedded-IPv4 IPv6 forms are rejected; profile tokens and provider families
are closed; capacity and Run budgets are finite and internally consistent;
Workspace overrides name a declared Workspace and select `inherit`
or a concrete value; and skill/template paths are clean relative paths in their
declared Organizer directories. Source and reference paths reject whitespace
and control characters. Directory scanning never turns an unreferenced file
into executable or prompt input.

The Project defaults also require `autoFixCiFailures` and
`autoFixReviewFeedback`; both default to `true` in the product plan but are
explicit in the version 1 document. A Workspace override may independently
replace launch and delivery mode, each capacity limit, each finite Run budget,
and either auto-fix flag. Omitted scalar fields and the literal `inherit` mode
retain the Project value. Task overrides live with the Task rather than in
Organizer Git and use the same field-by-field representation. The engine
resolves every field in the fixed order `Project → Workspace → Task` and
reports the supplying scope with the effective value.

## Preview and Apply

The Go application aggregate keeps `pending` and `active` revisions distinct.
Both mutations use optimistic aggregate versions.

1. `Preview` receives the exact Organizer Git object ID and the configuration
   bytes observed there.
2. The engine hashes the exact submitted bytes into `ContentSHA256`, strictly
   validates and canonicalizes the document, and calculates a fixed-order
   top-level impact. Valid input also receives `ConfigurationSHA256`, the hash
   of its canonical form. Invalid input retains the same raw-byte meaning for
   `ContentSHA256` and remains an invalid pending projection; the active
   revision is unchanged.
3. The preview ID deterministically binds the prior aggregate version, active
   revision and configuration hash, proposed revision, content hash, validation
   result and impact.
4. `Apply` accepts only the exact current preview ID at the expected aggregate
   version with a confirmed server-authenticated human approval bound to the
   proposed Organizer revision and a separate human acknowledgement bound to
   the active Organizer revision the Preview compared (the empty revision for
   initial activation). It reparses no caller-supplied configuration. A stale,
   mismatched, invalid, model/Organizer-authored or unconfirmed command leaves
   both active state and Run inputs unchanged.
5. Successful Apply moves the already validated pending revision to active and
   clears pending state.

The active-revision pointer is dynamic Project state rather than an Organizer
file, avoiding a self-referential commit. Models may propose configuration and
hosts may submit typed commands, but neither can manufacture Apply authority.
The TypeScript package renders engine projections and transports commands; it
does not validate, activate or freeze revisions.

Each revision aggregate is constructed inside an immutable security envelope
established from an exact canonical configuration by a separately authenticated
human confirmation. Its hash is part of every Preview and frozen Run snapshot.
Organizer proposals may tighten that envelope: remove repositories, switch
automatic launch to manual, switch direct delivery to pull request, lower
capacity or budgets, or turn automatic correction off. They cannot add or
retarget a repository, change provider/model/effort/permission authority,
enable a more powerful launch/delivery mode, raise capacity or budgets, or
reenable automatic correction beyond the envelope. Those proposals remain
visible as invalid pending Previews and cannot be activated even if replayed
with an ordinary Apply confirmation. Establishing a broader envelope is a
separate human-owned action, never an Organizer self-activation path.

## Frozen Run snapshots

`FreezeRunConfiguration` reads only the active revision. Before the first
Apply it fails closed. Once an active revision exists, it produces an immutable
snapshot containing:

- snapshot schema version `director.run-configuration-snapshot/v1`;
- the exact active Organizer Git object ID;
- the canonical configuration SHA-256;
- the immutable security-envelope SHA-256;
- the complete canonical version 1 configuration; and
- the complete canonical human-approved envelope configuration needed to
  verify tightened revisions and later Task overrides after restart.

Snapshot accessors return copies. Persisted snapshots are strictly reparsed and
their internal revision format, contract version, configuration validity and
content-hash consistency are verified within the bounded snapshot size. The
trusted TaskStore layer later binds that self-consistent snapshot to its exact
Project and Run. A later Preview or Apply cannot change a snapshot already
assigned to a Run. Thus a valid but unapproved pending revision, and even a
human-confirmed invalid revision, cannot affect a new or existing Run.
`Effective` resolves a frozen Workspace and Task override from that snapshot;
an invalid, inconsistent, unknown-Workspace, or envelope-expanding override is
refused rather than repaired or inherited silently.

## Create and Adopt Organizer

The M1 bootstrap application lives in the standalone Go Director Engine. The
Paseo UI may render its projections and submit typed commands, but neither the
TypeScript connector nor UI validates repositories, interprets approval,
chooses effects, or projects active state.

`PreviewCreate` is read-only. It validates Project identity, configuration,
the absent canonical Organizer target, and every configured product Workspace
path plus exact `origin` remote. It rejects an Organizer path which contains,
or is contained by, a product Workspace. The preview binds a caller-retained
request identity, the canonical configuration hash, the exact generated
ownership marker, README, deterministic non-secret placeholder skill/template
files, their hashes, and the complete ordered operation list. Every explicit
configuration reference is materialized and committed; no unreferenced file is
discovered or injected implicitly.
The minimal M1 Create mode is local-only; remote selection and private GitHub
repository creation remain later bootstrap capabilities and are never silent
fallbacks.

The request identity is a stable idempotency and integrity-correlation value,
not secret authorization. The marker is deliberately committed and may be
guessable. Create therefore requires the target to be absent beneath an
existing canonical parent owned by the engine user and not writable by group
or others. The created root and `.director` directory are mode `0700`, and
recovery from the first effect adopts only a root containing the exact marker
and no other state. A matching marker beside any foreign entry is refused
before Director writes another file. Later phases verify the marker only as a
binding to the already durable approved intent.

`ApplyCreate` requires the exact Preview ID and a confirmed,
server-authenticated human actor. Before touching the filesystem it persists a
paused Project, its one stable Organizer identity, every configured canonical
Workspace, and the approved operation identity atomically through the selected
TaskStore. The durable Workspace ID is derived from the stable Project ID and
Project-local Workspace ID; renaming or moving a checkout does not change it.
Create then advances through these engine-owned effects:

1. create/adopt the exact owned Organizer root and ownership marker;
2. atomically create the exact canonical `paseo-director.json`;
3. atomically create the previewed README and explicit reference files;
4. initialize local Git with direct argv and no lifecycle hooks;
5. create or adopt one exact initial commit; and
6. record that exact revision and activate the Project.

Each effect has a durable progress transition. If execution stops after a
handoff but before its result is recorded, restart re-observes and adopts only
the exact desired result. A mismatch, dirty repository, changed file, stale
Preview, reused request with different content, missing approval, TaskStore
conflict, or unavailable fact fails closed. The Project remains paused until
the final activation, and repeated Apply/Recover calls do not create a second
commit.

Every Git read and write ignores system/global configuration and overrides
repository-local and worktree-scoped executable settings: hooks and signing are
disabled, `core.fsmonitor=false`, `core.quotePath=false`, external diff/network
helpers are pinned inert, and filter commands plus diff command/text-conversion
drivers are enumerated independently in each enabled scope before being
overridden. Enumeration is bounded, never follows external include directives,
and fails closed on an include, malformed configuration, or ambiguous worktree
configuration extension.
Create stages exact regular files with `hash-object --no-filters` and
`update-index`, never `git add`, so attributes cannot execute a clean/process
filter. Status and tracked-file comparisons use NUL-delimited raw path output,
which preserves valid non-ASCII references exactly through every recovery
boundary. Combined stdout/stderr is capped by the writer while Git is running;
overflow stops collection and returns one typed redacted error without
retaining the excess bytes.

`PreviewAdopt` reads a clean exact Git root, committed
`paseo-director.json`, current HEAD, and every configured Workspace identity.
Workspace observation rejects symlink/traversal aliases, nested roots, and a
Git common directory outside the canonical source checkout. Supported remote
transport aliases are canonicalized to one host/repository key before mapping.
Every explicit skill/template reference must be a committed regular file. The
preview performs no repository or TaskStore write. Confirmed `ApplyAdopt`
re-runs that exact observation and then persists the active Project/revision in
one TaskStore transaction. `Open` rechecks repository cleanliness, revision,
configuration hash, Project identity, explicit references, and the complete
durable Workspace identity/policy mapping after a process or TaskStore reopen.
Drift never silently changes
the active revision.

Create and Adopt inspect product repositories only through read-only Git/path
operations. They never add, edit, stage, commit, branch, or configure a product
repository. The Organizer is durable repository/configuration state, not a
persistent planning or decision-making agent.

This M1 skeleton still does not authenticate a concrete host transport,
compute Task-level effective overrides, schedule work, launch agents, create a
remote Organizer, or execute delivery policy. Those behaviors belong to their
later Tasks and must consume these engine-owned contracts rather than duplicate
them in TypeScript.
