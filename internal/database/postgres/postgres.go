// Package postgres implements PostgreSQL connection and metadata behavior.
package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/benenen/dbh/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

type Driver struct{}

var _ database.Driver = Driver{}
var _ database.DDLProvider = Driver{}

func (Driver) Open(ctx context.Context, dsn string, dial database.Dial) (database.Connection, error) {
	if dial == nil {
		return database.OpenSQL(ctx, "pgx", dsn)
	}
	return open(ctx, dsn, "", dial)
}
func (Driver) Syntax() database.Syntax {
	return database.Syntax{NestedComments: true, EscapeStringPrefix: true, DollarQuotes: true, FoldUnquotedIdentifiers: true}
}

func (Driver) SwitchDatabase(ctx context.Context, _ database.Connection, dsn, name string, dial database.Dial) (database.Connection, error) {
	return open(ctx, dsn, name, dial)
}
func open(ctx context.Context, dsn, name string, dial database.Dial) (database.Connection, error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("invalid PostgreSQL connection configuration")
	}
	if name != "" {
		config.Database = name
	}
	if dial != nil {
		config.DialFunc = pgconn.DialFunc(dial)
		// Hostnames resolve at the last proxy hop instead of locally.
		config.LookupFunc = func(_ context.Context, host string) ([]string, error) { return []string{host}, nil }
	}
	return database.ConnectSQL(ctx, stdlib.OpenDB(*config))
}
func (Driver) Databases(ctx context.Context, conn database.Connection) ([]string, error) {
	return database.Names(ctx, conn, database.Query{Text: "SELECT datname FROM pg_database ORDER BY datname"})
}
func (Driver) Tables(ctx context.Context, conn database.Connection) ([]string, error) {
	return database.Names(ctx, conn, database.Query{Text: `SELECT table_schema || '.' || table_name FROM information_schema.tables WHERE table_schema NOT IN ('pg_catalog','information_schema') ORDER BY 1`})
}
func (Driver) Columns(ctx context.Context, conn database.Connection, table string) ([]string, error) {
	parts := strings.SplitN(table, ".", 2)
	if len(parts) == 2 {
		return database.Names(ctx, conn, database.Query{Text: `SELECT column_name FROM information_schema.columns WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position`, Args: []any{parts[0], parts[1]}})
	}
	return database.Names(ctx, conn, database.Query{Text: `SELECT column_name FROM information_schema.columns WHERE table_name=$1 AND table_schema = ANY(current_schemas(false)) ORDER BY ordinal_position`, Args: []any{table}})
}
func (Driver) ColumnNames(ctx context.Context, conn database.Connection) ([]string, error) {
	return database.Names(ctx, conn, database.Query{Text: "SELECT DISTINCT column_name::text FROM information_schema.columns WHERE table_schema NOT IN ('pg_catalog','information_schema')"})
}
func (Driver) ColumnDetails(ctx context.Context, conn database.Connection, table string) (database.Query, error) {
	table, err := resolveTable(ctx, conn, table)
	if err != nil {
		return database.Query{}, err
	}
	return database.Query{Text: `SELECT a.attname AS name, format_type(a.atttypid, a.atttypmod) AS type,
   CASE WHEN a.attnotnull THEN 'NO' ELSE 'YES' END AS nullable,
   pg_get_expr(d.adbin, d.adrelid) AS "default",
   COALESCE((SELECT k.ordinality FROM pg_index i,
    unnest(i.indkey) WITH ORDINALITY k(attnum, ordinality)
    WHERE i.indrelid = a.attrelid AND i.indisprimary AND k.attnum = a.attnum), 0)
    AS primary_key_position,
   col_description(a.attrelid, a.attnum) AS comment
   FROM pg_attribute a LEFT JOIN pg_attrdef d
    ON d.adrelid = a.attrelid AND d.adnum = a.attnum
   WHERE a.attrelid = to_regclass($1) AND a.attnum > 0 AND NOT a.attisdropped
   ORDER BY a.attnum`, Args: []any{table}}, nil
}
func (Driver) Indexes(ctx context.Context, conn database.Connection, table string) (database.Query, error) {
	table, err := resolveTable(ctx, conn, table)
	if err != nil {
		return database.Query{}, err
	}
	return database.Query{Text: `SELECT c.relname AS name, i.indisunique AS is_unique,
   i.indisprimary AS is_primary, pg_get_indexdef(i.indexrelid) AS definition
   FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
   WHERE i.indrelid = to_regclass($1) ORDER BY c.relname`, Args: []any{table}}, nil
}

