// SPDX-License-Identifier: Apache-2.0

package validation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/candidate"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	domaincorrection "github.com/mcuadros/director-engine/domain/correction"
	"github.com/mcuadros/director-engine/domain/execution"
	feedbackdomain "github.com/mcuadros/director-engine/domain/feedback"
	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
	domainreview "github.com/mcuadros/director-engine/domain/review"
	"github.com/mcuadros/director-engine/domain/runtimebudget"
	domainvalidation "github.com/mcuadros/director-engine/domain/validation"
	gitport "github.com/mcuadros/director-engine/ports/git"
	githubport "github.com/mcuadros/director-engine/ports/github"
)

const (
	testTaskAgent   = "11111111-1111-4111-8111-111111111111"
	testCoordinator = "22222222-2222-4222-8222-222222222222"
	testReviewer    = "33333333-3333-4333-8333-333333333333"
)

var errLostResponse = errors.New("test lost response")

func cloneRun(value domain.Run) domain.Run {
	encoded, _ := json.Marshal(value)
	var result domain.Run
	_ = json.Unmarshal(encoded, &result)
	return result
}

type memoryStore struct {
	mu        sync.Mutex
	project   domain.Project
	workspace domain.Workspace
	task      domain.Task
	run       domain.Run
	candidate domain.Candidate
	commands  map[string]domain.CommandResult
	loseNext  bool
}

func (store *memoryStore) Project(context.Context, string) (domain.Project, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.project, nil
}
func (store *memoryStore) Workspace(context.Context, string) (domain.Workspace, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.workspace, nil
}
func (store *memoryStore) Task(context.Context, string) (domain.Task, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.task, nil
}
func (store *memoryStore) Candidate(context.Context, string) (domain.Candidate, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.candidate, nil
}
func (store *memoryStore) Run(context.Context, string) (domain.Run, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return cloneRun(store.run), nil
}

func (store *memoryStore) UpdateRun(_ context.Context, command domain.CommandRequest, next domain.Run, event domain.Event) (domain.CommandResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if prior, ok := store.commands[command.IdempotencyKey]; ok {
		prior.Replay = true
		return prior, nil
	}
	if command.ExpectedVersion != store.run.Version {
		result := domain.CommandResult{Outcome: domain.CommandRejectedVersionConflict, ObservedVersion: store.run.Version, EventID: event.ID}
		store.commands[command.IdempotencyKey] = result
		return result, nil
	}
	if next.Version != store.run.Version+1 || !runtimebudget.ValidLedgerForLease(next.Execution.Budget, next.Execution.LeaseBinding.Epoch) ||
		next.Execution.Validation != nil && (next.Execution.ValidationPolicy == nil ||
			next.Execution.Validation.Policy.SHA256 != next.Execution.ValidationPolicy.SHA256 || !domainvalidation.ValidState(*next.Execution.Validation)) {
		return domain.CommandResult{}, errors.New("invalid validation write")
	}
	if next.Execution.Review != nil && !domainreview.ValidState(*next.Execution.Review) {
		return domain.CommandResult{}, errors.New("invalid review write")
	}
	if next.Execution.Publication != nil && !publicationdomain.ValidState(*next.Execution.Publication) {
		return domain.CommandResult{}, errors.New("invalid publication write")
	}
	store.run = cloneRun(next)
	result := domain.CommandResult{Outcome: domain.CommandApplied, ObservedVersion: next.Version, EventID: event.ID}
	store.commands[command.IdempotencyKey] = result
	if store.loseNext {
		store.loseNext = false
		return domain.CommandResult{}, errLostResponse
	}
	return result, nil
}

type testBranches struct {
	mu       sync.Mutex
	remote   string
	base     string
	nextBase string
	switchAt uint64
	observed uint64
	missing  bool
}

type testGitHub struct {
	mu                     sync.Mutex
	repositoryID           int64
	nodeID                 string
	owner                  string
	name                   string
	viewer                 string
	workflowRuns           []domainvalidation.WorkflowRun
	checkRuns              []domainvalidation.CheckRun
	commitStatuses         []domainvalidation.CommitStatus
	combinedStatus         string
	repositoryObservations uint64
	replaceRepositoryAt    uint64
	workflowTotalDrift     bool
}

func (forge *testGitHub) ObserveChecksRepository(_ context.Context, request githubport.ChecksRepositoryRequest) (domainvalidation.RepositoryObservation, error) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	forge.repositoryObservations++
	repositoryID := forge.repositoryID
	if forge.replaceRepositoryAt > 0 && forge.repositoryObservations >= forge.replaceRepositoryAt {
		repositoryID++
	}
	return domainvalidation.SealRepositoryObservation(domainvalidation.RepositoryObservation{ID: fmt.Sprintf("repository-%d", request.TaskStoreNowMillis),
		Code: domainvalidation.CodeOK, RepositoryID: repositoryID, RepositoryNodeID: forge.nodeID, Owner: forge.owner,
		Name: forge.name, ViewerLogin: forge.viewer, Authenticated: true, CanReadChecks: true, TLSVerified: true,
		APIVersion: "2022-11-28", RateRemaining: 5_000, ObservedAtMillis: request.TaskStoreNowMillis,
		MaximumAgeMillis: domainvalidation.MaximumObservationAgeMS}), nil
}

func testPageRange(length int, request githubport.CandidatePageRequest) (int, int, uint32, bool) {
	start := int((request.Page - 1) * request.PageSize)
	if start > length {
		start = length
	}
	end := min(start+int(request.PageSize), length)
	if end < length {
		return start, end, request.Page + 1, false
	}
	return start, end, 0, true
}

