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

// BoardState is the engine-derived state vocabulary. The M1 host contract
// still transports its original subset, while TaskProjection consumes the
// complete M2 fact set without changing that connector boundary.
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

// BoardFacts is the existing M1 persisted-fact adapter. DeriveBoardTask checks
// ownership and delegates state to the complete pure projection reducer.
type BoardFacts struct {
	Project   domain.Project
	Task      domain.Task
	Run       *domain.Run
	Candidate *domain.Candidate
}

// DeriveBoardTask maps persisted facts to the minimal version-1 host row
// without I/O or a second state policy.
func DeriveBoardTask(facts BoardFacts) (BoardTask, error) {
	if facts.Task.ProjectID != facts.Project.ID {
		return BoardTask{}, ErrTaskProjectMismatch
	}
	row := BoardTask{
		ID: facts.Task.ID, ProjectID: facts.Project.ID, ProjectName: facts.Project.Name,
		Title: facts.Task.Title,
	}
	if facts.Run == nil {
		if facts.Candidate != nil {
			return BoardTask{}, ErrCandidateRunMismatch
		}
	} else {
		if facts.Run.TaskID != facts.Task.ID {
			return BoardTask{}, ErrRunTaskMismatch
		}
		runNumber := strconv.FormatUint(facts.Run.Number, 10)
		row.RunNumber = &runNumber
		if facts.Candidate == nil {
			if facts.Run.CurrentCandidateID != "" {
				return BoardTask{}, ErrCandidateRunMismatch
			}
		} else {
			if facts.Run.CurrentCandidateID != facts.Candidate.ID || facts.Candidate.RunID != facts.Run.ID {
				return BoardTask{}, ErrCandidateRunMismatch
			}
			row.CandidateSHA = &facts.Candidate.CommitSHA
		}
	}
	row.State = DeriveTaskProjection(legacyTaskStateFacts(facts)).State
	return row, nil
}

func legacyTaskStateFacts(facts BoardFacts) TaskStateFacts {
	state := TaskStateFacts{
		TaskID: facts.Task.ID, TaskVersion: facts.Task.Version,
		Eligibility: EligibilityFact{
			Status: FactCurrent, TaskVersion: facts.Task.Version, Decision: EligibilityEligible,
		},
		Run:        RunFact{Status: FactMissing},
		Candidate:  CandidateFact{Status: FactMissing},
		Validation: ValidationFact{Status: FactMissing},
		Review:     ReviewFact{Status: FactMissing},
		Feedback:   FeedbackFact{Status: FactMissing},
		Delivery:   DeliveryFact{Status: FactMissing},
		Cleanup:    CleanupFact{Status: FactMissing},
		HumanInput: HumanInputFact{
			Status: FactCurrent, TaskVersion: facts.Task.Version, State: HumanInputNone,
		},
		Terminal: TerminalFact{
			Status: FactCurrent, TaskVersion: facts.Task.Version, State: TerminalOpen,
		},
	}
	if facts.Run != nil {
		state.Run = RunFact{
			Status: FactCurrent, TaskVersion: facts.Task.Version,
			ID: facts.Run.ID, Active: !facts.Run.Execution.Terminal,
		}
	}
	if facts.Candidate != nil && facts.Run != nil {
		state.Candidate = CandidateFact{
			Status: FactCurrent, TaskVersion: facts.Task.Version,
			ID: facts.Candidate.ID, RunID: facts.Run.ID,
		}
	}
	need := facts.Task.Attention
	if facts.Run != nil && facts.Run.Execution.NeedsYou != nil {
		need = facts.Run.Execution.NeedsYou
	}
	if need != nil {
		state.HumanInput = HumanInputFact{
			Status: FactCurrent, TaskVersion: facts.Task.Version, State: HumanInputPending,
			Code: AttentionPolicyOverrideRequired, WakeCondition: need.WakeCondition,
		}
	}
	return state
}
