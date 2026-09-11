// SPDX-License-Identifier: Apache-2.0

// Package feedback ingests authenticated Paseo and read-only GitHub feedback,
// persists immutable audit revisions, and delegates current-work correction to
// the existing bounded M4.4 service. It never writes to GitHub.
package feedback

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/candidate"
	domaincorrection "github.com/mcuadros/director-engine/domain/correction"
	directdomain "github.com/mcuadros/director-engine/domain/directdelivery"
	"github.com/mcuadros/director-engine/domain/execution"
	domainfeedback "github.com/mcuadros/director-engine/domain/feedback"
	integrationdomain "github.com/mcuadros/director-engine/domain/integration"
	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
	domainreview "github.com/mcuadros/director-engine/domain/review"
	"github.com/mcuadros/director-engine/domain/runtimebudget"
	domainvalidation "github.com/mcuadros/director-engine/domain/validation"
	correctionport "github.com/mcuadros/director-engine/ports/correction"
	gitport "github.com/mcuadros/director-engine/ports/git"
	githubport "github.com/mcuadros/director-engine/ports/github"
)

var (
	ErrInvalidCommand       = errors.New("feedback command is invalid")
	ErrConcurrentTransition = errors.New("feedback transition lost an expected-version race")
	ErrFeedbackUnavailable  = errors.New("feedback source is unavailable")
	ErrFeedbackAmbiguous    = errors.New("feedback facts are ambiguous")
)

type Store interface {
	Project(context.Context, string) (domain.Project, error)
	Task(context.Context, string) (domain.Task, error)
	Run(context.Context, string) (domain.Run, error)
	Candidate(context.Context, string) (domain.Candidate, error)
	UpdateRun(context.Context, domain.CommandRequest, domain.Run, domain.Event) (domain.CommandResult, error)
	CreateTask(context.Context, domain.CommandRequest, domain.Task, domain.Event) (domain.CommandResult, error)
}

type Service struct {
	store      Store
	github     githubport.FeedbackObservationPort
	git        gitport.BranchPort
	correction correctionport.Router
}

func NewService(store Store, github githubport.FeedbackObservationPort, correction correctionport.Router, branches ...gitport.BranchPort) (*Service, error) {
	if store == nil || correction == nil {
		return nil, errors.New("feedback store and correction router are required")
	}
	var git gitport.BranchPort
	if len(branches) > 1 {
		return nil, errors.New("feedback accepts at most one Git branch observer")
	}
	if len(branches) == 1 {
		git = branches[0]
	}
	return &Service{store: store, github: github, git: git, correction: correction}, nil
}

type RoutingContext struct {
	RunID                 string
	ExpectedTaskVersion   uint64
	ExpectedRunVersion    uint64
	LeaseEpoch            uint64
	FrozenPlanDigest      string
	CurrentPlanDigest     string
	FrozenSkillSetDigest  string
	CurrentSkillSetDigest string
	CurrentDecisionDigest string
	CurrentDiffDigest     string
	CorrectionPolicy      domaincorrection.Policy
	NowMillis             int64
}

type DirectCommand struct {
	RoutingContext
	Item domainfeedback.Item
}

type GitHubCommand struct{ RoutingContext }

type DecisionCommand struct {
	RoutingContext
	DecisionID          string
	ExpectedStateSHA256 string
	Actor               domainfeedback.Actor
	AllowCorrection     bool
}

type Result struct {
	Run                domain.Run
	Feedback           *domainfeedback.State
	FollowUpTask       *domain.Task
	FollowUpTasks      []domain.Task
	Progressed         bool
	Replayed           bool
	CorrectionReady    bool
	CorrectionRouted   bool
	WaitingExternal    bool
	NeedsHumanDecision bool
	Code               string
}

type loaded struct {
	project   domain.Project
	task      domain.Task
	run       domain.Run
	candidate domain.Candidate
	binding   domainfeedback.Binding
}

func stableID(prefix string, values ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(values, "\x1f")))
	return prefix + "-" + hex.EncodeToString(sum[:16])
}

func payload(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal fixed feedback event: " + err.Error())
	}
	return encoded
}

func validRouting(command RoutingContext) bool {
	return command.RunID != "" && command.LeaseEpoch > 0 && command.NowMillis >= 0 &&
		command.FrozenPlanDigest != "" && command.FrozenPlanDigest == command.CurrentPlanDigest &&
		command.FrozenSkillSetDigest != "" && command.FrozenSkillSetDigest == command.CurrentSkillSetDigest &&
		command.ExpectedTaskVersion > 0 && command.CurrentDecisionDigest != "" && command.CurrentDiffDigest != "" &&
		command.CorrectionPolicy.AttemptLimit == domaincorrection.AttemptLimit
}

func leaseCurrent(project domain.Project, run domain.Run, epoch uint64, nowMillis int64) bool {
	lease := project.Lease
	binding := run.Execution.LeaseBinding
	return projectLeaseCurrent(project, epoch, nowMillis) && project.ID == run.Execution.Scope.ProjectID &&
		execution.ValidLeaseBinding(binding) &&
		lease.Epoch == epoch && binding.Epoch == epoch && lease.HolderInstance == binding.HolderInstance &&
		lease.HolderProcessIdentity == binding.HolderProcessIdentity && nowMillis >= lease.AcquiredAtMillis && nowMillis < lease.ExpiresAtMillis
}

