// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"errors"
	"testing"

	"github.com/mcuadros/director-engine/domain"
	domainexecution "github.com/mcuadros/director-engine/domain/execution"
)

func TestStartupRejectsExecutionIdentityAliasingAcrossRuns(t *testing.T) {
	runs := []durableStartupRun{
		{run: domain.Run{ID: "run-1", Execution: domainexecution.State{
			Worktree: domainexecution.Effect{ExternalID: "worktree-shared"},
		}}},
		{run: domain.Run{ID: "run-2", Execution: domainexecution.State{
			Worktree: domainexecution.Effect{ExternalID: "worktree-shared"},
		}}},
	}
	if err := duplicateExecutionIdentity(runs); err == nil {
		t.Fatal("duplicate worktree identity was admitted")
	}
	runs[1].run.Execution.Worktree.ExternalID = "worktree-2"
	runs[0].run.Execution.Agent.ExternalID = "agent-shared"
	runs[1].run.Execution.Agent.ExternalID = "agent-shared"
	if err := duplicateExecutionIdentity(runs); err == nil {
		t.Fatal("duplicate agent identity was admitted")
	}
}

func TestEffectHandoffErrorRetainsUnknownAdapterResult(t *testing.T) {
	underlying := errors.New("adapter result lost")
	failure := &EffectHandoffError{Kind: domainexecution.EffectAgentCreate, err: underlying}
	if !errors.Is(failure, underlying) || failure.Error() != "task_agent.create_with_bootstrap outcome requires reconciliation" {
		t.Fatalf("handoff failure = %v", failure)
	}
}
