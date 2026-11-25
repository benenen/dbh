package introspect

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

var oraclePosParamRe = regexp.MustCompile(`:[0-9]+`)

func oracleSQL(sqlText string, isDM bool) string {
	if !isDM {
		return sqlText
	}
	return oraclePosParamRe.ReplaceAllString(sqlText, "?")
}

const oracleTablesColumnsSQL = `
SELECT
  t.OWNER,
  t.TABLE_NAME,
  c.COLUMN_NAME,
  c.DATA_TYPE,
  c.DATA_LENGTH,
  c.CHAR_LENGTH,
  c.DATA_PRECISION,
  c.DATA_SCALE,
  c.NULLABLE,
  c.DATA_DEFAULT,
  tc.COMMENTS AS table_comment,
  cc.COMMENTS AS column_comment
FROM ALL_TABLES t
JOIN ALL_TAB_COLUMNS c
  ON c.OWNER = t.OWNER AND c.TABLE_NAME = t.TABLE_NAME
LEFT JOIN ALL_TAB_COMMENTS tc
  ON tc.OWNER = t.OWNER AND tc.TABLE_NAME = t.TABLE_NAME
LEFT JOIN ALL_COL_COMMENTS cc
  ON cc.OWNER = c.OWNER AND cc.TABLE_NAME = c.TABLE_NAME AND cc.COLUMN_NAME = c.COLUMN_NAME
WHERE t.OWNER = :1
  AND t.TABLE_NAME NOT LIKE 'BIN$%'
ORDER BY t.TABLE_NAME, c.COLUMN_ID
`

const oracleConstraintsSQL = `
SELECT
  ac.TABLE_NAME,
  ac.CONSTRAINT_NAME,
  ac.CONSTRAINT_TYPE,
  acc.COLUMN_NAME
FROM ALL_CONSTRAINTS ac
JOIN ALL_CONS_COLUMNS acc
  ON acc.OWNER = ac.OWNER AND acc.CONSTRAINT_NAME = ac.CONSTRAINT_NAME AND acc.TABLE_NAME = ac.TABLE_NAME
WHERE ac.OWNER = :1
  AND ac.CONSTRAINT_TYPE IN ('P', 'U')
ORDER BY ac.TABLE_NAME, ac.CONSTRAINT_NAME, acc.POSITION
`

const oracleIndexesSQL = `
SELECT
  i.TABLE_NAME,
  i.INDEX_NAME,
  i.UNIQUENESS,
  LOWER(i.INDEX_TYPE) AS index_type,
  ic.COLUMN_NAME
FROM ALL_INDEXES i
JOIN ALL_IND_COLUMNS ic
  ON ic.INDEX_OWNER = i.OWNER AND ic.INDEX_NAME = i.INDEX_NAME
WHERE i.TABLE_OWNER = :1
  AND NOT EXISTS (
    SELECT 1 FROM ALL_CONSTRAINTS ac
    WHERE ac.OWNER = i.OWNER AND ac.CONSTRAINT_NAME = i.INDEX_NAME AND ac.CONSTRAINT_TYPE IN ('P', 'U')
  )
ORDER BY i.TABLE_NAME, i.INDEX_NAME, ic.COLUMN_POSITION
`

const oracleForeignKeysSQL = `
SELECT
  ac.CONSTRAINT_NAME,
  ac.OWNER,
  ac.TABLE_NAME,
  acc.COLUMN_NAME,
  rc.OWNER AS ref_owner,
  rc.TABLE_NAME AS ref_table,
  rcc.COLUMN_NAME AS ref_col,
  ac.DELETE_RULE
FROM ALL_CONSTRAINTS ac
JOIN ALL_CONS_COLUMNS acc
  ON acc.OWNER = ac.OWNER AND acc.CONSTRAINT_NAME = ac.CONSTRAINT_NAME
JOIN ALL_CONSTRAINTS rc
  ON rc.OWNER = ac.R_OWNER AND rc.CONSTRAINT_NAME = ac.R_CONSTRAINT_NAME
JOIN ALL_CONS_COLUMNS rcc
  ON rcc.OWNER = rc.OWNER AND rcc.CONSTRAINT_NAME = rc.CONSTRAINT_NAME AND rcc.POSITION = acc.POSITION
WHERE ac.OWNER = :1
  AND ac.CONSTRAINT_TYPE = 'R'
ORDER BY ac.TABLE_NAME, ac.CONSTRAINT_NAME, acc.POSITION
`

