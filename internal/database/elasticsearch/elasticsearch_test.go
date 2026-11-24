package elasticsearch

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

	"github.com/benenen/dbh/internal/database"
)

func TestElasticsearchDriver(t *testing.T) {
	var authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/":
			_, _ = io.WriteString(w, `{"name":"node-1","version":{"number":"8.15.0"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/_cat/indices":
			_, _ = io.WriteString(w, `[{"index":"logs"},{"index":"users"}]`)
		case r.Method == http.MethodHead && r.URL.Path == "/users":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/users/_mapping":
			_, _ = io.WriteString(w, `{"users":{"mappings":{"properties":{"name":{"type":"text"},"profile":{"properties":{"age":{"type":"integer"}}}}}}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/users/_search":
			_, _ = io.WriteString(w, `{"hits":{"hits":[{"_index":"users","_id":"1","_score":1.0,"_source":{"name":"Alice"}}]}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/_sql":
			_, _ = io.WriteString(w, `{"columns":[{"name":"name","type":"text"}],"rows":[["Alice"]]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/missing":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"type":"index_not_found_exception","reason":"no such index [missing]"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
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
	conn, err := d.Open(ctx, srv.URL+"/users?api_key=secret-key", dial)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if !dialed.Load() {
		t.Fatal("expected dial to be called")
	}
	if authHeader != "ApiKey secret-key" {
		t.Fatalf("Authorization = %q", authHeader)
	}

	cur, err := d.CurrentDatabase(ctx, conn)
	if err != nil || cur != "users" {
		t.Fatalf("CurrentDatabase = %q, %v", cur, err)
	}
	if _, err := d.SwitchDatabase(ctx, conn, "", "logs", nil); err != nil {
		t.Fatalf("SwitchDatabase: %v", err)
	}
	cur, _ = d.CurrentDatabase(ctx, conn)
	if cur != "logs" {
		t.Fatalf("after switch = %q", cur)
	}

	tables, err := d.Tables(ctx, conn)
	if err != nil || len(tables) != 2 || tables[0] != "logs" || tables[1] != "users" {
		t.Fatalf("Tables = %v, %v", tables, err)
	}

	cols, err := d.Columns(ctx, conn, "users")
	if err != nil || len(cols) != 3 {
		t.Fatalf("Columns = %v, %v", cols, err)
	}

	q, err := d.ColumnDetails(ctx, conn, "users")
	if err != nil {
		t.Fatalf("ColumnDetails: %v", err)
	}
	detailRows, err := conn.Query(ctx, q.Text)
	if err != nil {
		t.Fatalf("ColumnDetails query: %v", err)
	}
	_ = detailRows.Close()

	if _, err := d.Indexes(ctx, conn, "users"); err != database.ErrNoIndexes {
		t.Fatalf("Indexes err = %v", err)
	}

	// Search query (resolved relative to /users)
	if _, err := d.SwitchDatabase(ctx, conn, "", "users", nil); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.Query(ctx, `POST _search {"query":{"match_all":{}}}`)
	if err != nil {
		t.Fatalf("search Query: %v", err)
	}
	if !rows.Next() {
		t.Fatal("expected 1 search hit")
	}
	row, err := rows.Row()
	if err != nil {
		t.Fatalf("Row: %v", err)
	}
	b, _ := json.Marshal(row.Object)
	if !strings.Contains(string(b), `"Alice"`) {
		t.Fatalf("row object = %s", b)
	}
	_ = rows.Close()

	// ES SQL query
	sqlRows, err := conn.Query(ctx, `POST /_sql {"query":"SELECT name FROM users"}`)
	if err != nil {
		t.Fatalf("sql Query: %v", err)
	}
	if !sqlRows.Next() {
		t.Fatal("expected 1 sql row")
	}
	_ = sqlRows.Close()

	// Error response
	if _, err := conn.Query(ctx, `GET /missing`); err == nil || !strings.Contains(err.Error(), "index_not_found_exception") {
		t.Fatalf("expected ES error, got %v", err)
	}
}
