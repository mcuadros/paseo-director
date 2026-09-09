// SPDX-License-Identifier: Apache-2.0

package projection

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mcuadros/director-engine/domain"
)

var (
	ErrTaskQueryInvalid        = errors.New("task projection query is invalid")
	ErrTaskQueryCursorInvalid  = errors.New("task projection cursor is invalid")
	ErrTaskQueryCursorSnapshot = errors.New("task projection cursor belongs to another snapshot")
)

// TaskMembership selects Board lanes, completed List rows, or both. Done is
// deliberately membership rather than a mutable lane.
type TaskMembership string

const (
	TaskMembershipBoard TaskMembership = "board"
	TaskMembershipDone  TaskMembership = "done"
	TaskMembershipAll   TaskMembership = "all"
)

// TaskProjectionInput combines immutable display/filter metadata with the
// normalized fact set. It contains no caller-selected state or card position.
type TaskProjectionInput struct {
	TaskID             string          `json:"taskId"`
	ProjectID          string          `json:"projectId"`
	WorkspaceID        string          `json:"workspaceId"`
	EpicID             string          `json:"epicId,omitempty"`
	Title              string          `json:"title"`
	Priority           domain.Priority `json:"priority"`
	Labels             []string        `json:"labels,omitempty"`
	QueuedAtUnixMillis int64           `json:"queuedAtUnixMillis"`
	Facts              TaskStateFacts  `json:"facts"`
}

// TaskProjectionRow is one derived Board/List query result.
type TaskProjectionRow struct {
	TaskID             string          `json:"taskId"`
	ProjectID          string          `json:"projectId"`
	WorkspaceID        string          `json:"workspaceId"`
	EpicID             string          `json:"epicId,omitempty"`
	Title              string          `json:"title"`
	Priority           domain.Priority `json:"priority"`
	Labels             []string        `json:"labels,omitempty"`
	QueuedAtUnixMillis int64           `json:"queuedAtUnixMillis"`
	Projection         TaskProjection  `json:"projection"`
}

// TaskQuery is a bounded filter over one snapshot. Empty filter slices are
// wildcards. Labels are conjunctive; Attention codes are disjunctive.
type TaskQuery struct {
	Membership   TaskMembership    `json:"membership,omitempty"`
	ProjectIDs   []string          `json:"projectIds,omitempty"`
	WorkspaceIDs []string          `json:"workspaceIds,omitempty"`
	EpicIDs      []string          `json:"epicIds,omitempty"`
	Standalone   *bool             `json:"standalone,omitempty"`
	Priorities   []domain.Priority `json:"priorities,omitempty"`
	Labels       []string          `json:"labels,omitempty"`
	States       []BoardState      `json:"states,omitempty"`
	Attention    []AttentionCode   `json:"attention,omitempty"`
	Limit        int               `json:"limit"`
	After        string            `json:"after,omitempty"`
}

// TaskPage is one stable page. SnapshotCursor is decimal TaskStore event
// sequence; NextCursor additionally binds the filters and complete last-row
// ordering key.
type TaskPage struct {
	SnapshotCursor string              `json:"snapshotCursor"`
	Tasks          []TaskProjectionRow `json:"tasks"`
	NextCursor     string              `json:"nextCursor,omitempty"`
}

type taskQueryCursor struct {
	Version            int             `json:"v"`
	SnapshotCursor     string          `json:"snapshot"`
	FilterHash         string          `json:"filter"`
	Done               bool            `json:"done"`
	State              BoardState      `json:"state,omitempty"`
	Priority           domain.Priority `json:"priority"`
	QueuedAtUnixMillis int64           `json:"queuedAt"`
	TaskID             string          `json:"taskId"`
}

type normalizedTaskQuery struct {
	Membership   TaskMembership    `json:"membership"`
	ProjectIDs   []string          `json:"projectIds,omitempty"`
	WorkspaceIDs []string          `json:"workspaceIds,omitempty"`
	EpicIDs      []string          `json:"epicIds,omitempty"`
	Standalone   *bool             `json:"standalone,omitempty"`
	Priorities   []domain.Priority `json:"priorities,omitempty"`
	Labels       []string          `json:"labels,omitempty"`
	States       []BoardState      `json:"states,omitempty"`
	Attention    []AttentionCode   `json:"attention,omitempty"`
}

