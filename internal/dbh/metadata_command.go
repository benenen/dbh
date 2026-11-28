package dbh

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/benenen/dbh/internal/database"
	"github.com/benenen/dbh/internal/database/registry"
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
		var format, db, adhocDriver, adhocDSN string
		var adhocProxies []string
		var timeout time.Duration
		c := &cobra.Command{
			Use:     spec.use,
			Aliases: spec.aliases,
			Short:   spec.short,
			Args: func(cmd *cobra.Command, args []string) error {
				expected := spec.argc
				if cmd.Flags().Changed("dsn") {
					expected = spec.argc - 1
				}
				if len(args) != expected {
					return fmt.Errorf("accepts %d arg(s), received %d", expected, len(args))
				}
				return nil
			},
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
		c.Flags().StringVar(&adhocDSN, "dsn", "", "Ad-hoc DSN or URL (does not save a connection profile)")
		c.Flags().StringVar(&adhocDriver, "driver", "", "Driver for ad-hoc target")
		c.Flags().StringArrayVar(&adhocProxies, "proxy", nil, "Proxy hop for ad-hoc target; repeat in dial order")
		_ = c.RegisterFlagCompletionFunc("driver", cobra.FixedCompletions(registry.Drivers(), cobra.ShellCompDirectiveNoFileComp))
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
			var targetArg, tableArg string
			if cmd.Flags().Changed("dsn") {
				if spec.argc == 2 {
					tableArg = args[0]
				}
			} else {
				targetArg = args[0]
				if spec.argc == 2 {
					tableArg = args[1]
				}
			}
			p, _, err := resolveTarget(st, targetArg, adhocOptions{
				Driver:  adhocDriver,
				DSN:     adhocDSN,
				Proxies: adhocProxies,
			})
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
				return s.Describe(cmd.Context(), tableArg, format, cmd.OutOrStdout())
			case "indexes":
				return s.Indexes(cmd.Context(), tableArg, format, cmd.OutOrStdout())
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

	addDDLCommand(root, store, names)
}

func addDDLCommand(root *cobra.Command, store func() (Store, error), names cobra.CompletionFunc) {
	var format, db, outputPath, adhocDriver, adhocDSN string
	var adhocProxies []string
	var timeout time.Duration
	c := &cobra.Command{
		Use:   "ddl NAME TABLE",
		Short: "Export CREATE DDL for a table or view",
		Args: func(cmd *cobra.Command, args []string) error {
			expected := 2
			if cmd.Flags().Changed("dsn") {
				expected = 1
			}
			if len(args) != expected {
				return fmt.Errorf("accepts %d arg(s), received %d", expected, len(args))
			}
			return nil
		},
		ValidArgsFunction: names,
	}
	c.Flags().StringVar(&format, "format", "raw", "raw, table, csv, json or tsv")
	c.Flags().StringVar(&db, "db", "", "Server database to use")
	c.Flags().StringVarP(&outputPath, "output", "o", "", "Write DDL output atomically to this file")
	c.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "Connection/query timeout")
	c.Flags().StringVar(&adhocDSN, "dsn", "", "Ad-hoc DSN or URL (does not save a connection profile)")
	c.Flags().StringVar(&adhocDriver, "driver", "", "Driver for ad-hoc target")
	c.Flags().StringArrayVar(&adhocProxies, "proxy", nil, "Proxy hop for ad-hoc target; repeat in dial order")
	_ = c.RegisterFlagCompletionFunc("format", cobra.FixedCompletions([]string{"raw", "table", "csv", "json", "tsv"}, cobra.ShellCompDirectiveNoFileComp))
	_ = c.RegisterFlagCompletionFunc("driver", cobra.FixedCompletions(registry.Drivers(), cobra.ShellCompDirectiveNoFileComp))
	_ = c.RegisterFlagCompletionFunc("db", cobra.NoFileCompletions)
	_ = c.RegisterFlagCompletionFunc("timeout", cobra.NoFileCompletions)
	c.RunE = func(cmd *cobra.Command, args []string) error {
		if format != "raw" && !validFormat(format) {
			return fmt.Errorf("format must be raw, table, csv, json or tsv")
		}
		if timeout <= 0 {
			return fmt.Errorf("timeout must be positive")
		}
		if cmd.Flags().Changed("db") && strings.TrimSpace(db) == "" {
			return fmt.Errorf("database name must not be empty")
		}
		st, err := store()
		if err != nil {
			return err
		}
		var targetArg, tableArg string
		if cmd.Flags().Changed("dsn") {
			tableArg = args[0]
		} else {
			targetArg = args[0]
			tableArg = args[1]
		}
		p, _, err := resolveTarget(st, targetArg, adhocOptions{
			Driver:  adhocDriver,
			DSN:     adhocDSN,
			Proxies: adhocProxies,
		})
		if err != nil {
			return err
		}
		stopSpinner := startSpinner(cmd.ErrOrStderr(), "Reading ddl")
		s, err := openSession(cmd.Context(), p, timeout)
		if err != nil {
			stopSpinner()
			return err
		}
		defer s.Close()
		if cmd.Flags().Changed("db") {
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

		outWriter := cmd.OutOrStdout()
		var outBuf bytes.Buffer
		if outputPath != "" {
			outWriter = &outBuf
		}
		if err := s.DDL(cmd.Context(), tableArg, format, outWriter); err != nil {
			return err
		}
		if outputPath != "" {
			return writeAtomicFile(outputPath, outBuf.Bytes())
		}
		return nil
	}
	root.AddCommand(c)
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
