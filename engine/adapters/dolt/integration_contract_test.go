// SPDX-License-Identifier: Apache-2.0

package dolt_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mcuadros/director-engine/domain"
	candidatedomain "github.com/mcuadros/director-engine/domain/candidate"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	"github.com/mcuadros/director-engine/domain/execution"
	integrationdomain "github.com/mcuadros/director-engine/domain/integration"
	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
	reviewdomain "github.com/mcuadros/director-engine/domain/review"
	validationdomain "github.com/mcuadros/director-engine/domain/validation"
)

func durableValidation(t *testing.T, task domain.Task, run domain.Run, record domain.Candidate) (validationdomain.Policy, validationdomain.State) {
	t.Helper()
	policy, ok := validationdomain.NewPolicy(99, "maintained-linux-ci", []validationdomain.RequiredCheck{{ID: "maintained-linux-ci",
		Kind: validationdomain.CheckRunKind, Name: "Linux CI", AppID: 15368, AppSlug: "github-actions"}}, 10_000)
	if !ok {
		t.Fatal("validation policy")
	}
	authority := run.Execution.CandidateAuthority
	binding := validationdomain.SealBinding(validationdomain.Binding{TaskID: task.ID, RunID: run.ID, CandidateID: record.ID,
		CandidateSHA: record.CommitSHA, BaseSHA: record.Manifest.BaseSHA, TreeSHA: record.Manifest.TreeSHA,
		ManifestSHA256: record.Manifest.BindingSHA256, CandidateGeneration: authority.Generation, CISlotID: "direct-ci-slot",
		BaseRef: record.Claim.BaseRef, RepositoryBindingSHA256: record.Manifest.RepositoryBindingSHA256,
		CanonicalRemote: "https://github.com/example/product", GitHubRepositoryID: 123, GitHubRepositoryNodeID: "R_node",
		RepositoryOwner: "example", RepositoryName: "product", ViewerLogin: "example", PolicySHA256: policy.SHA256})
	state, ok := validationdomain.NewState(binding, policy, 1)
	if !ok {
		t.Fatal("validation state")
	}
	now := int64(2_000)
	repositoryBefore := validationdomain.SealRepositoryObservation(validationdomain.RepositoryObservation{ID: "repository-before", Code: validationdomain.CodeOK,
		RepositoryID: 123, RepositoryNodeID: "R_node", Owner: "example", Name: "product", ViewerLogin: "example", Authenticated: true, CanReadChecks: true, TLSVerified: true,
		APIVersion: "2022-11-28", RateRemaining: 100, ObservedAtMillis: now, MaximumAgeMillis: validationdomain.MaximumObservationAgeMS})
	repositoryAfter := repositoryBefore
	repositoryAfter.ID = "repository-after"
	repositoryAfter = validationdomain.SealRepositoryObservation(repositoryAfter)
	workflows := validationdomain.SealWorkflowScan(validationdomain.WorkflowScan{Pages: 1, TotalCount: 1, Runs: []validationdomain.WorkflowRun{{ID: 500, WorkflowID: 99,
		Name: "maintained-linux-ci", HeadSHA: record.CommitSHA, HeadRepositoryID: 123, CheckSuiteID: 700, Status: "completed", Conclusion: "success", Attempt: 1, StartedAtMillis: 100, UpdatedAtMillis: 200}}})
	checks := validationdomain.SealCheckScan(validationdomain.CheckScan{Pages: 1, TotalCount: 1, Checks: []validationdomain.CheckRun{{ID: 600, Name: "Linux CI",
		HeadSHA: record.CommitSHA, SuiteID: 700, SuiteHeadSHA: record.CommitSHA, AppID: 15368, AppSlug: "github-actions", Status: "completed", Conclusion: "success",
		DetailsURLSHA256: strings.Repeat("1", 64), StartedAtMillis: 100, CompletedAtMillis: 200}}})
	statuses := validationdomain.SealStatusScan(validationdomain.StatusScan{Pages: 1, CombinedState: "checks_only_no_statuses", Statuses: []validationdomain.CommitStatus{}})
	baseFact := strings.Repeat("2", 64)
	decision := validationdomain.Evaluate(binding, policy, workflows, checks, statuses, validationdomain.RepositoryObservationSHA256(repositoryBefore),
		validationdomain.RepositoryObservationSHA256(repositoryAfter), baseFact, baseFact, now)
	if decision.Evidence == nil || decision.Outcome != validationdomain.OutcomePassed {
		t.Fatalf("validation = %#v", decision)
	}
	state.Repository, state.RepositoryAfter, state.Workflows, state.Checks, state.Statuses = &repositoryBefore, &repositoryAfter, &workflows, &checks, &statuses
	state.BaseBeforeSHA256, state.BaseAfterSHA256, state.Evidence, state.Phase, state.Code = baseFact, baseFact, decision.Evidence, validationdomain.PhasePassed, validationdomain.CodeOK
	if !validationdomain.ValidState(state) {
		t.Fatal("durable validation invalid")
	}
	return policy, state
}

