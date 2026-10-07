//go:build e2e && (linux || darwin)

package e2e

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestTerminalCommentStatements(t *testing.T) {
	for _, driver := range []string{"mysql", "clickhouse"} {
		t.Run(driver, func(t *testing.T) {
			env := "DBH_TEST_MYSQL_DSN"
			if driver == "clickhouse" {
				env = "DBH_TEST_CLICKHOUSE_DSN"
			}
			if os.Getenv(env) == "" {
				t.Skip("set " + env + " to run")
			}
			f := newFixture(t)
			f.run("new", driver, "--driver", driver, "--dsn-env", env)
			term := f.openTerminal(driver, "--no-history")
			if driver == "mysql" {
				term.exchange("/*! SELECT 314159 AS marker */;\r", `"marker":314159`)
			} else {
				table := fmt.Sprintf("dbh_comment_%d", time.Now().UnixNano())
				t.Cleanup(func() { f.run("exec", driver, "DROP TABLE IF EXISTS "+table, "--no-history") })
				term.exchange("\\format table\r", driver+"> ")
				term.exchange("/* outer /* inner */ outer */ CREATE TABLE "+table+" (id UInt64) ENGINE=Memory;\r", "OK")
				term.exchange("/* outer /* inner */ outer */ INSERT INTO "+table+" VALUES (1);\r", "OK")
				term.exchange("SELECT count() FROM "+table+";\r", "(1 rows)")
			}
			term.exit("\\q\r")
		})
	}
}
