//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestMySQLExecutableComments(t *testing.T) {
	if os.Getenv("DBH_TEST_MYSQL_DSN") == "" {
		t.Skip("set DBH_TEST_MYSQL_DSN to run")
	}
	f := newFixture(t)
	f.run("new", "mysql", "--driver", "mysql", "--dsn-env", "DBH_TEST_MYSQL_DSN")
	query := func(sql string) string {
		return f.run("exec", "mysql", "--sql", sql, "--format", "json", "--no-history")
	}
	equal(t, query("/*! SELECT 314159 AS marker */;"), "{\"marker\":314159}\n")
	equal(t, query("/*! SELECT 314159 AS marker */"), "{\"marker\":314159}\n")
	equal(t, query("/* header */ /*!50000 SELECT 'a;b' AS value */; SELECT 2 AS value;"), "{\"value\":\"a;b\"}\n{\"value\":2}\n")
	table := fmt.Sprintf("dbh_comment_%d", time.Now().UnixNano())
	t.Cleanup(func() { query("DROP TABLE IF EXISTS " + table) })
	query("/*! CREATE TABLE " + table + " (id INT) */; /*! INSERT INTO " + table + " VALUES (1) */;")
	equal(t, query("SELECT COUNT(*) AS row_count FROM "+table), "{\"row_count\":1}\n")
}

func TestClickHouseNestedCommentWrites(t *testing.T) {
	if os.Getenv("DBH_TEST_CLICKHOUSE_DSN") == "" {
		t.Skip("set DBH_TEST_CLICKHOUSE_DSN to run")
	}
	f := newFixture(t)
	f.run("new", "clickhouse", "--driver", "clickhouse", "--dsn-env", "DBH_TEST_CLICKHOUSE_DSN")
	query := func(sql string) string {
		return f.run("exec", "clickhouse", "--sql", sql, "--format", "json", "--no-history")
	}
	table := fmt.Sprintf("dbh_comment_%d", time.Now().UnixNano())
	t.Cleanup(func() { query("DROP TABLE IF EXISTS " + table) })
	query("/* outer /* inner */ outer */ CREATE TABLE " + table + " (id UInt64) ENGINE=Memory;")
	query("/* outer /* inner /* deeper */ inner */ outer */ INSERT INTO " + table + " VALUES (1);")
	equal(t, query("/* outer /* inner */ outer */ SELECT count() AS row_count FROM "+table), "{\"row_count\":1}\n")
}
