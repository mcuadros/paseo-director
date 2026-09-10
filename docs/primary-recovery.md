# Primary replacement and orphan recovery

Primary recovery is a Director Engine state machine over durable Run facts and
fresh host/runtime observations. Plugin reload, daemon interruption, terminal
callback loss, coordinator restart, and Project lease takeover are wake causes;
none proves that an agent failed or authorizes another prompt.

## Reconcile before effects

One recovery pass binds all of these facts before any mutation:

- Task, Run version, Candidate, frozen profile/configuration and explicit
  fallback decision;
- Project lease holder/process/epoch and, after takeover, the consumed proof
  that the prior engine and all dispatch children are absent;
- canonical repository, source/common-directory, base, branch, worktree, and
  ownership binding;
- native agent/workspace IDs, parentage, title, placement, labels, provider,
  model, persistence, bootstrap, real-prompt, terminal, archive, and timeline
  observations;
- every Run-correlated Task Agent, Reviewer, helper, workspace, and related
  worktree;
- current lifecycle admission, rootless-OCI enforcement, operational telemetry,
  control state, helper state, and runtime-budget ledger.

Host and runtime reads are separate, bounded, self-hashed observations. The
Paseo connector exposes only normalized status, exact public identities and
closed failure signals. It never returns raw provider errors and never chooses
adoption, archive, fallback, replacement, retry, or cleanup.

## Failure classification

The Go engine owns the final class and replacement decision. The connector's
closed redacted signals map to engine classes as follows:

| Normalized signal | Engine class | Default route |
|---|---|---|
| no terminal failure | none | adopt the exact resumable persistent session |
| temporary service failure | transient | wait and re-observe; do not replace |
| provider session/process terminal | terminal provider | one replacement may be authorized |
| provider policy rejection | terminal policy | poison the old session; one replacement may be authorized |
| authentication rejection | terminal authentication | Needs you |
| configuration rejection | terminal configuration | Needs you |
| unknown or contradictory signals | ambiguous | Needs you |

The preserved lifecycle evidence established the operational lower bound of
two repeated same-session policy failures for a poisoned persistent session.
The PLAN and accepted ADRs remain policy authority: one terminal failure also
prevents another automatic send to that same session, and the Run has exactly
one replacement allowance. The journal contributes only the bounded classifier
fixture; no raw message, credential, environment, or conversation is retained.

## Replacement authority and ordering

Replacement authority is consumed durably before creation and binds the exact
Run version, old agent/workspace, failure observation, Project lease epoch,
repository binding, frozen profile, and selected fallback-chain result. It
cannot be reset by restart. The sequence is:

```text
complete host/runtime inventory
→ archive only the exact failed original Task Agent
→ prove closed + archivedAt + external process absence
→ consume the one replacement authority and budget count
→ register replacement visibility in Director Workers
→ create one parentless agent with zero-work bootstrap
→ persist exact native identity and labels
→ persist a separate real-prompt intent
→ send once with notifyOnFinish=true
→ verify prompt acceptance and promote the replacement primary
```

The replacement reuses the active Execution Workspace and Director-owned
worktree; it does not create another workspace, branch, or worktree. Its prompt
contains the frozen Task context plus durable Task/Run/Candidate bindings, not
the poisoned conversation history. A possible handoff of that prompt is
nonrepeatable.

## Ambiguity and composition

An extra, missing, parented, cross-Run, wrong-workspace, wrong-repository,
wrong-branch/base, or otherwise contradictory resource is preserved and routes
to one exact `primary_recovery_*` Needs-you reason. Recovery never deletes an
orphan. Existing correct Reviewers and helpers are adopted as observations and
are never archived by primary replacement. A live helper prevents replacement
because archiving its parent could cascade; the resource remains intact for a
human decision.

Pause and reconcile-first Resume block replacement dispatch. Cancel and
Emergency stop take over through their existing ordered containment/recovery
sagas. Soft/hard budgets, outstanding reservations, provider-usage ambiguity,
lease loss, missing rootless-OCI enforcement, and unavailable/exceeded
operational telemetry all take precedence. A second replacement failure,
consumed authority, or unprovable termination routes to Needs you with
`cleanupAuthorized=false`.

All mutations retain intent/dispatch/observe frontiers. TaskStore CAS chooses
one winner among competing coordinators; a same-version observation conflict
is re-read as a CAS loss after the winning version becomes visible. Direct
Dolt reopen, connector replacement, and lost responses therefore converge on
the same durable authority and at most one external replacement.

The maintained destructive/fault matrix uses only the fake host/runtime and
disposable Git/Dolt fixtures. Production packages do not import
`internal/testkit/executionoracle`.
