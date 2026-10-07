package dbh

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"github.com/rivo/uniseg"
	"golang.org/x/term"
)

func tableWidth(out io.Writer) int {
	if file, ok := out.(*os.File); ok {
		if width, _, err := term.GetSize(int(file.Fd())); err == nil && width >= 20 {
			return width
		}
	}
	return 100
}

// renderTable wraps whole grapheme clusters and separates logical records even
// when a cell contains newlines or extends over several physical terminal lines.
func renderTable(out io.Writer, columns []string, rows [][]string, width int) error {
	widths := make([]int, len(columns))
	minimumWidths := make([]int, len(columns))
	for i, column := range columns {
		widths[i] = max(1, cellWidth(column))
	}
	for _, row := range rows {
		for i, value := range row {
			widths[i] = max(widths[i], cellWidth(value))
		}
	}
	minimum := 1
	for i := range widths {
		widths[i] = min(widths[i], max(2, width-4))
		minimumWidths[i] = min(2, widths[i])
		minimum += minimumWidths[i] + 3
	}
	if minimum > width && len(columns) > 2 {
		if len(rows) == 0 {
			fields := make([][]string, len(columns))
			for i, column := range columns {
				fields[i] = []string{column, ""}
			}
			return renderTable(out, []string{"Column", "Value"}, fields, width)
		}
		for i, row := range rows {
			if _, err := fmt.Fprintf(out, "Row %d:\n", i+1); err != nil {
				return err
			}
			fields := make([][]string, len(columns))
			for j, column := range columns {
				fields[j] = []string{column, row[j]}
			}
			if err := renderTable(out, []string{"Column", "Value"}, fields, width); err != nil {
				return err
			}
		}
		return nil
	}
	for {
		total := 1
		largest := -1
		for i, w := range widths {
			total += w + 3
			if w > minimumWidths[i] && (largest == -1 || w > widths[largest]) {
				largest = i
			}
		}
		if total <= width || largest < 0 {
			break
		}
		widths[largest]--
	}
	rule := func(left, middle, right string) error {
		parts := make([]string, len(widths))
		for i, w := range widths {
			parts[i] = strings.Repeat("─", w+2)
		}
		_, err := fmt.Fprintln(out, left+strings.Join(parts, middle)+right)
		return err
	}
	record := func(values []string) error {
		cells := make([][]string, len(values))
		height := 1
		for i, value := range values {
			cells[i] = wrapCell(value, widths[i])
			height = max(height, len(cells[i]))
		}
		for line := 0; line < height; line++ {
			parts := make([]string, len(cells))
			for i, cell := range cells {
				text := ""
				if line < len(cell) {
					text = cell[line]
				}
				parts[i] = " " + text + strings.Repeat(" ", max(0, widths[i]-uniseg.StringWidth(text))) + " "
			}
			if _, err := fmt.Fprintln(out, "│"+strings.Join(parts, "│")+"│"); err != nil {
				return err
			}
		}
		return nil
	}
	if err := rule("┌", "┬", "┐"); err != nil {
		return err
	}
	if err := record(columns); err != nil {
		return err
	}
	for _, row := range rows {
		if err := rule("├", "┼", "┤"); err != nil {
			return err
		}
		if err := record(row); err != nil {
			return err
		}
	}
	return rule("└", "┴", "┘")
}

func cleanCell(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var clean strings.Builder
	for _, char := range text {
		if char == '\n' || !unicode.IsControl(char) {
			clean.WriteRune(char)
		} else {
			fmt.Fprintf(&clean, "\\x%02x", char)
		}
	}
	return clean.String()
}
func cellWidth(text string) int {
	width := 0
	for _, line := range strings.Split(cleanCell(text), "\n") {
		width = max(width, uniseg.StringWidth(line))
	}
	return width
}
func wrapCell(text string, width int) []string {
	var lines []string
	for _, physical := range strings.Split(cleanCell(text), "\n") {
		var line strings.Builder
		used := 0
		graphemes := uniseg.NewGraphemes(physical)
		for graphemes.Next() {
			cluster := graphemes.Str()
			size := uniseg.StringWidth(cluster)
			if used+size > width && line.Len() > 0 {
				lines = append(lines, line.String())
				line.Reset()
				used = 0
			}
			line.WriteString(cluster)
			used += size
		}
		lines = append(lines, line.String())
	}
	return lines
}
