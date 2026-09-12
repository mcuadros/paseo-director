// SPDX-License-Identifier: Apache-2.0

package dolt_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/mcuadros/director-engine/adapters/dolt"
	"github.com/mcuadros/director-engine/domain"
	repositorydomain "github.com/mcuadros/director-engine/domain/repository"
	storeport "github.com/mcuadros/director-engine/ports/taskstore"
)

const faultStoreID = "director-fault-store"

func faultProject(id, name string) domain.Project {
	return domain.Project{ID: id, Name: name, State: "active", Organizer: testOrganizer(id)}
}

func faultWorkspace(t *testing.T, projectID string) domain.Workspace {
	return testWorkspace(
		t, projectID, "workspace-"+projectID, "/srv/workspaces/"+projectID,
		"https://github.com/example/"+projectID+".git",
	)
}

type portCall struct {
	name   string
	invoke func(context.Context, storeport.TaskStore) error
}

func faultPortCalls(t *testing.T) []portCall {
	project := faultProject("fault-project", "Fault project")
	workspace := faultWorkspace(t, project.ID)
	task := domain.Task{
		ID: "fault-task", ProjectID: project.ID, Title: "Fault task",
		Objective: "Exercise failures", AcceptanceCriteria: "Every error is typed",
		WorkspaceIDs: []string{workspace.ID},
	}
	run := domain.Run{
		ID: "fault-run", TaskID: task.ID, Number: 1, BaseSHA: baseSHA,
		CurrentCandidateID: "fault-candidate", Version: 1,
	}
	return []portCall{
		{name: "SchemaVersion", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			_, err := store.SchemaVersion(ctx)
			return err
		}},
		{name: "CreateProject", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			_, err := store.CreateProject(
				ctx,
				command("fault-create-project", "project.create", "new-project", 0, `{"name":"New"}`),
				faultProject("new-project", "New"),
				[]domain.Workspace{faultWorkspace(t, "new-project")},
				event("fault-create-project-event", "", 1, "new-project", 0, "project.created"),
			)
			return err
		}},
		{name: "Project", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			_, err := store.Project(ctx, project.ID)
			return err
		}},
		{name: "Projects", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			_, err := store.Projects(ctx)
			return err
		}},
		{name: "UpdateProject", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			replacement := project
			replacement.Name = "Updated fault project"
			replacement.Version = 1
			_, err := store.UpdateProject(
				ctx,
				command("fault-update-project", "project.update", project.ID, 0, `{"name":"Updated"}`),
				replacement,
				event("fault-update-project-event", "", 2, project.ID, 1, "project.updated"),
			)
			return err
		}},
		{name: "ApplyProjectLease", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			mutation := domain.ProjectLeaseMutation{
				Kind: domain.ProjectLeaseAcquire, ProjectID: project.ID,
				HolderInstance: "engine-a", HolderProcessIdentity: "pid-100:start-1", DurationMillis: 1_000,
			}
			_, err := store.ApplyProjectLease(
				ctx, typedCommand(t, "fault-lease-apply", "project.lease.acquire", project.ID, 0, mutation), mutation,
			)
			return err
		}},
		{name: "RecordProjectLeaseObservation", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			input := validObservationInputForStore()
			payload := struct {
				ProjectID string                              `json:"projectId"`
				Input     domain.ProjectLeaseObservationInput `json:"input"`
			}{project.ID, input}
			_, err := store.RecordProjectLeaseObservation(
				ctx, typedCommand(t, "fault-lease-observe", "project.lease.observe_takeover", project.ID, 0, payload),
				project.ID, input,
			)
			return err
		}},
		{name: "EnableProjectLeaseDispatch", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			payload := struct {
				ProjectID     string `json:"projectId"`
				ObservationID string `json:"observationId"`
			}{project.ID, "observation-fault"}
			_, err := store.EnableProjectLeaseDispatch(
				ctx, typedCommand(t, "fault-lease-enable", "project.lease.enable_dispatch", project.ID, 0, payload),
				project.ID, payload.ObservationID,
			)
			return err
		}},
		{name: "CreateWorkspace", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			candidate := testWorkspace(
				t, project.ID, "new-workspace", "/srv/workspaces/new-workspace",
				"https://github.com/example/new-workspace.git",
			)
			_, err := store.CreateWorkspace(
				ctx, command("fault-create-workspace", "workspace.create", candidate.ID, 0, `{}`), candidate,
				event("fault-create-workspace-event", "", 1, candidate.ID, 0, "workspace.created"),
			)
			return err
		}},
		{name: "Workspace", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			_, err := store.Workspace(ctx, workspace.ID)
			return err
		}},
		{name: "Workspaces", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			_, err := store.Workspaces(ctx, project.ID)
			return err
		}},
		{name: "UpdateWorkspace", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			replacement := workspace
			replacement.Name = "Updated fault Workspace"
			replacement.Version = 1
			_, err := store.UpdateWorkspace(
				ctx, command("fault-update-workspace", "workspace.update", replacement.ID, 0, `{}`), replacement,
				event("fault-update-workspace-event", "", 1, replacement.ID, 1, "workspace.updated"),
			)
			return err
		}},
		{name: "CreateEpic", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			epic := domain.Epic{ID: "new-epic", ProjectID: project.ID, Key: "EPIC", Title: "New Epic"}
			_, err := store.CreateEpic(
				ctx, command("fault-create-epic", "epic.create", epic.ID, 0, `{}`), epic,
				event("fault-create-epic-event", "", 1, epic.ID, 0, "epic.created"),
			)
			return err
		}},
		{name: "Epic", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			_, err := store.Epic(ctx, "fault-epic")
			return err
		}},
		{name: "Epics", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			_, err := store.Epics(ctx, project.ID)
			return err
		}},
		{name: "UpdateEpic", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			epic := domain.Epic{ID: "fault-epic", ProjectID: project.ID, Key: "EPIC", Title: "Updated Epic", Version: 1}
			_, err := store.UpdateEpic(
				ctx, command("fault-update-epic", "epic.update", epic.ID, 0, `{}`), epic,
				event("fault-update-epic-event", "", 2, epic.ID, 1, "epic.updated"),
			)
			return err
		}},
		{name: "CreateTask", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			_, err := store.CreateTask(
				ctx,
				command("fault-create-task", "task.create", "new-task", 0, `{"title":"New"}`),
				domain.Task{
					ID: "new-task", ProjectID: project.ID, Title: "New task",
					Objective: "Create", AcceptanceCriteria: "Created",
					WorkspaceIDs: []string{workspace.ID},
				},
				event("fault-create-task-event", "", 1, "new-task", 0, "task.created"),
			)
			return err
		}},
		{name: "GrantDependencyOverride", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			grant := domain.HumanDependencyOverrideGrant{
				ID: "fault-override", TaskID: task.ID, ExpectedTaskVersion: task.Version,
				Dependency: domain.PlanningDependency{
					From: domain.PlanningNodeRef{Kind: domain.PlanningNodeTask, ID: task.ID},
					On:   domain.PlanningNodeRef{Kind: domain.PlanningNodeEpic, ID: "fault-epic"},
				},
				HumanActorID: "human-fault", AuditID: "audit-fault",
			}
			_, err := store.GrantDependencyOverride(
				ctx, typedCommand(t, "fault-grant-override", "task.dependency_override.grant", grant.ID, 0, grant), grant,
			)
			return err
		}},
		{name: "DependencyOverride", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			_, err := store.DependencyOverride(ctx, "fault-override")
			return err
		}},
		{name: "DependencyOverrides", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			_, err := store.DependencyOverrides(ctx, project.ID)
			return err
		}},
		{name: "Task", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			_, err := store.Task(ctx, task.ID)
			return err
		}},
		{name: "Tasks", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			_, err := store.Tasks(ctx, project.ID)
			return err
		}},
		{name: "UpdateTask", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			replacement := task
			replacement.Title = "Updated fault task"
			replacement.Version = 1
			_, err := store.UpdateTask(
				ctx,
				command("fault-update-task", "task.update", task.ID, 0, `{"title":"Updated"}`),
				replacement,
				event("fault-update-task-event", run.ID, 3, task.ID, 1, "task.updated"),
			)
			return err
		}},
		{name: "CreateRun", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			_, err := store.CreateRun(
				ctx,
				command("fault-create-run", "run.create", "new-run", 0, `{"number":2}`),
				domain.Run{ID: "new-run", TaskID: task.ID, Number: 2, BaseSHA: baseSHA},
				event("fault-create-run-event", "new-run", 1, "new-run", 0, "run.created"),
			)
			return err
		}},
		{name: "Run", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			_, err := store.Run(ctx, run.ID)
			return err
		}},
		{name: "Runs", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			_, err := store.Runs(ctx, task.ID)
			return err
		}},
		{name: "UpdateRun", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			replacement := run
			replacement.Version = 2
			_, err := store.UpdateRun(
				ctx,
				command("fault-update-run", "run.update", run.ID, 1, `{"state":"updated"}`),
				replacement,
				event("fault-update-run-event", run.ID, 3, run.ID, 2, "run.updated"),
			)
			return err
		}},
		{name: "AppendCandidate", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			_, err := store.AppendCandidate(
				ctx,
				command("fault-append-candidate", "candidate.append", run.ID, 1, `{"candidateId":"new-candidate"}`),
				storedCandidate("new-candidate", run.ID, 2, "abcdef0123456789abcdef0123456789abcdef01"),
				event("fault-append-candidate-event", run.ID, 3, run.ID, 2, "candidate.appended"),
			)
			return err
		}},
		{name: "Candidate", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			_, err := store.Candidate(ctx, "fault-candidate")
			return err
		}},
		{name: "Candidates", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			_, err := store.Candidates(ctx, run.ID)
			return err
		}},
		{name: "Command", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			_, err := store.Command(ctx, "fault-project-create")
			return err
		}},
		{name: "Events", invoke: func(ctx context.Context, store storeport.TaskStore) error {
			_, err := store.Events(ctx, domain.EventQuery{Limit: 20})
			return err
		}},
	}
}

