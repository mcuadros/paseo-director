// SPDX-License-Identifier: Apache-2.0

package dolt_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/mcuadros/director-engine/adapters/dolt"
	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/execution"
	storeport "github.com/mcuadros/director-engine/ports/taskstore"
)

const baseSHA = "0123456789abcdef0123456789abcdef01234567"

type doltFixture struct {
	address  string
	database string
	root     string
	command  *exec.Cmd
	waited   chan error
	stopped  bool
}

func stopDoltFixture(t *testing.T, fixture *doltFixture, signal os.Signal) {
	t.Helper()
	if fixture.stopped || fixture.command.Process == nil {
		return
	}
	if err := fixture.command.Process.Signal(signal); err != nil {
		t.Fatalf("signal Dolt server: %v", err)
	}
	select {
	case <-fixture.waited:
		fixture.stopped = true
	case <-time.After(10 * time.Second):
		_ = fixture.command.Process.Kill()
		<-fixture.waited
		fixture.stopped = true
	}
}

func reservePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve Dolt port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release Dolt port: %v", err)
	}
	return port
}

func doltEnvironment(clientRoot string) []string {
	return append(os.Environ(),
		"DOLT_ROOT_PATH="+clientRoot,
		"DOLT_DISABLE_EVENT_FLUSH=1",
	)
}

