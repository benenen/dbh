package erd

import (
	_ "embed"
	"encoding/json"
	"html"
	"io"
	"strings"
)

//go:embed template.html
var htmlTemplate string

type htmlModel struct {
	Name      string         `json:"name"`
	Tables    []htmlTable    `json:"tables"`
	Relations []htmlRelation `json:"relations"`
}

type htmlTable struct {
	Name    string       `json:"name"`
	Note    string       `json:"note,omitempty"`
	Models  []string     `json:"models,omitempty"`
	Indexes []string     `json:"indexes,omitempty"`
	Columns []htmlColumn `json:"columns"`
}

type htmlColumn struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	PK        bool   `json:"pk,omitempty"`
	Unique    bool   `json:"unique,omitempty"`
	NotNull   bool   `json:"notNull,omitempty"`
	Increment bool   `json:"increment,omitempty"`
	Default   string `json:"default,omitempty"`
	Note      string `json:"note,omitempty"`
}

type htmlRelation struct {
	Name        string   `json:"name,omitempty"`
	From        string   `json:"from"`
	FromColumns []string `json:"fromColumns"`
	Op          string   `json:"op"`
	To          string   `json:"to"`
	ToColumns   []string `json:"toColumns"`
	Condition   string   `json:"condition,omitempty"`
	OnDelete    string   `json:"onDelete,omitempty"`
	OnUpdate    string   `json:"onUpdate,omitempty"`
	Source      string   `json:"source"`
}

// RenderHTML writes a self-contained, offline interactive HTML ER diagram.
func RenderHTML(out io.Writer, m *Model, include map[string]bool) error {
	payload := htmlModel{
		Name:      m.Name,
		Tables:    make([]htmlTable, 0, len(m.Tables)),
		Relations: make([]htmlRelation, 0, len(m.Relations)),
	}
	for _, t := range m.Tables {
		name := t.Display()
		if include != nil && !include[name] {
			continue
		}
		cols := make([]htmlColumn, 0, len(t.Columns))
		for _, c := range t.Columns {
			cols = append(cols, htmlColumn{
				Name:      c.Name,
				Type:      columnType(c),
				PK:        c.PK,
				Unique:    c.Unique,
				NotNull:   c.NotNull != nil && *c.NotNull && !c.PK,
				Increment: c.Increment != nil && *c.Increment,
				Default:   columnDefault(c),
				Note:      columnNote(c),
			})
		}
		indexes := make([]string, 0, len(t.Indexes))
		for _, idx := range t.Indexes {
			indexes = append(indexes, formatIndexLine(idx))
		}
		payload.Tables = append(payload.Tables, htmlTable{
			Name:    name,
			Note:    t.Note,
			Models:  t.Models,
			Indexes: indexes,
			Columns: cols,
		})
	}
	for _, r := range m.Relations {
		if include != nil && (!include[r.From] || !include[r.To]) {
			continue
		}
		payload.Relations = append(payload.Relations, htmlRelation{
			Name:        r.Name,
			From:        r.From,
			FromColumns: r.FromColumns,
			Op:          r.Op,
			To:          r.To,
			ToColumns:   r.ToColumns,
			Condition:   r.Condition,
			OnDelete:    r.OnDelete,
			OnUpdate:    r.OnUpdate,
			Source:      r.Source,
		})
	}
	rawJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	title := m.Name
	if title == "" {
		title = DefaultModel
	}
	rendered := strings.NewReplacer(
		"__ERD_TITLE__", html.EscapeString(title),
		"__ERD_DATA__", string(rawJSON),
	).Replace(htmlTemplate)
	_, err = io.WriteString(out, rendered)
	return err
}
