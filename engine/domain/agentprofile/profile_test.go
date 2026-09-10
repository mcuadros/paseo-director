// SPDX-License-Identifier: Apache-2.0

package agentprofile

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
)

func selection(provider domainconfig.Provider, model, permission string, capabilities ...domainconfig.MCPCapability) domainconfig.AgentSelection {
	return domainconfig.AgentSelection{
		Provider: provider, Model: model, Effort: "high", Mode: "default", PermissionMode: permission,
		ProviderOptions: []domainconfig.ProviderOption{{Name: domainconfig.ProviderOptionNetworkAccess, Value: "disabled"}},
		MCPCapabilities: slices.Clone(capabilities),
	}
}

func testProfiles() domainconfig.AgentProfiles {
	return domainconfig.AgentProfiles{
		Organizer: domainconfig.AgentProfile{
			AgentSelection: selection(domainconfig.ProviderCodex, "gpt-5.4-mini", "read-only", domainconfig.MCPProjectRead, domainconfig.MCPPlanningCommandSubmit),
			FallbackChain:  []domainconfig.AgentSelection{},
		},
		Worker: domainconfig.AgentProfile{
			AgentSelection: selection(domainconfig.ProviderClaudeCode, "claude-haiku-4-5", "workspace-write", domainconfig.MCPTaskRead, domainconfig.MCPTaskOutcomeSubmit),
			FallbackChain:  []domainconfig.AgentSelection{},
		},
		Reviewer: domainconfig.AgentProfile{
			AgentSelection: selection(domainconfig.ProviderOpenCode, "opencode/nemotron-3-ultra-free", "read-only", domainconfig.MCPCandidateRead, domainconfig.MCPReviewVerdictSubmit),
			FallbackChain:  []domainconfig.AgentSelection{},
		},
	}
}

func variantFor(value domainconfig.AgentSelection) VariantFact {
	return VariantFact{
		Effort: value.Effort, Mode: value.Mode, PermissionMode: value.PermissionMode,
		ProviderOptions: slices.Clone(value.ProviderOptions), MCPCapabilities: slices.Clone(value.MCPCapabilities),
		SessionStdioMCP: true, ExactMCPToolPolicy: true, RuntimeProbePassed: true,
	}
}

func testDiscovery(profiles domainconfig.AgentProfiles) DiscoverySnapshot {
	snapshot := DiscoverySnapshot{
		SchemaVersion: DiscoverySchemaVersion, Revision: strings.Repeat("0", 64),
		PaseoVersion: SupportedPaseoVersion, ObservedAtMillis: 1_000, MaximumAgeMillis: 30_000,
		Providers: []ProviderFact{
			{Provider: domainconfig.ProviderCodex, CLIVersion: "0.147.0", State: ProviderReady, DiagnosticCodes: []DiagnosticCode{}, Models: []ModelFact{{Model: profiles.Organizer.Model, Variants: []VariantFact{variantFor(profiles.Organizer.AgentSelection)}}}},
			{Provider: domainconfig.ProviderClaudeCode, CLIVersion: "2.1.258", State: ProviderReady, DiagnosticCodes: []DiagnosticCode{}, Models: []ModelFact{{Model: profiles.Worker.Model, Variants: []VariantFact{variantFor(profiles.Worker.AgentSelection)}}}},
			{Provider: domainconfig.ProviderOpenCode, CLIVersion: "1.18.18", State: ProviderReady, DiagnosticCodes: []DiagnosticCode{}, Models: []ModelFact{{Model: profiles.Reviewer.Model, Variants: []VariantFact{variantFor(profiles.Reviewer.AgentSelection)}}}},
		},
	}
	sealed, err := SealDiscovery(snapshot)
	if err != nil {
		panic(err)
	}
	return sealed
}

func freezeRequest(profiles domainconfig.AgentProfiles, discovery DiscoverySnapshot) FreezeRequest {
	if sealed, err := SealDiscovery(discovery); err == nil {
		discovery = sealed
	}
	return FreezeRequest{
		Profiles: profiles, OrganizerRevision: strings.Repeat("a", 40),
		ExpectedOrganizerRevision: strings.Repeat("a", 40), ConfigurationSHA256: strings.Repeat("b", 64),
		Discovery: discovery, ExpectedDiscoveryRevision: discovery.Revision, NowMillis: 1_001,
	}
}

