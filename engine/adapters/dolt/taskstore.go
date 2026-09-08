// SPDX-License-Identifier: Apache-2.0

package dolt

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/go-sql-driver/mysql"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/jsondocument"
	storeport "github.com/mcuadros/director-engine/ports/taskstore"
)

const (
	aggregateProject        = "project"
	aggregateTask           = "task"
	aggregateRun            = "run"
	maximumJSONBytes        = 64 * 1024
	maximumProjectJSONBytes = 1152 * 1024
	// Store-only contention retries remain finite while covering the tested
	// eight-writer Event allocation envelope.
	writeAttempts = 16
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]*$`)

var errConcurrentWrite = errors.New("concurrent aggregate write")

func backendFailure(code storeport.HealthCode) error {
	return storeport.Unhealthy(code)
}

func boundedMutationError(err error) error {
	for _, known := range []error{
		storeport.ErrNotFound,
		storeport.ErrAlreadyExists,
		storeport.ErrInvalidRecord,
		storeport.ErrReferentialIntegrity,
		storeport.ErrIdempotencyConflict,
		storeport.ErrSchemaVersion,
		storeport.ErrUnhealthy,
	} {
		if errors.Is(err, known) {
			return err
		}
	}
	return backendFailure(storeport.HealthWriteFailed)
}

// DoltTaskStore is the direct-Dolt implementation of the engine-owned typed
// port. Its SQL pools and identity checks are not exposed through that port.
type DoltTaskStore struct {
	control  *sql.DB
	writer   *sql.DB
	database string
	storeID  string
}

var _ storeport.TaskStore = (*DoltTaskStore)(nil)

type preparedCommand struct {
	request domain.CommandRequest
	payload []byte
	hash    string
}

type preparedEvent struct {
	event   domain.Event
	payload []byte
}

type mutationResult struct {
	outcome         domain.CommandOutcome
	observedVersion uint64
}

type transactionMutation func(context.Context, *sql.Tx) (mutationResult, error)

func (store *DoltTaskStore) rejectInvalidCommandReplay(
	ctx context.Context,
	request domain.CommandRequest,
	validationError error,
) (domain.CommandResult, error) {
	if !safeIdentifier(request.IdempotencyKey, 128) ||
		!safeIdentifier(request.Type, 64) ||
		!safeIdentifier(request.AggregateID, 128) {
		return domain.CommandResult{}, validationError
	}
	_, err := store.Command(ctx, request.IdempotencyKey)
	switch {
	case err == nil:
		return domain.CommandResult{}, storeport.ErrIdempotencyConflict
	case errors.Is(err, storeport.ErrNotFound):
		return domain.CommandResult{}, validationError
	default:
		return domain.CommandResult{}, boundedMutationError(err)
	}
}

func safeIdentifier(value string, maximum int) bool {
	return len(value) > 0 && len(value) <= maximum && identifierPattern.MatchString(value)
}

func canonicalPayload(payload json.RawMessage) ([]byte, error) {
	if len(payload) > maximumJSONBytes {
		return nil, storeport.Invalid(storeport.ValidationPayloadTooLarge)
	}
	canonical, err := jsondocument.CanonicalWithNormalizedNumbersLimit(payload, maximumJSONBytes)
	if err != nil {
		if errors.Is(err, jsondocument.ErrCanonicalDocumentTooLarge) {
			return nil, storeport.Invalid(storeport.ValidationPayloadTooLarge)
		}
		return nil, storeport.Invalid(storeport.ValidationJSONInvalid)
	}
	if len(canonical) > maximumJSONBytes {
		return nil, storeport.Invalid(storeport.ValidationPayloadTooLarge)
	}
	return canonical, nil
}

func prepareCommand(request domain.CommandRequest) (preparedCommand, error) {
	if !safeIdentifier(request.IdempotencyKey, 128) ||
		!safeIdentifier(request.Type, 64) ||
		!safeIdentifier(request.AggregateID, 128) {
		return preparedCommand{}, fmt.Errorf("%w: invalid command identity", storeport.ErrInvalidRecord)
	}
	payload, err := canonicalPayload(request.Payload)
	if err != nil {
		return preparedCommand{}, err
	}
	document, err := json.Marshal(struct {
		SchemaVersion   int             `json:"schemaVersion"`
		Type            string          `json:"type"`
		AggregateID     string          `json:"aggregateId"`
		ExpectedVersion uint64          `json:"expectedVersion"`
		Payload         json.RawMessage `json:"payload"`
	}{
		SchemaVersion:   1,
		Type:            request.Type,
		AggregateID:     request.AggregateID,
		ExpectedVersion: request.ExpectedVersion,
		Payload:         payload,
	})
	if err != nil {
		return preparedCommand{}, fmt.Errorf("encode command identity: %w", err)
	}
	canonical, err := jsondocument.CanonicalWithNormalizedNumbers(document)
	if err != nil {
		return preparedCommand{}, err
	}
	digest := sha256.Sum256(canonical)
	return preparedCommand{
		request: request,
		payload: payload,
		hash:    hex.EncodeToString(digest[:]),
	}, nil
}

func prepareEvent(event domain.Event, command domain.CommandRequest) (preparedEvent, error) {
	if !safeIdentifier(event.ID, 128) || !safeIdentifier(event.Type, 64) {
		return preparedEvent{}, fmt.Errorf("%w: invalid event identity", storeport.ErrInvalidRecord)
	}
	if event.RunID != "" && !safeIdentifier(event.RunID, 128) {
		return preparedEvent{}, fmt.Errorf("%w: invalid event Run identity", storeport.ErrInvalidRecord)
	}
	if event.AggregateID != command.AggregateID {
		return preparedEvent{}, fmt.Errorf("%w: event and command aggregate differ", storeport.ErrInvalidRecord)
	}
	payload, err := canonicalPayload(event.Payload)
	if err != nil {
		return preparedEvent{}, err
	}
	return preparedEvent{event: event, payload: payload}, nil
}

func isTransactionContention(err error) bool {
	if errors.Is(err, errConcurrentWrite) {
		return true
	}
	var mysqlError *mysql.MySQLError
	return errors.As(err, &mysqlError) && (mysqlError.Number == 1205 || mysqlError.Number == 1213)
}

func isDuplicateError(err error) bool {
	var mysqlError *mysql.MySQLError
	return errors.As(err, &mysqlError) && mysqlError.Number == 1062
}

func (store *DoltTaskStore) prepareWriteConnection(ctx context.Context) (*sql.Conn, error) {
	control, err := store.control.Conn(ctx)
	if err != nil {
		return nil, backendFailure(storeport.HealthControlConnectionUnavailable)
	}
	if err := setAndVerifySafeCommitMode(ctx, control, true); err != nil {
		control.Close()
		return nil, boundedMutationError(err)
	}
	if err := store.verifyStoreIdentity(ctx, control); err != nil {
		control.Close()
		return nil, err
	}
	control.Close()

	writer, err := store.writer.Conn(ctx)
	if err != nil {
		return nil, backendFailure(storeport.HealthWriterConnectionUnavailable)
	}
	if err := setAndVerifySafeCommitMode(ctx, writer, false); err != nil {
		writer.Close()
		return nil, boundedMutationError(err)
	}
	if err := store.verifyStoreIdentity(ctx, writer); err != nil {
		writer.Close()
		return nil, err
	}
	var version int
	if err := writer.QueryRowContext(ctx,
		`SELECT schema_version FROM schema_metadata WHERE singleton = 1`,
	).Scan(&version); err != nil || version != currentSchemaVersion {
		writer.Close()
		if err != nil {
			return nil, storeport.InvalidSchema(storeport.SchemaVersionReadFailed)
		}
		return nil, storeport.InvalidSchema(storeport.SchemaVersionMismatch)
	}
	return writer, nil
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadCommand(ctx context.Context, query rowQuerier, key string) (domain.Command, error) {
	var command domain.Command
	var payload []byte
	var eventID sql.NullString
	err := query.QueryRowContext(ctx, `
		SELECT r.idempotency_key, r.command_type, r.aggregate_id, r.expected_version,
			r.request_hash, r.payload, o.outcome_type, o.observed_version, o.event_id
		FROM command_requests AS r
		JOIN command_outcomes AS o USING (idempotency_key)
		WHERE r.idempotency_key = ?`, key).Scan(
		&command.IdempotencyKey,
		&command.Type,
		&command.AggregateID,
		&command.ExpectedVersion,
		&command.PayloadHash,
		&payload,
		&command.Outcome,
		&command.ObservedVersion,
		&eventID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Command{}, storeport.ErrNotFound
	}
	if err != nil {
		return domain.Command{}, backendFailure(storeport.HealthCommandReadFailed)
	}
	command.Payload = append(json.RawMessage(nil), payload...)
	if eventID.Valid {
		command.EventID = eventID.String
	}
	return command, nil
}

func validateStoredCommand(command domain.Command) error {
	prepared, err := prepareCommand(domain.CommandRequest{
		IdempotencyKey:  command.IdempotencyKey,
		Type:            command.Type,
		AggregateID:     command.AggregateID,
		ExpectedVersion: command.ExpectedVersion,
		Payload:         command.Payload,
	})
	if err != nil {
		return backendFailure(storeport.HealthStoredRecordInvalid)
	}
	if prepared.hash != command.PayloadHash {
		return backendFailure(storeport.HealthStoredRecordInvalid)
	}
	if command.Outcome != domain.CommandApplied && command.Outcome != domain.CommandRejectedVersionConflict {
		return backendFailure(storeport.HealthStoredRecordInvalid)
	}
	return nil
}

func replayResult(command domain.Command, expectedHash string) (domain.CommandResult, error) {
	if err := validateStoredCommand(command); err != nil {
		return domain.CommandResult{}, err
	}
	if command.PayloadHash != expectedHash {
		return domain.CommandResult{}, storeport.ErrIdempotencyConflict
	}
	return domain.CommandResult{
		Outcome:         command.Outcome,
		ObservedVersion: command.ObservedVersion,
		EventID:         command.EventID,
		Replay:          true,
	}, nil
}

func (store *DoltTaskStore) replay(ctx context.Context, command preparedCommand) (domain.CommandResult, bool, error) {
	stored, err := store.Command(ctx, command.request.IdempotencyKey)
	if errors.Is(err, storeport.ErrNotFound) {
		return domain.CommandResult{}, false, nil
	}
	if err != nil {
		return domain.CommandResult{}, false, err
	}
	result, err := replayResult(stored, command.hash)
	return result, true, err
}

func insertCommand(ctx context.Context, tx *sql.Tx, command preparedCommand) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO command_requests
			(idempotency_key, command_type, aggregate_id, expected_version, request_hash, payload)
		VALUES (?, ?, ?, ?, ?, ?)`,
		command.request.IdempotencyKey,
		command.request.Type,
		command.request.AggregateID,
		command.request.ExpectedVersion,
		command.hash,
		command.payload,
	)
	if isDuplicateError(err) {
		return storeport.ErrAlreadyExists
	}
	return err
}

