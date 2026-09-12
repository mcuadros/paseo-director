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
	domainfeedback "github.com/mcuadros/director-engine/domain/feedback"
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
	tasks           map[string]domain.Task
	overrides       []domain.DependencyOverride
	inputs          []projection.TaskProjectionInput
	activeRuns      map[string]bool
	latestRuns      map[string]domain.Run
	candidates      map[string]domain.Candidate
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

// PlanningProjectListReader and PlanningProjectGraphReader are optional typed
// read optimizations. Together they preserve the complete graph validation
// performed by ordinary TaskStore reads while avoiding four reloads of the
// same Project Tasks for one Board/List page.
type PlanningProjectListReader interface {
	PlanningProjects(context.Context) ([]domain.Project, error)
}

type PlanningProjectGraphReader interface {
	PlanningProject(context.Context, string) (
		domain.Project,
		[]domain.Workspace,
		[]domain.Epic,
		[]domain.Task,
		[]domain.DependencyOverride,
		map[string]int64,
		error,
	)
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
	var projects []domain.Project
	var err error
	if optimized, ok := reader.store.(PlanningProjectListReader); ok {
		projects, err = optimized.PlanningProjects(ctx)
	} else {
		projects, err = reader.store.Projects(ctx)
	}
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
		var workspaces []domain.Workspace
		var epics []domain.Epic
		var tasks []domain.Task
		var overrides []domain.DependencyOverride
		var updatedAtByTask map[string]int64
		optimizedGraph := false
		if optimized, ok := reader.store.(PlanningProjectGraphReader); ok {
			project, workspaces, epics, tasks, overrides, updatedAtByTask, err = optimized.PlanningProject(ctx, project.ID)
			if err != nil {
				return planningFacts{}, fmt.Errorf("read planning Project graph: %w", err)
			}
			optimizedGraph = true
		} else {
			workspaces, err = reader.store.Workspaces(ctx, project.ID)
			if err != nil {
				return planningFacts{}, fmt.Errorf("read planning Workspaces: %w", err)
			}
			epics, err = reader.store.Epics(ctx, project.ID)
			if err != nil {
				return planningFacts{}, fmt.Errorf("read planning Epics: %w", err)
			}
			tasks, err = reader.store.Tasks(ctx, project.ID)
			if err != nil {
				return planningFacts{}, fmt.Errorf("read planning Tasks: %w", err)
			}
			overrides, err = reader.store.DependencyOverrides(ctx, project.ID)
			if err != nil {
				return planningFacts{}, fmt.Errorf("read planning overrides: %w", err)
			}
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
			tasks: make(map[string]domain.Task, len(tasks)), overrides: overrides,
			inputs: make([]projection.TaskProjectionInput, 0, len(tasks)), activeRuns: make(map[string]bool),
			latestRuns:      make(map[string]domain.Run),
			candidates:      make(map[string]domain.Candidate),
			workspaceCounts: make(map[string]planningCounts, len(workspaces)),
			epicProgress:    make(map[string]planningProgress, len(epics)),
		}
		labelSet := make(map[string]struct{})
		updatesAvailable := optimizedGraph
		if !updatesAvailable {
			updatedAtByTask = make(map[string]int64)
		}
		updates, separateUpdatesAvailable := reader.store.(PlanningTaskUpdateReader)
		if !updatesAvailable && separateUpdatesAvailable {
			updatedAtByTask, err = updates.PlanningTaskUpdatedAt(ctx, project.ID)
			if err != nil {
				return planningFacts{}, fmt.Errorf("read planning Task update facts: %w", err)
			}
			updatesAvailable = true
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
			projectFacts.tasks[task.ID] = task
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
					projectFacts.candidates[current.ID] = current
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
	case "cleanup_response_unknown":
		return "Cleanup result is ambiguous; recoverable material and any recreated resource are preserved"
	case "cleanup_snapshot_unverified":
		return "Cleanup is blocked until the exact recovery snapshot is durably verified"
	case "cleanup_disk_pressure":
		return "Cleanup is blocked by disk pressure; unintegrated work remains in place"
	case "cleanup_owner_mismatch", "cleanup_repository_mismatch", "cleanup_binding_changed":
		return "Cleanup ownership or repository identity changed; no destructive effect was authorized"
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

func feedbackSummary(run *domain.Run) *planningport.FeedbackSummary {
	if run == nil || run.Execution.Feedback == nil || !domainfeedback.ValidState(*run.Execution.Feedback) {
		return nil
	}
	state := run.Execution.Feedback
	var batch, reason *string
	if state.CorrectionBatchSHA != "" {
		value := state.CorrectionBatchSHA
		batch = &value
	}
	if state.NeedsYouCode != "" {
		value := state.NeedsYouCode
		reason = &value
	}
	return &planningport.FeedbackSummary{Phase: string(state.Phase),
		CurrentActionable: strconv.Itoa(len(domainfeedback.CurrentActionable(*state))),
		AuditRecords:      strconv.Itoa(len(state.Records)), CurrentRevision: state.CurrentRevision,
		CorrectionBatch: batch, ReasonCode: reason}
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
		Feedback:      feedbackSummary(run),
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

var ErrTaskDetailCursorInvalid = errors.New("task detail cursor is invalid")

type PlanningEventReader interface {
	Events(context.Context, domain.EventQuery) ([]domain.Event, error)
}

func detailExplanation(code, message string) *planningport.Explanation {
	wake := "refresh_exact_host_task_binding"
	return &planningport.Explanation{
		Code: code, Message: message, WakeCondition: &wake, HumanActionRequired: false,
	}
}

func currentNativeBinding(run *domain.Run, projectID, workspaceID, taskID string) (string, string, *planningport.Explanation) {
	if run == nil {
		return "", "", detailExplanation("task_not_started", "No Run has been created for this Task")
	}
	state := run.Execution
	if state.Scope.ProjectID != "" && (state.Scope.ProjectID != projectID || state.Scope.WorkspaceID != workspaceID ||
		state.Scope.TaskID != taskID || state.Scope.RunID != run.ID) {
		return "", "", detailExplanation("run_binding_contradictory", "The current Run binding is contradictory; agent navigation is disabled")
	}
	workspace, agent := state.HostView.ExternalID, state.PrimarySession.NativeAgentID
	agentEffect, visibility := state.Agent, state.WorkerVisibility
	if state.PrimaryRecovery.SchemaVersion != "" && execution.ValidPrimaryRecovery(state.PrimaryRecovery, state) &&
		state.PrimaryRecovery.Phase == execution.PrimaryRecoveryComplete && state.PrimaryRecovery.ReplacementAgent.ExternalID != "" {
		agent = state.PrimaryRecovery.ReplacementSession.NativeAgentID
		agentEffect = state.PrimaryRecovery.ReplacementAgent
		visibility = state.PrimaryRecovery.ReplacementVisibility
	}
	if workspace == "" || agent == "" || agentEffect.ExternalID != agent || visibility == nil ||
		visibility.AgentID != agent || visibility.ExecutionWorkspaceID != workspace ||
		visibility.ObservedDigest == "" || visibility.ObservedDigest != visibility.Digest {
		return "", "", detailExplanation("agent_binding_unavailable", "No exact current Paseo agent and Execution Workspace binding is available")
	}
	if state.AgentArchive.Phase == execution.EffectComplete || state.HostViewArchive.Phase == execution.EffectComplete {
		return "", "", detailExplanation("agent_archived", "The bound Paseo agent is no longer available for navigation")
	}
	return workspace, agent, nil
}

func projectedTaskRow(input projection.TaskProjectionInput) projection.TaskProjectionRow {
	return projection.TaskProjectionRow{
		TaskID: input.TaskID, ProjectID: input.ProjectID, WorkspaceID: input.WorkspaceID, EpicID: input.EpicID,
		Key: input.Key, Title: input.Title, Priority: input.Priority, Labels: cloneText(input.Labels),
		QueuedAtUnixMillis: input.QueuedAtUnixMillis, UpdatedAtUnixMillis: input.UpdatedAtUnixMillis,
		Projection: projection.DeriveTaskProjection(input.Facts),
	}
}

func taskInput(project *planningProjectFacts, taskID string) (projection.TaskProjectionInput, bool) {
	for _, input := range project.inputs {
		if input.TaskID == taskID {
			return input, true
		}
	}
	return projection.TaskProjectionInput{}, false
}

type detailMatch struct {
	project *planningProjectFacts
	task    domain.Task
	run     *domain.Run
	input   projection.TaskProjectionInput
}

func findTaskDetail(facts planningFacts, input planningport.TaskDetailQueryInput) (detailMatch, *planningport.Explanation) {
	matches := []detailMatch{}
	for projectIndex := range facts.projects {
		project := &facts.projects[projectIndex]
		for taskID, task := range project.tasks {
			if input.Context == "board" && (input.TaskID == nil || taskID != *input.TaskID) {
				continue
			}
			runValue, hasRun := project.latestRuns[taskID]
			var run *domain.Run
			if hasRun {
				current := runValue
				run = &current
			}
			if input.Context == "agent" {
				workspace, agent, unavailable := currentNativeBinding(run, project.project.ID, task.WorkspaceIDs[0], task.ID)
				if unavailable != nil || input.PaseoWorkspaceID == nil || input.PaseoAgentID == nil ||
					workspace != *input.PaseoWorkspaceID || agent != *input.PaseoAgentID {
					continue
				}
			}
			projected, ok := taskInput(project, taskID)
			if !ok {
				continue
			}
			matches = append(matches, detailMatch{project: project, task: task, run: run, input: projected})
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return detailMatch{}, detailExplanation("task_binding_ambiguous", "More than one Task matches this exact context; no fallback was selected")
	}
	if input.Context == "agent" {
		return detailMatch{}, detailExplanation("agent_task_unbound", "No Director Task is exactly bound to this Paseo agent and workspace")
	}
	return detailMatch{}, detailExplanation("task_unavailable", "The requested Task is unavailable on this exact Director host")
}

func detailBinding(hostID string, match detailMatch) planningport.TaskDetailBinding {
	binding := planningport.TaskDetailBinding{
		HostID: hostID, ProjectID: match.task.ProjectID, WorkspaceID: match.task.WorkspaceIDs[0], TaskID: match.task.ID,
		TaskVersion: strconv.FormatUint(match.task.Version, 10), AgentNavigation: "unavailable",
	}
	if match.run == nil {
		binding.UnavailableReason = detailExplanation("task_not_started", "No Run has been created for this Task")
		return binding
	}
	runID, runNumber, runVersion := match.run.ID, strconv.FormatUint(match.run.Number, 10), strconv.FormatUint(match.run.Version, 10)
	binding.RunID, binding.RunNumber, binding.RunVersion = &runID, &runNumber, &runVersion
	if match.run.CurrentCandidateID != "" {
		if candidate, ok := match.project.candidates[match.run.CurrentCandidateID]; ok && candidate.RunID == match.run.ID && len(candidate.CommitSHA) == 40 {
			candidateID, candidateSHA := candidate.ID, candidate.CommitSHA
			binding.CandidateID, binding.CandidateSHA = &candidateID, &candidateSHA
		}
	}
	workspace, agent, unavailable := currentNativeBinding(match.run, match.task.ProjectID, match.task.WorkspaceIDs[0], match.task.ID)
	if unavailable != nil {
		binding.UnavailableReason = unavailable
		return binding
	}
	binding.PaseoWorkspaceID, binding.PaseoAgentID = &workspace, &agent
	binding.AgentNavigation = "available"
	return binding
}

func detailAcceptance(task domain.Task) []planningport.TaskAcceptanceCriterion {
	status := "pending"
	if task.Complete {
		status = "satisfied"
	}
	return []planningport.TaskAcceptanceCriterion{{ID: "task-acceptance", Text: task.AcceptanceCriteria, Status: status}}
}

func nodeDetail(project *planningProjectFacts, reference domain.PlanningNodeRef) (string, string, bool) {
	if reference.Kind == domain.PlanningNodeTask {
		if task, ok := project.tasks[reference.ID]; ok {
			return defaultPlanningKey(task.Key, task.ID), task.Title, task.Complete
		}
	}
	if reference.Kind == domain.PlanningNodeEpic {
		for _, epic := range project.epics {
			if epic.ID == reference.ID {
				return defaultPlanningKey(epic.Key, epic.ID), epic.Title, epic.Complete
			}
		}
	}
	return reference.ID, "Unavailable dependency", false
}

func detailDependencies(match detailMatch) []planningport.TaskDependencyDetail {
	result := make([]planningport.TaskDependencyDetail, 0, len(match.task.Dependencies))
	for _, dependency := range match.task.Dependencies {
		key, title, complete := nodeDetail(match.project, dependency.On)
		overrideStatus := "none"
		for _, override := range match.project.overrides {
			if override.TaskID == match.task.ID && override.Dependency == dependency {
				overrideStatus = "pending"
				if override.Granted && override.ActorKind == domain.PlanningActorHuman && override.TaskVersion == match.task.Version {
					overrideStatus, complete = "approved", true
				}
			}
		}
		message := "Waiting for " + key
		code := "dependency_wait"
		if complete {
			message, code = "Dependency requirement is satisfied", "dependency_satisfied"
		}
		result = append(result, planningport.TaskDependencyDetail{
			ID: dependency.On.ID, Kind: string(dependency.On.Kind), Key: key, Title: title, Satisfied: complete,
			OverrideStatus: overrideStatus, Explanation: planningport.Explanation{Code: code, Message: message, HumanActionRequired: false},
		})
	}
	return result
}

func activityKind(eventType string) string {
	switch {
	case strings.Contains(eventType, "review"):
		return "review"
	case strings.Contains(eventType, "validation"), strings.Contains(eventType, "ci"):
		return "validation"
	case strings.Contains(eventType, "feedback"), strings.Contains(eventType, "human"), strings.Contains(eventType, "approval"):
		return "human"
	case strings.Contains(eventType, "task"), strings.Contains(eventType, "project"), strings.Contains(eventType, "epic"), strings.Contains(eventType, "dependency"):
		return "planning"
	default:
		return "execution"
	}
}

func activitySummary(eventType string) string {
	known := map[string]string{
		"task.created":         "Task created",
		"task.updated":         "Task details updated",
		"run.created":          "Run created",
		"candidate.admitted":   "Candidate admitted",
		"review.completed":     "Independent review recorded",
		"validation.completed": "Validation result recorded",
		"task.closed":          "Task completed",
	}
	if summary := known[eventType]; summary != "" {
		return summary
	}
	return "Director recorded a " + strings.ReplaceAll(strings.ReplaceAll(eventType, ".", " "), "_", " ") + " event"
}

func boundedDetailID(value string) bool {
	return value != "" && len(value) <= 128 && value == strings.TrimSpace(value) &&
		strings.IndexFunc(value, func(character rune) bool { return character < 0x20 || character == 0x7f }) < 0
}

func (reader *PlanningReader) detailActivity(ctx context.Context, match detailMatch, after uint64) ([]planningport.TaskActivity, error) {
	eventsStore, ok := reader.store.(PlanningEventReader)
	if !ok {
		return []planningport.TaskActivity{}, nil
	}
	all := []domain.Event{}
	taskEvents, err := eventsStore.Events(ctx, domain.EventQuery{AggregateID: match.task.ID, AfterGlobalSequence: after, Limit: 100})
	if err != nil {
		return nil, fmt.Errorf("read Task activity: %w", err)
	}
	all = append(all, taskEvents...)
	if match.run != nil {
		runEvents, runErr := eventsStore.Events(ctx, domain.EventQuery{RunID: match.run.ID, AfterGlobalSequence: after, Limit: 100})
		if runErr != nil {
			return nil, fmt.Errorf("read Run activity: %w", runErr)
		}
		all = append(all, runEvents...)
	}
	slices.SortFunc(all, func(left, right domain.Event) int {
		if left.GlobalSequence < right.GlobalSequence {
			return -1
		}
		if left.GlobalSequence > right.GlobalSequence {
			return 1
		}
		return 0
	})
	seen := map[uint64]struct{}{}
	result := []planningport.TaskActivity{}
	for _, event := range all {
		if _, duplicate := seen[event.GlobalSequence]; duplicate || len(result) == 100 {
			continue
		}
		seen[event.GlobalSequence] = struct{}{}
		code := event.Type
		if !boundedDetailID(code) {
			code = "director.event"
		}
		id := event.ID
		if !boundedDetailID(id) {
			id = "event-" + strconv.FormatUint(event.GlobalSequence, 10)
		}
		result = append(result, planningport.TaskActivity{
			ID: id, Sequence: strconv.FormatUint(event.GlobalSequence, 10), Kind: activityKind(code), Code: code, Message: activitySummary(code),
		})
	}
	return result, nil
}

func cloneTaskDetailQuery(input planningport.TaskDetailQueryInput) planningport.TaskDetailQueryInput {
	result := input
	if input.TaskID != nil {
		value := *input.TaskID
		result.TaskID = &value
	}
	if input.PaseoWorkspaceID != nil {
		value := *input.PaseoWorkspaceID
		result.PaseoWorkspaceID = &value
	}
	if input.PaseoAgentID != nil {
		value := *input.PaseoAgentID
		result.PaseoAgentID = &value
	}
	if input.AfterCursor != nil {
		value := *input.AfterCursor
		result.AfterCursor = &value
	}
	return result
}

// TaskDetail returns one exact-host semantic projection or an explicit
// unavailable result. It never substitutes another Task, Run, agent, or host.
func (reader *PlanningReader) TaskDetail(ctx context.Context, input planningport.TaskDetailQueryInput) (planningport.TaskDetailSnapshot, error) {
	if err := planningport.ValidateTaskDetailQuery(input); err != nil {
		return planningport.TaskDetailSnapshot{}, ErrPlanningQueryInvalid
	}
	afterCursor := uint64(0)
	if input.AfterCursor != nil {
		value, err := strconv.ParseUint(*input.AfterCursor, 10, 64)
		if err != nil {
			return planningport.TaskDetailSnapshot{}, ErrPlanningQueryInvalid
		}
		afterCursor = value
	}
	for range snapshotReadAttempts {
		before, err := reader.store.LatestEventSequence(ctx)
		if err != nil {
			return planningport.TaskDetailSnapshot{}, fmt.Errorf("read Task detail cursor: %w", err)
		}
		if afterCursor > before {
			return planningport.TaskDetailSnapshot{}, ErrTaskDetailCursorInvalid
		}
		facts, err := reader.loadFacts(ctx)
		if err != nil {
			return planningport.TaskDetailSnapshot{}, err
		}
		match, unavailable := findTaskDetail(facts, input)
		var detail *planningport.TaskDetail
		if unavailable == nil {
			activity, activityErr := reader.detailActivity(ctx, match, afterCursor)
			if activityErr != nil {
				return planningport.TaskDetailSnapshot{}, activityErr
			}
			workspaces := make(map[string]domain.Workspace, len(match.project.workspaces))
			for _, workspace := range match.project.workspaces {
				workspaces[workspace.ID] = workspace
			}
			cursor := strconv.FormatUint(before, 10)
			summary := taskSummary(projectedTaskRow(match.input), match.input, match.project.project, match.run, workspaces, cursor)
			detail = &planningport.TaskDetail{
				Binding: detailBinding(input.HostID, match), Summary: summary, Objective: match.task.Objective,
				AcceptanceCriteria: detailAcceptance(match.task), Dependencies: detailDependencies(match),
				ConfigurationTarget: planningport.ConfigurationTarget{Scope: "task", ID: match.task.ID},
				Configuration:       []planningport.ConfigurationEntry{}, ConfigurationPreview: nil, Activity: activity,
			}
		}
		after, err := reader.store.LatestEventSequence(ctx)
		if err != nil {
			return planningport.TaskDetailSnapshot{}, fmt.Errorf("read Task detail cursor: %w", err)
		}
		if before != after {
			continue
		}
		definition, err := planningport.EmbeddedDefinition()
		if err != nil {
			return planningport.TaskDetailSnapshot{}, err
		}
		hash, err := planningport.SchemaSHA256()
		if err != nil {
			return planningport.TaskDetailSnapshot{}, err
		}
		return planningport.TaskDetailSnapshot{
			SchemaVersion: definition.SchemaVersion, ContractVersion: definition.ContractVersion, ContractHash: hash,
			HostID: input.HostID, Cursor: strconv.FormatUint(after, 10), Query: cloneTaskDetailQuery(input), Detail: detail, UnavailableReason: unavailable,
		}, nil
	}
	return planningport.TaskDetailSnapshot{}, ErrSnapshotChanged
}
