package dbh

import (
	"bytes"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
	"golang.org/x/term"
)

func validPagerMode(mode string) bool {
	return mode == "auto" || mode == "on" || mode == "off"
}

func terminalSize(out io.Writer) (width, height int, ok bool) {
	if !isTerminalWriter(out) {
		return 0, 0, false
	}
	f, ok := out.(*os.File)
	if !ok {
		return 0, 0, false
	}
	w, h, err := term.GetSize(int(f.Fd()))
	if err != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

func tableNaturalWidth(columns []string, rows [][]string) int {
	total := 1
	for i, col := range columns {
		w := max(1, cellWidth(col))
		for _, row := range rows {
			if i < len(row) {
				w = max(w, cellWidth(row[i]))
			}
		}
		total += w + 3
	}
	return min(total, 4096)
}

func shouldUsePager(mode string, out io.Writer, columns []string, rows [][]string) bool {
	w, h, ok := terminalSize(out)
	if !ok {
		return false
	}
	return shouldUsePagerForSize(mode, w, h, columns, rows)
}

func shouldUsePagerForSize(mode string, width, height int, columns []string, rows [][]string) bool {
	if len(columns) == 0 || mode == "off" || width <= 0 || height <= 0 {
		return false
	}
	if mode == "on" {
		return true
	}
	// auto: page when the table exceeds terminal height or width.
	estimatedLines := len(rows)*2 + 3
	if estimatedLines > height-4 {
		return true
	}
	return tableNaturalWidth(columns, rows) > width
}

type pagerMatch struct {
	line     int
	colStart int
	colEnd   int
}

type tablePagerModel struct {
	lines          []string
	headerLines    int
	pinnedColWidth int
	maxWidth       int
	rows           int
	cols           int
	elapsed        time.Duration
	hasElapsed     bool
	truncated      bool
	width          int
	height         int
	yOffset        int
	xOffset        int
	searching      bool
	query          string
	matches        []pagerMatch
	matchIdx       int
	rawCols        []string
	rawRows        [][]string
	filteredIdx    []int
	rowStartLines  []int
	colOffsets     []int
	detailOpen     bool
	detailRow      int
	detailYOffset  int
	filtering      bool
	filterQuery    string
	saving         bool
	savePath       string
	statusMsg      string
	copyFn         func(string) error
}

func newTablePagerModel(content string, rows, cols int) *tablePagerModel {
	trimmed := strings.TrimSuffix(content, "\n")
	lines := strings.Split(trimmed, "\n")
	maxW := 0
	headerLines := 0
	for i, l := range lines {
		maxW = max(maxW, uniseg.StringWidth(l))
		if headerLines == 0 && strings.HasPrefix(l, "├") && i+1 < len(lines) {
			headerLines = i + 1
		}
	}
	return &tablePagerModel{
		lines:       lines,
		headerLines: headerLines,
		maxWidth:    maxW,
		rows:        rows,
		cols:        cols,
		width:       80,
		height:      24,
		copyFn: func(seq string) error {
			_, err := io.WriteString(os.Stdout, seq)
			return err
		},
	}
}

func (m *tablePagerModel) setSourceData(cols []string, rows [][]string) {
	m.rawCols = cols
	m.rawRows = rows
	m.filteredIdx = make([]int, len(rows))
	for i := range rows {
		m.filteredIdx[i] = i
	}
	m.computeOffsets()
}

func (m *tablePagerModel) computeOffsets() {
	m.colOffsets = nil
	if len(m.lines) > 0 {
		line := m.lines[0]
		skipFirst := m.pinnedColWidth > 0
		searchStart := 0
		for {
			idx := strings.Index(line[searchStart:], "┬")
			if idx < 0 {
				break
			}
			absEnd := searchStart + idx + len("┬")
			if skipFirst {
				skipFirst = false
			} else {
				off := uniseg.StringWidth(line[:absEnd]) - m.pinnedColWidth
				if off > 0 {
					m.colOffsets = append(m.colOffsets, off)
				}
			}
			searchStart = absEnd
		}
	}
	m.rowStartLines = nil
	if m.headerLines > 0 && m.headerLines < len(m.lines) {
		m.rowStartLines = append(m.rowStartLines, m.headerLines)
		for i := m.headerLines + 1; i < len(m.lines); i++ {
			if strings.HasPrefix(m.lines[i], "├") && i+1 < len(m.lines) {
				m.rowStartLines = append(m.rowStartLines, i+1)
			}
		}
	}
}

func (m *tablePagerModel) topVisibleRow() int {
	if len(m.rowStartLines) == 0 || len(m.filteredIdx) == 0 {
		return 0
	}
	target := m.pinnedLines() + m.yOffset
	row := 0
	for i, lineIdx := range m.rowStartLines {
		if lineIdx <= target {
			row = i
		} else {
			break
		}
	}
	if row >= len(m.filteredIdx) {
		row = len(m.filteredIdx) - 1
	}
	return row
}

func (m *tablePagerModel) nextColOffset() int {
	for _, off := range m.colOffsets {
		if off > m.xOffset {
			return min(off, m.maxXOffset())
		}
	}
	return m.maxXOffset()
}

func (m *tablePagerModel) prevColOffset() int {
	for i := len(m.colOffsets) - 1; i >= 0; i-- {
		if m.colOffsets[i] < m.xOffset {
			return m.colOffsets[i]
		}
	}
	return 0
}

func (m *tablePagerModel) rebuildFilteredTable() {
	q := strings.ToLower(strings.TrimSpace(m.filterQuery))
	m.filteredIdx = m.filteredIdx[:0]
	for i, row := range m.rawRows {
		if q == "" {
			m.filteredIdx = append(m.filteredIdx, i)
			continue
		}
		matched := false
		for _, cell := range row {
			if strings.Contains(strings.ToLower(cell), q) {
				matched = true
				break
			}
		}
		if matched {
			m.filteredIdx = append(m.filteredIdx, i)
		}
	}
	numberedCols := make([]string, len(m.rawCols)+1)
	numberedCols[0] = "No"
	copy(numberedCols[1:], m.rawCols)
	numberedRows := make([][]string, len(m.filteredIdx))
	for i, origIdx := range m.filteredIdx {
		r := make([]string, len(m.rawRows[origIdx])+1)
		r[0] = strconv.Itoa(origIdx + 1)
		copy(r[1:], m.rawRows[origIdx])
		numberedRows[i] = r
	}
	var buf bytes.Buffer
	width := max(tableNaturalWidth(numberedCols, numberedRows), 80)
	_ = renderTable(&buf, numberedCols, numberedRows, width)
	trimmed := strings.TrimSuffix(buf.String(), "\n")
	m.lines = strings.Split(trimmed, "\n")
	m.maxWidth = 0
	m.headerLines = 0
	for i, l := range m.lines {
		m.maxWidth = max(m.maxWidth, uniseg.StringWidth(l))
		if m.headerLines == 0 && strings.HasPrefix(l, "├") && i+1 < len(m.lines) {
			m.headerLines = i + 1
		}
	}
	m.rows = len(m.filteredIdx)
	m.pinFirstColumn()
	m.computeOffsets()
	m.yOffset = 0
	m.xOffset = 0
	m.recomputeMatches()
	m.clamp()
}

func (m *tablePagerModel) saveVisible(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("file path is required")
	}
	var buf bytes.Buffer
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".tsv":
		_, _ = fmt.Fprintln(&buf, tsvLine(m.rawCols))
		for _, idx := range m.filteredIdx {
			_, _ = fmt.Fprintln(&buf, tsvLine(m.rawRows[idx]))
		}
	case ".json":
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		for _, idx := range m.filteredIdx {
			row := m.rawRows[idx]
			obj := make(map[string]string, len(m.rawCols))
			for c, col := range m.rawCols {
				if c < len(row) {
					obj[col] = row[c]
				}
			}
			if err := enc.Encode(obj); err != nil {
				return err
			}
		}
	default:
		w := csv.NewWriter(&buf)
		if err := w.Write(m.rawCols); err != nil {
			return err
		}
		for _, idx := range m.filteredIdx {
			if err := w.Write(m.rawRows[idx]); err != nil {
				return err
			}
		}
		w.Flush()
		if err := w.Error(); err != nil {
			return err
		}
	}
	return writeAtomicFile(path, buf.Bytes())
}

