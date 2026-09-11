// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/mcuadros/director-engine/domain"
	candidatedomain "github.com/mcuadros/director-engine/domain/candidate"
	cleanupdomain "github.com/mcuadros/director-engine/domain/cleanup"
	domainexecution "github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/domain/runtimebudget"
	gitport "github.com/mcuadros/director-engine/ports/git"
	storeport "github.com/mcuadros/director-engine/ports/taskstore"
)

// EffectHandoffError means an adapter returned an error only after the
// dispatching phase was durable. The external result is unknown and startup
// reconciliation must observe it before making another decision.
type EffectHandoffError struct {
	Kind domainexecution.EffectKind
	err  error
}

func (failure *EffectHandoffError) Error() string {
	return fmt.Sprintf("%s outcome requires reconciliation", failure.Kind)
}

func (failure *EffectHandoffError) Unwrap() error {
	return failure.err
}

// StartupCommandSchemaVersion is the closed M1 startup command contract.
const StartupCommandSchemaVersion = "director.application.startup/v1"

// StartupCommand identifies one engine startup. RequestID is retained across
// retries within that process and changed only for a new startup. NowMillis is
// supplied by TaskStore time rather than a connector or model clock.
type StartupCommand struct {
	SchemaVersion string
	RequestID     string
	NowMillis     int64
}

// StartupRunResult is a bounded projection of facts recovered for one Run.
type StartupRunResult struct {
	RunID             string
	ReconciliationID  string
	RecoveredCommands int
	WorktreeID        string
	WorkspaceID       string
	AgentID           string
	CandidateID       string
	CandidateSHA      string
	CleanupIntents    int
	Progressed        bool
	HandoffUnknown    bool
	NeedsYou          bool
	Terminal          bool
}

// StartupResult reports one complete durable scan followed by at most one
// resumed transition for every active walking-skeleton Run.
type StartupResult struct {
	Projects int
	Tasks    int
	Runs     []StartupRunResult
}

type durableStartupRun struct {
	project   domain.Project
	task      domain.Task
	run       domain.Run
	commands  []string
	candidate *domain.Candidate
}

type observedStartupRun struct {
	durable              durableStartupRun
	operational          *domainexecution.OperationalObservation
	frontierKind         domainexecution.EffectKind
	frontierObservation  *domainexecution.EffectObservation
	effectObservations   []domainexecution.EffectObservation
	candidateObservation *domainexecution.CandidateObservation
	hostCursor           uint64
	unsafeCode           domainexecution.NeedCode
}

func expectedEffect(runID string, kind domainexecution.EffectKind) string {
	return stableID("effect", runID, string(kind))
}

func validDurableEffect(run domain.Run, effect domainexecution.Effect, kind domainexecution.EffectKind, attempts uint32, required bool, expectedID string) error {
	if effect.ID == "" {
		if required {
			return fmt.Errorf("%s intent is missing", kind)
		}
		return nil
	}
	if expectedID == "" {
		expectedID = expectedEffect(run.ID, kind)
	}
	if effect.ID != expectedID || effect.Kind != kind ||
		effect.AttemptLimit != attempts || effect.Attempt > effect.AttemptLimit {
		return fmt.Errorf("%s intent identity is invalid", kind)
	}
	switch effect.Phase {
	case domainexecution.EffectIntentRecorded, domainexecution.EffectDispatching:
		if effect.ExternalID != "" || effect.ObservedFactHash != "" || effect.ObservedCorrelation != "" {
			return fmt.Errorf("%s incomplete intent has an external identity", kind)
		}
	case domainexecution.EffectComplete:
		if effect.ExternalID == "" || !identifierPattern.MatchString(effect.ExternalID) || len(effect.ExternalID) > 128 ||
			!domainexecution.ValidPrimaryDigest(effect.ObservedFactHash) {
			return fmt.Errorf("%s completed intent lacks an external identity", kind)
		}
	default:
		return fmt.Errorf("%s intent phase is invalid", kind)
	}
	if effect.Observation != nil {
		if effect.Observation.EffectID != effect.ID ||
			effect.Observation.BindingHash != run.Execution.RepositoryBindingHash ||
			(effect.Observation.ExternalID != "" &&
				(!identifierPattern.MatchString(effect.Observation.ExternalID) || len(effect.Observation.ExternalID) > 128)) ||
			!domainexecution.ValidEffectObservation(*effect.Observation) {
			return fmt.Errorf("%s observation is invalid", kind)
		}
	}
	return nil
}

func effectProgressed(effect domainexecution.Effect) bool {
	return effect.Phase != domainexecution.EffectIntentRecorded || effect.Attempt > 0 || effect.Observation != nil
}

func controlContainmentComplete(state domainexecution.State) bool {
	if state.Control.SchemaVersion == "" ||
		(state.Control.Intent.Kind != domainexecution.ControlCancelTask && state.Control.Intent.Kind != domainexecution.ControlEmergencyStop) {
		return false
	}
	for _, target := range state.Control.Targets {
		if target.Archive.Phase != domainexecution.EffectComplete || !target.Archived || !target.ProcessAbsent {
			return false
		}
	}
	return true
}

func controlCleanupComplete(state domainexecution.State) bool {
	if !controlContainmentComplete(state) || !state.Control.Recovery.Preserved {
		return false
	}
	switch state.Control.Recovery.Mode {
	case domainexecution.RecoveryRetain:
		return state.HostViewArchive.ID == "" && state.WorktreeRemove.ID == ""
	case domainexecution.RecoverySnapshotThenDelete:
		return state.Control.Recovery.Snapshot.Phase == domainexecution.EffectComplete &&
			state.Control.Recovery.ArtifactID != "" && state.Control.Recovery.CleanupAuthorized &&
			state.HostViewArchive.Phase == domainexecution.EffectComplete && state.WorktreeRemove.Phase == domainexecution.EffectComplete
	default:
		return false
	}
}

