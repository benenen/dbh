package introspect

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/benenen/dbh/internal/dbml"
	_ "modernc.org/sqlite"
)

func TestSQLStringLiteral(t *testing.T) {
	for def, want := range map[string]string{
		"'new'::erd_live.status":     "new",
		"'guest'::character varying": "guest",
		"'it''s'::text":              "it's",
		"plain":                      "plain",
		`"quoted"`:                   "quoted",
		"'a::b'":                     "a::b",
	} {
		if got := sqlStringLiteral(def); got != want {
			t.Errorf("sqlStringLiteral(%q) = %q, want %q", def, got, want)
		}
	}
	if got := dbmlString(`it's \ ok`); got != `it\'s \\ ok` {
		t.Errorf("dbmlString = %q", got)
	}
}

func TestGenerateDBMLRoundTrip(t *testing.T) {
	defVal := "'open'::text"
	schema := &DBSchema{
		Tables: []*Table{
			{
				Name:    "users",
				Comment: "table 'users'\nline 2",
				Columns: []*Column{
					{Name: "id", Type: "INTEGER", IsPrimaryKey: true, IsIncrement: true},
					{Name: "status", Type: "varchar(20)", Default: &defVal, DefaultType: "string", Comment: "user's status"},
				},
			},
			{
				Name: "orders",
				Columns: []*Column{
					{Name: "tenant_id", Type: "INTEGER"},
					{Name: "code", Type: "TEXT"},
				},
				Constraints: []*Constraint{
					{Name: "pk_orders", Columns: []string{"tenant_id", "code"}, IsPrimary: true},
				},
				Indexes: []*Index{
					{Name: "idx_lower_code", Columns: []string{"lower(code)"}, Exprs: []bool{true}, Type: "btree"},
				},
			},
		},
		FKs: []*ForeignKey{
			{TableName: "orders", Columns: []string{"tenant_id"}, RefTable: "users", RefColumns: []string{"id"}, OnDelete: "CASCADE"},
		},
	}

	out := GenerateDBML(schema, "SQLite", false)
	parsed, errs := dbml.Parse(out, dbml.ParseOptions{})
	if len(errs) > 0 {
		t.Fatalf("GenerateDBML failed to re-parse: %v\n%s", errs, out)
	}
	if len(parsed.Tables) != 2 || len(parsed.Refs) != 1 {
		t.Fatalf("parsed = %+v", parsed)
	}
	if parsed.Tables[0].Note == nil || !strings.Contains(parsed.Tables[0].Note.Value, "line 2") {
		t.Fatalf("table note = %+v", parsed.Tables[0].Note)
	}
}

func TestIntrospectSQLite(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT UNIQUE NOT NULL);
		CREATE TABLE orders (
			id INTEGER PRIMARY KEY,
			user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			code TEXT
		);
		CREATE INDEX idx_orders_lower_code ON orders (lower(code));
	`)
	if err != nil {
		t.Fatal(err)
	}

	s, err := Introspect(context.Background(), db, EngineSQLite, "")
	if err != nil {
		t.Fatal(err)
	}
	out := GenerateDBML(s, "SQLite", false)
	parsed, errs := dbml.Parse(out, dbml.ParseOptions{})
	if len(errs) > 0 {
		t.Fatalf("re-parse errors: %v\n%s", errs, out)
	}
	if len(parsed.Tables) != 2 || len(parsed.Refs) != 1 {
		t.Fatalf("unexpected parsed schema: tables=%d refs=%d", len(parsed.Tables), len(parsed.Refs))
	}
}

func TestMSSQLAndOracleHelpers(t *testing.T) {
	if got := mssqlColumnType("nvarchar", sql.NullInt64{Int64: -1, Valid: true}, sql.NullInt64{}, sql.NullInt64{}); got != "nvarchar(max)" {
		t.Fatalf("nvarchar(max) = %q", got)
	}
	if got := mssqlColumnType("decimal", sql.NullInt64{}, sql.NullInt64{Int64: 10, Valid: true}, sql.NullInt64{Int64: 2, Valid: true}); got != "decimal(10,2)" {
		t.Fatalf("decimal = %q", got)
	}
	if v, typ := inferMSSQLDefault("(('open'))"); v != "'open'" || typ != "string" {
		t.Fatalf("inferMSSQLDefault string = (%q, %q)", v, typ)
	}
	if v, typ := inferMSSQLDefault("((0))"); v != "0" || typ != "number" {
		t.Fatalf("inferMSSQLDefault number = (%q, %q)", v, typ)
	}
	if got := oracleColumnType("VARCHAR2", sql.NullInt64{Int64: 100, Valid: true}, sql.NullInt64{Int64: 50, Valid: true}, sql.NullInt64{}, sql.NullInt64{}); got != "varchar2(50)" {
		t.Fatalf("oracleColumnType = %q", got)
	}
	if got := oracleColumnType("NUMBER", sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{Int64: 12, Valid: true}, sql.NullInt64{Int64: 4, Valid: true}); got != "number(12,4)" {
		t.Fatalf("oracleColumnType number = %q", got)
	}
	if !strings.Contains(oracleSQL("WHERE OWNER = :1 AND NAME = :2", true), "WHERE OWNER = ? AND NAME = ?") {
		t.Fatal("oracleSQL DM placeholder replacement failed")
	}

	schema := &DBSchema{
		Tables: []*Table{
			{
				Schema: "dbo",
				Name:   "orders",
				Columns: []*Column{
					{Name: "id", Type: "bigint", IsPrimaryKey: true, IsIncrement: true},
					{Name: "user_id", Type: "bigint"},
				},
			},
			{
				Schema: "dbo",
				Name:   "users",
				Columns: []*Column{
					{Name: "id", Type: "bigint", IsPrimaryKey: true},
				},
			},
		},
		FKs: []*ForeignKey{
			{Schema: "dbo", TableName: "orders", Columns: []string{"user_id"}, RefSchema: "dbo", RefTable: "users", RefColumns: []string{"id"}, OnDelete: "CASCADE"},
		},
	}
	out := GenerateDBML(schema, "SQL Server", false)
	parsed, errs := dbml.Parse(out, dbml.ParseOptions{})
	if len(errs) > 0 {
		t.Fatalf("re-parse SQL Server DBML failed: %v\n%s", errs, out)
	}
	if len(parsed.Tables) != 2 || len(parsed.Refs) != 1 {
		t.Fatalf("parsed = %+v", parsed)
	}
}
