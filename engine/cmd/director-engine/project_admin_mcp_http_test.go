// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	admincontract "github.com/mcuadros/director-engine/domain/projectadmin"
)

func versionAdminRequest(request *http.Request) {
	hash, _ := admincontract.SchemaSHA256()
	request.Header.Set("X-Director-Contract-Version", admincontract.ContractVersion)
	request.Header.Set("X-Director-Contract-SHA256", hash)
}

func TestProjectAdminHTTPFailsClosedBeforeServiceDispatch(t *testing.T) {
	handler := &projectAdminMCPHandler{authorization: strings.Repeat("a", 64)}
	tests := []struct {
		name       string
		path       string
		authorize  bool
		version    bool
		wantStatus int
		wantCode   string
	}{
		{name: "anonymous lifecycle", path: projectAdminLifecyclePath, wantStatus: http.StatusUnauthorized, wantCode: "MCP_SESSION_INVALID"},
		{name: "anonymous MCP", path: projectAdminMCPPath, version: true, wantStatus: http.StatusUnauthorized, wantCode: "MCP_SESSION_INVALID"},
		{name: "unversioned lifecycle", path: projectAdminLifecyclePath, authorize: true, wantStatus: http.StatusPreconditionFailed, wantCode: "MCP_CONTRACT_MISMATCH"},
		{name: "unversioned MCP", path: projectAdminMCPPath, authorize: true, wantStatus: http.StatusPreconditionFailed, wantCode: "MCP_CONTRACT_MISMATCH"},
		{name: "malformed versioned lifecycle", path: projectAdminLifecyclePath, authorize: true, version: true, wantStatus: http.StatusBadRequest, wantCode: "MCP_INPUT_INVALID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(`{}`))
			request.Header.Set("Content-Type", "application/json")
			if test.authorize {
				request.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
			}
			if test.version {
				versionAdminRequest(request)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), test.wantCode) {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestProjectAdminHTTPRejectsDuplicateAuthorizationAndContractHeaders(t *testing.T) {
	handler := &projectAdminMCPHandler{authorization: strings.Repeat("a", 64)}
	request := httptest.NewRequest(http.MethodPost, projectAdminLifecyclePath, strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Add("Authorization", "Bearer "+strings.Repeat("a", 64))
	request.Header.Add("Authorization", "Bearer "+strings.Repeat("a", 64))
	versionAdminRequest(request)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("duplicate bearer status = %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, projectAdminLifecyclePath, strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
	versionAdminRequest(request)
	request.Header.Add("X-Director-Contract-Version", admincontract.ContractVersion)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionFailed {
		t.Fatalf("duplicate contract status = %d", response.Code)
	}

	for _, header := range []string{"Content-Type", "X-Director-Admin-Session", "X-Director-Admin-Audience"} {
		request = httptest.NewRequest(http.MethodPost, projectAdminMCPPath, strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
		request.Header.Set("X-Director-Admin-Session", "session-1")
		request.Header.Set("X-Director-Admin-Audience", "audience-1")
		versionAdminRequest(request)
		request.Header.Add(header, request.Header.Get(header))
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized && response.Code != http.StatusNotFound {
			t.Fatalf("duplicate %s status = %d", header, response.Code)
		}
	}
}

func TestDecodeAdminLifecycleRequiresTheExactEnvelope(t *testing.T) {
	for name, body := range map[string]string{
		"missing keys":           `{"action":"revoke","requestId":"r","sessionId":"s"}`,
		"extra key":              `{"action":"revoke","registration":{},"requestId":"r","sessionId":"s","projectId":"project-b"}`,
		"registration on revoke": `{"action":"revoke","registration":{"requestId":"r"},"requestId":"r","sessionId":"s"}`,
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, projectAdminLifecyclePath, strings.NewReader(body))
			if _, err := decodeAdminLifecycle(request); err == nil {
				t.Fatal("invalid lifecycle envelope was accepted")
			}
		})
	}
}
