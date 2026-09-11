// SPDX-License-Identifier: Apache-2.0

// Package publication implements the engine-owned PR publication vertical.
// It serializes every frontier through the Run expected-version lock, records
// intent before handoff, observes after every possible handoff, and never
// changes delivery mode or grants merge authority.
package publication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/candidate"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	domaincorrection "github.com/mcuadros/director-engine/domain/correction"
	"github.com/mcuadros/director-engine/domain/execution"
	feedbackdomain "github.com/mcuadros/director-engine/domain/feedback"
	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
	domainreview "github.com/mcuadros/director-engine/domain/review"
	gitport "github.com/mcuadros/director-engine/ports/git"
	githubport "github.com/mcuadros/director-engine/ports/github"
)

// publicationRunLocks is the process-local half of the state lock. The Run
// CAS is durable; the active Project lease and takeover process-absence gate
// fence other processes. Keeping the mutex across adapter handoff prevents a
// second reconciler in the current engine from classifying an in-flight
// dispatch before its caller returns.
var publicationRunLocks sync.Map

func lockPublicationRun(runID string) func() {
	value, _ := publicationRunLocks.LoadOrStore(runID, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}

var (
	ErrInvalidCommand       = errors.New("publication command is invalid")
	ErrConcurrentTransition = errors.New("publication transition lost the Run state lock")
	ErrExternalUnavailable  = errors.New("publication external facts are unavailable")
	ErrResponseUnknown      = errors.New("publication effect result is unknown")
	ErrReviewRequired       = errors.New("publication is waiting for exact approved Review")
	ErrCorrectionPending    = errors.New("publication is blocked by active or parked correction")
	ErrPublicationRefused   = errors.New("publication failed closed")
)

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
	github githubport.Port
}

func NewService(store Store, branch gitport.BranchPort, forge githubport.Port) (*Service, error) {
	if store == nil || branch == nil || forge == nil {
		return nil, errors.New("publication store, Git branch port, and GitHub port are required")
	}
	return &Service{store: store, git: branch, github: forge}, nil
}

type AdmitCommand struct {
	RunID                  string
	ExpectedRunVersion     uint64
	GitHubRepositoryID     int64
	GitHubRepositoryNodeID string
	HeadOwner              string
	OwnershipSHA256        string
	ExpectedRemoteHead     string
	NowMillis              int64
}

type Result struct {
	Run              domain.Run
	Progressed       bool
	WaitingForReview bool
	WaitingExternal  bool
	Published        bool
	Draft            bool
	MergeAuthorized  bool
	Code             publicationdomain.ExternalCode
}

func stableID(prefix string, values ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(values, "\x1f")))
	return prefix + "-" + hex.EncodeToString(sum[:16])
}

func eventPayload(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal fixed publication event: " + err.Error())
	}
	return encoded
}

func publicationStateHash(value execution.State) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func (service *Service) persist(ctx context.Context, current, next domain.Run, transition string) (domain.Run, error) {
	next.Version = current.Version + 1
	commandID := stableID("command", current.ID, fmt.Sprintf("version-%d", next.Version), transition)
	publicationKey := ""
	bindingHash := ""
	if next.Execution.Publication != nil {
		publicationKey = next.Execution.Publication.PublicationKey
		bindingHash = next.Execution.Publication.Binding.BindingSHA256
	}
	result, err := service.store.UpdateRun(ctx, domain.CommandRequest{
		IdempotencyKey: commandID, Type: transition, AggregateID: current.ID, ExpectedVersion: current.Version,
		Payload: eventPayload(struct{ Transition, StateSHA256 string }{transition, publicationStateHash(next.Execution)}),
	}, next, domain.Event{
		ID: stableID("event", commandID), RunID: current.ID, Sequence: current.Version + 2,
		AggregateID: current.ID, AggregateVersion: next.Version, Type: transition,
		Payload: eventPayload(struct{ PublicationKey, BindingSHA256 string }{publicationKey, bindingHash}),
	})
	if err != nil {
		return current, err
	}
	if result.Outcome != domain.CommandApplied || result.Replay {
		return current, ErrConcurrentTransition
	}
	return next, nil
}

func currentProjectLease(project domain.Project, run domain.Run, nowMillis int64) bool {
	lease := project.Lease
	binding := run.Execution.LeaseBinding
	return project.ID == run.Execution.Scope.ProjectID && project.State == "active" && lease != nil &&
		domain.ValidateProjectLease(lease) == nil && execution.ValidLeaseBinding(binding) && lease.DispatchAllowed &&
		lease.HolderInstance == binding.HolderInstance && lease.HolderProcessIdentity == binding.HolderProcessIdentity &&
		lease.Epoch == binding.Epoch && nowMillis >= lease.AcquiredAtMillis && nowMillis < lease.ExpiresAtMillis
}

// correctionAllowsPublication admits an initial Candidate with no correction
// state, or the exact new Candidate after M4.4 has atomically invalidated prior
// authority and recorded its fresh-gates plan. Every active/parked correction
// and every Run-level NeedsYou fact blocks even Git/GitHub observation.
func correctionAllowsPublication(run domain.Run) bool {
	if run.Execution.NeedsYou != nil || run.Execution.Feedback != nil && feedbackdomain.BlocksDelivery(*run.Execution.Feedback) {
		return false
	}
	state := run.Execution.Correction
	if state == nil {
		return true
	}
	authority := run.Execution.CandidateAuthority
	if authority == nil || !domaincorrection.ValidState(*state) || state.Phase != domaincorrection.PhaseGatesRequired ||
		state.CurrentCandidateID != run.CurrentCandidateID || state.CurrentCandidateID != authority.CandidateID ||
		state.CurrentCandidateSHA != authority.CandidateSHA || len(state.Gates) == 0 {
		return false
	}
	gate := state.Gates[len(state.Gates)-1]
	gateCurrent := gate.CandidateID == authority.CandidateID && gate.CandidateSHA == authority.CandidateSHA &&
		gate.CandidateGeneration == authority.Generation && gate.FreshCIRequired && gate.FreshReviewRequired &&
		gate.PriorAuthorityInvalidated
	return gateCurrent
}

func repositoryName(key string) (string, string, bool) {
	parts := strings.Split(key, "/")
	if len(parts) != 3 || parts[0] != "github.com" || parts[1] == "" || parts[2] == "" {
		return "", "", false
	}
	return parts[1], parts[2], true
}

