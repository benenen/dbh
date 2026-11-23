package erd

import (
	"path"
	"strings"
)

// Rows are query results shared by the table, CSV, JSON and TSV encoders.
type Rows struct {
	Columns []string
	Values  [][]any
}

func (m *Model) TableRows() Rows {
	return m.TableRowsFiltered("", false, false)
}

// TableRowsFiltered lists tables with optional substring/glob match on table
// name or note, optional isolated-only filter (0 relations), and optional
// source models column.
func (m *Model) TableRowsFiltered(match string, isolated, withModel bool) Rows {
	counts := map[string]int{}
	for _, r := range m.Relations {
		counts[r.From]++
		if r.To != r.From {
			counts[r.To]++
		}
	}
	cols := []string{"table", "columns", "relations", "note"}
	if withModel {
		cols = append(cols, "models")
	}
	rows := Rows{Columns: cols}
	pattern := strings.TrimSpace(match)
	lowerPattern := strings.ToLower(pattern)
	isGlob := strings.ContainsAny(pattern, "*?[")
	for _, t := range m.Tables {
		display := t.Display()
		relCount := counts[display]
		if isolated && relCount > 0 {
			continue
		}
		if pattern != "" && !tableMatches(display, t.Name, t.Note, lowerPattern, isGlob) {
			continue
		}
		vals := []any{display, len(t.Columns), relCount, t.Note}
		if withModel {
			vals = append(vals, strings.Join(t.Models, ", "))
		}
		rows.Values = append(rows.Values, vals)
	}
	return rows
}

func tableMatches(display, name, note, lowerPattern string, isGlob bool) bool {
	ld := strings.ToLower(display)
	ln := strings.ToLower(name)
	if isGlob {
		if ok, _ := path.Match(lowerPattern, ld); ok {
			return true
		}
		if ok, _ := path.Match(lowerPattern, ln); ok {
			return true
		}
		return false
	}
	return strings.Contains(ld, lowerPattern) || strings.Contains(strings.ToLower(note), lowerPattern)
}

// ColumnRows lists one row per column of tables (all tables when empty),
// optionally filtered by a substring/glob match on the column name or a
// substring match on its note, with an optional source models column.
func (m *Model) ColumnRows(tables []string, match string, withModel bool) Rows {
	cols := []string{"table", "column", "type", "pk", "not_null", "unique", "default", "note"}
	if withModel {
		cols = append(cols, "models")
	}
	rows := Rows{Columns: cols}
	include := map[string]bool{}
	for _, t := range tables {
		include[t] = true
	}
	pattern := strings.TrimSpace(match)
	lowerPattern := strings.ToLower(pattern)
	isGlob := strings.ContainsAny(pattern, "*?[")
	for _, t := range m.Tables {
		display := t.Display()
		if len(include) > 0 && !include[display] {
			continue
		}
		for _, c := range t.Columns {
			note := columnNote(c)
			if pattern != "" && !tableMatches(c.Name, c.Name, note, lowerPattern, isGlob) {
				continue
			}
			notNull := c.PK || (c.NotNull != nil && *c.NotNull)
			vals := []any{display, c.Name, columnType(c), c.PK, notNull, c.Unique, columnDefault(c), note}
			if withModel {
				vals = append(vals, strings.Join(t.Models, ", "))
			}
			rows.Values = append(rows.Values, vals)
		}
	}
	return rows
}

// RelationRows lists relations touching table (any when empty) from source
// (any when empty).
func (m *Model) RelationRows(table, source string) Rows {
	rows := Rows{Columns: []string{"name", "from_table", "from_columns", "relation", "to_table", "to_columns", "condition", "on_delete", "on_update", "source", "evidence"}}
	for _, r := range m.Relations {
		if table != "" && r.From != table && r.To != table {
			continue
		}
		if source != "" && r.Source != source {
			continue
		}
		rows.Values = append(rows.Values, []any{r.Name, r.From, strings.Join(r.FromColumns, ", "), r.Op,
			r.To, strings.Join(r.ToColumns, ", "), r.Condition, r.OnDelete, r.OnUpdate, r.Source, r.Evidence})
	}
	return rows
}

func PathRows(steps []Step) Rows {
	rows := Rows{Columns: []string{"step", "from_table", "from_columns", "to_table", "to_columns", "condition", "source", "evidence"}}
	for i, s := range steps {
		rows.Values = append(rows.Values, []any{i + 1, s.From, strings.Join(s.FromColumns, ", "), s.To,
			strings.Join(s.ToColumns, ", "), s.Relation.Condition, s.Relation.Source, s.Relation.Evidence})
	}
	return rows
}

// PathRowsMulti formats multiple candidate shortest paths with a 1-based path index.
func PathRowsMulti(paths [][]Step) Rows {
	rows := Rows{Columns: []string{"path", "step", "from_table", "from_columns", "to_table", "to_columns", "condition", "source", "evidence"}}
	for pi, steps := range paths {
		for si, s := range steps {
			rows.Values = append(rows.Values, []any{pi + 1, si + 1, s.From, strings.Join(s.FromColumns, ", "), s.To,
				strings.Join(s.ToColumns, ", "), s.Relation.Condition, s.Relation.Source, s.Relation.Evidence})
		}
	}
	return rows
}
