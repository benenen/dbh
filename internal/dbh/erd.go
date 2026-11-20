package dbh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/benenen/dbh/internal/database"
	"github.com/benenen/dbh/internal/erd"
	"github.com/spf13/cobra"
)

func addERDCommands(root *cobra.Command, store func() (Store, error), names cobra.CompletionFunc) {
	var model, dirFlag string
	group := &cobra.Command{Use: "erd", Aliases: []string{"er"}, Short: "Query the DBML relationship models in .dbh/", Args: cobra.NoArgs}
	group.PersistentFlags().StringVar(&model, "model", "", "Model name: .dbh/<model>.dbml and .dbh/<model>.refs.dbml (default: all models for queries, \"schema\" for pull)")
	group.PersistentFlags().StringVar(&dirFlag, "dir", "", "Limit to one .dbh directory (default: scan every .dbh directory in the project)")

	// open loads the model for queries; invalid inferred relations are skipped
	// with a warning.
	open := func(cmd *cobra.Command) (*erd.Model, error) {
		m, err := loadERD(dirFlag, model)
		if err != nil {
			return nil, err
		}
		if err := m.Usable(); err != nil {
			return nil, err
		}
		for _, issue := range m.Errors {
			if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "warning: skipped %s\n", issue); err != nil {
				return nil, err
			}
		}
		return m, nil
	}
	tableNames := func(cmd *cobra.Command, args []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
		m, err := loadERD(dirFlag, model)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var completions []cobra.Completion
		for _, t := range m.Tables {
			completions = append(completions, t.Display())
		}
		return completions, cobra.ShellCompDirectiveNoFileComp
	}
	formatFlag := func(c *cobra.Command, format *string) {
		c.Flags().StringVar(format, "format", "table", "table, csv, json or tsv")
		_ = c.RegisterFlagCompletionFunc("format", cobra.FixedCompletions([]string{"table", "csv", "json", "tsv"}, cobra.ShellCompDirectiveNoFileComp))
	}

	group.AddCommand(erdPullCommand(store, names, &model, &dirFlag))

	check := &cobra.Command{Use: "check", Short: "Validate the model and its inferred relations", Args: cobra.NoArgs, ValidArgsFunction: cobra.NoFileCompletions, RunE: func(cmd *cobra.Command, args []string) error {
		m, err := loadERD(dirFlag, model)
		if err != nil {
			return err
		}
		if err := writeIssues(cmd.ErrOrStderr(), m); err != nil {
			return err
		}
		if len(m.Errors) > 0 {
			return fmt.Errorf("ERD model %q has %d error(s)", m.Name, len(m.Errors))
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "OK: %d tables, %d declared and %d inferred relations\n", len(m.Tables), m.Count(erd.SourceDeclared), m.Count(erd.SourceInferred))
		return err
	}}
	group.AddCommand(check)

	var statusFormat string
	status := &cobra.Command{Use: "status", Short: "List the models in .dbh/", Args: cobra.NoArgs, ValidArgsFunction: cobra.NoFileCompletions, RunE: func(cmd *cobra.Command, args []string) error {
		if !validFormat(statusFormat) {
			return fmt.Errorf("invalid output format")
		}
		dirs, err := erdDirs(dirFlag)
		if err != nil {
			return err
		}
		rows, err := erd.StatusRows(dirs, model)
		if err != nil {
			return err
		}
		return writeERDRows(cmd.OutOrStdout(), rows, statusFormat)
	}}
	formatFlag(status, &statusFormat)
	group.AddCommand(status)

	var tablesFormat string
	tables := &cobra.Command{Use: "tables", Short: "List the tables in the model", Args: cobra.NoArgs, ValidArgsFunction: cobra.NoFileCompletions, RunE: func(cmd *cobra.Command, args []string) error {
		if !validFormat(tablesFormat) {
			return fmt.Errorf("invalid output format")
		}
		m, err := open(cmd)
		if err != nil {
			return err
		}
		return writeERDRows(cmd.OutOrStdout(), m.TableRows(), tablesFormat)
	}}
	formatFlag(tables, &tablesFormat)
	group.AddCommand(tables)

	var relationsFormat, relationsTable, relationsSource string
	relations := &cobra.Command{Use: "relations", Short: "List relations, optionally for one table or source", Args: cobra.NoArgs, ValidArgsFunction: cobra.NoFileCompletions, RunE: func(cmd *cobra.Command, args []string) error {
		if !validFormat(relationsFormat) {
			return fmt.Errorf("invalid output format")
		}
		if relationsSource != "" && relationsSource != erd.SourceDeclared && relationsSource != erd.SourceInferred {
			return fmt.Errorf("source must be declared or inferred")
		}
		m, err := open(cmd)
		if err != nil {
			return err
		}
		table := ""
		if relationsTable != "" {
			if table, err = m.Lookup(relationsTable); err != nil {
				return err
			}
		}
		return writeERDRows(cmd.OutOrStdout(), m.RelationRows(table, relationsSource), relationsFormat)
	}}
	formatFlag(relations, &relationsFormat)
	relations.Flags().StringVar(&relationsTable, "table", "", "Only relations that involve this table")
	relations.Flags().StringVar(&relationsSource, "source", "", "Only declared or inferred relations")
	_ = relations.RegisterFlagCompletionFunc("table", tableNames)
	_ = relations.RegisterFlagCompletionFunc("source", cobra.FixedCompletions([]string{erd.SourceDeclared, erd.SourceInferred}, cobra.ShellCompDirectiveNoFileComp))
	group.AddCommand(relations)

	var showFormat string
	var depth int
	show := &cobra.Command{Use: "show [TABLE...]", Short: "Preview tables and relations as text, Graphviz dot or Mermaid", ValidArgsFunction: tableNames, RunE: func(cmd *cobra.Command, args []string) error {
		if depth < 0 {
			return fmt.Errorf("depth must be nonnegative")
		}
		var render func(io.Writer, *erd.Model, map[string]bool) error
		switch showFormat {
		case "text":
			render = erd.RenderText
		case "dot":
			render = erd.RenderDot
		case "mermaid":
			render = erd.RenderMermaid
		default:
			return fmt.Errorf("show format must be text, dot or mermaid")
		}
		m, err := open(cmd)
		if err != nil {
			return err
		}
		var include map[string]bool
		if len(args) > 0 {
			start := make([]string, len(args))
			for i, arg := range args {
				if start[i], err = m.Lookup(arg); err != nil {
					return err
				}
			}
			include = m.Neighborhood(start, depth)
		}
		return render(cmd.OutOrStdout(), m, include)
	}}
	show.Flags().StringVar(&showFormat, "format", "text", "text, dot or mermaid")
	show.Flags().IntVar(&depth, "depth", 1, "Include tables up to this many relations away from TABLE")
	_ = show.RegisterFlagCompletionFunc("format", cobra.FixedCompletions([]string{"text", "dot", "mermaid"}, cobra.ShellCompDirectiveNoFileComp))
	group.AddCommand(show)

	var pathFormat string
	var declaredOnly bool
	path := &cobra.Command{Use: "path FROM TO", Short: "Show the shortest relation path between two tables", Args: cobra.ExactArgs(2), ValidArgsFunction: tableNames, RunE: func(cmd *cobra.Command, args []string) error {
		if !validFormat(pathFormat) {
			return fmt.Errorf("invalid output format")
		}
		m, err := open(cmd)
		if err != nil {
			return err
		}
		from, err := m.Lookup(args[0])
		if err != nil {
			return err
		}
		to, err := m.Lookup(args[1])
		if err != nil {
			return err
		}
		steps, err := m.Path(from, to, declaredOnly)
		if err != nil {
			return err
		}
		return writeERDRows(cmd.OutOrStdout(), erd.PathRows(steps), pathFormat)
	}}
	formatFlag(path, &pathFormat)
	path.Flags().BoolVar(&declaredOnly, "declared-only", false, "Only follow foreign keys declared in the database")
	group.AddCommand(path)

	root.AddCommand(group)
}

