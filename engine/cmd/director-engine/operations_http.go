// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"net/http"

	homeapp "github.com/mcuadros/director-engine/application/home"
	planningport "github.com/mcuadros/director-engine/ports/planning"
)

type operationsReader interface {
	Operations(context.Context, planningport.OperationsQueryInput) (planningport.OperationsReport, error)
}

type operationsMutator interface {
	Mutate(context.Context, planningport.OperationsMutationInput, homeapp.AuthenticatedOperationsActor) (planningport.OperationsMutationResult, error)
}

type operationsHandler struct {
	reader          operationsReader
	contractVersion string
	contractHash    string
}

type operationsMutationHandler struct {
	service         operationsMutator
	contractVersion string
	contractHash    string
}

func newOperationsHandler(reader operationsReader) http.Handler {
	version, hash := planningContractIdentity()
	return &operationsHandler{reader: reader, contractVersion: version, contractHash: hash}
}

func newOperationsMutationHandler(service operationsMutator) http.Handler {
	version, hash := planningContractIdentity()
	return &operationsMutationHandler{service: service, contractVersion: version, contractHash: hash}
}

func operationsFailure(response http.ResponseWriter, err error, inputCode string) {
	switch {
	case errors.Is(err, homeapp.ErrHostMismatch):
		writePlanningError(response, http.StatusConflict, "OPERATIONS_HOST_MISMATCH")
	case errors.Is(err, homeapp.ErrOperationsSnapshot):
		writePlanningError(response, http.StatusConflict, "OPERATIONS_FACTS_STALE")
	case errors.Is(err, planningport.ErrQueryInvalid):
		writePlanningError(response, http.StatusBadRequest, inputCode)
	default:
		writePlanningError(response, http.StatusServiceUnavailable, "OPERATIONS_UNAVAILABLE")
	}
}

func (handler *operationsHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != planningport.OperationsQueryPath || request.URL.RawQuery != "" {
		http.NotFound(response, request)
		return
	}
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writePlanningError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
		return
	}
	if !validPlanningRequest(request, handler.contractVersion, handler.contractHash) {
		writePlanningError(response, http.StatusConflict, "CONTRACT_MISMATCH")
		return
	}
	var input planningport.OperationsQueryInput
	if decodeClosedJSON(request, &input) != nil || planningport.ValidateOperationsQuery(input) != nil {
		writePlanningError(response, http.StatusBadRequest, "OPERATIONS_INPUT_INVALID")
		return
	}
	report, err := handler.reader.Operations(request.Context(), input)
	if err != nil {
		operationsFailure(response, err, "OPERATIONS_INPUT_INVALID")
		return
	}
	writePlanningJSON(response, handler.contractVersion, handler.contractHash, report, true)
}

func (handler *operationsMutationHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != planningport.OperationsMutationPath || request.URL.RawQuery != "" {
		http.NotFound(response, request)
		return
	}
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writePlanningError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
		return
	}
	if !validPlanningRequest(request, handler.contractVersion, handler.contractHash) {
		writePlanningError(response, http.StatusConflict, "CONTRACT_MISMATCH")
		return
	}
	var input planningport.OperationsMutationInput
	if decodeClosedJSON(request, &input) != nil || planningport.ValidateOperationsMutation(input) != nil {
		writePlanningError(response, http.StatusBadRequest, "OPERATIONS_MUTATION_INPUT_INVALID")
		return
	}
	actor, authenticated := planningMutationActor(request)
	if !authenticated {
		writePlanningError(response, http.StatusUnauthorized, "OPERATIONS_HUMAN_AUTH_REQUIRED")
		return
	}
	result, err := handler.service.Mutate(request.Context(), input, homeapp.AuthenticatedOperationsActor{Kind: "human", ID: actor.ID, SessionID: actor.SessionID, Authenticated: true})
	if err != nil {
		operationsFailure(response, err, "OPERATIONS_MUTATION_INPUT_INVALID")
		return
	}
	writePlanningJSON(response, handler.contractVersion, handler.contractHash, result, true)
}
