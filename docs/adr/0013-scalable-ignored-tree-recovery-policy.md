# ADR-0013: Bound automatic ignored-tree recovery by measured Linux resources

- **Status:** Accepted
- **Date:** 2026-09-06
- **Beads Task:** `dir-m0.17`
- **Plan gate:** M0 scalable ignored-tree recovery on the supported Linux topology
- **Decision owner:** `dir-m0.17` Task Agent; expanding a limit requires new
  evidence and the human project owner
- **Amends:** [ADR-0007](0007-git-worktree-ownership-and-cleanup.md), replacing
  its provisional recovery ceilings with this measured supported Linux policy
- **Scope dependency:** Accepted
  [ADR-0011](0011-linux-only-platform-scope.md) defines Linux-only support for
  Director `1.0`; this ADR sets ignored-tree policy within that scope.

## Context

ADR-0007 defines the unified pre-destructive gate for exact owned worktree
cleanup. Its `dir-m0.6` Candidate
`af489d04d4f84bd60d39e575718884e0b645ee36` admits an owner-verified private
artifact for ignored and Git-invisible material in an integrated,
prospective-tree-clean worktree. Ignored bytes never enter Git. Dirty work plus
ignored material, unsupported file types, unprovable ownership, or failed
preservation stops in Needs you.

That Candidate deliberately treats 2 GiB aggregate/per-file, 10,000 recovery
entries, 100,000 inspected entries, a 64 KiB stream buffer, seven-day
retention, and 10% free space as evidence ceilings rather than release
defaults. It records the former 250,000-entry assumption as unproven and
delegates representative `node_modules`, `target`, and `.venv` measurements to
this Task. This ADR does not modify or publish `dir-m0.6`.

The `dir-m0.6` Candidate was independently approved unchanged and integrated
as `2eb0b692ca027cff2f5e2d9d1de364165e6cafa3`. This ADR's measured policy
therefore finalizes, rather than forks, that cleanup contract.

Large ignored trees are both a recoverability asset and a denial-of-service
input. An automatic policy that is too broad can exhaust memory, disk, or
cleanup time. A policy that is too narrow can park ordinary completed Tasks.
The release envelope therefore needs measured entry, byte, per-file, memory,
time, disk-reserve, retention, secrecy, ownership, retry, restoration, and
cleanup behavior on the supported Linux topology.

ADR-0008 was authoritative when this evidence was produced. Accepted ADR-0014
later superseded its M1-blocking Decision while retaining its maximal threat
analysis. These checks protect engine cleanup from accidental, buggy, or
ambiguous state; they are not containment against a hostile same-user process.
ADR-0014 records the resolved practical trusted-provider Linux boundary.

## Question or hypothesis

On the supported Linux topology, can the unified ADR-0007 private-artifact
lifecycle preserve a representative ignored tree at realistic automatic limits
with bounded streaming memory and wall time, exact required restoration, a
non-reducible 10% disk floor, path/content-free public state and logs, exact
owner/squatter refusal, and idempotent crash/retention recovery?

## Acceptance criteria

- One disposable composite tree is Git-ignored and represents dense
  `node_modules`, dense `.venv`, and large-file `target` layouts at the exact
  default entry, aggregate-byte, and per-file limits.
- Exact defaults pass. One entry, aggregate byte, per-file byte, inspected
  entry, buffer byte, or retention millisecond beyond policy is refused before
  destructive work; Project/Task policy can only tighten the envelope.
- Copy, hash, all five ADR-0007 gate revalidations, and restore reuse one 64 KiB
  buffer per sequential pass. Measured RSS growth, per-phase time, total
  lifecycle time, and an external worker deadline remain bounded.
- Disk admission charges remaining content plus 64 KiB fixed and 4 KiB per
  entry. Projected free space after that reservation is at least 10% of the
  relevant filesystem on creation, retry, and restore.
- Restored regular-file bytes, sizes, mtimes, modes, directory mtimes/modes,
  and empty directories match the private manifest.
