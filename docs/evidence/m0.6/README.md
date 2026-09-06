# dir-m0.6 Git worktree ownership and cleanup evidence

- Evidence date: 2026-09-06
- Repository base: `ae06c376002b079f55f9fdbd92b9b0e70466f422`
- Plan context: PLAN v0.3; accepted ADR-0010 top-level Task/Reviewer Agent semantics
- Related M0 context: integrated ADR-0005 keeps MCP defense-in-depth, Linux-only, and separate from cleanup authority
- Lifecycle context: integrated ADR-0003 requires full agent termination,
  durable recovery/removal-ready evidence before native workspace archive, and
  reconciliation of Paseo-removed paths before residual Git/ref cleanup
- Local topology: Debian 13, Linux `6.12.107+deb13-amd64`, x86-64
- Local tools: Node.js `v26.7.0`; Git `2.47.3`
- Harness compatibility floor: Node.js 22 or newer
- Scope: disposable repositories, explicitly approved/contained path remotes,
  loopback fault endpoint, refs, and paths only; product path/file origin
  effects otherwise refuse
- Windows status: Inconclusive after failed run `34037393562`; bounded file-
  flush correction pending fresh review before any new run

## Result and boundary

The corrected contract demonstrates engine cleanup behavior against accidental
or buggy state changes. It does not claim authority over a hostile same-user
process. ADR-0008 and `dir-m0.14` remain the OS/credential/repository-code
containment gate. ADR-0010 means this development branch and Candidate remain
owned by one top-level Task Agent; the next exact-SHA Reviewer Agent must be a
separate top-level agent in a detached checkout.

The harness creates one `mkdtemp` root containing its source clone, bare file
remote, worktrees, replacement repositories, durable-state fixtures, disabled
hook directory, and loopback authentication endpoint state. Every targeted
cleanup uses the contract below. Test-only force pushes/replacements construct
races entirely inside that owned root. A `finally` block terminates the owned
fault/lock processes and removes the root after success or failure.

Artifacts:

- [`tools/spikes/dir-m0.6/worktree-ownership.mjs`](../../../tools/spikes/dir-m0.6/worktree-ownership.mjs)
- [`.github/workflows/dir-m0.6-windows-post-review.yml`](../../../.github/workflows/dir-m0.6-windows-post-review.yml)
- [`docs/adr/0007-git-worktree-ownership-and-cleanup.md`](../../adr/0007-git-worktree-ownership-and-cleanup.md)

Final tracked executable hashes:

```text
bb951dfd87fa4bfeee940deeb4b504bdb149d2d11ac7ed16f577e5d8ffad3b0c  tools/spikes/dir-m0.6/worktree-ownership.mjs
458822760e3cbe3f7a2121c446eedd812ee83cdc0797d0f3b2d776fccfdfc16b  .github/workflows/dir-m0.6-windows-post-review.yml
```

Local reproduction:

```text
node --check tools/spikes/dir-m0.6/worktree-ownership.mjs
node tools/spikes/dir-m0.6/worktree-ownership.mjs --state-fsync-fixture
node tools/spikes/dir-m0.6/worktree-ownership.mjs
node tools/spikes/dir-m0.6/worktree-ownership.mjs
```

## Installed Paseo lifecycle contract

The refusal fields are derived from the exact installed artifact, not a moving
documentation page:

```text
$ paseo --version
0.7.2

$ sha256sum <installed @getpaseo/protocol files>
cca82722dea170cfa64862c802784d78b12b2d6ebb0ddbfda0f0ff720ced3724  dist/paseo-config-schema.d.ts
46196b354916602f8cadb87fe4690755c2e98d5305648864841cab41309469c7  dist/paseo-config-schema.js
```

`PaseoWorktreeConfigRawSchema` contains `setup`, `teardown`, `terminals`, and
`servicePorts`; `PaseoServicePortAllocationSchema` gives `servicePorts` the
executable `portScript` alternative. Installed server code automatically runs
setup commands and terminal specifications during worktree bootstrap and uses
`portScript` when allocating a launched service script's port. The harness
commits and individually refuses each of these four surfaces before worktree
creation. Empty setup/teardown/terminals plus a numeric range are inert.

Top-level `scripts.*.command` is configured executable data but is not itself
an automatic workspace-creation surface. Director still cannot run it without
separate approval and containment. Any malformed present `worktree` value is
also refused rather than relying on Paseo's catch/default behavior.

