// SPDX-License-Identifier: Apache-2.0

// Package agentbridge owns session MCP authorization and the durable typed
// command path inside the standalone Director Engine. The stdio runtime and
// Paseo connector only translate this application contract.
package agentbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/mcuadros/director-engine/domain"
	domainbridge "github.com/mcuadros/director-engine/domain/agentbridge"
	"github.com/mcuadros/director-engine/domain/agentprofile"
	domainexecution "github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/domain/jsondocument"
	providerport "github.com/mcuadros/director-engine/ports/provider"
	storeport "github.com/mcuadros/director-engine/ports/taskstore"
)

// Store is the exact aggregate/Command subset needed by the MCP ingress. It
// exposes no SQL, generic query, database selection, or backend handle.
type Store interface {
	Project(context.Context, string) (domain.Project, error)
	Workspace(context.Context, string) (domain.Workspace, error)
	Task(context.Context, string) (domain.Task, error)
	Run(context.Context, string) (domain.Run, error)
	Candidate(context.Context, string) (domain.Candidate, error)
	Command(context.Context, string) (domain.Command, error)
	UpdateRun(context.Context, domain.CommandRequest, domain.Run, domain.Event) (domain.CommandResult, error)
}

// Code is the closed application rejection vocabulary returned to transports.
type Code string

const (
	CodeSessionInvalid      Code = "MCP_SESSION_INVALID"
	CodeProviderPreflight   Code = "MCP_PROVIDER_PREFLIGHT_FAILED"
	CodeToolNotAllowed      Code = "MCP_TOOL_NOT_ALLOWED"
	CodeInputInvalid        Code = "MCP_INPUT_INVALID"
	CodeInputTooLarge       Code = "MCP_INPUT_TOO_LARGE"
	CodeSecretRejected      Code = "MCP_SECRET_REJECTED"
	CodePrivatePathRejected Code = "MCP_PRIVATE_PATH_REJECTED"
	CodeScopeMismatch       Code = "MCP_SCOPE_MISMATCH"
	CodeExpectedState       Code = "MCP_EXPECTED_STATE_MISMATCH"
	CodeIdempotencyConflict Code = "MCP_IDEMPOTENCY_CONFLICT"
	CodeCommandLimit        Code = "MCP_COMMAND_LIMIT_REACHED"
	CodeStoreUnavailable    Code = "MCP_STORE_UNAVAILABLE"
	CodeOutputTooLarge      Code = "MCP_OUTPUT_TOO_LARGE"
	CodeIntegrityFailure    Code = "MCP_DURABLE_INTEGRITY_FAILURE"
)

// Failure contains a stable code and an optional precise provider-preflight
// reason. It never wraps raw adapter, store, or provider output.
type Failure struct {
	Code          Code
	PreflightCode domainbridge.PreflightCode
}

func (failure *Failure) Error() string {
	if failure.PreflightCode != "" {
		return fmt.Sprintf("%s: %s", failure.Code, failure.PreflightCode)
	}
	return string(failure.Code)
}

func fail(code Code) error { return &Failure{Code: code} }

func mapInputError(err error) error {
	var input *domainbridge.InputError
	if !errors.As(err, &input) {
		return fail(CodeInputInvalid)
	}
	switch input.Code {
	case domainbridge.InputTooLarge:
		return fail(CodeInputTooLarge)
	case domainbridge.InputSecret:
		return fail(CodeSecretRejected)
	case domainbridge.InputPrivatePath:
		return fail(CodePrivatePathRejected)
	default:
		return fail(CodeInputInvalid)
	}
}

type scopeFacts struct {
	project   domain.Project
	workspace domain.Workspace
	task      domain.Task
	run       domain.Run
	candidate *domain.Candidate
	role      agentprofile.FrozenRole
}

// Service composes typed aggregate facts with normalized provider discovery.
type Service struct {
	store     Store
	discovery providerport.Discovery
}

// NewService constructs the engine-owned ingress boundary.
func NewService(store Store, discovery providerport.Discovery) (*Service, error) {
	if store == nil || discovery == nil {
		return nil, errors.New("agent MCP store and provider discovery are required")
	}
	return &Service{store: store, discovery: discovery}, nil
}

