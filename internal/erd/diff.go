package erd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/benenen/dbh/internal/dbml"
)

// DiffEntry describes one schema difference between two models.
type DiffEntry struct {
	Kind   string // "table", "column", "relation"
	Change string // "added", "removed", "modified"
	Target string
	Detail string
}

// ModelDiff holds the structural changes between an older and newer model.
type ModelDiff struct {
	Entries          []DiffEntry
	TablesAdded      int
	TablesRemoved    int
	TablesModified   int
	RelationsAdded   int
	RelationsRemoved int
}

// HasChanges reports whether any structural difference was found.
func (d ModelDiff) HasChanges() bool {
	return len(d.Entries) > 0
}

// Summary returns a concise single-line summary such as
// "+2 tables, -1 table, ~1 modified, +1 declared ref".
func (d ModelDiff) Summary() string {
	if !d.HasChanges() {
		return "no schema changes"
	}
	var parts []string
	if d.TablesAdded > 0 {
		parts = append(parts, pluralCount("+", d.TablesAdded, "table", "tables"))
	}
	if d.TablesRemoved > 0 {
		parts = append(parts, pluralCount("-", d.TablesRemoved, "table", "tables"))
	}
	if d.TablesModified > 0 {
		parts = append(parts, fmt.Sprintf("~%d modified", d.TablesModified))
	}
	if d.RelationsAdded > 0 {
		parts = append(parts, pluralCount("+", d.RelationsAdded, "declared ref", "declared refs"))
	}
	if d.RelationsRemoved > 0 {
		parts = append(parts, pluralCount("-", d.RelationsRemoved, "declared ref", "declared refs"))
	}
	return strings.Join(parts, ", ")
}

func pluralCount(prefix string, n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%s%d %s", prefix, n, singular)
	}
	return fmt.Sprintf("%s%d %s", prefix, n, plural)
}

// Rows returns the diff entries as structured table rows.
func (d ModelDiff) Rows() Rows {
	rows := Rows{Columns: []string{"kind", "change", "target", "detail"}}
	for _, e := range d.Entries {
		rows.Values = append(rows.Values, []any{e.Kind, e.Change, e.Target, e.Detail})
	}
	return rows
}

// DiffModels compares tables, columns, and declared relations between oldModel
// and newModel.
func DiffModels(oldModel, newModel *Model) ModelDiff {
	var diff ModelDiff
	oldTables := map[string]Table{}
	newTables := map[string]Table{}
	if oldModel != nil {
		for _, t := range oldModel.Tables {
			oldTables[t.Display()] = t
		}
	}
	if newModel != nil {
		for _, t := range newModel.Tables {
			newTables[t.Display()] = t
		}
	}

	allNames := make([]string, 0, len(oldTables)+len(newTables))
	seen := map[string]bool{}
	for name := range oldTables {
		seen[name] = true
		allNames = append(allNames, name)
	}
	for name := range newTables {
		if !seen[name] {
			allNames = append(allNames, name)
		}
	}
	sort.Strings(allNames)

	for _, name := range allNames {
		ot, inOld := oldTables[name]
		nt, inNew := newTables[name]
		switch {
		case !inOld && inNew:
			diff.TablesAdded++
			diff.Entries = append(diff.Entries, DiffEntry{
				Kind:   "table",
				Change: "added",
				Target: name,
				Detail: fmt.Sprintf("%d columns", len(nt.Columns)),
			})
		case inOld && !inNew:
			diff.TablesRemoved++
			diff.Entries = append(diff.Entries, DiffEntry{
				Kind:   "table",
				Change: "removed",
				Target: name,
				Detail: fmt.Sprintf("%d columns", len(ot.Columns)),
			})
		default:
			colEntries := diffColumns(name, ot.Columns, nt.Columns)
			if len(colEntries) > 0 {
				diff.TablesModified++
				diff.Entries = append(diff.Entries, colEntries...)
			}
		}
	}

	oldRels := declaredRelationMap(oldModel)
	newRels := declaredRelationMap(newModel)
	relKeys := make([]string, 0, len(oldRels)+len(newRels))
	seenRel := map[string]bool{}
	for k := range oldRels {
		seenRel[k] = true
		relKeys = append(relKeys, k)
	}
	for k := range newRels {
		if !seenRel[k] {
			relKeys = append(relKeys, k)
		}
	}
	sort.Strings(relKeys)

	for _, k := range relKeys {
		or, inOld := oldRels[k]
		nr, inNew := newRels[k]
		switch {
		case !inOld && inNew:
			diff.RelationsAdded++
			diff.Entries = append(diff.Entries, DiffEntry{
				Kind:   "relation",
				Change: "added",
				Target: nr.describe(),
				Detail: relationNotes(&nr),
			})
		case inOld && !inNew:
			diff.RelationsRemoved++
			diff.Entries = append(diff.Entries, DiffEntry{
				Kind:   "relation",
				Change: "removed",
				Target: or.describe(),
				Detail: relationNotes(&or),
			})
		}
	}

	return diff
}

func declaredRelationMap(m *Model) map[string]Relation {
	out := map[string]Relation{}
	if m == nil {
		return out
	}
	for _, r := range m.Relations {
		if r.Source != SourceDeclared {
			continue
		}
		out[r.key()] = r
	}
	return out
}