func nullableIdentifier(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func requireAggregateKind(ctx context.Context, tx *sql.Tx, id, expectedKind string) error {
	var kind string
	if err := tx.QueryRowContext(ctx, `SELECT kind FROM aggregates WHERE id = ?`, id).Scan(&kind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storeport.ErrReferentialIntegrity
		}
		return err
	}
	if kind != expectedKind {
		return storeport.ErrReferentialIntegrity
	}
	return nil
}

func appendEvent(ctx context.Context, tx *sql.Tx, event preparedEvent, observedVersion uint64) error {
	if event.event.AggregateVersion != observedVersion {
		return fmt.Errorf("%w: event aggregate version is not the durable version", storeport.ErrInvalidRecord)
	}
	if event.event.RunID != "" {
		if err := requireAggregateKind(ctx, tx, event.event.RunID, aggregateRun); err != nil {
			return err
		}
	}
	allocation, err := tx.ExecContext(ctx, `
		UPDATE event_stream_lock SET next_sequence = next_sequence + 1 WHERE singleton = 1`)
	if err != nil {
		return err
	}
	rows, err := allocation.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return backendFailure(storeport.HealthStoredRecordInvalid)
	}
	var globalSequence uint64
	if err := tx.QueryRowContext(ctx,
		`SELECT next_sequence FROM event_stream_lock WHERE singleton = 1`,
	).Scan(&globalSequence); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO events
			(global_sequence, event_id, run_id, sequence, aggregate_id, aggregate_version, event_type, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		globalSequence,
		event.event.ID,
		nullableIdentifier(event.event.RunID),
		event.event.Sequence,
		event.event.AggregateID,
		event.event.AggregateVersion,
		event.event.Type,
		event.payload,
	)
	if isDuplicateError(err) {
		return storeport.ErrAlreadyExists
	}
	return err
}

