// SPDX-License-Identifier: Apache-2.0

package home

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/execution"
	planningport "github.com/mcuadros/director-engine/ports/planning"
	"github.com/mcuadros/director-engine/projection"
)

type homeStore struct {
	projects           []domain.Project
	workspaces         map[string][]domain.Workspace
	tasks              map[string][]domain.Task
	runs               map[string][]domain.Run
	candidates         map[string]domain.Candidate
	cursor             uint64
	bulkRunReads       int
	bulkCandidateReads int
}

func (store *homeStore) Projects(context.Context) ([]domain.Project, error) {
	return append([]domain.Project(nil), store.projects...), nil
}
func (store *homeStore) Workspaces(_ context.Context, projectID string) ([]domain.Workspace, error) {
	return append([]domain.Workspace(nil), store.workspaces[projectID]...), nil
}
func (store *homeStore) Epics(context.Context, string) ([]domain.Epic, error) {
	return []domain.Epic{}, nil
}
func (store *homeStore) Tasks(_ context.Context, projectID string) ([]domain.Task, error) {
	return append([]domain.Task(nil), store.tasks[projectID]...), nil
}
func (store *homeStore) DependencyOverrides(context.Context, string) ([]domain.DependencyOverride, error) {
	return []domain.DependencyOverride{}, nil
}
func (store *homeStore) Runs(_ context.Context, taskID string) ([]domain.Run, error) {
	return append([]domain.Run(nil), store.runs[taskID]...), nil
}
func (store *homeStore) Candidate(_ context.Context, candidateID string) (domain.Candidate, error) {
	value, ok := store.candidates[candidateID]
	if !ok {
		return domain.Candidate{}, errors.New("candidate unavailable")
	}
	return value, nil
}
func (store *homeStore) LatestEventSequence(context.Context) (uint64, error) {
	return store.cursor, nil
}

func (store *homeStore) PlanningRuns(_ context.Context, projectID string) ([]domain.Run, error) {
	store.bulkRunReads++
	var result []domain.Run
	for _, task := range store.tasks[projectID] {
		result = append(result, store.runs[task.ID]...)
	}
	return result, nil
}

func (store *homeStore) PlanningCandidates(_ context.Context, projectID string) ([]domain.Candidate, error) {
	store.bulkCandidateReads++
	runIDs := map[string]struct{}{}
	for _, task := range store.tasks[projectID] {
		for _, run := range store.runs[task.ID] {
			runIDs[run.ID] = struct{}{}
		}
	}
	var result []domain.Candidate
	for _, candidate := range store.candidates {
		if _, ok := runIDs[candidate.RunID]; ok {
			result = append(result, candidate)
		}
	}
	return result, nil
}

type homeProjectionSource struct{ store *homeStore }

func (source *homeProjectionSource) TaskProjectionInputs(ctx context.Context) ([]projection.TaskProjectionInput, error) {
	var result []projection.TaskProjectionInput
	for _, project := range source.store.projects {
		runs, err := source.store.PlanningRuns(ctx, project.ID)
		if err != nil {
			return nil, err
		}
		if _, err := source.store.PlanningCandidates(ctx, project.ID); err != nil {
			return nil, err
		}
		runsByTask := map[string]domain.Run{}
		for _, run := range runs {
			current, exists := runsByTask[run.TaskID]
			if !exists || run.Number > current.Number {
				runsByTask[run.TaskID] = run
			}
		}
		for _, task := range source.store.tasks[project.ID] {
			facts := projection.TaskStateFacts{
				TaskID: task.ID, TaskVersion: task.Version,
				Eligibility: projection.EligibilityFact{Status: projection.FactCurrent, TaskVersion: task.Version, Decision: projection.EligibilityEligible},
				Run:         projection.RunFact{Status: projection.FactMissing},
				Candidate:   projection.CandidateFact{Status: projection.FactMissing},
				Validation:  projection.ValidationFact{Status: projection.FactMissing},
				Review:      projection.ReviewFact{Status: projection.FactMissing},
				Feedback:    projection.FeedbackFact{Status: projection.FactMissing},
				Delivery:    projection.DeliveryFact{Status: projection.FactMissing},
				Cleanup:     projection.CleanupFact{Status: projection.FactMissing},
				HumanInput:  projection.HumanInputFact{Status: projection.FactCurrent, TaskVersion: task.Version, State: projection.HumanInputNone},
				Terminal:    projection.TerminalFact{Status: projection.FactCurrent, TaskVersion: task.Version, State: projection.TerminalOpen},
			}
			if task.Complete {
				facts.Terminal.State = projection.TerminalDone
			}
			if task.Attention != nil {
				facts.HumanInput.State = projection.HumanInputPending
				facts.HumanInput.Code = projection.AttentionCredentialRequired
				facts.HumanInput.ReasonCode = string(task.Attention.Code)
				facts.HumanInput.WakeCondition = task.Attention.WakeCondition
			}
			if run, exists := runsByTask[task.ID]; exists {
				facts.Run = projection.RunFact{Status: projection.FactCurrent, TaskVersion: task.Version, ID: run.ID, Active: !run.Execution.Terminal}
			}
			result = append(result, projection.TaskProjectionInput{TaskID: task.ID, ProjectID: project.ID,
				WorkspaceID: task.WorkspaceIDs[0], Key: task.Key, Title: task.Title, Priority: task.Priority,
				QueuedAtUnixMillis: task.QueuedAtUnixMillis, UpdatedAtUnixMillis: task.QueuedAtUnixMillis, Facts: facts})
		}
	}
	return result, nil
}

