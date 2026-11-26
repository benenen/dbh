//go:build e2e

package e2e

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestTDengineE2E(t *testing.T) {
	if os.Getenv("DBH_TEST_TDENGINE_DSN") == "" {
		t.Skip("DBH_TEST_TDENGINE_DSN is not configured")
	}

	f := newFixture(t)
	f.run("new", "srv", "--driver", "tdengine", "--dsn-env", "DBH_TEST_TDENGINE_DSN")
	contains(t, f.run("check", "srv"), "OK")

	t.Cleanup(func() {
		f.run("exec", "srv", "DROP DATABASE IF EXISTS dbh_e2e_ts", "--no-history")
	})
	f.run("exec", "srv", "CREATE DATABASE IF NOT EXISTS dbh_e2e_ts", "--no-history")
	f.run("exec", "srv", "--db", "dbh_e2e_ts", "CREATE TABLE items (ts TIMESTAMP, val DOUBLE)", "--no-history")
	f.run("exec", "srv", "--db", "dbh_e2e_ts", "INSERT INTO items VALUES (NOW, 12.5)", "--no-history")
	got := f.run("exec", "srv", "--db", "dbh_e2e_ts", "SELECT val FROM items", "--format", "tsv")
	contains(t, got, "12.5")
	contains(t, f.run("tables", "srv", "--db", "dbh_e2e_ts"), "items")
	contains(t, f.run("describe", "srv", "--db", "dbh_e2e_ts", "items"), "val")
}

func TestTDengineMockE2E(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sql := string(body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(sql, "SERVER_VERSION"):
			_, _ = io.WriteString(w, `{"code":0,"column_meta":[["server_version()","VARCHAR",16]],"data":[["3.3.0.0"]]}`)
		case strings.Contains(sql, "SHOW STABLES"):
			_, _ = io.WriteString(w, `{"code":0,"column_meta":[["stable_name","VARCHAR",64]],"data":[]}`)
		case strings.Contains(sql, "SHOW TABLES"):
			_, _ = io.WriteString(w, `{"code":0,"column_meta":[["table_name","VARCHAR",64]],"data":[["items"]]}`)
		case strings.Contains(sql, "DESCRIBE"):
			_, _ = io.WriteString(w, `{"code":0,"column_meta":[["field","VARCHAR",64],["type","VARCHAR",16]],"data":[["ts","TIMESTAMP"],["val","DOUBLE"]]}`)
		default:
			_, _ = io.WriteString(w, `{"code":0,"column_meta":[["val","DOUBLE",8]],"data":[[12.5]]}`)
		}
	}))
	defer srv.Close()

	f := newFixture(t)
	f.run("new", "td", "--driver", "tdengine", "--dsn", srv.URL+"/power")
	contains(t, f.run("check", "td"), "OK")
	contains(t, f.run("tables", "td"), "items")
	contains(t, f.run("describe", "td", "items"), "val")
	contains(t, f.run("exec", "td", "SELECT val FROM items", "--format", "tsv"), "12.5")
}