func insertOutcome(ctx context.Context, tx *sql.Tx, command preparedCommand, result mutationResult, eventID any) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO command_outcomes
			(idempotency_key, outcome_type, observed_version, event_id)
		VALUES (?, ?, ?, ?)`,
		command.request.IdempotencyKey,
		result.outcome,
		result.observedVersion,
		eventID,
	)
	if isDuplicateError(err) {
		return storeport.ErrAlreadyExists
	}
	return err
}

func (store *DoltTaskStore) apply(
	ctx context.Context,
	request domain.CommandRequest,
	event domain.Event,
	mutation transactionMutation,
) (domain.CommandResult, error) {
	command, err := prepareCommand(request)
	if err != nil {
		return store.rejectInvalidCommandReplay(ctx, request, err)
	}
	preparedDomainEvent, err := prepareEvent(event, request)
	if err != nil {
		return domain.CommandResult{}, err
	}
	if result, found, replayErr := store.replay(ctx, command); found {
		if replayErr != nil {
			return domain.CommandResult{}, boundedMutationError(replayErr)
		}
		return result, nil
	} else if replayErr != nil {
		return domain.CommandResult{}, boundedMutationError(replayErr)
	}

	for attempt := 0; attempt < writeAttempts; attempt++ {
		connection, err := store.prepareWriteConnection(ctx)
		if err != nil {
			return domain.CommandResult{}, err
		}
		tx, err := connection.BeginTx(ctx, nil)
		if err != nil {
			connection.Close()
			return domain.CommandResult{}, backendFailure(storeport.HealthTransactionBeginFailed)
		}

		stored, lookupErr := loadCommand(ctx, tx, request.IdempotencyKey)
		if lookupErr == nil {
			_ = tx.Rollback()
			connection.Close()
			return replayResult(stored, command.hash)
		}
		if !errors.Is(lookupErr, storeport.ErrNotFound) {
			_ = tx.Rollback()
			connection.Close()
			return domain.CommandResult{}, boundedMutationError(lookupErr)
		}

		mutationResult, mutationErr := mutation(ctx, tx)
		if mutationErr == nil {
			mutationErr = insertCommand(ctx, tx, command)
		}
		eventID := any(nil)
		if mutationErr == nil && mutationResult.outcome == domain.CommandApplied {
			mutationErr = appendEvent(ctx, tx, preparedDomainEvent, mutationResult.observedVersion)
			eventID = event.ID
		}
		if mutationErr == nil {
			mutationErr = insertOutcome(ctx, tx, command, mutationResult, eventID)
		}
		if mutationErr == nil {
			mutationErr = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		connection.Close()

		if mutationErr == nil {
			resultEventID := ""
			if mutationResult.outcome == domain.CommandApplied {
				resultEventID = event.ID
			}
			return domain.CommandResult{
				Outcome:         mutationResult.outcome,
				ObservedVersion: mutationResult.observedVersion,
				EventID:         resultEventID,
			}, nil
		}
		if result, found, replayErr := store.replay(ctx, command); found {
			if replayErr != nil {
				return domain.CommandResult{}, boundedMutationError(replayErr)
			}
			return result, nil
		} else if replayErr != nil {
			return domain.CommandResult{}, boundedMutationError(replayErr)
		}
		if isTransactionContention(mutationErr) {
			continue
		}
		return domain.CommandResult{}, boundedMutationError(mutationErr)
	}
	return domain.CommandResult{}, backendFailure(storeport.HealthRetryBudgetExhausted)
}

func insertAggregate(ctx context.Context, tx *sql.Tx, id, kind string, parent any, version uint64, data []byte) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO aggregates (id, kind, parent_id, version, data) VALUES (?, ?, ?, ?, ?)`,
		id, kind, parent, version, data,
	)
	if isDuplicateError(err) {
		return storeport.ErrAlreadyExists
	}
	return err
}

