// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mcuadros/director-engine/domain/agentbridge"
)

func loopbackEngineURL(value string) (*url.URL, bool) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, false
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	address := net.ParseIP(strings.Trim(host, "[]"))
	return parsed, err == nil && port != "" && address != nil && address.IsLoopback()
}

func runAgentMCPProxy(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("agent-mcp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	engineValue := flags.String("engine-url", "", "exact loopback Director Engine origin")
	runID := flags.String("run", "", "fixed Run identity")
	role := flags.String("role", "", "fixed session role")
	session := flags.String("session-sha", "", "fixed session binding digest")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || *runID == "" || len(*session) != 64 || (*role != "worker" && *role != "reviewer") {
		fmt.Fprintln(stderr, "usage: director-engine agent-mcp --engine-url <loopback-origin> --run <id> --role <worker|reviewer> --session-sha <sha256>")
		return 2
	}
	base, ok := loopbackEngineURL(*engineValue)
	if !ok {
		fmt.Fprintln(stderr, "director-engine: MCP engine origin is invalid")
		return 1
	}
	endpoint := base.ResolveReference(&url.URL{Path: agentMCPPath})
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 4096), agentbridge.MaximumRequestBytes+1)
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
		request.Header.Set("X-Director-Run", *runID)
		request.Header.Set("X-Director-Role", *role)
		request.Header.Set("X-Director-Session", *session)
		response, err := client.Do(request)
		if err != nil {
			return 1
		}
		content, readErr := io.ReadAll(io.LimitReader(response.Body, agentbridge.MaximumResponseBytes+1))
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil || len(content) > agentbridge.MaximumResponseBytes {
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
