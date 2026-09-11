// SPDX-License-Identifier: Apache-2.0

package directdelivery

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/candidate"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	"github.com/mcuadros/director-engine/domain/correction"
	directdomain "github.com/mcuadros/director-engine/domain/directdelivery"
	"github.com/mcuadros/director-engine/domain/execution"
	domainfeedback "github.com/mcuadros/director-engine/domain/feedback"
	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
	"github.com/mcuadros/director-engine/domain/repository"
	"github.com/mcuadros/director-engine/domain/review"
	directport "github.com/mcuadros/director-engine/ports/directdelivery"
)

const (
	taskAgentUUID = "11111111-1111-4111-8111-111111111111"
	reviewerUUID  = "22222222-2222-4222-8222-222222222222"
	ownerUUID     = "33333333-3333-4333-8333-333333333333"
	coordUUID     = "44444444-4444-4444-8444-444444444444"
)

type memoryStore struct {
	mu        sync.Mutex
	project   domain.Project
	task      domain.Task
	run       domain.Run
	candidate domain.Candidate
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
	return store.run, nil
}

func (store *memoryStore) Candidate(context.Context, string) (domain.Candidate, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.candidate, nil
}

func (store *memoryStore) UpdateRun(_ context.Context, command domain.CommandRequest, next domain.Run, _ domain.Event) (domain.CommandResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if command.ExpectedVersion != store.run.Version || next.Version != store.run.Version+1 {
		return domain.CommandResult{Outcome: domain.CommandRejectedVersionConflict, ObservedVersion: store.run.Version}, nil
	}
	store.run = next
	return domain.CommandResult{Outcome: domain.CommandApplied, ObservedVersion: next.Version}, nil
}

type fakeRemote struct {
	mu           sync.Mutex
	head         string
	pushes       int
	observations int
	loseNext     bool
	failPushes   int
	unavailable  bool
}

func fakeObservation(target directport.Target, status directdomain.ObservationStatus, code directdomain.ObservationCode, current string) directdomain.Observation {
	return directdomain.SealObservation(directdomain.Observation{EffectID: target.EffectID,
		BindingSHA256: target.Binding.SHA256, Attempt: target.Attempt, Status: status, Code: code,
		RepositoryID: target.Binding.RepositoryID, TargetRef: target.Binding.TargetRef, CurrentSHA: current,
		ObservedAtMillis: target.TaskStoreNowMillis, MaximumAgeMillis: directdomain.MaximumObservationAgeMS})
}

func (remote *fakeRemote) Observe(_ context.Context, target directport.Target) (directdomain.Observation, error) {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	remote.observations++
	if remote.unavailable {
		return fakeObservation(target, directdomain.ObservationUnavailable, directdomain.CodeRemoteUnavailable, ""), nil
	}
	switch remote.head {
	case target.Binding.BaseSHA:
		return fakeObservation(target, directdomain.ObservationCurrentExpected, directdomain.CodeOK, remote.head), nil
	case target.Binding.CandidateSHA:
		return fakeObservation(target, directdomain.ObservationDesired, directdomain.CodeOK, remote.head), nil
	case "":
		return fakeObservation(target, directdomain.ObservationAbsent, directdomain.CodeRemoteRefAbsent, ""), nil
	default:
		return fakeObservation(target, directdomain.ObservationDifferent, directdomain.CodeRemoteRefChanged, remote.head), nil
	}
}

func (remote *fakeRemote) Push(_ context.Context, command directport.PushCommand) error {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if command.ExpectedObservation.CurrentSHA != remote.head || remote.head != command.Target.Binding.BaseSHA ||
		command.Attempt != command.Target.Attempt+1 {
		return &directport.DispatchError{Code: directport.FailurePreconditionChanged}
	}
	remote.pushes++
	if remote.failPushes > 0 {
		remote.failPushes--
		return &directport.DispatchError{Code: directport.FailureResultUnknown, PossibleHandoff: true}
	}
	remote.head = command.Target.Binding.CandidateSHA
	if remote.loseNext {
		remote.loseNext = false
		return &directport.DispatchError{Code: directport.FailureResultUnknown, PossibleHandoff: true}
	}
	return nil
}