func diffColumns(table string, oldCols, newCols []dbml.Column) []DiffEntry {
	oldMap := make(map[string]dbml.Column, len(oldCols))
	newMap := make(map[string]dbml.Column, len(newCols))
	var names []string
	seen := map[string]bool{}
	for _, c := range oldCols {
		oldMap[c.Name] = c
		seen[c.Name] = true
		names = append(names, c.Name)
	}
	for _, c := range newCols {
		newMap[c.Name] = c
		if !seen[c.Name] {
			names = append(names, c.Name)
		}
	}
	sort.Strings(names)

	var entries []DiffEntry
	for _, colName := range names {
		oc, inOld := oldMap[colName]
		nc, inNew := newMap[colName]
		target := table + "." + colName
		switch {
		case !inOld && inNew:
			entries = append(entries, DiffEntry{
				Kind:   "column",
				Change: "added",
				Target: target,
				Detail: columnSignature(nc),
			})
		case inOld && !inNew:
			entries = append(entries, DiffEntry{
				Kind:   "column",
				Change: "removed",
				Target: target,
				Detail: columnSignature(oc),
			})
		default:
			osig, nsig := columnSignature(oc), columnSignature(nc)
			if osig != nsig {
				entries = append(entries, DiffEntry{
					Kind:   "column",
					Change: "modified",
					Target: target,
					Detail: osig + " -> " + nsig,
				})
			}
		}
	}
	return entries
}

func columnSignature(c dbml.Column) string {
	sig := columnType(c)
	if flags := columnFlags(c); flags != "" {
		sig += " [" + flags + "]"
	}
	return sig
}

// RenderMigrationDDL produces a best-effort draft migration SQL script for the diff.
func (d ModelDiff) RenderMigrationDDL(oldModel, newModel *Model, dialect string) string {
	if !d.HasChanges() {
		return "-- No schema changes.\n"
	}
	oldTables := map[string]Table{}
	newTables := map[string]Table{}
	if oldModel != nil {
		for _, t := range oldModel.Tables {
			oldTables[t.Display()] = t
		}
	}
	if newModel != nil {
		for _, t := range newModel.Tables {
			newTables[t.Display()] = t
		}
	}

	var stmts []string
	stmts = append(stmts, "-- Draft migration DDL (review before executing)")
	for _, e := range d.Entries {
		switch {
		case e.Kind == "table" && e.Change == "added":
			if t, ok := newTables[e.Target]; ok {
				stmts = append(stmts, renderCreateTableDDL(t, dialect))
			}
		case e.Kind == "table" && e.Change == "removed":
			stmts = append(stmts, fmt.Sprintf("-- Review before dropping:\nDROP TABLE %s;", quoteDiffIdent(dialect, e.Target)))
		case e.Kind == "column" && e.Change == "added":
			tblName, colName, ok := splitDiffTarget(e.Target)
			if !ok {
				continue
			}
			if t, found := newTables[tblName]; found {
				if col, colFound := findDiffColumn(t.Columns, colName); colFound {
					stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s;", quoteDiffIdent(dialect, tblName), renderColumnDef(col, dialect)))
				}
			}
		case e.Kind == "column" && e.Change == "removed":
			tblName, colName, ok := splitDiffTarget(e.Target)
			if !ok {
				continue
			}
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", quoteDiffIdent(dialect, tblName), quoteDiffIdent(dialect, colName)))
		case e.Kind == "column" && e.Change == "modified":
			tblName, colName, ok := splitDiffTarget(e.Target)
			if !ok {
				continue
			}
			t, found := newTables[tblName]
			if !found {
				continue
			}
			col, colFound := findDiffColumn(t.Columns, colName)
			if !colFound {
				continue
			}
			switch strings.ToLower(dialect) {
			case "mysql":
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s;", quoteDiffIdent(dialect, tblName), renderColumnDef(col, dialect)))
			case "sqlite":
				stmts = append(stmts, fmt.Sprintf("-- SQLite requires table rebuild to alter column %s: %s", e.Target, e.Detail))
			default:
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s;", quoteDiffIdent(dialect, tblName), quoteDiffIdent(dialect, colName), columnType(col)))
			}
		case e.Kind == "relation" && e.Change == "added":
			stmts = append(stmts, fmt.Sprintf("-- Added relation: %s", e.Target))
		case e.Kind == "relation" && e.Change == "removed":
			stmts = append(stmts, fmt.Sprintf("-- Removed relation: %s", e.Target))
		}
	}
	return strings.Join(stmts, "\n\n") + "\n"
}

func renderCreateTableDDL(t Table, dialect string) string {
	lines := make([]string, 0, len(t.Columns))
	for _, c := range t.Columns {
		lines = append(lines, "  "+renderColumnDef(c, dialect))
	}
	return fmt.Sprintf("CREATE TABLE %s (\n%s\n);", quoteDiffIdent(dialect, t.Display()), strings.Join(lines, ",\n"))
}

func renderColumnDef(c dbml.Column, dialect string) string {
	def := quoteDiffIdent(dialect, c.Name) + " " + columnType(c)
	if c.NotNull != nil && *c.NotNull {
		def += " NOT NULL"
	}
	if d := columnDefault(c); d != "" {
		def += " DEFAULT " + d
	}
	if c.PK {
		def += " PRIMARY KEY"
	}
	return def
}

func splitDiffTarget(target string) (table, col string, ok bool) {
	idx := strings.LastIndex(target, ".")
	if idx <= 0 || idx >= len(target)-1 {
		return "", "", false
	}
	return target[:idx], target[idx+1:], true
}

func findDiffColumn(cols []dbml.Column, name string) (dbml.Column, bool) {
	for _, c := range cols {
		if c.Name == name {
			return c, true
		}
	}
	return dbml.Column{}, false
}

func quoteDiffIdent(dialect, name string) string {
	parts := strings.Split(name, ".")
	for i, part := range parts {
		if strings.EqualFold(dialect, "mysql") {
			parts[i] = "`" + strings.ReplaceAll(part, "`", "``") + "`"
		} else {
			parts[i] = `"` + strings.ReplaceAll(part, `"`, `""`) + `"`
		}
	}
	return strings.Join(parts, ".")
}
