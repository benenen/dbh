//go:build e2e

package e2e

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestTerminalExpandedTruncateSnippetAndBadges(t *testing.T) {
	f := newFixture(t)
	prodDB := filepath.Join(f.root, "prod.db")
	f.run("new", "prod", "--driver", "sqlite", "--dsn", prodDB, "--read-only", "--env", "prod")
	f.run("exec", "prod", "--allow-write", "CREATE TABLE items (id INTEGER PRIMARY KEY, note TEXT); INSERT INTO items VALUES (1, '"+strings.Repeat("a", 50)+"');")
	f.run("snippet", "set", "all_items", "SELECT id, note FROM items;")

	term := f.openTerminal("prod", "--no-history")
	// Prompt shows [RO][PROD] badges.
	term.exchange("\r", "[RO][PROD]> ")

	// Read-only blocks writes in shell too.
	out := term.exchange("DROP TABLE items;\r", "read-only")
	contains(t, out, "read-only")

	// \x toggles expanded vertical output and \truncate adjusts cell width.
	term.exchange("\\format table\r", "[RO][PROD]> ")
	term.exchange("\\x\r", "Expanded display is on.")
	term.exchange("\\truncate 20\r", "[RO][PROD]> ")
	contains(t, term.exchange("\\truncate\r", "Truncate: 20"), "Truncate: 20")
	runOut := term.exchange("\\run all_items\r", "(1 rows)")
	contains(t, runOut, "Row 1:")
	contains(t, runOut, "... (50 chars)")

	term.exchange("\\truncate off\r", "[RO][PROD]> ")
	contains(t, term.exchange("\\truncate\r", "Truncate: off"), "Truncate: off")
	term.exchange("\\x\r", "Expanded display is off.")

	// --env prod (without --read-only) still blocks dangerous writes without WHERE.
	prodRWDB := filepath.Join(f.root, "prod_rw.db")
	f.run("new", "prod_rw", "--driver", "sqlite", "--dsn", prodRWDB, "--env", "prod")
	f.run("exec", "prod_rw", "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT); INSERT INTO items VALUES (1, 'a'), (2, 'b');")
	term2 := f.openTerminal("prod_rw", "--no-history")
	term2.exchange("\r", "[PROD]> ")
	contains(t, term2.exchange("DELETE FROM items;\r", "without WHERE"), "without WHERE")
	term2.exchange("DELETE FROM items WHERE id = 1;\r", "[PROD]> ")

	// \ddl prints CREATE TABLE DDL in interactive shell.
	ddlOut := term2.exchange("\\ddl items\r", "[PROD]> ")
	contains(t, ddlOut, "CREATE TABLE items")

	// \snippet save and \snippet run.
	term2.exchange("SELECT id FROM items WHERE id = 2;\r", "[PROD]> ")
	contains(t, term2.exchange("\\snippet save get_one\r", "Saved snippet get_one"), "Saved snippet get_one")
	contains(t, term2.exchange("\\snippet run get_one\r", `{"id":2}`), `{"id":2}`)
}
