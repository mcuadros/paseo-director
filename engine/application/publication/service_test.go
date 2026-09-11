// SPDX-License-Identifier: Apache-2.0

package publication

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/candidate"
	domaincorrection "github.com/mcuadros/director-engine/domain/correction"
	"github.com/mcuadros/director-engine/domain/execution"
	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
	domainreview "github.com/mcuadros/director-engine/domain/review"
	gitport "github.com/mcuadros/director-engine/ports/git"
	githubport "github.com/mcuadros/director-engine/ports/github"
)

const (
	testTaskAgent    = "11111111-1111-4111-8111-111111111111"
	testCoordinator  = "22222222-2222-4222-8222-222222222222"
	testReviewOwner  = "33333333-3333-4333-8333-333333333333"
	testRepositoryID = int64(1358627520)
)

type memoryStore struct {
	mu         sync.Mutex
	project    domain.Project
	workspace  domain.Workspace
	task       domain.Task
	run        domain.Run
	candidates map[string]domain.Candidate
	commands   map[string]domain.CommandResult
}

type testBranches struct {
	mu             sync.Mutex
	remote         string
	heads          map[string]string
	observations   uint64
	pushDispatches uint64
}

func newTestBranches(remote string) *testBranches {
	return &testBranches{remote: remote, heads: map[string]string{}}
}

func (branches *testBranches) ObserveRemoteRef(_ context.Context, request gitport.RemoteRefRequest) (publicationdomain.RefObservation, error) {
	branches.mu.Lock()
	defer branches.mu.Unlock()
	branches.observations++
	oid, exists := branches.heads[request.Branch]
	return publicationdomain.SealRefObservation(publicationdomain.RefObservation{ID: fmt.Sprintf("test-ref-%d", request.TaskStoreNowMillis),
		Code: publicationdomain.CodeOK, Ref: "refs/heads/" + request.Branch, Exists: exists, OID: oid,
		RemoteCanonical: branches.remote, ObservedAtMillis: request.TaskStoreNowMillis,
		MaximumAgeMillis: publicationdomain.MaximumObservationAgeMS}), nil
}

func (branches *testBranches) PushExact(_ context.Context, request gitport.PushRequest) (gitport.PushResult, error) {
	branches.mu.Lock()
	defer branches.mu.Unlock()
	branches.pushDispatches++
	current := branches.heads[request.Branch]
	if current != request.ExpectedRemoteOID {
		return gitport.PushResult{Handoff: true, Code: publicationdomain.CodeRefChanged}, nil
	}
	branches.heads[request.Branch] = request.CandidateSHA
	return gitport.PushResult{Handoff: true, Code: publicationdomain.CodeOK}, nil
}

func (branches *testBranches) Set(branch, oid string) {
	branches.mu.Lock()
	defer branches.mu.Unlock()
	if oid == "" {
		delete(branches.heads, branch)
	} else {
		branches.heads[branch] = oid
	}
}

func (branches *testBranches) Snapshot(branch string) (string, uint64) {
	branches.mu.Lock()
	defer branches.mu.Unlock()
	return branches.heads[branch], branches.pushDispatches
}

func (branches *testBranches) ObservationCount() uint64 {
	branches.mu.Lock()
	defer branches.mu.Unlock()
	return branches.observations
}

type testGitHub struct {
	mu               sync.Mutex
	repositoryID     int64
	repositoryNodeID string
	owner            string
	name             string
	viewer           string
	RepositoryCode   publicationdomain.ExternalCode
	pulls            []publicationdomain.PullRequest
	createDispatches uint64
	updateDispatches uint64
	draftDispatches  uint64
	observations     uint64
	nextNumber       int64
}

func newTestGitHub(repositoryID int64, nodeID, owner, name, viewer string) *testGitHub {
	return &testGitHub{repositoryID: repositoryID, repositoryNodeID: nodeID, owner: owner, name: name,
		viewer: viewer, RepositoryCode: publicationdomain.CodeOK, nextNumber: 1}
}

func (forge *testGitHub) ObserveRepository(_ context.Context, request githubport.RepositoryRequest) (publicationdomain.RepositoryObservation, error) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	forge.observations++
	code := forge.RepositoryCode
	value := publicationdomain.RepositoryObservation{ID: fmt.Sprintf("test-repository-%d", request.TaskStoreNowMillis), Code: code,
		RepositoryID: forge.repositoryID, RepositoryNodeID: forge.repositoryNodeID, Owner: forge.owner, Name: forge.name,
		ViewerLogin: forge.viewer, Authenticated: code == publicationdomain.CodeOK, CanPush: code == publicationdomain.CodeOK,
		CanPullRequests: code == publicationdomain.CodeOK, TLSVerified: code != publicationdomain.CodeTLS, APIVersion: "2022-11-28",
		RateRemaining: 5_000, RateResetAtMillis: request.TaskStoreNowMillis + 60_000,
		ObservedAtMillis: request.TaskStoreNowMillis, MaximumAgeMillis: publicationdomain.MaximumObservationAgeMS}
	if code == publicationdomain.CodeRateLimited {
		value.RateRemaining = 0
	}
	return publicationdomain.SealRepositoryObservation(value), nil
}

func (forge *testGitHub) ListPullRequests(_ context.Context, request githubport.ListRequest) (publicationdomain.PullRequestPage, error) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	forge.observations++
	matching := make([]publicationdomain.PullRequest, 0)
	for _, pull := range forge.pulls {
		if pull.HeadRef == request.HeadRef && strings.EqualFold(pull.HeadOwner, request.HeadOwner) {
			matching = append(matching, pull)
		}
	}
	start := int((request.Page - 1) * request.PageSize)
	end := min(start+int(request.PageSize), len(matching))
	page := publicationdomain.PullRequestPage{ID: fmt.Sprintf("test-pulls-%d-%d", request.Page, request.TaskStoreNowMillis),
		Code: publicationdomain.CodeOK, Page: request.Page, ObservedAtMillis: request.TaskStoreNowMillis,
		MaximumAgeMillis: publicationdomain.MaximumObservationAgeMS}
	if start < len(matching) {
		page.PullRequests = slices.Clone(matching[start:end])
	}
	if end < len(matching) {
		page.NextPage = request.Page + 1
	} else {
		page.Complete = true
	}
	return publicationdomain.SealPullRequestPage(page), nil
}

func bodyMarker(body string) string {
	start := strings.LastIndex(body, "<!-- director-publication/v1 ")
	if start < 0 {
		return "missing-marker"
	}
	end := strings.Index(body[start:], "-->")
	if end < 0 {
		return "missing-marker"
	}
	return body[start : start+end+3]
}

