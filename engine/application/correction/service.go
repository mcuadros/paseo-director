// SPDX-License-Identifier: Apache-2.0

// Package correction implements the engine-owned correction-cycle vertical.
// Host and Git ports expose observations/effects only; every routing and
// invalidation decision remains in the Go engine.
package correction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/candidate"
	domaincorrection "github.com/mcuadros/director-engine/domain/correction"
	"github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/domain/runtimebudget"
	gitport "github.com/mcuadros/director-engine/ports/git"
	"github.com/mcuadros/director-engine/ports/host"
	correctionreducer "github.com/mcuadros/director-engine/reducer/correction"
)

var (
	ErrInvalidCommand       = errors.New("correction command is invalid")
	ErrConcurrentTransition = errors.New("correction transition lost an expected-version race")
	ErrCorrectionAmbiguous  = errors.New("correction external state is ambiguous")
	ErrCorrectionNotReady   = errors.New("correction is not ready for this transition")
)

type Store interface {
	Project(context.Context, string) (domain.Project, error)
	Task(context.Context, string) (domain.Task, error)
	Run(context.Context, string) (domain.Run, error)
	Candidate(context.Context, string) (domain.Candidate, error)
	Candidates(context.Context, string) ([]domain.Candidate, error)
	UpdateRun(context.Context, domain.CommandRequest, domain.Run, domain.Event) (domain.CommandResult, error)
	AppendCandidate(context.Context, domain.CommandRequest, domain.Candidate, domain.Event) (domain.CommandResult, error)
}

type Service struct {
	store Store
	git   gitport.CandidateObserver
	host  host.Port
}

func NewService(store Store, git gitport.CandidateObserver, hostPort host.Port) (*Service, error) {
	if store == nil || git == nil || hostPort == nil {
		return nil, errors.New("correction store, Git port, and host port are required")
	}
	return &Service{store: store, git: git, host: hostPort}, nil
}

type IngestCommand struct {
	RunID                 string
	ExpectedRunVersion    uint64
	LeaseEpoch            uint64
	OriginalTaskAgentUUID string
	CriterionIDs          []string
	FrozenPlanDigest      string
	CurrentPlanDigest     string
	FrozenSkillSetDigest  string
	CurrentSkillSetDigest string
	CurrentDecisionDigest string
	CurrentDiffDigest     string
	Policy                domaincorrection.Policy
	SourceSnapshots       []domaincorrection.SourceSnapshot
	Findings              []domaincorrection.FindingInput
	NowMillis             int64
}

type TransitionCommand struct {
	RunID              string
	ExpectedRunVersion uint64
	LeaseEpoch         uint64
	NowMillis          int64
}

type OutputCommand struct {
	TransitionCommand
	Output domaincorrection.Output
}

type Result struct {
	Run        domain.Run
	Progressed bool
	Replayed   bool
	Code       string
}

func stableID(prefix string, values ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(values, "\x1f")))
	return prefix + "-" + hex.EncodeToString(sum[:16])
}

func digest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal fixed correction application value: " + err.Error())
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func eventPayload(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal fixed correction event: " + err.Error())
	}
	return encoded
}

func currentLease(project domain.Project, run domain.Run, nowMillis int64) bool {
	lease := project.Lease
	binding := run.Execution.LeaseBinding
	return project.ID == run.Execution.Scope.ProjectID && project.State == "active" && lease != nil &&
		domain.ValidateProjectLease(lease) == nil && execution.ValidLeaseBinding(binding) && lease.DispatchAllowed &&
		lease.HolderInstance == binding.HolderInstance && lease.HolderProcessIdentity == binding.HolderProcessIdentity &&
		lease.Epoch == binding.Epoch && nowMillis >= lease.AcquiredAtMillis && nowMillis < lease.ExpiresAtMillis
}

func (service *Service) load(ctx context.Context, command TransitionCommand) (domain.Project, domain.Task, domain.Run, domain.Candidate, error) {
	run, err := service.store.Run(ctx, command.RunID)
	if err != nil {
		return domain.Project{}, domain.Task{}, domain.Run{}, domain.Candidate{}, err
	}
	if run.Version != command.ExpectedRunVersion || command.LeaseEpoch == 0 || run.Execution.LeaseBinding.Epoch != command.LeaseEpoch || command.NowMillis < 0 {
		return domain.Project{}, domain.Task{}, run, domain.Candidate{}, ErrConcurrentTransition
	}
	task, err := service.store.Task(ctx, run.TaskID)
	if err != nil {
		return domain.Project{}, domain.Task{}, run, domain.Candidate{}, err
	}
	project, err := service.store.Project(ctx, task.ProjectID)
	if err != nil {
		return domain.Project{}, task, run, domain.Candidate{}, err
	}
	if !currentLease(project, run, command.NowMillis) {
		return project, task, run, domain.Candidate{}, ErrConcurrentTransition
	}
	if run.CurrentCandidateID == "" || run.Execution.CandidateAuthority == nil || run.Execution.CandidateAuthority.Invalidated ||
		!candidate.ValidAuthority(*run.Execution.CandidateAuthority) {
		return project, task, run, domain.Candidate{}, ErrCorrectionNotReady
	}
	record, err := service.store.Candidate(ctx, run.CurrentCandidateID)
	if err != nil {
		return project, task, run, domain.Candidate{}, err
	}
	if record.RunID != run.ID || record.ID != run.Execution.CandidateAuthority.CandidateID || record.CommitSHA != run.Execution.CandidateAuthority.CandidateSHA ||
		record.Manifest.BindingSHA256 != run.Execution.CandidateAuthority.BindingSHA256 {
		return project, task, run, record, ErrCorrectionAmbiguous
	}
	return project, task, run, record, nil
}