## Durable ownership and mutation protocol

The manifest records exact Task, Run, nonce, base/Candidate, local/remote/base/
worktree-recovery/index-recovery refs, source and worktree lexical paths, native canonical paths,
filesystem device/inode identities, Git common-directory identity, exact
origin URL and file-remote identity, quarantine path, and intent location.
The intent repeats the authoritative Task/Run/nonce/ref/SHA scope.

At every reconciliation entry, the harness revalidates durable scope. One
unified gate then reruns the complete proof immediately before every worktree
move/removal, local/remote Task-ref deletion, and retained-artifact removal:

1. intent schema, operation, Task, Run, nonce, Candidate, and every durable ref;
2. exact lexical source path and non-link leaf;
3. native canonical source plus filesystem identity;
4. current Git common directory plus canonical/filesystem identity;
5. exact origin URL, non-link remote leaf, and canonical/filesystem identity;
6. Git-valid full ref names; and
7. where present, exact worktree/quarantine filesystem identity, root
   descendancy, shared common dir, origin, symbolic branch, and Candidate.

The same gate enumerates every recovery artifact bound in `removal_ready`:
the worktree recovery ref and its exact OID/parent/tree/provenance, the index
recovery ref with the same facts, and the private non-Git artifact identity,
owner, manifest, aggregate metadata, and every payload byte. While a worktree
path exists it also recomputes the real index, prospective tree, raw-byte
fidelity, material scan, and clean/dirty classification. Missing, moved,
recreated, or changed evidence raises one path-free Needs-you result.

After a successful proof, the gate issues one token bound to that
reconciliation pass and exact operation, cwd, target, expected OID, and argv.
The destructive dispatcher consumes it once; missing, stale, already-consumed,
or command-mismatched tokens cannot dispatch. A table-driven family mutates
all stored-evidence forms before move, remove, local deletion, and remote
deletion, while a separate token fixture proves an unguarded deletion request
leaves its sentinel ref intact. A valid token also rejects different argv and
cannot be reused. The family contains 51 mutation/stage cases:
16 private payload/envelope cases on the clean path, 28 Git-ref/OID plus
private-envelope cases on the dirty path, and seven retention-removal cases.
No row restores a saved intent. Evidence repair is followed by ordinary retry;
the five stage transitions are recorded and required in order.

Local and remote Task-ref deletion each persist exact ref/Candidate/nonce
intent before their effect. An absent ref after an unknown result is adopted;
any present ref is ambiguous and preserved. Once both absences are confirmed
and cleanup reaches `complete`, reconciliation is observation-only: a later
local or remote ref, even at the same Candidate, parks in Needs you and is
never deleted by the completed operation.

Deletion intent has a separate `refused_before_dispatch` result. A gate error
persists that status and a stage-specific refusal phase before rethrowing. Once
the human repairs the evidence, retry first proves the ref is still present at
the exact Candidate, restores `intent_recorded`, and reruns the complete gate.
Only then can dispatch occur. A genuine post-dispatch `intent_recorded` result
keeps the stricter present-ref ambiguity and is never auto-cleared.

Git `worktree --porcelain -z` paths and Node paths share one comparison
function: resolve on the host, apply native realpath while present (expanding
Windows 8.3 aliases), normalize separators, and fold case on Windows. Before
removal, exactly one registration must positively match the owned canonical
path, branch, and HEAD. After removal, neither the original canonical key nor
the pre-recorded quarantine key may remain. A separator/8.3 mismatch therefore
fails the positive assertion instead of vacuously passing negative checks.
After deletion, any non-existent registered path that still reports the Task
branch is also treated as stale/ambiguous and refused, even if its former 8.3
alias can no longer be expanded.

These checks run before recovery-ref creation, local Task-ref deletion, remote
Task-ref deletion, every retry, and postcondition observations. Replacement
fixtures prove that after a clean worktree is already removed, a new source
checkout, new `.git` common directory, or new bare origin at the same lexical
path can contain a matching ref and is still refused before that ref changes.

## Status-independent prospective tree

`git status` is never an input to cleanup classification. The engine creates a
fresh temporary index from the exact Candidate, disables sparse-index/checkout
behavior for the operation, adds the actual worktree, writes the prospective
tree, and compares it with the Candidate tree. Independently, `write-tree`
serializes the real index before classification. If that index differs from the
Candidate, an exact provenance-bearing index-tree commit and separate hidden
recovery ref become durable before removal. This preserves staged-only content
that a Candidate-seeded temporary index deliberately cannot see.

