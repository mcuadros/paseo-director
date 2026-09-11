// SPDX-License-Identifier: Apache-2.0

package dolt_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mcuadros/director-engine/domain"
	candidatedomain "github.com/mcuadros/director-engine/domain/candidate"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	directdomain "github.com/mcuadros/director-engine/domain/directdelivery"
	"github.com/mcuadros/director-engine/domain/execution"
	reviewdomain "github.com/mcuadros/director-engine/domain/review"
	storeport "github.com/mcuadros/director-engine/ports/taskstore"
)

func durableApprovedReview(t *testing.T, task domain.Task, run domain.Run, record domain.Candidate) reviewdomain.State {
	t.Helper()
	binding := reviewdomain.SealBinding(reviewdomain.Binding{TaskID: task.ID, RunID: run.ID, CandidateID: record.ID,
		CandidateSHA: record.CommitSHA, BaseSHA: record.Manifest.BaseSHA, TreeSHA: record.Manifest.TreeSHA,
		ManifestSHA256: record.Manifest.BindingSHA256, AcceptanceSHA256: record.Manifest.AcceptanceSHA256,
		ConfigurationSHA256: record.Manifest.ConfigurationSHA256, ProfileSHA256: record.Manifest.ProfileSHA256,
		ContextSHA256: record.Manifest.ContextSHA256, DecisionsSHA256: record.Manifest.DecisionsSHA256,
		FindingsSHA256: record.Manifest.FindingsSHA256, CandidateGeneration: run.Execution.CandidateAuthority.Generation,
		CISlotID: "direct-ci-slot", TaskAgentUUID: "11111111-1111-4111-8111-111111111111",
		CoordinatorUUID: "22222222-2222-4222-8222-222222222222", ReviewOwnerUUID: "33333333-3333-4333-8333-333333333333"})
	state, ok := reviewdomain.NewState(binding, []string{"criterion-direct"}, []reviewdomain.Probe{},
		reviewdomain.ProfileDecision{Admitted: true, Code: "reviewer_profile_admitted"})
	if !ok {
		t.Fatal("new durable Review state rejected")
	}
	state.SourcePath, state.PrimaryPath = "/srv/source", "/srv/worktree"
	state.ReviewerRoot, state.CheckoutPath = "/srv/reviewers", "/srv/reviewers/candidate"
	state.PrimaryHeadSHA = record.CommitSHA
	checkout := reviewdomain.SealCheckoutEvidence(reviewdomain.CheckoutEvidence{CheckoutID: state.ReviewKey,
		OwnerUUID: binding.ReviewOwnerUUID, CandidateSHA: record.CommitSHA, TreeSHA: record.Manifest.TreeSHA,
		Detached: true, Clean: true, PrimaryDistinct: true, SourceUnchanged: true})
	state.CheckoutEvidence = &checkout
	state.Checkout.Phase, state.Checkout.ExternalID, state.Checkout.FactSHA256 = reviewdomain.EffectComplete, state.ReviewKey, checkout.FactSHA256
	state.Workspace.Phase, state.Workspace.ExternalID, state.Workspace.FactSHA256 = reviewdomain.EffectComplete, "workspace-direct-review", strings.Repeat("1", 64)
	state.ReviewerUUID, state.ReviewerSessionSHA256 = "44444444-4444-4444-8444-444444444444", strings.Repeat("2", 64)
	state.RegisteredAt, state.RegistrationSHA256 = "2026-09-11T00:00:00Z", strings.Repeat("3", 64)
	state.Bootstrap.Phase, state.Bootstrap.ExternalID, state.Bootstrap.FactSHA256 = reviewdomain.EffectComplete, state.ReviewerUUID, strings.Repeat("4", 64)
	ci := reviewdomain.SealCIObservation(reviewdomain.CIObservation{ID: "direct-ci-observation", SlotID: binding.CISlotID,
		CandidateSHA: binding.CandidateSHA, BaseSHA: binding.BaseSHA, TreeSHA: binding.TreeSHA,
		ManifestSHA256: binding.ManifestSHA256, WorkflowRunID: "987654321", RequiredChecks: []string{"maintained-linux-ci"},
		Status: "passed", Authoritative: true, Complete: true, ObservedAtMillis: 1_001})
	state.CIObservation = &ci
	state.PromptSHA256 = strings.Repeat("5", 64)
	state.Prompt.Phase, state.Prompt.ExternalID, state.Prompt.FactSHA256 = reviewdomain.EffectComplete, state.ReviewerUUID, strings.Repeat("6", 64)
	evidence := reviewdomain.Evidence{SchemaVersion: reviewdomain.EvidenceSchemaVersion, ID: "direct-review-evidence",
		ReviewKey: state.ReviewKey, BindingSHA256: binding.BindingSHA256, ReviewerUUID: state.ReviewerUUID,
		HarnessReviewerUUID: state.ReviewerUUID, CIObservationSHA256: ci.SHA256, CheckoutFactSHA256: checkout.FactSHA256,
		Verdict: reviewdomain.VerdictApproveCandidate, ClaimSHA256: strings.Repeat("7", 64), ObservedAtMillis: 1_002}
	state.Evidence, state.VerdictDurablyObserved = &evidence, true
	if !reviewdomain.ValidState(state) {
		t.Fatal("durable approved Review state rejected")
	}
	return state
}

