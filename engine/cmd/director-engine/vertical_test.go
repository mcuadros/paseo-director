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
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mcuadros/director-engine/adapters/dolt"
	"github.com/mcuadros/director-engine/adapters/fake"
	executionapp "github.com/mcuadros/director-engine/application/execution"
	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/agentprofile"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	"github.com/mcuadros/director-engine/domain/execution"
	repositorydomain "github.com/mcuadros/director-engine/domain/repository"
	"github.com/mcuadros/director-engine/domain/runtimebudget"
	"github.com/mcuadros/director-engine/ports/host"
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

func createVerticalRecords(t *testing.T, store *dolt.DoltTaskStore, suffix, source string) (domain.Project, domain.Task) {
	t.Helper()
	ctx := context.Background()
	project := domain.Project{ID: "project-" + suffix, Name: "Vertical fixture", State: "active"}
	project.Organizer = &domain.Organizer{
		ID: domain.OrganizerID(project.ID), Mode: domain.OrganizerModeAdopt, Phase: domain.OrganizerPhaseActive,
		RepositoryPath: "/srv/organizers/" + project.ID, PreviewID: "preview-identity",
		OperationID: "operation-identity", HumanActorID: "human:test",
		ConfigurationSHA256: strings.Repeat("a", 64), OrganizerRevision: strings.Repeat("b", 40),
	}
	remote, err := repositorydomain.CanonicalRemote("https://github.com/example/" + project.ID + ".git")
	if err != nil {
		t.Fatal(err)
	}
	workspacePath, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	sourceStatus, err := os.Stat(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	commonPath, err := filepath.EvalSymlinks(filepath.Join(workspacePath, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	commonStatus, err := os.Stat(commonPath)
	if err != nil {
		t.Fatal(err)
	}
	sourceIdentity := sourceStatus.Sys().(*syscall.Stat_t)
	commonIdentity := commonStatus.Sys().(*syscall.Stat_t)
	workspace := domain.Workspace{
		ID: domain.WorkspaceID(project.ID, "workspace-"+suffix), ProjectID: project.ID,
		Key: "workspace-" + suffix, Name: "Vertical Workspace",
		Repository: domain.RepositoryIdentity{
			ID: remote.ID, Key: remote.Key, CanonicalRemote: remote.Canonical,
			SourcePath: workspacePath, SourceDevice: uint64(sourceIdentity.Dev), SourceInode: sourceIdentity.Ino,
			GitCommonDirectory: commonPath, GitCommonDevice: uint64(commonIdentity.Dev), GitCommonInode: commonIdentity.Ino,
		},
		DefaultBaseBranch: "main", Policy: domain.WorkspacePolicy{LaunchPolicy: "inherit", DeliveryMode: "inherit"},
	}
	task := domain.Task{
		ID: "task-" + suffix, ProjectID: project.ID, Title: "Implement the fake execution vertical path",
		Objective: "Produce one fake Candidate", AcceptanceCriteria: "criterion-1",
		WorkspaceIDs: []string{workspace.ID},
	}
	command := func(key, kind, aggregate string) domain.CommandRequest {
		return domain.CommandRequest{IdempotencyKey: key, Type: kind, AggregateID: aggregate, Payload: json.RawMessage(`{}`)}
	}
	event := func(id, aggregate, kind string) domain.Event {
		return domain.Event{ID: id, Sequence: 1, AggregateID: aggregate, Type: kind, Payload: json.RawMessage(`{}`)}
	}
	if result, err := store.CreateProject(ctx, command("create-project-"+suffix, "project.create", project.ID), project, []domain.Workspace{workspace}, event("project-created-"+suffix, project.ID, "project.created")); err != nil || result.Outcome != domain.CommandApplied {
		t.Fatalf("create Project: %#v, %v", result, err)
	}
	lease := domain.ProjectLeaseMutation{
		Kind: domain.ProjectLeaseAcquire, ProjectID: project.ID,
		HolderInstance: "engine-fixture", HolderProcessIdentity: "pid-100:start-1",
		DurationMillis: domain.MaximumProjectLeaseDurationMillis,
	}
	if result, err := store.ApplyProjectLease(ctx, domain.CommandRequest{
		IdempotencyKey: "lease-project-" + suffix, Type: "project.lease.acquire",
		AggregateID: project.ID, ExpectedVersion: 0, Payload: eventPayloadForTest(lease),
	}, lease); err != nil || result.Outcome != domain.CommandApplied {
		t.Fatalf("lease Project: %#v, %v", result, err)
	}
	project, err = store.Project(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := store.CreateTask(ctx, command("create-task-"+suffix, "task.create", task.ID), task, event("task-created-"+suffix, task.ID, "task.created")); err != nil || result.Outcome != domain.CommandApplied {
		t.Fatalf("create Task: %#v, %v", result, err)
	}
	return project, task
}

func eventPayloadForTest(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
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
	run("remote", "add", "origin", "https://github.com/example/project-"+suffix+".git")
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
		ID: id, ObservedAtMillis: time.Now().UnixMilli(),
		FreeDiskBasisPoints: measurement(5_000), WorktreeBytes: measurement(0),
		Processes: measurement(1), MemoryBytes: measurement(1),
		ElapsedMilliseconds: measurement(1), OutputBytes: measurement(0),
		TemporaryBytes: measurement(0),
	}
}

func eligibilityFacts(scope execution.Scope, surfaces execution.LifecycleSurfaces) eligibility.Facts {
	observedTrue := eligibility.BooleanFact{Observed: true, Value: true}
	operational := operationalObservation("launch-" + scope.RunID)
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
		OperationalObservation: operational,
		TaskStoreNowMillis:     operational.ObservedAtMillis,
	}
}

func verticalProfiles(t *testing.T) agentprofile.FrozenSet {
	t.Helper()
	readOnly := domainconfig.AgentSelection{
		Provider: domainconfig.ProviderCodex, Model: "gpt-5.4-mini", Effort: "high", Mode: "default",
		PermissionMode: "read-only", ProviderOptions: []domainconfig.ProviderOption{},
		MCPCapabilities: []domainconfig.MCPCapability{
			domainconfig.MCPProjectRead, domainconfig.MCPPlanningCommandSubmit,
			domainconfig.MCPCandidateRead, domainconfig.MCPReviewVerdictSubmit,
		},
	}
	worker := domainconfig.AgentSelection{
		Provider: domainconfig.ProviderCodex, Model: "gpt-5.4-mini", Effort: "high", Mode: "default",
		PermissionMode: "workspace-write", ProviderOptions: []domainconfig.ProviderOption{},
		MCPCapabilities: []domainconfig.MCPCapability{domainconfig.MCPTaskRead, domainconfig.MCPTaskOutcomeSubmit, domainconfig.MCPTaskHelperRequest},
	}
	profiles := domainconfig.AgentProfiles{
		Organizer: domainconfig.AgentProfile{AgentSelection: domainconfig.AgentSelection{
			Provider: readOnly.Provider, Model: readOnly.Model, Effort: readOnly.Effort, Mode: readOnly.Mode,
			PermissionMode: readOnly.PermissionMode, ProviderOptions: []domainconfig.ProviderOption{},
			MCPCapabilities: []domainconfig.MCPCapability{domainconfig.MCPProjectRead, domainconfig.MCPPlanningCommandSubmit},
		}, FallbackChain: []domainconfig.AgentSelection{}},
		Worker: domainconfig.AgentProfile{AgentSelection: worker, FallbackChain: []domainconfig.AgentSelection{}},
		Reviewer: domainconfig.AgentProfile{AgentSelection: domainconfig.AgentSelection{
			Provider: readOnly.Provider, Model: readOnly.Model, Effort: readOnly.Effort, Mode: readOnly.Mode,
			PermissionMode: readOnly.PermissionMode, ProviderOptions: []domainconfig.ProviderOption{},
			MCPCapabilities: []domainconfig.MCPCapability{domainconfig.MCPCandidateRead, domainconfig.MCPReviewVerdictSubmit},
		}, FallbackChain: []domainconfig.AgentSelection{}},
	}
	discovery := agentprofile.DiscoverySnapshot{
		SchemaVersion: agentprofile.DiscoverySchemaVersion, Revision: strings.Repeat("0", 64),
		PaseoVersion: agentprofile.SupportedPaseoVersion, ObservedAtMillis: 1_000, MaximumAgeMillis: 30_000,
		Providers: []agentprofile.ProviderFact{{
			Provider: domainconfig.ProviderCodex, CLIVersion: "0.147.0", State: agentprofile.ProviderReady,
			DiagnosticCodes: []agentprofile.DiagnosticCode{}, Models: []agentprofile.ModelFact{{
				Model: "gpt-5.4-mini", Variants: []agentprofile.VariantFact{
					{Effort: "high", Mode: "default", PermissionMode: "read-only", ProviderOptions: []domainconfig.ProviderOption{}, MCPCapabilities: readOnly.MCPCapabilities, SessionStdioMCP: true, ExactMCPToolPolicy: true, RuntimeProbePassed: true},
					{Effort: "high", Mode: "default", PermissionMode: "workspace-write", ProviderOptions: []domainconfig.ProviderOption{}, MCPCapabilities: append(append([]domainconfig.MCPCapability{}, worker.MCPCapabilities...), domainconfig.MCPHelperContributionSubmit), SessionStdioMCP: true, ExactMCPToolPolicy: true, RuntimeProbePassed: true},
				},
			}},
		}},
	}
	discovery, err := agentprofile.SealDiscovery(discovery)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := agentprofile.Freeze(agentprofile.FreezeRequest{
		Profiles: profiles, OrganizerRevision: strings.Repeat("b", 40),
		ExpectedOrganizerRevision: strings.Repeat("b", 40), ConfigurationSHA256: strings.Repeat("a", 64),
		Discovery: discovery, ExpectedDiscoveryRevision: discovery.Revision, NowMillis: 1_001,
	})
	if err != nil {
		t.Fatal(err)
	}
	return frozen
}

func startCommand(t *testing.T, task domain.Task, scope execution.Scope, source, worktree, base string, facts eligibility.Facts) executionapp.StartCommand {
	t.Helper()
	profiles := verticalProfiles(t)
	return executionapp.StartCommand{
		RequestID: "start-" + scope.RunID, Scope: scope, RunNumber: 1,
		SourcePath: source, WorktreePath: worktree,
		Branch: "task/" + scope.TaskID, BaseSHA: base,
		TaskTitle: task.Title, InitialPrompt: "Produce the declared fixture Candidate and return one completed claim.",
		CriterionIDs:    []string{"criterion-1"},
		RootWorkspaceID: "wks_root_" + scope.ProjectID,
		MCPServer: execution.MCPServerLaunch{
			Name: "director-session-mcp", Command: "/usr/bin/director-agent-runtime",
			Args: []string{"serve", "--run", scope.RunID}, Env: map[string]string{},
		},
		EffectiveProfiles: profiles,
		BudgetPolicy: runtimebudget.NewPolicy(
			profiles.ConfigurationSHA256(), 2*60*60*1_000, 200_000, 32, 0, 4,
		),
		TurnBudgetDemand: runtimebudget.Demand{WallTimeMilliseconds: 60_000, Tokens: 1_000, Turns: 1},
		HelperPolicy:     execution.HelperPolicy{MaximumPerTask: 3, MaximumConcurrentAgents: 8},
		EligibilityFacts: facts,
	}
}

func testNowMillis() int64 { return time.Now().UnixMilli() }

func runSteps(t *testing.T, store *dolt.DoltTaskStore, environment *fake.Environment, runID string, stop func(domain.Run) bool) domain.Run {
	t.Helper()
	ctx := context.Background()
	var last domain.Run
	for step := 0; step < 200; step++ {
		run, err := store.Run(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		last = run
		if stop(run) {
			return run
		}
		controller := executionapp.NewController(store, environment, environment, environment)
		nowMillis := testNowMillis()
		for _, effect := range []execution.Effect{run.Execution.Agent, run.Execution.AgentPrompt} {
			if effect.Observation != nil && effect.Observation.Status == execution.ObservationOwnedPresent {
				event, eventErr := environment.TerminalEvent(execution.CompletionEventFinished, effect.ID, nowMillis)
				if eventErr != nil {
					t.Fatal(eventErr)
				}
				if eventErr := controller.RecordCompletionEvent(ctx, runID, event, nowMillis); eventErr != nil {
					t.Fatal(eventErr)
				}
				continue
			}
		}
		_, err = controller.Step(ctx, runID, nowMillis)
		if err != nil && !errors.Is(err, fake.ErrResponseLost) {
			t.Fatalf("step %d: %v", step, err)
		}
	}
	t.Fatalf("execution path did not converge: version=%d needsYou=%#v worktree=%#v host=%#v boundary=%#v setup=%#v agent=%#v prompt=%#v budget=%#v", last.Version, last.Execution.NeedsYou, last.Execution.Worktree, last.Execution.HostView, last.Execution.Boundary, last.Execution.Setup, last.Execution.Agent, last.Execution.AgentPrompt, last.Execution.Budget)
	return domain.Run{}
}

func TestFakeExecutionVerticalPathRecoversEveryLostResponse(t *testing.T) {
	fixture := startVerticalDolt(t)
	store := openVerticalStore(t, fixture)
	source, base := initializeRepository(t, "complete")
	project, task := createVerticalRecords(t, store, "complete", source)
	worktree := filepath.Join(filepath.Dir(source), "owned-worktree")
	scope := execution.Scope{ProjectID: project.ID, WorkspaceID: task.WorkspaceIDs[0], TaskID: task.ID, RunID: "run-complete"}
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
	controller := executionapp.NewController(store, environment, environment, environment)
	started, err := controller.Start(context.Background(), startCommand(t, task, scope, source, worktree, base, facts))
	if err != nil || started.Decision.Kind != eligibility.DecisionEligible || started.RunID != scope.RunID {
		t.Fatalf("start = %#v, %v", started, err)
	}
	replayed, err := executionapp.NewController(store, environment, environment, environment).Start(
		context.Background(), startCommand(t, task, scope, source, worktree, base, facts),
	)
	if err != nil || replayed.RunID != started.RunID || replayed.Decision.DecisionID != started.Decision.DecisionID || environment.TotalMutationCount() != 0 {
		t.Fatalf("idempotent start replay = %#v, %v", replayed, err)
	}

	run := runSteps(t, store, environment, scope.RunID, func(run domain.Run) bool {
		return run.Execution.Agent.Phase == execution.EffectComplete
	})
	if run.Execution.Agent.ExternalID == "" || !run.Execution.PreparationReady ||
		run.Execution.EffectiveProfiles == nil || !run.Execution.EffectiveProfiles.Valid() ||
		run.Execution.EffectiveProfilesSHA256 != run.Execution.EffectiveProfiles.SHA256() {
		t.Fatalf("agent launch state = %#v", run.Execution)
	}
	workerProfile, ok := run.Execution.EffectiveProfiles.Role(agentprofile.RoleWorker)
	if !ok || workerProfile.Selection.Provider != domainconfig.ProviderCodex || workerProfile.Selection.PermissionMode != "workspace-write" {
		t.Fatalf("frozen Worker profile = %#v, %v", workerProfile, ok)
	}
	request := environment.AgentRequest()
	if request.ParentAgentID != nil || request.Title != task.Title || request.InitialPrompt != host.ZeroWorkBootstrapPrompt || request.NotifyOnFinish {
		t.Fatalf("top-level Task Agent request = %#v", request)
	}
	run = runSteps(t, store, environment, scope.RunID, func(run domain.Run) bool {
		return run.Execution.AgentPrompt.Observation != nil &&
			run.Execution.AgentPrompt.Observation.Status == execution.ObservationOwnedPresent
	})
	prompt := environment.PromptRequest()
	if prompt.AgentID != run.Execution.Agent.ExternalID || prompt.InitialPrompt != startCommand(t, task, scope, source, worktree, base, facts).InitialPrompt ||
		!prompt.NotifyOnFinish || len(prompt.Labels) != 0 || run.Execution.WorkerVisibility.AgentID != run.Execution.Agent.ExternalID {
		t.Fatalf("real Task prompt request = %#v, visibility = %#v", prompt, run.Execution.WorkerVisibility)
	}
	claim, err := environment.ProduceCandidate(context.Background(), fake.CandidateRequest{
		ClaimID: "claim-complete", AgentID: run.Execution.Agent.ExternalID,
		BaseSHA: base, CriteriaResults: map[string]string{"criterion-1": "claimed_satisfied"},
	})
	if err != nil {
		t.Fatal(err)
	}
	nowMillis := testNowMillis()
	completion, err := environment.TerminalEvent(execution.CompletionEventFinished, run.Execution.AgentPrompt.ID, nowMillis)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.RecordCompletionEvent(context.Background(), scope.RunID, completion, nowMillis); err != nil {
		t.Fatalf("record completion event: %v", err)
	}
	if err := executionapp.NewController(store, environment, environment, environment).RecordCompletionEvent(
		context.Background(), scope.RunID, completion, nowMillis,
	); err != nil {
		t.Fatalf("idempotent completion-event replay: %v", err)
	}
	conflict := completion
	conflict.Kind = execution.CompletionEventError
	conflict.FactHash = execution.CompletionEventHash(conflict)
	if err := controller.RecordCompletionEvent(context.Background(), scope.RunID, conflict, nowMillis); err == nil {
		t.Fatal("duplicate callback identity with changed facts was accepted")
	}
	if environment.QueueWakeCount() != 2 {
		t.Fatalf("bootstrap plus work reconciliation count = %d", environment.QueueWakeCount())
	}
	woken, err := store.Run(context.Background(), scope.RunID)
	if err != nil || woken.Execution.CompletionEventCursor != completion.Cursor ||
		woken.Execution.LastCompletionEvent == nil ||
		woken.Execution.LastCompletionEvent.FactHash != completion.FactHash ||
		len(woken.Execution.CompletionEventReceipts) != 2 ||
		woken.Execution.CompletionEventReceipts[1].DispatchLatencyMillis >= 1_000 ||
		!woken.Execution.OperationalObservationConsumed {
		t.Fatalf("durable completion wake = %#v, %v", woken.Execution, err)
	}
	olderDuplicate := woken.Execution.CompletionEventReceipts[0].Event
	if err := controller.RecordCompletionEvent(context.Background(), scope.RunID, olderDuplicate, 1_500); err != nil {
		t.Fatalf("older exact duplicate after later callback: %v", err)
	}
	if environment.QueueWakeCount() != 2 {
		t.Fatalf("older duplicate enqueued another reconciliation: %d", environment.QueueWakeCount())
	}
	run = runSteps(t, store, environment, scope.RunID, func(run domain.Run) bool {
		return run.Execution.AgentPrompt.Phase == execution.EffectComplete
	})
	controller = executionapp.NewController(store, environment, environment, environment)
	if err := controller.RecordCompletedClaim(context.Background(), scope.RunID, claim); err != nil {
		t.Fatal(err)
	}
	if err := executionapp.NewController(store, environment, environment, environment).RecordCompletedClaim(context.Background(), scope.RunID, claim); err != nil {
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
		execution.EffectAgentCreate, execution.EffectAgentPrompt, execution.EffectAgentArchive,
		execution.EffectHostViewArchive, execution.EffectWorktreeRemove,
	} {
		if count := environment.MutationCount(kind); count != 1 {
			t.Fatalf("%s mutation count = %d, want 1", kind, count)
		}
	}
	if order := environment.MutationOrder(); strings.Join(order, ",") != strings.Join([]string{
		string(execution.EffectWorktreeCreate), string(execution.EffectHostViewCreate),
		string(execution.EffectBoundaryMaterialize), string(execution.EffectSetupRun),
		string(execution.EffectAgentCreate), string(execution.EffectAgentPrompt), string(execution.EffectAgentArchive),
		string(execution.EffectHostViewArchive), string(execution.EffectWorktreeRemove),
	}, ",") {
		t.Fatalf("mutation order = %v", order)
	}
}

func TestPrimaryAgentDispatchIsCASSerializedAcrossConcurrentReconcilers(t *testing.T) {
	fixture := startVerticalDolt(t)
	store := openVerticalStore(t, fixture)
	source, base := initializeRepository(t, "primary-concurrent")
	project, task := createVerticalRecords(t, store, "primary-concurrent", source)
	worktree := filepath.Join(filepath.Dir(source), "primary-concurrent-worktree")
	scope := execution.Scope{ProjectID: project.ID, WorkspaceID: task.WorkspaceIDs[0], TaskID: task.ID, RunID: "run-primary-concurrent"}
	environment := fake.NewEnvironment(fake.Options{
		SourcePath: source, WorktreePath: worktree, Branch: "task/" + task.ID,
		BaseSHA: base, Operational: operationalObservation("primary-concurrent"),
	})
	t.Cleanup(environment.RemoveFixture)
	controller := executionapp.NewController(store, environment, environment, environment)
	if _, err := controller.Start(context.Background(), startCommand(t, task, scope, source, worktree, base, eligibilityFacts(scope, execution.LifecycleSurfaces{}))); err != nil {
		t.Fatal(err)
	}
	runSteps(t, store, environment, scope.RunID, func(run domain.Run) bool {
		reserved := false
		for _, reservation := range run.Execution.Budget.Reservations {
			reserved = reserved || (reservation.EffectID == run.Execution.Agent.ID && !reservation.Released)
		}
		return reserved && run.Execution.Agent.ID != "" && run.Execution.Agent.Observation != nil &&
			run.Execution.Agent.Observation.Status == execution.ObservationAbsent &&
			run.Execution.OperationalObservation != nil && !run.Execution.OperationalObservationConsumed &&
			run.Execution.OperationalObservationRunVersion == run.Version
	})

	const contenders = 16
	start := make(chan struct{})
	errorsChannel := make(chan error, contenders)
	var wait sync.WaitGroup
	for index := 0; index < contenders; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := executionapp.NewController(store, environment, environment, environment).Step(context.Background(), scope.RunID, testNowMillis())
			errorsChannel <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil && !strings.Contains(err.Error(), "version conflict") &&
			!strings.Contains(err.Error(), "IDEMPOTENCY_KEY_REUSED_WITH_DIFFERENT_PAYLOAD") {
			t.Fatalf("concurrent step returned unexpected error: %v", err)
		}
	}
	if count := environment.MutationCount(execution.EffectAgentCreate); count != 1 {
		t.Fatalf("concurrent reconcilers created %d primary agents", count)
	}
	owned := environment.AgentRequest()
	if owned.ParentAgentID != nil || owned.Title != task.Title || owned.InitialPrompt != host.ZeroWorkBootstrapPrompt {
		t.Fatalf("concurrent primary create = %#v", owned)
	}
}

func TestPrimaryLifecycleRefusesStaleLeaseAndWrongRepositoryBeforeMutation(t *testing.T) {
	fixture := startVerticalDolt(t)
	store := openVerticalStore(t, fixture)
	source, base := initializeRepository(t, "primary-authority")
	project, task := createVerticalRecords(t, store, "primary-authority", source)
	worktree := filepath.Join(filepath.Dir(source), "primary-authority-worktree")
	scope := execution.Scope{ProjectID: project.ID, WorkspaceID: task.WorkspaceIDs[0], TaskID: task.ID, RunID: "run-primary-authority"}
	environment := fake.NewEnvironment(fake.Options{
		SourcePath: source, WorktreePath: worktree, Branch: "task/" + task.ID,
		BaseSHA: base, Operational: operationalObservation("primary-authority"),
	})
	t.Cleanup(environment.RemoveFixture)
	controller := executionapp.NewController(store, environment, environment, environment)
	wrongSource := startCommand(t, task, scope, source, worktree, base, eligibilityFacts(scope, execution.LifecycleSurfaces{}))
	wrongSource.SourcePath = filepath.Join(source, "other")
	if _, err := controller.Start(context.Background(), wrongSource); err == nil {
		t.Fatal("wrong source repository entered a Run")
	}
	if environment.TotalMutationCount() != 0 || environment.HostCallCount() != 0 {
		t.Fatal("wrong source repository reached a side effect")
	}
	if _, err := controller.Start(context.Background(), startCommand(t, task, scope, source, worktree, base, eligibilityFacts(scope, execution.LifecycleSurfaces{}))); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Step(context.Background(), scope.RunID, project.Lease.ExpiresAtMillis); !errors.Is(err, executionapp.ErrProjectLeaseUnavailable) {
		t.Fatalf("expired lease step error = %v", err)
	}
	if environment.TotalMutationCount() != 0 || environment.HostCallCount() != 0 {
		t.Fatal("stale lease reached a side effect")
	}
}

func TestPrimaryLifecycleParksWrongWorktreePathBranchAndBaseBeforeDispatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*executionapp.StartCommand)
	}{
		{"worktree path", func(command *executionapp.StartCommand) { command.WorktreePath += "-wrong" }},
		{"branch", func(command *executionapp.StartCommand) { command.Branch += "-wrong" }},
		{"base", func(command *executionapp.StartCommand) { command.BaseSHA = strings.Repeat("f", 40) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := startVerticalDolt(t)
			store := openVerticalStore(t, fixture)
			suffix := "primary-wrong-" + strings.ReplaceAll(test.name, " ", "-")
			source, base := initializeRepository(t, suffix)
			project, task := createVerticalRecords(t, store, suffix, source)
			worktree := filepath.Join(filepath.Dir(source), suffix+"-worktree")
			scope := execution.Scope{ProjectID: project.ID, WorkspaceID: task.WorkspaceIDs[0], TaskID: task.ID, RunID: "run-" + suffix}
			environment := fake.NewEnvironment(fake.Options{
				SourcePath: source, WorktreePath: worktree, Branch: "task/" + task.ID,
				BaseSHA: base, Operational: operationalObservation(suffix),
			})
			t.Cleanup(environment.RemoveFixture)
			command := startCommand(t, task, scope, source, worktree, base, eligibilityFacts(scope, execution.LifecycleSurfaces{}))
			test.mutate(&command)
			controller := executionapp.NewController(store, environment, environment, environment)
			if _, err := controller.Start(context.Background(), command); err != nil {
				t.Fatal(err)
			}
			parked := runSteps(t, store, environment, scope.RunID, func(run domain.Run) bool { return run.Execution.NeedsYou != nil })
			if parked.Execution.NeedsYou.CleanupAuthorized || environment.TotalMutationCount() != 0 || environment.HostCallCount() != 0 {
				t.Fatalf("wrong target reached a side effect: needs=%#v mutations=%d host=%d", parked.Execution.NeedsYou, environment.TotalMutationCount(), environment.HostCallCount())
			}
		})
	}
}

