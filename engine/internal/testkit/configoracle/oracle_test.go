// SPDX-License-Identifier: Apache-2.0

package configoracle

import (
	"fmt"
	"reflect"
	"testing"
)

type seededSequence struct {
	state uint64
}

func (sequence *seededSequence) intn(bound int) int {
	sequence.state = sequence.state*6364136223846793005 + 1442695040888963407
	return int((sequence.state >> 32) % uint64(bound))
}

func assertEffective(
	t *testing.T,
	got Effective,
	wantValue int,
	wantSource Scope,
	wantRefusal RefusalCode,
) {
	t.Helper()
	if got.Refusal() != wantRefusal {
		t.Fatalf("Refusal() = %q, want %q; effective = %#v", got.Refusal(), wantRefusal, got)
	}
	if wantRefusal == RefusalNone {
		if !got.Allowed() || got.Value() != wantValue || got.Source() != wantSource {
			t.Fatalf(
				"effective = (value %d, source %q, allowed %v), want (%d, %q, true)",
				got.Value(), got.Source(), got.Allowed(), wantValue, wantSource,
			)
		}
	} else if got.Allowed() {
		t.Fatalf("refused effective reports Allowed(): %#v", got)
	}
}

func TestExhaustiveBoundedInheritanceMatrix(t *testing.T) {
	type choice struct {
		selection Selection
		concrete  bool
		value     int
	}
	choices := []choice{
		{selection: Inherit()},
		{selection: Concrete(0), concrete: true, value: 0},
		{selection: Concrete(1), concrete: true, value: 1},
		{selection: Concrete(2), concrete: true, value: 2},
	}
	envelope := NewEnvelope(0, 2)
	cases := 0
	for projectIndex, project := range choices {
		for workspaceIndex, workspace := range choices {
			for taskIndex, task := range choices {
				name := fmt.Sprintf("p%d-w%d-t%d", projectIndex, workspaceIndex, taskIndex)
				t.Run(name, func(t *testing.T) {
					configuration := NewConfiguration(project.selection).
						WithWorkspace(workspace.selection).
						WithTask(task.selection)
					got := Resolve(configuration, envelope)
					if !project.concrete {
						assertEffective(t, got, 0, ScopeNone, RefusalProjectValueRequired)
						return
					}
					wantValue, wantSource := project.value, ScopeProject
					if workspace.concrete {
						wantValue, wantSource = workspace.value, ScopeWorkspace
					}
					if task.concrete {
						wantValue, wantSource = task.value, ScopeTask
					}
					assertEffective(t, got, wantValue, wantSource, RefusalNone)
				})
				cases++
			}
		}
	}
	if cases != 64 {
		t.Fatalf("matrix cases = %d, want 64", cases)
	}
}

func TestExhaustiveBoundedEnvelopeMatrix(t *testing.T) {
	values := []int{-1, 0, 1, 2, 3}
	envelope := NewEnvelope(0, 2)
	cases := 0
	for _, project := range values {
		for _, workspace := range values {
			for _, task := range values {
				configuration := NewConfiguration(Concrete(project)).
					WithWorkspace(Concrete(workspace)).
					WithTask(Concrete(task))
				got := Resolve(configuration, envelope)
				want := RefusalNone
				if project < 0 || project > 2 || workspace < 0 || workspace > 2 || task < 0 || task > 2 {
					want = RefusalOutsideSecurityEnvelope
				}
				assertEffective(t, got, task, ScopeTask, want)
				cases++
			}
		}
	}
	if cases != 125 {
		t.Fatalf("matrix cases = %d, want 125", cases)
	}
}

