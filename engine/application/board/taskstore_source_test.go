// SPDX-License-Identifier: Apache-2.0

package board

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/mcuadros/director-engine/domain"
	domainexecution "github.com/mcuadros/director-engine/domain/execution"
	domainvalidation "github.com/mcuadros/director-engine/domain/validation"
	"github.com/mcuadros/director-engine/projection"
)

type planningFactStore struct {
	projects            []domain.Project
	workspaces          map[string][]domain.Workspace
	epics               map[string][]domain.Epic
	tasks               map[string][]domain.Task
	overrides           map[string][]domain.DependencyOverride
	runs                map[string][]domain.Run
	candidates          map[string]domain.Candidate
	cursor              uint64
	bulkRunReads        int
	bulkCandidateReads  int
	bulkTaskUpdateReads int
}

func TestIntegrationNeedsYouReasonAndWakeConditionReachBoardProjection(t *testing.T) {
	task := domain.Task{ID: "task-integration", Version: 3}
	run := domain.Run{Execution: domainexecution.State{NeedsYou: &domainexecution.NeedsYou{
		Code: "integration_feedback_present", WakeCondition: "fresh_candidate_validation_review_feedback_and_publication",
		CleanupAuthorized: false,
	}}}
	fact := normalizedHumanInput(task, &run)
	if fact.State != projection.HumanInputPending || fact.Code != projection.AttentionFeedbackDecisionRequired ||
		fact.ReasonCode != "integration_feedback_present" || fact.WakeCondition != "fresh_candidate_validation_review_feedback_and_publication" {
		t.Fatalf("integration attention = %#v", fact)
	}
}

func TestBaseInvalidationProjectsAnExactBoardOrganizerReason(t *testing.T) {
	policy, ok := domainvalidation.NewPolicy(99, "maintained-linux-ci", []domainvalidation.RequiredCheck{{ID: "linux-ci",
		Kind: domainvalidation.CheckRunKind, Name: "Linux CI", AppID: 15368, AppSlug: "github-actions"}}, 60_000)
	if !ok {
		t.Fatal("policy")
	}
	binding := domainvalidation.SealBinding(domainvalidation.Binding{TaskID: "task-1", RunID: "run-1", CandidateID: "candidate-1",
		CandidateSHA: strings.Repeat("b", 40), BaseSHA: strings.Repeat("a", 40), TreeSHA: strings.Repeat("c", 40),
		ManifestSHA256: strings.Repeat("1", 64), CandidateGeneration: 1, CISlotID: "ci-slot-1", BaseRef: "refs/heads/main",
		RepositoryBindingSHA256: strings.Repeat("2", 64), CanonicalRemote: "https://github.com/example/product",
		GitHubRepositoryID: 123, GitHubRepositoryNodeID: "R_node", RepositoryOwner: "example", RepositoryName: "product",
		ViewerLogin: "example", PolicySHA256: policy.SHA256})
	state, ok := domainvalidation.NewState(binding, policy, 1)
	if !ok {
		t.Fatal("state")
	}
	state = domainvalidation.Invalidate(state, domainvalidation.CodeBaseChanged)
	run := domain.Run{ID: "run-1", TaskID: "task-1", Number: 1, CurrentCandidateID: "candidate-1",
		Execution: domainexecution.State{SchemaVersion: domainexecution.SchemaVersion, ValidationPolicy: &policy, Validation: &state}}
	task := domain.Task{ID: "task-1", ProjectID: "project-1", WorkspaceIDs: []string{"workspace-1"}, Version: 1}
	record := domain.Candidate{ID: "candidate-1", RunID: "run-1"}
	facts := taskStateFacts(domain.Project{ID: "project-1", State: "active"}, task, false, &run, &record)
	result := projection.DeriveTaskProjection(facts)
	if result.State != projection.StateValidating || !slices.Contains(result.Blockers, projection.BlockerBaseRevalidationRequired) ||
		!slices.Contains(result.Blockers, projection.BlockerValidationStale) {
		t.Fatalf("projection = %#v", result)
	}
}

func (store *planningFactStore) Projects(context.Context) ([]domain.Project, error) {
	return append([]domain.Project(nil), store.projects...), nil
}

func (store *planningFactStore) Workspaces(_ context.Context, projectID string) ([]domain.Workspace, error) {
	return append([]domain.Workspace(nil), store.workspaces[projectID]...), nil
}

func (store *planningFactStore) Epics(_ context.Context, projectID string) ([]domain.Epic, error) {
	return append([]domain.Epic(nil), store.epics[projectID]...), nil
}

func (store *planningFactStore) Tasks(_ context.Context, projectID string) ([]domain.Task, error) {
	return append([]domain.Task(nil), store.tasks[projectID]...), nil
}

func (store *planningFactStore) DependencyOverrides(
	_ context.Context,
	projectID string,
) ([]domain.DependencyOverride, error) {
	return append([]domain.DependencyOverride(nil), store.overrides[projectID]...), nil
}

func (store *planningFactStore) Runs(_ context.Context, taskID string) ([]domain.Run, error) {
	return append([]domain.Run(nil), store.runs[taskID]...), nil
}

