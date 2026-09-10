// SPDX-License-Identifier: Apache-2.0

package agentbridge

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/mcuadros/director-engine/domain/agentprofile"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
)

func bridgeSelection(role agentprofile.Role) domainconfig.AgentSelection {
	selection := domainconfig.AgentSelection{
		Provider: domainconfig.ProviderCodex, Model: "gpt-5.4-mini", Effort: "high", Mode: "default",
		PermissionMode:  "read-only",
		ProviderOptions: []domainconfig.ProviderOption{{Name: domainconfig.ProviderOptionNetworkAccess, Value: "disabled"}},
	}
	switch role {
	case agentprofile.RoleOrganizer:
		selection.MCPCapabilities = []domainconfig.MCPCapability{domainconfig.MCPProjectRead, domainconfig.MCPPlanningCommandSubmit}
	case agentprofile.RoleWorker:
		selection.PermissionMode = "workspace-write"
		selection.MCPCapabilities = []domainconfig.MCPCapability{domainconfig.MCPProjectRead, domainconfig.MCPTaskRead, domainconfig.MCPTaskOutcomeSubmit}
	case agentprofile.RoleReviewer:
		selection.MCPCapabilities = []domainconfig.MCPCapability{domainconfig.MCPCandidateRead, domainconfig.MCPReviewVerdictSubmit}
	}
	return selection
}

func bridgeRole(role agentprofile.Role) agentprofile.FrozenRole {
	return agentprofile.FrozenRole{Role: role, Selection: bridgeSelection(role), ConfiguredChainSHA256: strings.Repeat("a", 64)}
}

