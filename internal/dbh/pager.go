package dbh

import (
	"bytes"
	"fmt"
	"io"
	"os"
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
	width          int
	height         int
	yOffset        int
	xOffset        int
	searching      bool
	query          string
	matches        []pagerMatch
	matchIdx       int
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
	}
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
		case keyRune:
			m.query += string(k.r)
			m.recomputeMatches()
		}
		return false
	}
	switch k.kind {
	case keyEsc, keyCtrlC:
		return true
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
		case ' ':
			m.yOffset += max(1, m.bodyHeight()-1)
		}
	}
	m.clamp()
	return false
}

func (m *tablePagerModel) Render() string {
	var b strings.Builder
	if m.hasElapsed {
		fmt.Fprintf(&b, "\x1b[1;36mdbh results (%d rows × %d cols) • Time: %s; %d rows returned\x1b[0m\n",
			m.rows, m.cols, m.elapsed.Round(time.Microsecond), m.rows)
	} else {
		fmt.Fprintf(&b, "\x1b[1;36mdbh results (%d rows × %d cols)\x1b[0m\n", m.rows, m.cols)
	}
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
	if m.searching {
		status := ""
		if m.query != "" {
			if len(m.matches) == 0 {
				status = " (no matches)"
			} else {
				status = fmt.Sprintf(" (%d/%d)", m.matchIdx+1, len(m.matches))
			}
		}
		fmt.Fprintf(&b, "/%s%s\x1b[2m • Enter confirm • Esc cancel\x1b[0m", m.query, status)
		return b.String()
	}
	pct := formatScrollPercent(m.yOffset, m.maxYOffset())
	if m.query != "" {
		status := "(no matches)"
		if len(m.matches) > 0 {
			status = fmt.Sprintf("(%d/%d)", m.matchIdx+1, len(m.matches))
		}
		fmt.Fprintf(&b, "\x1b[2m%s • col +%d • /%s %s • n/N next/prev • / search • q/esc back\x1b[0m", pct, m.xOffset, m.query, status)
		return b.String()
	}
	fmt.Fprintf(&b, "\x1b[2m%s • col +%d • ↑↓←→/hjkl scroll • / search • g/G top/bottom • q/esc back\x1b[0m", pct, m.xOffset)
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
func runTablePager(columns []string, rows [][]string, elapsed time.Duration) error {
	numberedCols, numberedRows := prependRowNumbers(columns, rows)
	var buf bytes.Buffer
	width := max(tableNaturalWidth(numberedCols, numberedRows), 80)
	if err := renderTable(&buf, numberedCols, numberedRows, width); err != nil {
		return err
	}
	m := newTablePagerModel(buf.String(), len(rows), len(columns))
	m.pinFirstColumn()
	m.elapsed = elapsed
	m.hasElapsed = true
	return runAltScreenTUI(m)
}
