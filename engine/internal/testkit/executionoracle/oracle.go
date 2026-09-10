// SPDX-License-Identifier: Apache-2.0

package executionoracle

import (
	"math/bits"
	"slices"
)

// ActionKind is the complete oracle output vocabulary. It deliberately omits
// Git, provider, delivery, integration, cleanup-deletion, and arbitrary host
// operations.
type ActionKind uint8

const (
	ActionCreateWorkspace ActionKind = iota + 1
	ActionCreateWorkerBootstrap
	ActionSendWorkerPrompt
	ActionCreateReviewerBootstrap
	ActionSendReviewerPrompt
	ActionIssueHelperAdmission
	ActionObserveWorker
	ActionObserveReviewer
	ActionObserveHelper
	ActionObserveAll
	ActionAwaitNotification
	ActionAwaitSafeBoundary
	ActionPauseRun
	ActionResumeReconcile
	ActionArchiveWorker
	ActionArchiveReviewer
	ActionArchiveHelper
	ActionCancelRun
	ActionReplaceWorkerBootstrap
	ActionParkNeedsYou
)

// ActionKinds returns the closed action vocabulary in canonical order.
func ActionKinds() []ActionKind {
	return []ActionKind{
		ActionCreateWorkspace, ActionCreateWorkerBootstrap, ActionSendWorkerPrompt,
		ActionCreateReviewerBootstrap, ActionSendReviewerPrompt,
		ActionIssueHelperAdmission, ActionObserveWorker, ActionObserveReviewer,
		ActionObserveHelper, ActionObserveAll, ActionAwaitNotification,
		ActionAwaitSafeBoundary, ActionPauseRun, ActionResumeReconcile,
		ActionArchiveWorker, ActionArchiveReviewer, ActionArchiveHelper,
		ActionCancelRun, ActionReplaceWorkerBootstrap, ActionParkNeedsYou,
	}
}

func (kind ActionKind) valid() bool {
	return kind >= ActionCreateWorkspace && kind <= ActionParkNeedsYou
}

// Reason is the closed explanation and refusal vocabulary.
type Reason string

const (
	ReasonWorkspaceRequired      Reason = "workspace_required"
	ReasonWorkerRequired         Reason = "worker_required"
	ReasonWorkerPromptRequired   Reason = "worker_prompt_required"
	ReasonReviewerRequired       Reason = "reviewer_required"
	ReasonReviewerPromptRequired Reason = "reviewer_prompt_required"
	ReasonHelperAdmission        Reason = "helper_admission_required"
	ReasonHelperObservation      Reason = "helper_observation_required"
	ReasonHelperLimit            Reason = "helper_limit_exhausted"
	ReasonPreparationRequired    Reason = "preparation_required"
	ReasonWorkerVisibility       Reason = "worker_visibility_required"
	ReasonReviewerVisibility     Reason = "reviewer_visibility_required"
	ReasonAwaitNotification      Reason = "await_terminal_notification"
	ReasonObserveWake            Reason = "observe_terminal_wake"
	ReasonObserveStall           Reason = "observe_compound_stall"
	ReasonLeaseObserveOnly       Reason = "lease_observe_only"
	ReasonExternalUnavailable    Reason = "external_unavailable"
	ReasonSoftBudget             Reason = "soft_budget_boundary"
	ReasonHardBudget             Reason = "hard_budget_exhausted"
	ReasonPaused                 Reason = "paused"
	ReasonResume                 Reason = "resume_reconciliation"
	ReasonCancelled              Reason = "cancelled"
	ReasonEmergencyStop          Reason = "emergency_stop"
	ReasonReplacement            Reason = "worker_replacement"
	ReasonReplacementExhausted   Reason = "replacement_exhausted"
	ReasonUnknownFact            Reason = "unknown_fact"
	ReasonContradictoryFact      Reason = "contradictory_fact"
	ReasonScopeViolation         Reason = "scope_violation"
	ReasonOwnershipViolation     Reason = "ownership_violation"
	ReasonProfileDrift           Reason = "frozen_profile_drift"
	ReasonMCPViolation           Reason = "scoped_mcp_violation"
	ReasonProviderNotAdmitted    Reason = "provider_not_admitted"
	ReasonLivenessAmbiguous      Reason = "liveness_ambiguous"
	ReasonFailureUnrecoverable   Reason = "failure_unrecoverable"
	ReasonRunTerminal            Reason = "run_terminal"
	ReasonHelperUnknown          Reason = "helper_creation_unknown"
)