func validateExecutionGraph(run domain.Run) error {
	state := run.Execution
	if state.SchemaVersion != domainexecution.SchemaVersion ||
		state.Scope.ProjectID == "" || state.Scope.WorkspaceID == "" ||
		state.Scope.TaskID != run.TaskID || state.Scope.RunID != run.ID ||
		state.StartCommandID == "" ||
		state.EffectiveProfiles == nil || !state.EffectiveProfiles.Valid() ||
		state.EffectiveProfilesSHA256 != state.EffectiveProfiles.SHA256() ||
		!domainexecution.ValidLeaseBinding(state.LeaseBinding) ||
		state.RepositoryBindingHash == "" ||
		state.RepositoryBindingHash != domainexecution.RepositoryBindingSHA256(state.RepositoryBinding) ||
		state.RepositoryBinding.SourcePath != state.SourcePath ||
		state.RepositoryBinding.WorktreePath != state.WorktreePath ||
		state.RepositoryBinding.Branch != state.Branch || state.RepositoryBinding.BaseSHA != run.BaseSHA ||
		!domainexecution.ValidPrimarySession(state.PrimarySession) ||
		!domainexecution.ValidHelpers(state) ||
		!domainexecution.PrimarySessionMatchesProfiles(state.PrimarySession, *state.EffectiveProfiles) ||
		state.PrimarySession.Scope != state.Scope ||
		state.PrimarySession.EffectiveProfilesSHA256 != state.EffectiveProfilesSHA256 ||
		state.PrimarySession.AgentIntentID != func() string {
			if state.PrimaryRecovery.Authority != nil && state.PrimaryRecovery.Phase == domainexecution.PrimaryRecoveryComplete {
				return state.PrimaryRecovery.Authority.ReplacementEffectID
			}
			return expectedEffect(run.ID, domainexecution.EffectAgentCreate)
		}() {
		return errors.New("Run execution scope or repository binding is invalid")
	}
	if !runtimebudget.ValidLedger(state.Budget) ||
		!runtimebudget.ValidTurnDemand(state.Budget.Policy, state.TurnBudgetDemand) ||
		state.Budget.Policy.Revision != state.EffectiveProfiles.ConfigurationSHA256() {
		return errors.New("Run runtime budget is invalid")
	}
	legacyRecovery := state.RecoveryPolicy.SchemaVersion == "" && state.PrimaryRecovery.SchemaVersion == ""
	if (!legacyRecovery && !domainexecution.ValidPrimaryRecoveryPolicy(state.RecoveryPolicy)) ||
		!domainexecution.ValidPrimaryRecovery(state.PrimaryRecovery, state) {
		return errors.New("Run primary recovery state is invalid")
	}
	if !domainexecution.ValidControlPolicy(state.ControlPolicy) || !domainexecution.ValidRunControl(state.Control, state) {
		return errors.New("Run execution control is invalid")
	}
	if state.CleanupPolicy != nil && !cleanupdomain.ValidPolicy(*state.CleanupPolicy) {
		return errors.New("Run cleanup policy is invalid")
	}
	if state.Cleanup != nil && (state.CleanupPolicy == nil || !cleanupdomain.ValidState(*state.Cleanup) ||
		state.Cleanup.Policy.SHA256 != state.CleanupPolicy.SHA256 || state.Cleanup.Binding.RunID != run.ID ||
		state.Cleanup.Binding.TaskID != run.TaskID || state.Cleanup.Binding.RepositoryBindingSHA256 != state.RepositoryBindingHash ||
		state.Cleanup.Binding.LeaseEpoch != state.LeaseBinding.Epoch) {
		return errors.New("Run cleanup state is invalid")
	}
	if state.LastStartupReconciliation != nil &&
		!domainexecution.ValidStartupReconciliation(*state.LastStartupReconciliation) {
		return errors.New("last startup reconciliation is invalid")
	}
	if len(state.MCPCommandReceipts) > domainexecution.MaximumMCPCommandReceipts {
		return errors.New("MCP command receipt ledger exceeds its bound")
	}
	seenMCPCommands := make(map[string]struct{}, len(state.MCPCommandReceipts))
	for _, receipt := range state.MCPCommandReceipts {
		if !domainexecution.ValidMCPCommandReceipt(receipt) {
			return errors.New("MCP command receipt is invalid")
		}
		if _, duplicate := seenMCPCommands[receipt.CommandKey]; duplicate {
			return errors.New("MCP command receipt is duplicated")
		}
		seenMCPCommands[receipt.CommandKey] = struct{}{}
	}
	for _, fixture := range []struct {
		effect     domainexecution.Effect
		kind       domainexecution.EffectKind
		attempts   uint32
		required   bool
		expectedID string
	}{
		{state.Worktree, domainexecution.EffectWorktreeCreate, 2, true, ""},
		{state.HostView, domainexecution.EffectHostViewCreate, 2, true, ""},
		{state.Boundary, domainexecution.EffectBoundaryMaterialize, 2, true, ""},
		{state.Setup, domainexecution.EffectSetupRun, 2, setupRequired(state.LifecycleSurfaces), ""},
		{state.Agent, domainexecution.EffectAgentCreate, 2, false, func() string {
			if state.PrimaryRecovery.Authority != nil && state.PrimaryRecovery.Phase == domainexecution.PrimaryRecoveryComplete {
				return state.PrimaryRecovery.Authority.ReplacementEffectID
			}
			return ""
		}()},
		{state.AgentPrompt, domainexecution.EffectAgentPrompt, func() uint32 {
			if state.PrimaryRecovery.Authority != nil && state.PrimaryRecovery.Phase == domainexecution.PrimaryRecoveryComplete {
				return 1
			}
			return 2
		}(), false, func() string {
			if state.PrimaryRecovery.Authority != nil && state.PrimaryRecovery.Phase == domainexecution.PrimaryRecoveryComplete {
				return state.PrimaryRecovery.ReplacementPrompt.ID
			}
			return ""
		}()},
		{state.AgentArchive, domainexecution.EffectAgentArchive, 2, false, ""},
		{state.HostViewArchive, domainexecution.EffectHostViewArchive, 2, false, ""},
		{state.WorktreeRemove, domainexecution.EffectWorktreeRemove, 1, false, ""},
	} {
		if err := validDurableEffect(run, fixture.effect, fixture.kind, fixture.attempts, fixture.required, fixture.expectedID); err != nil {
			return err
		}
	}
	if !setupRequired(state.LifecycleSurfaces) && state.Setup.ID != "" {
		return errors.New("Run has an undeclared lifecycle setup intent")
	}
	if effectProgressed(state.HostView) && state.Worktree.Phase != domainexecution.EffectComplete {
		return errors.New("host-view effect precedes worktree creation")
	}
	if effectProgressed(state.Boundary) && state.HostView.Phase != domainexecution.EffectComplete {
		return errors.New("isolation effect precedes host-view registration")
	}
	if state.Setup.ID != "" && effectProgressed(state.Setup) && state.Boundary.Phase != domainexecution.EffectComplete {
		return errors.New("lifecycle setup precedes isolation")
	}
	if state.PreparationReady && (state.Worktree.Phase != domainexecution.EffectComplete ||
		state.HostView.Phase != domainexecution.EffectComplete ||
		state.Boundary.Phase != domainexecution.EffectComplete ||
		(state.Setup.ID != "" && state.Setup.Phase != domainexecution.EffectComplete) ||
		state.PreparationBarrierHash == "" || state.PreparationBarrierHash != preparationBarrier(state)) {
		return errors.New("preparation_ready is not bound to completed preparation facts")
	}
	if state.Agent.ID != "" && !state.PreparationReady {
		return errors.New("Task Agent intent precedes preparation_ready")
	}
	if state.WorkerVisibility != nil && state.WorkerVisibility.AgentID != "" &&
		(state.Agent.Phase != domainexecution.EffectComplete || state.WorkerVisibility.AgentID != state.Agent.ExternalID) {
		return errors.New("persisted worker identity is not bound to completed bootstrap creation")
	}
	if state.WorkerVisibility != nil && state.WorkerVisibility.AgentID != "" &&
		(state.WorkerVisibility.ObservedDigest != state.WorkerVisibility.Digest ||
			state.Agent.ObservedCorrelation != state.WorkerVisibility.Digest ||
			state.PrimarySession.NativeAgentID != state.Agent.ExternalID || state.PrimarySession.BindingSHA256 == "") {
		return errors.New("persisted worker identity lacks exact host and session evidence")
	}
	if state.AgentPrompt.ID != "" && (state.Agent.Phase != domainexecution.EffectComplete ||
		state.WorkerVisibility == nil || state.WorkerVisibility.AgentID != state.Agent.ExternalID) {
		return errors.New("real prompt precedes persisted worker identity")
	}
	if state.Claim != nil && (state.AgentPrompt.Phase != domainexecution.EffectComplete || !validClaim(*state.Claim, run)) {
		return errors.New("Run completed claim is not bound to its execution")
	}
	seenCompletionEvents := make(map[string]string, len(state.CompletionEventReceipts))
	for _, receipt := range state.CompletionEventReceipts {
		if receipt.EventID == "" || receipt.EventFactHash == "" || receipt.Cursor == 0 ||
			receipt.Event.ID != receipt.EventID || receipt.Event.FactHash != receipt.EventFactHash ||
			receipt.Event.Cursor != receipt.Cursor || !domainexecution.ValidCompletionEvent(receipt.Event) ||
			(receipt.Phase != domainexecution.CompletionReceiptIntent && receipt.Phase != domainexecution.CompletionReceiptComplete) {
			return errors.New("completion-event receipt ledger is invalid")
		}
		if prior, duplicate := seenCompletionEvents[receipt.EventID]; duplicate || prior != "" {
			return errors.New("completion-event receipt ledger contains a duplicate identity")
		}
		seenCompletionEvents[receipt.EventID] = receipt.EventFactHash
		if receipt.Phase == domainexecution.CompletionReceiptComplete &&
			(receipt.EnqueuedAtMillis < 0 || receipt.DispatchLatencyMillis < 0 ||
				receipt.DispatchLatencyMillis >= domainexecution.CompletionDispatchTargetMillis) {
			return errors.New("completion-event dispatch receipt is invalid or late")
		}
	}
	if len(state.CompletionEventReceipts) > domainexecution.MaximumCompletionEventReceipts {
		return errors.New("completion-event receipt ledger exceeds its bound")
	}
	if state.CandidateObservation != nil {
		if state.Claim == nil || state.CandidateClaim == nil || state.CandidateClaim.ID != state.Claim.ID ||
			state.CandidateObservation.ClaimSHA256 != candidatedomain.ClaimSHA256(*state.CandidateClaim) ||
			state.CandidateObservation.RepositoryBindingSHA256 != state.RepositoryBindingHash ||
			!domainexecution.ValidCandidateObservation(*state.CandidateObservation) {
			return errors.New("Run Candidate observation is invalid")
		}
	}
	if run.CurrentCandidateID != "" && state.CandidateObservation == nil {
		return errors.New("current Candidate lacks its admission observation")
	}
	controlContainment := controlContainmentComplete(state)
	if state.AgentArchive.ID != "" && run.CurrentCandidateID == "" && state.Control.SchemaVersion == "" {
		return errors.New("cleanup intent precedes Candidate admission")
	}
	if state.HostViewArchive.ID != "" && state.AgentArchive.Phase != domainexecution.EffectComplete && !controlContainment {
		return errors.New("host-view cleanup precedes agent termination")
	}
	if state.WorktreeRemove.ID != "" && state.HostViewArchive.Phase != domainexecution.EffectComplete {
		return errors.New("worktree cleanup precedes host-view archival")
	}
	productCleanupComplete := state.Cleanup != nil && cleanupdomain.ValidState(*state.Cleanup) &&
		(state.Cleanup.Phase == cleanupdomain.PhaseComplete || state.Cleanup.Phase == cleanupdomain.PhaseRetained)
	if state.Terminal && !productCleanupComplete && !controlCleanupComplete(state) && (state.AgentArchive.Phase != domainexecution.EffectComplete ||
		state.HostViewArchive.Phase != domainexecution.EffectComplete || state.WorktreeRemove.Phase != domainexecution.EffectComplete) {
		return errors.New("terminal Run lacks completed cleanup facts")
	}
	return nil
}