func projectLeaseCurrent(project domain.Project, epoch uint64, nowMillis int64) bool {
	lease := project.Lease
	return lease != nil && project.State == "active" && domain.ValidateProjectLease(lease) == nil && lease.DispatchAllowed &&
		lease.Epoch == epoch && nowMillis >= lease.AcquiredAtMillis && nowMillis < lease.ExpiresAtMillis
}

func matchingPublication(run domain.Run, record domain.Candidate) *publicationdomain.State {
	matches := func(state publicationdomain.State) bool {
		return publicationdomain.ValidState(state) && state.Binding.TaskID == run.TaskID && state.Binding.RunID == run.ID &&
			state.Binding.CandidateID == record.ID && state.Binding.CandidateSHA == record.CommitSHA &&
			state.Binding.BaseSHA == record.Manifest.BaseSHA && state.Binding.ManifestSHA256 == record.Manifest.BindingSHA256 &&
			state.OwnedPullRequest != nil
	}
	if run.Execution.Publication != nil && matches(*run.Execution.Publication) {
		state := *run.Execution.Publication
		return &state
	}
	for index := len(run.Execution.PublicationHistory) - 1; index >= 0; index-- {
		if matches(run.Execution.PublicationHistory[index]) {
			state := run.Execution.PublicationHistory[index]
			return &state
		}
	}
	return nil
}

func feedbackBinding(task domain.Task, run domain.Run, record domain.Candidate) (domainfeedback.Binding, bool) {
	authority := run.Execution.CandidateAuthority
	if authority == nil || authority.Invalidated || !candidate.ValidAuthority(*authority) || run.CurrentCandidateID != record.ID ||
		record.RunID != run.ID || authority.CandidateID != record.ID || authority.CandidateSHA != record.CommitSHA ||
		authority.BaseSHA != record.Manifest.BaseSHA || authority.BindingSHA256 != record.Manifest.BindingSHA256 || authority.TaskVersion != task.Version {
		return domainfeedback.Binding{}, false
	}
	binding := domainfeedback.Binding{ProjectID: task.ProjectID, WorkspaceID: task.WorkspaceIDs[0], TaskID: task.ID,
		TaskVersion: task.Version, RunID: run.ID, CandidateID: record.ID, CandidateSHA: record.CommitSHA,
		BaseSHA: authority.BaseSHA, CandidateGeneration: authority.Generation, ManifestSHA256: authority.BindingSHA256}
	if publication := matchingPublication(run, record); publication != nil {
		binding.RepositoryID = publication.Binding.GitHubRepositoryID
		binding.RepositoryNodeID = publication.Binding.GitHubRepositoryNodeID
		binding.PullRequestNumber = publication.OwnedPullRequest.Number
	}
	sealed := domainfeedback.SealBinding(binding)
	return sealed, domainfeedback.ValidBinding(sealed)
}

func (service *Service) load(ctx context.Context, command RoutingContext) (loaded, error) {
	if !validRouting(command) {
		return loaded{}, ErrInvalidCommand
	}
	run, err := service.store.Run(ctx, command.RunID)
	if err != nil {
		return loaded{}, err
	}
	if run.Version != command.ExpectedRunVersion {
		return loaded{run: run}, ErrConcurrentTransition
	}
	task, err := service.store.Task(ctx, run.TaskID)
	if err != nil {
		return loaded{run: run}, err
	}
	project, err := service.store.Project(ctx, task.ProjectID)
	if err != nil {
		return loaded{task: task, run: run}, err
	}
	terminalFeedback := task.Complete || run.Execution.CandidateAuthority != nil && run.Execution.CandidateAuthority.Downstream.Integration != nil
	leaseValid := leaseCurrent(project, run, command.LeaseEpoch, command.NowMillis)
	if terminalFeedback {
		leaseValid = projectLeaseCurrent(project, command.LeaseEpoch, command.NowMillis)
	}
	if task.Version != command.ExpectedTaskVersion || !leaseValid || project.ID != run.Execution.Scope.ProjectID ||
		run.Execution.Scope.TaskID != task.ID || run.Execution.Scope.RunID != run.ID || len(task.WorkspaceIDs) != 1 ||
		run.Execution.Scope.WorkspaceID != task.WorkspaceIDs[0] ||
		command.CurrentDecisionDigest != run.Execution.DecisionContextSHA256 {
		return loaded{project: project, task: task, run: run}, ErrConcurrentTransition
	}
	if run.CurrentCandidateID == "" {
		return loaded{project: project, task: task, run: run}, ErrFeedbackAmbiguous
	}
	record, err := service.store.Candidate(ctx, run.CurrentCandidateID)
	if err != nil {
		return loaded{project: project, task: task, run: run}, err
	}
	binding, ok := feedbackBinding(task, run, record)
	if !ok {
		return loaded{project: project, task: task, run: run, candidate: record}, ErrFeedbackAmbiguous
	}
	return loaded{project: project, task: task, run: run, candidate: record, binding: binding}, nil
}

