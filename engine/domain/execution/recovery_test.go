// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"strings"
	"sync"
	"testing"
)

func recoveryFactsForTest(signal ProviderFailureSignal) PrimaryRecoveryFacts {
	binding := strings.Repeat("a", 64)
	agent := NativeAgentRecoveryFact{
		AgentID: "agent-original", WorkspaceID: "workspace-native", Role: ControlledTaskAgent,
		EffectID: "agent-effect", Status: "error", ArchivedAtPresent: true,
		TitleExact: true, WorktreeExact: true, LabelsRunExact: true, ProfileExact: true, SessionExact: true,
		BootstrapPresent: true, PromptPresent: true, PersistenceReferencePresent: true,
		FailureSignals: []ProviderFailureSignal{signal},
	}
	hostObservation := EffectObservation{
		ID: "host-observation", EffectID: "recovery-observe", Status: ObservationDesired,
		ExternalID: agent.AgentID, BindingHash: binding, PriorDispatcherAbsent: true,
		ObservedAtMillis: 1_000, MaximumAgeMillis: 30_000,
		Inventory: &PrimaryRecoveryInventory{
			Complete: true,
			Workspaces: []NativeWorkspaceRecoveryFact{{
				WorkspaceID: "workspace-native", Active: true, WorktreeExact: true, TitleExact: true, KindExact: true,
			}},
			Agents: []NativeAgentRecoveryFact{agent},
		},
	}
	hostObservation.FactHash = EffectObservationHash(hostObservation)
	runtimeObservation := PrimaryRuntimeRecoveryObservation{
		ID: "runtime-observation", ObservedAtMillis: 1_000, MaximumAgeMillis: 30_000,
		BindingHash: binding, RepositoryExact: true, WorktreePresent: true, WorktreeExact: true,
		BranchExact: true, BaseExact: true, OriginalAgentProcessAbsent: true,
		PriorEngineAndDispatchAbsent: true,
		RelatedWorktrees: []RelatedWorktreeRecoveryFact{{
			WorktreeID: "worktree-native", BindingHash: binding, Owned: true, ExactRun: true, Active: true,
		}},
	}
	runtimeObservation.FactHash = PrimaryRuntimeRecoveryObservationHash(runtimeObservation)
	observation := PrimaryRecoveryObservation{
		ID: "primary-recovery-observation", ObservedRunVersion: 10,
		Host: hostObservation, Runtime: runtimeObservation,
	}
	observation.FactHash = PrimaryRecoveryObservationHash(observation)
	return PrimaryRecoveryFacts{
		RunVersion: 11, CurrentAgentID: agent.AgentID, CurrentWorkspaceID: agent.WorkspaceID,
		RepositoryBindingHash: binding, ProfileSHA256: strings.Repeat("b", 64),
		FallbackDecisionSHA256: strings.Repeat("c", 64), LeaseEpoch: 7,
		HelpersSafe: true, Policy: DefaultPrimaryRecoveryPolicy(), NowMillis: 1_001,
		Recovery: PrimaryRecovery{RepeatedFailureCount: 2, PoisonedSession: true, Observation: &observation},
	}
}

