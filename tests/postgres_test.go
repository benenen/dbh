//go:build e2e

package e2e

import (
	"os"
	"testing"
)

func TestPostgresE2E(t *testing.T) {
	if os.Getenv("DBH_TEST_POSTGRES_DSN") == "" {
		t.Skip("DBH_TEST_POSTGRES_DSN is not configured")
	}

	f := newFixture(t)
	f.run("new", "pg", "--driver", "postgres", "--dsn-env", "DBH_TEST_POSTGRES_DSN")
	contains(t, f.run("check", "pg"), "OK")

	t.Cleanup(func() {
		f.run("exec", "pg", "DROP TABLE IF EXISTS dbh_e2e_pg_items;", "--no-history")
	})
	f.run("exec", "pg", "DROP TABLE IF EXISTS dbh_e2e_pg_items; CREATE TABLE dbh_e2e_pg_items (id SERIAL PRIMARY KEY, name TEXT NOT NULL, raw BYTEA); INSERT INTO dbh_e2e_pg_items (name, raw) VALUES ('alpha', E'\\\\x89504e47');", "--no-history")

	contains(t, f.run("exec", "pg", "SELECT id, name, raw FROM dbh_e2e_pg_items;", "--format", "tsv"), "1\talpha\t0x89504e47")
	contains(t, f.run("tables", "pg"), "dbh_e2e_pg_items")
	contains(t, f.run("describe", "pg", "dbh_e2e_pg_items"), "raw")
	contains(t, f.run("indexes", "pg", "dbh_e2e_pg_items"), "dbh_e2e_pg_items_pkey")
	contains(t, f.run("explain", "pg", "SELECT * FROM dbh_e2e_pg_items WHERE id = 1", "--format", "tsv"), "QUERY PLAN")
	contains(t, f.run("export", "pg", "--table", "dbh_e2e_pg_items", "--format", "csv"), "1,alpha")
}