type fixedProjectionSource struct {
	inputs []projection.TaskProjectionInput
}

func (source fixedProjectionSource) TaskProjectionInputs(context.Context) ([]projection.TaskProjectionInput, error) {
	return append([]projection.TaskProjectionInput(nil), source.inputs...), nil
}

func homeProject(index int, now int64) (domain.Project, domain.Workspace) {
	projectID := fmt.Sprintf("project-%02d", index)
	workspaceID := fmt.Sprintf("workspace-%02d", index)
	return domain.Project{
		ID: projectID, Name: fmt.Sprintf("Project %02d", index), State: "active", Version: uint64(index + 1),
		Organizer: &domain.Organizer{ID: domain.OrganizerID(projectID), Mode: domain.OrganizerModeCreate,
			Phase: domain.OrganizerPhaseActive, OrganizerRevision: strings.Repeat("a", 40), ConfigurationSHA256: strings.Repeat("b", 64)},
		LastLeaseEpoch: 1, Lease: &domain.ProjectLease{HolderInstance: "engine-owner", HolderProcessIdentity: "process-owner",
			Epoch: 1, AcquiredAtMillis: 1, RenewedAtMillis: now - 100, ExpiresAtMillis: now + 10_000, DispatchAllowed: true},
	}, domain.Workspace{ID: workspaceID, ProjectID: projectID, Key: fmt.Sprintf("repo-%02d", index), Name: fmt.Sprintf("Repository %02d", index), Version: 1}
}

func homeFixture(now int64, count int) (*homeStore, HostObservation) {
	store := &homeStore{workspaces: map[string][]domain.Workspace{}, tasks: map[string][]domain.Task{}, runs: map[string][]domain.Run{}, candidates: map[string]domain.Candidate{}, cursor: 41}
	observation := HostObservation{HostID: "host-a", Label: "Primary host", InstanceID: "engine-instance-a", State: "current",
		ObservedAtMillis: now, MaximumAgeMillis: HostObservationMaximumAgeMillis, Projects: map[string]ProjectObservation{}}
	for index := 0; index < count; index++ {
		project, workspace := homeProject(index, now)
		store.projects = append(store.projects, project)
		store.workspaces[project.ID] = []domain.Workspace{workspace}
		store.tasks[project.ID] = []domain.Task{
			{ID: fmt.Sprintf("task-%02d-build", index), ProjectID: project.ID, Key: "BUILD", Title: "Active work", Objective: "Produce one active Run", AcceptanceCriteria: "The Run is visible", WorkspaceIDs: []string{workspace.ID}, Priority: domain.PriorityNormal, QueuedAtUnixMillis: int64(index*2 + 1), Version: 1},
			{ID: fmt.Sprintf("task-%02d-need", index), ProjectID: project.ID, Key: "NEED", Title: "Needs human", Objective: "Request one bounded decision", AcceptanceCriteria: "The reason is aggregated", WorkspaceIDs: []string{workspace.ID}, Priority: domain.PriorityHigh, QueuedAtUnixMillis: int64(index*2 + 2), Version: 1},
		}
		store.runs[store.tasks[project.ID][0].ID] = []domain.Run{{ID: fmt.Sprintf("run-%02d", index), TaskID: store.tasks[project.ID][0].ID, Number: 1, Execution: execution.State{}}}
		observation.Projects[project.ID] = ProjectObservation{
			GitSync: SyncCurrent, TaskStoreSync: SyncCurrent, SyncObservedAtMillis: now - 100, SyncMaximumAgeMillis: 5_000,
			BoardWorkspaceID: "native-board-" + project.ID, OrganizerWorkspaceID: "native-organizer-" + project.ID,
			Workspaces: map[string]WorkspaceObservation{workspace.ID: {Health: "healthy", PaseoWorkspaceID: "native-" + workspace.ID}},
			Operations: OperationAvailability{Doctor: true, Control: true},
		}
	}
	if count > 0 {
		store.tasks[store.projects[0].ID][1].Attention = &execution.NeedsYou{Code: "credential_required", WakeCondition: "human_updates_host_credential"}
	}
	return store, observation
}

