package dbh

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestInferDriver(t *testing.T) {
	tests := []struct {
		target  string
		driver  string
		normDSN string
		wantOK  bool
		wantErr string
	}{
		{"postgres://user:pass@localhost:5432/app", "postgres", "postgres://user:pass@localhost:5432/app", true, ""},
		{"postgresql://localhost/app", "postgres", "postgresql://localhost/app", true, ""},
		{"mysql://user:p%40ss@db.local:3307/app?parseTime=true", "mysql", "user:p@ss@tcp(db.local:3307)/app?parseTime=true", true, ""},
		{"mysql://root@localhost/app", "mysql", "root@tcp(localhost:3306)/app", true, ""},
		{"mysql://[::1]/app", "mysql", "tcp([::1]:3306)/app", true, ""},
		{"root:pass@tcp(127.0.0.1:3306)/app", "mysql", "root:pass@tcp(127.0.0.1:3306)/app", true, ""},
		{"sqlite:///tmp/demo.db", "sqlite", "/tmp/demo.db", true, ""},
		{"sqlite3://./demo.db", "sqlite", "./demo.db", true, ""},
		{"sqlite://:memory:", "sqlite", ":memory:", true, ""},
		{":memory:", "sqlite", ":memory:", true, ""},
		{"file:test.db?mode=ro", "sqlite", "file:test.db?mode=ro", true, ""},
		{"./data/app.db", "sqlite", "./data/app.db", true, ""},
		{"app.sqlite3", "sqlite", "app.sqlite3", true, ""},
		{"./state.bbolt", "bbolt", "bbolt://./state.bbolt", true, ""},
		{"bbolt:///tmp/state.db", "bbolt", "bbolt:///tmp/state.db", true, ""},
		{"sqlserver://sa:pass@localhost:1433?database=app", "mssql", "sqlserver://sa:pass@localhost:1433?database=app", true, ""},
		{"mssql://sa:pass@localhost:1433?database=app", "mssql", "sqlserver://sa:pass@localhost:1433?database=app", true, ""},
		{"oracle://user:pass@localhost:1521/ORCLPDB1", "oracle", "oracle://user:pass@localhost:1521/ORCLPDB1", true, ""},
		{"trino://user@localhost:8080?catalog=memory", "trino", "trino://user@localhost:8080?catalog=memory", true, ""},
		{"dm://SYSDBA:pass@localhost:5236", "dm", "dm://SYSDBA:pass@localhost:5236", true, ""},
		{"mongodb://localhost:27017/app", "mongo", "mongodb://localhost:27017/app", true, ""},
		{"mongodb+srv://cluster.example.com/app", "mongo", "mongodb+srv://cluster.example.com/app", true, ""},
		{"clickhouse://localhost:9000/default", "clickhouse", "clickhouse://localhost:9000/default", true, ""},
		{"redis://localhost:6379/0", "redis", "redis://localhost:6379/0", true, ""},
		{"rediss+cluster://localhost:6379", "redis", "rediss+cluster://localhost:6379", true, ""},
		{"cassandra://localhost:9042/app", "cassandra", "cassandra://localhost:9042/app", true, ""},
		{"scylla://localhost:9042/app", "cassandra", "cassandra://localhost:9042/app", true, ""},
		{"etcd://localhost:2379", "etcd", "etcd://localhost:2379", true, ""},
		{"openobserve://root@localhost:5080/default", "openobserve", "openobserve://root@localhost:5080/default", true, ""},
		{"http://localhost:9200", "", "", false, "ambiguous http(s):// URL"},
		{"https://localhost:6333", "", "", false, "ambiguous http(s):// URL"},
		{"unknown://localhost:1234", "", "", false, "unrecognized URL scheme"},
		{"saved-conn", "", "", false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.target, func(t *testing.T) {
			driver, dsn, ok, err := inferDriver(tc.target)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok != tc.wantOK || driver != tc.driver || dsn != tc.normDSN {
				t.Fatalf("got (%q, %q, %v), want (%q, %q, %v)", driver, dsn, ok, tc.driver, tc.normDSN, tc.wantOK)
			}
		})
	}
}

func TestAdhocDisplayNameRedactsCredentials(t *testing.T) {
	cases := []struct {
		driver string
		dsn    string
		secret string
	}{
		{"postgres", "postgres://alice:s3cr3t@db.internal:5432/app?sslmode=require", "s3cr3t"},
		{"mysql", "alice:s3cr3t@tcp(db.internal:3306)/app", "s3cr3t"},
		{"sqlite", "/tmp/my weird/demo.db", ""},
		{"sqlite", ":memory:", ""},
	}
	for _, tc := range cases {
		name := adhocDisplayName(tc.driver, tc.dsn)
		if !validName.MatchString(name) {
			t.Fatalf("display name %q does not match validName", name)
		}
		if tc.secret != "" && strings.Contains(name, tc.secret) {
			t.Fatalf("display name %q leaked secret %q", name, tc.secret)
		}
	}
}

func TestResolveTargetSavedVsAdhoc(t *testing.T) {
	dir := t.TempDir()
	st := Store{Dir: dir, HistoryLimit: 100}
	savedDB := filepath.Join(dir, "saved.db")
	if err := st.Put(Profile{Name: "my.db", Driver: "sqlite", DSN: savedDB}, true); err != nil {
		t.Fatal(err)
	}

	// Saved profile wins even when name looks like a .db file.
	p, isAdhoc, err := resolveTarget(st, "my.db", adhocOptions{})
	if err != nil {
		t.Fatalf("resolveTarget saved: %v", err)
	}
	if isAdhoc || p.DSN != savedDB {
		t.Fatalf("expected saved profile, got isAdhoc=%v p=%+v", isAdhoc, p)
	}

	// Reject ad-hoc-only flags with saved profile.
	if _, _, err := resolveTarget(st, "my.db", adhocOptions{Driver: "sqlite"}); err == nil {
		t.Fatal("expected error when combining saved profile with --driver")
	}

	// Reject --dsn combined with positional NAME.
	if _, _, err := resolveTarget(st, "my.db", adhocOptions{DSN: ":memory:"}); err == nil {
		t.Fatal("expected error when combining --dsn with positional NAME")
	}

	// Ad-hoc exec via file path does not write connections.json or history files.
	adhocDB := filepath.Join(dir, "adhoc.db")
	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--config-dir", dir, "exec", adhocDB, "CREATE TABLE t (id INT); INSERT INTO t VALUES (1); SELECT id FROM t;", "--format", "tsv"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("adhoc exec failed: %v\noutput: %s", err, out.String())
	}
	if !strings.Contains(out.String(), "id\n1\n") {
		t.Fatalf("unexpected exec output: %q", out.String())
	}

	// Check ad-hoc --dsn on metadata and check commands.
	for _, args := range [][]string{
		{"--config-dir", dir, "tables", "--dsn", adhocDB, "--format", "tsv"},
		{"--config-dir", dir, "describe", "--dsn", adhocDB, "t"},
		{"--config-dir", dir, "check", "--dsn", adhocDB},
	} {
		out.Reset()
		cmd = NewCommand()
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out.String())
		}
	}
}
