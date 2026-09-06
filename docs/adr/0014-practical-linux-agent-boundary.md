# ADR-0014: Adopt a practical trusted-provider Linux agent boundary

- **Status:** Accepted
- **Date:** 2026-09-06
- **Beads Task:** `dir-m0.14`
- **Plan gate:** M0 operating-system authority separation
- **Decision owner:** Human project owner
- **Amends normative runtime-boundary decisions in:** PLAN §§4, 11–15, 18, 21, 23, and 25
- **Supersedes:** the M1-blocking No-go Decision in [ADR-0008](0008-director-threat-model.md), while retaining its maximal threat analysis
- **Preserves:** [ADR-0005](0005-session-scoped-mcp-provider-matrix.md),
  [ADR-0007](0007-git-worktree-ownership-and-cleanup.md), and
  [ADR-0011](0011-linux-only-platform-scope.md)

## Context

ADR-0008 deliberately tested a maximal threat model in which a provider binary,
repository process, or agent process might be fully hostile while sharing a
Linux host with Director. The M0.14 evidence demonstrated useful rootless-OCI
controls but also showed that this maximal model requires additional credential,
egress, lifecycle, and storage mediation not present in the approved 1.0
monolith.

The human project owner now adopts a practical Linux 1.0 trust boundary. The
exact reviewed Director engine and dependencies, the Paseo daemon, and the
installed provider CLI binaries are trusted components. Model output,
repository content, and external tool output remain untrusted inputs. This is a
security-scope decision, not a claim that the maximal threat model was solved.

The corrected Git evidence also proves that a Task Agent can create a legitimate
Candidate without writable shared refs or shared object storage. That result is
preserved independently of this trust decision.

Director 1.0 remains Linux-only. This ADR creates no requirement, claim, or
follow-up for another host operating system.

## Question or hypothesis

With the Director engine, Paseo daemon, and installed provider CLIs trusted, can
Director admit the exact Linux provider tuples proven by ADR-0005 while keeping
engine state and delivery authority outside agent scope, refusing automatic
repository lifecycle execution, requiring rootless OCI as defense in depth,
and parking on operational disk/resource uncertainty or excess?

## Acceptance criteria

- State the trusted components, untrusted inputs, and excluded compromise
  claims without ambiguity.
- Keep engine state, TaskStore and GitHub credentials, raw Paseo control,
  delivery authority, source checkouts, sibling Workspaces, and runtime sockets
  unavailable to governed agents.
- Require rootless OCI plus the frozen provider-native policy for filesystem,
  process, and network defense in depth.
- Permit provider-native authentication to the trusted provider CLI without
  promising confidentiality from a compromised provider binary.
- Refuse every installed non-empty automatic `paseo.json` lifecycle surface
  before workspace creation unless an exact human approval binds the fixed
  Director scope and lifecycle digest.
- Treat finite disk, aggregate-workspace-byte, process, memory, and elapsed-time
  ceilings as operational fail-closed controls. Missing telemetry or excess
  parks in `Needs you` and never authorizes destructive cleanup.
- Preserve the public Git private-object, per-worktree-ref, bundle, and exact
  engine-import path.
- Return one human-approved Go outcome for Linux 1.0 without weakening exact-SHA,
  ownership, idempotency, cleanup, or secret-persistence invariants.

## Evidence

The exact versions, public sources, installed artifact hashes, sanitized
observations, executable fixtures, and cleanup proof are in
[the M0.14 evidence bundle](../evidence/m0.14/README.md).

The evidence establishes these facts:

1. The exact Codex, Claude Code, and OpenCode executables run inside the tested
   rootless OCI profile. Its mount/network/process namespaces, read-only root,
   dropped capabilities, `NoNewPrivileges`, absent runtime sockets and raw
   control executables, cgroups, RLIMITs, and tmpfs bounds are available as
   defense-in-depth controls.
2. Engine state, source and sibling paths, host home, raw Paseo/GitHub/container
   controls, and host daemon loopback were absent inside that profile. The
   owned worktree remained writable and stdio MCP retained its exact one-tool
   scope.
3. Provider authentication deliberately mounted for the trusted CLI was
   readable inside the provider boundary. This is accepted only under the
   trusted-provider assumption below.