func bridgeDiscovery(t *testing.T, role agentprofile.Role) agentprofile.DiscoverySnapshot {
	t.Helper()
	selection := bridgeSelection(role)
	snapshot := agentprofile.DiscoverySnapshot{
		SchemaVersion: agentprofile.DiscoverySchemaVersion, Revision: strings.Repeat("0", 64),
		PaseoVersion: agentprofile.SupportedPaseoVersion, ObservedAtMillis: 1_000, MaximumAgeMillis: 30_000,
		Providers: []agentprofile.ProviderFact{{
			Provider: selection.Provider, CLIVersion: "0.147.0", State: agentprofile.ProviderReady,
			DiagnosticCodes: []agentprofile.DiagnosticCode{}, Models: []agentprofile.ModelFact{{
				Model: selection.Model, Variants: []agentprofile.VariantFact{{
					Effort: selection.Effort, Mode: selection.Mode, PermissionMode: selection.PermissionMode,
					ProviderOptions: slices.Clone(selection.ProviderOptions), MCPCapabilities: slices.Clone(selection.MCPCapabilities),
					SessionStdioMCP: true, ExactMCPToolPolicy: true, RuntimeProbePassed: true,
				}},
			}},
		}},
	}
	sealed, err := agentprofile.SealDiscovery(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func TestContractCatalogsAreClosedByEffectiveRole(t *testing.T) {
	definition, err := EmbeddedDefinition()
	if err != nil {
		t.Fatal(err)
	}
	if hash, err := SchemaSHA256(); err != nil || len(hash) != 64 {
		t.Fatalf("schema hash = %q, %v", hash, err)
	}
	wants := map[agentprofile.Role][]string{
		agentprofile.RoleOrganizer: {"director_project_read", "director_planning_command_submit"},
		agentprofile.RoleWorker:    {"director_project_read", "director_task_read", "director_task_outcome_submit"},
		agentprofile.RoleReviewer:  {"director_candidate_read", "director_review_verdict_submit"},
	}
	for role, want := range wants {
		catalog, err := Catalog(bridgeRole(role))
		if err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		var names []string
		for _, tool := range catalog {
			names = append(names, tool.Name)
			schema := string(tool.InputSchema)
			for _, selector := range []string{`"projectId"`, `"workspaceId"`, `"taskId"`, `"runId"`, `"candidateId"`} {
				if strings.Contains(schema, selector) {
					t.Fatalf("%s schema exposes scope selector %s", tool.Name, selector)
				}
			}
		}
		if !slices.Equal(names, want) {
			t.Fatalf("%s catalog = %v, want %v", role, names, want)
		}
	}
	if len(definition.Tools) != 6 || len(definition.Capabilities) != 6 {
		t.Fatalf("contract is not closed: %#v", definition)
	}
}

func TestContractMutationsFailClosed(t *testing.T) {
	original := Schema()
	mutations := [][]byte{
		[]byte(strings.Replace(string(original), `"roles": ["organizer", "worker", "reviewer"]`, `"roles": ["organizer", "reviewer", "worker"]`, 1)),
		[]byte(strings.Replace(string(original), `"capability": "planning.command.submit"`, `"capability": "project.read"`, 1)),
		[]byte(strings.Replace(string(original), `"roles": ["reviewer"]`, `"roles": ["worker"]`, 1)),
		[]byte(strings.Replace(string(original), `"additionalProperties": false`, `"additionalProperties": true`, 1)),
	}
	for index, mutation := range mutations {
		if _, err := ParseDefinition(mutation); err == nil {
			t.Errorf("mutation %d survived", index)
		}
	}
}

func preflightCode(t *testing.T, err error) PreflightCode {
	t.Helper()
	var failure *PreflightError
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v, want PreflightError", err)
	}
	return failure.Code
}

func TestProviderPreflightHasPreciseDeterministicReasons(t *testing.T) {
	role := bridgeRole(agentprofile.RoleWorker)
	tests := []struct {
		name   string
		mutate func(*agentprofile.DiscoverySnapshot)
		want   PreflightCode
	}{
		{"Paseo", func(snapshot *agentprofile.DiscoverySnapshot) { snapshot.PaseoVersion = "0.7.3" }, PreflightPaseoUnsupported},
		{"CLI", func(snapshot *agentprofile.DiscoverySnapshot) { snapshot.Providers[0].CLIVersion = "0.148.0" }, PreflightCLIVersionUnsupported},
		{"provider", func(snapshot *agentprofile.DiscoverySnapshot) {
			snapshot.Providers[0].Provider = domainconfig.ProviderClaudeCode
			snapshot.Providers[0].CLIVersion = "2.1.258"
		}, PreflightProviderMissing},
		{"unavailable", func(snapshot *agentprofile.DiscoverySnapshot) {
			snapshot.Providers[0].State = agentprofile.ProviderUnavailable
		}, PreflightProviderUnavailable},
		{"model", func(snapshot *agentprofile.DiscoverySnapshot) { snapshot.Providers[0].Models[0].Model = "other" }, PreflightModelUnsupported},
		{"variant", func(snapshot *agentprofile.DiscoverySnapshot) {
			snapshot.Providers[0].Models[0].Variants[0].Effort = "max"
		}, PreflightCombinationUnsupported},
		{"options", func(snapshot *agentprofile.DiscoverySnapshot) {
			snapshot.Providers[0].Models[0].Variants[0].ProviderOptions[0].Value = "enabled"
		}, PreflightOptionsUnsupported},
		{"capability", func(snapshot *agentprofile.DiscoverySnapshot) {
			snapshot.Providers[0].Models[0].Variants[0].MCPCapabilities = []domainconfig.MCPCapability{domainconfig.MCPTaskRead}
		}, PreflightCapabilitiesUnsupported},
		{"session MCP", func(snapshot *agentprofile.DiscoverySnapshot) {
			snapshot.Providers[0].Models[0].Variants[0].SessionStdioMCP = false
		}, PreflightSessionMCPUnavailable},
		{"tool policy", func(snapshot *agentprofile.DiscoverySnapshot) {
			snapshot.Providers[0].Models[0].Variants[0].ExactMCPToolPolicy = false
		}, PreflightToolPolicyUnavailable},
		{"runtime probe", func(snapshot *agentprofile.DiscoverySnapshot) {
			snapshot.Providers[0].Models[0].Variants[0].RuntimeProbePassed = false
		}, PreflightRuntimeProbeUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := bridgeDiscovery(t, agentprofile.RoleWorker)
			test.mutate(&snapshot)
			sealed, err := agentprofile.SealDiscovery(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			err = VerifyProviderPreflight(role, sealed, sealed.Revision, 1_001)
			if err == nil || preflightCode(t, err) != test.want {
				t.Fatalf("error = %v, want %s", err, test.want)
			}
		})
	}
	snapshot := bridgeDiscovery(t, agentprofile.RoleWorker)
	if err := VerifyProviderPreflight(role, snapshot, snapshot.Revision, 1_001); err != nil {
		t.Fatal(err)
	}
	if err := VerifyProviderPreflight(role, snapshot, strings.Repeat("f", 64), 1_001); preflightCode(t, err) != PreflightRevisionMismatch {
		t.Fatalf("revision error = %v", err)
	}
	if err := VerifyProviderPreflight(role, snapshot, snapshot.Revision, 31_001); preflightCode(t, err) != PreflightDiscoveryStale {
		t.Fatalf("stale error = %v", err)
	}
}

func TestToolInputRejectsForgedScopeUnknownFieldsOversizeAndSecrets(t *testing.T) {
	criteria := []string{"criterion-1"}
	valid := json.RawMessage(`{"outcome":"completed","candidateSha":"cccccccccccccccccccccccccccccccccccccccc","baseSha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","criteriaResults":{"criterion-1":"claimed_satisfied"},"residualRiskCodes":[]}`)
	if _, err := ValidateToolInput("director_task_outcome_submit", valid, criteria, strings.Repeat("b", 40)); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		raw  json.RawMessage
		code InputCode
	}{
		{"forged Task", json.RawMessage(`{"outcome":"blocked_by_dependency","dependencyIds":["task-2"],"wakePredicate":"dependency_done","taskId":"task-2"}`), InputInvalid},
		{"unknown", json.RawMessage(`{"outcome":"budget_exhausted","budgetDimension":"tokens","observedAmount":1,"unit":"tokens","extra":true}`), InputInvalid},
		{"secret", json.RawMessage(`{"outcome":"blocked_by_access","capabilityCode":"git","operationCode":"read","failureFingerprint":"token=github_pat_abcdefghijklmnop"}`), InputSecret},
		{"private path", json.RawMessage(`{"outcome":"blocked_by_access","resourceCode":"repository","operationCode":"read","failureFingerprint":"/home/agent/private"}`), InputPrivatePath},
		{"oversize", json.RawMessage(`{"value":"` + strings.Repeat("x", MaximumRequestBytes) + `"}`), InputTooLarge},
	}
	for _, test := range tests {
		_, err := ValidateToolInput("director_task_outcome_submit", test.raw, criteria, strings.Repeat("b", 40))
		var failure *InputError
		if !errors.As(err, &failure) || failure.Code != test.code {
			t.Errorf("%s error = %v, want %s", test.name, err, test.code)
		}
	}
}