func (service *Service) persist(ctx context.Context, current, next domain.Run, transition string) (domain.Run, error) {
	if next.Execution.Correction == nil || !domaincorrection.ValidState(*next.Execution.Correction) || !runtimebudget.ValidLedger(next.Execution.Budget) {
		return current, ErrInvalidCommand
	}
	next.Version = current.Version + 1
	commandID := stableID("command", current.ID, fmt.Sprintf("version-%d", next.Version), transition)
	state := next.Execution.Correction
	result, err := service.store.UpdateRun(ctx, domain.CommandRequest{
		IdempotencyKey: commandID, Type: transition, AggregateID: current.ID, ExpectedVersion: current.Version,
		Payload: eventPayload(struct{ Transition, StateSHA256 string }{transition, digest(state)}),
	}, next, domain.Event{
		ID: stableID("event", commandID), RunID: current.ID, Sequence: current.Version + 2,
		AggregateID: current.ID, AggregateVersion: next.Version, Type: transition,
		Payload: eventPayload(struct{ LineageKey, Phase, BatchSHA256 string }{state.LineageKey, string(state.Phase), state.Batches[len(state.Batches)-1].SHA256}),
	})
	if err != nil {
		return current, err
	}
	if result.Outcome != domain.CommandApplied || result.Replay {
		return current, ErrConcurrentTransition
	}
	return next, nil
}

func (service *Service) park(ctx context.Context, run domain.Run, code, wake string) (Result, error) {
	if run.Execution.Correction == nil {
		return Result{Run: run}, ErrInvalidCommand
	}
	next := run
	state := domaincorrection.CloneState(*run.Execution.Correction)
	state.Phase, state.NeedsYouCode, state.WakeCondition = domaincorrection.PhaseNeedsYou, code, wake
	next.Execution.Correction = &state
	next.Execution.NeedsYou = &execution.NeedsYou{Code: execution.NeedCode(code), WakeCondition: wake, CleanupAuthorized: false}
	next, err := service.persist(ctx, run, next, "correction.needs_you."+code)
	return Result{Run: next, Progressed: err == nil, Code: code}, err
}

func validIngest(command IngestCommand) bool {
	return command.RunID != "" && command.LeaseEpoch > 0 && command.NowMillis >= 0 &&
		command.FrozenPlanDigest == command.CurrentPlanDigest && command.FrozenSkillSetDigest == command.CurrentSkillSetDigest
}

// Ingest canonicalizes all four current source snapshots into one immutable
// batch. It performs no prompt, CI, Review, publication, or cleanup effect.
func (service *Service) Ingest(ctx context.Context, command IngestCommand) (Result, error) {
	if !validIngest(command) {
		return Result{}, ErrInvalidCommand
	}
	_, task, run, record, err := service.load(ctx, TransitionCommand{
		RunID: command.RunID, ExpectedRunVersion: command.ExpectedRunVersion,
		LeaseEpoch: command.LeaseEpoch, NowMillis: command.NowMillis,
	})
	if err != nil {
		return Result{Run: run}, err
	}
	if command.OriginalTaskAgentUUID != run.Execution.Agent.ExternalID || run.Execution.PrimarySession.NativeAgentID != run.Execution.Agent.ExternalID ||
		!slices.Equal(command.CriterionIDs, run.Execution.CriterionIDs) || command.CurrentDecisionDigest == "" || command.CurrentDiffDigest == "" ||
		command.CurrentDecisionDigest != run.Execution.DecisionContextSHA256 {
		return Result{Run: run}, ErrInvalidCommand
	}
	batch, ok := domaincorrection.Canonicalize(record.ID, record.CommitSHA, command.CriterionIDs, command.SourceSnapshots, command.Findings)
	if !ok {
		return Result{Run: run}, ErrInvalidCommand
	}
	var state domaincorrection.State
	if run.Execution.Correction == nil {
		state, ok = domaincorrection.NewState(task.ID, run.ID, record.ID, record.CommitSHA, command.OriginalTaskAgentUUID,
			command.CriterionIDs, command.FrozenPlanDigest, command.FrozenSkillSetDigest,
			command.CurrentDecisionDigest, command.CurrentDiffDigest, command.Policy, batch)
		if !ok {
			return Result{Run: run}, ErrInvalidCommand
		}
	} else {
		state = domaincorrection.CloneState(*run.Execution.Correction)
		if !domaincorrection.ValidState(state) || state.OriginalTaskAgentUUID != command.OriginalTaskAgentUUID ||
			state.FrozenPlanDigest != command.FrozenPlanDigest || state.FrozenSkillSetDigest != command.FrozenSkillSetDigest || state.Policy != command.Policy ||
			state.CurrentCandidateID != record.ID || state.CurrentCandidateSHA != record.CommitSHA || state.Phase == domaincorrection.PhaseNeedsYou ||
			state.Phase == domaincorrection.PhasePrompt || state.Phase == domaincorrection.PhaseAwaitingOutput ||
			state.Phase == domaincorrection.PhaseCandidatePending || state.Phase == domaincorrection.PhaseCandidateObserved {
			return Result{Run: run}, ErrCorrectionNotReady
		}
		current, _ := domaincorrection.CurrentBatch(state)
		if current.SHA256 == batch.SHA256 {
			return Result{Run: run, Replayed: true}, nil
		}
		currentBound := slices.ContainsFunc(state.Attempts, func(attempt domaincorrection.Attempt) bool {
			return attempt.BatchSHA256 == current.SHA256
		})
		if !currentBound {
			state.Batches[len(state.Batches)-1] = batch
		} else {
			if len(state.Batches) >= int(domaincorrection.MaximumBatches) {
				return Result{Run: run}, ErrCorrectionNotReady
			}
			state.Batches = append(slices.Clone(state.Batches), batch)
		}
		state.CurrentDecisionDigest, state.CurrentDiffDigest = command.CurrentDecisionDigest, command.CurrentDiffDigest
		state.Phase = domaincorrection.PhaseReady
	}
	next := run
	next.Execution.Correction = &state
	next, err = service.persist(ctx, run, next, "correction.batch_ingested")
	if err != nil {
		return Result{Run: run}, err
	}
	if batch.RequiresHumanDecision {
		code := correctionreducer.CodeHumanDecisionRequired
		if slices.ContainsFunc(batch.Findings, func(finding domaincorrection.Finding) bool {
			return finding.RequiresHumanDecision && finding.Severity == domaincorrection.SeverityP2
		}) {
			code = correctionreducer.CodeP2DecisionRequired
		}
		return service.park(ctx, next, code, "explicit_human_correction_decision")
	}
	return Result{Run: next, Progressed: true}, nil
}

