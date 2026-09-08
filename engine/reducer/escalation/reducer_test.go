// SPDX-License-Identifier: Apache-2.0

package escalation

import (
	"testing"

	"github.com/mcuadros/director-engine/domain/execution"
)

func TestReduceIsTheOnlyNeedsYouEmissionForReconciledTypedCauses(t *testing.T) {
	facts := Facts{
		SchemaVersion: SchemaVersion,
		Scope:         execution.Scope{ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "task-1", RunID: "run-1"},
		CauseCode:     execution.NeedMemoryLimit,
		Reconciled:    true,
		WakeCondition: "fresh_operational_observation",
	}
	first := Reduce(facts)
	second := Reduce(facts)
	if first != second || first.Kind != DecisionNeedsYou || first.NeedsYou.Code != execution.NeedMemoryLimit {
		t.Fatalf("escalation decision = %#v %#v", first, second)
	}
	if first.NeedsYou.CleanupAuthorized || first.DecisionID == "" {
		t.Fatalf("escalation granted cleanup or lacked identity: %#v", first)
	}
}

func TestReduceRefusesUnreconciledOrUnboundedCause(t *testing.T) {
	facts := Facts{SchemaVersion: SchemaVersion, CauseCode: execution.NeedMemoryLimit}
	if decision := Reduce(facts); decision.Kind != DecisionRefuse {
		t.Fatalf("unreconciled cause = %#v", decision)
	}
	facts.Reconciled = true
	facts.WakeCondition = "fresh_observation"
	if decision := Reduce(facts); decision.Kind != DecisionRefuse {
		t.Fatalf("missing scope = %#v", decision)
	}
}
