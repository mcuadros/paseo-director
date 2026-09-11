// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/candidate"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	directdomain "github.com/mcuadros/director-engine/domain/directdelivery"
	"github.com/mcuadros/director-engine/domain/execution"
	feedbackdomain "github.com/mcuadros/director-engine/domain/feedback"
	domainintegration "github.com/mcuadros/director-engine/domain/integration"
	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
	"github.com/mcuadros/director-engine/domain/repository"
	"github.com/mcuadros/director-engine/domain/review"
	"github.com/mcuadros/director-engine/domain/runtimebudget"
	domainvalidation "github.com/mcuadros/director-engine/domain/validation"
	gitport "github.com/mcuadros/director-engine/ports/git"
	githubport "github.com/mcuadros/director-engine/ports/github"
)

const (
	testTaskAgent = "11111111-1111-4111-8111-111111111111"
	testReviewer  = "22222222-2222-4222-8222-222222222222"
	testOwner     = "33333333-3333-4333-8333-333333333333"
	testCoord     = "44444444-4444-4444-8444-444444444444"
)

func cloneRun(value domain.Run) domain.Run {
	encoded, _ := json.Marshal(value)
	var result domain.Run
	_ = json.Unmarshal(encoded, &result)
	return result
}

type memoryStore struct {
	mu       sync.Mutex
	project  domain.Project
	task     domain.Task
	run      domain.Run
	record   domain.Candidate
	commands map[string]domain.CommandResult
}

type testForge struct {
	mu                                    sync.Mutex
	RepositoryID                          int64
	RepositoryNodeID, Owner, Name, Viewer string
	Pulls                                 []publicationdomain.PullRequest
	WorkflowRuns                          []domainvalidation.WorkflowRun
	CheckRuns                             []domainvalidation.CheckRun
	CommitStatuses                        []domainvalidation.CommitStatus
	CombinedStatus                        string
	Feedback                              map[feedbackdomain.Source][]feedbackdomain.Item
	IntegrationCode                       domainintegration.Code
	Mergeable                             bool
	MergeableState                        string
	AtomicExpectedHead                    bool
	DefaultBranch, NextMergeCommitSHA     string
	Merged                                map[int64]domainintegration.ForgeObservation
	LoseMergeResponse                     bool
	MergeDispatches                       uint64
}

func newTestForge() *testForge {
	return &testForge{RepositoryID: 123, RepositoryNodeID: "R_node", Owner: "example", Name: "product", Viewer: "example",
		CombinedStatus: "checks_only_no_statuses", Feedback: map[feedbackdomain.Source][]feedbackdomain.Item{}, IntegrationCode: domainintegration.CodeOK,
		Mergeable: true, MergeableState: "clean", AtomicExpectedHead: true, DefaultBranch: "main", Merged: map[int64]domainintegration.ForgeObservation{}}
}