func TestPreviewIsRepeatableOrderedAndDoesNotActivate(t *testing.T) {
	active := NewConfiguration(Concrete(1)).WithWorkspace(Inherit()).WithTask(Inherit())
	proposal := NewConfiguration(Concrete(2)).WithWorkspace(Concrete(3)).WithTask(Inherit())
	facts := NewFacts(NewEnvelope(0, 3)).
		WithActive("revision-1", active).
		WithProposal("revision-2", proposal, true)

	first := PreviewConfiguration(facts)
	if !first.Valid() || first.ProposedRevision() != "revision-2" {
		t.Fatalf("PreviewConfiguration() = %#v", first)
	}
	assertEffective(t, first.EffectiveBefore(), 1, ScopeProject, RefusalNone)
	assertEffective(t, first.EffectiveAfter(), 3, ScopeWorkspace, RefusalNone)
	diff := first.Diff()
	if len(diff) != 2 || diff[0].Scope() != ScopeProject || diff[1].Scope() != ScopeWorkspace {
		t.Fatalf("Diff() order = %#v", diff)
	}

	for iteration := 0; iteration < 100; iteration++ {
		if next := PreviewConfiguration(facts); !reflect.DeepEqual(first, next) {
			t.Fatalf("Preview iteration %d changed:\n%#v\n%#v", iteration, first, next)
		}
	}
	diff[0] = DiffEntry{scope: ScopeTask, after: Concrete(99)}
	if fresh := first.Diff(); fresh[0].Scope() != ScopeProject {
		t.Fatalf("Diff() exposed Preview storage: %#v", fresh)
	}
	if revision, ok := facts.ActiveRevision(); !ok || revision != "revision-1" {
		t.Fatalf("Preview changed active revision to %q, %v", revision, ok)
	}
	_, beforeApply := FreezeRun(facts)
	if beforeApply.ActiveRevision() != "revision-1" || beforeApply.Effective().Value() != 1 {
		t.Fatalf("Preview changed future Run input: %#v", beforeApply)
	}
}

func activationFixture() (Facts, Preview) {
	active := NewConfiguration(Concrete(1))
	proposal := NewConfiguration(Concrete(2)).WithWorkspace(Concrete(3))
	facts := NewFacts(NewEnvelope(0, 4)).
		WithActive("revision-1", active).
		WithProposal("revision-2", proposal, true)
	return facts, PreviewConfiguration(facts)
}

func confirmedActivation(facts Facts) Facts {
	return facts.
		WithApproval(ActorHuman, "human-1", "revision-2", true).
		WithAcknowledgement(ActorHuman, "human-1", "revision-1", true)
}