func ciBudgetAvailable(ledger runtimebudget.Ledger) bool {
	_, reserved, ok := runtimebudget.Outstanding(ledger)
	return ok && ledger.Consumption.CICycles+reserved.CICycles < ledger.Policy.CICycleLimit
}

func previousBlockingClasses(state domaincorrection.State) map[string]struct{} {
	seen := map[string]struct{}{}
	for index := 0; index+1 < len(state.Batches); index++ {
		for _, class := range state.Batches[index].BlockingClasses {
			seen[class] = struct{}{}
		}
	}
	return seen
}

func newBlockingClasses(state domaincorrection.State, batch domaincorrection.Batch) []string {
	seen := previousBlockingClasses(state)
	var result []string
	for _, class := range batch.BlockingClasses {
		if _, exists := seen[class]; !exists {
			result = append(result, class)
		}
	}
	return result
}

func repeatingRootCause(state domaincorrection.State, batch domaincorrection.Batch) bool {
	if state.AcknowledgementRejections > 0 || len(state.Attempts) == 0 {
		return false
	}
	last := state.Attempts[len(state.Attempts)-1]
	return last.Classification == domaincorrection.ClassificationChurn && last.ClassFingerprint == batch.ClassFingerprint
}

func reducerFacts(state domaincorrection.State, run domain.Run, plan, skills string, output *domaincorrection.Output, nowMillis int64) (correctionreducer.Facts, runtimebudget.Ledger) {
	ledger, budget := runtimebudget.Evaluate(run.Execution.Budget, nowMillis)
	batch, _ := domaincorrection.CurrentBatch(state)
	provider := correctionreducer.ProviderCurrent
	if ledger.TelemetryState == runtimebudget.UsageUnavailable {
		provider = correctionreducer.ProviderUnavailable
	}
	if ledger.TelemetryState == runtimebudget.UsageAmbiguous {
		provider = correctionreducer.ProviderAmbiguous
	}
	return correctionreducer.Facts{
		SchemaVersion: correctionreducer.SchemaVersion, State: state,
		CurrentTaskAgentUUID: run.Execution.Agent.ExternalID, CurrentCandidateID: state.CurrentCandidateID,
		CurrentCandidateSHA: state.CurrentCandidateSHA, CurrentPlanDigest: plan, CurrentSkillSetDigest: skills,
		CurrentDecisionDigest: state.CurrentDecisionDigest, CurrentDiffDigest: state.CurrentDiffDigest,
		ProviderState: provider, BudgetDisposition: budget.Disposition, BudgetReason: budget.Reason,
		CIBudgetAvailable: ciBudgetAvailable(ledger), RepeatingRootCause: repeatingRootCause(state, batch),
		NewBlockingClasses: newBlockingClasses(state, batch), Output: output,
	}, ledger
}

// AdmitAttempt reserves m3.6 time/cost/token/turn/correction capacity and
// persists one deterministic prompt intent before any host handoff.
func (service *Service) AdmitAttempt(ctx context.Context, command TransitionCommand) (Result, error) {
	_, _, run, _, err := service.load(ctx, command)
	if err != nil {
		return Result{Run: run}, err
	}
	if run.Execution.Correction == nil {
		return Result{Run: run}, ErrCorrectionNotReady
	}
	state := domaincorrection.CloneState(*run.Execution.Correction)
	if state.Phase != domaincorrection.PhaseReady || run.Execution.NeedsYou != nil {
		return Result{Run: run}, ErrCorrectionNotReady
	}
	facts, evaluatedBudget := reducerFacts(state, run, state.FrozenPlanDigest, state.FrozenSkillSetDigest, nil, command.NowMillis)
	run.Execution.Budget = evaluatedBudget
	decision := correctionreducer.Reduce(facts)
	if decision.Kind == correctionreducer.DecisionEscalate {
		return service.park(ctx, run, decision.Code, "human_review_or_fresh_correction_facts")
	}
	batch, _ := domaincorrection.CurrentBatch(state)
	number := uint32(len(state.Attempts) + 1)
	key := domaincorrection.AttemptKey(state.LineageKey, batch.SHA256, number, decision.AttemptReason)
	effectID := domaincorrection.PromptEffectID(key)
	reservationID := stableID("budget-reservation", key, effectID)
	fingerprint := digest(struct {
		Class, Coverage string
		Reason          domaincorrection.AttemptReason
	}{
		batch.ClassFingerprint, batch.CoverageFingerprint, decision.AttemptReason,
	})
	ledger, budget, reserveErr := runtimebudget.Reserve(run.Execution.Budget, runtimebudget.ReserveRequest{
		ID: reservationID, EffectID: effectID, Activity: runtimebudget.ActivityCorrectionTurn,
		LeaseEpoch: command.LeaseEpoch, PolicyRevision: run.Execution.Budget.Policy.Revision,
		Demand: run.Execution.TurnBudgetDemand, CandidateSHA: batch.CandidateSHA, Fingerprint: fingerprint,
	}, command.NowMillis)
	if reserveErr != nil {
		return Result{Run: run}, reserveErr
	}
	if budget.Disposition != runtimebudget.DispositionAllow {
		next := run
		next.Execution.Budget = ledger
		next.Execution.Correction = &state
		next, persistErr := service.persist(ctx, run, next, "correction.budget_reconciled")
		if persistErr != nil {
			return Result{Run: run}, persistErr
		}
		return service.park(ctx, next, string(budget.Reason), "human_budget_decision")
	}
	attempt := domaincorrection.Attempt{
		Number: number, Key: key, Reason: decision.AttemptReason, BatchSHA256: batch.SHA256,
		ClassFingerprint: batch.ClassFingerprint, CandidateBeforeID: batch.CandidateID, CandidateBeforeSHA: batch.CandidateSHA,
		Prompt: domaincorrection.Effect{ID: effectID, Phase: domaincorrection.EffectIntent}, BudgetReservationID: reservationID,
		CoverageDelta: domaincorrection.CoverageDelta{Added: []string{}, Removed: []string{}},
	}
	state.Attempts = append(slices.Clone(state.Attempts), attempt)
	state.Phase = domaincorrection.PhasePrompt
	next := run
	next.Execution.Correction, next.Execution.Budget = &state, ledger
	next, err = service.persist(ctx, run, next, "correction.prompt_intent_recorded")
	return Result{Run: next, Progressed: err == nil}, err
}

