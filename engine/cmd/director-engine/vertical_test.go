// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mcuadros/director-engine/adapters/dolt"
	"github.com/mcuadros/director-engine/adapters/fake"
	executionapp "github.com/mcuadros/director-engine/application/execution"
	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/reducer/eligibility"
)

type verticalDolt struct {
	address  string
	database string
	root     string
	command  *exec.Cmd
	waited   chan error
}

func startVerticalDolt(t *testing.T) *verticalDolt {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the walking-skeleton execution path supports Linux only")
	}
	version, err := exec.Command("dolt", "version").CombinedOutput()
	if err != nil || !strings.Contains(string(version), "dolt version 2.3.2") {
		t.Fatalf("Dolt 2.3.2 is required: %v: %s", err, version)
	}
	root := t.TempDir()
	clientRoot := filepath.Join(root, "client")
	if err := os.MkdirAll(filepath.Join(clientRoot, ".dolt"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(clientRoot, ".dolt", "config_global.json"),
		[]byte("{\n  \"metrics.disabled\": \"true\"\n}\n"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	database := "vertical_contract"
	databaseDirectory := filepath.Join(root, database)
	if err := os.Mkdir(databaseDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	runDolt := func(directory string, arguments ...string) {
		command := exec.Command("dolt", arguments...)
		command.Dir = directory
		command.Env = append(os.Environ(), "DOLT_ROOT_PATH="+clientRoot, "DOLT_DISABLE_EVENT_FLUSH=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("dolt %s: %v: %s", strings.Join(arguments, " "), err, output)
		}
	}
	runDolt(databaseDirectory, "init", "--name=Director vertical", "--email=vertical@example.invalid")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	configRoot := filepath.Join(root, ".doltcfg")
	if err := os.Mkdir(configRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	configuration := filepath.Join(root, "server.yaml")
	serverConfig := fmt.Sprintf("log_level: warning\ndata_dir: %q\ncfg_dir: %q\nbehavior:\n  read_only: false\nlistener:\n  host: 127.0.0.1\n  port: %d\nsystem_variables:\n  dolt_force_transaction_commit: 0\n", root, configRoot, port)
	if err := os.WriteFile(configuration, []byte(serverConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("dolt", "sql-server", "--config="+configuration)
	command.Dir = root
	command.Env = append(os.Environ(), "DOLT_ROOT_PATH="+clientRoot, "DOLT_DISABLE_EVENT_FLUSH=1")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	fixture := &verticalDolt{
		address: fmt.Sprintf("127.0.0.1:%d", port), database: database,
		root: root, command: command, waited: waited,
	}
	t.Cleanup(func() {
		if fixture.command.Process == nil {
			return
		}
		_ = fixture.command.Process.Signal(syscall.SIGTERM)
		select {
		case <-fixture.waited:
		case <-time.After(10 * time.Second):
			_ = fixture.command.Process.Kill()
			<-fixture.waited
		}
	})
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", fixture.address, 100*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return fixture
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("disposable Dolt server did not become ready")
	return nil
}

func openVerticalStore(t *testing.T, fixture *verticalDolt) *dolt.DoltTaskStore {
	t.Helper()
	endpoint := dolt.Endpoint{Address: fixture.address, Database: fixture.database, User: "root"}
	store, err := dolt.Open(dolt.Config{Control: endpoint, Writer: endpoint, StoreID: "vertical-store"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func createVerticalRecords(t *testing.T, store *dolt.DoltTaskStore, suffix string) (domain.Project, domain.Task) {
	t.Helper()
	ctx := context.Background()
	project := domain.Project{ID: "project-" + suffix, Name: "Vertical fixture", State: "active"}
	task := domain.Task{
		ID: "task-" + suffix, ProjectID: project.ID, Title: "Implement the fake execution vertical path",
		Objective: "Produce one fake Candidate", AcceptanceCriteria: "criterion-1",
	}
	command := func(key, kind, aggregate string) domain.CommandRequest {
		return domain.CommandRequest{IdempotencyKey: key, Type: kind, AggregateID: aggregate, Payload: json.RawMessage(`{}`)}
	}
	event := func(id, aggregate, kind string) domain.Event {
		return domain.Event{ID: id, Sequence: 1, AggregateID: aggregate, Type: kind, Payload: json.RawMessage(`{}`)}
	}
	if result, err := store.CreateProject(ctx, command("create-project-"+suffix, "project.create", project.ID), project, event("project-created-"+suffix, project.ID, "project.created")); err != nil || result.Outcome != domain.CommandApplied {
		t.Fatalf("create Project: %#v, %v", result, err)
	}
	if result, err := store.CreateTask(ctx, command("create-task-"+suffix, "task.create", task.ID), task, event("task-created-"+suffix, task.ID, "task.created")); err != nil || result.Outcome != domain.CommandApplied {
		t.Fatalf("create Task: %#v, %v", result, err)
	}
	return project, task
}

func initializeRepository(t *testing.T, suffix string) (string, string) {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "source-"+suffix)
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(arguments ...string) string {
		command := exec.Command("git", arguments...)
		command.Dir = repository
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
		}
		return strings.TrimSpace(string(output))
	}
	run("init", "--initial-branch=main")
	run("config", "user.name", "Director fixture")
	run("config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "fixture: initialize source")
	return repository, run("rev-parse", "HEAD")
}

func operationalObservation(id string) execution.OperationalObservation {
	measurement := func(value uint64) execution.Measurement {
		return execution.Measurement{Present: true, Value: value}
	}
	return execution.OperationalObservation{
		ID: id, ObservedAtMillis: 1_000,
		FreeDiskBasisPoints: measurement(5_000), WorktreeBytes: measurement(0),
		Processes: measurement(1), MemoryBytes: measurement(1),
		ElapsedMilliseconds: measurement(1), OutputBytes: measurement(0),
		TemporaryBytes: measurement(0),
	}
}

func eligibilityFacts(scope execution.Scope, surfaces execution.LifecycleSurfaces) eligibility.Facts {
	observedTrue := eligibility.BooleanFact{Observed: true, Value: true}
	return eligibility.Facts{
		SchemaVersion: eligibility.SchemaVersion, Scope: scope,
		ProjectLeaseCurrent: observedTrue, ProjectActive: observedTrue,
		OrganizerRevisionActive: observedTrue, TaskComplete: observedTrue,
		DependenciesSatisfied: observedTrue, NoActiveRun: observedTrue,
		LaunchPolicyAllows: observedTrue, CapacityAvailable: observedTrue,
		BudgetsAvailable: observedTrue, ProviderAdmitted: observedTrue,
		RepositoryIdentityExact: observedTrue, LifecycleSurfaces: surfaces,
		Isolation: execution.IsolationObservation{
			Observed: true, Runtime: "fixture-rootless-oci", Rootless: true,
			ReadOnlyRootFilesystem: true, CapabilitiesDropped: true,
			NoNewPrivileges: true, PrivateNetworkNamespace: true,
			RuntimeSocketsAbsent: true, ControlToolsAbsent: true,
			OwnedWorktreeOnly: true, FixedStdioMCP: true,
		},
		OperationalPolicy: execution.OperationalPolicy{
			MinimumFreeDiskBasisPoints: 1_000, MaximumWorktreeBytes: 1 << 20,
			MaximumProcesses: 32, MaximumMemoryBytes: 256 << 20,
			MaximumElapsedMilliseconds: 60_000, MaximumOutputBytes: 4 << 20,
			MaximumTemporaryBytes: 16 << 20, MaximumObservationAgeMillis: 30_000,
		},
		OperationalObservation: operationalObservation("launch-" + scope.RunID),
		TaskStoreNowMillis:     1_001,
	}
}

func startCommand(task domain.Task, scope execution.Scope, source, worktree, base string, facts eligibility.Facts) executionapp.StartCommand {
	return executionapp.StartCommand{
		RequestID: "start-" + scope.RunID, Scope: scope, RunNumber: 1,
		SourcePath: source, WorktreePath: worktree,
		Branch: "task/" + scope.TaskID, BaseSHA: base,
		TaskTitle: task.Title, InitialPrompt: "Produce the declared fixture Candidate and return one completed claim.",
		CriterionIDs:     []string{"criterion-1"},
		EligibilityFacts: facts,
	}
}

func runSteps(t *testing.T, store *dolt.DoltTaskStore, environment *fake.Environment, runID string, stop func(domain.Run) bool) domain.Run {
	t.Helper()
	ctx := context.Background()
	for step := 0; step < 200; step++ {
		run, err := store.Run(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		if stop(run) {
			return run
		}
		controller := executionapp.NewController(store, environment, environment)
		_, err = controller.Step(ctx, runID, 1_001)
		if err != nil && !errors.Is(err, fake.ErrResponseLost) {
			t.Fatalf("step %d: %v", step, err)
		}
	}
	t.Fatal("execution path did not converge")
	return domain.Run{}
}

func TestFakeExecutionVerticalPathRecoversEveryLostResponse(t *testing.T) {
	fixture := startVerticalDolt(t)
	store := openVerticalStore(t, fixture)
	project, task := createVerticalRecords(t, store, "complete")
	source, base := initializeRepository(t, "complete")
	worktree := filepath.Join(filepath.Dir(source), "owned-worktree")
	scope := execution.Scope{ProjectID: project.ID, WorkspaceID: "workspace-complete", TaskID: task.ID, RunID: "run-complete"}
	surfaces := execution.LifecycleSurfaces{Setup: []string{"fixture setup"}}
	facts := eligibilityFacts(scope, surfaces)
	facts.LifecycleApproval = &execution.LifecycleApproval{
		ActorKind: "human", Source: "authenticated_engine_command", ActorID: "human-1",
		Scope: scope, Digest: execution.LifecycleDigest(surfaces),
	}
	environment := fake.NewEnvironment(fake.Options{
		SourcePath: source, WorktreePath: worktree, Branch: "task/" + task.ID,
		BaseSHA: base, Operational: operationalObservation("periodic-1"),
		LoseEveryMutationResponse: true,
	})
	t.Cleanup(environment.RemoveFixture)
	controller := executionapp.NewController(store, environment, environment)
	started, err := controller.Start(context.Background(), startCommand(task, scope, source, worktree, base, facts))
	if err != nil || started.Decision.Kind != eligibility.DecisionEligible || started.RunID != scope.RunID {
		t.Fatalf("start = %#v, %v", started, err)
	}
	replayed, err := executionapp.NewController(store, environment, environment).Start(
		context.Background(), startCommand(task, scope, source, worktree, base, facts),
	)
	if err != nil || replayed.RunID != started.RunID || replayed.Decision.DecisionID != started.Decision.DecisionID || environment.TotalMutationCount() != 0 {
		t.Fatalf("idempotent start replay = %#v, %v", replayed, err)
	}

	run := runSteps(t, store, environment, scope.RunID, func(run domain.Run) bool {
		return run.Execution.Agent.Phase == execution.EffectComplete
	})
	if run.Execution.Agent.ExternalID == "" || !run.Execution.PreparationReady {
		t.Fatalf("agent launch state = %#v", run.Execution)
	}
	request := environment.AgentRequest()
	if request.ParentAgentID != nil || request.Title != task.Title || request.InitialPrompt == "" {
		t.Fatalf("top-level Task Agent request = %#v", request)
	}
	claim, err := environment.ProduceCandidate(context.Background(), fake.CandidateRequest{
		ClaimID: "claim-complete", AgentID: run.Execution.Agent.ExternalID,
		BaseSHA: base, CriteriaResults: map[string]string{"criterion-1": "claimed_satisfied"},
	})
	if err != nil {
		t.Fatal(err)
	}
	controller = executionapp.NewController(store, environment, environment)
	if err := controller.RecordCompletedClaim(context.Background(), scope.RunID, claim); err != nil {
		t.Fatal(err)
	}
	if err := executionapp.NewController(store, environment, environment).RecordCompletedClaim(context.Background(), scope.RunID, claim); err != nil {
		t.Fatalf("idempotent claim replay: %v", err)
	}
	run = runSteps(t, store, environment, scope.RunID, func(run domain.Run) bool {
		return run.Execution.Terminal
	})
	if run.CurrentCandidateID == "" || run.Execution.NeedsYou != nil {
		t.Fatalf("terminal Run = %#v", run)
	}
	candidate, err := store.Candidate(context.Background(), run.CurrentCandidateID)
	if err != nil || candidate.CommitSHA != claim.CandidateSHA || candidate.RunID != run.ID {
		t.Fatalf("durable Candidate = %#v, %v", candidate, err)
	}
	events, err := store.Events(context.Background(), domain.EventQuery{RunID: run.ID, Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	var durableObservation, durableOperational, durableClaim bool
	for _, event := range events {
		payload := string(event.Payload)
		durableObservation = durableObservation || strings.Contains(payload, `"effectObservation"`)
		durableOperational = durableOperational || strings.Contains(payload, `"operationalObservation"`)
		durableClaim = durableClaim || strings.Contains(payload, `"claim"`)
	}
	if !durableObservation || !durableOperational || !durableClaim {
		t.Fatalf("immutable event evidence missing: observation=%v operational=%v claim=%v", durableObservation, durableOperational, durableClaim)
	}
	if environment.WorktreePresent() {
		t.Fatal("Director-owned fixture worktree survived terminal cleanup")
	}
	for _, kind := range []execution.EffectKind{
		execution.EffectWorktreeCreate, execution.EffectHostViewCreate,
		execution.EffectBoundaryMaterialize, execution.EffectSetupRun,
		execution.EffectAgentCreate, execution.EffectAgentArchive,
		execution.EffectHostViewArchive, execution.EffectWorktreeRemove,
	} {
		if count := environment.MutationCount(kind); count != 1 {
			t.Fatalf("%s mutation count = %d, want 1", kind, count)
		}
	}
	if order := environment.MutationOrder(); strings.Join(order, ",") != strings.Join([]string{
		string(execution.EffectWorktreeCreate), string(execution.EffectHostViewCreate),
		string(execution.EffectBoundaryMaterialize), string(execution.EffectSetupRun),
		string(execution.EffectAgentCreate), string(execution.EffectAgentArchive),
		string(execution.EffectHostViewArchive), string(execution.EffectWorktreeRemove),
	}, ",") {
		t.Fatalf("mutation order = %v", order)
	}
}

func TestLifecycleAndPeriodicFactsParkWithoutSDKOrCleanupAuthority(t *testing.T) {
	fixture := startVerticalDolt(t)
	store := openVerticalStore(t, fixture)

	t.Run("unapproved lifecycle", func(t *testing.T) {
		project, task := createVerticalRecords(t, store, "lifecycle")
		source, base := initializeRepository(t, "lifecycle")
		worktree := filepath.Join(filepath.Dir(source), "unapproved-worktree")
		scope := execution.Scope{ProjectID: project.ID, WorkspaceID: "workspace-lifecycle", TaskID: task.ID, RunID: "run-lifecycle"}
		surfaces := execution.LifecycleSurfaces{Setup: []string{"must never execute"}}
		environment := fake.NewEnvironment(fake.Options{SourcePath: source, WorktreePath: worktree, Branch: "task/" + task.ID, BaseSHA: base, Operational: operationalObservation("periodic-lifecycle")})
		t.Cleanup(environment.RemoveFixture)
		controller := executionapp.NewController(store, environment, environment)
		result, err := controller.Start(context.Background(), startCommand(task, scope, source, worktree, base, eligibilityFacts(scope, surfaces)))
		if err != nil || result.Decision.Kind != eligibility.DecisionEscalate || result.Decision.CleanupAuthorized {
			t.Fatalf("unapproved start = %#v, %v", result, err)
		}
		persisted, err := store.Task(context.Background(), task.ID)
		if err != nil || persisted.Attention == nil || persisted.Attention.Code != execution.NeedLifecycleApproval || persisted.Attention.CleanupAuthorized {
			t.Fatalf("durable lifecycle park = %#v, %v", persisted, err)
		}
		if environment.HostCallCount() != 0 || environment.TotalMutationCount() != 0 || environment.SetupCount() != 0 {
			t.Fatalf("pre-admission side effects: host=%d mutation=%d setup=%d", environment.HostCallCount(), environment.TotalMutationCount(), environment.SetupCount())
		}
	})

	launchCases := []struct {
		name     string
		expected execution.NeedCode
		mutate   func(*eligibility.Facts)
	}{
		{"missing launch fact", execution.NeedOperationalFactMissing, func(facts *eligibility.Facts) {
			facts.OperationalObservation.FreeDiskBasisPoints.Present = false
		}},
		{"exceeded launch fact", execution.NeedMemoryLimit, func(facts *eligibility.Facts) {
			facts.OperationalObservation.MemoryBytes.Value = (256 << 20) + 1
		}},
		{"missing rootless OCI", execution.NeedRootlessOCI, func(facts *eligibility.Facts) {
			facts.Isolation.Rootless = false
		}},
	}
	for index, fixture := range launchCases {
		t.Run(fixture.name, func(t *testing.T) {
			suffix := fmt.Sprintf("launch-%d", index)
			project, task := createVerticalRecords(t, store, suffix)
			source, base := initializeRepository(t, suffix)
			worktree := filepath.Join(filepath.Dir(source), suffix+"-worktree")
			scope := execution.Scope{ProjectID: project.ID, WorkspaceID: "workspace-" + suffix, TaskID: task.ID, RunID: "run-" + suffix}
			facts := eligibilityFacts(scope, execution.LifecycleSurfaces{})
			fixture.mutate(&facts)
			environment := fake.NewEnvironment(fake.Options{SourcePath: source, WorktreePath: worktree, Branch: "task/" + task.ID, BaseSHA: base, Operational: operationalObservation("periodic-unused")})
			t.Cleanup(environment.RemoveFixture)
			controller := executionapp.NewController(store, environment, environment)
			result, err := controller.Start(context.Background(), startCommand(task, scope, source, worktree, base, facts))
			if err != nil || result.Decision.Kind != eligibility.DecisionEscalate || result.Decision.CleanupAuthorized {
				t.Fatalf("unsafe launch = %#v, %v", result, err)
			}
			persisted, err := store.Task(context.Background(), task.ID)
			if err != nil || persisted.Attention == nil || persisted.Attention.Code != fixture.expected || persisted.Attention.CleanupAuthorized {
				t.Fatalf("durable launch park = %#v, %v", persisted, err)
			}
			if environment.HostCallCount() != 0 || environment.TotalMutationCount() != 0 {
				t.Fatal("unsafe launch reached an external mutation")
			}
		})
	}

	periodicCases := []struct {
		name     string
		expected execution.NeedCode
		mutate   func(*execution.OperationalObservation)
	}{
		{"missing periodic fact", execution.NeedOperationalFactMissing, func(observation *execution.OperationalObservation) {
			observation.MemoryBytes.Present = false
		}},
		{"exceeded periodic fact", execution.NeedProcessLimit, func(observation *execution.OperationalObservation) {
			observation.Processes.Value = 33
		}},
	}
	for index, fixture := range periodicCases {
		t.Run(fixture.name, func(t *testing.T) {
			suffix := fmt.Sprintf("periodic-%d", index)
			project, task := createVerticalRecords(t, store, suffix)
			source, base := initializeRepository(t, suffix)
			worktree := filepath.Join(filepath.Dir(source), suffix+"-worktree")
			scope := execution.Scope{ProjectID: project.ID, WorkspaceID: "workspace-" + suffix, TaskID: task.ID, RunID: "run-" + suffix}
			facts := eligibilityFacts(scope, execution.LifecycleSurfaces{})
			environment := fake.NewEnvironment(fake.Options{SourcePath: source, WorktreePath: worktree, Branch: "task/" + task.ID, BaseSHA: base, Operational: operationalObservation("periodic-valid")})
			t.Cleanup(environment.RemoveFixture)
			controller := executionapp.NewController(store, environment, environment)
			if _, err := controller.Start(context.Background(), startCommand(task, scope, source, worktree, base, facts)); err != nil {
				t.Fatal(err)
			}
			runSteps(t, store, environment, scope.RunID, func(run domain.Run) bool {
				return run.Execution.Agent.Phase == execution.EffectComplete
			})
			observation := operationalObservation("periodic-unsafe")
			fixture.mutate(&observation)
			environment.SetOperational(observation)
			run := runSteps(t, store, environment, scope.RunID, func(run domain.Run) bool {
				return run.Execution.NeedsYou != nil
			})
			if run.Execution.NeedsYou.Code != fixture.expected || run.Execution.NeedsYou.CleanupAuthorized {
				t.Fatalf("periodic park = %#v", run.Execution.NeedsYou)
			}
			if environment.MutationCount(execution.EffectAgentArchive) != 0 || environment.MutationCount(execution.EffectHostViewArchive) != 0 || environment.MutationCount(execution.EffectWorktreeRemove) != 0 {
				t.Fatal("periodic parking dispatched cleanup")
			}
			if !environment.WorktreePresent() {
				t.Fatal("periodic parking removed the owned worktree")
			}
		})
	}
}