// resolveTable returns a quoted relation name. to_regclass follows the session's
// search_path and quoted identifiers; names listed by \tables (schema.Table) match as stored.
func resolveTable(ctx context.Context, conn database.Connection, table string) (string, error) {
	names, err := database.Names(ctx, conn, database.Query{Text: `SELECT c.oid::regclass::text FROM pg_class c
   JOIN pg_namespace n ON n.oid = c.relnamespace
   WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f') AND (c.oid = to_regclass($1)
    OR n.nspname || '.' || c.relname = $1
    OR (c.relname = $1 AND n.nspname = ANY(current_schemas(false))))
   ORDER BY COALESCE(c.oid = to_regclass($1), false) DESC,
    array_position(current_schemas(false), n.nspname::text) LIMIT 1`, Args: []any{table}})
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return "", fmt.Errorf("table or view %q does not exist", table)
	}
	return names[0], nil
}

func (Driver) CurrentDatabase(ctx context.Context, conn database.Connection) (string, error) {
	return database.ReadCurrentDatabase(ctx, conn, `SELECT current_database()`)
}
func (Driver) TableColumns(ctx context.Context, conn database.Connection) (map[string][]string, error) {
	return database.ReadTableColumns(ctx, conn, `SELECT name, column_name FROM (
 SELECT table_schema || '.' || table_name AS name, column_name, ordinal_position
 FROM information_schema.columns WHERE table_schema NOT IN ('pg_catalog','information_schema')
 UNION ALL
 SELECT table_name, column_name, ordinal_position FROM information_schema.columns
 WHERE table_schema NOT IN ('pg_catalog','information_schema')
 AND pg_table_is_visible(format('%I.%I', table_schema, table_name)::regclass)
 ) AS cols ORDER BY name, ordinal_position`)
}

