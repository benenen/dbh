// Package clickhouse implements ClickHouse connections and system metadata.
package clickhouse

import (
	"context"
	"fmt"
	"strings"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/benenen/dbh/internal/database"
)

type Driver struct{}

var _ database.Driver = Driver{}

func (Driver) Open(ctx context.Context, dsn string) (database.Connection, error) {
	options, err := ch.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("invalid connection configuration for clickhouse")
	}
	return connect(ctx, options)
}
func (Driver) Syntax() database.Syntax {
	return database.Syntax{BackslashEscapes: true, HashComments: true}
}
func (Driver) SwitchDatabase(ctx context.Context, _ database.Connection, dsn, name string) (database.Connection, error) {
	options, err := ch.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("invalid connection configuration for clickhouse")
	}
	options.Auth.Database = name
	return connect(ctx, options)
}
func (Driver) Databases(ctx context.Context, conn database.Connection) ([]string, error) {
	return database.Names(ctx, conn, database.Query{Text: "SELECT name FROM system.databases ORDER BY name"})
}
func (Driver) Tables(ctx context.Context, conn database.Connection) ([]string, error) {
	return database.Names(ctx, conn, database.Query{Text: "SELECT name FROM system.tables WHERE database=currentDatabase() ORDER BY name"})
}
func tableArgs(table string) []any {
	schema, name := "", table
	if parts := strings.SplitN(table, ".", 2); len(parts) == 2 {
		schema, name = parts[0], parts[1]
	}
	return []any{schema, name}
}

const tableFilter = "database=if(?='', currentDatabase(), ?) AND table=?"

func args(table string) []any { a := tableArgs(table); return []any{a[0], a[0], a[1]} }
func (Driver) Columns(ctx context.Context, conn database.Connection, table string) ([]string, error) {
	return database.Names(ctx, conn, database.Query{Text: "SELECT name FROM system.columns WHERE " + tableFilter + " ORDER BY position", Args: args(table)})
}
func resolve(ctx context.Context, conn database.Connection, table string) error {
	a := tableArgs(table)
	names, err := database.Names(ctx, conn, database.Query{Text: "SELECT name FROM system.tables WHERE database=if(?='',currentDatabase(),?) AND name=?", Args: []any{a[0], a[0], a[1]}})
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return fmt.Errorf("table or view %q does not exist", table)
	}
	return nil
}
func (Driver) ColumnDetails(ctx context.Context, conn database.Connection, table string) (database.Query, error) {
	if err := resolve(ctx, conn, table); err != nil {
		return database.Query{}, err
	}
	return database.Query{Text: "SELECT name, type, startsWith(type,'Nullable(') AS nullable, default_kind, default_expression, is_in_primary_key, is_in_sorting_key, comment FROM system.columns WHERE " + tableFilter + " ORDER BY position", Args: args(table)}, nil
}
func (Driver) Indexes(ctx context.Context, conn database.Connection, table string) (database.Query, error) {
	if err := resolve(ctx, conn, table); err != nil {
		return database.Query{}, err
	}
	return database.Query{Text: "SELECT 'PRIMARY KEY' AS name, 'primary' AS type, primary_key AS expression, toUInt64(0) AS granularity FROM system.tables WHERE database=if(?='',currentDatabase(),?) AND name=? AND primary_key!='' UNION ALL SELECT name,type,expr AS expression,granularity FROM system.data_skipping_indices WHERE " + tableFilter, Args: append(args(table), args(table)...)}, nil
}

func connect(ctx context.Context, options *ch.Options) (database.Connection, error) {
	conn, err := database.ConnectSQL(ctx, ch.OpenDB(options))
	if err != nil {
		return nil, err
	}
	return &connection{conn}, nil
}
