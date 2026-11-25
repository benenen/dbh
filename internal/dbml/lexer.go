package dbml

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokIdent
	tokString
	tokDoubleString
	tokBacktick
	tokNumber
	tokLBrace   // {
	tokRBrace   // }
	tokLBracket // [
	tokRBracket // ]
	tokLParen   // (
	tokRParen   // )
	tokComma    // ,
	tokColon    // :
	tokDot      // .
	tokLT       // <
	tokGT       // >
	tokDash     // -
	tokManyMany // <>
	tokNewline
)

type token struct {
	kind  tokenKind
	value string
	pos   Position
}

type lexer struct {
	src    string
	offset int
	line   int
	col    int
	errors []Error
}

func lex(src string) ([]token, []Error) {
	l := &lexer{src: src, line: 1, col: 1}
	var tokens []token
	for l.offset < len(l.src) {
		r, width := l.peek()
		if r == '\r' {
			l.advance(width)
			if next, nw := l.peek(); next == '\n' {
				l.advance(nw)
			}
			tokens = append(tokens, token{kind: tokNewline, value: "\n", pos: Position{Line: l.line, Column: l.col}})
			l.line++
			l.col = 1
			continue
		}
		if r == '\n' {
			tokens = append(tokens, token{kind: tokNewline, value: "\n", pos: Position{Line: l.line, Column: l.col}})
			l.advance(width)
			l.line++
			l.col = 1
			continue
		}
		if unicode.IsSpace(r) {
			l.advance(width)
			l.col++
			continue
		}
		// Line comment //
		if strings.HasPrefix(l.src[l.offset:], "//") {
			l.skipLineComment()
			continue
		}
		// Block comment /* ... */
		if strings.HasPrefix(l.src[l.offset:], "/*") {
			l.skipBlockComment()
			continue
		}

		pos := Position{Line: l.line, Column: l.col}

		// Triple-quoted string '''...'''
		if strings.HasPrefix(l.src[l.offset:], "'''") {
			val := l.readTripleString(pos)
			tokens = append(tokens, token{kind: tokString, value: val, pos: pos})
			continue
		}
		// Single-quoted string '...'
		if r == '\'' {
			val := l.readSingleString(pos)
			tokens = append(tokens, token{kind: tokString, value: val, pos: pos})
			continue
		}
		// Double-quoted identifier / string "..."
		if r == '"' {
			val := l.readDoubleString(pos)
			tokens = append(tokens, token{kind: tokDoubleString, value: val, pos: pos})
			continue
		}
		// Backtick expression `...`
		if r == '`' {
			val := l.readBacktick(pos)
			tokens = append(tokens, token{kind: tokBacktick, value: val, pos: pos})
			continue
		}

		// Two-char operator <>
		if strings.HasPrefix(l.src[l.offset:], "<>") {
			l.advance(2)
			l.col += 2
			tokens = append(tokens, token{kind: tokManyMany, value: "<>", pos: pos})
			continue
		}

		// Signed number vs dash operator
		if r == '-' || r == '+' {
			next, _ := utf8.DecodeRuneInString(l.src[l.offset+width:])
			if next >= '0' && next <= '9' {
				val := l.readNumber()
				tokens = append(tokens, token{kind: tokNumber, value: val, pos: pos})
				continue
			}
			if r == '-' {
				l.advance(width)
				l.col++
				tokens = append(tokens, token{kind: tokDash, value: "-", pos: pos})
				continue
			}
		}

		if r >= '0' && r <= '9' {
			val := l.readNumber()
			tokens = append(tokens, token{kind: tokNumber, value: val, pos: pos})
			continue
		}

		if isIdentStart(r) {
			val := l.readIdent()
			tokens = append(tokens, token{kind: tokIdent, value: val, pos: pos})
			continue
		}

		var kind tokenKind
		switch r {
		case '{':
			kind = tokLBrace
		case '}':
			kind = tokRBrace
		case '[':
			kind = tokLBracket
		case ']':
			kind = tokRBracket
		case '(':
			kind = tokLParen
		case ')':
			kind = tokRParen
		case ',':
			kind = tokComma
		case ':':
			kind = tokColon
		case '.':
			kind = tokDot
		case '<':
			kind = tokLT
		case '>':
			kind = tokGT
		default:
			l.errors = append(l.errors, Error{Position: pos, Message: "unexpected character " + string(r)})
			l.advance(width)
			l.col++
			continue
		}
		l.advance(width)
		l.col++
		tokens = append(tokens, token{kind: kind, value: string(r), pos: pos})
	}
	tokens = append(tokens, token{kind: tokEOF, pos: Position{Line: l.line, Column: l.col}})
	return tokens, l.errors
}

