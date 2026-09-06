# ADR-0011: Make Linux the sole Director 1.0 platform

- **Status:** Accepted
- **Date:** 2026-09-06
- **Beads Task:** `dir-m0.19`
- **Plan gate:** M0 platform scope and release readiness
- **Decision owner:** Human project owner
- **Amends normative platform scope in:** PLAN §§3.1, 8.1, 19.3, 21.1, 21.3, 23, 24.5, and 27; [ADR-0002](0002-paseo-0.7.2-public-surface.md), [ADR-0003](0003-paseo-0.7.2-lifecycle-recovery.md), [ADR-0005](0005-session-scoped-mcp-provider-matrix.md), [ADR-0007](0007-git-worktree-ownership-and-cleanup.md), [ADR-0008](0008-director-threat-model.md), and [ADR-0009](0009-public-licensing-and-distribution.md)

## Context

The approved plan and several M0 contracts mixed Linux evidence with
requirements for another host operating system. That made the `1.0` scope and
milestone gates broader than the product owner now authorizes.

The human project owner explicitly selected a Linux-only `1.0`. Existing
Linux evidence remains applicable to its recorded versions and topology.
Platform-specific workflow history is not an active repository contract and
does not establish a support result.

## Question or hypothesis

Can Director define a coherent Linux-only `1.0` by removing every other
platform requirement while preserving Linux evidence, product behavior, and
all unrelated safety gates?

## Acceptance criteria

- Linux is the sole `1.0` platform and release gate.
- Remove the platform-specific GitHub Actions workflow that is outside this
  scope.
- Remove non-Linux requirements, support statements, and active-contract
  clauses from the plan, affected ADRs, repository skills, and open Beads
  records.
- Preserve existing Linux evidence and cleanup behavior.
- Require a new explicit plan decision and evidence before expanding the
  supported operating-system scope.

## Evidence

- The repository audit identifies every affected normative plan, ADR, skill,
  workflow, evidence-contract, and open Beads field.
- The resulting diff deletes the out-of-scope workflow and changes no product
  or cleanup implementation.
- Existing Linux evidence remains bounded to its recorded versions, host, and
  topology.

## Alternatives considered

### Retain a multi-platform 1.0 scope

Rejected by the human project owner. It would preserve release and task gates
outside the approved product scope.

### Keep inactive platform obligations for later milestones

Rejected. Deferred obligations would still be active `1.0` requirements and
would contradict the Linux-only decision.

### Infer support from portable implementation

Rejected. Portable code and static checks do not authorize a support claim for
an operating system outside the approved scope.

## Decision

**Go.** Director `1.0` is Linux-only. Linux is the sole platform for M0
evidence, implementation acceptance, continuous integration, release
readiness, public beta, and stable `1.0.0`.

Remove `.github/workflows/dir-m0.6-windows-post-review.yml`. Remove every
non-Linux platform obligation from active repository and Beads contracts. Do
not preserve a deferred gate, compatibility claim, failure claim, or required
follow-up for an out-of-scope operating system.

Any future expansion of the supported host operating systems requires a new
human-approved plan decision, explicit compatibility bounds, reproducible
evidence, and corresponding milestone and release gates. It is not part of
Director `1.0`.

## Consequences

- M0 and later milestones require Linux evidence only; every unrelated stop
  condition remains blocking.
- `dir-m5.10` owns Linux and performance-scale validation only.
- Doctor, documentation, packaging, and release metadata must describe Linux
  as the sole supported `1.0` host operating system.
- Existing portable implementation may remain, but it creates no support
  contract outside Linux.
- Existing Linux evidence, security boundaries, exact-SHA rules, recovery
  requirements, and cleanup behavior are unchanged.

## Independent verification

Exact Candidate `ca8d37c18d7e10f899b124a4e3bdfdc24b4f86c7` received an
independent `approve_candidate` verdict and was integrated with its reviewed
base as merge `ab32567f3ce12170b6c3b9531f6a8d926a8b0886`. `dir-m0.19`
records the Linux-only scope checks, review, integration, cleanup, and
compatibility bounds.