func TestTerminalErrorAndPermissionCallbacksSynchronouslyEnqueueThenPark(t *testing.T) {
	for _, terminal := range []struct {
		name string
		kind execution.CompletionEventKind
		code execution.NeedCode
	}{
		{"error", execution.CompletionEventError, "agent_terminal_error"},
		{"permission", execution.CompletionEventPermission, "agent_terminal_permission"},
	} {
		t.Run(terminal.name, func(t *testing.T) {
			fixture := startVerticalDolt(t)
			store := openVerticalStore(t, fixture)
			source, base := initializeRepository(t, "terminal-"+terminal.name)
			project, task := createVerticalRecords(t, store, "terminal-"+terminal.name, source)
			worktree := filepath.Join(filepath.Dir(source), "terminal-"+terminal.name+"-worktree")
			scope := execution.Scope{ProjectID: project.ID, WorkspaceID: task.WorkspaceIDs[0], TaskID: task.ID, RunID: "run-terminal-" + terminal.name}
			environment := fake.NewEnvironment(fake.Options{
				SourcePath: source, WorktreePath: worktree, Branch: "task/" + task.ID,
				BaseSHA: base, Operational: operationalObservation("terminal-" + terminal.name),
			})
			t.Cleanup(environment.RemoveFixture)
			controller := executionapp.NewController(store, environment, environment, environment)
			if _, err := controller.Start(context.Background(), startCommand(t, task, scope, source, worktree, base, eligibilityFacts(scope, execution.LifecycleSurfaces{}))); err != nil {
				t.Fatal(err)
			}
			run := runSteps(t, store, environment, scope.RunID, func(run domain.Run) bool {
				return run.Execution.AgentPrompt.Observation != nil && run.Execution.AgentPrompt.Observation.Status == execution.ObservationOwnedPresent
			})
			nowMillis := testNowMillis()
			event, err := environment.TerminalEvent(terminal.kind, run.Execution.AgentPrompt.ID, nowMillis)
			if err != nil {
				t.Fatal(err)
			}
			if err := controller.RecordCompletionEvent(context.Background(), run.ID, event, nowMillis); err != nil {
				t.Fatal(err)
			}
			if environment.QueueWakeCount() != 2 {
				t.Fatalf("terminal reconciliation count = %d", environment.QueueWakeCount())
			}
			parked := runSteps(t, store, environment, scope.RunID, func(run domain.Run) bool { return run.Execution.NeedsYou != nil })
			if parked.Execution.NeedsYou.Code != terminal.code || parked.Execution.NeedsYou.CleanupAuthorized {
				t.Fatalf("terminal park = %#v", parked.Execution.NeedsYou)
			}
			if parked.Execution.Budget.Consumption.Turns != 2 || parked.Execution.Budget.Consumption.Tokens != 24 ||
				len(parked.Execution.Budget.ProviderSnapshots) != 2 {
				t.Fatalf("terminal usage was not accounted: %#v", parked.Execution.Budget)
			}
		})
	}
}