func integrationObservation(binding integrationdomain.Binding, attempt uint32, integrated bool) integrationdomain.Observation {
	now := int64(2_100)
	merge := ""
	forgeStatus := integrationdomain.ForgeReady
	gitStatus := integrationdomain.GitReady
	status := integrationdomain.ObservationReady
	if integrated {
		merge = strings.Repeat("d", 40)
		forgeStatus = integrationdomain.ForgeIntegrated
		gitStatus = integrationdomain.GitIntegrated
		status = integrationdomain.ObservationIntegrated
	}
	forge := integrationdomain.ForgeObservation{ID: "forge-observation", Status: forgeStatus, Code: integrationdomain.CodeOK, RepositoryID: binding.GitHubRepositoryID,
		RepositoryNodeID: binding.GitHubRepositoryNodeID, Owner: binding.RepositoryOwner, Name: binding.RepositoryName, ViewerLogin: binding.ViewerLogin,
		Authenticated: true, CanMerge: true, TLSVerified: true, RateRemaining: 100, APIVersion: "2022-11-28", AtomicExpectedHead: true, DefaultBranch: "main",
		PullRequestNumber: binding.PullRequestNumber, PullRequestNodeID: binding.PullRequestNodeID, Draft: false, HeadSHA: binding.CandidateSHA, HeadRef: binding.Branch,
		BaseRef: "main", HeadRepositoryID: binding.GitHubRepositoryID, BaseRepositoryID: binding.GitHubRepositoryID, HeadOwner: binding.ViewerLogin, AuthorLogin: binding.ViewerLogin,
		MarkerSHA256: binding.MarkerSHA256, MarkerCount: 1,
		ObservedAtMillis: now, MaximumAgeMillis: integrationdomain.MaximumObservationAgeMS}
	if integrated {
		forge.PullRequestState, forge.Merged, forge.MergedAtMillis, forge.MergeCommitSHA = "closed", true, now, merge
	} else {
		forge.PullRequestState, forge.Mergeable, forge.MergeableState = "open", true, "clean"
	}
	sealedForge := integrationdomain.SealForgeObservation(forge)
	git := integrationdomain.GitObservation{ID: "git-observation", Status: gitStatus, Code: integrationdomain.CodeOK, RepositoryID: binding.RepositoryID, CanonicalRemote: binding.CanonicalRemote,
		HeadRef: "refs/heads/" + binding.Branch, HeadSHA: binding.CandidateSHA, BaseRef: binding.BaseRef, BaseSHA: binding.BaseSHA, DefaultRef: binding.BaseRef, DefaultSHA: binding.BaseSHA,
		ObservedAtMillis: now, MaximumAgeMillis: integrationdomain.MaximumObservationAgeMS}
	if integrated {
		git.BaseSHA, git.DefaultSHA, git.MergeCommitSHA = merge, merge, merge
		git.ParentSHAs = []string{binding.BaseSHA, binding.CandidateSHA}
		git.TreeSHA = binding.TreeSHA
	}
	sealedGit := integrationdomain.SealGitObservation(git)
	return integrationdomain.SealObservation(integrationdomain.Observation{BindingSHA256: binding.SHA256, Attempt: attempt, Status: status, Code: integrationdomain.CodeOK,
		ForgeBefore: sealedForge, ForgeAfter: sealedForge, GitBefore: sealedGit, GitAfter: sealedGit, LiveValidationID: "live-validation", LiveValidationSHA256: strings.Repeat("3", 64),
		ConfiguredCheckCount: 1, ObservedCheckCount: 1, FeedbackSnapshotSHA256: strings.Repeat("4", 64), FeedbackClear: true, MergeCommitSHA: merge,
		ObservedAtMillis: now, MaximumAgeMillis: integrationdomain.MaximumObservationAgeMS})
}

