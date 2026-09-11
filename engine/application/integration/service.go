// SPDX-License-Identifier: Apache-2.0

// Package integration implements engine-owned pull-request final integration.
// The GitHub and Git ports only observe or perform one exact operation; this
// service owns Ready, human authorization, retry, invalidation, and completion.
package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/candidate"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	"github.com/mcuadros/director-engine/domain/correction"
	domainexecution "github.com/mcuadros/director-engine/domain/execution"
	feedbackdomain "github.com/mcuadros/director-engine/domain/feedback"
	domainintegration "github.com/mcuadros/director-engine/domain/integration"
	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
	"github.com/mcuadros/director-engine/domain/review"
	"github.com/mcuadros/director-engine/domain/runtimebudget"
	domainvalidation "github.com/mcuadros/director-engine/domain/validation"
	gitport "github.com/mcuadros/director-engine/ports/git"
	githubport "github.com/mcuadros/director-engine/ports/github"
	deliveryreducer "github.com/mcuadros/director-engine/reducer/delivery"
)

var (
	ErrInvalidCommand       = errors.New("integration command is invalid")
	ErrConcurrentTransition = errors.New("integration transition lost an expected-version race")
	ErrNotReady             = errors.New("pull-request integration is not ready")
	ErrExternalUnavailable  = errors.New("integration external state is unavailable")
	ErrExternalAmbiguous    = errors.New("integration external state is ambiguous")
)

type Store interface {
	Project(context.Context, string) (domain.Project, error)
	Task(context.Context, string) (domain.Task, error)
	Run(context.Context, string) (domain.Run, error)
	Candidate(context.Context, string) (domain.Candidate, error)
	UpdateRun(context.Context, domain.CommandRequest, domain.Run, domain.Event) (domain.CommandResult, error)
}

type Service struct {
	store    Store
	forge    githubport.IntegrationPort
	checks   githubport.ChecksPort
	feedback githubport.FeedbackPort
	git      gitport.IntegrationPort
	gate     sync.Mutex
	locks    map[string]*keyedLock
}

type keyedLock struct {
	mutex sync.Mutex
	users uint32
}

func NewService(store Store, forge githubport.IntegrationPort, checks githubport.ChecksPort,
	feedback githubport.FeedbackPort, git gitport.IntegrationPort) (*Service, error) {
	if store == nil || forge == nil || checks == nil || feedback == nil || git == nil {
		return nil, errors.New("integration store, GitHub, checks, feedback, and Git ports are required")
	}
	return &Service{store: store, forge: forge, checks: checks, feedback: feedback, git: git, locks: make(map[string]*keyedLock)}, nil
}

func (service *Service) lock(key string) func() {
	service.gate.Lock()
	lock := service.locks[key]
	if lock == nil {
		lock = &keyedLock{}
		service.locks[key] = lock
	}
	lock.users++
	service.gate.Unlock()
	lock.mutex.Lock()
	return func() {
		lock.mutex.Unlock()
		service.gate.Lock()
		lock.users--
		if lock.users == 0 {
			delete(service.locks, key)
		}
		service.gate.Unlock()
	}
}

type AdmitCommand struct {
	RunID              string
	ExpectedRunVersion uint64
	LeaseEpoch         uint64
	NowMillis          int64
}
type AuthorizeManualCommand struct {
	RunID              string
	ExpectedRunVersion uint64
	LeaseEpoch         uint64
	Actor              AuthenticatedHumanActor
	Authorization      domainintegration.HumanAuthorization
	NowMillis          int64
}
type StepCommand struct {
	RunID              string
	ExpectedRunVersion uint64
	LeaseEpoch         uint64
	NowMillis          int64
}

type AuthenticatedHumanActor struct {
	Kind, ID, SessionID, Source string
	Authenticated               bool
}

type Result struct {
	Run                                                                    domain.Run
	Progressed, Replayed, Ready, WaitingHuman, WaitingExternal, Integrated bool
	Code                                                                   string
	MergeCommitSHA                                                         string
}

type current struct {
	project domain.Project
	task    domain.Task
	run     domain.Run
	record  domain.Candidate
}

func stableID(prefix string, values ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(values, "\x1f")))
	return prefix + "-" + hex.EncodeToString(sum[:16])
}
func payload(value any) json.RawMessage { encoded, _ := json.Marshal(value); return encoded }
func stateSHA256(value *domainintegration.State) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func (service *Service) load(ctx context.Context, runID string) (current, error) {
	run, err := service.store.Run(ctx, runID)
	if err != nil {
		return current{}, err
	}
	task, err := service.store.Task(ctx, run.TaskID)
	if err != nil {
		return current{run: run}, err
	}
	project, err := service.store.Project(ctx, task.ProjectID)
	if err != nil {
		return current{run: run, task: task}, err
	}
	if run.CurrentCandidateID == "" {
		return current{project: project, run: run, task: task}, ErrNotReady
	}
	record, err := service.store.Candidate(ctx, run.CurrentCandidateID)
	return current{project: project, task: task, run: run, record: record}, err
}

func currentLease(value current, epoch uint64, nowMillis int64) bool {
	lease, binding := value.project.Lease, value.run.Execution.LeaseBinding
	return value.project.ID == value.run.Execution.Scope.ProjectID && value.project.State == "active" && lease != nil &&
		domain.ValidateProjectLease(lease) == nil && lease.DispatchAllowed && lease.Epoch == epoch && binding.Epoch == epoch &&
		lease.HolderInstance == binding.HolderInstance && lease.HolderProcessIdentity == binding.HolderProcessIdentity &&
		nowMillis >= lease.AcquiredAtMillis && nowMillis < lease.ExpiresAtMillis
}

func sameEvidence(binding *candidate.EvidenceBinding, authority *candidate.Authority, id string) bool {
	return binding != nil && authority != nil && binding.ID == id && binding.CandidateID == authority.CandidateID &&
		binding.CandidateSHA == authority.CandidateSHA && binding.BaseSHA == authority.BaseSHA &&
		binding.Generation == authority.Generation && binding.BindingSHA256 == authority.BindingSHA256
}

