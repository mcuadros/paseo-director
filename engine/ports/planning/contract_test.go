// SPDX-License-Identifier: Apache-2.0

package planning

import (
	"bytes"
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
	if len(definition.DerivedStates) != 7 || len(definition.AllowedActions) != 12 || len(definition.ConfigurationKeys) != 7 {
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
