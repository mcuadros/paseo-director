# ADR-0008: Treat same-user agent authority as an M0 stop condition

- **Status:** Proposed
- **Date:** 2026-09-06
- **Beads Task:** `dir-m0.8`
- **Plan gate:** M0 security and isolation
- **Decision owner:** M0 evidence owner; any plan or trust-scope change requires the human project owner
- **Amended by:** [ADR-0010](0010-top-level-task-agent-parentage.md), which distinguishes top-level Task/Reviewer creation from Task-Agent-created helpers without weakening this ADR's containment requirements

## Context

Director is a trusted Paseo plugin that coordinates source repositories,
provider agents, scoped MCP commands, Git and GitHub delivery, durable state,
cleanup, diagnostics, and support bundles. The approved plan, as amended by
ADR-0010, requires the engine to own every Director lifecycle effect while a
Task Agent may create only its scoped helper subagents. The system must stop
when it cannot prove repository ownership, exact Candidate/base identity,
recoverability, or secret safety.

Paseo's stable topology puts the plugin backend and provider agents on one
daemon host. The plugin backend is explicitly trusted and unsandboxed.
Provider agents run in the daemon user's context, while provider-native
sandbox and tool settings are documented as constraints rather than a host
security boundary. A threat model must therefore distinguish workflow policy
from enforceable authority separation.

This ADR contains no product implementation and does not silently change the
approved plan. It tests whether the default same-user topology can uphold the
plan's security claims against untrusted inputs.

## Question or hypothesis

For Director 1.0 on the stable Paseo 0.7 generation, with one single-user
Project bound to one daemon/executor and a trusted unsandboxed plugin process,
can the planned architecture protect untrusted repository, Task, agent,
provider, and remote inputs without treating provider sandboxes as a host
boundary?

Specifically, can it enforce engine-only Director lifecycle effects, the
scoped helper-creation exception in ADR-0010, fixed Project/Task/Run/role MCP
scopes, canonical repository/path ownership, argv-only command execution,
host-owned credential isolation, redacted logs/support bundles, and
fail-closed P0 stops on Linux and Windows?

## Acceptance criteria

- Identify protected assets, actors, trust boundaries, and assumptions.
- Cover the trusted plugin process, all three classes of agent behavior,
  repository and path ownership, process execution, credentials, logs,
  support bundles, MCP scope, persistence, delivery, and recovery.
- Map material abuse cases to preventive, detective, and recovery controls.
- State residual risks and explicit P0 stop conditions.
- Identify the smallest public Linux and Windows authority-separation options
  and whether they remain compatible with the supported Paseo topology.
- Return exactly one Go, No-go, or Inconclusive outcome without weakening a
  plan invariant.

Failure is established if a credible path remains for cross-scope repository
mutation or deletion, incorrect integration, secret exposure, shell
injection, agent bypass of engine ownership for a Director lifecycle effect,
unowned cleanup, or if a required boundary cannot be enforced through a
public supported mechanism.

## Evidence

The reproducible, redacted command transcript, artifact hashes, source
revisions, environment, failure state, and cleanup record are in
[`docs/evidence/m0.8/README.md`](../evidence/m0.8/README.md).
The rebased repository base also contains accepted
[ADR-0002](0002-paseo-0.7.2-public-surface.md), which independently bounds the
public M1 subset but does not prove agent authority separation.

The evidence establishes:

- Paseo 0.7 documentation calls plugin code trusted and unsandboxed; backend
  code can access host files, processes, credentials, and network.
- Paseo says agents run in the daemon user's context with provider-owned
  credentials and says provider options are not a host boundary.
- Session-scoped MCP and strict provider option schemas exist in the installed
  0.7.2 declarations, but this spike did not run a live agent because the
  shared daemon was unavailable.
- The built-in Paseo tool catalog includes cross-agent, cross-workspace,
  terminal, permission, and schedule mutations. Its documented filtering is
  not a boundary against shell access, and the current online filter contract
  is absent from the installed 0.7.2 provider-configuration declaration.
- Paseo worktree setup and teardown can execute repository-controlled shell
  scripts and can expose the source-checkout path.
- A documented custom-provider command can point at a trusted wrapper or
  container image, providing a possible public integration point. No
  cross-platform containment behavior has yet been proven.

The evidence is sufficient to falsify the same-user authority hypothesis. It
is intentionally insufficient to approve any isolation contingency.

## Threat model

