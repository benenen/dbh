package introspect

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const pgTablesColumnsSQL = `
WITH comments AS (
  SELECT DISTINCT ON (pc.relname, pn.nspname, pa.attname)
    pc.relname  AS table_name,
    pn.nspname  AS table_schema,
    pa.attname  AS column_name,
    pd.description
  FROM pg_description pd
  JOIN pg_class     pc ON pd.objoid = pc.oid
  JOIN pg_namespace pn ON pc.relnamespace = pn.oid
  LEFT JOIN pg_attribute pa
    ON pd.objoid = pa.attrelid AND pd.objsubid = pa.attnum
  WHERE pc.relkind = 'r'
    AND pn.nspname NOT IN ('pg_catalog', 'information_schema')
),
table_positions AS (
  SELECT
    pc.oid      AS table_ordinal_position,
    pc.relname  AS table_name,
    pn.nspname  AS table_schema
  FROM pg_class     pc
  JOIN pg_namespace pn ON pc.relnamespace = pn.oid
  WHERE pc.relkind = 'r'
    AND pn.nspname = :schema
)
SELECT
  t.table_schema,
  t.table_name,
  c.column_name,
  c.data_type,
  c.character_maximum_length,
  c.numeric_precision,
  c.numeric_scale,
  c.datetime_precision,
  c.udt_schema,
  c.udt_name,
  c.identity_increment,
  c.is_nullable,
  c.column_default,
  c.ordinal_position,
  CASE
    WHEN c.column_default IS NULL                              THEN NULL
    WHEN c.column_default LIKE 'nextval(%'                    THEN 'increment'
    WHEN c.column_default LIKE '''%'                          THEN 'string'
    WHEN c.column_default = 'true' OR c.column_default = 'false' THEN 'boolean'
    WHEN c.column_default ~ '^-?[0-9]+(.[0-9]+)?$'           THEN 'number'
    ELSE 'expression'
  END AS default_type,
  (SELECT description FROM comments
   WHERE table_name = t.table_name AND table_schema = t.table_schema
     AND column_name IS NULL LIMIT 1)                         AS table_comment,
  (SELECT description FROM comments
   WHERE table_name = t.table_name AND table_schema = t.table_schema
     AND column_name = c.column_name LIMIT 1)                AS column_comment
FROM information_schema.columns c
JOIN information_schema.tables  t
  ON c.table_name = t.table_name AND c.table_schema = t.table_schema
JOIN table_positions tp
  ON tp.table_name = t.table_name AND tp.table_schema = t.table_schema
WHERE t.table_type = 'BASE TABLE'
  AND t.table_schema = :schema
ORDER BY t.table_schema, tp.table_ordinal_position, c.ordinal_position
`

const pgIndexesSQL = `
WITH user_tables AS (
  SELECT schemaname AS tableschema, tablename
  FROM pg_tables
  WHERE schemaname = :schema
),
index_info AS (
  SELECT
    t.relnamespace::regnamespace::text                       AS table_schema,
    t.relname                                                AS table_name,
    i.relname                                                AS index_name,
    ix.indisunique                                           AS is_unique,
    ix.indisprimary                                          AS is_primary,
    am.amname                                                AS index_type,
    (
      SELECT string_agg(
        CASE
          WHEN ix.indkey[k - 1] = 0
            THEN 'E:' || pg_get_indexdef(ix.indexrelid, k, true)
          ELSE 'C:' || (
            SELECT a.attname FROM pg_attribute a
            WHERE a.attrelid = ix.indrelid AND a.attnum = ix.indkey[k - 1]
          )
        END,
        chr(31) ORDER BY k)
      FROM generate_series(1, ix.indnkeyatts) AS k
    )                                                        AS index_keys,
    CASE
      WHEN ix.indisprimary THEN 'PRIMARY KEY'
      WHEN EXISTS (
        SELECT 1 FROM pg_constraint con
        WHERE con.conindid = ix.indexrelid AND con.contype = 'u'
      ) THEN 'UNIQUE'
      ELSE NULL
    END                                                      AS constraint_type
  FROM pg_class t
  JOIN pg_index ix ON t.oid = ix.indrelid
  JOIN pg_class i  ON i.oid = ix.indexrelid
  JOIN pg_am am ON i.relam = am.oid
  WHERE t.relkind = 'r'
)
SELECT
  ut.tableschema   AS table_schema,
  ut.tablename     AS table_name,
  ii.index_name,
  ii.is_unique,
  ii.is_primary,
  ii.index_type,
  ii.index_keys,
  ii.constraint_type
FROM user_tables ut
JOIN index_info ii
  ON ut.tableschema = ii.table_schema AND ut.tablename = ii.table_name
WHERE ii.index_keys IS NOT NULL
ORDER BY ut.tablename, ii.constraint_type NULLS LAST, ii.index_name
`

