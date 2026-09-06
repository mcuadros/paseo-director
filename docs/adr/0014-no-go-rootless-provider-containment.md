# ADR-0014: Keep M1 blocked after the per-provider OCI path fails authority separation

- **Status:** Proposed
- **Date:** 2026-09-06
- **Beads Task:** `dir-m0.14`
- **Plan gate:** M0 operating-system authority separation
- **Decision owners:** `dir-m0.14` Task Agent for evidence; the human project
  owner for any architecture, trust, or product-scope change
- **Aligned with:** [ADR-0005](0005-session-scoped-mcp-provider-matrix.md),
  [ADR-0007](0007-git-worktree-ownership-and-cleanup.md), and
  [ADR-0008](0008-director-threat-model.md)
- **Platform scope:** [ADR-0011](0011-linux-only-platform-scope.md)

## Context

ADR-0008 proves that Paseo's default same-user provider topology cannot enforce
Director's engine-only effects, credential isolation, repository ownership, or
raw-control boundary against a prompt-injected agent or fully adversarial
provider/repository process. It identifies a per-provider OCI wrapper as the
smallest promising Linux experiment. ADR-0005 independently admits exact
Codex, Claude Code, and OpenCode tuples for session-scoped MCP and exact tool
preapproval, but explicitly makes no host-authority claim.

Director 1.0 remains Linux-only. This decision creates no requirement,
contingency, claim, or follow-up for another host operating system.

## Question or hypothesis

On exact Paseo 0.7.2 and the supported Linux host, can every ADR-0005 provider
run through Paseo's public command-array override in a rootless OCI container
that preserves provider authentication and session stdio MCP while the
operating system denies engine state/credentials, source and sibling paths,
raw Paseo and delivery controls, runtime control, and unapproved network; also
containing repository lifecycle code, preserving Candidate-capable Git work,
enforcing limits, reconciling interruption, and cleaning exactly owned state?

## Acceptance criteria

- Exercise the exact admitted provider binaries inside the candidate boundary.
- Prove owned-workspace access and denial of source, sibling, engine, host-home,
  raw-control, runtime-socket, and direct-network access.
- Exercise exact one-tool stdio MCP initialization, catalog, call, rejection,
  and closure through the boundary.
- Exercise provider-auth visibility without exposing a real credential.
- Exercise repository setup and teardown through the public Paseo worktree
  lifecycle.
- Prove the linked-worktree mapping either supports isolated Candidate writes
  or fails closed.
- Observe CPU, memory, process, file, temporary-disk, interruption/recovery,
  idempotency, and complete cleanup behavior.
- Return No-go if any common mechanism fails; do not spend authenticated model
  calls or weaken an invariant after a pre-provider failure.

## Evidence

The exact commands, versions, artifact hashes, fixtures, sanitized observations,
failure states, provider disposition, and cleanup proof are in
[the M0.14 evidence bundle](../evidence/m0.14/README.md).

The rootless OCI layer positively enforced a private user/mount/process/network
boundary with no effective capability and `NoNewPrivileges`; denied all
unmounted host paths, raw control executables/sockets, daemon loopback, and
public IPv4; preserved exact stdio MCP; applied observed cgroup/RLIMIT/tmpfs
limits; and supported exact labeled-container reconciliation and idempotent
cleanup. All three exact provider executables ran their version command inside
that boundary.

Those positive controls do not form an end-to-end Director boundary:

1. A real disposable `kind: "worktree"` creation and archive ran the committed
   setup and teardown commands before any provider process. Each command read a
   dummy engine credential, wrote engine and source state, reached the raw
   daemon listener, and found the Paseo executable with the daemon user's
   authority.
2. `--network=none` denied arbitrary egress but also denied authenticated
   provider operation. Restoring an ordinary container network restores
   arbitrary egress. The tested public contract supplies no scoped network or
   authentication broker.
3. A dummy read-only provider-auth file remained readable to the provider
   identity, as any directly consumed bearer credential must be. No public
   short-lived scope or credential broker was available.
4. Cgroups and per-file/tmpfs limits did not limit aggregate bytes in the host
   bind-mounted worktree.

Reviewer comment `01a077b9-82e8-70fc-86c8-4a2bbe72637f` invalidated the
original Git rationale. A corrected public-Git fixture now positively proves
that Git 2.47.3 can produce a legitimate Candidate while the shared common
directory remains read-only: the owned per-worktree gitdir stores `HEAD`, index,
and `refs/worktree/*`; `GIT_OBJECT_DIRECTORY` stores new objects privately;
`GIT_ALTERNATE_OBJECT_DIRECTORIES` reads the shared base objects; and a verified
bundle lets the engine import the exact Candidate. Shared heads and the complete
content/structure/mode digest outside the owned per-worktree gitdir remain
unchanged.