func primaryContext(run domain.Run) (*host.AgentProfile, *host.MCPSession) {
	session := run.Execution.PrimarySession
	profile := &host.AgentProfile{
		Provider: string(session.Provider), Model: session.Model, Effort: session.Effort, Mode: session.Mode,
		PermissionMode: session.PermissionMode, SHA256: run.Execution.EffectiveProfilesSHA256,
	}
	for _, option := range session.ProviderOptions {
		profile.ProviderOptions = append(profile.ProviderOptions, host.ProviderOption{Name: string(option.Name), Value: option.Value})
	}
	serverEnv := make(map[string]string, len(session.MCPServer.Env))
	for key, value := range session.MCPServer.Env {
		serverEnv[key] = value
	}
	mcp := &host.MCPSession{
		ContractVersion: session.MCPContractVersion, ContractHash: session.MCPContractSHA256,
		SessionSHA256: session.ReservationSHA256, Role: string(session.Role), Provider: string(session.Provider),
		Model: session.Model, Tools: slices.Clone(session.MCPTools), Server: host.MCPServer{
			Name: session.MCPServer.Name, Command: session.MCPServer.Command, Args: slices.Clone(session.MCPServer.Args), Env: serverEnv,
		},
	}
	return profile, mcp
}

func promptText(task domain.Task, state domaincorrection.State, attempt domaincorrection.Attempt) (string, string, error) {
	batch, ok := domaincorrection.CurrentBatch(state)
	if !ok {
		return "", "", ErrInvalidCommand
	}
	prompt := struct {
		SchemaVersion         string                                                                    `json:"schemaVersion"`
		TaskID                string                                                                    `json:"taskId"`
		RunID                 string                                                                    `json:"runId"`
		OriginalTaskAgentUUID string                                                                    `json:"originalTaskAgentUuid"`
		Attempt               domaincorrection.Attempt                                                  `json:"attempt"`
		Objective             string                                                                    `json:"objective"`
		AcceptanceCriteria    string                                                                    `json:"acceptanceCriteria"`
		Batch                 domaincorrection.Batch                                                    `json:"batch"`
		Reuse                 struct{ PlanSHA256, SkillSetSHA256 string }                               `json:"reuse"`
		Refresh               struct{ DecisionsSHA256, DiffSHA256 string }                              `json:"refresh"`
		Authority             struct{ SameTaskAgentOnly, NewCommitRequired, LifecycleEffectsNone bool } `json:"authority"`
	}{
		SchemaVersion: "director.correction-prompt/v1", TaskID: state.TaskID, RunID: state.RunID,
		OriginalTaskAgentUUID: state.OriginalTaskAgentUUID, Attempt: attempt, Objective: task.Objective,
		AcceptanceCriteria: task.AcceptanceCriteria, Batch: batch,
	}
	prompt.Attempt.Output = nil
	prompt.Reuse.PlanSHA256, prompt.Reuse.SkillSetSHA256 = state.FrozenPlanDigest, state.FrozenSkillSetDigest
	prompt.Refresh.DecisionsSHA256, prompt.Refresh.DiffSHA256 = state.CurrentDecisionDigest, state.CurrentDiffDigest
	prompt.Authority.SameTaskAgentOnly, prompt.Authority.NewCommitRequired, prompt.Authority.LifecycleEffectsNone = true, true, true
	encoded, err := json.Marshal(prompt)
	if err != nil || len(encoded) > 128*1024 {
		return "", "", ErrInvalidCommand
	}
	return string(encoded), digest(json.RawMessage(encoded)), nil
}