func firstCode(t *testing.T, err error, role Role) ResolutionCode {
	t.Helper()
	explanations, ok := ResolutionExplanations(err)
	if !ok {
		t.Fatalf("error = %v, want ResolutionError", err)
	}
	if role == "" && len(explanations) > 0 {
		return explanations[0].Code
	}
	for _, explanation := range explanations {
		if explanation.Role == role || explanation.Role == "" {
			return explanation.Code
		}
	}
	t.Fatalf("explanations = %#v, want role %s", explanations, role)
	return ""
}

func TestSupportedExactProviderCombinationsFreezeAllRoles(t *testing.T) {
	profiles := testProfiles()
	discovery := testDiscovery(profiles)
	frozen, err := Freeze(freezeRequest(profiles, discovery))
	if err != nil {
		t.Fatal(err)
	}
	if !frozen.Valid() || len(frozen.SHA256()) != 64 || frozen.OrganizerRevision() != strings.Repeat("a", 40) ||
		frozen.ConfigurationSHA256() != strings.Repeat("b", 64) || frozen.DiscoveryRevision() != discovery.Revision {
		t.Fatalf("frozen identity = %s %s %s %s", frozen.SHA256(), frozen.OrganizerRevision(), frozen.ConfigurationSHA256(), frozen.DiscoveryRevision())
	}
	for _, role := range []Role{RoleOrganizer, RoleWorker, RoleReviewer} {
		resolved, ok := frozen.Role(role)
		if !ok || resolved.Role != role || resolved.SelectedIndex != 0 || len(resolved.PriorExplanations) != 0 {
			t.Fatalf("role %s = %#v, %v", role, resolved, ok)
		}
	}
}

func TestUnsupportedAndUnavailableCombinationsHaveClosedExplanations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domainconfig.AgentProfiles, *DiscoverySnapshot)
		code   ResolutionCode
	}{
		{"provider not discovered", func(_ *domainconfig.AgentProfiles, discovery *DiscoverySnapshot) {
			discovery.Providers = discovery.Providers[1:]
		}, CodeProviderNotDiscovered},
		{"unsupported exact CLI tuple", func(_ *domainconfig.AgentProfiles, discovery *DiscoverySnapshot) {
			discovery.Providers[0].CLIVersion = "2.1.259"
		}, CodeProviderTupleUnsupported},
		{"provider unavailable", func(_ *domainconfig.AgentProfiles, discovery *DiscoverySnapshot) {
			discovery.Providers[0].State = ProviderUnavailable
			discovery.Providers[0].DiagnosticCodes = []DiagnosticCode{DiagnosticAuthenticationRequired}
		}, CodeProviderUnavailable},
		{"model", func(profiles *domainconfig.AgentProfiles, _ *DiscoverySnapshot) {
			profiles.Worker.Model = "another-model"
		}, CodeModelUnsupported},
		{"effort", func(profiles *domainconfig.AgentProfiles, _ *DiscoverySnapshot) { profiles.Worker.Effort = "max" }, CodeCombinationUnsupported},
		{"mode", func(profiles *domainconfig.AgentProfiles, _ *DiscoverySnapshot) { profiles.Worker.Mode = "plan" }, CodeCombinationUnsupported},
		{"permission", func(profiles *domainconfig.AgentProfiles, _ *DiscoverySnapshot) {
			profiles.Worker.PermissionMode = "read-only"
		}, CodeCombinationUnsupported},
		{"provider options", func(profiles *domainconfig.AgentProfiles, _ *DiscoverySnapshot) {
			profiles.Worker.ProviderOptions[0].Value = "enabled"
		}, CodeProviderOptionsUnsupported},
		{"MCP capability", func(_ *domainconfig.AgentProfiles, discovery *DiscoverySnapshot) {
			discovery.Providers[0].Models[0].Variants[0].MCPCapabilities = []domainconfig.MCPCapability{domainconfig.MCPTaskRead}
		}, CodeMCPCapabilityUnsupported},
		{"runtime proof", func(_ *domainconfig.AgentProfiles, discovery *DiscoverySnapshot) {
			discovery.Providers[0].Models[0].Variants[0].RuntimeProbePassed = false
		}, CodeMCPProofUnavailable},
		{"stdio MCP proof", func(_ *domainconfig.AgentProfiles, discovery *DiscoverySnapshot) {
			discovery.Providers[0].Models[0].Variants[0].SessionStdioMCP = false
		}, CodeMCPProofUnavailable},
		{"exact tool policy proof", func(_ *domainconfig.AgentProfiles, discovery *DiscoverySnapshot) {
			discovery.Providers[0].Models[0].Variants[0].ExactMCPToolPolicy = false
		}, CodeMCPProofUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profiles := testProfiles()
			discovery := testDiscovery(profiles)
			test.mutate(&profiles, &discovery)
			_, err := Freeze(freezeRequest(profiles, discovery))
			if err == nil || firstCode(t, err, RoleWorker) != test.code {
				t.Fatalf("Freeze() error = %v, want %s", err, test.code)
			}
		})
	}
}

