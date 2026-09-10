// SPDX-License-Identifier: Apache-2.0

package configuration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mcuadros/director-engine/domain/agentprofile"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
)

type profileDiscoveryStub struct {
	snapshot agentprofile.DiscoverySnapshot
	err      error
	calls    int
}

func (stub *profileDiscoveryStub) Discover(context.Context) (agentprofile.DiscoverySnapshot, error) {
	stub.calls++
	if stub.err != nil {
		return agentprofile.DiscoverySnapshot{}, stub.err
	}
	encoded, err := json.Marshal(stub.snapshot)
	if err != nil {
		return agentprofile.DiscoverySnapshot{}, err
	}
	return agentprofile.ParseDiscovery(encoded)
}

func profileServiceSelection(permission string, capabilities ...domainconfig.MCPCapability) domainconfig.AgentSelection {
	return domainconfig.AgentSelection{
		Provider: domainconfig.ProviderCodex, Model: "gpt-5.4-mini", Effort: "high", Mode: "default",
		PermissionMode: permission, ProviderOptions: []domainconfig.ProviderOption{},
		MCPCapabilities: capabilities,
	}
}

func profileActiveSnapshot(t *testing.T) RunConfigurationSnapshot {
	t.Helper()
	profiles := domainconfig.AgentProfiles{
		Organizer: domainconfig.AgentProfile{AgentSelection: profileServiceSelection("read-only", domainconfig.MCPProjectRead, domainconfig.MCPPlanningCommandSubmit), FallbackChain: []domainconfig.AgentSelection{}},
		Worker:    domainconfig.AgentProfile{AgentSelection: profileServiceSelection("workspace-write", domainconfig.MCPTaskRead, domainconfig.MCPTaskOutcomeSubmit), FallbackChain: []domainconfig.AgentSelection{}},
		Reviewer:  domainconfig.AgentProfile{AgentSelection: profileServiceSelection("read-only", domainconfig.MCPCandidateRead, domainconfig.MCPReviewVerdictSubmit), FallbackChain: []domainconfig.AgentSelection{}},
	}
	configuration := domainconfig.Configuration{
		Schema: domainconfig.SchemaID, SchemaVersion: domainconfig.SchemaVersion,
		Project:       domainconfig.Project{ID: "profiles", Name: "Profiles"},
		Workspaces:    []domainconfig.Workspace{{ID: "product", Remote: "https://github.com/example/product.git", SourcePath: "/srv/product", DefaultBaseBranch: "main"}},
		AgentProfiles: profiles,
		Defaults: domainconfig.Defaults{
			LaunchPolicy: domainconfig.LaunchManual, DeliveryMode: domainconfig.DeliveryPullRequest,
			Limits:            domainconfig.Limits{MaxActiveTasks: 1, MaxActiveTasksPerWorkspace: 1, MaxConcurrentAgents: 3, MaxSubagentsPerTask: 0},
			RunBudget:         domainconfig.RunBudget{ElapsedSeconds: 60, Tokens: 1000, Turns: 2, CICycles: 1},
			AutoFixCIFailures: false, AutoFixReviewFeedback: false,
		},
		WorkspaceOverrides: []domainconfig.WorkspaceOverride{},
		Skills:             []domainconfig.FileReference{{ID: "commits", Path: "skills/commits/SKILL.md"}},
		Templates:          []domainconfig.FileReference{{ID: "task", Path: "templates/task.md"}},
	}
	encoded, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	document, err := domainconfig.Parse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := NewSecurityEnvelope(document, HumanConfirmation{
		ActorKind: ActorHuman, ActorID: "human:owner", Revision: document.SHA256(), Confirmed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := NewState(envelope)
	if err != nil {
		t.Fatal(err)
	}
	revision := strings.Repeat("a", 40)
	state, preview, err := state.Preview(PreviewCommand{ExpectedVersion: 0, OrganizerRevision: revision, ConfigurationJSON: encoded})
	if err != nil || !preview.Valid {
		t.Fatalf("Preview() = %#v, %v", preview, err)
	}
	state, err = state.Apply(ApplyCommand{
		ExpectedVersion: preview.AggregateVersion, PreviewID: preview.ID,
		Confirmation:    HumanConfirmation{ActorKind: ActorHuman, ActorID: "human:owner", Revision: revision, Confirmed: true},
		Acknowledgement: HumanConfirmation{ActorKind: ActorHuman, ActorID: "human:owner", Revision: "", Confirmed: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := state.FreezeRunConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func profileServiceDiscovery() agentprofile.DiscoverySnapshot {
	readCapabilities := []domainconfig.MCPCapability{
		domainconfig.MCPProjectRead, domainconfig.MCPPlanningCommandSubmit,
		domainconfig.MCPCandidateRead, domainconfig.MCPReviewVerdictSubmit,
	}
	writeCapabilities := []domainconfig.MCPCapability{domainconfig.MCPTaskRead, domainconfig.MCPTaskOutcomeSubmit}
	snapshot := agentprofile.DiscoverySnapshot{
		SchemaVersion: agentprofile.DiscoverySchemaVersion, Revision: strings.Repeat("0", 64), PaseoVersion: agentprofile.SupportedPaseoVersion,
		ObservedAtMillis: 1_000, MaximumAgeMillis: 30_000,
		Providers: []agentprofile.ProviderFact{{
			Provider: domainconfig.ProviderCodex, CLIVersion: "0.147.0", State: agentprofile.ProviderReady,
			DiagnosticCodes: []agentprofile.DiagnosticCode{}, Models: []agentprofile.ModelFact{{Model: "gpt-5.4-mini", Variants: []agentprofile.VariantFact{
				{Effort: "high", Mode: "default", PermissionMode: "read-only", ProviderOptions: []domainconfig.ProviderOption{}, MCPCapabilities: readCapabilities, SessionStdioMCP: true, ExactMCPToolPolicy: true, RuntimeProbePassed: true},
				{Effort: "high", Mode: "default", PermissionMode: "workspace-write", ProviderOptions: []domainconfig.ProviderOption{}, MCPCapabilities: writeCapabilities, SessionStdioMCP: true, ExactMCPToolPolicy: true, RuntimeProbePassed: true},
			}}},
		}},
	}
	sealed, err := agentprofile.SealDiscovery(snapshot)
	if err != nil {
		panic(err)
	}
	return sealed
}

func TestProfileServiceBindsDiscoveryToActiveOrganizerSnapshot(t *testing.T) {
	snapshot := profileActiveSnapshot(t)
	stub := &profileDiscoveryStub{snapshot: profileServiceDiscovery()}
	service, err := NewProfileService(stub)
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.FreezeProfiles(context.Background(), snapshot, snapshot.OrganizerRevision(), stub.snapshot.Revision, 1_001)
	if err != nil {
		t.Fatal(err)
	}
	restarted, _ := NewProfileService(stub)
	second, err := restarted.FreezeProfiles(context.Background(), snapshot, snapshot.OrganizerRevision(), stub.snapshot.Revision, 1_001)
	if err != nil || first.SHA256() != second.SHA256() || first.ConfigurationSHA256() != snapshot.ConfigurationSHA256() || stub.calls != 2 {
		t.Fatalf("restart freeze = %s %s %s calls=%d error=%v", first.SHA256(), second.SHA256(), first.ConfigurationSHA256(), stub.calls, err)
	}
}

func TestProfileServiceFailsClosedOnPortAndRevisionErrors(t *testing.T) {
	if _, err := NewProfileService(nil); err == nil {
		t.Fatal("NewProfileService accepted a nil discovery port")
	}
	sentinel := errors.New("discovery unavailable")
	service, _ := NewProfileService(&profileDiscoveryStub{err: sentinel})
	if _, err := service.FreezeProfiles(context.Background(), profileActiveSnapshot(t), strings.Repeat("a", 40), strings.Repeat("d", 64), 1_001); !errors.Is(err, sentinel) {
		t.Fatalf("port error = %v", err)
	}
	snapshot := profileActiveSnapshot(t)
	service, _ = NewProfileService(&profileDiscoveryStub{snapshot: profileServiceDiscovery()})
	if _, err := service.FreezeProfiles(context.Background(), snapshot, strings.Repeat("e", 40), profileServiceDiscovery().Revision, 1_001); err == nil {
		t.Fatal("FreezeProfiles accepted a stale Organizer revision")
	}
}