func (forge *testForge) integrationObservation(request githubport.IntegrationRequest, status domainintegration.ForgeStatus, code domainintegration.Code) domainintegration.ForgeObservation {
	binding := request.Binding
	return domainintegration.SealForgeObservation(domainintegration.ForgeObservation{ID: fmt.Sprintf("forge-%d", request.TaskStoreNowMillis), Status: status, Code: code,
		RepositoryID: forge.RepositoryID, RepositoryNodeID: forge.RepositoryNodeID, Owner: forge.Owner, Name: forge.Name, ViewerLogin: forge.Viewer,
		Authenticated: true, CanMerge: true, TLSVerified: true, RateRemaining: 5000, APIVersion: "2022-11-28", AtomicExpectedHead: forge.AtomicExpectedHead,
		DefaultBranch: forge.DefaultBranch, PullRequestNumber: binding.PullRequestNumber, PullRequestNodeID: binding.PullRequestNodeID,
		HeadSHA: binding.CandidateSHA, HeadRef: binding.Branch, BaseRef: strings.TrimPrefix(binding.BaseRef, "refs/heads/"),
		HeadRepositoryID: forge.RepositoryID, BaseRepositoryID: forge.RepositoryID, HeadOwner: forge.Viewer, AuthorLogin: forge.Viewer,
		MarkerSHA256: binding.MarkerSHA256, MarkerCount: 1,
		ObservedAtMillis: request.TaskStoreNowMillis, MaximumAgeMillis: domainintegration.MaximumObservationAgeMS})
}
func (forge *testForge) ObserveIntegration(_ context.Context, request githubport.IntegrationRequest) (domainintegration.ForgeObservation, error) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	if forge.IntegrationCode != domainintegration.CodeOK {
		status := domainintegration.ForgeInvalid
		if domainintegration.WaitingCode(forge.IntegrationCode) {
			status = domainintegration.ForgeUnavailable
		}
		return forge.integrationObservation(request, status, forge.IntegrationCode), nil
	}
	if value, ok := forge.Merged[request.Binding.PullRequestNumber]; ok {
		value.ObservedAtMillis = request.TaskStoreNowMillis
		value.MaximumAgeMillis = domainintegration.MaximumObservationAgeMS
		return domainintegration.SealForgeObservation(value), nil
	}
	for _, pull := range forge.Pulls {
		if pull.Number == request.Binding.PullRequestNumber {
			value := forge.integrationObservation(request, domainintegration.ForgeReady, domainintegration.CodeOK)
			value.PullRequestNodeID, value.PullRequestState, value.Draft = pull.NodeID, pull.State, pull.Draft
			value.HeadSHA, value.HeadRef, value.BaseRef = pull.HeadSHA, pull.HeadRef, pull.BaseRef
			value.HeadRepositoryID, value.BaseRepositoryID, value.HeadOwner, value.AuthorLogin = pull.HeadRepositoryID, pull.BaseRepositoryID, pull.HeadOwner, pull.AuthorLogin
			value.MarkerSHA256, value.MarkerCount = pull.MarkerSHA256, pull.MarkerCount
			value.Mergeable, value.MergeableState = forge.Mergeable, forge.MergeableState
			sealed := domainintegration.SealForgeObservation(value)
			if !domainintegration.CurrentForgeObservation(sealed, request.Binding, request.TaskStoreNowMillis) {
				code := domainintegration.CodePullRequestMismatch
				if !forge.AtomicExpectedHead {
					code = domainintegration.CodeAtomicHeadUnavailable
				}
				return forge.integrationObservation(request, domainintegration.ForgeInvalid, code), nil
			}
			return sealed, nil
		}
	}
	return forge.integrationObservation(request, domainintegration.ForgeInvalid, domainintegration.CodeNotFound), nil
}
func (forge *testForge) MergeExpectedHead(_ context.Context, request githubport.MergeRequest) (githubport.MergeResult, error) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	for index := range forge.Pulls {
		pull := &forge.Pulls[index]
		if pull.Number != request.Binding.PullRequestNumber {
			continue
		}
		if pull.State != "open" || pull.Draft || pull.HeadSHA != request.Binding.CandidateSHA || !forge.AtomicExpectedHead {
			return githubport.MergeResult{Code: domainintegration.CodeConflict}, nil
		}
		forge.MergeDispatches++
		pull.State = "closed"
		merge := forge.NextMergeCommitSHA
		value := forge.integrationObservation(githubport.IntegrationRequest{Binding: request.Binding, TaskStoreNowMillis: request.TaskStoreNowMillis}, domainintegration.ForgeIntegrated, domainintegration.CodeOK)
		value.PullRequestState, value.Draft, value.HeadSHA, value.HeadRef, value.BaseRef = "closed", false, pull.HeadSHA, pull.HeadRef, pull.BaseRef
		value.HeadRepositoryID, value.BaseRepositoryID, value.HeadOwner, value.AuthorLogin = pull.HeadRepositoryID, pull.BaseRepositoryID, pull.HeadOwner, pull.AuthorLogin
		value.MarkerSHA256, value.MarkerCount = pull.MarkerSHA256, pull.MarkerCount
		value.Merged, value.MergedAtMillis, value.MergeCommitSHA = true, request.TaskStoreNowMillis, merge
		forge.Merged[pull.Number] = domainintegration.SealForgeObservation(value)
		if forge.LoseMergeResponse {
			forge.LoseMergeResponse = false
			return githubport.MergeResult{Handoff: true, Code: domainintegration.CodeResponseUnknown}, nil
		}
		return githubport.MergeResult{Handoff: true, Code: domainintegration.CodeOK}, nil
	}
	return githubport.MergeResult{Code: domainintegration.CodeNotFound}, nil
}
func pageRange(length int, request githubport.CandidatePageRequest) (int, int, uint32, bool) {
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
func (forge *testForge) ObserveChecksRepository(_ context.Context, request githubport.ChecksRepositoryRequest) (domainvalidation.RepositoryObservation, error) {
	return domainvalidation.SealRepositoryObservation(domainvalidation.RepositoryObservation{ID: "checks-repository", Code: domainvalidation.CodeOK, RepositoryID: forge.RepositoryID, RepositoryNodeID: forge.RepositoryNodeID, Owner: forge.Owner, Name: forge.Name, ViewerLogin: forge.Viewer, Authenticated: true, CanReadChecks: true, TLSVerified: true, APIVersion: "2022-11-28", RateRemaining: 5000, ObservedAtMillis: request.TaskStoreNowMillis, MaximumAgeMillis: domainvalidation.MaximumObservationAgeMS}), nil
}
func (forge *testForge) ListWorkflowRuns(_ context.Context, request githubport.CandidatePageRequest) (domainvalidation.WorkflowPage, error) {
	start, end, next, complete := pageRange(len(forge.WorkflowRuns), request)
	return domainvalidation.SealWorkflowPage(domainvalidation.WorkflowPage{ID: fmt.Sprintf("workflows-%d", request.Page), Code: domainvalidation.CodeOK, CandidateSHA: request.CandidateSHA, Page: request.Page, TotalCount: uint32(len(forge.WorkflowRuns)), NextPage: next, Complete: complete, Runs: slicesClone(forge.WorkflowRuns[start:end]), ObservedAtMillis: request.TaskStoreNowMillis, MaximumAgeMillis: domainvalidation.MaximumObservationAgeMS}), nil
}
func (forge *testForge) ListCheckRuns(_ context.Context, request githubport.CandidatePageRequest) (domainvalidation.CheckPage, error) {
	start, end, next, complete := pageRange(len(forge.CheckRuns), request)
	return domainvalidation.SealCheckPage(domainvalidation.CheckPage{ID: fmt.Sprintf("checks-%d", request.Page), Code: domainvalidation.CodeOK, CandidateSHA: request.CandidateSHA, Page: request.Page, TotalCount: uint32(len(forge.CheckRuns)), NextPage: next, Complete: complete, Checks: slicesClone(forge.CheckRuns[start:end]), ObservedAtMillis: request.TaskStoreNowMillis, MaximumAgeMillis: domainvalidation.MaximumObservationAgeMS}), nil
}
func (forge *testForge) ListCommitStatuses(_ context.Context, request githubport.CandidatePageRequest) (domainvalidation.StatusPage, error) {
	start, end, next, complete := pageRange(len(forge.CommitStatuses), request)
	rollup := forge.CombinedStatus
	if len(forge.CommitStatuses) == 0 {
		rollup = "checks_only_no_statuses"
	}
	return domainvalidation.SealStatusPage(domainvalidation.StatusPage{ID: fmt.Sprintf("statuses-%d", request.Page), Code: domainvalidation.CodeOK, CandidateSHA: request.CandidateSHA, CombinedState: rollup, Page: request.Page, TotalCount: uint32(len(forge.CommitStatuses)), NextPage: next, Complete: complete, Statuses: slicesClone(forge.CommitStatuses[start:end]), ObservedAtMillis: request.TaskStoreNowMillis, MaximumAgeMillis: domainvalidation.MaximumObservationAgeMS}), nil
}
func (forge *testForge) ObserveFeedbackPage(_ context.Context, request githubport.FeedbackRequest) (githubport.FeedbackPage, error) {
	values := forge.Feedback[request.Source]
	start := int((request.Page - 1) * request.PageSize)
	if start > len(values) {
		start = len(values)
	}
	end := min(start+int(request.PageSize), len(values))
	page := githubport.FeedbackPage{ID: fmt.Sprintf("feedback-%s-%d", request.Source, request.Page), Code: "ok", Source: request.Source, RepositoryID: request.RepositoryID, RepositoryNodeID: request.RepositoryNodeID, PullRequestNumber: request.PullRequestNumber, CandidateSHA: request.CandidateSHA, BaseSHA: request.BaseSHA, BindingSHA256: request.BindingSHA256, Page: request.Page, ObservedAtMillis: request.TaskStoreNowMillis, MaximumAgeMillis: feedbackdomain.MaximumObservationAge, Items: slicesClone(values[start:end])}
	if end < len(values) {
		page.NextPage = request.Page + 1
	} else {
		page.Complete = true
	}
	return githubport.SealFeedbackPage(page), nil
}

func (store *memoryStore) Project(context.Context, string) (domain.Project, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.project, nil
}
func (store *memoryStore) Task(context.Context, string) (domain.Task, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.task, nil
}
func (store *memoryStore) Run(context.Context, string) (domain.Run, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return cloneRun(store.run), nil
}
func (store *memoryStore) Candidate(context.Context, string) (domain.Candidate, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.record, nil
}
func (store *memoryStore) UpdateRun(_ context.Context, command domain.CommandRequest, next domain.Run, event domain.Event) (domain.CommandResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if prior, ok := store.commands[command.IdempotencyKey]; ok {
		prior.Replay = true
		return prior, nil
	}
	if command.ExpectedVersion != store.run.Version || next.Version != store.run.Version+1 {
		return domain.CommandResult{Outcome: domain.CommandRejectedVersionConflict, ObservedVersion: store.run.Version}, nil
	}
	if next.Execution.Integration != nil && !domainintegration.ValidState(*next.Execution.Integration) {
		return domain.CommandResult{}, errors.New("invalid integration write")
	}
	store.run = cloneRun(next)
	result := domain.CommandResult{Outcome: domain.CommandApplied, ObservedVersion: next.Version, EventID: event.ID}
	store.commands[command.IdempotencyKey] = result
	return result, nil
}

type fakeGit struct {
	mu           sync.Mutex
	code         domainintegration.Code
	base, head   string
	observations uint64
}

func (git *fakeGit) ObserveIntegration(_ context.Context, request gitport.IntegrationRequest) (domainintegration.GitObservation, error) {
	git.mu.Lock()
	defer git.mu.Unlock()
	git.observations++
	if git.code != "" && git.code != domainintegration.CodeOK {
		status := domainintegration.GitInvalid
		if domainintegration.WaitingCode(git.code) {
			status = domainintegration.GitUnavailable
		}
		return domainintegration.SealGitObservation(domainintegration.GitObservation{ID: fmt.Sprintf("git-%d", git.observations), Status: status, Code: git.code,
			RepositoryID: request.Binding.RepositoryID, CanonicalRemote: request.Binding.CanonicalRemote, HeadRef: "refs/heads/" + request.Binding.Branch,
			BaseRef: request.Binding.BaseRef, ObservedAtMillis: request.TaskStoreNowMillis, MaximumAgeMillis: domainintegration.MaximumObservationAgeMS}), nil
	}
	status, base := domainintegration.GitReady, request.Binding.BaseSHA
	value := domainintegration.GitObservation{ID: fmt.Sprintf("git-%d", git.observations), Status: status, Code: domainintegration.CodeOK,
		RepositoryID: request.Binding.RepositoryID, CanonicalRemote: request.Binding.CanonicalRemote,
		HeadRef: "refs/heads/" + request.Binding.Branch, HeadSHA: request.Binding.CandidateSHA, BaseRef: request.Binding.BaseRef,
		BaseSHA: base, DefaultRef: request.Binding.BaseRef, DefaultSHA: base,
		ObservedAtMillis: request.TaskStoreNowMillis, MaximumAgeMillis: domainintegration.MaximumObservationAgeMS}
	if git.head != "" {
		value.HeadSHA = git.head
	}
	if git.base != "" {
		value.BaseSHA, value.DefaultSHA = git.base, git.base
	}
	if request.MergeCommitSHA != "" {
		value.Status, value.BaseSHA, value.DefaultSHA = domainintegration.GitIntegrated, request.MergeCommitSHA, request.MergeCommitSHA
		value.MergeCommitSHA, value.ParentSHAs, value.TreeSHA = request.MergeCommitSHA,
			[]string{request.Binding.BaseSHA, request.Binding.CandidateSHA}, request.Binding.TreeSHA
	}
	if value.HeadSHA != request.Binding.CandidateSHA {
		value.Status, value.Code = domainintegration.GitInvalid, domainintegration.CodeHeadChanged
	}
	if request.MergeCommitSHA == "" && value.BaseSHA != request.Binding.BaseSHA {
		value.Status, value.Code = domainintegration.GitInvalid, domainintegration.CodeBaseChanged
	}
	return domainintegration.SealGitObservation(value), nil
}

type fixture struct {
	store   *memoryStore
	forge   *testForge
	git     *fakeGit
	service *Service
	now     int64
}

func candidateRecord(repositoryHash string) (domain.Candidate, candidate.Authority) {
	claim := candidate.Claim{SchemaVersion: candidate.ClaimSchemaVersion, ID: "candidate-claim-1", ProjectID: "project-1", WorkspaceID: "workspace-1",
		TaskID: "dir-m4.9", RunID: "run-1", ActorID: testTaskAgent, WorktreeID: "worktree-1", Branch: "task/dir-m4.9-final-integration",
		BaseRef: "refs/heads/main", CandidateSHA: strings.Repeat("b", 40), BaseSHA: strings.Repeat("a", 40), LeaseEpoch: 1,
		ExpectedRunVersion: 1, TaskVersion: 3, AcceptanceSHA256: strings.Repeat("1", 64), ConfigurationSHA256: strings.Repeat("2", 64),
		ProfileSHA256: strings.Repeat("3", 64), ContextSHA256: strings.Repeat("4", 64), DecisionsSHA256: strings.Repeat("5", 64), FindingsSHA256: strings.Repeat("6", 64)}
	manifest := candidate.Manifest{SchemaVersion: candidate.ManifestSchemaVersion, CandidateSHA: claim.CandidateSHA, BaseSHA: claim.BaseSHA,
		ParentSHA: claim.BaseSHA, TreeSHA: strings.Repeat("c", 40), DiffSHA256: strings.Repeat("7", 64), ChangedPathsSHA256: strings.Repeat("8", 64),
		RepositoryBindingSHA256: repositoryHash, ClaimSHA256: candidate.ClaimSHA256(claim), ObservationSHA256: strings.Repeat("9", 64),
		AcceptanceSHA256: claim.AcceptanceSHA256, ConfigurationSHA256: claim.ConfigurationSHA256, ProfileSHA256: claim.ProfileSHA256,
		ContextSHA256: claim.ContextSHA256, DecisionsSHA256: claim.DecisionsSHA256, FindingsSHA256: claim.FindingsSHA256,
		GraphPolicySHA256: candidate.GraphPolicySHA256(claim.GraphPolicy)}
	manifest.BindingSHA256 = candidate.ManifestSHA256(manifest)
	record := domain.Candidate{SchemaVersion: domain.CandidateSchemaVersion, ID: "candidate-1", RunID: claim.RunID, Sequence: 1,
		CommitSHA: claim.CandidateSHA, Claim: claim, Manifest: manifest, AdmittedAtMillis: 100}
	return record, candidate.NewAuthority(0, record.ID, claim.Branch, claim.TaskVersion, manifest)
}

func validationState(t *testing.T, record domain.Candidate, authority candidate.Authority, repositoryBinding execution.RepositoryBinding, now int64) (domainvalidation.Policy, domainvalidation.State) {
	t.Helper()
	policy, ok := domainvalidation.NewPolicy(99, "maintained-linux-ci", []domainvalidation.RequiredCheck{{ID: "linux-ci", Kind: domainvalidation.CheckRunKind, Name: "Linux CI", AppID: 15368, AppSlug: "github-actions"}}, 10_000)
	if !ok {
		t.Fatal("validation policy")
	}
	binding := domainvalidation.SealBinding(domainvalidation.Binding{TaskID: "dir-m4.9", RunID: "run-1", CandidateID: record.ID,
		CandidateSHA: record.CommitSHA, BaseSHA: record.Manifest.BaseSHA, TreeSHA: record.Manifest.TreeSHA, ManifestSHA256: record.Manifest.BindingSHA256,
		CandidateGeneration: authority.Generation, CISlotID: "ci-slot-1", BaseRef: record.Claim.BaseRef,
		RepositoryBindingSHA256: record.Manifest.RepositoryBindingSHA256, CanonicalRemote: repositoryBinding.CanonicalRemote,
		GitHubRepositoryID: 123, GitHubRepositoryNodeID: "R_node", RepositoryOwner: "example", RepositoryName: "product",
		ViewerLogin: "example", PolicySHA256: policy.SHA256})
	state, ok := domainvalidation.NewState(binding, policy, 1)
	if !ok {
		t.Fatal("validation state")
	}
	repositoryBefore := domainvalidation.SealRepositoryObservation(domainvalidation.RepositoryObservation{ID: "repository-before", Code: domainvalidation.CodeOK,
		RepositoryID: 123, RepositoryNodeID: "R_node", Owner: "example", Name: "product", ViewerLogin: "example", Authenticated: true,
		CanReadChecks: true, TLSVerified: true, APIVersion: "2022-11-28", RateRemaining: 100, ObservedAtMillis: now, MaximumAgeMillis: domainvalidation.MaximumObservationAgeMS})
	repositoryAfter := repositoryBefore
	repositoryAfter.ID = "repository-after"
	repositoryAfter = domainvalidation.SealRepositoryObservation(repositoryAfter)
	workflows := domainvalidation.SealWorkflowScan(domainvalidation.WorkflowScan{Pages: 1, TotalCount: 1, Runs: []domainvalidation.WorkflowRun{{ID: 500,
		WorkflowID: 99, Name: "maintained-linux-ci", HeadSHA: record.CommitSHA, HeadRepositoryID: 123, CheckSuiteID: 700,
		Status: "completed", Conclusion: "success", Attempt: 1, StartedAtMillis: 100, UpdatedAtMillis: 200}}})
	checks := domainvalidation.SealCheckScan(domainvalidation.CheckScan{Pages: 1, TotalCount: 1, Checks: []domainvalidation.CheckRun{{ID: 600,
		Name: "Linux CI", HeadSHA: record.CommitSHA, SuiteID: 700, SuiteHeadSHA: record.CommitSHA, AppID: 15368, AppSlug: "github-actions",
		Status: "completed", Conclusion: "success", DetailsURLSHA256: strings.Repeat("d", 64), StartedAtMillis: 100, CompletedAtMillis: 200}}})
	statuses := domainvalidation.SealStatusScan(domainvalidation.StatusScan{Pages: 1, CombinedState: "checks_only_no_statuses", Statuses: []domainvalidation.CommitStatus{}})
	baseFact := strings.Repeat("e", 64)
	decision := domainvalidation.Evaluate(binding, policy, workflows, checks, statuses,
		domainvalidation.RepositoryObservationSHA256(repositoryBefore), domainvalidation.RepositoryObservationSHA256(repositoryAfter), baseFact, baseFact, now)
	if decision.Outcome != domainvalidation.OutcomePassed || decision.Evidence == nil {
		t.Fatalf("validation decision = %#v", decision)
	}
	state.Repository, state.RepositoryAfter, state.Workflows, state.Checks, state.Statuses = &repositoryBefore, &repositoryAfter, &workflows, &checks, &statuses
	state.BaseBeforeSHA256, state.BaseAfterSHA256, state.Evidence, state.Phase, state.Code = baseFact, baseFact, decision.Evidence, domainvalidation.PhasePassed, domainvalidation.CodeOK
	if !domainvalidation.ValidState(state) {
		t.Fatalf("validation invalid: %#v", state)
	}
	return policy, state
}

func approvedReview(t *testing.T, record domain.Candidate, authority candidate.Authority, validation domainvalidation.State) review.State {
	t.Helper()
	binding := review.SealBinding(review.Binding{TaskID: "dir-m4.9", RunID: "run-1", CandidateID: record.ID, CandidateSHA: record.CommitSHA,
		BaseSHA: record.Manifest.BaseSHA, TreeSHA: record.Manifest.TreeSHA, ManifestSHA256: record.Manifest.BindingSHA256,
		AcceptanceSHA256: record.Manifest.AcceptanceSHA256, ConfigurationSHA256: record.Manifest.ConfigurationSHA256,
		ProfileSHA256: record.Manifest.ProfileSHA256, ContextSHA256: record.Manifest.ContextSHA256, DecisionsSHA256: record.Manifest.DecisionsSHA256,
		FindingsSHA256: record.Manifest.FindingsSHA256, CandidateGeneration: authority.Generation, CISlotID: validation.Binding.CISlotID,
		TaskAgentUUID: testTaskAgent, CoordinatorUUID: testCoord, ReviewOwnerUUID: testOwner})
	state, ok := review.NewState(binding, []string{"criterion-integration"}, []review.Probe{}, review.ProfileDecision{Admitted: true, Code: "reviewer_profile_admitted"})
	if !ok {
		t.Fatal("review state")
	}
	state.SourcePath, state.PrimaryPath, state.ReviewerRoot, state.CheckoutPath = "/srv/source", "/srv/worktree", "/srv/reviewers", "/srv/reviewers/candidate"
	state.PrimaryHeadSHA = record.CommitSHA
	checkout := review.SealCheckoutEvidence(review.CheckoutEvidence{CheckoutID: state.ReviewKey, OwnerUUID: testOwner, CandidateSHA: record.CommitSHA,
		TreeSHA: record.Manifest.TreeSHA, Detached: true, Clean: true, PrimaryDistinct: true, SourceUnchanged: true})
	state.CheckoutEvidence = &checkout
	state.Checkout.Phase, state.Checkout.ExternalID, state.Checkout.FactSHA256 = review.EffectComplete, state.ReviewKey, checkout.FactSHA256
	state.Workspace.Phase, state.Workspace.ExternalID, state.Workspace.FactSHA256 = review.EffectComplete, "review-workspace", strings.Repeat("1", 64)
	state.ReviewerUUID, state.ReviewerSessionSHA256, state.RegisteredAt, state.RegistrationSHA256 = testReviewer, strings.Repeat("2", 64), "2026-09-11T00:00:00Z", strings.Repeat("3", 64)
	state.Bootstrap.Phase, state.Bootstrap.ExternalID, state.Bootstrap.FactSHA256 = review.EffectComplete, testReviewer, strings.Repeat("4", 64)
	ci, ok := domainvalidation.ReviewObservation(validation)
	if !ok {
		t.Fatal("review CI")
	}
	state.CIObservation = &ci
	state.PromptSHA256 = strings.Repeat("5", 64)
	state.Prompt.Phase, state.Prompt.ExternalID, state.Prompt.FactSHA256 = review.EffectComplete, testReviewer, strings.Repeat("6", 64)
	evidence := review.Evidence{SchemaVersion: review.EvidenceSchemaVersion, ID: "review-evidence-1", ReviewKey: state.ReviewKey,
		BindingSHA256: binding.BindingSHA256, ReviewerUUID: testReviewer, HarnessReviewerUUID: testReviewer,
		CIObservationSHA256: ci.SHA256, CheckoutFactSHA256: checkout.FactSHA256, Verdict: review.VerdictApproveCandidate,
		ClaimSHA256: strings.Repeat("7", 64), ObservedAtMillis: 1_002}
	state.Evidence, state.VerdictDurablyObserved = &evidence, true
	if !review.ValidState(state) {
		t.Fatalf("review invalid: %#v", state)
	}
	return state
}

func readyPublication(t *testing.T, record domain.Candidate, authority candidate.Authority, repositoryBinding execution.RepositoryBinding, reviewState review.State, validation domainvalidation.State) (publicationdomain.Policy, publicationdomain.State) {
	t.Helper()
	policy := publicationdomain.NewPolicy("pull_request", true, nil)
	binding := publicationdomain.SealBinding(publicationdomain.Binding{TaskID: "dir-m4.9", RunID: "run-1", CandidateID: record.ID,
		CandidateSHA: record.CommitSHA, BaseSHA: record.Manifest.BaseSHA, TreeSHA: record.Manifest.TreeSHA, ManifestSHA256: record.Manifest.BindingSHA256,
		CandidateGeneration: authority.Generation, TaskVersion: 3, Branch: record.Claim.Branch, BaseRef: record.Claim.BaseRef,
		RepositoryBindingSHA256: record.Manifest.RepositoryBindingSHA256, CanonicalRemote: repositoryBinding.CanonicalRemote,
		GitHubRepositoryID: 123, GitHubRepositoryNodeID: "R_node", RepositoryOwner: "example", RepositoryName: "product", HeadOwner: "example",
		OwnershipSHA256: strings.Repeat("8", 64), PolicySHA256: policy.SHA256})
	template, ok := publicationdomain.RenderTemplate(binding, policy, "Implement manual and automatic final integration", "approve_candidate", reviewState.Evidence.ID,
		"passed", validation.Evidence.ID, nil)
	if !ok {
		t.Fatal("template")
	}
	state, ok := publicationdomain.NewState(binding, policy, template, "", nil)
	if !ok {
		t.Fatal("publication state")
	}
	state.Push.Phase, state.PullRequest.Phase, state.Metadata.Phase, state.Ready.Phase = publicationdomain.EffectComplete, publicationdomain.EffectComplete, publicationdomain.EffectComplete, publicationdomain.EffectComplete
	state.OwnedPullRequest = &publicationdomain.OwnedPullRequest{Number: 7, NodeID: "PR_node_7", URL: "https://github.com/example/product/pull/7",
		MarkerSHA256: publicationdomain.DigestText(publicationdomain.Marker(binding)), HeadSHA: record.CommitSHA, Draft: false}
	state.Evidence = publicationdomain.EvidenceFor(state, true, 1_100)
	if state.Evidence == nil || !publicationdomain.ValidState(state) {
		t.Fatalf("publication invalid: %#v", state)
	}
	return policy, state
}

func completedCILedger(t *testing.T, revision, candidateSHA string) runtimebudget.Ledger {
	t.Helper()
	policy := runtimebudget.NewPolicy(revision, 1_000_000, 100_000, 32, 0, 4)
	ledger, err := runtimebudget.NewLedger(policy, 0)
	if err != nil {
		t.Fatal(err)
	}
	request := runtimebudget.ReserveRequest{ID: "ci-reservation-1", EffectID: "ci-effect-1", Activity: runtimebudget.ActivityValidationCycle,
		LeaseEpoch: 1, PolicyRevision: revision, Demand: runtimebudget.Demand{WallTimeMilliseconds: 10_000}, CandidateSHA: candidateSHA}
	ledger, decision, err := runtimebudget.Reserve(ledger, request, 100)
	if err != nil || decision.Disposition != runtimebudget.DispositionAllow {
		t.Fatalf("reserve = %#v %v", decision, err)
	}
	observation := runtimebudget.ActivityObservation{ID: "ci-activity-1", ReservationID: request.ID, EffectID: request.EffectID,
		Activity: request.Activity, LeaseEpoch: 1, PolicyRevision: revision, ObservedAtMillis: 200, CandidateSHA: candidateSHA}
	observation.FactHash = runtimebudget.ActivityObservationHash(observation)
	ledger, _, err = runtimebudget.ApplyActivityObservation(ledger, observation)
	if err != nil {
		t.Fatal(err)
	}
	return ledger
}

func newFixture(t *testing.T, mode domainintegration.Mode) *fixture {
	t.Helper()
	identity, err := repository.CanonicalRemote("https://github.com/example/product")
	if err != nil {
		t.Fatal(err)
	}
	repositoryBinding := execution.RepositoryBinding{RepositoryID: identity.ID, RepositoryKey: identity.Key, CanonicalRemote: identity.Canonical,
		SourcePath: "/srv/source", SourceDevice: 1, SourceInode: 2, GitCommonDirectory: "/srv/source/.git", GitCommonDevice: 1,
		GitCommonInode: 3, WorktreePath: "/srv/worktree", Branch: "task/dir-m4.9-final-integration", BaseSHA: strings.Repeat("a", 40)}
	repositoryHash := execution.RepositoryBindingSHA256(repositoryBinding)
	record, authority := candidateRecord(repositoryHash)
	validationPolicy, validation := validationState(t, record, authority, repositoryBinding, 2_000)
	reviewState := approvedReview(t, record, authority, validation)
	publicationPolicy, publication := readyPublication(t, record, authority, repositoryBinding, reviewState, validation)
	authority.Downstream.Validation = &candidate.EvidenceBinding{ID: validation.Evidence.ID, CandidateID: authority.CandidateID, CandidateSHA: authority.CandidateSHA, BaseSHA: authority.BaseSHA, Generation: authority.Generation, BindingSHA256: authority.BindingSHA256}
	copyCI := *authority.Downstream.Validation
	authority.Downstream.CI = &copyCI
	authority.Downstream.Review = &candidate.EvidenceBinding{ID: reviewState.Evidence.ID, CandidateID: authority.CandidateID, CandidateSHA: authority.CandidateSHA, BaseSHA: authority.BaseSHA, Generation: authority.Generation, BindingSHA256: authority.BindingSHA256}
	authority.Downstream.Publication = &candidate.EvidenceBinding{ID: publication.Evidence.ID, CandidateID: authority.CandidateID, CandidateSHA: authority.CandidateSHA, BaseSHA: authority.BaseSHA, Generation: authority.Generation, BindingSHA256: authority.BindingSHA256}
	integrationPolicy, ok := domainintegration.NewPolicy(mode, record.Manifest.ConfigurationSHA256)
	if !ok {
		t.Fatal("integration policy")
	}
	task := domain.Task{ID: "dir-m4.9", ProjectID: "project-1", Key: "DIR-M4.9", Title: "Implement manual and automatic final integration",
		Objective: "Integrate the exact Candidate", AcceptanceCriteria: "Never merge twice", WorkspaceIDs: []string{"workspace-1"}, Version: 3}
	run := domain.Run{ID: "run-1", TaskID: task.ID, Number: 1, BaseSHA: record.Manifest.BaseSHA, CurrentCandidateID: record.ID, Version: 5,
		Execution: execution.State{SchemaVersion: execution.SchemaVersion, Scope: execution.Scope{ProjectID: task.ProjectID, WorkspaceID: "workspace-1", TaskID: task.ID, RunID: "run-1"},
			LeaseBinding: execution.LeaseBinding{HolderInstance: "engine-1", HolderProcessIdentity: "process-1", Epoch: 1}, RepositoryBinding: repositoryBinding,
			RepositoryBindingHash: repositoryHash, Branch: record.Claim.Branch, BaseRef: record.Claim.BaseRef, DeliveryMode: domainconfig.DeliveryPullRequest,
			PublicationPolicy: &publicationPolicy, ValidationPolicy: &validationPolicy, IntegrationPolicy: &integrationPolicy,
			CandidateAuthority: &authority, Publication: &publication, Validation: &validation, Review: &reviewState,
			Budget: completedCILedger(t, record.Manifest.ConfigurationSHA256, record.CommitSHA)}}
	store := &memoryStore{project: domain.Project{ID: task.ProjectID, State: "active", Lease: &domain.ProjectLease{HolderInstance: "engine-1",
		HolderProcessIdentity: "process-1", Epoch: 1, AcquiredAtMillis: 1, RenewedAtMillis: 1, ExpiresAtMillis: 100_000, DispatchAllowed: true}},
		task: task, run: run, record: record, commands: map[string]domain.CommandResult{}}
	forge := newTestForge()
	forge.NextMergeCommitSHA = strings.Repeat("d", 40)
	forge.Pulls = []publicationdomain.PullRequest{{Number: 7, NodeID: "PR_node_7", URL: "https://github.com/example/product/pull/7", State: "open", Draft: false,
		HeadSHA: record.CommitSHA, HeadRef: record.Claim.Branch, BaseRef: "main", HeadOwner: "example", HeadRepositoryID: 123, BaseRepositoryID: 123,
		AuthorLogin: "example", CreatedByViewer: true, MarkerSHA256: publication.OwnedPullRequest.MarkerSHA256, MarkerCount: 1,
		TitleSHA256: publication.Template.SHA256, BodySHA256: publication.Template.SHA256, UpdatedAtMillis: 1_100}}
	forge.WorkflowRuns = slicesClone(validation.Workflows.Runs)
	forge.CheckRuns = slicesClone(validation.Checks.Checks)
	forge.CommitStatuses = []domainvalidation.CommitStatus{}
	git := &fakeGit{}
	service, err := NewService(store, forge, forge, forge, git)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{store: store, forge: forge, git: git, service: service, now: 2_000}
}

func slicesClone[T any](values []T) []T { return append([]T(nil), values...) }

func TestAutomaticIntegrationConvergesAfterResponseLossAndNeverMergesTwice(t *testing.T) {
	fixture := newFixture(t, domainintegration.ModeAutomatic)
	fixture.forge.LoseMergeResponse = true
	admitted, err := fixture.service.Admit(context.Background(), AdmitCommand{RunID: "run-1", ExpectedRunVersion: 5, LeaseEpoch: 1, NowMillis: fixture.now})
	if err != nil || !admitted.Ready || admitted.WaitingHuman {
		t.Fatalf("admit = %#v, %v", admitted, err)
	}
	result, err := fixture.service.Step(context.Background(), StepCommand{RunID: "run-1", ExpectedRunVersion: admitted.Run.Version, LeaseEpoch: 1, NowMillis: fixture.now})
	if err != nil || !result.Integrated || result.MergeCommitSHA != strings.Repeat("d", 40) || fixture.forge.MergeDispatches != 1 {
		t.Fatalf("step = %#v, %v dispatches=%d", result, err, fixture.forge.MergeDispatches)
	}
	replay, err := fixture.service.Step(context.Background(), StepCommand{RunID: "run-1", ExpectedRunVersion: result.Run.Version, LeaseEpoch: 1, NowMillis: fixture.now})
	if err != nil || !replay.Integrated || fixture.forge.MergeDispatches != 1 {
		t.Fatalf("replay = %#v, %v dispatches=%d", replay, err, fixture.forge.MergeDispatches)
	}
}

func TestManualIntegrationPersistsReadyUntilExactServerAction(t *testing.T) {
	fixture := newFixture(t, domainintegration.ModeManual)
	admitted, err := fixture.service.Admit(context.Background(), AdmitCommand{RunID: "run-1", ExpectedRunVersion: 5, LeaseEpoch: 1, NowMillis: fixture.now})
	if err != nil || !admitted.Ready || !admitted.WaitingHuman || fixture.forge.MergeDispatches != 0 {
		t.Fatalf("admit = %#v %v", admitted, err)
	}
	waiting, err := fixture.service.Step(context.Background(), StepCommand{RunID: "run-1", ExpectedRunVersion: admitted.Run.Version, LeaseEpoch: 1, NowMillis: fixture.now})
	if err != nil || !waiting.WaitingHuman || fixture.forge.MergeDispatches != 0 {
		t.Fatalf("waiting = %#v %v", waiting, err)
	}
	binding := waiting.Run.Execution.Integration.Binding
	authorization := domainintegration.SealAuthorization(domainintegration.HumanAuthorization{ID: "human-integration-1", ActorKind: "human",
		ActorSource: domainintegration.AuthorizationActorSource, Authenticated: true, ActorID: "owner@example.invalid", SessionID: "session-1",
		DecisionID: "decision-1", Action: "integrate_pull_request", BindingSHA256: binding.SHA256, CandidateSHA: binding.CandidateSHA,
		BaseSHA: binding.BaseSHA, PullRequestNumber: binding.PullRequestNumber, PolicySHA256: binding.PolicySHA256, AuthorizedAtMillis: fixture.now})
	if _, err := fixture.service.AuthorizeManual(context.Background(), AuthorizeManualCommand{RunID: "run-1", ExpectedRunVersion: waiting.Run.Version,
		LeaseEpoch: 1, Actor: AuthenticatedHumanActor{Kind: "human", ID: "other", SessionID: "session-1", Source: "server", Authenticated: true},
		Authorization: authorization, NowMillis: fixture.now}); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("wrong actor error = %v", err)
	}
	authorized, err := fixture.service.AuthorizeManual(context.Background(), AuthorizeManualCommand{RunID: "run-1", ExpectedRunVersion: waiting.Run.Version,
		LeaseEpoch: 1, Actor: AuthenticatedHumanActor{Kind: "human", ID: "owner@example.invalid", SessionID: "session-1", Source: "server", Authenticated: true},
		Authorization: authorization, NowMillis: fixture.now})
	if err != nil || !authorized.Ready || fixture.forge.MergeDispatches != 0 {
		t.Fatalf("authorized = %#v %v", authorized, err)
	}
	result, err := fixture.service.Step(context.Background(), StepCommand{RunID: "run-1", ExpectedRunVersion: authorized.Run.Version, LeaseEpoch: 1, NowMillis: fixture.now})
	if err != nil || !result.Integrated || fixture.forge.MergeDispatches != 1 {
		t.Fatalf("integration = %#v %v", result, err)
	}
}

