// SPDX-License-Identifier: Apache-2.0

package planning

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"slices"

	"github.com/mcuadros/director-engine/domain/safedata"
)

type NativePaseoWorkspace struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	ProjectRootPath    string  `json:"projectRootPath"`
	WorkspaceDirectory string  `json:"workspaceDirectory"`
	WorkspaceKind      string  `json:"workspaceKind"`
	RemoteURL          *string `json:"remoteUrl"`
	BaseBranch         *string `json:"baseBranch"`
}

type NativePaseoProject struct {
	ProjectID          string                 `json:"projectId"`
	Name               string                 `json:"name"`
	ProjectRootPath    string                 `json:"projectRootPath"`
	ProjectKind        string                 `json:"projectKind"`
	OrganizerCandidate string                 `json:"organizerCandidate"`
	Workspaces         []NativePaseoWorkspace `json:"workspaces"`
	FactsRevision      string                 `json:"factsRevision"`
}

type NativePaseoProjectsInput struct {
	HostID string `json:"hostId"`
}

type NativePaseoProjectsSnapshot struct {
	SchemaVersion   int                  `json:"schemaVersion"`
	ContractVersion string               `json:"contractVersion"`
	ContractHash    string               `json:"contractHash"`
	HostID          string               `json:"hostId"`
	ObservedAt      string               `json:"observedAt"`
	Projects        []NativePaseoProject `json:"projects"`
}

func nativePaseoProjectValue(value NativePaseoProject) NativePaseoProject {
	value.FactsRevision = ""
	value.Workspaces = slices.Clone(value.Workspaces)
	return value
}

func NativePaseoProjectFactsSHA256(value NativePaseoProject) string {
	encoded, err := json.Marshal(nativePaseoProjectValue(value))
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func validOnboardingPath(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value && len(value) <= 4096 &&
		safedata.ClassifyText(value, false) == safedata.Safe
}

func ValidateNativePaseoProject(value NativePaseoProject) error {
	if !validOpaque(value.ProjectID) || !boundedMutationText(value.Name, 2048) ||
		!validOnboardingPath(value.ProjectRootPath) || !validOnboardingPath(value.OrganizerCandidate) ||
		(value.ProjectKind != "git" && value.ProjectKind != "directory" && value.ProjectKind != "non_git") ||
		len(value.Workspaces) > MaximumWorkspaces || value.FactsRevision != NativePaseoProjectFactsSHA256(value) {
		return ErrQueryInvalid
	}
	seen := map[string]struct{}{}
	for _, workspace := range value.Workspaces {
		if !validOpaque(workspace.ID) || !boundedMutationText(workspace.Name, 2048) ||
			!validOnboardingPath(workspace.ProjectRootPath) || !validOnboardingPath(workspace.WorkspaceDirectory) ||
			(workspace.WorkspaceKind != "worktree" && workspace.WorkspaceKind != "directory" &&
				workspace.WorkspaceKind != "checkout" && workspace.WorkspaceKind != "local_checkout") ||
			(workspace.RemoteURL != nil && safedata.ClassifyText(*workspace.RemoteURL, true) != safedata.Safe) ||
			(workspace.BaseBranch != nil && !boundedMutationText(*workspace.BaseBranch, 512)) {
			return ErrQueryInvalid
		}
		if _, duplicate := seen[workspace.ID]; duplicate {
			return ErrQueryInvalid
		}
		seen[workspace.ID] = struct{}{}
	}
	return nil
}
