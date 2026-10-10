package dbh

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/benenen/dbh/internal/database/proxy"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func NewCommand() *cobra.Command {
	var dir string
	root := &cobra.Command{Use: "dbh", Short: "Manage database connections and run queries", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().StringVar(&dir, "config-dir", "", "Configuration directory (or DBH_CONFIG_DIR)")
	store := func() (Store, error) {
		d := dir
		if d == "" {
			var err error
			d, err = defaultDir()
			if err != nil {
				return Store{}, err
			}
		}
		return Store{Dir: d}, nil
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
	ls := &cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "List saved connections (credentials are hidden)", Args: cobra.NoArgs, ValidArgsFunction: cobra.NoFileCompletions, RunE: func(cmd *cobra.Command, args []string) error {
		s, err := store()
		if err != nil {
			return err
		}
		profiles, err := s.List()
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		if _, err := fmt.Fprintln(w, "NAME\tDRIVER\tPROXIES"); err != nil {
			return err
		}
		for _, p := range profiles {
			hops := make([]string, len(p.Proxies))
			for i, hop := range p.Proxies {
				hops[i] = proxy.Display(hop)
			}
			if len(hops) == 0 {
				hops = []string{"-"}
			}
			if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n", p.Name, p.Driver, strings.Join(hops, " -> ")); err != nil {
				return err
			}
		}
		return w.Flush()
	}}
	root.AddCommand(ls)
	for _, create := range []bool{true, false} {
		var driver, dsn, dsnEnv string
		var proxies []string
		var prompt, noProxy bool
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
		c.Flags().StringVar(&driver, "driver", "", "sqlite, postgres, mysql, mongo or clickhouse")
		c.Flags().StringVar(&dsn, "dsn", "", "Driver DSN (stored locally)")
		c.Flags().StringVar(&dsnEnv, "dsn-env", "", "Read DSN from this environment variable")
		c.Flags().BoolVar(&prompt, "prompt-dsn", false, "Read DSN with terminal echo disabled")
		c.Flags().StringArrayVar(&proxies, "proxy", nil, "Proxy hop socks5://[user:pass@]host:port or ssh://[user[:pass]@]host[:port]; repeat in dial order")
		c.MarkFlagsMutuallyExclusive("dsn", "dsn-env", "prompt-dsn")
		_ = c.RegisterFlagCompletionFunc("driver", fixed("sqlite", "postgres", "mysql", "mongo", "clickhouse"))
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
			c.MarkFlagsMutuallyExclusive("proxy", "no-proxy")
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
			}
			if cmd.Flags().Changed("dsn-env") {
				value, ok := os.LookupEnv(dsnEnv)
				if !ok {
					return fmt.Errorf("environment variable %q is not set", dsnEnv)
				}
				p.DSN = value
			}
			if cmd.Flags().Changed("proxy") {
				p.Proxies = proxies
			}
			if noProxy {
				p.Proxies = nil
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
			}
			if !create && !cmd.Flags().Changed("driver") && !cmd.Flags().Changed("dsn") && !cmd.Flags().Changed("dsn-env") && !prompt && !cmd.Flags().Changed("proxy") && !noProxy {
				return fmt.Errorf("provide --driver, --dsn, --dsn-env, --prompt-dsn, --proxy or --no-proxy")
			}
			if err = s.Put(p, create); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Saved %s (%s)\n", p.Name, p.Driver)
			return err
		}
		root.AddCommand(c)
	}
	root.AddCommand(&cobra.Command{Use: "remove NAME", Aliases: []string{"rm", "r"}, Short: "Remove a saved connection (keeps the database)", Args: cobra.ExactArgs(1), ValidArgsFunction: names, RunE: func(cmd *cobra.Command, args []string) error {
		s, err := store()
		if err != nil {
			return err
		}
		if err = s.Remove(args[0]); err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "Removed", args[0])
		return err
	}})
	for _, executeOnly := range []bool{false, true} {
		var query, file, format, databaseName string
		var timeout time.Duration
		var noHistory bool
		connect := &cobra.Command{Use: "connect NAME", Aliases: []string{"c"}, Short: "Open a database shell, or execute queries from --sql, --file or stdin", Args: cobra.ExactArgs(1), ValidArgsFunction: names, RunE: func(cmd *cobra.Command, args []string) error {
			if executeOnly && len(args) == 2 {
				if cmd.Flags().Changed("sql") || cmd.Flags().Changed("file") {
					return fmt.Errorf("positional query cannot be combined with --sql or --file")
				}
				query = args[1]
			}
			if cmd.Flags().Changed("db") && strings.TrimSpace(databaseName) == "" {
				return fmt.Errorf("database name must not be empty")
			}
			if format != "table" && format != "csv" && format != "json" {
				return fmt.Errorf("format must be table, csv or json")
			}
			if timeout <= 0 {
				return fmt.Errorf("timeout must be positive")
			}
			s, err := store()
			if err != nil {
				return err
			}
			p, err := s.Get(args[0])
			if err != nil {
				return err
			}
			var input string
			batch := false
			switch {
			case cmd.Flags().Changed("sql") || executeOnly && len(args) == 2:
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
				for _, q := range statements {
					if !noHistory {
						if err := saveHistory(s, p.Name, terminate(q, p.Driver)); err != nil {
							return err
						}
					}
					if err := session.Execute(cmd.Context(), q, format, cmd.OutOrStdout()); err != nil {
						return err
					}
				}
				return nil
			}
			return shell(cmd.Context(), session, p, s, format, noHistory, cmd.OutOrStdout(), cmd.ErrOrStderr())
		}}
		if executeOnly {
			connect.Use = "exec NAME [QUERY]"
			connect.Aliases = nil
			connect.Short = "Execute queries using a saved connection and optional database"
			connect.Args = cobra.RangeArgs(1, 2)
			connect.Flags().StringVar(&databaseName, "db", "", "Server database to use (defaults to the saved connection database)")
		}
		connect.Flags().StringVarP(&query, "sql", "e", "", "Execute SQL or a MongoDB JSON command and exit")
		connect.Flags().StringVarP(&file, "file", "f", "", "Execute a query file (- for stdin)")
		connect.Flags().StringVar(&format, "format", "table", "table, csv or json (newline-delimited objects)")
		connect.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "Connection/query timeout")
		connect.Flags().BoolVar(&noHistory, "no-history", false, "Disable persisted query history")
		connect.MarkFlagsMutuallyExclusive("sql", "file")
		_ = connect.RegisterFlagCompletionFunc("file", files)
		_ = connect.RegisterFlagCompletionFunc("format", fixed("table", "csv", "json"))
		for _, flag := range []string{"sql", "timeout", "db"} {
			if connect.Flags().Lookup(flag) != nil {
				_ = connect.RegisterFlagCompletionFunc(flag, cobra.NoFileCompletions)
			}
		}
		root.AddCommand(connect)
	}
	root.AddCommand(&cobra.Command{Use: "history NAME", Short: "Show SQL history for a connection", Args: cobra.ExactArgs(1), ValidArgsFunction: names, RunE: func(cmd *cobra.Command, args []string) error {
		s, err := store()
		if err != nil {
			return err
		}
		if _, err = s.Get(args[0]); err != nil {
			return err
		}
		entries, err := readHistory(s, args[0])
		if err != nil {
			return err
		}
		for i, q := range entries {
			if _, err = fmt.Fprintf(cmd.OutOrStdout(), "%d  %s\n", i+1, q); err != nil {
				return err
			}
		}
		return nil
	}})
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
	entries := []string{}
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 4096), 16*1024*1024)
	for scan.Scan() {
		q, err := decodeHistory(scan.Bytes())
		if err != nil {
			return nil, err
		}
		entries = append(entries, q)
	}
	return entries, scan.Err()
}

func cleanInput(s string) string { return strings.TrimSpace(s) }
