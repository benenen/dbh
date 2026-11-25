//go:build e2e

package e2e

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLiteE2E(t *testing.T) {
	f := newFixture(t)
	f.sql("CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT NOT NULL, payload BLOB, vec TEXT);", "--no-history")
	f.sql("CREATE INDEX idx_items_name ON items (name);", "--no-history")
	f.sql("INSERT INTO items VALUES (1, 'alpha', X'89504E47', '[0.1,0.2,0.3,0.4,0.5,0.6,0.7,0.8,0.9,1.0]');", "--no-history")

	contains(t, f.run("check", "demo"), "OK")
	contains(t, f.run("tables", "demo"), "items")
	contains(t, f.run("describe", "demo", "items"), "payload")
	contains(t, f.run("indexes", "demo", "items"), "idx_items_name")
	contains(t, f.run("exec", "demo", "SELECT id, name, payload FROM items WHERE id = ?;", "--param", "1", "--format", "tsv"), "1\talpha\t0x89504e47")
	contains(t, f.run("explain", "demo", "SELECT * FROM items WHERE name = 'alpha'", "--format", "tsv"), "idx_items_name")

	csvPath := filepath.Join(f.root, "items.csv")
	f.run("export", "demo", "--table", "items", "--format", "csv", "-o", csvPath)
	contains(t, readFile(t, csvPath), "1,alpha")

	// Read-only & prod safety check
	roDB := filepath.Join(f.root, "ro.db")
	f.run("new", "ro", "--driver", "sqlite", "--dsn", roDB, "--read-only", "--env", "prod")
	contains(t, f.fail("exec", "ro", "CREATE TABLE x (id INT);"), "read-only")
	f.run("exec", "ro", "--allow-write", "CREATE TABLE x (id INT);")
	if !strings.Contains(f.run("ls"), "ro") {
		t.Fatal("expected ro in dbh ls")
	}
}
