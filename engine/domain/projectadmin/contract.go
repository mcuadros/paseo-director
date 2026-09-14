// SPDX-License-Identifier: Apache-2.0

// Package projectadmin owns the closed Project administration MCP contract.
// Unlike agentbridge, its authority is one Director Project plus the native
// Paseo Workspace, Agent, and session audience. Task and Run identifiers may
// appear only as operation targets and are always checked against that Project.
package projectadmin

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"

	"github.com/mcuadros/director-engine/domain/jsondocument"
)

const (
	ContractVersion      = "director.project-admin-mcp/v1"
	SessionSchemaVersion = "director.project-admin-mcp-session/v1"
	MaximumRequestBytes  = 64 * 1024
	MaximumResponseBytes = 64 * 1024
	MaximumSessions      = 128
	MaximumReceipts      = 256
)

//go:embed schemas/director-project-admin-mcp.v1.json
var embeddedSchema []byte

type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Mutating    bool            `json:"mutating"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type Definition struct {
	MetaSchema           string           `json:"$schema"`
	SchemaVersion        int              `json:"schemaVersion"`
	ContractVersion      string           `json:"contractVersion"`
	SupportedPaseo       string           `json:"supportedPaseoVersion"`
	MaximumRequestBytes  int              `json:"maximumRequestBytes"`
	MaximumResponseBytes int              `json:"maximumResponseBytes"`
	Tools                []ToolDefinition `json:"tools"`
}

var exactToolNames = []string{
	"director_admin_project_read",
	"director_admin_planning_read",
	"director_admin_execution_read",
	"director_admin_planning_command",
	"director_admin_control_command",
	"director_admin_diagnostics_read",
}

func CanonicalJSON(schema []byte) ([]byte, error) {
	return jsondocument.Canonical(schema)
}

func CanonicalSHA256(schema []byte) (string, error) {
	canonical, err := CanonicalJSON(schema)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func ParseDefinition(schema []byte) (Definition, error) {
	canonical, err := CanonicalJSON(schema)
	if err != nil {
		return Definition{}, err
	}
	var definition Definition
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&definition) != nil || definition.SchemaVersion != 1 ||
		definition.ContractVersion != ContractVersion || definition.SupportedPaseo != "0.7.2" ||
		definition.MaximumRequestBytes != MaximumRequestBytes || definition.MaximumResponseBytes != MaximumResponseBytes ||
		len(definition.Tools) != len(exactToolNames) {
		return Definition{}, errors.New("project administration MCP contract is invalid")
	}
	for index, tool := range definition.Tools {
		if tool.Name != exactToolNames[index] || tool.Description == "" || len(tool.InputSchema) == 0 {
			return Definition{}, errors.New("project administration MCP tool catalog is invalid")
		}
		var input any
		if json.Unmarshal(tool.InputSchema, &input) != nil || !closedSchema(input) || forbiddenAuthorityField(input) {
			return Definition{}, errors.New("project administration MCP tool input is not closed")
		}
	}
	return definition, nil
}

func closedSchema(value any) bool {
	switch current := value.(type) {
	case []any:
		for _, child := range current {
			if !closedSchema(child) {
				return false
			}
		}
	case map[string]any:
		if current["type"] == "object" {
			closed, ok := current["additionalProperties"].(bool)
			if !ok || closed {
				return false
			}
		}
		for _, child := range current {
			if !closedSchema(child) {
				return false
			}
		}
	}
	return true
}

func forbiddenAuthorityField(value any) bool {
	switch current := value.(type) {
	case []any:
		for _, child := range current {
			if forbiddenAuthorityField(child) {
				return true
			}
		}
	case map[string]any:
		if properties, ok := current["properties"].(map[string]any); ok {
			for _, name := range []string{"projectId", "hostId", "agentId", "nativeAgentId", "nativeWorkspaceId"} {
				if _, exists := properties[name]; exists {
					return true
				}
			}
		}
		for _, child := range current {
			if forbiddenAuthorityField(child) {
				return true
			}
		}
	}
	return false
}

func EmbeddedDefinition() (Definition, error) { return ParseDefinition(embeddedSchema) }
func Schema() []byte                          { return slices.Clone(embeddedSchema) }
func SchemaSHA256() (string, error)           { return CanonicalSHA256(embeddedSchema) }
