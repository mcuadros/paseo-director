// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"sync"

	homeport "github.com/mcuadros/director-engine/ports/home"
)

// HomeSource is a deterministic operational-fact adapter for Home projection,
// restart, staleness, pagination, and same-key cross-host tests.
type HomeSource struct {
	mu          sync.Mutex
	Observation homeport.HostObservation
	Failure     error
	Requests    []string
}

func cloneHomeObservation(value homeport.HostObservation) homeport.HostObservation {
	result := value
	result.Projects = make(map[string]homeport.ProjectObservation, len(value.Projects))
	for projectID, project := range value.Projects {
		copyProject := project
		copyProject.Workspaces = make(map[string]homeport.WorkspaceObservation, len(project.Workspaces))
		for workspaceID, workspace := range project.Workspaces {
			copyProject.Workspaces[workspaceID] = workspace
		}
		result.Projects[projectID] = copyProject
	}
	return result
}

func (source *HomeSource) Observe(_ context.Context, hostID string, _ []string) (homeport.HostObservation, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.Requests = append(source.Requests, hostID)
	if source.Failure != nil {
		return homeport.HostObservation{}, source.Failure
	}
	return cloneHomeObservation(source.Observation), nil
}

func (source *HomeSource) Replace(value homeport.HostObservation) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.Observation = cloneHomeObservation(value)
}
