package qdrant

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestQdrantDriver(t *testing.T) {
	var lastKey string
	var lastPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastKey = r.Header.Get("api-key")
		lastPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/collections":
			_, _ = io.WriteString(w, `{"result":{"collections":[{"name":"docs"},{"name":"items"}]},"status":"ok"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/collections/docs":
			_, _ = io.WriteString(w, `{"result":{"config":{"params":{"vectors":{"size":4,"distance":"Cosine"}}},"payload_schema":{"tag":{"data_type":"keyword","points":1}}},"status":"ok"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/collections/docs/points/scroll":
			_, _ = io.WriteString(w, `{"result":{"points":[{"id":1,"payload":{"tag":"go"}}]},"status":"ok"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"status":{"error":"not found"}}`)
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
	conn, err := d.Open(ctx, srv.URL+"/docs?api_key=qkey", dial)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if !dialed.Load() || lastKey != "qkey" {
		t.Fatalf("dialed=%v key=%q", dialed.Load(), lastKey)
	}
	if cur, err := d.CurrentDatabase(ctx, conn); err != nil || cur != "docs" {
		t.Fatalf("CurrentDatabase = %q, %v", cur, err)
	}
	if cols, err := d.Tables(ctx, conn); err != nil || len(cols) != 2 {
		t.Fatalf("Tables = %v, %v", cols, err)
	}
	if fields, err := d.Columns(ctx, conn, "docs"); err != nil || len(fields) != 2 {
		t.Fatalf("Columns = %v, %v", fields, err)
	}
	if q, err := d.ColumnDetails(ctx, conn, "docs"); err != nil || q.Text == "" {
		t.Fatalf("ColumnDetails = %+v, %v", q, err)
	}
	if q, err := d.Indexes(ctx, conn, "docs"); err != nil || q.Text == "" {
		t.Fatalf("Indexes = %+v, %v", q, err)
	}

	rows, err := conn.Query(ctx, `POST points/scroll {"limit":10}`)
	if err != nil {
		t.Fatalf("scroll Query: %v", err)
	}
	if lastPath != "/collections/docs/points/scroll" || !rows.Next() {
		t.Fatalf("lastPath=%q", lastPath)
	}
	row, err := rows.Row()
	if err != nil {
		t.Fatalf("Row: %v", err)
	}
	if obj, ok := row.Object.(map[string]any); !ok || obj["id"] == nil {
		t.Fatalf("row = %+v", row)
	}
	_ = rows.Close()

	if _, err := conn.Query(ctx, `GET /missing`); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected error, got %v", err)
	}
}
