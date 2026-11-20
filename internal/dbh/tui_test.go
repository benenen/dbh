package dbh

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestSpinnerDrawsAndClearsLine(t *testing.T) {
	var out syncBuffer
	stop := startSpinnerWithDelay(&out, "Running query", 5*time.Millisecond, 10*time.Millisecond)
	time.Sleep(40 * time.Millisecond)
	stop()
	stop() // idempotent

	got := out.String()
	if !strings.Contains(got, "Running query...") {
		t.Fatalf("expected spinner output, got %q", got)
	}
	if !strings.HasSuffix(got, "\r\x1b[2K") {
		t.Fatalf("expected clear-line sequence at end, got %q", got)
	}
}

func TestSpinnerNoOutputBeforeDelay(t *testing.T) {
	var out syncBuffer
	stop := startSpinnerWithDelay(&out, "Fast query", 500*time.Millisecond, 20*time.Millisecond)
	stop()
	if out.String() != "" {
		t.Fatalf("expected no output when stopped before delay, got %q", out.String())
	}
}

func TestFuzzyHistoryFilterAndPickerModel(t *testing.T) {
	entries := []string{
		"SELECT id, name\nFROM users\nWHERE active = 1;",
		"SELECT * FROM orders;",
		"SELECT email FROM users WHERE id = 42;",
	}
	got := filterHistory(entries, "users id")
	if len(got) != 2 {
		t.Fatalf("filterHistory = %v, want 2 matches", got)
	}
	if !strings.Contains(got[0], "id = 42") {
		t.Fatalf("first match = %q, want newer users query", got[0])
	}

	m := newHistoryPickerModel(entries, "")
	m.Resize(80, 24)
	view := m.Render()
	if !strings.Contains(view, "SQL history") || !strings.Contains(view, "SELECT email FROM users") {
		t.Fatalf("unexpected initial picker view:\n%s", view)
	}
	// Move down to the multi-line entry (index 2).
	m.HandleKey(tuiKey{kind: keyDown})
	m.HandleKey(tuiKey{kind: keyDown})
	view = m.Render()
	if !strings.Contains(view, "WHERE active = 1;") {
		t.Fatalf("preview missing multiline body:\n%s", view)
	}
	if !m.HandleKey(tuiKey{kind: keyEnter}) {
		t.Fatal("Enter must quit picker")
	}
	if !m.confirmed || m.selected != entries[0] {
		t.Fatalf("picked = (%v, %q), want %q", m.confirmed, m.selected, entries[0])
	}
}

func TestTablePagerModelScrollAndQuit(t *testing.T) {
	cols := []string{"id", "payload"}
	var rows [][]string
	for i := 0; i < 30; i++ {
		rows = append(rows, []string{"1", strings.Repeat("x", 120)})
	}
	if w := tableNaturalWidth(cols, rows); w < 120 {
		t.Fatalf("tableNaturalWidth = %d, want >= 120", w)
	}
	var buf bytes.Buffer
	if err := renderTable(&buf, cols, rows, tableNaturalWidth(cols, rows)); err != nil {
		t.Fatal(err)
	}
	m := newTablePagerModel(buf.String(), len(rows), len(cols))
	m.Resize(80, 20)
	view := m.Render()
	if !strings.Contains(view, "dbh results (30 rows × 2 cols)") {
		t.Fatalf("unexpected pager header:\n%s", view)
	}
	m.HandleKey(tuiKey{kind: keyRune, r: 'G'})
	if !strings.Contains(m.Render(), "100%") {
		t.Fatalf("expected 100%% after G:\n%s", m.Render())
	}
	m.HandleKey(tuiKey{kind: keyRight})
	if m.xOffset != 4 {
		t.Fatalf("xOffset = %d, want 4", m.xOffset)
	}
	if !m.HandleKey(tuiKey{kind: keyRune, r: 'q'}) {
		t.Fatal("q must quit pager")
	}
}
