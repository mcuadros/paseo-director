// SPDX-License-Identifier: Apache-2.0

// Package projectadmin implements the Project-scoped administration MCP. It
// translates closed agent intents into existing Engine planning and control
// applications; it is not a human-RPC adapter and owns no workflow reducer.
package projectadmin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mcuadros/director-engine/domain"
	domainexecution "github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/domain/jsondocument"
	contract "github.com/mcuadros/director-engine/domain/projectadmin"
	executionport "github.com/mcuadros/director-engine/ports/execution"
	planningport "github.com/mcuadros/director-engine/ports/planning"
	storeport "github.com/mcuadros/director-engine/ports/taskstore"
)

type Code string

const (
	CodeSessionInvalid          Code = "MCP_SESSION_INVALID"
	CodeSessionRevoked          Code = "MCP_SESSION_REVOKED"
	CodeScopeMismatch           Code = "MCP_SCOPE_MISMATCH"
	CodeToolNotAllowed          Code = "MCP_TOOL_NOT_ALLOWED"
	CodeInputInvalid            Code = "MCP_INPUT_INVALID"
	CodeInputTooLarge           Code = "MCP_INPUT_TOO_LARGE"
	CodeOutputTooLarge          Code = "MCP_OUTPUT_TOO_LARGE"
	CodeIdempotencyConflict     Code = "MCP_IDEMPOTENCY_CONFLICT"
	CodeExpectedVersionConflict Code = "MCP_EXPECTED_VERSION_CONFLICT"
	CodeContractMismatch        Code = "MCP_CONTRACT_MISMATCH"
	CodeActionNotAllowed        Code = "MCP_ACTION_NOT_ALLOWED"
	CodeCommandLimit            Code = "MCP_COMMAND_LIMIT"
	CodeUnavailable             Code = "MCP_UNAVAILABLE"
)

type Failure struct{ Code Code }

func (failure *Failure) Error() string { return string(failure.Code) }
func fail(code Code) error             { return &Failure{Code: code} }

type Store interface {
	Project(context.Context, string) (domain.Project, error)
	Projects(context.Context) ([]domain.Project, error)
	UpdateProject(context.Context, domain.CommandRequest, domain.Project, domain.Event) (domain.CommandResult, error)
	Workspace(context.Context, string) (domain.Workspace, error)
	Workspaces(context.Context, string) ([]domain.Workspace, error)
	Epic(context.Context, string) (domain.Epic, error)
	Epics(context.Context, string) ([]domain.Epic, error)
	Task(context.Context, string) (domain.Task, error)
	Tasks(context.Context, string) ([]domain.Task, error)
	Run(context.Context, string) (domain.Run, error)
	Runs(context.Context, string) ([]domain.Run, error)
	Candidates(context.Context, string) ([]domain.Candidate, error)
	Command(context.Context, string) (domain.Command, error)
	LatestEventSequence(context.Context) (uint64, error)
}

type PlanningReader interface {
	Query(context.Context, planningport.QueryInput) (planningport.Snapshot, error)
}

type Planner interface {
	Mutate(context.Context, planningport.MutationInput, planningport.Actor) (planningport.Result, error)
}

type Controller interface {
	RequestProjectControl(context.Context, executionport.ControlCommand) (executionport.ControlResult, error)
	CancelTask(context.Context, executionport.ControlCommand) (executionport.ControlResult, error)
}

type Reconciler interface {
	ReconcileRun(context.Context, string) (bool, error)
}

type Service struct {
	store      Store
	reader     PlanningReader
	planner    Planner
	controller Controller
	reconciler Reconciler
	now        func() int64
}

func NewService(store Store, reader PlanningReader, planner Planner, controller Controller, reconciler Reconciler) (*Service, error) {
	if store == nil || reader == nil || planner == nil || controller == nil {
		return nil, errors.New("Project administration composition is incomplete")
	}
	return &Service{store: store, reader: reader, planner: planner, controller: controller, reconciler: reconciler,
		now: func() int64 { return time.Now().UnixMilli() }}, nil
}

type Registration struct {
	RequestID         string `json:"requestId"`
	SessionID         string `json:"sessionId"`
	NativeWorkspaceID string `json:"nativeWorkspaceId"`
	NativeAgentID     string `json:"nativeAgentId"`
	Audience          string `json:"audience"`
	TokenSHA256       string `json:"tokenSha256"`
}

func digest(parts ...string) string {
	value := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(value[:])
}

func commandKey(prefix string, parts ...string) string { return prefix + "-" + digest(parts...)[:32] }