func loadRunEvents(ctx context.Context, store storeport.TaskStore, runID string) ([]domain.Event, error) {
	events := make([]domain.Event, 0)
	var cursor uint64
	for {
		page, err := store.Events(ctx, domain.EventQuery{RunID: runID, AfterGlobalSequence: cursor, Limit: 1000})
		if err != nil {
			return nil, err
		}
		events = append(events, page...)
		if len(page) < 1000 {
			return events, nil
		}
		cursor = page[len(page)-1].GlobalSequence
	}
}

func commandForEvent(run domain.Run, event domain.Event, candidate *domain.Candidate) (string, string, error) {
	switch event.Type {
	case "run.primary_execution.created":
		if event.AggregateVersion != 0 {
			return "", "", errors.New("Run create event version is invalid")
		}
		return run.Execution.StartCommandID, "run.primary_execution.create", nil
	case "candidate.admitted":
		if candidate == nil {
			return "", "", errors.New("Candidate event lacks an immutable Candidate")
		}
		return stableID("command", run.ID, fmt.Sprintf("candidate-%d", candidate.Sequence), candidate.CommitSHA), "candidate.admit", nil
	default:
		if event.AggregateVersion == 0 {
			return "", "", errors.New("Run transition has a create version")
		}
		return stableID("command", run.ID, fmt.Sprintf("version-%d", event.AggregateVersion), event.Type), event.Type, nil
	}
}

