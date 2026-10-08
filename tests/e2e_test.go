//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

var binary string

// Build the current source once so direct go test invocations cannot use a stale CLI.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "dbh-e2e-binary-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binary = filepath.Join(root, "dbh")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	cmd := exec.CommandContext(ctx, "go", "build", "-o", binary, "..")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	err = cmd.Run()
	cancel()
	code := 1
	if err == nil {
		code = m.Run()
	}
	if err := os.RemoveAll(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}

type fixture struct {
	t                      *testing.T
	root, config, database string
	env                    map[string]string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{t: t, root: root, config: filepath.Join(root, "config"), database: filepath.Join(root, "demo.db"), env: map[string]string{}}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		f.env[key] = value
	}
	f.env["DBH_CONFIG_DIR"] = f.config
	f.env["HOME"] = root
	f.env["XDG_CONFIG_HOME"] = filepath.Join(root, "xdg")
	f.env["INPUTRC"] = filepath.Join(root, "empty-inputrc")
	f.env["TERM"] = "xterm-256color"
	delete(f.env, "NO_COLOR")
	writeFile(t, f.env["INPUTRC"], "")
	f.run("n", "demo", "--driver", "sqlite", "--dsn", f.database)
	return f
}

func (f *fixture) environment() []string {
	env := make([]string, 0, len(f.env))
	for key, value := range f.env {
		env = append(env, key+"="+value)
	}
	return env
}

type result struct{ stdout, stderr string }

func (f *fixture) invoke(stdin string, success bool, args ...string) result {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = f.environment()
	cmd.Stdin = strings.NewReader(stdin)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	r := result{out.String(), stderr.String()}
	if ctx.Err() != nil {
		f.t.Fatalf("CLI timeout: %v", args)
	}
	if success {
		if err != nil || r.stderr != "" {
			f.t.Fatalf("CLI %v: %v\nstdout: %s\nstderr: %s", args, err, r.stdout, r.stderr)
		}
	} else {
		if _, ok := err.(*exec.ExitError); !ok || r.stdout != "" || !strings.Contains(r.stderr, "Error:") {
			f.t.Fatalf("expected CLI error for %v: %v, %+v", args, err, r)
		}
	}
	return r
}

func (f *fixture) run(args ...string) string { f.t.Helper(); return f.invoke("", true, args...).stdout }
func (f *fixture) fail(args ...string) string {
	f.t.Helper()
	return f.invoke("", false, args...).stderr
}
func (f *fixture) sql(query string, args ...string) string {
	f.t.Helper()
	return f.run(append([]string{"c", "demo", "--sql", query}, args...)...)
}
func (f *fixture) seed() {
	f.t.Helper()
	f.sql("CREATE TABLE users(id INTEGER PRIMARY KEY, name TEXT); INSERT INTO users(name) VALUES ('Alice');", "--no-history")
}
func (f *fixture) historyPath() string { return filepath.Join(f.config, "history", "demo.jsonl") }
func (f *fixture) history() []string {
	f.t.Helper()
	var queries []string
	for _, line := range strings.Split(strings.TrimSpace(readFile(f.t, f.historyPath())), "\n") {
		var query string
		if err := json.Unmarshal([]byte(line), &query); err != nil {
			f.t.Fatal(err)
		}
		queries = append(queries, query)
	}
	return queries
}

func equal[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v; want %#v", got, want)
	}
}
func contains(t *testing.T, text, want string) {
	t.Helper()
	if !strings.Contains(text, want) {
		t.Fatalf("missing %q in %q", want, text)
	}
}
func excludes(t *testing.T, text, unwanted string) {
	t.Helper()
	if strings.Contains(text, unwanted) {
		t.Fatalf("unexpected %q in %q", unwanted, text)
	}
}
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func absent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected absent path %s: %v", path, err)
	}
}
func permission(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	equal(t, info.Mode().Perm(), want)
}

