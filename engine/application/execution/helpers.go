// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/agentprofile"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	domainexecution "github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/domain/runtimebudget"
	"github.com/mcuadros/director-engine/ports/host"
	runtimeport "github.com/mcuadros/director-engine/ports/runtime"
)

// RequestHelperCommand is the Task Agent's voluntary, scope-fixed request.
// It contains no workspace, repository, path, provider, credential, capacity,
// or lifecycle selector.
type RequestHelperCommand struct {
	RequestID     string
	RunID         string
	ParentAgentID string
	Mode          domainexecution.HelperMode
	Purpose       string
}

type HelperStepResult struct {
	Run        domain.Run
	Helper     domainexecution.Helper
	Progressed bool
}

func helperByID(state *domainexecution.State, id string) (*domainexecution.Helper, error) {
	index := domainexecution.HelperIndex(state.Helpers, id)
	if index < 0 {
		return nil, errors.New("helper is not owned by the Run")
	}
	return &state.Helpers[index], nil
}

func helperEffect(helper *domainexecution.Helper, kind domainexecution.EffectKind) *domainexecution.Effect {
	switch kind {
	case domainexecution.EffectHelperCheckoutCreate:
		return &helper.Checkout
	case domainexecution.EffectHelperBoundary:
		return &helper.Boundary
	case domainexecution.EffectHelperAgentObserve:
		return &helper.AgentObservation
	case domainexecution.EffectHelperCommitHandoff:
		return &helper.HandoffEffect
	case domainexecution.EffectHelperAgentArchive:
		return &helper.Archive
	case domainexecution.EffectHelperCheckoutRemove:
		return &helper.CheckoutRemove
	default:
		return nil
	}
}

func helperRuntimeRequest(run domain.Run, helper domainexecution.Helper, effect domainexecution.Effect) runtimeport.HelperRequest {
	return runtimeport.HelperRequest{
		Scope: run.Execution.Scope, Helper: helper, Effect: effect,
		LeaseBinding: run.Execution.LeaseBinding, Repository: run.Execution.RepositoryBinding,
		BindingHash: run.Execution.RepositoryBindingHash, PrimaryWorktreePath: run.Execution.WorktreePath,
		LifecycleSurfaces: run.Execution.LifecycleSurfaces, LifecycleApproval: run.Execution.LifecycleApproval,
		LifecycleDigest: run.Execution.LifecycleDigest, Isolation: run.Execution.Isolation,
		OperationalPolicy:         run.Execution.OperationalPolicy,
		ControlRecoveryArtifactID: run.Execution.Control.Recovery.ArtifactID,
		ControlCleanupAuthorized:  run.Execution.Control.Recovery.CleanupAuthorized,
	}
}

func helperRegistration(run domain.Run, helper domainexecution.Helper) (host.HelperRegistration, error) {
	if helper.Admission == nil {
		return host.HelperRegistration{}, errors.New("helper admission is absent")
	}
	return host.HelperRegistration{
		RootWorkspaceID:      run.Execution.RootWorkspaceID,
		ExecutionWorkspaceID: run.Execution.HostView.ExternalID,
		Scope:                run.Execution.Scope, ParentAgentID: helper.ParentAgentID, BaseSHA: run.BaseSHA,
		EffectID: helper.AgentObservation.ID, ProfileSHA256: run.Execution.EffectiveProfilesSHA256,
		SessionSHA256: helper.Admission.SessionSHA256,
		RegisteredAt:  helper.Admission.RegisteredAt, StartedAt: helper.Admission.StartedAt,
	}, nil
}

func helperHostArguments(run domain.Run, helper domainexecution.Helper, effect domainexecution.Effect) (host.Arguments, error) {
	registration, err := helperRegistration(run, helper)
	if err != nil {
		return host.Arguments{}, err
	}
	labels, err := host.HelperLabels(registration)
	if err != nil {
		return host.Arguments{}, err
	}
	parent := helper.ParentAgentID
	return host.Arguments{
		Scope: run.Execution.Scope, EffectKind: effect.Kind, EffectID: effect.ID,
		WorktreeID: run.Execution.Worktree.ExternalID, WorktreePath: run.Execution.WorktreePath,
		WorkspaceID: run.Execution.HostView.ExternalID, AgentID: helper.NativeAgentID,
		Title: helper.Title, ParentAgentID: &parent, Labels: labels,
		InitialPrompt: helper.Admission.BootstrapPrompt, ClientMessageID: helper.Admission.BootstrapMessageID,
		LifecycleDigest: run.Execution.LifecycleDigest, IsolationDigest: run.Execution.IsolationDigest,
		BoundaryID: helper.Boundary.ExternalID, BindingHash: run.Execution.RepositoryBindingHash,
	}, nil
}

func helperResult(run domain.Run, helperID string, progressed bool) (HelperStepResult, error) {
	helper, err := helperByID(&run.Execution, helperID)
	if err != nil {
		return HelperStepResult{Run: run}, err
	}
	return HelperStepResult{Run: run, Helper: *helper, Progressed: progressed}, nil
}