func (controller *Controller) scanDurableRun(ctx context.Context, project domain.Project, task domain.Task, run domain.Run) (durableStartupRun, error) {
	result := durableStartupRun{project: project, task: task, run: run}
	if run.Execution.Scope.ProjectID != project.ID || task.ProjectID != project.ID {
		return result, errors.New("Run execution Project identity is invalid")
	}
	if err := validateExecutionGraph(run); err != nil {
		return result, err
	}
	candidates, err := controller.store.Candidates(ctx, run.ID)
	if err != nil {
		return result, err
	}
	if run.CurrentCandidateID == "" {
		if len(candidates) != 0 {
			return result, errors.New("Run has an unprojected Candidate")
		}
	} else {
		if len(candidates) == 0 || candidates[len(candidates)-1].ID != run.CurrentCandidateID ||
			run.Execution.Claim == nil || run.Execution.CandidateClaim == nil ||
			candidates[len(candidates)-1].RunID != run.ID ||
			candidates[len(candidates)-1].Sequence != uint64(len(candidates)) ||
			candidates[len(candidates)-1].ID != candidatedomain.RecordID(run.ID, uint64(len(candidates)), run.Execution.Claim.CandidateSHA) ||
			candidates[len(candidates)-1].CommitSHA != run.Execution.Claim.CandidateSHA ||
			run.Execution.CandidateObservation == nil ||
			run.Execution.CandidateObservation.CommitSHA != candidates[len(candidates)-1].CommitSHA ||
			run.Execution.CandidateObservation.BaseSHA != run.BaseSHA ||
			run.Execution.CandidateAuthority == nil || run.Execution.CandidateAuthority.CandidateID != run.CurrentCandidateID {
			return result, errors.New("Run Candidate projection is invalid")
		}
		for index, candidate := range candidates {
			if candidate.Sequence != uint64(index+1) {
				return result, errors.New("Run Candidate sequence is invalid")
			}
		}
		candidate := candidates[len(candidates)-1]
		result.candidate = &candidate
	}
	events, err := loadRunEvents(ctx, controller.store, run.ID)
	if err != nil {
		return result, err
	}
	if uint64(len(events)) != run.Version+1 {
		return result, errors.New("Run command/event history is incomplete")
	}
	seen := make(map[string]struct{}, len(events))
	for index, event := range events {
		if event.RunID != run.ID || event.AggregateID != run.ID ||
			event.AggregateVersion != uint64(index) || event.Sequence != uint64(index)+1 {
			return result, errors.New("Run event history is not contiguous")
		}
		commandID, commandType, err := commandForEvent(run, event, result.candidate)
		if err != nil {
			return result, err
		}
		if _, duplicate := seen[commandID]; duplicate {
			return result, errors.New("Run command history contains a duplicate identity")
		}
		command, err := controller.store.Command(ctx, commandID)
		if err != nil {
			return result, err
		}
		expectedVersion := uint64(0)
		if event.AggregateVersion > 0 {
			expectedVersion = event.AggregateVersion - 1
		}
		if command.IdempotencyKey != commandID || command.Type != commandType ||
			command.AggregateID != run.ID || command.ExpectedVersion != expectedVersion ||
			command.Outcome != domain.CommandApplied || command.ObservedVersion != event.AggregateVersion ||
			command.EventID != event.ID {
			return result, errors.New("Run command outcome is not bound to its event")
		}
		seen[commandID] = struct{}{}
		result.commands = append(result.commands, commandID)
	}
	return result, nil
}

func cleanupIntentFacts(state domainexecution.State) []domainexecution.CleanupIntentFact {
	result := make([]domainexecution.CleanupIntentFact, 0, 3+len(state.Helpers)+len(state.Control.Targets))
	for _, effect := range []domainexecution.Effect{state.AgentArchive, state.HostViewArchive, state.WorktreeRemove} {
		if effect.ID != "" {
			result = append(result, domainexecution.CleanupIntentFact{
				EffectID: effect.ID, Kind: effect.Kind, Phase: effect.Phase, Attempt: effect.Attempt,
			})
		}
	}
	for _, helper := range state.Helpers {
		for _, effect := range []domainexecution.Effect{helper.Archive, helper.CheckoutRemove} {
			if effect.ID != "" {
				result = append(result, domainexecution.CleanupIntentFact{
					EffectID: effect.ID, Kind: effect.Kind, Phase: effect.Phase, Attempt: effect.Attempt,
				})
			}
		}
	}
	for _, target := range state.Control.Targets {
		if target.Archive.ID != "" {
			result = append(result, domainexecution.CleanupIntentFact{
				EffectID: target.Archive.ID, Kind: target.Archive.Kind,
				Phase: target.Archive.Phase, Attempt: target.Archive.Attempt,
			})
		}
	}
	if state.Control.Recovery.Snapshot.ID != "" {
		effect := state.Control.Recovery.Snapshot
		result = append(result, domainexecution.CleanupIntentFact{
			EffectID: effect.ID, Kind: effect.Kind, Phase: effect.Phase, Attempt: effect.Attempt,
		})
	}
	if state.PrimaryRecovery.Archive.ID != "" {
		effect := state.PrimaryRecovery.Archive
		result = append(result, domainexecution.CleanupIntentFact{
			EffectID: effect.ID, Kind: effect.Kind, Phase: effect.Phase, Attempt: effect.Attempt,
		})
	}
	if state.Cleanup != nil {
		phase := func(value cleanupdomain.EffectPhase) domainexecution.EffectPhase {
			switch value {
			case cleanupdomain.EffectIntent, cleanupdomain.EffectRetained:
				return domainexecution.EffectIntentRecorded
			case cleanupdomain.EffectDispatching, cleanupdomain.EffectObservationRequired:
				return domainexecution.EffectDispatching
			case cleanupdomain.EffectComplete:
				return domainexecution.EffectComplete
			default:
				return ""
			}
		}
		for _, effect := range append(append([]cleanupdomain.Effect(nil), state.Cleanup.Effects...), state.Cleanup.RetentionEffects...) {
			result = append(result, domainexecution.CleanupIntentFact{EffectID: effect.ID,
				Kind: domainexecution.EffectKind("cleanup." + string(effect.Kind)), Phase: phase(effect.Phase), Attempt: effect.Attempt})
		}
	}
	return result
}