func (forge *testGitHub) CreatePullRequest(_ context.Context, request githubport.CreateRequest) (githubport.DispatchResult, error) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	forge.createDispatches++
	number := forge.nextNumber
	forge.nextNumber++
	marker := bodyMarker(request.Body)
	forge.pulls = append(forge.pulls, publicationdomain.PullRequest{Number: number, NodeID: "PR_node_" + strconv.FormatInt(number, 10),
		URL: fmt.Sprintf("https://github.com/%s/%s/pull/%d", request.Owner, request.Name, number), State: "open", Draft: request.Draft,
		HeadSHA: request.ExpectedHeadSHA, HeadRef: request.HeadRef, BaseRef: request.BaseRef, HeadOwner: request.HeadOwner,
		HeadRepositoryID: forge.repositoryID, BaseRepositoryID: forge.repositoryID, AuthorLogin: forge.viewer, CreatedByViewer: true,
		MarkerSHA256: publicationdomain.DigestText(marker), MarkerCount: uint32(strings.Count(request.Body, marker)),
		TitleSHA256: publicationdomain.DigestText(request.Title), BodySHA256: publicationdomain.DigestText(request.Body), UpdatedAtMillis: request.TaskStoreNowMillis})
	return githubport.DispatchResult{Handoff: true, Code: publicationdomain.CodeOK}, nil
}

func (forge *testGitHub) UpdatePullRequest(_ context.Context, request githubport.PullRequestRequest) (githubport.DispatchResult, error) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	forge.updateDispatches++
	for index := range forge.pulls {
		pull := &forge.pulls[index]
		if pull.Number != request.Number {
			continue
		}
		marker := bodyMarker(request.Body)
		pull.TitleSHA256, pull.BodySHA256 = publicationdomain.DigestText(request.Title), publicationdomain.DigestText(request.Body)
		pull.MarkerSHA256, pull.MarkerCount, pull.UpdatedAtMillis = publicationdomain.DigestText(marker), uint32(strings.Count(request.Body, marker)), request.TaskStoreNowMillis
		return githubport.DispatchResult{Handoff: true, Code: publicationdomain.CodeOK}, nil
	}
	return githubport.DispatchResult{Handoff: true, Code: publicationdomain.CodeNotFound}, nil
}

func (forge *testGitHub) SetPullRequestDraft(_ context.Context, request githubport.PullRequestRequest) (githubport.DispatchResult, error) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	forge.draftDispatches++
	for index := range forge.pulls {
		if forge.pulls[index].Number == request.Number {
			forge.pulls[index].Draft, forge.pulls[index].UpdatedAtMillis = request.Draft, request.TaskStoreNowMillis
			return githubport.DispatchResult{Handoff: true, Code: publicationdomain.CodeOK}, nil
		}
	}
	return githubport.DispatchResult{Handoff: true, Code: publicationdomain.CodeNotFound}, nil
}

func (forge *testGitHub) Snapshot() ([]publicationdomain.PullRequest, uint64, uint64, uint64) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	return slices.Clone(forge.pulls), forge.createDispatches, forge.updateDispatches, forge.draftDispatches
}

func (forge *testGitHub) ObservationCount() uint64 {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	return forge.observations
}

func (forge *testGitHub) ReplacePulls(pulls []publicationdomain.PullRequest) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	forge.pulls = slices.Clone(pulls)
	for _, pull := range pulls {
		forge.nextNumber = max(forge.nextNumber, pull.Number+1)
	}
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
func (store *memoryStore) Run(context.Context, string) (domain.Run, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.run, nil
}
func (store *memoryStore) Candidate(_ context.Context, id string) (domain.Candidate, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, ok := store.candidates[id]
	if !ok {
		return domain.Candidate{}, errors.New("Candidate not found")
	}
	return value, nil
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
	if next.Version != store.run.Version+1 || next.Execution.Publication == nil ||
		!publicationdomain.ValidState(*next.Execution.Publication) {
		return domain.CommandResult{}, errors.New("invalid publication state write")
	}
	store.run = next
	result := domain.CommandResult{Outcome: domain.CommandApplied, ObservedVersion: next.Version, EventID: event.ID}
	store.commands[command.IdempotencyKey] = result
	return result, nil
}

func candidateRecord(id, runID, candidateSHA, baseSHA, treeSHA, branch, bindingHash string, generation uint64) (domain.Candidate, candidate.Authority) {
	claim := candidate.Claim{SchemaVersion: candidate.ClaimSchemaVersion, ID: "claim-" + id, ProjectID: "project-1",
		WorkspaceID: "workspace-1", TaskID: "dir-m4.5", RunID: runID, ActorID: testTaskAgent,
		WorktreeID: "worktree-1", Branch: branch, BaseRef: "refs/heads/main", CandidateSHA: candidateSHA,
		BaseSHA: baseSHA, LeaseEpoch: 1, ExpectedRunVersion: 1, TaskVersion: 3,
		AcceptanceSHA256: strings.Repeat("1", 64), ConfigurationSHA256: strings.Repeat("2", 64),
		ProfileSHA256: strings.Repeat("3", 64), ContextSHA256: strings.Repeat("4", 64),
		DecisionsSHA256: strings.Repeat("5", 64), FindingsSHA256: strings.Repeat("6", 64)}
	manifest := candidate.Manifest{SchemaVersion: candidate.ManifestSchemaVersion, CandidateSHA: candidateSHA, BaseSHA: baseSHA,
		ParentSHA: baseSHA, TreeSHA: treeSHA, DiffSHA256: strings.Repeat("7", 64), ChangedPathsSHA256: strings.Repeat("8", 64),
		RepositoryBindingSHA256: bindingHash, ClaimSHA256: candidate.ClaimSHA256(claim), ObservationSHA256: strings.Repeat("9", 64),
		AcceptanceSHA256: claim.AcceptanceSHA256, ConfigurationSHA256: claim.ConfigurationSHA256, ProfileSHA256: claim.ProfileSHA256,
		ContextSHA256: claim.ContextSHA256, DecisionsSHA256: claim.DecisionsSHA256, FindingsSHA256: claim.FindingsSHA256,
		GraphPolicySHA256: candidate.GraphPolicySHA256(claim.GraphPolicy)}
	manifest.BindingSHA256 = candidate.ManifestSHA256(manifest)
	record := domain.Candidate{SchemaVersion: domain.CandidateSchemaVersion, ID: id, RunID: runID, Sequence: generation,
		CommitSHA: candidateSHA, Claim: claim, Manifest: manifest, AdmittedAtMillis: 1_000}
	authority := candidate.NewAuthority(generation-1, id, branch, 3, manifest)
	return record, authority
}

type fixture struct {
	store    *memoryStore
	branches *testBranches
	github   *testGitHub
	service  *Service
	command  AdmitCommand
	now      int64
}