4. Paseo 0.7.2 runs configured worktree setup/teardown before a provider wrapper.
   The selected design therefore detects and refuses all installed automatic
   lifecycle surfaces before asking Paseo to create the worktree.
5. Git 2.47.3 produced a real Candidate with a writable per-worktree gitdir,
   private new-object storage, and read-only shared base objects. Shared heads
   and the shared common-directory content/structure/mode digest outside the
   owned per-worktree gitdir remained unchanged. The engine verified a bundle,
   imported the exact Candidate, and reproduced its parent and contents.
6. Cgroups, per-file limits, and tmpfs bounds do not impose an aggregate byte
   quota on a writable host bind mount. The selected contract observes finite
   operational ceilings and parks rather than claiming hostile-provider OS
   enforcement.
7. The focused practical-boundary contract admits empty lifecycle configuration,
   parks each of setup, teardown, terminals, and service-port scripts without
   exact human approval, rejects non-human/stale/cross-scope approval, and
   invalidates approval after any command change. It also proves exact resource
   boundaries, each one-unit excess, and every missing observation.

## Trust boundary

| Category | Linux 1.0 treatment |
|---|---|
| Director engine, reviewed dependencies, Paseo daemon | Trusted computing base. A compromise is outside the 1.0 agent-containment claim. |
| Installed provider CLI at an admitted exact version | Trusted execution component. It must honor the frozen profile and provider policy. |
| Model output, prompts, repository content, dependencies, tests, and tool output | Untrusted inputs constrained by the trusted CLI, rootless OCI, fixed MCP scope, and engine-side authorization. |
| Repository lifecycle configuration | Untrusted executable intent. Non-empty automatic surfaces park before workspace creation unless an exact human approval exists. |
| Provider-native credential | May be visible to its trusted CLI. Director does not copy it into prompts, state, logs, support bundles, TaskStore, or agent-visible worktree paths. |
| Engine/TaskStore/GitHub/delivery credentials and authority | Never mounted or passed to governed agents. Only the engine performs delivery and Director lifecycle effects. |
| Root, kernel, container escape, malicious Director/Paseo revision, compromised provider CLI | Explicitly excluded from the 1.0 guarantee. |

Provider compromise is not silently treated as safe. It is outside the claimed
boundary and must be stated by onboarding, Doctor, and security documentation.
A later release that promises protection from a compromised provider or engine
requires a new human decision and new evidence.

## Alternatives considered

### Retain the fully adversarial provider/engine threat model for 1.0

Rejected for Linux 1.0. It requires a credential/egress broker, separate service
identity or host, and an aggregate storage-enforcement mechanism beyond the
approved monolith. It remains a valid hardening direction, not a current
release claim.

### Trust repository lifecycle configuration implicitly

Rejected. Enrolling a repository is not approval to run shell commands with the
daemon identity. Empty lifecycle configuration is admitted. Every non-empty
automatic surface requires a separately recorded human approval bound to exact
Project/Workspace/Task/Run scope and the canonical lifecycle digest. A changed
command invalidates the approval.

### Register an engine-created checkout as a directory workspace

This public Paseo path can avoid automatic worktree lifecycle execution. It is
not selected because PLAN §13.1 requires a Paseo-managed Execution
Workspace/worktree. The preflight refusal plus exact human-approval gate retains
that topology. Changing to directory workspaces remains a future explicit plan
decision, not an implicit fallback.

### Make hard aggregate filesystem quota a Linux 1.0 prerequisite

Rejected. Filesystem project quotas and privileged storage provisioning are not
uniformly available on supported hosts. Director instead requires finite
configured ceilings, preflight and periodic observation, bounded process output,
the existing 10% free-space floor, and fail-closed `Needs you` routing. This is
an availability/data-protection policy, not hostile-provider containment.

### Remove rootless OCI because provider CLIs are trusted

Rejected. Trust does not remove defense-in-depth requirements for prompt errors,
repository commands, dependency behavior, path mistakes, process descendants,
or accidental network access. The tested rootless OCI profile remains mandatory
for every governed provider Run.

## Decision

