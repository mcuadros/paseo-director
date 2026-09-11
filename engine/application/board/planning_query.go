// SPDX-License-Identifier: Apache-2.0

package board

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/domain/runtimebudget"
	"github.com/mcuadros/director-engine/domain/scheduling"
	planningport "github.com/mcuadros/director-engine/ports/planning"
	"github.com/mcuadros/director-engine/projection"
)

var ErrPlanningQueryInvalid = errors.New("planning presentation query is invalid")

type planningProjectFacts struct {
	project         domain.Project
	workspaces      []domain.Workspace
	epics           []domain.Epic
	inputs          []projection.TaskProjectionInput
	activeRuns      map[string]bool
	latestRuns      map[string]domain.Run
	counts          planningCounts
	workspaceCounts map[string]planningCounts
	epicProgress    map[string]planningProgress
	availableLabels []string
}

type planningCounts struct {
	open     uint64
	done     uint64
	needsYou uint64
}

type planningProgress struct {
	completed uint64
	total     uint64
}

type planningFacts struct {
	projects []planningProjectFacts
}

// PlanningBulkFactReader is an optional engine-side read optimization. It
// returns the same typed Run and Candidate facts as the base TaskStore port;
// it neither filters nor projects state.
type PlanningBulkFactReader interface {
	PlanningRuns(context.Context, string) ([]domain.Run, error)
	PlanningCandidates(context.Context, string) ([]domain.Candidate, error)
}

// PlanningTaskUpdateReader exposes the TaskStore-assigned aggregate update
// time in one typed bulk read. Callers cannot supply or alter this fact.
type PlanningTaskUpdateReader interface {
	PlanningTaskUpdatedAt(context.Context, string) (map[string]int64, error)
}

// PlanningReader integrates the durable planning graph with the pure Task
// projection query. It owns I/O composition only; state, filters, ordering,
// and cursor admission remain in the standalone engine projection package.
type PlanningReader struct {
	store PlanningFactReader
}

func NewPlanningReader(store PlanningFactReader) *PlanningReader {
	return &PlanningReader{store: store}
}

func defaultPlanningKey(key, id string) string {
	if key != "" {
		return key
	}
	return id
}

func cloneText(values []string) []string {
	return append([]string{}, values...)
}

