package dbml

import "testing"

func TestStringEscapes(t *testing.T) {
	tokens, errs := lex(`'it\'s' 'a\\' 'C:\path'`)
	if len(errs) > 0 {
		t.Fatalf("unexpected lex errors: %v", errs)
	}
	want := []string{"it's", `a\`, `C:\path`}
	for i, w := range want {
		if tokens[i].kind != tokString || tokens[i].value != w {
			t.Errorf("token %d: got %v %q, want %q", i, tokens[i].kind, tokens[i].value, w)
		}
	}
}

func TestTripleQuotedNoteAndEscapes(t *testing.T) {
	src := "Table items {\n  id int [pk, note: '''line1\nline2 \\'''quoted\\''' and \\\\slash''']\n}\n"
	db, errs := Parse(src, ParseOptions{})
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	note := db.Tables[0].Fields[0].Note
	want := "line1\nline2 '''quoted''' and \\slash"
	if note == nil || note.Value != want {
		t.Fatalf("note = %+v, want %q", note, want)
	}
}

func TestKeywordNamedColumns(t *testing.T) {
	src := "Table t {\n  \"note\" TEXT\n  records int [not null]\n  Note: 'table note'\n}\n" +
		"Table u {\n  note varchar(20)\n  Note {\n    'block note'\n  }\n  records (note) {\n    'x'\n  }\n}\n"
	db, errs := Parse(src, ParseOptions{})
	if len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	tbl := db.Tables[0]
	if len(tbl.Fields) != 2 || tbl.Fields[0].Name != "note" || tbl.Fields[1].Name != "records" {
		t.Fatalf("fields = %+v", tbl.Fields)
	}
	if tbl.Note == nil || tbl.Note.Value != "table note" {
		t.Fatalf("note = %+v", tbl.Note)
	}
	u := db.Tables[1]
	if len(u.Fields) != 1 || u.Fields[0].Name != "note" {
		t.Fatalf("table u fields = %+v", u.Fields)
	}
	if u.Note == nil || u.Note.Value != "block note" {
		t.Fatalf("table u note = %+v", u.Note)
	}
}

func TestRefsAndDirections(t *testing.T) {
	src := `
Table users {
  id int [pk, increment]
}
Table orders {
  id int [pk]
  user_id int [ref: > users.id]
  tenant_id int
  code varchar(32)
  indexes {
    (tenant_id, code) [unique, name: 'uq_tenant_code', type: btree]
    (` + "`lower(code)`" + `) [name: 'idx_lower_code']
  }
}
Ref r1: app.orders.(tenant_id, code) > app.tenants.(id, code) [delete: set null, update: cascade]
Ref {
  users.id < orders.user_id
}
`
	db, errs := Parse(src, ParseOptions{})
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(db.Refs) != 3 {
		t.Fatalf("refs = %d, want 3", len(db.Refs))
	}
	r0 := db.Refs[0]
	if r0.Endpoints[0].TableName != "orders" || r0.Endpoints[0].Relation != "*" || r0.Endpoints[1].TableName != "users" || r0.Endpoints[1].Relation != "1" {
		t.Fatalf("inline ref = %+v", r0)
	}
	r1 := db.Refs[1]
	if r1.Name == nil || *r1.Name != "r1" || r1.OnDelete == nil || *r1.OnDelete != "set null" || r1.OnUpdate == nil || *r1.OnUpdate != "cascade" {
		t.Fatalf("composite ref = %+v", r1)
	}
	if len(r1.Endpoints[0].FieldNames) != 2 || *r1.Endpoints[0].SchemaName != "app" {
		t.Fatalf("composite endpoint = %+v", r1.Endpoints[0])
	}
	r2 := db.Refs[2]
	if r2.Endpoints[0].Relation != "1" || r2.Endpoints[1].Relation != "*" {
		t.Fatalf("block ref = %+v", r2)
	}
	orders := db.Tables[1]
	if len(orders.Indexes) != 2 || !orders.Indexes[0].Unique || orders.Indexes[1].Columns[0].Type != "expression" {
		t.Fatalf("indexes = %+v", orders.Indexes)
	}
}

func TestRefsOnly(t *testing.T) {
	_, errs := Parse("Table users { id int }\nRef: users.id > users.id\n", ParseOptions{RefsOnly: true})
	if len(errs) == 0 {
		t.Fatal("expected RefsOnly error")
	}
	if errs[0].Position.Line != 1 || errs[0].Position.Column != 1 {
		t.Fatalf("error pos = %+v, want 1:1", errs[0].Position)
	}
}

func TestTripleQuotedTrailingQuoteAndEscapes(t *testing.T) {
	src := "Table items {\n  id int [pk, note: '''line1\n\\'''quoted\\''' and trailing \\'''']\n}\n"
	db, errs := Parse(src, ParseOptions{})
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	want := "line1\n'''quoted''' and trailing '"
	if got := db.Tables[0].Fields[0].Note; got == nil || got.Value != want {
		t.Fatalf("note = %+v, want %q", got, want)
	}
}

func TestNumbersAndSingleStringNewlineTracking(t *testing.T) {
	src := "Table metrics {\n  a float [default: 1e-5]\n  b int [default: +42]\n  c text [default: 'x\\\ny']\n  d int\n}\n"
	db, errs := Parse(src, ParseOptions{})
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	fields := db.Tables[0].Fields
	if fields[0].DBDefault == nil || fields[0].DBDefault.Type != "number" || fields[0].DBDefault.Value != "1e-5" {
		t.Fatalf("a default = %+v", fields[0].DBDefault)
	}
	if fields[1].DBDefault == nil || fields[1].DBDefault.Type != "number" || fields[1].DBDefault.Value != "+42" {
		t.Fatalf("b default = %+v", fields[1].DBDefault)
	}
	tokens, lexErrs := lex("'x\\\ny'\nident")
	if len(lexErrs) > 0 {
		t.Fatalf("lexErrs: %v", lexErrs)
	}
	lastIdent := tokens[len(tokens)-2]
	if lastIdent.pos.Line != 3 {
		t.Fatalf("line after escaped newline = %d, want 3", lastIdent.pos.Line)
	}
}

func TestComplexColumnTypes(t *testing.T) {
	src := `
Table complex_types {
  a character varying(50) [not null]
  b bit varying(10)
  c interval day(2) to second(6)
  d text[]
  e text[][]
  f character varying(50)[]
}
`
	db, errs := Parse(src, ParseOptions{})
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	fields := db.Tables[0].Fields
	if len(fields) != 6 {
		t.Fatalf("fields = %d, want 6", len(fields))
	}
	if fields[0].Type.TypeName != "character" || fields[0].Type.Suffix != "varying(50)" {
		t.Fatalf("a type = %+v", fields[0].Type)
	}
	if fields[1].Type.TypeName != "bit" || fields[1].Type.Suffix != "varying(10)" {
		t.Fatalf("b type = %+v", fields[1].Type)
	}
	if fields[2].Type.TypeName != "interval" || fields[2].Type.Suffix != "day(2) to second(6)" {
		t.Fatalf("c type = %+v", fields[2].Type)
	}
	if fields[3].Type.TypeName != "text" || fields[3].Type.Suffix != "[]" {
		t.Fatalf("d type = %+v", fields[3].Type)
	}
	if fields[4].Type.TypeName != "text" || fields[4].Type.Suffix != "[][]" {
		t.Fatalf("e type = %+v", fields[4].Type)
	}
	if fields[5].Type.TypeName != "character" || fields[5].Type.Suffix != "varying(50)[]" {
		t.Fatalf("f type = %+v", fields[5].Type)
	}
}

func TestTableHeaderNoteAndSettingsErrors(t *testing.T) {
	src := `Table users [headercolor: blue, note: 'table header note'] { id int [pk] }`
	db, errs := Parse(src, ParseOptions{})
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if db.Tables[0].Note == nil || db.Tables[0].Note.Value != "table header note" {
		t.Fatalf("table note = %+v", db.Tables[0].Note)
	}

	badInputs := []string{
		`Table t [note: 'x' { id int }`,
		`Table t { id int [not foo] }`,
		`Table t { id int [note: ] }`,
		`Table t { id int indexes { id [unique } }`,
		`Ref: a.id > b.id [delete: cascade`,
		`Enum s { a [note: 'x' }`,
	}
	for _, in := range badInputs {
		if _, errs := Parse(in, ParseOptions{}); len(errs) == 0 {
			t.Errorf("expected parse error for %q", in)
		}
	}
}
