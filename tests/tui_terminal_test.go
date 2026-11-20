//go:build e2e && (linux || darwin)

package e2e

import (
	"fmt"
	"strings"
	"testing"

	"github.com/creack/pty"
)

func TestTerminalFuzzyHistoryPickerAndPrefill(t *testing.T) {
	f := newFixture(t)
	f.sql("CREATE TABLE items(id INTEGER PRIMARY KEY, label TEXT); INSERT INTO items VALUES (1, 'alpha'), (2, 'beta');", "--no-history")

	// Seed multi-line and single-line history via normal interactive session.
	term := f.terminal()
	term.exchange("SELECT label\x1b\rFROM items\x1b\rWHERE id = 1;\r", "alpha")
	term.exchange("SELECT id FROM items WHERE id = 2;\r", `{"id":2}`)
	term.exit("\\q\r")

	// Re-open, press Ctrl-R, filter for "WHERE id = 1", press Enter to prefill
	// into readline, then press Enter to execute the recalled multi-line SQL.
	term = f.terminal()
	term.exchange("\x12", "SQL history")
	picker := term.exchange("WHERE id = 1", "(1/2)")
	contains(t, picker, "FROM items")
	// Enter selects and prefills readline without executing; second Enter runs it.
	term.exchange("\r", "WHERE id = 1;")
	result := term.exchange("\r", `{"label":"alpha"}`)
	contains(t, result, "1 rows returned")
	term.exit("\\q\r")
}

func TestTerminalLargeResultSetPager(t *testing.T) {
	f := newFixture(t)
	var b strings.Builder
	b.WriteString("CREATE TABLE nums(id INTEGER, note TEXT);")
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&b, "INSERT INTO nums VALUES (%d, 'row-%02d');", i, i)
	}
	f.sql(b.String(), "--no-history")

	term := f.terminal("--no-history")
	if err := pty.Setsize(term.master, &pty.Winsize{Rows: 20, Cols: 80}); err != nil {
		t.Fatal(err)
	}
	term.exchange("\\format table\r", "demo/main> ")

	// 40 rows exceeds 20-row terminal height: auto pager opens.
	pagerView := term.exchange("SELECT * FROM nums ORDER BY id;\r", "dbh results (40 rows")
	contains(t, pagerView, "row-01")
	afterQuit := term.exchange("q", "demo/main> ")
	contains(t, afterQuit, "(40 rows)")

	// \pager off prints directly without opening the pager.
	term.exchange("\\pager off\r", "demo/main> ")
	direct := term.exchange("SELECT * FROM nums WHERE id <= 30 ORDER BY id;\r", "(30 rows)")
	excludes(t, direct, "dbh results (30 rows")
	contains(t, direct, "row-30")

	// \pager on opens the pager even for a 1-row result.
	term.exchange("\\pager on\r", "demo/main> ")
	forced := term.exchange("SELECT * FROM nums WHERE id = 1;\r", "dbh results (1 rows")
	contains(t, forced, "row-01")
	term.exchange("q", "demo/main> ")

	term.exit("\\q\r")
}
