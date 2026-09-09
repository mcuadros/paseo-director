// SPDX-License-Identifier: Apache-2.0

package projectionoracle

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
)

const MaximumPageSize = 1000

var (
	ErrInvalidFixture  = errors.New("projection oracle fixture is invalid")
	ErrInvalidPageSize = errors.New("projection oracle page size is invalid")
	ErrInvalidCursor   = errors.New("projection oracle cursor is invalid")
	ErrCursorSnapshot  = errors.New("projection oracle cursor belongs to another snapshot")
)

// Priority is the closed Task priority used only to create deterministic
// expected-order fixtures. It does not make a scheduler or runtime decision.
type Priority string

const (
	PriorityUrgent Priority = "urgent"
	PriorityHigh   Priority = "high"
	PriorityNormal Priority = "normal"
	PriorityLow    Priority = "low"
)

// Fixture is one closed oracle input plus stable ordering facts.
type Fixture struct {
	Facts              Facts
	Priority           Priority
	QueuedAtUnixMillis int64
}

// ExpectedTask is one projected fixture in canonical order.
type ExpectedTask struct {
	TaskID             string
	Priority           Priority
	QueuedAtUnixMillis int64
	Projection         Projection
}

// Page is a deterministic, snapshot-bound fixture page.
type Page struct {
	SnapshotCursor string
	Tasks          []ExpectedTask
	NextCursor     string
}

type orderKey struct {
	laneRank           int
	priorityRank       int
	queuedAtUnixMillis int64
	taskID             string
}

type cursorPayload struct {
	SchemaVersion      int      `json:"schemaVersion"`
	SnapshotCursor     string   `json:"snapshotCursor"`
	State              State    `json:"state,omitempty"`
	Done               bool     `json:"done"`
	Priority           Priority `json:"priority"`
	QueuedAtUnixMillis string   `json:"queuedAtUnixMillis"`
	TaskID             string   `json:"taskId"`
}

// Ordered projects and sorts a copy of fixtures by lane display order,
// priority, queue time, and Task ID. The input and its nested claim slices are
// never mutated.
func Ordered(fixtures []Fixture) ([]ExpectedTask, error) {
	result := make([]ExpectedTask, 0, len(fixtures))
	seen := make(map[string]bool, len(fixtures))
	for _, fixture := range fixtures {
		if fixture.Facts.TaskID == "" || !knownPriority(fixture.Priority) || fixture.QueuedAtUnixMillis < 0 || seen[fixture.Facts.TaskID] {
			return nil, ErrInvalidFixture
		}
		seen[fixture.Facts.TaskID] = true
		result = append(result, ExpectedTask{
			TaskID:             fixture.Facts.TaskID,
			Priority:           fixture.Priority,
			QueuedAtUnixMillis: fixture.QueuedAtUnixMillis,
			Projection:         Project(fixture.Facts),
		})
	}
	slices.SortFunc(result, func(left, right ExpectedTask) int {
		return compareOrderKey(keyFor(left), keyFor(right))
	})
	return result, nil
}

// Paginate returns the first canonical page strictly after the supplied
// cursor. The cursor binds the complete ordering key to the decimal snapshot
// cursor, so a cursor cannot silently cross snapshots.
func Paginate(fixtures []Fixture, snapshot uint64, limit int, after string) (Page, error) {
	if limit < 1 || limit > MaximumPageSize {
		return Page{}, ErrInvalidPageSize
	}
	ordered, err := Ordered(fixtures)
	if err != nil {
		return Page{}, err
	}
	start := 0
	if after != "" {
		cursor, err := decodeCursor(after)
		if err != nil {
			return Page{}, err
		}
		if cursor.SnapshotCursor != strconv.FormatUint(snapshot, 10) {
			return Page{}, ErrCursorSnapshot
		}
		cursorKey, ok := keyForCursor(cursor)
		if !ok {
			return Page{}, ErrInvalidCursor
		}
		start = len(ordered)
		for index, task := range ordered {
			if compareOrderKey(keyFor(task), cursorKey) > 0 {
				start = index
				break
			}
		}
	}
	end := min(start+limit, len(ordered))
	page := Page{
		SnapshotCursor: strconv.FormatUint(snapshot, 10),
		Tasks:          cloneExpectedTasks(ordered[start:end]),
	}
	if end < len(ordered) {
		page.NextCursor, err = encodeCursor(snapshot, ordered[end-1])
		if err != nil {
			return Page{}, err
		}
	}
	return page, nil
}

