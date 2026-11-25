package dbh

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/gofrs/flock"
	"github.com/spf13/cobra"
)

type Snippet struct {
	Name        string `json:"name"`
	Query       string `json:"query"`
	Description string `json:"description,omitempty"`
}

func (s Store) loadSnippets() (map[string]Snippet, error) {
	snippets := map[string]Snippet{}
	data, err := os.ReadFile(filepath.Join(s.Dir, "snippets.json"))
	if errors.Is(err, os.ErrNotExist) {
		return snippets, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &snippets); err != nil {
		return nil, fmt.Errorf("invalid snippets.json: %w", err)
	}
	return snippets, nil
}

func (s Store) updateSnippets(fn func(map[string]Snippet) error) error {
	if err := s.prepare(); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(s.Dir, ".snippets.lock"))
	if err := lock.Lock(); err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()
	m, err := s.loadSnippets()
	if err != nil {
		return err
	}
	if err := fn(m); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomicFileMode(filepath.Join(s.Dir, "snippets.json"), append(data, '\n'), 0o600)
}

func addSnippetCommand(root *cobra.Command, store func() (Store, error), names cobra.CompletionFunc) {
	cmd := &cobra.Command{
		Use:   "snippet",
		Short: "Save and reuse named SQL/command snippets",
	}

	var setSQL, setFile, setDesc string
	setCmd := &cobra.Command{
		Use:   "set NAME [QUERY]",
		Short: "Create or update a named snippet",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !validName.MatchString(name) {
				return fmt.Errorf("name must contain letters, digits, dots, underscores or hyphens and start with a letter or digit")
			}
			q := setSQL
			if len(args) == 2 {
				if cmd.Flags().Changed("sql") || cmd.Flags().Changed("file") {
					return fmt.Errorf("positional query cannot be combined with --sql or --file")
				}
				q = args[1]
			} else if cmd.Flags().Changed("file") {
				b, err := os.ReadFile(setFile)
				if err != nil {
					return err
				}
				q = string(b)
			}
			q = strings.TrimSpace(q)
			if q == "" {
				return fmt.Errorf("snippet query cannot be empty")
			}
			st, err := store()
			if err != nil {
				return err
			}
			if err := st.updateSnippets(func(m map[string]Snippet) error {
				m[name] = Snippet{Name: name, Query: q, Description: setDesc}
				return nil
			}); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Saved snippet %s\n", name)
			return err
		},
	}
	setCmd.Flags().StringVarP(&setSQL, "sql", "e", "", "Snippet query text")
	setCmd.Flags().StringVarP(&setFile, "file", "f", "", "Read snippet query from file")
	setCmd.Flags().StringVar(&setDesc, "description", "", "Short description")
	setCmd.MarkFlagsMutuallyExclusive("sql", "file")

	var listFormat string
	listCmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List saved snippets",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if listFormat != "" && !validFormat(listFormat) {
				return fmt.Errorf("format must be table, csv, json or tsv")
			}
			st, err := store()
			if err != nil {
				return err
			}
			m, err := st.loadSnippets()
			if err != nil {
				return err
			}
			items := make([]Snippet, 0, len(m))
			for _, sn := range m {
				items = append(items, sn)
			}
			sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
			if listFormat != "" {
				vals := make([][]any, len(items))
				for i, sn := range items {
					vals[i] = []any{sn.Name, sn.Description, sn.Query}
				}
				return writeRows(cmd.OutOrStdout(), []string{"name", "description", "query"}, vals, listFormat)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "NAME\tDESCRIPTION\tQUERY"); err != nil {
				return err
			}
			for _, sn := range items {
				q := strings.Join(strings.Fields(sn.Query), " ")
				if len(q) > 60 {
					q = q[:57] + "..."
				}
				if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n", sn.Name, sn.Description, q); err != nil {
					return err
				}
			}
			return w.Flush()
		},
	}
	listCmd.Flags().StringVar(&listFormat, "format", "", "Structured output: table, csv, json or tsv")

	showCmd := &cobra.Command{
		Use:   "show NAME",
		Short: "Print a saved snippet",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := store()
			if err != nil {
				return err
			}
			m, err := st.loadSnippets()
			if err != nil {
				return err
			}
			sn, ok := m[args[0]]
			if !ok {
				return fmt.Errorf("snippet %q does not exist", args[0])
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), sn.Query)
			return err
		},
	}

	rmCmd := &cobra.Command{
		Use:     "remove NAME",
		Aliases: []string{"rm"},
		Short:   "Remove a saved snippet",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := store()
			if err != nil {
				return err
			}
			if err := st.updateSnippets(func(m map[string]Snippet) error {
				if _, ok := m[args[0]]; !ok {
					return fmt.Errorf("snippet %q does not exist", args[0])
				}
				delete(m, args[0])
				return nil
			}); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Removed snippet", args[0])
			return err
		},
	}

	var runDB, runFormat string
	var runParams []string
	var runTimeout time.Duration
	var runMaxRows, runMaxCell int
	var runStats, runAllowWrite, runExpanded bool
	runCmd := &cobra.Command{
		Use:   "run NAME CONNECTION",
		Short: "Execute a saved snippet against a connection",
		Args:  cobra.ExactArgs(2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
			if len(args) == 1 {
				return names(cmd, nil, toComplete)
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if !validFormat(runFormat) {
				return fmt.Errorf("format must be table, csv, json or tsv")
			}
			if runTimeout <= 0 {
				return fmt.Errorf("timeout must be positive")
			}
			st, err := store()
			if err != nil {
				return err
			}
			m, err := st.loadSnippets()
			if err != nil {
				return err
			}
			sn, ok := m[args[0]]
			if !ok {
				return fmt.Errorf("snippet %q does not exist", args[0])
			}
			p, err := st.Get(args[1])
			if err != nil {
				return err
			}
			bound, err := parseBoundParams(runParams)
			if err != nil {
				return err
			}
			session, err := openSession(cmd.Context(), p, runTimeout)
			if err != nil {
				return err
			}
			defer session.Close()
			session.MaxRows = runMaxRows
			session.MaxCellWidth = runMaxCell
			session.MaxCellWidthSet = cmd.Flags().Changed("max-cell-width")
			session.Stats = runStats
			session.AllowWrite = runAllowWrite
			session.Expanded = runExpanded
			session.Notice = cmd.ErrOrStderr()
			if cmd.Flags().Changed("db") {
				if err := session.UseDatabase(cmd.Context(), runDB); err != nil {
					return err
				}
			}
			if len(bound) > 0 {
				return session.executeArgs(cmd.Context(), sn.Query, runFormat, cmd.OutOrStdout(), bound...)
			}
			return session.Run(cmd.Context(), sn.Query, runFormat, cmd.OutOrStdout())
		},
	}
	runCmd.Flags().StringVar(&runDB, "db", "", "Server database to use")
	runCmd.Flags().StringArrayVar(&runParams, "param", nil, "Bind JSON scalar parameter; repeat in order")
	runCmd.Flags().StringVar(&runFormat, "format", "table", "table, csv, json or tsv")
	runCmd.Flags().DurationVar(&runTimeout, "timeout", 30*time.Second, "Query timeout")
	runCmd.Flags().IntVar(&runMaxRows, "max-rows", 0, "Maximum result rows (0 for unlimited)")
	runCmd.Flags().IntVar(&runMaxCell, "max-cell-width", 0, "Maximum cell display width")
	runCmd.Flags().BoolVar(&runStats, "stats", false, "Write query timing and row counts to stderr")
	runCmd.Flags().BoolVar(&runAllowWrite, "allow-write", false, "Allow write statements on read-only/prod connections")
	runCmd.Flags().BoolVarP(&runExpanded, "expanded", "x", false, "Display table rows vertically")

	cmd.AddCommand(setCmd, listCmd, showCmd, rmCmd, runCmd)
	root.AddCommand(cmd)
}

func parseBoundParams(params []string) ([]any, error) {
	bound := make([]any, len(params))
	for i, value := range params {
		decoder := json.NewDecoder(strings.NewReader(value))
		decoder.UseNumber()
		var v any
		if err := decoder.Decode(&v); err != nil {
			return nil, fmt.Errorf("parameter %d must be a JSON scalar: %w", i+1, err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("parameter %d must contain one JSON scalar", i+1)
		}
		switch n := v.(type) {
		case json.Number:
			if integer, err := n.Int64(); err == nil {
				v = integer
			} else if number, err := n.Float64(); err == nil {
				v = number
			} else {
				return nil, err
			}
		case nil, string, bool:
		default:
			return nil, fmt.Errorf("parameter %d must be a JSON scalar", i+1)
		}
		bound[i] = v
	}
	return bound, nil
}