func TestFallbackIsDisabledByDefaultAndUsesOnlyExplicitOrder(t *testing.T) {
	profiles := testProfiles()
	discovery := testDiscovery(profiles)
	discovery.Providers[0].State = ProviderUnavailable
	discovery.Providers[0].DiagnosticCodes = []DiagnosticCode{DiagnosticServiceUnavailable}
	if _, err := Freeze(freezeRequest(profiles, discovery)); err == nil || firstCode(t, err, RoleWorker) != CodeProviderUnavailable {
		t.Fatalf("empty fallback error = %v", err)
	}

	unsupported := selection(domainconfig.ProviderCodex, "not-discovered", "workspace-write", domainconfig.MCPTaskRead, domainconfig.MCPTaskOutcomeSubmit)
	supported := selection(domainconfig.ProviderOpenCode, profiles.Reviewer.Model, "workspace-write", domainconfig.MCPTaskRead, domainconfig.MCPTaskOutcomeSubmit)
	discovery.Providers[2].Models[0].Variants = append(discovery.Providers[2].Models[0].Variants, variantFor(supported))
	profiles.Worker.FallbackChain = []domainconfig.AgentSelection{unsupported, supported}
	frozen, err := Freeze(freezeRequest(profiles, discovery))
	if err != nil {
		t.Fatal(err)
	}
	worker, ok := frozen.Role(RoleWorker)
	if !ok || worker.SelectedIndex != 2 || worker.Selection.Provider != domainconfig.ProviderOpenCode || len(worker.PriorExplanations) != 2 ||
		worker.PriorExplanations[0].Code != CodeProviderUnavailable || worker.PriorExplanations[1].Code != CodeModelUnsupported {
		t.Fatalf("ordered fallback = %#v, %v", worker, ok)
	}
	if got := testProfiles().Worker.FallbackChain; got == nil || len(got) != 0 {
		t.Fatalf("default fallback = %#v, want explicit empty array", got)
	}
}

