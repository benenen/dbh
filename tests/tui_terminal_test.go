//go:build e2e && (linux || darwin)

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
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
	bottomView := term.exchange("G", "row-40")
	contains(t, bottomView, "id")
	contains(t, bottomView, "note")
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

func TestTerminalWideSmallResultOpensPager(t *testing.T) {
	f := newFixture(t)
	f.sql(`CREATE TABLE wide_items(
		col_01 TEXT, col_02 TEXT, col_03 TEXT, col_04 TEXT, col_05 TEXT,
		col_06 TEXT, col_07 TEXT, col_08 TEXT, col_09 TEXT, col_10 TEXT
	);
	INSERT INTO wide_items VALUES
		('alpha-1', 'beta-1', 'gamma-1', 'delta-1', 'epsilon-1', 'zeta-1', 'eta-1', 'theta-1', 'iota-1', 'kappa-1'),
		('alpha-2', 'beta-2', 'gamma-2', 'delta-2', 'epsilon-2', 'zeta-2', 'eta-2', 'theta-2', 'iota-2', 'kappa-2');`, "--no-history")

	term := f.terminal("--no-history")
	if err := pty.Setsize(term.master, &pty.Winsize{Rows: 20, Cols: 80}); err != nil {
		t.Fatal(err)
	}
	term.exchange("\\format table\r", "demo/main> ")

	// Default \pager auto opens the horizontal pager even for 2 wide rows.
	pagerView := term.exchange("SELECT * FROM wide_items LIMIT 2;\r", "dbh results (2 rows")
	contains(t, pagerView, "Time:")
	contains(t, pagerView, "2 rows returned")
	contains(t, pagerView, "No")
	contains(t, pagerView, "alpha-1")
	searchView := term.exchange("/kappa-2\r", "(1/1)")
	contains(t, searchView, "No")
	contains(t, searchView, "kappa-2")
	afterQuit := term.exchange("q", "demo/main> ")
	contains(t, afterQuit, "(2 rows)")

	// \pager off prints the horizontal table directly without vertical cards.
	term.exchange("\\pager off\r", "demo/main> ")
	direct := term.exchange("SELECT * FROM wide_items LIMIT 2;\r", "(2 rows)")
	excludes(t, direct, "dbh results (2 rows")
	excludes(t, direct, "Row 1:")
	contains(t, direct, "alph")
	contains(t, direct, "a-1")

	term.exit("\\q\r")
}

func TestTerminalPagerAdvancedInteractions(t *testing.T) {
	f := newFixture(t)
	f.sql(`CREATE TABLE items(id INTEGER PRIMARY KEY, name TEXT, role TEXT);
		INSERT INTO items VALUES
			(1, 'alpha', 'admin'),
			(2, 'beta', 'editor'),
			(3, 'gamma', 'viewer'),
			(4, 'delta', 'guest'),
			(5, 'epsilon', 'ops');`, "--no-history")

	term := f.terminal("--no-history")
	if err := pty.Setsize(term.master, &pty.Winsize{Rows: 20, Cols: 80}); err != nil {
		t.Fatal(err)
	}
	term.exchange("\\format table\r", "demo/main> ")
	term.exchange("\\limit 3\r", "demo/main> ")
	term.exchange("\\pager on\r", "demo/main> ")

	pagerView := term.exchange("SELECT * FROM items ORDER BY id;\r", "truncated")
	contains(t, pagerView, "No")
	contains(t, pagerView, "alpha")

	// Enter opens single-row detail view; l moves to row 2 (beta); Esc returns to table.
	detail := term.exchange("\r", "Row 1 (1/3)")
	contains(t, detail, "alpha")
	detail2 := term.exchange("l", "Row 2 (2/3)")
	contains(t, detail2, "beta")
	term.exchange("\x1b", "dbh results (3 rows")

	// & filters rows, preserving original No=2; s saves filtered rows to CSV; y copies row 2.
	filtered := term.exchange("&beta\r", `filter:"beta"`)
	contains(t, filtered, "beta")
	csvPath := filepath.Join(t.TempDir(), "filtered.csv")
	saved := term.exchange("s"+csvPath+"\r", "Saved 1 rows")
	contains(t, saved, "Saved 1 rows to")
	rawCSV, err := os.ReadFile(csvPath)
	if err != nil {
		t.Fatal(err)
	}
	contains(t, string(rawCSV), "beta")
	excludes(t, string(rawCSV), "alpha")

	copied := term.exchange("y", "Copied row 2")
	contains(t, copied, "Copied row 2")

	// Esc clears filter; q exits pager back to shell prompt.
	cleared := term.exchange("\x1b", "alpha")
	contains(t, cleared, "gamma")
	term.exchange("q", "demo/main> ")
	term.exit("\\q\r")
}

func TestTerminalMetaCommandsAndHistoryDelete(t *testing.T) {
	f := newFixture(t)
	f.seed()

	term := f.terminal()
	contains(t, term.exchange("\\timeout 15s\r", "Timeout: 15s"), "15s")

	dir := t.TempDir()
	outPath := filepath.Join(dir, "out.txt")
	term.exchange("\\o "+outPath+"\r", "Output redirected")
	term.exchange("SELECT name FROM users ORDER BY id;\r", "demo/main> ")
	term.exchange("\\o\r", "Output restored")
	rawOut, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	contains(t, string(rawOut), "Alice")

	scriptPath := filepath.Join(dir, "script.sql")
	if err := os.WriteFile(scriptPath, []byte("SELECT 99 AS from_script;\n"), 0600); err != nil {
		t.Fatal(err)
	}
	scriptOut := term.exchange("\\i "+scriptPath+"\r", "99")
	contains(t, scriptOut, "from_script")

	explainOut := term.exchange("\\explain SELECT * FROM users;\r", "SCAN users")
	contains(t, explainOut, "SCAN users")

	reconnectOut := term.exchange("\\reconnect\r", "Reconnected to demo")
	contains(t, reconnectOut, "demo")

	// Context-aware secondary prompt for unclosed single quote.
	sec := term.exchange("SELECT 'open\r", "'> ")
	contains(t, sec, "'> ")
	closed := term.exchange("';\r", "open")
	contains(t, closed, "open")

	// Ctrl-R + Ctrl-D deletes the newest matching history entry.
	term.exchange("SELECT 111 AS keep_me;\r", "111")
	term.exchange("SELECT 222 AS delete_me;\r", "222")
	term.exchange("\x12", "SQL history")
	term.exchange("\x04", "keep_me")
	term.exchange("\x1b", "demo/main> ")
	hist := term.exchange("\\history\r", "keep_me")
	contains(t, hist, "keep_me")
	excludes(t, hist, "delete_me")

	term.exit("\\q\r")
}