func TestFiveMinuteMultiSourceWatchdogRecoversOnlyALostTerminalEvent(t *testing.T) {
	fixture := startVerticalDolt(t)
	store := openVerticalStore(t, fixture)
	source, base := initializeRepository(t, "lost-terminal")
	project, task := createVerticalRecords(t, store, "lost-terminal", source)
	worktree := filepath.Join(filepath.Dir(source), "lost-terminal-worktree")
	scope := execution.Scope{ProjectID: project.ID, WorkspaceID: task.WorkspaceIDs[0], TaskID: task.ID, RunID: "run-lost-terminal"}
	environment := fake.NewEnvironment(fake.Options{
		SourcePath: source, WorktreePath: worktree, Branch: "task/" + task.ID,
		BaseSHA: base, Operational: operationalObservation("lost-terminal"),
	})
	t.Cleanup(environment.RemoveFixture)
	controller := executionapp.NewController(store, environment, environment, environment)
	if _, err := controller.Start(context.Background(), startCommand(t, task, scope, source, worktree, base, eligibilityFacts(scope, execution.LifecycleSurfaces{}))); err != nil {
		t.Fatal(err)
	}
	run := runSteps(t, store, environment, scope.RunID, func(run domain.Run) bool {
		return run.Execution.AgentPrompt.Observation != nil && run.Execution.AgentPrompt.Observation.Status == execution.ObservationOwnedPresent
	})
	lastProgress := testNowMillis()
	facts := execution.StallRecoveryFacts{
		ObservedAtMillis: lastProgress + execution.LostCompletionEventRecoveryMillis, LastProgressAtMillis: lastProgress,
		AgentStateUnchanged: true, NoToolActivity: true, NoWorktreeChange: true,
		NoUsageMovement: true, PendingTerminalEffectID: run.Execution.AgentPrompt.ID,
	}
	early := facts
	early.ObservedAtMillis--
	if err := controller.RecoverLostCompletionEvent(context.Background(), run.ID, early); err == nil {
		t.Fatal("early single-source wake was admitted")
	}
	if _, err := environment.TerminalEvent(execution.CompletionEventFinished, run.Execution.AgentPrompt.ID, testNowMillis()); err != nil {
		t.Fatal(err)
	}
	if err := controller.RecoverLostCompletionEvent(context.Background(), run.ID, facts); err != nil {
		t.Fatal(err)
	}
	completed := runSteps(t, store, environment, scope.RunID, func(run domain.Run) bool {
		return run.Execution.AgentPrompt.Phase == execution.EffectComplete
	})
	if completed.Execution.AgentPrompt.ExternalID != completed.Execution.Agent.ExternalID || environment.QueueWakeCount() != 1 {
		t.Fatalf("lost-event recovery = %#v, queue count %d", completed.Execution.AgentPrompt, environment.QueueWakeCount())
	}
}

