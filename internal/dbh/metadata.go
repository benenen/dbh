package dbh

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/benenen/dbh/internal/database"
)

// Describe prints column details and indexes using the current session.
func (s *Session) Describe(ctx context.Context, table, format string, out io.Writer) error {
	metadataCtx, cancel := context.WithTimeout(ctx, s.Timeout)
	query, err := s.backend.ColumnDetails(metadataCtx, s.Conn, table)
	cancel()
	if err != nil {
		return err
	}
	if format == "table" {
		heading := query.Heading
		if heading == "" {
			heading = "Columns:"
		}
		if _, err := fmt.Fprintln(out, heading); err != nil {
			return err
		}
	}
	if err := s.executeArgs(ctx, query.Text, format, out, query.Args...); err != nil {
		return err
	}
	if err := s.Indexes(ctx, table, format, out); !errors.Is(err, database.ErrNoIndexes) {
		return err
	}
	return nil
}

func (s *Session) Indexes(ctx context.Context, table, format string, out io.Writer) error {
	metadataCtx, cancel := context.WithTimeout(ctx, s.Timeout)
	query, err := s.backend.Indexes(metadataCtx, s.Conn, table)
	cancel()
	if err != nil {
		return err
	}
	if format == "table" {
		if _, err := fmt.Fprintln(out, "Indexes:"); err != nil {
			return err
		}
	}
	return s.executeArgs(ctx, query.Text, format, out, query.Args...)
}