// Reasons returns the complete closed decision explanation vocabulary.
func Reasons() []Reason {
	return []Reason{
		ReasonWorkspaceRequired, ReasonWorkerRequired, ReasonWorkerPromptRequired,
		ReasonReviewerRequired, ReasonReviewerPromptRequired, ReasonHelperAdmission,
		ReasonHelperObservation, ReasonHelperLimit, ReasonPreparationRequired,
		ReasonWorkerVisibility, ReasonReviewerVisibility, ReasonAwaitNotification,
		ReasonObserveWake, ReasonObserveStall, ReasonLeaseObserveOnly,
		ReasonExternalUnavailable, ReasonSoftBudget, ReasonHardBudget, ReasonPaused,
		ReasonResume, ReasonCancelled, ReasonEmergencyStop, ReasonReplacement,
		ReasonReplacementExhausted, ReasonUnknownFact, ReasonContradictoryFact,
		ReasonScopeViolation, ReasonOwnershipViolation, ReasonProfileDrift,
		ReasonMCPViolation, ReasonProviderNotAdmitted, ReasonLivenessAmbiguous,
		ReasonFailureUnrecoverable, ReasonRunTerminal, ReasonHelperUnknown,
	}
}

func (reason Reason) valid() bool { return slices.Contains(Reasons(), reason) }

// Action is one deterministic store, host, wait, or refusal expectation.
type Action struct {
	Kind           ActionKind
	Scope          Scope
	Role           Role
	TargetID       string
	ParentID       string
	Reason         Reason
	NotifyOnFinish bool
}

// Decision is the complete deterministic oracle result. Multi-action results
// occur only for scoped cancel and emergency-stop containment.
type Decision struct {
	Reason  Reason
	Actions []Action
}

type guard uint8

const (
	guardClosedVocabulary guard = iota
	guardFixedScope
	guardWorkspaceOwnership
	guardOnePrimary
	guardHelperIsolation
	guardCandidateOwnership
	guardFrozenProfile
	guardLease
	guardProvider
	guardScopedMCP
	guardHardBudget
	guardControl
	guardSoftBudget
	guardLiveness
	guardReplacement
	guardCount
)

type guardSet [guardCount]bool

func allGuards() guardSet {
	var result guardSet
	for index := range result {
		result[index] = true
	}
	return result
}

// Evaluate applies every independent M3 oracle guard.
func Evaluate(snapshot Snapshot) Decision { return evaluateWithGuards(snapshot.Clone(), allGuards()) }

