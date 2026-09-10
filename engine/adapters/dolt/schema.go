// SPDX-License-Identifier: Apache-2.0

package dolt

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"regexp"
	"slices"
	"sort"
	"strings"

	storeport "github.com/mcuadros/director-engine/ports/taskstore"
)

const currentSchemaVersion = storeport.SchemaVersion

const supportedDoltVersion = "2.3.2"

var expectedTables = []string{
	"aggregates",
	"aggregates_identity",
	"candidates",
	"candidates_identity",
	"command_outcomes",
	"command_outcomes_identity",
	"command_requests",
	"command_requests_identity",
	"event_stream_lock",
	"events",
	"events_identity",
	"guard_constants",
	"immutable_write_guard",
	"parent_guard",
	"schema_metadata",
	"taskstore_identity",
}

var expectedTriggers = []string{
	"aggregates_append_only_identity",
	"aggregates_reject_identity_update",
	"candidates_append_only_identity",
	"candidates_reject_delete",
	"candidates_reject_update",
	"command_outcomes_append_only_identity",
	"command_outcomes_reject_delete",
	"command_outcomes_reject_update",
	"command_requests_append_only_identity",
	"command_requests_reject_delete",
	"command_requests_reject_update",
	"events_append_only_identity",
	"events_reject_delete",
	"events_reject_update",
	"fk_aggregate_parent_present",
	"fk_aggregate_parent_present_on_update",
	"fk_candidate_run_present",
	"fk_command_outcome_event_present",
	"fk_command_request_aggregate_present",
	"fk_event_aggregate_present",
	"fk_event_run_present",
}

var triggerStatementPattern = regexp.MustCompile(
	`(?is)^CREATE\s+TRIGGER\s+([a-z0-9_]+)\s+(BEFORE|AFTER)\s+(INSERT|UPDATE|DELETE)\s+ON\s+([a-z0-9_]+)\s+FOR\s+EACH\s+ROW\s+(.+)$`,
)

