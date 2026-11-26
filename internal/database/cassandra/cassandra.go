// Package cassandra implements Cassandra/ScyllaDB CQL sessions and schema metadata using gocql.
package cassandra

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/benenen/dbh/internal/database"
	"github.com/gocql/gocql"
)

type Driver struct{}

var _ database.Driver = Driver{}
var _ database.CurrentDatabase = Driver{}

type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

func (d dialFunc) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d(ctx, network, addr)
}

type connection struct {
	session  *gocql.Session
	keyspace string
}

func (Driver) Open(ctx context.Context, dsn string, dial database.Dial) (database.Connection, error) {
	return open(ctx, dsn, "", dial)
}

func (Driver) SwitchDatabase(ctx context.Context, _ database.Connection, dsn, name string, dial database.Dial) (database.Connection, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("keyspace name cannot be empty")
	}
	return open(ctx, dsn, name, dial)
}

func open(ctx context.Context, dsn, overrideKeyspace string, dial database.Dial) (database.Connection, error) {
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme != "cassandra" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid cassandra connection URI")
	}

	hosts := strings.Split(parsed.Host, ",")
	cluster := gocql.NewCluster(hosts...)
	cluster.Timeout = 10 * time.Second
	cluster.ConnectTimeout = 10 * time.Second
	cluster.DisableInitialHostLookup = true

	if parsed.Port() != "" {
		if p, err := strconv.Atoi(parsed.Port()); err == nil && p > 0 {
			cluster.Port = p
		}
	}
	if parsed.User != nil {
		user := parsed.User.Username()
		pass, _ := parsed.User.Password()
		cluster.Authenticator = gocql.PasswordAuthenticator{
			Username: user,
			Password: pass,
		}
	}

	keyspace := strings.Trim(parsed.Path, "/")
	if overrideKeyspace != "" {
		keyspace = overrideKeyspace
	}
	if keyspace != "" {
		cluster.Keyspace = keyspace
	}

	q := parsed.Query()
	if cons := strings.TrimSpace(q.Get("consistency")); cons != "" {
		if parsedCons, err := gocql.ParseConsistencyWrapper(cons); err == nil {
			cluster.Consistency = parsedCons
		}
	}
	if t := strings.TrimSpace(q.Get("timeout")); t != "" {
		if d, err := time.ParseDuration(t); err == nil && d > 0 {
			cluster.Timeout = d
			cluster.ConnectTimeout = d
		}
	}
	if dial != nil {
		cluster.Dialer = dialFunc(dial)
	}

	session, err := cluster.CreateSession()
	if err != nil {
		return nil, fmt.Errorf("cassandra connect failed: %w", err)
	}
	conn := &connection{session: session, keyspace: keyspace}
	if err := conn.Exec(ctx, "SELECT release_version FROM system.local"); err != nil {
		session.Close()
		return nil, err
	}
	return conn, nil
}

func (Driver) Syntax() database.Syntax {
	return database.Syntax{
		CompletionWords: strings.Fields(
			"SELECT INSERT UPDATE DELETE CREATE ALTER DROP TRUNCATE USE KEYSPACE TABLE INDEX MATERIALIZED VIEW ALLOW FILTERING",
		),
	}
}

func (Driver) CurrentDatabase(_ context.Context, conn database.Connection) (string, error) {
	c, err := cassandraConnection(conn)
	if err != nil {
		return "", err
	}
	return c.keyspace, nil
}