func currentCandidate(value current) bool {
	authority, record := value.run.Execution.CandidateAuthority, value.record
	return authority != nil && !authority.Invalidated && candidate.ValidAuthority(*authority) && candidate.ValidManifest(record.Manifest) &&
		record.ID == value.run.CurrentCandidateID && record.RunID == value.run.ID && authority.CandidateID == record.ID &&
		authority.CandidateSHA == record.CommitSHA && authority.BaseSHA == value.run.BaseSHA && authority.BindingSHA256 == record.Manifest.BindingSHA256 &&
		authority.TaskVersion == value.task.Version && authority.ConfigurationSHA256 == record.Manifest.ConfigurationSHA256 &&
		authority.DecisionsSHA256 == record.Manifest.DecisionsSHA256 && authority.FindingsSHA256 == record.Manifest.FindingsSHA256 &&
		value.run.Execution.BaseRef == record.Claim.BaseRef && value.run.Execution.Branch == record.Claim.Branch &&
		value.run.Execution.RepositoryBindingHash == record.Manifest.RepositoryBindingSHA256
}

func publicationReady(value current) bool {
	state, authority := value.run.Execution.Publication, value.run.Execution.CandidateAuthority
	return state != nil && authority != nil && publicationdomain.ValidState(*state) && !state.Invalidated && state.Evidence != nil &&
		publicationdomain.ValidEvidence(*state.Evidence, *state) && state.Evidence.Ready && !state.Evidence.Draft && state.OwnedPullRequest != nil &&
		state.Binding.CandidateID == authority.CandidateID && state.Binding.CandidateSHA == authority.CandidateSHA &&
		state.Binding.BaseSHA == authority.BaseSHA && state.Binding.TreeSHA == value.record.Manifest.TreeSHA &&
		state.Binding.ManifestSHA256 == authority.BindingSHA256 && state.Binding.CandidateGeneration == authority.Generation &&
		state.OwnedPullRequest.HeadSHA == authority.CandidateSHA && !state.OwnedPullRequest.Draft &&
		sameEvidence(authority.Downstream.Publication, authority, state.Evidence.ID)
}

func validationPassed(value current) bool {
	state, policy, authority := value.run.Execution.Validation, value.run.Execution.ValidationPolicy, value.run.Execution.CandidateAuthority
	return state != nil && policy != nil && authority != nil && domainvalidation.ValidPolicy(*policy) && domainvalidation.ValidState(*state) &&
		!state.Invalidated && state.Policy.SHA256 == policy.SHA256 && state.Phase == domainvalidation.PhasePassed && state.Evidence != nil &&
		state.Evidence.Outcome == domainvalidation.OutcomePassed && state.Binding.CandidateID == authority.CandidateID &&
		state.Binding.CandidateSHA == authority.CandidateSHA && state.Binding.BaseSHA == authority.BaseSHA &&
		state.Binding.TreeSHA == value.record.Manifest.TreeSHA && state.Binding.ManifestSHA256 == authority.BindingSHA256 &&
		state.Binding.CandidateGeneration == authority.Generation && sameEvidence(authority.Downstream.Validation, authority, state.Evidence.ID) &&
		sameEvidence(authority.Downstream.CI, authority, state.Evidence.ID)
}

func reviewApproved(value current) bool {
	state, authority := value.run.Execution.Review, value.run.Execution.CandidateAuthority
	if state == nil || authority == nil || !review.ValidState(*state) || state.Invalidated || state.Evidence == nil ||
		state.Evidence.Verdict != review.VerdictApproveCandidate || !review.ValidEvidence(*state.Evidence, *state) ||
		state.CIObservation == nil || !review.ValidCIObservation(*state.CIObservation, state.Binding) ||
		state.Binding.CandidateID != authority.CandidateID || state.Binding.CandidateSHA != authority.CandidateSHA ||
		state.Binding.BaseSHA != authority.BaseSHA || state.Binding.TreeSHA != value.record.Manifest.TreeSHA ||
		state.Binding.ManifestSHA256 != authority.BindingSHA256 || state.Binding.CandidateGeneration != authority.Generation ||
		!sameEvidence(authority.Downstream.Review, authority, state.Evidence.ID) {
		return false
	}
	validation := value.run.Execution.Validation
	if validation == nil || validation.Evidence == nil {
		return false
	}
	ci, ok := domainvalidation.ReviewObservation(*validation)
	return ok && ci.SHA256 == state.CIObservation.SHA256 && state.Evidence.CIObservationSHA256 == ci.SHA256
}

func correctionSettled(value current) bool {
	if value.run.Execution.Feedback != nil && feedbackdomain.BlocksDelivery(*value.run.Execution.Feedback) {
		return false
	}
	state := value.run.Execution.Correction
	if state == nil {
		return true
	}
	authority := value.run.Execution.CandidateAuthority
	if authority == nil || !correction.ValidState(*state) || state.Phase != correction.PhaseGatesRequired ||
		state.CurrentCandidateID != authority.CandidateID || state.CurrentCandidateSHA != authority.CandidateSHA || len(state.Gates) == 0 {
		return false
	}
	gate := state.Gates[len(state.Gates)-1]
	return gate.CandidateID == authority.CandidateID && gate.CandidateSHA == authority.CandidateSHA &&
		gate.CandidateGeneration == authority.Generation && gate.FreshCIRequired && gate.FreshReviewRequired && gate.PriorAuthorityInvalidated
}

func feedbackStateSHA(value current) (string, bool) {
	state := value.run.Execution.Feedback
	if state == nil {
		return domainintegration.DigestText("no-current-feedback"), true
	}
	authority := value.run.Execution.CandidateAuthority
	return state.SHA256, authority != nil && feedbackdomain.ValidState(*state) && !state.Invalidated && !feedbackdomain.BlocksDelivery(*state) &&
		state.Binding.TaskID == value.task.ID && state.Binding.RunID == value.run.ID && state.Binding.CandidateID == authority.CandidateID &&
		state.Binding.CandidateSHA == authority.CandidateSHA && state.Binding.BaseSHA == authority.BaseSHA &&
		state.Binding.CandidateGeneration == authority.Generation && state.Binding.ManifestSHA256 == authority.BindingSHA256
}