func (l *lexer) peek() (rune, int) {
	return utf8.DecodeRuneInString(l.src[l.offset:])
}

func (l *lexer) advance(n int) {
	l.offset += n
}

func (l *lexer) skipLineComment() {
	for l.offset < len(l.src) {
		r, w := l.peek()
		if r == '\n' || r == '\r' {
			return
		}
		l.advance(w)
		l.col++
	}
}

func (l *lexer) skipBlockComment() {
	startPos := Position{Line: l.line, Column: l.col}
	l.advance(2)
	l.col += 2
	for l.offset < len(l.src) {
		if strings.HasPrefix(l.src[l.offset:], "*/") {
			l.advance(2)
			l.col += 2
			return
		}
		r, w := l.peek()
		l.advance(w)
		if r == '\n' {
			l.line++
			l.col = 1
		} else {
			l.col++
		}
	}
	l.errors = append(l.errors, Error{Position: startPos, Message: "unterminated block comment"})
}

func (l *lexer) readSingleString(startPos Position) string {
	l.advance(1)
	l.col++
	var b strings.Builder
	for l.offset < len(l.src) {
		r, w := l.peek()
		if r == '\'' {
			l.advance(1)
			l.col++
			return b.String()
		}
		if r == '\\' && l.offset+1 < len(l.src) {
			next, nw := utf8.DecodeRuneInString(l.src[l.offset+1:])
			switch next {
			case '\'', '\\':
				b.WriteRune(next)
				l.advance(1 + nw)
				l.col += 2
				continue
			case 'n':
				b.WriteByte('\n')
				l.advance(1 + nw)
				l.col += 2
				continue
			case 'r':
				b.WriteByte('\r')
				l.advance(1 + nw)
				l.col += 2
				continue
			case 't':
				b.WriteByte('\t')
				l.advance(1 + nw)
				l.col += 2
				continue
			default:
				// Preserve unknown backslash sequences verbatim (e.g. C:\path).
				b.WriteByte('\\')
				b.WriteRune(next)
				l.advance(1 + nw)
				if next == '\n' {
					l.line++
					l.col = 1
				} else {
					l.col += 2
				}
				continue
			}
		}
		b.WriteRune(r)
		l.advance(w)
		if r == '\n' {
			l.line++
			l.col = 1
		} else {
			l.col++
		}
	}
	l.errors = append(l.errors, Error{Position: startPos, Message: "unterminated string literal"})
	return b.String()
}

func (l *lexer) readTripleString(startPos Position) string {
	l.advance(3)
	l.col += 3
	var b strings.Builder
	for l.offset < len(l.src) {
		if strings.HasPrefix(l.src[l.offset:], "'''") {
			l.advance(3)
			l.col += 3
			return b.String()
		}
		if strings.HasPrefix(l.src[l.offset:], `\'''`) && (!strings.HasPrefix(l.src[l.offset:], `\''''`) || strings.HasPrefix(l.src[l.offset:], `\''''''`)) {
			b.WriteString("'''")
			l.advance(4)
			l.col += 4
			continue
		}
		if strings.HasPrefix(l.src[l.offset:], `\\`) {
			b.WriteByte('\\')
			l.advance(2)
			l.col += 2
			continue
		}
		if strings.HasPrefix(l.src[l.offset:], `\'`) {
			b.WriteByte('\'')
			l.advance(2)
			l.col += 2
			continue
		}
		r, w := l.peek()
		b.WriteRune(r)
		l.advance(w)
		if r == '\n' {
			l.line++
			l.col = 1
		} else {
			l.col++
		}
	}
	l.errors = append(l.errors, Error{Position: startPos, Message: "unterminated multiline string literal"})
	return b.String()
}