type fixture struct {
	store   *memoryStore
	remote  *fakeRemote
	service *Service
	policy  directdomain.Policy
}

func approvedReview(t *testing.T, record domain.Candidate, task domain.Task, generation uint64) review.State {
	t.Helper()
	binding := review.SealBinding(review.Binding{TaskID: task.ID, RunID: record.RunID, CandidateID: record.ID,
		CandidateSHA: record.CommitSHA, BaseSHA: record.Manifest.BaseSHA, TreeSHA: record.Manifest.TreeSHA,
		ManifestSHA256: record.Manifest.BindingSHA256, AcceptanceSHA256: record.Manifest.AcceptanceSHA256,
		ConfigurationSHA256: record.Manifest.ConfigurationSHA256, ProfileSHA256: record.Manifest.ProfileSHA256,
		ContextSHA256: record.Manifest.ContextSHA256, DecisionsSHA256: record.Manifest.DecisionsSHA256,
		FindingsSHA256: record.Manifest.FindingsSHA256, CandidateGeneration: generation, CISlotID: "ci-slot-1",
		TaskAgentUUID: taskAgentUUID, CoordinatorUUID: coordUUID, ReviewOwnerUUID: ownerUUID})
	state, ok := review.NewState(binding, []string{"criterion-direct"}, []review.Probe{},
		review.ProfileDecision{Admitted: true, Code: "reviewer_profile_admitted"})
	if !ok {
		t.Fatal("new Review state rejected")
	}
	state.SourcePath, state.PrimaryPath = "/srv/source", "/srv/worktree"
	state.ReviewerRoot, state.CheckoutPath = "/srv/reviewers", "/srv/reviewers/candidate"
	state.PrimaryHeadSHA = record.CommitSHA
	checkout := review.SealCheckoutEvidence(review.CheckoutEvidence{CheckoutID: state.ReviewKey, OwnerUUID: ownerUUID,
		CandidateSHA: record.CommitSHA, TreeSHA: record.Manifest.TreeSHA, Detached: true, Clean: true,
		PrimaryDistinct: true, SourceUnchanged: true})
	state.CheckoutEvidence = &checkout
	state.Checkout.Phase, state.Checkout.ExternalID, state.Checkout.FactSHA256 = review.EffectComplete, state.ReviewKey, checkout.FactSHA256
	state.Workspace.Phase, state.Workspace.ExternalID, state.Workspace.FactSHA256 = review.EffectComplete, "workspace-review", strings.Repeat("1", 64)
	state.ReviewerUUID, state.ReviewerSessionSHA256 = reviewerUUID, strings.Repeat("2", 64)
	state.RegisteredAt, state.RegistrationSHA256 = "2026-09-11T00:00:00Z", strings.Repeat("3", 64)
	state.Bootstrap.Phase, state.Bootstrap.ExternalID, state.Bootstrap.FactSHA256 = review.EffectComplete, reviewerUUID, strings.Repeat("4", 64)
	ci := review.SealCIObservation(review.CIObservation{ID: "ci-observation-1", SlotID: binding.CISlotID,
		CandidateSHA: binding.CandidateSHA, BaseSHA: binding.BaseSHA, TreeSHA: binding.TreeSHA,
		ManifestSHA256: binding.ManifestSHA256, WorkflowRunID: "123456789", RequiredChecks: []string{"maintained-linux-ci"},
		Status: "passed", Authoritative: true, Complete: true, ObservedAtMillis: 1_001})
	state.CIObservation = &ci
	state.PromptSHA256 = strings.Repeat("5", 64)
	state.Prompt.Phase, state.Prompt.ExternalID, state.Prompt.FactSHA256 = review.EffectComplete, reviewerUUID, strings.Repeat("6", 64)
	evidence := review.Evidence{SchemaVersion: review.EvidenceSchemaVersion, ID: "review-evidence-1", ReviewKey: state.ReviewKey,
		BindingSHA256: binding.BindingSHA256, ReviewerUUID: reviewerUUID, HarnessReviewerUUID: reviewerUUID,
		CIObservationSHA256: ci.SHA256, CheckoutFactSHA256: checkout.FactSHA256,
		Verdict: review.VerdictApproveCandidate, ClaimSHA256: strings.Repeat("7", 64), ObservedAtMillis: 1_002}
	state.Evidence, state.VerdictDurablyObserved = &evidence, true
	if !review.ValidState(state) {
		t.Fatalf("approved Review state invalid: %#v", state)
	}
	return state
}

