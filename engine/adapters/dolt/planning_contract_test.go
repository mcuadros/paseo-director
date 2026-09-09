// SPDX-License-Identifier: Apache-2.0

package dolt_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/mcuadros/director-engine/domain"
	storeport "github.com/mcuadros/director-engine/ports/taskstore"
)

func createPlanningProject(t *testing.T, store storeport.TaskStore) (domain.Project, []domain.Workspace) {
	t.Helper()
	project := domain.Project{
		ID: "planning-project", Name: "Planning project", State: "active",
		Organizer: testOrganizer("planning-project"),
	}
	workspaces := []domain.Workspace{
		testWorkspace(t, project.ID, "repository-a", "/srv/planning/repository-a", "https://github.com/example/repository-a.git"),
		testWorkspace(t, project.ID, "repository-b", "/srv/planning/repository-b", "https://github.com/example/repository-b.git"),
	}
	result, err := store.CreateProject(
		context.Background(),
		command("planning-project-create", "project.create", project.ID, 0, `{"name":"Planning project"}`),
		project, workspaces,
		event("planning-project-created", "", 1, project.ID, 0, "project.created"),
	)
	requireApplied(t, result, err)
	return project, workspaces
}

func planningSnapshot(t *testing.T, store storeport.TaskStore, project domain.Project, workspaces []domain.Workspace) domain.PlanningProject {
	t.Helper()
	epics, err := store.Epics(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("read Epics: %v", err)
	}
	tasks, err := store.Tasks(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("read Tasks: %v", err)
	}
	overrides, err := store.DependencyOverrides(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("read dependency overrides: %v", err)
	}
	return domain.PlanningProject{
		ID: project.ID, Workspaces: []string{workspaces[0].ID, workspaces[1].ID},
		Epics: epics, Tasks: tasks, Overrides: overrides,
	}
}