func TestEveryClosedWorkerOutcomeAndReviewerVerdictValidates(t *testing.T) {
	base := strings.Repeat("b", 40)
	candidate := strings.Repeat("c", 40)
	criteria := []string{"criterion-1"}
	outcomes := []json.RawMessage{
		json.RawMessage(`{"outcome":"completed","candidateSha":"` + candidate + `","baseSha":"` + base + `","criteriaResults":{"criterion-1":"claimed_satisfied"},"residualRiskCodes":[]}`),
		json.RawMessage(`{"outcome":"needs_validation","candidateSha":"` + candidate + `","baseSha":"` + base + `","checkIds":["linux-ci"]}`),
		json.RawMessage(`{"outcome":"needs_review","candidateSha":"` + candidate + `","baseSha":"` + base + `","criterionIds":["criterion-1"]}`),
		json.RawMessage(`{"outcome":"needs_human_decision","questionCode":"delivery_choice","question":"Choose the approved delivery mode","options":["pull_request","direct"],"affectedScope":"task","resumeCondition":"human_decision_recorded"}`),
		json.RawMessage(`{"outcome":"blocked_by_dependency","dependencyIds":["task-blocker"],"wakePredicate":"dependency_complete"}`),
		json.RawMessage(`{"outcome":"blocked_by_access","capabilityCode":"repository_read","operationCode":"read","failureFingerprint":"fingerprint-1"}`),
		json.RawMessage(`{"outcome":"budget_exhausted","budgetDimension":"tokens","observedAmount":100,"unit":"tokens"}`),
	}
	for index, outcome := range outcomes {
		if _, err := ValidateToolInput("director_task_outcome_submit", outcome, criteria, base); err != nil {
			t.Errorf("outcome %d: %v", index, err)
		}
	}
	verdicts := []json.RawMessage{
		json.RawMessage(`{"verdict":"approve_candidate","coverage":["acceptance","correctness","security","maintainability","readability","design","quality","rigor"],"findings":[],"residualRiskCodes":[]}`),
		json.RawMessage(`{"verdict":"changes_requested","coverage":["acceptance","correctness","security","maintainability","readability","design","quality","rigor"],"findings":[{"code":"correctness_bug","severity":"P1","dimension":"correctness","summary":"A bounded defect summary","references":["engine/file.go:10"]}],"residualRiskCodes":[]}`),
		json.RawMessage(`{"verdict":"needs_human_decision","coverage":["acceptance","correctness","security","maintainability","readability","design","quality","rigor"],"findings":[{"code":"owner_choice","severity":"P2","dimension":"design","summary":"A bounded owner decision is required","references":[]}],"residualRiskCodes":["owner_decision_pending"]}`),
	}
	for index, verdict := range verdicts {
		if _, err := ValidateToolInput("director_review_verdict_submit", verdict, criteria, base); err != nil {
			t.Errorf("verdict %d: %v", index, err)
		}
	}
	invalidApproval := json.RawMessage(`{"verdict":"approve_candidate","coverage":["acceptance","correctness","security","maintainability","readability","design","quality","rigor"],"findings":[{"code":"blocking","severity":"P1","dimension":"security","summary":"Blocking finding","references":[]}],"residualRiskCodes":[]}`)
	if _, err := ValidateToolInput("director_review_verdict_submit", invalidApproval, criteria, base); err == nil {
		t.Fatal("approval with a blocking finding was accepted")
	}
}