func startupFrontier(run domain.Run) domainexecution.EffectKind {
	state := run.Execution
	if state.Cleanup != nil {
		// The dedicated cleanup service owns its exact effect frontier and
		// reconstructs it from state.Cleanup; the walking-skeleton closure path
		// must not create a second archive or removal intent.
		return ""
	}
	if run.CurrentCandidateID != "" {
		if state.AgentArchive.ID == "" {
			return domainexecution.EffectAgentCreate
		}
		if state.AgentArchive.Phase != domainexecution.EffectComplete {
			return domainexecution.EffectAgentArchive
		}
		if state.HostViewArchive.ID == "" {
			return domainexecution.EffectHostViewCreate
		}
		if state.HostViewArchive.Phase != domainexecution.EffectComplete {
			return domainexecution.EffectHostViewArchive
		}
		if state.WorktreeRemove.ID == "" {
			return domainexecution.EffectWorktreeCreate
		}
		if state.WorktreeRemove.Phase != domainexecution.EffectComplete {
			return domainexecution.EffectWorktreeRemove
		}
		return ""
	}
	for _, effect := range []domainexecution.Effect{state.Worktree, state.HostView, state.Boundary} {
		if effect.Phase != domainexecution.EffectComplete {
			return effect.Kind
		}
	}
	if setupRequired(state.LifecycleSurfaces) && state.Setup.Phase != domainexecution.EffectComplete {
		return state.Setup.Kind
	}
	if state.Agent.ID == "" {
		return ""
	}
	if state.Agent.Phase != domainexecution.EffectComplete {
		return domainexecution.EffectAgentCreate
	}
	if state.AgentPrompt.ID == "" {
		return ""
	}
	if state.AgentPrompt.Phase != domainexecution.EffectComplete {
		return domainexecution.EffectAgentPrompt
	}
	return ""
}

func resourceStillExpected(closeEffect domainexecution.Effect) bool {
	return closeEffect.ID == "" || closeEffect.Phase == domainexecution.EffectIntentRecorded
}

func startupObservationKinds(run domain.Run) []domainexecution.EffectKind {
	state := run.Execution
	result := make([]domainexecution.EffectKind, 0, 6)
	seen := make(map[domainexecution.EffectKind]struct{}, 6)
	add := func(kind domainexecution.EffectKind) {
		if kind == "" {
			return
		}
		if _, exists := seen[kind]; exists {
			return
		}
		seen[kind] = struct{}{}
		result = append(result, kind)
	}
	if state.Worktree.Phase == domainexecution.EffectComplete && resourceStillExpected(state.WorktreeRemove) {
		add(domainexecution.EffectWorktreeCreate)
	}
	if state.HostView.Phase == domainexecution.EffectComplete && resourceStillExpected(state.HostViewArchive) {
		add(domainexecution.EffectHostViewCreate)
	}
	if state.Boundary.Phase == domainexecution.EffectComplete && resourceStillExpected(state.WorktreeRemove) {
		add(domainexecution.EffectBoundaryMaterialize)
	}
	if state.Setup.ID != "" && state.Setup.Phase == domainexecution.EffectComplete && resourceStillExpected(state.WorktreeRemove) {
		add(domainexecution.EffectSetupRun)
	}
	if state.Agent.Phase == domainexecution.EffectComplete && resourceStillExpected(state.AgentArchive) {
		add(domainexecution.EffectAgentCreate)
	}
	add(startupFrontier(run))
	return result
}

func exactCompletedIdentity(effect domainexecution.Effect, observation domainexecution.EffectObservation) bool {
	return effect.Phase != domainexecution.EffectComplete ||
		(observation.Status == domainexecution.ObservationDesired &&
			observation.ExternalID == effect.ExternalID)
}

func exactCleanupIdentity(state domainexecution.State, kind domainexecution.EffectKind, observation domainexecution.EffectObservation) bool {
	expected := ""
	switch kind {
	case domainexecution.EffectAgentArchive:
		expected = state.Agent.ExternalID
	case domainexecution.EffectHostViewArchive:
		expected = state.HostView.ExternalID
	case domainexecution.EffectWorktreeRemove:
		expected = state.Worktree.ExternalID
	default:
		return true
	}
	return expected != "" && observation.ExternalID == expected
}

func exactCandidateFacts(run domain.Run, observation domainexecution.CandidateObservation, nowMillis int64) bool {
	if run.Execution.CandidateClaim == nil {
		return false
	}
	decision := candidatedomain.Evaluate(*run.Execution.CandidateClaim, observation, run.Execution.RepositoryBindingHash, nowMillis)
	return decision.Kind == candidatedomain.DecisionAdmit && decision.Manifest != nil &&
		run.Execution.CandidateAuthority != nil &&
		decision.Manifest.CandidateSHA == run.Execution.CandidateAuthority.CandidateSHA &&
		decision.Manifest.BaseSHA == run.Execution.CandidateAuthority.BaseSHA &&
		decision.Manifest.TreeSHA != "" && decision.Manifest.DiffSHA256 != "" && decision.Manifest.ChangedPathsSHA256 != ""
}

