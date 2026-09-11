// SPDX-License-Identifier: Apache-2.0

package configuration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	repositorydomain "github.com/mcuadros/director-engine/domain/repository"
)

type configurationRemoteLengthCase struct {
	name           string
	remote         string
	canonicalBytes int
	wantAccepted   bool
}

func configurationPaddedRemote(t *testing.T, prefix, suffix string, totalBytes int) string {
	t.Helper()
	padding := totalBytes - len(prefix) - len(suffix)
	if padding < 1 {
		t.Fatalf("remote fixture length %d is too short", totalBytes)
	}
	return prefix + strings.Repeat("a", padding) + suffix
}

func configurationRemoteLengthCases(t *testing.T) []configurationRemoteLengthCase {
	t.Helper()
	cases := make([]configurationRemoteLengthCase, 0, 24)
	for _, target := range []int{
		repositorydomain.MaximumRemoteBytes - 1,
		repositorydomain.MaximumRemoteBytes,
		repositorydomain.MaximumRemoteBytes + 1,
	} {
		accepted := target <= repositorydomain.MaximumRemoteBytes
		name := fmt.Sprintf("canonical-%d", target)
		scpCanonicalPrefix := "ssh://git@a/"
		scpPath := strings.Repeat("a", target-len(scpCanonicalPrefix))
		urlCanonicalPrefix := "ssh://git@[0:0:0:0:0:0:0:1]/"
		urlPath := strings.Repeat("a", target-len(urlCanonicalPrefix))
		cases = append(cases,
			configurationRemoteLengthCase{
				name: "raw-url/" + name, remote: configurationPaddedRemote(t, "https://a.example/", "", target),
				canonicalBytes: target, wantAccepted: accepted,
			},
			configurationRemoteLengthCase{
				name: "scp-expansion/" + name, remote: "git@a:" + scpPath,
				canonicalBytes: target, wantAccepted: accepted,
			},
			configurationRemoteLengthCase{
				name:           "scp-raw-input/" + name,
				remote:         "git@a:" + strings.Repeat("a", target-len("git@a:")),
				canonicalBytes: target + len(scpCanonicalPrefix) - len("git@a:"), wantAccepted: false,
			},
			configurationRemoteLengthCase{
				name: "ipv6-url-expansion/" + name, remote: "ssh://git@[::1]/" + urlPath,
				canonicalBytes: target, wantAccepted: accepted,
			},
			configurationRemoteLengthCase{
				name:           "ipv6-url-raw-input/" + name,
				remote:         "ssh://git@[::1]/" + strings.Repeat("a", target-len("ssh://git@[::1]/")),
				canonicalBytes: target + len(urlCanonicalPrefix) - len("ssh://git@[::1]/"), wantAccepted: false,
			},
			configurationRemoteLengthCase{
				name: "percent-encoded/" + name, remote: configurationPaddedRemote(t, "https://a.example/", "%25z", target),
				canonicalBytes: target, wantAccepted: accepted,
			},
			configurationRemoteLengthCase{
				name: "utf8-byte-boundary/" + name, remote: configurationPaddedRemote(t, "https://a.example/", "é", target),
				canonicalBytes: target, wantAccepted: accepted,
			},
			configurationRemoteLengthCase{
				name:           "repeated-git-suffix/" + name,
				remote:         configurationPaddedRemote(t, "https://a.example/", ".git.git", target),
				canonicalBytes: target, wantAccepted: false,
			},
		)
	}
	return cases
}

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
    "organizer": {
      "provider": "codex",
      "model": "gpt-5.6",
      "effort": "high",
      "mode": "default",
      "permissionMode": "read-only",
      "providerOptions": [],
      "mcpCapabilities": ["project.read", "planning.command.submit"],
      "fallbackChain": []
    },
    "worker": {
      "provider": "codex",
      "model": "gpt-5.6",
      "effort": "high",
      "mode": "default",
      "permissionMode": "workspace-write",
      "providerOptions": [],
      "mcpCapabilities": ["project.read", "task.read", "task.outcome.submit"],
      "fallbackChain": []
    },
    "reviewer": {
      "provider": "claude-code",
      "model": "sonnet-4.6",
      "effort": "high",
      "mode": "default",
      "permissionMode": "read-only",
      "providerOptions": [],
      "mcpCapabilities": ["candidate.read", "review.verdict.submit"],
      "fallbackChain": []
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
    },
    "autoFixCiFailures": true,
    "autoFixReviewFeedback": true
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

