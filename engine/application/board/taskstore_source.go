// SPDX-License-Identifier: Apache-2.0

package board

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/projection"
)

var ErrDerivedPlanningInvalid = errors.New("derived Task source planning graph is invalid")

// PlanningFactReader is the read-only typed TaskStore surface required to
// normalize currently persisted M1/M2 facts for the complete projection
// reducer. Validation, Review, delivery, and cleanup facts remain explicitly
// missing until their owning later-milestone records exist; they are never
// guessed from claims or connector state.
type PlanningFactReader interface {
	Projects(context.Context) ([]domain.Project, error)
	Workspaces(context.Context, string) ([]domain.Workspace, error)
	Epics(context.Context, string) ([]domain.Epic, error)
	Tasks(context.Context, string) ([]domain.Task, error)
	DependencyOverrides(context.Context, string) ([]domain.DependencyOverride, error)
	Runs(context.Context, string) ([]domain.Run, error)
	Candidate(context.Context, string) (domain.Candidate, error)
	LatestEventSequence(context.Context) (uint64, error)
}

// TaskStoreFactSource is an engine-side normalizer. It supplies facts, not a
// lane or lifecycle decision, so TaskStore and adapters do not gain projection
// authority.
type TaskStoreFactSource struct {
	store PlanningFactReader
}

func NewTaskStoreFactSource(store PlanningFactReader) *TaskStoreFactSource {
	return &TaskStoreFactSource{store: store}
}

func (source *TaskStoreFactSource) LatestEventSequence(ctx context.Context) (uint64, error) {
	return source.store.LatestEventSequence(ctx)
}

func latestTaskRun(runs []domain.Run) (domain.Run, bool) {
	if len(runs) == 0 {
		return domain.Run{}, false
	}
	latest := runs[0]
	for _, run := range runs[1:] {
		if run.Number > latest.Number || (run.Number == latest.Number && run.ID > latest.ID) {
			latest = run
		}
	}
	return latest, true
}

func attentionForNeed(code execution.NeedCode) projection.AttentionCode {
	text := string(code)
	switch {
	case strings.Contains(text, "credential"):
		return projection.AttentionCredentialRequired
	case strings.Contains(text, "permission"):
		return projection.AttentionPermissionRequired
	case strings.Contains(text, "git"):
		return projection.AttentionGitStateIrreconcilable
	case strings.Contains(text, "recovery"), strings.Contains(text, "ambiguous"):
		return projection.AttentionRecoveryAmbiguous
	case strings.Contains(text, "configuration"), strings.Contains(text, "observation_missing"):
		return projection.AttentionConfigurationRequired
	case strings.Contains(text, "correction"):
		return projection.AttentionCorrectionBudgetExhausted
	case strings.Contains(text, "replacement"):
		return projection.AttentionReplacementBudgetExhausted
	case strings.Contains(text, "soft_limit"):
		return projection.AttentionSoftBudgetAcknowledgment
	case strings.Contains(text, "budget"):
		return projection.AttentionHardBudgetExhausted
	default:
		return projection.AttentionPolicyOverrideRequired
	}
}

func normalizedHumanInput(task domain.Task, run *domain.Run) projection.HumanInputFact {
	result := projection.HumanInputFact{
		Status: projection.FactCurrent, TaskVersion: task.Version, State: projection.HumanInputNone,
	}
	var needs []*execution.NeedsYou
	if task.Attention != nil {
		needs = append(needs, task.Attention)
	}
	if run != nil && run.Execution.NeedsYou != nil {
		needs = append(needs, run.Execution.NeedsYou)
	}
	if len(needs) == 0 {
		return result
	}
	first := needs[0]
	for _, current := range needs[1:] {
		if current.Code != first.Code || current.WakeCondition != first.WakeCondition ||
			current.CleanupAuthorized != first.CleanupAuthorized {
			return projection.HumanInputFact{Status: projection.FactContradictory, TaskVersion: task.Version}
		}
	}
	if first.Code == "" || strings.TrimSpace(first.WakeCondition) == "" || first.CleanupAuthorized {
		return projection.HumanInputFact{Status: projection.FactContradictory, TaskVersion: task.Version}
	}
	result.State = projection.HumanInputPending
	result.Code = attentionForNeed(first.Code)
	result.WakeCondition = first.WakeCondition
	return result
}