Two real fixtures set assume-unchanged and skip-worktree respectively, edit the
tracked file, prove ordinary status is empty, interrupt immediately after the
recovery ref is durable, and verify exact edited bytes/tree plus original
worktree/registration/local and remote refs before cleanup continues. A third
skip-worktree fixture removes the flagged file; missing sparse material returns
path-free Needs you with its worktree and refs intact, then cleans after the
fixture restores the file and clears the flag. Core sparse settings and sparse
index are explicitly disabled for prospective-tree commands.

A staged-only fixture stages one byte sequence and then restores the worktree
file to Candidate bytes. It proves the temporary prospective tree is clean but
the real index tree is not, interrupts after the index recovery ref effect, and
verifies the staged blob/tree, original index/worktree, registration, and both
Task refs before retry. Cleanup proceeds only after that durable index recovery
ref is adopted; detached restoration reproduces the staged bytes exactly.
Two additional committed fixtures cover an index-only added file removed from
the worktree and an index-only `git rm --cached` deletion; their detached index
recovery checkouts reproduce the added bytes and missing tracked path.

For every regular tracked/ordinary-untracked file, `hash-object --no-filters`
must match the prospective/recovery blob. A fixture adds `text eol=lf` and CRLF
worktree bytes; normalization is detected and returns path-free Needs you with
no recovery ref or removal. This avoids claiming byte-exact recovery when Git
attributes/EOL conversion would change bytes.

Plain prospective-tree-clean removal proves the Candidate is an ancestor of
the live fetched base before `removal_ready` and again immediately before move.
An unintegrated clean fixture retains its original path, positive registration,
local ref, and remote ref with no removal-ready evidence.

## Secret-safe material and Git executable controls

Before snapshot or removal, Git lists ignored untracked material separately,
while an independent no-follow filesystem walk catches Git-invisible state.
Nested `.git` directories/files and submodule markers are rejected regardless
of ignored coexistence. A tracked Git symlink is admitted only when its index
mode is `120000` and its raw target bytes match the prospective tree; the common
fixture proves that path. Untracked symlinks, junctions/reparse points, FIFOs,
sockets, and other special types stop path-free before artifact/removal effects.
Empty directories are recoverable non-Git material, including alongside a Git
snapshot of other dirty content. Linux exercises a FIFO and Unix socket;
pending Windows exercises a junction/reparse point. A dirty ignored file
coexists with both nested marker forms in the committed fixture.

Dirty work combined with Git-ignored files/directories returns a path-free
Needs-you error; neither filenames nor contents enter durable state/output or a
Git recovery commit. Git-invisible empty directories use the private non-Git
artifact alongside the ordinary dirty-tree recovery ref.

For a successfully integrated prospective-tree-clean worktree, the default
instead preserves supported ignored/invisible files/directories in a non-Git
artifact before removal. The artifact lifecycle is:

1. Prove repository, worktree, live-base integration, and recovery roots. Walk
   and enforce policy incrementally from `lstat` type/size/count before reading
   content. The bounded evidence default supports 2 GiB aggregate and per file,
   10,000 recovery entries, 100,000 inspected entries, and a 64 KiB stream
   buffer plus at most seven-day retention. Task policy may tighten those
   bounds; expansion is rejected until `dir-m0.17` supplies the missing
   operational evidence and policy decision.
2. Prove each attempt leaves the configured 10% free space after content plus a
   fixed 64 KiB and 4 KiB-per-entry reserve. Recheck this on every retry. A
   sparse file one byte beyond the per-file policy returns path-free Needs you
   before hashing/reading; the ordinary fixture also verifies a multi-entry
   ignored tree under the bounded policy envelope. A verified artifact is
   charged zero content bytes on retry, while an absent/partial artifact is
   charged the full remaining reservation; the 10% floor is always rechecked.
3. Stream hashing/copying/restoration with the bounded buffer; read failures,
   short/change-during-copy results, and oversize states map to path-free Needs
   you rather than raw `RangeError`/filesystem output. Durable state contains
   aggregate metadata/content digests and counts, never entry names or hashes.
