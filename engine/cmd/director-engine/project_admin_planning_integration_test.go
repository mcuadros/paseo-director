// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	planningapp "github.com/mcuadros/director-engine/application/planning"
	adminapp "github.com/mcuadros/director-engine/application/projectadmin"
	"github.com/mcuadros/director-engine/domain"
	admincontract "github.com/mcuadros/director-engine/domain/projectadmin"
	executionport "github.com/mcuadros/director-engine/ports/execution"
	planningport "github.com/mcuadros/director-engine/ports/planning"
	storeport "github.com/mcuadros/director-engine/ports/taskstore"
)

type adminPlanningStore struct {
	project   domain.Project
	workspace domain.Workspace
	tasks     map[string]domain.Task
	commands  map[string]domain.Command
}

func (store *adminPlanningStore) Project(_ context.Context, id string) (domain.Project, error) {
	if id != store.project.ID {
		return domain.Project{}, storeport.ErrNotFound
	}
	return store.project, nil
}

func (store *adminPlanningStore) Projects(context.Context) ([]domain.Project, error) {
	return []domain.Project{store.project}, nil
}

func (store *adminPlanningStore) Workspace(_ context.Context, id string) (domain.Workspace, error) {
	if id != store.workspace.ID {
		return domain.Workspace{}, storeport.ErrNotFound
	}
	return store.workspace, nil
}

func (store *adminPlanningStore) Workspaces(_ context.Context, projectID string) ([]domain.Workspace, error) {
	if projectID != store.project.ID {
		return nil, storeport.ErrNotFound
	}
	return []domain.Workspace{store.workspace}, nil
}

func (*adminPlanningStore) Epic(context.Context, string) (domain.Epic, error) {
	return domain.Epic{}, storeport.ErrNotFound
}

func (*adminPlanningStore) Epics(context.Context, string) ([]domain.Epic, error) {
	return []domain.Epic{}, nil
}

func (*adminPlanningStore) CreateEpic(context.Context, domain.CommandRequest, domain.Epic, domain.Event) (domain.CommandResult, error) {
	return domain.CommandResult{}, errors.New("unexpected Epic creation")
}

func (*adminPlanningStore) UpdateEpic(context.Context, domain.CommandRequest, domain.Epic, domain.Event) (domain.CommandResult, error) {
	return domain.CommandResult{}, errors.New("unexpected Epic update")
}

func (store *adminPlanningStore) Task(_ context.Context, id string) (domain.Task, error) {
	task, ok := store.tasks[id]
	if !ok {
		return domain.Task{}, storeport.ErrNotFound
	}
	return task, nil
}

func (store *adminPlanningStore) Tasks(_ context.Context, projectID string) ([]domain.Task, error) {
	result := []domain.Task{}
	for _, task := range store.tasks {
		if task.ProjectID == projectID {
			result = append(result, task)
		}
	}
	return result, nil
}

func (store *adminPlanningStore) CreateTask(_ context.Context, request domain.CommandRequest, task domain.Task, event domain.Event) (domain.CommandResult, error) {
	if _, exists := store.tasks[task.ID]; exists {
		return domain.CommandResult{}, storeport.ErrAlreadyExists
	}
	store.tasks[task.ID] = task
	store.commands[request.IdempotencyKey] = domain.Command{IdempotencyKey: request.IdempotencyKey, Type: request.Type,
		AggregateID: request.AggregateID, ExpectedVersion: request.ExpectedVersion, Payload: request.Payload,
		PayloadHash: "recorded", Outcome: domain.CommandApplied, ObservedVersion: task.Version, EventID: event.ID}
	return domain.CommandResult{Outcome: domain.CommandApplied, ObservedVersion: task.Version, EventID: event.ID}, nil
}

func (*adminPlanningStore) UpdateTask(context.Context, domain.CommandRequest, domain.Task, domain.Event) (domain.CommandResult, error) {
	return domain.CommandResult{}, errors.New("unexpected Task update")
}

func (*adminPlanningStore) GrantDependencyOverride(context.Context, domain.CommandRequest, domain.HumanDependencyOverrideGrant) (domain.CommandResult, error) {
	return domain.CommandResult{}, errors.New("human-only dependency override reached Project administration")
}

func (*adminPlanningStore) Run(context.Context, string) (domain.Run, error) {
	return domain.Run{}, storeport.ErrNotFound
}

func (*adminPlanningStore) Runs(context.Context, string) ([]domain.Run, error) {
	return []domain.Run{}, nil
}

func (*adminPlanningStore) Candidates(context.Context, string) ([]domain.Candidate, error) {
	return []domain.Candidate{}, nil
}

func (store *adminPlanningStore) Command(_ context.Context, key string) (domain.Command, error) {
	command, ok := store.commands[key]
	if !ok {
		return domain.Command{}, storeport.ErrNotFound
	}
	return command, nil
}

func (store *adminPlanningStore) LatestEventSequence(context.Context) (uint64, error) {
	return uint64(len(store.commands)), nil
}