func TestDoltPlanningContractPersistsCrossRepositoryGraphAndAuditedOverrides(t *testing.T) {
	fixture := startDoltFixture(t)
	store := openContractStore(t, fixture, "planning-contract-store", true)
	t.Cleanup(func() { _ = store.Close() })
	project, workspaces := createPlanningProject(t, store)
	ctx := context.Background()

	blocking := domain.Epic{
		ID: "epic-blocking", ProjectID: project.ID, Key: "BLOCK", Title: "Blocking epic",
	}
	result, err := store.CreateEpic(
		ctx, command("epic-blocking-create", "epic.create", blocking.ID, 0, `{}`), blocking,
		event("epic-blocking-created", "", 1, blocking.ID, 0, "epic.created"),
	)
	requireApplied(t, result, err)
	edge := domain.PlanningDependency{
		From: domain.PlanningNodeRef{Kind: domain.PlanningNodeEpic, ID: "epic-dependent"},
		On:   domain.PlanningNodeRef{Kind: domain.PlanningNodeEpic, ID: blocking.ID},
	}
	dependent := domain.Epic{
		ID: "epic-dependent", ProjectID: project.ID, Key: "DEPEND", Title: "Dependent epic",
		Dependencies: []domain.PlanningDependency{edge},
	}
	result, err = store.CreateEpic(
		ctx, command("epic-dependent-create", "epic.create", dependent.ID, 0, `{}`), dependent,
		event("epic-dependent-created", "", 1, dependent.ID, 0, "epic.created"),
	)
	requireApplied(t, result, err)
	parent := domain.PlanningNodeRef{Kind: domain.PlanningNodeEpic, ID: dependent.ID}
	task := domain.Task{
		ID: "task-dependent", ProjectID: project.ID, Key: "TASK-1", Title: "Dependent Task",
		Objective: "Exercise Epic propagation", AcceptanceCriteria: "Human override is audited",
		WorkspaceIDs: []string{workspaces[1].ID}, Parent: &parent,
		ExternalReferences: []domain.ExternalReference{{Provider: "github", Key: "example/repository-b#42"}},
		Priority:           domain.PriorityHigh, Labels: []string{"planning"}, QueuedAtUnixMillis: 200,
	}
	result, err = store.CreateTask(
		ctx, command("task-dependent-create", "task.create", task.ID, 0, `{}`), task,
		event("task-dependent-created", "", 1, task.ID, 0, "task.created"),
	)
	requireApplied(t, result, err)
	crossEdge := domain.PlanningDependency{
		From: domain.PlanningNodeRef{Kind: domain.PlanningNodeTask, ID: "task-cross"},
		On:   domain.PlanningNodeRef{Kind: domain.PlanningNodeTask, ID: task.ID},
	}
	crossTask := domain.Task{
		ID: "task-cross", ProjectID: project.ID, Key: "TASK-2", Title: "Cross repository Task",
		Objective: "Depend on another repository", AcceptanceCriteria: "Dependency remains canonical",
		WorkspaceIDs: []string{workspaces[0].ID}, Dependencies: []domain.PlanningDependency{crossEdge},
		Priority: domain.PriorityUrgent, Labels: []string{"planning", "cross-repository"}, QueuedAtUnixMillis: 100,
	}
	result, err = store.CreateTask(
		ctx, command("task-cross-create", "task.create", crossTask.ID, 0, `{}`), crossTask,
		event("task-cross-created", "", 1, crossTask.ID, 0, "task.created"),
	)
	requireApplied(t, result, err)
	persistedTask, err := store.Task(ctx, task.ID)
	if err != nil || persistedTask.QueuedAtUnixMillis != task.QueuedAtUnixMillis ||
		persistedTask.Priority != task.Priority || !slices.Equal(persistedTask.Labels, task.Labels) ||
		!slices.Equal(persistedTask.ExternalReferences, task.ExternalReferences) {
		t.Fatalf("persisted planning Task = %#v, %v", persistedTask, err)
	}

	before := domain.EvaluatePlanning(planningSnapshot(t, store, project, workspaces))
	if result, ok := before.Result(task.ID); !ok || !result.Blocked || !before.HasCode(domain.PlanningEpicDependencyBlocked) {
		t.Fatalf("Epic blocker did not propagate before override: %#v", before)
	}
	grant := domain.HumanDependencyOverrideGrant{
		ID: "override-dependent", TaskID: task.ID, ExpectedTaskVersion: task.Version, Dependency: edge,
		HumanActorID: "human-owner", AuditID: "audit-dependent",
	}
	grantCommand := typedCommand(t, "override-dependent-grant", "task.dependency_override.grant", grant.ID, 0, grant)
	result, err = store.GrantDependencyOverride(ctx, grantCommand, grant)
	requireApplied(t, result, err)
	replay, err := store.GrantDependencyOverride(ctx, grantCommand, grant)
	if err != nil || !replay.Replay || replay.Outcome != domain.CommandApplied {
		t.Fatalf("override replay = %#v, %v", replay, err)
	}
	persisted, err := store.DependencyOverride(ctx, grant.ID)
	if err != nil || persisted.ActorKind != domain.PlanningActorHuman || !persisted.Granted ||
		persisted.TaskVersion != task.Version || persisted.GrantedAtMillis <= 0 || persisted.AuditID != grant.AuditID {
		t.Fatalf("persisted override = %#v, %v", persisted, err)
	}
	after := domain.EvaluatePlanning(planningSnapshot(t, store, project, workspaces))
	if result, ok := after.Result(task.ID); !ok || result.Blocked {
		t.Fatalf("exact human override did not release only its Task: %#v", after)
	}
	if result, ok := after.Result(crossTask.ID); !ok || !result.Blocked {
		t.Fatalf("override leaked to another Task: %#v", after)
	}

	ambiguous := grant
	ambiguous.ID = "override-ambiguous"
	ambiguous.AuditID = "audit-ambiguous"
	ambiguousCommand := typedCommand(t, "override-ambiguous-grant", "task.dependency_override.grant", ambiguous.ID, 0, ambiguous)
	if _, err := store.GrantDependencyOverride(ctx, ambiguousCommand, ambiguous); !errors.Is(err, storeport.ErrInvalidRecord) {
		t.Fatalf("ambiguous override error = %v", err)
	}
	unauthorizedGrant := grant
	unauthorizedGrant.ID = "override-agent"
	unauthorizedGrant.HumanActorID = "agent-1"
	unauthorizedGrant.AuditID = "audit-agent"
	unauthorizedPayload := json.RawMessage(`{"id":"override-agent","taskId":"task-dependent","expectedTaskVersion":0,"dependency":{"from":{"kind":"epic","id":"epic-dependent"},"on":{"kind":"epic","id":"epic-blocking"}},"humanActorId":"agent-1","auditId":"audit-agent","actorKind":"agent"}`)
	unauthorizedCommand := domain.CommandRequest{
		IdempotencyKey: "override-agent-grant", Type: "task.dependency_override.grant",
		AggregateID: "override-agent", ExpectedVersion: 0, Payload: unauthorizedPayload,
	}
	if _, err := store.GrantDependencyOverride(ctx, unauthorizedCommand, unauthorizedGrant); !errors.Is(err, storeport.ErrInvalidRecord) {
		t.Fatalf("caller-selected actor kind error = %v", err)
	}

	task.Title = "Updated dependent Task"
	task.Version++
	result, err = store.UpdateTask(
		ctx, command("task-dependent-update", "task.update", task.ID, 0, `{}`), task,
		event("task-dependent-updated", "", 2, task.ID, 1, "task.updated"),
	)
	requireApplied(t, result, err)
	stale := domain.EvaluatePlanning(planningSnapshot(t, store, project, workspaces))
	if result, ok := stale.Result(task.ID); !ok || !result.Blocked || !stale.HasCode(domain.PlanningOverrideStale) {
		t.Fatalf("stale override released updated Task: %#v", stale)
	}
	currentGrant := grant
	currentGrant.ID = "override-dependent-v1"
	currentGrant.ExpectedTaskVersion = task.Version
	currentGrant.AuditID = "audit-dependent-v1"
	currentCommand := typedCommand(t, "override-dependent-v1-grant", "task.dependency_override.grant", currentGrant.ID, 0, currentGrant)
	result, err = store.GrantDependencyOverride(ctx, currentCommand, currentGrant)
	requireApplied(t, result, err)
	current := domain.EvaluatePlanning(planningSnapshot(t, store, project, workspaces))
	if result, ok := current.Result(task.ID); !ok || result.Blocked ||
		!current.HasCode(domain.PlanningOverrideStale) || current.HasCode(domain.PlanningOverrideAmbiguous) {
		t.Fatalf("current override did not coexist with immutable stale history: %#v", current)
	}
}