func reviewFacts(run domain.Run) (string, string, string, string, bool) {
	status, reviewID, validation, validationID := "pending", "", "pending", ""
	approved := false
	state := run.Execution.Review
	authority := run.Execution.CandidateAuthority
	if state == nil || authority == nil || state.Invalidated || !domainreview.ValidState(*state) ||
		state.Binding.TaskID != run.TaskID || state.Binding.RunID != run.ID ||
		state.Binding.CandidateID != authority.CandidateID || state.Binding.CandidateSHA != authority.CandidateSHA ||
		state.Binding.BaseSHA != authority.BaseSHA || state.Binding.ManifestSHA256 != authority.BindingSHA256 ||
		state.Binding.CandidateGeneration != authority.Generation {
		return status, reviewID, validation, validationID, false
	}
	if state.CIObservation != nil {
		validation = state.CIObservation.Status
		validationID = state.CIObservation.ID
	}
	if state.Evidence != nil && domainreview.ValidEvidence(*state.Evidence, *state) {
		status = string(state.Evidence.Verdict)
		reviewID = state.Evidence.ID
		approved = state.Evidence.Verdict == domainreview.VerdictApproveCandidate &&
			authority.Downstream.Review != nil && authority.Downstream.Review.ID == state.Evidence.ID &&
			authority.Downstream.Review.CandidateID == authority.CandidateID &&
			authority.Downstream.Review.CandidateSHA == authority.CandidateSHA &&
			authority.Downstream.Review.BaseSHA == authority.BaseSHA &&
			authority.Downstream.Review.Generation == authority.Generation &&
			authority.Downstream.Review.BindingSHA256 == authority.BindingSHA256
	}
	return status, reviewID, validation, validationID, approved
}

func riskCodes(run domain.Run) []string {
	if run.Execution.Claim == nil {
		return []string{}
	}
	result := slices.Clone(run.Execution.Claim.ResidualRiskCodes)
	sort.Strings(result)
	return slices.Compact(result)
}

func desiredTemplate(task domain.Task, run domain.Run, record domain.Candidate, state publicationdomain.State) (publicationdomain.Template, bool, bool) {
	reviewStatus, reviewID, validationStatus, validationID, approved := reviewFacts(run)
	template, ok := publicationdomain.RenderTemplate(state.Binding, state.Policy, task.Title, reviewStatus, reviewID,
		validationStatus, validationID, riskCodes(run))
	return template, approved, ok && record.ID == state.Binding.CandidateID
}

func currentBinding(task domain.Task, run domain.Run, record domain.Candidate, state publicationdomain.State) bool {
	authority := run.Execution.CandidateAuthority
	return authority != nil && candidate.ValidAuthority(*authority) && !authority.Invalidated &&
		run.Execution.DeliveryMode == domainconfig.DeliveryPullRequest && run.Execution.DirectDelivery == nil &&
		len(run.Execution.DirectDeliveryHistory) == 0 &&
		run.CurrentCandidateID == record.ID && record.RunID == run.ID && task.Version == authority.TaskVersion &&
		authority.CandidateID == state.Binding.CandidateID && authority.CandidateSHA == state.Binding.CandidateSHA &&
		authority.BaseSHA == state.Binding.BaseSHA && authority.Generation == state.Binding.CandidateGeneration &&
		authority.BindingSHA256 == state.Binding.ManifestSHA256 && record.Manifest.BindingSHA256 == state.Binding.ManifestSHA256 &&
		record.CommitSHA == state.Binding.CandidateSHA && record.Manifest.BaseSHA == state.Binding.BaseSHA &&
		record.Manifest.TreeSHA == state.Binding.TreeSHA && task.Version == state.Binding.TaskVersion &&
		run.Execution.RepositoryBindingHash == state.Binding.RepositoryBindingSHA256 &&
		run.Execution.RepositoryBinding.CanonicalRemote == state.Binding.CanonicalRemote &&
		run.Execution.Branch == state.Binding.Branch && run.Execution.BaseRef == state.Binding.BaseRef &&
		run.Execution.PublicationPolicy != nil && run.Execution.PublicationPolicy.SHA256 == state.Binding.PolicySHA256
}

func carriedPull(run domain.Run, binding publicationdomain.Binding) *publicationdomain.OwnedPullRequest {
	for index := len(run.Execution.PublicationHistory) - 1; index >= 0; index-- {
		historical := run.Execution.PublicationHistory[index]
		if historical.Binding.TaskID == binding.TaskID && historical.Binding.RunID == binding.RunID &&
			historical.Binding.Branch == binding.Branch && historical.Binding.RepositoryBindingSHA256 == binding.RepositoryBindingSHA256 &&
			historical.Binding.GitHubRepositoryID == binding.GitHubRepositoryID &&
			historical.Binding.OwnershipSHA256 == binding.OwnershipSHA256 {
			return publicationdomain.Carry(historical)
		}
	}
	return nil
}