func TestProviderFailureClassifierIsClosedAndContradictionSafe(t *testing.T) {
	cases := map[ProviderFailureSignal]ProviderFailureClass{
		ProviderFailureNone:           FailureClassNone,
		ProviderFailureTransient:      FailureClassTransient,
		ProviderFailureTerminal:       FailureClassTerminalProvider,
		ProviderFailurePolicy:         FailureClassTerminalPolicy,
		ProviderFailureAuthentication: FailureClassTerminalAuthentication,
		ProviderFailureConfiguration:  FailureClassTerminalConfiguration,
		ProviderFailureUnknown:        FailureClassAmbiguous,
	}
	for signal, expected := range cases {
		if got := ClassifyProviderFailure([]ProviderFailureSignal{signal}); got != expected {
			t.Fatalf("signal %s classified %s, want %s", signal, got, expected)
		}
		// Deduplication preserves valid signal
		if got := ClassifyProviderFailure([]ProviderFailureSignal{signal, signal}); got != expected {
			t.Fatalf("duplicate signal %s classified %s, want %s", signal, got, expected)
		}
	}

	// Pairwise contradictions all fail closed to FailureClassAmbiguous
	contradictionPairs := [][]ProviderFailureSignal{
		{ProviderFailurePolicy, ProviderFailureAuthentication},
		{ProviderFailureConfiguration, ProviderFailurePolicy},
		{ProviderFailureConfiguration, ProviderFailureAuthentication},
		{ProviderFailureConfiguration, ProviderFailureTransient},
		{ProviderFailureConfiguration, ProviderFailureTerminal},
		{ProviderFailureConfiguration, ProviderFailureUnknown},
		{ProviderFailureAuthentication, ProviderFailureTransient},
		{ProviderFailureTerminal, ProviderFailureTransient},
		{ProviderFailurePolicy, ProviderFailureTransient},
		{ProviderFailureConfiguration, ProviderFailurePolicy, ProviderFailureAuthentication},
	}
	for _, pair := range contradictionPairs {
		if got := ClassifyProviderFailure(pair); got != FailureClassAmbiguous {
			t.Fatalf("contradictory signals %v classified %s, want ambiguous", pair, got)
		}
	}

	// Unknown or unvalidated signals fail closed
	if got := ClassifyProviderFailure([]ProviderFailureSignal{"invalid_signal"}); got != FailureClassAmbiguous {
		t.Fatalf("invalid signal classified %s, want ambiguous", got)
	}
	if got := ClassifyProviderFailure([]ProviderFailureSignal{ProviderFailureConfiguration, "invalid_signal"}); got != FailureClassAmbiguous {
		t.Fatalf("mixed invalid signal classified %s, want ambiguous", got)
	}
}

func TestPrimaryRecoveryPolicyAllowsOnlyFrozenProviderAndPolicyReplacement(t *testing.T) {
	for signal, disposition := range map[ProviderFailureSignal]PrimaryRecoveryDisposition{
		ProviderFailureTerminal:       RecoveryDispositionAuthorizeReplace,
		ProviderFailurePolicy:         RecoveryDispositionAuthorizeReplace,
		ProviderFailureAuthentication: RecoveryDispositionNeedsYou,
		ProviderFailureConfiguration:  RecoveryDispositionNeedsYou,
		ProviderFailureUnknown:        RecoveryDispositionNeedsYou,
		ProviderFailureTransient:      RecoveryDispositionWaitExternal,
	} {
		facts := recoveryFactsForTest(signal)
		decision := EvaluatePrimaryRecovery(facts)
		if decision.Disposition != disposition {
			t.Fatalf("signal %s disposition %s, want %s (%s)", signal, decision.Disposition, disposition, decision.Code)
		}
		if decision.Disposition == RecoveryDispositionNeedsYou && decision.Code == "" {
			t.Fatalf("signal %s lacks bounded Needs-you code", signal)
		}
	}
}

func TestPrimaryRecoveryAdoptsExactResumableSessionWithoutReprompt(t *testing.T) {
	facts := recoveryFactsForTest(ProviderFailureNone)
	agent := &facts.Recovery.Observation.Host.Inventory.Agents[0]
	agent.Status = "idle"
	agent.ArchivedAtPresent = false
	facts.Recovery.Observation.Host.FactHash = EffectObservationHash(facts.Recovery.Observation.Host)
	facts.Recovery.Observation.FactHash = PrimaryRecoveryObservationHash(*facts.Recovery.Observation)
	decision := EvaluatePrimaryRecovery(facts)
	if decision.Disposition != RecoveryDispositionAdoptExisting {
		t.Fatalf("resumable session decision = %#v", decision)
	}
}

func TestPrimaryRecoveryNeverTreatsClosedNullArchiveAsTerminationOrResumability(t *testing.T) {
	facts := recoveryFactsForTest(ProviderFailureNone)
	agent := &facts.Recovery.Observation.Host.Inventory.Agents[0]
	agent.Status = "closed"
	agent.ArchivedAtPresent = false
	facts.Recovery.Observation.Host.FactHash = EffectObservationHash(facts.Recovery.Observation.Host)
	facts.Recovery.Observation.FactHash = PrimaryRecoveryObservationHash(*facts.Recovery.Observation)
	decision := EvaluatePrimaryRecovery(facts)
	if decision.Disposition != RecoveryDispositionNeedsYou || decision.Code != NeedRecoveryPromptAmbiguous {
		t.Fatalf("closed/null archive decision = %#v", decision)
	}
}