func (Driver) DDL(ctx context.Context, conn database.Connection, table string) (string, error) {
	regclass, err := resolveTable(ctx, conn, table)
	if err != nil {
		return "", err
	}

	metaRows, err := conn.Query(ctx, `SELECT c.relkind::text, COALESCE(pg_get_partkeydef(c.oid), ''), COALESCE(obj_description(c.oid, 'pg_class'), '')
FROM pg_class c WHERE c.oid = to_regclass($1)`, regclass)
	if err != nil {
		return "", err
	}
	var relkind, partKey, tableComment string
	if metaRows.Next() {
		row, err := metaRows.Row()
		if err != nil {
			_ = metaRows.Close()
			return "", err
		}
		if len(row.Values) >= 3 {
			relkind = fmt.Sprint(row.Values[0])
			partKey = fmt.Sprint(row.Values[1])
			tableComment = fmt.Sprint(row.Values[2])
		}
	}
	if err := metaRows.Err(); err != nil {
		_ = metaRows.Close()
		return "", err
	}
	_ = metaRows.Close()

	switch relkind {
	case "v", "m":
		defs, err := database.Names(ctx, conn, database.Query{
			Text: `SELECT pg_get_viewdef(to_regclass($1), true)`,
			Args: []any{regclass},
		})
		if err != nil {
			return "", err
		}
		if len(defs) == 0 {
			return "", fmt.Errorf("DDL for %q is unavailable", table)
		}
		kind := "VIEW"
		if relkind == "m" {
			kind = "MATERIALIZED VIEW"
		}
		body := strings.TrimRight(strings.TrimSpace(defs[0]), ";")
		return fmt.Sprintf("CREATE %s %s AS\n%s;", kind, regclass, body), nil
	case "r", "p":
	default:
		return "", fmt.Errorf("DDL export is not supported for relation kind %q", relkind)
	}

	var entries []string
	colRows, err := conn.Query(ctx, `SELECT quote_ident(a.attname), format_type(a.atttypid, a.atttypmod),
COALESCE(pg_get_expr(d.adbin, d.adrelid), ''), a.attnotnull
FROM pg_attribute a
LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE a.attrelid = to_regclass($1) AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum`, regclass)
	if err != nil {
		return "", err
	}
	for colRows.Next() {
		row, err := colRows.Row()
		if err != nil {
			_ = colRows.Close()
			return "", err
		}
		if len(row.Values) < 4 {
			continue
		}
		colName := fmt.Sprint(row.Values[0])
		colType := fmt.Sprint(row.Values[1])
		colDefault := fmt.Sprint(row.Values[2])
		notNull, _ := row.Values[3].(bool)
		line := "    " + colName + " " + colType
		if colDefault != "" {
			line += " DEFAULT " + colDefault
		}
		if notNull {
			line += " NOT NULL"
		}
		entries = append(entries, line)
	}
	if err := colRows.Err(); err != nil {
		_ = colRows.Close()
		return "", err
	}
	_ = colRows.Close()

	conRows, err := conn.Query(ctx, `SELECT quote_ident(conname), pg_get_constraintdef(oid, true)
FROM pg_constraint
WHERE conrelid = to_regclass($1) AND contype IN ('p','u','f','c')
ORDER BY CASE contype WHEN 'p' THEN 0 WHEN 'u' THEN 1 WHEN 'f' THEN 2 ELSE 3 END, conname`, regclass)
	if err != nil {
		return "", err
	}
	for conRows.Next() {
		row, err := conRows.Row()
		if err != nil {
			_ = conRows.Close()
			return "", err
		}
		if len(row.Values) >= 2 {
			entries = append(entries, fmt.Sprintf("    CONSTRAINT %s %s", fmt.Sprint(row.Values[0]), fmt.Sprint(row.Values[1])))
		}
	}
	if err := conRows.Err(); err != nil {
		_ = conRows.Close()
		return "", err
	}
	_ = conRows.Close()

	var stmts []string
	createStmt := fmt.Sprintf("CREATE TABLE %s (\n%s\n)", regclass, strings.Join(entries, ",\n"))
	if partKey != "" {
		createStmt += "\nPARTITION BY " + partKey
	}
	stmts = append(stmts, createStmt+";")

	idxDefs, err := database.Names(ctx, conn, database.Query{
		Text: `SELECT pg_get_indexdef(i.indexrelid)
FROM pg_index i
JOIN pg_class c ON c.oid = i.indexrelid
LEFT JOIN pg_constraint con ON con.conindid = i.indexrelid
WHERE i.indrelid = to_regclass($1) AND con.oid IS NULL
ORDER BY c.relname`,
		Args: []any{regclass},
	})
	if err != nil {
		return "", err
	}
	for _, idx := range idxDefs {
		idx = strings.TrimSpace(idx)
		if idx != "" {
			if !strings.HasSuffix(idx, ";") {
				idx += ";"
			}
			stmts = append(stmts, idx)
		}
	}

	if tableComment != "" {
		stmts = append(stmts, fmt.Sprintf("COMMENT ON TABLE %s IS '%s';", regclass, strings.ReplaceAll(tableComment, "'", "''")))
	}

	cmtRows, err := conn.Query(ctx, `SELECT quote_ident(a.attname), col_description(a.attrelid, a.attnum)
FROM pg_attribute a
WHERE a.attrelid = to_regclass($1) AND a.attnum > 0 AND NOT a.attisdropped
  AND col_description(a.attrelid, a.attnum) IS NOT NULL
ORDER BY a.attnum`, regclass)
	if err != nil {
		return "", err
	}
	for cmtRows.Next() {
		row, err := cmtRows.Row()
		if err != nil {
			_ = cmtRows.Close()
			return "", err
		}
		if len(row.Values) >= 2 && row.Values[1] != nil {
			cmt := strings.ReplaceAll(fmt.Sprint(row.Values[1]), "'", "''")
			stmts = append(stmts, fmt.Sprintf("COMMENT ON COLUMN %s.%s IS '%s';", regclass, fmt.Sprint(row.Values[0]), cmt))
		}
	}
	if err := cmtRows.Err(); err != nil {
		_ = cmtRows.Close()
		return "", err
	}
	_ = cmtRows.Close()

	return strings.Join(stmts, "\n\n"), nil
}
