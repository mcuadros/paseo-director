// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"time"

	admincontract "github.com/mcuadros/director-engine/domain/projectadmin"
)

const projectAdminTokenEnvironment = "DIRECTOR_PROJECT_ADMIN_TOKEN"

var projectAdminProxyIdentity = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,127}$`)

func runProjectAdminMCPProxy(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("project-admin-mcp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	engineValue := flags.String("engine-url", "", "exact loopback Director Engine origin")
	sessionID := flags.String("session", "", "fixed administration session")
	audience := flags.String("audience", "", "fixed session audience")
	token := os.Getenv(projectAdminTokenEnvironment)
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || !projectAdminProxyIdentity.MatchString(*sessionID) ||
		!projectAdminProxyIdentity.MatchString(*audience) || !validProjectAdminAuthorization(token) {
		fmt.Fprintln(stderr, "usage: director-engine project-admin-mcp --engine-url <loopback-origin> --session <id> --audience <id>")
		return 2
	}
	_ = os.Unsetenv(projectAdminTokenEnvironment)
	base, ok := loopbackEngineURL(*engineValue)
	if !ok {
		fmt.Fprintln(stderr, "director-engine: MCP engine origin is invalid")
		return 1
	}
	endpoint := base.ResolveReference(&url.URL{Path: projectAdminMCPPath})
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 4096), admincontract.MaximumRequestBytes+1)
	writer := bufio.NewWriter(stdout)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		request, err := http.NewRequest(http.MethodPost, endpoint.String(), bytes.NewReader(line))
		if err != nil {
			return 1
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("X-Director-Admin-Session", *sessionID)
		request.Header.Set("X-Director-Admin-Audience", *audience)
		request.Header.Set("X-Director-Contract-Version", admincontract.ContractVersion)
		contractHash, _ := admincontract.SchemaSHA256()
		request.Header.Set("X-Director-Contract-SHA256", contractHash)
		response, err := client.Do(request)
		if err != nil {
			return 1
		}
		content, readErr := io.ReadAll(io.LimitReader(response.Body, admincontract.MaximumResponseBytes+1))
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil || len(content) > admincontract.MaximumResponseBytes {
			return 1
		}
		versions := response.Header.Values("X-Director-Contract-Version")
		hashes := response.Header.Values("X-Director-Contract-SHA256")
		if len(versions) != 1 || versions[0] != admincontract.ContractVersion || len(hashes) != 1 || hashes[0] != contractHash {
			return 1
		}
		if response.StatusCode == http.StatusNoContent && len(content) == 0 {
			continue
		}
		if response.StatusCode != http.StatusOK || len(content) == 0 {
			return 1
		}
		if _, err := writer.Write(append(bytes.TrimSpace(content), '\n')); err != nil || writer.Flush() != nil {
			return 1
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return 1
	}
	return 0
}