4. Create a same-filesystem staging artifact beneath an exact engine-owned
   recovery root. POSIX directories/files use `0700`/`0600`; Windows applies
   an inheritance-protected ACL granting only the current SID. A private owner
   record and manifest bind Task, Run, nonce, Candidate, entries, metadata,
   hashes, and seven-day retention.
5. Atomically rename staging to its final random-scope path, verify every byte,
   manifest hash, permission, owner and filesystem identity, then bind that
   hash into `removal_ready`. No ignored content is added to Git.
6. At every destructive gate, re-enumerate source metadata/content while a
   worktree exists and always stream every artifact payload file against its
   bound private-manifest hash. The same proof repeats before move, remove,
   local/remote ref deletion, and retention removal. Changed evidence parks
   before dispatch; path-free aggregate digests remain bound in
   `removal_ready`.
7. After worktree removal, restore into a disposable destination and verify
   exact bytes, mtimes, POSIX modes, and empty directories. Before expiry the
   result is ordinary scheduled `retained` state, not Needs you; at expiry an
   owner/identity-verified removal-ready intent precedes deletion. A crash after
   deletion reconciles absence and repeated cleanup remains idempotent. A
   future Git recovery-ref expiry must wait until this artifact reaches verified
   `removed`; the artifact gate still requires those refs and safely resumes
   after a missing ref is repaired.

The committed clean fixture has an ignored file and directory. It interrupts
after the artifact rename but before result persistence, adopts the verified
artifact on retry, replaces the artifact path with an unknown sentinel and
proves refusal/preservation, removes the integrated worktree, restores exact
data/metadata, enforces scheduled retention and initial/retry disk-pressure
behavior, and exercises
effect-before-result expiry cleanup. If any preservation proof fails, the
worktree remains and Needs you is actionable; the ordinary successful path
therefore preserves PLAN §§9.2, 15.5, and 25 without losing or Git-persisting a
possible secret. Foreign owner/path/permission/content or recovery squatting
maps to one path-free Needs-you domain error with unknown data intact.

The policy validator accepts the exact 2 GiB aggregate/per-file ceilings,
seven-day retention, and 10% free-space floor; it rejects either byte cap plus
one, seven days plus one millisecond, and a 9% floor.
It also retains the existing non-expansion checks for recovery entries,
inspected entries, and stream-buffer size. Broader policy remains exclusively
owned by `dir-m0.17`.

The superseded 250,000-entry default was not evidence-backed: the independent
review measured roughly 17.77 seconds for 5,006 entries, 93.24 seconds for
20,021 entries, and about 245 seconds for 30,031 entries on its Linux probe.
The contract shares one buffer across each pass and revalidates artifact
payload bytes at all five destructive gates, plus source bytes while a
worktree exists; it does not
turn the earlier measurements into a product policy claim. Discovered sibling
`dir-m0.17` owns representative Linux/real-
Windows benchmarking and the release policy for large `node_modules`, `target`,
and `.venv` trees; as an open P0 child of `dir-m0`, it keeps that broader M0
cleanup gate blocked without expanding `dir-m0.6`.

For ordinary tracked/untracked worktree recovery, a temporary index reads the
Candidate, adds all material, writes a tree, creates a provenance-bearing
commit, and creates the hidden ref with expected-absent `update-ref`; the real
index tree uses its separate ref when needed. Before
`git add`, `git check-attr -z --stdin filter` inspects every present tracked or
ordinary untracked path. A declared driver with no configured `clean` or
`process` command is inert and allowed; an executable clean/process driver
produces Needs you. Every local-source Git invocation overrides
`core.hooksPath` with an empty owned directory, disables
`core.fsmonitor`, clears the credential helper, disables prompts, and uses
direct argv with `shell: false`. A real repository-configured
`post-index-change` hook, `core.fsmonitor=true`, and clean-filter command are
verified in local config; the local hook sentinel never appears. This does not
disable hooks in another repository used as a path/file origin. The contract
therefore refuses origin observation/fetch/push unless a separate human
approval and `dir-m0.14` containment fact are both recorded. A committed bare-
origin receive-hook fixture proves refusal occurs before that hook or any
cleanup effect; the disposable positive path records both test-only facts.

This protects deterministic engine behavior. A same-user process can still
race between inspection and mutation; ADR-0008/`dir-m0.14` must contain it.

## Live base, remote status, and exact deletion

`git ls-remote --exit-code` is classified exactly:

- status 0: exactly one full-SHA/exact-ref observation;
- status 2: confirmed absence; and
- every other status: unavailable/ambiguous, never absence.