var schemaStatements = []string{
	`CREATE TABLE taskstore_identity (
		singleton TINYINT NOT NULL PRIMARY KEY,
		store_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL UNIQUE
	)`,
	`CREATE TABLE aggregates (
		id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL PRIMARY KEY,
		kind VARCHAR(16) NOT NULL,
		parent_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NULL,
		version BIGINT UNSIGNED NOT NULL,
		data JSON NOT NULL,
		updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
		CONSTRAINT fk_aggregate_parent FOREIGN KEY (parent_id) REFERENCES aggregates(id)
	)`,
	`CREATE TABLE candidates (
		id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL PRIMARY KEY,
		run_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
		sequence BIGINT UNSIGNED NOT NULL,
		commit_sha VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		data JSON NOT NULL,
		UNIQUE KEY uq_candidate_run_sequence (run_id, sequence),
		CONSTRAINT fk_candidate_run FOREIGN KEY (run_id) REFERENCES aggregates(id)
	)`,
	`CREATE TABLE event_stream_lock (
		singleton TINYINT NOT NULL PRIMARY KEY,
		next_sequence BIGINT UNSIGNED NOT NULL
	)`,
	`INSERT INTO event_stream_lock (singleton, next_sequence) VALUES (1, 0)`,
	`CREATE TABLE events (
		global_sequence BIGINT UNSIGNED NOT NULL PRIMARY KEY,
		event_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL UNIQUE,
		run_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NULL,
		sequence BIGINT UNSIGNED NOT NULL,
		aggregate_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
		aggregate_version BIGINT UNSIGNED NOT NULL,
		event_type VARCHAR(64) NOT NULL,
		payload JSON NOT NULL,
		created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
		UNIQUE KEY uq_event_run_sequence (run_id, sequence),
		CONSTRAINT fk_event_run FOREIGN KEY (run_id) REFERENCES aggregates(id),
		CONSTRAINT fk_event_aggregate FOREIGN KEY (aggregate_id) REFERENCES aggregates(id)
	)`,
	`CREATE TABLE command_requests (
		idempotency_key VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL PRIMARY KEY,
		command_type VARCHAR(64) NOT NULL,
		aggregate_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL,
		expected_version BIGINT UNSIGNED NOT NULL,
		request_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		payload JSON NOT NULL,
		created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
		CONSTRAINT fk_command_request_aggregate FOREIGN KEY (aggregate_id) REFERENCES aggregates(id)
	)`,
	`CREATE TABLE command_outcomes (
		idempotency_key VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL PRIMARY KEY,
		outcome_type VARCHAR(32) NOT NULL,
		observed_version BIGINT UNSIGNED NOT NULL,
		event_id VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NULL,
		completed_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
		CONSTRAINT fk_command_outcome_request FOREIGN KEY (idempotency_key) REFERENCES command_requests(idempotency_key),
		CONSTRAINT fk_command_outcome_event FOREIGN KEY (event_id) REFERENCES events(event_id)
	)`,
	`CREATE TABLE guard_constants (singleton TINYINT NOT NULL PRIMARY KEY)`,
	`INSERT INTO guard_constants (singleton) VALUES (1)`,
	`CREATE TABLE parent_guard (
		identity VARBINARY(128) NOT NULL PRIMARY KEY
	)`,
	`INSERT INTO parent_guard (identity) VALUES ('guard.parent_missing')`,
	`CREATE TABLE immutable_write_guard (singleton TINYINT NOT NULL PRIMARY KEY)`,
	`INSERT INTO immutable_write_guard (singleton) VALUES (1)`,
	`CREATE TABLE aggregates_identity (identity VARBINARY(640) NOT NULL PRIMARY KEY)`,
	`CREATE TABLE candidates_identity (identity VARBINARY(640) NOT NULL PRIMARY KEY)`,
	`CREATE TABLE events_identity (identity VARBINARY(640) NOT NULL PRIMARY KEY)`,
	`CREATE TABLE command_requests_identity (identity VARBINARY(640) NOT NULL PRIMARY KEY)`,
	`CREATE TABLE command_outcomes_identity (identity VARBINARY(640) NOT NULL PRIMARY KEY)`,
	`CREATE TRIGGER aggregates_append_only_identity BEFORE INSERT ON aggregates FOR EACH ROW
		INSERT INTO aggregates_identity (identity) VALUES (CONCAT('aggregates.pk', CHAR(31), NEW.id))`,
	`CREATE TRIGGER candidates_append_only_identity BEFORE INSERT ON candidates FOR EACH ROW
		INSERT INTO candidates_identity (identity) VALUES
			(CONCAT('candidates.pk', CHAR(31), NEW.id)),
			(CONCAT('candidates.run_sequence', CHAR(31), NEW.run_id, CHAR(31), NEW.sequence))`,
	`CREATE TRIGGER events_append_only_identity BEFORE INSERT ON events FOR EACH ROW
		INSERT INTO events_identity (identity) VALUES
			(CONCAT('events.pk', CHAR(31), NEW.global_sequence)),
			(CONCAT('events.event_id', CHAR(31), NEW.event_id)),
			(CONCAT('events.stream_sequence', CHAR(31), COALESCE(NEW.run_id, CONCAT('@', NEW.aggregate_id)), CHAR(31), NEW.sequence))`,
	`CREATE TRIGGER command_requests_append_only_identity BEFORE INSERT ON command_requests FOR EACH ROW
		INSERT INTO command_requests_identity (identity) VALUES (CONCAT('command_requests.pk', CHAR(31), NEW.idempotency_key))`,
	`CREATE TRIGGER command_outcomes_append_only_identity BEFORE INSERT ON command_outcomes FOR EACH ROW
		INSERT INTO command_outcomes_identity (identity) VALUES (CONCAT('command_outcomes.pk', CHAR(31), NEW.idempotency_key))`,
	`CREATE TRIGGER aggregates_reject_identity_update BEFORE UPDATE ON aggregates FOR EACH ROW
		INSERT INTO immutable_write_guard (singleton)
		SELECT singleton FROM guard_constants WHERE NOT (NEW.id <=> OLD.id)`,
	`CREATE TRIGGER candidates_reject_update BEFORE UPDATE ON candidates FOR EACH ROW
		INSERT INTO immutable_write_guard (singleton) VALUES (1)`,
	`CREATE TRIGGER candidates_reject_delete BEFORE DELETE ON candidates FOR EACH ROW
		INSERT INTO immutable_write_guard (singleton) VALUES (1)`,
	`CREATE TRIGGER events_reject_update BEFORE UPDATE ON events FOR EACH ROW
		INSERT INTO immutable_write_guard (singleton) VALUES (1)`,
	`CREATE TRIGGER events_reject_delete BEFORE DELETE ON events FOR EACH ROW
		INSERT INTO immutable_write_guard (singleton) VALUES (1)`,
	`CREATE TRIGGER command_requests_reject_update BEFORE UPDATE ON command_requests FOR EACH ROW
		INSERT INTO immutable_write_guard (singleton) VALUES (1)`,
	`CREATE TRIGGER command_requests_reject_delete BEFORE DELETE ON command_requests FOR EACH ROW
		INSERT INTO immutable_write_guard (singleton) VALUES (1)`,
	`CREATE TRIGGER command_outcomes_reject_update BEFORE UPDATE ON command_outcomes FOR EACH ROW
		INSERT INTO immutable_write_guard (singleton) VALUES (1)`,
	`CREATE TRIGGER command_outcomes_reject_delete BEFORE DELETE ON command_outcomes FOR EACH ROW
		INSERT INTO immutable_write_guard (singleton) VALUES (1)`,
	`CREATE TRIGGER fk_aggregate_parent_present BEFORE INSERT ON aggregates FOR EACH ROW
		INSERT INTO parent_guard (identity)
		SELECT 'guard.parent_missing' FROM guard_constants
		LEFT JOIN aggregates AS guarded_parent ON guarded_parent.id = NEW.parent_id
		WHERE NEW.parent_id IS NOT NULL AND guarded_parent.id IS NULL`,
	`CREATE TRIGGER fk_aggregate_parent_present_on_update BEFORE UPDATE ON aggregates FOR EACH ROW
		INSERT INTO parent_guard (identity)
		SELECT 'guard.parent_missing' FROM guard_constants
		LEFT JOIN aggregates AS guarded_parent ON guarded_parent.id = NEW.parent_id
		WHERE NEW.parent_id IS NOT NULL AND guarded_parent.id IS NULL`,
	`CREATE TRIGGER fk_candidate_run_present BEFORE INSERT ON candidates FOR EACH ROW
		INSERT INTO parent_guard (identity)
		SELECT 'guard.parent_missing' FROM guard_constants
		LEFT JOIN aggregates AS guarded_parent ON guarded_parent.id = NEW.run_id
		WHERE guarded_parent.id IS NULL`,
	`CREATE TRIGGER fk_event_run_present BEFORE INSERT ON events FOR EACH ROW
		INSERT INTO parent_guard (identity)
		SELECT 'guard.parent_missing' FROM guard_constants
		LEFT JOIN aggregates AS guarded_parent ON guarded_parent.id = NEW.run_id
		WHERE NEW.run_id IS NOT NULL AND guarded_parent.id IS NULL`,
	`CREATE TRIGGER fk_event_aggregate_present BEFORE INSERT ON events FOR EACH ROW
		INSERT INTO parent_guard (identity)
		SELECT 'guard.parent_missing' FROM guard_constants
		LEFT JOIN aggregates AS guarded_parent ON guarded_parent.id = NEW.aggregate_id
		WHERE guarded_parent.id IS NULL`,
	`CREATE TRIGGER fk_command_request_aggregate_present BEFORE INSERT ON command_requests FOR EACH ROW
		INSERT INTO parent_guard (identity)
		SELECT 'guard.parent_missing' FROM guard_constants
		LEFT JOIN aggregates AS guarded_parent ON guarded_parent.id = NEW.aggregate_id
		WHERE guarded_parent.id IS NULL`,
	`CREATE TRIGGER fk_command_outcome_event_present BEFORE INSERT ON command_outcomes FOR EACH ROW
		INSERT INTO parent_guard (identity)
		SELECT 'guard.parent_missing' FROM guard_constants
		LEFT JOIN events AS guarded_parent ON guarded_parent.event_id = NEW.event_id
		WHERE NEW.event_id IS NOT NULL AND guarded_parent.event_id IS NULL`,
	`CREATE TABLE schema_metadata (
		singleton TINYINT NOT NULL PRIMARY KEY,
		schema_version INT NOT NULL
	)`,
	`INSERT INTO schema_metadata (singleton, schema_version) VALUES (1, 2)`,
}

