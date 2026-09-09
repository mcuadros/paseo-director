// SPDX-License-Identifier: Apache-2.0

package board

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/projection"
)

type planningFactStore struct {
	projects   []domain.Project
	workspaces map[string][]domain.Workspace
	epics      map[string][]domain.Epic
	tasks      map[string][]domain.Task
	overrides  map[string][]domain.DependencyOverride
	runs       map[string][]domain.Run
	candidates map[string]domain.Candidate
	cursor     uint64
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
