// Package postgres implements PostgreSQL connection and metadata behavior.
package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/benenen/dbh/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

type Driver struct{}

var _ database.Driver = Driver{}

func (Driver) Open(ctx context.Context, dsn string) (database.Connection, error) {
	return database.OpenSQL(ctx, "pgx", dsn)
}
func (Driver) Syntax() database.Syntax {
	return database.Syntax{NestedComments: true, EscapeStringPrefix: true, DollarQuotes: true, FoldUnquotedIdentifiers: true}
}

func (Driver) SwitchDatabase(ctx context.Context, _ database.Connection, dsn, name string) (database.Connection, error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("invalid PostgreSQL connection configuration")
	}
	config.Database = name
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
