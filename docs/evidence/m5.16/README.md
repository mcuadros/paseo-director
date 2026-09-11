# Evidence-backed terminal recovery adoption

## Context and Problem (DIR-M5.13-F002)

During the real-incident corpus consolidation in `dir-m5.13`, finding `DIR-M5.13-F002` identified that `EvaluatePrimaryRecovery` could not select `adopt_existing` from any terminal provider-failure class (e.g. `terminal_policy`, `terminal_configuration`, `terminal_provider`), even when the host issue was resolved and the native session and owned worktree were safely resumable.

In the redacted app-server incident (`DIR-M5.13-INCIDENT-APP-SERVER-01` / `02`), a host environment defect (missing `bubblewrap` on PATH) caused provider turns to fail. Once the host environment was repaired, the native agent remained alive, its worktree was intact, and its prompt had already been accepted. However, because `EvaluatePrimaryRecovery` branched unconditionally to `archive_original` on any terminal failure class, it attempted to archive the live agent and provision a replacement, losing intact worktree context and failing before Task execution.

## Solution

Task `dir-m5.16` adds a closed, authoritative fact set and fail-closed deterministic recovery branch to `EvaluatePrimaryRecovery` and the execution oracle:

1. **Proven Resumable Host-Recovery Branch:**
   When replacement authority has not been consumed (`facts.Recovery.Authority == nil`) and the native agent is proven resumable:
   - `original.Status` is live (`"idle"`, `"running"`, or `"initializing"`);
   - `!original.ArchivedAtPresent`;
   - `original.BootstrapPresent`, `original.PromptPresent`, and `original.PersistenceReferencePresent` are all `true`;
   - Exact bindings hold: `TitleExact`, `WorktreeExact`, `LabelsRunExact`, `ProfileExact`, `SessionExact`, `WorkspaceID == facts.CurrentWorkspaceID`, and `!original.ParentPresent`;
   - Runtime facts hold: `RepositoryExact`, `WorktreePresent`, `WorktreeExact`, `BranchExact`, `BaseExact`;
   - Helpers are safe (`facts.HelpersSafe == true`);
   - No control or budget block dispatch (`!facts.ControlBlocksDispatch && !facts.BudgetBlocksDispatch`).

   Under these proven conditions, `EvaluatePrimaryRecovery` deterministically selects `RecoveryDispositionAdoptExisting` without resending an already accepted prompt.

2. **Fail-Closed Terminal Replacement Preservation:**
   When the agent is not proven resumable (e.g., status is `"error"` or `"closed"`, `ArchivedAtPresent` is true, or prompt/persistence proofs are absent):
   - Terminal failure classes follow the established archival and replacement workflow (`RecoveryDispositionArchiveOriginal`, then once `OriginalAgentProcessAbsent` is proven, `RecoveryDispositionAuthorizeReplace` if policy allows, or `RecoveryDispositionNeedsYou` if policy requires human intervention);
   - Ambiguous, contradictory, stale, or incomplete facts route strictly to `Needs you` with bounded codes (e.g., `NeedRecoveryPromptAmbiguous`, `NeedRecoveryTerminationUnproven`, `NeedRecoveryResourceContradictory`, `NeedRecoveryBindingContradictory`).

3. **Independent Execution Oracle Conformance:**
   `ModelPrimaryRecovery` in `engine/internal/testkit/executionoracle/recovery.go` models `OriginalResumable` independently and verifies full mathematical conformance across the complete signal, resumability, and fact mutation matrix.

The embedded canonical corpus marks `DIR-M5.13-F002` as `behaviorChanged: true`
because this Task resolves terminal adoption, while `DIR-M5.13-F001` remains
`behaviorChanged: false` here; the sibling `dir-m5.15` owns that classification
correction.

## Focused Validation

All focused validation checks pass cleanly:

```text
GOCACHE=<TEMPORARY_CACHE> go -C engine test -count=1 ./domain/execution/... ./application/execution/...
  pass (unit, property, mutation, crash, replay, concurrency, and oracle conformance tests)
npm run check:real-incident-corpus
  pass (redaction verified, 7 fixtures, divergence findings recorded)
npm run test:host
  pass (81 connector, credential, board, home, planning, and UI tests; 66 coordinator/scaffold tests)
npm run typecheck
  pass (TypeScript strict typecheck)
PATH="/usr/local/go/bin:$PATH" npm run format:check
  pass (all 516 repository files formatted)
node tools/ci/scaffold-check.test.mjs
  pass (scaffold and workflow policy suites)
```
