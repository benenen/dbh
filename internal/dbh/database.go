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
	"github.com/benenen/dbh/internal/database/proxy"
	"github.com/benenen/dbh/internal/database/registry"
)

type Session struct {
	Conn            database.Connection
	Driver          string
	Timeout         time.Duration
	MaxRows         int
	BatchSize       int
	Notice          io.Writer
	Stats           bool
	CurrentDatabase string
	backend         database.Driver
	dsn             string
	proxy           *proxy.Chain
}

func openSession(ctx context.Context, p Profile, timeout time.Duration) (*Session, error) {
	backend, err := registry.Lookup(p.Driver)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	s := &Session{Driver: p.Driver, Timeout: timeout, backend: backend, dsn: p.DSN}
	if len(p.Proxies) > 0 {
		if s.proxy, err = proxy.Open(pingCtx, p.Proxies); err != nil {
			return nil, fmt.Errorf("cannot connect to %q: %s", p.Name, redact(err.Error(), "", p.Proxies))
		}
	}
	s.Conn, err = backend.Open(pingCtx, p.DSN, s.dial())
	if err != nil {
		s.closeProxy()
		return nil, fmt.Errorf("cannot connect to %q: %s", p.Name, redact(err.Error(), p.DSN, p.Proxies))
	}
	if current, ok := backend.(database.CurrentDatabase); ok {
		s.CurrentDatabase, err = current.CurrentDatabase(pingCtx, s.Conn)
		if err != nil {
			s.Close()
			return nil, fmt.Errorf("cannot read current database: %s", redact(err.Error(), p.DSN, p.Proxies))
		}
	}
	return s, nil
}

// redact hides the DSN and proxy credentials in error messages.
func redact(message, dsn string, proxies []string) string {
	if dsn != "" {
		message = strings.ReplaceAll(message, dsn, "[redacted DSN]")
	}
	for _, hop := range proxies {
		message = strings.ReplaceAll(message, hop, proxy.Redact(hop))
	}
	return message
}

func (s *Session) dial() database.Dial {
	if s.proxy == nil {
		return nil
	}
	return s.proxy.Dial()
}

func (s *Session) closeProxy() {
	if s.proxy != nil {
		_ = s.proxy.Close()
	}
}

func (s *Session) Close() {
	_ = s.Conn.Close()
	s.closeProxy()
}

func (s *Session) Execute(ctx context.Context, query, format string, out io.Writer) error {
	name, use, err := parseUseDatabase(query, s.backend.Syntax())
	if use {
		started := time.Now()
		if err != nil {
			return err
		}
		if err := s.UseDatabase(ctx, name); err != nil {
			return err
		}
		if format == "table" {
			_, err = fmt.Fprintln(out, "OK")
		}
		if err == nil && s.Stats {
			w := s.Notice
			if w == nil {
				w = out
			}
			_, err = fmt.Fprintf(w, "Time: %s\n", time.Since(started).Round(time.Microsecond))
		}
		return err
	}
	err = s.executeArgs(ctx, query, format, out)
	if err == nil && s.backend.Syntax().LineCommands {
		if current, ok := s.backend.(database.CurrentDatabase); ok {
			s.CurrentDatabase, err = current.CurrentDatabase(ctx, s.Conn)
		}
	}
	return err
}

func (s *Session) UseDatabase(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	conn, err := s.backend.SwitchDatabase(ctx, s.Conn, s.dsn, name, s.dial())
	if err == nil && conn != nil {
		_ = s.Conn.Close()
		s.Conn = conn
	}

	if err != nil {
		return fmt.Errorf("cannot switch database to %q: %s", name, redact(err.Error(), s.dsn, nil))
	}
	s.CurrentDatabase = name
	return nil
}

