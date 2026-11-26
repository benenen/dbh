package dbh

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/rivo/uniseg"
)

var errFuzzyHistory = errors.New("fuzzy history")

// fuzzyScore scores how well text matches query (higher is better). Every
// whitespace-separated token in query must match either as a substring or
// ordered subsequence (case-insensitive).
func fuzzyScore(text, query string) (int, bool) {
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return 1, true
	}
	lower := strings.ToLower(text)
	total := 0
	for _, token := range strings.Fields(query) {
		if idx := strings.Index(lower, token); idx >= 0 {
			if idx == 0 {
				total += 120
			} else {
				total += 100
			}
			continue
		}
		// Fallback to ordered subsequence matching.
		tRunes := []rune(token)
		pos := 0
		consecutive := 0
		subScore := 0
		for _, r := range lower {
			if pos < len(tRunes) && r == tRunes[pos] {
				pos++
				consecutive++
				subScore += 5 + consecutive*2
			} else {
				consecutive = 0
			}
		}
		if pos != len(tRunes) {
			return 0, false
		}
		total += subScore
	}
	return total, true
}

// filterHistory returns matching entries ordered by relevance, with newer
// entries winning ties.
func filterHistory(entries []string, query string) []string {
	type scored struct {
		text  string
		score int
		order int // 0 is newest
	}
	var matches []scored
	for i := len(entries) - 1; i >= 0; i-- {
		if sc, ok := fuzzyScore(entries[i], query); ok {
			matches = append(matches, scored{text: entries[i], score: sc, order: len(entries) - 1 - i})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].order < matches[j].order
	})
	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = m.text
	}
	return out
}

func summarizeHistoryLine(sql string, maxWidth int) string {
	lines := strings.Split(strings.ReplaceAll(sql, "\r\n", "\n"), "\n")
	parts := make([]string, 0, len(lines))
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	flat := strings.Join(parts, " ↵ ")
	if maxWidth <= 4 || uniseg.StringWidth(flat) <= maxWidth {
		return flat
	}
	var b strings.Builder
	w := 0
	g := uniseg.NewGraphemes(flat)
	for g.Next() {
		cluster := g.Str()
		cw := uniseg.StringWidth(cluster)
		if w+cw > maxWidth-1 {
			b.WriteRune('…')
			break
		}
		b.WriteString(cluster)
		w += cw
	}
	return b.String()
}

type historyPickerModel struct {
	all       []string
	filtered  []string
	query     []rune
	cursor    int
	offset    int
	width     int
	height    int
	selected  string
	confirmed bool
}

func newHistoryPickerModel(entries []string, initialQuery string) *historyPickerModel {
	m := &historyPickerModel{
		all:    entries,
		query:  []rune(initialQuery),
		width:  80,
		height: 24,
	}
	m.applyFilter()
	return m
}

func (m *historyPickerModel) Resize(width, height int) {
	m.width = width
	m.height = height
}

func (m *historyPickerModel) applyFilter() {
	m.filtered = filterHistory(m.all, string(m.query))
	if m.cursor >= len(m.filtered) {
		m.cursor = max(0, len(m.filtered)-1)
	}
	m.offset = 0
}

func (m *historyPickerModel) listHeight() int {
	preview := max(5, m.height/3)
	h := m.height - 4 - preview
	if h < 3 {
		h = 3
	}
	return h
}

func (m *historyPickerModel) previewHeight() int {
	h := m.height - 4 - m.listHeight()
	if h < 2 {
		h = 2
	}
	return h
}