func TestPrimaryRecoverySeededMutationMatrixPreservesUnsafeResources(t *testing.T) {
	seed := uint64(38)
	for iteration := 0; iteration < 256; iteration++ {
		facts := recoveryFactsForTest(ProviderFailurePolicy)
		seed = seed*6364136223846793005 + 1442695040888963407
		mutation := int(seed % 12)
		switch mutation {
		case 0:
			facts.Recovery.Observation.Runtime.RepositoryExact = false
		case 1:
			facts.Recovery.Observation.Runtime.WorktreeExact = false
		case 2:
			facts.Recovery.Observation.Runtime.BranchExact = false
		case 3:
			facts.Recovery.Observation.Runtime.BaseExact = false
		case 4:
			facts.Recovery.Observation.Host.Inventory.Workspaces[0].Active = false
		case 5:
			facts.Recovery.Observation.Host.Inventory.Workspaces = append(
				facts.Recovery.Observation.Host.Inventory.Workspaces,
				NativeWorkspaceRecoveryFact{WorkspaceID: "duplicate-workspace", Active: true, WorktreeExact: true, TitleExact: true, KindExact: true},
			)
		case 6:
			facts.Recovery.Observation.Host.Inventory.Agents[0].ParentPresent = true
		case 7:
			facts.Recovery.Observation.Host.Inventory.Agents[0].ProfileExact = false
		case 8:
			duplicate := facts.Recovery.Observation.Host.Inventory.Agents[0]
			duplicate.AgentID = "duplicate-primary"
			facts.Recovery.Observation.Host.Inventory.Agents = append(facts.Recovery.Observation.Host.Inventory.Agents, duplicate)
		case 9:
			facts.Recovery.Observation.Runtime.RelatedWorktrees = append(
				facts.Recovery.Observation.Runtime.RelatedWorktrees,
				RelatedWorktreeRecoveryFact{WorktreeID: "orphan-worktree", BindingHash: strings.Repeat("d", 64), Active: true},
			)
		case 10:
			facts.Recovery.Observation.Runtime.OriginalAgentProcessAbsent = false
		case 11:
			facts.HelpersSafe = false
		}
		facts.Recovery.Observation.Runtime.FactHash = PrimaryRuntimeRecoveryObservationHash(facts.Recovery.Observation.Runtime)
		facts.Recovery.Observation.Host.FactHash = EffectObservationHash(facts.Recovery.Observation.Host)
		facts.Recovery.Observation.FactHash = PrimaryRecoveryObservationHash(*facts.Recovery.Observation)
		decision := EvaluatePrimaryRecovery(facts)
		if decision.Disposition != RecoveryDispositionNeedsYou || decision.Code == "" {
			t.Fatalf("iteration %d mutation %d decision = %#v", iteration, mutation, decision)
		}
	}
}

func TestReplacementAuthorityBindsEveryImmutableRecoveryFact(t *testing.T) {
	authority := ReplacementAuthority{
		AuthorizedRunVersion: 12, ConsumedRunVersion: 13, LeaseEpoch: 7,
		OldAgentID: "agent-original", OldWorkspaceID: "workspace-native",
		FailureClass: FailureClassTerminalPolicy, FailureObservationID: "failure-observation",
		FailureObservationHash: strings.Repeat("a", 64), RepositoryBindingHash: strings.Repeat("b", 64),
		ProfileSHA256: strings.Repeat("c", 64), FallbackDecisionSHA256: strings.Repeat("d", 64),
		ReplacementEffectID: "replacement-effect", Consumed: true,
	}
	authority.ID = ReplacementAuthorityID(authority)
	if !ValidReplacementAuthority(authority) {
		t.Fatal("valid replacement authority rejected")
	}
	mutations := []func(*ReplacementAuthority){
		func(value *ReplacementAuthority) { value.AuthorizedRunVersion++ },
		func(value *ReplacementAuthority) { value.ConsumedRunVersion++ },
		func(value *ReplacementAuthority) { value.LeaseEpoch++ },
		func(value *ReplacementAuthority) { value.OldAgentID += "-changed" },
		func(value *ReplacementAuthority) { value.OldWorkspaceID += "-changed" },
		func(value *ReplacementAuthority) { value.FailureClass = FailureClassTerminalAuthentication },
		func(value *ReplacementAuthority) { value.FailureObservationHash = strings.Repeat("e", 64) },
		func(value *ReplacementAuthority) { value.RepositoryBindingHash = strings.Repeat("e", 64) },
		func(value *ReplacementAuthority) { value.ProfileSHA256 = strings.Repeat("e", 64) },
		func(value *ReplacementAuthority) { value.FallbackDecisionSHA256 = strings.Repeat("e", 64) },
		func(value *ReplacementAuthority) { value.ReplacementEffectID += "-changed" },
	}
	for index, mutate := range mutations {
		changed := authority
		mutate(&changed)
		if ValidReplacementAuthority(changed) {
			t.Fatalf("authority mutation %d survived", index)
		}
	}
}

