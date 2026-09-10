// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/mcuadros/director-engine/domain/agentbridge"
)

func TestRenderIsCanonicalAndContainsEveryClosedTool(t *testing.T) {
	original, err := render(agentbridge.Schema())
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(agentbridge.Schema(), &value); err != nil {
		t.Fatal(err)
	}
	reordered, err := json.MarshalIndent(value, "", "       ")
	if err != nil {
		t.Fatal(err)
	}
	second, err := render(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, second) {
		t.Fatal("generation changed after schema formatting changed")
	}
	for _, name := range []string{
		"director_project_read", "director_planning_command_submit", "director_task_read",
		"director_task_outcome_submit", "director_candidate_read", "director_review_verdict_submit",
	} {
		if !bytes.Contains(original, []byte(name)) {
			t.Errorf("generated client omits %s", name)
		}
	}
}
