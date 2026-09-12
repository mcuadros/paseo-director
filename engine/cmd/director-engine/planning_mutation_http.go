// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/mcuadros/director-engine/application/execution"
	planningapp "github.com/mcuadros/director-engine/application/planning"
	"github.com/mcuadros/director-engine/domain"
	domainexecution "github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/domain/jsondocument"
	planningport "github.com/mcuadros/director-engine/ports/planning"
	storeport "github.com/mcuadros/director-engine/ports/taskstore"
)

type planningControlStore interface {
	planningapp.Store
	Project(context.Context, string) (domain.Project, error)
	Task(context.Context, string) (domain.Task, error)
	Run(context.Context, string) (domain.Run, error)
	LatestEventSequence(context.Context) (uint64, error)
}

type planningMutationHandler struct {
	store      planningControlStore
	controller *execution.Controller
	planning   *planningapp.Service
	delivery   interface {
		IntegrateTask(context.Context, ManualIntegrationRequest) (domain.Run, error)
	}
	feedback interface {
		SubmitFeedback(context.Context, HumanFeedbackRequest) (domain.Run, error)
	}
	contractVersion string
	contractHash    string
	now             func() int64
}

func newPlanningMutationHandler(store planningControlStore, controller *execution.Controller, extensions ...any) http.Handler {
	definition, err := planningport.EmbeddedDefinition()
	if err != nil {
		panic(err)
	}
	hash, err := planningport.SchemaSHA256()
	if err != nil {
		panic(err)
	}
	var launcher planningapp.Launcher
	var delivery interface {
		IntegrateTask(context.Context, ManualIntegrationRequest) (domain.Run, error)
	}
	var feedback interface {
		SubmitFeedback(context.Context, HumanFeedbackRequest) (domain.Run, error)
	}
	for _, extension := range extensions {
		if value, ok := extension.(planningapp.Launcher); ok {
			launcher = value
		}
		if value, ok := extension.(interface {
			IntegrateTask(context.Context, ManualIntegrationRequest) (domain.Run, error)
		}); ok {
			delivery = value
		}
		if value, ok := extension.(interface {
			SubmitFeedback(context.Context, HumanFeedbackRequest) (domain.Run, error)
		}); ok {
			feedback = value
		}
	}
	var planningService *planningapp.Service
	if store != nil {
		planningService, err = planningapp.NewService(store, launcher, nil)
		if err != nil {
			panic(err)
		}
	}
	return &planningMutationHandler{
		store: store, controller: controller, contractVersion: definition.ContractVersion, contractHash: hash,
		planning: planningService,
		delivery: delivery,
		feedback: feedback,
		now:      func() int64 { return time.Now().UnixMilli() },
	}
}