// RequestHelper records one durable request. Admission and all external work
// remain later engine transitions; this method never launches a child.
func (controller *Controller) RequestHelper(ctx context.Context, command RequestHelperCommand, nowMillis int64) (HelperStepResult, error) {
	run, err := controller.store.Run(ctx, command.RunID)
	if err != nil {
		return HelperStepResult{}, err
	}
	if err := controller.currentExecutionAuthority(ctx, run, nowMillis); err != nil {
		return HelperStepResult{Run: run}, err
	}
	project, err := controller.store.Project(ctx, run.Execution.Scope.ProjectID)
	if err != nil {
		return HelperStepResult{Run: run}, err
	}
	if project.State != "active" || project.Control.ResumeRequired ||
		(run.Execution.Control.SchemaVersion != "" && run.Execution.Control.Phase != domainexecution.ControlComplete) {
		return HelperStepResult{Run: run}, errors.New("helper request is blocked by execution control")
	}
	helperID := stableID("helper", run.ID, command.RequestID)
	if index := domainexecution.HelperIndex(run.Execution.Helpers, helperID); index >= 0 {
		existing := run.Execution.Helpers[index]
		path := domainexecution.HelperWorktreePath(run.Execution.WorktreePath, helperID, command.Mode)
		if existing.RequestID == command.RequestID && existing.ParentAgentID == command.ParentAgentID &&
			existing.Mode == command.Mode && existing.Purpose == command.Purpose && existing.WorktreePath == path {
			return helperResult(run, helperID, false)
		}
		return HelperStepResult{Run: run}, errors.New("helper request identity conflicts")
	}
	if run.Execution.NeedsYou != nil || run.Execution.Terminal || run.CurrentCandidateID != "" ||
		run.Execution.AgentPrompt.Phase != domainexecution.EffectComplete || command.ParentAgentID != run.Execution.Agent.ExternalID {
		return HelperStepResult{Run: run}, errors.New("helper request is outside the active primary turn")
	}
	worker, present := run.Execution.EffectiveProfiles.Role(agentprofile.RoleWorker)
	capable := false
	if present {
		for _, capability := range worker.Selection.MCPCapabilities {
			if capability == domainconfig.MCPTaskHelperRequest {
				capable = true
				break
			}
		}
	}
	if !capable {
		return HelperStepResult{Run: run}, errors.New("helper request capability is not frozen into the Run")
	}
	path := domainexecution.HelperWorktreePath(run.Execution.WorktreePath, helperID, command.Mode)
	next := run
	next.Execution.Helpers = domainexecution.CloneHelpers(run.Execution.Helpers)
	next.Execution, err = domainexecution.AppendHelperRequest(next.Execution, helperID, command.RequestID, command.ParentAgentID, command.Mode, command.Purpose, path)
	if err != nil {
		if err.Error() == string(domainexecution.NeedHelperQuota) {
			parkErr := controller.parkRun(ctx, run, domainexecution.NeedHelperQuota)
			return HelperStepResult{Run: run, Progressed: parkErr == nil}, parkErr
		}
		return HelperStepResult{Run: run}, err
	}
	if err := controller.persistRun(ctx, run, next, "helper.requested"); err != nil {
		return HelperStepResult{Run: run}, err
	}
	return helperResult(next, helperID, true)
}

func (controller *Controller) parkHelper(ctx context.Context, run domain.Run, helperID string, code domainexecution.NeedCode) (HelperStepResult, error) {
	next := run
	next.Execution.Helpers = domainexecution.CloneHelpers(run.Execution.Helpers)
	helper, err := helperByID(&next.Execution, helperID)
	if err != nil {
		return HelperStepResult{Run: run}, err
	}
	helper.Phase = domainexecution.HelperParked
	helper.NeedsYou = &domainexecution.NeedsYou{Code: code, WakeCondition: "fresh_exact_helper_fact_or_human_decision", CleanupAuthorized: false}
	next.Execution.NeedsYou = &domainexecution.NeedsYou{Code: code, WakeCondition: helper.NeedsYou.WakeCondition, CleanupAuthorized: false}
	if err := controller.persistRun(ctx, run, next, "helper.needs_you"); err != nil {
		return HelperStepResult{Run: run}, err
	}
	return helperResult(next, helperID, true)
}

func (controller *Controller) prepareHelper(ctx context.Context, run domain.Run, helperID string, nowMillis int64) (HelperStepResult, error) {
	if controller.helperRuntime == nil {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperBoundaryInvalid)
	}
	current := run.Execution.Helpers[domainexecution.HelperIndex(run.Execution.Helpers, helperID)]
	capacity, err := controller.helperRuntime.ObserveHelperCapacity(ctx, run.Execution.Scope, run.Execution.HelperPolicy)
	if err != nil || !domainexecution.CurrentHelperCapacityObservation(capacity, run.Execution.HelperPolicy, nowMillis) {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperCapacityFactMissing)
	}
	if !domainexecution.HelperCapacityAvailable(capacity, run.Execution.HelperPolicy, nowMillis) {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperCapacity)
	}
	reservation, reserveErr := controller.helperRuntime.ReserveHelperCapacity(ctx, runtimeport.HelperCapacityReservation{
		ID: current.CapacityReservationID, Scope: run.Execution.Scope,
		Policy: run.Execution.HelperPolicy, Observation: capacity,
	})
	if reserveErr != nil {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperCapacity)
	}
	if !reservation.Applied {
		return helperResult(run, helperID, false)
	}
	operational, err := controller.runtime.ObserveOperational(ctx, run.Execution.Scope, run.Execution.OperationalPolicy)
	if err != nil || domainexecution.EvaluateOperationalLimits(run.Execution.OperationalPolicy, operational, nowMillis).Kind != domainexecution.AdmissionAllow {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedOperationalFactMissing)
	}
	if domainexecution.AdmitLifecycle(run.Execution.Scope, run.Execution.LifecycleSurfaces, run.Execution.LifecycleApproval).Kind != domainexecution.AdmissionAllow ||
		domainexecution.AdmitIsolation(run.Execution.Isolation).Kind != domainexecution.AdmissionAllow {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperBoundaryInvalid)
	}
	next := run
	next.Execution.Helpers = domainexecution.CloneHelpers(run.Execution.Helpers)
	helper, _ := helperByID(&next.Execution, helperID)
	helper.CapacityObservation = &capacity
	helper.Phase = domainexecution.HelperPreparing
	if current.Mode == domainexecution.HelperWriter {
		helper.Checkout = newEffect(run.ID+"-"+helper.ID, domainexecution.EffectHelperCheckoutCreate, 1)
	}
	helper.Boundary = newEffect(run.ID+"-"+helper.ID, domainexecution.EffectHelperBoundary, 1)
	helper.AgentObservation = newEffect(run.ID+"-"+helper.ID, domainexecution.EffectHelperAgentObserve, 1)
	next.Execution.OperationalObservation = &operational
	next.Execution.OperationalObservationRunVersion = run.Version + 1
	next.Execution.OperationalObservationConsumed = true
	if err := controller.persistRun(ctx, run, next, "helper.admission_facts_observed"); err != nil {
		return HelperStepResult{Run: run}, err
	}
	return helperResult(next, helperID, true)
}

