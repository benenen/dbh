package dbml

import (
	"fmt"
	"strings"
)

type parser struct {
	tokens []token
	pos    int
	opts   ParseOptions
	db     *Database
	errors []Error
}

// Parse lexes and parses DBML source into a Database AST.
func Parse(source string, opts ParseOptions) (*Database, []Error) {
	tokens, lexErrs := lex(source)
	p := &parser{
		tokens: tokens,
		opts:   opts,
		db:     &Database{},
		errors: lexErrs,
	}
	p.parseFile()
	return p.db, p.errors
}

func (p *parser) cur() token {
	if p.pos >= len(p.tokens) {
		return p.tokens[len(p.tokens)-1]
	}
	return p.tokens[p.pos]
}

func (p *parser) peekNonNewline(offset int) token {
	i := p.pos
	seen := 0
	for i < len(p.tokens) {
		if p.tokens[i].kind != tokNewline {
			if seen == offset {
				return p.tokens[i]
			}
			seen++
		}
		i++
	}
	return p.tokens[len(p.tokens)-1]
}

func (p *parser) next() token {
	t := p.cur()
	if p.pos < len(p.tokens)-1 {
		p.pos++
	}
	return t
}

func (p *parser) skipNewlines() {
	for p.cur().kind == tokNewline {
		p.next()
	}
}

func (p *parser) errorAt(pos Position, msg string) {
	p.errors = append(p.errors, Error{Position: pos, Message: msg})
}

func (p *parser) isIdentOrString(t token) bool {
	return t.kind == tokIdent || t.kind == tokDoubleString || t.kind == tokString
}

func (p *parser) parseFile() {
	for {
		p.skipNewlines()
		t := p.cur()
		if t.kind == tokEOF {
			return
		}
		if t.kind != tokIdent {
			p.errorAt(t.pos, fmt.Sprintf("unexpected token %q", t.value))
			p.next()
			continue
		}
		kw := strings.ToLower(t.value)
		if p.opts.RefsOnly && kw != "ref" {
			p.errorAt(t.pos, "only Ref declarations are allowed in a refs file")
		}
		switch kw {
		case "table":
			p.parseTable()
		case "ref":
			p.parseRef()
		case "enum":
			p.parseEnum()
		case "project", "tablegroup", "tablepartial", "note":
			p.skipTopLevelBlock()
		default:
			p.errorAt(t.pos, fmt.Sprintf("unexpected declaration %q", t.value))
			p.skipToNextTopLevel()
		}
	}
}

func (p *parser) skipToNextTopLevel() {
	for p.cur().kind != tokEOF {
		t := p.next()
		if t.kind == tokLBrace {
			p.skipBalancedBraces()
			return
		}
		if t.kind == tokNewline {
			return
		}
	}
}

func (p *parser) skipTopLevelBlock() {
	p.next() // keyword
	for p.cur().kind != tokEOF {
		t := p.cur()
		if t.kind == tokLBrace {
			p.next()
			p.skipBalancedBraces()
			return
		}
		if t.kind == tokNewline {
			// Check if '{' follows on next line for blocks like Project foo \n { ... }
			if p.peekNonNewline(0).kind == tokLBrace {
				p.skipNewlines()
				p.next()
				p.skipBalancedBraces()
			}
			return
		}
		p.next()
	}
}

func (p *parser) skipBalancedBraces() {
	depth := 1
	for p.cur().kind != tokEOF && depth > 0 {
		t := p.next()
		switch t.kind {
		case tokLBrace:
			depth++
		case tokRBrace:
			depth--
		}
	}
}

func (p *parser) parseQualifiedIdent() (*string, string, Position, bool) {
	t := p.cur()
	if !p.isIdentOrString(t) {
		p.errorAt(t.pos, fmt.Sprintf("expected identifier, got %q", t.value))
		return nil, "", t.pos, false
	}
	p.next()
	if p.cur().kind == tokDot {
		p.next()
		t2 := p.cur()
		if !p.isIdentOrString(t2) {
			p.errorAt(t2.pos, fmt.Sprintf("expected identifier after '.', got %q", t2.value))
			return nil, "", t.pos, false
		}
		p.next()
		schema := t.value
		return &schema, t2.value, t.pos, true
	}
	return nil, t.value, t.pos, true
}

