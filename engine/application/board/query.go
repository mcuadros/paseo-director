// SPDX-License-Identifier: Apache-2.0

// Package board loads persisted facts and builds the engine-owned Board/List
// read model. It performs no host calls and exposes no TaskStore backend data.
package board

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/projection"
)

// ErrMaximumTasks marks the deliberately bounded M1 query without claiming
// the production scale deferred to dir-m5.10.
var ErrMaximumTasks = errors.New("board query exceeds the M1 task bound")

// ErrSnapshotChanged means the persisted facts did not stabilize across the
// bounded full reads, either through cursor movement or a torn reference.
var ErrSnapshotChanged = errors.New("board facts changed during snapshot read")

var errReferentialRead = errors.New("board referential read was transient")

const snapshotReadAttempts = 3

// Store is the read-only subset of the engine-owned TaskStore used by this
// application query.
type Store interface {
	Projects(context.Context) ([]domain.Project, error)
	Tasks(context.Context, string) ([]domain.Task, error)
	Runs(context.Context, string) ([]domain.Run, error)
	Candidate(context.Context, string) (domain.Candidate, error)
	LatestEventSequence(context.Context) (uint64, error)
}

// Reader loads Board snapshots from persisted domain facts.
type Reader struct {
	store Store
}

// NewReader binds the query to the engine-owned TaskStore read port.
func NewReader(store Store) *Reader {
	return &Reader{store: store}
}

// Read returns a full snapshot. Hosts render State as supplied and never
// reproduce this fact-to-state mapping.
func (reader *Reader) Read(ctx context.Context) (projection.Board, error) {
	for range snapshotReadAttempts {
		before, err := reader.store.LatestEventSequence(ctx)
		if err != nil {
			return projection.Board{}, fmt.Errorf("read Board cursor: %w", err)
		}
		snapshot, err := reader.readFacts(ctx)
		if err != nil {
			if errors.Is(err, errReferentialRead) {
				continue
			}
			return projection.Board{}, err
		}
		after, err := reader.store.LatestEventSequence(ctx)
		if err != nil {
			return projection.Board{}, fmt.Errorf("read Board cursor: %w", err)
		}
		if before == after {
			snapshot.Cursor = strconv.FormatUint(after, 10)
			return snapshot, nil
		}
	}
	return projection.Board{}, ErrSnapshotChanged
}

func (reader *Reader) readFacts(ctx context.Context) (projection.Board, error) {
	projects, err := reader.store.Projects(ctx)
	if err != nil {
		return projection.Board{}, fmt.Errorf("read Board projects: %w", err)
	}
	tasks := make([]projection.BoardTask, 0)
	for _, project := range projects {
		projectTasks, err := reader.store.Tasks(ctx, project.ID)
		if err != nil {
			return projection.Board{}, fmt.Errorf("read Board tasks: %w", err)
		}
		if len(tasks)+len(projectTasks) > projection.MaximumBoardTasks {
			return projection.Board{}, ErrMaximumTasks
		}
		for _, task := range projectTasks {
			row, err := reader.task(ctx, project, task)
			if err != nil {
				return projection.Board{}, err
			}
			tasks = append(tasks, row)
		}
	}
	slices.SortFunc(tasks, func(left, right projection.BoardTask) int {
		if byProject := compareText(left.ProjectName, right.ProjectName); byProject != 0 {
			return byProject
		}
		return compareText(left.ID, right.ID)
	})
	return projection.Board{
		SchemaVersion: projection.BoardSchemaVersion,
		Tasks:         tasks,
	}, nil
}

func (reader *Reader) task(ctx context.Context, project domain.Project, task domain.Task) (projection.BoardTask, error) {
	runs, err := reader.store.Runs(ctx, task.ID)
	if err != nil {
		return projection.BoardTask{}, fmt.Errorf("read Board runs: %w", err)
	}
	if len(runs) == 0 {
		return projection.DeriveBoardTask(projection.BoardFacts{Project: project, Task: task})
	}
	latest, _ := latestTaskRun(runs)
	if latest.TaskID != task.ID {
		return projection.BoardTask{}, projection.ErrRunTaskMismatch
	}
	if latest.CurrentCandidateID == "" {
		return projection.DeriveBoardTask(projection.BoardFacts{
			Project: project, Task: task, Run: &latest,
		})
	}
	candidate, err := reader.store.Candidate(ctx, latest.CurrentCandidateID)
	if err != nil {
		return projection.BoardTask{}, fmt.Errorf("%w: current Candidate unavailable", errReferentialRead)
	}
	return projection.DeriveBoardTask(projection.BoardFacts{
		Project: project, Task: task, Run: &latest, Candidate: &candidate,
	})
}

func compareText(left, right string) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}
