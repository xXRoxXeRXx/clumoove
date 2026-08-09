package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// TestInitDBFreshSchemaSupportsTaskHashRuntimeQueries exercises the startup
// bootstrap in an otherwise empty schema. It guards the task INSERT, read, and
// migration/sync verification queries that require both hash columns.
func TestInitDBFreshSchemaSupportsTaskHashRuntimeQueries(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping InitDB fresh-schema integration test")
	}

	adminDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer adminDB.Close()
	if err := adminDB.Ping(); err != nil {
		t.Fatalf("ping database: %v", err)
	}

	schemaName := fmt.Sprintf("initdb_task_hashes_%d", time.Now().UnixNano())
	if _, err := adminDB.Exec("CREATE SCHEMA " + schemaName); err != nil {
		t.Fatalf("create isolated schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = adminDB.Exec("DROP SCHEMA " + schemaName + " CASCADE")
	})

	initializedDB, err := InitDB(dsnWithSearchPath(t, dsn, schemaName))
	if err != nil {
		t.Fatalf("InitDB() on fresh schema: %v", err)
	}
	defer initializedDB.Close()

	ctx := context.Background()
	var userID, migrationID, syncJobID string
	if err := initializedDB.QueryRow(`INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING id`, schemaName+"@example.test", "hash").Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := initializedDB.QueryRow(`INSERT INTO migrations (user_id, status) VALUES ($1, 'INDEXING') RETURNING id`, userID).Scan(&migrationID); err != nil {
		t.Fatalf("create migration: %v", err)
	}

	migrationTask := &Task{
		MigrationID:  migrationID,
		ResourceType: "files",
		FilePath:     "/indexed.txt",
		FileSize:     10,
		SourceHash:   sql.NullString{String: "SHA256:source", Valid: true},
		Status:       "PENDING",
		Metadata:     json.RawMessage(`{}`),
	}
	if _, err := CreateMigrationTaskWhileIndexing(initializedDB, migrationTask); err != nil {
		t.Fatalf("insert indexed task: %v", err)
	}
	if _, err := GetTask(initializedDB, migrationTask.ID); err != nil {
		t.Fatalf("GetTask(): %v", err)
	}
	if _, err := initializedDB.Exec(`UPDATE tasks SET status = 'COMPLETED', target_hash = 'SHA256:target' WHERE id = $1`, migrationTask.ID); err != nil {
		t.Fatalf("complete migration task: %v", err)
	}
	if tasks, err := GetUnverifiedCompletedTasks(initializedDB, ctx, migrationID); err != nil || len(tasks) != 1 {
		t.Fatalf("migration verification query = %d tasks, %v; want 1, nil", len(tasks), err)
	}

	if err := initializedDB.QueryRow(`
		INSERT INTO sync_jobs (user_id, source_url, source_username, source_password_encrypted, target_url, target_username, target_password_encrypted)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id
	`, userID, "https://source.example.test", "source", "source-secret", "https://target.example.test", "target", "target-secret").Scan(&syncJobID); err != nil {
		t.Fatalf("create sync job: %v", err)
	}
	if err := BulkCreateSyncTasks(ctx, initializedDB, []*Task{{
		SyncJobID:      syncJobID,
		PassGeneration: 1,
		ResourceType:   "files",
		FilePath:       "/sync.txt",
		FileSize:       20,
		SourceHash:     sql.NullString{String: "SHA256:sync-source", Valid: true},
		Status:         "COMPLETED",
		Metadata:       json.RawMessage(`{}`),
	}}); err != nil {
		t.Fatalf("insert sync task: %v", err)
	}
	if tasks, err := GetUnverifiedCompletedSyncTasks(initializedDB, ctx, syncJobID, 1); err != nil || len(tasks) != 1 {
		t.Fatalf("sync verification query = %d tasks, %v; want 1, nil", len(tasks), err)
	}
}

func dsnWithSearchPath(t *testing.T, dsn, schemaName string) string {
	t.Helper()
	if strings.Contains(dsn, "://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatalf("parse DATABASE_URL: %v", err)
		}
		query := parsed.Query()
		query.Set("search_path", schemaName)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return dsn + " search_path=" + schemaName
}
