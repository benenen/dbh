// Package sqlite implements SQLite connection and metadata behavior.
package sqlite

import (
	"context"
	"fmt"
	"strings"

	"github.com/benenen/dbh/internal/database"
	_ "modernc.org/sqlite"
)

type Driver struct{}

var _ database.Driver = Driver{}

func (Driver) Open(ctx context.Context, dsn string, dial database.Dial) (database.Connection, error) {
	if dial != nil {
		return nil, fmt.Errorf("proxies are not supported for SQLite")
	}
	return database.OpenSQL(ctx, "sqlite", dsn)
}
func (Driver) Syntax() database.Syntax { return database.Syntax{BracketIdentifiers: true} }

func (Driver) SwitchDatabase(context.Context, database.Connection, string, string, database.Dial) (database.Connection, error) {
	return nil, fmt.Errorf("database switching is not supported for SQLite")
}
func (Driver) Databases(context.Context, database.Connection) ([]string, error) {
	return nil, fmt.Errorf("database listing is not supported for SQLite")
}
func (Driver) Tables(ctx context.Context, conn database.Connection) ([]string, error) {
	return database.Names(ctx, conn, database.Query{Text: "SELECT name FROM sqlite_master WHERE type IN ('table','view') UNION SELECT name FROM sqlite_temp_master WHERE type IN ('table','view') ORDER BY name"})
}
func (Driver) Columns(ctx context.Context, conn database.Connection, table string) ([]string, error) {
	return database.Names(ctx, conn, database.Query{Text: `SELECT name FROM pragma_table_info(?)`, Args: []any{table}})
}
func (Driver) ColumnDetails(ctx context.Context, conn database.Connection, table string) (database.Query, error) {
	schema, name, err := resolveTable(ctx, conn, table)
	if err != nil {
		return database.Query{}, err
	}
	return database.Query{Text: `SELECT c.name AS name, c.type AS type,
   CASE WHEN c."notnull" OR (c.pk > 0 AND (t.wr OR t.strict OR
    (c.type = 'INTEGER' COLLATE NOCASE AND NOT EXISTS
     (SELECT 1 FROM pragma_index_list(?, ?) WHERE origin = 'pk'))))
    THEN 'NO' ELSE 'YES' END AS nullable,
   c.dflt_value AS "default", c.pk AS primary_key_position, NULL AS comment
   FROM pragma_table_xinfo(?, ?) c
   JOIN pragma_table_list t ON t.name = ? AND t.schema = ? ORDER BY c.cid`, Args: []any{name, schema, name, schema, name, schema}}, nil
}
func (Driver) Indexes(ctx context.Context, conn database.Connection, table string) (database.Query, error) {
	schema, name, err := resolveTable(ctx, conn, table)
	if err != nil {
		return database.Query{}, err
	}
	// Schema comes from SQLite metadata and is quoted as an identifier.
	quotedSchema := `"` + strings.ReplaceAll(schema, `"`, `""`) + `"`
	return database.Query{Text: `SELECT i.name AS name, i."unique" AS is_unique,
   i.origin = 'pk' AS is_primary, i.partial AS is_partial,
   (SELECT group_concat(label, ', ') FROM
    (SELECT COALESCE(x.name, '<expression>') || CASE WHEN x.desc THEN ' DESC' ELSE '' END AS label
     FROM pragma_index_xinfo(i.name, ?) x WHERE x.key = 1 ORDER BY x.seqno)) AS columns,
   m.sql AS definition FROM pragma_index_list(?, ?) i
   LEFT JOIN ` + quotedSchema + `.sqlite_schema m ON m.name = i.name AND m.type = 'index'
   ORDER BY i.name`, Args: []any{schema, name, schema}}, nil
}

// resolveTable returns the stored schema and table name; SQLite identifiers ignore case.
func resolveTable(ctx context.Context, conn database.Connection, table string) (schema, name string, err error) {
	var candidates [][2]string
	// \tables lists names unquoted, so a dotted table name is tried whole first.
	if !strings.ContainsAny(table, "\"`[") {
		candidates = append(candidates, [2]string{"", table})
	}
	switch parts := database.SplitIdentifier(table); len(parts) {
	case 1:
		candidates = append(candidates, [2]string{"", parts[0]})
	case 2:
		candidates = append(candidates, [2]string{parts[0], parts[1]})
	}
	for _, c := range candidates {
		schemas, err := database.Names(ctx, conn, database.Query{Text: `SELECT schema FROM pragma_table_list
   WHERE name = ? COLLATE NOCASE AND (? = '' OR schema = ? COLLATE NOCASE)
   ORDER BY CASE schema WHEN 'temp' THEN 0 WHEN 'main' THEN 1 ELSE 2 END`, Args: []any{c[1], c[0], c[0]}})
		if err != nil {
			return "", "", err
		}
		if len(schemas) == 0 {
			continue
		}
		names, err := database.Names(ctx, conn, database.Query{Text: `SELECT name FROM pragma_table_list
   WHERE schema = ? AND name = ? COLLATE NOCASE`, Args: []any{schemas[0], c[1]}})
		if err != nil {
			return "", "", err
		}
		if len(names) > 0 {
			return schemas[0], names[0], nil
		}
	}
	return "", "", fmt.Errorf("table or view %q does not exist", table)
}