const pgCheckConstraintsSQL = `
SELECT
  n.nspname                        AS table_schema,
  c.relname                        AS table_name,
  MAX(a.attname)                   AS column_name,
  con.conname                      AS check_name,
  pg_get_constraintdef(con.oid)    AS check_definition,
  CASE WHEN COUNT(*) = 1 THEN TRUE ELSE FALSE END AS is_column_constraint
FROM pg_catalog.pg_constraint AS con
JOIN pg_catalog.pg_class      AS c  ON con.conrelid = c.oid
LEFT JOIN pg_catalog.pg_namespace AS n ON c.relnamespace = n.oid
LEFT JOIN pg_catalog.pg_attribute AS a
  ON a.attrelid = c.oid AND a.attnum = ANY(con.conkey)
WHERE con.contype = 'c'
  AND n.nspname = :schema
GROUP BY con.conname, n.nspname, c.relname, con.oid
ORDER BY table_name, check_name
`

const pgEnumsSQL = `
SELECT DISTINCT
  n.nspname        AS schema_name,
  t.typname        AS enum_type,
  e.enumlabel      AS enum_value,
  e.enumsortorder  AS sort_order
FROM pg_enum      e
JOIN pg_type      t ON e.enumtypid   = t.oid
JOIN pg_namespace n ON t.typnamespace = n.oid
WHERE n.nspname = :schema
ORDER BY schema_name, enum_type, sort_order
`

const pgForeignKeysSQL = `
SELECT
  ns.nspname                                              AS table_schema,
  cl.relname                                              AS table_name,
  con.conname                                             AS fk_constraint_name,
  STRING_AGG(att.attname, ',' ORDER BY k.ord)             AS column_names,
  fns.nspname                                             AS foreign_table_schema,
  fcl.relname                                             AS foreign_table_name,
  STRING_AGG(fatt.attname, ',' ORDER BY k.ord)            AS foreign_column_names,
  CASE con.confdeltype
    WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT' WHEN 'c' THEN 'CASCADE'
    WHEN 'n' THEN 'SET NULL'  WHEN 'd' THEN 'SET DEFAULT'
  END                                                     AS on_delete,
  CASE con.confupdtype
    WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT' WHEN 'c' THEN 'CASCADE'
    WHEN 'n' THEN 'SET NULL'  WHEN 'd' THEN 'SET DEFAULT'
  END                                                     AS on_update
FROM pg_constraint AS con
JOIN pg_class      AS cl  ON cl.oid  = con.conrelid
JOIN pg_namespace  AS ns  ON ns.oid  = cl.relnamespace
JOIN pg_class      AS fcl ON fcl.oid = con.confrelid
JOIN pg_namespace  AS fns ON fns.oid = fcl.relnamespace
CROSS JOIN LATERAL UNNEST(con.conkey, con.confkey) WITH ORDINALITY AS k(attnum, fattnum, ord)
JOIN pg_attribute  AS att  ON att.attrelid  = con.conrelid  AND att.attnum  = k.attnum
JOIN pg_attribute  AS fatt ON fatt.attrelid = con.confrelid AND fatt.attnum = k.fattnum
WHERE con.contype = 'f'
  AND ns.nspname = :schema
GROUP BY
  ns.nspname, cl.relname, con.conname,
  fns.nspname, fcl.relname,
  con.confdeltype, con.confupdtype
ORDER BY ns.nspname, cl.relname, con.conname
`

