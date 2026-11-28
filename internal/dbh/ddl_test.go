package dbh

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSQLiteDDLExport(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ddl.db")

	setup := NewCommand()
	setup.SetArgs([]string{
		"--config-dir", dir,
		"exec", dbPath,
		`CREATE TABLE "Order Items" (
			id INTEGER PRIMARY KEY,
			sku TEXT NOT NULL DEFAULT 'unknown',
			qty INTEGER CHECK (qty > 0)
		);
		CREATE INDEX idx_order_items_sku ON "Order Items" (sku);
		CREATE VIEW v_items AS SELECT id, sku FROM "Order Items";`,
	})
	if err := setup.Execute(); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	trigSess, err := openSession(context.Background(), Profile{Name: "trig", Driver: "sqlite", DSN: dbPath}, 5*time.Second)
	if err != nil {
		t.Fatalf("open sqlite session for trigger: %v", err)
	}
	if err := trigSess.Conn.Exec(context.Background(), `CREATE TRIGGER trg_order_items_ins AFTER INSERT ON "Order Items" BEGIN SELECT 1; END;`); err != nil {
		trigSess.Close()
		t.Fatalf("create trigger: %v", err)
	}
	trigSess.Close()

	// Raw DDL via file path.
	var out bytes.Buffer
	cmd := NewCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--config-dir", dir, "ddl", dbPath, "Order Items"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("ddl failed: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		`CREATE TABLE "Order Items"`,
		`CREATE INDEX idx_order_items_sku`,
		`CREATE TRIGGER trg_order_items_ins`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("ddl output missing %q:\n%s", want, got)
		}
	}

	// View DDL via --dsn and --format tsv + -o output file.
	outFile := filepath.Join(dir, "view.sql")
	out.Reset()
	cmd = NewCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--config-dir", dir, "ddl", "--dsn", dbPath, "v_items", "-o", outFile})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("ddl view failed: %v", err)
	}
	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read -o file: %v", err)
	}
	if !strings.Contains(string(data), "CREATE VIEW v_items") {
		t.Fatalf("unexpected view ddl: %s", string(data))
	}

	// Structured formats: json, tsv, csv, table.
	for _, format := range []string{"json", "tsv", "csv", "table"} {
		out.Reset()
		cmd = NewCommand()
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"--config-dir", dir, "ddl", "--dsn", dbPath, "v_items", "--format", format})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("ddl --format %s failed: %v", format, err)
		}
		if !strings.Contains(out.String(), "CREATE VIEW v_items") {
			t.Fatalf("ddl --format %s missing view DDL: %s", format, out.String())
		}
	}

	// Nonexistent table returns error.
	cmd = NewCommand()
	cmd.SetArgs([]string{"--config-dir", dir, "ddl", "--dsn", dbPath, "no_such_table"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error for missing table")
	}

	// Unsupported driver returns clear error.
	session, err := openSession(context.Background(), Profile{
		Name:   "kv",
		Driver: "bbolt",
		DSN:    "bbolt://" + filepath.Join(dir, "kv.bbolt"),
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("open bbolt session: %v", err)
	}
	defer session.Close()
	if err := session.DDL(context.Background(), "bucket1", "raw", &out); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("expected unsupported error for bbolt, got %v", err)
	}
}
