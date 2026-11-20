package dbh

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

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
	if len(columns) == 0 || mode == "off" {
		return false
	}
	w, h, ok := terminalSize(out)
	if !ok {
		return false
	}
	if mode == "on" {
		return true
	}
	// auto: page when the table exceeds terminal height or is a multi-row wide table.
	estimatedLines := len(rows)*2 + 3
	if estimatedLines > h-4 {
		return true
	}
	return len(rows) >= 3 && tableNaturalWidth(columns, rows) > w
}

type tablePagerModel struct {
	lines    []string
	maxWidth int
	rows     int
	cols     int
	width    int
	height   int
	yOffset  int
	xOffset  int
}

func newTablePagerModel(content string, rows, cols int) *tablePagerModel {
	trimmed := strings.TrimSuffix(content, "\n")
	lines := strings.Split(trimmed, "\n")
	maxW := 0
	for _, l := range lines {
		maxW = max(maxW, uniseg.StringWidth(l))
	}
	return &tablePagerModel{
		lines:    lines,
		maxWidth: maxW,
		rows:     rows,
		cols:     cols,
		width:    80,
		height:   24,
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

func (m *tablePagerModel) maxYOffset() int {
	return max(0, len(m.lines)-m.viewHeight())
}

func (m *tablePagerModel) maxXOffset() int {
	return max(0, m.maxWidth-m.width)
}

func (m *tablePagerModel) clamp() {
	m.yOffset = min(max(0, m.yOffset), m.maxYOffset())
	m.xOffset = min(max(0, m.xOffset), m.maxXOffset())
}

func (m *tablePagerModel) HandleKey(k tuiKey) bool {
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
		m.yOffset -= max(1, m.viewHeight()-1)
	case keyPgDown:
		m.yOffset += max(1, m.viewHeight()-1)
	case keyHome:
		m.yOffset = 0
	case keyEnd:
		m.yOffset = m.maxYOffset()
	case keyRune:
		switch k.r {
		case 'q', 'Q':
			return true
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
			m.yOffset += max(1, m.viewHeight()-1)
		}
	}
	m.clamp()
	return false
}

func (m *tablePagerModel) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\x1b[1;36mdbh results (%d rows × %d cols)\x1b[0m\n", m.rows, m.cols)
	vh := m.viewHeight()
	end := min(len(m.lines), m.yOffset+vh)
	for i := m.yOffset; i < end; i++ {
		b.WriteString(sliceColumns(m.lines[i], m.xOffset, m.width))
		b.WriteByte('\n')
	}
	for i := end - m.yOffset; i < vh; i++ {
		b.WriteByte('\n')
	}
	pct := formatScrollPercent(m.yOffset, m.maxYOffset())
	fmt.Fprintf(&b, "\x1b[2m%s • col +%d • ↑↓←→/hjkl scroll • PgUp/PgDn page • g/G top/bottom • q/esc back\x1b[0m", pct, m.xOffset)
	return b.String()
}

// runTablePager opens an interactive scrollable viewport for wide or tall table
// results, preserving horizontal width without forced column wrapping.
func runTablePager(columns []string, rows [][]string) error {
	var buf bytes.Buffer
	width := max(tableNaturalWidth(columns, rows), 80)
	if err := renderTable(&buf, columns, rows, width); err != nil {
		return err
	}
	m := newTablePagerModel(buf.String(), len(rows), len(columns))
	return runAltScreenTUI(m)
}