### Protected assets and required properties

| Asset | Required property |
|---|---|
| Product repositories, worktrees, branches, refs, and dirty work | Integrity, scope confinement, ownership proof, and recoverability |
| Organizer Git and active configuration revision | Integrity, human-controlled authority expansion, and no secrets |
| TaskStore commands, facts, events, and audit | Integrity, ordering, idempotency, optimistic concurrency, and no secrets |
| Candidate, base, review, validation, publication, and integration facts | Exact-SHA binding and freshness |
| Git, GitHub, provider, Paseo, and operating-system credentials | Confidentiality, least privilege, and no agent access beyond unavoidable provider use |
| Director engine, execution lease, and MCP command ingress | Authenticity, one active executor, role/scope confinement, and replay resistance |
| Director logs, diagnostics, and support bundles | Confidentiality, bounded retention, provenance, and safe local handling |
| Host storage, process, and network capacity | Availability and bounded consumption |

### Actors and input classes

| Actor or input | Treatment |
|---|---|
| Authenticated single user | Trusted to approve the security envelope and explicit overrides, but can make mistakes; destructive previews and exact targets remain mandatory. |
| Director plugin revision and locked runtime dependencies | Trusted computing base. Compromise is total daemon-user compromise, so provenance and update controls are release gates. |
| Organizer, Task Agent, Reviewer Agent, and helper-subagent model output | Untrusted data and requests. Textual claims never prove external facts. |
| Repository content, Git metadata, dependencies, generated files, hooks, filters, and `paseo.json` | Untrusted until a specific executable action is separately approved and contained. Enrolling a remote does not authorize arbitrary code execution. |
| Provider, MCP, TaskStore, Git, GitHub, clock, filesystem, and process output | Untrusted external facts that require schema, identity, freshness, size, and state validation. |
| Paired or password-authenticated Paseo client | Equivalent to the single human in 1.0. Untrusted clients or multi-user authorization are outside the supported topology. |
| Unauthenticated network peer | Hostile; Paseo daemon authentication, encryption, and host allowlisting are operator prerequisites. |
| Root/administrator or a process already running as the Director engine identity | Outside any plugin-level containment claim. Such a principal can replace code or state. Doctor must state this explicitly. |

### Three distinct agent cases

The cases are not interchangeable:

1. **Accidental or buggy agent behavior.** The model misunderstands a Task,
   reports an effect it did not perform, uses the wrong path, or requests an
   unsafe command. Strict MCP schemas, engine-side policy, independent Git and
   GitHub observation, exact-SHA checks, and provider-native sandbox settings
   can contain this class when all controls are applied fail-closed.
2. **Prompt-injected model or malicious repository instructions.** Repository
   text, tool output, a dependency, or feedback deliberately instructs the
   model to read credentials, cross Workspaces, use raw `git`/`gh`/`paseo`, or
   bypass the Director MCP. The model remains inside a trusted provider CLI,
   but it intentionally exercises every tool the CLI makes available. MCP
   scoping alone does not contain this class; an enforced filesystem, process,
   credential, daemon-control, and network boundary is required.
3. **Fully adversarial same-user process.** A compromised provider binary,
   repository lifecycle command, test process, or other process with the
   daemon user's authority ignores provider policy entirely. In the default
   topology it can use the same readable files, credential helpers, sockets,
   process environment opportunities, and executables as the engine. Only an
   operating-system-enforced identity/container/VM boundary plus separated
   credentials and control endpoints can contain this class. The provider
   binary and any credential it must directly consume remain inside that
   boundary's trusted computing base.

### Trust boundaries

```text
Authenticated client
        |
        | Paseo-authenticated UI and typed plugin RPC
        v
Paseo daemon ---- trusted plugin subprocess / Director engine
                         |              |              |
                         |              |              +-- Git/gh/network credentials
                         |              +-- Organizer Git / TaskStore
                         +-- Paseo public SDK
                                  |
                                  v
                    provider agent execution boundary
                         |                    |
                         | owned checkout     | scoped stdio MCP
                         v                    v
                    Task worktree      Director command ingress

Forbidden from the agent boundary:
  host credential stores; raw Paseo/Director control socket; another
  Workspace; container/runtime socket; engine-owned Git/GitHub effects.
```

