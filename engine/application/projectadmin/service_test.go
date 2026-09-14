// SPDX-License-Identifier: Apache-2.0

package projectadmin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/mcuadros/director-engine/domain"
	contract "github.com/mcuadros/director-engine/domain/projectadmin"
	executionport "github.com/mcuadros/director-engine/ports/execution"
	planningport "github.com/mcuadros/director-engine/ports/planning"
	storeport "github.com/mcuadros/director-engine/ports/taskstore"
)

type memoryStore struct {
	projects   map[string]domain.Project
	workspaces map[string]domain.Workspace
	epics      map[string]domain.Epic
	tasks      map[string]domain.Task
	runs       map[string]domain.Run
	candidates map[string][]domain.Candidate
	commands   map[string]domain.Command
}

func newMemoryStore() *memoryStore {
	return &memoryStore{projects: map[string]domain.Project{}, workspaces: map[string]domain.Workspace{}, epics: map[string]domain.Epic{},
		tasks: map[string]domain.Task{}, runs: map[string]domain.Run{}, candidates: map[string][]domain.Candidate{}, commands: map[string]domain.Command{}}
}

func (store *memoryStore) Project(_ context.Context, id string) (domain.Project, error) {
	value, ok := store.projects[id]
	if !ok {
		return domain.Project{}, storeport.ErrNotFound
	}
	return value, nil
}
func (store *memoryStore) Projects(context.Context) ([]domain.Project, error) {
	result := make([]domain.Project, 0, len(store.projects))
	for _, value := range store.projects {
		result = append(result, value)
	}
	return result, nil
}
func (store *memoryStore) Workspace(_ context.Context, id string) (domain.Workspace, error) {
	value, ok := store.workspaces[id]
	if !ok {
		return domain.Workspace{}, storeport.ErrNotFound
	}
	return value, nil
}
func (store *memoryStore) Workspaces(_ context.Context, projectID string) ([]domain.Workspace, error) {
	result := []domain.Workspace{}
	for _, value := range store.workspaces {
		if value.ProjectID == projectID {
			result = append(result, value)
		}
	}
	return result, nil
}
func (store *memoryStore) Epic(_ context.Context, id string) (domain.Epic, error) {
	value, ok := store.epics[id]
	if !ok {
		return domain.Epic{}, storeport.ErrNotFound
	}
	return value, nil
}
func (store *memoryStore) Epics(_ context.Context, projectID string) ([]domain.Epic, error) {
	result := []domain.Epic{}
	for _, value := range store.epics {
		if value.ProjectID == projectID {
			result = append(result, value)
		}
	}
	return result, nil
}
func (store *memoryStore) Task(_ context.Context, id string) (domain.Task, error) {
	value, ok := store.tasks[id]
	if !ok {
		return domain.Task{}, storeport.ErrNotFound
	}
	return value, nil
}
func (store *memoryStore) Tasks(_ context.Context, projectID string) ([]domain.Task, error) {
	result := []domain.Task{}
	for _, value := range store.tasks {
		if value.ProjectID == projectID {
			result = append(result, value)
		}
	}
	return result, nil
}
func (store *memoryStore) Run(_ context.Context, id string) (domain.Run, error) {
	value, ok := store.runs[id]
	if !ok {
		return domain.Run{}, storeport.ErrNotFound
	}
	return value, nil
}
func (store *memoryStore) Runs(_ context.Context, taskID string) ([]domain.Run, error) {
	result := []domain.Run{}
	for _, value := range store.runs {
		if value.TaskID == taskID {
			result = append(result, value)
		}
	}
	return result, nil
}
func (store *memoryStore) Candidates(_ context.Context, runID string) ([]domain.Candidate, error) {
	return store.candidates[runID], nil
}
func (store *memoryStore) Command(_ context.Context, key string) (domain.Command, error) {
	value, ok := store.commands[key]
	if !ok {
		return domain.Command{}, storeport.ErrNotFound
	}
	return value, nil
}
func (store *memoryStore) LatestEventSequence(context.Context) (uint64, error) {
	return uint64(len(store.commands)), nil
}