For a bare-path or normalized `file://` origin, even observation or fetch can start another local
repository process and push can run its receive hooks. These operations refuse
unless both exact human approval and `dir-m0.14` containment are durable facts.
The harness sets both only for its disposable root; a negative fixture installs
a bare-origin receive hook, omits both facts, and proves no hook, worktree, or
ref effect occurs.

A real loopback HTTP endpoint returns an authentication challenge, then is
terminated to produce an offline transport failure. Both non-2 results stop.
The owned process is absent at cleanup. Removing the owned file origin itself
produces the same explicit `ExternalUnavailableError` classification rather
than a raw filesystem exception or false ref absence; local and remote Task
refs remain.

Before local and remote Task-branch deletion—and again just before mutation—the
engine reads the live `refs/heads/main`, fetches that exact advertised object
with `--no-write-fetch-head`, confirms it is a commit, and proves the Candidate
is its ancestor. A live base rewind preserves both Task refs. Local deletion
persists intent, uses `update-ref -d <ref> <expected>`, and confirms explicit
local absence. A crash after the effect adopts only absence; a present same-SHA
ref after intent is ambiguous and survives.
Immediately before it, every normalized Git worktree registration is
inspected. If another path reports the Task branch, cleanup returns Needs you.
The real fixture verifies that the unknown consumer's ref, symbolic branch,
Candidate HEAD, tracked file, and untracked sentinel remain intact.
Remote deletion repeats exact-head observation, uses
`--force-with-lease=<ref>:<expected>`, and accepts only a status-2 postcondition.
One raced head is refused by the just-in-time comparison. A second race is
injected after that comparison but immediately before push, so the actual
explicit lease rejects deletion and the raced ref survives. An injected crash
after successful remote deletion but before persistence is reconciled by fresh
identity/base checks and confirmed status-2 absence; no second deletion is
attempted.

The deletion intent is durable before push. If an interrupted delete is later
observed with the Task ref present—even at the same Candidate SHA—the engine
cannot distinguish a failed prior effect from an external same-SHA recreation.
It returns path-free Needs you and preserves the ref. The fixture recreates the
same SHA and proves refusal; forge-aware resolution remains explicitly owned by
`dir-m0.7`.

After local and remote absence are both durably confirmed, `complete` is a
terminal destructive state. The fixture recreates both refs at the Candidate,
reconciles, and proves both survive the path-free Needs-you result. Test-only
cleanup then removes those fixture refs with exact expected-head operations.

## Worktree removal and interruption

Clean and snapshotted paths persist a `removal_ready` phase before mutation. It
binds Candidate, original filesystem identity, prospective worktree tree, real
index tree, path-free filesystem-metadata digest, clean/dirty classification,
and both recovery SHAs. No artifact-specific resume branch can authorize a
destructive command. The unified gate freshly proves the complete worktree,
Git-recovery, index-recovery, private-artifact, ownership, and expected-OID set
and issues the exact one-use command token. Git
first moves the linked worktree to the pre-recorded
quarantine path, then removes that exact registered worktree. Missing paths are
accepted on retry only from removal-ready-or-later state and only after both
original/quarantine paths and Git worktree registrations are absent.

State writes use a mode-`0600` next file, file `fsync`, atomic rename, and a
POSIX state-directory `fsync`. The failed Windows run proved that the file
descriptor—not the already-skipped directory branch—was opened without the
write access required by `FlushFileBuffers`. The corrected helper uses
non-truncating `r+` only on Windows and retains `r` plus the later directory
flush on Linux. Injected operations assert both access branches, flush-before-
close ordering, descriptor closure after error, and unmodified error
propagation. A partial next file leaves the previous intent readable. An
explicitly truncated primary intent returns path-free Needs you before any
effect; the fixture restores it through the same atomic writer and proves
worktree/ref survival.

Fault injection covers:

- staged-index recovery ref created before its result is persisted: the staged
  bytes/tree and original worktree/refs remain recoverable before retry;
- recovery ref created before its result is persisted: retry recomputes the
  tree and verifies ref SHA, parent, Task, Run, and nonce before adoption;
- a new post-snapshot file: recomputed tree mismatch stops while it remains;
- a same-size clean tracked-file edit with its mtime restored: the unified gate
  refuses and preserves path/refs/data;
- clean worktree removed before result persistence: removal-ready evidence and
  absent registrations reconcile before ref cleanup;