func (forge *testGitHub) ListWorkflowRuns(_ context.Context, request githubport.CandidatePageRequest) (domainvalidation.WorkflowPage, error) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	start, end, next, complete := testPageRange(len(forge.workflowRuns), request)
	total := uint32(len(forge.workflowRuns))
	if forge.workflowTotalDrift && request.Page > 1 {
		total++
	}
	return domainvalidation.SealWorkflowPage(domainvalidation.WorkflowPage{ID: fmt.Sprintf("workflows-%d", request.Page), Code: domainvalidation.CodeOK,
		CandidateSHA: request.CandidateSHA, Page: request.Page, TotalCount: total, NextPage: next, Complete: complete,
		Runs: slices.Clone(forge.workflowRuns[start:end]), ObservedAtMillis: request.TaskStoreNowMillis, MaximumAgeMillis: domainvalidation.MaximumObservationAgeMS}), nil
}

func (forge *testGitHub) ListCheckRuns(_ context.Context, request githubport.CandidatePageRequest) (domainvalidation.CheckPage, error) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	start, end, next, complete := testPageRange(len(forge.checkRuns), request)
	return domainvalidation.SealCheckPage(domainvalidation.CheckPage{ID: fmt.Sprintf("checks-%d", request.Page), Code: domainvalidation.CodeOK,
		CandidateSHA: request.CandidateSHA, Page: request.Page, TotalCount: uint32(len(forge.checkRuns)), NextPage: next, Complete: complete,
		Checks: slices.Clone(forge.checkRuns[start:end]), ObservedAtMillis: request.TaskStoreNowMillis, MaximumAgeMillis: domainvalidation.MaximumObservationAgeMS}), nil
}

func (forge *testGitHub) ListCommitStatuses(_ context.Context, request githubport.CandidatePageRequest) (domainvalidation.StatusPage, error) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	start, end, next, complete := testPageRange(len(forge.commitStatuses), request)
	rollup := forge.combinedStatus
	if len(forge.commitStatuses) == 0 {
		rollup = "checks_only_no_statuses"
	}
	return domainvalidation.SealStatusPage(domainvalidation.StatusPage{ID: fmt.Sprintf("statuses-%d", request.Page), Code: domainvalidation.CodeOK,
		CandidateSHA: request.CandidateSHA, CombinedState: rollup, Page: request.Page, TotalCount: uint32(len(forge.commitStatuses)),
		NextPage: next, Complete: complete, Statuses: slices.Clone(forge.commitStatuses[start:end]),
		ObservedAtMillis: request.TaskStoreNowMillis, MaximumAgeMillis: domainvalidation.MaximumObservationAgeMS}), nil
}

func (branches *testBranches) ObserveRemoteRef(_ context.Context, request gitport.RemoteRefRequest) (publicationdomain.RefObservation, error) {
	branches.mu.Lock()
	defer branches.mu.Unlock()
	branches.observed++
	oid := branches.base
	if branches.switchAt > 0 && branches.observed >= branches.switchAt {
		oid = branches.nextBase
	}
	exists := !branches.missing
	if !exists {
		oid = ""
	}
	return publicationdomain.SealRefObservation(publicationdomain.RefObservation{ID: fmt.Sprintf("base-%d", branches.observed),
		Code: publicationdomain.CodeOK, Ref: "refs/heads/" + request.Branch, Exists: exists, OID: oid,
		RemoteCanonical: branches.remote, ObservedAtMillis: request.TaskStoreNowMillis,
		MaximumAgeMillis: publicationdomain.MaximumObservationAgeMS}), nil
}

func (*testBranches) PushExact(context.Context, gitport.PushRequest) (gitport.PushResult, error) {
	return gitport.PushResult{Code: publicationdomain.CodeResponseUnknown}, nil
}

type fixture struct {
	store    *memoryStore
	branches *testBranches
	github   *testGitHub
	service  *Service
	command  AdmitCommand
	now      int64
}

func candidateFixture(repositoryHash, branch string) (domain.Candidate, candidate.Authority) {
	claim := candidate.Claim{SchemaVersion: candidate.ClaimSchemaVersion, ID: "claim-1", ProjectID: "project-1", WorkspaceID: "workspace-1",
		TaskID: "dir-m4.6", RunID: "run-1", ActorID: testTaskAgent, WorktreeID: "worktree-1", Branch: branch,
		BaseRef: "refs/heads/main", CandidateSHA: strings.Repeat("b", 40), BaseSHA: strings.Repeat("a", 40), LeaseEpoch: 1,
		ExpectedRunVersion: 1, TaskVersion: 3, AcceptanceSHA256: strings.Repeat("1", 64), ConfigurationSHA256: strings.Repeat("2", 64),
		ProfileSHA256: strings.Repeat("3", 64), ContextSHA256: strings.Repeat("4", 64), DecisionsSHA256: strings.Repeat("5", 64),
		FindingsSHA256: strings.Repeat("6", 64)}
	manifest := candidate.Manifest{SchemaVersion: candidate.ManifestSchemaVersion, CandidateSHA: claim.CandidateSHA, BaseSHA: claim.BaseSHA,
		ParentSHA: claim.BaseSHA, TreeSHA: strings.Repeat("c", 40), DiffSHA256: strings.Repeat("7", 64), ChangedPathsSHA256: strings.Repeat("8", 64),
		RepositoryBindingSHA256: repositoryHash, ClaimSHA256: candidate.ClaimSHA256(claim), ObservationSHA256: strings.Repeat("9", 64),
		AcceptanceSHA256: claim.AcceptanceSHA256, ConfigurationSHA256: claim.ConfigurationSHA256, ProfileSHA256: claim.ProfileSHA256,
		ContextSHA256: claim.ContextSHA256, DecisionsSHA256: claim.DecisionsSHA256, FindingsSHA256: claim.FindingsSHA256,
		GraphPolicySHA256: candidate.GraphPolicySHA256(claim.GraphPolicy)}
	manifest.BindingSHA256 = candidate.ManifestSHA256(manifest)
	record := domain.Candidate{SchemaVersion: domain.CandidateSchemaVersion, ID: "candidate-1", RunID: "run-1", Sequence: 1,
		CommitSHA: claim.CandidateSHA, Claim: claim, Manifest: manifest, AdmittedAtMillis: 100}
	return record, candidate.NewAuthority(0, record.ID, branch, 3, manifest)
}

