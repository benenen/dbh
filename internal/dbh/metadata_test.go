package dbh

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func metadataRows(t *testing.T, out *bytes.Buffer) []map[string]any {
	t.Helper()
	var rows []map[string]any
	decoder := json.NewDecoder(out)
	for {
		var row map[string]any
		if err := decoder.Decode(&row); err == io.EOF {
			return rows
		} else if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
}

func TestSQLiteDescribe(t *testing.T) {
	ctx := context.Background()
	s, err := openSession(ctx, Profile{Name: "test", Driver: "sqlite", DSN: ":memory:"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ddl := `CREATE TABLE users(id INTEGER PRIMARY KEY, name TEXT NOT NULL DEFAULT 'guest',
		note TEXT, upper_name TEXT GENERATED ALWAYS AS (upper(name)) VIRTUAL);
		CREATE UNIQUE INDEX users_name ON users(name);
		CREATE INDEX users_expr ON users(lower(name)) WHERE note IS NOT NULL;`
	if err := s.Run(ctx, ddl, "json", io.Discard); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := s.Describe(ctx, "users", "json", &out); err != nil {
		t.Fatal(err)
	}
	rows := metadataRows(t, &out)
	if len(rows) != 6 {
		t.Fatalf("unexpected metadata: %#v", rows)
	}
	if rows[0]["primary_key_position"] != float64(1) || rows[0]["nullable"] != "NO" || rows[0]["type"] != "INTEGER" {
		t.Fatalf("primary key: %#v", rows[0])
	}
	if rows[1]["default"] != "'guest'" || rows[1]["nullable"] != "NO" || rows[1]["comment"] != nil {
		t.Fatalf("default/nullability: %#v", rows[1])
	}
	if rows[2]["nullable"] != "YES" || rows[3]["name"] != "upper_name" {
		t.Fatalf("nullable/generated columns: %#v", rows)
	}
	if rows[4]["is_partial"] != float64(1) || rows[4]["columns"] != "<expression>" || !strings.Contains(rows[4]["definition"].(string), "lower(name)") {
		t.Fatalf("expression index: %#v", rows[4])
	}
	if rows[5]["name"] != "users_name" || rows[5]["is_unique"] != float64(1) {
		t.Fatalf("unique index: %#v", rows[5])
	}
	if err := s.Describe(ctx, "main.users", "table", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Columns:") || !strings.Contains(out.String(), "Indexes:") {
		t.Fatal(out.String())
	}
	for _, table := range []string{"missing", "users' OR 1=1 --"} {
		out.Reset()
		if err := s.Indexes(ctx, table, "json", &out); err == nil || out.Len() != 0 {
			t.Fatalf("missing table %q: %v %s", table, err, &out)
		}
	}
}

func TestSQLiteDescribeSchemasAndCompositeKey(t *testing.T) {
	ctx := context.Background()
	s, err := openSession(ctx, Profile{Name: "test", Driver: "sqlite", DSN: ":memory:"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ddl := `CREATE TABLE items(main_column TEXT);
		CREATE TEMP TABLE items(a TEXT, b INTEGER, PRIMARY KEY(b, a)) WITHOUT ROWID;
		CREATE INDEX temp.items_b ON items(b DESC);
		CREATE VIEW item_view AS SELECT * FROM items;
		ATTACH ':memory:' AS attached;
		CREATE TABLE attached.other(value TEXT);`
	if err := s.Run(ctx, ddl, "json", io.Discard); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := s.Describe(ctx, "items", "json", &out); err != nil {
		t.Fatal(err)
	}
	rows := metadataRows(t, &out)
	if len(rows) != 4 || rows[0]["name"] != "a" || rows[0]["primary_key_position"] != float64(2) || rows[0]["nullable"] != "NO" || rows[1]["primary_key_position"] != float64(1) {
		t.Fatalf("temp/composite key: %#v", rows)
	}
	if rows[2]["columns"] != "b DESC" || rows[3]["is_primary"] != float64(1) || rows[3]["columns"] != "b, a" {
		t.Fatalf("index order: %#v", rows)
	}
	if err := s.Run(ctx, `CREATE TABLE "MyTable"(id INTEGER); CREATE TABLE "a.b"(id INTEGER);`, "json", io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"main.items", "item_view", "attached.other", "mytable", `"MyTable"`, "main.[MYTABLE]", "a.b", `main."a.b"`} {
		out.Reset()
		if err := s.Describe(ctx, table, "csv", &out); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if !strings.Contains(out.String(), "primary_key_position") || strings.Contains(out.String(), "Columns:") {
			t.Fatalf("CSV: %s", &out)
		}
	}
}
