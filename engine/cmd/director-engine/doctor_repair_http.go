// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	homeapp "github.com/mcuadros/director-engine/application/home"
	"github.com/mcuadros/director-engine/domain/jsondocument"
	planningport "github.com/mcuadros/director-engine/ports/planning"
)

type doctorReader interface {
	Doctor(context.Context, planningport.DoctorQueryInput) (planningport.DoctorReport, error)
}

type repairService interface {
	Repair(context.Context, planningport.RepairInput, homeapp.AuthenticatedRepairActor) (planningport.RepairResult, error)
}

type doctorHandler struct {
	reader          doctorReader
	contractVersion string
	contractHash    string
}

type repairHandler struct {
	service         repairService
	contractVersion string
	contractHash    string
}

func planningContractIdentity() (string, string) {
	definition, err := planningport.EmbeddedDefinition()
	if err != nil {
		panic(err)
	}
	hash, err := planningport.SchemaSHA256()
	if err != nil {
		panic(err)
	}
	return definition.ContractVersion, hash
}

func newDoctorHandler(reader doctorReader) http.Handler {
	version, hash := planningContractIdentity()
	return &doctorHandler{reader: reader, contractVersion: version, contractHash: hash}
}

func newRepairHandler(service repairService) http.Handler {
	version, hash := planningContractIdentity()
	return &repairHandler{service: service, contractVersion: version, contractHash: hash}
}

func decodeClosedJSON(request *http.Request, output any) error {
	if request.ContentLength > planningport.MaximumRequestBytes {
		return planningport.ErrQueryInvalid
	}
	content, err := io.ReadAll(io.LimitReader(request.Body, planningport.MaximumRequestBytes+1))
	if err != nil || len(content) == 0 || len(content) > planningport.MaximumRequestBytes {
		return planningport.ErrQueryInvalid
	}
	content, err = jsondocument.CanonicalWithNormalizedNumbersLimit(content, planningport.MaximumRequestBytes)
	if err != nil {
		return planningport.ErrQueryInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if decoder.Decode(output) != nil {
		return planningport.ErrQueryInvalid
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return planningport.ErrQueryInvalid
	}
	return nil
}

func validPlanningRequest(request *http.Request, version, hash string) bool {
	return request.Header.Get("Content-Type") == "application/json" &&
		request.Header.Get(contractVersionHeader) == version && request.Header.Get(contractHashHeader) == hash
}

func (handler *doctorHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != planningport.DoctorQueryPath || request.URL.RawQuery != "" {
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
	var input planningport.DoctorQueryInput
	if decodeClosedJSON(request, &input) != nil || planningport.ValidateDoctorQuery(input) != nil {
		writePlanningError(response, http.StatusBadRequest, "DOCTOR_INPUT_INVALID")
		return
	}
	report, err := handler.reader.Doctor(request.Context(), input)
	if err != nil {
		switch {
		case errors.Is(err, homeapp.ErrHostMismatch):
			writePlanningError(response, http.StatusConflict, "DOCTOR_HOST_MISMATCH")
		case errors.Is(err, homeapp.ErrDoctorVersionStale), errors.Is(err, homeapp.ErrDoctorSnapshot):
			writePlanningError(response, http.StatusConflict, "DOCTOR_FACTS_STALE")
		case errors.Is(err, planningport.ErrQueryInvalid):
			writePlanningError(response, http.StatusBadRequest, "DOCTOR_INPUT_INVALID")
		default:
			writePlanningError(response, http.StatusServiceUnavailable, "DOCTOR_UNAVAILABLE")
		}
		return
	}
	writePlanningJSON(response, handler.contractVersion, handler.contractHash, report, true)
}

func (handler *repairHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != planningport.RepairMutationPath || request.URL.RawQuery != "" {
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
	var input planningport.RepairInput
	if decodeClosedJSON(request, &input) != nil || planningport.ValidateRepairInput(input) != nil {
		writePlanningError(response, http.StatusBadRequest, "REPAIR_INPUT_INVALID")
		return
	}
	actor, authenticated := planningMutationActor(request)
	if input.Kind == "repair.apply" && !authenticated {
		writePlanningError(response, http.StatusUnauthorized, "REPAIR_HUMAN_AUTH_REQUIRED")
		return
	}
	repairActor := homeapp.AuthenticatedRepairActor{}
	if authenticated {
		repairActor = homeapp.AuthenticatedRepairActor{Kind: "human", ID: actor.ID, SessionID: actor.SessionID, Authenticated: true}
	}
	result, err := handler.service.Repair(request.Context(), input, repairActor)
	if err != nil {
		switch {
		case errors.Is(err, homeapp.ErrHostMismatch):
			writePlanningError(response, http.StatusConflict, "REPAIR_HOST_MISMATCH")
		case errors.Is(err, planningport.ErrQueryInvalid):
			writePlanningError(response, http.StatusBadRequest, "REPAIR_INPUT_INVALID")
		default:
			writePlanningError(response, http.StatusServiceUnavailable, "REPAIR_UNAVAILABLE")
		}
		return
	}
	writePlanningJSON(response, handler.contractVersion, handler.contractHash, result, true)
}
