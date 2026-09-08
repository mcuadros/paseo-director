// SPDX-License-Identifier: Apache-2.0

package board

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/projection"
)

type memoryStore struct {
	projects          []domain.Project
	tasks             map[string][]domain.Task
	runs              map[string][]domain.Run
	candidates        map[string]domain.Candidate
	cursor            uint64
	err               error
	candidateFailures []error
	candidateCalls    int
}

type changingCursorStore struct {
	*memoryStore
	cursors []uint64
	reads   int
}

func (store *changingCursorStore) LatestEventSequence(context.Context) (uint64, error) {
	if store.reads >= len(store.cursors) {
		return store.cursors[len(store.cursors)-1], nil
	}
	cursor := store.cursors[store.reads]
	store.reads++
	return cursor, nil
}

func (store *memoryStore) Projects(context.Context) ([]domain.Project, error) {
	if store.err != nil {
		return nil, store.err
	}
	return append([]domain.Project(nil), store.projects...), nil
}

func (store *memoryStore) Tasks(_ context.Context, projectID string) ([]domain.Task, error) {
	if store.err != nil {
		return nil, store.err
	}
	return append([]domain.Task(nil), store.tasks[projectID]...), nil
}

func (store *memoryStore) Runs(_ context.Context, taskID string) ([]domain.Run, error) {
	if store.err != nil {
		return nil, store.err
	}
	return append([]domain.Run(nil), store.runs[taskID]...), nil
}

func (store *memoryStore) Candidate(_ context.Context, candidateID string) (domain.Candidate, error) {
	store.candidateCalls++
	if store.candidateCalls <= len(store.candidateFailures) {
		if failure := store.candidateFailures[store.candidateCalls-1]; failure != nil {
			return domain.Candidate{}, failure
		}
	}
	if store.err != nil {
		return domain.Candidate{}, store.err
	}
	candidate, ok := store.candidates[candidateID]
	if !ok {
		return domain.Candidate{}, errors.New("candidate absent")
	}
	return candidate, nil
}

func (store *memoryStore) LatestEventSequence(context.Context) (uint64, error) {
	if store.err != nil {
		return 0, store.err
	}
	return store.cursor, nil
}

func TestReadDerivesWalkingSkeletonStateFromPersistedFacts(t *testing.T) {
	store := &memoryStore{
		projects: []domain.Project{
			{ID: "project-z", Name: "Zulu", State: "active"},
			{ID: "project-a", Name: "Alpha", State: "active"},
		},
		tasks: map[string][]domain.Task{
			"project-a": {
				{ID: "task-candidate", ProjectID: "project-a", Title: "Candidate task"},
				{ID: "task-building", ProjectID: "project-a", Title: "Building task"},
			},
			"project-z": {
				{ID: "task-queued", ProjectID: "project-z", Title: "Queued task"},
			},
		},
		runs: map[string][]domain.Run{
			"task-building": {
				{ID: "run-building", TaskID: "task-building", Number: 1},
			},
			"task-candidate": {
				{ID: "run-old", TaskID: "task-candidate", Number: 1},
				{ID: "run-ready", TaskID: "task-candidate", Number: 2, CurrentCandidateID: "candidate-ready"},
			},
		},
		candidates: map[string]domain.Candidate{
			"candidate-ready": {
				ID: "candidate-ready", RunID: "run-ready", Sequence: 1,
				CommitSHA: "abcdef0123456789abcdef0123456789abcdef01",
			},
		},
		cursor: 9,
	}

	snapshot, err := NewReader(store).Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if snapshot.SchemaVersion != 1 || snapshot.Cursor != "9" {
		t.Fatalf("snapshot identity = %#v", snapshot)
	}
	if len(snapshot.Tasks) != 3 {
		t.Fatalf("tasks = %#v", snapshot.Tasks)
	}
	if snapshot.Tasks[0].ID != "task-building" || snapshot.Tasks[0].State != projection.StateBuilding || snapshot.Tasks[0].RunNumber == nil || *snapshot.Tasks[0].RunNumber != "1" {
		t.Fatalf("building task = %#v", snapshot.Tasks[0])
	}
	if snapshot.Tasks[1].ID != "task-candidate" || snapshot.Tasks[1].State != projection.StateValidating || snapshot.Tasks[1].CandidateSHA == nil || *snapshot.Tasks[1].CandidateSHA != "abcdef0123456789abcdef0123456789abcdef01" {
		t.Fatalf("Candidate task = %#v", snapshot.Tasks[1])
	}
	if snapshot.Tasks[2].ID != "task-queued" || snapshot.Tasks[2].State != projection.StateQueued || snapshot.Tasks[2].RunNumber != nil {
		t.Fatalf("queued task = %#v", snapshot.Tasks[2])
	}
}

