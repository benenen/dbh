package tdengine

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/benenen/dbh/internal/database"
)

func TestTDengineDriver(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		sql := string(body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(sql, "SERVER_VERSION"):
			_, _ = io.WriteString(w, `{"code":0,"column_meta":[["server_version()","VARCHAR",16]],"data":[["3.3.0.0"]]}`)
		case strings.Contains(sql, "SHOW DATABASES"):
			_, _ = io.WriteString(w, `{"code":0,"column_meta":[["name","VARCHAR",64]],"data":[["power"]]}`)
		case strings.Contains(sql, "SHOW STABLES"):
			_, _ = io.WriteString(w, `{"code":0,"column_meta":[["stable_name","VARCHAR",64]],"data":[["meters"]]}`)
		case strings.Contains(sql, "SHOW TABLES"):
			_, _ = io.WriteString(w, `{"code":0,"column_meta":[["table_name","VARCHAR",64]],"data":[["d1001"]]}`)
		case strings.Contains(sql, "DESCRIBE"):
			_, _ = io.WriteString(w, `{"code":0,"column_meta":[["field","VARCHAR",64],["type","VARCHAR",16]],"data":[["ts","TIMESTAMP"],["val","FLOAT"]]}`)
		case strings.Contains(sql, "BAD"):
			_, _ = io.WriteString(w, `{"code":9728,"desc":"syntax error"}`)
		default:
			_, _ = io.WriteString(w, `{"code":0,"column_meta":[["val","FLOAT",4]],"data":[[10.5]]}`)
		}
	}))
	defer srv.Close()

	var dialed atomic.Bool
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialed.Store(true)
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}

	d := Driver{}
	ctx := context.Background()
	conn, err := d.Open(ctx, strings.Replace(srv.URL, "http://", "http://root:taosdata@", 1)+"/power", dial)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if !dialed.Load() || !strings.HasPrefix(auth, "Basic ") {
		t.Fatalf("dialed=%v auth=%q", dialed.Load(), auth)
	}
	if dbs, err := d.Databases(ctx, conn); err != nil || len(dbs) != 1 || dbs[0] != "power" {
		t.Fatalf("Databases = %v, %v", dbs, err)
	}
	if tbls, err := d.Tables(ctx, conn); err != nil || len(tbls) != 2 {
		t.Fatalf("Tables = %v, %v", tbls, err)
	}
	if cols, err := d.Columns(ctx, conn, "meters"); err != nil || len(cols) != 2 {
		t.Fatalf("Columns = %v, %v", cols, err)
	}
	if _, err := d.Indexes(ctx, conn, "meters"); err != database.ErrNoIndexes {
		t.Fatalf("Indexes = %v", err)
	}
	if _, err := conn.Query(ctx, "BAD SQL"); err == nil || !strings.Contains(err.Error(), "syntax error") {
		t.Fatalf("expected error, got %v", err)
	}
}
