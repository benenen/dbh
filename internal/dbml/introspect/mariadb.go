package introspect

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const mariaTablesColumnsSQL = `
SELECT
  t.table_name                                              AS tableName,
  COALESCE(t.table_comment, '')                            AS tableComment,
  c.column_name                                             AS columnName,
  c.column_default                                          AS columnDefault,
  CASE
    WHEN c.column_default IS NULL                          THEN 'null'
    WHEN c.column_default = 'NULL'                         THEN 'null'
    WHEN c.data_type = 'enum'                              THEN 'string'
    WHEN c.column_default REGEXP '^-?[0-9]+(\\.[0-9]+)?$' THEN 'number'
    WHEN c.extra LIKE '%DEFAULT_GENERATED%'                THEN 'expression'
    ELSE 'string'
  END                                                       AS defaultValueType,
  c.is_nullable                                             AS columnIsNullable,
  c.data_type                                               AS columnDataType,
  c.column_type                                             AS columnType,
  COALESCE(c.extra, '')                                    AS columnExtra,
  COALESCE(c.column_comment, '')                           AS columnComment,
  COALESCE(c.generation_expression, '')                    AS generationExpression
FROM information_schema.tables t
JOIN information_schema.columns c
  ON t.table_schema = c.table_schema AND t.table_name = c.table_name
WHERE t.table_schema = :schema
  AND t.table_type = 'BASE TABLE'
ORDER BY t.table_name, c.ordinal_position
`

const mariaEnumsSQL = `
SELECT
  t.table_name                                 AS tableName,
  c.column_name                                AS columnName,
  TRIM(LEADING 'enum' FROM c.column_type)      AS rawValues
FROM information_schema.tables t
JOIN information_schema.columns c
  ON t.table_schema = c.table_schema AND t.table_name = c.table_name
WHERE t.table_schema = :schema
  AND t.table_type = 'BASE TABLE'
  AND c.data_type = 'enum'
ORDER BY t.table_name, c.ordinal_position
`

const mariaConstraintsSQL = `
SELECT
  tc.table_name                                                          AS tableName,
  tc.constraint_name                                                     AS constraintName,
  GROUP_CONCAT(kcu.column_name ORDER BY kcu.ordinal_position SEPARATOR ',') AS columnNames,
  COUNT(kcu.column_name)                                                 AS columnCount,
  tc.constraint_type                                                     AS constraintType
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
  ON  tc.constraint_schema = kcu.constraint_schema
  AND tc.constraint_name   = kcu.constraint_name
  AND tc.table_name        = kcu.table_name
WHERE (tc.constraint_type = 'PRIMARY KEY' OR tc.constraint_type = 'UNIQUE')
  AND tc.table_schema = :schema
GROUP BY tc.table_name, tc.constraint_name, tc.constraint_type
ORDER BY tc.table_name, tc.constraint_name
`

const mariaIndexesSQL = `
WITH pk_fk_uniques AS (
  SELECT constraint_name, table_name
  FROM information_schema.table_constraints
  WHERE table_schema = :schema
)
SELECT
  st.table_name                                              AS tableName,
  CASE WHEN st.non_unique = 0 THEN TRUE ELSE FALSE END      AS isIdxUnique,
  st.index_name                                              AS idxName,
  st.column_name                                             AS columnName,
  st.sub_part                                                AS idxSubPart,
  st.index_type                                              AS idxType,
  NULL                                                       AS idxExpression
FROM information_schema.statistics st
WHERE st.table_schema = :schema
  AND st.index_name NOT IN (
    SELECT constraint_name
    FROM pk_fk_uniques pfu
    WHERE pfu.table_name = st.table_name
  )
  AND st.index_type IN ('BTREE', 'HASH')
ORDER BY st.table_name, st.index_name, st.seq_in_index
`

const mariaCheckConstraintsSQL = `
SELECT
  cc.constraint_name  AS constraintName,
  tc.table_name       AS tableName,
  cc.check_clause     AS checkClause
FROM information_schema.check_constraints cc
JOIN information_schema.table_constraints tc
  ON cc.constraint_name   = tc.constraint_name
  AND cc.constraint_schema = tc.constraint_schema
WHERE cc.constraint_schema = :schema
ORDER BY tc.table_name, cc.constraint_name
`

