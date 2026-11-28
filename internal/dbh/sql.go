package dbh

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/benenen/dbh/internal/database"
	"github.com/benenen/dbh/internal/database/registry"
)

// splitSQL recognizes statement separators outside strings, comments and dollar quotes.
// The remainder is held by the interactive shell until a terminating semicolon arrives.
func splitSQL(input, driver string) (statements []string, remainder string, err error) {
	statements, remainder, _, err = splitSQLWithDelimiter(input, driver, ";")
	return
}

func splitSQLWithDelimiter(input, driver, initialDelimiter string) (statements []string, remainder string, finalDelimiter string, err error) {
	if initialDelimiter == "" {
		initialDelimiter = ";"
	}
	finalDelimiter = initialDelimiter
	backend, err := registry.Lookup(driver)
	if err != nil {
		return nil, "", finalDelimiter, err
	}
	syntax := backend.Syntax()
	if syntax.LineCommands {
		statements, remainder, err = splitLines(input)
		return statements, remainder, finalDelimiter, err
	}

	delimiter := initialDelimiter
	start := 0
	quote := byte(0)
	dollar := ""
	block := 0
	line := false

	// Track compound CREATE TRIGGER/PROCEDURE/FUNCTION ... BEGIN ... END blocks
	// so internal semicolons do not split the statement when delimiter == ";".
	var headerWords []string
	compoundHeader := false
	beginDepth := 0
	caseDepth := 0
	resetCompound := func() {
		headerWords = nil
		compoundHeader = false
		beginDepth = 0
		caseDepth = 0
	}

	for i := 0; i < len(input); i++ {
		c := input[i]
		if line {
			if c == '\n' {
				line = false
			}
			continue
		}
		if block > 0 {
			if syntax.NestedComments && i+1 < len(input) && input[i:i+2] == "/*" {
				block++
				i++
			} else if i+1 < len(input) && input[i:i+2] == "*/" {
				block--
				i++
			}
			continue
		}
		if dollar != "" {
			if strings.HasPrefix(input[i:], dollar) {
				i += len(dollar) - 1
				dollar = ""
			}
			continue
		}
		if quote != 0 {
			if c == '\\' && syntax.BackslashEscapes && quote != ']' && (quote != '`' || syntax.BacktickEscapes) {
				i++
				continue
			}
			// PostgreSQL E'...' strings support backslash escapes.
			if c == '\\' && quote == 'e' {
				i++
				continue
			}
			end := quote
			if quote == 'e' {
				end = '\''
			}
			if c == end {
				if i+1 < len(input) && input[i+1] == end {
					i++
				} else {
					quote = 0
				}
			}
			continue
		}
		if i+1 < len(input) && input[i:i+2] == "--" && (!syntax.DashCommentNeedsSpace || i+2 == len(input) || unicode.IsSpace(rune(input[i+2]))) {
			line = true
			i++
			continue
		}
		if i+1 < len(input) && input[i:i+2] == "//" && syntax.SlashComments {
			line = true
			i++
			continue
		}
		if c == '#' && syntax.HashComments {
			line = true
			continue
		}
		if i+1 < len(input) && input[i:i+2] == "/*" {
			block = 1
			i++
			continue
		}

		// Handle DELIMITER <token> directive at statement start.
		if stripCommentsSyntax(input[start:i], syntax) == "" {
			if newDelim, nextPos, ok := parseDelimiterDirective(input, i); ok {
				delimiter = newDelim
				finalDelimiter = newDelim
				i = nextPos
				start = nextPos + 1
				resetCompound()
				continue
			}
		}

		if c == '\'' || c == '"' || c == '`' {
			quote = c
			if c == '\'' && syntax.EscapeStringPrefix && i > 0 && (input[i-1] == 'E' || input[i-1] == 'e') && (i == 1 || !isIdentifier(rune(input[i-2]))) {
				quote = 'e'
			}
			continue
		}
		if c == '[' && syntax.BracketIdentifiers {
			quote = ']'
			continue
		}
		if c == '$' && syntax.DollarQuotes && (i == 0 || (!isIdentifier(rune(input[i-1])) && input[i-1] != '$')) {
			j := i + 1
			for j < len(input) && (unicode.IsLetter(rune(input[j])) || input[j] == '_' || (j > i+1 && unicode.IsDigit(rune(input[j])))) {
				j++
			}
			if j < len(input) && input[j] == '$' {
				dollar = input[i : j+1]
				i = j
				continue
			}
		}

		if delimiter == ";" && (isASCIIAlpha(c) || c == '_') && (i == 0 || !isIdentByte(input[i-1])) {
			j := i + 1
			for j < len(input) && isIdentByte(input[j]) {
				j++
			}
			word := strings.ToUpper(input[i:j])
			if len(headerWords) < 5 {
				headerWords = append(headerWords, word)
				compoundHeader = isCompoundHeader(headerWords)
			}
			if compoundHeader {
				switch word {
				case "BEGIN":
					if !isBeginTransactionFollow(input[j:]) {
						beginDepth++
					}
				case "CASE":
					if beginDepth > 0 {
						caseDepth++
					}
				case "END":
					if caseDepth > 0 {
						caseDepth--
					} else if beginDepth > 0 {
						beginDepth--
					}
				}
			}
			i = j - 1
			continue
		}

		if beginDepth == 0 && strings.HasPrefix(input[i:], delimiter) {
			text := strings.TrimSpace(input[start:i])
			if stripCommentsSyntax(text, syntax) != "" {
				statements = append(statements, text)
			}
			i += len(delimiter) - 1
			start = i + 1
			resetCompound()
		}
	}
	if start < len(input) {
		remainder = strings.TrimSpace(input[start:])
	}
	if quote != 0 || dollar != "" || block > 0 {
		err = fmt.Errorf("unterminated SQL string, identifier or comment")
	}
	return statements, remainder, finalDelimiter, err
}

