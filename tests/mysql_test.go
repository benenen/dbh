//go:build e2e

package e2e

import (
	"os"
	"testing"
)

func TestMySQLE2E(t *testing.T) {
	if os.Getenv("DBH_TEST_MYSQL_DSN") == "" {
		t.Skip("DBH_TEST_MYSQL_DSN is not configured")
	}

	f := newFixture(t)
	f.run("new", "my", "--driver", "mysql", "--dsn-env", "DBH_TEST_MYSQL_DSN")
	contains(t, f.run("check", "my"), "OK")

	t.Cleanup(func() {
		f.run("exec", "my", "DROP TABLE IF EXISTS dbh_e2e_my_items;", "--no-history")
	})
	f.run("exec", "my", "DROP TABLE IF EXISTS dbh_e2e_my_items; CREATE TABLE dbh_e2e_my_items (id INT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(64) NOT NULL); INSERT INTO dbh_e2e_my_items (name) VALUES ('alpha');", "--no-history")

	contains(t, f.run("exec", "my", "SELECT id, name FROM dbh_e2e_my_items;", "--format", "tsv"), "1\talpha")
	contains(t, f.run("tables", "my"), "dbh_e2e_my_items")
	contains(t, f.run("describe", "my", "dbh_e2e_my_items"), "name")
	contains(t, f.run("indexes", "my", "dbh_e2e_my_items"), "PRIMARY")
	contains(t, f.run("explain", "my", "SELECT * FROM dbh_e2e_my_items WHERE id = 1", "--format", "tsv"), "EXPLAIN")
	contains(t, f.run("export", "my", "--table", "dbh_e2e_my_items", "--format", "csv"), "1,alpha")
}