func boundedIdentity(value string) bool {
	return value != "" && len(value) <= 128 && strings.IndexFunc(value, func(character rune) bool {
		return !(character == '-' || character == '_' || character == '.' || character == ':' || character == '@' || character == '/' ||
			character >= '0' && character <= '9' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z')
	}) < 0
}

func canonical(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return jsondocument.Canonical(encoded)
}

func projectEvent(key, projectID, kind string, nextVersion uint64, payload json.RawMessage) domain.Event {
	return domain.Event{ID: "event-" + strings.TrimPrefix(key, "project-admin-"), Sequence: nextVersion + 1,
		AggregateID: projectID, AggregateVersion: nextVersion, Type: kind, Payload: payload}
}

func validRegistration(input Registration) bool {
	binding := contract.SessionBinding{SchemaVersion: contract.SessionSchemaVersion, SessionID: input.SessionID,
		ProjectID: "placeholder", NativeWorkspaceID: input.NativeWorkspaceID, NativeAgentID: input.NativeAgentID, Audience: input.Audience}
	return contract.ValidBinding(binding) && boundedIdentity(input.RequestID) &&
		len(input.TokenSHA256) == 64 && strings.IndexFunc(input.TokenSHA256, func(value rune) bool {
		return !((value >= '0' && value <= '9') || (value >= 'a' && value <= 'f'))
	}) < 0
}

func (service *Service) projectForNativeWorkspace(ctx context.Context, nativeID string) (domain.Project, domain.Workspace, error) {
	projects, err := service.store.Projects(ctx)
	if err != nil {
		return domain.Project{}, domain.Workspace{}, fail(CodeUnavailable)
	}
	var matchedProject domain.Project
	var matchedWorkspace domain.Workspace
	matches := 0
	for _, project := range projects {
		workspaces, loadErr := service.store.Workspaces(ctx, project.ID)
		if loadErr != nil {
			return domain.Project{}, domain.Workspace{}, fail(CodeUnavailable)
		}
		for _, workspace := range workspaces {
			if workspace.NativePaseoWorkspaceID == nativeID {
				matchedProject, matchedWorkspace, matches = project, workspace, matches+1
			}
		}
	}
	if matches != 1 || matchedProject.Organizer == nil || matchedProject.Organizer.Phase != domain.OrganizerPhaseActive {
		return domain.Project{}, domain.Workspace{}, fail(CodeScopeMismatch)
	}
	return matchedProject, matchedWorkspace, nil
}

func (service *Service) RegisterSession(ctx context.Context, input Registration) (contract.SessionRecord, uint64, error) {
	if !validRegistration(input) {
		return contract.SessionRecord{}, 0, fail(CodeInputInvalid)
	}
	project, _, err := service.projectForNativeWorkspace(ctx, input.NativeWorkspaceID)
	if err != nil {
		return contract.SessionRecord{}, 0, err
	}
	binding := contract.SessionBinding{SchemaVersion: contract.SessionSchemaVersion, SessionID: input.SessionID,
		ProjectID: project.ID, NativeWorkspaceID: input.NativeWorkspaceID, NativeAgentID: input.NativeAgentID, Audience: input.Audience}
	key := commandKey("project-admin-session", input.RequestID, input.SessionID)
	type registrationPayload struct {
		SchemaVersion int                    `json:"schemaVersion"`
		RequestID     string                 `json:"requestId"`
		Record        contract.SessionRecord `json:"record"`
	}
	if stored, loadErr := service.store.Command(ctx, key); loadErr == nil {
		current, projectErr := service.store.Project(ctx, project.ID)
		var persisted registrationPayload
		if projectErr != nil || stored.Type != "project.admin_mcp.session_registered" || stored.AggregateID != project.ID ||
			stored.PayloadHash == "" || json.Unmarshal(stored.Payload, &persisted) != nil || persisted.SchemaVersion != 1 ||
			persisted.RequestID != input.RequestID || persisted.Record.Binding != binding ||
			persisted.Record.TokenSHA256 != input.TokenSHA256 || persisted.Record.State != contract.SessionActive {
			return contract.SessionRecord{}, 0, fail(CodeIdempotencyConflict)
		}
		for _, session := range current.ProjectAdmin.Sessions {
			if session.Binding.SessionID == input.SessionID && session == persisted.Record {
				return session, current.Version, nil
			}
		}
		return contract.SessionRecord{}, 0, fail(CodeUnavailable)
	} else if !errors.Is(loadErr, storeport.ErrNotFound) {
		return contract.SessionRecord{}, 0, fail(CodeUnavailable)
	}
	record := contract.SessionRecord{Binding: binding, TokenSHA256: input.TokenSHA256, State: contract.SessionActive, CreatedAtMillis: service.now()}
	payload, _ := canonical(registrationPayload{1, input.RequestID, record})
	projects, loadErr := service.store.Projects(ctx)
	if loadErr != nil {
		return contract.SessionRecord{}, 0, fail(CodeUnavailable)
	}
	for _, candidate := range projects {
		for _, session := range candidate.ProjectAdmin.Sessions {
			if session.Binding.SessionID == input.SessionID {
				return contract.SessionRecord{}, 0, fail(CodeIdempotencyConflict)
			}
		}
	}
	if len(project.ProjectAdmin.Sessions) >= contract.MaximumSessions {
		return contract.SessionRecord{}, 0, fail(CodeCommandLimit)
	}
	next := project
	next.ProjectAdmin = contract.Clone(project.ProjectAdmin)
	next.ProjectAdmin.Sessions = append(next.ProjectAdmin.Sessions, record)
	next.Version++
	result, writeErr := service.store.UpdateProject(ctx, domain.CommandRequest{IdempotencyKey: key,
		Type: "project.admin_mcp.session_registered", AggregateID: project.ID, ExpectedVersion: project.Version, Payload: payload},
		next, projectEvent(key, project.ID, "project.admin_mcp.session_registered", next.Version, payload))
	if writeErr != nil {
		if errors.Is(writeErr, storeport.ErrIdempotencyConflict) {
			return contract.SessionRecord{}, 0, fail(CodeIdempotencyConflict)
		}
		return contract.SessionRecord{}, 0, fail(CodeUnavailable)
	}
	if result.Outcome != domain.CommandApplied {
		return contract.SessionRecord{}, result.ObservedVersion, fail(CodeExpectedVersionConflict)
	}
	current, readErr := service.store.Project(ctx, project.ID)
	if readErr != nil {
		return contract.SessionRecord{}, 0, fail(CodeUnavailable)
	}
	return record, current.Version, nil
}

func mustCanonical(value []byte) []byte {
	canonical, err := jsondocument.Canonical(value)
	if err != nil {
		return nil
	}
	return canonical
}

func (service *Service) RevokeSession(ctx context.Context, requestID, sessionID string) error {
	if !boundedIdentity(requestID) || !boundedIdentity(sessionID) {
		return fail(CodeInputInvalid)
	}
	project, index, record, err := service.findSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if record.State == contract.SessionRevoked {
		return nil
	}
	next := project
	next.ProjectAdmin = contract.Clone(project.ProjectAdmin)
	next.ProjectAdmin.Sessions[index].State = contract.SessionRevoked
	next.ProjectAdmin.Sessions[index].RevokedAtMillis = service.now()
	next.Version++
	key := commandKey("project-admin-revoke", requestID, sessionID)
	payload, _ := canonical(struct {
		SchemaVersion int    `json:"schemaVersion"`
		RequestID     string `json:"requestId"`
		SessionID     string `json:"sessionId"`
	}{1, requestID, sessionID})
	result, writeErr := service.store.UpdateProject(ctx, domain.CommandRequest{IdempotencyKey: key,
		Type: "project.admin_mcp.session_revoked", AggregateID: project.ID, ExpectedVersion: project.Version, Payload: payload},
		next, projectEvent(key, project.ID, "project.admin_mcp.session_revoked", next.Version, payload))
	if writeErr != nil {
		return fail(CodeUnavailable)
	}
	if result.Outcome != domain.CommandApplied {
		return fail(CodeExpectedVersionConflict)
	}
	_, _, persisted, readErr := service.findSession(ctx, sessionID)
	if readErr != nil || persisted.State != contract.SessionRevoked || persisted.RevokedAtMillis == 0 {
		return fail(CodeUnavailable)
	}
	return nil
}

func (service *Service) findSession(ctx context.Context, sessionID string) (domain.Project, int, contract.SessionRecord, error) {
	projects, err := service.store.Projects(ctx)
	if err != nil {
		return domain.Project{}, -1, contract.SessionRecord{}, fail(CodeUnavailable)
	}
	var project domain.Project
	var record contract.SessionRecord
	index, matches := -1, 0
	for _, candidate := range projects {
		for currentIndex, session := range candidate.ProjectAdmin.Sessions {
			if session.Binding.SessionID == sessionID {
				project, record, index, matches = candidate, session, currentIndex, matches+1
			}
		}
	}
	if matches != 1 {
		return domain.Project{}, -1, contract.SessionRecord{}, fail(CodeSessionInvalid)
	}
	return project, index, record, nil
}

type Descriptor struct {
	ContractVersion string                    `json:"contractVersion"`
	ContractSHA256  string                    `json:"contractSha256"`
	SessionSHA256   string                    `json:"sessionSha256"`
	Tools           []contract.ToolDefinition `json:"tools"`
}

type Result struct {
	Payload json.RawMessage `json:"payload"`
}

type Session struct {
	service    *Service
	binding    contract.SessionBinding
	descriptor Descriptor
	tools      map[string]contract.ToolDefinition
}

func (service *Service) OpenSession(ctx context.Context, sessionID, audience, token string) (*Session, error) {
	project, _, record, err := service.findSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if record.State != contract.SessionActive {
		return nil, fail(CodeSessionRevoked)
	}
	if record.Binding.ProjectID != project.ID || record.Binding.Audience != audience || !contract.TokenMatches(record, token) {
		return nil, fail(CodeSessionInvalid)
	}
	_, workspace, err := service.projectForNativeWorkspace(ctx, record.Binding.NativeWorkspaceID)
	if err != nil || workspace.ProjectID != project.ID {
		return nil, fail(CodeScopeMismatch)
	}
	definition, err := contract.EmbeddedDefinition()
	if err != nil {
		return nil, fail(CodeUnavailable)
	}
	hash, _ := contract.SchemaSHA256()
	tools := make(map[string]contract.ToolDefinition, len(definition.Tools))
	for _, tool := range definition.Tools {
		tools[tool.Name] = tool
	}
	return &Session{service: service, binding: record.Binding,
		descriptor: Descriptor{ContractVersion: contract.ContractVersion, ContractSHA256: hash,
			SessionSHA256: digest(record.Binding.ProjectID, record.Binding.SessionID, record.Binding.Audience), Tools: definition.Tools},
		tools: tools}, nil
}

func (session *Session) Descriptor() Descriptor {
	result := session.descriptor
	result.Tools = slices.Clone(result.Tools)
	for index := range result.Tools {
		result.Tools[index].InputSchema = slices.Clone(result.Tools[index].InputSchema)
	}
	return result
}

func strict(raw json.RawMessage, destination any, exact []string) error {
	if len(raw) == 0 {
		return fail(CodeInputInvalid)
	}
	if len(raw) > contract.MaximumRequestBytes {
		return fail(CodeInputTooLarge)
	}
	canonical, err := jsondocument.CanonicalWithNormalizedNumbersLimit(raw, contract.MaximumRequestBytes)
	if err != nil {
		return fail(CodeInputInvalid)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(canonical, &fields) != nil || len(fields) != len(exact) {
		return fail(CodeInputInvalid)
	}
	for _, name := range exact {
		if _, exists := fields[name]; !exists {
			return fail(CodeInputInvalid)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	if decoder.Decode(destination) != nil {
		return fail(CodeInputInvalid)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fail(CodeInputInvalid)
	}
	return nil
}

func preflightRaw(raw json.RawMessage) error {
	if len(raw) == 0 {
		return fail(CodeInputInvalid)
	}
	if len(raw) > contract.MaximumRequestBytes {
		return fail(CodeInputTooLarge)
	}
	return nil
}

func boundedOutput(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fail(CodeUnavailable)
	}
	canonical, err := jsondocument.Canonical(encoded)
	if err != nil {
		return nil, fail(CodeUnavailable)
	}
	if len(canonical) > contract.MaximumResponseBytes {
		return nil, fail(CodeOutputTooLarge)
	}
	return canonical, nil
}

func (session *Session) current(ctx context.Context) (domain.Project, error) {
	project, _, record, err := session.service.findSession(ctx, session.binding.SessionID)
	if err != nil {
		return domain.Project{}, err
	}
	if record.Binding != session.binding {
		return domain.Project{}, fail(CodeScopeMismatch)
	}
	if record.State != contract.SessionActive {
		return domain.Project{}, fail(CodeSessionRevoked)
	}
	return project, nil
}

func (session *Session) projectRead(ctx context.Context) (json.RawMessage, error) {
	project, err := session.current(ctx)
	if err != nil {
		return nil, err
	}
	workspaces, err := session.service.store.Workspaces(ctx, project.ID)
	if err != nil {
		return nil, fail(CodeUnavailable)
	}
	type workspaceProjection struct {
		ID                string                 `json:"id"`
		Version           string                 `json:"version"`
		Name              string                 `json:"name"`
		DefaultBaseBranch string                 `json:"defaultBaseBranch"`
		Policy            domain.WorkspacePolicy `json:"policy"`
	}
	projected := make([]workspaceProjection, 0, len(workspaces))
	for _, workspace := range workspaces {
		projected = append(projected, workspaceProjection{workspace.ID, strconv.FormatUint(workspace.Version, 10), workspace.Name,
			workspace.DefaultBaseBranch, workspace.Policy})
	}
	organizer := struct {
		ID                  string `json:"id"`
		Mode                string `json:"mode"`
		Phase               string `json:"phase"`
		OrganizerRevision   string `json:"organizerRevision"`
		ConfigurationSHA256 string `json:"configurationSha256"`
	}{project.Organizer.ID, string(project.Organizer.Mode), string(project.Organizer.Phase), project.Organizer.OrganizerRevision, project.Organizer.ConfigurationSHA256}
	return boundedOutput(struct {
		SchemaVersion string                `json:"schemaVersion"`
		Project       any                   `json:"project"`
		Organizer     any                   `json:"organizer"`
		Workspaces    []workspaceProjection `json:"workspaces"`
		Scope         any                   `json:"scope"`
	}{contract.ContractVersion,
		struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			State   string `json:"state"`
			Version string `json:"version"`
		}{project.ID, project.Name, project.State, strconv.FormatUint(project.Version, 10)}, organizer, projected,
		struct {
			Kind string `json:"kind"`
		}{"own-project"}})
}

func planningInput(projectID string, cursor *string, pageSize int) planningport.QueryInput {
	return planningport.QueryInput{ProjectID: &projectID, WorkspaceIDs: []string{}, EpicIDs: []string{}, States: []string{},
		Priorities: []string{}, Labels: []string{}, Attention: []string{}, Search: nil, Sort: "scheduler_order",
		Cursor: cursor, PageSize: pageSize}
}

func (session *Session) planningRead(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	if err := preflightRaw(raw); err != nil {
		return nil, err
	}
	var input struct {
		Cursor   *string `json:"cursor"`
		PageSize int     `json:"pageSize"`
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, fail(CodeInputInvalid)
	}
	exact := []string{}
	if _, ok := fields["cursor"]; ok {
		exact = append(exact, "cursor")
	}
	if _, ok := fields["pageSize"]; ok {
		exact = append(exact, "pageSize")
	}
	if err := strict(raw, &input, exact); err != nil {
		return nil, err
	}
	if input.PageSize == 0 {
		input.PageSize = planningport.MaximumPageSize
	}
	project, err := session.current(ctx)
	if err != nil {
		return nil, err
	}
	query := planningInput(project.ID, input.Cursor, input.PageSize)
	if planningport.ValidateQuery(query) != nil {
		return nil, fail(CodeInputInvalid)
	}
	snapshot, err := session.service.reader.Query(ctx, query)
	if err != nil {
		return nil, fail(CodeUnavailable)
	}
	if snapshot.Page.SelectedProjectID == nil || *snapshot.Page.SelectedProjectID != project.ID ||
		len(snapshot.Page.Projects) != 1 || snapshot.Page.Projects[0].ID != project.ID {
		return nil, fail(CodeScopeMismatch)
	}
	return boundedOutput(snapshot)
}

type executionArguments struct {
	View   string  `json:"view"`
	TaskID *string `json:"taskId"`
	RunID  *string `json:"runId"`
}

func (session *Session) executionRead(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	if err := preflightRaw(raw); err != nil {
		return nil, err
	}
	var input executionArguments
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, fail(CodeInputInvalid)
	}
	exact := []string{"view"}
	if _, exists := fields["taskId"]; exists {
		exact = append(exact, "taskId")
	}
	if _, exists := fields["runId"]; exists {
		exact = append(exact, "runId")
	}
	if err := strict(raw, &input, exact); err != nil {
		return nil, err
	}
	if input.View != "runs" && input.View != "workers" && input.View != "reviews" && input.View != "queue" {
		return nil, fail(CodeInputInvalid)
	}
	if input.TaskID != nil && !boundedIdentity(*input.TaskID) || input.RunID != nil && !boundedIdentity(*input.RunID) {
		return nil, fail(CodeInputInvalid)
	}
	project, err := session.current(ctx)
	if err != nil {
		return nil, err
	}
	tasks, err := session.service.store.Tasks(ctx, project.ID)
	if err != nil {
		return nil, fail(CodeUnavailable)
	}
	selected := make([]domain.Task, 0, len(tasks))
	for _, task := range tasks {
		if input.TaskID == nil || task.ID == *input.TaskID {
			selected = append(selected, task)
		}
	}
	if input.TaskID != nil && len(selected) != 1 {
		return nil, fail(CodeScopeMismatch)
	}
	type row struct {
		TaskID          string `json:"taskId"`
		RunID           string `json:"runId"`
		State           string `json:"state"`
		Version         string `json:"version"`
		Number          uint64 `json:"number"`
		CandidateSHA    string `json:"candidateSha,omitempty"`
		WorkerPresent   bool   `json:"workerPresent"`
		ReviewerPresent bool   `json:"reviewerPresent"`
	}
	rows := []row{}
	if input.View == "queue" {
		if input.RunID != nil {
			return nil, fail(CodeInputInvalid)
		}
		for _, task := range selected {
			state := "queued"
			if task.Complete {
				state = "done"
			}
			rows = append(rows, row{TaskID: task.ID, State: state, Version: strconv.FormatUint(task.Version, 10)})
		}
	}
	for _, task := range selected {
		if input.View == "queue" {
			continue
		}
		runs, loadErr := session.service.store.Runs(ctx, task.ID)
		if loadErr != nil {
			return nil, fail(CodeUnavailable)
		}
		for _, run := range runs {
			if input.RunID != nil && run.ID != *input.RunID {
				continue
			}
			if input.View == "workers" && run.Execution.Agent.ExternalID == "" {
				continue
			}
			if input.View == "reviews" && (run.Execution.Review == nil || run.Execution.Review.ReviewerUUID == "") {
				continue
			}
			state := "active"
			if run.Execution.Terminal {
				state = "terminal"
			}
			candidateSHA := ""
			if run.CurrentCandidateID != "" {
				candidates, candidateErr := session.service.store.Candidates(ctx, run.ID)
				if candidateErr != nil {
					return nil, fail(CodeUnavailable)
				}
				for _, candidate := range candidates {
					if candidate.ID == run.CurrentCandidateID {
						candidateSHA = candidate.CommitSHA
					}
				}
			}
			rows = append(rows, row{TaskID: task.ID, RunID: run.ID, State: state, Version: strconv.FormatUint(run.Version, 10),
				Number: run.Number, CandidateSHA: candidateSHA, WorkerPresent: run.Execution.Agent.ExternalID != "",
				ReviewerPresent: run.Execution.Review != nil && run.Execution.Review.ReviewerUUID != ""})
		}
	}
	if input.RunID != nil && len(rows) != 1 {
		return nil, fail(CodeScopeMismatch)
	}
	if len(rows) > 256 {
		return nil, fail(CodeOutputTooLarge)
	}
	return boundedOutput(struct {
		SchemaVersion string `json:"schemaVersion"`
		View          string `json:"view"`
		Rows          []row  `json:"rows"`
	}{contract.ContractVersion, input.View, rows})
}

type mutationArguments struct {
	RequestID          string   `json:"requestId"`
	IdempotencyKey     string   `json:"idempotencyKey"`
	ExpectedVersion    string   `json:"expectedVersion"`
	Type               string   `json:"type"`
	Name               string   `json:"name"`
	Key                string   `json:"key"`
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	Objective          string   `json:"objective"`
	WorkspaceID        string   `json:"workspaceId"`
	EpicID             string   `json:"epicId"`
	TaskID             string   `json:"taskId"`
	DependencyKind     string   `json:"dependencyKind"`
	DependencyID       string   `json:"dependencyId"`
	AcceptanceCriteria []string `json:"acceptanceCriteria"`
	Labels             []string `json:"labels"`
	Priority           *string  `json:"priority"`
}

func mutationFields(kind string) []string {
	base := []string{"requestId", "idempotencyKey", "expectedVersion", "type"}
	switch kind {
	case "project.update":
		return append(base, "name")
	case "epic.create":
		return append(base, "key", "title", "description", "priority", "labels")
	case "epic.update":
		return append(base, "epicId", "title", "description", "priority", "labels")
	case "task.create":
		return append(base, "workspaceId", "epicId", "key", "title", "objective", "acceptanceCriteria", "priority", "labels")
	case "task.update":
		return append(base, "taskId", "epicId", "title", "objective", "acceptanceCriteria", "priority", "labels")
	case "dependency.add", "dependency.remove":
		return append(base, "taskId", "dependencyKind", "dependencyId")
	case "task.launch-now":
		return append(base, "taskId")
	default:
		return nil
	}
}

func decodeMutation(raw json.RawMessage) (mutationArguments, error) {
	if err := preflightRaw(raw); err != nil {
		return mutationArguments{}, err
	}
	var kind struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &kind) != nil || mutationFields(kind.Type) == nil {
		return mutationArguments{}, fail(CodeInputInvalid)
	}
	var input mutationArguments
	if err := strict(raw, &input, mutationFields(kind.Type)); err != nil {
		return mutationArguments{}, err
	}
	if !boundedIdentity(input.RequestID) || input.RequestID != input.IdempotencyKey {
		return mutationArguments{}, fail(CodeInputInvalid)
	}
	if _, err := planningport.ParseExpectedVersion(input.ExpectedVersion); err != nil {
		return mutationArguments{}, fail(CodeInputInvalid)
	}
	return input, nil
}