func storedCommand(request domain.CommandRequest, outcome domain.CommandOutcome, observed uint64, eventID string) domain.Command {
	digest := sha256.Sum256(request.Payload)
	return domain.Command{IdempotencyKey: request.IdempotencyKey, Type: request.Type, AggregateID: request.AggregateID,
		ExpectedVersion: request.ExpectedVersion, Payload: request.Payload, PayloadHash: hex.EncodeToString(digest[:]),
		Outcome: outcome, ObservedVersion: observed, EventID: eventID}
}

func (store *memoryStore) UpdateProject(_ context.Context, request domain.CommandRequest, project domain.Project, event domain.Event) (domain.CommandResult, error) {
	if existing, ok := store.commands[request.IdempotencyKey]; ok {
		return domain.CommandResult{Outcome: existing.Outcome, ObservedVersion: existing.ObservedVersion, EventID: existing.EventID, Replay: true}, nil
	}
	current, ok := store.projects[request.AggregateID]
	if !ok {
		return domain.CommandResult{}, storeport.ErrNotFound
	}
	if current.Version != request.ExpectedVersion {
		store.commands[request.IdempotencyKey] = storedCommand(request, domain.CommandRejectedVersionConflict, current.Version, "")
		return domain.CommandResult{Outcome: domain.CommandRejectedVersionConflict, ObservedVersion: current.Version}, nil
	}
	store.projects[project.ID] = project
	store.commands[request.IdempotencyKey] = storedCommand(request, domain.CommandApplied, project.Version, event.ID)
	return domain.CommandResult{Outcome: domain.CommandApplied, ObservedVersion: project.Version, EventID: event.ID}, nil
}

func (store *memoryStore) CreateEpic(_ context.Context, request domain.CommandRequest, epic domain.Epic, event domain.Event) (domain.CommandResult, error) {
	if _, exists := store.epics[epic.ID]; exists {
		return domain.CommandResult{}, storeport.ErrAlreadyExists
	}
	store.epics[epic.ID] = epic
	store.commands[request.IdempotencyKey] = storedCommand(request, domain.CommandApplied, epic.Version, event.ID)
	return domain.CommandResult{Outcome: domain.CommandApplied, ObservedVersion: epic.Version, EventID: event.ID}, nil
}
func (store *memoryStore) UpdateEpic(_ context.Context, request domain.CommandRequest, epic domain.Epic, event domain.Event) (domain.CommandResult, error) {
	store.epics[epic.ID] = epic
	store.commands[request.IdempotencyKey] = storedCommand(request, domain.CommandApplied, epic.Version, event.ID)
	return domain.CommandResult{Outcome: domain.CommandApplied, ObservedVersion: epic.Version, EventID: event.ID}, nil
}
func (store *memoryStore) CreateTask(_ context.Context, request domain.CommandRequest, task domain.Task, event domain.Event) (domain.CommandResult, error) {
	if _, exists := store.tasks[task.ID]; exists {
		return domain.CommandResult{}, storeport.ErrAlreadyExists
	}
	store.tasks[task.ID] = task
	store.commands[request.IdempotencyKey] = storedCommand(request, domain.CommandApplied, task.Version, event.ID)
	return domain.CommandResult{Outcome: domain.CommandApplied, ObservedVersion: task.Version, EventID: event.ID}, nil
}
func (store *memoryStore) UpdateTask(_ context.Context, request domain.CommandRequest, task domain.Task, event domain.Event) (domain.CommandResult, error) {
	store.tasks[task.ID] = task
	store.commands[request.IdempotencyKey] = storedCommand(request, domain.CommandApplied, task.Version, event.ID)
	return domain.CommandResult{Outcome: domain.CommandApplied, ObservedVersion: task.Version, EventID: event.ID}, nil
}
func (*memoryStore) GrantDependencyOverride(context.Context, domain.CommandRequest, domain.HumanDependencyOverrideGrant) (domain.CommandResult, error) {
	return domain.CommandResult{}, errors.New("human-only dependency override reached Project administration")
}

type currentPlanningReader struct{ store *memoryStore }

