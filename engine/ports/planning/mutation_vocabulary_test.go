// SPDX-License-Identifier: Apache-2.0

package planning

import "testing"

func TestAdvertisedPlanningIntentIsNotRejectedAsExecutionControlOnly(t *testing.T) {
	hash, err := SchemaSHA256()
	if err != nil {
		t.Fatal(err)
	}
	input := MutationInput{
		SchemaVersion: 1, ContractVersion: "director-planning/v1", ContractHash: hash,
		RequestID: "planning-task-create-0001", IdempotencyKey: "planning-task-create-0001",
		ExpectedVersion: "0",
		Intent: MutationIntent{
			Type: "task.create", ProjectID: "project-1", WorkspaceID: "workspace-1", Key: "DIR-1",
			Title: "Create the first Task", Objective: "Prove the production planning path",
			AcceptanceCriteria: []string{"The Task is durably recorded"}, Priority: stringPointerForMutationTest("normal"),
			Labels: []string{},
		},
	}
	if err := ValidateControlMutation(input); err != nil {
		t.Fatalf("advertised task.create intent was rejected as unwired: %v", err)
	}
}

func TestGoMutationValidatorClosesEveryGeneratedIntent(t *testing.T) {
	hash, err := SchemaSHA256()
	if err != nil {
		t.Fatal(err)
	}
	priority := "normal"
	approval, acknowledgement := "human-approval", "3"
	base := func(kind string, intent MutationIntent) MutationInput {
		return MutationInput{SchemaVersion: 1, ContractVersion: "director-planning/v1", ContractHash: hash,
			RequestID: "request-" + kind, IdempotencyKey: "request-" + kind, ExpectedVersion: "3", Intent: intent}
	}
	values := []MutationInput{
		base("project-create", MutationIntent{Type: "project.create", Name: "Project"}),
		base("project-update", MutationIntent{Type: "project.update", ProjectID: "project-1", Name: "Project"}),
		base("epic-create", MutationIntent{Type: "epic.create", ProjectID: "project-1", Key: "M6", Title: "Epic", Description: "Epic objective", Priority: nil, Labels: []string{}}),
		base("epic-update", MutationIntent{Type: "epic.update", EpicID: "epic-1", Title: "Epic", Description: "Epic objective", Priority: nil, Labels: []string{}}),
		base("task-create", MutationIntent{Type: "task.create", ProjectID: "project-1", WorkspaceID: "workspace-1", Key: "DIR-1", Title: "Task", Objective: "Objective", AcceptanceCriteria: []string{"Criterion"}, Priority: &priority, Labels: []string{}}),
		base("task-update", MutationIntent{Type: "task.update", TaskID: "task-1", Title: "Task", Objective: "Objective", AcceptanceCriteria: []string{"Criterion"}, Priority: &priority, Labels: []string{}}),
		base("dependency-add", MutationIntent{Type: "dependency.add", TaskID: "task-1", DependencyKind: "task", DependencyID: "task-2"}),
		base("dependency-remove", MutationIntent{Type: "dependency.remove", TaskID: "task-1", DependencyKind: "epic", DependencyID: "epic-1"}),
		base("configuration-preview", MutationIntent{Type: "configuration.preview", Target: &ConfigurationTarget{Scope: "task", ID: "task-1"}, Overrides: []ConfigurationOverride{}}),
		base("task-launch", MutationIntent{Type: "task.launch-now", TaskID: "task-1"}),
		base("project-pause", MutationIntent{Type: "project.pause", ProjectID: "project-1"}),
		base("project-resume", MutationIntent{Type: "project.resume", ProjectID: "project-1"}),
		base("emergency-prepare", MutationIntent{Type: "project.emergency-stop.prepare", ProjectID: "project-1"}),
		base("task-cancel", MutationIntent{Type: "task.cancel", ProjectID: "project-1", TaskID: "task-1", RunID: "run-1"}),
		base("task-integrate", MutationIntent{Type: "task.integrate", ProjectID: "project-1", TaskID: "task-1", RunID: "run-1"}),
		base("task-feedback", MutationIntent{Type: "task.feedback", ProjectID: "project-1", TaskID: "task-1", RunID: "run-1", Body: "Please cover restart recovery", Severity: "P2"}),
	}
	configurationApply := base("configuration-apply", MutationIntent{Type: "configuration.apply", Target: &ConfigurationTarget{Scope: "task", ID: "task-1"}, PreviewID: "preview-1"})
	configurationApply.HumanApprovalRef, configurationApply.AcknowledgementRevision = &approval, &acknowledgement
	values = append(values, configurationApply)
	override := base("dependency-override", MutationIntent{Type: "dependency.override", TaskID: "task-1", DependencyKind: "task", DependencyID: "task-2"})
	override.HumanApprovalRef, override.AcknowledgementRevision = &approval, &acknowledgement
	values = append(values, override)
	emergency := base("emergency-confirm", MutationIntent{Type: "project.emergency-stop.confirm", ProjectID: "project-1"})
	emergency.HumanApprovalRef = &approval
	values = append(values, emergency)
	for _, input := range values {
		if err := ValidateMutation(input); err != nil {
			t.Fatalf("generated intent %s rejected: %v", input.Intent.Type, err)
		}
	}
	unknown := base("unknown", MutationIntent{Type: "unadvertised.intent"})
	if ValidateMutation(unknown) == nil {
		t.Fatal("unadvertised planning intent was accepted")
	}
}

func stringPointerForMutationTest(value string) *string { return &value }
