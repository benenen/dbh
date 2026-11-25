package dbh

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func addExplainCommand(root *cobra.Command, store func() (Store, error), names cobra.CompletionFunc) {
	var query, file, db, format string
	var analyze, allowWrite bool
	var timeout time.Duration

	c := &cobra.Command{
		Use:               "explain NAME [QUERY]",
		Short:             "Show the query execution plan using the database's EXPLAIN syntax",
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: names,
	}
	c.Flags().StringVarP(&query, "sql", "e", "", "SQL statement to explain")
	c.Flags().StringVarP(&file, "file", "f", "", "Read SQL statement from file (- for stdin)")
	c.Flags().StringVar(&db, "db", "", "Server database to use")
	c.Flags().BoolVar(&analyze, "analyze", false, "Run EXPLAIN ANALYZE where supported (executes the query)")
	c.Flags().StringVar(&format, "format", "table", "table, csv, json or tsv")
	c.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "Query timeout")
	c.Flags().BoolVar(&allowWrite, "allow-write", false, "Allow EXPLAIN ANALYZE on write statements")
	c.MarkFlagsMutuallyExclusive("sql", "file")
	_ = c.RegisterFlagCompletionFunc("format", cobra.FixedCompletions([]string{"table", "csv", "json", "tsv"}, cobra.ShellCompDirectiveNoFileComp))

	c.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) == 2 {
			if cmd.Flags().Changed("sql") || cmd.Flags().Changed("file") {
				return fmt.Errorf("positional query cannot be combined with --sql or --file")
			}
			query = args[1]
		} else if cmd.Flags().Changed("file") {
			var b []byte
			var err error
			if file == "-" {
				b, err = io.ReadAll(cmd.InOrStdin())
			} else {
				b, err = os.ReadFile(file)
			}
			if err != nil {
				return err
			}
			query = string(b)
		}
		query = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(query), ";"))
		if query == "" {
			return fmt.Errorf("provide a SQL statement via argument, --sql or --file")
		}
		if !validFormat(format) {
			return fmt.Errorf("format must be table, csv, json or tsv")
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

		session, err := openSession(cmd.Context(), p, timeout)
		if err != nil {
			return err
		}
		defer session.Close()
		session.AllowWrite = allowWrite
		session.Notice = cmd.ErrOrStderr()
		if cmd.Flags().Changed("db") {
			if err := session.UseDatabase(cmd.Context(), db); err != nil {
				return err
			}
		}

		if analyze && p.ReadOnly && !allowWrite && isWriteStatement(query, session.backend.Syntax()) {
			return fmt.Errorf("EXPLAIN ANALYZE on a write statement is blocked on read-only connections (use --allow-write to override)")
		}

		explainQueries, err := buildExplainQueries(p.Driver, query, analyze)
		if err != nil {
			return err
		}
		for _, q := range explainQueries {
			if err := session.Execute(cmd.Context(), q, format, cmd.OutOrStdout()); err != nil {
				return err
			}
		}
		return nil
	}
	root.AddCommand(c)
}

func buildExplainQueries(driver, query string, analyze bool) ([]string, error) {
	switch driver {
	case "sqlite":
		return []string{"EXPLAIN QUERY PLAN " + query}, nil
	case "postgres":
		if analyze {
			return []string{"EXPLAIN (ANALYZE, BUFFERS) " + query}, nil
		}
		return []string{"EXPLAIN " + query}, nil
	case "mysql":
		if analyze {
			return []string{"EXPLAIN ANALYZE " + query}, nil
		}
		return []string{"EXPLAIN FORMAT=TREE " + query}, nil
	case "trino":
		if analyze {
			return []string{"EXPLAIN ANALYZE " + query}, nil
		}
		return []string{"EXPLAIN " + query}, nil
	case "clickhouse", "dm":
		return []string{"EXPLAIN " + query}, nil
	case "mssql":
		return []string{"SET SHOWPLAN_TEXT ON", query, "SET SHOWPLAN_TEXT OFF"}, nil
	case "oracle":
		return []string{"EXPLAIN PLAN FOR " + query, "SELECT PLAN_TABLE_OUTPUT FROM TABLE(DBMS_XPLAN.DISPLAY())"}, nil
	default:
		return nil, fmt.Errorf("explain is not supported for %s", driver)
	}
}