func directSnapshot(item domainfeedback.Item, binding domainfeedback.Binding, nowMillis int64) domainfeedback.Snapshot {
	identity := stableID("paseo-feedback", item.ExternalID, item.RevisionID, binding.BindingSHA256, fmt.Sprintf("%d", nowMillis))
	return domainfeedback.SealSnapshot(domainfeedback.Snapshot{ID: identity, Source: domainfeedback.SourcePaseoDirect,
		BindingSHA256: binding.BindingSHA256, Complete: false, PageCount: 1, ObservedAtMillis: nowMillis,
		MaximumAgeMillis: domainfeedback.MaximumObservationAge, Items: []domainfeedback.Item{item}})
}

func (service *Service) IngestDirect(ctx context.Context, command DirectCommand) (Result, error) {
	current, err := service.load(ctx, command.RoutingContext)
	if err != nil {
		return Result{Run: current.run}, err
	}
	if command.Item.Source != domainfeedback.SourcePaseoDirect || command.Item.Actor.Kind != domainfeedback.ActorHuman ||
		command.Item.Actor.Attestation != domainfeedback.AttestationPaseoHuman || command.Item.CandidateSHA != current.binding.CandidateSHA ||
		command.Item.BaseSHA != current.binding.BaseSHA {
		return Result{Run: current.run}, ErrInvalidCommand
	}
	return service.ingest(ctx, current, command.RoutingContext, []domainfeedback.Snapshot{directSnapshot(command.Item, current.binding, command.NowMillis)})
}

func sourceEndpointOrder() []domainfeedback.Source {
	return []domainfeedback.Source{domainfeedback.SourceGitHubReview, domainfeedback.SourceGitHubReviewComment, domainfeedback.SourceGitHubIssueComment}
}

func (service *Service) githubContextCurrent(ctx context.Context, current loaded, publication *publicationdomain.State, terminal bool, nowMillis int64) error {
	if service.github == nil || service.git == nil || publication == nil || publication.OwnedPullRequest == nil {
		return ErrFeedbackAmbiguous
	}
	binding := publication.Binding
	repository, err := service.github.ObserveRepository(ctx, githubport.RepositoryRequest{Owner: binding.RepositoryOwner,
		Name: binding.RepositoryName, RepositoryID: binding.GitHubRepositoryID, RepositoryNodeID: binding.GitHubRepositoryNodeID,
		ExpectedViewer: binding.HeadOwner, TaskStoreNowMillis: nowMillis})
	if err != nil || repository.Code != publicationdomain.CodeOK {
		return ErrFeedbackUnavailable
	}
	if !publicationdomain.CurrentRepositoryObservation(repository, binding, nowMillis) {
		return ErrFeedbackAmbiguous
	}
	marker := publicationdomain.Marker(binding)
	found := 0
	for pageNumber := uint32(1); pageNumber <= publicationdomain.MaximumPullRequestPages; pageNumber++ {
		page, listErr := service.github.ListPullRequests(ctx, githubport.ListRequest{Owner: binding.RepositoryOwner,
			Name: binding.RepositoryName, HeadOwner: binding.HeadOwner, HeadRef: binding.Branch,
			Page: pageNumber, PageSize: publicationdomain.PullRequestPageSize, TaskStoreNowMillis: nowMillis, Marker: marker})
		if listErr != nil || page.Code != publicationdomain.CodeOK {
			return ErrFeedbackUnavailable
		}
		if !publicationdomain.CurrentPullRequestPage(page, pageNumber, nowMillis) {
			return ErrFeedbackAmbiguous
		}
		for _, pull := range page.PullRequests {
			if pull.Number == publication.OwnedPullRequest.Number {
				stateAllowed := pull.State == "open" || terminal && pull.State == "closed"
				if pull.NodeID != publication.OwnedPullRequest.NodeID || !stateAllowed || pull.HeadSHA != binding.CandidateSHA ||
					pull.HeadRef != binding.Branch || pull.BaseRef != strings.TrimPrefix(binding.BaseRef, "refs/heads/") ||
					!strings.EqualFold(pull.HeadOwner, binding.HeadOwner) || pull.HeadRepositoryID != binding.GitHubRepositoryID ||
					pull.BaseRepositoryID != binding.GitHubRepositoryID || !pull.CreatedByViewer ||
					!strings.EqualFold(pull.AuthorLogin, binding.HeadOwner) || pull.MarkerCount != 1 ||
					pull.MarkerSHA256 != publicationdomain.DigestText(marker) {
					return ErrFeedbackAmbiguous
				}
				found++
			}
		}
		if page.Complete {
			break
		}
		if pageNumber == publicationdomain.MaximumPullRequestPages {
			return ErrFeedbackAmbiguous
		}
	}
	if found != 1 {
		return ErrFeedbackAmbiguous
	}
	if terminal {
		// A completed Task cannot be reopened. The exact historical PR/Candidate
		// association is sufficient to create new work; deleted Task refs and a
		// subsequently advanced base are expected and grant no old-Run authority.
		return nil
	}
	observe := func(branch string) (publicationdomain.RefObservation, error) {
		return service.git.ObserveRemoteRef(ctx, gitport.RemoteRefRequest{Repository: current.run.Execution.RepositoryBinding,
			RepositorySHA256: current.run.Execution.RepositoryBindingHash, RemoteName: publication.Policy.RemoteName,
			CanonicalRemote: binding.CanonicalRemote, Branch: branch, TaskStoreNowMillis: nowMillis})
	}
	head, err := observe(binding.Branch)
	if err != nil || head.Code != publicationdomain.CodeOK {
		return ErrFeedbackUnavailable
	}
	baseBranch := strings.TrimPrefix(binding.BaseRef, "refs/heads/")
	base, err := observe(baseBranch)
	if err != nil || base.Code != publicationdomain.CodeOK {
		return ErrFeedbackUnavailable
	}
	if !publicationdomain.CurrentNamedRefObservation(head, binding.CanonicalRemote, binding.Branch, nowMillis) || !head.Exists || head.OID != binding.CandidateSHA ||
		!publicationdomain.CurrentNamedRefObservation(base, binding.CanonicalRemote, baseBranch, nowMillis) || !base.Exists || base.OID != binding.BaseSHA {
		return ErrFeedbackAmbiguous
	}
	return nil
}

