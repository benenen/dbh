package dbh

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/benenen/dbh/internal/database"
)

func TestMetadataBindingAndRowLimit(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) (string, string, error) {
		t.Helper()
		c := NewCommand()
		var out, diagnostic bytes.Buffer
		c.SetOut(&out)
		c.SetErr(&diagnostic)
		c.SetArgs(append([]string{"--config-dir", dir}, args...))
		err := c.Execute()
		return out.String(), diagnostic.String(), err
	}
	if _, _, err := run("new", "test", "--driver", "sqlite", "--dsn", filepath.Join(dir, "db")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run("exec", "test", `CREATE TABLE "order items" (id INTEGER, label TEXT); INSERT INTO "order items" VALUES (1,'a'),(2,'b'),(3,'c');`); err != nil {
		t.Fatal(err)
	}
	out, _, err := run("describe", "test", `"order items"`, "--format", "tsv")
	if err != nil || !strings.Contains(out, "label") {
		t.Fatalf("describe: %q %v", out, err)
	}
	out, _, err = run("tables", "test", "--format", "json")
	if err != nil || !strings.Contains(out, `{"name":"order items"}`) {
		t.Fatalf("tables: %q %v", out, err)
	}
	out, _, err = run("exec", "test", `SELECT label FROM "order items" WHERE id = ?`, "--param", "2", "--format", "tsv")
	if err != nil || out != "label\nb\n" {
		t.Fatalf("binding: %q %v", out, err)
	}
	out, notice, err := run("exec", "test", `SELECT * FROM "order items" ORDER BY id`, "--format", "tsv", "--max-rows", "2")
	if err != nil || out != "id\tlabel\n1\ta\n2\tb\n" || !strings.Contains(notice, "truncated") {
		t.Fatalf("limit: %q %q %v", out, notice, err)
	}
	out, _, err = run("exec", "test", `SELECT * FROM "order items"`, "--table-batch-size", "2")
	if err != nil || strings.Count(out, "label") != 2 || !strings.Contains(out, "(3 rows)") {
		t.Fatalf("batch: %q %v", out, err)
	}
	if _, _, err = run("exec", "test", "SELECT ?; SELECT ?", "--param", "1"); err == nil {
		t.Fatal("bound multiple statements")
	}
	if _, _, err = run("exec", "test", "SELECT ?", "--param", "1 2"); err == nil {
		t.Fatal("accepted multiple JSON values")
	}
}