func updateAggregate(
	ctx context.Context,
	tx *sql.Tx,
	id, kind string,
	expectedVersion, replacementVersion uint64,
	data []byte,
) (mutationResult, error) {
	if replacementVersion != expectedVersion+1 {
		return mutationResult{}, fmt.Errorf("%w: replacement version must advance exactly once", storeport.ErrInvalidRecord)
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE aggregates SET version = ?, data = ? WHERE id = ? AND kind = ? AND version = ?`,
		replacementVersion, data, id, kind, expectedVersion,
	)
	if err != nil {
		return mutationResult{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return mutationResult{}, err
	}
	if rows == 1 {
		return mutationResult{outcome: domain.CommandApplied, observedVersion: replacementVersion}, nil
	}
	var actualKind string
	var actualVersion uint64
	if err := tx.QueryRowContext(ctx,
		`SELECT kind, version FROM aggregates WHERE id = ?`, id,
	).Scan(&actualKind, &actualVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return mutationResult{}, storeport.ErrNotFound
		}
		return mutationResult{}, err
	}
	if actualKind != kind {
		return mutationResult{}, backendFailure(storeport.HealthStoredRecordInvalid)
	}
	return mutationResult{
		outcome:         domain.CommandRejectedVersionConflict,
		observedVersion: actualVersion,
	}, nil
}

func validateCreateCommand(command domain.CommandRequest, aggregateID string, version uint64) error {
	if command.AggregateID != aggregateID || command.ExpectedVersion != 0 || version != 0 {
		return fmt.Errorf("%w: invalid create command version or target", storeport.ErrInvalidRecord)
	}
	return nil
}

func validateUpdateCommand(command domain.CommandRequest, aggregateID string) error {
	if command.AggregateID != aggregateID {
		return fmt.Errorf("%w: update command targets another aggregate", storeport.ErrInvalidRecord)
	}
	return nil
}

func marshalRecord(value any) ([]byte, error) {
	return marshalRecordLimit(value, maximumJSONBytes)
}

func marshalProjectRecord(value any) ([]byte, error) {
	return marshalRecordLimit(value, maximumProjectJSONBytes)
}

func marshalRecordLimit(value any, maximum int) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode aggregate: %w", err)
	}
	if len(data) > maximum {
		return nil, fmt.Errorf("%w: aggregate exceeds %d bytes", storeport.ErrInvalidRecord, maximum)
	}
	return data, nil
}