func TestApplyMutationMatrixRequiresExactHumanApprovalAndAcknowledgement(t *testing.T) {
	base, preview := activationFixture()
	tests := []struct {
		name    string
		facts   Facts
		preview Preview
		want    RefusalCode
	}{
		{name: "missing Preview", facts: confirmedActivation(base), preview: Preview{}, want: RefusalPreviewRequired},
		{name: "missing approval", facts: base.WithAcknowledgement(ActorHuman, "human-1", "revision-1", true), preview: preview, want: RefusalHumanApprovalRequired},
		{name: "unconfirmed approval", facts: base.WithApproval(ActorHuman, "human-1", "revision-2", false).WithAcknowledgement(ActorHuman, "human-1", "revision-1", true), preview: preview, want: RefusalHumanApprovalInvalid},
		{name: "Organizer approval", facts: base.WithApproval(ActorOrganizer, "organizer-1", "revision-2", true).WithAcknowledgement(ActorHuman, "human-1", "revision-1", true), preview: preview, want: RefusalHumanApprovalInvalid},
		{name: "model approval", facts: base.WithApproval(ActorModel, "model-1", "revision-2", true).WithAcknowledgement(ActorHuman, "human-1", "revision-1", true), preview: preview, want: RefusalHumanApprovalInvalid},
		{name: "stale approval revision", facts: base.WithApproval(ActorHuman, "human-1", "revision-old", true).WithAcknowledgement(ActorHuman, "human-1", "revision-1", true), preview: preview, want: RefusalApprovalRevisionMismatch},
		{name: "missing acknowledgement", facts: base.WithApproval(ActorHuman, "human-1", "revision-2", true), preview: preview, want: RefusalHumanAcknowledgementRequired},
		{name: "Organizer acknowledgement", facts: base.WithApproval(ActorHuman, "human-1", "revision-2", true).WithAcknowledgement(ActorOrganizer, "organizer-1", "revision-1", true), preview: preview, want: RefusalHumanAcknowledgementInvalid},
		{name: "stale acknowledgement revision", facts: base.WithApproval(ActorHuman, "human-1", "revision-2", true).WithAcknowledgement(ActorHuman, "human-1", "revision-old", true), preview: preview, want: RefusalAcknowledgementRevisionMismatch},
		{name: "changed proposal after Preview", facts: confirmedActivation(base.WithProposal("revision-2", NewConfiguration(Concrete(4)), true)), preview: preview, want: RefusalPreviewStale},
		{name: "changed active revision after Preview", facts: confirmedActivation(base.WithActive("revision-other", NewConfiguration(Concrete(1)))), preview: preview, want: RefusalPreviewStale},
		{name: "changed one-off facts after Preview", facts: confirmedActivation(base.WithOneOffOverride(ActorHuman, "human-1", "revision-2", ScopeTask, 3, true, true)), preview: preview, want: RefusalPreviewStale},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := test.facts
			after, result := ApplyConfiguration(test.facts, test.preview)
			if result.Refusal() != test.want || result.Applied() {
				t.Fatalf("ApplyConfiguration() = %#v, want refusal %q", result, test.want)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("refused Apply mutated facts:\n%#v\n%#v", before, after)
			}
		})
	}

	approved := confirmedActivation(base)
	next, result := ApplyConfiguration(approved, preview)
	if !result.Applied() || result.Refusal() != RefusalNone || result.ActiveRevision() != "revision-2" {
		t.Fatalf("approved ApplyConfiguration() = %#v", result)
	}
	if revision, ok := next.ActiveRevision(); !ok || revision != "revision-2" {
		t.Fatalf("active revision = %q, %v", revision, ok)
	}
	if revision, _ := base.ActiveRevision(); revision != "revision-1" {
		t.Fatalf("value-returning Apply mutated original active revision to %q", revision)
	}
}

func TestInitialActivationRequiresAndAcceptsExactEmptyAcknowledgement(t *testing.T) {
	configuration := NewConfiguration(Concrete(2))
	facts := NewFacts(NewEnvelope(0, 3)).WithProposal("revision-1", configuration, true)
	preview := PreviewConfiguration(facts)
	if !preview.Valid() || preview.EffectiveBefore().Refusal() != RefusalActiveRevisionRequired {
		t.Fatalf("initial Preview = %#v", preview)
	}
	facts = facts.
		WithApproval(ActorHuman, "human-1", "revision-1", true).
		WithAcknowledgement(ActorHuman, "human-1", "", true)
	next, result := ApplyConfiguration(facts, preview)
	if !result.Applied() {
		t.Fatalf("initial Apply refusal = %q", result.Refusal())
	}
	_, run := FreezeRun(next)
	assertEffective(t, run.Effective(), 2, ScopeProject, RefusalNone)
}

func TestInvalidPreviewFactsFailClosed(t *testing.T) {
	validConfiguration := NewConfiguration(Concrete(1))
	tests := []struct {
		name  string
		facts Facts
		want  RefusalCode
	}{
		{name: "missing proposal", facts: NewFacts(NewEnvelope(0, 2)), want: RefusalProposalRequired},
		{name: "empty proposal revision", facts: NewFacts(NewEnvelope(0, 2)).WithProposal("", validConfiguration, true), want: RefusalProposalRevisionInvalid},
		{name: "invalid proposal", facts: NewFacts(NewEnvelope(0, 2)).WithProposal("revision-1", validConfiguration, false), want: RefusalProposalInvalid},
		{name: "invalid active revision", facts: NewFacts(NewEnvelope(0, 2)).WithActive("", validConfiguration).WithProposal("revision-2", validConfiguration, true), want: RefusalActiveRevisionInvalid},
		{name: "invalid selection", facts: NewFacts(NewEnvelope(0, 2)).WithProposal("revision-1", Configuration{}, true), want: RefusalInvalidSelection},
		{name: "Project inherit", facts: NewFacts(NewEnvelope(0, 2)).WithProposal("revision-1", NewConfiguration(Inherit()), true), want: RefusalProjectValueRequired},
		{name: "missing envelope", facts: NewFacts(Envelope{}).WithProposal("revision-1", validConfiguration, true), want: RefusalInvalidSecurityEnvelope},
		{name: "reversed envelope", facts: NewFacts(NewEnvelope(2, 1)).WithProposal("revision-1", validConfiguration, true), want: RefusalInvalidSecurityEnvelope},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			preview := PreviewConfiguration(test.facts)
			if preview.Refusal() != test.want || preview.Valid() {
				t.Fatalf("PreviewConfiguration() refusal = %q, want %q", preview.Refusal(), test.want)
			}
		})
	}
}