func erdPullCommand(store func() (Store, error), names cobra.CompletionFunc, model, dirFlag *string) *cobra.Command {
	var db, schema, output string
	var save bool
	var timeout time.Duration
	c := &cobra.Command{Use: "pull NAME", Short: "Generate DBML from the database schema", Args: cobra.ExactArgs(1), ValidArgsFunction: names}
	c.Flags().StringVar(&db, "db", "", "Server database to read")
	c.Flags().StringVar(&schema, "schema", "", "PostgreSQL schema to read (default public)")
	c.Flags().BoolVar(&save, "save", false, "Write .dbh/<model>.dbml instead of standard output")
	c.Flags().StringVarP(&output, "output", "o", "", "Write the DBML to this file")
	c.Flags().DurationVar(&timeout, "timeout", 60*time.Second, "Connection and schema read timeout")
	c.MarkFlagsMutuallyExclusive("save", "output")
	c.RunE = func(cmd *cobra.Command, args []string) error {
		targetModel := *model
		if targetModel == "" {
			targetModel = erd.DefaultModel
		}
		if err := erd.ValidModelName(targetModel); err != nil {
			return err
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
		if cmd.Flags().Changed("schema") && p.Driver != "postgres" {
			return fmt.Errorf("--schema is only supported for PostgreSQL")
		}
		dbml, err := pullDBML(cmd.Context(), p, db, cmd.Flags().Changed("db"), schema, timeout)
		if err != nil {
			return err
		}
		switch {
		case output != "":
			return erd.Save(output, dbml)
		case !save:
			_, err = io.WriteString(cmd.OutOrStdout(), dbml)
			return err
		}
		var dir string
		if *dirFlag != "" {
			dir, err = resolveSaveERDDir(*dirFlag)
			if err != nil {
				return err
			}
		} else {
			var create string
			dir, create, err = erd.FindDir(".")
			if err != nil {
				return err
			}
			if dir == "" {
				dir = create
			}
		}
		mainFile, _ := erd.ModelFiles(dir, targetModel)
		if err := erd.Save(mainFile, dbml); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Saved %s\n", erd.DisplayPath(mainFile)); err != nil {
			return err
		}
		m, err := erd.Load(dir, targetModel)
		if err != nil {
			return err
		}
		return writeIssues(cmd.ErrOrStderr(), m)
	}
	return c
}

