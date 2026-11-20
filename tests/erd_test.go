//go:build e2e

package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestERDPullAndQueries(t *testing.T) {
	f := newFixture(t)
	repo := filepath.Join(f.root, "repo")
	f.dir = filepath.Join(repo, "app", "models")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f.sql(`CREATE TABLE users(id INTEGER PRIMARY KEY, email TEXT NOT NULL UNIQUE);
		CREATE TABLE products(id INTEGER PRIMARY KEY, title TEXT);
		CREATE TABLE orders(id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, product_id INTEGER);
		CREATE TABLE reviews(id INTEGER PRIMARY KEY, order_id INTEGER);`, "--no-history")

	fail := f.fail("erd", "tables")
	contains(t, fail, "no .dbh directory found")

	dbml := f.run("erd", "pull", "demo")
	contains(t, dbml, `Ref: "orders"."user_id" > "users"."id" [delete: cascade]`)
	equal(t, f.run("erd", "pull", "demo", "--save"), "Saved ../../.dbh/schema.dbml\n")
	equal(t, readFile(t, filepath.Join(repo, ".dbh", "schema.dbml")), dbml)

	refs := filepath.Join(repo, ".dbh", "schema.refs.dbml")
	writeFile(t, refs, "// app/models/order.go:9\nRef: orders.product_id > products.id\n// app/models/review.go:4\nRef: reviews.order_id > orders.id\n")
	equal(t, f.run("erd", "check"), "OK: 4 tables, 1 declared and 2 inferred relations\n")

	full := f.run("erd", "relations", "--format", "tsv")
	equal(t, full, f.run("er", "relations", "--format", "tsv"))
	equal(t, full, "name\tfrom_table\tfrom_columns\trelation\tto_table\tto_columns\ton_delete\ton_update\tsource\n"+
		"\torders\tuser_id\t>\tusers\tid\tcascade\t\tdeclared\n"+
		"\torders\tproduct_id\t>\tproducts\tid\t\t\tinferred\n"+
		"\treviews\torder_id\t>\torders\tid\t\t\tinferred\n")
	equal(t, f.run("erd", "relations", "--table", "products", "--source", "inferred", "--format", "csv"),
		"name,from_table,from_columns,relation,to_table,to_columns,on_delete,on_update,source\n,orders,product_id,>,products,id,,,inferred\n")
	equal(t, f.run("erd", "path", "reviews", "users", "--format", "tsv"),
		"step\tfrom_table\tfrom_columns\tto_table\tto_columns\tsource\n1\treviews\torder_id\torders\tid\tinferred\n2\torders\tuser_id\tusers\tid\tdeclared\n")
	contains(t, f.fail("erd", "path", "reviews", "users", "--declared-only"), "no relation path from reviews to users")
	equal(t, f.run("erd", "tables", "--format", "json"), `{"table":"orders","columns":3,"relations":3,"note":""}
{"table":"products","columns":2,"relations":1,"note":""}
{"table":"reviews","columns":2,"relations":1,"note":""}
{"table":"users","columns":2,"relations":1,"note":""}
`)
	show := f.run("erd", "show", "reviews")
	contains(t, show, "┄┄▶ orders.id")
	excludes(t, show, "\nusers\n")
	contains(t, f.run("erd", "show", "--format", "mermaid"), "orders }o..|| products")
	contains(t, f.run("erd", "status", "--format", "tsv"), "schema\t../../.dbh/schema.dbml\t../../.dbh/schema.refs.dbml\t4\t1\t2\tok\n")

	// Re-pulling keeps the refs file and reports inferred relations that became
	// foreign keys or lost their columns.
	f.sql(`DROP TABLE reviews; CREATE TABLE reviews(id INTEGER PRIMARY KEY);
		CREATE TABLE orders2(id INTEGER PRIMARY KEY, user_id INTEGER, product_id INTEGER REFERENCES products(id));
		INSERT INTO orders2 SELECT * FROM orders; DROP TABLE orders; ALTER TABLE orders2 RENAME TO orders;`, "--no-history")
	cmd := exec.Command(binary, "erd", "pull", "demo", "--save")
	cmd.Env, cmd.Dir = f.environment(), f.dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("pull --save: %v\n%s", err, stderr.String())
	}
	equal(t, stdout.String(), "Saved ../../.dbh/schema.dbml\n")
	equal(t, stderr.String(), "error: ../../.dbh/schema.refs.dbml:4:1: relation refers to unknown column \"reviews.order_id\"\n"+
		"warning: ../../.dbh/schema.refs.dbml:2:1: relation orders.product_id > products.id is already declared in the database; remove it from the refs file\n")
	contains(t, readFile(t, refs), "Ref: reviews.order_id > orders.id")
	contains(t, f.fail("erd", "check"), `ERD model "schema" has 1 error(s)`)
}