func TestOptionalCostBudgetUsesExactMicrousd(t *testing.T) {
	withCost := bytes.Replace(validConfigurationJSON(), []byte(`"ciCycles": 4`), []byte(`"ciCycles": 4, "costMicrousd": 5000000`), 1)
	document, err := Parse(withCost)
	if err != nil || document.Configuration().Defaults.RunBudget.CostMicrousd != 5_000_000 {
		t.Fatalf("Parse(costMicrousd) = %#v, %v", document.Configuration().Defaults.RunBudget, err)
	}
	negative := bytes.Replace(validConfigurationJSON(), []byte(`"ciCycles": 4`), []byte(`"ciCycles": 4, "costMicrousd": -1`), 1)
	if issues := issuesFor(t, negative); !containsIssue(issues, "schema_validation_failed") && !containsIssue(issues, "budget_invalid") {
		t.Fatalf("negative cost issues = %#v", issues)
	}
}

func TestPublishBeforeReviewIsOptionalAndExplicit(t *testing.T) {
	document, err := Parse(validConfigurationJSON())
	if err != nil || document.Configuration().Defaults.PublishBeforeReview {
		t.Fatalf("default publishBeforeReview = %#v, %v", document.Configuration().Defaults, err)
	}
	explicit := bytes.Replace(validConfigurationJSON(), []byte(`"autoFixReviewFeedback": true`),
		[]byte(`"autoFixReviewFeedback": true, "publishBeforeReview": true`), 1)
	document, err = Parse(explicit)
	if err != nil || !document.Configuration().Defaults.PublishBeforeReview {
		t.Fatalf("explicit publishBeforeReview = %#v, %v", document.Configuration().Defaults, err)
	}
}

func containsIssue(issues []Issue, code string) bool {
	return slices.ContainsFunc(issues, func(current Issue) bool { return current.Code == code })
}