func (service *Service) githubSnapshots(ctx context.Context, current loaded, nowMillis int64) ([]domainfeedback.Snapshot, error) {
	if service.github == nil || current.binding.RepositoryID <= 0 || current.binding.RepositoryNodeID == "" || current.binding.PullRequestNumber <= 0 ||
		matchingPublication(current.run, current.candidate) == nil {
		return nil, ErrFeedbackAmbiguous
	}
	publication := matchingPublication(current.run, current.candidate)
	if publication.OwnedPullRequest.HeadSHA != current.binding.CandidateSHA || publication.Binding.BaseSHA != current.binding.BaseSHA {
		return nil, ErrFeedbackAmbiguous
	}
	terminal := current.task.Complete || current.run.Execution.CandidateAuthority.Downstream.Integration != nil
	if err := service.githubContextCurrent(ctx, current, publication, terminal, nowMillis); err != nil {
		return nil, err
	}
	result := make([]domainfeedback.Snapshot, 0, 3)
	for _, source := range sourceEndpointOrder() {
		items := make([]domainfeedback.Item, 0)
		pageHashes := make([]string, 0)
		complete := false
		for pageNumber := uint32(1); pageNumber <= domainfeedback.MaximumPages; pageNumber++ {
			request := githubport.FeedbackRequest{Owner: publication.Binding.RepositoryOwner, Name: publication.Binding.RepositoryName,
				RepositoryID: current.binding.RepositoryID, RepositoryNodeID: current.binding.RepositoryNodeID,
				PullRequestNumber: current.binding.PullRequestNumber, Source: source, Page: pageNumber,
				PageSize: domainfeedback.MaximumPageSize, CandidateSHA: current.binding.CandidateSHA, BaseSHA: current.binding.BaseSHA,
				BindingSHA256: current.binding.BindingSHA256, TaskStoreNowMillis: nowMillis}
			page, err := service.github.ObserveFeedbackPage(ctx, request)
			if err != nil {
				return nil, ErrFeedbackUnavailable
			}
			if page.Code != "ok" {
				return nil, ErrFeedbackUnavailable
			}
			if !githubport.CurrentFeedbackPage(page, request) {
				return nil, ErrFeedbackAmbiguous
			}
			items = append(items, page.Items...)
			pageHashes = append(pageHashes, page.SHA256)
			if page.Complete {
				complete = true
				break
			}
		}
		if !complete {
			return nil, ErrFeedbackAmbiguous
		}
		snapshotID := stableID("github-feedback-snapshot", current.binding.BindingSHA256, string(source), strings.Join(pageHashes, ":"))
		result = append(result, domainfeedback.SealSnapshot(domainfeedback.Snapshot{ID: snapshotID, Source: source,
			BindingSHA256: current.binding.BindingSHA256, Complete: true, PageCount: uint32(len(pageHashes)),
			ObservedAtMillis: nowMillis, MaximumAgeMillis: domainfeedback.MaximumObservationAge, Items: items}))
	}
	return result, nil
}

func (service *Service) SyncGitHub(ctx context.Context, command GitHubCommand) (Result, error) {
	current, err := service.load(ctx, command.RoutingContext)
	if err != nil {
		return Result{Run: current.run}, err
	}
	snapshots, err := service.githubSnapshots(ctx, current, command.NowMillis)
	if err != nil {
		return Result{Run: current.run, WaitingExternal: errors.Is(err, ErrFeedbackUnavailable), Code: "github_feedback_unavailable"}, err
	}
	return service.ingest(ctx, current, command.RoutingContext, snapshots)
}

type auditReference struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}

func (service *Service) persistFeedback(ctx context.Context, current, next domain.Run, state domainfeedback.State, transition, identity string, priorRecords int) (domain.Run, bool, error) {
	next.Version = current.Version + 1
	next.Execution.Feedback = &state
	refs := make([]auditReference, 0, len(state.Records)-priorRecords)
	for _, record := range state.Records[priorRecords:] {
		refs = append(refs, auditReference{ID: record.ID, SHA256: record.AuditSHA256})
	}
	commandID := stableID("feedback-command", current.ID, transition, identity)
	result, err := service.store.UpdateRun(ctx, domain.CommandRequest{IdempotencyKey: commandID, Type: transition,
		AggregateID: current.ID, ExpectedVersion: current.Version, Payload: payload(struct {
			Binding, State string
			Records        []auditReference
		}{state.Binding.BindingSHA256, state.SHA256, refs})}, next, domain.Event{ID: stableID("feedback-event", commandID),
		RunID: current.ID, Sequence: current.Version + 2, AggregateID: current.ID, AggregateVersion: next.Version,
		Type: transition, Payload: payload(struct {
			Binding, State, Phase string
			Records               []auditReference
		}{state.Binding.BindingSHA256, state.SHA256, string(state.Phase), refs})})
	if err != nil {
		return current, false, err
	}
	if result.Outcome != domain.CommandApplied {
		return current, result.Replay, ErrConcurrentTransition
	}
	return next, result.Replay, nil
}

