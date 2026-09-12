// SPDX-License-Identifier: Apache-2.0

package board

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/correction"
	"github.com/mcuadros/director-engine/domain/execution"
	feedbackdomain "github.com/mcuadros/director-engine/domain/feedback"
	"github.com/mcuadros/director-engine/domain/runtimebudget"
	planningport "github.com/mcuadros/director-engine/ports/planning"
	"github.com/mcuadros/director-engine/projection"
)

func planningQueryInput() planningport.QueryInput {
	return planningport.QueryInput{
		WorkspaceIDs: []string{}, EpicIDs: []string{}, States: []string{}, Priorities: []string{},
		Labels: []string{}, Attention: []string{}, Sort: string(projection.TaskSortSchedulerOrder), PageSize: planningport.MaximumPageSize,
	}
}

func TestPlanningReaderProjectsRuntimeBudgetForOrganizerVisibility(t *testing.T) {
	store := planningScaleStore()
	policy := runtimebudget.NewPolicy("configuration-revision", 100_000, 100, 10, 0, 4)
	ledger, err := runtimebudget.NewLedger(policy, 0)
	if err != nil {
		t.Fatal(err)
	}
	request := runtimebudget.ReserveRequest{
		ID: "reservation-worker", EffectID: "effect-worker", Activity: runtimebudget.ActivityWorkerTurn,
		LeaseEpoch: 1, PolicyRevision: policy.Revision,
		Demand: runtimebudget.Demand{WallTimeMilliseconds: 100, Tokens: 10, Turns: 1},
	}
	ledger, decision, err := runtimebudget.Reserve(ledger, request, 0)
	if err != nil || decision.Disposition != runtimebudget.DispositionAllow {
		t.Fatal(decision, err)
	}
	usage := runtimebudget.ProviderObservation{
		ID: "usage-worker", EffectID: request.EffectID, AgentID: "agent-worker",
		Activity: request.Activity, LeaseEpoch: 1, PolicyRevision: policy.Revision,
		Sequence: 1, ObservedAtMillis: 1,
		ProviderUsage: runtimebudget.ProviderUsage{
			State: runtimebudget.UsageCurrent, SourceRevision: "source-worker",
			InputTokensPresent: true, InputTokens: 84,
			OutputTokensPresent: true,
		},
	}
	usage.FactHash = runtimebudget.ProviderObservationHash(usage)
	ledger, decision, err = runtimebudget.ApplyProviderObservation(ledger, usage)
	if err != nil || decision.Disposition != runtimebudget.DispositionAllow {
		t.Fatal(decision, err)
	}
	_, decision, err = runtimebudget.Reserve(ledger, runtimebudget.ReserveRequest{
		ID: "reservation-reviewer", EffectID: "effect-reviewer", Activity: runtimebudget.ActivityReviewerTurn,
		LeaseEpoch: 1, PolicyRevision: policy.Revision,
		Demand: runtimebudget.Demand{WallTimeMilliseconds: 100, Tokens: 1, Turns: 1},
	}, 2)
	if err != nil || decision.Disposition != runtimebudget.DispositionSoftPause {
		t.Fatal(decision, err)
	}
	taskID := store.tasks["project-scale"][1].ID
	store.runs[taskID] = []domain.Run{{
		ID: "run-budget", TaskID: taskID, Number: 1,
		Execution: execution.State{
			Budget: ledger,
			NeedsYou: &execution.NeedsYou{
				Code: execution.NeedCode(decision.Reason), WakeCondition: "human_acknowledges_exact_budget_warning",
			},
		},
	}}
	input := planningQueryInput()
	search := "Open task 00002"
	input.Search = &search
	result, err := NewPlanningReader(store).Query(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	var summary *planningport.TaskSummary
	for index := range result.Page.Tasks {
		if result.Page.Tasks[index].ID == taskID {
			summary = &result.Page.Tasks[index]
			break
		}
	}
	if summary == nil || summary.RuntimeBudget == nil {
		t.Fatalf("runtime budget missing from Organizer projection: %#v", summary)
	}
	budget := summary.RuntimeBudget
	if budget.State != "soft_paused" || budget.ReasonCode == nil || *budget.ReasonCode != string(runtimebudget.ReasonTokensSoft) ||
		budget.SoftThresholdBasisPoints != "8500" || budget.WorkerTurns != "1" ||
		len(budget.Dimensions) != 4 || len(budget.Counts) != 4 || budget.Dimensions[1].Consumed != "84" {
		t.Fatalf("runtime budget summary = %#v", budget)
	}
}

func TestPlanningReaderProjectsExactCorrectionReasonAndWakeAction(t *testing.T) {
	store := planningScaleStore()
	taskID := store.tasks["project-scale"][0].ID
	store.tasks["project-scale"][0].Attention = nil
	store.runs[taskID] = []domain.Run{{
		ID: "run-correction", TaskID: taskID, Number: 1,
		Execution: execution.State{NeedsYou: &execution.NeedsYou{
			Code: "correction_root_cause_repeated", WakeCondition: "human_review_or_new_root_cause_evidence",
		}},
	}}
	input := planningQueryInput()
	search := "Open task 00001"
	input.Search = &search
	result, err := NewPlanningReader(store).Query(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Page.Tasks) != 1 || len(result.Page.Tasks[0].NeedsYou) != 1 {
		t.Fatalf("correction attention = %#v", result.Page.Tasks)
	}
	reason := result.Page.Tasks[0].NeedsYou[0]
	if reason.Code != "correction_root_cause_repeated" || reason.WakeCondition == nil ||
		*reason.WakeCondition != "human_review_or_new_root_cause_evidence" || !reason.HumanActionRequired ||
		reason.Message != "The same correction root cause repeated without acceptance-coverage progress" {
		t.Fatalf("correction reason/action = %#v", reason)
	}
}

func TestPlanningReaderProjectsBoundedFeedbackAuditAndExactHumanGate(t *testing.T) {
	store := planningScaleStore()
	task := store.tasks["project-scale"][0]
	store.tasks["project-scale"][0].Attention = nil
	head, base := strings.Repeat("1", 40), strings.Repeat("0", 40)
	binding := feedbackdomain.SealBinding(feedbackdomain.Binding{ProjectID: task.ProjectID, WorkspaceID: task.WorkspaceIDs[0],
		TaskID: task.ID, TaskVersion: task.Version, RunID: "run-feedback", CandidateID: "candidate-feedback",
		CandidateSHA: head, BaseSHA: base, CandidateGeneration: 1, ManifestSHA256: strings.Repeat("a", 64)})
	item := feedbackdomain.Item{Source: feedbackdomain.SourcePaseoDirect, ExternalID: "message-1", RevisionID: "revision-1",
		Actor: feedbackdomain.Actor{Kind: feedbackdomain.ActorHuman, ID: "human-1", Login: "owner", Authenticated: true,
			Attestation: feedbackdomain.AttestationPaseoHuman}, Kind: feedbackdomain.KindComment, CandidateSHA: head, BaseSHA: base,
		ContextSHA256: strings.Repeat("b", 64), Body: "P2 feedback needs an explicit decision", Actionable: true,
		Severity: correction.SeverityP2, RequiresHumanDecision: true, CreatedAtMillis: 1, UpdatedAtMillis: 2}
	snapshot := feedbackdomain.SealSnapshot(feedbackdomain.Snapshot{ID: "snapshot-1", Source: feedbackdomain.SourcePaseoDirect,
		BindingSHA256: binding.BindingSHA256, PageCount: 1, ObservedAtMillis: 3,
		MaximumAgeMillis: feedbackdomain.MaximumObservationAge, Items: []feedbackdomain.Item{item}})
	state, _, ok := feedbackdomain.Reconcile(nil, binding, []feedbackdomain.Snapshot{snapshot}, 3)
	if !ok {
		t.Fatal("feedback fixture")
	}
	store.runs[task.ID] = []domain.Run{{ID: binding.RunID, TaskID: task.ID, Number: 1, CurrentCandidateID: binding.CandidateID,
		Execution: execution.State{Feedback: &state, NeedsYou: &execution.NeedsYou{Code: execution.NeedCode(state.NeedsYouCode),
			WakeCondition: state.WakeCondition}}}}
	store.candidates[binding.CandidateID] = domain.Candidate{ID: binding.CandidateID, RunID: binding.RunID}
	input := planningQueryInput()
	search := task.Title
	input.Search = &search
	result, err := NewPlanningReader(store).Query(context.Background(), input)
	if err != nil || len(result.Page.Tasks) != 1 {
		t.Fatalf("query = %#v, %v", result, err)
	}
	summary := result.Page.Tasks[0]
	if summary.DerivedState != "needs_you" || summary.Feedback == nil || summary.Feedback.Phase != "needs_you" ||
		summary.Feedback.CurrentActionable != "1" || summary.Feedback.AuditRecords != "1" || summary.Feedback.CurrentRevision != state.CurrentRevision ||
		len(summary.NeedsYou) != 1 || summary.NeedsYou[0].Code != state.NeedsYouCode || !summary.NeedsYou[0].HumanActionRequired {
		t.Fatalf("feedback projection = %#v", summary)
	}
}

func planningScaleStore() *planningFactStore {
	project := domain.Project{ID: "project-scale", Name: "Scale project", State: "active", Version: 3}
	workspaces := make([]domain.Workspace, 25)
	for index := range workspaces {
		workspaces[index] = domain.Workspace{
			ID: fmt.Sprintf("workspace-%02d", index), ProjectID: project.ID, Version: 2,
			Name: fmt.Sprintf("Workspace %02d", index+1), DefaultBaseBranch: "main",
			Policy: domain.WorkspacePolicy{LaunchPolicy: "automatic", DeliveryMode: "pull_request"},
		}
	}
	epics := make([]domain.Epic, 10)
	for index := range epics {
		epics[index] = domain.Epic{
			ID: fmt.Sprintf("epic-%02d", index), ProjectID: project.ID, Version: 1,
			Key: fmt.Sprintf("M2-%02d", index+1), Title: fmt.Sprintf("Milestone %02d", index+1),
			Labels: []string{"scale"},
		}
	}
	tasks := make([]domain.Task, 500)
	priorities := [...]domain.Priority{
		domain.PriorityUrgent, domain.PriorityHigh, domain.PriorityNormal, domain.PriorityLow,
	}
	for index := range tasks {
		epicID := epics[index%len(epics)].ID
		tasks[index] = domain.Task{
			ID: fmt.Sprintf("task-%05d", index), ProjectID: project.ID, Key: fmt.Sprintf("DIR-%05d", index+1),
			Title: fmt.Sprintf("Open task %05d", index+1), Objective: "Exercise the bounded planning query",
			AcceptanceCriteria: "The engine returns one stable page", WorkspaceIDs: []string{workspaces[index%len(workspaces)].ID},
			Parent:   &domain.PlanningNodeRef{Kind: domain.PlanningNodeEpic, ID: epicID},
			Priority: priorities[index%len(priorities)], Labels: []string{"scale", fmt.Sprintf("label-%d", index%3)},
			QueuedAtUnixMillis: int64(index + 1), Version: 1,
		}
	}
	tasks[0].Attention = &execution.NeedsYou{
		Code: "policy_override_required", WakeCondition: "human approval is recorded",
	}
	return &planningFactStore{
		projects: []domain.Project{project}, workspaces: map[string][]domain.Workspace{project.ID: workspaces},
		epics: map[string][]domain.Epic{project.ID: epics}, tasks: map[string][]domain.Task{project.ID: tasks},
		overrides: map[string][]domain.DependencyOverride{}, runs: map[string][]domain.Run{},
		candidates: map[string]domain.Candidate{}, cursor: 44,
	}
}

type optimizedPlanningFactStore struct {
	*planningFactStore
	projectListReads  int
	projectGraphReads int
}

func (store *optimizedPlanningFactStore) PlanningProjects(context.Context) ([]domain.Project, error) {
	store.projectListReads++
	return slices.Clone(store.projects), nil
}

func (store *optimizedPlanningFactStore) PlanningProject(_ context.Context, projectID string) (
	domain.Project,
	[]domain.Workspace,
	[]domain.Epic,
	[]domain.Task,
	[]domain.DependencyOverride,
	map[string]int64,
	error,
) {
	store.projectGraphReads++
	for _, project := range store.projects {
		if project.ID == projectID {
			workspaces := slices.Clone(store.workspaces[projectID])
			epics, tasks := slices.Clone(store.epics[projectID]), slices.Clone(store.tasks[projectID])
			overrides := slices.Clone(store.overrides[projectID])
			updates := make(map[string]int64, len(tasks))
			for _, task := range tasks {
				updates[task.ID] = task.QueuedAtUnixMillis
			}
			return project, workspaces, epics, tasks, overrides, updates, nil
		}
	}
	return domain.Project{}, nil, nil, nil, nil, nil, errors.New("Project not found")
}

func TestPlanningReaderUsesOneTypedProjectGraphRead(t *testing.T) {
	store := &optimizedPlanningFactStore{planningFactStore: planningScaleStore()}
	snapshot, err := NewPlanningReader(store).Query(context.Background(), planningQueryInput())
	if err != nil || snapshot.Page.TotalTasks != "500" || len(snapshot.Page.Tasks) != planningport.MaximumPageSize {
		t.Fatalf("optimized planning query = %#v, %v", snapshot.Page, err)
	}
	if store.projectListReads != 1 || store.projectGraphReads != 1 {
		t.Fatalf("optimized planning reads = list %d, graph %d", store.projectListReads, store.projectGraphReads)
	}
}

func TestPlanningReaderIntegratesScaleFiltersSortAndSnapshotPages(t *testing.T) {
	store := planningScaleStore()
	reader := NewPlanningReader(store)
	started := time.Now()
	first, err := reader.Query(context.Background(), planningQueryInput())
	if err != nil {
		t.Fatalf("first planning page: %v", err)
	}
	if first.SchemaVersion != 1 || first.ContractVersion != "director-planning/v1" ||
		first.Cursor != "44" || first.Page.TotalTasks != "500" || len(first.Page.Tasks) != 100 ||
		first.Page.NextCursor == nil || len(first.Page.Workspaces) != 25 || len(first.Page.Epics) != 10 {
		t.Fatalf("first planning page bounds = %#v", first)
	}
	if first.Page.Projects[0].TaskCounts.Open != "500" || first.Page.Projects[0].TaskCounts.Done != "0" ||
		first.Page.Projects[0].TaskCounts.NeedsYou != "1" {
		t.Fatalf("project counts = %#v", first.Page.Projects[0].TaskCounts)
	}
	if store.bulkRunReads != 1 || store.bulkCandidateReads != 1 || store.bulkTaskUpdateReads != 1 {
		t.Fatalf(
			"planning query used Run=%d Candidate=%d Task-update=%d bulk reads",
			store.bulkRunReads, store.bulkCandidateReads, store.bulkTaskUpdateReads,
		)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("integrated planning query exceeded conservative CI ceiling: %s", elapsed)
	}

	secondInput := planningQueryInput()
	secondInput.Cursor = first.Page.NextCursor
	second, err := reader.Query(context.Background(), secondInput)
	if err != nil || len(second.Page.Tasks) != 100 {
		t.Fatalf("second planning page = %d rows, %v", len(second.Page.Tasks), err)
	}
	seen := make(map[string]struct{}, 200)
	for _, task := range append(first.Page.Tasks, second.Page.Tasks...) {
		if _, duplicate := seen[task.ID]; duplicate {
			t.Fatalf("planning page boundary duplicated %s", task.ID)
		}
		seen[task.ID] = struct{}{}
	}

	filtered := planningQueryInput()
	projectID := "project-scale"
	search := "OPEN TASK"
	filtered.ProjectID = &projectID
	filtered.WorkspaceIDs = []string{"workspace-00"}
	filtered.EpicIDs = []string{"epic-00"}
	filtered.Priorities = []string{"normal"}
	filtered.Labels = []string{"scale", "label-2"}
	filtered.Search = &search
	filtered.Sort = string(projection.TaskSortKeyAsc)
	result, err := reader.Query(context.Background(), filtered)
	if err != nil || len(result.Page.Tasks) == 0 {
		t.Fatalf("combined planning filter = %#v, %v", result.Page, err)
	}
	for _, task := range result.Page.Tasks {
		if task.WorkspaceID != "workspace-00" || task.EpicID == nil || *task.EpicID != "epic-00" ||
			task.Priority != "normal" || len(task.Labels) != 2 {
			t.Fatalf("combined planning filter leaked %#v", task)
		}
	}
	recent := planningQueryInput()
	recent.Sort = string(projection.TaskSortUpdatedDesc)
	recentResult, err := reader.Query(context.Background(), recent)
	if err != nil || recentResult.Page.Tasks[0].ID != "task-00499" {
		t.Fatalf("updated-desc TaskStore order = %#v, %v", recentResult.Page.Tasks, err)
	}
	attention := planningQueryInput()
	attention.States = []string{"needs_you"}
	attention.Attention = []string{"policy_override_required"}
	attentionResult, err := reader.Query(context.Background(), attention)
	if err != nil || attentionResult.Page.TotalTasks != "1" || attentionResult.Page.Tasks[0].ID != "task-00000" {
		t.Fatalf("attention query = %#v, %v", attentionResult.Page, err)
	}

	store.cursor++
	if _, err := reader.Query(context.Background(), secondInput); !errors.Is(err, projection.ErrTaskQueryCursorSnapshot) {
		t.Fatalf("changed-snapshot cursor error = %v", err)
	}
}

func TestControlBoardExplanationsAndActionsAreExact(t *testing.T) {
	intent := execution.ControlIntent{
		RequestID: "request-board-control", Kind: execution.ControlPauseProject, ProjectID: "project-board",
		ActorKind: execution.ControlActorHuman, ActorID: "owner", ActorSessionID: "session",
		Source: "server", Authenticated: true, RequestedAtMillis: 1,
	}
	intent.ID = execution.ControlIntentID(intent)
	project := domain.Project{
		ID: "project-board", Name: "Board control", State: "paused", Version: 9,
		Control: execution.ProjectControl{
			SchemaVersion: execution.ProjectControlSchemaVersion, Generation: 1, Intent: intent,
			Phase: execution.ControlPaused, ResumeRequired: true,
			ExplanationCode: "project_paused_at_safe_boundary",
		},
	}
	summaries := projectSummaries(planningFacts{projects: []planningProjectFacts{{project: project}}})
	if len(summaries) != 1 || summaries[0].Control == nil ||
		summaries[0].Control.Message != "Project is paused at a safe boundary; active turns were allowed to finish" ||
		len(summaries[0].AllowedActions) != 2 || summaries[0].AllowedActions[0].Kind != "project.resume" ||
		summaries[0].AllowedActions[1].Kind != "project.emergency-stop.prepare" {
		t.Fatalf("paused Project summary = %#v", summaries)
	}
	run := domain.Run{Execution: execution.State{Control: execution.RunControl{
		SchemaVersion: execution.RunControlSchemaVersion, Phase: execution.ControlNeedsYou,
		ExplanationCode: "control_recovery_ambiguous",
	}}}
	explanation := runControlExplanation(&run)
	if explanation == nil || explanation.Message != "Control recovery is ambiguous; relaunch and destructive cleanup remain blocked" ||
		explanation.WakeCondition == nil || *explanation.WakeCondition != "fresh_control_reconciliation_or_human_recovery" ||
		!explanation.HumanActionRequired {
		t.Fatalf("Needs-you control explanation = %#v", explanation)
	}
	cancelIntent := intent
	cancelIntent.Kind = execution.ControlCancelTask
	cancelIntent.TaskID, cancelIntent.RunID = "task-board", "run-board"
	cancelIntent.ID = execution.ControlIntentID(cancelIntent)
	run = domain.Run{ID: "run-board", TaskID: "task-board", Execution: execution.State{
		Terminal: true,
		Control: execution.RunControl{
			SchemaVersion: execution.RunControlSchemaVersion, Intent: cancelIntent,
			ProjectGeneration: 1, Phase: execution.ControlCancelled, RelaunchBlocked: true,
			ExplanationCode: "task_cancelled_recovery_preserved",
		},
	}}
	task := domain.Task{ID: "task-board", ProjectID: project.ID, Version: 3}
	projected := projection.DeriveTaskProjection(taskStateFacts(project, task, false, &run, nil))
	if projected.State != projection.StateQueued || !slices.Contains(projected.Blockers, projection.BlockerPolicyWait) {
		t.Fatalf("cancelled Task relaunch projection = %#v", projected)
	}
}

func TestCleanupReasonsArePathFreeForBoardAndOrganizer(t *testing.T) {
	for code, want := range map[string]string{
		"cleanup_response_unknown":    "recoverable material",
		"cleanup_snapshot_unverified": "recovery snapshot",
		"cleanup_disk_pressure":       "unintegrated work",
		"cleanup_owner_mismatch":      "ownership or repository identity",
	} {
		message := explanationMessage(code)
		if !strings.Contains(message, want) || strings.Contains(message, "/") || strings.Contains(message, "token") {
			t.Fatalf("message %q for %s", message, code)
		}
	}
}
