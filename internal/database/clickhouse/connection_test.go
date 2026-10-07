package clickhouse

import (
	"context"
	"errors"
	"github.com/benenen/dbh/internal/database"
	"testing"
)

type recordingConnection struct {
	database.Connection
	queries, execs int
	err            error
}

func (c *recordingConnection) Query(context.Context, string, ...any) (database.Rows, error) {
	c.queries++
	return emptyRows{}, c.err
}
func (c *recordingConnection) Exec(context.Context, string, ...any) error { c.execs++; return c.err }
func TestQueryDispatch(t *testing.T) {
	for _, test := range []struct {
		query string
		write bool
	}{
		{"/* header */ -- line\n # line\n INSERT INTO t VALUES (1)", true},
		{"CREATE TABLE t (id UInt64) ENGINE=Memory", true},
		{"/* outer /* inner */ outer */ CREATE TABLE t (id UInt64) ENGINE=Memory", true},
		{"/* outer /* inner /* deeper */ inner */ outer */ INSERT INTO t VALUES (1)", true},
		{"/* outer /* inner */ outer */ SELECT 1", false},
		{"SELECT 'INSERT INTO t'", false},
		{"WITH 1 AS x SELECT x", false},
		{"SHOW TABLES", false},
	} {
		t.Run(test.query, func(t *testing.T) {
			failure := errors.New("write failed")
			for _, err := range []error{nil, failure} {
				inner := &recordingConnection{err: err}
				conn := &connection{inner}
				rows, got := conn.Query(context.Background(), test.query)
				if !errors.Is(got, err) {
					t.Fatalf("error: %v", got)
				}
				if test.write && (inner.execs != 1 || inner.queries != 0) {
					t.Fatal("write must execute exactly once")
				}
				if !test.write && (inner.queries != 1 || inner.execs != 0) {
					t.Fatal("read must retain rows")
				}
				if err == nil {
					if rows.Next() {
						t.Fatal("unexpected row")
					}
					if err := rows.Close(); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}