func (reader currentPlanningReader) Query(_ context.Context, input planningport.QueryInput) (planningport.Snapshot, error) {
	if input.ProjectID == nil {
		return planningport.Snapshot{}, errors.New("Project filter absent")
	}
	project, ok := reader.store.projects[*input.ProjectID]
	if !ok {
		return planningport.Snapshot{}, storeport.ErrNotFound
	}
	target := project.ID
	actions := []planningport.AllowedAction{{Kind: "task.create", TargetID: &target, ExpectedVersion: strconv.FormatUint(project.Version, 10)}}
	return planningport.Snapshot{SchemaVersion: 1, ContractVersion: "director-planning/v1", Cursor: "1", Page: planningport.Page{
		SelectedProjectID: input.ProjectID, Projects: []planningport.ProjectSummary{{ID: project.ID, Version: strconv.FormatUint(project.Version, 10),
			Name: project.Name, State: project.State, AllowedActions: actions}}, Workspaces: []planningport.WorkspaceSummary{},
		Epics: []planningport.EpicSummary{}, Tasks: []planningport.TaskSummary{}, NextCursor: nil}}, nil
}

type refusalController struct{}

func (refusalController) RequestProjectControl(context.Context, executionport.ControlCommand) (executionport.ControlResult, error) {
	return executionport.ControlResult{}, errors.New("not exercised")
}

type capturingPlanner struct {
	inputs []planningport.MutationInput
	actors []planningport.Actor
	store  *memoryStore
}

func (planner *capturingPlanner) Mutate(_ context.Context, input planningport.MutationInput, actor planningport.Actor) (planningport.Result, error) {
	planner.inputs, planner.actors = append(planner.inputs, input), append(planner.actors, actor)
	version, _ := planningport.ParseExpectedVersion(input.ExpectedVersion)
	version++
	if planner.store != nil && input.Intent.Type == "task.create" {
		version = 0
		planner.store.tasks["created-"+input.Intent.Key] = domain.Task{ID: "created-" + input.Intent.Key,
			ProjectID: input.Intent.ProjectID, Key: input.Intent.Key, Title: input.Intent.Title,
			Objective: input.Intent.Objective, AcceptanceCriteria: strings.Join(input.Intent.AcceptanceCriteria, "\n"),
			WorkspaceIDs: []string{input.Intent.WorkspaceID}, Priority: domain.Priority(*input.Intent.Priority), Labels: input.Intent.Labels}
	}
	return planningport.Result{Status: "accepted", Message: "admitted", UpdatedVersion: &version}, nil
}

type allCommandsReader struct{ store *memoryStore }

func (reader allCommandsReader) Query(_ context.Context, input planningport.QueryInput) (planningport.Snapshot, error) {
	if input.ProjectID == nil {
		return planningport.Snapshot{}, errors.New("Project filter absent")
	}
	project := reader.store.projects[*input.ProjectID]
	projectID := project.ID
	projectVersion := strconv.FormatUint(project.Version, 10)
	projectActions := []planningport.AllowedAction{}
	for _, kind := range []string{"project.update", "epic.create", "task.create", "project.pause", "project.resume"} {
		projectActions = append(projectActions, planningport.AllowedAction{Kind: kind, TargetID: &projectID, ExpectedVersion: projectVersion})
	}
	epics := []planningport.EpicSummary{}
	for _, epic := range reader.store.epics {
		if epic.ProjectID != project.ID {
			continue
		}
		target := epic.ID
		epics = append(epics, planningport.EpicSummary{ID: epic.ID, ProjectID: project.ID, Version: strconv.FormatUint(epic.Version, 10),
			AllowedActions: []planningport.AllowedAction{{Kind: "epic.update", TargetID: &target, ExpectedVersion: strconv.FormatUint(epic.Version, 10)}}})
	}
	tasks := []planningport.TaskSummary{}
	for _, task := range reader.store.tasks {
		if task.ProjectID != project.ID {
			continue
		}
		target := task.ID
		actions := []planningport.AllowedAction{}
		for _, kind := range []string{"task.update", "dependency.add", "task.launch-now"} {
			actions = append(actions, planningport.AllowedAction{Kind: kind, TargetID: &target, ExpectedVersion: strconv.FormatUint(task.Version, 10)})
		}
		for _, run := range reader.store.runs {
			if run.TaskID == task.ID {
				runID := run.ID
				actions = append(actions, planningport.AllowedAction{Kind: "task.cancel", TargetID: &runID, ExpectedVersion: strconv.FormatUint(run.Version, 10)})
			}
		}
		tasks = append(tasks, planningport.TaskSummary{ID: task.ID, ProjectID: project.ID, Version: strconv.FormatUint(task.Version, 10), AllowedActions: actions})
	}
	return planningport.Snapshot{SchemaVersion: 1, ContractVersion: "director-planning/v1", Page: planningport.Page{
		SelectedProjectID: input.ProjectID, Projects: []planningport.ProjectSummary{{ID: project.ID, Version: projectVersion, AllowedActions: projectActions}},
		Epics: epics, Tasks: tasks}}, nil
}

