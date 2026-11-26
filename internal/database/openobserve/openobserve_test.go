package openobserve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseDSN(t *testing.T) {
	cfg, err := parseDSN("openobserve://root@example.com:pass123@127.0.0.1:5080/myorg?stream_type=metrics&tls=false")
	if err != nil {
		t.Fatalf("parseDSN failed: %v", err)
	}
	if cfg.baseURL != "http://127.0.0.1:5080" {
		t.Fatalf("unexpected baseURL: %s", cfg.baseURL)
	}
	if cfg.org != "myorg" || cfg.streamType != "metrics" {
		t.Fatalf("unexpected org/streamType: %s / %s", cfg.org, cfg.streamType)
	}
	if cfg.authHeader == "" {
		t.Fatal("expected authHeader to be populated")
	}
}

func TestOpenObserveDriverOperations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/default/streams":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"list": []map[string]any{
					{
						"name":        "app_logs",
						"stream_type": "logs",
						"stats": map[string]any{
							"doc_num":      1,
							"storage_size": 128,
						},
					},
				},
			})
		case "/api/default/streams/app_logs/schema":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"schema": []map[string]any{
					{"name": "level", "type": "Utf8"},
					{"name": "msg", "type": "Utf8"},
				},
			})
		case "/api/default/app_logs/_json":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 200,
				"status": []map[string]any{
					{"name": "app_logs", "successful": 1, "failed": 0},
				},
			})
		case "/api/default/_search":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"hits": []map[string]any{
					{"level": "info", "msg": "started"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	d := Driver{}
	conn, err := d.Open(context.Background(), srv.URL+"/default", nil)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.Exec(context.Background(), `INGEST app_logs {"level":"info","msg":"started"}`); err != nil {
		t.Fatalf("INGEST failed: %v", err)
	}

	rows, err := conn.Query(context.Background(), `SELECT level, msg FROM app_logs LIMIT 10`)
	if err != nil {
		t.Fatalf("SELECT failed: %v", err)
	}
	if !rows.Next() {
		t.Fatal("expected hit row")
	}
	row, err := rows.Row()
	if err != nil {
		t.Fatalf("Row failed: %v", err)
	}
	if row.Values[0] != "info" {
		t.Fatalf("expected level=info, got %v", row.Values[0])
	}
	_ = rows.Close()

	cols, err := d.Columns(context.Background(), conn, "app_logs")
	if err != nil {
		t.Fatalf("Columns failed: %v", err)
	}
	if len(cols) != 2 || cols[0] != "level" {
		t.Fatalf("unexpected columns: %v", cols)
	}
}
