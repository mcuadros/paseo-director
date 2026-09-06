# ADR-0007: Require exact ownership and secret-safe recovery before Git cleanup

- **Status:** Proposed
- **Date:** 2026-09-06
- **Beads Task:** `dir-m0.6`
- **Plan gate:** M0 Git worktree ownership and Windows/Linux cleanup
- **Decision owner:** `dir-m0.6` Task Agent; platform reduction requires the human project owner

## Context

Director must create one isolated Execution Workspace for a top-level Task
Agent, create detached disposable checkouts for independent top-level Reviewer
Agents, preserve dirty work, and clean only exact Task-owned resources after
verified integration. PLAN v0.3 and ADR-0010 make that top-level parentage and
sole Task ownership explicit; they do not relax Git ownership or recovery.

Recorded paths and SHAs become stale. The source checkout, Git common
directory, origin, base, Task ref, recovery ref, or worktree may be replaced
between creation and cleanup or between an effect and result persistence. Git
also excludes ignored material from ordinary dirty status, can represent a
nested repository as a gitlink instead of preserving its worktree, and can run
repository-configured filters, hooks, or filesystem monitors during inspection
and snapshot operations.

The installed `@getpaseo/protocol@0.7.2` schema exposes four automatic
repository worktree surfaces: `worktree.setup`, `worktree.teardown`,
`worktree.terminals`, and `worktree.servicePorts.portScript`. Any can execute
repository-controlled commands during workspace lifecycle. ADR-0008 therefore
remains authoritative: path validation and worktree separation are not an OS
authority boundary. `dir-m0.14` separately owns containment of Task Agents,
Reviewer Agents, helper subagents, and repository code.

Integrated ADR-0005 admits exact Linux tuples for session-scoped MCP and tool
preapproval only. It explicitly preserves ADR-0008, makes no Windows or host-
containment claim, and does not authorize Git cleanup by an agent. Engine-owned
cleanup and the Windows gate here remain independent M0 requirements.

Integrated ADR-0003 proves that exact Paseo 0.7.2 archive and restart state
must be reconciled before cleanup. Every Task Agent, Reviewer Agent, and helper
must satisfy its full termination predicate before this Git contract runs.
Because native workspace archive can remove its managed worktree while leaving
Task refs, recovery/snapshot artifacts and `removal_ready` evidence must be
durable before that archive; afterward this contract reconciles path/
registration absence and owns only exact residual Git/ref cleanup.

## Question or hypothesis

Using supported Git CLI behavior and cross-platform Node.js 22-or-newer APIs,
can the engine revalidate exact durable ownership before every cleanup effect
and retry, protect all uncommitted material without blindly committing possible
secrets, bind deletion to the live fetched base, and reconcile interruption and
locked-file failures on real Linux and Windows hosts?

## Acceptance criteria

- Every worktree, local-ref, recovery-ref, and remote-ref mutation revalidates
  the exact lexical/canonical source path, filesystem identity, Git common-dir
  identity, origin URL/filesystem identity, and durable Task/Run/nonce/ref/SHA
  ownership. Retry performs the same checks.
- Git worktree registration paths are converted to the same native realpath,
  separator, drive-letter, and case comparison form as Node paths. A positive
  owned path/branch/HEAD registration must match before removal and exact owned
  registrations must be absent afterward.
- Source, common-directory, and origin replacement after worktree removal is
  refused before a matching ref in the replacement repository can be deleted.
- Tracked and ordinary untracked worktree data is captured in an exact hidden
  recovery commit/ref; when the real index differs from Candidate, its exact
  tree receives a second hidden recovery commit/ref. Cleanliness is never taken
  from `git status`: a fresh
  temporary index reads the Candidate with sparse settings disabled, adds the
  actual worktree, writes its prospective tree, and compares that tree to the
  Candidate. Assume-unchanged and present skip-worktree edits therefore recover;
  missing sparse material or byte-normalizing attributes stop path-free.
