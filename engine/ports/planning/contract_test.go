// SPDX-License-Identifier: Apache-2.0

package planning

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestEmbeddedPlanningContract(t *testing.T) {
	definition, err := EmbeddedDefinition()
	if err != nil {
		t.Fatal(err)
	}
	if definition.ContractVersion != "director-planning/v1" || definition.SchemaVersion != 1 {
		t.Fatalf("planning identity = %#v", definition)
	}
	if definition.QueryPath != QueryPath || definition.MaximumRequestBytes != MaximumRequestBytes ||
		definition.MaximumResponseBytes != MaximumResponseBytes || definition.MaximumPageSize != MaximumPageSize {
		t.Fatalf("planning transport bounds = %#v", definition)
	}
	if len(definition.DerivedStates) != 7 || len(definition.AttentionCodes) != 12 ||
		len(definition.AllowedActions) != 17 || len(definition.ConfigurationKeys) != 8 {
		t.Fatalf("planning closed vocabularies = %#v", definition)
	}
	hash, err := SchemaSHA256()
	if err != nil {
		t.Fatal(err)
	}
	if len(hash) != 64 {
		t.Fatalf("planning schema hash length = %d", len(hash))
	}
}

func TestExecutionControlMutationsExposeNoActorOrConfirmationBoolean(t *testing.T) {
	hash, err := SchemaSHA256()
	if err != nil {
		t.Fatal(err)
	}
	valid := MutationInput{
		SchemaVersion: 1, ContractVersion: "director-planning/v1", ContractHash: hash,
		RequestID: "request-control-0001", IdempotencyKey: "request-control-0001",
		ExpectedVersion: "7", Intent: MutationIntent{Type: "project.pause", ProjectID: "project"},
	}
	if err := ValidateControlMutation(valid); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{MutationInput{}, MutationIntent{}} {
		typeOf := reflect.TypeOf(value)
		for _, forbidden := range []string{"Actor", "ActorID", "ActorKind", "Authenticated", "HumanConfirmed"} {
			if _, present := typeOf.FieldByName(forbidden); present {
				t.Fatalf("%s exposes caller-authored %s", typeOf.Name(), forbidden)
			}
		}
	}
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"actor", "actorId", "actorKind", "authenticated", "humanConfirmed"} {
		if _, present := fields[forbidden]; present {
			t.Fatalf("mutation wire exposes %s", forbidden)
		}
	}
	confirmation := "confirmation-current"
	valid.Intent.Type = "project.emergency-stop.confirm"
	valid.HumanApprovalRef = &confirmation
	if err := ValidateControlMutation(valid); err != nil {
		t.Fatal(err)
	}
	valid.HumanApprovalRef = nil
	if err := ValidateControlMutation(valid); err == nil {
		t.Fatal("emergency confirmation without server reference was accepted")
	}
}

func TestPlanningQueryValidationMatchesTheClosedGeneratedBoundary(t *testing.T) {
	valid := QueryInput{
		WorkspaceIDs: []string{}, EpicIDs: []string{}, States: []string{}, Priorities: []string{},
		Labels: []string{}, Attention: []string{}, Sort: "scheduler_order", PageSize: MaximumPageSize,
	}
	if err := ValidateQuery(valid); err != nil {
		t.Fatalf("valid planning query: %v", err)
	}
	for name, mutate := range map[string]func(*QueryInput){
		"missing collection": func(input *QueryInput) { input.Labels = nil },
		"page too large":     func(input *QueryInput) { input.PageSize++ },
		"unknown state":      func(input *QueryInput) { input.States = []string{"invented"} },
		"duplicate filter":   func(input *QueryInput) { input.Labels = []string{"one", "one"} },
		"unknown sort":       func(input *QueryInput) { input.Sort = "client_order" },
		"invalid cursor":     func(input *QueryInput) { value := "bad.cursor"; input.Cursor = &value },
	} {
		t.Run(name, func(t *testing.T) {
			input := valid
			mutate(&input)
			if err := ValidateQuery(input); err == nil {
				t.Fatal("invalid planning query was accepted")
			}
		})
	}
}

func TestPlanningCanonicalHashIgnoresFormatting(t *testing.T) {
	first := []byte(`{"schemaVersion":1,"contractVersion":"director-planning/v1","queryNames":["planning.query","planning.task-detail"],"mutationName":"planning.mutate","maximumPageSize":100,"maximumProjects":100,"maximumWorkspaces":100,"maximumEpics":500,"derivedStates":["needs_you"],"priorities":["normal"],"stableSorts":["scheduler_order"],"allowedActions":["task.update"],"configurationKeys":["launchPolicy"],"$defs":{"derivedState":{"enum":["needs_you"]},"priority":{"enum":["normal"]},"stableSort":{"enum":["scheduler_order"]},"allowedActionKind":{"enum":["task.update"]}}}`)
	second := []byte(`{
		"$defs":{"allowedActionKind":{"enum":["task.update"]},"stableSort":{"enum":["scheduler_order"]},"priority":{"enum":["normal"]},"derivedState":{"enum":["needs_you"]}},
		"configurationKeys":["launchPolicy"],"allowedActions":["task.update"],"stableSorts":["scheduler_order"],"priorities":["normal"],"derivedStates":["needs_you"],
		"maximumEpics":500,"maximumWorkspaces":100,"maximumProjects":100,"maximumPageSize":100,"mutationName":"planning.mutate","queryNames":["planning.query","planning.task-detail"],"contractVersion":"director-planning/v1","schemaVersion":1
	}`)
	firstCanonical, err := CanonicalJSON(first)
	if err != nil {
		t.Fatal(err)
	}
	secondCanonical, err := CanonicalJSON(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstCanonical, secondCanonical) {
		t.Fatal("canonical planning JSON changed with formatting")
	}
}

func TestPlanningContractRejectsOpenObjects(t *testing.T) {
	changed := bytes.Replace(embeddedSchema, []byte(`"additionalProperties": false`), []byte(`"additionalProperties": true`), 1)
	if _, err := ParseDefinition(changed); err == nil {
		t.Fatal("planning contract accepted an open object schema")
	}
}

func TestPlanningContractRejectsDuplicateKeys(t *testing.T) {
	changed := bytes.Replace(embeddedSchema, []byte(`"schemaVersion": 1`), []byte(`"schemaVersion": 1, "schemaVersion": 1`), 1)
	if _, err := ParseDefinition(changed); err == nil {
		t.Fatal("planning contract accepted a duplicate key")
	}
}
