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
	backend, err := registry.Lookup(driver)
	if err != nil {
		return nil, "", err
	}
	syntax := backend.Syntax()
	start := 0
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
			if i+1 < len(input) && input[i:i+2] == "/*" {
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
			if c == '\\' && syntax.BackslashEscapes && quote != ']' {
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
		if c == ';' {
			text := strings.TrimSpace(input[start:i])
			if stripCommentsSyntax(text, syntax) != "" {
				statements = append(statements, text)
			}
			start = i + 1
		}
	}
	remainder = strings.TrimSpace(input[start:])
	if quote != 0 || dollar != "" || block > 0 {
		err = fmt.Errorf("unterminated SQL string, identifier or comment")
	}
	return
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
		if (strings.HasPrefix(s, "--") && (!syntax.DashCommentNeedsSpace || len(s) == 2 || unicode.IsSpace(rune(s[2])))) || (syntax.HashComments && strings.HasPrefix(s, "#")) {
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = strings.TrimSpace(s[i+1:])
				continue
			}
			return ""
		}
		if strings.HasPrefix(s, "/*") {
			depth, i := 1, 2
			for i+1 < len(s) && depth > 0 {
				switch s[i : i+2] {
				case "/*":
					depth++
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
			if !unicode.IsLetter(char) && !unicode.IsDigit(char) && !strings.ContainsRune("_$-", char) {
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