func newFixture(t *testing.T, mode directdomain.IntegrationMode) *fixture {
	t.Helper()
	remoteIdentity, err := repository.CanonicalRemote("https://example.invalid/direct.git")
	if err != nil {
		t.Fatal(err)
	}
	repositoryBinding := execution.RepositoryBinding{RepositoryID: remoteIdentity.ID, RepositoryKey: remoteIdentity.Key,
		CanonicalRemote: remoteIdentity.Canonical, SourcePath: "/srv/source", SourceDevice: 1, SourceInode: 2,
		GitCommonDirectory: "/srv/source/.git", GitCommonDevice: 1, GitCommonInode: 3,
		WorktreePath: "/srv/worktree", Branch: "task/direct", BaseSHA: strings.Repeat("b", 40)}
	repositorySHA := execution.RepositoryBindingSHA256(repositoryBinding)
	task := domain.Task{ID: "task-direct", ProjectID: "project-direct", Title: "Implement direct delivery",
		Objective: "Integrate without a pull request", AcceptanceCriteria: "Direct delivery is exact",
		WorkspaceIDs: []string{"workspace-direct"}, Version: 3}
	claim := candidate.Claim{SchemaVersion: candidate.ClaimSchemaVersion, ID: "claim-direct", ProjectID: task.ProjectID,
		WorkspaceID: "workspace-direct", TaskID: task.ID, RunID: "run-direct", ActorID: taskAgentUUID,
		WorktreeID: "worktree-direct", Branch: repositoryBinding.Branch, BaseRef: "refs/heads/main",
		CandidateSHA: strings.Repeat("a", 40), BaseSHA: repositoryBinding.BaseSHA, LeaseEpoch: 1,
		ExpectedRunVersion: 1, TaskVersion: task.Version, AcceptanceSHA256: strings.Repeat("8", 64),
		ConfigurationSHA256: strings.Repeat("9", 64), ProfileSHA256: strings.Repeat("a", 64),
		ContextSHA256: strings.Repeat("c", 64), DecisionsSHA256: strings.Repeat("d", 64),
		FindingsSHA256: strings.Repeat("e", 64)}
	observation := candidate.SealObservation(candidate.Observation{ClaimSHA256: candidate.ClaimSHA256(claim),
		RepositoryBindingSHA256: repositorySHA, ObservedAtMillis: 1_000, MaximumAgeMillis: candidate.MaximumObservationAgeMS,
		ObjectFormat: "sha1", CommitSHA: claim.CandidateSHA, BaseSHA: claim.BaseSHA, ParentSHA: claim.BaseSHA,
		TreeSHA: strings.Repeat("f", 40), BranchHeadSHA: claim.CandidateSHA, BaseRefHeadSHA: claim.BaseSHA,
		DiffSHA256: strings.Repeat("1", 64), ChangedPathsSHA256: strings.Repeat("2", 64),
		SourceDevice: 1, SourceInode: 2, CommonDevice: 1, CommonInode: 3, WorktreeDevice: 1, WorktreeInode: 4,
		RepositoryExact: true, RemoteExact: true, PathsCanonical: true, RegistrationExact: true,
		ObjectPresent: true, ObjectStoreOwned: true, BranchStable: true, BaseStable: true, DescendsFromBase: true,
		DirectParent: true, WorktreeClean: true, IndexClean: true, UntrackedAbsent: true, IgnoredAbsent: true,
		SubmodulesClean: true, ConflictFree: true, IntentToAddAbsent: true, SparseCheckoutAbsent: true,
		FilesystemExact: true, SnapshotSHA256: strings.Repeat("3", 64), Code: candidate.CodeOK})
	manifest := candidate.Evaluate(claim, observation, repositorySHA, 1_001).Manifest
	if manifest == nil {
		t.Fatal("Candidate manifest rejected")
	}
	record := domain.Candidate{SchemaVersion: domain.CandidateSchemaVersion, ID: "candidate-direct", RunID: claim.RunID,
		Sequence: 1, CommitSHA: claim.CandidateSHA, Claim: claim, Manifest: *manifest, AdmittedAtMillis: 1_001}
	authority := candidate.NewAuthority(0, record.ID, claim.Branch, task.Version, record.Manifest)
	reviewState := approvedReview(t, record, task, authority.Generation)
	authority.Downstream.Review = &candidate.EvidenceBinding{ID: reviewState.Evidence.ID, CandidateID: authority.CandidateID,
		CandidateSHA: authority.CandidateSHA, BaseSHA: authority.BaseSHA, Generation: authority.Generation,
		BindingSHA256: authority.BindingSHA256}
	run := domain.Run{ID: claim.RunID, TaskID: task.ID, Number: 1, BaseSHA: claim.BaseSHA,
		CurrentCandidateID: record.ID, Version: 5, Execution: execution.State{SchemaVersion: execution.SchemaVersion,
			Scope:             execution.Scope{ProjectID: task.ProjectID, WorkspaceID: claim.WorkspaceID, TaskID: task.ID, RunID: claim.RunID},
			LeaseBinding:      execution.LeaseBinding{HolderInstance: "engine-direct", HolderProcessIdentity: "process-direct", Epoch: 1},
			RepositoryBinding: repositoryBinding, RepositoryBindingHash: repositorySHA, Branch: claim.Branch, BaseRef: claim.BaseRef,
			Agent:              execution.Effect{ID: "agent-effect", Kind: execution.EffectAgentCreate, Phase: execution.EffectComplete, ExternalID: taskAgentUUID},
			DeliveryMode:       domainconfig.DeliveryDirect,
			CandidateAuthority: &authority, Review: &reviewState}}
	project := domain.Project{ID: task.ProjectID, Name: "Direct fixture", State: "active", Version: 1,
		Lease: &domain.ProjectLease{HolderInstance: "engine-direct", HolderProcessIdentity: "process-direct", Epoch: 1,
			AcquiredAtMillis: 100, RenewedAtMillis: 100, ExpiresAtMillis: 100_000, DispatchAllowed: true}}
	store := &memoryStore{project: project, task: task, run: run, candidate: record}
	remote := &fakeRemote{head: claim.BaseSHA}
	service, err := NewService(store, remote)
	if err != nil {
		t.Fatal(err)
	}
	policy := directdomain.Policy{DeliveryMode: "direct", IntegrationMode: mode,
		SelectionSource: directdomain.SelectionFrozenRunConfiguration, ConfigurationSHA256: claim.ConfigurationSHA256,
		AuthorizedTargetRefs: []string{claim.BaseRef}, AttemptLimit: 2}
	if mode == directdomain.IntegrationAutomatic {
		policy.AutomaticTargetRefs = []string{claim.BaseRef}
	}
	return &fixture{store: store, remote: remote, service: service, policy: directdomain.SealPolicy(policy)}
}

