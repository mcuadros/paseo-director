// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"

	"github.com/mcuadros/director-engine/domain"
)

func TestPrimaryTaskPromptIsExactBoundedAndNamesTheClosedOutcome(t *testing.T) {
	task := domain.Task{ID: "task-1", Title: "Focused change", Objective: "Create RESULT.md",
		AcceptanceCriteria: "RESULT.md exists\nThe worktree is clean"}
	prompt := primaryTaskPrompt(task)
	for _, expected := range []string{"Director Task task-1", "Create RESULT.md", "RESULT.md exists", "director_task_read",
		"director_task_outcome_submit exactly once", "Do not push, publish, merge"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("prompt omitted %q", expected)
		}
	}
	large := task
	large.AcceptanceCriteria = strings.Repeat("bounded criterion\n", 2_000)
	bounded := primaryTaskPrompt(large)
	if len(bounded) > 16*1024 || strings.Count(bounded, "bounded criterion") != 0 || !strings.Contains(bounded, "exceed the inline bound") {
		t.Fatalf("large prompt length=%d", len(bounded))
	}
}
