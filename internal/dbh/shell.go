package dbh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/benenen/dbh/internal/database"
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
	return updateHistory(s, name, func(entries []string) []string {
		return retainHistory(append(entries, q), s.HistoryLimit)
	})
}

type completer struct {
	words, databases, tables, snippets []string
	columns                            map[string][]string
	syntax                             database.Syntax
}

func (c *completer) refreshSnippets(store Store) {
	snippets, err := store.loadSnippets()
	if err != nil {
		c.snippets = nil
		return
	}
	names := make([]string, 0, len(snippets))
	for name := range snippets {
		names = append(names, name)
	}
	sort.Strings(names)
	c.snippets = names
}

func (c *completer) candidates(line []rune, pos int) ([]string, string) {
	if pos < 0 || pos > len(line) {
		return nil, ""
	}
	if command, name, ok := commandArgument(line[:pos]); ok {
		// List all valid values as soon as the command argument starts.
		names := c.tables
		switch command {
		case `\use`, `\database`:
			names = c.databases
		case `\format`:
			names = []string{"table", "csv", "json", "tsv"}
		case `\pager`:
			names = []string{"auto", "on", "off"}
		case `\truncate`:
			names = []string{"off", "0", "80", "200", "500"}
		case `\limit`:
			names = []string{"off", "0", "100", "500", "1000", "5000"}
		case `\timeout`:
			names = []string{"5s", "15s", "30s", "1m", "5m"}
		case `\run`:
			names = c.snippets
		}
		return matching(names, name), name
	}
	// A shell command with no matching argument completer must not offer SQL words.
	currentLine := string(line[:pos])
	currentLine = strings.TrimLeft(currentLine[strings.LastIndex(currentLine, "\n")+1:], " \t")
	if strings.HasPrefix(currentLine, `\`) && strings.ContainsAny(currentLine, " \t") {
		return nil, ""
	}
	start := pos
	for start > 0 && (isIdentifier(line[start-1]) || line[start-1] == '\\' || ((c.syntax.HTTPCommands || c.syntax.LineCommands) && (line[start-1] == '/' || line[start-1] == '-'))) {
		start--
	}
	prefix := string(line[start:pos])
	if !c.syntax.JSONCommands && !c.syntax.LineCommands && !c.syntax.HTTPCommands && !strings.HasPrefix(prefix, `\`) {
		before := contextTokens(string(line[:start]), c.syntax)
		if tableContext(before) {
			matches := matching(c.tables, prefix)
			for i := range matches {
				matches[i] = completionIdentifier(matches[i], c.syntax)
			}
			return matches, prefix
		}
		if dot := strings.LastIndex(prefix, "."); dot >= 0 {
			qualifier, fragment := prefix[:dot], prefix[dot+1:]
			depth := 0
			for _, t := range before {
				if t.text == "(" {
					depth = t.depth + 1
				} else {
					depth = t.depth
				}
			}
			cols := c.aliasColumns(string(line), qualifier, depth)
			matches := matching(cols, fragment)
			for i := range matches {
				matches[i] = qualifier + "." + completionIdentifier(matches[i], c.syntax)
			}
			return matches, prefix
		}
	}
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
	var contains []string
	match := strings.ToLower(text)
	for _, word := range words {
		lower := strings.ToLower(word)
		if strings.HasPrefix(lower, match) {
			results = append(results, word)
		} else if strings.Contains(lower, match) {
			contains = append(contains, word)
		}
	}
	return append(results, contains...)
}

func matchFilter(value, pattern string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return true
	}
	lowerValue := strings.ToLower(value)
	lowerPattern := strings.ToLower(pattern)
	if strings.ContainsAny(lowerPattern, "*?") {
		return wildcardMatch(lowerValue, lowerPattern)
	}
	return strings.Contains(lowerValue, lowerPattern)
}

// wildcardMatch matches s against pattern where '*' matches zero or more runes
// (including '/', '.', and newlines) and '?' matches any single rune.
func wildcardMatch(s, pattern string) bool {
	sr := []rune(s)
	pr := []rune(pattern)
	si, pi := 0, 0
	star, match := -1, 0
	for si < len(sr) {
		if pi < len(pr) && (pr[pi] == '?' || pr[pi] == sr[si]) {
			si++
			pi++
		} else if pi < len(pr) && pr[pi] == '*' {
			star = pi
			match = si
			pi++
		} else if star != -1 {
			pi = star + 1
			match++
			si = match
		} else {
			return false
		}
	}
	for pi < len(pr) && pr[pi] == '*' {
		pi++
	}
	return pi == len(pr)
}

func filterList(items []string, pattern string) []string {
	if strings.TrimSpace(pattern) == "" {
		return items
	}
	var out []string
	for _, item := range items {
		if matchFilter(item, pattern) {
			out = append(out, item)
		}
	}
	return out
}

// commandArgument returns the first argument of a supported shell command on
// the current line.
func commandArgument(line []rune) (command, name string, ok bool) {
	text := string(line)
	text = strings.TrimLeft(text[strings.LastIndex(text, "\n")+1:], " \t")
	for _, command := range []string{`\use`, `\database`, `\tables`, `\describe`, `\indexes`, `\format`, `\pager`, `\truncate`, `\limit`, `\timeout`, `\run`} {
		rest, found := strings.CutPrefix(text, command)
		if !found || rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
			continue
		}
		name := strings.TrimLeft(rest, " \t")
		if strings.ContainsAny(name, " \t") {
			return "", "", false
		}
		return command, name, true
	}
	return "", "", false
}

func unique(words []string) []string {
	sort.Strings(words)
	var result []string
	for _, w := range words {
		if len(result) == 0 || result[len(result)-1] != w {
			result = append(result, w)
		}
	}
	return result
}

func (c *completer) complete(line []rune, pos int) readline.Completions {
	words, prefix := c.candidates(line, pos)
	comps := readline.CompleteValues(words...).PreserveEscapes().NoFilter().NoSort()
	comps.PREFIX = prefix
	return comps
}

// completionAllowed uses the SQL scanner to suppress suggestions in literals and
// comments. A synthetic semicolon is consumed only outside a line comment.
func completionAllowed(input, driver string) bool {
	backend, err := registry.Lookup(driver)
	if err != nil {
		return false
	}
	if backend.Syntax().JSONCommands || backend.Syntax().HTTPCommands {
		return true
	}
	if backend.Syntax().LineCommands {
		_, _, err := splitSQL(input, driver)
		return err == nil
	}
	_, rest, err := splitSQL(input+";", driver)
	return err == nil && rest == ""
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
	c.syntax = s.backend.Syntax()
	c.columns = make(map[string][]string)
	words := append([]string{}, keywords...)
	if native := s.backend.Syntax().CompletionWords; len(native) > 0 {
		words = append([]string{}, native...)
	}
	words = append(words, `\help`, `\q`, `\database`, `\use`, `\tables`, `\describe`, `\indexes`, `\history`, `\refresh`, `\clear`, `\format`, `\pager`, `\truncate`, `\limit`, `\timeout`, `\reconnect`, `\o`, `\out`, `\i`, `\source`, `\e`, `\edit`, `\watch`, `\explain`, `\x`, `\run`)
	// SQLite has no database list; \use then offers no candidates.
	c.databases, _ = s.Databases(ctx)
	tables, err := s.Tables(ctx)
	if err != nil {
		c.words, c.tables = words, nil
		return err
	}
	var names []string
	// One query per table is slow on large schemas over high-latency links.
	var columns []string
	batched := false
	if backend, ok := s.backend.(database.TableColumns); ok {
		schemaCtx, cancel := context.WithTimeout(ctx, s.Timeout)
		schema, err := backend.TableColumns(schemaCtx, s.Conn)
		cancel()
		if err == nil {
			c.columns, batched = schema, true
			for _, cols := range schema {
				columns = append(columns, cols...)
			}
		}
	} else {
		var err error
		columns, batched, err = s.ColumnNames(ctx)
		batched = batched && err == nil
	}
	words = append(words, columns...)
	for _, table := range tables {
		names = append(names, table)
		if p := strings.SplitN(table, ".", 2); len(p) == 2 {
			names = append(names, p[1])
		}
		if batched {
			continue
		}
		cols, err := s.Columns(ctx, table)
		if err != nil {
			continue
		}
		c.columns[table] = cols
		words = append(words, cols...)
	}
	c.tables = unique(names)
	c.words = unique(append(words, names...))
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
\database [PATTERN] List databases, schemas, buckets, or keyspaces
\use DATABASE       Switch database (also accepts USE DATABASE;)
\tables [PATTERN]   List tables/views
\describe TABLE     Show column details and indexes
\indexes TABLE      List indexes
\history [PATTERN]  Show SQL history
\refresh            Refresh schema completion (use after DDL)
\clear              Clear pending query
\format table|csv|json|tsv  Change output format
\pager auto|on|off  Scroll wide/tall table results in an interactive pager
\truncate [N|off]   Limit cell display width (table defaults to 200)
\limit [N|off]      Limit rows returned per query (defaults to 1000; 0/off for unlimited)
\timeout [DUR]      Show or set query timeout (e.g. 30s, 5m)
\reconnect          Reconnect the current session and restore database
\o [FILE]           Redirect query output to FILE (no arg restores stdout)
\i FILE             Execute SQL/commands from FILE in current session
\e                  Edit pending or last query in $EDITOR
\watch [SEC]        Repeat last query every SEC seconds (default 2s; Ctrl-C stops)
\explain [analyze] SQL  Show query execution plan
\x                  Toggle expanded vertical table display
\run SNIPPET        Execute a saved snippet
`

const defaultShellMaxRows = 1000

func acceptSQLInput(line, driver string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || (strings.HasPrefix(trimmed, `\`) && !strings.Contains(trimmed, "\n")) {
		return true
	}
	if backend, err := registry.Lookup(driver); err == nil {
		if backend.Syntax().JSONCommands && json.Valid([]byte(trimmed)) {
			return true
		}
		if backend.Syntax().HTTPCommands && acceptHTTPInput(trimmed) {
			return true
		}
	}
	statements, rest, err := splitSQL(line, driver)
	if backend, lookupErr := registry.Lookup(driver); lookupErr == nil && backend.Syntax().LineCommands {
		// Enter runs every complete line, unless a quoted argument is still open.
		return err == nil
	}
	return err == nil && len(statements) > 0 && stripComments(rest, driver) == ""
}

func acceptHTTPInput(trimmed string) bool {
	if strings.HasSuffix(trimmed, ";") {
		return true
	}
	fields := strings.Fields(trimmed)
	if len(fields) < 2 {
		return false
	}
	switch strings.ToUpper(fields[0]) {
	case "GET", "POST", "PUT", "DELETE", "HEAD", "PATCH":
	default:
		return false
	}
	rest := strings.TrimSpace(trimmed[len(fields[0]):])
	// Find end of path token.
	idx := strings.IndexAny(rest, " \t\r\n")
	if idx < 0 {
		return true
	}
	body := strings.TrimSpace(rest[idx:])
	return body == "" || json.Valid([]byte(body))
}

func shell(ctx context.Context, s *Session, p Profile, store Store, format string, noHistory bool, out, errOut io.Writer) error {
	s.Stats = true
	s.Notice = errOut
	c := &completer{}
	if err := c.refresh(ctx, s); err != nil {
		_, _ = fmt.Fprintln(errOut, "Schema completion unavailable:", err)
	}
	c.refreshSnippets(store)
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
	var lastQuery string
	var outFile *os.File
	var outFilePath string
	defer func() {
		if outFile != nil {
			_ = outFile.Close()
		}
	}()
	activeOut := func() io.Writer {
		if outFile != nil {
			return outFile
		}
		return out
	}
	pagerMode := "auto"
	updatePager := func() {
		if pagerMode == "off" {
			s.Pager = nil
			return
		}
		s.Pager = func(columns []string, rows [][]string, elapsed time.Duration, truncated bool) (bool, error) {
			if !shouldUsePager(pagerMode, out, columns, rows) {
				return false, nil
			}
			if err := runTablePager(columns, rows, elapsed, truncated); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	updatePager()
	pendingSearchLine := ""
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
		"dbh-fuzzy-history": func() {
			pendingSearchLine = string(*rl.Line())
			rl.Keymap.Commands()["abort"]()
			rl.Display.AcceptLine()
			rl.History.Accept(false, false, errFuzzyHistory)
		},
	})
	for _, keymap := range []string{"emacs", "menu-select", "isearch"} {
		if err := rl.Config.Bind(keymap, "\x03", "dbh-interrupt", false); err != nil {
			return err
		}
	}
	for _, keymap := range []string{"emacs", "menu-select"} {
		if err := rl.Config.Bind(keymap, "\x12", "dbh-fuzzy-history", false); err != nil {
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
		label := p.Name
		if s.CurrentDatabase != "" {
			label += "/" + s.CurrentDatabase
		}
		var badges string
		if p.ReadOnly {
			badges += "[RO]"
		}
		if strings.EqualFold(p.Env, "prod") {
			badges += "[PROD]"
		}
		if badges != "" {
			label += " " + badges
		}
		if color {
			if strings.EqualFold(p.Env, "prod") {
				return "\x1b[31m" + label + "> \x1b[0m"
			}
			return "\x1b[36m" + label + "> \x1b[0m"
		}
		return label + "> "
	})
	rl.Prompt.Secondary(func() string {
		promptText := secondaryPrompt(string(*rl.Line()), s.Driver)
		if color {
			return "\x1b[90m" + promptText + "\x1b[0m"
		}
		return promptText
	})
	rl.Completer = func(line []rune, pos int) readline.Completions {
		if pos < 0 || pos > len(line) {
			return readline.Completions{}
		}
		if !completionAllowed(string(line[:pos]), s.Driver) {
			return readline.Completions{}
		}
		return c.complete(line, pos)
	}
	_, _ = fmt.Fprintln(out, "Connected to", p.Name, "("+p.Driver+"). Type \\help for help.")
	if s.MaxRows > 0 {
		_, _ = fmt.Fprintf(out, "Row limit: %d (use \\limit off for unlimited).\n", s.MaxRows)
	}
	for {
		line, err := rl.Readline()
		if errors.Is(err, errFuzzyHistory) {
			selected, ok, deleted, pickErr := pickHistory(history.entries, pendingSearchLine)
			if pickErr != nil {
				_, _ = fmt.Fprintln(errOut, "History search:", pickErr)
			}
			if len(deleted) > 0 {
				delSet := make(map[string]bool, len(deleted))
				for _, d := range deleted {
					delSet[d] = true
				}
				history.entries = removeHistoryEntries(history.entries, delSet)
				if !noHistory {
					if histErr := updateHistory(store, p.Name, func(entries []string) []string {
						return removeHistoryEntries(entries, delSet)
					}); histErr != nil {
						_, _ = fmt.Fprintln(errOut, "History:", histErr)
					}
				}
			}
			restore := pendingSearchLine
			if ok {
				restore = selected
			}
			pendingSearchLine = ""
			if restore != "" {
				rl.Line().Set([]rune(restore)...)
				rl.History.Accept(true, false, nil)
			}
			continue
		}
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
				if s.backend.Syntax().HelpText != "" {
					help = s.backend.Syntax().HelpText + shellHelp[strings.Index(shellHelp, `\help`):]
				}
				_, _ = fmt.Fprint(out, help)
			case `\clear`:
			case `\use`:
				name, err := parseDatabaseName(strings.TrimSpace(line[len(fields[0]):]), s.backend.Syntax())
				if err != nil {
					_, _ = fmt.Fprintln(errOut, `Usage: \use DATABASE`)
					break
				}
				stopSpinner := startSpinner(errOut, "Switching database")
				useErr, interrupted := runInterruptible(ctx, func(qCtx context.Context) error {
					if err := s.UseDatabase(qCtx, name); err != nil {
						return err
					}
					return nil
				})
				stopSpinner()
				if interrupted {
					_, _ = fmt.Fprintln(errOut, "Canceled.")
					break
				}
				if useErr != nil {
					_, _ = fmt.Fprintln(errOut, useErr)
					break
				}
				_, _ = fmt.Fprintln(out, "Database changed to", name)
				stopRefresh := startSpinner(errOut, "Refreshing schema")
				refErr, refInterrupted := runInterruptible(ctx, func(qCtx context.Context) error {
					return c.refresh(qCtx, s)
				})
				stopRefresh()
				if refInterrupted {
					_, _ = fmt.Fprintln(errOut, "Schema refresh canceled.")
				} else if refErr != nil {
					_, _ = fmt.Fprintln(errOut, "Schema completion unavailable:", refErr)
				}
			case `\refresh`:
				stopSpinner := startSpinner(errOut, "Refreshing schema")
				refErr, interrupted := runInterruptible(ctx, func(qCtx context.Context) error {
					return c.refresh(qCtx, s)
				})
				stopSpinner()
				if interrupted {
					_, _ = fmt.Fprintln(errOut, "Canceled.")
					break
				}
				if refErr != nil {
					_, _ = fmt.Fprintln(errOut, refErr)
				}
				c.refreshSnippets(store)
			case `\database`, `\tables`:
				if len(fields) > 2 {
					_, _ = fmt.Fprintf(errOut, "Usage: %s [PATTERN]\n", fields[0])
					break
				}
				pattern := ""
				if len(fields) == 2 {
					pattern = fields[1]
				}
				var names []string
				stopSpinner := startSpinner(errOut, "Loading metadata")
				metaErr, interrupted := runInterruptible(ctx, func(qCtx context.Context) error {
					var err error
					if fields[0] == `\database` {
						names, err = s.Databases(qCtx)
					} else {
						names, err = s.Tables(qCtx)
					}
					return err
				})
				stopSpinner()
				if interrupted {
					_, _ = fmt.Fprintln(errOut, "Canceled.")
					break
				}
				if metaErr != nil {
					_, _ = fmt.Fprintln(errOut, metaErr)
				} else {
					for _, n := range filterList(names, pattern) {
						_, _ = fmt.Fprintln(out, n)
					}
				}
			case `\describe`, `\indexes`:
				table := strings.TrimSpace(line[len(fields[0]):])
				if table == "" {
					_, _ = fmt.Fprintf(errOut, "Usage: %s TABLE\n", fields[0])
					break
				}
				stopSpinner := startSpinner(errOut, "Loading metadata")
				s.BeforeOutput = stopSpinner
				metaErr, interrupted := runInterruptible(ctx, func(qCtx context.Context) error {
					if fields[0] == `\describe` {
						return s.Describe(qCtx, table, format, out)
					}
					return s.Indexes(qCtx, table, format, out)
				})
				stopSpinner()
				s.BeforeOutput = nil
				if interrupted {
					_, _ = fmt.Fprintln(errOut, "Canceled.")
				} else if metaErr != nil {
					_, _ = fmt.Fprintln(errOut, metaErr)
				}
			case `\history`:
				pattern := strings.TrimSpace(line[len(fields[0]):])
				for i, q := range history.entries {
					if matchFilter(q, pattern) {
						_, _ = fmt.Fprintf(out, "%d  %s\n", i+1, q)
					}
				}
			case `\format`:
				if len(fields) != 2 || !validFormat(fields[1]) {
					_, _ = fmt.Fprintln(errOut, `Usage: \format table|csv|json|tsv`)
				} else {
					format = fields[1]
				}
			case `\pager`:
				if len(fields) == 1 {
					_, _ = fmt.Fprintln(out, "Pager:", pagerMode)
				} else if len(fields) != 2 || !validPagerMode(fields[1]) {
					_, _ = fmt.Fprintln(errOut, `Usage: \pager auto|on|off`)
				} else {
					pagerMode = fields[1]
					updatePager()
				}
			case `\truncate`:
				if len(fields) == 1 {
					w := s.effectiveMaxCellWidth(format)
					if w == 0 {
						_, _ = fmt.Fprintln(out, "Truncate: off")
					} else {
						_, _ = fmt.Fprintf(out, "Truncate: %d\n", w)
					}
				} else if len(fields) != 2 {
					_, _ = fmt.Fprintln(errOut, `Usage: \truncate [N|off]`)
				} else if fields[1] == "off" || fields[1] == "0" {
					s.MaxCellWidth = 0
					s.MaxCellWidthSet = true
				} else if n, convErr := strconv.Atoi(fields[1]); convErr != nil || n < 0 {
					_, _ = fmt.Fprintln(errOut, `Usage: \truncate [N|off]`)
				} else {
					s.MaxCellWidth = n
					s.MaxCellWidthSet = true
				}
			case `\limit`:
				if len(fields) == 1 {
					if s.MaxRows <= 0 {
						_, _ = fmt.Fprintln(out, "Limit: off")
					} else {
						_, _ = fmt.Fprintf(out, "Limit: %d\n", s.MaxRows)
					}
				} else if len(fields) != 2 {
					_, _ = fmt.Fprintln(errOut, `Usage: \limit [N|off]`)
				} else if fields[1] == "off" || fields[1] == "0" {
					s.MaxRows = 0
				} else if n, convErr := strconv.Atoi(fields[1]); convErr != nil || n < 0 {
					_, _ = fmt.Fprintln(errOut, `Usage: \limit [N|off]`)
				} else {
					s.MaxRows = n
				}
			case `\x`:
				s.Expanded = !s.Expanded
				if s.Expanded {
					_, _ = fmt.Fprintln(out, "Expanded display is on.")
				} else {
					_, _ = fmt.Fprintln(out, "Expanded display is off.")
				}
			case `\run`:
				if len(fields) != 2 {
					_, _ = fmt.Fprintln(errOut, `Usage: \run SNIPPET`)
					break
				}
				snippets, snErr := store.loadSnippets()
				if snErr != nil {
					_, _ = fmt.Fprintln(errOut, snErr)
					break
				}
				sn, ok := snippets[fields[1]]
				if !ok {
					_, _ = fmt.Fprintf(errOut, "snippet %q does not exist\n", fields[1])
					break
				}
				stopSpinner := startSpinner(errOut, "Running query")
				s.BeforeOutput = stopSpinner
				execErr, interrupted := runInterruptible(ctx, func(qCtx context.Context) error {
					if execErr := s.Run(qCtx, sn.Query, format, activeOut()); execErr != nil {
						return execErr
					}
					return nil
				})
				stopSpinner()
				s.BeforeOutput = nil
				if interrupted {
					_, _ = fmt.Fprintln(errOut, "Query canceled.")
					break
				}
				if execErr != nil {
					label := "SQL error:"
					if syntax := s.backend.Syntax(); syntax.JSONCommands || syntax.LineCommands || syntax.HTTPCommands {
						label = "Command error:"
					}
					_, _ = fmt.Fprintln(errOut, label, execErr)
				}
			case `\reconnect`:
				stopSpinner := startSpinner(errOut, "Reconnecting")
				recErr, interrupted := runInterruptible(ctx, func(qCtx context.Context) error {
					return s.Reconnect(qCtx, p)
				})
				stopSpinner()
				if interrupted {
					_, _ = fmt.Fprintln(errOut, "Canceled.")
					break
				}
				if recErr != nil {
					_, _ = fmt.Fprintln(errOut, recErr)
					break
				}
				_, _ = fmt.Fprintln(out, "Reconnected to", p.Name)
				if err := c.refresh(ctx, s); err != nil {
					_, _ = fmt.Fprintln(errOut, "Schema completion unavailable:", err)
				}
			case `\timeout`:
				if len(fields) == 1 {
					_, _ = fmt.Fprintln(out, "Timeout:", s.Timeout)
				} else if len(fields) != 2 {
					_, _ = fmt.Fprintln(errOut, `Usage: \timeout [DURATION]`)
				} else if d, dErr := time.ParseDuration(fields[1]); dErr != nil || d <= 0 {
					_, _ = fmt.Fprintln(errOut, `Usage: \timeout [DURATION] (e.g. 30s, 5m)`)
				} else {
					s.Timeout = d
					_, _ = fmt.Fprintln(out, "Timeout:", s.Timeout)
				}
			case `\o`, `\out`:
				if len(fields) == 1 {
					if outFile != nil {
						_ = outFile.Close()
						outFile = nil
						_, _ = fmt.Fprintln(out, "Output restored to terminal (closed", outFilePath+")")
						outFilePath = ""
					} else {
						_, _ = fmt.Fprintln(out, "Output: terminal")
					}
				} else {
					targetPath := strings.TrimSpace(line[len(fields[0]):])
					f, openErr := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
					if openErr != nil {
						_, _ = fmt.Fprintln(errOut, openErr)
						break
					}
					if outFile != nil {
						_ = outFile.Close()
					}
					outFile = f
					outFilePath = targetPath
					_, _ = fmt.Fprintln(out, "Output redirected to", outFilePath)
				}
			case `\i`, `\source`:
				srcPath := strings.TrimSpace(line[len(fields[0]):])
				if srcPath == "" {
					_, _ = fmt.Fprintf(errOut, "Usage: %s FILE\n", fields[0])
					break
				}
				raw, readErr := os.ReadFile(srcPath)
				if readErr != nil {
					_, _ = fmt.Fprintln(errOut, readErr)
					break
				}
				stmts, rem, splitErr := splitSQL(string(raw), s.Driver)
				if splitErr != nil {
					_, _ = fmt.Fprintln(errOut, splitErr)
					break
				}
				if strings.TrimSpace(rem) != "" {
					stmts = append(stmts, rem)
				}
				for _, stmt := range stmts {
					stopSpinner := startSpinner(errOut, "Running query")
					s.BeforeOutput = stopSpinner
					execErr, interrupted := runInterruptible(ctx, func(qCtx context.Context) error {
						return s.Execute(qCtx, stmt, format, activeOut())
					})
					stopSpinner()
					s.BeforeOutput = nil
					if interrupted {
						_, _ = fmt.Fprintln(errOut, "Query canceled.")
						break
					}
					if execErr != nil {
						_, _ = fmt.Fprintln(errOut, "SQL error:", execErr)
						break
					}
				}
			case `\e`, `\edit`:
				seed := strings.TrimSpace(resume)
				if seed == "" {
					seed = lastQuery
				}
				edited, edErr := openExternalEditor(seed)
				if edErr != nil {
					_, _ = fmt.Fprintln(errOut, "Editor:", edErr)
					break
				}
				resume = edited
			case `\watch`:
				if strings.TrimSpace(lastQuery) == "" {
					_, _ = fmt.Fprintln(errOut, "No previous query to watch.")
					break
				}
				interval := 2 * time.Second
				if len(fields) == 2 {
					if d, dErr := time.ParseDuration(fields[1]); dErr == nil && d > 0 {
						interval = d
					} else if sec, sErr := strconv.ParseFloat(fields[1], 64); sErr == nil && sec > 0 {
						interval = time.Duration(sec * float64(time.Second))
					} else {
						_, _ = fmt.Fprintln(errOut, `Usage: \watch [SECONDS|DURATION]`)
						break
					}
				} else if len(fields) > 2 {
					_, _ = fmt.Fprintln(errOut, `Usage: \watch [SECONDS|DURATION]`)
					break
				}
				runWatch(ctx, s, lastQuery, format, interval, activeOut(), errOut)
			case `\explain`:
				rest := strings.TrimSpace(line[len(fields[0]):])
				analyze := false
				if len(fields) >= 2 && strings.EqualFold(fields[1], "analyze") {
					analyze = true
					rest = strings.TrimSpace(rest[len(fields[1]):])
				}
				rest = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), ";"))
				if rest == "" {
					_, _ = fmt.Fprintln(errOut, `Usage: \explain [analyze] QUERY`)
					break
				}
				if analyze && s.ReadOnly && !s.AllowWrite && isWriteStatement(rest, s.backend.Syntax()) {
					_, _ = fmt.Fprintln(errOut, "EXPLAIN ANALYZE on a write statement is blocked on read-only connections")
					break
				}
				explainQueries, expErr := buildExplainQueries(s.Driver, rest, analyze)
				if expErr != nil {
					_, _ = fmt.Fprintln(errOut, expErr)
					break
				}
				for _, eq := range explainQueries {
					stopSpinner := startSpinner(errOut, "Running explain")
					s.BeforeOutput = stopSpinner
					execErr, interrupted := runInterruptible(ctx, func(qCtx context.Context) error {
						return s.Execute(qCtx, eq, format, activeOut())
					})
					stopSpinner()
					s.BeforeOutput = nil
					if interrupted {
						_, _ = fmt.Fprintln(errOut, "Query canceled.")
						break
					}
					if execErr != nil {
						_, _ = fmt.Fprintln(errOut, "SQL error:", execErr)
						break
					}
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
		if syntax := s.backend.Syntax(); (syntax.JSONCommands || syntax.LineCommands || syntax.HTTPCommands) && strings.TrimSpace(rest) != "" {
			statements = append(statements, rest)
		}
		for _, q := range statements {
			if !noHistory {
				entry := terminate(q, s.Driver)
				if err := saveHistory(store, p.Name, entry); err != nil {
					_, _ = fmt.Fprintln(errOut, "History:", err)
				}
				history.entries = retainHistory(append(history.entries, entry), store.HistoryLimit)
			}
			stopSpinner := startSpinner(errOut, "Running query")
			s.BeforeOutput = stopSpinner
			prevDB := s.CurrentDatabase
			lastQuery = q
			execErr, interrupted := runInterruptible(ctx, func(qCtx context.Context) error {
				return s.Execute(qCtx, q, format, activeOut())
			})
			stopSpinner()
			s.BeforeOutput = nil
			if interrupted {
				_, _ = fmt.Fprintln(errOut, "Query canceled.")
				break
			}
			if execErr != nil {
				label := "SQL error:"
				if syntax := s.backend.Syntax(); syntax.JSONCommands || syntax.LineCommands || syntax.HTTPCommands {
					label = "Command error:"
				}
				_, _ = fmt.Fprintln(errOut, label, execErr)
				break
			}
			if _, use, _ := parseUseDatabase(q, s.backend.Syntax()); use || s.CurrentDatabase != prevDB {
				if err := c.refresh(ctx, s); err != nil {
					_, _ = fmt.Fprintln(errOut, "Schema completion unavailable:", err)
				}
			}
		}
	}
}

func runInterruptible(ctx context.Context, fn func(context.Context) error) (error, bool) {
	qCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	err := fn(qCtx)
	if qCtx.Err() != nil && ctx.Err() == nil {
		return err, true
	}
	return err, false
}

func removeHistoryEntries(entries []string, delSet map[string]bool) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !delSet[e] {
			out = append(out, e)
		}
	}
	return out
}

func openExternalEditor(seed string) (string, error) {
	editor := strings.TrimSpace(os.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if editor == "" {
		editor = "vi"
	}
	tmp, err := os.CreateTemp("", "dbh-edit-*.sql")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if seed != "" {
		if _, err := tmp.WriteString(seed + "\n"); err != nil {
			_ = tmp.Close()
			return "", err
		}
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	parts := strings.Fields(editor)
	cmd := exec.Command(parts[0], append(parts[1:], tmpPath)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	edited, err := os.ReadFile(tmpPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(edited)), nil
}

func runWatch(ctx context.Context, s *Session, query, format string, interval time.Duration, out, errOut io.Writer) {
	watchCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	origPager := s.Pager
	s.Pager = nil
	defer func() { s.Pager = origPager }()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		_, _ = fmt.Fprintf(out, "\x1b[H\x1b[2JEvery %s: %s (%s) — press Ctrl-C to stop\n\n",
			interval, summarizeHistoryLine(query, 60), time.Now().Format("15:04:05"))
		if err := s.Execute(watchCtx, query, format, out); err != nil {
			if watchCtx.Err() != nil {
				_, _ = fmt.Fprintln(errOut, "\nWatch stopped.")
				return
			}
			_, _ = fmt.Fprintln(errOut, "Error:", err)
		}
		select {
		case <-watchCtx.Done():
			_, _ = fmt.Fprintln(errOut, "\nWatch stopped.")
			return
		case <-ticker.C:
		}
	}
}