func decodePlanningMutation(request *http.Request) (planningport.MutationInput, error) {
	if request.ContentLength > planningport.MaximumRequestBytes {
		return planningport.MutationInput{}, planningport.ErrQueryInvalid
	}
	content, err := io.ReadAll(io.LimitReader(request.Body, planningport.MaximumRequestBytes+1))
	if err != nil || len(content) == 0 || len(content) > planningport.MaximumRequestBytes {
		return planningport.MutationInput{}, planningport.ErrQueryInvalid
	}
	content, err = jsondocument.CanonicalWithNormalizedNumbersLimit(content, planningport.MaximumRequestBytes)
	if err != nil {
		return planningport.MutationInput{}, planningport.ErrQueryInvalid
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(content, &envelope) != nil || !exactPlanningFields(envelope, []string{
		"schemaVersion", "contractVersion", "contractHash", "requestId", "idempotencyKey", "expectedVersion",
		"humanApprovalRef", "acknowledgementRevision", "intent",
	}) {
		return planningport.MutationInput{}, planningport.ErrQueryInvalid
	}
	var intentFields map[string]json.RawMessage
	var intentType struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(envelope["intent"], &intentFields) != nil || json.Unmarshal(envelope["intent"], &intentType) != nil ||
		!exactPlanningFields(intentFields, planningIntentFields(intentType.Type)) {
		return planningport.MutationInput{}, planningport.ErrQueryInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var input planningport.MutationInput
	if err := decoder.Decode(&input); err != nil {
		return planningport.MutationInput{}, planningport.ErrQueryInvalid
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return planningport.MutationInput{}, planningport.ErrQueryInvalid
	}
	return input, planningport.ValidateMutation(input)
}

func exactPlanningFields(value map[string]json.RawMessage, expected []string) bool {
	if len(value) != len(expected) {
		return false
	}
	for _, field := range expected {
		if _, ok := value[field]; !ok {
			return false
		}
	}
	return true
}

func planningIntentFields(kind string) []string {
	switch kind {
	case "project.create":
		return []string{"type", "name"}
	case "project.update":
		return []string{"type", "projectId", "name"}
	case "epic.create":
		return []string{"type", "projectId", "key", "title", "description", "priority", "labels"}
	case "epic.update":
		return []string{"type", "epicId", "title", "description", "priority", "labels"}
	case "task.create":
		return []string{"type", "projectId", "workspaceId", "epicId", "key", "title", "objective", "acceptanceCriteria", "priority", "labels"}
	case "task.update":
		return []string{"type", "taskId", "epicId", "title", "objective", "acceptanceCriteria", "priority", "labels"}
	case "dependency.add", "dependency.remove", "dependency.override":
		return []string{"type", "taskId", "dependencyKind", "dependencyId"}
	case "configuration.preview":
		return []string{"type", "target", "overrides"}
	case "configuration.apply":
		return []string{"type", "target", "previewId"}
	case "task.launch-now":
		return []string{"type", "taskId"}
	case "project.pause", "project.resume", "project.emergency-stop.prepare", "project.emergency-stop.confirm":
		return []string{"type", "projectId"}
	case "task.cancel", "task.integrate":
		return []string{"type", "projectId", "taskId", "runId"}
	case "task.feedback":
		return []string{"type", "projectId", "taskId", "runId", "body", "severity"}
	default:
		return nil
	}
}

func (handler *planningMutationHandler) result(ctx context.Context, input planningport.MutationInput, status, message string, version *uint64, confirmation string) (planningport.MutationResult, error) {
	cursor, err := handler.store.LatestEventSequence(ctx)
	if err != nil {
		return planningport.MutationResult{}, err
	}
	var versionText *string
	if version != nil {
		value := strconv.FormatUint(*version, 10)
		versionText = &value
	}
	var confirmationRef *string
	if confirmation != "" {
		value := confirmation
		confirmationRef = &value
	}
	return planningport.MutationResult{
		SchemaVersion: 1, ContractVersion: handler.contractVersion, ContractHash: handler.contractHash,
		Cursor: strconv.FormatUint(cursor, 10), RequestID: input.RequestID, Status: status, Message: message,
		UpdatedVersion: versionText, Preview: nil, ConfirmationRef: confirmationRef,
	}, nil
}

func (handler *planningMutationHandler) mutate(
	ctx context.Context, input planningport.MutationInput, actor execution.AuthenticatedControlActor,
) (planningport.MutationResult, error) {
	expected, _ := planningport.ParseExpectedVersion(input.ExpectedVersion)
	var result execution.ControlResult
	var err error
	switch input.Intent.Type {
	case "project.pause":
		project, loadErr := handler.store.Project(ctx, input.Intent.ProjectID)
		if loadErr != nil {
			return planningport.MutationResult{}, loadErr
		}
		command := handler.controlCommand(input, actor, project, expected)
		command.Kind = domainexecution.ControlPauseProject
		result, err = handler.controller.RequestProjectControl(ctx, command)
	case "project.resume":
		project, loadErr := handler.store.Project(ctx, input.Intent.ProjectID)
		if loadErr != nil {
			return planningport.MutationResult{}, loadErr
		}
		command := handler.controlCommand(input, actor, project, expected)
		command.Kind = domainexecution.ControlResumeProject
		result, err = handler.controller.RequestProjectControl(ctx, command)
	case "project.emergency-stop.prepare":
		project, loadErr := handler.store.Project(ctx, input.Intent.ProjectID)
		if loadErr != nil {
			return planningport.MutationResult{}, loadErr
		}
		command := handler.controlCommand(input, actor, project, expected)
		result, err = handler.controller.PrepareEmergencyStop(ctx, command)
	case "project.emergency-stop.confirm":
		project, loadErr := handler.store.Project(ctx, input.Intent.ProjectID)
		if loadErr != nil {
			return planningport.MutationResult{}, loadErr
		}
		command := handler.controlCommand(input, actor, project, expected)
		command.ConfirmationID = *input.HumanApprovalRef
		result, err = handler.controller.ConfirmEmergencyStop(ctx, command)
	case "task.cancel":
		project, loadErr := handler.store.Project(ctx, input.Intent.ProjectID)
		if loadErr != nil {
			return planningport.MutationResult{}, loadErr
		}
		command := handler.controlCommand(input, actor, project, expected)
		task, taskErr := handler.store.Task(ctx, input.Intent.TaskID)
		if taskErr != nil {
			return planningport.MutationResult{}, taskErr
		}
		run, runErr := handler.store.Run(ctx, input.Intent.RunID)
		if runErr != nil {
			return planningport.MutationResult{}, runErr
		}
		command.TaskID, command.RunID = task.ID, run.ID
		command.ExpectedProjectVersion = project.Version
		command.ExpectedTaskVersion = task.Version
		command.ExpectedRunVersion = expected
		result, err = handler.controller.CancelTask(ctx, command)
	case "task.integrate":
		if handler.delivery == nil {
			return handler.result(ctx, input, "rejected", "Manual integration is unavailable until exact delivery gates are ready", nil, "")
		}
		run, integrateErr := handler.delivery.IntegrateTask(ctx, ManualIntegrationRequest{RequestID: input.RequestID,
			ProjectID: input.Intent.ProjectID, TaskID: input.Intent.TaskID, RunID: input.Intent.RunID,
			ExpectedRunVersion: expected, ActorID: actor.ID, ActorSessionID: actor.SessionID, NowMillis: handler.now()})
		if integrateErr != nil {
			return handler.result(ctx, input, "rejected", "Manual integration was refused by current exact Candidate, Review, CI, feedback, and base facts", nil, "")
		}
		return handler.result(ctx, input, "accepted", "Authenticated manual integration was durably authorized", &run.Version, "")
	case "task.feedback":
		if handler.feedback == nil {
			return handler.result(ctx, input, "rejected", "Human feedback is unavailable until an exact current Candidate is ready", nil, "")
		}
		run, feedbackErr := handler.feedback.SubmitFeedback(ctx, HumanFeedbackRequest{RequestID: input.RequestID,
			ProjectID: input.Intent.ProjectID, TaskID: input.Intent.TaskID, RunID: input.Intent.RunID,
			ExpectedRunVersion: expected, ActorID: actor.ID, ActorSessionID: actor.SessionID,
			Body: input.Intent.Body, Severity: input.Intent.Severity, NowMillis: handler.now()})
		if feedbackErr != nil {
			return handler.result(ctx, input, "rejected", "Human feedback was refused by current exact Candidate and correction facts", nil, "")
		}
		return handler.result(ctx, input, "accepted", "Authenticated human feedback was durably recorded", &run.Version, "")
	default:
		if handler.planning == nil {
			return planningport.MutationResult{}, errors.New("planning application service is unavailable")
		}
		planned, planningErr := handler.planning.Mutate(ctx, input, planningapp.Actor{ID: actor.ID, SessionID: actor.SessionID})
		if planningErr != nil {
			if errors.Is(planningErr, storeport.ErrNotFound) || errors.Is(planningErr, storeport.ErrInvalidRecord) ||
				errors.Is(planningErr, planningapp.ErrVersionConflict) {
				return handler.result(ctx, input, "rejected", "Planning intent was refused by current authoritative TaskStore facts", nil, "")
			}
			return planningport.MutationResult{}, planningErr
		}
		response, resultErr := handler.result(ctx, input, planned.Status, planned.Message, planned.UpdatedVersion, planned.ConfirmationRef)
		response.Preview = planned.Preview
		return response, resultErr
	}
	if err != nil {
		return handler.result(ctx, input, "rejected", "Execution control was rejected by current authoritative facts", nil, "")
	}
	version := result.Project.Version
	if result.Run != nil {
		version = result.Run.Version
	}
	return handler.result(ctx, input, "accepted", "Execution control intent was durably recorded", &version, result.ConfirmationID)
}

func (handler *planningMutationHandler) controlCommand(input planningport.MutationInput, actor execution.AuthenticatedControlActor, project domain.Project, expected uint64) execution.ControlCommand {
	return execution.ControlCommand{
		RequestID: input.RequestID, ProjectID: project.ID, ExpectedProjectVersion: expected,
		Lease: func() domainexecution.LeaseBinding {
			if project.Lease == nil {
				return domainexecution.LeaseBinding{}
			}
			return domainexecution.LeaseBinding{HolderInstance: project.Lease.HolderInstance,
				HolderProcessIdentity: project.Lease.HolderProcessIdentity, Epoch: project.Lease.Epoch}
		}(),
		Actor: execution.AuthenticatedControlActor{Kind: domainexecution.ControlActorHuman, ID: actor.ID,
			SessionID: actor.SessionID, Source: "server", Authenticated: true},
		NowMillis: handler.now(),
	}
}

func planningMutationActor(request *http.Request) (execution.AuthenticatedControlActor, bool) {
	definition, err := planningport.EmbeddedDefinition()
	if err != nil {
		return execution.AuthenticatedControlActor{}, false
	}
	headers := definition.MutationActorHeaders
	if len(request.Header.Values(headers.Kind)) != 1 || len(request.Header.Values(headers.ID)) != 1 ||
		len(request.Header.Values(headers.Session)) != 1 || !planningport.ValidMutationActor(
		request.Header.Get(headers.Kind), request.Header.Get(headers.ID), request.Header.Get(headers.Session),
	) {
		return execution.AuthenticatedControlActor{}, false
	}
	actor := execution.AuthenticatedControlActor{
		Kind: domainexecution.ControlActorHuman, ID: request.Header.Get(headers.ID),
		SessionID: request.Header.Get(headers.Session), Source: "server", Authenticated: true,
	}
	return actor, true
}

func (handler *planningMutationHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != planningport.MutationPath || request.URL.RawQuery != "" {
		http.NotFound(response, request)
		return
	}
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writePlanningError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
		return
	}
	if request.Header.Get("Content-Type") != "application/json" ||
		request.Header.Get(contractVersionHeader) != handler.contractVersion ||
		request.Header.Get(contractHashHeader) != handler.contractHash {
		writePlanningError(response, http.StatusConflict, "CONTRACT_MISMATCH")
		return
	}
	input, err := decodePlanningMutation(request)
	if err != nil {
		writePlanningError(response, http.StatusBadRequest, "PLANNING_INPUT_INVALID")
		return
	}
	actor, authenticated := planningMutationActor(request)
	if !authenticated {
		writePlanningError(response, http.StatusUnauthorized, "PLANNING_HUMAN_AUTH_REQUIRED")
		return
	}
	result, err := handler.mutate(request.Context(), input, actor)
	if err != nil {
		writePlanningError(response, http.StatusServiceUnavailable, "PLANNING_UNAVAILABLE")
		return
	}
	writePlanningJSON(response, handler.contractVersion, handler.contractHash, result, true)
}
