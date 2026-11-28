package dbh

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSnippetTemplateVarsAndCLI(t *testing.T) {
	vars, err := parseVarAssignments([]string{"days=7", "status='active'"})
	if err != nil {
		t.Fatalf("parseVarAssignments: %v", err)
	}
	if vars["days"] != "7" || vars["status"] != "'active'" {
		t.Fatalf("unexpected CLI vars: %+v", vars)
	}

	shellVars, err := parseShellVarAssignments([]string{"days=7", "status='active'"})
	if err != nil {
		t.Fatalf("parseShellVarAssignments: %v", err)
	}
	if shellVars["status"] != "active" {
		t.Fatalf("expected shell quotes stripped, got %q", shellVars["status"])
	}

	rendered, err := renderSnippetTemplate("SELECT * FROM users WHERE days <= {{ days }} AND status = '{{status}}';", shellVars)
	if err != nil {
		t.Fatalf("renderSnippetTemplate: %v", err)
	}
	want := "SELECT * FROM users WHERE days <= 7 AND status = 'active';"
	if rendered != want {
		t.Fatalf("got %q, want %q", rendered, want)
	}

	if _, err := renderSnippetTemplate("SELECT {{a}}, {{b}}, {{a}}", map[string]string{"a": "1"}); err == nil || !strings.Contains(err.Error(), "b") {
		t.Fatalf("expected missing variable b error, got %v", err)
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sn.db")
	run := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		cmd := NewCommand()
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append([]string{"--config-dir", dir}, args...))
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out.String())
		}
		return out.String()
	}

	run("exec", dbPath, "CREATE TABLE users (id INT, days INT); INSERT INTO users VALUES (1, 3), (2, 14);")
	run("snippet", "set", "recent", "SELECT id FROM users WHERE days <= {{days}} ORDER BY id;")
	out := run("snippet", "run", "recent", dbPath, "--var", "days=7", "--format", "tsv")
	if out != "id\n1\n" {
		t.Fatalf("unexpected snippet run output: %q", out)
	}
}

func TestCompletionSnippetSubcommands(t *testing.T) {
	c := &completer{snippets: []string{"recent", "all_users"}}
	got, prefix := c.candidates([]rune(`\snippet `), 9)
	if !reflect.DeepEqual(got, []string{"list", "show", "save", "run", "rm"}) || prefix != "" {
		t.Fatalf("got %v / %q", got, prefix)
	}
	got, prefix = c.candidates([]rune(`\snippet run rec`), 16)
	if !reflect.DeepEqual(got, []string{"recent"}) || prefix != "rec" {
		t.Fatalf("got %v / %q", got, prefix)
	}
}