- Dirty work combined with Git-ignored files/directories or unignored nested
  repositories stops in Needs you without logging paths/content. Git-invisible
  empty directories can use the private artifact alongside a Git snapshot. For a successfully integrated
  prospective-tree-clean worktree, ignored files/directories are preserved in
  a bounded owner-verified non-Git artifact before automatic removal, never
  force-added to Git. Task policy cannot exceed the 2 GiB aggregate/per-file,
  10,000 recovery-entry, 100,000 inspected-entry, or 64 KiB buffer evidence
  ceilings and cannot reduce the 10% free-space floor.
- Snapshot inspection allows declared filter attributes only when no clean or
  process command is configured, refuses executable clean filters, overrides
  repository hooks and `core.fsmonitor`, and records the residual same-user race boundary.
- All automatic executable fields in the exact installed Paseo schema are
  refused without separate human approval and `dir-m0.14` containment.
- Remote absence is accepted only from `ls-remote --exit-code` status 2.
  Transport/authentication failures stop; the exact live base is inspected and
  fetched without a tracking-ref update immediately before branch deletion;
  Candidate ancestry and local/remote deletion postconditions are verified.
- Local Task-ref deletion stops in Needs you if any registered worktree other
  than the owned original/quarantine path still reports that branch. The
  consumer's checkout, files, branch, and HEAD remain intact. Local deletion
  persists exact ref/SHA/nonce intent before `update-ref`, and retry accepts
  absence but parks on any present ref after an unknown result.
- A clean or snapshotted worktree receives durable removal-ready evidence
  before its quarantine/removal effect. Recovery handles crashes after
  quarantine, after removal, and after local or remote deletion without blind
  retry. Every removal-ready resume and immediate pre-removal boundary streams
  and verifies every preserved non-Git payload byte against the bound private
  manifest before the worktree can move.
- `complete` is terminal for destructive effects. Repeated reconciliation may
  observe confirmed absence, but any later local or remote Task-ref appearance,
  including the same Candidate SHA, is preserved and parks in Needs you.
- Every plain clean path proves the Candidate is contained by the live fetched
  base before persisting removal-ready and again immediately before move.
- Filesystem scanning is independent of Git visibility and ignored-path
  presence. Nested `.git` files/directories, submodules, untracked symlinks/
  reparse points, FIFOs, sockets, and other special types stop path-free; a
  tracked mode-`120000` symlink is byte-verified and admitted, and empty
  directories enter non-Git recovery.
- Windows exercises two real `FileShare.None` boundaries in one run. A dirty
  worktree must fail snapshot recomputation with the exact error
  `GitCommandError: git add exited 128`, leaving unchanged
  `snapshot_verified` intent and recovery data. A separate clean integrated
  worktree first persists `removal_ready`, recomputes its real index and
  prospective tree on resume, then acquires the held handle immediately before
  and must fail the quarantine move with exact error
  `GitCommandError: git worktree move exited 128`, retaining original
  path/registration/ref/data.
  Each releases the handle and retries through removal and completion. The
  remote job must observe every fact rather than infer it.

## Evidence

The self-cleaning harness, installed-schema hashes, complete reviewer-fault
matrix, Linux transcript, Windows procedure, and cleanup proof are in
[`docs/evidence/m0.6/README.md`](../evidence/m0.6/README.md).

On Debian 13 / Linux `6.12.107+deb13-amd64` x86-64 with Node.js
`v26.7.0` and Git `2.47.3`, the corrected harness repeatedly passed every
original case plus these review-derived cases:

- exact identity is rechecked at reconciliation entry, before recovery-ref
  creation, before expected-old local deletion, before explicit-lease remote
  deletion, and during postcondition observation;
- matching refs in replacement source/common-dir/origin fixtures survive after
  their worktrees are already gone;
- a path-normalized positive registration assertion precedes removal, stale
  registration absence is checked afterward, and a real unknown worktree
  consuming the Task ref blocks local deletion without damaging its state;
- a prospective-tree-clean integrated fixture preserves ignored file/directory
  bytes and metadata in a permission-restricted non-Git artifact, removes its worktree,
  restores the data, enforces disk/entry/byte bounds plus seven-day retention,
  refuses artifact replacement, and reconciles interrupted creation/removal;
- prospective-tree fixtures recover exact edits hidden by assume-unchanged and
  skip-worktree despite empty status; missing sparse material and EOL
  normalization stop without worktree/ref loss or false byte-exact claims;
