// Package mssql implements SQL Server connection and metadata behavior.
package mssql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/benenen/dbh/internal/database"
	gomssql "github.com/microsoft/go-mssqldb"
)

type Driver struct{}

var _ database.Driver = Driver{}
var _ database.CurrentDatabase = Driver{}
var _ database.ColumnNames = Driver{}
var _ database.TableColumns = Driver{}

func (Driver) Open(ctx context.Context, dsn string, dial database.Dial) (database.Connection, error) {
	connector, err := gomssql.NewConnector(dsn)
	if err != nil {
		return nil, fmt.Errorf("invalid SQL Server connection configuration: %w", err)
	}
	if dial != nil {
		connector.Dialer = dial
	}
	return database.ConnectSQL(ctx, sql.OpenDB(connector))
}

func (Driver) Syntax() database.Syntax {
	return database.Syntax{BracketIdentifiers: true}
}

func (Driver) SwitchDatabase(ctx context.Context, conn database.Connection, _ string, name string, _ database.Dial) (database.Connection, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("database name cannot be empty")
	}
	escaped := strings.ReplaceAll(name, "]", "]]")
	if err := conn.Exec(ctx, "USE ["+escaped+"]"); err != nil {
		return nil, err
	}
	return nil, nil
}

func (Driver) CurrentDatabase(ctx context.Context, conn database.Connection) (string, error) {
	return database.ReadCurrentDatabase(ctx, conn, `SELECT COALESCE(DB_NAME(), '')`)
}

func (Driver) Databases(ctx context.Context, conn database.Connection) ([]string, error) {
	return database.Names(ctx, conn, database.Query{Text: `SELECT name FROM sys.databases ORDER BY name`})
}

func (Driver) Tables(ctx context.Context, conn database.Connection) ([]string, error) {
	return database.Names(ctx, conn, database.Query{
		Text: `SELECT TABLE_SCHEMA + '.' + TABLE_NAME FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_TYPE IN ('BASE TABLE','VIEW') ORDER BY TABLE_SCHEMA, TABLE_NAME`,
	})
}

func splitTable(table string) (schema, name string) {
	parts := database.SplitIdentifier(table)
	if len(parts) >= 2 {
		return parts[len(parts)-2], parts[len(parts)-1]
	}
	return "", parts[0]
}

func (Driver) Columns(ctx context.Context, conn database.Connection, table string) ([]string, error) {
	schema, name := splitTable(table)
	if schema != "" {
		return database.Names(ctx, conn, database.Query{
			Text: `SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = @p1 AND TABLE_NAME = @p2 ORDER BY ORDINAL_POSITION`,
			Args: []any{schema, name},
		})
	}
	return database.Names(ctx, conn, database.Query{
		Text: `SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = SCHEMA_NAME() AND TABLE_NAME = @p1 ORDER BY ORDINAL_POSITION`,
		Args: []any{name},
	})
}

func (Driver) ColumnNames(ctx context.Context, conn database.Connection) ([]string, error) {
	return database.Names(ctx, conn, database.Query{Text: `SELECT DISTINCT COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS`})
}

func (Driver) TableColumns(ctx context.Context, conn database.Connection) (map[string][]string, error) {
	return database.ReadTableColumns(ctx, conn, `
SELECT TABLE_SCHEMA + '.' + TABLE_NAME AS name, COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS
UNION ALL
SELECT TABLE_NAME AS name, COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = SCHEMA_NAME()
ORDER BY name`)
}

func resolveTable(ctx context.Context, conn database.Connection, table string) (string, string, error) {
	schema, name := splitTable(table)
	var rows []string
	var err error
	if schema != "" {
		rows, err = database.Names(ctx, conn, database.Query{
			Text: `SELECT TABLE_SCHEMA + '.' + TABLE_NAME FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = @p1 AND TABLE_NAME = @p2`,
			Args: []any{schema, name},
		})
	} else {
		rows, err = database.Names(ctx, conn, database.Query{
			Text: `SELECT TABLE_SCHEMA + '.' + TABLE_NAME FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA = SCHEMA_NAME() AND TABLE_NAME = @p1`,
			Args: []any{name},
		})
	}
	if err != nil {
		return "", "", err
	}
	if len(rows) == 0 {
		return "", "", fmt.Errorf("table or view %q does not exist", table)
	}
	parts := strings.SplitN(rows[0], ".", 2)
	return parts[0], parts[1], nil
}

func (Driver) ColumnDetails(ctx context.Context, conn database.Connection, table string) (database.Query, error) {
	schema, name, err := resolveTable(ctx, conn, table)
	if err != nil {
		return database.Query{}, err
	}
	return database.Query{
		Text: `SELECT c.COLUMN_NAME AS name,
  CASE
    WHEN c.CHARACTER_MAXIMUM_LENGTH IS NOT NULL AND c.CHARACTER_MAXIMUM_LENGTH > 0
      THEN c.DATA_TYPE + '(' + CAST(c.CHARACTER_MAXIMUM_LENGTH AS VARCHAR(16)) + ')'
    WHEN c.CHARACTER_MAXIMUM_LENGTH = -1
      THEN c.DATA_TYPE + '(max)'
    WHEN c.NUMERIC_PRECISION IS NOT NULL AND c.DATA_TYPE IN ('decimal','numeric')
      THEN c.DATA_TYPE + '(' + CAST(c.NUMERIC_PRECISION AS VARCHAR(16)) + ',' + CAST(COALESCE(c.NUMERIC_SCALE, 0) AS VARCHAR(16)) + ')'
    ELSE c.DATA_TYPE
  END AS type,
  c.IS_NULLABLE AS nullable,
  c.COLUMN_DEFAULT AS [default],
  COALESCE(pk.key_ordinal, 0) AS primary_key_position
FROM INFORMATION_SCHEMA.COLUMNS c
LEFT JOIN (
  SELECT ic.column_id, ic.key_ordinal, col.name AS col_name
  FROM sys.indexes i
  JOIN sys.index_columns ic ON i.object_id = ic.object_id AND i.index_id = ic.index_id
  JOIN sys.columns col ON ic.object_id = col.object_id AND ic.column_id = col.column_id
  WHERE i.object_id = OBJECT_ID(@p1 + '.' + @p2) AND i.is_primary_key = 1
) pk ON pk.col_name = c.COLUMN_NAME
WHERE c.TABLE_SCHEMA = @p1 AND c.TABLE_NAME = @p2
ORDER BY c.ORDINAL_POSITION`,
		Args: []any{schema, name},
	}, nil
}

func (Driver) Indexes(ctx context.Context, conn database.Connection, table string) (database.Query, error) {
	schema, name, err := resolveTable(ctx, conn, table)
	if err != nil {
		return database.Query{}, err
	}
	return database.Query{
		Text: `SELECT i.name AS name, i.type_desc AS type, i.is_unique, i.is_primary_key AS is_primary,
  STUFF((
    SELECT ',' + col.name
    FROM sys.index_columns ic
    JOIN sys.columns col ON ic.object_id = col.object_id AND ic.column_id = col.column_id
    WHERE ic.object_id = i.object_id AND ic.index_id = i.index_id AND ic.is_included_column = 0
    ORDER BY ic.key_ordinal
    FOR XML PATH(''), TYPE
  ).value('.', 'NVARCHAR(MAX)'), 1, 1, '') AS columns
FROM sys.indexes i
WHERE i.object_id = OBJECT_ID(@p1 + '.' + @p2) AND i.name IS NOT NULL
ORDER BY i.is_primary_key DESC, i.name`,
		Args: []any{schema, name},
	}, nil
}