func encodeCursor(snapshot uint64, task ExpectedTask) (string, error) {
	payload := cursorPayload{
		SchemaVersion:      SchemaVersion,
		SnapshotCursor:     strconv.FormatUint(snapshot, 10),
		State:              task.Projection.State,
		Done:               task.Projection.DoneMember,
		Priority:           task.Priority,
		QueuedAtUnixMillis: strconv.FormatInt(task.QueuedAtUnixMillis, 10),
		TaskID:             task.TaskID,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeCursor(encoded string) (cursorPayload, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return cursorPayload{}, ErrInvalidCursor
	}
	var payload cursorPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return cursorPayload{}, ErrInvalidCursor
	}
	canonical, err := json.Marshal(payload)
	if err != nil || base64.RawURLEncoding.EncodeToString(canonical) != encoded || payload.SchemaVersion != SchemaVersion {
		return cursorPayload{}, ErrInvalidCursor
	}
	return payload, nil
}

func keyFor(task ExpectedTask) orderKey {
	return orderKey{
		laneRank:           laneRank(task.Projection),
		priorityRank:       priorityRank(task.Priority),
		queuedAtUnixMillis: task.QueuedAtUnixMillis,
		taskID:             task.TaskID,
	}
}

func keyForCursor(cursor cursorPayload) (orderKey, bool) {
	queuedAt, err := strconv.ParseInt(cursor.QueuedAtUnixMillis, 10, 64)
	if err != nil || queuedAt < 0 || cursor.TaskID == "" || !knownPriority(cursor.Priority) {
		return orderKey{}, false
	}
	projection := Projection{State: cursor.State, DoneMember: cursor.Done}
	if cursor.Done {
		if cursor.State != "" {
			return orderKey{}, false
		}
	} else if !knownState(cursor.State) {
		return orderKey{}, false
	}
	return orderKey{
		laneRank:           laneRank(projection),
		priorityRank:       priorityRank(cursor.Priority),
		queuedAtUnixMillis: queuedAt,
		taskID:             cursor.TaskID,
	}, true
}

func compareOrderKey(left, right orderKey) int {
	switch {
	case left.laneRank < right.laneRank:
		return -1
	case left.laneRank > right.laneRank:
		return 1
	case left.priorityRank < right.priorityRank:
		return -1
	case left.priorityRank > right.priorityRank:
		return 1
	case left.queuedAtUnixMillis < right.queuedAtUnixMillis:
		return -1
	case left.queuedAtUnixMillis > right.queuedAtUnixMillis:
		return 1
	case left.taskID < right.taskID:
		return -1
	case left.taskID > right.taskID:
		return 1
	default:
		return 0
	}
}

func laneRank(projection Projection) int {
	if projection.DoneMember {
		return 6
	}
	switch projection.State {
	case StateNeedsYou:
		return 0
	case StateQueued:
		return 1
	case StateBuilding:
		return 2
	case StateValidating:
		return 3
	case StateInReview:
		return 4
	case StateReady:
		return 5
	default:
		return 7
	}
}

func priorityRank(priority Priority) int {
	switch priority {
	case PriorityUrgent:
		return 0
	case PriorityHigh:
		return 1
	case PriorityNormal:
		return 2
	case PriorityLow:
		return 3
	default:
		return 4
	}
}

func knownPriority(priority Priority) bool {
	return priority == PriorityUrgent || priority == PriorityHigh || priority == PriorityNormal || priority == PriorityLow
}

func knownState(state State) bool {
	switch state {
	case StateNeedsYou, StateQueued, StateBuilding, StateValidating, StateInReview, StateReady:
		return true
	default:
		return false
	}
}

func cloneExpectedTasks(tasks []ExpectedTask) []ExpectedTask {
	result := append([]ExpectedTask(nil), tasks...)
	for index := range result {
		result[index].Projection.Blockers = append([]BlockerCode(nil), result[index].Projection.Blockers...)
		result[index].Projection.Attention = append([]AttentionCode(nil), result[index].Projection.Attention...)
	}
	return result
}