func evaluateWithGuards(snapshot Snapshot, guards guardSet) Decision {
	canonicalize(&snapshot)
	if reason := validateFacts(snapshot, guards); reason != "" {
		return park(snapshot.Run.Scope, reason)
	}
	if guards[guardLease] {
		if snapshot.Lease.State != LeaseCurrent || !snapshot.Lease.DispatchAllowed {
			if snapshot.Lease.State == LeaseAmbiguous {
				return park(snapshot.Run.Scope, ReasonContradictoryFact)
			}
			return single(snapshot.Run.Scope, ActionObserveAll, 0, "lease", "", ReasonLeaseObserveOnly)
		}
	}
	if guards[guardControl] {
		switch snapshot.Control.Kind {
		case ControlEmergencyStop:
			if !snapshot.Control.HumanConfirmed {
				return park(snapshot.Run.Scope, ReasonContradictoryFact)
			}
			return containment(snapshot, ReasonEmergencyStop)
		case ControlCancel:
			return containment(snapshot, ReasonCancelled)
		}
	}
	if reason := validateOperationalFacts(snapshot, guards); reason != "" {
		return park(snapshot.Run.Scope, reason)
	}
	if guards[guardHardBudget] {
		if reason := budgetFactReason(snapshot.Usage); reason != "" {
			return park(snapshot.Run.Scope, reason)
		}
		if hardBudgetReached(snapshot.Usage) {
			return park(snapshot.Run.Scope, ReasonHardBudget)
		}
	}
	if guards[guardControl] && snapshot.Control.Kind == ControlPause {
		return pause(snapshot, ReasonPaused)
	}
	if guards[guardControl] && snapshot.Control.Kind == ControlResume {
		return single(snapshot.Run.Scope, ActionResumeReconcile, 0, "run", "", ReasonResume)
	}
	if guards[guardSoftBudget] && softBudgetReached(snapshot.Usage) {
		return pause(snapshot, ReasonSoftBudget)
	}
	if guards[guardProvider] {
		switch snapshot.Provider.State {
		case ProviderUnavailable:
			return single(snapshot.Run.Scope, ActionObserveAll, 0, "provider", "", ReasonExternalUnavailable)
		case ProviderNotAdmitted:
			return park(snapshot.Run.Scope, ReasonProviderNotAdmitted)
		case ProviderStale, ProviderAmbiguous:
			return park(snapshot.Run.Scope, ReasonContradictoryFact)
		}
	}
	if snapshot.Failure.Kind == FailureExternalUnavailable {
		return single(snapshot.Run.Scope, ActionObserveAll, 0, "external", "", ReasonExternalUnavailable)
	}
	if snapshot.Failure.Kind == FailureUnknown || snapshot.Failure.Kind == FailureContradictory {
		return park(snapshot.Run.Scope, ReasonContradictoryFact)
	}
	if snapshot.Failure.Kind == FailureUnrecoverableWorker {
		return park(snapshot.Run.Scope, ReasonFailureUnrecoverable)
	}
	if guards[guardReplacement] && snapshot.Failure.Kind == FailureRecoverableWorker {
		return replacement(snapshot)
	}
	if guards[guardLiveness] {
		if snapshot.Liveness.ActiveExternalCadenceSeconds != 30 ||
			snapshot.Liveness.IdleExternalCadenceSeconds != 300 || snapshot.Liveness.ActivePolling {
			return park(snapshot.Run.Scope, ReasonLivenessAmbiguous)
		}
		switch snapshot.Liveness.State {
		case LivenessTerminalWake:
			if snapshot.Liveness.TerminalLatencyMillis >= 1000 {
				return park(snapshot.Run.Scope, ReasonLivenessAmbiguous)
			}
			return single(snapshot.Run.Scope, ActionObserveAll, 0, "terminal-event", "", ReasonObserveWake)
		case LivenessCompoundStalled:
			if compoundStall(snapshot.Liveness) {
				return single(snapshot.Run.Scope, ActionObserveAll, 0, "compound-stall", "", ReasonObserveStall)
			}
		case LivenessAmbiguous:
			return park(snapshot.Run.Scope, ReasonLivenessAmbiguous)
		}
	}
	if snapshot.Run.State == RunPaused {
		return single(snapshot.Run.Scope, ActionPauseRun, 0, "run", "", ReasonPaused)
	}
	if snapshot.Run.State == RunCancelled || snapshot.Run.State == RunTerminal {
		return Decision{Reason: ReasonRunTerminal}
	}
	if !snapshot.Run.PreparationReady {
		return park(snapshot.Run.Scope, ReasonPreparationRequired)
	}
	if snapshot.HelperRequest.State == HelperUnknown {
		return park(snapshot.Run.Scope, ReasonHelperUnknown)
	}
	if snapshot.HelperRequest.State == HelperRequested {
		if helperCount(snapshot.Agents)+1 > int(snapshot.Run.MaximumHelpers) {
			return park(snapshot.Run.Scope, ReasonHelperLimit)
		}
		return single(
			snapshot.Run.Scope, ActionIssueHelperAdmission, RoleHelper,
			snapshot.HelperRequest.IntentID, snapshot.HelperRequest.ParentID, ReasonHelperAdmission,
		)
	}
	if snapshot.HelperRequest.State == HelperAdmitted {
		return single(
			snapshot.Run.Scope, ActionObserveHelper, RoleHelper,
			snapshot.HelperRequest.IntentID, snapshot.HelperRequest.ParentID, ReasonHelperObservation,
		)
	}
	if snapshot.Workspace.State == WorkspaceAbsent {
		return single(snapshot.Run.Scope, ActionCreateWorkspace, 0, snapshot.Workspace.ID, "", ReasonWorkspaceRequired)
	}
	if snapshot.Workspace.State == WorkspaceArchived {
		return park(snapshot.Run.Scope, ReasonOwnershipViolation)
	}
	worker := currentAgent(snapshot.Agents, RoleWorker)
	if worker == nil {
		if !snapshot.Run.RootWorkerVisible {
			return park(snapshot.Run.Scope, ReasonWorkerVisibility)
		}
		return single(snapshot.Run.Scope, ActionCreateWorkerBootstrap, RoleWorker, workerID(snapshot, false), "", ReasonWorkerRequired)
	}
	if !worker.BootstrapDone {
		return single(snapshot.Run.Scope, ActionObserveWorker, RoleWorker, worker.ID, "", ReasonWorkerRequired)
	}
	if worker.Prompt == PromptNone {
		return single(snapshot.Run.Scope, ActionSendWorkerPrompt, RoleWorker, worker.ID, "", ReasonWorkerPromptRequired)
	}
	if worker.Prompt == PromptSent || snapshot.Liveness.State == LivenessSuspected {
		return single(snapshot.Run.Scope, ActionAwaitNotification, RoleWorker, worker.ID, "", ReasonAwaitNotification)
	}
	if worker.Prompt == PromptAmbiguous || worker.State == AgentAmbiguous {
		return park(snapshot.Run.Scope, ReasonContradictoryFact)
	}
	if snapshot.Candidate.State == CandidateAbsent {
		return single(snapshot.Run.Scope, ActionObserveWorker, RoleWorker, worker.ID, "", ReasonObserveWake)
	}
	if snapshot.Candidate.State != CandidateCurrent {
		return park(snapshot.Run.Scope, ReasonContradictoryFact)
	}
	reviewer := currentAgent(snapshot.Agents, RoleReviewer)
	if reviewer == nil {
		if !snapshot.Run.RootReviewerVisible {
			return park(snapshot.Run.Scope, ReasonReviewerVisibility)
		}
		return single(snapshot.Run.Scope, ActionCreateReviewerBootstrap, RoleReviewer, reviewerID(snapshot), "", ReasonReviewerRequired)
	}
	if !reviewer.BootstrapDone {
		return single(snapshot.Run.Scope, ActionObserveReviewer, RoleReviewer, reviewer.ID, "", ReasonReviewerRequired)
	}
	if reviewer.Prompt == PromptNone {
		return single(snapshot.Run.Scope, ActionSendReviewerPrompt, RoleReviewer, reviewer.ID, "", ReasonReviewerPromptRequired)
	}
	if reviewer.Prompt == PromptSent {
		return single(snapshot.Run.Scope, ActionAwaitNotification, RoleReviewer, reviewer.ID, "", ReasonAwaitNotification)
	}
	return single(snapshot.Run.Scope, ActionObserveReviewer, RoleReviewer, reviewer.ID, "", ReasonObserveWake)
}