func TestPrimaryRecoveryControlBudgetAndCandidatePrecedence(t *testing.T) {
	controlled := recoveryFactsForTest(ProviderFailurePolicy)
	controlled.ControlBlocksDispatch = true
	if decision := EvaluatePrimaryRecovery(controlled); decision.Disposition != RecoveryDispositionWaitExternal {
		t.Fatalf("control precedence = %#v", decision)
	}
	budgeted := recoveryFactsForTest(ProviderFailurePolicy)
	budgeted.BudgetBlocksDispatch = true
	if decision := EvaluatePrimaryRecovery(budgeted); decision.Disposition != RecoveryDispositionNeedsYou || decision.Code != NeedRecoveryBudgetBlocked {
		t.Fatalf("budget precedence = %#v", decision)
	}
	candidate := recoveryFactsForTest(ProviderFailurePolicy)
	candidate.CurrentCandidateID = "candidate-current"
	candidate.HelpersSafe = false
	candidate.Recovery.Observation.Runtime.CandidatePresent = true
	candidate.Recovery.Observation.Runtime.CandidateSHA = "1a2b3c4d5e6f789001234567890123456789abcd"
	candidate.Recovery.Observation.Runtime.CandidateExact = true
	candidate.Recovery.Observation.Runtime.FactHash = PrimaryRuntimeRecoveryObservationHash(candidate.Recovery.Observation.Runtime)
	candidate.Recovery.Observation.FactHash = PrimaryRecoveryObservationHash(*candidate.Recovery.Observation)
	if decision := EvaluatePrimaryRecovery(candidate); decision.Disposition != RecoveryDispositionAdoptExisting {
		t.Fatalf("Candidate preservation precedence = %#v", decision)
	}
}

func TestPrimaryFallbackDecisionBindsSelectedExplicitChainResult(t *testing.T) {
	chain := strings.Repeat("a", 64)
	first, err := PrimaryFallbackDecisionSHA256([]byte(`{"roles":["worker"]}`), 0, chain)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := PrimaryFallbackDecisionSHA256([]byte(`{"roles":["worker"]}`), 0, chain)
	if err != nil || replay != first {
		t.Fatalf("fallback replay = %q, %v", replay, err)
	}
	changed, err := PrimaryFallbackDecisionSHA256([]byte(`{"roles":["worker"]}`), 1, chain)
	if err != nil || changed == first {
		t.Fatalf("changed selected index did not change fallback decision: %q, %v", changed, err)
	}
	if _, err := PrimaryFallbackDecisionSHA256(nil, 0, chain); err == nil {
		t.Fatal("missing frozen profile bytes were accepted")
	}
}

func TestPrimaryRecoveryEvidenceBackedTerminalAdoption(t *testing.T) {
	terminalSignals := []ProviderFailureSignal{
		ProviderFailureTerminal,
		ProviderFailurePolicy,
		ProviderFailureAuthentication,
		ProviderFailureConfiguration,
		ProviderFailureTransient,
		ProviderFailureUnknown,
		ProviderFailureNone,
	}
	for _, status := range []string{"idle", "running", "initializing"} {
		for _, signal := range terminalSignals {
			facts := recoveryFactsForTest(signal)
			agent := &facts.Recovery.Observation.Host.Inventory.Agents[0]
			agent.Status = status
			agent.ArchivedAtPresent = false
			agent.BootstrapPresent = true
			agent.PromptPresent = true
			agent.PersistenceReferencePresent = true
			facts.Recovery.Observation.Host.FactHash = EffectObservationHash(facts.Recovery.Observation.Host)
			facts.Recovery.Observation.FactHash = PrimaryRecoveryObservationHash(*facts.Recovery.Observation)

			decision := EvaluatePrimaryRecovery(facts)
			if decision.Disposition != RecoveryDispositionAdoptExisting {
				t.Fatalf("status=%s signal=%s want adopt_existing, got %s (%s)", status, signal, decision.Disposition, decision.Code)
			}
		}
	}
}

