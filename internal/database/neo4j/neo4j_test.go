package neo4j

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNeo4jDriver(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Statements []struct {
				Statement string `json:"statement"`
			} `json:"statements"`
		}
		_ = json.Unmarshal(body, &req)
		stmt := ""
		if len(req.Statements) > 0 {
			stmt = req.Statements[0].Statement
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(stmt, "RETURN 1"):
			_, _ = io.WriteString(w, `{"results":[{"columns":["ok"],"data":[{"row":[1]}]}],"errors":[]}`)
		case strings.Contains(stmt, "SHOW DATABASES"):
			_, _ = io.WriteString(w, `{"results":[{"columns":["name"],"data":[{"row":["neo4j"]},{"row":["system"]}]}],"errors":[]}`)
		case strings.Contains(stmt, "db.labels"):
			_, _ = io.WriteString(w, `{"results":[{"columns":["label"],"data":[{"row":["Person"]}]}],"errors":[]}`)
		case strings.Contains(stmt, "UNWIND keys"):
			_, _ = io.WriteString(w, `{"results":[{"columns":["key"],"data":[{"row":["name"]}]}],"errors":[]}`)
		case strings.Contains(stmt, "SHOW INDEXES"):
			_, _ = io.WriteString(w, `{"results":[{"columns":["name","type"],"data":[{"row":["idx_person","RANGE"]}]}],"errors":[]}`)
		case strings.Contains(stmt, "BAD"):
			_, _ = io.WriteString(w, `{"results":[],"errors":[{"code":"Neo.ClientError.Statement.SyntaxError","message":"syntax error"}]}`)
		default:
			_, _ = io.WriteString(w, `{"results":[{"columns":["name"],"data":[{"row":["Alice"]}]}],"errors":[]}`)
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
	conn, err := d.Open(ctx, strings.Replace(srv.URL, "http://", "http://neo4j:secret@", 1)+"/neo4j", dial)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if !dialed.Load() || !strings.HasPrefix(auth, "Basic ") {
		t.Fatalf("dialed=%v auth=%q", dialed.Load(), auth)
	}
	if dbs, err := d.Databases(ctx, conn); err != nil || len(dbs) != 2 {
		t.Fatalf("Databases = %v, %v", dbs, err)
	}
	if lbls, err := d.Tables(ctx, conn); err != nil || len(lbls) != 1 || lbls[0] != "Person" {
		t.Fatalf("Tables = %v, %v", lbls, err)
	}
	if cols, err := d.Columns(ctx, conn, "Person"); err != nil || len(cols) != 1 || cols[0] != "name" {
		t.Fatalf("Columns = %v, %v", cols, err)
	}
	if _, err := d.Indexes(ctx, conn, "Person"); err != nil {
		t.Fatalf("Indexes: %v", err)
	}
	if _, err := conn.Query(ctx, "BAD CYPHER"); err == nil || !strings.Contains(err.Error(), "syntax error") {
		t.Fatalf("expected cypher error, got %v", err)
	}
}