func TestActivationChangesOnlyFutureRuns(t *testing.T) {
	active := NewConfiguration(Concrete(1)).WithWorkspace(Concrete(2))
	proposal := NewConfiguration(Concrete(3)).WithTask(Concrete(4))
	facts := NewFacts(NewEnvelope(0, 4)).WithActive("revision-1", active)
	_, existingRun := FreezeRun(facts)

	proposed := facts.WithProposal("revision-2", proposal, true)
	preview := PreviewConfiguration(proposed)
	_, whilePending := FreezeRun(proposed)
	if whilePending.ActiveRevision() != "revision-1" || whilePending.Effective().Value() != 2 {
		t.Fatalf("pending proposal changed Run input: %#v", whilePending)
	}
	approved := proposed.
		WithApproval(ActorHuman, "human-1", "revision-2", true).
		WithAcknowledgement(ActorHuman, "human-1", "revision-1", true)
	applied, result := ApplyConfiguration(approved, preview)
	if !result.Applied() {
		t.Fatalf("ApplyConfiguration() refusal = %q", result.Refusal())
	}
	_, futureRun := FreezeRun(applied)

	if existingRun.ActiveRevision() != "revision-1" || existingRun.Effective().Value() != 2 || existingRun.Effective().Source() != ScopeWorkspace {
		t.Fatalf("existing Run changed after Apply: %#v", existingRun)
	}
	if futureRun.ActiveRevision() != "revision-2" || futureRun.Effective().Value() != 4 || futureRun.Effective().Source() != ScopeTask {
		t.Fatalf("future Run did not use applied revision: %#v", futureRun)
	}
}

