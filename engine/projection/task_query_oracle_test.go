// SPDX-License-Identifier: Apache-2.0

package projection

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/mcuadros/director-engine/domain"
	oracle "github.com/mcuadros/director-engine/internal/testkit/projectionoracle"
)

func projectionQueryCorpus() ([]TaskProjectionInput, []oracle.Fixture) {
	stages := []string{"ready", "queued", "done", "building", "review", "validating", "queued", "ready"}
	priorities := []oracle.Priority{
		oracle.PriorityLow, oracle.PriorityNormal, oracle.PriorityUrgent, oracle.PriorityHigh,
		oracle.PriorityUrgent, oracle.PriorityNormal, oracle.PriorityHigh, oracle.PriorityUrgent,
	}
	queued := []int64{90, 10, 50, 20, 40, 30, 15, 5}
	inputs := make([]TaskProjectionInput, 0, len(stages))
	fixtures := make([]oracle.Fixture, 0, len(stages))
	for index, stage := range stages {
		facts := oracleStage(stage)
		facts.TaskID = "task-" + string(rune('a'+index))
		fixture := oracle.Fixture{Facts: facts, Priority: priorities[index], QueuedAtUnixMillis: queued[index]}
		fixtures = append(fixtures, fixture)
		inputs = append(inputs, TaskProjectionInput{
			TaskID: facts.TaskID, ProjectID: "project-1", WorkspaceID: "workspace-" + string(rune('a'+index%2)),
			EpicID: "epic-" + string(rune('a'+index%3)), Title: "Task " + facts.TaskID,
			Priority: domain.Priority(priorities[index]), Labels: []string{"common", "label-" + string(rune('a'+index%2))},
			QueuedAtUnixMillis: queued[index], Facts: productionFacts(facts),
		})
	}
	inputs[1].EpicID = ""
	inputs[6].EpicID = ""
	return inputs, fixtures
}

func assertOrderedAgainstOracle(t *testing.T, got []TaskProjectionRow, want []oracle.ExpectedTask) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("row count = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index].TaskID != want[index].TaskID ||
			got[index].Priority != domain.Priority(want[index].Priority) ||
			got[index].QueuedAtUnixMillis != want[index].QueuedAtUnixMillis ||
			!reflect.DeepEqual(got[index].Projection, productionExpected(want[index].Projection)) {
			t.Fatalf("row %d differs\ngot:  %#v\nwant: %#v", index, got[index], want[index])
		}
	}
}

func TestTaskQueryOrderingMatchesIndependentOracle(t *testing.T) {
	inputs, fixtures := projectionQueryCorpus()
	want, err := oracle.Ordered(fixtures)
	if err != nil {
		t.Fatalf("oracle order: %v", err)
	}
	page, err := QueryTaskProjections(inputs, 41, TaskQuery{Membership: TaskMembershipAll, Limit: MaximumBoardTasks})
	if err != nil || page.SnapshotCursor != "41" || page.NextCursor != "" {
		t.Fatalf("query page = %#v, %v", page, err)
	}
	assertOrderedAgainstOracle(t, page.Tasks, want)

	reversed := append([]TaskProjectionInput(nil), inputs...)
	slices.Reverse(reversed)
	again, err := QueryTaskProjections(reversed, 41, TaskQuery{Membership: TaskMembershipAll, Limit: MaximumBoardTasks})
	if err != nil || !reflect.DeepEqual(page, again) {
		t.Fatalf("card/input movement changed derived order\nfirst: %#v\nagain: %#v\nerror: %v", page, again, err)
	}
}

func TestTaskQueryPaginationMatchesIndependentOracleMembership(t *testing.T) {
	inputs, fixtures := projectionQueryCorpus()
	const snapshot = uint64(73)
	productionAfter, oracleAfter := "", ""
	var productionIDs, oracleIDs []string
	for {
		page, err := QueryTaskProjections(inputs, snapshot, TaskQuery{
			Membership: TaskMembershipAll, Limit: 2, After: productionAfter,
		})
		if err != nil {
			t.Fatalf("production page: %v", err)
		}
		expected, err := oracle.Paginate(fixtures, snapshot, 2, oracleAfter)
		if err != nil {
			t.Fatalf("oracle page: %v", err)
		}
		for _, row := range page.Tasks {
			productionIDs = append(productionIDs, row.TaskID)
		}
		for _, row := range expected.Tasks {
			oracleIDs = append(oracleIDs, row.TaskID)
		}
		if (page.NextCursor == "") != (expected.NextCursor == "") {
			t.Fatalf("pagination completion differs: production=%q oracle=%q", page.NextCursor, expected.NextCursor)
		}
		if page.NextCursor == "" {
			break
		}
		productionAfter, oracleAfter = page.NextCursor, expected.NextCursor
	}
	if !slices.Equal(productionIDs, oracleIDs) {
		t.Fatalf("paged membership differs: production=%v oracle=%v", productionIDs, oracleIDs)
	}

	first, err := QueryTaskProjections(inputs, snapshot, TaskQuery{Membership: TaskMembershipAll, Limit: 2})
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first page = %#v, %v", first, err)
	}
	if _, err := QueryTaskProjections(inputs, snapshot+1, TaskQuery{
		Membership: TaskMembershipAll, Limit: 2, After: first.NextCursor,
	}); !errors.Is(err, ErrTaskQueryCursorSnapshot) {
		t.Fatalf("cross-snapshot cursor error = %v", err)
	}
	if _, err := QueryTaskProjections(inputs, snapshot, TaskQuery{
		Membership: TaskMembershipDone, Limit: 2, After: first.NextCursor,
	}); !errors.Is(err, ErrTaskQueryCursorSnapshot) {
		t.Fatalf("cross-filter cursor error = %v", err)
	}
	if _, err := QueryTaskProjections(inputs, snapshot, TaskQuery{
		Membership: TaskMembershipAll, Limit: 3, After: first.NextCursor,
	}); !errors.Is(err, ErrTaskQueryCursorSnapshot) {
		t.Fatalf("cross-page-size cursor error = %v", err)
	}
}