func setAndVerifySafeCommitMode(ctx context.Context, connection *sql.Conn, global bool) error {
	if global {
		if _, err := connection.ExecContext(ctx, `SET @@GLOBAL.dolt_force_transaction_commit = 0`); err != nil {
			return backendFailure(storeport.HealthGlobalCommitModeSetFailed)
		}
	}
	if _, err := connection.ExecContext(ctx, `SET @@SESSION.dolt_force_transaction_commit = 0`); err != nil {
		return backendFailure(storeport.HealthSessionCommitModeSetFailed)
	}
	var sessionValue, globalValue string
	if err := connection.QueryRowContext(ctx,
		`SELECT CAST(@@SESSION.dolt_force_transaction_commit AS CHAR), CAST(@@GLOBAL.dolt_force_transaction_commit AS CHAR)`,
	).Scan(&sessionValue, &globalValue); err != nil {
		return backendFailure(storeport.HealthCommitModeReadFailed)
	}
	if sessionValue != "0" {
		return backendFailure(storeport.HealthUnsafeSessionCommitMode)
	}
	if globalValue != "0" {
		return backendFailure(storeport.HealthUnsafeGlobalCommitMode)
	}
	return nil
}

func (store *DoltTaskStore) verifyDatabase(ctx context.Context, connection *sql.Conn) error {
	var database, version string
	if err := connection.QueryRowContext(ctx, `SELECT DATABASE(), DOLT_VERSION()`).Scan(&database, &version); err != nil {
		return backendFailure(storeport.HealthDatabaseIdentityReadFailed)
	}
	if database != store.database {
		return backendFailure(storeport.HealthDatabaseIdentityMismatch)
	}
	if version != supportedDoltVersion {
		return backendFailure(storeport.HealthBackendVersionMismatch)
	}
	return nil
}