func pullDBML(ctx context.Context, p Profile, db string, switchDB bool, schema string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	s, err := openSession(ctx, p, timeout)
	if err != nil {
		return "", err
	}
	defer s.Close()
	if switchDB {
		if db == "" {
			return "", fmt.Errorf("database name must not be empty")
		}
		if err := s.UseDatabase(ctx, db); err != nil {
			return "", err
		}
	}
	if p.Driver == "mysql" {
		schema = s.CurrentDatabase
	}
	pooled, ok := s.Conn.(database.SQLPool)
	if !ok {
		return "", fmt.Errorf("erd pull is not supported for %s", p.Driver)
	}
	pool, err := pooled.ReleasePool()
	if err != nil {
		return "", err
	}
	dbml, err := erd.Pull(ctx, pool, p.Driver, schema)
	if err != nil {
		return "", fmt.Errorf("cannot read schema: %s", redact(err.Error(), p.DSN, p.Proxies))
	}
	return dbml, nil
}

func resolveSaveERDDir(flagDir string) (string, error) {
	abs, err := filepath.Abs(flagDir)
	if err != nil {
		return "", err
	}
	if filepath.Base(abs) == erd.DirName {
		return abs, nil
	}
	return filepath.Join(abs, erd.DirName), nil
}

func resolveReadERDDir(flagDir string) (string, error) {
	abs, err := filepath.Abs(flagDir)
	if err != nil {
		return "", err
	}
	if filepath.Base(abs) != erd.DirName {
		nested := filepath.Join(abs, erd.DirName)
		if info, err := os.Stat(nested); err == nil && info.IsDir() {
			return nested, nil
		}
	}
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("no .dbh directory found at %s", erd.DisplayPath(abs))
		}
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", erd.DisplayPath(abs))
	}
	return abs, nil
}

func erdDirs(dirFlag string) ([]string, error) {
	if dirFlag != "" {
		dir, err := resolveReadERDDir(dirFlag)
		if err != nil {
			return nil, err
		}
		return []string{dir}, nil
	}
	dirs, err := erd.FindAllDirs(".")
	if err != nil {
		return nil, err
	}
	if len(dirs) == 0 {
		return nil, errors.New("no .dbh directory found; run dbh erd pull NAME --save")
	}
	return dirs, nil
}

func loadERD(dirFlag, model string) (*erd.Model, error) {
	if model != "" {
		if err := erd.ValidModelName(model); err != nil {
			return nil, err
		}
	}
	dirs, err := erdDirs(dirFlag)
	if err != nil {
		return nil, err
	}
	return erd.LoadAll(dirs, model)
}

func writeIssues(w io.Writer, m *erd.Model) error {
	for _, issue := range m.Errors {
		if _, err := fmt.Fprintf(w, "error: %s\n", issue); err != nil {
			return err
		}
	}
	for _, issue := range m.Warnings {
		if _, err := fmt.Fprintf(w, "warning: %s\n", issue); err != nil {
			return err
		}
	}
	return nil
}

func writeERDRows(out io.Writer, rows erd.Rows, format string) error {
	return writeRows(out, rows.Columns, rows.Values, format)
}
