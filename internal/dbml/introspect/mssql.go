package introspect

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const mssqlTablesColumnsSQL = `
SELECT
  c.TABLE_SCHEMA,
  c.TABLE_NAME,
  c.COLUMN_NAME,
  c.DATA_TYPE,
  c.CHARACTER_MAXIMUM_LENGTH,
  c.NUMERIC_PRECISION,
  c.NUMERIC_SCALE,
  c.IS_NULLABLE,
  c.COLUMN_DEFAULT,
  COALESCE(COLUMNPROPERTY(OBJECT_ID(QUOTENAME(c.TABLE_SCHEMA) + '.' + QUOTENAME(c.TABLE_NAME)), c.COLUMN_NAME, 'IsIdentity'), 0) AS is_identity
FROM INFORMATION_SCHEMA.TABLES t
JOIN INFORMATION_SCHEMA.COLUMNS c
  ON t.TABLE_SCHEMA = c.TABLE_SCHEMA AND t.TABLE_NAME = c.TABLE_NAME
WHERE t.TABLE_TYPE = 'BASE TABLE'
  AND t.TABLE_SCHEMA = @p1
ORDER BY t.TABLE_NAME, c.ORDINAL_POSITION
`

const mssqlIndexesSQL = `
SELECT
  t.name AS table_name,
  i.name AS index_name,
  i.is_unique,
  i.is_primary_key,
  i.is_unique_constraint,
  LOWER(i.type_desc) AS index_type,
  col.name AS column_name
FROM sys.tables t
JOIN sys.schemas s ON s.schema_id = t.schema_id
JOIN sys.indexes i ON i.object_id = t.object_id
JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
JOIN sys.columns col ON col.object_id = ic.object_id AND col.column_id = ic.column_id
WHERE s.name = @p1
  AND i.name IS NOT NULL
  AND ic.is_included_column = 0
ORDER BY t.name, i.is_primary_key DESC, i.name, ic.key_ordinal
`

const mssqlForeignKeysSQL = `
SELECT
  fk.name AS fk_name,
  ps.name AS table_schema,
  pt.name AS table_name,
  pc.name AS col_name,
  rs.name AS ref_schema,
  rt.name AS ref_table,
  rc.name AS ref_col_name,
  fk.delete_referential_action_desc,
  fk.update_referential_action_desc
FROM sys.foreign_keys fk
JOIN sys.foreign_key_columns fkc ON fkc.constraint_object_id = fk.object_id
JOIN sys.tables pt ON pt.object_id = fkc.parent_object_id
JOIN sys.schemas ps ON ps.schema_id = pt.schema_id
JOIN sys.columns pc ON pc.object_id = fkc.parent_object_id AND pc.column_id = fkc.parent_column_id
JOIN sys.tables rt ON rt.object_id = fkc.referenced_object_id
JOIN sys.schemas rs ON rs.schema_id = rt.schema_id
JOIN sys.columns rc ON rc.object_id = fkc.referenced_object_id AND rc.column_id = fkc.referenced_column_id
WHERE ps.name = @p1
ORDER BY pt.name, fk.name, fkc.constraint_column_id
`

