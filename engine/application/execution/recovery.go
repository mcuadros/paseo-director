// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/agentprofile"
	domainexecution "github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/domain/runtimebudget"
	"github.com/mcuadros/director-engine/ports/host"
	runtimeport "github.com/mcuadros/director-engine/ports/runtime"
	"github.com/mcuadros/director-engine/reducer/escalation"
)

const PrimaryRecoveryCommandSchemaVersion = "director.application.primary-recovery/v1"

// PrimaryRecoveryCommand is an engine command keyed to one durable wake. It
// supplies no provider classification, replacement choice, or cleanup flag.
type PrimaryRecoveryCommand struct {
	SchemaVersion        string
	RequestID            string
	RunID                string
	ExpectedRunVersion   uint64
	Trigger              domainexecution.RecoveryTrigger
	RepeatedFailureCount uint32
	NowMillis            int64
}

type PrimaryRecoveryResult struct {
	Run        domain.Run
	Decision   domainexecution.PrimaryRecoveryDecision
	Progressed bool
}

func fallbackDecisionHash(state domainexecution.State) (string, error) {
	if state.EffectiveProfiles == nil || !state.EffectiveProfiles.Valid() {
		return "", errors.New("primary recovery frozen profile is unavailable")
	}
	worker, ok := state.EffectiveProfiles.Role(agentprofile.RoleWorker)
	if !ok {
		return "", errors.New("primary recovery Worker fallback decision is unavailable")
	}
	return domainexecution.PrimaryFallbackDecisionSHA256(
		state.EffectiveProfiles.CanonicalJSON(), worker.SelectedIndex, worker.ConfiguredChainSHA256,
	)
}

func sameRecoveryRequest(recovery domainexecution.PrimaryRecovery, command PrimaryRecoveryCommand) bool {
	return recovery.RequestID == command.RequestID && recovery.Trigger == command.Trigger &&
		recovery.RequestedRunVersion == command.ExpectedRunVersion && recovery.RepeatedFailureCount == command.RepeatedFailureCount
}

func safeRecoveryTakeover(project domain.Project, run domain.Run) bool {
	lease := project.Lease
	prior := run.Execution.LeaseBinding
	return lease != nil && lease.DispatchAllowed && lease.Epoch > prior.Epoch &&
		lease.PriorHolderInstance == prior.HolderInstance && lease.PriorProcessIdentity == prior.HolderProcessIdentity &&
		lease.TakeoverObservationID != "" && project.LeaseObservation != nil &&
		project.LeaseObservation.ID == lease.TakeoverObservationID
}

func newRecoveryObserve(runID, requestID string, sequence uint64) domainexecution.Effect {
	return domainexecution.Effect{
		ID:   stableID("effect", runID, "primary-recovery-observe", requestID, fmt.Sprintf("%d", sequence)),
		Kind: domainexecution.EffectPrimaryRecoveryObserve, Phase: domainexecution.EffectIntentRecorded,
	}
}

// RequestPrimaryRecovery freezes the original primary facts before any
// archive or replacement effect. A provider wake never authorizes a retry.
func (controller *Controller) RequestPrimaryRecovery(ctx context.Context, command PrimaryRecoveryCommand) (PrimaryRecoveryResult, error) {
	if command.SchemaVersion != PrimaryRecoveryCommandSchemaVersion || !identifierPattern.MatchString(command.RequestID) ||
		!identifierPattern.MatchString(command.RunID) || command.ExpectedRunVersion == 0 ||
		command.RepeatedFailureCount == 0 || command.NowMillis < 0 {
		return PrimaryRecoveryResult{}, errors.New("primary recovery command is invalid")
	}
	run, err := controller.store.Run(ctx, command.RunID)
	if err != nil {
		return PrimaryRecoveryResult{}, err
	}
	if run.Execution.PrimaryRecovery.SchemaVersion != "" {
		if !sameRecoveryRequest(run.Execution.PrimaryRecovery, command) {
			return PrimaryRecoveryResult{}, errors.New("primary recovery request identity conflicts")
		}
		return PrimaryRecoveryResult{Run: run}, nil
	}
	if run.Version != command.ExpectedRunVersion {
		return PrimaryRecoveryResult{}, errors.New("primary recovery command is stale")
	}
	if run.Execution.Terminal || run.Execution.Agent.Phase != domainexecution.EffectComplete ||
		run.Execution.Agent.ExternalID == "" || run.Execution.HostView.Phase != domainexecution.EffectComplete ||
		run.Execution.HostView.ExternalID == "" || run.Execution.AgentPrompt.ID == "" ||
		run.Execution.WorkerVisibility == nil || run.Execution.WorkerVisibility.AgentID != run.Execution.Agent.ExternalID ||
		run.Execution.PrimarySession.NativeAgentID != run.Execution.Agent.ExternalID {
		return PrimaryRecoveryResult{}, errors.New("primary recovery original worker facts are incomplete")
	}
	project, err := controller.store.Project(ctx, run.Execution.Scope.ProjectID)
	if err != nil {
		return PrimaryRecoveryResult{}, err
	}
	if !currentLease(project, run.Execution.LeaseBinding, command.NowMillis) && !safeRecoveryTakeover(project, run) {
		return PrimaryRecoveryResult{}, ErrProjectLeaseUnavailable
	}
	policy := run.Execution.RecoveryPolicy
	if policy.SchemaVersion == "" {
		policy = domainexecution.DefaultPrimaryRecoveryPolicy()
	}
	if !domainexecution.ValidPrimaryRecoveryPolicy(policy) {
		return PrimaryRecoveryResult{}, errors.New("primary recovery policy is invalid")
	}
	visibility := *run.Execution.WorkerVisibility
	recovery := domainexecution.PrimaryRecovery{
		SchemaVersion: domainexecution.PrimaryRecoverySchemaVersion,
		RequestID:     command.RequestID, Trigger: command.Trigger,
		Phase: domainexecution.PrimaryRecoveryIntentRecorded, RequestedAtMillis: command.NowMillis,
		RequestedRunVersion:  command.ExpectedRunVersion,
		RepeatedFailureCount: command.RepeatedFailureCount,
		PoisonedSession:      command.RepeatedFailureCount >= policy.PoisonedSessionFailureThreshold,
		Observe:              newRecoveryObserve(run.ID, command.RequestID, 1),
		OriginalAgent:        run.Execution.Agent, OriginalPrompt: run.Execution.AgentPrompt,
		OriginalSession: run.Execution.PrimarySession, OriginalVisibility: &visibility,
	}
	next := run
	next.Execution.RecoveryPolicy = policy
	next.Execution.PrimaryRecovery = recovery
	if next.Execution.NeedsYou != nil &&
		(next.Execution.NeedsYou.Code == domainexecution.NeedCode("agent_terminal_error") ||
			next.Execution.NeedsYou.Code == domainexecution.NeedCode("agent_terminal_permission")) {
		next.Execution.NeedsYou = nil
	}
	next.Execution.OperationalObservationConsumed = true
	won, err := controller.persistRunTransition(ctx, run, next, "run.primary_recovery_requested")
	if err != nil {
		return PrimaryRecoveryResult{}, err
	}
	if !won {
		current, readErr := controller.store.Run(ctx, run.ID)
		return PrimaryRecoveryResult{Run: current}, readErr
	}
	next.Version = run.Version + 1
	return PrimaryRecoveryResult{Run: next, Progressed: true}, nil
}

