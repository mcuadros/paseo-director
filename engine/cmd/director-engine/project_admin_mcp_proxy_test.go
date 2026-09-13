// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	admincontract "github.com/mcuadros/director-engine/domain/projectadmin"
)

func TestProjectAdminProxyUsesOnlyFixedAuthenticatedVersionedHTTP(t *testing.T) {
	token := strings.Repeat("a", 64)
	hash, _ := admincontract.SchemaSHA256()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != projectAdminMCPPath || request.URL.RawQuery != "" || request.Method != http.MethodPost ||
			request.Header.Get("Authorization") != "Bearer "+token || request.Header.Get("X-Director-Admin-Session") != "session-1" ||
			request.Header.Get("X-Director-Admin-Audience") != "audience-1" ||
			request.Header.Get("X-Director-Contract-Version") != admincontract.ContractVersion ||
			request.Header.Get("X-Director-Contract-SHA256") != hash {
			t.Errorf("unexpected proxy request: %#v", request)
		}
		content, _ := io.ReadAll(request.Body)
		if string(content) != `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` {
			t.Errorf("body = %s", content)
		}
		writeAdminContractHeaders(response)
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`))
	}))
	defer server.Close()
	t.Setenv(projectAdminTokenEnvironment, token)
	var stdout, stderr bytes.Buffer
	status := runProjectAdminMCPProxy([]string{"--engine-url", server.URL, "--session", "session-1", "--audience", "audience-1"},
		strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}\n"), &stdout, &stderr)
	if status != 0 || stderr.Len() != 0 || stdout.String() != "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"tools\":[]}}\n" {
		t.Fatalf("proxy = %d, stdout %q, stderr %q", status, stdout.String(), stderr.String())
	}
	if value, exists := os.LookupEnv(projectAdminTokenEnvironment); exists || value != "" {
		t.Fatal("Project administration token remained in the proxy environment")
	}
}

func TestProjectAdminProxyRefusesUnsafeOriginAndContractDrift(t *testing.T) {
	t.Setenv(projectAdminTokenEnvironment, strings.Repeat("a", 64))
	if status := runProjectAdminMCPProxy([]string{"--engine-url", "https://example.com", "--session", "session-1", "--audience", "audience-1"},
		strings.NewReader("{}\n"), io.Discard, io.Discard); status != 1 {
		t.Fatalf("unsafe origin status = %d", status)
	}

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("X-Director-Contract-Version", admincontract.ContractVersion)
		response.Header().Set("X-Director-Contract-SHA256", strings.Repeat("0", 64))
		_, _ = response.Write([]byte(`{}`))
	}))
	defer server.Close()
	t.Setenv(projectAdminTokenEnvironment, strings.Repeat("a", 64))
	if status := runProjectAdminMCPProxy([]string{"--engine-url", server.URL, "--session", "session-1", "--audience", "audience-1"},
		strings.NewReader("{}\n"), io.Discard, io.Discard); status != 1 {
		t.Fatalf("contract drift status = %d", status)
	}
}
