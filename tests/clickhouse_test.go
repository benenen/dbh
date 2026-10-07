//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func clickhouseFixture(t *testing.T) (*fixture, string) {
	t.Helper()
	if os.Getenv("DBH_TEST_CLICKHOUSE_DSN") == "" {
		t.Skip("set DBH_TEST_CLICKHOUSE_DSN to run")
	}
	f := newFixture(t)
	f.run("new", "clickhouse", "--driver", "clickhouse", "--dsn-env", "DBH_TEST_CLICKHOUSE_DSN")
	table := fmt.Sprintf("dbh_e2e_%d", time.Now().UnixNano())
	f.run("exec", "clickhouse", "/* test table */ CREATE TABLE "+table+" (id UInt64, name String, note Nullable(String), INDEX name_idx name TYPE bloom_filter GRANULARITY 1) ENGINE=MergeTree ORDER BY id", "--no-history")
	t.Cleanup(func() { f.run("exec", "clickhouse", "DROP TABLE IF EXISTS "+table, "--no-history") })
	f.run("exec", "clickhouse", "--sql", "-- insert rows\nINSERT INTO "+table+" VALUES (1,'hello','连接正常'), (2,'second',NULL)", "--no-history")
	return f, table
}
func TestClickHouse(t *testing.T) {
	f, table := clickhouseFixture(t)
	got := f.run("exec", "clickhouse", "SELECT * FROM "+table+" ORDER BY id", "--format", "json")
	contains(t, got, `"name":"hello"`)
	contains(t, got, `"note":null`)
	contains(t, got, "连接正常")
	got = f.run("c", "clickhouse", "--sql", "SELECT * FROM "+table+" ORDER BY id")
	contains(t, got, "┌")
	contains(t, got, "(2 rows)")
	got = f.run("exec", "clickhouse", "SELECT id,name FROM "+table+" ORDER BY id", "--format", "csv")
	equal(t, got, "id,name\n1,hello\n2,second\n")
	f.invoke("SELECT 7 AS value;", true, "exec", "clickhouse", "--format", "json")
	before := readFile(t, f.config+"/connections.json")
	if alt := os.Getenv("DBH_TEST_CLICKHOUSE_ALT_DATABASE"); alt != "" {
		contains(t, f.run("exec", "clickhouse", "--db", alt, "SELECT currentDatabase() AS db", "--format", "json"), alt)
	}
	f.fail("exec", "clickhouse", "--db", "dbh_missing_database_e2e", "SELECT 1")
	f.fail("exec", "clickhouse", "SELECT * FROM dbh_missing_table_e2e")
	equal(t, readFile(t, f.config+"/connections.json"), before)
}
