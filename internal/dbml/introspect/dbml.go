package introspect

import (
	"fmt"
	"strings"
)

// GenerateDBML converts a DBSchema to a DBML string.
// databaseType, if non-empty, is emitted as a Project block header.
// When normalize is true, column types are mapped to canonical DBML equivalents.
func GenerateDBML(schema *DBSchema, databaseType string, normalize bool) string {
	var sb strings.Builder

	if databaseType != "" {
		fmt.Fprintf(&sb, "Project {\n  database_type: '%s'\n}\n\n", dbmlString(databaseType))
	}

	for _, t := range schema.Tables {
		writeTable(&sb, t, normalize)
		sb.WriteByte('\n')
	}

	for _, e := range schema.Enums {
		writeEnum(&sb, e)
		sb.WriteByte('\n')
	}

	for _, fk := range schema.FKs {
		writeRef(&sb, fk)
	}

	return sb.String()
}

func writeTable(sb *strings.Builder, t *Table, normalize bool) {
	sb.WriteString("Table ")
	sb.WriteString(tableIdent(t.Schema, t.Name))
	sb.WriteString(" {\n")

	for _, col := range t.Columns {
		var dbmlType string
		var isSerial bool
		if normalize {
			dbmlType, isSerial = normalizedDBMLType(col)
		} else {
			dbmlType = col.Type
		}
		attrs := columnAttrs(col, isSerial)
		if len(attrs) > 0 {
			fmt.Fprintf(sb, "  %q %s [%s]\n", col.Name, dbmlType, strings.Join(attrs, ", "))
		} else {
			fmt.Fprintf(sb, "  %q %s\n", col.Name, dbmlType)
		}
	}

	var indexLines []string

	for _, c := range t.Constraints {
		if len(c.Columns) <= 1 && !(c.IsPrimary && len(c.Columns) == 1 &&
			c.Name != "" && c.Name != t.Name+"_pkey" && c.Name != "PRIMARY") {
			continue
		}
		var attrs []string
		if c.IsPrimary {
			attrs = append(attrs, "pk")
		} else {
			attrs = append(attrs, "unique")
		}
		if c.Name != "" {
			attrs = append(attrs, fmt.Sprintf("name: %s", formatDBMLString(c.Name)))
		}
		indexLines = append(indexLines,
			fmt.Sprintf("    (%s) [%s]", quotedCols(c.Columns), strings.Join(attrs, ", ")))
	}

	for _, idx := range t.Indexes {
		keys := make([]string, len(idx.Columns))
		for i, c := range idx.Columns {
			if i < len(idx.Exprs) && idx.Exprs[i] {
				keys[i] = "`" + c + "`"
			} else {
				keys[i] = fmt.Sprintf("%q", c)
			}
		}
		colStr := "(" + strings.Join(keys, ", ") + ")"
		var attrs []string
		if idx.IsUnique {
			attrs = append(attrs, "unique")
		}
		if idx.Name != "" {
			attrs = append(attrs, fmt.Sprintf("name: %s", formatDBMLString(idx.Name)))
		}
		if typ := strings.ToLower(idx.Type); typ != "" && typ != "btree" {
			attrs = append(attrs, "type: "+typ)
		}
		line := "    " + colStr
		if len(attrs) > 0 {
			line += fmt.Sprintf(" [%s]", strings.Join(attrs, ", "))
		}
		indexLines = append(indexLines, line)
	}

	if len(indexLines) > 0 {
		sb.WriteString("\n  indexes {\n")
		for _, line := range indexLines {
			sb.WriteString(line + "\n")
		}
		sb.WriteString("  }\n")
	}

	if t.Comment != "" {
		fmt.Fprintf(sb, "\n  Note: %s\n", formatDBMLString(t.Comment))
	}

	sb.WriteString("}\n")
}

func normalizedDBMLType(col *Column) (string, bool) {
	base := toDBMLType(col.Type)
	if col.IsIncrement {
		if base == "bigint" {
			return "bigserial", true
		}
		return "serial", true
	}
	return base, false
}

func toDBMLType(sqlType string) string {
	lower := strings.ToLower(strings.TrimSpace(sqlType))
	base := lower
	args := ""
	if i := strings.Index(lower, "("); i >= 0 {
		base = strings.TrimSpace(lower[:i])
		args = lower[i:]
	}
	switch base {
	case "int", "integer", "mediumint":
		return "int"
	case "tinyint":
		if args == "(1)" {
			return "bool"
		}
		return "int"
	case "bigint":
		return "bigint"
	case "smallint":
		return "smallint"
	case "float", "real":
		return "float"
	case "double", "double precision":
		return "double"
	case "decimal", "numeric":
		return "decimal" + args
	case "varchar", "character varying":
		return "varchar" + args
	case "text", "tinytext", "mediumtext", "longtext":
		return "text"
	case "char", "character":
		return "char" + args
	case "date", "time", "datetime", "uuid", "json", "jsonb", "serial", "bigserial":
		return base
	case "timestamp", "timestamp without time zone", "timestamp with time zone":
		return "timestamp"
	case "bool", "boolean":
		return "bool"
	case "blob", "tinyblob", "mediumblob", "longblob", "binary", "bytea":
		return "binary"
	case "varbinary":
		return "varbinary" + args
	}
	return sqlType
}

