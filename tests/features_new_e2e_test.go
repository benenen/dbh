//go:build e2e

package e2e

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBlobVectorAndCellTruncationE2E(t *testing.T) {
	f := newFixture(t)
	vec := "[0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0, 1.1, 1.2, 1.3, 1.4, 1.5, 1.6]"
	longNote := strings.Repeat("x", 240)
	f.sql("CREATE TABLE items (id INTEGER PRIMARY KEY, payload BLOB, vec TEXT, note TEXT);"+
		"INSERT INTO items VALUES (1, X'89504E470D0A1A0A0000000D4948445200000001000000010802000000907753DE00000000', '"+vec+"', '"+longNote+"');", "--no-history")

	tbl := f.run("exec", "demo", "SELECT payload, vec FROM items;")
	contains(t, tbl, "<binary 37 B,")
	contains(t, tbl, "<vector[16]:")
	noteTbl := f.run("exec", "demo", "SELECT note FROM items;")
	contains(t, noteTbl, "... (240 chars)")

	tsv := f.run("exec", "demo", "SELECT payload, vec, note FROM items;", "--format", "tsv")
	contains(t, tsv, "0x89504e47")
	contains(t, tsv, vec)
	contains(t, tsv, longNote)

	js := f.run("exec", "demo", "SELECT payload, vec FROM items;", "--format", "json")
	contains(t, js, `"payload":"0x89504e47`)
	contains(t, js, vec)

	jsTrunc := f.run("exec", "demo", "SELECT payload, vec FROM items;", "--format", "json", "--max-cell-width", "50")
	contains(t, jsTrunc, "<binary 37 B")
	contains(t, jsTrunc, "<vector[16]:")

	untruncTbl := f.run("exec", "demo", "SELECT note FROM items;", "--max-cell-width", "0")
	excludes(t, untruncTbl, "... (240 chars)")
	if strings.Count(untruncTbl, "x") < 240 {
		t.Fatalf("expected full 240-char note without truncation, got:\n%s", untruncTbl)
	}
	contains(t, f.fail("exec", "demo", "SELECT 1;", "--max-cell-width", "-1"), "max-cell-width")
}

