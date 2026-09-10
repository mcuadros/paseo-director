// SPDX-License-Identifier: Apache-2.0

package agentbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/mcuadros/director-engine/domain"
	domainbridge "github.com/mcuadros/director-engine/domain/agentbridge"
	"github.com/mcuadros/director-engine/domain/agentprofile"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	domainexecution "github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/domain/jsondocument"
	storeport "github.com/mcuadros/director-engine/ports/taskstore"
)

type fakeDiscovery struct {
	snapshot agentprofile.DiscoverySnapshot
}

func (adapter *fakeDiscovery) Discover(context.Context) (agentprofile.DiscoverySnapshot, error) {
	return adapter.snapshot, nil
}

type memoryStore struct {
	mu         sync.Mutex
	project    domain.Project
	workspace  domain.Workspace
	task       domain.Task
	run        domain.Run
	candidate  domain.Candidate
	commands   map[string]domain.Command
	writeCount int
}

func (store *memoryStore) Project(_ context.Context, id string) (domain.Project, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if id != store.project.ID {
		return domain.Project{}, storeport.ErrNotFound
	}
	return store.project, nil
}

func (store *memoryStore) Workspace(_ context.Context, id string) (domain.Workspace, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if id != store.workspace.ID {
		return domain.Workspace{}, storeport.ErrNotFound
	}
	return store.workspace, nil
}

func (store *memoryStore) Task(_ context.Context, id string) (domain.Task, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if id != store.task.ID {
		return domain.Task{}, storeport.ErrNotFound
	}
	return store.task, nil
}

func (store *memoryStore) Run(_ context.Context, id string) (domain.Run, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if id != store.run.ID {
		return domain.Run{}, storeport.ErrNotFound
	}
	return store.run, nil
}

func (store *memoryStore) Candidate(_ context.Context, id string) (domain.Candidate, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if id != store.candidate.ID {
		return domain.Candidate{}, storeport.ErrNotFound
	}
	return store.candidate, nil
}

func (store *memoryStore) Command(_ context.Context, key string) (domain.Command, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	command, ok := store.commands[key]
	if !ok {
		return domain.Command{}, storeport.ErrNotFound
	}
	command.Payload = slices.Clone(command.Payload)
	return command, nil
}

func (store *memoryStore) UpdateRun(_ context.Context, request domain.CommandRequest, next domain.Run, event domain.Event) (domain.CommandResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	canonical, err := jsondocument.Canonical(request.Payload)
	if err != nil {
		return domain.CommandResult{}, err
	}
	if stored, exists := store.commands[request.IdempotencyKey]; exists {
		storedPayload, _ := jsondocument.Canonical(stored.Payload)
		if stored.Type != request.Type || stored.AggregateID != request.AggregateID ||
			stored.ExpectedVersion != request.ExpectedVersion || !bytes.Equal(storedPayload, canonical) {
			return domain.CommandResult{}, storeport.ErrIdempotencyConflict
		}
		return domain.CommandResult{Outcome: stored.Outcome, ObservedVersion: stored.ObservedVersion, EventID: stored.EventID, Replay: true}, nil
	}
	command := domain.Command{
		IdempotencyKey: request.IdempotencyKey, Type: request.Type, AggregateID: request.AggregateID,
		ExpectedVersion: request.ExpectedVersion, Payload: slices.Clone(canonical), PayloadHash: strings.Repeat("a", 64),
	}
	if request.ExpectedVersion != store.run.Version {
		command.Outcome = domain.CommandRejectedVersionConflict
		command.ObservedVersion = store.run.Version
		store.commands[request.IdempotencyKey] = command
		return domain.CommandResult{Outcome: command.Outcome, ObservedVersion: command.ObservedVersion}, nil
	}
	if next.Version != store.run.Version+1 || event.AggregateVersion != next.Version || event.AggregateID != next.ID {
		return domain.CommandResult{}, storeport.ErrInvalidRecord
	}
	store.run = next
	store.writeCount++
	command.Outcome = domain.CommandApplied
	command.ObservedVersion = next.Version
	command.EventID = event.ID
	store.commands[request.IdempotencyKey] = command
	return domain.CommandResult{Outcome: command.Outcome, ObservedVersion: next.Version, EventID: event.ID}, nil
}

