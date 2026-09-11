// SPDX-License-Identifier: Apache-2.0

package board

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mcuadros/director-engine/domain"
	domaincleanup "github.com/mcuadros/director-engine/domain/cleanup"
	directdomain "github.com/mcuadros/director-engine/domain/directdelivery"
	"github.com/mcuadros/director-engine/domain/execution"
	domainfeedback "github.com/mcuadros/director-engine/domain/feedback"
	integrationdomain "github.com/mcuadros/director-engine/domain/integration"
	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
	domainreview "github.com/mcuadros/director-engine/domain/review"
	domainvalidation "github.com/mcuadros/director-engine/domain/validation"
	"github.com/mcuadros/director-engine/projection"
)

var ErrDerivedPlanningInvalid = errors.New("derived Task source planning graph is invalid")

// PlanningFactReader is the read-only typed TaskStore surface required to
// normalize currently persisted M1/M2 facts for the complete projection
// reducer. Missing later-lifecycle records remain explicit and are never
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
	case strings.Contains(text, "feedback"):
		return projection.AttentionFeedbackDecisionRequired
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
	result.ReasonCode = string(first.Code)
	result.WakeCondition = first.WakeCondition
	return result
}

func normalizedValidation(run *domain.Run, candidateID string) projection.ValidationFact {
	if run == nil || run.Execution.Validation == nil {
		return projection.ValidationFact{Status: projection.FactMissing}
	}
	state := run.Execution.Validation
	result := projection.ValidationFact{Status: projection.FactCurrent, CandidateID: state.Binding.CandidateID, Reason: state.Code}
	if run.Execution.ValidationPolicy == nil || !domainvalidation.ValidPolicy(*run.Execution.ValidationPolicy) ||
		state.Policy.SHA256 != run.Execution.ValidationPolicy.SHA256 || !domainvalidation.ValidState(*state) || state.Binding.CandidateID != candidateID {
		return projection.ValidationFact{Status: projection.FactContradictory, CandidateID: state.Binding.CandidateID}
	}
	if state.Invalidated {
		result.Status, result.Outcome, result.Reason = projection.FactStale, projection.ValidationPending, state.InvalidationCode
		return result
	}
	switch state.Phase {
	case domainvalidation.PhaseIntent, domainvalidation.PhaseReserved, domainvalidation.PhasePending,
		domainvalidation.PhaseWaiting:
		result.Outcome = projection.ValidationPending
	case domainvalidation.PhasePassed:
		result.Outcome = projection.ValidationPassed
	case domainvalidation.PhaseFailed, domainvalidation.PhaseTimedOut, domainvalidation.PhaseNeedsYou:
		result.Outcome = projection.ValidationFailed
	default:
		result.Status = projection.FactContradictory
	}
	return result
}

func normalizedReview(run *domain.Run, candidateID string) projection.ReviewFact {
	if run == nil || run.Execution.Review == nil {
		return projection.ReviewFact{Status: projection.FactMissing}
	}
	state := run.Execution.Review
	result := projection.ReviewFact{Status: projection.FactCurrent, CandidateID: state.Binding.CandidateID, Outcome: projection.ReviewPending}
	if !domainreview.ValidState(*state) || state.Binding.CandidateID != candidateID {
		return projection.ReviewFact{Status: projection.FactContradictory, CandidateID: state.Binding.CandidateID}
	}
	if state.Invalidated {
		result.Status = projection.FactStale
		return result
	}
	if state.Evidence != nil {
		switch state.Evidence.Verdict {
		case domainreview.VerdictApproveCandidate:
			result.Outcome = projection.ReviewApproved
		case domainreview.VerdictChangesRequested:
			result.Outcome = projection.ReviewChangesRequested
		case domainreview.VerdictNeedsHumanDecision:
			result.Outcome = projection.ReviewNeedsHuman
		}
	} else if state.Prompt.Phase == domainreview.EffectDispatching || state.Prompt.Phase == domainreview.EffectComplete {
		result.Outcome = projection.ReviewRunning
	}
	return result
}

func normalizedDelivery(run *domain.Run, candidateID string) projection.DeliveryFact {
	if run == nil {
		return projection.DeliveryFact{Status: projection.FactMissing}
	}
	if run.Execution.DirectDelivery != nil {
		state := run.Execution.DirectDelivery
		result := projection.DeliveryFact{Status: projection.FactCurrent, CandidateID: state.Binding.CandidateID, State: projection.DeliveryPublished}
		if !directdomain.ValidState(*state) || state.Binding.CandidateID != candidateID {
			return projection.DeliveryFact{Status: projection.FactContradictory, CandidateID: state.Binding.CandidateID}
		}
		if state.Phase == directdomain.PhaseInvalidated {
			result.Status = projection.FactStale
			return result
		}
		if state.Phase == directdomain.PhaseComplete {
			result.State = projection.DeliveryIntegrated
		}
		return result
	}
	if run.Execution.Publication == nil {
		return projection.DeliveryFact{Status: projection.FactMissing}
	}
	state := run.Execution.Publication
	result := projection.DeliveryFact{Status: projection.FactCurrent, CandidateID: state.Binding.CandidateID, State: projection.DeliveryPendingPublication}
	if !publicationdomain.ValidState(*state) || state.Binding.CandidateID != candidateID {
		return projection.DeliveryFact{Status: projection.FactContradictory, CandidateID: state.Binding.CandidateID}
	}
	if state.Invalidated {
		result.Status = projection.FactStale
		return result
	}
	if state.Evidence != nil && state.Evidence.Ready {
		result.State = projection.DeliveryPublished
	}
	if run.Execution.Integration != nil {
		integration := run.Execution.Integration
		if state.Evidence == nil || !integrationdomain.ValidState(*integration) || integration.Binding.CandidateID != candidateID ||
			integration.Binding.PublicationEvidenceID != state.Evidence.ID {
			return projection.DeliveryFact{Status: projection.FactContradictory, CandidateID: integration.Binding.CandidateID}
		}
		if integration.Phase == integrationdomain.PhaseInvalidated {
			result.Status = projection.FactStale
			return result
		}
		if integration.Phase == integrationdomain.PhaseComplete {
			result.State = projection.DeliveryIntegrated
		}
	}
	return result
}