func canonicalize(snapshot *Snapshot) {
	slices.SortFunc(snapshot.Agents, func(left, right AgentFact) int {
		if left.Role != right.Role {
			return int(left.Role) - int(right.Role)
		}
		if left.ID < right.ID {
			return -1
		}
		if left.ID > right.ID {
			return 1
		}
		return 0
	})
	slices.SortFunc(snapshot.MCP.Bindings, func(left, right MCPBinding) int {
		return int(left.Role) - int(right.Role)
	})
}

func validateFacts(snapshot Snapshot, guards guardSet) Reason {
	if guards[guardClosedVocabulary] {
		if snapshot.Platform != PlatformLinux || !snapshot.Organizer.State.valid() || !snapshot.Run.State.valid() ||
			!snapshot.Workspace.State.valid() || !snapshot.Lease.State.valid() || !snapshot.Provider.Kind.valid() ||
			!snapshot.Provider.State.valid() || !snapshot.Control.Kind.valid() || !snapshot.Failure.Kind.valid() ||
			!snapshot.Liveness.State.valid() || !snapshot.Candidate.State.valid() || !snapshot.HelperRequest.State.valid() ||
			!allFactKindsValid() {
			return ReasonUnknownFact
		}
		for _, agent := range snapshot.Agents {
			if agent.Role == RoleOrganizer || !agent.Role.valid() || !agent.State.valid() || !agent.Prompt.valid() {
				return ReasonUnknownFact
			}
		}
	}
	if !snapshot.Run.Scope.valid() || snapshot.Organizer.ProjectID == "" || snapshot.Organizer.Revision == "" ||
		snapshot.Run.OrganizerRevision == "" || snapshot.Run.ProfileRevision == "" || snapshot.Workspace.ID == "" ||
		snapshot.Lease.ProjectID == "" || snapshot.Lease.Holder == "" || snapshot.Lease.Epoch == 0 {
		return ReasonUnknownFact
	}
	if snapshot.Run.MaximumReplacements != 1 || snapshot.Run.MaximumHelpers == 0 {
		return ReasonContradictoryFact
	}
	if snapshot.Organizer.State != OrganizerApproved {
		return ReasonContradictoryFact
	}
	if snapshot.Organizer.ProjectID != snapshot.Run.Scope.ProjectID || snapshot.Organizer.Revision != snapshot.Run.OrganizerRevision {
		return ReasonProfileDrift
	}
	if snapshot.Control.RunID != snapshot.Run.Scope.RunID || snapshot.Failure.RunID != snapshot.Run.Scope.RunID ||
		snapshot.HelperRequest.RunID != snapshot.Run.Scope.RunID {
		return ReasonScopeViolation
	}
	if guards[guardWorkspaceOwnership] &&
		(snapshot.Workspace.ID != snapshot.Run.Scope.WorkspaceID || snapshot.Workspace.RunID != snapshot.Run.Scope.RunID || !snapshot.Workspace.Owned) {
		return ReasonOwnershipViolation
	}
	if guards[guardFixedScope] {
		for _, agent := range snapshot.Agents {
			if agent.RunID != snapshot.Run.Scope.RunID || agent.WorkspaceID != snapshot.Run.Scope.WorkspaceID {
				return ReasonScopeViolation
			}
		}
	}
	if guards[guardOnePrimary] {
		workers := roleCount(snapshot.Agents, RoleWorker)
		if activeCount(snapshot.Agents, RoleWorker) > 1 || workers > int(snapshot.Run.ReplacementCount)+1 {
			return ReasonOwnershipViolation
		}
	}
	if guards[guardOnePrimary] && activeCount(snapshot.Agents, RoleReviewer) > 1 {
		return ReasonOwnershipViolation
	}
	if guards[guardHelperIsolation] {
		workerIDs := make(map[string]bool)
		activeWorkerIDs := make(map[string]bool)
		for _, agent := range snapshot.Agents {
			if agent.Role == RoleWorker {
				workerIDs[agent.ID] = true
				if !terminated(agent) {
					activeWorkerIDs[agent.ID] = true
				}
				if agent.ParentID != "" {
					return ReasonOwnershipViolation
				}
			}
			if agent.Role == RoleReviewer && agent.ParentID != "" {
				return ReasonOwnershipViolation
			}
		}
		for _, agent := range snapshot.Agents {
			if agent.Role == RoleHelper && (!workerIDs[agent.ParentID] || agent.ParentID == "") {
				return ReasonScopeViolation
			}
		}
		if snapshot.HelperRequest.State != HelperNotRequested {
			if snapshot.HelperRequest.WorkspaceID != snapshot.Run.Scope.WorkspaceID ||
				snapshot.HelperRequest.ParentID == "" || !activeWorkerIDs[snapshot.HelperRequest.ParentID] ||
				snapshot.HelperRequest.IntentID == "" {
				return ReasonScopeViolation
			}
			if snapshot.HelperRequest.State == HelperObserved &&
				(snapshot.HelperRequest.HelperID == "" || !agentExists(snapshot.Agents, RoleHelper, snapshot.HelperRequest.HelperID)) {
				return ReasonScopeViolation
			}
		}
	}
	if guards[guardCandidateOwnership] && snapshot.Candidate.State == CandidateCurrent {
		if snapshot.Candidate.ID == "" || snapshot.Candidate.RunID != snapshot.Run.Scope.RunID ||
			snapshot.Candidate.WorkerID == "" || !agentExists(snapshot.Agents, RoleWorker, snapshot.Candidate.WorkerID) {
			return ReasonOwnershipViolation
		}
		for _, agent := range snapshot.Agents {
			if agent.Role == RoleReviewer && agent.CandidateID != snapshot.Candidate.ID {
				return ReasonOwnershipViolation
			}
		}
	}
	if snapshot.Run.ReplacementCount > snapshot.Run.MaximumReplacements {
		return ReasonContradictoryFact
	}
	return ""
}