func TestOneOffOverrideIsExactAuditedConsumableAndDoesNotExpandEnvelope(t *testing.T) {
	envelope := NewEnvelope(0, 3)
	active := NewConfiguration(Concrete(1))
	proposal := NewConfiguration(Concrete(1)).WithTask(Concrete(4))
	base := NewFacts(envelope).
		WithActive("revision-1", active).
		WithProposal("revision-2", proposal, true)
	withoutOverride := PreviewConfiguration(base)
	if withoutOverride.Refusal() != RefusalOutsideSecurityEnvelope {
		t.Fatalf("outside-envelope Preview refusal = %q", withoutOverride.Refusal())
	}

	tests := []struct {
		name      string
		kind      ActorKind
		actorID   string
		revision  string
		scope     Scope
		value     int
		confirmed bool
		audited   bool
	}{
		{name: "Organizer", kind: ActorOrganizer, actorID: "organizer-1", revision: "revision-2", scope: ScopeTask, value: 4, confirmed: true, audited: true},
		{name: "missing actor", kind: ActorHuman, revision: "revision-2", scope: ScopeTask, value: 4, confirmed: true, audited: true},
		{name: "wrong revision", kind: ActorHuman, actorID: "human-1", revision: "revision-old", scope: ScopeTask, value: 4, confirmed: true, audited: true},
		{name: "wrong scope", kind: ActorHuman, actorID: "human-1", revision: "revision-2", scope: ScopeWorkspace, value: 4, confirmed: true, audited: true},
		{name: "wrong value", kind: ActorHuman, actorID: "human-1", revision: "revision-2", scope: ScopeTask, value: 5, confirmed: true, audited: true},
		{name: "unconfirmed", kind: ActorHuman, actorID: "human-1", revision: "revision-2", scope: ScopeTask, value: 4, audited: true},
		{name: "unaudited", kind: ActorHuman, actorID: "human-1", revision: "revision-2", scope: ScopeTask, value: 4, confirmed: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			facts := base.WithOneOffOverride(
				test.kind, test.actorID, test.revision, test.scope, test.value, test.confirmed, test.audited,
			)
			preview := PreviewConfiguration(facts)
			if preview.Refusal() != RefusalOneOffOverrideInvalid {
				t.Fatalf("Preview refusal = %q, want %q", preview.Refusal(), RefusalOneOffOverrideInvalid)
			}
		})
	}

	confirmed := base.WithOneOffOverride(
		ActorHuman, "human-1", "revision-2", ScopeTask, 4, true, true,
	)
	preview := PreviewConfiguration(confirmed)
	if !preview.Valid() || !preview.EffectiveAfter().UsedOneOffOverride() {
		t.Fatalf("confirmed one-off Preview = %#v", preview)
	}
	confirmed = confirmed.
		WithApproval(ActorHuman, "human-1", "revision-2", true).
		WithAcknowledgement(ActorHuman, "human-1", "revision-1", true)
	applied, result := ApplyConfiguration(confirmed, preview)
	if !result.Applied() || result.SecurityEnvelope() != envelope {
		t.Fatalf("Apply result = %#v, want unchanged envelope %#v", result, envelope)
	}

	afterFirstRun, firstRun := FreezeRun(applied)
	assertEffective(t, firstRun.Effective(), 4, ScopeTask, RefusalNone)
	if !firstRun.Effective().UsedOneOffOverride() || firstRun.SecurityEnvelope() != envelope {
		t.Fatalf("first Run did not preserve exact one-off/envelope facts: %#v", firstRun)
	}
	_, repeatedFromOriginal := FreezeRun(applied)
	if !reflect.DeepEqual(firstRun, repeatedFromOriginal) {
		t.Fatalf("same frozen facts are not repeatable:\n%#v\n%#v", firstRun, repeatedFromOriginal)
	}
	_, secondRun := FreezeRun(afterFirstRun)
	if secondRun.Effective().Refusal() != RefusalOutsideSecurityEnvelope {
		t.Fatalf("consumed override admitted second Run: %#v", secondRun)
	}
	if afterFirstRun.SecurityEnvelope() != envelope {
		t.Fatalf("one-off expanded durable envelope to %#v", afterFirstRun.SecurityEnvelope())
	}
}

func TestOneOffCannotAuthorizeProjectOrWorkspaceEnvelopeExpansion(t *testing.T) {
	envelope := NewEnvelope(0, 3)
	for _, test := range []struct {
		name          string
		configuration Configuration
	}{
		{name: "Project", configuration: NewConfiguration(Concrete(4)).WithTask(Concrete(1))},
		{name: "Workspace", configuration: NewConfiguration(Concrete(1)).WithWorkspace(Concrete(4)).WithTask(Concrete(1))},
	} {
		t.Run(test.name, func(t *testing.T) {
			facts := NewFacts(envelope).
				WithProposal("revision-2", test.configuration, true).
				WithOneOffOverride(ActorHuman, "human-1", "revision-2", ScopeTask, 1, true, true)
			preview := PreviewConfiguration(facts)
			if preview.Refusal() != RefusalOutsideSecurityEnvelope {
				t.Fatalf("Preview refusal = %q, want Project/Workspace envelope refusal", preview.Refusal())
			}
		})
	}
}