| Boundary | Required enforcement |
|---|---|
| Client to plugin RPC | Paseo-authenticated single-user session; Zod structural validation; semantic validation; expected aggregate version; server-derived actor/context; explicit confirmation for envelope expansion and destructive actions. |
| Plugin subprocess to host | Exact trusted plugin revision and dependencies; no dynamic execution/import from Organizer or product repositories; least ambient environment; central redaction; bounded subprocesses. This is not a containment boundary. |
| Engine to TaskStore/Organizer | Typed ports; transactional intent/evidence; active-revision pinning; optimistic concurrency; append-only audit; validated paths; no credentials in payloads. |
| Engine to Git/GitHub/processes | Direct argv execution; exact executable and cwd; minimal environment; repository/remote/branch/SHA proof immediately before every mutation; time/output limits; idempotency and reconciliation. |
| Engine to provider agent | Frozen profile; exact admitted provider/version/mode/options; fail if a sandbox or tool restriction is unavailable; no Git/GitHub/daemon credentials; only owned checkout and narrow command channel exposed. |
| Agent to Task worktree | OS-enforced write scope; repository setup, teardown, hooks, filters, submodules, dependencies, and tests treated as code execution; dirty/unintegrated work protected from cleanup. |
| Agent to Director MCP | One process/session-bound Project, Task, Run, role, and capability set; no caller-selected scope; opaque short-lived replay-resistant credential if IPC requires one; engine reauthorizes every command. |
| Director to logs/support output | Structured allowlist, central redaction before serialization, size limits, path pseudonyms, local human preview, restrictive file permissions, no automatic upload. |

### Security rules required by any viable design

#### Plugin and configuration

- Installing, updating, or building Director is a trust decision. Production
  releases must resolve an exact reviewed revision, lock dependencies, verify
  expected source, and never execute a repository-supplied build command that
  was not part of that reviewed revision.
- Organizer and product files are data. Director never evaluates them,
  dynamically imports them, or selects executable files by directory
  discovery. Explicitly referenced files remain schema- and path-validated.
- The Organizer may propose configuration, but only a human Preview/Apply can
  activate an authority expansion. The active revision and effective Run
  configuration are immutable facts.

#### Agent containment

- Worktree isolation is concurrency hygiene, not authority separation.
- An admitted unattended provider must prove that its exact version applies
  the frozen filesystem, network, tool, and approval options without fallback.
  A warning, ignored field, or unsandboxed fallback fails preflight.
- Task Agents and Reviewer Agents receive no GitHub credential, Git credential
  helper, raw daemon/Director control endpoint, host home, source checkout,
  sibling Workspace, or container-runtime socket.
- The Reviewer Agent is an independent top-level agent with no Organizer or
  Task Agent parent. Its checkout is detached and read-only. Candidate content
  is untrusted and cannot change the review target, invoke effects, or supply
  the verdict schema. The engine binds a verdict to the Candidate SHA and
  reviewer/run ID.
- Writer helpers need separate owned checkouts. A read-only shared checkout
  is allowed only after the provider and OS boundary prove it cannot write or
  escape. The Task Agent may create helpers only through the narrowly scoped,
  policy-admitted orchestration mechanism defined by ADR-0010. Director does
  not launch them, but it authorizes and reconciles capacity, scope,
  containment, interruption, and cleanup. General mutable built-in Paseo
  agent/workspace tools remain unavailable to governed sessions.

#### Repository and path ownership

- Every Workspace records canonical source path, Git common directory,
  canonical remote identity, default base, and a human-approved root. Every
  Run records Paseo workspace ID, worktree path, branch/ref, base SHA, expected
  Candidate SHA, and ownership nonce.
- Lexical prefix tests are insufficient. Resolve platform-native canonical
  paths and identities; reject traversal, symlink/junction/reparse-point
  escapes, case and Unicode aliases, device/UNC surprises, nested repositories,
  and a Git common directory outside the approved repository.
- Re-resolve ownership immediately before mutation to limit time-of-check to
  time-of-use replacement. A same-identity hostile process still requires OS
  separation; path checking alone cannot defeat it.
- Branch and ref names pass Git's own validation and cannot begin an option.
  Remote URLs are normalized, disallow embedded credentials, and are compared
  after approved rewrite rules. Submodules and worktree indirection are not
  implicitly owned.
- Cleanup requires all recorded identities to match current facts, a clean or
  verified recovery snapshot, exact integration proof, no active consumer,
  and policy permission. Any mismatch means quarantine/Needs you, never best-
  effort recursive deletion.