func TestStatisticsKeepWritesInTheSession(t *testing.T) {
	ctx := context.Background()
	s, err := openSession(ctx, Profile{Name: "test", Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "test.db")}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var out, notice bytes.Buffer
	s.Stats, s.Notice = true, &notice
	trigger := `CREATE TRIGGER log_event AFTER INSERT ON events BEGIN INSERT INTO audit VALUES(new.id); END;`
	// The splitter does not support trigger bodies; create this trigger directly.
	if err := s.Run(ctx, `CREATE TABLE events(id INTEGER PRIMARY KEY, value INTEGER); CREATE TABLE audit(value INTEGER);`, "json", &out); err != nil {
		t.Fatal(err)
	}
	if err := s.Conn.Exec(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	notice.Reset()
	if err := s.Run(ctx, `BEGIN; INSERT INTO events VALUES(1,5),(2,6); UPDATE events SET value=value+1; DELETE FROM events WHERE id=2; SELECT value FROM events; SELECT COUNT(*) AS n FROM audit; ROLLBACK; SELECT COUNT(*) AS n FROM events;`, "json", &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "{\"value\":6}\n{\"n\":2}\n{\"n\":0}\n" {
		t.Fatalf("writes replayed or session lost: %s", out.String())
	}
	for _, want := range []string{"2 rows affected", "1 rows affected", "1 rows returned", "Time:"} {
		if !strings.Contains(notice.String(), want) {
			t.Fatalf("missing %q: %s", want, notice.String())
		}
	}
	out.Reset()
	notice.Reset()
	if err := s.Run(ctx, `WITH ids AS (SELECT 1) INSERT INTO events SELECT 3, 7 FROM ids; UPDATE events SET value=8 RETURNING value;`, "json", &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "{\"value\":8}\n" || !strings.Contains(notice.String(), "1 rows affected") {
		t.Fatalf("CTE/RETURNING: %s %s", out.String(), notice.String())
	}
}

func TestHistoryRetentionSearchClearAndConcurrentWriters(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	if err := store.Put(Profile{Name: "test", Driver: "sqlite", DSN: ":memory:"}, true); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := saveHistory(store, "test", fmt.Sprintf("SELECT %d;", i)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	entries, err := readHistory(store, "test")
	if err != nil || len(entries) != 20 {
		t.Fatalf("lost history: %v %v", entries, err)
	}
	store.HistoryLimit = 2
	for _, q := range []string{"SELECT 'first\nline';", "SELECT 99;", "SELECT 'first\nline';", "SELECT 100;"} {
		if err := saveHistory(store, "test", q); err != nil {
			t.Fatal(err)
		}
	}
	entries, err = readHistory(store, "test")
	if err != nil || !reflect.DeepEqual(entries, []string{"SELECT 'first\nline';", "SELECT 100;"}) {
		t.Fatalf("retention: %q %v", entries, err)
	}
	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--config-dir", store.Dir, "history", "test", "--search", "FIRST"})
	if err := cmd.Execute(); err != nil || out.String() != "1  SELECT 'first\nline';\n" {
		t.Fatalf("search %q %v", out.String(), err)
	}
	cmd = NewCommand()
	cmd.SetArgs([]string{"--config-dir", store.Dir, "history", "test", "--clear"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	entries, err = readHistory(store, "test")
	if err != nil || len(entries) != 0 {
		t.Fatalf("clear: %q %v", entries, err)
	}
	info, err := os.Stat(historyPath(store, "test"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("history permissions: %v %v", info, err)
	}
}

type streamingRows struct {
	database.Rows
	n, read int
}

func (r *streamingRows) Columns() ([]string, error) { return []string{"value"}, nil }
func (r *streamingRows) Next() bool {
	if r.read == r.n {
		return false
	}
	r.read++
	return true
}
func (r *streamingRows) Row() (database.Row, error) {
	return database.NewRow([]string{"value"}, []any{r.read}), nil
}
func (*streamingRows) Err() error          { return nil }
func (*streamingRows) Close() error        { return nil }
func (*streamingRows) NextResultSet() bool { return false }

type streamingConnection struct {
	database.Connection
	rows *streamingRows
}

func (c streamingConnection) Query(context.Context, string, ...any) (database.Rows, error) {
	return c.rows, nil
}

type batchWriter struct {
	bytes.Buffer
	rows      *streamingRows
	firstRead int
}

func (w *batchWriter) Write(p []byte) (int, error) {
	if w.Len() == 0 {
		w.firstRead = w.rows.read
	}
	return w.Buffer.Write(p)
}
func TestTableOutputStartsBeforeReadingAllRows(t *testing.T) {
	rows := &streamingRows{n: 1001}
	writer := &batchWriter{rows: rows}
	s := &Session{Conn: streamingConnection{rows: rows}, Timeout: time.Second, BatchSize: 25}
	if err := s.executeArgs(context.Background(), "query", "table", writer); err != nil {
		t.Fatal(err)
	}
	if writer.firstRead != 25 || !strings.Contains(writer.String(), "(1001 rows)") {
		t.Fatalf("first output after %d rows", writer.firstRead)
	}
}

func TestConnectionDiagnosticsDoNotExposeSecrets(t *testing.T) {
	command := func(p Profile) (string, error) {
		c := NewCommand()
		c.SetContext(context.Background())
		var out bytes.Buffer
		c.SetOut(&out)
		err := checkConnection(c, p, "", time.Second)
		text := out.String()
		if err != nil {
			text += err.Error()
		}
		for _, secret := range []string{"super-secret", "sensitive-user", p.DSN} {
			if strings.Contains(text, secret) {
				t.Fatalf("credential leak: %s", text)
			}
		}
		return text, err
	}
	p := Profile{Name: "test", Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "db")}
	if out, err := command(p); err != nil || !strings.Contains(out, "Current database: OK") {
		t.Fatalf("check: %s %v", out, err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	p = Profile{Name: "test", Driver: "postgres", DSN: "postgres://sensitive-user:super-secret@" + address + "/missing?sslmode=disable"}
	if out, err := command(p); err == nil || !strings.Contains(out, "Network: FAIL") {
		t.Fatalf("network: %s %v", out, err)
	}
	// Reject the SOCKS5 method negotiation before any database handshake.
	listener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func(l net.Listener) { _ = l.Close() }(listener)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		buf := make([]byte, 256)
		_, _ = conn.Read(buf)
		_, _ = conn.Write([]byte{5, 255})
	}()
	p.Proxies = []string{"socks5://sensitive-user:super-secret@" + listener.Addr().String()}
	if out, err := command(p); err == nil || !strings.Contains(out, "Proxy hop 1: FAIL") {
		t.Fatalf("proxy: %s %v", out, err)
	}
	<-done
	// A RESP endpoint that rejects authentication exercises the real Redis driver.
	listener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func(l net.Listener) { _ = l.Close() }(listener)
	done = make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		buf := make([]byte, 4096)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
			if _, err := conn.Write([]byte("-WRONGPASS invalid username-password pair\r\n")); err != nil {
				return
			}
		}
	}()
	p = Profile{Name: "test", Driver: "redis", DSN: "redis://sensitive-user:super-secret@" + listener.Addr().String() + "/0"}
	if out, err := command(p); err == nil || !strings.Contains(out, "Authentication: FAIL") {
		t.Fatalf("authentication: %s %v", out, err)
	}
	<-done
}

func TestDiagnosticStagePort401(t *testing.T) {
	netErr := &net.OpError{
		Op:   "dial",
		Net:  "tcp",
		Addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 40123},
		Err:  fmt.Errorf("connect: connection refused"),
	}
	if stage := diagnosticStage(netErr); stage != "Network" {
		t.Fatalf("diagnosticStage(port 40123 OpError) = %q, want Network", stage)
	}
	if reason := diagnosticReason(netErr); !strings.Contains(reason, "connection refused") {
		t.Fatalf("diagnosticReason(port 40123 OpError) = %q", reason)
	}
	authErr := fmt.Errorf("elasticsearch returned status 401")
	if stage := diagnosticStage(authErr); stage != "Authentication" {
		t.Fatalf("diagnosticStage(status 401) = %q, want Authentication", stage)
	}
}

func TestSQLFeaturesOnExternalDrivers(t *testing.T) {
	for _, driver := range []string{"postgres", "mysql"} {
		t.Run(driver, func(t *testing.T) {
			dsn := os.Getenv("DBH_TEST_" + strings.ToUpper(driver) + "_DSN")
			if dsn == "" {
				t.Skip("set the independent test database DSN to run")
			}
			st := Store{Dir: t.TempDir()}
			profile := Profile{Name: "test", Driver: driver, DSN: dsn}
			if err := st.Put(profile, true); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			s, err := openSession(ctx, profile, 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if s.CurrentDatabase == "" {
				t.Fatal("missing initial database")
			}
			table := fmt.Sprintf("dbh feature %d", time.Now().UnixNano())
			quote := `"`
			placeholder := "$1"
			if driver == "mysql" {
				quote = "`"
				placeholder = "?"
			}
			identifier := quote + table + quote
			if err := s.Conn.Exec(ctx, "CREATE TABLE "+identifier+" (id INTEGER PRIMARY KEY, label VARCHAR(100))"); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := s.Conn.Exec(ctx, "DROP TABLE "+identifier); err != nil {
					t.Error(err)
				}
			}()
			run := func(sql string, args ...string) (string, string) {
				t.Helper()
				cmd := NewCommand()
				var out, notice bytes.Buffer
				cmd.SetOut(&out)
				cmd.SetErr(&notice)
				cmd.SetArgs(append([]string{"--config-dir", st.Dir, "exec", "test", sql, "--no-history", "--format", "tsv"}, args...))
				if err := cmd.Execute(); err != nil {
					t.Fatal(err)
				}
				return out.String(), notice.String()
			}
			second := "$2"
			if driver == "mysql" {
				second = "?"
			}
			out, notice := run("INSERT INTO "+identifier+" VALUES("+placeholder+","+second+")", "--param", "1", "--param", `"a'b"`, "--stats")
			if out != "" || !strings.Contains(notice, "1 rows affected") {
				t.Fatalf("write stats: %q %q", out, notice)
			}
			out, _ = run("SELECT label FROM "+identifier+" WHERE id="+placeholder, "--param", "1")
			if out != "label\na'b\n" {
				t.Fatalf("bound value: %q", out)
			}
			out, notice = run("WITH ids AS (SELECT 1 AS id) SELECT label FROM "+identifier+" WHERE id IN (SELECT id FROM ids)", "--stats")
			if out != "label\na'b\n" || !strings.Contains(notice, "1 rows returned") {
				t.Fatalf("CTE query stats: %q %q", out, notice)
			}
			c := &completer{}
			if err := c.refresh(ctx, s); err != nil {
				t.Fatal(err)
			}
			line := "SELECT * FROM " + identifier + " AS u WHERE u."
			got, _ := c.candidates([]rune(line), len([]rune(line)))
			if !reflect.DeepEqual(got, []string{"u.id", "u.label"}) {
				t.Fatalf("cached columns: %q", got)
			}
			var metadata bytes.Buffer
			if err := s.Describe(ctx, identifier, "tsv", &metadata); err != nil || !strings.Contains(metadata.String(), "label") {
				t.Fatalf("metadata: %q %v", metadata.String(), err)
			}
		})
	}
}

func TestHistoryReadsOnlyTheRetainedTail(t *testing.T) {
	for _, trailingNewline := range []bool{false, true} {
		t.Run(fmt.Sprint(trailingNewline), func(t *testing.T) {
			st := Store{Dir: t.TempDir(), HistoryLimit: 2}
			if err := os.MkdirAll(filepath.Join(st.Dir, "history"), 0700); err != nil {
				t.Fatal(err)
			}
			// An old malformed record must not be read when it is outside the limit.
			text := strings.Repeat("old", 30000) + "\n\"SELECT 1;\"\n\"SELECT 'a\\nb';\""
			if trailingNewline {
				text += "\n"
			}
			if err := os.WriteFile(historyPath(st, "test"), []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := readHistory(st, "test")
			if err != nil || !reflect.DeepEqual(got, []string{"SELECT 1;", "SELECT 'a\nb';"}) {
				t.Fatalf("tail: %q %v", got, err)
			}
			st.HistoryLimit = 0
			if _, err := readHistory(st, "test"); err == nil {
				t.Fatal("unlimited read skipped the old record")
			}
		})
	}
}

type failingSwitchDriver struct {
	database.Driver
	switchErr error
}

func (d failingSwitchDriver) SwitchDatabase(context.Context, database.Connection, string, string, database.Dial) (database.Connection, error) {
	return nil, d.switchErr
}

func TestUseDatabaseRedactsProxies(t *testing.T) {
	s := &Session{
		Driver:  "sqlite",
		Timeout: time.Second,
		dsn:     "secret-dsn",
		proxies: []string{"socks5://alice:supersecret@proxy.local:1080"},
		backend: failingSwitchDriver{switchErr: fmt.Errorf("dial socks5://alice:supersecret@proxy.local:1080 via secret-dsn failed")},
	}
	err := s.UseDatabase(context.Background(), "other")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "supersecret") || strings.Contains(err.Error(), "secret-dsn") {
		t.Fatalf("error leaked credentials: %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted DSN]") {
		t.Fatalf("expected redacted DSN marker: %v", err)
	}
}

func TestSubcommandEnhancements(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "app.db")
	run := func(args ...string) (string, error) {
		cmd := NewCommand()
		var out, errBuf bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errBuf)
		cmd.SetArgs(append([]string{"--config-dir", dir}, args...))
		err := cmd.Execute()
		return out.String(), err
	}

	if _, err := run("new", "demo", "--driver", "sqlite", "--dsn", dbPath); err != nil {
		t.Fatal(err)
	}
	if _, err := run("new", "alt", "--driver", "sqlite", "--dsn", filepath.Join(dir, "alt.db")); err != nil {
		t.Fatal(err)
	}

	// list --format json / tsv and --driver filter.
	listed, err := run("list", "--driver", "sqlite", "--format", "tsv")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listed, "name\tdriver\tenv\tread_only\ttags\tproxies\n") || !strings.Contains(listed, "demo\tsqlite\t\tfalse\t\t-\n") {
		t.Fatalf("unexpected list tsv:\n%s", listed)
	}
	if _, err := run("list", "--driver", "unknown"); err == nil {
		t.Fatal("expected unknown driver error")
	}

	// exec alias x and -o atomic file export (no file created on SQL error).
	outFile := filepath.Join(dir, "sub", "users.tsv")
	if _, err := run("x", "demo", "CREATE TABLE users(id INTEGER PRIMARY KEY, name TEXT); INSERT INTO users VALUES (1, 'Alice');"); err != nil {
		t.Fatal(err)
	}
	if out, err := run("x", "demo", "SELECT id, name FROM users;", "--format", "tsv", "-o", outFile); err != nil || out != "" {
		t.Fatalf("exec -o out=%q err=%v", out, err)
	}
	if data, err := os.ReadFile(outFile); err != nil || string(data) != "id\tname\n1\tAlice\n" {
		t.Fatalf("outFile=%q err=%v", string(data), err)
	}
	errFile := filepath.Join(dir, "should_not_exist.tsv")
	if _, err := run("exec", "demo", "SELECT * FROM missing_table;", "-o", errFile); err == nil {
		t.Fatal("expected SQL error")
	}
	if _, err := os.Stat(errFile); !os.IsNotExist(err) {
		t.Fatalf("failed exec must not create %s", errFile)
	}

	// connect --db rejects empty database name.
	if _, err := run("connect", "demo", "--db", "   ", "--sql", "SELECT 1;"); err == nil || !strings.Contains(err.Error(), "database name must not be empty") {
		t.Fatalf("connect --db empty err = %v", err)
	}

	// history --limit and --format json.
	histOut, err := run("history", "demo", "--limit", "1", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(histOut, `"index":4`) || !strings.Contains(histOut, `SELECT * FROM missing_table;`) || strings.Contains(histOut, `CREATE TABLE`) {
		t.Fatalf("unexpected history json:\n%s", histOut)
	}

	// Metadata aliases and distinct descriptions; databases rejects --db.
	tblOut, err := run("tbl", "demo", "--format", "tsv")
	if err != nil || !strings.Contains(tblOut, "name\nusers\n") {
		t.Fatalf("tbl out=%q err=%v", tblOut, err)
	}
	if _, err := run("dbs", "demo", "--db", "main"); err == nil {
		t.Fatal("databases must not accept --db")
	}
	cmd := NewCommand()
	shorts := map[string]string{}
	for _, sub := range cmd.Commands() {
		shorts[sub.Name()] = sub.Short
	}
	if shorts["tables"] == shorts["describe"] || shorts["describe"] == shorts["indexes"] || shorts["indexes"] == shorts["databases"] {
		t.Fatalf("metadata commands must have distinct Short descriptions: %+v", shorts)
	}

	// check --format tsv, check --all, and check --db validation.
	chkOut, err := run("check", "demo", "--format", "tsv")
	if err != nil || !strings.Contains(chkOut, "name\tstage\tstatus\tdetail\telapsed\n") || !strings.Contains(chkOut, "demo\tConfiguration\tOK\t") {
		t.Fatalf("check --format tsv out=%q err=%v", chkOut, err)
	}
	allOut, err := run("check", "--all", "--format", "json")
	if err != nil || !strings.Contains(allOut, `"name":"demo"`) || !strings.Contains(allOut, `"name":"alt"`) {
		t.Fatalf("check --all out=%q err=%v", allOut, err)
	}
	if _, err := run("check", "demo", "--db", "  "); err == nil {
		t.Fatal("check --db empty must fail")
	}
	if _, err := run("check"); err == nil || !strings.Contains(err.Error(), "provide a connection NAME or --all") {
		t.Fatalf("check without args err = %v", err)
	}

	// remove deletes history by default; --keep-history preserves it.
	if _, err := run("exec", "alt", "SELECT 1;"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "history", "alt.jsonl")); err != nil {
		t.Fatalf("expected alt history: %v", err)
	}
	if _, err := run("remove", "alt", "--keep-history"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "history", "alt.jsonl")); err != nil {
		t.Fatalf("--keep-history should preserve alt history: %v", err)
	}
	if _, err := run("remove", "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "history", "demo.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("remove should delete demo history, stat err=%v", err)
	}
}