func profileSelection(role agentprofile.Role) domainconfig.AgentSelection {
	selection := domainconfig.AgentSelection{
		Effort: "high", Mode: "default", PermissionMode: "read-only",
		ProviderOptions: []domainconfig.ProviderOption{{Name: domainconfig.ProviderOptionNetworkAccess, Value: "disabled"}},
	}
	switch role {
	case agentprofile.RoleOrganizer:
		selection.Provider = domainconfig.ProviderCodex
		selection.Model = "gpt-5.4-mini"
		selection.MCPCapabilities = []domainconfig.MCPCapability{domainconfig.MCPProjectRead, domainconfig.MCPPlanningCommandSubmit}
	case agentprofile.RoleWorker:
		selection.Provider = domainconfig.ProviderClaudeCode
		selection.Model = "claude-haiku-4-5"
		selection.PermissionMode = "workspace-write"
		selection.MCPCapabilities = []domainconfig.MCPCapability{domainconfig.MCPProjectRead, domainconfig.MCPTaskRead, domainconfig.MCPTaskOutcomeSubmit, domainconfig.MCPTaskHelperRequest}
	case agentprofile.RoleReviewer:
		selection.Provider = domainconfig.ProviderOpenCode
		selection.Model = "opencode/nemotron-3-ultra-free"
		selection.MCPCapabilities = []domainconfig.MCPCapability{domainconfig.MCPCandidateRead, domainconfig.MCPReviewVerdictSubmit}
	}
	return selection
}

func serviceProfiles() domainconfig.AgentProfiles {
	return domainconfig.AgentProfiles{
		Organizer: domainconfig.AgentProfile{AgentSelection: profileSelection(agentprofile.RoleOrganizer), FallbackChain: []domainconfig.AgentSelection{}},
		Worker:    domainconfig.AgentProfile{AgentSelection: profileSelection(agentprofile.RoleWorker), FallbackChain: []domainconfig.AgentSelection{}},
		Reviewer:  domainconfig.AgentProfile{AgentSelection: profileSelection(agentprofile.RoleReviewer), FallbackChain: []domainconfig.AgentSelection{}},
	}
}