func (controller *Controller) persistHelperEffect(ctx context.Context, run domain.Run, helperID string, kind domainexecution.EffectKind, action string) (HelperStepResult, error) {
	next := run
	next.Execution.Helpers = domainexecution.CloneHelpers(run.Execution.Helpers)
	helper, err := helperByID(&next.Execution, helperID)
	if err != nil {
		return HelperStepResult{Run: run}, err
	}
	effect := helperEffect(helper, kind)
	if effect == nil || effect.ID == "" {
		return HelperStepResult{Run: run}, errors.New("helper effect is absent")
	}
	switch action {
	case "observe":
		if controller.helperRuntime == nil {
			return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperBoundaryInvalid)
		}
		observation, observeErr := controller.helperRuntime.ObserveHelperEffect(ctx, helperRuntimeRequest(run, *helper, *effect))
		if observeErr != nil {
			return HelperStepResult{Run: run}, observeErr
		}
		if !domainexecution.ValidEffectObservation(observation) || observation.EffectID != effect.ID ||
			observation.BindingHash != run.Execution.RepositoryBindingHash {
			return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperBoundaryInvalid)
		}
		effect.Observation = &observation
	case "dispatch":
		expectedStatus := domainexecution.ObservationAbsent
		if kind == domainexecution.EffectHelperCheckoutRemove {
			expectedStatus = domainexecution.ObservationOwnedPresent
		}
		if effect.Observation == nil || effect.Observation.Status != expectedStatus ||
			!effect.Observation.PriorDispatcherAbsent || effect.Attempt != 0 {
			return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperBoundaryInvalid)
		}
		effect.Phase = domainexecution.EffectDispatching
		effect.Attempt = 1
		effect.Observation = nil
		won, persistErr := controller.persistRunTransition(ctx, run, next, "helper.effect_dispatching")
		if persistErr != nil {
			return HelperStepResult{Run: run}, persistErr
		}
		if !won {
			current, readErr := controller.store.Run(ctx, run.ID)
			if readErr != nil {
				return HelperStepResult{}, readErr
			}
			return helperResult(current, helperID, false)
		}
		next.Version = run.Version + 1
		helper, _ = helperByID(&next.Execution, helperID)
		effect = helperEffect(helper, kind)
		dispatchErr := controller.helperRuntime.DispatchHelperEffect(ctx, helperRuntimeRequest(next, *helper, *effect))
		if dispatchErr != nil {
			return helperResult(next, helperID, true)
		}
		return helperResult(next, helperID, true)
	case "adopt":
		if effect.Observation == nil || effect.Observation.Status != domainexecution.ObservationDesired {
			return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperBoundaryInvalid)
		}
		effect.Phase = domainexecution.EffectComplete
		effect.ExternalID = effect.Observation.ExternalID
		effect.ObservedFactHash = effect.Observation.FactHash
		effect.Observation = nil
	default:
		return HelperStepResult{Run: run}, errors.New("unknown helper effect transition")
	}
	if err := controller.persistRun(ctx, run, next, "helper.effect_"+action); err != nil {
		return HelperStepResult{Run: run}, err
	}
	return helperResult(next, helperID, true)
}

func (controller *Controller) stepHelperRuntimeEffect(ctx context.Context, run domain.Run, helperID string, kind domainexecution.EffectKind, unknownCode domainexecution.NeedCode) (HelperStepResult, error) {
	helper := run.Execution.Helpers[domainexecution.HelperIndex(run.Execution.Helpers, helperID)]
	effect := helperEffect(&helper, kind)
	if effect.Phase == domainexecution.EffectIntentRecorded {
		if effect.Observation == nil {
			return controller.persistHelperEffect(ctx, run, helperID, kind, "observe")
		}
		switch effect.Observation.Status {
		case domainexecution.ObservationAbsent:
			return controller.persistHelperEffect(ctx, run, helperID, kind, "dispatch")
		case domainexecution.ObservationOwnedPresent:
			if kind == domainexecution.EffectHelperCheckoutRemove {
				return controller.persistHelperEffect(ctx, run, helperID, kind, "dispatch")
			}
			return controller.parkHelper(ctx, run, helperID, unknownCode)
		case domainexecution.ObservationDesired:
			return controller.persistHelperEffect(ctx, run, helperID, kind, "adopt")
		default:
			return controller.parkHelper(ctx, run, helperID, unknownCode)
		}
	}
	if effect.Phase == domainexecution.EffectDispatching {
		if effect.Observation == nil {
			return controller.persistHelperEffect(ctx, run, helperID, kind, "observe")
		}
		if effect.Observation.Status == domainexecution.ObservationDesired {
			return controller.persistHelperEffect(ctx, run, helperID, kind, "adopt")
		}
		return controller.parkHelper(ctx, run, helperID, unknownCode)
	}
	return helperResult(run, helperID, false)
}