func scopeReadFailure(err error) error {
	if errors.Is(err, storeport.ErrNotFound) {
		return fail(CodeScopeMismatch)
	}
	return fail(CodeStoreUnavailable)
}

// Descriptor is the immutable authorized session catalog.
type Descriptor struct {
	ContractVersion string                        `json:"contractVersion"`
	ContractSHA256  string                        `json:"contractSha256"`
	SessionSHA256   string                        `json:"sessionSha256"`
	Role            agentprofile.Role             `json:"role"`
	Tools           []domainbridge.ToolDefinition `json:"tools"`
}

// Session contains no mutable workflow state; every call rereads durable
// facts or replays an immutable Command result.
type Session struct {
	store        Store
	binding      domainbridge.SessionBinding
	descriptor   Descriptor
	tools        map[string]domainbridge.ToolDefinition
	criterionIDs []string
	baseSHA      string
}

func hashBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

// RunStateSHA256 binds a server-created session to the exact expected Run
// execution projection without putting that projection into tool arguments.
func RunStateSHA256(state domainexecution.State) (string, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	canonical, err := jsondocument.Canonical(encoded)
	if err != nil {
		return "", err
	}
	return hashBytes(canonical), nil
}

func (service *Service) loadScope(ctx context.Context, binding domainbridge.SessionBinding) (scopeFacts, error) {
	project, err := service.store.Project(ctx, binding.ProjectID)
	if err != nil {
		return scopeFacts{}, scopeReadFailure(err)
	}
	workspace, err := service.store.Workspace(ctx, binding.WorkspaceID)
	if err != nil {
		return scopeFacts{}, scopeReadFailure(err)
	}
	task, err := service.store.Task(ctx, binding.TaskID)
	if err != nil {
		return scopeFacts{}, scopeReadFailure(err)
	}
	run, err := service.store.Run(ctx, binding.RunID)
	if err != nil {
		return scopeFacts{}, scopeReadFailure(err)
	}
	facts := scopeFacts{project: project, workspace: workspace, task: task, run: run}
	if binding.CandidateID != "" {
		candidate, err := service.store.Candidate(ctx, binding.CandidateID)
		if err != nil {
			return scopeFacts{}, scopeReadFailure(err)
		}
		facts.candidate = &candidate
	}
	if err := immutableScope(binding, &facts); err != nil {
		return scopeFacts{}, err
	}
	return facts, nil
}

func immutableScope(binding domainbridge.SessionBinding, facts *scopeFacts) error {
	runScope := facts.run.Execution.Scope
	if facts.project.ID != binding.ProjectID || facts.workspace.ID != binding.WorkspaceID ||
		facts.workspace.ProjectID != binding.ProjectID || facts.task.ID != binding.TaskID ||
		facts.task.ProjectID != binding.ProjectID || len(facts.task.WorkspaceIDs) != 1 ||
		facts.task.WorkspaceIDs[0] != binding.WorkspaceID || facts.run.ID != binding.RunID ||
		facts.run.TaskID != binding.TaskID || runScope.ProjectID != binding.ProjectID ||
		runScope.WorkspaceID != binding.WorkspaceID || runScope.TaskID != binding.TaskID || runScope.RunID != binding.RunID {
		return fail(CodeScopeMismatch)
	}
	profiles := facts.run.Execution.EffectiveProfiles
	if profiles == nil || !profiles.Valid() || profiles.SHA256() != facts.run.Execution.EffectiveProfilesSHA256 ||
		profiles.SHA256() != binding.EffectiveProfilesSHA256 || profiles.OrganizerRevision() != binding.OrganizerRevision ||
		profiles.ConfigurationSHA256() != binding.ConfigurationSHA256 {
		return fail(CodeScopeMismatch)
	}
	role, exists := profiles.Role(binding.Role)
	if !exists {
		return fail(CodeScopeMismatch)
	}
	facts.role = role
	if binding.Role == agentprofile.RoleWorker && facts.run.Execution.Agent.ExternalID != "" &&
		facts.run.Execution.Agent.ExternalID != binding.NativeAgentID {
		return fail(CodeScopeMismatch)
	}
	if binding.Role == agentprofile.RoleReviewer {
		if facts.candidate == nil || facts.candidate.ID != binding.CandidateID || facts.candidate.RunID != binding.RunID ||
			facts.candidate.CommitSHA != binding.CandidateSHA {
			return fail(CodeScopeMismatch)
		}
	} else if facts.candidate != nil {
		return fail(CodeScopeMismatch)
	}
	return nil
}