func (session *Session) scopeIntent(ctx context.Context, project domain.Project, input mutationArguments) (planningport.MutationIntent, string, uint64, error) {
	intent := planningport.MutationIntent{Type: input.Type, Name: input.Name, Key: input.Key, Title: input.Title,
		Description: input.Description, Objective: input.Objective, AcceptanceCriteria: slices.Clone(input.AcceptanceCriteria),
		WorkspaceID: input.WorkspaceID, EpicID: input.EpicID, TaskID: input.TaskID, Priority: input.Priority,
		Labels: slices.Clone(input.Labels), DependencyKind: input.DependencyKind, DependencyID: input.DependencyID}
	switch input.Type {
	case "project.update", "epic.create", "task.create":
		intent.ProjectID = project.ID
	}
	hash, _ := planningport.SchemaSHA256()
	requestID := namespaced(session.binding, input.RequestID)
	if planningport.ValidateMutation(planningport.MutationInput{SchemaVersion: 1, ContractVersion: "director-planning/v1",
		ContractHash: hash, RequestID: requestID, IdempotencyKey: requestID, ExpectedVersion: input.ExpectedVersion,
		HumanApprovalRef: nil, AcknowledgementRevision: nil, Intent: intent}) != nil {
		return intent, "", 0, fail(CodeInputInvalid)
	}
	target, currentVersion := project.ID, project.Version
	switch input.Type {
	case "project.update", "epic.create":
	case "epic.update":
		epic, err := session.service.store.Epic(ctx, input.EpicID)
		if err != nil || epic.ProjectID != project.ID {
			return intent, "", 0, fail(CodeScopeMismatch)
		}
		intent.EpicID, target, currentVersion = epic.ID, epic.ID, epic.Version
	case "task.create":
		workspace, err := session.service.store.Workspace(ctx, input.WorkspaceID)
		if err != nil || workspace.ProjectID != project.ID {
			return intent, "", 0, fail(CodeScopeMismatch)
		}
		intent.WorkspaceID, intent.EpicID = workspace.ID, input.EpicID
		if input.EpicID != "" {
			epic, loadErr := session.service.store.Epic(ctx, input.EpicID)
			if loadErr != nil || epic.ProjectID != project.ID {
				return intent, "", 0, fail(CodeScopeMismatch)
			}
		}
	case "task.update", "dependency.add", "dependency.remove", "task.launch-now":
		task, err := session.service.store.Task(ctx, input.TaskID)
		if err != nil || task.ProjectID != project.ID {
			return intent, "", 0, fail(CodeScopeMismatch)
		}
		intent.TaskID, intent.EpicID, target, currentVersion = task.ID, input.EpicID, task.ID, task.Version
		if input.EpicID != "" {
			epic, loadErr := session.service.store.Epic(ctx, input.EpicID)
			if loadErr != nil || epic.ProjectID != project.ID {
				return intent, "", 0, fail(CodeScopeMismatch)
			}
		}
		if input.DependencyID != "" {
			if input.DependencyKind == "task" {
				dependency, loadErr := session.service.store.Task(ctx, input.DependencyID)
				if loadErr != nil || dependency.ProjectID != project.ID {
					return intent, "", 0, fail(CodeScopeMismatch)
				}
			} else {
				dependency, loadErr := session.service.store.Epic(ctx, input.DependencyID)
				if loadErr != nil || dependency.ProjectID != project.ID {
					return intent, "", 0, fail(CodeScopeMismatch)
				}
			}
		}
	}
	return intent, target, currentVersion, nil
}