func TestConnectionLifecycleAndAliases(t *testing.T) {
	f := newFixture(t)
	listing := f.run("list")
	equal(t, listing, f.run("ls"))
	contains(t, listing, "demo")
	excludes(t, listing, f.database)
	f.fail("new", "demo", "--driver", "sqlite", "--dsn", ":memory:")
	f.fail("new", "../escape", "--driver", "sqlite", "--dsn", ":memory:")
	absent(t, filepath.Join(f.root, "escape"))
	f.seed()
	f.run("edit", "demo", "--dsn", ":memory:")
	f.fail("c", "demo", "--sql", "SELECT * FROM users")
	f.run("e", "demo", "--dsn", f.database)
	equal(t, f.sql("SELECT name FROM users", "--format", "json"), "{\"name\":\"Alice\"}\n")
	for _, command := range []string{"remove", "rm", "r"} {
		f.run(command, "demo")
		f.fail("c", "demo", "--sql", "SELECT 1")
		if _, err := os.Stat(f.database); err != nil {
			t.Fatal(err)
		}
		f.run("new", "demo", "--driver", "sqlite", "--dsn", f.database)
	}
	permission(t, f.config, 0700)
	permission(t, filepath.Join(f.config, "connections.json"), 0600)
}

func TestEnvironmentDSNAndConfigOverride(t *testing.T) {
	f := newFixture(t)
	other := filepath.Join(f.root, "other-config")
	f.env["DBH_E2E_DSN"] = f.database
	f.run("--config-dir", other, "new", "env-test", "--driver", "sqlite", "--dsn-env", "DBH_E2E_DSN")
	excludes(t, f.run("ls"), "env-test")
	equal(t, f.run("--config-dir", other, "connect", "env-test", "--sql", "SELECT 7 AS value", "--format", "json"), "{\"value\":7}\n")
	delete(f.env, "DBH_E2E_UNSET")
	f.fail("new", "missing", "--driver", "sqlite", "--dsn-env", "DBH_E2E_UNSET")
}

func TestSQLInputsFormatsAndTransactions(t *testing.T) {
	f := newFixture(t)
	equal(t, f.sql("CREATE TABLE values_test(value TEXT); INSERT INTO values_test VALUES ('a;b'); BEGIN; INSERT INTO values_test VALUES ('rolled back'); ROLLBACK; SELECT value, NULL AS empty FROM values_test;", "--format", "json"), "{\"value\":\"a;b\",\"empty\":null}\n")
	query := "SELECT value FROM values_test;"
	file := filepath.Join(f.root, "query.sql")
	writeFile(t, file, query)
	for _, input := range []struct {
		args  []string
		stdin string
	}{{[]string{"--file", file}, ""}, {[]string{"--file", "-"}, query}, {nil, query}} {
		args := append([]string{"connect", "demo"}, input.args...)
		args = append(args, "--format", "csv")
		equal(t, f.invoke(input.stdin, true, args...).stdout, "value\na;b\n")
	}
	table := f.sql(query)
	contains(t, table, "a;b")
	contains(t, table, "(1 rows)")
}

func TestBatchErrorStopsRemainingStatements(t *testing.T) {
	f := newFixture(t)
	f.seed()
	contains(t, f.fail("c", "demo", "--sql", "SELECT * FROM missing; INSERT INTO users VALUES (2, 'unexpected');"), "missing")
	equal(t, f.sql("SELECT name FROM users", "--format", "json", "--no-history"), "{\"name\":\"Alice\"}\n")
	f.fail("c", "demo", "--sql", "SELECT 'unfinished")
	f.fail("c", "demo", "--sql", "SELECT 1", "--format", "invalid")
	f.fail("connect", "demo", "--sql", "SELECT 1", "--file", "-")
}

func TestHistoryMultilineAndNoHistory(t *testing.T) {
	f := newFixture(t)
	query := "SELECT 'first\nsecond;third' AS value;"
	f.sql(query, "--format", "json")
	equal(t, f.history(), []string{query})
	before := readFile(t, f.historyPath())
	f.sql("SELECT 99", "--no-history")
	equal(t, readFile(t, f.historyPath()), before)
	contains(t, f.run("history", "demo"), query)
	permission(t, f.historyPath(), 0600)
}

