// SPDX-License-Identifier: Apache-2.0

// Package validation implements the engine-owned authoritative remote-CI
// observation and relevant-base invalidation vertical.
package validation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/candidate"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	domaincorrection "github.com/mcuadros/director-engine/domain/correction"
	directdomain "github.com/mcuadros/director-engine/domain/directdelivery"
	"github.com/mcuadros/director-engine/domain/execution"
	feedbackdomain "github.com/mcuadros/director-engine/domain/feedback"
	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
	domainreview "github.com/mcuadros/director-engine/domain/review"
	"github.com/mcuadros/director-engine/domain/runtimebudget"
	domainvalidation "github.com/mcuadros/director-engine/domain/validation"
	gitport "github.com/mcuadros/director-engine/ports/git"
	githubport "github.com/mcuadros/director-engine/ports/github"
)

var (
	ErrInvalidCommand        = errors.New("validation command is invalid")
	ErrConcurrentTransition  = errors.New("validation transition lost the Run state lock")
	ErrExternalUnavailable   = errors.New("validation external facts are unavailable")
	ErrValidationRefused     = errors.New("validation failed closed")
	ErrValidationInvalidated = errors.New("validation binding is invalidated")
	ErrValidationDispatching = errors.New("validation invalidation awaits external dispatch reconciliation")
)

var runLocks sync.Map

func lockRun(runID string) func() {
	value, _ := runLocks.LoadOrStore(runID, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}

type Store interface {
	Project(context.Context, string) (domain.Project, error)
	Workspace(context.Context, string) (domain.Workspace, error)
	Task(context.Context, string) (domain.Task, error)
	Run(context.Context, string) (domain.Run, error)
	Candidate(context.Context, string) (domain.Candidate, error)
	UpdateRun(context.Context, domain.CommandRequest, domain.Run, domain.Event) (domain.CommandResult, error)
}

type Service struct {
	store  Store
	git    gitport.BranchPort
	github githubport.ChecksPort
}

func NewService(store Store, git gitport.BranchPort, github githubport.ChecksPort) (*Service, error) {
	if store == nil || git == nil || github == nil {
		return nil, errors.New("validation store, Git branch port, and GitHub checks port are required")
	}
	return &Service{store: store, git: git, github: github}, nil
}

type AdmitCommand struct {
	RunID                  string
	ExpectedRunVersion     uint64
	GitHubRepositoryID     int64
	GitHubRepositoryNodeID string
	ViewerLogin            string
	CISlotID               string
	WorkflowID             int64
	WorkflowName           string
	RequiredChecks         []domainvalidation.RequiredCheck
	CycleRuntimeMillis     uint64
	NowMillis              int64
}

type Result struct {
	Run             domain.Run
	Progressed      bool
	WaitingExternal bool
	Terminal        bool
	Invalidated     bool
	Outcome         domainvalidation.Outcome
	Code            domainvalidation.Code
}

func stableID(prefix string, values ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(values, "\x1f")))
	return prefix + "-" + hex.EncodeToString(sum[:16])
}

func eventPayload(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal fixed validation event: " + err.Error())
	}
	return encoded
}

func stateHash(value execution.State) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func (service *Service) persist(ctx context.Context, current, next domain.Run, transition string) (domain.Run, error) {
	if next.Execution.ValidationPolicy == nil || !domainvalidation.ValidPolicy(*next.Execution.ValidationPolicy) ||
		next.Execution.DeliveryMode != domainconfig.DeliveryPullRequest || next.Execution.PublicationPolicy == nil ||
		!publicationdomain.ValidPolicy(*next.Execution.PublicationPolicy) ||
		next.Execution.Validation != nil && (!domainvalidation.ValidState(*next.Execution.Validation) ||
			next.Execution.Validation.Policy.SHA256 != next.Execution.ValidationPolicy.SHA256) ||
		!runtimebudget.ValidLedgerForLease(next.Execution.Budget, next.Execution.LeaseBinding.Epoch) {
		return current, ErrInvalidCommand
	}
	next.Version = current.Version + 1
	commandID := stableID("command", current.ID, fmt.Sprintf("version-%d", next.Version), transition)
	validationKey, phase, code := "", domainvalidation.Phase(""), domainvalidation.Code("")
	if next.Execution.Validation != nil {
		validationKey, phase, code = next.Execution.Validation.ValidationKey, next.Execution.Validation.Phase, next.Execution.Validation.Code
	}
	result, err := service.store.UpdateRun(ctx, domain.CommandRequest{IdempotencyKey: commandID, Type: transition,
		AggregateID: current.ID, ExpectedVersion: current.Version,
		Payload: eventPayload(struct{ Transition, StateSHA256 string }{transition, stateHash(next.Execution)}),
	}, next, domain.Event{ID: stableID("event", commandID), RunID: current.ID, Sequence: current.Version + 2,
		AggregateID: current.ID, AggregateVersion: next.Version, Type: transition,
		Payload: eventPayload(struct {
			ValidationKey string
			Phase         domainvalidation.Phase
			Code          domainvalidation.Code
		}{validationKey, phase, code})})
	if err != nil {
		return current, err
	}
	if result.Outcome != domain.CommandApplied || result.Replay {
		return current, ErrConcurrentTransition
	}
	return next, nil
}