func actionMatches(action planningport.AllowedAction, kind, target, version string) bool {
	return action.Kind == kind && action.TargetID != nil && *action.TargetID == target && action.ExpectedVersion == version &&
		action.HumanApprovalRef == nil && action.AcknowledgementRevision == nil
}

func (session *Session) allowed(ctx context.Context, projectID, kind, target, version string) bool {
	var cursor *string
	for page := 0; page < 100; page++ {
		snapshot, err := session.service.reader.Query(ctx, planningInput(projectID, cursor, planningport.MaximumPageSize))
		if err != nil {
			return false
		}
		for _, project := range snapshot.Page.Projects {
			for _, action := range project.AllowedActions {
				if actionMatches(action, kind, target, version) {
					return true
				}
			}
		}
		for _, epic := range snapshot.Page.Epics {
			for _, action := range epic.AllowedActions {
				if actionMatches(action, kind, target, version) {
					return true
				}
			}
		}
		for _, task := range snapshot.Page.Tasks {
			for _, action := range task.AllowedActions {
				if actionMatches(action, kind, target, version) || kind == "dependency.remove" && actionMatches(action, "dependency.add", target, version) {
					return true
				}
			}
		}
		if snapshot.Page.NextCursor == nil {
			return false
		}
		cursor = snapshot.Page.NextCursor
	}
	return false
}

