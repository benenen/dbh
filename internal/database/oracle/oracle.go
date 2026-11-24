// Package oracle implements Oracle connection and metadata behavior.
package oracle

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/benenen/dbh/internal/database"
	go_ora "github.com/sijms/go-ora/v2"
)

type Driver struct{}

var _ database.Driver = Driver{}
var _ database.CurrentDatabase = Driver{}
var _ database.ColumnNames = Driver{}
var _ database.TableColumns = Driver{}

func (Driver) Open(ctx context.Context, dsn string, dial database.Dial) (database.Connection, error) {
	if !strings.HasPrefix(strings.ToLower(dsn), "oracle://") {
		return nil, fmt.Errorf("invalid Oracle connection URI")
	}
	connector := go_ora.NewConnector(dsn)
	if oc, ok := connector.(*go_ora.OracleConnector); ok && dial != nil {
		oc.Dialer(dial)
	}
	return database.ConnectSQL(ctx, sql.OpenDB(connector))
}

func (Driver) Syntax() database.Syntax {
	return database.Syntax{}
}

func (Driver) SwitchDatabase(ctx context.Context, conn database.Connection, _ string, name string, _ database.Dial) (database.Connection, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("schema name cannot be empty")
	}
	escaped := strings.ReplaceAll(name, `"`, `""`)
	if err := conn.Exec(ctx, `ALTER SESSION SET CURRENT_SCHEMA = "`+escaped+`"`); err != nil {
		return nil, err
	}
	return nil, nil
}

func (Driver) CurrentDatabase(ctx context.Context, conn database.Connection) (string, error) {
	return database.ReadCurrentDatabase(ctx, conn, `SELECT SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') FROM DUAL`)
}

func (Driver) Databases(ctx context.Context, conn database.Connection) ([]string, error) {
	return database.Names(ctx, conn, database.Query{Text: `SELECT USERNAME FROM ALL_USERS ORDER BY USERNAME`})
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
			Text: `SELECT COLUMN_NAME FROM ALL_TAB_COLUMNS WHERE OWNER = :1 AND TABLE_NAME = :2 ORDER BY COLUMN_ID`,
			Args: []any{owner, name},
		})
	}
	return database.Names(ctx, conn, database.Query{
		Text: `SELECT COLUMN_NAME FROM ALL_TAB_COLUMNS WHERE OWNER = SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') AND TABLE_NAME = :1 ORDER BY COLUMN_ID`,
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
			Text: `SELECT OWNER || '.' || OBJECT_NAME FROM ALL_OBJECTS WHERE OWNER = :1 AND OBJECT_NAME = :2 AND OBJECT_TYPE IN ('TABLE','VIEW','MATERIALIZED VIEW')`,
			Args: []any{owner, name},
		})
	} else {
		rows, err = database.Names(ctx, conn, database.Query{
			Text: `SELECT OWNER || '.' || OBJECT_NAME FROM ALL_OBJECTS WHERE OWNER = SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') AND OBJECT_NAME = :1 AND OBJECT_TYPE IN ('TABLE','VIEW','MATERIALIZED VIEW')`,
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
    WHEN c.DATA_TYPE IN ('VARCHAR2','NVARCHAR2','CHAR','NCHAR','RAW') AND c.CHAR_LENGTH > 0 THEN '(' || c.CHAR_LENGTH || ')'
    WHEN c.DATA_TYPE = 'NUMBER' AND c.DATA_PRECISION IS NOT NULL THEN '(' || c.DATA_PRECISION || CASE WHEN c.DATA_SCALE > 0 THEN ',' || c.DATA_SCALE ELSE '' END || ')'
    ELSE ''
  END AS type,
  c.NULLABLE AS nullable,
  c.DATA_DEFAULT AS "default",
  cc.COMMENTS AS "comment"
FROM ALL_TAB_COLUMNS c
LEFT JOIN ALL_COL_COMMENTS cc
  ON cc.OWNER = c.OWNER AND cc.TABLE_NAME = c.TABLE_NAME AND cc.COLUMN_NAME = c.COLUMN_NAME
WHERE c.OWNER = :1 AND c.TABLE_NAME = :2
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
WHERE i.TABLE_OWNER = :1 AND i.TABLE_NAME = :2
GROUP BY i.INDEX_NAME, i.INDEX_TYPE, i.UNIQUENESS
ORDER BY i.INDEX_NAME`,
		Args: []any{owner, name},
	}, nil
}