func currentLease(project domain.Project, run domain.Run, nowMillis int64) bool {
	lease, binding := project.Lease, run.Execution.LeaseBinding
	return project.ID == run.Execution.Scope.ProjectID && project.State == "active" && lease != nil && domain.ValidateProjectLease(lease) == nil &&
		execution.ValidLeaseBinding(binding) && lease.DispatchAllowed && lease.HolderInstance == binding.HolderInstance &&
		lease.HolderProcessIdentity == binding.HolderProcessIdentity && lease.Epoch == binding.Epoch &&
		nowMillis >= lease.AcquiredAtMillis && nowMillis < lease.ExpiresAtMillis
}

func repositoryName(key string) (string, string, bool) {
	parts := strings.Split(key, "/")
	if len(parts) != 3 || parts[0] != "github.com" || parts[1] == "" || parts[2] == "" {
		return "", "", false
	}
	return parts[1], parts[2], true
}

func draftCurrent(run domain.Run, record domain.Candidate) bool {
	state := run.Execution.Publication
	return state != nil && publicationdomain.ValidState(*state) && !state.Invalidated && state.Evidence != nil &&
		publicationdomain.ValidEvidence(*state.Evidence, *state) && state.Evidence.Draft && !state.Evidence.Ready &&
		state.Binding.CandidateID == record.ID && state.Binding.CandidateSHA == record.CommitSHA &&
		state.Binding.BaseSHA == record.Manifest.BaseSHA && state.Binding.ManifestSHA256 == record.Manifest.BindingSHA256 &&
		state.Binding.CandidateGeneration == run.Execution.CandidateAuthority.Generation
}

func cycleNumber(ledger runtimebudget.Ledger) (uint32, bool) {
	_, outstanding, ok := runtimebudget.Outstanding(ledger)
	if !ok {
		return 0, false
	}
	cycle := ledger.Consumption.CICycles + outstanding.CICycles + 1
	return cycle, cycle > 0 && cycle <= domainvalidation.MaximumCICycles && cycle <= ledger.Policy.CICycleLimit
}

func correctionAllowsValidation(run domain.Run, ciSlotID string) bool {
	state := run.Execution.Correction
	if state == nil {
		return true
	}
	authority := run.Execution.CandidateAuthority
	if authority == nil || !domaincorrection.ValidState(*state) || state.Phase != domaincorrection.PhaseGatesRequired || len(state.Gates) == 0 {
		return false
	}
	gate := state.Gates[len(state.Gates)-1]
	return gate.CandidateID == authority.CandidateID && gate.CandidateSHA == authority.CandidateSHA &&
		gate.CandidateGeneration == authority.Generation && gate.CIKey == ciSlotID && gate.FreshCIRequired &&
		gate.FreshReviewRequired && gate.PriorAuthorityInvalidated
}