- quarantine moved before result persistence: retry finds the one positive
  quarantine registration; a late file produces an exact recovery-tree
  mismatch and remains intact before cleanup continues;
- local ref deleted before result persistence: durable intent plus exact
  absence reconciles without another delete;
- remote ref deleted before result persistence: status-2 absence reconciles;
- Git administrative worktree lock: one force is refused and double-force is
  never attempted; and
- clean held-handle removal: a clean integrated worktree persists and verifies
  `removal_ready`, recomputes both trees on resume, and acquires the handle only
  at the immediate move boundary; Windows must observe exact
  move failure with all facts retained, while Linux proves move/removal and
  open-descriptor reads, followed by effect-before-result retry;
- one table-driven family deleting/moving both Git recovery refs and changing
  private artifact payload/manifest/owner/path identity before worktree move,
  worktree remove, local ref deletion, and remote ref deletion: every case
  returns path-free Needs you before dispatch and preserves every not-yet-
  completed path/ref;
- repair followed by ordinary retry at move, remove, local-ref deletion,
  remote-ref deletion, and retention removal without rewriting saved intent;
- retention refusal when a required Git recovery ref is absent, successful
  artifact removal after repair, and refusal of recovery-ref expiry until the
  private artifact is removed; and
- completed cleanup invoked repeatedly: confirmed absence is observed with no
  duplicate external effect, while any later same-SHA local/remote recreation
  is preserved and parked.

The hidden recovery ref is checked out detached after the original dirty
worktree and Task branch are gone. Modified tracked, staged, top-level
untracked, and nested untracked content is reproduced byte-for-byte.

## Corrected Linux result

Repeated executions return `result: pass` with 90 real assertions: 87 common
facts plus three Linux-specific facts. The report emits those counts
and names separately so no platform receives a no-op assertion.

Common assertions:

```text
all_installed_lifecycle_surfaces_refused
state_file_fsync_platform_access_and_failure
local_origin_effects_require_approval_and_containment
file_url_origin_refused_consistently
destructive_commands_require_fresh_gate_token
task_branch_and_worktree_owned
worktree_registration_identity_verified
committed_symlink_and_inactive_filter_supported
atomic_intent_write_and_corruption_fail_closed
detached_review_exact_candidate
source_and_common_dir_refused
unknown_worktree_refused
lexical_alias_refused
canonical_alias_refused
path_swap_refused
quarantine_squatting_needs_you
administrative_lock_refused
foreign_common_dir_refused
unintegrated_refs_refused
unintegrated_clean_worktree_preserved
remote_compare_delete_race_refused
assume_unchanged_edit_recovered
skip_worktree_edit_recovered
staged_index_only_edit_recovered
staged_index_only_addition_recovered
staged_index_only_deletion_recovered
sparse_missing_material_needs_you_then_recovers
eol_normalization_needs_you_without_false_recovery
ls_remote_statuses_distinguished
offline_and_auth_errors_refused
unproven_recovery_policy_expansion_blocked
ignored_recovery_max_bytes_boundary_enforced
ignored_recovery_max_file_bytes_boundary_enforced
ignored_recovery_minimum_free_percent_boundary_enforced
ignored_recovery_retention_boundary_enforced
ignored_recovery_disk_pressure_refused
oversized_sparse_ignored_needs_you_before_read
foreign_owner_recovery_squat_needs_you
ignored_recovery_artifact_effect_crash_reconciled
ignored_recovery_replacement_refused
ignored_content_change_path_free_needs_you
ignored_recovery_retry_charges_only_remaining_bytes
destructive_gate_token_binds_exact_command_once
stored_evidence_gate_artifact_before_move_table
stored_evidence_gate_artifact_before_remove_table
clean_removal_ready_crash_reconciled
stored_evidence_gate_artifact_before_local_delete_table
clean_ignored_material_preserved_removed_and_restored
active_task_ref_consumer_refused
source_replacement_after_removal_refused
common_dir_replacement_after_removal_refused
origin_unavailable_classified
origin_replacement_after_removal_refused
live_base_rewrite_refused
local_delete_effect_retry_reconciled
stored_evidence_gate_artifact_before_remote_delete_table
force_with_lease_race_refused
remote_same_sha_recreation_needs_you
remote_delete_effect_retry_reconciled
completed_ref_recreation_needs_you
ignored_recovery_retention_cleanup_idempotent
clean_lock_removal_ready_persisted
removal_ready_same_metadata_edit_refused
clean_lock_retry_completed
ignored_material_needs_you
nested_repository_and_ignored_coexistence_needs_you
snapshot_filter_refused_and_hooks_disabled
recovery_ref_effect_crash_reconciled
post_snapshot_change_refused
dirty_snapshot_verified
dirty_empty_directory_preserved
stored_evidence_gate_before_move_table
quarantine_move_effect_crash_reconciled
stored_evidence_gate_before_remove_table
worktree_removal_effect_crash_reconciled
stored_evidence_gate_before_local_delete_table
stored_evidence_gate_before_remote_delete_table
owned_worktree_removed
exact_local_ref_removed
exact_remote_ref_removed
stored_evidence_gate_before_retention_remove_table
recovery_ref_expiry_ordered_after_artifact
stored_evidence_repair_then_resume_all_stages
source_and_unknown_resources_preserved
snapshot_restored_after_removal
cleanup_idempotent
temporary_root_removed
```