func TestThirtyTwoCoordinatorsAndRestartFrontiersConvergeOnOneMerge(t *testing.T) {
	fixture := newFixture(t, domainintegration.ModeAutomatic)
	admitted, err := fixture.service.Admit(context.Background(), AdmitCommand{RunID: "run-1", ExpectedRunVersion: 5, LeaseEpoch: 1, NowMillis: fixture.now})
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	wait.Add(32)
	for range 32 {
		go func() {
			defer wait.Done()
			_, _ = fixture.service.Step(context.Background(), StepCommand{RunID: "run-1", ExpectedRunVersion: admitted.Run.Version, LeaseEpoch: 1, NowMillis: fixture.now})
		}()
	}
	wait.Wait()
	run, _ := fixture.store.Run(context.Background(), "run-1")
	if run.Execution.Integration.Phase != domainintegration.PhaseComplete || fixture.forge.MergeDispatches != 1 {
		t.Fatalf("run=%#v dispatches=%d", run.Execution.Integration, fixture.forge.MergeDispatches)
	}
	// Restart at a durable dispatch frontier after the forge already merged.
	restart := newFixture(t, domainintegration.ModeAutomatic)
	admitted, _ = restart.service.Admit(context.Background(), AdmitCommand{RunID: "run-1", ExpectedRunVersion: 5, LeaseEpoch: 1, NowMillis: restart.now})
	state := *admitted.Run.Execution.Integration
	readyObservation := restart.service.observe(context.Background(), current{project: restart.store.project, task: restart.store.task, run: admitted.Run, record: restart.store.record}, state, restart.now)
	state, _ = domainintegration.RecordObservation(state, readyObservation, restart.now)
	state, _ = domainintegration.BeginDispatch(state)
	restart.store.run = admitted.Run
	restart.store.run.Execution.Integration = &state
	restart.store.run.Version++
	binding := state.Binding
	request := githubMergeRequest(binding, readyObservation.ForgeAfter, restart.now)
	_, _ = restart.forge.MergeExpectedHead(context.Background(), request)
	restarted, _ := NewService(restart.store, restart.forge, restart.forge, restart.forge, restart.git)
	result, err := restarted.Step(context.Background(), StepCommand{RunID: "run-1", ExpectedRunVersion: restart.store.run.Version, LeaseEpoch: 1, NowMillis: restart.now})
	if err != nil || !result.Integrated || restart.forge.MergeDispatches != 1 {
		t.Fatalf("restart = %#v %v dispatches=%d", result, err, restart.forge.MergeDispatches)
	}
}

