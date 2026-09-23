package db

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// TestCanonicalSchemaColumnsAppearInInitDB prevents the checked-in bootstrap
// schema from gaining a column that startup migrations never create. It scopes
// every comparison to the table's CREATE TABLE and ALTER TABLE statements so a
// generic identifier in an unrelated table cannot satisfy the check.
func TestCanonicalSchemaColumnsAppearInInitDB(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate schema sync test")
	}
	initSource, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "db.go"))
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "../../../db/schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	initDBSource := string(initSource)
	canonicalSchema := string(canonical)

	tableRE := regexp.MustCompile(`(?is)CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+([a-z_]+)\s*\((.*?)\);`)
	columnRE := regexp.MustCompile(`(?im)^\s*([a-z_]+)\s+(?:UUID|TEXT|VARCHAR|BOOLEAN|INT|INTEGER|BIGINT|BYTEA|JSONB|TIMESTAMP|SMALLINT)\b`)
	for _, table := range tableRE.FindAllStringSubmatch(canonicalSchema, -1) {
		tableName, definition := table[1], table[2]
		tableDDL := initDBDDLForTable(initDBSource, tableName)
		if tableDDL == "" {
			t.Errorf("canonical table %q is absent from InitDB", tableName)
			continue
		}
		for _, column := range columnRE.FindAllStringSubmatch(definition, -1) {
			if !strings.Contains(tableDDL, column[1]) {
				t.Errorf("canonical column %s.%s is absent from InitDB", tableName, column[1])
			}
		}
	}
}

// TestInitDBTaskCreateIncludesHashColumns protects fresh databases: ALTER TABLE
// migrations repair existing installations, but an omitted column here breaks
// indexing before any runtime task query can succeed.
func TestInitDBTaskCreateIncludesHashColumns(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate schema sync test")
	}
	initSource, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "db.go"))
	if err != nil {
		t.Fatal(err)
	}

	createRE := regexp.MustCompile("(?is)CREATE\\s+TABLE\\s+IF\\s+NOT\\s+EXISTS\\s+tasks\\s*\\((.*?)\\)`")
	matches := createRE.FindStringSubmatch(string(initSource))
	if len(matches) != 2 {
		t.Fatal("tasks CREATE TABLE statement is absent from InitDB")
	}
	for _, column := range []string{"source_hash", "target_hash"} {
		if !regexp.MustCompile(`(?i)\b` + column + `\s+TEXT\b`).MatchString(matches[1]) {
			t.Errorf("fresh InitDB tasks table is missing %s", column)
		}
	}
}

func initDBDDLForTable(source, tableName string) string {
	name := regexp.QuoteMeta(tableName)
	createRE := regexp.MustCompile(`(?is)CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+` + name + `\s*\((.*?)\);`)
	alterRE := regexp.MustCompile(`(?is)ALTER\s+TABLE\s+` + name + `\b.*?(?:` + "`" + `|$)`)
	return strings.Join(append(createRE.FindAllString(source, -1), alterRE.FindAllString(source, -1)...), "\n")
}

// TestCanonicalSchemaTaskIndexesFollowColumnDeclarations ensures indexes in schema.sql
// do not reference columns before they are defined in either CREATE TABLE or ALTER TABLE.
func TestCanonicalSchemaTaskIndexesFollowColumnDeclarations(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate schema sync test")
	}
	canonical, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "../../../db/schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	canonicalSchema := string(canonical)

	syncJobColPos := strings.Index(canonicalSchema, "ADD COLUMN IF NOT EXISTS sync_job_id")
	if syncJobColPos == -1 {
		t.Fatal("schema.sql missing sync_job_id column addition on tasks")
	}
	syncVerifyingIdxPos := strings.Index(canonicalSchema, "idx_tasks_sync_verifying")
	if syncVerifyingIdxPos == -1 {
		t.Fatal("schema.sql missing idx_tasks_sync_verifying index")
	}
	if syncVerifyingIdxPos < syncJobColPos {
		t.Errorf("idx_tasks_sync_verifying index (offset %d) appears before sync_job_id column addition (offset %d)",
			syncVerifyingIdxPos, syncJobColPos)
	}
}
