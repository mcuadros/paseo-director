// SPDX-License-Identifier: Apache-2.0

package agentruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	applicationbridge "github.com/mcuadros/director-engine/application/agentbridge"
	"github.com/mcuadros/director-engine/domain"
	domainbridge "github.com/mcuadros/director-engine/domain/agentbridge"
	"github.com/mcuadros/director-engine/domain/agentprofile"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	domainexecution "github.com/mcuadros/director-engine/domain/execution"
)

var errRuntimeRecordMissing = errors.New("runtime fixture record missing")

type runtimeDiscovery struct {
	snapshot agentprofile.DiscoverySnapshot
}

func (adapter runtimeDiscovery) Discover(context.Context) (agentprofile.DiscoverySnapshot, error) {
	return adapter.snapshot, nil
}

type runtimeStore struct {
	project   domain.Project
	workspace domain.Workspace
	task      domain.Task
	run       domain.Run
}

func (store runtimeStore) Project(_ context.Context, id string) (domain.Project, error) {
	if id != store.project.ID {
		return domain.Project{}, errRuntimeRecordMissing
	}
	return store.project, nil
}
func (store runtimeStore) Workspace(_ context.Context, id string) (domain.Workspace, error) {
	if id != store.workspace.ID {
		return domain.Workspace{}, errRuntimeRecordMissing
	}
	return store.workspace, nil
}
func (store runtimeStore) Task(_ context.Context, id string) (domain.Task, error) {
	if id != store.task.ID {
		return domain.Task{}, errRuntimeRecordMissing
	}
	return store.task, nil
}
func (store runtimeStore) Run(_ context.Context, id string) (domain.Run, error) {
	if id != store.run.ID {
		return domain.Run{}, errRuntimeRecordMissing
	}
	return store.run, nil
}
func (runtimeStore) Candidate(context.Context, string) (domain.Candidate, error) {
	return domain.Candidate{}, errRuntimeRecordMissing
}
func (runtimeStore) Command(context.Context, string) (domain.Command, error) {
	return domain.Command{}, errRuntimeRecordMissing
}
func (runtimeStore) UpdateRun(context.Context, domain.CommandRequest, domain.Run, domain.Event) (domain.CommandResult, error) {
	return domain.CommandResult{}, errors.New("runtime fixture is read only")
}

func runtimeSelection(role agentprofile.Role) domainconfig.AgentSelection {
	selection := domainconfig.AgentSelection{
		Provider: domainconfig.ProviderCodex, Model: "gpt-5.4-mini", Effort: "high", Mode: "default", PermissionMode: "read-only",
		ProviderOptions: []domainconfig.ProviderOption{{Name: domainconfig.ProviderOptionNetworkAccess, Value: "disabled"}},
	}
	switch role {
	case agentprofile.RoleOrganizer:
		selection.MCPCapabilities = []domainconfig.MCPCapability{domainconfig.MCPProjectRead, domainconfig.MCPPlanningCommandSubmit}
	case agentprofile.RoleWorker:
		selection.PermissionMode = "workspace-write"
		selection.MCPCapabilities = []domainconfig.MCPCapability{domainconfig.MCPTaskRead, domainconfig.MCPTaskOutcomeSubmit}
	case agentprofile.RoleReviewer:
		selection.MCPCapabilities = []domainconfig.MCPCapability{domainconfig.MCPCandidateRead, domainconfig.MCPReviewVerdictSubmit}
	}
	return selection
}