func (p *parser) parseTable() {
	p.next() // Table
	schema, name, _, ok := p.parseQualifiedIdent()
	if !ok {
		p.skipToNextTopLevel()
		return
	}
	tbl := Table{SchemaName: schema, Name: name}

	// Optional: as alias
	if p.cur().kind == tokIdent && strings.EqualFold(p.cur().value, "as") {
		p.next()
		if p.isIdentOrString(p.cur()) {
			alias := p.cur().value
			tbl.Alias = &alias
			p.next()
		} else {
			p.errorAt(p.cur().pos, "expected table alias after 'as'")
		}
	}

	// Optional table settings [...]
	if p.cur().kind == tokLBracket {
		p.skipBracketSettings()
	}

	p.skipNewlines()
	if p.cur().kind != tokLBrace {
		p.errorAt(p.cur().pos, "expected '{' to start Table body")
		return
	}
	p.next() // {

	for {
		p.skipNewlines()
		t := p.cur()
		if t.kind == tokEOF {
			p.errorAt(t.pos, "unterminated Table block")
			break
		}
		if t.kind == tokRBrace {
			p.next()
			break
		}
		if t.kind == tokIdent {
			lower := strings.ToLower(t.value)
			nextTok := p.peekNonNewline(1)
			if lower == "note" && (nextTok.kind == tokColon || nextTok.kind == tokLBrace) {
				tbl.Note = p.parseNoteDecl()
				continue
			}
			if lower == "indexes" && nextTok.kind == tokLBrace {
				tbl.Indexes = append(tbl.Indexes, p.parseIndexesBlock()...)
				continue
			}
			if lower == "checks" && nextTok.kind == tokLBrace {
				p.next()
				p.skipNewlines()
				p.next() // {
				p.skipBalancedBraces()
				continue
			}
			if lower == "records" && (nextTok.kind == tokLParen || nextTok.kind == tokLBrace) {
				p.skipRecordsBlock()
				continue
			}
		}
		col, ok := p.parseColumn(tbl.SchemaName, tbl.Name)
		if ok {
			tbl.Fields = append(tbl.Fields, col)
		} else {
			p.skipLineInBlock()
		}
	}

	p.db.Tables = append(p.db.Tables, tbl)
}

func (p *parser) skipLineInBlock() {
	for p.cur().kind != tokEOF && p.cur().kind != tokNewline && p.cur().kind != tokRBrace {
		p.next()
	}
}

func (p *parser) skipBracketSettings() {
	if p.cur().kind != tokLBracket {
		return
	}
	p.next()
	depth := 1
	for p.cur().kind != tokEOF && depth > 0 {
		t := p.next()
		switch t.kind {
		case tokLBracket:
			depth++
		case tokRBracket:
			depth--
		}
	}
}

func (p *parser) skipRecordsBlock() {
	p.next() // records
	if p.cur().kind == tokLParen {
		p.next()
		depth := 1
		for p.cur().kind != tokEOF && depth > 0 {
			t := p.next()
			if t.kind == tokLParen {
				depth++
			} else if t.kind == tokRParen {
				depth--
			}
		}
	}
	p.skipNewlines()
	if p.cur().kind == tokLBrace {
		p.next()
		p.skipBalancedBraces()
	}
}

func (p *parser) parseNoteDecl() *Note {
	p.next() // Note
	p.skipNewlines()
	if p.cur().kind == tokColon {
		p.next()
		if p.cur().kind == tokString || p.cur().kind == tokDoubleString || p.cur().kind == tokIdent {
			val := p.cur().value
			p.next()
			return &Note{Value: val}
		}
		p.errorAt(p.cur().pos, "expected string after Note:")
		return nil
	}
	if p.cur().kind == tokLBrace {
		p.next()
		p.skipNewlines()
		var parts []string
		for p.cur().kind != tokEOF && p.cur().kind != tokRBrace {
			if p.cur().kind == tokString || p.cur().kind == tokDoubleString || p.cur().kind == tokIdent {
				parts = append(parts, p.cur().value)
			}
			p.next()
			p.skipNewlines()
		}
		if p.cur().kind == tokRBrace {
			p.next()
		} else {
			p.errorAt(p.cur().pos, "unterminated Note block")
		}
		return &Note{Value: strings.Join(parts, "\n")}
	}
	return nil
}