func TestRestartAtEveryIntentDispatchAndObserveFrontier(t *testing.T) {
	for _, frontier := range []string{"intent", "dispatching", "observation_required", "observed_integrated"} {
		t.Run(frontier, func(t *testing.T) {
			fixture := newFixture(t, domainintegration.ModeAutomatic)
			admitted, err := fixture.service.Admit(context.Background(), AdmitCommand{RunID: "run-1", ExpectedRunVersion: 5, LeaseEpoch: 1, NowMillis: fixture.now})
			if err != nil {
				t.Fatal(err)
			}
			state := *admitted.Run.Execution.Integration
			if frontier != "intent" {
				value := current{project: fixture.store.project, task: fixture.store.task, run: admitted.Run, record: fixture.store.record}
				ready := fixture.service.observe(context.Background(), value, state, fixture.now)
				state, _ = domainintegration.RecordObservation(state, ready, fixture.now)
				state, _ = domainintegration.BeginDispatch(state)
				_, _ = fixture.forge.MergeExpectedHead(context.Background(), githubMergeRequest(state.Binding, ready.ForgeAfter, fixture.now))
				if frontier != "dispatching" {
					state, _ = domainintegration.RequireObservation(state)
				}
				if frontier == "observed_integrated" {
					value.run.Execution.Integration = &state
					post := fixture.service.observe(context.Background(), value, state, fixture.now)
					state, _ = domainintegration.RecordObservation(state, post, fixture.now)
				}
			}
			fixture.store.run = admitted.Run
			fixture.store.run.Execution.Integration = &state
			fixture.store.run.Version++
			restarted, _ := NewService(fixture.store, fixture.forge, fixture.forge, fixture.forge, fixture.git)
			result, err := restarted.Step(context.Background(), StepCommand{RunID: "run-1", ExpectedRunVersion: fixture.store.run.Version, LeaseEpoch: 1, NowMillis: fixture.now})
			if err != nil || !result.Integrated || fixture.forge.MergeDispatches != 1 {
				t.Fatalf("frontier result = %#v, %v, dispatches=%d", result, err, fixture.forge.MergeDispatches)
			}
		})
	}
}