func recoveryRunLabels(scope domainexecution.Scope) (map[string]string, error) {
	definition, err := host.EmbeddedDefinition()
	if err != nil {
		return nil, err
	}
	labels := definition.WorkerRegistry.Labels
	return map[string]string{
		labels.Project: scope.ProjectID, labels.Workspace: scope.WorkspaceID,
		labels.Task: scope.TaskID, labels.Run: scope.RunID,
	}, nil
}

func recoveryProfileAndSession(session domainexecution.PrimarySession, profileHash string) (*host.AgentProfile, *host.MCPSession) {
	profile := &host.AgentProfile{
		Provider: string(session.Provider), Model: session.Model, Effort: session.Effort,
		Mode: session.Mode, PermissionMode: session.PermissionMode, SHA256: profileHash,
	}
	for _, option := range session.ProviderOptions {
		profile.ProviderOptions = append(profile.ProviderOptions, host.ProviderOption{Name: string(option.Name), Value: option.Value})
	}
	hostSession := &host.MCPSession{
		ContractVersion: session.MCPContractVersion, ContractHash: session.MCPContractSHA256,
		SessionSHA256: session.ReservationSHA256, Role: string(session.Role),
		Provider: string(session.Provider), Model: session.Model, Tools: append([]string(nil), session.MCPTools...),
		Server: host.MCPServer{Name: session.MCPServer.Name, Command: session.MCPServer.Command,
			Args: append([]string(nil), session.MCPServer.Args...), Env: mapsClone(session.MCPServer.Env)},
	}
	return profile, hostSession
}

func recoveryObservationArguments(run domain.Run) (host.Arguments, error) {
	recovery := run.Execution.PrimaryRecovery
	labels, err := recoveryRunLabels(run.Execution.Scope)
	if err != nil {
		return host.Arguments{}, err
	}
	profile, session := recoveryProfileAndSession(recovery.OriginalSession, run.Execution.EffectiveProfilesSHA256)
	return host.Arguments{
		Scope: run.Execution.Scope, EffectKind: domainexecution.EffectPrimaryRecoveryObserve,
		EffectID: recovery.Observe.ID, WorktreeID: run.Execution.Worktree.ExternalID,
		WorktreePath: run.Execution.WorktreePath, WorkspaceID: run.Execution.HostView.ExternalID,
		AgentID: recovery.OriginalAgent.ExternalID, Title: run.Execution.TaskTitle,
		ClientMessageID: stableID("message", recovery.OriginalPrompt.ID), Labels: labels,
		Profile: profile, Session: session, BindingHash: run.Execution.RepositoryBindingHash,
	}, nil
}

func normalizedHostRecoveryObservation(command host.Command, observed host.Observation) (domainexecution.EffectObservation, error) {
	if err := host.ValidateObservation(command, observed); err != nil {
		return domainexecution.EffectObservation{}, err
	}
	instant, err := time.Parse(time.RFC3339Nano, observed.ObservedAt)
	if err != nil {
		return domainexecution.EffectObservation{}, errors.New("primary recovery host observation time is invalid")
	}
	result := observed.Result
	observation := domainexecution.EffectObservation{
		ID:       stableID("host-observation", observed.RequestID, fmt.Sprintf("cursor-%d", observed.Cursor)),
		EffectID: result.EffectID, Status: result.Status, ExternalID: result.ExternalID,
		Cursor: observed.Cursor, ObservedAt: observed.ObservedAt, ObservedAtMillis: instant.UnixMilli(),
		MaximumAgeMillis: result.MaximumAgeMillis, BindingHash: result.BindingHash,
		CorrelationHash: result.CorrelationHash, PriorDispatcherAbsent: result.PriorDispatcherAbsent,
		Usage: result.Usage, NativeAgent: result.NativeAgent, Inventory: result.Inventory,
	}
	observation.FactHash = domainexecution.EffectObservationHash(observation)
	return observation, nil
}

func (controller *Controller) observePrimaryRecovery(ctx context.Context, run domain.Run, nowMillis int64) (PrimaryRecoveryResult, error) {
	if controller.recoveryRuntime == nil {
		return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoveryEnforcementUnavailable)
	}
	if run.Execution.PrimaryRecovery.Observe.Phase == domainexecution.EffectIntentRecorded {
		next := run
		next.Execution.PrimaryRecovery.Observe.Phase = domainexecution.EffectDispatching
		won, err := controller.persistRunTransition(ctx, run, next, "run.primary_recovery_observation_dispatching")
		if err != nil {
			return PrimaryRecoveryResult{}, err
		}
		if !won {
			current, readErr := controller.store.Run(ctx, run.ID)
			return PrimaryRecoveryResult{Run: current}, readErr
		}
		next.Version = run.Version + 1
		run = next
	}
	if err := controller.verifyHost(ctx); err != nil {
		return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoveryEnforcementUnavailable)
	}
	arguments, err := recoveryObservationArguments(run)
	if err != nil {
		return PrimaryRecoveryResult{}, err
	}
	hostCommand := host.Command{
		RequestID:       stableID("request", run.Execution.PrimaryRecovery.Observe.ID, "observe"),
		IdempotencyKey:  stableID("idempotency", run.Execution.PrimaryRecovery.Observe.ID, "observe"),
		ExpectedVersion: run.Version, AfterCursor: hostResumeCursor(run.Execution),
		Capability: host.CapabilityAgentObserve, Arguments: arguments,
	}
	observedHost, err := controller.host.Invoke(ctx, hostCommand)
	if err != nil {
		return PrimaryRecoveryResult{Run: run}, err
	}
	hostFact, err := normalizedHostRecoveryObservation(hostCommand, observedHost)
	if err != nil {
		return PrimaryRecoveryResult{Run: run}, err
	}
	candidateSHA := ""
	if run.CurrentCandidateID != "" {
		candidate, candidateErr := controller.store.Candidate(ctx, run.CurrentCandidateID)
		if candidateErr != nil {
			return PrimaryRecoveryResult{Run: run}, candidateErr
		}
		candidateSHA = candidate.CommitSHA
	}
	runtimeFact, err := controller.recoveryRuntime.ObservePrimaryRecovery(ctx, runtimeport.PrimaryRecoveryRequest{
		Scope: run.Execution.Scope, LeaseBinding: run.Execution.LeaseBinding,
		Repository: run.Execution.RepositoryBinding, BindingHash: run.Execution.RepositoryBindingHash,
		WorktreeID: run.Execution.Worktree.ExternalID, CandidateID: run.CurrentCandidateID,
		CandidateSHA: candidateSHA, OriginalAgentID: run.Execution.PrimaryRecovery.OriginalAgent.ExternalID,
	})
	if err != nil || !domainexecution.ValidPrimaryRuntimeRecoveryObservation(runtimeFact) {
		return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoveryEnforcementUnavailable)
	}
	observation := domainexecution.PrimaryRecoveryObservation{
		ID:                 stableID("primary-recovery-observation", hostFact.ID, runtimeFact.ID),
		ObservedRunVersion: run.Version, Host: hostFact, Runtime: runtimeFact,
	}
	observation.FactHash = domainexecution.PrimaryRecoveryObservationHash(observation)
	if !domainexecution.ValidPrimaryRecoveryObservation(observation) {
		return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoveryObservationInvalid)
	}
	next := run
	next.Execution.PrimaryRecovery.Observe.Phase = domainexecution.EffectComplete
	next.Execution.PrimaryRecovery.Observe.ExternalID = hostFact.ExternalID
	next.Execution.PrimaryRecovery.Observe.ObservedFactHash = hostFact.FactHash
	next.Execution.PrimaryRecovery.Observation = &observation
	next.Execution.PrimaryRecovery.Phase = domainexecution.PrimaryRecoveryObserved
	next.Execution.OperationalObservationConsumed = true
	if err := controller.persistRun(ctx, run, next, "run.primary_recovery_observed"); err != nil {
		return PrimaryRecoveryResult{Run: run}, err
	}
	next.Version = run.Version + 1
	return PrimaryRecoveryResult{Run: next, Progressed: true}, nil
}

