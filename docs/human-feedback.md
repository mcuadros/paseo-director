# Paseo and GitHub human feedback

Director Engine owns feedback ingestion and routing. Direct Paseo input enters
as a server-authenticated typed command. GitHub is a read-only observation
source covering pull-request reviews, review comments, and issue comments. The
GitHub port intentionally exposes no reply, approval, dismissal, or thread
resolution operation, so an agent cannot converse through GitHub or act as a
human.

## Identity and context

Every source revision records a server-attested actor kind (`human`, `bot`, or
`app`), stable actor and source identities, exact revision identity, timestamps,
and an exact Candidate/base context or an explicit ambiguous/stale result. A
GitHub login, author association, comment wording, or approval text never grants
Director authority. Only GitHub account type `User` is human feedback; bot and
GitHub App output remains immutable ignored evidence. A P2 finding, an unbound
issue comment, or manual correction policy requires a separate authenticated
Paseo human decision.

GitHub scans read all three streams in fixed order with at most ten pages of one
hundred records per stream. Incomplete pagination, changed repository/PR/head/
base binding, stale observation, duplicate identity with different content, or
an unavailable response fails closed before routing. Reviews and review
comments use their own commit identity. An issue comment without an exact
Candidate relationship is not silently rebound to the current head.

## Immutable revisions and routing

Feedback state is embedded in the active Run and persisted by Run compare-and-
swap. Each edit appends a new immutable revision. An exact duplicate is a
replay, late older revisions cannot replace newer current state, and a deletion
becomes a tombstone only after a complete source scan. Audit records contain a
bounded redacted summary and content-addressed evidence; raw bodies, local
paths, secrets, credentials, transcripts, and thread contents are not stored.

Current actionable feedback clears current Ready authority and is converted to
the human-feedback snapshot in the complete M4.4 batch. Review, Validation, and
CI snapshots remain present even when empty. The correction service therefore
keeps the original top-level Task Agent, exact three-attempt lineage, runtime
budgets, nonrepeatable prompt recovery, unchanged-SHA rejection, and fresh-
Candidate CI/Review gates. Feedback ingestion itself sends no prompt and has no
publication, integration, cleanup, or Task-closure authority.

Before that feedback CAS commits, the engine refuses to proceed while a PR or
direct-delivery dispatch has a possible handoff awaiting observation. When no
dispatch is in flight, the same CAS clears Ready, PR-publication, and direct-
integration authority, moves the applicable PR/direct state into immutable
history, and records the feedback state. Publication and direct-delivery
services independently reject unresolved feedback, closing the race between
feedback persistence and correction-batch persistence. A later corrected
Candidate may carry the same owned PR through its existing history contract;
direct delivery still requires fresh Candidate, Review, and CI bindings.

Restart and response loss adopt the immutable source revision, deterministic
correction batch, or deterministic follow-up Task. Project lease, Run version,
and TaskStore compare-and-swap checks fence concurrent coordinators; one winner
persists and losing stale writers cannot route or create duplicates.

## Feedback after Done

Authenticated actionable feedback observed after the Task is Done (or after
integration authority already exists) creates one deterministic sibling Task.
It retains the original Epic and Workspace, has no dependency on the completed
Task, and carries `director.discovered-from` plus a feedback-audit reference.
The completed Task and Run are not updated. Replaying the same feedback adopts
the same follow-up Task, including after a lost create response.

Board and Organizer projections expose only feedback phase, actionable and
audit counts, current revision, correction-batch identity, and exact Needs-you
reason. They never expose bodies or provide a GitHub response/resolution action.

Manual direct integration uses a separate versioned authorization requiring an
authenticated Paseo direct-integration action. Neither a GitHub human identity
nor an ordinary Paseo feedback record can satisfy that authorization schema.