func (reader *PlanningReader) loadFacts(ctx context.Context) (planningFacts, error) {
	projects, err := reader.store.Projects(ctx)
	if err != nil {
		return planningFacts{}, fmt.Errorf("read planning projects: %w", err)
	}
	if len(projects) > planningport.MaximumProjects {
		return planningFacts{}, ErrPlanningQueryInvalid
	}
	slices.SortFunc(projects, func(left, right domain.Project) int {
		if byName := strings.Compare(left.Name, right.Name); byName != 0 {
			return byName
		}
		return strings.Compare(left.ID, right.ID)
	})
	result := planningFacts{projects: make([]planningProjectFacts, 0, len(projects))}
	for _, project := range projects {
		workspaces, err := reader.store.Workspaces(ctx, project.ID)
		if err != nil {
			return planningFacts{}, fmt.Errorf("read planning Workspaces: %w", err)
		}
		epics, err := reader.store.Epics(ctx, project.ID)
		if err != nil {
			return planningFacts{}, fmt.Errorf("read planning Epics: %w", err)
		}
		tasks, err := reader.store.Tasks(ctx, project.ID)
		if err != nil {
			return planningFacts{}, fmt.Errorf("read planning Tasks: %w", err)
		}
		overrides, err := reader.store.DependencyOverrides(ctx, project.ID)
		if err != nil {
			return planningFacts{}, fmt.Errorf("read planning overrides: %w", err)
		}
		if len(workspaces) > planningport.MaximumWorkspaces || len(epics) > planningport.MaximumEpics {
			return planningFacts{}, ErrPlanningQueryInvalid
		}
		graph := domain.PlanningProject{ID: project.ID, Epics: epics, Tasks: tasks, Overrides: overrides}
		for _, workspace := range workspaces {
			graph.Workspaces = append(graph.Workspaces, workspace.ID)
		}
		report := domain.EvaluatePlanning(graph)
		if !report.Valid {
			return planningFacts{}, ErrDerivedPlanningInvalid
		}
		projectFacts := planningProjectFacts{
			project: project, workspaces: workspaces, epics: epics,
			inputs: make([]projection.TaskProjectionInput, 0, len(tasks)), activeRuns: make(map[string]bool),
			latestRuns:      make(map[string]domain.Run),
			workspaceCounts: make(map[string]planningCounts, len(workspaces)),
			epicProgress:    make(map[string]planningProgress, len(epics)),
		}
		labelSet := make(map[string]struct{})
		updatedAtByTask := make(map[string]int64)
		updates, updatesAvailable := reader.store.(PlanningTaskUpdateReader)
		if updatesAvailable {
			updatedAtByTask, err = updates.PlanningTaskUpdatedAt(ctx, project.ID)
			if err != nil {
				return planningFacts{}, fmt.Errorf("read planning Task update facts: %w", err)
			}
		}
		runsByTask := make(map[string][]domain.Run)
		candidatesByID := make(map[string]domain.Candidate)
		bulk, bulkAvailable := reader.store.(PlanningBulkFactReader)
		if bulkAvailable {
			runs, err := bulk.PlanningRuns(ctx, project.ID)
			if err != nil {
				return planningFacts{}, fmt.Errorf("read planning Runs: %w", err)
			}
			for _, run := range runs {
				runsByTask[run.TaskID] = append(runsByTask[run.TaskID], run)
			}
			candidates, err := bulk.PlanningCandidates(ctx, project.ID)
			if err != nil {
				return planningFacts{}, fmt.Errorf("read planning Candidates: %w", err)
			}
			for _, candidate := range candidates {
				if _, duplicate := candidatesByID[candidate.ID]; duplicate {
					return planningFacts{}, projection.ErrCandidateRunMismatch
				}
				candidatesByID[candidate.ID] = candidate
			}
		}
		for _, task := range tasks {
			dependency, ok := report.Result(task.ID)
			if !ok {
				return planningFacts{}, ErrDerivedPlanningInvalid
			}
			runs := runsByTask[task.ID]
			if !bulkAvailable {
				runs, err = reader.store.Runs(ctx, task.ID)
				if err != nil {
					return planningFacts{}, fmt.Errorf("read planning Runs: %w", err)
				}
			}
			var run *domain.Run
			var candidate *domain.Candidate
			if latest, exists := latestTaskRun(runs); exists {
				run = &latest
				projectFacts.latestRuns[task.ID] = latest
				projectFacts.activeRuns[task.ID] = !latest.Execution.Terminal
				if latest.CurrentCandidateID != "" {
					current, exists := candidatesByID[latest.CurrentCandidateID]
					if !bulkAvailable {
						current, err = reader.store.Candidate(ctx, latest.CurrentCandidateID)
						exists = err == nil
						if err != nil {
							return planningFacts{}, fmt.Errorf("read planning Candidate: %w", err)
						}
					}
					if !exists {
						return planningFacts{}, fmt.Errorf("read planning Candidate: %w", projection.ErrCandidateRunMismatch)
					}
					if current.RunID != latest.ID {
						return planningFacts{}, projection.ErrCandidateRunMismatch
					}
					candidate = &current
				}
			}
			epicID := ""
			if task.Parent != nil {
				epicID = task.Parent.ID
			}
			updatedAt := task.QueuedAtUnixMillis
			if updatesAvailable {
				var exists bool
				updatedAt, exists = updatedAtByTask[task.ID]
				if !exists || updatedAt < 0 {
					return planningFacts{}, ErrPlanningQueryInvalid
				}
			}
			input := projection.TaskProjectionInput{
				TaskID: task.ID, ProjectID: project.ID, WorkspaceID: task.WorkspaceIDs[0], EpicID: epicID,
				Key: defaultPlanningKey(task.Key, task.ID), Title: task.Title,
				Priority: domain.EffectivePriority(task.Priority), Labels: cloneText(task.Labels),
				QueuedAtUnixMillis: task.QueuedAtUnixMillis, UpdatedAtUnixMillis: updatedAt,
				Facts: taskStateFacts(project, task, dependency.Blocked, run, candidate),
			}
			if run != nil && runtimebudget.ValidLedger(run.Execution.Budget) {
				budget := run.Execution.Budget
				input.Budget = &budget
				if run.Execution.NeedsYou != nil && strings.HasPrefix(string(run.Execution.NeedsYou.Code), "budget_") {
					input.BudgetNeedCode = string(run.Execution.NeedsYou.Code)
				}
			}
			projectFacts.inputs = append(projectFacts.inputs, input)
			projected := projection.DeriveTaskProjection(input.Facts)
			projectFacts.counts.add(projected)
			workspaceCounts := projectFacts.workspaceCounts[input.WorkspaceID]
			workspaceCounts.add(projected)
			projectFacts.workspaceCounts[input.WorkspaceID] = workspaceCounts
			if input.EpicID != "" {
				progress := projectFacts.epicProgress[input.EpicID]
				progress.total++
				if projected.DoneMember {
					progress.completed++
				}
				projectFacts.epicProgress[input.EpicID] = progress
			}
			for _, label := range input.Labels {
				labelSet[label] = struct{}{}
			}
		}
		for label := range labelSet {
			projectFacts.availableLabels = append(projectFacts.availableLabels, label)
		}
		sort.Strings(projectFacts.availableLabels)
		if len(projectFacts.availableLabels) > 256 {
			projectFacts.availableLabels = projectFacts.availableLabels[:256]
		}
		result.projects = append(result.projects, projectFacts)
	}
	return result, nil
}

