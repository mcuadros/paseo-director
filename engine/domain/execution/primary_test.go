// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/mcuadros/director-engine/domain/agentprofile"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
)

func primaryProfiles(t *testing.T) agentprofile.FrozenSet {
	t.Helper()
	selection := domainconfig.AgentSelection{
		Provider: domainconfig.ProviderCodex, Model: "gpt-5.4-mini", Effort: "high", Mode: "default",
		PermissionMode: "workspace-write", ProviderOptions: []domainconfig.ProviderOption{},
		MCPCapabilities: []domainconfig.MCPCapability{domainconfig.MCPTaskRead, domainconfig.MCPTaskOutcomeSubmit},
	}
	readOnly := selection
	readOnly.PermissionMode = "read-only"
	readOnly.MCPCapabilities = []domainconfig.MCPCapability{domainconfig.MCPProjectRead, domainconfig.MCPPlanningCommandSubmit}
	reviewer := readOnly
	reviewer.MCPCapabilities = []domainconfig.MCPCapability{domainconfig.MCPCandidateRead, domainconfig.MCPReviewVerdictSubmit}
	profiles := domainconfig.AgentProfiles{
		Organizer: domainconfig.AgentProfile{AgentSelection: readOnly, FallbackChain: []domainconfig.AgentSelection{}},
		Worker:    domainconfig.AgentProfile{AgentSelection: selection, FallbackChain: []domainconfig.AgentSelection{}},
		Reviewer:  domainconfig.AgentProfile{AgentSelection: reviewer, FallbackChain: []domainconfig.AgentSelection{}},
	}
	discovery := agentprofile.DiscoverySnapshot{
		SchemaVersion: agentprofile.DiscoverySchemaVersion, PaseoVersion: agentprofile.SupportedPaseoVersion,
		ObservedAtMillis: 1_000, MaximumAgeMillis: 30_000, Revision: strings.Repeat("0", 64),
		Providers: []agentprofile.ProviderFact{{
			Provider: domainconfig.ProviderCodex, CLIVersion: "0.147.0", State: agentprofile.ProviderReady,
			DiagnosticCodes: []agentprofile.DiagnosticCode{},
			Models: []agentprofile.ModelFact{{Model: selection.Model, Variants: []agentprofile.VariantFact{
				{Effort: readOnly.Effort, Mode: readOnly.Mode, PermissionMode: readOnly.PermissionMode, ProviderOptions: []domainconfig.ProviderOption{}, MCPCapabilities: readOnly.MCPCapabilities, SessionStdioMCP: true, ExactMCPToolPolicy: true, RuntimeProbePassed: true},
				{Effort: selection.Effort, Mode: selection.Mode, PermissionMode: selection.PermissionMode, ProviderOptions: []domainconfig.ProviderOption{}, MCPCapabilities: selection.MCPCapabilities, SessionStdioMCP: true, ExactMCPToolPolicy: true, RuntimeProbePassed: true},
				{Effort: reviewer.Effort, Mode: reviewer.Mode, PermissionMode: reviewer.PermissionMode, ProviderOptions: []domainconfig.ProviderOption{}, MCPCapabilities: reviewer.MCPCapabilities, SessionStdioMCP: true, ExactMCPToolPolicy: true, RuntimeProbePassed: true},
			}}},
		}},
	}
	sealed, err := agentprofile.SealDiscovery(discovery)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := agentprofile.Freeze(agentprofile.FreezeRequest{
		Profiles: profiles, OrganizerRevision: strings.Repeat("b", 40), ExpectedOrganizerRevision: strings.Repeat("b", 40),
		ConfigurationSHA256: strings.Repeat("a", 64), Discovery: sealed,
		ExpectedDiscoveryRevision: sealed.Revision, NowMillis: 1_001,
	})
	if err != nil {
		t.Fatal(err)
	}
	return frozen
}

