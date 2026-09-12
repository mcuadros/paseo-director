// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentMCPProxyPreservesValidNoContentNotifications(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		if request.URL.Path != agentMCPPath || request.Header.Get("X-Director-Run") != "run-1" ||
			request.Header.Get("X-Director-Role") != "worker" || len(request.Header.Get("X-Director-Session")) != 64 {
			t.Fatalf("misbound proxy request")
		}
		if requests == 2 {
			response.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = fmt.Fprintf(response, `{"jsonrpc":"2.0","id":%d,"result":{}}`, requests)
	}))
	defer server.Close()
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`,
	}, "\n") + "\n"
	var output, diagnostic bytes.Buffer
	code := runAgentMCPProxy([]string{"--engine-url", server.URL, "--run", "run-1", "--role", "worker",
		"--session-sha", strings.Repeat("a", 64)}, strings.NewReader(input), &output, &diagnostic)
	if code != 0 || requests != 3 || strings.Count(output.String(), "\n") != 2 || diagnostic.Len() != 0 {
		t.Fatalf("proxy code=%d requests=%d responses=%q diagnostic=%q", code, requests, output.String(), diagnostic.String())
	}
}