#### Commands and external effects

- Use direct `spawn`/`execFile` argv with `shell: false`; never interpolate an
  untrusted value into a command string. Use `--` or command-specific end-of-
  options handling and validate every enum/ref/path before invocation.
- Resolve an allowlisted executable during Doctor/preflight, use a canonical
  cwd, and build a minimal environment. Do not inherit arbitrary `PATH`, Git
  configuration, proxy, credential, hook, pager, editor, or tracing variables.
- Repository-controlled setup, teardown, Git hooks, clean/smudge filters,
  credential helpers, test commands, package scripts, and provider wrappers
  are executable code. They may run only after explicit provenance/approval
  and inside the agent authority boundary. In particular, Director must not
  implicitly accept Paseo's default `paseo.json` shell lifecycle.
- Bound duration, stdout/stderr bytes, process count, memory, disk, and network.
  Sanitize control characters before UI/log rendering. Kill the owned process
  group on timeout without targeting unrelated processes.
- Only the engine uses Git/GitHub delivery authority. It persists intent,
  observes external state, performs an absent effect, records the result, and
  reconciles after interruption. Text returned by an agent is never evidence.

#### Credentials, logs, diagnostics, and support bundles

- Director has no custom vault. Git, GitHub, provider, and Paseo credentials
  remain with their existing owners and are referenced, not copied.
- Never place a credential in argv, MCP arguments, prompts, Organizer Git,
  TaskStore, events, audit, exception text, diagnostic output, or support
  bundles. Environment transport is permitted only for a narrowly scoped
  ephemeral capability when no stronger inherited channel exists; it must
  authorize only the already-fixed session scope, expire, be revocable, and
  never be durably logged.
- Redaction happens at every Director-owned sink before formatting or
  persistence. It combines field-name classification, known-secret
  fingerprints, URL-userinfo removal, token-pattern detection, path
  pseudonymization, and post-redaction secret scanning. Unknown or failed
  redaction aborts the sink rather than emitting raw data.
- Subprocess output is untrusted and can contain secrets. Store structured
  summaries and bounded codes, not raw `git`, `gh`, provider, MCP, or agent
  transcripts. Paseo's retained plugin stdout/stderr makes `console.log` a
  durable disclosure path.
- A support bundle is human-triggered, local-only, allowlist-built, previewed,
  and created with restrictive permissions. It excludes source, diffs,
  prompts, conversations, environment, credential configuration, raw logs,
  raw command output, TaskStore rows, Organizer contents, and full paths. It
  contains schemas/versions, pseudonymous IDs, bounded redacted state
  summaries, health codes, and a manifest. Director never uploads it.

#### MCP and control-plane scope

- Each Director MCP bridge is created for one immutable Project/Task/Run/role
  scope. Tools do not accept Project, Workspace, Task, Run, agent, path, remote,
  or credential selectors when those values can be fixed at launch.
- The bridge validates schemas and sizes but owns no workflow policy. It sends
  a durable command to the engine, which validates the active lease, frozen
  configuration, expected version, current Run state, role capability, and
  idempotency key.
- An IPC credential is a capability, not a general bearer token. It is random,
  short-lived, audience-bound to one bridge and engine instance, scoped,
  replay-resistant, revoked when the session/Run ends, and redacted everywhere.
  Possession never grants broader IDs or direct side effects.
- MCP preapproval changes prompting, not authorization. Built-in Paseo tools,
  raw Paseo SDK/CLI access, terminal control, and shell access must be absent or
  contained. Disabling names in a catalog is defense in depth only.
- MCP output is untrusted. It cannot declare an effect complete, change scope,
  inject a new tool/server, or return an executable path. Responses are
  bounded, schema-validated, and secret-scanned.

## Abuse cases and control status

