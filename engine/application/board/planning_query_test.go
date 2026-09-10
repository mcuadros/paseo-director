// SPDX-License-Identifier: Apache-2.0

package board

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/execution"
	planningport "github.com/mcuadros/director-engine/ports/planning"
	"github.com/mcuadros/director-engine/projection"
)

func planningQueryInput() planningport.QueryInput {
	return planningport.QueryInput{
		WorkspaceIDs: []string{}, EpicIDs: []string{}, States: []string{}, Priorities: []string{},
		Labels: []string{}, Attention: []string{}, Sort: string(projection.TaskSortSchedulerOrder), PageSize: planningport.MaximumPageSize,
	}
}

func planningScaleStore() *planningFactStore {
	project := domain.Project{ID: "project-scale", Name: "Scale project", State: "active", Version: 3}
	workspaces := make([]domain.Workspace, 25)
	for index := range workspaces {
		workspaces[index] = domain.Workspace{
			ID: fmt.Sprintf("workspace-%02d", index), ProjectID: project.ID, Version: 2,
			Name: fmt.Sprintf("Workspace %02d", index+1), DefaultBaseBranch: "main",
			Policy: domain.WorkspacePolicy{LaunchPolicy: "automatic", DeliveryMode: "pull_request"},
		}
	}
	epics := make([]domain.Epic, 10)
	for index := range epics {
		epics[index] = domain.Epic{
			ID: fmt.Sprintf("epic-%02d", index), ProjectID: project.ID, Version: 1,
			Key: fmt.Sprintf("M2-%02d", index+1), Title: fmt.Sprintf("Milestone %02d", index+1),
			Labels: []string{"scale"},
		}
	}
	tasks := make([]domain.Task, 500)
	priorities := [...]domain.Priority{
		domain.PriorityUrgent, domain.PriorityHigh, domain.PriorityNormal, domain.PriorityLow,
	}
	for index := range tasks {
		epicID := epics[index%len(epics)].ID
		tasks[index] = domain.Task{
			ID: fmt.Sprintf("task-%05d", index), ProjectID: project.ID, Key: fmt.Sprintf("DIR-%05d", index+1),
			Title: fmt.Sprintf("Open task %05d", index+1), Objective: "Exercise the bounded planning query",
			AcceptanceCriteria: "The engine returns one stable page", WorkspaceIDs: []string{workspaces[index%len(workspaces)].ID},
			Parent:   &domain.PlanningNodeRef{Kind: domain.PlanningNodeEpic, ID: epicID},
			Priority: priorities[index%len(priorities)], Labels: []string{"scale", fmt.Sprintf("label-%d", index%3)},
			QueuedAtUnixMillis: int64(index + 1), Version: 1,
		}
	}
	tasks[0].Attention = &execution.NeedsYou{
		Code: "policy_override_required", WakeCondition: "human approval is recorded",
	}
	return &planningFactStore{
		projects: []domain.Project{project}, workspaces: map[string][]domain.Workspace{project.ID: workspaces},
		epics: map[string][]domain.Epic{project.ID: epics}, tasks: map[string][]domain.Task{project.ID: tasks},
		overrides: map[string][]domain.DependencyOverride{}, runs: map[string][]domain.Run{},
		candidates: map[string]domain.Candidate{}, cursor: 44,
	}
}

func TestPlanningReaderIntegratesScaleFiltersSortAndSnapshotPages(t *testing.T) {
	store := planningScaleStore()
	reader := NewPlanningReader(store)
	started := time.Now()
	first, err := reader.Query(context.Background(), planningQueryInput())
	if err != nil {
		t.Fatalf("first planning page: %v", err)
	}
	if first.SchemaVersion != 1 || first.ContractVersion != "director-planning/v1" ||
		first.Cursor != "44" || first.Page.TotalTasks != "500" || len(first.Page.Tasks) != 100 ||
		first.Page.NextCursor == nil || len(first.Page.Workspaces) != 25 || len(first.Page.Epics) != 10 {
		t.Fatalf("first planning page bounds = %#v", first)
	}
	if first.Page.Projects[0].TaskCounts.Open != "500" || first.Page.Projects[0].TaskCounts.Done != "0" ||
		first.Page.Projects[0].TaskCounts.NeedsYou != "1" {
		t.Fatalf("project counts = %#v", first.Page.Projects[0].TaskCounts)
	}
	if store.bulkRunReads != 1 || store.bulkCandidateReads != 1 || store.bulkTaskUpdateReads != 1 {
		t.Fatalf(
			"planning query used Run=%d Candidate=%d Task-update=%d bulk reads",
			store.bulkRunReads, store.bulkCandidateReads, store.bulkTaskUpdateReads,
		)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("integrated planning query exceeded conservative CI ceiling: %s", elapsed)
	}

	secondInput := planningQueryInput()
	secondInput.Cursor = first.Page.NextCursor
	second, err := reader.Query(context.Background(), secondInput)
	if err != nil || len(second.Page.Tasks) != 100 {
		t.Fatalf("second planning page = %d rows, %v", len(second.Page.Tasks), err)
	}
	seen := make(map[string]struct{}, 200)
	for _, task := range append(first.Page.Tasks, second.Page.Tasks...) {
		if _, duplicate := seen[task.ID]; duplicate {
			t.Fatalf("planning page boundary duplicated %s", task.ID)
		}
		seen[task.ID] = struct{}{}
	}

	filtered := planningQueryInput()
	projectID := "project-scale"
	search := "OPEN TASK"
	filtered.ProjectID = &projectID
	filtered.WorkspaceIDs = []string{"workspace-00"}
	filtered.EpicIDs = []string{"epic-00"}
	filtered.Priorities = []string{"normal"}
	filtered.Labels = []string{"scale", "label-2"}
	filtered.Search = &search
	filtered.Sort = string(projection.TaskSortKeyAsc)
	result, err := reader.Query(context.Background(), filtered)
	if err != nil || len(result.Page.Tasks) == 0 {
		t.Fatalf("combined planning filter = %#v, %v", result.Page, err)
	}
	for _, task := range result.Page.Tasks {
		if task.WorkspaceID != "workspace-00" || task.EpicID == nil || *task.EpicID != "epic-00" ||
			task.Priority != "normal" || len(task.Labels) != 2 {
			t.Fatalf("combined planning filter leaked %#v", task)
		}
	}
	recent := planningQueryInput()
	recent.Sort = string(projection.TaskSortUpdatedDesc)
	recentResult, err := reader.Query(context.Background(), recent)
	if err != nil || recentResult.Page.Tasks[0].ID != "task-00499" {
		t.Fatalf("updated-desc TaskStore order = %#v, %v", recentResult.Page.Tasks, err)
	}
	attention := planningQueryInput()
	attention.States = []string{"needs_you"}
	attention.Attention = []string{"policy_override_required"}
	attentionResult, err := reader.Query(context.Background(), attention)
	if err != nil || attentionResult.Page.TotalTasks != "1" || attentionResult.Page.Tasks[0].ID != "task-00000" {
		t.Fatalf("attention query = %#v, %v", attentionResult.Page, err)
	}

	store.cursor++
	if _, err := reader.Query(context.Background(), secondInput); !errors.Is(err, projection.ErrTaskQueryCursorSnapshot) {
		t.Fatalf("changed-snapshot cursor error = %v", err)
	}
}