func (controller *Controller) issueHelperAdmission(ctx context.Context, run domain.Run, helperID string, nowMillis int64) (HelperStepResult, error) {
	if controller.helperRuntime == nil {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperBoundaryInvalid)
	}
	current := run.Execution.Helpers[domainexecution.HelperIndex(run.Execution.Helpers, helperID)]
	boundary, err := controller.helperRuntime.ObserveHelperBoundary(ctx, helperRuntimeRequest(run, current, current.Boundary))
	if err != nil || !domainexecution.CurrentHelperBoundary(boundary, current, run.Execution, nowMillis) {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperBoundaryInvalid)
	}
	instant := time.UnixMilli(nowMillis).UTC().Format(time.RFC3339Nano)
	sessionSHA := hashText(run.Execution.PrimarySession.ReservationSHA256 + "\x1f" + helperID + "\x1f" + string(current.Mode))
	admission := domainexecution.HelperAdmission{
		ID: stableID("helper-admission", run.ID, helperID), HelperID: helperID,
		ParentAgentID: current.ParentAgentID, ExecutionWorkspaceID: run.Execution.HostView.ExternalID,
		Mode: current.Mode, ProfileSHA256: run.Execution.EffectiveProfilesSHA256,
		SessionSHA256: sessionSHA, RegisteredAt: instant, StartedAt: instant, IssuedAtMillis: nowMillis,
		Role: "helper", Provider: string(run.Execution.PrimarySession.Provider), Model: run.Execution.PrimarySession.Model,
		Effort: run.Execution.PrimarySession.Effort, ProviderMode: run.Execution.PrimarySession.Mode,
		PermissionMode:     run.Execution.PrimarySession.PermissionMode,
		MCPContractVersion: run.Execution.PrimarySession.MCPContractVersion,
		MCPContractSHA256:  run.Execution.PrimarySession.MCPContractSHA256,
		MCPTools:           []string{"director_helper_contribution_submit", "director_task_read"},
		MCPServer:          run.Execution.PrimarySession.MCPServer,
		BootstrapPrompt:    domainexecution.HelperBootstrapPrompt,
		BootstrapMessageID: stableID("message", current.AgentObservation.ID, "bootstrap"),
	}
	if current.Mode == domainexecution.HelperReadOnly {
		admission.PermissionMode = "read-only"
	}
	temporary := current
	temporary.Admission = &admission
	registration, err := helperRegistration(run, temporary)
	if err != nil {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperBoundaryInvalid)
	}
	admission.LabelDigest, err = host.HelperRegistrationDigest(registration)
	if err != nil {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperBoundaryInvalid)
	}
	next := run
	next.Execution.Helpers = domainexecution.CloneHelpers(run.Execution.Helpers)
	helper, _ := helperByID(&next.Execution, helperID)
	helper.BoundaryObservation = &boundary
	helper.Admission = &admission
	helper.Phase = domainexecution.HelperAdmissionReady
	if err := controller.persistRun(ctx, run, next, "helper.admission_issued"); err != nil {
		return HelperStepResult{Run: run}, err
	}
	return helperResult(next, helperID, true)
}

// ConsumeHelperAdmission durably consumes the one-use grant immediately
// before the Task Agent invokes its parent-bound helper mechanism.
func (controller *Controller) ConsumeHelperAdmission(ctx context.Context, runID, helperID, invocationRequestID string, nowMillis int64) (HelperStepResult, error) {
	run, err := controller.store.Run(ctx, runID)
	if err != nil {
		return HelperStepResult{}, err
	}
	if err := controller.currentExecutionAuthority(ctx, run, nowMillis); err != nil {
		return HelperStepResult{Run: run}, err
	}
	if run.Execution.NeedsYou != nil {
		return HelperStepResult{Run: run}, errors.New("runtime budget or Run state prevents helper dispatch")
	}
	next := run
	next.Execution.Helpers = domainexecution.CloneHelpers(run.Execution.Helpers)
	helper, err := helperByID(&next.Execution, helperID)
	if err != nil {
		return HelperStepResult{Run: run}, err
	}
	if helper.Phase == domainexecution.HelperInvocationConsumed && helper.Admission != nil &&
		helper.Admission.InvocationRequestID == invocationRequestID && helper.BudgetReservationID != "" {
		return helperResult(run, helperID, false)
	}
	reservationID := stableID("budget-reservation", helper.ID, helper.BudgetEffectID)
	ledger, decision, err := runtimebudget.Reserve(run.Execution.Budget, runtimebudget.ReserveRequest{
		ID: reservationID, EffectID: helper.BudgetEffectID, Activity: runtimebudget.ActivityHelperTurn,
		LeaseEpoch: run.Execution.LeaseBinding.Epoch, PolicyRevision: run.Execution.Budget.Policy.Revision,
		Demand: run.Execution.TurnBudgetDemand,
	}, nowMillis)
	if err != nil {
		return HelperStepResult{Run: run}, err
	}
	if decision.Disposition != runtimebudget.DispositionAllow {
		needsYou, needsErr := budgetNeedsYou(run.Execution.Scope, decision)
		if needsErr != nil {
			return HelperStepResult{Run: run}, needsErr
		}
		next.Execution.Budget = ledger
		next.Execution.NeedsYou = needsYou
		if err := controller.persistRun(ctx, run, next, budgetTransition(decision, "helper.budget_paused")); err != nil {
			return HelperStepResult{Run: run}, err
		}
		return helperResult(next, helperID, true)
	}
	consumed, err := domainexecution.ConsumeHelperAdmission(*helper, invocationRequestID, nowMillis)
	if err != nil {
		return HelperStepResult{Run: run}, err
	}
	*helper = consumed
	helper.BudgetReservationID = reservationID
	next.Execution.Budget = ledger
	if err := controller.persistRun(ctx, run, next, "helper.invocation_consumed"); err != nil {
		return HelperStepResult{Run: run}, err
	}
	return helperResult(next, helperID, true)
}