func (Driver) Databases(ctx context.Context, conn database.Connection) ([]string, error) {
	names, err := database.Names(ctx, conn, database.Query{Text: "SELECT keyspace_name FROM system_schema.keyspaces"})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

func (Driver) Tables(ctx context.Context, conn database.Connection) ([]string, error) {
	c, err := cassandraConnection(conn)
	if err != nil {
		return nil, err
	}
	if c.keyspace == "" {
		return nil, fmt.Errorf("no keyspace selected; pass keyspace in DSN or use --db")
	}
	names, err := database.Names(ctx, conn, database.Query{
		Text: "SELECT table_name FROM system_schema.tables WHERE keyspace_name = ?",
		Args: []any{c.keyspace},
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

func (Driver) Columns(ctx context.Context, conn database.Connection, table string) ([]string, error) {
	c, err := cassandraConnection(conn)
	if err != nil {
		return nil, err
	}
	ks, tbl := splitTable(c.keyspace, table)
	if ks == "" {
		return nil, fmt.Errorf("no keyspace selected; pass keyspace in DSN or use --db")
	}
	return database.Names(ctx, conn, database.Query{
		Text: "SELECT column_name FROM system_schema.columns WHERE keyspace_name = ? AND table_name = ?",
		Args: []any{ks, tbl},
	})
}

func (Driver) ColumnDetails(_ context.Context, conn database.Connection, table string) (database.Query, error) {
	c, err := cassandraConnection(conn)
	if err != nil {
		return database.Query{}, err
	}
	ks, tbl := splitTable(c.keyspace, table)
	if ks == "" {
		return database.Query{}, fmt.Errorf("no keyspace selected; pass keyspace in DSN or use --db")
	}
	return database.Query{
		Text:    "SELECT column_name, type, kind, position FROM system_schema.columns WHERE keyspace_name = ? AND table_name = ?",
		Args:    []any{ks, tbl},
		Heading: "Columns:",
	}, nil
}

func (Driver) Indexes(_ context.Context, conn database.Connection, table string) (database.Query, error) {
	c, err := cassandraConnection(conn)
	if err != nil {
		return database.Query{}, err
	}
	ks, tbl := splitTable(c.keyspace, table)
	if ks == "" {
		return database.Query{}, fmt.Errorf("no keyspace selected; pass keyspace in DSN or use --db")
	}
	return database.Query{
		Text:    "SELECT index_name, kind, options FROM system_schema.indexes WHERE keyspace_name = ? AND table_name = ?",
		Args:    []any{ks, tbl},
		Heading: "Indexes:",
	}, nil
}

func splitTable(defaultKeyspace, table string) (string, string) {
	parts := database.SplitIdentifier(table)
	if len(parts) >= 2 {
		return parts[len(parts)-2], parts[len(parts)-1]
	}
	return defaultKeyspace, parts[0]
}

func (c *connection) Query(ctx context.Context, text string, args ...any) (database.Rows, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, fmt.Errorf("cassandra CQL statement cannot be empty")
	}
	iter := c.session.Query(trimmed, args...).WithContext(ctx).Iter()
	colInfos := iter.Columns()
	cols := make([]string, len(colInfos))
	for i, ci := range colInfos {
		cols[i] = ci.Name
	}
	var rows [][]any
	for {
		rowMap := make(map[string]any, len(cols))
		if !iter.MapScan(rowMap) {
			break
		}
		vals := make([]any, len(cols))
		for i, col := range cols {
			vals[i] = normalizeCell(rowMap[col])
		}
		rows = append(rows, vals)
	}
	if err := iter.Close(); err != nil {
		return nil, err
	}
	return newStaticRows(cols, rows), nil
}

func normalizeCell(v any) any {
	switch val := v.(type) {
	case []byte:
		return string(val)
	case gocql.UUID:
		return val.String()
	}
	return v
}

func (c *connection) Exec(ctx context.Context, text string, args ...any) error {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return fmt.Errorf("cassandra CQL statement cannot be empty")
	}
	return c.session.Query(trimmed, args...).WithContext(ctx).Exec()
}

func (c *connection) Close() error {
	c.session.Close()
	return nil
}

func cassandraConnection(conn database.Connection) (*connection, error) {
	c, ok := conn.(*connection)
	if !ok {
		return nil, fmt.Errorf("expected a cassandra connection")
	}
	return c, nil
}

type staticRows struct {
	cols []string
	rows []database.Row
	pos  int
}

func newStaticRows(cols []string, values [][]any) *staticRows {
	rows := make([]database.Row, len(values))
	for i, v := range values {
		rows[i] = database.NewRow(cols, v)
	}
	return &staticRows{cols: cols, rows: rows}
}

func (r *staticRows) Columns() ([]string, error) { return r.cols, nil }
func (r *staticRows) Next() bool {
	if r.pos >= len(r.rows) {
		return false
	}
	r.pos++
	return true
}
func (r *staticRows) Row() (database.Row, error) {
	if r.pos == 0 || r.pos > len(r.rows) {
		return database.Row{}, fmt.Errorf("no current row")
	}
	return r.rows[r.pos-1], nil
}
func (r *staticRows) Err() error          { return nil }
func (r *staticRows) NextResultSet() bool { return false }
func (r *staticRows) Close() error        { return nil }