func ciBudgetValid(value current) bool {
	ledger := value.run.Execution.Budget
	return runtimebudget.ValidLedgerForLease(ledger, value.run.Execution.LeaseBinding.Epoch) && ledger.Consumption.CICycles > 0 &&
		ledger.Consumption.CICycles <= domainvalidation.MaximumCICycles && ledger.Consumption.CICycles <= ledger.Policy.CICycleLimit &&
		!slices.ContainsFunc(ledger.Reservations, func(reservation runtimebudget.Reservation) bool {
			return reservation.Activity == runtimebudget.ActivityValidationCycle && !reservation.Released
		})
}

func currentBinding(value current) (domainintegration.Binding, bool) {
	state, validation, reviewState, policy := value.run.Execution.Publication, value.run.Execution.Validation,
		value.run.Execution.Review, value.run.Execution.IntegrationPolicy
	feedbackSHA, feedbackOK := feedbackStateSHA(value)
	if !currentCandidate(value) || !publicationReady(value) || !validationPassed(value) || !reviewApproved(value) || !feedbackOK ||
		!correctionSettled(value) || !ciBudgetValid(value) || value.run.Execution.DeliveryMode != domainconfig.DeliveryPullRequest ||
		policy == nil || !domainintegration.ValidPolicy(*policy) || value.run.Execution.DirectDelivery != nil ||
		len(value.run.Execution.DirectDeliveryHistory) != 0 || state == nil || state.OwnedPullRequest == nil || validation == nil ||
		validation.Evidence == nil || reviewState == nil || reviewState.Evidence == nil || reviewState.CIObservation == nil ||
		policy.ConfigurationSHA256 != value.record.Manifest.ConfigurationSHA256 {
		return domainintegration.Binding{}, false
	}
	authority := value.run.Execution.CandidateAuthority
	readyID := stableID("integration-ready", authority.BindingSHA256, policy.SHA256)
	binding := domainintegration.SealBinding(domainintegration.Binding{TaskID: value.task.ID, RunID: value.run.ID,
		CandidateID: value.record.ID, CandidateSHA: value.record.CommitSHA, BaseSHA: value.record.Manifest.BaseSHA,
		TreeSHA: value.record.Manifest.TreeSHA, ManifestSHA256: value.record.Manifest.BindingSHA256,
		CandidateGeneration: authority.Generation, TaskVersion: value.task.Version,
		ConfigurationSHA256: value.record.Manifest.ConfigurationSHA256, RepositoryID: value.run.Execution.RepositoryBinding.RepositoryID,
		RepositoryBindingSHA256: value.run.Execution.RepositoryBindingHash, CanonicalRemote: state.Binding.CanonicalRemote,
		GitHubRepositoryID: state.Binding.GitHubRepositoryID, GitHubRepositoryNodeID: state.Binding.GitHubRepositoryNodeID,
		RepositoryOwner: state.Binding.RepositoryOwner, RepositoryName: state.Binding.RepositoryName, ViewerLogin: state.Binding.HeadOwner,
		Branch: state.Binding.Branch, BaseRef: state.Binding.BaseRef, PullRequestNumber: state.OwnedPullRequest.Number,
		PullRequestNodeID: state.OwnedPullRequest.NodeID, OwnershipSHA256: state.Binding.OwnershipSHA256,
		MarkerSHA256: state.OwnedPullRequest.MarkerSHA256, PublicationEvidenceID: state.Evidence.ID,
		ValidationPolicySHA256: validation.Policy.SHA256, ValidationEvidenceID: validation.Evidence.ID,
		ValidationEvidenceSHA256: validation.Evidence.SHA256, ReviewEvidenceID: reviewState.Evidence.ID,
		ReviewerUUID: reviewState.Evidence.ReviewerUUID, CIObservationID: reviewState.CIObservation.ID,
		CIObservationSHA256: reviewState.CIObservation.SHA256, FeedbackStateSHA256: feedbackSHA,
		ReadyEvidenceID: readyID, PolicySHA256: policy.SHA256, LeaseEpoch: value.run.Execution.LeaseBinding.Epoch})
	return binding, domainintegration.ValidBinding(binding)
}

func durableGates(value current, binding domainintegration.Binding, nowMillis int64) deliveryreducer.IntegrationFacts {
	currentBinding, ok := currentBinding(value)
	feedbackSHA, feedbackOK := feedbackStateSHA(value)
	return deliveryreducer.IntegrationFacts{SchemaVersion: deliveryreducer.IntegrationSchemaVersion,
		State: *value.run.Execution.Integration, TaskBlocked: value.task.Complete || value.task.Attention != nil || value.run.Execution.NeedsYou != nil,
		ProjectActive: value.project.State == "active", LeaseCurrent: currentLease(value, binding.LeaseEpoch, nowMillis),
		CandidateCurrent: ok && currentBinding == binding, PublicationReady: publicationReady(value), ValidationPassed: validationPassed(value),
		ReviewApproved: reviewApproved(value), FeedbackClear: feedbackOK && feedbackSHA == binding.FeedbackStateSHA256,
		CorrectionSettled: correctionSettled(value), DirectDeliveryAbsent: value.run.Execution.DirectDelivery == nil && len(value.run.Execution.DirectDeliveryHistory) == 0,
		CIBudgetValid: ciBudgetValid(value), NowMillis: nowMillis}
}