func directBlockingFeedback(t *testing.T, fixture *fixture) domainfeedback.State {
	t.Helper()
	run, task, record := fixture.store.run, fixture.store.task, fixture.store.candidate
	binding := domainfeedback.SealBinding(domainfeedback.Binding{ProjectID: task.ProjectID, WorkspaceID: task.WorkspaceIDs[0],
		TaskID: task.ID, TaskVersion: task.Version, RunID: run.ID, CandidateID: record.ID, CandidateSHA: record.CommitSHA,
		BaseSHA: record.Manifest.BaseSHA, CandidateGeneration: run.Execution.CandidateAuthority.Generation,
		ManifestSHA256: record.Manifest.BindingSHA256})
	item := domainfeedback.Item{Source: domainfeedback.SourcePaseoDirect, ExternalID: "feedback-1", RevisionID: "revision-1",
		Actor: domainfeedback.Actor{Kind: domainfeedback.ActorHuman, ID: "human-1", Login: "owner", Authenticated: true,
			Attestation: domainfeedback.AttestationPaseoHuman}, Kind: domainfeedback.KindComment,
		CandidateSHA: record.CommitSHA, BaseSHA: record.Manifest.BaseSHA, ContextSHA256: strings.Repeat("f", 64),
		Body: "Reopen current work before direct delivery", Actionable: true, Severity: correction.SeverityP3,
		CreatedAtMillis: 1_000, UpdatedAtMillis: 1_001}
	snapshot := domainfeedback.SealSnapshot(domainfeedback.Snapshot{ID: "feedback-snapshot-1", Source: domainfeedback.SourcePaseoDirect,
		BindingSHA256: binding.BindingSHA256, PageCount: 1, ObservedAtMillis: 1_002,
		MaximumAgeMillis: domainfeedback.MaximumObservationAge, Items: []domainfeedback.Item{item}})
	state, _, ok := domainfeedback.Reconcile(nil, binding, []domainfeedback.Snapshot{snapshot}, 1_002)
	if !ok || !domainfeedback.BlocksDelivery(state) {
		t.Fatal("blocking feedback fixture")
	}
	return state
}

