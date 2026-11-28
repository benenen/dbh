// Package trino implements Trino / Presto connection and metadata behavior.
package trino

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/benenen/dbh/internal/database"
	trinosql "github.com/trinodb/trino-go-client/trino"
)

type Driver struct{}

var _ database.Driver = Driver{}
var _ database.CurrentDatabase = Driver{}
var _ database.ColumnNames = Driver{}
var _ database.TableColumns = Driver{}
var _ database.DDLProvider = Driver{}

var customClientSeq atomic.Uint64

type connection struct {
	database.Connection
	clientKey string
}

func (c *connection) Close() error {
	err := c.Connection.Close()
	if c.clientKey != "" {
		trinosql.DeregisterCustomClient(c.clientKey)
	}
	return err
}

func (c *connection) ReleasePool() (*sql.DB, error) {
	pool, ok := c.Connection.(database.SQLPool)
	if !ok {
		return nil, fmt.Errorf("trino connection does not expose a SQL pool")
	}
	return pool.ReleasePool()
}

func (Driver) Open(ctx context.Context, dsn string, dial database.Dial) (database.Connection, error) {
	return open(ctx, dsn, "", dial)
}

func (Driver) Syntax() database.Syntax {
	return database.Syntax{FoldUnquotedIdentifiers: true}
}

func (Driver) SwitchDatabase(ctx context.Context, _ database.Connection, dsn, name string, dial database.Dial) (database.Connection, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("schema name cannot be empty")
	}
	return open(ctx, dsn, name, dial)
}

func open(ctx context.Context, dsn, target string, dial database.Dial) (database.Connection, error) {
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "trino") || parsed.Host == "" {
		return nil, fmt.Errorf("invalid Trino connection URI")
	}

	q := parsed.Query()
	if target != "" {
		if catalog, schema, ok := strings.Cut(target, "."); ok && catalog != "" && schema != "" {
			q.Set("catalog", catalog)
			q.Set("schema", schema)
		} else {
			q.Set("schema", target)
		}
	}

	var clientKey string
	if dial != nil {
		clientKey = fmt.Sprintf("dbh-trino-%d", customClientSeq.Add(1))
		client := &http.Client{
			Transport: &http.Transport{
				DialContext:         dial,
				Proxy:               nil,
				MaxIdleConns:        10,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: 10 * time.Second,
			},
		}
		if err := trinosql.RegisterCustomClient(clientKey, client); err != nil {
			return nil, err
		}
		q.Set("custom_client", clientKey)
	}
	parsed.RawQuery = q.Encode()

	conn, err := database.OpenSQL(ctx, "trino", parsed.String())
	if err != nil {
		if clientKey != "" {
			trinosql.DeregisterCustomClient(clientKey)
		}
		return nil, err
	}
	return &connection{Connection: conn, clientKey: clientKey}, nil
}

func (Driver) CurrentDatabase(ctx context.Context, conn database.Connection) (string, error) {
	return database.ReadCurrentDatabase(ctx, conn, `SELECT COALESCE(current_catalog || '.' || current_schema, current_schema, current_catalog, '')`)
}

func (Driver) Databases(ctx context.Context, conn database.Connection) ([]string, error) {
	names, err := database.Names(ctx, conn, database.Query{Text: `SHOW SCHEMAS`})
	if err != nil {
		return database.Names(ctx, conn, database.Query{Text: `SHOW CATALOGS`})
	}
	return names, nil
}

func (Driver) Tables(ctx context.Context, conn database.Connection) ([]string, error) {
	return database.Names(ctx, conn, database.Query{Text: `SHOW TABLES`})
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
			Text: `SELECT column_name FROM information_schema.columns WHERE table_schema = ? AND table_name = ? ORDER BY ordinal_position`,
			Args: []any{schema, name},
		})
	}
	return database.Names(ctx, conn, database.Query{
		Text: `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema AND table_name = ? ORDER BY ordinal_position`,
		Args: []any{name},
	})
}

func (Driver) ColumnNames(ctx context.Context, conn database.Connection) ([]string, error) {
	return database.Names(ctx, conn, database.Query{
		Text: `SELECT DISTINCT column_name FROM information_schema.columns WHERE table_schema = current_schema`,
	})
}

func (Driver) TableColumns(ctx context.Context, conn database.Connection) (map[string][]string, error) {
	return database.ReadTableColumns(ctx, conn, `SELECT table_name, column_name FROM information_schema.columns WHERE table_schema = current_schema ORDER BY table_name, ordinal_position`)
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func (Driver) ColumnDetails(ctx context.Context, conn database.Connection, table string) (database.Query, error) {
	schema, name := splitTable(table)
	var quoted string
	if schema != "" {
		quoted = quoteIdent(schema) + "." + quoteIdent(name)
	} else {
		quoted = quoteIdent(name)
	}
	// Verify the table exists by checking DESCRIBE via a lightweight metadata query.
	rows, err := conn.Query(ctx, "DESCRIBE "+quoted)
	if err != nil {
		return database.Query{}, err
	}
	_ = rows.Close()
	return database.Query{Text: "DESCRIBE " + quoted}, nil
}

func (Driver) Indexes(_ context.Context, _ database.Connection, _ string) (database.Query, error) {
	return database.Query{}, database.ErrNoIndexes
}

func (Driver) DDL(ctx context.Context, conn database.Connection, table string) (string, error) {
	schema, name := splitTable(table)
	quoted := quoteIdent(name)
	if schema != "" {
		quoted = quoteIdent(schema) + "." + quoted
	}
	names, err := database.Names(ctx, conn, database.Query{Text: "SHOW CREATE TABLE " + quoted})
	if err != nil {
		names, err = database.Names(ctx, conn, database.Query{Text: "SHOW CREATE VIEW " + quoted})
		if err != nil {
			return "", err
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("DDL for %q is unavailable", table)
	}
	ddl := strings.TrimSpace(names[0])
	if !strings.HasSuffix(ddl, ";") {
		ddl += ";"
	}
	return ddl, nil
}

var _ = sql.Open