func seedFaultStore(t *testing.T, store storeport.TaskStore) {
	t.Helper()
	ctx := context.Background()
	project := faultProject("fault-project", "Fault project")
	result, err := store.CreateProject(
		ctx,
		command("fault-project-create", "project.create", project.ID, 0, `{"name":"Fault project"}`),
		project,
		[]domain.Workspace{faultWorkspace(t, project.ID)},
		event("fault-project-create-event", "", 1, project.ID, 0, "project.created"),
	)
	requireApplied(t, result, err)
	task := domain.Task{
		ID: "fault-task", ProjectID: project.ID, Title: "Fault task",
		Objective: "Exercise failures", AcceptanceCriteria: "Every error is typed",
		WorkspaceIDs: []string{domain.WorkspaceID(project.ID, "workspace-"+project.ID)},
	}
	result, err = store.CreateTask(
		ctx,
		command("fault-task-create", "task.create", task.ID, 0, `{"title":"Fault task"}`),
		task,
		event("fault-task-create-event", "", 1, task.ID, 0, "task.created"),
	)
	requireApplied(t, result, err)
	run := domain.Run{ID: "fault-run", TaskID: task.ID, Number: 1, BaseSHA: baseSHA}
	result, err = store.CreateRun(
		ctx,
		command("fault-run-create", "run.create", run.ID, 0, `{"number":1}`),
		run,
		event("fault-run-create-event", run.ID, 1, run.ID, 0, "run.created"),
	)
	requireApplied(t, result, err)
	result, err = store.AppendCandidate(
		ctx,
		command("fault-candidate-create", "candidate.append", run.ID, 0, `{"candidateId":"fault-candidate"}`),
		storedCandidate("fault-candidate", run.ID, 1, "abcdef0123456789abcdef0123456789abcdef01"),
		event("fault-candidate-create-event", run.ID, 2, run.ID, 1, "candidate.appended"),
	)
	requireApplied(t, result, err)
}

func requireRedactedPortFailure(t *testing.T, err error, forbidden ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("port operation unexpectedly succeeded")
	}
	var health *storeport.HealthError
	var schema *storeport.SchemaError
	if !errors.As(err, &health) && !errors.As(err, &schema) {
		t.Fatalf("port error has no typed redacted classification: %T %q", err, err)
	}
	message := strings.ToLower(err.Error())
	for _, text := range append(forbidden,
		"access denied", "dial tcp", "connection refused", "table not found",
		"error 1045", "error 1146", "sqlstate", "select ", "insert ", "update ",
	) {
		if text != "" && strings.Contains(message, strings.ToLower(text)) {
			t.Fatalf("port error leaked backend text %q: %q", text, err)
		}
	}
	if len(err.Error()) > 96 {
		t.Fatalf("port error exceeded bounded diagnostic size: %q", err)
	}
}

func requireStoredRecordHealth(t *testing.T, err error) {
	t.Helper()
	var failure *storeport.HealthError
	if !errors.As(err, &failure) || failure.Code != storeport.HealthStoredRecordInvalid {
		t.Fatalf("stored-state error = %T %v", err, err)
	}
	if err.Error() != "taskstore unhealthy: STORED_RECORD_INVALID" {
		t.Fatalf("stored-state error was not closed and redacted: %q", err.Error())
	}
}

func rootDatabase(t *testing.T, fixture *doltFixture, database string) *sql.DB {
	t.Helper()
	connector, err := mysql.NewConnector(&mysql.Config{
		User: "root", Net: "tcp", Addr: fixture.address, DBName: database,
		AllowNativePasswords: true,
	})
	if err != nil {
		t.Fatalf("create test inspection connector: %v", err)
	}
	return sql.OpenDB(connector)
}