func namespaced(binding contract.SessionBinding, id string) string {
	return "admin-" + digest(binding.ProjectID, binding.SessionID, id)[:32]
}

func (session *Session) replayReceipt(project domain.Project, input mutationArguments, toolName string, raw json.RawMessage) (json.RawMessage, bool, error) {
	key, payloadHash := namespaced(session.binding, input.IdempotencyKey), digest(string(mustCanonical(raw)))
	for _, receipt := range project.ProjectAdmin.Receipts {
		if receipt.Key != key {
			continue
		}
		if receipt.SessionID != session.binding.SessionID || receipt.ToolName != toolName ||
			receipt.RequestID != input.RequestID || receipt.PayloadSHA256 != payloadHash {
			return nil, true, fail(CodeIdempotencyConflict)
		}
		var output map[string]any
		if json.Unmarshal(receipt.Output, &output) != nil {
			return nil, true, fail(CodeUnavailable)
		}
		output["replay"] = true
		encoded, err := boundedOutput(output)
		return encoded, true, err
	}
	return nil, false, nil
}

func (session *Session) persistReceipt(ctx context.Context, input mutationArguments, toolName string, raw json.RawMessage,
	outputForProjectVersion func(uint64) (json.RawMessage, error)) (json.RawMessage, error) {
	key := namespaced(session.binding, input.IdempotencyKey)
	for attempt := 0; attempt < 8; attempt++ {
		project, err := session.current(ctx)
		if err != nil {
			return nil, err
		}
		if replay, found, replayErr := session.replayReceipt(project, input, toolName, raw); found {
			return replay, replayErr
		}
		if len(project.ProjectAdmin.Receipts) >= contract.MaximumReceipts {
			return nil, fail(CodeCommandLimit)
		}
		output, outputErr := outputForProjectVersion(project.Version + 1)
		if outputErr != nil {
			return nil, outputErr
		}
		receipt := contract.Receipt{Key: key, SessionID: session.binding.SessionID, ToolName: toolName, RequestID: input.RequestID,
			PayloadSHA256: digest(string(mustCanonical(raw))), Output: slices.Clone(output), RecordedAtMillis: session.service.now()}
		next := project
		next.ProjectAdmin = contract.Clone(project.ProjectAdmin)
		next.ProjectAdmin.Receipts = append(next.ProjectAdmin.Receipts, receipt)
		next.Version++
		payload, _ := canonical(receipt)
		writeKey := commandKey("project-admin-receipt", key, strconv.FormatUint(project.Version, 10))
		result, writeErr := session.service.store.UpdateProject(ctx, domain.CommandRequest{IdempotencyKey: writeKey,
			Type: "project.admin_mcp.receipt_recorded", AggregateID: project.ID, ExpectedVersion: project.Version, Payload: payload},
			next, projectEvent(writeKey, project.ID, "project.admin_mcp.receipt_recorded", next.Version, payload))
		if writeErr == nil && result.Outcome == domain.CommandApplied {
			current, readErr := session.current(ctx)
			if readErr != nil {
				return nil, readErr
			}
			for _, persisted := range current.ProjectAdmin.Receipts {
				if persisted.Key == receipt.Key && persisted.SessionID == receipt.SessionID && persisted.ToolName == receipt.ToolName &&
					persisted.RequestID == receipt.RequestID && persisted.PayloadSHA256 == receipt.PayloadSHA256 &&
					bytes.Equal(persisted.Output, receipt.Output) {
					return slices.Clone(persisted.Output), nil
				}
			}
			return nil, fail(CodeUnavailable)
		}
		if writeErr != nil && !errors.Is(writeErr, storeport.ErrIdempotencyConflict) {
			return nil, fail(CodeUnavailable)
		}
	}
	return nil, fail(CodeExpectedVersionConflict)
}