func (s *Session) executeArgs(ctx context.Context, query, format string, out io.Writer, args ...any) (err error) {
	started := time.Now()
	total := 0
	hasColumns := false
	var affected int64
	affectedKnown := false
	defer func() {
		if err != nil || !s.Stats {
			return
		}
		w := s.Notice
		if w == nil {
			w = out
		}
		detail := ""
		if affectedKnown {
			detail = fmt.Sprintf("; %d rows affected", affected)
		} else if hasColumns {
			detail = fmt.Sprintf("; %d rows returned", total)
		}
		_, err = fmt.Fprintf(w, "Time: %s%s\n", time.Since(started).Round(time.Microsecond), detail)
	}()
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	if s.Stats && affectedStatement(query, s.backend.Syntax()) {
		if executor, ok := s.Conn.(database.ResultExecutor); ok {
			affected, affectedKnown, err = executor.ExecResult(ctx, query, args...)
			if err != nil {
				return err
			}
			if format == "table" {
				_, err = fmt.Fprintln(out, "OK")
			}
			return err
		}
	}
	// Query works for both row-returning SQL and commands; consuming Next also
	// ensures SQLite steps commands which do not return columns.
	rows, err := s.Conn.Query(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() {
		closeErr := rows.Close()
		if err == nil {
			err = closeErr
		}
	}()
	var csvOut *csv.Writer
	// Keep rows already read when a later row fails, matching JSON's streaming output.
	defer func() {
		if csvOut != nil {
			csvOut.Flush()
		}
	}()
	for {
		cols, err := rows.Columns()
		if err != nil {
			return err
		}
		hasColumns = hasColumns || len(cols) > 0
		var tableRows [][]string
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
		case "tsv":
			if len(cols) > 0 {
				if _, err := fmt.Fprintln(out, tsvLine(cols)); err != nil {
					return err
				}
			}
		}
		count := 0
		truncated := false
		batchSize := s.BatchSize
		if batchSize <= 0 {
			batchSize = 500
		}
		for rows.Next() {
			if s.MaxRows > 0 && count >= s.MaxRows {
				truncated = true
				break
			}
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
				if len(tableRows) == batchSize {
					if err := renderTable(out, cols, tableRows, tableWidth(out)); err != nil {
						return err
					}
					tableRows = nil
				}
			case "csv":
				if err := csvOut.Write(fields); err != nil {
					return err
				}
			case "json":
				if err := jsonOut.Encode(object); err != nil {
					return err
				}
			case "tsv":
				for i, v := range row.Values {
					if v == nil {
						fields[i] = `\N`
					} else {
						fields[i] = tsvEscaper.Replace(fields[i])
					}
				}
				if _, err := fmt.Fprintln(out, strings.Join(fields, "\t")); err != nil {
					return err
				}
			}
			count++
			total++
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if format == "table" {
			if len(cols) > 0 {
				if len(tableRows) > 0 || count == 0 {
					if err := renderTable(out, cols, tableRows, tableWidth(out)); err != nil {
						return err
					}
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
		if truncated {
			w := s.Notice
			if w == nil {
				w = out
			}
			_, err := fmt.Fprintf(w, "Result truncated after %d rows (--max-rows).\n", count)
			return err
		}
		if !rows.NextResultSet() {
			return rows.Err()
		}
	}
}

func validFormat(format string) bool {
	return format == "table" || format == "csv" || format == "json" || format == "tsv"
}

// tsvEscaper keeps one record per line; NULL is written as \N so it differs
// from an empty string.
var tsvEscaper = strings.NewReplacer(`\`, `\\`, "\t", `\t`, "\n", `\n`, "\r", `\r`)

func tsvLine(values []string) string {
	escaped := make([]string, len(values))
	for i, value := range values {
		escaped[i] = tsvEscaper.Replace(value)
	}
	return strings.Join(escaped, "\t")
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

// ColumnNames lists every column of the current database in one query; ok is
// false when the driver can only read columns per table.
func (s *Session) ColumnNames(ctx context.Context) (names []string, ok bool, err error) {
	backend, ok := s.backend.(database.ColumnNames)
	if !ok {
		return nil, false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	names, err = backend.ColumnNames(ctx, s.Conn)
	return names, true, err
}

func (s *Session) Columns(ctx context.Context, table string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	return s.backend.Columns(ctx, s.Conn, table)
}
