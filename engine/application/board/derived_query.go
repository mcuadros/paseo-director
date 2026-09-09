// SPDX-License-Identifier: Apache-2.0

package board

import (
	"context"
	"fmt"

	"github.com/mcuadros/director-engine/projection"
)

// DerivedFactSource exposes normalized durable/external facts, never a
// caller-selected lane or precomputed state. TaskStore-backed implementations
// remain engine-side and policy-free; projection owns every reduction.
type DerivedFactSource interface {
	LatestEventSequence(context.Context) (uint64, error)
	TaskProjectionInputs(context.Context) ([]projection.TaskProjectionInput, error)
}

// DerivedReader serves the complete M2 Board/List query without changing the
// M1 host connector contract. A future connector may transport its result but
// cannot derive or mutate it.
type DerivedReader struct {
	source DerivedFactSource
}

func NewDerivedReader(source DerivedFactSource) *DerivedReader {
	return &DerivedReader{source: source}
}

// Query returns only a cursor-stable snapshot. Filtering, state reduction,
// canonical sorting, and cursor validation remain pure projection behavior.
func (reader *DerivedReader) Query(ctx context.Context, query projection.TaskQuery) (projection.TaskPage, error) {
	for range snapshotReadAttempts {
		before, err := reader.source.LatestEventSequence(ctx)
		if err != nil {
			return projection.TaskPage{}, fmt.Errorf("read derived Task cursor: %w", err)
		}
		inputs, err := reader.source.TaskProjectionInputs(ctx)
		if err != nil {
			return projection.TaskPage{}, fmt.Errorf("read derived Task facts: %w", err)
		}
		after, err := reader.source.LatestEventSequence(ctx)
		if err != nil {
			return projection.TaskPage{}, fmt.Errorf("read derived Task cursor: %w", err)
		}
		if before != after {
			continue
		}
		page, err := projection.QueryTaskProjections(inputs, after, query)
		if err != nil {
			return projection.TaskPage{}, fmt.Errorf("query derived Tasks: %w", err)
		}
		return page, nil
	}
	return projection.TaskPage{}, ErrSnapshotChanged
}
