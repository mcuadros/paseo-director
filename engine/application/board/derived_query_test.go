// SPDX-License-Identifier: Apache-2.0

package board

import (
	"context"
	"errors"
	"testing"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/projection"
)

type derivedFactStore struct {
	inputs  []projection.TaskProjectionInput
	cursors []uint64
	reads   int
	err     error
}

func (store *derivedFactStore) LatestEventSequence(context.Context) (uint64, error) {
	if store.err != nil {
		return 0, store.err
	}
	if len(store.cursors) == 0 {
		return 0, nil
	}
	index := min(store.reads, len(store.cursors)-1)
	store.reads++
	return store.cursors[index], nil
}

func (store *derivedFactStore) TaskProjectionInputs(context.Context) ([]projection.TaskProjectionInput, error) {
	if store.err != nil {
		return nil, store.err
	}
	return append([]projection.TaskProjectionInput(nil), store.inputs...), nil
}

func derivedQueuedInput(id string) projection.TaskProjectionInput {
	return projection.TaskProjectionInput{
		TaskID: id, ProjectID: "project-1", WorkspaceID: "workspace-1", Title: "Queued Task",
		Priority: domain.PriorityNormal, QueuedAtUnixMillis: 1,
		Facts: projection.TaskStateFacts{
			TaskID: id, TaskVersion: 1,
			Eligibility: projection.EligibilityFact{
				Status: projection.FactCurrent, TaskVersion: 1, Decision: projection.EligibilityDependencyWait,
			},
			Run:        projection.RunFact{Status: projection.FactMissing},
			Candidate:  projection.CandidateFact{Status: projection.FactMissing},
			Validation: projection.ValidationFact{Status: projection.FactMissing},
			Review:     projection.ReviewFact{Status: projection.FactMissing},
			Feedback:   projection.FeedbackFact{Status: projection.FactMissing},
			Delivery:   projection.DeliveryFact{Status: projection.FactMissing},
			Cleanup:    projection.CleanupFact{Status: projection.FactMissing},
			HumanInput: projection.HumanInputFact{
				Status: projection.FactCurrent, TaskVersion: 1, State: projection.HumanInputNone,
			},
			Terminal: projection.TerminalFact{
				Status: projection.FactCurrent, TaskVersion: 1, State: projection.TerminalOpen,
			},
		},
	}
}

func TestDerivedReaderReturnsOnlyStableEngineProjection(t *testing.T) {
	store := &derivedFactStore{
		inputs:  []projection.TaskProjectionInput{derivedQueuedInput("task-1")},
		cursors: []uint64{7, 8, 8, 8},
	}
	page, err := NewDerivedReader(store).Query(context.Background(), projection.TaskQuery{Limit: 10})
	if err != nil || page.SnapshotCursor != "8" || len(page.Tasks) != 1 ||
		page.Tasks[0].Projection.State != projection.StateQueued || store.reads != 4 {
		t.Fatalf("derived page = %#v, reads=%d, error=%v", page, store.reads, err)
	}
}

func TestDerivedReaderFailsClosedOnMovingSnapshotAndSourceFailure(t *testing.T) {
	moving := &derivedFactStore{
		inputs:  []projection.TaskProjectionInput{derivedQueuedInput("task-1")},
		cursors: []uint64{1, 2, 3, 4, 5, 6},
	}
	if _, err := NewDerivedReader(moving).Query(context.Background(), projection.TaskQuery{Limit: 10}); !errors.Is(err, ErrSnapshotChanged) {
		t.Fatalf("moving snapshot error = %v", err)
	}
	expected := errors.New("fact source unavailable")
	if _, err := NewDerivedReader(&derivedFactStore{err: expected}).Query(
		context.Background(), projection.TaskQuery{Limit: 10},
	); !errors.Is(err, expected) {
		t.Fatalf("source error = %v", err)
	}
}