func (m *historyPickerModel) HandleKey(k tuiKey) bool {
	switch k.kind {
	case keyCtrlC, keyEsc:
		return true
	case keyEnter:
		if len(m.filtered) > 0 && m.cursor < len(m.filtered) {
			m.selected = m.filtered[m.cursor]
			m.confirmed = true
		}
		return true
	case keyUp, keyCtrlP:
		if m.cursor > 0 {
			m.cursor--
			if m.cursor < m.offset {
				m.offset = m.cursor
			}
		}
	case keyDown, keyCtrlN:
		if m.cursor < len(m.filtered)-1 {
			m.cursor++
			lh := m.listHeight()
			if m.cursor >= m.offset+lh {
				m.offset = m.cursor - lh + 1
			}
		}
	case keyPgUp:
		step := max(1, m.listHeight()-1)
		m.cursor = max(0, m.cursor-step)
		if m.cursor < m.offset {
			m.offset = m.cursor
		}
	case keyPgDown:
		step := max(1, m.listHeight()-1)
		m.cursor = min(max(0, len(m.filtered)-1), m.cursor+step)
		lh := m.listHeight()
		if m.cursor >= m.offset+lh {
			m.offset = m.cursor - lh + 1
		}
	case keyBackspace:
		if len(m.query) > 0 {
			m.query = m.query[:len(m.query)-1]
			m.applyFilter()
		}
	case keyCtrlU:
		if len(m.query) > 0 {
			m.query = nil
			m.applyFilter()
		}
	case keyCtrlW:
		if len(m.query) > 0 {
			m.query = deleteLastWordRunes(m.query)
			m.applyFilter()
		}
	case keyRune:
		m.query = append(m.query, k.r)
		m.applyFilter()
	}
	return false
}

func (m *historyPickerModel) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\x1b[1;36mSQL history\x1b[0m  \x1b[2m(%d/%d)\x1b[0m\n", len(m.filtered), len(m.all))
	if len(m.query) == 0 {
		b.WriteString("> \x1b[2mType to filter SQL history...\x1b[0m\n")
	} else {
		fmt.Fprintf(&b, "> %s\n", summarizeHistoryLine(string(m.query), max(10, m.width-3)))
	}

	lh := m.listHeight()
	if len(m.filtered) == 0 {
		b.WriteString("\x1b[2m  No matching history entries\x1b[0m\n")
		for i := 1; i < lh; i++ {
			b.WriteByte('\n')
		}
	} else {
		end := min(len(m.filtered), m.offset+lh)
		for i := m.offset; i < end; i++ {
			summary := summarizeHistoryLine(m.filtered[i], max(10, m.width-4))
			if i == m.cursor {
				b.WriteString("\x1b[1;7m▸ " + summary + "\x1b[0m")
			} else {
				b.WriteString("  " + summary)
			}
			b.WriteByte('\n')
		}
		for i := end - m.offset; i < lh; i++ {
			b.WriteByte('\n')
		}
	}

	b.WriteString("\x1b[2m" + strings.Repeat("─", max(10, m.width)) + "\x1b[0m\n")

	ph := m.previewHeight()
	previewLines := 0
	if len(m.filtered) > 0 && m.cursor < len(m.filtered) {
		lines := strings.Split(strings.ReplaceAll(m.filtered[m.cursor], "\r\n", "\n"), "\n")
		for i, line := range lines {
			if i >= ph {
				break
			}
			b.WriteString(summarizeHistoryLine(line, max(10, m.width-2)))
			b.WriteByte('\n')
			previewLines++
		}
	}
	for i := previewLines; i < ph; i++ {
		b.WriteByte('\n')
	}

	footer := sliceColumns("↑↓/Ctrl-P/N move • Ctrl-W word • Enter select • Esc/Ctrl-C cancel", 0, m.width)
	b.WriteString("\x1b[2m" + footer + "\x1b[0m")
	return b.String()
}

// pickHistory launches an interactive fuzzy history picker with multi-line SQL
// preview and returns the selected statement.
func pickHistory(entries []string, initialQuery string) (string, bool, error) {
	m := newHistoryPickerModel(entries, initialQuery)
	if err := runAltScreenTUI(m); err != nil {
		return "", false, err
	}
	if !m.confirmed {
		return "", false, nil
	}
	return m.selected, true, nil
}