func historicalFeedback(run *domain.Run, binding domainfeedback.Binding) (*domainfeedback.State, int, bool) {
	if run.Execution.Feedback == nil {
		return nil, 0, true
	}
	state := run.Execution.Feedback
	if state.Binding == binding {
		return state, len(state.Records), true
	}
	if len(run.Execution.FeedbackHistory) >= 64 {
		return nil, 0, false
	}
	historical := domainfeedback.Invalidate(*state, "binding_changed")
	if !domainfeedback.ValidState(historical) {
		return nil, 0, false
	}
	run.Execution.FeedbackHistory = append(slices.Clone(run.Execution.FeedbackHistory), historical)
	run.Execution.Feedback = nil
	return nil, 0, true
}

func invalidateDelivery(run *domain.Run, nowMillis int64) bool {
	if run.Execution.Validation != nil && !run.Execution.Validation.Invalidated &&
		len(run.Execution.ValidationHistory) >= domainvalidation.MaximumHistory ||
		run.Execution.Review != nil && len(run.Execution.ReviewHistory) >= 64 {
		return false
	}
	if run.Execution.Integration != nil && (integrationdomain.DispatchInFlight(*run.Execution.Integration) ||
		len(run.Execution.IntegrationHistory) >= integrationdomain.MaximumHistoricalStates) {
		return false
	}
	if run.Execution.Publication != nil {
		if publicationdomain.DispatchInFlight(*run.Execution.Publication) || len(run.Execution.PublicationHistory) >= publicationdomain.MaximumHistoricalStates {
			return false
		}
		historical := *run.Execution.Publication
		if !historical.Invalidated {
			historical = publicationdomain.Invalidate(historical, "human_feedback")
		}
		if !publicationdomain.ValidState(historical) || !historical.Invalidated {
			return false
		}
		run.Execution.PublicationHistory = append(slices.Clone(run.Execution.PublicationHistory), historical)
		run.Execution.Publication = nil
	}
	if run.Execution.DirectDelivery != nil {
		if directdomain.DispatchInFlight(*run.Execution.DirectDelivery) || len(run.Execution.DirectDeliveryHistory) >= directdomain.MaximumHistoricalStates {
			return false
		}
		historical := directdomain.Invalidate(*run.Execution.DirectDelivery, "human_feedback", "fresh_candidate_validation_and_review")
		if !directdomain.ValidState(historical) || historical.Phase != directdomain.PhaseInvalidated {
			return false
		}
		run.Execution.DirectDeliveryHistory = append(slices.Clone(run.Execution.DirectDeliveryHistory), historical)
		run.Execution.DirectDelivery = nil
	}
	if run.Execution.Integration != nil {
		historical := integrationdomain.Invalidate(*run.Execution.Integration, "human_feedback", "fresh_candidate_validation_review_feedback_and_publication")
		if !integrationdomain.ValidState(historical) || historical.Phase != integrationdomain.PhaseInvalidated {
			return false
		}
		run.Execution.IntegrationHistory = append(slices.Clone(run.Execution.IntegrationHistory), historical)
		run.Execution.Integration = nil
	}
	if run.Execution.Validation != nil && !run.Execution.Validation.Invalidated {
		historical := domainvalidation.CloneState(*run.Execution.Validation)
		for _, reservation := range run.Execution.Budget.Reservations {
			if reservation.ID != historical.BudgetReservationID || reservation.EffectID != historical.EffectID || reservation.Released {
				continue
			}
			observation := runtimebudget.ActivityObservation{ID: stableID("activity", historical.EffectID, historical.ValidationKey),
				ReservationID: historical.BudgetReservationID, EffectID: historical.EffectID,
				Activity: runtimebudget.ActivityValidationCycle, LeaseEpoch: run.Execution.LeaseBinding.Epoch,
				PolicyRevision: run.Execution.Budget.Policy.Revision, ObservedAtMillis: nowMillis,
				CandidateSHA: historical.Binding.CandidateSHA, Failed: true}
			observation.FactHash = runtimebudget.ActivityObservationHash(observation)
			ledger, _, err := runtimebudget.ApplyActivityObservation(run.Execution.Budget, observation)
			if err != nil {
				return false
			}
			run.Execution.Budget = ledger
			break
		}
		historical = domainvalidation.Invalidate(historical, domainvalidation.CodeHumanFeedback)
		if !domainvalidation.ValidState(historical) || !historical.Invalidated {
			return false
		}
		run.Execution.ValidationHistory = append(slices.Clone(run.Execution.ValidationHistory), historical)
		run.Execution.Validation = nil
	}
	if run.Execution.Review != nil {
		historical := *run.Execution.Review
		if !historical.Invalidated {
			historical = domainreview.Invalidate(historical, "human_feedback")
		}
		if !domainreview.ValidState(historical) || !historical.Invalidated {
			return false
		}
		run.Execution.ReviewHistory = append(slices.Clone(run.Execution.ReviewHistory), historical)
		run.Execution.Review = nil
	}
	if run.Execution.CandidateAuthority == nil || run.Execution.CandidateAuthority.Invalidated {
		return false
	}
	authority := *run.Execution.CandidateAuthority
	// Human feedback reopens the complete Candidate gate set. The immutable
	// Validation, Review, and delivery states remain available as audit facts,
	// but none of their bindings retains current authority.
	authority.Downstream = candidate.Downstream{}
	run.Execution.CandidateAuthority = &authority
	return true
}

