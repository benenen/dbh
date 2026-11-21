package erd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
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