const mariaForeignKeysSQL = `
SELECT
  rc.constraint_name                                                              AS constraintName,
  rc.table_name                                                                   AS foreignTableName,
  GROUP_CONCAT(kcu.column_name            ORDER BY kcu.ordinal_position SEPARATOR ',') AS foreignColumnNames,
  kcu.referenced_table_name                                                       AS refTableName,
  GROUP_CONCAT(kcu.referenced_column_name ORDER BY kcu.ordinal_position SEPARATOR ',') AS refColumnNames,
  rc.update_rule                                                                  AS onUpdate,
  rc.delete_rule                                                                  AS onDelete
FROM information_schema.referential_constraints rc
JOIN information_schema.key_column_usage kcu
  ON  rc.constraint_name   = kcu.constraint_name
  AND rc.table_name        = kcu.table_name
  AND rc.constraint_schema = kcu.table_schema
WHERE rc.constraint_schema = :schema
GROUP BY
  rc.constraint_name, rc.table_name,
  kcu.referenced_table_name,
  rc.update_rule, rc.delete_rule
ORDER BY rc.table_name
`

func introspectMariaDB(ctx context.Context, db *sql.DB, schema string) (*DBSchema, error) {
	result := &DBSchema{}
	tableMap := map[string]*Table{}
	var tableOrder []string
	p := map[string]any{"schema": schema}

	rows, err := runMariaDB(ctx, db, mariaTablesColumnsSQL, p)
	if err != nil {
		return nil, fmt.Errorf("tables_columns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			tableName, tableComment, columnName string
			columnDefault                       sql.NullString
			defaultValueType, columnIsNullable  string
			columnDataType, columnType          string
			columnExtra, columnComment          string
			generationExpression                string
		)
		if err := rows.Scan(
			&tableName, &tableComment, &columnName,
			&columnDefault, &defaultValueType,
			&columnIsNullable, &columnDataType, &columnType,
			&columnExtra, &columnComment, &generationExpression,
		); err != nil {
			return nil, fmt.Errorf("tables_columns scan: %w", err)
		}

		t, ok := tableMap[tableName]
		if !ok {
			t = &Table{Name: tableName, Comment: tableComment}
			tableMap[tableName] = t
			tableOrder = append(tableOrder, tableName)
		}

		col := &Column{
			Name:        columnName,
			Type:        columnType,
			IsNullable:  columnIsNullable == "YES",
			Comment:     columnComment,
			Generated:   generationExpression,
			IsIncrement: strings.Contains(columnExtra, "auto_increment"),
		}
		if columnDefault.Valid && defaultValueType != "null" {
			col.Default = &columnDefault.String
			col.DefaultType = defaultValueType
		}
		t.Columns = append(t.Columns, col)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, name := range tableOrder {
		result.Tables = append(result.Tables, tableMap[name])
	}

	rows2, err := runMariaDB(ctx, db, mariaEnumsSQL, p)
	if err != nil {
		return nil, fmt.Errorf("enums: %w", err)
	}
	defer func() { _ = rows2.Close() }()

	for rows2.Next() {
		var tableName, columnName, rawValues string
		if err := rows2.Scan(&tableName, &columnName, &rawValues); err != nil {
			return nil, fmt.Errorf("enums scan: %w", err)
		}
		enumName := tableName + "_" + columnName
		result.Enums = append(result.Enums, &Enum{
			Name:   enumName,
			Values: parseEnumValues(rawValues),
		})
		if t, ok := tableMap[tableName]; ok {
			for _, col := range t.Columns {
				if col.Name == columnName {
					col.Type = enumName
					break
				}
			}
		}
	}
	if err := rows2.Err(); err != nil {
		return nil, err
	}

	rows3, err := runMariaDB(ctx, db, mariaConstraintsSQL, p)
	if err != nil {
		return nil, fmt.Errorf("constraints: %w", err)
	}
	defer func() { _ = rows3.Close() }()

	for rows3.Next() {
		var tableName, constraintName, columnNames, constraintType string
		var columnCount int
		if err := rows3.Scan(&tableName, &constraintName, &columnNames, &columnCount, &constraintType); err != nil {
			return nil, fmt.Errorf("constraints scan: %w", err)
		}
		t, ok := tableMap[tableName]
		if !ok {
			continue
		}
		cols := strings.Split(columnNames, ",")
		isPrimary := constraintType == "PRIMARY KEY"

		if !isPrimary && len(cols) == 1 && constraintName != cols[0] {
			idx := &Index{Name: constraintName, IsUnique: true, Type: "BTREE"}
			idx.addKey(cols[0], false)
			t.Indexes = append(t.Indexes, idx)
			continue
		}

		t.Constraints = append(t.Constraints, &Constraint{
			Name:      constraintName,
			Columns:   cols,
			IsPrimary: isPrimary,
		})
		if len(cols) == 1 {
			for _, col := range t.Columns {
				if col.Name == cols[0] {
					if isPrimary {
						col.IsPrimaryKey = true
					} else {
						col.IsUnique = true
					}
					break
				}
			}
		}
	}
	if err := rows3.Err(); err != nil {
		return nil, err
	}

	rows4, err := runMariaDB(ctx, db, mariaIndexesSQL, p)
	if err != nil {
		return nil, fmt.Errorf("indexes: %w", err)
	}
	defer func() { _ = rows4.Close() }()

	type idxKey struct{ table, name string }
	idxMap := map[idxKey]*Index{}
	var idxOrder []idxKey

	for rows4.Next() {
		var (
			tableName, idxName, idxType string
			isUniqueInt                 int64
			columnName                  sql.NullString
			idxSubPart                  sql.NullInt64
			idxExpression               sql.NullString
		)
		if err := rows4.Scan(&tableName, &isUniqueInt, &idxName, &columnName,
			&idxSubPart, &idxType, &idxExpression); err != nil {
			return nil, fmt.Errorf("indexes scan: %w", err)
		}
		key := idxKey{tableName, idxName}
		idx, ok := idxMap[key]
		if !ok {
			idx = &Index{
				Name:     idxName,
				IsUnique: isUniqueInt != 0,
				Type:     idxType,
			}
			idxMap[key] = idx
			idxOrder = append(idxOrder, key)
		}
		if idxExpression.Valid && idxExpression.String != "" {
			idx.addKey(idxExpression.String, true)
		} else if columnName.Valid && columnName.String != "" {
			idx.addKey(columnName.String, false)
		}
	}
	if err := rows4.Err(); err != nil {
		return nil, err
	}
	for _, key := range idxOrder {
		if t, ok := tableMap[key.table]; ok && len(idxMap[key].Columns) > 0 {
			t.Indexes = append(t.Indexes, idxMap[key])
		}
	}

	rows5, err := runMariaDB(ctx, db, mariaCheckConstraintsSQL, p)
	if err != nil {
		_ = err
	} else {
		defer func() { _ = rows5.Close() }()
		for rows5.Next() {
			var constraintName, tableName, checkClause string
			if err := rows5.Scan(&constraintName, &tableName, &checkClause); err != nil {
				return nil, fmt.Errorf("check_constraints scan: %w", err)
			}
			if t, ok := tableMap[tableName]; ok {
				t.CheckConstraints = append(t.CheckConstraints, &CheckConstraint{
					Name:       constraintName,
					Expression: checkClause,
				})
			}
		}
		if err := rows5.Err(); err != nil {
			return nil, err
		}
	}

	rows6, err := runMariaDB(ctx, db, mariaForeignKeysSQL, p)
	if err != nil {
		return nil, fmt.Errorf("foreign_keys: %w", err)
	}
	defer func() { _ = rows6.Close() }()

	for rows6.Next() {
		var constraintName, foreignTableName, foreignColumnNames string
		var refTableName, refColumnNames, onUpdate, onDelete string
		if err := rows6.Scan(
			&constraintName, &foreignTableName, &foreignColumnNames,
			&refTableName, &refColumnNames, &onUpdate, &onDelete,
		); err != nil {
			return nil, fmt.Errorf("foreign_keys scan: %w", err)
		}
		result.FKs = append(result.FKs, &ForeignKey{
			Name:       constraintName,
			TableName:  foreignTableName,
			Columns:    strings.Split(foreignColumnNames, ","),
			RefTable:   refTableName,
			RefColumns: strings.Split(refColumnNames, ","),
			OnDelete:   onDelete,
			OnUpdate:   onUpdate,
		})
	}
	return result, rows6.Err()
}

func parseEnumValues(raw string) []string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "(")
	raw = strings.TrimSuffix(raw, ")")
	var values []string
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\'' {
			continue
		}
		i++
		var b strings.Builder
		for i < len(raw) {
			if raw[i] == '\\' && i+1 < len(raw) {
				b.WriteByte(raw[i+1])
				i += 2
				continue
			}
			if raw[i] == '\'' {
				if i+1 < len(raw) && raw[i+1] == '\'' {
					b.WriteByte('\'')
					i += 2
					continue
				}
				break
			}
			b.WriteByte(raw[i])
			i++
		}
		values = append(values, b.String())
	}
	return values
}