func runDolt(t *testing.T, clientRoot, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("dolt", arguments...)
	command.Dir = directory
	command.Env = doltEnvironment(clientRoot)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("dolt %s failed: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func startDoltFixture(t *testing.T) *doltFixture {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the selected TaskStore topology supports Linux only")
	}
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Fatalf("Dolt 2.3.2 is required by the Linux TaskStore contract: %v", err)
	}
	versionRoot := t.TempDir()
	versionConfig := filepath.Join(versionRoot, ".dolt")
	if err := os.Mkdir(versionConfig, 0o700); err != nil {
		t.Fatalf("create version-probe configuration: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(versionConfig, "config_global.json"),
		[]byte("{\n  \"metrics.disabled\": \"true\"\n}\n"), 0o600,
	); err != nil {
		t.Fatalf("disable Dolt metrics for version probe: %v", err)
	}
	version := runDolt(t, versionRoot, ".", "version")
	if !strings.Contains(version, "dolt version 2.3.2") {
		t.Fatalf("TaskStore contract requires Dolt 2.3.2, observed %q", strings.TrimSpace(version))
	}

	root := t.TempDir()
	clientRoot := filepath.Join(root, "client")
	configDirectory := filepath.Join(clientRoot, ".dolt")
	if err := os.MkdirAll(configDirectory, 0o700); err != nil {
		t.Fatalf("create Dolt client configuration: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(configDirectory, "config_global.json"),
		[]byte("{\n  \"metrics.disabled\": \"true\"\n}\n"), 0o600,
	); err != nil {
		t.Fatalf("disable Dolt metrics: %v", err)
	}
	database := "taskstore_contract"
	databaseDirectory := filepath.Join(root, database)
	if err := os.Mkdir(databaseDirectory, 0o700); err != nil {
		t.Fatalf("create Dolt database: %v", err)
	}
	runDolt(t, clientRoot, databaseDirectory,
		"init", "--name=Director contract", "--email=contract@example.invalid",
	)

	port := reservePort(t)
	configuration := filepath.Join(root, "server.yaml")
	configRoot := filepath.Join(root, ".doltcfg")
	if err := os.Mkdir(configRoot, 0o700); err != nil {
		t.Fatalf("create server config root: %v", err)
	}
	serverConfig := fmt.Sprintf(`log_level: warning
data_dir: %q
cfg_dir: %q
behavior:
  read_only: false
listener:
  host: 127.0.0.1
  port: %d
system_variables:
  dolt_force_transaction_commit: 0
`, root, configRoot, port)
	if err := os.WriteFile(configuration, []byte(serverConfig), 0o600); err != nil {
		t.Fatalf("write Dolt server configuration: %v", err)
	}
	command := exec.Command("dolt", "sql-server", "--config="+configuration)
	command.Dir = root
	command.Env = doltEnvironment(clientRoot)
	var output strings.Builder
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatalf("start Dolt server: %v", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	fixture := &doltFixture{
		address: fmt.Sprintf("127.0.0.1:%d", port), database: database,
		root: root, command: command, waited: waited,
	}
	t.Cleanup(func() {
		stopDoltFixture(t, fixture, syscall.SIGTERM)
	})

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", fixture.address, 100*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return fixture
		}
		select {
		case err := <-waited:
			fixture.stopped = true
			t.Fatalf("Dolt server exited before readiness: %v: %s", err, output.String())
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("Dolt server did not listen: %s", output.String())
	return nil
}

func fixtureConfig(fixture *doltFixture, storeID string) dolt.Config {
	return fixtureDatabaseConfig(fixture, fixture.database, storeID)
}

func fixtureDatabaseConfig(fixture *doltFixture, database, storeID string) dolt.Config {
	endpoint := dolt.Endpoint{
		Address: fixture.address, Database: database, User: "root",
	}
	return dolt.Config{Control: endpoint, Writer: endpoint, StoreID: storeID}
}

func openContractStore(t *testing.T, fixture *doltFixture, storeID string, bootstrap bool) *dolt.DoltTaskStore {
	t.Helper()
	store, err := dolt.Open(fixtureConfig(fixture, storeID))
	if err != nil {
		t.Fatalf("open TaskStore: %v", err)
	}
	if bootstrap {
		if err := store.Bootstrap(context.Background()); err != nil {
			_ = store.Close()
			t.Fatalf("bootstrap TaskStore: %v", err)
		}
	}
	return store
}

func command(key, commandType, aggregateID string, expectedVersion uint64, payload string) domain.CommandRequest {
	return domain.CommandRequest{
		IdempotencyKey: key, Type: commandType, AggregateID: aggregateID,
		ExpectedVersion: expectedVersion, Payload: json.RawMessage(payload),
	}
}

func event(id, runID string, sequence uint64, aggregateID string, version uint64, eventType string) domain.Event {
	return domain.Event{
		ID: id, RunID: runID, Sequence: sequence, AggregateID: aggregateID,
		AggregateVersion: version, Type: eventType,
		Payload: json.RawMessage(`{"source":"contract"}`),
	}
}

func requireApplied(t *testing.T, result domain.CommandResult, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("apply command: %v", err)
	}
	if result.Outcome != domain.CommandApplied || result.Replay || result.EventID == "" {
		t.Fatalf("unexpected applied result: %#v", result)
	}
}

func runPortContract(t *testing.T, store storeport.TaskStore) {
	t.Helper()
	ctx := context.Background()
	project := domain.Project{ID: "project-1", Name: "Director", State: "active"}
	result, err := store.CreateProject(
		ctx,
		command("command-project-create", "project.create", project.ID, 0, `{"name":"Director"}`),
		project,
		event("event-project-create", "", 1, project.ID, 0, "project.created"),
	)
	requireApplied(t, result, err)
	task := domain.Task{
		ID: "task-1", ProjectID: project.ID, Title: "Persist skeleton",
		Objective: "Persist the walking skeleton", AcceptanceCriteria: "Reload exact records",
	}
	result, err = store.CreateTask(
		ctx,
		command("command-task-create", "task.create", task.ID, 0, `{"title":"Persist skeleton"}`),
		task,
		event("event-task-create", "", 1, task.ID, 0, "task.created"),
	)
	requireApplied(t, result, err)
	run := domain.Run{
		ID: "run-1", TaskID: task.ID, Number: 1, BaseSHA: baseSHA,
		Execution: execution.State{
			SchemaVersion: "director.execution/v1",
			Scope: execution.Scope{
				ProjectID: "project-1", WorkspaceID: "workspace-1",
				TaskID: "task-1", RunID: "run-1",
			},
			EligibilityDecisionID: "eligibility-1",
			LifecycleDigest:       strings.Repeat("1", 64),
			Worktree: execution.Effect{
				ID: "worktree-1", Kind: execution.EffectWorktreeCreate,
				Phase: execution.EffectIntentRecorded, AttemptLimit: 2,
			},
		},
	}
	result, err = store.CreateRun(
		ctx,
		command("command-run-create", "run.create", run.ID, 0, `{"number":1}`),
		run,
		event("event-run-create", run.ID, 1, run.ID, 0, "run.created"),
	)
	requireApplied(t, result, err)
	candidate := domain.Candidate{
		ID: "candidate-1", RunID: run.ID, Sequence: 1,
		CommitSHA: "abcdef0123456789abcdef0123456789abcdef01",
	}
	result, err = store.AppendCandidate(
		ctx,
		command("command-candidate-append", "candidate.append", run.ID, 0, `{"candidateId":"candidate-1"}`),
		candidate,
		event("event-candidate-append", run.ID, 2, run.ID, 1, "candidate.appended"),
	)
	requireApplied(t, result, err)

	task.Title = "Persist and reload skeleton"
	task.Attention = &execution.NeedsYou{
		Code:              execution.NeedOperationalFactMissing,
		WakeCondition:     "fresh_operational_observation",
		CleanupAuthorized: false,
	}
	task.Version = 1
	updateCommand := command(
		"command-task-update", "task.update", task.ID, 0,
		`{"title":"Persist and reload skeleton","reason":"contract"}`,
	)
	updateEvent := event("event-task-update", run.ID, 3, task.ID, 1, "task.updated")
	result, err = store.UpdateTask(ctx, updateCommand, task, updateEvent)
	requireApplied(t, result, err)

	replayCommand := updateCommand
	replayCommand.Payload = json.RawMessage(`{"reason":"contract","title":"Persist and reload skeleton"}`)
	replay, err := store.UpdateTask(ctx, replayCommand, task, updateEvent)
	if err != nil || !replay.Replay || replay.Outcome != domain.CommandApplied || replay.ObservedVersion != 1 {
		t.Fatalf("same canonical command did not replay: %#v, %v", replay, err)
	}
	conflictingReplay := replayCommand
	conflictingReplay.Payload = json.RawMessage(`{"reason":"different","title":"Persist and reload skeleton"}`)
	if _, err := store.UpdateTask(ctx, conflictingReplay, task, updateEvent); !errors.Is(err, storeport.ErrIdempotencyConflict) {
		t.Fatalf("different idempotency payload was not rejected: %v", err)
	}

	staleTask := task
	staleTask.Title = "Stale replacement"
	stale := command("command-task-stale", "task.update", task.ID, 0, `{"title":"Stale replacement"}`)
	staleResult, err := store.UpdateTask(
		ctx, stale, staleTask, event("event-task-stale", run.ID, 6, task.ID, 1, "task.updated"),
	)
	if err != nil || staleResult.Outcome != domain.CommandRejectedVersionConflict ||
		staleResult.ObservedVersion != 1 || staleResult.EventID != "" {
		t.Fatalf("stale version was not durably rejected: %#v, %v", staleResult, err)
	}
	staleReplay, err := store.UpdateTask(
		ctx, stale, staleTask, event("event-task-stale", run.ID, 6, task.ID, 1, "task.updated"),
	)
	if err != nil || !staleReplay.Replay || staleReplay.Outcome != domain.CommandRejectedVersionConflict {
		t.Fatalf("stale outcome did not replay: %#v, %v", staleReplay, err)
	}

	start := make(chan struct{})
	results := make(chan domain.CommandResult, 2)
	errorsChannel := make(chan error, 2)
	var wait sync.WaitGroup
	for index, title := range []string{"Concurrent A", "Concurrent B"} {
		wait.Add(1)
		go func(index int, title string) {
			defer wait.Done()
			<-start
			replacement := task
			replacement.Title = title
			replacement.Version = 2
			result, err := store.UpdateTask(
				ctx,
				command(fmt.Sprintf("command-task-concurrent-%d", index), "task.update", task.ID, 1, fmt.Sprintf(`{"title":%q}`, title)),
				replacement,
				event(fmt.Sprintf("event-task-concurrent-%d", index), run.ID, uint64(4+index), task.ID, 2, "task.updated"),
			)
			results <- result
			errorsChannel <- err
		}(index, title)
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("concurrent expected-version write failed: %v", err)
		}
	}
	var outcomes []domain.CommandOutcome
	for result := range results {
		outcomes = append(outcomes, result.Outcome)
	}
	sort.Slice(outcomes, func(left, right int) bool { return outcomes[left] < outcomes[right] })
	if fmt.Sprint(outcomes) != fmt.Sprint([]domain.CommandOutcome{
		domain.CommandApplied, domain.CommandRejectedVersionConflict,
	}) {
		t.Fatalf("same-version writers did not produce one winner: %v", outcomes)
	}
}