This bounded path removes Git from the No-go rationale. It does not resolve the
independent lifecycle-before-wrapper, provider credential/scoped-egress, or
aggregate writable-storage blockers above, so the overall outcome is unchanged.

The common failures occur before model behavior and apply equally to Codex CLI
0.147.0, Claude Code 2.1.258, and OpenCode 1.18.18. No real credential or paid
model turn was used after the stopping rule fired. ADR-0005 remains valid for
its narrower same-user MCP result, but none of its rows gains governed Director
execution authority from this experiment.

## Alternatives considered

### Treat the positive container denials as sufficient

Rejected. They do not cover the already-executed Paseo lifecycle process, do
not provide provider authentication, scoped egress, or aggregate worktree
quota. The corrected Git fixture means Candidate production is no longer part
of this rejection.

### Keep the original shared-Git impossibility conclusion

Rejected after review. A direct write to `refs/heads/*` does not establish that
Git cannot create a Candidate. The focused fixture proves the Task-side commit
and engine-side exact import with public Git mechanisms while the ordinary
shared refs and object store remain protected. Production recovery and scale
still require validation, but this ADR does not label the mechanism impossible
or require a trust-scope change merely to use private objects and bundle import.

### Add a root-managed systemd identity around each provider

A dedicated service identity can add mount, process, home, and address-family
restrictions, but Paseo 0.7.2 has no public per-session privileged launcher.
This option also leaves the lifecycle-before-provider, provider-credential,
scoped-egress, and aggregate-worktree-quota questions. It is not a
smaller supported end-to-end path on the tested host.

### Add an egress/authentication/effect broker or separate agent host

This is the credible direction for credentialless providers, scoped network,
private Git operations, and engine-only effects. It introduces a trusted
service/sidecar or remote-host topology and changes the approved modular
monolith, single-daemon paths, credential ownership, and possibly Candidate
production. It requires a human-approved plan ADR before implementation or a
larger spike; this Task cannot silently select it.

### Trust providers, models, and repository code

Rejected. It removes protection against prompt injection and compromised
dependencies and weakens PLAN invariants 7-11 and 13-14. No human approved that
scope reduction.

## Decision

**No-go.** The smallest public per-provider rootless-OCI mechanism does not
provide the complete Linux authority boundary required by ADR-0008 and
`dir-m0.14`.

M1 remains blocked. Director must not admit Codex, Claude Code, OpenCode, or a
future provider to governed unattended execution merely because its binary,
provider-native sandbox, MCP policy, or container filesystem checks pass. No
provider/model/permission/delivery fallback, provider exclusion, trusted-agent
scope reduction, private Paseo interface, sidecar, or remote agent host is
selected by this ADR.

The rootless OCI controls may be reused inside a later human-approved topology,
but they are defense in depth until the complete lifecycle, authentication,
network, disk, interruption, and cleanup contract passes together.

## Consequences

- PLAN invariants 7-11 and 13-14 remain unenforceable against fully adversarial
  agent/repository execution in the supported topology; ADR-0008's P0 stops
  remain active.
- Worktree creation with any repository executable surface remains refused for
  governed work. A provider wrapper is never evidence that setup or teardown
  was contained.
- A Task Agent does not need writable shared Git-common-directory authority to
  produce a Candidate. The supported candidate path uses an owned writable
  per-worktree gitdir, private new-object storage, read-only shared base objects,
  a per-worktree ref, and exact bundle verification/import by the engine.
- That focused Git path narrows this ADR; it does not independently approve its
  production recovery/scale implementation and is not a substitute for the
  remaining authority controls.
- Provider authentication and approved network access need a short-lived,
  scope-bound public mechanism or an explicitly approved broker. Mounting a
  long-lived bearer credential is not acceptable against a compromised
  provider binary.
- Cgroup limits remain required but insufficient; any future path also needs
  an enforced aggregate writable-storage bound and the plan's 10% recovery
  reserve.
- Doctor/preflight must fail closed rather than offer any ADR-0005 provider for
  governed execution while this gate is unresolved.
- This Task makes no non-Linux claim and creates no non-Linux follow-up work.
- The permitted next decision is human-owned: approve a Linux architecture
  change and its new M0 gates, reduce the trust/security scope explicitly, wait
  for an upstream public capability, postpone automatic execution/delivery, or
  keep M1 blocked.

## Independent verification

Pending. An independent top-level Reviewer Agent must reproduce the exact
Candidate from a detached clean checkout and verify the No-go result, installed
contracts, stopped authenticated-run boundary, absence of trust-scope
weakening, and complete cleanup. This Task Agent has not self-reviewed. No
publication, merge, closure, or workspace removal is authorized by this
Candidate.