func TestUnresolvedFeedbackBlocksDirectIntentBeforeRemoteObservation(t *testing.T) {
	fixture := newFixture(t, directdomain.IntegrationAutomatic)
	state := directBlockingFeedback(t, fixture)
	fixture.store.run.Execution.Feedback = &state
	run := fixture.store.run
	_, err := fixture.service.Admit(context.Background(), AdmitCommand{RunID: run.ID, ExpectedRunVersion: run.Version,
		LeaseEpoch: 1, Policy: fixture.policy, NowMillis: 2_000})
	if !errors.Is(err, ErrNotReady) || fixture.remote.observations != 0 || fixture.remote.pushes != 0 {
		t.Fatalf("feedback direct gate = %v observations=%d pushes=%d", err, fixture.remote.observations, fixture.remote.pushes)
	}
}

func admit(t *testing.T, fixture *fixture) domain.Run {
	t.Helper()
	run, _ := fixture.store.Run(context.Background(), "run-direct")
	result, err := fixture.service.Admit(context.Background(), AdmitCommand{RunID: run.ID,
		ExpectedRunVersion: run.Version, LeaseEpoch: 1, Policy: fixture.policy, NowMillis: 2_000})
	if err != nil {
		t.Fatal(err)
	}
	return result.Run
}

func step(t *testing.T, fixture *fixture) Result {
	t.Helper()
	run, _ := fixture.store.Run(context.Background(), "run-direct")
	result, err := fixture.service.Step(context.Background(), StepCommand{RunID: run.ID,
		ExpectedRunVersion: run.Version, LeaseEpoch: 1, NowMillis: 2_001})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestManualDirectWaitsForExplicitHumanActionThenIntegrates(t *testing.T) {
	fixture := newFixture(t, directdomain.IntegrationManual)
	run := admit(t, fixture)
	if run.Execution.DirectDelivery.Phase != directdomain.PhaseWaitingHuman || fixture.remote.pushes != 0 {
		t.Fatalf("manual admission = %#v, pushes=%d", run.Execution.DirectDelivery, fixture.remote.pushes)
	}
	if result := step(t, fixture); !result.WaitingHuman || fixture.remote.pushes != 0 {
		t.Fatalf("manual step = %#v, pushes=%d", result, fixture.remote.pushes)
	}
	run, _ = fixture.store.Run(context.Background(), run.ID)
	binding := run.Execution.DirectDelivery.Binding
	authorization := directdomain.SealAuthorization(directdomain.HumanAuthorization{ID: "human-authorization-1",
		ActorKind: "human", ActorSource: directdomain.AuthorizationActorSource, Authenticated: true,
		ActorID: "owner@example.invalid", DecisionID: "decision-direct-1", Action: "integrate_direct",
		BindingSHA256: binding.SHA256, CandidateSHA: binding.CandidateSHA, BaseSHA: binding.BaseSHA,
		TargetRef: binding.TargetRef, PolicySHA256: binding.PolicySHA256, AuthorizedAtMillis: 2_000})
	githubActor := AuthenticatedHumanActor{Kind: "human", ID: "owner@example.invalid", SessionID: "github-review-1", Source: "github_review", Authenticated: true}
	if _, err := fixture.service.AuthorizeManual(context.Background(), AuthorizeManualCommand{RunID: run.ID,
		ExpectedRunVersion: run.Version, LeaseEpoch: 1, Actor: githubActor, Authorization: authorization, NowMillis: 2_000}); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("GitHub review identity authorized direct integration: %v", err)
	}
	result, err := fixture.service.AuthorizeManual(context.Background(), AuthorizeManualCommand{RunID: run.ID,
		ExpectedRunVersion: run.Version, LeaseEpoch: 1,
		Actor:         AuthenticatedHumanActor{Kind: "human", ID: "owner@example.invalid", SessionID: "paseo-session-1", Source: "server", Authenticated: true},
		Authorization: authorization, NowMillis: 2_000})
	if err != nil || !result.Progressed {
		t.Fatalf("manual authorization = %#v, %v", result, err)
	}
	step(t, fixture)
	result = step(t, fixture)
	if !result.Integrated || fixture.remote.pushes != 1 || result.Run.Execution.CandidateAuthority.Downstream.Integration == nil {
		t.Fatalf("manual completion = %#v, pushes=%d", result, fixture.remote.pushes)
	}
}