// Admit persists one Candidate-bound publication intent without contacting Git
// or GitHub. A correction can carry only the exact previously bound PR number;
// its old Candidate becomes the mandatory branch lease expectation.
func (service *Service) Admit(ctx context.Context, command AdmitCommand) (Result, error) {
	run, err := service.store.Run(ctx, command.RunID)
	if err != nil {
		return Result{}, err
	}
	if run.Version != command.ExpectedRunVersion || command.NowMillis < 0 || run.CurrentCandidateID == "" ||
		run.Execution.CandidateAuthority == nil || run.Execution.CandidateAuthority.Invalidated ||
		!candidate.ValidAuthority(*run.Execution.CandidateAuthority) || run.Execution.PublicationPolicy == nil ||
		run.Execution.DeliveryMode != domainconfig.DeliveryPullRequest || run.Execution.DirectDelivery != nil ||
		len(run.Execution.DirectDeliveryHistory) != 0 ||
		!publicationdomain.ValidPolicy(*run.Execution.PublicationPolicy) || command.GitHubRepositoryID <= 0 ||
		command.GitHubRepositoryNodeID == "" || command.HeadOwner == "" || command.OwnershipSHA256 == "" {
		return Result{Run: run}, ErrInvalidCommand
	}
	if !correctionAllowsPublication(run) {
		return Result{Run: run}, ErrCorrectionPending
	}
	if run.Execution.Publication != nil {
		state := *run.Execution.Publication
		if publicationdomain.ValidState(state) && currentCandidatePublication(run, state) &&
			state.Binding.GitHubRepositoryID == command.GitHubRepositoryID &&
			state.Binding.GitHubRepositoryNodeID == command.GitHubRepositoryNodeID &&
			strings.EqualFold(state.Binding.HeadOwner, command.HeadOwner) &&
			state.Binding.OwnershipSHA256 == command.OwnershipSHA256 &&
			state.ExpectedRemoteHead == command.ExpectedRemoteHead {
			return Result{Run: run}, nil
		}
		return Result{Run: run}, ErrInvalidCommand
	}
	task, err := service.store.Task(ctx, run.TaskID)
	if err != nil {
		return Result{}, err
	}
	project, err := service.store.Project(ctx, task.ProjectID)
	if err != nil {
		return Result{}, err
	}
	if len(task.WorkspaceIDs) != 1 || !currentProjectLease(project, run, command.NowMillis) {
		return Result{Run: run}, ErrInvalidCommand
	}
	workspace, err := service.store.Workspace(ctx, task.WorkspaceIDs[0])
	if err != nil {
		return Result{}, err
	}
	record, err := service.store.Candidate(ctx, run.CurrentCandidateID)
	if err != nil {
		return Result{}, err
	}
	authority := run.Execution.CandidateAuthority
	owner, name, ok := repositoryName(run.Execution.RepositoryBinding.RepositoryKey)
	if !ok || workspace.ID != run.Execution.Scope.WorkspaceID || workspace.Repository.ID != run.Execution.RepositoryBinding.RepositoryID ||
		workspace.Repository.Key != run.Execution.RepositoryBinding.RepositoryKey || workspace.Repository.CanonicalRemote != run.Execution.RepositoryBinding.CanonicalRemote ||
		record.RunID != run.ID || record.ID != authority.CandidateID || record.CommitSHA != authority.CandidateSHA ||
		record.Manifest.BindingSHA256 != authority.BindingSHA256 || record.Manifest.BaseSHA != authority.BaseSHA ||
		record.Manifest.TreeSHA == "" || task.Version != authority.TaskVersion {
		return Result{Run: run}, ErrInvalidCommand
	}
	policy := *run.Execution.PublicationPolicy
	baseBranch := strings.TrimPrefix(run.Execution.BaseRef, "refs/heads/")
	if run.Execution.Branch == "main" || run.Execution.Branch == "master" || run.Execution.Branch == baseBranch ||
		slices.Contains(policy.ProtectedBranches, run.Execution.Branch) {
		return Result{Run: run, Code: publicationdomain.CodeProtectedRef}, ErrPublicationRefused
	}
	binding := publicationdomain.SealBinding(publicationdomain.Binding{
		TaskID: task.ID, RunID: run.ID, CandidateID: record.ID, CandidateSHA: record.CommitSHA,
		BaseSHA: record.Manifest.BaseSHA, TreeSHA: record.Manifest.TreeSHA, ManifestSHA256: record.Manifest.BindingSHA256,
		CandidateGeneration: authority.Generation, TaskVersion: task.Version, Branch: run.Execution.Branch, BaseRef: run.Execution.BaseRef,
		RepositoryBindingSHA256: run.Execution.RepositoryBindingHash, CanonicalRemote: run.Execution.RepositoryBinding.CanonicalRemote,
		GitHubRepositoryID: command.GitHubRepositoryID, GitHubRepositoryNodeID: command.GitHubRepositoryNodeID,
		RepositoryOwner: owner, RepositoryName: name, HeadOwner: command.HeadOwner,
		OwnershipSHA256: command.OwnershipSHA256, PolicySHA256: policy.SHA256,
	})
	carried := carriedPull(run, binding)
	expected := command.ExpectedRemoteHead
	if carried != nil {
		if expected != carried.HeadSHA {
			return Result{Run: run}, ErrInvalidCommand
		}
	} else if expected != "" {
		return Result{Run: run}, ErrInvalidCommand
	}
	placeholder, ok := publicationdomain.RenderTemplate(binding, policy, task.Title, "pending", "", "pending", "", riskCodes(run))
	if !ok {
		return Result{Run: run}, ErrInvalidCommand
	}
	state, ok := publicationdomain.NewState(binding, policy, placeholder, expected, carried)
	if !ok {
		return Result{Run: run}, ErrInvalidCommand
	}
	template, _, ok := desiredTemplate(task, run, record, state)
	if !ok {
		return Result{Run: run}, ErrInvalidCommand
	}
	state.Template = template
	next := run
	next.Execution.Publication = &state
	next, err = service.persist(ctx, run, next, "publication.intent_recorded")
	return Result{Run: next, Progressed: err == nil}, err
}

func currentCandidatePublication(run domain.Run, state publicationdomain.State) bool {
	return run.Execution.CandidateAuthority != nil && run.CurrentCandidateID == state.Binding.CandidateID &&
		run.Execution.CandidateAuthority.CandidateSHA == state.Binding.CandidateSHA &&
		run.Execution.CandidateAuthority.Generation == state.Binding.CandidateGeneration
}

func (service *Service) load(ctx context.Context, runID string, nowMillis int64) (domain.Project, domain.Task, domain.Run, domain.Candidate, publicationdomain.State, error) {
	run, err := service.store.Run(ctx, runID)
	if err != nil {
		return domain.Project{}, domain.Task{}, domain.Run{}, domain.Candidate{}, publicationdomain.State{}, err
	}
	if run.Execution.Publication == nil || !publicationdomain.ValidState(*run.Execution.Publication) || run.CurrentCandidateID == "" ||
		run.Execution.DeliveryMode != domainconfig.DeliveryPullRequest || run.Execution.DirectDelivery != nil ||
		len(run.Execution.DirectDeliveryHistory) != 0 {
		return domain.Project{}, domain.Task{}, run, domain.Candidate{}, publicationdomain.State{}, ErrInvalidCommand
	}
	task, err := service.store.Task(ctx, run.TaskID)
	if err != nil {
		return domain.Project{}, domain.Task{}, run, domain.Candidate{}, publicationdomain.State{}, err
	}
	project, err := service.store.Project(ctx, task.ProjectID)
	if err != nil {
		return domain.Project{}, task, run, domain.Candidate{}, publicationdomain.State{}, err
	}
	record, err := service.store.Candidate(ctx, run.CurrentCandidateID)
	if err != nil {
		return project, task, run, domain.Candidate{}, publicationdomain.State{}, err
	}
	state := *run.Execution.Publication
	if !currentProjectLease(project, run, nowMillis) || !currentBinding(task, run, record, state) {
		return project, task, run, record, state, ErrPublicationRefused
	}
	if !correctionAllowsPublication(run) {
		return project, task, run, record, state, ErrCorrectionPending
	}
	return project, task, run, record, state, nil
}

func effectPointer(state *publicationdomain.State, kind string) *publicationdomain.Effect {
	switch kind {
	case "quiesce":
		return &state.Quiesce
	case "push":
		return &state.Push
	case "pull_request":
		return &state.PullRequest
	case "metadata":
		return &state.Metadata
	case "ready":
		return &state.Ready
	default:
		panic("unknown publication frontier")
	}
}