func promptCommand(run domain.Run, state domaincorrection.State, attempt domaincorrection.Attempt, text string, observe bool) (host.Command, error) {
	if run.Execution.WorkerVisibility == nil {
		return host.Command{}, ErrInvalidCommand
	}
	registration, err := host.RegistrationFromVisibility(run.Execution.Scope, *run.Execution.WorkerVisibility)
	if err != nil || registration.Role != host.WorkerRoleTaskAgent || run.Execution.Agent.ExternalID != state.OriginalTaskAgentUUID ||
		run.Execution.PrimarySession.NativeAgentID != state.OriginalTaskAgentUUID {
		return host.Command{}, ErrInvalidCommand
	}
	profile, session := primaryContext(run)
	operationalObservationID := ""
	if run.Execution.OperationalObservation != nil {
		operationalObservationID = run.Execution.OperationalObservation.ID
	}
	requestSuffix := "dispatch"
	capability := host.CapabilityAgentPrompt
	if observe {
		requestSuffix, capability = "observe", host.CapabilityAgentObserve
	}
	command := host.Command{
		RequestID: attempt.Prompt.ID + "-" + requestSuffix, IdempotencyKey: attempt.Prompt.ID,
		ExpectedVersion: run.Version, AfterCursor: attempt.Prompt.Cursor, Capability: capability,
		Arguments: host.Arguments{
			Scope: run.Execution.Scope, EffectKind: execution.EffectAgentPrompt, EffectID: attempt.Prompt.ID,
			WorktreeID: run.Execution.Worktree.ExternalID, WorktreePath: run.Execution.WorktreePath,
			WorkspaceID: run.Execution.HostView.ExternalID, AgentID: state.OriginalTaskAgentUUID,
			Title: run.Execution.TaskTitle, ParentAgentID: nil, ClientMessageID: stableID("message", attempt.Prompt.ID),
			Profile: profile, Session: session, SessionBindingSHA256: run.Execution.PrimarySession.BindingSHA256,
			BindingHash: state.Batches[len(state.Batches)-1].SHA256, BoundaryID: run.Execution.Boundary.ExternalID,
			OperationalObservationID: operationalObservationID,
		},
	}
	if !observe {
		command.Arguments.InitialPrompt = text
		command.Arguments.NotifyOnFinish = true
		if err := host.AdmitAgentPrompt(command, registration, state.OriginalTaskAgentUUID); err != nil {
			return host.Command{}, ErrInvalidCommand
		}
	}
	return command, nil
}

func (service *Service) invoke(ctx context.Context, command host.Command, nowMillis int64) (host.Observation, error) {
	descriptor, err := service.host.Describe(ctx)
	if err != nil || host.ValidateDescriptor(descriptor) != nil {
		return host.Observation{}, ErrCorrectionAmbiguous
	}
	observation, err := service.host.Invoke(ctx, command)
	if err != nil {
		return host.Observation{}, err
	}
	if host.ValidateObservation(command, observation) != nil {
		return host.Observation{}, ErrCorrectionAmbiguous
	}
	observedAt, err := time.Parse(time.RFC3339Nano, observation.ObservedAt)
	if err != nil || observedAt.UnixMilli() > nowMillis || nowMillis-observedAt.UnixMilli() > observation.Result.MaximumAgeMillis {
		return host.Observation{}, ErrCorrectionAmbiguous
	}
	return observation, nil
}

func applyPromptUsage(run *domain.Run, state *domaincorrection.State, attempt *domaincorrection.Attempt, observation host.Observation, nowMillis int64) error {
	if observation.Result.Usage == nil {
		return ErrCorrectionAmbiguous
	}
	provider := runtimebudget.ProviderObservation{
		ID: stableID("correction-usage", attempt.Prompt.ID, observation.Result.FactHash), EffectID: attempt.Prompt.ID,
		AgentID: state.OriginalTaskAgentUUID, Activity: runtimebudget.ActivityCorrectionTurn,
		LeaseEpoch: run.Execution.LeaseBinding.Epoch, PolicyRevision: run.Execution.Budget.Policy.Revision,
		Sequence: observation.Cursor, ObservedAtMillis: nowMillis, ProviderUsage: *observation.Result.Usage,
	}
	provider.FactHash = runtimebudget.ProviderObservationHash(provider)
	ledger, decision, err := runtimebudget.ApplyProviderObservation(run.Execution.Budget, provider)
	if err != nil || decision.Disposition != runtimebudget.DispositionAllow {
		return ErrCorrectionAmbiguous
	}
	run.Execution.Budget = ledger
	attempt.BudgetEvidenceID = provider.ID
	return nil
}