func TestDoltStoreContract(t *testing.T) {
	fixture := startDoltFixture(t)
	const storeID = "director-contract-store"
	store := openContractStore(t, fixture, storeID, true)
	if version, err := store.SchemaVersion(context.Background()); err != nil || version != storeport.SchemaVersion {
		t.Fatalf("unexpected schema version: %d, %v", version, err)
	}
	runPortContract(t, store)
	if err := store.Close(); err != nil {
		t.Fatalf("close first TaskStore adapter: %v", err)
	}

	reloaded := openContractStore(t, fixture, storeID, true)
	t.Cleanup(func() { _ = reloaded.Close() })
	ctx := context.Background()
	project, err := reloaded.Project(ctx, "project-1")
	if err != nil || project.Name != "Director" || project.Version != 0 {
		t.Fatalf("Project did not reload: %#v, %v", project, err)
	}
	task, err := reloaded.Task(ctx, "task-1")
	if err != nil || task.Version != 2 || !strings.HasPrefix(task.Title, "Concurrent ") ||
		task.Attention == nil || task.Attention.Code != execution.NeedOperationalFactMissing ||
		task.Attention.CleanupAuthorized {
		t.Fatalf("Task did not reload at winning version: %#v, %v", task, err)
	}
	run, err := reloaded.Run(ctx, "run-1")
	if err != nil || run.Version != 1 || run.CurrentCandidateID != "candidate-1" ||
		run.Execution.EligibilityDecisionID != "eligibility-1" ||
		run.Execution.Worktree.ID != "worktree-1" {
		t.Fatalf("Run/Candidate projection did not reload: %#v, %v", run, err)
	}
	if projects, err := reloaded.Projects(ctx); err != nil || len(projects) != 1 {
		t.Fatalf("Project query failed: %#v, %v", projects, err)
	}
	if tasks, err := reloaded.Tasks(ctx, project.ID); err != nil || len(tasks) != 1 {
		t.Fatalf("Task query failed: %#v, %v", tasks, err)
	}
	if runs, err := reloaded.Runs(ctx, task.ID); err != nil || len(runs) != 1 {
		t.Fatalf("Run query failed: %#v, %v", runs, err)
	}
	if candidates, err := reloaded.Candidates(ctx, run.ID); err != nil || len(candidates) != 1 || candidates[0].CommitSHA == "" {
		t.Fatalf("Candidate query failed: %#v, %v", candidates, err)
	}
	storedCommand, err := reloaded.Command(ctx, "command-task-stale")
	if err != nil || storedCommand.Outcome != domain.CommandRejectedVersionConflict || storedCommand.EventID != "" {
		t.Fatalf("Command query failed: %#v, %v", storedCommand, err)
	}
	events, err := reloaded.Events(ctx, domain.EventQuery{RunID: run.ID, Limit: 20})
	if err != nil || len(events) != 4 {
		t.Fatalf("Run Event query failed: %#v, %v", events, err)
	}
	resumed, err := reloaded.Events(ctx, domain.EventQuery{
		AfterGlobalSequence: events[0].GlobalSequence, Limit: 20,
	})
	if err != nil || len(resumed) != 3 {
		t.Fatalf("resumable Event query failed: %#v, %v", resumed, err)
	}
	allEvents, err := reloaded.Events(ctx, domain.EventQuery{Limit: 1000})
	if err != nil || len(allEvents) == 0 {
		t.Fatalf("complete Event query failed: %#v, %v", allEvents, err)
	}
	cursor, err := reloaded.LatestEventSequence(ctx)
	if err != nil || cursor != allEvents[len(allEvents)-1].GlobalSequence {
		t.Fatalf("latest Event cursor = %d, %v", cursor, err)
	}
	wrongIdentity := openContractStore(t, fixture, "another-store", false)
	defer wrongIdentity.Close()
	if _, err := wrongIdentity.SchemaVersion(ctx); !errors.Is(err, storeport.ErrUnhealthy) {
		t.Fatalf("listener identity drift did not fail closed: %v", err)
	}
}