func TestFallbackSelectionPropertyChoosesEarliestSupportedIndex(t *testing.T) {
	for mask := 0; mask < 8; mask++ {
		profiles := testProfiles()
		primary := profiles.Worker.AgentSelection
		chain := []domainconfig.AgentSelection{primary}
		for index := 1; index < 3; index++ {
			candidate := cloneSelection(primary)
			candidate.Mode = "mode-" + string(rune('0'+index))
			chain = append(chain, candidate)
		}
		profiles.Worker.AgentSelection = chain[0]
		profiles.Worker.FallbackChain = slices.Clone(chain[1:])
		discovery := testDiscovery(profiles)
		discovery.Providers[0].Models[0].Variants = nil
		want := -1
		for index, candidate := range chain {
			if mask&(1<<index) != 0 {
				discovery.Providers[0].Models[0].Variants = append(discovery.Providers[0].Models[0].Variants, variantFor(candidate))
				if want < 0 {
					want = index
				}
			}
		}
		if len(discovery.Providers[0].Models[0].Variants) == 0 {
			// Keep the raw discovery valid while proving that no configured row
			// is selected.
			other := variantFor(primary)
			other.Mode = "unconfigured"
			discovery.Providers[0].Models[0].Variants = []VariantFact{other}
		}
		frozen, err := Freeze(freezeRequest(profiles, discovery))
		if want < 0 {
			if err == nil {
				t.Fatalf("mask %03b selected an inferred fallback", mask)
			}
			continue
		}
		if err != nil {
			t.Fatalf("mask %03b: %v", mask, err)
		}
		worker, _ := frozen.Role(RoleWorker)
		if worker.SelectedIndex != want {
			t.Fatalf("mask %03b selected %d, want %d", mask, worker.SelectedIndex, want)
		}
	}
}

func TestDiscoveryRevisionFreshnessAndDriftFailClosed(t *testing.T) {
	profiles := testProfiles()
	discovery := testDiscovery(profiles)
	tests := []struct {
		name   string
		mutate func(*FreezeRequest)
		code   ResolutionCode
	}{
		{"Organizer revision", func(request *FreezeRequest) { request.ExpectedOrganizerRevision = strings.Repeat("e", 40) }, CodeOrganizerRevisionStale},
		{"discovery revision", func(request *FreezeRequest) { request.ExpectedDiscoveryRevision = strings.Repeat("e", 64) }, CodeDiscoveryRevisionStale},
		{"expired facts", func(request *FreezeRequest) { request.NowMillis = 31_001 }, CodeDiscoveryFactsStale},
		{"future facts", func(request *FreezeRequest) { request.NowMillis = 999 }, CodeDiscoveryFactsStale},
		{"Paseo drift", func(request *FreezeRequest) {
			request.Discovery.PaseoVersion = "0.7.3"
			request.Discovery, _ = SealDiscovery(request.Discovery)
			request.ExpectedDiscoveryRevision = request.Discovery.Revision
		}, CodeProviderTupleUnsupported},
		{"unsealed fact drift", func(request *FreezeRequest) {
			request.Discovery.Providers[0].CLIVersion = "2.1.259"
		}, CodeDiscoveryInvalid},
		{"duplicate provider", func(request *FreezeRequest) {
			request.Discovery.Providers = append(request.Discovery.Providers, request.Discovery.Providers[0])
		}, CodeDiscoveryInvalid},
		{"duplicate variant", func(request *FreezeRequest) {
			variants := request.Discovery.Providers[0].Models[0].Variants
			request.Discovery.Providers[0].Models[0].Variants = append(variants, variants[0])
		}, CodeDiscoveryInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := freezeRequest(profiles, discovery)
			test.mutate(&request)
			_, err := Freeze(request)
			if err == nil || firstCode(t, err, "") != test.code {
				t.Fatalf("Freeze() error = %v, want %s", err, test.code)
			}
		})
	}
}

