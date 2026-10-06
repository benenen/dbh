package dbh

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// Describe prints column details and indexes using the current session.
func (s *Session) Describe(ctx context.Context, table, format string, out io.Writer) error {
	schema, name, err := s.resolveTable(ctx, table)
	if err != nil {
		return err
	}
	query := ""
	var args []any
	switch s.Driver {
	case "sqlite":
		query = `SELECT c.name AS name, c.type AS type,
			CASE WHEN c."notnull" OR (c.pk > 0 AND (t.wr OR t.strict OR
				(c.type = 'INTEGER' COLLATE NOCASE AND NOT EXISTS
					(SELECT 1 FROM pragma_index_list(?, ?) WHERE origin = 'pk'))))
				THEN 'NO' ELSE 'YES' END AS nullable,
			c.dflt_value AS "default", c.pk AS primary_key_position, NULL AS comment
			FROM pragma_table_xinfo(?, ?) c
			JOIN pragma_table_list t ON t.name = ? AND t.schema = ? ORDER BY c.cid`
		args = []any{name, schema, name, schema, name, schema}
	case "postgres":
		query = `SELECT a.attname AS name, format_type(a.atttypid, a.atttypmod) AS type,
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
			ORDER BY a.attnum`
		args = []any{table}
	case "mysql":
		query = `SELECT c.COLUMN_NAME AS name, c.COLUMN_TYPE AS type,
			c.IS_NULLABLE AS nullable, c.COLUMN_DEFAULT AS ` + "`default`" + `,
			COALESCE(k.ORDINAL_POSITION, 0) AS primary_key_position, c.COLUMN_COMMENT AS comment
			FROM information_schema.COLUMNS c LEFT JOIN information_schema.KEY_COLUMN_USAGE k
				ON k.TABLE_SCHEMA = c.TABLE_SCHEMA AND k.TABLE_NAME = c.TABLE_NAME
				AND k.COLUMN_NAME = c.COLUMN_NAME AND k.CONSTRAINT_NAME = 'PRIMARY'
			WHERE c.TABLE_SCHEMA = ? AND c.TABLE_NAME = ? ORDER BY c.ORDINAL_POSITION`
		args = []any{schema, name}
	}
	if format == "table" {
		if _, err := fmt.Fprintln(out, "Columns:"); err != nil {
			return err
		}
	}
	if err := s.executeArgs(ctx, query, format, out, args...); err != nil {
		return err
	}
	return s.printIndexes(ctx, schema, name, table, format, out)
}

func (s *Session) Indexes(ctx context.Context, table, format string, out io.Writer) error {
	schema, name, err := s.resolveTable(ctx, table)
	if err != nil {
		return err
	}
	return s.printIndexes(ctx, schema, name, table, format, out)
}

func (s *Session) printIndexes(ctx context.Context, schema, name, table, format string, out io.Writer) error {
	query := ""
	var args []any
	switch s.Driver {
	case "sqlite":
		// Schema comes from SQLite metadata and is quoted as an identifier.
		quotedSchema := `"` + strings.ReplaceAll(schema, `"`, `""`) + `"`
		query = `SELECT i.name AS name, i."unique" AS is_unique,
			i.origin = 'pk' AS is_primary, i.partial AS is_partial,
			(SELECT group_concat(label, ', ') FROM
				(SELECT COALESCE(x.name, '<expression>') || CASE WHEN x.desc THEN ' DESC' ELSE '' END AS label
				 FROM pragma_index_xinfo(i.name, ?) x WHERE x.key = 1 ORDER BY x.seqno)) AS columns,
			m.sql AS definition FROM pragma_index_list(?, ?) i
			LEFT JOIN ` + quotedSchema + `.sqlite_schema m ON m.name = i.name AND m.type = 'index'
			ORDER BY i.name`
		args = []any{schema, name, schema}
	case "postgres":
		query = `SELECT c.relname AS name, i.indisunique AS is_unique,
			i.indisprimary AS is_primary, pg_get_indexdef(i.indexrelid) AS definition
			FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
			WHERE i.indrelid = to_regclass($1) ORDER BY c.relname`
		args = []any{table}
	case "mysql":
		expression := "NULL"
		available, err := s.names(ctx, `SELECT COLUMN_NAME FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = 'information_schema' AND TABLE_NAME = 'STATISTICS' AND COLUMN_NAME = 'EXPRESSION'`)
		if err != nil {
			return err
		}
		if len(available) > 0 {
			expression = "EXPRESSION"
		}
		// One row per index column preserves order without GROUP_CONCAT truncation.
		query = `SELECT INDEX_NAME AS name, NON_UNIQUE = 0 AS is_unique,
			INDEX_NAME = 'PRIMARY' AS is_primary, INDEX_TYPE AS type,
			SEQ_IN_INDEX AS position, COLUMN_NAME AS column_name, ` + expression + ` AS expression,
			SUB_PART AS prefix_length, COLLATION AS direction, INDEX_COMMENT AS comment
			FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
			ORDER BY INDEX_NAME, SEQ_IN_INDEX`
		args = []any{schema, name}
	}
	if format == "table" {
		if _, err := fmt.Fprintln(out, "Indexes:"); err != nil {
			return err
		}
	}
	return s.executeArgs(ctx, query, format, out, args...)
}

// Resolve SQLite/MySQL schemas and reject missing relations before printing results.
// PostgreSQL to_regclass follows the session search_path and quoted identifiers.
func (s *Session) resolveTable(ctx context.Context, table string) (schema, name string, err error) {
	name = table
	if s.Driver == "postgres" {
		var names []string
		names, err = s.names(ctx, `SELECT c.relname FROM pg_class c WHERE c.oid = to_regclass($1)
			AND c.relkind IN ('r', 'p', 'v', 'm', 'f')`, table)
		if err == nil && len(names) == 0 {
			err = fmt.Errorf("table or view %q does not exist", table)
		}
		return
	}
	if parts := strings.SplitN(table, ".", 2); len(parts) == 2 {
		schema, name = parts[0], parts[1]
	}
	var schemas []string
	if s.Driver == "sqlite" {
		schemas, err = s.names(ctx, `SELECT schema FROM pragma_table_list
			WHERE name = ? AND (? = '' OR schema = ?)
			ORDER BY CASE schema WHEN 'temp' THEN 0 WHEN 'main' THEN 1 ELSE 2 END`, name, schema, schema)
	} else {
		schemas, err = s.names(ctx, `SELECT TABLE_SCHEMA FROM information_schema.TABLES
			WHERE TABLE_NAME = ? AND TABLE_SCHEMA = COALESCE(NULLIF(?, ''), DATABASE())`, name, schema)
	}
	if err != nil {
		return
	}
	if len(schemas) == 0 {
		err = fmt.Errorf("table or view %q does not exist", table)
		return
	}
	schema = schemas[0]
	return
}