func (store *DoltTaskStore) verifyStoreIdentity(ctx context.Context, connection *sql.Conn) error {
	if err := store.verifyDatabase(ctx, connection); err != nil {
		return err
	}
	var storeID string
	if err := connection.QueryRowContext(ctx,
		`SELECT store_id FROM taskstore_identity WHERE singleton = 1`,
	).Scan(&storeID); err != nil {
		return backendFailure(storeport.HealthStoreIdentityReadFailed)
	}
	if storeID != store.storeID {
		return backendFailure(storeport.HealthStoreIdentityMismatch)
	}
	return nil
}

func schemaTables(ctx context.Context, connection *sql.Conn) ([]string, error) {
	rows, err := connection.QueryContext(ctx,
		`SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() ORDER BY table_name`,
	)
	if err != nil {
		return nil, storeport.InvalidSchema(storeport.SchemaInspectionFailed)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, storeport.InvalidSchema(storeport.SchemaInspectionFailed)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		return nil, storeport.InvalidSchema(storeport.SchemaInspectionFailed)
	}
	if err := rows.Close(); err != nil {
		return nil, storeport.InvalidSchema(storeport.SchemaInspectionFailed)
	}
	return tables, nil
}

func completeSchemaTables(tables []string) bool {
	actual := slices.Clone(tables)
	sort.Strings(actual)
	expected := slices.Clone(expectedTables)
	sort.Strings(expected)
	return slices.Equal(actual, expected)
}