// OpenSession verifies public provider facts before returning any catalog.
// Unsupported tuples therefore fail before a provider launch can receive MCP.
func (service *Service) OpenSession(ctx context.Context, binding domainbridge.SessionBinding, nowMillis int64) (*Session, error) {
	if err := domainbridge.ValidateSessionBinding(binding); err != nil {
		return nil, fail(CodeSessionInvalid)
	}
	facts, err := service.loadScope(ctx, binding)
	if err != nil {
		return nil, err
	}
	sessionHash, err := domainbridge.SessionDigest(binding)
	if err != nil {
		return nil, fail(CodeSessionInvalid)
	}
	if err := expectedState(binding, facts); err != nil {
		recovered := false
		for _, receipt := range facts.run.Execution.MCPCommandReceipts {
			if receipt.SessionSHA256 == sessionHash &&
				receipt.EffectiveProfilesSHA256 == binding.EffectiveProfilesSHA256 &&
				receipt.ConfigurationSHA256 == binding.ConfigurationSHA256 {
				recovered = true
				break
			}
		}
		if !recovered {
			return nil, err
		}
	}
	snapshot, err := service.discovery.Discover(ctx)
	if err != nil {
		return nil, &Failure{Code: CodeProviderPreflight, PreflightCode: domainbridge.PreflightDiscoveryInvalid}
	}
	if err := domainbridge.VerifyProviderPreflight(facts.role, snapshot, binding.ProviderDiscoveryRevision, nowMillis); err != nil {
		var preflight *domainbridge.PreflightError
		if errors.As(err, &preflight) {
			return nil, &Failure{Code: CodeProviderPreflight, PreflightCode: preflight.Code}
		}
		return nil, &Failure{Code: CodeProviderPreflight, PreflightCode: domainbridge.PreflightDiscoveryInvalid}
	}
	tools, err := domainbridge.Catalog(facts.role)
	if err != nil {
		return nil, fail(CodeSessionInvalid)
	}
	contractHash, err := domainbridge.SchemaSHA256()
	if err != nil {
		return nil, fail(CodeSessionInvalid)
	}
	byName := make(map[string]domainbridge.ToolDefinition, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	return &Session{
		store: service.store, binding: binding,
		descriptor: Descriptor{
			ContractVersion: domainbridge.ContractVersion, ContractSHA256: contractHash,
			SessionSHA256: sessionHash, Role: binding.Role, Tools: tools,
		},
		tools: byName, criterionIDs: slices.Clone(facts.run.Execution.CriterionIDs), baseSHA: facts.run.BaseSHA,
	}, nil
}

// Descriptor returns defensive catalog copies.
func (session *Session) Descriptor() Descriptor {
	descriptor := session.descriptor
	descriptor.Tools = slices.Clone(session.descriptor.Tools)
	for index := range descriptor.Tools {
		descriptor.Tools[index].Roles = slices.Clone(descriptor.Tools[index].Roles)
		descriptor.Tools[index].InputSchema = slices.Clone(descriptor.Tools[index].InputSchema)
	}
	return descriptor
}

func (session *Session) loadScope(ctx context.Context) (scopeFacts, error) {
	service := Service{store: session.store}
	return service.loadScope(ctx, session.binding)
}

func expectedState(binding domainbridge.SessionBinding, facts scopeFacts) error {
	stateHash, err := RunStateSHA256(facts.run.Execution)
	if err != nil || facts.project.Version != binding.ExpectedProjectVersion ||
		facts.workspace.Version != binding.ExpectedWorkspaceVersion || facts.task.Version != binding.ExpectedTaskVersion ||
		facts.run.Version != binding.ExpectedRunVersion || stateHash != binding.ExpectedRunStateSHA256 {
		return fail(CodeExpectedState)
	}
	return nil
}

type projectOutput struct {
	SchemaVersion string `json:"schemaVersion"`
	Project       struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		State   string `json:"state"`
		Version uint64 `json:"version"`
	} `json:"project"`
	Workspace struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Version uint64 `json:"version"`
	} `json:"workspace"`
	TaskID string            `json:"taskId"`
	RunID  string            `json:"runId"`
	Role   agentprofile.Role `json:"role"`
}