func selectedProject(facts planningFacts, requested *string) *string {
	if requested != nil {
		value := *requested
		return &value
	}
	if len(facts.projects) == 0 {
		return nil
	}
	value := facts.projects[0].project.ID
	return &value
}

func projectionQuery(input planningport.QueryInput, selected *string) projection.TaskQuery {
	query := projection.TaskQuery{Limit: input.PageSize, Sort: projection.TaskSort(input.Sort)}
	if selected != nil {
		query.ProjectIDs = []string{*selected}
	}
	query.WorkspaceIDs = cloneText(input.WorkspaceIDs)
	query.EpicIDs = cloneText(input.EpicIDs)
	for _, priority := range input.Priorities {
		query.Priorities = append(query.Priorities, domain.Priority(priority))
	}
	hasDone := false
	for _, state := range input.States {
		if state == "done" {
			hasDone = true
			continue
		}
		query.States = append(query.States, projection.BoardState(state))
	}
	switch {
	case hasDone && len(query.States) > 0:
		query.Membership = projection.TaskMembershipAll
	case hasDone:
		query.Membership = projection.TaskMembershipDone
	default:
		query.Membership = projection.TaskMembershipBoard
	}
	query.Labels = cloneText(input.Labels)
	for _, code := range input.Attention {
		query.Attention = append(query.Attention, projection.AttentionCode(code))
	}
	if input.Search != nil {
		query.Search = *input.Search
	}
	if input.Cursor != nil {
		query.After = *input.Cursor
	}
	return query
}

func (counts *planningCounts) add(result projection.TaskProjection) {
	if result.DoneMember {
		counts.done++
	} else {
		counts.open++
	}
	if result.State == projection.StateNeedsYou {
		counts.needsYou++
	}
}

func taskCounts(counts planningCounts) planningport.TaskCounts {
	return planningport.TaskCounts{
		Open: strconv.FormatUint(counts.open, 10), Done: strconv.FormatUint(counts.done, 10),
		NeedsYou: strconv.FormatUint(counts.needsYou, 10),
	}
}

func explanationMessage(code string) string {
	switch code {
	case "project_paused_at_safe_boundary":
		return "Project is paused at a safe boundary; active turns were allowed to finish"
	case "project_resume_reconciled":
		return "Project resume reconciles every durable and external fact before dispatch"
	case "task_cancelled_recovery_preserved":
		return "Task Run was cancelled; configured recovery material is preserved"
	case "emergency_stop_recovery_preserved":
		return "Emergency stop cancelled active Project Runs; configured recovery material is preserved"
	case "control_recovery_ambiguous":
		return "Control recovery is ambiguous; relaunch and destructive cleanup remain blocked"
	case "control_external_observation_unavailable":
		return "Control is waiting for a fresh authoritative external observation"
	case "emergency_stop_confirmation_required":
		return "Emergency stop requires a fresh confirmation from this authenticated human session"
	case "correction_attempt_limit_exhausted":
		return "Three automatic correction attempts are exhausted; review the complete correction lineage"
	case "correction_new_blocking_class_after_cap":
		return "A new blocking finding class appeared after the three-attempt correction cap"
	case "correction_root_cause_repeated":
		return "The same correction root cause repeated without acceptance-coverage progress"
	case "correction_acknowledgement_repeated":
		return "The Task Agent repeated an acknowledgement without producing a changed Candidate"
	case "correction_p2_decision_required":
		return "A P2 correction finding requires an explicit human decision"
	case "correction_human_decision_required":
		return "Current correction feedback requires an explicit human decision"
	case "correction_provider_unavailable":
		return "The original Task Agent provider is unavailable; no hidden replacement was started"
	case "correction_prompt_result_ambiguous":
		return "The correction prompt result is ambiguous and cannot be resent automatically"
	}
	message := strings.ReplaceAll(code, "_", " ")
	if message == "" {
		return "Engine fact is unavailable"
	}
	return strings.ToUpper(message[:1]) + message[1:]
}

