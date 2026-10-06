package dbh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chzyer/readline"
)

func historyPath(s Store, name string) string { return filepath.Join(s.Dir, "history", name+".jsonl") }
func decodeHistory(b []byte) (string, error) {
	var q string
	err := json.Unmarshal(b, &q)
	return q, err
}
func saveHistory(s Store, name, q string) error {
	dir := filepath.Dir(historyPath(s, name))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(historyPath(s, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return json.NewEncoder(f).Encode(q)
}

type completer struct{ words []string }

func (c *completer) Do(line []rune, pos int) ([][]rune, int) {
	if pos < 0 || pos > len(line) {
		return nil, 0
	}
	start := pos
	for start > 0 && (isIdentifier(line[start-1]) || line[start-1] == '\\') {
		start--
	}
	prefix := strings.ToLower(string(line[start:pos]))
	results := [][]rune{}
	for _, word := range c.words {
		r := []rune(word)
		if strings.HasPrefix(strings.ToLower(word), prefix) && len(r) >= pos-start {
			results = append(results, append(r[pos-start:], ' '))
		}
	}
	return results, pos - start
}

var keywords = strings.Fields(`SELECT FROM WHERE INSERT INTO VALUES UPDATE SET DELETE CREATE TABLE ALTER DROP JOIN LEFT RIGHT INNER OUTER ON AS AND OR NOT NULL IS IN EXISTS LIKE ILIKE BETWEEN GROUP BY ORDER HAVING LIMIT OFFSET DISTINCT UNION ALL WITH RETURNING EXPLAIN BEGIN COMMIT ROLLBACK CASE WHEN THEN ELSE END COUNT SUM AVG MIN MAX TRUE FALSE PRIMARY KEY REFERENCES INDEX SHOW DESCRIBE PRAGMA`)

func (c *completer) refresh(ctx context.Context, s *Session) error {
	words := append([]string{}, keywords...)
	words = append(words, `\help`, `\q`, `\tables`, `\describe`, `\history`, `\refresh`, `\clear`, `\format`)
	tables, err := s.Tables(ctx)
	if err != nil {
		c.words = words
		return err
	}
	for _, table := range tables {
		words = append(words, table)
		if p := strings.SplitN(table, ".", 2); len(p) == 2 {
			words = append(words, p[1])
		}
		cols, err := s.Columns(ctx, table)
		if err != nil {
			continue
		}
		words = append(words, cols...)
	}
	sort.Strings(words)
	c.words = nil
	for _, w := range words {
		if len(c.words) == 0 || c.words[len(c.words)-1] != w {
			c.words = append(c.words, w)
		}
	}
	return nil
}

const shellHelp = `SQL ends with ; and may span multiple lines. Tab completes keywords/tables/columns.
Up/Down recall SQL; Ctrl-R searches history; Ctrl-C clears input; Ctrl-D exits.
\help               Show help
\q                  Exit
\tables             List tables/views
\describe TABLE     List columns
\history            Show SQL history
\refresh            Refresh schema completion (use after DDL)
\clear              Clear pending SQL
\format table|csv|json  Change output format
`

func shell(ctx context.Context, s *Session, p Profile, store Store, format string, noHistory bool, out, errOut io.Writer) error {
	c := &completer{}
	if err := c.refresh(ctx, s); err != nil {
		_, _ = fmt.Fprintln(errOut, "Schema completion unavailable:", err)
	}
	entries := []string{}
	if !noHistory {
		if err := store.prepare(); err != nil {
			return err
		}
		var err error
		entries, err = readHistory(store, p.Name)
		if err != nil {
			return err
		}
	}
	rl, err := readline.NewEx(&readline.Config{Prompt: p.Name + "> ", AutoComplete: c, HistoryLimit: 1000, DisableAutoSaveHistory: true, InterruptPrompt: "^C", EOFPrompt: "exit", Stdout: out, Stderr: errOut})
	if err != nil {
		return err
	}
	defer func() { _ = rl.Close() }()
	// readline's on-disk format is line-based, so we manage multi-line history ourselves.
	for _, q := range entries {
		if err := rl.SaveHistory(q); err != nil {
			return err
		}
	}
	_, _ = fmt.Fprintln(out, "Connected to", p.Name, "("+p.Driver+"). Type \\help for help.")
	pending := ""
	for {
		if pending == "" {
			rl.SetPrompt(p.Name + "> ")
		} else {
			rl.SetPrompt("...> ")
		}
		line, err := rl.Readline()
		if errors.Is(err, readline.ErrInterrupt) {
			pending = ""
			continue
		}
		if errors.Is(err, io.EOF) {
			if pending != "" {
				_, _ = fmt.Fprintln(errOut, "Discarded unfinished SQL.")
			}
			return nil
		}
		if err != nil {
			return err
		}
		line = cleanInput(line)
		if strings.HasPrefix(line, `\`) {
			fields := strings.Fields(line)
			switch fields[0] {
			case `\q`, `\quit`:
				return nil
			case `\help`:
				_, _ = fmt.Fprint(out, shellHelp)
			case `\clear`:
				pending = ""
			case `\refresh`:
				if err := c.refresh(ctx, s); err != nil {
					_, _ = fmt.Fprintln(errOut, err)
				}
			case `\tables`:
				names, err := s.Tables(ctx)
				if err != nil {
					_, _ = fmt.Fprintln(errOut, err)
				} else {
					for _, n := range names {
						_, _ = fmt.Fprintln(out, n)
					}
				}
			case `\describe`:
				if len(fields) != 2 {
					_, _ = fmt.Fprintln(errOut, `Usage: \describe TABLE`)
					continue
				}
				names, err := s.Columns(ctx, fields[1])
				if err != nil {
					_, _ = fmt.Fprintln(errOut, err)
				} else {
					for _, n := range names {
						_, _ = fmt.Fprintln(out, n)
					}
				}
			case `\history`:
				for i, q := range entries {
					_, _ = fmt.Fprintf(out, "%d  %s\n", i+1, q)
				}
			case `\format`:
				if len(fields) != 2 || (fields[1] != "table" && fields[1] != "csv" && fields[1] != "json") {
					_, _ = fmt.Fprintln(errOut, `Usage: \format table|csv|json`)
				} else {
					format = fields[1]
				}
			default:
				_, _ = fmt.Fprintln(errOut, "Unknown command. Type \\help.")
			}
			continue
		}
		if line == "" && pending == "" {
			continue
		}
		if pending != "" {
			pending += "\n"
		}
		pending += line
		statements, rest, parseErr := splitSQL(pending, s.Driver)
		pending = rest
		for _, q := range statements {
			if !noHistory {
				if err := saveHistory(store, p.Name, q+";"); err != nil {
					_, _ = fmt.Fprintln(errOut, "History:", err)
				}
				entries = append(entries, q+";")
				_ = rl.SaveHistory(q + ";")
			}
			if err := s.Execute(ctx, q, format, out); err != nil {
				_, _ = fmt.Fprintln(errOut, "SQL error:", err)
				break
			}
		}
		if parseErr == nil && stripComments(pending, s.Driver) == "" {
			pending = ""
		}
	}
}