func refreshRecoveryObservation(run *domain.Run, sequence uint64, phase domainexecution.PrimaryRecoveryPhase) {
	recovery := &run.Execution.PrimaryRecovery
	recovery.Observe = newRecoveryObserve(run.ID, recovery.RequestID, sequence)
	recovery.Observation = nil
	recovery.Phase = phase
	run.Execution.OperationalObservationConsumed = true
}

func (controller *Controller) parkPrimaryRecovery(ctx context.Context, run domain.Run, code domainexecution.NeedCode) (PrimaryRecoveryResult, error) {
	if run.Execution.PrimaryRecovery.Phase == domainexecution.PrimaryRecoveryNeedsYou &&
		run.Execution.PrimaryRecovery.NeedsYouCode == code && run.Execution.NeedsYou != nil && run.Execution.NeedsYou.Code == code {
		return PrimaryRecoveryResult{Run: run}, nil
	}
	escalated := escalation.Reduce(escalation.Facts{
		SchemaVersion: escalation.SchemaVersion, Scope: run.Execution.Scope,
		CauseCode: code, Reconciled: true,
		WakeCondition: "fresh_exact_recovery_facts_or_human_decision",
	})
	if escalated.Kind != escalation.DecisionNeedsYou {
		return PrimaryRecoveryResult{Run: run}, errors.New("primary recovery cause was not admitted by escalation reducer")
	}
	next := run
	next.Execution.PrimaryRecovery.Phase = domainexecution.PrimaryRecoveryNeedsYou
	next.Execution.PrimaryRecovery.NeedsYouCode = code
	next.Execution.NeedsYou = &escalated.NeedsYou
	if err := controller.persistRun(ctx, run, next, "run.primary_recovery_needs_you"); err != nil {
		return PrimaryRecoveryResult{Run: run}, err
	}
	next.Version = run.Version + 1
	return PrimaryRecoveryResult{Run: next, Decision: domainexecution.PrimaryRecoveryDecision{
		Disposition: domainexecution.RecoveryDispositionNeedsYou, Code: code,
	}, Progressed: true}, nil
}

func recoveryArchiveArguments(run domain.Run, effect domainexecution.Effect) (host.Arguments, error) {
	recovery := run.Execution.PrimaryRecovery
	registration, err := host.RegistrationFromVisibility(run.Execution.Scope, *recovery.OriginalVisibility)
	if err != nil {
		return host.Arguments{}, err
	}
	labels, err := host.WorkerLabels(registration)
	if err != nil {
		return host.Arguments{}, err
	}
	profile, session := recoveryProfileAndSession(recovery.OriginalSession, run.Execution.EffectiveProfilesSHA256)
	return host.Arguments{
		Scope: run.Execution.Scope, EffectKind: domainexecution.EffectAgentArchive, EffectID: effect.ID,
		WorktreeID: run.Execution.Worktree.ExternalID, WorktreePath: run.Execution.WorktreePath,
		WorkspaceID: run.Execution.HostView.ExternalID, AgentID: recovery.OriginalAgent.ExternalID,
		Title: run.Execution.TaskTitle, ParentAgentID: nil, Profile: profile, Session: session,
		Labels: labels, BindingHash: run.Execution.RepositoryBindingHash,
	}, nil
}

func (controller *Controller) observeRecoveryHostEffect(ctx context.Context, run domain.Run, effect domainexecution.Effect, arguments host.Arguments) (domainexecution.EffectObservation, error) {
	command := host.Command{
		RequestID:      stableID("request", effect.ID, "observe"),
		IdempotencyKey: stableID("idempotency", effect.ID, "observe"), ExpectedVersion: run.Version,
		AfterCursor: hostResumeCursor(run.Execution), Capability: hostCapability(effect.Kind, true), Arguments: arguments,
	}
	observed, err := controller.host.Invoke(ctx, command)
	if err != nil {
		return domainexecution.EffectObservation{}, err
	}
	return normalizedHostRecoveryObservation(command, observed)
}

func (controller *Controller) dispatchRecoveryHostEffect(ctx context.Context, run domain.Run, effect domainexecution.Effect, arguments host.Arguments) error {
	if err := controller.verifyHost(ctx); err != nil {
		return err
	}
	command := host.Command{
		RequestID:      stableID("request", effect.ID, fmt.Sprintf("attempt-%d", effect.Attempt)),
		IdempotencyKey: effect.ID, ExpectedVersion: run.Version,
		Capability: hostCapability(effect.Kind, false), Arguments: arguments,
	}
	return func() error { _, err := controller.host.Invoke(ctx, command); return err }()
}

