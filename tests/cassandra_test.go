//go:build e2e

package e2e

import (
	"os"
	"testing"
)

func TestCassandraE2E(t *testing.T) {
	if os.Getenv("DBH_TEST_CASSANDRA_DSN") == "" {
		t.Skip("DBH_TEST_CASSANDRA_DSN is not configured")
	}

	f := newFixture(t)
	f.run("new", "srv", "--driver", "cassandra", "--dsn-env", "DBH_TEST_CASSANDRA_DSN")
	contains(t, f.run("check", "srv"), "OK")

	t.Cleanup(func() {
		f.run("exec", "srv", "DROP KEYSPACE IF EXISTS dbh_e2e_ks", "--no-history")
	})
	f.run("exec", "srv", "CREATE KEYSPACE IF NOT EXISTS dbh_e2e_ks WITH replication = {'class':'SimpleStrategy','replication_factor':1}", "--no-history")
	f.run("exec", "srv", "--db", "dbh_e2e_ks", "CREATE TABLE items (id int PRIMARY KEY, name text)", "--no-history")
	f.run("exec", "srv", "--db", "dbh_e2e_ks", "INSERT INTO items (id, name) VALUES (1, 'alpha')", "--no-history")
	got := f.run("exec", "srv", "--db", "dbh_e2e_ks", "SELECT id, name FROM items", "--format", "tsv")
	contains(t, got, "1\talpha")
	contains(t, f.run("tables", "srv", "--db", "dbh_e2e_ks"), "items")
	contains(t, f.run("describe", "srv", "--db", "dbh_e2e_ks", "items"), "name")
}
