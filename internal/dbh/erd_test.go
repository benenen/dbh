package dbh

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestERDPullSQLite(t *testing.T) {
	ctx := context.Background()
	p := Profile{Name: "test", Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "erd.db")}
	s, err := openSession(ctx, p, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Run(ctx, "CREATE TABLE users(id INTEGER PRIMARY KEY); CREATE TABLE orders(id INTEGER PRIMARY KEY, user_id INTEGER REFERENCES users(id));", "json", io.Discard)
	s.Close()
	if err != nil {
		t.Fatal(err)
	}
	dbml, err := pullDBML(ctx, p, "", false, "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dbml, `Ref: "orders"."user_id" > "users"."id"`) {
		t.Fatalf("unexpected DBML:\n%s", dbml)
	}
}

func TestERDShowHTMLOpen(t *testing.T) {
	dir := t.TempDir()
	dbhDir := filepath.Join(dir, ".dbh")
	if err := os.MkdirAll(dbhDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbhDir, "schema.dbml"), []byte("Table users {\n  id integer [pk]\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	origOpen := openBrowser
	t.Cleanup(func() { openBrowser = origOpen })

	var opened []string
	openBrowser = func(target string) error {
		opened = append(opened, target)
		return nil
	}

	runShow := func(args ...string) (string, error) {
		cmd := NewCommand()
		var out, errBuf bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errBuf)
		cmd.SetArgs(append([]string{"erd", "show", "--dir", dbhDir}, args...))
		err := cmd.Execute()
		return out.String(), err
	}

	// --format html --open writes a temp HTML file and opens it.
	out, err := runShow("--format", "html", "--open")
	if err != nil {
		t.Fatalf("show --format html --open: %v", err)
	}
	if len(opened) != 1 || !strings.HasSuffix(opened[0], ".html") || !strings.HasPrefix(out, "Opened ") {
		t.Fatalf("opened=%v out=%q", opened, out)
	}
	t.Cleanup(func() { _ = os.Remove(opened[0]) })
	data, err := os.ReadFile(opened[0])
	if err != nil || !strings.Contains(string(data), "<!DOCTYPE html>") || !strings.Contains(string(data), `"name":"users"`) {
		t.Fatalf("temp html invalid: err=%v len=%d", err, len(data))
	}

	// --open with -o saves to the specified path and opens it.
	explicit := filepath.Join(dir, "diagram.html")
	if _, err := runShow("--open", "-o", explicit); err != nil {
		t.Fatalf("show --open -o: %v", err)
	}
	if len(opened) != 2 || opened[1] != explicit {
		t.Fatalf("opened=%v, want %s", opened, explicit)
	}

	// --open with non-html format fails clearly.
	if _, err := runShow("--format", "mermaid", "--open"); err == nil || !strings.Contains(err.Error(), "--open is only supported with --format html") {
		t.Fatalf("expected error for mermaid --open, got %v", err)
	}
}