func (controller *Controller) observeStartupRun(ctx context.Context, durable durableStartupRun, nowMillis int64) (observedStartupRun, error) {
	result := observedStartupRun{durable: durable}
	run := durable.run
	result.hostCursor = hostResumeCursor(run.Execution)
	if run.Execution.Terminal {
		return result, nil
	}
	operational, err := controller.runtime.ObserveOperational(ctx, run.Execution.Scope, run.Execution.OperationalPolicy)
	if err != nil {
		return result, err
	}
	if operational.ID == "" {
		return result, errors.New("startup operational observation identity is missing")
	}
	result.operational = &operational
	frontier := startupFrontier(run)
	result.frontierKind = frontier
	for _, kind := range startupObservationKinds(run) {
		observation, err := controller.observeEffectAfter(ctx, run, kind, result.hostCursor)
		if err != nil {
			return result, err
		}
		if !domainexecution.CurrentEffectObservation(observation, nowMillis) {
			return result, errors.New("startup effect observation is invalid or stale")
		}
		result.effectObservations = append(result.effectObservations, observation)
		if kind == frontier {
			frontierObservation := observation
			result.frontierObservation = &frontierObservation
		}
		if observation.Cursor > 0 {
			if result.hostCursor > 0 && observation.Cursor <= result.hostCursor {
				result.unsafeCode = domainexecution.NeedCode("startup_host_cursor_not_monotonic")
			}
			result.hostCursor = observation.Cursor
		}
		effect := effectPointer(&run.Execution, kind)
		if effect == nil || observation.EffectID != effect.ID ||
			observation.BindingHash != run.Execution.RepositoryBindingHash {
			result.unsafeCode = domainexecution.NeedCode("startup_execution_identity_mismatch")
		} else if !exactCompletedIdentity(*effect, observation) {
			result.unsafeCode = domainexecution.NeedCode("startup_execution_identity_mismatch")
		} else if !exactCleanupIdentity(run.Execution, kind, observation) {
			result.unsafeCode = domainexecution.NeedCode("startup_execution_identity_mismatch")
		}
	}
	if run.Execution.CandidateClaim != nil &&
		(run.Execution.WorktreeRemove.ID == "" || run.Execution.WorktreeRemove.Phase == domainexecution.EffectIntentRecorded) {
		if controller.candidateGit == nil {
			return result, errors.New("startup Candidate Git observer is unavailable")
		}
		observation, err := controller.candidateGit.ObserveCandidate(ctx, candidateRequest(run, nowMillis))
		if err != nil {
			return result, err
		}
		if !domainexecution.CurrentCandidateObservation(observation, nowMillis) {
			return result, errors.New("startup Candidate observation is invalid or stale")
		}
		result.candidateObservation = &observation
		if durable.candidate != nil && !exactCandidateFacts(run, observation, nowMillis) {
			result.unsafeCode = domainexecution.NeedCode("startup_candidate_facts_not_admitted")
		}
	}
	return result, nil
}

func candidateRequest(run domain.Run, nowMillis int64) gitport.CandidateRequest {
	return gitport.CandidateRequest{
		Claim: *run.Execution.CandidateClaim, Repository: run.Execution.RepositoryBinding,
		RepositoryBindingSHA256: run.Execution.RepositoryBindingHash, TaskStoreNowMillis: nowMillis,
	}
}

func startupSnapshot(request StartupCommand, observed observedStartupRun) domainexecution.StartupReconciliation {
	run := observed.durable.run
	snapshot := domainexecution.StartupReconciliation{
		SchemaVersion:             domainexecution.StartupReconciliationSchemaVersion,
		ID:                        stableID("startup-reconciliation", run.ID, request.RequestID),
		ObservedRunVersion:        run.Version,
		ObservedAtMillis:          request.NowMillis,
		CommandCount:              uint64(len(observed.durable.commands)),
		CommandChainHash:          hashText(strings.Join(observed.durable.commands, "\x1f")),
		WorktreeID:                run.Execution.Worktree.ExternalID,
		WorkspaceID:               run.Execution.HostView.ExternalID,
		AgentID:                   run.Execution.Agent.ExternalID,
		HelperCount:               uint64(len(run.Execution.Helpers)),
		HelperChainHash:           domainexecution.HelperGraphHash(run.Execution.Helpers),
		CleanupIntents:            cleanupIntentFacts(run.Execution),
		FrontierEffectKind:        observed.frontierKind,
		HostCursor:                observed.hostCursor,
		PrimaryRecoveryPhase:      run.Execution.PrimaryRecovery.Phase,
		OriginalPrimaryAgentID:    run.Execution.PrimaryRecovery.OriginalAgent.ExternalID,
		ReplacementPrimaryAgentID: run.Execution.PrimaryRecovery.ReplacementAgent.ExternalID,
	}
	if run.Execution.PrimaryRecovery.Authority != nil {
		snapshot.ReplacementAuthorityID = run.Execution.PrimaryRecovery.Authority.ID
	}
	if observed.operational != nil {
		snapshot.OperationalObservationID = observed.operational.ID
	}
	if len(observed.effectObservations) > 0 {
		parts := make([]string, 0, len(observed.effectObservations))
		for _, observation := range observed.effectObservations {
			parts = append(parts, observation.ID+"\x1e"+observation.FactHash)
		}
		snapshot.EffectObservationCount = uint64(len(observed.effectObservations))
		snapshot.EffectObservationChainHash = hashText(strings.Join(parts, "\x1f"))
	}
	if len(observed.durable.commands) > 0 {
		snapshot.LastCommandID = observed.durable.commands[len(observed.durable.commands)-1]
	}
	if effect := effectPointer(&run.Execution, observed.frontierKind); effect != nil {
		snapshot.FrontierEffectID = effect.ID
	}
	if observed.durable.candidate != nil {
		snapshot.CandidateID = observed.durable.candidate.ID
		snapshot.CandidateSHA = observed.durable.candidate.CommitSHA
	}
	if observed.frontierObservation != nil {
		snapshot.FrontierObservationID = observed.frontierObservation.ID
		snapshot.FrontierObservationHash = observed.frontierObservation.FactHash
	}
	if observed.candidateObservation != nil {
		snapshot.CandidateObservationID = observed.candidateObservation.ID
		snapshot.CandidateObservationHash = observed.candidateObservation.FactSHA256
	}
	snapshot.FactHash = domainexecution.StartupReconciliationHash(snapshot)
	return snapshot
}