func newFixture(t *testing.T, publishBeforeReview bool) *fixture {
	t.Helper()
	const remote = "https://github.com/example/product"
	branch := "task/dir-m4.5-publication"
	repository := execution.RepositoryBinding{RepositoryID: "repository-identity", RepositoryKey: "github.com/example/product",
		CanonicalRemote: remote, SourcePath: "/srv/product", SourceDevice: 1, SourceInode: 2,
		GitCommonDirectory: "/srv/product/.git", GitCommonDevice: 1, GitCommonInode: 3,
		WorktreePath: "/srv/worktree", Branch: branch, BaseSHA: strings.Repeat("a", 40)}
	bindingHash := execution.RepositoryBindingSHA256(repository)
	record, authority := candidateRecord("candidate-1", "run-1", strings.Repeat("b", 40), strings.Repeat("a", 40), strings.Repeat("c", 40), branch, bindingHash, 1)
	policy := publicationdomain.NewPolicy("pull_request", publishBeforeReview, []string{"release"})
	store := &memoryStore{project: domain.Project{ID: "project-1", State: "active", Lease: &domain.ProjectLease{
		HolderInstance: "engine-1", HolderProcessIdentity: "process-1", Epoch: 1,
		AcquiredAtMillis: 100, RenewedAtMillis: 100, ExpiresAtMillis: 100_000, DispatchAllowed: true}},
		workspace: domain.Workspace{ID: "workspace-1", ProjectID: "project-1", Repository: domain.RepositoryIdentity{
			ID: repository.RepositoryID, Key: repository.RepositoryKey, CanonicalRemote: repository.CanonicalRemote,
			SourcePath: repository.SourcePath, SourceDevice: repository.SourceDevice, SourceInode: repository.SourceInode,
			GitCommonDirectory: repository.GitCommonDirectory, GitCommonDevice: repository.GitCommonDevice, GitCommonInode: repository.GitCommonInode}},
		task: domain.Task{ID: "dir-m4.5", ProjectID: "project-1", Key: "DIR-M4.5", Title: "Implement idempotent GitHub PR publication",
			Objective: "Publish one pull request.", AcceptanceCriteria: "Publication is idempotent.", WorkspaceIDs: []string{"workspace-1"}, Version: 3},
		run: domain.Run{ID: "run-1", TaskID: "dir-m4.5", Number: 1, BaseSHA: record.Manifest.BaseSHA,
			CurrentCandidateID: record.ID, Version: 1, Execution: execution.State{SchemaVersion: execution.SchemaVersion,
				Scope:             execution.Scope{ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "dir-m4.5", RunID: "run-1"},
				LeaseBinding:      execution.LeaseBinding{HolderInstance: "engine-1", HolderProcessIdentity: "process-1", Epoch: 1},
				RepositoryBinding: repository, RepositoryBindingHash: bindingHash, Branch: branch, BaseRef: "refs/heads/main",
				Claim: &execution.CompletedClaim{ID: "claim-completed", SchemaVersion: "director.agent-outcome.completed/v1",
					Outcome: "completed", AgentID: testTaskAgent, CandidateSHA: record.CommitSHA, BaseSHA: record.Manifest.BaseSHA,
					CriteriaResults: map[string]string{"criterion-1": "claimed_satisfied"}, ResidualRiskCodes: []string{"P2_EXISTING_BOUND"}},
				CandidateAuthority: &authority, PublicationPolicy: &policy}},
		candidates: map[string]domain.Candidate{record.ID: record}, commands: map[string]domain.CommandResult{}}
	branches := newTestBranches(remote)
	branches.Set("main", strings.Repeat("a", 40))
	github := newTestGitHub(testRepositoryID, "R_kgDOExample", "example", "product", "example")
	service, err := NewService(store, branches, github)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{store: store, branches: branches, github: github, service: service, now: 1_100,
		command: AdmitCommand{RunID: "run-1", ExpectedRunVersion: 1, GitHubRepositoryID: testRepositoryID,
			GitHubRepositoryNodeID: "R_kgDOExample", HeadOwner: "example", OwnershipSHA256: strings.Repeat("d", 64), NowMillis: 1_100}}
}

func correctionState(t *testing.T, run domain.Run) domaincorrection.State {
	t.Helper()
	snapshots := []domaincorrection.SourceSnapshot{
		{Source: domaincorrection.SourceReview, Revision: strings.Repeat("1", 64), Count: 1},
		{Source: domaincorrection.SourceValidation, Revision: strings.Repeat("2", 64)},
		{Source: domaincorrection.SourceCI, Revision: strings.Repeat("3", 64)},
		{Source: domaincorrection.SourceHuman, Revision: strings.Repeat("4", 64)},
	}
	inputs := []domaincorrection.FindingInput{{ID: "review-finding", Source: domaincorrection.SourceReview,
		Class: "CORRECTNESS", Severity: domaincorrection.SeverityP1, CandidateID: run.CurrentCandidateID,
		CandidateSHA: run.Execution.CandidateAuthority.CandidateSHA, Summary: "Review requires a bounded correction",
		Evidence: []domaincorrection.Evidence{{ID: "review-evidence", Kind: domaincorrection.EvidenceReviewFinding,
			SHA256: strings.Repeat("5", 64)}}, AcceptanceCoverage: []string{"criterion-1"}, Blocking: true}}
	batch, ok := domaincorrection.Canonicalize(run.CurrentCandidateID, run.Execution.CandidateAuthority.CandidateSHA,
		[]string{"criterion-1"}, snapshots, inputs)
	if !ok {
		t.Fatal("canonicalize correction fixture")
	}
	state, ok := domaincorrection.NewState(run.TaskID, run.ID, run.CurrentCandidateID,
		run.Execution.CandidateAuthority.CandidateSHA, testTaskAgent, []string{"criterion-1"},
		strings.Repeat("6", 64), strings.Repeat("7", 64), strings.Repeat("8", 64), strings.Repeat("9", 64),
		domaincorrection.Policy{AutoFixCIFailures: true, AutoFixReviewFeedback: true, AttemptLimit: domaincorrection.AttemptLimit}, batch)
	if !ok {
		t.Fatal("create correction fixture")
	}
	return state
}

func gateCorrection(t *testing.T, run domain.Run) domaincorrection.State {
	t.Helper()
	state := correctionState(t, run)
	ciKey, reviewKey := domaincorrection.GateKeys(run.CurrentCandidateID, run.Execution.CandidateAuthority.CandidateSHA,
		run.Execution.CandidateAuthority.Generation)
	state.Phase = domaincorrection.PhaseGatesRequired
	state.Gates = []domaincorrection.GatePlan{{CandidateID: run.CurrentCandidateID,
		CandidateSHA:        run.Execution.CandidateAuthority.CandidateSHA,
		CandidateGeneration: run.Execution.CandidateAuthority.Generation, CIKey: ciKey, ReviewBindingKey: reviewKey,
		FreshCIRequired: true, FreshReviewRequired: true, PriorAuthorityInvalidated: true}}
	if !domaincorrection.ValidState(state) {
		t.Fatal("create corrected-Candidate gate fixture")
	}
	return state
}