func projectionExplanations(codes []projection.BlockerCode, human bool) []planningport.Explanation {
	result := make([]planningport.Explanation, 0, len(codes))
	for _, code := range codes {
		result = append(result, planningport.Explanation{
			Code: string(code), Message: explanationMessage(string(code)), HumanActionRequired: human,
		})
	}
	return result
}

func attentionExplanations(row projection.TaskProjectionRow, input projection.TaskProjectionInput) []planningport.Explanation {
	result := make([]planningport.Explanation, 0, len(row.Projection.Attention))
	for _, code := range row.Projection.Attention {
		reasonCode := string(code)
		if input.Facts.HumanInput.ReasonCode != "" {
			reasonCode = input.Facts.HumanInput.ReasonCode
		}
		var wake *string
		if input.Facts.HumanInput.WakeCondition != "" {
			value := input.Facts.HumanInput.WakeCondition
			wake = &value
		}
		result = append(result, planningport.Explanation{
			Code: reasonCode, Message: explanationMessage(reasonCode), WakeCondition: wake,
			HumanActionRequired: true,
		})
	}
	return result
}

func launchMode(workspaces map[string]domain.Workspace, workspaceID string) string {
	if workspace, ok := workspaces[workspaceID]; ok && workspace.Policy.LaunchPolicy == "manual" {
		return "manual"
	}
	return "automatic"
}

func decimalSum(left, right uint64) string {
	if ^uint64(0)-left < right {
		return strconv.FormatUint(^uint64(0), 10)
	}
	return strconv.FormatUint(left+right, 10)
}