// ReconcilePrompt advances one observed/persisted prompt frontier. A possible
// handoff with no authoritative observation is parked and never resent.
func (service *Service) ReconcilePrompt(ctx context.Context, command TransitionCommand) (Result, error) {
	_, task, run, _, err := service.load(ctx, command)
	if err != nil {
		return Result{Run: run}, err
	}
	if run.Execution.Correction == nil {
		return Result{Run: run}, ErrCorrectionNotReady
	}
	state := domaincorrection.CloneState(*run.Execution.Correction)
	if state.Phase != domaincorrection.PhasePrompt || len(state.Attempts) == 0 {
		return Result{Run: run}, ErrCorrectionNotReady
	}
	if run.Execution.Agent.ExternalID != state.OriginalTaskAgentUUID || run.Execution.PrimarySession.NativeAgentID != state.OriginalTaskAgentUUID ||
		run.Execution.PrimaryRecovery.SchemaVersion != "" && run.Execution.PrimaryRecovery.Phase != execution.PrimaryRecoveryComplete {
		return service.park(ctx, run, correctionreducer.CodeOriginalAgentChanged, "m3_8_explicit_primary_recovery_or_human_decision")
	}
	attempt := state.Attempts[len(state.Attempts)-1]
	text, promptSHA, promptErr := promptText(task, state, attempt)
	if promptErr != nil {
		return Result{Run: run}, promptErr
	}
	observe, commandErr := promptCommand(run, state, attempt, "", true)
	if commandErr != nil {
		return Result{Run: run}, commandErr
	}
	observation, observeErr := service.invoke(ctx, observe, command.NowMillis)
	if observeErr != nil {
		return service.park(ctx, run, correctionreducer.CodeProviderUnavailable, "provider_observation_available_or_human_decision")
	}
	switch observation.Result.Status {
	case execution.ObservationOwnedPresent:
		if attempt.Prompt.Phase != domaincorrection.EffectDispatching {
			return service.park(ctx, run, correctionreducer.CodePromptResultAmbiguous, "exact_nonrepeatable_prompt_observation")
		}
		attempt.Prompt.Cursor = observation.Cursor
		state.Attempts[len(state.Attempts)-1] = attempt
		next := run
		next.Execution.Correction = &state
		next, err = service.persist(ctx, run, next, "correction.prompt_running")
		return Result{Run: next, Progressed: err == nil}, err
	case execution.ObservationDesired:
		if attempt.Prompt.Phase != domaincorrection.EffectDispatching || observation.Result.ExternalID != state.OriginalTaskAgentUUID {
			return service.park(ctx, run, correctionreducer.CodePromptResultAmbiguous, "exact_primary_prompt_observation")
		}
		next := run
		if err := applyPromptUsage(&next, &state, &attempt, observation, command.NowMillis); err != nil {
			return service.park(ctx, run, correctionreducer.CodeProviderAmbiguous, "complete_provider_usage_or_human_decision")
		}
		attempt.Prompt.Phase, attempt.Prompt.ExternalID, attempt.Prompt.FactSHA256, attempt.Prompt.Cursor =
			domaincorrection.EffectComplete, observation.Result.ExternalID, observation.Result.FactHash, observation.Cursor
		attempt.PromptSHA256 = promptSHA
		state.Attempts[len(state.Attempts)-1], state.Phase = attempt, domaincorrection.PhaseAwaitingOutput
		next.Execution.Correction = &state
		next, err = service.persist(ctx, run, next, "correction.prompt_observed")
		return Result{Run: next, Progressed: err == nil}, err
	case execution.ObservationAbsent:
		if attempt.Prompt.Phase == domaincorrection.EffectDispatching {
			attempt.Prompt.Phase, attempt.Prompt.Cursor = domaincorrection.EffectAmbiguous, observation.Cursor
			state.Attempts[len(state.Attempts)-1] = attempt
			next := run
			next.Execution.Correction = &state
			next, persistErr := service.persist(ctx, run, next, "correction.prompt_ambiguous")
			if persistErr != nil {
				return Result{Run: run}, persistErr
			}
			return service.park(ctx, next, correctionreducer.CodePromptResultAmbiguous, "exact_nonrepeatable_prompt_observation")
		}
		dispatch, dispatchErr := promptCommand(run, state, attempt, text, false)
		if dispatchErr != nil {
			return Result{Run: run}, dispatchErr
		}
		attempt.Prompt.Phase, attempt.PromptSHA256 = domaincorrection.EffectDispatching, promptSHA
		state.Attempts[len(state.Attempts)-1] = attempt
		next := run
		next.Execution.Correction = &state
		next, err = service.persist(ctx, run, next, "correction.prompt_dispatching")
		if err != nil {
			return Result{Run: run}, err
		}
		_, invokeErr := service.invoke(ctx, dispatch, command.NowMillis)
		return Result{Run: next, Progressed: true}, invokeErr
	case execution.ObservationPermission:
		return service.park(ctx, run, correctionreducer.CodeHumanDecisionRequired, "explicit_permission_decision")
	case execution.ObservationErrored:
		return service.park(ctx, run, correctionreducer.CodeProviderUnavailable, "m3_8_explicit_primary_recovery_or_human_decision")
	default:
		return service.park(ctx, run, correctionreducer.CodePromptResultAmbiguous, "exact_primary_prompt_observation")
	}
}

// SubmitOutput records only the same primary agent's whole-batch response.
// An unchanged SHA is treated as acknowledgement-only and rejected once.
func (service *Service) SubmitOutput(ctx context.Context, command OutputCommand) (Result, error) {
	_, _, run, _, err := service.load(ctx, command.TransitionCommand)
	if err != nil {
		return Result{Run: run}, err
	}
	if run.Execution.Correction == nil {
		return Result{Run: run}, ErrCorrectionNotReady
	}
	state := domaincorrection.CloneState(*run.Execution.Correction)
	if state.Phase != domaincorrection.PhaseAwaitingOutput || len(state.Attempts) == 0 {
		return Result{Run: run}, ErrCorrectionNotReady
	}
	facts, evaluatedBudget := reducerFacts(state, run, state.FrozenPlanDigest, state.FrozenSkillSetDigest, &command.Output, command.NowMillis)
	run.Execution.Budget = evaluatedBudget
	decision := correctionreducer.Reduce(facts)
	attempt := state.Attempts[len(state.Attempts)-1]
	if decision.Kind == correctionreducer.DecisionEscalate {
		if domaincorrection.ValidOutput(command.Output, state, attempt) {
			attempt.Output = &command.Output
			attempt.CoverageDelta = domaincorrection.Delta(state.Batches[len(state.Batches)-1].AcceptanceCoverage, command.Output.AcceptanceCoverage)
			attempt.Classification = domaincorrection.ClassificationChurn
			state.Attempts[len(state.Attempts)-1] = attempt
			state.Phase, state.NeedsYouCode, state.WakeCondition = domaincorrection.PhaseNeedsYou, decision.Code, "human_review_of_correction_output"
			next := run
			next.Execution.Correction = &state
			next.Execution.NeedsYou = &execution.NeedsYou{Code: execution.NeedCode(decision.Code), WakeCondition: state.WakeCondition, CleanupAuthorized: false}
			next, err = service.persist(ctx, run, next, "correction.output_escalated")
			if err != nil {
				return Result{Run: run}, err
			}
			return Result{Run: next, Progressed: true, Code: decision.Code}, nil
		}
		return Result{Run: run}, ErrInvalidCommand
	}
	attempt.Output = &command.Output
	attempt.CoverageDelta = domaincorrection.Delta(state.Batches[len(state.Batches)-1].AcceptanceCoverage, command.Output.AcceptanceCoverage)
	productive := command.Output.CandidateSHA != state.CurrentCandidateSHA &&
		(len(attempt.CoverageDelta.Added) > 0 || len(command.Output.ResolvedFindingIDs) > 0)
	if productive {
		attempt.Classification = domaincorrection.ClassificationProductive
	} else {
		attempt.Classification = domaincorrection.ClassificationChurn
	}
	state.Attempts[len(state.Attempts)-1] = attempt
	transition := "correction.output_recorded"
	if decision.Kind == correctionreducer.DecisionRejectAcknowledgementOnce {
		state.AcknowledgementRejections++
		state.Phase = domaincorrection.PhaseReady
		transition = "correction.acknowledgement_rejected"
	} else {
		state.Phase = domaincorrection.PhaseCandidatePending
	}
	next := run
	next.Execution.Correction = &state
	next, err = service.persist(ctx, run, next, transition)
	return Result{Run: next, Progressed: err == nil, Code: decision.Code}, err
}