func githubMergeRequest(binding domainintegration.Binding, observation domainintegration.ForgeObservation, now int64) githubport.MergeRequest {
	return githubport.MergeRequest{Binding: binding, Attempt: 1, ExpectedObservation: observation, TaskStoreNowMillis: now}
}

func TestMovedHeadBaseFeedbackChecksAndProviderErrorsRefuseMerge(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*fixture)
		waiting bool
	}{
		{"head moved", func(f *fixture) { f.forge.Pulls[0].HeadSHA = strings.Repeat("e", 40) }, false},
		{"ownership marker changed", func(f *fixture) { f.forge.Pulls[0].MarkerSHA256 = strings.Repeat("e", 64) }, false},
		{"base moved", func(f *fixture) { f.git.base = strings.Repeat("e", 40) }, false},
		{"check failed", func(f *fixture) { f.forge.CheckRuns[0].Conclusion = "failure" }, false},
		{"feedback arrived", func(f *fixture) {
			f.forge.Feedback[feedbackdomain.SourceGitHubReview] = []feedbackdomain.Item{{Source: feedbackdomain.SourceGitHubReview,
				ExternalID: "review-feedback-1", RevisionID: "revision-1", Actor: feedbackdomain.Actor{Kind: feedbackdomain.ActorHuman, ID: "U_1", Login: "reviewer", Authenticated: true, Attestation: feedbackdomain.AttestationGitHubUser},
				Kind: feedbackdomain.KindChangesRequested, CandidateSHA: strings.Repeat("b", 40), BaseSHA: strings.Repeat("a", 40), ContextSHA256: strings.Repeat("f", 64), Body: "Please correct this", Actionable: true, Severity: "P1", CreatedAtMillis: 1_000, UpdatedAtMillis: 1_001}}
		}, false},
		{"rate limited", func(f *fixture) { f.forge.IntegrationCode = domainintegration.CodeRateLimited }, true},
		{"tls unavailable", func(f *fixture) { f.forge.IntegrationCode = domainintegration.CodeTLS }, true},
		{"provider timeout", func(f *fixture) { f.forge.IntegrationCode = domainintegration.CodeUnavailable }, true},
		{"provider server error", func(f *fixture) { f.forge.IntegrationCode = domainintegration.CodeServer }, true},
		{"atomic primitive missing", func(f *fixture) { f.forge.AtomicExpectedHead = false }, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t, domainintegration.ModeAutomatic)
			admitted, err := fixture.service.Admit(context.Background(), AdmitCommand{RunID: "run-1", ExpectedRunVersion: 5, LeaseEpoch: 1, NowMillis: fixture.now})
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(fixture)
			result, _ := fixture.service.Step(context.Background(), StepCommand{RunID: "run-1", ExpectedRunVersion: admitted.Run.Version, LeaseEpoch: 1, NowMillis: fixture.now})
			if fixture.forge.MergeDispatches != 0 {
				t.Fatalf("merge dispatched: %#v", result)
			}
			if test.waiting && !result.WaitingExternal {
				t.Fatalf("not waiting: %#v", result)
			}
			if !test.waiting && result.Run.Execution.Integration.Phase != domainintegration.PhaseInvalidated && result.Run.Execution.Integration.Phase != domainintegration.PhaseNeedsYou {
				t.Fatalf("not refused: %#v", result.Run.Execution.Integration)
			}
			if result.Run.Execution.Integration.Phase == domainintegration.PhaseInvalidated && result.Run.Execution.NeedsYou != nil {
				t.Fatalf("machine-revalidatable drift became human-only: %#v", result.Run.Execution.NeedsYou)
			}
		})
	}
}