func validateOperationalFacts(snapshot Snapshot, guards guardSet) Reason {
	if guards[guardFrozenProfile] {
		if snapshot.Provider.ProfileRevision != snapshot.Run.ProfileRevision {
			return ReasonProfileDrift
		}
		for _, agent := range snapshot.Agents {
			if agent.ProfileRevision != snapshot.Run.ProfileRevision {
				return ReasonProfileDrift
			}
		}
	}
	if guards[guardScopedMCP] && !validMCP(snapshot.MCP, snapshot.Run.Scope) {
		return ReasonMCPViolation
	}
	if guards[guardProvider] && snapshot.Provider.State == ProviderReady &&
		(!snapshot.Provider.SupportsMCP || !snapshot.Provider.ExactToolPolicy) {
		return ReasonProviderNotAdmitted
	}
	return ""
}

func validMCP(fact MCPFact, scope Scope) bool {
	if len(fact.Bindings) != len(Roles()) {
		return false
	}
	seen := make(map[Role]bool, len(fact.Bindings))
	for _, binding := range fact.Bindings {
		if !binding.Role.valid() || seen[binding.Role] || binding.ProjectID != scope.ProjectID ||
			binding.Tools != AllowedTools(binding.Role) || binding.Tools&^allTools() != 0 {
			return false
		}
		seen[binding.Role] = true
		if binding.Role == RoleOrganizer {
			if binding.TaskID != "" || binding.RunID != "" || binding.WorkspaceID != "" {
				return false
			}
		} else if binding.TaskID != scope.TaskID || binding.RunID != scope.RunID || binding.WorkspaceID != scope.WorkspaceID {
			return false
		}
	}
	return true
}