func (service *Service) persist(ctx context.Context, currentRun, next domain.Run, transition string) (domain.Run, error) {
	next.Version = currentRun.Version + 1
	commandID := stableID("integration-command", currentRun.ID, fmt.Sprintf("version-%d", next.Version), transition)
	result, err := service.store.UpdateRun(ctx, domain.CommandRequest{IdempotencyKey: commandID, Type: transition,
		AggregateID: currentRun.ID, ExpectedVersion: currentRun.Version,
		Payload: payload(struct{ Transition, StateSHA256 string }{transition, stateSHA256(next.Execution.Integration)})}, next,
		domain.Event{ID: stableID("integration-event", commandID), RunID: currentRun.ID, Sequence: currentRun.Version + 2,
			AggregateID: currentRun.ID, AggregateVersion: next.Version, Type: transition,
			Payload: payload(struct{ IntegrationID, Phase string }{next.Execution.Integration.ID, string(next.Execution.Integration.Phase)})})
	if err != nil {
		return currentRun, err
	}
	if result.Outcome != domain.CommandApplied || result.Replay {
		return currentRun, ErrConcurrentTransition
	}
	return next, nil
}

func evidenceBinding(authority *candidate.Authority, id string) *candidate.EvidenceBinding {
	return &candidate.EvidenceBinding{ID: id, CandidateID: authority.CandidateID, CandidateSHA: authority.CandidateSHA,
		BaseSHA: authority.BaseSHA, Generation: authority.Generation, BindingSHA256: authority.BindingSHA256}
}

func (service *Service) Admit(ctx context.Context, command AdmitCommand) (Result, error) {
	unlock := service.lock("run:" + command.RunID)
	defer unlock()
	if command.RunID == "" || command.LeaseEpoch == 0 || command.NowMillis < 0 {
		return Result{}, ErrInvalidCommand
	}
	value, err := service.load(ctx, command.RunID)
	if err != nil {
		return Result{Run: value.run}, err
	}
	binding, ok := currentBinding(value)
	if value.run.Execution.Integration != nil {
		state := value.run.Execution.Integration
		if ok && state.Binding == binding && domainintegration.ValidState(*state) {
			return Result{Run: value.run, Replayed: true, Ready: true, WaitingHuman: state.Phase == domainintegration.PhaseReady,
				WaitingExternal: state.Phase == domainintegration.PhaseWaitingExternal, Integrated: state.Phase == domainintegration.PhaseComplete}, nil
		}
		return Result{Run: value.run}, ErrNotReady
	}
	if value.run.Version != command.ExpectedRunVersion || !currentLease(value, command.LeaseEpoch, command.NowMillis) || !ok ||
		binding.LeaseEpoch != command.LeaseEpoch || value.run.Execution.CandidateAuthority.Downstream.Integration != nil {
		return Result{Run: value.run}, ErrNotReady
	}
	state, ok := domainintegration.NewState(binding, *value.run.Execution.IntegrationPolicy)
	if !ok {
		return Result{Run: value.run}, ErrInvalidCommand
	}
	next := value.run
	next.Execution.Integration = &state
	authority := *next.Execution.CandidateAuthority
	if authority.Downstream.Ready == nil {
		authority.Downstream.Ready = evidenceBinding(&authority, binding.ReadyEvidenceID)
	}
	if !sameEvidence(authority.Downstream.Ready, &authority, binding.ReadyEvidenceID) {
		return Result{Run: value.run}, ErrNotReady
	}
	next.Execution.CandidateAuthority = &authority
	next, err = service.persist(ctx, value.run, next, "integration.admitted")
	return Result{Run: next, Progressed: err == nil, Ready: err == nil, WaitingHuman: state.Phase == domainintegration.PhaseReady}, err
}

func validHuman(actor AuthenticatedHumanActor, authorization domainintegration.HumanAuthorization) bool {
	return actor.Kind == "human" && actor.ID != "" && actor.SessionID != "" && actor.Source == "server" && actor.Authenticated &&
		authorization.ActorKind == actor.Kind && authorization.ActorID == actor.ID && authorization.SessionID == actor.SessionID &&
		authorization.ActorSource == domainintegration.AuthorizationActorSource && authorization.Authenticated
}

func (service *Service) AuthorizeManual(ctx context.Context, command AuthorizeManualCommand) (Result, error) {
	unlock := service.lock("run:" + command.RunID)
	defer unlock()
	if command.RunID == "" || command.LeaseEpoch == 0 || command.NowMillis < 0 || !validHuman(command.Actor, command.Authorization) {
		return Result{}, ErrInvalidCommand
	}
	value, err := service.load(ctx, command.RunID)
	if err != nil {
		return Result{Run: value.run}, err
	}
	state := value.run.Execution.Integration
	if state == nil {
		return Result{Run: value.run}, ErrNotReady
	}
	if state.HumanAuthorization != nil && state.HumanAuthorization.SHA256 == command.Authorization.SHA256 {
		return Result{Run: value.run, Replayed: true, Ready: true}, nil
	}
	binding, ok := currentBinding(value)
	if value.run.Version != command.ExpectedRunVersion || !ok || binding != state.Binding ||
		!currentLease(value, command.LeaseEpoch, command.NowMillis) || command.LeaseEpoch != state.Binding.LeaseEpoch {
		return Result{Run: value.run}, ErrNotReady
	}
	nextState, ok := domainintegration.AuthorizeManual(*state, command.Authorization)
	if !ok {
		return Result{Run: value.run}, ErrInvalidCommand
	}
	next := value.run
	next.Execution.Integration = &nextState
	next, err = service.persist(ctx, value.run, next, "integration.human_authorized")
	return Result{Run: next, Progressed: err == nil, Ready: err == nil}, err
}

func gitRequest(value current, binding domainintegration.Binding, mergeCommitSHA string, nowMillis int64) gitport.IntegrationRequest {
	return gitport.IntegrationRequest{Binding: binding, CandidateClaim: value.record.Claim, CandidateManifest: value.record.Manifest,
		Repository: value.run.Execution.RepositoryBinding, RepositoryBindingSHA256: value.run.Execution.RepositoryBindingHash,
		MergeCommitSHA: mergeCommitSHA, TaskStoreNowMillis: nowMillis}
}

func invalidObservation(binding domainintegration.Binding, attempt uint32, code domainintegration.Code, nowMillis int64) domainintegration.Observation {
	status := domainintegration.ObservationInvalid
	if domainintegration.WaitingCode(code) {
		status = domainintegration.ObservationUnavailable
	}
	return domainintegration.SealObservation(domainintegration.Observation{BindingSHA256: binding.SHA256, Attempt: attempt,
		Status: status, Code: code, ObservedAtMillis: nowMillis, MaximumAgeMillis: domainintegration.MaximumObservationAgeMS})
}

