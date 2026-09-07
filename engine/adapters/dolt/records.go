// SPDX-License-Identifier: Apache-2.0

package dolt

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/mcuadros/director-engine/domain"
	storeport "github.com/mcuadros/director-engine/ports/taskstore"
)

type projectData struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

type taskData struct {
	Title              string `json:"title"`
	Objective          string `json:"objective"`
	AcceptanceCriteria string `json:"acceptanceCriteria"`
}

type runData struct {
	Number             uint64 `json:"number"`
	BaseSHA            string `json:"baseSha"`
	CurrentCandidateID string `json:"currentCandidateId,omitempty"`
}

func decodeRecord(document []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return backendFailure(storeport.HealthStoredRecordInvalid)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return backendFailure(storeport.HealthStoredRecordInvalid)
	}
	return nil
}

func validateLookupID(id string) error {
	if !safeIdentifier(id, 128) {
		return fmt.Errorf("%w: invalid lookup identity", storeport.ErrInvalidRecord)
	}
	return nil
}

func validateReloaded(err error) error {
	if err != nil {
		return backendFailure(storeport.HealthStoredRecordInvalid)
	}
	return nil
}

func queryFailure() error {
	return backendFailure(storeport.HealthQueryFailed)
}

func scanFailure() error {
	return backendFailure(storeport.HealthScanFailed)
}

func finishRows(rows *sql.Rows) error {
	if err := rows.Err(); err != nil {
		return backendFailure(storeport.HealthRowsFailed)
	}
	if err := rows.Close(); err != nil {
		return backendFailure(storeport.HealthRowsFailed)
	}
	return nil
}

func validateProject(project domain.Project) error {
	if !safeIdentifier(project.ID, 128) || project.Name == "" || len(project.Name) > 512 {
		return fmt.Errorf("%w: invalid Project", storeport.ErrInvalidRecord)
	}
	switch project.State {
	case "active", "paused", "degraded", "archived":
		return nil
	default:
		return fmt.Errorf("%w: invalid Project state", storeport.ErrInvalidRecord)
	}
}

func validateTask(task domain.Task) error {
	if !safeIdentifier(task.ID, 128) || !safeIdentifier(task.ProjectID, 128) ||
		task.Title == "" || len(task.Title) > 512 ||
		task.Objective == "" || len(task.Objective) > 16*1024 ||
		task.AcceptanceCriteria == "" || len(task.AcceptanceCriteria) > 16*1024 {
		return fmt.Errorf("%w: invalid Task", storeport.ErrInvalidRecord)
	}
	return nil
}

func validSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func validateRun(run domain.Run) error {
	if !safeIdentifier(run.ID, 128) || !safeIdentifier(run.TaskID, 128) || run.Number == 0 || !validSHA(run.BaseSHA) {
		return fmt.Errorf("%w: invalid Run", storeport.ErrInvalidRecord)
	}
	if run.CurrentCandidateID != "" && !safeIdentifier(run.CurrentCandidateID, 128) {
		return fmt.Errorf("%w: invalid current Candidate identity", storeport.ErrInvalidRecord)
	}
	return nil
}

func validateCandidate(candidate domain.Candidate) error {
	if !safeIdentifier(candidate.ID, 128) || !safeIdentifier(candidate.RunID, 128) ||
		candidate.Sequence == 0 || !validSHA(candidate.CommitSHA) {
		return fmt.Errorf("%w: invalid Candidate", storeport.ErrInvalidRecord)
	}
	return nil
}

func validateReloadedEvent(event domain.Event) error {
	if event.GlobalSequence == 0 || !safeIdentifier(event.ID, 128) ||
		!safeIdentifier(event.AggregateID, 128) || !safeIdentifier(event.Type, 64) {
		return backendFailure(storeport.HealthStoredRecordInvalid)
	}
	if event.RunID != "" && !safeIdentifier(event.RunID, 128) {
		return backendFailure(storeport.HealthStoredRecordInvalid)
	}
	if _, err := canonicalPayload(event.Payload); err != nil {
		return backendFailure(storeport.HealthStoredRecordInvalid)
	}
	return nil
}

