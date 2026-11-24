package introspect

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// Introspect reads the schema of an open *sql.DB using engine-specific catalog
// queries and returns it as a DBSchema.
func Introspect(ctx context.Context, db *sql.DB, engine Engine, schema string) (*DBSchema, error) {
	switch engine {
	case EngineMariaDB:
		return introspectMariaDB(ctx, db, schema)
	case EnginePostgres:
		return introspectPostgres(ctx, db, schema)
	case EngineSQLite:
		return introspectSQLite(ctx, db)
	case EngineMSSQL:
		return introspectMSSQL(ctx, db, schema)
	case EngineOracle:
		return introspectOracleLike(ctx, db, schema, false)
	case EngineDM:
		return introspectOracleLike(ctx, db, schema, true)
	case EngineTrino:
		return introspectTrino(ctx, db, schema)
	default:
		return nil, fmt.Errorf("unsupported engine %d", engine)
	}
}

var namedParamRe = regexp.MustCompile(`:([a-zA-Z_][a-zA-Z0-9_]*)`)

func runMariaDB(ctx context.Context, db *sql.DB, sqlText string, params map[string]any) (*sql.Rows, error) {
	var args []any
	prepared := namedParamRe.ReplaceAllStringFunc(sqlText, func(match string) string {
		args = append(args, params[match[1:]])
		return "?"
	})
	return db.QueryContext(ctx, prepared, args...)
}

func runPostgres(ctx context.Context, db *sql.DB, sqlText string, params map[string]any) (*sql.Rows, error) {
	const castMark = "\x00\x00"
	sqlText = strings.ReplaceAll(sqlText, "::", castMark)

	idx := map[string]int{}
	counter := 1
	var args []any

	prepared := namedParamRe.ReplaceAllStringFunc(sqlText, func(match string) string {
		name := match[1:]
		if _, ok := idx[name]; !ok {
			idx[name] = counter
			args = append(args, params[name])
			counter++
		}
		return fmt.Sprintf("$%d", idx[name])
	})

	prepared = strings.ReplaceAll(prepared, castMark, "::")
	return db.QueryContext(ctx, prepared, args...)
}
