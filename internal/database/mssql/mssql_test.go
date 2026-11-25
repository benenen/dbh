package mssql

import (
	"context"
	"testing"
)

func TestSyntaxAndSplit(t *testing.T) {
	d := Driver{}
	if !d.Syntax().BracketIdentifiers {
		t.Fatal("expected BracketIdentifiers to be true")
	}
	schema, name := splitTable("[dbo].[user_orders]")
	if schema != "dbo" || name != "user_orders" {
		t.Fatalf("splitTable = (%q, %q)", schema, name)
	}
	schema, name = splitTable("users")
	if schema != "" || name != "users" {
		t.Fatalf("splitTable = (%q, %q)", schema, name)
	}
}

func TestOpenInvalidDSN(t *testing.T) {
	if _, err := (Driver{}).Open(context.Background(), "://bad", nil); err == nil {
		t.Fatal("expected error for invalid DSN")
	}
}

func TestParseConfigDatabaseOverride(t *testing.T) {
	cfg, err := parseConfig("sqlserver://sa:pass@localhost:1433?database=master", "dbh_test")
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.Database != "dbh_test" {
		t.Fatalf("Database = %q, want dbh_test", cfg.Database)
	}
}

func TestParseConfigOverridesDatabase(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
	}{
		{"url", "sqlserver://sa:secret@localhost:1433?database=master"},
		{"ado", "server=localhost;user id=sa;password=secret;database=master"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parseConfig(tc.dsn, "dbh_test")
			if err != nil {
				t.Fatalf("parseConfig: %v", err)
			}
			if cfg.Database != "dbh_test" {
				t.Fatalf("Database = %q, want dbh_test", cfg.Database)
			}
		})
	}
}

func TestSwitchDatabaseRejectsEmptyName(t *testing.T) {
	if _, err := (Driver{}).SwitchDatabase(context.Background(), nil, "sqlserver://sa:secret@localhost:1433", "   ", nil); err == nil {
		t.Fatal("expected error for empty database name")
	}
}