func TestPrimaryRecoveryTerminalFailureWithoutResumableSessionPreservesArchiveAndReplace(t *testing.T) {
	// Errored agent unarchived -> must archive original.
	for _, signal := range []ProviderFailureSignal{ProviderFailureTerminal, ProviderFailurePolicy} {
		facts := recoveryFactsForTest(signal)
		agent := &facts.Recovery.Observation.Host.Inventory.Agents[0]
		agent.Status = "error"
		agent.ArchivedAtPresent = false
		facts.Recovery.Observation.Runtime.OriginalAgentProcessAbsent = false
		facts.Recovery.Observation.Host.FactHash = EffectObservationHash(facts.Recovery.Observation.Host)
		facts.Recovery.Observation.Runtime.FactHash = PrimaryRuntimeRecoveryObservationHash(facts.Recovery.Observation.Runtime)
		facts.Recovery.Observation.FactHash = PrimaryRecoveryObservationHash(*facts.Recovery.Observation)

		decision := EvaluatePrimaryRecovery(facts)
		if decision.Disposition != RecoveryDispositionArchiveOriginal {
			t.Fatalf("errored unarchived signal=%s want archive_original, got %s", signal, decision.Disposition)
		}
	}

	// Archived agent with process gone -> must authorize replacement.
	for _, signal := range []ProviderFailureSignal{ProviderFailureTerminal, ProviderFailurePolicy} {
		facts := recoveryFactsForTest(signal)
		agent := &facts.Recovery.Observation.Host.Inventory.Agents[0]
		agent.Status = "closed"
		agent.ArchivedAtPresent = true
		facts.Recovery.Observation.Runtime.OriginalAgentProcessAbsent = true
		facts.Recovery.Observation.Host.FactHash = EffectObservationHash(facts.Recovery.Observation.Host)
		facts.Recovery.Observation.Runtime.FactHash = PrimaryRuntimeRecoveryObservationHash(facts.Recovery.Observation.Runtime)
		facts.Recovery.Observation.FactHash = PrimaryRecoveryObservationHash(*facts.Recovery.Observation)

		decision := EvaluatePrimaryRecovery(facts)
		if decision.Disposition != RecoveryDispositionAuthorizeReplace {
			t.Fatalf("archived signal=%s want authorize_replacement, got %s", signal, decision.Disposition)
		}
	}

	// Archived agent but process NOT gone -> termination unproven.
	for _, signal := range []ProviderFailureSignal{ProviderFailureTerminal, ProviderFailurePolicy} {
		facts := recoveryFactsForTest(signal)
		agent := &facts.Recovery.Observation.Host.Inventory.Agents[0]
		agent.Status = "closed"
		agent.ArchivedAtPresent = true
		facts.Recovery.Observation.Runtime.OriginalAgentProcessAbsent = false
		facts.Recovery.Observation.Host.FactHash = EffectObservationHash(facts.Recovery.Observation.Host)
		facts.Recovery.Observation.Runtime.FactHash = PrimaryRuntimeRecoveryObservationHash(facts.Recovery.Observation.Runtime)
		facts.Recovery.Observation.FactHash = PrimaryRecoveryObservationHash(*facts.Recovery.Observation)

		decision := EvaluatePrimaryRecovery(facts)
		if decision.Disposition != RecoveryDispositionNeedsYou || decision.Code != NeedRecoveryTerminationUnproven {
			t.Fatalf("archived process-alive signal=%s want NeedsYou(termination_unproven), got %s (%s)", signal, decision.Disposition, decision.Code)
		}
	}
}

