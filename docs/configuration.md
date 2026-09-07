# Organizer configuration and revisions

`paseo-director.json` is the discovery marker and the only operational
configuration document in an Organizer repository. The Organizer is the
repository, its configuration and durable Project material. It is not an
agent, does not need a standing conversation, and cannot decide a lifecycle
transition.

The Director Engine owns the closed JSON Schema at
[`engine/domain/configuration/paseo-director.schema.json`](../engine/domain/configuration/paseo-director.schema.json).
The M1 schema version is `1` and requires the exact engine-owned `$schema`
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
      "maxActiveTasks": 4,
      "maxActiveTasksPerWorkspace": 2,
      "maxConcurrentAgents": 8,
      "maxSubagentsPerTask": 3
    },
    "runBudget": {
      "elapsedSeconds": 7200,
      "tokens": 200000,
      "turns": 32,
      "ciCycles": 4
    }
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
Workspace and reference identifiers are bounded; Workspace IDs and explicit
references are unique; source paths are clean absolute Linux paths; Git branch
names are safe; Workspace remotes use only `https://`, `ssh://`, `git://`, or
safe scp-like SSH syntax; password-bearing URL/scp userinfo, Git remote-helper
`token::address` dispatch, and command-bearing or local transports are rejected
while username-only SSH forms such as
`git@github.com:owner/repository.git` remain valid; profile tokens and provider
families are closed; capacity and Run budgets are finite and internally
consistent; Workspace overrides name a declared Workspace and select `inherit`
or a concrete value; and skill/template paths are clean relative paths in their
declared Organizer directories. Source and reference paths reject whitespace
and control characters. Directory scanning never turns an unreferenced file
into executable or prompt input.

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
   version with a confirmed server-authenticated human actor. It reparses no
   caller-supplied configuration. A stale, mismatched, invalid or unconfirmed
   command leaves both active state and Run inputs unchanged.
5. Successful Apply moves the already validated pending revision to active and
   clears pending state.

The active-revision pointer is dynamic Project state rather than an Organizer
file, avoiding a self-referential commit. Models may propose configuration and
hosts may submit typed commands, but neither can manufacture Apply authority.
The TypeScript package renders engine projections and transports commands; it
does not validate, activate or freeze revisions.

## Frozen Run snapshots

`FreezeRunConfiguration` reads only the active revision. Before the first
Apply it fails closed. Once an active revision exists, it produces an immutable
snapshot containing:

- snapshot schema version `director.run-configuration-snapshot/v1`;
- the exact active Organizer Git object ID;
- the canonical configuration SHA-256;
- the complete canonical version 1 configuration.

Snapshot accessors return copies. Persisted snapshots are strictly reparsed and
their internal revision format, contract version, configuration validity and
content-hash consistency are verified within the bounded snapshot size. The
trusted TaskStore layer later binds that self-consistent snapshot to its exact
Project and Run. A later Preview or Apply cannot change a snapshot already
assigned to a Run. Thus a valid but unapproved pending revision, and even a
human-confirmed invalid revision, cannot affect a new or existing Run.

This M1 skeleton does not create Organizer commits, authenticate a concrete
host transport, persist Project state, compute Task-level effective overrides,
schedule work, launch agents, or execute delivery policy. Those behaviors
belong to their later Tasks and must consume these engine-owned contracts rather
than duplicate them in TypeScript.