// CreateProject atomically appends the Project identity, Command, Event, and
// applied outcome.
func (store *DoltTaskStore) CreateProject(
	ctx context.Context,
	command domain.CommandRequest,
	project domain.Project,
	event domain.Event,
) (domain.CommandResult, error) {
	if err := validateProject(project); err != nil {
		return domain.CommandResult{}, err
	}
	if err := validateCreateCommand(command, project.ID, project.Version); err != nil {
		return domain.CommandResult{}, err
	}
	data, err := marshalRecord(projectData{Name: project.Name, State: project.State})
	if err != nil {
		return domain.CommandResult{}, err
	}
	return store.apply(ctx, command, event, func(ctx context.Context, tx *sql.Tx) (mutationResult, error) {
		if err := insertAggregate(ctx, tx, project.ID, aggregateProject, nil, project.Version, data); err != nil {
			return mutationResult{}, err
		}
		return mutationResult{outcome: domain.CommandApplied, observedVersion: project.Version}, nil
	})
}

// UpdateProject persists one expected-version replacement.
func (store *DoltTaskStore) UpdateProject(
	ctx context.Context,
	command domain.CommandRequest,
	project domain.Project,
	event domain.Event,
) (domain.CommandResult, error) {
	if err := validateProject(project); err != nil {
		return domain.CommandResult{}, err
	}
	if err := validateUpdateCommand(command, project.ID); err != nil {
		return domain.CommandResult{}, err
	}
	data, err := marshalRecord(projectData{Name: project.Name, State: project.State})
	if err != nil {
		return domain.CommandResult{}, err
	}
	return store.apply(ctx, command, event, func(ctx context.Context, tx *sql.Tx) (mutationResult, error) {
		return updateAggregate(ctx, tx, project.ID, aggregateProject, command.ExpectedVersion, project.Version, data)
	})
}

// CreateTask appends a Task under an existing Project.
func (store *DoltTaskStore) CreateTask(
	ctx context.Context,
	command domain.CommandRequest,
	task domain.Task,
	event domain.Event,
) (domain.CommandResult, error) {
	if err := validateTask(task); err != nil {
		return domain.CommandResult{}, err
	}
	if err := validateCreateCommand(command, task.ID, task.Version); err != nil {
		return domain.CommandResult{}, err
	}
	data, err := marshalRecord(taskData{
		Title: task.Title, Objective: task.Objective, AcceptanceCriteria: task.AcceptanceCriteria,
	})
	if err != nil {
		return domain.CommandResult{}, err
	}
	return store.apply(ctx, command, event, func(ctx context.Context, tx *sql.Tx) (mutationResult, error) {
		if err := requireAggregateKind(ctx, tx, task.ProjectID, aggregateProject); err != nil {
			return mutationResult{}, err
		}
		if err := insertAggregate(ctx, tx, task.ID, aggregateTask, task.ProjectID, task.Version, data); err != nil {
			return mutationResult{}, err
		}
		return mutationResult{outcome: domain.CommandApplied, observedVersion: task.Version}, nil
	})
}

// UpdateTask persists one expected-version replacement without changing its
// Project identity.
func (store *DoltTaskStore) UpdateTask(
	ctx context.Context,
	command domain.CommandRequest,
	task domain.Task,
	event domain.Event,
) (domain.CommandResult, error) {
	if err := validateTask(task); err != nil {
		return domain.CommandResult{}, err
	}
	if err := validateUpdateCommand(command, task.ID); err != nil {
		return domain.CommandResult{}, err
	}
	data, err := marshalRecord(taskData{
		Title: task.Title, Objective: task.Objective, AcceptanceCriteria: task.AcceptanceCriteria,
	})
	if err != nil {
		return domain.CommandResult{}, err
	}
	return store.apply(ctx, command, event, func(ctx context.Context, tx *sql.Tx) (mutationResult, error) {
		var parentID string
		if err := tx.QueryRowContext(ctx,
			`SELECT parent_id FROM aggregates WHERE id = ? AND kind = ?`, task.ID, aggregateTask,
		).Scan(&parentID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return mutationResult{}, storeport.ErrNotFound
			}
			return mutationResult{}, err
		}
		if parentID != task.ProjectID {
			return mutationResult{}, fmt.Errorf("%w: Task Project identity is immutable", storeport.ErrInvalidRecord)
		}
		return updateAggregate(ctx, tx, task.ID, aggregateTask, command.ExpectedVersion, task.Version, data)
	})
}