func (controller *Controller) stepRecoveryArchive(ctx context.Context, run domain.Run) (PrimaryRecoveryResult, error) {
	recovery := run.Execution.PrimaryRecovery
	if recovery.Archive.ID == "" {
		next := run
		next.Execution.PrimaryRecovery.Archive = newEffect(run.ID+"-primary-recovery", domainexecution.EffectAgentArchive, 2)
		next.Execution.PrimaryRecovery.Phase = domainexecution.PrimaryRecoveryArchiving
		if err := controller.persistRun(ctx, run, next, "run.primary_recovery_archive_intent"); err != nil {
			return PrimaryRecoveryResult{Run: run}, err
		}
		next.Version = run.Version + 1
		return PrimaryRecoveryResult{Run: next, Progressed: true}, nil
	}
	effect := recovery.Archive
	arguments, err := recoveryArchiveArguments(run, effect)
	if err != nil {
		return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoveryResourceContradictory)
	}
	if effect.Observation == nil {
		observation, observeErr := controller.observeRecoveryHostEffect(ctx, run, effect, arguments)
		if observeErr != nil {
			return PrimaryRecoveryResult{Run: run}, observeErr
		}
		next := run
		next.Execution.PrimaryRecovery.Archive.Observation = &observation
		if err := controller.persistRun(ctx, run, next, "run.primary_recovery_archive_observed"); err != nil {
			return PrimaryRecoveryResult{Run: run}, err
		}
		next.Version = run.Version + 1
		return PrimaryRecoveryResult{Run: next, Progressed: true}, nil
	}
	switch effect.Observation.Status {
	case domainexecution.ObservationDesired:
		next := run
		archive := &next.Execution.PrimaryRecovery.Archive
		archive.Phase = domainexecution.EffectComplete
		archive.ExternalID = effect.Observation.ExternalID
		archive.ObservedFactHash = effect.Observation.FactHash
		archive.Observation = nil
		refreshRecoveryObservation(&next, 2, domainexecution.PrimaryRecoveryObserved)
		if err := controller.persistRun(ctx, run, next, "run.primary_recovery_archive_completed"); err != nil {
			return PrimaryRecoveryResult{Run: run}, err
		}
		next.Version = run.Version + 1
		return PrimaryRecoveryResult{Run: next, Progressed: true}, nil
	case domainexecution.ObservationOwnedPresent:
		if effect.Attempt >= effect.AttemptLimit {
			return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoveryTerminationUnproven)
		}
		next := run
		archive := &next.Execution.PrimaryRecovery.Archive
		archive.Phase = domainexecution.EffectDispatching
		archive.Attempt++
		archive.Observation = nil
		won, persistErr := controller.persistRunTransition(ctx, run, next, "run.primary_recovery_archive_dispatching")
		if persistErr != nil {
			return PrimaryRecoveryResult{}, persistErr
		}
		if !won {
			current, readErr := controller.store.Run(ctx, run.ID)
			return PrimaryRecoveryResult{Run: current}, readErr
		}
		next.Version = run.Version + 1
		arguments, err = recoveryArchiveArguments(next, next.Execution.PrimaryRecovery.Archive)
		if err != nil {
			return PrimaryRecoveryResult{Run: next}, err
		}
		if err := controller.dispatchRecoveryHostEffect(ctx, next, next.Execution.PrimaryRecovery.Archive, arguments); err != nil {
			return PrimaryRecoveryResult{Run: next, Progressed: true}, &EffectHandoffError{Kind: domainexecution.EffectAgentArchive, err: err}
		}
		return PrimaryRecoveryResult{Run: next, Progressed: true}, nil
	default:
		return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoveryTerminationUnproven)
	}
}

func helperReplacementSafe(state domainexecution.State) bool {
	for _, helper := range state.Helpers {
		if helper.Phase != domainexecution.HelperTerminal {
			return false
		}
	}
	return true
}

func consumeReplacementBudget(run domain.Run, effectID string, nowMillis int64) (runtimebudget.Ledger, runtimebudget.Decision, error) {
	reservationID := stableID("budget-reservation", effectID, "replacement")
	ledger, decision, err := runtimebudget.Reserve(run.Execution.Budget, runtimebudget.ReserveRequest{
		ID: reservationID, EffectID: effectID, Activity: runtimebudget.ActivityReplacement,
		LeaseEpoch: run.Execution.LeaseBinding.Epoch, PolicyRevision: run.Execution.Budget.Policy.Revision,
		Demand: runtimebudget.Demand{WallTimeMilliseconds: 1},
	}, nowMillis)
	if err != nil || decision.Disposition != runtimebudget.DispositionAllow {
		return ledger, decision, err
	}
	observation := runtimebudget.ActivityObservation{
		ID: stableID("budget-activity", effectID, "replacement"), ReservationID: reservationID,
		EffectID: effectID, Activity: runtimebudget.ActivityReplacement,
		LeaseEpoch: run.Execution.LeaseBinding.Epoch, PolicyRevision: run.Execution.Budget.Policy.Revision,
		ObservedAtMillis: nowMillis,
	}
	observation.FactHash = runtimebudget.ActivityObservationHash(observation)
	return runtimebudget.ApplyActivityObservation(ledger, observation)
}

func (controller *Controller) consumeReplacementAuthority(ctx context.Context, run domain.Run, decision domainexecution.PrimaryRecoveryDecision, nowMillis int64) (PrimaryRecoveryResult, error) {
	if !helperReplacementSafe(run.Execution) {
		return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoveryHelperAmbiguous)
	}
	if run.Execution.PrimaryRecovery.Authority != nil {
		return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoveryReplacementExhausted)
	}
	fallbackHash, err := fallbackDecisionHash(run.Execution)
	if err != nil {
		return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoveryFactsInvalid)
	}
	replacementEffectID := stableID("effect", run.ID, "replacement-primary-1",
		run.Execution.PrimaryRecovery.OriginalAgent.ExternalID,
		run.Execution.PrimaryRecovery.Observation.FactHash,
	)
	ledger, budgetDecision, err := consumeReplacementBudget(run, replacementEffectID, nowMillis)
	if err != nil {
		return PrimaryRecoveryResult{Run: run}, err
	}
	if budgetDecision.Disposition != runtimebudget.DispositionAllow {
		next, persistErr := controller.persistBudgetResult(ctx, run, ledger, budgetDecision, "run.primary_recovery_budget_blocked")
		return PrimaryRecoveryResult{Run: next, Progressed: persistErr == nil}, persistErr
	}
	session, err := domainexecution.NewPrimarySession(
		run.Execution.Scope, replacementEffectID, *run.Execution.EffectiveProfiles,
		run.Execution.PrimaryRecovery.OriginalSession.MCPServer,
	)
	if err != nil {
		return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoveryFactsInvalid)
	}
	authority := domainexecution.ReplacementAuthority{
		AuthorizedRunVersion: run.Version, ConsumedRunVersion: run.Version + 1,
		LeaseEpoch:             run.Execution.LeaseBinding.Epoch,
		OldAgentID:             run.Execution.PrimaryRecovery.OriginalAgent.ExternalID,
		OldWorkspaceID:         run.Execution.HostView.ExternalID,
		FailureClass:           decision.FailureClass,
		FailureObservationID:   run.Execution.PrimaryRecovery.Observation.ID,
		FailureObservationHash: run.Execution.PrimaryRecovery.Observation.FactHash,
		RepositoryBindingHash:  run.Execution.RepositoryBindingHash,
		ProfileSHA256:          run.Execution.EffectiveProfilesSHA256,
		FallbackDecisionSHA256: fallbackHash, ReplacementEffectID: replacementEffectID, Consumed: true,
	}
	authority.ID = domainexecution.ReplacementAuthorityID(authority)
	next := run
	next.Execution.Budget = ledger
	next.Execution.PrimaryRecovery.Authority = &authority
	next.Execution.PrimaryRecovery.ReplacementAgent = domainexecution.Effect{
		ID: replacementEffectID, Kind: domainexecution.EffectAgentCreate,
		Phase: domainexecution.EffectIntentRecorded, AttemptLimit: 2,
	}
	next.Execution.PrimaryRecovery.ReplacementSession = session
	next.Execution.PrimaryRecovery.PoisonedSession = true
	next.Execution.PrimaryRecovery.Phase = domainexecution.PrimaryRecoveryAuthorityConsumed
	next.Execution.OperationalObservationConsumed = true
	won, err := controller.persistRunTransition(ctx, run, next, "run.primary_recovery_authority_consumed")
	if err != nil {
		return PrimaryRecoveryResult{}, err
	}
	if !won {
		current, readErr := controller.store.Run(ctx, run.ID)
		return PrimaryRecoveryResult{Run: current}, readErr
	}
	next.Version = run.Version + 1
	return PrimaryRecoveryResult{Run: next, Decision: decision, Progressed: true}, nil
}