func parkCorrection(t *testing.T, state domaincorrection.State, code string) domaincorrection.State {
	t.Helper()
	state.Phase, state.NeedsYouCode, state.WakeCondition = domaincorrection.PhaseNeedsYou, code, "fresh_correction_facts_or_human_decision"
	if !domaincorrection.ValidState(state) {
		t.Fatalf("park correction fixture for %s", code)
	}
	return state
}

func attachApproval(t *testing.T, fixture *fixture) {
	t.Helper()
	fixture.store.mu.Lock()
	defer fixture.store.mu.Unlock()
	run := &fixture.store.run
	authority := run.Execution.CandidateAuthority
	record := fixture.store.candidates[run.CurrentCandidateID]
	binding := domainreview.SealBinding(domainreview.Binding{TaskID: run.TaskID, RunID: run.ID, CandidateID: record.ID,
		CandidateSHA: record.CommitSHA, BaseSHA: record.Manifest.BaseSHA, TreeSHA: record.Manifest.TreeSHA,
		ManifestSHA256: record.Manifest.BindingSHA256, AcceptanceSHA256: record.Manifest.AcceptanceSHA256,
		ConfigurationSHA256: record.Manifest.ConfigurationSHA256, ProfileSHA256: record.Manifest.ProfileSHA256,
		ContextSHA256: record.Manifest.ContextSHA256, DecisionsSHA256: record.Manifest.DecisionsSHA256,
		FindingsSHA256: record.Manifest.FindingsSHA256, CandidateGeneration: authority.Generation, CISlotID: "ci-slot-1",
		TaskAgentUUID: testTaskAgent, CoordinatorUUID: testCoordinator, ReviewOwnerUUID: testReviewOwner})
	profile := domainreview.EvaluateProfile(domainreview.ProfilePolicy{}, domainreview.Profile{Provider: "codex", Model: "worker", SHA256: strings.Repeat("3", 64), Supported: true},
		domainreview.Profile{Provider: "codex", Model: "reviewer", SHA256: strings.Repeat("3", 64), Supported: true})
	state, ok := domainreview.NewState(binding, []string{"criterion-1"}, []domainreview.Probe{}, profile)
	if !ok {
		t.Fatal("create review state")
	}
	state.SourcePath, state.PrimaryPath, state.ReviewerRoot, state.CheckoutPath = "/srv/product", "/srv/worktree", "/srv/reviewers", "/srv/reviewers/candidate"
	state.PrimaryHeadSHA = record.CommitSHA
	checkout := domainreview.SealCheckoutEvidence(domainreview.CheckoutEvidence{CheckoutID: state.ReviewKey, OwnerUUID: testReviewOwner,
		CandidateSHA: record.CommitSHA, TreeSHA: record.Manifest.TreeSHA, Detached: true, Clean: true, PrimaryDistinct: true, SourceUnchanged: true})
	state.CheckoutEvidence = &checkout
	state.Checkout.Phase, state.Checkout.ExternalID, state.Checkout.FactSHA256 = domainreview.EffectComplete, state.ReviewKey, strings.Repeat("a", 64)
	state.Workspace.Phase, state.Workspace.ExternalID, state.Workspace.FactSHA256 = domainreview.EffectComplete, "review-workspace", strings.Repeat("b", 64)
	ci := domainreview.SealCIObservation(domainreview.CIObservation{ID: "ci-observation-1", SlotID: binding.CISlotID,
		CandidateSHA: binding.CandidateSHA, BaseSHA: binding.BaseSHA, TreeSHA: binding.TreeSHA, ManifestSHA256: binding.ManifestSHA256,
		WorkflowRunID: "34500000123", RequiredChecks: []string{"maintained-linux-ci"}, Status: "passed",
		Authoritative: true, Complete: true, ObservedAtMillis: fixture.now})
	state.CIObservation = &ci
	state.ReviewerUUID, state.ReviewerSessionSHA256, state.RegisteredAt, state.RegistrationSHA256 = testReviewOwner, strings.Repeat("c", 64), "2026-09-11T00:00:00Z", strings.Repeat("d", 64)
	state.Bootstrap.Phase, state.Bootstrap.ExternalID, state.Bootstrap.FactSHA256 = domainreview.EffectComplete, testReviewOwner, strings.Repeat("e", 64)
	state.PromptSHA256 = strings.Repeat("f", 64)
	state.Prompt.Phase, state.Prompt.ExternalID, state.Prompt.FactSHA256 = domainreview.EffectComplete, testReviewOwner, strings.Repeat("0", 64)
	state.Evidence = &domainreview.Evidence{SchemaVersion: domainreview.EvidenceSchemaVersion, ID: "review-evidence-1",
		ReviewKey: state.ReviewKey, BindingSHA256: binding.BindingSHA256, ReviewerUUID: testReviewOwner,
		HarnessReviewerUUID: testReviewOwner, CIObservationSHA256: ci.SHA256, CheckoutFactSHA256: checkout.FactSHA256,
		Verdict: domainreview.VerdictApproveCandidate, ClaimSHA256: strings.Repeat("1", 64), ObservedAtMillis: fixture.now}
	state.VerdictDurablyObserved = true
	if !domainreview.ValidState(state) {
		t.Fatal("constructed Review state is invalid")
	}
	run.Execution.Review = &state
	authority.Downstream.Review = &candidate.EvidenceBinding{ID: state.Evidence.ID, CandidateID: authority.CandidateID,
		CandidateSHA: authority.CandidateSHA, BaseSHA: authority.BaseSHA, Generation: authority.Generation, BindingSHA256: authority.BindingSHA256}
	run.Version++
}

func reconcileTo(t *testing.T, fixture *fixture, stop func(domain.Run) bool) domain.Run {
	t.Helper()
	for range 80 {
		run, _ := fixture.store.Run(context.Background(), "run-1")
		if stop(run) {
			return run
		}
		restarted, restartErr := NewService(fixture.store, fixture.branches, fixture.github)
		if restartErr != nil {
			t.Fatal(restartErr)
		}
		fixture.service = restarted
		_, err := restarted.Reconcile(context.Background(), "run-1", fixture.now)
		if err != nil && !errors.Is(err, ErrResponseUnknown) && !errors.Is(err, ErrReviewRequired) {
			t.Fatalf("reconcile: %v", err)
		}
	}
	t.Fatal("publication did not converge")
	return domain.Run{}
}