func openDatabaseStore(t *testing.T, fixture *doltFixture, database string) *dolt.DoltTaskStore {
	t.Helper()
	store, err := dolt.Open(fixtureDatabaseConfig(fixture, database, faultStoreID))
	if err != nil {
		t.Fatalf("open database TaskStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestEveryPortMethodRedactsWrongCredentials(t *testing.T) {
	fixture := startDoltFixture(t)
	store := openContractStore(t, fixture, faultStoreID, true)
	seedFaultStore(t, store)
	if err := store.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}
	const password = "review-secret-password-must-not-escape"
	badEndpoint := dolt.Endpoint{
		Address: fixture.address, Database: fixture.database,
		User: "missing-director-user", Password: password,
	}
	badStore, err := dolt.Open(dolt.Config{
		Control: badEndpoint, Writer: badEndpoint, StoreID: faultStoreID,
	})
	if err != nil {
		t.Fatalf("construct wrong-credential store: %v", err)
	}
	defer badStore.Close()
	for _, call := range faultPortCalls(t) {
		t.Run(call.name, func(t *testing.T) {
			requireRedactedPortFailure(t, call.invoke(context.Background(), badStore), password, badEndpoint.User)
		})
	}
	t.Run("Bootstrap", func(t *testing.T) {
		requireRedactedPortFailure(t, badStore.Bootstrap(context.Background()), password, badEndpoint.User)
	})
}

func TestEveryPortMethodRedactsBackendShutdown(t *testing.T) {
	fixture := startDoltFixture(t)
	store := openContractStore(t, fixture, faultStoreID, true)
	defer store.Close()
	seedFaultStore(t, store)
	stopDoltFixture(t, fixture, syscall.SIGKILL)
	for _, call := range faultPortCalls(t) {
		t.Run(call.name, func(t *testing.T) {
			requireRedactedPortFailure(t, call.invoke(context.Background(), store), fixture.address)
		})
	}
	t.Run("Bootstrap", func(t *testing.T) {
		requireRedactedPortFailure(t, store.Bootstrap(context.Background()), fixture.address)
	})
}

func createFaultBackup(t *testing.T, fixture *doltFixture) string {
	t.Helper()
	backupPath := filepath.Join(fixture.root, "fault-backup")
	if err := os.Mkdir(backupPath, 0o700); err != nil {
		t.Fatalf("create fault backup: %v", err)
	}
	backupURL := "file://" + filepath.ToSlash(backupPath)
	inspection := rootDatabase(t, fixture, fixture.database)
	defer inspection.Close()
	if _, err := inspection.ExecContext(context.Background(), `CALL DOLT_BACKUP('sync-url', ?)`, backupURL); err != nil {
		t.Fatalf("create fault backup: %v", err)
	}
	return backupURL
}

func restoreFaultDatabase(t *testing.T, fixture *doltFixture, backupURL, database string) *sql.DB {
	t.Helper()
	source := rootDatabase(t, fixture, fixture.database)
	if _, err := source.ExecContext(
		context.Background(), `CALL DOLT_BACKUP('restore', ?, ?)`, backupURL, database,
	); err != nil {
		source.Close()
		t.Fatalf("restore fault database %s: %v", database, err)
	}
	source.Close()
	return rootDatabase(t, fixture, database)
}

func dropTable(t *testing.T, database *sql.DB, table string) {
	t.Helper()
	connection, err := database.Conn(context.Background())
	if err != nil {
		t.Fatalf("open table-drop connection: %v", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(context.Background(), `SET @@SESSION.foreign_key_checks = 0`); err != nil {
		t.Fatalf("disable foreign keys for table-drop fixture: %v", err)
	}
	statement := "DROP TABLE `" + table + "`"
	if _, err := connection.ExecContext(context.Background(), statement); err != nil {
		t.Fatalf("drop fixture table %s: %v", table, err)
	}
}

func dropTrigger(t *testing.T, database *sql.DB, trigger string) {
	t.Helper()
	statement := "DROP TRIGGER `" + trigger + "`"
	if _, err := database.ExecContext(context.Background(), statement); err != nil {
		t.Fatalf("drop fixture trigger %s: %v", trigger, err)
	}
}

func TestEveryPortMethodRedactsMissingTables(t *testing.T) {
	fixture := startDoltFixture(t)
	seed := openContractStore(t, fixture, faultStoreID, true)
	seedFaultStore(t, seed)
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}
	backupURL := createFaultBackup(t, fixture)
	calls := make(map[string]portCall)
	for _, call := range faultPortCalls(t) {
		calls[call.name] = call
	}
	groups := []struct {
		table   string
		methods []string
	}{
		{table: "schema_metadata", methods: []string{"SchemaVersion"}},
		{table: "aggregates", methods: []string{"Project", "Projects", "Task", "Tasks", "Run", "Runs"}},
		{table: "candidates", methods: []string{"Candidate", "Candidates"}},
		{table: "command_requests", methods: []string{
			"CreateProject", "UpdateProject", "CreateTask", "UpdateTask", "CreateRun",
			"UpdateRun", "AppendCandidate", "Command",
		}},
		{table: "events", methods: []string{"Events"}},
	}
	for index, group := range groups {
		t.Run(group.table, func(t *testing.T) {
			databaseName := fmt.Sprintf("missing_%02d", index)
			database := restoreFaultDatabase(t, fixture, backupURL, databaseName)
			dropTable(t, database, group.table)
			database.Close()
			store := openDatabaseStore(t, fixture, databaseName)
			for _, method := range group.methods {
				requireRedactedPortFailure(t, calls[method].invoke(context.Background(), store), group.table)
			}
			requireRedactedPortFailure(t, store.Bootstrap(context.Background()), group.table)
			if !errors.Is(store.Bootstrap(context.Background()), storeport.ErrSchemaVersion) {
				t.Fatal("partial schema Bootstrap did not retain ErrSchemaVersion classification")
			}
		})
	}
}

func TestBootstrapRejectsEveryIncompleteTableSet(t *testing.T) {
	fixture := startDoltFixture(t)
	seed := openContractStore(t, fixture, faultStoreID, true)
	seedFaultStore(t, seed)
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}
	backupURL := createFaultBackup(t, fixture)
	expectedTables := []string{
		"aggregates", "aggregates_identity", "candidates", "candidates_identity",
		"command_outcomes", "command_outcomes_identity", "command_requests",
		"command_requests_identity", "event_stream_lock", "events", "events_identity",
		"guard_constants", "immutable_write_guard", "parent_guard", "schema_metadata",
		"taskstore_identity",
	}
	for index, table := range expectedTables {
		t.Run(table, func(t *testing.T) {
			databaseName := fmt.Sprintf("partial_%02d", index)
			database := restoreFaultDatabase(t, fixture, backupURL, databaseName)
			dropTable(t, database, table)
			database.Close()
			store := openDatabaseStore(t, fixture, databaseName)
			err := store.Bootstrap(context.Background())
			if !errors.Is(err, storeport.ErrSchemaVersion) {
				t.Fatalf("Bootstrap accepted missing table %s: %v", table, err)
			}
			var failure *storeport.SchemaError
			if !errors.As(err, &failure) || failure.Code != storeport.SchemaTableSetMismatch {
				t.Fatalf("missing table %s has wrong schema code: %T %v", table, err, err)
			}
		})
	}

	foreign := restoreFaultDatabase(t, fixture, backupURL, "partial_foreign")
	if _, err := foreign.ExecContext(context.Background(), `CREATE TABLE foreign_table (id INT PRIMARY KEY)`); err != nil {
		foreign.Close()
		t.Fatalf("create foreign table: %v", err)
	}
	foreign.Close()
	foreignStore := openDatabaseStore(t, fixture, "partial_foreign")
	if err := foreignStore.Bootstrap(context.Background()); !errors.Is(err, storeport.ErrSchemaVersion) {
		t.Fatalf("Bootstrap accepted foreign table: %v", err)
	}
}

func TestBootstrapRejectsEveryIncompleteTriggerSet(t *testing.T) {
	fixture := startDoltFixture(t)
	seed := openContractStore(t, fixture, faultStoreID, true)
	seedFaultStore(t, seed)
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}
	backupURL := createFaultBackup(t, fixture)
	expectedTriggers := []string{
		"aggregates_append_only_identity",
		"aggregates_reject_identity_update",
		"candidates_append_only_identity",
		"candidates_reject_delete",
		"candidates_reject_update",
		"command_outcomes_append_only_identity",
		"command_outcomes_reject_delete",
		"command_outcomes_reject_update",
		"command_requests_append_only_identity",
		"command_requests_reject_delete",
		"command_requests_reject_update",
		"events_append_only_identity",
		"events_reject_delete",
		"events_reject_update",
		"fk_aggregate_parent_present",
		"fk_aggregate_parent_present_on_update",
		"fk_candidate_run_present",
		"fk_command_outcome_event_present",
		"fk_command_request_aggregate_present",
		"fk_event_aggregate_present",
		"fk_event_run_present",
	}
	for index, trigger := range expectedTriggers {
		t.Run(trigger, func(t *testing.T) {
			databaseName := fmt.Sprintf("trigger_%02d", index)
			database := restoreFaultDatabase(t, fixture, backupURL, databaseName)
			dropTrigger(t, database, trigger)
			database.Close()
			store := openDatabaseStore(t, fixture, databaseName)
			err := store.Bootstrap(context.Background())
			if !errors.Is(err, storeport.ErrSchemaVersion) {
				t.Fatalf("Bootstrap accepted missing trigger %s: %v", trigger, err)
			}
			var failure *storeport.SchemaError
			if !errors.As(err, &failure) || failure.Code != storeport.SchemaTriggerSetMismatch {
				t.Fatalf("missing trigger %s has wrong schema code: %T %v", trigger, err, err)
			}
		})
	}

	for index, replacement := range []struct {
		name      string
		statement string
	}{
		{
			name: "candidates_reject_update",
			statement: `CREATE TRIGGER candidates_reject_update BEFORE UPDATE ON candidates FOR EACH ROW
				INSERT INTO immutable_write_guard (singleton)
				SELECT singleton FROM guard_constants WHERE 1 = 0`,
		},
		{
			name: "events_reject_delete",
			statement: `CREATE TRIGGER events_reject_delete BEFORE DELETE ON command_outcomes FOR EACH ROW
				INSERT INTO immutable_write_guard (singleton) VALUES (1)`,
		},
	} {
		t.Run("neutered_"+replacement.name, func(t *testing.T) {
			databaseName := fmt.Sprintf("neutered_%02d", index)
			database := restoreFaultDatabase(t, fixture, backupURL, databaseName)
			dropTrigger(t, database, replacement.name)
			if _, err := database.ExecContext(context.Background(), replacement.statement); err != nil {
				database.Close()
				t.Fatalf("install same-named changed trigger %s: %v", replacement.name, err)
			}
			database.Close()
			store := openDatabaseStore(t, fixture, databaseName)
			err := store.Bootstrap(context.Background())
			var failure *storeport.SchemaError
			if !errors.As(err, &failure) || failure.Code != storeport.SchemaTriggerSetMismatch {
				t.Fatalf("Bootstrap accepted changed trigger %s: %T %v", replacement.name, err, err)
			}
		})
	}

	extra := restoreFaultDatabase(t, fixture, backupURL, "trigger_foreign")
	if _, err := extra.ExecContext(context.Background(), `
		CREATE TRIGGER foreign_trigger BEFORE INSERT ON schema_metadata FOR EACH ROW
		INSERT INTO immutable_write_guard (singleton) VALUES (1)`); err != nil {
		extra.Close()
		t.Fatalf("create foreign trigger: %v", err)
	}
	extra.Close()
	extraStore := openDatabaseStore(t, fixture, "trigger_foreign")
	err := extraStore.Bootstrap(context.Background())
	var failure *storeport.SchemaError
	if !errors.As(err, &failure) || failure.Code != storeport.SchemaTriggerSetMismatch {
		t.Fatalf("Bootstrap accepted foreign trigger: %T %v", err, err)
	}
}