func waitingCode(code publicationdomain.ExternalCode) bool {
	return slices.Contains([]publicationdomain.ExternalCode{publicationdomain.CodeUnavailable, publicationdomain.CodeRateLimited,
		publicationdomain.CodeServer}, code)
}

func (service *Service) persistFailure(ctx context.Context, run domain.Run, state publicationdomain.State, kind string, code publicationdomain.ExternalCode) (Result, error) {
	effect := effectPointer(&state, kind)
	effect.Code = code
	if waitingCode(code) {
		effect.Phase = publicationdomain.EffectWaiting
	} else {
		effect.Phase = publicationdomain.EffectNeedsYou
	}
	next := run
	next.Execution.Publication = &state
	next, err := service.persist(ctx, run, next, "publication."+kind+"."+string(effect.Phase))
	return Result{Run: next, Progressed: err == nil, WaitingExternal: waitingCode(code), Code: code}, errors.Join(func() error {
		if waitingCode(code) {
			return ErrExternalUnavailable
		}
		return ErrPublicationRefused
	}(), err)
}

func (service *Service) observeRepository(ctx context.Context, state publicationdomain.State, nowMillis int64) publicationdomain.RepositoryObservation {
	observation, err := service.github.ObserveRepository(ctx, githubport.RepositoryRequest{
		Owner: state.Binding.RepositoryOwner, Name: state.Binding.RepositoryName,
		RepositoryID: state.Binding.GitHubRepositoryID, RepositoryNodeID: state.Binding.GitHubRepositoryNodeID,
		ExpectedViewer: state.Binding.HeadOwner, TaskStoreNowMillis: nowMillis,
	})
	if err != nil {
		observation = publicationdomain.SealRepositoryObservation(publicationdomain.RepositoryObservation{
			ID:   stableID("github-repository-unavailable", state.Binding.BindingSHA256, strconv.FormatInt(nowMillis, 10)),
			Code: publicationdomain.CodeUnavailable, RepositoryID: state.Binding.GitHubRepositoryID,
			RepositoryNodeID: state.Binding.GitHubRepositoryNodeID, Owner: state.Binding.RepositoryOwner,
			Name: state.Binding.RepositoryName, ViewerLogin: state.Binding.HeadOwner,
			ObservedAtMillis: nowMillis, MaximumAgeMillis: state.Policy.ObservationAgeMS,
		})
	}
	return observation
}

func preflightCode(observation publicationdomain.RepositoryObservation, state publicationdomain.State, nowMillis int64) publicationdomain.ExternalCode {
	if !publicationdomain.CurrentRepositoryObservation(observation, state.Binding, nowMillis) {
		return publicationdomain.CodeRepositoryMismatch
	}
	if observation.Code != publicationdomain.CodeOK {
		return observation.Code
	}
	if !observation.Authenticated {
		return publicationdomain.CodeUnauthorized
	}
	if !observation.CanPush || !observation.CanPullRequests {
		return publicationdomain.CodeCapabilityMissing
	}
	if observation.RateRemaining <= 0 {
		return publicationdomain.CodeRateLimited
	}
	return publicationdomain.CodeOK
}

func (service *Service) observeBase(ctx context.Context, run domain.Run, state *publicationdomain.State, nowMillis int64) publicationdomain.ExternalCode {
	baseBranch := strings.TrimPrefix(state.Binding.BaseRef, "refs/heads/")
	observation, err := service.git.ObserveRemoteRef(ctx, gitport.RemoteRefRequest{
		Repository: run.Execution.RepositoryBinding, RepositorySHA256: state.Binding.RepositoryBindingSHA256,
		RemoteName: state.Policy.RemoteName, CanonicalRemote: state.Binding.CanonicalRemote,
		Branch: baseBranch, TaskStoreNowMillis: nowMillis,
	})
	if err != nil {
		observation = publicationdomain.SealRefObservation(publicationdomain.RefObservation{
			ID:   stableID("git-base-unavailable", state.Binding.BindingSHA256, strconv.FormatInt(nowMillis, 10)),
			Code: publicationdomain.CodeUnavailable, Ref: state.Binding.BaseRef,
			RemoteCanonical: state.Binding.CanonicalRemote, ObservedAtMillis: nowMillis, MaximumAgeMillis: state.Policy.ObservationAgeMS,
		})
	}
	state.BaseObservation = &observation
	if !publicationdomain.CurrentNamedRefObservation(observation, state.Binding.CanonicalRemote, baseBranch, nowMillis) {
		return publicationdomain.CodeResponseUnknown
	}
	if observation.Code != publicationdomain.CodeOK {
		return observation.Code
	}
	if !observation.Exists || observation.OID != state.Binding.BaseSHA {
		return publicationdomain.CodeBaseChanged
	}
	return publicationdomain.CodeOK
}

func (service *Service) observeRef(ctx context.Context, run domain.Run, state publicationdomain.State, kind string, nowMillis int64) (Result, error) {
	repository := service.observeRepository(ctx, state, nowMillis)
	state.RepositoryObservation = &repository
	if code := preflightCode(repository, state, nowMillis); code != publicationdomain.CodeOK {
		return service.persistFailure(ctx, run, state, kind, code)
	}
	if code := service.observeBase(ctx, run, &state, nowMillis); code != publicationdomain.CodeOK {
		return service.persistFailure(ctx, run, state, kind, code)
	}
	observation, err := service.git.ObserveRemoteRef(ctx, gitport.RemoteRefRequest{
		Repository: run.Execution.RepositoryBinding, RepositorySHA256: state.Binding.RepositoryBindingSHA256,
		RemoteName: state.Policy.RemoteName, CanonicalRemote: state.Binding.CanonicalRemote,
		Branch: state.Binding.Branch, TaskStoreNowMillis: nowMillis,
	})
	if err != nil {
		observation = publicationdomain.SealRefObservation(publicationdomain.RefObservation{
			ID:   stableID("git-ref-unavailable", state.Binding.BindingSHA256, strconv.FormatInt(nowMillis, 10)),
			Code: publicationdomain.CodeUnavailable, Ref: "refs/heads/" + state.Binding.Branch,
			RemoteCanonical: state.Binding.CanonicalRemote, ObservedAtMillis: nowMillis, MaximumAgeMillis: state.Policy.ObservationAgeMS,
		})
	}
	state.RefObservation = &observation
	effect := effectPointer(&state, kind)
	if !publicationdomain.CurrentRefObservation(observation, state.Binding, nowMillis) {
		return service.persistFailure(ctx, run, state, kind, publicationdomain.CodeResponseUnknown)
	}
	if observation.Code != publicationdomain.CodeOK {
		return service.persistFailure(ctx, run, state, kind, observation.Code)
	}
	if effect.Phase != publicationdomain.EffectDispatching {
		effect.Phase = publicationdomain.EffectObserved
	}
	effect.Code = publicationdomain.CodeOK
	next := run
	next.Execution.Publication = &state
	next, err = service.persist(ctx, run, next, "publication."+kind+".observed")
	return Result{Run: next, Progressed: err == nil}, err
}