func TestHomeReaderBindsHostProjectsHealthActionsAndRedaction(t *testing.T) {
	now := int64(10_000)
	store, observation := homeFixture(now, 3)
	reader := NewReader(store, &homeProjectionSource{store}, &staticObservationSource{value: observation}, func() int64 { return now })
	result, err := reader.Query(context.Background(), planningport.HomeQueryInput{HostID: "host-a", PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if result.Page.Host.ID != "host-a" || result.Page.Host.InstanceID != "engine-instance-a" || result.Page.TotalProjects != "3" || len(result.Page.Projects) != 2 || result.Page.NextCursor == nil {
		t.Fatalf("Home identity/page = %#v", result.Page)
	}
	if result.Page.Totals.Projects != "3" || result.Page.Totals.NeedsYou != "1" || result.Page.Totals.ActiveWork != "3" {
		t.Fatalf("Home totals = %#v", result.Page.Totals)
	}
	first := result.Page.Projects[0]
	if first.Health != "needs_you" || first.Tasks.NeedsYou != "1" || first.ActiveWork.Building != "1" || len(first.NeedsYouReasons) != 1 || first.NeedsYouReasons[0].Code != "credential_required" {
		t.Fatalf("Home Project aggregation = %#v", first)
	}
	var boardAction, pauseAction *planningport.HomeAction
	for index := range first.Actions {
		switch first.Actions[index].Kind {
		case "open_board":
			boardAction = &first.Actions[index]
		case "pause":
			pauseAction = &first.Actions[index]
		}
	}
	if boardAction == nil || !boardAction.Enabled || boardAction.PaseoWorkspaceID == nil || *boardAction.PaseoWorkspaceID != "native-board-project-00" ||
		pauseAction == nil || !pauseAction.Enabled || pauseAction.Command == nil || pauseAction.Command.Kind != "project.pause" || pauseAction.Command.TargetID == nil || *pauseAction.Command.TargetID != first.ID {
		t.Fatalf("Home actions = %#v", first.Actions)
	}
	encoded, _ := json.Marshal(result)
	for _, forbidden := range []string{"engine-owner", "process-owner", "/home/", "/tmp/", "human_updates_host_credential", "Active work", "Needs human"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("Home projection leaked private or non-summary fact %q: %s", forbidden, encoded)
		}
	}

	second, err := reader.Query(context.Background(), planningport.HomeQueryInput{HostID: "host-a", Cursor: result.Page.NextCursor, PageSize: 2})
	if err != nil || len(second.Page.Projects) != 1 || second.Page.Projects[0].ID != "project-02" || second.Page.NextCursor != nil || second.Page.Totals != result.Page.Totals {
		t.Fatalf("second Home page = %#v, %v", second, err)
	}
}

type staticObservationSource struct{ value HostObservation }

func (source *staticObservationSource) Observe(_ context.Context, requested string, _ []string) (HostObservation, error) {
	if requested != source.value.HostID {
		return HostObservation{}, ErrHostMismatch
	}
	return source.value, nil
}

func TestHomeReaderNeverFallsThroughHostsOrRestartedPages(t *testing.T) {
	now := int64(10_000)
	store, observation := homeFixture(now, 3)
	source := &staticObservationSource{value: observation}
	reader := NewReader(store, &homeProjectionSource{store}, source, func() int64 { return now })
	if _, err := reader.Query(context.Background(), planningport.HomeQueryInput{HostID: "host-b", PageSize: 2}); !errors.Is(err, ErrHostMismatch) {
		t.Fatalf("different host error = %v", err)
	}
	first, err := reader.Query(context.Background(), planningport.HomeQueryInput{HostID: "host-a", PageSize: 2})
	if err != nil || first.Page.NextCursor == nil {
		t.Fatal(first, err)
	}
	source.value.HostID, source.value.Label, source.value.InstanceID = "host-b", "Other host", "engine-instance-b"
	if _, err := reader.Query(context.Background(), planningport.HomeQueryInput{HostID: "host-a", Cursor: first.Page.NextCursor, PageSize: 2}); !errors.Is(err, ErrHostMismatch) {
		t.Fatalf("host replacement error = %v", err)
	}
	source.value = observation
	source.value.InstanceID = "engine-instance-restarted"
	if _, err := reader.Query(context.Background(), planningport.HomeQueryInput{HostID: "host-a", Cursor: first.Page.NextCursor, PageSize: 2}); !errors.Is(err, ErrCursorInvalid) {
		t.Fatalf("restart cursor error = %v", err)
	}
	source.value = observation
	store.cursor++
	if _, err := reader.Query(context.Background(), planningport.HomeQueryInput{HostID: "host-a", Cursor: first.Page.NextCursor, PageSize: 2}); !errors.Is(err, ErrCursorInvalid) {
		t.Fatalf("changed snapshot cursor error = %v", err)
	}
}

func TestHomeReaderRejectsMissingDuplicateAndCrossProjectTaskFacts(t *testing.T) {
	now := int64(10_000)
	store, observation := homeFixture(now, 2)
	inputs, err := (&homeProjectionSource{store}).TaskProjectionInputs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for name, changed := range map[string][]projection.TaskProjectionInput{
		"missing":      inputs[:len(inputs)-1],
		"duplicate":    append(append([]projection.TaskProjectionInput{}, inputs...), inputs[0]),
		"foreign host": append(append([]projection.TaskProjectionInput{}, inputs...), projection.TaskProjectionInput{ProjectID: "project-foreign", TaskID: "task-shared"}),
		"wrong version": func() []projection.TaskProjectionInput {
			values := append([]projection.TaskProjectionInput{}, inputs...)
			values[0].Facts.TaskVersion++
			return values
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			reader := NewReader(store, fixedProjectionSource{changed}, &staticObservationSource{value: observation}, func() int64 { return now })
			if _, err := reader.Query(context.Background(), planningport.HomeQueryInput{HostID: "host-a", PageSize: 2}); !errors.Is(err, ErrHostFacts) {
				t.Fatalf("contaminated Task facts error = %v", err)
			}
		})
	}
}