func columnAttrs(col *Column, isSerial bool) []string {
	var attrs []string
	if col.IsPrimaryKey {
		attrs = append(attrs, "pk")
	}
	if col.IsIncrement && !isSerial {
		attrs = append(attrs, "increment")
	}
	if col.IsUnique {
		attrs = append(attrs, "unique")
	}
	if !col.IsNullable && !isSerial {
		attrs = append(attrs, "not null")
	}
	if col.Default != nil {
		switch col.DefaultType {
		case "number":
			attrs = append(attrs, "default: "+*col.Default)
		case "string":
			attrs = append(attrs, fmt.Sprintf("default: '%s'", dbmlString(sqlStringLiteral(*col.Default))))
		case "boolean":
			attrs = append(attrs, "default: "+strings.ToLower(*col.Default))
		case "expression":
			attrs = append(attrs, fmt.Sprintf("default: `%s`", *col.Default))
		}
	}
	if col.Comment != "" {
		attrs = append(attrs, "note: "+formatDBMLString(col.Comment))
	}
	return attrs
}

func writeEnum(sb *strings.Builder, e *Enum) {
	sb.WriteString("Enum ")
	sb.WriteString(tableIdent(e.Schema, e.Name))
	sb.WriteString(" {\n")
	for _, v := range e.Values {
		fmt.Fprintf(sb, "  %s\n", v)
	}
	sb.WriteString("}\n")
}

func writeRef(sb *strings.Builder, fk *ForeignKey) {
	fromTbl := tableIdent(fk.Schema, fk.TableName)
	toTbl := tableIdent(fk.RefSchema, fk.RefTable)

	var fromRef, toRef string
	if len(fk.Columns) == 1 {
		fromRef = fmt.Sprintf("%s.%q", fromTbl, fk.Columns[0])
		toRef = fmt.Sprintf("%s.%q", toTbl, refCol(fk.RefColumns, 0))
	} else {
		fromRef = fmt.Sprintf("%s.(%s)", fromTbl, quotedCols(fk.Columns))
		toRef = fmt.Sprintf("%s.(%s)", toTbl, quotedCols(fk.RefColumns))
	}

	var refAttrs []string
	if d := normaliseAction(fk.OnDelete); d != "" {
		refAttrs = append(refAttrs, "delete: "+d)
	}
	if u := normaliseAction(fk.OnUpdate); u != "" {
		refAttrs = append(refAttrs, "update: "+u)
	}

	if len(refAttrs) > 0 {
		fmt.Fprintf(sb, "Ref: %s > %s [%s]\n", fromRef, toRef, strings.Join(refAttrs, ", "))
	} else {
		fmt.Fprintf(sb, "Ref: %s > %s\n", fromRef, toRef)
	}
}

func tableIdent(schema, name string) string {
	if schema != "" && schema != "public" {
		return fmt.Sprintf("%q.%q", schema, name)
	}
	return fmt.Sprintf("%q", name)
}

func quotedCols(cols []string) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = fmt.Sprintf("%q", c)
	}
	return strings.Join(parts, ", ")
}

func refCol(cols []string, i int) string {
	if i < len(cols) {
		return cols[i]
	}
	return ""
}

func normaliseAction(action string) string {
	switch strings.ToUpper(action) {
	case "NO ACTION", "RESTRICT", "":
		return ""
	default:
		return strings.ToLower(action)
	}
}

// sqlStringLiteral returns the value of a quoted SQL default such as
// 'it”s'::character varying, dropping any cast. Unquoted values are returned unchanged.
func sqlStringLiteral(def string) string {
	if !strings.HasPrefix(def, "'") {
		return strings.Trim(def, "\"")
	}
	var b strings.Builder
	for i := 1; i < len(def); i++ {
		if def[i] == '\'' {
			if i+1 < len(def) && def[i+1] == '\'' {
				b.WriteByte('\'')
				i++
				continue
			}
			return b.String()
		}
		b.WriteByte(def[i])
	}
	return b.String()
}

func dbmlString(s string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s)
}

func formatDBMLString(s string) string {
	if strings.ContainsAny(s, "\n\r") {
		escaped := strings.ReplaceAll(s, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, `'''`, `\'\'\'`)
		return "'''" + escaped + "'''"
	}
	return "'" + dbmlString(s) + "'"
}