func (service *Service) ingest(ctx context.Context, current loaded, routing RoutingContext, snapshots []domainfeedback.Snapshot) (Result, error) {
	if current.task.Complete || current.run.Execution.CandidateAuthority.Downstream.Integration != nil {
		return service.createFollowUp(ctx, current, snapshots, routing.NowMillis)
	}
	next := current.run
	prior, priorRecords, ok := historicalFeedback(&next, current.binding)
	if !ok {
		return Result{Run: current.run}, ErrFeedbackAmbiguous
	}
	state, changed, ok := domainfeedback.Reconcile(prior, current.binding, snapshots, routing.NowMillis)
	if !ok {
		return Result{Run: current.run}, ErrFeedbackAmbiguous
	}
	if state.Phase == domainfeedback.PhaseCorrectionReady && !routing.CorrectionPolicy.AutoFixReviewFeedback {
		state, ok = domainfeedback.RequireManual(state, "feedback_manual_correction_required", "authenticated_human_correction_decision")
		changed = true
		if !ok {
			return Result{Run: current.run}, ErrFeedbackAmbiguous
		}
	}
	authority := *next.Execution.CandidateAuthority
	if domainfeedback.BlocksDelivery(state) {
		if !invalidateDelivery(&next, routing.NowMillis) {
			return Result{Run: current.run}, ErrFeedbackAmbiguous
		}
		authority = *next.Execution.CandidateAuthority
	}
	authority.Downstream.Feedback = &candidate.EvidenceBinding{ID: stableID("feedback-evidence", state.SHA256),
		CandidateID: authority.CandidateID, CandidateSHA: authority.CandidateSHA, BaseSHA: authority.BaseSHA,
		Generation: authority.Generation, BindingSHA256: authority.BindingSHA256}
	next.Execution.CandidateAuthority = &authority
	if state.Phase == domainfeedback.PhaseNeedsYou {
		next.Execution.NeedsYou = &execution.NeedsYou{Code: execution.NeedCode(state.NeedsYouCode), WakeCondition: state.WakeCondition, CleanupAuthorized: false}
	} else if next.Execution.NeedsYou != nil && strings.HasPrefix(string(next.Execution.NeedsYou.Code), "feedback_") {
		next.Execution.NeedsYou = nil
	}
	if changed || next.Execution.Feedback == nil || prior == nil {
		identity := state.SHA256
		persisted, replayed, err := service.persistFeedback(ctx, current.run, next, state, "feedback.observed", identity, priorRecords)
		if err != nil {
			return Result{Run: current.run}, err
		}
		current.run, state = persisted, *persisted.Execution.Feedback
		if replayed {
			return Result{Run: persisted, Feedback: &state, Replayed: true}, nil
		}
	} else {
		current.run = next
	}
	if state.Phase == domainfeedback.PhaseNeedsYou {
		return Result{Run: current.run, Feedback: &state, Progressed: changed, NeedsHumanDecision: true, Code: state.NeedsYouCode}, nil
	}
	if state.Phase == domainfeedback.PhaseCorrectionRouted || state.Phase == domainfeedback.PhaseObserved {
		return Result{Run: current.run, Feedback: &state, Progressed: changed, Replayed: !changed,
			CorrectionRouted: state.Phase == domainfeedback.PhaseCorrectionRouted}, nil
	}
	return service.routeCorrection(ctx, current, routing)
}

func emptySourceRevision(run domain.Run, source domaincorrection.Source) string {
	value := []string{run.CurrentCandidateID, run.Execution.CandidateAuthority.CandidateSHA, string(source)}
	if run.Execution.Review != nil {
		value = append(value, run.Execution.Review.ReviewKey)
		if run.Execution.Review.Evidence != nil {
			value = append(value, run.Execution.Review.Evidence.ID, string(run.Execution.Review.Evidence.Verdict))
		}
		if run.Execution.Review.CIObservation != nil {
			value = append(value, run.Execution.Review.CIObservation.ID, run.Execution.Review.CIObservation.Status)
		}
	}
	return domainfeedback.DigestText(strings.Join(value, "\x1f"))
}