func (m *tablePagerModel) copyRow(visibleIdx int) {
	if visibleIdx < 0 || visibleIdx >= len(m.filteredIdx) {
		return
	}
	origIdx := m.filteredIdx[visibleIdx]
	text := strings.Join(m.rawRows[origIdx], "\t")
	seq := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07"
	if m.copyFn != nil {
		if err := m.copyFn(seq); err != nil {
			m.statusMsg = "Copy failed: " + err.Error()
			return
		}
	}
	m.statusMsg = fmt.Sprintf("Copied row %d", origIdx+1)
}

func (m *tablePagerModel) detailLines() []string {
	if len(m.filteredIdx) == 0 || m.detailRow < 0 || m.detailRow >= len(m.filteredIdx) {
		return nil
	}
	row := m.rawRows[m.filteredIdx[m.detailRow]]
	colW := 0
	for _, col := range m.rawCols {
		colW = max(colW, uniseg.StringWidth(col))
	}
	colW = min(colW, max(12, m.width/3))
	valW := max(12, m.width-colW-4)
	var lines []string
	for i, col := range m.rawCols {
		val := ""
		if i < len(row) {
			val = row[i]
		}
		wrapped := wrapCell(val, valW)
		if len(wrapped) == 0 {
			wrapped = []string{""}
		}
		label := sliceColumns(col, 0, colW)
		pad := strings.Repeat(" ", max(0, colW-uniseg.StringWidth(label)))
		lines = append(lines, label+pad+" │ "+wrapped[0])
		blank := strings.Repeat(" ", colW) + " │ "
		for _, extra := range wrapped[1:] {
			lines = append(lines, blank+extra)
		}
	}
	return lines
}