// RecordHelperProviderUsage is the evidence-only completion path for one
// admitted helper turn. It remains callable while a Run is budget-paused so a
// missing/ambiguous first observation can be replaced by a later exact fact;
// it never dispatches, retries, or cleans a helper.
func (controller *Controller) RecordHelperProviderUsage(
	ctx context.Context, runID, helperID string, expectedRunVersion, leaseEpoch uint64,
	observation runtimebudget.ProviderObservation,
) (HelperStepResult, error) {
	run, err := controller.store.Run(ctx, runID)
	if err != nil {
		return HelperStepResult{}, err
	}
	if run.Version != expectedRunVersion || run.Execution.LeaseBinding.Epoch != leaseEpoch || observation.LeaseEpoch != leaseEpoch {
		return HelperStepResult{Run: run}, ErrRuntimeBudgetLeaseFenced
	}
	if err := controller.currentExecutionAuthority(ctx, run, observation.ObservedAtMillis); err != nil {
		return HelperStepResult{Run: run}, err
	}
	index := domainexecution.HelperIndex(run.Execution.Helpers, helperID)
	if index < 0 {
		return HelperStepResult{Run: run}, errors.New("helper usage is not owned by the Run")
	}
	helper := run.Execution.Helpers[index]
	if helper.Admission == nil || helper.Admission.ConsumedAtMillis == 0 || helper.BudgetReservationID == "" ||
		helper.NativeAgentID == "" || observation.EffectID != helper.BudgetEffectID ||
		observation.AgentID != helper.NativeAgentID || observation.Activity != runtimebudget.ActivityHelperTurn ||
		observation.PolicyRevision != run.Execution.Budget.Policy.Revision {
		return HelperStepResult{Run: run}, errors.New("helper usage observation is misbound")
	}
	ledger, decision, err := runtimebudget.ApplyProviderObservation(run.Execution.Budget, observation)
	if err != nil {
		return HelperStepResult{Run: run}, err
	}
	next := run
	next.Execution.Helpers = domainexecution.CloneHelpers(run.Execution.Helpers)
	next.Execution.Budget = ledger
	nextHelper, _ := helperByID(&next.Execution, helperID)
	for _, reservation := range ledger.Reservations {
		if reservation.ID == helper.BudgetReservationID && reservation.EffectID == helper.BudgetEffectID &&
			reservation.Released && reservation.EvidenceID == observation.ID {
			nextHelper.BudgetEvidenceID = observation.ID
			break
		}
	}
	transition := "helper.provider_usage_recorded"
	if decision.Disposition != runtimebudget.DispositionAllow {
		needsYou, needsErr := budgetNeedsYou(run.Execution.Scope, decision)
		if needsErr != nil {
			return HelperStepResult{Run: run}, needsErr
		}
		next.Execution.NeedsYou = needsYou
		transition = budgetTransition(decision, "helper.provider_usage_recorded")
	} else if nextHelper.BudgetEvidenceID != "" && next.Execution.NeedsYou != nil &&
		(next.Execution.NeedsYou.Code == domainexecution.NeedCode(runtimebudget.ReasonProviderUsageUnavailable) ||
			next.Execution.NeedsYou.Code == domainexecution.NeedCode(runtimebudget.ReasonProviderUsageAmbiguous)) {
		next.Execution.NeedsYou = nil
	}
	if ledgerEqual(run.Execution.Budget, next.Execution.Budget) && nextHelper.BudgetEvidenceID == helper.BudgetEvidenceID &&
		((run.Execution.NeedsYou == nil && next.Execution.NeedsYou == nil) ||
			(run.Execution.NeedsYou != nil && next.Execution.NeedsYou != nil && *run.Execution.NeedsYou == *next.Execution.NeedsYou)) {
		return helperResult(run, helperID, false)
	}
	if err := controller.persistRun(ctx, run, next, transition); err != nil {
		return HelperStepResult{Run: run}, err
	}
	next.Version = run.Version + 1
	return helperResult(next, helperID, true)
}

func helperBudgetReleased(run domain.Run, helper domainexecution.Helper) bool {
	for _, reservation := range run.Execution.Budget.Reservations {
		if reservation.ID == helper.BudgetReservationID && reservation.EffectID == helper.BudgetEffectID {
			return reservation.Released && reservation.EvidenceID == helper.BudgetEvidenceID && helper.BudgetEvidenceID != ""
		}
	}
	return false
}

func (controller *Controller) observeHelperAgent(ctx context.Context, run domain.Run, helperID string, nowMillis int64) (HelperStepResult, error) {
	helper := run.Execution.Helpers[domainexecution.HelperIndex(run.Execution.Helpers, helperID)]
	if controller.helperRuntime == nil {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperBoundaryInvalid)
	}
	capacity, capacityErr := controller.helperRuntime.ObserveHelperCapacity(ctx, run.Execution.Scope, run.Execution.HelperPolicy)
	if capacityErr != nil || !domainexecution.CurrentHelperCapacityObservation(capacity, run.Execution.HelperPolicy, nowMillis) {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperCapacityFactMissing)
	}
	if !domainexecution.HelperCapacityWithinLimit(capacity, run.Execution.HelperPolicy, nowMillis) {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperCapacity)
	}
	operational, operationalErr := controller.runtime.ObserveOperational(ctx, run.Execution.Scope, run.Execution.OperationalPolicy)
	if operationalErr != nil {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedOperationalFactMissing)
	}
	operationalAdmission := domainexecution.EvaluateOperationalLimits(run.Execution.OperationalPolicy, operational, nowMillis)
	if operationalAdmission.Kind != domainexecution.AdmissionAllow {
		return controller.parkHelper(ctx, run, helperID, operationalAdmission.Code)
	}
	boundary, boundaryErr := controller.helperRuntime.ObserveHelperBoundary(ctx, helperRuntimeRequest(run, helper, helper.Boundary))
	if boundaryErr != nil || !domainexecution.CurrentHelperBoundary(boundary, helper, run.Execution, nowMillis) {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperBoundaryInvalid)
	}
	effect := helper.AgentObservation
	arguments, err := helperHostArguments(run, helper, effect)
	if err != nil {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperIdentityInvalid)
	}
	registration, _ := helperRegistration(run, helper)
	command := host.Command{
		RequestID:      stableID("request", effect.ID, fmt.Sprintf("observe-%d", run.Version)),
		IdempotencyKey: stableID("idempotency", effect.ID, "observe"), ExpectedVersion: run.Version,
		Capability: host.CapabilityHelperAgentObserve, AfterCursor: hostResumeCursor(run.Execution), Arguments: arguments,
	}
	if err := host.AdmitHelperObserve(command, registration, helper.NativeAgentID); err != nil {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperIdentityInvalid)
	}
	if err := controller.verifyHost(ctx); err != nil {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperIdentityInvalid)
	}
	observed, err := controller.host.Invoke(ctx, command)
	if err != nil {
		return HelperStepResult{Run: run}, err
	}
	if err := host.ValidateObservation(command, observed); err != nil {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperIdentityInvalid)
	}
	if observed.Result.CorrelationHash != helper.Admission.LabelDigest {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperIdentityInvalid)
	}
	if observed.Result.Status == domainexecution.ObservationAbsent {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperCreationUnknown)
	}
	if observed.Result.Status == domainexecution.ObservationOwnedPresent {
		return helperResult(run, helperID, false)
	}
	if observed.Result.Status != domainexecution.ObservationDesired {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperIdentityInvalid)
	}
	if helper.NativeAgentID != "" && helper.NativeAgentID != observed.Result.ExternalID {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperIdentityInvalid)
	}
	next := run
	next.Execution.Helpers = domainexecution.CloneHelpers(run.Execution.Helpers)
	nextHelper, _ := helperByID(&next.Execution, helperID)
	nextHelper.NativeAgentID = observed.Result.ExternalID
	nextHelper.CapacityObservation = &capacity
	nextHelper.BoundaryObservation = &boundary
	nextHelper.AgentObservation.Phase = domainexecution.EffectComplete
	nextHelper.AgentObservation.ExternalID = observed.Result.ExternalID
	nextHelper.AgentObservation.ObservedFactHash = observed.Result.FactHash
	nextHelper.AgentObservation.ObservedCorrelation = observed.Result.CorrelationHash
	nextHelper.Phase = domainexecution.HelperActive
	nextHelper.NeedsYou = nil
	if next.Execution.NeedsYou != nil && (next.Execution.NeedsYou.Code == domainexecution.NeedHelperCreationUnknown || next.Execution.NeedsYou.Code == domainexecution.NeedHelperIdentityInvalid) {
		next.Execution.NeedsYou = nil
	}
	if err := controller.persistRun(ctx, run, next, "helper.identity_observed"); err != nil {
		return HelperStepResult{Run: run}, err
	}
	return helperResult(next, helperID, true)
}