func (controller *Controller) commitReplacementVisibility(ctx context.Context, run domain.Run, nowMillis int64) (PrimaryRecoveryResult, error) {
	recovery := run.Execution.PrimaryRecovery
	instant := time.UnixMilli(nowMillis).UTC().Format(time.RFC3339Nano)
	visibility := domainexecution.WorkerVisibility{
		RootWorkspaceID: run.Execution.RootWorkspaceID, ExecutionWorkspaceID: run.Execution.HostView.ExternalID,
		Role: string(host.WorkerRoleTaskAgent), Phase: "recovering", BaseSHA: run.BaseSHA,
		EffectID: recovery.ReplacementAgent.ID, ProfileSHA256: run.Execution.EffectiveProfilesSHA256,
		SessionSHA256: recovery.ReplacementSession.ReservationSHA256,
		RegisteredAt:  instant, StartedAt: instant, Digest: "pending",
	}
	registration, err := host.RegistrationFromVisibility(run.Execution.Scope, visibility)
	if err != nil {
		return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoveryVisibilityInvalid)
	}
	digest, err := host.RegistrationDigest(registration)
	if err != nil {
		return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoveryVisibilityInvalid)
	}
	visibility.Digest = digest
	next := run
	next.Execution.PrimaryRecovery.ReplacementVisibility = &visibility
	next.Execution.PrimaryRecovery.Phase = domainexecution.PrimaryRecoveryCreatingReplacement
	if err := controller.persistRun(ctx, run, next, "run.primary_recovery_visibility_registered"); err != nil {
		return PrimaryRecoveryResult{Run: run}, err
	}
	next.Version = run.Version + 1
	return PrimaryRecoveryResult{Run: next, Progressed: true}, nil
}

func replacementArguments(run domain.Run, effect domainexecution.Effect) (host.Arguments, error) {
	recovery := run.Execution.PrimaryRecovery
	if recovery.ReplacementVisibility == nil {
		return host.Arguments{}, errors.New("replacement visibility is absent")
	}
	registration, err := host.RegistrationFromVisibility(run.Execution.Scope, *recovery.ReplacementVisibility)
	if err != nil {
		return host.Arguments{}, err
	}
	labels, err := host.WorkerLabels(registration)
	if err != nil {
		return host.Arguments{}, err
	}
	profile, session := recoveryProfileAndSession(recovery.ReplacementSession, run.Execution.EffectiveProfilesSHA256)
	operationalID := ""
	if run.Execution.OperationalObservation != nil {
		operationalID = run.Execution.OperationalObservation.ID
	}
	arguments := host.Arguments{
		Scope: run.Execution.Scope, EffectKind: effect.Kind, EffectID: effect.ID,
		WorktreeID: run.Execution.Worktree.ExternalID, WorktreePath: run.Execution.WorktreePath,
		WorkspaceID: run.Execution.HostView.ExternalID, AgentID: recovery.ReplacementAgent.ExternalID,
		Title: run.Execution.TaskTitle, ParentAgentID: nil,
		LifecycleDigest: run.Execution.LifecycleDigest, IsolationDigest: run.Execution.IsolationDigest,
		PreparationReady: run.Execution.PreparationReady, PreparationBarrierHash: run.Execution.PreparationBarrierHash,
		ClientMessageID: stableID("message", effect.ID), BoundaryID: run.Execution.Boundary.ExternalID,
		OperationalObservationID: operationalID, Profile: profile, Session: session,
		BindingHash: run.Execution.RepositoryBindingHash,
	}
	if effect.Kind == domainexecution.EffectAgentCreate {
		arguments.InitialPrompt = host.ZeroWorkBootstrapPrompt
		arguments.AgentID = ""
		arguments.Labels = labels
	}
	if effect.Kind == domainexecution.EffectAgentPrompt {
		arguments.InitialPrompt = replacementPrompt(run)
		arguments.NotifyOnFinish = true
		arguments.SessionBindingSHA256 = recovery.ReplacementSession.BindingSHA256
		arguments.Labels = nil
	}
	return arguments, nil
}

func replacementPrompt(run domain.Run) string {
	recovery := run.Execution.PrimaryRecovery
	header := strings.Join([]string{
		"Director primary recovery continuation.",
		"Use durable Task and Run facts only; no conversation history is available.",
		"Task=" + run.Execution.Scope.TaskID,
		"Run=" + run.ID,
		"Base=" + run.BaseSHA,
		"RepositoryBinding=" + run.Execution.RepositoryBindingHash,
		"Profile=" + run.Execution.EffectiveProfilesSHA256,
		"ReplacementAuthority=" + recovery.Authority.ID,
	}, "\n")
	return header + "\n\n" + run.Execution.InitialPrompt
}

func (controller *Controller) reserveRecoveryTurn(ctx context.Context, run domain.Run, effect domainexecution.Effect, activity runtimebudget.Activity, nowMillis int64) (PrimaryRecoveryResult, bool, error) {
	reservationID := stableID("budget-reservation", effect.ID, fmt.Sprintf("attempt-%d", effect.Attempt+1))
	ledger, decision, err := runtimebudget.Reserve(run.Execution.Budget, runtimebudget.ReserveRequest{
		ID: reservationID, EffectID: effect.ID, Activity: activity,
		LeaseEpoch: run.Execution.LeaseBinding.Epoch, PolicyRevision: run.Execution.Budget.Policy.Revision,
		Demand: run.Execution.TurnBudgetDemand,
	}, nowMillis)
	if err != nil {
		return PrimaryRecoveryResult{Run: run}, true, err
	}
	if decision.Disposition != runtimebudget.DispositionAllow {
		next, persistErr := controller.persistBudgetResult(ctx, run, ledger, decision, "run.primary_recovery_turn_budget_blocked")
		return PrimaryRecoveryResult{Run: next, Progressed: persistErr == nil}, true, persistErr
	}
	if ledgerEqual(ledger, run.Execution.Budget) {
		return PrimaryRecoveryResult{Run: run}, false, nil
	}
	next := run
	next.Execution.Budget = ledger
	if err := controller.persistRun(ctx, run, next, "run.primary_recovery_turn_budget_reserved"); err != nil {
		return PrimaryRecoveryResult{Run: run}, true, err
	}
	next.Version = run.Version + 1
	return PrimaryRecoveryResult{Run: next, Progressed: true}, true, nil
}

func applyRecoveryProviderUsage(run *domain.Run, effect domainexecution.Effect, agentID string, nowMillis int64) runtimebudget.Decision {
	_, ok := effectReservation(run.Execution.Budget, effect)
	if !ok || effect.Observation == nil || agentID == "" {
		return runtimebudget.Decision{Disposition: runtimebudget.DispositionFailClosed, Reason: runtimebudget.ReasonProviderUsageAmbiguous}
	}
	usage := runtimebudget.ProviderUsage{State: runtimebudget.UsageUnavailable}
	if effect.Observation.Usage != nil {
		usage = *effect.Observation.Usage
	}
	activity := runtimebudget.ActivityWorkerTurn
	if effect.Kind == domainexecution.EffectAgentCreate {
		activity = runtimebudget.ActivityWorkerBootstrap
	}
	observation := runtimebudget.ProviderObservation{
		ID: stableID("provider-usage", effect.Observation.ID), EffectID: effect.ID,
		AgentID: agentID, Activity: activity, LeaseEpoch: run.Execution.LeaseBinding.Epoch,
		PolicyRevision: run.Execution.Budget.Policy.Revision, Sequence: effect.Observation.Cursor,
		ObservedAtMillis: nowMillis, ProviderUsage: usage,
	}
	observation.FactHash = runtimebudget.ProviderObservationHash(observation)
	ledger, decision, err := runtimebudget.ApplyProviderObservation(run.Execution.Budget, observation)
	if err != nil {
		return runtimebudget.Decision{Disposition: runtimebudget.DispositionFailClosed, Reason: runtimebudget.ReasonLedgerInvalid}
	}
	run.Execution.Budget = ledger
	return decision
}

