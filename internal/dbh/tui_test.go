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
	s.Pager = func(_ []string, _ [][]string, _ time.Duration, _ bool) (bool, error) {
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

func TestTablePagerPinnedHeaderOnVerticalAndHorizontalScroll(t *testing.T) {
	cols := []string{"first_col", "second_col", "third_col", "fourth_col"}
	rows := make([][]string, 40)
	for i := range rows {
		tag := string(rune('0'+i/10)) + string(rune('0'+i%10))
		rows[i] = []string{
			"r-" + tag,
			strings.Repeat("b", 12),
			strings.Repeat("c", 12),
			"row-" + tag,
		}
	}
	var buf bytes.Buffer
	if err := renderTable(&buf, cols, rows, tableNaturalWidth(cols, rows)); err != nil {
		t.Fatal(err)
	}
	m := newTablePagerModel(buf.String(), len(rows), len(cols))
	m.Resize(30, 12)

	// Scroll to bottom: header should stay pinned at the top while bottom row is visible.
	m.HandleKey(tuiKey{kind: keyRune, r: 'G'})
	view := m.Render()
	if !strings.Contains(view, "first_col") {
		t.Fatalf("expected pinned header first_col after scrolling to bottom:\n%s", view)
	}
	if !strings.Contains(view, "r-39") {
		t.Fatalf("expected bottom r-39 after scrolling to bottom:\n%s", view)
	}

	// Scroll horizontally to the right: pinned header should track xOffset too.
	for range 12 {
		m.HandleKey(tuiKey{kind: keyRight})
	}
	hView := m.Render()
	if !strings.Contains(hView, "fourth_col") || !strings.Contains(hView, "row-39") {
		t.Fatalf("expected pinned header fourth_col and row-39 after horizontal scroll:\n%s", hView)
	}
	if strings.Contains(hView, "first_col") {
		t.Fatalf("did not expect first_col after scrolling past it:\n%s", hView)
	}
}

func TestTablePagerPinnedNoColumn(t *testing.T) {
	cols := []string{"first_col", "second_col", "third_col", "fourth_col"}
	rows := make([][]string, 40)
	for i := range rows {
		tag := string(rune('0'+i/10)) + string(rune('0'+i%10))
		rows[i] = []string{
			"r-" + tag,
			strings.Repeat("b", 12),
			strings.Repeat("c", 12),
			"row-" + tag,
		}
	}
	numberedCols, numberedRows := prependRowNumbers(cols, rows)
	var buf bytes.Buffer
	if err := renderTable(&buf, numberedCols, numberedRows, tableNaturalWidth(numberedCols, numberedRows)); err != nil {
		t.Fatal(err)
	}
	m := newTablePagerModel(buf.String(), len(rows), len(cols))
	m.pinFirstColumn()
	m.Resize(34, 12)

	// Scroll to bottom and far right: No column and row number 40 stay pinned on the left.
	m.HandleKey(tuiKey{kind: keyRune, r: 'G'})
	for range 12 {
		m.HandleKey(tuiKey{kind: keyRight})
	}
	view := m.Render()
	if !strings.Contains(view, "No") || !strings.Contains(view, "40") {
		t.Fatalf("expected pinned No column and row number 40:\n%s", view)
	}
	if !strings.Contains(view, "fourth_col") || !strings.Contains(view, "row-39") {
		t.Fatalf("expected scrolled fourth_col and row-39 alongside pinned No column:\n%s", view)
	}
	if strings.Contains(view, "first_col") {
		t.Fatalf("did not expect first_col after horizontal scroll:\n%s", view)
	}
}

func TestParseTUIKeysAndWordDeletion(t *testing.T) {
	// Modified CSI arrow (\x1b[1;5C) -> keyRight, SGR wheel -> keyMouseUp, unknown CSI (\x1b[15~) ignored.
	keys := parseTUIKeys([]byte("\x1b[1;5C\x1b[<64;10;5M\x1b[15~\x17"))
	if len(keys) != 3 || keys[0].kind != keyRight || keys[1].kind != keyMouseUp || keys[2].kind != keyCtrlW {
		t.Fatalf("unexpected keys: %+v", keys)
	}

	// History picker Ctrl-W deletes last word.
	hp := newHistoryPickerModel([]string{"SELECT * FROM users;", "SELECT * FROM orders;"}, "SELECT users")
	hp.HandleKey(tuiKey{kind: keyCtrlW})
	if string(hp.query) != "SELECT " {
		t.Fatalf("expected 'SELECT ' after Ctrl-W, got %q", string(hp.query))
	}
}

func TestTablePagerSearchEscClearAndNarrowClipping(t *testing.T) {
	cols := []string{"first_col", "second_col", "third_col"}
	rows := [][]string{{"alpha", "beta", "gamma"}}
	numberedCols, numberedRows := prependRowNumbers(cols, rows)
	var buf bytes.Buffer
	if err := renderTable(&buf, numberedCols, numberedRows, tableNaturalWidth(numberedCols, numberedRows)); err != nil {
		t.Fatal(err)
	}
	m := newTablePagerModel(buf.String(), len(rows), len(cols))
	m.pinFirstColumn()
	m.truncated = true
	m.Resize(48, 10)

	// Header must include truncated indicator when width allows.
	view := m.Render()
	if !strings.Contains(view, "truncated") {
		t.Fatalf("expected truncated indicator in header:\n%s", view)
	}
	m.Resize(36, 10)
	narrowView := m.Render()
	lines := strings.Split(narrowView, "\n")
	if len(lines) < 2 || strings.Contains(lines[0], "truncated") {
		t.Fatalf("expected narrow header to be clipped at width 36:\n%s", narrowView)
	}

	// Search + Ctrl-W + Enter + Esc clears search first before quitting on second Esc.
	m.HandleKey(tuiKey{kind: keyRune, r: '/'})
	for _, r := range "alpha beta" {
		m.HandleKey(tuiKey{kind: keyRune, r: r})
	}
	m.HandleKey(tuiKey{kind: keyCtrlW})
	if m.query != "alpha " {
		t.Fatalf("expected query 'alpha ' after Ctrl-W, got %q", m.query)
	}
	m.HandleKey(tuiKey{kind: keyEnter})
	if quit := m.HandleKey(tuiKey{kind: keyEsc}); quit || m.query != "" {
		t.Fatalf("first Esc should clear search without quitting (quit=%v, query=%q)", quit, m.query)
	}
	if quit := m.HandleKey(tuiKey{kind: keyEsc}); !quit {
		t.Fatal("second Esc with empty query should quit pager")
	}

	// 0 and $ horizontal jumps.
	m.HandleKey(tuiKey{kind: keyRune, r: '$'})
	if m.xOffset != m.maxXOffset() {
		t.Fatalf("expected xOffset=%d after $, got %d", m.maxXOffset(), m.xOffset)
	}
	m.HandleKey(tuiKey{kind: keyRune, r: '0'})
	if m.xOffset != 0 {
		t.Fatalf("expected xOffset=0 after 0, got %d", m.xOffset)
	}
}

func TestParseTUIKeysMouseTabAndHistoryDelete(t *testing.T) {
	keys := parseTUIKeys([]byte("\t\x1b[Z\x04\x1b[<64;10;5M\x1b[<65;10;5M"))
	if len(keys) != 5 ||
		keys[0].kind != keyTab ||
		keys[1].kind != keyShiftTab ||
		keys[2].kind != keyCtrlD ||
		keys[3].kind != keyMouseUp ||
		keys[4].kind != keyMouseDown {
		t.Fatalf("unexpected keys: %+v", keys)
	}

	hp := newHistoryPickerModel([]string{"SELECT 1;", "SELECT 2;"}, "")
	hp.HandleKey(tuiKey{kind: keyCtrlD})
	if len(hp.all) != 1 || hp.all[0] != "SELECT 1;" || len(hp.deleted) != 1 || hp.deleted[0] != "SELECT 2;" {
		t.Fatalf("unexpected history delete state: all=%v deleted=%v", hp.all, hp.deleted)
	}
}

func TestTablePagerDetailFilterSaveCopyAndColJump(t *testing.T) {
	cols := []string{"name", "role", "note"}
	rows := [][]string{
		{"alice", "admin", "first note"},
		{"bob", "viewer", "second note"},
	}
	numberedCols, numberedRows := prependRowNumbers(cols, rows)
	var buf bytes.Buffer
	if err := renderTable(&buf, numberedCols, numberedRows, tableNaturalWidth(numberedCols, numberedRows)); err != nil {
		t.Fatal(err)
	}
	m := newTablePagerModel(buf.String(), len(rows), len(cols))
	m.pinFirstColumn()
	m.setSourceData(cols, rows)
	var copied string
	m.copyFn = func(seq string) error {
		copied = seq
		return nil
	}
	m.Resize(30, 12)

	// Tab / Shift-Tab column jumps.
	m.HandleKey(tuiKey{kind: keyTab})
	if m.xOffset == 0 {
		t.Fatal("expected Tab to jump to next column offset")
	}
	m.HandleKey(tuiKey{kind: keyShiftTab})
	if m.xOffset != 0 {
		t.Fatalf("expected Shift-Tab to jump back to 0, got %d", m.xOffset)
	}

	// Copy row (y).
	m.HandleKey(tuiKey{kind: keyRune, r: 'y'})
	if !strings.HasPrefix(copied, "\x1b]52;c;") || !strings.Contains(m.statusMsg, "Copied row 1") {
		t.Fatalf("unexpected copy state: copied=%q status=%q", copied, m.statusMsg)
	}

	// Detail view (Enter -> Right -> Esc).
	m.HandleKey(tuiKey{kind: keyEnter})
	if !m.detailOpen {
		t.Fatal("expected detail view to open on Enter")
	}
	detailView := m.Render()
	if !strings.Contains(detailView, "Row 1 (1/2)") || !strings.Contains(detailView, "alice") {
		t.Fatalf("unexpected detail view:\n%s", detailView)
	}
	m.HandleKey(tuiKey{kind: keyRight})
	if !strings.Contains(m.Render(), "bob") {
		t.Fatal("expected next row bob in detail view")
	}
	m.HandleKey(tuiKey{kind: keyEsc})
	if m.detailOpen {
		t.Fatal("expected Esc to close detail view")
	}

	// Filter (&bob + Enter).
	m.HandleKey(tuiKey{kind: keyRune, r: '&'})
	for _, r := range "bob" {
		m.HandleKey(tuiKey{kind: keyRune, r: r})
	}
	m.HandleKey(tuiKey{kind: keyEnter})
	if m.rows != 1 || len(m.filteredIdx) != 1 || m.filteredIdx[0] != 1 {
		t.Fatalf("expected filtered row bob (index 1), got rows=%d idx=%v", m.rows, m.filteredIdx)
	}

	// Save filtered rows to TSV/JSON/CSV.
	dir := t.TempDir()
	for _, ext := range []string{".csv", ".tsv", ".json"} {
		outPath := dir + "/out" + ext
		m.HandleKey(tuiKey{kind: keyRune, r: 's'})
		for _, r := range outPath {
			m.HandleKey(tuiKey{kind: keyRune, r: r})
		}
		m.HandleKey(tuiKey{kind: keyEnter})
		if !strings.Contains(m.statusMsg, "Saved 1 rows") {
			t.Fatalf("expected save confirmation for %s, got %q", ext, m.statusMsg)
		}
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
