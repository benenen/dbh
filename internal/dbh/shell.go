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

	"github.com/benenen/dbh/internal/database/registry"

	"github.com/reeflective/readline"
	"github.com/reeflective/readline/inputrc"
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

type completer struct{ words, databases []string }

func (c *completer) candidates(line []rune, pos int) ([]string, string) {
	if pos < 0 || pos > len(line) {
		return nil, ""
	}
	if name, ok := useArgument(line[:pos]); ok {
		// \use takes only a database name, so offer every database before typing.
		return matching(c.databases, name), name
	}
	start := pos
	for start > 0 && (isIdentifier(line[start-1]) || line[start-1] == '\\') {
		start--
	}
	prefix := string(line[start:pos])
	if prefix == "" {
		return nil, ""
	}
	words := c.words
	if !strings.HasPrefix(prefix, `\`) {
		words = nil
		for _, word := range c.words {
			if !strings.HasPrefix(word, `\`) {
				words = append(words, word)
			}
		}
	}
	return matching(words, prefix), prefix
}

func matching(words []string, text string) []string {
	results := []string{}
	match := strings.ToLower(text)
	for _, word := range words {
		if strings.Contains(strings.ToLower(word), match) {
			results = append(results, word)
		}
	}
	return results
}

// useArgument returns the database name typed after \use on the current line.
func useArgument(line []rune) (string, bool) {
	text := string(line)
	text = strings.TrimLeft(text[strings.LastIndex(text, "\n")+1:], " \t")
	rest, ok := strings.CutPrefix(text, `\use`)
	if !ok || rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		return "", false
	}
	name := strings.TrimLeft(rest, " \t")
	if strings.ContainsAny(name, " \t") {
		return "", false
	}
	return name, true
}

func (c *completer) complete(line []rune, pos int) readline.Completions {
	words, prefix := c.candidates(line, pos)
	comps := readline.CompleteValues(words...).PreserveEscapes().NoFilter()
	comps.PREFIX = prefix
	return comps
}

// The editor automatically saves each accepted input line. SQL history must instead
// contain complete statements, so Write is a no-op and the shell appends after parsing.
type sqlHistory struct{ entries []string }

func (h *sqlHistory) Write(string) (int, error) { return len(h.entries), nil }
func (h *sqlHistory) GetLine(pos int) (string, error) {
	if pos < 0 || pos >= len(h.entries) {
		return "", fmt.Errorf("history position out of range")
	}
	return h.entries[pos], nil
}
func (h *sqlHistory) Len() int  { return len(h.entries) }
func (h *sqlHistory) Dump() any { return h.entries }

var keywords = strings.Fields(`SELECT FROM WHERE INSERT INTO VALUES UPDATE SET DELETE CREATE TABLE ALTER DROP JOIN LEFT RIGHT INNER OUTER ON AS AND OR NOT NULL IS IN EXISTS LIKE ILIKE BETWEEN GROUP BY ORDER HAVING LIMIT OFFSET DISTINCT UNION ALL WITH RETURNING EXPLAIN BEGIN COMMIT ROLLBACK CASE WHEN THEN ELSE END COUNT SUM AVG MIN MAX TRUE FALSE PRIMARY KEY REFERENCES INDEX SHOW DESCRIBE PRAGMA`)

func (c *completer) refresh(ctx context.Context, s *Session) error {
	words := append([]string{}, keywords...)
	if native := s.backend.Syntax().CompletionWords; len(native) > 0 {
		words = append([]string{}, native...)
	}
	words = append(words, `\help`, `\q`, `\database`, `\use`, `\tables`, `\describe`, `\indexes`, `\history`, `\refresh`, `\clear`, `\format`)
	// SQLite has no database list; \use then offers no candidates.
	c.databases, _ = s.Databases(ctx)
	tables, err := s.Tables(ctx)
	if err != nil {
		c.words = words
		return err
	}
	// One query per table is slow on large schemas over high-latency links.
	columns, batched, err := s.ColumnNames(ctx)
	batched = batched && err == nil
	words = append(words, columns...)
	for _, table := range tables {
		words = append(words, table)
		if p := strings.SplitN(table, ".", 2); len(p) == 2 {
			words = append(words, p[1])
		}
		if batched {
			continue
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

const shellHelp = `SQL ends with ; and may span multiple lines. Suggestions appear as you type.
Paste keeps the entire block editable; press Enter to execute complete SQL.
Alt-Enter inserts a newline. Up/Down move within multi-line input.
Tab or Down (suggestions on the last line) selects keywords/tables/columns; arrows
move in the menu, Enter confirms the candidate, Shift-Tab selects the previous.
Up/Down recall SQL; Ctrl-R searches history; Right accepts a history suggestion.
Ctrl-C clears input; Ctrl-D exits.
\help               Show help
\q                  Exit
\database           List databases (MySQL/PostgreSQL/MongoDB)
\use DATABASE       Switch database (also accepts USE DATABASE;)
\tables             List tables/views
\describe TABLE     Show column details and indexes
\indexes TABLE      List indexes
\history            Show SQL history
\refresh            Refresh schema completion (use after DDL)
\clear              Clear pending query
\format table|csv|json  Change output format
`

func acceptSQLInput(line, driver string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || (strings.HasPrefix(trimmed, `\`) && !strings.Contains(trimmed, "\n")) {
		return true
	}
	if backend, err := registry.Lookup(driver); err == nil && backend.Syntax().JSONCommands && json.Valid([]byte(trimmed)) {
		return true
	}
	statements, rest, err := splitSQL(line, driver)
	return err == nil && len(statements) > 0 && stripComments(rest, driver) == ""
}

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
	history := &sqlHistory{entries: entries}
	rl := readline.NewShell()
	rl.Config.Vars["enable-bracketed-paste"] = true
	rl.Config.Vars["multiline-column"] = true
	rl.Config.Vars["autocomplete"] = true
	rl.Config.Vars["completion-ignore-case"] = true
	rl.Config.Vars["history-autosuggest"] = !noHistory
	rl.Config.Vars["cursor-position-probe"] = false
	rl.Keymap.Register(map[string]func(){
		"dbh-newline": func() {
			rl.Line().Insert(rl.Cursor().Pos(), '\n')
			rl.Cursor().Inc()
		},
		// Enter in the menu confirms the candidate; a second Enter accepts the line.
		"dbh-accept-candidate": rl.AcceptCompletion,
		// Down selects visible candidates on the last line; elsewhere it keeps
		// moving within multi-line input.
		"dbh-down": func() {
			line, pos := *rl.Line(), rl.Cursor().Pos()
			if rl.CompletionsVisible() && !strings.ContainsRune(string(line[min(pos, len(line)):]), '\n') {
				rl.Keymap.Commands()["menu-complete"]()
				return
			}
			rl.Keymap.Commands()["down-line-or-history"]()
		},
		"dbh-interrupt": func() {
			// Drop virtual completion/search buffers before accepting the interrupt.
			rl.Keymap.Commands()["abort"]()
			rl.Display.AcceptLine()
			rl.History.Accept(false, false, readline.ErrInterrupt)
		},
	})
	for _, keymap := range []string{"emacs", "menu-select", "isearch"} {
		if err := rl.Config.Bind(keymap, "\x03", "dbh-interrupt", false); err != nil {
			return err
		}
	}
	// The default "complete" command hides the menu after Tab; menu-complete
	// keeps as-you-type candidates visible for subsequent SQL words.
	if err := rl.Config.Bind("emacs", "\t", "menu-complete", false); err != nil {
		return err
	}
	if err := rl.Config.Bind("emacs", "\x1b[Z", "menu-complete-backward", false); err != nil {
		return err
	}
	if err := rl.Config.Bind("menu-select", "\r", "dbh-accept-candidate", false); err != nil {
		return err
	}
	for _, keymap := range []string{"emacs", "menu-select"} {
		// Both forms normalize to ESC+Enter. Override the meta form too,
		// otherwise the default self-insert binding takes precedence.
		for _, key := range []string{"\x1b\r", string(inputrc.Enmeta('\r'))} {
			if err := rl.Config.Bind(keymap, key, "dbh-newline", false); err != nil {
				return err
			}
		}
	}
	for key, action := range map[string]string{
		inputrc.Unescape(`\M-[A`): "up-line-or-history",
		inputrc.Unescape(`\M-[B`): "dbh-down",
	} {
		if err := rl.Config.Bind("emacs", key, action, false); err != nil {
			return err
		}
	}
	rl.History.Add("SQL history", history)
	resume := ""
	rl.AcceptMultiline = func(line []rune) bool {
		text := string(line)
		start := strings.LastIndex(text, "\n") + 1
		last := strings.TrimSpace(text[start:])
		if strings.HasPrefix(last, `\`) {
			// Commands can inspect the session while preserving unfinished SQL.
			// Do not interpret a command inside a literal or block comment.
			if _, _, err := splitSQL(text[:start], s.Driver); err == nil {
				resume = text[:start]
				if last == `\clear` || last == `\q` || last == `\quit` {
					resume = ""
				}
				rl.Line().Set([]rune(last)...)
				return true
			}
		}
		return acceptSQLInput(text, s.Driver)
	}
	color := os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	rl.Prompt.Primary(func() string {
		if color {
			return "\x1b[36m" + p.Name + "> \x1b[0m"
		}
		return p.Name + "> "
	})
	rl.Prompt.Secondary(func() string {
		if color {
			return "\x1b[90m...> \x1b[0m"
		}
		return "...> "
	})
	rl.Completer = func(line []rune, pos int) readline.Completions {
		if pos < 0 || pos > len(line) {
			return readline.Completions{}
		}
		// Do not suggest SQL words inside an unfinished literal or block comment.
		if _, _, err := splitSQL(string(line[:pos]), s.Driver); err != nil && !s.backend.Syntax().JSONCommands {
			return readline.Completions{}
		}
		return c.complete(line, pos)
	}
	_, _ = fmt.Fprintln(out, "Connected to", p.Name, "("+p.Driver+"). Type \\help for help.")
	for {
		line, err := rl.Readline()
		if errors.Is(err, readline.ErrInterrupt) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if strings.TrimSpace(line) != "" {
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
				help := shellHelp
				if s.backend.Syntax().JSONCommands {
					help = "MongoDB accepts native JSON command documents. Press Enter when complete.\nUse ; between commands in batch input.\n" + shellHelp[strings.Index(shellHelp, `\help`):]
				}
				_, _ = fmt.Fprint(out, help)
			case `\clear`:
			case `\use`:
				name, err := parseDatabaseName(strings.TrimSpace(line[len(fields[0]):]), s.backend.Syntax())
				if err != nil {
					_, _ = fmt.Fprintln(errOut, `Usage: \use DATABASE`)
					break
				}
				if err := s.UseDatabase(ctx, name); err != nil {
					_, _ = fmt.Fprintln(errOut, err)
					break
				}
				_, _ = fmt.Fprintln(out, "Database changed to", name)
				if err := c.refresh(ctx, s); err != nil {
					_, _ = fmt.Fprintln(errOut, "Schema completion unavailable:", err)
				}
			case `\refresh`:
				if err := c.refresh(ctx, s); err != nil {
					_, _ = fmt.Fprintln(errOut, err)
				}
			case `\database`, `\tables`:
				var names []string
				var err error
				if fields[0] == `\database` {
					if len(fields) != 1 {
						_, _ = fmt.Fprintln(errOut, `Usage: \database`)
						break
					}
					names, err = s.Databases(ctx)
				} else {
					names, err = s.Tables(ctx)
				}
				if err != nil {
					_, _ = fmt.Fprintln(errOut, err)
				} else {
					for _, n := range names {
						_, _ = fmt.Fprintln(out, n)
					}
				}
			case `\describe`, `\indexes`:
				if len(fields) != 2 {
					_, _ = fmt.Fprintf(errOut, "Usage: %s TABLE\n", fields[0])
					break
				}
				var err error
				if fields[0] == `\describe` {
					err = s.Describe(ctx, fields[1], format, out)
				} else {
					err = s.Indexes(ctx, fields[1], format, out)
				}
				if err != nil {
					_, _ = fmt.Fprintln(errOut, err)
				}
			case `\history`:
				for i, q := range history.entries {
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
			if resume != "" {
				rl.Line().Set([]rune(resume)...)
				rl.History.Accept(true, false, nil)
				resume = ""
			}
			continue
		}
		if line == "" {
			continue
		}
		statements, rest, _ := splitSQL(line, s.Driver)
		if s.backend.Syntax().JSONCommands && strings.TrimSpace(rest) != "" {
			statements = append(statements, rest)
		}
		for _, q := range statements {
			if !noHistory {
				entry := terminate(q, s.Driver)
				if err := saveHistory(store, p.Name, entry); err != nil {
					_, _ = fmt.Fprintln(errOut, "History:", err)
				}
				history.entries = append(history.entries, entry)
			}
			if err := s.Execute(ctx, q, format, out); err != nil {
				label := "SQL error:"
				if s.backend.Syntax().JSONCommands {
					label = "Command error:"
				}
				_, _ = fmt.Fprintln(errOut, label, err)
				break
			}
			if _, use, _ := parseUseDatabase(q, s.backend.Syntax()); use {
				if err := c.refresh(ctx, s); err != nil {
					_, _ = fmt.Fprintln(errOut, "Schema completion unavailable:", err)
				}
			}
		}
	}
}