func TestDatabaseConnections(t *testing.T) {
	for _, driver := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			dsn := ""
			if driver != "sqlite" {
				env := "DBH_TEST_MYSQL_DSN"
				if driver == "postgres" {
					env = "DBH_TEST_POSTGRES_DSN"
				}
				dsn = os.Getenv(env)
				if dsn == "" {
					t.Skip("set " + env + " to run")
				}
			}
			f := newFixture(t)
			if driver == "sqlite" {
				dsn = filepath.Join(f.root, "connection.db")
			}
			name := "e2e-" + driver
			table := fmt.Sprintf("dbh_e2e_%d", time.Now().UnixNano())
			index := table + "_name"
			f.env["DBH_E2E_DSN"] = dsn
			f.run("new", name, "--driver", driver, "--dsn-env", "DBH_E2E_DSN")
			listing := f.run("list")
			equal(t, listing, f.run("ls"))
			contains(t, listing, name)
			excludes(t, listing, dsn)
			query := func(sql string, args ...string) string {
				return f.run(append([]string{"connect", name, "--sql", sql, "--no-history"}, args...)...)
			}
			query("CREATE TABLE " + table + " (id INTEGER PRIMARY KEY, name VARCHAR(50), note VARCHAR(100));")
			t.Cleanup(func() { query("DROP TABLE " + table + ";") })
			query("CREATE UNIQUE INDEX " + index + " ON " + table + " (name);")
			output := query("INSERT INTO "+table+" VALUES (1, 'hello dbh', '连接正常'), (2, 'second row', NULL); BEGIN; INSERT INTO "+table+" VALUES (3, 'rollback', NULL); ROLLBACK; SELECT * FROM "+table+" ORDER BY id;", "--format", "json")
			var rows []map[string]any
			for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
				var row map[string]any
				if err := json.Unmarshal([]byte(line), &row); err != nil {
					t.Fatal(err)
				}
				rows = append(rows, row)
			}
			equal(t, rows, []map[string]any{{"id": float64(1), "name": "hello dbh", "note": "连接正常"}, {"id": float64(2), "name": "second row", "note": nil}})
			sql := "SELECT COUNT(*) AS row_count FROM " + table + ";"
			for _, command := range []string{"connect", "c"} {
				equal(t, f.run(command, name, "--sql", sql, "--format", "csv", "--no-history"), "row_count\n2\n")
			}
			file := filepath.Join(f.root, driver+".sql")
			writeFile(t, file, sql)
			for _, input := range []struct {
				args  []string
				stdin string
			}{{[]string{"--file", file}, ""}, {[]string{"--file", "-"}, sql}, {nil, sql}} {
				args := append([]string{"connect", name}, input.args...)
				args = append(args, "--format", "json", "--no-history")
				equal(t, f.invoke(input.stdin, true, args...).stdout, "{\"row_count\":2}\n")
			}
			output = query("SELECT * FROM " + table + " ORDER BY id;")
			contains(t, output, "连接正常")
			contains(t, output, "(2 rows)")
			if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
				terminal := f.openTerminal(name, "--no-history")
				terminal.exchange("sel", "SELECT")
				terminal.exchange("\x03", name+"> ")
				terminal.exchange("\\tables\r", table)
				output = terminal.exchange("\\describe "+table+"\r", index)
				contains(t, output, "\"primary_key_position\":1")
				contains(t, output, "\"name\":\"note\"")
				output = terminal.exchange("\\indexes "+table+"\r", index)
				unique := "\"is_unique\":1"
				if driver == "postgres" {
					unique = "\"is_unique\":true"
				}
				contains(t, output, unique)
				terminal.exchange(sql+"\r", "{\"row_count\":2}")
				terminal.exit("\\q\r")
			}
			f.run("remove", name)
			f.fail("connect", name, "--sql", sql)
			f.run("n", name, "--driver", driver, "--dsn-env", "DBH_E2E_DSN")
			equal(t, query(sql, "--format", "json"), "{\"row_count\":2}\n")
			absent(t, filepath.Join(f.config, "history", name+".jsonl"))
		})
	}
}