func TestHomeReaderProjectsStaleDisconnectedAndPartialSyncWithoutHealthyFallback(t *testing.T) {
	now := int64(60_000)
	store, observation := homeFixture(now, 1)
	observation.ObservedAtMillis = now - HostObservationMaximumAgeMillis - 1
	reader := NewReader(store, &homeProjectionSource{store}, &staticObservationSource{value: observation}, func() int64 { return now })
	stale, err := reader.Query(context.Background(), planningport.HomeQueryInput{HostID: "host-a", PageSize: 1})
	if err != nil || stale.Page.Host.State != "stale" || stale.Page.Projects[0].Health != "stale" {
		t.Fatalf("stale Home = %#v, %v", stale, err)
	}
	for _, action := range append(stale.Page.SurfaceActions, stale.Page.Projects[0].Actions...) {
		if action.Enabled {
			t.Fatalf("stale action remained enabled: %#v", action)
		}
	}

	_, disconnectedObservation := homeFixture(now, 1)
	disconnectedObservation.State = "disconnected"
	disconnected, err := NewReader(store, &homeProjectionSource{store}, &staticObservationSource{value: disconnectedObservation}, func() int64 { return now }).Query(context.Background(), planningport.HomeQueryInput{HostID: "host-a", PageSize: 1})
	if err != nil || disconnected.Page.Projects[0].Health != "disconnected" {
		t.Fatalf("disconnected Home = %#v, %v", disconnected, err)
	}

	_, partialObservation := homeFixture(now, 1)
	project := partialObservation.Projects["project-00"]
	project.GitSync, project.TaskStoreSync = SyncCurrent, SyncFailed
	partialObservation.Projects["project-00"] = project
	partial, err := NewReader(store, &homeProjectionSource{store}, &staticObservationSource{value: partialObservation}, func() int64 { return now }).Query(context.Background(), planningport.HomeQueryInput{HostID: "host-a", PageSize: 1})
	if err != nil || partial.Page.Projects[0].Sync.State != "partial" || partial.Page.Projects[0].Health != "needs_you" {
		t.Fatalf("partial-sync Home = %#v, %v", partial, err)
	}
	store.tasks["project-00"][1].Attention = nil
	partial, err = NewReader(store, &homeProjectionSource{store}, &staticObservationSource{value: partialObservation}, func() int64 { return now }).Query(context.Background(), planningport.HomeQueryInput{HostID: "host-a", PageSize: 1})
	if err != nil || partial.Page.Projects[0].Health != "degraded" {
		t.Fatalf("partial-sync degraded Home = %#v, %v", partial, err)
	}
}

