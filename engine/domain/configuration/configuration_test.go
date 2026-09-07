// SPDX-License-Identifier: Apache-2.0

package configuration

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func validConfigurationJSON() []byte {
	return []byte(`{
  "$schema": "` + SchemaID + `",
  "schemaVersion": 1,
  "project": {"id": "director", "name": "Director"},
  "workspaces": [
    {
      "id": "product",
      "remote": "https://github.com/example/product.git",
      "sourcePath": "/srv/director/product",
      "defaultBaseBranch": "main"
    }
  ],
  "agentProfiles": {
    "taskAgent": {
      "provider": "codex",
      "model": "gpt-5.6",
      "effort": "high",
      "permissionMode": "workspace-write"
    },
    "reviewerAgent": {
      "provider": "claude-code",
      "model": "sonnet-4.6",
      "effort": "high",
      "permissionMode": "read-only"
    }
  },
  "defaults": {
    "launchPolicy": "manual",
    "deliveryMode": "pull_request",
    "limits": {
      "maxActiveTasks": 4,
      "maxActiveTasksPerWorkspace": 2,
      "maxConcurrentAgents": 8,
      "maxSubagentsPerTask": 3
    },
    "runBudget": {
      "elapsedSeconds": 7200,
      "tokens": 200000,
      "turns": 32,
      "ciCycles": 4
    }
  },
  "workspaceOverrides": [
    {
      "workspaceId": "product",
      "launchPolicy": "inherit",
      "deliveryMode": "direct"
    }
  ],
  "skills": [
    {"id": "commits", "path": "skills/commits/SKILL.md"}
  ],
  "templates": [
    {"id": "task", "path": "templates/task.md"}
  ]
}`)
}

func issuesFor(t *testing.T, input []byte) []Issue {
	t.Helper()
	_, err := Parse(input)
	if err == nil {
		t.Fatal("Parse() accepted invalid configuration")
	}
	issues, ok := ValidationIssues(err)
	if !ok || len(issues) == 0 {
		t.Fatalf("Parse() error = %v, want ValidationError", err)
	}
	return issues
}

func containsIssue(issues []Issue, code string) bool {
	return slices.ContainsFunc(issues, func(current Issue) bool { return current.Code == code })
}

func TestParseValidConfigurationIsCanonicalAndDefensive(t *testing.T) {
	document, err := Parse(validConfigurationJSON())
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(document.SHA256()) != 64 {
		t.Fatalf("SHA256() = %q", document.SHA256())
	}
	remarshaled, err := json.Marshal(document.Configuration())
	if err != nil {
		t.Fatal(err)
	}
	equivalent, err := Parse(remarshaled)
	if err != nil {
		t.Fatalf("Parse(remarshaled) error = %v", err)
	}
	if equivalent.SHA256() != document.SHA256() {
		t.Fatalf("equivalent hashes differ: %s != %s", equivalent.SHA256(), document.SHA256())
	}

	configuration := document.Configuration()
	configuration.Workspaces[0].ID = "changed"
	configuration.Skills[0].Path = "changed"
	if fresh := document.Configuration(); fresh.Workspaces[0].ID != "product" || fresh.Skills[0].Path != "skills/commits/SKILL.md" {
		t.Fatalf("Configuration() exposed mutable document state: %#v", fresh)
	}
	canonical := document.CanonicalJSON()
	canonical[0] = '['
	if document.CanonicalJSON()[0] != '{' {
		t.Fatal("CanonicalJSON() exposed mutable document state")
	}
	schema := Schema()
	schema[0] = '['
	if Schema()[0] != '{' {
		t.Fatal("Schema() exposed mutable embedded bytes")
	}
	withoutHelpers := bytes.Replace(validConfigurationJSON(), []byte(`"maxSubagentsPerTask": 3`), []byte(`"maxSubagentsPerTask": 0`), 1)
	if _, err := Parse(withoutHelpers); err != nil {
		t.Fatalf("Parse(maxSubagentsPerTask=0) error = %v", err)
	}
}