func allFactKindsValid() bool {
	values := FactKinds()
	if len(values) != int(FactFailure) {
		return false
	}
	for index, value := range values {
		if !value.valid() || int(value) != index+1 {
			return false
		}
	}
	return true
}

func containment(snapshot Snapshot, reason Reason) Decision {
	actions := make([]Action, 0, len(snapshot.Agents)+1)
	for _, agent := range snapshot.Agents {
		if terminated(agent) {
			continue
		}
		kind := ActionArchiveHelper
		switch agent.Role {
		case RoleWorker:
			kind = ActionArchiveWorker
		case RoleReviewer:
			kind = ActionArchiveReviewer
		}
		actions = append(actions, Action{Kind: kind, Scope: snapshot.Run.Scope, Role: agent.Role, TargetID: agent.ID, ParentID: agent.ParentID, Reason: reason})
	}
	actions = append(actions, Action{Kind: ActionCancelRun, Scope: snapshot.Run.Scope, TargetID: snapshot.Run.Scope.RunID, Reason: reason})
	return Decision{Reason: reason, Actions: actions}
}

func pause(snapshot Snapshot, reason Reason) Decision {
	if activeTurn(snapshot.Agents) {
		return single(snapshot.Run.Scope, ActionAwaitSafeBoundary, 0, snapshot.Run.Scope.RunID, "", reason)
	}
	return single(snapshot.Run.Scope, ActionPauseRun, 0, snapshot.Run.Scope.RunID, "", reason)
}

func replacement(snapshot Snapshot) Decision {
	worker := currentAgent(snapshot.Agents, RoleWorker)
	if worker != nil {
		return single(snapshot.Run.Scope, ActionObserveWorker, RoleWorker, worker.ID, "", ReasonReplacement)
	}
	if !hasTerminatedWorker(snapshot.Agents) {
		return single(snapshot.Run.Scope, ActionObserveWorker, RoleWorker, "worker", "", ReasonReplacement)
	}
	if snapshot.Run.ReplacementCount >= snapshot.Run.MaximumReplacements {
		return park(snapshot.Run.Scope, ReasonReplacementExhausted)
	}
	return single(snapshot.Run.Scope, ActionReplaceWorkerBootstrap, RoleWorker, workerID(snapshot, true), "", ReasonReplacement)
}

func compoundStall(fact LivenessFact) bool {
	return fact.QuietSeconds >= 300 && fact.LocalSamples >= 3 && fact.ExternalCycles >= 2 &&
		fact.IndependentSources >= 2 && !fact.KnownWait && !fact.DeclaredLongStep && !fact.ExternalOutage
}

