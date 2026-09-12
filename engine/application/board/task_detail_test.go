// SPDX-License-Identifier: Apache-2.0

package board

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/execution"
	planningport "github.com/mcuadros/director-engine/ports/planning"
)

type taskDetailStore struct {
	*planningFactStore
	events []domain.Event
}

func (store *taskDetailStore) Events(_ context.Context, query domain.EventQuery) ([]domain.Event, error) {
	result := []domain.Event{}
	for _, event := range store.events {
		if event.GlobalSequence <= query.AfterGlobalSequence ||
			(query.AggregateID != "" && event.AggregateID != query.AggregateID) ||
			(query.RunID != "" && event.RunID != query.RunID) {
			continue
		}
		result = append(result, event)
		if len(result) == int(query.Limit) {
			break
		}
	}
	return result, nil
}

func taskDetailInput() planningport.TaskDetailQueryInput {
	taskID := "task-00000"
	return planningport.TaskDetailQueryInput{
		HostID: "host-a", Context: "board", TaskID: &taskID,
	}
}

func TestTaskDetailBindsExactTaskRunCandidateAgentWorkspaceAndHost(t *testing.T) {
	base := planningScaleStore()
	task := base.tasks["project-scale"][0]
	run := domain.Run{
		ID: "run-detail", TaskID: task.ID, Number: 2, Version: 9, CurrentCandidateID: "candidate-detail",
		Execution: execution.State{
			Scope:          execution.Scope{ProjectID: task.ProjectID, WorkspaceID: task.WorkspaceIDs[0], TaskID: task.ID, RunID: "run-detail"},
			HostView:       execution.Effect{ExternalID: "paseo-workspace-detail"},
			Agent:          execution.Effect{ExternalID: "paseo-agent-detail"},
			PrimarySession: execution.PrimarySession{NativeAgentID: "paseo-agent-detail"},
			WorkerVisibility: &execution.WorkerVisibility{
				AgentID: "paseo-agent-detail", ExecutionWorkspaceID: "paseo-workspace-detail",
				Digest: strings.Repeat("a", 64), ObservedDigest: strings.Repeat("a", 64),
			},
		},
	}
	base.runs[task.ID] = []domain.Run{run}
	base.candidates["candidate-detail"] = domain.Candidate{
		ID: "candidate-detail", RunID: run.ID, CommitSHA: strings.Repeat("b", 40),
	}
	base.cursor = 52
	store := &taskDetailStore{planningFactStore: base, events: []domain.Event{
		{GlobalSequence: 51, ID: "event-task", AggregateID: task.ID, Type: "task.updated"},
		{GlobalSequence: 52, ID: "event-run", RunID: run.ID, AggregateID: run.ID, Type: "validation.completed"},
	}}
	reader := NewPlanningReader(store)

	result, err := reader.TaskDetail(context.Background(), taskDetailInput())
	if err != nil || result.Detail == nil {
		t.Fatalf("TaskDetail() = %#v, %v", result, err)
	}
	binding := result.Detail.Binding
	if result.HostID != "host-a" || result.Query.HostID != "host-a" || binding.HostID != "host-a" ||
		binding.TaskID != task.ID || binding.RunID == nil || *binding.RunID != run.ID ||
		binding.CandidateSHA == nil || *binding.CandidateSHA != strings.Repeat("b", 40) ||
		binding.PaseoWorkspaceID == nil || *binding.PaseoWorkspaceID != "paseo-workspace-detail" ||
		binding.PaseoAgentID == nil || *binding.PaseoAgentID != "paseo-agent-detail" ||
		binding.AgentNavigation != "available" || binding.UnavailableReason != nil {
		t.Fatalf("Task detail binding = %#v", binding)
	}
	if len(result.Detail.Activity) != 2 || result.Detail.Activity[0].Message != "Task details updated" ||
		result.Detail.Activity[1].Kind != "validation" || result.Detail.Activity[1].OccurredAt != nil {
		t.Fatalf("Task activity = %#v", result.Detail.Activity)
	}

	agentInput := planningport.TaskDetailQueryInput{HostID: "host-a", Context: "agent"}
	workspaceID, agentID := "paseo-workspace-detail", "paseo-agent-detail"
	agentInput.PaseoWorkspaceID, agentInput.PaseoAgentID = &workspaceID, &agentID
	agentResult, err := reader.TaskDetail(context.Background(), agentInput)
	if err != nil || agentResult.Detail == nil || agentResult.Detail.Binding.TaskID != task.ID {
		t.Fatalf("agent TaskDetail() = %#v, %v", agentResult, err)
	}

	otherAgent := "paseo-agent-other"
	agentInput.PaseoAgentID = &otherAgent
	unbound, err := reader.TaskDetail(context.Background(), agentInput)
	if err != nil || unbound.Detail != nil || unbound.UnavailableReason == nil || unbound.UnavailableReason.Code != "agent_task_unbound" {
		t.Fatalf("unbound TaskDetail() = %#v, %v", unbound, err)
	}
}

func TestTaskDetailRejectsContextAndCursorDrift(t *testing.T) {
	reader := NewPlanningReader(planningScaleStore())
	invalid := taskDetailInput()
	invalid.Context = "agent"
	if _, err := reader.TaskDetail(context.Background(), invalid); !errors.Is(err, ErrPlanningQueryInvalid) {
		t.Fatalf("invalid context error = %v", err)
	}
	after := "45"
	invalid = taskDetailInput()
	invalid.AfterCursor = &after
	if _, err := reader.TaskDetail(context.Background(), invalid); !errors.Is(err, ErrTaskDetailCursorInvalid) {
		t.Fatalf("future cursor error = %v", err)
	}
}