func normalizedSQL(statement string) string {
	var normalized strings.Builder
	normalized.Grow(len(statement))
	var quote byte
	pendingSpace := false
	for index := 0; index < len(statement); index++ {
		character := statement[index]
		if quote != 0 {
			normalized.WriteByte(character)
			switch {
			case character == '\\' && index+1 < len(statement):
				index++
				normalized.WriteByte(statement[index])
			case character == quote && index+1 < len(statement) && statement[index+1] == quote:
				index++
				normalized.WriteByte(statement[index])
			case character == quote:
				quote = 0
			}
			continue
		}
		if character == '\'' || character == '"' || character == '`' {
			if pendingSpace && normalized.Len() > 0 {
				normalized.WriteByte(' ')
			}
			pendingSpace = false
			quote = character
			normalized.WriteByte(character)
			continue
		}
		if character == ' ' || character == '\t' || character == '\r' || character == '\n' || character == '\f' {
			pendingSpace = normalized.Len() > 0
			continue
		}
		if pendingSpace {
			normalized.WriteByte(' ')
			pendingSpace = false
		}
		normalized.WriteByte(character)
	}
	return normalized.String()
}

func triggerDigest(event, table, timing, statement string) [sha256.Size]byte {
	return sha256.Sum256([]byte(strings.Join(
		[]string{event, table, timing, normalizedSQL(statement)}, "\x1f",
	)))
}

func expectedTriggerDigests() (map[string][sha256.Size]byte, bool) {
	digests := make(map[string][sha256.Size]byte)
	for _, statement := range schemaStatements {
		match := triggerStatementPattern.FindStringSubmatch(strings.TrimSpace(statement))
		if match == nil {
			continue
		}
		name := match[1]
		if _, duplicate := digests[name]; duplicate {
			return nil, false
		}
		digests[name] = triggerDigest(match[3], match[4], match[2], match[5])
	}
	if len(digests) != len(expectedTriggers) {
		return nil, false
	}
	for _, name := range expectedTriggers {
		if _, ok := digests[name]; !ok {
			return nil, false
		}
	}
	return digests, true
}

func schemaTriggers(ctx context.Context, connection *sql.Conn) (map[string][sha256.Size]byte, error) {
	rows, err := connection.QueryContext(ctx,
		`SELECT trigger_name, event_manipulation, event_object_table, action_timing, action_statement
		FROM information_schema.triggers WHERE trigger_schema = DATABASE() ORDER BY trigger_name`,
	)
	if err != nil {
		return nil, storeport.InvalidSchema(storeport.SchemaInspectionFailed)
	}
	defer rows.Close()
	triggers := make(map[string][sha256.Size]byte)
	for rows.Next() {
		var name, event, table, timing, statement string
		if err := rows.Scan(&name, &event, &table, &timing, &statement); err != nil {
			return nil, storeport.InvalidSchema(storeport.SchemaInspectionFailed)
		}
		if _, duplicate := triggers[name]; duplicate {
			return nil, storeport.InvalidSchema(storeport.SchemaTriggerSetMismatch)
		}
		triggers[name] = triggerDigest(event, table, timing, statement)
	}
	if err := rows.Err(); err != nil {
		return nil, storeport.InvalidSchema(storeport.SchemaInspectionFailed)
	}
	if err := rows.Close(); err != nil {
		return nil, storeport.InvalidSchema(storeport.SchemaInspectionFailed)
	}
	return triggers, nil
}

func completeSchemaTriggers(triggers map[string][sha256.Size]byte) bool {
	expected, ok := expectedTriggerDigests()
	if !ok || len(triggers) != len(expected) {
		return false
	}
	for name, digest := range expected {
		if actual, ok := triggers[name]; !ok || actual != digest {
			return false
		}
	}
	return true
}