func TestDoltPlanningGraphSerializesConcurrentCycleCreation(t *testing.T) {
	fixture := startDoltFixture(t)
	store := openContractStore(t, fixture, "planning-cycle-store", true)
	t.Cleanup(func() { _ = store.Close() })
	project, workspaces := createPlanningProject(t, store)
	ctx := context.Background()
	epics := []domain.Epic{
		{ID: "epic-a", ProjectID: project.ID, Key: "A", Title: "Epic A"},
		{ID: "epic-b", ProjectID: project.ID, Key: "B", Title: "Epic B"},
	}
	for _, epic := range epics {
		result, err := store.CreateEpic(
			ctx, command(epic.ID+"-create", "epic.create", epic.ID, 0, `{}`), epic,
			event(epic.ID+"-created", "", 1, epic.ID, 0, "epic.created"),
		)
		requireApplied(t, result, err)
	}
	start := make(chan struct{})
	results := make(chan domain.CommandResult, 2)
	errorsChannel := make(chan error, 2)
	var wait sync.WaitGroup
	for index := range epics {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			replacement := epics[index]
			replacement.Version = 1
			replacement.Dependencies = []domain.PlanningDependency{{
				From: domain.PlanningNodeRef{Kind: domain.PlanningNodeEpic, ID: replacement.ID},
				On:   domain.PlanningNodeRef{Kind: domain.PlanningNodeEpic, ID: epics[1-index].ID},
			}}
			result, err := store.UpdateEpic(
				ctx, command(replacement.ID+"-edge", "epic.update", replacement.ID, 0, `{}`), replacement,
				event(replacement.ID+"-edge-event", "", 2, replacement.ID, 1, "epic.updated"),
			)
			results <- result
			errorsChannel <- err
		}(index)
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsChannel)
	applied, rejected := 0, 0
	for result := range results {
		if result.Outcome == domain.CommandApplied {
			applied++
		}
	}
	for err := range errorsChannel {
		switch {
		case err == nil:
		case errors.Is(err, storeport.ErrInvalidRecord):
			rejected++
		default:
			t.Fatalf("concurrent graph update error = %v", err)
		}
	}
	if applied != 1 || rejected != 1 {
		t.Fatalf("concurrent cycle results: applied=%d rejected=%d", applied, rejected)
	}
	planning := planningSnapshot(t, store, project, workspaces)
	if report := domain.EvaluatePlanning(planning); !report.Valid || report.HasCode(domain.PlanningDependencyCycle) {
		t.Fatalf("persisted graph contains a cycle: %#v", report)
	}
}
