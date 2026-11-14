package main

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/auth"
	"backend/internal/crypto"
)

func TestCreateBackupUsesProfileResolvedCredentials(t *testing.T) {
	const key = "01234567890123456789012345678901"
	sourcePassword, err := crypto.EncryptWithDomain("source-secret", key, crypto.ConnectionCredentialDomain(false))
	if err != nil {
		t.Fatalf("encrypt source password: %v", err)
	}
	targetPassword, err := crypto.EncryptWithDomain("target-secret", key, crypto.ConnectionCredentialDomain(false))
	if err != nil {
		t.Fatalf("encrypt target password: %v", err)
	}
	database, state := newBackupProfileConnectionTestDB(t, map[string][]driver.Value{
		"source-profile": profileTestRow("source-profile", "user-1", "nextcloud", "https://source.example.test", "source-user", sourcePassword),
		"target-profile": profileTestRow("target-profile", "user-1", "webdav", "https://target.example.test", "target-user", targetPassword),
	})
	server := &APIServer{db: database, encryptionKey: key, rateLimiter: allowAllFileRateLimiter{}}
	request := httptest.NewRequest(http.MethodPost, "/api/backups", bytes.NewBufferString(`{"source_profile_id":"source-profile","target_profile_id":"target-profile","selected_paths":["/Documents"],"target_dir":"/Backups","cron_expression":"0 0 * * *","timezone":"UTC","retention_count":7,"threads":2}`))
	request = request.WithContext(context.WithValue(request.Context(), auth.ClaimsKey, &auth.Claims{UserID: "user-1"}))
	recorder := httptest.NewRecorder()

	server.handleCreateBackup(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if state.backupInsertArgs == nil {
		t.Fatal("backup job was not created")
	}
	for _, check := range []struct {
		name   string
		arg    driver.Value
		secret string
	}{
		{name: "source", arg: state.backupInsertArgs[5], secret: "source-secret"},
		{name: "target", arg: state.backupInsertArgs[12], secret: "target-secret"},
	} {
		ciphertext, ok := check.arg.(string)
		if !ok {
			t.Fatalf("%s password type = %T, want string", check.name, check.arg)
		}
		plain, decryptErr := crypto.DecryptWithDomain(ciphertext, key, crypto.ConnectionCredentialDomain(false))
		if decryptErr != nil || plain != check.secret {
			t.Fatalf("%s stored password = %q, %v", check.name, plain, decryptErr)
		}
	}
}

func TestCreateBackupMapsProfileCredentialFailureToBackupConnectionError(t *testing.T) {
	const key = "01234567890123456789012345678901"
	database, _ := newBackupProfileConnectionTestDB(t, map[string][]driver.Value{
		"source-profile": profileTestRow("source-profile", "user-1", "nextcloud", "https://source.example.test", "source-user", "not-valid-ciphertext"),
	})
	server := &APIServer{db: database, encryptionKey: key, rateLimiter: allowAllFileRateLimiter{}}
	request := httptest.NewRequest(http.MethodPost, "/api/backups", bytes.NewBufferString(`{"source_profile_id":"source-profile","target_profile_id":"target-profile","selected_paths":["/Documents"],"target_dir":"/Backups","cron_expression":"0 0 * * *","timezone":"UTC","retention_count":7,"threads":2}`))
	request = request.WithContext(context.WithValue(request.Context(), auth.ClaimsKey, &auth.Claims{UserID: "user-1"}))
	recorder := httptest.NewRecorder()

	server.handleCreateBackup(recorder, request)

	assertAPIError(t, recorder, http.StatusInternalServerError, ErrBackupConnectionFailed)
}

func TestCreateSyncUsesProfileResolvedCredentials(t *testing.T) {
	const key = "01234567890123456789012345678901"
	sourcePassword, _ := crypto.EncryptWithDomain("source-secret", key, crypto.ConnectionCredentialDomain(false))
	targetPassword, _ := crypto.EncryptWithDomain("target-secret", key, crypto.ConnectionCredentialDomain(false))
	database, state := newBackupProfileConnectionTestDB(t, map[string][]driver.Value{
		"source-profile": profileTestRow("source-profile", "user-1", "nextcloud", "https://source.example.test", "source-user", sourcePassword),
		"target-profile": profileTestRow("target-profile", "user-1", "webdav", "https://target.example.test", "target-user", targetPassword),
	})
	server := &APIServer{db: database, encryptionKey: key, rateLimiter: allowAllFileRateLimiter{}}
	request := httptest.NewRequest(http.MethodPost, "/api/sync", bytes.NewBufferString(`{"source_profile_id":"source-profile","target_profile_id":"target-profile","selected_paths":["/Documents"]}`))
	request = request.WithContext(context.WithValue(request.Context(), auth.ClaimsKey, &auth.Claims{UserID: "user-1"}))
	recorder := httptest.NewRecorder()

	server.syncs().handleCreateSync(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	for _, check := range []struct {
		name   string
		arg    driver.Value
		secret string
	}{
		{name: "source", arg: state.syncInsertArgs[5], secret: "source-secret"},
		{name: "target", arg: state.syncInsertArgs[12], secret: "target-secret"},
	} {
		ciphertext, ok := check.arg.(string)
		if !ok {
			t.Fatalf("%s password type = %T, want string", check.name, check.arg)
		}
		plain, decryptErr := crypto.DecryptWithDomain(ciphertext, key, crypto.ConnectionCredentialDomain(false))
		if decryptErr != nil || plain != check.secret {
			t.Fatalf("%s stored password = %q, %v", check.name, plain, decryptErr)
		}
	}
}

func profileTestRow(id, userID, provider, url, username, password string) []driver.Value {
	now := time.Now().UTC()
	return []driver.Value{id, userID, "profile", provider, url, username, password, "", nil, "", "", "", now, now}
}

var backupProfileConnectionDriverID atomic.Uint64

type backupProfileConnectionState struct {
	profiles         map[string][]driver.Value
	backupInsertArgs []driver.Value
	syncInsertArgs   []driver.Value
	ownershipErr     error
}

func newBackupProfileConnectionTestDB(t *testing.T, profiles map[string][]driver.Value) (*sql.DB, *backupProfileConnectionState) {
	t.Helper()
	state := &backupProfileConnectionState{profiles: profiles}
	name := "backup-profile-connection-" + strconv.FormatUint(backupProfileConnectionDriverID.Add(1), 10)
	sql.Register(name, backupProfileConnectionDriver{state: state})
	database, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database, state
}

type backupProfileConnectionDriver struct{ state *backupProfileConnectionState }

func (d backupProfileConnectionDriver) Open(string) (driver.Conn, error) {
	return backupProfileConnectionConn{state: d.state}, nil
}

type backupProfileConnectionConn struct{ state *backupProfileConnectionState }

func (c backupProfileConnectionConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c backupProfileConnectionConn) Close() error                        { return nil }
func (c backupProfileConnectionConn) Begin() (driver.Tx, error) {
	return backupProfileConnectionTx{state: c.state}, nil
}
func (c backupProfileConnectionConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return backupProfileConnectionTx{state: c.state}, nil
}
func (c backupProfileConnectionConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.query(query, args)
}
func (c backupProfileConnectionConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}

func (c backupProfileConnectionConn) query(query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "INSERT INTO backup_jobs") {
		c.state.backupInsertArgs = make([]driver.Value, len(args))
		for i, arg := range args {
			c.state.backupInsertArgs[i] = arg.Value
		}
		return &backupProfileConnectionRows{rows: [][]driver.Value{{"backup-1"}}, columns: 1}, nil
	}
	if strings.Contains(query, "INSERT INTO sync_jobs") {
		c.state.syncInsertArgs = make([]driver.Value, len(args))
		for i, arg := range args {
			c.state.syncInsertArgs[i] = arg.Value
		}
		now := time.Now().UTC()
		return &backupProfileConnectionRows{rows: [][]driver.Value{{"sync-1", now, now}}, columns: 3}, nil
	}
	if strings.Contains(query, "INSERT INTO schedules") {
		now := time.Now().UTC()
		return &backupProfileConnectionRows{rows: [][]driver.Value{{"schedule-1", now, now}}, columns: 3}, nil
	}
	if strings.Contains(query, "SELECT EXISTS") && strings.Contains(query, "connection_profiles") {
		if c.state.ownershipErr != nil {
			return nil, c.state.ownershipErr
		}
		_, ok := c.state.profiles[args[0].Value.(string)]
		return &backupProfileConnectionRows{rows: [][]driver.Value{{ok}}, columns: 1}, nil
	}
	if strings.Contains(query, "FROM connection_profiles WHERE id") {
		return &backupProfileConnectionRows{rows: [][]driver.Value{c.state.profiles[args[0].Value.(string)]}, columns: 14}, nil
	}
	return nil, driver.ErrSkip
}

type backupProfileConnectionTx struct{ state *backupProfileConnectionState }

func (tx backupProfileConnectionTx) Commit() error   { return nil }
func (tx backupProfileConnectionTx) Rollback() error { return nil }
func (tx backupProfileConnectionTx) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "INSERT INTO backup_jobs") {
		tx.state.backupInsertArgs = make([]driver.Value, len(args))
		for i, arg := range args {
			tx.state.backupInsertArgs[i] = arg.Value
		}
		return &backupProfileConnectionRows{rows: [][]driver.Value{{"backup-1"}}, columns: 1}, nil
	}
	if strings.Contains(query, "INSERT INTO sync_jobs") {
		tx.state.syncInsertArgs = make([]driver.Value, len(args))
		for i, arg := range args {
			tx.state.syncInsertArgs[i] = arg.Value
		}
		now := time.Now().UTC()
		return &backupProfileConnectionRows{rows: [][]driver.Value{{"sync-1", now, now}}, columns: 3}, nil
	}
	if strings.Contains(query, "INSERT INTO schedules") {
		now := time.Now().UTC()
		return &backupProfileConnectionRows{rows: [][]driver.Value{{"schedule-1", now, now}}, columns: 3}, nil
	}
	return nil, driver.ErrSkip
}
func (tx backupProfileConnectionTx) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}

type backupProfileConnectionRows struct {
	rows    [][]driver.Value
	pos     int
	columns int
}

func (r *backupProfileConnectionRows) Columns() []string { return make([]string, r.columns) }
func (r *backupProfileConnectionRows) Close() error      { return nil }
func (r *backupProfileConnectionRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.pos])
	r.pos++
	return nil
}
