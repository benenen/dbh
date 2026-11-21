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
