//go:build e2e

package e2e

import (
	"os"
	"testing"
)

func TestMSSQLE2E(t *testing.T) {
	if os.Getenv("DBH_TEST_MSSQL_DSN") == "" {
		t.Skip("DBH_TEST_MSSQL_DSN is not configured")
	}

	f := newFixture(t)
	f.run("new", "srv", "--driver", "mssql", "--dsn-env", "DBH_TEST_MSSQL_DSN")
	contains(t, f.run("check", "srv"), "OK")

	dbArgs := []string{}
	if alt := os.Getenv("DBH_TEST_MSSQL_ALT_DATABASE"); alt != "" {
		dbArgs = append(dbArgs, "--db", alt)
	}
	t.Cleanup(func() {
		args := append([]string{"exec", "srv"}, dbArgs...)
		f.run(append(args, "DROP TABLE IF EXISTS dbo.dbh_e2e_items;", "--no-history")...)
	})
	args := append([]string{"exec", "srv"}, dbArgs...)
	f.run(append(args, "DROP TABLE IF EXISTS dbo.dbh_e2e_items; CREATE TABLE dbo.dbh_e2e_items (id INT PRIMARY KEY, name NVARCHAR(50)); INSERT INTO dbo.dbh_e2e_items VALUES (1, N'alpha');", "--no-history")...)
	got := f.run(append(args, "SELECT id, name FROM dbo.dbh_e2e_items;", "--format", "tsv")...)
	contains(t, got, "1\talpha")
	contains(t, f.run(append([]string{"tables", "srv"}, dbArgs...)...), "dbh_e2e_items")
	contains(t, f.run(append([]string{"describe", "srv"}, append(dbArgs, "dbo.dbh_e2e_items")...)...), "name")
	contains(t, f.run(append([]string{"indexes", "srv"}, append(dbArgs, "dbo.dbh_e2e_items")...)...), "id")
}