func commandOutput(input mutationArguments, status, message string, updated *uint64, projectVersion uint64) (json.RawMessage, error) {
	version := ""
	if updated != nil {
		version = strconv.FormatUint(*updated, 10)
	}
	return boundedOutput(struct {
		SchemaVersion  string `json:"schemaVersion"`
		RequestID      string `json:"requestId"`
		Status         string `json:"status"`
		Message        string `json:"message"`
		UpdatedVersion string `json:"updatedVersion"`
		ProjectVersion string `json:"projectVersion"`
		Replay         bool   `json:"replay"`
	}{contract.ContractVersion, input.RequestID, status, message, version, strconv.FormatUint(projectVersion, 10), false})
}

func (session *Session) planningCommand(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	input, err := decodeMutation(raw)
	if err != nil {
		return nil, err
	}
	project, err := session.current(ctx)
	if err != nil {
		return nil, err
	}
	if replay, found, replayErr := session.replayReceipt(project, input, "director_admin_planning_command", raw); found {
		return replay, replayErr
	}
	intent, target, currentVersion, err := session.scopeIntent(ctx, project, input)
	if err != nil {
		return nil, err
	}
	expected, _ := planningport.ParseExpectedVersion(input.ExpectedVersion)
	if currentVersion != expected {
		return nil, fail(CodeExpectedVersionConflict)
	}
	if !session.allowed(ctx, project.ID, input.Type, target, input.ExpectedVersion) {
		return nil, fail(CodeActionNotAllowed)
	}
	hash, _ := planningport.SchemaSHA256()
	request := namespaced(session.binding, input.RequestID)
	mutation := planningport.MutationInput{SchemaVersion: 1, ContractVersion: "director-planning/v1", ContractHash: hash,
		RequestID: request, IdempotencyKey: request, ExpectedVersion: input.ExpectedVersion,
		HumanApprovalRef: nil, AcknowledgementRevision: nil, Intent: intent}
	result, mutateErr := session.service.planner.Mutate(ctx, mutation, planningport.Actor{Kind: "agent", ID: session.binding.NativeAgentID, SessionID: session.binding.SessionID})
	if mutateErr != nil {
		return nil, fail(CodeUnavailable)
	}
	if result.Status != "accepted" {
		return nil, fail(CodeExpectedVersionConflict)
	}
	return session.persistReceipt(ctx, input, "director_admin_planning_command", raw, func(projectVersion uint64) (json.RawMessage, error) {
		return commandOutput(input, result.Status, result.Message, result.UpdatedVersion, projectVersion)
	})
}