func serviceDiscovery(t *testing.T, profiles domainconfig.AgentProfiles) agentprofile.DiscoverySnapshot {
	t.Helper()
	provider := func(selection domainconfig.AgentSelection, cliVersion string) agentprofile.ProviderFact {
		availableCapabilities := slices.Clone(selection.MCPCapabilities)
		if selection.Provider == profiles.Worker.Provider {
			availableCapabilities = append(availableCapabilities, domainconfig.MCPHelperContributionSubmit)
		}
		return agentprofile.ProviderFact{
			Provider: selection.Provider, CLIVersion: cliVersion, State: agentprofile.ProviderReady,
			DiagnosticCodes: []agentprofile.DiagnosticCode{}, Models: []agentprofile.ModelFact{{
				Model: selection.Model, Variants: []agentprofile.VariantFact{{
					Effort: selection.Effort, Mode: selection.Mode, PermissionMode: selection.PermissionMode,
					ProviderOptions: slices.Clone(selection.ProviderOptions), MCPCapabilities: availableCapabilities,
					SessionStdioMCP: true, ExactMCPToolPolicy: true, RuntimeProbePassed: true,
				}},
			}},
		}
	}
	snapshot := agentprofile.DiscoverySnapshot{
		SchemaVersion: agentprofile.DiscoverySchemaVersion, Revision: strings.Repeat("0", 64),
		PaseoVersion: agentprofile.SupportedPaseoVersion, ObservedAtMillis: 1_000, MaximumAgeMillis: 30_000,
		Providers: []agentprofile.ProviderFact{
			provider(profiles.Organizer.AgentSelection, "0.147.0"),
			provider(profiles.Worker.AgentSelection, "2.1.258"),
			provider(profiles.Reviewer.AgentSelection, "1.18.18"),
		},
	}
	sealed, err := agentprofile.SealDiscovery(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

type serviceFixture struct {
	store     *memoryStore
	discovery *fakeDiscovery
	service   *Service
	frozen    agentprofile.FrozenSet
}

func newServiceFixture(t *testing.T) *serviceFixture {
	t.Helper()
	profiles := serviceProfiles()
	discovery := serviceDiscovery(t, profiles)
	frozen, err := agentprofile.Freeze(agentprofile.FreezeRequest{
		Profiles: profiles, OrganizerRevision: strings.Repeat("a", 40), ExpectedOrganizerRevision: strings.Repeat("a", 40),
		ConfigurationSHA256: strings.Repeat("b", 64), Discovery: discovery,
		ExpectedDiscoveryRevision: discovery.Revision, NowMillis: 1_001,
	})
	if err != nil {
		t.Fatal(err)
	}
	scope := domainexecution.Scope{ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "task-1", RunID: "run-1"}
	store := &memoryStore{
		project: domain.Project{ID: scope.ProjectID, Name: "Director project", State: "active", Version: 3, Organizer: &domain.Organizer{
			OrganizerRevision: strings.Repeat("a", 40), ConfigurationSHA256: strings.Repeat("b", 64),
		}},
		workspace: domain.Workspace{ID: scope.WorkspaceID, ProjectID: scope.ProjectID, Name: "Workspace", Version: 1},
		task: domain.Task{
			ID: scope.TaskID, ProjectID: scope.ProjectID, Key: "DIR-1", Title: "Implement bridge", Objective: "Keep scope fixed",
			AcceptanceCriteria: "criterion-1", WorkspaceIDs: []string{scope.WorkspaceID}, Priority: domain.PriorityNormal, Version: 2,
		},
		run: domain.Run{
			ID: scope.RunID, TaskID: scope.TaskID, Number: 1, BaseSHA: strings.Repeat("c", 40),
			CurrentCandidateID: "candidate-1", Version: 4,
			Execution: domainexecution.State{
				SchemaVersion: domainexecution.SchemaVersion, Scope: scope, EffectiveProfiles: &frozen,
				EffectiveProfilesSHA256: frozen.SHA256(), CriterionIDs: []string{"criterion-1"},
				WorktreePath: "/tmp/director-mcp-primary", HelperPolicy: domainexecution.HelperPolicy{MaximumPerTask: 3, MaximumConcurrentAgents: 8},
				PrimarySession: domainexecution.PrimarySession{NativeAgentID: "worker-agent", PermissionMode: "workspace-write"},
				Agent:          domainexecution.Effect{ExternalID: "worker-agent"}, AgentPrompt: domainexecution.Effect{Phase: domainexecution.EffectComplete},
			},
		},
		candidate: domain.Candidate{ID: "candidate-1", RunID: scope.RunID, Sequence: 1, CommitSHA: strings.Repeat("d", 40)},
		commands:  make(map[string]domain.Command),
	}
	provider := &fakeDiscovery{snapshot: discovery}
	service, err := NewService(store, provider)
	if err != nil {
		t.Fatal(err)
	}
	return &serviceFixture{store: store, discovery: provider, service: service, frozen: frozen}
}

func (fixture *serviceFixture) binding(t *testing.T, role agentprofile.Role) domainbridge.SessionBinding {
	t.Helper()
	fixture.store.mu.Lock()
	run := fixture.store.run
	project := fixture.store.project
	workspace := fixture.store.workspace
	task := fixture.store.task
	fixture.store.mu.Unlock()
	stateHash, err := RunStateSHA256(run.Execution)
	if err != nil {
		t.Fatal(err)
	}
	binding := domainbridge.SessionBinding{
		SchemaVersion: domainbridge.SessionSchemaVersion, Audience: "session-" + string(role), Role: role,
		ProjectID: project.ID, WorkspaceID: workspace.ID, TaskID: task.ID, RunID: run.ID,
		NativeAgentID: "agent-" + string(role), TurnID: "turn-" + string(role),
		ExpectedProjectVersion: project.Version, ExpectedWorkspaceVersion: workspace.Version,
		ExpectedTaskVersion: task.Version, ExpectedRunVersion: run.Version, ExpectedRunStateSHA256: stateHash,
		EffectiveProfilesSHA256: fixture.frozen.SHA256(), OrganizerRevision: fixture.frozen.OrganizerRevision(),
		ConfigurationSHA256: fixture.frozen.ConfigurationSHA256(), ProviderDiscoveryRevision: fixture.discovery.snapshot.Revision,
	}
	if role == agentprofile.RoleWorker {
		binding.NativeAgentID = "worker-agent"
	}
	if role == agentprofile.RoleReviewer {
		binding.CandidateID = fixture.store.candidate.ID
		binding.CandidateSHA = fixture.store.candidate.CommitSHA
	}
	if role == agentprofile.RoleHelper {
		if len(run.Execution.Helpers) == 0 {
			t.Fatal("helper binding requested without a durable helper")
		}
		binding.HelperID = run.Execution.Helpers[0].ID
		binding.NativeAgentID = run.Execution.Helpers[0].NativeAgentID
	}
	return binding
}

func failureCode(t *testing.T, err error) Code {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v, want Failure", err)
	}
	return failure.Code
}

func TestSessionsExposeOnlyRoleProfileCapabilities(t *testing.T) {
	fixture := newServiceFixture(t)
	wants := map[agentprofile.Role][]string{
		agentprofile.RoleOrganizer: {"director_project_read", "director_planning_command_submit"},
		agentprofile.RoleWorker:    {"director_project_read", "director_task_read", "director_task_outcome_submit", "director_task_helper_request"},
		agentprofile.RoleReviewer:  {"director_candidate_read", "director_review_verdict_submit"},
	}
	for role, want := range wants {
		session, err := fixture.service.OpenSession(context.Background(), fixture.binding(t, role), 1_001)
		if err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		var names []string
		for _, tool := range session.Descriptor().Tools {
			names = append(names, tool.Name)
		}
		if !slices.Equal(names, want) {
			t.Fatalf("%s tools = %v, want %v", role, names, want)
		}
		if _, err := session.Call(context.Background(), "unknown-call", "raw_taskstore_query", json.RawMessage(`{}`)); failureCode(t, err) != CodeToolNotAllowed {
			t.Fatalf("unknown tool error = %v", err)
		}
	}
}

func TestWorkerRequestsHelperAndOnlyBoundHelperSubmitsContribution(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.store.run.CurrentCandidateID = ""
	workerBinding := fixture.binding(t, agentprofile.RoleWorker)
	worker, err := fixture.service.OpenSession(context.Background(), workerBinding, 1_001)
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.Call(context.Background(), "helper-request-1", "director_task_helper_request", json.RawMessage(`{"mode":"writer","purpose":"implement isolated change"}`))
	if err != nil {
		t.Fatal(err)
	}
	var receipt commandOutput
	if json.Unmarshal(result.Payload, &receipt) != nil || receipt.HelperID == "" {
		t.Fatalf("helper receipt = %s", result.Payload)
	}
	fixture.store.mu.Lock()
	if len(fixture.store.run.Execution.Helpers) != 1 || fixture.store.run.Execution.Helpers[0].ID != receipt.HelperID ||
		fixture.store.run.Execution.Helpers[0].ParentAgentID != workerBinding.NativeAgentID ||
		fixture.store.run.Execution.Helpers[0].WorktreePath == fixture.store.run.Execution.WorktreePath {
		fixture.store.mu.Unlock()
		t.Fatalf("durable helper request = %#v", fixture.store.run.Execution.Helpers)
	}
	fixture.store.run.Execution.Helpers[0].Phase = domainexecution.HelperActive
	fixture.store.run.Execution.Helpers[0].NativeAgentID = "native-helper-1"
	fixture.store.run.Execution.Helpers[0].Admission = &domainexecution.HelperAdmission{
		ID: "admission-1", HelperID: receipt.HelperID, ParentAgentID: workerBinding.NativeAgentID,
		ExecutionWorkspaceID: "native-workspace", Mode: domainexecution.HelperWriter,
		ProfileSHA256: fixture.frozen.SHA256(), SessionSHA256: strings.Repeat("e", 64), LabelDigest: strings.Repeat("f", 64),
		RegisteredAt: "2026-09-10T00:00:00Z", StartedAt: "2026-09-10T00:00:00Z", IssuedAtMillis: 1_000, ConsumedAtMillis: 1_001,
		InvocationRequestID: "invoke-helper-1",
	}
	fixture.store.mu.Unlock()
	helperBinding := fixture.binding(t, agentprofile.RoleHelper)
	helper, err := fixture.service.OpenSession(context.Background(), helperBinding, 1_001)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, tool := range helper.Descriptor().Tools {
		names = append(names, tool.Name)
	}
	if !slices.Equal(names, []string{"director_task_read", "director_helper_contribution_submit"}) {
		t.Fatalf("helper tools = %v", names)
	}
	commit := strings.Repeat("d", 40)
	if _, err := helper.Call(context.Background(), "contribution-1", "director_helper_contribution_submit", json.RawMessage(`{"commitSha":"`+commit+`","baseSha":"`+fixture.store.run.BaseSHA+`"}`)); err != nil {
		t.Fatal(err)
	}
	fixture.store.mu.Lock()
	stored := fixture.store.run.Execution.Helpers[0]
	fixture.store.mu.Unlock()
	if stored.Phase != domainexecution.HelperContributionReady || stored.Contribution == nil || stored.Contribution.CommitSHA != commit {
		t.Fatalf("helper contribution = %#v", stored)
	}

	forged := helperBinding
	forged.HelperID = "helper-other"
	if _, err := fixture.service.OpenSession(context.Background(), forged, 1_001); failureCode(t, err) != CodeScopeMismatch {
		t.Fatalf("forged helper scope = %v", err)
	}
}

func TestFixedScopeReadsAreBoundedAndRedacted(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.store.project.Name = "token=github_pat_abcdefghijklmnop"
	session, err := fixture.service.OpenSession(context.Background(), fixture.binding(t, agentprofile.RoleWorker), 1_001)
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.Call(context.Background(), "read-project", "director_project_read", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(result.Payload, []byte("github_pat_")) || !bytes.Contains(result.Payload, []byte("[REDACTED]")) ||
		bytes.Contains(result.Payload, []byte("sourcePath")) || bytes.Contains(result.Payload, []byte("repository")) {
		t.Fatalf("unsafe Project output: %s", result.Payload)
	}
	if _, err := session.Call(context.Background(), "forged-read", "director_task_read", json.RawMessage(`{"taskId":"task-other"}`)); failureCode(t, err) != CodeInputInvalid {
		t.Fatalf("forged scope error = %v", err)
	}
}

func TestWorkerCommandIsDurableIdempotentAndRestartReplaySafe(t *testing.T) {
	fixture := newServiceFixture(t)
	binding := fixture.binding(t, agentprofile.RoleWorker)
	session, err := fixture.service.OpenSession(context.Background(), binding, 1_001)
	if err != nil {
		t.Fatal(err)
	}
	claim := json.RawMessage(fmt.Sprintf(`{"outcome":"completed","candidateSha":"%s","baseSha":"%s","criteriaResults":{"criterion-1":"claimed_satisfied"},"residualRiskCodes":[]}`, strings.Repeat("e", 40), strings.Repeat("c", 40)))
	first, err := session.Call(context.Background(), "provider-call-1", "director_task_outcome_submit", claim)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(first.Payload, []byte(`"status":"accepted"`)) || !bytes.Contains(first.Payload, []byte(`"replay":false`)) {
		t.Fatalf("first result = %s", first.Payload)
	}
	if fixture.store.writeCount != 1 || len(fixture.store.run.Execution.MCPCommandReceipts) != 1 || len(fixture.store.commands) != 1 {
		t.Fatalf("durable facts = writes %d receipts %d commands %d", fixture.store.writeCount, len(fixture.store.run.Execution.MCPCommandReceipts), len(fixture.store.commands))
	}
	// Reconstruct both service and session over the same durable adapter.
	restarted, _ := NewService(fixture.store, fixture.discovery)
	restartedSession, err := restarted.OpenSession(context.Background(), binding, 1_001)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := restartedSession.Call(context.Background(), "provider-call-1", "director_task_outcome_submit", claim)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(replay.Payload, []byte(`"replay":true`)) || fixture.store.writeCount != 1 || len(fixture.store.run.Execution.MCPCommandReceipts) != 1 {
		t.Fatalf("restart replay = %s, writes %d receipts %d", replay.Payload, fixture.store.writeCount, len(fixture.store.run.Execution.MCPCommandReceipts))
	}
	changed := bytes.Replace(claim, []byte("claimed_satisfied"), []byte("claimed_unsatisfied"), 1)
	if _, err := restartedSession.Call(context.Background(), "provider-call-1", "director_task_outcome_submit", changed); failureCode(t, err) != CodeIdempotencyConflict {
		t.Fatalf("changed replay error = %v", err)
	}
}

func TestConcurrentReplayHasOneLogicalCommand(t *testing.T) {
	fixture := newServiceFixture(t)
	session, err := fixture.service.OpenSession(context.Background(), fixture.binding(t, agentprofile.RoleWorker), 1_001)
	if err != nil {
		t.Fatal(err)
	}
	claim := json.RawMessage(fmt.Sprintf(`{"outcome":"needs_review","candidateSha":"%s","baseSha":"%s","criterionIds":["criterion-1"]}`, strings.Repeat("e", 40), strings.Repeat("c", 40)))
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 16)
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, callErr := session.Call(context.Background(), "same-provider-call", "director_task_outcome_submit", claim)
			errorsSeen <- callErr
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for callErr := range errorsSeen {
		if callErr != nil {
			t.Errorf("concurrent replay: %v", callErr)
		}
	}
	if fixture.store.writeCount != 1 || len(fixture.store.commands) != 1 || len(fixture.store.run.Execution.MCPCommandReceipts) != 1 {
		t.Fatalf("concurrent durable facts = writes %d commands %d receipts %d", fixture.store.writeCount, len(fixture.store.commands), len(fixture.store.run.Execution.MCPCommandReceipts))
	}
}

func TestStaleVersionIsDurableAndOtherExpectedStateDriftRefuses(t *testing.T) {
	fixture := newServiceFixture(t)
	binding := fixture.binding(t, agentprofile.RoleOrganizer)
	session, err := fixture.service.OpenSession(context.Background(), binding, 1_001)
	if err != nil {
		t.Fatal(err)
	}
	fixture.store.run.Version++
	command := json.RawMessage(`{"kind":"task_update_proposal","title":"New title","objective":"New objective","acceptanceCriteria":["Criterion"],"priority":"normal","labels":[]}`)
	result, err := session.Call(context.Background(), "stale-call", "director_planning_command_submit", command)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(result.Payload, []byte(`"status":"version_conflict"`)) || len(fixture.store.commands) != 1 || fixture.store.writeCount != 0 {
		t.Fatalf("stale result = %s, commands %d writes %d", result.Payload, len(fixture.store.commands), fixture.store.writeCount)
	}
	replay, err := session.Call(context.Background(), "stale-call", "director_planning_command_submit", command)
	if err != nil || !bytes.Contains(replay.Payload, []byte(`"replay":true`)) {
		t.Fatalf("stale replay = %s, %v", replay.Payload, err)
	}

	secondFixture := newServiceFixture(t)
	secondBinding := secondFixture.binding(t, agentprofile.RoleOrganizer)
	secondSession, _ := secondFixture.service.OpenSession(context.Background(), secondBinding, 1_001)
	secondFixture.store.project.Version++
	if _, err := secondSession.Call(context.Background(), "project-drift", "director_planning_command_submit", command); failureCode(t, err) != CodeExpectedState {
		t.Fatalf("Project drift error = %v", err)
	}
	if len(secondFixture.store.commands) != 0 {
		t.Fatal("non-Run expected-state drift reached the Command store")
	}
}

func TestReviewerCandidateScopeAndVerdictAreExact(t *testing.T) {
	fixture := newServiceFixture(t)
	binding := fixture.binding(t, agentprofile.RoleReviewer)
	session, err := fixture.service.OpenSession(context.Background(), binding, 1_001)
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.Call(context.Background(), "candidate-read", "director_candidate_read", json.RawMessage(`{}`))
	if err != nil || !bytes.Contains(result.Payload, []byte(binding.CandidateSHA)) || bytes.Contains(result.Payload, []byte("worktree")) {
		t.Fatalf("Candidate read = %s, %v", result.Payload, err)
	}
	verdict := json.RawMessage(`{"verdict":"approve_candidate","coverage":["acceptance","correctness","security","maintainability","readability","design","quality","rigor"],"findings":[],"residualRiskCodes":[]}`)
	if _, err := session.Call(context.Background(), "verdict-call", "director_review_verdict_submit", verdict); err != nil {
		t.Fatal(err)
	}
	stored := fixture.store.commands[fixture.store.run.Execution.MCPCommandReceipts[0].CommandKey]
	if !bytes.Contains(stored.Payload, []byte(`"candidateId":"candidate-1"`)) || !bytes.Contains(stored.Payload, []byte(binding.CandidateSHA)) {
		t.Fatalf("stored verdict lacks injected Candidate binding: %s", stored.Payload)
	}
	forged := bytes.Replace(verdict, []byte(`"verdict"`), []byte(`"candidateId":"candidate-2","verdict"`), 1)
	if _, err := session.Call(context.Background(), "forged-verdict", "director_review_verdict_submit", forged); failureCode(t, err) != CodeInputInvalid {
		t.Fatalf("forged Candidate error = %v", err)
	}
}

func TestProfileConfigAndProviderDriftNeverWidensARecoveredSession(t *testing.T) {
	fixture := newServiceFixture(t)
	binding := fixture.binding(t, agentprofile.RoleWorker)
	fixture.store.project.Organizer.OrganizerRevision = strings.Repeat("f", 40)
	session, err := fixture.service.OpenSession(context.Background(), binding, 1_001)
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Descriptor().Tools) != 4 {
		t.Fatal("active Organizer drift changed the immutable Run catalog")
	}
	// A fresh discovery fact may expose more capabilities, but only the frozen
	// role set can become tools.
	fixture.discovery.snapshot.Providers[1].Models[0].Variants[0].MCPCapabilities = append(
		fixture.discovery.snapshot.Providers[1].Models[0].Variants[0].MCPCapabilities,
		domainconfig.MCPCandidateRead,
	)
	sealed, err := agentprofile.SealDiscovery(fixture.discovery.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	fixture.discovery.snapshot = sealed
	binding.ProviderDiscoveryRevision = sealed.Revision
	refreshed, err := fixture.service.OpenSession(context.Background(), binding, 1_001)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := refreshed.Call(context.Background(), "cross-role", "director_candidate_read", json.RawMessage(`{}`)); failureCode(t, err) != CodeToolNotAllowed {
		t.Fatalf("discovery drift widened catalog: %v", err)
	}
	fixture.store.run.Execution.EffectiveProfilesSHA256 = strings.Repeat("0", 64)
	if _, err := fixture.service.OpenSession(context.Background(), binding, 1_001); failureCode(t, err) != CodeScopeMismatch {
		t.Fatalf("profile mismatch error = %v", err)
	}
}

func TestProviderCapabilityLossFailsBeforeSessionCatalog(t *testing.T) {
	fixture := newServiceFixture(t)
	binding := fixture.binding(t, agentprofile.RoleWorker)
	for providerIndex := range fixture.discovery.snapshot.Providers {
		if fixture.discovery.snapshot.Providers[providerIndex].Provider == domainconfig.ProviderClaudeCode {
			fixture.discovery.snapshot.Providers[providerIndex].Models[0].Variants[0].SessionStdioMCP = false
		}
	}
	sealed, err := agentprofile.SealDiscovery(fixture.discovery.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	fixture.discovery.snapshot = sealed
	binding.ProviderDiscoveryRevision = sealed.Revision
	_, err = fixture.service.OpenSession(context.Background(), binding, 1_001)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != CodeProviderPreflight ||
		failure.PreflightCode != domainbridge.PreflightSessionMCPUnavailable {
		t.Fatalf("capability-loss error = %#v, %v", failure, err)
	}
	if fixture.store.writeCount != 0 || len(fixture.store.commands) != 0 {
		t.Fatal("provider capability loss reached durable command mutation")
	}
}

func TestForgedBindingsAndSecretShapedValuesFailBeforeMutation(t *testing.T) {
	for index, mutate := range []func(*domainbridge.SessionBinding){
		func(binding *domainbridge.SessionBinding) { binding.ProjectID = "project-other" },
		func(binding *domainbridge.SessionBinding) { binding.WorkspaceID = "workspace-other" },
		func(binding *domainbridge.SessionBinding) { binding.TaskID = "task-other" },
		func(binding *domainbridge.SessionBinding) { binding.RunID = "run-other" },
		func(binding *domainbridge.SessionBinding) { binding.EffectiveProfilesSHA256 = strings.Repeat("0", 64) },
	} {
		fixture := newServiceFixture(t)
		binding := fixture.binding(t, agentprofile.RoleWorker)
		mutate(&binding)
		if _, err := fixture.service.OpenSession(context.Background(), binding, 1_001); failureCode(t, err) != CodeScopeMismatch {
			t.Errorf("forgery %d error = %v", index, err)
		}
		if fixture.store.writeCount != 0 || len(fixture.store.commands) != 0 {
			t.Fatalf("forgery %d mutated durable state", index)
		}
	}
	fixture := newServiceFixture(t)
	session, _ := fixture.service.OpenSession(context.Background(), fixture.binding(t, agentprofile.RoleOrganizer), 1_001)
	secret := json.RawMessage(`{"kind":"task_update_proposal","title":"token=github_pat_abcdefghijklmnop","objective":"objective","acceptanceCriteria":["criterion"],"priority":"normal","labels":[]}`)
	if _, err := session.Call(context.Background(), "secret-call", "director_planning_command_submit", secret); failureCode(t, err) != CodeSecretRejected {
		t.Fatalf("secret error = %v", err)
	}
	if fixture.store.writeCount != 0 || len(fixture.store.commands) != 0 {
		t.Fatal("secret-shaped input reached durable state")
	}
}
