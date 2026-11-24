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