| ID | Abuse case | Required prevention, detection, and recovery | Current status |
|---|---|---|---|
| TCB-1 | Malicious or replaced Director revision/dependency gains daemon-user authority. | Exact reviewed source, locked dependencies, explicit trust/update UX, provenance verification, no dynamic product-repo code; disable and investigate on mismatch. | Design control only; compromise remains total. |
| CTL-1 | Malformed/replayed UI or Organizer command expands permissions or repeats an effect. | Typed plus semantic validation, server-derived context, human Apply/override token, expected version, idempotency key, immutable audit, reconciliation. | Must be proven by domain/effect tests. |
| CTL-2 | Plugin reload, crash, or competing executor duplicates agents, PRs, merges, or cleanup. | Durable lease and intent/evidence protocol; external observation before retry; startup reconciliation; effect-specific idempotency. | Open M0 stop: `dir-m0.2`, `dir-m0.10`. |
| AGT-1 | Buggy agent claims tests, commit, review, push, or merge occurred. | Ignore claims; inspect worktree, graph, remote, forge, and checks independently at exact SHAs. | Planned and testable. |
| AGT-2 | Prompt-injected agent invokes raw `git`, `gh`, `paseo`, another MCP, or daemon socket. | OS boundary removes credentials/control endpoints/network routes; only fixed-scope Director MCP remains; detect unexpected refs/process/network and park. | **Unresolved P0; default topology fails.** |
| AGT-3 | Adversarial same-user process reads credentials or mutates another repository/worktree. | Separate OS identity/container/VM, ACL/mount confinement, process isolation, egress policy, no host/runtime socket, separate credential broker. | **Unresolved P0; default topology fails.** |
| REP-1 | `paseo.json`, Git hook/filter, package script, test, dependency, or setup/teardown executes host code. | Disable implicit lifecycle code or require exact human approval; run only inside authority boundary with resource/network limits. | **Unresolved P0; `dir-m0.6` must include this path.** |
| AGT-4 | Candidate text prompt-injects Reviewer or forges a verdict. | Detached read-only checkout, trusted reviewer instructions/schema, no effects, engine-bound SHA/identity, deterministic validation, fresh review after change. | Residual model-quality risk remains. |
| AGT-5 | A Task Agent creates a writer helper in a sibling Workspace, exceeds its Run envelope, or mutates a shared checkout. | Task-Agent-created helper mechanism fixed to the current Run; separate writer checkout; Director-authorized and reconciled global/Task limits; reject caller-selected workspace. | MCP/provider proof pending `dir-m0.3`. |
| FS-1 | Traversal, symlink, junction, case, Unicode, UNC, nested-Git, or TOCTOU alias escapes an approved root. | Platform-native canonical identity, Git common-dir proof, descendant check, no-follow deletion, just-in-time revalidation, quarantine on mismatch. | Linux/Windows proof pending `dir-m0.6`. |
| FS-2 | Remote rewrite, stale base, branch-name injection, or repository swap targets the wrong remote/ref. | Canonical remote identity, no URL credentials, Git ref validation, exact base/Candidate, controlled Git config/env, refetch and compare before effect. | Delivery proof pending `dir-m0.7`. |
| FS-3 | Cleanup removes dirty, unintegrated, shared, or unowned state. | Ownership ledger, verified snapshot ref, integration reachability, no active consumer, retention, preview/quarantine, idempotent cleanup. | P0 proof pending `dir-m0.6`. |
| CMD-1 | Untrusted value becomes shell syntax, an option, executable, cwd, environment override, or pager/editor/hook. | `shell: false`, argv arrays, allowlisted executable/subcommand/options, `--`, minimal env, canonical cwd, disabled interactive helpers, bounded output/time. | Normative control; implementation tests required. |
| CMD-2 | Tool output floods disk/memory or injects terminal/log control sequences. | Byte/time/rate limits, structured capture, control-character escaping, log quotas, process-tree termination, disk launch threshold. | Implementation/release tests required. |
| SEC-1 | Agent inherits or locates GitHub/Git/Paseo/host credentials. | Do not inject; separate identity/home/mounts; deny credential/helper/control endpoints; narrow provider credential or broker. | **Unresolved P0; same-user denial rules are not a host boundary.** |
| SEC-2 | Secret enters config, prompt, URL, command error, MCP payload, event, audit, log, or diagnostic. | Reject URL userinfo and secret fields, minimize data, central pre-sink redaction, known-secret fingerprints, post-scan, abort/quarantine and incident handling. | Must receive adversarial tests before release. |
| SUP-1 | Support bundle includes source, prompt, environment, full path, raw log, or credential. | Local human action, allowlist schema, pseudonyms, preview, restrictive permissions, secret scan, no upload, safe deletion/retention. | Design requirement; implementation/release tests required. |
| MCP-1 | Caller substitutes another Project/Task/Run/path/agent or replays a bridge token. | Scope absent from tool input, session-bound capability, expected state/version, nonce/idempotency, expiry/revocation, audit and rate limits. | Provider matrix pending `dir-m0.3`. |
| MCP-2 | Built-in Paseo tools or shell bypass engine command policy. | Remove mutable built-in catalog and raw control access; OS containment; engine remains sole command executor. | **Unresolved P0; installed 0.7.2 lacks the documented per-provider catalog field.** |
| MCP-3 | Bridge compromise becomes an engine-side confused deputy. | Minimal command-ingress API, no direct adapter credentials/effects, server authorization, fixed audience/scope, process supervision and revocation. | Bridge mechanism remains M0 evidence. |
| DEL-1 | Review/checks apply to one SHA but another SHA is pushed or integrated, or retry merges twice. | Immutable Candidate, exact base, fresh external facts, atomic expected-head integration, intent/evidence reconciliation, invalidate on any change. | P0 proof pending `dir-m0.7`, `dir-m0.10`. |
| DAT-1 | Concurrent/stale/corrupt TaskStore write changes authority or hides audit. | Schema validation on read/write, transactions, expected versions, unique command IDs, append-only events, backups/restore, pause on uncertainty. | M0 evidence owned by `dir-m0.4`, `dir-m0.5`. |
| NET-1 | Unauthenticated or cleartext remote client controls the daemon and Director. | Loopback/socket, or authenticated encrypted relay/VPN; password and host allowlist for direct network; Doctor/preflight blocks insecure exposure. | Operator prerequisite; plugin cannot repair it silently. |
| DOS-1 | Agent/task/input/log explosion exhausts capacity and prevents recovery. | Input/schema limits, quotas, backpressure, time/cost/process/disk budgets, 10% disk stop, bounded retention, safe cleanup. | Release tests required; never sacrifice dirty work. |