func TestIntegrationIntentDispatchObservationAndEvidenceSurviveDoltReopen(t *testing.T) {
	fixture := startDoltFixture(t)
	const storeID = "integration-contract-store"
	store := openContractStore(t, fixture, storeID, true)
	ctx := context.Background()
	project := domain.Project{ID: "project-fixture", Name: "Integration fixture", State: "active", Organizer: testOrganizer("project-fixture")}
	workspace := testWorkspace(t, project.ID, "workspace-fixture", "/srv/integration", "https://github.com/example/product.git")
	result, err := store.CreateProject(ctx, command("integration-project", "project.create", project.ID, 0, `{}`), project, []domain.Workspace{workspace}, event("integration-project-event", "", 1, project.ID, 0, "project.created"))
	requireApplied(t, result, err)
	task := domain.Task{ID: "task-fixture", ProjectID: project.ID, Title: "Integration fixture", Objective: "Persist integration", AcceptanceCriteria: "Every frontier survives restart", WorkspaceIDs: []string{workspace.ID}, Version: 0}
	result, err = store.CreateTask(ctx, command("integration-task", "task.create", task.ID, 0, `{}`), task, event("integration-task-event", "", 1, task.ID, 0, "task.created"))
	requireApplied(t, result, err)
	publicationPolicy := publicationdomain.NewPolicy("pull_request", true, nil)
	validationPolicy, _ := validationdomain.NewPolicy(99, "maintained-linux-ci", []validationdomain.RequiredCheck{{ID: "maintained-linux-ci", Kind: validationdomain.CheckRunKind, Name: "Linux CI", AppID: 15368, AppSlug: "github-actions"}}, 10_000)
	integrationPolicy, _ := integrationdomain.NewPolicy(integrationdomain.ModeAutomatic, strings.Repeat("b", 64))
	run := domain.Run{ID: "run-integration", TaskID: task.ID, Number: 1, BaseSHA: strings.Repeat("0", 40), Execution: execution.State{SchemaVersion: execution.SchemaVersion,
		Scope: execution.Scope{ProjectID: project.ID, WorkspaceID: workspace.ID, TaskID: task.ID, RunID: "run-integration"}, DeliveryMode: domainconfig.DeliveryPullRequest,
		PublicationPolicy: &publicationPolicy, ValidationPolicy: &validationPolicy, IntegrationPolicy: &integrationPolicy}}
	result, err = store.CreateRun(ctx, command("integration-run", "run.create", run.ID, 0, `{}`), run, event("integration-run-event", run.ID, 1, run.ID, 0, "run.created"))
	requireApplied(t, result, err)
	record := storedCandidate("candidate-integration", run.ID, 1, strings.Repeat("1", 40))
	result, err = store.AppendCandidate(ctx, command("integration-candidate", "candidate.append", run.ID, 0, `{}`), record, event("integration-candidate-event", run.ID, 2, run.ID, 1, "candidate.appended"))
	requireApplied(t, result, err)
	run, err = store.Run(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	task, err = store.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, validation := durableValidation(t, task, run, record)
	review := durableApprovedReview(t, task, run, record)
	ci, ok := validationdomain.ReviewObservation(validation)
	if !ok {
		t.Fatal("CI observation")
	}
	review.CIObservation = &ci
	review.Evidence.CIObservationSHA256 = ci.SHA256
	if !reviewdomain.ValidState(review) {
		t.Fatal("review invalid")
	}
	authority := *run.Execution.CandidateAuthority
	publicationBinding := publicationdomain.SealBinding(publicationdomain.Binding{TaskID: task.ID, RunID: run.ID, CandidateID: record.ID, CandidateSHA: record.CommitSHA,
		BaseSHA: record.Manifest.BaseSHA, TreeSHA: record.Manifest.TreeSHA, ManifestSHA256: record.Manifest.BindingSHA256, CandidateGeneration: authority.Generation, TaskVersion: authority.TaskVersion,
		Branch: record.Claim.Branch, BaseRef: record.Claim.BaseRef, RepositoryBindingSHA256: record.Manifest.RepositoryBindingSHA256, CanonicalRemote: "https://github.com/example/product",
		GitHubRepositoryID: 123, GitHubRepositoryNodeID: "R_node", RepositoryOwner: "example", RepositoryName: "product", HeadOwner: "example", OwnershipSHA256: strings.Repeat("5", 64), PolicySHA256: publicationPolicy.SHA256})
	template, ok := publicationdomain.RenderTemplate(publicationBinding, publicationPolicy, task.Title, "approve_candidate", review.Evidence.ID, "passed", validation.Evidence.ID, nil)
	if !ok {
		t.Fatal("template")
	}
	publication, ok := publicationdomain.NewState(publicationBinding, publicationPolicy, template, "", nil)
	if !ok {
		t.Fatal("publication")
	}
	publication.Push.Phase, publication.PullRequest.Phase, publication.Metadata.Phase, publication.Ready.Phase = publicationdomain.EffectComplete, publicationdomain.EffectComplete, publicationdomain.EffectComplete, publicationdomain.EffectComplete
	publication.OwnedPullRequest = &publicationdomain.OwnedPullRequest{Number: 7, NodeID: "PR_node_7", URL: "https://github.com/example/product/pull/7", MarkerSHA256: publicationdomain.DigestText(publicationdomain.Marker(publicationBinding)), HeadSHA: record.CommitSHA, Draft: false}
	publication.Evidence = publicationdomain.EvidenceFor(publication, true, 2_050)
	if publication.Evidence == nil || !publicationdomain.ValidState(publication) {
		t.Fatal("ready publication")
	}
	readyID := "integration-ready-1"
	binding := integrationdomain.SealBinding(integrationdomain.Binding{TaskID: task.ID, RunID: run.ID, CandidateID: record.ID, CandidateSHA: record.CommitSHA,
		BaseSHA: record.Manifest.BaseSHA, TreeSHA: record.Manifest.TreeSHA, ManifestSHA256: record.Manifest.BindingSHA256, CandidateGeneration: authority.Generation, TaskVersion: authority.TaskVersion,
		ConfigurationSHA256: record.Manifest.ConfigurationSHA256, RepositoryID: "repository-fixture", RepositoryBindingSHA256: record.Manifest.RepositoryBindingSHA256, CanonicalRemote: publicationBinding.CanonicalRemote,
		GitHubRepositoryID: 123, GitHubRepositoryNodeID: "R_node", RepositoryOwner: "example", RepositoryName: "product", ViewerLogin: "example", Branch: record.Claim.Branch, BaseRef: record.Claim.BaseRef,
		PullRequestNumber: 7, PullRequestNodeID: "PR_node_7", PublicationEvidenceID: publication.Evidence.ID, ValidationPolicySHA256: validationPolicy.SHA256, ValidationEvidenceID: validation.Evidence.ID,
		OwnershipSHA256: publicationBinding.OwnershipSHA256, MarkerSHA256: publication.OwnedPullRequest.MarkerSHA256,
		ValidationEvidenceSHA256: validation.Evidence.SHA256, ReviewEvidenceID: review.Evidence.ID, ReviewerUUID: review.Evidence.ReviewerUUID, CIObservationID: review.CIObservation.ID,
		CIObservationSHA256: review.CIObservation.SHA256, FeedbackStateSHA256: integrationdomain.DigestText("no-current-feedback"), ReadyEvidenceID: readyID, PolicySHA256: integrationPolicy.SHA256, LeaseEpoch: 1})
	integration, ok := integrationdomain.NewState(binding, integrationPolicy)
	if !ok {
		t.Fatal("integration state")
	}
	precondition := integrationObservation(binding, 0, false)
	integration, ok = integrationdomain.RecordObservation(integration, precondition, 2_100)
	if !ok {
		t.Fatal("integration observation")
	}
	integration, ok = integrationdomain.BeginDispatch(integration)
	if !ok {
		t.Fatal("integration dispatch")
	}
	authority.Downstream.Validation = &candidatedomain.EvidenceBinding{ID: validation.Evidence.ID, CandidateID: authority.CandidateID, CandidateSHA: authority.CandidateSHA, BaseSHA: authority.BaseSHA, Generation: authority.Generation, BindingSHA256: authority.BindingSHA256}
	ciBinding := *authority.Downstream.Validation
	authority.Downstream.CI = &ciBinding
	authority.Downstream.Review = &candidatedomain.EvidenceBinding{ID: review.Evidence.ID, CandidateID: authority.CandidateID, CandidateSHA: authority.CandidateSHA, BaseSHA: authority.BaseSHA, Generation: authority.Generation, BindingSHA256: authority.BindingSHA256}
	authority.Downstream.Publication = &candidatedomain.EvidenceBinding{ID: publication.Evidence.ID, CandidateID: authority.CandidateID, CandidateSHA: authority.CandidateSHA, BaseSHA: authority.BaseSHA, Generation: authority.Generation, BindingSHA256: authority.BindingSHA256}
	authority.Downstream.Ready = &candidatedomain.EvidenceBinding{ID: readyID, CandidateID: authority.CandidateID, CandidateSHA: authority.CandidateSHA, BaseSHA: authority.BaseSHA, Generation: authority.Generation, BindingSHA256: authority.BindingSHA256}
	run.Execution.CandidateAuthority = &authority
	run.Execution.DeliveryMode = domainconfig.DeliveryPullRequest
	run.Execution.RepositoryBinding = execution.RepositoryBinding{RepositoryID: binding.RepositoryID}
	run.Execution.RepositoryBindingHash = binding.RepositoryBindingSHA256
	run.Execution.PublicationPolicy = &publicationPolicy
	run.Execution.ValidationPolicy = &validationPolicy
	run.Execution.IntegrationPolicy = &integrationPolicy
	run.Execution.Publication = &publication
	run.Execution.Validation = &validation
	run.Execution.Review = &review
	run.Execution.Integration = &integration
	run.Version++
	result, err = store.UpdateRun(ctx, command("integration-dispatch", "integration.dispatching", run.ID, 1, `{}`), run, event("integration-dispatch-event", run.ID, 3, run.ID, 2, "integration.dispatching"))
	requireApplied(t, result, err)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openContractStore(t, fixture, storeID, true)
	stored, err := store.Run(ctx, run.ID)
	if err != nil || stored.Execution.Integration == nil || !integrationdomain.DispatchInFlight(*stored.Execution.Integration) {
		t.Fatalf("reopened dispatch = %#v %v", stored.Execution.Integration, err)
	}
	required, ok := integrationdomain.RequireObservation(*stored.Execution.Integration)
	if !ok {
		t.Fatal("require observation")
	}
	stored.Execution.Integration = &required
	stored.Version++
	result, err = store.UpdateRun(ctx, command("integration-observe", "integration.observation_required", stored.ID, 2, `{}`), stored, event("integration-observe-event", stored.ID, 4, stored.ID, 3, "integration.observation_required"))
	requireApplied(t, result, err)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openContractStore(t, fixture, storeID, true)
	stored, err = store.Run(ctx, run.ID)
	if err != nil || stored.Execution.Integration.Phase != integrationdomain.PhaseObservationRequired {
		t.Fatalf("reopened observation = %#v %v", stored.Execution.Integration, err)
	}
	desired := integrationObservation(binding, 1, true)
	completed, ok := integrationdomain.RecordObservation(*stored.Execution.Integration, desired, 2_100)
	if !ok {
		t.Fatal("record integrated")
	}
	completed, ok = integrationdomain.Complete(completed, desired, 2_100)
	if !ok {
		t.Fatal("complete integration")
	}
	authority = *stored.Execution.CandidateAuthority
	authority.Downstream.Integration = &candidatedomain.EvidenceBinding{ID: completed.Evidence.ID, CandidateID: authority.CandidateID, CandidateSHA: authority.CandidateSHA, BaseSHA: authority.BaseSHA, Generation: authority.Generation, BindingSHA256: authority.BindingSHA256}
	stored.Execution.CandidateAuthority = &authority
	stored.Execution.Integration = &completed
	stored.Version++
	result, err = store.UpdateRun(ctx, command("integration-complete", "integration.complete", stored.ID, 3, `{}`), stored, event("integration-complete-event", stored.ID, 5, stored.ID, 4, "integration.complete"))
	requireApplied(t, result, err)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openContractStore(t, fixture, storeID, true)
	t.Cleanup(func() { _ = store.Close() })
	final, err := store.Run(ctx, run.ID)
	if err != nil || final.Execution.Integration == nil || final.Execution.Integration.Phase != integrationdomain.PhaseComplete || !integrationdomain.ValidState(*final.Execution.Integration) || final.Execution.CandidateAuthority.Downstream.Integration == nil {
		t.Fatalf("reopened integration = %#v %v", final.Execution.Integration, err)
	}
}