- a staged-only index version that differs from both Candidate and worktree is
  captured in a separate exact recovery commit/ref before any cleanup effect;
  an effect-before-state interruption leaves index/worktree/Task refs intact,
  and detached restoration reproduces the staged bytes;
- every removal-ready retry recomputes the real index and prospective worktree
  trees and byte fidelity; a same-size edit with restored mtime is refused with
  original path, registration, refs, and edited bytes intact;
- corrupting only a manifest-listed recovery payload byte is detected both on
  removal-ready resume and at the immediate pre-move boundary; the original
  worktree, registration, local/remote Task refs, and source bytes remain;
- an unintegrated prospective-tree-clean worktree remains registered with both
  Task refs and no removal-ready evidence;
- dirty-plus-ignored files/directories and an uncommitted unignored nested
  repository remain on disk and return path-free Needs-you diagnostics;
- a committed mode-`120000` symlink and declared inactive filter are admitted,
  while untracked links/reparse points and executable clean filters still stop;
- an empty untracked directory coexists with dirty Git recovery through the
  verified non-Git artifact and restores as an empty directory;
- all four installed Paseo worktree execution surfaces are individually
  refused, an active clean filter is refused, and a configured index hook plus
  filesystem monitor never execute under the snapshot command boundary;
- status 2 is distinguished from real loopback authentication and offline
  failures; a vanished origin is classified external-unavailable; live-base
  rewind blocks deletion; one race is caught by just-in-time comparison and a
  later injected race is rejected by the actual explicit lease; and remote-
  delete effect-before-result retry confirms absence rather than deleting
  again;
- same-SHA remote recreation after an interrupted delete is explicitly
  ambiguous and parks without deleting; forge resolution remains `dir-m0.7`;
- local deletion intent reconciles a crash after `update-ref`; after both ref
  absences become confirmed and cleanup completes, same-SHA local and remote
  recreations are observed, preserved, and parked without another delete;
- exact 2 GiB aggregate/per-file ceilings accept their boundary and reject one
  byte more, while exactly 10% free space is accepted and 9% is rejected;
- clean and dirty paths persist removal-ready evidence, reconcile missing
  paths only after Git registration is also absent, and preserve the same
  recovery SHA across effect-before-state interruptions;
- an injected crash immediately after the quarantine move is exercised; a
  later file makes snapshot verification fail while preserving that file, and
  cleanup resumes idempotently after the fixture removes it; and
- the complete temporary root and loopback fault process are absent after each
  run.

Real Windows evidence does not yet exist. The scoped post-review
`windows-2025` workflow is limited to PRs targeting `main` that change one of
the four exact `dir-m0.6` artifacts. It has a 10-minute limit, read-only
contents permission, exact event head/base validation, no secrets or matrix,
and two identical harness runs whose exit codes are checked independently. It
records `ImageOS`, `ImageVersion`, runner OS/architecture, OS build, Node, and
Git. The checkout uses current official `actions/checkout@v7.0.1`, pinned to
exact commit `3d3c42e5aac5ba805825da76410c181273ba90b1`; its immutable
`action.yml` declares Node.js 24. The dirty and clean held-handle cases above
must both pass in this same job before any Windows claim.

The Windows ACL helpers carry the path only in the dedicated child environment
field `DIRECTOR_RECOVERY_ACL_PATH`; no path is appended to or interpolated into
the PowerShell `-Command` string. One invocation sets an inheritance-protected
current-SID-only rule and another verifies SID, allow type, full-control rights,
and both inheritance flags. PowerShell is absent on the Linux evidence host,
so the real Windows run remains the authority for this plumbing and ACL result.

## Alternatives considered

### Trust the original repository after worktree removal

Rejected. Independent review reproduced deletion of a matching ref from a
replacement repository. Durable IDs describe intent, while lexical/canonical,
filesystem, common-dir, and origin facts prove the current mutation target.
Both are required before every effect and retry.

### Snapshot all ignored files automatically

