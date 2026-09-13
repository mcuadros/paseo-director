// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	provideradapter "github.com/mcuadros/director-engine/adapters/provider"
	agentruntime "github.com/mcuadros/director-engine/agent-runtime"
	bridgeapp "github.com/mcuadros/director-engine/application/agentbridge"
	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/agentbridge"
	"github.com/mcuadros/director-engine/domain/agentprofile"
)

const agentMCPPath = "/v1/agent-mcp"

type agentMCPStore interface {
	bridgeapp.Store
	Run(context.Context, string) (domain.Run, error)
	Task(context.Context, string) (domain.Task, error)
	Project(context.Context, string) (domain.Project, error)
	Workspace(context.Context, string) (domain.Workspace, error)
}

type agentMCPHandler struct {
	store agentMCPStore
}

func retryableMCPBindingError(err error) bool {
	var failure *bridgeapp.Failure
	return errors.As(err, &failure) && failure.Code == bridgeapp.CodeExpectedState
}

func newAgentMCPHandler(store agentMCPStore) http.Handler { return &agentMCPHandler{store: store} }

type storedSessionBinding struct {
	Role                      agentprofile.Role `json:"role"`
	ProjectID                 string            `json:"projectId"`
	WorkspaceID               string            `json:"workspaceId"`
	TaskID                    string            `json:"taskId"`
	RunID                     string            `json:"runId"`
	HelperID                  string            `json:"helperId"`
	CandidateID               string            `json:"candidateId"`
	CandidateSHA              string            `json:"candidateSha"`
	NativeAgentID             string            `json:"nativeAgentId"`
	TurnID                    string            `json:"turnId"`
	ExpectedProjectVersion    uint64            `json:"expectedProjectVersion"`
	ExpectedWorkspaceVersion  uint64            `json:"expectedWorkspaceVersion"`
	ExpectedTaskVersion       uint64            `json:"expectedTaskVersion"`
	ExpectedRunVersion        uint64            `json:"expectedRunVersion"`
	ExpectedRunStateSHA256    string            `json:"expectedRunStateSha256"`
	EffectiveProfilesSHA256   string            `json:"effectiveProfilesSha256"`
	OrganizerRevision         string            `json:"organizerRevision"`
	ConfigurationSHA256       string            `json:"configurationSha256"`
	ProviderDiscoveryRevision string            `json:"providerDiscoveryRevision"`
}

func currentMCPBinding(store agentMCPStore, ctx context.Context, run domain.Run, role agentprofile.Role, audience string) (agentbridge.SessionBinding, error) {
	task, err := store.Task(ctx, run.TaskID)
	if err != nil {
		return agentbridge.SessionBinding{}, err
	}
	project, err := store.Project(ctx, task.ProjectID)
	if err != nil {
		return agentbridge.SessionBinding{}, err
	}
	workspace, err := store.Workspace(ctx, run.Execution.Scope.WorkspaceID)
	if err != nil {
		return agentbridge.SessionBinding{}, err
	}
	stateHash, err := bridgeapp.RunStateSHA256(run.Execution)
	if err != nil {
		return agentbridge.SessionBinding{}, err
	}
	nativeAgentID, candidateID, candidateSHA := run.Execution.Agent.ExternalID, "", ""
	if nativeAgentID == "" {
		nativeAgentID = "pending-" + run.Execution.Agent.ID
	}
	if role == agentprofile.RoleReviewer {
		if run.Execution.Review == nil {
			return agentbridge.SessionBinding{}, errors.New("Reviewer session is not admitted")
		}
		candidateID, candidateSHA = run.CurrentCandidateID, run.Execution.Review.Binding.CandidateSHA
		nativeAgentID = run.Execution.Review.ReviewerUUID
		if nativeAgentID == "" {
			nativeAgentID = run.Execution.Review.Binding.ReviewOwnerUUID
		}
	}
	return agentbridge.SessionBinding{SchemaVersion: agentbridge.SessionSchemaVersion, Audience: audience, Role: role,
		ProjectID: project.ID, WorkspaceID: workspace.ID, TaskID: task.ID, RunID: run.ID,
		CandidateID: candidateID, CandidateSHA: candidateSHA, NativeAgentID: nativeAgentID,
		TurnID: "turn-" + run.ID, ExpectedProjectVersion: project.Version, ExpectedWorkspaceVersion: workspace.Version,
		ExpectedTaskVersion: task.Version, ExpectedRunVersion: run.Version, ExpectedRunStateSHA256: stateHash,
		EffectiveProfilesSHA256: run.Execution.EffectiveProfilesSHA256, OrganizerRevision: run.Execution.PrimarySession.OrganizerRevision,
		ConfigurationSHA256:       run.Execution.PrimarySession.ConfigurationSHA256,
		ProviderDiscoveryRevision: run.Execution.PrimarySession.ProviderDiscoveryRevision}, nil
}