func verifyGuardRows(ctx context.Context, connection *sql.Conn) error {
	var guardTotal, guardExpected int
	var parentTotal, parentExpected int
	var immutableTotal, immutableExpected int
	err := connection.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM guard_constants),
			(SELECT COUNT(*) FROM guard_constants WHERE singleton = 1),
			(SELECT COUNT(*) FROM parent_guard),
			(SELECT COUNT(*) FROM parent_guard WHERE HEX(identity) = HEX('guard.parent_missing')),
			(SELECT COUNT(*) FROM immutable_write_guard),
			(SELECT COUNT(*) FROM immutable_write_guard WHERE singleton = 1)`,
	).Scan(
		&guardTotal, &guardExpected,
		&parentTotal, &parentExpected,
		&immutableTotal, &immutableExpected,
	)
	if err != nil {
		return storeport.InvalidSchema(storeport.SchemaInspectionFailed)
	}
	if guardTotal != 1 || guardExpected != 1 ||
		parentTotal != 1 || parentExpected != 1 ||
		immutableTotal != 1 || immutableExpected != 1 {
		return storeport.InvalidSchema(storeport.SchemaGuardRowsMismatch)
	}
	return nil
}

func verifyCompleteSchema(ctx context.Context, connection *sql.Conn) error {
	tables, err := schemaTables(ctx, connection)
	if err != nil {
		return err
	}
	if !completeSchemaTables(tables) {
		return storeport.InvalidSchema(storeport.SchemaTableSetMismatch)
	}
	if err := verifyGuardRows(ctx, connection); err != nil {
		return err
	}
	triggers, err := schemaTriggers(ctx, connection)
	if err != nil {
		return err
	}
	if !completeSchemaTriggers(triggers) {
		return storeport.InvalidSchema(storeport.SchemaTriggerSetMismatch)
	}
	return nil
}

// Bootstrap installs schema version two only into an empty selected database,
// or verifies an existing store. Partial or unknown schemas fail closed.
func (store *DoltTaskStore) Bootstrap(ctx context.Context) error {
	connection, err := store.control.Conn(ctx)
	if err != nil {
		return backendFailure(storeport.HealthControlConnectionUnavailable)
	}
	defer connection.Close()
	if err := setAndVerifySafeCommitMode(ctx, connection, true); err != nil {
		return err
	}
	if err := store.verifyDatabase(ctx, connection); err != nil {
		return err
	}

	tables, err := schemaTables(ctx, connection)
	if err != nil {
		return err
	}
	if slices.Contains(tables, "schema_metadata") {
		if err := verifyCompleteSchema(ctx, connection); err != nil {
			return err
		}
		_, err := store.schemaVersionOn(ctx, connection)
		return err
	}
	if len(tables) != 0 {
		return storeport.InvalidSchema(storeport.SchemaTableSetMismatch)
	}
	for index, statement := range schemaStatements {
		if _, err := connection.ExecContext(ctx, statement); err != nil {
			return storeport.InvalidSchema(storeport.SchemaInstallFailed)
		}
		if index == 0 {
			if _, err := connection.ExecContext(ctx,
				`INSERT INTO taskstore_identity (singleton, store_id) VALUES (1, ?)`, store.storeID,
			); err != nil {
				return storeport.InvalidSchema(storeport.SchemaIdentityInstallFailed)
			}
		}
	}
	if err := verifyCompleteSchema(ctx, connection); err != nil {
		return err
	}
	_, err = store.schemaVersionOn(ctx, connection)
	return err
}

func (store *DoltTaskStore) schemaVersionOn(ctx context.Context, connection *sql.Conn) (int, error) {
	if err := store.verifyStoreIdentity(ctx, connection); err != nil {
		return 0, err
	}
	var version int
	if err := connection.QueryRowContext(ctx,
		`SELECT schema_version FROM schema_metadata WHERE singleton = 1`,
	).Scan(&version); err != nil {
		return 0, storeport.InvalidSchema(storeport.SchemaVersionReadFailed)
	}
	if version != currentSchemaVersion {
		return version, storeport.InvalidSchema(storeport.SchemaVersionMismatch)
	}
	return version, nil
}

// SchemaVersion validates the exact store identity and returns the one schema
// version understood by this adapter.
func (store *DoltTaskStore) SchemaVersion(ctx context.Context) (int, error) {
	connection, err := store.writer.Conn(ctx)
	if err != nil {
		return 0, backendFailure(storeport.HealthReadConnectionUnavailable)
	}
	defer connection.Close()
	return store.schemaVersionOn(ctx, connection)
}