func publicationDraft(t *testing.T, record domain.Candidate, authority candidate.Authority, repository execution.RepositoryBinding, githubID int64) (publicationdomain.Policy, publicationdomain.State, candidate.EvidenceBinding) {
	t.Helper()
	policy := publicationdomain.NewPolicy("pull_request", true, nil)
	binding := publicationdomain.SealBinding(publicationdomain.Binding{TaskID: "dir-m4.6", RunID: "run-1", CandidateID: record.ID,
		CandidateSHA: record.CommitSHA, BaseSHA: record.Manifest.BaseSHA, TreeSHA: record.Manifest.TreeSHA,
		ManifestSHA256: record.Manifest.BindingSHA256, CandidateGeneration: authority.Generation, TaskVersion: 3,
		Branch: repository.Branch, BaseRef: "refs/heads/main", RepositoryBindingSHA256: execution.RepositoryBindingSHA256(repository),
		CanonicalRemote: repository.CanonicalRemote, GitHubRepositoryID: githubID, GitHubRepositoryNodeID: "R_node",
		RepositoryOwner: "example", RepositoryName: "product", HeadOwner: "example", OwnershipSHA256: strings.Repeat("d", 64), PolicySHA256: policy.SHA256})
	template, ok := publicationdomain.RenderTemplate(binding, policy, "Implement GitHub checks and base invalidation", "pending", "", "pending", "", nil)
	if !ok {
		t.Fatal("publication template")
	}
	state, ok := publicationdomain.NewState(binding, policy, template, "", nil)
	if !ok {
		t.Fatal("publication state")
	}
	state.Push.Phase, state.PullRequest.Phase, state.Metadata.Phase = publicationdomain.EffectComplete, publicationdomain.EffectComplete, publicationdomain.EffectComplete
	state.OwnedPullRequest = &publicationdomain.OwnedPullRequest{Number: 7, NodeID: "PR_node_7", URL: "https://github.com/example/product/pull/7",
		MarkerSHA256: publicationdomain.DigestText(publicationdomain.Marker(binding)), HeadSHA: record.CommitSHA, Draft: true}
	state.Evidence = publicationdomain.EvidenceFor(state, false, 500)
	if state.Evidence == nil || !publicationdomain.ValidState(state) {
		t.Fatal("draft evidence")
	}
	evidence := candidate.EvidenceBinding{ID: state.Evidence.ID, CandidateID: authority.CandidateID, CandidateSHA: authority.CandidateSHA,
		BaseSHA: authority.BaseSHA, Generation: authority.Generation, BindingSHA256: authority.BindingSHA256}
	return policy, state, evidence
}

func reviewIntent(t *testing.T, record domain.Candidate, authority candidate.Authority, ciSlot string) domainreview.State {
	t.Helper()
	binding := domainreview.SealBinding(domainreview.Binding{TaskID: "dir-m4.6", RunID: "run-1", CandidateID: record.ID,
		CandidateSHA: record.CommitSHA, BaseSHA: record.Manifest.BaseSHA, TreeSHA: record.Manifest.TreeSHA,
		ManifestSHA256: record.Manifest.BindingSHA256, AcceptanceSHA256: record.Manifest.AcceptanceSHA256,
		ConfigurationSHA256: record.Manifest.ConfigurationSHA256, ProfileSHA256: record.Manifest.ProfileSHA256,
		ContextSHA256: record.Manifest.ContextSHA256, DecisionsSHA256: record.Manifest.DecisionsSHA256,
		FindingsSHA256: record.Manifest.FindingsSHA256, CandidateGeneration: authority.Generation, CISlotID: ciSlot,
		TaskAgentUUID: testTaskAgent, CoordinatorUUID: testCoordinator, ReviewOwnerUUID: testReviewer})
	state, ok := domainreview.NewState(binding, []string{"criterion-1"}, []domainreview.Probe{}, domainreview.ProfileDecision{Admitted: true, Code: "reviewer_profile_admitted"})
	if !ok {
		t.Fatal("review state")
	}
	state.SourcePath, state.PrimaryPath, state.ReviewerRoot, state.CheckoutPath = "/srv/product", "/srv/task", "/srv/reviews", "/srv/reviews/review-1"
	state.PrimaryHeadSHA = record.CommitSHA
	if !domainreview.ValidState(state) {
		t.Fatal("review intent invalid")
	}
	return state
}