**Go.** Director 1.0 adopts the practical trusted-provider Linux boundary in
this ADR.

The following requirements are normative:

1. Doctor/preflight admits only exact Paseo/provider tuples with the ADR-0005
   MCP and policy proof. Fallback remains explicit and empty by default.
2. Every governed Task Agent, Reviewer Agent, and helper provider path runs in a
   rootless OCI boundary. It receives only the owned worktree, owned per-worktree
   Git administration/private objects where applicable, its provider-native
   authentication location, and its fixed stdio MCP channel. The root filesystem
   is read-only; capabilities are dropped; `NoNewPrivileges` is set; runtime
   sockets and raw Director/Paseo/GitHub delivery controls are absent; process,
   memory, time, output, and temporary-storage limits are finite.
3. The provider uses a non-host network namespace. Network availability follows
   the frozen human-approved profile and provider-native restrictions. Engine
   control endpoints and delivery credentials are never reachable through that
   network. This is defense in depth under the trusted-provider assumption, not
   a claim against a malicious provider binary.
4. Before any public Paseo worktree-creation call, Director canonicalizes the
   installed automatic lifecycle surfaces: `worktree.setup`,
   `worktree.teardown`, `worktree.terminals[*].command`, and
   `worktree.servicePorts.portScript`. Empty configuration proceeds. Non-empty
   configuration parks in `Needs you` unless an authenticated engine command
   with a server-derived human actor binds the exact fixed scope and lifecycle
   digest. The admission check executes no command. Agent-supplied, non-human,
   absent, stale, changed, or cross-scope approval parks.
5. An explicitly approved lifecycle operation is no longer automatic. The
   audit records its human actor, exact scope, digest, and resulting observation
   before later effects. Approval never grants provider, delivery, or cleanup
   authority.
6. The Git Candidate path uses the proven per-worktree/private-object mechanism.
   The engine imports and verifies the exact Candidate before review or delivery;
   ordinary shared refs and shared object storage remain outside agent write
   scope.
7. Mandatory finite operational limits cover at least free disk percentage,
   aggregate observed worktree bytes, process count, memory, and elapsed time.
   They are checked before launch and periodically while work is active. Any
   missing observation or exceeded limit prevents launch/continuation and parks
   in `Needs you`. Parking grants no cleanup or data-destruction authority.
8. The engine remains the sole owner of TaskStore, GitHub credentials, push,
   publication, integration, cleanup, and raw Paseo lifecycle effects. Textual
   agent claims never prove an effect.

This decision resolves only the M0.14 security-scope question. M1 remains
subject to every other M0 gate and may not begin until the reviewed Candidate is
integrated and the M0 exit gate confirms all remaining Tasks.

## Consequences

- ADR-0008 remains the durable maximal threat analysis, but its fully hostile
  engine/provider cases are excluded from the Linux 1.0 product guarantee by
  this human decision.
- Invariants 7–11 and 13–14 remain engine-enforced product invariants against
  untrusted model/repository input under the trusted runtime components. They
  are not guarantees after compromise of the engine, daemon, provider CLI,
  kernel, or root account.
- Provider-native authentication can be mounted only for its exact trusted CLI.
  It is never reclassified as a Director secret or made available to engine
  delivery operations, prompts, worktrees, TaskStore, or support output.
- Repository lifecycle refusal and operational-limit parking become mandatory
  preflight/reconciliation contract tests in M1 and later release suites.
  `dir-m1.8` owns their implementation together with the mandatory rootless-OCI
  execution boundary in the walking-skeleton path.
- Rootless OCI remains mandatory even though its protections are defense in
  depth rather than the sole trust boundary.
- The corrected Git Candidate-transfer mechanism remains valid and no longer
  appears as a release blocker.
- No non-Linux support or follow-up is created.

## Independent verification

Exact Candidate `a8f4822210037a0bac24aa2d6a79cb93a949dda3` received an
independent `approve_candidate` verdict and was integrated with its reviewed
base as merge `13e399ad6b0c837ef503f80e3aa29e145675679f`. `dir-m0.14`
records the human scope decision, focused policy and Git checks, review,
integration, cleanup, and excluded-compromise bounds.