func TestBootstrapRejectsChangedGuardRows(t *testing.T) {
	fixture := startDoltFixture(t)
	seed := openContractStore(t, fixture, faultStoreID, true)
	seedFaultStore(t, seed)
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}
	backupURL := createFaultBackup(t, fixture)
	faults := []struct {
		name      string
		statement string
	}{
		{name: "update_guard_constants", statement: `UPDATE guard_constants SET singleton = 2 WHERE singleton = 1`},
		{name: "delete_guard_constants", statement: `DELETE FROM guard_constants`},
		{name: "update_parent_guard", statement: `UPDATE parent_guard SET identity = 'guard.parent changed'`},
		{name: "delete_parent_guard", statement: `DELETE FROM parent_guard`},
		{name: "update_immutable_write_guard", statement: `UPDATE immutable_write_guard SET singleton = 2 WHERE singleton = 1`},
		{name: "delete_immutable_write_guard", statement: `DELETE FROM immutable_write_guard`},
	}
	for index, fault := range faults {
		t.Run(fault.name, func(t *testing.T) {
			databaseName := fmt.Sprintf("guard_row_%02d", index)
			database := restoreFaultDatabase(t, fixture, backupURL, databaseName)
			if _, err := database.ExecContext(context.Background(), fault.statement); err != nil {
				database.Close()
				t.Fatalf("apply guard-row fault: %v", err)
			}
			database.Close()
			store := openDatabaseStore(t, fixture, databaseName)
			err := store.Bootstrap(context.Background())
			var failure *storeport.SchemaError
			if !errors.As(err, &failure) || failure.Code != storeport.SchemaGuardRowsMismatch {
				t.Fatalf("Bootstrap accepted changed guard row: %T %v", err, err)
			}
		})
	}
}

func TestSingularAndCollectionReloadsRejectInvalidRecords(t *testing.T) {
	fixture := startDoltFixture(t)
	store := openContractStore(t, fixture, faultStoreID, true)
	t.Cleanup(func() { _ = store.Close() })
	seedFaultStore(t, store)
	inspection := rootDatabase(t, fixture, fixture.database)
	defer inspection.Close()
	ctx := context.Background()
	var validProjectData []byte
	if err := inspection.QueryRowContext(ctx, `SELECT data FROM aggregates WHERE id = 'fault-project'`).Scan(&validProjectData); err != nil {
		t.Fatalf("capture valid Project: %v", err)
	}
	workspaceID := faultWorkspace(t, "fault-project").ID
	var validWorkspaceData []byte
	if err := inspection.QueryRowContext(ctx, `SELECT data FROM aggregates WHERE id = ?`, workspaceID).Scan(&validWorkspaceData); err != nil {
		t.Fatalf("capture valid Workspace: %v", err)
	}

	if _, err := inspection.ExecContext(ctx,
		`UPDATE aggregates SET data = JSON_OBJECT('name', '', 'state', 'bogus') WHERE id = 'fault-project'`,
	); err != nil {
		t.Fatalf("inject invalid Project: %v", err)
	}
	_, singularProject := store.Project(ctx, "fault-project")
	_, collectionProject := store.Projects(ctx)
	for _, err := range []error{singularProject, collectionProject} {
		requireRedactedPortFailure(t, err)
	}
	if _, err := inspection.ExecContext(ctx,
		`UPDATE aggregates SET data = ? WHERE id = 'fault-project'`, validProjectData,
	); err != nil {
		t.Fatalf("restore Project: %v", err)
	}

	if _, err := inspection.ExecContext(ctx,
		`UPDATE aggregates SET data = JSON_OBJECT('key', 'fault', 'name', '', 'repository', JSON_OBJECT()) WHERE id = ?`,
		workspaceID,
	); err != nil {
		t.Fatalf("inject invalid Workspace: %v", err)
	}
	_, singularWorkspace := store.Workspace(ctx, workspaceID)
	_, collectionWorkspace := store.Workspaces(ctx, "fault-project")
	for _, err := range []error{singularWorkspace, collectionWorkspace} {
		requireRedactedPortFailure(t, err)
	}
	if _, err := inspection.ExecContext(ctx,
		`UPDATE aggregates SET data = ? WHERE id = ?`, validWorkspaceData, workspaceID,
	); err != nil {
		t.Fatalf("restore Workspace: %v", err)
	}

	if _, err := inspection.ExecContext(ctx, `UPDATE aggregates SET data = JSON_SET(data, '$.lease', JSON_OBJECT(
		'holderInstance', 'engine-a', 'holderProcessIdentity', 'pid-1:start-1', 'epoch', 0,
		'acquiredAtMillis', 1000, 'renewedAtMillis', 1000, 'expiresAtMillis', 2000,
		'dispatchAllowed', true)) WHERE id = 'fault-project'`); err != nil {
		t.Fatalf("inject invalid Project lease: %v", err)
	}
	_, malformedLease := store.Project(ctx, "fault-project")
	requireRedactedPortFailure(t, malformedLease)
	if _, err := inspection.ExecContext(ctx,
		`UPDATE aggregates SET data = ? WHERE id = 'fault-project'`, validProjectData,
	); err != nil {
		t.Fatalf("restore Project after lease fault: %v", err)
	}

	if _, err := inspection.ExecContext(ctx,
		`UPDATE aggregates SET data = JSON_OBJECT('title', '', 'objective', '', 'acceptanceCriteria', '') WHERE id = 'fault-task'`,
	); err != nil {
		t.Fatalf("inject invalid Task: %v", err)
	}
	_, singularTask := store.Task(ctx, "fault-task")
	_, collectionTask := store.Tasks(ctx, "fault-project")
	for _, err := range []error{singularTask, collectionTask} {
		requireRedactedPortFailure(t, err)
	}
	if _, err := inspection.ExecContext(ctx,
		`UPDATE aggregates SET data = JSON_OBJECT('title', 'Fault task', 'objective', 'Exercise failures', 'acceptanceCriteria', 'Every error is typed') WHERE id = 'fault-task'`,
	); err != nil {
		t.Fatalf("restore Task: %v", err)
	}

	if _, err := inspection.ExecContext(ctx,
		`UPDATE aggregates SET data = JSON_OBJECT('number', 0, 'baseSha', 'not-a-sha') WHERE id = 'fault-run'`,
	); err != nil {
		t.Fatalf("inject invalid Run: %v", err)
	}
	_, singularRun := store.Run(ctx, "fault-run")
	_, collectionRun := store.Runs(ctx, "fault-task")
	for _, err := range []error{singularRun, collectionRun} {
		requireRedactedPortFailure(t, err)
	}
	if _, err := inspection.ExecContext(ctx,
		`UPDATE aggregates SET data = JSON_OBJECT('number', 1, 'baseSha', ?, 'currentCandidateId', 'fault-candidate') WHERE id = 'fault-run'`,
		baseSHA,
	); err != nil {
		t.Fatalf("restore Run: %v", err)
	}

	if _, err := inspection.ExecContext(ctx,
		`INSERT INTO candidates (id, run_id, sequence, commit_sha, data) VALUES ('invalid-candidate', 'fault-run', 99, REPEAT('z', 40), JSON_OBJECT())`,
	); err != nil {
		t.Fatalf("inject invalid Candidate: %v", err)
	}
	_, singularCandidate := store.Candidate(ctx, "invalid-candidate")
	_, collectionCandidate := store.Candidates(ctx, "fault-run")
	for _, err := range []error{singularCandidate, collectionCandidate} {
		requireRedactedPortFailure(t, err)
	}
}