func (store *planningFactStore) Candidate(_ context.Context, id string) (domain.Candidate, error) {
	candidate, ok := store.candidates[id]
	if !ok {
		return domain.Candidate{}, errors.New("Candidate missing")
	}
	return candidate, nil
}

func (store *planningFactStore) PlanningRuns(_ context.Context, _ string) ([]domain.Run, error) {
	store.bulkRunReads++
	var runs []domain.Run
	for _, values := range store.runs {
		runs = append(runs, values...)
	}
	return runs, nil
}

func (store *planningFactStore) PlanningCandidates(_ context.Context, _ string) ([]domain.Candidate, error) {
	store.bulkCandidateReads++
	candidates := make([]domain.Candidate, 0, len(store.candidates))
	for _, candidate := range store.candidates {
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

func (store *planningFactStore) PlanningTaskUpdatedAt(_ context.Context, projectID string) (map[string]int64, error) {
	store.bulkTaskUpdateReads++
	updates := make(map[string]int64)
	for _, task := range store.tasks[projectID] {
		updates[task.ID] = task.QueuedAtUnixMillis + 1_000
	}
	return updates, nil
}

func (store *planningFactStore) LatestEventSequence(context.Context) (uint64, error) {
	return store.cursor, nil
}

func TestTaskStoreFactSourceNormalizesPlanningAndExecutionWithoutOwningState(t *testing.T) {
	project := domain.Project{ID: "project-1", Name: "Director", State: "active"}
	workspace := domain.Workspace{ID: "workspace-1", ProjectID: project.ID}
	blocker := domain.Task{
		ID: "task-blocker", ProjectID: project.ID, Title: "Blocker", Objective: "Block",
		AcceptanceCriteria: "Remain incomplete", WorkspaceIDs: []string{workspace.ID}, Priority: domain.PriorityNormal,
	}
	edge := domain.PlanningDependency{
		From: domain.PlanningNodeRef{Kind: domain.PlanningNodeTask, ID: "task-dependent"},
		On:   domain.PlanningNodeRef{Kind: domain.PlanningNodeTask, ID: blocker.ID},
	}
	dependent := domain.Task{
		ID: "task-dependent", ProjectID: project.ID, Title: "Dependent", Objective: "Wait",
		AcceptanceCriteria: "Dependency completes", WorkspaceIDs: []string{workspace.ID},
		Priority: domain.PriorityUrgent, Dependencies: []domain.PlanningDependency{edge}, QueuedAtUnixMillis: 20,
	}
	store := &planningFactStore{
		projects: []domain.Project{project}, workspaces: map[string][]domain.Workspace{project.ID: {workspace}},
		epics: map[string][]domain.Epic{}, tasks: map[string][]domain.Task{project.ID: {blocker, dependent}},
		overrides: map[string][]domain.DependencyOverride{}, runs: map[string][]domain.Run{},
		candidates: map[string]domain.Candidate{}, cursor: 11,
	}
	page, err := NewDerivedReader(NewTaskStoreFactSource(store)).Query(context.Background(), projection.TaskQuery{
		Membership: projection.TaskMembershipAll, Limit: projection.MaximumBoardTasks,
	})
	if err != nil || page.SnapshotCursor != "11" || len(page.Tasks) != 2 {
		t.Fatalf("derived TaskStore page = %#v, %v", page, err)
	}
	for _, row := range page.Tasks {
		if row.Projection.State != projection.StateQueued {
			t.Fatalf("Task without a Run left Queued: %#v", row)
		}
		if row.TaskID == dependent.ID && !slices.Contains(row.Projection.Blockers, projection.BlockerDependencyWait) {
			t.Fatalf("dependency wait missing from normalized projection: %#v", row)
		}
	}

	run := domain.Run{ID: "run-dependent", TaskID: dependent.ID, Number: 1, CurrentCandidateID: "candidate-1"}
	store.runs[dependent.ID] = []domain.Run{run}
	store.candidates["candidate-1"] = domain.Candidate{ID: "candidate-1", RunID: run.ID}
	inputs, err := NewTaskStoreFactSource(store).TaskProjectionInputs(context.Background())
	if err != nil {
		t.Fatalf("normalize execution facts: %v", err)
	}
	for _, input := range inputs {
		if input.TaskID == dependent.ID {
			result := projection.DeriveTaskProjection(input.Facts)
			if result.State != projection.StateValidating ||
				!slices.Contains(result.Blockers, projection.BlockerValidationMissing) {
				t.Fatalf("Candidate facts were guessed beyond Validation: %#v", result)
			}
		}
	}
}

func TestTaskStoreFactSourceUsesOneBulkRunAndCandidateReadPerProject(t *testing.T) {
	store := planningScaleStore()
	inputs, err := NewTaskStoreFactSource(store).TaskProjectionInputs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 500 || store.bulkRunReads != 1 || store.bulkCandidateReads != 1 {
		t.Fatalf("bulk Task projection reads: inputs=%d runs=%d candidates=%d", len(inputs), store.bulkRunReads, store.bulkCandidateReads)
	}
}
