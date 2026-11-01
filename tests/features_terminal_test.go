//go:build e2e && (linux || darwin)

package e2e

import "testing"

func TestTerminalContextCompletionAndQuotedMetadata(t *testing.T) {
	f := newFixture(t)
	f.sql(`CREATE TABLE "order items" (id INTEGER, special_column TEXT); CREATE INDEX "order lookup" ON "order items"(id); CREATE TABLE users(user_name TEXT); INSERT INTO "order items" VALUES (7,'batch');`, "--no-history")
	term := f.terminal("--no-history")
	contains(t, term.transcript, "demo/main>")
	// Type characters and inspect candidates before selecting anything with Tab.
	output := term.exchange("SELECT * FROM ", "users")
	excludes(t, output, "SELECT\r\n")
	term.exchange("\x03", "demo/main> ")
	term.exchange("SELECT * FROM users u JOIN ", "users")
	term.exchange("\x03", "demo/main> ")
	term.exchange("SELECT * FROM users u WHERE u.", "u.user_name")
	term.exchange("\x03", "demo/main> ")
	term.exchange("SELECT * FROM ord", `"order items"`)
	term.exchange("\t\r")
	term.exchange(";\r", `"special_column":"batch"`)
	term.exchange("\\describe \"order items\"\r", `"name":"special_column"`)
	term.exchange("\\indexes \"order items\"\r", `"name":"order lookup"`)
	output = term.exchange("INSERT INTO users VALUES ('hello');\r", "1 rows affected")
	contains(t, output, "Time:")
	term.exit("\\q\r")
}

func TestTerminalHistoryRetention(t *testing.T) {
	f := newFixture(t)
	term := f.terminal("--history-limit", "2")
	for _, sql := range []string{"SELECT 1 AS value;", "SELECT 2 AS value;", "SELECT 1 AS value;", "SELECT 3 AS value;"} {
		term.exchange(sql+"\r", "Time:")
	}
	output := term.exchange("\\history\r", "2  SELECT 3 AS value;")
	contains(t, output, "1  SELECT 1 AS value;")
	excludes(t, output, "SELECT 2 AS value;")
	term.exit("\\q\r")
	equal(t, f.history(), []string{"SELECT 1 AS value;", "SELECT 3 AS value;"})
}