// Admit freezes exact check identities only after the owned draft for the
// Candidate is externally observed. It performs no GitHub request.
func (service *Service) Admit(ctx context.Context, command AdmitCommand) (Result, error) {
	unlock := lockRun(command.RunID)
	defer unlock()
	run, err := service.store.Run(ctx, command.RunID)
	if err != nil {
		return Result{}, err
	}
	if run.Version != command.ExpectedRunVersion || command.NowMillis < 0 || run.CurrentCandidateID == "" ||
		run.Execution.CandidateAuthority == nil || run.Execution.CandidateAuthority.Invalidated ||
		!candidate.ValidAuthority(*run.Execution.CandidateAuthority) || command.GitHubRepositoryID <= 0 ||
		command.GitHubRepositoryNodeID == "" || command.ViewerLogin == "" || command.CISlotID == "" ||
		run.Execution.NeedsYou != nil || run.Execution.DeliveryMode != domainconfig.DeliveryPullRequest ||
		run.Execution.PublicationPolicy == nil || !publicationdomain.ValidPolicy(*run.Execution.PublicationPolicy) ||
		!correctionAllowsValidation(run, command.CISlotID) ||
		!runtimebudget.ValidLedgerForLease(run.Execution.Budget, run.Execution.LeaseBinding.Epoch) {
		return Result{Run: run}, ErrInvalidCommand
	}
	policy, ok := domainvalidation.NewPolicy(command.WorkflowID, command.WorkflowName, command.RequiredChecks, command.CycleRuntimeMillis)
	if !ok || run.Execution.ValidationPolicy == nil || !domainvalidation.ValidPolicy(*run.Execution.ValidationPolicy) ||
		run.Execution.ValidationPolicy.SHA256 != policy.SHA256 {
		return Result{Run: run}, ErrInvalidCommand
	}
	policy = *run.Execution.ValidationPolicy
	policy.RequiredChecks = slices.Clone(policy.RequiredChecks)
	task, err := service.store.Task(ctx, run.TaskID)
	if err != nil {
		return Result{}, err
	}
	project, err := service.store.Project(ctx, task.ProjectID)
	if err != nil {
		return Result{}, err
	}
	workspace, err := service.store.Workspace(ctx, run.Execution.Scope.WorkspaceID)
	if err != nil {
		return Result{}, err
	}
	record, err := service.store.Candidate(ctx, run.CurrentCandidateID)
	if err != nil {
		return Result{}, err
	}
	authority := run.Execution.CandidateAuthority
	owner, name, repositoryOK := repositoryName(run.Execution.RepositoryBinding.RepositoryKey)
	cycle, cycleOK := cycleNumber(run.Execution.Budget)
	if !currentLease(project, run, command.NowMillis) || !repositoryOK || len(task.WorkspaceIDs) != 1 ||
		task.WorkspaceIDs[0] != workspace.ID || workspace.Repository.ID != run.Execution.RepositoryBinding.RepositoryID ||
		workspace.Repository.Key != run.Execution.RepositoryBinding.RepositoryKey ||
		workspace.Repository.CanonicalRemote != run.Execution.RepositoryBinding.CanonicalRemote || record.RunID != run.ID ||
		record.ID != authority.CandidateID || record.CommitSHA != authority.CandidateSHA || record.Manifest.BindingSHA256 != authority.BindingSHA256 ||
		record.Manifest.BaseSHA != authority.BaseSHA || record.Manifest.TreeSHA == "" || !draftCurrent(run, record) {
		return Result{Run: run}, ErrInvalidCommand
	}
	if !cycleOK {
		next := run
		next.Execution.NeedsYou = &execution.NeedsYou{Code: execution.NeedCode(runtimebudget.ReasonCIHard),
			WakeCondition: "explicit_ci_budget_revision_or_human_decision", CleanupAuthorized: false}
		next, persistErr := service.persist(ctx, run, next, "validation.ci_budget_exhausted")
		return Result{Run: next, Progressed: persistErr == nil, Code: domainvalidation.CodeBudgetExhausted}, errors.Join(ErrValidationRefused, persistErr)
	}
	if run.Execution.Review != nil && run.Execution.Review.Binding.CISlotID != command.CISlotID {
		return Result{Run: run}, ErrInvalidCommand
	}
	if run.Execution.Correction != nil && len(run.Execution.Correction.Gates) > 0 {
		gate := run.Execution.Correction.Gates[len(run.Execution.Correction.Gates)-1]
		if gate.CandidateID == record.ID && gate.CandidateSHA == record.CommitSHA && gate.CandidateGeneration == authority.Generation && gate.CIKey != command.CISlotID {
			return Result{Run: run}, ErrInvalidCommand
		}
	}
	binding := domainvalidation.SealBinding(domainvalidation.Binding{TaskID: task.ID, RunID: run.ID, CandidateID: record.ID,
		CandidateSHA: record.CommitSHA, BaseSHA: record.Manifest.BaseSHA, TreeSHA: record.Manifest.TreeSHA,
		ManifestSHA256: record.Manifest.BindingSHA256, CandidateGeneration: authority.Generation, CISlotID: command.CISlotID,
		BaseRef: run.Execution.BaseRef, RepositoryBindingSHA256: run.Execution.RepositoryBindingHash,
		CanonicalRemote: run.Execution.RepositoryBinding.CanonicalRemote, GitHubRepositoryID: command.GitHubRepositoryID,
		GitHubRepositoryNodeID: command.GitHubRepositoryNodeID, RepositoryOwner: owner, RepositoryName: name,
		ViewerLogin: command.ViewerLogin, PolicySHA256: policy.SHA256})
	state, ok := domainvalidation.NewState(binding, policy, cycle)
	if !ok {
		return Result{Run: run}, ErrInvalidCommand
	}
	if run.Execution.Validation != nil {
		existing := run.Execution.Validation
		if existing.ValidationKey == state.ValidationKey && existing.Binding.BindingSHA256 == state.Binding.BindingSHA256 && existing.Policy.SHA256 == state.Policy.SHA256 {
			if existing.Invalidated {
				return Result{Run: run, Invalidated: true, Code: existing.InvalidationCode}, ErrValidationInvalidated
			}
			return Result{Run: run, Terminal: existing.Evidence != nil, Code: existing.Code}, nil
		}
		return Result{Run: run}, ErrInvalidCommand
	}
	next := run
	next.Execution.Validation = &state
	next, err = service.persist(ctx, run, next, "validation.intent_recorded")
	return Result{Run: next, Progressed: err == nil}, err
}

func (service *Service) load(ctx context.Context, runID string, nowMillis int64) (domain.Project, domain.Run, domain.Candidate, domainvalidation.State, error) {
	run, err := service.store.Run(ctx, runID)
	if err != nil {
		return domain.Project{}, domain.Run{}, domain.Candidate{}, domainvalidation.State{}, err
	}
	if run.Execution.Validation == nil || run.CurrentCandidateID == "" || !domainvalidation.ValidState(*run.Execution.Validation) {
		return domain.Project{}, run, domain.Candidate{}, domainvalidation.State{}, ErrInvalidCommand
	}
	task, err := service.store.Task(ctx, run.TaskID)
	if err != nil {
		return domain.Project{}, run, domain.Candidate{}, *run.Execution.Validation, err
	}
	project, err := service.store.Project(ctx, task.ProjectID)
	if err != nil {
		return domain.Project{}, run, domain.Candidate{}, *run.Execution.Validation, err
	}
	record, err := service.store.Candidate(ctx, run.CurrentCandidateID)
	if err != nil {
		return project, run, domain.Candidate{}, *run.Execution.Validation, err
	}
	state, authority := *run.Execution.Validation, run.Execution.CandidateAuthority
	if !currentLease(project, run, nowMillis) || authority == nil || authority.Invalidated || !candidate.ValidAuthority(*authority) ||
		run.Execution.ValidationPolicy == nil || !domainvalidation.ValidPolicy(*run.Execution.ValidationPolicy) ||
		state.Policy.SHA256 != run.Execution.ValidationPolicy.SHA256 ||
		record.ID != authority.CandidateID || record.CommitSHA != authority.CandidateSHA || state.Binding.CandidateID != authority.CandidateID ||
		state.Binding.CandidateSHA != authority.CandidateSHA || state.Binding.BaseSHA != authority.BaseSHA ||
		state.Binding.ManifestSHA256 != authority.BindingSHA256 || state.Binding.CandidateGeneration != authority.Generation ||
		state.Binding.RepositoryBindingSHA256 != run.Execution.RepositoryBindingHash {
		return project, run, record, state, ErrValidationInvalidated
	}
	return project, run, record, state, nil
}

