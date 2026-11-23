package dbh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/benenen/dbh/internal/database"
	"github.com/benenen/dbh/internal/erd"
	"github.com/spf13/cobra"
)

var openBrowser = defaultOpenBrowser

func defaultOpenBrowser(target string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{target}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", target}
	default:
		name, args = "xdg-open", []string{target}
	}
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cannot open browser with %s: %w", name, err)
	}
	return nil
}

func addERDCommands(root *cobra.Command, store func() (Store, error), names cobra.CompletionFunc) {
	var model, dirFlag string
	group := &cobra.Command{Use: "erd", Aliases: []string{"er"}, Short: "Query the DBML relationship models in .dbh/", Args: cobra.NoArgs}
	group.PersistentFlags().StringVar(&model, "model", "", "Model name: .dbh/<model>.dbml and .dbh/<model>.refs.dbml (default: all models for queries, \"schema\" for pull)")
	group.PersistentFlags().StringVar(&dirFlag, "dir", "", "Limit to one .dbh directory (default: scan every .dbh directory in the project)")
	_ = group.RegisterFlagCompletionFunc("model", func(cmd *cobra.Command, args []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
		dirs, err := erdDirs(dirFlag)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		seen := map[string]bool{}
		var completions []cobra.Completion
		for _, d := range dirs {
			list, err := erd.List(d)
			if err != nil {
				continue
			}
			for _, name := range list {
				if !seen[name] {
					seen[name] = true
					completions = append(completions, name)
				}
			}
			refsOnly, err := erd.ListRefsOnly(d)
			if err != nil {
				continue
			}
			for _, name := range refsOnly {
				if !seen[name] {
					seen[name] = true
					completions = append(completions, name)
				}
			}
		}
		sort.Strings(completions)
		return completions, cobra.ShellCompDirectiveNoFileComp
	})

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
	// lookup resolves a table name and warns when models disagree on its columns.
	lookup := func(cmd *cobra.Command, m *erd.Model, name string) (string, error) {
		table, err := m.Lookup(name)
		if err != nil {
			return "", err
		}
		if msg, ok := m.TableConflict(table); ok {
			if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", msg); err != nil {
				return "", err
			}
		}
		return table, nil
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
	group.AddCommand(erdDiffCommand(store, names, &model, &dirFlag))

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
			return fmt.Errorf("format must be table, csv, json or tsv")
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

	var tablesFormat, tablesMatch string
	var tablesIsolated, tablesWithModel bool
	tables := &cobra.Command{Use: "tables", Short: "List the tables in the model", Args: cobra.NoArgs, ValidArgsFunction: cobra.NoFileCompletions, RunE: func(cmd *cobra.Command, args []string) error {
		if !validFormat(tablesFormat) {
			return fmt.Errorf("format must be table, csv, json or tsv")
		}
		m, err := open(cmd)
		if err != nil {
			return err
		}
		return writeERDRows(cmd.OutOrStdout(), m.TableRowsFiltered(tablesMatch, tablesIsolated, tablesWithModel), tablesFormat)
	}}
	formatFlag(tables, &tablesFormat)
	tables.Flags().StringVar(&tablesMatch, "match", "", "Filter tables by substring or glob pattern on table name/note")
	tables.Flags().BoolVar(&tablesIsolated, "isolated", false, "Only list tables with no relations")
	tables.Flags().BoolVar(&tablesWithModel, "with-model", false, "Include source model column")
	group.AddCommand(tables)

	var relationsFormat, relationsTable, relationsSource string
	relations := &cobra.Command{Use: "relations", Short: "List relations, optionally for one table or source", Args: cobra.NoArgs, ValidArgsFunction: cobra.NoFileCompletions, RunE: func(cmd *cobra.Command, args []string) error {
		if !validFormat(relationsFormat) {
			return fmt.Errorf("format must be table, csv, json or tsv")
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
			if table, err = lookup(cmd, m, relationsTable); err != nil {
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

	var showFormat, showOutput, showSource string
	var showOpen, showDeclaredOnly bool
	var depth int
	show := &cobra.Command{Use: "show [TABLE...]", Short: "Preview tables and relations as text, Graphviz dot, Mermaid or HTML", ValidArgsFunction: tableNames, RunE: func(cmd *cobra.Command, args []string) error {
		if depth < 0 {
			return fmt.Errorf("depth must be nonnegative")
		}
		if showSource != "" && showSource != erd.SourceDeclared && showSource != erd.SourceInferred {
			return fmt.Errorf("source must be declared or inferred")
		}
		effectiveSource := showSource
		if showDeclaredOnly {
			if showSource == erd.SourceInferred {
				return fmt.Errorf("--declared-only cannot be combined with --source inferred")
			}
			effectiveSource = erd.SourceDeclared
		}
		if showOpen && !cmd.Flags().Changed("format") {
			showFormat = "html"
		}
		if showOpen && showFormat != "html" {
			return fmt.Errorf("--open is only supported with --format html")
		}
		var render func(io.Writer, *erd.Model, map[string]bool) error
		switch showFormat {
		case "text":
			render = erd.RenderText
		case "dot":
			render = erd.RenderDot
		case "mermaid":
			render = erd.RenderMermaid
		case "html":
			render = erd.RenderHTML
		default:
			return fmt.Errorf("show format must be text, dot, mermaid or html")
		}
		m, err := open(cmd)
		if err != nil {
			return err
		}
		view := m.FilterSource(effectiveSource)
		var include map[string]bool
		if len(args) > 0 {
			start := make([]string, len(args))
			for i, arg := range args {
				if start[i], err = lookup(cmd, view, arg); err != nil {
					return err
				}
			}
			include = view.NeighborhoodFiltered(start, depth, effectiveSource)
		}
		if showOutput != "" || showOpen {
			var buf bytes.Buffer
			if err := render(&buf, view, include); err != nil {
				return err
			}
			target := showOutput
			if target != "" {
				if err := erd.Save(target, buf.String()); err != nil {
					return err
				}
			} else {
				f, err := os.CreateTemp("", "dbh-erd-*.html")
				if err != nil {
					return err
				}
				target = f.Name()
				if _, err := f.WriteString(buf.String()); err != nil {
					_ = f.Close()
					return err
				}
				if err := f.Close(); err != nil {
					return err
				}
			}
			if showOpen {
				absTarget, err := filepath.Abs(target)
				if err == nil {
					target = absTarget
				}
				if err := openBrowser(target); err != nil {
					return fmt.Errorf("%w (saved at %s)", err, target)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Opened %s\n", erd.DisplayPath(target))
				return err
			}
			return nil
		}
		return render(cmd.OutOrStdout(), view, include)
	}}
	show.Flags().StringVar(&showFormat, "format", "text", "text, dot, mermaid or html")
	show.Flags().IntVar(&depth, "depth", 1, "Include tables up to this many relations away from TABLE")
	show.Flags().StringVarP(&showOutput, "output", "o", "", "Write the rendered diagram to this file")
	show.Flags().BoolVar(&showOpen, "open", false, "Open the rendered HTML diagram in the default browser")
	show.Flags().BoolVar(&showDeclaredOnly, "declared-only", false, "Only show foreign keys declared in the database")
	show.Flags().StringVar(&showSource, "source", "", "Only show declared or inferred relations")
	_ = show.RegisterFlagCompletionFunc("format", cobra.FixedCompletions([]string{"text", "dot", "mermaid", "html"}, cobra.ShellCompDirectiveNoFileComp))
	_ = show.RegisterFlagCompletionFunc("source", cobra.FixedCompletions([]string{erd.SourceDeclared, erd.SourceInferred}, cobra.ShellCompDirectiveNoFileComp))
	group.AddCommand(show)

	var pathFormat string
	var declaredOnly, pathAll bool
	path := &cobra.Command{Use: "path FROM TO", Short: "Show the shortest relation path between two tables", Args: cobra.ExactArgs(2), ValidArgsFunction: tableNames, RunE: func(cmd *cobra.Command, args []string) error {
		if !validFormat(pathFormat) {
			return fmt.Errorf("format must be table, csv, json or tsv")
		}
		m, err := open(cmd)
		if err != nil {
			return err
		}
		from, err := lookup(cmd, m, args[0])
		if err != nil {
			return err
		}
		to, err := lookup(cmd, m, args[1])
		if err != nil {
			return err
		}
		if pathAll {
			paths, err := m.AllPaths(from, to, declaredOnly, 20)
			if err != nil {
				return err
			}
			return writeERDRows(cmd.OutOrStdout(), erd.PathRowsMulti(paths), pathFormat)
		}
		steps, err := m.Path(from, to, declaredOnly)
		if err != nil {
			return err
		}
		return writeERDRows(cmd.OutOrStdout(), erd.PathRows(steps), pathFormat)
	}}
	formatFlag(path, &pathFormat)
	path.Flags().BoolVar(&declaredOnly, "declared-only", false, "Only follow foreign keys declared in the database")
	path.Flags().BoolVar(&pathAll, "all", false, "Show all shortest paths (including parallel foreign keys and conditional relations)")
	group.AddCommand(path)

	root.AddCommand(group)
}

func erdPullCommand(store func() (Store, error), names cobra.CompletionFunc, model, dirFlag *string) *cobra.Command {
	var db, schema, output string
	var include, exclude []string
	var save bool
	var timeout time.Duration
	c := &cobra.Command{Use: "pull NAME", Short: "Generate DBML from the database schema", Args: cobra.ExactArgs(1), ValidArgsFunction: names}
	c.Flags().StringVar(&db, "db", "", "Server database to read")
	c.Flags().StringVar(&schema, "schema", "", "PostgreSQL schema(s) to read, comma-separated (default public)")
	c.Flags().StringSliceVar(&include, "include", nil, "Only include tables matching glob pattern (repeatable)")
	c.Flags().StringSliceVar(&exclude, "exclude", nil, "Exclude tables matching glob pattern (repeatable)")
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
		stopSpinner := startSpinner(cmd.ErrOrStderr(), "Reading schema")
		dbml, err := pullDBMLWithOptions(cmd.Context(), p, db, cmd.Flags().Changed("db"), schema, include, exclude, timeout)
		stopSpinner()
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
		var prevModel *erd.Model
		if prevBytes, err := os.ReadFile(mainFile); err == nil {
			prevModel = erd.ParseDBML(targetModel, mainFile, string(prevBytes))
		}
		if err := erd.Save(mainFile, dbml); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Saved %s\n", erd.DisplayPath(mainFile)); err != nil {
			return err
		}
		if prevModel != nil && len(prevModel.Errors) == 0 {
			nextModel := erd.ParseDBML(targetModel, mainFile, dbml)
			if len(nextModel.Errors) == 0 {
				diff := erd.DiffModels(prevModel, nextModel)
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Changes: %s\n", diff.Summary()); err != nil {
					return err
				}
			}
		}
		var m *erd.Model
		if *dirFlag == "" {
			if allDirs, scanErr := erd.FindAllDirs("."); scanErr == nil && len(allDirs) > 1 {
				m, err = erd.ValidateModelRefs(dir, targetModel, allDirs)
			} else {
				m, err = erd.Load(dir, targetModel)
			}
		} else {
			m, err = erd.Load(dir, targetModel)
		}
		if err != nil {
			return err
		}
		return writeIssues(cmd.ErrOrStderr(), m)
	}
	return c
}

func erdDiffCommand(store func() (Store, error), names cobra.CompletionFunc, model, dirFlag *string) *cobra.Command {
	var db, schema, format string
	var include, exclude []string
	var timeout time.Duration
	c := &cobra.Command{Use: "diff NAME", Short: "Compare the local .dbh/<model>.dbml against the live database schema", Args: cobra.ExactArgs(1), ValidArgsFunction: names}
	c.Flags().StringVar(&db, "db", "", "Server database to read")
	c.Flags().StringVar(&schema, "schema", "", "PostgreSQL schema(s) to read, comma-separated (default public)")
	c.Flags().StringSliceVar(&include, "include", nil, "Only include tables matching glob pattern (repeatable)")
	c.Flags().StringSliceVar(&exclude, "exclude", nil, "Exclude tables matching glob pattern (repeatable)")
	c.Flags().DurationVar(&timeout, "timeout", 60*time.Second, "Connection and schema read timeout")
	c.Flags().StringVar(&format, "format", "table", "table, csv, json or tsv")
	_ = c.RegisterFlagCompletionFunc("format", cobra.FixedCompletions([]string{"table", "csv", "json", "tsv"}, cobra.ShellCompDirectiveNoFileComp))
	c.RunE = func(cmd *cobra.Command, args []string) error {
		if !validFormat(format) {
			return fmt.Errorf("format must be table, csv, json or tsv")
		}
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
		var dir string
		var err error
		if *dirFlag != "" {
			dir, err = resolveReadERDDir(*dirFlag)
		} else {
			dir, _, err = erd.FindDir(".")
			if err == nil && dir == "" {
				err = errors.New("no .dbh directory found; run dbh erd pull NAME --save")
			}
		}
		if err != nil {
			return err
		}
		local, err := erd.Load(dir, targetModel)
		if err != nil {
			return err
		}
		if err := local.Usable(); err != nil {
			return err
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
		stopSpinner := startSpinner(cmd.ErrOrStderr(), "Reading schema")
		dbml, err := pullDBMLWithOptions(cmd.Context(), p, db, cmd.Flags().Changed("db"), schema, include, exclude, timeout)
		stopSpinner()
		if err != nil {
			return err
		}
		remote := erd.ParseDBML(targetModel, "<remote>", dbml)
		if len(remote.Errors) > 0 {
			return fmt.Errorf("cannot parse pulled schema: %s", remote.Errors[0])
		}
		diff := erd.DiffModels(local, remote)
		return writeERDRows(cmd.OutOrStdout(), diff.Rows(), format)
	}
	return c
}

func pullDBML(ctx context.Context, p Profile, db string, switchDB bool, schema string, timeout time.Duration) (string, error) {
	return pullDBMLWithOptions(ctx, p, db, switchDB, schema, nil, nil, timeout)
}

func pullDBMLWithOptions(ctx context.Context, p Profile, db string, switchDB bool, schema string, include, exclude []string, timeout time.Duration) (string, error) {
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
	var schemas []string
	if p.Driver == "mysql" {
		if s.CurrentDatabase != "" {
			schemas = []string{s.CurrentDatabase}
		}
	} else {
		for _, part := range strings.Split(schema, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				schemas = append(schemas, trimmed)
			}
		}
	}
	pooled, ok := s.Conn.(database.SQLPool)
	if !ok {
		return "", fmt.Errorf("erd pull is not supported for %s", p.Driver)
	}
	pool, err := pooled.ReleasePool()
	if err != nil {
		return "", err
	}
	dbml, err := erd.PullWithOptions(ctx, pool, p.Driver, erd.PullOptions{
		Schemas: schemas,
		Include: include,
		Exclude: exclude,
	})
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
