// SPDX-License-Identifier: Apache-2.0

package dolt_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
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
	repositorydomain "github.com/mcuadros/director-engine/domain/repository"
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

func typedCommand(t *testing.T, key, commandType, aggregateID string, expectedVersion uint64, payload any) domain.CommandRequest {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return domain.CommandRequest{
		IdempotencyKey: key, Type: commandType, AggregateID: aggregateID,
		ExpectedVersion: expectedVersion, Payload: encoded,
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

func testOrganizer(projectID string) *domain.Organizer {
	return &domain.Organizer{
		ID: domain.OrganizerID(projectID), Mode: domain.OrganizerModeAdopt,
		Phase: domain.OrganizerPhaseActive, RepositoryPath: "/srv/organizers/" + projectID,
		PreviewID: "preview-identity", OperationID: "operation-identity",
		HumanActorID: "human:test", ConfigurationSHA256: strings.Repeat("a", 64),
		OrganizerRevision: strings.Repeat("b", 40),
	}
}

func testWorkspace(t *testing.T, projectID, workspaceID, sourcePath, remote string) domain.Workspace {
	t.Helper()
	identity, err := repositorydomain.CanonicalRemote(remote)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(projectID + "\x1f" + workspaceID))
	sourceInode := binary.BigEndian.Uint64(digest[:8]) | 1
	commonInode := binary.BigEndian.Uint64(digest[8:16]) | 1
	return domain.Workspace{
		ID: domain.WorkspaceID(projectID, workspaceID), ProjectID: projectID,
		Key: workspaceID, Name: "Workspace " + workspaceID,
		Repository: domain.RepositoryIdentity{
			ID: identity.ID, Key: identity.Key, CanonicalRemote: identity.Canonical,
			SourcePath: sourcePath, SourceDevice: 1, SourceInode: sourceInode,
			GitCommonDirectory: sourcePath + "/.git", GitCommonDevice: 1, GitCommonInode: commonInode,
		},
		DefaultBaseBranch: "main",
		Policy:            domain.WorkspacePolicy{LaunchPolicy: "inherit", DeliveryMode: "inherit"},
	}
}

func storedWorkspaceJSON(t *testing.T, workspace domain.Workspace) []byte {
	t.Helper()
	encoded, err := json.Marshal(struct {
		Key               string                    `json:"key"`
		Name              string                    `json:"name"`
		Repository        domain.RepositoryIdentity `json:"repository"`
		DefaultBaseBranch string                    `json:"defaultBaseBranch"`
		Policy            domain.WorkspacePolicy    `json:"policy"`
	}{workspace.Key, workspace.Name, workspace.Repository, workspace.DefaultBaseBranch, workspace.Policy})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func validObservationInputForStore() domain.ProjectLeaseObservationInput {
	return domain.ProjectLeaseObservationInput{
		AdapterKind: "linux-process-supervisor", AdapterVersion: "v1",
		FactHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
}

func runPortContract(t *testing.T, store storeport.TaskStore) {
	t.Helper()
	ctx := context.Background()
	project := domain.Project{
		ID: "project-1", Name: "Director", State: "active", Organizer: testOrganizer("project-1"),
	}
	workspace := testWorkspace(
		t, project.ID, "workspace-1", "/srv/workspaces/project-1", "https://github.com/example/project-1.git",
	)
	result, err := store.CreateProject(
		ctx,
		command("command-project-create", "project.create", project.ID, 0, `{"name":"Director"}`),
		project,
		[]domain.Workspace{workspace},
		event("event-project-create", "", 1, project.ID, 0, "project.created"),
	)
	requireApplied(t, result, err)
	workspace.Name = "Renamed product repository"
	workspace.Repository.SourcePath = "/srv/moved/project-1"
	workspace.Repository.GitCommonDirectory = "/srv/moved/project-1/.git"
	alias, err := repositorydomain.CanonicalRemote("git@github.com:example/project-1.git")
	if err != nil {
		t.Fatal(err)
	}
	workspace.Repository.CanonicalRemote = alias.Canonical
	workspace.Version = 1
	workspaceUpdate := command(
		"command-workspace-update", "workspace.update", workspace.ID, 0,
		`{"name":"Renamed product repository","sourcePath":"/srv/moved/project-1"}`,
	)
	result, err = store.UpdateWorkspace(
		ctx, workspaceUpdate, workspace,
		event("event-workspace-update", "", 1, workspace.ID, 1, "workspace.updated"),
	)
	requireApplied(t, result, err)
	replayedWorkspace, err := store.UpdateWorkspace(
		ctx, workspaceUpdate, workspace,
		event("event-workspace-update", "", 1, workspace.ID, 1, "workspace.updated"),
	)
	if err != nil || !replayedWorkspace.Replay || replayedWorkspace.ObservedVersion != 1 {
		t.Fatalf("Workspace update did not replay: %#v, %v", replayedWorkspace, err)
	}
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
				ProjectID: "project-1", WorkspaceID: workspace.ID,
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
	if err != nil || project.Name != "Director" || project.Version != 0 || project.Organizer == nil ||
		project.Organizer.ID != domain.OrganizerID(project.ID) {
		t.Fatalf("Project did not reload: %#v, %v", project, err)
	}
	workspace, err := reloaded.Workspace(ctx, domain.WorkspaceID("project-1", "workspace-1"))
	if err != nil || workspace.Version != 1 || workspace.Name != "Renamed product repository" ||
		workspace.Repository.SourcePath != "/srv/moved/project-1" ||
		workspace.Repository.Key != "github.com/example/project-1" {
		t.Fatalf("Workspace did not reload with stable identity: %#v, %v", workspace, err)
	}
	workspaces, err := reloaded.Workspaces(ctx, project.ID)
	if err != nil || len(workspaces) != 1 || workspaces[0].ID != workspace.ID {
		t.Fatalf("Project Workspace mapping did not reload: %#v, %v", workspaces, err)
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

func TestDoltProjectLeaseUsesStoreClockAndImmutableObservation(t *testing.T) {
	fixture := startDoltFixture(t)
	const storeID = "project-lease-store"
	store := openContractStore(t, fixture, storeID, true)
	ctx := context.Background()
	nowMillis := int64(1_000)
	dolt.UseTransactionTimestampForTest(store, &nowMillis)
	project := domain.Project{
		ID: "lease-project", Name: "Lease Project", State: "active", Organizer: testOrganizer("lease-project"),
	}
	workspace := testWorkspace(
		t, project.ID, "product", "/srv/workspaces/lease-project",
		"https://github.com/example/lease-project.git",
	)
	result, err := store.CreateProject(
		ctx, command("lease-project-create", "project.create", project.ID, 0, `{}`), project,
		[]domain.Workspace{workspace},
		event("lease-project-created-event", "", 1, project.ID, 0, "project.created"),
	)
	requireApplied(t, result, err)

	acquire := domain.ProjectLeaseMutation{
		Kind: domain.ProjectLeaseAcquire, ProjectID: project.ID,
		HolderInstance: "engine-a", HolderProcessIdentity: "pid-100:start-1", DurationMillis: 1_000,
	}
	forged := typedCommand(
		t, "lease-clock-forgery", "project.lease.acquire", project.ID, 0,
		map[string]any{
			"kind": acquire.Kind, "projectId": project.ID, "expectedLeaseEpoch": 0,
			"holderInstance": acquire.HolderInstance, "holderProcessIdentity": acquire.HolderProcessIdentity,
			"durationMillis": 1_000, "taskStoreNowMillis": 9_999_999_999,
		},
	)
	if _, err := store.ApplyProjectLease(ctx, forged, acquire); !errors.Is(err, storeport.ErrInvalidRecord) {
		t.Fatalf("forged clock payload error = %v", err)
	}
	if _, err := store.Command(ctx, forged.IdempotencyKey); !errors.Is(err, storeport.ErrNotFound) {
		t.Fatalf("forged clock command persisted: %v", err)
	}

	acquireCommand := typedCommand(t, "lease-acquire-request-001", "project.lease.acquire", project.ID, 0, acquire)
	result, err = store.ApplyProjectLease(ctx, acquireCommand, acquire)
	requireApplied(t, result, err)
	project, err = store.Project(ctx, project.ID)
	if err != nil || project.Lease == nil || project.Lease.AcquiredAtMillis != 1_000 ||
		project.Lease.ExpiresAtMillis != 2_000 || !project.Lease.DispatchAllowed || project.LastLeaseEpoch != 1 {
		t.Fatalf("stored authoritative lease = %#v, %v", project.Lease, err)
	}

	takeover := domain.ProjectLeaseMutation{
		Kind: domain.ProjectLeaseTakeover, ProjectID: project.ID, ExpectedLeaseEpoch: 1,
		HolderInstance: "engine-b", HolderProcessIdentity: "pid-200:start-2", DurationMillis: 100_000,
	}
	nowMillis = 1_999
	preExpiry := typedCommand(t, "lease-takeover-too-early", "project.lease.takeover", project.ID, 1, takeover)
	if _, err := store.ApplyProjectLease(ctx, preExpiry, takeover); !errors.Is(err, domain.ErrLeaseHeld) {
		t.Fatalf("pre-expiry takeover error = %v", err)
	}
	if _, err := store.Command(ctx, preExpiry.IdempotencyKey); !errors.Is(err, storeport.ErrNotFound) {
		t.Fatalf("pre-expiry refusal persisted as success: %v", err)
	}

	nowMillis = 2_000
	takeoverCommand := typedCommand(t, "lease-takeover-exact-001", "project.lease.takeover", project.ID, 1, takeover)
	result, err = store.ApplyProjectLease(ctx, takeoverCommand, takeover)
	requireApplied(t, result, err)
	project, err = store.Project(ctx, project.ID)
	if err != nil || project.Lease == nil || project.Lease.Epoch != 2 || project.Lease.DispatchAllowed ||
		project.Lease.AcquiredAtMillis != 2_000 || project.LastLeaseEpoch != 2 {
		t.Fatalf("exact-expiry takeover = %#v, %v", project, err)
	}

	observationInput := domain.ProjectLeaseObservationInput{
		AdapterKind: "linux-process-supervisor", AdapterVersion: "v1",
		FactHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	observationPayload := struct {
		ProjectID string                              `json:"projectId"`
		Input     domain.ProjectLeaseObservationInput `json:"input"`
	}{project.ID, observationInput}
	observationCommand := typedCommand(
		t, "lease-observe-request-001", "project.lease.observe_takeover",
		project.ID, 2, observationPayload,
	)
	fabricatedEvidenceCommand := typedCommand(
		t, "lease-observe-fabricated", "project.lease.observe_takeover", project.ID, 2,
		map[string]any{
			"projectId": project.ID,
			"input": map[string]any{
				"adapterKind": observationInput.AdapterKind, "adapterVersion": observationInput.AdapterVersion,
				"factHash": observationInput.FactHash, "priorProcessAbsent": true,
				"dispatchChildrenAbsent": true, "fullReconciliation": true, "oneDaemonIdentity": true,
			},
		},
	)
	if _, err := store.RecordProjectLeaseObservation(
		ctx, fabricatedEvidenceCommand, project.ID, observationInput,
	); !errors.Is(err, storeport.ErrInvalidRecord) {
		t.Fatalf("fabricated authority fields error = %v", err)
	}
	if _, err := store.Command(ctx, fabricatedEvidenceCommand.IdempotencyKey); !errors.Is(err, storeport.ErrNotFound) {
		t.Fatalf("fabricated authority command persisted: %v", err)
	}

	interruptedContext, cancelObservation := context.WithCancel(ctx)
	cancelObservation()
	if _, err := store.RecordProjectLeaseObservation(
		interruptedContext, observationCommand, project.ID, observationInput,
	); err == nil {
		t.Fatal("interrupted observation unexpectedly succeeded")
	}
	if _, err := store.Command(ctx, observationCommand.IdempotencyKey); !errors.Is(err, storeport.ErrNotFound) {
		t.Fatalf("interrupted observation command persisted: %v", err)
	}
	project, err = store.Project(ctx, project.ID)
	if err != nil || project.Version != 2 || project.LeaseObservation != nil {
		t.Fatalf("interrupted observation changed Project = %#v, %v", project, err)
	}

	result, err = store.RecordProjectLeaseObservation(ctx, observationCommand, project.ID, observationInput)
	requireApplied(t, result, err)
	project, err = store.Project(ctx, project.ID)
	if err != nil || project.Version != 3 || project.LeaseObservation == nil ||
		project.LeaseObservation.RecordedAtMillis != 2_000 ||
		project.LeaseObservation.MaximumAgeMillis != domain.ProjectLeaseObservationMaximumAgeMillis {
		t.Fatalf("stored observation = %#v, %v", project.LeaseObservation, err)
	}
	events, err := store.Events(ctx, domain.EventQuery{AggregateID: project.ID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var immutableObservation domain.ProjectLeaseObservation
	for _, current := range events {
		if current.ID == "event-"+observationCommand.IdempotencyKey {
			if err := json.Unmarshal(current.Payload, &immutableObservation); err != nil {
				t.Fatal(err)
			}
		}
	}
	if immutableObservation != *project.LeaseObservation {
		t.Fatalf("immutable observation event = %#v, want %#v", immutableObservation, *project.LeaseObservation)
	}

	fabricatedPayload := struct {
		ProjectID     string `json:"projectId"`
		ObservationID string `json:"observationId"`
	}{project.ID, "fabricated-observation"}
	fabricatedCommand := typedCommand(
		t, "lease-enable-fabricated", "project.lease.enable_dispatch",
		project.ID, 3, fabricatedPayload,
	)
	if _, err := store.EnableProjectLeaseDispatch(
		ctx, fabricatedCommand, project.ID, fabricatedPayload.ObservationID,
	); !errors.Is(err, domain.ErrLeaseProofInvalid) {
		t.Fatalf("fabricated observation error = %v", err)
	}
	if _, err := store.Command(ctx, fabricatedCommand.IdempotencyKey); !errors.Is(err, storeport.ErrNotFound) {
		t.Fatalf("fabricated observation command persisted: %v", err)
	}

	enablePayload := struct {
		ProjectID     string `json:"projectId"`
		ObservationID string `json:"observationId"`
	}{project.ID, project.LeaseObservation.ID}
	nowMillis = project.LeaseObservation.RecordedAtMillis + domain.ProjectLeaseObservationMaximumAgeMillis + 1
	staleObservationCommand := typedCommand(
		t, "lease-enable-stale-observation", "project.lease.enable_dispatch",
		project.ID, 3, enablePayload,
	)
	if _, err := store.EnableProjectLeaseDispatch(
		ctx, staleObservationCommand, project.ID, enablePayload.ObservationID,
	); !errors.Is(err, domain.ErrLeaseProofInvalid) {
		t.Fatalf("stale observation error = %v", err)
	}

	nowMillis = 2_100
	enableCommand := typedCommand(
		t, "lease-enable-request-001", "project.lease.enable_dispatch",
		project.ID, 3, enablePayload,
	)
	result, err = store.EnableProjectLeaseDispatch(ctx, enableCommand, project.ID, enablePayload.ObservationID)
	requireApplied(t, result, err)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reloaded := openContractStore(t, fixture, storeID, true)
	dolt.UseTransactionTimestampForTest(reloaded, &nowMillis)
	t.Cleanup(func() { _ = reloaded.Close() })
	replay, err := reloaded.EnableProjectLeaseDispatch(ctx, enableCommand, project.ID, enablePayload.ObservationID)
	if err != nil || !replay.Replay || replay.Outcome != domain.CommandApplied {
		t.Fatalf("enable replay after reopen = %#v, %v", replay, err)
	}
	observationReplay, err := reloaded.RecordProjectLeaseObservation(
		ctx, observationCommand, project.ID, observationInput,
	)
	if err != nil || !observationReplay.Replay || observationReplay.Outcome != domain.CommandApplied {
		t.Fatalf("observation replay after reopen = %#v, %v", observationReplay, err)
	}
	project, err = reloaded.Project(ctx, project.ID)
	if err != nil || project.Version != 4 || project.Lease == nil || !project.Lease.DispatchAllowed ||
		project.LeaseObservation == nil || project.Lease.TakeoverObservationID != project.LeaseObservation.ID ||
		project.LastLeaseEpoch != 2 {
		t.Fatalf("reopened consumed observation = %#v, %v", project, err)
	}
	nowMillis = 2_500
	release := domain.ProjectLeaseMutation{
		Kind: domain.ProjectLeaseRelease, ProjectID: project.ID, ExpectedLeaseEpoch: 2,
		HolderInstance: project.Lease.HolderInstance, HolderProcessIdentity: project.Lease.HolderProcessIdentity,
	}
	releaseCommand := typedCommand(t, "lease-release-request-001", "project.lease.release", project.ID, 4, release)
	result, err = reloaded.ApplyProjectLease(ctx, releaseCommand, release)
	requireApplied(t, result, err)
	reacquire := domain.ProjectLeaseMutation{
		Kind: domain.ProjectLeaseAcquire, ProjectID: project.ID, ExpectedLeaseEpoch: 2,
		HolderInstance: "engine-b", HolderProcessIdentity: "pid-200:start-2", DurationMillis: 10_000,
	}
	reacquireCommand := typedCommand(t, "lease-reacquire-request-01", "project.lease.acquire", project.ID, 5, reacquire)
	result, err = reloaded.ApplyProjectLease(ctx, reacquireCommand, reacquire)
	requireApplied(t, result, err)
	oldObservationCommand := typedCommand(
		t, "lease-old-observation-001", "project.lease.enable_dispatch", project.ID, 6, enablePayload,
	)
	if _, err := reloaded.EnableProjectLeaseDispatch(
		ctx, oldObservationCommand, project.ID, enablePayload.ObservationID,
	); !errors.Is(err, domain.ErrLeaseProofInvalid) {
		t.Fatalf("prior epoch observation error = %v", err)
	}
	if _, err := reloaded.Command(ctx, oldObservationCommand.IdempotencyKey); !errors.Is(err, storeport.ErrNotFound) {
		t.Fatalf("prior epoch observation command persisted: %v", err)
	}
	project, err = reloaded.Project(ctx, project.ID)
	if err != nil || project.Version != 6 || project.Lease == nil || project.Lease.Epoch != 3 ||
		project.LastLeaseEpoch != 3 || !project.Lease.DispatchAllowed || project.LeaseObservation != nil {
		t.Fatalf("reacquired Project = %#v, %v", project, err)
	}
}

func TestDoltProjectLeaseEpochSurvivesReleaseRestartAndConcurrentReacquire(t *testing.T) {
	fixture := startDoltFixture(t)
	const storeID = "project-lease-monotonic-store"
	store := openContractStore(t, fixture, storeID, true)
	ctx := context.Background()
	nowMillis := int64(1_000)
	dolt.UseTransactionTimestampForTest(store, &nowMillis)
	project := domain.Project{
		ID: "monotonic-project", Name: "Monotonic Project", State: "active", Organizer: testOrganizer("monotonic-project"),
	}
	workspace := testWorkspace(
		t, project.ID, "product", "/srv/workspaces/monotonic-project",
		"https://github.com/example/monotonic-project.git",
	)
	result, err := store.CreateProject(
		ctx, command("monotonic-project-create", "project.create", project.ID, 0, `{}`), project,
		[]domain.Workspace{workspace}, event("monotonic-project-created", "", 1, project.ID, 0, "project.created"),
	)
	requireApplied(t, result, err)

	acquire := domain.ProjectLeaseMutation{
		Kind: domain.ProjectLeaseAcquire, ProjectID: project.ID, ExpectedLeaseEpoch: 0,
		HolderInstance: "engine-a", HolderProcessIdentity: "pid-100:start-1", DurationMillis: 10_000,
	}
	acquireCommand := typedCommand(t, "monotonic-acquire-001", "project.lease.acquire", project.ID, 0, acquire)
	result, err = store.ApplyProjectLease(ctx, acquireCommand, acquire)
	requireApplied(t, result, err)

	nowMillis = 2_000
	release := domain.ProjectLeaseMutation{
		Kind: domain.ProjectLeaseRelease, ProjectID: project.ID, ExpectedLeaseEpoch: 1,
		HolderInstance: "engine-a", HolderProcessIdentity: "pid-100:start-1",
	}
	releaseCommand := typedCommand(t, "monotonic-release-001", "project.lease.release", project.ID, 1, release)
	interrupted, cancelRelease := context.WithCancel(ctx)
	cancelRelease()
	if _, err := store.ApplyProjectLease(interrupted, releaseCommand, release); err == nil {
		t.Fatal("interrupted release unexpectedly succeeded")
	}
	if _, err := store.Command(ctx, releaseCommand.IdempotencyKey); !errors.Is(err, storeport.ErrNotFound) {
		t.Fatalf("interrupted release command persisted: %v", err)
	}
	project, err = store.Project(ctx, project.ID)
	if err != nil || project.Version != 1 || project.Lease == nil || project.Lease.Epoch != 1 || project.LastLeaseEpoch != 1 {
		t.Fatalf("interrupted release changed Project = %#v, %v", project, err)
	}
	result, err = store.ApplyProjectLease(ctx, releaseCommand, release)
	requireApplied(t, result, err)
	project, err = store.Project(ctx, project.ID)
	if err != nil || project.Version != 2 || project.Lease != nil || project.LastLeaseEpoch != 1 {
		t.Fatalf("released Project = %#v, %v", project, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := openContractStore(t, fixture, storeID, true)
	dolt.UseTransactionTimestampForTest(restarted, &nowMillis)
	project, err = restarted.Project(ctx, project.ID)
	if err != nil || project.Version != 2 || project.Lease != nil || project.LastLeaseEpoch != 1 {
		t.Fatalf("reopened release tombstone = %#v, %v", project, err)
	}

	mutations := []domain.ProjectLeaseMutation{
		{Kind: domain.ProjectLeaseAcquire, ProjectID: project.ID, ExpectedLeaseEpoch: 1, HolderInstance: "engine-b", HolderProcessIdentity: "pid-200:start-2", DurationMillis: 10_000},
		{Kind: domain.ProjectLeaseAcquire, ProjectID: project.ID, ExpectedLeaseEpoch: 1, HolderInstance: "engine-c", HolderProcessIdentity: "pid-300:start-3", DurationMillis: 10_000},
	}
	commands := []domain.CommandRequest{
		typedCommand(t, "monotonic-reacquire-b", "project.lease.acquire", project.ID, 2, mutations[0]),
		typedCommand(t, "monotonic-reacquire-c", "project.lease.acquire", project.ID, 2, mutations[1]),
	}
	type attempt struct {
		result domain.CommandResult
		err    error
	}
	attempts := make([]attempt, len(commands))
	var wait sync.WaitGroup
	for index := range commands {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			attempts[index].result, attempts[index].err = restarted.ApplyProjectLease(ctx, commands[index], mutations[index])
		}()
	}
	wait.Wait()
	outcomes := []domain.CommandOutcome{attempts[0].result.Outcome, attempts[1].result.Outcome}
	sort.Slice(outcomes, func(left, right int) bool { return outcomes[left] < outcomes[right] })
	if attempts[0].err != nil || attempts[1].err != nil || len(outcomes) != 2 ||
		outcomes[0] != domain.CommandApplied || outcomes[1] != domain.CommandRejectedVersionConflict {
		t.Fatalf("concurrent reacquire attempts = %#v outcomes=%v", attempts, outcomes)
	}
	project, err = restarted.Project(ctx, project.ID)
	if err != nil || project.Version != 3 || project.Lease == nil || project.Lease.Epoch != 2 ||
		project.LastLeaseEpoch != 2 || !project.Lease.DispatchAllowed {
		t.Fatalf("concurrent reacquire Project = %#v, %v", project, err)
	}
	staleRenew := domain.ProjectLeaseMutation{
		Kind: domain.ProjectLeaseRenew, ProjectID: project.ID, ExpectedLeaseEpoch: 1,
		HolderInstance: "engine-a", HolderProcessIdentity: "pid-100:start-1", DurationMillis: 10_000,
	}
	staleCommand := typedCommand(t, "monotonic-stale-epoch", "project.lease.renew", project.ID, 3, staleRenew)
	if _, err := restarted.ApplyProjectLease(ctx, staleCommand, staleRenew); !errors.Is(err, domain.ErrLeaseExpired) {
		t.Fatalf("stale epoch renew error = %v", err)
	}
	if _, err := restarted.Command(ctx, staleCommand.IdempotencyKey); !errors.Is(err, storeport.ErrNotFound) {
		t.Fatalf("stale epoch command persisted: %v", err)
	}
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}

	final := openContractStore(t, fixture, storeID, true)
	dolt.UseTransactionTimestampForTest(final, &nowMillis)
	t.Cleanup(func() { _ = final.Close() })
	for index := range commands {
		replay, err := final.ApplyProjectLease(ctx, commands[index], mutations[index])
		if err != nil || !replay.Replay || replay.Outcome != attempts[index].result.Outcome {
			t.Fatalf("reacquire replay %d = %#v, %v", index, replay, err)
		}
	}
	releaseReplay, err := final.ApplyProjectLease(ctx, releaseCommand, release)
	if err != nil || !releaseReplay.Replay || releaseReplay.Outcome != domain.CommandApplied {
		t.Fatalf("release replay after reopen = %#v, %v", releaseReplay, err)
	}
	project, err = final.Project(ctx, project.ID)
	if err != nil || project.Lease == nil || project.Lease.Epoch != 2 || project.LastLeaseEpoch != 2 {
		t.Fatalf("final monotonic Project = %#v, %v", project, err)
	}
}

func TestDoltProjectLeaseEpochOverflowFailsClosedAcrossReopen(t *testing.T) {
	fixture := startDoltFixture(t)
	const storeID = "project-lease-overflow-store"
	store := openContractStore(t, fixture, storeID, true)
	ctx := context.Background()
	nowMillis := int64(1_000)
	dolt.UseTransactionTimestampForTest(store, &nowMillis)
	project := domain.Project{
		ID: "overflow-project", Name: "Overflow Project", State: "active", Organizer: testOrganizer("overflow-project"),
	}
	workspace := testWorkspace(
		t, project.ID, "product", "/srv/workspaces/overflow-project",
		"https://github.com/example/overflow-project.git",
	)
	preallocated := project
	preallocated.LastLeaseEpoch = 1
	preallocatedCommand := command("overflow-preallocated-create", "project.create", project.ID, 0, `{}`)
	if _, err := store.CreateProject(
		ctx, preallocatedCommand, preallocated, []domain.Workspace{workspace},
		event("overflow-preallocated-event", "", 1, project.ID, 0, "project.created"),
	); !errors.Is(err, storeport.ErrInvalidRecord) {
		t.Fatalf("caller-preallocated epoch error = %v", err)
	}
	if _, err := store.Command(ctx, preallocatedCommand.IdempotencyKey); !errors.Is(err, storeport.ErrNotFound) {
		t.Fatalf("caller-preallocated epoch command persisted: %v", err)
	}
	result, err := store.CreateProject(
		ctx, command("overflow-project-create", "project.create", project.ID, 0, `{}`), project,
		[]domain.Workspace{workspace}, event("overflow-project-created", "", 1, project.ID, 0, "project.created"),
	)
	requireApplied(t, result, err)
	inspection := rootDatabase(t, fixture, fixture.database)
	var rawData []byte
	if err := inspection.QueryRowContext(ctx, `SELECT data FROM aggregates WHERE id = ?`, project.ID).Scan(&rawData); err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(rawData, &document); err != nil {
		t.Fatal(err)
	}
	document["lastLeaseEpoch"] = json.RawMessage(`18446744073709551615`)
	rawData, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inspection.ExecContext(ctx, `UPDATE aggregates SET data = ? WHERE id = ?`, rawData, project.ID); err != nil {
		t.Fatal(err)
	}
	inspection.Close()
	project, err = store.Project(ctx, project.ID)
	if err != nil || project.Lease != nil || project.LastLeaseEpoch != ^uint64(0) {
		t.Fatalf("maximum lease epoch Project = %#v, %v", project, err)
	}
	mutation := domain.ProjectLeaseMutation{
		Kind: domain.ProjectLeaseAcquire, ProjectID: project.ID, ExpectedLeaseEpoch: ^uint64(0),
		HolderInstance: "engine-overflow", HolderProcessIdentity: "pid-max:start-max", DurationMillis: 1_000,
	}
	request := typedCommand(t, "overflow-acquire-request", "project.lease.acquire", project.ID, 0, mutation)
	if _, err := store.ApplyProjectLease(ctx, request, mutation); !errors.Is(err, domain.ErrLeaseTransitionInvalid) {
		t.Fatalf("maximum lease epoch acquire error = %v", err)
	}
	if _, err := store.Command(ctx, request.IdempotencyKey); !errors.Is(err, storeport.ErrNotFound) {
		t.Fatalf("overflow command persisted: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openContractStore(t, fixture, storeID, true)
	t.Cleanup(func() { _ = reopened.Close() })
	project, err = reopened.Project(ctx, project.ID)
	if err != nil || project.Lease != nil || project.LastLeaseEpoch != ^uint64(0) {
		t.Fatalf("reopened maximum lease epoch Project = %#v, %v", project, err)
	}
}

func TestDoltProjectLeaseUsesServerTimeWithoutCallerClock(t *testing.T) {
	fixture := startDoltFixture(t)
	store := openContractStore(t, fixture, "project-lease-server-clock", true)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	project := domain.Project{
		ID: "clock-project", Name: "Clock Project", State: "active", Organizer: testOrganizer("clock-project"),
	}
	workspace := testWorkspace(
		t, project.ID, "product", "/srv/workspaces/clock-project",
		"https://github.com/example/clock-project.git",
	)
	result, err := store.CreateProject(
		ctx, command("clock-project-create", "project.create", project.ID, 0, `{}`), project,
		[]domain.Workspace{workspace}, event("clock-project-created", "", 1, project.ID, 0, "project.created"),
	)
	requireApplied(t, result, err)
	mutation := domain.ProjectLeaseMutation{
		Kind: domain.ProjectLeaseAcquire, ProjectID: project.ID,
		HolderInstance: "engine-clock", HolderProcessIdentity: "pid-300:start-1", DurationMillis: 60_000,
	}
	request := typedCommand(t, "clock-lease-acquire", "project.lease.acquire", project.ID, 0, mutation)
	before := time.Now().UnixMilli()
	result, err = store.ApplyProjectLease(ctx, request, mutation)
	after := time.Now().UnixMilli()
	requireApplied(t, result, err)
	project, err = store.Project(ctx, project.ID)
	if err != nil || project.Lease == nil || project.Lease.AcquiredAtMillis < before-1_000 ||
		project.Lease.AcquiredAtMillis > after+1_000 ||
		project.Lease.ExpiresAtMillis-project.Lease.AcquiredAtMillis != mutation.DurationMillis ||
		project.LastLeaseEpoch != 1 {
		t.Fatalf("server-clock lease = %#v before=%d after=%d error=%v", project.Lease, before, after, err)
	}
}

func TestDoltStaleWorkspaceReplacementConflictsBeforeMappingAndReplays(t *testing.T) {
	fixture := startDoltFixture(t)
	const storeID = "workspace-stale-store"
	store := openContractStore(t, fixture, storeID, true)
	ctx := context.Background()
	project := domain.Project{
		ID: "stale-project", Name: "Stale Project", State: "active", Organizer: testOrganizer("stale-project"),
	}
	first := testWorkspace(
		t, project.ID, "first", "/srv/workspaces/stale-first", "https://github.com/example/stale-first.git",
	)
	second := testWorkspace(
		t, project.ID, "second", "/srv/workspaces/stale-second", "https://github.com/example/stale-second.git",
	)
	result, err := store.CreateProject(
		ctx, command("stale-project-create", "project.create", project.ID, 0, `{}`), project,
		[]domain.Workspace{first, second}, event("stale-project-created", "", 1, project.ID, 0, "project.created"),
	)
	requireApplied(t, result, err)
	second.Name = "Current second"
	second.Version = 1
	result, err = store.UpdateWorkspace(
		ctx, command("stale-workspace-current", "workspace.update", second.ID, 0, `{}`), second,
		event("stale-workspace-current-event", "", 1, second.ID, 1, "workspace.updated"),
	)
	requireApplied(t, result, err)

	rebound := second
	repository, err := repositorydomain.CanonicalRemote("https://github.com/example/rebound.git")
	if err != nil {
		t.Fatal(err)
	}
	rebound.Repository.ID = repository.ID
	rebound.Repository.Key = repository.Key
	rebound.Repository.CanonicalRemote = repository.Canonical
	rebindCommand := command("stale-workspace-rebind", "workspace.update", second.ID, 0, `{}`)
	rebindEvent := event("stale-workspace-rebind-event", "", 2, second.ID, 1, "workspace.updated")
	rebindResult, err := store.UpdateWorkspace(ctx, rebindCommand, rebound, rebindEvent)
	if err != nil || rebindResult.Outcome != domain.CommandRejectedVersionConflict || rebindResult.ObservedVersion != 1 {
		t.Fatalf("stale rebind = %#v, %v", rebindResult, err)
	}

	aliasConflict := second
	aliasConflict.Repository.SourcePath = first.Repository.SourcePath
	aliasConflict.Repository.SourceDevice = first.Repository.SourceDevice
	aliasConflict.Repository.SourceInode = first.Repository.SourceInode
	aliasConflict.Repository.GitCommonDirectory = first.Repository.GitCommonDirectory
	aliasConflict.Repository.GitCommonDevice = first.Repository.GitCommonDevice
	aliasConflict.Repository.GitCommonInode = first.Repository.GitCommonInode
	aliasCommand := command("stale-workspace-alias", "workspace.update", second.ID, 0, `{}`)
	aliasEvent := event("stale-workspace-alias-event", "", 3, second.ID, 1, "workspace.updated")
	aliasResult, err := store.UpdateWorkspace(ctx, aliasCommand, aliasConflict, aliasEvent)
	if err != nil || aliasResult.Outcome != domain.CommandRejectedVersionConflict || aliasResult.ObservedVersion != 1 {
		t.Fatalf("stale alias conflict = %#v, %v", aliasResult, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reloaded := openContractStore(t, fixture, storeID, true)
	t.Cleanup(func() { _ = reloaded.Close() })
	for name, replay := range map[string]func() (domain.CommandResult, error){
		"rebind": func() (domain.CommandResult, error) {
			return reloaded.UpdateWorkspace(ctx, rebindCommand, rebound, rebindEvent)
		},
		"alias": func() (domain.CommandResult, error) {
			return reloaded.UpdateWorkspace(ctx, aliasCommand, aliasConflict, aliasEvent)
		},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := replay()
			if err != nil || !result.Replay || result.Outcome != domain.CommandRejectedVersionConflict || result.ObservedVersion != 1 {
				t.Fatalf("%s replay = %#v, %v", name, result, err)
			}
		})
	}
}

func TestDoltCanonicalRemoteSinglePassPersistenceAndRejection(t *testing.T) {
	fixture := startDoltFixture(t)
	const storeID = "canonical-remote-store"
	store := openContractStore(t, fixture, storeID, true)
	ctx := context.Background()
	project := domain.Project{
		ID: "percent-project", Name: "Percent Project", State: "active", Organizer: testOrganizer("percent-project"),
	}
	const remote = "https://github.com/acme/repo%252egit"
	workspace := testWorkspace(t, project.ID, "product", "/srv/workspaces/percent-project", remote)
	result, err := store.CreateProject(
		ctx, command("percent-project-create", "project.create", project.ID, 0, `{}`), project,
		[]domain.Workspace{workspace}, event("percent-project-created", "", 1, project.ID, 0, "project.created"),
	)
	requireApplied(t, result, err)
	persisted, err := store.Workspace(ctx, workspace.ID)
	if err != nil || persisted.Repository.CanonicalRemote != remote ||
		persisted.Repository.Key != "github.com/acme/repo%2egit" {
		t.Fatalf("persisted percent Workspace = %#v, %v", persisted, err)
	}
	roundTrip, err := repositorydomain.CanonicalRemote(persisted.Repository.CanonicalRemote)
	if err != nil || roundTrip.Canonical != persisted.Repository.CanonicalRemote ||
		roundTrip.Key != persisted.Repository.Key || roundTrip.ID != persisted.Repository.ID {
		t.Fatalf("persisted canonical round trip = %#v, %v", roundTrip, err)
	}

	for name, rejected := range map[string]string{
		"malformed embedded IPv4": "ssh://git@[192.168.1.1::]/example/product.git",
		"token-shaped username":   "https://token-shaped-username-0123456789abcdef@github.com/example/product.git",
		"raw repeated suffix":     "https://github.com/example/repository.git.git",
		"case repeated suffix":    "https://github.com/example/repository.GIT.git",
		"encoded prefix suffix":   "https://github.com/example/repository%2egit.git",
		"encoded terminal suffix": "https://github.com/example/repository.git%2egit",
		"deeper suffix chain":     "https://github.com/example/repository%2Egit%2egit%2EGIT",
	} {
		t.Run(name, func(t *testing.T) {
			rejectedProject := domain.Project{
				ID: "rejected-" + strings.ReplaceAll(name, " ", "-"), Name: "Rejected Remote", State: "active",
			}
			rejectedProject.Organizer = testOrganizer(rejectedProject.ID)
			rejectedWorkspace := testWorkspace(
				t, rejectedProject.ID, "product", "/srv/workspaces/"+rejectedProject.ID,
				"https://github.com/example/"+rejectedProject.ID+".git",
			)
			rejectedWorkspace.Repository.CanonicalRemote = rejected
			request := command("canonical-rejected-"+strings.ReplaceAll(name, " ", "-"), "project.create", rejectedProject.ID, 0, `{}`)
			_, err := store.CreateProject(
				ctx, request, rejectedProject, []domain.Workspace{rejectedWorkspace},
				event("canonical-rejected-event-"+strings.ReplaceAll(name, " ", "-"), "", 1, rejectedProject.ID, 0, "project.created"),
			)
			if !errors.Is(err, storeport.ErrInvalidRecord) || strings.Contains(err.Error(), rejected) {
				t.Fatalf("rejected remote error = %v", err)
			}
			if _, err := store.Command(ctx, request.IdempotencyKey); !errors.Is(err, storeport.ErrNotFound) {
				t.Fatalf("rejected remote command persisted: %v", err)
			}
		})
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reloaded := openContractStore(t, fixture, storeID, true)
	t.Cleanup(func() { _ = reloaded.Close() })
	persisted, err = reloaded.Workspace(ctx, workspace.ID)
	if err != nil || persisted.Repository.CanonicalRemote != remote || persisted.Repository.Key != "github.com/acme/repo%2egit" {
		t.Fatalf("reopened percent Workspace = %#v, %v", persisted, err)
	}
}

func TestDoltWorkspaceMappingIsIdempotentConcurrentAndProjectScoped(t *testing.T) {
	fixture := startDoltFixture(t)
	store := openContractStore(t, fixture, "workspace-mapping-store", true)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	project := domain.Project{
		ID: "mapping-project", Name: "Mapping Project", State: "active", Organizer: testOrganizer("mapping-project"),
	}
	initial := testWorkspace(
		t, project.ID, "workspace-initial", "/srv/workspaces/mapping-initial",
		"https://github.com/example/initial.git",
	)
	const secretRemoteUser = "token-shaped-username-0123456789abcdef"
	credentialWorkspace := initial
	credentialWorkspace.Repository.CanonicalRemote = "https://" + secretRemoteUser + "@github.com/example/initial.git"
	credentialCommand := command("mapping-credential-remote", "project.create", project.ID, 0, `{}`)
	if _, err := store.CreateProject(
		ctx, credentialCommand, project, []domain.Workspace{credentialWorkspace},
		event("mapping-credential-remote-event", "", 1, project.ID, 0, "project.created"),
	); !errors.Is(err, storeport.ErrInvalidRecord) || strings.Contains(err.Error(), secretRemoteUser) {
		t.Fatalf("credential remote error = %v", err)
	}
	if _, err := store.Command(ctx, credentialCommand.IdempotencyKey); !errors.Is(err, storeport.ErrNotFound) {
		t.Fatalf("credential-bearing command persisted: %v", err)
	}
	wrongOrganizer := project
	wrongOrganizer.ID = "mapping-project-invalid"
	wrongOrganizer.Organizer = testOrganizer(wrongOrganizer.ID)
	wrongOrganizer.Organizer.ID = domain.OrganizerID("another-project")
	wrongWorkspace := testWorkspace(
		t, wrongOrganizer.ID, "product", "/srv/workspaces/mapping-invalid",
		"https://github.com/example/mapping-invalid.git",
	)
	if _, err := store.CreateProject(
		ctx, command("mapping-invalid-organizer", "project.create", wrongOrganizer.ID, 0, `{}`), wrongOrganizer,
		[]domain.Workspace{wrongWorkspace},
		event("mapping-invalid-organizer-event", "", 1, wrongOrganizer.ID, 0, "project.created"),
	); !errors.Is(err, storeport.ErrInvalidRecord) {
		t.Fatalf("wrong Organizer identity error = %v", err)
	}
	if _, err := store.CreateProject(
		ctx, command("mapping-empty-workspaces", "project.create", project.ID, 0, `{}`), project, nil,
		event("mapping-empty-workspaces-event", "", 1, project.ID, 0, "project.created"),
	); !errors.Is(err, storeport.ErrInvalidRecord) {
		t.Fatalf("empty Workspace set error = %v", err)
	}
	result, err := store.CreateProject(
		ctx, command("mapping-project-create", "project.create", project.ID, 0, `{}`), project,
		[]domain.Workspace{initial}, event("mapping-project-created-event", "", 1, project.ID, 0, "project.created"),
	)
	requireApplied(t, result, err)

	added := testWorkspace(
		t, project.ID, "workspace-added", "/srv/workspaces/mapping-added",
		"https://github.com/example/shared.git",
	)
	createAdded := command("mapping-workspace-create", "workspace.create", added.ID, 0, `{}`)
	createAddedEvent := event("mapping-workspace-created-event", "", 1, added.ID, 0, "workspace.created")
	result, err = store.CreateWorkspace(ctx, createAdded, added, createAddedEvent)
	requireApplied(t, result, err)
	replay, err := store.CreateWorkspace(ctx, createAdded, added, createAddedEvent)
	if err != nil || !replay.Replay || replay.Outcome != domain.CommandApplied {
		t.Fatalf("Workspace create replay = %#v, %v", replay, err)
	}
	conflictingReplay := createAdded
	conflictingReplay.Payload = json.RawMessage(`{"different":true}`)
	if _, err := store.CreateWorkspace(ctx, conflictingReplay, added, createAddedEvent); !errors.Is(err, storeport.ErrIdempotencyConflict) {
		t.Fatalf("Workspace conflicting replay error = %v", err)
	}

	rebound := added
	differentRemote, err := repositorydomain.CanonicalRemote("https://github.com/example/different.git")
	if err != nil {
		t.Fatal(err)
	}
	rebound.Repository.ID, rebound.Repository.Key = differentRemote.ID, differentRemote.Key
	rebound.Repository.CanonicalRemote = differentRemote.Canonical
	rebound.Version = 1
	if _, err := store.UpdateWorkspace(
		ctx, command("mapping-workspace-rebind", "workspace.update", rebound.ID, 0, `{}`), rebound,
		event("mapping-workspace-rebind-event", "", 1, rebound.ID, 1, "workspace.updated"),
	); !errors.Is(err, storeport.ErrInvalidRecord) {
		t.Fatalf("Workspace repository rebind error = %v", err)
	}
	collidingPath := added
	collidingPath.Repository.SourcePath = initial.Repository.SourcePath
	collidingPath.Repository.GitCommonDirectory = initial.Repository.GitCommonDirectory
	collidingPath.Version = 1
	if _, err := store.UpdateWorkspace(
		ctx, command("mapping-workspace-path-conflict", "workspace.update", collidingPath.ID, 0, `{}`), collidingPath,
		event("mapping-workspace-path-conflict-event", "", 1, collidingPath.ID, 1, "workspace.updated"),
	); !errors.Is(err, storeport.ErrWorkspaceConflict) {
		t.Fatalf("Workspace path conflict error = %v", err)
	}

	alias := testWorkspace(
		t, project.ID, "workspace-alias", "/srv/workspaces/mapping-alias",
		"git@github.com:example/shared.git",
	)
	if _, err := store.CreateWorkspace(
		ctx, command("mapping-workspace-alias", "workspace.create", alias.ID, 0, `{}`), alias,
		event("mapping-workspace-alias-event", "", 1, alias.ID, 0, "workspace.created"),
	); !errors.Is(err, storeport.ErrWorkspaceConflict) {
		t.Fatalf("canonical alias conflict error = %v", err)
	}

	raceCandidates := []domain.Workspace{
		testWorkspace(t, project.ID, "workspace-race-a", "/srv/workspaces/race-a", "https://github.com/example/race.git"),
		testWorkspace(t, project.ID, "workspace-race-b", "/srv/workspaces/race-b", "git@github.com:example/race.git"),
	}
	start := make(chan struct{})
	raceResults := make(chan domain.CommandResult, 2)
	raceErrors := make(chan error, 2)
	var wait sync.WaitGroup
	for index := 0; index < len(raceCandidates); index++ {
		candidate := raceCandidates[index]
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, err := store.CreateWorkspace(
				ctx,
				command("mapping-race-"+candidate.ID, "workspace.create", candidate.ID, 0, `{}`),
				candidate,
				event("mapping-race-event-"+candidate.ID, "", 1, candidate.ID, 0, "workspace.created"),
			)
			raceResults <- result
			raceErrors <- err
		}()
	}
	close(start)
	wait.Wait()
	close(raceResults)
	close(raceErrors)
	applied, conflicts := 0, 0
	for result := range raceResults {
		if result.Outcome == domain.CommandApplied {
			applied++
		}
	}
	for err := range raceErrors {
		switch {
		case err == nil:
		case errors.Is(err, storeport.ErrWorkspaceConflict):
			conflicts++
		default:
			t.Fatalf("concurrent Workspace create error = %v", err)
		}
	}
	if applied != 1 || conflicts != 1 {
		t.Fatalf("concurrent Workspace results applied=%d conflicts=%d", applied, conflicts)
	}

	workspaces, err := store.Workspaces(ctx, project.ID)
	if err != nil || len(workspaces) != 3 {
		t.Fatalf("Workspaces() = %#v, %v", workspaces, err)
	}

	secondProject := domain.Project{
		ID: "mapping-project-2", Name: "Second Mapping Project", State: "active",
		Organizer: testOrganizer("mapping-project-2"),
	}
	secondWorkspace := testWorkspace(
		t, secondProject.ID, "workspace-second-project", "/srv/workspaces/second-project",
		"git@github.com:example/shared.git",
	)
	result, err = store.CreateProject(
		ctx, command("mapping-project-2-create", "project.create", secondProject.ID, 0, `{}`), secondProject,
		[]domain.Workspace{secondWorkspace},
		event("mapping-project-2-created-event", "", 1, secondProject.ID, 0, "project.created"),
	)
	requireApplied(t, result, err)
	projects, err := store.Projects(ctx)
	if err != nil || len(projects) != 2 {
		t.Fatalf("Projects() = %#v, %v", projects, err)
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
	if aggregateCount != 4 || commandCount != 9 || eventCount != 7 {
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
