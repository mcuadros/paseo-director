// SPDX-License-Identifier: Apache-2.0

package configuration

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
)

const validConfigurationTemplate = `{
  "$schema": "SCHEMA_ID",
  "schemaVersion": 1,
  "project": {"id": "director", "name": "PROJECT_NAME"},
  "workspaces": [{
    "id": "product",
    "remote": "https://github.com/example/product.git",
    "sourcePath": "/srv/director/product",
    "defaultBaseBranch": "main"
  }],
  "agentProfiles": {
    "taskAgent": {
      "provider": "codex",
      "model": "gpt-5.6",
      "effort": "high",
      "permissionMode": "workspace-write"
    },
    "reviewerAgent": {
      "provider": "opencode",
      "model": "reviewer-1",
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
  "workspaceOverrides": [],
  "skills": [{"id": "commits", "path": "skills/commits/SKILL.md"}],
  "templates": [{"id": "task", "path": "templates/task.md"}]
}`

func configurationJSON(projectName string) []byte {
	return []byte(strings.NewReplacer(
		"SCHEMA_ID", domainconfig.SchemaID,
		"PROJECT_NAME", projectName,
	).Replace(validConfigurationTemplate))
}

func confirmed(preview Preview, actor string) ApplyCommand {
	return ApplyCommand{
		ExpectedVersion: preview.AggregateVersion,
		PreviewID:       preview.ID,
		Confirmation: HumanConfirmation{
			ActorID:   actor,
			Confirmed: true,
		},
	}
}

