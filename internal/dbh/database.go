package dbh

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/benenen/dbh/internal/database"
	"github.com/benenen/dbh/internal/database/registry"
)

type Session struct {
	Conn    database.Connection
	Driver  string
	Timeout time.Duration
	backend database.Driver
	dsn     string
}

func openSession(ctx context.Context, p Profile, timeout time.Duration) (*Session, error) {
	backend, err := registry.Lookup(p.Driver)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := backend.Open(pingCtx, p.DSN)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to %q: %s", p.Name, redact(err.Error(), p.DSN))
	}
	return &Session{Conn: conn, Driver: p.Driver, Timeout: timeout, backend: backend, dsn: p.DSN}, nil
}

func redact(message, dsn string) string {
	if dsn != "" {
		message = strings.ReplaceAll(message, dsn, "[redacted DSN]")
	}
	return message
}

func (s *Session) Close() { _ = s.Conn.Close() }

func (s *Session) Execute(ctx context.Context, query, format string, out io.Writer) error {
	name, use, err := parseUseDatabase(query, s.backend.Syntax())
	if use {
		if err != nil {
			return err
		}
		if err := s.UseDatabase(ctx, name); err != nil {
			return err
		}
		if format == "table" {
			_, err = fmt.Fprintln(out, "OK")
		}
		return err
	}
	return s.executeArgs(ctx, query, format, out)
}

func (s *Session) UseDatabase(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	conn, err := s.backend.SwitchDatabase(ctx, s.Conn, s.dsn, name)
	if err == nil && conn != nil {
		s.Close()
		s.Conn = conn
	}

	if err != nil {
		return fmt.Errorf("cannot switch database to %q: %s", name, redact(err.Error(), s.dsn))
	}
	return nil
}

func (s *Session) executeArgs(ctx context.Context, query, format string, out io.Writer, args ...any) error {
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	// Query works for both row-returning SQL and commands; consuming Next also
	// ensures SQLite steps commands which do not return columns.
	rows, err := s.Conn.Query(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for {
		cols, err := rows.Columns()
		if err != nil {
			return err
		}
		var tableRows [][]string
		var csvOut *csv.Writer
		var jsonOut *json.Encoder
		switch format {
		case "csv":
			csvOut = csv.NewWriter(out)
			if len(cols) > 0 {
				if err := csvOut.Write(cols); err != nil {
					return err
				}
			}
		case "json":
			jsonOut = json.NewEncoder(out)
		}
		count := 0
		for rows.Next() {
			row, err := rows.Row()
			if err != nil {
				return err
			}
			fields := make([]string, len(cols))
			object := row.Object
			for i, v := range row.Values {
				if v == nil {
					fields[i] = "NULL"
				} else {
					fields[i] = fmt.Sprint(v)
				}
			}
			switch format {
			case "table":
				tableRows = append(tableRows, fields)
			case "csv":
				if err := csvOut.Write(fields); err != nil {
					return err
				}
			case "json":
				if err := jsonOut.Encode(object); err != nil {
					return err
				}
			}
			count++
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if format == "table" {
			if len(cols) > 0 {
				if err := renderTable(out, cols, tableRows, tableWidth(out)); err != nil {
					return err
				}
				if _, err := fmt.Fprintf(out, "(%d rows)\n", count); err != nil {
					return err
				}
			} else {
				if _, err := fmt.Fprintln(out, "OK"); err != nil {
					return err
				}
			}
		}

		if csvOut != nil {
			csvOut.Flush()
			if err := csvOut.Error(); err != nil {
				return err
			}
		}
		if !rows.NextResultSet() {
			return rows.Err()
		}
	}
}

func (s *Session) Run(ctx context.Context, input, format string, out io.Writer) error {
	statements, rest, err := splitSQL(input, s.Driver)
	if err != nil {
		return err
	}
	if stripComments(rest, s.Driver) != "" {
		statements = append(statements, rest)
	}
	for _, q := range statements {
		if err := s.Execute(ctx, q, format, out); err != nil {
			return err
		}
	}
	return nil
}

// Metadata is read through the same connection as SQL and transactions.
func (s *Session) Databases(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	return s.backend.Databases(ctx, s.Conn)
}

func (s *Session) Tables(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	return s.backend.Tables(ctx, s.Conn)
}

func (s *Session) Columns(ctx context.Context, table string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	return s.backend.Columns(ctx, s.Conn, table)
}