Linux-specific assertions:

```text
linux_clean_open_handle_removal
linux_symlink_fifo_and_socket_material_needs_you
linux_dirty_open_handle_recovery
```

The future Windows report must replace only those three with real, non-vacuous:

```text
windows_clean_removal_lock_fail_closed
windows_reparse_material_needs_you
windows_dirty_snapshot_lock_fail_closed
```

Each successful execution reports the exact OS, architecture, Node, Git, Paseo
schema version/hashes/surfaces, and `temporary_root_removed`. The final external
check also finds zero `director-m0.6-*` roots.

## Windows post-review gate

Successful Windows behavior is not claimed by this correction. Historical PR
[#11](https://github.com/mcuadros/paseo-director/pull/11) remains at approved
Candidate `d0530622bafcbdd342bc2fa4d572ab2fa964653d`. Its one opened-event run,
[`34037393562`](https://github.com/mcuadros/paseo-director/actions/runs/34037393562),
failed on `ImageOS=win25-vs2026`, `ImageVersion=20260824.214.3`, Windows
`10.0.26100.0` X64, Node.js `v22.23.2`, and Git `2.55.0.windows.5`. Exact
Candidate/base checkout and ancestry passed; the first harness stopped in
`writeState` with `EPERM: operation not permitted, fsync` on the next-file
descriptor opened `r`, the second run never started, and
`temporary_root_removed` was not emitted. The run was not retried and remains
failure evidence rather than a Windows pass.

Microsoft documents that `FlushFileBuffers` requires a handle with
`GENERIC_WRITE` access. The bounded correction opens the already-written next
file as non-truncating `r+` on Windows, continues to propagate any flush error,
and leaves the Linux file and directory durability sequence unchanged. A new
Windows run is forbidden until the correction Candidate receives fresh exact-
SHA approval.

Repository policy permits the committed job only after fresh exact-SHA
approval and PR publication. It is scoped to:

- event: `pull_request` opened/reopened, never synchronize;
- target: `main` only;
- paths: this workflow, ADR-0007, `docs/evidence/m0.6/**`, and the harness;
- runner/budget: fixed `windows-2025`, one x86-64 VM, 10 minutes, no matrix;
- authority: `contents: read`, no secrets, checkout credentials disabled;
- source/base: full event head/base SHAs with exact checkout and ancestry;
- facts: `ImageOS`, `ImageVersion`, runner OS/architecture, OS build, Node.js
  22-or-newer, and Git versions; and
- execution: the identical complete harness twice, with `$LASTEXITCODE`
  captured and required to be zero after each invocation independently.

The checkout step is current official `actions/checkout@v7.0.1`, pinned to
immutable commit `3d3c42e5aac5ba805825da76410c181273ba90b1`. On 2026-09-06 the
official release page identified that commit as v7.0.1, `git ls-remote`
returned the same tag SHA, and the exact-commit `action.yml` declared
`runs.using: node24`.
The downloaded immutable `action.yml` SHA-256 was
`d59219cb79590abdb877deaa14e3b65a00c05318bf5a6f3b989b9162b5d08c35`.
`persist-credentials: false` and repository `contents: read` remain unchanged.

The ACL setter/verifier carry the recovery path only through the dedicated
child environment field `DIRECTOR_RECOVERY_ACL_PATH`; the `-Command` string
contains no user path and has no trailing pseudo-argument. It rejects a missing
field, sets inheritance protection with only the current SID, and verifies one
allow/full-control rule with container/object inheritance. No `pwsh` or
`powershell.exe` is installed on the Linux host, so local empirical execution
is unavailable and the Windows job remains authoritative. That job also checks
`$LASTEXITCODE` after `rev-parse`, `cat-file`, and `merge-base` individually,
not only after the final native Git command.

On Windows, the alias test creates a real junction and the same run exercises
two separate exclusive-handle boundaries:

1. Dirty/snapshot: PowerShell holds the dirty tracked file with
   `FileShare.None`. The real reconciler must produce exact error
   `GitCommandError: git add exited 128`; intent remains byte-identical at `snapshot_verified`,
   the original positive registration remains, quarantine is absent, metadata
   and unaffected bytes match, and every recovery-ref byte verifies. After
   release, original tracked bytes match and quarantine/removal retries finish.
2. Clean/removal: a separate integrated clean worktree first interrupts after
   persisting and positively verifying `removal_ready`. On resume the reconciler
   recomputes the real index and prospective tree; immediately before the actual
   move, the bounded fault seam makes PowerShell take `FileShare.None`. The real
   reconciler must produce exact error
   `GitCommandError: git worktree move exited 128`, keep the intent byte-
   identical, retain original path/registration/local and remote refs plus
   committed data, and leave quarantine absent. After release, retry performs
   the actual move/removal, reconciles an effect-before-result interruption,
   deletes exact refs, and completes idempotently.

A different error, status, message, phase, path, registration, byte, or ref
fact fails the job. This Candidate records only the required observations; it
does not claim Windows has produced them. Each harness run must remove its
temporary root, and both independently checked exits must be zero.

A changed PR head cannot generate evidence; it requires fresh review and a
close/reopen. After Windows passes, the immutable run URL, event head/base,
runner image, tools, output, and cleanup result must be recorded in a changed
Candidate that receives another fresh review.

## Platform constraints

- Linux evidence is limited to the exact local tuple and filesystem above.
- Windows evidence will be limited to the recorded `windows-2025` image/tool
  tuple; no other tuple is inferred compatible.
- Canonical comparison normalizes separators, drive-letter case, and realpath/
  8.3 aliases while filesystem identity prevents target substitution; the
  Windows job exercises a junction. UNC/network filesystems, other reparse-point classes,
  case-sensitive Windows directories, unstable file IDs, and submodule
  worktrees remain fail-closed and unadmitted.
- POSIX coverage is named explicitly and proves both clean move/removal and
  dirty snapshot/recovery while descriptors remain readable. Windows coverage
  uses two distinct names and must prove both snapshot-open and clean-move
  sharing violations through the real reconciler.
- The recovery ref remains until its separately guarded retention cleanup.
- No result here weakens ADR-0008, expands ADR-0005, or satisfies
  `dir-m0.7`, `dir-m0.10`, or `dir-m0.14`.

## Primary contracts

Consulted on 2026-09-06:

- <https://git-scm.com/docs/git-worktree>
- <https://git-scm.com/docs/git-update-ref>
- <https://git-scm.com/docs/git-show-ref>
- <https://git-scm.com/docs/git-ls-remote>
- <https://git-scm.com/docs/git-fetch>
- <https://git-scm.com/docs/git-push>
- <https://git-scm.com/docs/gitattributes>
- <https://git-scm.com/docs/githooks>
- <https://nodejs.org/download/release/v22.22.0/docs/api/fs.html>
- <https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-flushfilebuffers>
- <https://learn.microsoft.com/en-us/dotnet/api/system.io.fileshare?view=net-9.0>
- <https://learn.microsoft.com/en-us/dotnet/api/system.security.accesscontrol.objectsecurity.setaccessruleprotection?view=net-10.0>
- <https://learn.microsoft.com/en-us/dotnet/api/system.security.accesscontrol.filesystemaccessrule?view=net-10.0>
- <https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_powershell_exe?view=powershell-5.1>
- <https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_preference_variables?view=powershell-7.5>
- <https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows>
- <https://docs.github.com/en/actions/reference/runners/github-hosted-runners>
- <https://github.com/actions/checkout/releases/tag/v7.0.1>
- <https://raw.githubusercontent.com/actions/checkout/3d3c42e5aac5ba805825da76410c181273ba90b1/action.yml>