func (service *Service) scan(ctx context.Context, state publicationdomain.State, nowMillis int64) (publicationdomain.Scan, publicationdomain.ExternalCode) {
	all := make([]publicationdomain.PullRequest, 0)
	for page := uint32(1); page <= state.Policy.MaximumPages; page++ {
		observation, err := service.github.ListPullRequests(ctx, githubport.ListRequest{
			Owner: state.Binding.RepositoryOwner, Name: state.Binding.RepositoryName,
			HeadOwner: state.Binding.HeadOwner, HeadRef: state.Binding.Branch,
			Page: page, PageSize: state.Policy.PageSize, TaskStoreNowMillis: nowMillis,
			Marker: publicationdomain.Marker(state.Binding),
		})
		if err != nil {
			return publicationdomain.Scan{}, publicationdomain.CodeUnavailable
		}
		if !publicationdomain.CurrentPullRequestPage(observation, page, nowMillis) {
			return publicationdomain.Scan{}, publicationdomain.CodeResponseUnknown
		}
		if observation.Code != publicationdomain.CodeOK {
			return publicationdomain.Scan{}, observation.Code
		}
		all = append(all, observation.PullRequests...)
		if observation.Complete {
			scan := publicationdomain.SealScan(publicationdomain.Scan{ID: stableID("github-pr-scan", state.Binding.BindingSHA256,
				strconv.FormatInt(nowMillis, 10)), Pages: page, PullRequests: all, ObservedAtMillis: nowMillis})
			if !publicationdomain.ValidScan(scan) {
				return publicationdomain.Scan{}, publicationdomain.CodePullRequestDuplicate
			}
			return scan, publicationdomain.CodeOK
		}
		if observation.NextPage != page+1 {
			return publicationdomain.Scan{}, publicationdomain.CodePaginationIncomplete
		}
	}
	return publicationdomain.Scan{}, publicationdomain.CodePaginationIncomplete
}

func (service *Service) observePullRequests(ctx context.Context, run domain.Run, state publicationdomain.State, kind string, nowMillis int64) (Result, error) {
	repository := service.observeRepository(ctx, state, nowMillis)
	state.RepositoryObservation = &repository
	if code := preflightCode(repository, state, nowMillis); code != publicationdomain.CodeOK {
		return service.persistFailure(ctx, run, state, kind, code)
	}
	if code := service.observeBase(ctx, run, &state, nowMillis); code != publicationdomain.CodeOK {
		return service.persistFailure(ctx, run, state, kind, code)
	}
	scan, code := service.scan(ctx, state, nowMillis)
	if code != publicationdomain.CodeOK {
		return service.persistFailure(ctx, run, state, kind, code)
	}
	state.Scan = &scan
	effect := effectPointer(&state, kind)
	if effect.Phase != publicationdomain.EffectDispatching {
		effect.Phase = publicationdomain.EffectObserved
	}
	effect.Code = publicationdomain.CodeOK
	next := run
	next.Execution.Publication = &state
	next, err := service.persist(ctx, run, next, "publication."+kind+".observed")
	return Result{Run: next, Progressed: err == nil}, err
}

func exactPullRequest(state publicationdomain.State) (*publicationdomain.PullRequest, publicationdomain.ExternalCode) {
	if state.Scan == nil || !publicationdomain.ValidScan(*state.Scan) {
		return nil, publicationdomain.CodeResponseUnknown
	}
	markerHash := publicationdomain.DigestText(publicationdomain.Marker(state.Binding))
	owned := make([]publicationdomain.PullRequest, 0, 1)
	for _, pull := range state.Scan.PullRequests {
		sameHead := pull.HeadRef == state.Binding.Branch && strings.EqualFold(pull.HeadOwner, state.Binding.HeadOwner) &&
			pull.HeadRepositoryID == state.Binding.GitHubRepositoryID
		if pull.State != "open" {
			if state.OwnedPullRequest != nil && pull.Number == state.OwnedPullRequest.Number {
				return nil, publicationdomain.CodePullRequestClosed
			}
			continue
		}
		if !sameHead {
			continue
		}
		if pull.BaseRef != strings.TrimPrefix(state.Binding.BaseRef, "refs/heads/") || pull.BaseRepositoryID != state.Binding.GitHubRepositoryID {
			return nil, publicationdomain.CodePullRequestUnowned
		}
		if pull.MarkerCount != 1 || pull.MarkerSHA256 != markerHash {
			return nil, publicationdomain.CodePullRequestUnowned
		}
		if !pull.CreatedByViewer || !strings.EqualFold(pull.AuthorLogin, state.Binding.HeadOwner) {
			return nil, publicationdomain.CodeMarkerForged
		}
		owned = append(owned, pull)
	}
	if len(owned) > 1 {
		return nil, publicationdomain.CodePullRequestDuplicate
	}
	if state.OwnedPullRequest != nil {
		if len(owned) == 0 {
			return nil, publicationdomain.CodeResponseUnknown
		}
		if owned[0].Number != state.OwnedPullRequest.Number || owned[0].NodeID != state.OwnedPullRequest.NodeID {
			return nil, publicationdomain.CodePullRequestDuplicate
		}
	}
	if len(owned) == 0 {
		return nil, publicationdomain.CodeOK
	}
	result := owned[0]
	return &result, publicationdomain.CodeOK
}

func updateOwned(state *publicationdomain.State, pull publicationdomain.PullRequest) {
	state.OwnedPullRequest = &publicationdomain.OwnedPullRequest{Number: pull.Number, NodeID: pull.NodeID, URL: pull.URL,
		MarkerSHA256: publicationdomain.DigestText(publicationdomain.Marker(state.Binding)), HeadSHA: pull.HeadSHA, Draft: pull.Draft}
}

func dispatchFailure(ctx context.Context, service *Service, run domain.Run, state publicationdomain.State, kind string,
	resultHandoff bool, code publicationdomain.ExternalCode) (Result, error) {
	if resultHandoff {
		return Result{Run: run, Code: publicationdomain.CodeResponseUnknown}, ErrResponseUnknown
	}
	effect := effectPointer(&state, kind)
	if waitingCode(code) {
		effect.Phase = publicationdomain.EffectIntent
	} else {
		effect.Phase = publicationdomain.EffectNeedsYou
	}
	effect.Code = code
	state.RepositoryObservation, state.RefObservation, state.BaseObservation, state.Scan = nil, nil, nil, nil
	next := run
	next.Execution.Publication = &state
	next, err := service.persist(ctx, run, next, "publication."+kind+".failed_pre_dispatch")
	if waitingCode(code) {
		return Result{Run: next, Progressed: err == nil, WaitingExternal: true, Code: code}, errors.Join(ErrExternalUnavailable, err)
	}
	return Result{Run: next, Progressed: err == nil, Code: code}, errors.Join(ErrPublicationRefused, err)
}

