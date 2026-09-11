// SPDX-License-Identifier: Apache-2.0

package dolt_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/candidate"
	"github.com/mcuadros/director-engine/domain/correction"
	"github.com/mcuadros/director-engine/domain/execution"
	feedbackdomain "github.com/mcuadros/director-engine/domain/feedback"
)

func TestFeedbackAuditReplayAndCandidateInvalidationSurviveDoltReopen(t *testing.T) {
	fixture := startDoltFixture(t)
	const storeID = "feedback-contract-store"
	store := openContractStore(t, fixture, storeID, true)
	ctx := context.Background()
	project := domain.Project{ID: "project-fixture", Name: "Feedback fixture", State: "active", Organizer: testOrganizer("project-fixture")}
	workspace := testWorkspace(t, project.ID, "workspace-fixture", "/srv/feedback", "https://github.com/example/feedback.git")
	result, err := store.CreateProject(ctx, command("feedback-project", "project.create", project.ID, 0, `{}`), project,
		[]domain.Workspace{workspace}, event("feedback-project-event", "", 1, project.ID, 0, "project.created"))
	requireApplied(t, result, err)
	task := domain.Task{ID: "task-fixture", ProjectID: project.ID, Key: "DIR-FEEDBACK", Title: "Feedback fixture",
		Objective: "Persist human feedback", AcceptanceCriteria: "Feedback audit survives restart",
		WorkspaceIDs: []string{workspace.ID}, Version: 0}
	result, err = store.CreateTask(ctx, command("feedback-task", "task.create", task.ID, 0, `{}`), task,
		event("feedback-task-event", "", 1, task.ID, 0, "task.created"))
	requireApplied(t, result, err)
	task.Version = 1
	result, err = store.UpdateTask(ctx, command("feedback-task-update", "task.update", task.ID, 0, `{}`), task,
		event("feedback-task-update-event", "", 2, task.ID, 1, "task.updated"))
	requireApplied(t, result, err)
	run := domain.Run{ID: "run-feedback", TaskID: task.ID, Number: 1, BaseSHA: strings.Repeat("0", 40), Execution: execution.State{
		SchemaVersion: execution.SchemaVersion, Scope: execution.Scope{ProjectID: project.ID, WorkspaceID: workspace.ID, TaskID: task.ID, RunID: "run-feedback"},
	}}
	result, err = store.CreateRun(ctx, command("feedback-run", "run.create", run.ID, 0, `{}`), run,
		event("feedback-run-event", run.ID, 1, run.ID, 0, "run.created"))
	requireApplied(t, result, err)
	first := storedCandidate("candidate-first", run.ID, 1, strings.Repeat("1", 40))
	result, err = store.AppendCandidate(ctx, command("feedback-candidate", "candidate.append", run.ID, 0, `{}`), first,
		event("feedback-candidate-event", run.ID, 2, run.ID, 1, "candidate.appended"))
	requireApplied(t, result, err)
	run, err = store.Run(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	binding := feedbackdomain.SealBinding(feedbackdomain.Binding{ProjectID: project.ID, WorkspaceID: workspace.ID, TaskID: task.ID,
		TaskVersion: task.Version, RunID: run.ID, CandidateID: first.ID, CandidateSHA: first.CommitSHA,
		BaseSHA: first.Manifest.BaseSHA, CandidateGeneration: run.Execution.CandidateAuthority.Generation,
		ManifestSHA256: first.Manifest.BindingSHA256})
	item := feedbackdomain.Item{Source: feedbackdomain.SourcePaseoDirect, ExternalID: "message-1", RevisionID: "revision-1",
		Actor: feedbackdomain.Actor{Kind: feedbackdomain.ActorHuman, ID: "human-1", Login: "owner", Authenticated: true,
			Attestation: feedbackdomain.AttestationPaseoHuman}, Kind: feedbackdomain.KindComment,
		CandidateSHA: first.CommitSHA, BaseSHA: first.Manifest.BaseSHA, ContextSHA256: strings.Repeat("6", 64),
		Body: "Please preserve this bounded feedback", Actionable: true, Severity: correction.SeverityP3,
		CreatedAtMillis: 1_000, UpdatedAtMillis: 1_001}
	snapshot := feedbackdomain.SealSnapshot(feedbackdomain.Snapshot{ID: "feedback-snapshot-1", Source: feedbackdomain.SourcePaseoDirect,
		BindingSHA256: binding.BindingSHA256, PageCount: 1, ObservedAtMillis: 1_002,
		MaximumAgeMillis: feedbackdomain.MaximumObservationAge, Items: []feedbackdomain.Item{item}})
	state, _, ok := feedbackdomain.Reconcile(nil, binding, []feedbackdomain.Snapshot{snapshot}, 1_002)
	if !ok {
		t.Fatal("feedback state rejected")
	}
	run.Execution.Feedback = &state
	authority := *run.Execution.CandidateAuthority
	authority.Downstream.Feedback = &candidate.EvidenceBinding{ID: "feedback-evidence-1", CandidateID: authority.CandidateID,
		CandidateSHA: authority.CandidateSHA, BaseSHA: authority.BaseSHA, Generation: authority.Generation, BindingSHA256: authority.BindingSHA256}
	run.Execution.CandidateAuthority = &authority
	run.Version++
	result, err = store.UpdateRun(ctx, command("feedback-state", "feedback.observed", run.ID, 1, `{"state":"`+state.SHA256+`"}`), run,
		event("feedback-state-event", run.ID, 3, run.ID, 2, "feedback.observed"))
	requireApplied(t, result, err)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openContractStore(t, fixture, storeID, true)
	stored, err := reopened.Run(ctx, run.ID)
	if err != nil || stored.Execution.Feedback == nil || !reflect.DeepEqual(*stored.Execution.Feedback, state) ||
		stored.Execution.CandidateAuthority.Downstream.Feedback == nil {
		t.Fatalf("reopened feedback = %#v, %v", stored.Execution.Feedback, err)
	}
	second := storedCandidate("candidate-second", run.ID, 2, strings.Repeat("2", 40))
	result, err = reopened.AppendCandidate(ctx, command("feedback-second", "candidate.correction.admit", run.ID, 2, `{}`), second,
		event("feedback-second-event", run.ID, 4, run.ID, 3, "candidate.correction.admitted"))
	requireApplied(t, result, err)
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	reopened = openContractStore(t, fixture, storeID, true)
	t.Cleanup(func() { _ = reopened.Close() })
	stored, err = reopened.Run(ctx, run.ID)
	if err != nil || stored.Execution.Feedback != nil || len(stored.Execution.FeedbackHistory) != 1 ||
		!stored.Execution.FeedbackHistory[0].Invalidated || stored.Execution.FeedbackHistory[0].InvalidationCode != "candidate_changed" ||
		!reflect.DeepEqual(stored.Execution.FeedbackHistory[0].Records, state.Records) || !candidate.DownstreamEmpty(stored.Execution.CandidateAuthority.Downstream) {
		t.Fatalf("historical feedback = %#v, %v", stored.Execution.FeedbackHistory, err)
	}
}