func (m *tablePagerModel) Resize(width, height int) {
	m.width = width
	m.height = height
	m.clamp()
}

func (m *tablePagerModel) viewHeight() int {
	return max(1, m.height-2)
}

func (m *tablePagerModel) pinnedLines() int {
	if m.headerLines > 0 && m.viewHeight() > m.headerLines {
		return m.headerLines
	}
	return 0
}

func (m *tablePagerModel) pinFirstColumn() {
	if len(m.lines) == 0 {
		return
	}
	if idx := strings.Index(m.lines[0], "┬"); idx >= 0 {
		m.pinnedColWidth = uniseg.StringWidth(m.lines[0][:idx+len("┬")])
	}
}

func (m *tablePagerModel) pinnedCols() int {
	if m.pinnedColWidth > 0 && m.width > m.pinnedColWidth+4 {
		return m.pinnedColWidth
	}
	return 0
}

func (m *tablePagerModel) bodyHeight() int {
	return max(1, m.viewHeight()-m.pinnedLines())
}

func (m *tablePagerModel) maxYOffset() int {
	return max(0, len(m.lines)-m.pinnedLines()-m.bodyHeight())
}

func (m *tablePagerModel) maxXOffset() int {
	return max(0, m.maxWidth-m.width)
}

func (m *tablePagerModel) clamp() {
	m.yOffset = min(max(0, m.yOffset), m.maxYOffset())
	m.xOffset = min(max(0, m.xOffset), m.maxXOffset())
}

func (m *tablePagerModel) recomputeMatches() {
	m.matches = nil
	m.matchIdx = 0
	if m.query == "" {
		return
	}
	qRunes := []rune(m.query)
	for i, r := range qRunes {
		qRunes[i] = unicode.ToLower(r)
	}
	qLen := len(qRunes)
	for lineIdx, line := range m.lines {
		lRunes := []rune(line)
		if len(lRunes) < qLen {
			continue
		}
		lowerRunes := make([]rune, len(lRunes))
		for i, r := range lRunes {
			lowerRunes[i] = unicode.ToLower(r)
		}
		for i := 0; i <= len(lowerRunes)-qLen; {
			matched := true
			for j := 0; j < qLen; j++ {
				if lowerRunes[i+j] != qRunes[j] {
					matched = false
					break
				}
			}
			if matched {
				colStart := uniseg.StringWidth(string(lRunes[:i]))
				colEnd := colStart + uniseg.StringWidth(string(lRunes[i:i+qLen]))
				m.matches = append(m.matches, pagerMatch{
					line:     lineIdx,
					colStart: colStart,
					colEnd:   colEnd,
				})
				i += qLen
				continue
			}
			i++
		}
	}
	if len(m.matches) > 0 {
		m.scrollToMatch(0)
	}
}