- The artifact root, staging directory, payload, manifest, owner envelope, and
  worker report root use owner-only modes; exact Task/Run/nonce/Candidate and
  filesystem identity are revalidated. Foreign or replaced artifacts are
  preserved and route to one path-free Needs-you result.
- A crash after atomic artifact rename is adopted without duplication. Expiry
  before seven days is scheduled, and a crash after guarded deletion is
  reconciled idempotently.
- Entry names, individual hashes, contents, and absolute paths remain only in
  the private artifact. TaskStore-shaped public state and stdout/stderr contain
  only scope, policy, aggregate counts/digests, phases, and measurements.
- The identical Linux harness run removes every Task-owned temporary root and
  worker after success or failure.

## Evidence

The harness, exact aggregate transcript, primary sources, and cleanup proof are
in [`docs/evidence/m0.17/README.md`](../evidence/m0.17/README.md).

The final measured run used Debian 13.6, Linux
`6.12.107+deb13-amd64` x86-64, Node.js `v26.7.0`, and Git `2.47.3`.
Its composite fixture contained exactly 10,000 recovery entries: 6,051 in a
`node_modules` shape, 3,302 in a `.venv` shape, and 647 in a `target` shape.
That was 9,037 regular files, 963 directories including one empty directory,
512 MiB total content, and a 256 MiB largest file.

The final rebased lifecycle took 255.711 seconds and increased sampled peak RSS
by 109,395,968 bytes. Artifact create/crash/adopt took 128.922 seconds; each of
five full-byte source-plus-artifact gate passes took 1.540–1.572 seconds; exact
restore and verification took 116.612 seconds. The fixture generator is
reported separately and took 2.755 seconds. These are observations
on the recorded tuple, not general latency guarantees.

The same run accepted exact limits and refused 10,001 entries, 512 MiB plus
one aggregate byte, and 256 MiB plus one per-file byte. Sparse oversize probes
were rejected from metadata before any content function was called. Exact 10%
projected free space passed while one byte below it failed. Policy expansion
for recovery/inspection entries, aggregate/per-file bytes, buffer, retention,
time, memory, or the disk floor was rejected.

A scaled hard worker deadline terminated the worker while preserving the owned
source sentinel and byte-identical intent. The timeout probe passed twice and
left no process or temporary root. The private artifact was adopted after an
injected crash between atomic rename and result persistence, survived foreign
creation and retention squatters, passed five complete source/artifact byte
gates, restored exact bytes and required metadata, remained scheduled before
expiry, and reconciled a crash after guarded expiry deletion. Repeated cleanup
observed verified absence. The disposable secret marker, its entry name, and
every temporary absolute path were absent from public state and the JSON
report.

## Alternatives considered

### Keep the 2 GiB and 100,000-entry evidence ceilings as defaults

Rejected. Those boundaries have unit evidence but no representative full
lifecycle timing. The inherited review measured approximately 17.77 seconds
for 5,006 entries, 93.24 seconds for 20,021, and 245 seconds for 30,031 in an
earlier probe. Treating the broader ceilings as defaults would convert a safety
bound into an unsupported performance claim.

### Persist ignored material in Git recovery refs

Rejected. Ignore rules commonly cover credentials, databases, build outputs,
and other material that must not become Git-reachable. ADR-0007 correctly uses
a permission-restricted non-Git artifact and path-free public state.

### Delete integrated worktrees without ignored recovery

Rejected. Integration proves committed history, not that ignored local data is
worthless. Silent deletion violates PLAN invariant 11. Unsupported or
over-limit trees must remain intact in Needs you.

### Always route any ignored tree to Needs you

Rejected. The measured composite envelope is practical on the recorded Linux
tuple and lets ordinary integrated, prospective-tree-clean Tasks finish
automatically. Failing every ignored tree would defeat the PLAN cleanup default
without a safety benefit inside the proven envelope.