func correctionReadyFeedback(t *testing.T, run domain.Run, record domain.Candidate, nowMillis int64) feedbackdomain.State {
	t.Helper()
	authority := run.Execution.CandidateAuthority
	binding := feedbackdomain.SealBinding(feedbackdomain.Binding{ProjectID: run.Execution.Scope.ProjectID,
		WorkspaceID: run.Execution.Scope.WorkspaceID, TaskID: run.TaskID, TaskVersion: authority.TaskVersion,
		RunID: run.ID, CandidateID: record.ID, CandidateSHA: record.CommitSHA, BaseSHA: record.Manifest.BaseSHA,
		CandidateGeneration: authority.Generation, ManifestSHA256: record.Manifest.BindingSHA256})
	item := feedbackdomain.Item{Source: feedbackdomain.SourcePaseoDirect, ExternalID: "message-base-race", RevisionID: "revision-1",
		Actor: feedbackdomain.Actor{Kind: feedbackdomain.ActorHuman, ID: "human-1", Login: "owner", Authenticated: true,
			Attestation: feedbackdomain.AttestationPaseoHuman}, Kind: feedbackdomain.KindComment,
		CandidateSHA: record.CommitSHA, BaseSHA: record.Manifest.BaseSHA, ContextSHA256: strings.Repeat("d", 64),
		Body: "Reconcile the exact feedback batch", Actionable: true, Severity: domaincorrection.SeverityP3,
		CreatedAtMillis: nowMillis - 2, UpdatedAtMillis: nowMillis - 1}
	snapshot := feedbackdomain.SealSnapshot(feedbackdomain.Snapshot{ID: "feedback-base-race", Source: feedbackdomain.SourcePaseoDirect,
		BindingSHA256: binding.BindingSHA256, PageCount: 1, ObservedAtMillis: nowMillis,
		MaximumAgeMillis: feedbackdomain.MaximumObservationAge, Items: []feedbackdomain.Item{item}})
	state, _, ok := feedbackdomain.Reconcile(nil, binding, []feedbackdomain.Snapshot{snapshot}, nowMillis)
	if !ok || !feedbackdomain.DispatchInFlight(state) {
		t.Fatal("correction-ready feedback fixture")
	}
	return state
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	const remote = "https://github.com/example/product"
	branch := "task/dir-m4.6-github-checks"
	repository := execution.RepositoryBinding{RepositoryID: "repository-1", RepositoryKey: "github.com/example/product", CanonicalRemote: remote,
		SourcePath: "/srv/product", SourceDevice: 1, SourceInode: 2, GitCommonDirectory: "/srv/product/.git", GitCommonDevice: 1,
		GitCommonInode: 3, WorktreePath: "/srv/task", Branch: branch, BaseSHA: strings.Repeat("a", 40)}
	repositoryHash := execution.RepositoryBindingSHA256(repository)
	record, authority := candidateFixture(repositoryHash, branch)
	publicationPolicy, publication, publicationEvidence := publicationDraft(t, record, authority, repository, 123)
	authority.Downstream.Publication = &publicationEvidence
	budgetPolicy := runtimebudget.NewPolicy("configuration-1", 1_000_000, 10_000, 32, 0, 4)
	ledger, err := runtimebudget.NewLedger(budgetPolicy, 0)
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{project: domain.Project{ID: "project-1", State: "active", Lease: &domain.ProjectLease{HolderInstance: "engine-1",
		HolderProcessIdentity: "process-1", Epoch: 1, AcquiredAtMillis: 1, RenewedAtMillis: 1, ExpiresAtMillis: 1_000_000, DispatchAllowed: true}},
		workspace: domain.Workspace{ID: "workspace-1", ProjectID: "project-1", Repository: domain.RepositoryIdentity{ID: repository.RepositoryID,
			Key: repository.RepositoryKey, CanonicalRemote: repository.CanonicalRemote, SourcePath: repository.SourcePath, SourceDevice: repository.SourceDevice,
			SourceInode: repository.SourceInode, GitCommonDirectory: repository.GitCommonDirectory, GitCommonDevice: repository.GitCommonDevice, GitCommonInode: repository.GitCommonInode}},
		task: domain.Task{ID: "dir-m4.6", ProjectID: "project-1", Key: "DIR-M4.6", Title: "Implement GitHub checks and base invalidation",
			Objective: "Observe exact checks.", AcceptanceCriteria: "Reject wrong-SHA checks.", WorkspaceIDs: []string{"workspace-1"}, Version: 3},
		run: domain.Run{ID: "run-1", TaskID: "dir-m4.6", Number: 1, BaseSHA: record.Manifest.BaseSHA, CurrentCandidateID: record.ID, Version: 1,
			Execution: execution.State{SchemaVersion: execution.SchemaVersion, Scope: execution.Scope{ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "dir-m4.6", RunID: "run-1"},
				LeaseBinding: execution.LeaseBinding{HolderInstance: "engine-1", HolderProcessIdentity: "process-1", Epoch: 1}, RepositoryBinding: repository,
				RepositoryBindingHash: repositoryHash, Branch: branch, BaseRef: "refs/heads/main", CandidateAuthority: &authority,
				DeliveryMode: domainconfig.DeliveryPullRequest, PublicationPolicy: &publicationPolicy, Publication: &publication, Budget: ledger}}, candidate: record, commands: map[string]domain.CommandResult{}}
	branches := &testBranches{remote: remote, base: record.Manifest.BaseSHA}
	github := &testGitHub{repositoryID: 123, nodeID: "R_node", owner: "example", name: "product", viewer: "example", combinedStatus: "checks_only_no_statuses"}
	github.workflowRuns = []domainvalidation.WorkflowRun{{ID: 500, WorkflowID: 99, Name: "maintained-linux-ci", HeadSHA: record.CommitSHA,
		HeadRepositoryID: 123, CheckSuiteID: 700, Status: "completed", Conclusion: "success", Attempt: 1, StartedAtMillis: 100, UpdatedAtMillis: 200}}
	github.checkRuns = []domainvalidation.CheckRun{{ID: 600, Name: "Linux CI", HeadSHA: record.CommitSHA, SuiteID: 700,
		SuiteHeadSHA: record.CommitSHA, AppID: 15368, AppSlug: "github-actions", Status: "completed", Conclusion: "success",
		DetailsURLSHA256: strings.Repeat("e", 64), StartedAtMillis: 100, CompletedAtMillis: 200}}
	service, err := NewService(store, branches, github)
	if err != nil {
		t.Fatal(err)
	}
	command := AdmitCommand{RunID: "run-1", ExpectedRunVersion: 1, GitHubRepositoryID: 123, GitHubRepositoryNodeID: "R_node",
		ViewerLogin: "example", CISlotID: "ci-slot-1", WorkflowID: 99, WorkflowName: "maintained-linux-ci",
		RequiredChecks:     []domainvalidation.RequiredCheck{{ID: "linux-ci", Kind: domainvalidation.CheckRunKind, Name: "Linux CI", AppID: 15368, AppSlug: "github-actions"}},
		CycleRuntimeMillis: 10_000, NowMillis: 1_000}
	validationPolicy, ok := domainvalidation.NewPolicy(command.WorkflowID, command.WorkflowName, command.RequiredChecks, command.CycleRuntimeMillis)
	if !ok {
		t.Fatal("validation policy")
	}
	store.run.Execution.ValidationPolicy = &validationPolicy
	return &fixture{store: store, branches: branches, github: github, service: service, now: 1_000, command: command}
}