func primaryServer(runID string) MCPServerLaunch {
	return MCPServerLaunch{
		Name: "director-session-mcp", Command: "/usr/bin/director-agent-runtime",
		Args: []string{"serve", "--run", runID}, Env: map[string]string{},
	}
}

func TestPrimarySessionFreezesProfileMCPAndNativeIdentity(t *testing.T) {
	scope := Scope{ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "task-1", RunID: "run-1"}
	session, err := NewPrimarySession(scope, "effect-agent-1", primaryProfiles(t), primaryServer(scope.RunID))
	if err != nil || !ValidPrimarySession(session) {
		t.Fatalf("primary session = %#v, %v", session, err)
	}
	if session.Role != agentprofile.RoleWorker || session.PermissionMode != "workspace-write" ||
		!slices.Equal(session.MCPTools, []string{"director_task_outcome_submit", "director_task_read"}) ||
		session.NativeAgentID != "" || session.BindingSHA256 != "" {
		t.Fatalf("frozen session = %#v", session)
	}
	bound, err := BindPrimarySession(session, "agent-native-1")
	if err != nil || !ValidPrimarySession(bound) || bound.NativeAgentID != "agent-native-1" || bound.BindingSHA256 == "" {
		t.Fatalf("bound session = %#v, %v", bound, err)
	}
	if _, err := BindPrimarySession(bound, "agent-native-2"); err == nil {
		t.Fatal("primary session native identity was rebound")
	}
}

func TestPrimarySessionMutationMatrixFailsClosed(t *testing.T) {
	scope := Scope{ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "task-1", RunID: "run-1"}
	profiles := primaryProfiles(t)
	session, err := NewPrimarySession(scope, "effect-agent-1", profiles, primaryServer(scope.RunID))
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*PrimarySession){
		"scope":            func(value *PrimarySession) { value.Scope.RunID = "run-other" },
		"role":             func(value *PrimarySession) { value.Role = agentprofile.RoleReviewer },
		"intent":           func(value *PrimarySession) { value.AgentIntentID = "" },
		"profile":          func(value *PrimarySession) { value.EffectiveProfilesSHA256 = strings.Repeat("0", 64) },
		"organizer":        func(value *PrimarySession) { value.OrganizerRevision = strings.Repeat("0", 40) },
		"configuration":    func(value *PrimarySession) { value.ConfigurationSHA256 = strings.Repeat("0", 64) },
		"discovery":        func(value *PrimarySession) { value.ProviderDiscoveryRevision = strings.Repeat("0", 64) },
		"provider":         func(value *PrimarySession) { value.Provider = domainconfig.ProviderOpenCode },
		"model":            func(value *PrimarySession) { value.Model = "other" },
		"permission":       func(value *PrimarySession) { value.PermissionMode = "read-only" },
		"mcp contract":     func(value *PrimarySession) { value.MCPContractVersion = "other" },
		"mcp hash":         func(value *PrimarySession) { value.MCPContractSHA256 = strings.Repeat("0", 64) },
		"tools":            func(value *PrimarySession) { value.MCPTools = []string{"other"} },
		"server":           func(value *PrimarySession) { value.MCPServer.Command = "/usr/bin/other" },
		"reservation":      func(value *PrimarySession) { value.ReservationSHA256 = strings.Repeat("0", 64) },
		"partial identity": func(value *PrimarySession) { value.NativeAgentID = "agent-1" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := session
			changed.MCPTools = slices.Clone(session.MCPTools)
			changed.MCPServer.Args = slices.Clone(session.MCPServer.Args)
			mutate(&changed)
			if ValidPrimarySession(changed) {
				t.Fatal("mutated primary session was admitted")
			}
		})
	}
	for name, mutate := range map[string]func(*PrimarySession){
		"provider":   func(value *PrimarySession) { value.Provider = domainconfig.ProviderOpenCode },
		"model":      func(value *PrimarySession) { value.Model = "other" },
		"permission": func(value *PrimarySession) { value.PermissionMode = "read-only" },
		"tools": func(value *PrimarySession) {
			value.MCPTools = []string{"director_task_read", "director_task_outcome_submit", "director_project_read"}
		},
	} {
		t.Run("self-consistent "+name, func(t *testing.T) {
			changed := session
			changed.MCPTools = slices.Clone(session.MCPTools)
			mutate(&changed)
			changed = normalizedSession(changed)
			changed.ReservationSHA256 = primaryDigest(reservationValue(changed))
			if ValidPrimarySession(changed) && PrimarySessionMatchesProfiles(changed, profiles) {
				t.Fatal("self-consistent profile or MCP mutation escaped frozen-profile re-derivation")
			}
		})
	}
}