type controlArguments struct {
	RequestID       string `json:"requestId"`
	IdempotencyKey  string `json:"idempotencyKey"`
	ExpectedVersion string `json:"expectedVersion"`
	Type            string `json:"type"`
	TaskID          string `json:"taskId"`
	RunID           string `json:"runId"`
}

func decodeControl(raw json.RawMessage) (controlArguments, error) {
	if err := preflightRaw(raw); err != nil {
		return controlArguments{}, err
	}
	var kind struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &kind) != nil {
		return controlArguments{}, fail(CodeInputInvalid)
	}
	fields := []string{"requestId", "idempotencyKey", "expectedVersion", "type"}
	switch kind.Type {
	case "project.pause", "project.resume":
	case "task.cancel":
		fields = append(fields, "taskId", "runId")
	case "recovery.reconcile":
		fields = append(fields, "runId")
	default:
		return controlArguments{}, fail(CodeInputInvalid)
	}
	var input controlArguments
	if err := strict(raw, &input, fields); err != nil {
		return controlArguments{}, err
	}
	if !boundedIdentity(input.RequestID) || input.RequestID != input.IdempotencyKey {
		return controlArguments{}, fail(CodeInputInvalid)
	}
	if _, err := planningport.ParseExpectedVersion(input.ExpectedVersion); err != nil {
		return controlArguments{}, fail(CodeInputInvalid)
	}
	if kind.Type == "task.cancel" && (!boundedIdentity(input.TaskID) || !boundedIdentity(input.RunID)) ||
		kind.Type == "recovery.reconcile" && !boundedIdentity(input.RunID) {
		return controlArguments{}, fail(CodeInputInvalid)
	}
	return input, nil
}