func (p *parser) parseColumn(tableSchema *string, tableName string) (Column, bool) {
	nameTok := p.cur()
	if !p.isIdentOrString(nameTok) {
		p.errorAt(nameTok.pos, fmt.Sprintf("expected column name, got %q", nameTok.value))
		return Column{}, false
	}
	p.next()
	col := Column{Name: nameTok.value}

	colType, ok := p.parseColumnType()
	if !ok {
		return Column{}, false
	}
	col.Type = colType

	if p.cur().kind == tokLBracket {
		p.parseColumnSettings(&col, tableSchema, tableName, nameTok.pos.Line)
	}
	return col, true
}

func (p *parser) parseColumnType() (ColumnType, bool) {
	t := p.cur()
	if !p.isIdentOrString(t) {
		p.errorAt(t.pos, fmt.Sprintf("expected column type, got %q", t.value))
		return ColumnType{}, false
	}
	p.next()
	var ct ColumnType
	if p.cur().kind == tokDot {
		p.next()
		t2 := p.cur()
		if !p.isIdentOrString(t2) {
			p.errorAt(t2.pos, fmt.Sprintf("expected type name after '.', got %q", t2.value))
			return ColumnType{}, false
		}
		p.next()
		schema := t.value
		ct.SchemaName = &schema
		ct.TypeName = t2.value
	} else {
		ct.TypeName = t.value
	}

	// Optional (args)
	if p.cur().kind == tokLParen {
		p.next()
		var args []string
		depth := 1
		for p.cur().kind != tokEOF && depth > 0 {
			tok := p.next()
			if tok.kind == tokLParen {
				depth++
				args = append(args, "(")
				continue
			}
			if tok.kind == tokRParen {
				depth--
				if depth > 0 {
					args = append(args, ")")
				}
				continue
			}
			if tok.kind == tokNewline {
				continue
			}
			args = append(args, tok.value)
		}
		ct.Args = joinTypeArgs(args)
	}

	// Optional array suffix [] or multi-word SQL type suffixes (e.g., "unsigned", "with time zone").
	var suffixes []string
	for p.cur().kind != tokEOF && p.cur().kind != tokNewline && p.cur().kind != tokLBracket && p.cur().kind != tokRBrace {
		if p.cur().kind == tokLBracket && p.peekNonNewline(1).kind == tokRBracket {
			// Not possible in our lexer since [ is always tokLBracket, handled below.
			break
		}
		if p.cur().kind == tokIdent {
			suffixes = append(suffixes, p.cur().value)
			p.next()
			continue
		}
		break
	}
	if p.cur().kind == tokLBracket && p.peekNonNewline(1).kind == tokRBracket {
		// Array suffix `[]` before optional `[settings]`
		p.next()
		p.next()
		suffixes = append(suffixes, "[]")
	}
	if len(suffixes) > 0 {
		ct.Suffix = strings.Join(suffixes, " ")
	}
	return ct, true
}

func joinTypeArgs(parts []string) string {
	var b strings.Builder
	for i, part := range parts {
		if i > 0 && part != "," && parts[i-1] != "(" && part != ")" {
			b.WriteByte(' ')
		}
		b.WriteString(part)
	}
	return b.String()
}

func (p *parser) parseColumnSettings(col *Column, tableSchema *string, tableName string, line int) {
	p.next() // [
	for p.cur().kind != tokEOF && p.cur().kind != tokRBracket {
		p.skipNewlines()
		if p.cur().kind == tokRBracket {
			break
		}
		if p.cur().kind == tokComma {
			p.next()
			continue
		}
		t := p.cur()
		if t.kind != tokIdent {
			p.errorAt(t.pos, fmt.Sprintf("unexpected token %q in column settings", t.value))
			p.next()
			continue
		}
		key := strings.ToLower(t.value)
		p.next()
		switch key {
		case "pk":
			col.PK = true
		case "primary":
			if p.cur().kind == tokIdent && strings.EqualFold(p.cur().value, "key") {
				p.next()
			}
			col.PK = true
		case "unique":
			col.Unique = true
		case "increment":
			v := true
			col.Increment = &v
		case "null":
			v := false
			col.NotNull = &v
		case "not":
			if p.cur().kind == tokIdent && strings.EqualFold(p.cur().value, "null") {
				p.next()
				v := true
				col.NotNull = &v
			}
		case "default":
			if p.cur().kind == tokColon {
				p.next()
				col.DBDefault = p.parseDefaultValue()
			}
		case "note":
			if p.cur().kind == tokColon {
				p.next()
				if p.cur().kind == tokString || p.cur().kind == tokDoubleString || p.cur().kind == tokIdent {
					col.Note = &Note{Value: p.cur().value}
					p.next()
				}
			}
		case "ref":
			if p.cur().kind == tokColon {
				p.next()
				p.parseInlineRef(tableSchema, tableName, col.Name, line)
			}
		default:
			// Consume unknown setting value if followed by ':'
			if p.cur().kind == tokColon {
				p.next()
				p.skipSettingValue()
			}
		}
	}
	if p.cur().kind == tokRBracket {
		p.next()
	} else {
		p.errorAt(p.cur().pos, "unterminated column settings '['")
	}
}