func TestAutomaticDirectRecoversLostPushResponseWithoutSecondMutation(t *testing.T) {
	fixture := newFixture(t, directdomain.IntegrationAutomatic)
	admit(t, fixture)
	fixture.remote.loseNext = true
	run, _ := fixture.store.Run(context.Background(), "run-direct")
	result, err := fixture.service.Step(context.Background(), StepCommand{RunID: run.ID,
		ExpectedRunVersion: run.Version, LeaseEpoch: 1, NowMillis: 2_001})
	if !directport.HandoffPossible(err) {
		t.Fatalf("lost response = %#v, %v", result, err)
	}
	if fixture.remote.pushes != 1 || result.Run.Execution.DirectDelivery.Phase != directdomain.PhaseDispatching {
		t.Fatalf("lost-response frontier = %#v, pushes=%d", result.Run.Execution.DirectDelivery, fixture.remote.pushes)
	}
	restarted, restartErr := NewService(fixture.store, fixture.remote)
	if restartErr != nil {
		t.Fatal(restartErr)
	}
	fixture.service = restarted
	run, _ = fixture.store.Run(context.Background(), "run-direct")
	result, err = restarted.Step(context.Background(), StepCommand{RunID: run.ID,
		ExpectedRunVersion: run.Version, LeaseEpoch: 1, NowMillis: 2_002})
	if err != nil || !result.Integrated || fixture.remote.pushes != 1 {
		t.Fatalf("restart completion = %#v, pushes=%d, error=%v", result, fixture.remote.pushes, err)
	}
}

func TestConcurrentCoordinatorsDispatchOneExactPush(t *testing.T) {
	fixture := newFixture(t, directdomain.IntegrationAutomatic)
	run := admit(t, fixture)
	var wait sync.WaitGroup
	wait.Add(32)
	for range 32 {
		go func() {
			defer wait.Done()
			_, _ = fixture.service.Step(context.Background(), StepCommand{RunID: run.ID,
				ExpectedRunVersion: run.Version, LeaseEpoch: 1, NowMillis: 2_001})
		}()
	}
	wait.Wait()
	if fixture.remote.pushes != 1 {
		t.Fatalf("concurrent push count = %d", fixture.remote.pushes)
	}
	step(t, fixture)
	stored, _ := fixture.store.Run(context.Background(), run.ID)
	if stored.Execution.DirectDelivery.Phase != directdomain.PhaseComplete {
		t.Fatalf("concurrent completion = %#v", stored.Execution.DirectDelivery)
	}
}