func TestLifecycleAndPeriodicFactsParkWithoutSDKOrCleanupAuthority(t *testing.T) {
	fixture := startVerticalDolt(t)
	store := openVerticalStore(t, fixture)

	t.Run("unapproved lifecycle", func(t *testing.T) {
		source, base := initializeRepository(t, "lifecycle")
		project, task := createVerticalRecords(t, store, "lifecycle", source)
		worktree := filepath.Join(filepath.Dir(source), "unapproved-worktree")
		scope := execution.Scope{ProjectID: project.ID, WorkspaceID: task.WorkspaceIDs[0], TaskID: task.ID, RunID: "run-lifecycle"}
		surfaces := execution.LifecycleSurfaces{Setup: []string{"must never execute"}}
		environment := fake.NewEnvironment(fake.Options{SourcePath: source, WorktreePath: worktree, Branch: "task/" + task.ID, BaseSHA: base, Operational: operationalObservation("periodic-lifecycle")})
		t.Cleanup(environment.RemoveFixture)
		controller := executionapp.NewController(store, environment, environment, environment)
		result, err := controller.Start(context.Background(), startCommand(t, task, scope, source, worktree, base, eligibilityFacts(scope, surfaces)))
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
			source, base := initializeRepository(t, suffix)
			project, task := createVerticalRecords(t, store, suffix, source)
			worktree := filepath.Join(filepath.Dir(source), suffix+"-worktree")
			scope := execution.Scope{ProjectID: project.ID, WorkspaceID: task.WorkspaceIDs[0], TaskID: task.ID, RunID: "run-" + suffix}
			facts := eligibilityFacts(scope, execution.LifecycleSurfaces{})
			fixture.mutate(&facts)
			environment := fake.NewEnvironment(fake.Options{SourcePath: source, WorktreePath: worktree, Branch: "task/" + task.ID, BaseSHA: base, Operational: operationalObservation("periodic-unused")})
			t.Cleanup(environment.RemoveFixture)
			controller := executionapp.NewController(store, environment, environment, environment)
			result, err := controller.Start(context.Background(), startCommand(t, task, scope, source, worktree, base, facts))
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
			source, base := initializeRepository(t, suffix)
			project, task := createVerticalRecords(t, store, suffix, source)
			worktree := filepath.Join(filepath.Dir(source), suffix+"-worktree")
			scope := execution.Scope{ProjectID: project.ID, WorkspaceID: task.WorkspaceIDs[0], TaskID: task.ID, RunID: "run-" + suffix}
			facts := eligibilityFacts(scope, execution.LifecycleSurfaces{})
			environment := fake.NewEnvironment(fake.Options{SourcePath: source, WorktreePath: worktree, Branch: "task/" + task.ID, BaseSHA: base, Operational: operationalObservation("periodic-valid")})
			t.Cleanup(environment.RemoveFixture)
			controller := executionapp.NewController(store, environment, environment, environment)
			if _, err := controller.Start(context.Background(), startCommand(t, task, scope, source, worktree, base, facts)); err != nil {
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
