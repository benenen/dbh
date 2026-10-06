package dbh

import (
	"fmt"
	"strings"
	"unicode"
)

// splitSQL recognizes statement separators outside strings, comments and dollar quotes.
// The remainder is held by the interactive shell until a terminating semicolon arrives.
func splitSQL(input, driver string) (statements []string, remainder string, err error) {
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
			if c == '\\' && driver == "mysql" && quote != ']' {
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
		if i+1 < len(input) && input[i:i+2] == "--" && (driver != "mysql" || i+2 == len(input) || unicode.IsSpace(rune(input[i+2]))) {
			line = true
			i++
			continue
		}
		if c == '#' && driver == "mysql" {
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
			if c == '\'' && driver == "postgres" && i > 0 && (input[i-1] == 'E' || input[i-1] == 'e') && (i == 1 || !isIdentifier(rune(input[i-2]))) {
				quote = 'e'
			}
			continue
		}
		if c == '[' && driver == "sqlite" {
			quote = ']'
			continue
		}
		if c == '$' && driver == "postgres" && (i == 0 || (!isIdentifier(rune(input[i-1])) && input[i-1] != '$')) {
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
			if stripComments(text, driver) != "" {
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
	s = strings.TrimSpace(s)
	for {
		if (strings.HasPrefix(s, "--") && (driver != "mysql" || len(s) == 2 || unicode.IsSpace(rune(s[2])))) || (driver == "mysql" && strings.HasPrefix(s, "#")) {
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