func otherCorrectionSources(run domain.Run) ([]domaincorrection.SourceSnapshot, []domaincorrection.FindingInput, bool) {
	sources := []domaincorrection.Source{domaincorrection.SourceReview, domaincorrection.SourceValidation, domaincorrection.SourceCI}
	if state := run.Execution.Correction; state != nil && domaincorrection.ValidState(*state) {
		batch, ok := domaincorrection.CurrentBatch(*state)
		if ok && batch.CandidateID == run.CurrentCandidateID && batch.CandidateSHA == run.Execution.CandidateAuthority.CandidateSHA {
			snapshots := make([]domaincorrection.SourceSnapshot, 0, 3)
			for _, source := range sources {
				for _, snapshot := range batch.Sources {
					if snapshot.Source == source {
						snapshots = append(snapshots, snapshot)
					}
				}
			}
			findings := make([]domaincorrection.FindingInput, 0)
			for _, finding := range batch.Findings {
				if finding.Source != domaincorrection.SourceHuman {
					findings = append(findings, finding.FindingInput)
				}
			}
			if len(snapshots) == 3 {
				return snapshots, findings, true
			}
		}
	}
	review := run.Execution.Review
	if review == nil && len(run.Execution.ReviewHistory) > 0 {
		review = &run.Execution.ReviewHistory[len(run.Execution.ReviewHistory)-1]
	}
	if review != nil && review.Evidence != nil {
		if review.Evidence.Verdict == "changes_requested" || review.CIObservation != nil && review.CIObservation.Status != "passed" {
			return nil, nil, false
		}
	}
	result := make([]domaincorrection.SourceSnapshot, 0, 3)
	for _, source := range sources {
		result = append(result, domaincorrection.SourceSnapshot{Source: source, Revision: emptySourceRevision(run, source), Count: 0})
	}
	return result, []domaincorrection.FindingInput{}, true
}

func (service *Service) routeCorrection(ctx context.Context, current loaded, routing RoutingContext) (Result, error) {
	state := current.run.Execution.Feedback
	if state == nil || state.Phase != domainfeedback.PhaseCorrectionReady || current.run.Execution.NeedsYou != nil {
		return Result{Run: current.run}, ErrFeedbackAmbiguous
	}
	humanSnapshot, humanFindings, ok := domainfeedback.CorrectionInputs(*state)
	if !ok {
		return Result{Run: current.run, Feedback: state}, nil
	}
	snapshots, findings, ok := otherCorrectionSources(current.run)
	if !ok {
		manual, _ := domainfeedback.RequireManual(*state, "feedback_incomplete_current_findings", "complete_current_correction_findings")
		next := current.run
		next.Execution.NeedsYou = &execution.NeedsYou{Code: execution.NeedCode(manual.NeedsYouCode), WakeCondition: manual.WakeCondition, CleanupAuthorized: false}
		persisted, _, err := service.persistFeedback(ctx, current.run, next, manual, "feedback.needs_you", manual.SHA256, len(state.Records))
		return Result{Run: persisted, Feedback: &manual, Progressed: err == nil, NeedsHumanDecision: true, Code: manual.NeedsYouCode}, err
	}
	snapshots = append(snapshots, humanSnapshot)
	findings = append(findings, humanFindings...)
	routed, err := service.correction.IngestFeedback(ctx, correctionport.IngestCommand{RunID: current.run.ID,
		ExpectedRunVersion: current.run.Version, LeaseEpoch: routing.LeaseEpoch,
		OriginalTaskAgentUUID: current.run.Execution.PrimarySession.NativeAgentID,
		CriterionIDs:          slices.Clone(current.run.Execution.CriterionIDs), FrozenPlanDigest: routing.FrozenPlanDigest,
		CurrentPlanDigest: routing.CurrentPlanDigest, FrozenSkillSetDigest: routing.FrozenSkillSetDigest,
		CurrentSkillSetDigest: routing.CurrentSkillSetDigest, CurrentDecisionDigest: routing.CurrentDecisionDigest,
		CurrentDiffDigest: routing.CurrentDiffDigest, Policy: routing.CorrectionPolicy,
		SourceSnapshots: snapshots, Findings: findings, NowMillis: routing.NowMillis})
	if err != nil {
		return Result{Run: current.run, Feedback: state, CorrectionReady: true}, err
	}
	batch, ok := domaincorrection.CurrentBatch(*routed.Run.Execution.Correction)
	if !ok {
		return Result{Run: routed.Run, Feedback: state}, ErrFeedbackAmbiguous
	}
	feedbackState := routed.Run.Execution.Feedback
	marked, ok := domainfeedback.MarkCorrectionRouted(*feedbackState, batch.SHA256)
	if !ok {
		return Result{Run: routed.Run, Feedback: feedbackState}, ErrFeedbackAmbiguous
	}
	next := routed.Run
	next.Execution.Feedback = &marked
	persisted, replayed, err := service.persistFeedback(ctx, routed.Run, next, marked, "feedback.correction_routed", batch.SHA256, len(marked.Records))
	if err != nil {
		return Result{Run: routed.Run, Feedback: feedbackState, CorrectionReady: true}, err
	}
	return Result{Run: persisted, Feedback: &marked, Progressed: true, Replayed: replayed,
		CorrectionRouted: true, NeedsHumanDecision: persisted.Execution.NeedsYou != nil,
		Code: func() string {
			if persisted.Execution.NeedsYou != nil {
				return string(persisted.Execution.NeedsYou.Code)
			}
			return ""
		}()}, nil
}

