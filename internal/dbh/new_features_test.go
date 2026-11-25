package dbh

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"--config-dir", dir}, args...))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out.String())
	}
	return out.String()
}

func TestTransferExplainSnippetExpandedTagsAndTwoConnDiff(t *testing.T) {
	dir := t.TempDir()
	db1 := filepath.Join(dir, "db1.sqlite")
	db2 := filepath.Join(dir, "db2.sqlite")

	runCLI(t, dir, "new", "c1", "--driver", "sqlite", "--dsn", db1, "--env", "dev", "--tag", "team-a", "--tag", "local")
	runCLI(t, dir, "new", "c2", "--driver", "sqlite", "--dsn", db2, "--env", "prod", "--read-only", "--tag", "prod")

	listed := runCLI(t, dir, "ls", "--tag", "team-a")
	if !strings.Contains(listed, "c1") || strings.Contains(listed, "c2") {
		t.Fatalf("tag filter list = %q", listed)
	}

	runCLI(t, dir, "exec", "c1", "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT); INSERT INTO users VALUES (1, 'Alice'), (2, 'Bob');")
	runCLI(t, dir, "exec", "c2", "--allow-write", "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, email TEXT);")

	// Export CSV, SQL, and gzipped JSON
	csvOut := runCLI(t, dir, "export", "c1", "--table", "users", "--format", "csv")
	if !strings.Contains(csvOut, "id,name") || !strings.Contains(csvOut, "1,Alice") {
		t.Fatalf("export csv = %q", csvOut)
	}
	sqlOut := runCLI(t, dir, "export", "c1", "--table", "users", "--format", "sql")
	if !strings.Contains(sqlOut, `INSERT INTO "users" ("id", "name") VALUES (1, 'Alice');`) {
		t.Fatalf("export sql = %q", sqlOut)
	}
	gzPath := filepath.Join(dir, "users.ndjson.gz")
	runCLI(t, dir, "export", "c1", "--table", "users", "--format", "json", "-o", gzPath)
	gf, err := os.Open(gzPath)
	if err != nil {
		t.Fatal(err)
	}
	gr, err := gzip.NewReader(gf)
	if err != nil {
		t.Fatal(err)
	}
	gzData, _ := io.ReadAll(gr)
	_ = gr.Close()
	_ = gf.Close()
	if !strings.Contains(string(gzData), `"name":"Alice"`) {
		t.Fatalf("gzipped json = %q", gzData)
	}

	// Import into c1 new table
	runCLI(t, dir, "exec", "c1", "CREATE TABLE users_copy (id INTEGER PRIMARY KEY, name TEXT);")
	impOut := runCLI(t, dir, "import", "c1", "--table", "users_copy", "-f", gzPath)
	if !strings.Contains(impOut, "Imported 2 rows") {
		t.Fatalf("import = %q", impOut)
	}

	// Expanded vertical output
	expOut := runCLI(t, dir, "exec", "c1", "SELECT id, name FROM users_copy ORDER BY id;", "-x")
	if !strings.Contains(expOut, "Row 1:") || !strings.Contains(expOut, "Column") || !strings.Contains(expOut, "Alice") {
		t.Fatalf("expanded = %q", expOut)
	}

	// Explain
	planOut := runCLI(t, dir, "explain", "c1", "SELECT * FROM users WHERE id = 1", "--format", "tsv")
	if !strings.Contains(planOut, "detail") {
		t.Fatalf("explain = %q", planOut)
	}

	// Snippet
	runCLI(t, dir, "snippet", "set", "find_user", "SELECT name FROM users WHERE id = ?", "--description", "lookup by id")
	snList := runCLI(t, dir, "snippet", "list")
	if !strings.Contains(snList, "find_user") {
		t.Fatalf("snippet list = %q", snList)
	}
	snRun := runCLI(t, dir, "snippet", "run", "find_user", "c1", "--param", "2", "--format", "tsv")
	if !strings.Contains(snRun, "Bob") {
		t.Fatalf("snippet run = %q", snRun)
	}

	// Two-connection ERD diff
	diffOut := runCLI(t, dir, "erd", "diff", "c1", "c2", "--format", "tsv")
	if !strings.Contains(diffOut, "users.email") || !strings.Contains(diffOut, "added") {
		t.Fatalf("two-connection erd diff = %q", diffOut)
	}
}
