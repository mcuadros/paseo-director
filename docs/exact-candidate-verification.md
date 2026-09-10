# Exact-SHA Candidate verification

Candidate admission is an engine-owned, fail-closed transition. A Worker
`completed` outcome is only an author claim. The Director Engine enriches it
from current TaskStore facts, records a bounded Git observation, and then uses
the Run version and Project lease epoch to admit one immutable Candidate.

## Immutable binding

`director.candidate-claim/v1` binds the full Git object ID to the exact
Project, Workspace, Task, Run, Worker, worktree identity, Task branch, observed
base ref and SHA, Task and Run versions, lease epoch, graph policy, and frozen
acceptance, configuration, profile, preparation-context, decision, and finding
hashes. SHA-1 and SHA-256 object formats are supported; abbreviated, uppercase,
mixed-format, absent, or ambiguous object IDs are refused.

The admitted `director.candidate-manifest/v1` additionally freezes the commit's
parent, tree, raw diff digest, changed-path-set digest, repository binding,
claim, observation, and graph-policy digests. Candidate sequence numbers are
strictly monotonic within one Run. The direct-Dolt adapter stores the complete
claim and manifest in an append-only TaskStore schema-version-2 Candidate row and advances the current
Candidate projection with one expected-version transaction.

## Git observation

The dedicated read-only Git adapter executes direct argv without shell
interpolation and suppresses repository hooks, filesystem monitors, ambient
Git configuration, and Git environment overrides. It verifies:

- canonical source, worktree, and Git common-directory identities;
- the canonical credential-free `origin` identity and exact registered linked
  worktree, symbolic branch, branch SHA, base ref, and base SHA;
- commit type, full object-format length, reachability from the owned branch,
  ancestry, and the default one-direct-parent graph;
- absence of alternate object stores;
- a clean index and worktree, including tracked changes, ordinary untracked
  and ignored/generated content, conflicts, intent-to-add, assume-unchanged or
  skip-worktree/sparse state, dirty or uninitialized submodules, empty
  untracked directories, untracked symlinks, and unsupported filesystem nodes;
  and
- identical snapshots before and after the complete inspection so ref, index,
  registration, filesystem-identity, or worktree movement is refused as a
  TOCTOU ambiguity.

Only bounded closed diagnostic codes cross the port. Paths, names, command
output, file content, remotes containing credentials, and filesystem details
do not enter errors, TaskStore events, logs, or support-facing observations.

## Invalidation contract

Each admission creates a new `director.candidate-authority/v1` generation with
empty downstream authority. Validation, Review, authoritative CI, draft
publication, feedback, Ready, and integration records may later be attached
only with the exact current Candidate manifest binding. A changed Candidate,
base, branch, Task version, configuration, decisions, findings, tree, diff, or
changed-path set increments the generation, marks it invalidated, and clears
all seven downstream authority slots. Historical Candidate and evidence rows
remain immutable.

This contract does not implement Reviewer lifecycle, PR/integration effects,
or cleanup. Those later M4 components must consume the exact binding and may
never restore authority by rebinding an older observation.