func (controller *Controller) persistStartupRun(ctx context.Context, request StartupCommand, observed observedStartupRun) (domain.Run, error) {
	run := observed.durable.run
	snapshot := startupSnapshot(request, observed)
	if run.Execution.LastStartupReconciliation != nil &&
		run.Execution.LastStartupReconciliation.ID == snapshot.ID {
		return run, nil
	}
	next := run
	next.Version = run.Version + 1
	next.Execution.LastStartupReconciliation = &snapshot
	if observed.operational != nil {
		observation := *observed.operational
		next.Execution.OperationalObservation = &observation
		next.Execution.OperationalObservationRunVersion = next.Version
		next.Execution.OperationalObservationConsumed = false
	}
	if observed.frontierObservation != nil {
		if effect := effectPointer(&next.Execution, observed.frontierKind); effect != nil && effect.Phase != domainexecution.EffectComplete {
			observation := *observed.frontierObservation
			effect.Observation = &observation
		}
	}
	commandID := stableID("command", run.ID, fmt.Sprintf("version-%d", next.Version), "run.startup_reconciled")
	payload := struct {
		Reconciliation         domainexecution.StartupReconciliation   `json:"reconciliation"`
		OperationalObservation *domainexecution.OperationalObservation `json:"operationalObservation"`
		EffectObservations     []domainexecution.EffectObservation     `json:"effectObservations,omitempty"`
		CandidateObservation   *domainexecution.CandidateObservation   `json:"candidateObservation,omitempty"`
		UnsafeCode             domainexecution.NeedCode                `json:"unsafeCode,omitempty"`
	}{snapshot, observed.operational, observed.effectObservations, observed.candidateObservation, observed.unsafeCode}
	result, err := controller.store.UpdateRun(ctx, domain.CommandRequest{
		IdempotencyKey: commandID, Type: "run.startup_reconciled", AggregateID: run.ID,
		ExpectedVersion: run.Version,
		Payload:         eventPayload(payload),
	}, next, domain.Event{
		ID: stableID("event", commandID), RunID: run.ID, Sequence: run.Version + 2,
		AggregateID: run.ID, AggregateVersion: next.Version, Type: "run.startup_reconciled",
		Payload: eventPayload(payload),
	})
	if err != nil {
		return domain.Run{}, err
	}
	if result.Outcome != domain.CommandApplied {
		return domain.Run{}, errors.New("persist startup reconciliation: version conflict")
	}
	return next, nil
}

func startupRunResult(run domain.Run) StartupRunResult {
	result := StartupRunResult{
		RunID: run.ID, RecoveredCommands: int(run.Version) + 1,
		WorktreeID:     run.Execution.Worktree.ExternalID,
		WorkspaceID:    run.Execution.HostView.ExternalID,
		AgentID:        run.Execution.Agent.ExternalID,
		CleanupIntents: len(cleanupIntentFacts(run.Execution)),
		NeedsYou:       run.Execution.NeedsYou != nil, Terminal: run.Execution.Terminal,
	}
	if run.Execution.LastStartupReconciliation != nil {
		result.ReconciliationID = run.Execution.LastStartupReconciliation.ID
	}
	result.CandidateID = run.CurrentCandidateID
	if result.CandidateID != "" && run.Execution.Claim != nil {
		result.CandidateSHA = run.Execution.Claim.CandidateSHA
	}
	return result
}

func duplicateExecutionIdentity(runs []durableStartupRun) error {
	seen := map[string]string{}
	for _, durable := range runs {
		state := durable.run.Execution
		for kind, identity := range map[string]string{
			"worktree":  state.Worktree.ExternalID,
			"workspace": state.HostView.ExternalID,
			"agent":     state.Agent.ExternalID,
		} {
			if identity == "" {
				continue
			}
			key := kind + "\x1f" + identity
			if owner, duplicate := seen[key]; duplicate && owner != durable.run.ID {
				return fmt.Errorf("duplicate %s execution identity across Runs", kind)
			}
			seen[key] = durable.run.ID
		}
		if state.PrimaryRecovery.SchemaVersion != "" {
			for kind, identity := range map[string]string{
				"original-agent":    state.PrimaryRecovery.OriginalAgent.ExternalID,
				"replacement-agent": state.PrimaryRecovery.ReplacementAgent.ExternalID,
			} {
				if identity == "" {
					continue
				}
				key := "agent\x1f" + identity
				if owner, duplicate := seen[key]; duplicate && owner != durable.run.ID {
					return fmt.Errorf("duplicate %s execution identity across Runs", kind)
				}
				seen[key] = durable.run.ID
			}
		}
	}
	return nil
}