func TestPRPublicationAuthorityCanNeverBecomeDirectFallback(t *testing.T) {
	fixture := newFixture(t, directdomain.IntegrationAutomatic)
	fixture.store.run.Execution.CandidateAuthority.Downstream.Publication = &candidate.EvidenceBinding{ID: "publication-1",
		CandidateID: fixture.store.candidate.ID, CandidateSHA: fixture.store.candidate.CommitSHA,
		BaseSHA: fixture.store.candidate.Manifest.BaseSHA, Generation: 1,
		BindingSHA256: fixture.store.candidate.Manifest.BindingSHA256}
	run := fixture.store.run
	result, err := fixture.service.Admit(context.Background(), AdmitCommand{RunID: run.ID,
		ExpectedRunVersion: run.Version, LeaseEpoch: 1, Policy: fixture.policy, NowMillis: 2_000})
	if !errors.Is(err, ErrFallbackForbidden) || result.Code == "" || fixture.store.run.Execution.DirectDelivery != nil || fixture.remote.pushes != 0 {
		t.Fatalf("fallback result = %#v, error=%v, pushes=%d", result, err, fixture.remote.pushes)
	}

	fixture = newFixture(t, directdomain.IntegrationAutomatic)
	fixture.store.run.Execution.DeliveryMode = domainconfig.DeliveryPullRequest
	run = fixture.store.run
	if _, err = fixture.service.Admit(context.Background(), AdmitCommand{RunID: run.ID,
		ExpectedRunVersion: run.Version, LeaseEpoch: 1, Policy: fixture.policy, NowMillis: 2_000}); !errors.Is(err, ErrNotReady) ||
		fixture.store.run.Execution.DirectDelivery != nil || fixture.remote.pushes != 0 {
		t.Fatalf("unpublished PR failure selected direct delivery: error=%v, pushes=%d", err, fixture.remote.pushes)
	}
}

func TestAmbiguousPublicationStateBlocksDirectBeforeObservationOrPush(t *testing.T) {
	fixture := newFixture(t, directdomain.IntegrationAutomatic)
	fixture.store.run.Execution.Publication = &publicationdomain.State{}
	run := fixture.store.run
	if _, err := fixture.service.Admit(context.Background(), AdmitCommand{RunID: run.ID,
		ExpectedRunVersion: run.Version, LeaseEpoch: 1, Policy: fixture.policy, NowMillis: 2_000}); !errors.Is(err, ErrFallbackForbidden) ||
		fixture.remote.observations != 0 || fixture.remote.pushes != 0 {
		t.Fatalf("ambiguous PR state admitted direct: error=%v, observations=%d, pushes=%d", err, fixture.remote.observations, fixture.remote.pushes)
	}

	fixture = newFixture(t, directdomain.IntegrationAutomatic)
	run = admit(t, fixture)
	fixture.store.run.Execution.Publication = &publicationdomain.State{}
	version := fixture.store.run.Version
	result, err := fixture.service.Step(context.Background(), StepCommand{RunID: run.ID,
		ExpectedRunVersion: version, LeaseEpoch: 1, NowMillis: 2_001})
	if !errors.Is(err, ErrFallbackForbidden) || result.Run.Version != version ||
		fixture.remote.observations != 0 || fixture.remote.pushes != 0 {
		t.Fatalf("ambiguous PR/direct reconciliation = %#v, error=%v, observations=%d, pushes=%d",
			result, err, fixture.remote.observations, fixture.remote.pushes)
	}
}

func TestRemoteMovementAndOutageFailClosedWithoutPush(t *testing.T) {
	fixture := newFixture(t, directdomain.IntegrationAutomatic)
	admit(t, fixture)
	fixture.remote.head = strings.Repeat("c", 40)
	result := step(t, fixture)
	if result.Code == "" || result.Run.Execution.DirectDelivery.Phase != directdomain.PhaseInvalidated ||
		!result.Run.Execution.CandidateAuthority.Invalidated || fixture.remote.pushes != 0 {
		t.Fatalf("moved remote result = %#v, pushes=%d", result, fixture.remote.pushes)
	}

	fixture = newFixture(t, directdomain.IntegrationAutomatic)
	admit(t, fixture)
	fixture.remote.unavailable = true
	result = step(t, fixture)
	if !result.WaitingExternal || result.Run.Execution.DirectDelivery.Phase != directdomain.PhaseWaitingExternal || fixture.remote.pushes != 0 {
		t.Fatalf("unavailable result = %#v, pushes=%d", result, fixture.remote.pushes)
	}
	fixture.remote.unavailable = false
	step(t, fixture)
	result = step(t, fixture)
	if !result.Integrated || fixture.remote.pushes != 1 {
		t.Fatalf("outage recovery = %#v, pushes=%d", result, fixture.remote.pushes)
	}
}