func budgetFactReason(usage UsageFact) Reason {
	for _, dimension := range []DimensionUsage{usage.Time, usage.Tokens, usage.Turns, usage.Cost} {
		if !dimension.State.valid() || dimension.Limit == 0 || dimension.Revision == 0 {
			return ReasonUnknownFact
		}
		if dimension.State != UsageCurrent {
			return ReasonContradictoryFact
		}
		if _, overflow := addUsage(dimension.Consumed, dimension.Reserved, dimension.NextReservation); overflow {
			return ReasonContradictoryFact
		}
	}
	return ""
}

func hardBudgetReached(usage UsageFact) bool {
	for _, dimension := range []DimensionUsage{usage.Time, usage.Tokens, usage.Turns, usage.Cost} {
		current, overflow := addUsage(dimension.Consumed, dimension.Reserved, 0)
		proposed, proposedOverflow := addUsage(dimension.Consumed, dimension.Reserved, dimension.NextReservation)
		if overflow || proposedOverflow || current >= dimension.Limit || proposed >= dimension.Limit {
			return true
		}
	}
	return false
}

func softBudgetReached(usage UsageFact) bool {
	for _, dimension := range []DimensionUsage{usage.Time, usage.Tokens, usage.Turns, usage.Cost} {
		proposed, overflow := addUsage(dimension.Consumed, dimension.Reserved, dimension.NextReservation)
		if overflow {
			return true
		}
		threshold := dimension.Limit - ((dimension.Limit/100)*15 + ((dimension.Limit%100)*15)/100)
		if proposed >= threshold && proposed < dimension.Limit && dimension.AcknowledgedRevision != dimension.Revision {
			return true
		}
	}
	return false
}

func addUsage(values ...uint64) (uint64, bool) {
	var result uint64
	for _, value := range values {
		var carry uint64
		result, carry = bits.Add64(result, value, 0)
		if carry != 0 {
			return 0, true
		}
	}
	return result, false
}

func activeCount(agents []AgentFact, role Role) int {
	count := 0
	for _, agent := range agents {
		if agent.Role == role && !terminated(agent) {
			count++
		}
	}
	return count
}

func roleCount(agents []AgentFact, role Role) int {
	count := 0
	for _, agent := range agents {
		if agent.Role == role {
			count++
		}
	}
	return count
}

func helperCount(agents []AgentFact) int { return activeCount(agents, RoleHelper) }

func currentAgent(agents []AgentFact, role Role) *AgentFact {
	for index := range agents {
		if agents[index].Role == role && !terminated(agents[index]) {
			return &agents[index]
		}
	}
	return nil
}

func agentExists(agents []AgentFact, role Role, id string) bool {
	for _, agent := range agents {
		if agent.Role == role && agent.ID == id {
			return true
		}
	}
	return false
}

func terminated(agent AgentFact) bool {
	return agent.State == AgentClosed && agent.Archived && agent.ProcessAbsent
}

func hasTerminatedWorker(agents []AgentFact) bool {
	for _, agent := range agents {
		if agent.Role == RoleWorker && terminated(agent) {
			return true
		}
	}
	return false
}

func activeTurn(agents []AgentFact) bool {
	for _, agent := range agents {
		if !terminated(agent) && agent.State == AgentRunning && agent.Prompt == PromptSent {
			return true
		}
	}
	return false
}

func workerID(snapshot Snapshot, replacement bool) string {
	suffix := ":0"
	if replacement {
		suffix = ":1"
	}
	return "worker:" + snapshot.Run.Scope.RunID + suffix
}

func reviewerID(snapshot Snapshot) string { return "reviewer:" + snapshot.Run.Scope.RunID + ":0" }

func park(scope Scope, reason Reason) Decision {
	return single(scope, ActionParkNeedsYou, 0, scope.RunID, "", reason)
}

func single(scope Scope, kind ActionKind, role Role, target, parent string, reason Reason) Decision {
	notify := kind == ActionSendWorkerPrompt || kind == ActionSendReviewerPrompt
	return Decision{Reason: reason, Actions: []Action{{Kind: kind, Scope: scope, Role: role, TargetID: target, ParentID: parent, Reason: reason, NotifyOnFinish: notify}}}
}
