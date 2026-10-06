package dbh

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type Session struct {
	DB      *sql.DB
	Conn    *sql.Conn
	Driver  string
	Timeout time.Duration
}

func openSession(ctx context.Context, p Profile, timeout time.Duration) (*Session, error) {
	driver := p.Driver
	if driver == "postgres" {
		driver = "pgx"
	}
	db, err := sql.Open(driver, p.DSN)
	if err != nil {
		return nil, fmt.Errorf("invalid connection configuration for %s", p.Driver)
	}
	db.SetMaxOpenConns(1)
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := db.Conn(pingCtx)
	if err == nil {
		err = conn.PingContext(pingCtx)
	}
	if err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		_ = db.Close()
		// Driver errors can include passwords embedded in a DSN.
		return nil, fmt.Errorf("cannot connect to %q: %s", p.Name, redact(err.Error(), p.DSN))
	}
	return &Session{DB: db, Conn: conn, Driver: p.Driver, Timeout: timeout}, nil
}

func redact(message, dsn string) string {
	if dsn != "" {
		message = strings.ReplaceAll(message, dsn, "[redacted DSN]")
	}
	return message
}

func (s *Session) Close() { _ = s.Conn.Close(); _ = s.DB.Close() }

func (s *Session) Execute(ctx context.Context, query, format string, out io.Writer) error {
	return s.executeArgs(ctx, query, format, out)
}

func (s *Session) executeArgs(ctx context.Context, query, format string, out io.Writer, args ...any) error {
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	// Query works for both row-returning SQL and commands; consuming Next also
	// ensures SQLite steps commands which do not return columns.
	rows, err := s.Conn.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for {
		cols, err := rows.Columns()
		if err != nil {
			return err
		}
		values := make([]any, len(cols))
		dest := make([]any, len(cols))
		for i := range values {
			dest[i] = &values[i]
		}
		var table *tabwriter.Writer
		var csvOut *csv.Writer
		var jsonOut *json.Encoder
		switch format {
		case "table":
			table = tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			if len(cols) > 0 {
				_, _ = fmt.Fprintln(table, strings.Join(cols, "\t"))
			}
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
			if err := rows.Scan(dest...); err != nil {
				return err
			}
			fields := make([]string, len(cols))
			object := make(map[string]any, len(cols))
			for i, v := range values {
				if b, ok := v.([]byte); ok {
					v = string(b)
				}
				if v == nil {
					fields[i] = "NULL"
				} else {
					fields[i] = fmt.Sprint(v)
				}
				object[cols[i]] = v
			}
			switch format {
			case "table":
				for i := range fields {
					fields[i] = strings.NewReplacer("\t", "\\t", "\n", "\\n", "\r", "\\r").Replace(fields[i])
				}
				_, _ = fmt.Fprintln(table, strings.Join(fields, "\t"))
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
		if table != nil {
			if err := table.Flush(); err != nil {
				return err
			}
			if len(cols) > 0 {
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

// Metadata is read from the same session, preserving temporary tables and the search path.
func (s *Session) Tables(ctx context.Context) ([]string, error) {
	query := "SELECT name FROM sqlite_master WHERE type IN ('table','view') UNION SELECT name FROM sqlite_temp_master WHERE type IN ('table','view') ORDER BY name"
	if s.Driver == "postgres" {
		query = `SELECT table_schema || '.' || table_name FROM information_schema.tables WHERE table_schema NOT IN ('pg_catalog','information_schema') ORDER BY 1`
	}
	if s.Driver == "mysql" {
		query = "SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() ORDER BY table_name"
	}
	return s.names(ctx, query)
}

func (s *Session) Columns(ctx context.Context, table string) ([]string, error) {
	if s.Driver == "sqlite" {
		return s.names(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	}
	parts := strings.SplitN(table, ".", 2)
	schema, name := "", table
	if len(parts) == 2 {
		schema, name = parts[0], parts[1]
	}
	if s.Driver == "postgres" {
		if schema == "" {
			return s.names(ctx, `SELECT column_name FROM information_schema.columns WHERE table_name=$1 AND table_schema = ANY(current_schemas(false)) ORDER BY ordinal_position`, name)
		}
		return s.names(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema=$1 AND table_name=$2 ORDER BY ordinal_position`, schema, name)
	}
	if schema == "" {
		return s.names(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? ORDER BY ordinal_position`, name)
	}
	return s.names(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema=? AND table_name=? ORDER BY ordinal_position`, schema, name)
}

func (s *Session) names(ctx context.Context, query string, args ...any) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	rows, err := s.Conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
