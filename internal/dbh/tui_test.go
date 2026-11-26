package dbh

import (
	"bytes"
	"context"
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

func TestShouldUsePagerForSize(t *testing.T) {
	narrowCols := []string{"id", "name"}
	narrowRows := [][]string{{"1", "alice"}, {"2", "bob"}}
	wideCols := []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8", "c9", "c10"}
	wideRows := [][]string{
		{"value1", "value2", "value3", "value4", "value5", "value6", "value7", "value8", "value9", "value10"},
		{"value1", "value2", "value3", "value4", "value5", "value6", "value7", "value8", "value9", "value10"},
	}

	if !shouldUsePagerForSize("auto", 80, 24, wideCols, wideRows[:1]) {
		t.Fatal("expected auto pager for 1-row wide table")
	}
	if !shouldUsePagerForSize("auto", 80, 24, wideCols, wideRows) {
		t.Fatal("expected auto pager for 2-row wide table")
	}
	if shouldUsePagerForSize("auto", 80, 24, narrowCols, narrowRows) {
		t.Fatal("did not expect auto pager for narrow 2-row table")
	}
	tallRows := make([][]string, 15)
	for i := range tallRows {
		tallRows[i] = []string{"1", "row"}
	}
	if !shouldUsePagerForSize("auto", 80, 24, narrowCols, tallRows) {
		t.Fatal("expected auto pager for tall table")
	}
	if shouldUsePagerForSize("off", 80, 24, wideCols, wideRows) {
		t.Fatal("did not expect pager when mode is off")
	}
	if !shouldUsePagerForSize("on", 80, 24, narrowCols, narrowRows) {
		t.Fatal("expected pager when mode is on")
	}
}

func TestSessionMaxPagerRowsAndCanceledContext(t *testing.T) {
	ctx := context.Background()
	s, err := openSession(ctx, Profile{Name: "mem", Driver: "sqlite", DSN: ":memory:"}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.Execute(ctx, "CREATE TABLE nums (n INT); INSERT INTO nums VALUES (1),(2),(3),(4),(5);", "table", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	pagerCalls := 0
	s.MaxPagerRows = 3
	s.BatchSize = 2
	s.Pager = func(_ []string, _ [][]string, _ time.Duration) (bool, error) {
		pagerCalls++
		return true, nil
	}

	var notice bytes.Buffer
	s.Notice = &notice
	var out bytes.Buffer
	if err := s.Execute(ctx, "SELECT n FROM nums WHERE n <= 3;", "table", &out); err != nil {
		t.Fatal(err)
	}
	if pagerCalls != 1 {
		t.Fatalf("expected pager to be called once for <= MaxPagerRows, got %d", pagerCalls)
	}

	out.Reset()
	notice.Reset()
	if err := s.Execute(ctx, "SELECT n FROM nums;", "table", &out); err != nil {
		t.Fatal(err)
	}
	if pagerCalls != 1 {
		t.Fatalf("expected streaming fallback without calling pager when > MaxPagerRows, got %d", pagerCalls)
	}
	if !strings.Contains(notice.String(), "streaming output directly") || !strings.Contains(out.String(), "(5 rows)") {
		t.Fatalf("expected streaming notice and 5 rows output, got notice=%q out=%q", notice.String(), out.String())
	}

	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Execute(canceledCtx, "SELECT n FROM nums;", "table", &bytes.Buffer{}); err == nil {
		t.Fatal("expected canceled context to return error")
	}
}

func TestTablePagerSearchAndHorizontalJump(t *testing.T) {
	cols := []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8"}
	rows := [][]string{
		{"alpha", "beta", "gamma", "delta", "epsilon", "zeta", "eta", "target_one"},
		{"alpha", "beta", "gamma", "delta", "epsilon", "zeta", "eta", "target_two"},
	}
	var buf bytes.Buffer
	if err := renderTable(&buf, cols, rows, tableNaturalWidth(cols, rows)); err != nil {
		t.Fatal(err)
	}
	m := newTablePagerModel(buf.String(), len(rows), len(cols))
	m.elapsed = 6038 * time.Microsecond
	m.hasElapsed = true
	m.Resize(80, 10)
	if header := m.Render(); !strings.Contains(header, "Time: 6.038ms; 2 rows returned") {
		t.Fatalf("expected elapsed time and row count in header:\n%s", header)
	}
	m.Resize(40, 10)

	// Enter search mode and type "TARGET".
	m.HandleKey(tuiKey{kind: keyRune, r: '/'})
	for _, r := range "TARGET" {
		m.HandleKey(tuiKey{kind: keyRune, r: r})
	}
	if len(m.matches) != 2 {
		t.Fatalf("matches = %d, want 2", len(m.matches))
	}
	if m.xOffset == 0 {
		t.Fatalf("expected horizontal scroll to reveal far-right match, got xOffset=0")
	}
	view := m.Render()
	if !strings.Contains(view, "(1/2)") || !strings.Contains(view, "\x1b[7;1mtarget\x1b[0m_one") {
		t.Fatalf("expected first match in view:\n%s", view)
	}

	// Confirm search and jump with n / N.
	m.HandleKey(tuiKey{kind: keyEnter})
	m.HandleKey(tuiKey{kind: keyRune, r: 'n'})
	if m.matchIdx != 1 || !strings.Contains(m.Render(), "(2/2)") {
		t.Fatalf("expected (2/2) after n:\n%s", m.Render())
	}
	m.HandleKey(tuiKey{kind: keyRune, r: 'N'})
	if m.matchIdx != 0 || !strings.Contains(m.Render(), "(1/2)") {
		t.Fatalf("expected wrap-around to (1/2) after N:\n%s", m.Render())
	}

	// Search with no matches and cancel with Esc without quitting.
	m.HandleKey(tuiKey{kind: keyRune, r: '/'})
	for _, r := range "nope" {
		m.HandleKey(tuiKey{kind: keyRune, r: r})
	}
	if !strings.Contains(m.Render(), "(no matches)") {
		t.Fatalf("expected no matches indicator:\n%s", m.Render())
	}
	if m.HandleKey(tuiKey{kind: keyEsc}) {
		t.Fatal("Esc in search mode must cancel search, not quit pager")
	}
	if m.searching || m.query != "" {
		t.Fatalf("expected cleared search state, got searching=%v query=%q", m.searching, m.query)
	}
}
