package dbh

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestProdWriteAuditLog(t *testing.T) {
	dir := t.TempDir()
	prodDB := filepath.Join(dir, "prod.db")
	devDB := filepath.Join(dir, "dev.db")

	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		cmd := NewCommand()
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append([]string{"--config-dir", dir}, args...))
		err := cmd.Execute()
		return out.String(), err
	}

	if _, err := run("new", "prod", "--driver", "sqlite", "--dsn", prodDB, "--env", "prod"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("new", "dev", "--driver", "sqlite", "--dsn", devDB, "--env", "dev"); err != nil {
		t.Fatal(err)
	}

	// Non-prod writes must NOT be audited.
	if _, err := run("exec", "dev", "CREATE TABLE t (id INT); INSERT INTO t VALUES (1);"); err != nil {
		t.Fatal(err)
	}
	st := Store{Dir: dir}
	entries, err := st.readAudit()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 audit entries for dev, got %d", len(entries))
	}

	// Prod allowed write is audited with rows_affected.
	if _, err := run("exec", "prod", "CREATE TABLE t (id INT); INSERT INTO t VALUES (1), (2);"); err != nil {
		t.Fatal(err)
	}
	// Prod read is NOT audited.
	if _, err := run("exec", "prod", "SELECT COUNT(*) FROM t;"); err != nil {
		t.Fatal(err)
	}
	// Prod blocked dangerous write is audited with success=false.
	if _, err := run("exec", "prod", "DELETE FROM t;"); err == nil {
		t.Fatal("expected blocked DELETE without WHERE on prod")
	}

	entries, err = st.readAudit()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 audit entries, got %d: %+v", len(entries), entries)
	}
	if !entries[1].Success || entries[1].RowsAffected == nil || *entries[1].RowsAffected != 2 {
		t.Fatalf("expected 2 rows affected on INSERT, got %+v", entries[1])
	}
	if entries[2].Success || !strings.Contains(entries[2].Error, "without WHERE") {
		t.Fatalf("expected failed DELETE audit entry, got %+v", entries[2])
	}

	// Inspect via `dbh audit --failed --format json`.
	out, err := run("audit", "--failed", "--format", "json")
	if err != nil {
		t.Fatalf("audit command failed: %v", err)
	}
	if !strings.Contains(out, `"DELETE FROM t"`) || strings.Contains(out, `"INSERT INTO t VALUES (1), (2)"`) {
		t.Fatalf("unexpected audit --failed output: %s", out)
	}
}
