package erd

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/mevdschee/dbml-tools/interpreter"
)

func columnType(c interpreter.Column) string {
	text := c.Type.TypeName
	if c.Type.SchemaName != nil {
		text = *c.Type.SchemaName + "." + text
	}
	switch args := c.Type.Args.(type) {
	case string:
		if args != "" {
			text += "(" + args + ")"
		}
	case []string:
		if len(args) > 0 {
			text += "(" + strings.Join(args, ",") + ")"
		}
	case []interface{}:
		parts := make([]string, len(args))
		for i, a := range args {
			parts[i] = fmt.Sprint(a)
		}
		if len(parts) > 0 {
			text += "(" + strings.Join(parts, ",") + ")"
		}
	}
	if c.Type.Suffix != "" {
		text += " " + c.Type.Suffix
	}
	return text
}

func columnFlags(c interpreter.Column) string {
	var flags []string
	if c.PK {
		flags = append(flags, "pk")
	}
	if c.Increment != nil && *c.Increment {
		flags = append(flags, "increment")
	}
	if c.Unique {
		flags = append(flags, "unique")
	}
	if c.NotNull != nil && *c.NotNull && !c.PK {
		flags = append(flags, "not null")
	}
	return strings.Join(flags, ", ")
}

func arrow(r *Relation, incoming bool) string {
	line := "──"
	if r.Source == SourceInferred {
		line = "┄┄"
	}
	switch {
	case r.Op == "<>":
		return "◀" + line + "▶"
	case r.Op == "-":
		return line + line[:len(line)/2]
	case incoming:
		return "◀" + line
	}
	return line + "▶"
}

func relationNotes(r *Relation) string {
	var notes []string
	switch r.Op {
	case "-":
		notes = append(notes, "one-to-one")
	case "<>":
		notes = append(notes, "many-to-many")
	}
	if r.Condition != "" {
		notes = append(notes, "when "+r.Condition)
	}
	if r.OnDelete != "" {
		notes = append(notes, "on delete "+r.OnDelete)
	}
	if r.OnUpdate != "" {
		notes = append(notes, "on update "+r.OnUpdate)
	}
	if r.Source == SourceInferred {
		notes = append(notes, SourceInferred)
	}
	return strings.Join(notes, ", ")
}

// RenderText prints each table with its columns, the relations it holds and
// the relations that point at it.
func RenderText(out io.Writer, m *Model, include map[string]bool) error {
	var buf strings.Builder
	if err := renderText(&buf, m, include); err != nil {
		return err
	}
	lines := strings.SplitAfter(buf.String(), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \n")
		if strings.HasSuffix(line, "\n") {
			lines[i] += "\n"
		}
	}
	_, err := io.WriteString(out, strings.Join(lines, ""))
	return err
}

func renderText(out io.Writer, m *Model, include map[string]bool) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	first := true
	for _, t := range m.Tables {
		name := t.Display()
		if include != nil && !include[name] {
			continue
		}
		if !first {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		first = false
		if _, err := fmt.Fprintln(w, name); err != nil {
			return err
		}
		outgoing := map[string][]*Relation{}
		var composite, incoming []*Relation
		for i := range m.Relations {
			r := &m.Relations[i]
			if r.From == name {
				if len(r.FromColumns) == 1 {
					outgoing[r.FromColumns[0]] = append(outgoing[r.FromColumns[0]], r)
				} else {
					composite = append(composite, r)
				}
			}
			if r.To == name && r.From != name {
				incoming = append(incoming, r)
			}
		}
		for _, c := range t.Columns {
			refs := outgoing[c.Name]
			if len(refs) == 0 {
				if _, err := fmt.Fprintf(w, "  %s\t%s\t%s\t\t\n", c.Name, columnType(c), columnFlags(c)); err != nil {
					return err
				}
				continue
			}
			for i, r := range refs {
				label, kind, flags := c.Name, columnType(c), columnFlags(c)
				if i > 0 {
					label, kind, flags = "", "", ""
				}
				if _, err := fmt.Fprintf(w, "  %s\t%s\t%s\t%s %s\t%s\n", label, kind, flags, arrow(r, false), endpointText(r.To, r.ToColumns), relationNotes(r)); err != nil {
					return err
				}
			}
		}
		for _, r := range composite {
			if _, err := fmt.Fprintf(w, "  (%s)\t\t\t%s %s\t%s\n", strings.Join(r.FromColumns, ", "), arrow(r, false), endpointText(r.To, r.ToColumns), relationNotes(r)); err != nil {
				return err
			}
		}
		for _, r := range incoming {
			if _, err := fmt.Fprintf(w, "  \t\t\t%s %s\t%s\n", arrow(r, true), endpointText(r.From, r.FromColumns), relationNotes(r)); err != nil {
				return err
			}
		}
	}
	return w.Flush()
}

func dotQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// RenderDot writes a Graphviz digraph; inferred relations are dashed.
func RenderDot(out io.Writer, m *Model, include map[string]bool) error {
	var b strings.Builder
	b.WriteString("digraph erd {\n  rankdir=LR;\n  node [shape=plaintext, fontname=\"Helvetica\"];\n  edge [fontname=\"Helvetica\", fontsize=10];\n")
	for _, t := range m.Tables {
		name := t.Display()
		if include != nil && !include[name] {
			continue
		}
		fmt.Fprintf(&b, "  %s [label=<<table border=\"0\" cellborder=\"1\" cellspacing=\"0\">\n    <tr><td bgcolor=\"lightgrey\"><b>%s</b></td></tr>\n", dotQuote(name), htmlEscape(name))
		for i, c := range t.Columns {
			text := c.Name + " " + columnType(c)
			if flags := columnFlags(c); flags != "" {
				text += " [" + flags + "]"
			}
			fmt.Fprintf(&b, "    <tr><td port=\"c%d\" align=\"left\">%s</td></tr>\n", i, htmlEscape(text))
		}
		b.WriteString("  </table>>];\n")
	}
	port := func(table string, columns []string) string {
		for _, t := range m.Tables {
			if t.Display() != table {
				continue
			}
			for i, c := range t.Columns {
				if len(columns) > 0 && c.Name == columns[0] {
					return fmt.Sprintf("%s:c%d", dotQuote(table), i)
				}
			}
		}
		return dotQuote(table)
	}
	for _, r := range m.Relations {
		if include != nil && (!include[r.From] || !include[r.To]) {
			continue
		}
		var attrs []string
		if r.Source == SourceInferred {
			attrs = append(attrs, "style=dashed")
		}
		switch r.Op {
		case "-":
			attrs = append(attrs, "arrowhead=none")
		case "<>":
			attrs = append(attrs, "dir=both")
		}
		if label := relationNotes(&r); label != "" {
			attrs = append(attrs, "label="+dotQuote(label))
		}
		fmt.Fprintf(&b, "  %s -> %s", port(r.From, r.FromColumns), port(r.To, r.ToColumns))
		if len(attrs) > 0 {
			fmt.Fprintf(&b, " [%s]", strings.Join(attrs, ", "))
		}
		b.WriteString(";\n")
	}
	b.WriteString("}\n")
	_, err := io.WriteString(out, b.String())
	return err
}

var (
	mermaidName = regexp.MustCompile(`[^A-Za-z0-9_-]`)
	mermaidType = regexp.MustCompile(`[^A-Za-z0-9_()\[\]-]`)
)

// RenderMermaid writes an erDiagram; inferred relations use dotted lines.
func RenderMermaid(out io.Writer, m *Model, include map[string]bool) error {
	var b strings.Builder
	b.WriteString("erDiagram\n")
	id := func(name string) string { return mermaidName.ReplaceAllString(name, "_") }
	entity := func(name string) string {
		if id(name) != name {
			return fmt.Sprintf("%s[%q]", id(name), name)
		}
		return name
	}
	for _, t := range m.Tables {
		name := t.Display()
		if include != nil && !include[name] {
			continue
		}
		fmt.Fprintf(&b, "  %s {\n", entity(name))
		for _, c := range t.Columns {
			fmt.Fprintf(&b, "    %s %s", mermaidType.ReplaceAllString(columnType(c), "_"), mermaidName.ReplaceAllString(c.Name, "_"))
			var keys []string
			if c.PK {
				keys = append(keys, "PK")
			}
			if c.Unique && !c.PK {
				keys = append(keys, "UK")
			}
			if len(keys) > 0 {
				b.WriteString(" " + strings.Join(keys, ","))
			}
			b.WriteString("\n")
		}
		b.WriteString("  }\n")
	}
	for _, r := range m.Relations {
		if include != nil && (!include[r.From] || !include[r.To]) {
			continue
		}
		line := "--"
		if r.Source == SourceInferred {
			line = ".."
		}
		left, right := "}o", "||"
		switch r.Op {
		case "-":
			left = "||"
		case "<>":
			right = "o{"
		}
		label := strings.Join(r.FromColumns, ", ")
		if r.Condition != "" {
			label = fmt.Sprintf("%s (when %s)", label, r.Condition)
		}
		fmt.Fprintf(&b, "  %s %s%s%s %s : %q\n", id(r.From), left, line, right, id(r.To), label)
	}
	_, err := io.WriteString(out, b.String())
	return err
}
