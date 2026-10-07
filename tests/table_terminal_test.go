//go:build e2e && (linux || darwin)

package e2e

import (
	"strings"
	"testing"

	"github.com/creack/pty"
	"github.com/rivo/uniseg"
)

func TestTerminalTableWrapsAndSeparatesRecords(t *testing.T) {
	f := newFixture(t)
	f.sql("CREATE TABLE wrapped(id INTEGER, note TEXT); INSERT INTO wrapped VALUES(1, 'first'||char(10)||'你好世界👩‍💻你好世界👩‍💻你好世界👩‍💻'), (2, 'next record');", "--no-history")
	term := f.terminal("--no-history")
	if err := pty.Setsize(term.master, &pty.Winsize{Rows: 24, Cols: 40}); err != nil {
		t.Fatal(err)
	}
	term.exchange("\\format table\r", "demo> ")
	sql := "SELECT * FROM wrapped ORDER BY id;"
	output := term.exchange(sql+"\r", "(2 rows)")
	start := strings.LastIndex(output, "┌")
	if start < 0 {
		t.Fatalf("missing bordered table: %q", output)
	}
	end := strings.Index(output[start:], "(2 rows)")
	if start < 0 || end < 0 {
		t.Fatalf("missing bordered table: %q", output)
	}
	table := output[start : start+end]
	contains(t, table, "first")
	contains(t, table, "next record")
	equal(t, strings.Count(table, "├"), 2)
	for _, line := range strings.Split(strings.TrimSpace(table), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if uniseg.StringWidth(line) > 40 {
			t.Fatalf("table exceeds terminal width: %q", line)
		}
		if strings.Contains(line, "👩") && !strings.Contains(line, "👩‍💻") {
			t.Fatalf("split grapheme: %q", line)
		}
	}
	term.exit("\\q\r")
}