func TestStaleLeaseRunCASDirectConflictAndCIBudgetRefuseBeforeObservation(t *testing.T) {
	fixture := newFixture(t, domainintegration.ModeAutomatic)
	fixture.store.project.Lease.Epoch = 2
	if _, err := fixture.service.Admit(context.Background(), AdmitCommand{RunID: "run-1", ExpectedRunVersion: 5, LeaseEpoch: 1, NowMillis: fixture.now}); !errors.Is(err, ErrNotReady) {
		t.Fatalf("lease error = %v", err)
	}
	if fixture.git.observations != 0 || fixture.forge.MergeDispatches != 0 {
		t.Fatal("stale lease reached external gate")
	}
	fixture = newFixture(t, domainintegration.ModeAutomatic)
	fixture.store.run.Version++
	if _, err := fixture.service.Admit(context.Background(), AdmitCommand{RunID: "run-1", ExpectedRunVersion: 5, LeaseEpoch: 1, NowMillis: fixture.now}); !errors.Is(err, ErrNotReady) {
		t.Fatalf("CAS error = %v", err)
	}
	fixture = newFixture(t, domainintegration.ModeAutomatic)
	fixture.store.run.Execution.DirectDelivery = &directdomain.State{}
	if _, err := fixture.service.Admit(context.Background(), AdmitCommand{RunID: "run-1", ExpectedRunVersion: 5, LeaseEpoch: 1, NowMillis: fixture.now}); !errors.Is(err, ErrNotReady) {
		t.Fatalf("direct conflict = %v", err)
	}
	fixture = newFixture(t, domainintegration.ModeAutomatic)
	fixture.store.run.Execution.Budget.Consumption.CICycles = 5
	if _, err := fixture.service.Admit(context.Background(), AdmitCommand{RunID: "run-1", ExpectedRunVersion: 5, LeaseEpoch: 1, NowMillis: fixture.now}); !errors.Is(err, ErrNotReady) {
		t.Fatalf("CI budget = %v", err)
	}
}
