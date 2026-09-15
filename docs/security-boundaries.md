# Security boundaries

Director fails closed around untrusted input, but it is not a sandbox for every
trusted component. This page summarizes the supported Linux 1.0 boundary for
exact stable Paseo `0.7.2`; [SECURITY.md](../SECURITY.md) owns private
vulnerability reporting.

## Trusted components and untrusted inputs

The trusted computing base is the exact reviewed Director bootstrap, Engine,
connector, locked dependencies, Paseo daemon, and exact admitted provider CLI
binaries. Model output, prompts, repository content, dependency/test code,
lifecycle configuration, and external tool output remain untrusted inputs.

Compromise of a trusted component, the Linux kernel, the host root account, or
a container escape is outside the 1.0 containment guarantee. Provider-native
authentication may be visible to its exact trusted CLI. These exclusions are
disclosed rather than presented as hostile-provider protection.

Every governed Task Agent, Reviewer, and helper still runs inside mandatory
rootless-OCI defense in depth: read-only root, dropped capabilities,
`NoNewPrivileges`, private network namespace, absent runtime socket and raw
control tools, finite process/memory/time/output/temp/worktree limits, and only
its owned checkout plus fixed stdio MCP scope. Missing enforcement stops work;
there is no weaker fallback.

## Handler-scoped Paseo authority

On exact Paseo `0.7.2`, each trusted plugin handler receives the daemon user's
complete public `PaseoApi`. Director uses only that handler-scoped object. It
does not configure a daemon URL, ask for or copy the daemon password, open a
secondary connection, or pass Paseo authority into the Go runtime.

This is broad trusted plugin authority and must be understood before
installation. The connector exposes only fixed generated Director contracts
and normalized observations to Engine; it does not become a second policy
engine or a generic Paseo API.

## Runtime and credential separation

The Go bootstrap alone owns private XDG identity/configuration, runtime-control
credentials, TaskStore credentials, locks, and exact Engine/Dolt children.
Engine is the sole user of the distinct control, writer, and exact-routine
maintenance SQL identities. The transient TaskStore bootstrap owner is removed
from runtime configuration after it seals the exact authority attestation.

Git uses the host credential mechanism; GitHub delivery uses the authenticated
`gh` session; provider authentication belongs to Paseo/provider CLIs. Director
has no custom vault or token-entry form. Credentials are prohibited from
Organizer Git, product repositories, prompts, MCP arguments, TaskStore rows,
events, audit, logs, diagnostics, screenshots, and support bundles. Remotes
with credential-bearing userinfo and configuration fields shaped like secrets
are rejected.

Director-owned sinks accept closed bounded fields and scan before writing. Raw
tool output is not durable evidence. If redaction, schema, identity, or size
validation fails, nothing is emitted. Director has no telemetry; a support
bundle is local-only and never uploaded automatically.

## Decision and agent authority

Only Director Engine decides eligibility, launch, retry, escalation, routing,
closure, delivery, integration, and cleanup. UI requests, model claims,
connector responses, terminal events, and Board columns are inputs or wake
signals, never proof.

Run-scoped Worker/Reviewer/helper MCP tools cannot select another Project,
Workspace, Task, Run, Candidate, path, remote, provider, or credential. A
Task Agent may request an admitted helper, but the helper never becomes Task
owner, Candidate producer of record, or independent Reviewer.

Every authenticated agent in a Director-managed Project may create a separate
Project-administration session fixed to that Project. It can submit bounded
planning/control commands only after membership and expected-version checks.
It has no Project selector, human-only confirmation, Review verdict,
integration, destructive purge, raw TaskStore, or generic SDK authority.

## Human and P2 gates

An authenticated human Preview/Apply is required for configuration activation,
dependency override, lifecycle-script approval, Emergency stop, or any
expansion of repository, provider/model/permission, delivery, integration,
budget, or destructive authority. Models and Project agents may propose but
cannot approve those effects.

Manual integration is the product default. Automatic integration is an
explicit envelope expansion and still requires the same current exact
Candidate/base, authoritative CI, independent Review, checks, feedback,
mergeability, ownership, and atomic expected-head gate. Any residual P2
finding requires an exact recorded human acceptance before approval or
release; P0/P1 cannot be accepted as ordinary debt.

## Repository execution

Enrolling a repository does not approve executable content. Before workspace
creation, Director canonicalizes `paseo.json` setup, teardown, terminal, and
service-port command surfaces. Any non-empty surface parks until a
server-authenticated human approval binds its exact scope and digest. A changed
command invalidates that approval.

Git hooks, filters, credential helpers, submodules, dependencies, tests, and
local/path remotes are executable input. Director uses fixed direct argv,
minimal environments, canonical working directories, validated options/refs,
bounded resources, and rootless OCI. It never interpolates an untrusted value
into a shell command.

## Destructive safeguards

Director deletes only exact owned ephemeral state after current integration,
termination, recovery, repository, ref, consumer, disk, and policy facts pass
one fresh gate. Unknown agents, Workspaces, worktrees, branches, refs, paths,
listeners, processes, caches, and recovery artifacts are preserved.

Dirty tracked/untracked work uses verified Git recovery commits. Ignored
material never enters Git; eligible data uses an owner-only non-Git artifact.
Dirty-plus-ignored content, nested repositories, untracked symlinks, special
files, changed identity, over-limit content, timeouts, or disk pressure remains
in place and routes to Needs you without exposing names or content.

Local and remote Task-ref deletion uses an exact expected-OID compare guard.
A changed, ambiguous, or unavailable ref survives. Completed deletion is
terminal; later reappearance is drift, not permission to delete again.

Secret exposure, unowned deletion, irreversible dirty-work loss, wrong-SHA
review, or incorrect/double integration is a P0 incident. Preserve state and
use the audited response path; stopping or restarting Paseo, clearing state,
or deleting evidence is not remediation.
