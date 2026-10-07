//go:build e2e

package e2e

import (
	"os"
	"strings"
	"testing"
)

func TestExec(t *testing.T) {
	f := newFixture(t)
	if got := f.run("exec", "demo", "SELECT 42 AS answer", "--format", "json"); got != "{\"answer\":42}\n" {
		t.Fatalf("unexpected result: %q", got)
	}
	f.run("exec", "demo", "--sql", "SELECT 1", "--no-history")
	f.invoke("SELECT 2", true, "exec", "demo", "--format", "csv")
	path := f.root + "/query.sql"
	writeFile(t, path, "SELECT 3;")
	f.run("exec", "demo", "--file", path)
	for _, args := range [][]string{
		{"exec"}, {"exec", "demo"}, {"exec", "missing", "SELECT 1"},
		{"exec", "demo", "SELECT 1", "--sql", "SELECT 2"},
		{"exec", "demo", "SELECT 1", "--db", ""},
		{"exec", "demo", "SELECT 1", "--db", "other"},
		{"exec", "demo", "invalid SQL"},
	} {
		f.fail(args...)
	}
}

func TestExecDatabaseSelection(t *testing.T) {
	for _, driver := range []string{"mysql", "postgres", "mongo"} {
		t.Run(driver, func(t *testing.T) {
			env := "DBH_TEST_" + strings.ToUpper(driver) + "_DSN"
			if os.Getenv(env) == "" {
				t.Skip(env + " is not configured")
			}
			f := newFixture(t)
			f.run("new", "server", "--driver", driver, "--dsn-env", env)
			query := "SELECT DATABASE() AS selected_db"
			want := "dbh_test"
			if driver == "postgres" {
				query = "SELECT current_database() AS selected_db"
			}
			if driver == "mongo" {
				query = "{\"dbStats\":1}"
			}
			if driver != "mongo" {
				if alt := os.Getenv("DBH_TEST_" + strings.ToUpper(driver) + "_ALT_DATABASE"); alt != "" {
					want = alt
				}
			}
			before := readFile(t, f.config+"/connections.json")
			got := f.run("exec", "server", "--db", want, query, "--format", "json")
			if !strings.Contains(got, "\""+want+"\"") {
				t.Fatalf("wrong selected database: %s", got)
			}
			if after := readFile(t, f.config+"/connections.json"); before != after {
				t.Fatal("exec changed saved connection")
			}
			if driver != "mongo" {
				f.fail("exec", "server", "--db", "dbh_missing_database_e2e", query)
			}
		})
	}
}