func expandingConfigurationJSON(t *testing.T, workspaceCount int) []byte {
	t.Helper()
	document, err := Parse(validConfigurationJSON())
	if err != nil {
		t.Fatal(err)
	}
	configuration := document.Configuration()
	configuration.Workspaces = make([]Workspace, 0, workspaceCount)
	configuration.WorkspaceOverrides = []WorkspaceOverride{}
	for index := 0; index < workspaceCount; index++ {
		identifier := fmt.Sprintf("product-%03d", index)
		configuration.Workspaces = append(configuration.Workspaces, Workspace{
			ID:                identifier,
			Remote:            fmt.Sprintf("ssh://git@host-%03d.example/product.git", index),
			SourcePath:        "/srv/" + identifier + "/" + strings.Repeat("<", 4000),
			DefaultBaseBranch: "main",
		})
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(configuration); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
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
	hash, err := SchemaSHA256()
	if err != nil || hash != "ccedd52bc3739e4f6830db163c959ec685f5b33b6a649d204748dad6d0a546d9" {
		t.Fatalf("configuration schema hash = %q: %v", hash, err)
	}
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

func TestAgentProfileSchemaIsClosedRoleScopedAndFallbackExplicit(t *testing.T) {
	var schema struct {
		Definitions map[string]struct {
			AdditionalProperties *bool    `json:"additionalProperties"`
			Required             []string `json:"required"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(Schema(), &schema); err != nil {
		t.Fatal(err)
	}
	for _, definition := range []string{"providerOption", "agentSelection", "agentProfile", "agentProfiles"} {
		current := schema.Definitions[definition]
		if current.AdditionalProperties == nil || *current.AdditionalProperties {
			t.Fatalf("schema definition %s is not closed", definition)
		}
	}
	for _, field := range []string{"mode", "providerOptions", "mcpCapabilities", "fallbackChain"} {
		if !slices.Contains(schema.Definitions["agentProfile"].Required, field) {
			t.Fatalf("agentProfile does not require %s", field)
		}
	}

	valid := string(validConfigurationJSON())
	tests := []struct {
		name  string
		input string
		code  string
	}{
		{"missing explicit fallback", strings.Replace(valid, ",\n      \"fallbackChain\": []", "", 1), "field_required"},
		{"Organizer permission expansion", strings.Replace(valid, `"permissionMode": "read-only"`, `"permissionMode": "workspace-write"`, 1), "organizer_permission_expansion"},
		{"Reviewer capability expansion", strings.Replace(valid, `"candidate.read"`, `"task.read"`, 1), "mcp_capability_forbidden"},
		{"credential-shaped option is unrepresentable", strings.Replace(valid, `"providerOptions": []`, `"providerOptions": [{"name":"apiToken","value":"redacted"}]`, 1), "provider_option_unsupported"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			issues := issuesFor(t, []byte(test.input))
			if !containsIssue(issues, test.code) {
				t.Fatalf("issues = %#v, want %s", issues, test.code)
			}
		})
	}

	document, err := Parse(validConfigurationJSON())
	if err != nil {
		t.Fatal(err)
	}
	configuration := document.Configuration()
	configuration.AgentProfiles.Worker.FallbackChain = []AgentSelection{configuration.AgentProfiles.Worker.AgentSelection}
	encoded, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	issues := issuesFor(t, encoded)
	if !containsIssue(issues, "fallback_duplicate") {
		t.Fatalf("duplicate fallback issues = %#v", issues)
	}

	configuration = document.Configuration()
	configuration.AgentProfiles.Worker.MCPCapabilities[0] = MCPReviewVerdictSubmit
	configuration.AgentProfiles.Worker.ProviderOptions = append(configuration.AgentProfiles.Worker.ProviderOptions, ProviderOption{Name: ProviderOptionNetworkAccess, Value: "enabled"})
	fresh := document.Configuration()
	if fresh.AgentProfiles.Worker.MCPCapabilities[0] != MCPProjectRead || len(fresh.AgentProfiles.Worker.ProviderOptions) != 0 {
		t.Fatal("Configuration() exposed mutable profile slices")
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
		"missing required boolean": {
			input: strings.Replace(valid, ",\n    \"autoFixCiFailures\": true", "", 1),
			code:  "schema_mismatch",
		},
		"null optional Workspace override": {
			input: strings.Replace(valid, `"workspaceId": "product",`, `"workspaceId": "product", "maxActiveTasks": null,`, 1),
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

func TestParseRejectsInvalidUTF8AndUnpairedSurrogates(t *testing.T) {
	invalidUTF8 := bytes.Replace(validConfigurationJSON(), []byte("Director"), []byte{'D', 0xff, 'r'}, 1)
	for name, input := range map[string][]byte{
		"invalid UTF-8": invalidUTF8,
		"lone high surrogate": bytes.Replace(
			validConfigurationJSON(),
			[]byte(`"name": "Director"`),
			[]byte(`"name": "Dir\ud800ector"`),
			1,
		),
		"lone low surrogate": bytes.Replace(
			validConfigurationJSON(),
			[]byte(`"name": "Director"`),
			[]byte(`"name": "Dir\udc00ector"`),
			1,
		),
	} {
		t.Run(name, func(t *testing.T) {
			issues := issuesFor(t, input)
			if !containsIssue(issues, "json_encoding_invalid") {
				t.Fatalf("issues = %#v", issues)
			}
		})
	}
}

func TestRemoteValidationAllowsOnlySafeExplicitGitTransports(t *testing.T) {
	valid := string(validConfigurationJSON())
	original := "https://github.com/example/product.git"
	for _, remote := range []string{
		"https://github.com/example/product.git",
		"ssh://git@github.com/example/product.git",
		"ssh://git@host.example:2222/example/product.git",
		"ssh://git@[2001:db8::1]/example/product.git",
		"git://github.com/example/product.git",
		"git@github.com:example/product.git",
		"github.com:example/product.git",
		"https://github.com/example/repo%252egit",
	} {
		t.Run(remote, func(t *testing.T) {
			if _, err := Parse([]byte(strings.Replace(valid, original, remote, 1))); err != nil {
				t.Fatalf("Parse(%q) error = %v", remote, err)
			}
		})
	}
}

func TestConfigurationEnforcesCanonicalRemoteLengthAndFixedPoint(t *testing.T) {
	const original = "https://github.com/example/product.git"
	for _, test := range configurationRemoteLengthCases(t) {
		t.Run(test.name, func(t *testing.T) {
			input := bytes.Replace(validConfigurationJSON(), []byte(original), []byte(test.remote), 1)
			document, err := Parse(input)
			if !test.wantAccepted {
				issues, ok := ValidationIssues(err)
				if err == nil || !ok || len(issues) == 0 {
					t.Fatalf("Parse() = %#v, %v; want bounded remote rejection", document, err)
				}
				wantCode := "remote_format_invalid"
				if len(test.remote) > repositorydomain.MaximumRemoteBytes {
					wantCode = "remote_invalid"
				}
				if !containsIssue(issues, wantCode) {
					t.Fatalf("issues = %#v, want %q", issues, wantCode)
				}
				encoded, marshalErr := json.Marshal(issues)
				if marshalErr != nil || bytes.Contains(encoded, []byte(test.remote)) || strings.Contains(err.Error(), test.remote) {
					t.Fatalf("rejected remote reached diagnostics: %s, %v, %v", encoded, err, marshalErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			workspace := document.Configuration().Workspaces[0]
			identity, err := repositorydomain.CanonicalRemote(workspace.Remote)
			if err != nil || len(identity.Canonical) != test.canonicalBytes || len(identity.Canonical) > repositorydomain.MaximumRemoteBytes {
				t.Fatalf("configuration identity bytes = %d, error = %v", len(identity.Canonical), err)
			}
			roundTrip, err := repositorydomain.CanonicalRemote(identity.Canonical)
			if err != nil || roundTrip != identity {
				t.Fatalf("configuration identity fixed point = %#v, %v; want %#v", roundTrip, err, identity)
			}
		})
	}
}

func TestRemoteValidationRejectsCredentialsUnsafeSchemesAndCommands(t *testing.T) {
	valid := string(validConfigurationJSON())
	original := "https://github.com/example/product.git"
	tests := map[string]struct {
		remote string
		code   string
	}{
		"HTTPS password": {
			remote: "https://alice:redacted@github.com/example/product.git",
			code:   "remote_userinfo_password",
		},
		"HTTPS username token": {
			remote: "https://token-shaped-username-0123456789abcdef@github.com/example/product.git",
			code:   "remote_userinfo_forbidden",
		},
		"Git username": {
			remote: "git://git@github.com/example/product.git",
			code:   "remote_userinfo_forbidden",
		},
		"SSH non-git username": {
			remote: "ssh://deploy.user@github.com/example/product.git",
			code:   "remote_username_unsupported",
		},
		"SCP non-git username": {
			remote: "deploy.user@github.com:example/product.git",
			code:   "remote_username_unsupported",
		},
		"SSH password": {
			remote: "ssh://git:redacted@github.com/example/product.git",
			code:   "remote_userinfo_password",
		},
		"scp password": {
			remote: "alice:redacted@github.com:example/product.git",
			code:   "remote_userinfo_password",
		},
		"encoded password separator": {
			remote: "https://alice%3aredacted@github.com/example/product.git",
			code:   "remote_userinfo_password",
		},
		"file URL": {
			remote: "file:///etc/passwd",
			code:   "remote_scheme_unsupported",
		},
		"file command form": {
			remote: "file:/etc/passwd",
			code:   "remote_format_invalid",
		},
		"ext command transport": {
			remote: "ext::sh",
			code:   "remote_helper_unsupported",
		},
		"custom remote helper": {
			remote: "foo.bar::sh",
			code:   "remote_helper_unsupported",
		},
		"custom remote helper absolute address": {
			remote: "a.b::/tmp/x",
			code:   "remote_helper_unsupported",
		},
		"custom remote helper arbitrary address": {
			remote: "my.helper::anything",
			code:   "remote_helper_unsupported",
		},
		"encoded custom remote helper": {
			remote: "foo.bar%3A%3Ash",
			code:   "remote_helper_unsupported",
		},
		"unencrypted HTTP": {
			remote: "http://github.com/example/product.git",
			code:   "remote_scheme_unsupported",
		},
		"command substitution": {
			remote: "ssh://git@github.com/example/`id`.git",
			code:   "remote_command_unsafe",
		},
		"encoded command substitution": {
			remote: "ssh://git@github.com/example/%60id%60.git",
			code:   "remote_command_unsafe",
		},
		"shell separator": {
			remote: "git@github.com:example/product.git;touch-marker",
			code:   "remote_command_unsafe",
		},
		"ambiguous scheme-like form": {
			remote: "javascript:payload",
			code:   "remote_format_invalid",
		},
		"local path": {
			remote: "/srv/repository",
			code:   "remote_format_invalid",
		},
		"malformed IPv6 repeated compression": {
			remote: "ssh://git@[::::]/example/product.git",
			code:   "remote_format_invalid",
		},
		"malformed IPv6 group count": {
			remote: "ssh://git@[2001:db8:1]/example/product.git",
			code:   "remote_format_invalid",
		},
		"invalid IPv4 octet": {
			remote: "ssh://git@999.1.1.1/example/product.git",
			code:   "remote_format_invalid",
		},
		"ambiguous IPv4 leading zero": {
			remote: "ssh://git@192.168.001.010/example/product.git",
			code:   "remote_format_invalid",
		},
		"embedded IPv4 before compression": {
			remote: "ssh://git@[192.168.1.1::]/example/product.git",
			code:   "remote_format_invalid",
		},
		"repeated Git suffix": {
			remote: "https://github.com/example/repository.git.git",
			code:   "remote_format_invalid",
		},
		"percent encoded repeated Git suffix": {
			remote: "https://github.com/example/repository%2egit.git",
			code:   "remote_format_invalid",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			issues := issuesFor(t, []byte(strings.Replace(valid, original, test.remote, 1)))
			if !containsIssue(issues, test.code) {
				t.Fatalf("issues = %#v, want %q", issues, test.code)
			}
		})
	}
}

func TestRepeatedGitSuffixValidationDoesNotPropagateInput(t *testing.T) {
	for _, remote := range []string{
		"https://github.com/example/repository.git.git",
		"https://github.com/example/repository.GIT.git",
		"https://github.com/example/repository%2egit.git",
		"https://github.com/example/repository.git%2egit",
		"https://github.com/example/repository%2Egit%2egit%2EGIT",
	} {
		input := bytes.Replace(
			validConfigurationJSON(),
			[]byte("https://github.com/example/product.git"),
			[]byte(remote),
			1,
		)
		issues := issuesFor(t, input)
		if !containsIssue(issues, "remote_format_invalid") {
			t.Fatalf("issues for %q = %#v", remote, issues)
		}
		encoded, err := json.Marshal(issues)
		if err != nil || bytes.Contains(encoded, []byte(remote)) {
			t.Fatalf("issues propagated %q: %s, %v", remote, encoded, err)
		}
		_, err = Parse(input)
		if err == nil || strings.Contains(err.Error(), remote) {
			t.Fatalf("Parse(%q) error = %v", remote, err)
		}
	}
}

func TestRemoteValidationDoesNotDecodeNestedPercentTwice(t *testing.T) {
	const remote = "https://github.com/example/repo%252egit"
	input := bytes.Replace(
		validConfigurationJSON(),
		[]byte("https://github.com/example/product.git"),
		[]byte(remote),
		1,
	)
	document, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	configuration := document.Configuration()
	if len(configuration.Workspaces) != 1 || configuration.Workspaces[0].Remote != remote {
		t.Fatalf("parsed remote = %#v", configuration.Workspaces)
	}
}

func TestRemoteValidationDoesNotPropagateRejectedCredential(t *testing.T) {
	const secret = "token-shaped-username-0123456789abcdef"
	input := bytes.Replace(
		validConfigurationJSON(),
		[]byte("https://github.com/example/product.git"),
		[]byte("https://"+secret+"@github.com/example/product.git"),
		1,
	)
	issues := issuesFor(t, input)
	encoded, err := json.Marshal(issues)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(secret)) {
		t.Fatalf("validation issues propagated credential material: %s", encoded)
	}
	_, err = Parse(input)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("Parse() error propagated credential material: %v", err)
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
		"source path whitespace": {
			input: strings.Replace(valid, `/srv/director/product`, `/srv/director/pro duct`, 1),
			code:  "source_path_invalid",
		},
		"source path control": {
			input: strings.Replace(valid, `/srv/director/product`, `/srv/director/pro\nduct`, 1),
			code:  "source_path_invalid",
		},
		"source path format control": {
			input: strings.Replace(valid, `/srv/director/product`, `/srv/director/pro\u200bduct`, 1),
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
		"reference whitespace": {
			input: strings.Replace(valid, `skills/commits/SKILL.md`, `skills/com mits/SKILL.md`, 1),
			code:  "reference_path_invalid",
		},
		"reference control": {
			input: strings.Replace(valid, `skills/commits/SKILL.md`, `skills/com\tmits/SKILL.md`, 1),
			code:  "reference_path_invalid",
		},
		"reference format control": {
			input: strings.Replace(valid, `skills/commits/SKILL.md`, `skills/com\u200bmits/SKILL.md`, 1),
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
	aliasWorkspace := `{
      "id": "product-alias",
      "name": "Product alias",
      "remote": "git@github.com:example/product.git",
      "sourcePath": "/srv/director/product-alias",
      "defaultBaseBranch": "main"
    }`
	tests["canonical repository alias"] = struct {
		input string
		code  string
	}{
		input: strings.Replace(valid, workspaceObject, workspaceObject+",\n    "+aliasWorkspace, 1),
		code:  "workspace_repository_duplicate",
	}
	sourceAliasWorkspace := strings.ReplaceAll(aliasWorkspace,
		`"remote": "git@github.com:example/product.git"`,
		`"remote": "https://github.com/example/other.git"`)
	sourceAliasWorkspace = strings.Replace(sourceAliasWorkspace, "/srv/director/product-alias", "/srv/director/product", 1)
	tests["canonical source path"] = struct {
		input string
		code  string
	}{
		input: strings.Replace(valid, workspaceObject, workspaceObject+",\n    "+sourceAliasWorkspace, 1),
		code:  "workspace_source_duplicate",
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

func TestParseBoundsCanonicalExpansionBeforeActivation(t *testing.T) {
	admitted := expandingConfigurationJSON(t, 40)
	if len(admitted) >= MaximumDocumentBytes {
		t.Fatalf("admitted fixture raw bytes = %d", len(admitted))
	}
	document, err := Parse(admitted)
	if err != nil {
		t.Fatalf("Parse(admitted expansion) error = %v", err)
	}
	if len(document.CanonicalJSON()) > MaximumCanonicalDocumentBytes ||
		len(document.CanonicalJSON()) < MaximumCanonicalDocumentBytes*9/10 ||
		len(document.CanonicalJSON()) <= len(admitted) {
		t.Fatalf("admitted sizes raw=%d canonical=%d", len(admitted), len(document.CanonicalJSON()))
	}

	rejected := expandingConfigurationJSON(t, 128)
	if len(rejected) >= MaximumDocumentBytes {
		t.Fatalf("rejected fixture does not isolate canonical expansion: raw bytes = %d", len(rejected))
	}
	issues := issuesFor(t, rejected)
	if !containsIssue(issues, "canonical_document_too_large") {
		t.Fatalf("issues = %#v", issues)
	}
}