### Increase concurrency or parallelize file copies

Rejected for the release policy. Parallelism increases open-file, memory,
disk-queue, and race exposure. The measured policy is deliberately sequential
and uses one fixed buffer. A future expansion requires new representative
Linux evidence and a superseding decision.

## Decision

**Go.** Adopt the following release policy for automatic ignored-tree recovery
on the supported Linux topology:

| Resource | Default and hard expansion ceiling |
|---|---:|
| Recovery entries, including files and directories | 10,000 |
| Inspected worktree entries | 25,000 |
| Aggregate ignored content | 512 MiB |
| One regular file | 256 MiB |
| Sequential stream buffer | 64 KiB |
| One copy/hash/verify/restore phase | 180 seconds |
| Full artifact lifecycle | 480 seconds |
| External worker supervisor | 540 seconds |
| Measured worker RSS growth | 192 MiB |
| Recovery retention | Seven days |
| Projected free-space floor | 10% of the relevant filesystem |
| Disk reservation | Remaining content + 64 KiB + 4 KiB per entry |
| Full pre-destructive artifact revalidations | Five |

Project and Task policy may tighten any maximum, increase the free-space floor,
or shorten retention. It cannot expand a maximum, lower the floor, extend
retention, parallelize streaming, or skip a gate without new Linux evidence
and an approved superseding ADR.

An over-limit, timed-out, memory-exhausted, disk-pressured, changed,
unsupported, unowned, replaced, unreadable, or otherwise unprovable tree
remains in place and enters Needs you with a path/content-free reason. A killed
worker never authorizes deletion. Retry starts from durable intent, adopts only
an exact verified artifact, and otherwise preserves every unknown path.

## Consequences

- The ignored-tree policy stop condition is resolved for the supported Linux
  topology by the independently approved and integrated Candidate recorded
  below. Other M0 stop conditions remain independent.
- The release policy uses the ADR-0007 normalized ignored-root discovery,
  exact owner envelope, private manifest/payload, atomic rename, unified gate,
  and guarded expiry. This Task adds policy/supervision evidence; it does not
  fork cleanup ownership or weaken any ADR-0007 check.
- Entry counts include both regular files and directories. Symlinks, nested
  repositories, FIFOs, sockets, devices, xattrs, ACL/owner restoration,
  sparse-allocation fidelity, network filesystems, unstable filesystem IDs,
  and trees that change during capture remain fail-closed and require human
  recovery.
- The private manifest necessarily contains entry paths and individual content
  hashes. It is recovery data beneath the exact owner-only artifact, not
  TaskStore, audit, logs, diagnostics, or support-bundle data. Public durable
  state stores only an opaque artifact ID, aggregate digests/counts, scope,
  limits, phases, and expiry.
- The 10% check is repeated against the artifact filesystem at creation and
  every retry and against the restoration filesystem before restore. Existing
  verified bytes are charged zero on adoption; absent or partial artifacts and
  restores reserve all remaining bytes. Hitting any disk or time bound does not
  trigger force cleanup.
- Expiry runs only after the enclosing cleanup is complete. Before expiry the
  artifact is scheduled `retained`, not Needs you. At expiry, exact root,
  artifact identity, owner, permissions, manifest, and payload are reverified
  before a removal-ready intent. Unknown replacements survive.
- The integrated `dir-m0.6` Candidate and this integrated policy Candidate are
  the binding evidence pair. A later change to either contract requires fresh
  Linux evidence and a superseding ADR.

## Independent verification

Exact Candidate `18c0bbfaa58c09600eb01ae68fe4b39d916c92e2` received an
independent `approve_candidate` verdict and was integrated with its reviewed
base as merge `1d93c7744d006da78dff54e3f1dbbb95fc833eb8`. `dir-m0.17`
records the representative Linux measurements, limit checks, review,
integration, cleanup, and residual filesystem bounds. This status
reconciliation uses that integrated evidence and does not rerun the expensive
large-tree benchmark.