## Plan invariants not enforceable in the default topology

The same-user evidence does not merely leave implementation details open. It
prevents the following approved invariants from being security guarantees
against prompt-injected or adversarial agent/repository execution:

- **Invariant 7 as amended by ADR-0010:** an agent with raw
  Git/GitHub/Paseo/host access can perform a Director lifecycle effect without
  the engine. The narrow Task-Agent-created helper path grants no such effect
  authority.
- **Invariants 8 and 9:** an out-of-band effect bypasses the engine's durable
  intent, idempotency key, and recovery reconciliation and can duplicate an
  agent, branch, PR, push, or integration.
- **Invariant 10:** a same-user process can mutate or delete an unknown or
  unowned accessible path/ref even when Director cleanup refuses to do so.
- **Invariant 11:** that process can destroy dirty or unintegrated work before
  the engine snapshots it.
- **Invariant 13:** an Organizer with ambient shell/control authority can act
  outside the human-approved Director command envelope even if Director
  rejects its proposal.
- **Invariant 14:** an agent or repository process with credential access can
  expose a secret through provider/Paseo output, a subprocess error, or a
  Director sink before a complete boundary exists.

The domain representation for Invariants 1 and 2 can still state one target
Workspace, branch, and PR, but ambient same-user authority can create real
external state outside that representation. Exact-SHA validation can still
protect Invariants 3 through 6 at the engine gate, provided the engine's own
state and process are not writable by the agent identity.

No wording change in documentation can turn these into enforceable
boundaries. Treating agents as fully trusted would be a material scope and
security-policy reduction requiring an explicit human-approved ADR.

## P0 stop conditions

Director must stop the affected launch, effect, M1 entry, platform release, or
all releases whenever it cannot prove the corresponding condition:

1. **Trusted code:** the exact Director revision, dependencies, or preparation
   commands are unknown, mutable, or not the reviewed release.
2. **Agent authority:** a Task Agent, Reviewer Agent, or helper subagent can
   reach a host credential, another Workspace, raw Paseo/Director control
   endpoint, container-runtime socket, or Git/GitHub delivery route outside
   its fixed capability scope.
3. **Fail-closed provider:** the exact provider/version cannot prove its frozen
   mode/options/MCP configuration was accepted and applied, or can silently
   fall back to an unsandboxed mode.
4. **Repository execution:** worktree creation, checkout filters, hooks,
   `paseo.json`, setup/teardown, tests, or dependencies can execute outside the
   approved authority boundary.
5. **MCP scope:** an MCP tool accepts caller-selected scope, a capability can
   broaden/replay after Run termination, mutable built-in tools remain
   reachable, or the bridge can perform direct effects.