func runtimeBudgetSummary(input projection.TaskProjectionInput) *planningport.RuntimeBudgetSummary {
	if input.Budget == nil || !runtimebudget.ValidLedger(*input.Budget) {
		return nil
	}
	ledger := *input.Budget
	reserved, counts, ok := runtimebudget.Outstanding(ledger)
	if !ok {
		return nil
	}
	state := "current"
	switch ledger.TelemetryState {
	case runtimebudget.UsageUnavailable:
		state = "unavailable"
	case runtimebudget.UsageAmbiguous:
		state = "ambiguous"
	}
	if input.BudgetNeedCode != "" {
		if strings.Contains(input.BudgetNeedCode, "soft_limit") {
			state = "soft_paused"
		} else if strings.Contains(input.BudgetNeedCode, "exhausted") {
			state = "hard_exhausted"
		} else {
			state = "fail_closed"
		}
	}
	type dimension struct {
		name     runtimebudget.Dimension
		enabled  bool
		consumed uint64
		reserved uint64
		limit    uint64
	}
	dimensions := []dimension{
		{runtimebudget.DimensionWallTime, true, ledger.Consumption.WallTimeMilliseconds, reserved.WallTimeMilliseconds, ledger.Policy.WallTimeLimitMilliseconds},
		{runtimebudget.DimensionTokens, true, ledger.Consumption.Tokens, reserved.Tokens, ledger.Policy.TokenLimit},
		{runtimebudget.DimensionTurns, true, ledger.Consumption.Turns, reserved.Turns, ledger.Policy.TurnLimit},
		{runtimebudget.DimensionCost, ledger.Policy.CostLimitMicrousd > 0, ledger.Consumption.CostMicrousd, reserved.CostMicrousd, ledger.Policy.CostLimitMicrousd},
	}
	projectedDimensions := make([]planningport.BudgetDimensionSummary, 0, len(dimensions))
	for _, current := range dimensions {
		measured := current.consumed
		if ^uint64(0)-measured >= current.reserved {
			measured += current.reserved
		} else {
			measured = ^uint64(0)
		}
		ratio := uint64(0)
		if current.enabled {
			ratio = runtimebudget.RatioBasisPoints(measured, current.limit)
		}
		projectedDimensions = append(projectedDimensions, planningport.BudgetDimensionSummary{
			Dimension: string(current.name), Enabled: current.enabled,
			Consumed: strconv.FormatUint(current.consumed, 10), Reserved: strconv.FormatUint(current.reserved, 10),
			Limit: strconv.FormatUint(current.limit, 10), RatioBasisPoints: strconv.FormatUint(ratio, 10),
		})
	}
	countRows := []struct {
		name               string
		consumed, reserved uint32
		limit              uint32
	}{
		{"correction_attempts", ledger.Consumption.CorrectionAttempts, counts.CorrectionAttempts, ledger.Policy.CorrectionLimit},
		{"ci_cycles", ledger.Consumption.CICycles, counts.CICycles, ledger.Policy.CICycleLimit},
		{"replacement_attempts", ledger.Consumption.ReplacementAttempts, counts.ReplacementAttempts, ledger.Policy.ReplacementLimit},
		{"setup_attempts", ledger.Consumption.SetupAttempts, counts.SetupAttempts, ledger.Policy.SetupAttemptLimit},
	}
	projectedCounts := make([]planningport.BudgetCountSummary, 0, len(countRows))
	for _, current := range countRows {
		projectedCounts = append(projectedCounts, planningport.BudgetCountSummary{
			Dimension: current.name, Consumed: strconv.FormatUint(uint64(current.consumed), 10),
			Reserved: strconv.FormatUint(uint64(current.reserved), 10), Limit: strconv.FormatUint(uint64(current.limit), 10),
		})
	}
	turns := map[runtimebudget.Activity]uint64{}
	for _, observation := range ledger.ProviderSnapshots {
		if observation.State == runtimebudget.UsageCurrent {
			turns[observation.Activity]++
		}
	}
	var reason *string
	if input.BudgetNeedCode != "" {
		value := input.BudgetNeedCode
		reason = &value
	}
	return &planningport.RuntimeBudgetSummary{
		PolicyRevision: ledger.Policy.Revision, State: state,
		SoftThresholdBasisPoints: strconv.FormatUint(ledger.Policy.SoftThresholdBasisPoints, 10),
		Dimensions:               projectedDimensions, Counts: projectedCounts,
		WorkerTurns:     decimalSum(turns[runtimebudget.ActivityWorkerBootstrap], turns[runtimebudget.ActivityWorkerTurn]),
		HelperTurns:     strconv.FormatUint(turns[runtimebudget.ActivityHelperTurn], 10),
		ReviewerTurns:   strconv.FormatUint(turns[runtimebudget.ActivityReviewerTurn], 10),
		CorrectionTurns: strconv.FormatUint(turns[runtimebudget.ActivityCorrectionTurn], 10),
		ReasonCode:      reason,
	}
}

func action(kind, label, target string, version uint64, approval *string, emphasis string) planningport.AllowedAction {
	digest := sha256.Sum256([]byte(kind + "\x1f" + target + "\x1f" + strconv.FormatUint(version, 10)))
	request := "action-" + strings.ReplaceAll(kind, ".", "-") + "-" + hex.EncodeToString(digest[:16])
	var targetID *string
	if target != "" {
		value := target
		targetID = &value
	}
	return planningport.AllowedAction{
		Kind: kind, Label: label, TargetID: targetID, RequestID: request,
		IdempotencyKey: request, ExpectedVersion: strconv.FormatUint(version, 10),
		HumanApprovalRef: approval, Emphasis: emphasis,
	}
}

func runControlExplanation(run *domain.Run) *planningport.Explanation {
	if run == nil || run.Execution.Control.SchemaVersion == "" || run.Execution.Control.ExplanationCode == "" {
		return nil
	}
	code := run.Execution.Control.ExplanationCode
	wake := "explicit_control_reconciliation"
	human := false
	if run.Execution.Control.Phase == execution.ControlPaused {
		wake, human = "explicit_project_resume", true
	}
	if run.Execution.Control.Phase == execution.ControlNeedsYou {
		wake, human = "fresh_control_reconciliation_or_human_recovery", true
	}
	return &planningport.Explanation{
		Code: code, Message: explanationMessage(code), WakeCondition: &wake, HumanActionRequired: human,
	}
}

