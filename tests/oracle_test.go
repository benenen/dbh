//go:build e2e

package e2e

import (
	"os"
	"testing"
)

func TestOracleE2E(t *testing.T) {
	if os.Getenv("DBH_TEST_ORACLE_DSN") == "" {
		t.Skip("DBH_TEST_ORACLE_DSN is not configured")
	}

	f := newFixture(t)
	f.run("new", "srv", "--driver", "oracle", "--dsn-env", "DBH_TEST_ORACLE_DSN")
	contains(t, f.run("check", "srv"), "OK")

	t.Cleanup(func() {
		f.run("exec", "srv", "DROP TABLE IF EXISTS dbh_e2e_items;", "--no-history")
	})
	f.run("exec", "srv", "CREATE TABLE dbh_e2e_items (id NUMBER PRIMARY KEY, name VARCHAR2(50)); INSERT INTO dbh_e2e_items VALUES (1, 'alpha'); COMMIT;", "--no-history")
	got := f.run("exec", "srv", "SELECT id, name FROM dbh_e2e_items", "--format", "tsv")
	contains(t, got, "1\talpha")
	contains(t, f.run("tables", "srv"), "DBH_E2E_ITEMS")
	contains(t, f.run("describe", "srv", "DBH_E2E_ITEMS"), "NAME")
}