func TestERDWarnsOnConflictingTable(t *testing.T) {
	dbhDir := filepath.Join(t.TempDir(), ".dbh")
	if err := os.MkdirAll(dbhDir, 0o755); err != nil {
		t.Fatal(err)
	}
	models := map[string]string{
		"resource.dbml": "Table notice {\n  id bigint [pk]\n  title varchar(50)\n}\nTable users {\n  id bigint [pk]\n  notice_id bigint [ref: > notice.id]\n}\n",
		"portal.dbml":   "Table notice {\n  id bigint [pk]\n  notice_title varchar(50)\n}\n",
	}
	for name, src := range models {
		if err := os.WriteFile(filepath.Join(dbhDir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) (string, string, error) {
		cmd := NewCommand()
		var out, errBuf bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errBuf)
		cmd.SetArgs(append([]string{"erd", "--dir", dbhDir}, args...))
		err := cmd.Execute()
		return out.String(), errBuf.String(), err
	}
	label := filepath.Base(filepath.Dir(dbhDir))
	warning := fmt.Sprintf("warning: table notice has different columns in models %s/portal, %s/resource; columns are merged, use --model to read one", label, label)
	for _, args := range [][]string{
		{"show", "notice", "--depth", "0"},
		{"relations", "--table", "notice"},
		{"path", "users", "notice"},
		{"check"},
	} {
		_, stderr, err := run(args...)
		if err != nil || !strings.Contains(stderr, warning) {
			t.Fatalf("%v: err=%v stderr=%q", args, err, stderr)
		}
	}
	out, stderr, err := run("show", "notice", "--depth", "0", "--model", "portal")
	if err != nil || stderr != "" || strings.Contains(out, " title") {
		t.Fatalf("--model portal: err=%v stderr=%q out=%q", err, stderr, out)
	}
	if _, stderr, err := run("show", "users", "--depth", "0"); err != nil || stderr != "" {
		t.Fatalf("users must not warn: err=%v stderr=%q", err, stderr)
	}
	// columns resolves tables through the same lookup, so it warns too.
	if _, stderr, err := run("cols", "notice", "--format", "tsv"); err != nil || !strings.Contains(stderr, warning) {
		t.Fatalf("cols notice: err=%v stderr=%q", err, stderr)
	}
	out, _, err = run("columns", "notice", "--model", "resource", "--format", "tsv")
	if err != nil || out != "table\tcolumn\ttype\tpk\tnot_null\tunique\tdefault\tnote\nnotice\tid\tbigint\ttrue\ttrue\tfalse\t\t\nnotice\ttitle\tvarchar(50)\tfalse\tfalse\tfalse\t\t\n" {
		t.Fatalf("columns --model resource: err=%v out=%q", err, out)
	}
}

// Opt-in: reads foreign keys from the databases supplied by the developer.
func TestExternalERDPull(t *testing.T) {
	for driver, env := range map[string]string{"postgres": "DBH_TEST_POSTGRES_DSN", "mysql": "DBH_TEST_MYSQL_DSN"} {
		t.Run(driver, func(t *testing.T) {
			dsn := os.Getenv(env)
			if dsn == "" {
				t.Skip("set " + env + " to run")
			}
			ctx := context.Background()
			p := Profile{Name: "integration", Driver: driver, DSN: dsn}
			s, err := openSession(ctx, p, 10*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			suffix := time.Now().UnixNano()
			parent, child := fmt.Sprintf("dbh_erd_parent_%d", suffix), fmt.Sprintf("dbh_erd_child_%d", suffix)
			// A composite key and a quoted default cover dbml-tools patches.
			ddl := fmt.Sprintf("CREATE TABLE %s (a INTEGER, b INTEGER, PRIMARY KEY (a, b)); CREATE TABLE %s (id INTEGER PRIMARY KEY, pa INTEGER, pb INTEGER, note VARCHAR(10) DEFAULT 'it''s', FOREIGN KEY (pa, pb) REFERENCES %s(a, b) ON DELETE CASCADE);", parent, child, parent)
			if err := s.Run(ctx, ddl, "json", io.Discard); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := s.Run(ctx, "DROP TABLE "+child+"; DROP TABLE "+parent+";", "json", io.Discard); err != nil {
					t.Errorf("cleanup: %v", err)
				}
			}()
			dbml, err := pullDBML(ctx, p, "", false, "", 30*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{
				fmt.Sprintf(`Ref: "%s".("pa", "pb") > "%s".("a", "b") [delete: cascade]`, child, parent),
				`"note" varchar(10) [default: 'it\'s']`,
			} {
				if !strings.Contains(dbml, want) {
					t.Fatalf("missing %s in:\n%s", want, dbml)
				}
			}
		})
	}
}

func TestJSONOutputKeepsHTMLCharacters(t *testing.T) {
	ctx := context.Background()
	p := Profile{Name: "test", Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "json.db")}
	s, err := openSession(ctx, p, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var out bytes.Buffer
	if err := s.Run(ctx, "SELECT '<b>&</b>' AS html", "json", &out); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != `{"html":"<b>&</b>"}` {
		t.Fatalf("json = %q", out.String())
	}

	dbhDir := filepath.Join(t.TempDir(), ".dbh")
	if err := os.MkdirAll(dbhDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbhDir, "schema.dbml"), []byte("Table users {\n  id integer [pk]\n}\nTable orders {\n  id integer [pk]\n  user_id integer\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbhDir, "schema.refs.dbml"), []byte("// app/orders.go:7 (GORM foreignKey)\nRef: orders.user_id > users.id\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := NewCommand()
	out.Reset()
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"erd", "--dir", dbhDir, "relations", "--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"relation":">"`) || !strings.Contains(out.String(), `"evidence":"app/orders.go:7 (GORM foreignKey)"`) {
		t.Fatalf("relations json = %q", out.String())
	}
}

func TestERDInitAndResyncSQLite(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	projDir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "app.db")

	p := Profile{Name: "demo", Driver: "sqlite", DSN: dbPath}
	s, err := openSession(ctx, p, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Run(ctx, "CREATE TABLE users(id INTEGER PRIMARY KEY, email TEXT);", "json", io.Discard); err != nil {
		s.Close()
		t.Fatal(err)
	}
	s.Close()

	st := Store{Dir: cfgDir}
	if err := st.Put(p, true); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) (string, string, error) {
		cmd := NewCommand()
		var out, errBuf bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errBuf)
		cmd.SetArgs(append([]string{"--config-dir", cfgDir, "erd", "--dir", projDir}, args...))
		err := cmd.Execute()
		return out.String(), errBuf.String(), err
	}

	// resync fails before init.
	if _, _, err := run("resync", "demo"); err == nil || !strings.Contains(err.Error(), "dbh erd init") {
		t.Fatalf("expected resync before init to fail with init hint, got %v", err)
	}

	// init creates .dbh/schema.dbml.
	out, stderr, err := run("init", "demo")
	if err != nil || !strings.Contains(out, "Saved ") || stderr != "" {
		t.Fatalf("init failed: err=%v out=%q stderr=%q", err, out, stderr)
	}
	schemaPath := filepath.Join(projDir, ".dbh", "schema.dbml")
	data, err := os.ReadFile(schemaPath)
	if err != nil || !strings.Contains(string(data), `Table "users"`) {
		t.Fatalf("schema.dbml = %q, err=%v", string(data), err)
	}

	// init again on existing model fails and points to resync.
	if _, _, err := run("init", "demo"); err == nil || !strings.Contains(err.Error(), "already exists") || !strings.Contains(err.Error(), "dbh erd resync") {
		t.Fatalf("expected second init to fail with resync hint, got %v", err)
	}

	// init with --model extra succeeds independently.
	if out, _, err := run("init", "demo", "--model", "extra"); err != nil || !strings.Contains(out, "Saved ") {
		t.Fatalf("init --model extra failed: err=%v out=%q", err, out)
	}
	if _, err := os.Stat(filepath.Join(projDir, ".dbh", "extra.dbml")); err != nil {
		t.Fatalf("extra.dbml missing: %v", err)
	}

	// Modify DB and add a refs file; resync updates schema.dbml, prints Changes:, and preserves refs.
	s, err = openSession(ctx, p, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Run(ctx, "CREATE TABLE items(id INTEGER PRIMARY KEY, user_id INTEGER);", "json", io.Discard); err != nil {
		s.Close()
		t.Fatal(err)
	}
	s.Close()

	refsPath := filepath.Join(projDir, ".dbh", "schema.refs.dbml")
	refsContent := "// app/items.go:3\nRef: items.user_id > users.id\n"
	if err := os.WriteFile(refsPath, []byte(refsContent), 0o644); err != nil {
		t.Fatal(err)
	}

	out, stderr, err = run("resync", "demo")
	if err != nil || !strings.Contains(out, "Saved ") || !strings.Contains(out, "Changes: +1 table") || stderr != "" {
		t.Fatalf("resync failed: err=%v out=%q stderr=%q", err, out, stderr)
	}
	updated, err := os.ReadFile(schemaPath)
	if err != nil || !strings.Contains(string(updated), `Table "items"`) {
		t.Fatalf("updated schema.dbml = %q, err=%v", string(updated), err)
	}
	keptRefs, err := os.ReadFile(refsPath)
	if err != nil || string(keptRefs) != refsContent {
		t.Fatalf("refs changed: %q, err=%v", string(keptRefs), err)
	}
}
