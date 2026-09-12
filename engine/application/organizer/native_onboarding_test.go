// SPDX-License-Identifier: Apache-2.0

package organizer

import (
	"encoding/json"
	"testing"

	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
)

func TestNativeConfigurationKeepsVisibleExecutionViewsOutOfCanonicalWorkspaceSet(t *testing.T) {
	facts := NativeProjectFact{ID: "native-project", Name: "Native project", ProjectRootPath: "/srv/native-project",
		OrganizerCandidate: "/srv/.native-project-director-organizer",
		Workspaces: []NativeWorkspaceFact{
			{ID: "root-workspace", Name: "Native project", ProjectRootPath: "/srv/native-project",
				WorkspaceDirectory: "/srv/native-project", RemoteURL: "https://github.com/example/native-project.git", BaseBranch: "main"},
			{ID: "execution-workspace", Name: "Task execution", ProjectRootPath: "/srv/native-project",
				WorkspaceDirectory: "/srv/.worktrees/task-1", RemoteURL: "https://github.com/example/native-project.git", BaseBranch: "task/task-1"},
		},
	}
	content, _, err := generatedNativeConfiguration(facts)
	if err != nil {
		t.Fatal(err)
	}
	var configuration domainconfig.Configuration
	if json.Unmarshal(content, &configuration) != nil || len(configuration.Workspaces) != 1 ||
		configuration.Workspaces[0].NativePaseoWorkspaceID != "root-workspace" ||
		configuration.Workspaces[0].DefaultBaseBranch != "main" ||
		configuration.Defaults.DeliveryMode != domainconfig.DeliveryDirect ||
		configuration.AgentProfiles.Worker.Model != "gpt-5.6-sol" ||
		configuration.AgentProfiles.Reviewer.Model != "gpt-5.6-sol" ||
		configuration.AgentProfiles.Worker.Mode != "auto-review" {
		t.Fatalf("generated native configuration = %#v", configuration)
	}
	if len(facts.Workspaces) != 2 {
		t.Fatal("authoritative visible Workspace facts were mutated")
	}
}

func TestNativeConfigurationRefusesAmbiguousCanonicalWorkspaceFacts(t *testing.T) {
	facts := NativeProjectFact{ID: "native-project", Name: "Native project", ProjectRootPath: "/srv/native-project",
		OrganizerCandidate: "/srv/.native-project-director-organizer",
		Workspaces: []NativeWorkspaceFact{
			{ID: "root-a", Name: "A", ProjectRootPath: "/srv/native-project", WorkspaceDirectory: "/srv/native-project",
				RemoteURL: "https://github.com/example/a.git", BaseBranch: "main"},
			{ID: "root-b", Name: "B", ProjectRootPath: "/srv/native-project", WorkspaceDirectory: "/srv/native-project",
				RemoteURL: "https://github.com/example/b.git", BaseBranch: "main"},
		},
	}
	if _, _, err := generatedNativeConfiguration(facts); err == nil {
		t.Fatal("ambiguous canonical native Workspace facts were selected")
	}
}