func TestExactRemoteCIConvergesOnceAndAttachesToReview(t *testing.T) {
	fixture := newFixture(t)
	fixture.store.run.Execution.Review = func() *domainreview.State {
		value := reviewIntent(t, fixture.store.candidate, *fixture.store.run.Execution.CandidateAuthority, fixture.command.CISlotID)
		return &value
	}()
	if _, err := fixture.service.Admit(context.Background(), fixture.command); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
	if err != nil || !result.Terminal || result.Outcome != domainvalidation.OutcomePassed {
		t.Fatalf("reconcile = %#v, %v", result, err)
	}
	run, _ := fixture.store.Run(context.Background(), "run-1")
	if run.Execution.Validation == nil || run.Execution.Validation.Evidence == nil || run.Execution.Validation.Phase != domainvalidation.PhasePassed ||
		run.Execution.CandidateAuthority.Downstream.Validation == nil || run.Execution.CandidateAuthority.Downstream.CI == nil ||
		run.Execution.Review.CIObservation == nil || run.Execution.Review.CIObservation.CandidateSHA != fixture.store.candidate.CommitSHA ||
		run.Execution.Budget.Consumption.CICycles != 1 || fixture.branches.observed != 2 {
		t.Fatalf("terminal run = %#v", run.Execution.Validation)
	}
	version := run.Version
	for index := 0; index < 5; index++ {
		if _, err := fixture.service.Reconcile(context.Background(), "run-1", fixture.now); err != nil {
			t.Fatal(err)
		}
	}
	run, _ = fixture.store.Run(context.Background(), "run-1")
	if run.Version != version || run.Execution.Budget.Consumption.CICycles != 1 {
		t.Fatalf("replay changed Run: version=%d cycles=%d", run.Version, run.Execution.Budget.Consumption.CICycles)
	}
}

func TestAdmitRejectsCallerSelectedChecksOutsideFrozenRunPolicy(t *testing.T) {
	fixture := newFixture(t)
	command := fixture.command
	command.RequiredChecks = []domainvalidation.RequiredCheck{{ID: "different", Kind: domainvalidation.CheckRunKind,
		Name: "Different CI", AppID: 15368, AppSlug: "github-actions"}}
	if _, err := fixture.service.Admit(context.Background(), command); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("unfrozen checks error = %v", err)
	}
	run, _ := fixture.store.Run(context.Background(), "run-1")
	if run.Version != 1 || run.Execution.Validation != nil {
		t.Fatal("caller-selected checks changed durable state")
	}
}

func TestWrongSHAAndAmbiguousCheckFactsNeverBecomeAuthority(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*fixture)
		code   domainvalidation.Code
	}{
		{"workflow SHA", func(f *fixture) { f.github.workflowRuns[0].HeadSHA = strings.Repeat("f", 40) }, domainvalidation.CodeWorkflowSHAMismatch},
		{"check SHA", func(f *fixture) { f.github.checkRuns[0].HeadSHA = strings.Repeat("f", 40) }, domainvalidation.CodeCheckSHAMismatch},
		{"duplicate workflow", func(f *fixture) { f.github.workflowRuns = append(f.github.workflowRuns, f.github.workflowRuns[0]) }, domainvalidation.CodeWorkflowAmbiguous},
		{"suite mismatch", func(f *fixture) { f.github.checkRuns[0].SuiteID++ }, domainvalidation.CodeCheckSuiteAmbiguous},
		{"status collision", func(f *fixture) {
			f.github.combinedStatus = "success"
			f.github.commitStatuses = []domainvalidation.CommitStatus{{ID: 3, Context: "Linux CI", SHA: f.store.candidate.CommitSHA, State: "success", CreatorID: 9, CreatorLogin: "legacy", TargetURLSHA256: strings.Repeat("4", 64), UpdatedAtMillis: 200}}
		}, domainvalidation.CodeCheckAmbiguous},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t)
			test.mutate(fixture)
			if _, err := fixture.service.Admit(context.Background(), fixture.command); err != nil {
				t.Fatal(err)
			}
			result, err := fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
			if !errors.Is(err, ErrValidationRefused) || result.Code != test.code {
				t.Fatalf("result = %#v, %v", result, err)
			}
			run, _ := fixture.store.Run(context.Background(), "run-1")
			if run.Execution.CandidateAuthority.Downstream.Validation != nil || run.Execution.CandidateAuthority.Downstream.CI != nil {
				t.Fatal("ambiguous fact gained authority")
			}
		})
	}
}