func TestReadReflectsStoreUpdatesWithoutClientSideStateDerivation(t *testing.T) {
	store := &memoryStore{
		projects: []domain.Project{{ID: "project-1", Name: "Director", State: "active"}},
		tasks: map[string][]domain.Task{
			"project-1": {{ID: "task-1", ProjectID: "project-1", Title: "First title"}},
		},
		runs:       map[string][]domain.Run{},
		candidates: map[string]domain.Candidate{},
		cursor:     2,
	}
	reader := NewReader(store)

	first, err := reader.Read(context.Background())
	if err != nil {
		t.Fatalf("first Read() error = %v", err)
	}
	store.tasks["project-1"][0].Title = "Updated title"
	store.runs["task-1"] = []domain.Run{{
		ID: "run-1", TaskID: "task-1", Number: 1,
	}}
	store.cursor = 4
	second, err := reader.Read(context.Background())
	if err != nil {
		t.Fatalf("second Read() error = %v", err)
	}

	if first.Cursor != "2" || first.Tasks[0].State != projection.StateQueued {
		t.Fatalf("first snapshot = %#v", first)
	}
	if second.Cursor != "4" || second.Tasks[0].Title != "Updated title" || second.Tasks[0].State != projection.StateBuilding {
		t.Fatalf("updated snapshot = %#v", second)
	}
}

func TestReadHandlesEmptyAndStoreFailure(t *testing.T) {
	empty, err := NewReader(&memoryStore{}).Read(context.Background())
	if err != nil || empty.Cursor != "0" || len(empty.Tasks) != 0 {
		t.Fatalf("empty snapshot = %#v, %v", empty, err)
	}

	expected := errors.New("store unavailable")
	if _, err := NewReader(&memoryStore{err: expected}).Read(context.Background()); !errors.Is(err, expected) {
		t.Fatalf("Read() error = %v, want %v", err, expected)
	}
}

func TestReadEnforcesTheBoundedM1Surface(t *testing.T) {
	tasks := make([]domain.Task, projection.MaximumBoardTasks+1)
	for index := range tasks {
		tasks[index] = domain.Task{
			ID: fmt.Sprintf("task-%04d", index), ProjectID: "project-1", Title: "Task",
		}
	}
	store := &memoryStore{
		projects: []domain.Project{{ID: "project-1", Name: "Director", State: "active"}},
		tasks:    map[string][]domain.Task{"project-1": tasks},
	}
	if _, err := NewReader(store).Read(context.Background()); !errors.Is(err, ErrMaximumTasks) {
		t.Fatalf("Read() error = %v, want %v", err, ErrMaximumTasks)
	}
}

func TestReadReturnsOnlyACursorStableSnapshot(t *testing.T) {
	base := &memoryStore{
		projects: []domain.Project{{ID: "project-1", Name: "Director", State: "active"}},
		tasks: map[string][]domain.Task{
			"project-1": {{ID: "task-1", ProjectID: "project-1", Title: "Task"}},
		},
		runs: map[string][]domain.Run{},
	}
	settles := &changingCursorStore{memoryStore: base, cursors: []uint64{1, 2, 2, 2}}
	snapshot, err := NewReader(settles).Read(context.Background())
	if err != nil || snapshot.Cursor != "2" || settles.reads != 4 {
		t.Fatalf("settled snapshot = %#v, cursor reads = %d, error = %v", snapshot, settles.reads, err)
	}

	changes := &changingCursorStore{memoryStore: base, cursors: []uint64{1, 2, 3, 4, 5, 6}}
	if _, err := NewReader(changes).Read(context.Background()); !errors.Is(err, ErrSnapshotChanged) {
		t.Fatalf("changing snapshot error = %v", err)
	}
}

func TestReadRetriesReferentialTornReadsWithinTheSnapshotBudget(t *testing.T) {
	base := &memoryStore{
		projects: []domain.Project{{ID: "project-1", Name: "Director"}},
		tasks: map[string][]domain.Task{
			"project-1": {{ID: "task-1", ProjectID: "project-1", Title: "Task"}},
		},
		runs: map[string][]domain.Run{
			"task-1": {{ID: "run-1", TaskID: "task-1", Number: 1, CurrentCandidateID: "candidate-1"}},
		},
		candidates: map[string]domain.Candidate{
			"candidate-1": {ID: "candidate-1", RunID: "run-1", CommitSHA: "abcdef0123456789abcdef0123456789abcdef01"},
		},
		cursor: 4,
	}
	base.candidateFailures = []error{errors.New("candidate row not yet visible")}
	snapshot, err := NewReader(base).Read(context.Background())
	if err != nil || base.candidateCalls != 2 || snapshot.Tasks[0].State != projection.StateValidating {
		t.Fatalf("retried snapshot = %#v, calls = %d, error = %v", snapshot, base.candidateCalls, err)
	}

	base.candidateCalls = 0
	base.candidateFailures = []error{
		errors.New("torn 1"), errors.New("torn 2"), errors.New("torn 3"),
	}
	if _, err := NewReader(base).Read(context.Background()); !errors.Is(err, ErrSnapshotChanged) || base.candidateCalls != 3 {
		t.Fatalf("exhausted torn read error = %v, calls = %d", err, base.candidateCalls)
	}
}

func TestReadRejectsRunOwnedByAnotherTask(t *testing.T) {
	store := &memoryStore{
		projects: []domain.Project{{ID: "project-1", Name: "Director"}},
		tasks: map[string][]domain.Task{
			"project-1": {{ID: "task-1", ProjectID: "project-1", Title: "Task"}},
		},
		runs: map[string][]domain.Run{
			"task-1": {{ID: "run-1", TaskID: "another-task", Number: 1}},
		},
	}
	if _, err := NewReader(store).Read(context.Background()); !errors.Is(err, projection.ErrRunTaskMismatch) {
		t.Fatalf("Run ownership error = %v", err)
	}
}