func (controller *Controller) observeReplacementEffect(ctx context.Context, run domain.Run, prompt bool) (PrimaryRecoveryResult, error) {
	recovery := run.Execution.PrimaryRecovery
	effect := recovery.ReplacementAgent
	if prompt {
		effect = recovery.ReplacementPrompt
	}
	arguments, err := replacementArguments(run, effect)
	if err != nil {
		return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoveryVisibilityInvalid)
	}
	observation, err := controller.observeRecoveryHostEffect(ctx, run, effect, arguments)
	if err != nil {
		return PrimaryRecoveryResult{Run: run}, err
	}
	next := run
	if prompt {
		next.Execution.PrimaryRecovery.ReplacementPrompt.Observation = &observation
	} else {
		next.Execution.PrimaryRecovery.ReplacementAgent.Observation = &observation
	}
	if err := controller.persistRun(ctx, run, next, "run.primary_recovery_replacement_observed"); err != nil {
		return PrimaryRecoveryResult{Run: run}, err
	}
	next.Version = run.Version + 1
	return PrimaryRecoveryResult{Run: next, Progressed: true}, nil
}

func (controller *Controller) dispatchReplacementEffect(ctx context.Context, run domain.Run, prompt bool, nowMillis int64) (PrimaryRecoveryResult, error) {
	effect := run.Execution.PrimaryRecovery.ReplacementAgent
	activity := runtimebudget.ActivityWorkerBootstrap
	if prompt {
		effect = run.Execution.PrimaryRecovery.ReplacementPrompt
		activity = runtimebudget.ActivityWorkerTurn
	}
	if reserved, handled, err := controller.reserveRecoveryTurn(ctx, run, effect, activity, nowMillis); err != nil || handled {
		return reserved, err
	}
	next := run
	target := &next.Execution.PrimaryRecovery.ReplacementAgent
	if prompt {
		target = &next.Execution.PrimaryRecovery.ReplacementPrompt
	}
	target.Phase = domainexecution.EffectDispatching
	target.Attempt++
	target.Observation = nil
	next.Execution.OperationalObservationConsumed = true
	won, err := controller.persistRunTransition(ctx, run, next, "run.primary_recovery_replacement_dispatching")
	if err != nil {
		return PrimaryRecoveryResult{}, err
	}
	if !won {
		current, readErr := controller.store.Run(ctx, run.ID)
		return PrimaryRecoveryResult{Run: current}, readErr
	}
	next.Version = run.Version + 1
	effect = next.Execution.PrimaryRecovery.ReplacementAgent
	if prompt {
		effect = next.Execution.PrimaryRecovery.ReplacementPrompt
	}
	arguments, err := replacementArguments(next, effect)
	if err != nil {
		return PrimaryRecoveryResult{Run: next}, err
	}
	if prompt {
		registration, registrationErr := host.RegistrationFromVisibility(next.Execution.Scope, *next.Execution.PrimaryRecovery.ReplacementVisibility)
		if registrationErr != nil {
			return PrimaryRecoveryResult{Run: next}, registrationErr
		}
		if err := host.AdmitAgentPrompt(host.Command{Capability: host.CapabilityAgentPrompt, Arguments: arguments}, registration, effectExternalAgent(next)); err != nil {
			return PrimaryRecoveryResult{Run: next}, err
		}
	} else {
		registration, registrationErr := host.RegistrationFromVisibility(next.Execution.Scope, *next.Execution.PrimaryRecovery.ReplacementVisibility)
		if registrationErr != nil {
			return PrimaryRecoveryResult{Run: next}, registrationErr
		}
		if err := host.AdmitAgentCreate(host.Command{Capability: host.CapabilityTaskAgentCreate, Arguments: arguments}, registration); err != nil {
			return PrimaryRecoveryResult{Run: next}, err
		}
	}
	if err := controller.dispatchRecoveryHostEffect(ctx, next, effect, arguments); err != nil {
		return PrimaryRecoveryResult{Run: next, Progressed: true}, &EffectHandoffError{Kind: effect.Kind, err: err}
	}
	return PrimaryRecoveryResult{Run: next, Progressed: true}, nil
}

func effectExternalAgent(run domain.Run) string {
	return run.Execution.PrimaryRecovery.ReplacementAgent.ExternalID
}

func (controller *Controller) bindReplacement(ctx context.Context, run domain.Run, nowMillis int64) (PrimaryRecoveryResult, error) {
	recovery := run.Execution.PrimaryRecovery
	effect := recovery.ReplacementAgent
	if effect.Observation == nil || effect.Observation.Status != domainexecution.ObservationDesired ||
		recovery.ReplacementVisibility == nil || effect.Observation.CorrelationHash != recovery.ReplacementVisibility.Digest ||
		effect.Observation.ExternalID == "" {
		return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoveryVisibilityInvalid)
	}
	next := run
	budgetDecision := applyRecoveryProviderUsage(&next, effect, effect.Observation.ExternalID, nowMillis)
	target := &next.Execution.PrimaryRecovery.ReplacementAgent
	target.Phase = domainexecution.EffectComplete
	target.ExternalID = effect.Observation.ExternalID
	target.ObservedFactHash = effect.Observation.FactHash
	target.ObservedCorrelation = effect.Observation.CorrelationHash
	target.Observation = nil
	bound, err := domainexecution.BindPrimarySession(next.Execution.PrimaryRecovery.ReplacementSession, target.ExternalID)
	if err != nil {
		return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoveryVisibilityInvalid)
	}
	next.Execution.PrimaryRecovery.ReplacementSession = bound
	next.Execution.PrimaryRecovery.ReplacementVisibility.AgentID = target.ExternalID
	next.Execution.PrimaryRecovery.ReplacementVisibility.ObservedDigest = target.ObservedCorrelation
	next.Execution.PrimaryRecovery.Phase = domainexecution.PrimaryRecoveryBindingReplacement
	if budgetDecision.Disposition != runtimebudget.DispositionAllow {
		needsYou, needsErr := budgetNeedsYou(run.Execution.Scope, budgetDecision)
		if needsErr != nil {
			return PrimaryRecoveryResult{Run: run}, needsErr
		}
		next.Execution.NeedsYou = needsYou
		next.Execution.PrimaryRecovery.Phase = domainexecution.PrimaryRecoveryNeedsYou
		next.Execution.PrimaryRecovery.NeedsYouCode = needsYou.Code
	}
	if err := controller.persistRun(ctx, run, next, "run.primary_recovery_replacement_identity_persisted"); err != nil {
		return PrimaryRecoveryResult{Run: run}, err
	}
	next.Version = run.Version + 1
	return PrimaryRecoveryResult{Run: next, Progressed: true}, nil
}