func TestProjectWorkspaceReloadsRejectCompleteSetTampering(t *testing.T) {
	fixture := startDoltFixture(t)
	store := openContractStore(t, fixture, "workspace-reload-tamper-store", true)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	project := faultProject("reload-project", "Reload Project")
	first := testWorkspace(
		t, project.ID, "first", "/srv/workspaces/reload-first",
		"https://github.com/example/reload-first.git",
	)
	second := testWorkspace(
		t, project.ID, "second", "/srv/workspaces/reload-second",
		"https://github.com/example/reload-second.git",
	)
	result, err := store.CreateProject(
		ctx, command("reload-project-create", "project.create", project.ID, 0, `{}`), project,
		[]domain.Workspace{first, second},
		event("reload-project-create-event", "", 1, project.ID, 0, "project.created"),
	)
	requireApplied(t, result, err)
	inspection := rootDatabase(t, fixture, fixture.database)
	defer inspection.Close()
	validSecond := storedWorkspaceJSON(t, second)

	assertAll := func(includeWorkspace bool) {
		_, projectErr := store.Project(ctx, project.ID)
		_, projectsErr := store.Projects(ctx)
		_, workspacesErr := store.Workspaces(ctx, project.ID)
		failures := []error{projectErr, projectsErr, workspacesErr}
		if includeWorkspace {
			_, workspaceErr := store.Workspace(ctx, first.ID)
			failures = append(failures, workspaceErr)
		}
		for _, failure := range failures {
			requireStoredRecordHealth(t, failure)
		}
	}

	tamperSecond := func(name string, replacement domain.Workspace) {
		t.Run(name, func(t *testing.T) {
			if _, err := inspection.ExecContext(ctx,
				`UPDATE aggregates SET data = ? WHERE id = ?`, storedWorkspaceJSON(t, replacement), second.ID,
			); err != nil {
				t.Fatalf("tamper Workspace: %v", err)
			}
			assertAll(true)
			if _, err := inspection.ExecContext(ctx,
				`UPDATE aggregates SET data = ? WHERE id = ?`, validSecond, second.ID,
			); err != nil {
				t.Fatalf("restore Workspace: %v", err)
			}
		})
	}

	duplicateKey := second
	duplicateKey.Key = first.Key
	tamperSecond("duplicate stable key", duplicateKey)
	remoteAlias := second
	alias, err := repositorydomain.CanonicalRemote("git@github.com:example/reload-first.git")
	if err != nil {
		t.Fatal(err)
	}
	remoteAlias.Repository.ID = alias.ID
	remoteAlias.Repository.Key = alias.Key
	remoteAlias.Repository.CanonicalRemote = alias.Canonical
	tamperSecond("duplicate remote alias", remoteAlias)
	sourceIdentity := second
	sourceIdentity.Repository.SourceDevice = first.Repository.SourceDevice
	sourceIdentity.Repository.SourceInode = first.Repository.SourceInode
	tamperSecond("duplicate source identity", sourceIdentity)
	commonIdentity := second
	commonIdentity.Repository.GitCommonDevice = first.Repository.GitCommonDevice
	commonIdentity.Repository.GitCommonInode = first.Repository.GitCommonInode
	tamperSecond("duplicate common-directory identity", commonIdentity)

	t.Run("129 Workspaces", func(t *testing.T) {
		extraIDs := make([]string, 0, 127)
		for index := 0; index < 127; index++ {
			key := fmt.Sprintf("extra-%03d", index)
			workspace := testWorkspace(
				t, project.ID, key, "/srv/workspaces/"+key,
				fmt.Sprintf("https://git-%03d.example/repository.git", index),
			)
			if _, err := inspection.ExecContext(ctx,
				`INSERT INTO aggregates (id, kind, parent_id, version, data) VALUES (?, 'workspace', ?, 0, ?)`,
				workspace.ID, project.ID, storedWorkspaceJSON(t, workspace),
			); err != nil {
				t.Fatalf("insert extra Workspace %d: %v", index, err)
			}
			extraIDs = append(extraIDs, workspace.ID)
		}
		assertAll(true)
		for _, id := range extraIDs {
			if _, err := inspection.ExecContext(ctx, `DELETE FROM aggregates WHERE id = ?`, id); err != nil {
				t.Fatalf("remove extra Workspace: %v", err)
			}
		}
	})

	if workspaces, err := store.Workspaces(ctx, project.ID); err != nil || len(workspaces) != 2 {
		t.Fatalf("restored Workspace set = %#v, %v", workspaces, err)
	}
	t.Run("zero Workspaces", func(t *testing.T) {
		if _, err := inspection.ExecContext(ctx,
			`DELETE FROM aggregates WHERE id IN (?, ?)`, first.ID, second.ID,
		); err != nil {
			t.Fatalf("delete Workspaces: %v", err)
		}
		assertAll(false)
	})
}

func TestPortInputsRejectInvalidRecordsBeforeBackendAccess(t *testing.T) {
	fixture := startDoltFixture(t)
	store := openContractStore(t, fixture, faultStoreID, true)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	invalidEvent := event("invalid-event", "", 1, "invalid", 0, "record.invalid")
	invalidWrites := []struct {
		name string
		err  error
	}{
		{name: "CreateProject", err: func() error {
			_, err := store.CreateProject(ctx, command("invalid-create-project", "project.create", "invalid", 0, `{}`), domain.Project{}, nil, invalidEvent)
			return err
		}()},
		{name: "UpdateProject", err: func() error {
			_, err := store.UpdateProject(ctx, command("invalid-update-project", "project.update", "invalid", 0, `{}`), domain.Project{ID: "invalid", Name: "Invalid", State: "bogus", Version: 1}, invalidEvent)
			return err
		}()},
		{name: "CreateWorkspace", err: func() error {
			_, err := store.CreateWorkspace(ctx, command("invalid-create-workspace", "workspace.create", "invalid", 0, `{}`), domain.Workspace{}, invalidEvent)
			return err
		}()},
		{name: "UpdateWorkspace", err: func() error {
			_, err := store.UpdateWorkspace(ctx, command("invalid-update-workspace", "workspace.update", "invalid", 0, `{}`), domain.Workspace{}, invalidEvent)
			return err
		}()},
		{name: "CreateTask", err: func() error {
			_, err := store.CreateTask(ctx, command("invalid-create-task", "task.create", "invalid", 0, `{}`), domain.Task{}, invalidEvent)
			return err
		}()},
		{name: "UpdateTask", err: func() error {
			_, err := store.UpdateTask(ctx, command("invalid-update-task", "task.update", "invalid", 0, `{}`), domain.Task{}, invalidEvent)
			return err
		}()},
		{name: "CreateRun", err: func() error {
			_, err := store.CreateRun(ctx, command("invalid-create-run", "run.create", "invalid", 0, `{}`), domain.Run{}, invalidEvent)
			return err
		}()},
		{name: "UpdateRun", err: func() error {
			_, err := store.UpdateRun(ctx, command("invalid-update-run", "run.update", "invalid", 0, `{}`), domain.Run{}, invalidEvent)
			return err
		}()},
		{name: "AppendCandidate", err: func() error {
			_, err := store.AppendCandidate(ctx, command("invalid-candidate", "candidate.append", "invalid", 0, `{}`), domain.Candidate{}, invalidEvent)
			return err
		}()},
	}
	for _, invalid := range invalidWrites {
		if !errors.Is(invalid.err, storeport.ErrInvalidRecord) {
			t.Fatalf("%s did not reject invalid record: %v", invalid.name, invalid.err)
		}
	}
	for name, err := range map[string]error{
		"Project":    func() error { _, err := store.Project(ctx, "bad id"); return err }(),
		"Workspace":  func() error { _, err := store.Workspace(ctx, "bad id"); return err }(),
		"Workspaces": func() error { _, err := store.Workspaces(ctx, "bad id"); return err }(),
		"Task":       func() error { _, err := store.Task(ctx, "bad id"); return err }(),
		"Tasks":      func() error { _, err := store.Tasks(ctx, "bad id"); return err }(),
		"Run":        func() error { _, err := store.Run(ctx, "bad id"); return err }(),
		"Runs":       func() error { _, err := store.Runs(ctx, "bad id"); return err }(),
		"Candidate":  func() error { _, err := store.Candidate(ctx, "bad id"); return err }(),
		"Candidates": func() error { _, err := store.Candidates(ctx, "bad id"); return err }(),
		"Command":    func() error { _, err := store.Command(ctx, "bad id"); return err }(),
		"Events": func() error {
			_, err := store.Events(ctx, domain.EventQuery{AggregateID: "bad id"})
			return err
		}(),
	} {
		if !errors.Is(err, storeport.ErrInvalidRecord) {
			t.Fatalf("%s did not reject invalid query input: %v", name, err)
		}
	}
}

func TestHealthErrorsExposeOnlyTypedCodes(t *testing.T) {
	err := storeport.Unhealthy(storeport.HealthQueryFailed)
	if !errors.Is(err, storeport.ErrUnhealthy) {
		t.Fatal("HealthError does not unwrap to ErrUnhealthy")
	}
	var health *storeport.HealthError
	if !errors.As(err, &health) || health.Code != storeport.HealthQueryFailed {
		t.Fatalf("HealthError code is unavailable: %T %v", err, err)
	}
	if strings.Contains(err.Error(), "backend detail") {
		t.Fatal("HealthError exposed an unexpected backend cause")
	}

	schemaErr := storeport.InvalidSchema(storeport.SchemaTableSetMismatch)
	if !errors.Is(schemaErr, storeport.ErrSchemaVersion) {
		t.Fatal("SchemaError does not unwrap to ErrSchemaVersion")
	}
	var schema *storeport.SchemaError
	if !errors.As(schemaErr, &schema) || schema.Code != storeport.SchemaTableSetMismatch {
		t.Fatalf("SchemaError code is unavailable: %T %v", schemaErr, schemaErr)
	}
}