Rejected. Ignored material commonly contains credentials, local databases,
and generated artifacts. Blindly making it reachable through a Git ref trades
data loss for secret persistence. For integrated prospective-tree-clean
worktrees, the selected mechanism uses a non-Git artifact below an exact engine-owned root:
owner-only mode/ACL, private payload/manifest, content hashes and original
metadata, no path/content logging, policy-derived entries/bytes/stream buffer,
a 10% disk reserve rechecked on every retry, seven-day retention, restoration
verification, and guarded idempotent expiry.
Dirty-plus-ignored or unsupported material still stops in Needs you.

### Treat a nested repository as ordinary untracked data

Rejected. `git add` can record only the nested `HEAD` gitlink and omit its
dirty/untracked content. Automatic cleanup stops while the nested `.git`
boundary and its files remain intact.

### Rely on disabled hooks without checking attributes

Rejected. `core.hooksPath` and `core.fsmonitor` overrides prevent those
repository mechanisms, but an applicable clean filter is invoked by `git add`
through attributes/config. The snapshot must preflight every present path and
stop when its declared driver has a configured `clean` or `process` command;
a declaration without either executable is inert. OS containment remains necessary for a hostile
same-user race after the check.

### Treat every nonzero `ls-remote` result as absence

Rejected. Git reserves status 2 for a missing matching ref under
`--exit-code`; authentication, transport, and repository failures use other
statuses. Only confirmed status 2 is idempotent absence.

### Use stale remote-tracking state for integration proof

Rejected. The engine reads the live base ref, fetches that exact object with
`--no-write-fetch-head`, proves Candidate ancestry, then repeats the proof
immediately before deletion. Relevant base rewrites stop cleanup.

### Remove directly without durable preparation

Rejected. A clean worktree has no snapshot marker from which to infer an
effect-before-result crash. A verified removal-ready phase records exact
worktree identity, Candidate, prospective tree, aggregate filesystem-metadata
digest, real index tree, clean/snapshot status, and both recovery SHAs before a
Git move/remove. Resume and immediate pre-move checks recompute both trees;
unexplained disappearance remains an error.

### Delete a Task ref while another worktree consumes it

Rejected. Exact SHA ownership does not make it safe to invalidate an unknown
checkout's symbolic branch. The local delete guard enumerates normalized Git
registrations immediately before mutation and routes any other consumer to
Needs you.

### Infer Windows behavior from Linux

Rejected. POSIX permits rename/unlink with an open descriptor, while Windows
sharing rules may block move or removal. Only the real bounded post-review job
can resolve that platform gate.

## Decision

**Inconclusive.** The corrected exact-ownership, live-base,
secret-safe-recovery, executable-surface, removal-ready, and reconciliation
contract is the sole candidate mechanism. Linux evidence supports it on the
recorded tuple, but the mandatory real Windows result is pending. The separate
large-tree policy evidence in `dir-m0.17` is also open. M0/M1 remain blocked
until those gates pass and the evidence-changing Candidate receives a fresh
independent top-level Reviewer Agent verdict.

The engine stops rather than deletes when dirty work and ignored/
unsnapshotable material coexist, non-Git preservation cannot be proved,
filters or locks fail, ref/identity facts are ambiguous, transport/auth fails,
the live base changes, or the filesystem is unsupported. Prospective-tree-clean
integrated worktrees with policy-bounded ignored files/directories use the
selected private non-Git recovery lifecycle and are removed automatically. No
recursive-force fallback, Git secret snapshot, Linux-only fallback, or platform
reduction is selected.

Repository `paseo.json` worktree commands remain a separate launch gate.
Configured top-level `scripts` are not automatically executed by workspace
creation in the installed schema, but any later request to run one still
requires explicit authority and containment. If Paseo cannot create a managed
worktree without an unapproved automatic surface, Director must obtain a public
skip mechanism or keep launch blocked.

## Consequences

- Future Git adapter commands must use the same durable ownership envelope and
  fresh repository/base checks; cached success never authorizes a later retry.
- This preserves PLAN §§9.2, 15.5, and 25: successfully integrated
  prospective-tree-clean worktrees still clean automatically, bounded private preservation protects
  ignored value/secrets, disk thresholds stop unsafe copying, and Needs you is
  reserved for a preservation/removal contract the engine cannot prove.