func (l *lexer) readDoubleString(startPos Position) string {
	l.advance(1)
	l.col++
	var b strings.Builder
	for l.offset < len(l.src) {
		r, w := l.peek()
		if r == '"' {
			l.advance(1)
			l.col++
			return b.String()
		}
		if r == '\\' && l.offset+1 < len(l.src) {
			next, nw := utf8.DecodeRuneInString(l.src[l.offset+1:])
			switch next {
			case '"', '\\':
				b.WriteRune(next)
				l.advance(1 + nw)
				l.col += 2
				continue
			case 'n':
				b.WriteByte('\n')
				l.advance(1 + nw)
				l.col += 2
				continue
			case 'r':
				b.WriteByte('\r')
				l.advance(1 + nw)
				l.col += 2
				continue
			case 't':
				b.WriteByte('\t')
				l.advance(1 + nw)
				l.col += 2
				continue
			}
		}
		b.WriteRune(r)
		l.advance(w)
		if r == '\n' {
			l.line++
			l.col = 1
		} else {
			l.col++
		}
	}
	l.errors = append(l.errors, Error{Position: startPos, Message: "unterminated quoted identifier"})
	return b.String()
}

func (l *lexer) readBacktick(startPos Position) string {
	l.advance(1)
	l.col++
	var b strings.Builder
	for l.offset < len(l.src) {
		r, w := l.peek()
		if r == '`' {
			l.advance(1)
			l.col++
			return b.String()
		}
		if r == '\\' && l.offset+1 < len(l.src) {
			next, nw := utf8.DecodeRuneInString(l.src[l.offset+1:])
			if next == '`' || next == '\\' {
				b.WriteRune(next)
				l.advance(1 + nw)
				l.col += 2
				continue
			}
		}
		b.WriteRune(r)
		l.advance(w)
		if r == '\n' {
			l.line++
			l.col = 1
		} else {
			l.col++
		}
	}
	l.errors = append(l.errors, Error{Position: startPos, Message: "unterminated expression"})
	return b.String()
}

func (l *lexer) readNumber() string {
	start := l.offset
	if l.src[l.offset] == '-' || l.src[l.offset] == '+' {
		l.advance(1)
		l.col++
	}
	dotSeen := false
	for l.offset < len(l.src) {
		r, w := l.peek()
		if r >= '0' && r <= '9' {
			l.advance(w)
			l.col++
			continue
		}
		if r == '.' && !dotSeen && l.offset+1 < len(l.src) && l.src[l.offset+1] >= '0' && l.src[l.offset+1] <= '9' {
			dotSeen = true
			l.advance(w)
			l.col++
			continue
		}
		break
	}
	// Optional scientific notation exponent: [eE][+-]?[0-9]+
	if l.offset < len(l.src) && (l.src[l.offset] == 'e' || l.src[l.offset] == 'E') {
		expPos := l.offset + 1
		if expPos < len(l.src) && (l.src[expPos] == '+' || l.src[expPos] == '-') {
			expPos++
		}
		if expPos < len(l.src) && l.src[expPos] >= '0' && l.src[expPos] <= '9' {
			adv := expPos - l.offset
			l.advance(adv)
			l.col += adv
			for l.offset < len(l.src) {
				r, w := l.peek()
				if r >= '0' && r <= '9' {
					l.advance(w)
					l.col++
					continue
				}
				break
			}
		}
	}
	return l.src[start:l.offset]
}

func (l *lexer) readIdent() string {
	start := l.offset
	for l.offset < len(l.src) {
		r, w := l.peek()
		if !isIdentContinue(r) {
			break
		}
		l.advance(w)
		l.col++
	}
	return l.src[start:l.offset]
}

func isIdentStart(r rune) bool {
	return unicode.IsLetter(r) || r == '_'
}

func isIdentContinue(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}