func TestExportAndImportE2E(t *testing.T) {
	f := newFixture(t)
	f.sql("CREATE TABLE src (id INTEGER PRIMARY KEY, name TEXT); INSERT INTO src VALUES (1, 'Alice'), (2, 'Bob');", "--no-history")
	f.sql("CREATE TABLE dst_csv (id INTEGER PRIMARY KEY, name TEXT); CREATE TABLE dst_json (id INTEGER PRIMARY KEY, name TEXT);", "--no-history")

	csvPath := filepath.Join(f.root, "src.csv")
	f.run("export", "demo", "--table", "src", "--format", "csv", "-o", csvPath)
	contains(t, readFile(t, csvPath), "1,Alice")

	sqlOut := f.run("export", "demo", "--table", "src", "--format", "sql")
	contains(t, sqlOut, `INSERT INTO "src" ("id", "name") VALUES (1, 'Alice');`)

	gzPath := filepath.Join(f.root, "src.ndjson.gz")
	f.run("export", "demo", "--table", "src", "--format", "json", "-o", gzPath)
	gf, err := os.Open(gzPath)
	if err != nil {
		t.Fatal(err)
	}
	gr, err := gzip.NewReader(gf)
	if err != nil {
		t.Fatal(err)
	}
	rawGZ, _ := io.ReadAll(gr)
	_ = gr.Close()
	_ = gf.Close()
	contains(t, string(rawGZ), `"name":"Bob"`)

	contains(t, f.run("import", "demo", "--table", "dst_csv", "-f", csvPath), "Imported 2 rows")
	contains(t, f.run("import", "demo", "--table", "dst_json", "-f", gzPath), "Imported 2 rows")
	contains(t, f.run("exec", "demo", "SELECT COUNT(*) FROM dst_csv;", "--format", "tsv"), "2")
	contains(t, f.run("exec", "demo", "SELECT COUNT(*) FROM dst_json;", "--format", "tsv"), "2")

	tsvQuery := f.run("export", "demo", "--sql", "SELECT id, name FROM src WHERE id = 1", "--format", "tsv")
	contains(t, tsvQuery, "1\tAlice")
	excludes(t, tsvQuery, "Bob")

	maxCmd := exec.Command(binary, "export", "demo", "--table", "src", "--format", "csv", "--max-rows", "1")
	maxCmd.Env, maxCmd.Dir = f.environment(), f.dir
	var maxOut, maxErr bytes.Buffer
	maxCmd.Stdout, maxCmd.Stderr = &maxOut, &maxErr
	if err := maxCmd.Run(); err != nil {
		t.Fatalf("export --max-rows 1: %v\n%s", err, maxErr.String())
	}
	contains(t, maxOut.String(), "1,Alice")
	excludes(t, maxOut.String(), "Bob")
	contains(t, maxErr.String(), "--max-rows")

	gzFlagPath := filepath.Join(f.root, "src_csv.dat")
	f.run("export", "demo", "--table", "src", "--format", "csv", "--gzip", "-o", gzFlagPath)
	gf2, err := os.Open(gzFlagPath)
	if err != nil {
		t.Fatal(err)
	}
	gr2, err := gzip.NewReader(gf2)
	if err != nil {
		t.Fatal(err)
	}
	rawCSVGZ, _ := io.ReadAll(gr2)
	_ = gr2.Close()
	_ = gf2.Close()
	contains(t, string(rawCSVGZ), "1,Alice")

	f.sql("CREATE TABLE dst_stdin (id INTEGER PRIMARY KEY, name TEXT);", "--no-history")
	cmd := exec.Command(binary, "import", "demo", "--table", "dst_stdin", "--format", "csv", "-f", "-")
	cmd.Env, cmd.Dir = f.environment(), f.dir
	cmd.Stdin = strings.NewReader("id,name\n10,Carol\n11,Dave\n")
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("import stdin: %v\n%s", err, errBuf.String())
	}
	contains(t, outBuf.String(), "Imported 2 rows")
	contains(t, f.run("exec", "demo", "SELECT COUNT(*) FROM dst_stdin;", "--format", "tsv"), "2")

	roImpDB := filepath.Join(f.root, "ro_import.db")
	f.run("new", "ro_imp", "--driver", "sqlite", "--dsn", roImpDB, "--read-only")
	f.run("exec", "ro_imp", "--allow-write", "CREATE TABLE dst_ro (id INTEGER PRIMARY KEY, name TEXT);")
	contains(t, f.fail("import", "ro_imp", "--table", "dst_ro", "-f", csvPath), "read-only")
	contains(t, f.run("import", "ro_imp", "--table", "dst_ro", "-f", csvPath, "--allow-write"), "Imported 2 rows")
}

func TestTwoConnectionERDDiffE2E(t *testing.T) {
	f := newFixture(t)
	f.sql("CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);", "--no-history")
	db2 := filepath.Join(f.root, "target.db")
	f.run("new", "target", "--driver", "sqlite", "--dsn", db2)
	f.run("exec", "target", "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, email TEXT); CREATE TABLE logs (id INTEGER PRIMARY KEY, msg TEXT);")

	diff := f.run("erd", "diff", "demo", "target", "--format", "tsv")
	contains(t, diff, "users.email")
	contains(t, diff, "added")
	contains(t, diff, "logs")

	diffExcl := f.run("erd", "diff", "demo", "target", "--exclude", "logs", "--format", "tsv")
	contains(t, diffExcl, "users.email")
	excludes(t, diffExcl, "logs")

	diffIncl := f.run("erd", "diff", "demo", "target", "--include", "users", "--format", "tsv")
	contains(t, diffIncl, "users.email")
	excludes(t, diffIncl, "logs")

	diffSame := f.run("erd", "diff", "demo", "demo", "--format", "tsv")
	excludes(t, diffSame, "added")
	excludes(t, diffSame, "removed")
}

