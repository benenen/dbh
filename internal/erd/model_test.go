package erd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benenen/dbh/internal/dbml/introspect"
)

const mainDBML = `Table users {
  id integer [pk]
  email varchar(255) [unique, not null]
}

Table orders {
  id integer [pk]
  user_id integer [not null, ref: > users.id]
  product_id integer
  region text
  zone text
}

Table products {
  id integer [pk]
}

Table billing.invoices {
  id integer [pk]
  order_id integer
}

Table regions {
  region text
  zone text
}

Ref fk_invoice_order: billing.invoices.order_id > orders.id [delete: cascade]
Ref: orders.(region, zone) > regions.(region, zone)
`

func writeModel(t *testing.T, main, refs string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), DirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mainFile, refsFile := ModelFiles(dir, DefaultModel)
	if err := os.WriteFile(mainFile, []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	if refs != "" {
		if err := os.WriteFile(refsFile, []byte(refs), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func load(t *testing.T, main, refs string) *Model {
	t.Helper()
	m, err := Load(writeModel(t, main, refs), DefaultModel)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestLoadSourcesAndNormalization(t *testing.T) {
	m := load(t, mainDBML, `// app/order.go:3
Ref: orders.product_id > products.id
Ref: products.id < orders.product_id
`)
	if len(m.Errors) != 0 {
		t.Fatalf("errors: %v", m.Errors)
	}
	if len(m.Tables) != 5 {
		t.Fatalf("tables = %d", len(m.Tables))
	}
	got := m.RelationRows("", "")
	want := [][]any{
		{"", "orders", "user_id", ">", "users", "id", "", "", "", "declared"},
		{"fk_invoice_order", "billing.invoices", "order_id", ">", "orders", "id", "", "cascade", "", "declared"},
		{"", "orders", "region, zone", ">", "regions", "region, zone", "", "", "", "declared"},
		{"", "orders", "product_id", ">", "products", "id", "", "", "", "inferred"},
	}
	if len(got.Values) != len(want) {
		t.Fatalf("relations = %v", got.Values)
	}
	for i := range want {
		for j := range want[i] {
			if got.Values[i][j] != want[i][j] {
				t.Fatalf("relation %d = %v, want %v", i, got.Values[i], want[i])
			}
		}
	}
	if m.Count(SourceInferred) != 1 || m.Count(SourceDeclared) != 3 {
		t.Fatalf("counts declared=%d inferred=%d", m.Count(SourceDeclared), m.Count(SourceInferred))
	}
}

func TestInferredDuplicateAndInvalid(t *testing.T) {
	m := load(t, mainDBML, `// duplicate written in the other direction
Ref: users.id < orders.user_id
// stale
Ref: orders.coupon_id > coupons.id
Ref: orders.id > coupons.id
`)
	if len(m.Warnings) != 1 || m.Warnings[0].Line != 2 || !strings.Contains(m.Warnings[0].Message, "already declared") {
		t.Fatalf("warnings = %v", m.Warnings)
	}
	if len(m.Errors) != 2 || m.Errors[0].Line != 4 || !strings.Contains(m.Errors[0].Message, `unknown column "orders.coupon_id"`) ||
		m.Errors[1].Line != 5 || !strings.Contains(m.Errors[1].Message, `unknown table "coupons"`) {
		t.Fatalf("errors = %v", m.Errors)
	}
	if err := m.Usable(); err != nil {
		t.Fatalf("invalid inferred relations must not block queries: %v", err)
	}
	if m.Count(SourceInferred) != 0 {
		t.Fatalf("inferred = %d", m.Count(SourceInferred))
	}
}

func TestRefsFileOnlyAllowsRefs(t *testing.T) {
	m := load(t, mainDBML, "Ref: orders.product_id > products.id\n\nTable extra {\n  id integer\n}\n")
	if err := m.Usable(); err == nil || !strings.Contains(err.Error(), "only Ref declarations") || !strings.Contains(err.Error(), ":3:1:") {
		t.Fatalf("Usable() = %v", err)
	}
}

func TestMainFileErrorsBlockQueries(t *testing.T) {
	m := load(t, "Table users {\n  id integer [pk\n}\n", "")
	if err := m.Usable(); err == nil || !strings.Contains(err.Error(), "schema.dbml:") {
		t.Fatalf("Usable() = %v", err)
	}
}

func TestLookup(t *testing.T) {
	m := load(t, mainDBML+"Table audit.orders {\n  id integer\n}\n", "")
	if got, err := m.Lookup("invoices"); err != nil || got != "billing.invoices" {
		t.Fatalf("Lookup(invoices) = %q, %v", got, err)
	}
	if got, err := m.Lookup("orders"); err != nil || got != "orders" {
		t.Fatalf("exact display name wins: %q, %v", got, err)
	}
	if _, err := m.Lookup("ORDERS"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("Lookup(ORDERS) = %v", err)
	}
	if _, err := m.Lookup("nope"); err == nil {
		t.Fatal("unknown table must fail")
	}
}

func TestPath(t *testing.T) {
	m := load(t, mainDBML, "Ref: billing.invoices.order_id > products.id\nRef: products.id > users.id\n")
	steps, err := m.Path("billing.invoices", "users", false)
	if err != nil {
		t.Fatal(err)
	}
	rows := PathRows(steps)
	if len(rows.Values) != 2 || rows.Values[0][3] != "orders" || rows.Values[0][6] != SourceDeclared ||
		rows.Values[1][2] != "user_id" || rows.Values[1][4] != "id" {
		t.Fatalf("path = %v", rows.Values)
	}
	if _, err := m.Path("products", "users", true); err == nil {
		t.Fatal("declared-only path must ignore inferred relations")
	}
	steps, err = m.Path("users", "orders", false)
	if err != nil || steps[0].FromColumns[0] != "id" || steps[0].ToColumns[0] != "user_id" {
		t.Fatalf("reverse step columns = %+v, %v", steps, err)
	}
}

func TestNeighborhoodAndRender(t *testing.T) {
	m := load(t, mainDBML, "// evidence\nRef: orders.product_id > products.id\n")
	include := m.Neighborhood([]string{"products"}, 1)
	if !include["orders"] || include["users"] {
		t.Fatalf("neighborhood = %v", include)
	}
	var text, dot, mermaid strings.Builder
	if err := RenderText(&text, m, include); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"(region, zone)", "┄┄▶ products.id", "◀┄┄ orders.product_id", "──▶ users.id"} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("text missing %q:\n%s", want, text.String())
		}
	}
	if strings.Contains(text.String(), " \n") {
		t.Fatalf("text has trailing spaces:\n%q", text.String())
	}
	if err := RenderDot(&dot, m, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dot.String(), `"orders":c2 -> "products":c0 [style=dashed`) {
		t.Fatalf("dot:\n%s", dot.String())
	}
	if err := RenderMermaid(&mermaid, m, nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`billing_invoices["billing.invoices"] {`, "varchar(255) email UK", `orders }o..|| products : "product_id"`, `billing_invoices }o--|| orders`} {
		if !strings.Contains(mermaid.String(), want) {
			t.Fatalf("mermaid missing %q:\n%s", want, mermaid.String())
		}
	}
}

func TestFindDir(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	nested := filepath.Join(repo, "a", "b")
	initGitDir(t, repo)
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	// A .dbh above the repository root is not used.
	if err := os.MkdirAll(filepath.Join(root, DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	dir, create, err := FindDir(nested)
	if err != nil || dir != "" || create != filepath.Join(repo, DirName) {
		t.Fatalf("FindDir = %q, %q, %v", dir, create, err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "a", DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	dir, _, err = FindDir(nested)
	if err != nil || dir != filepath.Join(repo, "a", DirName) {
		t.Fatalf("FindDir = %q, %v", dir, err)
	}
}

func TestModelNamesAndList(t *testing.T) {
	for _, bad := range []string{"", ".hidden", "../x", "a/b", "schema.refs"} {
		if ValidModelName(bad) == nil {
			t.Fatalf("%q must be invalid", bad)
		}
	}
	dir := writeModel(t, mainDBML, "Ref: orders.product_id > products.id\n")
	if err := os.WriteFile(filepath.Join(dir, "reporting.dbml"), []byte("Table t {\n  id int\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	names, err := List(dir)
	if err != nil || strings.Join(names, ",") != "reporting,schema" {
		t.Fatalf("List = %v, %v", names, err)
	}
	if _, err := Load(dir, "missing"); err == nil || !strings.Contains(err.Error(), "erd pull NAME --save") {
		t.Fatalf("Load(missing) = %v", err)
	}
}

func TestSaveReplacesAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), DirName, "schema.dbml")
	for _, content := range []string{"Table a {\n  id int\n}\n", "Table b {\n  id int\n}\n"} {
		if err := Save(path, content); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "Table b") {
		t.Fatalf("saved = %q, %v", data, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temporary files left: %v", entries)
	}
}

// Columns named like DBML keywords survive parsing (patched dbml-tools fork).
func TestKeywordNamedColumns(t *testing.T) {
	m := load(t, "Table orders {\n  \"id\" integer [pk]\n  \"note\" TEXT\n  \"records\" int\n  Note: 'orders table'\n}\n", "")
	if err := m.Usable(); err != nil {
		t.Fatal(err)
	}
	columns := m.Tables[0].Columns
	if len(columns) != 3 || columns[1].Name != "note" || columns[2].Name != "records" || m.Tables[0].Note != "orders table" {
		t.Fatalf("table = %+v", m.Tables[0])
	}
}

func TestFindAllDirs(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	for _, p := range []string{
		filepath.Join(root, DirName),
		filepath.Join(repo, ".git"),
		filepath.Join(repo, DirName),
		filepath.Join(repo, "svc-a", DirName),
		filepath.Join(repo, "svc-b", "nested", DirName),
		filepath.Join(repo, "keep", DirName),
		filepath.Join(repo, "data", "raw", DirName),
		filepath.Join(repo, ".cache", "sub", DirName),
		filepath.Join(repo, "node_modules", "pkg", DirName),
		filepath.Join(repo, "vendor", "mod", DirName),
		filepath.Join(repo, "d1", "d2", "d3", "d4", "d5", "d6", "d7", "d8", "d9", DirName),
	} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	initGitDir(t, repo)
	// .gitignore prunes data/ and honors !keep/, but never hides .dbh itself.
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".dbh/\ndata/\nkeep/\n!keep/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Calling from a nested directory still scans every .dbh under the repo root.
	got, err := FindAllDirs(filepath.Join(repo, "svc-a"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(repo, DirName),
		filepath.Join(repo, "keep", DirName),
		filepath.Join(repo, "svc-a", DirName),
		filepath.Join(repo, "svc-b", "nested", DirName),
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("FindAllDirs = %v, want %v", got, want)
	}
}

func TestLoadAllMergesModelsAndCrossDirRefs(t *testing.T) {
	root := t.TempDir()
	dirA := filepath.Join(root, "svc-a", DirName)
	dirB := filepath.Join(root, "svc-b", DirName)
	if err := os.MkdirAll(dirA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dirB, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirA, "schema.dbml"), []byte(`Table users {
  id integer [pk]
  email text
}
Table orders {
  id integer [pk]
  user_id integer [ref: > users.id]
  account_id integer
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Cross-.dbh inferred relation pointing to a table defined in svc-b.
	if err := os.WriteFile(filepath.Join(dirA, "schema.refs.dbml"), []byte("Ref: orders.account_id > accounts.id\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "billing.dbml"), []byte(`Table users {
  id integer [pk]
  billing_tier text
  Note: 'billing user view'
}
Table accounts {
  id integer [pk]
  owner_id integer [ref: > users.id]
}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := LoadAll([]string{dirA, dirB}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Usable(); err != nil {
		t.Fatalf("Usable() = %v", err)
	}
	if len(m.Errors) != 0 {
		t.Fatalf("errors = %v", m.Errors)
	}
	if len(m.Tables) != 3 {
		t.Fatalf("tables = %d, want 3 (users merged)", len(m.Tables))
	}
	// Merged users table must have id, email, billing_tier and the note.
	var users Table
	for _, tbl := range m.Tables {
		if tbl.Name == "users" {
			users = tbl
		}
	}
	if len(users.Columns) != 3 || users.Note != "billing user view" {
		t.Fatalf("merged users = %+v", users)
	}
	// users differs between the models, so the merge must be flagged.
	if msg, ok := m.TableConflict("users"); !ok || !strings.Contains(msg, "svc-a/schema, svc-b/billing") {
		t.Fatalf("users conflict = %q, %v", msg, ok)
	}
	if len(m.Warnings) != 1 || !strings.Contains(m.Warnings[0].String(), "table users has different columns") {
		t.Fatalf("warnings = %v", m.Warnings)
	}
	if _, ok := m.TableConflict("orders"); ok {
		t.Fatal("orders is defined once and must not conflict")
	}
	// accounts exists only in svc-b, so it is foreign when svc-a is local.
	if msg, ok := m.ForeignTable("accounts", dirA); !ok || !strings.Contains(msg, "only in another project's models svc-b/billing") {
		t.Fatalf("accounts foreign = %q, %v", msg, ok)
	}
	for _, name := range []string{"orders", "users"} {
		if msg, ok := m.ForeignTable(name, dirA); ok {
			t.Fatalf("%s is defined in svc-a but reported foreign: %q", name, msg)
		}
	}
	if _, ok := m.ForeignTable("accounts", ""); ok {
		t.Fatal("no local .dbh must disable the foreign check")
	}
	if m.Count(SourceDeclared) != 2 || m.Count(SourceInferred) != 1 {
		t.Fatalf("declared=%d inferred=%d", m.Count(SourceDeclared), m.Count(SourceInferred))
	}
	steps, err := m.Path("orders", "accounts", false)
	if err != nil || len(steps) != 1 || steps[0].FromColumns[0] != "account_id" {
		t.Fatalf("cross-dir path = %+v, %v", steps, err)
	}

	// Filtering by model name loads only matching models.
	onlyBilling, err := LoadAll([]string{dirA, dirB}, "billing")
	if err != nil {
		t.Fatal(err)
	}
	if len(onlyBilling.Tables) != 2 {
		t.Fatalf("billing tables = %d, want 2", len(onlyBilling.Tables))
	}

	// StatusRows validates cross-.dbh inferred relations against the combined table set.
	statusRows, err := StatusRows([]string{dirA, dirB}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(statusRows.Values) != 2 {
		t.Fatalf("status rows = %v", statusRows.Values)
	}
	// svc-a schema: 2 tables, 1 declared, 1 cross-.dbh inferred, ok.
	if statusRows.Values[0][3] != 2 || statusRows.Values[0][4] != 1 || statusRows.Values[0][5] != 1 || statusRows.Values[0][6] != "ok" {
		t.Fatalf("svc-a status = %v", statusRows.Values[0])
	}
	// svc-b billing: 2 tables, 1 declared, 0 inferred, ok.
	if statusRows.Values[1][3] != 2 || statusRows.Values[1][4] != 1 || statusRows.Values[1][5] != 0 || statusRows.Values[1][6] != "ok" {
		t.Fatalf("svc-b status = %v", statusRows.Values[1])
	}
}

func TestConditionalRelations(t *testing.T) {
	schema := `Table events {
  id integer [pk]
  a integer
  b integer
}

Table c {
  id integer [pk]
}

Table d {
  id integer [pk]
}
`
	refs := `// app/models/event.go:12
// when: a = 1
Ref: events.b > c.id

// app/models/event.go:15
Ref: events.b > d.id // when: a = 2

// distinct condition on the same endpoints is preserved
// WHEN: a = 3
// when: b > 0
Ref: events.b > c.id
`
	m := load(t, schema, refs)
	if len(m.Errors) != 0 || len(m.Warnings) != 0 {
		t.Fatalf("errors=%v warnings=%v", m.Errors, m.Warnings)
	}
	got := m.RelationRows("events", "")
	want := [][]any{
		{"", "events", "b", ">", "c", "id", "a = 1", "", "", "inferred"},
		{"", "events", "b", ">", "d", "id", "a = 2", "", "", "inferred"},
		{"", "events", "b", ">", "c", "id", "a = 3 AND b > 0", "", "", "inferred"},
	}
	if len(got.Values) != len(want) {
		t.Fatalf("relations = %v, want %v", got.Values, want)
	}
	for i := range want {
		for j := range want[i] {
			if got.Values[i][j] != want[i][j] {
				t.Fatalf("relation %d = %v, want %v", i, got.Values[i], want[i])
			}
		}
	}

	steps, err := m.Path("events", "d", false)
	if err != nil {
		t.Fatal(err)
	}
	pathRows := PathRows(steps)
	if len(pathRows.Values) != 1 || pathRows.Values[0][5] != "a = 2" || pathRows.Values[0][6] != SourceInferred {
		t.Fatalf("path = %v", pathRows.Values)
	}

	var text, dot, mermaid strings.Builder
	if err := RenderText(&text, m, nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"┄┄▶ c.id", "when a = 1, inferred", "┄┄▶ d.id", "when a = 2, inferred"} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("text missing %q:\n%s", want, text.String())
		}
	}
	if err := RenderDot(&dot, m, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dot.String(), `label="when a = 1, inferred"`) {
		t.Fatalf("dot:\n%s", dot.String())
	}
	if err := RenderMermaid(&mermaid, m, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mermaid.String(), `events }o..|| d : "b (when a = 2)"`) {
		t.Fatalf("mermaid:\n%s", mermaid.String())
	}
	var htmlOut strings.Builder
	if err := RenderHTML(&htmlOut, m, nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<!DOCTYPE html>", "<title>ERD - schema</title>", `"condition":"a = 1"`, `"condition":"a = 2"`, `"name":"events"`, `id="erd-data"`} {
		if !strings.Contains(htmlOut.String(), want) {
			t.Fatalf("html missing %q", want)
		}
	}
}

func TestConditionalCommentIsolation(t *testing.T) {
	schema := `Table t {
  id integer [pk]
  a integer
  b integer
}
Table c {
  id integer [pk]
}
`
	// Blank line separates a stray `// when:` comment from the following Ref.
	refs := `// when: a = 99

// plain evidence comment
Ref: t.b > c.id
`
	m := load(t, schema, refs)
	if len(m.Relations) != 1 || m.Relations[0].Condition != "" {
		t.Fatalf("relations = %+v", m.Relations)
	}
}

func BenchmarkFindAllDirs(b *testing.B) {
	repo := filepath.Join(b.TempDir(), "monorepo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	// Simulate a large monorepo: 50 services x 20 subdirs = 1,000+ directories, plus ignored trees.
	for i := 0; i < 50; i++ {
		svc := filepath.Join(repo, fmt.Sprintf("svc-%02d", i))
		if i%10 == 0 {
			if err := os.MkdirAll(filepath.Join(svc, DirName), 0o755); err != nil {
				b.Fatal(err)
			}
		}
		for j := 0; j < 20; j++ {
			if err := os.MkdirAll(filepath.Join(svc, fmt.Sprintf("pkg-%02d", j)), 0o755); err != nil {
				b.Fatal(err)
			}
		}
	}
	for i := 0; i < 200; i++ {
		if err := os.MkdirAll(filepath.Join(repo, "node_modules", fmt.Sprintf("dep-%03d", i)), 0o755); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dirs, err := FindAllDirs(repo)
		if err != nil || len(dirs) != 5 {
			b.Fatalf("FindAllDirs = %d, %v", len(dirs), err)
		}
	}
}

func TestSanitizeDBMLNotes(t *testing.T) {
	// Simulated dbml-tools output with Go-quoted double quotes and newlines.
	raw := "Table \"items\" {\n" +
		"  \"id\" int [pk, note: \"primary \\\"id\\\" field\"]\n" +
		"  \"status\" varchar(20) [not null, default: 'open', note: \"line1\\nline2 with 'quote' and \\\"dbl\\\"\"]\n" +
		"\n  Note: \"table \\\"items\\\" comment\"\n" +
		"}\n"
	sanitized := sanitizeDBMLNotes(raw)
	m := ParseDBML("schema", "schema.dbml", sanitized)
	if len(m.Errors) != 0 {
		t.Fatalf("sanitized DBML failed to parse: %v\n%s", m.Errors, sanitized)
	}
	if len(m.Tables) != 1 {
		t.Fatalf("tables = %d", len(m.Tables))
	}
	tbl := m.Tables[0]
	if tbl.Note != `table "items" comment` {
		t.Fatalf("table note = %q", tbl.Note)
	}
	if tbl.Columns[0].Note == nil || tbl.Columns[0].Note.Value != `primary "id" field` {
		t.Fatalf("col0 note = %+v", tbl.Columns[0].Note)
	}
	if tbl.Columns[1].Note == nil || !strings.Contains(tbl.Columns[1].Note.Value, "line1\nline2") || !strings.Contains(tbl.Columns[1].Note.Value, `"dbl"`) {
		t.Fatalf("col1 note = %+v", tbl.Columns[1].Note)
	}
}

func TestStandaloneRefsFileAndValidateModelRefs(t *testing.T) {
	root := t.TempDir()
	dirA := filepath.Join(root, "svc-a", DirName)
	dirB := filepath.Join(root, "svc-b", DirName)
	dirRoot := filepath.Join(root, DirName)
	for _, d := range []string{dirA, dirB, dirRoot} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dirA, "schema.dbml"), []byte("Table orders {\n  id int [pk]\n  user_id int\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirA, "schema.refs.dbml"), []byte("Ref: orders.user_id > users.id\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "users.dbml"), []byte("Table users {\n  id int [pk]\n  org_id int\n}\nTable orgs {\n  id int [pk]\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Standalone .refs.dbml without a matching .dbml file.
	if err := os.WriteFile(filepath.Join(dirRoot, "cross.refs.dbml"), []byte("Ref: users.org_id > orgs.id\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	refsOnly, err := ListRefsOnly(dirRoot)
	if err != nil || strings.Join(refsOnly, ",") != "cross" {
		t.Fatalf("ListRefsOnly = %v, %v", refsOnly, err)
	}
	allDirs := []string{dirRoot, dirA, dirB}
	m, err := LoadAll(allDirs, "")
	if err != nil || len(m.Errors) != 0 {
		t.Fatalf("LoadAll err=%v errors=%v", err, m.Errors)
	}
	if m.Count(SourceInferred) != 2 {
		t.Fatalf("inferred = %d, want 2", m.Count(SourceInferred))
	}

	// ValidateModelRefs validates svc-a's cross-.dbh ref without false unknown-table errors.
	validated, err := ValidateModelRefs(dirA, "schema", allDirs)
	if err != nil || len(validated.Errors) != 0 {
		t.Fatalf("ValidateModelRefs err=%v errors=%v", err, validated.Errors)
	}

	status, err := StatusRows(allDirs, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Values) != 3 {
		t.Fatalf("status rows = %v", status.Values)
	}
	// Standalone cross.refs.dbml appears with empty main file, 0 tables, 1 inferred, ok.
	if status.Values[0][0] != "cross" || status.Values[0][1] != "" || status.Values[0][5] != 1 || status.Values[0][6] != "ok" {
		t.Fatalf("standalone status row = %v", status.Values[0])
	}
}

func TestAllPathsAndParallelEdges(t *testing.T) {
	schema := `Table tasks {
  id int [pk]
  creator_id int [ref: > users.id]
  assignee_id int [ref: > users.id]
}
Table users {
  id int [pk]
}
`
	m := load(t, schema, "")
	paths, err := m.AllPaths("tasks", "users", false, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("paths = %d, want 2: %+v", len(paths), paths)
	}
	rows := PathRowsMulti(paths)
	if len(rows.Values) != 2 || rows.Values[0][0] != 1 || rows.Values[1][0] != 2 {
		t.Fatalf("PathRowsMulti = %v", rows.Values)
	}
}

func TestTableRowsFiltered(t *testing.T) {
	schema := `Table users {
  id int [pk]
  Note: 'account holders'
}
Table orders {
  id int [pk]
  user_id int [ref: > users.id]
}
Table audit_logs {
  id int [pk]
  Note: 'standalone audit trail'
}
`
	m := load(t, schema, "")
	iso := m.TableRowsFiltered("", true, false)
	if len(iso.Values) != 1 || iso.Values[0][0] != "audit_logs" {
		t.Fatalf("isolated = %v", iso.Values)
	}
	matched := m.TableRowsFiltered("account", false, true)
	if len(matched.Values) != 1 || matched.Values[0][0] != "users" || len(matched.Columns) != 5 || matched.Columns[4] != "models" {
		t.Fatalf("matched = %v cols=%v", matched.Values, matched.Columns)
	}
	globbed := m.TableRowsFiltered("audit_*", false, false)
	if len(globbed.Values) != 1 || globbed.Values[0][0] != "audit_logs" {
		t.Fatalf("globbed = %v", globbed.Values)
	}
}

func TestRenderTextNotesDefaultsIndexesAndFilterSource(t *testing.T) {
	schema := `Table orders {
  id int [pk]
  status varchar(20) [not null, default: 'open', note: 'order lifecycle']
  user_id int [ref: > users.id]
  product_id int

  indexes {
    (user_id, status) [name: 'idx_user_status']
  }

  Note: 'customer orders'
}
Table users {
  id int [pk]
}
Table products {
  id int [pk]
}
`
	m := load(t, schema, "Ref: orders.product_id > products.id\n")
	var text strings.Builder
	if err := RenderText(&text, m, map[string]bool{"orders": true}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"orders  # customer orders",
		"default 'open'",
		"# order lifecycle",
		"indexes:",
		"(user_id, status) [idx_user_status]",
	} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("RenderText missing %q:\n%s", want, text.String())
		}
	}

	declaredView := m.FilterSource(SourceDeclared)
	if len(declaredView.Relations) != 1 || declaredView.Relations[0].Source != SourceDeclared {
		t.Fatalf("FilterSource(declared) = %+v", declaredView.Relations)
	}
	nb := m.NeighborhoodFiltered([]string{"orders"}, 1, SourceDeclared)
	if !nb["users"] || nb["products"] {
		t.Fatalf("NeighborhoodFiltered(declared) = %v", nb)
	}

	var htmlOut strings.Builder
	if err := RenderHTML(&htmlOut, m, nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"note":"order lifecycle"`,
		`"default":"'open'"`,
		`"indexes":["(user_id, status) [idx_user_status]"]`,
		`bgRect.setAttribute('fill', 'var(--bg)')`,
	} {
		if !strings.Contains(htmlOut.String(), want) {
			t.Fatalf("RenderHTML missing %q", want)
		}
	}
}

func TestDiffModels(t *testing.T) {
	oldM := ParseDBML("schema", "old.dbml", `Table users {
  id int [pk]
  name varchar(50)
}
Table legacy {
  id int [pk]
}
`)
	newM := ParseDBML("schema", "new.dbml", `Table users {
  id int [pk]
  name varchar(100)
  email text [not null]
}
Table orders {
  id int [pk]
  user_id int [ref: > users.id]
}
`)
	diff := DiffModels(oldM, newM)
	if !diff.HasChanges() {
		t.Fatal("expected changes")
	}
	if diff.TablesAdded != 1 || diff.TablesRemoved != 1 || diff.TablesModified != 1 || diff.RelationsAdded != 1 {
		t.Fatalf("diff counts = %+v", diff)
	}
	wantSummary := "+1 table, -1 table, ~1 modified, +1 declared ref"
	if diff.Summary() != wantSummary {
		t.Fatalf("Summary() = %q, want %q", diff.Summary(), wantSummary)
	}
	same := DiffModels(newM, newM)
	if same.HasChanges() || same.Summary() != "no schema changes" {
		t.Fatalf("same diff = %+v", same)
	}
}

func TestFilterDBSchema(t *testing.T) {
	s := &introspect.DBSchema{
		Tables: []*introspect.Table{
			{Name: "users"},
			{Name: "orders"},
			{Name: "orders_bak"},
		},
		FKs: []*introspect.ForeignKey{
			{TableName: "orders", Columns: []string{"user_id"}, RefTable: "users", RefColumns: []string{"id"}},
			{TableName: "orders_bak", Columns: []string{"user_id"}, RefTable: "users", RefColumns: []string{"id"}},
		},
	}
	filterDBSchema(s, nil, []string{"*_bak"})
	if len(s.Tables) != 2 || len(s.FKs) != 1 {
		t.Fatalf("filtered tables=%d fks=%d", len(s.Tables), len(s.FKs))
	}
}

func TestLoadAllIdenticalTablesDoNotConflict(t *testing.T) {
	root := t.TempDir()
	schema := []byte("Table users {\n  id integer [pk]\n  email text\n}\n")
	var dirs []string
	for _, svc := range []string{"svc-a", "svc-b"} {
		dir := filepath.Join(root, svc, DirName)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "schema.dbml"), schema, 0o644); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, dir)
	}
	m, err := LoadAll(dirs, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.TableConflict("users"); ok || len(m.Warnings) != 0 {
		t.Fatalf("identical copies flagged: warnings = %v", m.Warnings)
	}
}

func TestSameColumnsComparesTypes(t *testing.T) {
	m := ParseDBML("a", "a.dbml", "Table t {\n  id integer\n}\nTable u {\n  id bigint\n}\n")
	if len(m.Errors) != 0 {
		t.Fatal(m.Errors)
	}
	if sameColumns(m.Tables[0].Columns, m.Tables[1].Columns) {
		t.Fatal("integer and bigint columns compared equal")
	}
	if !sameColumns(m.Tables[0].Columns, m.Tables[0].Columns) {
		t.Fatal("identical columns compared different")
	}
}

func TestRefEvidenceComments(t *testing.T) {
	schema := `Table t {
  id integer [pk]
  a integer
  b integer
}
Table c {
  id integer [pk]
}
`
	refs := `// header comment

// app/mapper/T.xml:12 (SQL JOIN)
// when: a = 1
Ref: t.b > c.id
Ref: t.a > c.id // app/models/t.go:8 (GORM foreignKey)
`
	m := load(t, schema, refs)
	if len(m.Relations) != 2 {
		t.Fatalf("relations = %+v", m.Relations)
	}
	if r := m.Relations[0]; r.Condition != "a = 1" || r.Evidence != "app/mapper/T.xml:12 (SQL JOIN)" {
		t.Fatalf("first relation = %+v", r)
	}
	if r := m.Relations[1]; r.Condition != "" || r.Evidence != "app/models/t.go:8 (GORM foreignKey)" {
		t.Fatalf("second relation = %+v", r)
	}
	rows := m.RelationRows("c", SourceInferred)
	if last := len(rows.Columns) - 1; rows.Columns[last] != "evidence" || rows.Values[0][last] != "app/mapper/T.xml:12 (SQL JOIN)" {
		t.Fatalf("relation rows = %v %v", rows.Columns, rows.Values)
	}
	steps, err := m.Path("t", "c", false)
	if err != nil {
		t.Fatal(err)
	}
	if path := PathRows(steps); path.Columns[len(path.Columns)-1] != "evidence" || path.Values[0][len(path.Columns)-1] == "" {
		t.Fatalf("path rows = %v %v", path.Columns, path.Values)
	}
}

// initGitDir marks dir as a Git work tree root with a minimal .git/HEAD.
func initGitDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestProjectRootIgnoresStrayGitDir(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initGitDir(t, repo)
	// A .git directory without HEAD (for example only .git/info/exclude) is not a repository.
	svc := filepath.Join(repo, "svc")
	if err := os.MkdirAll(filepath.Join(svc, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if root, err := ProjectRoot(svc); err != nil || root != repo {
		t.Fatalf("ProjectRoot(stray) = %q, %v; want %q", root, err, repo)
	}
	if _, create, err := FindDir(svc); err != nil || create != filepath.Join(repo, DirName) {
		t.Fatalf("FindDir(stray) create = %q, %v", create, err)
	}
	// A .git file (worktree or submodule pointer) marks a repository root.
	worktree := filepath.Join(repo, "wt")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if root, err := ProjectRoot(worktree); err != nil || root != worktree {
		t.Fatalf("ProjectRoot(worktree) = %q, %v; want %q", root, err, worktree)
	}
}

func TestRefEvidenceDirective(t *testing.T) {
	schema := `Table t {
  id integer [pk]
  kind integer
  ref_id integer
}
Table a {
  id integer [pk]
}
`
	refs := `// t.ref_id points to a different table per kind
// Evidence: app/A.java:10 (provider)
// evidence: app/T.xml:5 (SQL JOIN)
// when: t.kind = 1
Ref: t.ref_id > a.id
`
	m := load(t, schema, refs)
	if len(m.Relations) != 1 {
		t.Fatalf("relations = %+v", m.Relations)
	}
	if r := m.Relations[0]; r.Condition != "t.kind = 1" || r.Evidence != "app/A.java:10 (provider); app/T.xml:5 (SQL JOIN)" {
		t.Fatalf("relation = %+v", r)
	}
}

func TestColumnRows(t *testing.T) {
	m := ParseDBML("app", "app.dbml", `Table users {
  id integer [pk]
  email varchar(255) [not null, unique, note: 'login email']
  status int [default: 1]
}
Table orders {
  id integer [pk]
  user_id integer [note: 'buyer']
}
`)
	if len(m.Errors) != 0 {
		t.Fatal(m.Errors)
	}
	all := m.ColumnRows(nil, "", false)
	if len(all.Values) != 5 || all.Columns[len(all.Columns)-1] != "note" {
		t.Fatalf("all columns = %v %v", all.Columns, all.Values)
	}
	email := m.ColumnRows([]string{"users"}, "EMAIL", true)
	want := []any{"users", "email", "varchar(255)", false, true, true, "", "login email", "app"}
	if len(email.Values) != 1 || fmt.Sprint(email.Values[0]) != fmt.Sprint(want) {
		t.Fatalf("email = %v", email.Values)
	}
	if got := m.ColumnRows(nil, "*_id", false); len(got.Values) != 1 || got.Values[0][1] != "user_id" {
		t.Fatalf("glob = %v", got.Values)
	}
	if got := m.ColumnRows(nil, "buyer", false); len(got.Values) != 1 || got.Values[0][0] != "orders" {
		t.Fatalf("note match = %v", got.Values)
	}
	if got := m.ColumnRows([]string{"users"}, "status", false); got.Values[0][6] != "1" || got.Values[0][4] != false {
		t.Fatalf("default = %v", got.Values)
	}
}

func TestRenderColumnNotesInDotMermaidHTML(t *testing.T) {
	m := ParseDBML("app", "app.dbml", `Table items {
  id int [pk, note: 'primary "id" & <key>']
  code varchar(32) [unique, note: '''line1
line2''']
  plain text
}
`)
	if len(m.Errors) != 0 {
		t.Fatal(m.Errors)
	}

	var dot strings.Builder
	if err := RenderDot(&dot, m, nil); err != nil {
		t.Fatal(err)
	}
	dotStr := dot.String()
	for _, want := range []string{
		`id int [pk] <font color="#666666"># primary &quot;id&quot; &amp; &lt;key&gt;</font>`,
		`code varchar(32) [unique] <font color="#666666"># line1 line2</font>`,
		`<tr><td port="c2" align="left">plain text</td></tr>`,
	} {
		if !strings.Contains(dotStr, want) {
			t.Fatalf("RenderDot missing %q:\n%s", want, dotStr)
		}
	}

	var mermaid strings.Builder
	if err := RenderMermaid(&mermaid, m, nil); err != nil {
		t.Fatal(err)
	}
	mermaidStr := mermaid.String()
	for _, want := range []string{
		`int id PK "primary 'id' & <key>"`,
		`varchar(32) code UK "line1 line2"`,
		"    text plain\n",
	} {
		if !strings.Contains(mermaidStr, want) {
			t.Fatalf("RenderMermaid missing %q:\n%s", want, mermaidStr)
		}
	}

	var htmlOut strings.Builder
	if err := RenderHTML(&htmlOut, m, nil); err != nil {
		t.Fatal(err)
	}
	htmlStr := htmlOut.String()
	for _, want := range []string{
		`"note":"line1 line2"`,
		`const NOTE_H = 16;`,
		`colLayout.set(c.name`,
		`const noteFont = '10.5px -apple-system, sans-serif';`,
	} {
		if !strings.Contains(htmlStr, want) {
			t.Fatalf("RenderHTML missing %q", want)
		}
	}
}