func TestReviewBeforePRIsDefaultAndRepeatPublicationIsIdempotent(t *testing.T) {
	fixture := newFixture(t, false)
	admitted, err := fixture.service.Admit(context.Background(), fixture.command)
	if err != nil || !admitted.Progressed {
		t.Fatalf("Admit() = %#v, %v", admitted, err)
	}
	result, err := fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
	if !errors.Is(err, ErrReviewRequired) || !result.WaitingForReview {
		t.Fatalf("pre-review result = %#v, %v", result, err)
	}
	if _, pushes := fixture.branches.Snapshot(fixture.store.run.Execution.Branch); pushes != 0 {
		t.Fatal("branch pushed before Review")
	}
	if _, creates, _, _ := fixture.github.Snapshot(); creates != 0 {
		t.Fatal("PR created before Review")
	}
	attachApproval(t, fixture)
	final := reconcileTo(t, fixture, func(run domain.Run) bool {
		return run.Execution.Publication != nil && run.Execution.Publication.Evidence != nil && run.Execution.Publication.Evidence.Ready
	})
	if final.Execution.Publication.Evidence.Draft || final.Execution.Publication.Evidence.HeadSHA != strings.Repeat("b", 40) {
		t.Fatal("ready publication evidence is wrong")
	}
	pulls, creates, updates, ready := fixture.github.Snapshot()
	if len(pulls) != 1 || pulls[0].Draft || creates != 1 || updates != 0 || ready != 0 {
		t.Fatalf("PR facts = %#v, dispatches=%d/%d/%d", pulls, creates, updates, ready)
	}
	if _, pushes := fixture.branches.Snapshot(fixture.store.run.Execution.Branch); pushes != 1 {
		t.Fatalf("push dispatches = %d", pushes)
	}
	for range 8 {
		fixture.now++
		result, err := fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
		if err != nil || !result.Published {
			t.Fatalf("replay = %#v, %v", result, err)
		}
	}
	_, createsAfter, updatesAfter, readyAfter := fixture.github.Snapshot()
	if createsAfter != creates || updatesAfter != updates || readyAfter != ready {
		t.Fatal("repeat publication duplicated a GitHub effect")
	}
}

func TestPublishBeforeReviewDraftFencesThirtyTwoConcurrentCoordinators(t *testing.T) {
	fixture := newFixture(t, true)
	if _, err := fixture.service.Admit(context.Background(), fixture.command); err != nil {
		t.Fatal(err)
	}
	for batch := 0; batch < 20; batch++ {
		var wait sync.WaitGroup
		wait.Add(32)
		for range 32 {
			go func() {
				defer wait.Done()
				restarted, _ := NewService(fixture.store, fixture.branches, fixture.github)
				_, _ = restarted.Reconcile(context.Background(), "run-1", fixture.now)
			}()
		}
		wait.Wait()
		run, _ := fixture.store.Run(context.Background(), "run-1")
		if run.Execution.Publication != nil && run.Execution.Publication.Evidence != nil {
			break
		}
	}
	run, _ := fixture.store.Run(context.Background(), "run-1")
	if run.Execution.Publication.Evidence == nil || !run.Execution.Publication.Evidence.Draft {
		t.Fatal("owned draft evidence was not recorded")
	}
	_, creates, updates, ready := fixture.github.Snapshot()
	_, pushes := fixture.branches.Snapshot(run.Execution.Branch)
	if creates != 1 || pushes != 1 || updates != 0 || ready != 0 {
		t.Fatalf("concurrent dispatches push=%d create=%d update=%d ready=%d", pushes, creates, updates, ready)
	}
	if run.Execution.Publication.Evidence.Ready || run.Execution.Publication.Evidence.Draft == false {
		t.Fatal("draft acquired readiness")
	}
}

func TestCorrectionActiveOrParkedBlocksEveryPublicationEffectAndFallback(t *testing.T) {
	active := newFixture(t, true)
	if _, err := active.service.Admit(context.Background(), active.command); err != nil {
		t.Fatal(err)
	}
	active.store.mu.Lock()
	state := correctionState(t, active.store.run)
	active.store.run.Execution.Correction = &state
	active.store.run.Version++
	active.store.mu.Unlock()
	if _, err := active.service.Reconcile(context.Background(), "run-1", active.now); !errors.Is(err, ErrCorrectionPending) {
		t.Fatalf("active correction Reconcile() error = %v", err)
	}
	if _, pushes := active.branches.Snapshot(active.store.run.Execution.Branch); pushes != 0 {
		t.Fatal("active correction dispatched Git")
	}
	if _, creates, updates, drafts := active.github.Snapshot(); creates != 0 || updates != 0 || drafts != 0 {
		t.Fatalf("active correction dispatched GitHub: %d/%d/%d", creates, updates, drafts)
	}
	if active.branches.ObservationCount() != 0 || active.github.ObservationCount() != 0 {
		t.Fatal("active correction reached a Git or GitHub port")
	}

	for _, test := range []struct {
		name            string
		code            string
		acknowledgement bool
		runNeedsYou     bool
	}{
		{name: "parked", code: "correction_human_decision_required"},
		{name: "needs_you", code: "correction_needs_you", runNeedsYou: true},
		{name: "attempt_exhaustion", code: "correction_attempt_limit_exhausted", runNeedsYou: true},
		{name: "budget_stop", code: "runtime_budget_turns_hard", runNeedsYou: true},
		{name: "provider_ambiguity", code: "correction_provider_ambiguous", runNeedsYou: true},
		{name: "unchanged_sha_rejection", acknowledgement: true},
		{name: "repeated_unchanged_sha", code: "correction_acknowledgement_repeated", runNeedsYou: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t, true)
			fixture.store.mu.Lock()
			correction := correctionState(t, fixture.store.run)
			if test.acknowledgement {
				correction.AcknowledgementRejections = 1
				if !domaincorrection.ValidState(correction) {
					t.Fatal("unchanged-SHA rejection fixture is invalid")
				}
			} else {
				correction = parkCorrection(t, correction, test.code)
			}
			fixture.store.run.Execution.Correction = &correction
			if test.runNeedsYou {
				fixture.store.run.Execution.NeedsYou = &execution.NeedsYou{Code: execution.NeedCode(test.code),
					WakeCondition: "fresh_correction_facts_or_human_decision", CleanupAuthorized: false}
			}
			fixture.store.mu.Unlock()
			if _, err := fixture.service.Admit(context.Background(), fixture.command); !errors.Is(err, ErrCorrectionPending) {
				t.Fatalf("Admit() error = %v", err)
			}
			if fixture.store.run.Execution.Publication != nil {
				t.Fatal("blocked correction created a publication intent")
			}
			if _, pushes := fixture.branches.Snapshot(fixture.store.run.Execution.Branch); pushes != 0 {
				t.Fatal("blocked correction dispatched Git")
			}
			if _, creates, updates, drafts := fixture.github.Snapshot(); creates != 0 || updates != 0 || drafts != 0 {
				t.Fatalf("blocked correction dispatched GitHub: %d/%d/%d", creates, updates, drafts)
			}
			if fixture.branches.ObservationCount() != 0 || fixture.github.ObservationCount() != 0 {
				t.Fatal("blocked correction reached a Git or GitHub port")
			}
			if fixture.store.run.Execution.PublicationPolicy.DeliveryMode != "pull_request" {
				t.Fatal("blocked correction selected direct delivery")
			}
		})
	}
}