type taskOutput struct {
	SchemaVersion string `json:"schemaVersion"`
	ProjectID     string `json:"projectId"`
	WorkspaceID   string `json:"workspaceId"`
	Task          struct {
		ID                 string   `json:"id"`
		Key                string   `json:"key"`
		Title              string   `json:"title"`
		Objective          string   `json:"objective"`
		AcceptanceCriteria string   `json:"acceptanceCriteria"`
		CriterionIDs       []string `json:"criterionIds"`
		Priority           string   `json:"priority"`
		Version            uint64   `json:"version"`
	} `json:"task"`
	Run struct {
		ID                      string `json:"id"`
		Number                  uint64 `json:"number"`
		BaseSHA                 string `json:"baseSha"`
		Version                 uint64 `json:"version"`
		EffectiveProfilesSHA256 string `json:"effectiveProfilesSha256"`
	} `json:"run"`
}

var requiredReviewDimensions = []string{
	"acceptance", "correctness", "security", "maintainability",
	"readability", "design", "quality", "rigor",
}

type candidateOutput struct {
	SchemaVersion string `json:"schemaVersion"`
	ProjectID     string `json:"projectId"`
	WorkspaceID   string `json:"workspaceId"`
	Task          struct {
		ID                 string `json:"id"`
		Key                string `json:"key"`
		Title              string `json:"title"`
		Objective          string `json:"objective"`
		AcceptanceCriteria string `json:"acceptanceCriteria"`
	} `json:"task"`
	Run struct {
		ID      string `json:"id"`
		BaseSHA string `json:"baseSha"`
	} `json:"run"`
	Candidate struct {
		ID        string `json:"id"`
		CommitSHA string `json:"commitSha"`
	} `json:"candidate"`
	RequiredCoverage []string `json:"requiredCoverage"`
}

func boundedOutput(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fail(CodeOutputTooLarge)
	}
	canonical, err := jsondocument.Canonical(encoded)
	if err != nil || len(canonical) > domainbridge.MaximumResponseBytes {
		return nil, fail(CodeOutputTooLarge)
	}
	return canonical, nil
}

func readProject(binding domainbridge.SessionBinding, facts scopeFacts) (json.RawMessage, error) {
	output := projectOutput{SchemaVersion: domainbridge.ContractVersion, TaskID: binding.TaskID, RunID: binding.RunID, Role: binding.Role}
	output.Project.ID = facts.project.ID
	output.Project.Name = domainbridge.RedactedOutputText(facts.project.Name, 512)
	output.Project.State = domainbridge.RedactedOutputText(facts.project.State, 64)
	output.Project.Version = facts.project.Version
	output.Workspace.ID = facts.workspace.ID
	output.Workspace.Name = domainbridge.RedactedOutputText(facts.workspace.Name, 512)
	output.Workspace.Version = facts.workspace.Version
	return boundedOutput(output)
}

func readTask(facts scopeFacts) (json.RawMessage, error) {
	output := taskOutput{SchemaVersion: domainbridge.ContractVersion, ProjectID: facts.project.ID, WorkspaceID: facts.workspace.ID}
	output.Task.ID = facts.task.ID
	output.Task.Key = domainbridge.RedactedOutputText(facts.task.Key, 128)
	output.Task.Title = domainbridge.RedactedOutputText(facts.task.Title, 512)
	output.Task.Objective = domainbridge.RedactedOutputText(facts.task.Objective, 4096)
	output.Task.AcceptanceCriteria = domainbridge.RedactedOutputText(facts.task.AcceptanceCriteria, 16*1024)
	output.Task.CriterionIDs = slices.Clone(facts.run.Execution.CriterionIDs)
	output.Task.Priority = string(domain.EffectivePriority(facts.task.Priority))
	output.Task.Version = facts.task.Version
	output.Run.ID = facts.run.ID
	output.Run.Number = facts.run.Number
	output.Run.BaseSHA = facts.run.BaseSHA
	output.Run.Version = facts.run.Version
	output.Run.EffectiveProfilesSHA256 = facts.run.Execution.EffectiveProfilesSHA256
	return boundedOutput(output)
}

