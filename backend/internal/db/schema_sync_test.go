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

func initDBDDLForTable(source, tableName string) string {
	name := regexp.QuoteMeta(tableName)
	createRE := regexp.MustCompile(`(?is)CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+` + name + `\s*\((.*?)\);`)
	alterRE := regexp.MustCompile(`(?is)ALTER\s+TABLE\s+` + name + `\b.*?(?:` + "`" + `|$)`)
	return strings.Join(append(createRE.FindAllString(source, -1), alterRE.FindAllString(source, -1)...), "\n")
}