// RecordHelperContribution stores the helper's exact commit claim. Only the
// engine's later private-checkout observation can make it a handoff.
func (controller *Controller) RecordHelperContribution(ctx context.Context, runID, helperID, nativeAgentID, commitSHA, baseSHA string, nowMillis int64) (HelperStepResult, error) {
	run, err := controller.store.Run(ctx, runID)
	if err != nil {
		return HelperStepResult{}, err
	}
	if err := controller.currentExecutionAuthority(ctx, run, nowMillis); err != nil {
		return HelperStepResult{Run: run}, err
	}
	next := run
	next.Execution.Helpers = domainexecution.CloneHelpers(run.Execution.Helpers)
	helper, err := helperByID(&next.Execution, helperID)
	if err != nil {
		return HelperStepResult{Run: run}, err
	}
	if helper.Mode != domainexecution.HelperWriter || helper.Phase != domainexecution.HelperActive ||
		helper.NativeAgentID == "" || helper.NativeAgentID != nativeAgentID || !shaPattern.MatchString(commitSHA) || baseSHA != run.BaseSHA {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperContributionInvalid)
	}
	if helper.Contribution != nil {
		if helper.Contribution.CommitSHA == commitSHA && helper.Contribution.BaseSHA == baseSHA {
			return helperResult(run, helperID, false)
		}
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperContributionInvalid)
	}
	helper.Contribution = &domainexecution.HelperContribution{CommitSHA: commitSHA, BaseSHA: baseSHA}
	helper.Phase = domainexecution.HelperContributionReady
	if err := controller.persistRun(ctx, run, next, "helper.contribution_claimed"); err != nil {
		return HelperStepResult{Run: run}, err
	}
	return helperResult(next, helperID, true)
}

func (controller *Controller) stepHelperContribution(ctx context.Context, run domain.Run, helperID string, nowMillis int64) (HelperStepResult, error) {
	if controller.helperRuntime == nil {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperContributionInvalid)
	}
	helper := run.Execution.Helpers[domainexecution.HelperIndex(run.Execution.Helpers, helperID)]
	if helper.ContributionObservation == nil {
		observation, err := controller.helperRuntime.ObserveHelperContribution(ctx, helperRuntimeRequest(run, helper, helper.Checkout))
		if err != nil || !domainexecution.CurrentHelperContribution(observation, helper, run.Execution, nowMillis) {
			return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperContributionInvalid)
		}
		next := run
		next.Execution.Helpers = domainexecution.CloneHelpers(run.Execution.Helpers)
		nextHelper, _ := helperByID(&next.Execution, helperID)
		nextHelper.ContributionObservation = &observation
		nextHelper.HandoffEffect = newEffect(run.ID+"-"+helper.ID, domainexecution.EffectHelperCommitHandoff, 1)
		if err := controller.persistRun(ctx, run, next, "helper.contribution_observed"); err != nil {
			return HelperStepResult{Run: run}, err
		}
		return helperResult(next, helperID, true)
	}
	if !domainexecution.CurrentHelperContribution(*helper.ContributionObservation, helper, run.Execution, nowMillis) {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperContributionInvalid)
	}
	if helper.HandoffEffect.Phase != domainexecution.EffectComplete {
		return controller.stepHelperRuntimeEffect(ctx, run, helperID, domainexecution.EffectHelperCommitHandoff, domainexecution.NeedHelperHandoffUnknown)
	}
	next := run
	next.Execution.Helpers = domainexecution.CloneHelpers(run.Execution.Helpers)
	nextHelper, _ := helperByID(&next.Execution, helperID)
	nextHelper.Handoff = &domainexecution.HelperHandoff{
		CommitSHA: helper.Contribution.CommitSHA, ObservationID: helper.ContributionObservation.ID,
		ImportedFactHash: helper.HandoffEffect.ObservedFactHash,
	}
	nextHelper.Phase = domainexecution.HelperHandoffReady
	if err := controller.persistRun(ctx, run, next, "helper.commit_handoff_ready"); err != nil {
		return HelperStepResult{Run: run}, err
	}
	return helperResult(next, helperID, true)
}

func (controller *Controller) RequestHelperCleanup(ctx context.Context, runID, helperID string, nowMillis int64) (HelperStepResult, error) {
	run, err := controller.store.Run(ctx, runID)
	if err != nil {
		return HelperStepResult{}, err
	}
	if err := controller.currentExecutionAuthority(ctx, run, nowMillis); err != nil {
		return HelperStepResult{Run: run}, err
	}
	next := run
	next.Execution.Helpers = domainexecution.CloneHelpers(run.Execution.Helpers)
	helper, err := helperByID(&next.Execution, helperID)
	if err != nil {
		return HelperStepResult{Run: run}, err
	}
	if helper.NativeAgentID == "" || !helperBudgetReleased(run, *helper) ||
		(helper.Mode == domainexecution.HelperWriter && helper.Handoff == nil) {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperCleanupUnproven)
	}
	if helper.Phase == domainexecution.HelperTerminal {
		return helperResult(run, helperID, false)
	}
	helper.Archive = newEffect(run.ID+"-"+helper.ID, domainexecution.EffectHelperAgentArchive, 2)
	helper.Phase = domainexecution.HelperCleanupRequested
	if err := controller.persistRun(ctx, run, next, "helper.cleanup_requested"); err != nil {
		return HelperStepResult{Run: run}, err
	}
	return helperResult(next, helperID, true)
}

