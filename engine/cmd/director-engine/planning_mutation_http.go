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
	"github.com/mcuadros/director-engine/domain"
	domainexecution "github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/domain/jsondocument"
	planningport "github.com/mcuadros/director-engine/ports/planning"
)

type planningControlStore interface {
	Project(context.Context, string) (domain.Project, error)
	Task(context.Context, string) (domain.Task, error)
	Run(context.Context, string) (domain.Run, error)
	LatestEventSequence(context.Context) (uint64, error)
}

type planningMutationHandler struct {
	store           planningControlStore
	controller      *execution.Controller
	contractVersion string
	contractHash    string
	now             func() int64
}

func newPlanningMutationHandler(store planningControlStore, controller *execution.Controller) http.Handler {
	definition, err := planningport.EmbeddedDefinition()
	if err != nil {
		panic(err)
	}
	hash, err := planningport.SchemaSHA256()
	if err != nil {
		panic(err)
	}
	return &planningMutationHandler{
		store: store, controller: controller, contractVersion: definition.ContractVersion, contractHash: hash,
		now: func() int64 { return time.Now().UnixMilli() },
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
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var input planningport.MutationInput
	if err := decoder.Decode(&input); err != nil {
		return planningport.MutationInput{}, planningport.ErrQueryInvalid
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return planningport.MutationInput{}, planningport.ErrQueryInvalid
	}
	return input, planningport.ValidateControlMutation(input)
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
	project, err := handler.store.Project(ctx, input.Intent.ProjectID)
	if err != nil {
		return planningport.MutationResult{}, err
	}
	command := execution.ControlCommand{
		RequestID: input.RequestID, ProjectID: project.ID, ExpectedProjectVersion: expected,
		Lease: func() domainexecution.LeaseBinding {
			if project.Lease == nil {
				return domainexecution.LeaseBinding{}
			}
			return domainexecution.LeaseBinding{
				HolderInstance: project.Lease.HolderInstance, HolderProcessIdentity: project.Lease.HolderProcessIdentity,
				Epoch: project.Lease.Epoch,
			}
		}(),
		Actor: execution.AuthenticatedControlActor{
			Kind: domainexecution.ControlActorHuman, ID: actor.ID,
			SessionID: actor.SessionID, Source: "server", Authenticated: true,
		},
		NowMillis: handler.now(),
	}
	var result execution.ControlResult
	switch input.Intent.Type {
	case "project.pause":
		command.Kind = domainexecution.ControlPauseProject
		result, err = handler.controller.RequestProjectControl(ctx, command)
	case "project.resume":
		command.Kind = domainexecution.ControlResumeProject
		result, err = handler.controller.RequestProjectControl(ctx, command)
	case "project.emergency-stop.prepare":
		result, err = handler.controller.PrepareEmergencyStop(ctx, command)
	case "project.emergency-stop.confirm":
		command.ConfirmationID = *input.HumanApprovalRef
		result, err = handler.controller.ConfirmEmergencyStop(ctx, command)
	case "task.cancel":
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