func TestPrimarySessionRejectsCredentialAndControlEnvironment(t *testing.T) {
	profiles := primaryProfiles(t)
	scope := Scope{ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "task-1", RunID: "run-1"}
	for _, name := range []string{"AUTH_TOKEN", "PASSWORD", "GITHUB_TOKEN", "PASEO_SOCKET", "DIRECTOR_CREDENTIAL"} {
		t.Run(name, func(t *testing.T) {
			server := primaryServer(scope.RunID)
			server.Env[name] = "must-not-persist"
			if _, err := NewPrimarySession(scope, "effect-agent-1", profiles, server); err == nil {
				t.Fatal("credential-shaped environment reached the durable primary session")
			}
		})
	}
	server := primaryServer(scope.RunID)
	server.Args = append(server.Args, "--token=ghp_0123456789abcdefghijklmnop")
	if _, err := NewPrimarySession(scope, "effect-agent-1", profiles, server); err == nil {
		t.Fatal("credential-shaped argv reached the durable primary session")
	}
}

func TestRepositoryBindingPropertyMatrixBindsEveryRunAndTarget(t *testing.T) {
	for index := 0; index < 256; index++ {
		binding := RepositoryBinding{
			RepositoryID: fmt.Sprintf("repository-%064x", index+1), RepositoryKey: fmt.Sprintf("example.invalid/repo-%d", index),
			CanonicalRemote: fmt.Sprintf("https://example.invalid/repo-%d", index),
			SourcePath:      fmt.Sprintf("/srv/source/repo-%d", index), SourceDevice: 1, SourceInode: uint64(index + 1),
			GitCommonDirectory: fmt.Sprintf("/srv/source/repo-%d/.git", index), GitCommonDevice: 1, GitCommonInode: uint64(index + 257),
			WorktreePath: fmt.Sprintf("/srv/runs/run-%d", index), Branch: fmt.Sprintf("task/task-%d", index),
			BaseSHA: fmt.Sprintf("%040x", index+1),
		}
		first := RepositoryBindingSHA256(binding)
		if first == "" || first != RepositoryBindingSHA256(binding) {
			t.Fatalf("binding %d is not deterministic", index)
		}
		changed := binding
		changed.WorktreePath += "-other"
		if RepositoryBindingSHA256(changed) == first {
			t.Fatalf("binding %d ignored a cross-Run worktree change", index)
		}
	}
}

func TestRepositoryBindingRejectsGitInvalidRefsBeforeAnAdapterCall(t *testing.T) {
	binding := RepositoryBinding{
		RepositoryID: "repository-" + strings.Repeat("1", 64), RepositoryKey: "example.invalid/repo",
		CanonicalRemote: "https://example.invalid/repo", SourcePath: "/srv/source/repo",
		SourceDevice: 1, SourceInode: 2, GitCommonDirectory: "/srv/source/repo/.git",
		GitCommonDevice: 1, GitCommonInode: 3, WorktreePath: "/srv/runs/run-1",
		Branch: "task/task-1", BaseSHA: strings.Repeat("1", 40),
	}
	for _, branch := range []string{"-option", "task//one", "task/../one", "task/one.lock", "task/one:", "@"} {
		changed := binding
		changed.Branch = branch
		if RepositoryBindingSHA256(changed) != "" {
			t.Fatalf("invalid Git branch %q was admitted", branch)
		}
	}
}