func waiting(code domainvalidation.Code) bool {
	return slices.Contains([]domainvalidation.Code{domainvalidation.CodeUnavailable, domainvalidation.CodeRateLimited, domainvalidation.CodeServer}, code)
}

func (service *Service) park(ctx context.Context, run domain.Run, state domainvalidation.State, code domainvalidation.Code) (Result, error) {
	state.Code = code
	if waiting(code) {
		state.Phase = domainvalidation.PhaseWaiting
	} else {
		state.Phase = domainvalidation.PhaseNeedsYou
	}
	next := run
	next.Execution.Validation = &state
	if !waiting(code) {
		next.Execution.NeedsYou = &execution.NeedsYou{Code: execution.NeedCode(strings.ToLower(string(code))),
			WakeCondition: "fresh_exact_candidate_base_and_github_observation", CleanupAuthorized: false}
	}
	next, err := service.persist(ctx, run, next, "validation."+string(state.Phase)+"."+string(code))
	result := Result{Run: next, Progressed: err == nil, WaitingExternal: waiting(code), Code: code}
	if waiting(code) {
		return result, errors.Join(ErrExternalUnavailable, err)
	}
	return result, errors.Join(ErrValidationRefused, err)
}

func (service *Service) reserve(ctx context.Context, run domain.Run, state domainvalidation.State, nowMillis int64) (domain.Run, domainvalidation.State, error) {
	ledger, decision, err := runtimebudget.Reserve(run.Execution.Budget, runtimebudget.ReserveRequest{ID: state.BudgetReservationID,
		EffectID: state.EffectID, Activity: runtimebudget.ActivityValidationCycle, LeaseEpoch: run.Execution.LeaseBinding.Epoch,
		PolicyRevision: run.Execution.Budget.Policy.Revision, Demand: runtimebudget.Demand{WallTimeMilliseconds: state.Policy.CycleRuntimeMillis},
		CandidateSHA: state.Binding.CandidateSHA}, nowMillis)
	if err != nil {
		return run, state, err
	}
	if decision.Disposition != runtimebudget.DispositionAllow {
		code := domainvalidation.CodeBudgetExhausted
		state.Phase, state.Code = domainvalidation.PhaseNeedsYou, code
		next := run
		next.Execution.Budget, next.Execution.Validation = ledger, &state
		next.Execution.NeedsYou = &execution.NeedsYou{Code: execution.NeedCode(decision.Reason), WakeCondition: "explicit_budget_revision_or_human_decision", CleanupAuthorized: false}
		next, persistErr := service.persist(ctx, run, next, "validation.budget_refused."+string(decision.Reason))
		return next, state, errors.Join(ErrValidationRefused, persistErr)
	}
	state.Phase = domainvalidation.PhaseReserved
	next := run
	next.Execution.Budget, next.Execution.Validation = ledger, &state
	next, err = service.persist(ctx, run, next, "validation.budget_reserved")
	return next, state, err
}

func (service *Service) baseObservation(ctx context.Context, run domain.Run, state domainvalidation.State, nowMillis int64) publicationdomain.RefObservation {
	branch := strings.TrimPrefix(state.Binding.BaseRef, "refs/heads/")
	value, err := service.git.ObserveRemoteRef(ctx, gitport.RemoteRefRequest{Repository: run.Execution.RepositoryBinding,
		RepositorySHA256: state.Binding.RepositoryBindingSHA256, RemoteName: "origin", CanonicalRemote: state.Binding.CanonicalRemote,
		Branch: branch, TaskStoreNowMillis: nowMillis})
	if err != nil {
		return publicationdomain.SealRefObservation(publicationdomain.RefObservation{ID: stableID("validation-base-unavailable", state.ValidationKey, strconv.FormatInt(nowMillis, 10)),
			Code: publicationdomain.CodeUnavailable, Ref: state.Binding.BaseRef, RemoteCanonical: state.Binding.CanonicalRemote,
			ObservedAtMillis: nowMillis, MaximumAgeMillis: domainvalidation.MaximumObservationAgeMS})
	}
	return value
}

func refCode(value publicationdomain.RefObservation, state domainvalidation.State, nowMillis int64) domainvalidation.Code {
	branch := strings.TrimPrefix(state.Binding.BaseRef, "refs/heads/")
	if !publicationdomain.CurrentNamedRefObservation(value, state.Binding.CanonicalRemote, branch, nowMillis) {
		return domainvalidation.CodeResponseUnknown
	}
	switch value.Code {
	case publicationdomain.CodeOK:
		if !value.Exists || value.OID == "" {
			return domainvalidation.CodeBaseRace
		}
		return domainvalidation.CodeOK
	case publicationdomain.CodeRateLimited:
		return domainvalidation.CodeRateLimited
	case publicationdomain.CodeServer:
		return domainvalidation.CodeServer
	default:
		return domainvalidation.CodeUnavailable
	}
}