func (store *adminPlanningStore) UpdateProject(_ context.Context, request domain.CommandRequest, project domain.Project, event domain.Event) (domain.CommandResult, error) {
	if store.project.Version != request.ExpectedVersion || project.Version != request.ExpectedVersion+1 {
		return domain.CommandResult{Outcome: domain.CommandRejectedVersionConflict, ObservedVersion: store.project.Version}, nil
	}
	store.project = project
	store.commands[request.IdempotencyKey] = domain.Command{IdempotencyKey: request.IdempotencyKey, Type: request.Type,
		AggregateID: request.AggregateID, ExpectedVersion: request.ExpectedVersion, Payload: request.Payload,
		PayloadHash: "recorded", Outcome: domain.CommandApplied, ObservedVersion: project.Version, EventID: event.ID}
	return domain.CommandResult{Outcome: domain.CommandApplied, ObservedVersion: project.Version, EventID: event.ID}, nil
}

type adminPlanningReader struct{ store *adminPlanningStore }

func (reader adminPlanningReader) Query(_ context.Context, input planningport.QueryInput) (planningport.Snapshot, error) {
	if input.ProjectID == nil || *input.ProjectID != reader.store.project.ID {
		return planningport.Snapshot{}, errors.New("Project scope was not fixed")
	}
	target := reader.store.project.ID
	version := strconv.FormatUint(reader.store.project.Version, 10)
	return planningport.Snapshot{SchemaVersion: 1, ContractVersion: "director-planning/v1", Cursor: "1",
		Page: planningport.Page{SelectedProjectID: input.ProjectID, Projects: []planningport.ProjectSummary{{
			ID: target, Version: version, AllowedActions: []planningport.AllowedAction{{Kind: "task.create", TargetID: &target, ExpectedVersion: version}},
		}}, Workspaces: []planningport.WorkspaceSummary{}, Epics: []planningport.EpicSummary{}, Tasks: []planningport.TaskSummary{}}}, nil
}

type refusedAdminControl struct{}

func (refusedAdminControl) RequestProjectControl(context.Context, executionport.ControlCommand) (executionport.ControlResult, error) {
	return executionport.ControlResult{}, errors.New("unexpected control")
}

func (refusedAdminControl) CancelTask(context.Context, executionport.ControlCommand) (executionport.ControlResult, error) {
	return executionport.ControlResult{}, errors.New("unexpected cancellation")
}

func TestProjectAdminTaskCreationUsesProductionPlanningService(t *testing.T) {
	store := &adminPlanningStore{project: domain.Project{ID: "project-integration", Name: "Integration", State: "active", Version: 1,
		Organizer: &domain.Organizer{ID: domain.OrganizerID("project-integration"), Phase: domain.OrganizerPhaseActive}},
		workspace: domain.Workspace{ID: "workspace-integration", ProjectID: "project-integration", NativePaseoWorkspaceID: "native-workspace-integration", Version: 1},
		tasks:     map[string]domain.Task{}, commands: map[string]domain.Command{}}
	planner, err := planningapp.NewService(store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := adminapp.NewService(store, adminPlanningReader{store}, planner, refusedAdminControl{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	token := "project-admin-integration-token"
	tokenDigest := sha256.Sum256([]byte(token))
	registration := adminapp.Registration{RequestID: "register-integration", SessionID: "session-integration",
		NativeWorkspaceID: store.workspace.NativePaseoWorkspaceID, NativeAgentID: "ordinary-native-agent",
		Audience: "audience-integration", TokenSHA256: hex.EncodeToString(tokenDigest[:])}
	if _, version, registerErr := service.RegisterSession(context.Background(), registration); registerErr != nil || version != 2 {
		t.Fatalf("registration = version %d, error %v", version, registerErr)
	}
	session, err := service.OpenSession(context.Background(), registration.SessionID, registration.Audience, token)
	if err != nil {
		t.Fatal(err)
	}
	command := json.RawMessage(`{"requestId":"create-integration","idempotencyKey":"create-integration","expectedVersion":"2","type":"task.create","workspaceId":"workspace-integration","epicId":null,"key":"DIR-INT","title":"Created by an ordinary agent","objective":"Use the production planning service","acceptanceCriteria":["Task is durable"],"priority":"normal","labels":["mcp"]}`)
	result, err := session.Call(context.Background(), "mcp-create-integration", "director_admin_planning_command", command)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.tasks) != 1 || store.project.Version != 3 || len(store.project.ProjectAdmin.Receipts) != 1 ||
		!admincontract.ValidState(store.project.ProjectAdmin, store.project.ID) ||
		!json.Valid(result.Payload) {
		t.Fatalf("production Task creation = tasks %d, Project version %d, receipts %d, output %s",
			len(store.tasks), store.project.Version, len(store.project.ProjectAdmin.Receipts), result.Payload)
	}
	for _, task := range store.tasks {
		if task.ProjectID != store.project.ID || task.WorkspaceIDs[0] != store.workspace.ID || task.Objective != "Use the production planning service" {
			t.Fatalf("created Task = %#v", task)
		}
	}
}
