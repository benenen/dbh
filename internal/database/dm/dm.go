// Package dm implements Dameng (达梦) connection and metadata behavior.
package dm

import (
	"context"
	"fmt"
	"strings"

	_ "gitee.com/chunanyong/dm"
	"github.com/benenen/dbh/internal/database"
)

type Driver struct{}

var _ database.Driver = Driver{}
var _ database.CurrentDatabase = Driver{}
var _ database.ColumnNames = Driver{}
var _ database.TableColumns = Driver{}

func (Driver) Open(ctx context.Context, dsn string, dial database.Dial) (database.Connection, error) {
	if !strings.HasPrefix(strings.ToLower(dsn), "dm://") {
		return nil, fmt.Errorf("invalid Dameng connection URI")
	}
	if dial != nil {
		return nil, fmt.Errorf("proxies are not supported for dm")
	}
	return database.OpenSQL(ctx, "dm", dsn)
}

func (Driver) Syntax() database.Syntax {
	return database.Syntax{}
}

func (Driver) SwitchDatabase(ctx context.Context, conn database.Connection, _ string, name string, _ database.Dial) (database.Connection, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("schema name cannot be empty")
	}
	escaped := strings.ReplaceAll(name, `"`, `""`)
	if err := conn.Exec(ctx, `SET SCHEMA "`+escaped+`"`); err != nil {
		return nil, err
	}
	return nil, nil
}

func (Driver) CurrentDatabase(ctx context.Context, conn database.Connection) (string, error) {
	return database.ReadCurrentDatabase(ctx, conn, `SELECT SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA')`)
}

func (Driver) Databases(ctx context.Context, conn database.Connection) ([]string, error) {
	return database.Names(ctx, conn, database.Query{Text: `SELECT NAME FROM SYS.SYSOBJECTS WHERE TYPE$ = 'SCH' ORDER BY NAME`})
}

func (Driver) Tables(ctx context.Context, conn database.Connection) ([]string, error) {
	return database.Names(ctx, conn, database.Query{
		Text: `SELECT TABLE_NAME FROM ALL_TABLES WHERE OWNER = SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') UNION ALL SELECT VIEW_NAME FROM ALL_VIEWS WHERE OWNER = SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') ORDER BY 1`,
	})
}

func splitTable(table string) (owner, name string) {
	parts := database.SplitIdentifier(table)
	if len(parts) >= 2 {
		return parts[len(parts)-2], parts[len(parts)-1]
	}
	return "", parts[0]
}

func (Driver) Columns(ctx context.Context, conn database.Connection, table string) ([]string, error) {
	owner, name := splitTable(table)
	if owner != "" {
		return database.Names(ctx, conn, database.Query{
			Text: `SELECT COLUMN_NAME FROM ALL_TAB_COLUMNS WHERE OWNER = ? AND TABLE_NAME = ? ORDER BY COLUMN_ID`,
			Args: []any{owner, name},
		})
	}
	return database.Names(ctx, conn, database.Query{
		Text: `SELECT COLUMN_NAME FROM ALL_TAB_COLUMNS WHERE OWNER = SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') AND TABLE_NAME = ? ORDER BY COLUMN_ID`,
		Args: []any{name},
	})
}

func (Driver) ColumnNames(ctx context.Context, conn database.Connection) ([]string, error) {
	return database.Names(ctx, conn, database.Query{
		Text: `SELECT DISTINCT COLUMN_NAME FROM ALL_TAB_COLUMNS WHERE OWNER = SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA')`,
	})
}

func (Driver) TableColumns(ctx context.Context, conn database.Connection) (map[string][]string, error) {
	return database.ReadTableColumns(ctx, conn, `SELECT TABLE_NAME, COLUMN_NAME FROM ALL_TAB_COLUMNS WHERE OWNER = SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') ORDER BY TABLE_NAME, COLUMN_ID`)
}

func resolveTable(ctx context.Context, conn database.Connection, table string) (string, string, error) {
	owner, name := splitTable(table)
	var rows []string
	var err error
	if owner != "" {
		rows, err = database.Names(ctx, conn, database.Query{
			Text: `SELECT OWNER || '.' || OBJECT_NAME FROM ALL_OBJECTS WHERE OWNER = ? AND OBJECT_NAME = ? AND OBJECT_TYPE IN ('TABLE','VIEW','MATERIALIZED VIEW')`,
			Args: []any{owner, name},
		})
	} else {
		rows, err = database.Names(ctx, conn, database.Query{
			Text: `SELECT OWNER || '.' || OBJECT_NAME FROM ALL_OBJECTS WHERE OWNER = SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') AND OBJECT_NAME = ? AND OBJECT_TYPE IN ('TABLE','VIEW','MATERIALIZED VIEW')`,
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
	owner, name, err := resolveTable(ctx, conn, table)
	if err != nil {
		return database.Query{}, err
	}
	return database.Query{
		Text: `SELECT c.COLUMN_NAME AS name,
  c.DATA_TYPE || CASE
    WHEN c.DATA_TYPE IN ('VARCHAR','VARCHAR2','CHAR','CHARACTER','VARBINARY','BINARY') AND c.DATA_LENGTH > 0 THEN '(' || c.DATA_LENGTH || ')'
    WHEN c.DATA_TYPE IN ('DECIMAL','DEC','NUMERIC','NUMBER') AND c.DATA_PRECISION IS NOT NULL THEN '(' || c.DATA_PRECISION || CASE WHEN c.DATA_SCALE > 0 THEN ',' || c.DATA_SCALE ELSE '' END || ')'
    ELSE ''
  END AS type,
  c.NULLABLE AS nullable,
  c.DATA_DEFAULT AS "default",
  cc.COMMENTS AS "comment"
FROM ALL_TAB_COLUMNS c
LEFT JOIN ALL_COL_COMMENTS cc
  ON cc.OWNER = c.OWNER AND cc.TABLE_NAME = c.TABLE_NAME AND cc.COLUMN_NAME = c.COLUMN_NAME
WHERE c.OWNER = ? AND c.TABLE_NAME = ?
ORDER BY c.COLUMN_ID`,
		Args: []any{owner, name},
	}, nil
}

func (Driver) Indexes(ctx context.Context, conn database.Connection, table string) (database.Query, error) {
	owner, name, err := resolveTable(ctx, conn, table)
	if err != nil {
		return database.Query{}, err
	}
	return database.Query{
		Text: `SELECT i.INDEX_NAME AS name, i.INDEX_TYPE AS type, i.UNIQUENESS AS uniqueness,
  LISTAGG(ic.COLUMN_NAME, ',') WITHIN GROUP (ORDER BY ic.COLUMN_POSITION) AS columns
FROM ALL_INDEXES i
JOIN ALL_IND_COLUMNS ic ON ic.INDEX_OWNER = i.OWNER AND ic.INDEX_NAME = i.INDEX_NAME
WHERE i.TABLE_OWNER = ? AND i.TABLE_NAME = ?
GROUP BY i.INDEX_NAME, i.INDEX_TYPE, i.UNIQUENESS
ORDER BY i.INDEX_NAME`,
		Args: []any{owner, name},
	}, nil
}