func directEvidenceBinding(authority *candidatedomain.Authority, id string) *candidatedomain.EvidenceBinding {
	return &candidatedomain.EvidenceBinding{ID: id, CandidateID: authority.CandidateID,
		CandidateSHA: authority.CandidateSHA, BaseSHA: authority.BaseSHA, Generation: authority.Generation,
		BindingSHA256: authority.BindingSHA256}
}

func TestDirectDeliveryFrontierAndCorrectionHistorySurviveDoltReopen(t *testing.T) {
	fixture := startDoltFixture(t)
	const storeID = "direct-delivery-contract-store"
	store := openContractStore(t, fixture, storeID, true)
	ctx := context.Background()
	project := domain.Project{ID: "project-fixture", Name: "Direct delivery fixture", State: "active", Organizer: testOrganizer("project-fixture")}
	workspace := testWorkspace(t, project.ID, "workspace-fixture", "/srv/direct", "https://github.com/example/direct.git")
	result, err := store.CreateProject(ctx, command("direct-project", "project.create", project.ID, 0, `{}`), project,
		[]domain.Workspace{workspace}, event("direct-project-event", "", 1, project.ID, 0, "project.created"))
	requireApplied(t, result, err)
	task := domain.Task{ID: "task-fixture", ProjectID: project.ID, Title: "Direct delivery fixture",
		Objective: "Persist direct delivery", AcceptanceCriteria: "Exact state survives restart", WorkspaceIDs: []string{workspace.ID}, Version: 0}
	result, err = store.CreateTask(ctx, command("direct-task", "task.create", task.ID, 0, `{}`), task,
		event("direct-task-event", "", 1, task.ID, 0, "task.created"))
	requireApplied(t, result, err)
	run := domain.Run{ID: "run-direct", TaskID: task.ID, Number: 1, BaseSHA: strings.Repeat("0", 40),
		Execution: execution.State{SchemaVersion: execution.SchemaVersion,
			Scope:        execution.Scope{ProjectID: project.ID, WorkspaceID: workspace.ID, TaskID: task.ID, RunID: "run-direct"},
			DeliveryMode: domainconfig.DeliveryDirect}}
	result, err = store.CreateRun(ctx, command("direct-run", "run.create", run.ID, 0, `{}`), run,
		event("direct-run-event", run.ID, 1, run.ID, 0, "run.created"))
	requireApplied(t, result, err)
	first := storedCandidate("candidate-direct-first", run.ID, 1, strings.Repeat("1", 40))
	result, err = store.AppendCandidate(ctx, command("direct-first", "candidate.append", run.ID, 0, `{}`), first,
		event("direct-first-event", run.ID, 2, run.ID, 1, "candidate.appended"))
	requireApplied(t, result, err)
	run, err = store.Run(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	reviewState := durableApprovedReview(t, task, run, first)
	run.Execution.Review = &reviewState
	run.Execution.DeliveryMode = domainconfig.DeliveryDirect
	authority := *run.Execution.CandidateAuthority
	authority.Downstream.Review = directEvidenceBinding(&authority, reviewState.Evidence.ID)
	policy := directdomain.SealPolicy(directdomain.Policy{DeliveryMode: "direct", IntegrationMode: directdomain.IntegrationAutomatic,
		SelectionSource: directdomain.SelectionFrozenRunConfiguration, ConfigurationSHA256: first.Manifest.ConfigurationSHA256,
		AuthorizedTargetRefs: []string{"refs/heads/main"}, AutomaticTargetRefs: []string{"refs/heads/main"}, AttemptLimit: 2})
	binding := directdomain.SealBinding(directdomain.Binding{TaskID: task.ID, RunID: run.ID, CandidateID: first.ID,
		CandidateSHA: first.CommitSHA, BaseSHA: first.Manifest.BaseSHA, TreeSHA: first.Manifest.TreeSHA,
		ManifestSHA256: first.Manifest.BindingSHA256, CandidateGeneration: authority.Generation, TaskVersion: authority.TaskVersion,
		ConfigurationSHA256: first.Manifest.ConfigurationSHA256, RepositoryID: "repository-fixture",
		RepositoryBindingSHA256: first.Manifest.RepositoryBindingSHA256, TargetRef: "refs/heads/main", PolicySHA256: policy.SHA256,
		ReviewEvidenceID: reviewState.Evidence.ID, ReviewerUUID: reviewState.ReviewerUUID,
		CIObservationID: reviewState.CIObservation.ID, CIObservationSHA256: reviewState.CIObservation.SHA256, LeaseEpoch: 1})
	direct, ok := directdomain.NewState(binding, policy)
	if !ok {
		t.Fatal("durable direct state rejected")
	}
	precondition := directdomain.SealObservation(directdomain.Observation{EffectID: direct.Integration.ID,
		BindingSHA256: binding.SHA256, Status: directdomain.ObservationCurrentExpected, Code: directdomain.CodeOK,
		RepositoryID: binding.RepositoryID, TargetRef: binding.TargetRef, CurrentSHA: binding.BaseSHA,
		ObservedAtMillis: 1_003, MaximumAgeMillis: directdomain.MaximumObservationAgeMS})
	direct, ok = directdomain.RecordObservation(direct, precondition)
	if !ok {
		t.Fatal("record durable direct observation")
	}
	direct, ok = directdomain.BeginDispatch(direct)
	if !ok {
		t.Fatal("begin durable direct dispatch")
	}
	authority.Downstream.CI = directEvidenceBinding(&authority, reviewState.CIObservation.ID)
	authority.Downstream.Ready = directEvidenceBinding(&authority, direct.ID+"-ready")
	run.Execution.CandidateAuthority = &authority
	run.Execution.RepositoryBinding = execution.RepositoryBinding{RepositoryID: binding.RepositoryID}
	run.Execution.RepositoryBindingHash = binding.RepositoryBindingSHA256
	run.Execution.DirectDelivery = &direct
	run.Version++
	result, err = store.UpdateRun(ctx, command("direct-dispatch", "direct_delivery.dispatching", run.ID, 1, `{}`), run,
		event("direct-dispatch-event", run.ID, 3, run.ID, 2, "direct_delivery.dispatching"))
	requireApplied(t, result, err)
	blocked := storedCandidate("candidate-direct-blocked", run.ID, 2, strings.Repeat("2", 40))
	_, err = store.AppendCandidate(ctx, command("direct-blocked", "candidate.correction.admit", run.ID, 2, `{}`), blocked,
		event("direct-blocked-event", run.ID, 4, run.ID, 3, "candidate.correction.admitted"))
	if !errors.Is(err, storeport.ErrReferentialIntegrity) {
		t.Fatalf("Candidate replacement during unreconciled direct dispatch = %v", err)
	}
	unchanged, err := store.Run(ctx, run.ID)
	if err != nil || unchanged.Version != 2 || unchanged.CurrentCandidateID != first.ID ||
		unchanged.Execution.DirectDelivery == nil || !directdomain.DispatchInFlight(*unchanged.Execution.DirectDelivery) {
		t.Fatalf("in-flight direct frontier changed = %#v, %v", unchanged, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openContractStore(t, fixture, storeID, true)
	stored, err := reopened.Run(ctx, run.ID)
	if err != nil || stored.Execution.DirectDelivery == nil || stored.Execution.DirectDelivery.Phase != directdomain.PhaseDispatching ||
		!directdomain.ValidState(*stored.Execution.DirectDelivery) {
		t.Fatalf("reopened dispatch frontier = %#v, %v", stored.Execution.DirectDelivery, err)
	}
	desired := directdomain.SealObservation(directdomain.Observation{EffectID: direct.Integration.ID,
		BindingSHA256: binding.SHA256, Attempt: 1, Status: directdomain.ObservationDesired, Code: directdomain.CodeOK,
		RepositoryID: binding.RepositoryID, TargetRef: binding.TargetRef, CurrentSHA: binding.CandidateSHA,
		ObservedAtMillis: 1_004, MaximumAgeMillis: directdomain.MaximumObservationAgeMS})
	direct, ok = directdomain.RecordObservation(*stored.Execution.DirectDelivery, desired)
	if !ok {
		t.Fatal("record desired state after restart")
	}
	direct, ok = directdomain.Complete(direct, desired, 1_004)
	if !ok {
		t.Fatal("complete direct delivery after restart")
	}
	authority = *stored.Execution.CandidateAuthority
	authority.Downstream.Integration = directEvidenceBinding(&authority, direct.Evidence.ID)
	stored.Execution.CandidateAuthority = &authority
	stored.Execution.DirectDelivery = &direct
	stored.Version++
	result, err = reopened.UpdateRun(ctx, command("direct-complete", "direct_delivery.integrated", stored.ID, 2, `{}`), stored,
		event("direct-complete-event", stored.ID, 4, stored.ID, 3, "direct_delivery.integrated"))
	requireApplied(t, result, err)
	second := storedCandidate("candidate-direct-second", stored.ID, 2, strings.Repeat("2", 40))
	result, err = reopened.AppendCandidate(ctx, command("direct-second", "candidate.correction.admit", stored.ID, 3, `{}`), second,
		event("direct-second-event", stored.ID, 5, stored.ID, 4, "candidate.correction.admitted"))
	requireApplied(t, result, err)
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	finalStore := openContractStore(t, fixture, storeID, true)
	t.Cleanup(func() { _ = finalStore.Close() })
	final, err := finalStore.Run(ctx, stored.ID)
	if err != nil || final.CurrentCandidateID != second.ID || final.Execution.DirectDelivery != nil ||
		len(final.Execution.DirectDeliveryHistory) != 1 || final.Execution.DirectDeliveryHistory[0].Phase != directdomain.PhaseInvalidated ||
		final.Execution.DirectDeliveryHistory[0].Evidence == nil || !directdomain.ValidState(final.Execution.DirectDeliveryHistory[0]) {
		t.Fatalf("reopened correction history = %#v, %v", final, err)
	}
}
