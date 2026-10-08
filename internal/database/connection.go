package database

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// Connection is the live session shared by commands, metadata and transactions.
type Connection interface {
	Query(context.Context, string, ...any) (Rows, error)
	Exec(context.Context, string, ...any) error
	Close() error
}

type Rows interface {
	Columns() ([]string, error)
	Next() bool
	Row() (Row, error)
	Err() error
	NextResultSet() bool
	Close() error
}

// Row supports tabular values and complete JSON documents without flattening BSON.
// Object is the JSON value written for the row.
type Row struct {
	Values []any
	Object any
}

// object keeps SQL column order; repeated names get _2, _3 suffixes so no value is lost.
type object struct {
	keys   []string
	values []any
}

func newObject(columns []string, values []any) object {
	used := make(map[string]bool, len(columns))
	for _, column := range columns {
		used[column] = true
	}
	seen := make(map[string]bool, len(columns))
	keys := make([]string, len(columns))
	for i, column := range columns {
		key := column
		for n := 2; seen[key] || (key != column && used[key]); n++ {
			key = column + "_" + strconv.Itoa(n)
		}
		seen[key] = true
		keys[i] = key
	}
	return object{keys: keys, values: values}
}

func (o object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, key := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		v, err := json.Marshal(o.values[i])
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

type sqlConnection struct {
	db   *sql.DB
	conn *sql.Conn
}

func OpenSQL(ctx context.Context, driver, dsn string) (Connection, error) {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("invalid connection configuration for %s", driver)
	}
	return ConnectSQL(ctx, db)
}
func ConnectSQL(ctx context.Context, db *sql.DB) (Connection, error) {
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err == nil {
		err = conn.PingContext(ctx)
	}
	if err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		_ = db.Close()
		return nil, err
	}
	return &sqlConnection{db: db, conn: conn}, nil
}
func (c *sqlConnection) Query(ctx context.Context, query string, args ...any) (Rows, error) {
	rows, err := c.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return &sqlRows{Rows: rows}, nil
}
func (c *sqlConnection) Exec(ctx context.Context, query string, args ...any) error {
	_, err := c.conn.ExecContext(ctx, query, args...)
	return err
}
func (c *sqlConnection) Close() error { return errors.Join(c.conn.Close(), c.db.Close()) }

type sqlRows struct{ *sql.Rows }

func (r *sqlRows) Row() (Row, error) {
	columns, err := r.Columns()
	if err != nil {
		return Row{}, err
	}
	row := Row{Values: make([]any, len(columns))}
	dest := make([]any, len(columns))
	for i := range dest {
		dest[i] = &row.Values[i]
	}
	if err := r.Scan(dest...); err != nil {
		return Row{}, err
	}
	for i, value := range row.Values {
		if raw, ok := value.([]byte); ok {
			value = string(raw)
			row.Values[i] = value
		}
	}
	row.Object = newObject(columns, row.Values)
	return row, nil
}
