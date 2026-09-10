// SPDX-License-Identifier: Apache-2.0

package projection

import (
	"fmt"
	"slices"
	"testing"

	"github.com/mcuadros/director-engine/domain"
)

const (
	planningScaleOpenTasks       = 500
	planningScaleHistoricalTasks = 10_000
)

func planningScaleInputs() []TaskProjectionInput {
	inputs := make([]TaskProjectionInput, 0, planningScaleOpenTasks+planningScaleHistoricalTasks)
	openStages := [...]string{"queued", "building", "validating", "review", "ready"}
	priorities := [...]domain.Priority{
		domain.PriorityUrgent, domain.PriorityHigh, domain.PriorityNormal, domain.PriorityLow,
	}
	for index := 0; index < planningScaleOpenTasks+planningScaleHistoricalTasks; index++ {
		stage := "done"
		if index < planningScaleOpenTasks {
			stage = openStages[index%len(openStages)]
		}
		facts := oracleStage(stage)
		id := fmt.Sprintf("task-%05d", index)
		facts.TaskID = id
		inputs = append(inputs, TaskProjectionInput{
			TaskID: id, ProjectID: "project-scale", WorkspaceID: fmt.Sprintf("workspace-%02d", index%25),
			EpicID: fmt.Sprintf("epic-%02d", index%10), Key: fmt.Sprintf("DIR-%05d", index+1),
			Title: fmt.Sprintf("%s task %05d", stage, index), Priority: priorities[index%len(priorities)],
			Labels:             []string{"scale", fmt.Sprintf("label-%d", index%3)},
			QueuedAtUnixMillis: int64(index + 1), UpdatedAtUnixMillis: int64(index + 101),
			Facts: productionFacts(facts),
		})
	}
	return inputs
}

func taskIDs(rows []TaskProjectionRow) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.TaskID)
	}
	return ids
}

func TestTaskQueryAgreedScaleKeepsPagesAndAllocationBounded(t *testing.T) {
	inputs := planningScaleInputs()
	if len(inputs) != 10_500 {
		t.Fatalf("scale inputs = %d", len(inputs))
	}
	workspaces := make(map[string]struct{})
	var openTasks, historicalTasks int
	for _, input := range inputs {
		workspaces[input.WorkspaceID] = struct{}{}
		if DeriveTaskProjection(input.Facts).DoneMember {
			historicalTasks++
		} else {
			openTasks++
		}
	}
	if len(workspaces) != 25 || openTasks != planningScaleOpenTasks || historicalTasks != planningScaleHistoricalTasks {
		t.Fatalf("scale envelope = workspaces %d, open %d, historical %d", len(workspaces), openTasks, historicalTasks)
	}
	openPage, err := QueryTaskProjections(inputs, 77, TaskQuery{Membership: TaskMembershipBoard, Limit: 100})
	if err != nil || openPage.TotalTasks != planningScaleOpenTasks || len(openPage.Tasks) != 100 {
		t.Fatalf("open scale page = total %d, rows %d, %v", openPage.TotalTasks, len(openPage.Tasks), err)
	}
	historyPage, err := QueryTaskProjections(inputs, 77, TaskQuery{Membership: TaskMembershipDone, Limit: 100})
	if err != nil || historyPage.TotalTasks != planningScaleHistoricalTasks || len(historyPage.Tasks) != 100 {
		t.Fatalf("historical scale page = total %d, rows %d, %v", historyPage.TotalTasks, len(historyPage.Tasks), err)
	}
	query := TaskQuery{Membership: TaskMembershipAll, Sort: TaskSortSchedulerOrder, Limit: 100}
	page, err := QueryTaskProjections(inputs, 77, query)
	if err != nil {
		t.Fatalf("scale query: %v", err)
	}
	if page.TotalTasks != 10_500 || len(page.Tasks) != 100 || page.NextCursor == "" {
		t.Fatalf("scale page bounds = total %d, rows %d, next %t", page.TotalTasks, len(page.Tasks), page.NextCursor != "")
	}
	allocations := testing.AllocsPerRun(2, func() {
		result, queryErr := QueryTaskProjections(inputs, 77, query)
		if queryErr != nil || len(result.Tasks) != 100 {
			panic("scale allocation query failed")
		}
	})
	if allocations > 300_000 {
		t.Fatalf("scale query allocations = %.0f, want <= 300000", allocations)
	}
}

func TestTaskQueryFilterSortAndPageBoundariesRemainDeterministic(t *testing.T) {
	inputs := planningScaleInputs()
	filtered := TaskQuery{
		Membership: TaskMembershipDone, WorkspaceIDs: []string{"workspace-00"}, EpicIDs: []string{"epic-00"},
		Priorities: []domain.Priority{domain.PriorityNormal}, Labels: []string{"scale", "label-1"},
		Search: "DONE TASK", Sort: TaskSortKeyAsc, Limit: 100,
	}
	first, err := QueryTaskProjections(inputs, 91, filtered)
	if err != nil {
		t.Fatalf("combined filter query: %v", err)
	}
	for _, row := range first.Tasks {
		if row.WorkspaceID != "workspace-00" || row.EpicID != "epic-00" ||
			row.Priority != domain.PriorityNormal || !row.Projection.DoneMember ||
			!slices.Contains(row.Labels, "label-1") {
			t.Fatalf("combined filter leaked row: %#v", row)
		}
	}

	boundary := inputs[:201]
	after := ""
	var all []string
	for expectedPage, expectedRows := range []int{100, 100, 1} {
		page, queryErr := QueryTaskProjections(boundary, 92, TaskQuery{
			Membership: TaskMembershipAll, Sort: TaskSortUpdatedDesc, Limit: 100, After: after,
		})
		if queryErr != nil || len(page.Tasks) != expectedRows {
			t.Fatalf("page %d = %d rows, %v", expectedPage+1, len(page.Tasks), queryErr)
		}
		all = append(all, taskIDs(page.Tasks)...)
		if expectedPage < 2 && page.NextCursor == "" {
			t.Fatalf("page %d omitted next cursor", expectedPage+1)
		}
		after = page.NextCursor
	}
	if len(all) != 201 {
		t.Fatalf("paged rows = %d", len(all))
	}
	seen := make(map[string]struct{}, len(all))
	for _, id := range all {
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("page boundary duplicated %s", id)
		}
		seen[id] = struct{}{}
	}

	reversed := slices.Clone(inputs)
	slices.Reverse(reversed)
	for _, order := range []TaskSort{TaskSortSchedulerOrder, TaskSortUpdatedDesc, TaskSortPriorityFIFO, TaskSortKeyAsc} {
		left, leftErr := QueryTaskProjections(inputs, 93, TaskQuery{Membership: TaskMembershipAll, Sort: order, Limit: 100})
		right, rightErr := QueryTaskProjections(reversed, 93, TaskQuery{Membership: TaskMembershipAll, Sort: order, Limit: 100})
		if leftErr != nil || rightErr != nil || !slices.Equal(taskIDs(left.Tasks), taskIDs(right.Tasks)) {
			t.Fatalf("%s order changed with input order: %v, %v", order, leftErr, rightErr)
		}
	}
}
