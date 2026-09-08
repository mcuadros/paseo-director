// SPDX-License-Identifier: Apache-2.0

// Package execution composes the pure M1 reducers with the TaskStore and thin
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
	"sort"
	"strings"
	"time"

	"github.com/mcuadros/director-engine/domain"
	domainexecution "github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/ports/host"
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
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]*$`)
	shaPattern        = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// Controller is restartable: it owns no workflow state outside TaskStore.
type Controller struct {
	store   storeport.TaskStore
	runtime runtimeport.Port
	host    host.Port
}

// NewController wires the engine-owned ports without importing an adapter.
func NewController(store storeport.TaskStore, runtime runtimeport.Port, hostPort host.Port) *Controller {
	return &Controller{store: store, runtime: runtime, host: hostPort}
}

// StartCommand freezes every identity and fact needed by the fake M1 Run.
type StartCommand struct {
	RequestID        string
	Scope            domainexecution.Scope
	RunNumber        uint64
	SourcePath       string
	WorktreePath     string
	Branch           string
	BaseSHA          string
	TaskTitle        string
	CriterionIDs     []string
	InitialPrompt    string
	EligibilityFacts eligibility.Facts
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

func stableID(prefix string, parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return prefix + "-" + hex.EncodeToString(digest[:16])
}

func hashText(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func repositoryBindingHash(scope domainexecution.Scope, sourcePath, worktreePath, branch, baseSHA string) string {
	return hashText(strings.Join([]string{
		scope.ProjectID, scope.WorkspaceID, scope.TaskID, scope.RunID,
		sourcePath, worktreePath, branch, baseSHA,
	}, "\x1f"))
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

func validStart(command StartCommand, project domain.Project, task domain.Task) bool {
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
	return identifierPattern.MatchString(command.RequestID) && command.RunNumber > 0 &&
		command.Scope.ProjectID == project.ID && command.Scope.TaskID == task.ID &&
		identifierPattern.MatchString(command.Scope.WorkspaceID) && identifierPattern.MatchString(command.Scope.RunID) &&
		command.EligibilityFacts.Scope == command.Scope &&
		command.BaseSHA != "" && shaPattern.MatchString(command.BaseSHA) &&
		len(command.SourcePath) <= 4_096 && filepath.IsAbs(command.SourcePath) && filepath.Clean(command.SourcePath) == command.SourcePath &&
		len(command.WorktreePath) <= 4_096 && filepath.IsAbs(command.WorktreePath) && filepath.Clean(command.WorktreePath) == command.WorktreePath &&
		command.SourcePath != command.WorktreePath && identifierPattern.MatchString(command.Branch) &&
		!strings.Contains(command.Branch, "..") && !strings.Contains(command.Branch, "//") &&
		command.TaskTitle == task.Title && strings.TrimSpace(command.InitialPrompt) != "" && len(command.InitialPrompt) <= 16*1_024
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
	if !validStart(command, project, task) {
		return StartResult{}, errors.New("invalid fake execution start command")
	}
	decision := eligibility.Reduce(command.EligibilityFacts)
	result := StartResult{Decision: decision}
	if existing, err := controller.store.Run(ctx, command.Scope.RunID); err == nil {
		if existing.TaskID != task.ID || existing.BaseSHA != command.BaseSHA ||
			existing.Execution.EligibilityDecisionID != decision.DecisionID ||
			existing.Execution.EligibilityFactsHash != decision.FactsHash {
			return StartResult{}, errors.New("existing Run conflicts with start command")
		}
		result.Decision.Kind = eligibility.DecisionEligible
		result.RunID = existing.ID
		return result, nil
	} else if !errors.Is(err, storeport.ErrNotFound) {
		return StartResult{}, err
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
	state := domainexecution.State{
		SchemaVersion: domainexecution.SchemaVersion, Scope: command.Scope,
		EligibilityDecisionVersion: eligibility.SchemaVersion,
		EligibilityDecisionID:      decision.DecisionID, EligibilityFactsHash: decision.FactsHash,
		CapacityReservationID: stableID("capacity", command.Scope.RunID),
		BudgetReservationID:   stableID("budget", command.Scope.RunID),
		RepositoryBindingHash: repositoryBindingHash(
			command.Scope, command.SourcePath, command.WorktreePath, command.Branch, command.BaseSHA,
		),
		LifecycleDigest: decision.LifecycleDigest, IsolationDigest: decision.IsolationDigest,
		LifecycleApproval: command.EligibilityFacts.LifecycleApproval,
		Isolation:         command.EligibilityFacts.Isolation,
		OperationalPolicy: command.EligibilityFacts.OperationalPolicy,
		LifecycleSurfaces: command.EligibilityFacts.LifecycleSurfaces,
		SourcePath:        command.SourcePath, WorktreePath: command.WorktreePath,
		Branch: command.Branch, TaskTitle: command.TaskTitle,
		CriterionIDs:  append([]string(nil), command.CriterionIDs...),
		InitialPrompt: command.InitialPrompt, InitialPromptHash: hashText(command.InitialPrompt),
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
			state.LifecycleDigest, state.IsolationDigest, state.InitialPromptHash,
			strings.Join(state.CriterionIDs, "\x1e"),
		}, "\x1f")),
	)
	run := domain.Run{
		ID: command.Scope.RunID, TaskID: task.ID, Number: command.RunNumber,
		BaseSHA: command.BaseSHA, Execution: state,
	}
	commandID := stableID("command", command.RequestID, "run-create")
	storeResult, err := controller.store.CreateRun(ctx, domain.CommandRequest{
		IdempotencyKey: commandID, Type: "run.fake_execution.create", AggregateID: run.ID,
		Payload: eventPayload(struct {
			EligibilityDecisionID string `json:"eligibilityDecisionId"`
			LifecycleDigest       string `json:"lifecycleDigest"`
			IsolationDigest       string `json:"isolationDigest"`
		}{decision.DecisionID, decision.LifecycleDigest, decision.IsolationDigest}),
	}, run, domain.Event{
		ID: stableID("event", commandID), RunID: run.ID, Sequence: 1,
		AggregateID: run.ID, AggregateVersion: 0, Type: "run.fake_execution.created",
		Payload: eventPayload(struct {
			EligibilityDecisionID string `json:"eligibilityDecisionId"`
		}{decision.DecisionID}),
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

func (controller *Controller) persistRun(ctx context.Context, current, next domain.Run, transition string) error {
	next.Version = current.Version + 1
	commandID := stableID("command", current.ID, fmt.Sprintf("version-%d", next.Version), transition)
	result, err := controller.store.UpdateRun(ctx, domain.CommandRequest{
		IdempotencyKey: commandID, Type: transition, AggregateID: current.ID,
		ExpectedVersion: current.Version,
		Payload: eventPayload(struct {
			Transition string `json:"transition"`
			StateHash  string `json:"stateHash"`
		}{transition, hashState(next.Execution)}),
	}, next, domain.Event{
		ID: stableID("event", commandID), RunID: current.ID, Sequence: current.Version + 2,
		AggregateID: current.ID, AggregateVersion: next.Version, Type: transition,
		Payload: durableTransitionPayload(transition, next.Execution),
	})
	if err != nil {
		return err
	}
	if result.Outcome != domain.CommandApplied {
		return errors.New("persist execution transition: version conflict")
	}
	return nil
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
		state.Worktree, state.HostView, state.Boundary, state.Setup, state.Agent,
		state.AgentArchive, state.HostViewArchive, state.WorktreeRemove,
	} {
		if effect.Observation != nil {
			observation := *effect.Observation
			return &observation
		}
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
		state.Worktree, state.HostView, state.Boundary, state.Setup, state.Agent,
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
		FrozenInputsHash:      state.RepositoryBindingHash,
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
	case domainexecution.EffectHostViewCreate, domainexecution.EffectAgentCreate,
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
	return host.Arguments{
		Scope: state.Scope, EffectKind: effect.Kind, EffectID: effect.ID,
		WorktreeID: state.Worktree.ExternalID, WorktreePath: state.WorktreePath,
		WorkspaceID: state.HostView.ExternalID, AgentID: state.Agent.ExternalID,
		Title: state.TaskTitle, InitialPrompt: state.InitialPrompt,
		ParentAgentID: nil, LifecycleDigest: state.LifecycleDigest,
		IsolationDigest: state.IsolationDigest, PreparationReady: state.PreparationReady,
		PreparationBarrierHash: state.PreparationBarrierHash,
		BindingHash:            state.RepositoryBindingHash,
	}
}

func runtimeRequest(run domain.Run, effect domainexecution.Effect) runtimeport.Request {
	state := run.Execution
	return runtimeport.Request{
		Scope: state.Scope, Effect: effect, SourcePath: state.SourcePath,
		WorktreePath: state.WorktreePath, Branch: state.Branch, BaseSHA: run.BaseSHA,
		WorktreeID:        state.Worktree.ExternalID,
		BindingHash:       state.RepositoryBindingHash,
		LifecycleSurfaces: state.LifecycleSurfaces,
		LifecycleApproval: state.LifecycleApproval,
		LifecycleDigest:   state.LifecycleDigest,
		Isolation:         state.Isolation, IsolationDigest: state.IsolationDigest,
	}
}

func (controller *Controller) verifyHost(ctx context.Context) error {
	descriptor, err := controller.host.Describe(ctx)
	if err != nil {
		return err
	}
	return host.ValidateDescriptor(descriptor)
}

func (controller *Controller) observeEffect(ctx context.Context, run domain.Run, kind domainexecution.EffectKind) (domainexecution.EffectObservation, error) {
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
			Arguments: hostArguments(run, *effect),
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
			Cursor: observed.Cursor, ObservedAt: observed.ObservedAt,
			ObservedAtMillis: observedAt.UnixMilli(), MaximumAgeMillis: observed.Result.MaximumAgeMillis,
			PriorDispatcherAbsent: observed.Result.PriorDispatcherAbsent,
		}
		normalized.FactHash = domainexecution.EffectObservationHash(normalized)
		return normalized, nil
	}
	return controller.runtime.ObserveEffect(ctx, runtimeRequest(run, *effect))
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
		_, err := controller.host.Invoke(ctx, host.Command{
			RequestID:      stableID("request", effect.ID, fmt.Sprintf("attempt-%d", effect.Attempt)),
			IdempotencyKey: effect.ID, ExpectedVersion: run.Version,
			Capability: hostCapability(kind, false), Arguments: hostArguments(run, *effect),
		})
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

func (controller *Controller) applyEffectDecision(
	ctx context.Context,
	run domain.Run,
	kind domainexecution.EffectKind,
	action string,
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
		if err := controller.persistRun(ctx, run, next, "run.effect_observed"); err != nil {
			return StepResult{Run: run}, err
		}
		return StepResult{Run: next, Progressed: true}, nil
	case "dispatch":
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
		if err := controller.persistRun(ctx, run, next, "run.effect_dispatching"); err != nil {
			return StepResult{Run: run}, err
		}
		next.Version = run.Version + 1
		err := controller.dispatchEffect(ctx, next, kind)
		return StepResult{Run: next, Progressed: true}, err
	case "adopt":
		next := run
		effect := effectPointer(&next.Execution, kind)
		effect.Phase = domainexecution.EffectComplete
		effect.ExternalID = effect.Observation.ExternalID
		effect.Observation = nil
		next.Execution.OperationalObservationConsumed = true
		if err := controller.persistRun(ctx, run, next, "run.effect_completed"); err != nil {
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
		return controller.applyEffectDecision(ctx, run, kind, "dispatch")
	case retry.DecisionAdopt:
		return controller.applyEffectDecision(ctx, run, kind, "adopt")
	case retry.DecisionEscalate:
		return StepResult{Run: run, Progressed: true}, controller.parkRun(ctx, run, domainexecution.NeedCode(decision.Code))
	default:
		return StepResult{Run: run}, errors.New("unknown retry decision")
	}
}

func (controller *Controller) stepLaunch(ctx context.Context, run domain.Run, nowMillis int64) (StepResult, error) {
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
		ExactRepositoryBinding: run.Execution.RepositoryBindingHash == repositoryBindingHash(
			run.Execution.Scope, run.Execution.SourcePath, run.Execution.WorktreePath,
			run.Execution.Branch, run.BaseSHA,
		) && launchBindingsExact(run.Execution),
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
		TaskStoreNowMillis:        nowMillis,
		Worktree:                  run.Execution.Worktree, HostView: run.Execution.HostView,
		Boundary: run.Execution.Boundary, Setup: run.Execution.Setup, Agent: run.Execution.Agent,
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
		return controller.applyEffectDecision(ctx, run, decision.EffectKind, "observe")
	case launch.DecisionDispatch:
		return controller.applyEffectDecision(ctx, run, decision.EffectKind, "dispatch")
	case launch.DecisionAdopt:
		return controller.applyEffectDecision(ctx, run, decision.EffectKind, "adopt")
	case launch.DecisionRetry:
		return controller.applyRetryDecision(ctx, run, decision.EffectKind, nowMillis)
	case launch.DecisionCommitPreparationReady:
		next := run
		next.Execution.PreparationReady = true
		next.Execution.PreparationBarrierHash = decision.PreparationBarrierHash
		next.Execution.OperationalObservationConsumed = true
		return StepResult{Run: next, Progressed: true}, controller.persistRun(ctx, run, next, "run.preparation_ready")
	case launch.DecisionCreateAgentIntent:
		next := run
		next.Execution.Agent = newEffect(run.ID, domainexecution.EffectAgentCreate, 2)
		next.Execution.OperationalObservationConsumed = true
		return StepResult{Run: next, Progressed: true}, controller.persistRun(ctx, run, next, "run.agent_intent_recorded")
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

func (controller *Controller) stepClosure(ctx context.Context, run domain.Run, nowMillis int64) (StepResult, error) {
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
			run.Execution.RepositoryBindingHash == repositoryBindingHash(
				run.Execution.Scope, run.Execution.SourcePath, run.Execution.WorktreePath,
				run.Execution.Branch, run.BaseSHA,
			),
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
		return controller.applyEffectDecision(ctx, run, decision.EffectKind, "observe")
	case closure.DecisionDispatch:
		return controller.applyEffectDecision(ctx, run, decision.EffectKind, "dispatch")
	case closure.DecisionAdopt:
		return controller.applyEffectDecision(ctx, run, decision.EffectKind, "adopt")
	case closure.DecisionRetry:
		return controller.applyRetryDecision(ctx, run, decision.EffectKind, nowMillis)
	case closure.DecisionTerminal:
		next := run
		next.Execution.Terminal = true
		next.Execution.OperationalObservationConsumed = true
		return StepResult{Run: next, Progressed: true}, controller.persistRun(ctx, run, next, "run.fake_execution_terminal")
	default:
		return StepResult{Run: run}, errors.New("unknown closure decision")
	}
}

// Step executes at most one durable transition or one already-persisted
// adapter handoff. Constructing a new Controller between calls is supported.
func (controller *Controller) Step(ctx context.Context, runID string, nowMillis int64) (StepResult, error) {
	run, err := controller.store.Run(ctx, runID)
	if err != nil {
		return StepResult{}, err
	}
	if run.Execution.NeedsYou != nil || run.Execution.Terminal {
		return StepResult{Run: run}, nil
	}
	if run.CurrentCandidateID != "" {
		return controller.stepClosure(ctx, run, nowMillis)
	}
	if run.Execution.Agent.Phase == domainexecution.EffectComplete {
		return controller.stepRouting(ctx, run, nowMillis)
	}
	return controller.stepLaunch(ctx, run, nowMillis)
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