func replayMCPBinding(store agentMCPStore, ctx context.Context, run domain.Run, role agentprofile.Role, audience, requestID string) (agentbridge.SessionBinding, bool) {
	for _, receipt := range run.Execution.MCPCommandReceipts {
		if receipt.RequestID != requestID || receipt.Role != string(role) {
			continue
		}
		command, err := store.Command(ctx, receipt.CommandKey)
		if err != nil {
			return agentbridge.SessionBinding{}, false
		}
		var stored storedSessionBinding
		if json.Unmarshal(command.Payload, &stored) != nil {
			return agentbridge.SessionBinding{}, false
		}
		return agentbridge.SessionBinding{SchemaVersion: agentbridge.SessionSchemaVersion, Audience: audience, Role: stored.Role,
			ProjectID: stored.ProjectID, WorkspaceID: stored.WorkspaceID, TaskID: stored.TaskID, RunID: stored.RunID,
			HelperID: stored.HelperID, CandidateID: stored.CandidateID, CandidateSHA: stored.CandidateSHA,
			NativeAgentID: stored.NativeAgentID, TurnID: stored.TurnID, ExpectedProjectVersion: stored.ExpectedProjectVersion,
			ExpectedWorkspaceVersion: stored.ExpectedWorkspaceVersion, ExpectedTaskVersion: stored.ExpectedTaskVersion,
			ExpectedRunVersion: stored.ExpectedRunVersion, ExpectedRunStateSHA256: stored.ExpectedRunStateSHA256,
			EffectiveProfilesSHA256: stored.EffectiveProfilesSHA256, OrganizerRevision: stored.OrganizerRevision,
			ConfigurationSHA256: stored.ConfigurationSHA256, ProviderDiscoveryRevision: stored.ProviderDiscoveryRevision}, true
	}
	return agentbridge.SessionBinding{}, false
}

func (handler *agentMCPHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != agentMCPPath || request.URL.RawQuery != "" || request.Method != http.MethodPost ||
		request.Header.Get("Content-Type") != "application/json" {
		http.NotFound(response, request)
		return
	}
	runID, roleText, audienceHash := request.Header.Get("X-Director-Run"), request.Header.Get("X-Director-Role"), request.Header.Get("X-Director-Session")
	role := agentprofile.Role(roleText)
	if runID == "" || len(audienceHash) != 64 || (role != agentprofile.RoleWorker && role != agentprofile.RoleReviewer) {
		writePlanningError(response, http.StatusUnauthorized, "MCP_SESSION_INVALID")
		return
	}
	content, err := io.ReadAll(io.LimitReader(request.Body, agentbridge.MaximumRequestBytes+1))
	if err != nil || len(content) == 0 || len(content) > agentbridge.MaximumRequestBytes {
		writePlanningError(response, http.StatusBadRequest, "MCP_INPUT_INVALID")
		return
	}
	audience := "session-" + audienceHash
	service, err := bridgeapp.NewService(handler.store, provideradapter.Local{})
	if err != nil {
		writePlanningError(response, http.StatusServiceUnavailable, "MCP_UNAVAILABLE")
		return
	}
	var session *bridgeapp.Session
	for attempt := 0; attempt < 8; attempt++ {
		run, loadErr := handler.store.Run(request.Context(), runID)
		if loadErr != nil || run.Execution.PrimarySession.ReservationSHA256 == "" {
			writePlanningError(response, http.StatusUnauthorized, "MCP_SESSION_INVALID")
			return
		}
		if role == agentprofile.RoleWorker && audienceHash != run.Execution.PrimarySession.ReservationSHA256 ||
			role == agentprofile.RoleReviewer && (run.Execution.Review == nil || run.Execution.Review.ReviewerSessionSHA256 == "" ||
				audienceHash != run.Execution.Review.ReviewerSessionSHA256) {
			writePlanningError(response, http.StatusUnauthorized, "MCP_SESSION_INVALID")
			return
		}
		binding, replay := replayMCPBinding(handler.store, request.Context(), run, role, audience, agentruntime.RequestIdentity(content))
		if !replay {
			binding, err = currentMCPBinding(handler.store, request.Context(), run, role, audience)
			if err != nil {
				writePlanningError(response, http.StatusConflict, "MCP_SESSION_STALE")
				return
			}
		}
		session, err = service.OpenSession(request.Context(), binding, time.Now().UnixMilli())
		if err == nil || !retryableMCPBindingError(err) {
			break
		}
	}
	if err != nil {
		writePlanningError(response, http.StatusConflict, "MCP_SESSION_STALE")
		return
	}
	var output bytes.Buffer
	if err := agentruntime.Serve(request.Context(), bytes.NewReader(append(content, '\n')), &output, session); err != nil || output.Len() > agentbridge.MaximumResponseBytes {
		writePlanningError(response, http.StatusServiceUnavailable, "MCP_UNAVAILABLE")
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	if output.Len() == 0 {
		response.WriteHeader(http.StatusNoContent)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	_, _ = response.Write(bytes.TrimSpace(output.Bytes()))
}
