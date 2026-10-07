package dbh

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/benenen/dbh/internal/database/registry"
)

func TestStoreLifecycle(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	p := Profile{Name: "local", Driver: "sqlite", DSN: ":memory:"}
	if err := s.Put(p, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(p, true); err == nil {
		t.Fatal("duplicate accepted")
	}
	p.DSN = "test.db"
	if err := s.Put(p, false); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(p.Name)
	if err != nil || got != p {
		t.Fatalf("got %+v, %v", got, err)
	}
	info, err := os.Stat(filepath.Join(s.Dir, "connections.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("unsafe permissions: %v", info.Mode())
	}
	if err := s.Remove(p.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(p.Name); err == nil {
		t.Fatal("removed connection exists")
	}
	if err := s.Put(Profile{Name: "../escape", Driver: "sqlite", DSN: "x"}, true); err == nil {
		t.Fatal("unsafe name accepted")
	}
}

func TestSplitSQL(t *testing.T) {
	tests := []struct {
		driver, input string
		count         int
		rest          string
		bad           bool
	}{
		{"sqlite", `select ';'; select "a;b";`, 2, "", false},
		{"sqlite", "-- ignore ;\nselect 1; /* nested /* ; */ ; */ select 2;", 2, "", false},
		{"postgres", `DO $tag$ BEGIN RAISE NOTICE ';'; END $tag$; select 1;`, 2, "", false},
		{"postgres", `SELECT E'it\'s; ok'; select 2;`, 2, "", false},
		{"mysql", `select 'it\'s; ok'; # comment ;
select 2;`, 2, "", false},
		{"sqlite", `select [a;b] from t; select 2`, 1, "select 2", false},
		{"sqlite", `select 'unfinished`, 0, `select 'unfinished`, true},
		{"sqlite", `select 'it''s; fine';`, 1, "", false},
		{"mysql", `select 1--1; select 2;`, 2, "", false},
		{"postgres", `select foo$bar$ from t;`, 1, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			stmts, rest, err := splitSQL(tt.input, tt.driver)
			if len(stmts) != tt.count || rest != tt.rest || (err != nil) != tt.bad {
				t.Fatalf("statements=%q remainder=%q err=%v", stmts, rest, err)
			}
		})
	}
}

func TestSQLiteSession(t *testing.T) {
	ctx := context.Background()
	s, err := openSession(ctx, Profile{Name: "test", Driver: "sqlite", DSN: ":memory:"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var out bytes.Buffer
	if err := s.Run(ctx, `CREATE TABLE users(id INTEGER PRIMARY KEY, name TEXT); INSERT INTO users(name) VALUES ('a;b'); BEGIN; INSERT INTO users(name) VALUES ('rolled back'); ROLLBACK; SELECT * FROM users;`, "json", &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "{\"id\":1,\"name\":\"a;b\"}\n" {
		t.Fatal(out.String())
	}
	names, err := s.Tables(ctx)
	if err != nil || !reflect.DeepEqual(names, []string{"users"}) {
		t.Fatalf("%v %v", names, err)
	}
	cols, err := s.Columns(ctx, "users")
	if err != nil || !reflect.DeepEqual(cols, []string{"id", "name"}) {
		t.Fatalf("%v %v", cols, err)
	}
	c := &completer{}
	if err := c.refresh(ctx, s); err != nil {
		t.Fatal(err)
	}
	matches, prefix := c.candidates([]rune("select * from us"), len([]rune("select * from us")))
	if prefix != "us" || !reflect.DeepEqual(matches, []string{"users"}) {
		t.Fatalf("%q %s", matches, prefix)
	}
	out.Reset()
	if err := s.Run(ctx, `INSERT INTO users(name) VALUES ('returned') RETURNING id;`, "csv", &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "id\n2\n" {
		t.Fatal(out.String())
	}
	if err := s.Run(ctx, `SELECT * FROM missing`, "table", &out); err == nil {
		t.Fatal("SQL error ignored")
	}
}

func TestHistoryPreservesSQL(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	q := "select 'first\nsecond';"
	if err := saveHistory(s, "test", q); err != nil {
		t.Fatal(err)
	}
	entries, err := readHistory(s, "test")
	if err != nil || !reflect.DeepEqual(entries, []string{q}) {
		t.Fatalf("%q %v", entries, err)
	}
}

func TestAcceptSQLInput(t *testing.T) {
	for _, tt := range []struct {
		input, driver string
		accept        bool
	}{
		{"", "sqlite", true},
		{`\tables`, "sqlite", true},
		{"SELECT\n    1;\n", "sqlite", true},
		{"SELECT 1;\nSELECT 2", "sqlite", false},
		{"SELECT ';'", "sqlite", false},
		{"SELECT 'first\n\\q\nlast';", "sqlite", true},
		{"SELECT 'unfinished;", "sqlite", false},
		{"SELECT 1; /* unfinished", "sqlite", false},
		{"SELECT 1; -- comment", "sqlite", true},
		{"SELECT $$semi;\ncolon$$;", "postgres", true},
		{"SELECT $tag$unfinished;", "postgres", false},
	} {
		t.Run(tt.input, func(t *testing.T) {
			if got := acceptSQLInput(tt.input, tt.driver); got != tt.accept {
				t.Fatalf("accept=%v, want %v", got, tt.accept)
			}
		})
	}
}

func TestCLI(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) (string, error) {
		t.Helper()
		cmd := NewCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append([]string{"--config-dir", dir}, args...))
		err := cmd.Execute()
		return out.String(), err
	}
	if _, err := run("n", "test", "--driver", "sqlite", "--dsn", filepath.Join(dir, "data.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := run("c", "test", "--sql", `CREATE TABLE t(x); INSERT INTO t VALUES(42);`); err != nil {
		t.Fatal(err)
	}
	out, err := run("connect", "test", "--sql", "SELECT * FROM t", "--format", "json")
	if err != nil || out != "{\"x\":42}\n" {
		t.Fatalf("%s %v", out, err)
	}
	out, err = run("history", "test")
	if err != nil || !strings.Contains(out, "SELECT * FROM t;") {
		t.Fatalf("history: %s %v", out, err)
	}
	before, _ := readHistory(Store{Dir: dir}, "test")
	if _, err := run("connect", "test", "--sql", "SELECT 99", "--no-history"); err != nil {
		t.Fatal(err)
	}
	after, _ := readHistory(Store{Dir: dir}, "test")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("--no-history persisted SQL")
	}
	cmd := NewCommand()
	var piped bytes.Buffer
	cmd.SetOut(&piped)
	cmd.SetIn(strings.NewReader("SELECT * FROM t;"))
	cmd.SetArgs([]string{"--config-dir", dir, "connect", "test", "--file", "-", "--format", "csv"})
	if err := cmd.Execute(); err != nil || piped.String() != "x\n42\n" {
		t.Fatalf("stdin: %s %v", piped.String(), err)
	}
	out, err = run("ls")
	if err != nil || !strings.Contains(out, "test") || strings.Contains(out, "data.db") {
		t.Fatalf("%s %v", out, err)
	}
	fullList, err := run("list")
	if err != nil || fullList != out {
		t.Fatalf("list and ls differ: %s %v", fullList, err)
	}
	if _, err := run("e", "test", "--dsn", ":memory:"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("rm", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("connect", "test", "--sql", "select 1"); err == nil {
		t.Fatal("missing profile accepted")
	}
}

func TestUseDatabaseParsing(t *testing.T) {
	for _, test := range []struct {
		query, driver, want string
		matched, invalid    bool
	}{
		{"use dbh_test", "postgres", "dbh_test", true, false},
		{"USE MixedCase", "postgres", "mixedcase", true, false},
		{`USE "Mixed Case"`, "postgres", "Mixed Case", true, false},
		{`USE "a""b"`, "postgres", `a"b`, true, false},
		{"USE `a``b`", "mysql", "a`b", true, false},
		{"USE MixedCase", "mysql", "MixedCase", true, false},
		{"-- leading comment\nUSE dbh_test -- trailing comment", "postgres", "dbh_test", true, false},
		{"SELECT 'USE dbh_test'", "postgres", "", false, false},
		{"USE", "postgres", "", true, true},
		{"USE a b", "postgres", "", true, true},
		{`USE ""`, "postgres", "", true, true},
		{`USE "unfinished`, "postgres", "", true, true},
		{"USE db; DROP TABLE users", "mysql", "", true, true},
	} {
		t.Run(test.query, func(t *testing.T) {
			backend, err := registry.Lookup(test.driver)
			if err != nil {
				t.Fatal(err)
			}
			name, matched, err := parseUseDatabase(test.query, backend.Syntax())
			if matched != test.matched || (err != nil) != test.invalid || (!test.invalid && name != test.want) {
				t.Fatalf("got (%q, %v, %v); want (%q, %v, invalid=%v)", name, matched, err, test.want, test.matched, test.invalid)
			}
		})
	}
}