func (service *Service) stepPush(ctx context.Context, run domain.Run, state publicationdomain.State, nowMillis int64) (Result, error) {
	effect := &state.Push
	if effect.Phase == publicationdomain.EffectIntent || effect.Phase == publicationdomain.EffectWaiting ||
		effect.Phase == publicationdomain.EffectDispatching && state.RefObservation == nil {
		return service.observeRef(ctx, run, state, "push", nowMillis)
	}
	if effect.Phase == publicationdomain.EffectNeedsYou {
		return Result{Run: run, Code: effect.Code}, ErrPublicationRefused
	}
	if effect.Phase == publicationdomain.EffectComplete {
		return Result{Run: run}, nil
	}
	observation := state.RefObservation
	if observation == nil || !publicationdomain.CurrentRefObservation(*observation, state.Binding, nowMillis) {
		return service.observeRef(ctx, run, state, "push", nowMillis)
	}
	if observation.Exists && observation.OID == state.Binding.CandidateSHA {
		effect.Phase, effect.Code = publicationdomain.EffectComplete, publicationdomain.CodeOK
		state.RefObservation = nil
		next := run
		next.Execution.Publication = &state
		next, err := service.persist(ctx, run, next, "publication.push.complete")
		return Result{Run: next, Progressed: err == nil}, err
	}
	current := ""
	if observation.Exists {
		current = observation.OID
	}
	if current != state.ExpectedRemoteHead {
		return service.persistFailure(ctx, run, state, "push", publicationdomain.CodeRefChanged)
	}
	if effect.Phase == publicationdomain.EffectDispatching {
		return service.persistFailure(ctx, run, state, "push", publicationdomain.CodeResponseUnknown)
	}
	if effect.Attempts >= publicationdomain.MaximumEffectAttempts {
		return service.persistFailure(ctx, run, state, "push", publicationdomain.CodeResponseUnknown)
	}
	effect.Phase = publicationdomain.EffectDispatching
	effect.Attempts++
	state.RepositoryObservation, state.RefObservation, state.BaseObservation = nil, nil, nil
	dispatching := run
	dispatching.Execution.Publication = &state
	dispatching, err := service.persist(ctx, run, dispatching, "publication.push.dispatching")
	if err != nil {
		return Result{Run: run}, err
	}
	result, callErr := service.git.PushExact(ctx, gitport.PushRequest{RemoteRefRequest: gitport.RemoteRefRequest{
		Repository: dispatching.Execution.RepositoryBinding, RepositorySHA256: state.Binding.RepositoryBindingSHA256,
		RemoteName: state.Policy.RemoteName, CanonicalRemote: state.Binding.CanonicalRemote,
		Branch: state.Binding.Branch, TaskStoreNowMillis: nowMillis,
	}, CandidateSHA: state.Binding.CandidateSHA, ExpectedRemoteOID: state.ExpectedRemoteHead,
		ProtectedBranches: append(slices.Clone(state.Policy.ProtectedBranches), strings.TrimPrefix(state.Binding.BaseRef, "refs/heads/"))})
	if callErr != nil {
		return dispatchFailure(ctx, service, dispatching, state, "push", true, publicationdomain.CodeResponseUnknown)
	}
	if !result.Handoff {
		return dispatchFailure(ctx, service, dispatching, state, "push", false, result.Code)
	}
	return Result{Run: dispatching, Progressed: true, Code: result.Code}, ErrResponseUnknown
}

func (service *Service) beginPullDispatch(ctx context.Context, run domain.Run, state publicationdomain.State, kind, transition string) (domain.Run, publicationdomain.State, error) {
	effect := effectPointer(&state, kind)
	if effect.Attempts >= publicationdomain.MaximumEffectAttempts {
		return run, state, ErrPublicationRefused
	}
	effect.Phase = publicationdomain.EffectDispatching
	effect.Attempts++
	state.RepositoryObservation, state.BaseObservation, state.Scan = nil, nil, nil
	next := run
	next.Execution.Publication = &state
	next, err := service.persist(ctx, run, next, transition)
	return next, state, err
}

func (service *Service) stepQuiesce(ctx context.Context, run domain.Run, state publicationdomain.State, nowMillis int64) (Result, error) {
	effect := &state.Quiesce
	if effect.Phase == publicationdomain.EffectComplete {
		return Result{Run: run}, nil
	}
	if effect.Phase == publicationdomain.EffectIntent || effect.Phase == publicationdomain.EffectWaiting ||
		effect.Phase == publicationdomain.EffectDispatching && state.Scan == nil {
		return service.observePullRequests(ctx, run, state, "quiesce", nowMillis)
	}
	if effect.Phase == publicationdomain.EffectNeedsYou {
		return Result{Run: run, Code: effect.Code}, ErrPublicationRefused
	}
	pull, code := exactPullRequest(state)
	if code != publicationdomain.CodeOK || pull == nil {
		return service.persistFailure(ctx, run, state, "quiesce", code)
	}
	if pull.HeadSHA != state.ExpectedRemoteHead {
		return service.persistFailure(ctx, run, state, "quiesce", publicationdomain.CodeHeadChanged)
	}
	updateOwned(&state, *pull)
	if pull.Draft {
		state.Quiesce.Phase = publicationdomain.EffectComplete
		next := run
		next.Execution.Publication = &state
		next, err := service.persist(ctx, run, next, "publication.quiesce.complete")
		return Result{Run: next, Progressed: err == nil}, err
	}
	if effect.Attempts >= publicationdomain.MaximumEffectAttempts {
		return service.persistFailure(ctx, run, state, "quiesce", publicationdomain.CodeResponseUnknown)
	}
	dispatching, nextState, err := service.beginPullDispatch(ctx, run, state, "quiesce", "publication.quiesce.dispatching")
	if err != nil {
		return Result{Run: run}, err
	}
	result, callErr := service.github.SetPullRequestDraft(ctx, githubport.PullRequestRequest{Owner: state.Binding.RepositoryOwner,
		Name: state.Binding.RepositoryName, Number: pull.Number, ExpectedHeadSHA: pull.HeadSHA,
		ExpectedBaseRef: pull.BaseRef, ExpectedUpdatedAt: pull.UpdatedAtMillis, Draft: true, TaskStoreNowMillis: nowMillis})
	if callErr != nil {
		return dispatchFailure(ctx, service, dispatching, nextState, "quiesce", true, publicationdomain.CodeResponseUnknown)
	}
	if !result.Handoff {
		return dispatchFailure(ctx, service, dispatching, nextState, "quiesce", false, result.Code)
	}
	return Result{Run: dispatching, Progressed: true, Code: result.Code}, ErrResponseUnknown
}