func TestAllGitHubCollectionsUseCompleteBoundedPagination(t *testing.T) {
	fixture := newFixture(t)
	sha := fixture.store.candidate.CommitSHA
	for index := 0; index < 100; index++ {
		fixture.github.workflowRuns = append(fixture.github.workflowRuns, domainvalidation.WorkflowRun{ID: int64(1_000 + index), WorkflowID: int64(1_000 + index),
			Name: fmt.Sprintf("other-workflow-%d", index), HeadSHA: sha, HeadRepositoryID: 123, CheckSuiteID: int64(2_000 + index),
			Status: "completed", Conclusion: "success", Attempt: 1, StartedAtMillis: 100, UpdatedAtMillis: 200})
		fixture.github.checkRuns = append(fixture.github.checkRuns, domainvalidation.CheckRun{ID: int64(3_000 + index), Name: fmt.Sprintf("Other Check %d", index),
			HeadSHA: sha, SuiteID: int64(2_000 + index), SuiteHeadSHA: sha, AppID: 15368, AppSlug: "github-actions", Status: "completed",
			Conclusion: "success", DetailsURLSHA256: strings.Repeat("e", 64), StartedAtMillis: 100, CompletedAtMillis: 200})
		fixture.github.commitStatuses = append(fixture.github.commitStatuses, domainvalidation.CommitStatus{ID: int64(4_000 + index), Context: fmt.Sprintf("Legacy Check %d", index),
			SHA: sha, State: "success", CreatorID: 9, CreatorLogin: "legacy", TargetURLSHA256: strings.Repeat("f", 64), UpdatedAtMillis: 200})
	}
	fixture.github.combinedStatus = "success"
	if _, err := fixture.service.Admit(context.Background(), fixture.command); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
	if err != nil || result.Outcome != domainvalidation.OutcomePassed {
		t.Fatalf("pagination = %#v, %v", result, err)
	}
	if result.Run.Execution.Validation.Workflows.Pages != 2 || result.Run.Execution.Validation.Checks.Pages != 2 || result.Run.Execution.Validation.Statuses.Pages != 1 {
		// Statuses contain exactly 100 entries and therefore finish on the first page by total_count, rather than inferring a missing next page from page fullness.
		t.Fatalf("pages = %d/%d/%d", result.Run.Execution.Validation.Workflows.Pages, result.Run.Execution.Validation.Checks.Pages, result.Run.Execution.Validation.Statuses.Pages)
	}
}

func TestRepositoryIdentityReplacementDuringPaginationFailsClosed(t *testing.T) {
	fixture := newFixture(t)
	fixture.github.replaceRepositoryAt = 2
	if _, err := fixture.service.Admit(context.Background(), fixture.command); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
	if !errors.Is(err, ErrValidationRefused) || result.Code != domainvalidation.CodeRepositoryMismatch {
		t.Fatalf("repository race = %#v, %v", result, err)
	}
	run, _ := fixture.store.Run(context.Background(), "run-1")
	if run.Execution.CandidateAuthority.Downstream.Validation != nil || run.Execution.CandidateAuthority.Downstream.CI != nil {
		t.Fatal("replacement repository gained Validation authority")
	}
}

func TestPaginationTotalDriftFailsClosed(t *testing.T) {
	fixture := newFixture(t)
	sha := fixture.store.candidate.CommitSHA
	for index := 0; index < 100; index++ {
		fixture.github.workflowRuns = append(fixture.github.workflowRuns, domainvalidation.WorkflowRun{ID: int64(1_000 + index), WorkflowID: int64(1_000 + index),
			Name: fmt.Sprintf("other-workflow-%d", index), HeadSHA: sha, HeadRepositoryID: 123, CheckSuiteID: int64(2_000 + index),
			Status: "completed", Conclusion: "success", Attempt: 1, StartedAtMillis: 100, UpdatedAtMillis: 200})
	}
	fixture.github.workflowTotalDrift = true
	if _, err := fixture.service.Admit(context.Background(), fixture.command); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
	if !errors.Is(err, ErrValidationRefused) || result.Code != domainvalidation.CodePaginationIncomplete {
		t.Fatalf("pagination drift = %#v, %v", result, err)
	}
}