func TestERDRejectsInvalidModels(t *testing.T) {
	f := newFixture(t)
	f.dir = f.root
	if err := os.MkdirAll(filepath.Join(f.root, ".dbh"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(f.root, ".dbh", "schema.dbml"), "Table users {\n  id integer [pk]\n}\n")
	writeFile(t, filepath.Join(f.root, ".dbh", "schema.refs.dbml"), "Table extra {\n  id integer\n}\n")
	stderr := f.fail("erd", "tables")
	contains(t, stderr, ".dbh/schema.refs.dbml:1:1: only Ref declarations are allowed in a refs file")
	contains(t, f.fail("erd", "tables", "--model", "../x"), `invalid model name "../x"`)
	contains(t, f.fail("erd", "pull", "demo", "--schema", "main"), "--schema is only supported for PostgreSQL")
	if strings.Contains(readFile(t, filepath.Join(f.root, ".dbh", "schema.dbml")), "extra") {
		t.Fatal("model changed")
	}
}

func TestERDScansAllProjectDirs(t *testing.T) {
	f := newFixture(t)
	repo := filepath.Join(f.root, "monorepo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "svc-a", ".dbh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "svc-b", ".dbh"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "svc-a", ".dbh", "schema.dbml"), `Table orders {
  id integer [pk]
  user_id integer
}
`)
	writeFile(t, filepath.Join(repo, "svc-a", ".dbh", "schema.refs.dbml"), "// cross-service ref\nRef: orders.user_id > users.id\n")
	writeFile(t, filepath.Join(repo, "svc-b", ".dbh", "users.dbml"), `Table users {
  id integer [pk]
  email text
}
`)

	// Run from svc-a; queries still scan every .dbh under the repo root.
	f.dir = filepath.Join(repo, "svc-a")
	equal(t, f.run("erd", "check"), "OK: 2 tables, 0 declared and 1 inferred relations\n")
	equal(t, f.run("erd", "path", "orders", "users", "--format", "tsv"),
		"step\tfrom_table\tfrom_columns\tto_table\tto_columns\tsource\n1\torders\tuser_id\tusers\tid\tinferred\n")
	status := f.run("erd", "status", "--format", "tsv")
	contains(t, status, "schema\t.dbh/schema.dbml\t.dbh/schema.refs.dbml\t1\t0\t1\tok\n")
	contains(t, status, "users\t../svc-b/.dbh/users.dbml\t\t1\t0\t0\tok\n")

	// --dir limits queries to one .dbh directory.
	onlyB := f.run("erd", "tables", "--dir", filepath.Join(repo, "svc-b"), "--format", "tsv")
	contains(t, onlyB, "users\t2\t0\t\n")
	excludes(t, onlyB, "orders")

	// --model filters by model name across directories.
	onlyUsersModel := f.run("erd", "tables", "--model", "users", "--format", "tsv")
	contains(t, onlyUsersModel, "users\t2\t0\t\n")
	excludes(t, onlyUsersModel, "orders")

	// pull --save --dir writes to the specified service's .dbh.
	f.sql(`CREATE TABLE items(id INTEGER PRIMARY KEY);`, "--no-history")
	saved := f.run("erd", "pull", "demo", "--save", "--dir", filepath.Join(repo, "svc-c"))
	contains(t, saved, "svc-c/.dbh/schema.dbml")
	contains(t, readFile(t, filepath.Join(repo, "svc-c", ".dbh", "schema.dbml")), `Table "items"`)
}
