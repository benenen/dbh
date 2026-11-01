package dbh

import (
	"strings"
	"unicode"

	"github.com/benenen/dbh/internal/database"
)

type sqlToken struct {
	text   string
	depth  int
	quoted bool
}

// contextTokens skips comments and literals so their keywords cannot change SQL context.
// It accepts unfinished input because completion runs before a statement is complete.
func contextTokens(input string, syntax database.Syntax) []sqlToken {
	r := []rune(input)
	var result []sqlToken
	depth := 0
	for i := 0; i < len(r); {
		ch := r[i]
		if unicode.IsSpace(ch) {
			i++
			continue
		}
		if (ch == '-' && i+1 < len(r) && r[i+1] == '-' && (!syntax.DashCommentNeedsSpace || i+2 == len(r) || unicode.IsSpace(r[i+2]))) || (ch == '#' && syntax.HashComments) {
			for i < len(r) && r[i] != '\n' {
				i++
			}
			continue
		}
		if ch == '/' && i+1 < len(r) && r[i+1] == '*' {
			nesting := 1
			i += 2
			for i < len(r) && nesting > 0 {
				if i+1 < len(r) && r[i] == '*' && r[i+1] == '/' {
					nesting--
					i += 2
				} else if syntax.NestedComments && i+1 < len(r) && r[i] == '/' && r[i+1] == '*' {
					nesting++
					i += 2
				} else {
					i++
				}
			}
			continue
		}
		if ch == '$' && syntax.DollarQuotes {
			j := i + 1
			for j < len(r) && (unicode.IsLetter(r[j]) || r[j] == '_' || (j > i+1 && unicode.IsDigit(r[j]))) {
				j++
			}
			if j < len(r) && r[j] == '$' {
				tag := string(r[i : j+1])
				rest := string(r[j+1:])
				end := strings.Index(rest, tag)
				if end < 0 {
					break
				}
				i = j + 1 + len([]rune(rest[:end])) + len([]rune(tag))
				result = append(result, sqlToken{depth: depth, quoted: true})
				continue
			}
		}
		if ch == '\'' || ch == '"' || ch == '`' || (ch == '[' && syntax.BracketIdentifiers) {
			end := ch
			if ch == '[' {
				end = ']'
			}
			escape := syntax.BackslashEscapes || (ch == '\'' && syntax.EscapeStringPrefix && i > 0 && (r[i-1] == 'E' || r[i-1] == 'e'))
			var value strings.Builder
			i++
			for i < len(r) {
				if r[i] == '\\' && escape && end != ']' && (end != '`' || syntax.BacktickEscapes) && i+1 < len(r) {
					value.WriteRune(r[i+1])
					i += 2
					continue
				}
				if r[i] == end {
					if i+1 < len(r) && r[i+1] == end {
						value.WriteRune(end)
						i += 2
						continue
					}
					i++
					break
				}
				value.WriteRune(r[i])
				i++
			}
			text := value.String()
			if ch == '\'' {
				text = ""
			}
			result = append(result, sqlToken{text: text, depth: depth, quoted: true})
			continue
		}
		if ch == ')' {
			depth--
			if depth < 0 {
				depth = 0
			}
		}
		if unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '_' || ch == '$' {
			start := i
			i++
			for i < len(r) && (unicode.IsLetter(r[i]) || unicode.IsDigit(r[i]) || r[i] == '_' || r[i] == '$') {
				i++
			}
			result = append(result, sqlToken{text: string(r[start:i]), depth: depth})
			continue
		}
		result = append(result, sqlToken{text: string(ch), depth: depth})
		if ch == '(' {
			depth++
		}
		i++
	}
	return result
}

func tokenIs(t sqlToken, word string) bool { return !t.quoted && strings.EqualFold(t.text, word) }

func tableContext(tokens []sqlToken) bool {
	if len(tokens) == 0 {
		return false
	}
	last := tokens[len(tokens)-1]
	if tokenIs(last, "FROM") || tokenIs(last, "JOIN") {
		return true
	}
	if last.text != "," {
		return false
	}
	for i := len(tokens) - 2; i >= 0; i-- {
		t := tokens[i]
		if t.depth != last.depth {
			continue
		}
		if tokenIs(t, "FROM") {
			return true
		}
		for _, end := range []string{"SELECT", "WHERE", "ON", "GROUP", "ORDER", "SET", "VALUES", ";"} {
			if tokenIs(t, end) {
				return false
			}
		}
	}
	return false
}