func (p *parser) parseDefaultValue() *DefaultValue {
	t := p.cur()
	switch t.kind {
	case tokString, tokDoubleString:
		p.next()
		return &DefaultValue{Type: "string", Value: t.value}
	case tokNumber:
		p.next()
		return &DefaultValue{Type: "number", Value: t.value}
	case tokBacktick:
		p.next()
		return &DefaultValue{Type: "expression", Value: t.value}
	case tokIdent:
		p.next()
		lower := strings.ToLower(t.value)
		switch lower {
		case "true", "false":
			return &DefaultValue{Type: "boolean", Value: lower}
		case "null":
			return &DefaultValue{Type: "null", Value: nil}
		default:
			return &DefaultValue{Type: "expression", Value: t.value}
		}
	default:
		p.errorAt(t.pos, fmt.Sprintf("expected default value, got %q", t.value))
		return nil
	}
}

func (p *parser) skipSettingValue() {
	for p.cur().kind != tokEOF && p.cur().kind != tokComma && p.cur().kind != tokRBracket {
		p.next()
	}
}

func (p *parser) parseInlineRef(tableSchema *string, tableName, colName string, line int) {
	op, ok := p.parseRefOp()
	if !ok {
		return
	}
	right, ok := p.parseRefEndpoint()
	if !ok {
		return
	}
	left := RefEndpoint{
		SchemaName: tableSchema,
		TableName:  tableName,
		FieldNames: []string{colName},
		Line:       line,
	}
	applyOpRelations(&left, &right, op)
	p.db.Refs = append(p.db.Refs, Ref{
		Endpoints: []RefEndpoint{left, right},
		Line:      line,
	})
}

func (p *parser) parseIndexesBlock() []Index {
	p.next() // indexes
	p.skipNewlines()
	p.next() // {
	var indexes []Index
	for {
		p.skipNewlines()
		if p.cur().kind == tokEOF {
			p.errorAt(p.cur().pos, "unterminated indexes block")
			break
		}
		if p.cur().kind == tokRBrace {
			p.next()
			break
		}
		var idx Index
		if p.cur().kind == tokLParen {
			p.next()
			for p.cur().kind != tokEOF && p.cur().kind != tokRParen {
				if p.cur().kind == tokComma {
					p.next()
					continue
				}
				if p.cur().kind == tokBacktick {
					idx.Columns = append(idx.Columns, IndexColumn{Type: "expression", Value: p.cur().value})
					p.next()
				} else if p.isIdentOrString(p.cur()) {
					idx.Columns = append(idx.Columns, IndexColumn{Type: "column", Value: p.cur().value})
					p.next()
				} else {
					p.errorAt(p.cur().pos, fmt.Sprintf("unexpected token %q in index", p.cur().value))
					p.next()
				}
			}
			if p.cur().kind == tokRParen {
				p.next()
			}
		} else if p.cur().kind == tokBacktick {
			idx.Columns = append(idx.Columns, IndexColumn{Type: "expression", Value: p.cur().value})
			p.next()
		} else if p.isIdentOrString(p.cur()) {
			idx.Columns = append(idx.Columns, IndexColumn{Type: "column", Value: p.cur().value})
			p.next()
		} else {
			p.errorAt(p.cur().pos, fmt.Sprintf("unexpected token %q in indexes block", p.cur().value))
			p.skipLineInBlock()
			continue
		}

		if p.cur().kind == tokLBracket {
			p.parseIndexSettings(&idx)
		}
		indexes = append(indexes, idx)
	}
	return indexes
}