func (m *tablePagerModel) scrollToMatch(idx int) {
	if idx < 0 || idx >= len(m.matches) {
		return
	}
	match := m.matches[idx]
	pinned := m.pinnedLines()
	bh := m.bodyHeight()
	if match.line >= pinned {
		bodyLine := match.line - pinned
		if bodyLine < m.yOffset || bodyLine >= m.yOffset+bh {
			m.yOffset = bodyLine - bh/3
		}
	}
	pinW := m.pinnedCols()
	scrollW := m.width - pinW
	if match.colStart >= pinW && (match.colStart < pinW+m.xOffset || match.colEnd > pinW+m.xOffset+scrollW) {
		m.xOffset = (match.colStart - pinW) - scrollW/4
	}
	m.clamp()
}

func (m *tablePagerModel) highlightVisibleLine(visible string, lineIdx, startCol int) string {
	if m.query == "" || len(m.matches) == 0 {
		return visible
	}
	hasMatch := false
	for _, match := range m.matches {
		if match.line == lineIdx {
			hasMatch = true
			break
		}
	}
	if !hasMatch {
		return visible
	}
	var b strings.Builder
	col := startCol
	state := 0 // 0=none, 1=other match, 2=active match
	g := uniseg.NewGraphemes(visible)
	for g.Next() {
		cluster := g.Str()
		cw := uniseg.StringWidth(cluster)
		nextState := 0
		for idx, match := range m.matches {
			if match.line != lineIdx {
				continue
			}
			if col < match.colEnd && col+cw > match.colStart {
				if idx == m.matchIdx {
					nextState = 2
					break
				}
				nextState = 1
			}
		}
		if nextState != state {
			if state != 0 {
				b.WriteString("\x1b[0m")
			}
			switch nextState {
			case 1:
				b.WriteString("\x1b[30;43m")
			case 2:
				b.WriteString("\x1b[7;1m")
			}
			state = nextState
		}
		b.WriteString(cluster)
		col += cw
	}
	if state != 0 {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

func (m *tablePagerModel) HandleKey(k tuiKey) bool {
	if !m.searching && !m.filtering && !m.saving {
		m.statusMsg = ""
	}
	if m.detailOpen {
		return m.handleDetailKey(k)
	}
	if m.filtering {
		switch k.kind {
		case keyCtrlC:
			return true
		case keyEsc:
			m.filtering = false
			m.filterQuery = ""
			m.rebuildFilteredTable()
		case keyEnter:
			m.filtering = false
			m.rebuildFilteredTable()
		case keyBackspace:
			if m.filterQuery != "" {
				_, size := utf8.DecodeLastRuneInString(m.filterQuery)
				m.filterQuery = m.filterQuery[:len(m.filterQuery)-size]
			}
		case keyCtrlU:
			m.filterQuery = ""
		case keyCtrlW:
			if m.filterQuery != "" {
				m.filterQuery = string(deleteLastWordRunes([]rune(m.filterQuery)))
			}
		case keyRune:
			m.filterQuery += string(k.r)
		}
		return false
	}
	if m.saving {
		switch k.kind {
		case keyCtrlC:
			return true
		case keyEsc:
			m.saving = false
			m.savePath = ""
		case keyEnter:
			path := m.savePath
			m.saving = false
			m.savePath = ""
			if err := m.saveVisible(path); err != nil {
				m.statusMsg = "Save failed: " + err.Error()
			} else {
				m.statusMsg = fmt.Sprintf("Saved %d rows to %s", len(m.filteredIdx), path)
			}
		case keyBackspace:
			if m.savePath != "" {
				_, size := utf8.DecodeLastRuneInString(m.savePath)
				m.savePath = m.savePath[:len(m.savePath)-size]
			}
		case keyCtrlU:
			m.savePath = ""
		case keyCtrlW:
			if m.savePath != "" {
				m.savePath = string(deleteLastWordRunes([]rune(m.savePath)))
			}
		case keyRune:
			m.savePath += string(k.r)
		}
		return false
	}
	if m.searching {
		switch k.kind {
		case keyCtrlC:
			return true
		case keyEsc:
			m.searching = false
			m.query = ""
			m.matches = nil
			m.matchIdx = 0
		case keyEnter:
			m.searching = false
		case keyBackspace:
			if m.query != "" {
				_, size := utf8.DecodeLastRuneInString(m.query)
				m.query = m.query[:len(m.query)-size]
				m.recomputeMatches()
			}
		case keyCtrlU:
			m.query = ""
			m.recomputeMatches()
		case keyCtrlW:
			if m.query != "" {
				m.query = string(deleteLastWordRunes([]rune(m.query)))
				m.recomputeMatches()
			}
		case keyRune:
			m.query += string(k.r)
			m.recomputeMatches()
		}
		return false
	}
	switch k.kind {
	case keyCtrlC:
		return true
	case keyEsc:
		if m.query != "" {
			m.query = ""
			m.matches = nil
			m.matchIdx = 0
			return false
		}
		if m.filterQuery != "" {
			m.filterQuery = ""
			m.rebuildFilteredTable()
			return false
		}
		return true
	case keyEnter:
		if len(m.filteredIdx) > 0 && len(m.rawCols) > 0 {
			m.detailRow = m.topVisibleRow()
			m.detailYOffset = 0
			m.detailOpen = true
		}
		return false
	case keyTab:
		m.xOffset = m.nextColOffset()
	case keyShiftTab:
		m.xOffset = m.prevColOffset()
	case keyMouseUp:
		m.yOffset -= 3
	case keyMouseDown:
		m.yOffset += 3
	case keyUp:
		m.yOffset--
	case keyDown:
		m.yOffset++
	case keyLeft:
		m.xOffset -= 4
	case keyRight:
		m.xOffset += 4
	case keyPgUp:
		m.yOffset -= max(1, m.bodyHeight()-1)
	case keyPgDown:
		m.yOffset += max(1, m.bodyHeight()-1)
	case keyHome:
		m.yOffset = 0
	case keyEnd:
		m.yOffset = m.maxYOffset()
	case keyRune:
		switch k.r {
		case 'q', 'Q':
			return true
		case 'v':
			if len(m.filteredIdx) > 0 && len(m.rawCols) > 0 {
				m.detailRow = m.topVisibleRow()
				m.detailYOffset = 0
				m.detailOpen = true
			}
			return false
		case '&':
			m.filtering = true
			return false
		case 's':
			m.saving = true
			return false
		case 'y':
			m.copyRow(m.topVisibleRow())
			return false
		case 'w':
			m.xOffset = m.nextColOffset()
		case 'b':
			m.xOffset = m.prevColOffset()
		case '/':
			m.searching = true
			m.query = ""
			m.matches = nil
			m.matchIdx = 0
			return false
		case 'n':
			if len(m.matches) > 0 {
				m.matchIdx = (m.matchIdx + 1) % len(m.matches)
				m.scrollToMatch(m.matchIdx)
			}
			return false
		case 'N':
			if len(m.matches) > 0 {
				m.matchIdx = (m.matchIdx - 1 + len(m.matches)) % len(m.matches)
				m.scrollToMatch(m.matchIdx)
			}
			return false
		case 'k':
			m.yOffset--
		case 'j':
			m.yOffset++
		case 'h':
			m.xOffset -= 4
		case 'l':
			m.xOffset += 4
		case 'g':
			m.yOffset = 0
		case 'G':
			m.yOffset = m.maxYOffset()
		case '0', '^':
			m.xOffset = 0
		case '$':
			m.xOffset = m.maxXOffset()
		case 'H':
			m.xOffset -= max(4, (m.width-m.pinnedCols())/2)
		case 'L':
			m.xOffset += max(4, (m.width-m.pinnedCols())/2)
		case ' ':
			m.yOffset += max(1, m.bodyHeight()-1)
		}
	}
	m.clamp()
	return false
}

func (m *tablePagerModel) handleDetailKey(k tuiKey) bool {
	lines := m.detailLines()
	maxOff := max(0, len(lines)-m.viewHeight())
	switch k.kind {
	case keyCtrlC:
		return true
	case keyEsc, keyEnter:
		m.detailOpen = false
		return false
	case keyUp, keyMouseUp:
		m.detailYOffset = max(0, m.detailYOffset-1)
	case keyDown, keyMouseDown:
		m.detailYOffset = min(maxOff, m.detailYOffset+1)
	case keyPgUp:
		m.detailYOffset = max(0, m.detailYOffset-max(1, m.viewHeight()-1))
	case keyPgDown:
		m.detailYOffset = min(maxOff, m.detailYOffset+max(1, m.viewHeight()-1))
	case keyLeft:
		if m.detailRow > 0 {
			m.detailRow--
			m.detailYOffset = 0
		}
	case keyRight:
		if m.detailRow < len(m.filteredIdx)-1 {
			m.detailRow++
			m.detailYOffset = 0
		}
	case keyRune:
		switch k.r {
		case 'q', 'Q', 'v':
			m.detailOpen = false
			return false
		case 'k':
			m.detailYOffset = max(0, m.detailYOffset-1)
		case 'j':
			m.detailYOffset = min(maxOff, m.detailYOffset+1)
		case 'h':
			if m.detailRow > 0 {
				m.detailRow--
				m.detailYOffset = 0
			}
		case 'l':
			if m.detailRow < len(m.filteredIdx)-1 {
				m.detailRow++
				m.detailYOffset = 0
			}
		case 'y':
			m.copyRow(m.detailRow)
		}
	}
	return false
}

func (m *tablePagerModel) renderDetail() string {
	var b strings.Builder
	origNo := m.detailRow + 1
	if m.detailRow >= 0 && m.detailRow < len(m.filteredIdx) {
		origNo = m.filteredIdx[m.detailRow] + 1
	}
	header := fmt.Sprintf("Row %d (%d/%d) • %d cols", origNo, m.detailRow+1, len(m.filteredIdx), len(m.rawCols))
	fmt.Fprintf(&b, "\x1b[1;36m%s\x1b[0m\n", sliceColumns(header, 0, m.width))
	lines := m.detailLines()
	vh := m.viewHeight()
	maxOff := max(0, len(lines)-vh)
	off := min(max(0, m.detailYOffset), maxOff)
	end := min(len(lines), off+vh)
	for i := off; i < end; i++ {
		b.WriteString(sliceColumns(lines[i], 0, m.width))
		b.WriteByte('\n')
	}
	for i := end - off; i < vh; i++ {
		b.WriteByte('\n')
	}
	footer := "↑↓/jk scroll • ←→/hl prev/next row • y copy • Esc/Enter/q back"
	if m.statusMsg != "" {
		footer = m.statusMsg + " • " + footer
	}
	fmt.Fprintf(&b, "\x1b[2m%s\x1b[0m", sliceColumns(footer, 0, m.width))
	return b.String()
}

func (m *tablePagerModel) Render() string {
	if m.detailOpen {
		return m.renderDetail()
	}
	var b strings.Builder
	var header string
	if m.hasElapsed {
		header = fmt.Sprintf("dbh results (%d rows × %d cols) • Time: %s; %d rows returned",
			m.rows, m.cols, m.elapsed.Round(time.Microsecond), m.rows)
	} else {
		header = fmt.Sprintf("dbh results (%d rows × %d cols)", m.rows, m.cols)
	}
	if m.filterQuery != "" {
		header += fmt.Sprintf(" • filter:%q", m.filterQuery)
	}
	if m.truncated {
		header += " • truncated"
	}
	fmt.Fprintf(&b, "\x1b[1;36m%s\x1b[0m\n", sliceColumns(header, 0, m.width))
	vh := m.viewHeight()
	pinned := m.pinnedLines()
	bh := m.bodyHeight()
	for i := 0; i < pinned; i++ {
		b.WriteString(m.renderLine(i))
		b.WriteByte('\n')
	}
	start := min(len(m.lines), pinned+m.yOffset)
	end := min(len(m.lines), start+bh)
	for i := start; i < end; i++ {
		b.WriteString(m.renderLine(i))
		b.WriteByte('\n')
	}
	for i := pinned + (end - start); i < vh; i++ {
		b.WriteByte('\n')
	}
	if m.filtering {
		prompt := "&" + m.filterQuery
		hint := sliceColumns(" • Enter filter • Esc clear", 0, max(0, m.width-uniseg.StringWidth(prompt)))
		fmt.Fprintf(&b, "%s\x1b[2m%s\x1b[0m", sliceColumns(prompt, 0, m.width), hint)
		return b.String()
	}
	if m.saving {
		prompt := "Save (.csv/.tsv/.json): " + m.savePath
		hint := sliceColumns(" • Enter save • Esc cancel", 0, max(0, m.width-uniseg.StringWidth(prompt)))
		fmt.Fprintf(&b, "%s\x1b[2m%s\x1b[0m", sliceColumns(prompt, 0, m.width), hint)
		return b.String()
	}
	if m.searching {
		status := ""
		if m.query != "" {
			if len(m.matches) == 0 {
				status = " (no matches)"
			} else {
				status = fmt.Sprintf(" (%d/%d)", m.matchIdx+1, len(m.matches))
			}
		}
		prompt := "/" + m.query + status
		hint := sliceColumns(" • Enter confirm • Esc cancel", 0, max(0, m.width-uniseg.StringWidth(prompt)))
		fmt.Fprintf(&b, "%s\x1b[2m%s\x1b[0m", sliceColumns(prompt, 0, m.width), hint)
		return b.String()
	}
	pct := formatScrollPercent(m.yOffset, m.maxYOffset())
	var footer string
	if m.statusMsg != "" {
		footer = fmt.Sprintf("%s • %s", pct, m.statusMsg)
	} else if m.query != "" {
		status := "(no matches)"
		if len(m.matches) > 0 {
			status = fmt.Sprintf("(%d/%d)", m.matchIdx+1, len(m.matches))
		}
		footer = fmt.Sprintf("%s • col +%d • /%s %s • n/N next/prev • Esc clear • q back", pct, m.xOffset, m.query, status)
	} else {
		footer = fmt.Sprintf("%s • col +%d • ↑↓←→ scroll • Tab/0/$ cols • Enter detail • / search • & filter • s save • y copy • q back", pct, m.xOffset)
	}
	fmt.Fprintf(&b, "\x1b[2m%s\x1b[0m", sliceColumns(footer, 0, m.width))
	return b.String()
}

func (m *tablePagerModel) renderLine(lineIdx int) string {
	if pinW := m.pinnedCols(); pinW > 0 {
		left := m.highlightVisibleLine(sliceColumns(m.lines[lineIdx], 0, pinW), lineIdx, 0)
		right := m.highlightVisibleLine(sliceColumns(m.lines[lineIdx], pinW+m.xOffset, m.width-pinW), lineIdx, pinW+m.xOffset)
		return left + right
	}
	return m.highlightVisibleLine(sliceColumns(m.lines[lineIdx], m.xOffset, m.width), lineIdx, m.xOffset)
}

func prependRowNumbers(columns []string, rows [][]string) ([]string, [][]string) {
	numberedCols := make([]string, len(columns)+1)
	numberedCols[0] = "No"
	copy(numberedCols[1:], columns)

	numberedRows := make([][]string, len(rows))
	for i, row := range rows {
		r := make([]string, len(row)+1)
		r[0] = strconv.Itoa(i + 1)
		copy(r[1:], row)
		numberedRows[i] = r
	}
	return numberedCols, numberedRows
}

// runTablePager opens an interactive scrollable viewport for wide or tall table
// results, preserving horizontal width without forced column wrapping.
func runTablePager(columns []string, rows [][]string, elapsed time.Duration, truncated bool) error {
	numberedCols, numberedRows := prependRowNumbers(columns, rows)
	var buf bytes.Buffer
	width := max(tableNaturalWidth(numberedCols, numberedRows), 80)
	if err := renderTable(&buf, numberedCols, numberedRows, width); err != nil {
		return err
	}
	m := newTablePagerModel(buf.String(), len(rows), len(columns))
	m.pinFirstColumn()
	m.setSourceData(columns, rows)
	m.elapsed = elapsed
	m.hasElapsed = true
	m.truncated = truncated
	return runAltScreenTUI(m)
}
