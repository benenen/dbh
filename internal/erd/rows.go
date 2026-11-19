package erd

import "strings"

// Rows are query results shared by the table, CSV, JSON and TSV encoders.
type Rows struct {
	Columns []string
	Values  [][]any
}

func (m *Model) TableRows() Rows {
	counts := map[string]int{}
	for _, r := range m.Relations {
		counts[r.From]++
		if r.To != r.From {
			counts[r.To]++
		}
	}
	rows := Rows{Columns: []string{"table", "columns", "relations", "note"}}
	for _, t := range m.Tables {
		rows.Values = append(rows.Values, []any{t.Display(), len(t.Columns), counts[t.Display()], t.Note})
	}
	return rows
}

// RelationRows lists relations touching table (any when empty) from source
// (any when empty).
func (m *Model) RelationRows(table, source string) Rows {
	rows := Rows{Columns: []string{"name", "from_table", "from_columns", "relation", "to_table", "to_columns", "on_delete", "on_update", "source"}}
	for _, r := range m.Relations {
		if table != "" && r.From != table && r.To != table {
			continue
		}
		if source != "" && r.Source != source {
			continue
		}
		rows.Values = append(rows.Values, []any{r.Name, r.From, strings.Join(r.FromColumns, ", "), r.Op,
			r.To, strings.Join(r.ToColumns, ", "), r.OnDelete, r.OnUpdate, r.Source})
	}
	return rows
}

func PathRows(steps []Step) Rows {
	rows := Rows{Columns: []string{"step", "from_table", "from_columns", "to_table", "to_columns", "source"}}
	for i, s := range steps {
		rows.Values = append(rows.Values, []any{i + 1, s.From, strings.Join(s.FromColumns, ", "), s.To,
			strings.Join(s.ToColumns, ", "), s.Relation.Source})
	}
	return rows
}