// CreateRun appends a Run under an existing Task.
func (store *DoltTaskStore) CreateRun(
	ctx context.Context,
	command domain.CommandRequest,
	run domain.Run,
	event domain.Event,
) (domain.CommandResult, error) {
	if err := validateRun(run); err != nil {
		return domain.CommandResult{}, err
	}
	if run.CurrentCandidateID != "" {
		return domain.CommandResult{}, fmt.Errorf("%w: a new Run cannot have a Candidate", storeport.ErrInvalidRecord)
	}
	if err := validateCreateCommand(command, run.ID, run.Version); err != nil {
		return domain.CommandResult{}, err
	}
	data, err := marshalRecord(runData{Number: run.Number, BaseSHA: run.BaseSHA})
	if err != nil {
		return domain.CommandResult{}, err
	}
	return store.apply(ctx, command, event, func(ctx context.Context, tx *sql.Tx) (mutationResult, error) {
		if err := requireAggregateKind(ctx, tx, run.TaskID, aggregateTask); err != nil {
			return mutationResult{}, err
		}
		if err := insertAggregate(ctx, tx, run.ID, aggregateRun, run.TaskID, run.Version, data); err != nil {
			return mutationResult{}, err
		}
		return mutationResult{outcome: domain.CommandApplied, observedVersion: run.Version}, nil
	})
}

// UpdateRun persists one expected-version replacement without changing its
// Task identity or rewriting a Candidate.
func (store *DoltTaskStore) UpdateRun(
	ctx context.Context,
	command domain.CommandRequest,
	run domain.Run,
	event domain.Event,
) (domain.CommandResult, error) {
	if err := validateRun(run); err != nil {
		return domain.CommandResult{}, err
	}
	if err := validateUpdateCommand(command, run.ID); err != nil {
		return domain.CommandResult{}, err
	}
	data, err := marshalRecord(runData{
		Number: run.Number, BaseSHA: run.BaseSHA, CurrentCandidateID: run.CurrentCandidateID,
	})
	if err != nil {
		return domain.CommandResult{}, err
	}
	return store.apply(ctx, command, event, func(ctx context.Context, tx *sql.Tx) (mutationResult, error) {
		var parentID string
		var currentVersion uint64
		var rawData []byte
		if err := tx.QueryRowContext(ctx,
			`SELECT parent_id, version, data FROM aggregates WHERE id = ? AND kind = ?`, run.ID, aggregateRun,
		).Scan(&parentID, &currentVersion, &rawData); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return mutationResult{}, storeport.ErrNotFound
			}
			return mutationResult{}, err
		}
		if parentID != run.TaskID {
			return mutationResult{}, fmt.Errorf("%w: Run Task identity is immutable", storeport.ErrInvalidRecord)
		}
		var currentData runData
		if err := decodeRecord(rawData, &currentData); err != nil {
			return mutationResult{}, err
		}
		current := domain.Run{
			ID: run.ID, TaskID: parentID, Number: currentData.Number,
			BaseSHA: currentData.BaseSHA, CurrentCandidateID: currentData.CurrentCandidateID,
			Version: currentVersion,
		}
		if err := validateReloaded(validateRun(current)); err != nil {
			return mutationResult{}, err
		}
		if current.Number != run.Number {
			return mutationResult{}, fmt.Errorf("%w: Run number is immutable", storeport.ErrInvalidRecord)
		}
		if run.CurrentCandidateID != "" {
			var candidateRunID string
			if err := tx.QueryRowContext(ctx,
				`SELECT run_id FROM candidates WHERE id = ?`, run.CurrentCandidateID,
			).Scan(&candidateRunID); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return mutationResult{}, storeport.ErrReferentialIntegrity
				}
				return mutationResult{}, err
			}
			if candidateRunID != run.ID {
				return mutationResult{}, storeport.ErrReferentialIntegrity
			}
		}
		return updateAggregate(ctx, tx, run.ID, aggregateRun, command.ExpectedVersion, run.Version, data)
	})
}

