package dbh

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/benenen/dbh/internal/database"
	"github.com/spf13/cobra"
)

func addMetadataCommands(root *cobra.Command, store func() (Store, error), names cobra.CompletionFunc) {
	for _, verb := range []string{"tables", "describe", "indexes", "databases"} {
		var format, db string
		var timeout time.Duration
		argc := 1
		use := verb + " NAME"
		if verb == "describe" || verb == "indexes" {
			argc = 2
			use += " TABLE"
		}
		c := &cobra.Command{Use: use, Short: "Read database metadata", Args: cobra.ExactArgs(argc), ValidArgsFunction: names}
		c.Flags().StringVar(&format, "format", "table", "table, csv, json or tsv")
		c.Flags().StringVar(&db, "db", "", "Server database to use")
		c.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "Connection/query timeout")
		c.RunE = func(cmd *cobra.Command, args []string) error {
			if !validFormat(format) {
				return fmt.Errorf("invalid output format")
			}
			if timeout <= 0 {
				return fmt.Errorf("timeout must be positive")
			}
			st, err := store()
			if err != nil {
				return err
			}
			p, err := st.Get(args[0])
			if err != nil {
				return err
			}
			s, err := openSession(cmd.Context(), p, timeout)
			if err != nil {
				return err
			}
			defer s.Close()
			if cmd.Flags().Changed("db") {
				if db == "" {
					return fmt.Errorf("database name must not be empty")
				}
				if err := s.UseDatabase(cmd.Context(), db); err != nil {
					return err
				}
			}
			switch verb {
			case "describe":
				return s.Describe(cmd.Context(), args[1], format, cmd.OutOrStdout())
			case "indexes":
				return s.Indexes(cmd.Context(), args[1], format, cmd.OutOrStdout())
			}
			var values []string
			if verb == "tables" {
				values, err = s.Tables(cmd.Context())
			} else {
				values, err = s.Databases(cmd.Context())
			}
			if err != nil {
				return err
			}
			return writeNames(cmd, values, format)
		}
		root.AddCommand(c)
	}
}

func writeNames(cmd *cobra.Command, names []string, format string) error {
	values := make([][]any, len(names))
	for i, name := range names {
		values[i] = []any{name}
	}
	return writeRows(cmd.OutOrStdout(), []string{"name"}, values, format)
}

// writeRows encodes metadata rows in the shared output formats without a
// database session.
func writeRows(out io.Writer, columns []string, values [][]any, format string) error {
	text := make([][]string, len(values))
	for i, row := range values {
		text[i] = make([]string, len(row))
		for j, value := range row {
			text[i][j] = fmt.Sprint(value)
		}
	}
	switch format {
	case "table":
		return renderTable(out, columns, text, tableWidth(out))
	case "json":
		encoder := json.NewEncoder(out)
		for _, row := range values {
			if err := encoder.Encode(database.NewRow(columns, row).Object); err != nil {
				return err
			}
		}
	case "csv":
		w := csv.NewWriter(out)
		if err := w.Write(columns); err != nil {
			return err
		}
		if err := w.WriteAll(text); err != nil {
			return err
		}
		return w.Error()
	case "tsv":
		if _, err := fmt.Fprintln(out, tsvLine(columns)); err != nil {
			return err
		}
		for _, row := range text {
			if _, err := fmt.Fprintln(out, tsvLine(row)); err != nil {
				return err
			}
		}
	}
	return nil
}