func TestInvalidUnicodeCannotCollideWithCommandIdentity(t *testing.T) {
	fixture := startDoltFixture(t)
	store := openContractStore(t, fixture, faultStoreID, true)
	t.Cleanup(func() { _ = store.Close() })
	seedFaultStore(t, store)
	ctx := context.Background()
	task := domain.Task{
		ID: "fault-task", ProjectID: "fault-project", Title: "Dir�ector",
		Objective: "Exercise failures", AcceptanceCriteria: "Every error is typed",
		WorkspaceIDs: []string{domain.WorkspaceID("fault-project", "workspace-fault-project")},
		Version:      1,
	}
	request := command(
		"unicode-command", "task.update", task.ID, 0,
		`{"title":"Dir�ector"}`,
	)
	result, err := store.UpdateTask(
		ctx, request, task,
		event("unicode-command-event", "fault-run", 3, task.ID, 1, "task.updated"),
	)
	requireApplied(t, result, err)

	notApplied := task
	notApplied.Title = "This value must not be applied"
	loneSurrogate := request
	loneSurrogate.Payload = json.RawMessage(`{"title":"Dir\ud800ector"}`)
	if _, err := store.UpdateTask(
		ctx, loneSurrogate, notApplied,
		event("unicode-surrogate-event", "fault-run", 4, task.ID, 1, "task.updated"),
	); !errors.Is(err, storeport.ErrIdempotencyConflict) {
		t.Fatalf("lone-surrogate key reuse was not an idempotency conflict: %v", err)
	}

	invalidUTF8 := request
	invalidUTF8.Payload = append(json.RawMessage(`{"title":"Dir`), 0xff)
	invalidUTF8.Payload = append(invalidUTF8.Payload, []byte(`ector"}`)...)
	if _, err := store.UpdateTask(
		ctx, invalidUTF8, notApplied,
		event("unicode-utf8-event", "fault-run", 5, task.ID, 1, "task.updated"),
	); !errors.Is(err, storeport.ErrIdempotencyConflict) {
		t.Fatalf("invalid-UTF-8 key reuse was not an idempotency conflict: %v", err)
	}

	newInvalid := loneSurrogate
	newInvalid.IdempotencyKey = "new-invalid-unicode-command"
	if _, err := store.UpdateTask(
		ctx, newInvalid, notApplied,
		event("new-invalid-unicode-event", "fault-run", 6, task.ID, 1, "task.updated"),
	); !errors.Is(err, storeport.ErrInvalidRecord) {
		t.Fatalf("new invalid-Unicode command was not rejected before hashing: %v", err)
	}
	reloaded, err := store.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("reload Task after Unicode collision probes: %v", err)
	}
	if reloaded.Title != task.Title || reloaded.Version != 1 {
		t.Fatalf("invalid Unicode collision mutated Task: %#v", reloaded)
	}
}

func TestRuntimeWriterGrantProtectsGuardRowsAndExcludesDDL(t *testing.T) {
	fixture := startDoltFixture(t)
	bootstrap := openContractStore(t, fixture, faultStoreID, true)
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("close bootstrap store: %v", err)
	}
	const (
		writerUser     = "runtime_writer"
		writerPassword = "test-only-runtime-writer-credential"
	)
	control := rootDatabase(t, fixture, fixture.database)
	if _, err := control.ExecContext(context.Background(), `
		CREATE USER 'runtime_writer'@'%' IDENTIFIED WITH mysql_native_password BY 'test-only-runtime-writer-credential'`); err != nil {
		control.Close()
		t.Fatalf("create restricted runtime writer: %v", err)
	}
	grants := []string{
		fmt.Sprintf("GRANT SELECT ON `%s`.* TO '%s'@'%%'", fixture.database, writerUser),
		fmt.Sprintf("GRANT INSERT, UPDATE ON `%s`.aggregates TO '%s'@'%%'", fixture.database, writerUser),
		fmt.Sprintf("GRANT INSERT ON `%s`.candidates TO '%s'@'%%'", fixture.database, writerUser),
		fmt.Sprintf("GRANT INSERT ON `%s`.command_requests TO '%s'@'%%'", fixture.database, writerUser),
		fmt.Sprintf("GRANT INSERT ON `%s`.command_outcomes TO '%s'@'%%'", fixture.database, writerUser),
		fmt.Sprintf("GRANT INSERT ON `%s`.events TO '%s'@'%%'", fixture.database, writerUser),
		fmt.Sprintf("GRANT UPDATE ON `%s`.event_stream_lock TO '%s'@'%%'", fixture.database, writerUser),
	}
	for _, grant := range grants {
		if _, err := control.ExecContext(context.Background(), grant); err != nil {
			control.Close()
			t.Fatalf("grant restricted runtime writer: %v", err)
		}
	}
	control.Close()

	config := fixtureConfig(fixture, faultStoreID)
	config.Writer.User = writerUser
	config.Writer.Password = writerPassword
	store, err := dolt.Open(config)
	if err != nil {
		t.Fatalf("open restricted-writer store: %v", err)
	}
	defer store.Close()
	project := faultProject("grant-project", "Grant project")
	result, err := store.CreateProject(
		context.Background(),
		command("grant-project-create", "project.create", project.ID, 0, `{}`),
		project,
		[]domain.Workspace{faultWorkspace(t, project.ID)},
		event("grant-project-create-event", "", 1, project.ID, 0, "project.created"),
	)
	requireApplied(t, result, err)

	connector, err := mysql.NewConnector(&mysql.Config{
		User: writerUser, Passwd: writerPassword, Net: "tcp",
		Addr: fixture.address, DBName: fixture.database, AllowNativePasswords: true,
	})
	if err != nil {
		t.Fatalf("create restricted writer connector: %v", err)
	}
	writer := sql.OpenDB(connector)
	defer writer.Close()
	guardMutations := []string{
		`UPDATE guard_constants SET singleton = 2 WHERE singleton = 1`,
		`DELETE FROM guard_constants`,
		`UPDATE parent_guard SET identity = 'guard.parent changed'`,
		`DELETE FROM parent_guard`,
		`UPDATE immutable_write_guard SET singleton = 2 WHERE singleton = 1`,
		`DELETE FROM immutable_write_guard`,
	}
	for _, statement := range guardMutations {
		if _, err := writer.ExecContext(context.Background(), statement); err == nil {
			t.Fatalf("runtime writer grant authorized guard-row mutation: %s", statement)
		}
	}
	if _, err := writer.ExecContext(context.Background(), `TRUNCATE TABLE events`); err == nil {
		t.Fatal("runtime writer grant unexpectedly authorized TRUNCATE")
	}
	if _, err := writer.ExecContext(context.Background(), `DROP TABLE events`); err == nil {
		t.Fatal("runtime writer grant unexpectedly authorized DROP")
	}
	if events, err := store.Events(context.Background(), domain.EventQuery{Limit: 10}); err != nil || len(events) != 1 {
		t.Fatalf("denied DDL changed runtime Event history: %#v, %v", events, err)
	}
	if err := store.Bootstrap(context.Background()); err != nil {
		t.Fatalf("denied guard-row mutations changed verified schema: %v", err)
	}
}