func TestBaseMovementAndMidScanRaceInvalidateEveryDownstreamAuthority(t *testing.T) {
	for _, test := range []struct {
		name     string
		switchAt uint64
		code     domainvalidation.Code
	}{
		{"already moved", 1, domainvalidation.CodeBaseChanged}, {"mid scan", 2, domainvalidation.CodeBaseRace},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t)
			authority := *fixture.store.run.Execution.CandidateAuthority
			binding := func(id string) *candidate.EvidenceBinding {
				return &candidate.EvidenceBinding{ID: id, CandidateID: authority.CandidateID, CandidateSHA: authority.CandidateSHA, BaseSHA: authority.BaseSHA, Generation: authority.Generation, BindingSHA256: authority.BindingSHA256}
			}
			authority.Downstream.Validation, authority.Downstream.CI, authority.Downstream.Review = binding("old-validation"), binding("old-ci"), binding("old-review")
			authority.Downstream.Ready, authority.Downstream.Integration = binding("old-ready"), binding("old-integration")
			fixture.store.run.Execution.CandidateAuthority = &authority
			review := reviewIntent(t, fixture.store.candidate, authority, fixture.command.CISlotID)
			fixture.store.run.Execution.Review = &review
			fixture.branches.nextBase, fixture.branches.switchAt = strings.Repeat("f", 40), test.switchAt
			if _, err := fixture.service.Admit(context.Background(), fixture.command); err != nil {
				t.Fatal(err)
			}
			result, err := fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
			if !errors.Is(err, ErrValidationInvalidated) || !result.Invalidated || result.Code != test.code {
				t.Fatalf("result = %#v, %v", result, err)
			}
			run, _ := fixture.store.Run(context.Background(), "run-1")
			if run.Execution.Validation == nil || !run.Execution.Validation.Invalidated || run.Execution.Validation.BaseInvalidation == nil ||
				run.Execution.Validation.BaseInvalidation.NewBaseSHA != strings.Repeat("f", 40) || !candidate.DownstreamEmpty(run.Execution.CandidateAuthority.Downstream) ||
				run.Execution.Review != nil || len(run.Execution.ReviewHistory) != 1 || !run.Execution.ReviewHistory[0].Invalidated ||
				run.Execution.Publication != nil || len(run.Execution.PublicationHistory) != 1 || !run.Execution.PublicationHistory[0].Invalidated ||
				run.Execution.PublicationHistory[0].OwnedPullRequest == nil || run.Execution.Budget.Consumption.CICycles != 1 || run.Execution.NeedsYou != nil {
				t.Fatalf("invalidation did not preserve history and clear authority: %#v", run.Execution)
			}
			prior := run.Execution.Validation.BaseInvalidation.PriorAuthority.Downstream
			if prior.Ready == nil || prior.Integration == nil || prior.Publication == nil || prior.Validation == nil || prior.Review == nil || prior.CI == nil {
				t.Fatalf("prior authority incomplete: %#v", prior)
			}
		})
	}
}

func TestBaseInvalidationWaitsForInFlightPublicationAndFeedbackDispatch(t *testing.T) {
	for _, test := range []struct {
		name    string
		install func(*fixture)
	}{
		{"publication", func(fixture *fixture) {
			state := fixture.store.run.Execution.Publication
			state.Ready.Phase, state.Ready.Attempts = publicationdomain.EffectDispatching, 1
			if !publicationdomain.ValidState(*state) || !publicationdomain.DispatchInFlight(*state) {
				t.Fatal("in-flight publication fixture")
			}
		}},
		{"feedback", func(fixture *fixture) {
			state := correctionReadyFeedback(t, fixture.store.run, fixture.store.candidate, fixture.now)
			fixture.store.run.Execution.Feedback = &state
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t)
			if _, err := fixture.service.Admit(context.Background(), fixture.command); err != nil {
				t.Fatal(err)
			}
			test.install(fixture)
			before := cloneRun(fixture.store.run)
			fixture.branches.nextBase, fixture.branches.switchAt = strings.Repeat("f", 40), 1
			result, err := fixture.service.Reconcile(context.Background(), fixture.store.run.ID, fixture.now)
			if !errors.Is(err, ErrValidationDispatching) || !result.WaitingExternal ||
				result.Run.Execution.Validation == nil || result.Run.Execution.Validation.Invalidated ||
				!candidate.DownstreamEmpty(before.Execution.CandidateAuthority.Downstream) &&
					result.Run.Execution.CandidateAuthority.Downstream.Publication == nil {
				t.Fatalf("in-flight %s invalidation = %#v, %v", test.name, result, err)
			}
			if test.name == "publication" && (result.Run.Execution.Publication == nil || len(result.Run.Execution.PublicationHistory) != 0) ||
				test.name == "feedback" && (result.Run.Execution.Feedback == nil || len(result.Run.Execution.FeedbackHistory) != 0) {
				t.Fatalf("in-flight %s state was discarded before observation", test.name)
			}
		})
	}
}

func TestMissingRelevantBaseInvalidatesAuthorityAndRequiresAnExactWake(t *testing.T) {
	fixture := newFixture(t)
	fixture.branches.missing = true
	if _, err := fixture.service.Admit(context.Background(), fixture.command); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
	if !errors.Is(err, ErrValidationInvalidated) || result.Code != domainvalidation.CodeBaseRace {
		t.Fatalf("missing base = %#v, %v", result, err)
	}
	run, _ := fixture.store.Run(context.Background(), "run-1")
	if run.Execution.Validation == nil || run.Execution.Validation.BaseInvalidation == nil ||
		run.Execution.Validation.BaseInvalidation.NewBasePresent || !candidate.DownstreamEmpty(run.Execution.CandidateAuthority.Downstream) ||
		run.Execution.NeedsYou == nil || run.Execution.NeedsYou.WakeCondition != "live_relevant_base_ref_is_present_and_exact" {
		t.Fatalf("missing-base invalidation = %#v", run.Execution)
	}
}