type capturingController struct {
	store    *memoryStore
	projects []executionport.ControlCommand
	cancels  []executionport.ControlCommand
}

func (controller *capturingController) RequestProjectControl(_ context.Context, command executionport.ControlCommand) (executionport.ControlResult, error) {
	controller.projects = append(controller.projects, command)
	project := controller.store.projects[command.ProjectID]
	project.Version++
	return executionport.ControlResult{Project: project}, nil
}

func (controller *capturingController) CancelTask(_ context.Context, command executionport.ControlCommand) (executionport.ControlResult, error) {
	controller.cancels = append(controller.cancels, command)
	run := controller.store.runs[command.RunID]
	run.Version++
	return executionport.ControlResult{Run: &run}, nil
}

type capturingReconciler struct {
	store *memoryStore
	runs  []string
}

func (reconciler *capturingReconciler) ReconcileRun(_ context.Context, runID string) (bool, error) {
	reconciler.runs = append(reconciler.runs, runID)
	run := reconciler.store.runs[runID]
	run.Version++
	reconciler.store.runs[runID] = run
	return true, nil
}
func (refusalController) CancelTask(context.Context, executionport.ControlCommand) (executionport.ControlResult, error) {
	return executionport.ControlResult{}, errors.New("not exercised")
}

func fixture(t *testing.T) (*Service, *memoryStore, Registration, string) {
	t.Helper()
	store := newMemoryStore()
	store.projects["project-a"] = domain.Project{ID: "project-a", Name: "Alpha", State: "active", Version: 1,
		Organizer: &domain.Organizer{ID: domain.OrganizerID("project-a"), Phase: domain.OrganizerPhaseActive,
			RepositoryPath: "/private/alpha", OrganizerRevision: strings.Repeat("a", 40), ConfigurationSHA256: strings.Repeat("b", 64)}}
	store.projects["project-b"] = domain.Project{ID: "project-b", Name: "Beta", State: "active", Version: 1,
		Organizer: &domain.Organizer{ID: domain.OrganizerID("project-b"), Phase: domain.OrganizerPhaseActive,
			RepositoryPath: "/private/beta", OrganizerRevision: strings.Repeat("c", 40), ConfigurationSHA256: strings.Repeat("d", 64)}}
	store.workspaces["workspace-a"] = domain.Workspace{ID: "workspace-a", ProjectID: "project-a", Name: "Alpha", NativePaseoWorkspaceID: "native-workspace-a", Version: 1}
	store.workspaces["workspace-b"] = domain.Workspace{ID: "workspace-b", ProjectID: "project-b", Name: "Beta", NativePaseoWorkspaceID: "native-workspace-b", Version: 1}
	store.tasks["foreign-task"] = domain.Task{ID: "foreign-task", ProjectID: "project-b", WorkspaceIDs: []string{"workspace-b"}, Version: 4}
	store.runs["foreign-run"] = domain.Run{ID: "foreign-run", TaskID: "foreign-task", Number: 1, Version: 2}
	planner := &capturingPlanner{store: store}
	service, err := NewService(store, currentPlanningReader{store}, planner, refusalController{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	token := "project-admin-session-secret"
	tokenHash := sha256.Sum256([]byte(token))
	registration := Registration{RequestID: "registration-1", SessionID: "session-1", NativeWorkspaceID: "native-workspace-a",
		NativeAgentID: "native-agent-a", Audience: "audience-1", TokenSHA256: hex.EncodeToString(tokenHash[:])}
	return service, store, registration, token
}

func requireCode(t *testing.T, err error, code Code) {
	t.Helper()
	if ErrorCode(err) != string(code) {
		t.Fatalf("error code = %q, want %q (error %v)", ErrorCode(err), code, err)
	}
}

func TestProjectAdminSessionCreatesRealTaskAndReplaysDurably(t *testing.T) {
	ctx := context.Background()
	service, store, registration, token := fixture(t)
	record, version, err := service.RegisterSession(ctx, registration)
	if err != nil || record.Binding.ProjectID != "project-a" || version != 2 {
		t.Fatalf("RegisterSession() = %#v, %d, %v", record, version, err)
	}
	if _, _, err := service.RegisterSession(ctx, registration); err != nil {
		t.Fatalf("RegisterSession replay error = %v", err)
	}
	if _, err := service.OpenSession(ctx, registration.SessionID, registration.Audience, ""); err == nil {
		t.Fatal("anonymous session opened")
	} else {
		requireCode(t, err, CodeSessionInvalid)
	}
	if _, err := service.OpenSession(ctx, registration.SessionID, "other-audience", token); err == nil {
		t.Fatal("confused audience opened")
	} else {
		requireCode(t, err, CodeSessionInvalid)
	}
	session, err := service.OpenSession(ctx, registration.SessionID, registration.Audience, token)
	if err != nil {
		t.Fatal(err)
	}

	projectResult, err := session.Call(ctx, "read-1", "director_admin_project_read", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(projectResult.Payload), "/private/") || strings.Contains(string(projectResult.Payload), "native-agent") ||
		strings.Contains(string(projectResult.Payload), registration.TokenSHA256) || strings.Contains(string(projectResult.Payload), "project-b") {
		t.Fatalf("project projection leaked private authority: %s", projectResult.Payload)
	}
	planningResult, err := session.Call(ctx, "planning-read-1", "director_admin_planning_read", json.RawMessage(`{}`))
	if err != nil || strings.Contains(string(planningResult.Payload), "project-b") {
		t.Fatalf("planning projection escaped fixed Project = %s, %v", planningResult.Payload, err)
	}
	if _, err := session.Call(ctx, "cross-1", "director_admin_execution_read", json.RawMessage(`{"view":"runs","taskId":"foreign-task"}`)); err == nil {
		t.Fatal("cross-Project Task read succeeded")
	} else {
		requireCode(t, err, CodeScopeMismatch)
	}
	foreignCreate := json.RawMessage(`{"requestId":"foreign-create","idempotencyKey":"foreign-create","expectedVersion":"2","type":"task.create","workspaceId":"workspace-b","epicId":null,"key":"DIR-X","title":"Foreign","objective":"Cross Project","acceptanceCriteria":["Never"],"priority":"normal","labels":[]}`)
	if _, err := session.Call(ctx, "cross-2", "director_admin_planning_command", foreignCreate); err == nil {
		t.Fatal("cross-Project Task creation succeeded")
	} else {
		requireCode(t, err, CodeScopeMismatch)
	}

	command := json.RawMessage(`{"requestId":"task-create-1","idempotencyKey":"task-create-1","expectedVersion":"2","type":"task.create","workspaceId":"workspace-a","epicId":null,"key":"DIR-1","title":"Created through MCP","objective":"Exercise the real planning command","acceptanceCriteria":["Task is durable"],"priority":"normal","labels":["mcp"]}`)
	created, err := session.Call(ctx, "call-1", "director_admin_planning_command", command)
	if err != nil {
		t.Fatalf("task create error = %v", err)
	}
	if len(store.tasks) != 2 || !strings.Contains(string(created.Payload), `"projectVersion":"3"`) || !strings.Contains(string(created.Payload), `"replay":false`) {
		t.Fatalf("task creation output/store = %s, %#v", created.Payload, store.tasks)
	}
	replayed, err := session.Call(ctx, "call-2", "director_admin_planning_command", command)
	if err != nil || !strings.Contains(string(replayed.Payload), `"replay":true`) {
		t.Fatalf("task replay = %s, %v", replayed.Payload, err)
	}
	changed := strings.Replace(string(command), "Created through MCP", "Changed replay", 1)
	if _, err := session.Call(ctx, "call-3", "director_admin_planning_command", json.RawMessage(changed)); err == nil {
		t.Fatal("changed idempotency replay succeeded")
	} else {
		requireCode(t, err, CodeIdempotencyConflict)
	}
	stale := strings.Replace(strings.Replace(string(command), "task-create-1", "task-create-2", 2), `"2"`, `"1"`, 1)
	if _, err := session.Call(ctx, "call-4", "director_admin_planning_command", json.RawMessage(stale)); err == nil {
		t.Fatal("stale expected version succeeded")
	} else {
		requireCode(t, err, CodeExpectedVersionConflict)
	}

	if err := service.RevokeSession(ctx, "revoke-1", registration.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Call(ctx, "read-after-revoke", "director_admin_project_read", json.RawMessage(`{}`)); err == nil {
		t.Fatal("revoked session remained usable")
	} else {
		requireCode(t, err, CodeSessionRevoked)
	}
}

func TestRegistrationFailsClosedForAmbiguousOrUnknownWorkspace(t *testing.T) {
	ctx := context.Background()
	service, store, registration, _ := fixture(t)
	unknown := registration
	unknown.SessionID, unknown.RequestID, unknown.NativeWorkspaceID = "unknown-session", "unknown-request", "unknown-workspace"
	if _, _, err := service.RegisterSession(ctx, unknown); err == nil {
		t.Fatal("unknown workspace registered")
	} else {
		requireCode(t, err, CodeScopeMismatch)
	}
	workspace := store.workspaces["workspace-b"]
	workspace.NativePaseoWorkspaceID = registration.NativeWorkspaceID
	store.workspaces[workspace.ID] = workspace
	if _, _, err := service.RegisterSession(ctx, registration); err == nil {
		t.Fatal("ambiguous same-instance workspace registered")
	} else {
		requireCode(t, err, CodeScopeMismatch)
	}
}

func TestProjectAdminInputAndOutputAreBounded(t *testing.T) {
	ctx := context.Background()
	service, store, registration, token := fixture(t)
	if _, _, err := service.RegisterSession(ctx, registration); err != nil {
		t.Fatal(err)
	}
	session, err := service.OpenSession(ctx, registration.SessionID, registration.Audience, token)
	if err != nil {
		t.Fatal(err)
	}
	tooLarge := json.RawMessage(`{"view":"runs","unknown":"` + strings.Repeat("x", contract.MaximumRequestBytes) + `"}`)
	if _, err := session.Call(ctx, "large-1", "director_admin_execution_read", tooLarge); err == nil {
		t.Fatal("oversized input succeeded")
	} else {
		requireCode(t, err, CodeInputTooLarge)
	}
	if _, err := session.Call(ctx, "unknown-1", "raw_taskstore_query", json.RawMessage(`{}`)); err == nil {
		t.Fatal("unknown raw storage tool succeeded")
	} else {
		requireCode(t, err, CodeToolNotAllowed)
	}
	for index := 0; index < 64; index++ {
		id := fmt.Sprintf("workspace-large-%d", index)
		store.workspaces[id] = domain.Workspace{ID: id, ProjectID: "project-a", Name: strings.Repeat("w", 2048), Version: 1}
	}
	if _, err := session.Call(ctx, "large-output-1", "director_admin_project_read", json.RawMessage(`{}`)); err == nil {
		t.Fatal("oversized output succeeded")
	} else {
		requireCode(t, err, CodeOutputTooLarge)
	}
}

func TestEveryExposedAdministrativeCommandUsesAgentActorAndCurrentGates(t *testing.T) {
	ctx := context.Background()
	_, store, registration, token := fixture(t)
	store.epics["epic-a"] = domain.Epic{ID: "epic-a", ProjectID: "project-a", Version: 5}
	store.tasks["task-a"] = domain.Task{ID: "task-a", ProjectID: "project-a", WorkspaceIDs: []string{"workspace-a"}, Version: 7}
	store.runs["run-a"] = domain.Run{ID: "run-a", TaskID: "task-a", Version: 9}
	planner := &capturingPlanner{}
	controller := &capturingController{store: store}
	reconciler := &capturingReconciler{store: store}
	service, err := NewService(store, allCommandsReader{store}, planner, controller, reconciler)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.RegisterSession(ctx, registration); err != nil {
		t.Fatal(err)
	}
	session, err := service.OpenSession(ctx, registration.SessionID, registration.Audience, token)
	if err != nil {
		t.Fatal(err)
	}

	projectCommand := func(requestID, kind, tail string) json.RawMessage {
		version := store.projects["project-a"].Version
		separator := ""
		if tail != "" {
			separator = ","
		}
		return json.RawMessage(fmt.Sprintf(`{"requestId":%q,"idempotencyKey":%q,"expectedVersion":%q,"type":%q%s%s}`,
			requestID, requestID, strconv.FormatUint(version, 10), kind, separator, tail))
	}
	planningCommands := []func() json.RawMessage{
		func() json.RawMessage { return projectCommand("project-update", "project.update", `"name":"Renamed"`) },
		func() json.RawMessage {
			return projectCommand("epic-create", "epic.create", `"key":"E-2","title":"Epic","description":"Description","priority":null,"labels":[]`)
		},
		func() json.RawMessage {
			return json.RawMessage(`{"requestId":"epic-update","idempotencyKey":"epic-update","expectedVersion":"5","type":"epic.update","epicId":"epic-a","title":"Epic","description":"Description","priority":null,"labels":[]}`)
		},
		func() json.RawMessage {
			return projectCommand("task-create", "task.create", `"workspaceId":"workspace-a","epicId":"epic-a","key":"T-2","title":"Task","objective":"Objective","acceptanceCriteria":["Done"],"priority":"normal","labels":[]`)
		},
		func() json.RawMessage {
			return json.RawMessage(`{"requestId":"task-update","idempotencyKey":"task-update","expectedVersion":"7","type":"task.update","taskId":"task-a","epicId":null,"title":"Task","objective":"Objective","acceptanceCriteria":["Done"],"priority":"normal","labels":[]}`)
		},
		func() json.RawMessage {
			return json.RawMessage(`{"requestId":"dependency-add","idempotencyKey":"dependency-add","expectedVersion":"7","type":"dependency.add","taskId":"task-a","dependencyKind":"epic","dependencyId":"epic-a"}`)
		},
		func() json.RawMessage {
			return json.RawMessage(`{"requestId":"dependency-remove","idempotencyKey":"dependency-remove","expectedVersion":"7","type":"dependency.remove","taskId":"task-a","dependencyKind":"epic","dependencyId":"epic-a"}`)
		},
		func() json.RawMessage {
			return json.RawMessage(`{"requestId":"launch-now","idempotencyKey":"launch-now","expectedVersion":"7","type":"task.launch-now","taskId":"task-a"}`)
		},
	}
	for index, command := range planningCommands {
		if _, err := session.Call(ctx, fmt.Sprintf("planning-%d", index), "director_admin_planning_command", command()); err != nil {
			t.Fatalf("planning command %d error = %v", index, err)
		}
	}
	if len(planner.inputs) != len(planningCommands) {
		t.Fatalf("planner calls = %d, want %d", len(planner.inputs), len(planningCommands))
	}
	for _, actor := range planner.actors {
		if actor.Kind != "agent" || actor.ID != registration.NativeAgentID || actor.SessionID != registration.SessionID {
			t.Fatalf("planning actor = %#v", actor)
		}
	}

	controls := []func() json.RawMessage{
		func() json.RawMessage { return projectCommand("pause-project", "project.pause", "") },
		func() json.RawMessage { return projectCommand("resume-project", "project.resume", "") },
		func() json.RawMessage {
			return json.RawMessage(`{"requestId":"cancel-task","idempotencyKey":"cancel-task","expectedVersion":"9","type":"task.cancel","taskId":"task-a","runId":"run-a"}`)
		},
		func() json.RawMessage {
			return json.RawMessage(`{"requestId":"recover-run","idempotencyKey":"recover-run","expectedVersion":"9","type":"recovery.reconcile","runId":"run-a"}`)
		},
	}
	for index, command := range controls {
		if _, err := session.Call(ctx, fmt.Sprintf("control-%d", index), "director_admin_control_command", command()); err != nil {
			t.Fatalf("control command %d error = %v", index, err)
		}
	}
	if len(controller.projects) != 2 || len(controller.cancels) != 1 || len(reconciler.runs) != 1 {
		t.Fatalf("control dispatch = projects:%d cancels:%d recovery:%d", len(controller.projects), len(controller.cancels), len(reconciler.runs))
	}
	for _, command := range append(controller.projects, controller.cancels...) {
		if !command.Actor.Authenticated || command.Actor.Kind != "agent" || command.Actor.ID != registration.NativeAgentID {
			t.Fatalf("control actor = %#v", command.Actor)
		}
	}
}