func TestPreviewIsDeterministicAndDoesNotActivate(t *testing.T) {
	var initial State
	command := PreviewCommand{
		ExpectedVersion:   0,
		OrganizerRevision: strings.Repeat("a", 40),
		ConfigurationJSON: configurationJSON("Director"),
	}
	firstState, first, err := initial.Preview(command)
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	secondState, second, err := initial.Preview(command)
	if err != nil {
		t.Fatalf("second Preview() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("Preview() is not deterministic:\n%#v\n%#v", first, second)
	}
	if first.ID == "" || len(first.ID) != 64 || !first.Valid || first.AggregateVersion != 1 {
		t.Fatalf("Preview() = %#v", first)
	}
	if first.Impact.Kind != ImpactInitialActivation || !reflect.DeepEqual(first.Impact.ChangedSections, configurationSectionOrder) {
		t.Fatalf("Preview impact = %#v", first.Impact)
	}
	if _, ok := firstState.Active(); ok {
		t.Fatal("Preview activated the pending revision")
	}
	if _, err := firstState.FreezeRunConfiguration(); !errors.Is(err, ErrActiveRevisionMissing) {
		t.Fatalf("FreezeRunConfiguration() error = %v", err)
	}
	if pending, ok := firstState.Pending(); !ok || !reflect.DeepEqual(pending, first) {
		t.Fatalf("Pending() = %#v, %v", pending, ok)
	}
	first.Impact.ChangedSections[0] = "changed"
	if pending, _ := firstState.Pending(); pending.Impact.ChangedSections[0] != "project" {
		t.Fatal("Preview projection exposed mutable pending state")
	}
	if secondState.Version() != firstState.Version() {
		t.Fatalf("deterministic states have versions %d and %d", firstState.Version(), secondState.Version())
	}
	command.ConfigurationJSON[0] = '['
	if pending, _ := firstState.Pending(); !pending.Valid || pending.ConfigurationSHA256 != first.ConfigurationSHA256 {
		t.Fatal("Preview retained mutable command bytes")
	}
}

func TestApplyRequiresExactHumanApprovedValidPreview(t *testing.T) {
	var initial State
	state, preview, err := initial.Preview(PreviewCommand{
		ExpectedVersion:   0,
		OrganizerRevision: strings.Repeat("a", 40),
		ConfigurationJSON: configurationJSON("Director"),
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]struct {
		command ApplyCommand
		want    error
	}{
		"stale aggregate": {
			command: ApplyCommand{ExpectedVersion: 0, PreviewID: preview.ID},
			want:    ErrVersionConflict,
		},
		"wrong Preview": {
			command: ApplyCommand{ExpectedVersion: state.Version(), PreviewID: strings.Repeat("0", 64)},
			want:    ErrPreviewMismatch,
		},
		"missing confirmation": {
			command: ApplyCommand{ExpectedVersion: state.Version(), PreviewID: preview.ID},
			want:    ErrHumanApprovalRequired,
		},
		"empty human identity": {
			command: ApplyCommand{
				ExpectedVersion: state.Version(),
				PreviewID:       preview.ID,
				Confirmation:    HumanConfirmation{Confirmed: true},
			},
			want: ErrHumanApprovalRequired,
		},
		"unbounded human identity": {
			command: ApplyCommand{
				ExpectedVersion: state.Version(),
				PreviewID:       preview.ID,
				Confirmation: HumanConfirmation{
					ActorID:   strings.Repeat("a", 257),
					Confirmed: true,
				},
			},
			want: ErrHumanApprovalRequired,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			after, err := state.Apply(test.command)
			if !errors.Is(err, test.want) {
				t.Fatalf("Apply() error = %v, want %v", err, test.want)
			}
			if after.Version() != state.Version() {
				t.Fatalf("rejected Apply changed version to %d", after.Version())
			}
			if _, ok := after.Active(); ok {
				t.Fatal("rejected Apply activated a revision")
			}
		})
	}

	state, err = state.Apply(confirmed(preview, "human:user-1"))
	if err != nil {
		t.Fatalf("Apply(confirmed) error = %v", err)
	}
	active, ok := state.Active()
	if !ok || active.OrganizerRevision != preview.ProposedRevision || active.ConfigurationSHA256 != preview.ConfigurationSHA256 {
		t.Fatalf("Active() = %#v, %v", active, ok)
	}
	if state.Version() != 2 {
		t.Fatalf("applied state version = %d", state.Version())
	}
	if _, ok := state.Pending(); ok {
		t.Fatal("successful Apply retained a pending revision")
	}
}

func TestInvalidOrUnapprovedPendingRevisionCannotAffectRun(t *testing.T) {
	var state State
	state, firstPreview, err := state.Preview(PreviewCommand{
		ExpectedVersion:   state.Version(),
		OrganizerRevision: strings.Repeat("a", 40),
		ConfigurationJSON: configurationJSON("Director"),
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = state.Apply(confirmed(firstPreview, "human:user-1"))
	if err != nil {
		t.Fatal(err)
	}
	originalSnapshot, err := state.FreezeRunConfiguration()
	if err != nil {
		t.Fatal(err)
	}

	invalidJSON := bytes.Replace(configurationJSON("Invalid"), []byte(`"schemaVersion": 1`), []byte(`"schemaVersion": 2`), 1)
	state, invalidPreview, err := state.Preview(PreviewCommand{
		ExpectedVersion:   state.Version(),
		OrganizerRevision: strings.Repeat("b", 40),
		ConfigurationJSON: invalidJSON,
	})
	if err != nil {
		t.Fatal(err)
	}
	if invalidPreview.Valid || invalidPreview.Impact.Kind != ImpactInvalid || len(invalidPreview.Issues) == 0 {
		t.Fatalf("invalid Preview = %#v", invalidPreview)
	}
	if _, err := state.Apply(confirmed(invalidPreview, "human:user-1")); !errors.Is(err, ErrPendingRevisionInvalid) {
		t.Fatalf("Apply(invalid) error = %v", err)
	}
	whileInvalid, err := state.FreezeRunConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	if whileInvalid.OrganizerRevision() != originalSnapshot.OrganizerRevision() || whileInvalid.ConfigurationSHA256() != originalSnapshot.ConfigurationSHA256() {
		t.Fatal("invalid pending revision changed the Run configuration source")
	}

	state, validPending, err := state.Preview(PreviewCommand{
		ExpectedVersion:   state.Version(),
		OrganizerRevision: strings.Repeat("c", 40),
		ConfigurationJSON: configurationJSON("Director Next"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if validPending.Impact.Kind != ImpactConfigurationChange || !reflect.DeepEqual(validPending.Impact.ChangedSections, []string{"project"}) {
		t.Fatalf("changed Preview impact = %#v", validPending.Impact)
	}
	whileUnapproved, err := state.FreezeRunConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	if whileUnapproved.OrganizerRevision() != originalSnapshot.OrganizerRevision() {
		t.Fatal("unapproved pending revision changed the Run configuration source")
	}
	if rejected, err := state.Apply(ApplyCommand{ExpectedVersion: state.Version(), PreviewID: validPending.ID}); !errors.Is(err, ErrHumanApprovalRequired) || rejected.Version() != state.Version() {
		t.Fatalf("unapproved Apply = version %d, error %v", rejected.Version(), err)
	}

	state, err = state.Apply(confirmed(validPending, "human:user-1"))
	if err != nil {
		t.Fatal(err)
	}
	newSnapshot, err := state.FreezeRunConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	if newSnapshot.OrganizerRevision() != strings.Repeat("c", 40) || newSnapshot.ConfigurationSHA256() == originalSnapshot.ConfigurationSHA256() {
		t.Fatalf("new snapshot = revision %s hash %s", newSnapshot.OrganizerRevision(), newSnapshot.ConfigurationSHA256())
	}
	if originalSnapshot.OrganizerRevision() != strings.Repeat("a", 40) || originalSnapshot.Configuration().Project.Name != "Director" {
		t.Fatal("existing Run snapshot changed after a later Apply")
	}
}

func TestRunConfigurationSnapshotIsImmutableAndStrictlyRoundTrips(t *testing.T) {
	var state State
	state, preview, err := state.Preview(PreviewCommand{
		ExpectedVersion:   0,
		OrganizerRevision: strings.Repeat("d", 64),
		ConfigurationJSON: configurationJSON("Director"),
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = state.Apply(confirmed(preview, "human:user-1"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := state.FreezeRunConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	first, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("snapshot serialization is not deterministic:\n%s\n%s", first, second)
	}
	restored, err := ParseRunConfigurationSnapshot(first)
	if err != nil {
		t.Fatalf("ParseRunConfigurationSnapshot() error = %v", err)
	}
	if restored.OrganizerRevision() != snapshot.OrganizerRevision() || restored.ConfigurationSHA256() != snapshot.ConfigurationSHA256() {
		t.Fatalf("restored snapshot = %s %s", restored.OrganizerRevision(), restored.ConfigurationSHA256())
	}

	configuration := restored.Configuration()
	configuration.Workspaces[0].ID = "changed"
	configurationJSON := restored.ConfigurationJSON()
	configurationJSON[0] = '['
	if restored.Configuration().Workspaces[0].ID != "product" || restored.ConfigurationJSON()[0] != '{' {
		t.Fatal("snapshot accessors exposed mutable state")
	}

	tamperedConfiguration := bytes.Replace(first, []byte(`"name":"Director"`), []byte(`"name":"Changed"`), 1)
	if _, err := ParseRunConfigurationSnapshot(tamperedConfiguration); !errors.Is(err, ErrSnapshotInvalid) {
		t.Fatalf("tampered configuration error = %v", err)
	}
	unknownField := bytes.Replace(first, []byte{'{'}, []byte(`{"unexpected":true,`), 1)
	if _, err := ParseRunConfigurationSnapshot(unknownField); !errors.Is(err, ErrSnapshotInvalid) {
		t.Fatalf("unknown snapshot field error = %v", err)
	}
	duplicateField := bytes.Replace(first, []byte(`"snapshotVersion":`), []byte(`"snapshotVersion":"duplicate","snapshotVersion":`), 1)
	if _, err := ParseRunConfigurationSnapshot(duplicateField); !errors.Is(err, ErrSnapshotInvalid) {
		t.Fatalf("duplicate snapshot field error = %v", err)
	}
}

func TestRevisionCommandsFailClosedOnIdentityAndContentConflicts(t *testing.T) {
	var state State
	if after, _, err := state.Preview(PreviewCommand{
		ExpectedVersion:   1,
		OrganizerRevision: strings.Repeat("a", 40),
		ConfigurationJSON: configurationJSON("Director"),
	}); !errors.Is(err, ErrVersionConflict) || after.Version() != 0 {
		t.Fatalf("stale Preview = version %d, error %v", after.Version(), err)
	}
	if after, _, err := state.Preview(PreviewCommand{
		ExpectedVersion:   0,
		OrganizerRevision: strings.Repeat("A", 40),
		ConfigurationJSON: configurationJSON("Director"),
	}); !errors.Is(err, ErrRevisionInvalid) || after.Version() != 0 {
		t.Fatalf("invalid revision Preview = version %d, error %v", after.Version(), err)
	}

	state, preview, err := state.Preview(PreviewCommand{
		ExpectedVersion:   0,
		OrganizerRevision: strings.Repeat("a", 40),
		ConfigurationJSON: configurationJSON("Director"),
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = state.Apply(confirmed(preview, "human:user-1"))
	if err != nil {
		t.Fatal(err)
	}
	before := state.Version()
	_, noChange, err := state.Preview(PreviewCommand{
		ExpectedVersion:   before,
		OrganizerRevision: strings.Repeat("b", 40),
		ConfigurationJSON: configurationJSON("Director"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if noChange.Impact.Kind != ImpactNoConfigurationChange || len(noChange.Impact.ChangedSections) != 0 || noChange.ActiveConfigurationSHA256 == "" {
		t.Fatalf("no-change Preview = %#v", noChange)
	}
	after, _, err := state.Preview(PreviewCommand{
		ExpectedVersion:   before,
		OrganizerRevision: strings.Repeat("a", 40),
		ConfigurationJSON: configurationJSON("Different content"),
	})
	if !errors.Is(err, ErrRevisionContentConflict) || after.Version() != before {
		t.Fatalf("content conflict Preview = version %d, error %v", after.Version(), err)
	}
}