func introspectOracleLike(ctx context.Context, db *sql.DB, schema string, isDM bool) (*DBSchema, error) {
	if schema == "" {
		q := `SELECT SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') FROM DUAL`
		if isDM {
			q = `SELECT SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA')`
		}
		if err := db.QueryRowContext(ctx, q).Scan(&schema); err != nil {
			return nil, fmt.Errorf("current schema: %w", err)
		}
	}

	result := &DBSchema{}
	tableMap := map[string]*Table{}
	var tableOrder []string

	rows, err := db.QueryContext(ctx, oracleSQL(oracleTablesColumnsSQL, isDM), schema)
	if err != nil {
		return nil, fmt.Errorf("tables_columns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			owner, tableName, colName, dataType, nullable string
			dataLen, charLen                              sql.NullInt64
			precision, scale                              sql.NullInt64
			dataDefault, tableComment, colComment         sql.NullString
		)
		if err := rows.Scan(
			&owner, &tableName, &colName, &dataType,
			&dataLen, &charLen, &precision, &scale,
			&nullable, &dataDefault, &tableComment, &colComment,
		); err != nil {
			return nil, fmt.Errorf("tables_columns scan: %w", err)
		}

		t, ok := tableMap[tableName]
		if !ok {
			comment := ""
			if tableComment.Valid {
				comment = tableComment.String
			}
			t = &Table{Schema: owner, Name: tableName, Comment: comment}
			tableMap[tableName] = t
			tableOrder = append(tableOrder, tableName)
		}

		col := &Column{
			Name:       colName,
			Type:       oracleColumnType(dataType, dataLen, charLen, precision, scale),
			IsNullable: nullable == "Y",
		}
		if colComment.Valid {
			col.Comment = colComment.String
		}
		if dataDefault.Valid {
			val := strings.TrimSpace(dataDefault.String)
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

	if !isDM {
		idRows, err := db.QueryContext(ctx,
			`SELECT TABLE_NAME, COLUMN_NAME FROM ALL_TAB_COLUMNS WHERE OWNER = :1 AND IDENTITY_COLUMN = 'YES'`,
			schema)
		if err == nil {
			defer func() { _ = idRows.Close() }()
			for idRows.Next() {
				var tblName, colName string
				if scanErr := idRows.Scan(&tblName, &colName); scanErr == nil {
					if t, ok := tableMap[tblName]; ok {
						for _, col := range t.Columns {
							if col.Name == colName {
								col.IsIncrement = true
								break
							}
						}
					}
				}
			}
		}
	}

	rows2, err := db.QueryContext(ctx, oracleSQL(oracleConstraintsSQL, isDM), schema)
	if err != nil {
		return nil, fmt.Errorf("constraints: %w", err)
	}
	defer func() { _ = rows2.Close() }()

	type conKey struct{ table, name string }
	conMap := map[conKey]*Constraint{}
	var conOrder []conKey
	for rows2.Next() {
		var tableName, conName, conType, colName string
		if err := rows2.Scan(&tableName, &conName, &conType, &colName); err != nil {
			return nil, fmt.Errorf("constraints scan: %w", err)
		}
		key := conKey{tableName, conName}
		c, ok := conMap[key]
		if !ok {
			c = &Constraint{Name: conName, IsPrimary: conType == "P"}
			conMap[key] = c
			conOrder = append(conOrder, key)
		}
		c.Columns = append(c.Columns, colName)
	}
	if err := rows2.Err(); err != nil {
		return nil, err
	}
	for _, key := range conOrder {
		t, ok := tableMap[key.table]
		if !ok {
			continue
		}
		c := conMap[key]
		t.Constraints = append(t.Constraints, c)
		if len(c.Columns) == 1 {
			for _, col := range t.Columns {
				if col.Name == c.Columns[0] {
					if c.IsPrimary {
						col.IsPrimaryKey = true
					} else {
						col.IsUnique = true
					}
					break
				}
			}
		}
	}

	rows3, err := db.QueryContext(ctx, oracleSQL(oracleIndexesSQL, isDM), schema)
	if err != nil {
		return nil, fmt.Errorf("indexes: %w", err)
	}
	defer func() { _ = rows3.Close() }()

	type idxKey struct{ table, name string }
	idxMap := map[idxKey]*Index{}
	var idxOrder []idxKey
	for rows3.Next() {
		var tableName, idxName, uniqueness, idxType, colName string
		if err := rows3.Scan(&tableName, &idxName, &uniqueness, &idxType, &colName); err != nil {
			return nil, fmt.Errorf("indexes scan: %w", err)
		}
		key := idxKey{tableName, idxName}
		idx, ok := idxMap[key]
		if !ok {
			idx = &Index{
				Name:     idxName,
				IsUnique: strings.EqualFold(uniqueness, "UNIQUE"),
				Type:     idxType,
			}
			idxMap[key] = idx
			idxOrder = append(idxOrder, key)
		}
		idx.addKey(colName, false)
	}
	if err := rows3.Err(); err != nil {
		return nil, err
	}
	for _, key := range idxOrder {
		if t, ok := tableMap[key.table]; ok {
			t.Indexes = append(t.Indexes, idxMap[key])
		}
	}

	rows4, err := db.QueryContext(ctx, oracleSQL(oracleForeignKeysSQL, isDM), schema)
	if err != nil {
		return nil, fmt.Errorf("foreign_keys: %w", err)
	}
	defer func() { _ = rows4.Close() }()

	fkMap := map[string]*ForeignKey{}
	var fkOrder []string
	for rows4.Next() {
		var fkName, owner, tableName, colName, refOwner, refTable, refCol string
		var delRule sql.NullString
		if err := rows4.Scan(&fkName, &owner, &tableName, &colName, &refOwner, &refTable, &refCol, &delRule); err != nil {
			return nil, fmt.Errorf("foreign_keys scan: %w", err)
		}
		key := owner + "." + tableName + "." + fkName
		fk, ok := fkMap[key]
		if !ok {
			fk = &ForeignKey{
				Name:      fkName,
				Schema:    owner,
				TableName: tableName,
				RefSchema: refOwner,
				RefTable:  refTable,
			}
			if delRule.Valid {
				fk.OnDelete = delRule.String
			}
			fkMap[key] = fk
			fkOrder = append(fkOrder, key)
		}
		fk.Columns = append(fk.Columns, colName)
		fk.RefColumns = append(fk.RefColumns, refCol)
	}
	if err := rows4.Err(); err != nil {
		return nil, err
	}
	for _, key := range fkOrder {
		result.FKs = append(result.FKs, fkMap[key])
	}

	return result, nil
}

func oracleColumnType(dataType string, dataLen, charLen, precision, scale sql.NullInt64) string {
	lower := strings.ToLower(dataType)
	switch lower {
	case "varchar2", "nvarchar2", "varchar", "char", "nchar", "character":
		if charLen.Valid && charLen.Int64 > 0 {
			return fmt.Sprintf("%s(%d)", lower, charLen.Int64)
		}
		if dataLen.Valid && dataLen.Int64 > 0 {
			return fmt.Sprintf("%s(%d)", lower, dataLen.Int64)
		}
	case "raw", "binary", "varbinary":
		if dataLen.Valid && dataLen.Int64 > 0 {
			return fmt.Sprintf("%s(%d)", lower, dataLen.Int64)
		}
	case "number", "decimal", "dec", "numeric":
		if precision.Valid {
			if scale.Valid && scale.Int64 > 0 {
				return fmt.Sprintf("%s(%d,%d)", lower, precision.Int64, scale.Int64)
			}
			return fmt.Sprintf("%s(%d)", lower, precision.Int64)
		}
	}
	return lower
}

func inferOracleDefault(val string) string {
	if strings.HasPrefix(val, "'") {
		return "string"
	}
	if isNumericLiteral(val) {
		return "number"
	}
	return "expression"
}