6. **Ownership:** repository, remote, Git common directory, path, branch/ref,
   worktree, Candidate, or cleanup ownership does not exactly match durable
   facts at mutation time.
7. **Recoverability:** dirty/unintegrated work lacks a verified recoverable
   snapshot, or cleanup cannot prove it targets only owned ephemeral state.
8. **Secret safety:** a plaintext secret is observed in any Director-owned
   prompt, config, state, command/audit payload, event, log, diagnostic, or
   support bundle, or redaction cannot prove safe output.
9. **Exact delivery:** review, validation, publication, checks, feedback, base,
   and integration do not refer to the same current exact SHA, or the forge
   cannot atomically reject a changed integration head.
10. **Recovery/idempotency:** an interrupted effect cannot be reconciled from
    durable intent and authoritative external facts before retry.
11. **Daemon access:** the daemon is remotely reachable without the required
    authentication, host validation, and transport confidentiality.
12. **Platform proof:** Linux or Windows path, process, credential, IPC,
    containment, locked-file, and cleanup behavior is untested for a claimed
    release platform. Failure blocks that platform; because both are stable
    release gates, unresolved failure blocks stable 1.0.

An observed secret exposure, irreversible dirty-work loss, unowned deletion,
or incorrect integration is an incident and release-blocking defect, not a
recoverable warning or P2 acceptance candidate.

## Smallest public authority-separation options

These are candidates for a new experiment and explicit human decision. None is
approved by this ADR.

| Option | Minimum boundary | Paseo and platform compatibility |
|---|---|---|
| Linux provider wrapper in an OCI container | A trusted custom-provider argv wrapper launches the provider without the Docker/runtime socket; bind-mounts only the owned worktree and fixed-scope MCP endpoint; uses a distinct user namespace/UID; hides host home, source checkout, sibling paths, engine state, and Git/GitHub credentials; restricts egress to the provider/broker; applies process/disk limits. | Paseo publicly documents custom command/Docker wrappers, and installed 0.7.2 exposes command-array profiles. This is conceptually compatible with a Linux daemon but adds an external runtime and privileged setup. Stdio, cwd/path mapping, provider auth, custom MCP, interruption, and cleanup require proof. Containerizing the whole daemon is insufficient because plugin and agents still share one identity inside it. |
| Linux separate service identity | A root-managed systemd unit or equivalent starts each provider under a dedicated static/dynamic user with `NoNewPrivileges`, protected home/system mounts, only the worktree writable, restricted address families/egress, no daemon/control/runtime socket, and a narrow MCP capability. | Uses public Linux primitives but requires a privileged launcher not documented in Paseo's per-session API. A custom provider wrapper might integrate it, subject to M0 proof and host policy. It is not portable to every Paseo-supported host. |
| Windows AppContainer/restricted-token wrapper | A trusted wrapper launches the provider with a distinct restricted identity/AppContainer; ACLs grant only the owned worktree and fixed-scope IPC; credential stores, other profiles, named pipes, daemon endpoints, and process handles are denied; network capabilities/firewall allow only provider/broker traffic. | Public Windows primitives exist, and Paseo's custom command array is a possible entrypoint. No installed stable Paseo contract proves token/AppContainer creation, stdio, provider CLI behavior, path mapping, or teardown. Restricted tokens alone are insufficient where the same user ACL still grants access. |
| Windows Hyper-V-isolated provider container | The provider runs in a per-Run Hyper-V container/VM with only the owned worktree and scoped IPC mounted, no host/container control socket, no Git/GitHub credentials, bounded egress/resources, and verified teardown. | Provides a stronger Windows boundary than process isolation but requires supported Windows editions/features, container images, and path/stdio/MCP/provider tests. It is not a universal Windows baseline and adds an external runtime. |
| Credentialless agent host plus separate effect broker | Agents run on a dedicated VM/host that has only provider access and Task worktrees; a separately authenticated least-privilege engine/broker holds GitHub, Git delivery, and daemon administration authority. | Strongest cross-platform concept and limits fully adversarial Task Agent processes. It changes the approved single-daemon monolith/no-sidecar architecture and path topology, so it requires a human-approved plan ADR and new M0 validation before implementation. A dedicated host without separating effect credentials is insufficient. |

Every viable option must also address provider authentication. A fully
adversarial provider process can steal any credential it directly consumes.
Protecting that credential requires a short-lived narrow token or a broker
that does not expose the upstream secret, and the broker must not grant Git,
GitHub, Paseo, or cross-scope Director authority.

