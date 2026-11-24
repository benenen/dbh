package introspect

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const trinoTablesColumnsSQL = `
SELECT
  c.table_schema,
  c.table_name,
  c.column_name,
  c.data_type,
  c.is_nullable,
  c.column_default
FROM information_schema.tables t
JOIN information_schema.columns c
  ON t.table_schema = c.table_schema AND t.table_name = c.table_name
WHERE t.table_schema = ?
  AND t.table_type = 'BASE TABLE'
ORDER BY t.table_name, c.ordinal_position
`

func introspectTrino(ctx context.Context, db *sql.DB, schema string) (*DBSchema, error) {
	if _, s, ok := strings.Cut(schema, "."); ok && s != "" {
		schema = s
	}
	if schema == "" {
		if err := db.QueryRowContext(ctx, `SELECT current_schema`).Scan(&schema); err != nil {
			return nil, fmt.Errorf("current schema: %w", err)
		}
	}

	result := &DBSchema{}
	tableMap := map[string]*Table{}
	var tableOrder []string

	rows, err := db.QueryContext(ctx, trinoTablesColumnsSQL, schema)
	if err != nil {
		return nil, fmt.Errorf("tables_columns: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			tableSchema, tableName, colName, dataType, isNullable string
			colDefault                                            sql.NullString
		)
		if err := rows.Scan(&tableSchema, &tableName, &colName, &dataType, &isNullable, &colDefault); err != nil {
			return nil, fmt.Errorf("tables_columns scan: %w", err)
		}

		t, ok := tableMap[tableName]
		if !ok {
			t = &Table{Schema: tableSchema, Name: tableName}
			tableMap[tableName] = t
			tableOrder = append(tableOrder, tableName)
		}

		col := &Column{
			Name:       colName,
			Type:       dataType,
			IsNullable: strings.EqualFold(isNullable, "YES"),
		}
		if colDefault.Valid {
			val := strings.TrimSpace(colDefault.String)
			if val != "" && !strings.EqualFold(val, "NULL") {
				col.Default = &val
				col.DefaultType = inferOracleDefault(val)
			}
		}
		t.Columns = append(t.Columns, col)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, name := range tableOrder {
		result.Tables = append(result.Tables, tableMap[name])
	}
	return result, nil
}