func (service *Service) stepPullRequest(ctx context.Context, run domain.Run, state publicationdomain.State, nowMillis int64) (Result, error) {
	effect := &state.PullRequest
	if effect.Phase == publicationdomain.EffectComplete {
		return Result{Run: run}, nil
	}
	if effect.Phase == publicationdomain.EffectIntent || effect.Phase == publicationdomain.EffectWaiting ||
		effect.Phase == publicationdomain.EffectDispatching && state.Scan == nil {
		return service.observePullRequests(ctx, run, state, "pull_request", nowMillis)
	}
	if effect.Phase == publicationdomain.EffectNeedsYou {
		return Result{Run: run, Code: effect.Code}, ErrPublicationRefused
	}
	pull, code := exactPullRequest(state)
	if code != publicationdomain.CodeOK {
		return service.persistFailure(ctx, run, state, "pull_request", code)
	}
	if pull != nil {
		if pull.HeadSHA != state.Binding.CandidateSHA {
			return service.persistFailure(ctx, run, state, "pull_request", publicationdomain.CodeHeadChanged)
		}
		updateOwned(&state, *pull)
		state.PullRequest.Phase = publicationdomain.EffectComplete
		next := run
		next.Execution.Publication = &state
		next, err := service.persist(ctx, run, next, "publication.pull_request.complete")
		return Result{Run: next, Progressed: err == nil}, err
	}
	if effect.Phase == publicationdomain.EffectDispatching {
		return service.persistFailure(ctx, run, state, "pull_request", publicationdomain.CodeResponseUnknown)
	}
	if effect.Attempts >= publicationdomain.MaximumEffectAttempts {
		return service.persistFailure(ctx, run, state, "pull_request", publicationdomain.CodeResponseUnknown)
	}
	draft := state.Policy.Timing == publicationdomain.TimingPublishBeforePR
	dispatching, nextState, err := service.beginPullDispatch(ctx, run, state, "pull_request", "publication.pull_request.dispatching")
	if err != nil {
		return Result{Run: run}, err
	}
	result, callErr := service.github.CreatePullRequest(ctx, githubport.CreateRequest{Owner: state.Binding.RepositoryOwner,
		Name: state.Binding.RepositoryName, HeadOwner: state.Binding.HeadOwner, HeadRef: state.Binding.Branch,
		BaseRef: strings.TrimPrefix(state.Binding.BaseRef, "refs/heads/"), ExpectedHeadSHA: state.Binding.CandidateSHA,
		Title: state.Template.Title, Body: state.Template.Body, Draft: draft, TaskStoreNowMillis: nowMillis})
	if callErr != nil {
		return dispatchFailure(ctx, service, dispatching, nextState, "pull_request", true, publicationdomain.CodeResponseUnknown)
	}
	if !result.Handoff {
		return dispatchFailure(ctx, service, dispatching, nextState, "pull_request", false, result.Code)
	}
	return Result{Run: dispatching, Progressed: true, Code: result.Code}, ErrResponseUnknown
}

func (service *Service) stepMetadata(ctx context.Context, run domain.Run, state publicationdomain.State, nowMillis int64) (Result, error) {
	effect := &state.Metadata
	if effect.Phase == publicationdomain.EffectComplete {
		return Result{Run: run}, nil
	}
	if effect.Phase == publicationdomain.EffectIntent || effect.Phase == publicationdomain.EffectWaiting ||
		effect.Phase == publicationdomain.EffectDispatching && state.Scan == nil {
		return service.observePullRequests(ctx, run, state, "metadata", nowMillis)
	}
	if effect.Phase == publicationdomain.EffectNeedsYou {
		return Result{Run: run, Code: effect.Code}, ErrPublicationRefused
	}
	pull, code := exactPullRequest(state)
	if code != publicationdomain.CodeOK || pull == nil {
		return service.persistFailure(ctx, run, state, "metadata", code)
	}
	if pull.HeadSHA != state.Binding.CandidateSHA {
		return service.persistFailure(ctx, run, state, "metadata", publicationdomain.CodeHeadChanged)
	}
	updateOwned(&state, *pull)
	if pull.TitleSHA256 == publicationdomain.DigestText(state.Template.Title) && pull.BodySHA256 == publicationdomain.DigestText(state.Template.Body) {
		state.Metadata.Phase = publicationdomain.EffectComplete
		next := run
		next.Execution.Publication = &state
		next, err := service.persist(ctx, run, next, "publication.metadata.complete")
		return Result{Run: next, Progressed: err == nil}, err
	}
	if effect.Phase == publicationdomain.EffectDispatching {
		return service.persistFailure(ctx, run, state, "metadata", publicationdomain.CodeResponseUnknown)
	}
	if effect.Attempts >= publicationdomain.MaximumEffectAttempts {
		return service.persistFailure(ctx, run, state, "metadata", publicationdomain.CodeResponseUnknown)
	}
	dispatching, nextState, err := service.beginPullDispatch(ctx, run, state, "metadata", "publication.metadata.dispatching")
	if err != nil {
		return Result{Run: run}, err
	}
	result, callErr := service.github.UpdatePullRequest(ctx, githubport.PullRequestRequest{Owner: state.Binding.RepositoryOwner,
		Name: state.Binding.RepositoryName, Number: pull.Number, ExpectedHeadSHA: pull.HeadSHA,
		ExpectedBaseRef: pull.BaseRef, ExpectedUpdatedAt: pull.UpdatedAtMillis,
		Title: state.Template.Title, Body: state.Template.Body, Draft: pull.Draft, TaskStoreNowMillis: nowMillis})
	if callErr != nil {
		return dispatchFailure(ctx, service, dispatching, nextState, "metadata", true, publicationdomain.CodeResponseUnknown)
	}
	if !result.Handoff {
		return dispatchFailure(ctx, service, dispatching, nextState, "metadata", false, result.Code)
	}
	return Result{Run: dispatching, Progressed: true, Code: result.Code}, ErrResponseUnknown
}