func parseDelimiterDirective(input string, pos int) (delim string, endOfLine int, ok bool) {
	if pos > 0 && input[pos-1] != '\n' && input[pos-1] != '\r' {
		// Allow leading whitespace on the line.
		k := pos - 1
		for k >= 0 && (input[k] == ' ' || input[k] == '\t') {
			k--
		}
		if k >= 0 && input[k] != '\n' && input[k] != '\r' {
			return "", 0, false
		}
	}
	lineEnd := pos
	for lineEnd < len(input) && input[lineEnd] != '\n' {
		lineEnd++
	}
	line := strings.TrimSpace(input[pos:lineEnd])
	fields := strings.Fields(line)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "DELIMITER") {
		return "", 0, false
	}
	if lineEnd < len(input) && input[lineEnd] == '\n' {
		return fields[1], lineEnd, true
	}
	return fields[1], lineEnd - 1, true
}

func isCompoundHeader(words []string) bool {
	if len(words) < 2 || words[0] != "CREATE" {
		return false
	}
	idx := 1
	if idx < len(words) && (words[idx] == "TEMP" || words[idx] == "TEMPORARY") {
		idx++
	} else if idx+1 < len(words) && words[idx] == "OR" && words[idx+1] == "REPLACE" {
		idx += 2
	}
	if idx >= len(words) {
		return false
	}
	switch words[idx] {
	case "TRIGGER", "PROCEDURE", "FUNCTION":
		return true
	}
	return false
}

func isBeginTransactionFollow(rest string) bool {
	rest = strings.TrimLeft(rest, " \t\r\n")
	if rest == "" || rest[0] == ';' {
		return true
	}
	end := 0
	for end < len(rest) && isIdentByte(rest[end]) {
		end++
	}
	switch strings.ToUpper(rest[:end]) {
	case "TRANSACTION", "DEFERRED", "IMMEDIATE", "EXCLUSIVE", "WORK":
		return true
	}
	return false
}

func secondaryPrompt(input, driver string) string {
	backend, err := registry.Lookup(driver)
	if err != nil {
		return "...> "
	}
	syntax := backend.Syntax()
	if syntax.LineCommands {
		_, _, err := splitLines(input)
		if err != nil {
			return `"> `
		}
		return "...> "
	}
	quote := byte(0)
	dollar := ""
	block := 0
	line := false
	for i := 0; i < len(input); i++ {
		c := input[i]
		if line {
			if c == '\n' {
				line = false
			}
			continue
		}
		if block > 0 {
			if syntax.NestedComments && i+1 < len(input) && input[i:i+2] == "/*" {
				block++
				i++
			} else if i+1 < len(input) && input[i:i+2] == "*/" {
				block--
				i++
			}
			continue
		}
		if dollar != "" {
			if strings.HasPrefix(input[i:], dollar) {
				i += len(dollar) - 1
				dollar = ""
			}
			continue
		}
		if quote != 0 {
			if c == '\\' && syntax.BackslashEscapes && quote != ']' && (quote != '`' || syntax.BacktickEscapes) {
				i++
				continue
			}
			if c == '\\' && quote == 'e' {
				i++
				continue
			}
			end := quote
			if quote == 'e' {
				end = '\''
			}
			if c == end {
				if i+1 < len(input) && input[i+1] == end {
					i++
				} else {
					quote = 0
				}
			}
			continue
		}
		if i+1 < len(input) && input[i:i+2] == "--" && (!syntax.DashCommentNeedsSpace || i+2 == len(input) || unicode.IsSpace(rune(input[i+2]))) {
			line = true
			i++
			continue
		}
		if i+1 < len(input) && input[i:i+2] == "//" && syntax.SlashComments {
			line = true
			i++
			continue
		}
		if c == '#' && syntax.HashComments {
			line = true
			continue
		}
		if i+1 < len(input) && input[i:i+2] == "/*" {
			block = 1
			i++
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quote = c
			if c == '\'' && syntax.EscapeStringPrefix && i > 0 && (input[i-1] == 'E' || input[i-1] == 'e') && (i == 1 || !isIdentifier(rune(input[i-2]))) {
				quote = 'e'
			}
			continue
		}
		if c == '[' && syntax.BracketIdentifiers {
			quote = ']'
			continue
		}
		if c == '$' && syntax.DollarQuotes && (i == 0 || (!isIdentifier(rune(input[i-1])) && input[i-1] != '$')) {
			j := i + 1
			for j < len(input) && (unicode.IsLetter(rune(input[j])) || input[j] == '_' || (j > i+1 && unicode.IsDigit(rune(input[j])))) {
				j++
			}
			if j < len(input) && input[j] == '$' {
				dollar = input[i : j+1]
				i = j
				continue
			}
		}
	}
	if block > 0 {
		return "/*> "
	}
	if dollar != "" {
		return "$$> "
	}
	switch quote {
	case '\'', 'e':
		return "'> "
	case '"':
		return `"> `
	case '`':
		return "`> "
	case ']':
		return "]> "
	}
	return "...> "
}

