// SPDX-License-Identifier: Apache-2.0

package configuration

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	"github.com/mcuadros/director-engine/internal/testkit/configoracle"
)

func intPointer(value int) *int       { return &value }
func int64Pointer(value int64) *int64 { return &value }
func boolPointer(value bool) *bool    { return &value }

func configurationDocument(t *testing.T, mutate func(*domainconfig.Configuration)) domainconfig.Document {
	t.Helper()
	document, err := domainconfig.Parse(configurationJSON("Director"))
	if err != nil {
		t.Fatal(err)
	}
	configuration := document.Configuration()
	mutate(&configuration)
	encoded, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	document, err = domainconfig.Parse(encoded)
	if err != nil {
		t.Fatalf("Parse(mutated configuration) error = %v\n%s", err, encoded)
	}
	return document
}

func approvedEnvelope(t *testing.T, document domainconfig.Document) SecurityEnvelope {
	t.Helper()
	envelope, err := NewSecurityEnvelope(document, HumanConfirmation{
		ActorKind: ActorHuman, ActorID: "human:owner",
		Revision: document.SHA256(), Confirmed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func TestSecurityEnvelopeRequiresExactHumanApproval(t *testing.T) {
	document := configurationDocument(t, func(*domainconfig.Configuration) {})
	tests := []HumanConfirmation{
		{},
		{ActorKind: ActorOrganizer, ActorID: "organizer:1", Revision: document.SHA256(), Confirmed: true},
		{ActorKind: ActorModel, ActorID: "model:1", Revision: document.SHA256(), Confirmed: true},
		{ActorKind: ActorHuman, ActorID: "human:owner", Revision: strings.Repeat("0", 64), Confirmed: true},
		{ActorKind: ActorHuman, ActorID: "human:owner", Revision: document.SHA256(), Confirmed: false},
	}
	for index, confirmation := range tests {
		if envelope, err := NewSecurityEnvelope(document, confirmation); !errors.Is(err, ErrSecurityEnvelopeInvalid) || envelope.SHA256() != "" {
			t.Fatalf("case %d envelope = %#v, error = %v", index, envelope, err)
		}
	}
	if _, err := NewState(SecurityEnvelope{}); !errors.Is(err, ErrSecurityEnvelopeInvalid) {
		t.Fatalf("NewState(empty envelope) error = %v", err)
	}
}

func TestProductionInheritanceMatchesIndependentOracle(t *testing.T) {
	boundary := configurationDocument(t, func(configuration *domainconfig.Configuration) {
		configuration.Defaults.Limits.MaxSubagentsPerTask = 2
		configuration.WorkspaceOverrides = []domainconfig.WorkspaceOverride{}
	})
	envelope := approvedEnvelope(t, boundary)
	type choice struct {
		inherit bool
		value   int
	}
	projectChoices := []choice{{value: 0}, {value: 1}, {value: 2}}
	overrideChoices := []choice{{inherit: true}, {value: 0}, {value: 1}, {value: 2}}
	cases := 0
	for _, project := range projectChoices {
		for _, workspace := range overrideChoices {
			for _, task := range overrideChoices {
				name := fmt.Sprintf("p%d-w%d-%t-t%d-%t", project.value, workspace.value, workspace.inherit, task.value, task.inherit)
				t.Run(name, func(t *testing.T) {
					document := configurationDocument(t, func(configuration *domainconfig.Configuration) {
						configuration.Defaults.Limits.MaxSubagentsPerTask = project.value
						configuration.WorkspaceOverrides = []domainconfig.WorkspaceOverride{}
						if !workspace.inherit {
							configuration.WorkspaceOverrides = append(configuration.WorkspaceOverrides, domainconfig.WorkspaceOverride{
								WorkspaceID: "product", LaunchPolicy: domainconfig.LaunchInherit,
								DeliveryMode: domainconfig.DeliveryInherit, MaxSubagentsPerTask: intPointer(workspace.value),
							})
						}
					})
					taskOverride := TaskOverride{}
					if !task.inherit {
						taskOverride.MaxSubagentsPerTask = intPointer(task.value)
					}
					got, err := envelope.ResolveEffective(document, "product", taskOverride)
					if err != nil {
						t.Fatal(err)
					}

					oracleConfiguration := configoracle.NewConfiguration(configoracle.Concrete(project.value))
					if !workspace.inherit {
						oracleConfiguration = oracleConfiguration.WithWorkspace(configoracle.Concrete(workspace.value))
					}
					if !task.inherit {
						oracleConfiguration = oracleConfiguration.WithTask(configoracle.Concrete(task.value))
					}
					want := configoracle.Resolve(oracleConfiguration, configoracle.NewEnvelope(0, 2))
					if !want.Allowed() || got.Limits.MaxSubagentsPerTask != want.Value() || string(got.Sources.MaxSubagentsPerTask) != string(want.Source()) {
						t.Fatalf("effective = %d from %s; oracle = %d from %s (%s)", got.Limits.MaxSubagentsPerTask, got.Sources.MaxSubagentsPerTask, want.Value(), want.Source(), want.Refusal())
					}
				})
				cases++
			}
		}
	}
	if cases != 48 {
		t.Fatalf("inheritance cases = %d, want 48", cases)
	}
}

func TestAllEffectiveFieldsReportTheirSupplyingScope(t *testing.T) {
	document := configurationDocument(t, func(configuration *domainconfig.Configuration) {
		configuration.Defaults.RunBudget.CostMicrousd = 5_000_000
		configuration.Defaults.RequireDifferentReviewerModel = true
		configuration.WorkspaceOverrides = []domainconfig.WorkspaceOverride{{
			WorkspaceID: "product", LaunchPolicy: domainconfig.LaunchAutomatic,
			DeliveryMode:   domainconfig.DeliveryInherit,
			MaxActiveTasks: intPointer(3), ElapsedSeconds: int64Pointer(3600),
			CostMicrousd:                  int64Pointer(2_000_000),
			AutoFixCIFailures:             boolPointer(false),
			RequireDifferentReviewerModel: boolPointer(true),
		}}
	})
	envelopeDocument := configurationDocument(t, func(configuration *domainconfig.Configuration) {
		configuration.Defaults.LaunchPolicy = domainconfig.LaunchAutomatic
		configuration.Defaults.DeliveryMode = domainconfig.DeliveryDirect
		configuration.Defaults.RunBudget.CostMicrousd = 5_000_000
		configuration.Defaults.RequireDifferentReviewerModel = true
	})
	effective, err := approvedEnvelope(t, envelopeDocument).ResolveEffective(document, "product", TaskOverride{
		DeliveryMode:               domainconfig.DeliveryPullRequest,
		MaxActiveTasksPerWorkspace: intPointer(1),
		MaxConcurrentAgents:        intPointer(6),
		MaxSubagentsPerTask:        intPointer(2),
		Tokens:                     int64Pointer(100000), Turns: int64Pointer(16), CICycles: int64Pointer(2),
		CostMicrousd:          int64Pointer(1_000_000),
		AutoFixReviewFeedback: boolPointer(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	if effective.Sources.LaunchPolicy != ScopeWorkspace || effective.Sources.DeliveryMode != ScopeTask ||
		effective.Sources.MaxActiveTasks != ScopeWorkspace || effective.Sources.ElapsedSeconds != ScopeWorkspace ||
		effective.Sources.MaxActiveTasksPerWorkspace != ScopeTask || effective.Sources.Tokens != ScopeTask ||
		effective.Sources.CostMicrousd != ScopeTask ||
		effective.Sources.AutoFixCIFailures != ScopeWorkspace || effective.Sources.AutoFixReviewFeedback != ScopeTask ||
		effective.Sources.RequireDifferentReviewerModel != ScopeWorkspace {
		t.Fatalf("effective sources = %#v", effective.Sources)
	}
	if effective.Limits.MaxActiveTasks != 3 || effective.Limits.MaxActiveTasksPerWorkspace != 1 ||
		effective.Limits.MaxConcurrentAgents != 6 || effective.Limits.MaxSubagentsPerTask != 2 ||
		effective.RunBudget.ElapsedSeconds != 3600 || effective.RunBudget.Tokens != 100000 ||
		effective.RunBudget.Turns != 16 || effective.RunBudget.CICycles != 2 ||
		effective.RunBudget.CostMicrousd != 1_000_000 ||
		effective.AutoFixCIFailures || effective.AutoFixReviewFeedback || !effective.RequireDifferentReviewerModel {
		t.Fatalf("effective values = %#v", effective)
	}
	if !effective.ReviewPolicy().RequireDifferentReviewerModel {
		t.Fatal("effective Reviewer policy did not freeze the inherited requirement")
	}
}

func TestDifferentReviewerModelPolicyMayTightenButNotLoosenTheEnvelope(t *testing.T) {
	optional := configurationDocument(t, func(configuration *domainconfig.Configuration) {
		configuration.Defaults.RequireDifferentReviewerModel = false
	})
	required := configurationDocument(t, func(configuration *domainconfig.Configuration) {
		configuration.Defaults.RequireDifferentReviewerModel = true
	})
	if _, err := approvedEnvelope(t, optional).ResolveEffective(required, "product", TaskOverride{}); err != nil {
		t.Fatalf("requiring a different Reviewer model did not tighten the envelope: %v", err)
	}
	if _, err := approvedEnvelope(t, required).ResolveEffective(optional, "product", TaskOverride{}); !errors.Is(err, ErrOutsideSecurityEnvelope) {
		t.Fatalf("loosening the different-Reviewer requirement error = %v", err)
	}
	if _, err := approvedEnvelope(t, required).ResolveEffective(required, "product", TaskOverride{RequireDifferentReviewerModel: boolPointer(false)}); !errors.Is(err, ErrOutsideSecurityEnvelope) {
		t.Fatalf("Task override loosened the different-Reviewer requirement: %v", err)
	}
}

func TestOptionalCostLimitMayTightenButNotBeRemovedOrRaised(t *testing.T) {
	unlimited := configurationDocument(t, func(*domainconfig.Configuration) {})
	finite := configurationDocument(t, func(configuration *domainconfig.Configuration) {
		configuration.Defaults.RunBudget.CostMicrousd = 5_000_000
	})
	if _, err := approvedEnvelope(t, unlimited).ResolveEffective(finite, "product", TaskOverride{}); err != nil {
		t.Fatalf("finite cost did not tighten an unlimited envelope: %v", err)
	}
	finiteEnvelope := approvedEnvelope(t, finite)
	if _, err := finiteEnvelope.ResolveEffective(unlimited, "product", TaskOverride{}); !errors.Is(err, ErrOutsideSecurityEnvelope) {
		t.Fatalf("removing finite cost error = %v", err)
	}
	if _, err := finiteEnvelope.ResolveEffective(finite, "product", TaskOverride{CostMicrousd: int64Pointer(6_000_000)}); !errors.Is(err, ErrOutsideSecurityEnvelope) {
		t.Fatalf("raising finite cost error = %v", err)
	}
	if effective, err := finiteEnvelope.ResolveEffective(finite, "product", TaskOverride{CostMicrousd: int64Pointer(1_000_000)}); err != nil || effective.RunBudget.CostMicrousd != 1_000_000 {
		t.Fatalf("tightened Task cost = %#v, %v", effective.RunBudget, err)
	}
}

func TestPreviewAndTaskResolutionFailClosedOutsideSecurityEnvelope(t *testing.T) {
	boundary := configurationDocument(t, func(configuration *domainconfig.Configuration) {
		configuration.Defaults.Limits.MaxSubagentsPerTask = 3
		configuration.Defaults.AutoFixCIFailures = false
	})
	state, err := NewState(approvedEnvelope(t, boundary))
	if err != nil {
		t.Fatal(err)
	}
	expansion := configurationDocument(t, func(configuration *domainconfig.Configuration) {
		configuration.Defaults.Limits.MaxSubagentsPerTask = 4
	})
	next, preview, err := state.Preview(PreviewCommand{
		ExpectedVersion: 0, OrganizerRevision: strings.Repeat("a", 40),
		ConfigurationJSON: expansion.CanonicalJSON(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Valid || len(preview.Issues) != 1 || preview.Issues[0].Code != "security_envelope_expansion" || preview.SecurityEnvelopeSHA256 == "" {
		t.Fatalf("expansion Preview = %#v", preview)
	}
	if _, err := next.Apply(confirmed(preview, "human:owner")); !errors.Is(err, ErrPendingRevisionInvalid) {
		t.Fatalf("Apply(expansion) error = %v", err)
	}

	if _, err := approvedEnvelope(t, boundary).ResolveEffective(boundary, "product", TaskOverride{
		MaxSubagentsPerTask: intPointer(4),
	}); !errors.Is(err, ErrOutsideSecurityEnvelope) {
		t.Fatalf("outside-envelope Task override error = %v", err)
	}
	if _, err := approvedEnvelope(t, boundary).ResolveEffective(boundary, "product", TaskOverride{
		AutoFixCIFailures: boolPointer(true),
	}); !errors.Is(err, ErrOutsideSecurityEnvelope) {
		t.Fatalf("authority-expanding Task override error = %v", err)
	}
}

func TestApplyRejectsNonHumanStaleAndMissingBindings(t *testing.T) {
	state := newTestState(t)
	state, preview, err := state.Preview(PreviewCommand{
		ExpectedVersion: 0, OrganizerRevision: strings.Repeat("b", 40),
		ConfigurationJSON: configurationJSON("Director"),
	})
	if err != nil {
		t.Fatal(err)
	}
	valid := confirmed(preview, "human:owner")
	tests := []struct {
		name    string
		mutate  func(ApplyCommand) ApplyCommand
		wantErr error
	}{
		{name: "Organizer self-approval", mutate: func(command ApplyCommand) ApplyCommand {
			command.Confirmation.ActorKind = ActorOrganizer
			return command
		}, wantErr: ErrHumanApprovalRequired},
		{name: "model approval", mutate: func(command ApplyCommand) ApplyCommand { command.Confirmation.ActorKind = ActorModel; return command }, wantErr: ErrHumanApprovalRequired},
		{name: "stale approval", mutate: func(command ApplyCommand) ApplyCommand {
			command.Confirmation.Revision = strings.Repeat("c", 40)
			return command
		}, wantErr: ErrApprovalRevisionMismatch},
		{name: "missing acknowledgement", mutate: func(command ApplyCommand) ApplyCommand { command.Acknowledgement = HumanConfirmation{}; return command }, wantErr: ErrHumanAcknowledgementRequired},
		{name: "Organizer acknowledgement", mutate: func(command ApplyCommand) ApplyCommand {
			command.Acknowledgement.ActorKind = ActorOrganizer
			return command
		}, wantErr: ErrHumanAcknowledgementRequired},
		{name: "stale acknowledgement", mutate: func(command ApplyCommand) ApplyCommand {
			command.Acknowledgement.Revision = strings.Repeat("d", 40)
			return command
		}, wantErr: ErrAcknowledgementRevisionMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			after, err := state.Apply(test.mutate(valid))
			if !errors.Is(err, test.wantErr) || after.Version() != state.Version() {
				t.Fatalf("Apply() = version %d, error %v; want unchanged and %v", after.Version(), err, test.wantErr)
			}
			if _, active := after.Active(); active {
				t.Fatal("rejected Apply activated Organizer revision")
			}
		})
	}
}

func TestActiveRevisionAloneFeedsFutureRuns(t *testing.T) {
	state := newTestState(t)
	state, first, err := state.Preview(PreviewCommand{
		ExpectedVersion: 0, OrganizerRevision: strings.Repeat("1", 40),
		ConfigurationJSON: configurationJSON("Director"),
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = state.Apply(confirmed(first, "human:owner"))
	if err != nil {
		t.Fatal(err)
	}
	existing, err := state.FreezeRunConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	state, pending, err := state.Preview(PreviewCommand{
		ExpectedVersion: state.Version(), OrganizerRevision: strings.Repeat("2", 40),
		ConfigurationJSON: configurationJSON("Director Next"),
	})
	if err != nil || !pending.Valid {
		t.Fatalf("Preview(next) = %#v, %v", pending, err)
	}
	whilePending, err := state.FreezeRunConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(existing.Configuration(), whilePending.Configuration()) || existing.OrganizerRevision() != whilePending.OrganizerRevision() {
		t.Fatal("pending revision changed future Run input")
	}
	state, err = state.Apply(confirmed(pending, "human:owner"))
	if err != nil {
		t.Fatal(err)
	}
	future, err := state.FreezeRunConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	if future.OrganizerRevision() != strings.Repeat("2", 40) || future.Configuration().Project.Name != "Director Next" || existing.Configuration().Project.Name != "Director" {
		t.Fatalf("existing/future revisions = %s/%s", existing.OrganizerRevision(), future.OrganizerRevision())
	}
	serialized, err := json.Marshal(future)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := ParseRunConfigurationSnapshot(serialized)
	if err != nil || restored.envelope.SHA256() != future.envelope.SHA256() || restored.Configuration().Project.Name != "Director Next" {
		t.Fatalf("tightened snapshot envelope round trip = %#v, %v", restored, err)
	}
}