func normalizedCleanup(run *domain.Run, candidateID string) projection.CleanupFact {
	if run == nil || run.Execution.Cleanup == nil {
		return projection.CleanupFact{Status: projection.FactMissing}
	}
	state := run.Execution.Cleanup
	result := projection.CleanupFact{Status: projection.FactCurrent, RunID: run.ID, State: projection.CleanupPending}
	if run.Execution.CleanupPolicy == nil || !domaincleanup.ValidPolicy(*run.Execution.CleanupPolicy) ||
		!domaincleanup.ValidState(*state) || state.Policy.SHA256 != run.Execution.CleanupPolicy.SHA256 ||
		state.Binding.RunID != run.ID || state.Binding.CandidateID != candidateID {
		result.Status = projection.FactContradictory
		return result
	}
	if state.Phase == domaincleanup.PhaseComplete || state.Phase == domaincleanup.PhaseRetained {
		result.State = projection.CleanupComplete
	}
	return result
}

func taskStateFacts(
	project domain.Project,
	task domain.Task,
	dependencyBlocked bool,
	run *domain.Run,
	candidate *domain.Candidate,
) projection.TaskStateFacts {
	decision := projection.EligibilityEligible
	if dependencyBlocked {
		decision = projection.EligibilityDependencyWait
	}
	if run != nil && run.Execution.Terminal && run.Execution.Control.RelaunchBlocked &&
		run.Execution.Control.Phase == execution.ControlCancelled {
		emergencyResumed := run.Execution.Control.Intent.Kind == execution.ControlEmergencyStop &&
			project.State == "active" && project.Control.Intent.Kind == execution.ControlResumeProject &&
			project.Control.Phase == execution.ControlComplete && !project.Control.ResumeRequired &&
			project.Control.Generation > run.Execution.Control.ProjectGeneration
		if !emergencyResumed {
			decision = projection.EligibilityPolicyWait
		}
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
		facts.Validation = normalizedValidation(run, candidate.ID)
		facts.Review = normalizedReview(run, candidate.ID)
		facts.Delivery = normalizedDelivery(run, candidate.ID)
		facts.Cleanup = normalizedCleanup(run, candidate.ID)
		if run.Execution.Feedback == nil {
			facts.Feedback = projection.FeedbackFact{Status: projection.FactCurrent, CandidateID: candidate.ID, State: projection.FeedbackNone}
		} else {
			feedback := *run.Execution.Feedback
			switch {
			case !domainfeedback.ValidState(feedback):
				facts.Feedback = projection.FeedbackFact{Status: projection.FactContradictory, CandidateID: candidate.ID}
			case feedback.Invalidated || feedback.Binding.CandidateID != candidate.ID:
				facts.Feedback = projection.FeedbackFact{Status: projection.FactStale, CandidateID: candidate.ID}
			case len(domainfeedback.CurrentActionable(feedback)) > 0:
				facts.Feedback = projection.FeedbackFact{Status: projection.FactCurrent, CandidateID: candidate.ID, State: projection.FeedbackActionable}
			default:
				facts.Feedback = projection.FeedbackFact{Status: projection.FactCurrent, CandidateID: candidate.ID, State: projection.FeedbackResolved}
			}
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
		runsByTask := make(map[string][]domain.Run)
		candidatesByID := make(map[string]domain.Candidate)
		bulk, bulkAvailable := source.store.(PlanningBulkFactReader)
		if bulkAvailable {
			runs, err := bulk.PlanningRuns(ctx, project.ID)
			if err != nil {
				return nil, fmt.Errorf("read projection Runs: %w", err)
			}
			for _, run := range runs {
				runsByTask[run.TaskID] = append(runsByTask[run.TaskID], run)
			}
			candidates, err := bulk.PlanningCandidates(ctx, project.ID)
			if err != nil {
				return nil, fmt.Errorf("read projection Candidates: %w", err)
			}
			for _, candidate := range candidates {
				if _, duplicate := candidatesByID[candidate.ID]; duplicate {
					return nil, projection.ErrCandidateRunMismatch
				}
				candidatesByID[candidate.ID] = candidate
			}
		}
		for _, task := range tasks {
			dependency, ok := report.Result(task.ID)
			if !ok {
				return nil, ErrDerivedPlanningInvalid
			}
			runs := runsByTask[task.ID]
			if !bulkAvailable {
				runs, err = source.store.Runs(ctx, task.ID)
				if err != nil {
					return nil, fmt.Errorf("read projection Runs: %w", err)
				}
			}
			var run *domain.Run
			var candidate *domain.Candidate
			if latest, exists := latestTaskRun(runs); exists {
				run = &latest
				if latest.CurrentCandidateID != "" {
					current, exists := candidatesByID[latest.CurrentCandidateID]
					if !bulkAvailable {
						current, err = source.store.Candidate(ctx, latest.CurrentCandidateID)
						exists = err == nil
						if err != nil {
							return nil, fmt.Errorf("read projection Candidate: %w", err)
						}
					}
					if !exists {
						return nil, fmt.Errorf("read projection Candidate: %w", projection.ErrCandidateRunMismatch)
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
				Facts:               taskStateFacts(project, task, dependency.Blocked, run, candidate),
			})
		}
	}
	return inputs, nil
}