func TestEveryMutationFrontierRecoversFromResponseLoss(t *testing.T) {
	fixture := newFixture(t, true)
	if _, err := fixture.service.Admit(context.Background(), fixture.command); err != nil {
		t.Fatal(err)
	}
	var sawPush, sawCreate bool
	for range 40 {
		run, _ := fixture.store.Run(context.Background(), "run-1")
		if run.Execution.Publication.PullRequest.Phase == publicationdomain.EffectComplete {
			pulls, _, _, _ := fixture.github.Snapshot()
			pulls[0].TitleSHA256 = strings.Repeat("0", 64)
			fixture.github.ReplacePulls(pulls)
			break
		}
		restarted, _ := NewService(fixture.store, fixture.branches, fixture.github)
		fixture.service = restarted
		_, err := restarted.Reconcile(context.Background(), "run-1", fixture.now)
		if errors.Is(err, ErrResponseUnknown) {
			current, _ := fixture.store.Run(context.Background(), "run-1")
			sawPush = sawPush || current.Execution.Publication.Push.Phase == publicationdomain.EffectDispatching
			sawCreate = sawCreate || current.Execution.Publication.PullRequest.Phase == publicationdomain.EffectDispatching
		}
	}
	if !sawPush || !sawCreate {
		t.Fatalf("response-loss frontiers push=%t create=%t", sawPush, sawCreate)
	}
	reconcileTo(t, fixture, func(run domain.Run) bool {
		return run.Execution.Publication.Metadata.Phase == publicationdomain.EffectComplete
	})
	attachApproval(t, fixture)
	var sawReady bool
	final := reconcileTo(t, fixture, func(run domain.Run) bool {
		if run.Execution.Publication.Ready.Phase == publicationdomain.EffectDispatching {
			sawReady = true
		}
		return run.Execution.Publication.Evidence != nil && run.Execution.Publication.Evidence.Ready
	})
	if !sawReady || !final.Execution.Publication.Evidence.Ready {
		t.Fatal("ready response loss was not recovered")
	}
	_, creates, updates, ready := fixture.github.Snapshot()
	if creates != 1 || updates != 2 || ready != 1 {
		t.Fatalf("frontier dispatches create=%d update=%d ready=%d", creates, updates, ready)
	}
}

func TestRepositoryAndTransportFailuresFailClosedWithoutDeliveryFallback(t *testing.T) {
	codes := []publicationdomain.ExternalCode{publicationdomain.CodeRateLimited, publicationdomain.CodeTLS,
		publicationdomain.CodeUnauthorized, publicationdomain.CodeForbidden, publicationdomain.CodeNotFound,
		publicationdomain.CodeConflict, publicationdomain.CodeUnprocessable, publicationdomain.CodeServer,
		publicationdomain.CodeUnavailable, publicationdomain.CodeCapabilityMissing, publicationdomain.CodeRepositoryMismatch}
	for _, code := range codes {
		t.Run(string(code), func(t *testing.T) {
			fixture := newFixture(t, true)
			fixture.github.RepositoryCode = code
			if _, err := fixture.service.Admit(context.Background(), fixture.command); err != nil {
				t.Fatal(err)
			}
			result, err := fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
			if err == nil || result.Code != code {
				t.Fatalf("Reconcile() = %#v, %v", result, err)
			}
			run, _ := fixture.store.Run(context.Background(), "run-1")
			if run.Execution.Publication.Policy.DeliveryMode != "pull_request" {
				t.Fatal("delivery mode changed")
			}
			if _, pushes := fixture.branches.Snapshot(run.Execution.Branch); pushes != 0 {
				t.Fatal("branch changed during failed preflight")
			}
			if _, creates, _, _ := fixture.github.Snapshot(); creates != 0 {
				t.Fatal("PR created during failed preflight")
			}
		})
	}
}

func TestBranchDriftProtectedRefsAndSecretInputAreRefused(t *testing.T) {
	fixture := newFixture(t, true)
	fixture.branches.Set(fixture.store.run.Execution.Branch, strings.Repeat("e", 40))
	if _, err := fixture.service.Admit(context.Background(), fixture.command); err != nil {
		t.Fatal(err)
	}
	_, _ = fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
	result, err := fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
	if !errors.Is(err, ErrPublicationRefused) || result.Code != publicationdomain.CodeRefChanged {
		t.Fatalf("drift = %#v, %v", result, err)
	}
	if _, pushes := fixture.branches.Snapshot(fixture.store.run.Execution.Branch); pushes != 0 {
		t.Fatal("drift caused a push")
	}

	baseDrift := newFixture(t, true)
	baseDrift.branches.Set("main", strings.Repeat("f", 40))
	if _, err := baseDrift.service.Admit(context.Background(), baseDrift.command); err != nil {
		t.Fatal(err)
	}
	baseResult, err := baseDrift.service.Reconcile(context.Background(), "run-1", baseDrift.now)
	if !errors.Is(err, ErrPublicationRefused) || baseResult.Code != publicationdomain.CodeBaseChanged {
		t.Fatalf("live base drift = %#v, %v", baseResult, err)
	}
	if _, pushes := baseDrift.branches.Snapshot(baseDrift.store.run.Execution.Branch); pushes != 0 {
		t.Fatal("base drift caused a push")
	}

	protected := newFixture(t, true)
	protected.store.run.Execution.Branch = "main"
	protected.store.run.Execution.RepositoryBinding.Branch = "main"
	protected.store.run.Execution.RepositoryBindingHash = execution.RepositoryBindingSHA256(protected.store.run.Execution.RepositoryBinding)
	_, err = protected.service.Admit(context.Background(), protected.command)
	if !errors.Is(err, ErrPublicationRefused) {
		t.Fatalf("protected ref error = %v", err)
	}

	secret := newFixture(t, true)
	secret.store.task.Title = "publish token=github_pat_abcdefghijklmnop"
	_, err = secret.service.Admit(context.Background(), secret.command)
	if !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("secret title error = %v", err)
	}
	if _, creates, _, _ := secret.github.Snapshot(); creates != 0 {
		t.Fatal("secret reached GitHub")
	}
}