func taskStateFacts(
	task domain.Task,
	dependencyBlocked bool,
	run *domain.Run,
	candidate *domain.Candidate,
) projection.TaskStateFacts {
	decision := projection.EligibilityEligible
	if dependencyBlocked {
		decision = projection.EligibilityDependencyWait
	}
	facts := projection.TaskStateFacts{
		TaskID: task.ID, TaskVersion: task.Version,
		Eligibility: projection.EligibilityFact{
			Status: projection.FactCurrent, TaskVersion: task.Version, Decision: decision,
		},
		Run:        projection.RunFact{Status: projection.FactMissing},
		Candidate:  projection.CandidateFact{Status: projection.FactMissing},
		Validation: projection.ValidationFact{Status: projection.FactMissing},
		Review:     projection.ReviewFact{Status: projection.FactMissing},
		Feedback:   projection.FeedbackFact{Status: projection.FactMissing},
		Delivery:   projection.DeliveryFact{Status: projection.FactMissing},
		Cleanup:    projection.CleanupFact{Status: projection.FactMissing},
		Terminal: projection.TerminalFact{
			Status: projection.FactCurrent, TaskVersion: task.Version, State: projection.TerminalOpen,
		},
	}
	if task.Complete {
		facts.Terminal.State = projection.TerminalDone
	}
	if run != nil {
		facts.Run = projection.RunFact{
			Status: projection.FactCurrent, TaskVersion: task.Version,
			ID: run.ID, Active: !run.Execution.Terminal,
		}
	}
	if candidate != nil && run != nil {
		facts.Candidate = projection.CandidateFact{
			Status: projection.FactCurrent, TaskVersion: task.Version,
			ID: candidate.ID, RunID: run.ID,
		}
	}
	facts.HumanInput = normalizedHumanInput(task, run)
	return facts
}

// TaskProjectionInputs loads the complete current planning graph and existing
// execution aggregates. Unsupported later lifecycle evidence remains Missing,
// which keeps projection conservative and auditable.
func (source *TaskStoreFactSource) TaskProjectionInputs(ctx context.Context) ([]projection.TaskProjectionInput, error) {
	projects, err := source.store.Projects(ctx)
	if err != nil {
		return nil, err
	}
	inputs := make([]projection.TaskProjectionInput, 0)
	for _, project := range projects {
		workspaces, err := source.store.Workspaces(ctx, project.ID)
		if err != nil {
			return nil, fmt.Errorf("read projection Workspaces: %w", err)
		}
		epics, err := source.store.Epics(ctx, project.ID)
		if err != nil {
			return nil, fmt.Errorf("read projection Epics: %w", err)
		}
		tasks, err := source.store.Tasks(ctx, project.ID)
		if err != nil {
			return nil, fmt.Errorf("read projection Tasks: %w", err)
		}
		overrides, err := source.store.DependencyOverrides(ctx, project.ID)
		if err != nil {
			return nil, fmt.Errorf("read projection overrides: %w", err)
		}
		planning := domain.PlanningProject{ID: project.ID, Epics: epics, Tasks: tasks, Overrides: overrides}
		for _, workspace := range workspaces {
			planning.Workspaces = append(planning.Workspaces, workspace.ID)
		}
		report := domain.EvaluatePlanning(planning)
		if !report.Valid {
			return nil, ErrDerivedPlanningInvalid
		}
		for _, task := range tasks {
			dependency, ok := report.Result(task.ID)
			if !ok {
				return nil, ErrDerivedPlanningInvalid
			}
			runs, err := source.store.Runs(ctx, task.ID)
			if err != nil {
				return nil, fmt.Errorf("read projection Runs: %w", err)
			}
			var run *domain.Run
			var candidate *domain.Candidate
			if latest, exists := latestTaskRun(runs); exists {
				run = &latest
				if latest.CurrentCandidateID != "" {
					current, err := source.store.Candidate(ctx, latest.CurrentCandidateID)
					if err != nil {
						return nil, fmt.Errorf("read projection Candidate: %w", err)
					}
					if current.RunID != latest.ID {
						return nil, projection.ErrCandidateRunMismatch
					}
					candidate = &current
				}
			}
			epicID := ""
			if task.Parent != nil {
				epicID = task.Parent.ID
			}
			inputs = append(inputs, projection.TaskProjectionInput{
				TaskID: task.ID, ProjectID: project.ID, WorkspaceID: task.WorkspaceIDs[0], EpicID: epicID,
				Key: task.Key, Title: task.Title, Priority: domain.EffectivePriority(task.Priority),
				Labels: append([]string(nil), task.Labels...), QueuedAtUnixMillis: task.QueuedAtUnixMillis,
				UpdatedAtUnixMillis: task.QueuedAtUnixMillis,
				Facts:               taskStateFacts(task, dependency.Blocked, run, candidate),
			})
		}
	}
	return inputs, nil
}