func TestPrimaryRecoveryAppServerBubblewrapIncidentDeterministicResolution(t *testing.T) {
	// Incident Phase 1: host app-server failure occurs (bubblewrap missing on PATH).
	// Agent is observed errored and unarchived.
	incidentFacts := recoveryFactsForTest(ProviderFailurePolicy)
	agent := &incidentFacts.Recovery.Observation.Host.Inventory.Agents[0]
	agent.Status = "error"
	agent.ArchivedAtPresent = false
	incidentFacts.Recovery.Observation.Runtime.OriginalAgentProcessAbsent = false
	incidentFacts.Recovery.Observation.Host.FactHash = EffectObservationHash(incidentFacts.Recovery.Observation.Host)
	incidentFacts.Recovery.Observation.Runtime.FactHash = PrimaryRuntimeRecoveryObservationHash(incidentFacts.Recovery.Observation.Runtime)
	incidentFacts.Recovery.Observation.FactHash = PrimaryRecoveryObservationHash(*incidentFacts.Recovery.Observation)

	phase1 := EvaluatePrimaryRecovery(incidentFacts)
	if phase1.Disposition != RecoveryDispositionArchiveOriginal {
		t.Fatalf("phase 1 want archive_original, got %s", phase1.Disposition)
	}

	// Incident Phase 2: Host recovers before archival is dispatched (or host issue resolved).
	// Agent is unarchived, live (idle), worktree is intact, prompt and persistence reference are present.
	recoveredFacts := recoveryFactsForTest(ProviderFailurePolicy)
	recoveredAgent := &recoveredFacts.Recovery.Observation.Host.Inventory.Agents[0]
	recoveredAgent.Status = "idle"
	recoveredAgent.ArchivedAtPresent = false
	recoveredAgent.BootstrapPresent = true
	recoveredAgent.PromptPresent = true
	recoveredAgent.PersistenceReferencePresent = true
	recoveredFacts.Recovery.Observation.Host.FactHash = EffectObservationHash(recoveredFacts.Recovery.Observation.Host)
	recoveredFacts.Recovery.Observation.Runtime.FactHash = PrimaryRuntimeRecoveryObservationHash(recoveredFacts.Recovery.Observation.Runtime)
	recoveredFacts.Recovery.Observation.FactHash = PrimaryRecoveryObservationHash(*recoveredFacts.Recovery.Observation)

	phase2 := EvaluatePrimaryRecovery(recoveredFacts)
	if phase2.Disposition != RecoveryDispositionAdoptExisting {
		t.Fatalf("phase 2 want adopt_existing, got %s (%s)", phase2.Disposition, phase2.Code)
	}
}

func TestPrimaryRecoveryDeterministicReplayAcrossIdenticalFacts(t *testing.T) {
	facts := recoveryFactsForTest(ProviderFailurePolicy)
	agent := &facts.Recovery.Observation.Host.Inventory.Agents[0]
	agent.Status = "idle"
	agent.ArchivedAtPresent = false
	facts.Recovery.Observation.Host.FactHash = EffectObservationHash(facts.Recovery.Observation.Host)
	facts.Recovery.Observation.FactHash = PrimaryRecoveryObservationHash(*facts.Recovery.Observation)

	first := EvaluatePrimaryRecovery(facts)
	for iteration := 0; iteration < 20; iteration++ {
		replay := EvaluatePrimaryRecovery(facts)
		if replay != first {
			t.Fatalf("replay iteration %d diverged: got %#v, want %#v", iteration, replay, first)
		}
	}
}

func TestPrimaryRecoveryConcurrentEvaluation(t *testing.T) {
	const goroutines = 32
	const iterations = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				resumable := (id+i)%2 == 0
				facts := recoveryFactsForTest(ProviderFailurePolicy)
				agent := &facts.Recovery.Observation.Host.Inventory.Agents[0]
				if resumable {
					agent.Status = "running"
					agent.ArchivedAtPresent = false
				} else {
					agent.Status = "error"
					agent.ArchivedAtPresent = true
					facts.Recovery.Observation.Runtime.OriginalAgentProcessAbsent = true
				}
				facts.Recovery.Observation.Host.FactHash = EffectObservationHash(facts.Recovery.Observation.Host)
				facts.Recovery.Observation.Runtime.FactHash = PrimaryRuntimeRecoveryObservationHash(facts.Recovery.Observation.Runtime)
				facts.Recovery.Observation.FactHash = PrimaryRecoveryObservationHash(*facts.Recovery.Observation)

				decision := EvaluatePrimaryRecovery(facts)
				if resumable && decision.Disposition != RecoveryDispositionAdoptExisting {
					t.Errorf("concurrent resumable want adopt_existing, got %s", decision.Disposition)
				}
				if !resumable && decision.Disposition != RecoveryDispositionAuthorizeReplace {
					t.Errorf("concurrent non-resumable want authorize_replacement, got %s", decision.Disposition)
				}
			}
		}(g)
	}
	wg.Wait()
}

