package dbh

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/benenen/dbh/internal/database"
)

const (
	defaultTableMaxCellWidth = 200
	shortBinaryLimit         = 32
	vectorSummaryThreshold   = 8
)

func (s *Session) effectiveMaxCellWidth(format string) int {
	if s.MaxCellWidthSet {
		return s.MaxCellWidth
	}
	if format == "table" {
		return defaultTableMaxCellWidth
	}
	return 0
}

func formatCellValue(v any, maxWidth int) (text string, jsonVal any) {
	if v == nil {
		return "NULL", nil
	}
	if b, ok := v.(database.Binary); ok {
		if maxWidth > 0 && len(b) > shortBinaryLimit {
			summary := fmt.Sprintf("<binary %s, 0x%s...>", formatByteSize(len(b)), hex.EncodeToString(b[:8]))
			return summary, summary
		}
		return b.String(), b
	}
	raw := fmt.Sprint(v)
	if maxWidth > 0 {
		if summary, ok := summarizeVector(raw); ok {
			return summary, summary
		}
		if truncated, ok := truncateText(raw, maxWidth); ok {
			return truncated, truncated
		}
	}
	return raw, v
}

func formatByteSize(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

func truncateText(s string, maxWidth int) (string, bool) {
	if maxWidth <= 0 {
		return s, false
	}
	runes := []rune(s)
	if len(runes) <= maxWidth {
		return s, false
	}
	sizeLabel := fmt.Sprintf("%d chars", len(runes))
	if len(s) >= 1024 {
		sizeLabel = formatByteSize(len(s))
	}
	return string(runes[:maxWidth]) + fmt.Sprintf("... (%s)", sizeLabel), true
}

// summarizeVector collapses bracketed numeric arrays longer than vectorSummaryThreshold
// into a compact "<vector[N]: [a, b, c, ...]>" representation.
func summarizeVector(s string) (string, bool) {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) < 3 || trimmed[0] != '[' || trimmed[len(trimmed)-1] != ']' {
		return "", false
	}
	inner := strings.TrimSpace(trimmed[1 : len(trimmed)-1])
	if inner == "" {
		return "", false
	}
	parts := strings.Split(inner, ",")
	if len(parts) <= vectorSummaryThreshold {
		return "", false
	}
	var head [3]string
	for i, part := range parts {
		token := strings.TrimSpace(part)
		f, err := strconv.ParseFloat(token, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return "", false
		}
		if i < 3 {
			head[i] = token
		}
	}
	return fmt.Sprintf("<vector[%d]: [%s, %s, %s, ...]>", len(parts), head[0], head[1], head[2]), true
}

func summarizeJSONValue(v any, maxWidth int) any {
	if maxWidth <= 0 || v == nil {
		return v
	}
	switch val := v.(type) {
	case database.Binary:
		if len(val) > shortBinaryLimit {
			return fmt.Sprintf("<binary %s, 0x%s...>", formatByteSize(len(val)), hex.EncodeToString(val[:8]))
		}
		return val
	case string:
		if summary, ok := summarizeVector(val); ok {
			return summary
		}
		if truncated, ok := truncateText(val, maxWidth); ok {
			return truncated
		}
		return val
	case []any:
		if summary, ok := summarizeNumericSlice(val); ok {
			return summary
		}
		out := make([]any, len(val))
		for i, elem := range val {
			out[i] = summarizeJSONValue(elem, maxWidth)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, item := range val {
			out[k] = summarizeJSONValue(item, maxWidth)
		}
		return out
	default:
		return v
	}
}

func summarizeNumericSlice(items []any) (string, bool) {
	if len(items) <= vectorSummaryThreshold {
		return "", false
	}
	var head [3]string
	for i, item := range items {
		var f float64
		switch n := item.(type) {
		case float64:
			f = n
		case float32:
			f = float64(n)
		case int:
			f = float64(n)
		case int64:
			f = float64(n)
		case json.Number:
			parsed, err := n.Float64()
			if err != nil {
				return "", false
			}
			f = parsed
		default:
			return "", false
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return "", false
		}
		if i < 3 {
			head[i] = fmt.Sprint(item)
		}
	}
	return fmt.Sprintf("<vector[%d]: [%s, %s, %s, ...]>", len(items), head[0], head[1], head[2]), true
}
