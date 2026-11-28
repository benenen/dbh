package dbh

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/benenen/dbh/internal/database"
	"github.com/benenen/dbh/internal/database/proxy"
	"github.com/benenen/dbh/internal/database/registry"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func NewCommand() *cobra.Command {
	var dir string
	var historyLimit int
	root := &cobra.Command{Use: "dbh", Version: version, Short: "Manage database connections and run queries", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().StringVar(&dir, "config-dir", "", "Configuration directory (or DBH_CONFIG_DIR)")
	root.PersistentFlags().IntVar(&historyLimit, "history-limit", 1000, "Maximum retained history entries (0 for unlimited)")
	store := func() (Store, error) {
		d := dir
		if d == "" {
			var err error
			d, err = defaultDir()
			if err != nil {
				return Store{}, err
			}
		}
		if historyLimit < 0 {
			return Store{}, fmt.Errorf("history-limit must be nonnegative")
		}
		return Store{Dir: d, HistoryLimit: historyLimit}, nil
	}
	// Shell completion offers saved connection names for the NAME argument.
	names := func(_ *cobra.Command, args []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		s, err := store()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		profiles, err := s.List()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		completions := make([]cobra.Completion, len(profiles))
		for i, p := range profiles {
			completions[i] = cobra.CompletionWithDesc(p.Name, p.Driver)
		}
		return completions, cobra.ShellCompDirectiveNoFileComp
	}
	files := func(*cobra.Command, []string, string) ([]cobra.Completion, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveDefault
	}
	fixed := func(values ...string) cobra.CompletionFunc {
		return cobra.FixedCompletions(values, cobra.ShellCompDirectiveNoFileComp)
	}
	var listFormat, listDriver, listTag string
	ls := &cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "List saved connections (credentials are hidden)", Args: cobra.NoArgs, ValidArgsFunction: cobra.NoFileCompletions, RunE: func(cmd *cobra.Command, args []string) error {
		if listFormat != "" && !validFormat(listFormat) {
			return fmt.Errorf("format must be table, csv, json or tsv")
		}
		if listDriver != "" {
			if _, err := registry.Lookup(listDriver); err != nil {
				return err
			}
		}
		s, err := store()
		if err != nil {
			return err
		}
		profiles, err := s.List()
		if err != nil {
			return err
		}
		var filtered []Profile
		for _, p := range profiles {
			if listDriver != "" && !strings.EqualFold(p.Driver, listDriver) {
				continue
			}
			if listTag != "" && !hasTag(p, listTag) {
				continue
			}
			filtered = append(filtered, p)
		}
		formatProxyCol := func(proxies []string) string {
			if len(proxies) == 0 {
				return "-"
			}
			hops := make([]string, len(proxies))
			for i, hop := range proxies {
				hops[i] = proxy.Display(hop)
			}
			return strings.Join(hops, " -> ")
		}
		if listFormat != "" {
			values := make([][]any, 0, len(filtered))
			for _, p := range filtered {
				values = append(values, []any{p.Name, p.Driver, p.Env, p.ReadOnly, strings.Join(p.Tags, ","), formatProxyCol(p.Proxies)})
			}
			return writeRows(cmd.OutOrStdout(), []string{"name", "driver", "env", "read_only", "tags", "proxies"}, values, listFormat)
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		if _, err := fmt.Fprintln(w, "NAME\tDRIVER\tENV\tMODE\tTAGS\tPROXIES"); err != nil {
			return err
		}
		for _, p := range filtered {
			envCol := p.Env
			if envCol == "" {
				envCol = "-"
			}
			modeCol := "rw"
			if p.ReadOnly {
				modeCol = "ro"
			}
			tagsCol := "-"
			if len(p.Tags) > 0 {
				tagsCol = strings.Join(p.Tags, ",")
			}
			if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", p.Name, p.Driver, envCol, modeCol, tagsCol, formatProxyCol(p.Proxies)); err != nil {
				return err
			}
		}
		return w.Flush()
	}}
	ls.Flags().StringVar(&listFormat, "format", "", "Structured output: table, csv, json or tsv")
	ls.Flags().StringVar(&listDriver, "driver", "", "Filter connections by driver")
	ls.Flags().StringVar(&listTag, "tag", "", "Filter connections by tag")
	_ = ls.RegisterFlagCompletionFunc("format", fixed("table", "csv", "json", "tsv"))
	_ = ls.RegisterFlagCompletionFunc("driver", fixed(registry.Drivers()...))
	root.AddCommand(ls)
	for _, create := range []bool{true, false} {
		var driver, dsn, dsnEnv, dsnCmd, env string
		var proxies, tags []string
		var prompt, noProxy, readOnly, noReadOnly, noTags bool
		verb := "new"
		short := "Create a connection"
		if !create {
			verb = "edit"
			short = "Update a saved connection"
		}
		c := &cobra.Command{Use: verb + " NAME", Aliases: []string{verb[:1]}, Short: short, Args: cobra.ExactArgs(1), ValidArgsFunction: cobra.NoFileCompletions}
		if !create {
			c.ValidArgsFunction = names
		}
		c.Flags().StringVar(&driver, "driver", "", strings.Join(registry.Drivers(), ", "))
		c.Flags().StringVar(&dsn, "dsn", "", "Driver DSN (stored encrypted locally)")
		c.Flags().StringVar(&dsnEnv, "dsn-env", "", "Read DSN from this environment variable")
		c.Flags().StringVar(&dsnCmd, "dsn-cmd", "", "Shell command executed at connect time to obtain DSN")
		c.Flags().BoolVar(&prompt, "prompt-dsn", false, "Read DSN with terminal echo disabled")
		c.Flags().StringArrayVar(&proxies, "proxy", nil, "Proxy hop socks5://[user:pass@]host:port or ssh://[user[:pass]@]host[:port]; repeat in dial order")
		c.Flags().BoolVar(&readOnly, "read-only", false, "Block write statements by default")
		c.Flags().StringVar(&env, "env", "", "Environment: dev, test, staging or prod")
		c.Flags().StringArrayVar(&tags, "tag", nil, "Connection tag; repeat to set multiple")
		c.MarkFlagsMutuallyExclusive("dsn", "dsn-env", "dsn-cmd", "prompt-dsn")
		_ = c.RegisterFlagCompletionFunc("driver", fixed(registry.Drivers()...))
		_ = c.RegisterFlagCompletionFunc("env", fixed("dev", "test", "staging", "prod"))
		// SQLite DSNs are file paths.
		_ = c.RegisterFlagCompletionFunc("dsn", files)
		_ = c.RegisterFlagCompletionFunc("dsn-env", func(_ *cobra.Command, _ []string, prefix string) ([]cobra.Completion, cobra.ShellCompDirective) {
			var completions []cobra.Completion
			for _, entry := range os.Environ() {
				if name, _, _ := strings.Cut(entry, "="); strings.HasPrefix(name, prefix) {
					completions = append(completions, name)
				}
			}
			return completions, cobra.ShellCompDirectiveNoFileComp
		})
		_ = c.RegisterFlagCompletionFunc("proxy", cobra.NoFileCompletions)
		if !create {
			c.Flags().BoolVar(&noProxy, "no-proxy", false, "Remove all proxies")
			c.Flags().BoolVar(&noReadOnly, "no-read-only", false, "Disable read-only protection")
			c.Flags().BoolVar(&noTags, "no-tags", false, "Remove all tags")
			c.MarkFlagsMutuallyExclusive("proxy", "no-proxy")
			c.MarkFlagsMutuallyExclusive("read-only", "no-read-only")
			c.MarkFlagsMutuallyExclusive("tag", "no-tags")
		}
		c.RunE = func(cmd *cobra.Command, args []string) error {
			s, err := store()
			if err != nil {
				return err
			}
			p := Profile{Name: args[0]}
			if !create {
				p, err = s.Get(args[0])
				if err != nil {
					return err
				}
			}
			if create || cmd.Flags().Changed("driver") {
				p.Driver = driver
			}
			if cmd.Flags().Changed("dsn") {
				p.DSN = dsn
				p.DSNCmd = ""
			}
			if cmd.Flags().Changed("dsn-env") {
				value, ok := os.LookupEnv(dsnEnv)
				if !ok {
					return fmt.Errorf("environment variable %q is not set", dsnEnv)
				}
				p.DSN = value
				p.DSNCmd = ""
			}
			if cmd.Flags().Changed("dsn-cmd") {
				p.DSNCmd = dsnCmd
				p.DSN = ""
			}
			if cmd.Flags().Changed("proxy") {
				p.Proxies = proxies
			}
			if noProxy {
				p.Proxies = nil
			}
			if cmd.Flags().Changed("read-only") {
				p.ReadOnly = readOnly
			}
			if noReadOnly {
				p.ReadOnly = false
			}
			if cmd.Flags().Changed("env") {
				normEnv, envErr := normalizeEnv(env)
				if envErr != nil {
					return envErr
				}
				p.Env = normEnv
			}
			if cmd.Flags().Changed("tag") {
				p.Tags = tags
			}
			if noTags {
				p.Tags = nil
			}
			if prompt {
				if !term.IsTerminal(int(os.Stdin.Fd())) {
					return fmt.Errorf("--prompt-dsn requires a terminal")
				}
				_, _ = fmt.Fprint(cmd.ErrOrStderr(), "DSN: ")
				value, err := term.ReadPassword(int(os.Stdin.Fd()))
				_, _ = fmt.Fprintln(cmd.ErrOrStderr())
				if err != nil {
					return err
				}
				p.DSN = string(value)
				p.DSNCmd = ""
			}
			if !create && !cmd.Flags().Changed("driver") && !cmd.Flags().Changed("dsn") && !cmd.Flags().Changed("dsn-env") && !cmd.Flags().Changed("dsn-cmd") && !prompt && !cmd.Flags().Changed("proxy") && !noProxy && !cmd.Flags().Changed("read-only") && !noReadOnly && !cmd.Flags().Changed("env") && !cmd.Flags().Changed("tag") && !noTags {
				return fmt.Errorf("provide --driver, --dsn, --dsn-env, --dsn-cmd, --prompt-dsn, --proxy, --no-proxy, --read-only, --env or --tag")
			}
			if err = s.Put(p, create); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Saved %s (%s)\n", p.Name, p.Driver)
			return err
		}
		root.AddCommand(c)
	}
	var keepHistory bool
	rmCmd := &cobra.Command{Use: "remove NAME", Aliases: []string{"rm", "r"}, Short: "Remove a saved connection (and its history unless --keep-history)", Args: cobra.ExactArgs(1), ValidArgsFunction: names, RunE: func(cmd *cobra.Command, args []string) error {
		s, err := store()
		if err != nil {
			return err
		}
		if err = s.RemoveWithOptions(args[0], keepHistory); err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "Removed", args[0])
		return err
	}}
	rmCmd.Flags().BoolVar(&keepHistory, "keep-history", false, "Keep saved SQL history file for this connection")
	root.AddCommand(rmCmd)
	for _, executeOnly := range []bool{false, true} {
		var query, file, format, databaseName, outputPath string
		var adhocDriver, adhocDSN string
		var adhocProxies []string
		var timeout time.Duration
		var noHistory, stats, allowWrite, expanded, adhocReadOnly bool
		var maxRows, maxCellWidth, batchSize int
		var params []string
		connect := &cobra.Command{Use: "connect NAME", Aliases: []string{"c"}, Short: "Open a database shell, or execute queries from --sql, --file or stdin", Args: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("dsn") {
				if len(args) > 0 {
					return fmt.Errorf("connect does not accept a positional NAME when --dsn is set")
				}
				return nil
			}
			switch len(args) {
			case 0:
				return fmt.Errorf("missing connection NAME (usage: dbh %s NAME)", cmd.CalledAs())
			case 1:
				return nil
			case 2:
				return fmt.Errorf("connect does not take a positional query; use `dbh exec %s %q` or `dbh %s %s -e %q`", args[0], args[1], cmd.CalledAs(), args[0], args[1])
			default:
				return fmt.Errorf("connect expects 1 argument (NAME), got %d; to run a query use `dbh exec NAME QUERY` or `--sql`", len(args))
			}
		}, ValidArgsFunction: names, RunE: func(cmd *cobra.Command, args []string) error {
			var targetArg string
			hasPositionalQuery := false
			if cmd.Flags().Changed("dsn") {
				if executeOnly && len(args) == 1 {
					hasPositionalQuery = true
					if cmd.Flags().Changed("sql") || cmd.Flags().Changed("file") {
						return fmt.Errorf("positional query cannot be combined with --sql or --file")
					}
					query = args[0]
				}
			} else {
				targetArg = args[0]
				if executeOnly && len(args) == 2 {
					hasPositionalQuery = true
					if cmd.Flags().Changed("sql") || cmd.Flags().Changed("file") {
						return fmt.Errorf("positional query cannot be combined with --sql or --file")
					}
					query = args[1]
				}
			}
			if cmd.Flags().Changed("db") && strings.TrimSpace(databaseName) == "" {
				return fmt.Errorf("database name must not be empty")
			}
			if !validFormat(format) {
				return fmt.Errorf("format must be table, csv, json or tsv")
			}
			if maxRows < 0 || maxCellWidth < 0 || batchSize <= 0 {
				return fmt.Errorf("max-rows and max-cell-width must be nonnegative and table-batch-size positive")
			}
			if timeout <= 0 {
				return fmt.Errorf("timeout must be positive")
			}
			s, err := store()
			if err != nil {
				return err
			}
			p, isAdhoc, err := resolveTarget(s, targetArg, adhocOptions{
				Driver:   adhocDriver,
				DSN:      adhocDSN,
				Proxies:  adhocProxies,
				ReadOnly: adhocReadOnly,
			})
			if err != nil {
				return err
			}
			if isAdhoc {
				noHistory = true
			}
			var input string
			batch := false
			switch {
			case cmd.Flags().Changed("sql") || hasPositionalQuery:
				input = query
				batch = true
			case cmd.Flags().Changed("file"):
				var b []byte
				if file == "-" {
					b, err = io.ReadAll(cmd.InOrStdin())
				} else {
					b, err = os.ReadFile(file)
				}
				if err != nil {
					return err
				}
				input = string(b)
				batch = true
			default:
				if executeOnly || !term.IsTerminal(int(os.Stdin.Fd())) {
					if executeOnly && term.IsTerminal(int(os.Stdin.Fd())) {
						return fmt.Errorf("provide a query, --sql, --file or piped stdin")
					}
					b, e := io.ReadAll(cmd.InOrStdin())
					if e != nil {
						return e
					}
					input = string(b)
					batch = true
				}
			}
			if executeOnly && strings.TrimSpace(input) == "" {
				return fmt.Errorf("provide a query, --sql, --file or piped stdin")
			}
			session, err := openSession(cmd.Context(), p, timeout)
			if err != nil {
				return err
			}
			defer session.Close()
			session.MaxRows, session.BatchSize, session.Notice = maxRows, batchSize, cmd.ErrOrStderr()
			session.MaxCellWidth = maxCellWidth
			session.MaxCellWidthSet = cmd.Flags().Changed("max-cell-width")
			session.AllowWrite = allowWrite
			session.Expanded = expanded
			session.Stats = stats
			session.AuditStore = &s
			if cmd.Flags().Changed("db") {
				if err := session.UseDatabase(cmd.Context(), databaseName); err != nil {
					return err
				}
			}
			if batch {
				statements, rest, err := splitSQL(input, p.Driver)
				if err != nil {
					return err
				}
				if stripComments(rest, p.Driver) != "" {
					statements = append(statements, rest)
				}
				if !noHistory {
					if err := s.prepare(); err != nil {
						return err
					}
				}
				if len(params) > 0 {
					backend, lookupErr := registry.Lookup(p.Driver)
					syntax := database.Syntax{}
					if lookupErr == nil {
						syntax = backend.Syntax()
					}
					if len(statements) != 1 || syntax.JSONCommands || syntax.LineCommands || syntax.HTTPCommands {
						return fmt.Errorf("--param requires exactly one SQL statement")
					}
				}
				bound := make([]any, len(params))
				for i, value := range params {
					decoder := json.NewDecoder(strings.NewReader(value))
					decoder.UseNumber()
					var v any
					if err := decoder.Decode(&v); err != nil {
						return fmt.Errorf("parameter %d must be a JSON scalar: %w", i+1, err)
					}
					var extra any
					if err := decoder.Decode(&extra); err != io.EOF {
						return fmt.Errorf("parameter %d must contain one JSON scalar", i+1)
					}
					switch n := v.(type) {
					case json.Number:
						if integer, err := n.Int64(); err == nil {
							v = integer
						} else if number, err := n.Float64(); err == nil {
							v = number
						} else {
							return err
						}
					case nil, string, bool:
					default:
						return fmt.Errorf("parameter %d must be a JSON scalar", i+1)
					}
					bound[i] = v
				}
				outWriter := cmd.OutOrStdout()
				var outBuf bytes.Buffer
				if outputPath != "" {
					outWriter = &outBuf
				}
				for _, q := range statements {
					if !noHistory {
						if err := saveHistory(s, p.Name, terminate(q, p.Driver)); err != nil {
							return err
						}
					}
					stopSpinner := startSpinner(cmd.ErrOrStderr(), "Running query")
					session.BeforeOutput = stopSpinner
					execute := func() error { return session.Execute(cmd.Context(), q, format, outWriter) }
					if len(bound) > 0 {
						execute = func() error { return session.executeArgs(cmd.Context(), q, format, outWriter, bound...) }
					}
					execErr := execute()
					stopSpinner()
					session.BeforeOutput = nil
					if execErr != nil {
						return execErr
					}
				}
				if outputPath != "" {
					return writeAtomicFile(outputPath, outBuf.Bytes())
				}
				return nil
			}
			if !cmd.Flags().Changed("max-rows") {
				session.MaxRows = defaultShellMaxRows
			}
			return shell(cmd.Context(), session, p, s, format, noHistory, cmd.OutOrStdout(), cmd.ErrOrStderr())
		}}
		connect.Flags().StringVar(&databaseName, "db", "", "Server database to use (defaults to the saved connection database)")
		if executeOnly {
			connect.Flags().StringArrayVar(&params, "param", nil, "Bind a JSON scalar to a native SQL placeholder; repeat in order")
			connect.Flags().StringVarP(&outputPath, "output", "o", "", "Write query output atomically to this file")
			_ = connect.RegisterFlagCompletionFunc("output", files)
			connect.Use = "exec NAME [QUERY]"
			connect.Aliases = []string{"x"}
			connect.Short = "Execute queries using a saved connection and optional database"
			connect.Args = func(cmd *cobra.Command, args []string) error {
				if cmd.Flags().Changed("dsn") {
					if len(args) > 1 {
						return fmt.Errorf("exec with --dsn expects at most 1 positional argument (QUERY), got %d", len(args))
					}
					return nil
				}
				switch {
				case len(args) == 0:
					return fmt.Errorf("missing connection NAME (usage: dbh %s NAME [QUERY])", cmd.CalledAs())
				case len(args) > 2:
					return fmt.Errorf("exec expects 1 or 2 arguments (NAME [QUERY]), got %d; quote the query as a single argument or use --sql / --file", len(args))
				default:
					return nil
				}
			}
		}
		connect.Flags().StringVar(&adhocDSN, "dsn", "", "Ad-hoc DSN or URL (does not save a connection profile)")
		connect.Flags().StringVar(&adhocDriver, "driver", "", "Driver for ad-hoc target ("+strings.Join(registry.Drivers(), ", ")+")")
		connect.Flags().StringArrayVar(&adhocProxies, "proxy", nil, "Proxy hop for ad-hoc target; repeat in dial order")
		connect.Flags().BoolVar(&adhocReadOnly, "read-only", false, "Block write statements on ad-hoc connection")
		_ = connect.RegisterFlagCompletionFunc("driver", fixed(registry.Drivers()...))
		_ = connect.RegisterFlagCompletionFunc("dsn", files)
		_ = connect.RegisterFlagCompletionFunc("proxy", cobra.NoFileCompletions)
		connect.Flags().StringVarP(&query, "sql", "e", "", "Execute SQL, a MongoDB JSON command or Redis commands and exit")
		connect.Flags().StringVarP(&file, "file", "f", "", "Execute a query file (- for stdin)")
		connect.Flags().StringVar(&format, "format", "table", "table, csv, json (newline-delimited objects) or tsv (compact, for agents)")
		connect.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "Connection/query timeout")
		connect.Flags().IntVar(&maxRows, "max-rows", 0, "Maximum result rows per statement (0 for unlimited)")
		connect.Flags().IntVar(&maxCellWidth, "max-cell-width", 0, "Maximum cell display width (0 for unlimited; table defaults to 200)")
		connect.Flags().IntVar(&batchSize, "table-batch-size", 500, "Rows per rendered table batch")
		connect.Flags().BoolVar(&stats, "stats", false, "Write query timing and row counts to stderr")
		connect.Flags().BoolVar(&noHistory, "no-history", false, "Disable persisted query history")
		connect.Flags().BoolVar(&allowWrite, "allow-write", false, "Allow write or destructive statements on read-only/prod connections")
		connect.Flags().BoolVarP(&expanded, "expanded", "x", false, "Display table rows vertically")
		connect.MarkFlagsMutuallyExclusive("sql", "file")
		_ = connect.RegisterFlagCompletionFunc("file", files)
		_ = connect.RegisterFlagCompletionFunc("format", fixed("table", "csv", "json", "tsv"))
		for _, flag := range []string{"sql", "timeout", "db"} {
			if connect.Flags().Lookup(flag) != nil {
				_ = connect.RegisterFlagCompletionFunc(flag, cobra.NoFileCompletions)
			}
		}
		root.AddCommand(connect)
	}
	var historySearch, historyFormat string
	var historyLimitInput int
	var clearHistory bool
	historyCommand := &cobra.Command{Use: "history NAME", Short: "Show SQL history for a connection", Args: cobra.ExactArgs(1), ValidArgsFunction: names, RunE: func(cmd *cobra.Command, args []string) error {
		if historyLimitInput < 0 {
			return fmt.Errorf("limit must be nonnegative")
		}
		if historyFormat != "" && !validFormat(historyFormat) {
			return fmt.Errorf("format must be table, csv, json or tsv")
		}
		s, err := store()
		if err != nil {
			return err
		}
		if _, err = s.Get(args[0]); err != nil {
			return err
		}
		if clearHistory {
			return updateHistory(s, args[0], func([]string) []string { return nil })
		}
		entries, err := readHistory(s, args[0])
		if err != nil {
			return err
		}
		type matchedEntry struct {
			index int
			query string
		}
		var matched []matchedEntry
		for i, q := range entries {
			if historySearch != "" && !strings.Contains(strings.ToLower(q), strings.ToLower(historySearch)) {
				continue
			}
			matched = append(matched, matchedEntry{index: i + 1, query: q})
		}
		if historyLimitInput > 0 && len(matched) > historyLimitInput {
			matched = matched[len(matched)-historyLimitInput:]
		}
		if historyFormat != "" {
			values := make([][]any, len(matched))
			for i, item := range matched {
				values[i] = []any{item.index, item.query}
			}
			return writeRows(cmd.OutOrStdout(), []string{"index", "query"}, values, historyFormat)
		}
		for _, item := range matched {
			if _, err = fmt.Fprintf(cmd.OutOrStdout(), "%d  %s\n", item.index, item.query); err != nil {
				return err
			}
		}
		return nil
	}}
	historyCommand.Flags().StringVar(&historySearch, "search", "", "Filter history by case-insensitive text")
	historyCommand.Flags().IntVar(&historyLimitInput, "limit", 0, "Show only the most recent N matching entries (0 for all)")
	historyCommand.Flags().StringVar(&historyFormat, "format", "", "Structured output: table, csv, json or tsv")
	historyCommand.Flags().BoolVar(&clearHistory, "clear", false, "Clear saved history")
	historyCommand.MarkFlagsMutuallyExclusive("search", "clear")
	historyCommand.MarkFlagsMutuallyExclusive("limit", "clear")
	historyCommand.MarkFlagsMutuallyExclusive("format", "clear")
	_ = historyCommand.RegisterFlagCompletionFunc("format", fixed("table", "csv", "json", "tsv"))
	root.AddCommand(historyCommand)
	addMetadataCommands(root, store, names)
	addCheckCommand(root, store, names)
	addERDCommands(root, store, names)
	addTransferCommands(root, store, names)
	addExplainCommand(root, store, names)
	addSnippetCommand(root, store, names)
	addAuditCommand(root, store, names)
	return root
}

// One JSON string per line preserves multi-line SQL without changing literals.
func readHistory(s Store, name string) ([]string, error) {
	f, err := os.Open(historyPath(s, name))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if err := seekHistoryTail(f, s.HistoryLimit); err != nil {
		return nil, err
	}
	entries := []string{}
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 4096), 16*1024*1024)
	for scan.Scan() {
		q, err := decodeHistory(scan.Bytes())
		if err != nil {
			return nil, err
		}
		entries = append(entries, q)
		if s.HistoryLimit > 0 && len(entries) > s.HistoryLimit {
			copy(entries, entries[1:])
			entries = entries[:s.HistoryLimit]
		}
	}
	return retainHistory(entries, s.HistoryLimit), scan.Err()
}

func cleanInput(s string) string { return strings.TrimSpace(s) }

func normalizeEnv(raw string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	switch v {
	case "", "dev", "test", "staging", "prod":
		return v, nil
	default:
		return "", fmt.Errorf("env must be dev, test, staging or prod")
	}
}

func writeAtomicFile(path string, data []byte) error {
	return writeAtomicFileMode(path, data, 0o644)
}

func writeAtomicFileMode(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".dbh-out-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
