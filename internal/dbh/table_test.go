package dbh

import (
	"bytes"
	"strings"
	"testing"

	"github.com/rivo/uniseg"
)

func TestTableSeparatesMultilineRecords(t *testing.T) {
	var out bytes.Buffer
	if err := renderTable(&out, []string{"id", "note"}, [][]string{{"1", "first\nsecond"}, {"2", "third"}}, 40); err != nil {
		t.Fatal(err)
	}
	want := "┌────┬────────┐\n│ id │ note   │\n├────┼────────┤\n│ 1  │ first  │\n│    │ second │\n├────┼────────┤\n│ 2  │ third  │\n└────┴────────┘\n"
	if out.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", &out, want)
	}
}
func TestTableWrapsUnicodeWithinTerminalWidth(t *testing.T) {
	var out bytes.Buffer
	text := "你好世界👩‍💻你好世界👩‍💻"
	if err := renderTable(&out, []string{"id", "note"}, [][]string{{"1", text}, {"2", "next"}}, 22); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if uniseg.StringWidth(line) > 22 {
			t.Fatalf("line exceeds terminal width: %q", line)
		}
		if strings.Contains(line, "👩") && !strings.Contains(line, "👩‍💻") {
			t.Fatalf("split grapheme: %q", line)
		}
	}
	if strings.Count(out.String(), "├") != 2 || !strings.Contains(out.String(), "next") {
		t.Fatalf("records are not distinct:\n%s", &out)
	}
}
func TestTableWideResultsStayHorizontal(t *testing.T) {
	cols := []string{"a", "b", "c", "d", "e", "f", "g"}
	rows := [][]string{{"1", "2", "3", "4", "5", "6", "7"}}

	var out bytes.Buffer
	if err := renderTable(&out, cols, rows, 22); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, "Row 1:") || strings.Contains(got, "Column") {
		t.Fatalf("unexpected vertical card layout:\n%s", got)
	}
	if !strings.Contains(got, "│ a │ b │ c │ d │ e │ f │ g │") || !strings.Contains(got, "│ 1 │ 2 │ 3 │ 4 │ 5 │ 6 │ 7 │") {
		t.Fatalf("expected horizontal table layout, got:\n%s", got)
	}

	var exp bytes.Buffer
	if err := renderExpandedTable(&exp, cols, rows, 22); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(exp.String(), "Row 1:") || !strings.Contains(exp.String(), "Column") {
		t.Fatalf("expected expanded vertical layout, got:\n%s", exp.String())
	}
}
