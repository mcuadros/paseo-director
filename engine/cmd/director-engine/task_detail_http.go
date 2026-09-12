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
)

type taskDetailReader interface {
	TaskDetail(context.Context, planningport.TaskDetailQueryInput) (planningport.TaskDetailSnapshot, error)
}

type taskDetailHandler struct {
	reader          taskDetailReader
	hostID          string
	contractVersion string
	contractHash    string
}

func newTaskDetailHandler(reader taskDetailReader, hostID string) http.Handler {
	definition, err := planningport.EmbeddedDefinition()
	if err != nil {
		panic(err)
	}
	hash, err := planningport.SchemaSHA256()
	if err != nil {
		panic(err)
	}
	return &taskDetailHandler{reader: reader, hostID: hostID, contractVersion: definition.ContractVersion, contractHash: hash}
}

var taskDetailQueryFields = [...]string{
	"hostId", "context", "taskId", "paseoWorkspaceId", "paseoAgentId", "afterCursor",
}

func decodeTaskDetailQuery(request *http.Request) (planningport.TaskDetailQueryInput, error) {
	if request.ContentLength > planningport.MaximumRequestBytes {
		return planningport.TaskDetailQueryInput{}, board.ErrPlanningQueryInvalid
	}
	content, err := io.ReadAll(io.LimitReader(request.Body, planningport.MaximumRequestBytes+1))
	if err != nil || len(content) == 0 || len(content) > planningport.MaximumRequestBytes {
		return planningport.TaskDetailQueryInput{}, board.ErrPlanningQueryInvalid
	}
	content, err = jsondocument.CanonicalWithNormalizedNumbersLimit(content, planningport.MaximumRequestBytes)
	if err != nil {
		return planningport.TaskDetailQueryInput{}, board.ErrPlanningQueryInvalid
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(content, &fields); err != nil || len(fields) != len(taskDetailQueryFields) {
		return planningport.TaskDetailQueryInput{}, board.ErrPlanningQueryInvalid
	}
	for _, field := range taskDetailQueryFields {
		if _, ok := fields[field]; !ok {
			return planningport.TaskDetailQueryInput{}, board.ErrPlanningQueryInvalid
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var input planningport.TaskDetailQueryInput
	if decoder.Decode(&input) != nil || planningport.ValidateTaskDetailQuery(input) != nil {
		return planningport.TaskDetailQueryInput{}, board.ErrPlanningQueryInvalid
	}
	return input, nil
}

func (handler *taskDetailHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != planningport.TaskDetailQueryPath || request.URL.RawQuery != "" {
		http.NotFound(response, request)
		return
	}
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writePlanningError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
		return
	}
	if request.Header.Get("Content-Type") != "application/json" {
		writePlanningError(response, http.StatusBadRequest, "TASK_DETAIL_INPUT_INVALID")
		return
	}
	if request.Header.Get(contractVersionHeader) != handler.contractVersion || request.Header.Get(contractHashHeader) != handler.contractHash {
		writePlanningError(response, http.StatusConflict, "CONTRACT_MISMATCH")
		return
	}
	input, err := decodeTaskDetailQuery(request)
	if err != nil {
		writePlanningError(response, http.StatusBadRequest, "TASK_DETAIL_INPUT_INVALID")
		return
	}
	if input.HostID != handler.hostID {
		writePlanningError(response, http.StatusConflict, "TASK_DETAIL_HOST_MISMATCH")
		return
	}
	snapshot, err := handler.reader.TaskDetail(request.Context(), input)
	if err != nil {
		switch {
		case errors.Is(err, board.ErrTaskDetailCursorInvalid), errors.Is(err, board.ErrSnapshotChanged):
			writePlanningError(response, http.StatusConflict, "TASK_DETAIL_CURSOR_INVALIDATED")
		case errors.Is(err, board.ErrPlanningQueryInvalid):
			writePlanningError(response, http.StatusBadRequest, "TASK_DETAIL_INPUT_INVALID")
		default:
			writePlanningError(response, http.StatusServiceUnavailable, "TASK_DETAIL_UNAVAILABLE")
		}
		return
	}
	writePlanningJSON(response, handler.contractVersion, handler.contractHash, snapshot, true)
}