func introspectPostgres(ctx context.Context, db *sql.DB, schema string) (*DBSchema, error) {
	result := &DBSchema{}
	tableMap := map[string]*Table{}
	var tableOrder []string
	p := map[string]any{"schema": schema}

	rows, err := runPostgres(ctx, db, pgTablesColumnsSQL, p)
	if err != nil {
		return nil, fmt.Errorf("tables_columns: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			tableSchema, tableName, columnName string
			dataType                           string
			charMaxLen                         sql.NullInt64
			numPrecision, numScale             sql.NullInt64
			dtPrecision                        sql.NullInt64
			udtSchema, udtName                 string
			identityIncrement                  sql.NullString
			isNullable                         string
			columnDefault                      sql.NullString
			ordinalPosition                    int
			defaultType                        sql.NullString
			tableComment, columnComment        sql.NullString
		)
		if err := rows.Scan(
			&tableSchema, &tableName, &columnName,
			&dataType, &charMaxLen, &numPrecision, &numScale, &dtPrecision,
			&udtSchema, &udtName,
			&identityIncrement, &isNullable, &columnDefault, &ordinalPosition,
			&defaultType, &tableComment, &columnComment,
		); err != nil {
			return nil, fmt.Errorf("tables_columns scan: %w", err)
		}

		t, ok := tableMap[tableName]
		if !ok {
			comment := ""
			if tableComment.Valid {
				comment = tableComment.String
			}
			t = &Table{Schema: tableSchema, Name: tableName, Comment: comment}
			tableMap[tableName] = t
			tableOrder = append(tableOrder, tableName)
		}

		col := &Column{
			Name:       columnName,
			Type:       pgColumnType(dataType, charMaxLen, numPrecision, numScale, dtPrecision, udtSchema, udtName),
			IsNullable: isNullable == "YES",
		}
		if columnComment.Valid {
			col.Comment = columnComment.String
		}
		if columnDefault.Valid {
			col.Default = &columnDefault.String
			if defaultType.Valid {
				col.DefaultType = defaultType.String
				col.IsIncrement = col.DefaultType == "increment"
			}
		}
		if identityIncrement.Valid {
			col.IsIncrement = true
		}
		t.Columns = append(t.Columns, col)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, name := range tableOrder {
		result.Tables = append(result.Tables, tableMap[name])
	}

	rows2, err := runPostgres(ctx, db, pgIndexesSQL, p)
	if err != nil {
		return nil, fmt.Errorf("indexes: %w", err)
	}
	defer rows2.Close()

	for rows2.Next() {
		var (
			tableSchema, tableName, indexName string
			isUnique, isPrimary               bool
			indexType                         string
			indexKeys                         string
			constraintType                    sql.NullString
		)
		if err := rows2.Scan(
			&tableSchema, &tableName, &indexName,
			&isUnique, &isPrimary, &indexType,
			&indexKeys, &constraintType,
		); err != nil {
			return nil, fmt.Errorf("indexes scan: %w", err)
		}

		t, ok := tableMap[tableName]
		if !ok {
			continue
		}
		idx := &Index{
			Name:     indexName,
			IsUnique: isUnique,
			Type:     indexType,
		}
		for _, key := range strings.Split(indexKeys, "\x1f") {
			if expr, ok := strings.CutPrefix(key, "E:"); ok {
				idx.addKey(expr, true)
			} else {
				idx.addKey(strings.TrimPrefix(key, "C:"), false)
			}
		}

		if isPrimary || (isUnique && constraintType.Valid) {
			cols := idx.Columns
			isPK := isPrimary
			t.Constraints = append(t.Constraints, &Constraint{Name: indexName, Columns: cols, IsPrimary: isPK})
			if len(cols) == 1 {
				for _, col := range t.Columns {
					if col.Name == cols[0] {
						if isPK {
							col.IsPrimaryKey = true
						} else {
							col.IsUnique = true
						}
						break
					}
				}
			}
		} else {
			t.Indexes = append(t.Indexes, idx)
		}
	}
	if err := rows2.Err(); err != nil {
		return nil, err
	}

	rows3, err := runPostgres(ctx, db, pgCheckConstraintsSQL, p)
	if err != nil {
		return nil, fmt.Errorf("check_constraints: %w", err)
	}
	defer rows3.Close()

	for rows3.Next() {
		var tableSchema, tableName string
		var columnName, checkName, checkDef sql.NullString
		var isColConstraint bool
		if err := rows3.Scan(&tableSchema, &tableName, &columnName, &checkName, &checkDef, &isColConstraint); err != nil {
			return nil, fmt.Errorf("check_constraints scan: %w", err)
		}
		if t, ok := tableMap[tableName]; ok && checkName.Valid && checkDef.Valid {
			cc := &CheckConstraint{Name: checkName.String, Expression: checkDef.String}
			if columnName.Valid {
				cc.ColumnName = columnName.String
			}
			t.CheckConstraints = append(t.CheckConstraints, cc)
		}
	}
	if err := rows3.Err(); err != nil {
		return nil, err
	}

	rows4, err := runPostgres(ctx, db, pgEnumsSQL, p)
	if err != nil {
		return nil, fmt.Errorf("enums: %w", err)
	}
	defer rows4.Close()

	enumMap := map[string]*Enum{}
	var enumOrder []string

	for rows4.Next() {
		var schemaName, enumType, enumValue string
		var sortOrder float64
		if err := rows4.Scan(&schemaName, &enumType, &enumValue, &sortOrder); err != nil {
			return nil, fmt.Errorf("enums scan: %w", err)
		}
		key := schemaName + "." + enumType
		e, ok := enumMap[key]
		if !ok {
			e = &Enum{Schema: schemaName, Name: enumType}
			enumMap[key] = e
			enumOrder = append(enumOrder, key)
		}
		e.Values = append(e.Values, enumValue)
	}
	if err := rows4.Err(); err != nil {
		return nil, err
	}
	for _, key := range enumOrder {
		result.Enums = append(result.Enums, enumMap[key])
	}

	rows5, err := runPostgres(ctx, db, pgForeignKeysSQL, p)
	if err != nil {
		return nil, fmt.Errorf("foreign_keys: %w", err)
	}
	defer rows5.Close()

	for rows5.Next() {
		var (
			tableSchema, tableName, fkName string
			columnNames                    string
			foreignTableSchema             string
			foreignTableName               string
			foreignColumnNames             string
			onDelete, onUpdate             sql.NullString
		)
		if err := rows5.Scan(
			&tableSchema, &tableName, &fkName,
			&columnNames, &foreignTableSchema, &foreignTableName, &foreignColumnNames,
			&onDelete, &onUpdate,
		); err != nil {
			return nil, fmt.Errorf("foreign_keys scan: %w", err)
		}
		fk := &ForeignKey{
			Name:       fkName,
			TableName:  tableName,
			Schema:     tableSchema,
			Columns:    splitCols(columnNames),
			RefSchema:  foreignTableSchema,
			RefTable:   foreignTableName,
			RefColumns: splitCols(foreignColumnNames),
		}
		if onDelete.Valid {
			fk.OnDelete = onDelete.String
		}
		if onUpdate.Valid {
			fk.OnUpdate = onUpdate.String
		}
		result.FKs = append(result.FKs, fk)
	}
	return result, rows5.Err()
}

func pgColumnType(dataType string, charMaxLen, numPrecision, numScale, dtPrecision sql.NullInt64, udtSchema, udtName string) string {
	precision := ""
	if dtPrecision.Valid && dtPrecision.Int64 != 6 {
		precision = fmt.Sprintf("(%d)", dtPrecision.Int64)
	}
	switch strings.ToLower(dataType) {
	case "character varying":
		if charMaxLen.Valid {
			return fmt.Sprintf("varchar(%d)", charMaxLen.Int64)
		}
		return "varchar"
	case "character":
		if charMaxLen.Valid {
			return fmt.Sprintf("char(%d)", charMaxLen.Int64)
		}
		return "char"
	case "numeric", "decimal":
		if numPrecision.Valid && numScale.Valid {
			return fmt.Sprintf("%s(%d,%d)", strings.ToLower(dataType), numPrecision.Int64, numScale.Int64)
		}
		if numPrecision.Valid {
			return fmt.Sprintf("%s(%d)", strings.ToLower(dataType), numPrecision.Int64)
		}
		return strings.ToLower(dataType)
	case "timestamp without time zone":
		return "timestamp" + precision
	case "timestamp with time zone":
		return "timestamptz" + precision
	case "time without time zone":
		return "time" + precision
	case "time with time zone":
		return "timetz" + precision
	case "double precision":
		return "float8"
	case "real":
		return "float4"
	case "user-defined":
		if udtSchema != "" && udtSchema != "pg_catalog" && udtSchema != "public" {
			return udtSchema + "." + udtName
		}
		return udtName
	case "array":
		if strings.HasPrefix(udtName, "_") {
			return udtName[1:] + "[]"
		}
		return udtName + "[]"
	default:
		return strings.ToLower(dataType)
	}
}

func splitCols(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
