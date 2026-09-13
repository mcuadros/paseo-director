// SPDX-License-Identifier: Apache-2.0

package dolt

import (
	"context"
	"crypto/sha1" // #nosec G505 -- required by Dolt's mysql_native_password verifier format
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

// AuthorityCode is the complete path-free result vocabulary for production
// TaskStore identity and grant verification.
type AuthorityCode string

const (
	AuthorityCurrent                  AuthorityCode = "taskstore_authority_current"
	AuthorityConfigurationInvalid     AuthorityCode = "taskstore_authority_configuration_invalid"
	AuthorityUnavailable              AuthorityCode = "taskstore_authority_unavailable"
	AuthorityPrincipalMismatch        AuthorityCode = "taskstore_authority_principal_mismatch"
	AuthorityBroadGrantRefused        AuthorityCode = "taskstore_authority_broad_grant_refused"
	AuthorityMaintenanceGrantMismatch AuthorityCode = "taskstore_maintenance_grant_mismatch"
	AuthorityAccountProvisionFailed   AuthorityCode = "taskstore_authority_account_provision_failed"
	AuthorityGrantProvisionFailed     AuthorityCode = "taskstore_authority_grant_provision_failed"
	AuthorityAttestationMismatch      AuthorityCode = "taskstore_authority_attestation_mismatch"
)

var (
	ErrRuntimeAuthority = errors.New("TaskStore runtime authority is unsafe")
	errAccountCreate    = errors.New("TaskStore runtime account creation failed")
	errAccountAlter     = errors.New("TaskStore runtime account update failed")
	errAccountRevoke    = errors.New("TaskStore runtime account reset failed")
	accountPartPattern  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
)

// AuthorityStatus contains only a bounded code and a digest of normalized
// identities and grants. It never contains SQL, a credential, or a path.
type AuthorityStatus struct {
	Code   AuthorityCode
	SHA256 string
	Exact  bool
}

// RuntimeIdentity is accepted only by the owner-authorized bootstrap adapter.
// It is never serialized by the TaskStore or returned by diagnostics.
type RuntimeIdentity struct {
	User     string
	Host     string
	Password string
}

// RuntimeAuthorityBootstrap is the typed M6.11 composition seam. The owner
// endpoint is transient bootstrap authority; only the three resulting runtime
// identities are returned to production configuration.
type RuntimeAuthorityBootstrap struct {
	Owner         Endpoint
	StoreID       string
	Control       RuntimeIdentity
	Writer        RuntimeIdentity
	Maintenance   RuntimeIdentity
	PrivilegeFile string
}

func safeSQLPrincipal(value string) bool {
	parts := strings.Split(value, "@")
	return len(parts) == 2 && accountPartPattern.MatchString(parts[0]) && (parts[1] == "%" || parts[1] == "localhost")
}

func validRuntimeIdentity(value RuntimeIdentity) bool {
	if !accountPartPattern.MatchString(value.User) || (value.Host != "%" && value.Host != "localhost") ||
		len(value.Password) == 0 || len(value.Password) > 1024 || !utf8.ValidString(value.Password) {
		return false
	}
	return strings.IndexFunc(value.Password, unicode.IsControl) < 0
}

func (value RuntimeIdentity) principal() string { return value.User + "@" + value.Host }

func quoteAccount(value RuntimeIdentity) string {
	return "`" + value.User + "`@`" + value.Host + "`"
}

// MaintenanceDatabaseNames returns the two fixed store-bound database names
// used for fresh validation and retained failed-migration recovery. Fixed names
// let bootstrap grant exact database scope before either database exists.
func MaintenanceDatabaseNames(storeID string) (validation string, recovery string, err error) {
	if !safeIdentifier(storeID, 128) {
		return "", "", ErrRuntimeAuthority
	}
	digest := sha256.Sum256([]byte("director-taskstore-maintenance-databases\x1f" + storeID))
	suffix := hex.EncodeToString(digest[:12])
	return "director_validate_" + suffix, "director_recovery_" + suffix, nil
}

func normalizedGrant(value string) string {
	value = strings.NewReplacer("`", "", "'", "").Replace(value)
	return strings.ToUpper(strings.Join(strings.Fields(value), " "))
}

func lowerHexDigest(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func privilegeFileDigest(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", ErrRuntimeAuthority
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return "", ErrRuntimeAuthority
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Mode().Perm() != 0o600 ||
		before.Size() <= 0 || before.Size() > 16*1024*1024 {
		return "", ErrRuntimeAuthority
	}
	identity, ok := before.Sys().(*syscall.Stat_t)
	if !ok || identity.Uid != uint32(os.Geteuid()) {
		return "", ErrRuntimeAuthority
	}
	document, err := os.ReadFile(path)
	if err != nil {
		return "", ErrRuntimeAuthority
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime() != after.ModTime() || before.Mode() != after.Mode() {
		return "", ErrRuntimeAuthority
	}
	digest := sha256.Sum256(document)
	return hex.EncodeToString(digest[:]), nil
}

func ownedPrivilegeFileCandidate(path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) != "privileges.db" {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > 16*1024*1024 {
		return false
	}
	identity, ok := info.Sys().(*syscall.Stat_t)
	if !ok || identity.Uid != uint32(os.Geteuid()) {
		return false
	}
	_, err = exactOwnedDirectory(filepath.Dir(path))
	return err == nil
}

func (store *DoltTaskStore) verifyPrivilegeAttestation() bool {
	if store == nil || !lowerHexDigest(store.privilegeFileSHA256) {
		return false
	}
	digest, err := privilegeFileDigest(store.privilegeFile)
	return err == nil && digest == store.privilegeFileSHA256
}

func readCurrentPrincipal(ctx context.Context, database *sql.DB) (string, error) {
	connection, err := database.Conn(ctx)
	if err != nil {
		return "", ErrRuntimeAuthority
	}
	defer connection.Close()
	var principal string
	if connection.QueryRowContext(ctx, `SELECT CURRENT_USER()`).Scan(&principal) != nil {
		return "", ErrRuntimeAuthority
	}
	return principal, nil
}

func readGrantsAsOwner(ctx context.Context, connection *sql.Conn, identity RuntimeIdentity) ([]string, error) {
	rows, err := connection.QueryContext(ctx, "SHOW GRANTS FOR "+quoteAccount(identity))
	if err != nil {
		return nil, ErrRuntimeAuthority
	}
	defer rows.Close()
	var grants []string
	for rows.Next() {
		var grant string
		if rows.Scan(&grant) != nil || len(grant) == 0 || len(grant) > 4096 {
			return nil, ErrRuntimeAuthority
		}
		grants = append(grants, normalizedGrant(grant))
	}
	if rows.Err() != nil || len(grants) == 0 || len(grants) > 64 {
		return nil, ErrRuntimeAuthority
	}
	sort.Strings(grants)
	return grants, nil
}

func authorityDigest(values ...string) string {
	digest := sha256.New()
	for _, value := range values {
		_, _ = digest.Write([]byte{byte(len(value) >> 24), byte(len(value) >> 16), byte(len(value) >> 8), byte(len(value))})
		_, _ = digest.Write([]byte(value))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func broadGrant(grant string) bool {
	if strings.Contains(grant, " WITH GRANT OPTION") || strings.HasPrefix(grant, "GRANT ALL ") ||
		strings.Contains(grant, " SUPER") || strings.Contains(grant, "CREATE USER") || strings.Contains(grant, "_ADMIN") {
		return true
	}
	return strings.Contains(grant, " ON *.*") && !strings.HasPrefix(grant, "GRANT USAGE ON *.* TO ")
}

func narrowRuntimeGrants(grants []string) bool {
	nonUsage := false
	for _, grant := range grants {
		if broadGrant(grant) || strings.Contains(grant, ".DOLT_BACKUP TO ") {
			return false
		}
		if !strings.HasPrefix(grant, "GRANT USAGE ON *.* TO ") {
			nonUsage = true
		}
	}
	return nonUsage
}

func exactMaintenanceGrants(database, principal string, grants []string) bool {
	wanted := []string{
		normalizedGrant("GRANT USAGE ON *.* TO " + principal),
		normalizedGrant("GRANT EXECUTE ON PROCEDURE " + database + ".dolt_backup TO " + principal),
	}
	sort.Strings(wanted)
	return len(grants) == len(wanted) && grants[0] == wanted[0] && grants[1] == wanted[1]
}

func (store *DoltTaskStore) verifyMaintenanceAuthority(ctx context.Context) (AuthorityStatus, error) {
	if store == nil || store.maintenanceClient == nil {
		return AuthorityStatus{Code: AuthorityConfigurationInvalid}, ErrRuntimeAuthority
	}
	if !store.requireLeastPrivilege {
		return AuthorityStatus{Code: AuthorityCurrent, SHA256: authorityDigest("fixture-authority"), Exact: true}, nil
	}
	if !store.verifyPrivilegeAttestation() {
		return AuthorityStatus{Code: AuthorityAttestationMismatch}, ErrRuntimeAuthority
	}
	principal, err := readCurrentPrincipal(ctx, store.maintenanceClient)
	if err != nil {
		return AuthorityStatus{Code: AuthorityUnavailable}, ErrRuntimeAuthority
	}
	if principal != store.maintenancePrincipal {
		return AuthorityStatus{Code: AuthorityPrincipalMismatch}, ErrRuntimeAuthority
	}
	return AuthorityStatus{Code: AuthorityCurrent, SHA256: store.authoritySHA256, Exact: lowerHexDigest(store.authoritySHA256)}, nil
}

// VerifyRuntimeAuthority proves that production uses three exact principals,
// rejects administrator/grant-option authority on normal Engine identities,
// and grants the maintenance identity only exact DOLT_BACKUP execution.
func (store *DoltTaskStore) VerifyRuntimeAuthority(ctx context.Context) (AuthorityStatus, error) {
	if store == nil || !store.requireLeastPrivilege || !safeSQLPrincipal(store.controlPrincipal) ||
		!safeSQLPrincipal(store.writerPrincipal) || !safeSQLPrincipal(store.maintenancePrincipal) || !lowerHexDigest(store.authoritySHA256) {
		return AuthorityStatus{Code: AuthorityConfigurationInvalid}, ErrRuntimeAuthority
	}
	if !store.verifyPrivilegeAttestation() {
		return AuthorityStatus{Code: AuthorityAttestationMismatch}, ErrRuntimeAuthority
	}
	values := []struct {
		database  *sql.DB
		principal string
	}{
		{store.control, store.controlPrincipal},
		{store.writer, store.writerPrincipal},
		{store.maintenanceClient, store.maintenancePrincipal},
	}
	for _, value := range values {
		principal, err := readCurrentPrincipal(ctx, value.database)
		if err != nil {
			return AuthorityStatus{Code: AuthorityUnavailable}, ErrRuntimeAuthority
		}
		if principal != value.principal {
			return AuthorityStatus{Code: AuthorityPrincipalMismatch}, ErrRuntimeAuthority
		}
	}
	return AuthorityStatus{Code: AuthorityCurrent, SHA256: store.authoritySHA256, Exact: true}, nil
}

func executeAuthorityStatement(ctx context.Context, connection *sql.Conn, statement string, arguments ...any) error {
	if _, err := connection.ExecContext(ctx, statement, arguments...); err != nil {
		return ErrRuntimeAuthority
	}
	return nil
}

func prepareRuntimeAccount(ctx context.Context, connection *sql.Conn, identity RuntimeIdentity) error {
	account := quoteAccount(identity)
	first := sha1.Sum([]byte(identity.Password))
	second := sha1.Sum(first[:])
	authenticationHash := "*" + strings.ToUpper(hex.EncodeToString(second[:]))
	if executeAuthorityStatement(ctx, connection, "CREATE USER IF NOT EXISTS "+account) != nil {
		return errAccountCreate
	}
	if executeAuthorityStatement(ctx, connection, "ALTER USER "+account+" IDENTIFIED WITH mysql_native_password AS '"+authenticationHash+"'") != nil {
		return errAccountAlter
	}
	if executeAuthorityStatement(ctx, connection, "REVOKE ALL PRIVILEGES, GRANT OPTION FROM "+account) != nil {
		return errAccountRevoke
	}
	return nil
}

func grantStatements(database, validation, recovery string, control, writer, maintenance RuntimeIdentity) []string {
	db, validateDB, recoveryDB := quoteDatabase(database), quoteDatabase(validation), quoteDatabase(recovery)
	controlAccount, writerAccount, maintenanceAccount := quoteAccount(control), quoteAccount(writer), quoteAccount(maintenance)
	return []string{
		"GRANT SELECT, TRIGGER ON " + db + ".* TO " + controlAccount,
		"GRANT INSERT, UPDATE ON " + db + ".`aggregates` TO " + controlAccount,
		"GRANT UPDATE, ALTER ON " + db + ".`candidates` TO " + controlAccount,
		"GRANT INSERT ON " + db + ".`command_requests` TO " + controlAccount,
		"GRANT INSERT ON " + db + ".`command_outcomes` TO " + controlAccount,
		"GRANT INSERT ON " + db + ".`events` TO " + controlAccount,
		"GRANT UPDATE ON " + db + ".`event_stream_lock` TO " + controlAccount,
		"GRANT UPDATE ON " + db + ".`schema_metadata` TO " + controlAccount,
		"GRANT INSERT ON " + db + ".`immutable_write_guard` TO " + controlAccount,
		"GRANT EXECUTE ON PROCEDURE " + db + ".`dolt_verify_constraints` TO " + controlAccount,
		"GRANT SELECT, DROP, TRIGGER ON " + validateDB + ".* TO " + controlAccount,
		"GRANT EXECUTE ON PROCEDURE " + validateDB + ".`dolt_verify_constraints` TO " + controlAccount,
		"GRANT SELECT, TRIGGER ON " + recoveryDB + ".* TO " + controlAccount,
		"GRANT EXECUTE ON PROCEDURE " + recoveryDB + ".`dolt_verify_constraints` TO " + controlAccount,
		"GRANT SELECT ON " + db + ".* TO " + writerAccount,
		"GRANT INSERT, UPDATE ON " + db + ".`aggregates` TO " + writerAccount,
		"GRANT INSERT ON " + db + ".`candidates` TO " + writerAccount,
		"GRANT INSERT ON " + db + ".`command_requests` TO " + writerAccount,
		"GRANT INSERT ON " + db + ".`command_outcomes` TO " + writerAccount,
		"GRANT INSERT ON " + db + ".`events` TO " + writerAccount,
		"GRANT UPDATE ON " + db + ".`event_stream_lock` TO " + writerAccount,
		"GRANT EXECUTE ON PROCEDURE " + db + ".`dolt_backup` TO " + maintenanceAccount,
	}
}

// ProvisionRuntimeAuthority is called by the supported TaskStore bootstrap.
// It idempotently resets only the three exact runtime identities, grants the
// closed production capability set, and verifies it before returning.
func ProvisionRuntimeAuthority(ctx context.Context, bootstrap RuntimeAuthorityBootstrap) (Config, AuthorityStatus, error) {
	identities := []RuntimeIdentity{bootstrap.Control, bootstrap.Writer, bootstrap.Maintenance}
	host, _, addressErr := net.SplitHostPort(bootstrap.Owner.Address)
	address := net.ParseIP(host)
	if !safeIdentifier(bootstrap.StoreID, 128) || bootstrap.Owner.Address == "" || bootstrap.Owner.Database == "" || bootstrap.Owner.User == "" ||
		addressErr != nil || address == nil || !address.IsLoopback() || !safeIdentifier(bootstrap.Owner.Database, 128) ||
		!filepath.IsAbs(bootstrap.PrivilegeFile) || filepath.Clean(bootstrap.PrivilegeFile) != bootstrap.PrivilegeFile ||
		!validRuntimeIdentity(identities[0]) || !validRuntimeIdentity(identities[1]) || !validRuntimeIdentity(identities[2]) ||
		identities[0].principal() == identities[1].principal() || identities[0].principal() == identities[2].principal() ||
		identities[1].principal() == identities[2].principal() {
		return Config{}, AuthorityStatus{Code: AuthorityConfigurationInvalid}, ErrRuntimeAuthority
	}
	validation, recovery, err := MaintenanceDatabaseNames(bootstrap.StoreID)
	if err != nil {
		return Config{}, AuthorityStatus{Code: AuthorityConfigurationInvalid}, ErrRuntimeAuthority
	}
	owner, err := openEndpoint(bootstrap.Owner)
	if err != nil {
		return Config{}, AuthorityStatus{Code: AuthorityUnavailable}, ErrRuntimeAuthority
	}
	defer owner.Close()
	connection, err := owner.Conn(ctx)
	if err != nil {
		return Config{}, AuthorityStatus{Code: AuthorityUnavailable}, ErrRuntimeAuthority
	}
	var database, version string
	if connection.QueryRowContext(ctx, `SELECT DATABASE(), DOLT_VERSION()`).Scan(&database, &version) != nil ||
		database != bootstrap.Owner.Database || version != supportedDoltVersion {
		connection.Close()
		return Config{}, AuthorityStatus{Code: AuthorityUnavailable}, ErrRuntimeAuthority
	}
	for _, identity := range identities {
		if prepareRuntimeAccount(ctx, connection, identity) != nil {
			connection.Close()
			return Config{}, AuthorityStatus{Code: AuthorityAccountProvisionFailed}, ErrRuntimeAuthority
		}
	}
	statements := grantStatements(database, validation, recovery, identities[0], identities[1], identities[2])
	for _, statement := range statements {
		if executeAuthorityStatement(ctx, connection, statement) != nil {
			connection.Close()
			return Config{}, AuthorityStatus{Code: AuthorityGrantProvisionFailed}, ErrRuntimeAuthority
		}
	}
	attestationValues := []string{bootstrap.StoreID, database, validation, recovery}
	for index, identity := range identities {
		grants, grantsErr := readGrantsAsOwner(ctx, connection, identity)
		if grantsErr != nil {
			connection.Close()
			return Config{}, AuthorityStatus{Code: AuthorityUnavailable}, ErrRuntimeAuthority
		}
		if index < 2 && !narrowRuntimeGrants(grants) {
			connection.Close()
			return Config{}, AuthorityStatus{Code: AuthorityBroadGrantRefused}, ErrRuntimeAuthority
		}
		if index == 2 && !exactMaintenanceGrants(database, identity.principal(), grants) {
			connection.Close()
			return Config{}, AuthorityStatus{Code: AuthorityMaintenanceGrantMismatch}, ErrRuntimeAuthority
		}
		attestationValues = append(attestationValues, identity.principal())
		attestationValues = append(attestationValues, grants...)
	}
	authoritySHA256 := authorityDigest(attestationValues...)
	if connection.Close() != nil {
		return Config{}, AuthorityStatus{Code: AuthorityUnavailable}, ErrRuntimeAuthority
	}
	if !ownedPrivilegeFileCandidate(bootstrap.PrivilegeFile) || os.Chmod(bootstrap.PrivilegeFile, 0o600) != nil {
		return Config{}, AuthorityStatus{Code: AuthorityUnavailable}, ErrRuntimeAuthority
	}
	privilegeFile, openErr := os.Open(bootstrap.PrivilegeFile)
	if openErr != nil {
		return Config{}, AuthorityStatus{Code: AuthorityUnavailable}, ErrRuntimeAuthority
	}
	syncErr, closeErr := privilegeFile.Sync(), privilegeFile.Close()
	if syncErr != nil || closeErr != nil || syncDirectory(filepath.Dir(bootstrap.PrivilegeFile)) != nil {
		return Config{}, AuthorityStatus{Code: AuthorityUnavailable}, ErrRuntimeAuthority
	}
	privilegeFileSHA256, err := privilegeFileDigest(bootstrap.PrivilegeFile)
	if err != nil {
		return Config{}, AuthorityStatus{Code: AuthorityUnavailable}, ErrRuntimeAuthority
	}
	config := Config{
		Control:     Endpoint{Address: bootstrap.Owner.Address, Database: database, User: identities[0].User, Password: identities[0].Password, Principal: identities[0].principal()},
		Writer:      Endpoint{Address: bootstrap.Owner.Address, Database: database, User: identities[1].User, Password: identities[1].Password, Principal: identities[1].principal()},
		Maintenance: Endpoint{Address: bootstrap.Owner.Address, Database: database, User: identities[2].User, Password: identities[2].Password, Principal: identities[2].principal()},
		StoreID:     bootstrap.StoreID, AuthoritySHA256: authoritySHA256, PrivilegeFile: bootstrap.PrivilegeFile,
		PrivilegeFileSHA256: privilegeFileSHA256, RequireLeastPrivilege: true,
	}
	store, err := Open(config)
	if err != nil {
		return Config{}, AuthorityStatus{Code: AuthorityUnavailable}, ErrRuntimeAuthority
	}
	defer store.Close()
	status, err := store.VerifyRuntimeAuthority(ctx)
	if err != nil {
		return Config{}, status, err
	}
	return config, status, nil
}
