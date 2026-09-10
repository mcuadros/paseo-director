// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"context"
	"errors"
	"fmt"

	"github.com/mcuadros/director-engine/domain"
	domainexecution "github.com/mcuadros/director-engine/domain/execution"
)

// RecordControlledAgentTerminalEvent records a helper/Reviewer terminal wake
// against an already engine-registered containment target. It clears only a
// replaceable observation; the next step must perform a fresh host read.
func (controller *Controller) RecordControlledAgentTerminalEvent(
	ctx context.Context, runID string, event domainexecution.CompletionEvent, nowMillis int64,
) error {
	run, err := controller.store.Run(ctx, runID)
	if err != nil {
		return err
	}
	if !domainexecution.ValidCompletionEvent(event) || event.BindingHash != run.Execution.RepositoryBindingHash ||
		event.ObservedAtMillis > nowMillis || nowMillis-event.ObservedAtMillis >= domainexecution.CompletionDispatchTargetMillis {
		return errors.New("controlled agent terminal event is invalid")
	}
	index := -1
	for current := range run.Execution.Control.Targets {
		if run.Execution.Control.Targets[current].Identity.ID == event.AgentID {
			index = current
			break
		}
	}
	if index < 0 {
		return errors.New("controlled agent terminal event is outside the Run")
	}
	target := run.Execution.Control.Targets[index]
	if target.TerminalEventID != "" {
		if target.TerminalEventID == event.ID && target.TerminalEventHash == event.FactHash {
			return nil
		}
		return errors.New("controlled agent terminal event identity conflicts")
	}
	if event.Cursor <= target.TerminalCursor {
		return errors.New("controlled agent terminal event is stale")
	}
	next := run
	updated := &next.Execution.Control.Targets[index]
	updated.TerminalEventID = event.ID
	updated.TerminalEventHash = event.FactHash
	updated.TerminalCursor = event.Cursor
	if updated.Boundary.Observation != nil && updated.Boundary.Observation.Status == domainexecution.ObservationOwnedPresent {
		updated.Boundary.Observation = nil
	}
	if updated.Archive.Observation != nil && updated.Archive.Observation.Status == domainexecution.ObservationOwnedPresent {
		updated.Archive.Observation = nil
	}
	next.Version = run.Version + 1
	transition := "run.control.terminal_wake"
	commandID := stableID("command", run.ID, fmt.Sprintf("version-%d", next.Version), transition)
	result, err := controller.store.UpdateRun(ctx, domain.CommandRequest{
		IdempotencyKey: commandID, Type: transition, AggregateID: run.ID,
		ExpectedVersion: run.Version, Payload: eventPayload(struct {
			Transition string `json:"transition"`
			StateHash  string `json:"stateHash"`
		}{transition, hashState(next.Execution)}),
	}, next, domain.Event{
		ID: stableID("event", commandID), RunID: run.ID, Sequence: next.Version + 1,
		AggregateID: run.ID, AggregateVersion: next.Version, Type: transition,
		Payload: durableTransitionPayload(transition, next.Execution),
	})
	if err != nil {
		return err
	}
	if result.Outcome != domain.CommandApplied && !result.Replay {
		return errors.New("controlled agent terminal event lost its CAS")
	}
	return nil
}