func (service *Service) scanWorkflows(ctx context.Context, state domainvalidation.State, nowMillis int64) (domainvalidation.WorkflowScan, domainvalidation.Code) {
	values := []domainvalidation.WorkflowRun{}
	var total uint32
	for page := uint32(1); page <= state.Policy.MaximumPages; page++ {
		value, err := service.github.ListWorkflowRuns(ctx, githubport.CandidatePageRequest{Owner: state.Binding.RepositoryOwner, Name: state.Binding.RepositoryName,
			RepositoryID: state.Binding.GitHubRepositoryID, CandidateSHA: state.Binding.CandidateSHA, Page: page, PageSize: state.Policy.PageSize, TaskStoreNowMillis: nowMillis})
		if err != nil {
			return domainvalidation.WorkflowScan{}, domainvalidation.CodeUnavailable
		}
		if !domainvalidation.CurrentWorkflowPage(value, state.Binding.CandidateSHA, page, nowMillis) {
			return domainvalidation.WorkflowScan{}, domainvalidation.CodeResponseUnknown
		}
		if value.Code != domainvalidation.CodeOK {
			return domainvalidation.WorkflowScan{}, value.Code
		}
		if page == 1 {
			total = value.TotalCount
		} else if value.TotalCount != total {
			return domainvalidation.WorkflowScan{}, domainvalidation.CodePaginationIncomplete
		}
		values = append(values, value.Runs...)
		if value.Complete {
			if uint32(len(values)) != total {
				return domainvalidation.WorkflowScan{}, domainvalidation.CodePaginationIncomplete
			}
			return domainvalidation.SealWorkflowScan(domainvalidation.WorkflowScan{Pages: page, TotalCount: total, Runs: values}), domainvalidation.CodeOK
		}
	}
	return domainvalidation.WorkflowScan{}, domainvalidation.CodePaginationIncomplete
}

func (service *Service) scanChecks(ctx context.Context, state domainvalidation.State, nowMillis int64) (domainvalidation.CheckScan, domainvalidation.Code) {
	values := []domainvalidation.CheckRun{}
	var total uint32
	for page := uint32(1); page <= state.Policy.MaximumPages; page++ {
		value, err := service.github.ListCheckRuns(ctx, githubport.CandidatePageRequest{Owner: state.Binding.RepositoryOwner, Name: state.Binding.RepositoryName,
			RepositoryID: state.Binding.GitHubRepositoryID, CandidateSHA: state.Binding.CandidateSHA, Page: page, PageSize: state.Policy.PageSize, TaskStoreNowMillis: nowMillis})
		if err != nil {
			return domainvalidation.CheckScan{}, domainvalidation.CodeUnavailable
		}
		if !domainvalidation.CurrentCheckPage(value, state.Binding.CandidateSHA, page, nowMillis) {
			return domainvalidation.CheckScan{}, domainvalidation.CodeResponseUnknown
		}
		if value.Code != domainvalidation.CodeOK {
			return domainvalidation.CheckScan{}, value.Code
		}
		if page == 1 {
			total = value.TotalCount
		} else if value.TotalCount != total {
			return domainvalidation.CheckScan{}, domainvalidation.CodePaginationIncomplete
		}
		values = append(values, value.Checks...)
		if value.Complete {
			if uint32(len(values)) != total {
				return domainvalidation.CheckScan{}, domainvalidation.CodePaginationIncomplete
			}
			return domainvalidation.SealCheckScan(domainvalidation.CheckScan{Pages: page, TotalCount: total, Checks: values}), domainvalidation.CodeOK
		}
	}
	return domainvalidation.CheckScan{}, domainvalidation.CodePaginationIncomplete
}

func (service *Service) scanStatuses(ctx context.Context, state domainvalidation.State, nowMillis int64) (domainvalidation.StatusScan, domainvalidation.Code) {
	values := []domainvalidation.CommitStatus{}
	var total uint32
	rollup := ""
	for page := uint32(1); page <= state.Policy.MaximumPages; page++ {
		value, err := service.github.ListCommitStatuses(ctx, githubport.CandidatePageRequest{Owner: state.Binding.RepositoryOwner, Name: state.Binding.RepositoryName,
			RepositoryID: state.Binding.GitHubRepositoryID, CandidateSHA: state.Binding.CandidateSHA, Page: page, PageSize: state.Policy.PageSize, TaskStoreNowMillis: nowMillis})
		if err != nil {
			return domainvalidation.StatusScan{}, domainvalidation.CodeUnavailable
		}
		if !domainvalidation.CurrentStatusPage(value, state.Binding.CandidateSHA, page, nowMillis) {
			return domainvalidation.StatusScan{}, domainvalidation.CodeResponseUnknown
		}
		if value.Code != domainvalidation.CodeOK {
			return domainvalidation.StatusScan{}, value.Code
		}
		if page == 1 {
			total, rollup = value.TotalCount, value.CombinedState
		} else if value.TotalCount != total || value.CombinedState != rollup {
			return domainvalidation.StatusScan{}, domainvalidation.CodePaginationIncomplete
		}
		values = append(values, value.Statuses...)
		if value.Complete {
			if uint32(len(values)) != total {
				return domainvalidation.StatusScan{}, domainvalidation.CodePaginationIncomplete
			}
			return domainvalidation.SealStatusScan(domainvalidation.StatusScan{Pages: page, TotalCount: total, CombinedState: rollup, Statuses: values}), domainvalidation.CodeOK
		}
	}
	return domainvalidation.StatusScan{}, domainvalidation.CodePaginationIncomplete
}

func completeBudget(run domain.Run, state domainvalidation.State, failed bool, nowMillis int64) (runtimebudget.Ledger, error) {
	observation := runtimebudget.ActivityObservation{ID: stableID("activity", state.EffectID, state.ValidationKey),
		ReservationID: state.BudgetReservationID, EffectID: state.EffectID, Activity: runtimebudget.ActivityValidationCycle,
		LeaseEpoch: run.Execution.LeaseBinding.Epoch, PolicyRevision: run.Execution.Budget.Policy.Revision,
		ObservedAtMillis: nowMillis, CandidateSHA: state.Binding.CandidateSHA, Failed: failed}
	observation.FactHash = runtimebudget.ActivityObservationHash(observation)
	ledger, _, err := runtimebudget.ApplyActivityObservation(run.Execution.Budget, observation)
	return ledger, err
}