func TestFrozenProfilesAreDefensiveAndRestartDeterministic(t *testing.T) {
	profiles := testProfiles()
	discovery := testDiscovery(profiles)
	request := freezeRequest(profiles, discovery)
	first, err := Freeze(request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Freeze(request)
	if err != nil || first.SHA256() != second.SHA256() || !bytes.Equal(first.CanonicalJSON(), second.CanonicalJSON()) {
		t.Fatalf("deterministic freeze = %s %s, %v", first.SHA256(), second.SHA256(), err)
	}
	reordered := testDiscovery(testProfiles())
	slices.Reverse(reordered.Providers)
	reordered, err = SealDiscovery(reordered)
	if err != nil || reordered.Revision != discovery.Revision {
		t.Fatalf("semantic discovery reorder changed revision: %s %s, %v", reordered.Revision, discovery.Revision, err)
	}
	reorderedFrozen, err := Freeze(freezeRequest(testProfiles(), reordered))
	if err != nil || reorderedFrozen.SHA256() != first.SHA256() {
		t.Fatalf("semantic discovery reorder changed frozen Run: %s %s, %v", reorderedFrozen.SHA256(), first.SHA256(), err)
	}
	before := first.CanonicalJSON()
	profiles.Worker.Model = "mutated"
	discovery.Providers[0].Models[0].Model = "mutated"
	role, _ := first.Role(RoleWorker)
	role.Selection.Model = "mutated-copy"
	role.Selection.MCPCapabilities[0] = domainconfig.MCPProjectRead
	if !bytes.Equal(before, first.CanonicalJSON()) {
		t.Fatal("source or accessor mutation changed a frozen Run profile")
	}
	restored, err := ParseFrozenSet(first.CanonicalJSON())
	if err != nil || restored.SHA256() != first.SHA256() || !bytes.Equal(restored.CanonicalJSON(), first.CanonicalJSON()) {
		t.Fatalf("restart restore = %s, %v", restored.SHA256(), err)
	}
	encoded, err := json.Marshal(struct {
		Profiles FrozenSet `json:"profiles"`
	}{Profiles: first})
	if err != nil {
		t.Fatal(err)
	}
	var restarted struct {
		Profiles FrozenSet `json:"profiles"`
	}
	if err := json.Unmarshal(encoded, &restarted); err != nil || restarted.Profiles.SHA256() != first.SHA256() {
		t.Fatalf("TaskStore-shaped restart = %s, %v", restarted.Profiles.SHA256(), err)
	}
}

func TestClosedSchemasHashesAndParsersRejectMutation(t *testing.T) {
	expectedHashes := map[string]string{
		"discovery": "b24f996491a706c9a1da957dc802bc971f0cdbbd3f0b3d8214546832b03eba6e",
		"frozen":    "c961897556e407ca8b817522f671b61265574f8a6eabb2ca09d5039528f4b384",
	}
	for name, schemaAndHash := range map[string]struct {
		schema []byte
		hash   func() (string, error)
	}{
		"discovery": {DiscoverySchema(), DiscoverySchemaSHA256},
		"frozen":    {FrozenSchema(), FrozenSchemaSHA256},
	} {
		t.Run(name, func(t *testing.T) {
			hash, err := schemaAndHash.hash()
			if err != nil || hash != expectedHashes[name] || len(schemaAndHash.schema) == 0 {
				t.Fatalf("schema hash = %q, want %q: %v", hash, expectedHashes[name], err)
			}
			mutated := schemaAndHash.schema
			mutated[0] = '['
			if bytes.Equal(mutated, map[string][]byte{"discovery": DiscoverySchema(), "frozen": FrozenSchema()}[name]) {
				t.Fatal("schema accessor is not defensive")
			}
		})
	}
	profiles := testProfiles()
	discovery := testDiscovery(profiles)
	encoded, err := json.Marshal(discovery)
	if err != nil {
		t.Fatal(err)
	}
	unknown := bytes.Replace(encoded, []byte(`"providers":`), []byte(`"rawDiagnostic":"credential-shaped-value","providers":`), 1)
	if _, err := ParseDiscovery(unknown); err == nil || bytes.Contains([]byte(err.Error()), []byte("credential-shaped-value")) {
		t.Fatalf("unknown diagnostic mutation error = %v", err)
	}
	frozen, err := Freeze(freezeRequest(profiles, discovery))
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(frozen.CanonicalJSON(), []byte(`"role":"reviewer"`), []byte(`"role":"reviewer","unexpected":true`), 1)
	if _, err := ParseFrozenSet(tampered); err == nil {
		t.Fatal("ParseFrozenSet accepted an unknown field")
	}
	tampered = bytes.Replace(frozen.CanonicalJSON(), []byte(`"permissionMode":"read-only"`), []byte(`"permissionMode":"workspace-write"`), 1)
	if _, err := ParseFrozenSet(tampered); err == nil {
		t.Fatal("ParseFrozenSet accepted authority-expanding mutation")
	}
}