// splitLines ends each command at a newline outside quotes; the last line is the remainder.
func splitLines(input string) (statements []string, remainder string, err error) {
	start := 0
	quote := byte(0)
	for i := 0; i < len(input); i++ {
		switch c := input[i]; {
		case quote != 0 && c == '\\':
			i++
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '\n':
			if text := strings.TrimSpace(input[start:i]); text != "" {
				statements = append(statements, text)
			}
			start = i + 1
		}
	}
	remainder = strings.TrimSpace(input[start:])
	if quote != 0 {
		err = fmt.Errorf("unterminated quoted string")
	}
	return
}

// terminate appends the history separator, moving it to a new line when the statement
// ends inside a line comment. Line commands need no separator.
func terminate(q, driver string) string {
	if backend, err := registry.Lookup(driver); err == nil && backend.Syntax().LineCommands {
		return q
	}
	if statements, rest, err := splitSQL(q+";", driver); err == nil && len(statements) == 1 && rest == "" {
		return q + ";"
	}
	return q + "\n;"
}

func stripComments(s, driver string) string {
	backend, err := registry.Lookup(driver)
	if err != nil {
		return s
	}
	return stripCommentsSyntax(s, backend.Syntax())
}

func stripCommentsSyntax(s string, syntax database.Syntax) string {
	s = strings.TrimSpace(s)
	for {
		if (strings.HasPrefix(s, "--") && (!syntax.DashCommentNeedsSpace || len(s) == 2 || unicode.IsSpace(rune(s[2])))) || (syntax.HashComments && strings.HasPrefix(s, "#")) || (syntax.SlashComments && strings.HasPrefix(s, "//")) {
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = strings.TrimSpace(s[i+1:])
				continue
			}
			return ""
		}
		if syntax.ExecutableComments && strings.HasPrefix(s, "/*!") {
			// MySQL executes this comment's contents, including versioned SQL.
			return s
		}
		if strings.HasPrefix(s, "/*") {
			depth, i := 1, 2
			for i+1 < len(s) && depth > 0 {
				switch s[i : i+2] {
				case "/*":
					if syntax.NestedComments {
						depth++
					}
					i += 2
				case "*/":
					depth--
					i += 2
				default:
					i++
				}
			}
			if depth != 0 {
				return s
			}
			s = strings.TrimSpace(s[i:])
			continue
		}
		return s
	}
}

func isIdentifier(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.'
}

// Recognize USE as a client-side operation without rewriting ordinary SQL.
func parseUseDatabase(query string, syntax database.Syntax) (name string, matched bool, err error) {
	query = stripCommentsSyntax(query, syntax)
	fields := strings.Fields(query)
	if len(fields) == 0 || !strings.EqualFold(fields[0], "USE") {
		return "", false, nil
	}
	name, err = parseDatabaseName(strings.TrimSpace(query[len(fields[0]):]), syntax)
	return name, true, err
}

func parseDatabaseName(input string, syntax database.Syntax) (string, error) {
	input = stripCommentsSyntax(input, syntax)
	if input == "" {
		return "", fmt.Errorf("database name is required")
	}
	name := ""
	rest := ""
	quoted := input[0] == '"' || input[0] == '`'
	if quoted {
		quote := input[0]
		var value strings.Builder
		closed := false
		for i := 1; i < len(input); i++ {
			if input[i] == quote {
				if i+1 < len(input) && input[i+1] == quote {
					value.WriteByte(quote)
					i++
					continue
				}
				name, rest, closed = value.String(), input[i+1:], true
				break
			}
			value.WriteByte(input[i])
		}
		if !closed {
			return "", fmt.Errorf("unterminated database identifier")
		}
	} else {
		end := len(input)
		for i, char := range input {
			if unicode.IsSpace(char) || char == ';' {
				end = i
				break
			}
		}
		name, rest = input[:end], input[end:]
		for _, char := range name {
			if !unicode.IsLetter(char) && !unicode.IsDigit(char) && !strings.ContainsRune("_$-.", char) {
				return "", fmt.Errorf("invalid database name; use a quoted identifier")
			}
		}
		if syntax.FoldUnquotedIdentifiers {
			name = strings.ToLower(name)
		}
	}
	rest = strings.TrimSpace(rest)
	rest = strings.TrimPrefix(rest, ";")
	if name == "" || stripCommentsSyntax(rest, syntax) != "" {
		return "", fmt.Errorf("expected one database name")
	}
	return name, nil
}
