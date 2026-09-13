// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	agentruntime "github.com/mcuadros/director-engine/agent-runtime"
	adminapp "github.com/mcuadros/director-engine/application/projectadmin"
	"github.com/mcuadros/director-engine/domain/jsondocument"
	admincontract "github.com/mcuadros/director-engine/domain/projectadmin"
)

const (
	projectAdminMCPPath       = "/v1/project-admin-mcp"
	projectAdminLifecyclePath = "/v1/project-admin-mcp/session"
)

type projectAdminMCPHandler struct {
	service       *adminapp.Service
	authorization string
}

func exactBearer(request *http.Request, expected string) bool {
	values := request.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") || expected == "" {
		return false
	}
	actual := strings.TrimPrefix(values[0], "Bearer ")
	return len(actual) == len(expected) && subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) == 1
}

func exactAdminContract(request *http.Request) bool {
	versions := request.Header.Values("X-Director-Contract-Version")
	hashes := request.Header.Values("X-Director-Contract-SHA256")
	expectedHash, err := admincontract.SchemaSHA256()
	return err == nil && len(versions) == 1 && versions[0] == admincontract.ContractVersion &&
		len(hashes) == 1 && hashes[0] == expectedHash
}

func writeAdminContractHeaders(response http.ResponseWriter) {
	hash, _ := admincontract.SchemaSHA256()
	response.Header().Set("X-Director-Contract-Version", admincontract.ContractVersion)
	response.Header().Set("X-Director-Contract-SHA256", hash)
}

func exactJSONContentType(request *http.Request) bool {
	values := request.Header.Values("Content-Type")
	return len(values) == 1 && values[0] == "application/json"
}

func decodeAdminLifecycle(request *http.Request) (struct {
	Action       string                `json:"action"`
	Registration adminapp.Registration `json:"registration"`
	RequestID    string                `json:"requestId"`
	SessionID    string                `json:"sessionId"`
}, error) {
	var input struct {
		Action       string                `json:"action"`
		Registration adminapp.Registration `json:"registration"`
		RequestID    string                `json:"requestId"`
		SessionID    string                `json:"sessionId"`
	}
	content, err := io.ReadAll(io.LimitReader(request.Body, admincontract.MaximumRequestBytes+1))
	if err != nil || len(content) == 0 || len(content) > admincontract.MaximumRequestBytes {
		return input, errors.New("invalid lifecycle input")
	}
	content, err = jsondocument.CanonicalWithNormalizedNumbersLimit(content, admincontract.MaximumRequestBytes)
	if err != nil {
		return input, errors.New("invalid lifecycle input")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil {
		return input, errors.New("invalid lifecycle input")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(content, &fields) != nil || len(fields) != 4 {
		return input, errors.New("invalid lifecycle input")
	}
	for _, field := range []string{"action", "registration", "requestId", "sessionId"} {
		if _, exists := fields[field]; !exists {
			return input, errors.New("invalid lifecycle input")
		}
	}
	if input.Action == "register" {
		if input.RequestID != "" || input.SessionID != "" {
			return input, errors.New("invalid lifecycle input")
		}
	} else if input.Action == "revoke" {
		if input.RequestID == "" || input.SessionID == "" || input.Registration != (adminapp.Registration{}) {
			return input, errors.New("invalid lifecycle input")
		}
	} else {
		return input, errors.New("invalid lifecycle input")
	}
	return input, nil
}

func (handler *projectAdminMCPHandler) lifecycle(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || !exactJSONContentType(request) ||
		!exactBearer(request, handler.authorization) {
		writePlanningError(response, http.StatusUnauthorized, "MCP_SESSION_INVALID")
		return
	}
	if !exactAdminContract(request) {
		writePlanningError(response, http.StatusPreconditionFailed, string(adminapp.CodeContractMismatch))
		return
	}
	input, err := decodeAdminLifecycle(request)
	if err != nil {
		writePlanningError(response, http.StatusBadRequest, "MCP_INPUT_INVALID")
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "application/json")
	writeAdminContractHeaders(response)
	if input.Action == "revoke" {
		if err := handler.service.RevokeSession(request.Context(), input.RequestID, input.SessionID); err != nil {
			writePlanningError(response, http.StatusConflict, adminapp.ErrorCode(err))
			return
		}
		_ = json.NewEncoder(response).Encode(map[string]any{"status": "revoked", "sessionId": input.SessionID})
		return
	}
	record, version, err := handler.service.RegisterSession(request.Context(), input.Registration)
	if err != nil {
		writePlanningError(response, http.StatusConflict, adminapp.ErrorCode(err))
		return
	}
	_ = json.NewEncoder(response).Encode(map[string]any{"status": "active", "sessionId": record.Binding.SessionID,
		"projectVersion": strconv.FormatUint(version, 10), "contractVersion": admincontract.ContractVersion})
}

func (handler *projectAdminMCPHandler) mcp(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || !exactJSONContentType(request) ||
		request.ContentLength > admincontract.MaximumRequestBytes {
		http.NotFound(response, request)
		return
	}
	if !exactAdminContract(request) {
		writePlanningError(response, http.StatusPreconditionFailed, string(adminapp.CodeContractMismatch))
		return
	}
	sessions := request.Header.Values("X-Director-Admin-Session")
	audiences := request.Header.Values("X-Director-Admin-Audience")
	values := request.Header.Values("Authorization")
	if len(sessions) != 1 || sessions[0] == "" || len(audiences) != 1 || audiences[0] == "" ||
		len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		writePlanningError(response, http.StatusUnauthorized, "MCP_SESSION_INVALID")
		return
	}
	content, err := io.ReadAll(io.LimitReader(request.Body, admincontract.MaximumRequestBytes+1))
	if err != nil || len(content) == 0 || len(content) > admincontract.MaximumRequestBytes {
		writePlanningError(response, http.StatusBadRequest, "MCP_INPUT_INVALID")
		return
	}
	content, err = jsondocument.CanonicalWithNormalizedNumbersLimit(content, admincontract.MaximumRequestBytes)
	if err != nil {
		writePlanningError(response, http.StatusBadRequest, "MCP_INPUT_INVALID")
		return
	}
	session, err := handler.service.OpenSession(request.Context(), sessions[0], audiences[0], strings.TrimPrefix(values[0], "Bearer "))
	if err != nil {
		writePlanningError(response, http.StatusUnauthorized, adminapp.ErrorCode(err))
		return
	}
	var output bytes.Buffer
	if err := agentruntime.ServeProjectAdmin(request.Context(), bytes.NewReader(append(content, '\n')), &output, session); err != nil ||
		output.Len() > admincontract.MaximumResponseBytes {
		writePlanningError(response, http.StatusServiceUnavailable, "MCP_UNAVAILABLE")
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeAdminContractHeaders(response)
	if output.Len() == 0 {
		response.WriteHeader(http.StatusNoContent)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	_, _ = response.Write(bytes.TrimSpace(output.Bytes()))
}

func (handler *projectAdminMCPHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		http.NotFound(response, request)
		return
	}
	switch request.URL.Path {
	case projectAdminLifecyclePath:
		handler.lifecycle(response, request)
	case projectAdminMCPPath:
		handler.mcp(response, request)
	default:
		http.NotFound(response, request)
	}
}