func taskSummary(row projection.TaskProjectionRow, input projection.TaskProjectionInput, project domain.Project, run *domain.Run, workspaces map[string]domain.Workspace, cursor string) planningport.TaskSummary {
	state := string(row.Projection.State)
	if row.Projection.DoneMember {
		state = "done"
	}
	var epicID *string
	if row.EpicID != "" {
		value := row.EpicID
		epicID = &value
	}
	blockers := projectionExplanations(row.Projection.Blockers, false)
	needs := attentionExplanations(row, input)
	disposition := "waiting"
	if len(needs) > 0 {
		disposition = "needs_you"
	} else if row.Projection.State == projection.StateQueued && len(blockers) == 0 {
		disposition = "eligible"
	}
	explanations := append(slices.Clone(blockers), needs...)
	if control := runControlExplanation(run); control != nil {
		explanations = append(explanations, *control)
		if len(needs) == 0 {
			blockers = append(blockers, *control)
		}
	}
	actions := []planningport.AllowedAction{}
	if run != nil && !run.Execution.Terminal && run.Execution.Control.Phase != execution.ControlContaining &&
		project.Control.Intent.Kind != execution.ControlEmergencyStop {
		actions = append(actions, action("task.cancel", "Cancel Task", run.ID, run.Version, nil, "danger"))
	}
	return planningport.TaskSummary{
		ID: row.TaskID, ProjectID: row.ProjectID, WorkspaceID: row.WorkspaceID, EpicID: epicID,
		Version: strconv.FormatUint(input.Facts.TaskVersion, 10), Key: row.Key, Title: row.Title,
		DerivedState: state, Priority: string(row.Priority), Labels: cloneText(row.Labels),
		UpdatedAt: time.UnixMilli(row.UpdatedAtUnixMillis).UTC().Format(time.RFC3339Nano),
		Blockers:  blockers, NeedsYou: needs, AllowedActions: actions,
		SchedulingFacts: planningport.SchedulerFacts{
			LaunchMode: launchMode(workspaces, row.WorkspaceID), LaunchDisposition: disposition,
			FactsRevision: cursor, Explanations: explanations,
		},
		RuntimeBudget: runtimeBudgetSummary(input),
	}
}

func projectSummaries(facts planningFacts) []planningport.ProjectSummary {
	result := make([]planningport.ProjectSummary, 0, len(facts.projects))
	for _, current := range facts.projects {
		project := current.project
		actions := []planningport.AllowedAction{}
		var control *planningport.Explanation
		if project.Control.SchemaVersion != "" {
			wake := "control_reconciliation"
			human := false
			if project.Control.Phase == execution.ControlAwaitingConfirmation {
				wake, human = "fresh_authenticated_human_confirmation", true
			}
			control = &planningport.Explanation{
				Code: project.Control.ExplanationCode, Message: explanationMessage(project.Control.ExplanationCode),
				WakeCondition: &wake, HumanActionRequired: human,
			}
		}
		if project.Control.Phase == execution.ControlAwaitingConfirmation && project.Control.Confirmation != nil {
			approval := project.Control.Confirmation.ID
			if project.State == "active" {
				actions = append(actions, action("project.pause", "Pause Project", project.ID, project.Version, nil, "secondary"))
			}
			actions = append(actions, action("project.emergency-stop.confirm", "Confirm emergency stop", project.ID, project.Version, &approval, "danger"))
		} else if project.State == "active" {
			actions = append(actions,
				action("project.pause", "Pause Project", project.ID, project.Version, nil, "secondary"),
				action("project.emergency-stop.prepare", "Emergency stop…", project.ID, project.Version, nil, "danger"),
			)
		} else if project.State == "paused" {
			if project.Control.Phase != execution.ControlContaining {
				actions = append(actions, action("project.resume", "Resume Project", project.ID, project.Version, nil, "primary"))
			}
			if !project.Control.EmergencyLatched {
				actions = append(actions, action("project.emergency-stop.prepare", "Emergency stop…", project.ID, project.Version, nil, "danger"))
			}
		}
		result = append(result, planningport.ProjectSummary{
			ID: project.ID, Version: strconv.FormatUint(project.Version, 10),
			Name: project.Name, State: project.State,
			WorkspaceCount: strconv.Itoa(len(current.workspaces)), TaskCounts: taskCounts(current.counts),
			Control: control, AllowedActions: actions,
		})
	}
	return result
}

