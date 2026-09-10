// SPDX-License-Identifier: Apache-2.0

package agentruntime

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	applicationbridge "github.com/mcuadros/director-engine/application/agentbridge"
	domainbridge "github.com/mcuadros/director-engine/domain/agentbridge"
	"github.com/mcuadros/director-engine/domain/jsondocument"
)

const serverName = "director-session-mcp"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type mcpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type callResult struct {
	Content []content `json:"content"`
	IsError bool      `json:"isError"`
}

func strictRequest(raw []byte, destination any) error {
	canonical, err := jsondocument.CanonicalWithNormalizedNumbersLimit(raw, domainbridge.MaximumRequestBytes)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

func validRequestID(id json.RawMessage) bool {
	if len(id) == 0 || bytes.Equal(id, []byte("null")) || len(id) > 256 {
		return false
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(id))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return false
	}
	switch current := value.(type) {
	case string:
		return current != "" && len(current) <= 128 && strings.IndexFunc(current, func(character rune) bool { return character < 0x20 }) < 0
	case json.Number:
		return len(current) <= 64
	default:
		return false
	}
}

func stableRequestID(id json.RawMessage) string {
	canonical, _ := jsondocument.CanonicalWithNormalizedNumbers(id)
	digest := sha256.Sum256(canonical)
	return "mcp-call-" + hex.EncodeToString(digest[:])
}

func boundedCode(err error) string {
	var failure *applicationbridge.Failure
	if errors.As(err, &failure) {
		if failure.PreflightCode != "" {
			return string(failure.PreflightCode)
		}
		return string(failure.Code)
	}
	return string(applicationbridge.CodeStoreUnavailable)
}

func responseError(id json.RawMessage, code int, message string) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}}
}

func writeResponse(writer *bufio.Writer, response rpcResponse) error {
	encoded, err := json.Marshal(response)
	if err != nil {
		return errors.New("encode bounded MCP response")
	}
	if len(encoded)+1 > domainbridge.MaximumResponseBytes {
		encoded, _ = json.Marshal(responseError(response.ID, -32603, string(applicationbridge.CodeOutputTooLarge)))
	}
	if _, err := writer.Write(encoded); err != nil {
		return err
	}
	if err := writer.WriteByte('\n'); err != nil {
		return err
	}
	return writer.Flush()
}

func toolsList(descriptor applicationbridge.Descriptor) []mcpTool {
	tools := make([]mcpTool, 0, len(descriptor.Tools))
	for _, tool := range descriptor.Tools {
		tools = append(tools, mcpTool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
	}
	return tools
}

func validProtocolVersion(value string) bool {
	if len(value) != len("2025-06-18") {
		return false
	}
	for index, character := range value {
		if index == 4 || index == 7 {
			if character != '-' {
				return false
			}
			continue
		}
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func initializeResult(request rpcRequest, descriptor applicationbridge.Descriptor) any {
	protocolVersion := "2025-06-18"
	if len(request.Params) > 0 {
		var parameters struct {
			ProtocolVersion string         `json:"protocolVersion"`
			Capabilities    map[string]any `json:"capabilities"`
			ClientInfo      map[string]any `json:"clientInfo"`
		}
		if json.Unmarshal(request.Params, &parameters) == nil && validProtocolVersion(parameters.ProtocolVersion) {
			protocolVersion = parameters.ProtocolVersion
		}
	}
	return struct {
		ProtocolVersion string         `json:"protocolVersion"`
		Capabilities    map[string]any `json:"capabilities"`
		ServerInfo      map[string]any `json:"serverInfo"`
		Instructions    string         `json:"instructions"`
	}{
		ProtocolVersion: protocolVersion,
		Capabilities:    map[string]any{"tools": map[string]any{"listChanged": false}},
		ServerInfo: map[string]any{
			"name": serverName, "version": descriptor.ContractVersion,
		},
		Instructions: "Use only the tools listed for this immutable Director session scope.",
	}
}

func handleRequest(ctx context.Context, session *applicationbridge.Session, request rpcRequest) *rpcResponse {
	if request.JSONRPC != "2.0" || request.Method == "" {
		response := responseError(request.ID, -32600, "Invalid Request")
		return &response
	}
	if len(request.ID) == 0 {
		// MCP notifications are wake-ups only and have no response.
		return nil
	}
	if !validRequestID(request.ID) {
		response := responseError(json.RawMessage("null"), -32600, "Invalid Request")
		return &response
	}
	descriptor := session.Descriptor()
	response := rpcResponse{JSONRPC: "2.0", ID: request.ID}
	switch request.Method {
	case "initialize":
		response.Result = initializeResult(request, descriptor)
	case "ping":
		response.Result = struct{}{}
	case "tools/list":
		response.Result = struct {
			Tools []mcpTool `json:"tools"`
		}{Tools: toolsList(descriptor)}
	case "tools/call":
		var parameters callParams
		if len(request.Params) == 0 || strictRequest(request.Params, &parameters) != nil ||
			parameters.Name == "" || len(parameters.Arguments) == 0 {
			returnValue := responseError(request.ID, -32602, "Invalid params")
			return &returnValue
		}
		result, err := session.Call(ctx, stableRequestID(request.ID), parameters.Name, parameters.Arguments)
		if err != nil {
			response.Result = callResult{Content: []content{{Type: "text", Text: boundedCode(err)}}, IsError: true}
		} else {
			response.Result = callResult{Content: []content{{Type: "text", Text: string(result.Payload)}}, IsError: false}
		}
	default:
		returnValue := responseError(request.ID, -32601, "Method not found")
		return &returnValue
	}
	return &response
}

// Serve runs one newline-delimited stdio MCP session. The authorized Session
// is created by the standalone engine application before this transport starts.
func Serve(ctx context.Context, input io.Reader, output io.Writer, session *applicationbridge.Session) error {
	if session == nil {
		return errors.New("authorized MCP session is required")
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), domainbridge.MaximumRequestBytes+1)
	writer := bufio.NewWriter(output)
	for scanner.Scan() {
		line := slicesTrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var request rpcRequest
		if strictRequest(line, &request) != nil {
			if err := writeResponse(writer, responseError(json.RawMessage("null"), -32700, "Parse error")); err != nil {
				return err
			}
			continue
		}
		response := handleRequest(ctx, session, request)
		if response != nil {
			if err := writeResponse(writer, *response); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("bounded MCP input failed")
	}
	return writer.Flush()
}

func slicesTrimSpace(value []byte) []byte { return bytes.TrimSpace(value) }