func TestSchemaIsPublishedClosedAndVersioned(t *testing.T) {
	var schema struct {
		ID                   string   `json:"$id"`
		Dialect              string   `json:"$schema"`
		Type                 string   `json:"type"`
		AdditionalProperties *bool    `json:"additionalProperties"`
		Required             []string `json:"required"`
		Properties           map[string]struct {
			Const any `json:"const"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(Schema(), &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	if schema.ID != SchemaID || schema.Dialect != "https://json-schema.org/draft/2020-12/schema" || schema.Type != "object" {
		t.Fatalf("schema identity = %#v", schema)
	}
	if schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		t.Fatal("configuration schema is not a closed object")
	}
	for _, field := range []string{"$schema", "schemaVersion", "project", "workspaces", "agentProfiles", "defaults", "workspaceOverrides", "skills", "templates"} {
		if !slices.Contains(schema.Required, field) {
			t.Fatalf("schema does not require %q", field)
		}
	}
	if schema.Properties["$schema"].Const != SchemaID {
		t.Fatalf("$schema const = %#v", schema.Properties["$schema"].Const)
	}
	if schema.Properties["schemaVersion"].Const != float64(SchemaVersion) {
		t.Fatalf("schemaVersion const = %#v", schema.Properties["schemaVersion"].Const)
	}
}

func TestParseRejectsNonStrictJSONAndSchemaDrift(t *testing.T) {
	valid := string(validConfigurationJSON())
	tests := map[string]struct {
		input string
		code  string
	}{
		"unknown root field": {
			input: strings.Replace(valid, `"schemaVersion": 1,`, `"schemaVersion": 1, "unexpected": true,`, 1),
			code:  "schema_mismatch",
		},
		"unknown nested field": {
			input: strings.Replace(valid, `"name": "Director"`, `"name": "Director", "agent": true`, 1),
			code:  "schema_mismatch",
		},
		"duplicate nested key": {
			input: strings.Replace(valid, `"id": "director",`, `"id": "director", "id": "other",`, 1),
			code:  "json_invalid",
		},
		"unsupported schema uri": {
			input: strings.Replace(valid, SchemaID, "https://example.invalid/schema.json", 1),
			code:  "schema_id_unsupported",
		},
		"unsupported version": {
			input: strings.Replace(valid, `"schemaVersion": 1`, `"schemaVersion": 2`, 1),
			code:  "schema_version_unsupported",
		},
		"non-integer spelling": {
			input: strings.Replace(valid, `"maxActiveTasks": 4`, `"maxActiveTasks": 4.0`, 1),
			code:  "json_invalid",
		},
		"missing zero-valued required field": {
			input: strings.Replace(valid, ",\n      \"maxSubagentsPerTask\": 3", "", 1),
			code:  "schema_mismatch",
		},
		"null zero-valued required field": {
			input: strings.Replace(valid, `"maxSubagentsPerTask": 3`, `"maxSubagentsPerTask": null`, 1),
			code:  "schema_mismatch",
		},
		"trailing value": {
			input: valid + `{}`,
			code:  "json_invalid",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			issues := issuesFor(t, []byte(test.input))
			if !containsIssue(issues, test.code) {
				t.Fatalf("issues = %#v, want code %q", issues, test.code)
			}
		})
	}
}

func TestParseRejectsSemanticConflicts(t *testing.T) {
	valid := string(validConfigurationJSON())
	tests := map[string]struct {
		input string
		code  string
	}{
		"relative source path": {
			input: strings.Replace(valid, `/srv/director/product`, `../product`, 1),
			code:  "source_path_invalid",
		},
		"unsafe base branch": {
			input: strings.Replace(valid, `"defaultBaseBranch": "main"`, `"defaultBaseBranch": "refs/../main"`, 1),
			code:  "base_branch_invalid",
		},
		"unknown override Workspace": {
			input: strings.Replace(valid, `"workspaceId": "product"`, `"workspaceId": "unknown"`, 1),
			code:  "workspace_unknown",
		},
		"empty override": {
			input: strings.Replace(valid, `"deliveryMode": "direct"`, `"deliveryMode": "inherit"`, 1),
			code:  "override_empty",
		},
		"reference traversal": {
			input: strings.Replace(valid, `skills/commits/SKILL.md`, `skills/../secrets/SKILL.md`, 1),
			code:  "reference_path_invalid",
		},
		"capacity conflict": {
			input: strings.Replace(valid, `"maxActiveTasksPerWorkspace": 2`, `"maxActiveTasksPerWorkspace": 5`, 1),
			code:  "limit_inconsistent",
		},
	}
	workspaceObject := `{
      "id": "product",
      "remote": "https://github.com/example/product.git",
      "sourcePath": "/srv/director/product",
      "defaultBaseBranch": "main"
    }`
	tests["duplicate Workspace"] = struct {
		input string
		code  string
	}{
		input: strings.Replace(valid, workspaceObject, workspaceObject+",\n    "+workspaceObject, 1),
		code:  "workspace_duplicate",
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			issues := issuesFor(t, []byte(test.input))
			if !containsIssue(issues, test.code) {
				t.Fatalf("issues = %#v, want code %q", issues, test.code)
			}
		})
	}
}

func TestValidationIssuesAreDeterministic(t *testing.T) {
	invalid := []byte(strings.NewReplacer(
		`"maxActiveTasks": 4`, `"maxActiveTasks": 0`,
		`"elapsedSeconds": 7200`, `"elapsedSeconds": 0`,
		`"tokens": 200000`, `"tokens": 0`,
		`"turns": 32`, `"turns": 0`,
	).Replace(string(validConfigurationJSON())))
	first, err := json.Marshal(issuesFor(t, invalid))
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 50; attempt++ {
		current, err := json.Marshal(issuesFor(t, invalid))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(current, first) {
			t.Fatalf("validation issue order changed:\n%s\n%s", first, current)
		}
	}
}

func TestParseRejectsOversizeDocumentBeforeDecode(t *testing.T) {
	issues := issuesFor(t, bytes.Repeat([]byte{' '}, MaximumDocumentBytes+1))
	if !containsIssue(issues, "document_too_large") {
		t.Fatalf("issues = %#v", issues)
	}
}
