package dbh

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/benenen/dbh/internal/database"
	"github.com/spf13/cobra"
)

type metadataSpec struct {
	verb    string
	aliases []string
	short   string
	argc    int
	use     string
	hasDB   bool
}

func addMetadataCommands(root *cobra.Command, store func() (Store, error), names cobra.CompletionFunc) {
	specs := []metadataSpec{
		{verb: "tables", aliases: []string{"tbl"}, short: "List tables or collections in the current database", argc: 1, use: "tables NAME", hasDB: true},
		{verb: "describe", aliases: []string{"desc"}, short: "Show column details and indexes for a table", argc: 2, use: "describe NAME TABLE", hasDB: true},
		{verb: "indexes", aliases: []string{"idx"}, short: "List indexes for a table", argc: 2, use: "indexes NAME TABLE", hasDB: true},
		{verb: "databases", aliases: []string{"dbs"}, short: "List databases on the server", argc: 1, use: "databases NAME", hasDB: false},
	}
	for _, spec := range specs {
		spec := spec
		var format, db string
		var timeout time.Duration
		c := &cobra.Command{
			Use:               spec.use,
			Aliases:           spec.aliases,
			Short:             spec.short,
			Args:              cobra.ExactArgs(spec.argc),
			ValidArgsFunction: names,
		}
		c.Flags().StringVar(&format, "format", "table", "table, csv, json or tsv")
		_ = c.RegisterFlagCompletionFunc("format", cobra.FixedCompletions([]string{"table", "csv", "json", "tsv"}, cobra.ShellCompDirectiveNoFileComp))
		if spec.hasDB {
			c.Flags().StringVar(&db, "db", "", "Server database to use")
			_ = c.RegisterFlagCompletionFunc("db", cobra.NoFileCompletions)
		}
		c.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "Connection/query timeout")
		_ = c.RegisterFlagCompletionFunc("timeout", cobra.NoFileCompletions)
		c.RunE = func(cmd *cobra.Command, args []string) error {
			if !validFormat(format) {
				return fmt.Errorf("format must be table, csv, json or tsv")
			}
			if timeout <= 0 {
				return fmt.Errorf("timeout must be positive")
			}
			if spec.hasDB && cmd.Flags().Changed("db") && strings.TrimSpace(db) == "" {
				return fmt.Errorf("database name must not be empty")
			}
			st, err := store()
			if err != nil {
				return err
			}
			p, err := st.Get(args[0])
			if err != nil {
				return err
			}
			stopSpinner := startSpinner(cmd.ErrOrStderr(), "Reading "+spec.verb)
			s, err := openSession(cmd.Context(), p, timeout)
			if err != nil {
				stopSpinner()
				return err
			}
			defer s.Close()
			if spec.hasDB && cmd.Flags().Changed("db") {
				if err := s.UseDatabase(cmd.Context(), db); err != nil {
					stopSpinner()
					return err
				}
			}
			s.BeforeOutput = stopSpinner
			defer func() {
				stopSpinner()
				s.BeforeOutput = nil
			}()
			switch spec.verb {
			case "describe":
				return s.Describe(cmd.Context(), args[1], format, cmd.OutOrStdout())
			case "indexes":
				return s.Indexes(cmd.Context(), args[1], format, cmd.OutOrStdout())
			}
			var values []string
			if spec.verb == "tables" {
				values, err = s.Tables(cmd.Context())
			} else {
				values, err = s.Databases(cmd.Context())
			}
			stopSpinner()
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
		encoder.SetEscapeHTML(false)
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