func (service *Service) stepReady(ctx context.Context, run domain.Run, state publicationdomain.State, nowMillis int64) (Result, error) {
	effect := &state.Ready
	if effect.Phase == publicationdomain.EffectComplete {
		return Result{Run: run}, nil
	}
	if effect.Phase == publicationdomain.EffectIntent || effect.Phase == publicationdomain.EffectWaiting ||
		effect.Phase == publicationdomain.EffectDispatching && state.Scan == nil {
		return service.observePullRequests(ctx, run, state, "ready", nowMillis)
	}
	if effect.Phase == publicationdomain.EffectNeedsYou {
		return Result{Run: run, Code: effect.Code}, ErrPublicationRefused
	}
	pull, code := exactPullRequest(state)
	if code != publicationdomain.CodeOK || pull == nil {
		return service.persistFailure(ctx, run, state, "ready", code)
	}
	if pull.HeadSHA != state.Binding.CandidateSHA {
		return service.persistFailure(ctx, run, state, "ready", publicationdomain.CodeHeadChanged)
	}
	updateOwned(&state, *pull)
	if !pull.Draft {
		state.Ready.Phase = publicationdomain.EffectComplete
		next := run
		next.Execution.Publication = &state
		next, err := service.persist(ctx, run, next, "publication.ready.complete")
		return Result{Run: next, Progressed: err == nil}, err
	}
	if effect.Phase == publicationdomain.EffectDispatching {
		return service.persistFailure(ctx, run, state, "ready", publicationdomain.CodeResponseUnknown)
	}
	if effect.Attempts >= publicationdomain.MaximumEffectAttempts {
		return service.persistFailure(ctx, run, state, "ready", publicationdomain.CodeResponseUnknown)
	}
	dispatching, nextState, err := service.beginPullDispatch(ctx, run, state, "ready", "publication.ready.dispatching")
	if err != nil {
		return Result{Run: run}, err
	}
	result, callErr := service.github.SetPullRequestDraft(ctx, githubport.PullRequestRequest{Owner: state.Binding.RepositoryOwner,
		Name: state.Binding.RepositoryName, Number: pull.Number, ExpectedHeadSHA: pull.HeadSHA,
		ExpectedBaseRef: pull.BaseRef, ExpectedUpdatedAt: pull.UpdatedAtMillis, Draft: false, TaskStoreNowMillis: nowMillis})
	if callErr != nil {
		return dispatchFailure(ctx, service, dispatching, nextState, "ready", true, publicationdomain.CodeResponseUnknown)
	}
	if !result.Handoff {
		return dispatchFailure(ctx, service, dispatching, nextState, "ready", false, result.Code)
	}
	return Result{Run: dispatching, Progressed: true, Code: result.Code}, ErrResponseUnknown
}

func (service *Service) persistEvidence(ctx context.Context, run domain.Run, state publicationdomain.State, ready bool, nowMillis int64) (Result, error) {
	if state.Evidence != nil && publicationdomain.ValidEvidence(*state.Evidence, state) && state.Evidence.Ready == ready {
		return Result{Run: run, Published: true, Draft: state.Evidence.Draft}, nil
	}
	evidence := publicationdomain.EvidenceFor(state, ready, nowMillis)
	if evidence == nil {
		return Result{Run: run}, ErrInvalidCommand
	}
	state.Evidence = evidence
	next := run
	next.Execution.Publication = &state
	authority := *next.Execution.CandidateAuthority
	authority.Downstream.Publication = &candidate.EvidenceBinding{ID: evidence.ID, CandidateID: authority.CandidateID,
		CandidateSHA: authority.CandidateSHA, BaseSHA: authority.BaseSHA, Generation: authority.Generation,
		BindingSHA256: authority.BindingSHA256}
	next.Execution.CandidateAuthority = &authority
	next, err := service.persist(ctx, run, next, "publication.evidence_observed")
	return Result{Run: next, Progressed: err == nil, Published: err == nil, Draft: !ready}, err
}

// Reconcile advances at most one durable transition or one external handoff.
// A response from a mutation is deliberately returned as unknown until the
// next call re-observes the authoritative remote state.
func (service *Service) Reconcile(ctx context.Context, runID string, nowMillis int64) (Result, error) {
	unlock := lockPublicationRun(runID)
	defer unlock()
	return service.reconcile(ctx, runID, nowMillis)
}

func (service *Service) reconcile(ctx context.Context, runID string, nowMillis int64) (Result, error) {
	_, task, run, record, state, err := service.load(ctx, runID, nowMillis)
	if err != nil {
		return Result{Run: run}, err
	}
	template, approved, ok := desiredTemplate(task, run, record, state)
	if !ok {
		return Result{Run: run}, ErrInvalidCommand
	}
	if template.SHA256 != state.Template.SHA256 {
		state.Template = template
		if state.Metadata.Phase == publicationdomain.EffectComplete {
			state.Metadata.Phase = publicationdomain.EffectIntent
			state.Ready.Phase = publicationdomain.EffectIntent
		}
		state.Evidence = nil
		authority := *run.Execution.CandidateAuthority
		authority.Downstream.Publication = nil
		next := run
		next.Execution.Publication, next.Execution.CandidateAuthority = &state, &authority
		next, persistErr := service.persist(ctx, run, next, "publication.template_refreshed")
		return Result{Run: next, Progressed: persistErr == nil}, persistErr
	}
	if state.Quiesce.Phase != publicationdomain.EffectComplete {
		return service.stepQuiesce(ctx, run, state, nowMillis)
	}
	if state.Policy.Timing == publicationdomain.TimingReviewBeforePR && !approved {
		return Result{Run: run, WaitingForReview: true, MergeAuthorized: false}, ErrReviewRequired
	}
	if state.Push.Phase != publicationdomain.EffectComplete {
		return service.stepPush(ctx, run, state, nowMillis)
	}
	if state.PullRequest.Phase != publicationdomain.EffectComplete {
		return service.stepPullRequest(ctx, run, state, nowMillis)
	}
	if state.Metadata.Phase != publicationdomain.EffectComplete {
		return service.stepMetadata(ctx, run, state, nowMillis)
	}
	if state.Policy.Timing == publicationdomain.TimingPublishBeforePR && !approved {
		if state.OwnedPullRequest == nil || !state.OwnedPullRequest.Draft {
			return service.persistFailure(ctx, run, state, "ready", publicationdomain.CodeResponseUnknown)
		}
		if state.Evidence == nil || !state.Evidence.Draft {
			return service.persistEvidence(ctx, run, state, false, nowMillis)
		}
		return Result{Run: run, WaitingForReview: true, Published: true, Draft: true, MergeAuthorized: false}, ErrReviewRequired
	}
	if state.Ready.Phase != publicationdomain.EffectComplete {
		return service.stepReady(ctx, run, state, nowMillis)
	}
	return service.persistEvidence(ctx, run, state, true, nowMillis)
}