func readCandidate(facts scopeFacts) (json.RawMessage, error) {
	if facts.candidate == nil {
		return nil, fail(CodeScopeMismatch)
	}
	output := candidateOutput{
		SchemaVersion: domainbridge.ContractVersion, ProjectID: facts.project.ID,
		WorkspaceID: facts.workspace.ID, RequiredCoverage: slices.Clone(requiredReviewDimensions),
	}
	output.Task.ID = facts.task.ID
	output.Task.Key = domainbridge.RedactedOutputText(facts.task.Key, 128)
	output.Task.Title = domainbridge.RedactedOutputText(facts.task.Title, 512)
	output.Task.Objective = domainbridge.RedactedOutputText(facts.task.Objective, 4096)
	output.Task.AcceptanceCriteria = domainbridge.RedactedOutputText(facts.task.AcceptanceCriteria, 16*1024)
	output.Run.ID = facts.run.ID
	output.Run.BaseSHA = facts.run.BaseSHA
	output.Candidate.ID = facts.candidate.ID
	output.Candidate.CommitSHA = facts.candidate.CommitSHA
	return boundedOutput(output)
}

// Result is one bounded tool result. Payload is a projection for reads and a
// durable command receipt for writes.
type Result struct {
	Payload json.RawMessage `json:"payload"`
}

type commandPayload struct {
	SchemaVersion            string            `json:"schemaVersion"`
	SessionSHA256            string            `json:"sessionSha256"`
	Role                     agentprofile.Role `json:"role"`
	ProjectID                string            `json:"projectId"`
	WorkspaceID              string            `json:"workspaceId"`
	TaskID                   string            `json:"taskId"`
	RunID                    string            `json:"runId"`
	CandidateID              string            `json:"candidateId"`
	CandidateSHA             string            `json:"candidateSha"`
	NativeAgentID            string            `json:"nativeAgentId"`
	TurnID                   string            `json:"turnId"`
	ToolName                 string            `json:"toolName"`
	Capability               string            `json:"capability"`
	ExpectedProjectVersion   uint64            `json:"expectedProjectVersion"`
	ExpectedWorkspaceVersion uint64            `json:"expectedWorkspaceVersion"`
	ExpectedTaskVersion      uint64            `json:"expectedTaskVersion"`
	ExpectedRunVersion       uint64            `json:"expectedRunVersion"`
	ExpectedRunStateSHA256   string            `json:"expectedRunStateSha256"`
	EffectiveProfilesSHA256  string            `json:"effectiveProfilesSha256"`
	OrganizerRevision        string            `json:"organizerRevision"`
	ConfigurationSHA256      string            `json:"configurationSha256"`
	Arguments                json.RawMessage   `json:"arguments"`
}

type commandOutput struct {
	SchemaVersion   string `json:"schemaVersion"`
	CommandKey      string `json:"commandKey"`
	Status          string `json:"status"`
	ObservedVersion uint64 `json:"observedVersion"`
	Replay          bool   `json:"replay"`
}

func commandIdentity(binding domainbridge.SessionBinding, requestID string) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{binding.ProjectID, "mcp", binding.Audience, requestID}, "\x1f")))
	return "mcp-command-" + hex.EncodeToString(digest[:])
}

