// SPDX-License-Identifier: Apache-2.0

package agentoutcome

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestClosedOutcomeSchemas(t *testing.T) {
	expected := []Kind{
		Completed,
		NeedsValidation,
		NeedsReview,
		NeedsHumanDecision,
		BlockedByDependency,
		BlockedByAccess,
		BudgetExhausted,
	}
	if actual := Kinds(); !slices.Equal(actual, expected) {
		t.Fatalf("Kinds() = %v, want %v", actual, expected)
	}
	for _, kind := range expected {
		t.Run(string(kind), func(t *testing.T) {
			schemaBytes, ok := Schema(kind)
			if !ok {
				t.Fatalf("Schema(%q) is missing", kind)
			}
			var schema struct {
				Type                 string `json:"type"`
				AdditionalProperties *bool  `json:"additionalProperties"`
				Required             []string
				Properties           map[string]struct {
					Const string `json:"const"`
				} `json:"properties"`
			}
			if err := json.Unmarshal(schemaBytes, &schema); err != nil {
				t.Fatalf("decode schema: %v", err)
			}
			if schema.Type != "object" || schema.AdditionalProperties == nil || *schema.AdditionalProperties {
				t.Fatalf("schema %q is not a closed object", kind)
			}
			if !slices.Contains(schema.Required, "outcome") {
				t.Fatalf("schema %q does not require outcome", kind)
			}
			if schema.Properties["outcome"].Const != string(kind) {
				t.Fatalf("schema outcome = %q, want %q", schema.Properties["outcome"].Const, kind)
			}
		})
	}
}

func TestKindsAndSchemasRejectExtension(t *testing.T) {
	actual := Kinds()
	actual[0] = Kind("other")
	if Kinds()[0] != Completed {
		t.Fatal("Kinds returned mutable package state")
	}
	if _, ok := Schema(Kind("other")); ok {
		t.Fatal("Schema accepted an outcome outside the closed vocabulary")
	}
}