func runtimeSession(t *testing.T) *applicationbridge.Session {
	t.Helper()
	profiles := domainconfig.AgentProfiles{
		Organizer: domainconfig.AgentProfile{AgentSelection: runtimeSelection(agentprofile.RoleOrganizer), FallbackChain: []domainconfig.AgentSelection{}},
		Worker:    domainconfig.AgentProfile{AgentSelection: runtimeSelection(agentprofile.RoleWorker), FallbackChain: []domainconfig.AgentSelection{}},
		Reviewer:  domainconfig.AgentProfile{AgentSelection: runtimeSelection(agentprofile.RoleReviewer), FallbackChain: []domainconfig.AgentSelection{}},
	}
	variants := make([]agentprofile.VariantFact, 0, 3)
	for _, selection := range []domainconfig.AgentSelection{profiles.Organizer.AgentSelection, profiles.Worker.AgentSelection, profiles.Reviewer.AgentSelection} {
		variants = append(variants, agentprofile.VariantFact{
			Effort: selection.Effort, Mode: selection.Mode, PermissionMode: selection.PermissionMode,
			ProviderOptions: selection.ProviderOptions, MCPCapabilities: selection.MCPCapabilities,
			SessionStdioMCP: true, ExactMCPToolPolicy: true, RuntimeProbePassed: true,
		})
	}
	discovery, err := agentprofile.SealDiscovery(agentprofile.DiscoverySnapshot{
		SchemaVersion: agentprofile.DiscoverySchemaVersion, Revision: strings.Repeat("0", 64),
		PaseoVersion: agentprofile.SupportedPaseoVersion, ObservedAtMillis: 1_000, MaximumAgeMillis: 30_000,
		Providers: []agentprofile.ProviderFact{{
			Provider: domainconfig.ProviderCodex, CLIVersion: "0.147.0", State: agentprofile.ProviderReady,
			DiagnosticCodes: []agentprofile.DiagnosticCode{}, Models: []agentprofile.ModelFact{{Model: "gpt-5.4-mini", Variants: variants}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := agentprofile.Freeze(agentprofile.FreezeRequest{
		Profiles: profiles, OrganizerRevision: strings.Repeat("a", 40), ExpectedOrganizerRevision: strings.Repeat("a", 40),
		ConfigurationSHA256: strings.Repeat("b", 64), Discovery: discovery,
		ExpectedDiscoveryRevision: discovery.Revision, NowMillis: 1_001,
	})
	if err != nil {
		t.Fatal(err)
	}
	scope := domainexecution.Scope{ProjectID: "project-runtime", WorkspaceID: "workspace-runtime", TaskID: "task-runtime", RunID: "run-runtime"}
	store := runtimeStore{
		project:   domain.Project{ID: scope.ProjectID, Name: "Runtime Project", State: "active", Version: 1},
		workspace: domain.Workspace{ID: scope.WorkspaceID, ProjectID: scope.ProjectID, Name: "Runtime Workspace", Version: 2},
		task:      domain.Task{ID: scope.TaskID, ProjectID: scope.ProjectID, WorkspaceIDs: []string{scope.WorkspaceID}, Version: 3},
		run: domain.Run{ID: scope.RunID, TaskID: scope.TaskID, Number: 1, BaseSHA: strings.Repeat("c", 40), Version: 4, Execution: domainexecution.State{
			SchemaVersion: domainexecution.SchemaVersion, Scope: scope, EffectiveProfiles: &frozen, EffectiveProfilesSHA256: frozen.SHA256(), CriterionIDs: []string{"criterion-1"},
		}},
	}
	stateHash, _ := applicationbridge.RunStateSHA256(store.run.Execution)
	binding := domainbridge.SessionBinding{
		SchemaVersion: domainbridge.SessionSchemaVersion, Audience: "runtime-session", Role: agentprofile.RoleOrganizer,
		ProjectID: scope.ProjectID, WorkspaceID: scope.WorkspaceID, TaskID: scope.TaskID, RunID: scope.RunID,
		NativeAgentID: "organizer-context", TurnID: "organizer-turn",
		ExpectedProjectVersion: 1, ExpectedWorkspaceVersion: 2, ExpectedTaskVersion: 3, ExpectedRunVersion: 4,
		ExpectedRunStateSHA256: stateHash, EffectiveProfilesSHA256: frozen.SHA256(), OrganizerRevision: frozen.OrganizerRevision(),
		ConfigurationSHA256: frozen.ConfigurationSHA256(), ProviderDiscoveryRevision: discovery.Revision,
	}
	service, _ := applicationbridge.NewService(store, runtimeDiscovery{snapshot: discovery})
	session, err := service.OpenSession(context.Background(), binding, 1_001)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestServeImplementsBoundedClosedStdioMCP(t *testing.T) {
	session := runtimeSession(t)
	requests := []string{
		`{"jsonrpc":"2.0","id":"init","method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fixture"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":"list","method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":"read","method":"tools/call","params":{"name":"director_project_read","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":"escape","method":"tools/call","params":{"name":"raw_taskstore_query","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":"unknown","method":"resources/list","params":{}}`,
		`not-json`,
	}
	var output bytes.Buffer
	if err := Serve(context.Background(), strings.NewReader(strings.Join(requests, "\n")+"\n"), &output, session); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 6 {
		t.Fatalf("response count = %d: %s", len(lines), output.String())
	}
	var listed struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Result.Tools) != 2 || listed.Result.Tools[0].Name != "director_project_read" || listed.Result.Tools[1].Name != "director_planning_command_submit" {
		t.Fatalf("tools/list = %s", lines[1])
	}
	if strings.Contains(output.String(), "TaskStore") || !strings.Contains(lines[2], `"isError":false`) ||
		!strings.Contains(lines[3], string(applicationbridge.CodeToolNotAllowed)) || !strings.Contains(lines[4], `"code":-32601`) ||
		!strings.Contains(lines[5], `"code":-32700`) {
		t.Fatalf("unexpected MCP output: %s", output.String())
	}
}

func TestServeRejectsOversizedAndUnknownFieldsWithoutEcho(t *testing.T) {
	session := runtimeSession(t)
	oversized := strings.Repeat("x", domainbridge.MaximumRequestBytes+1) + "\n"
	var output bytes.Buffer
	if err := Serve(context.Background(), strings.NewReader(oversized), &output, session); err == nil || output.Len() != 0 {
		t.Fatalf("oversized input = error %v output %q", err, output.String())
	}
	unknown := `{"jsonrpc":"2.0","id":"bad","method":"tools/list","params":{},"scope":"forged"}` + "\n"
	if err := Serve(context.Background(), strings.NewReader(unknown), &output, session); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "forged") || !strings.Contains(output.String(), `"code":-32700`) {
		t.Fatalf("unknown-field response = %s", output.String())
	}
}