func TestDoltSchemaGuardsAndExternalInspection(t *testing.T) {
	fixture := startDoltFixture(t)
	store := openContractStore(t, fixture, "director-guard-store", true)
	t.Cleanup(func() { _ = store.Close() })
	runPortContract(t, store)

	connector, err := mysql.NewConnector(&mysql.Config{
		User: "root", Net: "tcp", Addr: fixture.address, DBName: fixture.database,
		AllowNativePasswords: true,
	})
	if err != nil {
		t.Fatalf("create inspection connector: %v", err)
	}
	inspection := sql.OpenDB(connector)
	defer inspection.Close()
	ctx := context.Background()
	var aggregateCount, commandCount, eventCount int
	if err := inspection.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM aggregates),
			(SELECT COUNT(*) FROM command_requests),
			(SELECT COUNT(*) FROM events)`,
	).Scan(&aggregateCount, &commandCount, &eventCount); err != nil {
		t.Fatalf("inspect plain TaskStore tables: %v", err)
	}
	if aggregateCount != 3 || commandCount != 8 || eventCount != 6 {
		t.Fatalf("unexpected externally inspectable counts: aggregates=%d commands=%d events=%d", aggregateCount, commandCount, eventCount)
	}
	if _, err := inspection.ExecContext(ctx,
		`UPDATE candidates SET commit_sha = ? WHERE id = ?`, baseSHA, "candidate-1",
	); err == nil {
		t.Fatal("immutable Candidate update unexpectedly succeeded")
	}
	connection, err := inspection.Conn(ctx)
	if err != nil {
		t.Fatalf("open guard probe connection: %v", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `SET @@SESSION.foreign_key_checks = 0`); err != nil {
		t.Fatalf("disable foreign key checks for guard probe: %v", err)
	}
	if _, err := connection.ExecContext(ctx,
		`INSERT INTO candidates (id, run_id, sequence, commit_sha) VALUES (?, ?, ?, ?)`,
		"candidate-orphan", "run-missing", 99, baseSHA,
	); err == nil {
		t.Fatal("append-only parent guard accepted an orphan Candidate")
	}
	backupPath := filepath.Join(fixture.root, "external-backup")
	if err := os.Mkdir(backupPath, 0o700); err != nil {
		t.Fatalf("create external backup destination: %v", err)
	}
	backupURL := "file://" + filepath.ToSlash(backupPath)
	if _, err := inspection.ExecContext(ctx, `CALL DOLT_BACKUP('sync-url', ?)`, backupURL); err != nil {
		t.Fatalf("create externally compatible online backup: %v", err)
	}
	const restoredDatabase = "taskstore_restored"
	if _, err := inspection.ExecContext(ctx, `CALL DOLT_BACKUP('restore', ?, ?)`, backupURL, restoredDatabase); err != nil {
		t.Fatalf("restore externally compatible backup: %v", err)
	}
	restoredConnector, err := mysql.NewConnector(&mysql.Config{
		User: "root", Net: "tcp", Addr: fixture.address, DBName: restoredDatabase,
		AllowNativePasswords: true,
	})
	if err != nil {
		t.Fatalf("create restored-store connector: %v", err)
	}
	restored := sql.OpenDB(restoredConnector)
	defer restored.Close()
	var restoredVersion, restoredTasks int
	if err := restored.QueryRowContext(ctx, `
		SELECT
			(SELECT schema_version FROM schema_metadata WHERE singleton = 1),
			(SELECT COUNT(*) FROM aggregates WHERE kind = 'task')`,
	).Scan(&restoredVersion, &restoredTasks); err != nil {
		t.Fatalf("inspect restored external backup: %v", err)
	}
	if restoredVersion != storeport.SchemaVersion || restoredTasks != 1 {
		t.Fatalf("restored backup changed schema/data: version=%d tasks=%d", restoredVersion, restoredTasks)
	}
	if _, err := inspection.ExecContext(ctx, `UPDATE schema_metadata SET schema_version = 2 WHERE singleton = 1`); err != nil {
		t.Fatalf("inject schema-version drift: %v", err)
	}
	if _, err := store.SchemaVersion(ctx); !errors.Is(err, storeport.ErrSchemaVersion) {
		t.Fatalf("schema-version drift did not fail closed: %v", err)
	}
}