func TestTaskQueryFiltersBoardDoneAndPlanningMetadata(t *testing.T) {
	inputs, _ := projectionQueryCorpus()
	board, err := QueryTaskProjections(inputs, 9, TaskQuery{Limit: MaximumBoardTasks})
	if err != nil {
		t.Fatalf("default Board query: %v", err)
	}
	for _, row := range board.Tasks {
		if !row.Projection.BoardMember || row.Projection.DoneMember {
			t.Fatalf("default query leaked Done row: %#v", row)
		}
	}
	done, err := QueryTaskProjections(inputs, 9, TaskQuery{Membership: TaskMembershipDone, Limit: MaximumBoardTasks})
	if err != nil || len(done.Tasks) != 1 || !done.Tasks[0].Projection.DoneMember {
		t.Fatalf("Done query = %#v, %v", done, err)
	}
	standalone := true
	filtered, err := QueryTaskProjections(inputs, 9, TaskQuery{
		Membership: TaskMembershipAll, WorkspaceIDs: []string{"workspace-b"}, Standalone: &standalone,
		Priorities: []domain.Priority{domain.PriorityNormal}, Labels: []string{"common", "label-b"},
		States: []BoardState{StateQueued}, Limit: MaximumBoardTasks,
	})
	if err != nil || len(filtered.Tasks) != 1 || filtered.Tasks[0].TaskID != "task-b" {
		t.Fatalf("filtered query = %#v, %v", filtered, err)
	}
	inputs[0].Facts.HumanInput = HumanInputFact{
		Status: FactCurrent, TaskVersion: inputs[0].Facts.TaskVersion, State: HumanInputPending,
		Code: AttentionCredentialRequired, WakeCondition: "credential is provided",
	}
	attention, err := QueryTaskProjections(inputs, 9, TaskQuery{
		Membership: TaskMembershipAll, ProjectIDs: []string{"project-1"}, EpicIDs: []string{"epic-a"},
		Attention: []AttentionCode{AttentionCredentialRequired}, Limit: MaximumBoardTasks,
	})
	if err != nil || len(attention.Tasks) != 1 || attention.Tasks[0].TaskID != inputs[0].TaskID ||
		attention.Tasks[0].Projection.State != StateNeedsYou {
		t.Fatalf("attention filter = %#v, %v", attention, err)
	}
}

func TestTaskQueryRejectsInvalidInputAndReturnsDefensiveCopies(t *testing.T) {
	inputs, _ := projectionQueryCorpus()
	for name, query := range map[string]TaskQuery{
		"zero limit":       {Limit: 0},
		"large limit":      {Limit: MaximumBoardTasks + 1},
		"unknown state":    {Limit: 1, States: []BoardState{"future"}},
		"unknown priority": {Limit: 1, Priorities: []domain.Priority{"future"}},
		"duplicate filter": {Limit: 1, ProjectIDs: []string{"project-1", "project-1"}},
		"invalid cursor":   {Limit: 1, After: "not-a-cursor"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := QueryTaskProjections(inputs, 1, query); !errors.Is(err, ErrTaskQueryInvalid) &&
				!errors.Is(err, ErrTaskQueryCursorInvalid) {
				t.Fatalf("query error = %v", err)
			}
		})
	}
	page, err := QueryTaskProjections(inputs, 1, TaskQuery{Membership: TaskMembershipAll, Limit: MaximumBoardTasks})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	page.Tasks[0].Labels[0] = "mutated"
	page.Tasks[0].Projection.Blockers = append(page.Tasks[0].Projection.Blockers, "mutated")
	again, err := QueryTaskProjections(inputs, 1, TaskQuery{Membership: TaskMembershipAll, Limit: MaximumBoardTasks})
	if err != nil || slices.Contains(again.Tasks[0].Labels, "mutated") ||
		slices.Contains(again.Tasks[0].Projection.Blockers, BlockerCode("mutated")) {
		t.Fatalf("query returned shared mutable storage: %#v, %v", again.Tasks[0], err)
	}
	mismatched := append([]TaskProjectionInput(nil), inputs...)
	mismatched[0].Facts.TaskID = "another-task"
	failClosed, err := QueryTaskProjections(mismatched, 1, TaskQuery{
		Membership: TaskMembershipAll, Limit: MaximumBoardTasks,
	})
	if err != nil {
		t.Fatalf("fact identity mismatch query: %v", err)
	}
	found := false
	for _, row := range failClosed.Tasks {
		if row.TaskID == mismatched[0].TaskID {
			found = slices.Contains(row.Projection.Blockers, BlockerTaskFactContradictory)
		}
	}
	if !found {
		t.Fatalf("fact identity mismatch did not fail closed: %#v", failClosed.Tasks)
	}
}
