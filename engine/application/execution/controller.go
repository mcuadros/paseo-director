// SPDX-License-Identifier: Apache-2.0

// Package execution composes the pure reducers with the TaskStore and thin
// effect ports. It persists intent and observations before dependent actions;
// adapter responses and AgentOutcomeClaims are never treated as evidence.
package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/agentprofile"
	domainexecution "github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/domain/runtimebudget"
	"github.com/mcuadros/director-engine/ports/host"
	reconciliationport "github.com/mcuadros/director-engine/ports/reconciliation"
	runtimeport "github.com/mcuadros/director-engine/ports/runtime"
	storeport "github.com/mcuadros/director-engine/ports/taskstore"
	"github.com/mcuadros/director-engine/reducer/closure"
	"github.com/mcuadros/director-engine/reducer/eligibility"
	"github.com/mcuadros/director-engine/reducer/escalation"
	"github.com/mcuadros/director-engine/reducer/launch"
	"github.com/mcuadros/director-engine/reducer/retry"
	"github.com/mcuadros/director-engine/reducer/routing"
)

var (
	identifierPattern           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]*$`)
	shaPattern                  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	ErrProjectLeaseUnavailable  = errors.New("project execution lease is stale or belongs to another engine")
	ErrRepositoryBindingChanged = errors.New("execution repository binding changed")
	ErrRuntimeBudgetLeaseFenced = errors.New(string(runtimebudget.ReasonLeaseFenced))
)

// Controller is restartable: it owns no workflow state outside TaskStore.
type Controller struct {
	store           storeport.TaskStore
	runtime         runtimeport.Port
	helperRuntime   runtimeport.HelperPort
	recoveryRuntime runtimeport.PrimaryRecoveryPort
	host            host.Port
	queue           reconciliationport.Queue
}

// NewController wires the engine-owned ports without importing an adapter.
func NewController(store storeport.TaskStore, runtime runtimeport.Port, hostPort host.Port, queue reconciliationport.Queue) *Controller {
	helperRuntime, _ := runtime.(runtimeport.HelperPort)
	recoveryRuntime, _ := runtime.(runtimeport.PrimaryRecoveryPort)
	return &Controller{store: store, runtime: runtime, helperRuntime: helperRuntime, recoveryRuntime: recoveryRuntime, host: hostPort, queue: queue}
}

// StartCommand freezes every identity and fact needed by one primary Run.
type StartCommand struct {
	RequestID         string
	Scope             domainexecution.Scope
	RunNumber         uint64
	SourcePath        string
	WorktreePath      string
	Branch            string
	BaseSHA           string
	TaskTitle         string
	CriterionIDs      []string
	InitialPrompt     string
	RootWorkspaceID   string
	MCPServer         domainexecution.MCPServerLaunch
	EffectiveProfiles agentprofile.FrozenSet
	BudgetPolicy      runtimebudget.Policy
	TurnBudgetDemand  runtimebudget.Demand
	HelperPolicy      domainexecution.HelperPolicy
	ControlPolicy     domainexecution.ControlPolicy
	RecoveryPolicy    domainexecution.PrimaryRecoveryPolicy
	EligibilityFacts  eligibility.Facts
}

func effectiveRecoveryPolicy(policy domainexecution.PrimaryRecoveryPolicy) domainexecution.PrimaryRecoveryPolicy {
	if policy.SchemaVersion == "" {
		return domainexecution.DefaultPrimaryRecoveryPolicy()
	}
	return policy
}

// StartResult reports the pure eligibility result and the Run identity, if an
// eligible command durably created or adopted it.
type StartResult struct {
	Decision eligibility.Decision
	RunID    string
}

// StepResult exposes only the durable Run after one bounded transition.
type StepResult struct {
	Run        domain.Run
	Progressed bool
}

// BudgetWorkCommand is the engine-owned admission for setup, model, Review,
// correction, Validation, or replacement work. The TaskStore Run version and
// Project lease epoch fence concurrent and stale writers before persistence.
type BudgetWorkCommand struct {
	RequestID          string
	RunID              string
	ExpectedRunVersion uint64
	LeaseEpoch         uint64
	EffectID           string
	Activity           runtimebudget.Activity
	Demand             runtimebudget.Demand
	CandidateSHA       string
	FailureFingerprint string
	NowMillis          int64
}

type BudgetResult struct {
	Run           domain.Run
	Decision      runtimebudget.Decision
	ReservationID string
	Replay        bool
}

type BudgetAcknowledgementCommand struct {
	RequestID          string
	RunID              string
	ExpectedRunVersion uint64
	LeaseEpoch         uint64
	WarningID          string
	PolicyRevision     string
	ActorKind          string
	ActorID            string
	Source             string
	NowMillis          int64
}

func stableID(prefix string, parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return prefix + "-" + hex.EncodeToString(digest[:16])
}

func hashText(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func frozenProfilesHash(state domainexecution.State) string {
	if state.EffectiveProfiles == nil {
		return ""
	}
	return state.EffectiveProfiles.SHA256()
}

func repositoryBinding(workspace domain.Workspace, worktreePath, branch, baseSHA string) domainexecution.RepositoryBinding {
	return domainexecution.RepositoryBinding{
		RepositoryID: workspace.Repository.ID, RepositoryKey: workspace.Repository.Key,
		CanonicalRemote: workspace.Repository.CanonicalRemote, SourcePath: workspace.Repository.SourcePath,
		SourceDevice: workspace.Repository.SourceDevice, SourceInode: workspace.Repository.SourceInode,
		GitCommonDirectory: workspace.Repository.GitCommonDirectory,
		GitCommonDevice:    workspace.Repository.GitCommonDevice, GitCommonInode: workspace.Repository.GitCommonInode,
		WorktreePath: worktreePath, Branch: branch, BaseSHA: baseSHA,
	}
}

func leaseBinding(project domain.Project) domainexecution.LeaseBinding {
	if project.Lease == nil {
		return domainexecution.LeaseBinding{}
	}
	return domainexecution.LeaseBinding{
		HolderInstance:        project.Lease.HolderInstance,
		HolderProcessIdentity: project.Lease.HolderProcessIdentity,
		Epoch:                 project.Lease.Epoch,
	}
}

func currentLease(project domain.Project, binding domainexecution.LeaseBinding, nowMillis int64) bool {
	return domainexecution.ValidLeaseBinding(binding) && project.Lease != nil &&
		project.Lease.DispatchAllowed && nowMillis >= project.Lease.AcquiredAtMillis &&
		project.Lease.ExpiresAtMillis > nowMillis &&
		leaseBinding(project) == binding
}

func exactRepository(run domain.Run, workspace domain.Workspace) bool {
	state := run.Execution
	want := repositoryBinding(workspace, state.WorktreePath, state.Branch, run.BaseSHA)
	return state.SourcePath == workspace.Repository.SourcePath &&
		state.RepositoryBinding == want &&
		state.RepositoryBindingHash != "" &&
		state.RepositoryBindingHash == domainexecution.RepositoryBindingSHA256(want)
}

func (controller *Controller) currentExecutionAuthority(ctx context.Context, run domain.Run, nowMillis int64) error {
	project, err := controller.store.Project(ctx, run.Execution.Scope.ProjectID)
	if err != nil {
		return err
	}
	if !currentLease(project, run.Execution.LeaseBinding, nowMillis) {
		return ErrProjectLeaseUnavailable
	}
	if !runtimebudget.ValidLedgerForLease(run.Execution.Budget, run.Execution.LeaseBinding.Epoch) {
		return ErrRuntimeBudgetLeaseFenced
	}
	workspace, err := controller.store.Workspace(ctx, run.Execution.Scope.WorkspaceID)
	if err != nil {
		return err
	}
	if !exactRepository(run, workspace) {
		return ErrRepositoryBindingChanged
	}
	return nil
}

func newEffect(runID string, kind domainexecution.EffectKind, attempts uint32) domainexecution.Effect {
	return domainexecution.Effect{
		ID: stableID("effect", runID, string(kind)), Kind: kind,
		Phase: domainexecution.EffectIntentRecorded, AttemptLimit: attempts,
	}
}

func setupRequired(surfaces domainexecution.LifecycleSurfaces) bool {
	for _, command := range surfaces.Setup {
		if strings.TrimSpace(command) != "" {
			return true
		}
	}
	return false
}

func latestControlRelaunchBlocked(project domain.Project, runs []domain.Run) bool {
	for _, run := range runs {
		control := run.Execution.Control
		if !run.Execution.Terminal || !control.RelaunchBlocked || control.Phase != domainexecution.ControlCancelled {
			continue
		}
		if control.Intent.Kind == domainexecution.ControlEmergencyStop && project.State == "active" &&
			project.Control.Intent.Kind == domainexecution.ControlResumeProject &&
			project.Control.Phase == domainexecution.ControlComplete && !project.Control.ResumeRequired &&
			project.Control.Generation > control.ProjectGeneration {
			continue
		}
		return true
	}
	return false
}

func validStart(command StartCommand, project domain.Project, workspace domain.Workspace, task domain.Task) bool {
	if len(command.CriterionIDs) == 0 || len(command.CriterionIDs) > 128 {
		return false
	}
	seenCriteria := make(map[string]struct{}, len(command.CriterionIDs))
	for _, criterionID := range command.CriterionIDs {
		if !identifierPattern.MatchString(criterionID) || len(criterionID) > 128 {
			return false
		}
		if _, exists := seenCriteria[criterionID]; exists {
			return false
		}
		seenCriteria[criterionID] = struct{}{}
	}
	controlPolicy := command.ControlPolicy
	if controlPolicy == (domainexecution.ControlPolicy{}) {
		controlPolicy = domainexecution.DefaultControlPolicy()
	}
	recoveryPolicy := effectiveRecoveryPolicy(command.RecoveryPolicy)
	return project.State == "active" && !project.Control.ResumeRequired &&
		project.Organizer != nil && project.Organizer.Phase == domain.OrganizerPhaseActive &&
		project.Lease != nil && project.Lease.DispatchAllowed &&
		command.EligibilityFacts.TaskStoreNowMillis >= project.Lease.AcquiredAtMillis &&
		project.Lease.ExpiresAtMillis > command.EligibilityFacts.TaskStoreNowMillis &&
		command.EffectiveProfiles.OrganizerRevision() == project.Organizer.OrganizerRevision &&
		command.EffectiveProfiles.ConfigurationSHA256() == project.Organizer.ConfigurationSHA256 &&
		identifierPattern.MatchString(command.RequestID) && command.RunNumber > 0 &&
		command.Scope.ProjectID == project.ID && command.Scope.TaskID == task.ID &&
		command.Scope.WorkspaceID == workspace.ID && workspace.ProjectID == project.ID &&
		len(task.WorkspaceIDs) == 1 && task.WorkspaceIDs[0] == workspace.ID &&
		identifierPattern.MatchString(command.Scope.WorkspaceID) && identifierPattern.MatchString(command.Scope.RunID) &&
		command.EligibilityFacts.Scope == command.Scope &&
		command.BaseSHA != "" && shaPattern.MatchString(command.BaseSHA) &&
		command.SourcePath == workspace.Repository.SourcePath && len(command.SourcePath) <= 4_096 && filepath.IsAbs(command.SourcePath) && filepath.Clean(command.SourcePath) == command.SourcePath &&
		len(command.WorktreePath) <= 4_096 && filepath.IsAbs(command.WorktreePath) && filepath.Clean(command.WorktreePath) == command.WorktreePath &&
		command.SourcePath != command.WorktreePath && identifierPattern.MatchString(command.Branch) &&
		!strings.Contains(command.Branch, "..") && !strings.Contains(command.Branch, "//") &&
		command.TaskTitle == task.Title && strings.TrimSpace(command.InitialPrompt) != "" && len(command.InitialPrompt) <= 16*1_024 &&
		command.EffectiveProfiles.Valid() && runtimebudget.ValidPolicy(command.BudgetPolicy) &&
		command.BudgetPolicy.Revision == command.EffectiveProfiles.ConfigurationSHA256() &&
		command.TurnBudgetDemand.WallTimeMilliseconds > 0 && command.TurnBudgetDemand.Tokens > 0 &&
		command.TurnBudgetDemand.Turns == 1 &&
		(command.BudgetPolicy.CostLimitMicrousd == 0 || command.TurnBudgetDemand.CostMicrousd > 0) &&
		domainexecution.ValidHelperPolicy(command.HelperPolicy) && domainexecution.ValidControlPolicy(controlPolicy) &&
		domainexecution.ValidPrimaryRecoveryPolicy(recoveryPolicy)
}

func eventPayload(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal fixed execution event: " + err.Error())
	}
	return encoded
}

func (controller *Controller) persistTaskAttention(
	ctx context.Context,
	task domain.Task,
	requestID string,
	scope domainexecution.Scope,
	code domainexecution.NeedCode,
) error {
	escalated := escalation.Reduce(escalation.Facts{
		SchemaVersion: escalation.SchemaVersion, Scope: scope, CauseCode: code,
		Reconciled: true, WakeCondition: "facts_reconciled_or_human_decision",
	})
	if escalated.Kind != escalation.DecisionNeedsYou {
		return errors.New("eligibility cause was not admitted by escalation reducer")
	}
	if task.Attention != nil && task.Attention.Code == code && !task.Attention.CleanupAuthorized {
		return nil
	}
	next := task
	next.Version++
	next.Attention = &escalated.NeedsYou
	commandID := stableID("command", requestID, "needs-you")
	result, err := controller.store.UpdateTask(ctx, domain.CommandRequest{
		IdempotencyKey: commandID, Type: "task.needs_you", AggregateID: task.ID,
		ExpectedVersion: task.Version,
		Payload: eventPayload(struct {
			Code domainexecution.NeedCode `json:"code"`
		}{code}),
	}, next, domain.Event{
		ID: stableID("event", commandID), Sequence: task.Version + 2,
		AggregateID: task.ID, AggregateVersion: next.Version,
		Type: "task.needs_you", Payload: eventPayload(struct {
			Code              domainexecution.NeedCode `json:"code"`
			CleanupAuthorized bool                     `json:"cleanupAuthorized"`
		}{code, false}),
	})
	if err != nil {
		return err
	}
	if result.Outcome != domain.CommandApplied {
		return errors.New("persist Task attention: version conflict")
	}
	return nil
}

// Start applies the real eligibility reducer before creating any Run,
// worktree, OCI, or host intent.
func (controller *Controller) Start(ctx context.Context, command StartCommand) (StartResult, error) {
	project, err := controller.store.Project(ctx, command.Scope.ProjectID)
	if err != nil {
		return StartResult{}, err
	}
	task, err := controller.store.Task(ctx, command.Scope.TaskID)
	if err != nil {
		return StartResult{}, err
	}
	workspace, err := controller.store.Workspace(ctx, command.Scope.WorkspaceID)
	if err != nil {
		return StartResult{}, err
	}
	if !validStart(command, project, workspace, task) {
		return StartResult{}, errors.New("invalid primary execution start command")
	}
	decision := eligibility.Reduce(command.EligibilityFacts)
	result := StartResult{Decision: decision}
	startCommandID := stableID("command", command.RequestID, "run-create")
	if existing, err := controller.store.Run(ctx, command.Scope.RunID); err == nil {
		controlPolicy := command.ControlPolicy
		if controlPolicy == (domainexecution.ControlPolicy{}) {
			controlPolicy = domainexecution.DefaultControlPolicy()
		}
		recoveryPolicy := effectiveRecoveryPolicy(command.RecoveryPolicy)
		expectedSession, sessionErr := domainexecution.NewPrimarySession(
			command.Scope, stableID("effect", command.Scope.RunID, string(domainexecution.EffectAgentCreate)),
			command.EffectiveProfiles, command.MCPServer,
		)
		if existing.TaskID != task.ID || existing.BaseSHA != command.BaseSHA ||
			existing.Execution.StartCommandID != startCommandID ||
			existing.Execution.EligibilityDecisionID != decision.DecisionID ||
			existing.Execution.EligibilityFactsHash != decision.FactsHash ||
			existing.Execution.EffectiveProfilesSHA256 != command.EffectiveProfiles.SHA256() ||
			existing.Execution.RepositoryBinding != repositoryBinding(workspace, command.WorktreePath, command.Branch, command.BaseSHA) ||
			existing.Execution.LeaseBinding != leaseBinding(project) || sessionErr != nil ||
			existing.Execution.PrimarySession.ReservationSHA256 != expectedSession.ReservationSHA256 ||
			existing.Execution.Budget.Policy != command.BudgetPolicy ||
			existing.Execution.TurnBudgetDemand != command.TurnBudgetDemand ||
			existing.Execution.HelperPolicy != command.HelperPolicy || existing.Execution.ControlPolicy != controlPolicy ||
			existing.Execution.RecoveryPolicy != recoveryPolicy {
			return StartResult{}, errors.New("existing Run conflicts with start command")
		}
		result.Decision.Kind = eligibility.DecisionEligible
		result.RunID = existing.ID
		return result, nil
	} else if !errors.Is(err, storeport.ErrNotFound) {
		return StartResult{}, err
	}
	taskRuns, err := controller.store.Runs(ctx, task.ID)
	if err != nil {
		return StartResult{}, err
	}
	if latestControlRelaunchBlocked(project, taskRuns) {
		return StartResult{}, errors.New("cancelled Task requires an explicit engine-authorized relaunch")
	}
	if decision.Kind == eligibility.DecisionEscalate {
		if err := controller.persistTaskAttention(ctx, task, command.RequestID, command.Scope, domainexecution.NeedCode(decision.Code)); err != nil {
			return StartResult{}, err
		}
		return result, nil
	}
	if decision.Kind != eligibility.DecisionEligible {
		return result, nil
	}
	commandID := startCommandID
	profiles := command.EffectiveProfiles
	repository := repositoryBinding(workspace, command.WorktreePath, command.Branch, command.BaseSHA)
	repositoryHash := domainexecution.RepositoryBindingSHA256(repository)
	primarySession, err := domainexecution.NewPrimarySession(
		command.Scope, stableID("effect", command.Scope.RunID, string(domainexecution.EffectAgentCreate)),
		profiles, command.MCPServer,
	)
	if err != nil || repositoryHash == "" {
		return StartResult{}, errors.New("primary execution binding is invalid")
	}
	budget, err := runtimebudget.NewLedger(command.BudgetPolicy, command.EligibilityFacts.TaskStoreNowMillis)
	if err != nil {
		return StartResult{}, errors.New("primary execution budget is invalid")
	}
	state := domainexecution.State{
		SchemaVersion: domainexecution.SchemaVersion, Scope: command.Scope,
		StartCommandID:             commandID,
		EligibilityDecisionVersion: eligibility.SchemaVersion,
		EligibilityDecisionID:      decision.DecisionID, EligibilityFactsHash: decision.FactsHash,
		CapacityReservationID: stableID("capacity", command.Scope.RunID),
		BudgetReservationID:   stableID("budget", command.Scope.RunID),
		LeaseBinding:          leaseBinding(project),
		RepositoryBinding:     repository, RepositoryBindingHash: repositoryHash,
		LifecycleDigest: decision.LifecycleDigest, IsolationDigest: decision.IsolationDigest,
		EffectiveProfiles:       &profiles,
		EffectiveProfilesSHA256: command.EffectiveProfiles.SHA256(),
		LifecycleApproval:       command.EligibilityFacts.LifecycleApproval,
		Isolation:               command.EligibilityFacts.Isolation,
		OperationalPolicy:       command.EligibilityFacts.OperationalPolicy,
		Budget:                  budget,
		TurnBudgetDemand:        command.TurnBudgetDemand,
		ControlPolicy: func() domainexecution.ControlPolicy {
			if command.ControlPolicy == (domainexecution.ControlPolicy{}) {
				return domainexecution.DefaultControlPolicy()
			}
			return command.ControlPolicy
		}(),
		RecoveryPolicy:    effectiveRecoveryPolicy(command.RecoveryPolicy),
		LifecycleSurfaces: command.EligibilityFacts.LifecycleSurfaces,
		SourcePath:        command.SourcePath, WorktreePath: command.WorktreePath,
		Branch: command.Branch, TaskTitle: command.TaskTitle,
		RootWorkspaceID: command.RootWorkspaceID,
		CriterionIDs:    append([]string(nil), command.CriterionIDs...),
		InitialPrompt:   command.InitialPrompt, InitialPromptHash: hashText(command.InitialPrompt),
		PrimarySession:                   primarySession,
		HelperPolicy:                     command.HelperPolicy,
		Worktree:                         newEffect(command.Scope.RunID, domainexecution.EffectWorktreeCreate, 2),
		HostView:                         newEffect(command.Scope.RunID, domainexecution.EffectHostViewCreate, 2),
		Boundary:                         newEffect(command.Scope.RunID, domainexecution.EffectBoundaryMaterialize, 2),
		OperationalObservation:           &command.EligibilityFacts.OperationalObservation,
		OperationalObservationRunVersion: 0,
		FakeTerminalRung:                 true,
	}
	if setupRequired(state.LifecycleSurfaces) {
		state.Setup = newEffect(command.Scope.RunID, domainexecution.EffectSetupRun, 2)
	}
	state.PreparationPlan = domainexecution.NewM1PreparationPlan(
		command.Scope,
		hashText(strings.Join([]string{
			state.RepositoryBindingHash, state.EligibilityFactsHash,
			state.LifecycleDigest, state.IsolationDigest, state.EffectiveProfilesSHA256,
			state.PrimarySession.ReservationSHA256, state.InitialPromptHash,
			hashText(string(eventPayload(state.Budget.Policy))), hashText(string(eventPayload(state.TurnBudgetDemand))),
			hashText(string(eventPayload(state.ControlPolicy))),
			hashText(string(eventPayload(state.RecoveryPolicy))),
			strings.Join(state.CriterionIDs, "\x1e"),
		}, "\x1f")),
	)
	run := domain.Run{
		ID: command.Scope.RunID, TaskID: task.ID, Number: command.RunNumber,
		BaseSHA: command.BaseSHA, Execution: state,
	}
	storeResult, err := controller.store.CreateRun(ctx, domain.CommandRequest{
		IdempotencyKey: commandID, Type: "run.primary_execution.create", AggregateID: run.ID,
		Payload: eventPayload(struct {
			EligibilityDecisionID   string `json:"eligibilityDecisionId"`
			LifecycleDigest         string `json:"lifecycleDigest"`
			IsolationDigest         string `json:"isolationDigest"`
			EffectiveProfilesSHA256 string `json:"effectiveProfilesSha256"`
			RepositoryBindingSHA256 string `json:"repositoryBindingSha256"`
			PrimarySessionSHA256    string `json:"primarySessionSha256"`
			BudgetPolicySHA256      string `json:"budgetPolicySha256"`
			ControlPolicySHA256     string `json:"controlPolicySha256"`
			RecoveryPolicySHA256    string `json:"recoveryPolicySha256"`
			LeaseEpoch              uint64 `json:"leaseEpoch"`
		}{decision.DecisionID, decision.LifecycleDigest, decision.IsolationDigest, state.EffectiveProfilesSHA256,
			state.RepositoryBindingHash, state.PrimarySession.ReservationSHA256,
			hashText(string(eventPayload(state.Budget.Policy))), hashText(string(eventPayload(state.ControlPolicy))),
			hashText(string(eventPayload(state.RecoveryPolicy))),
			state.LeaseBinding.Epoch}),
	}, run, domain.Event{
		ID: stableID("event", commandID), RunID: run.ID, Sequence: 1,
		AggregateID: run.ID, AggregateVersion: 0, Type: "run.primary_execution.created",
		Payload: eventPayload(struct {
			EligibilityDecisionID   string `json:"eligibilityDecisionId"`
			EffectiveProfilesSHA256 string `json:"effectiveProfilesSha256"`
			RepositoryBindingSHA256 string `json:"repositoryBindingSha256"`
			PrimarySessionSHA256    string `json:"primarySessionSha256"`
			BudgetPolicySHA256      string `json:"budgetPolicySha256"`
			ControlPolicySHA256     string `json:"controlPolicySha256"`
			RecoveryPolicySHA256    string `json:"recoveryPolicySha256"`
			LeaseEpoch              uint64 `json:"leaseEpoch"`
		}{decision.DecisionID, state.EffectiveProfilesSHA256, state.RepositoryBindingHash,
			state.PrimarySession.ReservationSHA256, hashText(string(eventPayload(state.Budget.Policy))),
			hashText(string(eventPayload(state.ControlPolicy))), hashText(string(eventPayload(state.RecoveryPolicy))),
			state.LeaseBinding.Epoch}),
	})
	if err != nil {
		return StartResult{}, err
	}
	if storeResult.Outcome != domain.CommandApplied {
		return StartResult{}, errors.New("create fake execution Run: version conflict")
	}
	result.RunID = run.ID
	return result, nil
}

func cloneClaim(claim domainexecution.CompletedClaim) domainexecution.CompletedClaim {
	claim.CriteriaResults = mapsClone(claim.CriteriaResults)
	claim.ResidualRiskCodes = append([]string(nil), claim.ResidualRiskCodes...)
	return claim
}

func mapsClone(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func sameClaim(left, right domainexecution.CompletedClaim) bool {
	if left.ID != right.ID || left.SchemaVersion != right.SchemaVersion || left.Outcome != right.Outcome ||
		left.AgentID != right.AgentID || left.CandidateSHA != right.CandidateSHA || left.BaseSHA != right.BaseSHA ||
		len(left.CriteriaResults) != len(right.CriteriaResults) || len(left.ResidualRiskCodes) != len(right.ResidualRiskCodes) {
		return false
	}
	for key, value := range left.CriteriaResults {
		if right.CriteriaResults[key] != value {
			return false
		}
	}
	for index, value := range left.ResidualRiskCodes {
		if right.ResidualRiskCodes[index] != value {
			return false
		}
	}
	return true
}

func allCriteriaClaimedSatisfied(claim *domainexecution.CompletedClaim) bool {
	if claim == nil || len(claim.CriteriaResults) == 0 {
		return false
	}
	for _, result := range claim.CriteriaResults {
		if result != "claimed_satisfied" {
			return false
		}
	}
	return true
}

func validClaim(claim domainexecution.CompletedClaim, run domain.Run) bool {
	if !identifierPattern.MatchString(claim.ID) ||
		claim.SchemaVersion != "director.agent-outcome.completed/v1" || claim.Outcome != "completed" ||
		run.Execution.AgentPrompt.Phase != domainexecution.EffectComplete ||
		claim.AgentID != run.Execution.Agent.ExternalID || !shaPattern.MatchString(claim.CandidateSHA) ||
		claim.BaseSHA != run.BaseSHA || len(claim.CriteriaResults) == 0 || len(claim.CriteriaResults) > 128 ||
		len(claim.ResidualRiskCodes) > 32 {
		return false
	}
	if len(claim.CriteriaResults) != len(run.Execution.CriterionIDs) {
		return false
	}
	for _, criterionID := range run.Execution.CriterionIDs {
		if _, exists := claim.CriteriaResults[criterionID]; !exists {
			return false
		}
	}
	for id, result := range claim.CriteriaResults {
		if !identifierPattern.MatchString(id) || (result != "claimed_satisfied" && result != "claimed_unsatisfied") {
			return false
		}
	}
	seen := make(map[string]struct{}, len(claim.ResidualRiskCodes))
	for _, code := range claim.ResidualRiskCodes {
		if !identifierPattern.MatchString(code) || len(code) > 64 {
			return false
		}
		if _, exists := seen[code]; exists {
			return false
		}
		seen[code] = struct{}{}
	}
	return true
}

// RecordCompletedClaim persists one closed claim without admitting a Candidate
// or changing the execution route.
func (controller *Controller) RecordCompletedClaim(ctx context.Context, runID string, claim domainexecution.CompletedClaim) error {
	run, err := controller.store.Run(ctx, runID)
	if err != nil {
		return err
	}
	if !validClaim(claim, run) {
		return errors.New("completed AgentOutcomeClaim is invalid")
	}
	if run.Execution.Claim != nil {
		if sameClaim(*run.Execution.Claim, claim) {
			return nil
		}
		return errors.New("completed AgentOutcomeClaim identity is immutable")
	}
	next := run
	cloned := cloneClaim(claim)
	next.Execution.Claim = &cloned
	next.Execution.CandidateObservation = nil
	next.Execution.OperationalObservationConsumed = true
	return controller.persistRun(ctx, run, next, "agent.claim.recorded")
}

// RecordCompletionEvent durably deduplicates one daemon terminal callback and
// synchronously enqueues coordinator reconciliation. The callback is only a
// wake signal; the reconciler must freshly observe the terminal host state.
func (controller *Controller) RecordCompletionEvent(
	ctx context.Context, runID string, event domainexecution.CompletionEvent, nowMillis int64,
) error {
	run, err := controller.store.Run(ctx, runID)
	if err != nil {
		return err
	}
	receiptIndex := -1
	for index := range run.Execution.CompletionEventReceipts {
		receipt := run.Execution.CompletionEventReceipts[index]
		if receipt.EventID != event.ID {
			continue
		}
		if receipt.EventFactHash != event.FactHash || receipt.Event != event {
			return errors.New("completion event identity was reused with conflicting facts")
		}
		if receipt.Phase == domainexecution.CompletionReceiptComplete {
			return nil
		}
		receiptIndex = index
		break
	}
	if receiptIndex < 0 {
		effect := notifiedEffectPointer(&run.Execution, event.DispatchEffectID)
		if effect == nil || effect.ID != event.DispatchEffectID || effect.Observation == nil ||
			effect.Observation.Status != domainexecution.ObservationOwnedPresent {
			return errors.New("completion event is not bound to a pending notified turn")
		}
		agentID := effect.Observation.ExternalID
		if effect.ID == run.Execution.Agent.ID || effect.ID == run.Execution.AgentPrompt.ID {
			agentID = run.Execution.Agent.ExternalID
			if agentID == "" {
				agentID = effect.Observation.ExternalID
			}
		}
		admission := domainexecution.AdmitCompletionEvent(
			event, agentID, run.Execution.RepositoryBindingHash,
			run.Execution.CompletionEventCursor, nowMillis,
		)
		if admission.Kind != domainexecution.AdmissionAllow {
			return fmt.Errorf("completion event refused: %s", admission.Code)
		}
		if len(run.Execution.CompletionEventReceipts) >= domainexecution.MaximumCompletionEventReceipts {
			return fmt.Errorf("completion event refused: %s", domainexecution.NeedCompletionLedgerFull)
		}
		next := run
		recorded := event
		next.Execution.LastCompletionEvent = &recorded
		next.Execution.CompletionEventCursor = event.Cursor
		next.Execution.CompletionEventReceipts = append(next.Execution.CompletionEventReceipts, domainexecution.CompletionEventReceipt{
			EventID: event.ID, EventFactHash: event.FactHash, Event: event, Cursor: event.Cursor,
			Phase: domainexecution.CompletionReceiptIntent,
		})
		for index := range next.Execution.Control.Targets {
			target := &next.Execution.Control.Targets[index]
			if target.Identity.ID != event.AgentID {
				continue
			}
			target.TerminalEventID = event.ID
			target.TerminalEventHash = event.FactHash
			target.TerminalCursor = event.Cursor
			if target.Boundary.Observation != nil && target.Boundary.Observation.Status == domainexecution.ObservationOwnedPresent {
				target.Boundary.Observation = nil
			}
			if target.Archive.Observation != nil && target.Archive.Observation.Status == domainexecution.ObservationOwnedPresent {
				target.Archive.Observation = nil
			}
		}
		pending := notifiedEffectPointer(&next.Execution, event.DispatchEffectID)
		pending.Observation = nil
		next.Execution.OperationalObservationConsumed = true
		if err := controller.persistRun(ctx, run, next, "agent.terminal_event.recorded"); err != nil {
			return err
		}
		next.Version = run.Version + 1
		run = next
		receiptIndex = len(run.Execution.CompletionEventReceipts) - 1
	}
	request := reconciliationport.Request{
		EventID: event.ID, EventFactHash: event.FactHash, RunID: run.ID,
		AgentID: event.AgentID, DispatchEffectID: event.DispatchEffectID, Kind: event.Kind,
		ReceivedAtMillis: event.ObservedAtMillis,
		DeadlineAtMillis: event.ObservedAtMillis + domainexecution.CompletionDispatchTargetMillis,
	}
	if controller.queue == nil {
		return errors.New("coordinator reconciliation queue is unavailable")
	}
	receipt, err := controller.queue.Enqueue(ctx, request)
	if err != nil {
		return fmt.Errorf("enqueue coordinator reconciliation: %w", err)
	}
	if err := reconciliationport.ValidateReceipt(request, receipt); err != nil {
		return err
	}
	next := run
	next.Execution.CompletionEventReceipts[receiptIndex].Phase = domainexecution.CompletionReceiptComplete
	next.Execution.CompletionEventReceipts[receiptIndex].EnqueuedAtMillis = receipt.EnqueuedAtMillis
	next.Execution.CompletionEventReceipts[receiptIndex].DispatchLatencyMillis = receipt.EnqueuedAtMillis - event.ObservedAtMillis
	return controller.persistRun(ctx, run, next, "agent.reconciliation_enqueued")
}

// RecoverLostCompletionEvent is the PLAN watchdog's only launch wake path. It
// accepts the full multi-source five-minute stall proof and merely invalidates
// the replaceable host sample so the next reconciliation freshly observes it.
func (controller *Controller) RecoverLostCompletionEvent(
	ctx context.Context, runID string, facts domainexecution.StallRecoveryFacts,
) error {
	run, err := controller.store.Run(ctx, runID)
	if err != nil {
		return err
	}
	kind := domainexecution.EffectKind("")
	if effect := notifiedEffectPointer(&run.Execution, facts.PendingTerminalEffectID); effect != nil &&
		effect.Observation != nil && effect.Observation.Status == domainexecution.ObservationOwnedPresent {
		kind = effect.Kind
	}
	if kind == "" {
		return fmt.Errorf("lost-event recovery refused: %s", domainexecution.NeedStallRecoveryInvalid)
	}
	admission := domainexecution.AdmitLostCompletionEventRecovery(facts, facts.PendingTerminalEffectID)
	if admission.Kind != domainexecution.AdmissionAllow {
		return fmt.Errorf("lost-event recovery refused: %s", admission.Code)
	}
	next := run
	notifiedEffectPointer(&next.Execution, facts.PendingTerminalEffectID).Observation = nil
	next.Execution.OperationalObservationConsumed = true
	return controller.persistRun(ctx, run, next, "agent.lost_event_recovery")
}

// dispatchEffectIDKind returns the durable notified effect for one callback.
func completionEffectKind(event domainexecution.CompletionEvent, state domainexecution.State) domainexecution.EffectKind {
	if effect := notifiedEffectPointer(&state, event.DispatchEffectID); effect != nil {
		return effect.Kind
	}
	return ""
}

func notifiedEffectPointer(state *domainexecution.State, effectID string) *domainexecution.Effect {
	for _, effect := range []*domainexecution.Effect{
		&state.Agent, &state.AgentPrompt,
		&state.PrimaryRecovery.ReplacementAgent, &state.PrimaryRecovery.ReplacementPrompt,
	} {
		if effect.ID == effectID {
			return effect
		}
	}
	return nil
}

func (controller *Controller) persistRunTransition(ctx context.Context, current, next domain.Run, transition string) (bool, error) {
	next.Version = current.Version + 1
	stateHash := hashState(next.Execution)
	commandID := stableID("command", current.ID, fmt.Sprintf("version-%d", next.Version), transition)
	result, err := controller.store.UpdateRun(ctx, domain.CommandRequest{
		IdempotencyKey: commandID, Type: transition, AggregateID: current.ID,
		ExpectedVersion: current.Version,
		Payload: eventPayload(struct {
			Transition string `json:"transition"`
			StateHash  string `json:"stateHash"`
		}{transition, stateHash}),
	}, next, domain.Event{
		ID: stableID("event", commandID), RunID: current.ID, Sequence: current.Version + 2,
		AggregateID: current.ID, AggregateVersion: next.Version, Type: transition,
		Payload: durableTransitionPayload(transition, next.Execution),
	})
	if err != nil {
		return false, err
	}
	if result.Outcome != domain.CommandApplied {
		return false, errors.New("persist execution transition: version conflict")
	}
	return !result.Replay, nil
}

func (controller *Controller) persistRun(ctx context.Context, current, next domain.Run, transition string) error {
	_, err := controller.persistRunTransition(ctx, current, next, transition)
	return err
}

func hashState(state domainexecution.State) string {
	encoded, err := json.Marshal(state)
	if err != nil {
		panic("marshal execution state: " + err.Error())
	}
	return hashText(string(encoded))
}

func currentEffectObservation(state domainexecution.State) *domainexecution.EffectObservation {
	for _, effect := range []domainexecution.Effect{
		state.Worktree, state.HostView, state.Boundary, state.Setup, state.Agent, state.AgentPrompt,
		state.AgentArchive, state.HostViewArchive, state.WorktreeRemove,
		state.PrimaryRecovery.Observe, state.PrimaryRecovery.Archive,
		state.PrimaryRecovery.ReplacementAgent, state.PrimaryRecovery.ReplacementPrompt,
	} {
		if effect.Observation != nil {
			observation := *effect.Observation
			return &observation
		}
	}
	for _, target := range state.Control.Targets {
		for _, effect := range []domainexecution.Effect{target.Boundary, target.Archive} {
			if effect.Observation != nil {
				observation := *effect.Observation
				return &observation
			}
		}
	}
	if state.Control.Recovery.Snapshot.Observation != nil {
		observation := *state.Control.Recovery.Snapshot.Observation
		return &observation
	}
	if state.PrimaryRecovery.Observation != nil {
		observation := state.PrimaryRecovery.Observation.Host
		return &observation
	}
	return nil
}

func durableTransitionPayload(transition string, state domainexecution.State) json.RawMessage {
	payload := struct {
		Transition             string                                  `json:"transition"`
		StateHash              string                                  `json:"stateHash"`
		EffectObservation      *domainexecution.EffectObservation      `json:"effectObservation,omitempty"`
		OperationalObservation *domainexecution.OperationalObservation `json:"operationalObservation,omitempty"`
		Claim                  *domainexecution.CompletedClaim         `json:"claim,omitempty"`
		NeedsYou               *domainexecution.NeedsYou               `json:"needsYou,omitempty"`
		CompletionReceipt      *domainexecution.CompletionEventReceipt `json:"completionReceipt,omitempty"`
		PrimaryRecovery        *domainexecution.PrimaryRecovery        `json:"primaryRecovery,omitempty"`
	}{
		Transition: transition, StateHash: hashState(state),
		EffectObservation: currentEffectObservation(state),
		NeedsYou:          state.NeedsYou,
	}
	if transition == "run.operational_observed" {
		payload.OperationalObservation = state.OperationalObservation
	}
	if transition == "agent.claim.recorded" {
		payload.Claim = state.Claim
	}
	if (transition == "agent.terminal_event.recorded" || transition == "agent.reconciliation_enqueued") &&
		len(state.CompletionEventReceipts) > 0 {
		receipt := state.CompletionEventReceipts[len(state.CompletionEventReceipts)-1]
		payload.CompletionReceipt = &receipt
	}
	if strings.HasPrefix(transition, "run.primary_recovery_") {
		payload.PrimaryRecovery = &state.PrimaryRecovery
	}
	return eventPayload(payload)
}

func effectPointer(state *domainexecution.State, kind domainexecution.EffectKind) *domainexecution.Effect {
	switch kind {
	case domainexecution.EffectWorktreeCreate:
		return &state.Worktree
	case domainexecution.EffectHostViewCreate:
		return &state.HostView
	case domainexecution.EffectBoundaryMaterialize:
		return &state.Boundary
	case domainexecution.EffectSetupRun:
		return &state.Setup
	case domainexecution.EffectAgentCreate:
		return &state.Agent
	case domainexecution.EffectAgentPrompt:
		return &state.AgentPrompt
	case domainexecution.EffectAgentArchive:
		return &state.AgentArchive
	case domainexecution.EffectHostViewArchive:
		return &state.HostViewArchive
	case domainexecution.EffectWorktreeRemove:
		return &state.WorktreeRemove
	default:
		return nil
	}
}

func effectBindingExact(effect domainexecution.Effect, bindingHash string) bool {
	return effect.Observation == nil || effect.Observation.BindingHash == bindingHash
}

func launchBindingsExact(state domainexecution.State) bool {
	for _, effect := range []domainexecution.Effect{
		state.Worktree, state.HostView, state.Boundary, state.Setup, state.Agent, state.AgentPrompt,
	} {
		if effect.ID != "" && !effectBindingExact(effect, state.RepositoryBindingHash) {
			return false
		}
	}
	return true
}

func cleanupBindingsExact(state domainexecution.State) bool {
	for _, effect := range []domainexecution.Effect{
		state.AgentArchive, state.HostViewArchive, state.WorktreeRemove,
	} {
		if effect.ID != "" && !effectBindingExact(effect, state.RepositoryBindingHash) {
			return false
		}
	}
	return true
}

func preparationBarrier(state domainexecution.State) string {
	setupHash := hashText("no_lifecycle_setup")
	if state.Setup.ID != "" {
		setupHash = state.Setup.ExternalID
	}
	barrier, ok := domainexecution.PreparationBarrier(state.PreparationPlan, domainexecution.PreparationOutputs{
		FrozenInputsHash: hashText(strings.Join([]string{
			state.RepositoryBindingHash, frozenProfilesHash(state), func() string {
				if state.PrimaryRecovery.SchemaVersion != "" {
					return state.PrimaryRecovery.OriginalSession.ReservationSHA256
				}
				return state.PrimarySession.ReservationSHA256
			}(),
			hashText(string(eventPayload(state.Budget.Policy))), hashText(string(eventPayload(state.TurnBudgetDemand))),
			hashText(string(eventPayload(state.ControlPolicy))),
			hashText(string(eventPayload(state.RecoveryPolicy))),
		}, "\x1f")),
		EligibilityHash:       state.EligibilityFactsHash,
		SecurityAdmissionHash: hashText(state.LifecycleDigest + "\x1f" + state.IsolationDigest),
		WorktreeHash:          state.Worktree.ExternalID,
		HostViewHash:          state.HostView.ExternalID,
		IsolationHash:         state.Boundary.ExternalID,
		ToolingHash:           hashText(state.Isolation.Runtime),
		DependenciesHash:      setupHash,
		ContextHash:           hashText(state.InitialPromptHash + "\x1f" + strings.Join(state.CriterionIDs, "\x1e")),
	})
	if !ok {
		return ""
	}
	return barrier
}

func isHostEffect(kind domainexecution.EffectKind) bool {
	switch kind {
	case domainexecution.EffectHostViewCreate, domainexecution.EffectAgentCreate, domainexecution.EffectAgentPrompt,
		domainexecution.EffectAgentArchive, domainexecution.EffectHostViewArchive:
		return true
	default:
		return false
	}
}

func hostCapability(kind domainexecution.EffectKind, observe bool) host.Capability {
	switch kind {
	case domainexecution.EffectHostViewCreate:
		if observe {
			return host.CapabilityWorkspaceObserve
		}
		return host.CapabilityWorkspaceCreate
	case domainexecution.EffectAgentCreate:
		if observe {
			return host.CapabilityAgentObserve
		}
		return host.CapabilityTaskAgentCreate
	case domainexecution.EffectAgentPrompt:
		if observe {
			return host.CapabilityAgentObserve
		}
		return host.CapabilityAgentPrompt
	case domainexecution.EffectAgentArchive:
		if observe {
			return host.CapabilityAgentObserve
		}
		return host.CapabilityAgentArchive
	case domainexecution.EffectHostViewArchive:
		if observe {
			return host.CapabilityWorkspaceObserve
		}
		return host.CapabilityWorkspaceArchive
	default:
		return ""
	}
}

func hostArguments(run domain.Run, effect domainexecution.Effect) host.Arguments {
	state := run.Execution
	profile := &host.AgentProfile{
		Provider: string(state.PrimarySession.Provider), Model: state.PrimarySession.Model,
		Effort: state.PrimarySession.Effort, Mode: state.PrimarySession.Mode,
		PermissionMode: state.PrimarySession.PermissionMode,
		SHA256:         state.EffectiveProfilesSHA256,
	}
	profile.ProviderOptions = make([]host.ProviderOption, 0, len(state.PrimarySession.ProviderOptions))
	for _, option := range state.PrimarySession.ProviderOptions {
		profile.ProviderOptions = append(profile.ProviderOptions, host.ProviderOption{Name: string(option.Name), Value: option.Value})
	}
	session := &host.MCPSession{
		ContractVersion: state.PrimarySession.MCPContractVersion,
		ContractHash:    state.PrimarySession.MCPContractSHA256,
		SessionSHA256:   state.PrimarySession.ReservationSHA256,
		Role:            string(state.PrimarySession.Role), Provider: string(state.PrimarySession.Provider),
		Model: state.PrimarySession.Model, Tools: append([]string(nil), state.PrimarySession.MCPTools...),
		Server: host.MCPServer{
			Name: state.PrimarySession.MCPServer.Name, Command: state.PrimarySession.MCPServer.Command,
			Args: append([]string(nil), state.PrimarySession.MCPServer.Args...),
			Env:  mapsClone(state.PrimarySession.MCPServer.Env),
		},
	}
	operationalObservationID := ""
	if state.OperationalObservation != nil {
		operationalObservationID = state.OperationalObservation.ID
	}
	arguments := host.Arguments{
		Scope: state.Scope, EffectKind: effect.Kind, EffectID: effect.ID,
		WorktreeID: state.Worktree.ExternalID, WorktreePath: state.WorktreePath,
		WorkspaceID: state.HostView.ExternalID, AgentID: state.Agent.ExternalID,
		Title:         state.TaskTitle,
		ParentAgentID: nil, LifecycleDigest: state.LifecycleDigest,
		IsolationDigest: state.IsolationDigest, PreparationReady: state.PreparationReady,
		PreparationBarrierHash: state.PreparationBarrierHash,
		ClientMessageID:        stableID("message", effect.ID), BoundaryID: state.Boundary.ExternalID,
		OperationalObservationID: operationalObservationID,
		Profile:                  profile, Session: session, BindingHash: state.RepositoryBindingHash,
	}
	if effect.Kind == domainexecution.EffectAgentCreate {
		arguments.InitialPrompt = host.ZeroWorkBootstrapPrompt
	}
	if effect.Kind == domainexecution.EffectAgentPrompt {
		arguments.InitialPrompt = state.InitialPrompt
		arguments.NotifyOnFinish = true
		arguments.SessionBindingSHA256 = state.PrimarySession.BindingSHA256
	}
	// The agent-create command carries the frozen registry labels so the host
	// creates a worker that is already discoverable from its root workspace.
	// The launch reducer has already refused this effect without them.
	if effect.Kind == domainexecution.EffectAgentCreate && state.WorkerVisibility != nil {
		registration, err := host.RegistrationFromVisibility(state.Scope, *state.WorkerVisibility)
		if err == nil {
			if labels, labelErr := host.WorkerLabels(registration); labelErr == nil {
				arguments.Labels = labels
			}
		}
	}
	return arguments
}

func hostResumeCursor(state domainexecution.State) uint64 {
	var cursor uint64
	if state.LastStartupReconciliation != nil {
		cursor = state.LastStartupReconciliation.HostCursor
	}
	for _, effect := range []domainexecution.Effect{
		state.HostView, state.Agent, state.AgentPrompt, state.AgentArchive, state.HostViewArchive,
		state.PrimaryRecovery.Observe, state.PrimaryRecovery.Archive,
		state.PrimaryRecovery.ReplacementAgent, state.PrimaryRecovery.ReplacementPrompt,
	} {
		if effect.Observation != nil && effect.Observation.Cursor > cursor {
			cursor = effect.Observation.Cursor
		}
	}
	for _, target := range state.Control.Targets {
		for _, effect := range []domainexecution.Effect{target.Boundary, target.Archive} {
			if effect.Observation != nil && effect.Observation.Cursor > cursor {
				cursor = effect.Observation.Cursor
			}
		}
	}
	return cursor
}

func runtimeRequest(run domain.Run, effect domainexecution.Effect) runtimeport.Request {
	state := run.Execution
	recoveryPaths := make([]string, 0, len(state.Helpers))
	for _, helper := range state.Helpers {
		if helper.Mode == domainexecution.HelperWriter && helper.Phase != domainexecution.HelperTerminal && helper.WorktreePath != "" {
			recoveryPaths = append(recoveryPaths, helper.WorktreePath)
		}
	}
	sort.Strings(recoveryPaths)
	return runtimeport.Request{
		Scope: state.Scope, Effect: effect, LeaseBinding: state.LeaseBinding,
		Repository: state.RepositoryBinding, SourcePath: state.SourcePath,
		WorktreePath: state.WorktreePath, Branch: state.Branch, BaseSHA: run.BaseSHA,
		WorktreeID:        state.Worktree.ExternalID,
		BindingHash:       state.RepositoryBindingHash,
		LifecycleSurfaces: state.LifecycleSurfaces,
		LifecycleApproval: state.LifecycleApproval,
		LifecycleDigest:   state.LifecycleDigest,
		Isolation:         state.Isolation, IsolationDigest: state.IsolationDigest,
		RecoveryWorktreePaths: recoveryPaths, ControlRecoveryArtifactID: state.Control.Recovery.ArtifactID,
		ControlCleanupAuthorized: state.Control.Recovery.CleanupAuthorized,
	}
}

func (controller *Controller) verifyHost(ctx context.Context) error {
	descriptor, err := controller.host.Describe(ctx)
	if err != nil {
		return err
	}
	return host.ValidateDescriptor(descriptor)
}

func (controller *Controller) observeEffectAfter(ctx context.Context, run domain.Run, kind domainexecution.EffectKind, afterCursor uint64) (domainexecution.EffectObservation, error) {
	effect := effectPointer(&run.Execution, kind)
	if effect == nil {
		return domainexecution.EffectObservation{}, errors.New("unknown execution effect")
	}
	if isHostEffect(kind) {
		if err := controller.verifyHost(ctx); err != nil {
			return domainexecution.EffectObservation{}, err
		}
		requestID := stableID("request", effect.ID, "observe")
		command := host.Command{
			RequestID:       requestID,
			IdempotencyKey:  stableID("idempotency", effect.ID, "observe"),
			ExpectedVersion: run.Version, Capability: hostCapability(kind, true),
			AfterCursor: afterCursor, Arguments: hostArguments(run, *effect),
		}
		observed, err := controller.host.Invoke(ctx, command)
		if err != nil {
			return domainexecution.EffectObservation{}, err
		}
		if err := host.ValidateObservation(command, observed); err != nil {
			return domainexecution.EffectObservation{}, err
		}
		observedAt, err := time.Parse(time.RFC3339Nano, observed.ObservedAt)
		if err != nil {
			return domainexecution.EffectObservation{}, errors.New("host observation time is invalid")
		}
		normalized := domainexecution.EffectObservation{
			ID:       stableID("host-observation", observed.RequestID, fmt.Sprintf("cursor-%d", observed.Cursor)),
			EffectID: observed.Result.EffectID, Status: observed.Result.Status,
			ExternalID: observed.Result.ExternalID, BindingHash: observed.Result.BindingHash,
			CorrelationHash: observed.Result.CorrelationHash,
			Cursor:          observed.Cursor, ObservedAt: observed.ObservedAt,
			ObservedAtMillis: observedAt.UnixMilli(), MaximumAgeMillis: observed.Result.MaximumAgeMillis,
			PriorDispatcherAbsent: observed.Result.PriorDispatcherAbsent,
			Usage:                 observed.Result.Usage,
		}
		normalized.FactHash = domainexecution.EffectObservationHash(normalized)
		return normalized, nil
	}
	return controller.runtime.ObserveEffect(ctx, runtimeRequest(run, *effect))
}

func (controller *Controller) observeEffect(ctx context.Context, run domain.Run, kind domainexecution.EffectKind) (domainexecution.EffectObservation, error) {
	return controller.observeEffectAfter(ctx, run, kind, hostResumeCursor(run.Execution))
}

func (controller *Controller) dispatchEffect(ctx context.Context, run domain.Run, kind domainexecution.EffectKind) error {
	effect := effectPointer(&run.Execution, kind)
	if effect == nil {
		return errors.New("unknown execution effect")
	}
	if isHostEffect(kind) {
		if err := controller.verifyHost(ctx); err != nil {
			return err
		}
		command := host.Command{
			RequestID:      stableID("request", effect.ID, fmt.Sprintf("attempt-%d", effect.Attempt)),
			IdempotencyKey: effect.ID, ExpectedVersion: run.Version,
			Capability: hostCapability(kind, false), Arguments: hostArguments(run, *effect),
		}
		if kind == domainexecution.EffectAgentCreate {
			if run.Execution.WorkerVisibility == nil {
				return errors.New("worker launch registration is missing")
			}
			registration, err := host.RegistrationFromVisibility(run.Execution.Scope, *run.Execution.WorkerVisibility)
			if err != nil {
				return err
			}
			if err := host.AdmitAgentCreate(command, registration); err != nil {
				return err
			}
		}
		if kind == domainexecution.EffectAgentPrompt {
			if run.Execution.WorkerVisibility == nil {
				return errors.New("worker launch registration is missing")
			}
			registration, err := host.RegistrationFromVisibility(run.Execution.Scope, *run.Execution.WorkerVisibility)
			if err != nil {
				return err
			}
			if err := host.AdmitAgentPrompt(command, registration, run.Execution.Agent.ExternalID); err != nil {
				return err
			}
		}
		_, err := controller.host.Invoke(ctx, command)
		return err
	}
	return controller.runtime.DispatchEffect(ctx, runtimeRequest(run, *effect))
}

func operationalResult(run domain.Run, nowMillis int64) domainexecution.Admission {
	state := run.Execution
	if state.OperationalObservation == nil || state.OperationalObservationConsumed ||
		state.OperationalObservationRunVersion != run.Version {
		return domainexecution.Admission{Kind: domainexecution.AdmissionPark, Code: domainexecution.NeedOperationalFactMissing}
	}
	return domainexecution.EvaluateOperationalLimits(state.OperationalPolicy, *state.OperationalObservation, nowMillis)
}

func (controller *Controller) observeOperational(ctx context.Context, run domain.Run) error {
	observation, err := controller.runtime.ObserveOperational(ctx, run.Execution.Scope, run.Execution.OperationalPolicy)
	if err != nil {
		return controller.parkRun(ctx, run, domainexecution.NeedOperationalFactMissing)
	}
	next := run
	next.Execution.OperationalObservation = &observation
	next.Execution.OperationalObservationRunVersion = run.Version + 1
	next.Execution.OperationalObservationConsumed = false
	return controller.persistRun(ctx, run, next, "run.operational_observed")
}

func (controller *Controller) parkRun(ctx context.Context, run domain.Run, code domainexecution.NeedCode) error {
	escalated := escalation.Reduce(escalation.Facts{
		SchemaVersion: escalation.SchemaVersion, Scope: run.Execution.Scope,
		CauseCode: code, Reconciled: true, WakeCondition: "fresh_fact_or_human_decision",
	})
	if escalated.Kind != escalation.DecisionNeedsYou {
		return errors.New("execution cause was not admitted by escalation reducer")
	}
	if run.Execution.NeedsYou != nil && run.Execution.NeedsYou.Code == code {
		return nil
	}
	next := run
	next.Execution.NeedsYou = &escalated.NeedsYou
	return controller.persistRun(ctx, run, next, "run.needs_you")
}

func budgetActivity(kind domainexecution.EffectKind) (runtimebudget.Activity, bool) {
	switch kind {
	case domainexecution.EffectAgentCreate:
		return runtimebudget.ActivityWorkerBootstrap, true
	case domainexecution.EffectAgentPrompt:
		return runtimebudget.ActivityWorkerTurn, true
	case domainexecution.EffectSetupRun:
		return runtimebudget.ActivitySetupAttempt, true
	default:
		return "", false
	}
}

func budgetReservationID(effect domainexecution.Effect) string {
	return stableID("budget-reservation", effect.ID, fmt.Sprintf("attempt-%d", effect.Attempt+1))
}

func budgetDemand(run domain.Run, activity runtimebudget.Activity) runtimebudget.Demand {
	if activity != runtimebudget.ActivitySetupAttempt {
		return run.Execution.TurnBudgetDemand
	}
	for _, step := range run.Execution.PreparationPlan.Steps {
		if step.Kind == "prepare_dependencies" {
			return runtimebudget.Demand{WallTimeMilliseconds: uint64(step.TimeoutSeconds) * 1_000}
		}
	}
	return runtimebudget.Demand{}
}

func budgetWakeCondition(decision runtimebudget.Decision) string {
	switch decision.Disposition {
	case runtimebudget.DispositionSoftPause:
		return "human_acknowledges_exact_budget_warning"
	case runtimebudget.DispositionHardExhausted:
		return "human_applies_permitted_budget_revision"
	default:
		return "fresh_unambiguous_provider_usage_or_human_decision"
	}
}

func budgetTransition(decision runtimebudget.Decision, fallback string) string {
	switch decision.Disposition {
	case runtimebudget.DispositionSoftPause:
		return "soft_budget_reached"
	case runtimebudget.DispositionHardExhausted:
		return "hard_budget_exhausted"
	case runtimebudget.DispositionFailClosed:
		return "run.budget_fact_failed_closed"
	default:
		return fallback
	}
}

func budgetNeedsYou(scope domainexecution.Scope, decision runtimebudget.Decision) (*domainexecution.NeedsYou, error) {
	escalated := escalation.Reduce(escalation.Facts{
		SchemaVersion: escalation.SchemaVersion, Scope: scope,
		CauseCode: domainexecution.NeedCode(decision.Reason), Reconciled: true,
		WakeCondition: budgetWakeCondition(decision),
	})
	if escalated.Kind != escalation.DecisionNeedsYou {
		return nil, errors.New("runtime budget cause was not admitted by escalation reducer")
	}
	return &escalated.NeedsYou, nil
}

func (controller *Controller) reserveEffectBudget(
	ctx context.Context, run domain.Run, kind domainexecution.EffectKind, nowMillis int64,
) (StepResult, bool, error) {
	activity, budgeted := budgetActivity(kind)
	if !budgeted {
		return StepResult{Run: run}, false, nil
	}
	effect := effectPointer(&run.Execution, kind)
	if effect == nil {
		return StepResult{Run: run}, true, errors.New("budgeted execution effect is missing")
	}
	request := runtimebudget.ReserveRequest{
		ID: budgetReservationID(*effect), EffectID: effect.ID, Activity: activity,
		LeaseEpoch:     run.Execution.LeaseBinding.Epoch,
		PolicyRevision: run.Execution.Budget.Policy.Revision,
		Demand:         budgetDemand(run, activity),
	}
	ledger := run.Execution.Budget
	if effect.Attempt > 0 && effect.Observation != nil &&
		effect.Observation.Status == domainexecution.ObservationAbsent && effect.Observation.PriorDispatcherAbsent {
		released, err := runtimebudget.ReleaseAbsentReservation(
			ledger, effect.ID, run.Execution.LeaseBinding.Epoch,
			stableID("budget-absence", effect.Observation.ID),
		)
		if err != nil {
			return StepResult{Run: run}, true, err
		}
		ledger = released
	}
	if effect.Observation != nil {
		switch effect.Observation.Status {
		case domainexecution.ObservationErrored, domainexecution.ObservationPermission,
			domainexecution.ObservationDifferent, domainexecution.ObservationAmbiguous:
			request.Fingerprint = hashText(string(effect.Observation.Status) + "\x1f" + effect.Observation.ExternalID)
		}
	}
	ledger, decision, err := runtimebudget.Reserve(ledger, request, nowMillis)
	if err != nil {
		return StepResult{Run: run}, true, err
	}
	if decision.Disposition != runtimebudget.DispositionAllow {
		needsYou, needsErr := budgetNeedsYou(run.Execution.Scope, decision)
		if needsErr != nil {
			return StepResult{Run: run}, true, needsErr
		}
		next := run
		next.Execution.Budget = ledger
		next.Execution.NeedsYou = needsYou
		return StepResult{Run: next, Progressed: true}, true, controller.persistRun(
			ctx, run, next, budgetTransition(decision, "run.budget_paused"),
		)
	}
	if ledgerEqual(run.Execution.Budget, ledger) {
		return StepResult{Run: run}, false, nil
	}
	next := run
	next.Execution.Budget = ledger
	return StepResult{Run: next, Progressed: true}, true, controller.persistRun(ctx, run, next, "run.budget_reserved")
}

func ledgerEqual(left, right runtimebudget.Ledger) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func effectReservation(ledger runtimebudget.Ledger, effect domainexecution.Effect) (runtimebudget.Reservation, bool) {
	id := stableID("budget-reservation", effect.ID, fmt.Sprintf("attempt-%d", effect.Attempt))
	index := slices.IndexFunc(ledger.Reservations, func(reservation runtimebudget.Reservation) bool {
		return reservation.ID == id && !reservation.Released
	})
	if index < 0 {
		return runtimebudget.Reservation{}, false
	}
	return ledger.Reservations[index], true
}

func applyEffectBudget(run *domain.Run, effect domainexecution.Effect, nowMillis int64) runtimebudget.Decision {
	activity, budgeted := budgetActivity(effect.Kind)
	if !budgeted {
		return runtimebudget.Decision{Disposition: runtimebudget.DispositionAllow}
	}
	reservation, ok := effectReservation(run.Execution.Budget, effect)
	if !ok || effect.Observation == nil {
		return runtimebudget.Decision{Disposition: runtimebudget.DispositionFailClosed, Reason: runtimebudget.ReasonProviderUsageAmbiguous}
	}
	if activity == runtimebudget.ActivitySetupAttempt {
		observation := runtimebudget.ActivityObservation{
			ID: stableID("budget-activity", effect.Observation.ID), ReservationID: reservation.ID,
			EffectID: effect.ID, Activity: activity, LeaseEpoch: run.Execution.LeaseBinding.Epoch,
			PolicyRevision:   run.Execution.Budget.Policy.Revision,
			ObservedAtMillis: nowMillis, Fingerprint: reservation.Fingerprint,
		}
		observation.FactHash = runtimebudget.ActivityObservationHash(observation)
		ledger, decision, err := runtimebudget.ApplyActivityObservation(run.Execution.Budget, observation)
		if err != nil {
			return runtimebudget.Decision{Disposition: runtimebudget.DispositionFailClosed, Reason: runtimebudget.ReasonLedgerInvalid}
		}
		run.Execution.Budget = ledger
		return decision
	}
	usage := runtimebudget.ProviderUsage{State: runtimebudget.UsageUnavailable}
	if effect.Observation.Usage != nil {
		usage = *effect.Observation.Usage
	}
	agentID := run.Execution.Agent.ExternalID
	if agentID == "" {
		agentID = effect.Observation.ExternalID
	}
	observation := runtimebudget.ProviderObservation{
		ID: stableID("provider-usage", effect.Observation.ID), EffectID: effect.ID,
		AgentID: agentID, Activity: activity,
		LeaseEpoch: run.Execution.LeaseBinding.Epoch, PolicyRevision: run.Execution.Budget.Policy.Revision,
		Sequence: effect.Observation.Cursor, ObservedAtMillis: nowMillis,
		ProviderUsage: usage,
	}
	observation.FactHash = runtimebudget.ProviderObservationHash(observation)
	ledger, decision, err := runtimebudget.ApplyProviderObservation(run.Execution.Budget, observation)
	if err != nil {
		return runtimebudget.Decision{Disposition: runtimebudget.DispositionFailClosed, Reason: runtimebudget.ReasonLedgerInvalid}
	}
	run.Execution.Budget = ledger
	return decision
}

func (controller *Controller) applyEffectDecision(
	ctx context.Context,
	run domain.Run,
	kind domainexecution.EffectKind,
	action string,
	nowMillis int64,
) (StepResult, error) {
	switch action {
	case "observe":
		observation, err := controller.observeEffect(ctx, run, kind)
		if err != nil {
			return StepResult{Run: run}, err
		}
		next := run
		effect := effectPointer(&next.Execution, kind)
		effect.Observation = &observation
		transition := "run.effect_observed"
		if (kind == domainexecution.EffectAgentCreate || kind == domainexecution.EffectAgentPrompt) &&
			(observation.Status == domainexecution.ObservationErrored || observation.Status == domainexecution.ObservationPermission) {
			budgetDecision := applyEffectBudget(&next, *effect, nowMillis)
			transition = "run.effect_terminal_usage_observed"
			if budgetDecision.Disposition != runtimebudget.DispositionAllow {
				needsYou, needsErr := budgetNeedsYou(run.Execution.Scope, budgetDecision)
				if needsErr != nil {
					return StepResult{Run: run}, needsErr
				}
				next.Execution.NeedsYou = needsYou
				transition = budgetTransition(budgetDecision, transition)
			}
		}
		if err := controller.persistRun(ctx, run, next, transition); err != nil {
			return StepResult{Run: run}, err
		}
		return StepResult{Run: next, Progressed: true}, nil
	case "dispatch":
		if budgeted, handled, err := controller.reserveEffectBudget(ctx, run, kind, nowMillis); err != nil || handled {
			return budgeted, err
		}
		if isHostEffect(kind) {
			if err := controller.verifyHost(ctx); err != nil {
				if parkErr := controller.parkRun(ctx, run, domainexecution.NeedCode("host_contract_unavailable")); parkErr != nil {
					return StepResult{Run: run}, parkErr
				}
				return StepResult{Run: run, Progressed: true}, nil
			}
		}
		next := run
		effect := effectPointer(&next.Execution, kind)
		effect.Phase = domainexecution.EffectDispatching
		effect.Attempt++
		effect.Observation = nil
		next.Execution.OperationalObservationConsumed = true
		won, err := controller.persistRunTransition(ctx, run, next, "run.effect_dispatching")
		if err != nil {
			return StepResult{Run: run}, err
		}
		if !won {
			current, readErr := controller.store.Run(ctx, run.ID)
			return StepResult{Run: current}, readErr
		}
		next.Version = run.Version + 1
		err = controller.dispatchEffect(ctx, next, kind)
		if err != nil {
			return StepResult{Run: next, Progressed: true}, &EffectHandoffError{Kind: kind, err: err}
		}
		return StepResult{Run: next, Progressed: true}, nil
	case "adopt":
		if kind == domainexecution.EffectAgentCreate &&
			(run.Execution.WorkerVisibility == nil ||
				run.Execution.WorkerVisibility.Digest == "" ||
				run.Execution.WorkerVisibility.Digest != effectPointer(&run.Execution, kind).Observation.CorrelationHash) {
			return StepResult{Run: run, Progressed: true}, controller.parkRun(
				ctx, run, domainexecution.NeedWorkerVisibilityMissing,
			)
		}
		next := run
		effect := effectPointer(&next.Execution, kind)
		budgetDecision := applyEffectBudget(&next, *effect, nowMillis)
		effect.Phase = domainexecution.EffectComplete
		effect.ExternalID = effect.Observation.ExternalID
		effect.ObservedFactHash = effect.Observation.FactHash
		effect.ObservedCorrelation = effect.Observation.CorrelationHash
		effect.Observation = nil
		next.Execution.OperationalObservationConsumed = true
		transition := "run.effect_completed"
		if budgetDecision.Disposition != runtimebudget.DispositionAllow {
			needsYou, err := budgetNeedsYou(run.Execution.Scope, budgetDecision)
			if err != nil {
				return StepResult{Run: run}, err
			}
			next.Execution.NeedsYou = needsYou
			transition = budgetTransition(budgetDecision, "run.effect_completed_budget_paused")
		}
		if err := controller.persistRun(ctx, run, next, transition); err != nil {
			return StepResult{Run: run}, err
		}
		return StepResult{Run: next, Progressed: true}, nil
	default:
		return StepResult{Run: run}, errors.New("unknown reducer effect action")
	}
}

func (controller *Controller) applyRetryDecision(
	ctx context.Context,
	run domain.Run,
	kind domainexecution.EffectKind,
	nowMillis int64,
	recoveryAuthorized bool,
) (StepResult, error) {
	effect := effectPointer(&run.Execution, kind)
	if effect == nil || effect.Observation == nil {
		return StepResult{Run: run}, errors.New("retry reducer requires an observed effect")
	}
	decision := retry.Reduce(retry.Facts{
		SchemaVersion: retry.SchemaVersion, Effect: *effect, Observation: *effect.Observation,
		BindingUnchanged:      effect.Observation.BindingHash == run.Execution.RepositoryBindingHash,
		PriorDispatcherAbsent: effect.Observation.PriorDispatcherAbsent,
		BudgetAvailable:       run.Execution.BudgetReservationID != "" && run.Execution.CapacityReservationID != "",
		TaskStoreNowMillis:    nowMillis,
	})
	switch decision.Kind {
	case retry.DecisionDispatch:
		if !recoveryAuthorized {
			return StepResult{Run: run}, nil
		}
		return controller.applyEffectDecision(ctx, run, kind, "dispatch", nowMillis)
	case retry.DecisionAdopt:
		return controller.applyEffectDecision(ctx, run, kind, "adopt", nowMillis)
	case retry.DecisionEscalate:
		return StepResult{Run: run, Progressed: true}, controller.parkRun(ctx, run, domainexecution.NeedCode(decision.Code))
	default:
		return StepResult{Run: run}, errors.New("unknown retry decision")
	}
}

// admittedWorkerVisibilityDigest re-derives the launch registration digest from
// the durable Run and returns it only when it still matches the frozen record.
// A registration that was never admitted, or whose stored labels no longer
// produce the frozen digest, yields the empty string and the launch reducer
// refuses to create the agent.
func admittedWorkerVisibilityDigest(state domainexecution.State) string {
	if state.WorkerVisibility == nil {
		return ""
	}
	registration, err := host.RegistrationFromVisibility(state.Scope, *state.WorkerVisibility)
	if err != nil {
		return ""
	}
	digest, err := host.RegistrationDigest(registration)
	if err != nil || digest != state.WorkerVisibility.Digest {
		return ""
	}
	return digest
}

// commitWorkerVisibility freezes the root-workspace launch registration once
// the Execution Workspace exists and before any agent-creation intent. The
// engine owns the record; the host port owns the label vocabulary and admits
// the exact set. A registration the host port refuses parks the Run rather
// than launching a worker the owner could not find from its root workspace.
func (controller *Controller) commitWorkerVisibility(
	ctx context.Context, run domain.Run, nowMillis int64,
) (StepResult, error) {
	instant := time.UnixMilli(nowMillis).UTC().Format(time.RFC3339Nano)
	visibility := domainexecution.WorkerVisibility{
		RootWorkspaceID:      run.Execution.RootWorkspaceID,
		ExecutionWorkspaceID: run.Execution.HostView.ExternalID,
		Role:                 string(host.WorkerRoleTaskAgent),
		Phase:                host.WorkerPhaseBuilding,
		BaseSHA:              run.BaseSHA,
		EffectID:             run.Execution.PrimarySession.AgentIntentID,
		ProfileSHA256:        run.Execution.EffectiveProfilesSHA256,
		SessionSHA256:        run.Execution.PrimarySession.ReservationSHA256,
		RegisteredAt:         instant,
		StartedAt:            instant,
		Digest:               "pending",
	}
	registration, err := host.RegistrationFromVisibility(run.Execution.Scope, visibility)
	if err != nil {
		return StepResult{Run: run, Progressed: true}, controller.parkRun(
			ctx, run, domainexecution.NeedWorkerVisibilityMissing,
		)
	}
	digest, err := host.RegistrationDigest(registration)
	if err != nil {
		return StepResult{Run: run, Progressed: true}, controller.parkRun(
			ctx, run, domainexecution.NeedWorkerVisibilityMissing,
		)
	}
	visibility.Digest = digest
	next := run
	next.Execution.WorkerVisibility = &visibility
	next.Execution.OperationalObservationConsumed = true
	return StepResult{Run: next, Progressed: true}, controller.persistRun(
		ctx, run, next, "run.worker_visibility_registered",
	)
}

func (controller *Controller) commitWorkerIdentity(ctx context.Context, run domain.Run) (StepResult, error) {
	if run.Execution.WorkerVisibility == nil || run.Execution.Agent.ExternalID == "" ||
		run.Execution.Agent.ObservedCorrelation != run.Execution.WorkerVisibility.Digest ||
		run.Execution.Agent.ObservedFactHash == "" {
		return StepResult{Run: run, Progressed: true}, controller.parkRun(
			ctx, run, domainexecution.NeedWorkerVisibilityMissing,
		)
	}
	next := run
	next.Execution.WorkerVisibility.AgentID = run.Execution.Agent.ExternalID
	next.Execution.WorkerVisibility.ObservedDigest = run.Execution.Agent.ObservedCorrelation
	bound, err := domainexecution.BindPrimarySession(next.Execution.PrimarySession, run.Execution.Agent.ExternalID)
	if err != nil {
		return StepResult{Run: run, Progressed: true}, controller.parkRun(
			ctx, run, domainexecution.NeedWorkerVisibilityMissing,
		)
	}
	next.Execution.PrimarySession = bound
	registration, err := host.RegistrationFromVisibility(next.Execution.Scope, *next.Execution.WorkerVisibility)
	if err != nil {
		return StepResult{Run: run, Progressed: true}, controller.parkRun(
			ctx, run, domainexecution.NeedWorkerVisibilityMissing,
		)
	}
	labels, err := host.WorkerLabels(registration)
	if err != nil {
		return StepResult{Run: run, Progressed: true}, controller.parkRun(
			ctx, run, domainexecution.NeedWorkerVisibilityMissing,
		)
	}
	identity := domainexecution.ControlledAgentIdentity{
		ID: run.Execution.Agent.ExternalID, Role: domainexecution.ControlledTaskAgent,
		WorkspaceID: run.Execution.HostView.ExternalID, Title: run.Execution.TaskTitle,
		WorktreePath: run.Execution.WorktreePath, Labels: labels,
		ProfileSHA256: run.Execution.EffectiveProfilesSHA256,
	}
	if existing := controlledAgentIndex(next.Execution.ControlledAgents, identity.ID); existing < 0 {
		next.Execution.ControlledAgents = append(next.Execution.ControlledAgents, identity)
	}
	next.Execution.OperationalObservationConsumed = true
	return StepResult{Run: next, Progressed: true}, controller.persistRun(
		ctx, run, next, "run.worker_identity_persisted",
	)
}

func (controller *Controller) stepLaunch(ctx context.Context, run domain.Run, nowMillis int64, recoveryAuthorized bool) (StepResult, error) {
	operational := operationalResult(run, nowMillis)
	lifecycle := domainexecution.AdmitLifecycle(
		run.Execution.Scope, run.Execution.LifecycleSurfaces, run.Execution.LifecycleApproval,
	)
	isolation := domainexecution.AdmitIsolation(run.Execution.Isolation)
	barrier := preparationBarrier(run.Execution)
	barrierFact := barrier
	if run.Execution.PreparationReady && run.Execution.PreparationBarrierHash != barrier {
		barrierFact = ""
	}
	facts := launch.Facts{
		SchemaVersion:         launch.SchemaVersion,
		EligibilityDecisionID: run.Execution.EligibilityDecisionID,
		EligibilityCurrent: run.Execution.EligibilityDecisionVersion == eligibility.SchemaVersion &&
			run.Execution.EligibilityFactsHash != "",
		CapacityReserved:  run.Execution.CapacityReservationID != "",
		BudgetReserved:    run.Execution.BudgetReservationID != "",
		ImmutableRunReady: run.Execution.SchemaVersion == domainexecution.SchemaVersion,
		ExactRepositoryBinding: run.Execution.RepositoryBindingHash == domainexecution.RepositoryBindingSHA256(run.Execution.RepositoryBinding) &&
			run.Execution.RepositoryBinding.SourcePath == run.Execution.SourcePath &&
			run.Execution.RepositoryBinding.WorktreePath == run.Execution.WorktreePath &&
			run.Execution.RepositoryBinding.Branch == run.Execution.Branch &&
			run.Execution.RepositoryBinding.BaseSHA == run.BaseSHA && launchBindingsExact(run.Execution),
		LifecycleAdmissionDigest: func() string {
			if lifecycle.Kind == domainexecution.AdmissionAllow && lifecycle.Digest == run.Execution.LifecycleDigest {
				return lifecycle.Digest
			}
			return ""
		}(),
		IsolationAdmissionDigest: func() string {
			if isolation.Kind == domainexecution.AdmissionAllow && isolation.Digest == run.Execution.IsolationDigest {
				return isolation.Digest
			}
			return ""
		}(),
		OperationalLimitsAdmitted: operational.Kind == domainexecution.AdmissionAllow,
		SetupRequired:             setupRequired(run.Execution.LifecycleSurfaces),
		PreparationPlanID:         run.Execution.PreparationPlan.ID,
		PreparationPlanValid:      domainexecution.ValidPreparationPlan(run.Execution.PreparationPlan),
		PreparationBarrierHash:    barrierFact,
		PreparationReady:          run.Execution.PreparationReady,
		WorkerVisibilityDigest:    admittedWorkerVisibilityDigest(run.Execution),
		WorkerIdentityPersisted: run.Execution.WorkerVisibility != nil &&
			run.Execution.WorkerVisibility.AgentID != "" &&
			run.Execution.WorkerVisibility.AgentID == run.Execution.Agent.ExternalID &&
			run.Execution.WorkerVisibility.ObservedDigest == run.Execution.WorkerVisibility.Digest &&
			run.Execution.PrimarySession.NativeAgentID == run.Execution.Agent.ExternalID &&
			run.Execution.PrimarySession.BindingSHA256 != "" &&
			domainexecution.ValidPrimarySession(run.Execution.PrimarySession),
		TaskStoreNowMillis: nowMillis,
		Worktree:           run.Execution.Worktree, HostView: run.Execution.HostView,
		Boundary: run.Execution.Boundary, Setup: run.Execution.Setup, Agent: run.Execution.Agent,
		AgentPrompt: run.Execution.AgentPrompt,
	}
	if run.Execution.OperationalObservation != nil && !run.Execution.OperationalObservationConsumed &&
		run.Execution.OperationalObservationRunVersion == run.Version {
		facts.OperationalObservationID = run.Execution.OperationalObservation.ID
	}
	decision := launch.Reduce(facts)
	if decision.Kind == launch.DecisionEscalate {
		code := domainexecution.NeedCode(decision.Code)
		if operational.Kind != domainexecution.AdmissionAllow {
			code = operational.Code
		}
		return StepResult{Run: run, Progressed: true}, controller.parkRun(ctx, run, code)
	}
	switch decision.Kind {
	case launch.DecisionObserveOperationalLimits:
		return StepResult{Run: run, Progressed: true}, controller.observeOperational(ctx, run)
	case launch.DecisionObserve:
		return controller.applyEffectDecision(ctx, run, decision.EffectKind, "observe", nowMillis)
	case launch.DecisionDispatch:
		return controller.applyEffectDecision(ctx, run, decision.EffectKind, "dispatch", nowMillis)
	case launch.DecisionAdopt:
		return controller.applyEffectDecision(ctx, run, decision.EffectKind, "adopt", nowMillis)
	case launch.DecisionRetry:
		return controller.applyRetryDecision(ctx, run, decision.EffectKind, nowMillis, recoveryAuthorized)
	case launch.DecisionCommitPreparationReady:
		next := run
		next.Execution.PreparationReady = true
		next.Execution.PreparationBarrierHash = decision.PreparationBarrierHash
		next.Execution.OperationalObservationConsumed = true
		return StepResult{Run: next, Progressed: true}, controller.persistRun(ctx, run, next, "run.preparation_ready")
	case launch.DecisionCommitWorkerVisibility:
		return controller.commitWorkerVisibility(ctx, run, nowMillis)
	case launch.DecisionCommitWorkerIdentity:
		return controller.commitWorkerIdentity(ctx, run)
	case launch.DecisionCreateAgentIntent:
		next := run
		next.Execution.Agent = newEffect(run.ID, domainexecution.EffectAgentCreate, 2)
		next.Execution.OperationalObservationConsumed = true
		return StepResult{Run: next, Progressed: true}, controller.persistRun(ctx, run, next, "run.agent_intent_recorded")
	case launch.DecisionCreateAgentPromptIntent:
		next := run
		next.Execution.AgentPrompt = newEffect(run.ID, domainexecution.EffectAgentPrompt, 2)
		next.Execution.OperationalObservationConsumed = true
		return StepResult{Run: next, Progressed: true}, controller.persistRun(ctx, run, next, "run.agent_prompt_intent_recorded")
	case launch.DecisionWaitTerminalEvent:
		return StepResult{Run: run}, nil
	case launch.DecisionLaunched:
		return StepResult{Run: run}, nil
	default:
		return StepResult{Run: run}, errors.New("unknown launch decision")
	}
}

func (controller *Controller) stepRouting(ctx context.Context, run domain.Run, nowMillis int64) (StepResult, error) {
	operational := operationalResult(run, nowMillis)
	facts := routing.Facts{
		SchemaVersion: routing.SchemaVersion, AgentID: run.Execution.Agent.ExternalID,
		AgentTurnEnded:            run.Execution.Claim != nil,
		RepositoryBindingHash:     run.Execution.RepositoryBindingHash,
		OperationalLimitsAdmitted: operational.Kind == domainexecution.AdmissionAllow,
		OperationalNeedCode:       operational.Code, Claim: run.Execution.Claim,
		CandidateObservation: run.Execution.CandidateObservation,
		TaskStoreNowMillis:   nowMillis,
	}
	if run.Execution.OperationalObservation != nil && !run.Execution.OperationalObservationConsumed &&
		run.Execution.OperationalObservationRunVersion == run.Version {
		facts.OperationalObservationID = run.Execution.OperationalObservation.ID
	}
	decision := routing.Reduce(facts)
	switch decision.Kind {
	case routing.DecisionObserveOperationalLimits:
		return StepResult{Run: run, Progressed: true}, controller.observeOperational(ctx, run)
	case routing.DecisionEscalate:
		return StepResult{Run: run, Progressed: true}, controller.parkRun(ctx, run, domainexecution.NeedCode(decision.Code))
	case routing.DecisionWaitAgent:
		return StepResult{Run: run}, nil
	case routing.DecisionObserveCandidate:
		observation, err := controller.runtime.ObserveCandidate(ctx, runtimeport.CandidateRequest{
			Scope: run.Execution.Scope, SourcePath: run.Execution.SourcePath,
			WorktreePath: run.Execution.WorktreePath, Branch: run.Execution.Branch,
			WorktreeID:  run.Execution.Worktree.ExternalID,
			BindingHash: run.Execution.RepositoryBindingHash, Claim: *run.Execution.Claim,
		})
		if err != nil {
			return StepResult{Run: run}, err
		}
		next := run
		next.Execution.CandidateObservation = &observation
		next.Execution.OperationalObservationConsumed = true
		return StepResult{Run: next, Progressed: true}, controller.persistRun(ctx, run, next, "run.candidate_observed")
	case routing.DecisionAdmitCandidate:
		candidate := domain.Candidate{
			ID:    stableID("candidate", run.ID, "1", decision.CandidateSHA),
			RunID: run.ID, Sequence: 1, CommitSHA: decision.CandidateSHA,
		}
		commandID := stableID("command", run.ID, "candidate-1", decision.CandidateSHA)
		result, err := controller.store.AppendCandidate(ctx, domain.CommandRequest{
			IdempotencyKey: commandID, Type: "candidate.admit", AggregateID: run.ID,
			ExpectedVersion: run.Version,
			Payload: eventPayload(struct {
				ClaimID       string `json:"claimId"`
				ObservationID string `json:"observationId"`
				CandidateID   string `json:"candidateId"`
			}{run.Execution.Claim.ID, run.Execution.CandidateObservation.ID, candidate.ID}),
		}, candidate, domain.Event{
			ID: stableID("event", commandID), RunID: run.ID, Sequence: run.Version + 2,
			AggregateID: run.ID, AggregateVersion: run.Version + 1,
			Type: "candidate.admitted", Payload: eventPayload(struct {
				CandidateID string `json:"candidateId"`
			}{candidate.ID}),
		})
		if err != nil {
			return StepResult{Run: run}, err
		}
		if result.Outcome != domain.CommandApplied {
			return StepResult{Run: run}, errors.New("admit Candidate: version conflict")
		}
		next, err := controller.store.Run(ctx, run.ID)
		return StepResult{Run: next, Progressed: true}, err
	default:
		return StepResult{Run: run}, errors.New("unknown routing decision")
	}
}

func (controller *Controller) stepClosure(ctx context.Context, run domain.Run, nowMillis int64, recoveryAuthorized bool) (StepResult, error) {
	operational := operationalResult(run, nowMillis)
	expectedCandidate := ""
	if run.Execution.Claim != nil {
		expectedCandidate = stableID("candidate", run.ID, "1", run.Execution.Claim.CandidateSHA)
	}
	facts := closure.Facts{
		SchemaVersion: closure.SchemaVersion, FakeTerminalRung: run.Execution.FakeTerminalRung,
		CandidateCurrent:        run.CurrentCandidateID != "" && run.CurrentCandidateID == expectedCandidate,
		CriteriaClaimsSatisfied: allCriteriaClaimedSatisfied(run.Execution.Claim),
		ExactOwnership: run.Execution.Agent.ExternalID != "" && run.Execution.HostView.ExternalID != "" &&
			run.Execution.Worktree.ExternalID != "" && cleanupBindingsExact(run.Execution) &&
			run.Execution.RepositoryBindingHash == domainexecution.RepositoryBindingSHA256(run.Execution.RepositoryBinding) &&
			run.Execution.RepositoryBinding.SourcePath == run.Execution.SourcePath &&
			run.Execution.RepositoryBinding.WorktreePath == run.Execution.WorktreePath &&
			run.Execution.RepositoryBinding.Branch == run.Execution.Branch &&
			run.Execution.RepositoryBinding.BaseSHA == run.BaseSHA,
		OperationalLimitsAdmitted: operational.Kind == domainexecution.AdmissionAllow,
		OperationalNeedCode:       operational.Code,
		TaskStoreNowMillis:        nowMillis,
		AgentArchive:              run.Execution.AgentArchive, HostViewArchive: run.Execution.HostViewArchive,
		WorktreeRemove: run.Execution.WorktreeRemove,
	}
	if run.Execution.OperationalObservation != nil && !run.Execution.OperationalObservationConsumed &&
		run.Execution.OperationalObservationRunVersion == run.Version {
		facts.OperationalObservationID = run.Execution.OperationalObservation.ID
	}
	decision := closure.Reduce(facts)
	switch decision.Kind {
	case closure.DecisionObserveOperationalLimits:
		return StepResult{Run: run, Progressed: true}, controller.observeOperational(ctx, run)
	case closure.DecisionEscalate:
		return StepResult{Run: run, Progressed: true}, controller.parkRun(ctx, run, domainexecution.NeedCode(decision.Code))
	case closure.DecisionCreateAgentIntent:
		next := run
		next.Execution.AgentArchive = newEffect(run.ID, domainexecution.EffectAgentArchive, 2)
		next.Execution.OperationalObservationConsumed = true
		return StepResult{Run: next, Progressed: true}, controller.persistRun(ctx, run, next, "run.agent_archive_intent_recorded")
	case closure.DecisionCreateHostViewIntent:
		next := run
		next.Execution.HostViewArchive = newEffect(run.ID, domainexecution.EffectHostViewArchive, 2)
		next.Execution.OperationalObservationConsumed = true
		return StepResult{Run: next, Progressed: true}, controller.persistRun(ctx, run, next, "run.host_view_archive_intent_recorded")
	case closure.DecisionCreateWorktreeIntent:
		next := run
		next.Execution.WorktreeRemove = newEffect(run.ID, domainexecution.EffectWorktreeRemove, 1)
		next.Execution.OperationalObservationConsumed = true
		return StepResult{Run: next, Progressed: true}, controller.persistRun(ctx, run, next, "run.worktree_remove_intent_recorded")
	case closure.DecisionObserve:
		return controller.applyEffectDecision(ctx, run, decision.EffectKind, "observe", nowMillis)
	case closure.DecisionDispatch:
		return controller.applyEffectDecision(ctx, run, decision.EffectKind, "dispatch", nowMillis)
	case closure.DecisionAdopt:
		return controller.applyEffectDecision(ctx, run, decision.EffectKind, "adopt", nowMillis)
	case closure.DecisionRetry:
		return controller.applyRetryDecision(ctx, run, decision.EffectKind, nowMillis, recoveryAuthorized)
	case closure.DecisionTerminal:
		next := run
		next.Execution.Terminal = true
		next.Execution.OperationalObservationConsumed = true
		return StepResult{Run: next, Progressed: true}, controller.persistRun(ctx, run, next, "run.fake_execution_terminal")
	default:
		return StepResult{Run: run}, errors.New("unknown closure decision")
	}
}

// stepPausedEvidence continues only observation and evidence persistence for
// an already handed-off model turn. It cannot create an intent, reserve work,
// retry, prompt, review, validate, or clean recoverable state.
func (controller *Controller) stepPausedEvidence(ctx context.Context, run domain.Run, nowMillis int64) (StepResult, error) {
	if run.Execution.NeedsYou != nil &&
		(run.Execution.NeedsYou.Code == domainexecution.NeedCode(runtimebudget.ReasonProviderUsageUnavailable) ||
			run.Execution.NeedsYou.Code == domainexecution.NeedCode(runtimebudget.ReasonProviderUsageAmbiguous)) {
		for _, kind := range []domainexecution.EffectKind{domainexecution.EffectAgentPrompt, domainexecution.EffectAgentCreate} {
			effect := effectPointer(&run.Execution, kind)
			if effect == nil || effect.ID == "" || effect.Phase != domainexecution.EffectComplete {
				continue
			}
			if _, outstanding := effectReservation(run.Execution.Budget, *effect); !outstanding {
				continue
			}
			observation, err := controller.observeEffect(ctx, run, kind)
			if err != nil {
				return StepResult{Run: run}, err
			}
			if observation.Status != domainexecution.ObservationDesired &&
				observation.Status != domainexecution.ObservationErrored &&
				observation.Status != domainexecution.ObservationPermission {
				return StepResult{Run: run}, nil
			}
			observedEffect := *effect
			observedEffect.Observation = &observation
			next := run
			decision := applyEffectBudget(&next, observedEffect, nowMillis)
			if decision.Disposition == runtimebudget.DispositionAllow {
				next.Execution.NeedsYou = nil
			} else {
				needsYou, needsErr := budgetNeedsYou(run.Execution.Scope, decision)
				if needsErr != nil {
					return StepResult{Run: run}, needsErr
				}
				next.Execution.NeedsYou = needsYou
			}
			if ledgerEqual(run.Execution.Budget, next.Execution.Budget) &&
				run.Execution.NeedsYou != nil && next.Execution.NeedsYou != nil &&
				*run.Execution.NeedsYou == *next.Execution.NeedsYou {
				return StepResult{Run: run}, nil
			}
			if err := controller.persistRun(ctx, run, next, "run.provider_usage_reconciled"); err != nil {
				return StepResult{Run: run}, err
			}
			next.Version = run.Version + 1
			return StepResult{Run: next, Progressed: true}, nil
		}
	}
	for _, kind := range []domainexecution.EffectKind{domainexecution.EffectAgentPrompt, domainexecution.EffectAgentCreate} {
		effect := effectPointer(&run.Execution, kind)
		if effect == nil || effect.ID == "" || effect.Phase == domainexecution.EffectComplete {
			continue
		}
		if effect.Observation == nil {
			return controller.applyEffectDecision(ctx, run, kind, "observe", nowMillis)
		}
		if effect.Observation.Status == domainexecution.ObservationDesired {
			return controller.applyEffectDecision(ctx, run, kind, "adopt", nowMillis)
		}
		return StepResult{Run: run}, nil
	}
	return StepResult{Run: run}, nil
}

// Step executes at most one durable transition or one already-persisted
// adapter handoff. Constructing a new Controller between calls is supported.
func (controller *Controller) step(ctx context.Context, runID string, nowMillis int64, recoveryAuthorized bool) (StepResult, error) {
	run, err := controller.store.Run(ctx, runID)
	if err != nil {
		return StepResult{}, err
	}
	if run.Execution.Terminal {
		return StepResult{Run: run}, nil
	}
	if run.Execution.PrimaryRecovery.SchemaVersion != "" &&
		run.Execution.PrimaryRecovery.Phase != domainexecution.PrimaryRecoveryComplete &&
		run.Execution.PrimaryRecovery.Phase != domainexecution.PrimaryRecoveryNeedsYou {
		recovery, recoveryErr := controller.ReconcilePrimaryRecovery(ctx, run.ID, nowMillis)
		return StepResult{Run: recovery.Run, Progressed: recovery.Progressed}, recoveryErr
	}
	if err := controller.currentExecutionAuthority(ctx, run, nowMillis); err != nil {
		return StepResult{Run: run}, err
	}
	if run.Execution.Control.SchemaVersion != "" && run.Execution.Control.Phase != domainexecution.ControlComplete {
		return controller.stepRunControl(ctx, run, nowMillis)
	}
	project, err := controller.store.Project(ctx, run.Execution.Scope.ProjectID)
	if err != nil {
		return StepResult{Run: run}, err
	}
	if project.State != "active" || project.Control.ResumeRequired {
		return StepResult{Run: run}, nil
	}
	if run.Execution.PrimaryRecovery.Authority != nil && run.Execution.PrimaryRecovery.Phase == domainexecution.PrimaryRecoveryComplete &&
		run.Execution.AgentPrompt.Observation != nil &&
		(run.Execution.AgentPrompt.Observation.Status == domainexecution.ObservationErrored ||
			run.Execution.AgentPrompt.Observation.Status == domainexecution.ObservationPermission) {
		return StepResult{Run: run, Progressed: true}, controller.parkRun(ctx, run, domainexecution.NeedRecoverySecondFailure)
	}
	if run.Execution.PrimaryRecovery.SchemaVersion == "" && run.Execution.AgentPrompt.Observation != nil &&
		(run.Execution.AgentPrompt.Observation.Status == domainexecution.ObservationErrored ||
			run.Execution.AgentPrompt.Observation.Status == domainexecution.ObservationPermission) {
		recovery, recoveryErr := controller.RequestPrimaryRecovery(ctx, PrimaryRecoveryCommand{
			SchemaVersion: PrimaryRecoveryCommandSchemaVersion,
			RequestID:     stableID("primary-recovery", run.ID, run.Execution.AgentPrompt.Observation.ID),
			RunID:         run.ID, ExpectedRunVersion: run.Version,
			Trigger: domainexecution.RecoveryProviderFailure, RepeatedFailureCount: 1, NowMillis: nowMillis,
		})
		return StepResult{Run: recovery.Run, Progressed: recovery.Progressed}, recoveryErr
	}
	if run.Execution.NeedsYou != nil {
		return controller.stepPausedEvidence(ctx, run, nowMillis)
	}
	ledger, budgetDecision := runtimebudget.Evaluate(run.Execution.Budget, nowMillis)
	if budgetDecision.Disposition != runtimebudget.DispositionAllow {
		next, persistErr := controller.persistBudgetResult(ctx, run, ledger, budgetDecision, "run.budget_reconciled")
		return StepResult{Run: next, Progressed: persistErr == nil}, persistErr
	}
	if run.CurrentCandidateID != "" {
		for _, helper := range run.Execution.Helpers {
			if helper.Phase == domainexecution.HelperTerminal {
				continue
			}
			if helper.Phase == domainexecution.HelperCleanupRequested {
				result, helperErr := controller.StepHelper(ctx, run.ID, helper.ID, nowMillis)
				return StepResult{Run: result.Run, Progressed: result.Progressed}, helperErr
			}
			if helper.Phase == domainexecution.HelperHandoffReady ||
				(helper.Mode == domainexecution.HelperReadOnly && helper.Phase == domainexecution.HelperActive) {
				result, helperErr := controller.RequestHelperCleanup(ctx, run.ID, helper.ID, nowMillis)
				return StepResult{Run: result.Run, Progressed: result.Progressed}, helperErr
			}
			return StepResult{Run: run, Progressed: true}, controller.parkRun(ctx, run, domainexecution.NeedHelperCleanupUnproven)
		}
		return controller.stepClosure(ctx, run, nowMillis, recoveryAuthorized)
	}
	if run.Execution.AgentPrompt.Phase == domainexecution.EffectComplete {
		return controller.stepRouting(ctx, run, nowMillis)
	}
	return controller.stepLaunch(ctx, run, nowMillis, recoveryAuthorized)
}

// Step advances ordinary event/command-driven execution. It deliberately
// cannot retry an effect whose previous dispatcher may still be live; only a
// complete startup reconciliation can supply that authorization.
func (controller *Controller) Step(ctx context.Context, runID string, nowMillis int64) (StepResult, error) {
	return controller.step(ctx, runID, nowMillis, false)
}

func validBudgetCommand(command BudgetWorkCommand) bool {
	return identifierPattern.MatchString(command.RequestID) && identifierPattern.MatchString(command.RunID) &&
		identifierPattern.MatchString(command.EffectID) && command.LeaseEpoch > 0 && command.NowMillis >= 0
}

func (controller *Controller) persistBudgetResult(
	ctx context.Context, current domain.Run, ledger runtimebudget.Ledger,
	decision runtimebudget.Decision, transition string,
) (domain.Run, error) {
	next := current
	next.Execution.Budget = ledger
	if decision.Disposition != runtimebudget.DispositionAllow {
		needsYou, err := budgetNeedsYou(current.Execution.Scope, decision)
		if err != nil {
			return current, err
		}
		next.Execution.NeedsYou = needsYou
		transition = budgetTransition(decision, transition)
	}
	if ledgerEqual(current.Execution.Budget, next.Execution.Budget) &&
		((current.Execution.NeedsYou == nil && next.Execution.NeedsYou == nil) ||
			(current.Execution.NeedsYou != nil && next.Execution.NeedsYou != nil && *current.Execution.NeedsYou == *next.Execution.NeedsYou)) {
		return current, nil
	}
	if err := controller.persistRun(ctx, current, next, transition); err != nil {
		return current, err
	}
	next.Version = current.Version + 1
	return next, nil
}

// ReserveAutomatedWork persists the exact reservation before a later adapter
// handoff. Reviewers and correction/validation controllers use this same path;
// no connector or model may reserve itself.
func (controller *Controller) ReserveAutomatedWork(ctx context.Context, command BudgetWorkCommand) (BudgetResult, error) {
	if !validBudgetCommand(command) {
		return BudgetResult{}, errors.New("runtime budget work command is invalid")
	}
	run, err := controller.store.Run(ctx, command.RunID)
	if err != nil {
		return BudgetResult{}, err
	}
	if run.Execution.LeaseBinding.Epoch != command.LeaseEpoch {
		return BudgetResult{}, ErrRuntimeBudgetLeaseFenced
	}
	if run.Version != command.ExpectedRunVersion {
		return BudgetResult{}, errors.New("runtime budget work command is stale")
	}
	if run.Execution.NeedsYou != nil || run.Execution.Terminal {
		return BudgetResult{}, errors.New("runtime budget work command targets a paused or terminal Run")
	}
	if err := controller.currentExecutionAuthority(ctx, run, command.NowMillis); err != nil {
		return BudgetResult{}, err
	}
	reservationID := stableID("budget-reservation", command.RequestID, command.EffectID)
	request := runtimebudget.ReserveRequest{
		ID: reservationID, EffectID: command.EffectID, Activity: command.Activity,
		LeaseEpoch: command.LeaseEpoch, PolicyRevision: run.Execution.Budget.Policy.Revision,
		Demand: command.Demand, CandidateSHA: command.CandidateSHA,
		Fingerprint: command.FailureFingerprint,
	}
	ledger, decision, err := runtimebudget.Reserve(run.Execution.Budget, request, command.NowMillis)
	if err != nil {
		return BudgetResult{}, err
	}
	replay := ledgerEqual(run.Execution.Budget, ledger) && decision.Disposition == runtimebudget.DispositionAllow
	next, err := controller.persistBudgetResult(ctx, run, ledger, decision, "run.budget_work_reserved")
	if err != nil {
		return BudgetResult{}, err
	}
	return BudgetResult{Run: next, Decision: decision, ReservationID: reservationID, Replay: replay}, nil
}

// RecordProviderUsage persists one exact provider snapshot and releases its
// matching reservation. Missing/ambiguous observations are persisted and park
// fail closed; they are never repaired by a pricing estimate.
func (controller *Controller) RecordProviderUsage(
	ctx context.Context, runID string, expectedRunVersion, leaseEpoch uint64,
	observation runtimebudget.ProviderObservation,
) (BudgetResult, error) {
	run, err := controller.store.Run(ctx, runID)
	if err != nil {
		return BudgetResult{}, err
	}
	if run.Execution.LeaseBinding.Epoch != leaseEpoch || observation.LeaseEpoch != leaseEpoch {
		return BudgetResult{}, ErrRuntimeBudgetLeaseFenced
	}
	if run.Version != expectedRunVersion {
		return BudgetResult{}, errors.New("provider usage observation is stale or lease-fenced")
	}
	if err := controller.currentExecutionAuthority(ctx, run, observation.ObservedAtMillis); err != nil {
		return BudgetResult{}, err
	}
	ledger, decision, err := runtimebudget.ApplyProviderObservation(run.Execution.Budget, observation)
	if err != nil {
		return BudgetResult{}, err
	}
	replay := ledgerEqual(run.Execution.Budget, ledger)
	next, err := controller.persistBudgetResult(ctx, run, ledger, decision, "run.provider_usage_recorded")
	return BudgetResult{Run: next, Decision: decision, Replay: replay}, err
}

// RecordBudgetActivity persists bounded non-provider setup, Validation, and
// replacement accounting with the same Run/lease fencing.
func (controller *Controller) RecordBudgetActivity(
	ctx context.Context, runID string, expectedRunVersion, leaseEpoch uint64,
	observation runtimebudget.ActivityObservation,
) (BudgetResult, error) {
	run, err := controller.store.Run(ctx, runID)
	if err != nil {
		return BudgetResult{}, err
	}
	if run.Execution.LeaseBinding.Epoch != leaseEpoch || observation.LeaseEpoch != leaseEpoch {
		return BudgetResult{}, ErrRuntimeBudgetLeaseFenced
	}
	if run.Version != expectedRunVersion {
		return BudgetResult{}, errors.New("budget activity observation is stale or lease-fenced")
	}
	if err := controller.currentExecutionAuthority(ctx, run, observation.ObservedAtMillis); err != nil {
		return BudgetResult{}, err
	}
	ledger, decision, err := runtimebudget.ApplyActivityObservation(run.Execution.Budget, observation)
	if err != nil {
		return BudgetResult{}, err
	}
	replay := ledgerEqual(run.Execution.Budget, ledger)
	next, err := controller.persistBudgetResult(ctx, run, ledger, decision, "run.budget_activity_recorded")
	return BudgetResult{Run: next, Decision: decision, Replay: replay}, err
}

// AcknowledgeBudgetWarning is the only unchanged-limit resume path. It
// accepts only a server-authenticated human command and clears only the exact
// matching soft-budget park after persisting the acknowledgement.
func (controller *Controller) AcknowledgeBudgetWarning(
	ctx context.Context, command BudgetAcknowledgementCommand,
) (BudgetResult, error) {
	if !identifierPattern.MatchString(command.RequestID) || !identifierPattern.MatchString(command.RunID) ||
		command.ActorKind != "human" || command.Source != "authenticated_engine_command" ||
		!identifierPattern.MatchString(command.ActorID) || command.NowMillis < 0 {
		return BudgetResult{}, errors.New("budget acknowledgement command is invalid")
	}
	run, err := controller.store.Run(ctx, command.RunID)
	if err != nil {
		return BudgetResult{}, err
	}
	if run.Execution.LeaseBinding.Epoch != command.LeaseEpoch {
		return BudgetResult{}, ErrRuntimeBudgetLeaseFenced
	}
	if run.Version != command.ExpectedRunVersion {
		return BudgetResult{}, errors.New("budget acknowledgement command is stale")
	}
	if err := controller.currentExecutionAuthority(ctx, run, command.NowMillis); err != nil {
		return BudgetResult{}, err
	}
	ledger, err := runtimebudget.AcknowledgeSoftWarning(
		run.Execution.Budget, command.WarningID, command.PolicyRevision, command.ActorID, command.NowMillis,
	)
	if err != nil {
		return BudgetResult{}, err
	}
	next := run
	next.Execution.Budget = ledger
	if next.Execution.NeedsYou != nil {
		code := runtimebudget.ReasonCode(next.Execution.NeedsYou.Code)
		if code == runtimebudget.ReasonWallTimeSoft || code == runtimebudget.ReasonTokensSoft ||
			code == runtimebudget.ReasonTurnsSoft || code == runtimebudget.ReasonCostSoft {
			next.Execution.NeedsYou = nil
		}
	}
	if err := controller.persistRun(ctx, run, next, "run.budget_warning_acknowledged"); err != nil {
		return BudgetResult{}, err
	}
	next.Version = run.Version + 1
	return BudgetResult{Run: next, Decision: runtimebudget.Decision{Disposition: runtimebudget.DispositionAllow}}, nil
}

// SortedCriteria returns the stable criterion IDs retained by a completed
// claim. It is useful to projections without exposing mutable map iteration.
func SortedCriteria(claim domainexecution.CompletedClaim) []string {
	identifiers := make([]string, 0, len(claim.CriteriaResults))
	for identifier := range claim.CriteriaResults {
		identifiers = append(identifiers, identifier)
	}
	sort.Strings(identifiers)
	return identifiers
}
