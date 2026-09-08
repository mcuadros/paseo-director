// SPDX-License-Identifier: Apache-2.0

// Package projection contains engine-owned read models derived from domain facts.
package projection

import (
	"errors"
	"strconv"

	"github.com/mcuadros/director-engine/domain"
)

var (
	ErrTaskProjectMismatch  = errors.New("board Task belongs to another Project")
	ErrRunTaskMismatch      = errors.New("board Run belongs to another Task")
	ErrCandidateRunMismatch = errors.New("board Candidate belongs to another Run")
)

// BoardState is the engine-derived state rendered by Board and List clients.
// The M1 walking skeleton currently produces NeedsYou, Queued, Building, and
// Validating; the complete closed vocabulary keeps the host contract stable for
// later facts.
type BoardState string

const (
	// BoardSchemaVersion is the engine-owned read-model version.
	BoardSchemaVersion = 1
	// MaximumBoardTasks is the bounded M1 response size. It is not a scale claim.
	MaximumBoardTasks = 1000
	// MaximumBoardBytes bounds the complete serialized transport response.
	MaximumBoardBytes = 2 * 1024 * 1024
)

const (
	StateNeedsYou   BoardState = "needs_you"
	StateQueued     BoardState = "queued"
	StateBuilding   BoardState = "building"
	StateValidating BoardState = "validating"
	StateInReview   BoardState = "in_review"
	StateReady      BoardState = "ready"
)

// BoardStates returns the closed state vocabulary in display order.
func BoardStates() []BoardState {
	return []BoardState{
		StateNeedsYou,
		StateQueued,
		StateBuilding,
		StateValidating,
		StateInReview,
		StateReady,
	}
}

// BoardTask is one policy-free display row. State is always computed by the
// Director Engine and is never inferred by a host or UI.
type BoardTask struct {
	ID           string     `json:"id"`
	ProjectID    string     `json:"projectId"`
	ProjectName  string     `json:"projectName"`
	Title        string     `json:"title"`
	State        BoardState `json:"state"`
	RunNumber    *string    `json:"runNumber"`
	CandidateSHA *string    `json:"candidateSha"`
}

// Board is the complete M1 Board/List read model. Cursor is the TaskStore's
// monotonic event high-water mark represented as decimal text for JSON safety.
type Board struct {
	SchemaVersion int         `json:"schemaVersion"`
	Cursor        string      `json:"cursor"`
	Tasks         []BoardTask `json:"tasks"`
}

// BoardFacts is the closed fact set loaded by the application reader. It keeps
// state derivation pure and host-independent until the routing reducer exposes
// the complete post-M1 phase projection.
type BoardFacts struct {
	Project   domain.Project
	Task      domain.Task
	Run       *domain.Run
	Candidate *domain.Candidate
}

// DeriveBoardTask maps persisted facts to the minimal Board row without I/O.
func DeriveBoardTask(facts BoardFacts) (BoardTask, error) {
	if facts.Task.ProjectID != facts.Project.ID {
		return BoardTask{}, ErrTaskProjectMismatch
	}
	row := BoardTask{
		ID: facts.Task.ID, ProjectID: facts.Project.ID, ProjectName: facts.Project.Name,
		Title: facts.Task.Title, State: StateQueued,
	}
	if facts.Task.Attention != nil {
		row.State = StateNeedsYou
	}
	if facts.Run == nil {
		if facts.Candidate != nil {
			return BoardTask{}, ErrCandidateRunMismatch
		}
		return row, nil
	}
	if facts.Run.TaskID != facts.Task.ID {
		return BoardTask{}, ErrRunTaskMismatch
	}
	runNumber := strconv.FormatUint(facts.Run.Number, 10)
	row.RunNumber = &runNumber
	if row.State != StateNeedsYou {
		row.State = StateBuilding
	}
	if facts.Run.Execution.NeedsYou != nil {
		row.State = StateNeedsYou
	}
	if facts.Candidate == nil {
		if facts.Run.CurrentCandidateID != "" {
			return BoardTask{}, ErrCandidateRunMismatch
		}
		return row, nil
	}
	if facts.Run.CurrentCandidateID != facts.Candidate.ID || facts.Candidate.RunID != facts.Run.ID {
		return BoardTask{}, ErrCandidateRunMismatch
	}
	row.CandidateSHA = &facts.Candidate.CommitSHA
	if row.State != StateNeedsYou {
		// Candidate admission proves quality work is required, never readiness.
		row.State = StateValidating
	}
	return row, nil
}
