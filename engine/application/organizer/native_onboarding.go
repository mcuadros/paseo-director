// SPDX-License-Identifier: Apache-2.0

package organizer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
)

type NativeWorkspaceFact struct {
	ID                 string
	Name               string
	ProjectRootPath    string
	WorkspaceDirectory string
	RemoteURL          string
	BaseBranch         string
}

type NativeProjectFact struct {
	ID                 string
	Name               string
	ProjectRootPath    string
	OrganizerCandidate string
	Workspaces         []NativeWorkspaceFact
}

func derivedID(prefix string, values ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(values, "\x1f")))
	return prefix + "-" + hex.EncodeToString(digest[:16])
}

// NativeProjectID is stable across display-name and path changes because it
// binds only the authoritative native Paseo Project identity.
func NativeProjectID(nativeProjectID string) string {
	return derivedID("paseo-project", nativeProjectID)
}

// OrganizerCandidate derives one sibling path rather than nesting an
// Organizer inside the native Project repository.
func OrganizerCandidate(projectRoot string) string {
	clean := filepath.Clean(projectRoot)
	return filepath.Join(filepath.Dir(clean), "."+filepath.Base(clean)+"-director-organizer")
}

func generatedNativeConfiguration(facts NativeProjectFact) ([]byte, string, error) {
	if facts.ID == "" || facts.Name == "" || !filepath.IsAbs(facts.ProjectRootPath) ||
		facts.OrganizerCandidate != OrganizerCandidate(facts.ProjectRootPath) || len(facts.Workspaces) == 0 {
		return nil, "", errors.New("native Paseo Project facts are incomplete")
	}
	projectID := NativeProjectID(facts.ID)
	selected := make(map[string]NativeWorkspaceFact, len(facts.Workspaces))
	order := make([]string, 0, len(facts.Workspaces))
	for _, workspace := range facts.Workspaces {
		// All visible native Workspaces stay in the Preview facts, including
		// temporary execution views. Only a Workspace with current Git remote
		// and base facts can become one canonical Director product Workspace.
		if workspace.RemoteURL == "" || workspace.BaseBranch == "" || workspace.WorkspaceDirectory != workspace.ProjectRootPath {
			continue
		}
		key := workspace.ProjectRootPath
		current, duplicate := selected[key]
		if !duplicate {
			order = append(order, key)
		}
		if duplicate && (current.RemoteURL != workspace.RemoteURL || current.BaseBranch != workspace.BaseBranch) {
			return nil, "", errors.New("native Paseo Project has ambiguous canonical Git Workspaces")
		}
		selected[key] = workspace
	}
	if len(selected) == 0 {
		return nil, "", errors.New("native Paseo Project has no canonical Git Workspace")
	}
	workspaces := make([]domainconfig.Workspace, 0, len(selected))
	for _, key := range order {
		workspace := selected[key]
		workspaces = append(workspaces, domainconfig.Workspace{ID: derivedID("workspace", facts.ID, workspace.ID),
			Name: workspace.Name, NativePaseoWorkspaceID: workspace.ID, Remote: workspace.RemoteURL, SourcePath: workspace.ProjectRootPath,
			DefaultBaseBranch: workspace.BaseBranch})
	}
	selection := func(permission string, capabilities ...domainconfig.MCPCapability) domainconfig.AgentSelection {
		return domainconfig.AgentSelection{Provider: domainconfig.ProviderCodex, Model: "gpt-5.6-sol", Effort: "high",
			Mode: "auto-review", PermissionMode: permission, ProviderOptions: []domainconfig.ProviderOption{},
			MCPCapabilities: capabilities}
	}
	configuration := domainconfig.Configuration{Schema: domainconfig.SchemaID, SchemaVersion: domainconfig.SchemaVersion,
		Project: domainconfig.Project{ID: projectID, Name: facts.Name}, Workspaces: workspaces,
		AgentProfiles: domainconfig.AgentProfiles{
			Organizer: domainconfig.AgentProfile{AgentSelection: selection("read-only", domainconfig.MCPProjectRead, domainconfig.MCPPlanningCommandSubmit), FallbackChain: []domainconfig.AgentSelection{}},
			Worker:    domainconfig.AgentProfile{AgentSelection: selection("workspace-write", domainconfig.MCPTaskRead, domainconfig.MCPTaskOutcomeSubmit), FallbackChain: []domainconfig.AgentSelection{}},
			Reviewer:  domainconfig.AgentProfile{AgentSelection: selection("read-only", domainconfig.MCPCandidateRead, domainconfig.MCPReviewVerdictSubmit), FallbackChain: []domainconfig.AgentSelection{}},
		},
		Defaults: domainconfig.Defaults{LaunchPolicy: domainconfig.LaunchManual, DeliveryMode: domainconfig.DeliveryDirect,
			IntegrationMode:   domainconfig.IntegrationManual,
			Limits:            domainconfig.Limits{MaxActiveTasks: 6, MaxActiveTasksPerWorkspace: 2, MaxConcurrentAgents: 8, MaxSubagentsPerTask: 3},
			RunBudget:         domainconfig.RunBudget{ElapsedSeconds: 7200, Tokens: 200000, Turns: 32, CICycles: 4},
			AutoFixCIFailures: true, AutoFixReviewFeedback: true},
		WorkspaceOverrides: []domainconfig.WorkspaceOverride{},
		Skills:             []domainconfig.FileReference{{ID: "commits", Path: "skills/commits/SKILL.md"}},
		Templates:          []domainconfig.FileReference{{ID: "task", Path: "templates/task.md"}}}
	encoded, err := json.Marshal(configuration)
	if err != nil {
		return nil, "", err
	}
	return encoded, projectID, nil
}

// NativeCreateRequest converts an authoritative connector observation into
// the ordinary Organizer Preview/Apply request. The existing service still
// performs schema, repository, Workspace, Git, and human confirmation checks.
func NativeCreateRequest(requestID string, facts NativeProjectFact) (CreateRequest, error) {
	configuration, projectID, err := generatedNativeConfiguration(facts)
	if err != nil {
		return CreateRequest{}, err
	}
	return CreateRequest{RequestID: requestID, ProjectID: projectID, ProjectName: facts.Name,
		RepositoryPath: OrganizerCandidate(facts.ProjectRootPath), ConfigurationJSON: configuration}, nil
}