func TestRunIdentityFieldsStayConsistent(t *testing.T) {
	fixture := startDoltFixture(t)
	store := openContractStore(t, fixture, faultStoreID, true)
	t.Cleanup(func() { _ = store.Close() })
	seedFaultStore(t, store)
	ctx := context.Background()
	current, err := store.Run(ctx, "fault-run")
	if err != nil {
		t.Fatalf("load current Run: %v", err)
	}

	changedNumber := current
	changedNumber.Number = 2
	changedNumber.Version = 2
	if _, err := store.UpdateRun(
		ctx,
		command("change-run-number", "run.update", current.ID, 1, `{"number":2}`),
		changedNumber,
		event("change-run-number-event", current.ID, 3, current.ID, 2, "run.updated"),
	); !errors.Is(err, storeport.ErrInvalidRecord) {
		t.Fatalf("UpdateRun accepted a changed Run number: %v", err)
	}

	changedBase := current
	changedBase.BaseSHA = "1111111111111111111111111111111111111111"
	changedBase.Version = 2
	if _, err := store.UpdateRun(
		ctx,
		command("change-run-base", "run.update", current.ID, 1, `{"baseSha":"1111111111111111111111111111111111111111"}`),
		changedBase,
		event("change-run-base-event", current.ID, 3, current.ID, 2, "run.updated"),
	); !errors.Is(err, storeport.ErrInvalidRecord) {
		t.Fatalf("UpdateRun accepted a changed Run base SHA: %v", err)
	}

	dangling := current
	dangling.CurrentCandidateID = "candidate-does-not-exist"
	dangling.Version = 2
	if _, err := store.UpdateRun(
		ctx,
		command("dangling-run-candidate", "run.update", current.ID, 1, `{"candidateId":"candidate-does-not-exist"}`),
		dangling,
		event("dangling-run-candidate-event", current.ID, 3, current.ID, 2, "run.updated"),
	); !errors.Is(err, storeport.ErrReferentialIntegrity) {
		t.Fatalf("UpdateRun accepted a missing Candidate: %v", err)
	}

	otherRun := domain.Run{ID: "other-run", TaskID: "fault-task", Number: 2, BaseSHA: baseSHA}
	result, err := store.CreateRun(
		ctx,
		command("other-run-create", "run.create", otherRun.ID, 0, `{"number":2}`),
		otherRun,
		event("other-run-create-event", otherRun.ID, 1, otherRun.ID, 0, "run.created"),
	)
	requireApplied(t, result, err)
	otherCandidate := storedCandidate("other-candidate", otherRun.ID, 1, "fedcba9876543210fedcba9876543210fedcba98")
	result, err = store.AppendCandidate(
		ctx,
		command("other-candidate-create", "candidate.append", otherRun.ID, 0, `{"candidateId":"other-candidate"}`),
		otherCandidate,
		event("other-candidate-create-event", otherRun.ID, 2, otherRun.ID, 1, "candidate.appended"),
	)
	requireApplied(t, result, err)

	foreign := current
	foreign.CurrentCandidateID = otherCandidate.ID
	foreign.Version = 2
	if _, err := store.UpdateRun(
		ctx,
		command("foreign-run-candidate", "run.update", current.ID, 1, `{"candidateId":"other-candidate"}`),
		foreign,
		event("foreign-run-candidate-event", current.ID, 3, current.ID, 2, "run.updated"),
	); !errors.Is(err, storeport.ErrReferentialIntegrity) {
		t.Fatalf("UpdateRun accepted another Run Candidate: %v", err)
	}

	valid := current
	valid.Version = 2
	result, err = store.UpdateRun(
		ctx,
		command("valid-run-update", "run.update", current.ID, 1, `{}`),
		valid,
		event("valid-run-update-event", current.ID, 3, current.ID, 2, "run.updated"),
	)
	requireApplied(t, result, err)
	reloaded, err := store.Run(ctx, current.ID)
	if err != nil {
		t.Fatalf("reload updated Run: %v", err)
	}
	if reloaded.Number != current.Number || reloaded.CurrentCandidateID != current.CurrentCandidateID ||
		reloaded.BaseSHA != current.BaseSHA || reloaded.Version != 2 {
		t.Fatalf("valid Run update changed identity fields: %#v", reloaded)
	}
}