func TestSeededMetamorphicPrecedenceAndRepeatability(t *testing.T) {
	const seed uint64 = 20260909
	random := seededSequence{state: seed}
	envelope := NewEnvelope(0, 15)
	for index := 0; index < 1024; index++ {
		projectValue := random.intn(16)
		workspaceConcrete := random.intn(2) == 1
		workspaceValue := random.intn(16)
		taskConcrete := random.intn(2) == 1
		taskValue := random.intn(16)

		configuration := NewConfiguration(Concrete(projectValue))
		if workspaceConcrete {
			configuration = configuration.WithWorkspace(Concrete(workspaceValue))
		}
		if taskConcrete {
			configuration = configuration.WithTask(Concrete(taskValue))
		}
		got := Resolve(configuration, envelope)
		wantValue, wantSource := projectValue, ScopeProject
		if workspaceConcrete {
			wantValue, wantSource = workspaceValue, ScopeWorkspace
		}
		if taskConcrete {
			wantValue, wantSource = taskValue, ScopeTask
		}
		assertEffective(t, got, wantValue, wantSource, RefusalNone)
		if repeated := Resolve(configuration, envelope); !reflect.DeepEqual(got, repeated) {
			t.Fatalf("seed %d case %d was not repeatable", seed, index)
		}

		if taskConcrete {
			mutatedParents := NewConfiguration(Concrete((projectValue + 1) % 16)).
				WithWorkspace(Concrete((workspaceValue + 1) % 16)).
				WithTask(Concrete(taskValue))
			if metamorphic := Resolve(mutatedParents, envelope); !reflect.DeepEqual(got, metamorphic) {
				t.Fatalf("seed %d case %d: shadowed parent mutation changed Task effective value", seed, index)
			}
		} else if workspaceConcrete {
			mutatedProject := NewConfiguration(Concrete((projectValue + 1) % 16)).
				WithWorkspace(Concrete(workspaceValue))
			if metamorphic := Resolve(mutatedProject, envelope); !reflect.DeepEqual(got, metamorphic) {
				t.Fatalf("seed %d case %d: shadowed Project mutation changed Workspace effective value", seed, index)
			}
		}

		wider := NewEnvelope(-1, 16)
		if metamorphic := Resolve(configuration, wider); !reflect.DeepEqual(got, metamorphic) {
			t.Fatalf("seed %d case %d: widening an already-admitting envelope changed effective value", seed, index)
		}
	}
}

func TestValueBuildersKeepInputsFrozen(t *testing.T) {
	baseConfiguration := NewConfiguration(Concrete(1))
	derivedConfiguration := baseConfiguration.WithWorkspace(Concrete(2)).WithTask(Concrete(3))
	if baseConfiguration.Selection(ScopeWorkspace).Kind() != SelectionInherit ||
		baseConfiguration.Selection(ScopeTask).Kind() != SelectionInherit {
		t.Fatalf("configuration builder mutated base: %#v", baseConfiguration)
	}
	if derivedConfiguration.Selection(ScopeWorkspace).Kind() != SelectionValue ||
		derivedConfiguration.Selection(ScopeTask).Kind() != SelectionValue {
		t.Fatalf("configuration builder did not return replacements: %#v", derivedConfiguration)
	}

	baseFacts := NewFacts(NewEnvelope(0, 3))
	derivedFacts := baseFacts.WithActive("revision-1", derivedConfiguration)
	if _, ok := baseFacts.ActiveRevision(); ok {
		t.Fatal("Facts builder mutated base")
	}
	if revision, ok := derivedFacts.ActiveRevision(); !ok || revision != "revision-1" {
		t.Fatalf("derived facts active revision = %q, %v", revision, ok)
	}

	for _, value := range []any{Selection{}, Configuration{}, Envelope{}, Facts{}} {
		typeOf := reflect.TypeOf(value)
		for fieldIndex := 0; fieldIndex < typeOf.NumField(); fieldIndex++ {
			if typeOf.Field(fieldIndex).IsExported() {
				t.Fatalf("%s exposes mutable input field %s", typeOf, typeOf.Field(fieldIndex).Name)
			}
		}
	}
}
