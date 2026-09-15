# Getting started

This guide takes a new single-user installation through the supported
selector-first setup and creation of its first Task. It applies only to Linux
amd64 with glibc and exact stable Paseo `0.7.2`.

## 1. Check the host and authority boundary

For the current default-`main` source-testing channel, install exact Go
`1.26.5` and make the committed `go.sum` module closure available in the local
module cache. Published alpha/beta/stable packages require neither Go nor Git,
but no tagged release channel has been published yet. Node.js 22 or newer and
npm with lockfile-version-3 support are required in every channel. Git and an
admitted provider CLI are required for product work; GitHub CLI is required
only for GitHub delivery.

```console
paseo --version
node --version
npm --version
git --version
```

Continue only when Paseo reports exactly `0.7.2`. Director downloads and
verifies canonical precompiled Dolt `2.3.2`; do not install or select a system
Dolt for Director.

Director is trusted, unsandboxed plugin code. Every server RPC receives the
daemon user's complete public `PaseoApi` from its handler. Director uses only
that handler-scoped object: it does not ask for, copy, persist, or open a
secondary connection with the daemon password. Installing or updating the
plugin therefore remains a trust decision. Read
[Security boundaries](security-boundaries.md) before continuing.

## 2. Install and activate default main

No tagged alpha, beta, or stable channel is currently published. Install the
explicit source-testing channel by omitting `--ref`:

```console
paseo plugin add mcuadros/paseo-director
paseo plugin ls --json
paseo plugin reload director
paseo plugin logs director --json
```

Paseo first installs the locked seven-package production closure with
`npm ci --omit=dev --ignore-scripts --no-audit --no-fund`, then runs the
committed verifier. Package lifecycle scripts are disabled and
`@getpaseo/client@0.7.2` is installed from the integrity-pinned lock rather
than vendored.

For unpublished `main`, declared add/update preparation builds the small Go
bootstrap once; that bootstrap builds the exact Engine once with the pinned
toolchain and existing verified module closure. The bootstrap also downloads
and verifies canonical Dolt. Runtime and reload never compile. A later
published release instead ships a pinned precompiled bootstrap and downloads
only its pinned precompiled Engine, exact-source notices, and canonical Dolt;
it never compiles or falls back to source.

Activation succeeds only when the plugin is running and its latest bounded log
contains `DIRECTOR_ACTIVATION_READY` with `result=running-current`. A failed
candidate preserves the prior installation, TaskStore, verified caches, and
recovery data.

Never stop or restart the Paseo daemon or machine for installation, update,
configuration, recovery, or troubleshooting. Plugin-scoped reload/update
preserves unrelated agents and workspaces.

## 3. Create from a native Paseo Project

Open **Director** in Paseo and select **Create Project**. The standard modal is
**Set up from a Paseo Project**:

1. Select one current native Paseo Project from the authoritative list.
2. Inspect its native Project ID/name, canonical repository root, proposed
   sibling Organizer location, and currently visible Git Workspaces.
3. Press **Preview**. Director Engine derives stable Director identities and
   generates the complete safe `paseo-director.json` plus ordered file/Git
   operations.
4. Review the generated configuration, manual launch, manual direct
   integration, finite budgets, empty fallback chain, and independent Review.
5. Press **Apply exact Preview** as a separate authenticated human action.

The normal path never asks you to duplicate a Project ID/name/path or paste raw
JSON. Any changed native fact invalidates the Preview. Apply creates one local
Organizer commit, records the native Workspace mapping, and activates the
Project idempotently. It does not edit a product repository.

**Advanced import** is a separate path only for an already configured
Organizer repository. Use the [example Organizer](../examples/organizer/README.md)
as an advanced-import reference, not as the standard setup form.

## 4. Run Doctor

On the new Project row, select **Doctor**. Doctor is read-only. It checks the
exact host, runtime, TaskStore, Organizer, lease, sync, Workspace, provider,
MCP, rootless-OCI, and resource facts and supplies bounded remediation for
every non-passing check.

Do not launch work from a blocking or stale report. If Doctor offers
**Repair Project…**, inspect the exact non-destructive Preview and confirm it
separately. Repair never installs software, changes channels or policy, deletes
a path, or repeats an unknown effect.

## 5. Plan and launch the first Task

Open **Board**, then:

1. Select **Create Epic** when grouping is useful.
2. Select **Create Task**, choose exactly one Workspace and optional Epic, and
   enter a key, title, objective, priority, labels, and one acceptance criterion
   per line.
3. Use **Add dependency** for a Task or Epic prerequisite. The graph must remain
   acyclic; ordinary dependency waits stay Queued.
4. Open Task detail and inspect the effective Project → Workspace → Task
   configuration.
5. Select **Launch now**.

Director rereads the approved Organizer revision, dependencies, lease,
provider, repository/base, disk, budget, and capacity facts. It freezes the
complete effective configuration and starts at most one Run. Missing, stale,
or ambiguous evidence is refused rather than filled in by a model.

Continue with the [User guide](user-guide.md).

## Update, reload, or remove

An explicit update prepares and activates a changed Git Candidate. Reload only
adopts or retries already prepared artifacts and never compiles:

```console
paseo plugin update director
paseo plugin reload director
paseo plugin ls --json
```

An immutable tag or commit does not advance through update. Follow the bounded
two-read removal procedure in [Installation and update](installation-update.md)
for rollback; do not use a private identifier or edit Paseo state. Removing
the connector stops lease renewal but preserves Director identity, TaskStore,
Organizer repositories, verified caches, and recovery material.
