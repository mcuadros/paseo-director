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

// RepairExecutor is an idempotent deterministic adapter used to prove the
// engine's Preview/Apply authority boundary without touching a live host.
type RepairExecutor struct {
	mu       sync.Mutex
	Failure  error
	Requests []homeport.RepairExecution
	results  map[string]homeport.RepairEvidence
	inputs   map[string]homeport.RepairExecution
}

func (executor *RepairExecutor) ObserveRepair(_ context.Context, requestID string) (homeport.RepairExecution, homeport.RepairEvidence, bool, error) {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.Failure != nil {
		return homeport.RepairExecution{}, homeport.RepairEvidence{}, false, executor.Failure
	}
	result, ok := executor.results[requestID]
	if !ok {
		return homeport.RepairExecution{}, homeport.RepairEvidence{}, false, nil
	}
	return executor.inputs[requestID], result, true, nil
}

func (executor *RepairExecutor) ApplyRepair(_ context.Context, input homeport.RepairExecution) (homeport.RepairEvidence, error) {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.Failure != nil {
		return homeport.RepairEvidence{}, executor.Failure
	}
	if executor.results == nil {
		executor.results = map[string]homeport.RepairEvidence{}
		executor.inputs = map[string]homeport.RepairExecution{}
	}
	if existing, ok := executor.results[input.RequestID]; ok {
		return existing, nil
	}
	executor.Requests = append(executor.Requests, input)
	result := homeport.RepairEvidence{ProjectVersion: input.ProjectVersion + 1, Cursor: input.Cursor + 1, OperationIDs: append([]string{}, input.OperationIDs...)}
	executor.results[input.RequestID] = result
	executor.inputs[input.RequestID] = input
	return result, nil
}

func cloneHomeObservation(value homeport.HostObservation) homeport.HostObservation {
	result := value
	result.Projects = make(map[string]homeport.ProjectObservation, len(value.Projects))
	for projectID, project := range value.Projects {
		copyProject := project
		copyProject.Preflight = append([]homeport.PreflightObservation{}, project.Preflight...)
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