func (controller *Controller) createReplacementPromptIntent(ctx context.Context, run domain.Run) (PrimaryRecoveryResult, error) {
	next := run
	next.Execution.PrimaryRecovery.ReplacementPrompt = domainexecution.Effect{
		ID:   stableID("effect", run.ID, "replacement-primary-prompt-1", run.Execution.PrimaryRecovery.Authority.ID),
		Kind: domainexecution.EffectAgentPrompt, Phase: domainexecution.EffectIntentRecorded, AttemptLimit: 1,
	}
	next.Execution.PrimaryRecovery.Phase = domainexecution.PrimaryRecoveryPromptingReplacement
	if err := controller.persistRun(ctx, run, next, "run.primary_recovery_prompt_intent"); err != nil {
		return PrimaryRecoveryResult{Run: run}, err
	}
	next.Version = run.Version + 1
	return PrimaryRecoveryResult{Run: next, Progressed: true}, nil
}

func controlledIdentityForReplacement(run domain.Run) (domainexecution.ControlledAgentIdentity, error) {
	recovery := run.Execution.PrimaryRecovery
	registration, err := host.RegistrationFromVisibility(run.Execution.Scope, *recovery.ReplacementVisibility)
	if err != nil {
		return domainexecution.ControlledAgentIdentity{}, err
	}
	labels, err := host.WorkerLabels(registration)
	if err != nil {
		return domainexecution.ControlledAgentIdentity{}, err
	}
	return domainexecution.ControlledAgentIdentity{
		ID: recovery.ReplacementAgent.ExternalID, Role: domainexecution.ControlledTaskAgent,
		WorkspaceID: run.Execution.HostView.ExternalID, Title: run.Execution.TaskTitle,
		WorktreePath: run.Execution.WorktreePath, Labels: labels,
		ProfileSHA256: run.Execution.EffectiveProfilesSHA256,
	}, nil
}

func (controller *Controller) promoteReplacement(ctx context.Context, run domain.Run, terminal bool, nowMillis int64) (PrimaryRecoveryResult, error) {
	recovery := run.Execution.PrimaryRecovery
	identity, err := controlledIdentityForReplacement(run)
	if err != nil {
		return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoveryVisibilityInvalid)
	}
	next := run
	if terminal {
		decision := applyRecoveryProviderUsage(&next, recovery.ReplacementPrompt, recovery.ReplacementAgent.ExternalID, nowMillis)
		if decision.Disposition != runtimebudget.DispositionAllow {
			needsYou, needsErr := budgetNeedsYou(run.Execution.Scope, decision)
			if needsErr != nil {
				return PrimaryRecoveryResult{Run: run}, needsErr
			}
			next.Execution.NeedsYou = needsYou
		}
		target := &next.Execution.PrimaryRecovery.ReplacementPrompt
		target.Phase = domainexecution.EffectComplete
		target.ExternalID = recovery.ReplacementPrompt.Observation.ExternalID
		target.ObservedFactHash = recovery.ReplacementPrompt.Observation.FactHash
		target.Observation = nil
	}
	next.Execution.Agent = next.Execution.PrimaryRecovery.ReplacementAgent
	next.Execution.AgentPrompt = next.Execution.PrimaryRecovery.ReplacementPrompt
	next.Execution.PrimarySession = next.Execution.PrimaryRecovery.ReplacementSession
	visibility := *next.Execution.PrimaryRecovery.ReplacementVisibility
	next.Execution.WorkerVisibility = &visibility
	if controlledAgentIndex(next.Execution.ControlledAgents, identity.ID) < 0 {
		next.Execution.ControlledAgents = append(next.Execution.ControlledAgents, identity)
	}
	next.Execution.PrimaryRecovery.Phase = domainexecution.PrimaryRecoveryComplete
	next.Execution.PrimaryRecovery.CompletedAtMillis = nowMillis
	next.Execution.OperationalObservationConsumed = true
	if err := controller.persistRun(ctx, run, next, "run.primary_recovery_completed"); err != nil {
		return PrimaryRecoveryResult{Run: run}, err
	}
	next.Version = run.Version + 1
	return PrimaryRecoveryResult{Run: next, Progressed: true}, nil
}

func (controller *Controller) stepReplacement(ctx context.Context, run domain.Run, nowMillis int64) (PrimaryRecoveryResult, error) {
	recovery := run.Execution.PrimaryRecovery
	if recovery.ReplacementVisibility == nil {
		return controller.commitReplacementVisibility(ctx, run, nowMillis)
	}
	if recovery.ReplacementAgent.Phase != domainexecution.EffectComplete {
		effect := recovery.ReplacementAgent
		if effect.Observation == nil {
			return controller.observeReplacementEffect(ctx, run, false)
		}
		switch effect.Observation.Status {
		case domainexecution.ObservationAbsent:
			if effect.Attempt > 0 || !effect.Observation.PriorDispatcherAbsent {
				return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoveryResourceContradictory)
			}
			return controller.dispatchReplacementEffect(ctx, run, false, nowMillis)
		case domainexecution.ObservationOwnedPresent:
			return PrimaryRecoveryResult{Run: run}, nil
		case domainexecution.ObservationDesired:
			return controller.bindReplacement(ctx, run, nowMillis)
		case domainexecution.ObservationErrored, domainexecution.ObservationPermission:
			return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoverySecondFailure)
		default:
			return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoveryOrphanResource)
		}
	}
	if recovery.ReplacementPrompt.ID == "" {
		return controller.createReplacementPromptIntent(ctx, run)
	}
	prompt := recovery.ReplacementPrompt
	if prompt.Observation == nil {
		return controller.observeReplacementEffect(ctx, run, true)
	}
	switch prompt.Observation.Status {
	case domainexecution.ObservationAbsent:
		if prompt.Attempt > 0 || !prompt.Observation.PriorDispatcherAbsent {
			return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoveryPromptAmbiguous)
		}
		return controller.dispatchReplacementEffect(ctx, run, true, nowMillis)
	case domainexecution.ObservationOwnedPresent:
		return controller.promoteReplacement(ctx, run, false, nowMillis)
	case domainexecution.ObservationDesired:
		return controller.promoteReplacement(ctx, run, true, nowMillis)
	case domainexecution.ObservationErrored, domainexecution.ObservationPermission:
		return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoverySecondFailure)
	default:
		return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoveryPromptAmbiguous)
	}
}

func recoveryControlBlocked(project domain.Project, run domain.Run) bool {
	return project.State != "active" || project.Control.ResumeRequired ||
		(run.Execution.Control.SchemaVersion != "" && run.Execution.Control.Phase != domainexecution.ControlComplete)
}