func TestExplainSnippetAndExpandedE2E(t *testing.T) {
	f := newFixture(t)
	f.sql("CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT); INSERT INTO users VALUES (1, 'Alice'), (2, 'Bob');", "--no-history")

	contains(t, f.run("explain", "demo", "SELECT * FROM users WHERE id = 1", "--format", "tsv"), "detail")
	contains(t, f.run("explain", "demo", "-e", "SELECT * FROM users WHERE id = 1", "--format", "json"), `"detail"`)

	qPath := filepath.Join(f.root, "q.sql")
	writeFile(t, qPath, "SELECT * FROM users WHERE id = 2;\n")
	contains(t, f.run("explain", "demo", "-f", qPath, "--format", "tsv"), "detail")

	roExpDB := filepath.Join(f.root, "ro_exp.db")
	f.run("new", "ro_exp", "--driver", "sqlite", "--dsn", roExpDB, "--read-only")
	contains(t, f.fail("explain", "ro_exp", "--analyze", "DELETE FROM users WHERE id = 1"), "EXPLAIN ANALYZE on a write statement is blocked on read-only connections")

	f.run("snippet", "set", "get_user", "SELECT name FROM users WHERE id = ?", "--description", "fetch user")
	contains(t, f.run("snippet", "list"), "get_user")
	contains(t, f.run("snippet", "show", "get_user"), "SELECT name FROM users")
	contains(t, f.run("snippet", "run", "get_user", "demo", "--param", "2", "--format", "tsv"), "Bob")
	f.run("snippet", "rm", "get_user")

	snipFile := filepath.Join(f.root, "snip.sql")
	writeFile(t, snipFile, "SELECT id, name FROM users ORDER BY id;\n")
	f.run("snippet", "set", "all_users_file", "-f", snipFile)
	contains(t, f.run("snippet", "ls", "--format", "json"), `"name":"all_users_file"`)

	runCmd := exec.Command(binary, "snippet", "run", "all_users_file", "demo", "--stats", "--max-rows", "1", "-x", "--max-cell-width", "50")
	runCmd.Env, runCmd.Dir = f.environment(), f.dir
	var runOut, runErr bytes.Buffer
	runCmd.Stdout, runCmd.Stderr = &runOut, &runErr
	if err := runCmd.Run(); err != nil {
		t.Fatalf("snippet run --stats: %v\n%s", err, runErr.String())
	}
	contains(t, runOut.String(), "Row 1:")
	contains(t, runOut.String(), "Alice")
	excludes(t, runOut.String(), "Bob")
	if strings.TrimSpace(runErr.String()) == "" {
		t.Fatalf("expected --stats output on stderr")
	}

	f.run("exec", "ro_exp", "--allow-write", "CREATE TABLE t (id INTEGER PRIMARY KEY);")
	f.run("snippet", "set", "ins_t", "INSERT INTO t VALUES (1);")
	contains(t, f.fail("snippet", "run", "ins_t", "ro_exp"), "read-only")
	f.run("snippet", "run", "ins_t", "ro_exp", "--allow-write")

	contains(t, f.fail("snippet", "show", "no_such_snippet"), "does not exist")
	contains(t, f.fail("snippet", "set", "bad!name", "SELECT 1"), "name must contain")

	expanded := f.run("exec", "demo", "SELECT id, name FROM users ORDER BY id;", "-x")
	contains(t, expanded, "Row 1:")
	contains(t, expanded, "Column")
	contains(t, expanded, "Alice")

	for _, driver := range []string{"postgres", "mysql"} {
		env := "DBH_TEST_" + strings.ToUpper(driver) + "_DSN"
		if os.Getenv(env) == "" {
			continue
		}
		t.Run("explain-"+driver, func(t *testing.T) {
			f.run("new", driver, "--driver", driver, "--dsn-env", env)
			contains(t, f.run("explain", driver, "SELECT 1", "--format", "tsv"), "1")
		})
	}
}

func TestRedisClusterAndSentinelE2E(t *testing.T) {
	if dsn := os.Getenv("DBH_TEST_REDIS_CLUSTER_DSN"); dsn != "" {
		t.Run("cluster", func(t *testing.T) {
			f := newFixture(t)
			f.run("new", "rc", "--driver", "redis", "--dsn-env", "DBH_TEST_REDIS_CLUSTER_DSN")
			contains(t, f.run("check", "rc"), "OK")
			contains(t, f.run("exec", "rc", "PING", "--format", "tsv"), "PONG")
		})
	} else {
		t.Log("DBH_TEST_REDIS_CLUSTER_DSN not set; skipping Redis Cluster check")
	}

	if dsn := os.Getenv("DBH_TEST_REDIS_SENTINEL_DSN"); dsn != "" {
		t.Run("sentinel", func(t *testing.T) {
			f := newFixture(t)
			f.run("new", "rs", "--driver", "redis", "--dsn-env", "DBH_TEST_REDIS_SENTINEL_DSN")
			contains(t, f.run("check", "rs"), "OK")
			contains(t, f.run("exec", "rs", "PING", "--format", "tsv"), "PONG")
		})
	} else {
		t.Log("DBH_TEST_REDIS_SENTINEL_DSN not set; skipping Redis Sentinel check")
	}
}