func (service *Service) ApplyHumanDecision(ctx context.Context, command DecisionCommand) (Result, error) {
	current, err := service.load(ctx, command.RoutingContext)
	if err != nil {
		return Result{Run: current.run}, err
	}
	state := current.run.Execution.Feedback
	if state == nil || state.SHA256 != command.ExpectedStateSHA256 || current.run.Execution.NeedsYou == nil ||
		string(current.run.Execution.NeedsYou.Code) != state.NeedsYouCode {
		return Result{Run: current.run}, ErrConcurrentTransition
	}
	decided, ok := domainfeedback.ApplyDecision(*state, command.Actor, command.DecisionID, command.AllowCorrection, command.NowMillis)
	if !ok {
		return Result{Run: current.run}, ErrInvalidCommand
	}
	next := current.run
	next.Execution.NeedsYou = nil
	persisted, replayed, err := service.persistFeedback(ctx, current.run, next, decided, "feedback.human_decision", decided.Decision.AuditSHA256, len(state.Records))
	if err != nil {
		return Result{Run: current.run}, err
	}
	if !command.AllowCorrection {
		return Result{Run: persisted, Feedback: &decided, Progressed: true, Replayed: replayed}, nil
	}
	current.run = persisted
	authorized := command.RoutingContext
	// This is a one-batch audited human authorization, not a silent Project or
	// Task policy change. CI correction policy remains independently frozen.
	authorized.CorrectionPolicy.AutoFixReviewFeedback = true
	return service.routeCorrection(ctx, current, authorized)
}

func cloneParent(value *domain.PlanningNodeRef) *domain.PlanningNodeRef {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (service *Service) createFollowUp(ctx context.Context, current loaded, snapshots []domainfeedback.Snapshot, nowMillis int64) (Result, error) {
	state, _, ok := domainfeedback.Reconcile(nil, current.binding, snapshots, nowMillis)
	if !ok {
		return Result{Run: current.run}, ErrFeedbackAmbiguous
	}
	records := domainfeedback.FollowUpRecords(state)
	if len(records) == 0 {
		return Result{Run: current.run, Feedback: &state, Replayed: true}, nil
	}
	followUps := make([]domain.Task, 0, len(records))
	allReplayed := true
	for _, record := range records {
		audit, valid := domainfeedback.Revision(record)
		if !valid {
			return Result{Run: current.run, Feedback: &state, FollowUpTasks: followUps}, ErrFeedbackAmbiguous
		}
		fingerprint := domainfeedback.DigestText(state.Binding.BindingSHA256 + "\x1f" + audit.SHA256)
		taskID := stableID("feedback-followup", current.task.ID, fingerprint)
		suffix := strings.ToUpper(fingerprint[:8])
		key := current.task.Key + "-FB-" + suffix
		if len(key) > 128 {
			key = "FB-" + suffix
		}
		followUp := domain.Task{ID: taskID, ProjectID: current.task.ProjectID, Key: key,
			Title:              "Follow up human feedback for " + current.task.Key,
			Objective:          fmt.Sprintf("Address authenticated human feedback recorded by audit %s without changing completed Task %s history.", fingerprint, current.task.ID),
			AcceptanceCriteria: "Resolve the linked human feedback and preserve the original completed Task and Run as immutable history.",
			WorkspaceIDs:       slices.Clone(current.task.WorkspaceIDs), Parent: cloneParent(current.task.Parent), Priority: domain.PriorityNormal,
			Labels: []string{"human-feedback", "follow-up"}, ExternalReferences: []domain.ExternalReference{
				{Provider: "director.discovered-from", Key: current.task.ID}, {Provider: "director.feedback-audit", Key: fingerprint}},
			QueuedAtUnixMillis: audit.UpdatedAtMillis, Version: 0}
		commandID := stableID("feedback-followup-command", current.task.ID, fingerprint)
		event := domain.Event{ID: stableID("feedback-followup-event", taskID), Sequence: 1, AggregateID: taskID,
			AggregateVersion: 0, Type: "feedback.followup_created", Payload: payload(struct {
				OriginalTaskID, OriginalRunID, FeedbackBinding string
				Task                                           domain.Task
				Record                                         domainfeedback.RevisionAudit
			}{current.task.ID, current.run.ID, state.Binding.BindingSHA256, followUp, audit})}
		result, err := service.store.CreateTask(ctx, domain.CommandRequest{IdempotencyKey: commandID, Type: "feedback.followup_create",
			AggregateID: taskID, ExpectedVersion: 0, Payload: event.Payload}, followUp, event)
		if err != nil {
			// A response lost after commit is reconciled by the deterministic Task
			// identity on the next call; no original Task/Run row is changed.
			if existing, readErr := service.store.Task(ctx, taskID); readErr == nil && existing.ID == followUp.ID && existing.Key == followUp.Key &&
				existing.QueuedAtUnixMillis == followUp.QueuedAtUnixMillis {
				followUps = append(followUps, existing)
				continue
			}
			return Result{Run: current.run, Feedback: &state, FollowUpTasks: followUps}, err
		}
		if result.Outcome != domain.CommandApplied {
			return Result{Run: current.run, Feedback: &state, FollowUpTasks: followUps}, ErrConcurrentTransition
		}
		allReplayed = allReplayed && result.Replay
		followUps = append(followUps, followUp)
	}
	first := followUps[0]
	return Result{Run: current.run, Feedback: &state, FollowUpTask: &first, FollowUpTasks: followUps,
		Progressed: !allReplayed, Replayed: allReplayed}, nil
}
