# Mandatory independent Review

Director Engine owns Review admission, effect ordering, verdict evidence, and
Reviewer-resource cleanup. A Review is keyed by the exact Task, Run,
Candidate, base, tree, Candidate manifest, acceptance, configuration, profile,
context, decisions, findings, Candidate generation, CI slot, Task Agent,
coordinator, and checkout owner. A change to any binding creates a different
Review key; a changed authoritative CI observation invalidates the current
Review instead of silently replacing it.

## Reviewer topology and authority

One admitted Candidate has one normal top-level Reviewer for its current
Review key. The engine:

1. creates an owner-marked disposable Git worktree detached at the exact
   Candidate and proves the primary checkout is clean and unchanged;
2. registers a host view over that checkout;
3. freezes the root-workspace Worker labels and the exact supported Reviewer
   profile;
4. creates the Reviewer with `parentAgentId` omitted and only the fixed
   zero-work bootstrap;
5. persists the completed bootstrap identity and label digest; and
6. sends the separate Review prompt with terminal notification enabled only
   after the one authoritative remote CI observation is attached.

The Reviewer profile is read-only and its MCP catalog is exactly
`director_candidate_read` plus `director_review_verdict_submit`. Source,
primary checkout, branch, CI, PR, integration, cleanup, and Task-closure
mutations have no Reviewer capability. A same provider/model tuple is admitted
with `same_reviewer_model`; the optional inherited
`requireDifferentReviewerModel` policy refuses it before provider dispatch.
The engine uses only the already preflighted frozen profile and never searches
for or silently selects a fallback.

## Harness and verdict

The frozen matrix contains every Task acceptance-criterion ID and the eight
mandatory dimensions: acceptance, correctness, security, maintainability,
readability, design, quality, and rigor. The independent probe plan contains at
most 16 direct-argv, five-minute probes. Git probes use read-only Git verbs,
source probes use `rg`, installers and complete CI are refused, and the
remote-only harness never executes focused tests.

The harness consumes the single authoritative remote Linux CI observation; it
does not start another complete CI. Missing local dependencies are an
environment observation, never a Candidate defect. Repeated preparation
fingerprints or attempts produce `setup_thrash` and no verdict evidence.

One bounded claim returns `approve_candidate`, `changes_requested`, or
`needs_human_decision`, P0-P3 findings with relative citations, complete matrix
and probe results, and bounded residual-risk codes. Runtime Reviewer, harness,
and durable-evidence UUIDs must be exactly equal. Approval cannot contain P0 or
P1 findings; P2 approval requires an exact prior human acceptance, while
`needs_human_decision` requires an unaccepted P2 finding.

## Recovery and cleanup

Checkout, host view, bootstrap, prompt, Reviewer archive, workspace archive,
and checkout removal each persist intent before dispatch. Recovery observes
the exact external identity before progressing. A bootstrap or Review turn
that is still active waits; a possibly handed-off prompt is never resent.
Duplicate, changed, parented, self-review, dirty, attached, moving, or
otherwise ambiguous facts invalidate or park the Review and preserve every
resource.

Only the coordinator identity frozen into the Review can request cleanup, and
only after verdict evidence is durable. Cleanup archives the Reviewer, archives
the host view, then removes the exact owner-marked detached checkout. An
ambiguous identity or owner marker stops cleanup permanently for that attempt;
unknown resources are never adopted or removed.

The setup-thrash regression fixtures contain only the bounded classifications
learned from the `dir-m0.4` and `dir-m0.6` review churn. They contain no raw
conversation history, credentials, private paths, filenames, or secret-shaped
values.