func normalizeUniqueText(values []string) ([]string, bool) {
	if len(values) > MaximumBoardTasks {
		return nil, false
	}
	result := append([]string(nil), values...)
	sort.Strings(result)
	for index, value := range result {
		if !validTaskQueryText(value, 512) || (index > 0 && result[index-1] == value) {
			return nil, false
		}
	}
	return result, true
}

func validTaskQueryText(value string, maximum int) bool {
	return len(value) > 0 && len(value) <= maximum && utf8.ValidString(value) &&
		value == strings.TrimSpace(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func normalizeTaskQuery(query TaskQuery) (normalizedTaskQuery, error) {
	result := normalizedTaskQuery{Membership: query.Membership}
	if result.Membership == "" {
		result.Membership = TaskMembershipBoard
	}
	if result.Membership != TaskMembershipBoard && result.Membership != TaskMembershipDone &&
		result.Membership != TaskMembershipAll {
		return normalizedTaskQuery{}, ErrTaskQueryInvalid
	}
	var ok bool
	if result.ProjectIDs, ok = normalizeUniqueText(query.ProjectIDs); !ok {
		return normalizedTaskQuery{}, ErrTaskQueryInvalid
	}
	if result.WorkspaceIDs, ok = normalizeUniqueText(query.WorkspaceIDs); !ok {
		return normalizedTaskQuery{}, ErrTaskQueryInvalid
	}
	if result.EpicIDs, ok = normalizeUniqueText(query.EpicIDs); !ok {
		return normalizedTaskQuery{}, ErrTaskQueryInvalid
	}
	if result.Labels, ok = normalizeUniqueText(query.Labels); !ok {
		return normalizedTaskQuery{}, ErrTaskQueryInvalid
	}
	if query.Standalone != nil {
		value := *query.Standalone
		result.Standalone = &value
	}
	priorityText := make([]string, 0, len(query.Priorities))
	for _, priority := range query.Priorities {
		if priority != domain.PriorityUrgent && priority != domain.PriorityHigh &&
			priority != domain.PriorityNormal && priority != domain.PriorityLow {
			return normalizedTaskQuery{}, ErrTaskQueryInvalid
		}
		priorityText = append(priorityText, string(priority))
	}
	priorityText, ok = normalizeUniqueText(priorityText)
	if !ok {
		return normalizedTaskQuery{}, ErrTaskQueryInvalid
	}
	for _, priority := range priorityText {
		result.Priorities = append(result.Priorities, domain.Priority(priority))
	}
	stateText := make([]string, 0, len(query.States))
	for _, state := range query.States {
		if stateIndex(state) < 0 {
			return normalizedTaskQuery{}, ErrTaskQueryInvalid
		}
		stateText = append(stateText, string(state))
	}
	stateText, ok = normalizeUniqueText(stateText)
	if !ok {
		return normalizedTaskQuery{}, ErrTaskQueryInvalid
	}
	for _, state := range stateText {
		result.States = append(result.States, BoardState(state))
	}
	attentionText := make([]string, 0, len(query.Attention))
	for _, code := range query.Attention {
		if !validAttention(code) {
			return normalizedTaskQuery{}, ErrTaskQueryInvalid
		}
		attentionText = append(attentionText, string(code))
	}
	attentionText, ok = normalizeUniqueText(attentionText)
	if !ok {
		return normalizedTaskQuery{}, ErrTaskQueryInvalid
	}
	for _, code := range attentionText {
		result.Attention = append(result.Attention, AttentionCode(code))
	}
	return result, nil
}

func taskQueryHash(query normalizedTaskQuery) string {
	encoded, err := json.Marshal(query)
	if err != nil {
		panic("marshal normalized Task query: " + err.Error())
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func stateIndex(state BoardState) int {
	for index, candidate := range BoardStates() {
		if candidate == state {
			return index
		}
	}
	return -1
}

func priorityIndex(priority domain.Priority) int {
	switch domain.EffectivePriority(priority) {
	case domain.PriorityUrgent:
		return 0
	case domain.PriorityHigh:
		return 1
	case domain.PriorityNormal:
		return 2
	case domain.PriorityLow:
		return 3
	default:
		return 4
	}
}

func compareTaskRows(left, right TaskProjectionRow) int {
	leftDone, rightDone := left.Projection.DoneMember, right.Projection.DoneMember
	if leftDone != rightDone {
		if leftDone {
			return 1
		}
		return -1
	}
	if !leftDone {
		if comparison := stateIndex(left.Projection.State) - stateIndex(right.Projection.State); comparison != 0 {
			return comparison
		}
	}
	if comparison := priorityIndex(left.Priority) - priorityIndex(right.Priority); comparison != 0 {
		return comparison
	}
	if left.QueuedAtUnixMillis < right.QueuedAtUnixMillis {
		return -1
	}
	if left.QueuedAtUnixMillis > right.QueuedAtUnixMillis {
		return 1
	}
	return compareText(left.TaskID, right.TaskID)
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

func compareRowCursor(row TaskProjectionRow, cursor taskQueryCursor) int {
	return compareTaskRows(row, TaskProjectionRow{
		TaskID: cursor.TaskID, Priority: cursor.Priority,
		QueuedAtUnixMillis: cursor.QueuedAtUnixMillis,
		Projection:         TaskProjection{State: cursor.State, DoneMember: cursor.Done},
	})
}

func encodeTaskCursor(snapshot uint64, filterHash string, row TaskProjectionRow) string {
	cursor := taskQueryCursor{
		Version: 1, SnapshotCursor: strconv.FormatUint(snapshot, 10), FilterHash: filterHash,
		Done: row.Projection.DoneMember, State: row.Projection.State,
		Priority: domain.EffectivePriority(row.Priority), QueuedAtUnixMillis: row.QueuedAtUnixMillis,
		TaskID: row.TaskID,
	}
	encoded, err := json.Marshal(cursor)
	if err != nil {
		panic("marshal Task cursor: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeTaskCursor(value string) (taskQueryCursor, error) {
	if len(value) == 0 || len(value) > 5500 {
		return taskQueryCursor{}, ErrTaskQueryCursorInvalid
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) == 0 || len(decoded) > 4096 {
		return taskQueryCursor{}, ErrTaskQueryCursorInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	var cursor taskQueryCursor
	if err := decoder.Decode(&cursor); err != nil {
		return taskQueryCursor{}, ErrTaskQueryCursorInvalid
	}
	snapshot, snapshotErr := strconv.ParseUint(cursor.SnapshotCursor, 10, 64)
	canonical, err := json.Marshal(cursor)
	if err != nil || !bytes.Equal(canonical, decoded) || cursor.Version != 1 || cursor.SnapshotCursor == "" ||
		!validCursorHash(cursor.FilterHash) || !validTaskQueryText(cursor.TaskID, 128) ||
		(!cursor.Done && stateIndex(cursor.State) < 0) ||
		(cursor.Done && cursor.State != "") ||
		(cursor.Priority != domain.PriorityUrgent && cursor.Priority != domain.PriorityHigh &&
			cursor.Priority != domain.PriorityNormal && cursor.Priority != domain.PriorityLow) ||
		cursor.QueuedAtUnixMillis < 0 || snapshotErr != nil || strconv.FormatUint(snapshot, 10) != cursor.SnapshotCursor {
		return taskQueryCursor{}, ErrTaskQueryCursorInvalid
	}
	return cursor, nil
}

func validCursorHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func containsText(values []string, wanted string) bool {
	return len(values) == 0 || slices.Contains(values, wanted)
}

func containsPriority(values []domain.Priority, wanted domain.Priority) bool {
	return len(values) == 0 || slices.Contains(values, wanted)
}

func containsState(values []BoardState, wanted BoardState) bool {
	return len(values) == 0 || slices.Contains(values, wanted)
}

func matchesTaskQuery(row TaskProjectionRow, query normalizedTaskQuery) bool {
	switch query.Membership {
	case TaskMembershipBoard:
		if !row.Projection.BoardMember {
			return false
		}
	case TaskMembershipDone:
		if !row.Projection.DoneMember {
			return false
		}
	case TaskMembershipAll:
	}
	if !containsText(query.ProjectIDs, row.ProjectID) || !containsText(query.WorkspaceIDs, row.WorkspaceID) ||
		!containsPriority(query.Priorities, domain.EffectivePriority(row.Priority)) {
		return false
	}
	if query.Standalone != nil && (*query.Standalone != (row.EpicID == "")) {
		return false
	}
	if len(query.EpicIDs) > 0 && !slices.Contains(query.EpicIDs, row.EpicID) {
		return false
	}
	if row.Projection.BoardMember && !containsState(query.States, row.Projection.State) {
		return false
	}
	if row.Projection.DoneMember && len(query.States) > 0 {
		return false
	}
	for _, label := range query.Labels {
		if !slices.Contains(row.Labels, label) {
			return false
		}
	}
	if len(query.Attention) > 0 {
		matched := false
		for _, code := range query.Attention {
			matched = matched || slices.Contains(row.Projection.Attention, code)
		}
		if !matched {
			return false
		}
	}
	return true
}

func cloneTaskProjectionRow(row TaskProjectionRow) TaskProjectionRow {
	row.Labels = append([]string(nil), row.Labels...)
	row.Projection.Blockers = append([]BlockerCode(nil), row.Projection.Blockers...)
	row.Projection.Attention = append([]AttentionCode(nil), row.Projection.Attention...)
	return row
}

// QueryTaskProjections derives, filters, canonically sorts, and paginates one
// immutable snapshot. It never mutates input facts and rejects cursors from a
// different snapshot or filter set.
func QueryTaskProjections(inputs []TaskProjectionInput, snapshot uint64, query TaskQuery) (TaskPage, error) {
	if query.Limit <= 0 || query.Limit > MaximumBoardTasks {
		return TaskPage{}, ErrTaskQueryInvalid
	}
	normalized, err := normalizeTaskQuery(query)
	if err != nil {
		return TaskPage{}, err
	}
	filterHash := taskQueryHash(normalized)
	rows := make([]TaskProjectionRow, 0, len(inputs))
	identities := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		if !validTaskQueryText(input.TaskID, 128) || !validTaskQueryText(input.ProjectID, 128) ||
			!validTaskQueryText(input.WorkspaceID, 128) || !validTaskQueryText(input.Title, 512) ||
			(input.EpicID != "" && !validTaskQueryText(input.EpicID, 128)) ||
			input.QueuedAtUnixMillis < 0 || priorityIndex(input.Priority) > 3 {
			return TaskPage{}, ErrTaskQueryInvalid
		}
		if _, duplicate := identities[input.TaskID]; duplicate {
			return TaskPage{}, ErrTaskQueryInvalid
		}
		identities[input.TaskID] = struct{}{}
		labels, labelsValid := normalizeUniqueText(input.Labels)
		if !labelsValid {
			return TaskPage{}, ErrTaskQueryInvalid
		}
		facts := input.Facts
		if facts.TaskID != input.TaskID {
			facts.TaskID = ""
		}
		row := TaskProjectionRow{
			TaskID: input.TaskID, ProjectID: input.ProjectID, WorkspaceID: input.WorkspaceID,
			EpicID: input.EpicID, Title: input.Title, Priority: domain.EffectivePriority(input.Priority),
			Labels: labels, QueuedAtUnixMillis: input.QueuedAtUnixMillis,
			Projection: DeriveTaskProjection(facts),
		}
		if matchesTaskQuery(row, normalized) {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(left, right int) bool { return compareTaskRows(rows[left], rows[right]) < 0 })
	start := 0
	if query.After != "" {
		cursor, err := decodeTaskCursor(query.After)
		if err != nil {
			return TaskPage{}, err
		}
		if cursor.SnapshotCursor != strconv.FormatUint(snapshot, 10) || cursor.FilterHash != filterHash {
			return TaskPage{}, ErrTaskQueryCursorSnapshot
		}
		start = sort.Search(len(rows), func(index int) bool { return compareRowCursor(rows[index], cursor) > 0 })
	}
	end := min(start+query.Limit, len(rows))
	page := TaskPage{SnapshotCursor: strconv.FormatUint(snapshot, 10)}
	for _, row := range rows[start:end] {
		page.Tasks = append(page.Tasks, cloneTaskProjectionRow(row))
	}
	if end < len(rows) && len(page.Tasks) > 0 {
		page.NextCursor = encodeTaskCursor(snapshot, filterHash, page.Tasks[len(page.Tasks)-1])
	}
	return page, nil
}
