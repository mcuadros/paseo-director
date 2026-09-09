// SPDX-License-Identifier: Apache-2.0

package projection

import (
	"errors"
	"testing"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/execution"
)

func TestDeriveBoardTaskIsPureAndFactBound(t *testing.T) {
	project := domain.Project{ID: "project-1", Name: "Director"}
	task := domain.Task{ID: "task-1", ProjectID: project.ID, Title: "Board task"}
	queued, err := DeriveBoardTask(BoardFacts{Project: project, Task: task})
	if err != nil || queued.State != StateQueued || queued.RunNumber != nil {
		t.Fatalf("queued = %#v, %v", queued, err)
	}

	run := domain.Run{ID: "run-1", TaskID: task.ID, Number: 1}
	building, err := DeriveBoardTask(BoardFacts{Project: project, Task: task, Run: &run})
	if err != nil || building.State != StateBuilding || building.RunNumber == nil || *building.RunNumber != "1" {
		t.Fatalf("building = %#v, %v", building, err)
	}

	candidate := domain.Candidate{
		ID: "candidate-1", RunID: run.ID,
		CommitSHA: "abcdef0123456789abcdef0123456789abcdef01",
	}
	run.CurrentCandidateID = candidate.ID
	validating, err := DeriveBoardTask(BoardFacts{
		Project: project, Task: task, Run: &run, Candidate: &candidate,
	})
	if err != nil || validating.State != StateValidating || validating.CandidateSHA == nil {
		t.Fatalf("validating = %#v, %v", validating, err)
	}

	attention := execution.NeedsYou{Code: "operational_limit_exceeded", WakeCondition: "human approval is recorded"}
	task.Attention = &attention
	run.CurrentCandidateID = ""
	needsYou, err := DeriveBoardTask(BoardFacts{Project: project, Task: task, Run: &run})
	if err != nil || needsYou.State != StateNeedsYou {
		t.Fatalf("needs you = %#v, %v", needsYou, err)
	}
}

func TestDeriveBoardTaskRejectsOwnershipMismatch(t *testing.T) {
	project := domain.Project{ID: "project-1", Name: "Director"}
	task := domain.Task{ID: "task-1", ProjectID: project.ID, Title: "Board task"}
	wrongProject := task
	wrongProject.ProjectID = "another-project"
	if _, err := DeriveBoardTask(BoardFacts{Project: project, Task: wrongProject}); !errors.Is(err, ErrTaskProjectMismatch) {
		t.Fatalf("Task mismatch error = %v", err)
	}
	run := domain.Run{ID: "run-1", TaskID: "another-task", Number: 1}
	if _, err := DeriveBoardTask(BoardFacts{Project: project, Task: task, Run: &run}); !errors.Is(err, ErrRunTaskMismatch) {
		t.Fatalf("Run mismatch error = %v", err)
	}
	run.TaskID = task.ID
	run.CurrentCandidateID = "candidate-1"
	candidate := domain.Candidate{ID: "candidate-1", RunID: "another-run"}
	if _, err := DeriveBoardTask(BoardFacts{Project: project, Task: task, Run: &run, Candidate: &candidate}); !errors.Is(err, ErrCandidateRunMismatch) {
		t.Fatalf("Candidate mismatch error = %v", err)
	}
}