// AppendCandidate atomically appends the immutable Candidate and advances its
// Run's current-Candidate projection by expected version.
func (store *DoltTaskStore) AppendCandidate(
	ctx context.Context,
	command domain.CommandRequest,
	candidate domain.Candidate,
	event domain.Event,
) (domain.CommandResult, error) {
	if err := validateCandidate(candidate); err != nil {
		return domain.CommandResult{}, err
	}
	if err := validateUpdateCommand(command, candidate.RunID); err != nil {
		return domain.CommandResult{}, err
	}
	return store.apply(ctx, command, event, func(ctx context.Context, tx *sql.Tx) (mutationResult, error) {
		var version uint64
		var rawData []byte
		if err := tx.QueryRowContext(ctx,
			`SELECT version, data FROM aggregates WHERE id = ? AND kind = ?`, candidate.RunID, aggregateRun,
		).Scan(&version, &rawData); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return mutationResult{}, storeport.ErrReferentialIntegrity
			}
			return mutationResult{}, err
		}
		if version != command.ExpectedVersion {
			return mutationResult{
				outcome: domain.CommandRejectedVersionConflict, observedVersion: version,
			}, nil
		}
		var current runData
		if err := decodeRecord(rawData, &current); err != nil {
			return mutationResult{}, err
		}
		current.CurrentCandidateID = candidate.ID
		data, err := marshalRecord(current)
		if err != nil {
			return mutationResult{}, err
		}
		newVersion := version + 1
		result, err := tx.ExecContext(ctx,
			`UPDATE aggregates SET version = ?, data = ? WHERE id = ? AND kind = ? AND version = ?`,
			newVersion, data, candidate.RunID, aggregateRun, version,
		)
		if err != nil {
			return mutationResult{}, err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return mutationResult{}, err
		}
		if rows != 1 {
			return mutationResult{}, errConcurrentWrite
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO candidates (id, run_id, sequence, commit_sha) VALUES (?, ?, ?, ?)`,
			candidate.ID, candidate.RunID, candidate.Sequence, candidate.CommitSHA,
		); err != nil {
			if isDuplicateError(err) {
				return mutationResult{}, storeport.ErrAlreadyExists
			}
			return mutationResult{}, err
		}
		return mutationResult{outcome: domain.CommandApplied, observedVersion: newVersion}, nil
	})
}

func (store *DoltTaskStore) readConnection(ctx context.Context) (*sql.Conn, error) {
	connection, err := store.writer.Conn(ctx)
	if err != nil {
		return nil, backendFailure(storeport.HealthReadConnectionUnavailable)
	}
	if err := store.verifyStoreIdentity(ctx, connection); err != nil {
		connection.Close()
		return nil, err
	}
	return connection, nil
}

// Project returns one Project without exposing its aggregate row or JSON.
func (store *DoltTaskStore) Project(ctx context.Context, id string) (domain.Project, error) {
	if err := validateLookupID(id); err != nil {
		return domain.Project{}, err
	}
	connection, err := store.readConnection(ctx)
	if err != nil {
		return domain.Project{}, err
	}
	defer connection.Close()
	return projectByID(ctx, connection, id)
}

func projectByID(ctx context.Context, query rowQuerier, id string) (domain.Project, error) {
	var project domain.Project
	var rawData []byte
	err := query.QueryRowContext(ctx,
		`SELECT id, version, data FROM aggregates WHERE id = ? AND kind = ?`, id, aggregateProject,
	).Scan(&project.ID, &project.Version, &rawData)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Project{}, storeport.ErrNotFound
	}
	if err != nil {
		return domain.Project{}, queryFailure()
	}
	var data projectData
	if err := decodeRecord(rawData, &data); err != nil {
		return domain.Project{}, err
	}
	project.Name, project.State = data.Name, data.State
	if err := validateReloaded(validateProject(project)); err != nil {
		return domain.Project{}, err
	}
	return project, nil
}

// Projects returns all Projects ordered by stable identity.
func (store *DoltTaskStore) Projects(ctx context.Context) ([]domain.Project, error) {
	connection, err := store.readConnection(ctx)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	rows, err := connection.QueryContext(ctx,
		`SELECT id, version, data FROM aggregates WHERE kind = ? ORDER BY id`, aggregateProject,
	)
	if err != nil {
		return nil, queryFailure()
	}
	defer rows.Close()
	var projects []domain.Project
	for rows.Next() {
		var project domain.Project
		var rawData []byte
		if err := rows.Scan(&project.ID, &project.Version, &rawData); err != nil {
			return nil, scanFailure()
		}
		var data projectData
		if err := decodeRecord(rawData, &data); err != nil {
			return nil, err
		}
		project.Name, project.State = data.Name, data.State
		if err := validateReloaded(validateProject(project)); err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	if err := finishRows(rows); err != nil {
		return nil, err
	}
	return projects, nil
}

// Task returns one Task without exposing its aggregate row or JSON.
func (store *DoltTaskStore) Task(ctx context.Context, id string) (domain.Task, error) {
	if err := validateLookupID(id); err != nil {
		return domain.Task{}, err
	}
	connection, err := store.readConnection(ctx)
	if err != nil {
		return domain.Task{}, err
	}
	defer connection.Close()
	var task domain.Task
	var rawData []byte
	err = connection.QueryRowContext(ctx,
		`SELECT id, parent_id, version, data FROM aggregates WHERE id = ? AND kind = ?`, id, aggregateTask,
	).Scan(&task.ID, &task.ProjectID, &task.Version, &rawData)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, storeport.ErrNotFound
	}
	if err != nil {
		return domain.Task{}, queryFailure()
	}
	var data taskData
	if err := decodeRecord(rawData, &data); err != nil {
		return domain.Task{}, err
	}
	task.Title, task.Objective, task.AcceptanceCriteria = data.Title, data.Objective, data.AcceptanceCriteria
	if err := validateReloaded(validateTask(task)); err != nil {
		return domain.Task{}, err
	}
	return task, nil
}

// Tasks returns one Project's Tasks ordered by stable identity.
func (store *DoltTaskStore) Tasks(ctx context.Context, projectID string) ([]domain.Task, error) {
	if err := validateLookupID(projectID); err != nil {
		return nil, err
	}
	connection, err := store.readConnection(ctx)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	rows, err := connection.QueryContext(ctx,
		`SELECT id, parent_id, version, data FROM aggregates WHERE kind = ? AND parent_id = ? ORDER BY id`,
		aggregateTask, projectID,
	)
	if err != nil {
		return nil, queryFailure()
	}
	defer rows.Close()
	var tasks []domain.Task
	for rows.Next() {
		var task domain.Task
		var rawData []byte
		if err := rows.Scan(&task.ID, &task.ProjectID, &task.Version, &rawData); err != nil {
			return nil, scanFailure()
		}
		var data taskData
		if err := decodeRecord(rawData, &data); err != nil {
			return nil, err
		}
		task.Title, task.Objective, task.AcceptanceCriteria = data.Title, data.Objective, data.AcceptanceCriteria
		if err := validateReloaded(validateTask(task)); err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	if err := finishRows(rows); err != nil {
		return nil, err
	}
	return tasks, nil
}

// Run returns one Run without exposing its aggregate row or JSON.
func (store *DoltTaskStore) Run(ctx context.Context, id string) (domain.Run, error) {
	if err := validateLookupID(id); err != nil {
		return domain.Run{}, err
	}
	connection, err := store.readConnection(ctx)
	if err != nil {
		return domain.Run{}, err
	}
	defer connection.Close()
	var run domain.Run
	var rawData []byte
	err = connection.QueryRowContext(ctx,
		`SELECT id, parent_id, version, data FROM aggregates WHERE id = ? AND kind = ?`, id, aggregateRun,
	).Scan(&run.ID, &run.TaskID, &run.Version, &rawData)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Run{}, storeport.ErrNotFound
	}
	if err != nil {
		return domain.Run{}, queryFailure()
	}
	var data runData
	if err := decodeRecord(rawData, &data); err != nil {
		return domain.Run{}, err
	}
	run.Number, run.BaseSHA, run.CurrentCandidateID = data.Number, data.BaseSHA, data.CurrentCandidateID
	if err := validateReloaded(validateRun(run)); err != nil {
		return domain.Run{}, err
	}
	return run, nil
}

// Runs returns one Task's Runs ordered by run number and stable identity.
func (store *DoltTaskStore) Runs(ctx context.Context, taskID string) ([]domain.Run, error) {
	if err := validateLookupID(taskID); err != nil {
		return nil, err
	}
	connection, err := store.readConnection(ctx)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	rows, err := connection.QueryContext(ctx,
		`SELECT id, parent_id, version, data FROM aggregates WHERE kind = ? AND parent_id = ?
		ORDER BY CAST(JSON_UNQUOTE(JSON_EXTRACT(data, '$.number')) AS UNSIGNED), id`,
		aggregateRun, taskID,
	)
	if err != nil {
		return nil, queryFailure()
	}
	defer rows.Close()
	var runs []domain.Run
	for rows.Next() {
		var run domain.Run
		var rawData []byte
		if err := rows.Scan(&run.ID, &run.TaskID, &run.Version, &rawData); err != nil {
			return nil, scanFailure()
		}
		var data runData
		if err := decodeRecord(rawData, &data); err != nil {
			return nil, err
		}
		run.Number, run.BaseSHA, run.CurrentCandidateID = data.Number, data.BaseSHA, data.CurrentCandidateID
		if err := validateReloaded(validateRun(run)); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := finishRows(rows); err != nil {
		return nil, err
	}
	return runs, nil
}

// Candidate returns one immutable Candidate.
func (store *DoltTaskStore) Candidate(ctx context.Context, id string) (domain.Candidate, error) {
	if err := validateLookupID(id); err != nil {
		return domain.Candidate{}, err
	}
	connection, err := store.readConnection(ctx)
	if err != nil {
		return domain.Candidate{}, err
	}
	defer connection.Close()
	var candidate domain.Candidate
	err = connection.QueryRowContext(ctx,
		`SELECT id, run_id, sequence, commit_sha FROM candidates WHERE id = ?`, id,
	).Scan(&candidate.ID, &candidate.RunID, &candidate.Sequence, &candidate.CommitSHA)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Candidate{}, storeport.ErrNotFound
	}
	if err != nil {
		return domain.Candidate{}, queryFailure()
	}
	if err := validateReloaded(validateCandidate(candidate)); err != nil {
		return domain.Candidate{}, err
	}
	return candidate, nil
}

// Candidates returns one Run's immutable Candidates in sequence order.
func (store *DoltTaskStore) Candidates(ctx context.Context, runID string) ([]domain.Candidate, error) {
	if err := validateLookupID(runID); err != nil {
		return nil, err
	}
	connection, err := store.readConnection(ctx)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	rows, err := connection.QueryContext(ctx,
		`SELECT id, run_id, sequence, commit_sha FROM candidates WHERE run_id = ? ORDER BY sequence`, runID,
	)
	if err != nil {
		return nil, queryFailure()
	}
	defer rows.Close()
	var candidates []domain.Candidate
	for rows.Next() {
		var candidate domain.Candidate
		if err := rows.Scan(&candidate.ID, &candidate.RunID, &candidate.Sequence, &candidate.CommitSHA); err != nil {
			return nil, scanFailure()
		}
		if err := validateReloaded(validateCandidate(candidate)); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	if err := finishRows(rows); err != nil {
		return nil, err
	}
	return candidates, nil
}

// Command returns the immutable request and durable outcome for one key.
func (store *DoltTaskStore) Command(ctx context.Context, key string) (domain.Command, error) {
	if err := validateLookupID(key); err != nil {
		return domain.Command{}, err
	}
	connection, err := store.readConnection(ctx)
	if err != nil {
		return domain.Command{}, err
	}
	defer connection.Close()
	command, err := loadCommand(ctx, connection, key)
	if err != nil {
		return domain.Command{}, err
	}
	if err := validateStoredCommand(command); err != nil {
		return domain.Command{}, err
	}
	return command, nil
}

// Events returns a bounded projection over immutable Event rows.
func (store *DoltTaskStore) Events(ctx context.Context, query domain.EventQuery) ([]domain.Event, error) {
	if query.Limit == 0 {
		query.Limit = 100
	}
	if query.Limit > 1000 {
		return nil, fmt.Errorf("%w: Event query limit exceeds 1000", storeport.ErrInvalidRecord)
	}
	if query.RunID != "" {
		if err := validateLookupID(query.RunID); err != nil {
			return nil, err
		}
	}
	if query.AggregateID != "" {
		if err := validateLookupID(query.AggregateID); err != nil {
			return nil, err
		}
	}
	connection, err := store.readConnection(ctx)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	statement := `
		SELECT global_sequence, event_id, run_id, sequence, aggregate_id,
			aggregate_version, event_type, payload
		FROM events WHERE global_sequence > ?`
	arguments := []any{query.AfterGlobalSequence}
	if query.RunID != "" {
		statement += ` AND run_id = ?`
		arguments = append(arguments, query.RunID)
	}
	if query.AggregateID != "" {
		statement += ` AND aggregate_id = ?`
		arguments = append(arguments, query.AggregateID)
	}
	statement += ` ORDER BY global_sequence LIMIT ?`
	arguments = append(arguments, query.Limit)
	rows, err := connection.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, queryFailure()
	}
	defer rows.Close()
	var events []domain.Event
	for rows.Next() {
		var event domain.Event
		var runID sql.NullString
		var payload []byte
		if err := rows.Scan(
			&event.GlobalSequence,
			&event.ID,
			&runID,
			&event.Sequence,
			&event.AggregateID,
			&event.AggregateVersion,
			&event.Type,
			&payload,
		); err != nil {
			return nil, scanFailure()
		}
		if runID.Valid {
			event.RunID = runID.String
		}
		event.Payload = append(json.RawMessage(nil), payload...)
		if err := validateReloadedEvent(event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := finishRows(rows); err != nil {
		return nil, err
	}
	return events, nil
}