func (controller *Controller) stepHelperArchive(ctx context.Context, run domain.Run, helperID string, nowMillis int64) (HelperStepResult, error) {
	helper := run.Execution.Helpers[domainexecution.HelperIndex(run.Execution.Helpers, helperID)]
	if controller.helperRuntime == nil {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperCleanupUnproven)
	}
	operational, operationalErr := controller.runtime.ObserveOperational(ctx, run.Execution.Scope, run.Execution.OperationalPolicy)
	boundary, boundaryErr := controller.helperRuntime.ObserveHelperBoundary(ctx, helperRuntimeRequest(run, helper, helper.Boundary))
	if operationalErr != nil || domainexecution.EvaluateOperationalLimits(run.Execution.OperationalPolicy, operational, nowMillis).Kind != domainexecution.AdmissionAllow ||
		boundaryErr != nil || !domainexecution.CurrentHelperBoundary(boundary, helper, run.Execution, nowMillis) {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperCleanupUnproven)
	}
	effect := helper.Archive
	arguments, err := helperHostArguments(run, helper, effect)
	if err != nil {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperCleanupUnproven)
	}
	registration, _ := helperRegistration(run, helper)
	if err := controller.verifyHost(ctx); err != nil {
		return HelperStepResult{Run: run}, err
	}
	if effect.Observation == nil {
		observeArguments := arguments
		observeCommand := host.Command{
			RequestID:      stableID("request", effect.ID, fmt.Sprintf("observe-%d", run.Version)),
			IdempotencyKey: stableID("idempotency", effect.ID, "observe"), ExpectedVersion: run.Version,
			AfterCursor: hostResumeCursor(run.Execution), Capability: host.CapabilityHelperAgentObserve,
			Arguments: observeArguments,
		}
		if err := host.AdmitHelperObserve(observeCommand, registration, helper.NativeAgentID); err != nil {
			return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperCleanupUnproven)
		}
		observed, invokeErr := controller.host.Invoke(ctx, observeCommand)
		if invokeErr != nil {
			return HelperStepResult{Run: run}, invokeErr
		}
		if err := host.ValidateObservation(observeCommand, observed); err != nil ||
			observed.Result.ExternalID != helper.NativeAgentID || observed.Result.CorrelationHash != helper.Admission.LabelDigest {
			return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperCleanupUnproven)
		}
		observedAt, parseErr := time.Parse(time.RFC3339Nano, observed.ObservedAt)
		if parseErr != nil {
			return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperCleanupUnproven)
		}
		normalized := domainexecution.EffectObservation{
			ID:       stableID("host-observation", observed.RequestID, fmt.Sprintf("cursor-%d", observed.Cursor)),
			EffectID: effect.ID, Status: observed.Result.Status, ExternalID: observed.Result.ExternalID,
			Cursor: observed.Cursor, ObservedAt: observed.ObservedAt, ObservedAtMillis: observedAt.UnixMilli(),
			MaximumAgeMillis: observed.Result.MaximumAgeMillis, BindingHash: observed.Result.BindingHash,
			CorrelationHash: observed.Result.CorrelationHash, PriorDispatcherAbsent: observed.Result.PriorDispatcherAbsent,
		}
		normalized.FactHash = domainexecution.EffectObservationHash(normalized)
		next := run
		next.Execution.Helpers = domainexecution.CloneHelpers(run.Execution.Helpers)
		nextHelper, _ := helperByID(&next.Execution, helperID)
		nextHelper.Archive.Observation = &normalized
		if err := controller.persistRun(ctx, run, next, "helper.archive_observed"); err != nil {
			return HelperStepResult{Run: run}, err
		}
		return helperResult(next, helperID, true)
	}
	if !domainexecution.CurrentEffectObservation(*effect.Observation, nowMillis) || effect.Observation.ExternalID != helper.NativeAgentID ||
		effect.Observation.CorrelationHash != helper.Admission.LabelDigest {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperCleanupUnproven)
	}
	if effect.Observation.Status == domainexecution.ObservationDesired {
		next := run
		next.Execution.Helpers = domainexecution.CloneHelpers(run.Execution.Helpers)
		nextHelper, _ := helperByID(&next.Execution, helperID)
		nextHelper.Archive.Phase = domainexecution.EffectComplete
		nextHelper.Archive.ExternalID = helper.NativeAgentID
		nextHelper.Archive.ObservedFactHash = effect.Observation.FactHash
		nextHelper.Archive.ObservedCorrelation = effect.Observation.CorrelationHash
		nextHelper.Archive.Observation = nil
		if helper.Mode == domainexecution.HelperWriter {
			nextHelper.CheckoutRemove = newEffect(run.ID+"-"+helper.ID, domainexecution.EffectHelperCheckoutRemove, 1)
		} else {
			if controller.helperRuntime.ReleaseHelperCapacity(ctx, helper.CapacityReservationID, run.Execution.Scope) != nil {
				return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperCleanupUnproven)
			}
			nextHelper.Phase = domainexecution.HelperTerminal
		}
		if err := controller.persistRun(ctx, run, next, "helper.agent_archived"); err != nil {
			return HelperStepResult{Run: run}, err
		}
		return helperResult(next, helperID, true)
	}
	if effect.Observation.Status != domainexecution.ObservationOwnedPresent {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperCleanupUnproven)
	}
	next := run
	next.Execution.Helpers = domainexecution.CloneHelpers(run.Execution.Helpers)
	nextHelper, _ := helperByID(&next.Execution, helperID)
	if nextHelper.Archive.Attempt >= nextHelper.Archive.AttemptLimit {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperCleanupUnproven)
	}
	nextHelper.Archive.Phase = domainexecution.EffectDispatching
	nextHelper.Archive.Attempt++
	nextHelper.Archive.Observation = nil
	won, err := controller.persistRunTransition(ctx, run, next, "helper.archive_dispatching")
	if err != nil {
		return HelperStepResult{Run: run}, err
	}
	if !won {
		current, readErr := controller.store.Run(ctx, run.ID)
		if readErr != nil {
			return HelperStepResult{}, readErr
		}
		return helperResult(current, helperID, false)
	}
	arguments, err = helperHostArguments(next, *nextHelper, nextHelper.Archive)
	if err != nil {
		return controller.parkHelper(ctx, next, helperID, domainexecution.NeedHelperCleanupUnproven)
	}
	command := host.Command{
		RequestID:      stableID("request", effect.ID, fmt.Sprintf("attempt-%d", nextHelper.Archive.Attempt)),
		IdempotencyKey: effect.ID, ExpectedVersion: run.Version + 1, Capability: host.CapabilityAgentArchive,
		AfterCursor: hostResumeCursor(run.Execution), Arguments: arguments,
	}
	if err := host.AdmitHelperArchive(command, registration, helper.NativeAgentID); err != nil {
		return controller.parkHelper(ctx, next, helperID, domainexecution.NeedHelperCleanupUnproven)
	}
	if _, err := controller.host.Invoke(ctx, command); err != nil {
		return helperResult(next, helperID, true)
	}
	return helperResult(next, helperID, true)
}