func cycleOutstanding(run domain.Run, state domainvalidation.State) bool {
	return slices.ContainsFunc(run.Execution.Budget.Reservations, func(reservation runtimebudget.Reservation) bool {
		return reservation.ID == state.BudgetReservationID && reservation.EffectID == state.EffectID && !reservation.Released
	})
}

func attachReview(run *domain.Run, state domainvalidation.State) domainvalidation.Code {
	if run.Execution.Review == nil || state.Evidence == nil {
		return domainvalidation.CodeOK
	}
	review := *run.Execution.Review
	observation, ok := domainvalidation.ReviewObservation(state)
	if !ok || !domainreview.ValidCIObservation(observation, review.Binding) {
		return domainvalidation.CodeResponseUnknown
	}
	if review.CIObservation != nil {
		if review.CIObservation.SHA256 == observation.SHA256 {
			return domainvalidation.CodeOK
		}
		invalid := domainreview.Invalidate(review, "authoritative_ci_changed")
		run.Execution.Review = &invalid
		if run.Execution.CandidateAuthority != nil {
			authority := *run.Execution.CandidateAuthority
			authority.Downstream.Review = nil
			run.Execution.CandidateAuthority = &authority
		}
		return domainvalidation.CodeResponseUnknown
	}
	review.CIObservation = &observation
	run.Execution.Review = &review
	return domainvalidation.CodeOK
}

func (service *Service) invalidateBase(ctx context.Context, run domain.Run, state domainvalidation.State,
	before, after publicationdomain.RefObservation, code domainvalidation.Code, nowMillis int64) (Result, error) {
	// Never erase the only durable record of an external effect whose response
	// may have been lost. A later reconciliation closes the dispatch window;
	// the same deterministic base observation can then invalidate the binding.
	if run.Execution.Publication != nil && publicationdomain.DispatchInFlight(*run.Execution.Publication) ||
		run.Execution.DirectDelivery != nil && directdomain.DispatchInFlight(*run.Execution.DirectDelivery) ||
		run.Execution.Feedback != nil && feedbackdomain.DispatchInFlight(*run.Execution.Feedback) {
		return Result{Run: run, WaitingExternal: true, Code: code}, ErrValidationDispatching
	}
	if run.Execution.Review != nil && len(run.Execution.ReviewHistory) >= 64 ||
		run.Execution.Publication != nil && len(run.Execution.PublicationHistory) >= publicationdomain.MaximumHistoricalStates ||
		run.Execution.DirectDelivery != nil && len(run.Execution.DirectDeliveryHistory) >= directdomain.MaximumHistoricalStates ||
		run.Execution.Feedback != nil && len(run.Execution.FeedbackHistory) >= 64 {
		return Result{Run: run, Code: code}, ErrValidationRefused
	}
	newBase := after.OID
	if newBase == "" {
		newBase = before.OID
	}
	prior := *run.Execution.CandidateAuthority
	state = domainvalidation.Invalidate(state, code)
	state.BaseBeforeSHA256, state.BaseAfterSHA256 = publicationdomain.RefObservationSHA256(before), publicationdomain.RefObservationSHA256(after)
	state.BaseInvalidation = &domainvalidation.BaseInvalidation{OldBaseSHA: state.Binding.BaseSHA, NewBaseSHA: newBase, NewBasePresent: newBase != "",
		BeforeFactSHA256: state.BaseBeforeSHA256, AfterFactSHA256: state.BaseAfterSHA256, PriorAuthority: prior, ObservedAtMillis: nowMillis}
	ledger := run.Execution.Budget
	if cycleOutstanding(run, state) {
		var budgetErr error
		ledger, budgetErr = completeBudget(run, state, true, nowMillis)
		if budgetErr != nil {
			return Result{Run: run}, budgetErr
		}
	}
	next := run
	next.Execution.Budget, next.Execution.Validation = ledger, &state
	authority := prior
	authority.Downstream = candidate.Downstream{}
	next.Execution.CandidateAuthority = &authority
	if next.Execution.Review != nil {
		invalid := domainreview.Invalidate(*next.Execution.Review, string(code))
		next.Execution.ReviewHistory = append(slices.Clone(next.Execution.ReviewHistory), invalid)
		next.Execution.Review = nil
	}
	if next.Execution.Publication != nil {
		invalid := publicationdomain.Invalidate(*next.Execution.Publication, string(code))
		next.Execution.PublicationHistory = append(slices.Clone(next.Execution.PublicationHistory), invalid)
		next.Execution.Publication = nil
	}
	if next.Execution.DirectDelivery != nil {
		invalid := directdomain.Invalidate(*next.Execution.DirectDelivery, string(code), "fresh_candidate_validation_and_review")
		if !directdomain.ValidState(invalid) || invalid.Phase != directdomain.PhaseInvalidated {
			return Result{Run: run, Code: code}, ErrValidationRefused
		}
		next.Execution.DirectDeliveryHistory = append(slices.Clone(next.Execution.DirectDeliveryHistory), invalid)
		next.Execution.DirectDelivery = nil
	}
	if next.Execution.Feedback != nil {
		invalid := feedbackdomain.Invalidate(*next.Execution.Feedback, strings.ToLower(string(code)))
		if !feedbackdomain.ValidState(invalid) || !invalid.Invalidated {
			return Result{Run: run, Code: code}, ErrValidationRefused
		}
		next.Execution.FeedbackHistory = append(slices.Clone(next.Execution.FeedbackHistory), invalid)
		next.Execution.Feedback = nil
	}
	if newBase == "" {
		next.Execution.NeedsYou = &execution.NeedsYou{Code: execution.NeedCode(strings.ToLower(string(code))),
			WakeCondition: "live_relevant_base_ref_is_present_and_exact", CleanupAuthorized: false}
	} else {
		next.Execution.NeedsYou = nil
	}
	next, err := service.persist(ctx, run, next, "validation.base_invalidated."+string(code))
	return Result{Run: next, Progressed: err == nil, Invalidated: true, Code: code}, errors.Join(ErrValidationInvalidated, err)
}