The smallest promising experiment for prompt-injection containment is a
human-configured custom provider profile whose argv wrapper enters an
OS-enforced per-Run container and retains provider stdio while exposing only
the owned worktree and narrow MCP channel. It is not yet evidence for Linux or
Windows support, and it does not by itself contain a compromised provider
binary or protect a raw provider credential.

## Alternatives considered

### Rely on worktrees, provider sandbox options, and MCP tool filtering

These controls are useful for concurrency and accidental behavior. They are
rejected as the security boundary because Paseo explicitly disclaims that
claim, repository lifecycle scripts run outside the MCP policy, and a same-user
shell can reach ambient executables and credentials.

### Declare agents and repository code trusted

This would make the same-user topology internally consistent but would remove
protection against prompt injection and malicious dependency/repository code.
It would weaken the human-owned security envelope and the requirement that
the engine own every Director lifecycle effect. This Task is not authorized
to make that product-scope change.

### Run the entire Paseo daemon in one container or dedicated machine

This reduces blast radius to mounted data and that host. It does not separate
the trusted plugin/engine from agents or delivery credentials within the
daemon, so it does not solve the confused-deputy or engine-bypass threat by
itself.

### Add OS-separated provider execution

This is the smallest technically credible direction for prompt-injected tools
and repository processes. Public wrapper hooks and OS primitives exist, but
the end-to-end Paseo contract, provider credential handling, stdio MCP,
cross-platform behavior, and lifecycle cleanup are unproven. Selecting it now
would turn a hypothesis into architecture without the required experiment.

### Add a privileged external-effect broker

This best separates source-code execution from GitHub and daemon authority,
including against a compromised same-user Task Agent. It conflicts with the
approved monolith/no-sidecar and single-daemon path assumptions. It may be
selected only through a human-approved plan ADR after a bounded M0 spike.

## Decision

**No-go.** The default stable Paseo same-user topology does not meet the
falsifiable criteria for engine-only Director lifecycle effects or
credential/control-plane isolation against prompt-injected repository
instructions or a fully adversarial same-user process.

The selected contingency is to keep the M1 security gate blocked. Director
must not claim, implement against, or test away the affected plan invariants
until public, OS-enforced authority separation is proven for every admitted
provider on the claimed Linux and Windows topology, or the human project owner
explicitly approves a plan/scope change. Undocumented Paseo internals, silent
fallback, and treating provider settings as a host boundary remain forbidden.

The threat controls above remain the required baseline for accidental and
buggy behavior even if a stronger execution boundary is later selected.

## Consequences

- No M1 product code is authorized by this ADR. The stop condition is an M0
  result, not a request to improvise a sidecar or weaken an invariant.
- `dir-m0.1` established the exact 0.7.2 public subset in ADR-0002 but did not
  prove authority separation. `dir-m0.2` must prove lifecycle and recovery;
  `dir-m0.3` must prove provider-specific MCP/options and fail-closed behavior;
  `dir-m0.6` must cover
  repository lifecycle execution plus Linux/Windows ownership/cleanup;
  `dir-m0.7` must prove exact-SHA delivery; and `dir-m0.10` must preserve the
  authority boundary across every effect and recovery point.
- Current online documentation must never substitute for installed artifact
  capability detection. The observed 0.7.2 documentation/artifact skew is a
  fail-closed compatibility input.
- A later accepted isolation design must add adversarial tests that attempt
  credential reads, daemon/CLI access, sibling-workspace mutation, raw
  Git/GitHub effects, lifecycle scripts, token replay, container/runtime access,
  network escape, and cleanup races on Linux and Windows.
- If no supported authority boundary is available, the permitted outcomes are
  an explicit human-approved reduction to a trusted-agent/trusted-repository
  product, postponement of automatic execution/delivery, or an upstream Paseo
  capability request. None is silently selected here.
- Residual risk remains from a malicious trusted plugin revision, root/admin,
  authenticated human decisions, container/kernel escapes, provider compromise,
  and model review omissions. These are documented trust limits; they do not
  excuse a known P0 path inside the supported boundary.

## Independent verification

Pending independent reproduction and review of the exact Candidate SHA.
Review must verify the No-go evidence, the mapping to plan invariants, the
absence of an implicit trust-scope reduction, and the Linux/Windows option
bounds. No PR, merge, or Task closure is authorized by this Candidate alone.