func TestStaticSourceNeverInventsOperationalHealthOrNavigation(t *testing.T) {
	now := int64(10_000)
	store, _ := homeFixture(now, 1)
	reader := NewReader(store, &homeProjectionSource{store}, NewStaticSource("host-a", "Primary host", "engine-static", func() int64 { return now }), func() int64 { return now })
	result, err := reader.Query(context.Background(), planningport.HomeQueryInput{HostID: "host-a", PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	project := result.Page.Projects[0]
	if project.Health != "needs_you" || project.Sync.State != "unavailable" || project.Workspaces[0].Health != "unknown" || project.Workspaces[0].PaseoWorkspaceID != nil {
		t.Fatalf("static source fabricated health/navigation = %#v", project)
	}
	for _, action := range project.Actions {
		if (action.Kind == "open_board" || action.Kind == "open_organizer" || action.Kind == "sync_now" || action.Kind == "reconcile_now" || action.Kind == "repair") && action.Enabled {
			t.Fatalf("static source fabricated action availability: %#v", action)
		}
	}
}

func TestHomeReaderBoundsProjectPagesAndUsesBulkRunCandidateFactsAtScale(t *testing.T) {
	now := int64(10_000)
	store, observation := homeFixture(now, planningport.MaximumProjects)
	project := store.projects[0]
	workspaces := make([]domain.Workspace, 25)
	for index := range workspaces {
		workspaces[index] = domain.Workspace{ID: fmt.Sprintf("scale-workspace-%02d", index), ProjectID: project.ID,
			Key: fmt.Sprintf("scale-repository-%02d", index), Name: fmt.Sprintf("Scale Repository %02d", index), Version: 1}
	}
	store.workspaces[project.ID] = workspaces
	projectObservation := observation.Projects[project.ID]
	projectObservation.Workspaces = map[string]WorkspaceObservation{}
	for _, workspace := range workspaces {
		projectObservation.Workspaces[workspace.ID] = WorkspaceObservation{Health: "healthy"}
	}
	store.tasks[project.ID] = make([]domain.Task, 10_000)
	for index := range store.tasks[project.ID] {
		store.tasks[project.ID][index] = domain.Task{ID: fmt.Sprintf("scale-task-%05d", index), ProjectID: project.ID,
			Key: fmt.Sprintf("SCALE-%05d", index), Title: fmt.Sprintf("Scale Task %05d", index), Objective: "Exercise Home aggregation",
			AcceptanceCriteria: "The Task is counted exactly once", WorkspaceIDs: []string{workspaces[index%len(workspaces)].ID},
			Complete: index >= 500, Priority: domain.PriorityNormal, QueuedAtUnixMillis: int64(index + 1), Version: 1}
	}
	observation.Projects[project.ID] = projectObservation
	reader := NewReader(store, &homeProjectionSource{store}, &staticObservationSource{value: observation}, func() int64 { return now })
	started := time.Now()
	result, err := reader.Query(context.Background(), planningport.HomeQueryInput{HostID: "host-a", PageSize: planningport.MaximumHomePageSize})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Page.Projects) != planningport.MaximumHomePageSize || result.Page.NextCursor == nil || result.Page.TotalProjects != "100" ||
		result.Page.Projects[0].Tasks.Open != "10000" || result.Page.Projects[0].Tasks.Done != "0" {
		t.Fatalf("scaled Home page: projects=%d next=%v total=%s tasks=%#v", len(result.Page.Projects), result.Page.NextCursor != nil,
			result.Page.TotalProjects, result.Page.Projects[0].Tasks)
	}
	if store.bulkRunReads != planningport.MaximumProjects || store.bulkCandidateReads != planningport.MaximumProjects {
		t.Fatalf("Home used non-bulk Run/Candidate reads: runs=%d candidates=%d", store.bulkRunReads, store.bulkCandidateReads)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("scaled Home projection exceeded conservative CI ceiling: %s", elapsed)
	}
}