func (session *Session) controlCommand(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	control, err := decodeControl(raw)
	if err != nil {
		return nil, err
	}
	input := mutationArguments{RequestID: control.RequestID, IdempotencyKey: control.IdempotencyKey, ExpectedVersion: control.ExpectedVersion, Type: control.Type}
	project, err := session.current(ctx)
	if err != nil {
		return nil, err
	}
	if replay, found, replayErr := session.replayReceipt(project, input, "director_admin_control_command", raw); found {
		return replay, replayErr
	}
	expected, _ := planningport.ParseExpectedVersion(control.ExpectedVersion)
	requestID := namespaced(session.binding, control.RequestID)
	actor := executionport.AuthenticatedControlActor{Kind: domainexecution.ControlActorAgent, ID: session.binding.NativeAgentID,
		SessionID: session.binding.SessionID, Source: "server", Authenticated: true}
	var updated uint64
	message := "Control request was durably admitted"
	switch control.Type {
	case "project.pause", "project.resume":
		if project.Version != expected {
			return nil, fail(CodeExpectedVersionConflict)
		}
		if !session.allowed(ctx, project.ID, control.Type, project.ID, control.ExpectedVersion) {
			return nil, fail(CodeActionNotAllowed)
		}
		kind := domainexecution.ControlPauseProject
		if control.Type == "project.resume" {
			kind = domainexecution.ControlResumeProject
		}
		result, controlErr := session.service.controller.RequestProjectControl(ctx, executionport.ControlCommand{Kind: kind, RequestID: requestID,
			ProjectID: project.ID, ExpectedProjectVersion: expected, Lease: leaseBinding(project), Actor: actor, NowMillis: session.service.now()})
		if controlErr != nil {
			return nil, fail(CodeActionNotAllowed)
		}
		updated = result.Project.Version
	case "task.cancel":
		task, loadErr := session.service.store.Task(ctx, control.TaskID)
		if loadErr != nil || task.ProjectID != project.ID {
			return nil, fail(CodeScopeMismatch)
		}
		run, loadErr := session.service.store.Run(ctx, control.RunID)
		if loadErr != nil || run.TaskID != task.ID {
			return nil, fail(CodeScopeMismatch)
		}
		if run.Version != expected {
			return nil, fail(CodeExpectedVersionConflict)
		}
		if !session.allowed(ctx, project.ID, control.Type, run.ID, control.ExpectedVersion) {
			return nil, fail(CodeActionNotAllowed)
		}
		result, controlErr := session.service.controller.CancelTask(ctx, executionport.ControlCommand{RequestID: requestID, ProjectID: project.ID,
			TaskID: task.ID, RunID: run.ID, ExpectedProjectVersion: project.Version, ExpectedTaskVersion: task.Version,
			ExpectedRunVersion: expected, Lease: leaseBinding(project), Actor: actor, NowMillis: session.service.now()})
		if controlErr != nil || result.Run == nil {
			return nil, fail(CodeActionNotAllowed)
		}
		updated = result.Run.Version
	case "recovery.reconcile":
		if session.service.reconciler == nil {
			return nil, fail(CodeActionNotAllowed)
		}
		run, loadErr := session.service.store.Run(ctx, control.RunID)
		if loadErr != nil {
			return nil, fail(CodeScopeMismatch)
		}
		task, loadErr := session.service.store.Task(ctx, run.TaskID)
		if loadErr != nil || task.ProjectID != project.ID {
			return nil, fail(CodeScopeMismatch)
		}
		if run.Version != expected {
			return nil, fail(CodeExpectedVersionConflict)
		}
		if _, reconcileErr := session.service.reconciler.ReconcileRun(ctx, run.ID); reconcileErr != nil {
			return nil, fail(CodeActionNotAllowed)
		}
		current, loadErr := session.service.store.Run(ctx, run.ID)
		if loadErr != nil {
			return nil, fail(CodeUnavailable)
		}
		updated, message = current.Version, "Recovery reconciliation completed one bounded Engine step"
	}
	return session.persistReceipt(ctx, input, "director_admin_control_command", raw, func(projectVersion uint64) (json.RawMessage, error) {
		return commandOutput(input, "accepted", message, &updated, projectVersion)
	})
}

func leaseBinding(project domain.Project) domainexecution.LeaseBinding {
	if project.Lease == nil {
		return domainexecution.LeaseBinding{}
	}
	return domainexecution.LeaseBinding{HolderInstance: project.Lease.HolderInstance,
		HolderProcessIdentity: project.Lease.HolderProcessIdentity, Epoch: project.Lease.Epoch}
}

func (session *Session) diagnostics(ctx context.Context) (json.RawMessage, error) {
	project, err := session.current(ctx)
	if err != nil {
		return nil, err
	}
	cursor, err := session.service.store.LatestEventSequence(ctx)
	if err != nil {
		return nil, fail(CodeUnavailable)
	}
	active, revoked := 0, 0
	for _, record := range project.ProjectAdmin.Sessions {
		if record.State == contract.SessionActive {
			active++
		} else {
			revoked++
		}
	}
	return boundedOutput(struct {
		SchemaVersion       string `json:"schemaVersion"`
		ProjectID           string `json:"projectId"`
		ProjectVersion      string `json:"projectVersion"`
		Cursor              string `json:"cursor"`
		State               string `json:"state"`
		ActiveSessions      int    `json:"activeSessions"`
		RevokedSessions     int    `json:"revokedSessions"`
		DurableReceipts     int    `json:"durableReceipts"`
		RawOutputIncluded   bool   `json:"rawOutputIncluded"`
		PathsIncluded       bool   `json:"pathsIncluded"`
		CredentialsIncluded bool   `json:"credentialsIncluded"`
	}{contract.ContractVersion, project.ID, strconv.FormatUint(project.Version, 10), strconv.FormatUint(cursor, 10), project.State,
		active, revoked, len(project.ProjectAdmin.Receipts), false, false, false})
}

func (session *Session) Call(ctx context.Context, requestID, toolName string, raw json.RawMessage) (Result, error) {
	if !boundedIdentity(requestID) {
		return Result{}, fail(CodeInputInvalid)
	}
	if _, exists := session.tools[toolName]; !exists {
		return Result{}, fail(CodeToolNotAllowed)
	}
	var payload json.RawMessage
	var err error
	switch toolName {
	case "director_admin_project_read":
		var empty struct{}
		if err = strict(raw, &empty, []string{}); err == nil {
			payload, err = session.projectRead(ctx)
		}
	case "director_admin_planning_read":
		payload, err = session.planningRead(ctx, raw)
	case "director_admin_execution_read":
		payload, err = session.executionRead(ctx, raw)
	case "director_admin_planning_command":
		payload, err = session.planningCommand(ctx, raw)
	case "director_admin_control_command":
		payload, err = session.controlCommand(ctx, raw)
	case "director_admin_diagnostics_read":
		var empty struct{}
		if err = strict(raw, &empty, []string{}); err == nil {
			payload, err = session.diagnostics(ctx)
		}
	default:
		err = fail(CodeToolNotAllowed)
	}
	return Result{Payload: payload}, err
}

func ErrorCode(err error) string {
	var failure *Failure
	if errors.As(err, &failure) {
		return string(failure.Code)
	}
	return string(CodeUnavailable)
}