- The bounded evidence recovery envelope is 2 GiB aggregate and per file,
  10,000 recovery entries, 100,000 inspected entries, a 64 KiB streaming
  buffer, seven days, and 10% free space. Task policy may tighten but cannot
  expand either byte cap or the other evidence ceilings, and cannot weaken the
  10% floor, until `dir-m0.17` resolves the release policy. Oversized
  sparse files stop from `lstat` before reads; hashing, copying, verification,
  and restoration share bounded buffers. Each retry charges only bytes still
  to be written while rechecking the 10% floor.
- Review measurements for the former default were about 17.77 seconds at 5,006
  entries, 93.24 seconds at 20,021, and 245 seconds at 30,031. The corrected
  contract deliberately revalidates payload bytes at both destructive
  boundaries; `dir-m0.17` owns representative Linux/real-Windows benchmarks
  and the release policy for larger generated trees. It is
  a P0 sibling under `dir-m0`, discovered from this Task, so the broader M0
  cleanup claim remains blocked without expanding `dir-m0.6`.
- Registration comparison must retain its positive pre-removal assertion;
  negative-only tests are insufficient for Windows separators and 8.3 aliases.
- Recovery-ref expiry is a separate guarded local-ref effect with identical
  identity, expected-SHA, active-consumer, and secret policy checks.
- A dirty worktree remains ineligible for review. Exact worktree and staged-
  index recovery snapshots authorize cleanup only while recomputed trees still match.
- Ignored data never enters Git recovery. Integrated prospective-tree-clean
  work uses the private non-Git artifact and default seven-day retention;
  Git-invisible empty directories may accompany a dirty Git snapshot, while
  dirty-plus-ignored, unignored nested repositories, untracked links/reparse
  points or other special files, ownership or
  permission uncertainty, and exhausted preservation/disk bounds enter Needs
  you without names or contents in durable output.
- The ignored-artifact manifest is private recovery data, not TaskStore/audit/
  support output. Durable state stores only owner scope, aggregate sizes,
  hashes, retention, paths, and permission model; retention cleanup repeats
  exact identity/owner checks and reconciles effect-before-result interruption.
- Entry names and individual content hashes never enter assertion/domain-error
  payloads. Current-versus-artifact comparisons use aggregate digests and map
  content/owner/squatter mismatch to one path-free Needs-you result.
- Retention-before-expiry is scheduled `retained` state, not Needs you. Foreign
  owner/path/permission/content facts map to path-free Needs you.
- State writes use a permission-restricted next file, file `fsync`, atomic
  rename, and POSIX directory `fsync`. A partial next file leaves the prior
  intent readable; an invalid primary intent fails closed before effects.
- Local and remote deletion intents make a same-SHA branch recreation after an
  unknown delete result explicitly ambiguous. Confirmed completion is terminal:
  a later local or remote ref is observed and parked, never deleted again. The
  ref remains intact in Needs you pending the forge-aware ownership decision in
  `dir-m0.7`.
- `dir-m0.7` still owns forge integration races; `dir-m0.10` owns the general
  effect/reconciliation contract; `dir-m0.14` owns OS authority; and
  `dir-m0.17` owns large ignored-tree policy/benchmarks. This spike does not
  absorb or resolve those Tasks.
- ADR-0003 owns agent/native-workspace termination and archive. Director orders
  it with this contract as: terminate/reconcile all agents, preserve worktree
  data and persist removal-ready, archive/reconcile the native workspace, then
  clean exact residual Git refs/resources. A missing worktree before recovery-
  ready remains an error.
- PLAN v0.3 and ADR-0010 require the development Task Agent to remain the sole
  branch/Candidate owner and a fresh Reviewer Agent to be independent and
  top-level. No helper can supply the required verdict.
- ADR-0005 changes no cleanup authority: admitted MCP tuples remain defense in
  depth and Linux-only until their separate gates expand.

## Independent verification

The previous Candidate received `changes_requested`; all P0/P1/P2/P3 findings
are addressed by the changed harness and evidence but require a completely
fresh review of the final exact Candidate/base. Windows CI, publication, PR,
merge, Task closure, and final branch/workspace cleanup remain post-review
gates.
