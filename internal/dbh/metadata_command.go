package dbh

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
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
	// Reuse the result encoder without opening another database session.
	rows := make([][]string, len(names))
	for i, name := range names {
		rows[i] = []string{name}
	}
	switch format {
	case "table":
		return renderTable(cmd.OutOrStdout(), []string{"name"}, rows, tableWidth(cmd.OutOrStdout()))
	case "json":
		for _, name := range names {
			if err := jsonName(cmd, database.NewRow([]string{"name"}, []any{name})); err != nil {
				return err
			}
		}
	case "csv":
		w := csv.NewWriter(cmd.OutOrStdout())
		if err := w.Write([]string{"name"}); err != nil {
			return err
		}
		for _, row := range rows {
			if err := w.Write(row); err != nil {
				return err
			}
		}
		w.Flush()
		return w.Error()
	case "tsv":
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), "name"); err != nil {
			return err
		}
		for _, name := range names {
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), tsvLine([]string{name})); err != nil {
				return err
			}
		}
	}
	return nil
}

func jsonName(cmd *cobra.Command, row database.Row) error {
	return json.NewEncoder(cmd.OutOrStdout()).Encode(row.Object)
}
