// SPDX-License-Identifier: Apache-2.0

package dolt_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/mcuadros/director-engine/domain"
	candidatedomain "github.com/mcuadros/director-engine/domain/candidate"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	correctiondomain "github.com/mcuadros/director-engine/domain/correction"
	"github.com/mcuadros/director-engine/domain/execution"
	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
)

func TestCorrectionStateAndPriorAuthorityHistorySurviveDoltReopen(t *testing.T) {
	fixture := startDoltFixture(t)
	const storeID = "correction-contract-store"
	store := openContractStore(t, fixture, storeID, true)
	ctx := context.Background()
	project := domain.Project{ID: "project-fixture", Name: "Correction fixture", State: "active", Organizer: testOrganizer("project-fixture")}
	workspace := testWorkspace(t, project.ID, "workspace-fixture", "/srv/correction", "https://github.com/example/correction.git")
	result, err := store.CreateProject(ctx, command("correction-project", "project.create", project.ID, 0, `{}`), project,
		[]domain.Workspace{workspace}, event("correction-project-event", "", 1, project.ID, 0, "project.created"))
	requireApplied(t, result, err)
	task := domain.Task{ID: "task-fixture", ProjectID: project.ID, Title: "Correction fixture", Objective: "Persist correction state",
		AcceptanceCriteria: "Prior authority remains historical", WorkspaceIDs: []string{workspace.ID}}
	result, err = store.CreateTask(ctx, command("correction-task", "task.create", task.ID, 0, `{}`), task,
		event("correction-task-event", "", 1, task.ID, 0, "task.created"))
	requireApplied(t, result, err)
	publicationPolicy := publicationdomain.NewPolicy("pull_request", true, []string{"release"})
	run := domain.Run{ID: "run-correction", TaskID: task.ID, Number: 1, BaseSHA: strings.Repeat("0", 40), Execution: execution.State{
		SchemaVersion: execution.SchemaVersion, Scope: execution.Scope{ProjectID: project.ID, WorkspaceID: workspace.ID, TaskID: task.ID, RunID: "run-correction"},
		DeliveryMode: domainconfig.DeliveryPullRequest, PublicationPolicy: &publicationPolicy,
	}}
	result, err = store.CreateRun(ctx, command("correction-run", "run.create", run.ID, 0, `{}`), run,
		event("correction-run-event", run.ID, 1, run.ID, 0, "run.created"))
	requireApplied(t, result, err)
	first := storedCandidate("candidate-first", run.ID, 1, strings.Repeat("1", 40))
	result, err = store.AppendCandidate(ctx, command("correction-first", "candidate.append", run.ID, 0, `{}`), first,
		event("correction-first-event", run.ID, 2, run.ID, 1, "candidate.appended"))
	requireApplied(t, result, err)
	run, err = store.Run(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	bound := func(id string) *candidatedomain.EvidenceBinding {
		return &candidatedomain.EvidenceBinding{ID: id, CandidateID: first.ID,
			CandidateSHA: first.CommitSHA, BaseSHA: first.Manifest.BaseSHA, Generation: run.Execution.CandidateAuthority.Generation,
			BindingSHA256: first.Manifest.BindingSHA256}
	}
	publicationBinding := publicationdomain.SealBinding(publicationdomain.Binding{TaskID: task.ID, RunID: run.ID,
		CandidateID: first.ID, CandidateSHA: first.CommitSHA, BaseSHA: first.Manifest.BaseSHA, TreeSHA: first.Manifest.TreeSHA,
		ManifestSHA256: first.Manifest.BindingSHA256, CandidateGeneration: run.Execution.CandidateAuthority.Generation,
		TaskVersion: run.Execution.CandidateAuthority.TaskVersion, Branch: first.Claim.Branch, BaseRef: first.Claim.BaseRef,
		RepositoryBindingSHA256: first.Manifest.RepositoryBindingSHA256, CanonicalRemote: "https://github.com/example/correction",
		GitHubRepositoryID: 123, GitHubRepositoryNodeID: "R_correction", RepositoryOwner: "example",
		RepositoryName: "correction", HeadOwner: "example", OwnershipSHA256: strings.Repeat("a", 64),
		PolicySHA256: publicationPolicy.SHA256})
	publicationTemplate, ok := publicationdomain.RenderTemplate(publicationBinding, publicationPolicy, task.Title,
		"pending", "", "pending", "", []string{})
	if !ok {
		t.Fatal("publication template rejected")
	}
	publication, ok := publicationdomain.NewState(publicationBinding, publicationPolicy, publicationTemplate, "", nil)
	if !ok {
		t.Fatal("publication state rejected")
	}
	publication.Push.Phase, publication.PullRequest.Phase, publication.Metadata.Phase =
		publicationdomain.EffectComplete, publicationdomain.EffectComplete, publicationdomain.EffectComplete
	publication.OwnedPullRequest = &publicationdomain.OwnedPullRequest{Number: 7, NodeID: "PR_correction",
		URL: "https://github.com/example/correction/pull/7", MarkerSHA256: publicationdomain.DigestText(publicationdomain.Marker(publicationBinding)),
		HeadSHA: first.CommitSHA, Draft: true}
	publication.Evidence = publicationdomain.EvidenceFor(publication, false, 1_001)
	if publication.Evidence == nil || !publicationdomain.ValidState(publication) {
		t.Fatal("publication draft evidence rejected")
	}
	run.Execution.PublicationPolicy, run.Execution.Publication = &publicationPolicy, &publication
	run.Execution.CandidateAuthority.Downstream = candidatedomain.Downstream{Validation: bound("validation-1"), Review: bound("review-1"),
		CI: bound("ci-1"), Publication: bound(publication.Evidence.ID), Feedback: bound("feedback-1"), Ready: bound("ready-1"), Integration: bound("integration-1")}
	snapshots := []correctiondomain.SourceSnapshot{{Source: correctiondomain.SourceReview, Revision: strings.Repeat("1", 64), Count: 1},
		{Source: correctiondomain.SourceValidation, Revision: strings.Repeat("2", 64), Count: 0},
		{Source: correctiondomain.SourceCI, Revision: strings.Repeat("3", 64), Count: 0},
		{Source: correctiondomain.SourceHuman, Revision: strings.Repeat("4", 64), Count: 0}}
	batch, ok := correctiondomain.Canonicalize(first.ID, first.CommitSHA, []string{"criterion-1"}, snapshots, []correctiondomain.FindingInput{{
		ID: "review-1", Source: correctiondomain.SourceReview, Class: "CORRECTNESS", Severity: correctiondomain.SeverityP1,
		CandidateID: first.ID, CandidateSHA: first.CommitSHA, Summary: "Bounded correction finding",
		Evidence:           []correctiondomain.Evidence{{ID: "review-evidence", Kind: correctiondomain.EvidenceReviewFinding, SHA256: strings.Repeat("5", 64)}},
		AcceptanceCoverage: []string{"criterion-1"}, Blocking: true,
	}})
	if !ok {
		t.Fatal("correction batch rejected")
	}
	state, ok := correctiondomain.NewState(task.ID, run.ID, first.ID, first.CommitSHA, "11111111-1111-4111-8111-111111111111",
		[]string{"criterion-1"}, strings.Repeat("6", 64), strings.Repeat("7", 64), strings.Repeat("8", 64), strings.Repeat("9", 64),
		correctiondomain.Policy{AutoFixCIFailures: true, AutoFixReviewFeedback: true, AttemptLimit: 3}, batch)
	if !ok {
		t.Fatal("correction state rejected")
	}
	run.Execution.Correction = &state
	run.Version++
	result, err = store.UpdateRun(ctx, command("correction-state", "correction.state", run.ID, 1, `{}`), run,
		event("correction-state-event", run.ID, 3, run.ID, 2, "correction.state"))
	requireApplied(t, result, err)
	second := storedCandidate("candidate-second", run.ID, 2, strings.Repeat("2", 40))
	result, err = store.AppendCandidate(ctx, command("correction-second", "candidate.correction.admit", run.ID, 2, `{}`), second,
		event("correction-second-event", run.ID, 4, run.ID, 3, "candidate.correction.admitted"))
	requireApplied(t, result, err)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openContractStore(t, fixture, storeID, true)
	t.Cleanup(func() { _ = reopened.Close() })
	stored, err := reopened.Run(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CurrentCandidateID != second.ID || stored.Execution.CandidateAuthority == nil ||
		stored.Execution.CandidateAuthority.Generation != 2 || !candidatedomain.DownstreamEmpty(stored.Execution.CandidateAuthority.Downstream) ||
		len(stored.Execution.CandidateAuthorityHistory) != 1 || !reflect.DeepEqual(stored.Execution.CandidateAuthorityHistory[0].Authority.Downstream, run.Execution.CandidateAuthority.Downstream) ||
		!candidatedomain.ValidHistoricalAuthority(stored.Execution.CandidateAuthorityHistory[0]) || stored.Execution.Correction == nil ||
		!correctiondomain.ValidState(*stored.Execution.Correction) || stored.Execution.FindingContextSHA256 != second.Manifest.FindingsSHA256 ||
		stored.Execution.Publication != nil || len(stored.Execution.PublicationHistory) != 1 ||
		!stored.Execution.PublicationHistory[0].Invalidated || stored.Execution.PublicationHistory[0].Evidence != nil ||
		stored.Execution.PublicationHistory[0].OwnedPullRequest == nil ||
		stored.Execution.PublicationHistory[0].OwnedPullRequest.Number != publication.OwnedPullRequest.Number {
		t.Fatalf("reopened correction Run = %#v", stored)
	}
}