func (p *parser) parseIndexSettings(idx *Index) {
	p.next() // [
	for p.cur().kind != tokEOF && p.cur().kind != tokRBracket {
		if p.cur().kind == tokComma || p.cur().kind == tokNewline {
			p.next()
			continue
		}
		t := p.cur()
		if t.kind != tokIdent {
			p.next()
			continue
		}
		key := strings.ToLower(t.value)
		p.next()
		switch key {
		case "pk":
			idx.PK = true
		case "unique":
			idx.Unique = true
		case "name":
			if p.cur().kind == tokColon {
				p.next()
				if p.isIdentOrString(p.cur()) {
					val := p.cur().value
					idx.Name = &val
					p.next()
				}
			}
		case "type":
			if p.cur().kind == tokColon {
				p.next()
				if p.isIdentOrString(p.cur()) {
					val := p.cur().value
					idx.Type = &val
					p.next()
				}
			}
		case "note":
			if p.cur().kind == tokColon {
				p.next()
				if p.isIdentOrString(p.cur()) {
					idx.Note = &Note{Value: p.cur().value}
					p.next()
				}
			}
		default:
			if p.cur().kind == tokColon {
				p.next()
				p.skipSettingValue()
			}
		}
	}
	if p.cur().kind == tokRBracket {
		p.next()
	}
}

func (p *parser) parseRef() {
	refTok := p.next() // Ref
	var refName *string
	if p.isIdentOrString(p.cur()) && p.peekNonNewline(1).kind != tokDot {
		name := p.cur().value
		refName = &name
		p.next()
	}
	if p.cur().kind == tokColon {
		p.next()
		p.skipNewlines()
		if ref, ok := p.parseSingleRef(refName, refTok.pos.Line); ok {
			p.db.Refs = append(p.db.Refs, ref)
		}
		return
	}
	p.skipNewlines()
	if p.cur().kind == tokLBrace {
		p.next()
		for {
			p.skipNewlines()
			if p.cur().kind == tokEOF {
				p.errorAt(p.cur().pos, "unterminated Ref block")
				break
			}
			if p.cur().kind == tokRBrace {
				p.next()
				break
			}
			if ref, ok := p.parseSingleRef(refName, p.cur().pos.Line); ok {
				p.db.Refs = append(p.db.Refs, ref)
			} else {
				p.skipLineInBlock()
			}
		}
		return
	}
	if ref, ok := p.parseSingleRef(refName, refTok.pos.Line); ok {
		p.db.Refs = append(p.db.Refs, ref)
	}
}

func (p *parser) parseSingleRef(name *string, line int) (Ref, bool) {
	left, ok := p.parseRefEndpoint()
	if !ok {
		return Ref{}, false
	}
	op, ok := p.parseRefOp()
	if !ok {
		return Ref{}, false
	}
	right, ok := p.parseRefEndpoint()
	if !ok {
		return Ref{}, false
	}
	applyOpRelations(&left, &right, op)
	ref := Ref{
		Name:      name,
		Endpoints: []RefEndpoint{left, right},
		Line:      left.Line,
	}
	if ref.Line == 0 {
		ref.Line = line
	}
	if p.cur().kind == tokLBracket {
		p.parseRefSettings(&ref)
	}
	return ref, true
}

func (p *parser) parseRefOp() (tokenKind, bool) {
	t := p.cur()
	switch t.kind {
	case tokLT, tokGT, tokDash, tokManyMany:
		p.next()
		return t.kind, true
	default:
		p.errorAt(t.pos, fmt.Sprintf("expected relationship operator (<, >, -, <>), got %q", t.value))
		return 0, false
	}
}

func applyOpRelations(left, right *RefEndpoint, op tokenKind) {
	switch op {
	case tokGT:
		left.Relation = "*"
		right.Relation = "1"
	case tokLT:
		left.Relation = "1"
		right.Relation = "*"
	case tokManyMany:
		left.Relation = "*"
		right.Relation = "*"
	default:
		left.Relation = "1"
		right.Relation = "1"
	}
}