// ReconcileStartup performs one complete read-only scan of every durable M1
// Run and its immutable Command/Candidate graph before it records observations
// or resumes any effect. Each active Run then advances by at most one existing
// reducer decision, so a restart cannot starve another Run or bypass policy.
func (controller *Controller) ReconcileStartup(ctx context.Context, command StartupCommand) (StartupResult, error) {
	if command.SchemaVersion != StartupCommandSchemaVersion ||
		!identifierPattern.MatchString(command.RequestID) || len(command.RequestID) > 128 || command.NowMillis < 0 {
		return StartupResult{}, errors.New("invalid startup reconciliation command")
	}
	version, err := controller.store.SchemaVersion(ctx)
	if err != nil {
		return StartupResult{}, err
	}
	if version != storeport.SchemaVersion {
		return StartupResult{}, storeport.ErrSchemaVersion
	}
	projects, err := controller.store.Projects(ctx)
	if err != nil {
		return StartupResult{}, err
	}
	sort.Slice(projects, func(left, right int) bool { return projects[left].ID < projects[right].ID })
	result := StartupResult{Projects: len(projects)}
	durableRuns := make([]durableStartupRun, 0)
	for _, project := range projects {
		tasks, err := controller.store.Tasks(ctx, project.ID)
		if err != nil {
			return StartupResult{}, err
		}
		sort.Slice(tasks, func(left, right int) bool { return tasks[left].ID < tasks[right].ID })
		result.Tasks += len(tasks)
		for _, task := range tasks {
			runs, err := controller.store.Runs(ctx, task.ID)
			if err != nil {
				return StartupResult{}, err
			}
			for _, run := range runs {
				if run.Execution.SchemaVersion == "" {
					continue
				}
				durable, err := controller.scanDurableRun(ctx, project, task, run)
				if err != nil {
					return StartupResult{}, fmt.Errorf("startup scan %s: %w", run.ID, err)
				}
				durableRuns = append(durableRuns, durable)
			}
		}
	}
	for _, durable := range durableRuns {
		if !currentLease(durable.project, durable.run.Execution.LeaseBinding, command.NowMillis) &&
			!safeRecoveryTakeover(durable.project, durable.run) {
			return StartupResult{}, fmt.Errorf("startup scan %s: %w", durable.run.ID, ErrProjectLeaseUnavailable)
		}
		workspace, err := controller.store.Workspace(ctx, durable.run.Execution.Scope.WorkspaceID)
		if err != nil {
			return StartupResult{}, err
		}
		if !exactRepository(durable.run, workspace) {
			return StartupResult{}, fmt.Errorf("startup scan %s: %w", durable.run.ID, ErrRepositoryBindingChanged)
		}
	}
	if err := duplicateExecutionIdentity(durableRuns); err != nil {
		return StartupResult{}, err
	}

	// Gather all external facts before the first TaskStore write or lifecycle
	// mutation. A connector/runtime failure therefore leaves every Run intact.
	observedRuns := make([]observedStartupRun, 0, len(durableRuns))
	for _, durable := range durableRuns {
		observed, err := controller.observeStartupRun(ctx, durable, command.NowMillis)
		if err != nil {
			return StartupResult{}, fmt.Errorf("startup observe %s: %w", durable.run.ID, err)
		}
		observedRuns = append(observedRuns, observed)
	}

	for _, observed := range observedRuns {
		run := observed.durable.run
		entry := startupRunResult(run)
		if observed.durable.task.Attention != nil {
			entry.NeedsYou = true
		}
		if run.Execution.Terminal {
			result.Runs = append(result.Runs, entry)
			continue
		}
		run, err = controller.persistStartupRun(ctx, command, observed)
		if err != nil {
			return StartupResult{}, err
		}
		if !currentLease(observed.durable.project, run.Execution.LeaseBinding, command.NowMillis) &&
			safeRecoveryTakeover(observed.durable.project, run) && run.Execution.PrimaryRecovery.SchemaVersion == "" {
			recovery, recoveryErr := controller.RequestPrimaryRecovery(ctx, PrimaryRecoveryCommand{
				SchemaVersion: PrimaryRecoveryCommandSchemaVersion,
				RequestID:     stableID("startup-primary-recovery", command.RequestID, run.ID),
				RunID:         run.ID, ExpectedRunVersion: run.Version, Trigger: domainexecution.RecoveryLeaseTakeover,
				RepeatedFailureCount: 1, NowMillis: command.NowMillis,
			})
			if recoveryErr != nil {
				return StartupResult{}, recoveryErr
			}
			run = recovery.Run
			entry = startupRunResult(run)
			entry.Progressed = recovery.Progressed
			result.Runs = append(result.Runs, entry)
			continue
		}
		if run.Execution.Control.SchemaVersion != "" &&
			run.Execution.Control.Intent.Kind == domainexecution.ControlCancelTask &&
			run.Execution.Control.Phase != domainexecution.ControlCancelled &&
			run.Execution.Control.Phase != domainexecution.ControlComplete &&
			run.Execution.Control.Phase != domainexecution.ControlNeedsYou {
			step, stepErr := controller.stepRunControl(ctx, run, command.NowMillis)
			entry = startupRunResult(step.Run)
			entry.Progressed = step.Progressed
			if stepErr != nil {
				var handoff *EffectHandoffError
				if !errors.As(stepErr, &handoff) {
					return StartupResult{}, stepErr
				}
				entry.HandoffUnknown = true
			}
			result.Runs = append(result.Runs, entry)
			continue
		}
		if observed.durable.project.State != "active" || observed.durable.task.Attention != nil || run.Execution.NeedsYou != nil {
			entry = startupRunResult(run)
			if observed.durable.task.Attention != nil {
				entry.NeedsYou = true
			}
			entry.Progressed = run.Version != observed.durable.run.Version
			result.Runs = append(result.Runs, entry)
			continue
		}
		if observed.unsafeCode != "" {
			if err := controller.parkRun(ctx, run, observed.unsafeCode); err != nil {
				return StartupResult{}, err
			}
			run, err = controller.store.Run(ctx, run.ID)
			if err != nil {
				return StartupResult{}, err
			}
			entry = startupRunResult(run)
			entry.Progressed = true
			result.Runs = append(result.Runs, entry)
			continue
		}
		step, stepErr := controller.step(ctx, run.ID, command.NowMillis, true)
		entry = startupRunResult(step.Run)
		entry.Progressed = step.Progressed
		if stepErr != nil {
			var handoff *EffectHandoffError
			if !errors.As(stepErr, &handoff) {
				return StartupResult{}, stepErr
			}
			entry.HandoffUnknown = true
		}
		current, err := controller.store.Run(ctx, run.ID)
		if err != nil {
			return StartupResult{}, err
		}
		updated := startupRunResult(current)
		updated.Progressed = entry.Progressed
		updated.HandoffUnknown = entry.HandoffUnknown
		result.Runs = append(result.Runs, updated)
	}
	// Project controls are recovered after the complete ordinary external scan.
	// Each control reconciler advances at most one durable frontier per Run and
	// reuses the same lease/CAS gates as command-driven execution.
	for _, project := range projects {
		if project.Control.SchemaVersion == "" || project.Control.Phase == domainexecution.ControlAwaitingConfirmation ||
			project.Control.Phase == domainexecution.ControlPaused || project.Control.Phase == domainexecution.ControlComplete {
			continue
		}
		if _, err := controller.ReconcileProjectControl(ctx, ReconcileControlCommand{
			RequestID: stableID("startup-control", command.RequestID, project.ID),
			ProjectID: project.ID, Lease: leaseBinding(project), NowMillis: command.NowMillis,
		}); err != nil {
			return StartupResult{}, fmt.Errorf("startup control reconciliation %s: %w", project.ID, err)
		}
	}
	sort.Slice(result.Runs, func(left, right int) bool { return result.Runs[left].RunID < result.Runs[right].RunID })
	return result, nil
}