func matchingProject(facts planningFacts, selected *string) *planningProjectFacts {
	if selected == nil {
		return nil
	}
	for index := range facts.projects {
		if facts.projects[index].project.ID == *selected {
			return &facts.projects[index]
		}
	}
	return nil
}

func workspaceSummaries(project *planningProjectFacts) []planningport.WorkspaceSummary {
	if project == nil {
		return []planningport.WorkspaceSummary{}
	}
	workspaces := slices.Clone(project.workspaces)
	slices.SortFunc(workspaces, func(left, right domain.Workspace) int {
		if byName := strings.Compare(left.Name, right.Name); byName != 0 {
			return byName
		}
		return strings.Compare(left.ID, right.ID)
	})
	result := make([]planningport.WorkspaceSummary, 0, len(workspaces))
	for _, workspace := range workspaces {
		health := "healthy"
		if project.project.State == "degraded" {
			health = "degraded"
		}
		result = append(result, planningport.WorkspaceSummary{
			ID: workspace.ID, ProjectID: workspace.ProjectID, Version: strconv.FormatUint(workspace.Version, 10),
			Name: workspace.Name, Health: health, DefaultBaseBranch: workspace.DefaultBaseBranch,
			TaskCounts: taskCounts(project.workspaceCounts[workspace.ID]), AllowedActions: []planningport.AllowedAction{},
		})
	}
	return result
}

func epicSummaries(project *planningProjectFacts) []planningport.EpicSummary {
	if project == nil {
		return []planningport.EpicSummary{}
	}
	epics := slices.Clone(project.epics)
	slices.SortFunc(epics, func(left, right domain.Epic) int {
		if byKey := strings.Compare(defaultPlanningKey(left.Key, left.ID), defaultPlanningKey(right.Key, right.ID)); byKey != 0 {
			return byKey
		}
		return strings.Compare(left.ID, right.ID)
	})
	result := make([]planningport.EpicSummary, 0, len(epics))
	for _, epic := range epics {
		progress := project.epicProgress[epic.ID]
		var priority *string
		if epic.Priority != "" {
			value := string(domain.EffectivePriority(epic.Priority))
			priority = &value
		}
		result = append(result, planningport.EpicSummary{
			ID: epic.ID, ProjectID: epic.ProjectID, Version: strconv.FormatUint(epic.Version, 10),
			Key: defaultPlanningKey(epic.Key, epic.ID), Title: epic.Title, Priority: priority,
			Labels: cloneText(epic.Labels),
			Progress: planningport.EpicProgress{
				Completed: strconv.FormatUint(progress.completed, 10), Total: strconv.FormatUint(progress.total, 10),
			},
			Blockers: []planningport.Explanation{}, AllowedActions: []planningport.AllowedAction{},
		})
	}
	return result
}

func availableLabels(project *planningProjectFacts) []string {
	if project == nil {
		return []string{}
	}
	return cloneText(project.availableLabels)
}

func capacityFacts(project *planningProjectFacts, selectedWorkspaces []string) planningport.CapacityFacts {
	limits := scheduling.DefaultLimits()
	var active uint64
	selected := make(map[string]struct{}, len(selectedWorkspaces))
	for _, workspaceID := range selectedWorkspaces {
		selected[workspaceID] = struct{}{}
	}
	workspaceActive := make(map[string]uint64)
	if project != nil {
		for _, input := range project.inputs {
			if !project.activeRuns[input.TaskID] {
				continue
			}
			active++
			workspaceActive[input.WorkspaceID]++
		}
	}
	var activeWorkspace uint64
	for workspaceID, count := range workspaceActive {
		_, explicitlySelected := selected[workspaceID]
		if (len(selected) == 0 || explicitlySelected) && count > activeWorkspace {
			activeWorkspace = count
		}
	}
	return planningport.CapacityFacts{
		ActiveTasks: strconv.FormatUint(active, 10), MaxActiveTasks: strconv.FormatUint(limits.MaxActiveTasks, 10),
		ActiveWorkspaceTasks:       strconv.FormatUint(activeWorkspace, 10),
		MaxActiveTasksPerWorkspace: strconv.FormatUint(limits.MaxActiveTasksPerWorkspace, 10),
		ActiveAgents:               strconv.FormatUint(active, 10), MaxConcurrentAgents: strconv.FormatUint(limits.MaxConcurrentAgents, 10),
		ReservedAgents: "0",
	}
}