func (p *parser) parseRefEndpoint() (RefEndpoint, bool) {
	t1 := p.cur()
	if !p.isIdentOrString(t1) {
		p.errorAt(t1.pos, fmt.Sprintf("expected table name in Ref, got %q", t1.value))
		return RefEndpoint{}, false
	}
	p.next()
	if p.cur().kind != tokDot {
		p.errorAt(p.cur().pos, "expected '.' in Ref endpoint")
		return RefEndpoint{}, false
	}
	p.next()

	var schema *string
	table := t1.value

	// Either schema.table.col / schema.table.(cols) OR table.col / table.(cols)
	if p.cur().kind == tokLParen {
		cols, ok := p.parseParenFieldList()
		return RefEndpoint{SchemaName: schema, TableName: table, FieldNames: cols, Line: t1.pos.Line}, ok
	}
	t2 := p.cur()
	if !p.isIdentOrString(t2) {
		p.errorAt(t2.pos, fmt.Sprintf("expected column or table name after '.', got %q", t2.value))
		return RefEndpoint{}, false
	}
	p.next()
	if p.cur().kind == tokDot {
		p.next()
		s := table
		schema = &s
		table = t2.value
		if p.cur().kind == tokLParen {
			cols, ok := p.parseParenFieldList()
			return RefEndpoint{SchemaName: schema, TableName: table, FieldNames: cols, Line: t1.pos.Line}, ok
		}
		t3 := p.cur()
		if !p.isIdentOrString(t3) {
			p.errorAt(t3.pos, fmt.Sprintf("expected column name after '.', got %q", t3.value))
			return RefEndpoint{}, false
		}
		p.next()
		return RefEndpoint{SchemaName: schema, TableName: table, FieldNames: []string{t3.value}, Line: t1.pos.Line}, true
	}
	return RefEndpoint{SchemaName: schema, TableName: table, FieldNames: []string{t2.value}, Line: t1.pos.Line}, true
}

func (p *parser) parseParenFieldList() ([]string, bool) {
	p.next() // (
	var cols []string
	for p.cur().kind != tokEOF && p.cur().kind != tokRParen {
		if p.cur().kind == tokComma {
			p.next()
			continue
		}
		if !p.isIdentOrString(p.cur()) {
			p.errorAt(p.cur().pos, fmt.Sprintf("expected column name, got %q", p.cur().value))
			return nil, false
		}
		cols = append(cols, p.cur().value)
		p.next()
	}
	if p.cur().kind != tokRParen {
		p.errorAt(p.cur().pos, "expected ')' in composite Ref endpoint")
		return nil, false
	}
	p.next()
	return cols, true
}

func (p *parser) parseRefSettings(ref *Ref) {
	p.next() // [
	for p.cur().kind != tokEOF && p.cur().kind != tokRBracket {
		if p.cur().kind == tokComma || p.cur().kind == tokNewline {
			p.next()
			continue
		}
		t := p.cur()
		if t.kind != tokIdent {
			p.next()
			continue
		}
		key := strings.ToLower(t.value)
		p.next()
		if p.cur().kind != tokColon {
			continue
		}
		p.next()
		switch key {
		case "delete":
			val := p.readActionValue()
			ref.OnDelete = &val
		case "update":
			val := p.readActionValue()
			ref.OnUpdate = &val
		default:
			p.skipSettingValue()
		}
	}
	if p.cur().kind == tokRBracket {
		p.next()
	}
}

func (p *parser) readActionValue() string {
	var parts []string
	for p.cur().kind == tokIdent || p.cur().kind == tokString || p.cur().kind == tokDoubleString {
		parts = append(parts, p.cur().value)
		p.next()
	}
	return strings.Join(parts, " ")
}

func (p *parser) parseEnum() {
	p.next() // Enum
	schema, name, _, ok := p.parseQualifiedIdent()
	if !ok {
		p.skipToNextTopLevel()
		return
	}
	p.skipNewlines()
	if p.cur().kind != tokLBrace {
		p.errorAt(p.cur().pos, "expected '{' to start Enum body")
		return
	}
	p.next()
	enum := Enum{SchemaName: schema, Name: name}
	for {
		p.skipNewlines()
		if p.cur().kind == tokEOF {
			p.errorAt(p.cur().pos, "unterminated Enum block")
			break
		}
		if p.cur().kind == tokRBrace {
			p.next()
			break
		}
		if !p.isIdentOrString(p.cur()) {
			p.errorAt(p.cur().pos, fmt.Sprintf("expected enum value, got %q", p.cur().value))
			p.next()
			continue
		}
		val := EnumValue{Name: p.cur().value}
		p.next()
		if p.cur().kind == tokLBracket {
			p.next()
			for p.cur().kind != tokEOF && p.cur().kind != tokRBracket {
				if p.cur().kind == tokIdent && strings.EqualFold(p.cur().value, "note") {
					p.next()
					if p.cur().kind == tokColon {
						p.next()
						if p.isIdentOrString(p.cur()) {
							val.Note = &Note{Value: p.cur().value}
							p.next()
						}
					}
					continue
				}
				p.next()
			}
			if p.cur().kind == tokRBracket {
				p.next()
			}
		}
		enum.Values = append(enum.Values, val)
	}
	p.db.Enums = append(p.db.Enums, enum)
}
