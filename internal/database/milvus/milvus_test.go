package milvus

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

func TestMilvusDriver(t *testing.T) {
	var lastAuth string
	var lastDBInQuery string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastAuth = r.Header.Get("Authorization")
		bodyBytes, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/v2/vectordb/databases/list":
			_, _ = io.WriteString(w, `{"code":0,"data":["default","analytics"]}`)
		case "/v2/vectordb/collections/list":
			_, _ = io.WriteString(w, `{"code":0,"data":["docs","users"]}`)
		case "/v2/vectordb/collections/describe":
			_, _ = io.WriteString(w, `{"code":0,"data":{"collectionName":"docs","fields":[{"name":"id","type":"Int64","primaryKey":true,"autoId":false},{"name":"embedding","type":"FloatVector","params":[{"key":"dim","value":"768"}]}]}}`)
		case "/v2/vectordb/indexes/list":
			_, _ = io.WriteString(w, `{"code":0,"data":["idx_embedding"]}`)
		case "/v2/vectordb/indexes/describe":
			_, _ = io.WriteString(w, `{"code":0,"data":[{"indexName":"idx_embedding","fieldName":"embedding","indexType":"HNSW","metricType":"COSINE"}]}`)
		case "/v2/vectordb/entities/query":
			var req map[string]any
			_ = json.Unmarshal(bodyBytes, &req)
			lastDBInQuery, _ = req["dbName"].(string)
			_, _ = io.WriteString(w, `{"code":0,"data":[{"id":1,"title":"hello"}]}`)
		case "/v2/vectordb/entities/search":
			_, _ = io.WriteString(w, `{"code":100,"message":"collection not loaded"}`)
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
	conn, err := d.Open(ctx, srv.URL+"/analytics?token=secret-token", dial)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if !dialed.Load() {
		t.Fatal("expected dial to be called")
	}
	if lastAuth != "Bearer secret-token" {
		t.Fatalf("Authorization = %q", lastAuth)
	}

	cur, err := d.CurrentDatabase(ctx, conn)
	if err != nil || cur != "analytics" {
		t.Fatalf("CurrentDatabase = %q, %v", cur, err)
	}

	dbs, err := d.Databases(ctx, conn)
	if err != nil || len(dbs) != 2 || dbs[0] != "analytics" || dbs[1] != "default" {
		t.Fatalf("Databases = %v, %v", dbs, err)
	}

	cols, err := d.Tables(ctx, conn)
	if err != nil || len(cols) != 2 || cols[0] != "docs" || cols[1] != "users" {
		t.Fatalf("Tables = %v, %v", cols, err)
	}

	fields, err := d.Columns(ctx, conn, "docs")
	if err != nil || len(fields) != 2 || fields[0] != "id" || fields[1] != "embedding" {
		t.Fatalf("Columns = %v, %v", fields, err)
	}

	q, err := d.ColumnDetails(ctx, conn, "docs")
	if err != nil {
		t.Fatalf("ColumnDetails: %v", err)
	}
	detailRows, err := conn.Query(ctx, q.Text)
	if err != nil {
		t.Fatalf("ColumnDetails query: %v", err)
	}
	_ = detailRows.Close()

	iq, err := d.Indexes(ctx, conn, "docs")
	if err != nil {
		t.Fatalf("Indexes: %v", err)
	}
	idxRows, err := conn.Query(ctx, iq.Text)
	if err != nil || !idxRows.Next() {
		t.Fatalf("Indexes query: %v", err)
	}
	_ = idxRows.Close()

	// Switch database and verify dbName injection into relative command
	if _, err := d.SwitchDatabase(ctx, conn, "", "analytics", nil); err != nil {
		t.Fatalf("SwitchDatabase: %v", err)
	}
	rows, err := conn.Query(ctx, `POST entities/query {"collectionName":"docs","filter":"id > 0"}`)
	if err != nil {
		t.Fatalf("entities/query: %v", err)
	}
	if lastDBInQuery != "analytics" {
		t.Fatalf("injected dbName = %q, want analytics", lastDBInQuery)
	}
	if !rows.Next() {
		t.Fatal("expected 1 row")
	}
	row, err := rows.Row()
	if err != nil {
		t.Fatalf("row err = %v", err)
	}
	obj, ok := row.Object.(map[string]any)
	if !ok || obj["title"] != "hello" {
		t.Fatalf("row = %+v", row)
	}
	_ = rows.Close()

	// Milvus error code handling
	if _, err := conn.Query(ctx, `POST /v2/vectordb/entities/search {"collectionName":"docs"}`); err == nil || !strings.Contains(err.Error(), "collection not loaded") {
		t.Fatalf("expected milvus error, got %v", err)
	}
}