func TestConcurrentRunUpdateCannotRaceBaseSHAImmutability(t *testing.T) {
	fixture := startDoltFixture(t)
	store := openContractStore(t, fixture, faultStoreID, true)
	t.Cleanup(func() { _ = store.Close() })
	seedFaultStore(t, store)
	ctx := context.Background()
	current, err := store.Run(ctx, "fault-run")
	if err != nil {
		t.Fatal(err)
	}
	valid := current
	valid.Version = 2
	changedBase := valid
	changedBase.BaseSHA = strings.Repeat("1", 40)

	type updateResult struct {
		result domain.CommandResult
		err    error
	}
	results := make(chan updateResult, 2)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for _, input := range []struct {
		key string
		run domain.Run
	}{
		{"concurrent-valid-run-update", valid},
		{"concurrent-base-change", changedBase},
	} {
		input := input
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, updateErr := store.UpdateRun(
				ctx,
				command(input.key, "run.update", current.ID, 1, `{}`),
				input.run,
				event(input.key+"-event", current.ID, 3, current.ID, 2, "run.updated"),
			)
			results <- updateResult{result: result, err: updateErr}
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	applied, refused := 0, 0
	for result := range results {
		switch {
		case result.err == nil && result.result.Outcome == domain.CommandApplied:
			applied++
		case errors.Is(result.err, storeport.ErrInvalidRecord):
			refused++
		default:
			t.Fatalf("unexpected concurrent update result: %#v, %v", result.result, result.err)
		}
	}
	if applied != 1 || refused != 1 {
		t.Fatalf("concurrent outcomes applied=%d refused=%d", applied, refused)
	}
	reloaded, err := store.Run(ctx, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.BaseSHA != current.BaseSHA || reloaded.Version != 2 {
		t.Fatalf("concurrent update changed immutable Run base: %#v", reloaded)
	}
}

func TestDuplicateEventAndCommandIdentitiesAreRecordConflicts(t *testing.T) {
	fixture := startDoltFixture(t)
	store := openContractStore(t, fixture, faultStoreID, true)
	t.Cleanup(func() { _ = store.Close() })
	seedFaultStore(t, store)
	ctx := context.Background()
	replacement := faultProject("fault-project", "Must not be applied")
	replacement.Version = 1
	for _, duplicate := range []struct {
		name    string
		command string
		event   domain.Event
	}{
		{
			name: "event_id", command: "duplicate-event-id-command",
			event: event("fault-project-create-event", "", 2, "fault-project", 1, "project.updated"),
		},
		{
			name: "stream_sequence", command: "duplicate-event-sequence-command",
			event: event("new-event-id", "", 1, "fault-project", 1, "project.updated"),
		},
	} {
		t.Run(duplicate.name, func(t *testing.T) {
			_, err := store.UpdateProject(
				ctx,
				command(duplicate.command, "project.update", replacement.ID, 0, `{"name":"Must not be applied"}`),
				replacement,
				duplicate.event,
			)
			if !errors.Is(err, storeport.ErrAlreadyExists) || errors.Is(err, storeport.ErrUnhealthy) {
				t.Fatalf("duplicate Event %s has wrong classification: %v", duplicate.name, err)
			}
			if _, err := store.Command(ctx, duplicate.command); !errors.Is(err, storeport.ErrNotFound) {
				t.Fatalf("duplicate Event left a Command for %s: %v", duplicate.name, err)
			}
		})
	}

	inspection := rootDatabase(t, fixture, fixture.database)
	connection, err := inspection.Conn(ctx)
	if err != nil {
		inspection.Close()
		t.Fatalf("open command identity fixture: %v", err)
	}
	if _, err := connection.ExecContext(ctx, `SET @@SESSION.foreign_key_checks = 0`); err != nil {
		connection.Close()
		inspection.Close()
		t.Fatalf("disable foreign keys for command identity fixture: %v", err)
	}
	for _, table := range []string{"command_outcomes", "command_requests"} {
		if _, err := connection.ExecContext(ctx, "TRUNCATE TABLE "+table); err != nil {
			connection.Close()
			inspection.Close()
			t.Fatalf("truncate %s fixture rows: %v", table, err)
		}
	}
	connection.Close()
	inspection.Close()
	_, err = store.UpdateProject(
		ctx,
		command("fault-project-create", "project.update", replacement.ID, 0, `{"name":"Must not be applied"}`),
		replacement,
		event("command-ledger-conflict-event", "", 2, replacement.ID, 1, "project.updated"),
	)
	if !errors.Is(err, storeport.ErrAlreadyExists) || errors.Is(err, storeport.ErrUnhealthy) {
		t.Fatalf("duplicate Command ledger identity has wrong classification: %v", err)
	}

	project, err := store.Project(ctx, replacement.ID)
	if err != nil {
		t.Fatalf("reload Project after duplicate identity probes: %v", err)
	}
	if project.Name != "Fault project" || project.Version != 0 {
		t.Fatalf("duplicate identity probe mutated Project: %#v", project)
	}
}

func TestRunsUseNumericOrderThenStableIdentity(t *testing.T) {
	fixture := startDoltFixture(t)
	store := openContractStore(t, fixture, faultStoreID, true)
	t.Cleanup(func() { _ = store.Close() })
	seedFaultStore(t, store)
	ctx := context.Background()
	for _, run := range []domain.Run{
		{ID: "run-10", TaskID: "fault-task", Number: 10, BaseSHA: baseSHA},
		{ID: "run-2", TaskID: "fault-task", Number: 2, BaseSHA: baseSHA},
		{ID: "run-2b", TaskID: "fault-task", Number: 2, BaseSHA: baseSHA},
	} {
		result, err := store.CreateRun(
			ctx,
			command(run.ID+"-create", "run.create", run.ID, 0, fmt.Sprintf(`{"number":%d}`, run.Number)),
			run,
			event(run.ID+"-create-event", run.ID, 1, run.ID, 0, "run.created"),
		)
		requireApplied(t, result, err)
	}
	runs, err := store.Runs(ctx, "fault-task")
	if err != nil {
		t.Fatalf("query numerically ordered Runs: %v", err)
	}
	var identities []string
	for _, run := range runs {
		identities = append(identities, run.ID)
	}
	want := []string{"fault-run", "run-2", "run-2b", "run-10"}
	if fmt.Sprint(identities) != fmt.Sprint(want) {
		t.Fatalf("Runs order = %v, want %v", identities, want)
	}
}

func TestInvalidJSONErrorsAreBoundedAndRedacted(t *testing.T) {
	fixture := startDoltFixture(t)
	store := openContractStore(t, fixture, faultStoreID, true)
	t.Cleanup(func() { _ = store.Close() })
	seedFaultStore(t, store)
	const secretMarker = "untrusted-secret-key-material"
	largeKey := secretMarker + strings.Repeat("x", 4096)
	payload := json.RawMessage(fmt.Sprintf(`{%q:1,%q:2}`, largeKey, largeKey))
	task := domain.Task{
		ID: "fault-task", ProjectID: "fault-project", Title: "Must not apply",
		Objective: "Exercise failures", AcceptanceCriteria: "Every error is typed",
		WorkspaceIDs: []string{domain.WorkspaceID("fault-project", "workspace-fault-project")},
		Version:      1,
	}
	_, err := store.UpdateTask(
		context.Background(),
		domain.CommandRequest{
			IdempotencyKey: "large-invalid-json", Type: "task.update",
			AggregateID: task.ID, ExpectedVersion: 0, Payload: payload,
		},
		task,
		event("large-invalid-json-event", "fault-run", 3, task.ID, 1, "task.updated"),
	)
	if !errors.Is(err, storeport.ErrInvalidRecord) {
		t.Fatalf("large duplicate-key payload has wrong classification: %v", err)
	}
	var validation *storeport.ValidationError
	if !errors.As(err, &validation) || validation.Code != storeport.ValidationJSONInvalid {
		t.Fatalf("large duplicate-key payload has no typed validation code: %T %v", err, err)
	}
	if len(err.Error()) > 96 || strings.Contains(err.Error(), secretMarker) || strings.Contains(err.Error(), largeKey) {
		t.Fatalf("invalid-record error exposed untrusted payload: %q", err)
	}
	if _, commandErr := store.Command(context.Background(), "large-invalid-json"); !errors.Is(commandErr, storeport.ErrNotFound) {
		t.Fatalf("invalid JSON persisted a Command: %v", commandErr)
	}
}

func TestFullCapNormalizedPayloadFailsWithinPortBudget(t *testing.T) {
	const payloadLimit = 64 * 1024
	numbers := strings.TrimSuffix(strings.Repeat("1e8191,", 9), ",")
	payload := json.RawMessage("[" + numbers + strings.Repeat(" ", payloadLimit-len(numbers)-2) + "]")
	if len(payload) != payloadLimit {
		t.Fatalf("full-cap fixture is %d bytes, want %d", len(payload), payloadLimit)
	}
	fixture := startDoltFixture(t)
	store := openContractStore(t, fixture, faultStoreID, true)
	t.Cleanup(func() { _ = store.Close() })

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := store.CreateProject(
		context.Background(),
		domain.CommandRequest{
			IdempotencyKey: "bounded-payload", Type: "project.create",
			AggregateID: "bounded-payload", Payload: payload,
		},
		faultProject("bounded-payload", "Bounded payload"),
		[]domain.Workspace{faultWorkspace(t, "bounded-payload")},
		event("bounded-payload-event", "", 1, "bounded-payload", 0, "project.created"),
	)
	runtime.ReadMemStats(&after)
	var validation *storeport.ValidationError
	if !errors.As(err, &validation) || validation.Code != storeport.ValidationPayloadTooLarge {
		t.Fatalf("full-cap expansion has wrong classification: %T %v", err, err)
	}
	if len(err.Error()) > 96 {
		t.Fatalf("full-cap expansion returned unbounded error: %q", err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 32<<20 {
		t.Fatalf("full-cap expansion allocated %d bytes, want at most %d", allocated, 32<<20)
	}
	if _, commandErr := store.Command(context.Background(), "bounded-payload"); !errors.Is(commandErr, storeport.ErrNotFound) {
		t.Fatalf("full-cap expansion persisted a Command: %v", commandErr)
	}
	if _, projectErr := store.Project(context.Background(), "bounded-payload"); !errors.Is(projectErr, storeport.ErrNotFound) {
		t.Fatalf("full-cap expansion persisted a Project: %v", projectErr)
	}
}

func TestWellFormedNumericMagnitudeOverflowIsPayloadTooLarge(t *testing.T) {
	fixture := startDoltFixture(t)
	store := openContractStore(t, fixture, faultStoreID, true)
	t.Cleanup(func() { _ = store.Close() })

	_, err := store.CreateProject(
		context.Background(),
		domain.CommandRequest{
			IdempotencyKey: "numeric-magnitude-overflow", Type: "project.create",
			AggregateID: "numeric-magnitude-overflow", Payload: json.RawMessage(`1e99999999999999999999`),
		},
		faultProject("numeric-magnitude-overflow", "Numeric magnitude overflow"),
		[]domain.Workspace{faultWorkspace(t, "numeric-magnitude-overflow")},
		event("numeric-magnitude-overflow-event", "", 1, "numeric-magnitude-overflow", 0, "project.created"),
	)
	var validation *storeport.ValidationError
	if !errors.As(err, &validation) || validation.Code != storeport.ValidationPayloadTooLarge {
		t.Fatalf("well-formed numeric magnitude overflow has wrong classification: %T %v", err, err)
	}
	if len(err.Error()) > 96 {
		t.Fatalf("numeric magnitude overflow returned unbounded error: %q", err)
	}
	if _, commandErr := store.Command(context.Background(), "numeric-magnitude-overflow"); !errors.Is(commandErr, storeport.ErrNotFound) {
		t.Fatalf("numeric magnitude overflow persisted a Command: %v", commandErr)
	}
	if _, projectErr := store.Project(context.Background(), "numeric-magnitude-overflow"); !errors.Is(projectErr, storeport.ErrNotFound) {
		t.Fatalf("numeric magnitude overflow persisted a Project: %v", projectErr)
	}
}

func TestConcurrentStrictEventResumeObservesEveryCommit(t *testing.T) {
	fixture := startDoltFixture(t)
	store := openContractStore(t, fixture, faultStoreID, true)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	const (
		rounds  = 12
		writers = 8
	)
	observed := make(map[string]uint64, rounds*writers)
	var cursor uint64
	consumeAvailable := func() error {
		for {
			page, err := store.Events(ctx, domain.EventQuery{
				AfterGlobalSequence: cursor,
				Limit:               3,
			})
			if err != nil {
				return err
			}
			if len(page) == 0 {
				return nil
			}
			for _, event := range page {
				if event.GlobalSequence <= cursor {
					return fmt.Errorf("Event cursor did not advance: %d <= %d", event.GlobalSequence, cursor)
				}
				if _, duplicate := observed[event.ID]; duplicate {
					return fmt.Errorf("Event %s was observed twice", event.ID)
				}
				observed[event.ID] = event.GlobalSequence
				cursor = event.GlobalSequence
			}
		}
	}

	for round := 0; round < rounds; round++ {
		start := make(chan struct{})
		results := make(chan error, writers)
		var wait sync.WaitGroup
		for writer := 0; writer < writers; writer++ {
			wait.Add(1)
			go func(writer int) {
				defer wait.Done()
				<-start
				identity := fmt.Sprintf("cursor-%02d-%02d", round, writer)
				_, err := store.CreateProject(
					ctx,
					command("command-"+identity, "project.create", identity, 0, fmt.Sprintf(`{"round":%d,"writer":%d}`, round, writer)),
					faultProject(identity, identity),
					[]domain.Workspace{faultWorkspace(t, identity)},
					event("event-"+identity, "", 1, identity, 0, "project.created"),
				)
				results <- err
			}(writer)
		}
		close(start)
		completed := 0
		for completed < writers {
			select {
			case err := <-results:
				if err != nil {
					t.Fatalf("concurrent Event writer failed: %v", err)
				}
				completed++
				if err := consumeAvailable(); err != nil {
					t.Fatalf("resume Event consumer: %v", err)
				}
			case <-time.After(time.Millisecond):
				if err := consumeAvailable(); err != nil {
					t.Fatalf("poll Event consumer: %v", err)
				}
			}
		}
		wait.Wait()
		if err := consumeAvailable(); err != nil {
			t.Fatalf("drain Event consumer: %v", err)
		}
	}

	allEvents, err := store.Events(ctx, domain.EventQuery{Limit: 1000})
	if err != nil {
		t.Fatalf("load complete durable Event stream: %v", err)
	}
	if len(allEvents) != rounds*writers || len(observed) != len(allEvents) {
		t.Fatalf("strict resume observed %d of %d durable Events", len(observed), len(allEvents))
	}
	for _, event := range allEvents {
		if sequence, ok := observed[event.ID]; !ok || sequence != event.GlobalSequence {
			t.Fatalf("strict resume skipped durable Event %s at %d", event.ID, event.GlobalSequence)
		}
	}
}