func cloneQuery(input planningport.QueryInput) planningport.QueryInput {
	input.WorkspaceIDs = cloneText(input.WorkspaceIDs)
	input.EpicIDs = cloneText(input.EpicIDs)
	input.States = cloneText(input.States)
	input.Priorities = cloneText(input.Priorities)
	input.Labels = cloneText(input.Labels)
	input.Attention = cloneText(input.Attention)
	return input
}

func (reader *PlanningReader) project(input planningport.QueryInput, cursor uint64, facts planningFacts) (planningport.Snapshot, error) {
	selected := selectedProject(facts, input.ProjectID)
	project := matchingProject(facts, selected)
	inputs := []projection.TaskProjectionInput{}
	if project != nil {
		inputs = project.inputs
	}
	page, err := projection.QueryTaskProjections(inputs, cursor, projectionQuery(input, selected))
	if err != nil {
		return planningport.Snapshot{}, err
	}
	inputByID := make(map[string]projection.TaskProjectionInput, len(page.Tasks))
	workspaces := make(map[string]domain.Workspace)
	if project != nil {
		wanted := make(map[string]struct{}, len(page.Tasks))
		for _, row := range page.Tasks {
			wanted[row.TaskID] = struct{}{}
		}
		for _, candidate := range project.inputs {
			if _, ok := wanted[candidate.TaskID]; ok {
				inputByID[candidate.TaskID] = candidate
			}
		}
		for _, workspace := range project.workspaces {
			workspaces[workspace.ID] = workspace
		}
	}
	tasks := make([]planningport.TaskSummary, 0, len(page.Tasks))
	for _, row := range page.Tasks {
		var run *domain.Run
		if project != nil {
			if latest, ok := project.latestRuns[row.TaskID]; ok {
				value := latest
				run = &value
			}
		}
		projectValue := domain.Project{}
		if project != nil {
			projectValue = project.project
		}
		tasks = append(tasks, taskSummary(row, inputByID[row.TaskID], projectValue, run, workspaces, page.SnapshotCursor))
	}
	definition, err := planningport.EmbeddedDefinition()
	if err != nil {
		return planningport.Snapshot{}, err
	}
	hash, err := planningport.SchemaSHA256()
	if err != nil {
		return planningport.Snapshot{}, err
	}
	var next *string
	if page.NextCursor != "" {
		value := page.NextCursor
		next = &value
	}
	return planningport.Snapshot{
		SchemaVersion: definition.SchemaVersion, ContractVersion: definition.ContractVersion,
		ContractHash: hash, Cursor: page.SnapshotCursor,
		Page: planningport.Page{
			SelectedProjectID: selected, SelectedWorkspaceIDs: cloneText(input.WorkspaceIDs),
			SelectedEpicIDs: cloneText(input.EpicIDs), AppliedQuery: cloneQuery(input),
			AvailableSorts: slices.Clone(definition.StableSorts), AvailableLabels: availableLabels(project),
			Projects: projectSummaries(facts), Workspaces: workspaceSummaries(project), Epics: epicSummaries(project),
			Tasks: tasks, Capacity: capacityFacts(project, input.WorkspaceIDs),
			SurfaceActions: []planningport.AllowedAction{}, TotalTasks: strconv.FormatUint(page.TotalTasks, 10),
			NextCursor: next,
		},
	}, nil
}

// Query returns one cursor-stable, contract-bound planning page. A page cursor
// from an older TaskStore event snapshot is rejected by the pure projection.
func (reader *PlanningReader) Query(ctx context.Context, input planningport.QueryInput) (planningport.Snapshot, error) {
	if err := planningport.ValidateQuery(input); err != nil {
		return planningport.Snapshot{}, ErrPlanningQueryInvalid
	}
	for range snapshotReadAttempts {
		before, err := reader.store.LatestEventSequence(ctx)
		if err != nil {
			return planningport.Snapshot{}, fmt.Errorf("read planning cursor: %w", err)
		}
		facts, err := reader.loadFacts(ctx)
		if err != nil {
			return planningport.Snapshot{}, err
		}
		after, err := reader.store.LatestEventSequence(ctx)
		if err != nil {
			return planningport.Snapshot{}, fmt.Errorf("read planning cursor: %w", err)
		}
		if before == after {
			return reader.project(input, after, facts)
		}
	}
	return planningport.Snapshot{}, ErrSnapshotChanged
}
