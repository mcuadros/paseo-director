# Execution controls

Pause, Resume, Cancel Task, and Emergency stop are Director Engine commands.
Director for Paseo only validates the generated planning wire contract,
forwards the request, and translates already-authorized host effects. It does
not decide scope, confirmation, ordering, retry, recovery, or relaunch.

## Durable control contract

Every control is an immutable, server-attributed intent followed by durable
dispatch and observation frontiers. Project and Run versions provide CAS;
the exact Project lease holder and epoch fence all observations and effects.
A takeover remains observe-only until the normal lease reconciliation gate is
complete. Replaying the same request returns its durable result. Reusing its
identity for another kind, target, actor session, or confirmation fails
closed.

Project pause changes the Project to `paused` before Run work can be added.
For each registered Task Agent, Reviewer, and helper, Director records a safe-
boundary observation. A running turn is allowed to finish. Its terminal event
only invalidates the replaceable sample; a fresh host observation must prove
the safe boundary before the Run becomes `paused`. Pause never archives an
agent, deletes a worktree, or consumes recovery authority.

Resume is a separate explicit command. The Project stays `paused` while every
nonterminal Run receives a complete startup-equivalent reconciliation of its
TaskStore command/event graph, exact repository binding, current host facts,
Candidate facts, effects, usage, and cleanup intents. Only after all Run
reconciliation records are durable does the Project return to `active`.
Already completed effects and notified prompts are adopted; they are not sent
again.

Cancel Task records a saga only on the selected Task and active Run. Project
state and sibling Tasks/Runs do not change. Emergency stop records one Project-
wide saga, pauses the Project first, and covers every active Run. Hard budget,
profile, or ordinary MCP attention cannot suppress hard containment; a lost or
stale lease still prevents dispatch and requires observation.

Hard containment is ordered:

1. archive every observed helper in stable ID order;
2. archive the independent Reviewer;
3. archive the Task Agent;
4. apply the frozen Run recovery policy and verify preservation;
5. for `snapshot_then_delete`, archive the host view and remove only the exact
   owned worktree after recovery is durable;
6. mark the Run cancelled and project the Task back to Queued.

`retain` terminates agents but keeps the Execution Workspace/worktree and
authorizes no destructive cleanup. `snapshot_then_delete` is the default; its
recovery effect must produce a verified artifact and retention deadline before
cleanup can proceed. Unknown ownership, identity, termination, preservation,
or post-handoff state routes to `control_recovery_ambiguous` with
`cleanupAuthorized=false`.

## Emergency confirmation

The planning payload has no actor, authentication, or `humanConfirmed` field.
The trusted server ingress derives the human and session. Preparing Emergency
stop mints a one-use confirmation reference bound to the exact Project version,
human, server session, challenge digest, and a 60-second validity window. It
causes no execution effect. Confirmation from an agent/model, another session,
another Project/version, an expired reference, a consumed reference, or a
caller boolean is rejected. A confirmed stop remains latched and cannot launch
anything until an explicit reconcile-first Resume completes.

## Board explanations

The engine emits fixed codes and messages:

| Code | Meaning |
|---|---|
| `project_paused_at_safe_boundary` | The Project is paused; active turns were allowed to finish. |
| `project_resume_reconciled` | Resume is reconciling durable and external facts before dispatch. |
| `task_cancelled_recovery_preserved` | The selected Run was cancelled and its configured recovery is preserved. |
| `emergency_stop_recovery_preserved` | Active Project Runs were cancelled and their configured recovery is preserved. |
| `emergency_stop_confirmation_required` | A fresh confirmation from the authenticated human session is required. |
| `control_external_observation_unavailable` | Control is waiting for a fresh authoritative observation. |
| `control_recovery_ambiguous` | Relaunch and destructive cleanup remain blocked pending recovery. |

Only `control_recovery_ambiguous` is a Needs-you condition. Ordinary Pause and
external waiting remain control/Board explanations rather than fabricated
human-attention state.

## Recovery testing

The maintained focused suite injects response loss and restart after intent,
dispatch, observation, containment, recovery, host-view archive, and worktree
removal. It also covers concurrent CAS contenders, stale lease input, Task
scope isolation, terminal-event safe boundaries, resume reconciliation,
budget precedence, generated connector drift, direct Dolt read-back, and
test-only black-box comparison with the integrated execution oracle. Runtime
packages do not import the oracle.