// ObserveCandidate applies the complete m4.2 exact-SHA claim/manifest path to
// a changed correction commit. The read-only Git observation is safe to repeat.
func (service *Service) ObserveCandidate(ctx context.Context, command TransitionCommand) (Result, error) {
	_, task, run, _, err := service.load(ctx, command)
	if err != nil {
		return Result{Run: run}, err
	}
	if run.Execution.Correction == nil {
		return Result{Run: run}, ErrCorrectionNotReady
	}
	state := domaincorrection.CloneState(*run.Execution.Correction)
	if state.Phase != domaincorrection.PhaseCandidatePending || len(state.Attempts) == 0 {
		return Result{Run: run}, ErrCorrectionNotReady
	}
	attempt := state.Attempts[len(state.Attempts)-1]
	if attempt.Output == nil || attempt.Output.CandidateSHA == state.CurrentCandidateSHA {
		return Result{Run: run}, ErrInvalidCommand
	}
	batch, _ := domaincorrection.CurrentBatch(state)
	claim := candidate.Claim{
		SchemaVersion: candidate.ClaimSchemaVersion, ID: stableID("correction-claim", attempt.Key, attempt.Output.CandidateSHA),
		ProjectID: run.Execution.Scope.ProjectID, WorkspaceID: run.Execution.Scope.WorkspaceID, TaskID: task.ID, RunID: run.ID,
		ActorID: state.OriginalTaskAgentUUID, WorktreeID: run.Execution.Worktree.ExternalID, Branch: run.Execution.Branch,
		BaseRef: run.Execution.BaseRef, CandidateSHA: attempt.Output.CandidateSHA, BaseSHA: run.BaseSHA,
		LeaseEpoch: command.LeaseEpoch, ExpectedRunVersion: run.Version + 1, TaskVersion: task.Version,
		AcceptanceSHA256:    candidate.AcceptanceSHA256(task.ID, task.Version, task.Objective, task.AcceptanceCriteria, state.CriterionIDs),
		ConfigurationSHA256: run.Execution.PrimarySession.ConfigurationSHA256, ProfileSHA256: run.Execution.EffectiveProfilesSHA256,
		ContextSHA256: run.Execution.PreparationBarrierHash, DecisionsSHA256: state.CurrentDecisionDigest,
		FindingsSHA256: batch.SHA256, GraphPolicy: candidate.GraphPolicy{},
	}
	if !candidate.ValidClaim(claim) || claim.AcceptanceSHA256 != run.Execution.AcceptanceSHA256 {
		return Result{Run: run}, ErrInvalidCommand
	}
	observation, err := service.git.ObserveCandidate(ctx, gitport.CandidateRequest{
		Claim: claim, Repository: run.Execution.RepositoryBinding,
		RepositoryBindingSHA256: run.Execution.RepositoryBindingHash, TaskStoreNowMillis: command.NowMillis,
	})
	if err != nil {
		return Result{Run: run}, err
	}
	admission := candidate.Evaluate(claim, observation, run.Execution.RepositoryBindingHash, command.NowMillis)
	if admission.Kind != candidate.DecisionAdmit || admission.Manifest == nil {
		return service.park(ctx, run, "correction_candidate_observation_rejected", "fresh_exact_candidate_or_human_decision")
	}
	candidates, err := service.store.Candidates(ctx, run.ID)
	if err != nil {
		return Result{Run: run}, err
	}
	state.PendingCandidateClaim, state.PendingCandidateObservation = &claim, &observation
	state.PendingCandidateSequence, state.Phase = uint64(len(candidates)+1), domaincorrection.PhaseCandidateObserved
	next := run
	next.Execution.Correction = &state
	next, err = service.persist(ctx, run, next, "correction.candidate_observed")
	return Result{Run: next, Progressed: err == nil}, err
}

func (service *Service) finalizeCandidate(ctx context.Context, run domain.Run) (Result, error) {
	state := domaincorrection.CloneState(*run.Execution.Correction)
	claim := state.PendingCandidateClaim
	if claim == nil || run.CurrentCandidateID == "" || run.Execution.CandidateAuthority == nil ||
		run.Execution.CandidateAuthority.CandidateSHA != claim.CandidateSHA || run.Execution.CandidateAuthority.Downstream != (candidate.Downstream{}) {
		return Result{Run: run}, ErrCorrectionAmbiguous
	}
	attempt := state.Attempts[len(state.Attempts)-1]
	attempt.ResultCandidateID, attempt.ResultCandidateSHA = run.CurrentCandidateID, claim.CandidateSHA
	state.Attempts[len(state.Attempts)-1] = attempt
	state.CurrentCandidateID, state.CurrentCandidateSHA = run.CurrentCandidateID, claim.CandidateSHA
	ciKey, reviewKey := domaincorrection.GateKeys(run.CurrentCandidateID, claim.CandidateSHA, run.Execution.CandidateAuthority.Generation)
	state.Gates = append(slices.Clone(state.Gates), domaincorrection.GatePlan{
		CandidateID: run.CurrentCandidateID, CandidateSHA: claim.CandidateSHA,
		CandidateGeneration: run.Execution.CandidateAuthority.Generation, CIKey: ciKey, ReviewBindingKey: reviewKey,
		FreshCIRequired: true, FreshReviewRequired: true, PriorAuthorityInvalidated: true,
	})
	state.PendingCandidateClaim, state.PendingCandidateObservation, state.PendingCandidateSequence = nil, nil, 0
	state.Phase = domaincorrection.PhaseGatesRequired
	next := run
	next.Execution.Correction = &state
	next, err := service.persist(ctx, run, next, "correction.candidate_gates_required")
	return Result{Run: next, Progressed: err == nil}, err
}

