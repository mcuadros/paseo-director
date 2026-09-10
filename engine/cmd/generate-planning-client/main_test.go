// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"os"
	"testing"
)

func TestRenderIsDeterministicAndCarriesClosedPlanningSurface(t *testing.T) {
	schema, err := os.ReadFile("../../ports/planning/planning-surface.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	first, err := render(schema)
	if err != nil {
		t.Fatal(err)
	}
	second, err := render(schema)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("planning client generation is not deterministic")
	}
	for _, expected := range [][]byte{
		[]byte(`PLANNING_CONTRACT_VERSION = "director-planning/v1"`),
		[]byte(`PLANNING_CONTRACT_SHA256`),
		[]byte(`PLANNING_QUERY_PATH = "/v1/planning/query"`),
		[]byte(`planningPageCursorSchema`),
		[]byte(`planningSnapshotSchema`),
		[]byte(`taskDetailSnapshotSchema`),
		[]byte(`planningMutationInputSchema`),
		[]byte(`interface PlanningClient`),
		[]byte(`bindPlanningMutation`),
	} {
		if !bytes.Contains(first, expected) {
			t.Fatalf("generated planning client does not contain %q", expected)
		}
	}
}

func TestRenderRejectsOpenPlanningObject(t *testing.T) {
	schema, err := os.ReadFile("../../ports/planning/planning-surface.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(schema, []byte(`"additionalProperties": false`), []byte(`"additionalProperties": true`), 1)
	if _, err := render(changed); err == nil {
		t.Fatal("planning generator accepted an open object")
	}
}