func canonicalCommandPayload(binding domainbridge.SessionBinding, descriptor Descriptor, tool domainbridge.ToolDefinition, arguments json.RawMessage) ([]byte, error) {
	payload := commandPayload{
		SchemaVersion: domainbridge.ContractVersion, SessionSHA256: descriptor.SessionSHA256,
		Role: binding.Role, ProjectID: binding.ProjectID, WorkspaceID: binding.WorkspaceID,
		TaskID: binding.TaskID, RunID: binding.RunID, CandidateID: binding.CandidateID,
		CandidateSHA: binding.CandidateSHA, NativeAgentID: binding.NativeAgentID, TurnID: binding.TurnID,
		ToolName: tool.Name, Capability: string(tool.Capability), ExpectedProjectVersion: binding.ExpectedProjectVersion,
		ExpectedWorkspaceVersion: binding.ExpectedWorkspaceVersion, ExpectedTaskVersion: binding.ExpectedTaskVersion,
		ExpectedRunVersion: binding.ExpectedRunVersion, ExpectedRunStateSHA256: binding.ExpectedRunStateSHA256,
		EffectiveProfilesSHA256: binding.EffectiveProfilesSHA256, OrganizerRevision: binding.OrganizerRevision,
		ConfigurationSHA256: binding.ConfigurationSHA256, Arguments: arguments,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fail(CodeInputInvalid)
	}
	canonical, err := jsondocument.Canonical(encoded)
	if err != nil || len(canonical) > domainbridge.MaximumRequestBytes {
		return nil, fail(CodeInputTooLarge)
	}
	return canonical, nil
}

func commandType(tool string) string {
	switch tool {
	case "director_planning_command_submit":
		return "agent.mcp.planning_command.submit"
	case "director_task_outcome_submit":
		return "agent.mcp.task_outcome.submit"
	case "director_review_verdict_submit":
		return "agent.mcp.review_verdict.submit"
	default:
		return ""
	}
}

func sameStoredCommand(stored domain.Command, key, kind, runID string, expected uint64, payload []byte) bool {
	if stored.IdempotencyKey != key || stored.Type != kind || stored.AggregateID != runID || stored.ExpectedVersion != expected {
		return false
	}
	canonical, err := jsondocument.Canonical(stored.Payload)
	return err == nil && bytes.Equal(canonical, payload)
}

func receiptPresent(run domain.Run, key, payloadHash, sessionHash string) bool {
	for _, receipt := range run.Execution.MCPCommandReceipts {
		if receipt.CommandKey == key {
			return receipt.PayloadSHA256 == payloadHash && receipt.SessionSHA256 == sessionHash
		}
	}
	return false
}

func (session *Session) replay(ctx context.Context, key, kind string, payload []byte) (Result, bool, error) {
	stored, err := session.store.Command(ctx, key)
	if errors.Is(err, storeport.ErrNotFound) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, fail(CodeStoreUnavailable)
	}
	if !sameStoredCommand(stored, key, kind, session.binding.RunID, session.binding.ExpectedRunVersion, payload) {
		return Result{}, true, fail(CodeIdempotencyConflict)
	}
	if stored.Outcome == domain.CommandApplied {
		run, err := session.store.Run(ctx, session.binding.RunID)
		if err != nil {
			return Result{}, true, fail(CodeStoreUnavailable)
		}
		if !receiptPresent(run, key, hashBytes(payload), session.descriptor.SessionSHA256) {
			return Result{}, true, fail(CodeIntegrityFailure)
		}
	}
	status := "accepted"
	if stored.Outcome == domain.CommandRejectedVersionConflict {
		status = "version_conflict"
	}
	output, err := boundedOutput(commandOutput{
		SchemaVersion: domainbridge.ContractVersion, CommandKey: key, Status: status,
		ObservedVersion: stored.ObservedVersion, Replay: true,
	})
	return Result{Payload: output}, true, err
}

