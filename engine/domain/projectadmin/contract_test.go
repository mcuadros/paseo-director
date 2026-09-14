// SPDX-License-Identifier: Apache-2.0

package projectadmin

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestEmbeddedDefinitionIsExactClosedAndProjectSelectorFree(t *testing.T) {
	definition, err := EmbeddedDefinition()
	if err != nil {
		t.Fatalf("EmbeddedDefinition() error = %v", err)
	}
	names := make([]string, len(definition.Tools))
	for index, tool := range definition.Tools {
		names[index] = tool.Name
	}
	if !slices.Equal(names, exactToolNames) {
		t.Fatalf("tool catalog = %q", names)
	}
	canonical, err := CanonicalJSON(Schema())
	if err != nil {
		t.Fatalf("CanonicalJSON() error = %v", err)
	}
	for _, forbidden := range []string{`"projectId"`, `"hostId"`, `"agentId"`, `"nativeAgentId"`, `"nativeWorkspaceId"`,
		`"humanApprovalRef"`, `"acknowledgementRevision"`, `"dependency.override"`, `"task.integrate"`,
		`"project.emergency-stop.confirm"`} {
		if strings.Contains(string(canonical), forbidden) {
			t.Fatalf("model contract contains forbidden authority or human-only field %s", forbidden)
		}
	}
	hash, err := SchemaSHA256()
	if err != nil || !hashPattern.MatchString(hash) {
		t.Fatalf("SchemaSHA256() = %q, %v", hash, err)
	}
}

func TestParseDefinitionRejectsSelectorAndOpenSchema(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal(Schema(), &document); err != nil {
		t.Fatal(err)
	}
	tools := document["tools"].([]any)
	input := tools[0].(map[string]any)["inputSchema"].(map[string]any)
	properties := input["properties"].(map[string]any)
	properties["projectId"] = map[string]any{"type": "string"}
	changed, _ := json.Marshal(document)
	if _, err := ParseDefinition(changed); err == nil {
		t.Fatal("ParseDefinition accepted a model-selected Project")
	}
	delete(properties, "projectId")
	input["additionalProperties"] = true
	changed, _ = json.Marshal(document)
	if _, err := ParseDefinition(changed); err == nil {
		t.Fatal("ParseDefinition accepted an open tool input")
	}
}

func TestSessionStateIsProjectBoundAndStoresOnlyTokenDigest(t *testing.T) {
	binding := SessionBinding{SchemaVersion: SessionSchemaVersion, SessionID: "session-1", ProjectID: "project-1",
		NativeWorkspaceID: "workspace-1", NativeAgentID: "agent-1", Audience: "audience-1"}
	record := SessionRecord{Binding: binding, TokenSHA256: strings.Repeat("a", 64), State: SessionActive, CreatedAtMillis: 1}
	state := State{Sessions: []SessionRecord{record}, Receipts: []Receipt{{Key: "receipt-1", SessionID: binding.SessionID,
		ToolName: "command", RequestID: "request-1", PayloadSHA256: strings.Repeat("b", 64), Output: json.RawMessage(`{"ok":true}`), RecordedAtMillis: 2}}}
	if !ValidState(state, binding.ProjectID) || ValidState(state, "project-2") {
		t.Fatal("session ledger was not fixed to exactly one Project")
	}
	digestRecord := record
	digestRecord.TokenSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte("secret")))
	if !TokenMatches(digestRecord, "secret") || TokenMatches(digestRecord, "different") {
		t.Fatal("session token comparison did not use the persisted digest")
	}
	encoded, _ := json.Marshal(state)
	if strings.Contains(string(encoded), "secret") {
		t.Fatal("session state serialized a raw credential")
	}
}