func TestPrimaryRecoveryPropertyAndMutationExhaustion(t *testing.T) {
	// Any individual corruption of the resumable fact set must prevent adopt_existing.
	cases := []struct {
		name   string
		mutate func(*PrimaryRecoveryFacts)
	}{
		{
			name: "prompt missing",
			mutate: func(f *PrimaryRecoveryFacts) {
				f.Recovery.Observation.Host.Inventory.Agents[0].PromptPresent = false
			},
		},
		{
			name: "bootstrap missing",
			mutate: func(f *PrimaryRecoveryFacts) {
				f.Recovery.Observation.Host.Inventory.Agents[0].BootstrapPresent = false
			},
		},
		{
			name: "persistence reference missing",
			mutate: func(f *PrimaryRecoveryFacts) {
				f.Recovery.Observation.Host.Inventory.Agents[0].PersistenceReferencePresent = false
			},
		},
		{
			name: "archived agent",
			mutate: func(f *PrimaryRecoveryFacts) {
				f.Recovery.Observation.Host.Inventory.Agents[0].ArchivedAtPresent = true
			},
		},
		{
			name: "closed status",
			mutate: func(f *PrimaryRecoveryFacts) {
				f.Recovery.Observation.Host.Inventory.Agents[0].Status = "closed"
			},
		},
		{
			name: "errored status",
			mutate: func(f *PrimaryRecoveryFacts) {
				f.Recovery.Observation.Host.Inventory.Agents[0].Status = "error"
			},
		},
		{
			name: "parent present",
			mutate: func(f *PrimaryRecoveryFacts) {
				f.Recovery.Observation.Host.Inventory.Agents[0].ParentPresent = true
			},
		},
		{
			name: "title not exact",
			mutate: func(f *PrimaryRecoveryFacts) {
				f.Recovery.Observation.Host.Inventory.Agents[0].TitleExact = false
			},
		},
		{
			name: "worktree not exact",
			mutate: func(f *PrimaryRecoveryFacts) {
				f.Recovery.Observation.Host.Inventory.Agents[0].WorktreeExact = false
			},
		},
		{
			name: "profile not exact",
			mutate: func(f *PrimaryRecoveryFacts) {
				f.Recovery.Observation.Host.Inventory.Agents[0].ProfileExact = false
			},
		},
		{
			name: "session not exact",
			mutate: func(f *PrimaryRecoveryFacts) {
				f.Recovery.Observation.Host.Inventory.Agents[0].SessionExact = false
			},
		},
		{
			name: "runtime repository not exact",
			mutate: func(f *PrimaryRecoveryFacts) {
				f.Recovery.Observation.Runtime.RepositoryExact = false
			},
		},
		{
			name: "runtime worktree not exact",
			mutate: func(f *PrimaryRecoveryFacts) {
				f.Recovery.Observation.Runtime.WorktreeExact = false
			},
		},
		{
			name: "runtime branch not exact",
			mutate: func(f *PrimaryRecoveryFacts) {
				f.Recovery.Observation.Runtime.BranchExact = false
			},
		},
		{
			name: "runtime base not exact",
			mutate: func(f *PrimaryRecoveryFacts) {
				f.Recovery.Observation.Runtime.BaseExact = false
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			facts := recoveryFactsForTest(ProviderFailurePolicy)
			agent := &facts.Recovery.Observation.Host.Inventory.Agents[0]
			agent.Status = "idle"
			agent.ArchivedAtPresent = false
			agent.BootstrapPresent = true
			agent.PromptPresent = true
			agent.PersistenceReferencePresent = true

			tc.mutate(&facts)

			facts.Recovery.Observation.Host.FactHash = EffectObservationHash(facts.Recovery.Observation.Host)
			facts.Recovery.Observation.Runtime.FactHash = PrimaryRuntimeRecoveryObservationHash(facts.Recovery.Observation.Runtime)
			facts.Recovery.Observation.FactHash = PrimaryRecoveryObservationHash(*facts.Recovery.Observation)

			decision := EvaluatePrimaryRecovery(facts)
			if decision.Disposition == RecoveryDispositionAdoptExisting {
				t.Fatalf("case %s unexpectedly adopted existing", tc.name)
			}
		})
	}
}
