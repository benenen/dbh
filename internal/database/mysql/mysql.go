// Package mysql implements MySQL connection and metadata behavior.
package mysql

import (
	"context"
	"fmt"
	"strings"

	"github.com/benenen/dbh/internal/database"
	_ "github.com/go-sql-driver/mysql"
)

type Driver struct{}

var _ database.Driver = Driver{}

func (Driver) Open(ctx context.Context, dsn string) (database.Connection, error) {
	return database.OpenSQL(ctx, "mysql", dsn)
}
func (Driver) Syntax() database.Syntax {
	return database.Syntax{BackslashEscapes: true, DashCommentNeedsSpace: true, HashComments: true, ExecutableComments: true}
}

func (Driver) SwitchDatabase(ctx context.Context, conn database.Connection, _ string, name string) (database.Connection, error) {
	err := conn.Exec(ctx, "USE `"+strings.ReplaceAll(name, "`", "``")+"`")
	return nil, err
}
func (Driver) Databases(ctx context.Context, conn database.Connection) ([]string, error) {
	return database.Names(ctx, conn, database.Query{Text: "SHOW DATABASES"})
}
func (Driver) Tables(ctx context.Context, conn database.Connection) ([]string, error) {
	return database.Names(ctx, conn, database.Query{Text: "SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() ORDER BY table_name"})
}
func (Driver) Columns(ctx context.Context, conn database.Connection, table string) ([]string, error) {
	schema, name := splitTable(table)
	if schema == "" {
		return database.Names(ctx, conn, database.Query{Text: `SELECT column_name FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? ORDER BY ordinal_position`, Args: []any{name}})
	}
	return database.Names(ctx, conn, database.Query{Text: `SELECT column_name FROM information_schema.columns WHERE table_schema=? AND table_name=? ORDER BY ordinal_position`, Args: []any{schema, name}})
}
func (Driver) ColumnDetails(ctx context.Context, conn database.Connection, table string) (database.Query, error) {
	schema, name, err := resolveTable(ctx, conn, table)
	if err != nil {
		return database.Query{}, err
	}
	return database.Query{Text: `SELECT c.COLUMN_NAME AS name, c.COLUMN_TYPE AS type,
   c.IS_NULLABLE AS nullable, c.COLUMN_DEFAULT AS ` + "`default`" + `,
   COALESCE(k.ORDINAL_POSITION, 0) AS primary_key_position, c.COLUMN_COMMENT AS comment
   FROM information_schema.COLUMNS c LEFT JOIN information_schema.KEY_COLUMN_USAGE k
    ON k.TABLE_SCHEMA = c.TABLE_SCHEMA AND k.TABLE_NAME = c.TABLE_NAME
    AND k.COLUMN_NAME = c.COLUMN_NAME AND k.CONSTRAINT_NAME = 'PRIMARY'
   WHERE c.TABLE_SCHEMA = ? AND c.TABLE_NAME = ? ORDER BY c.ORDINAL_POSITION`, Args: []any{schema, name}}, nil
}
func (Driver) Indexes(ctx context.Context, conn database.Connection, table string) (database.Query, error) {
	schema, name, err := resolveTable(ctx, conn, table)
	if err != nil {
		return database.Query{}, err
	}
	expression := "NULL"
	available, err := database.Names(ctx, conn, database.Query{Text: `SELECT COLUMN_NAME FROM information_schema.COLUMNS
   WHERE TABLE_SCHEMA = 'information_schema' AND TABLE_NAME = 'STATISTICS' AND COLUMN_NAME = 'EXPRESSION'`})
	if err != nil {
		return database.Query{}, err
	}
	if len(available) > 0 {
		expression = "EXPRESSION"
	}
	// One row per index column preserves order without GROUP_CONCAT truncation.
	return database.Query{Text: `SELECT INDEX_NAME AS name, NON_UNIQUE = 0 AS is_unique,
   INDEX_NAME = 'PRIMARY' AS is_primary, INDEX_TYPE AS type,
   SEQ_IN_INDEX AS position, COLUMN_NAME AS column_name, ` + expression + ` AS expression,
   SUB_PART AS prefix_length, COLLATION AS direction, INDEX_COMMENT AS comment
   FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
   ORDER BY INDEX_NAME, SEQ_IN_INDEX`, Args: []any{schema, name}}, nil
}
func splitTable(table string) (schema, name string) {
	if parts := strings.SplitN(table, ".", 2); len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", table
}
func resolveTable(ctx context.Context, conn database.Connection, table string) (schema, name string, err error) {
	schema, name = splitTable(table)
	schemas, err := database.Names(ctx, conn, database.Query{Text: `SELECT TABLE_SCHEMA FROM information_schema.TABLES
   WHERE TABLE_NAME = ? AND TABLE_SCHEMA = COALESCE(NULLIF(?, ''), DATABASE())`, Args: []any{name, schema}})
	if err != nil {
		return "", "", err
	}
	if len(schemas) == 0 {
		return "", "", fmt.Errorf("table or view %q does not exist", table)
	}
	return schemas[0], name, nil
}
