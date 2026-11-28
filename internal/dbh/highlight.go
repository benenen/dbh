package dbh

import (
	"strings"
	"unicode"

	"github.com/benenen/dbh/internal/database"
)

const (
	ansiReset   = "\x1b[0m"
	ansiKeyword = "\x1b[1;34m"
	ansiString  = "\x1b[32m"
	ansiComment = "\x1b[90m"
	ansiNumber  = "\x1b[35m"
	ansiMeta    = "\x1b[36m"
)

var sqlKeywordSet = func() map[string]bool {
	m := make(map[string]bool, len(keywords)+24)
	for _, k := range keywords {
		m[strings.ToUpper(k)] = true
	}
	for _, extra := range []string{
		"TRIGGER", "PROCEDURE", "FUNCTION", "RETURNS", "DECLARE", "DELIMITER",
		"IF", "ELSIF", "ELSEIF", "LOOP", "WHILE", "REPEAT", "UNTIL", "FOR", "EACH", "ROW",
		"AFTER", "BEFORE", "INSTEAD", "OF", "TEMPORARY", "TEMP", "REPLACE", "CASCADE",
		"RESTRICT", "CHECK", "UNIQUE", "FOREIGN", "DEFAULT", "CONSTRAINT", "VIEW", "MATERIALIZED",
		"EXPLAIN", "ANALYZE", "TRUNCATE", "USE", "DATABASE", "SCHEMA", "GRANT", "REVOKE",
	} {
		m[extra] = true
	}
	return m
}()

// highlightSQL applies ANSI colors to SQL tokens and leading shell meta-commands.
// Stripping ANSI CSI sequences from the return value always yields string(line).
func highlightSQL(line []rune, syntax database.Syntax) string {
	s := string(line)
	if s == "" {
		return ""
	}
	var out strings.Builder
	out.Grow(len(s) + 32)

	i := 0
	// Highlight a leading shell meta-command (\tables, \ddl, etc.).
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		out.WriteByte(s[i])
		i++
	}
	if i < len(s) && s[i] == '\\' {
		start := i
		for i < len(s) && s[i] != ' ' && s[i] != '\t' && s[i] != '\n' && s[i] != '\r' {
			i++
		}
		out.WriteString(ansiMeta)
		out.WriteString(s[start:i])
		out.WriteString(ansiReset)
		out.WriteString(s[i:])
		return out.String()
	}

	for i < len(s) {
		c := s[i]

		// Line comments: --, //, #
		if (i+1 < len(s) && s[i:i+2] == "--" && (!syntax.DashCommentNeedsSpace || i+2 == len(s) || unicode.IsSpace(rune(s[i+2])))) ||
			(i+1 < len(s) && s[i:i+2] == "//" && syntax.SlashComments) ||
			(c == '#' && syntax.HashComments) {
			start := i
			for i < len(s) && s[i] != '\n' {
				i++
			}
			out.WriteString(ansiComment)
			out.WriteString(s[start:i])
			out.WriteString(ansiReset)
			continue
		}

		// Block comments: /* ... */
		if i+1 < len(s) && s[i:i+2] == "/*" {
			start := i
			depth := 1
			i += 2
			for i < len(s) && depth > 0 {
				if syntax.NestedComments && i+1 < len(s) && s[i:i+2] == "/*" {
					depth++
					i += 2
					continue
				}
				if i+1 < len(s) && s[i:i+2] == "*/" {
					depth--
					i += 2
					continue
				}
				i++
			}
			out.WriteString(ansiComment)
			out.WriteString(s[start:i])
			out.WriteString(ansiReset)
			continue
		}

		// Dollar-quoted strings (PostgreSQL).
		if c == '$' && syntax.DollarQuotes && (i == 0 || (!isIdentByte(s[i-1]) && s[i-1] != '$')) {
			j := i + 1
			for j < len(s) && (isASCIIAlpha(s[j]) || s[j] == '_' || (j > i+1 && isASCIIDigit(s[j]))) {
				j++
			}
			if j < len(s) && s[j] == '$' {
				tag := s[i : j+1]
				start := i
				i = j + 1
				if end := strings.Index(s[i:], tag); end >= 0 {
					i += end + len(tag)
				} else {
					i = len(s)
				}
				out.WriteString(ansiString)
				out.WriteString(s[start:i])
				out.WriteString(ansiReset)
				continue
			}
		}

		// Single-quoted strings (including PostgreSQL E'...').
		if c == '\'' {
			start := i
			escape := syntax.BackslashEscapes || (syntax.EscapeStringPrefix && i > 0 && (s[i-1] == 'E' || s[i-1] == 'e') && (i == 1 || !isIdentByte(s[i-2])))
			i++
			for i < len(s) {
				if s[i] == '\\' && escape {
					if i+1 < len(s) {
						i += 2
					} else {
						i++
					}
					continue
				}
				if s[i] == '\'' {
					if i+1 < len(s) && s[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
			out.WriteString(ansiString)
			out.WriteString(s[start:i])
			out.WriteString(ansiReset)
			continue
		}

		// Quoted identifiers ("...", `...`, [...]) stay default color.
		if c == '"' || c == '`' || (c == '[' && syntax.BracketIdentifiers) {
			endChar := c
			if c == '[' {
				endChar = ']'
			}
			start := i
			i++
			for i < len(s) {
				if s[i] == '\\' && syntax.BackslashEscapes && c == '`' && syntax.BacktickEscapes {
					if i+1 < len(s) {
						i += 2
					} else {
						i++
					}
					continue
				}
				if s[i] == endChar {
					if endChar != ']' && i+1 < len(s) && s[i+1] == endChar {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
			out.WriteString(s[start:i])
			continue
		}

		// Numeric literals (not preceded by identifier character).
		if isASCIIDigit(c) && (i == 0 || !isIdentByte(s[i-1])) {
			start := i
			i++
			for i < len(s) && (isASCIIDigit(s[i]) || s[i] == '.' || s[i] == 'e' || s[i] == 'E') {
				i++
			}
			out.WriteString(ansiNumber)
			out.WriteString(s[start:i])
			out.WriteString(ansiReset)
			continue
		}

		// ASCII word tokens: highlight SQL keywords.
		if isASCIIAlpha(c) || c == '_' {
			start := i
			for i < len(s) && isIdentByte(s[i]) {
				i++
			}
			word := s[start:i]
			if sqlKeywordSet[strings.ToUpper(word)] {
				out.WriteString(ansiKeyword)
				out.WriteString(word)
				out.WriteString(ansiReset)
			} else {
				out.WriteString(word)
			}
			continue
		}

		out.WriteByte(c)
		i++
	}
	return out.String()
}

func isASCIIAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isASCIIDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

func isIdentByte(b byte) bool {
	return isASCIIAlpha(b) || isASCIIDigit(b) || b == '_'
}
