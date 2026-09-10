// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/mcuadros/director-engine/application/board"
	"github.com/mcuadros/director-engine/domain/jsondocument"
	planningport "github.com/mcuadros/director-engine/ports/planning"
	"github.com/mcuadros/director-engine/projection"
)

type planningReader interface {
	Query(context.Context, planningport.QueryInput) (planningport.Snapshot, error)
}

type planningHandler struct {
	reader          planningReader
	contractVersion string
	contractHash    string
}

func newPlanningHandler(reader planningReader) http.Handler {
	definition, err := planningport.EmbeddedDefinition()
	if err != nil {
		panic(err)
	}
	hash, err := planningport.SchemaSHA256()
	if err != nil {
		panic(err)
	}
	return &planningHandler{reader: reader, contractVersion: definition.ContractVersion, contractHash: hash}
}

var planningQueryFields = [...]string{
	"projectId", "workspaceIds", "epicIds", "states", "priorities", "labels", "attention", "search", "sort", "cursor", "pageSize",
}

func decodePlanningQuery(request *http.Request) (planningport.QueryInput, error) {
	if request.ContentLength > planningport.MaximumRequestBytes {
		return planningport.QueryInput{}, board.ErrPlanningQueryInvalid
	}
	content, err := io.ReadAll(io.LimitReader(request.Body, planningport.MaximumRequestBytes+1))
	if err != nil || len(content) == 0 || len(content) > planningport.MaximumRequestBytes {
		return planningport.QueryInput{}, board.ErrPlanningQueryInvalid
	}
	content, err = jsondocument.CanonicalWithNormalizedNumbersLimit(content, planningport.MaximumRequestBytes)
	if err != nil {
		return planningport.QueryInput{}, board.ErrPlanningQueryInvalid
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(content, &fields); err != nil || len(fields) != len(planningQueryFields) {
		return planningport.QueryInput{}, board.ErrPlanningQueryInvalid
	}
	for _, field := range planningQueryFields {
		if _, ok := fields[field]; !ok {
			return planningport.QueryInput{}, board.ErrPlanningQueryInvalid
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var input planningport.QueryInput
	if err := decoder.Decode(&input); err != nil || input.WorkspaceIDs == nil || input.EpicIDs == nil ||
		input.States == nil || input.Priorities == nil || input.Labels == nil || input.Attention == nil {
		return planningport.QueryInput{}, board.ErrPlanningQueryInvalid
	}
	if err := planningport.ValidateQuery(input); err != nil {
		return planningport.QueryInput{}, board.ErrPlanningQueryInvalid
	}
	return input, nil
}

func (handler *planningHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != planningport.QueryPath || request.URL.RawQuery != "" {
		http.NotFound(response, request)
		return
	}
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writePlanningError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
		return
	}
	if request.Header.Get("Content-Type") != "application/json" {
		writePlanningError(response, http.StatusBadRequest, "PLANNING_INPUT_INVALID")
		return
	}
	if request.Header.Get(contractVersionHeader) != handler.contractVersion ||
		request.Header.Get(contractHashHeader) != handler.contractHash {
		writePlanningError(response, http.StatusConflict, "CONTRACT_MISMATCH")
		return
	}
	input, err := decodePlanningQuery(request)
	if err != nil {
		writePlanningError(response, http.StatusBadRequest, "PLANNING_INPUT_INVALID")
		return
	}
	snapshot, err := handler.reader.Query(request.Context(), input)
	if err != nil {
		switch {
		case errors.Is(err, projection.ErrTaskQueryCursorInvalid), errors.Is(err, projection.ErrTaskQueryCursorSnapshot):
			writePlanningError(response, http.StatusConflict, "PLANNING_CURSOR_INVALIDATED")
		case errors.Is(err, projection.ErrTaskQueryInvalid), errors.Is(err, board.ErrPlanningQueryInvalid):
			writePlanningError(response, http.StatusBadRequest, "PLANNING_INPUT_INVALID")
		default:
			writePlanningError(response, http.StatusServiceUnavailable, "PLANNING_UNAVAILABLE")
		}
		return
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(snapshot); err != nil || encoded.Len() > planningport.MaximumResponseBytes {
		writePlanningError(response, http.StatusServiceUnavailable, "PLANNING_UNAVAILABLE")
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set(contractVersionHeader, handler.contractVersion)
	response.Header().Set(contractHashHeader, handler.contractHash)
	_, _ = response.Write(encoded.Bytes())
}

func writePlanningError(response http.ResponseWriter, status int, code string) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(struct {
		Code string `json:"code"`
	}{Code: code})
}