func (c *completer) aliasColumns(line, qualifier string, cursorDepth int) []string {
	tokens := contextTokens(line, c.syntax)
	bestDepth := -1
	var columns []string
	for i := 0; i+1 < len(tokens); i++ {
		t := tokens[i]
		source := tokenIs(t, "FROM") || tokenIs(t, "JOIN") || tokenIs(t, "UPDATE") || tokenIs(t, "INTO")
		if t.text == "," {
			source = tableContext(tokens[:i+1])
		}
		if !source || t.depth > cursorDepth || t.depth < bestDepth {
			continue
		}
		j := i + 1
		if tokens[j].text == "(" || tokens[j].text == "" {
			continue
		}
		table := tokens[j].text
		j++
		for j+1 < len(tokens) && tokens[j].text == "." {
			table += "." + tokens[j+1].text
			j += 2
		}
		alias := ""
		if j < len(tokens) && tokenIs(tokens[j], "AS") {
			j++
		}
		if j < len(tokens) && !reservedAlias(tokens[j]) {
			alias = tokens[j].text
		}
		parts := strings.Split(table, ".")
		if !strings.EqualFold(qualifier, alias) && !strings.EqualFold(qualifier, table) && !strings.EqualFold(qualifier, parts[len(parts)-1]) {
			continue
		}
		for name, cols := range c.columns {
			if strings.EqualFold(name, table) {
				columns = cols
				bestDepth = t.depth
				break
			}
		}
	}
	// A table qualifier can be used while its FROM clause is still being edited.
	if bestDepth < 0 {
		for name, cols := range c.columns {
			if strings.EqualFold(name, qualifier) {
				return cols
			}
		}
	}
	return columns
}

func reservedAlias(t sqlToken) bool {
	if t.quoted {
		return t.text == ""
	}
	for _, w := range keywords {
		if strings.EqualFold(t.text, w) {
			return true
		}
	}
	switch strings.ToUpper(t.text) {
	case "USING", "CROSS", "FULL", "NATURAL", "WINDOW", "FETCH", "FOR":
		return true
	}
	return strings.ContainsAny(t.text, "(),;.=+-*/") || t.text == ""
}

// affectedStatement selects non-returning DML for ExecContext, which exposes
// RowsAffected without executing a write a second time. CTE bodies are nested.
func affectedStatement(query string, syntax database.Syntax) bool {
	tokens := contextTokens(query, syntax)
	dml := false
	for _, t := range tokens {
		if t.depth != 0 || t.quoted {
			continue
		}
		if tokenIs(t, "RETURNING") || tokenIs(t, "OUTPUT") {
			return false
		}
		if !dml {
			switch strings.ToUpper(t.text) {
			case "INSERT", "UPDATE", "DELETE", "REPLACE", "MERGE":
				dml = true
			case "WITH", "RECURSIVE", ",", "AS", "(", ")":
			case "SELECT", "EXPLAIN", "SHOW", "PRAGMA", "CREATE", "ALTER", "DROP":
				return false
			}
		}
	}
	return dml
}

func completionIdentifier(name string, syntax database.Syntax) string {
	parts := database.SplitIdentifier(name)
	for i, part := range parts {
		quote := reservedAlias(sqlToken{text: part})
		for j, r := range part {
			if (!unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_') || (j == 0 && unicode.IsDigit(r)) || (syntax.FoldUnquotedIdentifiers && unicode.IsUpper(r)) {
				quote = true
			}
		}
		if quote {
			delimiter := `"`
			if syntax.BacktickEscapes || syntax.DashCommentNeedsSpace {
				delimiter = "`"
			}
			parts[i] = delimiter + strings.ReplaceAll(part, delimiter, delimiter+delimiter) + delimiter
		}
	}
	return strings.Join(parts, ".")
}
