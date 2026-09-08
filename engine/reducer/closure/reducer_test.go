// SPDX-License-Identifier: Apache-2.0

package closure

import (
	"testing"

	"github.com/mcuadros/director-engine/domain/execution"
)

func closureFacts() Facts {
	return Facts{
		SchemaVersion:             SchemaVersion,
		FakeTerminalRung:          true,
		CandidateCurrent:          true,
		CriteriaClaimsSatisfied:   true,
		ExactOwnership:            true,
		OperationalLimitsAdmitted: true,
		OperationalObservationID:  "periodic-1",
		TaskStoreNowMillis:        1_001,
		AgentArchive: execution.Effect{
			ID: "agent-archive", Kind: execution.EffectAgentArchive,
			Phase: execution.EffectIntentRecorded, AttemptLimit: 2,
		},
	}
}

func TestReduceOrdersTerminalAgentViewAndDirectorWorktreeCleanup(t *testing.T) {
	facts := closureFacts()
	if decision := Reduce(facts); decision.Kind != DecisionObserve || decision.EffectKind != execution.EffectAgentArchive {
		t.Fatalf("agent cleanup = %#v", decision)
	}
	facts.AgentArchive.Phase = execution.EffectComplete
	facts.AgentArchive.ExternalID = "agent-1"
	facts.HostViewArchive = execution.Effect{
		ID: "host-view-archive", Kind: execution.EffectHostViewArchive,
		Phase: execution.EffectIntentRecorded, AttemptLimit: 2,
	}
	if decision := Reduce(facts); decision.EffectKind != execution.EffectHostViewArchive {
		t.Fatalf("view cleanup = %#v", decision)
	}
	facts.HostViewArchive.Phase = execution.EffectComplete
	facts.HostViewArchive.ExternalID = "workspace-1"
	facts.WorktreeRemove = execution.Effect{
		ID: "worktree-remove", Kind: execution.EffectWorktreeRemove,
		Phase: execution.EffectIntentRecorded, AttemptLimit: 1,
	}
	if decision := Reduce(facts); decision.EffectKind != execution.EffectWorktreeRemove {
		t.Fatalf("worktree cleanup = %#v", decision)
	}
	facts.WorktreeRemove.Phase = execution.EffectComplete
	facts.WorktreeRemove.ExternalID = "worktree-1"
	if decision := Reduce(facts); decision.Kind != DecisionTerminal {
		t.Fatalf("terminal decision = %#v", decision)
	}
}

func TestReduceNeverTreatsNeedsYouAsCleanupAuthority(t *testing.T) {
	for name, mutate := range map[string]func(*Facts){
		"limits": func(facts *Facts) {
			facts.OperationalLimitsAdmitted = false
			facts.OperationalNeedCode = execution.NeedProcessLimit
		},
		"ownership": func(facts *Facts) { facts.ExactOwnership = false },
		"candidate": func(facts *Facts) { facts.CandidateCurrent = false },
	} {
		t.Run(name, func(t *testing.T) {
			facts := closureFacts()
			mutate(&facts)
			decision := Reduce(facts)
			if decision.Kind != DecisionEscalate || decision.CleanupAuthorized {
				t.Fatalf("cleanup refusal = %#v", decision)
			}
		})
	}
}
