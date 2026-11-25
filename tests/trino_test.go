//go:build e2e

package e2e

import (
	"os"
	"testing"
)

func TestTrinoE2E(t *testing.T) {
	if os.Getenv("DBH_TEST_TRINO_DSN") == "" {
		t.Skip("DBH_TEST_TRINO_DSN is not configured")
	}

	f := newFixture(t)
	f.run("new", "srv", "--driver", "trino", "--dsn-env", "DBH_TEST_TRINO_DSN")
	contains(t, f.run("check", "srv"), "OK")

	t.Cleanup(func() {
		f.run("exec", "srv", "DROP TABLE IF EXISTS memory.default.dbh_e2e_items", "--no-history")
	})
	f.run("exec", "srv", "CREATE TABLE IF NOT EXISTS memory.default.dbh_e2e_items (id BIGINT, name VARCHAR); INSERT INTO memory.default.dbh_e2e_items VALUES (1, 'alpha');", "--no-history")
	got := f.run("exec", "srv", "SELECT id, name FROM memory.default.dbh_e2e_items", "--format", "tsv")
	contains(t, got, "1\talpha")
	contains(t, f.run("tables", "srv"), "dbh_e2e_items")
	contains(t, f.run("describe", "srv", "dbh_e2e_items"), "name")
}