func scanWorkflows(ctx context.Context, port githubport.ChecksPort, state domainvalidation.State, nowMillis int64) (domainvalidation.WorkflowScan, domainvalidation.Code) {
	values := []domainvalidation.WorkflowRun{}
	var total uint32
	for page := uint32(1); page <= state.Policy.MaximumPages; page++ {
		value, err := port.ListWorkflowRuns(ctx, githubport.CandidatePageRequest{Owner: state.Binding.RepositoryOwner, Name: state.Binding.RepositoryName,
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

func scanChecks(ctx context.Context, port githubport.ChecksPort, state domainvalidation.State, nowMillis int64) (domainvalidation.CheckScan, domainvalidation.Code) {
	values := []domainvalidation.CheckRun{}
	var total uint32
	for page := uint32(1); page <= state.Policy.MaximumPages; page++ {
		value, err := port.ListCheckRuns(ctx, githubport.CandidatePageRequest{Owner: state.Binding.RepositoryOwner, Name: state.Binding.RepositoryName,
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

func scanStatuses(ctx context.Context, port githubport.ChecksPort, state domainvalidation.State, nowMillis int64) (domainvalidation.StatusScan, domainvalidation.Code) {
	values := []domainvalidation.CommitStatus{}
	var total uint32
	rollup := ""
	for page := uint32(1); page <= state.Policy.MaximumPages; page++ {
		value, err := port.ListCommitStatuses(ctx, githubport.CandidatePageRequest{Owner: state.Binding.RepositoryOwner, Name: state.Binding.RepositoryName,
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

func feedbackBinding(value current) feedbackdomain.Binding {
	authority := value.run.Execution.CandidateAuthority
	publication := value.run.Execution.Publication
	return feedbackdomain.SealBinding(feedbackdomain.Binding{ProjectID: value.task.ProjectID, WorkspaceID: value.task.WorkspaceIDs[0],
		TaskID: value.task.ID, TaskVersion: value.task.Version, RunID: value.run.ID, CandidateID: value.record.ID,
		CandidateSHA: value.record.CommitSHA, BaseSHA: authority.BaseSHA, CandidateGeneration: authority.Generation,
		ManifestSHA256: authority.BindingSHA256, RepositoryID: publication.Binding.GitHubRepositoryID,
		RepositoryNodeID: publication.Binding.GitHubRepositoryNodeID, PullRequestNumber: publication.OwnedPullRequest.Number})
}

func scanFeedback(ctx context.Context, port githubport.FeedbackPort, value current, nowMillis int64) (string, bool, domainintegration.Code) {
	binding := feedbackBinding(value)
	if !feedbackdomain.ValidBinding(binding) {
		return "", false, domainintegration.CodeFeedbackPresent
	}
	sources := []feedbackdomain.Source{feedbackdomain.SourceGitHubReview, feedbackdomain.SourceGitHubReviewComment, feedbackdomain.SourceGitHubIssueComment}
	snapshots := make([]feedbackdomain.Snapshot, 0, len(sources))
	hashes := make([]string, 0, len(sources))
	publication := value.run.Execution.Publication
	for _, source := range sources {
		items := []feedbackdomain.Item{}
		pageHashes := []string{}
		complete := false
		for page := uint32(1); page <= feedbackdomain.MaximumPages; page++ {
			request := githubport.FeedbackRequest{Owner: publication.Binding.RepositoryOwner, Name: publication.Binding.RepositoryName,
				RepositoryID: binding.RepositoryID, RepositoryNodeID: binding.RepositoryNodeID, PullRequestNumber: binding.PullRequestNumber,
				Source: source, Page: page, PageSize: feedbackdomain.MaximumPageSize, CandidateSHA: binding.CandidateSHA,
				BaseSHA: binding.BaseSHA, BindingSHA256: binding.BindingSHA256, TaskStoreNowMillis: nowMillis}
			pageValue, err := port.ObserveFeedbackPage(ctx, request)
			if err != nil || pageValue.Code != "ok" {
				return "", false, domainintegration.CodeUnavailable
			}
			if !githubport.CurrentFeedbackPage(pageValue, request) {
				return "", false, domainintegration.CodeFeedbackPresent
			}
			items = append(items, pageValue.Items...)
			pageHashes = append(pageHashes, pageValue.SHA256)
			if pageValue.Complete {
				complete = true
				break
			}
		}
		if !complete {
			return "", false, domainintegration.CodeFeedbackPresent
		}
		snapshot := feedbackdomain.SealSnapshot(feedbackdomain.Snapshot{ID: stableID("integration-feedback", binding.BindingSHA256, string(source), strings.Join(pageHashes, ":")),
			Source: source, BindingSHA256: binding.BindingSHA256, Complete: true, PageCount: uint32(len(pageHashes)),
			ObservedAtMillis: nowMillis, MaximumAgeMillis: feedbackdomain.MaximumObservationAge, Items: items})
		snapshots = append(snapshots, snapshot)
		hashes = append(hashes, snapshot.SHA256)
	}
	var prior *feedbackdomain.State
	if value.run.Execution.Feedback != nil {
		copy := *value.run.Execution.Feedback
		prior = &copy
	}
	live, _, ok := feedbackdomain.Reconcile(prior, binding, snapshots, nowMillis)
	if !ok || feedbackdomain.BlocksDelivery(live) {
		return domainintegration.DigestText(strings.Join(hashes, ":")), false, domainintegration.CodeFeedbackPresent
	}
	return domainintegration.DigestText(strings.Join(hashes, ":")), true, domainintegration.CodeOK
}

func validationCode(code domainvalidation.Code) domainintegration.Code {
	switch code {
	case domainvalidation.CodeUnavailable, domainvalidation.CodeServer:
		return domainintegration.CodeUnavailable
	case domainvalidation.CodeTLS:
		return domainintegration.CodeTLS
	case domainvalidation.CodeRateLimited:
		return domainintegration.CodeRateLimited
	case domainvalidation.CodeUnauthorized:
		return domainintegration.CodeUnauthorized
	case domainvalidation.CodeForbidden:
		return domainintegration.CodeForbidden
	case domainvalidation.CodeNotFound:
		return domainintegration.CodeNotFound
	default:
		return domainintegration.CodeChecksChanged
	}
}

// observe re-reads forge/Git identity on both sides of configured checks,
// statuses, and all feedback streams. It starts no CI and performs no merge.
func (service *Service) observe(ctx context.Context, value current, state domainintegration.State, nowMillis int64) domainintegration.Observation {
	attempt, binding := state.Integration.Attempt, state.Binding
	forgeBefore, err := service.forge.ObserveIntegration(ctx, githubport.IntegrationRequest{Binding: binding, TaskStoreNowMillis: nowMillis})
	if err != nil {
		return invalidObservation(binding, attempt, domainintegration.CodeUnavailable, nowMillis)
	}
	if forgeBefore.Status == domainintegration.ForgeUnavailable {
		return invalidObservation(binding, attempt, forgeBefore.Code, nowMillis)
	}
	if forgeBefore.Status == domainintegration.ForgeInvalid {
		return invalidObservation(binding, attempt, forgeBefore.Code, nowMillis)
	}
	mergeSHA := forgeBefore.MergeCommitSHA
	gitBefore, err := service.git.ObserveIntegration(ctx, gitRequest(value, binding, mergeSHA, nowMillis))
	if err != nil {
		return invalidObservation(binding, attempt, domainintegration.CodeUnavailable, nowMillis)
	}
	if gitBefore.Status == domainintegration.GitUnavailable {
		return invalidObservation(binding, attempt, gitBefore.Code, nowMillis)
	}
	if gitBefore.Status == domainintegration.GitInvalid {
		return invalidObservation(binding, attempt, gitBefore.Code, nowMillis)
	}
	validation := *value.run.Execution.Validation
	repositoryBefore, err := service.checks.ObserveChecksRepository(ctx, githubport.ChecksRepositoryRequest{Owner: validation.Binding.RepositoryOwner,
		Name: validation.Binding.RepositoryName, RepositoryID: validation.Binding.GitHubRepositoryID,
		RepositoryNodeID: validation.Binding.GitHubRepositoryNodeID, ExpectedViewer: validation.Binding.ViewerLogin, TaskStoreNowMillis: nowMillis})
	if err != nil || !domainvalidation.CurrentRepositoryObservation(repositoryBefore, validation.Binding, nowMillis) || repositoryBefore.Code != domainvalidation.CodeOK {
		return invalidObservation(binding, attempt, domainintegration.CodeUnavailable, nowMillis)
	}
	workflows, code := scanWorkflows(ctx, service.checks, validation, nowMillis)
	if code != domainvalidation.CodeOK {
		return invalidObservation(binding, attempt, validationCode(code), nowMillis)
	}
	checks, code := scanChecks(ctx, service.checks, validation, nowMillis)
	if code != domainvalidation.CodeOK {
		return invalidObservation(binding, attempt, validationCode(code), nowMillis)
	}
	statuses, code := scanStatuses(ctx, service.checks, validation, nowMillis)
	if code != domainvalidation.CodeOK {
		return invalidObservation(binding, attempt, validationCode(code), nowMillis)
	}
	repositoryAfter, err := service.checks.ObserveChecksRepository(ctx, githubport.ChecksRepositoryRequest{Owner: validation.Binding.RepositoryOwner,
		Name: validation.Binding.RepositoryName, RepositoryID: validation.Binding.GitHubRepositoryID,
		RepositoryNodeID: validation.Binding.GitHubRepositoryNodeID, ExpectedViewer: validation.Binding.ViewerLogin, TaskStoreNowMillis: nowMillis})
	if err != nil || !domainvalidation.CurrentRepositoryObservation(repositoryAfter, validation.Binding, nowMillis) || repositoryAfter.Code != domainvalidation.CodeOK {
		return invalidObservation(binding, attempt, domainintegration.CodeUnavailable, nowMillis)
	}
	live := domainvalidation.Evaluate(validation.Binding, validation.Policy, workflows, checks, statuses,
		domainvalidation.RepositoryObservationSHA256(repositoryBefore), domainvalidation.RepositoryObservationSHA256(repositoryAfter),
		gitBefore.FactSHA256, gitBefore.FactSHA256, nowMillis)
	if live.Outcome != domainvalidation.OutcomePassed || live.Evidence == nil || validation.Evidence == nil ||
		live.Evidence.WorkflowRunID != validation.Evidence.WorkflowRunID || live.Evidence.WorkflowAttempt != validation.Evidence.WorkflowAttempt ||
		live.Evidence.WorkflowCheckSuite != validation.Evidence.WorkflowCheckSuite || live.Evidence.StatusRollup != validation.Evidence.StatusRollup ||
		!reflect.DeepEqual(live.Evidence.Checks, validation.Evidence.Checks) {
		return invalidObservation(binding, attempt, domainintegration.CodeChecksChanged, nowMillis)
	}
	feedbackSHA, feedbackClear, feedbackCode := scanFeedback(ctx, service.feedback, value, nowMillis)
	if !feedbackClear {
		return invalidObservation(binding, attempt, feedbackCode, nowMillis)
	}
	forgeAfter, err := service.forge.ObserveIntegration(ctx, githubport.IntegrationRequest{Binding: binding, TaskStoreNowMillis: nowMillis})
	if err != nil {
		return invalidObservation(binding, attempt, domainintegration.CodeUnavailable, nowMillis)
	}
	if forgeAfter.Status == domainintegration.ForgeUnavailable {
		return invalidObservation(binding, attempt, forgeAfter.Code, nowMillis)
	}
	if forgeAfter.Status == domainintegration.ForgeInvalid {
		return invalidObservation(binding, attempt, forgeAfter.Code, nowMillis)
	}
	if forgeAfter.Status != forgeBefore.Status || forgeAfter.HeadSHA != forgeBefore.HeadSHA || forgeAfter.MergeCommitSHA != forgeBefore.MergeCommitSHA {
		return invalidObservation(binding, attempt, domainintegration.CodeConflict, nowMillis)
	}
	gitAfter, err := service.git.ObserveIntegration(ctx, gitRequest(value, binding, mergeSHA, nowMillis))
	if err != nil {
		return invalidObservation(binding, attempt, domainintegration.CodeUnavailable, nowMillis)
	}
	if gitAfter.Status != gitBefore.Status || gitAfter.BaseSHA != gitBefore.BaseSHA || gitAfter.FactSHA256 == "" {
		return invalidObservation(binding, attempt, domainintegration.CodeBaseChanged, nowMillis)
	}
	status := domainintegration.ObservationReady
	if forgeBefore.Status == domainintegration.ForgeIntegrated {
		status = domainintegration.ObservationIntegrated
	}
	observation := domainintegration.SealObservation(domainintegration.Observation{BindingSHA256: binding.SHA256, Attempt: attempt,
		Status: status, Code: domainintegration.CodeOK, ForgeBefore: forgeBefore, ForgeAfter: forgeAfter,
		GitBefore: gitBefore, GitAfter: gitAfter, LiveValidationID: live.Evidence.ID, LiveValidationSHA256: live.Evidence.SHA256,
		ConfiguredCheckCount: uint32(len(validation.Policy.RequiredChecks)), ObservedCheckCount: uint32(len(live.Evidence.Checks)),
		StatusCount: statuses.TotalCount, FeedbackSnapshotSHA256: feedbackSHA, FeedbackClear: true, MergeCommitSHA: mergeSHA,
		ObservedAtMillis: nowMillis, MaximumAgeMillis: domainintegration.MaximumObservationAgeMS})
	if !domainintegration.ValidObservation(observation, binding, state.Integration.ID, nowMillis) {
		return invalidObservation(binding, attempt, domainintegration.CodeResponseUnknown, nowMillis)
	}
	return observation
}

func (service *Service) park(ctx context.Context, value current, code, wake string, invalidate bool) (Result, error) {
	state := *value.run.Execution.Integration
	if invalidate {
		state = domainintegration.Invalidate(state, code, wake)
	} else {
		var ok bool
		state, ok = domainintegration.Park(state, code, wake)
		if !ok {
			return Result{Run: value.run}, ErrExternalAmbiguous
		}
	}
	if !domainintegration.ValidState(state) {
		return Result{Run: value.run}, ErrExternalAmbiguous
	}
	next := value.run
	next.Execution.Integration = &state
	if invalidate {
		// Candidate/base/check/feedback drift has an existing deterministic
		// revalidation or correction route. Preserve its exact integration
		// invalidation code without manufacturing a human-only Needs-you gate.
		next.Execution.NeedsYou = nil
	} else {
		need := domainexecution.NeedsYou{Code: domainexecution.NeedCode(strings.ToLower(code)), WakeCondition: wake, CleanupAuthorized: false}
		next.Execution.NeedsYou = &need
	}
	if next.Execution.CandidateAuthority != nil {
		authority := *next.Execution.CandidateAuthority
		authority.Downstream.Ready = nil
		authority.Downstream.Integration = nil
		next.Execution.CandidateAuthority = &authority
	}
	next, err := service.persist(ctx, value.run, next, "integration."+strings.ToLower(code))
	return Result{Run: next, Progressed: err == nil, Code: code}, errors.Join(ErrExternalAmbiguous, err)
}

func invalidateCode(code string) bool {
	return slices.Contains([]string{string(domainintegration.CodeHeadChanged), string(domainintegration.CodeBaseChanged),
		string(domainintegration.CodeCandidateChanged), string(domainintegration.CodeChecksChanged), string(domainintegration.CodeFeedbackPresent),
		deliveryreducer.IntegrationCodeCandidateStale, deliveryreducer.IntegrationCodePublicationStale,
		deliveryreducer.IntegrationCodeValidationStale, deliveryreducer.IntegrationCodeReviewStale,
		deliveryreducer.IntegrationCodeFeedbackPending}, code)
}

func (service *Service) applyDecision(ctx context.Context, value current, decision deliveryreducer.IntegrationDecision, nowMillis int64) (Result, error) {
	state := *value.run.Execution.Integration
	switch decision.Kind {
	case deliveryreducer.IntegrationDecisionWaitHuman:
		return Result{Run: value.run, Ready: true, WaitingHuman: true}, nil
	case deliveryreducer.IntegrationDecisionWaitExternal:
		return Result{Run: value.run, Ready: true, WaitingExternal: true, Code: decision.Code}, ErrExternalUnavailable
	case deliveryreducer.IntegrationDecisionComplete:
		if state.Phase == domainintegration.PhaseComplete {
			return Result{Run: value.run, Ready: true, Integrated: true, MergeCommitSHA: state.Evidence.MergeCommitSHA}, nil
		}
		if state.Integration == nil || state.Integration.Observation == nil {
			return Result{Run: value.run}, ErrExternalAmbiguous
		}
		completed, ok := domainintegration.Complete(state, *state.Integration.Observation, nowMillis)
		if !ok {
			return Result{Run: value.run}, ErrExternalAmbiguous
		}
		next := value.run
		next.Execution.Integration = &completed
		next.Execution.NeedsYou = nil
		authority := *next.Execution.CandidateAuthority
		authority.Downstream.Integration = evidenceBinding(&authority, completed.Evidence.ID)
		next.Execution.CandidateAuthority = &authority
		next, err := service.persist(ctx, value.run, next, "integration.complete")
		return Result{Run: next, Progressed: err == nil, Ready: err == nil, Integrated: err == nil, MergeCommitSHA: completed.Evidence.MergeCommitSHA}, err
	case deliveryreducer.IntegrationDecisionEscalate:
		if (state.Phase == domainintegration.PhaseNeedsYou && state.NeedsYouCode == decision.Code) ||
			(state.Phase == domainintegration.PhaseInvalidated && state.InvalidationCode == decision.Code) {
			return Result{Run: value.run, Code: decision.Code}, ErrExternalAmbiguous
		}
		wake := "fresh_exact_integration_observation"
		if invalidateCode(decision.Code) {
			wake = "fresh_candidate_validation_review_feedback_and_publication"
		}
		return service.park(ctx, value, decision.Code, wake, invalidateCode(decision.Code))
	default:
		return Result{Run: value.run}, nil
	}
}

// Step advances one durable frontier and, after any merge response, performs
// the required immediate authoritative reobservation before returning.
func (service *Service) Step(ctx context.Context, command StepCommand) (Result, error) {
	unlockRun := service.lock("run:" + command.RunID)
	defer unlockRun()
	if command.RunID == "" || command.LeaseEpoch == 0 || command.NowMillis < 0 {
		return Result{}, ErrInvalidCommand
	}
	value, err := service.load(ctx, command.RunID)
	if err != nil {
		return Result{Run: value.run}, err
	}
	if value.run.Execution.Integration == nil {
		return Result{Run: value.run}, ErrNotReady
	}
	unlockRepository := service.lock("repository:" + value.run.Execution.RepositoryBinding.RepositoryID)
	defer unlockRepository()
	state := *value.run.Execution.Integration
	if value.run.Version != command.ExpectedRunVersion || command.LeaseEpoch != state.Binding.LeaseEpoch {
		return Result{Run: value.run}, ErrConcurrentTransition
	}
	decision := deliveryreducer.ReduceIntegration(durableGates(value, state.Binding, command.NowMillis))
	retryUnavailable := state.Phase == domainintegration.PhaseWaitingExternal && decision.Kind == deliveryreducer.IntegrationDecisionWaitExternal
	if decision.Kind != deliveryreducer.IntegrationDecisionObserve && decision.Kind != deliveryreducer.IntegrationDecisionDispatch && !retryUnavailable {
		return service.applyDecision(ctx, value, decision, command.NowMillis)
	}
	if state.Integration == nil {
		return Result{Run: value.run}, ErrNotReady
	}
	observation := service.observe(ctx, value, state, command.NowMillis)
	observed, ok := domainintegration.RecordObservation(state, observation, command.NowMillis)
	if !ok {
		return service.park(ctx, value, string(domainintegration.CodeResponseUnknown), "fresh_exact_integration_observation", false)
	}
	next := value.run
	next.Execution.Integration = &observed
	next, err = service.persist(ctx, value.run, next, "integration.observed")
	if err != nil {
		return Result{Run: value.run}, err
	}
	value, err = service.load(ctx, next.ID)
	if err != nil {
		return Result{Run: next}, err
	}
	decision = deliveryreducer.ReduceIntegration(durableGates(value, observed.Binding, command.NowMillis))
	if decision.Kind != deliveryreducer.IntegrationDecisionDispatch {
		return service.applyDecision(ctx, value, decision, command.NowMillis)
	}
	state = *value.run.Execution.Integration
	precondition := *state.Integration.Observation
	dispatching, ok := domainintegration.BeginDispatch(state)
	if !ok {
		return Result{Run: value.run}, ErrExternalAmbiguous
	}
	dispatchRun := value.run
	dispatchRun.Execution.Integration = &dispatching
	dispatchRun, err = service.persist(ctx, value.run, dispatchRun, "integration.dispatching")
	if err != nil {
		return Result{Run: value.run}, err
	}
	mergeResult, mergeErr := service.forge.MergeExpectedHead(ctx, githubport.MergeRequest{Binding: state.Binding,
		Attempt: dispatching.Integration.Attempt, ExpectedObservation: precondition.ForgeAfter, TaskStoreNowMillis: command.NowMillis})
	required, ok := domainintegration.RequireObservation(dispatching)
	if !ok {
		return Result{Run: dispatchRun}, ErrExternalAmbiguous
	}
	observingRun := dispatchRun
	observingRun.Execution.Integration = &required
	observingRun, err = service.persist(ctx, dispatchRun, observingRun, "integration.observation_required")
	if err != nil {
		return Result{Run: dispatchRun}, err
	}
	// A proven-before-handoff failure remains retryable only after this fresh
	// observation. Possible handoff and response loss use the same safe path.
	value, err = service.load(ctx, observingRun.ID)
	if err != nil {
		return Result{Run: observingRun}, err
	}
	post := service.observe(ctx, value, *value.run.Execution.Integration, command.NowMillis)
	postState, ok := domainintegration.RecordObservation(*value.run.Execution.Integration, post, command.NowMillis)
	if !ok {
		return Result{Run: value.run}, errors.Join(ErrExternalAmbiguous, mergeErr)
	}
	postRun := value.run
	postRun.Execution.Integration = &postState
	postRun, err = service.persist(ctx, value.run, postRun, "integration.post_response_observed")
	if err != nil {
		return Result{Run: value.run}, errors.Join(err, mergeErr)
	}
	value, err = service.load(ctx, postRun.ID)
	if err != nil {
		return Result{Run: postRun}, errors.Join(err, mergeErr)
	}
	decision = deliveryreducer.ReduceIntegration(durableGates(value, postState.Binding, command.NowMillis))
	result, decisionErr := service.applyDecision(ctx, value, decision, command.NowMillis)
	if mergeErr != nil {
		decisionErr = errors.Join(decisionErr, mergeErr)
	}
	if !mergeResult.Handoff && mergeResult.Code != domainintegration.CodeOK && decision.Kind == deliveryreducer.IntegrationDecisionDispatch {
		decisionErr = errors.Join(decisionErr, ErrExternalUnavailable)
	}
	return result, decisionErr
}

// Deterministic check identifiers are returned in stable order for callers
// that need to project the exact Ready gate without exposing provider output.
func RequiredCheckIDs(state domainvalidation.State) []string {
	result := make([]string, len(state.Policy.RequiredChecks))
	for index, check := range state.Policy.RequiredChecks {
		result[index] = check.ID
	}
	sort.Strings(result)
	return result
}