func validPull(state publicationdomain.State, number int64) publicationdomain.PullRequest {
	return publicationdomain.PullRequest{Number: number, NodeID: "PR_node_" + strconv.FormatInt(number, 10),
		URL: "https://github.com/example/product/pull/" + strconv.FormatInt(number, 10), State: "open", Draft: true,
		HeadSHA: state.Binding.CandidateSHA, HeadRef: state.Binding.Branch, BaseRef: strings.TrimPrefix(state.Binding.BaseRef, "refs/heads/"),
		HeadOwner: state.Binding.HeadOwner, HeadRepositoryID: state.Binding.GitHubRepositoryID,
		BaseRepositoryID: state.Binding.GitHubRepositoryID, AuthorLogin: state.Binding.HeadOwner, CreatedByViewer: true,
		MarkerSHA256: publicationdomain.DigestText(publicationdomain.Marker(state.Binding)), MarkerCount: 1,
		TitleSHA256: publicationdomain.DigestText(state.Template.Title), BodySHA256: publicationdomain.DigestText(state.Template.Body), UpdatedAtMillis: 1_000}
}

func preparePRScan(t *testing.T, pulls []publicationdomain.PullRequest) (*fixture, Result, error) {
	t.Helper()
	fixture := newFixture(t, true)
	fixture.branches.Set(fixture.store.run.Execution.Branch, strings.Repeat("b", 40))
	if _, err := fixture.service.Admit(context.Background(), fixture.command); err != nil {
		t.Fatal(err)
	}
	_, _ = fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
	_, _ = fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
	state := *fixture.store.run.Execution.Publication
	for index := range pulls {
		if pulls[index].Number == 0 {
			pulls[index] = validPull(state, int64(index+1))
		}
	}
	fixture.github.ReplacePulls(pulls)
	first, err := fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
	return fixture, first, err
}

func TestUnownedDuplicateForgedAndClosedHistoricalPullRequests(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(publicationdomain.PullRequest) []publicationdomain.PullRequest
		code   publicationdomain.ExternalCode
	}{
		{"unowned", func(p publicationdomain.PullRequest) []publicationdomain.PullRequest {
			p.MarkerCount, p.MarkerSHA256 = 0, ""
			return []publicationdomain.PullRequest{p}
		}, publicationdomain.CodePullRequestUnowned},
		{"duplicate", func(p publicationdomain.PullRequest) []publicationdomain.PullRequest {
			q := p
			q.Number, q.NodeID, q.URL = 2, "PR_node_b", "https://github.com/example/product/pull/2"
			return []publicationdomain.PullRequest{p, q}
		}, publicationdomain.CodePullRequestDuplicate},
		{"forged", func(p publicationdomain.PullRequest) []publicationdomain.PullRequest {
			p.CreatedByViewer, p.AuthorLogin = false, "attacker"
			return []publicationdomain.PullRequest{p}
		}, publicationdomain.CodeMarkerForged},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t, true)
			fixture.branches.Set(fixture.store.run.Execution.Branch, strings.Repeat("b", 40))
			if _, err := fixture.service.Admit(context.Background(), fixture.command); err != nil {
				t.Fatal(err)
			}
			_, _ = fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
			_, _ = fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
			p := validPull(*fixture.store.run.Execution.Publication, 1)
			fixture.github.ReplacePulls(test.mutate(p))
			_, _ = fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
			result, err := fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
			if !errors.Is(err, ErrPublicationRefused) || result.Code != test.code {
				t.Fatalf("result = %#v, %v", result, err)
			}
		})
	}

	closed := newFixture(t, true)
	closed.branches.Set(closed.store.run.Execution.Branch, strings.Repeat("b", 40))
	if _, err := closed.service.Admit(context.Background(), closed.command); err != nil {
		t.Fatal(err)
	}
	_, _ = closed.service.Reconcile(context.Background(), "run-1", closed.now)
	_, _ = closed.service.Reconcile(context.Background(), "run-1", closed.now)
	historical := validPull(*closed.store.run.Execution.Publication, 1)
	historical.State = "closed"
	closed.github.ReplacePulls([]publicationdomain.PullRequest{historical})
	reconcileTo(t, closed, func(run domain.Run) bool { return run.Execution.Publication.Evidence != nil })
	pulls, creates, _, _ := closed.github.Snapshot()
	if creates != 1 || len(pulls) != 2 {
		t.Fatalf("closed history aliased: pulls=%d creates=%d", len(pulls), creates)
	}
}

func TestBoundedPaginationFindsOneOwnedPullRequest(t *testing.T) {
	fixture := newFixture(t, true)
	fixture.branches.Set(fixture.store.run.Execution.Branch, strings.Repeat("b", 40))
	if _, err := fixture.service.Admit(context.Background(), fixture.command); err != nil {
		t.Fatal(err)
	}
	_, _ = fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
	_, _ = fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
	state := *fixture.store.run.Execution.Publication
	pulls := make([]publicationdomain.PullRequest, 0, 151)
	for index := 1; index <= 150; index++ {
		pull := validPull(state, int64(index))
		pull.State = "closed"
		pulls = append(pulls, pull)
	}
	pulls = append(pulls, validPull(state, 151))
	fixture.github.ReplacePulls(pulls)
	reconcileTo(t, fixture, func(run domain.Run) bool { return run.Execution.Publication.Evidence != nil })
	_, creates, _, _ := fixture.github.Snapshot()
	if creates != 0 || fixture.store.run.Execution.Publication.OwnedPullRequest.Number != 151 {
		t.Fatal("paginated owned PR was not adopted")
	}
}

func TestPaginationLimitHeadDriftAndBaseDriftFailClosed(t *testing.T) {
	fixture := newFixture(t, true)
	fixture.branches.Set(fixture.store.run.Execution.Branch, strings.Repeat("b", 40))
	if _, err := fixture.service.Admit(context.Background(), fixture.command); err != nil {
		t.Fatal(err)
	}
	_, _ = fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
	_, _ = fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
	state := *fixture.store.run.Execution.Publication
	pulls := make([]publicationdomain.PullRequest, 0, 1_001)
	for index := 1; index <= 1_001; index++ {
		pull := validPull(state, int64(index))
		pull.State = "closed"
		pulls = append(pulls, pull)
	}
	fixture.github.ReplacePulls(pulls)
	result, err := fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
	if !errors.Is(err, ErrPublicationRefused) || result.Code != publicationdomain.CodePaginationIncomplete {
		t.Fatalf("pagination = %#v, %v", result, err)
	}
	if _, creates, _, _ := fixture.github.Snapshot(); creates != 0 {
		t.Fatal("incomplete pagination created a PR")
	}

	for _, test := range []struct {
		name   string
		mutate func(*publicationdomain.PullRequest)
		code   publicationdomain.ExternalCode
	}{
		{"head", func(p *publicationdomain.PullRequest) { p.HeadSHA = strings.Repeat("e", 40) }, publicationdomain.CodeHeadChanged},
		{"base", func(p *publicationdomain.PullRequest) { p.BaseRef = "release" }, publicationdomain.CodePullRequestUnowned},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidateFixture := newFixture(t, true)
			candidateFixture.branches.Set(candidateFixture.store.run.Execution.Branch, strings.Repeat("b", 40))
			if _, err := candidateFixture.service.Admit(context.Background(), candidateFixture.command); err != nil {
				t.Fatal(err)
			}
			_, _ = candidateFixture.service.Reconcile(context.Background(), "run-1", candidateFixture.now)
			_, _ = candidateFixture.service.Reconcile(context.Background(), "run-1", candidateFixture.now)
			pull := validPull(*candidateFixture.store.run.Execution.Publication, 1)
			test.mutate(&pull)
			candidateFixture.github.ReplacePulls([]publicationdomain.PullRequest{pull})
			_, _ = candidateFixture.service.Reconcile(context.Background(), "run-1", candidateFixture.now)
			result, err := candidateFixture.service.Reconcile(context.Background(), "run-1", candidateFixture.now)
			if !errors.Is(err, ErrPublicationRefused) || result.Code != test.code {
				t.Fatalf("drift = %#v, %v", result, err)
			}
		})
	}
}