func (session *Session) mutate(ctx context.Context, requestID string, tool domainbridge.ToolDefinition, arguments json.RawMessage) (Result, error) {
	kind := commandType(tool.Name)
	if kind == "" {
		return Result{}, fail(CodeToolNotAllowed)
	}
	payload, err := canonicalCommandPayload(session.binding, session.descriptor, tool, arguments)
	if err != nil {
		return Result{}, err
	}
	key := commandIdentity(session.binding, requestID)
	if replay, found, err := session.replay(ctx, key, kind, payload); found || err != nil {
		return replay, err
	}
	facts, err := session.loadScope(ctx)
	if err != nil {
		return Result{}, err
	}
	if err := expectedState(session.binding, facts); err != nil && facts.run.Version == session.binding.ExpectedRunVersion {
		// A stale Run version is submitted below so the TaskStore can persist a
		// replayable version-conflict outcome. Any other expected-state drift
		// refuses before mutation because UpdateRun cannot compare that fact.
		return Result{}, err
	}
	if len(facts.run.Execution.MCPCommandReceipts) >= domainexecution.MaximumMCPCommandReceipts {
		return Result{}, fail(CodeCommandLimit)
	}
	payloadHash := hashBytes(payload)
	next := facts.run
	next.Version = session.binding.ExpectedRunVersion + 1
	next.Execution.MCPCommandReceipts = append(slices.Clone(facts.run.Execution.MCPCommandReceipts), domainexecution.MCPCommandReceipt{
		CommandKey: key, ToolName: tool.Name, Capability: string(tool.Capability), Role: string(session.binding.Role),
		SessionSHA256: session.descriptor.SessionSHA256, PayloadSHA256: payloadHash,
		EffectiveProfilesSHA256: session.binding.EffectiveProfilesSHA256,
		ConfigurationSHA256:     session.binding.ConfigurationSHA256,
	})
	eventPayload, _ := json.Marshal(next.Execution.MCPCommandReceipts[len(next.Execution.MCPCommandReceipts)-1])
	result, err := session.store.UpdateRun(ctx, domain.CommandRequest{
		IdempotencyKey: key, Type: kind, AggregateID: session.binding.RunID,
		ExpectedVersion: session.binding.ExpectedRunVersion, Payload: payload,
	}, next, domain.Event{
		ID: "mcp-event-" + strings.TrimPrefix(key, "mcp-command-"), RunID: session.binding.RunID,
		Sequence: session.binding.ExpectedRunVersion + 2, AggregateID: session.binding.RunID,
		AggregateVersion: session.binding.ExpectedRunVersion + 1, Type: kind, Payload: eventPayload,
	})
	if err != nil {
		if errors.Is(err, storeport.ErrIdempotencyConflict) {
			return Result{}, fail(CodeIdempotencyConflict)
		}
		return Result{}, fail(CodeStoreUnavailable)
	}
	status := "accepted"
	if result.Outcome == domain.CommandRejectedVersionConflict {
		status = "version_conflict"
	}
	output, err := boundedOutput(commandOutput{
		SchemaVersion: domainbridge.ContractVersion, CommandKey: key, Status: status,
		ObservedVersion: result.ObservedVersion, Replay: result.Replay,
	})
	return Result{Payload: output}, err
}

// Call authorizes one exact catalog tool. Scope selectors are impossible
// because they are absent from every tool schema and injected from binding.
func (session *Session) Call(ctx context.Context, requestID, toolName string, rawArguments json.RawMessage) (Result, error) {
	if requestID == "" || len(requestID) > 128 || strings.IndexFunc(requestID, func(r rune) bool { return r < 0x20 }) >= 0 {
		return Result{}, fail(CodeInputInvalid)
	}
	tool, allowed := session.tools[toolName]
	if !allowed {
		return Result{}, fail(CodeToolNotAllowed)
	}
	arguments, err := domainbridge.ValidateToolInput(toolName, rawArguments, session.criterionIDs, session.baseSHA)
	if err != nil {
		return Result{}, mapInputError(err)
	}
	if tool.Mutating {
		return session.mutate(ctx, requestID, tool, arguments)
	}
	facts, err := session.loadScope(ctx)
	if err != nil {
		return Result{}, err
	}
	if err := expectedState(session.binding, facts); err != nil {
		return Result{}, err
	}
	var payload json.RawMessage
	switch toolName {
	case "director_project_read":
		payload, err = readProject(session.binding, facts)
	case "director_task_read":
		payload, err = readTask(facts)
	case "director_candidate_read":
		payload, err = readCandidate(facts)
	default:
		return Result{}, fail(CodeToolNotAllowed)
	}
	if err != nil {
		return Result{}, err
	}
	return Result{Payload: payload}, nil
}