// Reconcile performs only repeatable observations. It reserves one runtime CI
// slot once, re-reads the live base on both sides of every bounded GitHub scan,
// and persists terminal evidence or downstream invalidation in one Run CAS.
func (service *Service) Reconcile(ctx context.Context, runID string, nowMillis int64) (Result, error) {
	unlock := lockRun(runID)
	defer unlock()
	_, run, _, state, err := service.load(ctx, runID, nowMillis)
	if err != nil {
		return Result{Run: run}, err
	}
	if state.Invalidated {
		return Result{Run: run, Invalidated: true, Code: state.InvalidationCode}, ErrValidationInvalidated
	}
	if state.Evidence != nil {
		repository, observeErr := service.github.ObserveChecksRepository(ctx, githubport.ChecksRepositoryRequest{Owner: state.Binding.RepositoryOwner,
			Name: state.Binding.RepositoryName, RepositoryID: state.Binding.GitHubRepositoryID, RepositoryNodeID: state.Binding.GitHubRepositoryNodeID,
			ExpectedViewer: state.Binding.ViewerLogin, TaskStoreNowMillis: nowMillis})
		if observeErr != nil || !domainvalidation.CurrentRepositoryObservation(repository, state.Binding, nowMillis) || repository.Code != domainvalidation.CodeOK {
			return Result{Run: run, WaitingExternal: true, Terminal: true, Outcome: state.Evidence.Outcome, Code: domainvalidation.CodeUnavailable}, ErrExternalUnavailable
		}
		before := service.baseObservation(ctx, run, state, nowMillis)
		if code := refCode(before, state, nowMillis); code != domainvalidation.CodeOK {
			if code == domainvalidation.CodeBaseRace {
				return service.invalidateBase(ctx, run, state, before, before, code, nowMillis)
			}
			return Result{Run: run, WaitingExternal: true, Terminal: true, Outcome: state.Evidence.Outcome, Code: code}, ErrExternalUnavailable
		}
		after := service.baseObservation(ctx, run, state, nowMillis)
		if code := refCode(after, state, nowMillis); code != domainvalidation.CodeOK {
			if code == domainvalidation.CodeBaseRace {
				return service.invalidateBase(ctx, run, state, before, after, code, nowMillis)
			}
			return Result{Run: run, WaitingExternal: true, Terminal: true, Outcome: state.Evidence.Outcome, Code: code}, ErrExternalUnavailable
		}
		if before.OID != after.OID {
			return service.invalidateBase(ctx, run, state, before, after, domainvalidation.CodeBaseRace, nowMillis)
		}
		if after.OID != state.Binding.BaseSHA {
			return service.invalidateBase(ctx, run, state, before, after, domainvalidation.CodeBaseChanged, nowMillis)
		}
		return Result{Run: run, Terminal: true, Outcome: state.Evidence.Outcome, Code: state.Code}, nil
	}
	if state.Phase == domainvalidation.PhaseNeedsYou {
		return Result{Run: run, Code: state.Code}, ErrValidationRefused
	}
	if state.Phase == domainvalidation.PhaseIntent {
		run, state, err = service.reserve(ctx, run, state, nowMillis)
		if err != nil {
			return Result{Run: run, Progressed: run.Version > 0, Code: state.Code}, err
		}
	}
	repository, observeErr := service.github.ObserveChecksRepository(ctx, githubport.ChecksRepositoryRequest{Owner: state.Binding.RepositoryOwner,
		Name: state.Binding.RepositoryName, RepositoryID: state.Binding.GitHubRepositoryID, RepositoryNodeID: state.Binding.GitHubRepositoryNodeID,
		ExpectedViewer: state.Binding.ViewerLogin, TaskStoreNowMillis: nowMillis})
	if observeErr != nil {
		return service.park(ctx, run, state, domainvalidation.CodeUnavailable)
	}
	state.Repository = &repository
	if !domainvalidation.CurrentRepositoryObservation(repository, state.Binding, nowMillis) {
		return service.park(ctx, run, state, domainvalidation.CodeRepositoryMismatch)
	}
	if repository.Code != domainvalidation.CodeOK {
		return service.park(ctx, run, state, repository.Code)
	}
	before := service.baseObservation(ctx, run, state, nowMillis)
	if code := refCode(before, state, nowMillis); code != domainvalidation.CodeOK {
		if code == domainvalidation.CodeBaseRace {
			return service.invalidateBase(ctx, run, state, before, before, code, nowMillis)
		}
		return service.park(ctx, run, state, code)
	}
	if before.OID != state.Binding.BaseSHA {
		return service.invalidateBase(ctx, run, state, before, before, domainvalidation.CodeBaseChanged, nowMillis)
	}
	workflows, code := service.scanWorkflows(ctx, state, nowMillis)
	if code != domainvalidation.CodeOK {
		return service.park(ctx, run, state, code)
	}
	checks, code := service.scanChecks(ctx, state, nowMillis)
	if code != domainvalidation.CodeOK {
		return service.park(ctx, run, state, code)
	}
	statuses, code := service.scanStatuses(ctx, state, nowMillis)
	if code != domainvalidation.CodeOK {
		return service.park(ctx, run, state, code)
	}
	repositoryAfter, observeErr := service.github.ObserveChecksRepository(ctx, githubport.ChecksRepositoryRequest{Owner: state.Binding.RepositoryOwner,
		Name: state.Binding.RepositoryName, RepositoryID: state.Binding.GitHubRepositoryID, RepositoryNodeID: state.Binding.GitHubRepositoryNodeID,
		ExpectedViewer: state.Binding.ViewerLogin, TaskStoreNowMillis: nowMillis})
	if observeErr != nil {
		return service.park(ctx, run, state, domainvalidation.CodeUnavailable)
	}
	state.RepositoryAfter = &repositoryAfter
	if !domainvalidation.CurrentRepositoryObservation(repositoryAfter, state.Binding, nowMillis) {
		return service.park(ctx, run, state, domainvalidation.CodeRepositoryMismatch)
	}
	if repositoryAfter.Code != domainvalidation.CodeOK {
		return service.park(ctx, run, state, repositoryAfter.Code)
	}
	after := service.baseObservation(ctx, run, state, nowMillis)
	if code := refCode(after, state, nowMillis); code != domainvalidation.CodeOK {
		if code == domainvalidation.CodeBaseRace {
			return service.invalidateBase(ctx, run, state, before, after, code, nowMillis)
		}
		return service.park(ctx, run, state, code)
	}
	if before.OID != after.OID {
		return service.invalidateBase(ctx, run, state, before, after, domainvalidation.CodeBaseRace, nowMillis)
	}
	if after.OID != state.Binding.BaseSHA {
		return service.invalidateBase(ctx, run, state, before, after, domainvalidation.CodeBaseChanged, nowMillis)
	}
	state.Workflows, state.Checks, state.Statuses = &workflows, &checks, &statuses
	state.BaseBeforeSHA256, state.BaseAfterSHA256 = publicationdomain.RefObservationSHA256(before), publicationdomain.RefObservationSHA256(after)
	decision := domainvalidation.Evaluate(state.Binding, state.Policy, workflows, checks, statuses,
		domainvalidation.RepositoryObservationSHA256(repository), domainvalidation.RepositoryObservationSHA256(repositoryAfter),
		state.BaseBeforeSHA256, state.BaseAfterSHA256, nowMillis)
	state.Code = decision.Code
	if decision.Outcome == domainvalidation.OutcomePending {
		started := nowMillis
		if len(workflows.Runs) > 0 {
			started = workflows.Runs[0].StartedAtMillis
		}
		if started < 0 || started > nowMillis {
			return service.park(ctx, run, state, domainvalidation.CodeResponseUnknown)
		}
		if uint64(nowMillis-started) >= state.Policy.CycleRuntimeMillis {
			return service.park(ctx, run, state, domainvalidation.CodeRuntimeExhausted)
		}
		state.Phase = domainvalidation.PhasePending
		next := run
		next.Execution.Validation = &state
		next, err = service.persist(ctx, run, next, "validation.pending")
		return Result{Run: next, Progressed: err == nil, Outcome: decision.Outcome, Code: decision.Code}, err
	}
	if decision.Evidence == nil {
		return service.park(ctx, run, state, decision.Code)
	}
	ledger, err := completeBudget(run, state, decision.Outcome != domainvalidation.OutcomePassed, nowMillis)
	if err != nil {
		return Result{Run: run}, err
	}
	state.Evidence = decision.Evidence
	switch decision.Outcome {
	case domainvalidation.OutcomePassed:
		state.Phase = domainvalidation.PhasePassed
	case domainvalidation.OutcomeTimedOut:
		state.Phase = domainvalidation.PhaseTimedOut
	default:
		state.Phase = domainvalidation.PhaseFailed
	}
	next := run
	next.Execution.Budget, next.Execution.Validation = ledger, &state
	if decision.Outcome == domainvalidation.OutcomePassed {
		authority := *next.Execution.CandidateAuthority
		evidence := &candidate.EvidenceBinding{ID: decision.Evidence.ID, CandidateID: state.Binding.CandidateID,
			CandidateSHA: state.Binding.CandidateSHA, BaseSHA: state.Binding.BaseSHA, Generation: state.Binding.CandidateGeneration,
			BindingSHA256: state.Binding.ManifestSHA256}
		authority.Downstream.Validation = evidence
		copy := *evidence
		authority.Downstream.CI = &copy
		next.Execution.CandidateAuthority = &authority
	}
	if attachCode := attachReview(&next, state); attachCode != domainvalidation.CodeOK {
		state.Code, state.Phase = attachCode, domainvalidation.PhaseNeedsYou
		next.Execution.Validation = &state
		next.Execution.NeedsYou = &execution.NeedsYou{Code: execution.NeedCode(strings.ToLower(string(attachCode))),
			WakeCondition: "one_exact_authoritative_ci_observation", CleanupAuthorized: false}
	}
	next, err = service.persist(ctx, run, next, "validation.terminal."+string(state.Phase))
	return Result{Run: next, Progressed: err == nil, Terminal: true, Outcome: decision.Outcome, Code: state.Code}, err
}