func TestUnknownRemoteFailureRetriesOnlyFromFreshExactBaseAndStopsAtBudget(t *testing.T) {
	fixture := newFixture(t, directdomain.IntegrationAutomatic)
	admit(t, fixture)
	fixture.remote.failPushes = 2
	for attempt := 1; attempt <= 2; attempt++ {
		run, _ := fixture.store.Run(context.Background(), "run-direct")
		result, err := fixture.service.Step(context.Background(), StepCommand{RunID: run.ID,
			ExpectedRunVersion: run.Version, LeaseEpoch: 1, NowMillis: int64(2_000 + attempt)})
		if !directport.HandoffPossible(err) || result.Run.Execution.DirectDelivery.Integration.Attempt != uint32(attempt) {
			t.Fatalf("unknown attempt %d = %#v, %v", attempt, result, err)
		}
	}
	result := step(t, fixture)
	if result.Code == "" || result.Run.Execution.DirectDelivery.Phase != directdomain.PhaseNeedsYou ||
		fixture.remote.pushes != 2 || fixture.remote.head != fixture.store.candidate.Manifest.BaseSHA {
		t.Fatalf("exhausted remote failure = %#v, pushes=%d, head=%s", result, fixture.remote.pushes, fixture.remote.head)
	}
}

func TestStaleLeaseAndActiveCorrectionCannotAdmitDirectIntent(t *testing.T) {
	fixture := newFixture(t, directdomain.IntegrationAutomatic)
	fixture.store.project.Lease.ExpiresAtMillis = 1_500
	run := fixture.store.run
	if _, err := fixture.service.Admit(context.Background(), AdmitCommand{RunID: run.ID,
		ExpectedRunVersion: run.Version, LeaseEpoch: 1, Policy: fixture.policy, NowMillis: 2_000}); !errors.Is(err, ErrConcurrentTransition) {
		t.Fatalf("stale lease error = %v", err)
	}
	fixture = newFixture(t, directdomain.IntegrationAutomatic)
	fixture.store.run.Execution.Correction = &correction.State{}
	run = fixture.store.run
	if _, err := fixture.service.Admit(context.Background(), AdmitCommand{RunID: run.ID,
		ExpectedRunVersion: run.Version, LeaseEpoch: 1, Policy: fixture.policy, NowMillis: 2_000}); !errors.Is(err, ErrNotReady) {
		t.Fatalf("active correction error = %v", err)
	}
}

func TestStaleLeaseCannotPersistOrRepeatAnUnavailableRemoteObservation(t *testing.T) {
	fixture := newFixture(t, directdomain.IntegrationAutomatic)
	admit(t, fixture)
	fixture.remote.unavailable = true
	first := step(t, fixture)
	if !first.WaitingExternal || fixture.remote.observations != 1 {
		t.Fatalf("initial outage = %#v, observations=%d", first, fixture.remote.observations)
	}
	fixture.store.project.Lease.ExpiresAtMillis = 2_001
	run, _ := fixture.store.Run(context.Background(), "run-direct")
	version := run.Version
	result, err := fixture.service.Step(context.Background(), StepCommand{RunID: run.ID,
		ExpectedRunVersion: run.Version, LeaseEpoch: 1, NowMillis: 2_002})
	if err != nil || !result.WaitingExternal || fixture.remote.observations != 1 || result.Run.Version != version {
		t.Fatalf("stale-lease outage = %#v, observations=%d, error=%v", result, fixture.remote.observations, err)
	}
}