// AdmitCandidate appends a new immutable Candidate and atomically replaces all
// prior downstream authority in TaskStore. Recovery adopts an already-applied
// append by exact Candidate ID and then finishes the gate projection.
func (service *Service) AdmitCandidate(ctx context.Context, command TransitionCommand) (Result, error) {
	run, err := service.store.Run(ctx, command.RunID)
	if err != nil {
		return Result{}, err
	}
	if run.Execution.Correction == nil {
		return Result{Run: run}, ErrCorrectionNotReady
	}
	state := domaincorrection.CloneState(*run.Execution.Correction)
	if state.Phase == domaincorrection.PhaseGatesRequired {
		return Result{Run: run, Replayed: true}, nil
	}
	if state.Phase != domaincorrection.PhaseCandidateObserved || state.PendingCandidateClaim == nil || state.PendingCandidateObservation == nil || state.PendingCandidateSequence == 0 {
		return Result{Run: run}, ErrCorrectionNotReady
	}
	pendingID := candidate.RecordID(run.ID, state.PendingCandidateSequence, state.PendingCandidateClaim.CandidateSHA)
	if run.CurrentCandidateID == pendingID {
		if command.LeaseEpoch != run.Execution.LeaseBinding.Epoch || command.NowMillis < 0 {
			return Result{Run: run}, ErrConcurrentTransition
		}
		return service.finalizeCandidate(ctx, run)
	}
	if run.Version != command.ExpectedRunVersion || command.LeaseEpoch != run.Execution.LeaseBinding.Epoch {
		return Result{Run: run}, ErrConcurrentTransition
	}
	_, task, checked, _, err := service.load(ctx, command)
	if err != nil {
		return Result{Run: run}, err
	}
	run = checked
	claim, observation := *state.PendingCandidateClaim, *state.PendingCandidateObservation
	decision := candidate.Evaluate(claim, observation, run.Execution.RepositoryBindingHash, command.NowMillis)
	if decision.Kind != candidate.DecisionAdmit || decision.Manifest == nil || claim.TaskVersion != task.Version || claim.ActorID != state.OriginalTaskAgentUUID {
		return Result{Run: run}, ErrCorrectionAmbiguous
	}
	record := domain.Candidate{
		SchemaVersion: domain.CandidateSchemaVersion, ID: pendingID, RunID: run.ID,
		Sequence: state.PendingCandidateSequence, CommitSHA: claim.CandidateSHA, Claim: claim,
		Manifest: *decision.Manifest, AdmittedAtMillis: command.NowMillis,
	}
	commandID := stableID("command", run.ID, fmt.Sprintf("candidate-%d", record.Sequence), record.CommitSHA)
	result, err := service.store.AppendCandidate(ctx, domain.CommandRequest{
		IdempotencyKey: commandID, Type: "candidate.correction.admit", AggregateID: run.ID, ExpectedVersion: run.Version,
		Payload: eventPayload(struct{ AttemptKey, CandidateID, BindingSHA256 string }{state.Attempts[len(state.Attempts)-1].Key, record.ID, record.Manifest.BindingSHA256}),
	}, record, domain.Event{
		ID: stableID("event", commandID), RunID: run.ID, Sequence: run.Version + 2,
		AggregateID: run.ID, AggregateVersion: run.Version + 1, Type: "candidate.correction.admitted",
		Payload: eventPayload(struct{ CandidateID string }{record.ID}),
	})
	if err != nil {
		return Result{Run: run}, err
	}
	if result.Outcome != domain.CommandApplied {
		return Result{Run: run}, ErrConcurrentTransition
	}
	updated, err := service.store.Run(ctx, run.ID)
	if err != nil {
		return Result{Run: run}, err
	}
	return service.finalizeCandidate(ctx, updated)
}

// Reconcile resumes only the exact persisted correction frontier. It never
// creates a replacement Task Agent or starts CI/Review/publication itself.
func (service *Service) Reconcile(ctx context.Context, command TransitionCommand) (Result, error) {
	run, err := service.store.Run(ctx, command.RunID)
	if err != nil {
		return Result{}, err
	}
	if run.Execution.Correction == nil {
		return Result{Run: run}, ErrCorrectionNotReady
	}
	switch run.Execution.Correction.Phase {
	case domaincorrection.PhasePrompt:
		return service.ReconcilePrompt(ctx, command)
	case domaincorrection.PhaseCandidatePending:
		return service.ObserveCandidate(ctx, command)
	case domaincorrection.PhaseCandidateObserved:
		return service.AdmitCandidate(ctx, command)
	case domaincorrection.PhaseReady, domaincorrection.PhaseAwaitingOutput, domaincorrection.PhaseGatesRequired, domaincorrection.PhaseNeedsYou:
		return Result{Run: run}, nil
	default:
		return Result{Run: run}, ErrCorrectionAmbiguous
	}
}