// StepHelper advances at most one helper transition. A fresh Controller can
// resume every phase from the durable Run without repeating a possible
// helper-creation or commit-handoff effect.
func (controller *Controller) StepHelper(ctx context.Context, runID, helperID string, nowMillis int64) (HelperStepResult, error) {
	run, err := controller.store.Run(ctx, runID)
	if err != nil {
		return HelperStepResult{}, err
	}
	if err := controller.currentExecutionAuthority(ctx, run, nowMillis); err != nil {
		return HelperStepResult{Run: run}, err
	}
	index := domainexecution.HelperIndex(run.Execution.Helpers, helperID)
	if index < 0 {
		return HelperStepResult{Run: run}, errors.New("helper is not owned by the Run")
	}
	helper := run.Execution.Helpers[index]
	if helper.Scope != run.Execution.Scope || helper.ParentAgentID != run.Execution.Agent.ExternalID {
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperIdentityInvalid)
	}
	if helper.Phase == domainexecution.HelperParked && helper.NeedsYou != nil &&
		(helper.NeedsYou.Code == domainexecution.NeedHelperCreationUnknown || helper.NeedsYou.Code == domainexecution.NeedHelperIdentityInvalid) &&
		helper.Admission != nil && helper.Admission.ConsumedAtMillis != 0 {
		return controller.observeHelperAgent(ctx, run, helperID, nowMillis)
	}
	switch helper.Phase {
	case domainexecution.HelperRequested:
		return controller.prepareHelper(ctx, run, helperID, nowMillis)
	case domainexecution.HelperPreparing:
		if helper.Mode == domainexecution.HelperWriter && helper.Checkout.Phase != domainexecution.EffectComplete {
			return controller.stepHelperRuntimeEffect(ctx, run, helperID, domainexecution.EffectHelperCheckoutCreate, domainexecution.NeedHelperBoundaryInvalid)
		}
		if helper.Boundary.Phase != domainexecution.EffectComplete {
			return controller.stepHelperRuntimeEffect(ctx, run, helperID, domainexecution.EffectHelperBoundary, domainexecution.NeedHelperBoundaryInvalid)
		}
		return controller.issueHelperAdmission(ctx, run, helperID, nowMillis)
	case domainexecution.HelperAdmissionReady:
		return helperResult(run, helperID, false)
	case domainexecution.HelperInvocationConsumed:
		return controller.observeHelperAgent(ctx, run, helperID, nowMillis)
	case domainexecution.HelperActive:
		return controller.observeHelperAgent(ctx, run, helperID, nowMillis)
	case domainexecution.HelperContributionReady:
		return controller.stepHelperContribution(ctx, run, helperID, nowMillis)
	case domainexecution.HelperHandoffReady:
		return helperResult(run, helperID, false)
	case domainexecution.HelperCleanupRequested:
		if helper.Archive.Phase != domainexecution.EffectComplete {
			return controller.stepHelperArchive(ctx, run, helperID, nowMillis)
		}
		if helper.Mode == domainexecution.HelperWriter && helper.CheckoutRemove.Phase == domainexecution.EffectComplete {
			if controller.helperRuntime == nil || controller.helperRuntime.ReleaseHelperCapacity(ctx, helper.CapacityReservationID, run.Execution.Scope) != nil {
				return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperCleanupUnproven)
			}
			next := run
			next.Execution.Helpers = domainexecution.CloneHelpers(run.Execution.Helpers)
			nextHelper, _ := helperByID(&next.Execution, helperID)
			nextHelper.Phase = domainexecution.HelperTerminal
			if err := controller.persistRun(ctx, run, next, "helper.terminal"); err != nil {
				return HelperStepResult{Run: run}, err
			}
			return helperResult(next, helperID, true)
		}
		if helper.Mode == domainexecution.HelperWriter && helper.CheckoutRemove.Phase != domainexecution.EffectComplete {
			if helper.CheckoutRemove.Phase == domainexecution.EffectIntentRecorded {
				if controller.helperRuntime == nil {
					return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperCleanupUnproven)
				}
				operational, operationalErr := controller.runtime.ObserveOperational(ctx, run.Execution.Scope, run.Execution.OperationalPolicy)
				boundary, boundaryErr := controller.helperRuntime.ObserveHelperBoundary(ctx, helperRuntimeRequest(run, helper, helper.Boundary))
				if operationalErr != nil || domainexecution.EvaluateOperationalLimits(run.Execution.OperationalPolicy, operational, nowMillis).Kind != domainexecution.AdmissionAllow ||
					boundaryErr != nil || !domainexecution.CurrentHelperBoundary(boundary, helper, run.Execution, nowMillis) {
					return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperCleanupUnproven)
				}
			}
			return controller.stepHelperRuntimeEffect(ctx, run, helperID, domainexecution.EffectHelperCheckoutRemove, domainexecution.NeedHelperCleanupUnproven)
		}
		return helperResult(run, helperID, false)
	case domainexecution.HelperTerminal, domainexecution.HelperParked:
		return helperResult(run, helperID, false)
	default:
		return controller.parkHelper(ctx, run, helperID, domainexecution.NeedHelperIdentityInvalid)
	}
}
