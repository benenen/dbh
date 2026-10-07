//go:build e2e && (linux || darwin)

package e2e

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestClickHouseTerminal(t *testing.T) {
	f, table := clickhouseFixture(t)
	var current struct{ DB string }
	if err := json.Unmarshal([]byte(f.run("exec", "clickhouse", "SELECT currentDatabase() AS DB", "--format", "json", "--no-history")), &current); err != nil {
		t.Fatal(err)
	}
	term := f.openTerminal("clickhouse", "--no-history")
	term.exchange("\\database\r", "system")
	term.exchange("\\tables\r", table)
	term.exchange("\\describe "+table+"\r", "Nullable(String)")
	term.exchange("\\indexes "+table+"\r", "name_idx")
	term.exchange("SELECT na", "name")
	term.exchange("\x03", "clickhouse> ")
	term.exchange("\\use dbh_missing_database_e2e\r", "cannot switch database")
	term.exchange("SELECT count() AS count FROM "+table+";\r", `"count":2`)
	if alt := os.Getenv("DBH_TEST_CLICKHOUSE_ALT_DATABASE"); alt != "" {
		term.exchange("\\use "+alt+"\r", "Database changed to "+alt)
		term.exchange("SELECT currentDatabase() AS db;\r", alt)
		term.exchange("USE "+"`"+strings.ReplaceAll(current.DB, "`", "``")+"`"+";\r", "clickhouse> ")
		term.exchange("\\tables\r", table)
	}
	term.exit("\\q\r")
}