func TestCorrectedCandidateReusesDraftAndExactOldHeadLease(t *testing.T) {
	fixture := newFixture(t, true)
	if _, err := fixture.service.Admit(context.Background(), fixture.command); err != nil {
		t.Fatal(err)
	}
	first := reconcileTo(t, fixture, func(run domain.Run) bool {
		return run.Execution.Publication.Evidence != nil && run.Execution.Publication.Evidence.Draft
	})
	firstNumber := first.Execution.Publication.OwnedPullRequest.Number
	oldHead := first.Execution.Publication.Binding.CandidateSHA
	attachApproval(t, fixture)

	fixture.store.mu.Lock()
	oldAuthority := *fixture.store.run.Execution.CandidateAuthority
	oldAuthority.Downstream.CI = &candidate.EvidenceBinding{ID: "ci-old", CandidateID: oldAuthority.CandidateID,
		CandidateSHA: oldAuthority.CandidateSHA, BaseSHA: oldAuthority.BaseSHA, Generation: oldAuthority.Generation,
		BindingSHA256: oldAuthority.BindingSHA256}
	fixture.store.run.Execution.CandidateAuthority = &oldAuthority
	old := publicationdomain.Invalidate(*fixture.store.run.Execution.Publication, "candidate_changed")
	fixture.store.run.Execution.PublicationHistory = append(fixture.store.run.Execution.PublicationHistory, old)
	fixture.store.run.Execution.Publication = nil
	newRecord, newAuthority := candidateRecord("candidate-2", "run-1", strings.Repeat("e", 40), strings.Repeat("a", 40), strings.Repeat("f", 40),
		fixture.store.run.Execution.Branch, fixture.store.run.Execution.RepositoryBindingHash, 2)
	historicalAuthority := candidate.HistoricalAuthority{Authority: oldAuthority, InvalidationCode: candidate.CodeCandidateChanged,
		InvalidatedByCandidateID: newRecord.ID, InvalidatedByCandidateSHA: newRecord.CommitSHA, InvalidatedAtMillis: fixture.now}
	if !candidate.ValidHistoricalAuthority(historicalAuthority) {
		t.Fatal("old Candidate authority did not retain exact downstream bindings")
	}
	fixture.store.run.Execution.CandidateAuthorityHistory = append(fixture.store.run.Execution.CandidateAuthorityHistory, historicalAuthority)
	oldReview := domainreview.Invalidate(*fixture.store.run.Execution.Review, "candidate_changed")
	fixture.store.run.Execution.ReviewHistory = append(fixture.store.run.Execution.ReviewHistory, oldReview)
	fixture.store.run.Execution.Review = nil
	fixture.store.candidates[newRecord.ID] = newRecord
	fixture.store.run.CurrentCandidateID = newRecord.ID
	fixture.store.run.Execution.CandidateAuthority = &newAuthority
	fixture.store.run.Execution.Claim.CandidateSHA = newRecord.CommitSHA
	correction := gateCorrection(t, fixture.store.run)
	fixture.store.run.Execution.Correction = &correction
	if !candidate.DownstreamEmpty(newAuthority.Downstream) || historicalAuthority.Authority.Downstream.Review == nil ||
		historicalAuthority.Authority.Downstream.CI == nil || historicalAuthority.Authority.Downstream.Publication == nil ||
		!oldReview.Invalidated || !old.Invalidated {
		t.Fatal("Candidate replacement did not atomically invalidate old Review/CI/publication authority")
	}
	fixture.store.run.Version++
	version := fixture.store.run.Version
	fixture.store.mu.Unlock()
	command := fixture.command
	command.ExpectedRunVersion, command.ExpectedRemoteHead = version, oldHead
	if _, err := fixture.service.Admit(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		run, _ := fixture.store.Run(context.Background(), "run-1")
		if run.Execution.Publication.Push.Phase == publicationdomain.EffectComplete {
			break
		}
		_, err := fixture.service.Reconcile(context.Background(), "run-1", fixture.now)
		if err != nil && !errors.Is(err, ErrResponseUnknown) {
			t.Fatal(err)
		}
	}
	newHead, pushes := fixture.branches.Snapshot(fixture.store.run.Execution.Branch)
	if newHead != strings.Repeat("e", 40) || pushes != 2 {
		t.Fatalf("correction push head=%s dispatches=%d", newHead, pushes)
	}
	pulls, _, _, _ := fixture.github.Snapshot()
	pulls[0].HeadSHA = newHead
	fixture.github.ReplacePulls(pulls)
	second := reconcileTo(t, fixture, func(run domain.Run) bool {
		return run.Execution.Publication.Evidence != nil && run.Execution.Publication.Evidence.Draft
	})
	_, creates, updates, _ := fixture.github.Snapshot()
	if creates != 1 || updates != 1 || second.Execution.Publication.OwnedPullRequest.Number != firstNumber {
		t.Fatalf("corrected publication create=%d update=%d PR=%d", creates, updates, second.Execution.Publication.OwnedPullRequest.Number)
	}
	if second.Execution.CandidateAuthority.Downstream.Review != nil {
		t.Fatal("old Review survived Candidate correction")
	}
	if len(second.Execution.CandidateAuthorityHistory) != 1 || len(second.Execution.ReviewHistory) != 1 ||
		len(second.Execution.PublicationHistory) != 1 || !second.Execution.ReviewHistory[0].Invalidated ||
		!second.Execution.PublicationHistory[0].Invalidated {
		t.Fatal("corrected Candidate lost atomic Review/publication history")
	}
}