func introspectMSSQL(ctx context.Context, db *sql.DB, schema string) (*DBSchema, error) {
	if schema == "" {
		if err := db.QueryRowContext(ctx, `SELECT COALESCE(SCHEMA_NAME(), 'dbo')`).Scan(&schema); err != nil {
			return nil, fmt.Errorf("current schema: %w", err)
		}
	}

	result := &DBSchema{}
	tableMap := map[string]*Table{}
	var tableOrder []string

	rows, err := db.QueryContext(ctx, mssqlTablesColumnsSQL, schema)
	if err != nil {
		return nil, fmt.Errorf("tables_columns: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			tableSchema, tableName, columnName, dataType, isNullable string
			charMaxLen, numPrecision, numScale                       sql.NullInt64
			columnDefault                                            sql.NullString
			isIdentity                                               int
		)
		if err := rows.Scan(
			&tableSchema, &tableName, &columnName, &dataType,
			&charMaxLen, &numPrecision, &numScale,
			&isNullable, &columnDefault, &isIdentity,
		); err != nil {
			return nil, fmt.Errorf("tables_columns scan: %w", err)
		}

		t, ok := tableMap[tableName]
		if !ok {
			t = &Table{Schema: tableSchema, Name: tableName}
			tableMap[tableName] = t
			tableOrder = append(tableOrder, tableName)
		}

		col := &Column{
			Name:        columnName,
			Type:        mssqlColumnType(dataType, charMaxLen, numPrecision, numScale),
			IsNullable:  isNullable == "YES",
			IsIncrement: isIdentity != 0,
		}
		if columnDefault.Valid {
			defVal, defType := inferMSSQLDefault(columnDefault.String)
			col.Default = &defVal
			col.DefaultType = defType
		}
		t.Columns = append(t.Columns, col)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, name := range tableOrder {
		result.Tables = append(result.Tables, tableMap[name])
	}

	rows2, err := db.QueryContext(ctx, mssqlIndexesSQL, schema)
	if err != nil {
		return nil, fmt.Errorf("indexes: %w", err)
	}
	defer rows2.Close()

	type idxKey struct{ table, name string }
	type mssqlIdx struct {
		table, name, typ   string
		isUnique, isPK     bool
		isUniqueConstraint bool
		cols               []string
	}
	idxMap := map[idxKey]*mssqlIdx{}
	var idxOrder []idxKey

	for rows2.Next() {
		var (
			tableName, indexName, indexType, colName string
			isUnique, isPK, isUniqueConstraint       bool
		)
		if err := rows2.Scan(&tableName, &indexName, &isUnique, &isPK, &isUniqueConstraint, &indexType, &colName); err != nil {
			return nil, fmt.Errorf("indexes scan: %w", err)
		}
		key := idxKey{tableName, indexName}
		entry, ok := idxMap[key]
		if !ok {
			entry = &mssqlIdx{
				table: tableName, name: indexName, typ: indexType,
				isUnique: isUnique, isPK: isPK, isUniqueConstraint: isUniqueConstraint,
			}
			idxMap[key] = entry
			idxOrder = append(idxOrder, key)
		}
		entry.cols = append(entry.cols, colName)
	}
	if err := rows2.Err(); err != nil {
		return nil, err
	}

	for _, key := range idxOrder {
		entry := idxMap[key]
		t, ok := tableMap[entry.table]
		if !ok {
			continue
		}
		if entry.isPK || entry.isUniqueConstraint {
			t.Constraints = append(t.Constraints, &Constraint{
				Name:      entry.name,
				Columns:   entry.cols,
				IsPrimary: entry.isPK,
			})
			if len(entry.cols) == 1 {
				for _, col := range t.Columns {
					if col.Name == entry.cols[0] {
						if entry.isPK {
							col.IsPrimaryKey = true
						} else {
							col.IsUnique = true
						}
						break
					}
				}
			}
		} else {
			idx := &Index{Name: entry.name, IsUnique: entry.isUnique, Type: entry.typ}
			for _, c := range entry.cols {
				idx.addKey(c, false)
			}
			t.Indexes = append(t.Indexes, idx)
		}
	}

	rows3, err := db.QueryContext(ctx, mssqlForeignKeysSQL, schema)
	if err != nil {
		return nil, fmt.Errorf("foreign_keys: %w", err)
	}
	defer rows3.Close()

	fkMap := map[string]*ForeignKey{}
	var fkOrder []string
	for rows3.Next() {
		var fkName, tableSchema, tableName, colName, refSchema, refTable, refCol, onDel, onUpd string
		if err := rows3.Scan(&fkName, &tableSchema, &tableName, &colName, &refSchema, &refTable, &refCol, &onDel, &onUpd); err != nil {
			return nil, fmt.Errorf("foreign_keys scan: %w", err)
		}
		key := tableSchema + "." + tableName + "." + fkName
		fk, ok := fkMap[key]
		if !ok {
			fk = &ForeignKey{
				Name:      fkName,
				Schema:    tableSchema,
				TableName: tableName,
				RefSchema: refSchema,
				RefTable:  refTable,
				OnDelete:  strings.ReplaceAll(onDel, "_", " "),
				OnUpdate:  strings.ReplaceAll(onUpd, "_", " "),
			}
			fkMap[key] = fk
			fkOrder = append(fkOrder, key)
		}
		fk.Columns = append(fk.Columns, colName)
		fk.RefColumns = append(fk.RefColumns, refCol)
	}
	if err := rows3.Err(); err != nil {
		return nil, err
	}
	for _, key := range fkOrder {
		result.FKs = append(result.FKs, fkMap[key])
	}

	return result, nil
}

func mssqlColumnType(dataType string, charMaxLen, numPrecision, numScale sql.NullInt64) string {
	lower := strings.ToLower(dataType)
	switch lower {
	case "varchar", "nvarchar", "char", "nchar", "varbinary", "binary":
		if charMaxLen.Valid {
			if charMaxLen.Int64 == -1 {
				return lower + "(max)"
			}
			return fmt.Sprintf("%s(%d)", lower, charMaxLen.Int64)
		}
	case "decimal", "numeric":
		if numPrecision.Valid && numScale.Valid {
			return fmt.Sprintf("%s(%d,%d)", lower, numPrecision.Int64, numScale.Int64)
		}
		if numPrecision.Valid {
			return fmt.Sprintf("%s(%d)", lower, numPrecision.Int64)
		}
	}
	return lower
}

func inferMSSQLDefault(raw string) (string, string) {
	v := strings.TrimSpace(raw)
	for strings.HasPrefix(v, "(") && strings.HasSuffix(v, ")") {
		v = strings.TrimSpace(v[1 : len(v)-1])
	}
	if strings.HasPrefix(v, "N'") {
		v = v[1:]
	}
	if strings.HasPrefix(v, "'") {
		return v, "string"
	}
	if isNumericLiteral(v) {
		return v, "number"
	}
	return v, "expression"
}

func isNumericLiteral(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if c == '-' && i == 0 {
			continue
		}
		if c == '.' {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