func TestBaseMovementAfterReadyInvalidatesTerminalEvidenceWithoutAnotherCycle(t *testing.T) {
	fixture := newFixture(t)
	if _, err := fixture.service.Admit(context.Background(), fixture.command); err != nil {
		t.Fatal(err)
	}
	if result, err := fixture.service.Reconcile(context.Background(), "run-1", fixture.now); err != nil || result.Outcome != domainvalidation.OutcomePassed {
		t.Fatalf("initial CI = %#v, %v", result, err)
	}
	fixture.store.mu.Lock()
	authority := *fixture.store.run.Execution.CandidateAuthority
	bound := func(id string) *candidate.EvidenceBinding {
		return &candidate.EvidenceBinding{ID: id, CandidateID: authority.CandidateID, CandidateSHA: authority.CandidateSHA,
			BaseSHA: authority.BaseSHA, Generation: authority.Generation, BindingSHA256: authority.BindingSHA256}
	}
	authority.Downstream.Review, authority.Downstream.Ready, authority.Downstream.Integration = bound("review-ready"), bound("ready-ready"), bound("integration-ready")
	fixture.store.run.Execution.CandidateAuthority = &authority
	fixture.store.mu.Unlock()
	fixture.branches.mu.Lock()
	fixture.branches.base = strings.Repeat("f", 40)
	fixture.branches.mu.Unlock()
	result, err := fixture.service.Reconcile(context.Background(), "run-1", fixture.now+1)
	if !errors.Is(err, ErrValidationInvalidated) || result.Code != domainvalidation.CodeBaseChanged {
		t.Fatalf("post-Ready drift = %#v, %v", result, err)
	}
	run, _ := fixture.store.Run(context.Background(), "run-1")
	if !candidate.DownstreamEmpty(run.Execution.CandidateAuthority.Downstream) || run.Execution.Publication != nil ||
		len(run.Execution.PublicationHistory) != 1 || !run.Execution.PublicationHistory[0].Invalidated ||
		run.Execution.Validation == nil || !run.Execution.Validation.Invalidated ||
		run.Execution.Budget.Consumption.CICycles != 1 || len(run.Execution.Budget.Activities) != 1 {
		t.Fatalf("post-Ready invalidation = %#v", run.Execution)
	}
}

func TestResponseLossRestartAndThirtyTwoCoordinatorsConverge(t *testing.T) {
	fixture := newFixture(t)
	if _, err := fixture.service.Admit(context.Background(), fixture.command); err != nil {
		t.Fatal(err)
	}
	fixture.store.mu.Lock()
	fixture.store.loseNext = true
	fixture.store.mu.Unlock()
	_, _ = fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 32)
	for index := 0; index < 32; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Errorf("coordinator: %v", err)
		}
	}
	run, _ := fixture.store.Run(context.Background(), "run-1")
	if run.Execution.Validation == nil || run.Execution.Validation.Phase != domainvalidation.PhasePassed || run.Execution.Budget.Consumption.CICycles != 1 || len(run.Execution.Budget.Activities) != 1 {
		t.Fatalf("did not converge: %#v", run.Execution.Validation)
	}
}

func TestUnchangedFailedCandidateDoesNotRunAgainAndFourCycleBudgetParks(t *testing.T) {
	fixture := newFixture(t)
	fixture.github.workflowRuns[0].Conclusion = "failure"
	if _, err := fixture.service.Admit(context.Background(), fixture.command); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
	if err != nil || result.Outcome != domainvalidation.OutcomeFailed {
		t.Fatalf("failed CI = %#v, %v", result, err)
	}
	run, _ := fixture.store.Run(context.Background(), "run-1")
	command := fixture.command
	command.ExpectedRunVersion = run.Version
	if replay, err := fixture.service.Admit(context.Background(), command); err != nil || !replay.Terminal {
		t.Fatalf("failed replay = %#v, %v", replay, err)
	}
	if again, err := fixture.service.Reconcile(context.Background(), "run-1", fixture.now); err != nil || !again.Terminal {
		t.Fatalf("failed reconcile replay = %#v, %v", again, err)
	}
	run, _ = fixture.store.Run(context.Background(), "run-1")
	if run.Execution.Budget.Consumption.CICycles != 1 || len(run.Execution.Budget.Activities) != 1 {
		t.Fatal("unchanged failed SHA consumed another CI cycle")
	}

	budgetFixture := newFixture(t)
	ledger := budgetFixture.store.run.Execution.Budget
	for index := 0; index < 4; index++ {
		sha := strings.Repeat(string(rune('1'+index)), 40)
		request := runtimebudget.ReserveRequest{ID: fmt.Sprintf("reservation-%d", index), EffectID: fmt.Sprintf("effect-%d", index),
			Activity: runtimebudget.ActivityValidationCycle, LeaseEpoch: 1, PolicyRevision: ledger.Policy.Revision,
			Demand: runtimebudget.Demand{WallTimeMilliseconds: 1}, CandidateSHA: sha}
		var decision runtimebudget.Decision
		var reserveErr error
		ledger, decision, reserveErr = runtimebudget.Reserve(ledger, request, int64(index+1))
		if reserveErr != nil || decision.Disposition != runtimebudget.DispositionAllow {
			t.Fatal(decision, reserveErr)
		}
		observation := runtimebudget.ActivityObservation{ID: fmt.Sprintf("activity-%d", index), ReservationID: request.ID,
			EffectID: request.EffectID, Activity: request.Activity, LeaseEpoch: 1, PolicyRevision: ledger.Policy.Revision,
			ObservedAtMillis: int64(index + 1), CandidateSHA: sha}
		observation.FactHash = runtimebudget.ActivityObservationHash(observation)
		ledger, _, reserveErr = runtimebudget.ApplyActivityObservation(ledger, observation)
		if reserveErr != nil {
			t.Fatal(reserveErr)
		}
	}
	budgetFixture.store.run.Execution.Budget = ledger
	result, err = budgetFixture.service.Admit(context.Background(), budgetFixture.command)
	if !errors.Is(err, ErrValidationRefused) || result.Code != domainvalidation.CodeBudgetExhausted || result.Run.Execution.NeedsYou == nil ||
		result.Run.Execution.NeedsYou.Code != execution.NeedCode(runtimebudget.ReasonCIHard) {
		t.Fatalf("budget result = %#v, %v", result, err)
	}
}
