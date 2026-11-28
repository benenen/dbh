package dbh

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopyCommandStreaming(t *testing.T) {
	dir := t.TempDir()
	srcDB := filepath.Join(dir, "src.db")
	dstDB := filepath.Join(dir, "dst.db")

	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		cmd := NewCommand()
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append([]string{"--config-dir", dir}, args...))
		err := cmd.Execute()
		return out.String(), err
	}

	if _, err := run("exec", srcDB, "CREATE TABLE users (id INT, name TEXT); INSERT INTO users VALUES (1,'a'),(2,'b'),(3,'c'),(4,'d'),(5,'e');"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("exec", dstDB, "CREATE TABLE users (id INT, name TEXT); CREATE TABLE users_sub (id INT, name TEXT);"); err != nil {
		t.Fatal(err)
	}

	out, err := run("copy", srcDB, dstDB, "--table", "users", "--batch-size", "2")
	if err != nil {
		t.Fatalf("copy failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Copied 5 rows") {
		t.Fatalf("unexpected copy output: %q", out)
	}

	got, err := run("exec", dstDB, "SELECT COUNT(*) AS cnt FROM users;", "--format", "tsv")
	if err != nil || got != "cnt\n5\n" {
		t.Fatalf("dst count = %q, err = %v", got, err)
	}

	// Copy with --sql, --target-table, and --max-rows.
	if _, err := run("copy", srcDB, dstDB, "--sql", "SELECT id, name FROM users ORDER BY id", "--target-table", "users_sub", "--max-rows", "2"); err != nil {
		t.Fatal(err)
	}
	got, err = run("exec", dstDB, "SELECT COUNT(*) AS cnt FROM users_sub;", "--format", "tsv")
	if err != nil || got != "cnt\n2\n" {
		t.Fatalf("dst sub count = %q, err = %v", got, err)
	}

	// Read-only destination blocks copy without --allow-write.
	if _, err := run("new", "ro_dst", "--driver", "sqlite", "--dsn", dstDB, "--read-only"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("copy", srcDB, "ro_dst", "--table", "users"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("expected read-only error, got %v", err)
	}
}