func (controller *Controller) recoveryFacts(project domain.Project, run domain.Run, nowMillis int64) (domainexecution.PrimaryRecoveryFacts, runtimebudget.Decision, error) {
	fallbackHash, err := fallbackDecisionHash(run.Execution)
	if err != nil {
		return domainexecution.PrimaryRecoveryFacts{}, runtimebudget.Decision{}, err
	}
	_, budgetDecision := runtimebudget.Evaluate(run.Execution.Budget, nowMillis)
	return domainexecution.PrimaryRecoveryFacts{
		RunVersion: run.Version, CurrentAgentID: run.Execution.PrimaryRecovery.OriginalAgent.ExternalID,
		CurrentWorkspaceID: run.Execution.HostView.ExternalID, CurrentCandidateID: run.CurrentCandidateID,
		RepositoryBindingHash: run.Execution.RepositoryBindingHash,
		ProfileSHA256:         run.Execution.EffectiveProfilesSHA256, FallbackDecisionSHA256: fallbackHash,
		LeaseEpoch: run.Execution.LeaseBinding.Epoch, ControlBlocksDispatch: recoveryControlBlocked(project, run),
		BudgetBlocksDispatch: budgetDecision.Disposition != runtimebudget.DispositionAllow,
		HelpersSafe:          domainexecution.ValidHelpers(run.Execution) && helperReplacementSafe(run.Execution), Policy: run.Execution.RecoveryPolicy,
		Recovery: run.Execution.PrimaryRecovery, NowMillis: nowMillis,
	}, budgetDecision, nil
}

func (controller *Controller) rebindRecoveryLease(ctx context.Context, project domain.Project, run domain.Run) (PrimaryRecoveryResult, error) {
	if !safeRecoveryTakeover(project, run) || run.Execution.PrimaryRecovery.Observation == nil ||
		!run.Execution.PrimaryRecovery.Observation.Runtime.PriorEngineAndDispatchAbsent {
		return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoveryLeaseAmbiguous)
	}
	ledger, err := runtimebudget.RebindOutstandingReservations(
		run.Execution.Budget, run.Execution.LeaseBinding.Epoch, project.Lease.Epoch,
	)
	if err != nil {
		return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoveryLeaseAmbiguous)
	}
	next := run
	next.Execution.LeaseBinding = leaseBinding(project)
	next.Execution.Budget = ledger
	refreshRecoveryObservation(&next, 3, domainexecution.PrimaryRecoveryIntentRecorded)
	if err := controller.persistRun(ctx, run, next, "run.primary_recovery_lease_rebound"); err != nil {
		return PrimaryRecoveryResult{Run: run}, err
	}
	next.Version = run.Version + 1
	return PrimaryRecoveryResult{Run: next, Progressed: true}, nil
}

// ReconcilePrimaryRecovery advances one durable frontier. It is safe after
// connector reload, daemon interruption, callback loss, coordinator restart,
// and a proven Project lease takeover.
func (controller *Controller) ReconcilePrimaryRecovery(ctx context.Context, runID string, nowMillis int64) (PrimaryRecoveryResult, error) {
	run, err := controller.store.Run(ctx, runID)
	if err != nil {
		return PrimaryRecoveryResult{}, err
	}
	if run.Execution.PrimaryRecovery.SchemaVersion == "" {
		return PrimaryRecoveryResult{}, errors.New("primary recovery was not requested")
	}
	if run.Execution.PrimaryRecovery.Phase == domainexecution.PrimaryRecoveryComplete ||
		run.Execution.PrimaryRecovery.Phase == domainexecution.PrimaryRecoveryNeedsYou {
		return PrimaryRecoveryResult{Run: run}, nil
	}
	project, err := controller.store.Project(ctx, run.Execution.Scope.ProjectID)
	if err != nil {
		return PrimaryRecoveryResult{}, err
	}
	if !currentLease(project, run.Execution.LeaseBinding, nowMillis) {
		if run.Execution.PrimaryRecovery.Observation == nil {
			return controller.observePrimaryRecovery(ctx, run, nowMillis)
		}
		return controller.rebindRecoveryLease(ctx, project, run)
	}
	if run.Execution.Control.SchemaVersion != "" && run.Execution.Control.Phase != domainexecution.ControlComplete {
		step, stepErr := controller.stepRunControl(ctx, run, nowMillis)
		return PrimaryRecoveryResult{Run: step.Run, Progressed: step.Progressed}, stepErr
	}
	if run.Execution.NeedsYou != nil {
		return controller.parkPrimaryRecovery(ctx, run, run.Execution.NeedsYou.Code)
	}
	if recoveryControlBlocked(project, run) {
		return PrimaryRecoveryResult{Run: run}, nil
	}
	lifecycle := domainexecution.AdmitLifecycle(run.Execution.Scope, run.Execution.LifecycleSurfaces, run.Execution.LifecycleApproval)
	isolation := domainexecution.AdmitIsolation(run.Execution.Isolation)
	if lifecycle.Kind != domainexecution.AdmissionAllow || lifecycle.Digest != run.Execution.LifecycleDigest ||
		isolation.Kind != domainexecution.AdmissionAllow || isolation.Digest != run.Execution.IsolationDigest {
		return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoveryEnforcementUnavailable)
	}
	if operational := operationalResult(run, nowMillis); operational.Kind != domainexecution.AdmissionAllow {
		if run.Execution.OperationalObservation == nil || run.Execution.OperationalObservationConsumed ||
			run.Execution.OperationalObservationRunVersion != run.Version {
			if err := controller.observeOperational(ctx, run); err != nil {
				return PrimaryRecoveryResult{Run: run}, err
			}
			current, readErr := controller.store.Run(ctx, run.ID)
			return PrimaryRecoveryResult{Run: current, Progressed: readErr == nil}, readErr
		}
		return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoveryEnforcementUnavailable)
	}
	facts, budgetDecision, err := controller.recoveryFacts(project, run, nowMillis)
	if err != nil {
		return controller.parkPrimaryRecovery(ctx, run, domainexecution.NeedRecoveryFactsInvalid)
	}
	if budgetDecision.Disposition != runtimebudget.DispositionAllow {
		ledger, _ := runtimebudget.Evaluate(run.Execution.Budget, nowMillis)
		next, persistErr := controller.persistBudgetResult(ctx, run, ledger, budgetDecision, "run.primary_recovery_budget_precedence")
		return PrimaryRecoveryResult{Run: next, Progressed: persistErr == nil}, persistErr
	}
	decision := domainexecution.EvaluatePrimaryRecovery(facts)
	switch decision.Disposition {
	case domainexecution.RecoveryDispositionObserve:
		return controller.observePrimaryRecovery(ctx, run, nowMillis)
	case domainexecution.RecoveryDispositionWaitExternal:
		return PrimaryRecoveryResult{Run: run, Decision: decision}, nil
	case domainexecution.RecoveryDispositionAdoptExisting:
		next := run
		next.Execution.PrimaryRecovery.Phase = domainexecution.PrimaryRecoveryComplete
		next.Execution.PrimaryRecovery.CompletedAtMillis = nowMillis
		if err := controller.persistRun(ctx, run, next, "run.primary_recovery_existing_adopted"); err != nil {
			return PrimaryRecoveryResult{Run: run}, err
		}
		next.Version = run.Version + 1
		return PrimaryRecoveryResult{Run: next, Decision: decision, Progressed: true}, nil
	case domainexecution.RecoveryDispositionArchiveOriginal:
		return controller.stepRecoveryArchive(ctx, run)
	case domainexecution.RecoveryDispositionAuthorizeReplace:
		return controller.consumeReplacementAuthority(ctx, run, decision, nowMillis)
	case domainexecution.RecoveryDispositionContinueReplace:
		return controller.stepReplacement(ctx, run, nowMillis)
	case domainexecution.RecoveryDispositionNeedsYou:
		return controller.parkPrimaryRecovery(ctx, run, decision.Code)
	default:
		return PrimaryRecoveryResult{Run: run}, errors.New("primary recovery decision is unknown")
	}
}
